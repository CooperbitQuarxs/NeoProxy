// Run against the user's OWN NeoPi source with fake credentials. All network
// traffic goes to this loopback recorder, including canonical-host fallbacks.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const {anthropicModelManagerOptions} = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");
const {claudeUsageProvider} = await import(root + "/pi-ai/src/usage/claude.ts");
const nativeFetch = globalThis.fetch;
const fixedNow = Date.parse("2026-11-04T12:00:00Z");
Date.now = () => fixedNow;
type Reply = {status?: number; contentType?: string; headers?: Record<string,string>; body?: any; text?: string};
let replies: Reply[] = [], requests: any[] = [], urls: string[] = [];
const server = Bun.serve({hostname:"127.0.0.1",port:0,async fetch(request) {
  requests.push({method:request.method,headers:Object.fromEntries(request.headers),body:await request.text()});
  const reply = replies[Math.min(requests.length-1,replies.length-1)] ?? {};
  return new Response(reply.text ?? JSON.stringify(reply.body ?? {}), {status:reply.status ?? 200,headers:{"Content-Type":reply.contentType ?? "application/json",...reply.headers}});
}});
const captureFetch = Object.assign(async (input: any, init?: any) => {
  const url = new URL(input instanceof Request ? input.url : String(input));
  if (!url.pathname.includes("/models") && !url.pathname.startsWith("/api/oauth/")) {
    // Optional public stencil metadata is outside this provider-wire fixture.
    return Response.json({});
  }
  if (!new Set(["api.anthropic.com","custom.example.invalid"]).has(url.host)) throw new Error("Unexpected capture host: " + url.host);
  urls.push(url.href);
  const headers = new Headers(init?.headers); headers.set("host",url.host);
  return nativeFetch(`http://127.0.0.1:${server.port}` + url.pathname + url.search,{...init,headers});
}, {preconnect:nativeFetch.preconnect});
const normalize = () => requests.map((request,index)=>({...request,url:urls[index]}));
const fresh = (response: Reply[]) => { replies=response; requests=[]; urls=[]; };
const reset = {cedar_ember:{eligible:false}};
const good = {...reset,five_hour:{utilization:25,resets_at:"2026-11-04T16:00:00Z"},seven_day:{utilization:65,resets_at:"2026-11-10T12:00:00Z"}};
const cases: {name:string,base?:string,replies:Reply[]}[] = [
  {name:"legacy-windows",replies:[{body:good}]},
  {name:"generic-inactive-and-scoped",replies:[{body:{...reset,limits:[{kind:"session",percent:"0x19",resets_at:"2026-11-04T16:00:00Z",is_active:false},{kind:"weekly_all",percent:71,is_active:false},{kind:"weekly_scoped",percent:100,scope:{model:{display_name:"Fable"}}}]}}]},
  {name:"legacy-precedes-generic",replies:[{body:{...good,limits:[{kind:"session",percent:100},{kind:"weekly_all",percent:100}]}}]},
  {name:"null-windows",replies:[{body:{...reset,five_hour:null,seven_day:null}}]},
  {name:"reset-only-window",replies:[{body:{...reset,five_hour:{resets_at:"2026-11-04T16:00:00Z"}}}]},
  {name:"scoped-not-shared",replies:[{body:{...reset,limits:[{kind:"weekly_scoped",percent:100,scope:{model:{display_name:"Fable"}}}]}}]},
  {name:"extra-only",replies:[{body:{...reset,extra_usage:{is_enabled:true,used_credits:1200,monthly_limit:4000}}}]},
  {name:"spend-only",replies:[{body:{...reset,spend:{enabled:true,used:{amount_minor:1200,exponent:2,currency:"USD"},limit:null}}}]},
  {name:"bounded-percent",replies:[{body:{...reset,five_hour:{utilization:-5},seven_day:{utilization:130}}}]},
  {name:"custom-base-v1",base:"https://custom.example.invalid/v1",replies:[{body:good}]},
  {name:"custom-base-oauth",base:"https://custom.example.invalid/api/oauth",replies:[{body:good}]},
  ...[401,403,429].map(status=>({name:`terminal-${status}`,base:"https://custom.example.invalid",replies:[{status}]})),
  ...[408,500,503].map(status=>({name:`retry-${status}`,replies:[{status},{body:good}]})),
  {name:"retry-after-prefix",replies:[{status:503,headers:{"retry-after":"2 seconds"}},{body:good}]},
  {name:"retry-after-date",replies:[{status:503,headers:{"retry-after":"Wed, 04 Nov 2026 12:00:03 GMT"}},{body:good}]},
  {name:"retries-exhausted",replies:[{status:500}]},
  ...[404,405,410,501].map(status=>({name:`fallback-${status}`,base:"https://custom.example.invalid",replies:[{status},{body:good}]})),
  {name:"fallback-html",base:"https://custom.example.invalid",replies:[{contentType:"text/html",text:"<html>index</html>"},{body:good}]},
  {name:"fallback-empty",base:"https://custom.example.invalid",replies:[{text:"   "},{body:good}]},
  {name:"malformed-json-retries",base:"https://custom.example.invalid",replies:[{text:"{"},{body:good}]},
  {name:"trailing-json-retries",replies:[{text:JSON.stringify(good)+" {}"},{body:good}]},
];
const usage:any[]=[];
const models:any[]=[];
try {
  for (const spec of cases) {
    fresh(spec.replies);
    const delays:number[]=[];
    const report = await claudeUsageProvider.fetchUsage({provider:"anthropic",baseUrl:spec.base,credential:{type:"oauth",accessToken:"sk-ant-oat-parity-fake",accountId:"account-parity",email:"parity@example.invalid"}}, {fetch:captureFetch,retryWait:async(delay:number)=>{delays.push(delay)}});
    usage.push({...spec,requests:normalize(),delays,windows:(report?.limits ?? []).filter((row:any)=>row.scope.shared).map((row:any)=>({id:row.scope.windowId,used:row.amount.used,resetsAt:row.window?.resetsAt ?? null}))});
  }
  const bodies: {name:string,reply:Reply}[] = [
    {name:"nested-validity-and-deduplication",reply:{body:{data:{items:[{id:"claude-sonnet-4-5",name:"Name",display_name:"Display"},{id:"claude-new-future",display_name:"Future"},{id:"claude-new-future",name:"Last name"},{id:"invalid-name",name:12},{id:""},null]}}}},
    {name:"empty-success",reply:{body:{data:[]}}},
    {name:"direct-array",reply:{body:[{id:"new-id",name:null},{id:"empty-display",display_name:""}]}},
    {name:"rejected-key",reply:{status:401}},
    {name:"malformed-json",reply:{text:"{"}},
    {name:"trailing-json",reply:{text:'{"data":[]} {}'}},
  ];
  for (const token of ["sk-ant-oat-parity-fake","sk-ant-api-parity-fake"]) {
    for (const fixture of bodies) {
      const base="https://custom.example.invalid/v1";
      const response=[fixture.reply]; fresh(response);
      const options=anthropicModelManagerOptions({apiKey:token,baseUrl:base,fetch:captureFetch});
      const result=await options.fetchDynamicModels!();
      models.push({name:fixture.name + (token.includes("oat")?"-oauth":"-apikey"),token,base,replies:response,requests:normalize(),models:result?.map((row:any)=>({id:row.id,name:row.name}))});
    }
  }
  await Bun.write(process.argv[2] ?? new URL("discovery_usage_capture.json",import.meta.url), JSON.stringify({source:"NeoPi OWN anthropicModelManagerOptions and claudeUsageProvider, loopback captures only",fixedNow,usage,models},null,2)+"\n");
  console.log(`Captured ${usage.length} usage and ${models.length} discovery cases from NeoPi source`);
} finally {server.stop(true);}
