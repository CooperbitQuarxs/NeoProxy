package cursorwire

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicatalog"
)

// Ports of providers/cursor.ts request building: buildGrpcRequestForWireMode
// and the history builders it uses.

// Model is the Model<"cursor-agent"> fields the Cursor transport reads.
type Model struct {
	ID, Name, Provider, BaseURL  string
	RequestModelID               string
	MaxMode                      *bool
	MaxModeRoutes                map[string]bool
	ModelRoutes                  map[string]neopicatalog.CursorRoute
	ModelParameters              []neopicatalog.ModelParameter
	EffortRouting                map[string]string
	RequiresToolSchemaProjection bool
}

// WireMode is CursorWireMode.
type WireMode string

const (
	WireNormalized WireMode = "normalized"
	WireDiscovered WireMode = "discovered"
)

// thinkingEfforts is THINKING_EFFORTS.
var thinkingEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// Blobs is a conversation's blob store keyed by hex blob id; requests of one
// conversation share it.
type Blobs struct {
	mu   sync.Mutex
	data map[string][]byte
}

// NewBlobs is an empty store.
func NewBlobs() *Blobs { return &Blobs{data: map[string][]byte{}} }

// Store is storeCursorBlob: the data under its SHA-256 id.
func (b *Blobs) Store(data []byte) []byte {
	sum := sha256.Sum256(data)
	b.Set(sum[:], data)
	return sum[:]
}

// Set stores data under id (setBlobArgs).
func (b *Blobs) Set(id, data []byte) {
	b.mu.Lock()
	b.data[hex.EncodeToString(id)] = data
	b.mu.Unlock()
}

// Get returns the data under id (getBlobArgs).
func (b *Blobs) Get(id []byte) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.data[hex.EncodeToString(id)]
	return data, ok
}

// RequestState is CursorRequestState.
type RequestState struct {
	ConversationID    string
	Blobs             *Blobs
	ConversationState *ConversationStateStructure
	Resume            bool
}

// BuiltRequest is CursorTransportRequest.
type BuiltRequest struct {
	Bytes             []byte
	ConversationState *ConversationStateStructure
	// FallbackWireModelID is the exact discovery id eligible for the
	// discovered-mode retry, "" when none.
	FallbackWireModelID string
}

// HistoryError is the ValidationError a history Cursor cannot continue raises.
type HistoryError struct{ Message string }

func (e *HistoryError) Error() string { return e.Message }

// jsTrim is String.prototype.trim.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
			return true
		}
		return r >= '\u2000' && r <= '\u200a'
	})
}

// NormalizeSystemPrompts is normalizeSystemPrompts with credential redaction
// off (NeoPi's default): blank prompts dropped.
func NormalizeSystemPrompts(prompts []string) []string {
	var out []string
	for _, prompt := range prompts {
		if jsTrim(prompt) != "" {
			out = append(out, prompt)
		}
	}
	return out
}

func jsonString(fields ...any) string {
	object := &JSONObject{}
	for i := 0; i+1 < len(fields); i += 2 {
		object.Set(fields[i].(string), fields[i+1])
	}
	return StringifyJSON(object)
}

func jsonObject(fields ...any) *JSONObject {
	object := &JSONObject{}
	for i := 0; i+1 < len(fields); i += 2 {
		object.Set(fields[i].(string), fields[i+1])
	}
	return object
}

// SystemPromptJSONs is buildCursorSystemPromptJsons.
func SystemPromptJSONs(systemPrompt []string) []string {
	prompts := NormalizeSystemPrompts(systemPrompt)
	if len(prompts) == 0 {
		return []string{jsonString("role", "system", "content", "You are a helpful assistant.")}
	}
	out := make([]string, len(prompts))
	for i, content := range prompts {
		out[i] = jsonString("role", "system", "content", content)
	}
	return out
}

// rulesEntryJSON is buildCursorRulesEntryJson.
func rulesEntryJSON(systemPrompt []string) string {
	prompts := NormalizeSystemPrompts(systemPrompt)
	if len(prompts) == 0 {
		return ""
	}
	return jsonString("role", "user", "content", "<rules>\n"+strings.Join(prompts, "\n\n")+"\n</rules>")
}

// RequestContextRules is buildCursorRequestContextRules.
func RequestContextRules(systemPrompt []string) []*CursorRule {
	prompts := NormalizeSystemPrompts(systemPrompt)
	out := make([]*CursorRule, len(prompts))
	for i, content := range prompts {
		out[i] = &CursorRule{FullPath: fmt.Sprintf("/omp/system-prompt/%d.mdc", i), Content: content, Source: 2,
			Type: &CursorRuleType{Type: &CursorRuleType_Global{Global: &CursorRuleTypeGlobal{}}}}
	}
	return out
}

// cursorNativeToolNames is CURSOR_NATIVE_TOOL_NAMES.
var cursorNativeToolNames = map[string]bool{"bash": true, "read": true, "write": true, "delete": true, "ls": true, "grep": true, "todo": true}

// MCPToolDefinitions is buildMcpToolDefinitions; wireSchema renders a tool's
// toolWireSchema (projected for Cursor when the model requires it).
func MCPToolDefinitions(tools []Tool, wireSchema func(Tool) any) []*McpToolDefinition {
	var advertised []Tool
	var writeTool *Tool
	for i, tool := range tools {
		if !cursorNativeToolNames[tool.Name] {
			advertised = append(advertised, tool)
		}
		if tool.Name == "write" && writeTool == nil {
			writeTool = &tools[i]
		}
	}
	if len(advertised) == 0 {
		return nil
	}
	if writeTool != nil {
		advertised = append(advertised, *writeTool)
	}
	out := make([]*McpToolDefinition, 0, len(advertised))
	for _, tool := range advertised {
		schema := wireSchema(tool)
		if _, isArray := schema.([]any); schema == nil || isArray {
			schema = jsonObject("type", "object", "properties", &JSONObject{}, "required", []any{})
		}
		out = append(out, &McpToolDefinition{Name: tool.Name, Description: tool.Description, ProviderIdentifier: "pi-agent",
			ToolName: tool.Name, InputSchema: EncodeJSONValue(schema)})
	}
	return out
}

// NormalizeToolCallID is normalizeToolCallId: characters outside
// [a-zA-Z0-9_-] become "_" per UTF-16 unit, capped at 64 units.
func NormalizeToolCallID(id string) string {
	units := utf16.Encode([]rune(id))
	out := make([]byte, 0, min(len(units), 64))
	for _, unit := range units {
		if len(out) == 64 {
			break
		}
		c := rune(unit)
		if c < 128 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			out = append(out, byte(c))
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

// userText is the trimmed text of user content (extractUserMessageText).
func userText(msg Message) string {
	if msg.Plain {
		return jsTrim(msg.Text)
	}
	return jsTrim(joinText(msg.Parts))
}

func joinText(parts []Part) string {
	var texts []string
	for _, part := range parts {
		if !part.Image {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func hasImages(msg Message) bool {
	if msg.Plain {
		return false
	}
	for _, part := range msg.Parts {
		if part.Image {
			return true
		}
	}
	return false
}

// toolResultText is toolResultToText.
func toolResultText(msg Message) string {
	texts := make([]string, len(msg.Parts))
	for i, part := range msg.Parts {
		if part.Image {
			texts[i] = "[" + part.MimeType + " image]"
		} else {
			texts[i] = part.Text
		}
	}
	return strings.Join(texts, "\n")
}

func isUserLike(role string) bool { return role == "user" || role == "developer" }

// createUserMessage is createCursorUserMessage; images draw fresh uuids.
func createUserMessage(msg Message, text, messageID string, newUUID func() string) *UserMessage {
	out := &UserMessage{Text: text, MessageId: messageID}
	var images []*SelectedImage
	if !msg.Plain {
		for _, part := range msg.Parts {
			if part.Image {
				images = append(images, &SelectedImage{Uuid: newUUID(), MimeType: part.MimeType,
					DataOrBlobId: &SelectedImage_Data{Data: NodeBase64(part.Data)}})
			}
		}
	}
	if len(images) > 0 {
		out.SelectedContext = &SelectedContext{SelectedImages: images}
	}
	return out
}

// userContentKey is cursorUserContentKey.
func userContentKey(msg Message) string {
	if msg.Plain {
		return jsTrim(msg.Text)
	}
	hash := sha256.New()
	for _, part := range msg.Parts {
		if part.Image {
			hash.Write([]byte("image"))
			hash.Write([]byte(part.MimeType))
			hash.Write([]byte(part.Data))
		} else {
			hash.Write([]byte("text"))
			hash.Write([]byte(part.Text))
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// rootPromptContent is buildCursorRootPromptContent.
func rootPromptContent(msg Message) []any {
	if msg.Plain {
		if text := jsTrim(msg.Text); text != "" {
			return []any{jsonObject("type", "text", "text", text)}
		}
		return nil
	}
	var parts []any
	for _, part := range msg.Parts {
		if part.Image {
			parts = append(parts, jsonObject("type", "image", "image", "data:"+part.MimeType+";base64,"+part.Data, "mediaType", part.MimeType))
		} else if text := jsTrim(part.Text); text != "" {
			parts = append(parts, jsonObject("type", "text", "text", text))
		}
	}
	return parts
}

// canReplayThinking is canReplayCursorThinking.
func canReplayThinking(msg Message, targetModelID string) bool {
	return targetModelID != "" && neopicatalog.Classify("cursor", targetModelID).Family == "k3" &&
		msg.API == "cursor-agent" && msg.Provider == "cursor" && msg.Model == targetModelID
}

type assistantStep struct {
	content []any
	callIDs []string
}

// assistantSteps is buildCursorAssistantSteps.
func assistantSteps(msg Message, targetModelID string) []assistantStep {
	var steps []assistantStep
	step := assistantStep{}
	replayThinking := canReplayThinking(msg, targetModelID)
	issued := map[string]bool{}
	for _, item := range msg.Blocks {
		if item.Type == "toolCall" {
			if issued[item.ID] {
				continue
			}
			issued[item.ID] = true
		}
		if item.Type != "toolCall" && len(step.callIDs) > 0 {
			steps = append(steps, step)
			step = assistantStep{}
		}
		switch item.Type {
		case "text":
			if item.Text != "" {
				step.content = append(step.content, jsonObject("type", "text", "text", item.Text))
			}
		case "thinking":
			if replayThinking && item.Thinking != "" {
				part := jsonObject("type", "reasoning", "text", item.Thinking, "providerOptions",
					jsonObject("cursor", jsonObject("modelName", msg.Model)))
				if item.ThinkingSignature != "" {
					part.Set("signature", item.ThinkingSignature)
				}
				step.content = append(step.content, part)
			}
		case "toolCall":
			step.content = append(step.content, jsonObject("type", "tool-call", "toolCallId", NormalizeToolCallID(item.ID),
				"toolName", item.Name, "args", argumentsValue(item.Arguments)))
			step.callIDs = append(step.callIDs, item.ID)
		}
	}
	steps = append(steps, step)
	out := steps[:0]
	for _, s := range steps {
		if len(s.content) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// assertKimiK3HistoryReplayable is assertCursorKimiK3HistoryReplayable's
// failing arm (its warning arm only logs).
func assertKimiK3HistoryReplayable(messages []Message, activeIndex int, targetModelID string) error {
	if targetModelID == "" || neopicatalog.Classify("cursor", targetModelID).Family != "k3" {
		return nil
	}
	end := historyEnd(messages, activeIndex)
	for _, msg := range messages[:end] {
		if msg.Role != "assistant" {
			continue
		}
		if !(msg.API == "cursor-agent" && msg.Provider == "cursor" && msg.Model == targetModelID) {
			return &HistoryError{Message: fmt.Sprintf("Cursor %s cannot continue history from a different model (%s/%s); start a new session.",
				targetModelID, msg.Provider, msg.Model)}
		}
	}
	return nil
}

func historyEnd(messages []Message, activeIndex int) int {
	if activeIndex >= 0 {
		return activeIndex
	}
	return len(messages)
}

type toolHistory struct {
	results map[string]Message
	paired  map[string]bool
}

// collectToolHistory is collectCursorToolHistory.
func collectToolHistory(messages []Message, activeIndex int) toolHistory {
	out := toolHistory{results: map[string]Message{}, paired: map[string]bool{}}
	for _, msg := range messages[:historyEnd(messages, activeIndex)] {
		switch msg.Role {
		case "toolResult":
			out.results[msg.ToolCallID] = msg
		case "assistant":
			for _, item := range msg.Blocks {
				if item.Type == "toolCall" {
					out.paired[item.ID] = true
				}
			}
		}
	}
	return out
}

func orphanToolResultText(msg Message) string {
	prefix := "[Tool Result]"
	if msg.IsError {
		prefix = "[Tool Error]"
	}
	text := toolResultText(msg)
	if text == "" {
		text = "(empty result)"
	}
	return prefix + "\n" + text
}

// rootPromptMessagesJSON is buildRootPromptMessagesJson.
func rootPromptMessagesJSON(messages []Message, systemPromptIDs [][]byte, blobs *Blobs, activeIndex int, targetModelID string, history toolHistory) ([][]byte, error) {
	if err := assertKimiK3HistoryReplayable(messages, activeIndex, targetModelID); err != nil {
		return nil, err
	}
	end := historyEnd(messages, activeIndex)
	entries := append([][]byte(nil), systemPromptIDs...)
	push := func(object *JSONObject) { entries = append(entries, blobs.Store([]byte(StringifyJSON(object)))) }
	emitted := map[string]bool{}
	pushToolResult := func(result Message) {
		id := NormalizeToolCallID(result.ToolCallID)
		item := jsonObject("type", "tool-result", "toolName", result.ToolName, "toolCallId", id, "result", toolResultText(result))
		if result.IsError {
			item.Set("isError", true)
		}
		push(jsonObject("role", "tool", "id", id, "content", []any{item}))
		emitted[result.ToolCallID] = true
	}
	for i := 0; i < end; i++ {
		msg := messages[i]
		switch {
		case isUserLike(msg.Role):
			content := rootPromptContent(msg)
			if len(content) == 0 {
				continue
			}
			push(jsonObject("role", "user", "content", content))
		case msg.Role == "assistant":
			steps := assistantSteps(msg, targetModelID)
			for index, step := range steps {
				push(jsonObject("role", "assistant", "content", step.content))
				if index == len(steps)-1 {
					continue
				}
				round := map[string]bool{}
				for _, id := range step.callIDs {
					round[id] = true
				}
				for j := i + 1; j < end; j++ {
					later := messages[j]
					if later.Role != "toolResult" || !round[later.ToolCallID] || emitted[later.ToolCallID] {
						continue
					}
					if result, ok := history.results[later.ToolCallID]; ok {
						pushToolResult(result)
					}
				}
			}
		case msg.Role == "toolResult":
			if emitted[msg.ToolCallID] {
				continue
			}
			if !history.paired[msg.ToolCallID] {
				push(jsonObject("role", "assistant", "content", []any{jsonObject("type", "text", "text", orphanToolResultText(msg))}))
				continue
			}
			result := msg
			for j := i + 1; j < end && messages[j].Role == "toolResult"; j++ {
				if messages[j].ToolCallID == msg.ToolCallID {
					result = messages[j]
				}
			}
			pushToolResult(result)
		}
	}
	return entries, nil
}

// encodeMcpArguments is encodeCursorMcpArguments.
func encodeMcpArguments(args *JSONObject) Map[[]byte] {
	var out Map[[]byte]
	if args == nil {
		return out
	}
	for _, name := range args.Keys() {
		value, _ := args.Get(name)
		out.Set(name, EncodeJSONValue(value))
	}
	return out
}

// mcpResult is createCursorMcpResult.
func mcpResult(result Message) *McpToolResult {
	if result.IsError {
		return &McpToolResult{Result: &McpToolResult_Error{Error: &McpToolError{Error: toolResultText(result)}}}
	}
	content := make([]*McpToolResultContentItem, len(result.Parts))
	for i, part := range result.Parts {
		if part.Image {
			content[i] = &McpToolResultContentItem{Content: &McpToolResultContentItem_Image{Image: &McpImageContent{Data: NodeBase64(part.Data), MimeType: part.MimeType}}}
		} else {
			content[i] = &McpToolResultContentItem{Content: &McpToolResultContentItem_Text{Text: &McpTextContent{Text: part.Text}}}
		}
	}
	return &McpToolResult{Result: &McpToolResult_Success{Success: &McpSuccess{Content: content}}}
}

// toolCallStep is createCursorToolCallStep.
func toolCallStep(call Block, result *Message) *ConversationStep {
	id := NormalizeToolCallID(call.ID)
	mcp := &McpToolCall{Args: &McpArgs{Name: call.Name, Args: encodeMcpArguments(call.Arguments), ToolCallId: id,
		ProviderIdentifier: "pi-agent", ToolName: call.Name}}
	if result != nil {
		mcp.Result = mcpResult(*result)
	}
	return &ConversationStep{Message: &ConversationStep_ToolCall{ToolCall: &ToolCall{Tool: &ToolCall_McpToolCall{McpToolCall: mcp}, ToolCallId: &id}}}
}

// conversationTurns is buildConversationTurns.
func conversationTurns(messages []Message, blobs *Blobs, activeIndex int, targetModelID string, history toolHistory, newUUID func() string) [][]byte {
	var turns [][]byte
	for i := 0; i < len(messages); {
		msg := messages[i]
		if !isUserLike(msg.Role) {
			i++
			continue
		}
		if i == activeIndex {
			break
		}
		text := userText(msg)
		if text == "" && !hasImages(msg) {
			i++
			continue
		}
		user := createUserMessage(msg, text, DeterministicUUID(fmt.Sprintf("u:%d:%s", len(turns), userContentKey(msg))), newUUID)
		userBlob := blobs.Store(user.Marshal())
		var steps [][]byte
		i++
		for i < len(messages) && !isUserLike(messages[i].Role) {
			stepMsg := messages[i]
			switch {
			case stepMsg.Role == "assistant":
				issued := map[string]bool{}
				for _, item := range stepMsg.Blocks {
					var step *ConversationStep
					switch item.Type {
					case "text":
						if item.Text == "" {
							continue
						}
						step = &ConversationStep{Message: &ConversationStep_AssistantMessage{AssistantMessage: &AssistantMessage{Text: item.Text}}}
					case "thinking":
						if item.Thinking == "" || !canReplayThinking(stepMsg, targetModelID) {
							continue
						}
						step = &ConversationStep{Message: &ConversationStep_ThinkingMessage{ThinkingMessage: &ThinkingMessage{Text: item.Thinking}}}
					case "toolCall":
						if issued[item.ID] {
							continue
						}
						issued[item.ID] = true
						var result *Message
						if found, ok := history.results[item.ID]; ok {
							result = &found
						}
						step = toolCallStep(item, result)
					default:
						continue
					}
					steps = append(steps, blobs.Store(step.Marshal()))
				}
			case stepMsg.Role == "toolResult" && !history.paired[stepMsg.ToolCallID]:
				step := &ConversationStep{Message: &ConversationStep_AssistantMessage{AssistantMessage: &AssistantMessage{Text: orphanToolResultText(stepMsg)}}}
				steps = append(steps, blobs.Store(step.Marshal()))
			}
			i++
		}
		turn := &ConversationTurnStructure{Turn: &ConversationTurnStructure_AgentConversationTurn{
			AgentConversationTurn: &AgentConversationTurnStructure{UserMessage: userBlob, Steps: steps}}}
		turns = append(turns, blobs.Store(turn.Marshal()))
	}
	return turns
}

// MaxModeFor is resolveCursorMaxMode.
func (m *Model) MaxModeFor(wireModelID string) bool {
	if discovered, ok := m.MaxModeRoutes[wireModelID]; ok {
		return discovered
	}
	own := func() bool {
		if m.MaxMode != nil {
			return *m.MaxMode
		}
		return neopicatalog.IsCursorMaxModeWireID(wireModelID)
	}
	if m.EffortRouting == nil || wireModelID == m.ID {
		return own()
	}
	off, hasOff := m.EffortRouting["off"]
	routesOwnID := hasOff && off == m.ID
	hasInferredMaxRoute := hasOff && neopicatalog.IsCursorMaxModeWireID(off)
	for _, effort := range thinkingEfforts {
		target, ok := m.EffortRouting[effort]
		if ok && target == m.ID {
			routesOwnID = true
		}
		if ok && neopicatalog.IsCursorMaxModeWireID(target) {
			hasInferredMaxRoute = true
		}
	}
	if routesOwnID {
		return own()
	}
	if m.MaxMode != nil && *m.MaxMode && !hasInferredMaxRoute {
		return true
	}
	return neopicatalog.IsCursorMaxModeWireID(wireModelID)
}

// wireModel is resolveCursorWireModel's result.
type wireModel struct {
	modelID, modelDetailsID string
	parameters              []*RequestedModel_ModelParameterbytes
	maxMode                 bool
	discoveredRoute         bool
}

func parameterBytes(parameters []neopicatalog.ModelParameter) []*RequestedModel_ModelParameterbytes {
	out := make([]*RequestedModel_ModelParameterbytes, len(parameters))
	for i, parameter := range parameters {
		out[i] = &RequestedModel_ModelParameterbytes{Id: parameter.ID, Value: parameter.Value}
	}
	return out
}

// resolveWireModel is resolveCursorWireModel.
func (m *Model) resolveWireModel(wireModelID string, mode WireMode) wireModel {
	maxMode := m.MaxModeFor(wireModelID)
	if route, ok := m.ModelRoutes[wireModelID]; ok {
		if route.MaxMode != nil {
			maxMode = *route.MaxMode
		}
		return wireModel{modelID: route.ModelID, modelDetailsID: wireModelID, parameters: parameterBytes(route.Parameters), maxMode: maxMode, discoveredRoute: true}
	}
	if mode == WireDiscovered {
		return wireModel{modelID: wireModelID, modelDetailsID: wireModelID, parameters: parameterBytes(m.ModelParameters), maxMode: maxMode}
	}
	collapsed := neopicatalog.CollapseVariantID("cursor", wireModelID)
	if collapsed.Effort != "" && collapsed.LogicalID != "" && neopicatalog.Classify("cursor", collapsed.LogicalID).Class == "openai" {
		if collapsed.Effort == "off" {
			return wireModel{modelID: collapsed.LogicalID, modelDetailsID: wireModelID, maxMode: maxMode}
		}
		for _, effort := range thinkingEfforts {
			if effort == collapsed.Effort {
				return wireModel{modelID: collapsed.LogicalID, modelDetailsID: wireModelID, maxMode: maxMode,
					parameters: []*RequestedModel_ModelParameterbytes{{Id: "reasoning", Value: collapsed.Effort}}}
			}
		}
	}
	if fixed := neopicatalog.CursorModelParameters(wireModelID); len(fixed) > 0 {
		return wireModel{modelID: wireModelID, modelDetailsID: wireModelID, parameters: parameterBytes(fixed), maxMode: maxMode}
	}
	return wireModel{modelID: wireModelID, modelDetailsID: wireModelID, maxMode: maxMode}
}

// BuildRunRequest is buildGrpcRequestForWireMode. wireModelID is the
// effort-routed id (resolveWireModelId); onPayload, when set, may edit the
// run request before it is serialized.
func BuildRunRequest(model *Model, ctx *Context, wireModelID string, state *RequestState, mode WireMode, newUUID func() string, onPayload func(*AgentRunRequest)) (*BuiltRequest, error) {
	blobs := state.Blobs
	jsons := SystemPromptJSONs(ctx.SystemPrompt)
	systemPromptIDs := make([][]byte, len(jsons))
	for i, text := range jsons {
		systemPromptIDs[i] = blobs.Store([]byte(text))
	}

	historyEndIndex := -1
	var active *Message
	if n := len(ctx.Messages); n > 0 && !state.Resume && isUserLike(ctx.Messages[n-1].Role) {
		active = &ctx.Messages[n-1]
		historyEndIndex = n - 1
	}
	action := &ConversationAction{Action: &ConversationAction_ResumeAction{ResumeAction: &ResumeAction{}}}
	if active != nil {
		text := ""
		truthy := true
		if active.Plain {
			text = jsTrim(active.Text)
			truthy = active.Text != ""
		} else {
			text = joinText(active.Parts)
		}
		if truthy && (jsTrim(text) != "" || hasImages(*active)) {
			action = &ConversationAction{Action: &ConversationAction_UserMessageAction{UserMessageAction: &UserMessageAction{
				UserMessage: createUserMessage(*active, text, newUUID(), newUUID)}}}
		}
	}

	wire := model.resolveWireModel(wireModelID, mode)
	details := &ModelDetails{ModelId: wire.modelDetailsID, DisplayModelId: model.ID, DisplayName: model.Name}
	if wire.maxMode {
		details.MaxMode = ptr(true)
	}
	requested := &RequestedModel{ModelId: wire.modelID, MaxMode: wire.maxMode, Parameters: wire.parameters}

	var conversationState *ConversationStateStructure
	if state.Resume && state.ConversationState != nil {
		conversationState = state.ConversationState
	} else {
		built, err := buildConversationState(model, ctx, state, blobs, systemPromptIDs, historyEndIndex, newUUID)
		if err != nil {
			return nil, err
		}
		conversationState = built
	}

	run := &AgentRunRequest{ConversationState: conversationState, Action: action, ModelDetails: details,
		RequestedModel: requested, ConversationId: ptr(state.ConversationID)}
	if onPayload != nil {
		onPayload(run)
	}

	fallback := ""
	if mode == WireNormalized && !wire.discoveredRoute && len(wire.parameters) > 0 && wire.modelID != wireModelID &&
		run.RequestedModel != nil && run.RequestedModel.ModelId == wire.modelID &&
		run.ModelDetails != nil && run.ModelDetails.ModelId == wire.modelDetailsID &&
		len(run.RequestedModel.Parameters) == len(wire.parameters) {
		same := true
		for i, parameter := range run.RequestedModel.Parameters {
			if parameter.Id != wire.parameters[i].Id || parameter.Value != wire.parameters[i].Value {
				same = false
				break
			}
		}
		if same {
			fallback = wireModelID
		}
	}
	message := &AgentClientMessage{Message: &AgentClientMessage_RunRequest{RunRequest: run}}
	return &BuiltRequest{Bytes: message.Marshal(), ConversationState: conversationState, FallbackWireModelID: fallback}, nil
}

func ptr[T any](v T) *T { return &v }

// buildConversationState is buildCursorConversationState without server
// record replay: gateway history never carries a cursorHistory payload.
func buildConversationState(model *Model, ctx *Context, state *RequestState, blobs *Blobs, systemPromptIDs [][]byte, historyEndIndex int, newUUID func() string) (*ConversationStateStructure, error) {
	history := collectToolHistory(ctx.Messages, historyEndIndex)
	turns := conversationTurns(ctx.Messages, blobs, historyEndIndex, model.ID, history, newUUID)
	root, err := rootPromptMessagesJSON(ctx.Messages, systemPromptIDs, blobs, historyEndIndex, model.ID, history)
	if err != nil {
		return nil, err
	}
	if len(root) > len(systemPromptIDs) {
		if entry := rulesEntryJSON(ctx.SystemPrompt); entry != "" {
			id := blobs.Store([]byte(entry))
			root = append(root[:len(systemPromptIDs)], append([][]byte{id}, root[len(systemPromptIDs):]...)...)
		}
	}
	matching := false
	if cached := state.ConversationState; cached != nil && len(cached.RootPromptMessagesJson) >= len(systemPromptIDs) {
		matching = true
		for i, id := range systemPromptIDs {
			if string(cached.RootPromptMessagesJson[i]) != string(id) {
				matching = false
				break
			}
		}
	}
	out := &ConversationStateStructure{}
	if matching {
		copied := *state.ConversationState
		out = &copied
	}
	out.RootPromptMessagesJson = root
	out.Turns = turns
	return out, nil
}
