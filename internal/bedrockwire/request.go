package bedrockwire

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopidialect"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitransform"
	"github.com/tidwall/gjson"
)

const (
	// Provider is the provider id in NeoPi's error messages.
	Provider = "amazon-bedrock"
	// minOutputTokens and outputCapWhenUnknown are stream.ts MIN_OUTPUT_TOKENS
	// and OUTPUT_CAP_WHEN_UNKNOWN.
	minOutputTokens      = 1024
	outputCapWhenUnknown = 64_000
	// toolCallNameMaxLength is the catalog's tool-call-name-max-length for
	// bedrock-converse-stream (providers/tool-call-names.kdl).
	toolCallNameMaxLength = 64
	// thinkingBindingControlsBeta is THINKING_BINDING_CONTROLS_BETA.
	thinkingBindingControlsBeta = "thinking-binding-controls-2026-08-01"
	// noToolsSentinelName is NO_TOOLS_SENTINEL_NAME.
	noToolsSentinelName = "__no_tools__"
)

// claudeThinkingBudgets is stream.ts BEDROCK_CLAUDE_THINKING, the budgets
// mapOptionsForApi sizes max tokens with.
var claudeThinkingBudgets = map[string]int{"minimal": 1024, "low": 2048, "medium": 8192, "high": 16384, "xhigh": 16384, "max": 32768}

// defaultThinkingBudgets is DEFAULT_BEDROCK_THINKING_BUDGETS, the budgets
// the provider sends when no override applies.
var defaultThinkingBudgets = map[string]int{"minimal": 1024, "low": 2048, "medium": 8192, "high": 16384, "xhigh": 32768, "max": 32768}

// Model is the part of a bedrock-converse-stream row the request reads.
type Model struct {
	ID        string
	Reasoning bool
	// MaxTokens is model.maxTokens, 0 when unknown.
	MaxTokens int
	// Mode, Efforts, EffortMap, SupportsDisplay, PrefixBinding,
	// RequiresEffort and SuppressWhenOff are model.thinking's.
	Mode                            string
	Efforts                         []string
	EffortMap                       map[string]string
	SupportsDisplay                 bool
	PrefixBinding                   bool
	RequiresEffort                  bool
	SuppressWhenOff                 bool
	PromptCacheMode                 string
	PromptCacheMaximumCheckpoints   int
	SupportsForcedToolChoice        bool
	SupportsThinkingBindingControls bool
	DisabledThinking                string
	// SupportsSamplingParams false strips temperature and top_p; nil keeps them.
	SupportsSamplingParams          *bool
	RequiresToolResultImageHoisting bool
}

// ToolChoice is the stream's toolChoice after mapAnthropicToolChoice: Mode
// "auto", "any", "none" or "tool" (with Name), "" when absent.
type ToolChoice struct {
	Mode, Name string
}

// ParseToolChoice is the gateway's normalization of a chat body's
// tool_choice followed by mapAnthropicToolChoice: "required" is any, and a
// named choice without a name is dropped.
func ParseToolChoice(value gjson.Result) ToolChoice {
	switch {
	case value.Type == gjson.String:
		switch value.Str {
		case "auto", "none":
			return ToolChoice{Mode: value.Str}
		case "required":
			return ToolChoice{Mode: "any"}
		}
	case value.IsObject():
		name := ""
		if function := value.Get("function"); function.Exists() && function.Type != gjson.Null && function.Type != gjson.False &&
			!(function.Type == gjson.Number && function.Num == 0) && !(function.Type == gjson.String && function.Str == "") {
			name = function.Get("name").String()
		} else if value.Get("type").String() == "tool" && value.Get("name").Type == gjson.String {
			name = value.Get("name").Str
		}

		if name != "" {
			return ToolChoice{Mode: "tool", Name: name}
		}
	}
	return ToolChoice{}
}

// effortOrder is THINKING_EFFORTS.
var effortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// Effort is the gateway's normalizeGatewayThinking (a client effort the
// ladder lacks, and the effort map does not name, fitted to the nearest
// tier) followed by mapOptionsForApi's normalizeMandatoryReasoningOptions (a
// model that always thinks runs an off or effort-less turn at its floor).
// off is reasoning_effort "none"; the results are Options.Reasoning and
// Options.ReasoningOff.
func Effort(model *Model, requested string, off bool) (string, bool) {
	var efforts []string
	if model.Reasoning {
		efforts = model.Efforts
	}
	if requested != "" && len(efforts) > 0 && !slices.Contains(efforts, requested) {
		if _, mapped := model.EffortMap[requested]; !mapped {
			if clamped := codexwire.ClampEffort(efforts, requested); clamped != "" {
				requested = clamped
			}
		}
	}
	if !model.Reasoning || !model.RequiresEffort || model.SuppressWhenOff || (requested != "" && !off) {
		return requested, off
	}
	for _, effort := range effortOrder {
		if slices.Contains(efforts, effort) {
			return effort, false
		}
	}
	return requested, off
}

// Options are the streamSimple options a Converse request takes.
type Options struct {
	// Reasoning is the effort after the gateway's ladder fit and the
	// mandatory-reasoning floor, "" for none; ReasoningOff is
	// disableReasoning or forceReasoningOff.
	Reasoning    string
	ReasoningOff bool
	// MaxTokens is the client's output limit, nil when absent.
	MaxTokens *int64
	// Temperature and TopP are raw JSON values, nil when absent.
	Temperature json.RawMessage
	TopP        json.RawMessage
	ToolChoice  ToolChoice
}

// ValidationError is a request NeoPi rejects before sending it.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// Request is one Converse request: its body, and what the stream mapping and
// the prefix-binding retry need from it.
type Request struct {
	messages []wireMessage
	fields   []jsjson.Field
	// SentinelInjected marks a request carrying the __no_tools__ placeholder.
	SentinelInjected bool
	// PrefixBinding marks a request with thinking binding controls, which
	// may be retried once without its reasoning blocks.
	PrefixBinding bool
}

// Body is the request body; withoutReasoning drops assistant reasoning
// blocks (and turns left empty), as dropBedrockReasoningContent does.
func (r *Request) Body(withoutReasoning bool) json.RawMessage {
	messages := r.messages
	if withoutReasoning {
		messages = make([]wireMessage, 0, len(r.messages))
		for _, msg := range r.messages {
			if msg.role != "assistant" {
				messages = append(messages, msg)
				continue
			}
			var kept []json.RawMessage
			for i, block := range msg.content {
				if !msg.reasoning[i] {
					kept = append(kept, block)
				}
			}
			if len(kept) > 0 {
				messages = append(messages, wireMessage{role: msg.role, content: kept, reasoning: make([]bool, len(kept))})
			}
		}
	}
	items := make([]json.RawMessage, len(messages))
	for i, msg := range messages {
		items[i] = jsjson.Object(
			jsjson.Field{Name: "role", Value: jsjson.Marshal(msg.role)},
			jsjson.Field{Name: "content", Value: jsonArray(msg.content)},
		)
	}
	fields := append([]jsjson.Field{{Name: "messages", Value: jsonArray(items)}}, r.fields...)
	return jsjson.Object(fields...)
}

// prefixBindingError, invalidThinkingSignature and compactionMarker are
// isThinkingPrefixBindingError's patterns.
var (
	compactionMarker         = regexp.MustCompile(`(?i)\bcompaction_[a-z_]+\b`)
	invalidThinkingSignature = regexp.MustCompile("(?i)invalid\\s+`?signature`?\\s+in\\s+`?thinking`?(?:\\s+block)?")
	prefixBindingError       = regexp.MustCompile(`(?i)(?:bound to a different conversation|block_binding\.prefix_mismatch_behavior|prefix_mismatch_behavior)`)
)

// IsThinkingPrefixBindingError is isThinkingPrefixBindingError.
func IsThinkingPrefixBindingError(message string) bool {
	return !compactionMarker.MatchString(message) && invalidThinkingSignature.MatchString(message) && prefixBindingError.MatchString(message)
}

// mapped is mapOptionsForApi's bedrock-converse-stream options.
type mapped struct {
	reasoning   string
	maxTokens   *int64
	budgets     map[string]int
	temperature json.RawMessage
	topP        json.RawMessage
}

// requireSupportedEffort is requireSupportedEffort.
func requireSupportedEffort(model *Model, effort string) error {
	if !model.Reasoning {
		return &ValidationError{Message: fmt.Sprintf("Model %s/%s does not support thinking", Provider, model.ID)}
	}
	if !slices.Contains(model.Efforts, effort) {
		return &ValidationError{Message: fmt.Sprintf("Thinking effort %s is not supported by %s/%s. Supported efforts: %s",
			effort, Provider, model.ID, strings.Join(model.Efforts, ", "))}
	}
	return nil
}

// mapOptions is the sampling filter and mapOptionsForApi's
// bedrock-converse-stream case: the caller's limit (else the model's),
// reasoning folded off, and max tokens sized for the thinking budget.
func mapOptions(model *Model, opts Options) (mapped, error) {
	out := mapped{temperature: opts.Temperature, topP: opts.TopP}
	if model.SupportsSamplingParams != nil && !*model.SupportsSamplingParams {
		out.temperature, out.topP = nil, nil
	}
	out.maxTokens = opts.MaxTokens
	if out.maxTokens == nil && model.MaxTokens > 0 {
		limit := int64(model.MaxTokens)
		out.maxTokens = &limit
	}
	if !opts.ReasoningOff {
		out.reasoning = opts.Reasoning
	}
	modelCap := func(value int64) int64 {
		if model.MaxTokens > 0 {
			return min(value, int64(model.MaxTokens))
		}
		return value
	}
	switch model.Mode {
	case "anthropic-adaptive":
		// Adaptive Claude shares max_tokens between thinking and the answer:
		// a capped request gets the effort's budget on top.
		budget := 0
		if out.reasoning != "" {
			budget = claudeThinkingBudgets[out.reasoning]
		}
		if model.Reasoning && out.maxTokens != nil && budget > 0 {
			limit := modelCap(*out.maxTokens + int64(budget))
			out.maxTokens = &limit
		}
	case "effort":
	default:
		if out.reasoning == "" || !model.Reasoning {
			return out, nil
		}
		if err := requireSupportedEffort(model, out.reasoning); err != nil {
			return out, err
		}
		budget := int64(claudeThinkingBudgets[out.reasoning])
		maxTokens := int64(outputCapWhenUnknown)
		if out.maxTokens != nil {
			maxTokens = *out.maxTokens
		}
		if maxTokens <= budget {
			if desired := modelCap(budget + minOutputTokens); desired > maxTokens {
				maxTokens = desired
			}
		}
		if maxTokens <= budget {
			out.budgets = map[string]int{out.reasoning: int(max(0, maxTokens-minOutputTokens))}
		}
		out.maxTokens = &maxTokens
	}
	return out, nil
}

// BuildRequest is streamBedrock's request shaping after the stream options
// are mapped: messages with cache checkpoints, the tool plan, the thinking
// fields, forced-tool handling and thinking binding controls.
func BuildRequest(model *Model, ctx *cursorwire.Context, opts Options) (*Request, error) {
	options, err := mapOptions(model, opts)
	if err != nil {
		return nil, err
	}
	policy := promptCachePolicy(model)
	messages, err := convertMessages(ctx, model, &policy)
	if err != nil {
		return nil, err
	}
	toolConfig, sentinel := planToolConfig(ctx.Tools, opts.ToolChoice, messages)
	fields, err := additionalModelRequestFields(model, options)
	if err != nil {
		return nil, err
	}
	thinkingDisabled := false
	if thinking, ok := fields.get("thinking"); ok {
		thinkingDisabled = gjson.GetBytes(thinking, "type").String() == "disabled"
	}
	binding := !thinkingDisabled && model.PrefixBinding && model.SupportsThinkingBindingControls
	// Some models reject forced tool use outright: keep the tools under auto
	// and leave thinking intact. Elsewhere Bedrock rejects thinking with a
	// forced choice: prefix-bound lines think regardless, so downgrade them;
	// others drop the thinking fields.
	if toolConfig != nil && (toolConfig.choice.Mode == "any" || toolConfig.choice.Mode == "tool") {
		if !model.SupportsForcedToolChoice {
			toolConfig.choice = ToolChoice{Mode: "auto"}
		} else if !thinkingDisabled {
			if model.PrefixBinding {
				toolConfig.choice = ToolChoice{Mode: "auto"}
			} else {
				fields = nil
			}
		}
	}
	if binding {
		fields = applyThinkingBinding(fields)
	}

	request := &Request{messages: messages, SentinelInjected: sentinel, PrefixBinding: binding}
	if system := buildSystemPrompt(ctx.SystemPrompt, &policy); system != nil {
		request.fields = append(request.fields, jsjson.Field{Name: "system", Value: system})
	}
	var inference []jsjson.Field
	if options.maxTokens != nil {
		inference = append(inference, jsjson.Field{Name: "maxTokens", Value: jsjson.Marshal(*options.maxTokens)})
	}
	if len(options.temperature) > 0 {
		inference = append(inference, jsjson.Field{Name: "temperature", Value: jsjson.Normalize(options.temperature)})
	}
	if len(options.topP) > 0 {
		inference = append(inference, jsjson.Field{Name: "topP", Value: jsjson.Normalize(options.topP)})
	}
	request.fields = append(request.fields, jsjson.Field{Name: "inferenceConfig", Value: jsjson.Object(inference...)})
	if toolConfig != nil {
		request.fields = append(request.fields, jsjson.Field{Name: "toolConfig", Value: toolConfig.encode()})
	}
	if fields != nil {
		request.fields = append(request.fields, jsjson.Field{Name: "additionalModelRequestFields", Value: fields.encode()})
	}
	if binding {
		request.fields = append(request.fields, jsjson.Field{Name: "additionalModelResponseFieldPaths", Value: json.RawMessage(`["/input_transformations"]`)})
	}
	return request, nil
}

// object is a JSON object under construction, in JavaScript key order: a
// key set again keeps its first position.
type object []jsjson.Field

func (o object) get(name string) (json.RawMessage, bool) {
	for _, field := range o {
		if field.Name == name {
			return field.Value, true
		}
	}
	return nil, false
}

func (o object) set(name string, value json.RawMessage) object {
	for i, field := range o {
		if field.Name == name {
			o[i].Value = value
			return o
		}
	}
	return append(o, jsjson.Field{Name: name, Value: value})
}

func (o object) encode() json.RawMessage { return jsjson.Object(o...) }

// additionalModelRequestFields is buildAdditionalModelRequestFields.
func additionalModelRequestFields(model *Model, options mapped) (object, error) {
	if !model.Reasoning {
		return nil, nil
	}
	reasoning := options.reasoning
	if reasoning == "" {
		if model.DisabledThinking == "disabled" {
			return object{{Name: "thinking", Value: json.RawMessage(`{"type":"disabled"}`)},
				{Name: "output_config", Value: json.RawMessage(`{"effort":"low"}`)}}, nil
		}
		return nil, nil
	}
	if err := requireSupportedEffort(model, reasoning); err != nil {
		return nil, err
	}
	mappedEffort := reasoning
	if value, ok := model.EffortMap[reasoning]; ok {
		mappedEffort = value
	}
	switch model.Mode {
	case "anthropic-adaptive":
		// mapEffortToAnthropicAdaptiveEffort: the wire has no minimal tier.
		if mappedEffort == "minimal" {
			mappedEffort = "low"
		}
		thinking := object{{Name: "type", Value: jsjson.Marshal("adaptive")}}
		if model.SupportsDisplay {
			thinking = thinking.set("display", jsjson.Marshal("summarized"))
		}
		return object{{Name: "thinking", Value: thinking.encode()},
			{Name: "output_config", Value: jsjson.Object(jsjson.Field{Name: "effort", Value: jsjson.Marshal(mappedEffort)})}}, nil
	case "effort":
		return object{{Name: "reasoning", Value: jsjson.Object(jsjson.Field{Name: "effort", Value: jsjson.Marshal(mappedEffort)})}}, nil
	}
	budget, ok := options.budgets[reasoning]
	if !ok {
		budget = defaultThinkingBudgets[reasoning]
	}
	return object{{Name: "thinking", Value: jsjson.Object(
		jsjson.Field{Name: "type", Value: jsjson.Marshal("enabled")},
		jsjson.Field{Name: "budget_tokens", Value: jsjson.Marshal(budget)},
		jsjson.Field{Name: "display", Value: jsjson.Marshal("summarized")},
	)}}, nil
}

// applyThinkingBinding is applyBedrockThinkingBinding with NeoPi's default
// drop_block: the thinking block (adaptive when absent) bound to its prefix,
// and the binding-controls beta added once.
func applyThinkingBinding(fields object) object {
	result := slices.Clone(fields)
	thinking := object{{Name: "type", Value: jsjson.Marshal("adaptive")}}
	if raw, ok := result.get("thinking"); ok && gjson.ParseBytes(raw).IsObject() {
		thinking = nil
		gjson.ParseBytes(raw).ForEach(func(key, value gjson.Result) bool {
			thinking = thinking.set(key.String(), json.RawMessage(value.Raw))
			return true
		})
	}
	thinking = thinking.set("block_binding", json.RawMessage(`{"prefix_mismatch_behavior":"drop_block"}`))
	result = result.set("thinking", thinking.encode())
	var betas []string
	if raw, ok := result.get("anthropic_beta"); ok {
		for _, beta := range gjson.ParseBytes(raw).Array() {
			if beta.Type == gjson.String {
				betas = append(betas, beta.Str)
			}
		}
	}
	if !slices.Contains(betas, thinkingBindingControlsBeta) {
		betas = append(betas, thinkingBindingControlsBeta)
	}
	return result.set("anthropic_beta", jsjson.Marshal(betas))
}

// cachePolicy is BedrockPromptCachePolicy at NeoPi's default short
// retention: explicit-cache rows take up to two checkpoints, no TTL.
type cachePolicy struct{ remaining int }

// promptCachePolicy is resolvePromptCachePolicy.
func promptCachePolicy(model *Model) cachePolicy {
	if model.PromptCacheMode != "explicit" || model.PromptCacheMaximumCheckpoints <= 0 {
		return cachePolicy{}
	}
	return cachePolicy{remaining: min(model.PromptCacheMaximumCheckpoints, 2)}
}

// take is takeCachePoint.
func (p *cachePolicy) take() json.RawMessage {
	if p.remaining <= 0 {
		return nil
	}
	p.remaining--
	return json.RawMessage(`{"cachePoint":{"type":"default"}}`)
}

// buildSystemPrompt is buildSystemPrompt: one text block per non-blank
// prompt, then a checkpoint when the policy has one left.
func buildSystemPrompt(systemPrompt []string, policy *cachePolicy) json.RawMessage {
	prompts := cursorwire.NormalizeSystemPrompts(systemPrompt)
	if len(prompts) == 0 {
		return nil
	}
	blocks := make([]json.RawMessage, 0, len(prompts)+1)
	for _, prompt := range prompts {
		blocks = append(blocks, textBlock(prompt))
	}
	if point := policy.take(); point != nil {
		blocks = append(blocks, point)
	}
	return jsonArray(blocks)
}

// wireMessage is a Converse message; reasoning marks its reasoningContent
// blocks.
type wireMessage struct {
	role      string
	content   []json.RawMessage
	reasoning []bool
	toolBlock bool
}

func (m *wireMessage) push(block json.RawMessage, reasoning bool) {
	m.content = append(m.content, block)
	m.reasoning = append(m.reasoning, reasoning)
}

func textBlock(text string) json.RawMessage {
	return jsjson.Object(jsjson.Field{Name: "text", Value: jsjson.Marshal(text)})
}

// toWellFormed is String.prototype.toWellFormed over Go text.
func toWellFormed(text string) string { return strings.ToValidUTF8(text, "\uFFFD") }

// imageBlock is createImageBlock in its wrapper.
func imageBlock(mimeType, data string) (json.RawMessage, error) {
	var format string
	switch mimeType {
	case "image/jpeg", "image/jpg":
		format = "jpeg"
	case "image/png":
		format = "png"
	case "image/gif":
		format = "gif"
	case "image/webp":
		format = "webp"
	default:
		return nil, &ValidationError{Message: "Unknown image type: " + mimeType}
	}
	return jsjson.Object(jsjson.Field{Name: "image", Value: jsjson.Object(
		jsjson.Field{Name: "source", Value: jsjson.Object(jsjson.Field{Name: "bytes", Value: jsjson.Marshal(data)})},
		jsjson.Field{Name: "format", Value: jsjson.Marshal(format)},
	)}), nil
}

// convertMessages is convertMessages: the transformed history (ids through
// normalizeToolCallId, foreign thinking demoted), blank user content and
// empty assistant turns skipped, consecutive tool results in one user turn
// with hoisted images after them, then a checkpoint on a final user turn.
func convertMessages(ctx *cursorwire.Context, model *Model, policy *cachePolicy) ([]wireMessage, error) {
	history := neopitransform.Messages(ctx.Messages, neopitransform.Target{ModelID: model.ID,
		MaxToolCallNameLength: toolCallNameMaxLength, NormalizeToolCallID: cursorwire.NormalizeToolCallID})
	var result []wireMessage
	for i := 0; i < len(history); i++ {
		msg := history[i]
		switch msg.Role {
		case "developer", "user":
			out := wireMessage{role: "user"}
			if msg.Plain {
				if jsTrim(msg.Text) == "" {
					continue
				}
				out.push(textBlock(toWellFormed(msg.Text)), false)
			} else {
				for _, part := range msg.Parts {
					if part.Image {
						block, err := imageBlock(part.MimeType, part.Data)
						if err != nil {
							return nil, err
						}
						out.push(block, false)
						continue
					}
					text := toWellFormed(part.Text)
					if jsTrim(text) == "" {
						continue
					}
					out.push(textBlock(text), false)
				}
			}
			if len(out.content) == 0 {
				continue
			}
			result = append(result, out)
		case "assistant":
			if len(msg.Blocks) == 0 {
				continue
			}
			out := wireMessage{role: "assistant"}
			for _, block := range msg.Blocks {
				switch block.Type {
				case "text":
					if jsTrim(block.Text) == "" {
						continue
					}
					out.push(textBlock(toWellFormed(block.Text)), false)
				case "toolCall":
					fields := []jsjson.Field{{Name: "toolUseId", Value: jsjson.Marshal(cursorwire.NormalizeToolCallID(block.ID))},
						{Name: "name", Value: jsjson.Marshal(block.Name)}}
					if block.Arguments != nil {
						fields = append(fields, jsjson.Field{Name: "input", Value: json.RawMessage(cursorwire.StringifyJSON(block.Arguments))})
					}
					out.push(jsjson.Object(jsjson.Field{Name: "toolUse", Value: jsjson.Object(fields...)}), false)
					out.toolBlock = true
				case "thinking":
					// Hidden thinking has empty text but a load-bearing signature.
					if jsTrim(block.Thinking) == "" && block.ThinkingSignature == "" {
						continue
					}
					if block.ThinkingSignature != "" {
						out.push(jsjson.Object(jsjson.Field{Name: "reasoningContent", Value: jsjson.Object(
							jsjson.Field{Name: "reasoningText", Value: jsjson.Object(
								jsjson.Field{Name: "text", Value: jsjson.Marshal(toWellFormed(block.Thinking))},
								jsjson.Field{Name: "signature", Value: jsjson.Marshal(block.ThinkingSignature)},
							)})}), true)
						continue
					}
					// Unsigned reasoning is not replayed as reasoningContent (Nova
					// rejects it); it survives as demoted text.
					out.push(textBlock(neopidialect.RenderDemotedThinking(model.ID, block.Thinking)), false)
				default:
					return nil, &ValidationError{Message: "Unknown assistant content type"}
				}
			}
			if len(out.content) == 0 {
				continue
			}
			result = append(result, out)
		case "toolResult":
			// Bedrock requires every tool result of a turn in one user message.
			out := wireMessage{role: "user", toolBlock: true}
			var hoisted []json.RawMessage
			for ; i < len(history) && history[i].Role == "toolResult"; i++ {
				block, err := toolResultBlock(history[i], model, &hoisted)
				if err != nil {
					return nil, err
				}
				out.push(block, false)
			}
			i--
			for _, image := range hoisted {
				out.push(image, false)
			}
			result = append(result, out)
		default:
			return nil, &ValidationError{Message: "Unknown message role"}
		}
	}
	// The final user checkpoint comes first; the system prompt takes what is left.
	if n := len(result); n > 0 && result[n-1].role == "user" {
		if point := policy.take(); point != nil {
			result[n-1].push(point, false)
		}
	}
	return result, nil
}

// toolResultBlock is buildToolResultBlock: error results (and rows that
// require it) move their images out after the turn's results, leaving a
// placeholder.
func toolResultBlock(msg cursorwire.Message, model *Model, hoisted *[]json.RawMessage) (json.RawMessage, error) {
	hoist := msg.IsError || model.RequiresToolResultImageHoisting
	content := make([]json.RawMessage, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		if !part.Image {
			content = append(content, textBlock(toWellFormed(part.Text)))
			continue
		}
		image, err := imageBlock(part.MimeType, part.Data)
		if err != nil {
			return nil, err
		}
		if hoist {
			content = append(content, textBlock("(see attached image)"))
			*hoisted = append(*hoisted, image)
		} else {
			content = append(content, image)
		}
	}
	status := "success"
	if msg.IsError {
		status = "error"
	}
	return jsjson.Object(jsjson.Field{Name: "toolResult", Value: jsjson.Object(
		jsjson.Field{Name: "toolUseId", Value: jsjson.Marshal(cursorwire.NormalizeToolCallID(msg.ToolCallID))},
		jsjson.Field{Name: "content", Value: jsonArray(content)},
		jsjson.Field{Name: "status", Value: jsjson.Marshal(status)},
	)}), nil
}

// toolConfig is WireToolConfig.
type toolConfig struct {
	tools  []json.RawMessage
	choice ToolChoice
}

func (c *toolConfig) encode() json.RawMessage {
	var choice json.RawMessage
	switch c.choice.Mode {
	case "auto":
		choice = json.RawMessage(`{"auto":{}}`)
	case "any":
		choice = json.RawMessage(`{"any":{}}`)
	case "tool":
		choice = jsjson.Object(jsjson.Field{Name: "tool", Value: jsjson.Object(jsjson.Field{Name: "name", Value: jsjson.Marshal(c.choice.Name)})})
	}
	return jsjson.Object(jsjson.Field{Name: "tools", Value: jsonArray(c.tools)}, jsjson.Field{Name: "toolChoice", Value: choice})
}

// noToolsSentinel is NO_TOOLS_SENTINEL.
var noToolsSentinel = json.RawMessage(`{"toolSpec":{"name":"` + noToolsSentinelName + `","description":"Placeholder required by Bedrock validation. Do not call; answer with text.","inputSchema":{"json":{"type":"object","properties":{}}}}}`)

// toolSpec is convertToolSpec: an empty description is omitted, which
// Bedrock permits where it rejects an empty one.
func toolSpec(tool cursorwire.Tool) json.RawMessage {
	var description json.RawMessage
	if tool.Description != "" {
		description = jsjson.Marshal(tool.Description)
	}
	schema := codexwire.ToolWireSchema(gjson.Parse(cursorwire.StringifyJSON(tool.Parameters)))
	return jsjson.Object(jsjson.Field{Name: "toolSpec", Value: jsjson.Object(
		jsjson.Field{Name: "name", Value: jsjson.Marshal(tool.Name)},
		jsjson.Field{Name: "description", Value: description},
		jsjson.Field{Name: "inputSchema", Value: jsjson.Object(jsjson.Field{Name: "json", Value: schema})},
	)})
}

// planToolConfig is planToolConfig: a none choice offers the tools without a
// choice only when history holds tool blocks, the placeholder when it holds
// them but no tool is active; otherwise active tools with the mapped choice.
func planToolConfig(tools []cursorwire.Tool, choice ToolChoice, messages []wireMessage) (*toolConfig, bool) {
	historyHasToolBlocks := slices.ContainsFunc(messages, func(m wireMessage) bool { return m.toolBlock })
	if choice.Mode == "none" {
		if !historyHasToolBlocks {
			return nil, false
		}
		if len(tools) == 0 {
			return &toolConfig{tools: []json.RawMessage{noToolsSentinel}, choice: ToolChoice{Mode: "auto"}}, true
		}
		choice = ToolChoice{}
	}
	if len(tools) == 0 {
		return nil, false
	}
	specs := make([]json.RawMessage, len(tools))
	for i, tool := range tools {
		specs[i] = toolSpec(tool)
	}
	return &toolConfig{tools: specs, choice: choice}, false
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

// isJSSpace is the WhiteSpace and LineTerminator set String.prototype.trim
// removes.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }
