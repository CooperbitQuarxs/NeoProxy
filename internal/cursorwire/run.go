package cursorwire

import (
	"bufio"
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Port of streamCursorWithWireMode: one Cursor Agent turn over the Run
// bidi stream, with the HTTP/1 RunSSE/BidiAppend fallback, checkpoint-resumed
// retries, the discovered-id retry and #8345 conversation rotation.

const (
	maxStreamRetries      = 5
	retryBaseDelay        = 500 * time.Millisecond
	maxRetryDelay         = 60 * time.Second
	heartbeatInterval     = 5 * time.Second
	conversationCacheSize = 128
	http1AppendConcurrent = 16
	http1AppendBaseWait   = 60 * time.Second
	http1AppendBytesPerS  = 128 * 1024
)

type conversationEntry struct {
	state *ConversationStateStructure
	blobs *Blobs
}

// Conversations is the process-wide conversation cache (an LRU of 128
// checkpoints and blob stores) and the #8345 rotation bookkeeping.
type Conversations struct {
	mu                sync.Mutex
	order             *list.List
	entries           map[string]*list.Element
	rotated           map[string]string
	successfulRotated map[string]bool
	freshRotated      map[string]bool
}

type lruItem struct {
	key   string
	entry *conversationEntry
}

// NewConversations is an empty cache.
func NewConversations() *Conversations {
	return &Conversations{order: list.New(), entries: map[string]*list.Element{}, rotated: map[string]string{},
		successfulRotated: map[string]bool{}, freshRotated: map[string]bool{}}
}

func (c *Conversations) get(key string) *conversationEntry {
	element, ok := c.entries[key]
	if !ok {
		return nil
	}
	c.order.MoveToFront(element)
	return element.Value.(*lruItem).entry
}

func (c *Conversations) set(key string, entry *conversationEntry) {
	if element, ok := c.entries[key]; ok {
		element.Value.(*lruItem).entry = entry
		c.order.MoveToFront(element)
		return
	}
	c.entries[key] = c.order.PushFront(&lruItem{key: key, entry: entry})
	for c.order.Len() > conversationCacheSize {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*lruItem).key)
	}
}

func (c *Conversations) delete(key string) {
	if element, ok := c.entries[key]; ok {
		c.order.Remove(element)
		delete(c.entries, key)
	}
}

// RunInput is one turn's request.
type RunInput struct {
	Model       *Model
	Context     *Context
	WireModelID string
	// ConversationKey is the caller's session id (the base conversation id).
	ConversationKey string
	APIKey          string
	// Do sends one HTTP request through the account's transport.
	Do      func(*http.Request) (*http.Response, error)
	Sink    Sink
	NewUUID func() string
	// ToolSchema renders a tool's wire schema (toolWireSchema, projected
	// when the model requires it).
	ToolSchema func(Tool) any
	// OnPayload edits the run request before serialization (user payload rules).
	OnPayload func(*AgentRunRequest)
	// Wait sleeps between retries; nil uses a timer.
	Wait func(context.Context, time.Duration) error
	// Decode undoes a response Content-Encoding (Bun fetch asks for gzip,
	// deflate, br and zstd on the RunSSE path).
	Decode func(body io.ReadCloser, encoding string) (io.ReadCloser, error)
	// OnRequest observes each request sent (logging).
	OnRequest func(url string, header http.Header, body []byte)
}

type retryState struct {
	attempt           int
	originalRequestID string
	requestIDs        []string
	checkpoint        *ConversationStateStructure
	checkpointHash    string
	noProgressResumes int
	baseConversation  string
	conversation      string
	blobs             *Blobs
}

// Run streams one turn; the returned error is a *RunError or the context's.
func (c *Conversations) Run(ctx context.Context, in *RunInput) (*Turn, error) {
	turn := NewTurn(in.Sink, in.NewUUID)
	mode := WireNormalized
	transport := "http2"
	var retry *retryState
	for {
		next, err := c.attempt(ctx, in, turn, mode, transport, retry)
		if next == nil {
			return turn, err
		}
		mode, transport, retry = next.mode, next.transport, next.retry
		if next.freshTurn {
			turn = NewTurn(in.Sink, in.NewUUID)
		}
	}
}

type nextAttempt struct {
	mode      WireMode
	transport string
	retry     *retryState
	freshTurn bool
}

func (c *Conversations) attempt(ctx context.Context, in *RunInput, turn *Turn, mode WireMode, transportMode string, retry *retryState) (*nextAttempt, error) {
	knownConversation := retry != nil
	var baseConversation, conversation string
	if retry != nil {
		baseConversation, conversation = retry.baseConversation, retry.conversation
	} else {
		baseConversation = in.ConversationKey
		c.mu.Lock()
		conversation = baseConversation
		if rotated, ok := c.rotated[baseConversation]; ok {
			conversation = rotated
		}
		c.mu.Unlock()
	}
	c.mu.Lock()
	rotatedFresh := retry == nil && c.freshRotated[conversation]
	cached := c.get(conversation)
	if cached != nil {
		knownConversation = true
	}
	var blobs *Blobs
	switch {
	case retry != nil:
		blobs = retry.blobs
	case cached != nil:
		blobs = cached.blobs
	default:
		blobs = NewBlobs()
	}
	entry := &conversationEntry{blobs: blobs}
	if cached != nil {
		entry.state = cached.state
	}
	c.set(conversation, entry)
	var cachedState *ConversationStateStructure
	if retry != nil && retry.checkpoint != nil {
		cachedState = retry.checkpoint
	} else if !rotatedFresh && cached != nil {
		cachedState = cached.state
	}
	c.mu.Unlock()

	built, errBuild := BuildRunRequest(in.Model, in.Context, in.WireModelID, &RequestState{ConversationID: conversation, Blobs: blobs,
		ConversationState: cachedState, Resume: retry != nil && retry.checkpoint != nil}, mode, in.NewUUID, in.OnPayload)
	if errBuild != nil {
		var history *HistoryError
		if errors.As(errBuild, &history) {
			return nil, &RunError{Message: history.Message, Status: 400, Kind: "validation"}
		}
		return nil, &RunError{Message: errBuild.Error(), Kind: "runtime"}
	}
	c.mu.Lock()
	entry.state = built.ConversationState
	c.mu.Unlock()

	attempt := NewAttempt(turn)
	attempt.Blobs = blobs
	attempt.Tools = MCPToolDefinitions(in.Context.Tools, in.ToolSchema)
	attempt.Rules = RequestContextRules(in.Context.SystemPrompt)
	attempt.ExternalToolExecutor = true
	attempt.OnCheckpoint = func(checkpoint *ConversationStateStructure) {
		c.mu.Lock()
		entry.state = checkpoint
		c.set(conversation, entry)
		c.mu.Unlock()
	}

	baseURL := strings.TrimRight(in.Model.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	requestID := in.NewUUID()
	originalRequestID := requestID
	var requestIDs []string
	if retry != nil {
		originalRequestID = retry.originalRequestID
		requestIDs = retry.requestIDs
	}
	requestIDs = append(append([]string(nil), requestIDs...), requestID)
	header := http.Header{}
	SetClientHeaders(header, in.APIKey, ContentTypeConnectProto)
	header.Set("Connect-Protocol-Version", "1")
	header.Set("X-Request-Id", requestID)
	if retry != nil {
		header.Set("X-Original-Request-Id", originalRequestID)
	}
	// Node's HTTP/2 client sends no user agent.
	header["User-Agent"] = []string{""}

	errRun := c.stream(ctx, in, attempt, baseURL, header, requestID, built.Bytes, transportMode)
	if errRun == nil {
		c.mu.Lock()
		if conversation != baseConversation {
			c.successfulRotated[conversation] = true
			delete(c.freshRotated, conversation)
		}
		c.mu.Unlock()
		turn.Flush()
		return nil, nil
	}
	if ctx.Err() != nil {
		turn.Flush()
		return nil, ctx.Err()
	}
	runErr := asRunError(errRun)

	if runErr.Kind == "h2-unavailable" && !attempt.SawProgressOrSideEffect {
		return &nextAttempt{mode: mode, transport: "http1", retry: &retryState{
			attempt: retryAttempt(retry), originalRequestID: originalRequestID, requestIDs: requestIDs,
			checkpoint: retryCheckpoint(retry), checkpointHash: retryCheckpointHash(retry), noProgressResumes: retryNoProgress(retry),
			baseConversation: baseConversation, conversation: conversation, blobs: blobs,
		}}, nil
	}
	if mode == WireNormalized && !attempt.SawProgressOrSideEffect && runErr.modelNotFound() && built.FallbackWireModelID != "" {
		return &nextAttempt{mode: WireDiscovered, transport: transportMode, freshTurn: true}, nil
	}

	attemptNumber := retryAttempt(retry)
	freshCheckpoint := attempt.LatestCheckpoint != nil && attempt.LatestCheckpointProgressVersion == attempt.ProgressVersion &&
		!attempt.LatestCheckpointTerminal
	replaySafe := freshCheckpoint || !attempt.SawProgressOrSideEffect
	checkpoint := retryCheckpoint(retry)
	if freshCheckpoint {
		checkpoint = attempt.LatestCheckpoint
	}
	hash := ""
	if checkpoint != nil {
		sum := sha256.Sum256(checkpoint.Marshal())
		hash = string(sum[:])
	}
	noProgress := 0
	if hash != "" && retry != nil && hash == retry.checkpointHash {
		noProgress = retry.noProgressResumes + 1
	}
	if replaySafe && attemptNumber < maxStreamRetries && noProgress <= 2 && (runErr.Retryable() || runErr.Kind == "incomplete-stream") {
		delay := retryBaseDelay << attemptNumber
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
		wait := in.Wait
		if wait == nil {
			wait = sleep
		}
		if errWait := wait(ctx, delay); errWait == nil {
			return &nextAttempt{mode: mode, transport: transportMode, retry: &retryState{
				attempt: attemptNumber + 1, originalRequestID: originalRequestID, requestIDs: requestIDs, checkpoint: checkpoint,
				checkpointHash: hash, noProgressResumes: noProgress, baseConversation: baseConversation, conversation: conversation, blobs: blobs,
			}}, nil
		} else if ctx.Err() != nil {
			turn.Flush()
			return nil, ctx.Err()
		}
	}
	turn.Flush()

	c.mu.Lock()
	currentRotated, hasRotated := c.rotated[baseConversation]
	canRotate := !hasRotated || c.successfulRotated[currentRotated]
	rotatedRejection := false
	if !turn.SawTokenDelta() && runErr.conversationRejected() && canRotate {
		rotated := in.NewUUID()
		if hasRotated {
			delete(c.successfulRotated, currentRotated)
		}
		c.rotated[baseConversation] = rotated
		c.freshRotated[rotated] = true
		c.delete(conversation)
		rotatedRejection = knownConversation
	}
	c.mu.Unlock()
	if rotatedRejection {
		// The fresh id is a different request: mark this failure transient
		// so the caller's retry rebuilds on it.
		return nil, &RunError{Message: runErr.Message, Diagnostic: runErr.Diagnostic, Status: 503, Code: runErr.Code, Kind: "rotated", Transient: true}
	}
	return nil, runErr
}

func retryAttempt(retry *retryState) int {
	if retry == nil {
		return 0
	}
	return retry.attempt
}

func retryCheckpoint(retry *retryState) *ConversationStateStructure {
	if retry == nil {
		return nil
	}
	return retry.checkpoint
}

func retryCheckpointHash(retry *retryState) string {
	if retry == nil {
		return ""
	}
	return retry.checkpointHash
}

func retryNoProgress(retry *retryState) int {
	if retry == nil {
		return 0
	}
	return retry.noProgressResumes
}

func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func asRunError(err error) *RunError {
	var runErr *RunError
	if errors.As(err, &runErr) {
		return runErr
	}
	return &RunError{Message: err.Error(), Kind: "transport"}
}

// stream runs one request on the chosen transport and settles it.
func (c *Conversations) stream(ctx context.Context, in *RunInput, attempt *Attempt, baseURL string, header http.Header, requestID string, request []byte, transportMode string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var transport runTransport
	var body io.ReadCloser
	var trailer func() http.Header
	var err error
	if transportMode == "http1" {
		transport, body, err = openHTTP1(ctx, in, baseURL, header, requestID, request)
		trailer = func() http.Header { return nil }
	} else {
		var response *http.Response
		transport, response, err = openHTTP2(ctx, in, baseURL, header, request)
		if response != nil {
			body = response.Body
			trailer = func() http.Header { return response.Trailer }
		}
	}
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	defer transport.close()
	attempt.WriteRaw = transport.write
	attempt.WriteResult = transport.write

	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		frame := HeartbeatFrame()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				transport.write(frame)
			}
		}
	}()

	reader := bufio.NewReaderSize(body, 64*1024)
	var transportErr error
	for {
		if !frameBuffered(reader) {
			attempt.FlushDeferred()
		}
		flags, payload, errFrame := readFrame(reader)
		if errFrame != nil {
			if !errors.Is(errFrame, io.EOF) {
				if failure := transport.failure(); failure != nil {
					transportErr = failure
				} else {
					transportErr = &RunError{Message: errFrame.Error(), Kind: "transport"}
				}
			}
			break
		}
		if attempt.HandleFrame(flags, payload) {
			if attempt.EndStreamError != nil {
				transport.close()
				break
			}
			transport.end()
		}
	}
	attempt.FlushDeferred()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if transportErr == nil {
		if failure := transport.failure(); failure != nil {
			transportErr = failure
		}
	}
	if transportErr == nil && attempt.EndStreamError == nil {
		if trailers := trailer(); trailers != nil {
			if status := trailers.Get("Grpc-Status"); status != "" && status != "0" {
				message, _ := url.PathUnescape(trailers.Get("Grpc-Message"))
				attempt.EndStreamError = grpcTrailerError(status, message)
			}
		}
	}
	return settle(attempt, transportErr)
}

// settle is settleH2.
func settle(attempt *Attempt, transportErr error) error {
	if transportErr != nil || attempt.EndStreamError != nil {
		if attempt.SawTurnEnded && attempt.LatestCheckpointTerminal {
			return nil
		}
		if transportErr != nil {
			return transportErr
		}
		return attempt.EndStreamError
	}
	if !attempt.SawTurnEnded {
		if attempt.StepCompletedIsLatest && attempt.Turn.LastIsText() && !attempt.Turn.OpenToolCalls() {
			return nil
		}
		return &RunError{Message: "Cursor stream ended before turnEnded", Kind: "incomplete-stream"}
	}
	return nil
}

func frameBuffered(reader *bufio.Reader) bool {
	if reader.Buffered() < 5 {
		return false
	}
	header, err := reader.Peek(5)
	if err != nil {
		return false
	}
	return reader.Buffered() >= 5+int(binary.BigEndian.Uint32(header[1:5]))
}

func readFrame(reader *bufio.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, io.EOF
		}
		return 0, nil, err
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[1:5]))
	if _, err := io.ReadFull(reader, payload); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, io.EOF
		}
		return 0, nil, err
	}
	return header[0], payload, nil
}

// runTransport is CursorRunTransport's write side.
type runTransport interface {
	write(frame []byte)
	// end half-closes after the server's end frame; later writes drop.
	end()
	close()
	// failure is a transport-level failure the read side cannot see.
	failure() error
}

type http2Transport struct {
	mu     sync.Mutex
	pipe   *io.PipeWriter
	ended  bool
	cancel context.CancelFunc
}

func (t *http2Transport) write(frame []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ended {
		return
	}
	if _, err := t.pipe.Write(frame); err != nil {
		t.ended = true
	}
}

func (t *http2Transport) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ended {
		t.ended = true
		_ = t.pipe.Close()
	}
}

func (t *http2Transport) close() {
	// Cancelling first unblocks a write parked on the pipe.
	t.cancel()
	t.end()
}

func (t *http2Transport) failure() error { return nil }

// openHTTP2 opens the Run bidi stream. A connection that did not negotiate
// h2 is reported as h2-unavailable so the turn can fall back to RunSSE.
func openHTTP2(ctx context.Context, in *RunInput, baseURL string, header http.Header, request []byte) (runTransport, *http.Response, error) {
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	transport := &http2Transport{pipe: writer, cancel: cancel}
	fail := func(err error) (runTransport, *http.Response, error) {
		cancel()
		_ = reader.CloseWithError(err)
		return nil, nil, err
	}
	var downgraded atomic.Bool
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		if conn, ok := info.Conn.(*tls.Conn); ok && conn.ConnectionState().NegotiatedProtocol != "h2" {
			downgraded.Store(true)
			cancel()
		}
	}}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, baseURL+RunPath, reader)
	if err != nil {
		return fail(err)
	}
	req.Header = header.Clone()
	req.Header.Set("Te", "trailers")
	if in.OnRequest != nil {
		in.OnRequest(req.URL.String(), req.Header, request)
	}
	go transport.write(Frame(request, 0))
	response, err := in.Do(req)
	if downgraded.Load() || (err == nil && response.ProtoMajor < 2) {
		if err == nil {
			_ = response.Body.Close()
		}
		return fail(&RunError{Message: fmt.Sprintf("Cursor run transport could not negotiate HTTP/2 with %s", baseURL), Kind: "h2-unavailable"})
	}
	if err != nil {
		return fail(&RunError{Message: err.Error(), Kind: "transport"})
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		return fail(httpStatusError("Run", response.StatusCode, data))
	}
	return transport, response, nil
}

// httpStatusError classifies a non-2xx RPC response: a Connect error body
// when it has one, else the status.
func httpStatusError(rpc string, status int, body []byte) *RunError {
	if decoded, ok := ParseJSON(strings.ToValidUTF8(string(body), "\uFFFD")); ok {
		if object := asObject(decoded); object != nil {
			if _, hasCode := object.Get("code"); hasCode {
				classified := ClassifyConnectError(object)
				if classified.Status == 0 {
					classified.Status = status
				}
				return classified
			}
		}
	}
	return &RunError{Message: fmt.Sprintf("Cursor %s failed with HTTP %d", rpc, status), Status: status, Kind: "http"}
}

// http1Transport is createHttp1RunTransport's write side: each client
// message is a BidiAppend unary call carrying a sequence number.
type http1Transport struct {
	mu      sync.Mutex
	in      *RunInput
	ctx     context.Context
	cancel  context.CancelFunc
	url     string
	header  http.Header
	request *BidiRequestId
	seqno   int64
	pending [][]byte
	active  int
	closed  bool
	ended   bool
	err     error
}

func (t *http1Transport) write(frame []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.ended {
		return
	}
	if len(frame) < 5 || int(binary.BigEndian.Uint32(frame[1:5])) != len(frame)-5 || frame[0]&FlagEndStream != 0 {
		t.failLocked(&RunError{Message: "Cursor emitted an invalid Connect request frame", Kind: "envelope"})
		return
	}
	message := &BidiAppendRequest{RequestId: t.request, AppendSeqno: t.seqno, DataBinary: frame[5:]}
	t.seqno++
	t.pending = append(t.pending, message.Marshal())
	t.pumpLocked()
}

func (t *http1Transport) pumpLocked() {
	for !t.closed && t.active < http1AppendConcurrent && len(t.pending) > 0 {
		body := t.pending[0]
		t.pending = t.pending[1:]
		t.active++
		go func() {
			err := t.send(body)
			t.mu.Lock()
			defer t.mu.Unlock()
			t.active--
			if err != nil {
				t.failLocked(err)
				return
			}
			t.pumpLocked()
		}()
	}
}

func (t *http1Transport) send(body []byte) error {
	// The append's own deadline: 60s plus a second per 128 KiB.
	timeout := http1AppendBaseWait + time.Duration((len(body)+http1AppendBytesPerS-1)/http1AppendBytesPerS)*time.Second
	ctx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url+BidiAppendPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = t.header.Clone()
	req.Header.Set("Content-Type", ContentTypeProto)
	response, err := t.in.Do(req)
	if err != nil {
		return &RunError{Message: err.Error(), Kind: "transport"}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	classified := httpStatusError("BidiAppend", response.StatusCode, data)
	return classified
}

func (t *http1Transport) failLocked(err error) {
	if t.closed {
		return
	}
	t.closed = true
	t.err = err
	t.pending = nil
	t.cancel()
}

func (t *http1Transport) end() {
	t.mu.Lock()
	t.ended = true
	t.pending = nil
	t.mu.Unlock()
}

func (t *http1Transport) close() {
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		t.pending = nil
		t.cancel()
	}
	t.mu.Unlock()
}

func (t *http1Transport) failure() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// openHTTP1 opens RunSSE: the request body is the framed request id, and the
// run request itself goes out as the first BidiAppend.
func openHTTP1(ctx context.Context, in *RunInput, baseURL string, header http.Header, requestID string, request []byte) (runTransport, io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	id := &BidiRequestId{RequestId: requestID}
	// Both RunSSE and its appends are Bun fetches of the same header set.
	streamHeader := header.Clone()
	streamHeader.Set("X-Cursor-Streaming", "true")
	SetFetchHeaders(streamHeader)
	transport := &http1Transport{in: in, ctx: ctx, cancel: cancel, url: baseURL, header: streamHeader, request: id}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+RunSSEPath, bytes.NewReader(Frame(id.Marshal(), 0)))
	if err != nil {
		cancel()
		return nil, nil, err
	}
	req.Header = streamHeader
	if in.OnRequest != nil {
		in.OnRequest(req.URL.String(), req.Header, request)
	}
	transport.write(Frame(request, 0))
	response, err := in.Do(req)
	if err != nil {
		transport.close()
		if failure := transport.failure(); failure != nil {
			return nil, nil, failure
		}
		return nil, nil, &RunError{Message: err.Error(), Kind: "transport"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		transport.close()
		return nil, nil, &RunError{Message: fmt.Sprintf("Cursor RunSSE failed with HTTP %d", response.StatusCode), Status: response.StatusCode, Kind: "http"}
	}
	body := response.Body
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && in.Decode != nil {
		decoded, errDecode := in.Decode(response.Body, encoding)
		if errDecode != nil {
			_ = response.Body.Close()
			transport.close()
			return nil, nil, &RunError{Message: errDecode.Error(), Kind: "transport"}
		}
		body = decoded
	}
	return transport, body, nil
}
