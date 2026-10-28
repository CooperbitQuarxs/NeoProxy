package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// These requests execute the real shared auth manager, translator, provider HTTP
// executor and SQLite quota ledger. Only the upstream servers are fault fixtures;
// E separately proves real provider inference rather than treating these as it.
func TestNeoRouteThreeProtocolExecutionAndPinnedFallback(t *testing.T) {
	for _, protocol := range []string{"openai", "openai-response", "claude"} {
		t.Run(protocol, func(t *testing.T) {
			ctx := context.Background()
			var failed, success, rogue atomic.Int64
			var upstream []byte
			fault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				failed.Add(1)
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":{"message":"forced first stage failure"}}`))
			}))
			defer fault.Close()
			good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				success.Add(1)
				upstream, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl-proof","object":"chat.completion","created":1,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"real shared engine"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
			}))
			defer good.Close()
			forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rogue.Add(1); w.WriteHeader(500) }))
			defer forbidden.Close()
			identity, err := neo.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer identity.Close()
			session, _, err := identity.Bootstrap(ctx, "captain", "Captain", "test-password-123")
			if err != nil {
				t.Fatal(err)
			}
			admin := neo.Principal{UserID: session.UserID, Role: "admin", Session: true}
			cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "model", Protocol: "openai"}}, Params: map[string]any{"reasoning_effort": "low"}}}}}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor.NewOpenAICompatExecutor("neo-test", cfg))
			ids := []string{"fault-" + uuid.NewString(), "good-" + uuid.NewString(), "rogue-" + uuid.NewString()}
			urls := []string{fault.URL, good.URL, forbidden.URL}
			for i, id := range ids {
				models := []*registry.ModelInfo{{ID: "model", Object: "model"}}
				if i == 0 {
					models = []*registry.ModelInfo{{ID: "fail", Object: "model"}}
				}
				if i == 2 {
					models = append(models, &registry.ModelInfo{ID: "fail", Object: "model"})
				}
				_, err = manager.Register(ctx, &coreauth.Auth{ID: id, Provider: "neo-test", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "test-only", "base_url": urls[i]}})
				if err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "neo-test", models)
				defer registry.GetGlobalRegistry().UnregisterClient(id)
			}
			pools, err := neopool.New(identity.DB(), manager)
			if err != nil {
				t.Fatal(err)
			}
			identity.SetAccountScopeValidator(pools.ScopeOwned)
			if _, err = pools.Snapshot(ctx); err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				account := neopool.AccountID(id)
				if err = pools.AssignOwner(ctx, account, admin.UserID); err != nil {
					t.Fatal(err)
				}
				if err = pools.ConfigureWindow(ctx, account, admin.UserID, "day", 1000000, time.Now().Add(-time.Hour).UnixMilli(), true); err != nil {
					t.Fatal(err)
				}
			}
			route, err := neoroute.New(identity, pools)
			if err != nil {
				t.Fatal(err)
			}
			effort := "high"
			vm, err := route.SaveVirtualModel(neo.WithPrincipal(ctx, admin), neoroute.VirtualModel{Name: "route", Stages: []neoroute.Stage{{ID: "s0", Model: "neo-test/fail", Accounts: neoroute.AccountScope{IDs: []string{neopool.AccountID(ids[0])}}, Draw: "fill", PerAccount: .5, Cap: .5, Window: "day", Per: "user"}, {ID: "s1", Model: "neo-test/model", Accounts: neoroute.AccountScope{IDs: []string{neopool.AccountID(ids[1])}}, Draw: "fill", PerAccount: .5, Cap: .5, Window: "day", Per: "user", Effort: &effort}}, Terminal: neoroute.Terminal{Kind: "limit", Message: "No route"}}, true)
			if err != nil {
				t.Fatal(err)
			}
			user, err := identity.CreateUser(ctx, neo.UserInput{Handle: "client", Role: "service", State: "active", Grants: []neo.Grant{{VirtualModel: vm.ID, Allotment: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			_, secret, err := identity.MintKey(ctx, admin, user.ID, "scope", []string{neopool.AccountID(ids[0]), neopool.AccountID(ids[1])}, nil)
			if err != nil {
				t.Fatal(err)
			}
			principal, err := identity.AuthenticateKey(ctx, secret)
			if err != nil {
				t.Fatal(err)
			}
			ctx = neo.WithPrincipal(ctx, principal)
			handler := NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			handler.SetNeoResolver(route)
			body := []byte(`{"model":"neoproxy/route","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
			if protocol == "openai-response" {
				body = []byte(`{"model":"neoproxy/route","input":[{"role":"user","content":"hello"}],"max_output_tokens":32}`)
			}
			response, headers, errMessage := handler.ExecuteWithAuthManager(ctx, protocol, "neoproxy/route", body, "")
			if errMessage != nil {
				t.Fatalf("execution: %+v", errMessage)
			}
			if !strings.Contains(string(response), "real shared engine") {
				t.Fatalf("missing translated reply: %s", response)
			}
			if failed.Load() < 1 || success.Load() != 1 || rogue.Load() != 0 {
				t.Fatalf("failed=%d success=%d rogue=%d", failed.Load(), success.Load(), rogue.Load())
			}
			if gjson.GetBytes(upstream, "model").String() != "model" || gjson.GetBytes(upstream, "reasoning_effort").String() != "low" {
				t.Fatalf("routing overwrote final payload rules: %s", upstream)
			}
			requestID := headers.Get("X-NeoProxy-Request-ID")
			if requestID == "" {
				t.Fatal("missing request correlation")
			}
			var raw []byte
			if err = identity.DB().QueryRow(`SELECT body FROM neoroute_requests WHERE id=?`, requestID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var record neoroute.RequestRecord
			if err = json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			if record.Asked != "neoproxy/route" || record.Decision.StageID != "s1" || record.Decision.Model != "neo-test/model" || record.Decision.AccountID != neopool.AccountID(ids[1]) || record.Decision.Trail[0].Skipped != "upstream-error" || record.Decision.Trail[0].FailureStatus != 503 || record.Tokens.In != 10 || record.Tokens.Out != 5 {
				t.Fatalf("incorrect actual decision/usage: %s", raw)
			}
		})
	}
}
