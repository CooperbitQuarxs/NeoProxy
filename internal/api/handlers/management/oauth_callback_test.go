package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPostOAuthCallbackCreatesMissingAuthDir(t *testing.T) {

	authDir := filepath.Join(t.TempDir(), "missing-auth")
	state := "test-antigravity-state"
	RegisterOAuthSession(state, "antigravity")
	defer CompleteOAuthSession(state)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.POST("/v0/management/oauth-callback", h.PostOAuthCallback)

	body := `{"provider":"antigravity","redirect_url":"http://localhost:59788/oauth-callback?state=test-antigravity-state&code=test-code"}`
	req := httptest.NewRequest(http.MethodPost, "/v0/management/oauth-callback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	callbackPath := filepath.Join(authDir, ".oauth-antigravity-"+state+".oauth")
	data, errRead := os.ReadFile(callbackPath)
	if errRead != nil {
		t.Fatalf("expected callback file to be written: %v", errRead)
	}

	var payload oauthCallbackFilePayload
	if errUnmarshal := json.Unmarshal(data, &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode callback payload: %v", errUnmarshal)
	}
	if payload.State != state || payload.Code != "test-code" || payload.Error != "" {
		t.Fatalf("unexpected callback payload: %+v", payload)
	}
}

func TestGetOAuthCallbackWritesPluginProviderCallback(t *testing.T) {
	authDir := filepath.Join(t.TempDir(), "missing-auth")
	state := "test-geminicli-state"
	if errRegister := RegisterPluginOAuthSession(state, "gemini-cli", nil); errRegister != nil {
		t.Fatalf("register plugin oauth session: %v", errRegister)
	}
	defer CompleteOAuthSession(state)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.GET("/v0/management/oauth-callback", h.GetOAuthCallback)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/oauth-callback?state="+state+"&code=test-code", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	callbackPath := filepath.Join(authDir, ".oauth-gemini-cli-"+state+".oauth")
	data, errRead := os.ReadFile(callbackPath)
	if errRead != nil {
		t.Fatalf("expected callback file to be written: %v", errRead)
	}

	var payload oauthCallbackFilePayload
	if errUnmarshal := json.Unmarshal(data, &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode callback payload: %v", errUnmarshal)
	}
	if payload.State != state || payload.Code != "test-code" || payload.Error != "" {
		t.Fatalf("unexpected callback payload: %+v", payload)
	}
}

func TestGetOAuthCallbackDoesNotAliasPluginProvider(t *testing.T) {
	authDir := filepath.Join(t.TempDir(), "missing-auth")
	state := "test-openai-plugin-state"
	if errRegister := RegisterPluginOAuthSession(state, "openai", nil); errRegister != nil {
		t.Fatalf("register plugin oauth session: %v", errRegister)
	}
	defer CompleteOAuthSession(state)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.GET("/v0/management/oauth-callback", h.GetOAuthCallback)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/oauth-callback?state="+state+"&code=test-code", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	callbackPath := filepath.Join(authDir, ".oauth-openai-"+state+".oauth")
	if _, errRead := os.ReadFile(callbackPath); errRead != nil {
		t.Fatalf("expected plugin callback provider to stay openai: %v", errRead)
	}
	if _, errRead := os.ReadFile(filepath.Join(authDir, ".oauth-codex-"+state+".oauth")); errRead == nil {
		t.Fatal("unexpected codex callback file for openai plugin provider")
	}
}

func TestWriteOAuthCallbackFileForPendingSessionCreatesMissingAuthDirForCallbackProviders(t *testing.T) {
	// xAI uses device-code flow and no longer writes callback files.
	providers := []string{"anthropic", "codex", "gemini", "antigravity"}
	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			authDir := filepath.Join(t.TempDir(), "missing-auth")
			state := provider + "-state"
			RegisterOAuthSession(state, provider)
			defer CompleteOAuthSession(state)

			path, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, provider, state, "code-"+provider, "")
			if errWrite != nil {
				t.Fatalf("expected callback file write to succeed: %v", errWrite)
			}

			data, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatalf("expected callback file to be written: %v", errRead)
			}

			var payload oauthCallbackFilePayload
			if errUnmarshal := json.Unmarshal(data, &payload); errUnmarshal != nil {
				t.Fatalf("failed to decode callback payload: %v", errUnmarshal)
			}
			if payload.State != state || payload.Code != "code-"+provider || payload.Error != "" {
				t.Fatalf("unexpected callback payload: %+v", payload)
			}
		})
	}
}

// TestPostOAuthCallbackReadsPastedGitLabAgentAddress finishes a manual-only
// GitLab Duo Agent sign-in from what the person pastes, as NeoPi's
// parseCallbackInput reads it, and refuses a paste naming another sign-in's
// state instead of handing its code to this one.
func TestPostOAuthCallbackReadsPastedGitLabAgentAddress(t *testing.T) {
	const state = "a1b2c3d4e5f60718"
	cases := []struct {
		name     string
		body     string
		status   int
		wantCode string
	}{
		{"vscode address", `{"provider":"gitlab-duo-agent","state":"` + state + `","code":"the-code","redirect_url":"vscode://gitlab.gitlab-workflow/authentication?code=the-code&state=` + state + `"}`, http.StatusOK, "the-code"},
		{"address of another sign-in", `{"provider":"gitlab-duo-agent","state":"` + state + `","code":"the-code","redirect_url":"vscode://gitlab.gitlab-workflow/authentication?code=the-code&state=ffff0000ffff0000"}`, http.StatusBadRequest, ""},
		{"query alone", `{"provider":"gitlab-duo-agent","state":"` + state + `","code":"?code=the-code&state=` + state + `"}`, http.StatusOK, "the-code"},
		{"code and state", `{"provider":"gitlab-duo-agent","state":"` + state + `","code":"the-code#` + state + `"}`, http.StatusOK, "the-code"},
		{"code and another state", `{"provider":"gitlab-duo-agent","state":"` + state + `","code":"the-code#ffff0000ffff0000"}`, http.StatusBadRequest, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authDir := t.TempDir()
			RegisterOAuthSession(state, "gitlab-duo-agent")
			defer CompleteOAuthSession(state)
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
			router := gin.New()
			router.POST("/v0/management/oauth-callback", h.PostOAuthCallback)
			req := httptest.NewRequest(http.MethodPost, "/v0/management/oauth-callback", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			data, errRead := os.ReadFile(filepath.Join(authDir, ".oauth-gitlab-duo-agent-"+state+".oauth"))
			if tc.wantCode == "" {
				if errRead == nil {
					t.Fatalf("a refused paste reached the sign-in: %s", data)
				}
				return
			}
			var payload oauthCallbackFilePayload
			if errRead != nil || json.Unmarshal(data, &payload) != nil || payload.Code != tc.wantCode || payload.State != state {
				t.Fatalf("callback payload %s (%v)", data, errRead)
			}
		})
	}
}
