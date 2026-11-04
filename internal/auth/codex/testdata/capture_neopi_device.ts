// Executes NeoPi's OWN device login against synthetic, fail-closed HTTP responses.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { loginOpenAICodexDevice } = await import(root + "/pi-ai/src/registry/oauth/openai-codex.ts");
const nativeFetch = globalThis.fetch;
const nativeSleep = Bun.sleep;
const jwt = (payload: unknown) => "eyJhbGciOiJub25lIn0." + Buffer.from(JSON.stringify(payload)).toString("base64url") + ".fake";
const tokens = {access_token:jwt({"https://api.openai.com/auth":{chatgpt_account_id:"device-account",chatgpt_plan_type:" PRO "},"https://api.openai.com/profile":{email:" Device@Example.Invalid "}}),refresh_token:"device-refresh-fake",expires_in:3600};
const init = {device_auth_id:"device-id-fake",user_code:"FAKE-CODE",interval:"5"};
const authorized = {authorization_code:"device-code-fake",code_verifier:"device-verifier-fake"};
let spec: any, requests: any[], waits: number[], poll: number, controller: AbortController;
const requestedURLs: string[] = [];
const server = Bun.serve({hostname:"127.0.0.1",port:0,async fetch(request) {
  const url = requestedURLs.shift()!;
  requests.push({method:request.method,url,headers:Object.fromEntries(request.headers),body:await request.text()});
  if (url.endsWith("/usercode")) return Response.json(spec.init ?? init,{status:spec.initStatus ?? 200});
  if (url.endsWith("/deviceauth/token")) {
    const response = spec.polls[Math.min(poll++,spec.polls.length-1)];
    return Response.json(response.body ?? {},{status:response.status});
  }
  return Response.json(spec.tokens ?? tokens,{status:spec.tokenStatus ?? 200});
}});
const captureFetch = Object.assign(async (input:any, options?:any) => {
  const url = new URL(input instanceof Request ? input.url : String(input));
  if (url.origin !== "https://auth.openai.com" || !["/api/accounts/deviceauth/usercode","/api/accounts/deviceauth/token","/oauth/token"].includes(url.pathname)) throw new Error("Unexpected Codex device capture URL: " + url.href);
  requestedURLs.push(url.href);
  const headers = new Headers(options?.headers);
  headers.set("host",url.host);
  return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`,{...options,headers});
},{preconnect:nativeFetch.preconnect});
const pending = [{status:403},{status:404},{status:200,body:authorized}];
const scenarios: any[] = [];
try {
  globalThis.fetch = captureFetch as typeof fetch;
  Bun.sleep = (async (duration:number) => {waits.push(duration);if(spec.cancel) controller.abort();}) as typeof Bun.sleep;
  for (spec of [
    {name:"pending-403-404-then-authorized",polls:pending},
    {name:"numeric-zero-interval",init:{...init,interval:0},polls:pending},
    {name:"string-interval-prefix",init:{...init,interval:"6seconds"},polls:pending},
    {name:"missing-refresh-token",polls:[{status:200,body:authorized}],tokens:{...tokens,refresh_token:undefined}},
    {name:"nested-token-error",polls:[{status:200,body:authorized}],tokenStatus:400,tokens:{error:{code:"invalid_grant",message:"Device grant expired"}}},
    {name:"initiation-rejected",initStatus:401,polls:[]},
    {name:"polling-rejected",polls:[{status:429}]},
    {name:"missing-verifier",polls:[{status:200,body:{authorization_code:"device-code-fake"}}]},
    {name:"polling-exhausted",polls:[{status:403}]},
    {name:"cancelled-before-poll",polls:pending,cancel:true},
  ]) {
    requests=[];waits=[];poll=0;controller=new AbortController();
    let auth:unknown, credential:unknown, error:string|undefined;
    const before=Date.now();
    try {credential=await loginOpenAICodexDevice({signal:controller.signal,onAuth:(value:unknown)=>{auth=value;}});}
    catch(failure) {error=String(failure instanceof Error ? failure.message : failure);}
    scenarios.push({...spec,init:spec.init??init,tokens:spec.tokens??tokens,auth,credential,error,before,after:Date.now(),waits,requests});
  }
  console.log(JSON.stringify({source:"NeoPi OWN loginOpenAICodexDevice",normalizations:["synthetic credentials only","Bun.sleep records requested waits without sleeping","expiry is compared relative to request time; zero stored skew"],scenarios},null,2));
} finally {globalThis.fetch=nativeFetch;Bun.sleep=nativeSleep;server.stop(true);}
