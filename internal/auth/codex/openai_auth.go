// Package codex provides authentication and token management for OpenAI's Codex API.
// It handles the OAuth2 flow, including generating authorization URLs, exchanging
// authorization codes for tokens, and refreshing expired tokens. The package also
// defines data structures for storing and managing Codex authentication credentials.
package codex

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"golang.org/x/sync/singleflight"
)

// OAuth configuration constants for OpenAI Codex
const (
	AuthURL             = "https://auth.openai.com/oauth/authorize"
	TokenURL            = "https://auth.openai.com/oauth/token"
	ClientID            = "app_EMoamEEZ73f0CkXaXp7hrann"
	RedirectURI         = "http://localhost:1455/auth/callback"
	codexRefreshTimeout = 15 * time.Second
)

// CodexAuth handles the OpenAI OAuth2 authentication flow.
// It manages the HTTP client and provides methods for generating authorization URLs,
// exchanging authorization codes for tokens, and refreshing access tokens.
type CodexAuth struct {
	httpClient *http.Client
}

var codexRefreshGroup singleflight.Group

// NewCodexAuth creates a new CodexAuth service instance.
// It initializes an HTTP client with proxy settings from the provided configuration.
func NewCodexAuth(cfg *config.Config) *CodexAuth {
	return NewCodexAuthWithProxyURL(cfg, "")
}

// NewCodexAuthWithProxyURL creates a new CodexAuth service instance.
// proxyURL takes precedence over cfg.ProxyURL when non-empty.
func NewCodexAuthWithProxyURL(cfg *config.Config, proxyURL string) *CodexAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &CodexAuth{httpClient: util.SetProxy(&sdkCfg, &http.Client{})}
}

// GenerateAuthURL creates the OAuth authorization URL with PKCE (Proof Key for Code Exchange).
// It constructs the URL with the necessary parameters, including the client ID,
// response type, redirect URI, scopes, and PKCE challenge.
func (o *CodexAuth) GenerateAuthURL(state string, pkceCodes *PKCECodes) (string, error) {
	if pkceCodes == nil {
		return "", fmt.Errorf("PKCE codes are required")
	}

	return AuthURL + "?" + codexForm(
		[2]string{"client_id", ClientID},
		[2]string{"response_type", "code"},
		[2]string{"redirect_uri", RedirectURI},
		[2]string{"scope", "openid profile email offline_access api.connectors.read api.connectors.invoke"},
		[2]string{"code_challenge", pkceCodes.CodeChallenge},
		[2]string{"code_challenge_method", "S256"},
		[2]string{"state", state},
		[2]string{"id_token_add_organizations", "true"},
		[2]string{"codex_cli_simplified_flow", "true"},
		[2]string{"originator", "omp"},
	), nil
}

// ExchangeCodeForTokens exchanges an authorization code for access and refresh tokens.
// It performs an HTTP POST request to the OpenAI token endpoint with the provided
// authorization code and PKCE verifier.
func (o *CodexAuth) ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *PKCECodes) (*CodexAuthBundle, error) {
	if pkceCodes == nil {
		return nil, fmt.Errorf("PKCE codes are required for token exchange")
	}

	// Preserve URLSearchParams insertion order as well as the values.
	if fragment := strings.IndexByte(code, '#'); fragment >= 0 {
		code = code[:fragment]
	}
	fields := [][2]string{
		{"grant_type", "authorization_code"},
		{"client_id", ClientID},
		{"code", code},
		{"redirect_uri", RedirectURI},
		{"code_verifier", pkceCodes.CodeVerifier},
	}
	tokenData, err := o.requestNeoPiToken(ctx, codexForm(fields...), "", false)
	if err != nil {
		return nil, err
	}
	return &CodexAuthBundle{TokenData: *tokenData, LastRefresh: time.Now().Format(time.RFC3339Nano)}, nil
}

// RefreshTokens refreshes an access token using a refresh token.
// This method is called when an access token has expired. It makes a request to the
// token endpoint to obtain a new set of tokens.
func (o *CodexAuth) RefreshTokens(ctx context.Context, refreshToken string) (*CodexTokenData, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := codexRefreshGroup.DoChan(refreshToken, func() (any, error) {
		return o.refreshTokensSingleFlight(ctx, refreshToken)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case refreshed := <-result:
		if refreshed.Err != nil {
			return nil, refreshed.Err
		}
		tokenData, ok := refreshed.Val.(*CodexTokenData)
		if !ok || tokenData == nil {
			return nil, fmt.Errorf("token refresh failed: invalid single-flight result")
		}
		return tokenData, nil
	}
}

func (o *CodexAuth) refreshTokensSingleFlight(ctx context.Context, refreshToken string) (*CodexTokenData, error) {
	return o.requestNeoPiToken(ctx, codexForm(
		[2]string{"grant_type", "refresh_token"},
		[2]string{"client_id", ClientID},
		[2]string{"refresh_token", refreshToken},
	), refreshToken, true)
}

// CreateTokenStorage creates a new CodexTokenStorage from a CodexAuthBundle.
// It populates the storage struct with token data, user information, and timestamps.
func (o *CodexAuth) CreateTokenStorage(bundle *CodexAuthBundle) *CodexTokenStorage {
	if bundle == nil {
		return nil
	}
	storage := &CodexTokenStorage{
		IDToken:      bundle.TokenData.IDToken,
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		AccountID:    bundle.TokenData.AccountID,
		LastRefresh:  bundle.LastRefresh,
		Email:        bundle.TokenData.Email,
		Expire:       bundle.TokenData.Expire,
		PlanType:     bundle.TokenData.PlanType,
	}

	return storage
}

func isNonRetryableRefreshErr(err error) bool {
	if err == nil {
		return false
	}
	raw := strings.ToLower(err.Error())
	return strings.Contains(raw, "refresh_token_reused")
}

// UpdateTokenStorage updates an existing CodexTokenStorage with new token data.
// This is typically called after a successful token refresh to persist the new credentials.
func (o *CodexAuth) UpdateTokenStorage(storage *CodexTokenStorage, tokenData *CodexTokenData) {
	if storage == nil || tokenData == nil {
		return
	}
	if tokenData.IDToken != "" {
		storage.IDToken = tokenData.IDToken
	}
	storage.AccessToken = tokenData.AccessToken
	if tokenData.RefreshToken != "" {
		storage.RefreshToken = tokenData.RefreshToken
	}
	if tokenData.AccountID != "" {
		storage.AccountID = tokenData.AccountID
	}
	if tokenData.Email != "" {
		storage.Email = tokenData.Email
	}
	storage.LastRefresh = time.Now().Format(time.RFC3339Nano)
	storage.Expire = tokenData.Expire
	// NeoPi retains the stored organization name (plan) on refresh.
}
