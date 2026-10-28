package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// This is a private HTTP server with real identity, pool, routing, shared handlers,
// translators and provider execution. Upstreams are local fault fixtures, not E's
// independently required real-provider inference proof.
func TestNeoAttachedServerThreeProtocolFallbackAndScope(t *testing.T) {
	t.Setenv("NEOPROXY_DATA_DIR", "")
	identity, adminUser, _, _ := neoTestIdentity(t)
	server := newTestServerWithOptions(t, WithNeoStore(identity), WithRequestLoggerFactory(nil))
	if err := server.neoStartupError(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	admin := neo.Principal{UserID: adminUser.ID, Role: neo.RoleAdmin, Session: true}
	var failures, successes atomic.Int64
	fault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failures.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"injected first-stage failure"}}`)
	}))
	defer fault.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		successes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-http-proof","object":"chat.completion","created":1,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"attached server fallback"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	defer good.Close()
	manager := server.handlers.AuthManager
	manager.RegisterExecutor(executor.NewOpenAICompatExecutor("neo-http-test", server.cfg))
	ids := []string{"http-fault-" + uuid.NewString(), "http-good-" + uuid.NewString()}
	for i, id := range ids {
		model, endpoint := "fail", fault.URL
		if i == 1 {
			model, endpoint = "model", good.URL
		}
		// Each protocol must hit the injected fault rather than inherit the
		// previous protocol's intentionally triggered credential cooldown.
		if _, err := manager.Register(ctx, &coreauth.Auth{ID: id, Provider: "neo-http-test", Status: coreauth.StatusActive, Metadata: map[string]any{"disable_cooling": true}, Attributes: map[string]string{"api_key": "private-fixture", "base_url": endpoint}}); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "neo-http-test", []*registry.ModelInfo{{ID: model, Object: "model", ContextLength: 10000}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	if _, err := server.neoPool.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		account := neopool.AccountID(id)
		if err := server.neoPool.AssignOwner(ctx, account, admin.UserID); err != nil {
			t.Fatal(err)
		}
		if err := server.neoPool.ConfigureWindow(ctx, account, admin.UserID, "day", 1000000, time.Now().Add(-time.Hour).UnixMilli(), true); err != nil {
			t.Fatal(err)
		}
	}
	vm, err := server.neoRoute.SaveVirtualModel(neo.WithPrincipal(ctx, admin), neoroute.VirtualModel{Name: "http-route", Stages: []neoroute.Stage{
		{ID: "first", Model: "neo-http-test/fail", Accounts: neoroute.AccountScope{IDs: []string{neopool.AccountID(ids[0])}}, Draw: "fill", PerAccount: .5, Cap: .5, Window: "day", Per: "user"},
		{ID: "fallback", Model: "neo-http-test/model", Accounts: neoroute.AccountScope{IDs: []string{neopool.AccountID(ids[1])}}, Draw: "fill", PerAccount: .5, Cap: .5, Window: "day", Per: "user"},
	}, Terminal: neoroute.Terminal{Kind: "limit", Message: "No route"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	user, err := identity.CreateUser(ctx, neo.UserInput{Handle: "http-client", Role: neo.RoleService, State: "active", Grants: []neo.Grant{{VirtualModel: vm.ID, Allotment: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := identity.MintKey(ctx, admin, user.ID, "http-scope", []string{neopool.AccountID(ids[0]), neopool.AccountID(ids[1])}, nil)
	if err != nil {
		t.Fatal(err)
	}
	private := httptest.NewServer(server.engine)
	defer private.Close()
	request := func(method, path, body string) (int, http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, private.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		response, err := private.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Error(err)
			}
		}()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header, data
	}
	status, _, data := request("GET", "/v1/models", "")
	if status != 200 || !strings.Contains(string(data), vm.Served) || strings.Contains(string(data), "neo-http-test/") {
		t.Fatalf("consumer catalogue status=%d body=%s", status, data)
	}
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"neoproxy/http-route","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`},
		{"/v1/responses", `{"model":"neoproxy/http-route","input":[{"role":"user","content":"hello"}],"max_output_tokens":32}`},
		{"/v1/messages", `{"model":"neoproxy/http-route","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			status, headers, data := request("POST", tc.path, tc.body)
			if status != 200 || !strings.Contains(string(data), "attached server fallback") {
				t.Fatalf("status=%d body=%s", status, data)
			}
			var raw []byte
			if err := identity.DB().QueryRowContext(ctx, `SELECT body FROM neoroute_requests WHERE id=?`, headers.Get("X-NeoProxy-Request-ID")).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var record neoroute.RequestRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			if record.Asked != vm.Served || record.Decision.StageID != "fallback" || record.Decision.AccountID != neopool.AccountID(ids[1]) || record.Decision.Trail[0].FailureStatus != 503 || record.Tokens.In != 10 || record.Tokens.Out != 5 {
				t.Fatalf("incorrect HTTP decision/usage: %s", raw)
			}
		})
	}
	if failures.Load() < 3 || successes.Load() != 3 {
		t.Fatalf("first-stage failures=%d fallback successes=%d", failures.Load(), successes.Load())
	}
	for _, model := range []string{"model", "neoproxy/neo-http-test/model"} {
		status, _, data := request("POST", "/v1/chat/completions", `{"model":"`+model+`","messages":[],"max_tokens":32}`)
		if status < 400 {
			t.Fatalf("raw/direct model bypass status=%d body=%s", status, data)
		}
	}
	if _, err := identity.UpdateUser(ctx, user.ID, neo.UserInput{Handle: user.Handle, Role: neo.RoleService, State: "active"}); err != nil {
		t.Fatal(err)
	}
	status, _, data = request("GET", "/v1/models", "")
	if status != 200 || strings.Contains(string(data), vm.Served) {
		t.Fatalf("revoked grant remained discoverable status=%d body=%s", status, data)
	}
	status, _, data = request("POST", "/v1/chat/completions", `{"model":"neoproxy/http-route","messages":[],"max_tokens":32}`)
	if status != 403 || !strings.Contains(string(data), "neoproxy_not-granted") || successes.Load() != 3 {
		t.Fatalf("revoked grant executed status=%d body=%s", status, data)
	}
}
