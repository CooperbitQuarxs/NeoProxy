package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// umansNeoPiJSON is NeoPi's bundled umans catalog, exported by
// runtime/executor/testdata/capture_umans_npi_profiles.ts --registry. NeoPi
// serves a key from it until that key's /v1/models/info answers.
//
//go:embed models/umans_neopi.json
var umansNeoPiJSON []byte

var umansBundledCatalog = func() []NeoPiModel {
	var export struct {
		Models []NeoPiModel `json:"models"`
	}
	if err := json.Unmarshal(umansNeoPiJSON, &export); err != nil {
		panic(fmt.Sprintf("registry: umans_neopi.json: %v", err))
	}
	return export.Models
}()

// umansDefaultEfforts is UMANS_DEFAULT_REASONING_EFFORTS: the ladder of a
// reasoning model whose models/info lists no known level.
var umansDefaultEfforts = []string{"minimal", "low", "medium", "high", "xhigh"}

// umansAnthropicCompat is the compat NeoPi's buildModel resolves for every
// umans row (capture_umans_npi_profiles.ts); only replayUnsignedThinking
// follows the row, set from its reasoning flag.
var umansAnthropicCompat = NeoPiAnthropicCompat{
	EscapeBuiltinToolNames:      true,
	InjectClaudeCodeInstruction: true,
	SupportsContextManagement:   true,
	SupportsOutputEffort:        true,
	SupportsSamplingParams:      true,
	SupportsForcedToolChoice:    true,
}

// umansMandatoryThinking is the umans rule's thinking-requires-effort models
// (compat/rules/providers/umans.kdl).
var umansMandatoryThinking = []string{"umans-coder", "umans-kimi-k2.7"}

// UmansNeoPiModel is mapUmansModelInfo followed by buildModel: one
// /v1/models/info entry over NeoPi's bundled row of the same id, if any.
func UmansNeoPiModel(id string, raw gjson.Result, baseURL string) NeoPiModel {
	var model NeoPiModel
	if reference := UmansBundledNeoPiModel(id); reference != nil {
		model = *reference
	}
	capabilities := raw.Get("capabilities")
	if !capabilities.IsObject() {
		capabilities = gjson.Result{}
	}
	model.ID = id
	model.Name = umansModelName(raw.Get("display_name"), umansModelName(raw.Get("name"), id))
	model.API = "anthropic-messages"
	model.Provider = "umans"
	model.BaseURL = baseURL
	model.Thinking = umansThinking(capabilities.Get("reasoning"))
	model.Reasoning = model.Thinking != nil
	if model.Thinking != nil && !model.Thinking.RequiresEffort {
		// buildModel fills an absent requiresEffort (fillExplicitThinking):
		// the umans rule's mandatory models, else a thinking-variant id
		// (impliesMandatoryReasoning).
		model.Thinking.RequiresEffort = slices.Contains(umansMandatoryThinking, id) || neoPiThinkingVariant(id)
	}
	model.Input = []string{"text"}
	if capabilities.Get("supports_vision").Type == gjson.True {
		model.Input = []string{"text", "image"}
	}
	model.ContextWindow = umansPositive(capabilities.Get("context_window"), model.ContextWindow)
	model.MaxTokens = umansPositive(capabilities.Get("recommended_max_tokens"), umansPositive(capabilities.Get("max_completion_tokens"), model.MaxTokens))
	compat := umansAnthropicCompat
	compat.ReplayUnsignedThinking = model.Reasoning
	model.AnthropicCompat = &compat
	return model
}

// neoPiThinkingPairTokens is rules.json taxonomy.collapse.pairTokens.
var neoPiThinkingPairTokens = []string{"thinking", "reasoning", "reasoner"}

// neoPiThinkingVariant is stripThinkingVariantSuffix(id) !== undefined: a
// "-<pair token>" not followed by a letter or digit and not negated by a
// preceding "non"/"no" word names a reasoning-locked sibling.
func neoPiThinkingVariant(id string) bool {
	lower := strings.ToLower(id)
	tokenChar := func(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' }
	for _, token := range neoPiThinkingPairTokens {
		needle := "-" + token
		for from := 0; from < len(lower); {
			index := strings.Index(lower[from:], needle)
			if index < 0 {
				break
			}
			index += from
			end := index + len(needle)
			start := index
			for start > 0 && tokenChar(lower[start-1]) {
				start--
			}
			preceding := lower[start:index]
			if (end >= len(lower) || !tokenChar(lower[end])) && preceding != "non" && preceding != "no" {
				return len(id) > len(needle)
			}
			from = index + 1
		}
	}
	return false
}

// umansModelName is toModelName: a non-blank string, trimmed, else fallback.
func umansModelName(value gjson.Result, fallback string) string {
	if value.Type != gjson.String {
		return fallback
	}
	if trimmed := strings.TrimSpace(value.String()); trimmed != "" {
		return trimmed
	}
	return fallback
}

// umansPositive is toPositiveNumber: a finite positive number (or numeric
// string), else fallback.
func umansPositive(value gjson.Result, fallback int) int {
	var parsed float64
	switch value.Type {
	case gjson.Number:
		parsed = value.Float()
	case gjson.String:
		text := strings.TrimSpace(value.String())
		if text == "" {
			return fallback
		}
		number, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return fallback
		}
		parsed = number
	default:
		return fallback
	}
	if parsed > 0 {
		return int(parsed)
	}
	return fallback
}

// umansThinking is mapUmansThinkingConfig: nil unless reasoning is
// supported; levels map onto NeoPi's efforts in listed order, and a "max"
// level selects Anthropic's budget-plus-effort mode.
func umansThinking(reasoning gjson.Result) *NeoPiThinking {
	supported := reasoning.Type == gjson.True
	if reasoning.IsObject() {
		supported = reasoning.Get("supported").Type == gjson.True
	}
	if !supported {
		return nil
	}
	levels := reasoning.Get("levels")
	var efforts []string
	hasMax := false
	if reasoning.IsObject() && levels.IsArray() {
		for _, level := range levels.Array() {
			if level.Type != gjson.String {
				continue
			}
			hasMax = hasMax || level.String() == "max"
			if slices.Contains(neoPiThinkingEfforts, level.String()) && !slices.Contains(efforts, level.String()) {
				efforts = append(efforts, level.String())
			}
		}
	}
	if len(efforts) == 0 {
		efforts = slices.Clone(umansDefaultEfforts)
	}
	thinking := &NeoPiThinking{Efforts: efforts, Mode: "budget"}
	if hasMax {
		thinking.Mode = "anthropic-budget-effort"
	}
	if reasoning.IsObject() {
		// Only an explicit can_disable:false sets it; buildModel backfills
		// the absent flag.
		thinking.RequiresEffort = reasoning.Get("can_disable").Type == gjson.False
		if level := reasoning.Get("default_level"); level.Type == gjson.String && slices.Contains(efforts, level.String()) {
			thinking.DefaultLevel = level.String()
		}
	}
	return thinking
}

// UmansModels maps a /v1/models/info payload onto registry models, sorted by
// id as NeoPi sorts them.
func UmansModels(payload []byte, baseURL string) []*ModelInfo {
	root := gjson.ParseBytes(payload)
	if !root.IsObject() {
		return nil
	}
	out := []*ModelInfo{}
	root.ForEach(func(key, value gjson.Result) bool {
		if id := key.String(); id != "" && value.IsObject() {
			model := UmansNeoPiModel(id, value, baseURL)
			out = append(out, umansModelInfo(&model))
		}
		return true
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// UmansBundledModels is NeoPi's bundled umans catalog as registry models.
func UmansBundledModels() []*ModelInfo {
	out := make([]*ModelInfo, 0, len(umansBundledCatalog))
	for i := range umansBundledCatalog {
		out = append(out, umansModelInfo(&umansBundledCatalog[i]))
	}
	return out
}

// UmansBundledNeoPiModel resolves an id against NeoPi's bundled umans
// catalog; nil when the bundle does not carry it.
func UmansBundledNeoPiModel(id string) *NeoPiModel {
	for i := range umansBundledCatalog {
		if umansBundledCatalog[i].ID == id {
			return umansBundledCatalog[i].Clone()
		}
	}
	return nil
}

func umansModelInfo(model *NeoPiModel) *ModelInfo {
	info := &ModelInfo{ID: model.ID, Object: "model", OwnedBy: "umans", Type: "umans", Name: model.ID, DisplayName: model.Name,
		ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model.Clone()}
	if model.Thinking != nil {
		// Thinking off passes through: the shaper sends a requires-effort
		// model's lowest effort, as NeoPi does.
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: true}
	}
	return info
}
