package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claudeneopi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"
)

// ClaudeNeoPiExecutor owns only the experimental subscription flow. It shares
// dialect translation and usage accounting, never ClaudeExecutor's cloaking,
// signing, aliases, transport fingerprint, or thinking-replay state.
type ClaudeNeoPiExecutor struct {
	cfg     *config.Config
	refresh singleflight.Group
	wait    func(context.Context, time.Duration) error
}

func NewClaudeNeoPiExecutor(cfg *config.Config) *ClaudeNeoPiExecutor {
	return &ClaudeNeoPiExecutor{cfg: cfg, wait: neoPiWait}
}

func (*ClaudeNeoPiExecutor) Identifier() string { return claudeneopi.Provider }

func neoPiWait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func neoPiCredential(auth *cliproxyauth.Auth) (*claudeneopi.Credential, error) {
	if auth == nil {
		return nil, errors.New("claude-neopi: missing credential")
	}
	encoded, err := json.Marshal(auth.Metadata)
	if err != nil {
		return nil, err
	}
	var credential claudeneopi.Credential
	if err = json.Unmarshal(encoded, &credential); err != nil {
		return nil, err
	}
	if credential.AccessToken == "" {
		return nil, errors.New("claude-neopi: missing subscription access token")
	}
	return &credential, nil
}

func neoPiBaseURL(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
			return strings.TrimRight(value, "/")
		}
		if value, ok := auth.Metadata["base_url"].(string); ok && value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	return "https://api.anthropic.com"
}

func neoPiSession(opts cliproxyexecutor.Options, body []byte) string {
	if value := opts.Headers.Get("X-Claude-Code-Session-Id"); value != "" {
		return value
	}
	for _, key := range []string{cliproxyexecutor.ExecutionSessionMetadataKey, cliproxyexecutor.CanonicalSessionIDMetadataKey, cliproxyexecutor.DerivedSessionIDMetadataKey} {
		if value, ok := opts.Metadata[key].(string); ok && value != "" {
			return value
		}
	}
	if userID := gjson.GetBytes(body, "metadata.user_id").String(); gjson.Valid(userID) {
		if session := gjson.Get(userID, "session_id").String(); session != "" {
			return session
		}
	}
	return uuid.NewString()
}

type neoPiPrepared struct {
	body       []byte
	translated []byte
	headers    http.Header
	url        string
	model      string
	token      string
	identity   neoPiWireIdentity
	base       string
}

func (e *ClaudeNeoPiExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*neoPiPrepared, error) {
	credential, err := neoPiCredential(auth)
	if err != nil {
		return nil, err
	}
	model := thinking.ParseSuffix(req.Model).ModelName
	model = strings.TrimPrefix(model, claudeneopi.Provider+"/")
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	_, translated, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FromString("claude"), model, original, req.Payload, true, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "claude", e.Identifier())
	if err != nil {
		return nil, err
	}
	identity := neoPiWireIdentity{InstallID: credential.InstallID, AccountID: credential.AccountUUID, SessionID: neoPiSession(opts, translated)}
	base := neoPiBaseURL(auth)
	body, headers, err := projectNeoPiRequest(translated, model, base, claudeneopi.WireVersion(), identity)
	if err != nil {
		return nil, err
	}
	// NeoPi honors caller claude-cli user agents and the caller-owned slow lane.
	if ua := opts.Headers.Get("User-Agent"); strings.HasPrefix(strings.ToLower(ua), "claude-cli") {
		headers.Set("User-Agent", ua)
	}
	if slow := opts.Headers.Get("anthropic-usage-limit"); slow != "" {
		headers.Set("anthropic-usage-limit", slow)
	}
	headers.Set("Authorization", "Bearer "+credential.AccessToken)
	return &neoPiPrepared{body: body, translated: translated, headers: headers, url: base + "/v1/messages?beta=true", model: model, token: credential.AccessToken, identity: identity, base: base}, nil
}

func (e *ClaudeNeoPiExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	credential, err := neoPiCredential(auth)
	if err != nil {
		return nil, err
	}
	request = request.Clone(ctx)
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Del("X-Api-Key")
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}

func (e *ClaudeNeoPiExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	credential, err := neoPiCredential(auth)
	if err != nil {
		return nil, err
	}
	// The conductor normally serializes refreshes. This also protects direct
	// executor callers; the key never appears in logs or persisted metadata.
	key := auth.ID + "\x00" + credential.RefreshToken
	value, err, _ := e.refresh.Do(key, func() (any, error) {
		updated, refreshErr := claudeneopi.NewClient(e.cfg, auth.ProxyURL).Refresh(ctx, credential)
		if refreshErr != nil {
			return nil, refreshErr
		}
		next := auth.Clone()
		if next.Metadata == nil {
			next.Metadata = make(map[string]any)
		}
		encoded, marshalErr := json.Marshal(updated)
		if marshalErr != nil {
			return nil, marshalErr
		}
		var fields map[string]any
		if marshalErr = json.Unmarshal(encoded, &fields); marshalErr != nil {
			return nil, marshalErr
		}
		for name, field := range fields {
			next.Metadata[name] = field
		}
		next.Provider = claudeneopi.Provider
		next.Prefix = claudeneopi.Provider
		next.LastRefreshedAt = time.Now()
		return next, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*cliproxyauth.Auth).Clone(), nil
}

type neoPiUpstreamError struct {
	code    int
	body    string
	headers http.Header
}

func (e *neoPiUpstreamError) Error() string                { return e.body }
func (e *neoPiUpstreamError) StatusCode() int              { return e.code }
func (e *neoPiUpstreamError) ResponseHeaders() http.Header { return e.headers.Clone() }
func (e *neoPiUpstreamError) RetryAfter() *time.Duration {
	if delay, ok := neoPiRetryAfter(e.headers, time.Now()); ok {
		return &delay
	}
	return nil
}

func neoPiRetryAfter(headers http.Header, now time.Time) (time.Duration, bool) {
	if raw := headers.Get("retry-after-ms"); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil && value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			return time.Duration(value * float64(time.Millisecond)), true
		}
	}
	if raw := headers.Get("retry-after"); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil && value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			return time.Duration(value * float64(time.Second)), true
		}
		if date, err := http.ParseTime(raw); err == nil {
			return max(time.Duration(0), date.Sub(now)), true
		}
	}
	return 0, false
}

func neoPiRetryable(err error) bool {
	var upstream *neoPiUpstreamError
	if !errors.As(err, &upstream) {
		return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	message := strings.ToLower(upstream.body)
	for _, limit := range []string{"usage_limit_reached", "insufficient_quota", "weekly usage limit", "usage credits are required", "you have reached your", "credit balance is too low"} {
		if strings.Contains(message, limit) {
			return false
		}
	}
	return upstream.code == 408 || upstream.code == 429 || upstream.code >= 500
}

func (e *ClaudeNeoPiExecutor) retry(ctx context.Context, err error, attempt int) error {
	if attempt >= 10 || !neoPiRetryable(err) {
		return err
	}
	delay := time.Duration(math.Min(.5*math.Pow(2, float64(attempt)), 8) * (1 - rand.Float64()*.25) * float64(time.Second))
	var upstream *neoPiUpstreamError
	if errors.As(err, &upstream) {
		if hint, ok := neoPiRetryAfter(upstream.headers, time.Now()); ok {
			if hint > time.Minute {
				return err
			}
			delay = max(delay, hint)
		}
	}
	return e.wait(ctx, delay)
}

// neoPiStream owns the response and buffers only replay-safe pre-content frames.
// Once any text/thinking/tool-input delta has arrived the attempt is committed;
// a later transport error is never silently replayed.
type neoPiStream struct {
	response *http.Response
	body     io.ReadCloser
	scanner  *bufio.Scanner
	pending  [][]byte
	started  bool
	stopped  bool
}

func neoPiSSEData(line []byte) []byte {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	return bytes.TrimSpace(line[5:])
}

func neoPiMeaningful(data []byte) bool {
	root := gjson.ParseBytes(data)
	if root.Get("type").String() != "content_block_delta" {
		return false
	}
	for _, name := range []string{"text", "thinking", "partial_json"} {
		if root.Get("delta."+name).String() != "" {
			return true
		}
	}
	return false
}

func neoPiSSEError(data []byte, headers http.Header) error {
	if gjson.GetBytes(data, "type").String() != "error" {
		return nil
	}
	kind := gjson.GetBytes(data, "error.type").String()
	status := http.StatusBadRequest
	switch kind {
	case "overloaded_error":
		status = 529
	case "rate_limit_error":
		status = 429
	case "api_error", "server_error":
		status = 500
	}
	return &neoPiUpstreamError{code: status, body: string(data), headers: headers}
}

func (e *ClaudeNeoPiExecutor) open(ctx context.Context, client *http.Client, prepared *neoPiPrepared) (*neoPiStream, error) {
	emptyRetries := 0
	for attempt := 0; ; attempt++ {
		requestCtx, cancel := context.WithCancel(ctx)
		// NeoPi's timeout ends when response headers arrive, not ten minutes
		// into a valid, long-running streamed response.
		timer := time.AfterFunc(10*time.Minute, cancel)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, prepared.url, bytes.NewReader(prepared.body))
		if err != nil {
			timer.Stop()
			cancel()
			return nil, err
		}
		request.Header = prepared.headers.Clone()
		response, err := client.Do(request)
		timer.Stop()
		if err != nil {
			cancel()
			if retryErr := e.retry(ctx, err, attempt); retryErr != nil {
				return nil, retryErr
			}
			continue
		}
		decoded, err := decodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
		if err != nil {
			response.Body.Close()
			cancel()
			return nil, err
		}
		body := &neoPiCancelBody{ReadCloser: decoded, cancel: cancel}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			data, readErr := io.ReadAll(body)
			body.Close()
			if readErr != nil {
				err = readErr
			} else {
				err = &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
			}
			if claudeneopi.AdoptRequiredVersion(err.Error()) {
				projected, headers, projectErr := projectNeoPiRequest(prepared.translated, prepared.model, prepared.base, claudeneopi.WireVersion(), prepared.identity)
				if projectErr != nil {
					return nil, projectErr
				}
				headers.Set("Authorization", "Bearer "+prepared.token)
				if slow := prepared.headers.Get("anthropic-usage-limit"); slow != "" {
					headers.Set("anthropic-usage-limit", slow)
				}
				prepared.body, prepared.headers = projected, headers
				attempt--
				continue
			}
			if retryErr := e.retry(ctx, err, attempt); retryErr != nil {
				return nil, retryErr
			}
			continue
		}
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 64<<10), 32<<20)
		stream := &neoPiStream{response: response, body: body, scanner: scanner}
		visibleTool := false
		for scanner.Scan() {
			data := neoPiSSEData(scanner.Bytes())
			if len(data) == 0 {
				continue
			}
			if !gjson.ValidBytes(data) {
				err = errors.New("claude-neopi: malformed SSE JSON before content")
				break
			}
			if eventErr := neoPiSSEError(data, response.Header); eventErr != nil {
				err = eventErr
				break
			}
			kind := gjson.GetBytes(data, "type").String()
			if kind == "message_start" {
				stream.started = true
			}
			if kind == "content_block_start" && gjson.GetBytes(data, "content_block.type").String() == "tool_use" {
				visibleTool = true
			}
			stream.pending = append(stream.pending, bytes.Clone(data))
			if neoPiMeaningful(data) {
				if !stream.started {
					err = errors.New("claude-neopi: content before message_start")
					break
				}
				return stream, nil
			}
			if kind == "message_stop" {
				stream.stopped = true
				if !stream.started {
					err = errors.New("claude-neopi: message_stop without message_start")
					break
				}
				if visibleTool || emptyRetries >= 2 {
					return stream, nil
				}
				break
			}
		}
		body.Close()
		if err == nil {
			err = scanner.Err()
		}
		if err == nil && stream.stopped {
			if waitErr := e.wait(ctx, 500*time.Millisecond*time.Duration(1<<emptyRetries)); waitErr != nil {
				return nil, waitErr
			}
			emptyRetries++
			attempt = -1
			continue
		}
		if err == nil {
			err = errors.New("claude-neopi: stream ended before message_stop")
		}
		if retryErr := e.retry(ctx, err, attempt); retryErr != nil {
			return nil, retryErr
		}
	}
}

type neoPiCancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *neoPiCancelBody) Close() error { b.cancel(); return b.ReadCloser.Close() }

func (s *neoPiStream) consume(emit func([]byte) error) error {
	defer s.body.Close()
	for _, data := range s.pending {
		if err := emit(data); err != nil {
			return err
		}
	}
	if s.stopped {
		return nil
	}
	for s.scanner.Scan() {
		data := neoPiSSEData(s.scanner.Bytes())
		if len(data) == 0 {
			continue
		}
		if !gjson.ValidBytes(data) {
			return errors.New("claude-neopi: malformed SSE JSON")
		}
		if err := neoPiSSEError(data, s.response.Header); err != nil {
			return err
		}
		if err := emit(data); err != nil {
			return err
		}
		if gjson.GetBytes(data, "type").String() == "message_stop" {
			return nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return err
	}
	return errors.New("claude-neopi: stream ended before message_stop")
}

func neoPiFrame(data []byte) []byte {
	return []byte("event: " + gjson.GetBytes(data, "type").String() + "\ndata: " + string(data) + "\n\n")
}

func (e *ClaudeNeoPiExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (result cliproxyexecutor.Response, err error) {
	prepared, err := e.prepare(ctx, auth, req, opts)
	if err != nil {
		return result, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model, auth)
	defer reporter.TrackFailure(ctx, &err)
	stream, err := e.open(ctx, reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)), prepared)
	if err != nil {
		return result, err
	}
	var transcript bytes.Buffer
	var usage helps.StreamUsageBuffer
	err = stream.consume(func(data []byte) error {
		data = neoPiStripToolPrefix(data)
		line := append([]byte("data: "), data...)
		usage.ObserveClaudeStream(line)
		reporter.ObserveResponseModel(line)
		transcript.Write(neoPiFrame(data))
		return nil
	})
	if err != nil {
		usage.PublishFailure(ctx, reporter, err)
		return result, err
	}
	data := transcript.Bytes()
	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	if format == sdktranslator.FromString("claude") {
		data, err = neoPiAggregateMessage(data)
		if err != nil {
			return result, err
		}
	} else {
		var state any
		data = sdktranslator.TranslateNonStream(ctx, sdktranslator.FromString("claude"), format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.body, data, &state)
		if helps.ApplyPatchTranslationError(state) != nil || len(data) == 0 {
			return result, errors.New("claude-neopi: response translation failed")
		}
	}
	usage.Publish(ctx, reporter)
	log.WithFields(log.Fields{"provider": e.Identifier(), "model": prepared.model}).Info("experimental provider request completed")
	return cliproxyexecutor.Response{Payload: data, Headers: stream.response.Header.Clone()}, nil
}

func (e *ClaudeNeoPiExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	prepared, err := e.prepare(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model, auth)
	defer reporter.TrackFailure(ctx, &err)
	stream, err := e.open(ctx, reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)), prepared)
	if err != nil {
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		var usage helps.StreamUsageBuffer
		var state any
		format := cliproxyexecutor.ResponseFormatOrSource(opts)
		consumeErr := stream.consume(func(data []byte) error {
			data = neoPiStripToolPrefix(data)
			line := append([]byte("data: "), data...)
			usage.ObserveClaudeStream(line)
			reporter.ObserveResponseModel(line)
			chunks := [][]byte{neoPiFrame(data)}
			if format != sdktranslator.FromString("claude") {
				chunks = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("claude"), format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.body, line, &state)
			}
			if helps.ApplyPatchTranslationError(state) != nil {
				return errors.New("claude-neopi: stream translation failed")
			}
			for _, chunk := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: bytes.Clone(chunk)}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
		if consumeErr != nil {
			usage.PublishFailure(ctx, reporter, consumeErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: consumeErr}:
			case <-ctx.Done():
			}
			return
		}
		usage.Publish(ctx, reporter)
		log.WithFields(log.Fields{"provider": e.Identifier(), "model": prepared.model}).Info("experimental provider request completed")
	}()
	return &cliproxyexecutor.StreamResult{Headers: stream.response.Header.Clone(), Chunks: out}, nil
}

func (e *ClaudeNeoPiExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	prepared, err := e.prepare(ctx, auth, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	body := prepared.body
	for _, name := range []string{"max_tokens", "stream", "metadata", "output_config", "speed", "service_tier", "fallbacks", "fallback_credit_token"} {
		body = neoPiDelete(body, name)
	}
	body = neoPiSignBody(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, prepared.base+"/v1/messages/count_tokens?beta=true", bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	request.Header = prepared.headers
	response, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 10*time.Minute).Do(request)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer response.Body.Close()
	decoded, err := decodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer decoded.Close()
	data, err := io.ReadAll(decoded)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return cliproxyexecutor.Response{}, &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
	}
	return cliproxyexecutor.Response{Payload: data, Headers: response.Header.Clone()}, nil
}
