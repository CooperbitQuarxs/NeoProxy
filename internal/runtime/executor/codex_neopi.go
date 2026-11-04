package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// codexNeoPiSessions is NeoPi's process-wide Codex provider session state:
// thread/window identity, turn state, effort baselines, sockets and chains.
var codexNeoPiSessions = codexwire.NewSessions()

// codexNeoPiDial opens Codex sockets through the credential's proxy.
var codexNeoPiDial = func(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) codexwire.DialFunc {
	return newProxyAwareWebsocketDialer(ctx, cfg, auth).DialContext
}

// codexNeoPiTurn is one ChatGPT-subscription Codex request shaped by NeoPi.
type codexNeoPiTurn struct {
	shaped *codexwire.Request
	model  *registry.CodexModel
	// downstream is the Responses body the turn was shaped from.
	downstream []byte
}

// codexNeoPiSessionID is NeoPi's options.sessionId for a downstream request:
// the client's prompt cache identity, else the stable execution session.
func codexNeoPiSessionID(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	for _, payload := range [][]byte{opts.OriginalRequest, req.Payload} {
		if key := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); key != "" {
			return key
		}
	}
	return helps.ProviderSessionUUID("codex", req.Metadata, opts.Metadata)
}

// neoPiCodexTurn shapes an OAuth Codex request exactly as NeoPi does. It
// reports false for API-key credentials, which NeoPi serves as a different
// provider. Payload rules are not applied here: the runner applies them to
// each frame or body it actually sends.
func (e *CodexExecutor) neoPiCodexTurn(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) (*codexNeoPiTurn, bool, error) {
	if codexAuthUsesAPIKey(auth) {
		return nil, false, nil
	}
	modelInfo, ok := cliproxyauth.ResolvedModelInfo(req)
	if !ok || modelInfo == nil || modelInfo.Codex == nil {
		return nil, true, fmt.Errorf("codex: model %s has no discovered NeoPi metadata", req.Model)
	}
	turn, err := e.shapeNeoPiCodex(auth, modelInfo.Codex, codexNeoPiSessionID(req, opts), body)
	return turn, true, err
}

func (e *CodexExecutor) shapeNeoPiCodex(auth *cliproxyauth.Auth, model *registry.CodexModel, sessionID string, body []byte) (*codexNeoPiTurn, error) {
	accessToken, baseURL := codexCreds(auth)
	authDir := ""
	if e != nil && e.cfg != nil {
		authDir = e.cfg.AuthDir
	}
	installID, err := neopiid.InstallID(authDir)
	if err != nil {
		return nil, err
	}
	conversation, options := codexwire.FromResponses(body, model)
	options.SessionID = sessionID
	// NeoPi derives the account header from the bearer's own claim.
	shaped, err := codexNeoPiSessions.Shape(model, conversation, options, codexwire.Call{
		AccessToken: accessToken,
		BaseURL:     baseURL,
		InstallID:   installID,
		Transport:   codexwire.TransportWebsocket,
	})
	if err != nil {
		return nil, err
	}
	return &codexNeoPiTurn{shaped: shaped, model: model, downstream: body}, nil
}

// codexNeoPiStream translates one NeoPi Codex turn's events for the client.
type codexNeoPiStream struct {
	e                    *CodexExecutor
	ctx                  context.Context
	req                  cliproxyexecutor.Request
	reporter             *helps.UsageReporter
	to, responseFormat   sdktranslator.Format
	originalPayload      []byte
	body                 []byte
	preserveNativeOutput bool

	param             any
	claudeInputTokens *helps.ClaudeInputTokenState
	outputByIndex     map[int64][]byte
	outputFallback    [][]byte
	sawOutputDelta    bool
}

// translate turns one upstream event into client chunks; completed carries
// the patched terminal event for non-streaming callers.
func (s *codexNeoPiStream) translate(event []byte) (chunks [][]byte, completed []byte, err error) {
	observeCodexTokenEvent(s.reporter, event)
	if helps.HasMeaningfulCodexOutputDelta(event) {
		s.sawOutputDelta = true
	}
	switch gjson.GetBytes(event, "type").String() {
	case "response.created":
		s.outputByIndex, s.outputFallback, s.sawOutputDelta = map[int64][]byte{}, nil, false
	case "response.output_item.done":
		collectCodexOutputItemDone(event, s.outputByIndex, &s.outputFallback)
	case "response.completed", "response.incomplete", "response.done":
		if helps.IsCodexTerminalEmptyIncomplete(event, len(s.outputByIndex)+len(s.outputFallback), s.sawOutputDelta) {
			return nil, nil, newCodexEmptyIncompleteStreamError()
		}
		event = normalizeCodexWebsocketCompletion(event)
		if detail, ok := helps.ParseCodexUsage(event); ok {
			s.reporter.Publish(s.ctx, detail)
		} else {
			s.reporter.EnsurePublished(s.ctx)
		}
		publishCodexImageToolUsage(s.ctx, s.reporter, s.body, event)
		if !s.preserveNativeOutput {
			event = patchCodexCompletedOutput(event, s.outputByIndex, s.outputFallback)
		}
		completed = event
	}
	helps.AppendAPIResponseChunk(s.ctx, s.e.cfg, event)
	line := append([]byte("data: "), event...)
	return helps.TranslateStreamWithClaudeInputTokens(s.ctx, s.to, s.responseFormat, s.req.Model, s.originalPayload, s.body, line, &s.param, s.claudeInputTokens), completed, nil
}

// neoPiError maps a runner failure onto the proxy's credential-aware errors.
func (e *CodexExecutor) neoPiError(err error) error {
	var httpErr *codexwire.HTTPError
	if errors.As(err, &httpErr) {
		return newCodexStatusErrWithCooling(httpErr.Status, httpErr.Body, e.modelLevelCooling())
	}
	var streamErr *codexwire.StreamError
	if errors.As(err, &streamErr) {
		if len(streamErr.Event) > 0 {
			if mapped, _, ok := codexTerminalFailureErrWithCooling(streamErr.Event, e.modelLevelCooling()); ok {
				return mapped
			}
		}
		return statusErr{code: http.StatusBadGateway, msg: streamErr.Message}
	}
	var transportErr *codexwire.TransportError
	if errors.As(err, &transportErr) {
		return statusErr{code: http.StatusBadGateway, msg: transportErr.Message}
	}
	return err
}

// neoPiDo sends one HTTP attempt and decodes the body as Bun's fetch would.
func neoPiDo(client *http.Client) func(*http.Request) (*http.Response, error) {
	return func(request *http.Request) (*http.Response, error) {
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
		if err != nil {
			_ = response.Body.Close()
			return nil, err
		}
		response.Body = decoded
		response.Header.Del("Content-Encoding")
		return response, nil
	}
}

// neoPiTransport opens one request's HTTP attempts and sockets through the
// credential's proxy. Response metadata feeds request logs and the
// credential's quota observation, as on every other executor path.
func (e *CodexExecutor) neoPiTransport(ctx context.Context, auth *cliproxyauth.Auth) (func(*http.Request) (*http.Response, error), codexwire.DialFunc) {
	do := neoPiDo(helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0))
	dial := codexNeoPiDial(ctx, e.cfg, auth)
	record := func(request *http.Request) (*http.Response, error) {
		response, err := do(request)
		if err == nil {
			helps.RecordAPIResponseMetadata(ctx, e.cfg, response.StatusCode, response.Header.Clone())
		}
		return response, err
	}
	recordDial := func(dialCtx context.Context, url string, header http.Header) (*websocket.Conn, *http.Response, error) {
		conn, response, err := dial(dialCtx, url, header)
		if response != nil {
			helps.RecordAPIWebsocketHandshake(ctx, e.cfg, response.StatusCode, response.Header.Clone())
		}
		return conn, response, err
	}
	return record, recordDial
}

// observeNeoPiQuota merges the rate limits a Codex event reports into the
// response headers the credential's quota observation reads.
func observeNeoPiQuota(ctx context.Context, event []byte) {
	logging.MergeResponseHeaders(ctx, helps.ParseCodexQuotaEventHeaders(event))
}

// runNeoPiCodex runs a shaped turn through NeoPi's transport. A response the
// server ends for accepted steering continues into its automatic successor
// as NeoPi's next request would: the steering becomes history and the
// successor is read without sending anything.
func (e *CodexExecutor) runNeoPiCodex(ctx context.Context, auth *cliproxyauth.Auth, turn *codexNeoPiTurn, sessionID string, steering codexwire.SteerSource, emit func([]byte) error) error {
	do, dial := e.neoPiTransport(ctx, auth)
	observe := func(event []byte) error {
		observeNeoPiQuota(ctx, event)
		return emit(event)
	}
	for {
		helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
			URL: turn.shaped.URL, Method: http.MethodPost, Headers: turn.shaped.Header.Clone(), Body: turn.shaped.Body, Provider: e.Identifier(),
		})
		runner := &codexwire.Turn{
			Sessions: codexNeoPiSessions,
			Request:  turn.shaped,
			Model:    turn.model,
			Finalize: func(body []byte) []byte { return helps.FinalizePayload(ctx, body) },
			Dial:     dial,
			Do:       do,
			Steering: steering,
		}
		outcome, err := runner.Run(ctx, observe)
		if err != nil {
			return err
		}
		if len(outcome.Steering) == 0 || awaitsClientOutput(outcome.Output) {
			return nil
		}
		next, errNext := successorDownstream(turn.downstream, outcome.Output, outcome.Steering)
		if errNext != nil {
			return errNext
		}
		if turn, err = e.shapeNeoPiCodex(auth, turn.model, sessionID, next); err != nil {
			return err
		}
	}
}

// awaitsClientOutput reports output the client must answer before the server
// continues: tool calls.
func awaitsClientOutput(output []json.RawMessage) bool {
	for _, item := range output {
		switch gjson.GetBytes(item, "type").String() {
		case "function_call", "custom_tool_call", "local_shell_call", "computer_call", "apply_patch_call", "shell_call":
			return true
		}
	}
	return false
}

// successorDownstream is the transcript NeoPi sends after steering: the
// prior input, the steered response, then the accepted steering messages.
func successorDownstream(downstream []byte, output, steering []json.RawMessage) ([]byte, error) {
	input := gjson.GetBytes(downstream, "input")
	var items []json.RawMessage
	if input.IsArray() {
		for _, item := range input.Array() {
			items = append(items, json.RawMessage(item.Raw))
		}
	} else if input.Type == gjson.String {
		items = append(items, json.RawMessage(fmt.Sprintf(`{"role":"user","content":%s}`, input.Raw)))
	}
	items = append(append(items, output...), steering...)
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(downstream, "input", encoded)
}

// codexNeoPiSteering is the steering source for a client connected over the
// Responses WebSocket: its response.steer frames, acknowledged in-stream.
type codexNeoPiSteering struct {
	ctx   context.Context
	input <-chan cliproxyexecutor.WebsocketInput
	ack   func([]byte)
}

func newCodexNeoPiSteering(ctx context.Context, enabled bool, ack func([]byte)) codexwire.SteerSource {
	input := cliproxyexecutor.WebsocketInputFromContext(ctx)
	if !enabled || input == nil {
		return nil
	}
	return &codexNeoPiSteering{ctx: ctx, input: input, ack: ack}
}

// Claim takes the client's next response.steer. Other frames belong to the
// connection loop and are handed back to it.
func (s *codexNeoPiSteering) Claim(ctx context.Context) (*codexwire.SteerClaim, bool) {
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case message, ok := <-s.input:
			if !ok || message.Err != nil {
				if ok {
					cliproxyexecutor.DeferWebsocketInput(s.ctx, message)
				}
				return nil, false
			}
			if gjson.GetBytes(message.Payload, "type").String() != "response.steer" {
				cliproxyexecutor.DeferWebsocketInput(s.ctx, message)
				continue
			}
			return &codexwire.SteerClaim{Input: steerInputItems(message.Payload), Resolve: s.resolve}, true
		}
	}
}

func (s *codexNeoPiSteering) resolve(accepted bool, ack []byte) {
	if len(ack) == 0 {
		ack = []byte(`{"type":"response.steer.failed","error":{"code":"steering_not_delivered","message":"steering was not delivered to the response"}}`)
	}
	s.ack(ack)
}

// steerInputItems reads a response.steer frame's input as Responses items.
func steerInputItems(frame []byte) []json.RawMessage {
	input := gjson.GetBytes(frame, "input")
	if input.Type == gjson.String {
		text, _ := json.Marshal(input.String())
		return []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":` + string(text) + `}]}`)}
	}
	var items []json.RawMessage
	for _, item := range input.Array() {
		items = append(items, json.RawMessage(item.Raw))
	}
	return items
}

// streamNeoPiCodex runs the turn and returns once the first event reaches the
// client, so failures before that point can fail over to another credential.
// translate turns each upstream event into client chunks.
func (e *CodexExecutor) streamNeoPiCodex(ctx context.Context, auth *cliproxyauth.Auth, turn *codexNeoPiTurn, sessionID string, reporter *helps.UsageReporter, translate func([]byte) ([][]byte, error)) (*cliproxyexecutor.StreamResult, error) {
	out := make(chan cliproxyexecutor.StreamChunk, 64)
	started := make(chan error, 1)
	var once sync.Once
	var mu sync.Mutex
	committed := false
	start := func(err error) { once.Do(func() { started <- err }) }
	send := func(chunk cliproxyexecutor.StreamChunk) error {
		select {
		case out <- chunk:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// emit is shared by the event loop and steering acknowledgements.
	emit := func(event []byte) error {
		mu.Lock()
		defer mu.Unlock()
		chunks, err := translate(event)
		if err != nil {
			return err
		}
		committed = true
		start(nil)
		for _, chunk := range chunks {
			if errSend := send(cliproxyexecutor.StreamChunk{Payload: chunk}); errSend != nil {
				return errSend
			}
		}
		return nil
	}
	steeringEnabled := e.cfg != nil && (e.cfg.Codex.ResponseSteering || e.cfg.CodexResponseSteering)
	steering := newCodexNeoPiSteering(ctx, steeringEnabled, func(ack []byte) { _ = emit(ack) })
	reporter.StartResponseTTFT()
	go func() {
		defer close(out)
		err := e.runNeoPiCodex(ctx, auth, turn, sessionID, steering, emit)
		if err == nil {
			start(nil)
			return
		}
		mapped := e.neoPiError(err)
		helps.RecordAPIResponseError(ctx, e.cfg, mapped)
		reporter.PublishFailure(ctx, mapped)
		mu.Lock()
		wasCommitted := committed
		mu.Unlock()
		if !wasCommitted {
			start(mapped)
			return
		}
		_ = send(cliproxyexecutor.StreamChunk{Err: mapped})
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return &cliproxyexecutor.StreamResult{Headers: http.Header{}, Chunks: out}, nil
}

// executeNeoPiCodex runs the turn for a non-streaming client and translates
// the completed response.
func (e *CodexExecutor) executeNeoPiCodex(ctx context.Context, auth *cliproxyauth.Auth, turn *codexNeoPiTurn, sessionID string, stream *codexNeoPiStream) (cliproxyexecutor.Response, error) {
	var completed []byte
	err := e.runNeoPiCodex(ctx, auth, turn, sessionID, nil, func(event []byte) error {
		_, done, errTranslate := stream.translate(event)
		if done != nil {
			completed = done
		}
		return errTranslate
	})
	if err != nil {
		mapped := e.neoPiError(err)
		helps.RecordAPIResponseError(ctx, e.cfg, mapped)
		return cliproxyexecutor.Response{}, mapped
	}
	if completed == nil {
		return cliproxyexecutor.Response{}, newCodexIncompleteStreamError()
	}
	var param any
	out := sdktranslator.TranslateNonStream(ctx, stream.to, stream.responseFormat, stream.req.Model, stream.originalPayload, stream.body, completed, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	if stream.responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: http.Header{}}, nil
}
