package neoroute

import (
 "context"
 "encoding/json"
 "math"
 "os/exec"
 "reflect"
 "strings"
 "testing"

 "github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
 "github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

func policyState() routeState {
 window:=neopool.Window{Kind:"day",Known:true,Capacity:10000,StartsAt:1000,ResetsAt:86401000,Source:"configured-metered",Unit:"tokens"}
 vm:=VirtualModel{ID:"vm-route",Name:"route",Served:"neoproxy/route",Stages:[]Stage{{ID:"s0",Model:"primary/model",Accounts:AccountScope{All:true},Draw:"fill",PerAccount:.5,Cap:.8,Window:"day",Per:"user"}},Terminal:Terminal{Kind:"limit",Message:"Wait for reset"}}
 return routeState{principal:neo.Principal{UserID:"u",Role:"member",Grants:[]neo.Grant{{VirtualModel:vm.ID,Allotment:1}}},vm:&vm,at:2000,cursors:map[string]int64{"s0":0},snapshot:neopool.Snapshot{Providers:[]neopool.Provider{{ID:"primary",Exposed:true},{ID:"fallback",Exposed:true}},Models:[]neopool.Model{{ID:"primary/model",ProviderID:"primary",Name:"model",Served:"neoproxy/primary/model",Exposed:true},{ID:"fallback/model",ProviderID:"fallback",Name:"model",Served:"neoproxy/fallback/model",Exposed:true}},Accounts:[]neopool.Account{{ID:"a",EngineAuthID:"engine-a",ProviderID:"primary",OwnerID:"owner1",State:"healthy",ModelIDs:[]string{"primary/model"},Windows:[]neopool.Window{window}},{ID:"b",EngineAuthID:"engine-b",ProviderID:"primary",OwnerID:"owner2",State:"healthy",ModelIDs:[]string{"primary/model"},Windows:[]neopool.Window{window}},{ID:"c",EngineAuthID:"engine-c",ProviderID:"fallback",OwnerID:"owner1",State:"healthy",ModelIDs:[]string{"fallback/model"},Windows:[]neopool.Window{window}}}}}
}
func addUse(state *routeState,user,account string,tokens int64){state.usage=append(state.usage,neopool.Usage{UserID:user,VirtualModelID:state.vm.ID,StageID:"s0",AccountID:account,Window:"day",StartsAt:1000,ResetsAt:86401000,Tokens:tokens})}
func TestPolicyAuthorityAndBudgets(t *testing.T){
 tests:=[]struct{name string;change func(*routeState);wantAccount,wantReason,wantSkip string}{
  {"fill",func(*routeState){},"a","",""},
  {"grant",func(s *routeState){s.principal.Grants=nil},"","not-granted",""},
  {"key intersection",func(s *routeState){s.principal.AccountIDs=[]string{"b"}},"b","",""},
  {"empty key scope",func(s *routeState){s.principal.AccountIDs=[]string{}},"","limit","per-account"},
  {"ownership",func(s *routeState){s.principal.Role="owner";s.principal.UserID="owner2"},"b","",""},
  {"hidden provider",func(s *routeState){s.snapshot.Providers[0].Exposed=false},"","limit","unhealthy"},
  {"hidden model",func(s *routeState){s.snapshot.Models[0].Exposed=false},"","limit","unhealthy"},
  {"unavailable quota",func(s *routeState){for i:=range s.snapshot.Accounts{s.snapshot.Accounts[i].Windows=nil}},"","limit","unhealthy"},
  {"no auth model support",func(s *routeState){s.snapshot.Accounts[0].ModelIDs=nil;s.snapshot.Accounts[1].ModelIDs=nil},"","limit","unhealthy"},
  {"least-used",func(s *routeState){s.vm.Stages[0].Draw="least-used";addUse(s,"u","a",2000);addUse(s,"u","b",1000)},"b","",""},
  {"alternate cursor",func(s *routeState){s.vm.Stages[0].Draw="alternate";s.cursors["s0"]=1},"b","",""},
  {"allotment",func(s *routeState){s.principal.Grants[0].Allotment=.5;addUse(s,"u","a",2500)},"b","",""},
  {"shared",func(s *routeState){s.vm.Stages[0].Per="all";addUse(s,"other","a",5000)},"b","",""},
  {"per-user",func(s *routeState){addUse(s,"other","a",5000)},"a","",""},
  {"cap",func(s *routeState){addUse(s,"u","a",4000);addUse(s,"u","b",4000)},"","limit","cap"},
  {"reservations count",func(s *routeState){addUse(s,"u","a",0);s.usage[0].Reserved=5000},"b","",""},
  {"old epochs ignored",func(s *routeState){addUse(s,"u","a",5000);s.usage[0].StartsAt=0},"a","",""},
  {"window expired not zero",func(s *routeState){s.at=86401000},"","limit","unhealthy"},
  {"cap lower than sip",func(s *routeState){s.vm.Stages[0].Cap=.01;addUse(s,"u","a",100)},"","limit","cap"},
 }
 for _,test:=range tests{t.Run(test.name,func(t *testing.T){state:=policyState();test.change(&state);before,_:=json.Marshal(state.usage);d:=decide(Request{Asked:"neoproxy/route"},state,-1,nil);if d.AccountID!=test.wantAccount||d.Reason!=test.wantReason{t.Fatalf("decision=%+v",d)};if test.wantSkip!=""&&d.Trail[0].Skipped!=test.wantSkip{t.Fatalf("trail=%+v",d.Trail)};after,_:=json.Marshal(state.usage);if string(before)!=string(after){t.Fatal("pure resolver mutated usage")}})}
}
func TestDirectAndPassthroughScopes(t *testing.T){
 state:=policyState();d:=decide(Request{Asked:"neoproxy/primary/model"},state,-1,nil);if d.Reason!="no-provider-access"{t.Fatal(d)}
 state.principal.ProviderAccess=[]string{"primary"};state.principal.AccountIDs=[]string{"b"};d=decide(Request{Asked:"neoproxy/primary/model"},state,-1,nil);if !d.Direct||d.AccountID!="b"||d.StageID!=""{t.Fatal(d)}
 state=policyState();state.vm.Stages[0].Cap=0;state.vm.Terminal=Terminal{Kind:"passthrough",Model:"fallback/model"};d=decide(Request{Asked:"neoproxy/route"},state,-1,nil);if d.Outcome!="ok"||d.Direct||d.VirtualModel!="vm-route"||d.AccountID!="c"||d.StageID!=""||d.StageIndex!=nil{t.Fatal(d)}
 state.principal.AccountIDs=[]string{"a"};d=decide(Request{Asked:"neoproxy/route"},state,-1,nil);if d.Reason!="no-healthy-account"{t.Fatal(d)}
 state.principal.AccountIDs=nil;state.snapshot.Models[1].Exposed=false;d=decide(Request{Asked:"neoproxy/route"},state,-1,nil);if d.Reason!="unknown-model"{t.Fatal(d)}
}
func TestScriptProposalsCannotBypassPolicy(t *testing.T){
 for _,change:=range []func(*routeState){func(s *routeState){s.principal.AccountIDs=[]string{"a"}},func(s *routeState){s.snapshot.Accounts[1].State="disabled"},func(s *routeState){s.vm.Stages[0].Cap=0},func(s *routeState){s.principal.Grants=nil}}{
  state:=policyState();change(&state);d:=decide(Request{Asked:"neoproxy/route"},state,-1,&ScriptProposal{StageID:"s0",AccountID:"b"});if d.Outcome=="ok"{t.Fatalf("script escaped validation: %+v",d)}
 }
 state:=policyState();d:=decide(Request{Asked:"neoproxy/route"},state,0,&ScriptProposal{StageID:"s0",AccountID:"a"});if d.Outcome=="ok"{t.Fatal("script replayed a failed stage")}
}
func TestVirtualValidation(t *testing.T){
 for _,test:=range []struct{name string;change func(*VirtualModel)}{{"provider collision",func(v *VirtualModel){v.Name="primary"}},{"invalid name",func(v *VirtualModel){v.Name="Bad"}},{"duplicate stage",func(v *VirtualModel){v.Stages=append(v.Stages,v.Stages[0])}},{"unknown model",func(v *VirtualModel){v.Stages[0].Model="primary/unknown"}},{"wrong account",func(v *VirtualModel){v.Stages[0].Accounts=AccountScope{IDs:[]string{"c"}}}},{"nan",func(v *VirtualModel){v.Stages[0].Cap=math.NaN()}},{"infinite",func(v *VirtualModel){v.Stages[0].PerAccount=math.Inf(1)}},{"effort",func(v *VirtualModel){e:="turbo";v.Stages[0].Effort=&e}},{"terminal",func(v *VirtualModel){v.Terminal.Kind="silent-fallback"}}}{t.Run(test.name,func(t *testing.T){state:=policyState();test.change(state.vm);if Validate(*state.vm,state.snapshot,neo.Principal{Role:"admin"})==nil{t.Fatal("invalid virtual accepted")}})}
 state:=policyState();state.vm.Stages[0].Cap=.01;state.vm.Stages[0].PerAccount=.9;if err:=Validate(*state.vm,state.snapshot,neo.Principal{Role:"admin"});err!=nil{t.Fatal("cap lower than sip must be accepted",err)}
}

// Exercise the retained JavaScript resolver as an oracle, not a production path.
// Missing fixture scope/exposure checks and wall-clock rotation are tested above
// against the written contract instead of copied into production behavior.
func TestFixtureOracleParity(t *testing.T){
 node,err:=exec.LookPath("node");if err!=nil{t.Fatal("Node is required for fixture oracle parity:",err)}
 cases:=[]struct{name string;change func(*routeState)}{{"fill",func(*routeState){}},{"least-used",func(s *routeState){s.vm.Stages[0].Draw="least-used";addUse(s,"u","a",3000)}},{"allotment",func(s *routeState){s.principal.Grants[0].Allotment=.5;addUse(s,"u","a",2500)}},{"shared",func(s *routeState){s.vm.Stages[0].Per="all";addUse(s,"other","a",5000)}},{"exhausted",func(s *routeState){s.vm.Stages[0].Cap=0}},{"queue",func(s *routeState){s.vm.Stages[0].Cap=0;s.vm.Terminal.Kind="queue"}},{"passthrough",func(s *routeState){s.vm.Stages[0].Cap=0;s.vm.Terminal=Terminal{Kind:"passthrough",Model:"fallback/model"}}},{"not-granted",func(s *routeState){s.principal.Grants=nil}}}
 for _,test:=range cases{t.Run(test.name,func(t *testing.T){state:=policyState();test.change(&state);usage:=map[string]map[string]map[string]map[string]float64{};for _,u:=range state.usage{if usage[u.UserID]==nil{usage[u.UserID]=map[string]map[string]map[string]float64{}};if usage[u.UserID][u.VirtualModelID]==nil{usage[u.UserID][u.VirtualModelID]=map[string]map[string]float64{}};if usage[u.UserID][u.VirtualModelID][u.StageID]==nil{usage[u.UserID][u.VirtualModelID][u.StageID]=map[string]float64{}};usage[u.UserID][u.VirtualModelID][u.StageID][u.AccountID]=float64(u.Tokens)/10000}
 input:=map[string]any{"request":map[string]any{"asked":"neoproxy/route","user":map[string]any{"id":state.principal.UserID,"grants":state.principal.Grants,"providerAccess":state.principal.ProviderAccess},"at":0},"world":map[string]any{"providers":state.snapshot.Providers,"models":state.snapshot.Models,"accounts":state.snapshot.Accounts,"virtualModels":[]VirtualModel{*state.vm},"usage":usage}}
 raw,_:=json.Marshal(input);cmd:=exec.CommandContext(context.Background(),node,"--input-type=module","-e",`import {resolve} from '../neoui/static/lib/fixture.js'; let data='';for await(const c of process.stdin)data+=c;const x=JSON.parse(data);console.log(JSON.stringify(resolve(x.request,x.world)));`);cmd.Stdin=strings.NewReader(string(raw));output,err:=cmd.CombinedOutput();if err!=nil{t.Fatalf("oracle: %v %s",err,output)};var want map[string]any;if err=json.Unmarshal(output,&want);err!=nil{t.Fatal(err)};decision:=decide(Request{Asked:"neoproxy/route"},state,-1,nil);gotRaw,_:=json.Marshal(decision);var got map[string]any;_ = json.Unmarshal(gotRaw,&got)
 for _,key:=range []string{"outcome","reason","direct","virtualModel","stageIndex","stageId","model","accountId","effort"}{if !reflect.DeepEqual(got[key],want[key]){t.Fatalf("%s got=%v want=%v; decision=%s oracle=%s",key,got[key],want[key],gotRaw,output)}}
 wantTrail:=want["trail"].([]any);if len(decision.Trail)!=len(wantTrail){t.Fatalf("trail lengths differ: %s %s",gotRaw,output)};for i,step:=range decision.Trail{expected:=wantTrail[i].(map[string]any);if step.Kind!=expected["kind"]{t.Fatalf("trail kind differs: %+v",step)};for key,value:=range map[string]string{"skipped":step.Skipped,"chosen":step.Chosen}{if text,_:=expected[key].(string);text!=value{t.Fatalf("trail %s got=%s want=%s",key,value,text)}}}
 })}
}
