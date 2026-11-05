package registry

import (
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
	"sync"

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
	if defaults.reasoning {
		defaults.kind = neoPiKindReasoning
	}
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
	if spec.reasoning {
		spec.kind = neoPiKindReasoning
	}
	spec.input = []string{"text"}
	if capabilities.Get("vision").Type == gjson.True {
		spec.input = []string{"text", "image"}
	}
	spec.contextWindow = neoPiPositive(wafer.Get("context_length"), neoPiPositive(entry.Get("max_model_len"), defaults.contextWindow))
	spec.maxTokens = 0
	if spec.contextWindow > 0 {
		spec.maxTokens = min(spec.contextWindow, neoPiWaferMaxTokensCap)
	}
	return spec
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
		spec.kind = neoPiKindReasoning
		if efforts := neoPiParseWireEfforts(entry.Get("reasoning_efforts")); len(efforts) > 0 {
			spec.thinking = &NeoPiThinking{Mode: "effort", Efforts: efforts}
			if level, ok := neoPiWireEfforts[entry.Get("reasoning_effort_default").Str]; ok && entry.Get("reasoning_effort_default").Type == gjson.String && slices.Contains(efforts, level) {
				spec.thinking.DefaultLevel = level
			}
		}
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
	spec.kind = ""
	if spec.reasoning {
		spec.kind = neoPiKindReasoning
	}
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
	return defaults
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
	spec.kind = ""
	if spec.reasoning {
		spec.kind = neoPiKindReasoning
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
	return spec
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
	spec.kind = ""
	if spec.reasoning {
		spec.kind = neoPiKindReasoning
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
		spec.ownThinking = true
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
				spec.thinking.RequiresEffort = mandatory.Bool()
			}
		}
	}
	if spec.reasoning && controls.Exists() {
		disable := controls.Get("disable")
		switch {
		case controls.Get("mandatory").Type == gjson.True || disable.Type == gjson.Null && disable.Exists():
			spec.reasoningDisableMode = "lowest-effort"
		case disable.IsObject() && disable.Get("reasoning_effort").Str == "none" && disable.Get("reasoning_effort").Type == gjson.String:
			spec.reasoningDisableMode = "none-effort"
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
	return spec, true
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
	spec.kind = ""
	if spec.reasoning {
		spec.kind = neoPiKindReasoning
	}
	spec.input = neoPiInputCapabilities(entry.Get("input_modalities"))
	spec.contextWindow = neoPiPositive(entry.Get("context_size"), spec.contextWindow)
	spec.maxTokens = neoPiPositive(entry.Get("max_output_tokens"), spec.maxTokens)
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
	if spec.reasoning {
		spec.kind = neoPiKindReasoning
	}
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
	return spec, true
}

// neoPiMapVenice is veniceModelManagerOptions' mapModel: Venice streams no
// usage chunk for listed models.
func neoPiMapVenice(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	spec := neoPiMapWithBundledReference(entry, defaults, reference)
	spec.noStreamingUsage = true
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
		spec.kind = neoPiKindReasoning
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
	spec.kind = neoPiKindReasoning
	return spec
}
