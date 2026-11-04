package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/nous"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// NousExecutor serves Nous Portal sign-ins. Inference is plain OpenAI chat
// completions, so it delegates to the OpenAI-compatible executor with the
// sign-in's current access token; it adds only the portal's token refresh.
type NousExecutor struct {
	cfg    *config.Config
	compat *OpenAICompatExecutor
}

// NewNousExecutor builds the executor for the nous-portal provider.
func NewNousExecutor(cfg *config.Config) *NousExecutor {
	return &NousExecutor{cfg: cfg, compat: NewOpenAICompatExecutor(nous.Provider, cfg)}
}

func (e *NousExecutor) Identifier() string { return nous.Provider }

// view projects the OAuth access token and fixed endpoint onto the attributes
// the OpenAI-compatible executor reads. It is rebuilt on every call so a
// refreshed token is used immediately and never cached in attributes.
func (e *NousExecutor) view(auth *cliproxyauth.Auth) *cliproxyauth.Auth {
	if auth == nil {
		return nil
	}
	out := auth.Clone()
	attrs := make(map[string]string, len(auth.Attributes)+2)
	for k, v := range auth.Attributes {
		attrs[k] = v
	}
	token, _ := auth.Metadata["access_token"].(string)
	attrs["api_key"] = strings.TrimSpace(token)
	attrs["base_url"] = nous.InferenceURL
	out.Attributes = attrs
	return out
}

func (e *NousExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.compat.Execute(ctx, e.view(auth), req, opts)
}

func (e *NousExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return e.compat.ExecuteStream(ctx, e.view(auth), req, opts)
}

func (e *NousExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.compat.CountTokens(ctx, e.view(auth), req, opts)
}

func (e *NousExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	return e.compat.PrepareRequest(req, e.view(auth))
}

func (e *NousExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	return e.compat.HttpRequest(ctx, e.view(auth), req)
}

// Refresh renews the access token with the portal. Imported sign-ins carry no
// refresh token (NeoPi stays their refresher), so there is nothing to do.
func (e *NousExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("nous executor: auth is nil")
	}
	refreshToken, _ := auth.Metadata["refresh_token"].(string)
	if strings.TrimSpace(refreshToken) == "" {
		return auth, nil
	}
	client := nous.NewClient(e.cfg, auth.ProxyURL)
	if httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 30*time.Second); httpClient != nil {
		client.SetHTTPClient(httpClient)
	}
	token, err := client.Refresh(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	auth.Metadata["access_token"] = token.AccessToken
	auth.Metadata["refresh_token"] = token.RefreshToken
	auth.Metadata["expired"] = token.ExpiresAt.UTC().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	return auth, nil
}
