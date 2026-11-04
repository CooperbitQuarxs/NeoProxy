package executor

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/devinwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// NeoPi's Devin provider replays a conversation as NeoPi messages: the request
// arrives here as Anthropic Messages (the dialect every downstream format
// translates to), becomes NeoPi's history with every assistant turn treated as
// the model's own, runs through transformMessages' same-model path, and is
// shaped by buildDevinChatRequest.

// devinToolCallIDMaxLength is DEVIN_TOOL_CALL_ID_MAX_LENGTH.
const devinToolCallIDMaxLength = 40

type devinPart struct {
	kind                string // text, image, thinking, toolCall
	text                string
	data, mime          string
	thinking, signature string
	id, name, arguments string
}

type devinMessage struct {
	role       string // user, assistant, toolResult
	parts      []devinPart
	toolCallID string
	toolName   string
	isError    bool
}

// devinSystemPrompt is normalizeSystemPrompts(...).join("\n\n").
func devinSystemPrompt(body gjson.Result) string {
	var prompts []string
	system := body.Get("system")
	switch {
	case system.Type == gjson.String:
		prompts = append(prompts, system.Str)
	case system.IsArray():
		for _, block := range system.Array() {
			if block.Get("type").String() == "text" {
				prompts = append(prompts, block.Get("text").String())
			}
		}
	}
	kept := prompts[:0]
	for _, prompt := range prompts {
		if strings.TrimSpace(prompt) != "" {
			kept = append(kept, prompt)
		}
	}
	return strings.Join(kept, "\n\n")
}

func devinContentParts(content gjson.Result) []devinPart {
	if content.Type == gjson.String {
		return []devinPart{{kind: "text", text: content.Str}}
	}
	var parts []devinPart
	for _, block := range content.Array() {
		switch block.Get("type").String() {
		case "text":
			parts = append(parts, devinPart{kind: "text", text: block.Get("text").String()})
		case "image":
			if source := block.Get("source"); source.Get("type").String() == "base64" {
				parts = append(parts, devinPart{kind: "image", data: source.Get("data").String(), mime: source.Get("media_type").String()})
			}
		}
	}
	return parts
}

// devinMessages maps Anthropic Messages history onto NeoPi messages: user
// text/images, tool_result blocks as toolResult messages, assistant thinking,
// text and tool_use.
func devinMessages(body gjson.Result) []devinMessage {
	var out []devinMessage
	toolNames := map[string]string{}
	for _, message := range body.Get("messages").Array() {
		content := message.Get("content")
		switch message.Get("role").String() {
		case "assistant":
			msg := devinMessage{role: "assistant"}
			if content.Type == gjson.String {
				msg.parts = append(msg.parts, devinPart{kind: "text", text: content.Str})
			}
			for _, block := range content.Array() {
				switch block.Get("type").String() {
				case "text":
					msg.parts = append(msg.parts, devinPart{kind: "text", text: block.Get("text").String()})
				case "thinking":
					msg.parts = append(msg.parts, devinPart{kind: "thinking", thinking: block.Get("thinking").String(), signature: block.Get("signature").String()})
				case "tool_use":
					input := block.Get("input")
					arguments := "{}"
					if input.Exists() {
						arguments = string(jsjson.Normalize([]byte(input.Raw)))
					}
					id, name := block.Get("id").String(), block.Get("name").String()
					toolNames[id] = name
					msg.parts = append(msg.parts, devinPart{kind: "toolCall", id: id, name: name, arguments: arguments})
				}
			}
			out = append(out, msg)
		default:
			if content.Type == gjson.String {
				out = append(out, devinMessage{role: "user", parts: []devinPart{{kind: "text", text: content.Str}}})
				continue
			}
			var pending []devinPart
			flush := func() {
				if len(pending) > 0 {
					out = append(out, devinMessage{role: "user", parts: pending})
					pending = nil
				}
			}
			for _, block := range content.Array() {
				if block.Get("type").String() != "tool_result" {
					pending = append(pending, devinContentParts(gjson.Parse("["+block.Raw+"]"))...)
					continue
				}
				flush()
				id := block.Get("tool_use_id").String()
				out = append(out, devinMessage{role: "toolResult", toolCallID: id, toolName: toolNames[id], isError: block.Get("is_error").Bool(),
					parts: devinContentParts(block.Get("content"))})
			}
			flush()
			if len(content.Array()) == 0 {
				out = append(out, devinMessage{role: "user"})
			}
		}
	}
	return out
}

var devinToolCallName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func devinMalformedToolCall(part devinPart) bool {
	return strings.TrimSpace(part.id) == "" || len([]rune(part.name)) > 128 || !devinToolCallName.MatchString(part.name)
}

// devinSanitizeMalformedToolCalls is sanitizeMalformedToolCalls.
func devinSanitizeMalformedToolCalls(messages []devinMessage) []devinMessage {
	malformed := false
	for _, msg := range messages {
		for _, part := range msg.parts {
			if msg.role == "assistant" && part.kind == "toolCall" && devinMalformedToolCall(part) {
				malformed = true
			}
		}
	}
	if !malformed {
		return messages
	}
	drops := map[string][]bool{}
	var out []devinMessage
	for _, msg := range messages {
		switch msg.role {
		case "assistant":
			drops = map[string][]bool{}
			var kept []devinPart
			for _, part := range msg.parts {
				if part.kind == "toolCall" {
					bad := devinMalformedToolCall(part)
					drops[part.id] = append(drops[part.id], bad)
					if bad {
						continue
					}
				}
				kept = append(kept, part)
			}
			if len(kept) == 0 {
				continue
			}
			msg.parts = kept
			out = append(out, msg)
		case "toolResult":
			if queue := drops[msg.toolCallID]; len(queue) > 0 {
				drop := queue[0]
				drops[msg.toolCallID] = queue[1:]
				if drop {
					continue
				}
			}
			out = append(out, msg)
		default:
			drops = map[string][]bool{}
			out = append(out, msg)
		}
	}
	return out
}

// devinDuplicateSuffix is appendSegmentDuplicateSuffix (Devin ids carry no
// Responses composites).
func devinDuplicateSuffix(id, suffix string, maxLength int) string {
	units := []rune(id)
	if len(units)+len(suffix) <= maxLength {
		return id + suffix
	}
	budget := max(0, maxLength-len(suffix))
	if budget > len(units) {
		budget = len(units)
	}
	return string(units[:budget]) + suffix
}

// devinTransform is transformMessages for a Devin target whose history is
// all its own: thinking survives unless empty and unsigned, duplicate call ids
// get `_dup` suffixes, every call gets exactly one following result
// ("No result provided" when none arrived), and orphan results become a
// stale-tool-result note or are dropped.
func devinTransform(messages []devinMessage) []devinMessage {
	messages = devinSanitizeMalformedToolCalls(messages)
	for i, msg := range messages {
		if msg.role != "assistant" {
			continue
		}
		var kept []devinPart
		for _, part := range msg.parts {
			if part.kind == "thinking" && part.signature == "" && strings.TrimSpace(part.thinking) == "" {
				continue
			}
			kept = append(kept, part)
		}
		messages[i].parts = kept
	}

	// deduplicateToolCallIds
	type rewrite struct{ id string }
	seen := map[string]int{}
	pending := map[string][]*rewrite{}
	type indexed struct {
		index    int
		msg      devinMessage
		consumed bool
	}
	results := map[string][]*indexed{}
	valid := map[string]bool{}
	deduped := make([]devinMessage, len(messages))
	for index, msg := range messages {
		switch msg.role {
		case "toolResult":
			queue := pending[msg.toolCallID]
			if len(queue) > 0 {
				next := queue[0]
				pending[msg.toolCallID] = queue[1:]
				if len(pending[msg.toolCallID]) == 0 {
					delete(pending, msg.toolCallID)
				}
				if next != nil {
					msg.toolCallID = next.id
				}
			}
			results[msg.toolCallID] = append(results[msg.toolCallID], &indexed{index: index, msg: msg})
		case "assistant":
			touched := map[string]bool{}
			parts := make([]devinPart, len(msg.parts))
			copy(parts, msg.parts)
			for j, part := range parts {
				if part.kind != "toolCall" {
					continue
				}
				if !touched[part.id] {
					touched[part.id] = true
					delete(pending, part.id)
				}
				count := seen[part.id]
				if count == 0 {
					seen[part.id] = 1
					pending[part.id] = append(pending[part.id], nil)
					continue
				}
				dup := count
				replacement := devinDuplicateSuffix(part.id, "_dup"+strconv.Itoa(dup), devinToolCallIDMaxLength)
				for seen[replacement] > 0 {
					dup++
					replacement = devinDuplicateSuffix(part.id, "_dup"+strconv.Itoa(dup), devinToolCallIDMaxLength)
				}
				seen[part.id] = dup + 1
				seen[replacement] = 1
				pending[part.id] = append(pending[part.id], &rewrite{id: replacement})
				parts[j].id = replacement
			}
			for _, part := range parts {
				if part.kind == "toolCall" {
					valid[part.id] = true
				}
			}
			msg.parts = parts
		}
		deduped[index] = msg
	}
	takeResult := func(id string, after int) (devinMessage, bool) {
		for _, entry := range results[id] {
			if entry.consumed || entry.index <= after {
				continue
			}
			entry.consumed = true
			return entry.msg, true
		}
		return devinMessage{}, false
	}

	// Second pass: one result immediately after each surviving call.
	var out []devinMessage
	var calls []devinPart
	callsAt := -1
	resolved := map[string]bool{}
	flush := func() {
		for _, call := range calls {
			if resolved[call.id] {
				continue
			}
			if result, ok := takeResult(call.id, callsAt); ok {
				out = append(out, result)
			} else {
				out = append(out, devinMessage{role: "toolResult", toolCallID: call.id, toolName: call.name, isError: true,
					parts: []devinPart{{kind: "text", text: "No result provided"}}})
			}
			resolved[call.id] = true
		}
		calls = nil
	}
	for i, msg := range deduped {
		switch msg.role {
		case "assistant":
			flush()
			var pendingCalls []devinPart
			for _, part := range msg.parts {
				if part.kind == "toolCall" {
					pendingCalls = append(pendingCalls, part)
				}
			}
			if len(pendingCalls) > 0 {
				calls, callsAt = pendingCalls, i
			}
			out = append(out, msg)
		case "toolResult":
			if resolved[msg.toolCallID] {
				continue
			}
			inWindow := false
			for _, call := range calls {
				if call.id == msg.toolCallID {
					inWindow = true
				}
			}
			if inWindow {
				resolved[msg.toolCallID] = true
				out = append(out, msg)
				continue
			}
			if valid[msg.toolCallID] {
				continue
			}
			open := false
			for _, call := range calls {
				if !resolved[call.id] {
					open = true
				}
			}
			if open {
				continue
			}
			var texts []string
			for _, part := range msg.parts {
				if part.kind == "text" && strings.TrimSpace(part.text) != "" {
					texts = append(texts, part.text)
				}
			}
			if len(texts) > 0 {
				attr := ""
				if msg.isError {
					attr = ` is-error="true"`
				}
				note := "<stale-tool-result tool=\"" + msg.toolName + "\" id=\"" + msg.toolCallID + "\"" + attr + ">\n" + strings.Join(texts, "\n") + "\n</stale-tool-result>"
				out = append(out, devinMessage{role: "user", parts: []devinPart{{kind: "text", text: note}}})
			}
		default:
			flush()
			out = append(out, msg)
		}
	}
	flush()
	return out
}

func devinUserPrompt(msg devinMessage, messageID string) devinwire.Prompt {
	prompt := devinwire.Prompt{MessageID: messageID, Source: devinwire.SourceUser}
	for _, part := range msg.parts {
		switch part.kind {
		case "text":
			prompt.Prompt += part.text
		case "image":
			prompt.Images = append(prompt.Images, devinwire.Image{Base64Data: part.data, MimeType: part.mime})
		}
	}
	return prompt
}

// devinPrompts is buildChatMessagePrompts over transformed history.
func devinPrompts(messages []devinMessage, cascadeID string) []devinwire.Prompt {
	var prompts []devinwire.Prompt
	for index, msg := range messages {
		seed := cascadeID + "\x00" + strconv.Itoa(index) + "\x00"
		switch msg.role {
		case "user":
			prompts = append(prompts, devinUserPrompt(msg, devinwire.DeterministicUUID(seed+"user")))
		case "assistant":
			prompt := devinwire.Prompt{MessageID: devinwire.DeterministicUUID(seed + "assistant"), Source: devinwire.SourceSystem}
			for _, part := range msg.parts {
				switch part.kind {
				case "text":
					prompt.Prompt += part.text
				case "thinking":
					prompt.Thinking += part.thinking
					if prompt.Signature == "" && part.signature != "" {
						prompt.Signature = part.signature
					}
				case "toolCall":
					prompt.ToolCalls = append(prompt.ToolCalls, devinwire.ToolCall{ID: part.id, Name: part.name, ArgumentsJSON: part.arguments})
				}
			}
			if prompt.Prompt == "" && prompt.Thinking == "" && prompt.Signature == "" && len(prompt.ToolCalls) == 0 {
				continue
			}
			prompts = append(prompts, prompt)
		default:
			prompt := devinwire.Prompt{MessageID: devinwire.DeterministicUUID(seed + "tool\x00" + msg.toolCallID), Source: devinwire.SourceTool,
				ToolCallID: msg.toolCallID, ToolResultIsError: msg.isError}
			for _, part := range msg.parts {
				switch part.kind {
				case "text":
					prompt.Prompt += part.text
				case "image":
					prompt.Images = append(prompt.Images, devinwire.Image{Base64Data: part.data, MimeType: part.mime})
				}
			}
			prompts = append(prompts, prompt)
		}
	}
	return prompts
}

// devinRouterPrompt is buildRouterPrompt: the latest user turn, without an id.
func devinRouterPrompt(messages []devinMessage) *devinwire.Prompt {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].role == "user" {
			prompt := devinUserPrompt(messages[i], "")
			return &prompt
		}
	}
	return nil
}

// devinIsGemini mirrors the Gemini-backend checks in buildDevinChatRequest:
// classifyModel's gemini class (lenient: a `gemini` id) or the server's
// MODEL_GOOGLE_GEMINI_ enum namespace.
func devinIsGemini(modelID, chatModelUID string) bool {
	return strings.HasPrefix(strings.ToLower(modelID), "gemini") || strings.HasPrefix(strings.ToLower(chatModelUID), "gemini") ||
		strings.HasPrefix(chatModelUID, "MODEL_GOOGLE_GEMINI_")
}

func devinTools(body gjson.Result, google bool) []devinwire.Tool {
	var tools []devinwire.Tool
	for _, tool := range body.Get("tools").Array() {
		name := tool.Get("name").String()
		if name == "" {
			continue
		}
		schema := codexwire.ToolWireSchema(tool.Get("input_schema"))
		if google {
			schema = codexwire.NormalizeSchemaForGoogle(schema)
		}
		tools = append(tools, devinwire.Tool{Name: name, Description: tool.Get("description").String(), JSONSchema: string(schema),
			Strict: tool.Get("strict").Bool()})
	}
	return tools
}

// devinWireModelID is resolveWireModelId.
func devinWireModelID(model *registry.NeoPiModel, effort string) string {
	key := effort
	if key == "" {
		key = "off"
	}
	if model.Thinking != nil {
		if id := model.Thinking.EffortRouting[key]; id != "" {
			return id
		}
	}
	if model.RequestModelID != "" {
		return model.RequestModelID
	}
	return model.ID
}

// devinDefaultEffort is defaultSupportedEffort.
func devinDefaultEffort(model *registry.NeoPiModel) string {
	thinking := model.Thinking
	if thinking == nil || len(thinking.Efforts) == 0 {
		return ""
	}
	if thinking.EffortRouting != nil && model.RequestModelID != "" {
		for _, effort := range neoPiEffortOrder {
			if containsString(thinking.Efforts, effort) && thinking.EffortRouting[effort] == model.RequestModelID {
				return effort
			}
		}
	}
	for _, effort := range neoPiEffortOrder {
		if containsString(thinking.Efforts, effort) {
			return effort
		}
	}
	return thinking.Efforts[0]
}

var neoPiEffortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// devinTopP is Math.fround: Devin's CLI widens a float32 topP to double.
func devinTopP(value float64) float64 {
	return float64(float32(value))
}

// devinChatSettings are the request options NeoPi derives from a turn. Stop
// sequences are absent: mapOptionsForApi drops them for devin-agent, so NeoPi
// always sends no stop patterns.
type devinChatSettings struct {
	maxTokens   uint64
	temperature float64
	topP        float64
}

func devinSettings(body gjson.Result, model *registry.NeoPiModel) devinChatSettings {
	settings := devinChatSettings{temperature: 1, topP: devinTopP(0.95)}
	switch {
	case body.Get("max_tokens").Exists():
		settings.maxTokens = uint64(math.Max(0, body.Get("max_tokens").Float()))
	case model.MaxTokens > 0:
		settings.maxTokens = uint64(model.MaxTokens)
	default:
		settings.maxTokens = 64000
	}
	if temperature := body.Get("temperature"); temperature.Exists() {
		settings.temperature = temperature.Float()
	}
	if topP := body.Get("top_p"); topP.Exists() {
		settings.topP = devinTopP(topP.Float())
	}
	return settings
}

// devinChatRequest is buildDevinChatRequest.
func devinChatRequest(model *registry.NeoPiModel, body gjson.Result, messages []devinMessage, apiKey, userJWT, cascadeID, executionID, chatModelUID string, assignment *devinwire.Assignment) devinwire.ChatRequest {
	settings := devinSettings(body, model)
	parallel := model.DevinCompat != nil && model.DevinCompat.SupportsParallelToolCalls
	request := devinwire.ChatRequest{
		Metadata:                 devinwire.CLIMetadata(apiKey, userJWT),
		Prompt:                   devinSystemPrompt(body),
		Prompts:                  devinPrompts(messages, cascadeID),
		ChatModelUID:             chatModelUID,
		Configuration:            devinwire.Configuration{MaxTokens: settings.maxTokens, Temperature: settings.temperature, TopP: settings.topP},
		Tools:                    devinTools(body, devinIsGemini(model.ID, chatModelUID)),
		DisableParallelToolCalls: !parallel,
		CascadeID:                cascadeID,
		ExecutionID:              executionID,
	}
	if assignment != nil {
		jwt := assignment.JWT
		request.ModelAssignmentJWT = &jwt
	}
	return request
}

// devinEffort reads the effort NeoProxy's thinking pipeline settled on.
func devinEffort(body gjson.Result) string {
	if body.Get("thinking.type").String() == "disabled" {
		return ""
	}
	return body.Get("output_config.effort").String()
}
