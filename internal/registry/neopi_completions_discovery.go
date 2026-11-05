package registry

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cloudflare"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicompat"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
	"github.com/tidwall/gjson"
)

// NeoPi's live model discovery for the providers it serves through its
// OpenAI Chat Completions provider. Each provider's ModelManagerOptions
// (pi-catalog provider-models/openai-compat.ts) fetches a listing, maps every
// entry onto a spec, and the model manager builds the specs and merges them
// into the bundled roster (mergeDynamicModel). This file ports the mapping
// and the merge, building every row through NeoPi's compat engine (package
// neopicompat); the HTTP request lives with the caller. Parity against NeoPi's
// own managers is captured by testdata/capture_neopi_completions_discovery.ts.

//go:embed models/neopi_discovery/alibaba_token_plan_seeds.json.gz
var neoPiAlibabaSeedsGz []byte

// ErrNeoPiListing marks a listing NeoPi would treat as a failed discovery:
// its manager then keeps the bundled roster.
var ErrNeoPiListing = errors.New("registry: unusable model listing")

// NeoPiEmptyListing is a successful listing that named no models. NeoPi
// serves Models (the bundled rows its merge keeps) for this cycle but does
// not cache the result as authoritative: it retries after its short
// interval. It travels as an error so it survives callers' error wrapping.
type NeoPiEmptyListing struct {
	Models []*ModelInfo
}

func (e *NeoPiEmptyListing) Error() string { return "registry: the model listing named no models" }

type neoPiSeed struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Reasoning     bool            `json:"reasoning"`
	Input         []string        `json:"input"`
	ContextWindow int             `json:"contextWindow"`
	MaxTokens     int             `json:"maxTokens"`
	Thinking      json.RawMessage `json:"thinking"`
	Compat        map[string]any  `json:"compat"`
}

var (
	neoPiSeedsOnce sync.Once
	neoPiSeeds     map[string]*neoPiSeed
)

// neoPiAlibabaSeedSpec is the QwenCloud Token Plan seed spec NeoPi authors
// for id (seedModels("alibaba-token-plan")), or false.
func neoPiAlibabaSeedSpec(id string) (neoPiSpec, bool) {
	neoPiSeedsOnce.Do(func() {
		neoPiSeeds = map[string]*neoPiSeed{}
		reader, err := gzip.NewReader(bytes.NewReader(neoPiAlibabaSeedsGz))
		if err != nil {
			return
		}
		defer func() { _ = reader.Close() }()
		data, err := io.ReadAll(reader)
		if err != nil {
			return
		}
		var seeds []*neoPiSeed
		if json.Unmarshal(data, &seeds) != nil {
			return
		}
		for _, seed := range seeds {
			neoPiSeeds[seed.ID] = seed
		}
	})
	seed, ok := neoPiSeeds[id]
	if !ok {
		return neoPiSpec{}, false
	}
	thinking, requiresEffortSet, err := neoPiDecodeThinking(seed.Thinking)
	if err != nil {
		return neoPiSpec{}, false
	}
	return neoPiSpec{id: seed.ID, name: seed.Name, reasoning: seed.Reasoning, input: slices.Clone(seed.Input),
		contextWindow: seed.ContextWindow, maxTokens: seed.MaxTokens, thinking: thinking,
		thinkingRequiresEffortSet: requiresEffortSet, compat: maps.Clone(seed.Compat)}, true
}

// neoPiSpec is the ModelSpec a discovery mapper yields for one listed id. A
// zero context window or max output is NeoPi's null.
type neoPiSpec struct {
	id            string
	name          string
	reasoning     bool
	input         []string
	contextWindow int
	maxTokens     int
	// thinking is the spec's authored effort ladder; NeoPi's compat engine
	// fills what it leaves out. thinkingRequiresEffortSet marks an authored
	// requiresEffort of false, which the engine must not fill.
	thinking                  *NeoPiThinking
	thinkingRequiresEffortSet bool
	// compat is the spec's sparse authored compat (spec.compat).
	compat map[string]any
	// requestModelID is a collapsed family's default wire id.
	requestModelID string
	// api is the spec's transport when a mapper moves it off
	// openai-completions ("openai-images", "anthropic-messages"); kind is its
	// authored model kind.
	api  string
	kind string
	// baseURL is the spec's own endpoint when its mapper routes it off the
	// provider's listing base (ZenMux's Anthropic route); empty is that base.
	baseURL string
	// cost is the spec's token tariff (zero for a bare listing row).
	cost NeoPiCost
}

// withCompat sets one authored compat override.
func (s *neoPiSpec) withCompat(key string, value any) {
	if s.compat == nil {
		s.compat = map[string]any{}
	}
	s.compat[key] = value
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

// NeoPiCompletionsDiscoversWithoutKey reports whether provider's NeoPi model
// manager still fetches its listing when no key is configured.
func NeoPiCompletionsDiscoversWithoutKey(provider string) bool {
	catalog, ok := NeoPiCompletionsProvider(provider)
	return ok && catalog.Discovery.Listing && catalog.Discovery.Unauthenticated
}

// NeoPiCompletionsListing resolves a provider's GET /models body (fetched from
// baseURL) into the roster NeoPi's model manager produces: the bundled rows,
// with the listing's ids and limits merged in. An unusable body is an error;
// NeoPi then serves the bundled roster.
func NeoPiCompletionsListing(provider, baseURL string, body []byte) ([]*ModelInfo, error) {
	return neoPiCompletionsListing(provider, baseURL, body, neoPiListingExtras{})
}

// neoPiListingExtras are what a manager fetched beside its listing: the
// models.dev references (SiliconFlow) and LM Studio's native metadata.
type neoPiListingExtras struct {
	modelsDev map[string]*NeoPiModel
	lmStudio  map[string]neoPiLmStudioMetadata
}

// NeoPiSiliconFlowListing is NeoPiCompletionsListing for SiliconFlow, whose
// manager hydrates the listing from the models.dev catalog it fetched first
// (modelsDev; nil when that fetch failed).
func NeoPiSiliconFlowListing(provider, baseURL string, body, modelsDev []byte) ([]*ModelInfo, error) {
	return neoPiCompletionsListing(provider, baseURL, body, neoPiListingExtras{modelsDev: neoPiModelsDevReferences(provider, neoPiNormalizeBase(baseURL), modelsDev)})
}

func neoPiCompletionsListing(provider, baseURL string, body []byte, extras neoPiListingExtras) ([]*ModelInfo, error) {
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
	specs, authoritative := neoPiListingSpecs(provider, baseURL, entries, extras.modelsDev)
	if extras.lmStudio != nil {
		neoPiApplyLmStudioMetadata(specs, extras.lmStudio)
	}
	// A listing that names no models comes back as NeoPiEmptyListing with
	// the bundled rows the merge keeps (runner rows, for an authoritative
	// provider), which the discovery cache retries after its short interval.
	return neoPiMergeListing(provider, baseURL, specs, authoritative)
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
func NeoPiFireworksListing(baseURL string, records []json.RawMessage) ([]*ModelInfo, error) {
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

// neoPiPerTokenPricingCost is perTokenPricingCost: OpenRouter-style per-token
// USD rates scaled to per million; a missing or non-positive rate is 0.
func neoPiPerTokenPricingCost(pricing gjson.Result) NeoPiCost {
	if !pricing.IsObject() {
		return NeoPiCost{}
	}
	rate := func(keys ...string) float64 {
		for _, key := range keys {
			// rates.a ?? rates.b: the first key that is present (not null).
			if value := pricing.Get(key); value.Exists() && value.Type != gjson.Null {
				if number, ok := neoPiToNumber(value); ok && number > 0 {
					return number * 1_000_000
				}
				return 0
			}
		}
		return 0
	}
	return NeoPiCost{Input: rate("prompt"), Output: rate("completion"),
		CacheRead: rate("input_cache_read", "input_cache_reads"), CacheWrite: rate("input_cache_write", "input_cache_writes")}
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

// neoPiMapWithBundledReference is mapWithBundledReference: the bundled row as
// a spec (its capabilities, ladder and authored compat), named by the listing
// and, when it reports them, limited by its limits.
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
	spec := neoPiReferenceSpec(defaults.id, reference)
	spec.name = name
	spec.contextWindow = neoPiPositive(entry.Get("context_length"), reference.ContextWindow)
	spec.maxTokens = neoPiPositive(entry.Get("max_completion_tokens"), reference.MaxTokens)
	return spec
}

// neoPiReferenceSpec is toModelSpec(reference) under id: the bundled row as
// the spec it was built from.
func neoPiReferenceSpec(id string, reference *NeoPiModel) neoPiSpec {
	spec := neoPiSpec{id: id, name: reference.Name, reasoning: reference.Reasoning, input: slices.Clone(reference.Input),
		contextWindow: reference.ContextWindow, maxTokens: reference.MaxTokens, thinking: reference.Thinking.Clone(),
		compat: maps.Clone(reference.CompatConfig), requestModelID: reference.RequestModelID,
		thinkingRequiresEffortSet: reference.thinkingRequiresEffortSet, kind: reference.Kind, cost: reference.cost}
	if neoPiRunnerAPI(reference.API) {
		spec.api = reference.API
	}
	if reference.API == neopicompat.APIAnthropicMessages {
		spec.api, spec.baseURL = reference.API, reference.BaseURL
	}
	return spec
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
func neoPiListingSpecs(provider, baseURL string, entries []gjson.Result, modelsDev map[string]*NeoPiModel) (specs []neoPiSpec, authoritative bool) {
	if catalog, ok := NeoPiCompletionsProvider(provider); ok {
		authoritative = catalog.Discovery.ModelsAuthoritative
	}
	index := map[string]int{}
	thinkingBases := map[string]bool{}
	for _, entry := range entries {
		defaults := neoPiDefaultSpec(entry)
		reference := neoPiBundledRow(provider, defaults.id)
		if catalog, ok := NeoPiCompletionsProvider(provider); ok && strings.HasPrefix(provider, "xiaomi-token-plan-") && !slices.Contains(catalog.Seeds, defaults.id) {
			// xiaomiModelManagerOptions references xiaomi's bundled rows,
			// tariffs included; only the region's own seeds take precedence.
			reference = neoPiBundledRow("xiaomi", defaults.id)
		}
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
			spec = neoPiMapAlibaba(defaults)
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
		case "xiaomi", "xiaomi-token-plan-sgp", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn":
			spec = neoPiMapXiaomiTokenPlan(entry, defaults, reference)
			if neoPiExcludedModel(provider, spec.id) {
				continue
			}
		case "yolo-auto":
			spec = neoPiMapYoloAuto(entry, defaults, reference)
		case "gmi-cloud":
			spec = neoPiMapGmiCloud(entry, defaults, reference)
		case "aimlapi":
			// isLikelyAimlApiChatModelId: image, audio, video and embedding
			// generators share the listing.
			spec = neoPiMapWithBundledReference(entry, defaults, reference)
			normalized := strings.ToLower(strings.TrimSpace(spec.id))
			if normalized == "" || neoPiExcludedModel("aimlapi", normalized) {
				continue
			}
		case "singularityapi-dev":
			spec = neoPiMapSingularityApi(entry, defaults)
		case "singularityapi-tech":
			// The lane roster answers bare ids; rows keep the defaults.
			spec = defaults
		case "charm-hyper":
			spec = neoPiMapCharmHyper(entry, defaults)
		case "kilo":
			// kiloModelManagerOptions: the defaults plus the per-token tariff.
			spec = defaults
			spec.cost = neoPiPerTokenPricingCost(entry.Get("pricing"))
		case "siliconflow", "siliconflow-cn":
			// isLikelySiliconFlowChatModelId: both regions share SiliconFlow's
			// exclusion rules for embeddings and generators.
			if normalized := strings.ToLower(strings.TrimSpace(defaults.id)); normalized == "" || neoPiExcludedModel("siliconflow", normalized) {
				continue
			}
			spec = neoPiMapSiliconFlow(entry, defaults, modelsDev)
		case "vllm":
			spec = neoPiMapVllm(entry, defaults, reference)
		case "zenmux":
			spec = neoPiMapZenMux(entry, defaults, baseURL)
		case cloudflare.Provider:
			spec = neoPiMapCloudflare(entry, defaults, reference, baseURL)
		case "commandcode":
			// A listed seed id gives way to the seed NeoPi appends (TypeSafe
			// Jev, a judge on its own route, which no chat port serves).
			if catalog, ok := NeoPiCompletionsProvider(provider); ok && slices.Contains(catalog.Seeds, defaults.id) {
				continue
			}
			spec = neoPiMapCommandCode(entry, defaults, baseURL)
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
		model.input = []string{"text", "image"}
		if model.contextWindow == 0 {
			model.contextWindow = limits[0]
		}
		if model.maxTokens == 0 {
			model.maxTokens = limits[1]
		}
		// The official tariff stands in for a listing that reports none.
		if model.cost.Input == 0 && model.cost.Output == 0 && model.cost.CacheRead == 0 {
			model.cost = NeoPiCost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3}
		}
		return model
	}
	isK2 := strings.HasPrefix(id, "kimi-k2")
	thinkingVariant := strings.HasPrefix(id, "kimi") && strings.HasSuffix(id, "-thinking")
	if (thinkingVariant || isK2) && !model.reasoning {
		model.reasoning = true
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
	spec := defaults
	if reference == nil {
		spec.contextWindow = neoPiPositive(entry.Get("context_length"), 0)
		spec.maxTokens = neoPiPositive(maxOutput, 0)
		spec.name = neoPiModelName(entry.Get("name"), defaults.name)
	} else {
		spec = neoPiReferenceSpec(defaults.id, reference)
		spec.name = neoPiModelName(entry.Get("name"), reference.Name)
		spec.contextWindow = neoPiPositive(entry.Get("context_length"), reference.ContextWindow)
		spec.maxTokens = neoPiPositive(maxOutput, reference.MaxTokens)
	}
	// nanoGptPricingCost: prompt and completion per million, the cache legs
	// per thousand; legs the row does not price keep the mapped tariff.
	if pricing := entry.Get("pricing"); pricing.IsObject() && pricing.Get("unit").Type == gjson.String && pricing.Get("unit").Str == "per_million_tokens" {
		perThousand := func(value gjson.Result, fallback float64) float64 {
			if rate := neoPiPositiveRate(value); rate > 0 {
				return rate * 1_000
			}
			return fallback
		}
		positive := func(value gjson.Result, fallback float64) float64 {
			if rate := neoPiPositiveRate(value); rate > 0 {
				return rate
			}
			return fallback
		}
		spec.cost = NeoPiCost{Input: positive(pricing.Get("prompt"), spec.cost.Input), Output: positive(pricing.Get("completion"), spec.cost.Output),
			CacheRead:  perThousand(pricing.Get("cacheReadInputPer1kTokens"), spec.cost.CacheRead),
			CacheWrite: perThousand(pricing.Get("cacheWriteInputPer1kTokens"), spec.cost.CacheWrite)}
	}
	return spec
}

// neoPiMapAlibaba is alibabaTokenPlanModelManagerOptions' mapModel: a listed
// seed id is NeoPi's authored seed spec whole (the manager's static models
// are the provider's seeds, not the generated bundle); others take the
// model-limits rules.
func neoPiMapAlibaba(defaults neoPiSpec) neoPiSpec {
	if seed, ok := neoPiAlibabaSeedSpec(defaults.id); ok {
		return seed
	}
	normalized := strings.ToLower(strings.TrimSpace(defaults.id))
	if limits, ok := alibabaLimits[normalized]; ok {
		defaults.contextWindow, defaults.maxTokens = limits[0], limits[1]
	}
	if neoPiIsDeepseekV4Generation("alibaba-token-plan", normalized) {
		// The V4 generation reasons on a high/max ladder only.
		defaults.reasoning = true
		defaults.thinking = &NeoPiThinking{Mode: "effort", Efforts: []string{"high", "max"}}
	}
	return defaults
}

// neoPiMapGmiCloud is mapGmiCloudModel: an id the bundle lacks borrows the
// capabilities, ladder and limits of the upstream row NeoPi's bundled
// reference index resolves it to, its output cap held to its window.
func neoPiMapGmiCloud(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	if reference != nil {
		return neoPiMapWithBundledReference(entry, defaults, reference)
	}
	canonical := neoPiResolveModelReference(defaults.id)
	if canonical == nil {
		defaults.name = neoPiModelName(entry.Get("name"), defaults.name)
		return defaults
	}
	spec := defaults
	spec.name = neoPiModelName(entry.Get("name"), canonical.Name)
	spec.reasoning = canonical.Reasoning
	spec.input = slices.Clone(canonical.Input)
	if thinking, requiresEffortSet, err := neoPiDecodeThinking(canonical.Thinking); err == nil && thinking != nil {
		spec.thinking, spec.thinkingRequiresEffortSet = thinking, requiresEffortSet
	}
	if canonical.ContextWindow > 0 {
		spec.contextWindow = canonical.ContextWindow
	}
	switch {
	case canonical.MaxTokens > 0 && spec.contextWindow > 0:
		spec.maxTokens = min(canonical.MaxTokens, spec.contextWindow)
	case canonical.MaxTokens > 0:
		spec.maxTokens = canonical.MaxTokens
	}
	return spec
}

// neoPiMapFireworks is mapFireworksControlPlaneModel. The control plane
// carries capability flags but no reasoning bit: every serverless chat model
// reasons, so unbundled ids default to reasoning.
func neoPiMapFireworks(record gjson.Result, publicID string, reference *NeoPiModel) neoPiSpec {
	fallback := publicID
	if reference != nil {
		fallback = reference.Name
	}
	// The reference row is the spec's base; an unbundled id starts bare.
	spec := neoPiSpec{id: publicID, input: []string{"text"}}
	if reference != nil {
		spec = neoPiReferenceSpec(publicID, reference)
		spec.contextWindow = neoPiPositive(record.Get("contextLength"), reference.ContextWindow)
	} else {
		spec.reasoning = true
		spec.contextWindow = neoPiPositive(record.Get("contextLength"), 0)
	}
	spec.name = neoPiModelName(record.Get("displayName"), fallback)
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
// the bundled roster: every spec is built first (buildDiscoveredModelSpecs: a
// spec NeoPi cannot build is skipped, and a listing none of whose specs build
// fails), then a discovered id replaces its bundled row additively, unknown
// ids are added, and an authoritative provider keeps only what was
// discovered. A failed rebuild of a merged row fails the listing, as it does
// NeoPi's refresh.
func neoPiMergeListing(provider, baseURL string, specs []neoPiSpec, authoritative bool) ([]*ModelInfo, error) {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	catalog, _ := NeoPiCompletionsProvider(provider)
	built := make([]*NeoPiModel, 0, len(specs))
	builtSpecs := make(map[string]*neoPiSpec, len(specs))
	var firstErr error
	for i := range specs {
		row, err := neoPiBuildRow(provider, baseURL, &specs[i])
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		built = append(built, row)
		builtSpecs[row.ID] = &specs[i]
	}
	if len(built) == 0 && firstErr != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrNeoPiListing, provider, firstErr)
	}
	byID := make(map[string]*NeoPiModel, len(built))
	for _, row := range built {
		byID[row.ID] = row
	}
	rows := make([]*NeoPiModel, 0, len(neopiCompletionsRows[provider])+len(built))
	seen := map[string]bool{}
	for _, static := range neopiCompletionsRows[provider] {
		dynamic := byID[static.ID]
		chat := static.Kind == "" || static.Kind == "chat"
		if dynamic == nil {
			// retainModelIds: an authoritative listing drops only the chat
			// rows it does not name.
			if !authoritative || !chat {
				rows = append(rows, static.Clone())
			}
			continue
		}
		seen[static.ID] = true
		if !chat && builtSpecs[static.ID].kind == "" {
			// A bundled runner row is replaced only by a listing that
			// authors a kind itself.
			rows = append(rows, static.Clone())
			continue
		}
		merged, err := neoPiMergedRow(provider, baseURL, catalog, static, dynamic, builtSpecs[static.ID])
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrNeoPiListing, provider, err)
		}
		rows = append(rows, merged)
	}
	for _, row := range built {
		if !seen[row.ID] {
			rows = append(rows, row)
		}
	}
	rows = neoPiCollapseThinkingPairs(provider, rows)
	out := make([]*ModelInfo, 0, len(rows))
	for _, row := range rows {
		if neoPiServed(row) {
			out = append(out, NeoPiCompletionsModelInfo(row, provider))
		}
	}
	if len(specs) == 0 {
		return nil, &NeoPiEmptyListing{Models: out}
	}
	return out, nil
}

// neoPiMergedRow is mergeDynamicModel for a bundled row the listing names:
// the built discovered row is layered over the bundled row (name, reasoning,
// input and limits by NeoPi's merge rules, the built ladder, and the spec's
// authored compat or else the bundled row's), and the result built again.
func neoPiMergedRow(provider, baseURL string, catalog *NeoPiCompletionsCatalog, static, dynamic *NeoPiModel, spec *neoPiSpec) (*NeoPiModel, error) {
	merged := neoPiSpec{id: static.ID, name: neoPiPreferName(dynamic.Name, static.Name, static.ID), requestModelID: static.RequestModelID}
	if dynamic.RequestModelID != "" {
		merged.requestModelID = dynamic.RequestModelID
	}
	// The discovered row's transport and endpoint win.
	merged.api = dynamic.API
	if dynamic.BaseURL != baseURL {
		merged.baseURL = dynamic.BaseURL
	}
	merged.reasoning = static.Reasoning || dynamic.Reasoning
	if catalog != nil && catalog.Discovery.ReasoningAuthoritative {
		merged.reasoning = dynamic.Reasoning
	}
	supportsImage := slices.Contains(static.Input, "image") || slices.Contains(dynamic.Input, "image")
	if static.BaseURL != dynamic.BaseURL || catalog != nil && catalog.Discovery.InputAuthoritative {
		supportsImage = slices.Contains(dynamic.Input, "image")
	}
	merged.input = []string{"text"}
	if supportsImage {
		merged.input = []string{"text", "image"}
	}
	merged.contextWindow = neoPiPreferLimit(dynamic.ContextWindow, static.ContextWindow)
	merged.maxTokens = neoPiPreferLimit(dynamic.MaxTokens, static.MaxTokens)
	merged.thinking, merged.thinkingRequiresEffortSet = dynamic.Thinking.Clone(), dynamic.thinkingRequiresEffortSet
	// The bundled compat is transport-scoped: it follows only a row whose
	// discovered API is unchanged.
	merged.compat = maps.Clone(spec.compat)
	if merged.compat == nil && dynamic.API == static.API {
		merged.compat = maps.Clone(static.CompatConfig)
	}
	costAuthoritative := catalog != nil && catalog.Discovery.CostAuthoritative
	merged.cost = NeoPiCost{
		Input:      neoPiPreferDiscoveryCost(dynamic.cost.Input, static.cost.Input, costAuthoritative),
		Output:     neoPiPreferDiscoveryCost(dynamic.cost.Output, static.cost.Output, costAuthoritative),
		CacheRead:  neoPiPreferDiscoveryCost(dynamic.cost.CacheRead, static.cost.CacheRead, costAuthoritative),
		CacheWrite: neoPiPreferDiscoveryCost(dynamic.cost.CacheWrite, static.cost.CacheWrite, costAuthoritative),
	}
	return neoPiBuildRow(provider, baseURL, &merged)
}

// neoPiPreferDiscoveryCost is preferDiscoveryCost: a discovered price wins
// when positive, or when zero is a price (cost-authoritative discovery).
func neoPiPreferDiscoveryCost(discovered, fallback float64, zeroIsPrice bool) float64 {
	if discovered > 0 || zeroIsPrice && discovered == 0 {
		return discovered
	}
	return fallback
}

// neoPiDefaultPairEfforts is DEFAULT_PAIR_EFFORTS.
var neoPiDefaultPairEfforts = []string{"minimal", "low", "medium", "high"}

// neoPiPairSurface is a ladder as derivePairThinkingSurface keeps it: no
// routing, mandatory or off-suppression flags.
func neoPiPairSurface(thinking *NeoPiThinking) *NeoPiThinking {
	if thinking == nil || len(thinking.Efforts) == 0 {
		return nil
	}
	surface := thinking.Clone()
	surface.EffortRouting, surface.RequiresEffort, surface.SuppressWhenOff = nil, false, false
	return surface
}

// neoPiCollapseThinkingPairs is the model manager's collapseBuiltVariants
// for the completions providers (none has a reviewed collapse table): each
// "<id>-thinking" style row whose base id is also listed folds into the
// base, which then routes every effort of the pair's thinking surface to the
// variant and "off" to itself, and is built again.
func neoPiCollapseThinkingPairs(provider string, rows []*NeoPiModel) []*NeoPiModel {
	byID := make(map[string]*NeoPiModel, len(rows))
	for _, row := range rows {
		if _, ok := byID[row.ID]; !ok {
			byID[row.ID] = row
		}
	}
	dropped := map[string]bool{}
	rebuilt := map[string]*NeoPiModel{}
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
		// deriveThinkingPairFamilies keeps members of different transports, or
		// both priced differently, apart.
		if row.API != base.API || row.cost.priced() && base.cost.priced() && row.cost != base.cost {
			continue
		}
		if base.Thinking != nil && base.Thinking.EffortRouting != nil {
			// The base is already collapsed: it wins and the raw member goes.
			dropped[row.ID] = true
			continue
		}
		// derivePairThinkingSurface: the variant's ladder, else the base's,
		// else what the rules derive for the variant as a reasoning row
		// (without its authored compat), else NeoPi's default pair ladder.
		surface := neoPiPairSurface(row.Thinking)
		if surface == nil {
			surface = neoPiPairSurface(base.Thinking)
		}
		if surface == nil {
			variant := neoPiSpec{id: row.ID, name: row.Name, reasoning: true, input: row.Input, contextWindow: row.ContextWindow, maxTokens: row.MaxTokens}
			if derived, err := neoPiBuildRow(provider, row.BaseURL, &variant); err == nil {
				surface = neoPiPairSurface(derived.Thinking)
			}
		}
		if surface == nil {
			surface = &NeoPiThinking{Mode: "budget", Efforts: slices.Clone(neoPiDefaultPairEfforts)}
		}
		surface.EffortRouting = map[string]string{"off": base.ID}
		for _, effort := range surface.Efforts {
			surface.EffortRouting[effort] = row.ID
		}
		input := base.Input
		if slices.Contains(row.Input, "image") {
			input = []string{"text", "image"}
		}
		// The family spreads its first member: the pair's transport and kind
		// survive the rebuild (a Decisions judge stays out of chat).
		collapsed := neoPiSpec{id: base.ID, name: base.Name, api: base.API, kind: base.Kind, reasoning: true, input: input,
			contextWindow: max(base.ContextWindow, row.ContextWindow), maxTokens: max(base.MaxTokens, row.MaxTokens),
			thinking: surface, compat: maps.Clone(base.CompatConfig), requestModelID: base.RequestModelID, cost: base.cost}
		if built, err := neoPiBuildRow(provider, base.BaseURL, &collapsed); err == nil {
			rebuilt[base.ID] = built
			byID[base.ID] = built
		}
		dropped[row.ID] = true
	}
	if len(dropped) == 0 {
		return rows
	}
	out := rows[:0:0]
	for _, row := range rows {
		if dropped[row.ID] {
			continue
		}
		if built, ok := rebuilt[row.ID]; ok {
			row = built
		}
		out = append(out, row)
	}
	return out
}
