// Captures NeoPi's real declarative OAuth engines with synthetic credentials only.
// All fetches fail closed unless redirected to this loopback recorder.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { authPolicyFor } = await import(root + "/pi-catalog/src/compat/auth.ts");
const { DeclarativeOAuthCodeFlow } = await import(root + "/pi-ai/src/registry/engine/oauth-code.ts");
const { getProviderDefinition } = await import(root + "/pi-ai/src/registry/index.ts");
const { getOAuthProviders } = await import(root + "/pi-ai/src/registry/oauth/index.ts");
const nativeFetch = globalThis.fetch;
const jwt = (payload: unknown) => "eyJhbGciOiJub25lIn0." + Buffer.from(JSON.stringify(payload)).toString("base64url") + ".fake";
const claims = (account: string, email: string, plan: string) => ({"https://api.openai.com/auth":{chatgpt_account_id:account,chatgpt_plan_type:plan},"https://api.openai.com/profile":{email}});
const access = jwt(claims("account-access", " Access@Example.Invalid ", " PRO "));
const idToken = jwt(claims("account-id", "id@example.invalid", "team"));
const baseResponse = {access_token:access,refresh_token:"refresh-fake",id_token:idToken,expires_in:3600};
let responseBody: unknown = baseResponse;
let responseStatus = 200;
let currentRequests: any[] = [];
const requestedURLs: string[] = [];
const server = Bun.serve({hostname:"127.0.0.1",port:0,async fetch(request) {
  currentRequests.push({method:request.method,url:requestedURLs.shift(),headers:Object.fromEntries(request.headers),body:await request.text()});
  return Response.json(responseBody,{status:responseStatus});
}});
const captureFetch = Object.assign(async (input: any, init?: any) => {
  const url = new URL(input instanceof Request ? input.url : String(input));
  if (url.origin !== "https://auth.openai.com" || url.pathname !== "/oauth/token") throw new Error("Unexpected Codex capture URL: " + url.href);
  requestedURLs.push(url.href);
  const headers = new Headers(init?.headers);
  headers.set("host",url.host);
  return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`,{...init,headers});
},{preconnect:nativeFetch.preconnect});
const policy = authPolicyFor("openai-codex");
const provider = getProviderDefinition("openai-codex");
const scenarios: any[] = [];
const previous = {access:"previous-access-fake",refresh:"previous-refresh-fake",expires:1893456000000,accountId:"stored-account",email:"stored@example.invalid",orgId:"stored-org",orgName:"stored-plan"};
try {
  globalThis.fetch = captureFetch as typeof fetch;
  for (const spec of [
    {name:"login-access-claims-first",phase:"login",response:baseResponse},
    {name:"login-id-token-fallback",phase:"login",response:{...baseResponse,access_token:jwt({})}},
    {name:"login-account-only",phase:"login",response:{...baseResponse,access_token:jwt({"https://api.openai.com/auth":{chatgpt_account_id:"account-only"}}),id_token:"invalid"}},
    {name:"refresh-preserves-organization",phase:"refresh",response:baseResponse},
    {name:"refresh-missing-claims-and-refresh",phase:"refresh",response:{access_token:"rotated-access-fake",expires_in:3600}},
    {name:"refresh-zero-expiry",phase:"refresh",response:{access_token:"rotated-access-fake",expires_in:0}},
    {name:"login-missing-identity",phase:"login",response:{access_token:"invalid",refresh_token:"fake",expires_in:3600}},
    {name:"refresh-missing-expiry",phase:"refresh",response:{access_token:"rotated-access-fake"}},
    {name:"refresh-rejected",phase:"refresh",status:400,response:{error:"invalid_grant"}},
  ]) {
    responseBody=spec.response; responseStatus=spec.status ?? 200; currentRequests=[];
    let authURL: string | undefined;
    let flow: any;
    if(spec.phase === "login") {
      flow = new DeclarativeOAuthCodeFlow({fetch:captureFetch},policy.login,policy,{preferredPort:1455,callbackPath:"/auth/callback",allowPortFallback:false});
      authURL=(await flow.generateAuthUrl("state-parity-fake","http://localhost:1455/auth/callback")).url;
    }
    const before=Date.now();
    let credential: unknown, error: string | undefined;
    try { credential=spec.phase === "login" ? await flow.exchangeToken("code-parity-fake","state-parity-fake","http://localhost:1455/auth/callback") : await provider.refreshToken(previous); }
    catch (failure) { error=String(failure instanceof Error ? failure.message : failure); }
    scenarios.push({...spec,authURL,previous:spec.phase === "refresh" ? previous : undefined,before,after:Date.now(),credential,error,requests:currentRequests});
  }
  console.log(JSON.stringify({source:"NeoPi OWN openai-codex auth rule, DeclarativeOAuthCodeFlow, createRefresh and openAICodexProfileHook",normalizations:["PKCE verifier/challenge are replayed from the fixture","expiry is compared relative to the request time; zero stored skew"],deviceOffered:getOAuthProviders().some((p:any)=>p.id === "openai-codex-device" && p.available),scenarios},null,2));
} finally {globalThis.fetch=nativeFetch;server.stop(true);}
