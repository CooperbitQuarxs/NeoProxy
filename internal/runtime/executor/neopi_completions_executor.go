package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cloudflare"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/snowflake"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/sync/singleflight"
)

// snowflakeRefreshGroup runs one refresh per Snowflake refresh token at a time.
var snowflakeRefreshGroup singleflight.Group

// NeoPiCompletionsProfile adapts the shared NeoPi Chat Completions port to one
// host: where its models come from, how its key is read, and any provider
// hooks NeoPi applies around the shared builder.
type NeoPiCompletionsProfile struct {
	// Provider is NeoPi's provider id, which keys the exported catalog.
	Provider string
	// Row resolves a model id to NeoPi's record; it defaults to the provider's
	// exported catalog (bundled rows plus NeoPi's discovered-id compat).
	Row func(provider, model string) *registry.NeoPiModel
	// APIKey reads the bearer credential; it defaults to the account's api_key.
	APIKey func(auth *cliproxyauth.Auth) string
	// URL is the request endpoint; it defaults to the row's base URL plus
	// /chat/completions.
	URL func(row *registry.NeoPiModel) string
	// Headers runs after NeoPi's own headers are set, for providers that add
	// identity headers of their own.
	Headers func(ctx context.Context, auth *cliproxyauth.Auth, incoming http.Header, key string, row *registry.NeoPiModel, headers http.Header, sessionID string)
	// Body runs after NeoPi's request shaping, for provider-only body fields.
	Body func(ctx context.Context, auth *cliproxyauth.Auth, row *registry.NeoPiModel, body []byte) ([]byte, error)
	// ModelID transforms the model id already resolved by NeoPi's compat rules.
	ModelID func(ctx context.Context, auth *cliproxyauth.Auth, row *registry.NeoPiModel, modelID string) string
	// Request runs on the validated Chat Completions request before NeoPi's
	// shaping, for providers that pass the shared builder their own context
	// and options (Factory Droid's system prefix and tool-name encoding).
	Request func(ctx context.Context, auth *cliproxyauth.Auth, row *registry.NeoPiModel, translated []byte) ([]byte, error)
	// SessionID replaces the options.sessionId the shared builder receives.
	SessionID func(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string
	// Line rewrites each normalized upstream stream line before usage and
	// translation see it (Factory Droid's returned tool names).
	Line func(line []byte) []byte
}

// NeoPiCompletionsExecutor serves a NeoPi openai-completions provider: the
// request built by NeoPi's own buildParams, sent with NeoPi's headers and retry
// policy, relayed as NeoPi's stream. A row NeoPi routes over Anthropic
// Messages (ZenMux's Claude models) goes to NeoPi's Anthropic Messages port
// on that row's endpoint instead.
type NeoPiCompletionsExecutor struct {
	provider string
	profile  NeoPiCompletionsProfile
	cfg      *config.Config
	counter  *OpenAICompatExecutor
	messages *ClaudeExecutor
	wait     func(context.Context, time.Duration) error
}

// NewNeoPiCompletionsExecutor binds the shared port to a provider id.
func NewNeoPiCompletionsExecutor(provider string, cfg *config.Config, profile NeoPiCompletionsProfile) *NeoPiCompletionsExecutor {
	if profile.Provider == "" {
		profile.Provider = provider
	}
	e := &NeoPiCompletionsExecutor{
		provider: provider,
		profile:  profile,
		cfg:      cfg,
		counter:  NewOpenAICompatExecutor(provider, cfg),
		wait:     neoPiWait,
	}
	e.messages = NewClaudeExecutor(cfg)
	e.messages.provider = provider
	e.messages.thirdParty = &neoPiThirdPartyProvider{
		catalog: func(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, _ string) *registry.NeoPiModel {
			return e.row(auth, req)
		},
		credential: e.credential,
		// Vertex's authenticated fetch sets the ADC bearer last.
		enforceBearer: e.profile.Provider == vertexNeoPiProvider,
	}
	if e.profile.Provider == cloudflare.Provider {
		e.messages.thirdParty.gatewayAuthHeader = cloudflareAuthHeader
	}
	return e
}

// anthropicRow reports whether NeoPi serves the request's model over
// Anthropic Messages.
func (e *NeoPiCompletionsExecutor) anthropicRow(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) bool {
	row := e.row(auth, req)
	return row != nil && row.API == "anthropic-messages"
}

// Identifier is the engine provider key this executor is registered under.
func (e *NeoPiCompletionsExecutor) Identifier() string { return e.provider }

// UsesConfig reports whether the executor was built for cfg.
func (e *NeoPiCompletionsExecutor) UsesConfig(cfg *config.Config) bool {
	return e != nil && e.cfg == cfg
}

// SupportsNativeToolSearch keeps these hosts out of Anthropic's native tool search.
func (e *NeoPiCompletionsExecutor) SupportsNativeToolSearch(*cliproxyauth.Auth, string) bool {
	return false
}

// RequestToFormat reports Chat Completions for chat requests, Claude Messages
// for a row served over Anthropic Messages; an image or speech request keeps
// its own media format.
func (e *NeoPiCompletionsExecutor) RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	if openAICompatImageEndpointPath(opts) != "" || xaiIsSpeechRequest(opts) {
		return opts.SourceFormat
	}
	if e.anthropicRow(nil, req) {
		return sdktranslator.FormatClaude
	}
	return sdktranslator.FormatOpenAI
}

// Refresh renews a signed-in Snowflake account's tokens
// (refreshSnowflakeToken); every other provider here takes long-lived keys.
func (e *NeoPiCompletionsExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if e.profile.Provider != snowflake.Provider || auth == nil {
		return auth, nil
	}
	tokens, account, ok := snowflake.RecordTokens(auth.Metadata)
	if !ok {
		return auth, nil
	}
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if cliproxyauth.IsRefreshLocked(auth) {
		return nil, cliproxyauth.ErrRefreshLocked
	}
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	var result any
	var err error
	if tokens.Refresh == "" {
		// Nothing to exchange: the record keeps or loses its own access token,
		// so it must not join another record's flight.
		result, err = snowflake.Refresh(ctx, client, account, tokens)
	} else {
		result, err, _ = snowflakeRefreshGroup.Do(account+"\x00"+tokens.Refresh, func() (any, error) {
			return snowflake.Refresh(ctx, client, account, tokens)
		})
	}
	if err != nil {
		var oauthErr *snowflake.OAuthError
		if errors.As(err, &oauthErr) && (oauthErr.Status == 0 || oauthErr.Status == http.StatusBadRequest || oauthErr.Status == http.StatusUnauthorized) {
			return nil, statusErr{code: http.StatusUnauthorized, msg: err.Error()}
		}
		return nil, err
	}
	renewed := result.(snowflake.Tokens)
	if renewed == tokens {
		return auth, nil
	}
	updated := auth.Clone()
	for key, value := range snowflake.Metadata(renewed, account) {
		updated.Metadata[key] = value
	}
	updated.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	return updated, nil
}

// HttpRequest sends an arbitrary request with the account's bearer
// credential.
func (e *NeoPiCompletionsExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("%s: request is nil", e.provider)
	}
	if e.profile.Provider == vertexNeoPiProvider {
		token, _, err := vertexAuthenticatedRequest(ctx, e.cfg, auth)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
	} else if key := e.apiKey(auth); key != "" {
		if e.profile.Provider == cloudflare.Provider {
			request.Header.Set(cloudflareAuthHeader, "Bearer "+key)
		} else {
			request.Header.Set("Authorization", "Bearer "+key)
		}
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(request, auth.Attributes)
	}
	return e.httpClient(ctx, auth).Do(request)
}

// httpClient is the account's proxy-aware client. Cloudflare's gateway token
// rides in its own header, which net/http would copy onto a redirect to any
// origin, so that client drops it from every cross-origin hop.
func (e *NeoPiCompletionsExecutor) httpClient(ctx context.Context, auth *cliproxyauth.Auth) *http.Client {
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	if e.profile.Provider == cloudflare.Provider {
		return helps.GuardCredentialRedirects(client, cloudflareAuthHeader)
	}
	return client
}

// CountTokens estimates locally from the Chat Completions form.
func (e *NeoPiCompletionsExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.counter.CountTokens(ctx, auth, req, opts)
}

// Execute runs one non-streaming request through NeoPi's stream-shaped wire.
func (e *NeoPiCompletionsExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (result cliproxyexecutor.Response, err error) {
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImage(ctx, auth, req, opts, endpointPath)
	}
	if xaiIsSpeechRequest(opts) {
		return e.executeSpeech(ctx, auth, req, opts)
	}
	if err = e.transportReady(auth, req); err != nil {
		return result, err
	}
	if e.anthropicRow(auth, req) {
		return e.messages.Execute(ctx, auth, req, opts)
	}
	row := e.row(auth, req)
	prepared, err := e.prepare(ctx, auth, req, opts, row)
	if err != nil {
		return result, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model, auth)
	defer reporter.TrackFailure(ctx, &err)
	response, err := e.open(ctx, auth, prepared, reporter.TrackHTTPClient(e.httpClient(ctx, auth)))
	if err != nil {
		return result, err
	}
	defer func() { _ = response.Body.Close() }()
	var usage helps.StreamUsageBuffer
	var lines [][]byte
	err = neoPiScanNormalizedCompletions(ctx, e.cfg, e.provider, row.CompletionsCompat, response.Header, response.Body, func(line []byte) error {
		if e.profile.Line != nil {
			line = e.profile.Line(line)
		}
		usage.ObserveOpenAIStream(line)
		reporter.ObserveResponseModel(line)
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		if line := neoPiCompletionsFailureUsage(err); line != nil {
			usage.ObserveOpenAIStream(line)
		}
		usage.PublishFailure(ctx, reporter, err)
		return result, err
	}
	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	data := neoPiAggregateCompletionStream(lines)
	if format != sdktranslator.FormatOpenAI {
		var state any
		data = sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.body, data, &state)
		if helps.ApplyPatchTranslationError(state) != nil {
			return result, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		}
		if len(data) == 0 {
			return result, fmt.Errorf("%s: response translation failed", e.provider)
		}
	}
	usage.Publish(ctx, reporter)
	log.WithFields(log.Fields{"provider": e.provider, "model": prepared.model}).Info("provider request completed")
	return cliproxyexecutor.Response{Payload: data, Headers: response.Header.Clone()}, nil
}

// ExecuteStream relays NeoPi's upstream stream in the client's format.
func (e *NeoPiCompletionsExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if openAICompatImageEndpointPath(opts) != "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("%s: image generation does not stream; send the request without stream", e.provider)}
	}
	if xaiIsSpeechRequest(opts) {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("%s: speech synthesis does not stream", e.provider)}
	}
	if err = e.transportReady(auth, req); err != nil {
		return nil, err
	}
	if e.anthropicRow(auth, req) {
		return e.messages.ExecuteStream(ctx, auth, req, opts)
	}
	row := e.row(auth, req)
	prepared, err := e.prepare(ctx, auth, req, opts, row)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model, auth)
	defer reporter.TrackFailure(ctx, &err)
	response, err := e.open(ctx, auth, prepared, reporter.TrackHTTPClient(e.httpClient(ctx, auth)))
	if err != nil {
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		defer func() { _ = response.Body.Close() }()
		var usage helps.StreamUsageBuffer
		var state any
		format := cliproxyexecutor.ResponseFormatOrSource(opts)
		original := helps.ApplyPatchOriginalRequest(req, opts)
		helps.InitializeApplyPatchStream(ctx, sdktranslator.FormatOpenAI, format, req.Model, original, prepared.body, &state)
		send := func(chunk []byte) error {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: bytes.Clone(chunk)}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		consumeErr := neoPiScanNormalizedCompletions(ctx, e.cfg, e.provider, row.CompletionsCompat, response.Header, response.Body, func(line []byte) error {
			if e.profile.Line != nil {
				line = e.profile.Line(line)
			}
			usage.ObserveOpenAIStream(line)
			reporter.ObserveResponseModel(line)
			for _, chunk := range sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, format, req.Model, original, prepared.body, line, &state) {
				if errSend := send(chunk); errSend != nil {
					return errSend
				}
			}
			if helps.ApplyPatchTranslationError(state) != nil {
				return statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
			}
			return nil
		})
		for _, chunk := range helps.FinalizeApplyPatchStream(state) {
			if send(chunk) != nil {
				return
			}
		}
		if consumeErr != nil {
			if line := neoPiCompletionsFailureUsage(consumeErr); line != nil {
				usage.ObserveOpenAIStream(line)
			}
			usage.PublishFailure(ctx, reporter, consumeErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: consumeErr}:
			case <-ctx.Done():
			}
			return
		}
		usage.Publish(ctx, reporter)
		log.WithFields(log.Fields{"provider": e.provider, "model": prepared.model}).Info("provider request completed")
	}()
	return &cliproxyexecutor.StreamResult{Headers: response.Header.Clone(), Chunks: out}, nil
}

// neoPiCompletionsPrepared is one shaped request ready to post.
type neoPiCompletionsPrepared struct {
	body    []byte
	headers http.Header
	url     string
	model   string
}

// prepare shapes the downstream body exactly as NeoPi does for this row.
func (e *NeoPiCompletionsExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, row *registry.NeoPiModel) (*neoPiCompletionsPrepared, error) {
	if row == nil || row.CompletionsCompat == nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: e.provider + ": model has no NeoPi completions record"}
	}
	key, place, err := e.credential(ctx, auth)
	if err != nil {
		return nil, err
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	originalTranslated, translated, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatOpenAI, row.ID, original, req.Payload, true, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	if translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "openai", e.provider); err != nil {
		return nil, err
	}
	if e.profile.Request != nil {
		if translated, err = e.profile.Request(ctx, auth, row, translated); err != nil {
			return nil, err
		}
	}
	sessionID := e.sessionID(req, opts)
	body, err := neoPiCompletionsRequest(translated, row, neoPiCompletionsCall{SessionID: sessionID})
	if err != nil {
		return nil, statusErr{code: http.StatusBadGateway, msg: e.provider + ": " + err.Error()}
	}
	if e.profile.ModelID != nil {
		modelID := e.profile.ModelID(ctx, auth, row, gjson.GetBytes(body, "model").String())
		if body, err = sjson.SetBytes(body, "model", modelID); err != nil {
			return nil, err
		}
	}
	if e.profile.Body != nil {
		if body, err = e.profile.Body(ctx, auth, row, body); err != nil {
			return nil, err
		}
	}
	body = helps.NewPayloadFinalizer(e.cfg, e.provider, row.ID, "openai", "", originalTranslated, req, opts)(body)
	headers := e.requestHeaders(ctx, auth, opts.Headers, key, row, sessionID)
	return &neoPiCompletionsPrepared{body: body, headers: headers, url: place(e.url(row)), model: row.ID}, nil
}

// credential is the bearer the request carries and the placement its URL
// takes: the account's key on the row's own endpoint, or for a Vertex
// Model Garden row an ADC access token (createVertexAuthenticatedFetch) on
// the account's project and location.
func (e *NeoPiCompletionsExecutor) credential(ctx context.Context, auth *cliproxyauth.Auth) (string, func(string) string, error) {
	if e.profile.Provider == vertexNeoPiProvider {
		return vertexAuthenticatedRequest(ctx, e.cfg, auth)
	}
	key := e.apiKey(auth)
	if key == "" {
		return "", nil, statusErr{code: http.StatusUnauthorized, msg: e.provider + ": account has no API key"}
	}
	return key, func(url string) string { return url }, nil
}

// requestHeaders are NeoPi's Chat Completions headers for the row, with the
// provider's own additions and Cloudflare's gateway auth header.
func (e *NeoPiCompletionsExecutor) requestHeaders(ctx context.Context, auth *cliproxyauth.Auth, incoming http.Header, key string, row *registry.NeoPiModel, sessionID string) http.Header {
	headers := neoPiCompletionsHeaders(ctx, auth, incoming, key, row, sessionID)
	if e.profile.Headers != nil {
		e.profile.Headers(ctx, auth, incoming, key, row, headers, sessionID)
	}
	if e.profile.Provider == cloudflare.Provider {
		// The gateway takes its token in its own header; NeoPi sends no
		// Authorization or X-Api-Key at all.
		headers.Del("Authorization")
		headers.Del("X-Api-Key")
		headers.Set(cloudflareAuthHeader, "Bearer "+key)
	}
	return headers
}

// sessionID is NeoPi's options.sessionId for a downstream request: the
// client's prompt cache identity, else a stable per-conversation id.
func (e *NeoPiCompletionsExecutor) sessionID(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	if e.profile.SessionID != nil {
		return e.profile.SessionID(req, opts)
	}
	for _, payload := range [][]byte{opts.OriginalRequest, req.Payload} {
		if key := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); key != "" {
			return key
		}
	}
	return helps.ProviderSessionUUID(e.provider, req.Metadata, opts.Metadata)
}

// open posts the shaped request with NeoPi's transport retry policy: 5xx, 408
// and 429 are retried before any response byte, honouring Retry-After hints up
// to one minute.
func (e *NeoPiCompletionsExecutor) open(ctx context.Context, auth *cliproxyauth.Auth, prepared *neoPiCompletionsPrepared, client *http.Client) (*http.Response, error) {
	const maxAttempts = 6
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, prepared.url, bytes.NewReader(prepared.body))
		if err != nil {
			return nil, err
		}
		request.Header = prepared.headers.Clone()
		info := helps.UpstreamRequestLog{URL: request.URL.String(), Method: request.Method, Headers: request.Header, Body: prepared.body, Provider: e.provider}
		if auth != nil {
			info.AuthID, info.AuthLabel = auth.ID, auth.Label
			info.AuthType, info.AuthValue = auth.AccountInfo()
		}
		helps.RecordAPIRequest(ctx, e.cfg, info)
		response, err := client.Do(request)
		if err != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			if attempt+1 >= maxAttempts || ctx.Err() != nil {
				return nil, err
			}
			if waitErr := e.wait(ctx, neoPiCompletionsDefaultRetryDelay(attempt)); waitErr != nil {
				return nil, err
			}
			continue
		}
		helps.RecordAPIResponseMetadata(ctx, e.cfg, response.StatusCode, response.Header)
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			decoded, errDecode := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
			if errDecode != nil {
				_ = response.Body.Close()
				return nil, errDecode
			}
			response.Body = decoded
			return response, nil
		}
		decoded, errDecode := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
		var data []byte
		if errDecode == nil {
			data, _ = io.ReadAll(decoded)
			_ = decoded.Close()
		} else {
			_ = response.Body.Close()
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		if !neoPiRetryableStatus(response.StatusCode) || attempt+1 >= maxAttempts {
			return nil, &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
		}
		// Proxy concurrency-admission failures are handled by session recovery,
		// not the transport retry loop.
		if strings.TrimSpace(response.Header.Get("rate_limit_type")) == "max_parallel_requests" ||
			gjson.GetBytes(data, "rate_limit_type").String() == "max_parallel_requests" ||
			gjson.GetBytes(data, "error.rate_limit_type").String() == "max_parallel_requests" {
			return nil, &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
		}
		// NeoPi's transport honours a Retry-After hint up to a minute; a longer
		// one is surfaced instead of slept on.
		hint, ok := neoPiCompletionsRetryHint(response.Header, data, e.now())
		if ok && hint > time.Minute {
			return nil, &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
		}
		if !ok {
			hint = neoPiCompletionsDefaultRetryDelay(attempt)
		}
		if waitErr := e.wait(ctx, hint); waitErr != nil {
			return nil, &neoPiUpstreamError{code: response.StatusCode, body: string(data), headers: response.Header}
		}
	}
}

// neoPiRetryableStatus is isRetryableStatus.
func neoPiRetryableStatus(status int) bool {
	return status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests
}

// neoPiCompletionsDefaultRetryDelay is fetchWithRetry's fallback schedule.
func neoPiCompletionsDefaultRetryDelay(attempt int) time.Duration {
	return time.Duration(math.Min(float64(500*time.Millisecond)*math.Pow(2, float64(attempt)), float64(time.Minute)))
}

func (e *NeoPiCompletionsExecutor) now() time.Time { return time.Now() }

// apiKey reads the account's bearer credential (for Snowflake, the token of
// its PAT or structured OAuth credential; for Cloudflare, its gateway token).
func (e *NeoPiCompletionsExecutor) apiKey(auth *cliproxyauth.Auth) string {
	if e.profile.APIKey != nil {
		return strings.TrimSpace(e.profile.APIKey(auth))
	}
	if e.profile.Provider == snowflake.Provider {
		credential, ok, err := e.snowflakeCredential(auth)
		if err != nil || !ok {
			return ""
		}
		return credential.Token
	}
	if e.profile.Provider == cloudflare.Provider {
		credential, ok := e.cloudflareCredential(auth)
		if !ok {
			return ""
		}
		return credential.Token
	}
	return neoPiCompletionsAPIKey(auth)
}

// neoPiCompletionsAPIKey is the api_key an imported or pasted key is stored on.
func neoPiCompletionsAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
		return key
	}
	key, _ := auth.Metadata["api_key"].(string)
	return strings.TrimSpace(key)
}

// row resolves the NeoPi record the request must be shaped from: the row the
// account's discovery registered, else NeoPi's bundled or built row, on the
// account's configured endpoint.
func (e *NeoPiCompletionsExecutor) row(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) *registry.NeoPiModel {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil &&
		(info.NeoPi.API == "openai-completions" || info.NeoPi.API == "anthropic-messages") {
		return e.accountRow(auth, info.NeoPi)
	}
	resolve := e.profile.Row
	if resolve == nil {
		resolve = registry.NeoPiCompletionsModel
	}
	return e.accountRow(auth, resolve(e.profile.Provider, req.Model))
}

// accountRow applies NeoPi's provider baseUrl override: an endpoint the
// account names itself (not the catalog's own) replaces the endpoint of every
// row still on one of the provider's bundled endpoints, whatever its
// transport. A row a mapper or discovery placed elsewhere (a Xiaomi Token Plan
// cluster, a ZenMux Anthropic route derived from the account) keeps its
// endpoint; a Token Plan credential always selects its regional endpoint.
// Snowflake's transport then places the effective row on the credential's
// account. Cloudflare's transport first routes the row (prepareModel, run on
// every request's row, configured ones included), then places it on the
// credential's account and gateway.
func (e *NeoPiCompletionsExecutor) accountRow(auth *cliproxyauth.Auth, row *registry.NeoPiModel) *registry.NeoPiModel {
	if row == nil {
		return nil
	}
	provider := e.profile.Provider
	if auth == nil {
		if provider == cloudflare.Provider {
			return registry.NeoPiCloudflarePrepareModel(row)
		}
		return row
	}
	// A Vertex account's base_url belongs to no Model Garden endpoint: NeoPi
	// places those rows on the project and location alone.
	if base := registry.NeoPiAccountBaseURL(provider, auth.Attributes["base_url"]); base != "" && base != row.BaseURL && provider != vertexNeoPiProvider &&
		(provider == "alibaba-token-plan" ||
			(base != registry.NeoPiCompletionsListingBase(provider) && registry.NeoPiCompletionsBundledEndpoint(provider, row.BaseURL))) {
		row = row.Clone()
		row.BaseURL = base
	}
	switch provider {
	case snowflake.Provider:
		return e.snowflakeRow(auth, row)
	case cloudflare.Provider:
		if row = registry.NeoPiCloudflarePrepareModel(row); row == nil {
			return nil
		}
		return e.cloudflareRow(auth, row)
	}
	return row
}

// url is the endpoint NeoPi posts the shaped request to.
func (e *NeoPiCompletionsExecutor) url(row *registry.NeoPiModel) string {
	if e.profile.URL != nil {
		return e.profile.URL(row)
	}
	return strings.TrimRight(row.BaseURL, "/") + "/chat/completions"
}

// snowflakeCredential is the account's Snowflake credential: a signed-in
// account's OAuth token bound to its account, else its key (a PAT, or a
// structured OAuth credential naming its account), else SNOWFLAKE_PAT, as
// NeoPi's transport falls back to.
func (e *NeoPiCompletionsExecutor) snowflakeCredential(auth *cliproxyauth.Auth) (snowflake.Credential, bool, error) {
	if auth != nil {
		if credential, ok, err := snowflake.RecordCredential(auth.Metadata); ok || err != nil {
			return credential, ok, err
		}
	}
	raw := neoPiCompletionsAPIKey(auth)
	if raw == "" {
		raw = os.Getenv("SNOWFLAKE_PAT")
	}
	return snowflake.ParseCredential(raw)
}

// snowflakeRow is snowflakeTransport.prepareRequest's endpoint rewrite: a row
// on the account placeholder moves to the credential's account, else
// SNOWFLAKE_ACCOUNT's. A row that cannot be placed keeps the placeholder,
// which snowflakeReady refuses before any request.
func (e *NeoPiCompletionsExecutor) snowflakeRow(auth *cliproxyauth.Auth, row *registry.NeoPiModel) *registry.NeoPiModel {
	credential, ok, err := e.snowflakeCredential(auth)
	if err != nil || !ok {
		return row
	}
	base, err := snowflake.BaseURL(row.BaseURL, credential, os.Getenv("SNOWFLAKE_ACCOUNT"))
	if err != nil || base == row.BaseURL {
		return row
	}
	row = row.Clone()
	row.BaseURL = base
	return row
}

// cloudflareAuthHeader is the header Cloudflare AI Gateway takes its token in.
const cloudflareAuthHeader = "cf-aig-authorization"

// cloudflareCredential is the account's gateway credential: its key (a
// structured login credential or a plain token), else
// CLOUDFLARE_AI_GATEWAY_API_KEY, as NeoPi's transport falls back to.
func (e *NeoPiCompletionsExecutor) cloudflareCredential(auth *cliproxyauth.Auth) (cloudflare.Credential, bool) {
	raw := neoPiCompletionsAPIKey(auth)
	if raw == "" {
		raw = os.Getenv("CLOUDFLARE_AI_GATEWAY_API_KEY")
	}
	return cloudflare.ParseCredential(raw)
}

// cloudflareRow is cloudflareAiGatewayTransport.prepareRequest's endpoint
// rewrite: a row on the gateway placeholders moves to the credential's
// account and gateway, else CLOUDFLARE_ACCOUNT_ID / CLOUDFLARE_GATEWAY_ID. A
// row that cannot be placed keeps the placeholders, which transportReady
// refuses before any request.
func (e *NeoPiCompletionsExecutor) cloudflareRow(auth *cliproxyauth.Auth, row *registry.NeoPiModel) *registry.NeoPiModel {
	credential, ok := e.cloudflareCredential(auth)
	if !ok {
		return row
	}
	base, err := cloudflare.BaseURL(row.BaseURL, credential, os.Getenv("CLOUDFLARE_ACCOUNT_ID"), os.Getenv("CLOUDFLARE_GATEWAY_ID"))
	if err != nil || base == row.BaseURL {
		return row
	}
	row = row.Clone()
	row.BaseURL = base
	return row
}

// transportReady reports the configuration error NeoPi's Snowflake or
// Cloudflare transport raises for the request's effective endpoint (a
// malformed credential, no account or gateway, an OAuth account that does
// not own the endpoint) before anything is sent.
func (e *NeoPiCompletionsExecutor) transportReady(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) error {
	switch e.profile.Provider {
	case snowflake.Provider:
		return e.snowflakeReady(auth, req)
	case cloudflare.Provider:
		credential, ok := e.cloudflareCredential(auth)
		if !ok {
			return statusErr{code: http.StatusUnauthorized, msg: e.provider + ": account has no gateway token"}
		}
		if row := e.row(auth, req); row != nil {
			if _, err := cloudflare.BaseURL(row.BaseURL, credential, os.Getenv("CLOUDFLARE_ACCOUNT_ID"), os.Getenv("CLOUDFLARE_GATEWAY_ID")); err != nil {
				return statusErr{code: http.StatusBadRequest, msg: e.provider + ": " + err.Error()}
			}
		}
	}
	return nil
}

// snowflakeReady is transportReady for Snowflake.
func (e *NeoPiCompletionsExecutor) snowflakeReady(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) error {
	credential, ok, err := e.snowflakeCredential(auth)
	if err != nil {
		return statusErr{code: http.StatusUnauthorized, msg: e.provider + ": " + err.Error()}
	}
	if !ok {
		return statusErr{code: http.StatusUnauthorized, msg: e.provider + ": account has no credential"}
	}
	if row := e.row(auth, req); row != nil {
		if _, err = snowflake.BaseURL(row.BaseURL, credential, os.Getenv("SNOWFLAKE_ACCOUNT")); err != nil {
			return statusErr{code: http.StatusBadRequest, msg: e.provider + ": " + err.Error()}
		}
	}
	return nil
}
