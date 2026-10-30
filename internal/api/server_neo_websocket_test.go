package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// Exercise the attached server and real local Codex executor, not a replacement
// route handler. No provider credentials or external inference are involved.
func TestNeoMemberResponsesWebsocket(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/backend-api/codex/responses"} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("NEOPROXY_DATA_DIR", "")
			identity, adminUser, _, _ := neoTestIdentity(t)
			server := newTestServerWithOptions(t, WithNeoStore(identity), WithRequestLoggerFactory(nil))
			if err := server.neoStartupError(); err != nil {
				t.Fatal(err)
			}
			server.cfg.CodexResponseSteering = true
			server.cfg.Codex.ResponseSteering = true
			ctx := t.Context()
			admin := neo.Principal{UserID: adminUser.ID, Role: neo.RoleAdmin, Session: true}
			var allowedCalls, forbiddenCalls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer allowed-fixture" {
					forbiddenCalls.Add(1)
					w.WriteHeader(http.StatusForbidden)
					return
				}
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Errorf("upstream upgrade: %v", err)
					return
				}
				defer func() { _ = conn.Close() }()
				for {
					_, request, err := conn.ReadMessage()
					if err != nil {
						return
					}
					if gjson.GetBytes(request, "model").String() != "member-ws-model" {
						t.Errorf("virtual model was not resolved: %s", request)
						return
					}
					if gjson.GetBytes(request, "previous_response_id").Exists() {
						t.Errorf("scoped turn retained account-bound response ID: %s", request)
						return
					}
					turn := allowedCalls.Add(1)
					if turn > 1 && !strings.Contains(string(request), "member scoped answer") {
						t.Errorf("continuation lost prior assistant output: %s", request)
						return
					}
					responseID := fmt.Sprintf("resp_member_%d", turn)
					if err := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, responseID))); err != nil {
						return
					}
					completed := fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[{"id":"message_member","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"member scoped answer"}]}],"usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7}}}`, responseID)
					if err := conn.WriteMessage(websocket.TextMessage, []byte(completed)); err != nil {
						return
					}
				}
			}))
			defer upstream.Close()
			manager := server.handlers.AuthManager
			manager.RegisterExecutor(executor.NewCodexAutoExecutor(server.cfg))
			ids := []string{"member-ws-allowed-" + uuid.NewString(), "member-ws-forbidden-" + uuid.NewString()}
			for i, id := range ids {
				key := "allowed-fixture"
				if i == 1 {
					key = "forbidden-fixture"
				}
				if _, err := manager.Register(ctx, &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": key, "base_url": upstream.URL, "websockets": "true"}}); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "member-ws-model", Object: "model"}})
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
			vm, err := server.neoRoute.SaveVirtualModel(neo.WithPrincipal(ctx, admin), neoroute.VirtualModel{Name: "member-ws", Stages: []neoroute.Stage{{ID: "allowed", Model: "codex/member-ws-model", Accounts: neoroute.AccountScope{All: true}, Draw: "fill", PerAccount: .5, Cap: .5, Window: "day", Per: "user"}}, Terminal: neoroute.Terminal{Kind: "limit", Message: "No authorized account"}}, true)
			if err != nil {
				t.Fatal(err)
			}
			outside, err := server.neoRoute.SaveVirtualModel(neo.WithPrincipal(ctx, admin), neoroute.VirtualModel{Name: "outside-scope", Stages: []neoroute.Stage{{ID: "forbidden", Model: "codex/member-ws-model", Accounts: neoroute.AccountScope{IDs: []string{neopool.AccountID(ids[1])}}, Draw: "fill", PerAccount: .5, Cap: .5, Window: "day", Per: "user"}}, Terminal: neoroute.Terminal{Kind: "limit", Message: "No authorized account"}}, true)
			if err != nil {
				t.Fatal(err)
			}
			grants := []neo.Grant{{VirtualModel: vm.ID, Allotment: 1}, {VirtualModel: outside.ID, Allotment: 1}}
			member, err := identity.CreateUser(ctx, neo.UserInput{Handle: "socket-member", Role: neo.RoleMember, State: "active", Password: "socket-test-password", Grants: grants})
			if err != nil {
				t.Fatal(err)
			}
			key, secret, err := identity.MintKey(ctx, admin, member.ID, "member-socket", []string{neopool.AccountID(ids[0])}, nil)
			if err != nil {
				t.Fatal(err)
			}
			private := httptest.NewServer(server.engine)
			defer private.Close()
			dial := func(secret string) (*websocket.Conn, *http.Response, error) {
				t.Helper()
				return websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(private.URL, "http")+path, http.Header{"Authorization": []string{"Bearer " + secret}})
			}
			client, response, err := dial(secret)
			if err != nil {
				status := 0
				if response != nil {
					status = response.StatusCode
					_ = response.Body.Close()
				}
				t.Fatalf("valid member WebSocket handshake: HTTP %d: %v", status, err)
			}
			defer func() { _ = client.Close() }()
			if err := client.WriteJSON(map[string]any{"type": "response.create", "model": vm.Served, "generate": false, "input": []any{}, "max_output_tokens": 32}); err != nil {
				t.Fatal(err)
			}
			var prewarmID string
			for _, want := range []string{"response.created", "response.completed"} {
				_, event, err := client.ReadMessage()
				if err != nil || gjson.GetBytes(event, "type").String() != want {
					t.Fatalf("scoped prewarm: event=%s error=%v", event, err)
				}
				prewarmID = gjson.GetBytes(event, "response.id").String()
			}
			if prewarmID == "" || allowedCalls.Load() != 0 || forbiddenCalls.Load() != 0 {
				t.Fatal("local prewarm opened an upstream inference")
			}
			readTurn := func(payload map[string]any) string {
				t.Helper()
				if err := client.WriteJSON(payload); err != nil {
					t.Fatal(err)
				}
				for {
					_, event, err := client.ReadMessage()
					if err != nil {
						t.Fatal(err)
					}
					switch gjson.GetBytes(event, "type").String() {
					case "error":
						t.Fatalf("authorized socket turn failed: %s", event)
					case "response.completed":
						if !strings.Contains(string(event), "member scoped answer") {
							t.Fatalf("missing assistant answer: %s", event)
						}
						return gjson.GetBytes(event, "response.id").String()
					}
				}
			}
			responseID := readTurn(map[string]any{"type": "response.create", "previous_response_id": prewarmID, "input": []any{}, "max_output_tokens": 32})
			responseID = readTurn(map[string]any{"type": "response.create", "previous_response_id": responseID, "input": []any{}, "max_output_tokens": 32})
			responseID = readTurn(map[string]any{"type": "response.append", "input": []any{}, "max_output_tokens": 32})
			if allowedCalls.Load() != 3 || forbiddenCalls.Load() != 0 {
				t.Fatalf("scoped continuation calls: allowed=%d forbidden=%d", allowedCalls.Load(), forbiddenCalls.Load())
			}
			assertDenied := func(conn *websocket.Conn, payload map[string]any, status int) {
				t.Helper()
				defer func() { _ = conn.Close() }()
				if err := conn.WriteJSON(payload); err != nil {
					t.Fatal(err)
				}
				_, event, err := conn.ReadMessage()
				if err == nil {
					if gjson.GetBytes(event, "type").String() != "error" || gjson.GetBytes(event, "status").Int() != int64(status) {
						t.Fatalf("unauthorized socket emitted inference: event=%s", event)
					}
				} else if !websocket.IsCloseError(err, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure, websocket.CloseNoStatusReceived) {
					t.Fatalf("expected authorization rejection, got %v", err)
				}
				if allowedCalls.Load() != 3 || forbiddenCalls.Load() != 0 {
					t.Fatalf("unauthorized socket reached upstream: allowed=%d forbidden=%d", allowedCalls.Load(), forbiddenCalls.Load())
				}
			}
			for _, model := range []string{"member-ws-model", "neoproxy/codex/member-ws-model", outside.Served} {
				conn, _, err := dial(secret)
				if err != nil {
					t.Fatal(err)
				}
				status := http.StatusForbidden
				if model == "member-ws-model" {
					status = http.StatusNotFound
				} else if model == outside.Served {
					status = http.StatusTooManyRequests
				}
				assertDenied(conn, map[string]any{"type": "response.create", "model": model, "input": []any{}, "max_output_tokens": 32}, status)
			}
			grantSocket, _, err := dial(secret)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := identity.UpdateUser(ctx, member.ID, neo.UserInput{Handle: member.Handle, Role: member.Role, State: "active"}); err != nil {
				t.Fatal(err)
			}
			assertDenied(grantSocket, map[string]any{"type": "response.create", "model": vm.Served, "input": []any{}, "max_output_tokens": 32}, http.StatusForbidden)
			if _, err := identity.UpdateUser(ctx, member.ID, neo.UserInput{Handle: member.Handle, Role: member.Role, State: "active", Grants: grants}); err != nil {
				t.Fatal(err)
			}
			prewarmSocket, _, err := dial(secret)
			if err != nil {
				t.Fatal(err)
			}
			// The same established socket must not keep handshake-time authority.
			if err := identity.RevokeKey(ctx, admin, member.ID, key.ID); err != nil {
				t.Fatal(err)
			}
			assertDenied(client, map[string]any{"type": "response.create", "previous_response_id": responseID, "input": []any{}, "max_output_tokens": 32}, http.StatusUnauthorized)
			assertDenied(prewarmSocket, map[string]any{"type": "response.create", "model": vm.Served, "generate": false, "input": []any{}}, http.StatusUnauthorized)
			for _, credential := range []string{secret, "nk_invalid-member-key"} {
				conn, response, err := dial(credential)
				if conn != nil {
					_ = conn.Close()
				}
				if response != nil {
					_ = response.Body.Close()
				}
				if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
					t.Fatalf("invalid/revoked member handshake: response=%v error=%v", response, err)
				}
			}
			// Each successful turn must have passed the real scoped resolver.
			rows, err := identity.DB().QueryContext(ctx, `SELECT body FROM neoroute_requests WHERE key_id=? AND outcome='ok'`, key.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var records int
			for rows.Next() {
				var raw []byte
				if err := rows.Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var record neoroute.RequestRecord
				if err := json.Unmarshal(raw, &record); err != nil {
					t.Fatal(err)
				}
				if record.Asked != vm.Served || record.Decision.AccountID != neopool.AccountID(ids[0]) || record.UserID != member.ID {
					t.Fatalf("socket escaped scoped dispatch: %s", raw)
				}
				records++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if records != 3 {
				t.Fatalf("scoped success records=%d want 3", records)
			}
		})
	}
}
