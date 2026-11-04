package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
)

// The credential middleware reads c.Query before the pool login handlers run,
// which primes gin's per-context query cache. The pool handlers must still hand
// the engine their own narrowed query, so the dashboard "Sign in provider" flow
// reaches the real management OAuth handlers instead of "provider is required".
func TestNeoPoolProviderSignInReachesEngineThroughMiddlewareChain(t *testing.T) {
	t.Setenv("NEOPROXY_DATA_DIR", "")
	store, _, token, _ := neoTestIdentity(t)
	cfg := &proxyconfig.Config{}
	server := newTestServerWithConfig(t, cfg, WithNeoStore(store), WithRequestLoggerFactory(nil))
	if server.neoErr != nil {
		t.Fatal(server.neoErr)
	}
	// Loopback OAuth callbacks need a configured server port; nothing listens.
	cfg.Port = 18399
	server.mgmt.SetConfig(cfg)
	request := func(method, path, body string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Neoproxy-CSRF", "1")
		req.AddCookie(&http.Cookie{Name: neo.SessionCookie, Value: token})
		w := httptest.NewRecorder()
		server.engine.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}

	status, data := request("POST", "/v8/neo/vault/login/devin", `{}`)
	if status != http.StatusOK {
		t.Fatalf("start sign-in: %d %s", status, data)
	}
	var started struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(data, &started); err != nil || started.State == "" || !strings.Contains(started.URL, "redirect_uri=") {
		t.Fatalf("engine did not return an authorization URL and state: %s", data)
	}

	status, data = request("GET", "/v8/neo/vault/login/status?state="+started.State, "")
	if status != http.StatusOK || !strings.Contains(string(data), `"wait"`) {
		t.Fatalf("status before callback: %d %s", status, data)
	}
	status, data = request("POST", "/v8/neo/vault/login/callback", `{"provider":"devin","state":"`+started.State+`","error":"access_denied"}`)
	if status != http.StatusOK {
		t.Fatalf("callback: %d %s", status, data)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, data = request("GET", "/v8/neo/vault/login/status?state="+started.State, "")
		if status == http.StatusOK && strings.Contains(string(data), `"error"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("callback never reached the engine login: %d %s", status, data)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Engine failures keep their cause instead of a generic message.
	status, data = request("POST", "/v8/neo/vault/login/gemini", `{}`)
	if status != http.StatusNotFound || !strings.Contains(string(data), "provider_not_found") {
		t.Fatalf("engine error was not surfaced: %d %s", status, data)
	}
}
