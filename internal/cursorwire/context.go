package cursorwire

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// Context is pi-ai's Context as the auth gateway's OpenAI chat route
// (openai-chat-server.ts parseRequest) builds it.
type Context struct {
	// SystemPrompt is nil when the request carried no system text.
	SystemPrompt []string
	Messages     []Message
	Tools        []Tool
}

// Part is one TextContent or ImageContent; Data is base64.
type Part struct {
	Image          bool
	Text           string
	Data, MimeType string
}

// Block is one assistant content block: "text", "thinking" or "toolCall".
type Block struct {
	Type              string
	Text              string
	Thinking          string
	ThinkingSignature string
	ID, Name          string
	Arguments         *JSONObject
}

// Message is a user, developer, assistant or toolResult message.
type Message struct {
	Role string
	// User and developer content is either a string (Plain) or parts.
	Plain bool
	Text  string
	Parts []Part
	// Assistant fields; the gateway stamps parsed history as
	// openai-completions / openai.
	Blocks               []Block
	API, Provider, Model string
	// Tool result fields; Parts holds its text content.
	ToolCallID, ToolName string
	IsError              bool
}

// Tool is a client function tool; Parameters is its JSON schema.
type Tool struct {
	Name, Description string
	Parameters        any
}

// Request is ParsedRequest: the context plus the options the Cursor stream
// consumes.
type Request struct {
	ModelID string
	Context Context
	Stream  bool
	// Reasoning is the requested effort; ForceReasoningOff is reasoning_effort "none".
	Reasoning         string
	ForceReasoningOff bool
	// PromptCacheKey is resolvePromptCacheKey's client key, "" when none.
	PromptCacheKey     string
	IncludeStreamUsage bool
	MaxOutputTokens    *int64
}

var cacheKeyHeaders = []string{"x-prompt-cache-key", "session_id", "conversation_id", "x-session-id", "x-conversation-id"}

// ParseOpenAIChat is parseRequest over a chat-completions body; header looks
// up an inbound header for the prompt-cache key.
func ParseOpenAIChat(body []byte, header func(string) string) (*Request, error) {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return nil, fmt.Errorf("openai-chat: request body must be an object")
	}
	if root.Get("prompt_cache_options").Exists() || root.Get("prompt_cache_breakpoint").Exists() {
		return nil, fmt.Errorf("prompt_cache_options and prompt_cache_breakpoint are not supported")
	}
	out := &Request{ModelID: root.Get("model").String(), Stream: root.Get("stream").Type == gjson.True}
	var systemParts []string
	toolNamesByID := map[string]string{}
	for _, m := range root.Get("messages").Array() {
		if m.Get("prompt_cache_breakpoint").Exists() {
			return nil, fmt.Errorf("prompt_cache_options and prompt_cache_breakpoint are not supported")
		}
		for _, part := range m.Get("content").Array() {
			if part.IsObject() && part.Get("prompt_cache_breakpoint").Exists() {
				return nil, fmt.Errorf("prompt_cache_options and prompt_cache_breakpoint are not supported")
			}
		}
		content := m.Get("content")
		switch m.Get("role").String() {
		case "system":
			if text := stringifyContent(content); text != "" {
				systemParts = append(systemParts, text)
			}
		case "developer", "user":
			msg := parseUserLikeContent(content)
			msg.Role = m.Get("role").String()
			out.Context.Messages = append(out.Context.Messages, msg)
		case "assistant":
			for _, call := range m.Get("tool_calls").Array() {
				if call.Get("type").Exists() && call.Get("type").String() != "function" {
					continue
				}
				if id, name := call.Get("id").String(), call.Get("function.name").String(); id != "" && name != "" {
					toolNamesByID[id] = name
				}
			}
			out.Context.Messages = append(out.Context.Messages, buildAssistantMessage(m, out.ModelID))
		case "tool":
			name := ""
			if wire := m.Get("name"); wire.Exists() && wire.Type != gjson.Null {
				name = wire.String()
			} else if id := m.Get("tool_call_id").String(); id != "" {
				name = toolNamesByID[id]
			}
			out.Context.Messages = pushToolResultMessages(out.Context.Messages, content, m.Get("tool_call_id").String(), name)
		case "function":
			out.Context.Messages = pushToolResultMessages(out.Context.Messages, content, "", m.Get("name").String())
		}
	}
	if len(systemParts) > 0 {
		out.Context.SystemPrompt = []string{strings.Join(systemParts, "\n\n")}
	}
	for _, tool := range root.Get("tools").Array() {
		if tool.Get("type").String() != "function" {
			continue
		}
		parameters := any(&JSONObject{})
		if raw := tool.Get("function.parameters"); raw.Exists() && raw.Type != gjson.Null {
			parameters = FromGJSON(raw)
		}
		out.Context.Tools = append(out.Context.Tools, Tool{Name: tool.Get("function.name").String(),
			Description: tool.Get("function.description").String(), Parameters: parameters})
	}
	if effort := root.Get("reasoning_effort").String(); effort == "none" {
		out.ForceReasoningOff = true
	} else if isReasoningEffort(effort) {
		out.Reasoning = effort
	}
	if limit := root.Get("max_completion_tokens"); limit.Exists() && limit.Type == gjson.Number {
		value := limit.Int()
		out.MaxOutputTokens = &value
	} else if limit := root.Get("max_tokens"); limit.Exists() && limit.Type == gjson.Number {
		value := limit.Int()
		out.MaxOutputTokens = &value
	}
	out.IncludeStreamUsage = root.Get("stream_options.include_usage").Type == gjson.True
	out.PromptCacheKey = readBodyCacheKey(root)
	if out.PromptCacheKey == "" && header != nil {
		for _, name := range cacheKeyHeaders {
			if value := header(name); value != "" {
				out.PromptCacheKey = value
				break
			}
		}
	}
	// normalizeClientSessionKey: a blank key counts as none.
	if strings.TrimSpace(out.PromptCacheKey) == "" {
		out.PromptCacheKey = ""
	}
	return out, nil
}

func isReasoningEffort(effort string) bool {
	switch effort {
	case "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

func readBodyCacheKey(root gjson.Result) string {
	if direct := root.Get("prompt_cache_key"); direct.Type == gjson.String && direct.String() != "" {
		return direct.String()
	}
	metadata := root.Get("metadata")
	if !metadata.IsObject() {
		return ""
	}
	for _, field := range []string{"prompt_cache_key", "session_id", "conversation_id"} {
		if value := metadata.Get(field); value.Type == gjson.String && value.String() != "" {
			return value.String()
		}
	}
	return ""
}

func stringifyContent(content gjson.Result) string {
	if !content.Exists() || content.Type == gjson.Null {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	var out strings.Builder
	for _, part := range content.Array() {
		if part.Get("type").String() == "text" {
			out.WriteString(part.Get("text").String())
		}
	}
	return out.String()
}

func imageURL(part gjson.Result) string {
	if url := part.Get("image_url"); url.Type == gjson.String {
		return url.String()
	}
	return part.Get("image_url.url").String()
}

func parseUserLikeContent(content gjson.Result) Message {
	if !content.Exists() || content.Type == gjson.Null {
		return Message{Plain: true}
	}
	if content.Type == gjson.String {
		return Message{Plain: true, Text: content.String()}
	}
	msg := Message{Parts: []Part{}}
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "text":
			msg.Parts = append(msg.Parts, Part{Text: part.Get("text").String()})
		case "image_url":
			url := imageURL(part)
			if data, mimeType, ok := decodeDataURI(url); ok {
				msg.Parts = append(msg.Parts, Part{Image: true, Data: data, MimeType: mimeType})
			} else {
				msg.Parts = append(msg.Parts, Part{Text: "[image: " + url + "]"})
			}
		}
	}
	return msg
}

func buildAssistantMessage(m gjson.Result, modelID string) Message {
	msg := Message{Role: "assistant", API: "openai-completions", Provider: "openai", Model: modelID, Blocks: []Block{}}
	if reasoning := m.Get("reasoning_content").String(); reasoning != "" {
		msg.Blocks = append(msg.Blocks, Block{Type: "thinking", Thinking: reasoning, ThinkingSignature: "reasoning_content"})
	}
	if text := stringifyContent(m.Get("content")); text != "" {
		msg.Blocks = append(msg.Blocks, Block{Type: "text", Text: text})
	}
	for _, call := range m.Get("tool_calls").Array() {
		if call.Get("type").Exists() && call.Get("type").String() != "function" {
			continue
		}
		raw := call.Get("function.arguments").String()
		args := &JSONObject{}
		if raw != "" {
			if value, ok := ParseJSON(raw); ok {
				if object, isObject := value.(*JSONObject); isObject {
					args = object
				} else {
					args.Set("__raw", raw)
				}
			} else {
				args.Set("__raw", raw)
			}
		}
		msg.Blocks = append(msg.Blocks, Block{Type: "toolCall", ID: call.Get("id").String(), Name: call.Get("function.name").String(), Arguments: args})
	}
	return msg
}

func pushToolResultMessages(messages []Message, content gjson.Result, toolCallID, toolName string) []Message {
	var texts, images []Part
	if content.Type == gjson.String {
		if content.String() != "" {
			texts = append(texts, Part{Text: content.String()})
		}
	} else if content.IsArray() {
		for _, part := range content.Array() {
			switch part.Get("type").String() {
			case "text":
				texts = append(texts, Part{Text: part.Get("text").String()})
			case "image_url":
				url := imageURL(part)
				if data, mimeType, ok := decodeDataURI(url); ok {
					images = append(images, Part{Image: true, Data: data, MimeType: mimeType})
				} else {
					texts = append(texts, Part{Text: "[image: " + url + "]"})
				}
			}
		}
	}
	if len(texts) == 0 {
		texts = []Part{{}}
	}
	messages = append(messages, Message{Role: "toolResult", ToolCallID: toolCallID, ToolName: toolName, Parts: texts})
	if len(images) > 0 {
		messages = append(messages, Message{Role: "user", Parts: images})
	}
	return messages
}

// decodeDataURI is decodeDataUri: base64 or percent-encoded data: URIs.
func decodeDataURI(url string) (data, mimeType string, ok bool) {
	if len(url) < 5 || !strings.EqualFold(url[:5], "data:") {
		return "", "", false
	}
	comma := strings.IndexByte(url, ',')
	if comma < 0 {
		return "", "", false
	}
	header, payload := url[5:comma], url[comma+1:]
	isBase64 := strings.HasSuffix(strings.ToLower(header), ";base64")
	mimeType = header
	if isBase64 {
		mimeType = header[:len(header)-len(";base64")]
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if isBase64 {
		return payload, mimeType, true
	}
	var decoded []byte
	for i := 0; i < len(payload); i++ {
		if payload[i] != '%' {
			decoded = append(decoded, payload[i])
			continue
		}
		high, low := hexNibble(payload, i+1), hexNibble(payload, i+2)
		if high < 0 || low < 0 {
			// URIError: the parse fails as a whole.
			return "", "", false
		}
		decoded = append(decoded, byte(high<<4|low))
		i += 2
	}
	return base64.StdEncoding.EncodeToString(decoded), mimeType, true
}

func hexNibble(s string, i int) int {
	if i >= len(s) {
		return -1
	}
	switch c := s[i]; {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	}
	return -1
}

// NodeBase64 is Buffer.from(text, "base64"): standard or URL alphabet,
// whitespace and stray characters skipped, decoding stopping at padding.
func NodeBase64(text string) []byte {
	var clean strings.Builder
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/':
			clean.WriteByte(c)
		case c == '-':
			clean.WriteByte('+')
		case c == '_':
			clean.WriteByte('/')
		case c == '=':
			i = len(text)
		}
	}
	s := clean.String()
	if len(s)%4 == 1 {
		s = s[:len(s)-1]
	}
	out, _ := base64.RawStdEncoding.DecodeString(s)
	return out
}

// partsJSON is the JSON value of user content: a string or the parts array.
func contentJSON(msg Message) any {
	if msg.Plain {
		return msg.Text
	}
	items := make([]any, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		item := &JSONObject{}
		if part.Image {
			item.Set("type", "image")
			item.Set("data", part.Data)
			item.Set("mimeType", part.MimeType)
		} else {
			item.Set("type", "text")
			item.Set("text", part.Text)
		}
		items = append(items, item)
	}
	return items
}

func blocksJSON(blocks []Block) any {
	items := make([]any, 0, len(blocks))
	for _, block := range blocks {
		item := &JSONObject{}
		item.Set("type", block.Type)
		switch block.Type {
		case "thinking":
			item.Set("thinking", block.Thinking)
			if block.ThinkingSignature != "" {
				item.Set("thinkingSignature", block.ThinkingSignature)
			}
		case "text":
			item.Set("text", block.Text)
		case "toolCall":
			item.Set("id", block.ID)
			item.Set("name", block.Name)
			item.Set("arguments", argumentsValue(block.Arguments))
		}
		items = append(items, item)
	}
	return items
}

func argumentsValue(args *JSONObject) any {
	if args == nil {
		return &JSONObject{}
	}
	return args
}

// DeriveSessionID is the gateway's deriveSessionId: a stable id from the
// model, system prompt, tools and first message.
func DeriveSessionID(modelID string, ctx *Context) string {
	parts := []string{modelID}
	if len(ctx.SystemPrompt) > 0 {
		parts = append(parts, strings.Join(ctx.SystemPrompt, "\n\n"))
	}
	if len(ctx.Tools) > 0 {
		parts = append(parts, ToolsJSON(ctx.Tools))
	}
	if len(ctx.Messages) > 0 {
		first := ctx.Messages[0]
		object := &JSONObject{}
		object.Set("role", first.Role)
		switch first.Role {
		case "assistant":
			object.Set("content", blocksJSON(first.Blocks))
		default:
			object.Set("content", contentJSON(first))
		}
		parts = append(parts, StringifyJSON(object))
	}
	return DeterministicUUID(strings.Join(parts, "\x00"))
}

// ToolsJSON is JSON.stringify(context.tools).
func ToolsJSON(tools []Tool) string {
	items := make([]any, 0, len(tools))
	for _, tool := range tools {
		item := &JSONObject{}
		item.Set("name", tool.Name)
		item.Set("description", tool.Description)
		item.Set("parameters", tool.Parameters)
		items = append(items, item)
	}
	return StringifyJSON(items)
}
