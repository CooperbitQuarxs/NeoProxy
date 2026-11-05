package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	umansauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/umans"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// UmansExecutor serves a Umans AI Coding Plan key the way NeoPi's `umans`
// provider does: every model the key's /v1/models/info lists, over Anthropic
// Messages at api.code.umans.ai, authenticated with X-Api-Key.
type UmansExecutor struct {
	*ClaudeExecutor
	cfg *config.Config
	// counter estimates tokens locally; NeoPi never asks Umans to count.
	counter *OpenAICompatExecutor
}

// NewUmansExecutor binds the Umans executor to an openai-compatibility
// provider key.
func NewUmansExecutor(provider string, cfg *config.Config) *UmansExecutor {
	messages := NewClaudeExecutor(cfg)
	messages.provider = provider
	messages.thirdParty = &neoPiThirdPartyProvider{
		catalog: func(_ *cliproxyauth.Auth, req cliproxyexecutor.Request, model string) *registry.NeoPiModel {
			return umansDiscoveredModel(req, model)
		},
		bearer:           umansAPIKey,
		apiKeyHeader:     true,
		gatewayWebSearch: umansauth.WebSearch,
	}
	return &UmansExecutor{ClaudeExecutor: messages, cfg: cfg, counter: NewOpenAICompatExecutor(provider, cfg)}
}

// umansDiscoveredModel is the NeoPi record discovery registered for the
// account the request was routed to, else NeoPi's bundled record, which NeoPi
// serves until discovery answers.
func umansDiscoveredModel(req cliproxyexecutor.Request, model string) *registry.NeoPiModel {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.Provider == umansauth.Provider {
		return info.NeoPi
	}
	return registry.UmansBundledNeoPiModel(model)
}

func umansAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
		return key
	}
	key, _ := auth.Metadata["api_key"].(string)
	return strings.TrimSpace(key)
}

// umansBaseURL is normalizeUmansBaseUrl over the account's base URL: no
// trailing slash, no /v1.
func umansBaseURL(auth *cliproxyauth.Auth) string {
	base := ""
	if auth != nil {
		base = strings.TrimRight(strings.TrimSpace(auth.Attributes["base_url"]), "/")
	}
	if base == "" {
		return umansauth.BaseURL
	}
	return strings.TrimSuffix(base, "/v1")
}

// UsesConfig reports whether the executor was built for cfg.
func (e *UmansExecutor) UsesConfig(cfg *config.Config) bool { return e != nil && e.cfg == cfg }

// SupportsNativeToolSearch keeps Umans out of Anthropic's native tool search.
func (e *UmansExecutor) SupportsNativeToolSearch(*cliproxyauth.Auth, string) bool { return false }

// Refresh is a no-op: a Umans key never expires.
func (e *UmansExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// HttpRequest sends an arbitrary request with the account's key in X-Api-Key.
func (e *UmansExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("umans executor: request is nil")
	}
	if key := umansAPIKey(auth); key != "" {
		request.Header.Del("Authorization")
		request.Header.Set("X-Api-Key", key)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(request, auth.Attributes)
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}

// CountTokens estimates locally from the Chat Completions form.
func (e *UmansExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.counter.CountTokens(ctx, auth, req, opts)
}

// FetchUmansModels lists a key's models as NeoPi's umans discovery does:
// GET {base}/v1/models/info with the key in x-api-key, each entry resolved
// over NeoPi's bundled row of the same id.
func FetchUmansModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	base := umansBaseURL(auth)
	url := base + "/v1/models/info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if key := umansAPIKey(auth); key != "" {
		req.Header.Set("x-api-key", key)
	}
	req.Header.Set("User-Agent", "Bun/1.3.14")
	resp, err := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0).Do(req)
	if err != nil {
		return nil, fmt.Errorf("umans models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("umans models: %s returned HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("umans models: read %s: %w", url, err)
	}
	models := registry.UmansModels(body, base)
	if models == nil {
		return nil, fmt.Errorf("umans models: %s did not answer a model map", url)
	}
	return models, nil
}
