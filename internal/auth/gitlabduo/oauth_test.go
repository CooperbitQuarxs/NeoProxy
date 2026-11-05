package gitlabduo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// gitlabDuoAuthCapture is testdata/gitlab_duo_auth.json: NeoPi's OWN
// gitlab-duo rule through DeclarativeOAuthCodeFlow and createRefresh, with
// PKCE randomness and the clock pinned (capture_neopi_gitlab_duo_auth.ts).
type gitlabDuoAuthCapture struct {
	Now    int64  `json:"now"`
	State  string `json:"state"`
	Logins []struct {
		Name     string            `json:"name"`
		Env      map[string]string `json:"env"`
		Response struct {
			Status int    `json:"status"`
			Body   string `json:"body"`
		} `json:"response"`
		Callback struct {
			PreferredPort     int    `json:"preferredPort"`
			CallbackPath      string `json:"callbackPath"`
			CallbackHostname  string `json:"callbackHostname"`
			RedirectURI       string `json:"redirectUri"`
			AllowPortFallback bool   `json:"allowPortFallback"`
		} `json:"callback"`
		RedirectURI string `json:"redirectUri"`
		Authorize   struct {
			URL string `json:"url"`
		} `json:"authorize"`
		Requests    []gitlabDuoCapturedRequest `json:"requests"`
		Credentials *gitlabDuoCapturedCred     `json:"credentials"`
		Error       *string                    `json:"error"`
	} `json:"logins"`
	Refreshes []struct {
		Name   string `json:"name"`
		Stored struct {
			Refresh string `json:"refresh"`
		} `json:"stored"`
		Response struct {
			Status int    `json:"status"`
			Body   string `json:"body"`
		} `json:"response"`
		Requests    []gitlabDuoCapturedRequest `json:"requests"`
		Credentials *gitlabDuoCapturedCred     `json:"credentials"`
		Error       *string                    `json:"error"`
	} `json:"refreshes"`
	// Pastes are parseCallbackInput's readings of pasted callback input
	// (gitlab_duo_agent_auth.json only).
	Pastes []struct {
		Input  string `json:"input"`
		Parsed struct {
			Code  string `json:"code"`
			State string `json:"state"`
		} `json:"parsed"`
	} `json:"pastes"`
}

type gitlabDuoCapturedRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type gitlabDuoCapturedCred struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires"`
}

// gitlabDuoTransport answers gitlab.com in process and records each request.
type gitlabDuoTransport struct {
	server *httptest.Server
	seen   []gitlabDuoCapturedRequest
}

func (t *gitlabDuoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "gitlab.com" {
		return nil, &url.Error{Op: req.Method, URL: req.URL.String(), Err: io.ErrUnexpectedEOF}
	}
	body, _ := io.ReadAll(req.Body)
	headers := map[string]string{}
	for name := range req.Header {
		headers[strings.ToLower(name)] = req.Header.Get(name)
	}
	t.seen = append(t.seen, gitlabDuoCapturedRequest{Method: req.Method, URL: req.URL.String(), Headers: headers, Body: string(body)})
	target, _ := url.Parse(t.server.URL + req.URL.Path)
	forward, _ := http.NewRequestWithContext(req.Context(), req.Method, target.String(), strings.NewReader(string(body)))
	return http.DefaultTransport.RoundTrip(forward)
}

func loadGitLabDuoAuthCapture(t *testing.T, name string) gitlabDuoAuthCapture {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var capture gitlabDuoAuthCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

// The capture's pinned randomness: byte i of the first draw is i*7+3.
func gitlabDuoPinnedVerifier() []byte {
	raw := make([]byte, 96)
	for i := range raw {
		raw[i] = byte(i*7 + 3)
	}
	return raw
}

// The wire headers NeoPi's token requests carry beside the framing.
var gitlabDuoTokenHeaders = []string{"accept", "accept-encoding", "connection", "content-type", "user-agent"}

func assertGitLabDuoRequests(t *testing.T, got, want []gitlabDuoCapturedRequest) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sent %d requests, NeoPi sent %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Method != want[i].Method || got[i].URL != want[i].URL || got[i].Body != want[i].Body {
			t.Fatalf("request %d = %s %s %q, NeoPi sent %s %s %q", i, got[i].Method, got[i].URL, got[i].Body, want[i].Method, want[i].URL, want[i].Body)
		}
		for _, name := range gitlabDuoTokenHeaders {
			if got[i].Headers[name] != want[i].Headers[name] {
				t.Fatalf("request %d header %s = %q, NeoPi sent %q", i, name, got[i].Headers[name], want[i].Headers[name])
			}
		}
	}
}

func assertGitLabDuoOutcome(t *testing.T, credential Credential, err error, want *gitlabDuoCapturedCred, wantErr *string) {
	t.Helper()
	if wantErr != nil {
		if err == nil || err.Error() != *wantErr {
			t.Fatalf("error = %v, NeoPi failed with %q", err, *wantErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := gitlabDuoCapturedCred{Access: credential.Access, Refresh: credential.Refresh, Expires: credential.Expires.UnixMilli()}
	if got != *want {
		t.Fatalf("credential = %+v, NeoPi projected %+v", got, *want)
	}
}

// Every captured sign-in: the callback NeoPi resolves (with and without the
// GITLAB_CLIENT_ID / GITLAB_REDIRECT_URI overrides), the authorize URL, the
// token exchange on the wire, and the projected credential or failure.
func TestGitLabDuoLoginMatchesNeoPi(t *testing.T) {
	capture := loadGitLabDuoAuthCapture(t, "gitlab_duo_auth.json")
	defer func(restore func() time.Time) { now = restore }(now)
	now = func() time.Time { return time.UnixMilli(capture.Now) }
	for _, login := range capture.Logins {
		t.Run(login.Name, func(t *testing.T) {
			t.Setenv("GITLAB_CLIENT_ID", login.Env["GITLAB_CLIENT_ID"])
			t.Setenv("GITLAB_REDIRECT_URI", login.Env["GITLAB_REDIRECT_URI"])
			callback, err := ResolveCallback()
			if err != nil {
				t.Fatal(err)
			}
			want := login.Callback
			if callback.Port != want.PreferredPort || callback.Path != want.CallbackPath || callback.Host != want.CallbackHostname ||
				callback.RedirectURI != want.RedirectURI || callback.PortFallback != want.AllowPortFallback {
				t.Fatalf("callback = %+v, NeoPi resolved %+v", callback, want)
			}
			redirectURI := callback.RedirectURI
			if redirectURI == "" {
				redirectURI = "http://" + callback.Host + ":" + "8080" + callback.Path
			}
			if redirectURI != login.RedirectURI {
				t.Fatalf("redirect URI = %q, NeoPi advertised %q", redirectURI, login.RedirectURI)
			}
			session := Duo.newSession(redirectURI, capture.State, gitlabDuoPinnedVerifier())
			if got := session.AuthURL(); got != login.Authorize.URL {
				t.Fatalf("authorize URL\n got %s\nwant %s", got, login.Authorize.URL)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(login.Response.Status)
				_, _ = io.WriteString(w, login.Response.Body)
			}))
			defer server.Close()
			transport := &gitlabDuoTransport{server: server}
			credential, err := session.Exchange(context.Background(), &http.Client{Transport: transport}, "auth-code")
			assertGitLabDuoRequests(t, transport.seen, login.Requests)
			assertGitLabDuoOutcome(t, credential, err, login.Credentials, login.Error)
		})
	}
}

// Every captured refresh: the grant on the wire and the projected credential,
// an unrotated answer keeping the stored refresh token.
func TestGitLabDuoRefreshMatchesNeoPi(t *testing.T) {
	capture := loadGitLabDuoAuthCapture(t, "gitlab_duo_auth.json")
	defer func(restore func() time.Time) { now = restore }(now)
	now = func() time.Time { return time.UnixMilli(capture.Now) }
	t.Setenv("GITLAB_CLIENT_ID", "")
	for _, refresh := range capture.Refreshes {
		t.Run(refresh.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(refresh.Response.Status)
				_, _ = io.WriteString(w, refresh.Response.Body)
			}))
			defer server.Close()
			transport := &gitlabDuoTransport{server: server}
			credential, err := Duo.Refresh(context.Background(), &http.Client{Transport: transport}, refresh.Stored.Refresh)
			assertGitLabDuoRequests(t, transport.seen, refresh.Requests)
			assertGitLabDuoOutcome(t, credential, err, refresh.Credentials, refresh.Error)
		})
	}
}

// Every captured GitLab Duo Agent sign-in: the VS Code client and redirect
// NeoPi advertises with no listener, the authorize URL, the token exchange on
// the wire, and the projected credential or failure under the agent's id.
func TestGitLabDuoAgentLoginMatchesNeoPi(t *testing.T) {
	capture := loadGitLabDuoAuthCapture(t, "gitlab_duo_agent_auth.json")
	defer func(restore func() time.Time) { now = restore }(now)
	now = func() time.Time { return time.UnixMilli(capture.Now) }
	t.Setenv("GITLAB_CLIENT_ID", "an-override-the-agent-rule-ignores")
	for _, login := range capture.Logins {
		t.Run(login.Name, func(t *testing.T) {
			if Agent.RedirectURI() != login.RedirectURI || login.Callback.RedirectURI != login.RedirectURI {
				t.Fatalf("redirect URI = %q, NeoPi advertised %q", Agent.RedirectURI(), login.RedirectURI)
			}
			session := Agent.newSession(Agent.RedirectURI(), capture.State, gitlabDuoPinnedVerifier())
			if got := session.AuthURL(); got != login.Authorize.URL {
				t.Fatalf("authorize URL\n got %s\nwant %s", got, login.Authorize.URL)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(login.Response.Status)
				_, _ = io.WriteString(w, login.Response.Body)
			}))
			defer server.Close()
			transport := &gitlabDuoTransport{server: server}
			credential, err := session.Exchange(context.Background(), &http.Client{Transport: transport}, "auth-code")
			assertGitLabDuoRequests(t, transport.seen, login.Requests)
			assertGitLabDuoOutcome(t, credential, err, login.Credentials, login.Error)
		})
	}
}

// Every captured GitLab Duo Agent refresh: the grant with the VS Code
// redirect on the wire, and the projected credential.
func TestGitLabDuoAgentRefreshMatchesNeoPi(t *testing.T) {
	capture := loadGitLabDuoAuthCapture(t, "gitlab_duo_agent_auth.json")
	defer func(restore func() time.Time) { now = restore }(now)
	now = func() time.Time { return time.UnixMilli(capture.Now) }
	for _, refresh := range capture.Refreshes {
		t.Run(refresh.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(refresh.Response.Status)
				_, _ = io.WriteString(w, refresh.Response.Body)
			}))
			defer server.Close()
			transport := &gitlabDuoTransport{server: server}
			credential, err := Agent.Refresh(context.Background(), &http.Client{Transport: transport}, refresh.Stored.Refresh)
			assertGitLabDuoRequests(t, transport.seen, refresh.Requests)
			assertGitLabDuoOutcome(t, credential, err, refresh.Credentials, refresh.Error)
		})
	}
}

// What a user pastes back from the manual-only sign-in reads as NeoPi's
// parseCallbackInput reads it.
func TestParseCallbackInputMatchesNeoPi(t *testing.T) {
	for _, paste := range loadGitLabDuoAuthCapture(t, "gitlab_duo_agent_auth.json").Pastes {
		code, state := ParseCallbackInput(paste.Input)
		if code != paste.Parsed.Code || state != paste.Parsed.State {
			t.Fatalf("ParseCallbackInput(%q) = %q, %q; NeoPi read %q, %q", paste.Input, code, state, paste.Parsed.Code, paste.Parsed.State)
		}
	}
}

// NeoPi's localhost callback (OAuthCallbackFlow#createServer) serves both
// loopback families on one port. A port another process holds on ::1 is in
// use: an unpinned sign-in moves to a free port, a pinned one fails, and
// neither advertises a localhost URL ::1 would hand to that process.
func TestGitLabDuoCallbackTreatsIPv6CollisionAsInUse(t *testing.T) {
	held, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	defer func() { _ = held.Close() }()
	port := held.Addr().(*net.TCPAddr).Port
	closeAll := func(listeners []net.Listener) {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}

	listeners, redirectURI, err := Callback{Host: "localhost", Port: port, Path: "/callback", PortFallback: true}.Listen()
	if err != nil {
		t.Fatalf("fallback sign-in: %v", err)
	}
	closeAll(listeners)
	if bound := listeners[0].Addr().(*net.TCPAddr).Port; bound == port || len(listeners) != 2 {
		t.Fatalf("fallback bound port %d with %d listeners (%s); ::1 holds %d", bound, len(listeners), redirectURI, port)
	}

	for name, callback := range map[string]Callback{
		"no fallback": {Host: "localhost", Port: port, Path: "/callback"},
		"pinned":      {Host: "localhost", Port: port, Path: "/callback", RedirectURI: fmt.Sprintf("http://localhost:%d/callback", port)},
	} {
		listeners, _, err := callback.Listen()
		closeAll(listeners)
		if err == nil {
			t.Fatalf("%s: listened beside the ::1 holder of port %d", name, port)
		}
	}
}

// A redirect override names its path as WHATWG URL.pathname does: a URL
// without one returns to "/".
func TestGitLabDuoRedirectOverrideWithoutPathReturnsToRoot(t *testing.T) {
	t.Setenv("GITLAB_REDIRECT_URI", "http://127.0.0.1:9123")
	callback, err := ResolveCallback()
	if err != nil {
		t.Fatal(err)
	}
	if callback.Path != "/" {
		t.Fatalf("callback path = %q, NeoPi serves %q", callback.Path, "/")
	}
}
