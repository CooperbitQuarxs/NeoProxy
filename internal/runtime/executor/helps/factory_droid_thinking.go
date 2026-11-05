package helps

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// FactoryDroidAnthropicModel is the inner anthropic-messages model of a
// Factory route as buildParams reads it, plus the route's request dialect.
type FactoryDroidAnthropicModel struct {
	Reasoning bool
	// Mode is thinking.mode; SupportsDisplay nil means unset.
	Mode            string
	SupportsDisplay *bool
	PrefixBinding   bool
	EffortBudgets   map[string]int
	// TagOnly is usesAdaptiveThinkingTagOnly: every effort maps to adaptive.
	TagOnly bool
	// Compat.
	DisabledThinking                string
	BetweenToolsEffort              string
	SupportsBetweenToolsThinking    bool
	DisableAdaptiveThinking         bool
	RequiresThinkingEnabled         bool
	SupportsOutputEffort            bool
	SupportsThinkingBindingControls bool
	StripThinkingHistory            bool
	// AnthropicThinking is the route's request dialect (adaptive,
	// adaptive-summarized, budget-effort, budget-interleaved).
	AnthropicThinking string
}

// FactoryDroidAnthropicThinking is one Messages attempt's thinking fields.
type FactoryDroidAnthropicThinking struct {
	// Thinking is the wire thinking object, nil for none.
	Thinking json.RawMessage
	// Effort is output_config.effort, empty for none.
	Effort string
	// DropThinkingHistory replays history without thinking blocks.
	DropThinkingHistory bool
	// Interleaved is the interleavedThinking option: the beta rides only
	// while budget-interleaved thinking is on a thinking-led history.
	Interleaved bool
	// OptionEffort is the output effort option streamMessagesWire passes,
	// which a route's effortBeta follows.
	OptionEffort string
}

// FactoryDroidResolveAnthropicThinking is streamMessagesWire's option mapping
// followed by buildParams' thinking and output_config section. effort and off
// come from FactoryDroidSelectEffort; historyStripped is
// FactoryDroidThinkingHistoryStripped over the request's messages; hide asks
// for omitted thinking summaries.
func FactoryDroidResolveAnthropicThinking(model FactoryDroidAnthropicModel, effort string, off, historyStripped, hide bool) FactoryDroidAnthropicThinking {
	style := model.AnthropicThinking
	adaptiveStyle := style == "adaptive" || style == "adaptive-summarized"
	// streamMessagesWire: adaptive keeps the full ladder, budget-effort maps
	// through {low,medium,high}, budget-interleaved sends no output effort.
	outputEffort := ""
	if effort != "" && (adaptiveStyle || style == "budget-effort") {
		outputEffort = effort
		if !adaptiveStyle && (effort == "xhigh" || effort == "max") {
			outputEffort = "high"
		}
	}
	budget := 0
	if !adaptiveStyle && effort != "" {
		budget = model.EffortBudgets[effort]
	}
	display := ""
	if hide {
		display = "omitted"
	}
	orDefault := func(value string) string {
		if value == "" {
			return "summarized"
		}
		return value
	}
	object := func(fields ...jsjson.Field) json.RawMessage { return jsjson.Object(fields...) }
	kind := func(value string) jsjson.Field { return jsjson.Field{Name: "type", Value: jsjson.Marshal(value)} }
	supportsDisplay := model.SupportsDisplay != nil && *model.SupportsDisplay
	adaptiveOnly := model.Mode == "anthropic-adaptive" && !model.DisableAdaptiveThinking && !model.TagOnly
	usesBudget := model.Mode != "anthropic-adaptive" || model.DisableAdaptiveThinking

	var thinking json.RawMessage
	var wireEffort string
	if model.Reasoning {
		switch {
		case off && model.DisabledThinking != "":
			switch model.DisabledThinking {
			case "between-tools":
				thinking = object(kind("between_tools"))
				wireEffort = model.BetweenToolsEffort
			case "adaptive":
				fields := []jsjson.Field{kind("adaptive")}
				if supportsDisplay {
					fields = append(fields, jsjson.Field{Name: "display", Value: jsjson.Marshal(orDefault(display))})
				}
				thinking = object(fields...)
			case "omit":
			default:
				thinking = object(kind("disabled"))
			}
		case !off || model.RequiresThinkingEnabled:
			resolved := outputEffort
			if resolved != "" && model.TagOnly {
				resolved = "adaptive"
			}
			if !usesBudget {
				fields := []jsjson.Field{kind("adaptive")}
				if supportsDisplay {
					fields = append(fields, jsjson.Field{Name: "display", Value: jsjson.Marshal(orDefault(display))})
				}
				thinking = object(fields...)
				if resolved != "" && resolved != "adaptive" {
					wireEffort = resolved
				}
			} else {
				tokens := budget
				if tokens <= 0 {
					tokens = 1024
				}
				fields := []jsjson.Field{kind("enabled"), {Name: "budget_tokens", Value: jsjson.Marshal(tokens)}}
				if model.SupportsDisplay == nil || *model.SupportsDisplay {
					fields = append(fields, jsjson.Field{Name: "display", Value: jsjson.Marshal(orDefault(display))})
				}
				thinking = object(fields...)
				if model.Mode == "anthropic-budget-effort" && resolved != "" && resolved != "adaptive" {
					wireEffort = resolved
				}
			}
		default:
			switch {
			case model.SupportsBetweenToolsThinking:
				thinking = object(kind("between_tools"))
				wireEffort = model.BetweenToolsEffort
			case adaptiveOnly:
				wireEffort = "low"
			default:
				thinking = object(kind("disabled"))
			}
		}
	}
	// Prefix binding (anthropicPrefixMismatchBehavior defaults to drop_block).
	if model.PrefixBinding && model.SupportsThinkingBindingControls {
		if thinking == nil && model.Mode == "anthropic-adaptive" && !(off && model.DisabledThinking == "omit") {
			thinking = object(kind("adaptive"))
		}
		if t := gjson.GetBytes(thinking, "type").String(); t == "adaptive" || t == "enabled" {
			thinking = jsjson.Object(append(objectFields(thinking), jsjson.Field{
				Name:  "block_binding",
				Value: jsjson.Object(jsjson.Field{Name: "prefix_mismatch_behavior", Value: jsjson.Marshal("drop_block")}),
			})...)
		}
	}
	thinkingType := gjson.GetBytes(thinking, "type").String()
	strip := model.StripThinkingHistory && (thinkingType == "disabled" || thinkingType == "enabled" && historyStripped)
	if strip && thinkingType == "enabled" {
		thinking = nil
	}
	// between_tools rejects xhigh/max; the default adaptive request takes them.
	if thinkingType == "between_tools" && (wireEffort == "xhigh" || wireEffort == "max") {
		thinking = nil
	}
	if !model.SupportsOutputEffort {
		wireEffort = ""
	}
	return FactoryDroidAnthropicThinking{
		Thinking:            thinking,
		Effort:              wireEffort,
		DropThinkingHistory: strip,
		Interleaved:         style == "budget-interleaved" && effort != "" && !historyStripped,
		OptionEffort:        outputEffort,
	}
}

// objectFields splits a flat JSON object into ordered fields.
func objectFields(raw json.RawMessage) []jsjson.Field {
	var fields []jsjson.Field
	gjson.ParseBytes(raw).ForEach(func(key, value gjson.Result) bool {
		fields = append(fields, jsjson.Field{Name: key.String(), Value: json.RawMessage(value.Raw)})
		return true
	})
	return fields
}

// FactoryDroidThinkingHistoryStripped is shouldStripThinkingHistory over a
// Messages request's turns: no assistant turn leads with thinking, or the turn
// after the last user turn is a non-thinking assistant turn. NeoPi reads its
// own messages, where tool results are not user turns, so a user turn made
// only of tool_result blocks does not count as one here.
func FactoryDroidThinkingHistoryStripped(messages []byte) bool {
	turns := gjson.ParseBytes(messages).Array()
	hasAssistant, thinkingLed := false, false
	lastUser := -1
	for i, message := range turns {
		switch message.Get("role").String() {
		case "assistant":
			content := message.Get("content")
			if !content.IsArray() || len(content.Array()) == 0 {
				continue
			}
			hasAssistant = true
			if lead := factoryDroidThinkingLead(content.Array()); lead == "thinking" || lead == "redacted_thinking" {
				thinkingLed = true
			}
		case "user":
			if factoryDroidUserTurn(message.Get("content")) {
				lastUser = i
			}
		}
	}
	if hasAssistant && !thinkingLed {
		return true
	}
	if lastUser < 0 || lastUser == len(turns)-1 {
		return false
	}
	next := turns[lastUser+1]
	if next.Get("role").String() != "assistant" {
		return false
	}
	content := next.Get("content")
	if !content.IsArray() || len(content.Array()) == 0 {
		return false
	}
	lead := factoryDroidThinkingLead(content.Array())
	return lead != "" && lead != "thinking" && lead != "redacted_thinking"
}

// factoryDroidUserTurn reports whether a Messages user turn is a NeoPi user
// message rather than only tool results.
func factoryDroidUserTurn(content gjson.Result) bool {
	if !content.IsArray() {
		return true
	}
	blocks := content.Array()
	if len(blocks) == 0 {
		return true
	}
	for _, block := range blocks {
		if block.Get("type").String() != "tool_result" {
			return true
		}
	}
	return false
}

func factoryDroidThinkingLead(blocks []gjson.Result) string {
	for _, block := range blocks {
		if !block.IsObject() {
			return ""
		}
		if kind := block.Get("type").String(); kind != "fallback" {
			return kind
		}
	}
	return ""
}
