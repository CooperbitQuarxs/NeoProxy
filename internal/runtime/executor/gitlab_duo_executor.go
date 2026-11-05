package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/gitlabduo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"golang.org/x/sync/singleflight"
)

// GitLabDuoExecutor serves NeoPi's GitLab Duo (non-agentic) provider
// (streamGitLabDuo): each request buys a direct-access token with the
// account's GitLab token and goes to GitLab's AI gateway proxy over the API
// its model routes to (Anthropic Messages, Chat Completions or Responses),
// on the shared NeoPi port for that API, under the upstream model id.
type GitLabDuoExecutor struct {
	// provider is the executor key: gitlab-duo for sign-ins, the entry's own
	// key for a personal access token added as an openai-compatibility key.
	provider    string
	cfg         *config.Config
	completions *NeoPiCompletionsExecutor
	refresh     singleflight.Group
}

// NewGitLabDuoExecutor builds the GitLab Duo executor under provider.
func NewGitLabDuoExecutor(provider string, cfg *config.Config) *GitLabDuoExecutor {
	return &GitLabDuoExecutor{provider: provider, cfg: cfg, completions: NewNeoPiCompletionsExecutor(provider, cfg, NeoPiCompletionsProfile{})}
}

// Identifier is the provider key.
func (e *GitLabDuoExecutor) Identifier() string { return e.provider }

// UsesConfig reports whether the executor was built for cfg.
func (e *GitLabDuoExecutor) UsesConfig(cfg *config.Config) bool { return e != nil && e.cfg == cfg }

// gitlabDuoToken is the GitLab token an account carries: a personal access
// token entered as a key, else the sign-in's access token.
func gitlabDuoToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
		return key
	}
	if credential, ok := gitlabduo.RecordCredential(auth.Metadata); ok {
		return credential.Access
	}
	return ""
}

// gitlabDuoAttempt is one request bound to the account's direct access.
type gitlabDuoAttempt struct {
	row    *registry.NeoPiModel
	direct gitlabduo.DirectAccess
}

// attempt resolves the request's row and the account's direct access.
func (e *GitLabDuoExecutor) attempt(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request) (*gitlabDuoAttempt, error) {
	token := gitlabDuoToken(auth)
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "Missing GitLab access token. Run /login gitlab-duo or set GITLAB_TOKEN."}
	}
	id := thinking.ParseSuffix(req.Model).ModelName
	var row *registry.NeoPiModel
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.Provider == gitlabduo.Provider {
		row = info.NeoPi.Clone()
	} else if bundled, err := registry.GitLabDuoBundledModels(); err == nil {
		for _, candidate := range bundled {
			if candidate.ID == id {
				row = candidate
				break
			}
		}
	}
	if row == nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: "Unsupported GitLab Duo model: " + id}
	}
	direct, err := gitlabduo.GetDirectAccess(ctx, helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0), token)
	if err != nil {
		var mint *gitlabduo.Error
		if errors.As(err, &mint) {
			return nil, statusErr{code: mint.Status, msg: mint.Message}
		}
		return nil, err
	}
	return &gitlabDuoAttempt{row: row, direct: direct}, nil
}

// gitlabDuoWire is the shared transport one model's route runs on.
type gitlabDuoWire interface {
	Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

// wire binds the attempt's route to its shared port. NeoPi's headers are
// the minted set under the caller's (here the account's configured ones).
func (e *GitLabDuoExecutor) wire(a *gitlabDuoAttempt, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (gitlabDuoWire, error) {
	row := a.row
	switch row.API {
	case "anthropic-messages":
		messages := NewClaudeExecutor(e.cfg)
		messages.provider = e.provider
		messages.thirdParty = &neoPiThirdPartyProvider{
			catalog:       func(*cliproxyauth.Auth, cliproxyexecutor.Request, string) *registry.NeoPiModel { return row },
			credential:    neoPiAccountBearer(func(*cliproxyauth.Auth) string { return a.direct.Token }),
			oauth:         true,
			callerHeaders: a.direct.Headers,
		}
		return messages, nil
	case "openai-completions":
		exec := *e.completions
		exec.profile = NeoPiCompletionsProfile{
			Provider: gitlabduo.Provider,
			Row:      func(string, string) *registry.NeoPiModel { return row },
			APIKey:   func(*cliproxyauth.Auth) string { return a.direct.Token },
			// streamGitLabDuo passes {...minted, ...configured} as the caller's
			// headers, which win over the OpenAI client's Accept, Content-Type
			// and Authorization; only the computed timeout is set after them.
			Headers: func(ctx context.Context, auth *cliproxyauth.Auth, incoming http.Header, _ string, row *registry.NeoPiModel, headers http.Header, _ string) {
				a.underConfigured(ctx, auth, incoming, headers)
				util.ApplyCustomHeadersFromAttrs((&http.Request{Header: headers}).WithContext(ctx), gitlabDuoAttrs(auth), incoming)
				if timeout := neoPiCompletionsTimeoutHeader(row); timeout != "" {
					headers.Set("X-Stainless-Timeout", timeout)
				}
			},
		}
		return &exec, nil
	case "openai-responses":
		exec := NewNeoPiResponsesExecutor(e.provider, e.cfg)
		exec.StreamSimple = true
		session := neoPiSession(opts, nil)
		if session == "" {
			session = neoPiResponsesSessionID(gitlabduo.Provider, req, opts)
		}
		// streamGitLabDuo's inner model is named by its upstream id.
		inner := row.Clone()
		if inner.RequestModelID != "" {
			inner.ID, inner.RequestModelID = inner.RequestModelID, ""
		}
		exec.binding = &neoPiResponsesBinding{model: inner, token: a.direct.Token, session: session,
			headers: func(ctx context.Context, auth *cliproxyauth.Auth, header, clientHeaders http.Header) {
				a.underConfigured(ctx, auth, clientHeaders, header)
				util.ApplyCustomHeadersFromAttrs((&http.Request{Header: header}).WithContext(ctx), gitlabDuoAttrs(auth), clientHeaders)
			}}
		return exec, nil
	}
	return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("Missing GitLab Duo API route for model: %s", row.ID)}
}

// underConfigured sets the minted headers the account did not configure.
func (a *gitlabDuoAttempt) underConfigured(ctx context.Context, auth *cliproxyauth.Auth, incoming, headers http.Header) {
	configured := make(http.Header)
	util.ApplyCustomHeadersFromAttrs((&http.Request{Header: configured}).WithContext(ctx), gitlabDuoAttrs(auth), incoming)
	for name, values := range a.direct.Headers {
		if _, set := configured[name]; !set {
			headers[name] = append([]string(nil), values...)
		}
	}
}

func gitlabDuoAttrs(auth *cliproxyauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	return auth.Attributes
}

// Execute runs one non-streaming request.
func (e *GitLabDuoExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	a, err := e.attempt(ctx, auth, req)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	wire, err := e.wire(a, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return wire.Execute(ctx, auth, req, opts)
}

// ExecuteStream relays the upstream stream in the client's format.
func (e *GitLabDuoExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	a, err := e.attempt(ctx, auth, req)
	if err != nil {
		return nil, err
	}
	wire, err := e.wire(a, req, opts)
	if err != nil {
		return nil, err
	}
	return wire.ExecuteStream(ctx, auth, req, opts)
}

// CountTokens estimates locally from the Chat Completions form.
func (e *GitLabDuoExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.completions.CountTokens(ctx, auth, req, opts)
}

// Refresh runs the sign-in's refresh grant and drops every minted direct
// access token, as NeoPi's after-refresh hook does. A personal access token
// never refreshes; an imported NeoPi sign-in is refresh-locked, GitLab
// rotating the refresh token NeoPi still holds.
func (e *GitLabDuoExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
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
		refreshed, errRefresh := gitlabduo.Duo.Refresh(ctx, helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0), credential.Refresh)
		if errRefresh != nil {
			return nil, errRefresh
		}
		gitlabduo.ClearDirectAccessCache()
		next := auth.Clone()
		if next.Metadata == nil {
			next.Metadata = map[string]any{}
		}
		for key, field := range gitlabduo.Duo.Metadata(refreshed) {
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
func (e *GitLabDuoExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("gitlab-duo executor: request is nil")
	}
	token := gitlabDuoToken(auth)
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "gitlab-duo executor: account has no token"}
	}
	request = request.WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+token)
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}
