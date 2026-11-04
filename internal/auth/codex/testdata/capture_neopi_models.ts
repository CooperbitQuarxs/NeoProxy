// Captures the OWN Codex discovery function and materialized model policy.
// Network access is restricted to the synthetic loopback recorder.
const root=process.env.NEOPI_SOURCE_MODULES;
if(!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const {fetchCodexModels}=await import(root+"/pi-catalog/src/discovery/codex.ts");
const {buildModel}=await import(root+"/pi-catalog/src/build.ts");
const {getBundledModels}=await import(root+"/pi-catalog/src/models.ts");
const nativeFetch=globalThis.fetch;
let spec:any,requests:any[],step:number;
const requestedURLs:string[]=[];
const server=Bun.serve({hostname:"127.0.0.1",port:0,async fetch(request){
 const url=requestedURLs.shift()!;
 requests.push({method:request.method,url,headers:Object.fromEntries(request.headers),body:await request.text()});
 const response=spec.responses[Math.min(step++,spec.responses.length-1)];
 const headers={"Content-Type":"application/json",...(response.etag?{etag:response.etag}:{})};
 return new Response(response.raw??JSON.stringify(response.body??{}),{status:response.status??200,headers});
}});
const captureFetch=Object.assign(async(input:any,options?:any)=>{
 const url=new URL(input instanceof Request?input.url:String(input));
 if(url.origin!=="https://chatgpt.com"||!["/backend-api/codex/models","/backend-api/models"].includes(url.pathname)) throw new Error("Unexpected Codex discovery URL: "+url.href);
 requestedURLs.push(url.href);
 const headers=new Headers(options?.headers);headers.set("host",url.host);
 return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`,{...options,headers});
},{preconnect:nativeFetch.preconnect});
const terra={slug:"gpt-6.1-terra",display_name:"GPT-6.1 Terra",context_window:500000,max_context_window:922000,supported_reasoning_levels:[{effort:"high"}],prefer_websockets:true,use_responses_lite:true,priority:1,service_tiers:[{id:"priority"},{id:"priority"},{id:"ultrafast"}],available_access_programs:{cyber:[" research ",""]}};
const scenarios:any[]=[];
try {
 for(spec of [
  {name:"account-models-and-etag",accountId:"account-a",responses:[{etag:'"roster-a"',body:{models:[terra,{slug:"hidden-retired",visibility:" HIDDEN "},{slug:"hidden-alias",visibility:"hide"},{slug:"text-only",priority:0,input_modalities:["TEXT","audio","text"],default_reasoning_level:"none"}]}}]},
  {name:"fallback-route",accountId:"account-b",responses:[{status:404},{body:{data:[{id:"gpt-6.1-sol",display_name:" GPT-6.1 Sol ",context_window:272000,max_context_window:872000}]}}]},
  {name:"missing-account-header",responses:[{body:{models:[{slug:"opaque-model",input_modalities:[],service_tiers:[],tool_mode:"code_mode_only"}]}}]},
  {name:"worker-alias-and-context-floor",accountId:"account-a",responses:[{body:{models:[{slug:"gpt-5.6-luna-wm",context_window:272000},{slug:"unknown-wm"},{slug:"gpt-5.6-sol"}]}}]},
  {name:"advertised-plain-wins",responses:[{body:{models:[{slug:"gpt-5.6-luna-wm",display_name:"Worker"},{slug:"gpt-5.6-luna",display_name:"Plain",context_window:1100000}]}}]},
  {name:"lineage-and-rolling-aliases",responses:[{body:{models:[
   "gpt-4.1","gpt-5.1-codex-mini","gpt-5.3-codex-spark","gpt-5.4","gpt-5.4-mini","gpt-5.4-nano",
   "gpt-daybreak-blue-latest","gpt-daybreak-red-latest-wm","daybreak-blue-latest",
   "o1","o1-preview","o3","o3-mini","o4-mini","chatgpt-5.4-latest","opaque-reasoner",
  ].map(slug=>({slug,context_window:400000,priority:99,default_reasoning_level:"high"}))}}]},
  {name:"credential-rejected",accountId:"account-revoked",responses:[{status:401},{body:{models:[terra]}}]},
  {name:"credential-forbidden",responses:[{status:403}]},
  {name:"authoritative-empty",responses:[{body:{models:[]}}]},
  {name:"malformed-then-fallback",responses:[{raw:"not-json"},{body:{models:[terra]}}]},
  {name:"unavailable-routes",responses:[{status:503},{status:404}]},
 ]) {
  requests=[];step=0;
  const result=await fetchCodexModels({accessToken:"discovery-access-fake",accountId:spec.accountId,fetchFn:captureFetch});
  scenarios.push({...spec,result,materialized:result?.models.map((model:any)=>buildModel(model)),requests});
 }
 console.log(JSON.stringify({source:"NeoPi OWN fetchCodexModels and buildModel",bundled:getBundledModels("openai-codex"),normalizations:[],scenarios},null,2));
} finally {server.stop(true);}
