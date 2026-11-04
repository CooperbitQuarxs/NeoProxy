package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	devinauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestDevinAuthenticatorProviderAndRefreshLead(t *testing.T) {
	authenticator := NewDevinAuthenticator()
	if authenticator.Provider() != "devin" {
		t.Fatalf("Provider() = %q, want devin", authenticator.Provider())
	}
	if lead := authenticator.RefreshLead(); lead != nil {
		t.Fatalf("RefreshLead() = %v, want nil: NeoPi's Devin credential has no refresh grant", lead)
	}
}

func TestDevinAuthenticatorHeadlessPromptNil(t *testing.T) {
	_, err := NewDevinAuthenticator().Login(context.Background(), &config.Config{}, &LoginOptions{NoBrowser: true})
	if err == nil || !strings.Contains(err.Error(), "requires an interactive prompt") {
		t.Fatalf("error = %v, want interactive prompt requirement", err)
	}
}

// tokenServer answers the code exchange for wantCode and leaves seat
// management unavailable, so the record falls back to a digest identity.
func tokenServer(t *testing.T, wantCode, token string) (*devinauth.DevinAuthService, *bool) {
	t.Helper()
	exchanged := false
	authSvc := newTestDevinAuthService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/cli/token" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		exchanged = true
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["code"] != wantCode || body["code_verifier"] == "" {
			t.Errorf("token request body = %v, want code %q with a verifier", body, wantCode)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"` + token + `"}`))
	})
	return authSvc, &exchanged
}

func TestDevinAuthenticatorHeadlessCodeLogin(t *testing.T) {
	for name, paste := range map[string]string{
		"bare code":                  "devin-cli-auth-code-123",
		"quoted code":                `"devin-cli-auth-code-123"`,
		"callback URL without state": "http://127.0.0.1:59653/callback?code=devin-cli-auth-code-123",
	} {
		t.Run(name, func(t *testing.T) {
			authSvc, _ := tokenServer(t, "devin-cli-auth-code-123", "eyJtest.manual.token")
			authenticator := NewDevinAuthenticator()
			authenticator.AuthService = authSvc
			auth, err := authenticator.Login(context.Background(), &config.Config{}, &LoginOptions{
				NoBrowser: true,
				Prompt:    func(string) (string, error) { return paste, nil },
			})
			if err != nil {
				t.Fatalf("Login failed: %v", err)
			}
			if auth.Provider != "devin" || auth.Attributes["api_key"] != "eyJtest.manual.token" || auth.Metadata["api_key"] != "eyJtest.manual.token" {
				t.Fatalf("stored credential = %s %v, want the raw session token", auth.Provider, auth.Attributes)
			}
			if auth.Metadata["expired"] == nil {
				t.Fatal("record carries no projected expiry")
			}
		})
	}
}

func TestDevinAuthenticatorHeadlessEmptyInputAborts(t *testing.T) {
	_, err := NewDevinAuthenticator().Login(context.Background(), &config.Config{}, &LoginOptions{
		NoBrowser: true,
		Prompt:    func(string) (string, error) { return "", nil },
	})
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("error = %v, want cancellation", err)
	}
}

func TestParseDevinManualPaste(t *testing.T) {
	tests := []struct {
		name, input, expectedState, wantCode, wantErr string
		wantSentinel                                  error
	}{
		{name: "empty", input: "  "},
		{name: "quoted empty", input: `"   "`},
		{name: "raw code", input: "devin-cli-auth-code-123", wantCode: "devin-cli-auth-code-123"},
		{name: "quoted code", input: `"quoted-code-xyz"`, wantCode: "quoted-code-xyz"},
		{name: "code with echoed state", input: "code-1#state-1", wantCode: "code-1#state-1"},
		{name: "callback without state", input: "http://127.0.0.1:9/callback?code=c1", wantCode: "c1"},
		{name: "callback matching state", input: "http://127.0.0.1:9/callback?code=c1&state=abc", expectedState: "abc", wantCode: "c1"},
		{name: "callback mismatched state", input: "http://127.0.0.1:9/callback?code=stolen-code&state=other", expectedState: "abc", wantErr: "state mismatch"},
		{name: "oauth error", input: "http://127.0.0.1:9/callback?error=access_denied&state=abc", wantErr: "oauth error"},
		{name: "oauth error with description", input: "http://127.0.0.1:9/callback?error=access_denied&error_description=user%20denied", wantErr: "user denied"},
		{name: "unrecognized", input: "not a code, has spaces", wantErr: "unrecognized", wantSentinel: errDevinUnrecognizedPaste},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, errPaste := parseDevinManualPaste(tt.input, tt.expectedState)
			if tt.wantErr != "" {
				if errPaste == nil || !strings.Contains(errPaste.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", errPaste, tt.wantErr)
				}
				if tt.wantSentinel != nil && !errors.Is(errPaste, tt.wantSentinel) {
					t.Fatalf("error %v is not %v", errPaste, tt.wantSentinel)
				}
				return
			}
			if errPaste != nil || code != tt.wantCode {
				t.Fatalf("code=%q err=%v, want code=%q", code, errPaste, tt.wantCode)
			}
		})
	}
}

// TestDevinAuthenticatorHeadlessAuthURL: without a browser the login prints
// NeoPi's authorize URL for the rule's loopback callback, and the pasted
// redirect completes it.
func TestDevinAuthenticatorHeadlessAuthURL(t *testing.T) {
	out := captureStdout(t, func() {
		_, _ = NewDevinAuthenticator().Login(context.Background(), &config.Config{}, &LoginOptions{
			NoBrowser: true,
			Prompt:    func(string) (string, error) { return "", nil },
		})
	})
	var authURL string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "/auth/cli/continue?") {
			authURL = strings.TrimSpace(line)
			break
		}
	}
	parsed, errParse := url.Parse(authURL)
	if authURL == "" || errParse != nil {
		t.Fatalf("printed output did not contain an auth URL:\n%s", out)
	}
	q := parsed.Query()
	if parsed.Host != "app.devin.ai" || q.Get("redirect_uri") != "http://127.0.0.1:59653/callback" || q.Has("cli_pkce_marker") {
		t.Fatalf("unexpected authorize URL %s", authURL)
	}
	if _, errUUID := uuid.Parse(q.Get("state")); errUUID != nil {
		t.Fatalf("state %q is not a uuid", q.Get("state"))
	}
	if !strings.HasPrefix(parsed.RawQuery, "response_type=code&redirect_uri=") || !strings.HasSuffix(parsed.RawQuery, "&prompt=select_account") {
		t.Fatalf("query order differs from NeoPi's: %s", parsed.RawQuery)
	}
}

func TestDevinAuthenticatorHeadlessStateMismatch(t *testing.T) {
	authSvc, exchanged := tokenServer(t, "unused", "eyJunused")
	authenticator := NewDevinAuthenticator()
	authenticator.AuthService = authSvc
	_, err := authenticator.Login(context.Background(), &config.Config{}, &LoginOptions{
		NoBrowser: true,
		Prompt: func(string) (string, error) {
			return "http://127.0.0.1:9/callback?code=stolen-code&state=other-session", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("error = %v, want state mismatch", err)
	}
	if *exchanged {
		t.Fatal("token exchange ran on a CSRF mismatch")
	}
}

func TestDevinAuthenticatorHeadlessOAuthErrorURL(t *testing.T) {
	_, err := NewDevinAuthenticator().Login(context.Background(), &config.Config{}, &LoginOptions{
		NoBrowser: true,
		Prompt: func(string) (string, error) {
			return "http://127.0.0.1:9/callback?error=access_denied&error_description=user%20denied", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("error = %v, want access_denied oauth error", err)
	}
}

func newTestDevinAuthService(t *testing.T, handler http.HandlerFunc) *devinauth.DevinAuthService {
	t.Helper()
	mockServer := httptest.NewServer(handler)
	t.Cleanup(mockServer.Close)
	authSvc := devinauth.NewDevinAuthService(mockServer.Client())
	authSvc.SetAPIBaseURL(mockServer.URL)
	authSvc.SetServerBaseURL(mockServer.URL)
	return authSvc
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, errPipe := os.Pipe()
	if errPipe != nil {
		t.Fatal(errPipe)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, errCopy := io.Copy(&buf, r); errCopy != nil {
		t.Fatal(errCopy)
	}
	_ = r.Close()
	return buf.String()
}
