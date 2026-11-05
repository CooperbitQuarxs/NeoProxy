package helps

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/googlewire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// factoryDroidGoogleAPIs are the transports whose outputs carry
// Gemini-verifiable signatures; Factory's own Gemini outputs are stamped
// google-generative-ai.
var factoryDroidGoogleAPIs = map[string]bool{"google-generative-ai": true, "google-vertex": true, "google-gemini-cli": true}

// FactoryDroidGeminiOptions are streamFactoryDroidGemini's options.
type FactoryDroidGeminiOptions struct {
	// ModelID is model.requestModelId ?? model.id.
	ModelID string
	// Temperature, TopP and TopK are the caller's; nil takes droid's
	// defaults (1, 0.95, 64).
	Temperature, TopP, TopK *float64
	// StopSequences is sent when set, even empty.
	StopSequences []string
	// Effort and Disabled are the attempt's reasoning; LevelMedium is the
	// route's level-medium thinking dialect.
	Effort      string
	Disabled    bool
	LevelMedium bool
}

// FactoryDroidGeminiBody is streamFactoryDroidGemini's request body for a
// NeoPi context whose system prompt already leads with the Droid prefix,
// with the wire-name to declared-name map for returned calls. model is the
// factory-droid-agent model (provider factory-droid).
func FactoryDroidGeminiBody(model *googlewire.Model, ctx googlewire.Context, opts FactoryDroidGeminiOptions) ([]byte, map[string]string, error) {
	contents, system := factoryDroidGeminiContents(model, ctx)
	thinking := jsjson.Object(jsjson.Field{Name: "includeThoughts", Value: jsjson.Marshal(false)})
	if !opts.Disabled {
		thinking = jsjson.Object(
			jsjson.Field{Name: "includeThoughts", Value: jsjson.Marshal(true)},
			jsjson.Field{Name: "thinkingLevel", Value: jsjson.Marshal(factoryDroidGeminiThinkingLevel(opts.Effort, opts.LevelMedium))},
		)
	}
	number := func(value *float64, fallback float64) json.RawMessage {
		if value != nil {
			return json.RawMessage(jsjson.Number(*value))
		}
		return json.RawMessage(jsjson.Number(fallback))
	}
	config := []jsjson.Field{
		{Name: "temperature", Value: number(opts.Temperature, 1)},
		{Name: "topP", Value: number(opts.TopP, 0.95)},
		{Name: "topK", Value: number(opts.TopK, 64)},
	}
	if opts.StopSequences != nil {
		config = append(config, jsjson.Field{Name: "stopSequences", Value: jsjson.Marshal(opts.StopSequences)})
	}
	config = append(config, jsjson.Field{Name: "thinkingConfig", Value: thinking})
	fields := []jsjson.Field{
		{Name: "model", Value: jsjson.Marshal(opts.ModelID)},
		{Name: "contents", Value: contents},
	}
	if system != "" {
		fields = append(fields, jsjson.Field{Name: "systemInstruction", Value: jsjson.Object(jsjson.Field{Name: "parts",
			Value: jsjson.Marshal([]json.RawMessage{jsjson.Object(jsjson.Field{Name: "text", Value: jsjson.Marshal(system)})})})})
	}
	fields = append(fields, jsjson.Field{Name: "generationConfig", Value: jsjson.Object(config...)})
	names := make([]string, 0, len(ctx.Tools))
	for _, tool := range ctx.Tools {
		names = append(names, tool.Name)
	}
	byWire, err := FactoryDroidToolNamesByWire(names, factoryDroidGeminiName)
	if err != nil {
		return nil, nil, err
	}
	if len(ctx.Tools) > 0 {
		declarations := make([]json.RawMessage, 0, len(ctx.Tools))
		for _, tool := range ctx.Tools {
			declarations = append(declarations, jsjson.Object(
				jsjson.Field{Name: "name", Value: jsjson.Marshal(factoryDroidGeminiName(tool.Name))},
				jsjson.Field{Name: "description", Value: jsjson.Marshal(tool.Description)},
				jsjson.Field{Name: "parameters", Value: codexwire.FactoryDroidToolSchema(gjson.ParseBytes(tool.Parameters))},
			))
		}
		fields = append(fields, jsjson.Field{Name: "tools", Value: jsjson.Marshal([]json.RawMessage{
			jsjson.Object(jsjson.Field{Name: "functionDeclarations", Value: jsjson.Marshal(declarations)}),
		})})
	}
	return jsjson.Object(fields...), byWire, nil
}

func factoryDroidGeminiName(name string) string { return FactoryDroidSanitizeToolName(name, 64) }

// factoryDroidGeminiThinkingLevel maps an effort to Gemini's thinkingLevel:
// low/minimal LOW, medium MEDIUM where the route has it (else HIGH), HIGH
// otherwise.
func factoryDroidGeminiThinkingLevel(effort string, levelMedium bool) string {
	switch effort {
	case "low", "minimal":
		return "LOW"
	case "medium":
		if levelMedium {
			return "MEDIUM"
		}
	}
	return "HIGH"
}

// factoryDroidAdoptReplay is adoptFactoryReplay: only signed, Google-origin
// thinking and Google-origin call signatures survive; everything else is
// dropped. Call names take their wire form before transformMessages, and only
// Google-origin turns are stamped as the target.
func factoryDroidAdoptReplay(message googlewire.Message, model *googlewire.Model) googlewire.Message {
	if message.Role != googlewire.RoleAssistant {
		return message
	}
	googleOrigin := factoryDroidGoogleAPIs[message.API]
	sameModel := googleOrigin && message.Provider == model.Provider && message.Model == model.ID
	content := make([]googlewire.Block, 0, len(message.Content))
	for _, block := range message.Content {
		switch block.Type {
		case googlewire.BlockThinking:
			signature := strings.TrimSpace(block.Signature)
			if googleOrigin && strings.TrimSpace(block.Text) != "" && signature != "" && signature != googlewire.SkipThoughtSignature {
				content = append(content, block)
			}
		case googlewire.BlockToolCall:
			call := block
			call.Name = factoryDroidGeminiName(call.Name)
			if !googleOrigin {
				call.Signature = ""
			}
			content = append(content, call)
		case googlewire.BlockText:
			if !sameModel && block.Signature != "" {
				block.Signature = ""
			}
			content = append(content, block)
		default:
			content = append(content, block)
		}
	}
	message.Content = content
	if googleOrigin {
		message.API, message.Provider, message.Model = model.API, model.Provider, model.ID
	}
	return message
}

// factoryDroidPart is one Gemini part; responseOrdinal is set on a function
// response paired with a recorded call.
type factoryDroidPart struct {
	raw              json.RawMessage
	functionResponse bool
	functionCall     bool
	signature        string
	ordinal          int
	paired           bool
}

type factoryDroidContent struct {
	role  string
	parts []*factoryDroidPart
}

// factoryDroidGeminiContents is toGeminiContents: the proxy's Gemini history
// contract, and the system prompt joined with single newlines.
func factoryDroidGeminiContents(model *googlewire.Model, ctx googlewire.Context) (json.RawMessage, string) {
	adopted := make([]googlewire.Message, 0, len(ctx.Messages))
	for _, message := range ctx.Messages {
		adopted = append(adopted, factoryDroidAdoptReplay(message, model))
	}
	var contents []*factoryDroidContent
	ordinals := map[string]int{}
	next := 0
	text := func(value, signature string) *factoryDroidPart {
		fields := []jsjson.Field{{Name: "text", Value: jsjson.Marshal(value)}}
		if signature != "" {
			fields = append(fields, jsjson.Field{Name: "thoughtSignature", Value: jsjson.Marshal(signature)})
		}
		return &factoryDroidPart{raw: jsjson.Object(fields...)}
	}
	inline := func(block googlewire.Block) json.RawMessage {
		return jsjson.Object(jsjson.Field{Name: "inlineData", Value: jsjson.Object(
			jsjson.Field{Name: "mimeType", Value: jsjson.Marshal(block.MimeType)},
			jsjson.Field{Name: "data", Value: jsjson.Marshal(block.Data)},
		)})
	}
	for _, message := range googlewire.TransformMessages(adopted, model) {
		switch message.Role {
		case googlewire.RoleUser, googlewire.RoleDeveloper:
			var parts []*factoryDroidPart
			if message.IsString {
				if message.Text != "" {
					parts = append(parts, text(message.Text, ""))
				}
			} else {
				for _, block := range message.Content {
					if block.Type == googlewire.BlockText && block.Text != "" {
						parts = append(parts, text(block.Text, ""))
					} else if block.Type == googlewire.BlockImage {
						parts = append(parts, &factoryDroidPart{raw: inline(block)})
					}
				}
			}
			if len(parts) > 0 {
				contents = append(contents, &factoryDroidContent{role: "user", parts: parts})
			}
		case googlewire.RoleAssistant:
			var parts []*factoryDroidPart
			for _, block := range message.Content {
				switch {
				case block.Type == googlewire.BlockText && block.Text != "":
					parts = append(parts, text(block.Text, block.Signature))
				case block.Type == googlewire.BlockThinking && block.Signature != "":
					parts = append(parts, text(block.Text, block.Signature))
				case block.Type == googlewire.BlockToolCall:
					ordinals[block.ID] = next
					next++
					args := block.Arguments
					if len(args) == 0 {
						args = json.RawMessage("{}")
					}
					parts = append(parts, &factoryDroidPart{functionCall: true, signature: block.Signature, raw: jsjson.Object(
						jsjson.Field{Name: "name", Value: jsjson.Marshal(block.Name)},
						jsjson.Field{Name: "args", Value: jsjson.Normalize(args)},
					)})
				}
			}
			if len(parts) > 0 {
				contents = append(contents, &factoryDroidContent{role: "model", parts: parts})
			}
		case googlewire.RoleToolResult:
			var texts []string
			var binary []json.RawMessage
			for _, block := range message.Content {
				if block.Type == googlewire.BlockText && block.Text != "" {
					texts = append(texts, block.Text)
				} else if block.Type == googlewire.BlockImage {
					binary = append(binary, inline(block))
				}
			}
			result := "Tool execution succeeded."
			switch {
			case len(texts) > 0:
				result = strings.Join(texts, "\n")
			case len(binary) > 0:
				result = fmt.Sprintf("Binary content provided (%d item(s)).", len(binary))
			}
			response := []jsjson.Field{
				{Name: "name", Value: jsjson.Marshal(factoryDroidGeminiName(message.ToolName))},
				{Name: "response", Value: jsjson.Object(jsjson.Field{Name: "result", Value: jsjson.Marshal(result)})},
			}
			if len(binary) > 0 {
				response = append(response, jsjson.Field{Name: "parts", Value: jsjson.Marshal(binary)})
			}
			part := &factoryDroidPart{functionResponse: true, raw: jsjson.Object(jsjson.Field{Name: "functionResponse", Value: jsjson.Object(response...)})}
			part.ordinal, part.paired = ordinals[message.ToolCallID]
			if last := len(contents) - 1; last >= 0 && contents[last].role == "user" && factoryDroidAllResponses(contents[last].parts) {
				contents[last].parts = append(contents[last].parts, part)
			} else {
				contents = append(contents, &factoryDroidContent{role: "user", parts: []*factoryDroidPart{part}})
			}
		}
	}
	// callOrder.apply: matched responses sort back into call order within
	// the slots they occupy.
	for _, content := range contents {
		var slots []int
		var known []*factoryDroidPart
		for index, part := range content.parts {
			if part.paired {
				slots = append(slots, index)
				known = append(known, part)
			}
		}
		if len(known) < 2 {
			continue
		}
		sort.SliceStable(known, func(i, j int) bool { return known[i].ordinal < known[j].ordinal })
		for index, slot := range slots {
			content.parts[slot] = known[index]
		}
	}
	// Calls missing a signature get the validator-skip sentinel, from the
	// latest user turn with a non-response part on.
	lastUserText := 0
	for index := len(contents) - 1; index >= 0; index-- {
		if contents[index].role == "user" && !factoryDroidAllResponses(contents[index].parts) {
			lastUserText = index
			break
		}
	}
	for index := lastUserText; index < len(contents); index++ {
		if contents[index].role != "model" {
			continue
		}
		for _, part := range contents[index].parts {
			if part.functionCall && strings.TrimSpace(part.signature) == "" {
				part.signature = googlewire.SkipThoughtSignature
			}
		}
	}
	encoded := make([]json.RawMessage, 0, len(contents))
	for _, content := range contents {
		parts := make([]json.RawMessage, 0, len(content.parts))
		for _, part := range content.parts {
			if !part.functionCall {
				parts = append(parts, part.raw)
				continue
			}
			fields := []jsjson.Field{{Name: "functionCall", Value: part.raw}}
			if part.signature != "" {
				fields = append(fields, jsjson.Field{Name: "thoughtSignature", Value: jsjson.Marshal(part.signature)})
			}
			parts = append(parts, jsjson.Object(fields...))
		}
		encoded = append(encoded, jsjson.Object(
			jsjson.Field{Name: "role", Value: jsjson.Marshal(content.role)},
			jsjson.Field{Name: "parts", Value: jsjson.Marshal(parts)},
		))
	}
	return jsjson.Marshal(encoded), strings.Join(ctx.SystemPrompts, "\n")
}

// factoryDroidAllResponses reports a user turn made only of function
// responses (every() over an empty turn is true).
func factoryDroidAllResponses(parts []*factoryDroidPart) bool {
	for _, part := range parts {
		if !part.functionResponse {
			return false
		}
	}
	return true
}
