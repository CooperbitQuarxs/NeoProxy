package neoroute

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoparams"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/tidwall/gjson"
)

func setting(mode string, rules ...ParamRule) ParamSetting {
	return ParamSetting{Mode: mode, Rules: rules}
}
func always(v any) ParamRule { return ParamRule{Value: v} }

// paramState is policyState with a sized model and a "-fast" sibling that
// every account of the provider serves.
func paramState() routeState {
	state := policyState()
	state.snapshot.Models[0].Context = 100000
	state.snapshot.Models = append(state.snapshot.Models, neopool.Model{ID: "primary/model-fast", ProviderID: "primary", Name: "model-fast", Served: "neoproxy/primary/model-fast", Exposed: true})
	for i := range state.snapshot.Accounts {
		if state.snapshot.Accounts[i].ProviderID == "primary" {
			state.snapshot.Accounts[i].ModelIDs = append(state.snapshot.Accounts[i].ModelIDs, "primary/model-fast")
		}
	}
	return state
}

func validateStage(t *testing.T, mutate func(*Stage)) error {
	t.Helper()
	state := paramState()
	mutate(&state.vm.Stages[0])
	return Validate(*state.vm, state.snapshot, neo.Principal{Role: "admin"})
}

func TestParamValidation(t *testing.T) {
	over := Condition{Field: "account.usage", Op: "lt", Value: 0.5}
	pool := Condition{Field: "pool.size", Op: "gt", Value: 1.0}
	for _, test := range []struct {
		name   string
		params map[string]ParamSetting
		follow string
		want   string // "" = valid
	}{
		{"speed by sibling", map[string]ParamSetting{"speed": setting("force", always("fast"))}, "", ""},
		{"context in range", map[string]ParamSetting{"context": setting("force", always(50000.0))}, "", ""},
		{"effort", map[string]ParamSetting{"effort": setting("", always("high"))}, "", ""},
		{"unknown parameter", map[string]ParamSetting{"temperature": setting("force", always(0.5))}, "", "temperature is not a parameter"},
		{"bad mode", map[string]ParamSetting{"effort": setting("sometimes", always("high"))}, "", "mode must be force or default"},
		{"context default", map[string]ParamSetting{"context": setting("default", always(50000.0))}, "", "always enforced"},
		{"efforts default", map[string]ParamSetting{"efforts": setting("default", always([]any{"low"}))}, "", "always enforced"},
		{"context too large", map[string]ParamSetting{"context": setting("force", always(200000.0))}, "", "outside"},
		{"context too small", map[string]ParamSetting{"context": setting("force", always(10.0))}, "", "outside"},
		{"choice value", map[string]ParamSetting{"speed": setting("force", always("ultrafast"))}, "", "not one of"},
		{"effort value", map[string]ParamSetting{"effort": setting("force", always("ultra"))}, "", "ultra"},
		{"set value", map[string]ParamSetting{"efforts": setting("force", always([]any{"low", "turbo"}))}, "", "turbo"},
		{"no rules", map[string]ParamSetting{"effort": {Mode: "force"}}, "", "needs 1..16 rules"},
		{"rule after fallback", map[string]ParamSetting{"effort": setting("force", always("low"), ParamRule{When: &Condition{Field: "request.stream", Op: "eq", Value: true}, Value: "high"})}, "", "can never apply"},
		{"conditional rules", map[string]ParamSetting{"effort": setting("force", ParamRule{When: &Condition{Field: "request.tokens", Op: "gt", Value: 1000.0}, Value: "low"}, always("high"))}, "", ""},
		{"account field", map[string]ParamSetting{"effort": setting("force", ParamRule{When: &over, Value: "low"})}, "", "needs accounts"},
		{"pool field", map[string]ParamSetting{"effort": setting("force", ParamRule{When: &pool, Value: "low"})}, "", "needs accounts"},
		{"invalid condition", map[string]ParamSetting{"effort": setting("force", ParamRule{When: &Condition{Field: "user.age", Op: "gt", Value: 3.0}, Value: "low"})}, "", "unknown field"},
		{"follow matches nothing", nil, "primary/nothing-*", "matches no exposed model"},
		{"follow bad pattern", nil, "primary/[", "invalid pattern"},
		{"follow without provider", nil, "model*", "start with a provider"},
		{"follow valid", nil, "primary/mod*", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateStage(t, func(s *Stage) { s.Params, s.Follow = test.params, test.follow })
			if test.want == "" && err != nil {
				t.Fatalf("valid configuration rejected: %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParamRulesFollowConditions(t *testing.T) {
	state := paramState()
	state.vm.Stages[0].Params = map[string]ParamSetting{"effort": setting("force",
		ParamRule{When: &Condition{Field: "request.tokens", Op: "gt", Value: 100.0}, Value: "low"},
		ParamRule{When: &Condition{Field: "user.role", Op: "eq", Value: "admin"}, Value: "max"},
		always("high"))}
	big := `{"messages":[{"role":"user","content":"` + strings.Repeat("x", 800) + `"}]}`
	for _, test := range []struct {
		name string
		body string
		role string
		want string
	}{
		{"first matching rule wins", big, "member", "low"},
		{"second rule", `{"messages":[]}`, "admin", "max"},
		{"fallback", `{"messages":[]}`, "member", "high"},
		{"earlier rule beats later", big, "admin", "low"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := state
			s.principal.Role = test.role
			d := decide(chatRequest(test.body), s, -1, nil)
			if got := appliedEffort(d.Params); d.Outcome != "ok" || got == nil || *got != test.want {
				t.Fatalf("effort = %v want %q; %+v", got, test.want, d)
			}
		})
	}
	// With no fallback and no match, nothing is applied.
	state.vm.Stages[0].Params = map[string]ParamSetting{"effort": setting("force", ParamRule{When: &Condition{Field: "request.tokens", Op: "gt", Value: 1e9}, Value: "low"})}
	if d := decide(chatRequest(`{}`), state, -1, nil); d.Outcome != "ok" || len(d.Params) != 0 {
		t.Fatalf("unmatched rule applied params: %+v", d.Params)
	}
}

func TestSpeedSiblingSwapForceVersusDefault(t *testing.T) {
	for _, test := range []struct {
		name      string
		mode      string
		body      string
		wantModel string
	}{
		{"force swaps", "force", `{"messages":[]}`, "primary/model-fast"},
		{"force beats the caller's tier", "force", `{"messages":[],"service_tier":"auto"}`, "primary/model-fast"},
		{"default swaps when the caller sent nothing", "default", `{"messages":[]}`, "primary/model-fast"},
		{"default yields to the caller's tier", "default", `{"messages":[],"service_tier":"auto"}`, "primary/model"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := paramState()
			state.vm.Stages[0].Params = map[string]ParamSetting{"speed": setting(test.mode, always("fast"))}
			d := decide(chatRequest(test.body), state, -1, nil)
			if d.Outcome != "ok" || d.Model != test.wantModel {
				t.Fatalf("model = %q want %q; %+v", d.Model, test.wantModel, d)
			}
			swapped := test.wantModel == "primary/model-fast"
			if swapped != (len(d.Params) == 1 && d.Params[0].Via == "model" && strings.Contains(d.Params[0].Realize, "primary/model-fast")) {
				t.Fatalf("params = %+v", d.Params)
			}
			if d.Trail[0].Model != test.wantModel {
				t.Fatalf("trail model = %q", d.Trail[0].Model)
			}
		})
	}
	// The sibling must be served by an account of the stage; otherwise the stage
	// cannot serve and says why.
	state := paramState()
	for i := range state.snapshot.Accounts {
		state.snapshot.Accounts[i].ModelIDs = slices.DeleteFunc(slices.Clone(state.snapshot.Accounts[i].ModelIDs), func(id string) bool { return id == "primary/model-fast" })
	}
	state.vm.Stages[0].Params = map[string]ParamSetting{"speed": setting("force", always("fast"))}
	if d := decide(chatRequest(`{}`), state, -1, nil); d.Outcome == "ok" {
		t.Fatalf("served a sibling no account supports: %+v", d)
	}
}

func TestContextWindowRefusesLargerPrompts(t *testing.T) {
	prompt := func(tokens int) string {
		return `{"messages":[{"role":"user","content":"` + strings.Repeat("x", tokens*4) + `"}]}`
	}
	capped := Stage{ID: "s0", Model: "primary/model", Accounts: AccountScope{All: true}, Draw: "fill", PerAccount: .5, Cap: .8, Window: "day", Per: "user",
		Params: map[string]ParamSetting{"context": setting("force", always(20000.0))}}
	state := paramState()
	state.vm.Stages = []Stage{capped}

	if d := decide(chatRequest(prompt(10000)), state, -1, nil); d.Outcome != "ok" || d.Params[0].ID != "context" || d.Params[0].Via != "window" {
		t.Fatalf("a prompt inside the window was not served: %+v", d)
	}
	d := decide(chatRequest(prompt(30000)), state, -1, nil)
	if d.Outcome != "error" || d.Reason != "context_length_exceeded" || !strings.Contains(d.Message, "20000") {
		t.Fatalf("over-limit prompt: %+v", d)
	}
	var routeErr *Error
	err := decisionError(d)
	if e, ok := err.(*Error); !ok || e.Status != 400 || e.Code != "context_length_exceeded" {
		t.Fatalf("error = %#v", err)
	} else {
		routeErr = e
	}
	if !strings.Contains(routeErr.Error(), "about 300") {
		t.Fatalf("message = %q", routeErr.Error())
	}
	if d.Trail[0].Skipped != "context" {
		t.Fatalf("trail = %+v", d.Trail)
	}

	// A later stage whose window fits takes the prompt instead.
	fallback := Stage{ID: "s1", Model: "primary/model", Accounts: AccountScope{All: true}, Draw: "fill", PerAccount: .5, Cap: .8, Window: "day", Per: "user",
		Params: map[string]ParamSetting{"context": setting("force", always(100000.0))}}
	state.vm.Stages = []Stage{capped, fallback}
	d = decide(chatRequest(prompt(30000)), state, -1, nil)
	if d.Outcome != "ok" || d.StageID != "s1" || d.Trail[0].Skipped != "context" {
		t.Fatalf("fallback stage did not take the prompt: %+v", d)
	}
	// Past every window, the largest one is named.
	d = decide(chatRequest(prompt(150000)), state, -1, nil)
	if d.Reason != "context_length_exceeded" || !strings.Contains(d.Message, "100000") {
		t.Fatalf("decision = %+v", d)
	}
	// A passthrough terminal still gets its say.
	state.vm.Stages = []Stage{capped}
	state.vm.Terminal = Terminal{Kind: "passthrough", Model: "fallback/model"}
	if d = decide(chatRequest(prompt(30000)), state, -1, nil); d.Reason == "context_length_exceeded" {
		t.Fatalf("passthrough terminal ignored: %+v", d)
	}
}

func TestContextWindowExtendsPastTheModelsOwn(t *testing.T) {
	// The model's own window is 100000; an imposed 100000+ window is advertised
	// and enforced as such even though FitContext is off.
	state := paramState()
	state.vm.Stages[0].Params = map[string]ParamSetting{"context": setting("force", always(100000.0))}
	body := `{"messages":[{"role":"user","content":"` + strings.Repeat("x", 4*90000) + `"}]}`
	if d := decide(chatRequest(body), state, -1, nil); d.Outcome != "ok" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestFollowPicksNewestVersion(t *testing.T) {
	snap := neopool.Snapshot{Providers: []neopool.Provider{{ID: "codex", Exposed: true}, {ID: "hidden", Exposed: true}, {ID: "off", Exposed: false}}}
	add := func(provider, name string, exposed bool) {
		snap.Models = append(snap.Models, neopool.Model{ID: provider + "/" + name, ProviderID: provider, Name: name, Exposed: exposed})
	}
	for _, name := range []string{"gpt-5.6-luna", "gpt-6-luna", "gpt-6.1-luna", "gpt-luna-latest", "gpt-6.1-luna-pro", "gpt-6.10-luna", "gpt-6.2-luna"} {
		add("codex", name, true)
	}
	add("codex", "gpt-7-luna", false) // newer but not exposed
	add("off", "gpt-9-luna", true)    // provider not exposed
	add("hidden", "gpt-8-luna", true) // other provider
	for _, test := range []struct {
		pattern string
		want    string
	}{
		{"codex/gpt-*-luna", "codex/gpt-6.10-luna"},
		{"codex/gpt-6-*", "codex/gpt-6-luna"},
		{"codex/gpt-5*", "codex/gpt-5.6-luna"},
		{"codex/gpt-*-luna-pro", "codex/gpt-6.1-luna-pro"},
		{"codex/*", "codex/gpt-6.10-luna"},
	} {
		m, ok := newestMatch(snap, test.pattern)
		if !ok || m.ID != test.want {
			t.Fatalf("%s -> %q (%v), want %q", test.pattern, m.ID, ok, test.want)
		}
	}
	if _, ok := newestMatch(snap, "off/gpt-*"); ok {
		t.Fatal("matched a model of a hidden provider")
	}
	if _, ok := newestMatch(snap, "codex/*/gpt-*"); ok {
		t.Fatal("* must not cross a slash")
	}

	vm := VirtualModel{Stages: []Stage{{ID: "a", Model: "codex/gpt-5.6-luna", Follow: "codex/gpt-*-luna"}, {ID: "b", Model: "codex/gpt-6-luna"}}}
	resolved := resolveFollows(vm, snap)
	if resolved.Stages[0].Model != "codex/gpt-6.10-luna" || resolved.Stages[1].Model != "codex/gpt-6-luna" {
		t.Fatalf("resolved = %+v", resolved.Stages)
	}
	if vm.Stages[0].Model != "codex/gpt-5.6-luna" {
		t.Fatal("resolveFollows modified its input")
	}
	// A pattern that matches nothing keeps the last resolved model.
	vm.Stages[0].Follow = "codex/gone-*"
	if got := resolveFollows(vm, snap).Stages[0].Model; got != "codex/gpt-5.6-luna" {
		t.Fatalf("stale follow = %q", got)
	}
}

func TestFollowServesTheResolvedModel(t *testing.T) {
	state := paramState()
	state.snapshot.Models = append(state.snapshot.Models, neopool.Model{ID: "primary/model-2", ProviderID: "primary", Name: "model-2", Served: "neoproxy/primary/model-2", Exposed: true})
	for i := range state.snapshot.Accounts {
		if state.snapshot.Accounts[i].ProviderID == "primary" {
			state.snapshot.Accounts[i].ModelIDs = append(state.snapshot.Accounts[i].ModelIDs, "primary/model-2")
		}
	}
	vm := resolveFollows(*state.vm, state.snapshot)
	if vm.Stages[0].Model != "primary/model" {
		t.Fatalf("a stage without follow changed: %+v", vm.Stages[0])
	}
	state.vm.Stages[0].Follow = "primary/model*"
	resolved := resolveFollows(*state.vm, state.snapshot)
	state.vm = &resolved
	// model-fast is the sibling, but model-2 has the higher version.
	if d := decide(chatRequest(`{}`), state, -1, nil); d.Outcome != "ok" || d.Model != "primary/model-2" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestServedWindowAndSummary(t *testing.T) {
	state := paramState()
	snap := state.snapshot
	vm := *state.vm
	if got := servedWindow(vm, snap); got != 100000 {
		t.Fatalf("plain window = %d", got)
	}
	vm.Stages = slices.Clone(vm.Stages)
	vm.Stages[0].Params = map[string]ParamSetting{
		"context": setting("force", always(50000.0)),
		"speed":   setting("force", always("fast")),
		"effort":  setting("force", always("high")),
	}
	if got := servedWindow(vm, snap); got != 50000 {
		t.Fatalf("imposed window = %d", got)
	}
	if got := paramSummary(vm, snap); got != "Fast speed, 50k context, high effort" {
		t.Fatalf("summary = %q", got)
	}
	// Conditional windows advertise the widest value, and without a fallback rule
	// the model's own window too.
	vm.Stages[0].Params = map[string]ParamSetting{"context": setting("force", ParamRule{When: &Condition{Field: "user.role", Op: "eq", Value: "admin"}, Value: 30000.0})}
	if got := servedWindow(vm, snap); got != 100000 {
		t.Fatalf("conditional window = %d", got)
	}
	vm.Stages[0].Params = map[string]ParamSetting{"context": setting("force", ParamRule{When: &Condition{Field: "user.role", Op: "eq", Value: "admin"}, Value: 30000.0}, always(20000.0))}
	if got := servedWindow(vm, snap); got != 30000 {
		t.Fatalf("covered conditional window = %d", got)
	}
	if got := paramSummary(vm, snap); got != "" {
		t.Fatalf("a conditional setting is not a fixed property: %q", got)
	}
}

func TestParamsReachSimulateAndModelListing(t *testing.T) {
	f := newRouteFixture(t, "fill")
	ctx := neo.WithPrincipal(context.Background(), f.admin)
	vm, err := f.route.SaveVirtualModel(ctx, VirtualModel{Name: "capped", Description: "Small", Stages: []Stage{{ID: "s0", Model: "primary/model", Accounts: AccountScope{All: true}, Draw: "fill", PerAccount: .5, Cap: .8, Window: "day", Per: "user",
		Params: map[string]ParamSetting{"context": {Rules: []ParamRule{{Value: 10000.0}}}, "effort": {Rules: []ParamRule{{Value: "high"}}}}}}, Terminal: Terminal{Kind: "limit", Message: "x"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := vm.Stages[0].Params["effort"].Mode; got != "force" {
		t.Fatalf("mode was not made explicit: %q", got)
	}
	if _, err = f.identity.UpdateUser(context.Background(), f.user.ID, neo.UserInput{Handle: f.user.Handle, Role: "service", State: "active", Grants: []neo.Grant{{VirtualModel: f.vm.ID, Allotment: 1}, {VirtualModel: vm.ID, Allotment: 1}}}); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(f.ctx)
	})
	router.GET("/v1/models", f.route.ModelsHTTP)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET", "/v1/models", nil))
	var listed struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			Description   string `json:"description"`
		} `json:"data"`
	}
	if err = json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil {
		t.Fatalf("%v: %s", err, recorder.Body)
	}
	found := false
	for _, m := range listed.Data {
		if m.ID == "neoproxy/capped" {
			found = true
			if m.ContextLength != 10000 || m.Description != "Small — 10k context, high effort" {
				t.Fatalf("capped = %+v", m)
			}
		}
	}
	if !found {
		t.Fatalf("capped model is not listed: %s", recorder.Body)
	}

	d, err := f.route.Simulate(f.ctx, Request{Asked: "neoproxy/capped", Protocol: "openai", Body: []byte(`{"messages":[]}`)})
	if err != nil || d.Outcome != "ok" || len(d.Params) != 2 {
		t.Fatalf("simulate = %+v err=%v", d, err)
	}
	raw, _ := json.Marshal(d)
	if !gjson.GetBytes(raw, "params.#(id==\"context\").value").Exists() || !gjson.GetBytes(raw, "trail.0.params").Exists() || gjson.GetBytes(raw, "effort").Exists() {
		t.Fatalf("decision json = %s", raw)
	}
}

func TestModelParamsEndpoint(t *testing.T) {
	f := newRouteFixture(t, "fill")
	router := gin.New()
	principal := f.admin
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(neo.WithPrincipal(c.Request.Context(), principal))
	})
	f.route.RegisterRoutes(router.Group("/v8/neo"))
	get := func(query string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", "/v8/neo/model-params?model="+query, nil))
		return recorder
	}
	recorder := get("primary/model")
	var body struct {
		Model string           `json:"model"`
		Specs []neoparams.Spec `json:"specs"`
	}
	if recorder.Code != 200 || json.Unmarshal(recorder.Body.Bytes(), &body) != nil || body.Model != "primary/model" {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body)
	}
	if _, ok := neoparams.ByID(body.Specs, "effort"); !ok {
		t.Fatalf("specs = %+v", body.Specs)
	}
	if recorder = get("primary/unknown"); recorder.Code != 404 {
		t.Fatalf("unknown model status %d", recorder.Code)
	}
	principal = neo.Principal{UserID: "m", Role: "member", Session: true}
	if recorder = get("primary/model"); recorder.Code != 403 {
		t.Fatalf("member status %d", recorder.Code)
	}
}
