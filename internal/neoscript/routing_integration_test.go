//go:build linux && amd64

package neoscript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Use the actual identity database, engine inventory, metered ledger, HTTP session
// checks and routing store. Synthetic local engine records have no credentials;
// these are policy/simulation tests, not claims of real upstream inference.
type routingFixture struct {
	identity                            *neo.Store
	pools                               *neopool.Store
	routes                              *neoroute.Store
	manager                             *coreauth.Manager
	router                              *gin.Engine
	admin                               neo.Principal
	member                              neo.User
	adminToken, memberToken, ownerToken string
	accounts                            []string
	engineIDs                           map[string]string
}

func realRoutingFixture(t *testing.T) *routingFixture {
	t.Helper()
	ctx := context.Background()
	identity, err := neo.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := identity.Close(); err != nil {
			t.Error(err)
		}
	})
	adminSession, adminToken, err := identity.Bootstrap(ctx, "script-admin", "Script Admin", "local-test-password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := identity.PrincipalForUser(ctx, adminSession.UserID)
	if err != nil {
		t.Fatal(err)
	}
	admin.Session = true
	owner, err := identity.CreateUser(ctx, neo.UserInput{Handle: "script-owner", Name: "Script Owner", Role: neo.RoleOwner, State: "active", Password: "local-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := identity.CreateUser(ctx, neo.UserInput{Handle: "script-member", Name: "Script Member", Role: neo.RoleMember, State: "active", Password: "local-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	_, ownerToken, err := identity.Login(ctx, owner.Handle, "local-test-password")
	if err != nil {
		t.Fatal(err)
	}
	_, memberToken, err := identity.Login(ctx, member.Handle, "local-test-password")
	if err != nil {
		t.Fatal(err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	engineIDs := map[string]string{}
	var accounts []string
	for i := 0; i < 2; i++ {
		engineID := "script-local-" + uuid.NewString()
		if _, err := manager.Register(ctx, &coreauth.Auth{ID: engineID, Provider: "codex", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(engineID, "codex", []*registry.ModelInfo{{ID: "script-model", DisplayName: "Script Model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(engineID) })
		accountID := neopool.AccountID(engineID)
		accounts = append(accounts, accountID)
		engineIDs[accountID] = engineID
	}
	slices.Sort(accounts)
	pools, err := neopool.New(identity.DB(), manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pools.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range accounts {
		if err = pools.AssignOwner(ctx, id, owner.ID); err != nil {
			t.Fatal(err)
		}
		if err = pools.ConfigureWindow(ctx, id, owner.ID, "day", 1000000, time.Now().Add(-time.Hour).UnixMilli(), false); err != nil {
			t.Fatal(err)
		}
	}
	identity.SetAccountScopeValidator(pools.ScopeOwned)
	routes, err := neoroute.New(identity, pools)
	if err != nil {
		t.Fatal(err)
	}
	routes.SetScriptHook(New())
	router := gin.New()
	routes.RegisterRoutes(router.Group("/v8/neo", neo.NewHTTP(identity).SessionMiddleware()))
	return &routingFixture{identity: identity, pools: pools, routes: routes, manager: manager, router: router, admin: admin, member: member, adminToken: adminToken, memberToken: memberToken, ownerToken: ownerToken, accounts: accounts, engineIDs: engineIDs}
}

func (f *routingFixture) model() neoroute.VirtualModel {
	return neoroute.VirtualModel{Name: "scripted", Description: "Local script integration policy", Stages: []neoroute.Stage{{ID: "stage", Model: "codex/script-model", Accounts: neoroute.AccountScope{All: true}, Draw: "fill", PerAccount: 1, Cap: 1, Window: "day", Per: "user"}}, Terminal: neoroute.Terminal{Kind: "limit", Message: "meter exhausted"}, Script: fmt.Sprintf(`if input.request.input=="prefer-second" then return {stageId="stage",accountId=%q} end; return {stageId="stage",accountId=%q}`, f.accounts[1], f.accounts[0])}
}
func (f *routingFixture) grant(t *testing.T, modelID string, enabled bool) {
	t.Helper()
	var grants []neo.Grant
	if enabled {
		grants = []neo.Grant{{VirtualModel: modelID, Allotment: 1}}
	}
	user, err := f.identity.UpdateUser(context.Background(), f.member.ID, neo.UserInput{Handle: f.member.Handle, Name: f.member.Name, Role: f.member.Role, State: f.member.State, Grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	f.member = user
}
func (f *routingFixture) memberContext(t *testing.T) context.Context {
	t.Helper()
	principal, err := f.identity.PrincipalForUser(context.Background(), f.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	return neo.WithPrincipal(context.Background(), principal)
}
func (f *routingFixture) request(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, "http://localhost/v8/neo"+path, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Neoproxy-CSRF", "1")
	if token != "" {
		request.AddCookie(&http.Cookie{Name: neo.SessionCookie, Value: token})
	}
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}
func (f *routingFixture) save(t *testing.T, vm neoroute.VirtualModel, create bool) neoroute.VirtualModel {
	t.Helper()
	vm, err := f.routes.SaveVirtualModel(neo.WithPrincipal(context.Background(), f.admin), vm, create)
	if err != nil {
		t.Fatal(err)
	}
	return vm
}

func TestPersistedLuaIsAdminOnlyAndHTTPReportsRealErrors(t *testing.T) {
	f := realRoutingFixture(t)
	vm := f.model()
	response := f.request(t, http.MethodPost, "/virtual-models", f.adminToken, vm)
	if response.Code != 201 {
		t.Fatalf("admin save: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &vm); err != nil {
		t.Fatal(err)
	}
	f.grant(t, vm.ID, true)
	loaded, err := f.routes.VirtualModel(context.Background(), vm.ID)
	if err != nil || loaded.Script != vm.Script {
		t.Fatalf("source not persisted: %+v %v", loaded, err)
	}
	for _, token := range []string{f.memberToken, f.ownerToken} {
		modified := vm
		modified.Script = `error("unauthorized replacement")`
		response = f.request(t, http.MethodPut, "/virtual-models/"+vm.ID, token, modified)
		if response.Code != 403 {
			t.Fatalf("non-admin script save: %d %s", response.Code, response.Body.String())
		}
	}
	response = f.request(t, http.MethodPut, "/virtual-models/"+vm.ID, "", vm)
	if response.Code != 401 {
		t.Fatalf("anonymous source save = %d", response.Code)
	}
	response = f.request(t, http.MethodGet, "/virtual-models/"+vm.ID, f.memberToken, nil)
	if response.Code != 200 || strings.Contains(response.Body.String(), "prefer-second") {
		t.Fatalf("member source redaction: %d %s", response.Code, response.Body.String())
	}
	invalid := vm
	invalid.Script = `return {`
	response = f.request(t, http.MethodPut, "/virtual-models/"+vm.ID, f.adminToken, invalid)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "syntax") {
		t.Fatalf("syntax error hidden: %d %s", response.Code, response.Body.String())
	}
	loaded, err = f.routes.VirtualModel(context.Background(), vm.ID)
	if err != nil || loaded.Script != vm.Script {
		t.Fatal("invalid save changed persisted source", err)
	}
	response = f.request(t, http.MethodPost, "/simulate", f.adminToken, map[string]any{"asked": vm.Served, "userId": f.member.ID, "protocol": "openai-response", "request": map[string]any{"model": vm.Served, "input": "prefer-second"}})
	if response.Code != 200 {
		t.Fatalf("payload simulation: %d %s", response.Code, response.Body.String())
	}
	var decision neoroute.Decision
	if err = json.Unmarshal(response.Body.Bytes(), &decision); err != nil {
		t.Fatal(err)
	}
	if decision.AccountID != f.accounts[1] {
		t.Fatalf("simulation dropped business payload: %+v", decision)
	}
	for _, source := range []string{`error("deliberate route-policy failure")`, `return {stageId="stage",accountId="forbidden-account"}`} {
		modified := vm
		modified.Script = source
		modified = f.save(t, modified, false)
		response = f.request(t, http.MethodPost, "/simulate", f.adminToken, map[string]any{"asked": vm.Served, "userId": f.member.ID, "protocol": "openai-response", "request": map[string]any{"input": "prefer-second"}})
		if response.Code < 400 || !strings.Contains(response.Body.String(), "neoproxy_script") {
			t.Fatalf("script failure was not honestly typed: %d %s", response.Code, response.Body.String())
		}
		diagnostic := "deliberate route-policy failure"
		if strings.Contains(source, "forbidden-account") {
			diagnostic = "invalid script output"
		}
		if !strings.Contains(response.Body.String(), diagnostic) {
			t.Fatalf("script diagnostic hidden: %s", response.Body.String())
		}
	}
}

func TestRealRoutingScriptCannotWidenScopeHealthExposureOrBudget(t *testing.T) {
	f := realRoutingFixture(t)
	vm := f.save(t, f.model(), true)
	f.grant(t, vm.ID, true)
	request := neoroute.Request{Asked: vm.Served, Protocol: "openai-response", Body: []byte(`{"input":"prefer-second"}`)}
	decision, err := f.routes.Simulate(f.memberContext(t), request)
	if err != nil || decision.AccountID != f.accounts[1] {
		t.Fatalf("real Lua route = %+v %v", decision, err)
	}
	// A narrower live inference key must not be widened by an administrator script.
	_, secret, err := f.identity.MintKey(context.Background(), f.admin, f.member.ID, "narrow", []string{f.accounts[0]}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := f.identity.AuthenticateKey(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	decision, err = f.routes.Simulate(neo.WithPrincipal(context.Background(), principal), request)
	if err == nil && decision.Outcome == "ok" {
		t.Fatalf("script widened key scope: %+v", decision)
	}
	f.grant(t, vm.ID, false)
	decision, err = f.routes.Simulate(f.memberContext(t), request)
	if err != nil || decision.Reason != "not-granted" {
		t.Fatalf("script bypassed grant removal: %+v %v", decision, err)
	}
	f.grant(t, vm.ID, true)
	if err = f.pools.SetExposure(context.Background(), "codex", false, true); err != nil {
		t.Fatal(err)
	}
	decision, err = f.routes.Simulate(f.memberContext(t), request)
	if err != nil || decision.Outcome != "limited" {
		t.Fatalf("hidden provider bypassed or terminal lost: %+v %v", decision, err)
	}
	if err = f.pools.SetExposure(context.Background(), "codex", true, true); err != nil {
		t.Fatal(err)
	}
	unhealthy, ok := f.manager.GetByID(f.engineIDs[f.accounts[1]])
	if !ok {
		t.Fatal("missing synthetic engine account")
	}
	unhealthy.Disabled = true
	if _, err = f.manager.Update(context.Background(), unhealthy); err != nil {
		t.Fatal(err)
	}
	decision, err = f.routes.Simulate(f.memberContext(t), request)
	if err == nil && decision.Outcome == "ok" {
		t.Fatalf("script selected disabled account: %+v", decision)
	}
	unhealthy.Disabled = false
	if _, err = f.manager.Update(context.Background(), unhealthy); err != nil {
		t.Fatal(err)
	}
	reservation := neopool.Reservation{ID: uuid.NewString(), RequestID: uuid.NewString(), UserID: f.member.ID, VirtualModelID: vm.ID, StageID: "stage", AccountID: f.accounts[1], AccountIDs: f.accounts, Window: "day", Tokens: 100000, PerAccount: 1, Cap: 1}
	if err = f.pools.Reserve(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	if err = f.pools.Settle(context.Background(), neopool.Settlement{ID: reservation.ID, AccountID: reservation.AccountID, InputTokens: 100000, UsageKnown: true}); err != nil {
		t.Fatal(err)
	}
	vm.Stages[0].Cap = 0.1
	vm = f.save(t, vm, false)
	decision, err = f.routes.Simulate(f.memberContext(t), request)
	if err != nil || decision.Outcome != "limited" {
		t.Fatalf("script bypassed real settled ledger cap: %+v %v", decision, err)
	}
}

func TestConfiguredMissingEvaluatorFailsButUnscriptedPathRemains(t *testing.T) {
	f := realRoutingFixture(t)
	vm := f.save(t, f.model(), true)
	f.grant(t, vm.ID, true)
	f.routes.SetScriptHook(nil)
	request := neoroute.Request{Asked: vm.Served, Protocol: "openai-response", Body: []byte(`{"input":"prefer-second"}`)}
	if _, err := f.routes.Simulate(f.memberContext(t), request); err == nil || !strings.Contains(err.Error(), "evaluator unavailable") {
		t.Fatalf("configured script silently ignored: %v", err)
	}
	vm.Script = ""
	vm = f.save(t, vm, false)
	decision, err := f.routes.Simulate(f.memberContext(t), request)
	if err != nil || decision.AccountID != f.accounts[0] {
		t.Fatalf("unscripted prior fill path changed: %+v %v", decision, err)
	}
}

func TestRealExecutionSeamHonorsLuaAndPersistsFailedAttempt(t *testing.T) {
	f := realRoutingFixture(t)
	vm := f.save(t, f.model(), true)
	f.grant(t, vm.ID, true)
	request := neoroute.Request{Asked: vm.Served, Protocol: "openai-response", Body: []byte(`{"input":"prefer-second","max_output_tokens":32}`)}
	attempts := 0
	injected := errors.New("deliberate upstream failure before any output")
	_, err := f.routes.Run(f.memberContext(t), request, func(ctx context.Context, selection neoroute.Selection) (*neoroute.Execution, error) {
		attempts++
		if selection.AuthID != f.engineIDs[f.accounts[1]] || selection.Decision.AccountID != f.accounts[1] || selection.Decision.StageID != "stage" || selection.Asked != vm.Served {
			t.Fatalf("actual execution pin ignored Lua proposal: %+v", selection)
		}
		return nil, injected
	})
	if err == nil || attempts != 1 {
		t.Fatalf("deliberate failed dispatch = attempts %d, error %v", attempts, err)
	}
	usage, err := f.pools.Usage(context.Background(), f.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0].AccountID != f.accounts[1] || usage[0].Tokens <= 0 || usage[0].Reserved != 0 {
		t.Fatalf("dispatched failure was not truthfully settled: %+v", usage)
	}
	var raw []byte
	if err = f.identity.DB().QueryRow(`SELECT body FROM neoroute_requests WHERE user_id=?`, f.member.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record neoroute.RequestRecord
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Asked != vm.Served || record.Outcome == "ok" {
		t.Fatalf("failed scripted execution misreported: %+v", record)
	}
	found := false
	for _, step := range record.Decision.Trail {
		if step.StageID == "stage" && step.Chosen == f.accounts[1] {
			found = true
		}
	}
	if !found {
		t.Fatalf("persisted explanation lost actual scripted attempt: %+v", record.Decision)
	}
}
