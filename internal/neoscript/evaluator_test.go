//go:build linux && amd64

package neoscript

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
)

func TestMain(m *testing.M) {
	if handled, code := WorkerMain(); handled {
		os.Exit(code)
	}
	if len(os.Args) == 3 && os.Args[1] == "--test-script-confinement" {
		os.Exit(confinementProbe(os.Args[2]))
	}
	os.Exit(m.Run())
}

func exampleInput(source string) neoroute.ScriptInput {
	return neoroute.ScriptInput{Source: source, Asked: "neoproxy/heavy", Protocol: "responses", UserID: "user-member", VirtualModel: neoroute.VirtualModel{ID: "vm-heavy", Name: "heavy", Script: source}, Stages: []neoroute.ScriptStage{
		{StageID: "stage-spent", Model: "codex/first", Skipped: "cap", Total: 0.8, Cap: 0.8},
		{StageID: "stage-second", Model: "codex/second", Total: 0.2, Cap: 0.8, Candidates: []neoroute.Candidate{{AccountID: "account-a", Used: 0.18, Budget: 0.4}, {AccountID: "account-b", Used: 0.02, Budget: 0.4}}},
	}}
}

func TestDecisionReadsRequestAndUsage(t *testing.T) {
	evaluator := New()
	input := exampleInput(`
 assert(input.asked == "neoproxy/heavy" and input.protocol == "responses")
 assert(input.userId == "user-member" and input.virtualModel.script == nil)
 assert(#input.stages == 2 and input.stages[1].skipped == "cap")
 local best, used = nil, math.huge
 for _, candidate in ipairs(input.stages[2].candidates) do
   if candidate.used < used then best, used = candidate.accountId, candidate.used end
 end
 return {stageId=input.stages[2].stageId, accountId=best}
 `)
	decision, err := evaluator.Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.StageID != "stage-second" || decision.AccountID != "account-b" {
		t.Fatalf("least-used decision = %+v", decision)
	}
	// A child never receives a pointer back into authoritative caller state.
	if input.Stages[1].Candidates[0].Used != 0.18 || input.VirtualModel.Script != input.Source {
		t.Fatal("script altered caller snapshot")
	}
}

func TestBusinessPayloadChangesDecisionAcrossProtocols(t *testing.T) {
	evaluator := New()
	source := `
 assert(input.authorization==nil and input.headers==nil and input.cookies==nil)
 local text
 if input.protocol=="openai-response" then text=input.request.input
 else text=input.request.messages[1].content end
 local chosen="account-a"
 if string.find(text,"prefer-b",1,true) then chosen="account-b" end
 return {stageId="stage-second",accountId=chosen}
 `
	for _, protocol := range []string{"openai", "openai-response", "claude"} {
		t.Run(protocol, func(t *testing.T) {
			for _, text := range []string{"prefer-b", "ordinary question"} {
				input := exampleInput(source)
				input.Protocol = protocol
				var body any = map[string]any{"model": "neoproxy/heavy", "messages": []any{map[string]any{"role": "user", "content": text}}, "max_tokens": 64}
				if protocol == "openai-response" {
					body = map[string]any{"model": "neoproxy/heavy", "input": text, "max_output_tokens": 64}
				}
				input.Request, _ = json.Marshal(body)
				decision, err := evaluator.Evaluate(context.Background(), input)
				if err != nil {
					t.Fatal(err)
				}
				want := "account-a"
				if text == "prefer-b" {
					want = "account-b"
				}
				if decision.AccountID != want {
					t.Fatalf("payload-dependent %s route = %+v, want %s", protocol, decision, want)
				}
			}
		})
	}
	input := exampleInput(`input.request.messages[1].content="changed"; return {stageId="stage-second",accountId="account-b"}`)
	input.Request = json.RawMessage(`{"messages":[{"role":"user","content":"immutable"}]}`)
	if _, err := evaluator.Evaluate(context.Background(), input); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("request body was mutable: %v", err)
	}
	if string(input.Request) != `{"messages":[{"role":"user","content":"immutable"}]}` {
		t.Fatal("authoritative request mutated")
	}
}

func TestDecisionReadsHealthWithoutInventingUnknownQuota(t *testing.T) {
	evaluator := New()
	input := exampleInput(`
 for _, account in ipairs(input.accounts) do
   assert(account.ownerId==nil and account.credentials==nil and account.authId==nil)
   if account.state=="healthy" and account.windows[1].known and account.windows[1].used < 0.9 then
     return {stageId="stage-second",accountId=account.accountId}
   end
 end
 error("no measured healthy account")
 `)
	input.Accounts = []neoroute.ScriptAccount{
		{AccountID: "account-a", ProviderID: "codex", State: "healthy", Windows: []neoroute.ScriptWindow{{Kind: "day", Known: false}}},
		{AccountID: "account-b", ProviderID: "codex", State: "healthy", Windows: []neoroute.ScriptWindow{{Kind: "day", Known: true, Used: 0.3, ResetsAt: 123456}}},
	}
	decision, err := evaluator.Evaluate(context.Background(), input)
	if err != nil || decision.AccountID != "account-b" {
		t.Fatalf("unknown quota was treated as unused: %+v %v", decision, err)
	}
	input.Accounts[0].Windows[0].Known = true
	input.Accounts[0].Windows[0].Used = 0.2
	input.Accounts[1].State = "cooldown"
	decision, err = evaluator.Evaluate(context.Background(), input)
	if err != nil || decision.AccountID != "account-a" {
		t.Fatalf("health/measurement change ignored: %+v %v", decision, err)
	}
	input.Accounts[0].State = "disabled"
	if _, err = evaluator.Evaluate(context.Background(), input); err == nil || !strings.Contains(err.Error(), "no measured healthy account") {
		t.Fatalf("unhealthy accounts used silently: %v", err)
	}
}

func TestReadOnlyNestedInputAndSafeTableHelpers(t *testing.T) {
	evaluator := New()
	for _, mutation := range []string{
		`input.asked="neoproxy/direct"`,
		`input.stages[2].candidates[1].used=0`,
		`input.stages[3]={}`,
		`table.insert(input.stages,{})`,
		`table.remove(input.stages,1)`,
		`table.sort(input.stages)`,
	} {
		t.Run(mutation, func(t *testing.T) {
			_, err := evaluator.Evaluate(context.Background(), exampleInput(mutation+`; return {stageId="stage-second",accountId="account-a"}`))
			if err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("mutation error = %v", err)
			}
		})
	}
	decision, err := evaluator.Evaluate(context.Background(), exampleInput(`
 assert(table.getn(input.stages)==2)
 local localData={"b","a"}; table.sort(localData)
 assert(table.concat(localData, "-")=="a-b")
 local n=0; for k,v in pairs(input.stages[2]) do n=n+1 end
 assert(n >= 4)
 return {stageId="stage-second",accountId="account-a"}
 `))
	if err != nil || decision.AccountID != "account-a" {
		t.Fatalf("pure helpers failed: %+v %v", decision, err)
	}
}

func TestHostCapabilitiesAbsent(t *testing.T) {
	_, err := New().Evaluate(context.Background(), exampleInput(`
 assert(os==nil and io==nil and package==nil and debug==nil and coroutine==nil and channel==nil)
 assert(require==nil and dofile==nil and loadfile==nil and load==nil and loadstring==nil)
 assert(rawset==nil and rawget==nil and setmetatable==nil and getmetatable==nil)
 assert(getfenv==nil and setfenv==nil and newproxy==nil and collectgarbage==nil and print==nil)
 assert(string.dump==nil and math.random==nil and math.randomseed==nil)
 return {stageId="stage-second",accountId="account-b"}
 `))
	if err != nil {
		t.Fatal(err)
	}
}

func TestInvalidDecisionCannotSelectOutsideAuthorizedSnapshot(t *testing.T) {
	evaluator := New()
	for _, source := range []string{
		`return {stageId="stage-spent",accountId="account-a"}`,
		`return {stageId="stage-second",accountId="another-owners-account"}`,
		`return {stageId="unknown-stage",accountId="account-a"}`,
		`return {stageId="stage-second",accountId="account-a",budget=100}`,
		`return {stageId="stage-second",accountId="account-a",model="codex/forbidden"}`,
		`return {stageId=2,accountId="account-a"}`,
		`return {stageId="stage-second",accountId=""}`,
		`return {stageId="stage-second",accountId=function() end}`,
		`return nil`, `return input`,
		`return {stageId="stage-second",accountId="account-a"}, "extra"`,
	} {
		t.Run(source, func(t *testing.T) {
			_, err := evaluator.Evaluate(context.Background(), exampleInput(source))
			if err == nil || !strings.Contains(err.Error(), "invalid") {
				t.Fatalf("invalid proposal accepted or hidden: %v", err)
			}
		})
	}
}

func TestSyntaxAndRuntimeErrorsAreDistinct(t *testing.T) {
	evaluator := New()
	if err := evaluator.Validate(`return {stageId="stage-second",accountId="account-a"}`); err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Validate(`return {`); err == nil || !strings.Contains(err.Error(), "syntax") {
		t.Fatalf("syntax error = %v", err)
	}
	if err := evaluator.Validate(`error("not executed on save")`); err != nil {
		t.Fatalf("validation executed script: %v", err)
	}
	_, err := evaluator.Evaluate(context.Background(), exampleInput(`error("policy failed")`))
	if err == nil || !strings.Contains(err.Error(), "runtime") || !strings.Contains(err.Error(), "policy failed") {
		t.Fatalf("runtime error = %v", err)
	}
}

func TestResourceExhaustionDoesNotKillServerOrPoisonNextDecision(t *testing.T) {
	evaluator := New()
	for _, program := range []struct{ name, source, guard string }{
		{"cpu", `while true do end`, "limit"},
		{"large-allocation", `local huge=string.rep("x",1073741824); return {stageId=huge,accountId="account-a"}`, "resource exhaustion"},
		{"table-growth", `local t={}; for i=1,100000000 do t[i]={i,i,i,i} end; return t`, ""},
	} {
		_, err := evaluator.Evaluate(context.Background(), exampleInput(program.source))
		if err == nil || (!strings.Contains(err.Error(), "limit") && !strings.Contains(err.Error(), "resource exhaustion")) || !strings.Contains(err.Error(), program.guard) {
			t.Fatalf("%s did not reach its resource guard: %v", program.name, err)
		}
		if _, err = evaluator.Evaluate(context.Background(), exampleInput(`return {stageId="stage-second",accountId="account-b"}`)); err != nil {
			t.Fatalf("worker exhaustion poisoned next decision: %v", err)
		}
	}
}

func TestCancellationAndAdmissionStayBounded(t *testing.T) {
	evaluator := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := evaluator.Evaluate(ctx, exampleInput(`while true do end`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	// Saturate admission deterministically, without sleeps or a fleet of expensive
	// programs. The same limiter protects real calls before child creation.
	evaluator.once.Do(func() { evaluator.slots = make(chan struct{}, 2) })
	evaluator.slots <- struct{}{}
	evaluator.slots <- struct{}{}
	if _, err := evaluator.Evaluate(context.Background(), exampleInput(`while true do end`)); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("admission = %v", err)
	}
	<-evaluator.slots
	<-evaluator.slots
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := evaluator.Evaluate(ctx, exampleInput(`while true do end`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active cancellation = %v", err)
	}
	if _, err := evaluator.Evaluate(context.Background(), exampleInput(`return {stageId="stage-second",accountId="account-a"}`)); err != nil {
		t.Fatalf("cancellation leaked child/admission: %v", err)
	}
	// Exercise two real disposable workers concurrently, not only the admission
	// counter. Both must finish and leave the shared evaluator reusable.
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := evaluator.Evaluate(context.Background(), exampleInput(`return {stageId="stage-second",accountId="account-b"}`))
			results <- err
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("concurrent worker failed: %v", err)
		}
	}
}

func TestInputAndOutputAreBounded(t *testing.T) {
	evaluator := New()
	if err := evaluator.Validate(strings.Repeat("x", MaxSource+1)); err == nil {
		t.Fatal("oversized source accepted")
	}
	input := exampleInput(`return {stageId="stage-second",accountId="account-a"}`)
	input.Asked = strings.Repeat("x", MaxInput)
	if _, err := evaluator.Evaluate(context.Background(), input); err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("oversized input = %v", err)
	}
	input = exampleInput(`return {stageId="stage-second",accountId="account-a"}`)
	input.Request = json.RawMessage(`{"input":"` + strings.Repeat("x", MaxInput) + `"}`)
	if _, err := evaluator.Evaluate(context.Background(), input); err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("oversized business request = %v", err)
	}
	_, err := evaluator.Evaluate(context.Background(), exampleInput(`error(string.rep("x",100000))`))
	if err == nil || len(err.Error()) > MaxOutput {
		t.Fatalf("runtime output not bounded: %v", err)
	}
	var output cappedBuffer
	payload := make([]byte, MaxOutput*4)
	if n, err := output.Write(payload); n != len(payload) || err != nil || !output.overflow || len(output.data) != MaxOutput {
		t.Fatal("worker pipe backpressure/output cap invariant violated")
	}
}
