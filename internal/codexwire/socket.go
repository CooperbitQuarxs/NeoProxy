package codexwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NeoPi's Codex WebSocket liveness constants (openai-codex-responses.ts). These
// deadlines are the provider's transport-selection signals: they decide when a
// socket is replaced or the turn falls back to SSE.
const (
	websocketConnectTimeout = 10 * time.Second
	websocketPingInterval   = 10 * time.Second
	websocketPongTimeout    = 60 * time.Second
	websocketQueueCapacity  = 4096
	websocketMaxIdleReuse   = 30 * time.Second
	websocketIdleTimeout    = 300 * time.Second
	websocketFirstEvent     = 300 * time.Second
	steerAckTimeout         = 10 * time.Second
)

// TransportError is CodexWebSocketTransportError: a failure of the socket
// itself rather than of the response it carried.
type TransportError struct{ Message string }

func (e *TransportError) Error() string { return e.Message }

func transportErrorf(format string, args ...any) *TransportError {
	return &TransportError{Message: fmt.Sprintf(format, args...)}
}

// SteerCommitError is CodexSteerCommitError: accepted steering the server
// never committed to a successor response.
type SteerCommitError struct{ Message string }

func (e *SteerCommitError) Error() string { return e.Message }

// isFatalTransport reports CODEX_WEBSOCKET_FATAL_PATTERNS: failures that
// disable the websocket for the session at once.
func isFatalTransport(err error) bool {
	message := strings.ToLower(err.Error())
	for _, pattern := range []string{"websocket error:", "websocket closed before open", "connection timeout"} {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

// isRetryableTransport is isCodexWebSocketRetryableStreamError.
func isRetryableTransport(err error) bool {
	var transport *TransportError
	if !errors.As(err, &transport) {
		return false
	}
	message := strings.ToLower(transport.Message)
	for _, pattern := range []string{
		"websocket closed (", "websocket closed before response completion", "websocket connection is unavailable",
		"websocket send failed", "websocket ping failed", "websocket pong timeout", "websocket message queue exceeded",
		"websocket request already in progress", "idle timeout waiting for websocket", "timeout waiting for first websocket event",
		"syntaxerror", "json",
	} {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

var websocketProgressEvents = map[string]bool{
	"response.created": true, "response.output_item.added": true, "response.reasoning_summary_part.added": true,
	"response.reasoning_summary_text.delta": true, "response.reasoning_summary_text.done": true,
	"response.reasoning_summary_part.done": true, "response.reasoning_text.delta": true,
	"response.content_part.added": true, "response.output_text.delta": true, "response.refusal.delta": true,
	"response.function_call_arguments.delta": true, "response.function_call_arguments.done": true,
	"response.custom_tool_call_input.delta": true, "response.custom_tool_call_input.done": true,
	"response.output_item.done": true, "response.completed": true, "response.incomplete": true,
	"response.failed": true, "error": true, "response.done": true,
}

// DialFunc opens the upgrade; the executor supplies proxy-aware dialing.
type DialFunc func(ctx context.Context, url string, header http.Header) (*websocket.Conn, *http.Response, error)

type socketItem struct {
	event []byte
	err   error
	end   bool
}

type steerWaiter struct {
	previousResponseID string
	done               chan steerAck
}

type steerAck struct {
	accepted bool
	id       string
	code     string
	message  string
	err      error
	// raw is the acknowledgement frame, absent for a native-lane refusal.
	raw []byte
}

// Socket is CodexWebSocketConnection: one upstream socket reused across a
// session's turns while healthy.
type Socket struct {
	url         string
	header      http.Header
	dial        DialFunc
	onHandshake func(http.Header)

	mu                 sync.Mutex
	conn               *websocket.Conn
	connecting         chan struct{}
	connectErr         error
	queue              []socketItem
	notify             chan struct{}
	active             bool
	lastPingAt         time.Time
	lastSeenResponseID string
	steerWaiters       []*steerWaiter
	acceptedSteers     map[string]bool
	failedSteers       map[string]bool
	attachSteerIDs     map[string]bool
	stopHeartbeat      chan struct{}
	writeMu            sync.Mutex
	lastInboundAt      atomic.Int64
}

func newSocket(url string, header http.Header, dial DialFunc, onHandshake func(http.Header)) *Socket {
	return &Socket{
		url: url, header: header.Clone(), dial: dial, onHandshake: onHandshake,
		notify: make(chan struct{}, 1), acceptedSteers: map[string]bool{}, failedSteers: map[string]bool{},
	}
}

func (s *Socket) isOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil
}

func (s *Socket) isConnecting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connecting != nil
}

func (s *Socket) isBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active || s.connecting != nil
}

// isHealthyForReuse refuses sockets silent past the idle-reuse window.
func (s *Socket) isHealthyForReuse(now time.Time) bool {
	if !s.isOpen() {
		return false
	}
	last := s.lastInboundAt.Load()
	return last != 0 && now.Sub(time.UnixMilli(last)) <= websocketMaxIdleReuse
}

func (s *Socket) matchesAuth(header http.Header) bool {
	return s.header.Get("Authorization") == header.Get("Authorization")
}

// Close is close(reason): the socket stops and pending steering is rejected.
func (s *Socket) Close(reason string) {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.haltHeartbeatLocked()
	waiters := s.takeSteerWaitersLocked()
	s.mu.Unlock()
	rejectSteerWaiters(waiters, fmt.Sprintf("websocket closed (%s)", reason))
	if conn != nil {
		s.writeMu.Lock()
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason), time.Now().Add(time.Second))
		s.writeMu.Unlock()
		_ = conn.Close()
	}
}

func (s *Socket) connect(ctx context.Context) error {
	s.mu.Lock()
	if s.conn != nil {
		s.mu.Unlock()
		return nil
	}
	if pending := s.connecting; pending != nil {
		s.mu.Unlock()
		select {
		case <-pending:
		case <-ctx.Done():
			return transportErrorf("request was aborted")
		}
		s.mu.Lock()
		err := s.connectErr
		s.mu.Unlock()
		return err
	}
	done := make(chan struct{})
	s.connecting = done
	s.mu.Unlock()

	dialCtx, cancel := context.WithTimeout(ctx, websocketConnectTimeout)
	conn, response, err := s.dial(dialCtx, s.url, s.header)
	cancel()
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	switch {
	case err == nil:
	case ctx.Err() != nil:
		err = transportErrorf("request was aborted")
	case errors.Is(dialCtx.Err(), context.DeadlineExceeded):
		err = transportErrorf("connection timeout")
	case response != nil:
		err = transportErrorf("websocket error: Expected 101 status code, got %d", response.StatusCode)
	default:
		err = transportErrorf("websocket error: %v", err)
	}

	s.mu.Lock()
	s.connecting = nil
	s.connectErr = err
	if err == nil {
		conn.EnableWriteCompression(false)
		s.conn = conn
		s.lastInboundAt.Store(time.Now().UnixMilli())
		s.startHeartbeatLocked(conn)
	}
	s.mu.Unlock()
	close(done)
	if err != nil {
		return err
	}
	conn.SetPongHandler(func(string) error {
		s.lastInboundAt.Store(time.Now().UnixMilli())
		return nil
	})
	if s.onHandshake != nil && response != nil {
		s.onHandshake(response.Header)
	}
	go s.readLoop(conn)
	return nil
}

func (s *Socket) readLoop(conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			code := websocket.CloseAbnormalClosure
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				code = closeErr.Code
			}
			s.mu.Lock()
			current := s.conn == conn
			if current {
				s.conn = nil
				s.haltHeartbeatLocked()
			}
			waiters := s.takeSteerWaitersLocked()
			s.mu.Unlock()
			rejectSteerWaiters(waiters, fmt.Sprintf("websocket closed (%d)", code))
			if current {
				s.push(socketItem{err: transportErrorf("websocket closed (%d)", code)})
				s.push(socketItem{end: true})
			}
			return
		}
		s.lastInboundAt.Store(time.Now().UnixMilli())
		s.handleMessage(data)
	}
}

func (s *Socket) handleMessage(data []byte) {
	if len(data) == 0 {
		return
	}
	if !json.Valid(data) || !gjson.ParseBytes(data).IsObject() {
		s.push(socketItem{err: transportErrorf("SyntaxError: JSON Parse error: Unable to parse JSON string")})
		return
	}
	root := gjson.ParseBytes(data)
	eventType := root.Get("type").String()
	if eventType == "error" && root.Get("error").IsObject() {
		if code := root.Get("error.code"); code.Type == gjson.String && root.Get("code").Type != gjson.String {
			data, _ = sjson.SetBytes(data, "code", code.String())
		}
		if message := root.Get("error.message"); message.Type == gjson.String && root.Get("message").Type != gjson.String {
			data, _ = sjson.SetBytes(data, "message", message.String())
		}
		root = gjson.ParseBytes(data)
	}
	if strings.HasPrefix(eventType, "response.steer.") {
		s.handleSteerEvent(root, data)
		return
	}
	if eventType == "error" && root.Get("code").String() == nativeLaneSteerRejectedCode {
		s.refuseSteerWaiters(root.Get("code").String(), root.Get("message").String())
	}
	s.push(socketItem{event: data})
}

func (s *Socket) push(item socketItem) {
	s.mu.Lock()
	if item.err == nil && !item.end && len(s.queue) >= websocketQueueCapacity {
		s.mu.Unlock()
		s.failQueue(transportErrorf("websocket message queue exceeded %d items", websocketQueueCapacity), "queue-overflow")
		return
	}
	s.queue = append(s.queue, item)
	s.mu.Unlock()
	s.wake()
}

func (s *Socket) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *Socket) failQueue(err error, reason string) {
	s.mu.Lock()
	s.queue = append(s.queue[:0], socketItem{err: err})
	s.mu.Unlock()
	s.Close(reason)
	s.wake()
}

func (s *Socket) dropStaleFrames() {
	s.mu.Lock()
	defer s.mu.Unlock()
	surviving := s.queue[:0]
	for _, item := range s.queue {
		if item.err != nil {
			surviving = append(surviving, item)
		}
	}
	s.queue = surviving
}

func (s *Socket) nextMessage(ctx context.Context, timeout time.Duration, reason string) socketItem {
	var deadline <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			item := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			return item
		}
		s.mu.Unlock()
		select {
		case <-s.notify:
		case <-deadline:
			s.mu.Lock()
			empty := len(s.queue) == 0
			s.mu.Unlock()
			if empty {
				return socketItem{err: transportErrorf("%s", reason)}
			}
		case <-ctx.Done():
			s.push(socketItem{err: transportErrorf("request was aborted")})
			s.Close("aborted")
		}
	}
}

func (s *Socket) startHeartbeatLocked(conn *websocket.Conn) {
	s.haltHeartbeatLocked()
	stop := make(chan struct{})
	s.stopHeartbeat = stop
	s.lastPingAt = time.Time{}
	go func() {
		ticker := time.NewTicker(websocketPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			s.mu.Lock()
			if s.conn != conn {
				s.mu.Unlock()
				return
			}
			lastPing := s.lastPingAt
			s.mu.Unlock()
			lastInbound := time.UnixMilli(s.lastInboundAt.Load())
			if !lastPing.IsZero() && lastPing.After(lastInbound) && time.Since(lastPing) > websocketPongTimeout {
				s.failQueue(transportErrorf("websocket pong timeout"), "pong-timeout")
				return
			}
			s.writeMu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(websocketPingInterval))
			s.writeMu.Unlock()
			if err != nil {
				s.failQueue(transportErrorf("websocket ping failed: %v", err), "ping-failed")
				return
			}
			s.mu.Lock()
			s.lastPingAt = time.Now()
			s.mu.Unlock()
		}
	}()
}

func (s *Socket) haltHeartbeatLocked() {
	if s.stopHeartbeat != nil {
		close(s.stopHeartbeat)
		s.stopHeartbeat = nil
	}
	s.lastPingAt = time.Time{}
}

func (s *Socket) send(payload []byte) error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return transportErrorf("websocket connection is unavailable")
	}
	s.writeMu.Lock()
	err := conn.WriteMessage(websocket.TextMessage, payload)
	s.writeMu.Unlock()
	if err != nil {
		return transportErrorf("websocket send failed: %v", err)
	}
	return nil
}

// Steer is steer(previousResponseId, input): it sends response.steer and waits
// for the matching acknowledgement.
func (s *Socket) Steer(ctx context.Context, previousResponseID string, frame []byte) steerAck {
	waiter := &steerWaiter{previousResponseID: previousResponseID, done: make(chan steerAck, 1)}
	s.mu.Lock()
	if s.conn == nil {
		s.mu.Unlock()
		return steerAck{err: transportErrorf("websocket connection is unavailable")}
	}
	s.steerWaiters = append(s.steerWaiters, waiter)
	s.mu.Unlock()
	if err := s.send(frame); err != nil {
		s.mu.Lock()
		for i, candidate := range s.steerWaiters {
			if candidate == waiter {
				s.steerWaiters = append(s.steerWaiters[:i], s.steerWaiters[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		return steerAck{err: err}
	}
	timer := time.NewTimer(steerAckTimeout)
	defer timer.Stop()
	select {
	case ack := <-waiter.done:
		return ack
	case <-timer.C:
		return steerAck{err: transportErrorf("steering acknowledgement timed out")}
	case <-ctx.Done():
		return steerAck{err: transportErrorf("request was aborted")}
	}
}

func (s *Socket) hasFailedSteer(ids []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if s.failedSteers[id] {
			return true
		}
	}
	return false
}

func (s *Socket) handleSteerEvent(event gjson.Result, raw []byte) {
	id := event.Get("steer.id").String()
	previous := event.Get("steer.previous_response_id").String()
	switch event.Get("type").String() {
	case "response.steer.accepted":
		if id == "" {
			return
		}
		s.mu.Lock()
		s.acceptedSteers[id] = true
		waiter := s.takeSteerWaiterLocked(previous)
		s.mu.Unlock()
		if waiter != nil {
			waiter.done <- steerAck{accepted: true, id: id, raw: raw}
		}
	case "response.steer.failed":
		code, message := event.Get("error.code").String(), event.Get("error.message").String()
		s.mu.Lock()
		if id != "" && s.acceptedSteers[id] {
			s.failedSteers[id] = true
			attached := s.attachSteerIDs[id]
			s.mu.Unlock()
			if attached {
				reason := code
				if reason == "" {
					reason = message
				}
				if reason == "" {
					reason = "unknown"
				}
				s.push(socketItem{err: &SteerCommitError{Message: fmt.Sprintf("steering %s was not committed: %s", id, reason)}})
			}
			return
		}
		waiter := s.takeSteerWaiterLocked(previous)
		s.mu.Unlock()
		if waiter != nil {
			waiter.done <- steerAck{code: code, message: message, raw: raw}
		}
	}
}

func (s *Socket) takeSteerWaiterLocked(previous string) *steerWaiter {
	for i, waiter := range s.steerWaiters {
		if waiter.previousResponseID == previous {
			s.steerWaiters = append(s.steerWaiters[:i], s.steerWaiters[i+1:]...)
			return waiter
		}
	}
	return nil
}

func (s *Socket) takeSteerWaitersLocked() []*steerWaiter {
	waiters := s.steerWaiters
	s.steerWaiters = nil
	return waiters
}

func (s *Socket) refuseSteerWaiters(code, message string) {
	s.mu.Lock()
	waiters := s.takeSteerWaitersLocked()
	s.mu.Unlock()
	for _, waiter := range waiters {
		waiter.done <- steerAck{code: code, message: message}
	}
}

func rejectSteerWaiters(waiters []*steerWaiter, reason string) {
	for _, waiter := range waiters {
		waiter.done <- steerAck{err: transportErrorf("%s", reason)}
	}
}

// socketStream is one streamRequest: it reads the response a request created.
type socketStream struct {
	socket         *Socket
	ctx            context.Context
	sawFirst       bool
	lastProgressAt time.Time
	lastProgress   string
	lastEventAt    time.Time
	lastEvent      string
	activeID       string
	priorID        string
	lastSequence   int64
	haveSequence   bool
	done           bool
}

// stream starts streamRequest: send is the response.create frame, or nil to
// attach to the successor accepted steering started.
func (s *Socket) stream(ctx context.Context, send []byte, attach []string) (*socketStream, error) {
	s.mu.Lock()
	if s.conn == nil {
		s.mu.Unlock()
		return nil, transportErrorf("websocket connection is unavailable")
	}
	if s.active {
		s.mu.Unlock()
		return nil, transportErrorf("websocket request already in progress")
	}
	if ctx.Err() != nil {
		s.mu.Unlock()
		return nil, transportErrorf("request was aborted")
	}
	s.active = true
	prior := s.lastSeenResponseID
	if send == nil {
		s.attachSteerIDs = map[string]bool{}
		for _, id := range attach {
			s.attachSteerIDs[id] = true
		}
	}
	s.mu.Unlock()
	stream := &socketStream{socket: s, ctx: ctx, priorID: prior}
	if send != nil {
		s.dropStaleFrames()
		if err := s.send(send); err != nil {
			stream.finish()
			return nil, err
		}
	} else if s.hasFailedSteer(attach) {
		stream.finish()
		return nil, &SteerCommitError{Message: "accepted steering was not committed to a successor response"}
	}
	now := time.Now()
	stream.lastProgressAt, stream.lastEventAt = now, now
	return stream, nil
}

func (stream *socketStream) timeoutMessage(reason string) string {
	now := time.Now()
	describe := func(kind string, at time.Time) string {
		if kind == "" {
			return "none"
		}
		return fmt.Sprintf("%s %dms ago", kind, max(0, now.Sub(at).Milliseconds()))
	}
	return fmt.Sprintf("%s (last event: %s; last progress: %s)", reason, describe(stream.lastEvent, stream.lastEventAt), describe(stream.lastProgress, stream.lastProgressAt))
}

// next yields the following event of this response; the terminal event ends it.
func (stream *socketStream) next() ([]byte, error) {
	if stream.done {
		return nil, nil
	}
	for {
		var timeout time.Duration
		var reason string
		if stream.sawFirst {
			reason = stream.timeoutMessage("idle timeout waiting for websocket")
			timeout = websocketIdleTimeout - time.Since(stream.lastProgressAt)
			if timeout <= 0 {
				return nil, transportErrorf("%s", reason)
			}
		} else {
			reason = stream.timeoutMessage("timeout waiting for first websocket event")
			timeout = websocketFirstEvent
		}
		item := stream.socket.nextMessage(stream.ctx, timeout, reason)
		if item.err != nil {
			return nil, item.err
		}
		if item.end {
			return nil, transportErrorf("websocket closed before response completion")
		}
		root := gjson.ParseBytes(item.event)
		eventType := root.Get("type").String()
		if id := root.Get("response.id"); id.Type == gjson.String && id.String() != "" {
			frameID := id.String()
			if stream.activeID == "" {
				if stream.priorID != "" && frameID == stream.priorID {
					continue
				}
				stream.activeID = frameID
			} else if frameID != stream.activeID {
				stream.socket.Close("stale-frame")
				return nil, transportErrorf("websocket frame for response %s interleaved into active response %s", frameID, stream.activeID)
			}
			stream.socket.mu.Lock()
			stream.socket.lastSeenResponseID = frameID
			stream.socket.mu.Unlock()
		}
		if sequence := root.Get("sequence_number"); sequence.Type == gjson.Number {
			value := int64(sequence.Float())
			if stream.activeID != "" && stream.haveSequence && value < stream.lastSequence {
				stream.socket.Close("stale-frame")
				return nil, transportErrorf("websocket sequence_number %d regressed below %d within response %s", value, stream.lastSequence, stream.activeID)
			}
			stream.lastSequence, stream.haveSequence = value, true
		}
		stream.sawFirst = true
		stream.lastEventAt = time.Now()
		stream.lastEvent = eventType
		if websocketProgressEvents[eventType] {
			stream.lastProgressAt, stream.lastProgress = stream.lastEventAt, eventType
		}
		switch eventType {
		case "response.completed", "response.done", "response.incomplete", "response.failed", "error":
			stream.done = true
		}
		return item.event, nil
	}
}

// finish is streamRequest's finally block.
func (stream *socketStream) finish() {
	stream.socket.mu.Lock()
	stream.socket.active = false
	stream.socket.attachSteerIDs = nil
	stream.socket.mu.Unlock()
}
