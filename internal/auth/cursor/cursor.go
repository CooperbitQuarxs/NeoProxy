// Package cursor ports NeoPi's Cursor sign-in and renewal
// (pi-ai/src/registry/oauth/cursor.ts): the browser login polled with a PKCE
// verifier, the access-token renewal, and the account profile lookup.
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

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	loginURL   = "https://cursor.com/loginDeepControl"
	pollURL    = "https://api2.cursor.sh/auth/poll"
	refreshURL = "https://api2.cursor.sh/oauth/token"
	getMeURL   = "https://api2.cursor.sh/aiserver.v1.DashboardService/GetMe"
	// clientID is the OAuth client the Cursor IDE renews its login session with.
	clientID = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"

	profileTimeout = 3 * time.Second
	// refreshTimeout is the deadline NeoPi's refresher runs a renewal under.
	refreshTimeout = 10 * time.Second

	pollMaxAttempts = 150
	pollBaseDelay   = time.Second
	pollMaxDelay    = 10 * time.Second
	pollBackoff     = 1.2

	expirySkew = 5 * time.Minute
)

// MaxPollDuration bounds a sign-in session; NeoPi's 150-attempt budget ends
// first (about 24 minutes of backoff).
const MaxPollDuration = 25 * time.Minute

// RefreshLead is NeoPi's sixty-second refresh skew against the stored expiry,
// which is already the token's exp less five minutes.
const RefreshLead = time.Minute

// ErrSessionEnded is the renewal Cursor refuses with shouldLogout.
var ErrSessionEnded = errors.New("invalid_grant: Cursor ended this session; run /login cursor again")

// LoginSession is one started browser sign-in.
type LoginSession struct {
	UUID     string
	Verifier string
	URL      string
}

// StartLogin is generateCursorAuthParams.
func StartLogin() (*LoginSession, error) {
	raw := make([]byte, 96)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("cursor: generate verifier: %w", err)
	}
	return newLoginSession(raw, newUUID()), nil
}

func newLoginSession(verifierBytes []byte, id string) *LoginSession {
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	query := formEncode([2]string{"challenge", challenge}, [2]string{"uuid", id}, [2]string{"mode", "login"}, [2]string{"redirectTarget", "cli"})
	return &LoginSession{UUID: id, Verifier: verifier, URL: loginURL + "?" + query}
}

// formEncode is URLSearchParams serialization: insertion order and the
// application/x-www-form-urlencoded percent-encode set.
func formEncode(fields ...[2]string) string {
	var out strings.Builder
	for i, field := range fields {
		if i > 0 {
			out.WriteByte('&')
		}
		for j, value := range field {
			if j > 0 {
				out.WriteByte('=')
			}
			value = strings.ReplaceAll(url.QueryEscape(value), "~", "%7E")
			out.WriteString(strings.ReplaceAll(value, "%2A", "*"))
		}
	}
	return out.String()
}

// Credential is NeoPi's OAuthCredentials for Cursor.
type Credential struct {
	Access  string
	Refresh string
	Expires time.Time
	Email   string
}

// Auth runs Cursor's sign-in, renewal and profile requests.
type Auth struct {
	httpClient *http.Client
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error
	pollURL    string
	refreshURL string
	getMeURL   string
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
	return newAuth(util.SetProxy(&sdkCfg, &http.Client{}))
}

func newAuth(client *http.Client) *Auth {
	return &Auth{httpClient: client, now: time.Now, sleep: sleepContext, pollURL: pollURL, refreshURL: refreshURL, getMeURL: getMeURL}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// fetchHeaders are the headers Bun's fetch adds to NeoPi's requests.
func fetchHeaders(h http.Header) {
	h.Set("Accept", "*/*")
	h.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	h.Set("Connection", "keep-alive")
	h.Set("User-Agent", "Bun/1.3.14")
}

func (a *Auth) do(req *http.Request) (int, []byte, error) {
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	body, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		_ = resp.Body.Close()
		return resp.StatusCode, nil, err
	}
	defer func() {
		if errClose := body.Close(); errClose != nil {
			log.WithError(errClose).Debug("cursor: close response body")
		}
	}()
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	return resp.StatusCode, data, err
}

// Login is loginCursorHook: poll until the browser step finishes, then name
// the account by its profile email.
func (a *Auth) Login(ctx context.Context, s *LoginSession) (*Credential, error) {
	if s == nil {
		return nil, errors.New("cursor: login session is nil")
	}
	access, refresh, err := a.poll(ctx, s.UUID, s.Verifier)
	if err != nil {
		return nil, err
	}
	credential := &Credential{Access: access, Refresh: refresh, Expires: tokenExpiry(access, a.now())}
	credential.Email = a.accountEmail(ctx, access)
	return credential, nil
}

// poll is pollCursorAuth. 404 means "not yet" and backs off; any other
// failure, including an unreadable success body, counts toward three
// consecutive errors at an unchanged delay.
func (a *Auth) poll(ctx context.Context, id, verifier string) (string, string, error) {
	delay := pollBaseDelay
	consecutiveErrors := 0
	endpoint := a.pollURL + "?uuid=" + id + "&verifier=" + verifier
	for attempt := 0; attempt < pollMaxAttempts; attempt++ {
		if err := a.sleep(ctx, delay); err != nil {
			return "", "", err
		}
		access, refresh, pending, err := a.pollOnce(ctx, endpoint)
		switch {
		case ctx.Err() != nil:
			return "", "", ctx.Err()
		case pending:
			consecutiveErrors = 0
			delay = min(time.Duration(float64(delay)*pollBackoff), pollMaxDelay)
		case err == nil:
			return access, refresh, nil
		default:
			if consecutiveErrors++; consecutiveErrors >= 3 {
				return "", "", errors.New("Too many consecutive errors during Cursor auth polling")
			}
		}
	}
	return "", "", errors.New("Cursor authentication polling timeout")
}

func (a *Auth) pollOnce(ctx context.Context, endpoint string) (access, refresh string, pending bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", false, err
	}
	fetchHeaders(req.Header)
	status, body, err := a.do(req)
	if err != nil {
		return "", "", false, err
	}
	if status == http.StatusNotFound {
		return "", "", true, nil
	}
	if status < 200 || status >= 300 {
		return "", "", false, fmt.Errorf("Poll failed: %d", status)
	}
	var out struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err = json.Unmarshal(body, &out); err != nil {
		return "", "", false, err
	}
	if out.AccessToken == "" {
		return "", "", false, errors.New("cursor: sign-in returned no access token")
	}
	return out.AccessToken, out.RefreshToken, false, nil
}

// Refresh is refreshCursorHook: renew the access token, keeping the stored
// refresh token unless Cursor rotates it, and look up the email only when
// none is stored.
func (a *Auth) Refresh(ctx context.Context, stored Credential) (*Credential, error) {
	refreshed, err := a.refreshToken(ctx, stored.Refresh)
	if err != nil {
		return nil, err
	}
	if stored.Email == "" {
		refreshed.Email = a.accountEmail(ctx, refreshed.Access)
	}
	return refreshed, nil
}

// refreshToken is refreshCursorToken.
func (a *Auth) refreshToken(ctx context.Context, refreshToken string) (*Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	body := jsjson.Object(
		jsjson.Field{Name: "grant_type", Value: jsjson.Marshal("refresh_token")},
		jsjson.Field{Name: "client_id", Value: jsjson.Marshal(clientID)},
		jsjson.Field{Name: "refresh_token", Value: jsjson.Marshal(refreshToken)},
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.refreshURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	fetchHeaders(req.Header)
	req.Header.Set("Content-Type", "application/json")
	status, data, err := a.do(req)
	if err != nil {
		return nil, fmt.Errorf("cursor: token refresh request failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("Cursor token refresh failed: %s", data)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ShouldLogout bool   `json:"shouldLogout"`
	}
	if err = json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("cursor: decode token refresh: %w", err)
	}
	// Cursor answers a session it will not renew with 200, an empty token and shouldLogout.
	if out.ShouldLogout {
		return nil, ErrSessionEnded
	}
	if out.AccessToken == "" {
		return nil, errors.New("Cursor token refresh returned no access token")
	}
	rotated := out.RefreshToken
	if rotated == "" {
		rotated = refreshToken
	}
	return &Credential{Access: out.AccessToken, Refresh: rotated, Expires: tokenExpiry(out.AccessToken, a.now())}, nil
}

// accountEmail is withCursorAccountEmail: a failed or foreign profile lookup
// leaves the credential unnamed.
func (a *Auth) accountEmail(ctx context.Context, accessToken string) string {
	ctx, cancel := context.WithTimeout(ctx, profileTimeout)
	defer cancel()
	email, _ := a.FetchEmail(ctx, accessToken)
	return email
}

// FetchEmail is fetchCursorAccountEmail: the email of the account that owns
// accessToken, read from DashboardService/GetMe. Empty when the token names
// no subject or the profile names another.
func (a *Auth) FetchEmail(ctx context.Context, accessToken string) (string, error) {
	subject := Subject(accessToken)
	if subject == "" {
		return "", nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.getMeURL, strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	SetConnectJSONHeaders(req.Header, accessToken)
	status, data, err := a.do(req)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("Cursor profile request failed: %d", status)
	}
	var profile map[string]any
	if err = json.Unmarshal(data, &profile); err != nil {
		return "", err
	}
	authID, _ := profile["authId"].(string)
	email, ok := profile["email"].(string)
	if authID != subject || !ok {
		return "", nil
	}
	return strings.TrimSpace(email), nil
}

// SetConnectJSONHeaders is cursorConnectRequest's headers: a unary Connect
// call with a JSON body, authenticated by accessToken.
func SetConnectJSONHeaders(h http.Header, accessToken string) {
	fetchHeaders(h)
	h.Set("Accept", "application/json")
	h.Set("Authorization", "Bearer "+accessToken)
	h.Set("Connect-Protocol-Version", "1")
	h.Set("Content-Type", "application/json")
}

// SetSessionHeaders is cursorSessionHeaders: accessToken presented to
// cursor.com as its user's web session.
func SetSessionHeaders(h http.Header, userID, accessToken string) {
	fetchHeaders(h)
	h.Set("Accept", "application/json")
	h.Set("Cookie", "WorkosCursorSessionToken="+encodeURIComponent(userID+"::"+accessToken))
}

// encodeURIComponent escapes everything except A-Z a-z 0-9 - _ . ! ~ * ' ( ).
func encodeURIComponent(s string) string {
	const unreserved = "-_.!~*'()"
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte(unreserved, c) >= 0 {
			out.WriteByte(c)
			continue
		}
		fmt.Fprintf(&out, "%%%02X", c)
	}
	return out.String()
}

// tokenExpiry is getTokenExpiry: the JWT exp less five minutes, else an hour
// from now.
func tokenExpiry(token string, now time.Time) time.Time {
	if exp, ok := jwtClaims(token)["exp"].(float64); ok {
		return time.UnixMilli(int64(exp*1000) - expirySkew.Milliseconds())
	}
	return now.Add(time.Hour)
}

// Subject is cursorAccessTokenSubject: the token's full sub claim, such as
// "github|1245379" or "auth0|user_…".
func Subject(token string) string {
	sub, _ := jwtClaims(token)["sub"].(string)
	return strings.TrimSpace(sub)
}

// UserID is extractCursorAccessTokenUserId: the sub claim's second
// "|"-separated part, or the whole subject.
func UserID(token string) string {
	sub := Subject(token)
	if sub == "" {
		return ""
	}
	parts := strings.Split(sub, "|")
	if len(parts) > 1 {
		return strings.TrimSpace(parts[1])
	}
	return sub
}

func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil
	}
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(strings.NewReplacer("-", "+", "_", "/").Replace(parts[1]), "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
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

// Metadata is the auth-file content for a signed-in Cursor session; expired
// is NeoPi's stored expiry.
func Metadata(c *Credential) map[string]any {
	meta := map[string]any{
		"type":          "cursor",
		"access_token":  c.Access,
		"refresh_token": c.Refresh,
		"expired":       c.Expires.UTC().Format(time.RFC3339),
		"last_refresh":  time.Now().UTC().Format(time.RFC3339),
		"base_url":      cursorwire.DefaultBaseURL,
		"auth_kind":     "oauth",
	}
	if id := UserID(c.Access); id != "" {
		meta["user_id"] = id
	}
	if c.Email != "" {
		meta["email"] = c.Email
	}
	return meta
}
