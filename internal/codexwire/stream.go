package codexwire

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiretry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// NeoPi's Codex retry and recovery budgets (openai-codex-responses.ts).
const (
	maxRetries                  = 5
	retryDelay                  = 500 * time.Millisecond
	rateLimitBudget             = 5 * time.Minute
	sseIdleTimeout              = 300 * time.Second
	sseFirstEventTimeout        = 300 * time.Second
	whitespaceDeltaEventLimit   = 256
	whitespaceDeltaCharLimit    = 16 * 1024
	whitespaceLoopRetryLimit    = 2
	whitespaceLoopRetryDelay    = 250 * time.Millisecond
	nativeLaneSteerRejectedCode = "unsupported_native_inflight_message"
)

var (
	retryableEventCodes = map[string]bool{
		"model_error": true, "server_error": true, "internal_error": true, nativeLaneSteerRejectedCode: true,
	}
	retryableEventMessage = regexp.MustCompile(`(?i)processing your request|retry your request|temporar(?:y|ily)|overloaded|service.?unavailable|internal error|server error`)
	http2StreamReset      = regexp.MustCompile(`<StreamReset stream_id:\d+, error_code:(?:2|7), remote_reset:True>`)
	incompleteChunk       = regexp.MustCompile(`peer closed connection without sending complete message body \(incomplete chunked read\)`)
	stalePrevious         = regexp.MustCompile(`(?i)previous[ _]?response`)
	staleReason           = regexp.MustCompile(`(?i)not[ _]?found|invalid|expired|stale|unsupported`)
	accessProgramMessage  = regexp.MustCompile(`(?i)\baccess[ _]programs?\b|\breduced refusals\b`)
	transientMessage      = regexp.MustCompile(`(?i)overloaded|rate.?limit|too many requests|service.?unavailable|server error|internal error|connection.?error|unable to connect|fetch failed|network error|stream stall|other side closed|HTTP2(?:StreamReset|RefusedStream|EnhanceYourCalm)`)

	// droppedAccessPrograms holds account\0model pairs whose cyber program the
	// backend rejected this process (access-programs.ts droppedPrograms).
	droppedAccessPrograms sync.Map
)

// StreamError is CodexProviderStreamError: a failure the stream itself reported.
type StreamError struct {
	Message   string
	Retryable bool
	Code      string
	// Event is the failure frame, when the backend sent one.
	Event []byte
}

func (e *StreamError) Error() string { return e.Message }

// HTTPError is CodexApiError: a non-2xx SSE response.
type HTTPError struct {
	Status int
	Header http.Header
	Body   []byte
	Code   string
}

func (e *HTTPError) Error() string {
	message := gjson.GetBytes(e.Body, "error.message").String()
	if message == "" {
		message = strings.TrimSpace(string(e.Body))
	}
	if message == "" {
		message = http.StatusText(e.Status)
	}
	return message
}

type whitespaceLoopError struct{ message string }

func (e *whitespaceLoopError) Error() string { return e.message }

// streamFailure is createCodexProviderStreamError.
func streamFailure(event []byte) *StreamError {
	root := gjson.ParseBytes(event)
	str := func(path string) string {
		if value := root.Get(path); value.Type == gjson.String {
			return value.String()
		}
		return ""
	}
	nested := "error"
	if !root.Get("error").IsObject() && root.Get("response.error").IsObject() {
		nested = "response.error"
	}
	code := firstNonEmpty(str(nested+".code"), str(nested+".type"), str("code"))
	message := firstNonEmpty(str(nested+".message"), str("message"), str("response.message"))
	status := firstNonEmpty(str("response.status"), str("status"))
	var meta []string
	if code != "" {
		meta = append(meta, "code="+code)
	}
	if status != "" {
		meta = append(meta, "status="+status)
	}
	var formatted string
	switch {
	case message != "" && len(meta) > 0:
		formatted = fmt.Sprintf("Codex response failed: %s (%s)", message, strings.Join(meta, ", "))
	case message != "":
		formatted = "Codex response failed: " + message
	case len(meta) > 0:
		formatted = fmt.Sprintf("Codex response failed (%s)", strings.Join(meta, ", "))
	default:
		text := string(jsjson.Normalize(event))
		if len(text) > 800 {
			text = fmt.Sprintf("%s…[truncated %d]", text[:800], len(text)-800)
		}
		formatted = "Codex response failed: " + text
	}
	if str("type") == "error" {
		formatted = strings.Replace(formatted, "response failed", "error event", 1)
	}
	retryable := (code != "" && retryableEventCodes[strings.ToLower(code)]) ||
		(message != "" && (retryableEventMessage.MatchString(message) || http2StreamReset.MatchString(message) || incompleteChunk.MatchString(message)))
	return &StreamError{Message: formatted, Retryable: retryable, Code: code, Event: event}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func errorCode(err error) string {
	var stream *StreamError
	if errors.As(err, &stream) {
		return stream.Code
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Code
	}
	return ""
}

// isStalePrevious is isCodexStalePreviousResponseError.
func isStalePrevious(err error) bool {
	if err == nil {
		return false
	}
	switch errorCode(err) {
	case "previous_response_not_found", "codex_previous_response_stale":
		return true
	}
	return stalePrevious.MatchString(err.Error()) && staleReason.MatchString(err.Error())
}

func isAccessProgramRejection(err error) bool {
	switch errorCode(err) {
	case "access_program_not_enabled", "invalid_access_program":
		return true
	}
	return err != nil && accessProgramMessage.MatchString(err.Error())
}

// isProviderRetryable is AIError.isProviderRetryableError for errors that are
// not stream frames: transient HTTP statuses and network failures.
func isProviderRetryable(err error) bool {
	var stream *StreamError
	if errors.As(err, &stream) {
		return stream.Retryable
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return neopiretry.IsRetryableStatus(httpErr.Status)
	}
	return err != nil && transientMessage.MatchString(err.Error())
}

// SteerClaim is one steering submission taken from the caller.
type SteerClaim struct {
	Input []json.RawMessage
	// Resolve reports the server's acknowledgement frame, or nil when none came.
	Resolve func(accepted bool, ack []byte)
}

// SteerSource is LiveSteering: the caller's queue of steering input.
type SteerSource interface {
	// Claim waits for the next submission; false once ctx ends or the source closes.
	Claim(ctx context.Context) (*SteerClaim, bool)
}

// Turn runs one Codex request the way NeoPi's streamOpenAICodexResponses does:
// WebSocket first with chaining, SSE fallback, and the recovery ladder.
type Turn struct {
	Sessions *Sessions
	Request  *Request
	Model    *registry.CodexModel
	// Finalize applies user payload rules to every frame or body put on the wire.
	Finalize func([]byte) []byte
	Dial     DialFunc
	// Do sends one HTTP attempt and returns the decoded response.
	Do       func(*http.Request) (*http.Response, error)
	Steering SteerSource
	Wait     func(context.Context, time.Duration) error
}

// Outcome is what a turn delivered.
type Outcome struct {
	Transport  Transport
	ResponseID string
	// Output is the response's native output items.
	Output []json.RawMessage
	// Steering is input the server accepted and continues in a successor.
	Steering []json.RawMessage
	// Committed reports that events reached the caller, so a replay is impossible.
	Committed bool
}

type outEntry struct {
	itemID      string
	outputIndex int64
	hasIndex    bool
	native      json.RawMessage
}

type whitespaceState struct {
	itemID        string
	outputIndex   int64
	hasIndex      bool
	events, chars int
	firstSeq      *int64
	lastSeq       *int64
}

func rawItems(value gjson.Result) []json.RawMessage {
	var items []json.RawMessage
	for _, item := range value.Array() {
		items = append(items, json.RawMessage(item.Raw))
	}
	return items
}

type turnRun struct {
	*Turn
	ctx   context.Context
	emit  func([]byte) error
	state *transportState

	transport    Transport
	ws           *socketStream
	sse          *sseStream
	bodyForState object

	committed   bool
	preamble    [][]byte
	sawTerminal bool
	responseID  string

	entries     []*outEntry
	extra       []json.RawMessage
	openByID    map[string]*outEntry
	openByIndex map[int64]*outEntry
	current     *outEntry
	whitespace  *whitespaceState

	whitespaceRetries int
	wsStreamRetries   int
	providerRetries   int

	pump         *steerPump
	pumpSocket   *Socket
	steerTainted bool
	steered      []json.RawMessage
}

var preambleEvents = map[string]bool{
	"response.created": true, "response.in_progress": true, "response.queued": true,
	"response.metadata": true, "codex.rate_limits": true,
}

// Run streams the turn's events to emit until a terminal event or a failure
// the recovery ladder cannot absorb. Recovery only replays while nothing has
// reached emit beyond the response preamble: unlike NeoPi's in-process
// consumer, a downstream client cannot discard content it already received.
func (t *Turn) Run(ctx context.Context, emit func([]byte) error) (Outcome, error) {
	if t.Wait == nil {
		t.Wait = neopiretry.Sleep
	}
	if t.Finalize == nil {
		t.Finalize = func(body []byte) []byte { return body }
	}
	r := &turnRun{Turn: t, ctx: ctx, emit: emit}
	if t.Request.SessionKey != "" {
		t.Sessions.mu.Lock()
		r.state = t.Sessions.transport(t.Request.SessionKey)
		t.Sessions.mu.Unlock()
	}
	r.resetAccumulators()
	err := r.openInitial()
	for err == nil {
		err = r.pumpEvents()
		if err == nil {
			r.settleSteering(true)
			return r.outcome(), nil
		}
		r.closeStreams()
		r.retireSteering()
		recovered, errRecover := r.recover(err)
		if errRecover != nil {
			err = errRecover
			break
		}
		if !recovered {
			r.settleSteering(false)
			break
		}
		err = nil
	}
	r.closeStreams()
	r.failure(err)
	return r.outcome(), err
}

// RunCompaction is openCodexCompactionEventStream: the compaction request
// opens like a turn (websocket first), the append baseline is dropped at once
// because the replacement history makes it stale, and a websocket attempt is
// held back until it finishes so an SSE replay replaces rather than extends
// it. Every event reaches emit; the caller judges the outcome.
func (t *Turn) RunCompaction(ctx context.Context, emit func([]byte) error) error {
	if t.Wait == nil {
		t.Wait = neopiretry.Sleep
	}
	if t.Finalize == nil {
		t.Finalize = func(body []byte) []byte { return body }
	}
	r := &turnRun{Turn: t, ctx: ctx, emit: emit}
	if t.Request.SessionKey != "" {
		t.Sessions.mu.Lock()
		r.state = t.Sessions.transport(t.Request.SessionKey)
		t.Sessions.mu.Unlock()
	}
	defer r.closeStreams()
	if err := r.openInitial(); err != nil {
		return err
	}
	r.resetChain(false)
	key := t.Request.SessionKey
	t.Sessions.mu.Lock()
	previousTurnState := t.Request.session.turnStates[key]
	previousETag := ""
	if r.state != nil {
		previousETag = r.state.modelsETag
	}
	t.Sessions.mu.Unlock()
	completed := false
	defer func() {
		if completed {
			return
		}
		t.Sessions.mu.Lock()
		if previousTurnState == "" {
			delete(t.Request.session.turnStates, key)
		} else {
			t.Request.session.turnStates[key] = previousTurnState
		}
		if r.state != nil {
			r.state.modelsETag = previousETag
		}
		t.Sessions.mu.Unlock()
	}()
	deliver := func(event []byte) error {
		if gjson.GetBytes(event, "type").String() == "response.metadata" {
			header := http.Header{}
			gjson.GetBytes(event, "headers").ForEach(func(name, value gjson.Result) bool {
				header.Set(name.String(), value.String())
				return true
			})
			t.Sessions.RecordResponseHeaders(t.Request, header)
		}
		return emit(event)
	}
	drain := func() error {
		for {
			event, err := r.next()
			if err != nil || event == nil {
				return err
			}
			if err = deliver(event); err != nil {
				return err
			}
		}
	}
	if r.transport == TransportWebsocket {
		var buffered [][]byte
		var streamErr error
		for {
			event, err := r.next()
			if err != nil {
				streamErr = err
				break
			}
			if event == nil {
				break
			}
			buffered = append(buffered, event)
		}
		if streamErr != nil {
			if ctx.Err() != nil || !isTransport(streamErr) {
				return streamErr
			}
			r.recordWebsocketFailure(true)
			if err := r.openSSE(); err != nil {
				return err
			}
			err := drain()
			completed = err == nil
			return err
		}
		var failure []byte
		for _, event := range buffered {
			switch gjson.GetBytes(event, "type").String() {
			case "error", "response.failed":
				failure = event
			}
		}
		if failure != nil && r.dropRejectedAccessPrograms(streamFailure(failure)) {
			if err := r.openSSE(); err != nil {
				return err
			}
			err := drain()
			completed = err == nil
			return err
		}
		for _, event := range buffered {
			if err := deliver(event); err != nil {
				return err
			}
		}
		completed = true
		return nil
	}
	err := drain()
	completed = err == nil
	return err
}

func (r *turnRun) outcome() Outcome {
	out := Outcome{Transport: r.transport, ResponseID: r.responseID, Output: r.nativeOutput(), Committed: r.committed}
	if r.state != nil {
		r.Sessions.mu.Lock()
		if accepted := r.state.accepted; accepted != nil {
			for _, steer := range accepted.steers {
				out.Steering = append(out.Steering, steer.items...)
			}
		}
		r.Sessions.mu.Unlock()
	}
	return out
}

// failure is handleCodexStreamFailure's state reset: the append baseline and
// etag survive only a rate-limit rejection of a chained websocket request.
func (r *turnRun) failure(err error) {
	if r.state == nil || err == nil {
		return
	}
	r.Sessions.mu.Lock()
	defer r.Sessions.mu.Unlock()
	code := strings.ToLower(errorCode(err))
	preserve := (code == "rate_limit_exceeded" || code == "slow_down") && r.transport == TransportWebsocket &&
		r.state.canAppend && r.state.lastRequest != nil && r.state.lastResponseID != "" && r.state.lastResponseItems != nil
	if !preserve {
		r.state.resetAppend()
		r.state.modelsETag = ""
	}
}

func (r *turnRun) wait(delay time.Duration) error { return r.Wait(r.ctx, delay) }

func isTransport(err error) bool {
	var transport *TransportError
	return errors.As(err, &transport)
}

// openInitial is openInitialCodexEventStream.
func (r *turnRun) openInitial() error {
	if r.useWebsocket() {
		retries := 0
		for {
			err := r.openWebsocket(retries)
			if err == nil {
				return nil
			}
			if !isTransport(err) {
				return err
			}
			activate := isFatalTransport(err) || retries >= maxRetries
			r.recordWebsocketFailure(activate)
			if activate {
				break
			}
			retries++
			if errWait := r.wait(retryDelay * time.Duration(max(1, retries))); errWait != nil {
				return errWait
			}
		}
	} else if r.state != nil {
		r.releaseIdleSocket()
	}
	err := r.openSSE()
	if err != nil && r.dropRejectedAccessPrograms(err) {
		return r.openSSE()
	}
	return err
}

// useWebsocket is shouldUseCodexWebSocket: discovery only ever marks models
// as preferring websockets, so only a session fallback disables them.
func (r *turnRun) useWebsocket() bool {
	if r.state == nil {
		return false
	}
	r.Sessions.mu.Lock()
	defer r.Sessions.mu.Unlock()
	return !r.state.disableWebsocket
}

// recordWebsocketFailure is recordCodexWebSocketFailure.
func (r *turnRun) recordWebsocketFailure(activate bool) {
	r.Sessions.mu.Lock()
	r.state.resetAppend()
	socket := r.state.socket
	if socket != nil && !socket.isConnecting() {
		r.state.socket = nil
	} else {
		socket = nil
	}
	if activate && !r.state.disableWebsocket {
		r.state.disableWebsocket = true
		r.state.fallbackCount++
	}
	r.Sessions.mu.Unlock()
	if socket != nil {
		socket.Close("fallback")
	}
}

// releaseIdleSocket is releaseIdleCodexWebSocket.
func (r *turnRun) releaseIdleSocket() {
	r.Sessions.mu.Lock()
	socket := r.state.socket
	if socket == nil || socket.isBusy() {
		r.Sessions.mu.Unlock()
		return
	}
	r.state.socket = nil
	r.state.resetAppend()
	r.Sessions.mu.Unlock()
	socket.Close("transport_not_selected")
}

// acquireSocket is getOrCreateCodexWebSocketConnection.
func (r *turnRun) acquireSocket(url string, header http.Header) (*Socket, error) {
	for join := 0; join < 3; join++ {
		r.Sessions.mu.Lock()
		pending := r.state.socket
		r.Sessions.mu.Unlock()
		if pending == nil || pending.isOpen() || !pending.isConnecting() {
			break
		}
		_ = pending.connect(r.ctx)
	}
	r.Sessions.mu.Lock()
	existing := r.state.socket
	r.Sessions.mu.Unlock()
	if existing != nil && existing.isOpen() {
		if existing.matchesAuth(header) && existing.isHealthyForReuse(time.Now()) {
			return existing, nil
		}
		reason := "stale-reuse"
		if !existing.matchesAuth(header) {
			reason = "token-refresh"
		}
		existing.Close(reason)
	}
	if existing != nil {
		existing.Close("reconnect")
	}
	request := r.Request
	socket := newSocket(url, header, r.Dial, func(handshake http.Header) { r.Sessions.RecordResponseHeaders(request, handshake) })
	r.Sessions.mu.Lock()
	r.state.resetAppend()
	r.state.socket = socket
	r.Sessions.mu.Unlock()
	if err := socket.connect(r.ctx); err != nil {
		return nil, err
	}
	return socket, nil
}

func websocketURL(raw string) string {
	switch {
	case strings.HasPrefix(raw, "https://"):
		return "wss://" + strings.TrimPrefix(raw, "https://")
	case strings.HasPrefix(raw, "http://"):
		return "ws://" + strings.TrimPrefix(raw, "http://")
	}
	return raw
}

// openWebsocket is openCodexWebSocketTransport.
func (r *turnRun) openWebsocket(retry int) error {
	r.Sessions.mu.Lock()
	steered := r.state.accepted
	r.state.accepted = nil
	body := parseObject(r.Request.Body)
	chained := chainedBody(r.state, body)
	r.Sessions.mu.Unlock()
	header := r.Sessions.Headers(r.Request, TransportWebsocket)
	socket, err := r.acquireSocket(websocketURL(r.Request.URL), header)
	if err != nil {
		return err
	}
	var live []acceptedSteer
	if steered != nil && steered.socket == socket {
		for _, steer := range steered.steers {
			if !socket.hasFailedSteer([]string{steer.id}) {
				live = append(live, steer)
			}
		}
	}
	if len(live) > 0 {
		var delta []json.RawMessage
		if chained.has("previous_response_id") {
			delta = rawItems(chained.get("input"))
		}
		var steering []json.RawMessage
		var ids []string
		for _, steer := range live {
			steering = append(steering, steer.items...)
			ids = append(ids, steer.id)
		}
		plan, input := PlanSteeredRequest(delta, steering)
		switch plan {
		case SteerAttach:
			stream, errStream := socket.stream(r.ctx, nil, ids)
			if errStream != nil {
				return errStream
			}
			r.beginWebsocket(stream, body)
			return nil
		case SteerCreate:
			chained = append(object(nil), chained...)
			chained.set("input", jsjson.Marshal(input))
		default:
			socket.Close("steering-discard")
			r.Sessions.mu.Lock()
			if r.state.socket == socket {
				r.state.socket = nil
			}
			r.state.resetAppend()
			r.Sessions.mu.Unlock()
			chained = body
			if socket, err = r.acquireSocket(websocketURL(r.Request.URL), header); err != nil {
				return err
			}
		}
	}
	r.Sessions.mu.Lock()
	frame := websocketFrame(r.Request, chained)
	r.Sessions.mu.Unlock()
	wire := r.Finalize(frame.bytes())
	sent := parseObject(wire)
	state := append(object(nil), body...)
	syncField(&state, sent, "stream_options")
	syncField(&state, sent, "service_tier")
	stream, err := socket.stream(r.ctx, wire, nil)
	if err != nil {
		return err
	}
	r.beginWebsocket(stream, state)
	return nil
}

func syncField(target *object, source object, name string) {
	if value := source.get(name); value.Exists() {
		target.set(name, json.RawMessage(value.Raw))
	} else {
		target.del(name)
	}
}

func (r *turnRun) beginWebsocket(stream *socketStream, bodyForState object) {
	r.closeStreams()
	r.ws = stream
	r.transport = TransportWebsocket
	r.bodyForState = bodyForState
}

func (r *turnRun) closeStreams() {
	if r.ws != nil {
		r.ws.finish()
		r.ws = nil
	}
	if r.sse != nil {
		r.sse.close()
		r.sse = nil
	}
}

// openSSE is openCodexSseTransport plus openCodexSseEventStream.
func (r *turnRun) openSSE() error {
	wire := r.Finalize(r.Request.Body)
	sent := parseObject(wire)
	state := object{}
	syncField(&state, sent, "model")
	syncField(&state, sent, "stream_options")
	syncField(&state, sent, "service_tier")
	header := r.Sessions.Headers(r.Request, TransportSSE)
	header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	header.Set("Connection", "keep-alive")
	var compressed []byte
	if IsOfficialURL(r.Request.URL) {
		if encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(3))); err == nil {
			compressed = encoder.EncodeAll(wire, nil)
			_ = encoder.Close()
		}
	}
	attemptCtx, cancelAttempt := context.WithCancel(r.ctx)
	var preResponse *time.Timer
	send := func(payload []byte, encoded bool) (*http.Response, error) {
		return neopiretry.Do(r.ctx, func(int) (*http.Response, error) {
			if preResponse != nil {
				preResponse.Stop()
			}
			preResponse = time.AfterFunc(sseFirstEventTimeout, cancelAttempt)
			request, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, r.Request.URL, bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			request.Header = header.Clone()
			if encoded {
				request.Header.Set("Content-Encoding", "zstd")
			}
			response, err := r.Do(request)
			if err == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
				preResponse.Stop()
			}
			return response, err
		}, neopiretry.Options{
			MaxAttempts:  maxRetries + 1,
			MaxDelay:     rateLimitBudget,
			DefaultDelay: func(attempt int) time.Duration { return retryDelay * time.Duration(attempt+1) },
			Wait:         r.Wait,
		})
	}
	var response *http.Response
	var err error
	if compressed != nil {
		response, err = send(compressed, true)
		if err == nil && (response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnsupportedMediaType) {
			_ = response.Body.Close()
			response, err = send(wire, false)
		}
	} else {
		response, err = send(wire, false)
	}
	if err != nil {
		cancelAttempt()
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		cancelAttempt()
		code := gjson.GetBytes(body, "error.code").String()
		if code == "" {
			code = gjson.GetBytes(body, "error.type").String()
		}
		return &HTTPError{Status: response.StatusCode, Header: response.Header, Body: body, Code: code}
	}
	r.Sessions.RecordResponseHeaders(r.Request, response.Header)
	r.closeStreams()
	r.sse = newSSEStream(response.Body, cancelAttempt)
	r.transport = TransportSSE
	r.bodyForState = state
	return nil
}

func (r *turnRun) next() ([]byte, error) {
	if r.ws != nil {
		return r.ws.next()
	}
	if r.sse != nil {
		return r.sse.next()
	}
	return nil, nil
}

func (r *turnRun) resetAccumulators() {
	r.entries, r.extra, r.current = nil, nil, nil
	r.openByID, r.openByIndex = map[string]*outEntry{}, map[int64]*outEntry{}
	r.preamble = nil
	r.whitespace = nil
}

func (r *turnRun) nativeOutput() []json.RawMessage {
	var out []json.RawMessage
	for _, entry := range r.entries {
		if entry.native != nil {
			out = append(out, entry.native)
		}
	}
	return append(out, r.extra...)
}

func (r *turnRun) openItemFor(event gjson.Result) *outEntry {
	if id := event.Get("item_id"); id.Type == gjson.String && id.String() != "" {
		return r.openByID[id.String()]
	}
	if index := event.Get("output_index"); index.Type == gjson.Number {
		return r.openByIndex[int64(index.Float())]
	}
	return r.current
}

func (r *turnRun) closeItem(entry *outEntry) {
	if entry == nil {
		return
	}
	if entry.itemID != "" {
		delete(r.openByID, entry.itemID)
	}
	if entry.hasIndex {
		delete(r.openByIndex, entry.outputIndex)
	}
	if r.current == entry {
		r.current = nil
	}
}

func (r *turnRun) forward(event []byte, eventType string) error {
	if !r.committed && preambleEvents[eventType] {
		r.preamble = append(r.preamble, event)
		return nil
	}
	if !r.committed {
		r.committed = true
		for _, held := range r.preamble {
			if err := r.emit(held); err != nil {
				return err
			}
		}
		r.preamble = nil
	}
	return r.emit(event)
}

// pumpEvents is CodexStreamProcessor.process's event loop for one attempt.
func (r *turnRun) pumpEvents() error {
	for {
		event, err := r.next()
		if err != nil {
			return err
		}
		if event == nil {
			break
		}
		root := gjson.ParseBytes(event)
		eventType := root.Get("type").String()
		if eventType == "" {
			continue
		}
		if err = r.observe(root, event, eventType); err != nil {
			return err
		}
		if err = r.forward(event, eventType); err != nil {
			return err
		}
		if r.sawTerminal {
			r.closeStreams()
			return nil
		}
	}
	if !r.sawTerminal {
		return &StreamError{Message: "Codex stream ended before terminal completion event", Retryable: true}
	}
	return nil
}

// observe is #handleStreamEvent's bookkeeping for transport state.
func (r *turnRun) observe(root gjson.Result, raw []byte, eventType string) error {
	switch eventType {
	case "response.created":
		r.startSteering(root)
	case "response.output_item.added":
		r.whitespace = nil
		entry := &outEntry{itemID: root.Get("item.id").String()}
		if index := root.Get("output_index"); index.Type == gjson.Number {
			entry.outputIndex, entry.hasIndex = int64(index.Float()), true
		}
		r.entries = append(r.entries, entry)
		r.current = entry
		if entry.itemID != "" {
			r.openByID[entry.itemID] = entry
		}
		if entry.hasIndex {
			r.openByIndex[entry.outputIndex] = entry
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		if err := r.observeWhitespace(root); err != nil {
			r.closeSocket("degenerate-tool-call")
			return err
		}
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		r.whitespace = nil
	case "response.output_item.done":
		r.whitespace = nil
		item := root.Get("item")
		if !item.IsObject() {
			return nil
		}
		native := json.RawMessage(item.Raw)
		if item.Get("type").String() == "image_generation_call" && item.Get("result").Exists() && isTruthy(item.Get("result")) {
			object := parseObject(native)
			object.set("status", jsjson.Marshal("completed"))
			native = object.bytes()
		}
		var entry *outEntry
		if id := item.Get("id"); id.Type == gjson.String && id.String() != "" {
			entry = r.openByID[id.String()]
		}
		if entry == nil {
			entry = r.openItemFor(root)
		}
		if entry != nil {
			entry.native = native
		} else {
			r.extra = append(r.extra, native)
		}
		r.closeItem(entry)
	case "response.completed", "response.done", "response.incomplete":
		r.complete(root, eventType)
	case "response.metadata":
		header := http.Header{}
		root.Get("headers").ForEach(func(key, value gjson.Result) bool {
			header.Set(key.String(), value.String())
			return true
		})
		r.Sessions.RecordResponseHeaders(r.Request, header)
	case "error", "response.failed":
		return streamFailure(raw)
	}
	return nil
}

func isTruthy(value gjson.Result) bool {
	switch value.Type {
	case gjson.String:
		return value.String() != ""
	case gjson.Null, gjson.False:
		return false
	case gjson.Number:
		return value.Float() != 0
	}
	return true
}

func (r *turnRun) observeWhitespace(root gjson.Result) error {
	delta := root.Get("delta").String()
	if strings.Trim(delta, "\t\n\r ") != "" {
		r.whitespace = nil
		return nil
	}
	itemID := root.Get("item_id").String()
	if itemID == "" && r.current != nil {
		itemID = r.current.itemID
	}
	var index int64
	hasIndex := false
	if value := root.Get("output_index"); value.Type == gjson.Number {
		index, hasIndex = int64(value.Float()), true
	}
	var sequence *int64
	if value := root.Get("sequence_number"); value.Type == gjson.Number {
		number := int64(value.Float())
		sequence = &number
	}
	state := r.whitespace
	if state == nil || state.itemID != itemID || state.hasIndex != hasIndex || state.outputIndex != index {
		state = &whitespaceState{itemID: itemID, outputIndex: index, hasIndex: hasIndex, firstSeq: sequence}
		r.whitespace = state
	}
	state.events++
	state.chars += len([]rune(delta))
	state.lastSeq = sequence
	if state.events < whitespaceDeltaEventLimit && state.chars < whitespaceDeltaCharLimit {
		return nil
	}
	sequenceLabel, itemLabel := "", ""
	if state.firstSeq != nil && state.lastSeq != nil {
		sequenceLabel = fmt.Sprintf(", sequence %d..%d", *state.firstSeq, *state.lastSeq)
	}
	if itemID != "" {
		itemLabel = " for item " + itemID
	}
	return &whitespaceLoopError{message: fmt.Sprintf("Interrupted OpenAI Codex response after %d consecutive whitespace-only tool-call argument delta events (%d chars%s)%s.", state.events, state.chars, sequenceLabel, itemLabel)}
}

func (r *turnRun) closeSocket(reason string) {
	if r.state == nil {
		return
	}
	r.Sessions.mu.Lock()
	socket := r.state.socket
	r.Sessions.mu.Unlock()
	if socket != nil {
		socket.Close(reason)
	}
}

// complete is #handleResponseCompleted's transport bookkeeping.
func (r *turnRun) complete(root gjson.Result, eventType string) {
	r.sawTerminal = true
	if id := root.Get("response.id"); id.Type == gjson.String {
		r.responseID = id.String()
	}
	if r.state == nil {
		return
	}
	steered := root.Get("response.status").String() == "incomplete" && root.Get("response.incomplete_details.reason").String() == "steered"
	r.Sessions.mu.Lock()
	defer r.Sessions.mu.Unlock()
	if r.transport != TransportWebsocket {
		r.state.resetAppend()
		return
	}
	r.state.lastRequest = r.bodyForState
	native := r.nativeOutput()
	// Codex's own host serves native-resolution image detail (compat resolve.ts).
	items := sanitizeAssistantReplay(native, IsOfficialURL(r.Request.URL))
	if r.responseID != "" && items != nil && len(items) == len(native) {
		r.state.lastResponseID = r.responseID
		r.state.lastResponseItems = items
		r.state.canAppend = eventType == "response.done" || eventType == "response.completed" || steered
	} else {
		r.state.resetAppend()
	}
}

// recover is #recoverStreamError's ladder.
func (r *turnRun) recover(err error) (bool, error) {
	if r.ctx.Err() != nil {
		return false, nil
	}
	r.stopSteeringOnNativeLaneRejection(err)
	var commit *SteerCommitError
	if errors.As(err, &commit) && r.state != nil && !r.committed {
		r.resetAccumulators()
		return true, r.reopenWebsocket()
	}
	for _, step := range []func(error) (bool, error){
		r.tryDropRejectedAccessPrograms, r.tryWhitespaceLoop, r.tryConnectionLimit,
		r.tryStalePrevious, r.tryWebsocketOverSSE, r.tryProviderRetry,
	} {
		if recovered, errStep := step(err); recovered || errStep != nil {
			return recovered, errStep
		}
	}
	return false, nil
}

func (r *turnRun) stopSteeringOnNativeLaneRejection(err error) {
	if r.state == nil || errorCode(err) != nativeLaneSteerRejectedCode {
		return
	}
	var stream *StreamError
	if !errors.As(err, &stream) {
		return
	}
	r.Sessions.mu.Lock()
	r.state.steeringUnsupported = true
	socket := r.state.socket
	r.state.socket = nil
	r.state.resetAppend()
	r.Sessions.mu.Unlock()
	if socket != nil {
		socket.Close("native-lane-steer-rejected")
	}
}

func (r *turnRun) dropRejectedAccessPrograms(err error) bool {
	body := parseObject(r.Request.Body)
	if !body.has("access_programs") || !isAccessProgramRejection(err) {
		return false
	}
	body.del("access_programs")
	r.Request.Body = body.bytes()
	if r.Request.AccountID != "" && r.Model != nil {
		droppedAccessPrograms.Store(r.Request.AccountID+"\x00"+r.Model.ID, true)
	}
	return true
}

func (r *turnRun) tryDropRejectedAccessPrograms(err error) (bool, error) {
	if r.committed || !r.dropRejectedAccessPrograms(err) {
		return false, nil
	}
	r.resetChain(false)
	r.resetAccumulators()
	r.sawTerminal = false
	if r.transport == TransportWebsocket && r.state != nil {
		return true, r.reopenWebsocket()
	}
	return true, r.reopenSSE()
}

func (r *turnRun) tryWhitespaceLoop(err error) (bool, error) {
	var loop *whitespaceLoopError
	if !errors.As(err, &loop) || r.whitespaceRetries >= whitespaceLoopRetryLimit || r.committed {
		return false, nil
	}
	r.whitespaceRetries++
	r.resetChain(true)
	r.resetAccumulators()
	r.sawTerminal = false
	if errWait := r.wait(whitespaceLoopRetryDelay * time.Duration(r.whitespaceRetries)); errWait != nil {
		return false, errWait
	}
	if r.transport == TransportWebsocket && r.state != nil {
		return true, r.reopenWebsocket()
	}
	return true, r.reopenSSE()
}

func (r *turnRun) tryConnectionLimit(err error) (bool, error) {
	var stream *StreamError
	if !errors.As(err, &stream) || stream.Code != "websocket_connection_limit_reached" || r.state == nil || r.transport != TransportWebsocket {
		return false, nil
	}
	r.Sessions.mu.Lock()
	socket := r.state.socket
	r.state.socket = nil
	r.state.resetAppend()
	r.Sessions.mu.Unlock()
	if socket != nil {
		socket.Close("connection_limit")
	}
	if r.committed {
		return false, nil
	}
	r.resetAccumulators()
	if r.wsStreamRetries >= maxRetries {
		r.recordWebsocketFailure(true)
		return true, r.reopenSSE()
	}
	r.wsStreamRetries++
	if errWait := r.wait(retryDelay * time.Duration(max(1, r.wsStreamRetries))); errWait != nil {
		return false, errWait
	}
	return true, r.reopenWebsocket()
}

func (r *turnRun) tryStalePrevious(err error) (bool, error) {
	if !isStalePrevious(err) || r.state == nil || r.committed || r.providerRetries >= maxRetries || r.transport != TransportWebsocket {
		return false, nil
	}
	r.providerRetries++
	r.resetChain(true)
	r.resetAccumulators()
	r.sawTerminal = false
	return true, r.reopenWebsocket()
}

func (r *turnRun) tryWebsocketOverSSE(err error) (bool, error) {
	if r.transport != TransportWebsocket || r.state == nil || !isRetryableTransport(err) || r.committed || r.sawTerminal {
		return false, nil
	}
	activate := isFatalTransport(err) || r.wsStreamRetries >= maxRetries
	r.recordWebsocketFailure(activate)
	r.resetAccumulators()
	if !activate {
		r.wsStreamRetries++
		if errWait := r.wait(retryDelay * time.Duration(max(1, r.wsStreamRetries))); errWait != nil {
			return false, errWait
		}
		return true, r.reopenWebsocket()
	}
	return true, r.reopenSSE()
}

func (r *turnRun) tryProviderRetry(err error) (bool, error) {
	if !isProviderRetryable(err) || r.committed || r.providerRetries >= maxRetries {
		return false, nil
	}
	r.providerRetries++
	r.resetChain(true)
	r.resetAccumulators()
	r.sawTerminal = false
	if errWait := r.wait(retryDelay * time.Duration(r.providerRetries)); errWait != nil {
		return false, errWait
	}
	if r.transport == TransportWebsocket && r.state != nil {
		return true, r.reopenWebsocket()
	}
	return true, r.reopenSSE()
}

func (r *turnRun) resetChain(etag bool) {
	if r.state == nil {
		return
	}
	r.Sessions.mu.Lock()
	r.state.resetAppend()
	if etag {
		r.state.modelsETag = ""
	}
	r.Sessions.mu.Unlock()
}

// reopenWebsocket is #reopenWebSocketStream: a websocket-layer failure to
// reopen disables the websocket and replays over SSE.
func (r *turnRun) reopenWebsocket() error {
	err := r.openWebsocket(r.wsStreamRetries)
	if err == nil || !isTransport(err) {
		return err
	}
	r.recordWebsocketFailure(true)
	return r.reopenSSE()
}

func (r *turnRun) reopenSSE() error { return r.openSSE() }

// sseStream reads one SSE response body as JSON events under NeoPi's
// first-event and idle watchdogs.
type sseStream struct {
	events  chan sseItem
	cancel  context.CancelFunc
	body    io.ReadCloser
	once    sync.Once
	first   bool
	lastAt  time.Time
	stopped chan struct{}
}

type sseItem struct {
	event []byte
	err   error
}

func newSSEStream(body io.ReadCloser, cancel context.CancelFunc) *sseStream {
	stream := &sseStream{events: make(chan sseItem), cancel: cancel, body: body, first: true, lastAt: time.Now(), stopped: make(chan struct{})}
	go stream.read()
	return stream
}

func (s *sseStream) read() {
	defer close(s.events)
	reader := bufio.NewReaderSize(s.body, 64*1024)
	var data []string
	// dispatch delivers the buffered data lines as one event.
	dispatch := func() bool {
		if len(data) == 0 {
			return true
		}
		payload := strings.Join(data, "\n")
		data = nil
		if payload == "[DONE]" {
			return true
		}
		item := sseItem{event: []byte(payload)}
		if !json.Valid(item.event) {
			item = sseItem{err: errors.New("SyntaxError: JSON Parse error: Unable to parse JSON string")}
		}
		select {
		case s.events <- item:
			return item.err == nil
		case <-s.stopped:
			return false
		}
	}
	for {
		line, err := reader.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case trimmed == "" && line != "":
			if !dispatch() {
				return
			}
		case strings.HasPrefix(trimmed, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " "))
		}
		if err != nil {
			if !dispatch() || errors.Is(err, io.EOF) {
				return
			}
			select {
			case s.events <- sseItem{err: err}:
			case <-s.stopped:
			}
			return
		}
	}
}

func (s *sseStream) next() ([]byte, error) {
	timeout := sseIdleTimeout - time.Since(s.lastAt)
	message := "OpenAI Codex SSE stream stalled while waiting for the next event"
	if s.first {
		timeout = sseFirstEventTimeout - time.Since(s.lastAt)
		message = "OpenAI Codex SSE stream timed out while waiting for the first event"
	}
	timer := time.NewTimer(max(timeout, 0))
	defer timer.Stop()
	for {
		select {
		case item, ok := <-s.events:
			if !ok {
				return nil, nil
			}
			if item.err != nil {
				return nil, item.err
			}
			if s.first || websocketProgressEvents[gjson.GetBytes(item.event, "type").String()] {
				s.lastAt = time.Now()
			}
			s.first = false
			return item.event, nil
		case <-timer.C:
			s.close()
			return nil, errors.New(message)
		}
	}
}

func (s *sseStream) close() {
	s.once.Do(func() {
		close(s.stopped)
		s.cancel()
		_ = s.body.Close()
	})
}

// steerPump is CodexSteerPump: it submits caller steering to one response.
type steerPump struct {
	stop       context.CancelFunc
	done       chan struct{}
	responseID string
	accepted   []acceptedSteer
	uncertain  bool
}

// startSteering is #startSteering: steering begins once response.created
// names the websocket response it may steer.
func (r *turnRun) startSteering(root gjson.Result) {
	responseID := root.Get("response.id")
	if r.Steering == nil || r.state == nil || r.pump != nil || responseID.Type != gjson.String ||
		r.transport != TransportWebsocket || r.Model == nil || r.Model.Compat == nil || !r.Model.Compat.SupportsSteering {
		return
	}
	r.Sessions.mu.Lock()
	socket := r.state.socket
	unsupported := r.state.steeringUnsupported
	r.Sessions.mu.Unlock()
	if socket == nil || unsupported {
		return
	}
	ctx, cancel := context.WithCancel(r.ctx)
	pump := &steerPump{stop: cancel, done: make(chan struct{}), responseID: responseID.String()}
	r.pump, r.pumpSocket = pump, socket
	go pump.loop(ctx, r.ctx, r.Steering, socket)
}

func (p *steerPump) loop(ctx, requestCtx context.Context, source SteerSource, socket *Socket) {
	defer close(p.done)
	for ctx.Err() == nil {
		claim, ok := source.Claim(ctx)
		if !ok {
			return
		}
		input := toSteerInputItems(claim.Input)
		if input == nil || ctx.Err() != nil {
			claim.Resolve(false, nil)
			return
		}
		frame, err := SteerFrame(p.responseID, input)
		if err != nil {
			claim.Resolve(false, nil)
			return
		}
		ack := socket.Steer(requestCtx, p.responseID, frame)
		if ack.err != nil {
			p.uncertain = true
			claim.Resolve(false, nil)
			return
		}
		if !ack.accepted {
			claim.Resolve(false, ack.raw)
			return
		}
		p.accepted = append(p.accepted, acceptedSteer{id: ack.id, items: input})
		claim.Resolve(true, ack.raw)
	}
}

func (p *steerPump) finish() {
	p.stop()
	<-p.done
}

// toSteerInputItems admits only plain user messages.
func toSteerInputItems(items []json.RawMessage) []json.RawMessage {
	if len(items) == 0 {
		return nil
	}
	for _, raw := range items {
		item := gjson.ParseBytes(raw)
		kind := item.Get("type")
		if item.Get("role").String() != "user" || (kind.Exists() && kind.Type != gjson.Null && kind.String() != "message") {
			return nil
		}
	}
	return items
}

func (r *turnRun) retireSteering() {
	pump := r.pump
	if pump == nil {
		return
	}
	r.pump = nil
	pump.finish()
	if len(pump.accepted) > 0 || pump.uncertain {
		r.steerTainted = true
	}
}

// settleSteering is #settleSteering: accepted steering on a completed,
// chainable response carries to the next request on the same socket.
func (r *turnRun) settleSteering(completed bool) {
	pump := r.pump
	r.pump = nil
	var accepted []acceptedSteer
	uncertain := false
	responseID := ""
	if pump != nil {
		pump.finish()
		accepted, uncertain, responseID = pump.accepted, pump.uncertain, pump.responseID
	}
	tainted := r.steerTainted || uncertain
	socket := r.pumpSocket
	if r.state == nil || socket == nil || (len(accepted) == 0 && !tainted) {
		return
	}
	r.Sessions.mu.Lock()
	if completed && !tainted && r.state.canAppend && r.state.socket == socket && r.state.lastResponseID == responseID {
		r.state.accepted = &acceptedSteering{socket: socket, steers: accepted}
		r.Sessions.mu.Unlock()
		return
	}
	if r.state.socket == socket {
		r.state.socket = nil
	}
	r.state.resetAppend()
	r.Sessions.mu.Unlock()
	socket.Close("steering-discard")
}
