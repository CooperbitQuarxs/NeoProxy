package registry

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicompat"
)

// neoPiBuildRow is buildModel for one discovered spec of provider served from
// baseURL (or the spec's own base URL, when its mapper routes it elsewhere).
// For an openai-completions, openai-responses or anthropic-messages spec
// NeoPi's compat engine resolves the row's compat, thinking and reasoning
// from its id, its authored thinking and its sparse compat (package
// neopicompat); a runner spec (openai-images, openai-speech) or a Decisions
// judge (openai-decisions, served by no chat route) carries no policy and is
// materialized as mapped.
func neoPiBuildRow(provider, baseURL string, spec *neoPiSpec) (*NeoPiModel, error) {
	if spec.baseURL != "" {
		baseURL = spec.baseURL
	}
	if neoPiRunnerAPI(spec.api) || spec.api == "openai-decisions" {
		return &NeoPiModel{ID: spec.id, Name: cleanModelName(spec.name), API: spec.api, Kind: spec.kind, Provider: provider,
			BaseURL: baseURL, Input: slices.Clone(spec.input), ContextWindow: spec.contextWindow, MaxTokens: spec.maxTokens, cost: spec.cost}, nil
	}
	api := neopicompat.APIOpenAICompletions
	switch spec.api {
	case neopicompat.APIAnthropicMessages, neopicompat.APIOpenAIResponses:
		api = spec.api
	}
	policy, err := neopicompat.Resolve(neopicompat.Spec{
		Provider:  provider,
		API:       api,
		ID:        spec.id,
		BaseURL:   baseURL,
		Reasoning: spec.reasoning,
		Thinking:  neoPiThinkingSpec(spec.thinking, spec.thinkingRequiresEffortSet),
		Compat:    spec.compat,
	})
	if err != nil {
		return nil, err
	}
	row := &NeoPiModel{ID: spec.id, Name: cleanModelName(spec.name), API: api, Provider: provider, BaseURL: baseURL,
		Reasoning: policy.Reasoning, ContextWindow: spec.contextWindow, MaxTokens: spec.maxTokens,
		CompatConfig: maps.Clone(spec.compat), RequestModelID: spec.requestModelID, cost: spec.cost}
	switch api {
	case neopicompat.APIAnthropicMessages:
		row.AnthropicCompat = &NeoPiAnthropicCompat{}
		err = neoPiDecodePolicy(policy.Compat, row.AnthropicCompat)
	case neopicompat.APIOpenAIResponses:
		row.Compat = &NeoPiCompat{}
		err = neoPiDecodePolicy(policy.Compat, row.Compat)
	default:
		row.CompletionsCompat = &NeoPiCompletionsCompat{}
		err = neoPiDecodePolicy(policy.Compat, row.CompletionsCompat)
	}
	if err != nil {
		return nil, err
	}
	if policy.Thinking != nil {
		row.Thinking = &NeoPiThinking{}
		if err = neoPiDecodePolicy(policy.Thinking, row.Thinking); err != nil {
			return nil, err
		}
		_, row.thinkingRequiresEffortSet = policy.Thinking["requiresEffort"]
	}
	row.Input = slices.Clone(spec.input)
	if len(row.Input) == 0 {
		row.Input = []string{"text"}
	}
	neoPiApplyCatalog(row, policy.Catalog)
	return row, nil
}

// neoPiModelKinds is MODEL_KINDS.
var neoPiModelKinds = []string{"chat", "tiny", "image", "tts", "stt", "search", "judge", "embedding", "rerank", "video"}

// neoPiApplyCatalog is the part of buildModel's applyCatalogAssignments and
// applyCatalogCorrections a NeoPiModel carries: the rule-owned kind, and the
// reviewed limit and input corrections (limits-patch, max-tokens-fallback,
// max-tokens-ceiling, context-window-floor, input-modalities). A zero limit
// is NeoPi's null.
func neoPiApplyCatalog(row *NeoPiModel, catalog map[string]any) {
	if kind, ok := catalog["kind"].(string); ok && slices.Contains(neoPiModelKinds, kind) {
		row.Kind = kind
	}
	if patch, ok := catalog["limitsPatch"].(map[string]any); ok {
		if value, ok := patch["contextWindow"].(float64); ok {
			row.ContextWindow = int(value)
		}
		if value, ok := patch["maxTokens"].(float64); ok {
			row.MaxTokens = int(value)
		}
	}
	if value, ok := catalog["maxTokensFallback"].(float64); ok && row.MaxTokens == 0 {
		row.MaxTokens = int(value)
	}
	if value, ok := catalog["maxTokensCeiling"].(float64); ok && row.MaxTokens != 0 && row.MaxTokens > int(value) {
		row.MaxTokens = int(value)
	}
	if value, ok := catalog["contextWindowFloor"].(float64); ok {
		row.ContextWindow = max(row.ContextWindow, int(value))
	}
	if modalities, ok := catalog["inputModalities"].([]any); ok {
		input := make([]string, 0, len(modalities))
		for _, modality := range modalities {
			value, _ := modality.(string)
			if value != "text" && value != "image" {
				return
			}
			input = append(input, value)
		}
		row.Input = input
	}
}

// neoPiThinkingSpec is a ladder as authored spec thinking: its JSON form, with
// an explicitly held requiresEffort (false included) kept so NeoPi does not
// fill it.
func neoPiThinkingSpec(thinking *NeoPiThinking, requiresEffortSet bool) map[string]any {
	if thinking == nil {
		return nil
	}
	raw, err := json.Marshal(thinking)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	if requiresEffortSet {
		out["requiresEffort"] = thinking.RequiresEffort
	}
	return out
}

// neoPiDecodeThinking decodes a ladder and reports whether it holds
// requiresEffort explicitly.
func neoPiDecodeThinking(raw json.RawMessage) (*NeoPiThinking, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false, nil
	}
	thinking := &NeoPiThinking{}
	if err := json.Unmarshal(raw, thinking); err != nil {
		return nil, false, err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, false, err
	}
	_, set := keys["requiresEffort"]
	return thinking, set, nil
}

// neoPiDecodePolicy fills a typed record from the engine's JSON-shaped one;
// keys NeoPi leaves undefined are nil and decode as absent.
func neoPiDecodePolicy(value map[string]any, target any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}
