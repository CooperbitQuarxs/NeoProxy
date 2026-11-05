package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/githubcopilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// GitHubCopilotExecutor serves NeoPi's GitHub Copilot provider: each model
// on its own API (Anthropic Messages, Chat Completions or Responses), on the
// shared NeoPi port for that API, with Copilot's client identity.
type GitHubCopilotExecutor struct {
	cfg         *config.Config
	completions *NeoPiCompletionsExecutor
}

// NewGitHubCopilotExecutor builds the GitHub Copilot executor.
func NewGitHubCopilotExecutor(cfg *config.Config) *GitHubCopilotExecutor {
	return &GitHubCopilotExecutor{cfg: cfg, completions: NewNeoPiCompletionsExecutor(githubcopilot.Provider, cfg, NeoPiCompletionsProfile{})}
}

// Identifier is the provider key.
func (e *GitHubCopilotExecutor) Identifier() string { return githubcopilot.Provider }

// githubCopilotAttempt is one request bound to the account serving it.
type githubCopilotAttempt struct {
	row        *registry.NeoPiModel
	credential *githubcopilot.Credential
	// explicit is the Copilot-Integration-Id the account configured (a pin
	// is never second-guessed); it falls back to COPILOT_INTEGRATION_ID.
	explicit string
	// cacheKey keys the identity learned for this credential and host;
	// cached is that identity as the request's headers are built from it,
	// the snapshot the fallback decides a reverse retry on.
	cacheKey, cached string
	// configured is the headers the account configured, resolved against
	// the client's request: NeoPi's caller headers, whose X-Initiator
	// overrides the inferred initiator and whose Copilot-Integration-Id pins
	// the identity.
	configured http.Header
}

// identityFallback scopes NeoPi's Copilot identity fallback to every client
// the wire builds for ctx; an explicit identity disables it.
func (a *githubCopilotAttempt) identityFallback(ctx context.Context) context.Context {
	pinned := githubcopilot.NormalizeIntegrationID(a.explicit) != ""
	return helps.WithTransportWrapper(ctx, func(next http.RoundTripper) http.RoundTripper {
		return helps.GitHubCopilotIdentityFallback(next, pinned, a.cacheKey, a.cached)
	})
}

// attempt resolves the request's model row on the credential's endpoint.
func (e *GitHubCopilotExecutor) attempt(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*githubCopilotAttempt, error) {
	var credential *githubcopilot.Credential
	if auth != nil {
		credential, _ = githubcopilot.RecordCredential(auth.Metadata)
	}
	if credential == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "No GitHub Copilot credentials found. Sign in to GitHub Copilot (device code)."}
	}
	id := thinking.ParseSuffix(req.Model).ModelName
	var row *registry.NeoPiModel
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.Provider == githubcopilot.Provider {
		row = info.NeoPi.Clone()
	} else if bundled, err := registry.GitHubCopilotBundledModels(); err == nil {
		for _, candidate := range bundled {
			if candidate.ID == id {
				row = candidate
				break
			}
		}
	}
	if row == nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("github-copilot: model %s is not in the GitHub Copilot catalog", id)}
	}
	row.BaseURL = githubcopilot.ResolveBaseURL(row.BaseURL, credential)
	configured := make(http.Header)
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs((&http.Request{Header: configured}).WithContext(ctx), auth.Attributes, opts.Headers)
	}
	explicit := configured.Get("Copilot-Integration-Id")
	if githubcopilot.NormalizeIntegrationID(explicit) == "" {
		explicit = os.Getenv("COPILOT_INTEGRATION_ID")
	}
	cacheKey := helps.GitHubCopilotIntegrationCacheKey(credential, row.BaseURL)
	return &githubCopilotAttempt{row: row, credential: credential, explicit: explicit, cacheKey: cacheKey,
		cached: helps.GitHubCopilotCachedIntegrationID(cacheKey), configured: configured}, nil
}

// dynamicHeaders is the turn's Copilot identity over headers. NeoPi's model
// headers sit under the configured (caller) ones, so they only fill names the
// account did not configure, and the dynamic set lands over both
// (resolveOpenAIRequestSetup; the Anthropic branch reapplies the caller's
// headers after). The initiator is the client's X-Initiator, else the
// configured one (buildCopilotDynamicHeaders reads the merged model and
// caller headers), else the inferred one.
func (a *githubCopilotAttempt) dynamicHeaders(headers, incoming http.Header, initiator string, hasImages bool) {
	if override := helps.GitHubCopilotInitiatorOverride(incoming); override != "" {
		initiator = override
	} else if override := helps.GitHubCopilotInitiatorOverride(a.configured); override != "" {
		initiator = override
	}
	for name, values := range githubcopilot.APIHeaders() {
		if _, set := a.configured[name]; !set {
			headers[name] = values
		}
	}
	for name, values := range helps.GitHubCopilotDynamicHeaders(initiator, hasImages, a.explicit, a.cached, a.credential.EnterpriseURL != "") {
		headers[name] = values
	}
}

// githubCopilotWire is the shared transport one model's API runs on.
type githubCopilotWire interface {
	Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

// wire binds the attempt's API to its shared port.
func (e *GitHubCopilotExecutor) wire(a *githubCopilotAttempt, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (githubCopilotWire, error) {
	switch a.row.API {
	case "anthropic-messages":
		messages := NewClaudeExecutor(e.cfg)
		messages.provider = githubcopilot.Provider
		row := a.row
		messages.thirdParty = &neoPiThirdPartyProvider{
			catalog:    func(*cliproxyauth.Auth, cliproxyexecutor.Request, string) *registry.NeoPiModel { return row },
			credential: neoPiAccountBearer(func(*cliproxyauth.Auth) string { return a.credential.AccessToken }),
			copilot: func(translated []byte, headers, incoming http.Header) {
				initiator, hasImages := helps.GitHubCopilotClaudeTurn(translated)
				a.dynamicHeaders(headers, incoming, initiator, hasImages)
			},
		}
		return messages, nil
	case "openai-completions":
		row := a.row
		initiator, hasImages := "user", false
		exec := *e.completions
		exec.profile = NeoPiCompletionsProfile{
			Provider: githubcopilot.Provider,
			Row:      func(string, string) *registry.NeoPiModel { return row },
			APIKey:   func(*cliproxyauth.Auth) string { return a.credential.AccessToken },
			Request: func(_ context.Context, _ *cliproxyauth.Auth, _ *registry.NeoPiModel, translated []byte) ([]byte, error) {
				initiator, hasImages = helps.GitHubCopilotChatTurn(translated)
				return translated, nil
			},
			Headers: func(_ context.Context, _ *cliproxyauth.Auth, incoming http.Header, _ string, _ *registry.NeoPiModel, headers http.Header, _ string) {
				a.dynamicHeaders(headers, incoming, initiator, hasImages)
			},
		}
		return &exec, nil
	case "openai-responses":
		exec := NewNeoPiResponsesExecutor(githubcopilot.Provider, e.cfg)
		// streamSimple: the model's whole output allowance when the caller
		// names none, mandatory reasoning at its floor.
		exec.StreamSimple = true
		session := neoPiSession(opts, nil)
		if session == "" {
			session = neoPiResponsesSessionID(githubcopilot.Provider, req, opts)
		}
		initiator := "user"
		exec.binding = &neoPiResponsesBinding{model: a.row, token: a.credential.AccessToken, session: session,
			shape: func(conversation *openaiwire.Context, _ *openaiwire.Options) {
				initiator = helps.GitHubCopilotResponsesInitiator(conversation.Input)
			},
			// NeoPi's Responses server boundary keeps a user turn's content as
			// its native item (parseRequest), so its messages carry no image
			// content and the Responses route never asks for vision. The
			// configured headers land first and the dynamic set over them, as
			// resolveOpenAIRequestSetup merges options.headers.
			headers: func(_ context.Context, _ *cliproxyauth.Auth, header, clientHeaders http.Header) {
				for name, values := range a.configured {
					header[name] = values
				}
				a.dynamicHeaders(header, clientHeaders, initiator, false)
			}}
		return exec, nil
	}
	return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("github-copilot: model %s has unknown api %s", a.row.ID, a.row.API)}
}

// Execute runs one non-streaming request.
func (e *GitHubCopilotExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	a, err := e.attempt(ctx, auth, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	wire, err := e.wire(a, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return wire.Execute(a.identityFallback(ctx), auth, req, opts)
}

// ExecuteStream relays the upstream stream in the client's format.
func (e *GitHubCopilotExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	a, err := e.attempt(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	wire, err := e.wire(a, req, opts)
	if err != nil {
		return nil, err
	}
	return wire.ExecuteStream(a.identityFallback(ctx), auth, req, opts)
}

// CountTokens estimates locally from the Chat Completions form.
func (e *GitHubCopilotExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.completions.CountTokens(ctx, auth, req, opts)
}

// Refresh returns the account unchanged: the GitHub token is long-lived and
// NeoPi's refresh hook returns it as it is.
func (e *GitHubCopilotExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// HttpRequest sends an arbitrary request with the account's bearer.
func (e *GitHubCopilotExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("github-copilot executor: request is nil")
	}
	credential, ok := githubcopilot.RecordCredential(auth.Metadata)
	if !ok {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "github-copilot executor: account has no token"}
	}
	request = request.WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(credential.AccessToken))
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}

// FetchGitHubCopilotModels is githubCopilotModelManagerOptions'
// fetchDynamicModels for an account: on the personal host the listing comes
// from the plan endpoint GitHub advertises for the token (falling back to the
// host), and GET /models carries Copilot's discovery identity. NeoPi bounds
// both requests at 10s; the repository bans deadlines once the connection is
// up, so neither has one.
func FetchGitHubCopilotModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	var credential *githubcopilot.Credential
	if auth != nil {
		credential, _ = githubcopilot.RecordCredential(auth.Metadata)
	}
	if credential == nil {
		return nil, fmt.Errorf("github-copilot models: account has no token")
	}
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	base := githubcopilot.ResolveBaseURL(githubcopilot.PersonalBaseURL, credential)
	if base == githubcopilot.PersonalBaseURL {
		if endpoint := githubcopilot.NewAuth(client).DiscoverAPIEndpoint(ctx, credential.AccessToken); endpoint != "" {
			base = endpoint
		}
	}
	url := base + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header = githubcopilot.DiscoveryHeaders()
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github-copilot models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("github-copilot models: %s returned HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("github-copilot models: read %s: %w", url, err)
	}
	return registry.GitHubCopilotListing(base, body)
}
