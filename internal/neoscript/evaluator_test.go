//go:build linux && amd64

package neoscript

import (
 "context"
 "errors"
 "os"
 "strings"
 "testing"
 "time"

 "github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
)

func TestMain(m *testing.M) {
 if handled,code:=WorkerMain();handled {os.Exit(code)}
 if len(os.Args)==3 && os.Args[1]=="--test-script-confinement" {os.Exit(confinementProbe(os.Args[2]))}
 os.Exit(m.Run())
}

func exampleInput(source string) neoroute.ScriptInput {
 return neoroute.ScriptInput{Source:source,Asked:"neoproxy/heavy",Protocol:"responses",UserID:"user-member",VirtualModel:neoroute.VirtualModel{ID:"vm-heavy",Name:"heavy",Script:source},Stages:[]neoroute.ScriptStage{
  {StageID:"stage-spent",Model:"codex/first",Skipped:"cap",Total:0.8,Cap:0.8},
  {StageID:"stage-second",Model:"codex/second",Total:0.2,Cap:0.8,Candidates:[]neoroute.Candidate{{AccountID:"account-a",Used:0.18,Budget:0.4},{AccountID:"account-b",Used:0.02,Budget:0.4}}},
 }}
}

func TestDecisionReadsRequestAndUsage(t *testing.T) {
 evaluator:=New()
 input:=exampleInput(`
 assert(input.asked == "neoproxy/heavy" and input.protocol == "responses")
 assert(input.userId == "user-member" and input.virtualModel.script == nil)
 assert(#input.stages == 2 and input.stages[1].skipped == "cap")
 local best, used = nil, math.huge
 for _, candidate in ipairs(input.stages[2].candidates) do
   if candidate.used < used then best, used = candidate.accountId, candidate.used end
 end
 return {stageId=input.stages[2].stageId, accountId=best}
 `)
 decision,err:=evaluator.Evaluate(context.Background(),input)
 if err!=nil {t.Fatal(err)}
 if decision.StageID!="stage-second" || decision.AccountID!="account-b" {t.Fatalf("least-used decision = %+v",decision)}
 // A child never receives a pointer back into authoritative caller state.
 if input.Stages[1].Candidates[0].Used!=0.18 || input.VirtualModel.Script!=input.Source {t.Fatal("script altered caller snapshot")}
}

func TestReadOnlyNestedInputAndSafeTableHelpers(t *testing.T) {
 evaluator:=New()
 for _,mutation:=range []string{
  `input.asked="neoproxy/direct"`,
  `input.stages[2].candidates[1].used=0`,
  `input.stages[3]={}`,
  `table.insert(input.stages,{})`,
  `table.remove(input.stages,1)`,
  `table.sort(input.stages)`,
 } {
  t.Run(mutation,func(t *testing.T){_,err:=evaluator.Evaluate(context.Background(),exampleInput(mutation+`; return {stageId="stage-second",accountId="account-a"}`));if err==nil || !strings.Contains(err.Error(),"read-only") {t.Fatalf("mutation error = %v",err)}})
 }
 decision,err:=evaluator.Evaluate(context.Background(),exampleInput(`
 assert(table.getn(input.stages)==2)
 local localData={"b","a"}; table.sort(localData)
 assert(table.concat(localData, "-")=="a-b")
 local n=0; for k,v in pairs(input.stages[2]) do n=n+1 end
 assert(n >= 4)
 return {stageId="stage-second",accountId="account-a"}
 `))
 if err!=nil || decision.AccountID!="account-a" {t.Fatalf("pure helpers failed: %+v %v",decision,err)}
}

func TestHostCapabilitiesAbsent(t *testing.T) {
 _,err:=New().Evaluate(context.Background(),exampleInput(`
 assert(os==nil and io==nil and package==nil and debug==nil and coroutine==nil and channel==nil)
 assert(require==nil and dofile==nil and loadfile==nil and load==nil and loadstring==nil)
 assert(rawset==nil and rawget==nil and setmetatable==nil and getmetatable==nil)
 assert(getfenv==nil and setfenv==nil and newproxy==nil and collectgarbage==nil and print==nil)
 assert(string.dump==nil and math.random==nil and math.randomseed==nil)
 return {stageId="stage-second",accountId="account-b"}
 `))
 if err!=nil {t.Fatal(err)}
}

func TestInvalidDecisionCannotSelectOutsideAuthorizedSnapshot(t *testing.T) {
 evaluator:=New()
 for _,source:=range []string{
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
 } {t.Run(source,func(t *testing.T){_,err:=evaluator.Evaluate(context.Background(),exampleInput(source));if err==nil || !strings.Contains(err.Error(),"invalid") {t.Fatalf("invalid proposal accepted or hidden: %v",err)}})}
}

func TestSyntaxAndRuntimeErrorsAreDistinct(t *testing.T) {
 evaluator:=New()
 if err:=evaluator.Validate(`return {stageId="stage-second",accountId="account-a"}`);err!=nil {t.Fatal(err)}
 if err:=evaluator.Validate(`return {`);err==nil || !strings.Contains(err.Error(),"syntax") {t.Fatalf("syntax error = %v",err)}
 if err:=evaluator.Validate(`error("not executed on save")`);err!=nil {t.Fatalf("validation executed script: %v",err)}
 _,err:=evaluator.Evaluate(context.Background(),exampleInput(`error("policy failed")`))
 if err==nil || !strings.Contains(err.Error(),"runtime") || !strings.Contains(err.Error(),"policy failed") {t.Fatalf("runtime error = %v",err)}
}

func TestResourceExhaustionDoesNotKillServerOrPoisonNextDecision(t *testing.T) {
 evaluator:=New()
 for _,source:=range []string{
  `while true do end`,
  `local huge=string.rep("x",1024*1024*1024); return {stageId=huge,accountId="account-a"}`,
  `local t={}; for i=1,100000000 do t[i]={i,i,i,i} end; return t`,
 } {
  _,err:=evaluator.Evaluate(context.Background(),exampleInput(source))
  if err==nil || (!strings.Contains(err.Error(),"limit") && !strings.Contains(err.Error(),"resource exhaustion")) {t.Fatalf("unbounded program did not reach a resource guard: %s: %v",source,err)}
  if _,err=evaluator.Evaluate(context.Background(),exampleInput(`return {stageId="stage-second",accountId="account-b"}`));err!=nil {t.Fatalf("worker exhaustion poisoned next decision: %v",err)}
 }
}

func TestCancellationAndAdmissionStayBounded(t *testing.T) {
 evaluator:=New()
 ctx,cancel:=context.WithCancel(context.Background());cancel()
 if _,err:=evaluator.Evaluate(ctx,exampleInput(`while true do end`));!errors.Is(err,context.Canceled) {t.Fatalf("cancellation = %v",err)}
 // Saturate admission deterministically, without sleeps or a fleet of expensive
 // programs. The same limiter protects real calls before child creation.
 evaluator.once.Do(func(){evaluator.slots=make(chan struct{},2)})
 evaluator.slots<-struct{}{};evaluator.slots<-struct{}{}
 if _,err:=evaluator.Evaluate(context.Background(),exampleInput(`while true do end`));err==nil || !strings.Contains(err.Error(),"busy") {t.Fatalf("admission = %v",err)}
 <-evaluator.slots;<-evaluator.slots
 ctx,cancel=context.WithTimeout(context.Background(),50*time.Millisecond)
 defer cancel()
 if _,err:=evaluator.Evaluate(ctx,exampleInput(`while true do end`));!errors.Is(err,context.DeadlineExceeded) {t.Fatalf("active cancellation = %v",err)}
 if _,err:=evaluator.Evaluate(context.Background(),exampleInput(`return {stageId="stage-second",accountId="account-a"}`));err!=nil {t.Fatalf("cancellation leaked child/admission: %v",err)}
}

func TestInputAndOutputAreBounded(t *testing.T) {
 evaluator:=New()
 if err:=evaluator.Validate(strings.Repeat("x",MaxSource+1));err==nil {t.Fatal("oversized source accepted")}
 input:=exampleInput(`return {stageId="stage-second",accountId="account-a"}`)
 input.Asked=strings.Repeat("x",MaxInput)
 if _,err:=evaluator.Evaluate(context.Background(),input);err==nil || !strings.Contains(err.Error(),"bound") {t.Fatalf("oversized input = %v",err)}
 _,err:=evaluator.Evaluate(context.Background(),exampleInput(`error(string.rep("x",100000))`))
 if err==nil || len(err.Error())>MaxOutput {t.Fatalf("runtime output not bounded: %v",err)}
 var output cappedBuffer
 payload:=make([]byte,MaxOutput*4)
 if n,err:=output.Write(payload);n!=len(payload)||err!=nil||!output.overflow||len(output.data)!=MaxOutput {t.Fatal("worker pipe backpressure/output cap invariant violated")}
}
