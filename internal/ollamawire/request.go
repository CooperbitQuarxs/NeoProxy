package ollamawire

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitransform"
	"github.com/tidwall/gjson"
)

const (
	// DefaultBaseURL is the Ollama Cloud origin a blank base resolves to.
	DefaultBaseURL = "https://ollama.com"
	// cloudProvider is the provider whose num_predict is capped and whose
	// effort map applies.
	cloudProvider = "ollama-cloud"
	// cloudMaxOutputTokens is the num_predict ceiling Ollama Cloud accepts.
	cloudMaxOutputTokens = 65_536
	// imageOmittedPlaceholder is IMAGE_OMITTED_PLACEHOLDER.
	imageOmittedPlaceholder = "[image omitted: model does not support vision]"
)

// Model is the part of an ollama-chat row the request builder reads.
type Model struct {
	ID       string
	Provider string
	// Reasoning is model.reasoning; EffortMap is model.thinking.effortMap.
	Reasoning bool
	EffortMap map[string]string
	// SupportsImages is model.input including "image".
	SupportsImages bool
	// StripThinkingHistory is ollamaChatDropsOwnThinking: assistant thinking
	// is removed from replayed history.
	StripThinkingHistory bool
	OmitMaxOutputTokens  bool
}

// ToolChoice is the gateway's normalized tool_choice: Mode "auto", "none" or
// "required", or Named for {name} (Name empty when the client named none).
type ToolChoice struct {
	Mode  string
	Named bool
	Name  string
}

// Options are the streamSimple options an ollama-chat request takes.
type Options struct {
	// Reasoning is the effort resolveOpenAiReasoningEffort settled on, ""
	// for none.
	Reasoning        string
	DisableReasoning bool
	ToolChoice       ToolChoice
	MaxTokens        *int64
	// Temperature and TopP are raw JSON values, nil when absent.
	Temperature json.RawMessage
	TopP        json.RawMessage
}

// NormalizeBaseURL is normalizeBaseUrl: trimmed, one trailing slash and a
// trailing /api removed, https://ollama.com when blank.
func NormalizeBaseURL(baseURL string) string {
	value := jsTrim(baseURL)
	if value == "" {
		return DefaultBaseURL
	}
	value = strings.TrimSuffix(value, "/")
	return strings.TrimSuffix(value, "/api")
}

// ParseToolChoice is the gateway's normalizeToolChoice over a chat body's
// tool_choice.
func ParseToolChoice(value gjson.Result) ToolChoice {
	switch {
	case value.Type == gjson.String:
		switch value.Str {
		case "auto", "none", "required":
			return ToolChoice{Mode: value.Str}
		}
	case value.IsObject():
		if function := value.Get("function"); function.Exists() && function.Type != gjson.Null && function.Type != gjson.False &&
			!(function.Type == gjson.Number && function.Num == 0) && !(function.Type == gjson.String && function.Str == "") {
			name := function.Get("name")
			return ToolChoice{Named: true, Name: name.String()}
		}
		if value.Get("type").String() == "tool" && value.Get("name").Type == gjson.String {
			return ToolChoice{Named: true, Name: value.Get("name").Str}
		}
	}
	return ToolChoice{}
}

// mapReasoning is mapReasoning: false to switch a reasoning model's thinking
// off, an Ollama think level, or "" to omit think.
func mapReasoning(model *Model, reasoning string, disableReasoning bool) json.RawMessage {
	if disableReasoning && model.Reasoning {
		return json.RawMessage("false")
	}
	mapped := reasoning
	if model.Provider == cloudProvider && reasoning != "" {
		if effort, ok := model.EffortMap[reasoning]; ok {
			mapped = effort
		}
	}
	switch mapped {
	case "minimal", "low":
		return jsjson.Marshal("low")
	case "medium":
		return jsjson.Marshal("medium")
	case "high", "xhigh":
		return jsjson.Marshal("high")
	case "max":
		return jsjson.Marshal("max")
	}
	return nil
}

// mapToolChoice is mapToolChoice: auto and absent are omitted.
func mapToolChoice(choice ToolChoice) string {
	switch {
	case choice.Named:
		return "required"
	case choice.Mode == "none":
		return "none"
	case choice.Mode == "required":
		return "required"
	}
	return ""
}

// selectTools is selectToolsForToolChoice: a named choice keeps only that
// tool (none when absent); a choice naming no tool keeps them all.
func selectTools(tools []cursorwire.Tool, choice ToolChoice) []cursorwire.Tool {
	if !choice.Named || choice.Name == "" || tools == nil {
		return tools
	}
	for _, tool := range tools {
		if tool.Name == choice.Name {
			return []cursorwire.Tool{tool}
		}
	}
	return []cursorwire.Tool{}
}

// BuildBody is createChatBody: the /api/chat body for one turn.
func BuildBody(model *Model, ctx *cursorwire.Context, opts Options) json.RawMessage {
	think := mapReasoning(model, opts.Reasoning, opts.DisableReasoning)
	toolChoice := mapToolChoice(opts.ToolChoice)
	tools := convertTools(selectTools(ctx.Tools, opts.ToolChoice))
	var runtime []jsjson.Field
	if opts.MaxTokens != nil && !model.OmitMaxOutputTokens {
		limit := *opts.MaxTokens
		if model.Provider == cloudProvider && limit > cloudMaxOutputTokens {
			limit = cloudMaxOutputTokens
		}
		runtime = append(runtime, jsjson.Field{Name: "num_predict", Value: jsjson.Normalize([]byte(jsonInt(limit)))})
	}
	if len(opts.Temperature) > 0 {
		runtime = append(runtime, jsjson.Field{Name: "temperature", Value: jsjson.Normalize(opts.Temperature)})
	}
	if len(opts.TopP) > 0 {
		runtime = append(runtime, jsjson.Field{Name: "top_p", Value: jsjson.Normalize(opts.TopP)})
	}
	var runtimeOptions json.RawMessage
	if len(runtime) > 0 {
		runtimeOptions = jsjson.Object(runtime...)
	}
	var toolChoiceValue json.RawMessage
	if toolChoice != "" {
		toolChoiceValue = jsjson.Marshal(toolChoice)
	}
	return jsjson.Object(
		jsjson.Field{Name: "model", Value: jsjson.Marshal(model.ID)},
		jsjson.Field{Name: "messages", Value: convertMessages(model, ctx)},
		jsjson.Field{Name: "tools", Value: tools},
		jsjson.Field{Name: "think", Value: think},
		jsjson.Field{Name: "tool_choice", Value: toolChoiceValue},
		jsjson.Field{Name: "options", Value: runtimeOptions},
		jsjson.Field{Name: "stream", Value: json.RawMessage("true")},
	)
}

func jsonInt(value int64) string {
	return string(jsjson.Marshal(value))
}

// convertTools is convertTools: each tool's wire schema sanitized for
// Ollama's Go parser; no tools omits the field.
func convertTools(tools []cursorwire.Tool) json.RawMessage {
	if len(tools) == 0 {
		return nil
	}
	items := make([]json.RawMessage, len(tools))
	for i, tool := range tools {
		wire := codexwire.ToolWireSchema(gjson.Parse(cursorwire.StringifyJSON(tool.Parameters)))
		items[i] = jsjson.Object(
			jsjson.Field{Name: "type", Value: jsjson.Marshal("function")},
			jsjson.Field{Name: "function", Value: jsjson.Object(
				jsjson.Field{Name: "name", Value: jsjson.Marshal(tool.Name)},
				jsjson.Field{Name: "description", Value: jsjson.Marshal(tool.Description)},
				jsjson.Field{Name: "parameters", Value: codexwire.SanitizeSchemaForOllama(wire)},
			)},
		)
	}
	return jsonArray(items)
}

func jsonArray(items []json.RawMessage) json.RawMessage {
	out := []byte{'['}
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}

// message is OllamaMessage before serialization.
type message struct {
	role      string
	content   string
	images    []string
	thinking  string
	toolCalls []cursorwire.Block
	toolName  *string
}

func (m message) encode() json.RawMessage {
	fields := []jsjson.Field{{Name: "role", Value: jsjson.Marshal(m.role)}}
	if m.toolName != nil {
		fields = append(fields, jsjson.Field{Name: "tool_name", Value: jsjson.Marshal(*m.toolName)})
	}
	fields = append(fields, jsjson.Field{Name: "content", Value: jsjson.Marshal(m.content)})
	if m.images != nil {
		images := make([]json.RawMessage, len(m.images))
		for i, data := range m.images {
			images[i] = jsjson.Marshal(data)
		}
		fields = append(fields, jsjson.Field{Name: "images", Value: jsonArray(images)})
	}
	if m.thinking != "" {
		fields = append(fields, jsjson.Field{Name: "thinking", Value: jsjson.Marshal(m.thinking)})
	}
	if len(m.toolCalls) > 0 {
		calls := make([]json.RawMessage, len(m.toolCalls))
		for i, call := range m.toolCalls {
			var arguments any = call.Arguments
			if call.Arguments == nil {
				arguments = &cursorwire.JSONObject{}
			}
			calls[i] = jsjson.Object(
				jsjson.Field{Name: "type", Value: jsjson.Marshal("function")},
				jsjson.Field{Name: "function", Value: jsjson.Object(
					jsjson.Field{Name: "name", Value: jsjson.Marshal(call.Name)},
					jsjson.Field{Name: "arguments", Value: json.RawMessage(cursorwire.StringifyJSON(arguments))},
				)},
			)
		}
		fields = append(fields, jsjson.Field{Name: "tool_calls", Value: jsonArray(calls)})
	}
	return jsjson.Object(fields...)
}

// toPlainContent is toPlainContent: text joined by newlines (plus the
// vision placeholder when images are withheld) and the images' base64 data.
func toPlainContent(msg cursorwire.Message, supportsImages bool) (string, []string) {
	if msg.Plain {
		return msg.Text, nil
	}
	var texts, images []string
	omitted := false
	for _, part := range msg.Parts {
		switch {
		case !part.Image:
			texts = append(texts, part.Text)
		case supportsImages:
			images = append(images, part.Data)
		default:
			omitted = true
		}
	}
	return joinTextWithImagePlaceholder(strings.Join(texts, "\n"), omitted), images
}

// joinTextWithImagePlaceholder is joinTextWithImagePlaceholder.
func joinTextWithImagePlaceholder(text string, omitted bool) string {
	switch {
	case !omitted:
		return text
	case text == "":
		return imageOmittedPlaceholder
	}
	return text + "\n" + imageOmittedPlaceholder
}

// convertMessages is convertMessages: system prompts first as system turns,
// the transformed history after them (a developer turn stays system unless
// the user authored it), stripped assistant thinking where the row says so,
// and the last non-prefix system turn demoted to user when no user turn
// would otherwise reach Ollama.
func convertMessages(model *Model, ctx *cursorwire.Context) json.RawMessage {
	prompts := cursorwire.NormalizeSystemPrompts(ctx.SystemPrompt)
	history := make([]cursorwire.Message, 0, len(prompts)+len(ctx.Messages))
	for _, prompt := range prompts {
		history = append(history, cursorwire.Message{Role: "developer", Plain: true, Text: prompt})
	}
	history = append(history, ctx.Messages...)
	converted := make([]message, 0, len(history))
	hasUser := false
	for _, msg := range neopitransform.Messages(history, neopitransform.Target{ModelID: model.ID}) {
		out := convertMessage(msg, model.SupportsImages)
		if model.StripThinkingHistory && out.role == "assistant" {
			out.thinking = ""
		}
		hasUser = hasUser || out.role == "user"
		converted = append(converted, out)
	}
	if !hasUser {
		for i := len(converted) - 1; i >= len(prompts); i-- {
			if converted[i].role == "system" {
				converted[i].role = "user"
				break
			}
		}
	}
	items := make([]json.RawMessage, len(converted))
	for i, msg := range converted {
		items[i] = msg.encode()
	}
	return jsonArray(items)
}

// convertMessage is convertMessage. Gateway developer turns carry no
// attribution, so every developer turn keeps the system role.
func convertMessage(msg cursorwire.Message, supportsImages bool) message {
	switch msg.Role {
	case "user":
		content, images := toPlainContent(msg, supportsImages)
		return message{role: "user", content: content, images: images}
	case "developer":
		content, images := toPlainContent(msg, supportsImages)
		return message{role: "system", content: content, images: images}
	case "toolResult":
		content, images := toPlainContent(msg, supportsImages)
		name := msg.ToolName
		return message{role: "tool", toolName: &name, content: content, images: images}
	}
	var text, thinking []string
	var calls []cursorwire.Block
	for _, block := range msg.Blocks {
		switch block.Type {
		case "text":
			text = append(text, block.Text)
		case "thinking":
			thinking = append(thinking, block.Thinking)
		case "toolCall":
			calls = append(calls, block)
		}
	}
	return message{role: "assistant", content: strings.Join(text, "\n"), thinking: strings.Join(thinking, "\n"), toolCalls: calls}
}
