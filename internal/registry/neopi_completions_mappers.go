package registry

import (
	_ "embed"
	"encoding/json"
	"math"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cloudflare"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicompat"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
	"github.com/tidwall/gjson"
)

// neoPiWaferMaxTokensCap is WAFER_MAX_TOKENS_CAP.
const neoPiWaferMaxTokensCap = 65536

// Per-provider discovery mappers: each is the mapModel of the provider's
// NeoPi model manager options (catalog provider-models/openai-compat.ts).

// neoPiMapZhipuCodingPlan is zhipuCodingPlanModelManagerOptions' mapModel:
// the listing contributes only ids. GLM 4.5 and later reasons (Flash from
// 5.3), as does a thinking variant; vision is rule-owned, so the row is
// text-only and the bundled row keeps its limits.
func neoPiMapZhipuCodingPlan(defaults neoPiSpec) neoPiSpec {
	identity := neopitaxonomy.Classify("zhipu-coding-plan", defaults.id)
	defaults.reasoning = neoPiIsGlmReasoningIdentity("zhipu-coding-plan", defaults.id, "4.5") || identity.ThinkingVariant
	defaults.input = []string{"text"}
	return defaults
}

// neoPiMapWafer is mapWaferModel: Wafer's "wafer" envelope carries the
// capabilities, limits and display name. The output cap is the context
// window up to WAFER_MAX_TOKENS_CAP.
func neoPiMapWafer(entry gjson.Result, defaults neoPiSpec) neoPiSpec {
	wafer := entry.Get("wafer")
	if !wafer.IsObject() {
		wafer = gjson.Result{}
	}
	capabilities := wafer.Get("capabilities")
	spec := defaults
	spec.name = neoPiModelName(wafer.Get("display_name"), defaults.name)
	spec.reasoning = capabilities.Get("reasoning").Type == gjson.True
	spec.input = []string{"text"}
	if capabilities.Get("vision").Type == gjson.True {
		spec.input = []string{"text", "image"}
	}
	spec.contextWindow = neoPiPositive(wafer.Get("context_length"), neoPiPositive(entry.Get("max_model_len"), defaults.contextWindow))
	spec.maxTokens = 0
	if spec.contextWindow > 0 {
		spec.maxTokens = min(spec.contextWindow, neoPiWaferMaxTokensCap)
	}
	// Wafer passes the body through, so a reasoning row speaks its upstream
	// backend's thinking control; every row refuses the developer role.
	spec.withCompat("supportsDeveloperRole", false)
	if spec.reasoning {
		spec.withCompat("reasoningContentField", "reasoning_content")
		if format := neoPiWaferThinkingFormat(defaults.id, wafer.Get("provider")); format != "" {
			spec.withCompat("thinkingFormat", format)
		}
	}
	// Wafer prices in cents per million; NeoPi's conversion, operation order
	// kept.
	pricing := wafer.Get("pricing")
	spec.cost = NeoPiCost{
		Input:     neoPiPositiveRate(pricing.Get("input_cents_per_million")) * 125 / 10000,
		Output:    neoPiPositiveRate(pricing.Get("output_cents_per_million")) * 125 / 10000,
		CacheRead: neoPiPositiveRate(pricing.Get("cache_read_cents_per_million")) * 125 / 10000,
	}
	return spec
}

// neoPiPositiveRate is toPositiveNumber(value, 0) for a rate.
func neoPiPositiveRate(value gjson.Result) float64 {
	if number, ok := neoPiToNumber(value); ok && number > 0 {
		return number
	}
	return 0
}

// neoPiWaferThinkingFormat is resolveWaferServerlessThinkingFormat: the
// upstream named by wafer.provider selects zai (Zhipu, Moonshot/Kimi) or
// qwen; without one, GLM 4.5+ and Kimi ids need zai and the rest resolve
// from their id.
func neoPiWaferThinkingFormat(id string, provider gjson.Result) string {
	upstream := ""
	if provider.Type == gjson.String {
		upstream = strings.ToLower(strings.TrimSpace(provider.Str))
	}
	if upstream != "" {
		switch {
		case upstream == "zai" || upstream == "z.ai" || upstream == "z-ai" || strings.Contains(upstream, "zhipu") ||
			strings.Contains(upstream, "moonshot") || strings.Contains(upstream, "kimi"):
			return "zai"
		case strings.Contains(upstream, "qwen") || strings.Contains(upstream, "alibaba") || strings.Contains(upstream, "dashscope"):
			return "qwen"
		}
		return ""
	}
	if neoPiIsGlmReasoningIdentity("wafer-serverless", id, "4.5") || neopitaxonomy.Classify("wafer-serverless", id).Class == "kimi" {
		return "zai"
	}
	return ""
}

// neoPiWireEfforts is EFFORT_BY_WIRE_VALUE.
var neoPiWireEfforts = map[string]string{"minimal": "minimal", "low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "max"}

// neoPiParseWireEfforts is parseWireEfforts: the listed efforts in wire
// order, unknown values dropped.
func neoPiParseWireEfforts(value gjson.Result) []string {
	var efforts []string
	if !value.IsArray() {
		return efforts
	}
	for _, item := range value.Array() {
		if effort, ok := neoPiWireEfforts[item.Str]; ok && item.Type == gjson.String {
			efforts = append(efforts, effort)
		}
	}
	return efforts
}

// neoPiStrings is the string members of a JSON array.
func neoPiStrings(value gjson.Result) []string {
	var out []string
	if !value.IsArray() {
		return out
	}
	for _, item := range value.Array() {
		if item.Type == gjson.String {
			out = append(out, item.Str)
		}
	}
	return out
}

// neoPiMapAiand is mapAiandModel: the listing is the whole truth (no bundled
// reference). The description is the display name; "reasoning" and
// "vision" capabilities switch those on; an advertised effort list becomes
// the effort ladder with its default.
func neoPiMapAiand(entry gjson.Result, defaults neoPiSpec) neoPiSpec {
	capabilities := neoPiStrings(entry.Get("capabilities"))
	spec := defaults
	spec.reasoning = slices.Contains(capabilities, "reasoning")
	if description := entry.Get("description"); description.Type == gjson.String && strings.TrimSpace(description.Str) != "" {
		// buildModel trims the display name.
		spec.name = strings.TrimSpace(description.Str)
	} else {
		spec.name = neoPiModelName(entry.Get("name"), defaults.name)
	}
	spec.input = []string{"text"}
	if slices.Contains(capabilities, "vision") {
		spec.input = []string{"text", "image"}
	}
	spec.contextWindow = neoPiPositive(entry.Get("context_window"), 0)
	spec.maxTokens = 0
	if spec.reasoning {
		if efforts := neoPiParseWireEfforts(entry.Get("reasoning_efforts")); len(efforts) > 0 {
			spec.thinking = &NeoPiThinking{Mode: "effort", Efforts: efforts}
			if level, ok := neoPiWireEfforts[entry.Get("reasoning_effort_default").Str]; ok && entry.Get("reasoning_effort_default").Type == gjson.String && slices.Contains(efforts, level) {
				spec.thinking.DefaultLevel = level
			}
		}
	}
	// mapAiandCost: USD per-million rates; another currency counts as unpriced.
	if currency := entry.Get("currency"); currency.Type != gjson.String || currency.Str == "usd" {
		spec.cost = NeoPiCost{Input: neoPiPositiveRate(entry.Get("input_per_1m")), Output: neoPiPositiveRate(entry.Get("output_per_1m"))}
	}
	return spec
}

// neoPiMapBaseten is basetenModelManagerOptions' mapModel. Reasoning is
// switched on only for the families with a verified Baseten reasoning policy
// (Kimi K3, GLM 5.2+, gpt-oss-120b, DeepSeek V4) and only when the listing
// advertises it; the listed image modality or the bundled row grants vision;
// listed limits win over the bundled row's.
func neoPiMapBaseten(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	features := neoPiStrings(entry.Get("supported_features"))
	modalities := neoPiStrings(entry.Get("input_modalities"))
	identity := neopitaxonomy.Classify("baseten", defaults.id)
	supported := identity.Class == "kimi" && identity.Family == "k3" ||
		neoPiIsGlmReasoningIdentity("baseten", defaults.id, "5.2") ||
		defaults.id == "openai/gpt-oss-120b" ||
		neoPiIsDeepseekV4Generation("baseten", defaults.id)
	spec := neoPiMapWithBundledReference(entry, defaults, reference)
	spec.reasoning = supported && (slices.Contains(features, "reasoning") || slices.Contains(features, "reasoning_effort"))
	spec.input = []string{"text"}
	if slices.Contains(modalities, "image") || reference != nil && slices.Contains(reference.Input, "image") {
		spec.input = []string{"text", "image"}
	}
	contextFallback, maxFallback := 0, 0
	if reference != nil {
		contextFallback, maxFallback = reference.ContextWindow, reference.MaxTokens
	}
	spec.contextWindow = neoPiPositive(entry.Get("context_length"), contextFallback)
	spec.maxTokens = neoPiPositive(entry.Get("max_completion_tokens"), maxFallback)
	// Baseten quotes OpenRouter-style per-token rates; no cache write.
	spec.cost = NeoPiCost{
		Input:     neoPiPositiveRate(entry.Get("pricing.prompt")) * 1_000_000,
		Output:    neoPiPositiveRate(entry.Get("pricing.completion")) * 1_000_000,
		CacheRead: neoPiPositiveRate(entry.Get("pricing.input_cache_read")) * 1_000_000,
	}
	return spec
}

//go:embed models/neopi_discovery/exclude_models.json
var neoPiExcludeModelsJSON []byte

// neoPiExcludeRule is one compiled exclude-models rule.
type neoPiExcludeRule struct {
	Provider string `json:"provider"`
	Match    struct {
		Exact     []string `json:"exact"`
		Prefix    []string `json:"prefix"`
		Substring []string `json:"substring"`
		Token     []string `json:"token"`
		Glob      []string `json:"glob"`
	} `json:"match"`
}

var (
	neoPiExcludeOnce  sync.Once
	neoPiExcludeRules []neoPiExcludeRule
)

// neoPiExcludedModel is isExcludedModel(provider, id): whether NeoPi's
// runtime exclude-models rules drop id (lowercased) from the provider's
// discovered roster.
func neoPiExcludedModel(provider, id string) bool {
	neoPiExcludeOnce.Do(func() {
		if err := json.Unmarshal(neoPiExcludeModelsJSON, &neoPiExcludeRules); err != nil {
			panic("registry: embedded exclude_models.json is invalid: " + err.Error())
		}
	})
	lower := strings.ToLower(id)
	var tokens []string
	for _, rule := range neoPiExcludeRules {
		if rule.Provider != provider {
			continue
		}
		match := rule.Match
		if slices.Contains(match.Exact, lower) ||
			slices.ContainsFunc(match.Prefix, func(prefix string) bool { return strings.HasPrefix(lower, prefix) }) ||
			slices.ContainsFunc(match.Substring, func(sub string) bool { return strings.Contains(lower, sub) }) ||
			slices.ContainsFunc(match.Glob, func(glob string) bool { return neopitaxonomy.GlobMatch(glob, lower) }) {
			return true
		}
		if len(match.Token) > 0 {
			if tokens == nil {
				tokens = strings.FieldsFunc(lower, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') })
			}
			if slices.ContainsFunc(match.Token, func(token string) bool { return slices.Contains(tokens, token) }) {
				return true
			}
		}
	}
	return false
}

// neoPiMapSambaNova is sambanovaModelManagerOptions' mapModel: no bundled
// reference, the listed limits only.
func neoPiMapSambaNova(entry gjson.Result, defaults neoPiSpec) neoPiSpec {
	defaults.contextWindow = neoPiPositive(entry.Get("context_length"), 0)
	defaults.maxTokens = neoPiPositive(entry.Get("max_completion_tokens"), 0)
	// sambanovaPricing: prompt and completion are required, cache legs default
	// to 0; an unreadable card stays unpriced.
	pricing := entry.Get("pricing")
	if !pricing.IsObject() {
		pricing = gjson.Result{}
	}
	rate := func(value gjson.Result, required bool) (float64, bool) {
		if !value.Exists() || value.Type == gjson.Null {
			return 0, !required
		}
		number, ok := neoPiToNumber(value)
		if !ok || number < 0 {
			return 0, false
		}
		return neoPiSettleRate(number), true
	}
	input, okInput := rate(pricing.Get("prompt"), true)
	output, okOutput := rate(pricing.Get("completion"), true)
	cacheRead, okRead := rate(pricing.Get("input_cache_read"), false)
	cacheWrite, okWrite := rate(pricing.Get("input_cache_write"), false)
	if okInput && okOutput && okRead && okWrite {
		defaults.cost = NeoPiCost{Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite}
	}
	return defaults
}

// neoPiSettleRate is Math.round(rate * 1e12) / 1e6: a per-token rate as USD
// per million, settled to a millionth so the scaling does not drift.
func neoPiSettleRate(rate float64) float64 {
	return math.Round(rate*1e12) / 1e6
}

// neoPiLadder is THINKING_EFFORTS filtered to the listed wire values.
func neoPiLadder(wire []string) []string {
	var efforts []string
	for _, effort := range neoPiThinkingEfforts {
		if slices.Contains(wire, effort) {
			efforts = append(efforts, effort)
		}
	}
	return efforts
}

// neoPiSyntheticFallbackMaxTokens is SYNTHETIC_FALLBACK_MAX_TOKENS.
const neoPiSyntheticFallbackMaxTokens = 8192

// neoPiSyntheticThinking is resolveSyntheticThinking: the advertised tiers;
// a wire "none" becomes a minimal tier sent as "none".
func neoPiSyntheticThinking(wire []string) *NeoPiThinking {
	efforts := neoPiLadder(wire)
	hasNone := slices.Contains(wire, "none")
	switch {
	case len(efforts) == 0 && !hasNone:
		return nil
	case len(efforts) == 0:
		return &NeoPiThinking{Mode: "effort", Efforts: []string{"minimal"}, EffortMap: map[string]string{"minimal": "none"}}
	case !hasNone || slices.Contains(efforts, "minimal"):
		return &NeoPiThinking{Mode: "effort", Efforts: efforts}
	}
	return &NeoPiThinking{Mode: "effort", Efforts: append([]string{"minimal"}, efforts...), EffortMap: map[string]string{"minimal": "none"}}
}

// neoPiMapSynthetic is syntheticModelManagerOptions' mapModel. An
// advertised effort vocabulary or modality list is authoritative over the
// bundled row; when the listing is silent the bundled row (or the listing's
// supports_* flags) decides. Unbundled rows get the 8k output placeholder.
func neoPiMapSynthetic(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	features := neoPiStrings(entry.Get("supported_features"))
	modalities := neoPiStrings(entry.Get("input_modalities"))
	var wire []string
	if parameters := entry.Get("reasoning_parameters"); parameters.IsObject() {
		wire = neoPiStrings(parameters.Get("efforts"))
	}
	thinking := neoPiSyntheticThinking(wire)
	named := 0
	if thinking != nil {
		named = len(thinking.Efforts)
	}
	if slices.Contains(wire, "none") {
		named--
	}
	spec := defaults
	fallbackName, contextFallback, maxFallback := defaults.name, 0, neoPiSyntheticFallbackMaxTokens
	referenceImage := false
	if reference != nil {
		spec.input = slices.Clone(reference.Input)
		fallbackName, contextFallback = reference.Name, reference.ContextWindow
		if reference.MaxTokens > 0 {
			maxFallback = reference.MaxTokens
		} else {
			maxFallback = 0
		}
		referenceImage = slices.Contains(reference.Input, "image")
	}
	spec.name = neoPiModelName(entry.Get("name"), fallbackName)
	switch {
	case (slices.Contains(features, "reasoning") || len(wire) > 0) && named > 0:
		spec.reasoning = true
	case len(wire) > 0:
		spec.reasoning = false
	default:
		spec.reasoning = entry.Get("supports_reasoning").Type == gjson.True || reference != nil && reference.Reasoning
	}
	spec.thinking = thinking
	switch {
	case len(modalities) > 0 && slices.Contains(modalities, "image"),
		len(modalities) == 0 && (entry.Get("supports_vision").Type == gjson.True || referenceImage):
		spec.input = []string{"text", "image"}
	default:
		spec.input = []string{"text"}
	}
	spec.contextWindow = neoPiPositive(entry.Get("context_length"), contextFallback)
	maxField := entry.Get("max_output_length")
	if !maxField.Exists() || maxField.Type == gjson.Null {
		maxField = entry.Get("max_tokens")
	}
	spec.maxTokens = neoPiPositive(maxField, maxFallback)
	// resolveSyntheticCost: prompt and completion are required ("$" allowed);
	// otherwise the reference's tariff stands.
	var fallback NeoPiCost
	if reference != nil {
		fallback = reference.cost
	}
	spec.cost = fallback
	if pricing := entry.Get("pricing"); pricing.IsObject() {
		input, okInput := neoPiSyntheticRate(pricing.Get("prompt"))
		output, okOutput := neoPiSyntheticRate(pricing.Get("completion"))
		if okInput && okOutput {
			spec.cost = NeoPiCost{Input: input, Output: output, CacheRead: fallback.CacheRead, CacheWrite: fallback.CacheWrite}
			if read, ok := neoPiSyntheticRate(pricing.Get("input_cache_reads")); ok {
				spec.cost.CacheRead = read
			}
			if write, ok := neoPiSyntheticRate(pricing.Get("input_cache_writes")); ok {
				spec.cost.CacheWrite = write
			}
		}
	}
	return spec
}

// neoPiSyntheticRate is toSyntheticCostPerMillion: a per-token rate (a string
// may lead with "$") settled per million.
func neoPiSyntheticRate(value gjson.Result) (float64, bool) {
	if value.Type == gjson.String {
		value = gjson.Result{Type: gjson.String, Str: strings.TrimPrefix(strings.TrimSpace(value.Str), "$")}
	}
	number, ok := neoPiToNumber(value)
	if !ok || number < 0 {
		return 0, false
	}
	return neoPiSettleRate(number), true
}

// neoPiMapCoralbricks is mapCoralbricksModel: rows with supports_chat false
// are dropped (ok is false). The listing's flags are authoritative: an
// explicit supports_reasoning decides reasoning, an advertised
// reasoning.supported_efforts becomes the ladder, supports_image_input the
// modality. Names and output caps come from the bundled row only.
func neoPiMapCoralbricks(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) (neoPiSpec, bool) {
	if entry.Get("supports_chat").Type == gjson.False {
		return neoPiSpec{}, false
	}
	spec := defaults
	if reference != nil {
		spec.name = reference.Name
		spec.reasoning = reference.Reasoning
		spec.input = slices.Clone(reference.Input)
	}
	reasoningFlag := entry.Get("supports_reasoning")
	switch reasoningFlag.Type {
	case gjson.True:
		spec.reasoning = true
	case gjson.False:
		spec.reasoning = false
	}
	controls := entry.Get("reasoning")
	if !controls.IsObject() {
		controls = gjson.Result{}
	}
	wire := controls.Get("supported_efforts")
	// An explicit flag or vocabulary is the row's whole truth for its dial:
	// the bundled ladder survives only while the row still reasons, and an
	// advertised vocabulary replaces it (an empty one leaves no dial).
	if reasoningFlag.IsBool() || wire.IsArray() {
		spec.withCompat("trustExplicitThinkingOnly", true)
		if spec.reasoning && reference != nil {
			spec.thinking = reference.Thinking.Clone()
		}
	}
	if spec.reasoning && wire.IsArray() {
		spec.thinking = nil
		if efforts := neoPiLadder(neoPiStrings(wire)); len(efforts) > 0 {
			spec.thinking = &NeoPiThinking{Mode: "effort", Efforts: efforts}
			if level := controls.Get("default_effort"); level.Type == gjson.String && slices.Contains(efforts, level.Str) {
				spec.thinking.DefaultLevel = level.Str
			}
			if mandatory := controls.Get("mandatory"); mandatory.IsBool() {
				spec.thinking.RequiresEffort, spec.thinkingRequiresEffortSet = mandatory.Bool(), true
			}
		}
	}
	if spec.reasoning && controls.Exists() {
		disable := controls.Get("disable")
		switch {
		case controls.Get("mandatory").Type == gjson.True || disable.Type == gjson.Null && disable.Exists():
			spec.withCompat("reasoningDisableMode", "lowest-effort")
		case disable.IsObject() && disable.Get("reasoning_effort").Str == "none" && disable.Get("reasoning_effort").Type == gjson.String:
			spec.withCompat("reasoningDisableMode", "none-effort")
		}
	}
	switch entry.Get("supports_image_input").Type {
	case gjson.True:
		spec.input = []string{"text", "image"}
	case gjson.False:
		spec.input = []string{"text"}
	}
	contextFallback, maxTokens := 0, 0
	if reference != nil {
		contextFallback, maxTokens = reference.ContextWindow, reference.MaxTokens
	}
	spec.contextWindow = neoPiPositive(entry.Get("context_length"), contextFallback)
	spec.maxTokens = maxTokens
	// Coral quotes per-million rates; an unpriced leg keeps the reference's.
	pricing := entry.Get("pricing")
	if !pricing.IsObject() {
		pricing = gjson.Result{}
	}
	var fallback NeoPiCost
	if reference != nil {
		fallback = reference.cost
	}
	spec.cost = NeoPiCost{
		Input:      neoPiRate(pricing.Get("input_per_m"), fallback.Input),
		Output:     neoPiRate(pricing.Get("output_per_m"), fallback.Output),
		CacheRead:  neoPiRate(pricing.Get("cached_input_per_m"), fallback.CacheRead),
		CacheWrite: neoPiRate(pricing.Get("cache_write_per_m"), fallback.CacheWrite),
	}
	return spec, true
}

// neoPiRate is the "a finite rate of zero or more, else fallback" parse
// (coralRate, toCharmHyperRate, toSingularityApiRate).
func neoPiRate(value gjson.Result, fallback float64) float64 {
	if number, ok := neoPiToNumber(value); ok && number >= 0 {
		return number
	}
	return fallback
}

// neoPiInputCapabilities is toInputCapabilities: image when the list names
// it, text otherwise.
func neoPiInputCapabilities(value gjson.Result) []string {
	if slices.Contains(neoPiStrings(value), "image") {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

// neoPiMapInception is inceptionModelManagerOptions' mapModel: only rows
// whose supported_endpoints name chat.completions are chat models (ok is
// false otherwise); listed modalities and limits win over the bundled row.
func neoPiMapInception(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) (neoPiSpec, bool) {
	if !slices.Contains(neoPiStrings(entry.Get("supported_endpoints")), "chat.completions") {
		return neoPiSpec{}, false
	}
	spec := neoPiMapWithBundledReference(entry, defaults, reference)
	if modalities := entry.Get("input_modalities"); modalities.IsArray() {
		spec.input = neoPiInputCapabilities(modalities)
	}
	spec.contextWindow = neoPiPositive(entry.Get("context_length"), spec.contextWindow)
	spec.maxTokens = neoPiPositive(entry.Get("max_output_length"), spec.maxTokens)
	// A pricing record replaces the reference's tariff.
	if pricing := entry.Get("pricing"); pricing.IsObject() {
		spec.cost = neoPiPerTokenPricingCost(pricing)
	}
	return spec, true
}

// neoPiNovitaListed is novitaModelManagerOptions' filterModel: an active,
// public chat-completions row with a positive output cap.
func neoPiNovitaListed(entry gjson.Result, id string) bool {
	status := entry.Get("status")
	active := status.Type != gjson.Number || status.Num == 1
	return active && !strings.HasPrefix(strings.ToLower(id), "ai_infer_test") &&
		slices.Contains(neoPiStrings(entry.Get("endpoints")), "chat/completions") &&
		neoPiPositive(entry.Get("max_output_tokens"), 0) > 0
}

// neoPiMapNovita is mapNovitaModel: the display name (display_name, title,
// then name), the listed features and modalities, and the listed limits.
func neoPiMapNovita(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	name := entry.Get("display_name")
	if name.Type == gjson.Null || !name.Exists() {
		name = entry.Get("title")
	}
	if name.Type == gjson.Null || !name.Exists() {
		name = entry.Get("name")
	}
	fallback := defaults.name
	if reference != nil {
		fallback = reference.Name
	}
	spec := neoPiMapWithBundledReference(entry, defaults, reference)
	spec.name = neoPiModelName(name, fallback)
	spec.reasoning = slices.Contains(neoPiStrings(entry.Get("features")), "reasoning")
	spec.input = neoPiInputCapabilities(entry.Get("input_modalities"))
	spec.contextWindow = neoPiPositive(entry.Get("context_size"), spec.contextWindow)
	spec.maxTokens = neoPiPositive(entry.Get("max_output_tokens"), spec.maxTokens)
	// Novita quotes ten-thousandths of a dollar per million (toNovitaCostPerMillion).
	var cacheRead float64
	if read := entry.Get("pricing.input_cache_read"); entry.Get("pricing").IsObject() && read.IsObject() {
		cacheRead = neoPiPositiveRate(read.Get("price_per_m")) / 10_000
	}
	spec.cost = NeoPiCost{Input: neoPiPositiveRate(entry.Get("input_token_price_per_m")) / 10_000,
		Output: neoPiPositiveRate(entry.Get("output_token_price_per_m")) / 10_000, CacheRead: cacheRead}
	return spec
}

// neoPiDeepinfraEfforts is DEEPINFRA_EFFORTS.
var neoPiDeepinfraEfforts = []string{"low", "medium", "high"}

// neoPiMapDeepinfra is mapDeepinfraModel over one row of DeepInfra's
// filter=with_meta catalog: only rows tagged "chat" are kept (ok is false
// otherwise). The tags carry reasoning, the advertised effort dial and
// vision; metadata.max_tokens is an output cap only when it is below the
// context window, else the bundled cap (clamped to the window) applies.
func neoPiMapDeepinfra(entry gjson.Result) (neoPiSpec, bool) {
	id := ""
	if raw := entry.Get("id"); raw.Type == gjson.String {
		id = strings.TrimSpace(raw.Str)
	}
	if id == "" {
		return neoPiSpec{}, false
	}
	metadata := entry.Get("metadata")
	if !metadata.IsObject() {
		metadata = gjson.Result{}
	}
	tags := neoPiStrings(metadata.Get("tags"))
	if !slices.Contains(tags, "chat") {
		return neoPiSpec{}, false
	}
	var reference *NeoPiModel
	if raw := entry.Get("id"); raw.Type == gjson.String {
		reference = neoPiBundledRow("deepinfra", raw.Str)
	}
	spec := neoPiSpec{id: id, name: id, input: []string{"text"}}
	contextFallback, referenceMax := 0, 0
	if reference != nil {
		spec.name = reference.Name
		contextFallback, referenceMax = reference.ContextWindow, reference.MaxTokens
	}
	spec.reasoning = slices.Contains(tags, "reasoning") || slices.Contains(tags, "reasoning_effort")
	if slices.Contains(tags, "reasoning_effort") {
		spec.thinking = &NeoPiThinking{Mode: "effort", Efforts: slices.Clone(neoPiDeepinfraEfforts)}
	}
	if slices.Contains(tags, "vision") || slices.Contains(tags, "vlm") {
		spec.input = []string{"text", "image"}
	}
	spec.contextWindow = neoPiPositive(metadata.Get("context_length"), contextFallback)
	liveMax := neoPiPositive(metadata.Get("max_tokens"), 0)
	switch {
	case spec.contextWindow > 0 && liveMax > 0 && liveMax < spec.contextWindow:
		spec.maxTokens = liveMax
	case referenceMax > 0 && spec.contextWindow > 0:
		spec.maxTokens = min(referenceMax, spec.contextWindow)
	default:
		spec.maxTokens = referenceMax
	}
	// The list rates, with an advertised discount in (0, 1) folded in.
	pricing := metadata.Get("pricing")
	if !pricing.IsObject() {
		pricing = gjson.Result{}
	}
	multiplier := 1.0
	if discount, ok := neoPiToNumber(metadata.Get("discount")); ok && discount > 0 && discount < 1 {
		multiplier = 1 - discount
	}
	spec.cost = NeoPiCost{Input: neoPiPositiveRate(pricing.Get("input_tokens")) * multiplier,
		Output:    neoPiPositiveRate(pricing.Get("output_tokens")) * multiplier,
		CacheRead: neoPiPositiveRate(pricing.Get("cache_read_tokens")) * multiplier}
	return spec, true
}

// neoPiMapVenice is veniceModelManagerOptions' mapModel: Venice streams no
// usage chunk for listed models.
func neoPiMapVenice(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	spec := neoPiMapWithBundledReference(entry, defaults, reference)
	spec.withCompat("supportsUsageInStreaming", false)
	return spec
}

// neoPiStepfunChat is isStepfunChatModelId.
func neoPiStepfunChat(id string) bool {
	normalized := strings.ToLower(strings.TrimSpace(id))
	return normalized != "" && !neoPiExcludedModel("stepfun", normalized)
}

// neoPiMapStepfun is stepfunModelManagerOptions' mapModel: an unbundled row
// that advertises reasoning_effort_support_list reasons with that ladder.
func neoPiMapStepfun(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	spec := neoPiMapWithBundledReference(entry, defaults, reference)
	if reference != nil {
		return spec
	}
	if efforts := neoPiLadder(neoPiStrings(entry.Get("reasoning_effort_support_list"))); len(efforts) > 0 {
		spec.reasoning = true
		spec.thinking = &NeoPiThinking{Mode: "effort", Efforts: efforts}
	}
	return spec
}

// neoPiMapXiaomiTokenPlan is xiaomiModelManagerOptions' mapModel for the
// Token Plan regions: the bundled row lends its metadata and the listing's
// display_name names the row.
func neoPiMapXiaomiTokenPlan(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	spec := neoPiMapWithBundledReference(entry, defaults, reference)
	spec.name = neoPiModelName(entry.Get("display_name"), spec.name)
	return spec
}

// neoPiMapYoloAuto is mapYoloAutoModel: an advertised "thinking" effort list
// is the row's ladder (keeping the reference's default level when listed)
// and makes it reason.
func neoPiMapYoloAuto(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	spec := neoPiMapWithBundledReference(entry, defaults, reference)
	efforts := neoPiParseWireEfforts(entry.Get("thinking"))
	if len(efforts) == 0 {
		return spec
	}
	thinking := &NeoPiThinking{Mode: "effort"}
	if reference != nil && reference.Thinking != nil {
		thinking = reference.Thinking.Clone()
		if thinking.Mode == "" {
			thinking.Mode = "effort"
		}
	}
	if !slices.Contains(efforts, thinking.DefaultLevel) {
		thinking.DefaultLevel = ""
	}
	thinking.Efforts = efforts
	spec.thinking = thinking
	spec.reasoning = true
	return spec
}

// SingularityAPI capability endpoints that decide a row's transport.
const (
	neoPiSingularityAPIChatEndpoint  = "/v1/chat/completions"
	neoPiSingularityAPIImageEndpoint = "/v1/images/generations"
)

// neoPiMapSingularityApi is mapSingularityApiModel: a row whose capabilities
// serve chat completions takes that capability's window and output cap, and
// a row whose only surface is image generation moves to openai-images.
func neoPiMapSingularityApi(entry gjson.Result, defaults neoPiSpec) neoPiSpec {
	var chat gjson.Result
	image := false
	if capabilities := entry.Get("capabilities"); capabilities.IsArray() {
		for _, capability := range capabilities.Array() {
			if !capability.IsObject() {
				continue
			}
			endpoint := capability.Get("endpoint")
			if endpoint.Type != gjson.String {
				continue
			}
			switch endpoint.String() {
			case neoPiSingularityAPIChatEndpoint:
				if !chat.Exists() {
					chat = capability
				}
			case neoPiSingularityAPIImageEndpoint:
				image = true
			}
		}
	}
	spec := defaults
	spec.name = neoPiModelName(entry.Get("name"), defaults.name)
	if !chat.Exists() && image {
		spec.api, spec.kind = "openai-images", "image"
		spec.reasoning = false
		spec.input = []string{"text", "image"}
		spec.contextWindow, spec.maxTokens = 0, 0
		return spec
	}
	spec.contextWindow = neoPiPositive(chat.Get("context_window_tokens"), defaults.contextWindow)
	spec.maxTokens = neoPiPositive(chat.Get("maximum_output_tokens"), defaults.maxTokens)
	// resolveSingularityApiCost: the chat capability's per-million rates.
	if pricing := chat.Get("pricing"); pricing.IsObject() {
		spec.cost = NeoPiCost{Input: neoPiRate(pricing.Get("input_per_million_usd"), 0), Output: neoPiRate(pricing.Get("output_per_million_usd"), 0)}
	}
	return spec
}

// neoPiV1BaseProviders are the providers whose configured base URL NeoPi
// resolves onto the gateway's /v1 surface (normalizeSingularityApiBaseUrl,
// normalizeCharmHyperBaseUrl).
var neoPiV1BaseProviders = map[string]bool{"singularityapi-dev": true, "singularityapi-tech": true, "charm-hyper": true, "commandcode": true}

// NeoPiV1BaseURL is the discovery and inference endpoint of a provider in
// neoPiV1BaseProviders: a blank configured URL is the provider's canonical
// endpoint, and any other keeps its host and gains a missing /v1. ZenMux's
// Anthropic route names its OpenAI one (normalizeZenMuxOpenAiBaseUrl), and
// an Ollama account's rows run at its server's /v1 (NeoPiOllamaBaseURL: the
// configured origin, else NeoPi's implicit server). Other providers' URLs
// are returned trimmed, the catalog's when blank.
func NeoPiV1BaseURL(provider, configured string) string {
	if provider == NeoPiOllamaProvider {
		return NeoPiOllamaBaseURL(configured, os.Getenv)
	}
	trimmed := strings.TrimRight(strings.TrimSpace(configured), "/")
	if trimmed == "" {
		return NeoPiCompletionsListingBase(provider)
	}
	if provider == "zenmux" && strings.HasSuffix(trimmed, "/api/anthropic") {
		return strings.Replace(trimmed, "/api/anthropic", "/api/v1", 1)
	}
	if !neoPiV1BaseProviders[provider] || strings.HasSuffix(trimmed, "/v1") {
		return trimmed
	}
	return trimmed + "/v1"
}

// NeoPiAccountBaseURL is the endpoint a NeoPi provider's account serves from
// when it configures one (NeoPi's provider baseUrl override), normalized as
// NeoPiV1BaseURL does. Empty when the account configures none.
func NeoPiAccountBaseURL(provider, configured string) string {
	if strings.TrimSpace(configured) == "" {
		return ""
	}
	return NeoPiV1BaseURL(provider, configured)
}

// neoPiCharmHyperEffortNone is Hyper's thinking-off wire tier.
const neoPiCharmHyperEffortNone = "none"

// neoPiMapCharmHyper is charmHyperModelManagerOptions' mapModel: the ladder
// is the advertised effort vocabulary (none excluded: it is the off switch,
// routed through reasoningDisableMode), with the advertised default; a row
// without one exposes no dial.
func neoPiMapCharmHyper(entry gjson.Result, defaults neoPiSpec) neoPiSpec {
	var wire []string
	reasoning := entry.Get("reasoning")
	if levels := reasoning.Get("effort_levels"); reasoning.IsObject() && levels.IsArray() {
		for _, level := range levels.Array() {
			if value := level.Get("value"); level.IsObject() && value.Type == gjson.String && !slices.Contains(wire, value.Str) {
				wire = append(wire, value.Str)
			}
		}
	}
	var efforts []string
	for _, effort := range neoPiThinkingEfforts {
		if slices.Contains(wire, effort) {
			efforts = append(efforts, effort)
		}
	}
	spec := defaults
	spec.name = neoPiModelName(entry.Get("display_name"), defaults.name)
	spec.reasoning = len(efforts) > 0
	if spec.reasoning {
		spec.thinking = &NeoPiThinking{Mode: "effort", Efforts: efforts}
		if advertised := reasoning.Get("default_effort_level"); advertised.Type == gjson.String && slices.Contains(efforts, advertised.Str) {
			spec.thinking.DefaultLevel = advertised.Str
		}
		if slices.Contains(wire, neoPiCharmHyperEffortNone) {
			spec.withCompat("reasoningDisableMode", "none-effort")
		}
	}
	spec.input = []string{"text"}
	if capabilities := entry.Get("capabilities"); capabilities.IsObject() && capabilities.Get("vision").Type == gjson.True {
		spec.input = []string{"text", "image"}
	}
	spec.contextWindow = neoPiPositive(entry.Get("context_window"), defaults.contextWindow)
	spec.maxTokens = neoPiPositive(entry.Get("max_output_tokens"), defaults.maxTokens)
	// resolveCharmHyperCost: per-million rates; zero is a real (free) rate.
	if pricing := entry.Get("pricing"); pricing.IsObject() {
		spec.cost = NeoPiCost{Input: neoPiRate(pricing.Get("input"), 0), Output: neoPiRate(pricing.Get("output"), 0),
			CacheRead: neoPiRate(pricing.Get("cache_hit"), 0), CacheWrite: neoPiRate(pricing.Get("cache_create"), 0)}
	}
	return spec
}

// neoPiZenMuxAnthropicBase is toZenMuxAnthropicBaseUrl: the Anthropic route
// beside ZenMux's OpenAI base (its /api/v1 swapped for /api/anthropic, any
// other path replaced), the default route when the base does not parse.
func neoPiZenMuxAnthropicBase(openAIBase string) string {
	parsed, err := url.Parse(openAIBase)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "https://zenmux.ai/api/anthropic"
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if strings.HasSuffix(path, "/api/v1") {
		path = strings.TrimSuffix(path, "/api/v1") + "/api/anthropic"
	} else {
		path = "/api/anthropic"
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host) + path
}

// neoPiZenMuxPrice is getZenMuxPricingValue: the first numeric value of a
// pricings bucket, 0 when it has none.
func neoPiZenMuxPrice(pricings gjson.Result, key string) float64 {
	bucket := pricings.Get(key)
	if !bucket.IsArray() {
		return 0
	}
	for _, item := range bucket.Array() {
		if !item.IsObject() {
			continue
		}
		if value, ok := neoPiToNumber(item.Get("value")); ok {
			return value
		}
	}
	return 0
}

// neoPiMapZenMux is zenmuxModelManagerOptions' mapModel: Claude models (by
// owner or classification) move to ZenMux's Anthropic Messages route, the
// rest stay on Chat Completions; the listing's display name, reasoning flag,
// modalities, bucketed prices and limits fill the row.
func neoPiMapZenMux(entry gjson.Result, defaults neoPiSpec, openAIBase string) neoPiSpec {
	spec := defaults
	owner := entry.Get("owned_by")
	if owner.Type == gjson.String && strings.ToLower(owner.Str) == "anthropic" ||
		neopitaxonomy.Classify("zenmux", defaults.id).Class == "anthropic" {
		spec.api, spec.baseURL = neopicompat.APIAnthropicMessages, neoPiZenMuxAnthropicBase(openAIBase)
	}
	spec.name = neoPiModelName(entry.Get("display_name"), defaults.name)
	if capabilities := entry.Get("capabilities"); capabilities.IsObject() && capabilities.Get("reasoning").Type == gjson.True {
		spec.reasoning = true
	}
	spec.input = neoPiInputCapabilities(entry.Get("input_modalities"))
	var pricings gjson.Result
	if value := entry.Get("pricings"); value.IsObject() {
		pricings = value
	}
	cacheWrite := neoPiZenMuxPrice(pricings, "input_cache_write_1_h")
	if cacheWrite <= 0 {
		cacheWrite = neoPiZenMuxPrice(pricings, "input_cache_write_5_min")
	}
	if cacheWrite <= 0 {
		cacheWrite = neoPiZenMuxPrice(pricings, "input_cache_write")
	}
	spec.cost = NeoPiCost{
		Input:      neoPiZenMuxPrice(pricings, "prompt"),
		Output:     neoPiZenMuxPrice(pricings, "completion"),
		CacheRead:  neoPiZenMuxPrice(pricings, "input_cache_read"),
		CacheWrite: cacheWrite,
	}
	spec.contextWindow = neoPiPositive(entry.Get("context_length"), defaults.contextWindow)
	spec.maxTokens = neoPiPositive(entry.Get("max_completion_tokens"), defaults.maxTokens)
	return spec
}

// neoPiMapCommandCode is commandCodeModelManagerOptions' mapModel: the id's
// route in NeoPi's api-routes table (Claude ids to Anthropic Messages at the
// provider root, the listed ids to Responses, every other id to Chat
// Completions under /v1), the listing's name, and only its context window;
// reasoning, input and output stay neutral for KDL policy to fill.
func neoPiMapCommandCode(entry gjson.Result, defaults neoPiSpec, listingBase string) neoPiSpec {
	spec := defaults
	spec.name = neoPiModelName(entry.Get("name"), defaults.name)
	route, _ := neoPiAPIRouteFor("commandcode", defaults.id)
	switch route {
	case neopicompat.APIAnthropicMessages:
		spec.api, spec.baseURL = route, strings.TrimSuffix(listingBase, "/v1")
	case neopicompat.APIOpenAIResponses:
		spec.api = route
	}
	spec.contextWindow = neoPiPositive(entry.Get("context_length"), 0)
	spec.maxTokens = 0
	return spec
}

// NeoPiCloudflareListingBase is the base cloudflareAiGatewayModelManagerOptions
// lists from (normalizeAnthropicBaseUrl): the account's configured endpoint,
// else NeoPi's Anthropic gateway base. The catalog's own base, which a key
// added from the dashboard carries, configures no endpoint.
func NeoPiCloudflareListingBase(configured string) string {
	value := strings.TrimSpace(configured)
	if value == "" || strings.TrimSuffix(value, "/") == NeoPiCompletionsListingBase(cloudflare.Provider) {
		return cloudflare.GatewayBase + "/anthropic"
	}
	return strings.TrimSuffix(value, "/")
}

// NeoPiCloudflarePlaceholders reports a base whose gateway placeholders no
// request has filled; NeoPi's listing there never reaches an account.
func NeoPiCloudflarePlaceholders(base string) bool {
	return strings.Contains(base, "<account>") || strings.Contains(base, "<gateway>")
}

// neoPiMapCloudflare is cloudflareAiGatewayModelManagerOptions' mapModel (an
// Anthropic Messages row on the listing base, named by display_name), routed
// as cloudflareAiGatewayTransport.prepareModel routes it (neoPiCloudflareRoute).
func neoPiMapCloudflare(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel, baseURL string) neoPiSpec {
	spec := neoPiMapWithBundledReference(entry, defaults, reference)
	spec.name = neoPiModelName(entry.Get("display_name"), spec.name)
	spec.api, spec.baseURL, spec.requestModelID = neopicompat.APIAnthropicMessages, baseURL, ""
	if route, ok := neoPiCloudflareRoute(spec.id, baseURL); ok {
		spec.baseURL = route.baseURL
		if route.api == neopicompat.APIAnthropicMessages {
			spec.requestModelID = route.requestModelID
		} else {
			spec.api = ""
			if route.stripped {
				spec.requestModelID = route.requestModelID
			}
		}
	}
	return spec
}

// neoPiCloudflareRouting is one route of NeoPi's Cloudflare api-routes.
type neoPiCloudflareRouting struct {
	api            string
	baseURL        string
	requestModelID string
	// stripped marks a route that strips its prefix (anthropic/, openai/);
	// requestModelID is meaningful only then.
	stripped bool
}

// neoPiCloudflareRoute is apiRouteFor("cloudflare-ai-gateway", id) with
// prepareModel's endpoint choice: anthropic/ ids ride Anthropic Messages under
// their prefix-stripped id with periods as hyphens, openai/ ids ride Chat
// Completions under their prefix-stripped id, and workers-ai/ ids ride Chat
// Completions under their own id. A base still on the gateway's placeholders
// moves to the route's own gateway base; any other base is kept.
func neoPiCloudflareRoute(id, baseURL string) (neoPiCloudflareRouting, bool) {
	routeBase := func(route string) string {
		if NeoPiCloudflarePlaceholders(baseURL) {
			return cloudflare.GatewayBase + route
		}
		return baseURL
	}
	switch {
	case strings.HasPrefix(id, "anthropic/"):
		return neoPiCloudflareRouting{api: neopicompat.APIAnthropicMessages, baseURL: routeBase("/anthropic"),
			requestModelID: strings.ReplaceAll(strings.TrimPrefix(id, "anthropic/"), ".", "-"), stripped: true}, true
	case strings.HasPrefix(id, "openai/"):
		return neoPiCloudflareRouting{api: neopicompat.APIOpenAICompletions, baseURL: routeBase("/openai"),
			requestModelID: strings.TrimPrefix(id, "openai/"), stripped: true}, true
	case strings.HasPrefix(id, "workers-ai/"):
		return neoPiCloudflareRouting{api: neopicompat.APIOpenAICompletions, baseURL: routeBase("/compat")}, true
	}
	return neoPiCloudflareRouting{}, false
}

// NeoPiCloudflarePrepareModel is cloudflareAiGatewayTransport.prepareModel,
// which NeoPi runs on every request's row, bundled, discovered or configured:
// a routed id moves to its route's transport, endpoint and wire id. The
// Anthropic route keeps the rest of the row as is; a row moved onto Chat
// Completions is rebuilt through NeoPi's compat engine from its authored
// compat. A row already on its route is returned unchanged. Nil when the
// compat engine cannot build the routed row, as NeoPi's buildModel throws.
func NeoPiCloudflarePrepareModel(row *NeoPiModel) *NeoPiModel {
	if row == nil {
		return nil
	}
	route, ok := neoPiCloudflareRoute(row.ID, row.BaseURL)
	if !ok {
		return row
	}
	if route.api == neopicompat.APIAnthropicMessages {
		if row.API == route.api && row.BaseURL == route.baseURL && row.RequestModelID == route.requestModelID {
			return row
		}
		prepared := row.Clone()
		prepared.API, prepared.BaseURL, prepared.RequestModelID = route.api, route.baseURL, route.requestModelID
		return prepared
	}
	if row.API == route.api && row.BaseURL == route.baseURL && row.RequestModelID == route.requestModelID {
		return row
	}
	spec := neoPiReferenceSpec(row.ID, row)
	spec.api, spec.baseURL = "", route.baseURL
	if route.stripped {
		spec.requestModelID = route.requestModelID
	}
	prepared, err := neoPiBuildRow(row.Provider, route.baseURL, &spec)
	if err != nil {
		return nil
	}
	return prepared
}
