package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// KimiExecutor serves a Kimi Code sign-in the way NeoPi's `kimi-code`
// provider does: every model the account's /coding/v1/models listing returns,
// over Anthropic Messages at api.kimi.com/coding, with the Kimi CLI identity
// headers.
type KimiExecutor struct {
	*ClaudeExecutor
	cfg *config.Config
	// counter estimates tokens locally; NeoPi never asks Kimi to count.
	counter *OpenAICompatExecutor
}

// NewKimiExecutor builds the Kimi Code executor.
func NewKimiExecutor(cfg *config.Config) *KimiExecutor {
	messages := NewClaudeExecutor(cfg)
	messages.provider = kimiauth.Type
	messages.thirdParty = &neoPiThirdPartyProvider{
		catalog: func(_ *cliproxyauth.Auth, req cliproxyexecutor.Request, model string) *registry.NeoPiModel {
			return kimiDiscoveredModel(req, model)
		},
		bearer:  kimiAccessToken,
		headers: func() http.Header { return kimiauth.CommonHeaders(kimiAuthDir(cfg)) },
	}
	return &KimiExecutor{ClaudeExecutor: messages, cfg: cfg, counter: NewOpenAICompatExecutor(kimiauth.Type, cfg)}
}

// kimiDiscoveredModel is the NeoPi record discovery registered for the
// account the request was routed to; without one (Home-resolved metadata
// carries none) it is NeoPi's bundled record, which NeoPi serves until
// discovery answers.
func kimiDiscoveredModel(req cliproxyexecutor.Request, model string) *registry.NeoPiModel {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil {
		return info.NeoPi
	}
	return registry.KimiBundledNeoPiModel(model)
}

func kimiAuthDir(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	dir, err := util.ResolveAuthDir(cfg.AuthDir)
	if err != nil {
		return cfg.AuthDir
	}
	return dir
}

func kimiAccessToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if token, _ := auth.Metadata["access_token"].(string); strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token)
	}
	if storage, ok := auth.Storage.(*kimiauth.KimiTokenStorage); ok && storage != nil {
		return strings.TrimSpace(storage.AccessToken)
	}
	return ""
}

func kimiRefreshToken(auth *cliproxyauth.Auth) string {
	if token, _ := auth.Metadata["refresh_token"].(string); strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token)
	}
	if storage, ok := auth.Storage.(*kimiauth.KimiTokenStorage); ok && storage != nil {
		return strings.TrimSpace(storage.RefreshToken)
	}
	return ""
}

// UsesConfig reports whether the executor was built for cfg.
func (e *KimiExecutor) UsesConfig(cfg *config.Config) bool { return e != nil && e.cfg == cfg }

// SupportsNativeToolSearch keeps Kimi out of Anthropic's native tool search.
func (e *KimiExecutor) SupportsNativeToolSearch(*cliproxyauth.Auth, string) bool { return false }

// Refresh runs NeoPi's kimi-code refresh-token grant.
func (e *KimiExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("kimi executor: auth is nil")
	}
	refreshToken := kimiRefreshToken(auth)
	if refreshToken == "" {
		return auth, nil
	}
	credential, err := kimiauth.NewAuth(e.cfg, auth.ProxyURL).Refresh(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	next := auth.Clone()
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	storage := kimiauth.NewTokenStorage(credential)
	next.Metadata["access_token"] = storage.AccessToken
	next.Metadata["refresh_token"] = storage.RefreshToken
	next.Metadata["expired"] = storage.Expired
	next.Metadata["type"] = kimiauth.Type
	if storage.AccountID != "" {
		next.Metadata["account_id"] = storage.AccountID
	}
	if previous, ok := auth.Storage.(*kimiauth.KimiTokenStorage); ok && previous != nil {
		storage.Metadata = previous.Metadata
		next.Storage = storage
	}
	next.LastRefreshedAt = time.Now()
	return next, nil
}

// HttpRequest sends an arbitrary request with the account's bearer and the
// Kimi CLI identity headers.
func (e *KimiExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("kimi executor: request is nil")
	}
	request = request.Clone(ctx)
	for name, values := range kimiauth.CommonHeaders(kimiAuthDir(e.cfg)) {
		request.Header[name] = values
	}
	if token := kimiAccessToken(auth); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(request, auth.Attributes)
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}

// FetchKimiModels lists the account's models as NeoPi's kimi-code discovery
// does: GET /coding/v1/models with the Kimi CLI platform headers, each row
// resolved into its NeoPi record.
func FetchKimiModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	token := kimiAccessToken(auth)
	if token == "" {
		return nil, fmt.Errorf("kimi models: credential has no access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kimiauth.ModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "KimiCLI/1.0")
	req.Header.Set("X-Msh-Platform", "kimi_cli")
	resp, err := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0).Do(req)
	if err != nil {
		return nil, fmt.Errorf("kimi models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kimi models: %s returned HTTP %d", kimiauth.ModelsURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("kimi models: read %s: %w", kimiauth.ModelsURL, err)
	}
	models := registry.KimiModels(body)
	if len(models) == 0 {
		return nil, fmt.Errorf("kimi models: %s listed no models", kimiauth.ModelsURL)
	}
	return models, nil
}

// CountTokens estimates locally from the Chat Completions form.
func (e *KimiExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.counter.CountTokens(ctx, auth, req, opts)
}
