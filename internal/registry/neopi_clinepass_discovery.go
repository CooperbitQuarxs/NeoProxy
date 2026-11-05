package registry

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
)

// NeoPi's ClinePass discovery (pi-catalog provider-models/openai-compat.ts
// fetchClinePassModels). Cline has no OpenAI-style /models route: its
// recommended-models endpoint is the authoritative roster, a `clinePass`
// bucket of subscription ids (wire ids under "cline-pass/") and an optional
// `free` bucket of $0 ids on the same key. Metadata resolves from Cline's
// curated table, then NeoPi's bundled proxy-reference index, then OpenRouter's
// public catalog (the enrichment Cline's own client uses), then conservative
// constants. models/neopi_discovery/cline_pass.json.gz carries the curated
// table and the reference index exactly as NeoPi builds them.

//go:embed models/neopi_discovery/cline_pass.json.gz
var neoPiClinePassGz []byte

const (
	// NeoPiClinePassModelsURL is CLINEPASS_MODELS_URL.
	NeoPiClinePassModelsURL = "https://api.cline.bot/api/v1/ai/cline/recommended-models"
	// NeoPiClinePassLiveCatalogURL is CLINE_PASS_LIVE_CATALOG_URL.
	NeoPiClinePassLiveCatalogURL = "https://openrouter.ai/api/v1/models"

	neoPiClinePassProvider              = "cline-pass"
	neoPiClinePassWirePrefix            = "cline-pass/"
	neoPiClinePassFallbackContextWindow = 128_000
	neoPiClinePassFallbackMaxTokens     = 8_192
)

type neoPiClinePassMetadata struct {
	Name          string          `json:"name"`
	ContextWindow int             `json:"contextWindow"`
	MaxTokens     int             `json:"maxTokens"`
	Input         []string        `json:"input"`
	Reasoning     bool            `json:"reasoning"`
	Thinking      json.RawMessage `json:"thinking"`
	Tier          string          `json:"tier"`
}

// thinking is the curated ladder and whether it authors requiresEffort.
func (m neoPiClinePassMetadata) thinking() (*NeoPiThinking, bool) {
	thinking, requiresEffortSet, err := neoPiDecodeThinking(m.Thinking)
	if err != nil {
		return nil, false
	}
	return thinking, requiresEffortSet
}

// neoPiReferenceRow is the part of a bundled upstream row discovery reads
// (ClinePass, GMI Cloud). A zero limit is NeoPi's undefined.
type neoPiReferenceRow struct {
	Provider      string          `json:"provider"`
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Reasoning     bool            `json:"reasoning"`
	Input         []string        `json:"input"`
	ContextWindow int             `json:"contextWindow"`
	MaxTokens     int             `json:"maxTokens"`
	Thinking      json.RawMessage `json:"thinking"`
}

type neoPiClinePassData struct {
	Metadata  map[string]neoPiClinePassMetadata `json:"metadata"`
	Reference struct {
		MarkerSuffixes []string             `json:"markerSuffixes"`
		Exact          map[string]int       `json:"exact"`
		SuffixAlias    map[string]int       `json:"suffixAlias"`
		Rows           []*neoPiReferenceRow `json:"rows"`
	} `json:"reference"`
}

var (
	neoPiClinePassOnce sync.Once
	neoPiClinePass     *neoPiClinePassData
)

func neoPiClinePassTables() *neoPiClinePassData {
	neoPiClinePassOnce.Do(func() {
		neoPiClinePass = &neoPiClinePassData{}
		reader, err := gzip.NewReader(bytes.NewReader(neoPiClinePassGz))
		if err != nil {
			return
		}
		defer func() { _ = reader.Close() }()
		data, err := io.ReadAll(reader)
		if err != nil {
			return
		}
		_ = json.Unmarshal(data, neoPiClinePass)
	})
	return neoPiClinePass
}

// neoPiClinePassLive is one ClinePassLiveCatalogEntry; hasReasoning and a nil
// input are its absent fields.
type neoPiClinePassLive struct {
	name          string
	contextWindow int
	maxTokens     int
	reasoning     bool
	hasReasoning  bool
	input         []string
}

type neoPiClinePassLiveCatalog struct {
	byID   map[string]*neoPiClinePassLive
	bySlug map[string]*neoPiClinePassLive
}

// neoPiJSSpace is ECMAScript's \s; RE2's \s is ASCII-only.
const neoPiJSSpace = `[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

var neoPiLiveNamePrefix = regexp.MustCompile(`^[^:]+:` + neoPiJSSpace + `*`)

// neoPiParseClinePassLiveCatalog is fetchClinePassLiveCatalog's parse of
// OpenRouter's /models: nil when the body is unusable, which NeoPi treats as
// no enrichment rather than a failure.
func neoPiParseClinePassLiveCatalog(body []byte) *neoPiClinePassLiveCatalog {
	if !gjson.ValidBytes(body) {
		return nil
	}
	payload := gjson.ParseBytes(body)
	data := payload.Get("data")
	if !payload.IsObject() || !data.IsArray() {
		return nil
	}
	catalog := &neoPiClinePassLiveCatalog{byID: map[string]*neoPiClinePassLive{}, bySlug: map[string]*neoPiClinePassLive{}}
	for _, raw := range data.Array() {
		id := raw.Get("id")
		if !raw.IsObject() || id.Type != gjson.String || id.String() == "" {
			continue
		}
		entry := &neoPiClinePassLive{}
		if name := raw.Get("name"); name.Type == gjson.String && name.String() != "" {
			entry.name = neoPiLiveNamePrefix.ReplaceAllString(name.String(), "")
		}
		entry.contextWindow = neoPiPositive(raw.Get("context_length"), 0)
		if top := raw.Get("top_provider"); top.IsObject() {
			entry.maxTokens = neoPiPositive(top.Get("max_completion_tokens"), 0)
		}
		if params := raw.Get("supported_parameters"); params.IsArray() {
			entry.hasReasoning = true
			for _, param := range params.Array() {
				if param.Type == gjson.String && param.String() == "reasoning" {
					entry.reasoning = true
				}
			}
		}
		if architecture := raw.Get("architecture"); architecture.IsObject() {
			if modality := neoPiJSString(architecture.Get("modality")); modality != "" {
				entry.input = []string{"text"}
				if strings.Contains(modality, "image") {
					entry.input = []string{"text", "image"}
				}
			}
		}
		catalog.byID[id.String()] = entry
		// Cline's buildModelsNameMap: roster ids are lab-less slugs, so the
		// catalog is also keyed by the id's last segment.
		if slug := neoPiLastSegment(id.String()); slug != "" {
			catalog.bySlug[slug] = entry
		}
	}
	if len(catalog.byID) == 0 {
		return nil
	}
	return catalog
}

// neoPiJSString is String(value ?? "") for a JSON value.
func neoPiJSString(value gjson.Result) string {
	switch value.Type {
	case gjson.Null:
		return ""
	case gjson.String:
		return value.String()
	case gjson.True:
		return "true"
	case gjson.False:
		return "false"
	case gjson.Number:
		return value.Raw
	}
	if !value.Exists() {
		return ""
	}
	if value.IsArray() {
		parts := make([]string, 0)
		for _, item := range value.Array() {
			parts = append(parts, neoPiJSString(item))
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

func neoPiLastSegment(id string) string {
	return id[strings.LastIndex(id, "/")+1:]
}

// NeoPiClinePassListing resolves Cline's recommended-models body, beside
// OpenRouter's catalog body (nil when that fetch failed), into the roster
// NeoPi's ClinePass model manager produces. A roster NeoPi rejects is an
// error; NeoPi then serves the bundled roster.
func NeoPiClinePassListing(baseURL string, roster, live []byte) ([]*ModelInfo, error) {
	if !gjson.ValidBytes(roster) {
		return nil, ErrNeoPiListing
	}
	payload := gjson.ParseBytes(roster)
	pass := payload.Get("clinePass")
	if !payload.IsObject() || !pass.IsArray() {
		return nil, ErrNeoPiListing
	}
	var catalog *neoPiClinePassLiveCatalog
	if live != nil {
		catalog = neoPiParseClinePassLiveCatalog(live)
	}
	specs := []neoPiSpec{}
	index := map[string]int{}
	put := func(spec neoPiSpec) {
		if at, ok := index[spec.id]; ok {
			specs[at] = spec
			return
		}
		index[spec.id] = len(specs)
		specs = append(specs, spec)
	}
	for _, entry := range pass.Array() {
		id := entry.Get("id")
		if !entry.IsObject() || id.Type != gjson.String {
			continue
		}
		wireID := neoPiJSTrim(id.String())
		if !strings.HasPrefix(wireID, neoPiClinePassWirePrefix) {
			continue
		}
		public := neoPiJSTrim(strings.TrimPrefix(wireID, neoPiClinePassWirePrefix))
		if public == "" {
			continue
		}
		put(neoPiClinePassSubscriptionSpec(public, catalog))
	}
	if len(specs) == 0 {
		return nil, ErrNeoPiListing
	}
	if free := payload.Get("free"); free.IsArray() {
		for _, entry := range free.Array() {
			id := entry.Get("id")
			if !entry.IsObject() || id.Type != gjson.String {
				continue
			}
			public := neoPiJSTrim(id.String())
			if _, taken := index[public]; public == "" || strings.HasPrefix(public, neoPiClinePassWirePrefix) || taken {
				continue
			}
			put(neoPiClinePassFreeSpec(public, catalog))
		}
	}
	return neoPiMergeListing(neoPiClinePassProvider, neoPiNormalizeBase(baseURL), specs, true), nil
}

// neoPiClinePassFallbackSpec is buildClinePassFallbackModel.
func neoPiClinePassFallbackSpec(id string) neoPiSpec {
	return neoPiSpec{id: id, name: id, reasoning: true, input: []string{"text"},
		contextWindow: neoPiClinePassFallbackContextWindow, maxTokens: neoPiClinePassFallbackMaxTokens,
		compat: map[string]any{"supportsReasoningEffort": false}}
}

// neoPiClinePassCuratedSpec is buildCuratedClinePassModel.
func neoPiClinePassCuratedSpec(id, tier string) (neoPiSpec, bool) {
	metadata, ok := neoPiClinePassTables().Metadata[id]
	if !ok || metadata.Tier != tier {
		return neoPiSpec{}, false
	}
	spec := neoPiClinePassFallbackSpec(id)
	spec.name = metadata.Name
	spec.contextWindow = metadata.ContextWindow
	spec.maxTokens = metadata.MaxTokens
	spec.input = slices.Clone(metadata.Input)
	spec.reasoning = metadata.Reasoning
	spec.thinking, spec.thinkingRequiresEffortSet = metadata.thinking()
	spec.compat = map[string]any{}
	if spec.thinking == nil {
		spec.compat["supportsReasoningEffort"] = false
	}
	if tier == "free" {
		spec.compat["wireModelIdMode"] = "raw"
	}
	return spec, true
}

// neoPiClinePassBundledSpec is the bundled row as the spec it was built from
// (toModelSpec: its ladder and authored compat).
func neoPiClinePassBundledSpec(row *NeoPiModel) neoPiSpec {
	return neoPiSpec{id: row.ID, name: row.Name, reasoning: row.Reasoning, input: slices.Clone(row.Input),
		contextWindow: row.ContextWindow, maxTokens: row.MaxTokens, thinking: row.Thinking.Clone(), compat: maps.Clone(row.CompatConfig),
		thinkingRequiresEffortSet: row.thinkingRequiresEffortSet}
}

// neoPiClinePassSubscriptionSpec is buildClinePassSubscriptionModel.
func neoPiClinePassSubscriptionSpec(id string, live *neoPiClinePassLiveCatalog) neoPiSpec {
	if curated, ok := neoPiClinePassCuratedSpec(id, "subscription"); ok {
		return curated
	}
	bundled := neoPiBundledRow(neoPiClinePassProvider, id)
	base := neoPiClinePassFallbackSpec(id)
	if bundled != nil {
		base = neoPiClinePassBundledSpec(bundled)
	}
	// A reference back into the ClinePass bundle means the last regen knew
	// nothing about the id; the same holds for a bundled row that carries the
	// fallback limits.
	upstream := neoPiResolveModelReference(id)
	if upstream != nil && upstream.Provider == neoPiClinePassProvider {
		upstream = nil
	}
	fallbackLimits := base.contextWindow == neoPiClinePassFallbackContextWindow && base.maxTokens == neoPiClinePassFallbackMaxTokens
	if upstream != nil {
		if bundled != nil && !fallbackLimits {
			// Known with real limits: only the list price changes.
			return base
		}
		if bundled == nil {
			base.name = upstream.Name
		}
		base.reasoning = upstream.Reasoning
		base.input = slices.Clone(upstream.Input)
		base.contextWindow = upstream.ContextWindow
		base.maxTokens = upstream.MaxTokens
		if !upstream.Reasoning {
			base.thinking = nil
		}
		return base
	}
	var entry *neoPiClinePassLive
	if live != nil {
		entry = live.bySlug[id]
	}
	if entry == nil {
		return base
	}
	neoPiApplyClinePassLive(&base, entry)
	return base
}

func neoPiApplyClinePassLive(spec *neoPiSpec, entry *neoPiClinePassLive) {
	if entry.name != "" {
		spec.name = entry.name
	}
	if entry.contextWindow > 0 {
		spec.contextWindow = entry.contextWindow
	}
	if entry.maxTokens > 0 {
		spec.maxTokens = entry.maxTokens
	}
	if entry.hasReasoning {
		spec.reasoning = entry.reasoning
		if !entry.reasoning {
			spec.thinking = nil
		}
	}
	if entry.input != nil {
		spec.input = slices.Clone(entry.input)
	}
}

var (
	neoPiFreeNameMarker = regexp.MustCompile(`(?i)` + neoPiJSSpace + `*\(free\)` + neoPiJSSpace + `*$`)
	neoPiFreeIDMarker   = regexp.MustCompile(`(?i):free$`)
)

// neoPiClinePassFreeSpec is buildClinePassFreeModel: free ids are
// OpenRouter-native, so their reference and live entry resolve from the full
// id, and the gateway already namespaces them on the wire.
func neoPiClinePassFreeSpec(id string, live *neoPiClinePassLiveCatalog) neoPiSpec {
	if curated, ok := neoPiClinePassCuratedSpec(id, "free"); ok {
		return curated
	}
	upstream := neoPiResolveModelReference(id)
	if upstream != nil && upstream.Provider == neoPiClinePassProvider {
		upstream = nil
	}
	var entry *neoPiClinePassLive
	if live != nil {
		if entry = live.byID[id]; entry == nil {
			entry = live.bySlug[neoPiLastSegment(id)]
		}
	}
	baseName := neoPiLastSegment(id)
	switch {
	case upstream != nil:
		baseName = upstream.Name
	case entry != nil && entry.name != "":
		baseName = entry.name
	}
	baseName = neoPiJSTrim(neoPiFreeIDMarker.ReplaceAllString(neoPiFreeNameMarker.ReplaceAllString(baseName, ""), ""))
	if baseName == "" {
		baseName = id
	}
	spec := neoPiClinePassFallbackSpec(id)
	spec.name = baseName + " (free)"
	switch {
	case upstream != nil:
		spec.reasoning = upstream.Reasoning
		spec.input = slices.Clone(upstream.Input)
	case entry != nil:
		if entry.hasReasoning {
			spec.reasoning = entry.reasoning
		}
		if entry.input != nil {
			spec.input = slices.Clone(entry.input)
		}
	}
	// Limits fall through tier by tier: a reference without a limit defers
	// to the live entry, then to the constants.
	spec.contextWindow = neoPiFirstLimit(neoPiReferenceLimit(upstream, true), neoPiLiveLimit(entry, true), neoPiClinePassFallbackContextWindow)
	spec.maxTokens = neoPiFirstLimit(neoPiReferenceLimit(upstream, false), neoPiLiveLimit(entry, false), neoPiClinePassFallbackMaxTokens)
	spec.withCompat("wireModelIdMode", "raw")
	return spec
}

func neoPiReferenceLimit(row *neoPiReferenceRow, context bool) int {
	if row == nil {
		return 0
	}
	if context {
		return row.ContextWindow
	}
	return row.MaxTokens
}

func neoPiLiveLimit(entry *neoPiClinePassLive, context bool) int {
	if entry == nil {
		return 0
	}
	if context {
		return entry.contextWindow
	}
	return entry.maxTokens
}

func neoPiFirstLimit(limits ...int) int {
	for _, limit := range limits {
		if limit > 0 {
			return limit
		}
	}
	return 0
}

// neoPiResolveModelReference is resolveModelReference over NeoPi's bundled
// reference index: the first candidate spelling of id that names a bundled
// upstream row. Gateway ids ("@provider/model") are opaque.
func neoPiResolveModelReference(id string) *neoPiReferenceRow {
	if strings.HasPrefix(id, "@") {
		return nil
	}
	tables := neoPiClinePassTables()
	for _, candidate := range neoPiReferenceCandidateIDs(id) {
		key := strings.ToLower(neoPiJSTrim(candidate))
		at, ok := tables.Reference.Exact[key]
		if !ok {
			at, ok = tables.Reference.SuffixAlias[key]
		}
		if ok && at >= 0 && at < len(tables.Reference.Rows) {
			return tables.Reference.Rows[at]
		}
	}
	return nil
}

// neoPiReferenceCandidateIDs is getReferenceCandidateIds: the spellings a
// proxied or affixed id may be catalogued under, least stripped first.
func neoPiReferenceCandidateIDs(id string) []string {
	seen := map[string]bool{}
	var candidates []string
	queue := []string{id}
	for i := 0; i < len(queue); i++ {
		candidate := neoPiJSTrim(queue[i])
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		candidates = append(candidates, candidate)
		queue = append(queue, neoPiBracketStrippedCandidates(candidate)...)
		queue = append(queue, neoPiModelLikeSegments(candidate)...)
		lower := strings.ToLower(candidate)
		for _, suffix := range []string{":cloud", "-cloud"} {
			if strings.HasSuffix(lower, suffix) {
				queue = append(queue, candidate[:len(candidate)-len(suffix)])
			}
		}
		if slash := strings.LastIndex(candidate, "/"); slash != -1 {
			queue = append(queue, candidate[slash+1:])
		}
		if dashed := strings.ReplaceAll(candidate, ":", "-"); dashed != candidate {
			queue = append(queue, dashed)
		}
		if lower != candidate {
			queue = append(queue, lower)
		}
		if stripped, ok := neoPiStripReferenceMarker(candidate); ok {
			queue = append(queue, stripped)
		}
	}
	return candidates
}

// neoPiStripReferenceMarker is stripReferenceTrailingMarker: the longest
// declared billing, routing or thinking marker off the end.
func neoPiStripReferenceMarker(candidate string) (string, bool) {
	lower := strings.ToLower(candidate)
	longest := 0
	for _, suffix := range neoPiClinePassTables().Reference.MarkerSuffixes {
		if strings.HasSuffix(lower, suffix) && len(suffix) > longest {
			longest = len(suffix)
		}
	}
	if longest == 0 || longest >= len(candidate) {
		return "", false
	}
	return candidate[:len(candidate)-longest], true
}

var (
	neoPiModelIDSegment       = regexp.MustCompile(`[a-z0-9.:-]+`)
	neoPiModelFamilyPrefix    = regexp.MustCompile(`(?i)^(claude|gemini|gpt|grok|glm|qwen|deepseek|kimi|mimo|doubao|ernie|gpt-oss|gemma|minimax|step|command|jamba|llama|o[1345])`)
	neoPiWhitespaceRun        = regexp.MustCompile(neoPiJSSpace + `+`)
	neoPiLeadingBracketAffix  = regexp.MustCompile(`^(?:` + neoPiJSSpace + `*(?:\[|【)[^\]】]+(?:\]|】)` + neoPiJSSpace + `*)+`)
	neoPiTrailingBracketAffix = regexp.MustCompile(`(?:` + neoPiJSSpace + `*(?:\[|【)[^\]】]+(?:\]|】)` + neoPiJSSpace + `*)+$`)
)

func neoPiNormalizeIDWhitespace(value string) string {
	return neoPiWhitespaceRun.ReplaceAllString(neoPiJSTrim(value), " ")
}

// neoPiModelLikeSegments is getModelLikeIdSegments: the id's lowercase
// segments that start like a model family and carry a digit, longest first.
func neoPiModelLikeSegments(id string) []string {
	var segments []string
	for _, segment := range neoPiModelIDSegment.FindAllString(strings.ToLower(neoPiNormalizeIDWhitespace(id)), -1) {
		if neoPiModelFamilyPrefix.MatchString(segment) && strings.ContainsAny(segment, "0123456789") && !slices.Contains(segments, segment) {
			segments = append(segments, segment)
		}
	}
	sort.SliceStable(segments, func(i, j int) bool { return neoPiCompareSegments(segments[i], segments[j]) < 0 })
	return segments
}

// neoPiCompareSegments is compareSegmentPreference: longer first, then
// localeCompare, whose root collation orders these characters
// '-' < ':' < '.' < digits < letters.
func neoPiCompareSegments(left, right string) int {
	if len(left) != len(right) {
		return len(right) - len(left)
	}
	rank := func(c byte) int {
		switch {
		case c == '-':
			return 0
		case c == ':':
			return 1
		case c == '.':
			return 2
		case c >= '0' && c <= '9':
			return 3 + int(c-'0')
		default:
			return 13 + int(c)
		}
	}
	for i := 0; i < len(left); i++ {
		if a, b := rank(left[i]), rank(right[i]); a != b {
			return a - b
		}
	}
	return 0
}

// neoPiBracketStrippedCandidates is getBracketStrippedModelIdCandidates:
// the id without reseller tags in brackets, both ends first.
func neoPiBracketStrippedCandidates(id string) []string {
	if !strings.ContainsAny(id, "[]【】") {
		return nil
	}
	normalized := neoPiNormalizeIDWhitespace(id)
	if normalized == "" {
		return nil
	}
	strippedLeading := neoPiLeadingBracketAffix.ReplaceAllString(normalized, "")
	options := []string{
		neoPiNormalizeIDWhitespace(neoPiTrailingBracketAffix.ReplaceAllString(strippedLeading, "")),
		neoPiNormalizeIDWhitespace(strippedLeading),
		neoPiNormalizeIDWhitespace(neoPiTrailingBracketAffix.ReplaceAllString(normalized, "")),
	}
	var out []string
	for _, option := range options {
		if option != "" && option != normalized && !slices.Contains(out, option) {
			out = append(out, option)
		}
	}
	return out
}

// neoPiJSTrim is String.prototype.trim.
func neoPiJSTrim(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}
