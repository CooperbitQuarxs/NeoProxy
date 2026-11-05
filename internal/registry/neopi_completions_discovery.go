package registry

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
	"github.com/tidwall/gjson"
)

// NeoPi's live model discovery for the providers it serves through its
// OpenAI Chat Completions provider. Each provider's ModelManagerOptions
// (pi-catalog provider-models/openai-compat.ts) fetches a listing, maps every
// entry onto a spec, and the model manager merges the specs into the bundled
// roster (mergeDynamicModel). This file ports the mapping and the merge; the
// HTTP request lives with the caller. Parity against NeoPi's own managers is
// captured by testdata/capture_neopi_completions_discovery.ts.
//
// Rows NeoPi's rule engine derives for an id the bundle lacks (effort ladder,
// compat) cannot be recomputed here, so models/neopi_discovery/derived.json.gz
// carries what NeoPi derives for each kind of row a mapper can switch
// reasoning on for.

//go:embed models/neopi_discovery/derived.json.gz
var neoPiDiscoveryDerivedGz []byte

// ErrNeoPiListing marks a listing NeoPi would treat as a failed discovery:
// its manager then keeps the bundled roster.
var ErrNeoPiListing = errors.New("registry: unusable model listing")

type neoPiDerivedRow struct {
	Provider string                  `json:"provider"`
	Kind     string                  `json:"kind"`
	Thinking *NeoPiThinking          `json:"thinking"`
	Compat   *NeoPiCompletionsCompat `json:"compat"`
}

var (
	neoPiDerivedOnce sync.Once
	neoPiDerivedRows map[string]*neoPiDerivedRow
)

func neoPiDerivedFor(provider, kind string) *neoPiDerivedRow {
	neoPiDerivedOnce.Do(func() {
		neoPiDerivedRows = map[string]*neoPiDerivedRow{}
		reader, err := gzip.NewReader(bytes.NewReader(neoPiDiscoveryDerivedGz))
		if err != nil {
			return
		}
		defer func() { _ = reader.Close() }()
		data, err := io.ReadAll(reader)
		if err != nil {
			return
		}
		var rows []*neoPiDerivedRow
		if json.Unmarshal(data, &rows) != nil {
			return
		}
		for _, row := range rows {
			neoPiDerivedRows[row.Provider+"\x00"+row.Kind] = row
		}
	})
	return neoPiDerivedRows[provider+"\x00"+kind]
}

// Derived-row kinds a mapper can switch reasoning on for.
const (
	neoPiKindReasoning  = "reasoning"
	neoPiKindDeepseekV4 = "deepseek-v4"
	neoPiKindKimiK3     = "kimi-k3"
	neoPiKindKimiK2     = "kimi-k2"
)

// neoPiSpec is the ModelSpec a discovery mapper yields for one listed id. A
// zero context window or max output is NeoPi's null.
type neoPiSpec struct {
	id            string
	name          string
	reasoning     bool
	input         []string
	contextWindow int
	maxTokens     int
	// kind selects the derived thinking/compat when reasoning was switched on.
	kind string
	// thinking is an explicit effort ladder the reference row authors; like a
	// ModelSpec's own thinking it wins over the rule-derived one in the merge.
	thinking *NeoPiThinking
	// ownThinking is compat trustExplicitThinkingOnly: the row keeps exactly
	// thinking (possibly none), never the bundled or rule-derived ladder.
	ownThinking bool
	// compat are the mapper's sparse compat overrides (its spec.compat),
	// applied over the row's resolved compat.
	compat []func(*NeoPiCompletionsCompat)
}

// NeoPiCompletionsListingBase is the discovery base URL a provider's NeoPi
// manager defaults to (its bundled rows' base URL).
func NeoPiCompletionsListingBase(provider string) string {
	catalog, ok := NeoPiCompletionsProvider(provider)
	if !ok {
		return ""
	}
	return catalog.BaseURL
}

// NeoPiCompletionsListingQuery is the query NeoPi appends to the /models probe.
func NeoPiCompletionsListingQuery(provider string) url.Values {
	switch provider {
	case "nanogpt":
		// Only the detailed roster carries per-model names and limits.
		return url.Values{"detailed": {"true"}}
	case "deepinfra":
		// DEEPINFRA_MODELS_QUERY: the catalog with its metadata block.
		return url.Values{"filter": {"with_meta"}, "sort_by": {"omp"}}
	}
	return nil
}

// NeoPiCompletionsDiscovers reports whether NeoPi's model manager for
// provider fetches a live listing; without one it serves the bundled roster.
func NeoPiCompletionsDiscovers(provider string) bool {
	catalog, ok := NeoPiCompletionsProvider(provider)
	return ok && catalog.Discovery.Listing
}

// NeoPiCompletionsListing resolves a provider's GET /models body (fetched from
// baseURL) into the roster NeoPi's model manager produces: the bundled rows,
// with the listing's ids and limits merged in. An unusable body is an error;
// NeoPi then serves the bundled roster.
func NeoPiCompletionsListing(provider, baseURL string, body []byte) ([]*ModelInfo, error) {
	if !NeoPiCompletionsDiscovers(provider) || provider == "fireworks" || provider == neoPiClinePassProvider {
		return nil, ErrNeoPiListing
	}
	payload := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) {
		return nil, ErrNeoPiListing
	}
	var entries []gjson.Result
	if provider == "deepinfra" {
		// fetchDeepinfraModels reads only an object's data array.
		data := payload.Get("data")
		if !payload.IsObject() || !data.IsArray() {
			return nil, ErrNeoPiListing
		}
		entries = data.Array()
	} else {
		var ok bool
		if entries, ok = neoPiExtractEntries(payload); !ok {
			return nil, ErrNeoPiListing
		}
	}
	baseURL = neoPiNormalizeBase(baseURL)
	specs, authoritative := neoPiListingSpecs(provider, entries)
	if authoritative && len(specs) == 0 {
		// NeoPi serves an empty roster for this cycle and does not cache it
		// as authoritative; the discovery cache retries it after its retry
		// interval.
		return []*ModelInfo{}, nil
	}
	return neoPiMergeListing(provider, baseURL, specs, authoritative), nil
}

// NeoPiFireworksPage parses one page of Fireworks' control-plane catalog
// (GET /v1/accounts/fireworks/models): its records and the next page token.
func NeoPiFireworksPage(body []byte) (records []json.RawMessage, next string, err error) {
	if !gjson.ValidBytes(body) {
		return nil, "", ErrNeoPiListing
	}
	payload := gjson.ParseBytes(body)
	if !payload.IsObject() {
		return nil, "", ErrNeoPiListing
	}
	if models := payload.Get("models"); models.IsArray() {
		for _, entry := range models.Array() {
			if entry.IsObject() {
				records = append(records, json.RawMessage(entry.Raw))
			}
		}
	}
	if token := payload.Get("nextPageToken"); token.Type == gjson.String {
		next = token.String()
	}
	return records, next, nil
}

// NeoPiFireworksListing resolves the control-plane records (all pages) into
// the roster NeoPi's Fireworks manager produces. Unlike the generic
// providers, Fireworks accepts an empty catalog.
func NeoPiFireworksListing(baseURL string, records []json.RawMessage) []*ModelInfo {
	baseURL = neoPiNormalizeBase(baseURL)
	var specs []neoPiSpec
	index := map[string]int{}
	for _, raw := range records {
		record := gjson.ParseBytes(raw)
		if record.Get("supportsServerless").Type != gjson.True {
			continue
		}
		if state := record.Get("state"); state.Type == gjson.String && state.String() != "READY" {
			continue
		}
		wire := record.Get("name")
		if wire.Type != gjson.String || wire.String() == "" {
			continue
		}
		publicID := neoPiFireworksPublicID(wire.String())
		if publicID == "" {
			continue
		}
		spec := neoPiMapFireworks(record, publicID, neoPiBundledRow("fireworks", publicID))
		if at, dup := index[publicID]; dup {
			specs[at] = spec
			continue
		}
		index[publicID] = len(specs)
		specs = append(specs, spec)
	}
	return neoPiMergeListing("fireworks", baseURL, specs, false)
}

func neoPiNormalizeBase(baseURL string) string {
	return strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
}

// neoPiFireworksPublicID is toFireworksPublicModelId.
func neoPiFireworksPublicID(id string) string {
	id = strings.TrimPrefix(id, "accounts/fireworks/models/")
	var out strings.Builder
	for i := 0; i < len(id); i++ {
		// "p" between two digits is a version separator ("5p3" -> "5.3").
		if id[i] == 'p' && i > 0 && i+1 < len(id) && neoPiIsDigitByte(id[i-1]) && neoPiIsDigitByte(id[i+1]) {
			out.WriteByte('.')
			continue
		}
		out.WriteByte(id[i])
	}
	return out.String()
}

func neoPiIsDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// neoPiExtractEntries is extractModelEntries: an array, or the first of
// data/models/result/items that yields one, keeping the entries that carry a
// non-empty string id and a string-or-null name.
func neoPiExtractEntries(node gjson.Result) ([]gjson.Result, bool) {
	if node.IsArray() {
		out := []gjson.Result{}
		for _, item := range node.Array() {
			if neoPiValidEntry(item) {
				out = append(out, item)
			}
		}
		return out, true
	}
	if node.IsObject() {
		for _, key := range []string{"data", "models", "result", "items"} {
			candidate := node.Get(key)
			if !candidate.Exists() {
				continue
			}
			if nested, ok := neoPiExtractEntries(candidate); ok {
				return nested, true
			}
		}
	}
	return nil, false
}

func neoPiValidEntry(item gjson.Result) bool {
	if !item.IsObject() {
		return false
	}
	id := item.Get("id")
	if id.Type != gjson.String || id.String() == "" {
		return false
	}
	if name := item.Get("name"); name.Exists() && name.Type != gjson.String && name.Type != gjson.Null {
		return false
	}
	return true
}

func neoPiToNumber(value gjson.Result) (float64, bool) {
	var number float64
	switch value.Type {
	case gjson.Number:
		number = value.Float()
	case gjson.String:
		text := strings.TrimSpace(value.String())
		if text == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

// neoPiPositive is toPositiveNumber(value, fallback) with 0 as null.
func neoPiPositive(value gjson.Result, fallback int) int {
	if number, ok := neoPiToNumber(value); ok && number > 0 {
		return int(number)
	}
	return fallback
}

// neoPiModelName is toModelName.
func neoPiModelName(value gjson.Result, fallback string) string {
	if value.Type != gjson.String {
		return fallback
	}
	if trimmed := strings.TrimSpace(value.String()); trimmed != "" {
		return trimmed
	}
	return fallback
}

func neoPiBundledRow(provider, id string) *NeoPiModel {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	for _, row := range neopiCompletionsRows[provider] {
		if row.ID == id {
			return row
		}
	}
	return nil
}

// neoPiDefaultSpec is the generic discovery default for a listed entry.
func neoPiDefaultSpec(entry gjson.Result) neoPiSpec {
	id := entry.Get("id").String()
	name := id
	if value := entry.Get("name"); value.Type == gjson.String && value.String() != "" {
		name = value.String()
	}
	return neoPiSpec{id: id, name: name, input: []string{"text"}}
}

// neoPiMapWithBundledReference is mapWithBundledReference: the bundled row
// supplies capabilities; the listing supplies the name and, when present,
// the limits.
func neoPiMapWithBundledReference(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	fallback := defaults.name
	if reference != nil {
		fallback = reference.Name
	}
	name := neoPiModelName(entry.Get("name"), fallback)
	if reference == nil {
		defaults.name = name
		return defaults
	}
	return neoPiSpec{
		id:            defaults.id,
		name:          name,
		reasoning:     reference.Reasoning,
		input:         slices.Clone(reference.Input),
		contextWindow: neoPiPositive(entry.Get("context_length"), reference.ContextWindow),
		maxTokens:     neoPiPositive(entry.Get("max_completion_tokens"), reference.MaxTokens),
	}
}

var (
	// alibabaLimits are the model-limits rules NeoPi pins for ids the
	// discovery lists without limits.
	alibabaLimits = map[string][2]int{
		"qwen3.6-plus":           {1000000, 65536},
		"qwen3.8-max":            {1000000, 131072},
		"deepseek-v4-flash":      {1000000, 384000},
		"deepseek-v4-flash-0731": {1000000, 384000},
		"deepseek-v4-pro-0813":   {1000000, 384000},
		"deepseek-v3.2":          {131072, 65536},
		"glm-5.1":                {202752, 128000},
		"glm-5":                  {202752, 16384},
		"kimi-k2.7-code":         {262144, 262144},
		"kimi-k2.6":              {262144, 262144},
		"kimi-k2.5":              {262144, 98304},
		"minimax-m2.5":           {196608, 32768},
	}
	moonshotKimiK3Limits = [2]int{1048576, 131072}
)

// neoPiNanoGptThinkingSuffix is NANO_GPT_THINKING_SUFFIX_RE
// (/:thinking(:[^:]+)?$/): the index of the leftmost match in id, or -1.
func neoPiNanoGptThinkingSuffix(id string) int {
	const marker = ":thinking"
	for from := 0; from < len(id); {
		found := strings.Index(id[from:], marker)
		if found < 0 {
			return -1
		}
		at := from + found
		rest := id[at+len(marker):]
		if rest == "" || (strings.HasPrefix(rest, ":") && len(rest) > 1 && !strings.Contains(rest[1:], ":")) {
			return at
		}
		from = at + 1
	}
	return -1
}

func neoPiIsLikelyNanoGptText(id string) bool {
	normalized := strings.ToLower(strings.TrimSpace(id))
	if normalized == "" || neoPiNanoGptThinkingSuffix(normalized) >= 0 {
		return false
	}
	return !neoPiExcludedModel("nanogpt", normalized)
}

// neoPiIsDeepseekV4Generation is isDeepseekV4Generation: the DeepSeek V4,
// Flash and Pro families.
func neoPiIsDeepseekV4Generation(provider, id string) bool {
	identity := neopitaxonomy.Classify(provider, id)
	return identity.Class == "deepseek" && (identity.Family == "v4" || identity.Family == "flash" || identity.Family == "pro")
}

// neoPiIsGlmReasoningIdentity is isGlmReasoningIdentity: a GLM at or past
// floor, of the base, Air or Turbo family, or Flash from 5.3 on.
func neoPiIsGlmReasoningIdentity(provider, id, floor string) bool {
	identity := neopitaxonomy.Classify(provider, id)
	if identity.Class != "glm" || !identity.AtLeast(floor) {
		return false
	}
	if identity.Family == "flash" {
		return identity.AtLeast("5.3")
	}
	return identity.Family == "" || identity.Family == "air" || identity.Family == "turbo"
}

// neoPiListingSpecs maps the listing's entries with the provider's mapper and
// filter, deduplicating by id (the last entry wins).
func neoPiListingSpecs(provider string, entries []gjson.Result) (specs []neoPiSpec, authoritative bool) {
	if catalog, ok := NeoPiCompletionsProvider(provider); ok {
		authoritative = catalog.Discovery.ModelsAuthoritative
	}
	index := map[string]int{}
	thinkingBases := map[string]bool{}
	for _, entry := range entries {
		defaults := neoPiDefaultSpec(entry)
		reference := neoPiBundledRow(provider, defaults.id)
		var spec neoPiSpec
		switch provider {
		case "moonshot":
			spec = neoPiMapMoonshot(entry, defaults, reference)
		case "nanogpt":
			spec = neoPiMapNanoGpt(entry, defaults, reference)
			if at := neoPiNanoGptThinkingSuffix(spec.id); at >= 0 {
				// Thinking variants are dropped; their base id is marked
				// reasoning-capable.
				thinkingBases[spec.id[:at]] = true
				continue
			}
			if !neoPiIsLikelyNanoGptText(spec.id) {
				continue
			}
		case "alibaba-token-plan":
			spec = neoPiMapAlibaba(defaults, reference)
			normalized := strings.ToLower(strings.TrimSpace(spec.id))
			if normalized == "" || neoPiExcludedModel("alibaba-token-plan", normalized) {
				continue
			}
		case "zhipu-coding-plan":
			spec = neoPiMapZhipuCodingPlan(defaults)
		case "wafer-serverless":
			spec = neoPiMapWafer(entry, defaults)
		case "aiand":
			spec = neoPiMapAiand(entry, defaults)
		case "baseten":
			spec = neoPiMapBaseten(entry, defaults, reference)
		case "sambanova":
			spec = neoPiMapSambaNova(entry, defaults)
			if neoPiExcludedModel("sambanova", spec.id) {
				continue
			}
		case "synthetic":
			spec = neoPiMapSynthetic(entry, defaults, reference)
		case "coralbricks":
			var ok bool
			if spec, ok = neoPiMapCoralbricks(entry, defaults, reference); !ok {
				continue
			}
		case "inception":
			var ok bool
			if spec, ok = neoPiMapInception(entry, defaults, reference); !ok {
				continue
			}
		case "novita":
			spec = neoPiMapNovita(entry, defaults, reference)
			if !neoPiNovitaListed(entry, spec.id) {
				continue
			}
		case "deepinfra":
			var ok bool
			if spec, ok = neoPiMapDeepinfra(entry); !ok {
				continue
			}
			if _, dup := index[spec.id]; dup {
				// fetchDeepinfraModels keeps the first row of an id.
				continue
			}
		case "venice":
			spec = neoPiMapVenice(entry, defaults, reference)
		case "stepfun":
			spec = neoPiMapStepfun(entry, defaults, reference)
			if !neoPiStepfunChat(spec.id) {
				continue
			}
		case "helmcode":
			// NeoPi lends an unbundled id the metadata of the Anthropic,
			// OpenAI or Google row it resells; those catalogs are not
			// carried here, so such an id maps like any unknown id.
			spec = neoPiMapWithBundledReference(entry, defaults, reference)
			if neoPiExcludedModel("helmcode", spec.id) {
				continue
			}
		case "xiaomi-token-plan-sgp", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn":
			spec = neoPiMapXiaomiTokenPlan(entry, defaults, reference)
			if neoPiExcludedModel(provider, spec.id) {
				continue
			}
		case "yolo-auto":
			spec = neoPiMapYoloAuto(entry, defaults, reference)
		case "gmi-cloud":
			// mapGmiCloudModel resolves an unbundled id against NeoPi's
			// cross-provider reference index, which is not carried here; such
			// an id maps like any unknown id.
			spec = neoPiMapWithBundledReference(entry, defaults, reference)
		case "aimlapi":
			// isLikelyAimlApiChatModelId: image, audio, video and embedding
			// generators share the listing.
			spec = neoPiMapWithBundledReference(entry, defaults, reference)
			normalized := strings.ToLower(strings.TrimSpace(spec.id))
			if normalized == "" || neoPiExcludedModel("aimlapi", normalized) {
				continue
			}
		default:
			spec = neoPiMapWithBundledReference(entry, defaults, reference)
		}
		if at, dup := index[spec.id]; dup {
			specs[at] = spec
			continue
		}
		index[spec.id] = len(specs)
		specs = append(specs, spec)
	}
	if provider == "nanogpt" {
		for i := range specs {
			if !specs[i].reasoning && thinkingBases[specs[i].id] {
				specs[i].reasoning = true
				specs[i].kind = neoPiKindReasoning
			}
		}
	}
	return specs, authoritative
}

// neoPiMapMoonshot is moonshotModelManagerOptions' mapModel.
func neoPiMapMoonshot(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	model := neoPiMapWithBundledReference(entry, defaults, reference)
	id := strings.ToLower(model.id)
	if reference == nil && strings.HasPrefix(id, "kimi-k3") {
		// Kimi K3 is discovered but bundled nowhere: it always reasons, takes
		// images, and its limits are pinned by NeoPi's model-limits rules.
		limits := moonshotKimiK3Limits
		model.reasoning = true
		model.kind = neoPiKindKimiK3
		model.input = []string{"text", "image"}
		if model.contextWindow == 0 {
			model.contextWindow = limits[0]
		}
		if model.maxTokens == 0 {
			model.maxTokens = limits[1]
		}
		return model
	}
	isK2 := strings.HasPrefix(id, "kimi-k2")
	thinkingVariant := strings.HasPrefix(id, "kimi") && strings.HasSuffix(id, "-thinking")
	if (thinkingVariant || isK2) && !model.reasoning {
		model.reasoning = true
		model.kind = neoPiKindKimiK2
	}
	return model
}

// neoPiMapNanoGpt is nanoGptModelManagerOptions' mapModel: the detailed
// roster names its output cap max_output_tokens, and ids without a bundled
// reference take their limits from the listing.
func neoPiMapNanoGpt(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	maxOutput := entry.Get("max_output_tokens")
	if !maxOutput.Exists() || maxOutput.Type == gjson.Null {
		maxOutput = entry.Get("max_completion_tokens")
	}
	if reference == nil {
		defaults.contextWindow = neoPiPositive(entry.Get("context_length"), 0)
		defaults.maxTokens = neoPiPositive(maxOutput, 0)
		defaults.name = neoPiModelName(entry.Get("name"), defaults.name)
		return defaults
	}
	name := neoPiModelName(entry.Get("name"), reference.Name)
	return neoPiSpec{
		id:            defaults.id,
		name:          name,
		reasoning:     reference.Reasoning,
		input:         slices.Clone(reference.Input),
		contextWindow: neoPiPositive(entry.Get("context_length"), reference.ContextWindow),
		maxTokens:     neoPiPositive(maxOutput, reference.MaxTokens),
	}
}

// neoPiMapAlibaba is alibabaTokenPlanModelManagerOptions' mapModel: a listed
// seed id keeps NeoPi's authored seed row whole, including the explicit effort
// ladder it authors (the manager's static models are the provider's seed
// rows, not the generated bundle, whose ladder is rule-derived and can differ);
// others take the model-limits rules.
func neoPiMapAlibaba(defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	if reference != nil {
		spec := neoPiSpec{id: defaults.id, name: reference.Name, reasoning: reference.Reasoning, input: slices.Clone(reference.Input),
			contextWindow: reference.ContextWindow, maxTokens: reference.MaxTokens}
		if seed := neoPiDerivedFor("alibaba-token-plan", "seed:"+defaults.id); seed != nil {
			spec.thinking = seed.Thinking.Clone()
		}
		return spec
	}
	normalized := strings.ToLower(strings.TrimSpace(defaults.id))
	if limits, ok := alibabaLimits[normalized]; ok {
		defaults.contextWindow, defaults.maxTokens = limits[0], limits[1]
	}
	if neoPiIsDeepseekV4Generation("alibaba-token-plan", normalized) {
		defaults.reasoning = true
		defaults.kind = neoPiKindDeepseekV4
	}
	return defaults
}

// neoPiMapFireworks is mapFireworksControlPlaneModel. The control plane
// carries capability flags but no reasoning bit: every serverless chat model
// reasons, so unbundled ids default to reasoning.
func neoPiMapFireworks(record gjson.Result, publicID string, reference *NeoPiModel) neoPiSpec {
	fallback := publicID
	if reference != nil {
		fallback = reference.Name
	}
	spec := neoPiSpec{id: publicID, name: neoPiModelName(record.Get("displayName"), fallback), input: []string{"text"}}
	if reference != nil {
		spec.reasoning = reference.Reasoning
		spec.input = slices.Clone(reference.Input)
		spec.maxTokens = reference.MaxTokens
		spec.contextWindow = neoPiPositive(record.Get("contextLength"), reference.ContextWindow)
	} else {
		spec.reasoning = true
		spec.contextWindow = neoPiPositive(record.Get("contextLength"), 0)
		if neoPiIsDeepseekV4Generation("fireworks", publicID) {
			spec.kind = neoPiKindDeepseekV4
		} else {
			spec.kind = neoPiKindReasoning
		}
	}
	if image := record.Get("supportsImageInput"); image.Type == gjson.True {
		spec.input = []string{"text", "image"}
	}
	return spec
}

// neoPiPreferName is preferDiscoveryName.
func neoPiPreferName(discovered, fallback, id string) string {
	discovered = strings.TrimSpace(discovered)
	if discovered == "" {
		return fallback
	}
	if discovered == id && fallback != id {
		return fallback
	}
	return discovered
}

// neoPiPreferLimit is preferDiscoveryLimit; zero is null.
func neoPiPreferLimit(discovered, fallback int) int {
	if discovered <= 0 {
		return fallback
	}
	if discovered == 4096 && fallback > discovered {
		return fallback
	}
	return discovered
}

// neoPiMergeListing is the model manager's merge of the discovered specs into
// the bundled roster: a discovered id replaces its bundled row additively,
// unknown ids are added, and an authoritative provider keeps only what was
// discovered.
func neoPiMergeListing(provider, baseURL string, specs []neoPiSpec, authoritative bool) []*ModelInfo {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	catalog, _ := NeoPiCompletionsProvider(provider)
	byID := make(map[string]*neoPiSpec, len(specs))
	for i := range specs {
		byID[specs[i].id] = &specs[i]
	}
	rows := make([]*NeoPiModel, 0, len(neopiCompletionsRows[provider])+len(specs))
	seen := map[string]bool{}
	for _, static := range neopiCompletionsRows[provider] {
		spec := byID[static.ID]
		if spec == nil {
			if !authoritative {
				rows = append(rows, static.Clone())
			}
			continue
		}
		seen[static.ID] = true
		rows = append(rows, neoPiMergedRow(provider, baseURL, catalog, static, spec))
	}
	var added []*NeoPiModel
	for i := range specs {
		if seen[specs[i].id] {
			continue
		}
		added = append(added, neoPiNewRow(provider, baseURL, catalog, &specs[i]))
	}
	authored := map[string]bool{}
	for i := range specs {
		if specs[i].thinking != nil {
			authored[specs[i].id] = true
		}
	}
	rows = neoPiCollapseThinkingPairs(provider, append(rows, added...), authored)
	out := make([]*ModelInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, NeoPiCompletionsModelInfo(row, provider))
	}
	return out
}

// neoPiMergedRow is mergeDynamicModel for a bundled row the listing names.
func neoPiMergedRow(provider, baseURL string, catalog *NeoPiCompletionsCatalog, static *NeoPiModel, spec *neoPiSpec) *NeoPiModel {
	row := static.Clone()
	endpointChanged := static.BaseURL != baseURL
	row.BaseURL = baseURL
	row.Name = neoPiPreferName(spec.name, static.Name, static.ID)
	row.Reasoning = static.Reasoning || spec.reasoning
	if catalog != nil && catalog.Discovery.ReasoningAuthoritative {
		row.Reasoning = spec.reasoning
	}
	supportsImage := slices.Contains(static.Input, "image") || slices.Contains(spec.input, "image")
	if endpointChanged || catalog != nil && catalog.Discovery.InputAuthoritative {
		supportsImage = slices.Contains(spec.input, "image")
	}
	row.Input = []string{"text"}
	if supportsImage {
		row.Input = []string{"text", "image"}
	}
	row.ContextWindow = neoPiPreferLimit(spec.contextWindow, static.ContextWindow)
	row.MaxTokens = neoPiPreferLimit(spec.maxTokens, static.MaxTokens)
	if row.Reasoning && !static.Reasoning {
		neoPiApplyDerived(provider, row, spec.kind)
	}
	if spec.thinking != nil || spec.ownThinking {
		row.Thinking = spec.thinking.Clone()
	}
	neoPiApplySpecCompat(row, spec)
	if !row.Reasoning {
		// buildModel gives a non-reasoning row no thinking surface.
		row.Thinking = nil
	}
	return row
}

// neoPiApplySpecCompat applies a mapper's sparse compat overrides.
func neoPiApplySpecCompat(row *NeoPiModel, spec *neoPiSpec) {
	if !spec.ownThinking && len(spec.compat) == 0 {
		return
	}
	if row.CompletionsCompat == nil {
		row.CompletionsCompat = &NeoPiCompletionsCompat{}
	}
	if spec.ownThinking {
		trust := true
		row.CompletionsCompat.TrustExplicitThinkingOnly = &trust
	}
	for _, edit := range spec.compat {
		edit(row.CompletionsCompat)
	}
}

// neoPiSiblingMinShare is the share of a discovered id's length that a
// bundled id must match as a prefix to stand in for it.
const neoPiSiblingMinShare = 0.3

// neoPiNewRow builds the row for an id the bundle lacks.
//
// NeoPi derives such a row's compat and effort ladder from its id through the
// catalog rule engine (buildModel), which cannot run here. The nearest
// bundled sibling of the same provider and reasoning state carries what that
// engine derived for a neighbouring id, which measured against NeoPi's own
// buildModel (leave-one-out over each bundled roster) predicts the compat far
// more often than one provider-wide default; with no close sibling the
// provider's default for an unknown id applies.
func neoPiNewRow(provider, baseURL string, catalog *NeoPiCompletionsCatalog, spec *neoPiSpec) *NeoPiModel {
	input := spec.input
	if len(input) == 0 {
		input = []string{"text"}
	}
	row := &NeoPiModel{ID: spec.id, Name: spec.name, API: "openai-completions", Provider: provider, BaseURL: baseURL,
		Reasoning: spec.reasoning, Input: slices.Clone(input), ContextWindow: spec.contextWindow, MaxTokens: spec.maxTokens,
		CompletionsCompat: catalog.UnknownCompat().Clone()}
	if spec.reasoning && !spec.ownThinking {
		neoPiApplyDerived(provider, row, spec.kind)
		if spec.thinking != nil {
			row.Thinking = spec.thinking.Clone()
		}
	} else if spec.reasoning {
		if sibling := neoPiNearestBundled(provider, spec.id, true); sibling != nil {
			row.CompletionsCompat = sibling.CompletionsCompat.Clone()
		}
		row.Thinking = spec.thinking.Clone()
	} else if sibling := neoPiNearestBundled(provider, spec.id, false); sibling != nil {
		row.CompletionsCompat = sibling.CompletionsCompat.Clone()
	}
	neoPiApplySpecCompat(row, spec)
	if !row.Reasoning {
		row.Thinking = nil
	}
	return row
}

// neoPiApplyDerived gives a row whose reasoning a mapper switched on the
// effort ladder and compat NeoPi derives for its kind. Rows of the generic
// kind take their nearest reasoning sibling's; the kind-specific rows (Kimi,
// DeepSeek V4) take the captured derivation. NeoPi pins effort ladders to
// exact revisions (glm.kdl's 5.2 and 5.3 ladders name those spellings), so a
// sibling of another revision of the same line lends only its compat; the
// ladder is the provider's generic one when that was captured.
func neoPiApplyDerived(provider string, row *NeoPiModel, kind string) {
	if kind == neoPiKindReasoning {
		if sibling := neoPiNearestBundled(provider, row.ID, true); sibling != nil && sibling.Thinking != nil {
			row.Thinking = sibling.Thinking.Clone()
			// Routing names the sibling's own variants.
			row.Thinking.EffortRouting = nil
			row.CompletionsCompat = sibling.CompletionsCompat.Clone()
			if generic := neoPiDerivedFor(provider, neoPiKindReasoning); generic != nil && neoPiOtherRevision(provider, row.ID, sibling.ID) {
				row.Thinking = generic.Thinking.Clone()
			}
			return
		}
	}
	derived := neoPiDerivedFor(provider, kind)
	if derived == nil {
		return
	}
	row.Thinking = derived.Thinking.Clone()
	if derived.Compat != nil {
		row.CompletionsCompat = derived.Compat.Clone()
	}
}

// neoPiOtherRevision reports whether two ids are the same class and family
// at different revisions.
func neoPiOtherRevision(provider, id, other string) bool {
	a, b := neopitaxonomy.Classify(provider, id), neopitaxonomy.Classify(provider, other)
	return a.Class != "unknown" && a.Class == b.Class && a.Family == b.Family &&
		a.HasRevision && b.HasRevision && a.Revision != b.Revision
}

// neoPiNearestBundled is the bundled row of the provider sharing the longest
// case-insensitive id prefix with id (at least neoPiSiblingMinShare of it),
// among rows with the wanted reasoning state. Rows sharing equally long
// prefixes prefer the same class and family at the newest revision not
// past id's: NeoPi's lineage rules mostly select open-ended revision
// ranges, so that row's derivation is the one id falls into.
func neoPiNearestBundled(provider, id string, reasoning bool) *NeoPiModel {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	lower := strings.ToLower(id)
	target := neopitaxonomy.Classify(provider, id)
	// lineage ranks a candidate among equally long prefixes: 2 for the same
	// lineage at or below the target revision, 1 for the same lineage
	// otherwise, 0 for another lineage.
	lineage := func(row *NeoPiModel) (int, neopitaxonomy.Revision) {
		identity := neopitaxonomy.Classify(provider, row.ID)
		if identity.Class != target.Class || identity.Family != target.Family || target.Class == "unknown" {
			return 0, neopitaxonomy.Revision{}
		}
		if identity.HasRevision && target.HasRevision && identity.Revision.Compare(target.Revision) <= 0 {
			return 2, identity.Revision
		}
		return 1, neopitaxonomy.Revision{}
	}
	var best *NeoPiModel
	bestShared, bestTier := 0, 0
	var bestRevision neopitaxonomy.Revision
	for _, row := range neopiCompletionsRows[provider] {
		if row.Reasoning != reasoning || row.ID == id || row.CompletionsCompat == nil {
			continue
		}
		other := strings.ToLower(row.ID)
		shared := 0
		for shared < len(lower) && shared < len(other) && lower[shared] == other[shared] {
			shared++
		}
		if shared < bestShared || shared == 0 {
			continue
		}
		tier, revision := lineage(row)
		if shared > bestShared || tier > bestTier || tier == bestTier && tier == 2 && revision.Compare(bestRevision) > 0 {
			best, bestShared, bestTier, bestRevision = row, shared, tier, revision
		}
	}
	if best == nil || float64(bestShared) < neoPiSiblingMinShare*float64(len(lower)) {
		return nil
	}
	return best
}

// neoPiDefaultPairEfforts is DEFAULT_PAIR_EFFORTS.
var neoPiDefaultPairEfforts = []string{"minimal", "low", "medium", "high"}

// neoPiCollapseThinkingPairs is the model manager's collapseBuiltVariants
// for the completions providers (none has a reviewed collapse table): each
// "<id>-thinking" style row whose base id is also listed folds into the
// base, which then routes every effort of the pair's thinking surface to
// the variant and "off" to itself. authored names the rows whose ladder the
// listing itself supplied. The collapsed row keeps a reasoning member's
// compat where NeoPi re-derives it for the now-reasoning base.
func neoPiCollapseThinkingPairs(provider string, rows []*NeoPiModel, authored map[string]bool) []*NeoPiModel {
	byID := make(map[string]*NeoPiModel, len(rows))
	for _, row := range rows {
		if _, ok := byID[row.ID]; !ok {
			byID[row.ID] = row
		}
	}
	dropped := map[string]bool{}
	for _, row := range rows {
		baseID, ok := "", false
		if logical, thinking := neopitaxonomy.CollapseVariant(provider, row.ID); thinking {
			baseID, ok = logical, true
		} else {
			baseID, ok = neopitaxonomy.StripThinkingVariantSuffix(row.ID)
		}
		base := byID[baseID]
		if !ok || baseID == row.ID || base == nil || dropped[row.ID] || dropped[base.ID] {
			continue
		}
		if base.Thinking != nil && base.Thinking.EffortRouting != nil {
			// The base is already collapsed: it wins and the raw member goes.
			dropped[row.ID] = true
			continue
		}
		// derivePairThinkingSurface: the variant's ladder, else the base's,
		// else what the rule engine derives for the variant as a reasoning
		// row, else NeoPi's default pair ladder.
		surface, source := row.Thinking, row.ID
		if surface == nil || len(surface.Efforts) == 0 {
			surface, source = base.Thinking, base.ID
		}
		if surface == nil || len(surface.Efforts) == 0 {
			derived := row.Clone()
			derived.Reasoning, derived.Thinking = true, nil
			neoPiApplyDerived(provider, derived, neoPiKindReasoning)
			surface, source = derived.Thinking, ""
		}
		if surface == nil || len(surface.Efforts) == 0 {
			surface = &NeoPiThinking{Mode: "budget", Efforts: slices.Clone(neoPiDefaultPairEfforts)}
		}
		thinking := surface.Clone()
		if authored[source] {
			// The surface drops a listing-authored mandatory/suppress flag;
			// only the rule engine's own (kept on derived rows) survives
			// the rebuild.
			thinking.RequiresEffort, thinking.SuppressWhenOff = false, false
		}
		thinking.EffortRouting = map[string]string{"off": base.ID}
		for _, effort := range thinking.Efforts {
			thinking.EffortRouting[effort] = row.ID
		}
		if !base.Reasoning {
			base.CompletionsCompat = row.CompletionsCompat.Clone()
		}
		base.Reasoning = true
		base.Thinking = thinking
		if slices.Contains(row.Input, "image") {
			base.Input = []string{"text", "image"}
		}
		base.ContextWindow = max(base.ContextWindow, row.ContextWindow)
		base.MaxTokens = max(base.MaxTokens, row.MaxTokens)
		dropped[row.ID] = true
	}
	if len(dropped) == 0 {
		return rows
	}
	out := rows[:0:0]
	for _, row := range rows {
		if !dropped[row.ID] {
			out = append(out, row)
		}
	}
	return out
}
