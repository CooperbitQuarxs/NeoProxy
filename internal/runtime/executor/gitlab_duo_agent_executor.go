package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/gitlabduo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/duoworkflow"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"
)

// gitlabDuoAgentRuntime holds the live Duo workflows across requests and
// configuration reloads, as cursorConversations holds Cursor's.
var gitlabDuoAgentRuntime = duoworkflow.NewRuntime()

// gitlabDuoAgentDial opens an account's workflow sockets through its proxy;
// tests replace it.
var gitlabDuoAgentDial = func(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) duoworkflow.Dialer {
	return duoworkflow.WebSocketDialer(newProxyAwareWebsocketDialer(ctx, cfg, auth))
}

// GitLabDuoAgentExecutor serves NeoPi's GitLab Duo Agent provider
// (streamGitLabDuoWorkflow): each turn runs on a GitLab Duo Agent Platform
// workflow over its WebSocket, with the client's tools as the workflow's MCP
// tools. A request is read as an OpenAI chat, as NeoPi's gateway reads it,
// and answered as one.
type GitLabDuoAgentExecutor struct {
	cfg     *config.Config
	refresh singleflight.Group
}

// NewGitLabDuoAgentExecutor builds the GitLab Duo Agent executor.
func NewGitLabDuoAgentExecutor(cfg *config.Config) *GitLabDuoAgentExecutor {
	return &GitLabDuoAgentExecutor{cfg: cfg}
}

// Identifier is the provider key.
func (e *GitLabDuoAgentExecutor) Identifier() string { return gitlabduo.AgentProvider }

type gitlabDuoAgentPrepared struct {
	token, baseURL, modelID, key string
	request                      *cursorwire.Request
	toolChoiceNone               bool
	translated                   []byte
	finalize                     helps.PayloadFinalizer
}

func (e *GitLabDuoAgentExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*gitlabDuoAgentPrepared, error) {
	token := gitlabDuoToken(auth)
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "Missing GitLab access token. Run /login gitlab-duo-agent or set GITLAB_TOKEN."}
	}
	modelID := thinking.ParseSuffix(req.Model).ModelName
	baseURL := ""
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.Provider == gitlabduo.AgentProvider {
		baseURL = info.NeoPi.BaseURL
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
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
	key := cursorCacheKey(original, opts.Headers)
	if key == "" {
		key = request.PromptCacheKey
	}
	if key == "" {
		key = cursorwire.DeriveSessionID(request.ModelID, &request.Context)
	}
	// The gateway hands only tool_choice "none" on as an option the
	// workflow acts on: the tools are then not advertised.
	choice := gjson.GetBytes(translated, "tool_choice")
	return &gitlabDuoAgentPrepared{token: token, baseURL: baseURL, modelID: modelID, key: key, request: request, translated: translated,
		toolChoiceNone: choice.Type == gjson.String && choice.Str == "none",
		finalize:       helps.NewPayloadFinalizer(e.cfg, e.Identifier(), modelID, gitlabduo.AgentProvider, "", translated, req, opts)}, nil
}

// run streams one turn into sink and returns its message; a failed turn is
// an error carrying the status GitLab answered with, else 502.
func (e *GitLabDuoAgentExecutor) run(ctx context.Context, auth *cliproxyauth.Auth, p *gitlabDuoAgentPrepared, reporter *helps.UsageReporter, sink cursorwire.Sink) (*cursorwire.Output, error) {
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	out := gitlabDuoAgentRuntime.Stream(ctx, &duoworkflow.Request{
		APIKey: p.token, BaseURL: p.baseURL, ModelID: p.modelID, SessionID: p.key, Context: &p.request.Context,
		Options: duoworkflow.Options{ToolChoiceNone: p.toolChoiceNone}, Client: client, Finalize: p.finalize,
		Sink: func(event duoworkflow.Event) { forwardGitLabDuoAgentEvent(sink, event) },
		Dial: gitlabDuoAgentDial(ctx, e.cfg, auth),
	})
	output := &cursorwire.Output{Content: out.Content, Usage: out.Usage, StopReason: out.StopReason}
	if out.StopReason != "error" {
		return output, nil
	}
	if ctx.Err() != nil {
		return output, ctx.Err()
	}
	code := out.ErrorStatus
	if code == 0 {
		code = http.StatusBadGateway
	}
	return output, statusErr{code: code, msg: out.ErrorMessage}
}

// forwardGitLabDuoAgentEvent hands a turn's content events to the OpenAI
// chat encoder.
func forwardGitLabDuoAgentEvent(sink cursorwire.Sink, event duoworkflow.Event) {
	switch event.Type {
	case "text_delta":
		sink.TextDelta(event.Delta)
	case "thinking_delta":
		sink.ThinkingDelta(event.Delta)
	case "toolcall_start":
		sink.ToolCallStart(event.Block)
	case "toolcall_delta":
		sink.ToolCallDelta(event.Block, event.Delta)
	case "toolcall_end":
		sink.ToolCallEnd(event.Block)
	}
}

// Execute runs one turn and answers with a chat completion.
func (e *GitLabDuoAgentExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
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
	helps.AppendAPIResponseChunk(ctx, e.cfg, completion)
	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	var state any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), p.translated, completion, &state)
	if helps.ApplyPatchTranslationError(state) != nil {
		return resp, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	if len(out) == 0 {
		return resp, statusErr{code: http.StatusBadGateway, msg: "gitlab-duo-agent: response translation produced no output"}
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
func (e *GitLabDuoAgentExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
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
		return nil, statusErr{code: http.StatusBadGateway, msg: "gitlab-duo-agent: stream produced no output"}
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
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
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

// CountTokens estimates locally; the workflow has no counting call.
func (e *GitLabDuoAgentExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return NewOpenAICompatExecutor(e.Identifier(), e.cfg).CountTokens(ctx, auth, req, opts)
}

// Refresh runs the VS Code application's refresh grant, which carries its
// redirect. A personal access token never refreshes; an imported NeoPi
// sign-in is refresh-locked, GitLab rotating the refresh token NeoPi holds.
func (e *GitLabDuoAgentExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if cliproxyauth.IsRefreshLocked(auth) {
		return nil, cliproxyauth.ErrRefreshLocked
	}
	if auth == nil || strings.TrimSpace(auth.Attributes["api_key"]) != "" {
		return auth, nil
	}
	credential, ok := gitlabduo.RecordCredential(auth.Metadata)
	if !ok || credential.Refresh == "" {
		return auth, nil
	}
	// GitLab rotates the refresh token: one grant per token at a time.
	value, err, _ := e.refresh.Do(auth.ID+"\x00"+credential.Refresh, func() (any, error) {
		refreshed, errRefresh := gitlabduo.Agent.Refresh(ctx, helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0), credential.Refresh)
		if errRefresh != nil {
			return nil, errRefresh
		}
		next := auth.Clone()
		if next.Metadata == nil {
			next.Metadata = map[string]any{}
		}
		for key, field := range gitlabduo.Agent.Metadata(refreshed) {
			next.Metadata[key] = field
		}
		next.LastRefreshedAt = time.Now()
		return next, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*cliproxyauth.Auth).Clone(), nil
}

// HttpRequest sends an arbitrary request with the account's GitLab token.
func (e *GitLabDuoAgentExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("gitlab-duo-agent executor: request is nil")
	}
	token := gitlabDuoToken(auth)
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "gitlab-duo-agent executor: account has no token"}
	}
	request = request.WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+token)
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}

// FetchGitLabDuoAgentModels is fetchGitLabDuoWorkflowModels: the models
// GitLab offers the account's Duo namespace (pinned, else selectable, else
// the default), as NeoPi's rows. A namespace whose models vanish lists none,
// which keeps the account on its seed.
func FetchGitLabDuoAgentModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	token := gitlabDuoToken(auth)
	if token == "" {
		return nil, errors.New("gitlab-duo-agent models: account has no token")
	}
	models, err := duoworkflow.FetchModels(ctx, duoworkflow.DiscoveryConfig{APIKey: token, Client: helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)})
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, errors.New("gitlab-duo-agent models: the namespace offers no models")
	}
	out := make([]*registry.ModelInfo, len(models))
	for i, model := range models {
		out[i] = registry.GitLabDuoAgentModelInfo(registry.GitLabDuoAgentRow(model.ID, model.Name, model.BaseURL, model.ContextWindow))
	}
	return out, nil
}
