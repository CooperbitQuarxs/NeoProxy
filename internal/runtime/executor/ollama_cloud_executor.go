package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/ollamawire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	// ollamaCloudMaxAttempts is fetchWithRetry's default attempt budget.
	ollamaCloudMaxAttempts = 5
	// ollamaCloudMaxRetryDelay is fetchWithRetry's per-delay cap: a longer
	// server hint returns the response instead of sleeping on it.
	ollamaCloudMaxRetryDelay = time.Minute
	// ollamaCloudMaxEmptyRetries is MAX_EMPTY_COMPLETION_RETRIES.
	ollamaCloudMaxEmptyRetries = 2
	// ollamaCloudEmptyRetryBase is EMPTY_COMPLETION_BASE_DELAY_MS.
	ollamaCloudEmptyRetryBase = 500 * time.Millisecond
)

// ollamaCloudRetryDelays is OLLAMA_RETRY_DELAYS_MS, indexed by attempt and
// clamped to its last entry.
var ollamaCloudRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// OllamaCloudExecutor serves NeoPi's Ollama Cloud provider (streamOllama,
// api ollama-chat): each turn is one native /api/chat request streamed back
// as NDJSON. A request is read as an OpenAI chat, as NeoPi's gateway reads
// it, and answered as one.
type OllamaCloudExecutor struct {
	cfg  *config.Config
	wait func(context.Context, time.Duration) error
	now  func() time.Time
}

// NewOllamaCloudExecutor builds the Ollama Cloud executor.
func NewOllamaCloudExecutor(cfg *config.Config) *OllamaCloudExecutor {
	return &OllamaCloudExecutor{cfg: cfg, wait: neoPiWait, now: time.Now}
}

// Identifier is the provider key.
func (e *OllamaCloudExecutor) Identifier() string { return registry.OllamaCloudProvider }

type ollamaCloudPrepared struct {
	url, modelID  string
	identityClass string
	body          []byte
	headers       http.Header
	translated    []byte
}

// ollamaCloudKey is the account's Ollama Cloud API key.
func ollamaCloudKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
		return key
	}
	key, _ := auth.Metadata["api_key"].(string)
	return strings.TrimSpace(key)
}

// ollamaCloudRow is the NeoPi row the request resolved to, else the bundled
// row of that id.
func ollamaCloudRow(req cliproxyexecutor.Request, modelID string) (*registry.NeoPiModel, bool) {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.API == "ollama-chat" {
		return info.NeoPi.Clone(), true
	}
	rows, err := registry.OllamaCloudBundledModels()
	if err != nil {
		return nil, false
	}
	for _, row := range rows {
		if row.ID == modelID {
			return row, true
		}
	}
	return nil, false
}

func (e *OllamaCloudExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*ollamaCloudPrepared, error) {
	key := ollamaCloudKey(auth)
	if key == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "No API key found for ollama-cloud. Set OLLAMA_CLOUD_API_KEY or add an Ollama Cloud key."}
	}
	modelID := thinking.ParseSuffix(req.Model).ModelName
	row, ok := ollamaCloudRow(req, modelID)
	if !ok {
		return nil, statusErr{code: http.StatusBadRequest, msg: "ollama-cloud: unknown model " + modelID}
	}
	translated, err := helps.TranslateRequestReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatOpenAI, modelID, bytes.Clone(req.Payload), stream, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	if translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "openai", e.Identifier()); err != nil {
		return nil, err
	}
	request, err := cursorwire.ParseOpenAIChat(translated, opts.Headers.Get)
	if err != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	// streamSimple: mandatory-reasoning rows run an off or effort-less turn at
	// their floor; resolveOpenAiReasoningEffort then settles the effort. The
	// gateway's reasoning_effort "none" never reaches ollama-chat's
	// disableReasoning.
	effort := neoPiCompletionsOptions{reasoning: request.Reasoning, disableReasoning: request.ForceReasoningOff}
	neoPiNormalizeMandatoryReasoning(row, &effort)
	if effort.disableReasoning {
		effort.reasoning = ""
	}
	options := ollamawire.Options{
		Reasoning:  neoPiNormalizeReasoningEffort(row, &registry.NeoPiCompletionsCompat{}, effort.reasoning),
		ToolChoice: ollamawire.ParseToolChoice(gjson.GetBytes(translated, "tool_choice")),
	}
	if row.MaxTokens > 0 {
		limit := int64(row.MaxTokens)
		options.MaxTokens = &limit
	}
	if value := gjson.GetBytes(translated, "temperature"); value.Exists() {
		options.Temperature = []byte(value.Raw)
	}
	if value := gjson.GetBytes(translated, "top_p"); value.Exists() {
		options.TopP = []byte(value.Raw)
	}
	model := &ollamawire.Model{ID: row.ID, Provider: row.Provider, Reasoning: row.Reasoning, OmitMaxOutputTokens: row.OmitMaxOutputTokens,
		StripThinkingHistory: row.OllamaCompat == nil || row.OllamaCompat.StripThinkingHistory}
	if row.Thinking != nil {
		model.EffortMap = row.Thinking.EffortMap
	}
	for _, input := range row.Input {
		model.SupportsImages = model.SupportsImages || input == "image"
	}
	body := ollamawire.BuildBody(model, &request.Context, options)
	body = helps.NewPayloadFinalizer(e.cfg, e.Identifier(), row.ID, "ollama", "", translated, req, opts)(body)
	baseURL := ""
	if auth != nil {
		baseURL = auth.Attributes["base_url"]
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = row.BaseURL
	}
	return &ollamaCloudPrepared{url: ollamawire.NormalizeBaseURL(baseURL) + "/api/chat", modelID: row.ID,
		identityClass: neopitaxonomy.Classify(row.Provider, row.ID).Class, body: body,
		headers: ollamaCloudHeaders(ctx, auth, opts.Headers, key), translated: translated}, nil
}

// ollamaCloudHeaders is the /api/chat request as NeoPi's fetch puts it on
// the wire: the account's configured headers, the bearer and JSON content
// type over them, and Bun's defaults where nothing set them.
func ollamaCloudHeaders(ctx context.Context, auth *cliproxyauth.Auth, incoming http.Header, key string) http.Header {
	headers := make(http.Header)
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs((&http.Request{Header: headers}).WithContext(ctx), auth.Attributes, incoming)
	}
	headers.Set("Authorization", "Bearer "+key)
	headers.Set("Content-Type", "application/json")
	for name, value := range map[string]string{"Accept": "*/*", "Accept-Encoding": "gzip, deflate, br, zstd", "Connection": "keep-alive", "User-Agent": openaiwire.NeoPiUserAgent} {
		if headers.Get(name) == "" {
			headers.Set(name, value)
		}
	}
	return headers
}

// open is fetchWithRetry with Ollama's schedule: network failures and 5xx,
// 408 and 429 answers are retried before any response byte (a 5xx naming a
// llama.cpp tool-call parse failure is not), honouring server hints up to a
// minute.
func (e *OllamaCloudExecutor) open(ctx context.Context, auth *cliproxyauth.Auth, p *ollamaCloudPrepared, client *http.Client) (*http.Response, error) {
	delay := func(attempt int) time.Duration {
		return ollamaCloudRetryDelays[min(attempt, len(ollamaCloudRetryDelays)-1)]
	}
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(p.body))
		if err != nil {
			return nil, err
		}
		request.Header = p.headers.Clone()
		info := helps.UpstreamRequestLog{URL: p.url, Method: http.MethodPost, Headers: request.Header, Body: p.body, Provider: e.Identifier()}
		if auth != nil {
			info.AuthID, info.AuthLabel = auth.ID, auth.Label
			info.AuthType, info.AuthValue = auth.AccountInfo()
		}
		helps.RecordAPIRequest(ctx, e.cfg, info)
		response, err := client.Do(request)
		if err != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			if ctx.Err() != nil || attempt+1 >= ollamaCloudMaxAttempts {
				return nil, err
			}
			if errWait := e.wait(ctx, delay(attempt)); errWait != nil {
				return nil, err
			}
			continue
		}
		helps.RecordAPIResponseMetadata(ctx, e.cfg, response.StatusCode, response.Header)
		decoded, errDecode := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
		if errDecode != nil {
			_ = response.Body.Close()
			return nil, errDecode
		}
		response.Body = decoded
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		failure := &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
		if !neoPiRetryableStatus(response.StatusCode) || attempt+1 >= ollamaCloudMaxAttempts || !ollamawire.RetryResponse(response.StatusCode, data) {
			return nil, failure
		}
		hint, ok := neoPiCompletionsRetryHint(response.Header, data, e.now())
		if ok && hint > ollamaCloudMaxRetryDelay {
			return nil, failure
		}
		if !ok {
			hint = delay(attempt)
		}
		if errWait := e.wait(ctx, hint); errWait != nil {
			return nil, failure
		}
	}
}

// ollamaCloudReplaySink holds an attempt's events until one delivers
// content (withReplaySafeStreamRetry), so a discarded attempt reaches no
// client.
type ollamaCloudReplaySink struct {
	sink      cursorwire.Sink
	pending   []func()
	committed bool
}

func (s *ollamaCloudReplaySink) emit(event func(), meaningful bool) {
	if s.committed {
		event()
		return
	}
	s.pending = append(s.pending, event)
	if !meaningful {
		return
	}
	s.committed = true
	for _, held := range s.pending {
		held()
	}
	s.pending = nil
}

func (s *ollamaCloudReplaySink) TextDelta(delta string) {
	s.emit(func() { s.sink.TextDelta(delta) }, delta != "")
}

func (s *ollamaCloudReplaySink) ThinkingDelta(delta string) {
	s.emit(func() { s.sink.ThinkingDelta(delta) }, delta != "")
}

func (s *ollamaCloudReplaySink) ToolCallStart(block *cursorwire.OutBlock) {
	s.emit(func() { s.sink.ToolCallStart(block) }, false)
}

func (s *ollamaCloudReplaySink) ToolCallDelta(block *cursorwire.OutBlock, delta string) {
	s.emit(func() { s.sink.ToolCallDelta(block, delta) }, delta != "")
}

func (s *ollamaCloudReplaySink) ToolCallEnd(block *cursorwire.OutBlock) {
	s.emit(func() { s.sink.ToolCallEnd(block) }, false)
}

// flush releases what an attempt held back once it is final.
func (s *ollamaCloudReplaySink) flush() {
	for _, held := range s.pending {
		held()
	}
	s.pending = nil
}

// run streams one turn into sink, re-issuing an empty natural stop up to
// twice (streamOllama's retryEmptyCompletion), and returns its message; a
// failed turn is an error carrying the upstream status, else 502.
func (e *OllamaCloudExecutor) run(ctx context.Context, auth *cliproxyauth.Auth, p *ollamaCloudPrepared, reporter *helps.UsageReporter, sink cursorwire.Sink) (*cursorwire.Output, error) {
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	for emptyRetries := 0; ; emptyRetries++ {
		response, err := e.open(ctx, auth, p, client)
		if err != nil {
			return nil, err
		}
		replay := &ollamaCloudReplaySink{sink: sink}
		result, errStream := ollamawire.ReadStream(&ollamaCloudLoggedBody{ctx: ctx, cfg: e.cfg, body: response.Body}, p.identityClass, replay)
		_ = response.Body.Close()
		output := result.Output
		if errStream != nil {
			replay.flush()
			if ctx.Err() != nil {
				return output, ctx.Err()
			}
			var failure *ollamawire.StreamError
			if errors.As(errStream, &failure) {
				return output, statusErr{code: http.StatusBadGateway, msg: failure.Message}
			}
			return output, errStream
		}
		if output.StopReason == "error" {
			replay.flush()
			return output, statusErr{code: http.StatusBadGateway, msg: result.ErrorMessage}
		}
		if !result.Committed && output.StopReason == "stop" && output.Usage.Output <= 1 && !ollamaCloudVisible(output) && emptyRetries < ollamaCloudMaxEmptyRetries {
			if errWait := e.wait(ctx, ollamaCloudEmptyRetryBase<<emptyRetries); errWait == nil {
				continue
			}
		}
		replay.flush()
		return output, nil
	}
}

// ollamaCloudVisible is empty-completion-retry's hasVisibleAssistantContent:
// a tool call or non-blank text.
func ollamaCloudVisible(output *cursorwire.Output) bool {
	for _, block := range output.Content {
		if block.Type == "toolCall" || block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			return true
		}
	}
	return false
}

// ollamaCloudLoggedBody records the NDJSON body as it is read.
type ollamaCloudLoggedBody struct {
	ctx  context.Context
	cfg  *config.Config
	body io.Reader
}

func (b *ollamaCloudLoggedBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		helps.AppendAPIResponseChunk(b.ctx, b.cfg, bytes.Clone(p[:n]))
	}
	return n, err
}

// Execute runs one turn and answers with a chat completion.
func (e *OllamaCloudExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	p, err := e.prepare(ctx, auth, req, opts, false)
	if err != nil {
		return resp, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, p.modelID, auth)
	defer reporter.TrackFailure(ctx, &err)
	collector := &cursorCollector{}
	output, err := e.run(ctx, auth, p, reporter, collector)
	if err != nil {
		return resp, err
	}
	completion := encodeCursorCompletion(output, collector, req.Model)
	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	var state any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), p.translated, completion, &state)
	if helps.ApplyPatchTranslationError(state) != nil {
		return resp, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	if len(out) == 0 {
		return resp, statusErr{code: http.StatusBadGateway, msg: "ollama-cloud: response translation produced no output"}
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(completion))
	if format == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

// ExecuteStream runs one turn as chat completion chunks. A failure before
// the first chunk is returned directly, so the caller can try another
// account.
func (e *OllamaCloudExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	p, err := e.prepare(ctx, auth, req, opts, true)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, p.modelID, auth)
	defer reporter.TrackFailure(ctx, &err)

	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	original := helps.ApplyPatchOriginalRequest(req, opts)
	lines := make(chan []byte, 64)
	done := make(chan error, 1)
	encoder := newCursorChunkEncoder(req.Model, func(chunk []byte) error {
		select {
		case lines <- chunk:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	go func() {
		output, errRun := e.run(ctx, auth, p, reporter, encoder)
		if errRun == nil {
			errRun = encoder.finish(output)
		}
		close(lines)
		done <- errRun
	}()

	first, ok := <-lines
	if !ok {
		if errRun := <-done; errRun != nil {
			return nil, errRun
		}
		return nil, statusErr{code: http.StatusBadGateway, msg: "ollama-cloud: stream produced no output"}
	}
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		var usage helps.StreamUsageBuffer
		var state any
		claudeInput := helps.NewClaudeInputTokenState(opts.SourceFormat, sdktranslator.FormatOpenAI, format, original)
		helps.InitializeApplyPatchStream(ctx, sdktranslator.FormatOpenAI, format, req.Model, original, p.translated, &state)
		forward := func(chunk []byte) bool {
			line := append([]byte("data: "), chunk...)
			usage.ObserveOpenAIStream(line)
			helps.ObserveChatTokenEvent(reporter, chunk)
			for _, translated := range helps.TranslateStreamWithClaudeInputTokens(ctx, sdktranslator.FormatOpenAI, format, req.Model, original, p.translated, line, &state, claudeInput) {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: bytes.Clone(translated)}:
				case <-ctx.Done():
					return false
				}
			}
			return helps.ApplyPatchTranslationError(state) == nil
		}
		healthy := forward(first)
		for chunk := range lines {
			if healthy {
				healthy = forward(chunk)
			}
		}
		errRun := <-done
		for _, chunk := range helps.FinalizeApplyPatchStream(state) {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
			case <-ctx.Done():
				return
			}
		}
		if helps.ApplyPatchTranslationError(state) != nil {
			errRun = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		}
		if errRun != nil {
			if ctx.Err() != nil {
				return
			}
			helps.RecordAPIResponseError(ctx, e.cfg, errRun)
			usage.PublishFailure(ctx, reporter, errRun)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errRun}:
			case <-ctx.Done():
			}
			return
		}
		usage.Publish(ctx, reporter)
	}()
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"Content-Type": {"text/event-stream"}}, Chunks: out}, nil
}

// CountTokens estimates locally; Ollama Cloud has no counting call.
func (e *OllamaCloudExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return NewOpenAICompatExecutor(e.Identifier(), e.cfg).CountTokens(ctx, auth, req, opts)
}

// Refresh is a no-op: an API key never refreshes.
func (e *OllamaCloudExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// HttpRequest sends an arbitrary request with the account's key.
func (e *OllamaCloudExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("ollama-cloud executor: request is nil")
	}
	key := ollamaCloudKey(auth)
	if key == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "ollama-cloud executor: account has no API key"}
	}
	request = request.WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+key)
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}
