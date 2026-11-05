package googlewire

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// first returns the first existing value among camelCase and snake_case keys.
func first(root gjson.Result, paths ...string) gjson.Result {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func geminiBlocks(parts []gjson.Result, supportsImages bool) []Block {
	var blocks []Block
	for _, part := range parts {
		if text := part.Get("text"); text.Type == gjson.String {
			blocks = append(blocks, Block{Type: BlockText, Text: text.String()})
			continue
		}
		if inline := first(part, "inlineData", "inline_data"); inline.Exists() {
			blocks = append(blocks, Block{Type: BlockImage, MimeType: first(inline, "mimeType", "mime_type").String(), Data: inline.Get("data").String()})
			continue
		}
		if file := first(part, "fileData", "file_data"); file.Exists() {
			blocks = append(blocks, Block{Type: BlockImage, MimeType: first(file, "mimeType", "mime_type").String(), URL: first(file, "fileUri", "file_uri").String()})
		}
	}
	return blocks
}

// geminiResult reads a functionResponse's response object as tool result
// text: NeoPi's {output} and {error} shapes carry their string, any other
// object is replayed as its JSON.
func geminiResult(response gjson.Result) (text string, isError bool) {
	if response.IsObject() {
		if value := response.Get("error"); value.Type == gjson.String && !response.Get("output").Exists() {
			return value.String(), true
		}
		if value := response.Get("output"); value.Type == gjson.String {
			return value.String(), false
		}
	}
	if response.Type == gjson.String {
		return response.String(), false
	}
	if !response.Exists() {
		return "", false
	}
	return string(jsjson.Normalize([]byte(response.Raw))), false
}

type pendingCall struct{ id, name string }

// FromGemini reads a native Gemini generateContent request as the NeoPi
// context and options a NeoPi agent would have used, without an OpenAI Chat
// detour: thought parts and every signature survive. A model turn is the
// model's own unless one of its signatures was minted by another provider
// (signature must pass Model.NormalizeSignature), in which case NeoPi treats it
// as foreign history: signatures are dropped and thinking is demoted to text.
func FromGemini(body []byte, model *Model) (Context, Options, error) {
	root := gjson.ParseBytes(body)
	var ctx Context
	supportsImages := model.supportsImages()
	for _, part := range first(root, "systemInstruction", "system_instruction").Get("parts").Array() {
		if text := part.Get("text"); text.Type == gjson.String && text.String() != "" {
			ctx.SystemPrompts = append(ctx.SystemPrompts, text.String())
		}
	}

	var pending []pendingCall
	callCount := 0
	for _, content := range root.Get("contents").Array() {
		parts := content.Get("parts").Array()
		if content.Get("role").String() == "model" {
			msg := Message{Role: RoleAssistant, Provider: model.Provider, API: model.API, Model: model.ID, StopReason: "stop"}
			foreign := false
			type signed struct {
				index        int
				signature    string
				functionCall bool
			}
			var signatures []signed
			for _, part := range parts {
				signature := first(part, "thoughtSignature", "thought_signature").String()
				if call := first(part, "functionCall", "function_call"); call.Exists() {
					callCount++
					id := call.Get("id").String()
					if id == "" {
						id = "call_" + strconv.Itoa(callCount)
					}
					args := json.RawMessage("{}")
					if raw := call.Get("args"); raw.IsObject() {
						args = json.RawMessage(raw.Raw)
					}
					msg.Content = append(msg.Content, Block{Type: BlockToolCall, ID: id, Name: call.Get("name").String(), Arguments: args, Signature: signature})
					msg.StopReason = "toolUse"
					pending = append(pending, pendingCall{id: id, name: call.Get("name").String()})
				} else if text := part.Get("text"); text.Type == gjson.String || signature != "" {
					block := Block{Type: BlockText, Text: text.String(), Signature: signature}
					if part.Get("thought").Type == gjson.True {
						block.Type = BlockThinking
					}
					msg.Content = append(msg.Content, block)
				} else {
					continue
				}
				if signature == SkipThoughtSignature {
					// The bypass sentinel carries no provenance on a call, but Gemini
					// never signs a thought with it: a thought carrying it was
					// rewritten from another provider's signature, so the turn is foreign.
					msg.Content[len(msg.Content)-1].Signature = ""
					if msg.Content[len(msg.Content)-1].Type == BlockThinking {
						foreign = true
					}
				} else if signature != "" {
					signatures = append(signatures, signed{len(msg.Content) - 1, signature, msg.Content[len(msg.Content)-1].Type == BlockToolCall})
				}
			}
			for _, entry := range signatures {
				if model.NormalizeSignature == nil {
					continue
				}
				if normalized, ok := model.NormalizeSignature(entry.signature, entry.functionCall); ok {
					msg.Content[entry.index].Signature = normalized
				} else {
					foreign = true
				}
			}
			if foreign {
				msg.Provider = foreignProvider
				for i := range msg.Content {
					msg.Content[i].Signature = ""
				}
			}
			ctx.Messages = append(ctx.Messages, msg)
			continue
		}

		var rest []gjson.Result
		for _, part := range parts {
			response := first(part, "functionResponse", "function_response")
			if !response.Exists() {
				rest = append(rest, part)
				continue
			}
			name, id := response.Get("name").String(), response.Get("id").String()
			match := -1
			for i, call := range pending {
				if id != "" && call.id == id || id == "" && (call.name == name || name == "") {
					match = i
					break
				}
			}
			if match < 0 {
				for i, call := range pending {
					if call.name == name || name == "" {
						match = i
						break
					}
				}
			}
			callID := id
			if match >= 0 {
				callID = pending[match].id
				if name == "" {
					name = pending[match].name
				}
				pending = append(pending[:match], pending[match+1:]...)
			} else if callID == "" {
				callCount++
				callID = "call_" + strconv.Itoa(callCount)
			}
			text, isError := geminiResult(response.Get("response"))
			blocks := []Block{{Type: BlockText, Text: text}}
			if text == "" {
				blocks = nil
			}
			blocks = append(blocks, geminiBlocks(response.Get("parts").Array(), supportsImages)...)
			ctx.Messages = append(ctx.Messages, Message{Role: RoleToolResult, ToolCallID: callID, ToolName: name, IsError: isError, Content: blocks})
		}
		if len(rest) > 0 {
			ctx.Messages = append(ctx.Messages, Message{Role: RoleUser, Content: geminiBlocks(rest, supportsImages)})
		}
	}

	for _, tool := range root.Get("tools").Array() {
		for _, declaration := range first(tool, "functionDeclarations", "function_declarations").Array() {
			parameters := first(declaration, "parametersJsonSchema", "parameters_json_schema", "parameters")
			raw := json.RawMessage(`{"type":"object","properties":{}}`)
			if parameters.Exists() {
				raw = json.RawMessage(parameters.Raw)
			}
			ctx.Tools = append(ctx.Tools, Tool{Name: declaration.Get("name").String(), Description: declaration.Get("description").String(), Parameters: raw})
		}
	}

	var opts Options
	generation := first(root, "generationConfig", "generation_config")
	float := func(names ...string) *float64 {
		if value := first(generation, names...); value.Type == gjson.Number {
			number := value.Float()
			return &number
		}
		return nil
	}
	opts.Temperature, opts.TopP, opts.TopK = float("temperature"), float("topP", "top_p"), float("topK", "top_k")
	opts.PresencePenalty = float("presencePenalty", "presence_penalty")
	if value := first(generation, "maxOutputTokens", "max_output_tokens"); value.Type == gjson.Number && value.Int() > 0 {
		capped := int(value.Int())
		opts.MaxTokens = &capped
	}

	thinking := first(generation, "thinkingConfig", "thinking_config")
	if thinking.Exists() {
		if include := first(thinking, "includeThoughts", "include_thoughts"); include.Type == gjson.False {
			opts.HideThinkingSummary = true
		}
		if level := first(thinking, "thinkingLevel", "thinking_level"); level.Type == gjson.String {
			opts.Reasoning = strings.ToLower(level.String())
		} else if budget := first(thinking, "thinkingBudget", "thinking_budget"); budget.Type == gjson.Number {
			if budget.Int() == 0 {
				opts.DisableReasoning = true
			} else {
				opts.Reasoning = effortForBudget(model, int(budget.Int()))
			}
		}
	}

	calling := first(first(root, "toolConfig", "tool_config"), "functionCallingConfig", "function_calling_config")
	switch strings.ToUpper(calling.Get("mode").String()) {
	case "NONE":
		opts.ToolChoice = ToolChoice{Mode: "none"}
	case "ANY":
		names := first(calling, "allowedFunctionNames", "allowed_function_names").Array()
		if len(names) == 1 {
			opts.ToolChoice = ToolChoice{Mode: "named", Name: names[0].String()}
		} else {
			opts.ToolChoice = ToolChoice{Mode: "any"}
		}
	}
	if tier := strings.ToLower(first(root, "serviceTier", "service_tier").String()); tier == "flex" || tier == "priority" {
		opts.ServiceTier = tier
	}
	if cached := first(root, "cachedContent", "cached_content"); cached.Type == gjson.String {
		opts.CachedContent = cached.String()
	}
	return ctx, opts, nil
}

// effortForBudget maps a native thinkingBudget back to the model's effort:
// the smallest effort whose budget covers it, else the highest. A dynamic
// budget (-1) is the highest effort.
func effortForBudget(model *Model, budget int) string {
	if model.Thinking == nil || len(model.Thinking.Efforts) == 0 {
		return "high"
	}
	best := ""
	for _, effort := range effortLadder {
		if !supportsEffort(model, effort) {
			continue
		}
		limit := model.Thinking.EffortBudgets[effort]
		if limit == 0 {
			limit = googleThinkingBudgets[effort]
		}
		if budget > 0 && limit >= budget {
			return effort
		}
		best = effort
	}
	return best
}
