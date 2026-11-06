package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	awsauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/aws"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/bedrockwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
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
	// amazonBedrockMaxAttempts and amazonBedrockMaxRetryDelay are
	// fetchWithRetry's defaults: five attempts, and a server hint longer than
	// a minute returns the response instead of sleeping on it.
	amazonBedrockMaxAttempts   = 5
	amazonBedrockMaxRetryDelay = time.Minute
	// amazonBedrockRetryBase is the default schedule's 500ms * 2 ** attempt.
	amazonBedrockRetryBase = 500 * time.Millisecond
	// amazonBedrockErrorBodyLimit is the UTF-16 length of the error body a
	// failed request's message carries.
	amazonBedrockErrorBodyLimit = 1000
)

// amazonBedrockDroppedHeaders are SIGNER_OWNED_HEADERS and
// BEDROCK_RESERVED_HEADERS: names the request sets itself, which a caller
// copy in any casing would duplicate.
var amazonBedrockDroppedHeaders = map[string]bool{"host": true, "x-amz-date": true, "x-amz-content-sha256": true, "x-amz-security-token": true,
	"content-type": true, "accept": true, "authorization": true, "content-length": true}

// AmazonBedrockExecutor serves NeoPi's Amazon Bedrock provider
// (streamBedrock, api bedrock-converse-stream): each turn is one Converse
// stream request answered in AWS eventstream framing. A request is read as an
// OpenAI chat, as NeoPi's gateway reads it, and answered as one.
type AmazonBedrockExecutor struct {
	provider string
	cfg      *config.Config
	wait     func(context.Context, time.Duration) error
	now      func() time.Time
	// ambientRegion is resolveAwsAmbientRegion without a profile option.
	ambientRegion func() string
}

// NewAmazonBedrockExecutor builds the Amazon Bedrock executor for the
// account's runtime provider key ("amazon-bedrock" when blank).
func NewAmazonBedrockExecutor(provider string, cfg *config.Config) *AmazonBedrockExecutor {
	if strings.TrimSpace(provider) == "" {
		provider = registry.AmazonBedrockProvider
	}
	return &AmazonBedrockExecutor{provider: provider, cfg: cfg, wait: neoPiWait, now: time.Now,
		ambientRegion: func() string { return awsauth.AmbientRegion("") }}
}

// Identifier is the provider key.
func (e *AmazonBedrockExecutor) Identifier() string { return e.provider }

// UsesConfig reports whether the executor was built for cfg.
func (e *AmazonBedrockExecutor) UsesConfig(cfg *config.Config) bool {
	return e != nil && e.cfg == cfg
}

type amazonBedrockPrepared struct {
	url, modelID string
	request      *bedrockwire.Request
	body         []byte
	finalize     func([]byte) []byte
	headers      http.Header
	translated   []byte
}

// amazonBedrockKey is the account's Amazon Bedrock API key.
func amazonBedrockKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
		return key
	}
	key, _ := auth.Metadata["api_key"].(string)
	return strings.TrimSpace(key)
}

// amazonBedrockRow is the NeoPi row the request resolved to, else the
// bundled row of that id.
func amazonBedrockRow(req cliproxyexecutor.Request, modelID string) (*registry.NeoPiModel, bool) {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.API == "bedrock-converse-stream" {
		return info.NeoPi.Clone(), true
	}
	rows, err := registry.AmazonBedrockBundledModels()
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

// amazonBedrockWireModel is the part of a row the Converse request reads.
func amazonBedrockWireModel(row *registry.NeoPiModel) *bedrockwire.Model {
	model := &bedrockwire.Model{ID: row.ID, Reasoning: row.Reasoning, MaxTokens: row.MaxTokens,
		RequiresToolResultImageHoisting: row.RequiresToolResultImageHoisting}
	if thinking := row.Thinking; thinking != nil {
		model.Mode, model.Efforts, model.EffortMap = thinking.Mode, thinking.Efforts, thinking.EffortMap
		model.SupportsDisplay = thinking.SupportsDisplay != nil && *thinking.SupportsDisplay
		model.PrefixBinding, model.RequiresEffort, model.SuppressWhenOff = thinking.PrefixBinding, thinking.RequiresEffort, thinking.SuppressWhenOff
	}
	if compat := row.BedrockCompat; compat != nil {
		model.PromptCacheMode, model.PromptCacheMaximumCheckpoints = compat.PromptCacheMode, compat.PromptCacheMaximumCheckpoints
		model.SupportsForcedToolChoice, model.SupportsThinkingBindingControls = compat.SupportsForcedToolChoice, compat.SupportsThinkingBindingControls
		model.DisabledThinking, model.SupportsSamplingParams = compat.DisabledThinking, compat.SupportsSamplingParams
	}
	return model
}

func (e *AmazonBedrockExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*amazonBedrockPrepared, error) {
	// resolveAwsBearerToken: the account's key, else AWS_BEARER_TOKEN_BEDROCK.
	// Without one NeoPi signs with SigV4 from the AWS credential chain, which
	// is not ported.
	bearer := awsauth.BearerToken(amazonBedrockKey(auth))
	if bearer == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "amazon-bedrock: no Amazon Bedrock API key; add one to the account or set AWS_BEARER_TOKEN_BEDROCK (AWS IAM credentials are not supported)"}
	}
	modelID := thinking.ParseSuffix(req.Model).ModelName
	row, ok := amazonBedrockRow(req, modelID)
	if !ok {
		return nil, statusErr{code: http.StatusBadRequest, msg: "amazon-bedrock: unknown model " + modelID}
	}
	translated, err := helps.TranslateRequestReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatOpenAI, modelID, bytes.Clone(req.Payload), stream, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	if translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "openai", e.Identifier()); err != nil {
		return nil, err
	}
	parsed, err := cursorwire.ParseOpenAIChat(translated, opts.Headers.Get)
	if err != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	model := amazonBedrockWireModel(row)
	options := bedrockwire.Options{MaxTokens: parsed.MaxOutputTokens, ToolChoice: bedrockwire.ParseToolChoice(gjson.GetBytes(translated, "tool_choice"))}
	options.Reasoning, options.ReasoningOff = bedrockwire.Effort(model, parsed.Reasoning, parsed.ForceReasoningOff)
	if value := gjson.GetBytes(translated, "temperature"); value.Exists() {
		options.Temperature = []byte(value.Raw)
	}
	if value := gjson.GetBytes(translated, "top_p"); value.Exists() {
		options.TopP = []byte(value.Raw)
	}
	request, err := bedrockwire.BuildRequest(model, &parsed.Context, options)
	if err != nil {
		var invalid *bedrockwire.ValidationError
		if errors.As(err, &invalid) {
			return nil, statusErr{code: http.StatusBadRequest, msg: invalid.Message}
		}
		return nil, err
	}
	finalize := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), row.ID, "bedrock", "", translated, req, opts)
	baseURL := ""
	if auth != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if baseURL == "" {
		baseURL = row.BaseURL
	}
	region := bedrockwire.Region(row.ID, e.ambientRegion())
	if baseURL == "" {
		baseURL = bedrockwire.DefaultBaseURL(region)
	}
	scheme, host, path, search, ok := registry.NeoPiURLParts(baseURL)
	if !ok {
		return nil, statusErr{code: http.StatusBadRequest, msg: "amazon-bedrock: invalid base URL " + baseURL}
	}
	return &amazonBedrockPrepared{
		url:        bedrockwire.RequestURL(bedrockwire.Base{Scheme: scheme, Host: host, Path: path, Search: search}, region, row.ID),
		modelID:    row.ID,
		request:    request,
		body:       finalize(request.Body(false)),
		finalize:   finalize,
		headers:    amazonBedrockHeaders(ctx, auth, opts.Headers, bearer),
		translated: translated,
	}, nil
}

// amazonBedrockHeaders is sendBedrockRequest's header set: the account's
// configured headers lowercased, minus the names the request sets itself, a
// User-Agent where none was given, the JSON content type and eventstream
// accept over them, then the bearer; Bun's fetch fills its defaults.
func amazonBedrockHeaders(ctx context.Context, auth *cliproxyauth.Auth, incoming http.Header, bearer string) http.Header {
	configured := make(http.Header)
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs((&http.Request{Header: configured}).WithContext(ctx), auth.Attributes, incoming)
	}
	headers := make(http.Header)
	for name, values := range configured {
		if !amazonBedrockDroppedHeaders[strings.ToLower(name)] {
			headers[name] = values
		}
	}
	if headers.Get("User-Agent") == "" {
		headers.Set("User-Agent", openaiwire.NeoPiUserAgent)
	}
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/vnd.amazon.eventstream")
	headers.Set("Authorization", "Bearer "+bearer)
	for name, value := range map[string]string{"Accept-Encoding": "gzip, deflate, br, zstd", "Connection": "keep-alive"} {
		if headers.Get(name) == "" {
			headers.Set(name, value)
		}
	}
	return headers
}

// amazonBedrockErrorBody is errBody.slice(0, 1000): the first 1000 UTF-16
// units of the body.
func amazonBedrockErrorBody(body []byte) string {
	units := utf16.Encode([]rune(string(body)))
	if len(units) <= amazonBedrockErrorBodyLimit {
		return string(body)
	}
	return string(utf16.Decode(units[:amazonBedrockErrorBodyLimit]))
}

// open sends the Converse request with fetchWithRetry's default schedule
// (network failures and 5xx, 408 and 429 answers retried before any response
// byte, server hints honoured up to a minute). A failure keeps its status
// and headers; a request whose thinking prefix changed is re-sent once
// without its bound reasoning blocks.
func (e *AmazonBedrockExecutor) open(ctx context.Context, auth *cliproxyauth.Auth, p *amazonBedrockPrepared, client *http.Client) (*http.Response, error) {
	body := p.body
	retriedPrefix := false
	for {
		response, err := e.send(ctx, auth, p, body, client)
		if err != nil {
			return nil, err
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		message := "Bedrock HTTP " + strconv.Itoa(response.StatusCode) + ": " + amazonBedrockErrorBody(data)
		if !retriedPrefix && p.request.PrefixBinding && bedrockwire.IsThinkingPrefixBindingError(message) {
			retriedPrefix = true
			body = p.finalize(p.request.Body(true))
			continue
		}
		return nil, &neoPiUpstreamError{code: response.StatusCode, body: message, headers: response.Header}
	}
}

// send is one fetchWithRetry call: it returns the first final response.
func (e *AmazonBedrockExecutor) send(ctx context.Context, auth *cliproxyauth.Auth, p *amazonBedrockPrepared, body []byte, client *http.Client) (*http.Response, error) {
	delay := func(attempt int) time.Duration {
		return min(amazonBedrockRetryBase<<attempt, amazonBedrockMaxRetryDelay)
	}
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header = p.headers.Clone()
		info := helps.UpstreamRequestLog{URL: p.url, Method: http.MethodPost, Headers: request.Header, Body: body, Provider: e.Identifier()}
		if auth != nil {
			info.AuthID, info.AuthLabel = auth.ID, auth.Label
			info.AuthType, info.AuthValue = auth.AccountInfo()
		}
		helps.RecordAPIRequest(ctx, e.cfg, info)
		response, err := client.Do(request)
		if err != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			if ctx.Err() != nil || attempt+1 >= amazonBedrockMaxAttempts {
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
		if !neoPiRetryableStatus(response.StatusCode) || attempt+1 >= amazonBedrockMaxAttempts {
			return response, nil
		}
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		hint, ok := neoPiCompletionsRetryHint(response.Header, data, e.now())
		if ok && hint > amazonBedrockMaxRetryDelay {
			response.Body = io.NopCloser(bytes.NewReader(data))
			return response, nil
		}
		if !ok {
			hint = delay(attempt)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		if errWait := e.wait(ctx, hint); errWait != nil {
			return nil, errWait
		}
	}
}

// run streams one turn into sink and returns its message; a failed turn is an
// error carrying its status (an in-stream exception's service-model status),
// else 502.
func (e *AmazonBedrockExecutor) run(ctx context.Context, auth *cliproxyauth.Auth, p *amazonBedrockPrepared, reporter *helps.UsageReporter, sink cursorwire.Sink) (*cursorwire.Output, error) {
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	response, err := e.open(ctx, auth, p, client)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	result, err := bedrockwire.ReadStream(&ollamaCloudLoggedBody{ctx: ctx, cfg: e.cfg, body: response.Body}, p.request.SentinelInjected, sink)
	if err == nil {
		return result.Output, nil
	}
	if ctx.Err() != nil {
		return result.Output, ctx.Err()
	}
	var failure *bedrockwire.APIError
	if errors.As(err, &failure) {
		code := failure.Status
		if code == 0 {
			code = http.StatusBadGateway
		}
		return result.Output, statusErr{code: code, msg: failure.Message}
	}
	return result.Output, statusErr{code: http.StatusBadGateway, msg: err.Error()}
}

// Execute runs one turn and answers with a chat completion.
func (e *AmazonBedrockExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
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
		return resp, statusErr{code: http.StatusBadGateway, msg: "amazon-bedrock: response translation produced no output"}
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
func (e *AmazonBedrockExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
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
		return nil, statusErr{code: http.StatusBadGateway, msg: "amazon-bedrock: stream produced no output"}
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

// CountTokens estimates locally; NeoPi has no Converse counting call.
func (e *AmazonBedrockExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return NewOpenAICompatExecutor(e.Identifier(), e.cfg).CountTokens(ctx, auth, req, opts)
}

// Refresh is a no-op: an API key never refreshes.
func (e *AmazonBedrockExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// HttpRequest sends an arbitrary request with the account's bearer.
func (e *AmazonBedrockExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("amazon-bedrock executor: request is nil")
	}
	bearer := awsauth.BearerToken(amazonBedrockKey(auth))
	if bearer == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "amazon-bedrock executor: account has no API key"}
	}
	request = request.WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+bearer)
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}
