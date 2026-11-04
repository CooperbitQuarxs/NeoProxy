// Package cursor implements Cursor's CLI sign-in (browser login polled by a
// PKCE verifier), access-token renewal, and the account-scoped model list.
package cursor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	// APIBaseURL hosts Cursor's Connect RPCs and its token endpoint.
	APIBaseURL = "https://api2.cursor.sh"
	// ClientVersion is the released Cursor agent CLI build whose protocol
	// surface NeoProxy mirrors; the service gates protocol features on it.
	ClientVersion = "cli-2026.09.02-c22c1a3"

	loginURL   = "https://cursor.com/loginDeepControl"
	pollPath   = "/auth/poll"
	tokenPath  = "/oauth/token"
	profileURL = "https://cursor.com/api/auth/me"
	// clientID is the OAuth client the Cursor IDE renews its login session with.
	clientID = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"

	// RunPath is the bidirectional agent stream.
	RunPath = "/agent.v1.AgentService/Run"
	// GetUsableModelsPath lists the model slugs the account can run.
	GetUsableModelsPath = "/agent.v1.AgentService/GetUsableModels"

	pollBaseDelay  = time.Second
	pollMaxDelay   = 10 * time.Second
	pollBackoff    = 1.2
	pollMaxTries   = 150
	httpTimeout    = 30 * time.Second
	refreshLead    = 5 * time.Minute
	defaultTokenTT = time.Hour
)

// MaxPollDuration bounds how long a sign-in waits for the browser step.
const MaxPollDuration = 20 * time.Minute

// RefreshLead is how long before expiry an access token is renewed.
func RefreshLead() time.Duration { return refreshLead }

// ClientHeaders are the headers Cursor's authenticated CLI RPCs carry.
func ClientHeaders(h http.Header, accessToken, contentType string) {
	h.Set("Content-Type", contentType)
	h.Set("Authorization", "Bearer "+accessToken)
	h.Set("X-Cursor-Client-Type", "cli")
	h.Set("X-Cursor-Client-Version", ClientVersion)
	h.Set("X-Ghost-Mode", "true")
	h.Set("Connect-Protocol-Version", "1")
	h.Set("X-Request-Id", uuid.NewString())
}

// Auth performs Cursor sign-in, renewal and account lookups.
type Auth struct {
	httpClient *http.Client
	baseURL    string
}

// NewAuth creates a Cursor auth helper using the config proxy settings.
func NewAuth(cfg *config.Config) *Auth { return NewAuthWithProxyURL(cfg, "") }

// NewAuthWithProxyURL creates a Cursor auth helper with an explicit proxy URL.
func NewAuthWithProxyURL(cfg *config.Config, proxyURL string) *Auth {
	effective := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effective == "" {
			effective = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effective
	return &Auth{httpClient: util.SetProxy(&sdkCfg, &http.Client{Timeout: httpTimeout}), baseURL: APIBaseURL}
}

// LoginSession is one started browser sign-in.
type LoginSession struct {
	UUID     string
	Verifier string
	URL      string
}

// StartLogin creates the PKCE pair and the cursor.com page the person opens.
func StartLogin() (*LoginSession, error) {
	raw := make([]byte, 96)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("cursor: generate verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	id := uuid.NewString()
	q := url.Values{"challenge": {challenge}, "uuid": {id}, "mode": {"login"}, "redirectTarget": {"cli"}}
	return &LoginSession{UUID: id, Verifier: verifier, URL: loginURL + "?" + q.Encode()}, nil
}

// Tokens is a Cursor session.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	Expires      time.Time
}

// WaitForLogin polls until the person finishes the browser step, the context
// ends, or the poll budget runs out. 404 means "not yet".
func (a *Auth) WaitForLogin(ctx context.Context, s *LoginSession) (*Tokens, error) {
	if s == nil {
		return nil, errors.New("cursor: login session is nil")
	}
	delay := pollBaseDelay
	consecutiveErrors := 0
	endpoint := a.baseURL + pollPath + "?" + url.Values{"uuid": {s.UUID}, "verifier": {s.Verifier}}.Encode()
	for attempt := 0; attempt < pollMaxTries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		resp, err := a.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if consecutiveErrors++; consecutiveErrors >= 3 {
				return nil, fmt.Errorf("cursor: polling failed: %w", err)
			}
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			consecutiveErrors = 0
			delay = min(time.Duration(float64(delay)*pollBackoff), pollMaxDelay)
			continue
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			var out struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
			}
			if err = json.Unmarshal(body, &out); err != nil || strings.TrimSpace(out.AccessToken) == "" {
				return nil, errors.New("cursor: sign-in returned no access token")
			}
			return &Tokens{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, Expires: TokenExpiry(out.AccessToken)}, nil
		default:
			if consecutiveErrors++; consecutiveErrors >= 3 {
				return nil, fmt.Errorf("cursor: polling failed with status %d", resp.StatusCode)
			}
		}
	}
	return nil, errors.New("cursor: sign-in timed out")
}

// ErrSessionEnded means Cursor will not renew this session; sign in again.
var ErrSessionEnded = errors.New("cursor: Cursor ended this session; sign in again")

// Refresh renews the access token. Cursor answers only an access token, so the
// stored refresh token is kept unless a rotated one is returned.
func (a *Auth) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, errors.New("cursor: missing refresh token")
	}
	payload, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "client_id": clientID, "refresh_token": refreshToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+tokenPath, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cursor: token refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cursor: token refresh failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ShouldLogout bool   `json:"shouldLogout"`
	}
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("cursor: decode token refresh: %w", err)
	}
	// Cursor answers a session it will not renew with 200, an empty token and shouldLogout.
	if out.ShouldLogout {
		return nil, ErrSessionEnded
	}
	if out.AccessToken == "" {
		return nil, errors.New("cursor: token refresh returned no access token")
	}
	rotated := out.RefreshToken
	if rotated == "" {
		rotated = refreshToken
	}
	return &Tokens{AccessToken: out.AccessToken, RefreshToken: rotated, Expires: TokenExpiry(out.AccessToken)}, nil
}

// FetchEmail reads the email of the account that owns accessToken from its
// cursor.com profile. Empty when the token names no user or the profile
// belongs to someone else.
func (a *Auth) FetchEmail(ctx context.Context, accessToken string) (string, error) {
	userID := UserID(accessToken)
	if userID == "" {
		return "", nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", "WorkosCursorSessionToken="+url.QueryEscape(userID+"::"+accessToken))
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("cursor: profile request failed with status %d", resp.StatusCode)
	}
	var profile struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&profile); err != nil {
		return "", err
	}
	if profile.Sub != userID {
		return "", nil
	}
	return strings.TrimSpace(profile.Email), nil
}

// UsableModel is one model slug the account can run.
type UsableModel struct {
	ID          string
	DisplayName string
	MaxMode     bool
}

// FetchUsableModels calls GetUsableModels, the exact wire slugs the account
// can run.
func (a *Auth) FetchUsableModels(ctx context.Context, accessToken string) ([]UsableModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+GetUsableModelsPath, http.NoBody)
	if err != nil {
		return nil, err
	}
	ClientHeaders(req.Header, accessToken, "application/proto")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cursor: GetUsableModels failed with status %d", resp.StatusCode)
	}
	return DecodeUsableModels(body)
}

// TokenExpiry is the access token's JWT expiry, or an hour from now when the
// token carries none.
func TokenExpiry(token string) time.Time {
	if claims := jwtClaims(token); claims != nil {
		if exp, ok := claims["exp"].(float64); ok && exp > 0 {
			return time.Unix(int64(exp), 0)
		}
	}
	return time.Now().Add(defaultTokenTT)
}

// UserID is the Cursor user id in the access token's subject ("auth0|user_…").
func UserID(token string) string {
	claims := jwtClaims(token)
	sub, _ := claims["sub"].(string)
	if i := strings.LastIndex(sub, "|"); i >= 0 {
		sub = sub[i+1:]
	}
	return strings.TrimSpace(sub)
}

func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

var fileSegmentUnsafe = regexp.MustCompile(`[^a-zA-Z0-9@._-]+`)

// CredentialFileName names the auth file for a Cursor account.
func CredentialFileName(email, userID string) string {
	for _, v := range []string{email, userID} {
		if s := strings.Trim(fileSegmentUnsafe.ReplaceAllString(strings.TrimSpace(v), "_"), "_"); s != "" {
			return "cursor-" + s + ".json"
		}
	}
	return fmt.Sprintf("cursor-%d.json", time.Now().UnixMilli())
}

// Metadata is the auth-file content for a signed-in Cursor session.
func Metadata(t *Tokens, email string) map[string]any {
	meta := map[string]any{
		"type":          "cursor",
		"access_token":  t.AccessToken,
		"refresh_token": t.RefreshToken,
		"expired":       t.Expires.UTC().Format(time.RFC3339),
		"last_refresh":  time.Now().UTC().Format(time.RFC3339),
		"base_url":      APIBaseURL,
		"auth_kind":     "oauth",
	}
	if id := UserID(t.AccessToken); id != "" {
		meta["user_id"] = id
	}
	if email != "" {
		meta["email"] = email
	}
	return meta
}
