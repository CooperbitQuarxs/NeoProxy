package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/encoding/protowire"
)

var _ cliproxyauth.ProviderExecutor = (*CursorExecutor)(nil)

var cursorRefreshGroup singleflight.Group

// CursorModelPrefix namespaces Cursor's model slugs so they never shadow the
// same names served by other providers.
const CursorModelPrefix = "cursor/"

const (
	cursorHeartbeatInterval = 5 * time.Second
	// cursorHandoffMessage answers an MCP exec frame for a tool the API client
	// runs itself; its result arrives with the client's next request.
	cursorHandoffMessage  = "Tool call received and handed off to the external client for execution. Do not retry or call it again; end the turn. The result will be provided in the next request."
	cursorToolUnavailable = "Tool not available"
	cursorNotImplemented  = "not implemented by this client"
)

// CursorExecutor serves Cursor subscription models through Cursor's agent
// protocol (agent.v1.AgentService/Run). Requests arrive as OpenAI chat
// completions; the client's tools are offered to Cursor as MCP tools whose
// calls are handed back to the client.
type CursorExecutor struct {
	cfg *config.Config
}

// NewCursorExecutor constructs a Cursor executor.
func NewCursorExecutor(cfg *config.Config) *CursorExecutor { return &CursorExecutor{cfg: cfg} }

// Identifier returns the provider key "cursor".
func (e *CursorExecutor) Identifier() string { return "cursor" }

func cursorCreds(auth *cliproxyauth.Auth) (baseURL, token string) {
	baseURL = cursorauth.APIBaseURL
	if auth == nil {
		return baseURL, ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok {
			token = strings.TrimSpace(v)
		}
		if v, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(v) != "" {
			baseURL = strings.TrimRight(strings.TrimSpace(v), "/")
		}
	}
	if token == "" && auth.Attributes != nil {
		token = strings.TrimSpace(auth.Attributes["access_token"])
	}
	return baseURL, token
}

// PrepareRequest injects Cursor credentials into an outgoing request.
func (e *CursorExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, token := cursorCreds(auth)
	contentType := req.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/proto"
	}
	cursorauth.ClientHeaders(req.Header, token, contentType)
	return nil
}

// HttpRequest injects Cursor credentials and executes the request.
func (e *CursorExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("cursor executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

// Refresh renews the access token with the stored refresh token. Imported
// credentials are refresh-locked: NeoPi stays their only refresher.
func (e *CursorExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "cursor executor: auth is nil"}
	}
	if cliproxyauth.IsRefreshLocked(auth) {
		return nil, cliproxyauth.ErrRefreshLocked
	}
	refreshToken, _ := auth.Metadata["refresh_token"].(string)
	if strings.TrimSpace(refreshToken) == "" {
		return auth, nil
	}
	res, err, _ := cursorRefreshGroup.Do(refreshToken, func() (any, error) {
		return cursorauth.NewAuthWithProxyURL(e.cfg, auth.ProxyURL).Refresh(ctx, refreshToken)
	})
	if err != nil {
		if errors.Is(err, cursorauth.ErrSessionEnded) {
			return nil, statusErr{code: http.StatusUnauthorized, msg: err.Error()}
		}
		return nil, err
	}
	tokens := res.(*cursorauth.Tokens)
	if auth.Metadata == nil {
		auth.Metadata = map[string]any{}
	}
	auth.Metadata["type"] = "cursor"
	auth.Metadata["access_token"] = tokens.AccessToken
	auth.Metadata["refresh_token"] = tokens.RefreshToken
	auth.Metadata["expired"] = tokens.Expires.UTC().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	auth.LastRefreshedAt = time.Now()
	return auth, nil
}

// CountTokens estimates prompt tokens; Cursor has no counting endpoint.
func (e *CursorExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	promptTokens := int64(len(req.Payload) / 4)
	return cliproxyexecutor.Response{Payload: []byte(fmt.Sprintf(`{"total_tokens":%d,"input_tokens":%d}`, promptTokens, promptTokens))}, nil
}

type cursorPrepared struct {
	conv       *cursorConversation
	translated []byte
	original   []byte
	slug       string
	display    string
	maxMode    bool
	from       sdktranslator.Format
	respFormat sdktranslator.Format
}

func (e *CursorExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*cursorPrepared, error) {
	from := opts.SourceFormat
	to := sdktranslator.FormatOpenAI
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	slug := strings.TrimPrefix(baseModel, CursorModelPrefix)
	translated, err := helps.TranslateRequestReturningError(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), stream, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	conv, err := parseCursorChatRequest(translated)
	if err != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	if len(conv.messages) == 0 {
		return nil, statusErr{code: http.StatusBadRequest, msg: "cursor: request has no messages"}
	}
	p := &cursorPrepared{conv: conv, translated: translated, original: helps.ApplyPatchOriginalRequest(req, opts), slug: slug, display: slug, from: from, respFormat: cliproxyexecutor.ResponseFormatOrSource(opts)}
	if auth != nil {
		if m, ok := cursorauth.LookupModel(auth.ID, slug); ok {
			p.maxMode = m.MaxMode
			if m.DisplayName != "" {
				p.display = m.DisplayName
			}
		}
	}
	return p, nil
}

// ---- events and the OpenAI chat chunk builder ----

type cursorEventKind int

const (
	cursorEventText cursorEventKind = iota
	cursorEventReasoning
	cursorEventToolCall
)

type cursorEvent struct {
	kind     cursorEventKind
	text     string
	toolID   string
	toolName string
	toolArgs string
}

type cursorUsage struct {
	input, output, cacheRead, cacheWrite, reasoning int64
}

type cursorChatBuilder struct {
	id, model string
	created   int64
	started   bool
	toolIndex int
}

func newCursorChatBuilder(model string) *cursorChatBuilder {
	return &cursorChatBuilder{id: "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", ""), model: model, created: time.Now().Unix()}
}

func (b *cursorChatBuilder) chunk(delta map[string]any, finish any, usage map[string]any) []byte {
	if !b.started {
		delta["role"] = "assistant"
		b.started = true
	}
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": finish}
	out := map[string]any{"id": b.id, "object": "chat.completion.chunk", "created": b.created, "model": b.model, "choices": []any{choice}}
	if usage != nil {
		out["usage"] = usage
	}
	return cursorJSON(out)
}

func (b *cursorChatBuilder) event(ev cursorEvent) []byte {
	switch ev.kind {
	case cursorEventReasoning:
		return b.chunk(map[string]any{"reasoning_content": ev.text}, nil, nil)
	case cursorEventToolCall:
		call := map[string]any{"index": b.toolIndex, "id": ev.toolID, "type": "function", "function": map[string]any{"name": ev.toolName, "arguments": ev.toolArgs}}
		b.toolIndex++
		return b.chunk(map[string]any{"tool_calls": []any{call}}, nil, nil)
	default:
		return b.chunk(map[string]any{"content": ev.text}, nil, nil)
	}
}

func (b *cursorChatBuilder) finish(reason string) []byte {
	return b.chunk(map[string]any{}, reason, nil)
}

func (b *cursorChatBuilder) usageChunk(u cursorUsage) []byte {
	return cursorJSON(map[string]any{"id": b.id, "object": "chat.completion.chunk", "created": b.created, "model": b.model, "choices": []any{}, "usage": u.openAI()})
}

func (u cursorUsage) openAI() map[string]any {
	out := map[string]any{"prompt_tokens": u.input, "completion_tokens": u.output, "total_tokens": u.input + u.output}
	if u.cacheRead > 0 || u.cacheWrite > 0 {
		details := map[string]any{"cached_tokens": u.cacheRead}
		if u.cacheWrite > 0 {
			details["cache_write_tokens"] = u.cacheWrite
		}
		out["prompt_tokens_details"] = details
	}
	if u.reasoning > 0 {
		out["completion_tokens_details"] = map[string]any{"reasoning_tokens": u.reasoning}
	}
	return out
}

// ---- the run ----

type cursorRunResult struct {
	usage     cursorUsage
	toolCalls int
}

// cursorRun is one Run stream's protocol state.
type cursorRun struct {
	conv  *cursorConversation
	blobs cursorBlobs
	send  func([]byte)
	emit  func(cursorEvent) error

	emittedTools        map[string]bool
	toolCalls           int
	emitted             bool
	lastWasText         bool
	sawTurnEnded        bool
	stepCompletedLatest bool
	outputEstimate      int64
	usage               cursorUsage
	endErr              error
	// handedOff ends the response at the first checkpoint after a tool call
	// went to the client: the turn's remaining steps would only react to the
	// placeholder result, and the client sends the real one next request.
	handedOff   bool
	done        bool
	contextUsed int64
}

// handle processes one AgentServerMessage.
func (r *cursorRun) handle(payload []byte) error {
	msg, err := cursorParse(payload)
	if err != nil {
		return nil
	}
	num, body, ok := msg.oneofCase()
	if !ok {
		return nil
	}
	switch num {
	case 1:
		return r.interaction(body)
	case 2:
		return r.exec(body)
	case 3: // conversation_checkpoint_update{token_details=5{used_tokens=1}}
		if details, okDetails := body.sub(5); okDetails {
			r.contextUsed = int64(details.uint(1))
		}
		if r.handedOff {
			r.done = true
		}
	case 4:
		r.kv(body)
	case 7:
		r.interactionQuery(body)
	}
	return nil
}

func (r *cursorRun) interaction(update cursorMsg) error {
	num, body, ok := update.oneofCase()
	if !ok {
		return nil
	}
	if num != 13 && num != 17 { // heartbeat, step_completed
		r.stepCompletedLatest = false
	}
	switch num {
	case 1: // text_delta
		if text := body.str(1); text != "" {
			r.lastWasText = true
			return r.out(cursorEvent{kind: cursorEventText, text: text})
		}
	case 4: // thinking_delta
		if text := body.str(1); text != "" {
			return r.out(cursorEvent{kind: cursorEventReasoning, text: text})
		}
	case 8: // token_delta
		r.outputEstimate += int64(int32(body.uint(1)))
	case 14: // turn_ended
		r.sawTurnEnded = true
		r.usage = cursorUsage{
			input: int64(body.uint(1)), output: int64(body.uint(2)),
			cacheRead: int64(body.uint(3)), cacheWrite: int64(body.uint(4)), reasoning: int64(body.uint(5)),
		}
	case 17: // step_completed
		r.stepCompletedLatest = true
	}
	return nil
}

func (r *cursorRun) out(ev cursorEvent) error {
	r.emitted = true
	if ev.kind != cursorEventText {
		r.lastWasText = false
	}
	return r.emit(ev)
}

func (r *cursorRun) exec(m cursorMsg) error {
	id := m.uint(1)
	execID := m.str(15)
	num, args, ok := m.oneofCase(15, 19)
	if !ok {
		for _, frame := range cursorExecThrow(id, "Unknown exec message variant", "unknown_exec_variant") {
			r.send(frame)
		}
		return nil
	}
	reply := func(field protowire.Number, result cursorPB) { r.send(cursorExecReply(id, execID, field, result)) }
	pathRejected := func(variant protowire.Number) cursorPB {
		return cursorPB(nil).msg(variant, cursorPB(nil).str(1, args.str(1)).str(2, cursorToolUnavailable))
	}
	switch num {
	case 10: // request_context_args
		reply(10, cursorPB(nil).msg(1, cursorPB(nil).msg(1, cursorRequestContext(r.conv))))
	case 11: // mcp_args
		return r.mcp(args, reply)
	case 7, 29: // read, redacted read
		reply(num, pathRejected(3))
	case 8: // ls
		reply(8, pathRejected(3))
	case 9: // diagnostics
		reply(9, pathRejected(3))
	case 3: // write
		reply(3, pathRejected(6))
	case 4: // delete
		reply(4, pathRejected(6))
	case 5: // grep
		reply(5, cursorPB(nil).msg(2, cursorPB(nil).str(1, cursorToolUnavailable)))
	case 2, 52: // shell, mini-swe-agent bash
		field := protowire.Number(2)
		if num == 52 {
			field = 55
		}
		reply(field, cursorPB(nil).msg(4, cursorPB(nil).str(1, args.str(1)).str(2, args.str(2)).str(3, cursorToolUnavailable)))
	case 20: // fetch
		reply(20, cursorPB(nil).msg(2, cursorPB(nil).str(1, args.str(1)).str(2, cursorToolUnavailable)))
	case 17: // list_mcp_resources: an explicit empty success
		reply(17, cursorPB(nil).msg(1, nil))
	case 36: // mcp_state: the client's tools as one connected server
		var wanted []string
		for _, f := range args {
			if f.num == 1 && f.typ == protowire.BytesType {
				wanted = append(wanted, string(f.bytes))
			}
		}
		reply(36, cursorMcpState(r.conv, wanted))
	case 27: // execute_hook: no hooks run here, so the neutral response of the asked case
		hook, _ := args.sub(1)
		if kind, _, okKind := hook.oneofCase(); okKind {
			reply(27, cursorPB(nil).msg(1, cursorPB(nil).msg(kind, nil)))
			return nil
		}
		for _, frame := range cursorExecThrow(id, "Unsupported hook request", "unknown_hook_request") {
			r.send(frame)
		}
	case 41, 42, 43: // shell / MCP / web-fetch allowlist prechecks: never pre-approved
		reply(num, nil)
	default:
		for _, frame := range cursorExecThrow(id, fmt.Sprintf("No handler for exec message field %d", num), "exec_variant_unsupported") {
			r.send(frame)
		}
	}
	return nil
}

// mcp hands a call to one of the client's own tools back to the client: the
// call is emitted once and Cursor is told the result comes with the next request.
func (r *cursorRun) mcp(args cursorMsg, reply func(protowire.Number, cursorPB)) error {
	name := args.str(5)
	if name == "" {
		name = args.str(1)
	}
	if args.uint(7) != 0 { // smart_mode_approval_only: an approval probe, never an invocation
		reply(11, cursorPB(nil).msg(3, cursorPB(nil).str(1, fmt.Sprintf("Tool %q is not approved to run without asking.", name))))
		return nil
	}
	callID := args.str(3)
	if callID == "" {
		callID = "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	decoded := map[string]any{}
	for _, f := range args {
		if f.num != 2 || f.typ != protowire.BytesType {
			continue
		}
		entry, err := cursorParse(f.bytes)
		if err != nil {
			continue
		}
		key := entry.str(1)
		valueField, _ := entry.field(2)
		decoded[key] = cursorDecodeArgValue(valueField.bytes)
	}
	reply(11, cursorPB(nil).msg(1, cursorPB(nil).msg(1, cursorPB(nil).msg(1, cursorPB(nil).str(1, cursorHandoffMessage)))))
	if r.emittedTools[callID] {
		return nil
	}
	r.emittedTools[callID] = true
	r.toolCalls++
	r.handedOff = true
	return r.out(cursorEvent{kind: cursorEventToolCall, toolID: callID, toolName: name, toolArgs: string(cursorJSON(decoded))})
}

func (r *cursorRun) kv(m cursorMsg) {
	id := m.uint(1)
	num, args, ok := m.oneofCase(4)
	if !ok {
		return
	}
	switch num {
	case 2: // get_blob_args
		key := fmt.Sprintf("%x", args.str(1))
		var result cursorPB
		if data, found := r.blobs[key]; found {
			result = result.raw(1, data)
		}
		r.send(cursorClientMessage(3, cursorPB(nil).uvarint(1, id).msg(2, result)))
	case 3: // set_blob_args
		blobID, _ := args.field(1)
		data, _ := args.field(2)
		r.blobs[fmt.Sprintf("%x", blobID.bytes)] = bytes.Clone(data.bytes)
		r.send(cursorClientMessage(3, cursorPB(nil).uvarint(1, id).msg(3, nil)))
	}
}

// interactionQuery answers Cursor's in-turn permission prompts the way NeoPi
// does: hosted web search and fetch are approved, interactive prompts refused.
func (r *cursorRun) interactionQuery(m cursorMsg) {
	id := m.uint(1)
	num, _, ok := m.oneofCase()
	if !ok {
		return
	}
	var response cursorPB
	switch num {
	case 2, 5, 6, 9: // web search, exa search, exa fetch, web fetch
		response = cursorPB(nil).msg(1, nil)
	case 3: // ask question → AskQuestionResult{rejected=3{reason=1}}
		response = cursorPB(nil).msg(1, cursorPB(nil).msg(3, cursorPB(nil).str(1, "Interactive questions are "+cursorNotImplemented)))
	case 4: // switch mode → rejected=2
		response = cursorPB(nil).msg(2, cursorPB(nil).str(1, "Mode switches are "+cursorNotImplemented))
	case 7: // create plan → CreatePlanResult{error=2}
		response = cursorPB(nil).msg(1, cursorPB(nil).msg(2, cursorPB(nil).str(1, "Plan files are "+cursorNotImplemented)))
	case 8: // setup VM: no answer exists
		return
	default: // a newer approval prompt: approve it like the known ones
		response = cursorPB(nil).msg(1, nil)
	}
	r.send(cursorClientMessage(6, cursorPB(nil).uvarint(1, id).msg(num, response)))
}

// complete reports whether the stream delivered a whole turn.
func (r *cursorRun) complete() error {
	if r.endErr != nil && !r.sawTurnEnded {
		return r.endErr
	}
	if r.sawTurnEnded || r.done {
		return nil
	}
	// A stream cut after the final step lost only usage; the final step ends on answer text.
	if r.stepCompletedLatest && r.lastWasText {
		return nil
	}
	return statusErr{code: http.StatusBadGateway, msg: "cursor: stream ended before turnEnded"}
}

// runCursor opens one Run stream and drives it to the end of the turn.
func (e *CursorExecutor) runCursor(ctx context.Context, auth *cliproxyauth.Auth, p *cursorPrepared, wire cursorWireModel, emit func(cursorEvent) error) (*cursorRun, error) {
	baseURL, token := cursorCreds(auth)
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "cursor: missing access token"}
	}
	blobs := cursorBlobs{}
	runRequest := cursorRunRequest(p.conv, wire, p.display, uuid.NewString(), blobs)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	var writeMu sync.Mutex
	writerClosed := false
	send := func(frame []byte) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if writerClosed {
			return
		}
		if _, err := pw.Write(frame); err != nil {
			writerClosed = true
		}
	}
	closeWriter := func() {
		writeMu.Lock()
		defer writeMu.Unlock()
		if !writerClosed {
			writerClosed = true
			_ = pw.Close()
		}
	}
	defer closeWriter()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+cursorauth.RunPath, pr)
	if err != nil {
		return nil, err
	}
	cursorauth.ClientHeaders(req.Header, token, "application/connect+proto")
	req.Header.Set("Te", "trailers")
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL: req.URL.String(), Method: http.MethodPost, Headers: req.Header.Clone(),
		Body: []byte(fmt.Sprintf("<cursor run request: %d bytes protobuf, model %s>", len(runRequest), wire.modelID)), Provider: e.Identifier(),
		AuthID: cursorAuthField(auth, false), AuthLabel: cursorAuthField(auth, true),
	})

	// HTTP/2 returns response headers while the request body stays open, so
	// the run request goes out from its own goroutine.
	go send(cursorFrame(runRequest))
	go func() {
		ticker := time.NewTicker(cursorHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				send(cursorHeartbeat())
			}
		}
	}()

	httpResp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	defer func() { _ = httpResp.Body.Close() }()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		return nil, cursorHTTPError(httpResp.StatusCode, body)
	}

	run := &cursorRun{conv: p.conv, blobs: blobs, send: send, emit: emit, emittedTools: map[string]bool{}}
	frames := newCursorFrameReader(httpResp.Body)
	for {
		flags, payload, errFrame := frames.next()
		if errFrame != nil {
			if errors.Is(errFrame, io.EOF) {
				break
			}
			if ctx.Err() != nil {
				return run, ctx.Err()
			}
			if run.sawTurnEnded {
				break
			}
			return run, statusErr{code: http.StatusBadGateway, msg: errFrame.Error()}
		}
		if flags&cursorFlagEndStream != 0 {
			run.endErr = cursorEndStreamError(payload)
			// The end frame is the server's last message: half-close our side.
			closeWriter()
			continue
		}
		if errHandle := run.handle(payload); errHandle != nil {
			return run, errHandle
		}
		if run.done {
			return run, nil
		}
	}
	if status := httpResp.Trailer.Get("Grpc-Status"); status != "" && status != "0" && run.endErr == nil {
		run.endErr = statusErr{code: http.StatusBadGateway, msg: "cursor: gRPC error " + status + ": " + httpResp.Trailer.Get("Grpc-Message")}
	}
	return run, run.complete()
}

func cursorAuthField(a *cliproxyauth.Auth, label bool) string {
	switch {
	case a == nil:
		return ""
	case label:
		return a.Label
	default:
		return a.ID
	}
}

// runWithFallback runs the normalized wire model and, when Cursor rejects it
// as not found before anything streamed, retries once with the discovered
// slug in both identity fields (NeoPi's discovered wire mode).
func (e *CursorExecutor) runWithFallback(ctx context.Context, auth *cliproxyauth.Auth, p *cursorPrepared, emit func(cursorEvent) error) (*cursorRun, error) {
	wire := resolveCursorWireModel(p.slug, p.maxMode)
	run, err := e.runCursor(ctx, auth, p, wire, emit)
	if err == nil || (run != nil && run.emitted) || !cursorIsNotFound(err) {
		return run, err
	}
	discovered := cursorWireModel{modelID: p.slug, detailsModelID: p.slug, maxMode: p.maxMode}
	if discovered.modelID == wire.modelID && len(wire.params) == 0 {
		return run, err
	}
	return e.runCursor(ctx, auth, p, discovered, emit)
}

// Execute performs a non-streaming request.
func (e *CursorExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	p, err := e.prepare(ctx, auth, req, opts, false)
	if err != nil {
		return resp, err
	}
	var text, reasoning strings.Builder
	var toolCalls []any
	run, err := e.runWithFallback(ctx, auth, p, func(ev cursorEvent) error {
		switch ev.kind {
		case cursorEventText:
			text.WriteString(ev.text)
		case cursorEventReasoning:
			reasoning.WriteString(ev.text)
		case cursorEventToolCall:
			toolCalls = append(toolCalls, map[string]any{"id": ev.toolID, "type": "function", "function": map[string]any{"name": ev.toolName, "arguments": ev.toolArgs}})
		}
		return nil
	})
	if err != nil {
		return resp, err
	}
	message := map[string]any{"role": "assistant", "content": text.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	finish := "stop"
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		finish = "tool_calls"
	}
	usage := run.finalUsage()
	completion := cursorJSON(map[string]any{
		"id": "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", ""), "object": "chat.completion", "created": time.Now().Unix(), "model": req.Model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   usage.openAI(),
	})
	helps.AppendAPIResponseChunk(ctx, e.cfg, completion)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, p.respFormat, req.Model, p.original, p.translated, completion, &param)
	if len(out) == 0 {
		return resp, statusErr{code: http.StatusBadGateway, msg: "cursor: response translation produced no output"}
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(completion))
	if p.respFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

// finalUsage prefers the turn's reported counters; a response that ended at a
// tool handoff has only the streamed output estimate and the checkpoint's
// context occupancy, whose remainder is the prompt.
func (r *cursorRun) finalUsage() cursorUsage {
	u := r.usage
	if u.output == 0 {
		u.output = r.outputEstimate
	}
	if u.input == 0 && r.contextUsed > u.output {
		u.input = r.contextUsed - u.output
	}
	return u
}

// ExecuteStream performs a streaming request.
func (e *CursorExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	p, err := e.prepare(ctx, auth, req, opts, true)
	if err != nil {
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		claudeInput := helps.NewClaudeInputTokenState(p.from, sdktranslator.FormatOpenAI, p.respFormat, p.original)
		var param any
		helps.InitializeApplyPatchStream(ctx, sdktranslator.FormatOpenAI, p.respFormat, req.Model, p.original, p.translated, &param)
		var streamUsage helps.StreamUsageBuffer
		builder := newCursorChatBuilder(req.Model)
		forward := func(chunk []byte) error {
			line := append([]byte("data: "), chunk...)
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			streamUsage.ObserveOpenAIStream(line)
			helps.ObserveChatTokenEvent(reporter, chunk)
			for _, translated := range helps.TranslateStreamWithClaudeInputTokens(ctx, sdktranslator.FormatOpenAI, p.respFormat, req.Model, p.original, p.translated, line, &param, claudeInput) {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: translated}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}
		run, errRun := e.runWithFallback(ctx, auth, p, func(ev cursorEvent) error { return forward(builder.event(ev)) })
		if errRun != nil {
			if ctx.Err() == nil {
				helps.RecordAPIResponseError(ctx, e.cfg, errRun)
				reporter.PublishFailure(ctx, errRun)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errRun}:
				case <-ctx.Done():
				}
			}
			return
		}
		finish := "stop"
		if run.toolCalls > 0 {
			finish = "tool_calls"
		}
		if forward(builder.finish(finish)) != nil || forward(builder.usageChunk(run.finalUsage())) != nil || forward([]byte("[DONE]")) != nil {
			return
		}
		streamUsage.Publish(ctx, reporter)
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"Content-Type": {"text/event-stream"}}, Chunks: out}, nil
}

// ---- errors ----

// cursorErrorStatus maps aiserver.v1.CursorError codes to HTTP statuses (NeoPi's CURSOR_ERROR_STATUS).
var cursorErrorStatus = map[uint64]int{
	1: 401, 2: 401, 3: 401, 4: 403, 5: 404, 6: 404, 7: 429, 8: 429, 9: 429, 10: 429, 11: 401, 12: 401,
	14: 429, 18: 401, 20: 413, 22: 429, 23: 403, 28: 429, 33: 404, 34: 429, 35: 429, 38: 401, 39: 404,
	41: 429, 42: 401, 43: 413, 44: 429, 45: 429, 46: 403, 47: 403, 48: 403, 49: 403, 50: 429, 51: 429,
	54: 403, 58: 403, 61: 404, 64: 403, 65: 401,
}

var cursorRetryableErrors = map[uint64]bool{13: true, 25: true, 55: true, 56: true, 57: true, 59: true, 63: true}

// cursorConnectStatus maps Connect error codes to HTTP statuses.
var cursorConnectStatus = map[string]int{
	"canceled": 499, "invalid_argument": 400, "deadline_exceeded": 504, "not_found": 404, "already_exists": 409,
	"permission_denied": 403, "resource_exhausted": 429, "failed_precondition": 400, "aborted": 409,
	"out_of_range": 400, "unimplemented": 501, "internal": 500, "unavailable": 503, "data_loss": 500,
	"unauthenticated": 401,
}

// cursorEndStreamError decodes a Connect end-of-stream frame; nil when it carries no error.
func cursorEndStreamError(payload []byte) error {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	errNode := gjson.GetBytes(payload, "error")
	if !errNode.Exists() || !errNode.IsObject() {
		if !gjson.ValidBytes(payload) {
			return statusErr{code: http.StatusBadGateway, msg: "cursor: malformed end-of-stream frame"}
		}
		return nil
	}
	return cursorConnectError(errNode)
}

func cursorConnectError(errNode gjson.Result) error {
	code := errNode.Get("code").String()
	message := errNode.Get("message").String()
	for _, d := range errNode.Get("details").Array() {
		typ := d.Get("type").String()
		if typ != "aiserver.v1.ErrorDetails" && typ != "type.googleapis.com/aiserver.v1.ErrorDetails" {
			continue
		}
		raw, errDecode := base64.RawStdEncoding.DecodeString(strings.TrimRight(d.Get("value").String(), "="))
		if errDecode != nil {
			continue
		}
		detail, errParse := cursorParse(raw)
		if errParse != nil {
			continue
		}
		cursorCode := detail.uint(1)
		var title, body string
		var retryable, hasRetryable bool
		if custom, ok := detail.sub(2); ok {
			title, body = strings.TrimSpace(custom.str(1)), strings.TrimSpace(custom.str(2))
			if f, okRetry := custom.field(4); okRetry {
				hasRetryable, retryable = true, f.varint != 0
			}
		}
		text := strings.Trim(strings.Join([]string{title, body}, ": "), ": ")
		if text == "" {
			text = message
		}
		full := fmt.Sprintf("Cursor error %d: %s", cursorCode, text)
		if status, ok := cursorErrorStatus[cursorCode]; ok {
			return statusErr{code: status, msg: full}
		}
		if retryable || (!hasRetryable && cursorRetryableErrors[cursorCode]) {
			return statusErr{code: http.StatusServiceUnavailable, msg: full}
		}
		return statusErr{code: http.StatusBadRequest, msg: full}
	}
	status, ok := cursorConnectStatus[code]
	if !ok {
		status = http.StatusBadGateway
	}
	if code == "" {
		code = "unknown"
	}
	return statusErr{code: status, msg: "cursor: Connect error " + code + ": " + message}
}

func cursorHTTPError(status int, body []byte) error {
	if errNode := gjson.ParseBytes(body); errNode.Get("code").Exists() {
		if err, ok := cursorConnectError(errNode).(statusErr); ok {
			if err.code == http.StatusBadGateway {
				err.code = status
			}
			return err
		}
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return statusErr{code: status, msg: "cursor: upstream status " + strconv.Itoa(status) + ": " + msg}
}

func cursorIsNotFound(err error) bool {
	var se statusErr
	return errors.As(err, &se) && se.code == http.StatusNotFound
}
