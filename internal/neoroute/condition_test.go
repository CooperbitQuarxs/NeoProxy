package neoroute

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

func leaf(field, op string, value any) Condition {
	return Condition{Field: field, Op: op, Value: value}
}

func conditionState(c Condition) routeState {
	state := policyState()
	state.vm.Stages[0].Condition = &c
	state.snapshot.Accounts[0].Label = "Alpha"
	state.snapshot.Accounts[1].Label = "Beta"
	state.snapshot.Accounts[1].Plan = "max"
	return state
}

func chatRequest(body string) Request {
	return Request{Asked: "neoproxy/route", Protocol: "openai", Body: []byte(body)}
}

func TestConditionRequestFields(t *testing.T) {
	plain := chatRequest(`{"messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
	for _, test := range []struct {
		name      string
		condition Condition
		req       Request
		serves    bool
	}{
		{"all true", Condition{All: []Condition{leaf("user.role", "eq", "member"), leaf("request.stream", "eq", false), leaf("request.protocol", "in", []any{"openai", "claude"})}}, plain, true},
		{"all one false", Condition{All: []Condition{leaf("user.role", "eq", "member"), leaf("request.stream", "eq", true)}}, plain, false},
		{"any", Condition{Any: []Condition{leaf("user.id", "eq", "someone-else"), leaf("request.model", "eq", "neoproxy/route")}}, plain, true},
		{"any none", Condition{Any: []Condition{leaf("user.id", "eq", "someone-else"), leaf("request.tokens", "gt", 1000)}}, plain, false},
		{"not", Condition{Not: &Condition{Field: "request.protocol", Op: "eq", Value: "openai"}}, plain, false},
		{"tokens", leaf("request.tokens", "lte", 30), plain, true},
		{"missing field compares false", leaf("request.effort", "neq", "high"), plain, false},
		{"missing field not exists", leaf("request.effort", "exists", false), plain, true},
		{"missing handle", leaf("user.handle", "exists", nil), plain, false},
		{"nin", leaf("user.role", "nin", []any{"admin", "owner"}), plain, true},
		{"effort", leaf("request.effort", "eq", "high"), chatRequest(`{"reasoning_effort":"high","messages":[]}`), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := decide(test.req, conditionState(test.condition), -1, nil)
			if test.serves {
				if d.Outcome != "ok" || d.AccountID != "a" || d.Trail[0].Window != "day" {
					t.Fatalf("decision=%+v", d)
				}
				return
			}
			if d.Outcome != "limited" || d.Trail[0].Skipped != "condition" || len(d.Trail[0].Candidates) != 0 {
				t.Fatalf("decision=%+v", d)
			}
		})
	}
}

func TestConditionAccountFields(t *testing.T) {
	state := conditionState(Condition{All: []Condition{leaf("user.role", "eq", "member"), leaf("account.label", "eq", "Beta")}})
	d := decide(chatRequest(`{}`), state, -1, nil)
	if d.Outcome != "ok" || d.AccountID != "b" {
		t.Fatalf("decision=%+v", d)
	}
	got := d.Trail[0].Candidates
	if len(got) != 2 || got[0].AccountID != "b" || got[0].Reason != "" || got[1].AccountID != "a" || got[1].Reason != "condition" {
		t.Fatalf("candidates=%+v", got)
	}
	raw, _ := json.Marshal(d.Trail[0])
	if !strings.Contains(string(raw), `"reason":"condition"`) || !strings.Contains(string(raw), `"window":"day"`) {
		t.Fatalf("trail json=%s", raw)
	}

	// Usage of the stage window: a is at 40%, b at 10%.
	state = conditionState(leaf("account.usage", "lt", .3))
	addUse(&state, "u", "a", 4000)
	addUse(&state, "u", "b", 1000)
	if d = decide(chatRequest(`{}`), state, -1, nil); d.AccountID != "b" {
		t.Fatalf("decision=%+v", d)
	}
	// No weekly meter: absent, so comparisons fail and exists is false.
	state = conditionState(leaf("account.usage.week", "lt", 1))
	if d = decide(chatRequest(`{}`), state, -1, nil); d.Outcome != "limited" || d.Trail[0].Skipped != "condition" || len(d.Trail[0].Candidates) != 2 {
		t.Fatalf("decision=%+v", d)
	}
	state = conditionState(Condition{Any: []Condition{leaf("account.plan", "exists", true), leaf("account.usage.day", "exists", false)}})
	if d = decide(chatRequest(`{}`), state, -1, nil); d.AccountID != "b" {
		t.Fatalf("decision=%+v", d)
	}
	// A provider-reported weekly window becomes readable per account.
	state = conditionState(leaf("account.usage.week", "gte", .5))
	state.snapshot.Accounts[1].ProviderWindows = []neopool.Window{{Kind: "week", Known: true, Used: .75}}
	if d = decide(chatRequest(`{}`), state, -1, nil); d.AccountID != "b" {
		t.Fatalf("decision=%+v", d)
	}
	state = conditionState(Condition{All: []Condition{leaf("account.metered", "eq", true), leaf("account.provider", "eq", "primary"), leaf("account.state", "eq", "healthy")}})
	state.snapshot.Accounts[0].Windows = nil
	if d = decide(chatRequest(`{}`), state, -1, nil); d.AccountID != "b" {
		t.Fatalf("decision=%+v", d)
	}
}

func TestConditionPoolFields(t *testing.T) {
	state := conditionState(Condition{All: []Condition{leaf("pool.size", "eq", 2), leaf("pool.average", "gte", .25)}})
	addUse(&state, "u", "a", 4000)
	addUse(&state, "u", "b", 1000)
	if d := decide(chatRequest(`{}`), state, -1, nil); d.Outcome != "ok" {
		t.Fatalf("decision=%+v", d)
	}
	state = conditionState(leaf("pool.average.week", "exists", nil))
	if d := decide(chatRequest(`{}`), state, -1, nil); d.Trail[0].Skipped != "condition" {
		t.Fatalf("decision=%+v", d)
	}
	state = conditionState(leaf("pool.average.day", "eq", 0))
	if d := decide(chatRequest(`{}`), state, -1, nil); d.Outcome != "ok" {
		t.Fatalf("decision=%+v", d)
	}
}

func TestConditionMatchesPrompt(t *testing.T) {
	condition := leaf("request.prompt", "matches", `(?i)\brefactor\b`)
	for _, test := range []struct {
		name   string
		req    Request
		serves bool
	}{
		{"chat string", chatRequest(`{"messages":[{"role":"user","content":"Please REFACTOR this"},{"role":"assistant","content":"ok"}]}`), true},
		{"chat last user wins", chatRequest(`{"messages":[{"role":"user","content":"refactor"},{"role":"user","content":"never mind"}]}`), false},
		{"responses string", Request{Asked: "neoproxy/route", Protocol: "openai-response", Body: []byte(`{"input":"refactor it"}`)}, true},
		{"responses items", Request{Asked: "neoproxy/route", Protocol: "openai-response", Body: []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"go refactor"}]}]}`)}, true},
		{"claude skips tool results", Request{Asked: "neoproxy/route", Protocol: "claude", Body: []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"refactor x"}]},{"role":"assistant","content":"…"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"done"}]}]}`)}, true},
		{"no user text", chatRequest(`{"messages":[{"role":"system","content":"refactor"}]}`), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := decide(test.req, conditionState(condition), -1, nil)
			if (d.Outcome == "ok") != test.serves {
				t.Fatalf("decision=%+v", d)
			}
		})
	}
	if d := decide(chatRequest(`{"messages":[{"role":"user","content":"write a refactoring plan"}]}`), conditionState(leaf("request.prompt", "contains", "refactoring")), -1, nil); d.Outcome != "ok" {
		t.Fatalf("contains: %+v", d)
	}
}

func TestConditionTimeFieldsUseInjectedClock(t *testing.T) {
	condition := Condition{All: []Condition{leaf("time.weekday", "in", []any{"saturday", "sunday"}), leaf("time.hour", "gte", 22), leaf("time.date", "gte", "2026-11-01"), leaf("time.date", "lt", "2026-12-01")}}
	state := conditionState(condition)
	state.now = time.Date(2026, 11, 7, 23, 30, 0, 0, time.UTC) // Saturday
	if d := decide(chatRequest(`{}`), state, -1, nil); d.Outcome != "ok" {
		t.Fatalf("decision=%+v", d)
	}
	state.now = time.Date(2026, 11, 9, 23, 30, 0, 0, time.UTC) // Monday
	if d := decide(chatRequest(`{}`), state, -1, nil); d.Trail[0].Skipped != "condition" {
		t.Fatalf("decision=%+v", d)
	}
	state.now = time.Date(2026, 11, 8, 21, 59, 0, 0, time.UTC) // Sunday, too early
	if d := decide(chatRequest(`{}`), state, -1, nil); d.Trail[0].Skipped != "condition" {
		t.Fatalf("decision=%+v", d)
	}
}

// The store's clock and the caller's handle reach conditions in Simulate.
func TestConditionStoreClockAndHandle(t *testing.T) {
	f := newRouteFixture(t, "fill")
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.Local) // Saturday
	f.route.now = func() time.Time { return clock }
	f.vm.Stages[0].Condition = &Condition{All: []Condition{leaf("user.handle", "eq", "client"), leaf("time.weekday", "eq", "saturday")}}
	if _, err := f.route.SaveVirtualModel(neo.WithPrincipal(f.ctx, f.admin), f.vm, false); err != nil {
		t.Fatal(err)
	}
	d, err := f.route.Simulate(f.ctx, boundedRequest(false))
	if err != nil || d.Outcome != "ok" {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
	clock = clock.Add(24 * time.Hour)
	d, err = f.route.Simulate(f.ctx, boundedRequest(false))
	if err != nil || d.Outcome != "limited" || d.Trail[0].Skipped != "condition" {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
}

func effortSetting(mode string, values ...string) map[string]ParamSetting {
	out := map[string]ParamSetting{}
	if len(values) == 1 {
		out["effort"] = ParamSetting{Mode: mode, Rules: []ParamRule{{Value: values[0]}}}
		return out
	}
	list := make([]any, len(values))
	for i, v := range values {
		list[i] = v
	}
	out["efforts"] = ParamSetting{Mode: "force", Rules: []ParamRule{{Value: list}}}
	return out
}

func TestStageEfforts(t *testing.T) {
	for _, test := range []struct {
		name   string
		params map[string]ParamSetting
		body   string
		want   string // "" means pass through (no override)
	}{
		{"no params", nil, `{"reasoning_effort":"medium"}`, ""},
		{"caller allowed", effortSetting("force", "low", "high"), `{"reasoning_effort":"low"}`, ""},
		{"caller outside set clamps to highest", effortSetting("force", "low", "high", "medium"), `{"reasoning_effort":"max"}`, "high"},
		{"caller absent uses highest", effortSetting("force", "medium", "xhigh"), `{}`, "xhigh"},
		{"caller disabled thinking", effortSetting("force", "minimal", "low"), `{"reasoning_effort":"none"}`, "low"},
		{"force effort alone", effortSetting("force", "high"), `{}`, "high"},
		{"force effort beats caller", effortSetting("force", "low"), `{"reasoning_effort":"high"}`, "low"},
		{"default effort yields to caller", effortSetting("default", "low"), `{"reasoning_effort":"high"}`, ""},
		{"default effort fills a gap", effortSetting("default", "low"), `{}`, "low"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := policyState()
			state.vm.Stages[0].Params = test.params
			d := decide(chatRequest(test.body), state, -1, nil)
			got := appliedEffort(d.Params)
			if d.Outcome != "ok" || (got == nil) != (test.want == "") || (got != nil && *got != test.want) {
				t.Fatalf("decision=%+v effort=%v want %q", d, got, test.want)
			}
		})
	}
}

func TestConditionAndEffortValidation(t *testing.T) {
	deep := leaf("user.role", "eq", "member")
	for range conditionMaxDepth {
		inner := deep
		deep = Condition{Not: &inner}
	}
	many := Condition{}
	for range conditionMaxLeaves + 1 {
		many.Any = append(many.Any, leaf("user.role", "eq", "member"))
	}
	for _, test := range []struct {
		name   string
		change func(*Stage)
		want   string
	}{
		{"unknown field", func(s *Stage) { c := leaf("user.age", "gt", 3); s.Condition = &c }, `unknown field "user.age"`},
		{"unknown op", func(s *Stage) { c := leaf("user.role", "like", "x"); s.Condition = &c }, `unknown op "like"`},
		{"bad regex", func(s *Stage) { c := leaf("request.prompt", "matches", "("); s.Condition = &c }, "invalid regular expression"},
		{"too deep", func(s *Stage) { s.Condition = &deep }, "deeper than 8"},
		{"too many leaves", func(s *Stage) { s.Condition = &many }, "more than 64"},
		{"value kind", func(s *Stage) { c := leaf("request.tokens", "gt", "many"); s.Condition = &c }, "needs a number value"},
		{"op kind", func(s *Stage) { c := leaf("request.stream", "gt", true); s.Condition = &c }, `op "gt" does not apply`},
		{"list value", func(s *Stage) { c := leaf("user.role", "in", "admin"); s.Condition = &c }, "needs a list"},
		{"empty group", func(s *Stage) { s.Condition = &Condition{All: []Condition{}} }, `"all" group must not be empty`},
		{"two forms", func(s *Stage) {
			s.Condition = &Condition{Any: []Condition{leaf("user.role", "eq", "x")}, Field: "user.id", Op: "eq", Value: "y"}
		}, "exactly one of"},
		{"exists value", func(s *Stage) { c := leaf("user.role", "exists", "yes"); s.Condition = &c }, "exists takes"},
		{"effort set", func(s *Stage) { s.Params = effortSetting("force", "high", "turbo") }, "turbo"},
		{"effort duplicate", func(s *Stage) { s.Params = effortSetting("force", "high", "high") }, "twice"},
		{"forced effort", func(s *Stage) { s.Params = effortSetting("force", "ultra") }, "ultra"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := policyState()
			test.change(&state.vm.Stages[0])
			err := Validate(*state.vm, state.snapshot, neo.Principal{Role: "admin"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want %q", err, test.want)
			}
		})
	}
	// A stored condition round-trips through JSON and stays valid.
	var st Stage
	raw := `{"id":"s0","model":"primary/model","accounts":"all","draw":"fill","perAccount":0.5,"cap":0.8,"window":"day","per":"user","params":{"efforts":{"mode":"force","rules":[{"value":["low","xhigh"]}]}},
		"condition":{"all":[{"field":"request.stream","op":"eq","value":false},{"not":{"field":"request.prompt","op":"matches","value":"(?i)urgent"}},{"any":[{"field":"account.usage","op":"lt","value":0.9},{"field":"account.metered","op":"eq","value":false}]}]}}`
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatal(err)
	}
	state := policyState()
	state.vm.Stages[0] = st
	if err := Validate(*state.vm, state.snapshot, neo.Principal{Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if d := decide(chatRequest(`{"messages":[{"role":"user","content":"hi"}]}`), state, -1, nil); d.Outcome != "ok" || appliedEffort(d.Params) == nil || *appliedEffort(d.Params) != "xhigh" {
		t.Fatalf("decision=%+v", d)
	}
	again, _ := json.Marshal(st)
	var back Stage
	if err := json.Unmarshal(again, &back); err != nil || validateCondition(back.Condition) != nil {
		t.Fatalf("round trip %s: %v", again, err)
	}
}

func TestConditionCatalogueEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(neo.WithPrincipal(c.Request.Context(), neo.Principal{UserID: "u", Role: "member", Session: true}))
	})
	(&Store{}).RegisterRoutes(router.Group("/v8/neo"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET", "/v8/neo/virtual-models/fields", nil))
	var body struct {
		Fields []ConditionField `json:"fields"`
		Ops    []ConditionOp    `json:"ops"`
	}
	if recorder.Code != 200 || json.Unmarshal(recorder.Body.Bytes(), &body) != nil || len(body.Fields) != len(ConditionFields) || len(body.Ops) != 11 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	scope := &conditionScope{facts: &requestFacts{req: Request{Protocol: "openai"}}, state: &routeState{handle: "h", principal: neo.Principal{UserID: "u", Role: "member"}}, stage: &Stage{Window: "day"}, pool: &poolFacts{}, account: &neopool.Account{Plan: "p"}}
	for _, field := range ConditionFields {
		if field.Kind == "" || (field.Scope != "request" && field.Scope != "account") || field.Doc == "" {
			t.Fatalf("incomplete field %+v", field)
		}
		if _, ok := scope.lookup(field.Name); !ok && !strings.Contains(field.Doc, "absent") {
			t.Fatalf("field %q is never resolved", field.Name)
		}
	}
}
