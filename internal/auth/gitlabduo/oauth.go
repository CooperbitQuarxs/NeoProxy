// Package gitlabduo is NeoPi's GitLab Duo (non-agentic) sign-in: the
// `auth "gitlab-duo"` rule's authorization-code + PKCE login against
// gitlab.com, its refresh grant, and the record a sign-in or a personal
// access token is stored as.
package gitlabduo

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
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// Provider and AgentProvider are NeoPi's provider ids for GitLab Duo and
// GitLab Duo Agent.
const (
	Provider      = "gitlab-duo"
	AgentProvider = "gitlab-duo-agent"
)

const (
	// DefaultClientID is the bundled GitLab OAuth application; GITLAB_CLIENT_ID
	// replaces it with the user's own (#2424).
	DefaultClientID = "da4edff2e6ebd2bc3208611e2768bc1c1dd7be791dc5ff26ca34ca9ee44f7d4b"
	// AgentClientID is GitLab's official VS Code OAuth application, which the
	// agent rule signs in with; it has no override.
	AgentClientID = "36f2a70cddeb5a0889d4fd8295c241b7e9848e89cf9e599d0eed2d8e5350fbf5"
	// AgentRedirectURI is that application's registered redirect: VS Code's
	// handler, which the user copies back (callback manual-only).
	AgentRedirectURI = "vscode://gitlab.gitlab-workflow/authentication"
	// AuthorizeURL and TokenURL are the rules' gitlab.com endpoints.
	AuthorizeURL = "https://gitlab.com/oauth/authorize"
	TokenURL     = "https://gitlab.com/oauth/token"
	// CallbackHost, CallbackPort and CallbackPath are the rule's callback; a
	// taken port falls back to a free one unless GITLAB_REDIRECT_URI pins it.
	CallbackHost = "localhost"
	CallbackPort = 8080
	CallbackPath = "/callback"
	// Scope is the rules' only scope.
	Scope = "api"
	// userAgent is the runtime NeoPi's token requests go out under.
	userAgent = "Bun/1.3.14"
	// expirySkew is the credential's `expires "seconds" skew-ms=300000`.
	expirySkew = 5 * time.Minute
)

// now is the clock token lifetimes count from; tests replace it.
var now = time.Now

// Rule is one of NeoPi's GitLab oauth-code rules: the provider it signs in,
// its client, and for a manual-only rule the fixed redirect the user pastes
// back, which its refresh also sends.
type Rule struct {
	Provider    string
	clientID    func() string
	redirectURI string
}

var (
	// Duo is the `gitlab-duo` rule: the bundled client (or GITLAB_CLIENT_ID)
	// returning to a loopback listener.
	Duo = Rule{Provider: Provider, clientID: duoClientID}
	// Agent is the `gitlab-duo-agent` rule: GitLab's VS Code client,
	// manual-only, with redirect_uri on refresh.
	Agent = Rule{Provider: AgentProvider, clientID: func() string { return AgentClientID }, redirectURI: AgentRedirectURI}
)

// ClientID is the rule's client-id.
func (r Rule) ClientID() string { return r.clientID() }

// RedirectURI is a manual-only rule's fixed redirect, empty for a rule whose
// listener decides it.
func (r Rule) RedirectURI() string { return r.redirectURI }

// duoClientID is the gitlab-duo client-id: GITLAB_CLIENT_ID when set, else
// the bundled application.
func duoClientID() string {
	if value := strings.TrimSpace(os.Getenv("GITLAB_CLIENT_ID")); value != "" {
		return value
	}
	return DefaultClientID
}

// Callback is where the browser returns: the address to listen on, whether
// another port may serve when it is taken, and the redirect URI to advertise
// (empty while the listener's port decides it).
type Callback struct {
	Host         string
	Port         int
	Path         string
	RedirectURI  string
	PortFallback bool
}

// ResolveCallback is resolveCallbackOptions for the rule. GITLAB_REDIRECT_URI
// is advertised verbatim with no port fallback; a loopback override also
// binds the listener to its host, port and path, and any other host listens
// on a free port of the rule's host.
func ResolveCallback() (Callback, error) {
	base := Callback{Host: CallbackHost, Port: CallbackPort, Path: CallbackPath, PortFallback: true}
	override := strings.TrimSpace(os.Getenv("GITLAB_REDIRECT_URI"))
	if override == "" {
		return base, nil
	}
	parsed, err := url.Parse(override)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return Callback{}, fmt.Errorf("Invalid redirect URI override: %s", override)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Callback{}, fmt.Errorf("Redirect URI override must use http:// or https://, got: %s", override)
	}
	hostname := parsed.Hostname()
	loopback := hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1"
	if loopback && parsed.Scheme != "http" {
		return Callback{}, fmt.Errorf("Loopback redirect URI overrides must use http://, got: %s", override)
	}
	port := 80
	if parsed.Scheme == "https" {
		port = 443
	}
	if parsed.Port() != "" {
		if port, err = strconv.Atoi(parsed.Port()); err != nil {
			return Callback{}, fmt.Errorf("Invalid redirect URI override: %s", override)
		}
	}
	// URL.pathname: a URL without a path returns to "/".
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	callback := Callback{Host: CallbackHost, Port: 0, Path: path, RedirectURI: override}
	if loopback {
		callback.Host, callback.Port = hostname, port
	}
	return callback, nil
}

// Listen binds the callback. localhost binds both loopback families on one
// port, as NeoPi does, so a browser resolving ::1 first still reaches it. A
// port another process holds on ::1 is in use like one held on 127.0.0.1; a
// host without IPv6 serves on IPv4 alone. The returned redirect URI is the
// one to advertise.
func (c Callback) Listen() ([]net.Listener, string, error) {
	listen := func(port int) ([]net.Listener, error) {
		if c.Host != "localhost" {
			listener, err := net.Listen("tcp", net.JoinHostPort(c.Host, strconv.Itoa(port)))
			if err != nil {
				return nil, err
			}
			return []net.Listener{listener}, nil
		}
		for attempt := 0; ; attempt++ {
			primary, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				return nil, err
			}
			bound := primary.Addr().(*net.TCPAddr).Port
			companion, errSix := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(bound)))
			if errSix == nil {
				return []net.Listener{primary, companion}, nil
			}
			if !addressInUse(errSix) {
				return []net.Listener{primary}, nil
			}
			_ = primary.Close()
			// A chosen port has no alternative; a free one is drawn again.
			if port != 0 || attempt >= ipv6CompanionAttempts {
				return nil, errSix
			}
		}
	}
	listeners, err := listen(c.Port)
	if err != nil {
		if c.RedirectURI != "" {
			return nil, "", fmt.Errorf("OAuth callback port %d is in use, but oauth.redirectUri (%s) requires this exact port. Free port %d (e.g. stop the process bound to it) and retry, or change oauth.redirectUri to point at an available port.", c.Port, c.RedirectURI, c.Port)
		}
		if !c.PortFallback {
			return nil, "", fmt.Errorf("OAuth callback port %d is in use", c.Port)
		}
		if listeners, err = listen(0); err != nil {
			return nil, "", err
		}
	}
	if c.RedirectURI != "" {
		return listeners, c.RedirectURI, nil
	}
	port := listeners[0].Addr().(*net.TCPAddr).Port
	return listeners, fmt.Sprintf("http://%s:%d%s", c.Host, port, c.Path), nil
}

// ipv6CompanionAttempts is IPV6_COMPANION_ATTEMPTS: how often a free port
// whose ::1 twin is taken is drawn again.
const ipv6CompanionAttempts = 4

// addressInUse reports a bind that failed because another socket holds the
// address (EADDRINUSE, or WSAEADDRINUSE on Windows).
func addressInUse(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == syscall.EADDRINUSE || errno == 10048)
}

// Session is one sign-in (DeclarativeOAuthCodeFlow): the rule's provider and
// client, CSRF state, PKCE pair and the redirect the browser returns to.
type Session struct {
	Provider    string
	ClientID    string
	State       string
	Verifier    string
	Challenge   string
	RedirectURI string
}

// NewSession starts a sign-in under the rule that returns to redirectURI.
func (r Rule) NewSession(redirectURI string) (*Session, error) {
	state := make([]byte, 16)
	verifier := make([]byte, 96)
	if _, err := rand.Read(state); err != nil {
		return nil, err
	}
	if _, err := rand.Read(verifier); err != nil {
		return nil, err
	}
	return r.newSession(redirectURI, hex.EncodeToString(state), verifier), nil
}

func (r Rule) newSession(redirectURI, state string, verifier []byte) *Session {
	session := &Session{Provider: r.Provider, ClientID: r.ClientID(), State: state, Verifier: base64.RawURLEncoding.EncodeToString(verifier), RedirectURI: redirectURI}
	digest := sha256.Sum256([]byte(session.Verifier))
	session.Challenge = base64.RawURLEncoding.EncodeToString(digest[:])
	return session
}

// AuthURL is generateAuthUrl: the standard authorize parameters in NeoPi's
// order.
func (s *Session) AuthURL() string {
	return AuthorizeURL + "?" + formEncode([][2]string{
		{"client_id", s.ClientID},
		{"response_type", "code"},
		{"redirect_uri", s.RedirectURI},
		{"scope", Scope},
		{"code_challenge", s.Challenge},
		{"code_challenge_method", "S256"},
		{"state", s.State},
	})
}

// Credential is NeoPi's projected GitLab credential.
type Credential struct {
	Access  string
	Refresh string
	Expires time.Time
}

// Exchange is exchangeToken: the code (without a `#state` fragment GitLab
// may echo) and the PKCE verifier for the account's tokens.
func (s *Session) Exchange(ctx context.Context, client *http.Client, code string) (Credential, error) {
	if cut := strings.IndexByte(code, '#'); cut >= 0 {
		code = code[:cut]
	}
	return tokenRequest(ctx, client, s.Provider, "token exchange", [][2]string{
		{"grant_type", "authorization_code"},
		{"client_id", s.ClientID},
		{"code", code},
		{"redirect_uri", s.RedirectURI},
		{"code_verifier", s.Verifier},
	}, "")
}

// Refresh is the rule's refresh grant, with a manual-only rule's redirect
// (`params { redirect_uri "{redirect_uri}" }`). A response without a refresh
// token keeps the current one.
func (r Rule) Refresh(ctx context.Context, client *http.Client, refreshToken string) (Credential, error) {
	form := [][2]string{
		{"grant_type", "refresh_token"},
		{"client_id", r.ClientID()},
		{"refresh_token", refreshToken},
	}
	if r.redirectURI != "" {
		form = append(form, [2]string{"redirect_uri", r.redirectURI})
	}
	return tokenRequest(ctx, client, r.Provider, "token refresh", form, refreshToken)
}

// tokenRequest is postTokenRequest and mapCredentials: a form POST to the
// token endpoint, its failure as NeoPi reports it under the provider's id,
// and the credential the rule projects.
func tokenRequest(ctx context.Context, client *http.Client, provider, operation string, form [][2]string, previousRefresh string) (Credential, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, strings.NewReader(formEncode(form)))
	if err != nil {
		return Credential{}, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return Credential{}, fmt.Errorf("%s request to %s failed: %w", provider, TokenURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	// The request advertises encodings, so Go leaves decoding to us; NeoPi's
	// fetch decodes them before reading the text.
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return Credential{}, fmt.Errorf("%s request to %s failed: %w", provider, TokenURL, err)
	}
	defer func() { _ = decoded.Close() }()
	data, err := io.ReadAll(io.LimitReader(decoded, 1<<20))
	if err != nil {
		return Credential{}, fmt.Errorf("%s request to %s failed: %w", provider, TokenURL, err)
	}
	text := string(data)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Credential{}, fmt.Errorf("%s %s failed: %d %s", provider, operation, resp.StatusCode, firstUnits(text, 500))
	}
	var body map[string]any
	if json.Unmarshal(data, &body) != nil {
		body = nil
	}
	access, _ := body["access_token"].(string)
	if access == "" {
		// The body as JSON.stringify(JSON.parse(text)) spells it.
		shown := text
		if body != nil {
			shown = string(jsjson.Normalize(data))
		}
		return Credential{}, fmt.Errorf("%s token response missing access token: %s", provider, firstUnits(shown, 500))
	}
	seconds, isNumber := body["expires_in"].(float64)
	if !isNumber || math.IsInf(seconds, 0) || math.IsNaN(seconds) {
		return Credential{}, fmt.Errorf("%s token response missing expires_in", provider)
	}
	baseMs := float64(now().UnixMilli())
	if created, ok := body["created_at"].(float64); ok && !math.IsInf(created, 0) && !math.IsNaN(created) {
		baseMs = created * 1000
	}
	credential := Credential{Access: access, Expires: time.UnixMilli(int64(baseMs + seconds*1000 - float64(expirySkew.Milliseconds())))}
	credential.Refresh, _ = body["refresh_token"].(string)
	if credential.Refresh == "" {
		credential.Refresh = previousRefresh
	}
	return credential, nil
}

// ParseCallbackInput is parseCallbackInput: what a user pastes back from a
// manual-only sign-in — the redirect URL, its query alone, or the code with
// an optional `#state` — read as code and state.
func ParseCallbackInput(input string) (code, state string) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		return query.Get("code"), query.Get("state")
	}
	if strings.Contains(value, "code=") {
		query, _ := url.ParseQuery(strings.TrimLeft(value[:1], "?#") + value[1:])
		return query.Get("code"), query.Get("state")
	}
	code, state, _ = strings.Cut(value, "#")
	return code, state
}

// firstUnits is text.slice(0, n) over UTF-16 code units, as NeoPi cuts.
func firstUnits(text string, n int) string {
	units := 0
	for index, r := range text {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > n {
			return text[:index]
		}
		units += width
	}
	return text
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
