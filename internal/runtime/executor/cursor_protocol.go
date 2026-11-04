package executor

// Cursor agent protocol (agent.v1.AgentService/Run): Connect-framed protobuf
// over one bidirectional HTTP/2 stream. Field numbers follow Cursor's
// agent.proto as mirrored by NeoPi (packages/ai/src/providers/cursor/proto).
// Only the messages NeoProxy sends or reads are expressed here.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// ---- Connect envelope framing ----

const (
	cursorFlagCompressed = 0x01
	cursorFlagEndStream  = 0x02
	cursorMaxFrameBytes  = 64 << 20
)

// cursorFrame wraps one message in a Connect envelope: flags, big-endian length, payload.
func cursorFrame(msg []byte) []byte {
	out := make([]byte, 5+len(msg))
	binary.BigEndian.PutUint32(out[1:5], uint32(len(msg)))
	copy(out[5:], msg)
	return out
}

type cursorFrameReader struct{ r *bufio.Reader }

func newCursorFrameReader(r io.Reader) *cursorFrameReader {
	return &cursorFrameReader{r: bufio.NewReaderSize(r, 64<<10)}
}

// next returns the next envelope; io.EOF only on a clean boundary.
func (fr *cursorFrameReader) next() (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(fr.r, header[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, errors.New("cursor: stream cut inside a frame header")
		}
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(header[1:5])
	if n > cursorMaxFrameBytes {
		return 0, nil, fmt.Errorf("cursor: frame of %d bytes exceeds limit", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(fr.r, payload); err != nil {
		return 0, nil, errors.New("cursor: stream cut inside a frame")
	}
	if header[0]&cursorFlagCompressed != 0 {
		return 0, nil, errors.New("cursor: compressed frames are not negotiated")
	}
	return header[0], payload, nil
}

// ---- protobuf encoding ----

type cursorPB []byte

func (b cursorPB) str(n protowire.Number, s string) cursorPB {
	if s == "" {
		return b
	}
	b = protowire.AppendTag(b, n, protowire.BytesType)
	return protowire.AppendString(b, s)
}

// raw always emits a length-delimited field (bytes, or an explicitly present message).
func (b cursorPB) raw(n protowire.Number, v []byte) cursorPB {
	b = protowire.AppendTag(b, n, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func (b cursorPB) msg(n protowire.Number, m cursorPB) cursorPB { return b.raw(n, m) }

func (b cursorPB) uvarint(n protowire.Number, v uint64) cursorPB {
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, n, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func (b cursorPB) flag(n protowire.Number, v bool) cursorPB {
	if !v {
		return b
	}
	return b.uvarint(n, 1)
}

// ---- protobuf decoding ----

type cursorField struct {
	num    protowire.Number
	typ    protowire.Type
	bytes  []byte
	varint uint64
}

type cursorMsg []cursorField

func cursorParse(b []byte) (cursorMsg, error) {
	var out cursorMsg
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]
		f := cursorField{num: num, typ: typ}
		switch typ {
		case protowire.VarintType:
			f.varint, n = protowire.ConsumeVarint(b)
		case protowire.BytesType:
			f.bytes, n = protowire.ConsumeBytes(b)
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
		}
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]
		out = append(out, f)
	}
	return out, nil
}

func (m cursorMsg) field(n protowire.Number) (cursorField, bool) {
	for i := len(m) - 1; i >= 0; i-- {
		if m[i].num == n {
			return m[i], true
		}
	}
	return cursorField{}, false
}

func (m cursorMsg) str(n protowire.Number) string {
	f, _ := m.field(n)
	return string(f.bytes)
}

func (m cursorMsg) uint(n protowire.Number) uint64 {
	f, _ := m.field(n)
	return f.varint
}

func (m cursorMsg) sub(n protowire.Number) (cursorMsg, bool) {
	f, ok := m.field(n)
	if !ok || f.typ != protowire.BytesType {
		return nil, false
	}
	parsed, err := cursorParse(f.bytes)
	if err != nil {
		return nil, false
	}
	return parsed, true
}

// oneofCase returns the first length-delimited field outside skip: the set
// variant of a message whose remaining fields are scalars.
func (m cursorMsg) oneofCase(skip ...protowire.Number) (protowire.Number, cursorMsg, bool) {
outer:
	for _, f := range m {
		if f.typ != protowire.BytesType {
			continue
		}
		for _, s := range skip {
			if f.num == s {
				continue outer
			}
		}
		parsed, err := cursorParse(f.bytes)
		if err != nil {
			return f.num, nil, true
		}
		return f.num, parsed, true
	}
	return 0, nil, false
}

// ---- google.protobuf.Value (MCP arguments and tool schemas) ----

func cursorEncodeValue(v any) []byte {
	value, err := structpb.NewValue(cursorJSONNormalize(v))
	if err != nil {
		value = structpb.NewNullValue()
	}
	out, err := proto.MarshalOptions{Deterministic: true}.Marshal(value)
	if err != nil {
		return nil
	}
	return out
}

// cursorJSONNormalize round-trips through JSON so every value is one structpb accepts.
func cursorJSONNormalize(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// cursorDecodeArgValue mirrors NeoPi's decodeMcpArgValue: a protobuf Value,
// with JSON-looking strings parsed; otherwise the bytes as JSON text.
func cursorDecodeArgValue(b []byte) any {
	var value structpb.Value
	if err := proto.Unmarshal(b, &value); err == nil && value.Kind != nil {
		decoded := value.AsInterface()
		if s, ok := decoded.(string); ok {
			trimmed := strings.TrimLeft(s, " \t\r\n")
			if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, `"`) {
				var parsed any
				if json.Unmarshal([]byte(strings.TrimSpace(s)), &parsed) == nil {
					return parsed
				}
			}
		}
		return decoded
	}
	text := string(b)
	var parsed any
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &parsed) == nil {
		return parsed
	}
	return text
}

// ---- conversation model (from an OpenAI chat completions request) ----

type cursorImage struct {
	mime string
	data []byte
}

type cursorToolCall struct {
	id   string
	name string
	args map[string]any
}

type cursorMessage struct {
	role      string // user, assistant, tool
	text      string
	images    []cursorImage
	toolCalls []cursorToolCall
	// tool results
	toolCallID string
	toolName   string
	isError    bool
}

type cursorToolDef struct {
	name        string
	description string
	schema      any
}

type cursorConversation struct {
	systemPrompts []string
	messages      []cursorMessage
	tools         []cursorToolDef
}

var cursorDataURL = regexp.MustCompile(`^data:([^;,]+);base64,(.*)$`)

// parseCursorChatRequest reads the translated OpenAI chat completions body.
func parseCursorChatRequest(body []byte) (*cursorConversation, error) {
	if !gjson.ValidBytes(body) {
		return nil, errors.New("cursor: request body is not JSON")
	}
	conv := &cursorConversation{}
	toolNames := map[string]string{}
	for _, m := range gjson.GetBytes(body, "messages").Array() {
		role := m.Get("role").String()
		switch role {
		case "system", "developer":
			if text := strings.TrimSpace(cursorContentText(m.Get("content"))); text != "" {
				conv.systemPrompts = append(conv.systemPrompts, text)
			}
		case "user":
			text, images := cursorContentParts(m.Get("content"))
			conv.messages = append(conv.messages, cursorMessage{role: "user", text: text, images: images})
		case "assistant":
			msg := cursorMessage{role: "assistant", text: cursorContentText(m.Get("content"))}
			for _, tc := range m.Get("tool_calls").Array() {
				call := cursorToolCall{id: tc.Get("id").String(), name: tc.Get("function.name").String(), args: map[string]any{}}
				if raw := strings.TrimSpace(tc.Get("function.arguments").String()); raw != "" {
					var parsed map[string]any
					if json.Unmarshal([]byte(raw), &parsed) == nil && parsed != nil {
						call.args = parsed
					}
				}
				if call.id == "" {
					call.id = "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")
				}
				toolNames[call.id] = call.name
				msg.toolCalls = append(msg.toolCalls, call)
			}
			conv.messages = append(conv.messages, msg)
		case "tool", "function":
			id := m.Get("tool_call_id").String()
			name := toolNames[id]
			if name == "" {
				name = m.Get("name").String()
			}
			conv.messages = append(conv.messages, cursorMessage{role: "tool", text: cursorContentText(m.Get("content")), toolCallID: id, toolName: name})
		}
	}
	for _, t := range gjson.GetBytes(body, "tools").Array() {
		fn := t.Get("function")
		if !fn.Exists() || fn.Get("name").String() == "" {
			continue
		}
		var schema any = map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
		if p := fn.Get("parameters"); p.Exists() && p.IsObject() {
			var parsed any
			if json.Unmarshal([]byte(p.Raw), &parsed) == nil {
				schema = parsed
			}
		}
		conv.tools = append(conv.tools, cursorToolDef{name: fn.Get("name").String(), description: fn.Get("description").String(), schema: schema})
	}
	return conv, nil
}

func cursorContentText(content gjson.Result) string {
	text, _ := cursorContentParts(content)
	return text
}

func cursorContentParts(content gjson.Result) (string, []cursorImage) {
	if content.Type == gjson.String {
		return content.String(), nil
	}
	var texts []string
	var images []cursorImage
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "text", "input_text", "output_text":
			texts = append(texts, part.Get("text").String())
		case "image_url", "input_image":
			url := part.Get("image_url.url").String()
			if url == "" {
				url = part.Get("image_url").String()
			}
			if sub := cursorDataURL.FindStringSubmatch(url); sub != nil {
				if data, err := base64.StdEncoding.DecodeString(sub[2]); err == nil {
					images = append(images, cursorImage{mime: sub[1], data: data})
					continue
				}
			}
			if url != "" {
				texts = append(texts, "[image: "+url+"]")
			}
		}
	}
	return strings.Join(texts, "\n"), images
}

// ---- request building ----

const cursorMcpProvider = "neoproxy"

// cursorBlobs is the per-run KV store the server reads history blobs from.
type cursorBlobs map[string][]byte

func (b cursorBlobs) put(data []byte) []byte {
	sum := sha256.Sum256(data)
	b[hex.EncodeToString(sum[:])] = data
	return sum[:]
}

var cursorToolCallIDUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// cursorToolCallID matches Cursor's tool-call id charset (^[a-zA-Z0-9_-]+$, ≤64).
func cursorToolCallID(id string) string {
	id = cursorToolCallIDUnsafe.ReplaceAllString(id, "_")
	if len(id) > 64 {
		id = id[:64]
	}
	return id
}

func cursorJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

type cursorPromptPart = map[string]any

func cursorSystemPromptJSONs(prompts []string) [][]byte {
	if len(prompts) == 0 {
		return [][]byte{cursorJSON(map[string]any{"role": "system", "content": "You are a helpful assistant."})}
	}
	out := make([][]byte, 0, len(prompts))
	for _, p := range prompts {
		out = append(out, cursorJSON(map[string]any{"role": "system", "content": p}))
	}
	return out
}

func cursorUserPromptContent(m cursorMessage) []cursorPromptPart {
	var parts []cursorPromptPart
	if text := strings.TrimSpace(m.text); text != "" {
		parts = append(parts, cursorPromptPart{"type": "text", "text": text})
	}
	for _, img := range m.images {
		parts = append(parts, cursorPromptPart{"type": "image", "image": "data:" + img.mime + ";base64," + base64.StdEncoding.EncodeToString(img.data), "mediaType": img.mime})
	}
	return parts
}

func cursorOrphanResultText(m cursorMessage) string {
	prefix := "[Tool Result]"
	if m.isError {
		prefix = "[Tool Error]"
	}
	text := m.text
	if text == "" {
		text = "(empty result)"
	}
	return prefix + "\n" + text
}

// cursorHistory holds the history fields of ConversationStateStructure.
type cursorHistory struct {
	rootPromptMessages [][]byte // blob ids
	turns              [][]byte // blob ids
}

// buildCursorHistory ports NeoPi's buildRootPromptMessagesJson and
// buildConversationTurns: Cursor builds the model prompt from the root prompt
// messages (Vercel-AI-SDK-shaped JSON) and keeps turns as structured records.
// historyEnd excludes the active user message, which rides in the action.
func buildCursorHistory(conv *cursorConversation, blobs cursorBlobs, historyEnd int) cursorHistory {
	var h cursorHistory
	for _, sys := range cursorSystemPromptJSONs(conv.systemPrompts) {
		h.rootPromptMessages = append(h.rootPromptMessages, blobs.put(sys))
	}
	paired := map[string]bool{}
	results := map[string]cursorMessage{}
	for i := 0; i < historyEnd; i++ {
		m := conv.messages[i]
		switch m.role {
		case "assistant":
			for _, tc := range m.toolCalls {
				paired[tc.id] = true
			}
		case "tool":
			results[m.toolCallID] = m
		}
	}
	push := func(v any) { h.rootPromptMessages = append(h.rootPromptMessages, blobs.put(cursorJSON(v))) }
	for i := 0; i < historyEnd; i++ {
		m := conv.messages[i]
		switch m.role {
		case "user":
			if content := cursorUserPromptContent(m); len(content) > 0 {
				push(map[string]any{"role": "user", "content": content})
			}
		case "assistant":
			var content []cursorPromptPart
			if m.text != "" {
				content = append(content, cursorPromptPart{"type": "text", "text": m.text})
			}
			for _, tc := range m.toolCalls {
				content = append(content, cursorPromptPart{"type": "tool-call", "toolCallId": cursorToolCallID(tc.id), "toolName": tc.name, "args": tc.args})
			}
			if len(content) > 0 {
				push(map[string]any{"role": "assistant", "content": content})
			}
		case "tool":
			if !paired[m.toolCallID] {
				push(map[string]any{"role": "assistant", "content": []cursorPromptPart{{"type": "text", "text": cursorOrphanResultText(m)}}})
				continue
			}
			id := cursorToolCallID(m.toolCallID)
			result := cursorPromptPart{"type": "tool-result", "toolName": m.toolName, "toolCallId": id, "result": m.text}
			if m.isError {
				result["isError"] = true
			}
			push(map[string]any{"role": "tool", "id": id, "content": []cursorPromptPart{result}})
		}
	}

	for i := 0; i < len(conv.messages); {
		m := conv.messages[i]
		if m.role != "user" {
			i++
			continue
		}
		if i == historyEnd {
			break
		}
		if strings.TrimSpace(m.text) == "" && len(m.images) == 0 {
			i++
			continue
		}
		key := sha256.Sum256([]byte(m.text))
		messageID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("u:%d:%x", len(h.turns), key))).String()
		userBlob := blobs.put(cursorUserMessage(m, messageID))
		var steps [][]byte
		for i++; i < len(conv.messages) && conv.messages[i].role != "user"; i++ {
			sm := conv.messages[i]
			switch sm.role {
			case "assistant":
				if sm.text != "" {
					steps = append(steps, blobs.put(cursorPB(nil).msg(1, cursorPB(nil).str(1, sm.text))))
				}
				for _, tc := range sm.toolCalls {
					var result *cursorMessage
					if r, ok := results[tc.id]; ok {
						result = &r
					}
					steps = append(steps, blobs.put(cursorPB(nil).msg(2, cursorToolCallStep(tc, result))))
				}
			case "tool":
				if !paired[sm.toolCallID] {
					steps = append(steps, blobs.put(cursorPB(nil).msg(1, cursorPB(nil).str(1, cursorOrphanResultText(sm)))))
				}
			}
		}
		turn := cursorPB(nil).raw(1, userBlob)
		for _, s := range steps {
			turn = turn.raw(2, s)
		}
		h.turns = append(h.turns, blobs.put(cursorPB(nil).msg(1, turn)))
	}
	return h
}

// cursorUserMessage encodes UserMessage{text=1, message_id=2, selected_context=3{selected_images=1}}.
func cursorUserMessage(m cursorMessage, messageID string) cursorPB {
	out := cursorPB(nil).str(1, strings.TrimSpace(m.text)).str(2, messageID)
	if len(m.images) > 0 {
		var ctx cursorPB
		for _, img := range m.images {
			// SelectedImage{uuid=2, mime_type=7, data=8}
			ctx = ctx.msg(1, cursorPB(nil).str(2, uuid.NewString()).str(7, img.mime).raw(8, img.data))
		}
		out = out.msg(3, ctx)
	}
	return out
}

// cursorMcpArgsMap encodes map<string, bytes> args=2 as repeated entries.
func cursorMcpArgsMap(b cursorPB, n protowire.Number, args map[string]any) cursorPB {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b = b.msg(n, cursorPB(nil).str(1, k).raw(2, cursorEncodeValue(args[k])))
	}
	return b
}

// cursorToolCallStep encodes ToolCall{mcp_tool_call=15{args=1, result=2}, tool_call_id=57}.
func cursorToolCallStep(tc cursorToolCall, result *cursorMessage) cursorPB {
	id := cursorToolCallID(tc.id)
	// McpArgs{name=1, args=2, tool_call_id=3, provider_identifier=4, tool_name=5}
	args := cursorPB(nil).str(1, tc.name)
	args = cursorMcpArgsMap(args, 2, tc.args)
	args = args.str(3, id).str(4, cursorMcpProvider).str(5, tc.name)
	call := cursorPB(nil).msg(1, args)
	if result != nil {
		var res cursorPB
		if result.isError {
			res = res.msg(2, cursorPB(nil).str(1, result.text)) // McpToolError{error=1}
		} else {
			// McpSuccess{content=1[McpToolResultContentItem{text=1{text=1}}]}
			res = res.msg(1, cursorPB(nil).msg(1, cursorPB(nil).msg(1, cursorPB(nil).str(1, result.text))))
		}
		call = call.msg(2, res)
	}
	return cursorPB(nil).msg(15, call).str(57, id)
}

// cursorWireModel is the paired model identity Cursor validates per run.
type cursorWireModel struct {
	modelID        string // RequestedModel.model_id
	detailsModelID string // ModelDetails.model_id
	params         [][2]string
	maxMode        bool
}

// cursorEffortSuffixes is the catalog's effort-suffix vocabulary, longest
// first so "-extra-high" wins over "-high"; "off" runs the base with no parameter.
var cursorEffortSuffixes = [][2]string{
	{"-extra-high", "xhigh"}, {"-minimal", "minimal"}, {"-medium", "medium"}, {"-xhigh", "xhigh"},
	{"-none", "off"}, {"-high", "high"}, {"-low", "low"}, {"-max", "max"},
}

// resolveCursorWireModel ports NeoPi's normalized legacy resolution: an OpenAI
// family sibling slug (gpt-<digit>…-<effort>[-fast]) runs as its base id (the
// -fast lane kept) with {reasoning: effort}; "-none" runs the base with no
// parameter. A bare composer-2.5 pins the Standard lane (fast=false). Other
// slugs pass through unchanged.
func resolveCursorWireModel(slug string, maxMode bool) cursorWireModel {
	w := cursorWireModel{modelID: slug, detailsModelID: slug, maxMode: maxMode}
	if slug == "composer-2.5" {
		w.params = [][2]string{{"fast", "false"}}
		return w
	}
	if !cursorIsOpenAIFamily(slug) {
		return w
	}
	lane := ""
	base := slug
	if strings.HasSuffix(base, "-fast") {
		lane = "-fast"
		base = strings.TrimSuffix(base, "-fast")
	}
	for _, s := range cursorEffortSuffixes {
		if !strings.HasSuffix(base, s[0]) {
			continue
		}
		stem := strings.TrimSuffix(base, s[0])
		if !cursorIsOpenAIFamily(stem) {
			return w
		}
		w.modelID = stem + lane
		if s[1] != "off" {
			w.params = [][2]string{{"reasoning", s[1]}}
		}
		return w
	}
	return w
}

// cursorIsOpenAIFamily is the catalog's family marker: "gpt-" followed by a digit.
func cursorIsOpenAIFamily(id string) bool {
	for i := strings.Index(id, "gpt-"); i >= 0; {
		if j := i + 4; j < len(id) && id[j] >= '0' && id[j] <= '9' {
			return true
		}
		next := strings.Index(id[i+1:], "gpt-")
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return false
}

// cursorRunRequest encodes AgentClientMessage{run_request=1}.
func cursorRunRequest(conv *cursorConversation, wire cursorWireModel, displayName, conversationID string, blobs cursorBlobs) []byte {
	last := len(conv.messages) - 1
	historyEnd := len(conv.messages)
	var action cursorPB
	if last >= 0 && conv.messages[last].role == "user" {
		historyEnd = last
		active := conv.messages[last]
		if strings.TrimSpace(active.text) != "" || len(active.images) > 0 {
			// ConversationAction{user_message_action=1{user_message=1}}
			action = cursorPB(nil).msg(1, cursorPB(nil).msg(1, cursorUserMessage(active, uuid.NewString())))
		}
	}
	if action == nil {
		action = cursorPB(nil).msg(2, nil) // resume_action
	}
	history := buildCursorHistory(conv, blobs, historyEnd)
	var state cursorPB
	for _, id := range history.rootPromptMessages {
		state = state.raw(1, id)
	}
	for _, id := range history.turns {
		state = state.raw(8, id)
	}
	// ModelDetails{model_id=1, display_model_id=3, display_name=4, max_mode=7}
	details := cursorPB(nil).str(1, wire.detailsModelID).str(3, wire.detailsModelID).str(4, displayName).flag(7, wire.maxMode)
	// RequestedModel{model_id=1, max_mode=2, parameters=3{id=1, value=2}}
	requested := cursorPB(nil).str(1, wire.modelID).flag(2, wire.maxMode)
	for _, p := range wire.params {
		requested = requested.msg(3, cursorPB(nil).str(1, p[0]).str(2, p[1]))
	}
	run := cursorPB(nil).msg(1, state).msg(2, action).msg(3, details).msg(9, requested).str(5, conversationID)
	return cursorPB(nil).msg(1, run)
}

// cursorRequestContext encodes RequestContext{rules=2, tools=7}: system prompts
// as global user rules and the client's tools as MCP tools.
func cursorRequestContext(conv *cursorConversation) cursorPB {
	var rc cursorPB
	for i, p := range conv.systemPrompts {
		// CursorRule{full_path=1, content=2, type=3{global=1{}}, source=4 (USER=2)}
		rule := cursorPB(nil).str(1, fmt.Sprintf("/neoproxy/system-prompt/%d.mdc", i)).str(2, p).msg(3, cursorPB(nil).msg(1, nil)).uvarint(4, 2)
		rc = rc.msg(2, rule)
	}
	for _, def := range cursorToolDefinitions(conv) {
		rc = rc.msg(7, def)
	}
	return rc
}

// cursorToolDefinitions encodes the client's tools as
// McpToolDefinition{name=1, description=2, input_schema=3, provider_identifier=4, tool_name=5}.
func cursorToolDefinitions(conv *cursorConversation) []cursorPB {
	out := make([]cursorPB, 0, len(conv.tools))
	for _, t := range conv.tools {
		out = append(out, cursorPB(nil).str(1, t.name).str(2, t.description).raw(3, cursorEncodeValue(t.schema)).str(4, cursorMcpProvider).str(5, t.name))
	}
	return out
}

// cursorMcpState answers an MCP state query (NeoPi's buildMcpStateResult):
// McpStateExecResult{success=1{servers=1[McpStateServer{server_name=1,
// server_identifier=2, tools=5, status=7}]}}, filtered to the requested servers.
func cursorMcpState(conv *cursorConversation, wanted []string) cursorPB {
	var success cursorPB
	include := len(wanted) == 0
	for _, w := range wanted {
		include = include || w == cursorMcpProvider
	}
	if include && len(conv.tools) > 0 {
		server := cursorPB(nil).str(1, cursorMcpProvider).str(2, cursorMcpProvider)
		for _, def := range cursorToolDefinitions(conv) {
			server = server.msg(5, def)
		}
		success = success.msg(1, server.str(7, "connected"))
	}
	return cursorPB(nil).msg(1, success)
}

// ---- client replies ----

func cursorClientMessage(n protowire.Number, body cursorPB) []byte {
	return cursorFrame(cursorPB(nil).msg(n, body))
}

func cursorHeartbeat() []byte { return cursorClientMessage(7, nil) }

// cursorExecReply encodes AgentClientMessage{exec_client_message=2{id=1, exec_id=15, <case>}}.
func cursorExecReply(id uint64, execID string, resultField protowire.Number, result cursorPB) []byte {
	msg := cursorPB(nil).uvarint(1, id).str(15, execID).msg(resultField, result)
	return cursorClientMessage(2, msg)
}

// cursorExecThrow is Cursor's own reply to an exec frame no handler claims:
// ExecClientControlMessage throw=2 then stream_close=1.
func cursorExecThrow(id uint64, message, code string) [][]byte {
	throw := cursorPB(nil).uvarint(1, id).str(2, message).str(4, code)
	return [][]byte{
		cursorClientMessage(5, cursorPB(nil).msg(2, throw)),
		cursorClientMessage(5, cursorPB(nil).msg(1, cursorPB(nil).uvarint(1, id))),
	}
}
