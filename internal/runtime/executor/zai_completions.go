package executor

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

const neoPiToolImagesNote = "Attached image(s) from tool result:"

// neoPiZaiCompletionsRequest is NeoPi's openai-completions buildParams for a
// Z.ai catalog row (thinkingFormat "zai"), shaped from the OpenAI Chat body a
// client sent after NeoProxy's thinking pipeline set reasoning_effort.
func neoPiZaiCompletionsRequest(input []byte, row *registry.NeoPiModel) ([]byte, error) {
	if !gjson.ValidBytes(input) {
		return nil, fmt.Errorf("zai: invalid Chat Completions JSON")
	}
	compat := row.CompletionsCompat
	if compat == nil {
		return nil, fmt.Errorf("zai: model %s has no NeoPi completions compat", row.ID)
	}
	root := gjson.ParseBytes(input)
	images := false
	for _, input := range row.Input {
		images = images || input == "image"
	}
	fields := []jsjson.Field{{Name: "model", Value: jsjson.Marshal(row.ID)}, {Name: "messages", Value: jsjson.Marshal(neoPiZaiCompletionsMessages(root.Get("messages"), images))}, {Name: "stream", Value: jsjson.Marshal(true)}}
	if compat.SupportsUsageInStreaming {
		fields = append(fields, jsjson.Field{Name: "stream_options", Value: []byte(`{"include_usage":true}`)})
	}
	if compat.SupportsStore {
		fields = append(fields, jsjson.Field{Name: "store", Value: jsjson.Marshal(false)})
	}
	sampling := []string{"temperature", "top_p", "top_k", "min_p"}
	if compat.SupportsPenaltyAndStopParams {
		sampling = append(sampling, "presence_penalty", "repetition_penalty", "frequency_penalty")
	}
	if compat.SupportsSamplingParams {
		for _, name := range sampling {
			if value := root.Get(name); value.Exists() && value.Type != gjson.Null {
				fields = append(fields, jsjson.Field{Name: name, Value: []byte(value.Raw)})
			}
		}
	}
	if stop := root.Get("stop"); compat.SupportsPenaltyAndStopParams && stop.Exists() {
		if stop.IsArray() {
			var seqs []string
			for _, seq := range stop.Array() {
				seqs = append(seqs, seq.String())
			}
			if len(seqs) == 1 {
				fields = append(fields, jsjson.Field{Name: "stop", Value: jsjson.Marshal(seqs[0])})
			} else if len(seqs) > 1 {
				fields = append(fields, jsjson.Field{Name: "stop", Value: jsjson.Marshal(seqs[:min(len(seqs), 4)])})
			}
		} else if stop.String() != "" {
			fields = append(fields, jsjson.Field{Name: "stop", Value: jsjson.Marshal(stop.String())})
		}
	}
	var toolNames []string
	if tools := root.Get("tools"); tools.IsArray() && len(tools.Array()) > 0 {
		var wire []json.RawMessage
		for _, tool := range tools.Array() {
			function := tool.Get("function")
			parameters := []byte(function.Get("parameters").Raw)
			if len(parameters) == 0 {
				parameters = []byte(`{"type":"object","properties":{}}`)
			}
			name := function.Get("name").String()
			toolNames = append(toolNames, name)
			wire = append(wire, jsjson.Object(jsjson.Field{Name: "type", Value: jsjson.Marshal("function")}, jsjson.Field{Name: "function", Value: jsjson.Object(
				jsjson.Field{Name: "name", Value: jsjson.Marshal(name)},
				jsjson.Field{Name: "description", Value: jsjson.Marshal(function.Get("description").String())},
				jsjson.Field{Name: "parameters", Value: parameters})}))
		}
		fields = append(fields, jsjson.Field{Name: "tools", Value: jsjson.Marshal(wire)})
	}
	if choice := root.Get("tool_choice"); choice.Exists() && compat.SupportsToolChoice {
		raw := []byte(choice.Raw)
		forced := choice.String() == "required" || choice.IsObject()
		named := choice.Get("function.name").String()
		switch {
		case choice.IsObject() && !compat.SupportsNamedToolChoice:
			raw = jsjson.Marshal("required")
		case forced && !compat.SupportsForcedToolChoice:
			raw = jsjson.Marshal("auto")
		}
		keep := true
		if len(toolNames) == 0 && (choice.String() == "none" || forced) {
			keep = false
		}
		if named != "" && !containsString(toolNames, named) {
			keep = false
		}
		if keep {
			fields = append(fields, jsjson.Field{Name: "tool_choice", Value: raw})
		}
	}
	maxTokens := int64(row.MaxTokens)
	for _, name := range []string{"max_completion_tokens", "max_tokens"} {
		if value := root.Get(name).Int(); value > 0 {
			maxTokens = value
			break
		}
	}
	if compat.ClampOutputToModelMax && row.MaxTokens > 0 {
		maxTokens = min(maxTokens, int64(row.MaxTokens))
	}
	if maxTokens > 0 {
		field := compat.MaxTokensField
		if field == "" {
			field = "max_tokens"
		}
		fields = append(fields, jsjson.Field{Name: field, Value: jsjson.Marshal(maxTokens)})
	}
	if row.Reasoning && compat.ThinkingFormat == "zai" {
		effort := strings.TrimSpace(root.Get("reasoning_effort").String())
		if effort == "none" || effort == "off" {
			effort = ""
		}
		if effort == "" && row.Thinking != nil && row.Thinking.RequiresEffort && len(row.Thinking.Efforts) > 0 {
			effort = row.Thinking.Efforts[0]
		}
		if effort == "" {
			fields = append(fields, jsjson.Field{Name: "thinking", Value: []byte(`{"type":"disabled"}`)})
		} else {
			fields = append(fields, jsjson.Field{Name: "thinking", Value: []byte(`{"type":"enabled"}`)})
			if compat.SupportsReasoningEffort {
				if mapped := compat.ReasoningEffortMap[effort]; mapped != "" {
					effort = mapped
				}
				fields = append(fields, jsjson.Field{Name: "reasoning_effort", Value: jsjson.Marshal(effort)})
			}
		}
	}
	return jsjson.Marshal(jsjson.Object(fields...)), nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// neoPiZaiCompletionsMessages is convertMessages for a host without a
// developer role: system turns stay system, assistant turns replay
// reasoning_content and tool calls, and consecutive tool results are sent as
// text with their images attached in one following user turn.
func neoPiZaiCompletionsMessages(messages gjson.Result, images bool) []json.RawMessage {
	list := messages.Array()
	out := make([]json.RawMessage, 0, len(list))
	for i := 0; i < len(list); i++ {
		message := list[i]
		switch role := message.Get("role").String(); role {
		case "system", "developer":
			out = append(out, jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal("system")}, jsjson.Field{Name: "content", Value: []byte(message.Get("content").Raw)}))
		case "assistant":
			content := message.Get("content")
			reasoning := message.Get("reasoning_content")
			calls := message.Get("tool_calls")
			hasCalls := calls.IsArray() && len(calls.Array()) > 0
			hasReasoning := reasoning.Type == gjson.String && reasoning.String() != ""
			contentRaw := []byte(content.Raw)
			if !content.Exists() || content.Type == gjson.Null {
				contentRaw = []byte("null")
				if hasReasoning || hasCalls {
					contentRaw = jsjson.Marshal("")
				}
			}
			hasContent := content.Type == gjson.String && content.String() != "" || content.IsArray() && len(content.Array()) > 0
			if !hasContent && !hasCalls && !hasReasoning {
				continue
			}
			fields := []jsjson.Field{{Name: "role", Value: jsjson.Marshal("assistant")}, {Name: "content", Value: contentRaw}}
			if hasReasoning {
				fields = append(fields, jsjson.Field{Name: "reasoning_content", Value: jsjson.Marshal(reasoning.String())})
			}
			if hasCalls {
				var wire []json.RawMessage
				for _, call := range calls.Array() {
					arguments := call.Get("function.arguments")
					args := arguments.String()
					if arguments.Type != gjson.String {
						args = arguments.Raw
					}
					wire = append(wire, jsjson.Object(jsjson.Field{Name: "id", Value: jsjson.Marshal(call.Get("id").String())}, jsjson.Field{Name: "type", Value: jsjson.Marshal("function")}, jsjson.Field{Name: "function", Value: jsjson.Object(
						jsjson.Field{Name: "name", Value: jsjson.Marshal(call.Get("function.name").String())},
						jsjson.Field{Name: "arguments", Value: jsjson.Marshal(args)})}))
				}
				fields = append(fields, jsjson.Field{Name: "tool_calls", Value: jsjson.Marshal(wire)})
			}
			out = append(out, jsjson.Object(fields...))
		case "tool":
			var attached []json.RawMessage
			for ; i < len(list) && list[i].Get("role").String() == "tool"; i++ {
				tool := list[i]
				content := tool.Get("content")
				text := content.String()
				hasImages := false
				if content.IsArray() {
					var texts []string
					for _, part := range content.Array() {
						switch part.Get("type").String() {
						case "text":
							texts = append(texts, part.Get("text").String())
						case "image_url":
							hasImages = true
							if images {
								attached = append(attached, json.RawMessage(jsjson.Object(jsjson.Field{Name: "type", Value: jsjson.Marshal("image_url")}, jsjson.Field{Name: "image_url", Value: jsjson.Object(jsjson.Field{Name: "url", Value: jsjson.Marshal(part.Get("image_url.url").String())})})))
							}
						}
					}
					text = strings.Join(texts, "\n")
				}
				switch {
				case hasImages && !images:
					if text == "" {
						text = neoPiNonVisionImagePlaceholder
					} else {
						text += "\n" + neoPiNonVisionImagePlaceholder
					}
				case text == "" && hasImages:
					text = neoPiImageOnlyPrefix
				}
				out = append(out, jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal("tool")}, jsjson.Field{Name: "content", Value: jsjson.Marshal(text)}, jsjson.Field{Name: "tool_call_id", Value: jsjson.Marshal(tool.Get("tool_call_id").String())}))
			}
			i--
			if len(attached) > 0 {
				parts := append([]json.RawMessage{neoPiTextBlock(neoPiToolImagesNote)}, attached...)
				out = append(out, jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal("user")}, jsjson.Field{Name: "content", Value: jsjson.Marshal(parts)}))
			}
		default:
			content := message.Get("content")
			if content.Type == gjson.String && strings.TrimSpace(content.String()) == "" {
				continue
			}
			out = append(out, jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal(role)}, jsjson.Field{Name: "content", Value: []byte(content.Raw)}))
		}
	}
	return out
}
