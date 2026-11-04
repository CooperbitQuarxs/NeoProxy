package codexwire

import (
	"encoding/json"
	"strings"
	"unicode/utf16"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// Options are NeoPi's OpenAICodexResponsesOptions for one downstream request.
type Options struct {
	// SessionID is NeoPi's options.sessionId: transport, metadata and cache identity.
	SessionID string
	// PromptCacheKey overrides the session-derived prompt cache key.
	PromptCacheKey string
	// Effort is the requested effort; empty leaves reasoning unset.
	Effort       string
	ReasoningOff bool
	// Summary is reasoning.summary; SummaryNull is an explicit null (hide thinking).
	Summary          string
	SummaryNull      bool
	ReasoningContext string
	TextVerbosity    string
	Include          []string
	ServiceTier      string
	ToolChoice       json.RawMessage
	// ClientMetadata extras ride inside the turn metadata JSON in insertion order.
	ClientMetadata []jsjson.Field
	ParentTurnID   string
	ResponsesLite  bool
	// Compaction makes the request a V2 compaction (request_kind "compaction").
	Compaction *Compaction
}

// Context is the NeoPi conversation a downstream Responses request carries.
type Context struct {
	SystemPrompts []string
	Input         []json.RawMessage
	Tools         []json.RawMessage
}

const (
	orphanOutputLimit      = 16000
	interruptedToolOutput  = "[No tool output recorded: the tool call was interrupted before it produced a result.]"
	encryptedReasoningItem = "reasoning.encrypted_content"
)

// thinkingEfforts is NeoPi's THINKING_EFFORTS ladder order.
var thinkingEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

func effortIndex(effort string) int {
	for i, value := range thinkingEfforts {
		if value == effort {
			return i
		}
	}
	return -1
}

// clampEffort is NeoPi's clampThinkingLevelForModel, applied by the agent
// before the provider's requireSupportedEffort.
func clampEffort(model *registry.CodexModel, requested string) string {
	if model == nil || !model.Reasoning || model.Thinking == nil {
		return ""
	}
	return ClampEffort(model.Thinking.Efforts, requested)
}

// ClampEffort is clampThinkingLevelForModel over a model's effort ladder: the
// requested level when offered, else the highest offered level below it, else
// the lowest offered level. An empty ladder or unknown level yields "".
func ClampEffort(levels []string, requested string) string {
	if len(levels) == 0 {
		return ""
	}
	for _, level := range levels {
		if level == requested {
			return requested
		}
	}
	requestedIndex := effortIndex(requested)
	if requestedIndex < 0 {
		return ""
	}
	clamped := ""
	for _, level := range levels {
		if effortIndex(level) > requestedIndex {
			break
		}
		clamped = level
	}
	if clamped == "" {
		return levels[0]
	}
	return clamped
}

// jsLength is a JavaScript string's UTF-16 length.
func jsLength(value string) int { return len(utf16.Encode([]rune(value))) }

// jsSlice is String.prototype.slice(0, n) over UTF-16 code units.
func jsSlice(value string, n int) string {
	units := utf16.Encode([]rune(value))
	if n >= len(units) {
		return value
	}
	return string(utf16.Decode(units[:n]))
}

// SanitizeCallID is NeoPi's sanitizeCodexCallId.
func SanitizeCallID(raw string) string {
	if raw == "" {
		return "call_" + bunHash36("empty")
	}
	base := raw
	if sep := strings.IndexAny(raw, "\n|"); sep > 0 {
		base = raw[:sep]
	} else if sep == 0 {
		base = raw[1:]
	}
	var sanitized strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			sanitized.WriteRune(r)
		case r > 0xFFFF:
			// JavaScript's /[^…]/g replaces each UTF-16 code unit.
			sanitized.WriteString("__")
		default:
			sanitized.WriteByte('_')
		}
	}
	clean := strings.TrimRight(sanitized.String(), "_")
	if clean != "" && len(clean) <= 64 && clean == base {
		return clean
	}
	hashed := base
	if hashed == "" {
		hashed = raw
	}
	hash := bunHash36(hashed)
	effective := clean
	if effective == "" {
		effective = "call"
	}
	prefix := 63 - len(hash)
	if prefix < 0 {
		prefix = 0
	}
	if prefix < len(effective) {
		effective = effective[:prefix]
	}
	out := effective + "_" + hash
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func toolCallKind(itemType string) string {
	switch itemType {
	case "function_call":
		return "function"
	case "custom_tool_call":
		return "custom"
	case "computer_call":
		return "computer"
	}
	return ""
}

func toolOutputKind(itemType string) string {
	switch itemType {
	case "function_call_output":
		return "function"
	case "custom_tool_call_output":
		return "custom"
	case "computer_call_output":
		return "computer"
	}
	return ""
}

func stringValue(result gjson.Result) (string, bool) {
	if result.Type != gjson.String {
		return "", false
	}
	return result.String(), true
}

// filterInput drops item references and strips non-computer item ids.
func filterInput(items []object) []object {
	out := items[:0:0]
	for _, item := range items {
		itemType := item.get("type").String()
		if itemType == "item_reference" {
			continue
		}
		if itemType != "computer_call" && item.has("id") && item.get("id").Type != gjson.Null {
			item.del("id")
		}
		out = append(out, item)
	}
	return out
}

func sanitizeInputCallIDs(items []object) {
	for i := range items {
		if callID, ok := stringValue(items[i].get("call_id")); ok {
			items[i].set("call_id", jsjson.Marshal(SanitizeCallID(callID)))
		}
	}
}

func orphanOutputMessage(item object, callID string) object {
	toolName := "tool"
	if name, ok := stringValue(item.get("name")); ok {
		toolName = name
	}
	output := item.get("output")
	text := output.String()
	if output.Type != gjson.String {
		if output.Exists() {
			text = string(jsjson.Normalize([]byte(output.Raw)))
		} else {
			// JSON.stringify(undefined) is undefined; the template prints it.
			text = "undefined"
		}
	}
	if jsLength(text) > orphanOutputLimit {
		text = jsSlice(text, orphanOutputLimit) + "\n...[truncated]"
	}
	return object{
		{Name: "type", Value: jsjson.Marshal("message")},
		{Name: "role", Value: jsjson.Marshal("assistant")},
		{Name: "content", Value: jsjson.Marshal("[Previous " + toolName + " result; call_id=" + callID + "]: " + text)},
	}
}

// repairToolCallPairs keeps the Responses input grammar valid in both directions.
func repairToolCallPairs(items []object) []object {
	callKinds := map[string]string{}
	outputKinds := map[string]string{}
	for _, item := range items {
		callID, ok := stringValue(item.get("call_id"))
		if !ok {
			continue
		}
		itemType := item.get("type").String()
		if kind := toolCallKind(itemType); kind != "" {
			callKinds[callID] = kind
		}
		if kind := toolOutputKind(itemType); kind != "" {
			outputKinds[callID] = kind
		}
	}
	repaired := make([]object, 0, len(items))
	for _, item := range items {
		callID, ok := stringValue(item.get("call_id"))
		itemType := item.get("type").String()
		callKind, outputKind := toolCallKind(itemType), toolOutputKind(itemType)
		if outputKind != "" && ok && callKinds[callID] != outputKind {
			repaired = append(repaired, orphanOutputMessage(item, callID))
			continue
		}
		if callKind != "" && ok && outputKinds[callID] != callKind {
			if callKind == "computer" {
				repaired = append(repaired, object{
					{Name: "type", Value: jsjson.Marshal("message")},
					{Name: "role", Value: jsjson.Marshal("assistant")},
					{Name: "content", Value: jsjson.Marshal("[Computer call interrupted before a screenshot was recorded; call_id=" + callID + "]")},
				})
				continue
			}
			outputType := "function_call_output"
			if callKind == "custom" {
				outputType = "custom_tool_call_output"
			}
			repaired = append(repaired, item, object{
				{Name: "type", Value: jsjson.Marshal(outputType)},
				{Name: "call_id", Value: jsjson.Marshal(callID)},
				{Name: "output", Value: jsjson.Marshal(interruptedToolOutput)},
			})
			continue
		}
		repaired = append(repaired, item)
	}
	return repaired
}

func developerMessage(text string) object {
	return object{
		{Name: "type", Value: jsjson.Marshal("message")},
		{Name: "role", Value: jsjson.Marshal("developer")},
		{Name: "content", Value: jsjson.Marshal([]json.RawMessage{jsjson.Object(
			jsjson.Field{Name: "type", Value: jsjson.Marshal("input_text")},
			jsjson.Field{Name: "text", Value: jsjson.Marshal(text)},
		)})},
	}
}

func lastDeveloperText(items []object) (string, bool) {
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		content := item.get("content")
		if item.get("role").String() != "developer" || !content.IsArray() {
			continue
		}
		parts := content.Array()
		for j := len(parts) - 1; j >= 0; j-- {
			part := parts[j]
			text := part.Get("text")
			if part.Get("type").String() == "input_text" && text.Type == gjson.String && strings.TrimSpace(text.String()) != "" {
				return text.String(), true
			}
		}
	}
	return "", false
}

func itemsJSON(items []object) json.RawMessage {
	raw := make([]json.RawMessage, len(items))
	for i := range items {
		raw[i] = items[i].bytes()
	}
	return jsjson.Marshal(raw)
}

func parseItems(raw gjson.Result) []object {
	var items []object
	for _, item := range raw.Array() {
		items = append(items, parseObject([]byte(item.Raw)))
	}
	return items
}

// applyResponsesLiteShape is NeoPi's applyCodexResponsesLiteShape.
func applyResponsesLiteShape(body *object) {
	items := parseItems(body.get("input"))
	for i := range items {
		for _, key := range []string{"content", "output"} {
			collection := items[i].get(key)
			if !collection.IsArray() {
				continue
			}
			var parts []json.RawMessage
			for _, part := range collection.Array() {
				obj := parseObject([]byte(part.Raw))
				if part.IsObject() && obj.get("type").String() == "input_image" && obj.has("detail") {
					// detail = undefined: JSON.stringify omits it.
					obj.set("detail", nil)
				}
				if part.IsObject() {
					parts = append(parts, obj.bytes())
				} else {
					parts = append(parts, jsjson.Normalize([]byte(part.Raw)))
				}
			}
			items[i].set(key, jsjson.Marshal(parts))
		}
	}
	sanitizeInputCallIDs(items)
	body.set("parallel_tool_calls", jsjson.Marshal(false))
	declared := body.get("tools")
	var tools []gjson.Result
	if declared.IsArray() {
		tools = declared.Array()
	}
	additional := tools
	if choice := body.get("tool_choice"); choice.IsObject() && choice.Get("type").Exists() {
		for _, tool := range tools {
			if !tool.IsObject() || !tool.Get("type").Exists() {
				continue
			}
			matches := false
			if choice.Get("type").String() == "computer" {
				matches = tool.Get("type").String() == "computer"
			} else {
				matches = choice.Get("type").String() == "function" && tool.Get("type").String() == "function" &&
					choice.Get("name").Type == gjson.String && tool.Get("name").Exists() && tool.Get("name").Value() == choice.Get("name").Value()
			}
			if matches {
				additional = []gjson.Result{tool}
				body.set("tool_choice", jsjson.Marshal("required"))
				break
			}
		}
	}
	additionalRaw := make([]json.RawMessage, len(additional))
	for i, tool := range additional {
		additionalRaw[i] = jsjson.Normalize([]byte(tool.Raw))
	}
	prefix := []object{{
		{Name: "type", Value: jsjson.Marshal("additional_tools")},
		{Name: "role", Value: jsjson.Marshal("developer")},
		{Name: "tools", Value: jsjson.Marshal(additionalRaw)},
	}}
	if instructions := body.get("instructions"); instructions.Type == gjson.String && instructions.String() != "" {
		prefix = append(prefix, developerMessage(instructions.String()))
	}
	body.set("input", itemsJSON(append(prefix, items...)))
	if choice := body.get("tool_choice"); choice.String() != "none" && choice.String() != "required" || choice.Type != gjson.String {
		body.set("tool_choice", jsjson.Marshal("auto"))
	}
	body.del("instructions")
	body.del("tools")
}

// transformRequestBody is NeoPi's transformRequestBody.
func transformRequestBody(body *object, model *registry.CodexModel, opts Options, developerTexts []string) {
	body.set("store", jsjson.Marshal(false))
	body.set("stream", jsjson.Marshal(true))
	items := parseItems(body.get("input"))
	if body.get("input").IsArray() {
		items = filterInput(items)
		sanitizeInputCallIDs(items)
		items = repairToolCallPairs(items)
	}
	if len(developerTexts) > 0 {
		prefix := make([]object, len(developerTexts))
		for i, text := range developerTexts {
			prefix[i] = developerMessage(text)
		}
		items = append(prefix, items...)
	}
	if len(developerTexts) > 0 || body.get("input").IsArray() {
		body.set("input", itemsJSON(items))
	}
	final, found := "", false
	for i := len(developerTexts) - 1; i >= 0; i-- {
		if strings.TrimSpace(developerTexts[i]) != "" {
			final, found = developerTexts[i], true
			break
		}
	}
	if !found {
		final, found = lastDeveloperText(items)
	}
	if instructions := body.get("instructions"); !found && instructions.Type == gjson.String && strings.TrimSpace(instructions.String()) != "" {
		final, found = instructions.String(), true
	}
	if found {
		visible := false
		for _, item := range items {
			if item.get("role").String() != "developer" {
				visible = true
				break
			}
		}
		if !visible {
			user := object{
				{Name: "type", Value: jsjson.Marshal("message")},
				{Name: "role", Value: jsjson.Marshal("user")},
				{Name: "content", Value: jsjson.Marshal([]json.RawMessage{jsjson.Object(
					jsjson.Field{Name: "type", Value: jsjson.Marshal("input_text")},
					jsjson.Field{Name: "text", Value: jsjson.Marshal(final)},
				)})},
			}
			items = append(items, user)
			body.set("input", itemsJSON(items))
		}
	}
	if opts.ResponsesLite {
		applyResponsesLiteShape(body)
	}
	compat := registry.CodexModelCompat{}
	if model.Compat != nil {
		compat = *model.Compat
	}
	if opts.ReasoningOff || opts.Effort != "" || opts.ResponsesLite {
		reasoning := parseObject([]byte(body.get("reasoning").Raw))
		switch {
		case opts.ReasoningOff:
			reasoning.set("effort", jsjson.Marshal("none"))
		case opts.Effort != "":
			// getReasoningConfig: an explicit "none" still requests a summary.
			reasoning.set("effort", jsjson.Marshal(opts.Effort))
			if !opts.SummaryNull && compat.SupportsReasoningSummary {
				summary := opts.Summary
				if summary == "" {
					summary = "auto"
				}
				reasoning.set("summary", jsjson.Marshal(summary))
			}
		}
		if opts.ResponsesLite {
			reasoning.set("context", jsjson.Marshal("all_turns"))
		} else if opts.ReasoningContext != "" {
			if opts.ReasoningContext == "all_turns" && !compat.SupportsAllTurnsReasoningContext {
				reasoning.del("context")
			} else {
				reasoning.set("context", jsjson.Marshal(opts.ReasoningContext))
			}
		}
		body.set("reasoning", reasoning.bytes())
	} else {
		body.del("reasoning")
	}
	if !compat.SupportsReasoningSummary && body.has("reasoning") {
		reasoning := parseObject([]byte(body.get("reasoning").Raw))
		reasoning.del("summary")
		body.set("reasoning", reasoning.bytes())
	}
	// Concurrent reasoning summaries are an environment opt-in that NeoPi ships off.
	body.del("stream_options")
	if opts.TextVerbosity != "" {
		text := parseObject([]byte(body.get("text").Raw))
		text.set("verbosity", jsjson.Marshal(opts.TextVerbosity))
		body.set("text", text.bytes())
	}
	include := append(append([]string(nil), opts.Include...), encryptedReasoningItem)
	seen := map[string]bool{}
	unique := include[:0]
	for _, value := range include {
		if !seen[value] {
			seen[value] = true
			unique = append(unique, value)
		}
	}
	body.set("include", jsjson.Marshal(unique))
	body.del("max_output_tokens")
	body.del("max_completion_tokens")
}
