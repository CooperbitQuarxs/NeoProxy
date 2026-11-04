package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// An openai-compatibility entry may serve each model over a different upstream
// API (OpenCode: chat completions, Responses, Anthropic Messages and Gemini
// generateContent behind one key). The executor keeps its chat-completions path
// and routes the other dialects here, reusing the engine's translators between
// the client's format and the model's wire.

// compatDialectRoute is the upstream API and base URL chosen for one request.
type compatDialectRoute struct {
	dialect string
	format  sdktranslator.Format
	baseURL string
}

// resolveCompatDialect selects the model's upstream API. Requests with an Alt
// (responses/compact) keep the chat-completions path, which already targets
// their own endpoint; a per-model base URL still applies to them.
func resolveCompatDialect(compat *config.OpenAICompatibility, baseURL string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (compatDialectRoute, error) {
	route := compatDialectRoute{dialect: config.DialectChatCompletions, format: sdktranslator.FormatOpenAI, baseURL: baseURL}
	if compat == nil {
		return route, nil
	}
	upstream := thinking.ParseSuffix(req.Model).ModelName
	requested := thinking.ParseSuffix(helps.PayloadRequestedModel(opts, req.Model)).ModelName
	dialect, modelBase := compat.ResolveModelDialect(upstream, requested)
	if modelBase != "" {
		if !sameCompatOrigin(modelBase, baseURL) {
			return route, statusErr{code: http.StatusInternalServerError, msg: fmt.Sprintf("openai-compatibility %q: model %q has a base-url on another host than the entry", compat.Name, upstream)}
		}
		route.baseURL = modelBase
	}
	if opts.Alt != "" {
		return route, nil
	}
	switch dialect {
	case config.DialectChatCompletions:
	case config.DialectResponses:
		route.dialect, route.format = dialect, sdktranslator.FormatCodex
	case config.DialectMessages:
		route.dialect, route.format = dialect, sdktranslator.FormatClaude
	case config.DialectGenerateContent:
		route.dialect, route.format = dialect, sdktranslator.FormatGemini
	default:
		return route, statusErr{code: http.StatusInternalServerError, msg: fmt.Sprintf("openai-compatibility %q: unsupported dialect %q", compat.Name, dialect)}
	}
	return route, nil
}

// sameCompatOrigin keeps a model's base URL on the entry's scheme and host, so a
// model list can never send the entry's key to another server.
func sameCompatOrigin(modelBase, entryBase string) bool {
	m, errM := url.Parse(modelBase)
	e, errE := url.Parse(entryBase)
	return errM == nil && errE == nil && m.Host != "" && strings.EqualFold(m.Scheme, e.Scheme) && strings.EqualFold(m.Host, e.Host)
}

// applyCompatSessionHeader sends a per-conversation id in the entry's session
// header. Gateways that require it (OpenCode) reject requests without one, so
// sessionless requests carry a stable id derived from the credential. The
// client's own session id is hashed with the entry name and never sent as is.
func applyCompatSessionHeader(r *http.Request, compat *config.OpenAICompatibility, auth *cliproxyauth.Auth) {
	if r == nil || compat == nil {
		return
	}
	name := strings.TrimSpace(compat.SessionHeader)
	if name == "" || r.Header.Get(name) != "" {
		return
	}
	identity := "credential\x00" + compat.Name
	if auth != nil {
		identity += "\x00" + auth.ID
	}
	if session := util.SessionIDFromContext(r.Context()); session != "" {
		identity = "session\x00" + compat.Name + "\x00" + session
	}
	r.Header.Set(name, uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:openai-compat:session\x00"+identity)).String())
}

// compatDialectCall is a prepared upstream request for a non-chat dialect.
type compatDialectCall struct {
	route           compatDialectRoute
	httpReq         *http.Request
	body            []byte
	originalRequest []byte
	from            sdktranslator.Format
	responseFormat  sdktranslator.Format
}

func (e *OpenAICompatExecutor) prepareDialectCall(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, route compatDialectRoute, apiKey string, compat *config.OpenAICompatibility, reporter *helps.UsageReporter, clientStream bool) (*compatDialectCall, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	to := route.format
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	// The Responses wire is always read as an event stream, as the Codex
	// executor reads it, so non-streaming clients get the completed response.
	upstreamStream := clientStream || route.dialect == config.DialectResponses
	isCompat := helps.APIKeyModelIsCompat(req)
	targetModel := resolveResponsesSignatureTargetModel(baseModel, req, opts)
	originalTranslated, body, updatesChanged, err := helps.TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, opts.Headers, e.cfg, from, to, targetModel, originalPayload, req.Payload, upstreamStream, isCompat)
	if err != nil {
		return nil, err
	}
	if route.dialect == config.DialectResponses {
		body = plainResponsesRequest(req.Payload, body)
	}
	if route.dialect != config.DialectGenerateContent {
		originalTranslated = helps.SetStringIfDifferent(originalTranslated, "model", baseModel)
		body = helps.SetStringIfDifferent(body, "model", baseModel)
	}
	if body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged); err != nil {
		return nil, err
	}
	switch route.dialect {
	case config.DialectResponses:
		body = helps.SetBoolIfDifferent(body, "stream", true)
		body, _ = sjson.DeleteBytes(body, "stream_options")
		body = sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(ctx, "openai compat executor", body, isCompat, targetModel, route.baseURL)
	case config.DialectMessages:
		body = helps.SetBoolIfDifferent(body, "stream", clientStream)
	case config.DialectGenerateContent:
		body, _ = sjson.DeleteBytes(body, "model")
		body, _ = sjson.DeleteBytes(body, "session_id")
	}
	reporter.SetTranslatedReasoningEffort(body, to.String())
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, helps.PayloadRequestPath(opts), opts.Headers)
	if err = validateNeoPiPayload(auth, body, baseModel); err != nil {
		return nil, err
	}

	target := compatDialectURL(route, baseModel, upstreamStream)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	setCompatDialectAuth(httpReq.Header, route.dialect, apiKey)
	ensureOpenAICompatUserAgent(httpReq, opts.Headers)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	applyNeoPiCredentialPin(httpReq, attrs)
	applyCompatSessionHeader(httpReq, compat, auth)
	if upstreamStream {
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Cache-Control", "no-cache")
	}
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       target,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	return &compatDialectCall{
		route:           route,
		httpReq:         httpReq,
		body:            body,
		originalRequest: helps.ApplyPatchOriginalRequest(req, opts),
		from:            from,
		responseFormat:  cliproxyexecutor.ResponseFormatOrSource(opts),
	}, nil
}

// plainResponsesRequest adapts the engine's Codex-shaped Responses body to a
// plain Responses API. The Codex translator drops output limits and sampling,
// which ChatGPT's backend rejects but these gateways accept, and it invents a
// medium reasoning effort, which some gateways (Sakana) do not offer.
func plainResponsesRequest(source, body []byte) []byte {
	src := gjson.ParseBytes(source)
	if !gjson.GetBytes(body, "max_output_tokens").Exists() {
		for _, path := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens", "generationConfig.maxOutputTokens"} {
			if v := src.Get(path); v.Type == gjson.Number && v.Int() > 0 {
				body, _ = sjson.SetBytes(body, "max_output_tokens", v.Int())
				break
			}
		}
	}
	for path, sources := range map[string][]string{"temperature": {"temperature", "generationConfig.temperature"}, "top_p": {"top_p", "generationConfig.topP"}} {
		if gjson.GetBytes(body, path).Exists() {
			continue
		}
		for _, from := range sources {
			if v := src.Get(from); v.Type == gjson.Number {
				body, _ = sjson.SetBytes(body, path, v.Float())
				break
			}
		}
	}
	requested := false
	for _, path := range []string{"reasoning_effort", "reasoning", "thinking", "output_config.effort", "generationConfig.thinkingConfig"} {
		requested = requested || src.Get(path).Exists()
	}
	if !requested {
		body, _ = sjson.DeleteBytes(body, "reasoning")
	}
	return body
}

func compatDialectURL(route compatDialectRoute, model string, stream bool) string {
	base := strings.TrimRight(route.baseURL, "/")
	switch route.dialect {
	case config.DialectResponses:
		return base + "/responses"
	case config.DialectMessages:
		return base + "/messages"
	default:
		if stream {
			return base + "/models/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
		}
		return base + "/models/" + url.PathEscape(model) + ":generateContent"
	}
}

// setCompatDialectAuth presents the key the way each wire expects it; Anthropic
// gateways such as OpenCode reject Bearer auth on /messages.
func setCompatDialectAuth(h http.Header, dialect, apiKey string) {
	if dialect == config.DialectMessages {
		h.Set("anthropic-version", "2023-06-01")
	}
	if apiKey == "" {
		return
	}
	switch dialect {
	case config.DialectMessages:
		h.Set("x-api-key", apiKey)
	case config.DialectGenerateContent:
		h.Set("x-goog-api-key", apiKey)
	default:
		h.Set("Authorization", "Bearer "+apiKey)
	}
}

func (e *OpenAICompatExecutor) sendDialectCall(ctx context.Context, auth *cliproxyauth.Auth, call *compatDialectCall, reporter *helps.UsageReporter, stream bool) (*http.Response, error) {
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = neoPiHTTPClient(httpClient, auth)
	if stream {
		httpClient = reporter.TrackHTTPClientRoundTripOnly(httpClient)
	} else {
		httpClient = reporter.TrackHTTPClient(httpClient)
	}
	httpResp, err := httpClient.Do(call.httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		closeCompatBody(httpResp)
		return nil, newOpenAICompatStatusError(httpResp.StatusCode, httpResp.Header, b)
	}
	return httpResp, nil
}

func closeCompatBody(httpResp *http.Response) {
	if errClose := httpResp.Body.Close(); errClose != nil {
		log.Errorf("openai compat executor: close response body error: %v", errClose)
	}
}

func (e *OpenAICompatExecutor) executeDialect(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, route compatDialectRoute, apiKey string, compat *config.OpenAICompatibility) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	if route.baseURL == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
	}
	call, err := e.prepareDialectCall(ctx, auth, req, opts, route, apiKey, compat, reporter, false)
	if err != nil {
		return resp, err
	}
	httpResp, err := e.sendDialectCall(ctx, auth, call, reporter, false)
	if err != nil {
		return resp, err
	}
	defer closeCompatBody(httpResp)
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	payload := data
	switch route.dialect {
	case config.DialectResponses:
		if payload, err = completedResponsesEvent(data); err != nil {
			return resp, err
		}
		if detail, ok := helps.ParseCodexUsage(payload); ok {
			reporter.Publish(ctx, detail)
		}
	case config.DialectMessages:
		reporter.Publish(ctx, helps.ParseClaudeUsage(data))
	case config.DialectGenerateContent:
		reporter.Publish(ctx, helps.ParseGeminiUsage(data))
	}
	reporter.ObserveResponseModel(payload)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, route.format, call.responseFormat, req.Model, call.originalRequest, call.body, payload, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	if call.responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

// completedResponsesEvent reduces a Responses event stream to its terminal
// response.completed (or response.incomplete) event with the streamed output
// items restored. A gateway that ignored stream=true and answered with the
// response object is accepted as well.
func completedResponsesEvent(data []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' && gjson.GetBytes(trimmed, "object").String() == "response" {
		wrapped, _ := sjson.SetRawBytes([]byte(`{"type":"response.completed"}`), "response", trimmed)
		return wrapped, nil
	}
	items := make(map[int64][]byte)
	var fallback [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, len(data)+1)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		event := bytes.TrimSpace(line[len(dataTag):])
		if streamErr, _, failed := codexTerminalFailureErrWithCooling(event, false); failed {
			return nil, streamErr
		}
		switch gjson.GetBytes(event, "type").String() {
		case "response.output_item.done":
			collectCodexOutputItemDone(event, items, &fallback)
		case "response.completed", "response.incomplete":
			return patchCodexCompletedOutput(event, items, fallback), nil
		}
	}
	return nil, statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before response.completed"}
}

func (e *OpenAICompatExecutor) executeDialectStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, route compatDialectRoute, apiKey string, compat *config.OpenAICompatibility) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	if route.baseURL == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
	}
	call, err := e.prepareDialectCall(ctx, auth, req, opts, route, apiKey, compat, reporter, true)
	if err != nil {
		return nil, err
	}
	httpResp, err := e.sendDialectCall(ctx, auth, call, reporter, true)
	if err != nil {
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer closeCompatBody(httpResp)
		s := &compatDialectStream{e: e, ctx: ctx, req: req, call: call, reporter: reporter, out: out}
		s.claudeInputTokens = helps.NewClaudeInputTokenState(call.from, route.format, call.responseFormat, call.originalRequest)
		helps.InitializeApplyPatchStream(ctx, route.format, call.responseFormat, req.Model, call.originalRequest, call.body, &s.param)
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		switch route.dialect {
		case config.DialectResponses:
			s.pumpResponses(scanner)
		case config.DialectMessages:
			s.pumpMessages(scanner)
		default:
			s.pumpGenerateContent(scanner)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// compatDialectStream translates one upstream event stream to the client.
type compatDialectStream struct {
	e                 *OpenAICompatExecutor
	ctx               context.Context
	req               cliproxyexecutor.Request
	call              *compatDialectCall
	reporter          *helps.UsageReporter
	out               chan<- cliproxyexecutor.StreamChunk
	param             any
	claudeInputTokens *helps.ClaudeInputTokenState
	usage             helps.StreamUsageBuffer
}

var compatApplyPatchGatewayErr = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}

func (s *compatDialectStream) send(chunks [][]byte) bool {
	for i := range chunks {
		select {
		case s.out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
		case <-s.ctx.Done():
			return false
		}
	}
	return true
}

// translate forwards one upstream line; false ends the stream.
func (s *compatDialectStream) translate(line []byte) bool {
	chunks := helps.TranslateStreamWithClaudeInputTokens(s.ctx, s.call.route.format, s.call.responseFormat, s.req.Model, s.call.originalRequest, s.call.body, line, &s.param, s.claudeInputTokens)
	helps.RecordApplyPatchStreamFailure(s.ctx, s.param, s.reporter, compatApplyPatchGatewayErr)
	if s.call.responseFormat == sdktranslator.FormatOpenAIResponse && helps.ApplyPatchTranslationError(s.param) == nil {
		for i, chunk := range chunks {
			chunks[i] = helps.EnsureResponsesUsageDetails(chunk)
		}
	}
	if !s.send(chunks) {
		return false
	}
	return !helps.StopApplyPatchStream(s.ctx, s.param, s.reporter, s.out, compatApplyPatchGatewayErr)
}

func (s *compatDialectStream) fail(err error) {
	if s.ctx.Err() != nil {
		return
	}
	helps.RecordAPIResponseError(s.ctx, s.e.cfg, err)
	if !s.usage.PublishFailure(s.ctx, s.reporter, err) {
		s.reporter.PublishFailure(s.ctx, err)
	}
	select {
	case s.out <- cliproxyexecutor.StreamChunk{Err: err}:
	case <-s.ctx.Done():
	}
}

// finish closes a stream that reached its terminal event, or fails it.
func (s *compatDialectStream) finish(scanner *bufio.Scanner, completed bool, terminal string) {
	if helps.EndApplyPatchStream(s.ctx, s.param, s.reporter, s.out, compatApplyPatchGatewayErr) {
		return
	}
	if !completed {
		err := scanner.Err()
		if err == nil {
			err = statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before " + terminal}
		}
		s.fail(err)
		return
	}
	s.usage.Publish(s.ctx, s.reporter)
	s.reporter.EnsurePublished(s.ctx)
}

func (s *compatDialectStream) pumpResponses(scanner *bufio.Scanner) {
	items := make(map[int64][]byte)
	var fallback [][]byte
	completed := false
	for scanner.Scan() {
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(s.ctx, s.e.cfg, line)
		forward := bytes.Clone(line)
		if trimmed := bytes.TrimSpace(line); bytes.HasPrefix(trimmed, dataTag) {
			event := bytes.TrimSpace(trimmed[len(dataTag):])
			observeCodexTokenEvent(s.reporter, event)
			if streamErr, _, failed := codexTerminalFailureErrWithCooling(event, false); failed {
				s.fail(streamErr)
				return
			}
			switch gjson.GetBytes(event, "type").String() {
			case "response.output_item.done":
				collectCodexOutputItemDone(event, items, &fallback)
			case "response.completed", "response.incomplete":
				completed = true
				if detail, ok := helps.ParseCodexUsage(event); ok {
					s.reporter.Publish(s.ctx, detail)
				}
				event = patchCodexCompletedOutput(event, items, fallback)
			}
			forward = append([]byte("data: "), event...)
		}
		if !s.translate(forward) || completed {
			break
		}
	}
	s.finish(scanner, completed, "response.completed")
}

func (s *compatDialectStream) pumpMessages(scanner *bufio.Scanner) {
	passthrough := s.call.responseFormat == sdktranslator.FormatClaude
	var event bytes.Buffer
	var messageID string
	var streamErr error
	completed := false
	for scanner.Scan() {
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(s.ctx, s.e.cfg, line)
		s.reporter.ObserveResponseModel(line)
		s.usage.ObserveClaudeStream(line)
		event.Write(line)
		event.WriteByte('\n')
		boundary := len(bytes.TrimSpace(line)) == 0
		if boundary {
			observeClaudeSSEEvent(event.Bytes(), &messageID, &completed, &streamErr)
		}
		// A Claude client receives whole events unchanged (there is no
		// Claude-to-Claude translator); other clients get each line translated.
		if passthrough {
			if boundary {
				if !s.send([][]byte{bytes.Clone(event.Bytes())}) {
					return
				}
				event.Reset()
			}
		} else {
			if boundary {
				event.Reset()
			}
			if !s.translate(bytes.Clone(line)) {
				return
			}
		}
		if completed || streamErr != nil {
			break
		}
	}
	if event.Len() > 0 {
		observeClaudeSSEEvent(event.Bytes(), &messageID, &completed, &streamErr)
		if passthrough && !s.send([][]byte{bytes.Clone(event.Bytes())}) {
			return
		}
	}
	if streamErr != nil {
		s.fail(streamErr)
		return
	}
	s.finish(scanner, completed, "message_stop")
}

func (s *compatDialectStream) pumpGenerateContent(scanner *bufio.Scanner) {
	var retained error
	sawFinish := false
	for scanner.Scan() {
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(s.ctx, s.e.cfg, line)
		s.reporter.ObserveResponseModel(line)
		raw := helps.JSONPayload(line)
		sawFinish = sawFinish || geminiStreamHasFinishSignal(raw)
		if detail, ok := helps.ParseGeminiStreamUsage(raw); ok {
			s.usage.Observe(detail, true)
		}
		payload := helps.JSONPayload(helps.FilterSSEUsageMetadata(line))
		if len(payload) == 0 {
			continue
		}
		chunks := helps.TranslateStreamWithClaudeInputTokens(s.ctx, s.call.route.format, s.call.responseFormat, s.req.Model, s.call.originalRequest, s.call.body, bytes.Clone(payload), &s.param, s.claudeInputTokens)
		helps.RecordApplyPatchStreamFailure(s.ctx, s.param, s.reporter, compatApplyPatchGatewayErr)
		helps.CaptureTranslatedClaudeStreamError(s.call.responseFormat, chunks, &retained, func(msg string) error {
			return statusErr{code: http.StatusBadGateway, msg: msg}
		})
		if !s.send(chunks) || helps.StopApplyPatchStream(s.ctx, s.param, s.reporter, s.out, compatApplyPatchGatewayErr) {
			return
		}
		if helps.StopTranslatedClaudeStream(s.ctx, &retained, s.reporter, &s.usage, s.out) {
			return
		}
	}
	if !sawFinish {
		s.finish(scanner, false, "finishReason")
		return
	}
	if helps.EndApplyPatchStream(s.ctx, s.param, s.reporter, s.out, compatApplyPatchGatewayErr) {
		return
	}
	if err := scanner.Err(); err != nil {
		s.fail(err)
		return
	}
	done := helps.TranslateStreamWithClaudeInputTokens(s.ctx, s.call.route.format, s.call.responseFormat, s.req.Model, s.call.originalRequest, s.call.body, []byte("[DONE]"), &s.param, s.claudeInputTokens)
	if !s.send(done) || helps.StopTranslatedClaudeStream(s.ctx, &retained, s.reporter, &s.usage, s.out) {
		return
	}
	s.usage.Publish(s.ctx, s.reporter)
	s.reporter.EnsurePublished(s.ctx)
}
