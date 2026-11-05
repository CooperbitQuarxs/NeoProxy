package registry

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicompat"
)

// neoPiBuildRow is buildModel for one discovered openai-completions spec of
// provider served from baseURL: NeoPi's compat engine resolves the row's
// compat, thinking and reasoning from its id, its authored thinking and its
// sparse compat (package neopicompat).
func neoPiBuildRow(provider, baseURL string, spec *neoPiSpec) (*NeoPiModel, error) {
	policy, err := neopicompat.Resolve(neopicompat.Spec{
		Provider:  provider,
		API:       "openai-completions",
		ID:        spec.id,
		BaseURL:   baseURL,
		Reasoning: spec.reasoning,
		Thinking:  neoPiThinkingSpec(spec.thinking, spec.thinkingRequiresEffortSet),
		Compat:    spec.compat,
	})
	if err != nil {
		return nil, err
	}
	compat := &NeoPiCompletionsCompat{}
	if err = neoPiDecodePolicy(policy.Compat, compat); err != nil {
		return nil, err
	}
	var thinking *NeoPiThinking
	requiresEffortSet := false
	if policy.Thinking != nil {
		thinking = &NeoPiThinking{}
		if err = neoPiDecodePolicy(policy.Thinking, thinking); err != nil {
			return nil, err
		}
		_, requiresEffortSet = policy.Thinking["requiresEffort"]
	}
	input := slices.Clone(spec.input)
	if len(input) == 0 {
		input = []string{"text"}
	}
	return &NeoPiModel{ID: spec.id, Name: cleanModelName(spec.name), API: "openai-completions", Provider: provider, BaseURL: baseURL,
		Reasoning: policy.Reasoning, Input: input, ContextWindow: spec.contextWindow, MaxTokens: spec.maxTokens,
		Thinking: thinking, CompletionsCompat: compat, CompatConfig: maps.Clone(spec.compat), RequestModelID: spec.requestModelID,
		thinkingRequiresEffortSet: requiresEffortSet}, nil
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
