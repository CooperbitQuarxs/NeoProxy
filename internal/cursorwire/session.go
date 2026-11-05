package cursorwire

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Ports of providers/cursor.ts stream handling (handleServerMessage,
// processInteractionUpdate, the exec dispatcher without local handlers,
// handleKvServerMessage) and cursor/interaction-query.ts, as the auth gateway
// runs them: no exec handlers, unhandled MCP calls handed to the client.

// Usage is the AssistantMessage usage the stream maintains.
type Usage struct {
	Input, Output, CacheRead, CacheWrite, Total int64
	// Reasoning is set only when turnEnded reports reasoning tokens.
	Reasoning     *int64
	ContextTokens int64
}

// OutBlock is one assistant content block the stream built.
type OutBlock struct {
	Type      string // "text", "thinking" or "toolCall"
	Text      string // text or thinking content
	ID, Name  string
	Arguments any
	// Kind is the streamed block kind: mcp, todo, cursor-exec, cursor-edit,
	// connect-scm or web-fetch.
	Kind       string
	EnvelopeID string
	// Resolved is kCursorExecResolved: Cursor ran the call, so it is no
	// handoff to the client.
	Resolved bool
	partial  *string
}

// Output is the AssistantMessage a turn accumulates across its retries.
type Output struct {
	Content    []*OutBlock
	Usage      Usage
	StopReason string
}

// Sink receives the stream's content events.
type Sink interface {
	TextDelta(delta string)
	ThinkingDelta(delta string)
	ToolCallStart(block *OutBlock)
	ToolCallDelta(block *OutBlock, delta string)
	ToolCallEnd(block *OutBlock)
}

// Turn is the state a turn's attempts share: output, open blocks and usage.
type Turn struct {
	Output *Output
	Sink   Sink
	// NewUUID mints the ids crypto.randomUUID would.
	NewUUID func() string

	text, thinking *OutBlock
	current        *OutBlock
	// open is openToolCalls, an insertion-ordered map keyed by envelope id.
	open          Map[*OutBlock]
	resolvedMcp   map[string]bool
	editOwned     map[string]bool
	pairedEdit    map[string]bool
	sawTokenDelta bool
}

// NewTurn starts a turn's shared state.
func NewTurn(sink Sink, newUUID func() string) *Turn {
	return &Turn{Output: &Output{StopReason: "stop"}, Sink: sink, NewUUID: newUUID,
		resolvedMcp: map[string]bool{}, editOwned: map[string]bool{}, pairedEdit: map[string]bool{}}
}

// SawTokenDelta reports whether any tokenDelta arrived (usageState).
func (t *Turn) SawTokenDelta() bool { return t.sawTokenDelta }

func (t *Turn) push(block *OutBlock) {
	t.Output.Content = append(t.Output.Content, block)
}

func (t *Turn) hasToolCall(id string) *OutBlock {
	for _, block := range t.Output.Content {
		if block.Type == "toolCall" && block.ID == id {
			return block
		}
	}
	return nil
}

func (t *Turn) endText()     { t.text = nil }
func (t *Turn) endThinking() { t.thinking = nil }

func (t *Turn) retain(block *OutBlock, envelopeID string) {
	if envelopeID != "" {
		t.open.Set(envelopeID, block)
	}
	t.current = block
}

func (t *Turn) resolveStreamed(envelopeID string) *OutBlock {
	if envelopeID == "" {
		return t.current
	}
	if keyed, _ := t.open.Get(envelopeID); keyed != nil {
		return keyed
	}
	if t.current != nil && t.current.EnvelopeID == "" {
		return t.current
	}
	return nil
}

func (t *Turn) release(block *OutBlock) {
	if block.EnvelopeID != "" {
		t.open.Delete(block.EnvelopeID)
	}
	if t.current == block {
		t.current = nil
	}
}

// synthesize is synthesizeCursorExecToolCall.
func (t *Turn) synthesize(id, name string, args any, executed bool) {
	if t.hasToolCall(id) != nil {
		return
	}
	t.endText()
	t.endThinking()
	block := &OutBlock{Type: "toolCall", ID: id, Name: name, Arguments: args, Kind: "cursor-exec", Resolved: executed}
	t.push(block)
	t.Sink.ToolCallStart(block)
	t.Sink.ToolCallEnd(block)
}

// Flush is the end-of-stream close: open text/thinking ended and every open
// tool-call block settled (flushOpenToolCalls).
func (t *Turn) Flush() {
	t.endText()
	t.endThinking()
	seen := map[*OutBlock]bool{}
	var blocks []*OutBlock
	for _, key := range t.open.keys {
		block, _ := t.open.Get(key)
		if !seen[block] {
			seen[block] = true
			blocks = append(blocks, block)
		}
	}
	if t.current != nil && !seen[t.current] {
		blocks = append(blocks, t.current)
	}
	for _, block := range blocks {
		if block.partial != nil {
			block.Arguments = ParseToolCallArguments(*block.partial)
			block.partial = nil
		}
		t.Sink.ToolCallEnd(block)
	}
	t.open = Map[*OutBlock]{}
	t.current = nil
}

// OpenToolCalls reports whether any streamed tool-call block is unsettled.
func (t *Turn) OpenToolCalls() bool { return t.current != nil || t.open.Len() > 0 }

// LastIsText reports whether the last content block is text.
func (t *Turn) LastIsText() bool {
	n := len(t.Output.Content)
	return n > 0 && t.Output.Content[n-1].Type == "text"
}

// Attempt is one Run request's protocol state.
type Attempt struct {
	Turn  *Turn
	Blobs *Blobs
	Tools []*McpToolDefinition
	Rules []*CursorRule
	// WriteRaw writes a frame without counting it as progress (KV replies,
	// heartbeats); WriteResult writes a client result.
	WriteRaw    func(frame []byte)
	WriteResult func(frame []byte)
	// OnCheckpoint stores a checkpoint in the conversation cache.
	OnCheckpoint func(*ConversationStateStructure)
	NewUUID      func() string
	// ExternalToolExecutor hands unhandled MCP calls to the client.
	ExternalToolExecutor bool

	ProgressVersion                 int
	LatestCheckpoint                *ConversationStateStructure
	LatestCheckpointProgressVersion int
	LatestCheckpointTerminal        bool
	SawTurnEnded                    bool
	StepCompletedIsLatest           bool
	SawProgressOrSideEffect         bool
	EndStreamError                  *RunError

	deferred []deferredReply
}

// deferredReply is a reply NeoPi writes after `depth` microtask hops.
type deferredReply struct {
	depth int
	fn    func()
}

// NewAttempt prepares an attempt; LatestCheckpointProgressVersion starts at -1.
func NewAttempt(turn *Turn) *Attempt {
	return &Attempt{Turn: turn, NewUUID: turn.NewUUID, LatestCheckpointProgressVersion: -1}
}

func (a *Attempt) writeResult(message *AgentClientMessage) {
	a.ProgressVersion++
	a.WriteResult(Frame(message.Marshal(), 0))
}

// later queues a reply NeoPi writes only after an await, so it follows every
// synchronous reply to frames already read. A local-handler frame answered
// through resolveExecHandler resolves three microtask hops later.
func (a *Attempt) later(fn func()) { a.laterAt(3, fn) }

// laterAt queues a reply that resolves after depth microtask hops: a bare
// `await undefined` is one, an unpaired resolveExecHandler two.
func (a *Attempt) laterAt(depth int, fn func()) {
	a.deferred = append(a.deferred, deferredReply{depth: depth, fn: fn})
}

// FlushDeferred runs queued replies; call when no further frame is already
// buffered (NeoPi's microtasks run once the data chunk is consumed). The
// microtask queue drains hop by hop, so shallower replies go first and equal
// depths keep arrival order.
func (a *Attempt) FlushDeferred() {
	queued := a.deferred
	a.deferred = nil
	sort.SliceStable(queued, func(i, j int) bool { return queued[i].depth < queued[j].depth })
	for _, reply := range queued {
		reply.fn()
	}
}

// HandleFrame is onData's per-frame body. It reports whether the frame was
// the Connect end-of-stream frame.
func (a *Attempt) HandleFrame(flags byte, payload []byte) (endStream bool) {
	if flags&FlagEndStream != 0 {
		a.EndStreamError = ParseConnectEndStream(payload)
		return true
	}
	var msg AgentServerMessage
	if err := msg.Unmarshal(payload); err != nil {
		return false
	}
	interactionCase := ""
	update, _ := msg.Message.(*AgentServerMessage_InteractionUpdate)
	if update != nil && update.InteractionUpdate != nil {
		interactionCase = interactionUpdateCase(update.InteractionUpdate)
	}
	_, isCheckpoint := msg.Message.(*AgentServerMessage_ConversationCheckpointUpdate)
	_, isKv := msg.Message.(*AgentServerMessage_KvServerMessage)
	if interactionCase != "heartbeat" {
		a.SawProgressOrSideEffect = true
		a.ProgressVersion++
	}
	if interactionCase == "stepCompleted" {
		a.StepCompletedIsLatest = true
	} else if interactionCase != "heartbeat" && !isCheckpoint && !isKv {
		a.StepCompletedIsLatest = false
	}
	if isCheckpoint {
		checkpoint := msg.Message.(*AgentServerMessage_ConversationCheckpointUpdate).ConversationCheckpointUpdate
		a.LatestCheckpoint = checkpoint
		a.LatestCheckpointProgressVersion = a.ProgressVersion
		a.LatestCheckpointTerminal = a.SawTurnEnded
		if a.OnCheckpoint != nil {
			a.OnCheckpoint(checkpoint)
		}
	}
	a.handleServerMessage(&msg)
	if interactionCase == "turnEnded" {
		a.SawTurnEnded = true
	}
	return false
}

func interactionUpdateCase(update *InteractionUpdate) string {
	switch update.Message.(type) {
	case *InteractionUpdate_TextDelta:
		return "textDelta"
	case *InteractionUpdate_ThinkingDelta:
		return "thinkingDelta"
	case *InteractionUpdate_ThinkingCompleted:
		return "thinkingCompleted"
	case *InteractionUpdate_ToolCallStarted:
		return "toolCallStarted"
	case *InteractionUpdate_ToolCallDelta:
		return "toolCallDelta"
	case *InteractionUpdate_PartialToolCall:
		return "partialToolCall"
	case *InteractionUpdate_ToolCallCompleted:
		return "toolCallCompleted"
	case *InteractionUpdate_TurnEnded:
		return "turnEnded"
	case *InteractionUpdate_TokenDelta:
		return "tokenDelta"
	case *InteractionUpdate_Heartbeat:
		return "heartbeat"
	case *InteractionUpdate_StepCompleted:
		return "stepCompleted"
	case *InteractionUpdate_StepStarted:
		return "stepStarted"
	case nil:
		return ""
	}
	return "other"
}

func (a *Attempt) handleServerMessage(msg *AgentServerMessage) {
	switch m := msg.Message.(type) {
	case *AgentServerMessage_InteractionUpdate:
		if m.InteractionUpdate != nil {
			a.Turn.processInteractionUpdate(m.InteractionUpdate)
		}
	case *AgentServerMessage_KvServerMessage:
		if m.KvServerMessage != nil {
			a.handleKv(m.KvServerMessage)
		}
	case *AgentServerMessage_ExecServerMessage:
		if m.ExecServerMessage != nil {
			a.handleExec(m.ExecServerMessage)
		}
	case *AgentServerMessage_InteractionQuery:
		if m.InteractionQuery != nil {
			a.handleInteractionQuery(m.InteractionQuery)
		}
	case *AgentServerMessage_ConversationCheckpointUpdate:
		if m.ConversationCheckpointUpdate != nil {
			if details := m.ConversationCheckpointUpdate.TokenDetails; details != nil && details.UsedTokens > 0 {
				a.Turn.Output.Usage.ContextTokens = int64(details.UsedTokens)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// processInteractionUpdate
// ---------------------------------------------------------------------------

func (t *Turn) processInteractionUpdate(update *InteractionUpdate) {
	switch u := update.Message.(type) {
	case *InteractionUpdate_TextDelta:
		delta := ""
		if u.TextDelta != nil {
			delta = u.TextDelta.Text
		}
		if t.text == nil {
			t.text = &OutBlock{Type: "text"}
			t.push(t.text)
		}
		t.text.Text += delta
		t.Sink.TextDelta(delta)
	case *InteractionUpdate_ThinkingDelta:
		delta := ""
		if u.ThinkingDelta != nil {
			delta = u.ThinkingDelta.Text
		}
		if t.thinking == nil {
			t.thinking = &OutBlock{Type: "thinking"}
			t.push(t.thinking)
		}
		t.thinking.Text += delta
		t.Sink.ThinkingDelta(delta)
	case *InteractionUpdate_ThinkingCompleted:
		t.endThinking()
	case *InteractionUpdate_ToolCallStarted:
		if u.ToolCallStarted != nil {
			t.toolCallStarted(u.ToolCallStarted.CallId, u.ToolCallStarted.ToolCall)
		}
	case *InteractionUpdate_ToolCallDelta:
		if u.ToolCallDelta != nil {
			t.toolCallDelta(u.ToolCallDelta.CallId, u.ToolCallDelta.ToolCallDelta, "")
		}
	case *InteractionUpdate_PartialToolCall:
		if u.PartialToolCall != nil {
			if u.PartialToolCall.ToolCall != nil {
				t.openOrUpdateEdit(u.PartialToolCall.ToolCall, u.PartialToolCall.CallId)
			}
			t.toolCallDelta(u.PartialToolCall.CallId, nil, u.PartialToolCall.ArgsTextDelta)
		}
	case *InteractionUpdate_ToolCallCompleted:
		if u.ToolCallCompleted != nil {
			t.toolCallCompleted(u.ToolCallCompleted.CallId, u.ToolCallCompleted.ToolCall)
		}
	case *InteractionUpdate_TurnEnded:
		t.Output.StopReason = "stop"
		if u.TurnEnded != nil {
			applyTurnEndedUsage(&t.Output.Usage, u.TurnEnded)
		}
	case *InteractionUpdate_TokenDelta:
		t.sawTokenDelta = true
		if u.TokenDelta != nil {
			t.Output.Usage.Output += int64(u.TokenDelta.Tokens)
		}
		t.Output.Usage.Total = t.Output.Usage.Input + t.Output.Usage.Output
	}
}

func applyTurnEndedUsage(usage *Usage, update *TurnEndedUpdate) {
	value := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	input, output := value(update.InputTokens), value(update.OutputTokens)
	cacheRead, cacheWrite := value(update.CacheReadTokens), value(update.CacheWriteTokens)
	reasoning := value(update.ReasoningTokens)
	if input <= 0 && output <= 0 && cacheRead <= 0 && cacheWrite <= 0 {
		return
	}
	if input > 0 {
		usage.Input = max(input-cacheRead-cacheWrite, 0)
	}
	if output > 0 {
		usage.Output = output
	}
	if cacheRead > 0 {
		usage.CacheRead = cacheRead
	}
	if cacheWrite > 0 {
		usage.CacheWrite = cacheWrite
	}
	if reasoning > 0 {
		usage.Reasoning = &reasoning
	}
	usage.Total = usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

// execOwnedToolCall is isExecOwnedToolCall.
func execOwnedToolCall(call *ToolCall) bool {
	switch call.Tool.(type) {
	case *ToolCall_PiReadToolCall, *ToolCall_PiBashToolCall, *ToolCall_PiEditToolCall, *ToolCall_PiWriteToolCall,
		*ToolCall_PiGrepToolCall, *ToolCall_PiFindToolCall, *ToolCall_PiLsToolCall,
		*ToolCall_ListMcpResourcesToolCall, *ToolCall_ReadMcpResourceToolCall:
		return true
	}
	return false
}

// newID is `envelopeId || crypto.randomUUID()`.
func (t *Turn) newID(envelopeID string) string {
	if envelopeID != "" {
		return envelopeID
	}
	return t.NewUUID()
}

func (t *Turn) toolCallStarted(callID string, call *ToolCall) {
	if scm, ok := call.GetTool().(*ToolCall_ConnectScmToolCall); ok {
		t.endText()
		t.endThinking()
		id := ""
		if scm.ConnectScmToolCall != nil && scm.ConnectScmToolCall.Args != nil {
			id = scm.ConnectScmToolCall.Args.ToolCallId
		}
		if id == "" {
			id = t.newID(callID)
		}
		block := &OutBlock{Type: "toolCall", ID: id, Name: "connect_scm", Kind: "connect-scm", EnvelopeID: callID, Resolved: true}
		t.push(block)
		t.retain(block, callID)
		t.Sink.ToolCallStart(block)
		return
	}
	if call != nil && execOwnedToolCall(call) {
		t.endText()
		t.endThinking()
		return
	}
	t.endText()
	t.endThinking()
	if call == nil {
		return
	}
	switch tool := call.Tool.(type) {
	case *ToolCall_McpToolCall:
		args := (*McpArgs)(nil)
		if tool.McpToolCall != nil {
			args = tool.McpToolCall.Args
		}
		if args == nil {
			args = &McpArgs{}
		}
		id := args.ToolCallId
		if id == "" {
			id = t.NewUUID()
		}
		delete(t.resolvedMcp, id)
		if t.hasToolCall(id) != nil {
			return
		}
		name := args.ToolName
		if name == "" {
			name = args.Name
		}
		block := &OutBlock{Type: "toolCall", ID: id, Name: name, Arguments: decodeMcpArgsMap(args.Args), Kind: "mcp", EnvelopeID: callID}
		t.push(block)
		t.retain(block, callID)
		t.Sink.ToolCallStart(block)
		return
	case *ToolCall_UpdateTodosToolCall, *ToolCall_ReadTodosToolCall:
		block := &OutBlock{Type: "toolCall", ID: t.newID(callID), Name: "todo", Kind: "todo", EnvelopeID: callID, Resolved: true}
		t.push(block)
		t.retain(block, callID)
		t.Sink.ToolCallStart(block)
		return
	case *ToolCall_FetchToolCall, *ToolCall_WebFetchToolCall:
		id := ""
		switch fetch := tool.(type) {
		case *ToolCall_FetchToolCall:
			if fetch.FetchToolCall != nil && fetch.FetchToolCall.Args != nil {
				id = fetch.FetchToolCall.Args.ToolCallId
			}
		case *ToolCall_WebFetchToolCall:
			if fetch.WebFetchToolCall != nil && fetch.WebFetchToolCall.Args != nil {
				id = fetch.WebFetchToolCall.Args.ToolCallId
			}
		}
		if id == "" {
			id = t.newID(callID)
		}
		block := &OutBlock{Type: "toolCall", ID: id, Name: "web_fetch", Kind: "web-fetch", EnvelopeID: callID, Resolved: true}
		t.push(block)
		t.retain(block, callID)
		t.Sink.ToolCallStart(block)
		return
	}
	if hasUnknownField(call.Unknown, 37) {
		block := &OutBlock{Type: "toolCall", ID: t.newID(callID), Name: "web_fetch", Kind: "web-fetch", EnvelopeID: callID, Resolved: true}
		t.push(block)
		t.retain(block, callID)
		t.Sink.ToolCallStart(block)
		return
	}
	t.openOrUpdateEdit(call, callID)
}

// GetTool returns the selected ToolCall variant (nil-safe).
func (m *ToolCall) GetTool() isToolCall_Tool {
	if m == nil {
		return nil
	}
	return m.Tool
}

// openOrUpdateEdit is openOrUpdateEditBlock.
func (t *Turn) openOrUpdateEdit(call *ToolCall, envelopeID string) {
	if _, ok := call.Tool.(*ToolCall_EditToolCall); !ok {
		return
	}
	if envelopeID != "" {
		t.editOwned[envelopeID] = true
	}
	id := ""
	if call.ToolCallId != nil {
		id = *call.ToolCallId
	}
	if id != "" {
		t.editOwned[id] = true
	} else {
		id = envelopeID
	}
	if id == "" {
		return
	}
	if t.hasToolCall(id) != nil {
		return
	}
	t.endText()
	t.endThinking()
	block := &OutBlock{Type: "toolCall", ID: id, Name: "edit", Kind: "cursor-edit", EnvelopeID: envelopeID, Resolved: true}
	t.push(block)
	t.retain(block, envelopeID)
	t.Sink.ToolCallStart(block)
}

func (t *Turn) toolCallDelta(callID string, delta *ToolCallDelta, snapshot string) {
	if delta != nil {
		if edit, ok := delta.Delta.(*ToolCallDelta_EditToolCallDelta); ok && edit.EditToolCallDelta != nil && edit.EditToolCallDelta.StreamContentDelta != "" {
			if target := t.resolveStreamed(callID); target != nil && target.Kind == "cursor-edit" {
				t.Sink.ToolCallDelta(target, edit.EditToolCallDelta.StreamContentDelta)
				return
			}
		}
	}
	target := t.resolveStreamed(callID)
	if target == nil || target.Kind != "mcp" {
		return
	}
	current := ""
	if target.partial != nil {
		current = *target.partial
	}
	chunk := snapshot
	if strings.HasPrefix(snapshot, current) {
		chunk = snapshot[len(current):]
	}
	if chunk == "" {
		return
	}
	next := current + chunk
	target.partial = &next
	t.Sink.ToolCallDelta(target, chunk)
}

func (t *Turn) toolCallCompleted(callID string, call *ToolCall) {
	settled := t.resolveStreamed(callID)
	if settled == nil {
		return
	}
	if settled.Kind == "mcp" {
		if settled.partial != nil && *settled.partial != "" {
			settled.Arguments = ParseToolCallArguments(*settled.partial)
		}
		var decoded *JSONObject
		if mcp, ok := call.GetTool().(*ToolCall_McpToolCall); ok && mcp.McpToolCall != nil && mcp.McpToolCall.Args != nil {
			decoded = decodeMcpArgsMap(mcp.McpToolCall.Args.Args)
		}
		args, isObject := settled.Arguments.(*JSONObject)
		parseError := false
		if isObject {
			_, parseError = args.Get("__parseError")
		}
		if !isObject || !parseError {
			settled.Arguments = mergeMcpToolCallArgs(settled.Arguments, decoded)
		} else if decoded != nil && decoded.Len() > 0 {
			partial := ""
			if settled.partial != nil {
				partial = *settled.partial
			}
			if ClassifyJSONPrefix(partial) != JSONPrefix {
				settled.Arguments = decoded
			}
		}
	}
	settled.partial = nil
	t.Sink.ToolCallEnd(settled)
	t.release(settled)
}

// mergeMcpToolCallArgs is mergeCursorMcpToolCallArgs.
func mergeMcpToolCallArgs(streamed any, completion *JSONObject) *JSONObject {
	merged := &JSONObject{}
	if object, ok := streamed.(*JSONObject); ok {
		for _, key := range object.Keys() {
			value, _ := object.Get(key)
			merged.Set(key, value)
		}
	}
	if completion == nil {
		return merged
	}
	for _, key := range completion.Keys() {
		value, _ := completion.Get(key)
		existing, has := merged.Get(key)
		if _, isString := value.(string); isString && has {
			switch existing.(type) {
			case *JSONObject, []any:
				continue
			}
		}
		merged.Set(key, value)
	}
	return merged
}

// decodeMcpArgValue is decodeMcpArgValue.
func decodeMcpArgValue(value []byte) any {
	if decoded, err := DecodeJSONValue(value); err == nil {
		if text, ok := decoded.(string); ok {
			trimmed := strings.TrimLeftFunc(text, func(r rune) bool { return jsTrim(string(r)) == "" })
			if trimmed != "" && (trimmed[0] == '{' || trimmed[0] == '[' || trimmed[0] == '"') {
				return parseToolArgsJSON(text)
			}
			return text
		}
		return decoded
	}
	return parseToolArgsJSON(strings.ToValidUTF8(string(value), "\uFFFD"))
}

func parseToolArgsJSON(text string) any {
	trimmed := jsTrim(text)
	if trimmed == "" {
		return text
	}
	value, err := ParseJSONWithRepair(trimmed)
	if err != nil {
		return text
	}
	return value
}

// decodeMcpArgsMap is decodeMcpArgsMap; nil for an absent map.
func decodeMcpArgsMap(args Map[[]byte]) *JSONObject {
	out := &JSONObject{}
	for _, key := range args.Keys() {
		value, _ := args.Get(key)
		out.Set(key, decodeMcpArgValue(value))
	}
	return out
}

// ---------------------------------------------------------------------------
// KV, interaction queries
// ---------------------------------------------------------------------------

func (a *Attempt) handleKv(kv *KvServerMessage) {
	switch m := kv.Message.(type) {
	case *KvServerMessage_GetBlobArgs:
		result := &GetBlobResult{}
		if m.GetBlobArgs != nil {
			if data, ok := a.Blobs.Get(m.GetBlobArgs.BlobId); ok {
				result.BlobData = &data
			}
		}
		reply := &AgentClientMessage{Message: &AgentClientMessage_KvClientMessage{KvClientMessage: &KvClientMessage{Id: kv.Id,
			Message: &KvClientMessage_GetBlobResult{GetBlobResult: result}}}}
		a.WriteRaw(Frame(reply.Marshal(), 0))
	case *KvServerMessage_SetBlobArgs:
		if m.SetBlobArgs != nil {
			a.Blobs.Set(m.SetBlobArgs.BlobId, m.SetBlobArgs.BlobData)
		}
		reply := &AgentClientMessage{Message: &AgentClientMessage_KvClientMessage{KvClientMessage: &KvClientMessage{Id: kv.Id,
			Message: &KvClientMessage_SetBlobResult{SetBlobResult: &SetBlobResult{}}}}}
		a.WriteRaw(Frame(reply.Marshal(), 0))
	}
}

const notImplementedSuffix = "not implemented by this client"

type unknownField struct {
	no       int32
	wireType int
	data     []byte
}

// unknownFields splits a message's retained unknown bytes into fields.
func unknownFields(raw []byte) []unknownField {
	var out []unknownField
	r := reader{buf: raw}
	for r.more() {
		number, wireType, err := r.tag()
		if err != nil {
			return out
		}
		start := r.pos
		if err := r.skip(wireType); err != nil {
			return out
		}
		data := raw[start:r.pos]
		if wireType == wireBytes {
			inner := reader{buf: data}
			if payload, err := inner.bytes(); err == nil {
				data = payload
			}
		}
		out = append(out, unknownField{no: number, wireType: wireType, data: data})
	}
	return out
}

func hasUnknownField(raw []byte, number int32) bool {
	for _, field := range unknownFields(raw) {
		if field.no == number {
			return true
		}
	}
	return false
}

func (a *Attempt) sendInteractionResponse(id uint32, result isInteractionResponse_Result) {
	a.writeResult(&AgentClientMessage{Message: &AgentClientMessage_InteractionResponse{InteractionResponse: &InteractionResponse{Id: id, Result: result}}})
}

// handleInteractionQuery is handleInteractionQuery.
func (a *Attempt) handleInteractionQuery(query *InteractionQuery) {
	switch query.Query.(type) {
	case nil:
		for _, field := range unknownFields(query.Unknown) {
			if field.wireType == wireBytes && field.no >= 2 {
				response := &InteractionResponse{Id: query.Id}
				response.Unknown = appendTag(nil, field.no, wireBytes)
				response.Unknown = append(response.Unknown, 0x02, 0x0a, 0x00)
				a.writeResult(&AgentClientMessage{Message: &AgentClientMessage_InteractionResponse{InteractionResponse: response}})
				return
			}
		}
	case *InteractionQuery_WebSearchRequestQuery:
		a.sendInteractionResponse(query.Id, &InteractionResponse_WebSearchRequestResponse{WebSearchRequestResponse: &WebSearchRequestResponse{
			Result: &WebSearchRequestResponse_Approved_{Approved: &WebSearchRequestResponse_Approved{}}}})
	case *InteractionQuery_ExaSearchRequestQuery:
		a.sendInteractionResponse(query.Id, &InteractionResponse_ExaSearchRequestResponse{ExaSearchRequestResponse: &ExaSearchRequestResponse{
			Result: &ExaSearchRequestResponse_Approved_{Approved: &ExaSearchRequestResponse_Approved{}}}})
	case *InteractionQuery_ExaFetchRequestQuery:
		a.sendInteractionResponse(query.Id, &InteractionResponse_ExaFetchRequestResponse{ExaFetchRequestResponse: &ExaFetchRequestResponse{
			Result: &ExaFetchRequestResponse_Approved_{Approved: &ExaFetchRequestResponse_Approved{}}}})
	case *InteractionQuery_WebFetchRequestQuery:
		a.sendInteractionResponse(query.Id, &InteractionResponse_WebFetchRequestResponse{WebFetchRequestResponse: &WebFetchRequestResponse{
			Result: &WebFetchRequestResponse_Approved_{Approved: &WebFetchRequestResponse_Approved{}}}})
	case *InteractionQuery_AskQuestionInteractionQuery:
		a.sendInteractionResponse(query.Id, &InteractionResponse_AskQuestionInteractionResponse{AskQuestionInteractionResponse: &AskQuestionInteractionResponse{
			Result: &AskQuestionResult{Result: &AskQuestionResult_Rejected{Rejected: &AskQuestionRejected{Reason: "Interactive questions are " + notImplementedSuffix}}}}})
	case *InteractionQuery_SwitchModeRequestQuery:
		a.sendInteractionResponse(query.Id, &InteractionResponse_SwitchModeRequestResponse{SwitchModeRequestResponse: &SwitchModeRequestResponse{
			Result: &SwitchModeRequestResponse_Rejected_{Rejected: &SwitchModeRequestResponse_Rejected{Reason: "Mode switches are " + notImplementedSuffix}}}})
	case *InteractionQuery_CreatePlanRequestQuery:
		a.sendInteractionResponse(query.Id, &InteractionResponse_CreatePlanRequestResponse{CreatePlanRequestResponse: &CreatePlanRequestResponse{
			Result: &CreatePlanResult{Result: &CreatePlanResult_Error{Error: &CreatePlanError{Error: "Plan files are " + notImplementedSuffix}}}}})
	}
}

// ---------------------------------------------------------------------------
// Exec dispatcher (no local handlers)
// ---------------------------------------------------------------------------

const toolNotAvailable = "Tool not available"

// mcpExternalHandoffMessage is cursor-external-tool-handoff.md, trimmed.
const mcpExternalHandoffMessage = "Tool call received and handed off to the external client for execution. Do not retry or call it again; end the turn. The result will be provided in the next request."

func (a *Attempt) sendExec(exec *ExecServerMessage, message isExecClientMessage_Message) {
	a.writeResult(&AgentClientMessage{Message: &AgentClientMessage_ExecClientMessage{ExecClientMessage: &ExecClientMessage{
		Id: exec.Id, ExecId: exec.ExecId, Message: message}}})
}

func (a *Attempt) sendStreamClose(exec *ExecServerMessage) {
	a.writeResult(&AgentClientMessage{Message: &AgentClientMessage_ExecClientControlMessage{ExecClientControlMessage: &ExecClientControlMessage{
		Message: &ExecClientControlMessage_StreamClose{StreamClose: &ExecClientStreamClose{Id: exec.Id}}}}})
}

func (a *Attempt) sendThrow(exec *ExecServerMessage, message, code string) {
	a.writeResult(&AgentClientMessage{Message: &AgentClientMessage_ExecClientControlMessage{ExecClientControlMessage: &ExecClientControlMessage{
		Message: &ExecClientControlMessage_Throw{Throw: &ExecClientThrow{Id: exec.Id, Error: message, ErrorCode: &code}}}}})
	a.sendStreamClose(exec)
}

func (a *Attempt) ensureID(id *string) string {
	if *id == "" {
		*id = a.NewUUID()
	}
	return *id
}

func processCwd() string {
	cwd, _ := os.Getwd()
	return cwd
}

func shellRejected(command, cwd, reason string) *ShellResult {
	return &ShellResult{Result: &ShellResult_Rejected{Rejected: &ShellRejected{Command: command, WorkingDirectory: cwd, Reason: reason}}}
}

func (a *Attempt) isEditOwned(id string) bool {
	if a.Turn.editOwned[id] {
		return true
	}
	block := a.Turn.hasToolCall(id)
	return block != nil && block.Name == "edit"
}

func (a *Attempt) handleExec(exec *ExecServerMessage) {
	turn := a.Turn
	switch m := exec.Message.(type) {
	case nil:
		a.sendThrow(exec, "Unknown exec message variant", "unknown_exec_variant")
	case *ExecServerMessage_RequestContextArgs:
		a.sendExec(exec, &ExecClientMessage_RequestContextResult{RequestContextResult: &RequestContextResult{
			Result: &RequestContextResult_Success{Success: &RequestContextSuccess{RequestContext: &RequestContext{Rules: a.Rules, Tools: a.Tools}}}}})
	case *ExecServerMessage_ReadArgs:
		args := orEmpty(m.ReadArgs)
		id := a.ensureID(&args.ToolCallId)
		if !a.isEditOwned(id) {
			turn.synthesize(id, "read", nil, true)
		}
		a.later(func() {
			a.sendExec(exec, &ExecClientMessage_ReadResult{ReadResult: &ReadResult{Result: &ReadResult_Rejected{Rejected: &ReadRejected{Path: args.Path, Reason: toolNotAvailable}}}})
		})
	case *ExecServerMessage_LsArgs:
		args := orEmpty(m.LsArgs)
		turn.synthesize(a.ensureID(&args.ToolCallId), "read", nil, true)
		a.later(func() {
			a.sendExec(exec, &ExecClientMessage_LsResult{LsResult: &LsResult{Result: &LsResult_Rejected{Rejected: &LsRejected{Path: args.Path, Reason: toolNotAvailable}}}})
		})
	case *ExecServerMessage_GrepArgs:
		args := orEmpty(m.GrepArgs)
		id := a.ensureID(&args.ToolCallId)
		glob := ""
		if args.Glob != nil {
			glob = *args.Glob
		}
		if rejection := emptyGrepPatternRejection(args.Pattern, glob); rejection != "" {
			a.sendExec(exec, &ExecClientMessage_GrepResult{GrepResult: &GrepResult{Result: &GrepResult_Error{Error: &GrepError{Error: rejection}}}})
			return
		}
		turn.synthesize(id, "grep", nil, true)
		a.later(func() {
			a.sendExec(exec, &ExecClientMessage_GrepResult{GrepResult: &GrepResult{Result: &GrepResult_Error{Error: &GrepError{Error: toolNotAvailable}}}})
		})
	case *ExecServerMessage_WriteArgs:
		args := orEmpty(m.WriteArgs)
		id := a.ensureID(&args.ToolCallId)
		editOwned := a.isEditOwned(id)
		if !editOwned {
			turn.synthesize(id, "write", nil, true)
		}
		a.later(func() {
			if editOwned {
				turn.pairedEdit[id] = true
			}
			a.sendExec(exec, &ExecClientMessage_WriteResult{WriteResult: &WriteResult{Result: &WriteResult_Rejected{Rejected: &WriteRejected{Path: args.Path, Reason: toolNotAvailable}}}})
		})
	case *ExecServerMessage_DeleteArgs:
		args := orEmpty(m.DeleteArgs)
		turn.synthesize(a.ensureID(&args.ToolCallId), "delete", nil, true)
		a.later(func() {
			a.sendExec(exec, &ExecClientMessage_DeleteResult{DeleteResult: &DeleteResult{Result: &DeleteResult_Rejected{Rejected: &DeleteRejected{Path: args.Path, Reason: toolNotAvailable}}}})
		})
	case *ExecServerMessage_ShellArgs, *ExecServerMessage_MiniSweAgentBashArgs:
		var args *ShellArgs
		mini := false
		if shell, ok := m.(*ExecServerMessage_ShellArgs); ok {
			args = orEmpty(shell.ShellArgs)
		} else {
			args = orEmpty(m.(*ExecServerMessage_MiniSweAgentBashArgs).MiniSweAgentBashArgs)
			mini = true
		}
		cwd := args.WorkingDirectory
		if cwd == "" {
			cwd = processCwd()
		}
		turn.synthesize(a.ensureID(&args.ToolCallId), "bash", nil, true)
		a.later(func() {
			result := shellRejected(args.Command, cwd, toolNotAvailable)
			if mini {
				a.sendExec(exec, &ExecClientMessage_MiniSweAgentBashResult{MiniSweAgentBashResult: result})
			} else {
				a.sendExec(exec, &ExecClientMessage_ShellResult{ShellResult: result})
			}
		})
	case *ExecServerMessage_ShellStreamArgs:
		args := orEmpty(m.ShellStreamArgs)
		turn.synthesize(a.ensureID(&args.ToolCallId), "bash", nil, true)
		cwd := args.WorkingDirectory
		if cwd == "" {
			cwd = processCwd()
		}
		a.sendExec(exec, &ExecClientMessage_ShellStream{ShellStream: &ShellStream{Event: &ShellStream_Start{Start: &ShellStreamStart{}}}})
		a.later(func() {
			rejected := &ShellRejected{Command: args.Command, WorkingDirectory: cwd, Reason: toolNotAvailable}
			a.sendExec(exec, &ExecClientMessage_ShellStream{ShellStream: &ShellStream{Event: &ShellStream_Rejected{Rejected: rejected}}})
			a.sendExec(exec, &ExecClientMessage_ShellStream{ShellStream: &ShellStream{Event: &ShellStream_Exit{Exit: &ShellStreamExit{Code: 1, Cwd: cwd}}}})
			a.sendExec(exec, &ExecClientMessage_ShellResult{ShellResult: &ShellResult{Result: &ShellResult_Rejected{Rejected: rejected}}})
			a.sendStreamClose(exec)
		})
	case *ExecServerMessage_BackgroundShellSpawnArgs:
		args := orEmpty(m.BackgroundShellSpawnArgs)
		a.sendExec(exec, &ExecClientMessage_BackgroundShellSpawnResult{BackgroundShellSpawnResult: &BackgroundShellSpawnResult{
			Result: &BackgroundShellSpawnResult_Rejected{Rejected: &ShellRejected{Command: args.Command, WorkingDirectory: args.WorkingDirectory, Reason: "Not implemented"}}}})
	case *ExecServerMessage_WriteShellStdinArgs:
		a.sendExec(exec, &ExecClientMessage_WriteShellStdinResult{WriteShellStdinResult: &WriteShellStdinResult{
			Result: &WriteShellStdinResult_Error{Error: &WriteShellStdinError{Error: "Not implemented"}}}})
	case *ExecServerMessage_FetchArgs:
		args := orEmpty(m.FetchArgs)
		a.sendExec(exec, &ExecClientMessage_FetchResult{FetchResult: &FetchResult{Result: &FetchResult_Error{Error: &FetchError{Url: args.Url, Error: "Not implemented"}}}})
	case *ExecServerMessage_DiagnosticsArgs:
		args := orEmpty(m.DiagnosticsArgs)
		turn.synthesize(a.ensureID(&args.ToolCallId), "lsp", nil, true)
		a.later(func() {
			a.sendExec(exec, &ExecClientMessage_DiagnosticsResult{DiagnosticsResult: &DiagnosticsResult{
				Result: &DiagnosticsResult_Rejected{Rejected: &DiagnosticsRejected{Path: args.Path, Reason: toolNotAvailable}}}})
		})
	case *ExecServerMessage_McpArgs:
		a.handleMcp(exec, orEmpty(m.McpArgs))
	case *ExecServerMessage_ListMcpResourcesExecArgs:
		a.laterAt(1, func() {
			a.sendExec(exec, &ExecClientMessage_ListMcpResourcesExecResult{ListMcpResourcesExecResult: &ListMcpResourcesExecResult{
				Result: &ListMcpResourcesExecResult_Success{Success: &ListMcpResourcesSuccess{}}}})
		})
	case *ExecServerMessage_ReadMcpResourceExecArgs:
		args := orEmpty(m.ReadMcpResourceExecArgs)
		a.laterAt(1, func() {
			a.sendExec(exec, &ExecClientMessage_ReadMcpResourceExecResult{ReadMcpResourceExecResult: &ReadMcpResourceExecResult{
				Result: &ReadMcpResourceExecResult_NotFound{NotFound: &ReadMcpResourceNotFound{Uri: args.Uri}}}})
		})
	case *ExecServerMessage_RecordScreenArgs:
		a.sendExec(exec, &ExecClientMessage_RecordScreenResult{RecordScreenResult: &RecordScreenResult{
			Result: &RecordScreenResult_Failure{Failure: &RecordScreenFailure{Error: "Not implemented by this client"}}}})
	case *ExecServerMessage_ComputerUseArgs:
		a.sendExec(exec, &ExecClientMessage_ComputerUseResult{ComputerUseResult: &ComputerUseResult{
			Result: &ComputerUseResult_Error{Error: &ComputerUseError{Error: "Not implemented by this client"}}}})
	case *ExecServerMessage_PiReadArgs:
		turn.synthesize(a.NewUUID(), "read", nil, true)
		a.later(func() {
			a.sendExec(exec, &ExecClientMessage_PiReadResult{PiReadResult: &PiReadExecResult{Result: &PiReadExecResult_Error{Error: &PiReadExecError{Error: toolNotAvailable}}}})
		})
	case *ExecServerMessage_PiBashArgs:
		turn.synthesize(a.NewUUID(), "bash", nil, true)
		a.later(func() {
			a.sendExec(exec, &ExecClientMessage_PiBashResult{PiBashResult: &PiBashExecResult{Result: &PiBashExecResult_Error{Error: &PiBashExecError{Error: toolNotAvailable}}}})
		})
	case *ExecServerMessage_PiEditArgs:
		turn.synthesize(a.NewUUID(), "edit", nil, true)
		a.later(func() {
			a.sendExec(exec, &ExecClientMessage_PiEditResult{PiEditResult: &PiEditExecResult{Result: &PiEditExecResult_Rejected{Rejected: &PiEditExecRejected{Reason: toolNotAvailable}}}})
		})
	case *ExecServerMessage_PiWriteArgs:
		turn.synthesize(a.NewUUID(), "write", nil, true)
		a.later(func() {
			a.sendExec(exec, &ExecClientMessage_PiWriteResult{PiWriteResult: &PiWriteExecResult{Result: &PiWriteExecResult_Rejected{Rejected: &PiWriteExecRejected{Reason: toolNotAvailable}}}})
		})
	case *ExecServerMessage_PiGrepArgs:
		turn.synthesize(a.NewUUID(), "grep", nil, true)
		a.later(func() {
			a.sendExec(exec, &ExecClientMessage_PiGrepResult{PiGrepResult: &PiGrepExecResult{Result: &PiGrepExecResult_Error{Error: &PiGrepExecError{Error: toolNotAvailable}}}})
		})
	case *ExecServerMessage_PiFindArgs:
		turn.synthesize(a.NewUUID(), "glob", nil, true)
		a.later(func() {
			a.sendExec(exec, &ExecClientMessage_PiFindResult{PiFindResult: &PiFindExecResult{Result: &PiFindExecResult_Error{Error: &PiFindExecError{Error: toolNotAvailable}}}})
		})
	case *ExecServerMessage_PiLsArgs:
		turn.synthesize(a.NewUUID(), "read", nil, true)
		a.later(func() {
			a.sendExec(exec, &ExecClientMessage_PiLsResult{PiLsResult: &PiLsExecResult{Result: &PiLsExecResult_Error{Error: &PiLsExecError{Error: toolNotAvailable}}}})
		})
	case *ExecServerMessage_RedactedReadArgs:
		args := orEmpty(m.RedactedReadArgs)
		a.sendExec(exec, &ExecClientMessage_RedactedReadResult{RedactedReadResult: &ReadResult{Result: &ReadResult_Error{Error: &ReadError{Path: args.Path,
			Error: "Secret redaction is not implemented by this client"}}}})
	case *ExecServerMessage_McpStateExecArgs:
		args := orEmpty(m.McpStateExecArgs)
		a.sendExec(exec, &ExecClientMessage_McpStateExecResult{McpStateExecResult: mcpStateResult(a.Tools, args.ServerIdentifiers)})
	case *ExecServerMessage_ExecuteHookArgs:
		args := orEmpty(m.ExecuteHookArgs)
		result, caseName := neutralHookResult(args.Request)
		if result == nil {
			a.sendThrow(exec, "Unsupported hook request: "+caseName, "unknown_hook_request")
			return
		}
		a.sendExec(exec, &ExecClientMessage_ExecuteHookResult{ExecuteHookResult: result})
	case *ExecServerMessage_SubagentArgs:
		a.sendExec(exec, &ExecClientMessage_SubagentResult{SubagentResult: &SubagentResult{
			Result: &SubagentResult_Error{Error: &SubagentError{Error: "Subagents are " + notImplementedSuffix}}}})
	case *ExecServerMessage_SubagentAwaitArgs:
		args := orEmpty(m.SubagentAwaitArgs)
		a.sendExec(exec, &ExecClientMessage_SubagentAwaitResult{SubagentAwaitResult: &SubagentAwaitResult{
			Result: &SubagentAwaitResult_NotFound{NotFound: &SubagentAwaitNotFound{AgentId: args.AgentId}}}})
	case *ExecServerMessage_ForceBackgroundShellArgs:
		a.sendExec(exec, &ExecClientMessage_ForceBackgroundShellResult{ForceBackgroundShellResult: &ForceBackgroundShellResult{Status: 2}})
	case *ExecServerMessage_ForceBackgroundSubagentArgs:
		a.sendExec(exec, &ExecClientMessage_ForceBackgroundSubagentResult{ForceBackgroundSubagentResult: &ForceBackgroundSubagentResult{Status: 2}})
	case *ExecServerMessage_SmartModeClassifierArgs:
		a.sendExec(exec, &ExecClientMessage_SmartModeClassifierResult{SmartModeClassifierResult: &SmartModeClassifierResult{
			Result: &SmartModeClassifierResult_Error{Error: &SmartModeClassifierError{Error: "Smart-mode classification is " + notImplementedSuffix}}}})
	case *ExecServerMessage_CanvasDiagnosticsArgs:
		args := orEmpty(m.CanvasDiagnosticsArgs)
		a.sendExec(exec, &ExecClientMessage_CanvasDiagnosticsResult{CanvasDiagnosticsResult: &CanvasDiagnosticsResult{
			Result: &CanvasDiagnosticsResult_Error{Error: &CanvasDiagnosticsError{Path: args.Path, Error: "Canvas diagnostics are " + notImplementedSuffix}}}})
	case *ExecServerMessage_ShellAllowlistPrecheckArgs:
		a.sendExec(exec, &ExecClientMessage_ShellAllowlistPrecheckResult{ShellAllowlistPrecheckResult: &ShellAllowlistPrecheckResult{}})
	case *ExecServerMessage_McpAllowlistPrecheckArgs:
		a.sendExec(exec, &ExecClientMessage_McpAllowlistPrecheckResult{McpAllowlistPrecheckResult: &McpAllowlistPrecheckResult{}})
	case *ExecServerMessage_WebFetchAllowlistPrecheckArgs:
		a.sendExec(exec, &ExecClientMessage_WebFetchAllowlistPrecheckResult{WebFetchAllowlistPrecheckResult: &WebFetchAllowlistPrecheckResult{}})
	case *ExecServerMessage_ConversationSearchArgs:
		args := orEmpty(m.ConversationSearchArgs)
		id := args.ToolCallId
		if id == "" {
			id = a.NewUUID()
		}
		message := "Conversation search is " + notImplementedSuffix
		turn.synthesize(id, "search_conversations", nil, true)
		a.laterAt(1, func() {
			a.sendExec(exec, &ExecClientMessage_ConversationSearchResult{ConversationSearchResult: &ConversationSearchResult{
				Result: &ConversationSearchResult_Error{Error: &ConversationSearchError{Error: message}}}})
		})
	case *ExecServerMessage_AgentStoreConflictArgs:
		a.sendExec(exec, &ExecClientMessage_AgentStoreConflictResult{AgentStoreConflictResult: &AgentStoreConflictResult{
			Result: &AgentStoreConflictResult_Error{Error: &AgentStoreConflictError{Error: "Agent store conflicts are " + notImplementedSuffix}}}})
	case *ExecServerMessage_GitDiffRequest:
		a.sendThrow(exec, "Git diff is "+notImplementedSuffix, "exec_variant_unsupported")
	default:
		a.sendThrow(exec, fmt.Sprintf("No handler for exec message of type %s", execCaseName(exec)), "exec_variant_unsupported")
	}
}

func orEmpty[T any](value *T) *T {
	if value == nil {
		return new(T)
	}
	return value
}

var execCaseWrapper = regexp.MustCompile(`ExecServerMessage_(\w+)$`)

// execCaseName is protobuf-es's camelCase case name for an exec variant.
func execCaseName(exec *ExecServerMessage) string {
	name := fmt.Sprintf("%T", exec.Message)
	if match := execCaseWrapper.FindStringSubmatch(name); match != nil {
		return strings.ToLower(match[1][:1]) + match[1][1:]
	}
	return name
}

// emptyGrepPatternRejection is emptyGrepPatternRejection; "" when the
// pattern is usable.
func emptyGrepPatternRejection(pattern, glob string) string {
	if pattern != "" && jsTrim(pattern) != "" {
		return ""
	}
	if glob != "" {
		return `grep pattern is required (received an empty pattern). To list files matching "` + glob +
			`", pass a non-empty regex (e.g. ".") and set path to that glob, or use the ls/read tool instead.`
	}
	return "grep pattern is required (received an empty pattern)."
}

func (a *Attempt) handleMcp(exec *ExecServerMessage, args *McpArgs) {
	toolName := args.ToolName
	if toolName == "" {
		toolName = args.Name
	}
	decoded := decodeMcpArgsMap(args.Args)
	if args.SmartModeApprovalOnly {
		a.laterAt(1, func() {
			a.sendExec(exec, &ExecClientMessage_McpResult{McpResult: &McpResult{Result: &McpResult_Rejected{Rejected: &McpRejected{
				Reason: `Tool "` + toolName + `" is not approved to run without asking.`}}}})
		})
		return
	}
	handoff := a.ExternalToolExecutor
	if handoff && a.Turn.hasToolCall(args.ToolCallId) == nil {
		a.Turn.synthesize(args.ToolCallId, toolName, decoded, false)
	}
	a.laterAt(2, func() {
		var result *McpResult
		if handoff {
			result = &McpResult{Result: &McpResult_Success{Success: &McpSuccess{Content: []*McpToolResultContentItem{{
				Content: &McpToolResultContentItem_Text{Text: &McpTextContent{Text: mcpExternalHandoffMessage}}}}}}}
		} else {
			result = &McpResult{Result: &McpResult_ToolNotFound{ToolNotFound: &McpToolNotFound{Name: toolName}}}
		}
		a.sendExec(exec, &ExecClientMessage_McpResult{McpResult: result})
	})
}

// mcpStateResult is buildMcpStateResult.
func mcpStateResult(tools []*McpToolDefinition, serverIdentifiers []string) *McpStateExecResult {
	var order []string
	byProvider := map[string][]*McpToolDefinition{}
	for _, tool := range tools {
		if _, ok := byProvider[tool.ProviderIdentifier]; !ok {
			order = append(order, tool.ProviderIdentifier)
		}
		byProvider[tool.ProviderIdentifier] = append(byProvider[tool.ProviderIdentifier], tool)
	}
	wanted := map[string]bool{}
	for _, id := range serverIdentifiers {
		wanted[id] = true
	}
	var servers []*McpStateServer
	for _, identifier := range order {
		if len(wanted) > 0 && !wanted[identifier] {
			continue
		}
		servers = append(servers, &McpStateServer{ServerName: identifier, ServerIdentifier: identifier, Tools: byProvider[identifier], Status: ptr("connected")})
	}
	return &McpStateExecResult{Result: &McpStateExecResult_Success{Success: &McpStateSuccess{Servers: servers}}}
}

// neutralHookResult is buildNeutralHookResult; nil with the request's case
// name for one it does not model.
func neutralHookResult(request *ExecuteHookRequest) (*ExecuteHookResult, string) {
	var response isExecuteHookResponse_Response
	switch request.GetRequest().(type) {
	case *ExecuteHookRequest_PreCompact:
		response = &ExecuteHookResponse_PreCompact{PreCompact: &PreCompactRequestResponse{}}
	case *ExecuteHookRequest_SubagentStart:
		response = &ExecuteHookResponse_SubagentStart{SubagentStart: &SubagentStartRequestResponse{}}
	case *ExecuteHookRequest_SubagentStop:
		response = &ExecuteHookResponse_SubagentStop{SubagentStop: &SubagentStopRequestResponse{}}
	case *ExecuteHookRequest_PreToolUse:
		response = &ExecuteHookResponse_PreToolUse{PreToolUse: &PreToolUseRequestResponse{}}
	case *ExecuteHookRequest_PostToolUse:
		response = &ExecuteHookResponse_PostToolUse{PostToolUse: &PostToolUseRequestResponse{}}
	case *ExecuteHookRequest_PostToolUseFailure:
		response = &ExecuteHookResponse_PostToolUseFailure{PostToolUseFailure: &PostToolUseFailureRequestResponse{}}
	case *ExecuteHookRequest_BeforeSubmitPrompt:
		response = &ExecuteHookResponse_BeforeSubmitPrompt{BeforeSubmitPrompt: &BeforeSubmitPromptRequestResponse{}}
	case *ExecuteHookRequest_AfterAgentResponse:
		response = &ExecuteHookResponse_AfterAgentResponse{AfterAgentResponse: &AfterAgentResponseRequestResponse{}}
	case *ExecuteHookRequest_AfterAgentThought:
		response = &ExecuteHookResponse_AfterAgentThought{AfterAgentThought: &AfterAgentThoughtRequestResponse{}}
	case *ExecuteHookRequest_Stop:
		response = &ExecuteHookResponse_Stop{Stop: &StopRequestResponse{}}
	case nil:
		return nil, "unset"
	default:
		return nil, "unknown"
	}
	return &ExecuteHookResult{Response: &ExecuteHookResponse{Response: response}}, ""
}

// GetRequest returns the selected hook request variant (nil-safe).
func (m *ExecuteHookRequest) GetRequest() isExecuteHookRequest_Request {
	if m == nil {
		return nil
	}
	return m.Request
}

// HeartbeatFrame is the framed ClientHeartbeat.
func HeartbeatFrame() []byte {
	message := &AgentClientMessage{Message: &AgentClientMessage_ClientHeartbeat{ClientHeartbeat: &ClientHeartbeat{}}}
	return Frame(message.Marshal(), 0)
}
