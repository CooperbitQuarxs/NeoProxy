package neopicompat

import (
	"fmt"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
)

// thinkingEfforts is THINKING_EFFORTS, in order.
var thinkingEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

var (
	defaultReasoningEfforts          = []string{"minimal", "low", "medium", "high"}
	defaultReasoningEffortsWithXHigh = []string{"minimal", "low", "medium", "high", "xhigh"}
	// fireworksThinkingEffortMap is FIREWORKS_THINKING_EFFORT_MAP.
	fireworksThinkingEffortMap = map[string]any{"minimal": "none"}
)

func effortList(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		effort, ok := entry.(string)
		if !ok || !slices.Contains(thinkingEfforts, effort) {
			return nil
		}
		out = append(out, effort)
	}
	return out
}

// effortRecord keeps the effort-keyed string entries of a map, or nil.
func effortRecord(value any) map[string]any {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for key, mapped := range record {
		if text, ok := mapped.(string); ok && slices.Contains(thinkingEfforts, key) {
			out[key] = text
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func effortNumberRecord(value any) map[string]any {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for key, mapped := range record {
		if number, ok := mapped.(float64); ok && slices.Contains(thinkingEfforts, key) {
			out[key] = number
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ruleThinking is RuleThinking: the cascade's thinking axes, validated.
type ruleThinking struct {
	mode            string
	efforts         []string
	effortsSet      bool
	defaultLevel    string
	effortMap       map[string]any
	effortBudgets   map[string]any
	requiresEffort  *bool
	suppressWhenOff bool
	supportsDisplay *bool
	prefixBinding   bool
	unverified      bool
}

func readRuleThinking(a axes) ruleThinking {
	raw := a.thinking
	out := ruleThinking{}
	switch mode, _ := raw["mode"].(string); mode {
	case "effort", "budget", "google-level", "anthropic-adaptive", "anthropic-budget-effort":
		out.mode = mode
	}
	if efforts := effortList(raw["efforts"]); efforts != nil {
		out.efforts, out.effortsSet = efforts, true
	}
	if level, _ := raw["defaultLevel"].(string); slices.Contains(thinkingEfforts, level) {
		out.defaultLevel = level
	}
	out.effortMap = effortRecord(raw["effortMap"])
	out.effortBudgets = effortNumberRecord(raw["effortBudgets"])
	if value, ok := raw["requiresEffort"].(bool); ok {
		out.requiresEffort = &value
	}
	out.suppressWhenOff, _ = raw["suppressWhenOff"].(bool)
	if value, ok := raw["supportsDisplay"].(bool); ok {
		out.supportsDisplay = &value
	}
	out.prefixBinding, _ = raw["prefixBinding"].(bool)
	out.unverified, _ = raw["unverified"].(bool)
	return out
}

// impliesMandatoryReasoning is the identity-derived requiresEffort default.
func impliesMandatoryReasoning(facts identityFacts, modelID string) bool {
	if facts.identity.ThinkingVariant {
		return true
	}
	_, ok := neopitaxonomy.StripThinkingVariantSuffix(modelID)
	return ok
}

// fallbackEfforts is fallbackEfforts for the ported APIs.
func fallbackEfforts(spec Spec, compat map[string]any) []string {
	if spec.API == APIAnthropicMessages {
		return defaultReasoningEffortsWithXHigh
	}
	if compat["thinkingFormat"] == "openai" && compat["supportsReasoningEffort"] == true {
		return defaultReasoningEffortsWithXHigh
	}
	return defaultReasoningEfforts
}

// defaultThinkingMode is defaultThinkingMode for the ported APIs; thinking-mode
// rules override it.
func defaultThinkingMode(spec Spec, facts identityFacts) string {
	if spec.API != APIAnthropicMessages {
		return "effort"
	}
	if facts.is("anthropic") {
		// Haiku stays on budget thinking until 5.5, its first adaptive generation.
		floor := "4.6"
		if facts.family("haiku") {
			floor = "5.5"
		}
		if facts.revGte(floor) {
			return "anthropic-adaptive"
		}
		if facts.family("opus") && facts.revGte("4.5") {
			return "anthropic-budget-effort"
		}
	}
	return "budget"
}

// defaultSupportsDisplay is defaultSupportsDisplay: an Anthropic-wire lineage
// fact, false on openai-completions.
func defaultSupportsDisplay(spec Spec, facts identityFacts) bool {
	return spec.API == APIAnthropicMessages && facts.anthropicAdaptiveGenAtLeast("4.7")
}

// ruleSupportsDisplay is `rule.supportsDisplay ?? defaultSupportsDisplay(...)`.
func ruleSupportsDisplay(spec Spec, facts identityFacts, rule ruleThinking) bool {
	if rule.supportsDisplay != nil {
		return *rule.supportsDisplay
	}
	return defaultSupportsDisplay(spec, facts)
}

// mergeEffortMap is mergeEffortMap: the Fireworks host default, the rule
// map, then the configured map, filtered to the ladder.
func mergeEffortMap(spec Spec, ruleMap map[string]any, compat map[string]any, efforts []string) map[string]any {
	var detected map[string]any
	if modelMatchesHost(spec.Provider, spec.BaseURL, "fireworks") {
		detected = fireworksThinkingEffortMap
	}
	configured, _ := compat["reasoningEffortMap"].(map[string]any)
	if len(configured) == 0 {
		configured = nil
	}
	if detected == nil && ruleMap == nil && configured == nil {
		return nil
	}
	merged := map[string]any{}
	for _, layer := range []map[string]any{detected, ruleMap, configured} {
		for key, value := range layer {
			merged[key] = value
		}
	}
	var filtered map[string]any
	for _, effort := range efforts {
		if mapped, ok := merged[effort]; ok {
			if filtered == nil {
				filtered = map[string]any{}
			}
			filtered[effort] = mapped
		}
	}
	return filtered
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// resolveThinkingPolicy is resolveThinkingPolicy for openai-completions.
func resolveThinkingPolicy(spec Spec, facts identityFacts, a axes, compat map[string]any) (map[string]any, error) {
	rule := readRuleThinking(a)
	var explicit map[string]any
	if efforts, ok := spec.Thinking["efforts"].([]any); ok && len(efforts) > 0 {
		explicit = spec.Thinking
	}
	unverified := !compatReasoning(spec, a)
	if unverified && (explicit != nil || facts.identity.Class != "unknown" || !rule.unverified) {
		return nil, nil
	}
	if explicit != nil {
		return fillExplicitThinking(spec, facts, compat, explicit, rule), nil
	}
	if compat["trustExplicitThinkingOnly"] == true {
		return nil, nil
	}
	if rule.effortsSet && len(rule.efforts) == 0 {
		return nil, nil
	}
	mode := rule.mode
	if mode == "" {
		mode = defaultThinkingMode(spec, facts)
	}
	efforts := rule.efforts
	if !rule.effortsSet {
		efforts = fallbackEfforts(spec, compat)
	}
	if len(efforts) == 0 {
		return nil, fmt.Errorf("model %s/%s resolved to an empty thinking range", spec.Provider, spec.ID)
	}
	config := map[string]any{"mode": mode, "efforts": stringsToAny(efforts)}
	if effortMap := mergeEffortMap(spec, rule.effortMap, compat, efforts); effortMap != nil {
		config["effortMap"] = effortMap
	}
	if unverified {
		config["unverified"] = true
		return config, nil
	}
	if rule.defaultLevel != "" {
		config["defaultLevel"] = rule.defaultLevel
	}
	if rule.effortBudgets != nil {
		config["effortBudgets"] = rule.effortBudgets
	}
	if ruleSupportsDisplay(spec, facts, rule) {
		config["supportsDisplay"] = true
	}
	if rule.prefixBinding {
		config["prefixBinding"] = true
	}
	requiresEffort := impliesMandatoryReasoning(facts, spec.ID) || compat["qwenTemplateReasoningEffort"] == true
	if rule.requiresEffort != nil {
		requiresEffort = *rule.requiresEffort
	}
	if requiresEffort {
		config["requiresEffort"] = true
	}
	if rule.suppressWhenOff {
		config["suppressWhenOff"] = true
	}
	return config, nil
}

// fillExplicitThinking backfills absent fields of an authored ladder.
func fillExplicitThinking(spec Spec, facts identityFacts, compat map[string]any, thinking map[string]any, rule ruleThinking) map[string]any {
	efforts := effortList(thinking["efforts"])
	var effortMap map[string]any
	if _, has := thinking["effortMap"]; !has {
		effortMap = mergeEffortMap(spec, rule.effortMap, compat, efforts)
	}
	_, hasDisplay := thinking["supportsDisplay"]
	needsDisplay := !hasDisplay && ruleSupportsDisplay(spec, facts, rule)
	_, hasRequires := thinking["requiresEffort"]
	requires := impliesMandatoryReasoning(facts, spec.ID) || compat["qwenTemplateReasoningEffort"] == true
	if rule.requiresEffort != nil {
		requires = *rule.requiresEffort
	}
	needsRequires := !hasRequires && requires
	_, hasDefault := thinking["defaultLevel"]
	needsDefault := !hasDefault && rule.defaultLevel != ""
	_, hasPrefix := thinking["prefixBinding"]
	needsPrefix := !hasPrefix && rule.prefixBinding
	_, hasBudgets := thinking["effortBudgets"]
	needsBudgets := !hasBudgets && rule.effortBudgets != nil
	if effortMap == nil && !needsDisplay && !needsRequires && !needsDefault && !needsPrefix && !needsBudgets {
		return thinking
	}
	filled := make(map[string]any, len(thinking)+4)
	for key, value := range thinking {
		filled[key] = value
	}
	if effortMap != nil {
		filled["effortMap"] = effortMap
	}
	if needsDisplay {
		filled["supportsDisplay"] = true
	}
	if needsDefault {
		filled["defaultLevel"] = rule.defaultLevel
	}
	if needsRequires {
		filled["requiresEffort"] = true
	}
	if needsPrefix {
		filled["prefixBinding"] = true
	}
	if needsBudgets {
		filled["effortBudgets"] = rule.effortBudgets
	}
	return filled
}
