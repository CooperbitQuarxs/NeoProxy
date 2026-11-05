package snowflake

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ClientID is SNOWFLAKE_OAUTH_CLIENT_ID: Snowflake's built-in local-application
// OAuth client.
const ClientID = "LOCAL_APPLICATION"

// CallbackPort is the auth policy's callback-port; NeoPi falls back to a
// random loopback port when it is taken.
const CallbackPort = 54551

// Session is one sign-in (SnowflakeOAuthFlow): the account, CSRF state, PKCE
// pair and the loopback redirect the account's authorization server returns to.
type Session struct {
	AccountURL  string
	State       string
	Verifier    string
	Challenge   string
	RedirectURI string
}

// NewSession starts loginSnowflake for an account identifier or URL, whose
// callback listens on port (CallbackPort unless it was taken).
func NewSession(account string, port int) (*Session, error) {
	accountURL, err := NormalizeAccountURL(account)
	if err != nil {
		return nil, err
	}
	state := make([]byte, 16)
	verifier := make([]byte, 96)
	if _, err = rand.Read(state); err != nil {
		return nil, err
	}
	if _, err = rand.Read(verifier); err != nil {
		return nil, err
	}
	session := &Session{AccountURL: accountURL, State: hex.EncodeToString(state), Verifier: base64.RawURLEncoding.EncodeToString(verifier)}
	digest := sha256.Sum256([]byte(session.Verifier))
	session.Challenge = base64.RawURLEncoding.EncodeToString(digest[:])
	// Some local-app integrations reject callback subpaths, so the callback is "/".
	session.RedirectURI = fmt.Sprintf("http://127.0.0.1:%d/", port)
	return session, nil
}

// AuthURL is generateAuthUrl: the account's authorize endpoint with NeoPi's
// parameters, in NeoPi's order.
func (s *Session) AuthURL() string {
	params := [][2]string{
		{"client_id", ClientID},
		{"response_type", "code"},
		{"redirect_uri", s.RedirectURI},
		{"scope", "refresh_token"},
		{"state", s.State},
		{"code_challenge", s.Challenge},
		{"code_challenge_method", "S256"},
	}
	return s.AccountURL + "/oauth/authorize?" + formEncode(params)
}

// Tokens is a token-request answer: the access token, the refresh token when
// issued, and when the access token expires.
type Tokens struct {
	Access  string
	Refresh string
	Expires time.Time
}

// OAuthError is a failed token request; it carries the HTTP status but never
// the server's answer, which may reflect the authorization code or a token.
type OAuthError struct {
	Message string
	Status  int
}

func (e *OAuthError) Error() string { return e.Message }

// now is the clock token lifetimes count from; tests replace it.
var now = time.Now

// Exchange is exchangeToken: the authorization code and PKCE verifier for the
// account's tokens.
func (s *Session) Exchange(ctx context.Context, client *http.Client, code string) (Tokens, error) {
	return tokenRequest(ctx, client, s.AccountURL, "exchange", [][2]string{
		{"grant_type", "authorization_code"},
		{"code", code},
		{"redirect_uri", s.RedirectURI},
		{"code_verifier", s.Verifier},
		{"client_id", ClientID},
	})
}

// Refresh is refreshSnowflakeToken. Without a refresh token a still-valid
// access token is kept (the shared refresh lead would otherwise discard it)
// and an expired one needs a new sign-in.
func Refresh(ctx context.Context, client *http.Client, account string, current Tokens) (Tokens, error) {
	if strings.TrimSpace(current.Refresh) == "" {
		if now().Before(current.Expires) {
			return current, nil
		}
		return Tokens{}, &OAuthError{Message: "Snowflake did not issue a refresh token; run /login snowflake again or use SNOWFLAKE_ACCOUNT and SNOWFLAKE_PAT"}
	}
	accountURL, err := NormalizeAccountURL(account)
	if err != nil {
		return Tokens{}, &OAuthError{Message: "Invalid Snowflake account; run /login snowflake again"}
	}
	tokens, err := tokenRequest(ctx, client, accountURL, "refresh", [][2]string{
		{"grant_type", "refresh_token"},
		{"refresh_token", current.Refresh},
		{"client_id", ClientID},
	})
	if err != nil {
		return Tokens{}, err
	}
	if tokens.Refresh == "" {
		tokens.Refresh = current.Refresh
	}
	return tokens, nil
}

// errRedirect refuses redirects: NeoPi's token requests use redirect "error".
var errRedirect = errors.New("snowflake token request redirected")

// refuseRedirects fails a redirect answer inside the round trip, so the
// client never parses its Location and the error names only the token
// endpoint: a Location may reflect the authorization code or a token.
type refuseRedirects struct{ base http.RoundTripper }

func (t refuseRedirects) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		_ = resp.Body.Close()
		return nil, errRedirect
	}
	return resp, nil
}

// tokenRequest posts a form to the account's token endpoint and validates the
// answer as parseTokenResponse does.
func tokenRequest(ctx context.Context, client *http.Client, accountURL, operation string, form [][2]string) (Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, accountURL+"/oauth/token-request", strings.NewReader(formEncode(form)))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	noRedirect := *client
	base := noRedirect.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	noRedirect.Transport = refuseRedirects{base: base}
	resp, err := noRedirect.Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return Tokens{}, &OAuthError{Message: fmt.Sprintf("Snowflake token %s failed (%d)", operation, resp.StatusCode), Status: resp.StatusCode}
	}
	invalid := &OAuthError{Message: "Snowflake returned an invalid token response"}
	var body map[string]any
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil || body == nil {
		return Tokens{}, invalid
	}
	access, _ := body["access_token"].(string)
	expiresIn, isNumber := body["expires_in"].(float64)
	if strings.TrimSpace(access) == "" || !isNumber || math.IsInf(expiresIn, 0) || math.IsNaN(expiresIn) || expiresIn <= 0 {
		return Tokens{}, invalid
	}
	refresh := ""
	if value, present := body["refresh_token"]; present {
		text, isString := value.(string)
		if !isString || strings.TrimSpace(text) == "" {
			return Tokens{}, invalid
		}
		refresh = text
	}
	// expires = Date.now() + expires_in * 1000, in milliseconds as NeoPi
	// counts; only a lifetime past what a timestamp holds is refused.
	lifetimeMs := expiresIn * 1000
	nowMs := now().UnixMilli()
	if math.IsInf(lifetimeMs, 0) || lifetimeMs > float64(math.MaxInt64-nowMs) {
		return Tokens{}, &OAuthError{Message: "Snowflake returned an invalid token lifetime"}
	}
	return Tokens{Access: access, Refresh: refresh, Expires: time.UnixMilli(nowMs + int64(lifetimeMs))}, nil
}

// formEncode is URLSearchParams' application/x-www-form-urlencoded
// serialization, keeping the given order.
func formEncode(pairs [][2]string) string {
	parts := make([]string, len(pairs))
	for i, pair := range pairs {
		parts[i] = formEscape(pair[0]) + "=" + formEscape(pair[1])
	}
	return strings.Join(parts, "&")
}

// formEscape escapes as URLSearchParams does: '~' is escaped too, which Go's
// QueryEscape leaves alone.
func formEscape(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "~", "%7E")
}
