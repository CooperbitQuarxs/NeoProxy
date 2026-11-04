// Only fake credentials are used. Every token/bootstrap fetch is redirected to
// the local recorder; no request leaves the machine and no profile is consulted.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const {getProviderDefinition} = await import(root + "/pi-ai/src/registry/index.ts");
const nativeFetch = globalThis.fetch;
const requests: unknown[] = [];
const server = Bun.serve({hostname:"127.0.0.1",port:0,async fetch(request) {
  const text = await request.text();
  requests.push({method:request.method,path:new URL(request.url).pathname+new URL(request.url).search,headers:Object.fromEntries(request.headers),body:text ? JSON.parse(text) : null});
  if (new URL(request.url).pathname === "/api/claude_cli/bootstrap") {
    return Response.json({oauth_account:{account_uuid:"22222222-2222-4222-8222-222222222222",account_email:"parity@example.invalid",organization_uuid:"44444444-4444-4444-8444-444444444444",organization_name:"Parity organization"}});
  }
  return Response.json({access_token:"sk-ant-oat-parity-fake",refresh_token:"refresh-parity-fake",expires_in:3600});
}});
const requestedURLs: string[] = [];
const captureFetch = Object.assign(async (input: any, init?: any) => {
  const url = new URL(input instanceof Request ? input.url : String(input));
  if (url.origin !== "https://api.anthropic.com") throw new Error("Unexpected OAuth capture origin");
  requestedURLs.push(url.href);
  const headers = new Headers(init?.headers); headers.set("host",url.host);
  return nativeFetch(`http://127.0.0.1:${server.port}` + url.pathname + url.search,{...init,headers});
}, {preconnect:nativeFetch.preconnect});
let authURL = "";
try {
  const provider = getProviderDefinition("anthropic");
  const before = Date.now();
  const credential = await provider.login({onAuth:(info: any)=>{authURL=info.url},onManualCodeInput:async()=>`code-parity-fake#${new URL(authURL).searchParams.get("state")}`,fetch:captureFetch});
  const after = Date.now();
  globalThis.fetch = captureFetch as typeof fetch;
  const refreshBefore = Date.now();
  const refreshed = await provider.refreshToken(credential);
  const refreshAfter = Date.now();
  const loginRequests = requests.length;
  // Project the existing Claude JSON shape into NeoPi's credential engine.
  const legacyCredential = {type:"claude",prefix:"team",id_token:"legacy-id-fake",access_token:"sk-ant-oat-parity-fake",refresh_token:"refresh-parity-fake",expired:"2030-01-01T00:00:00Z",email:"parity@example.invalid",account_uuid:"22222222-2222-4222-8222-222222222222",organization_uuid:"44444444-4444-4444-8444-444444444444",org_name:"Legacy organization",claude_device_ids:["legacy-device-fake"],owner_id:"owner-parity"};
  await provider.refreshToken({access:legacyCredential.access_token,refresh:legacyCredential.refresh_token,expires:Date.parse(legacyCredential.expired),email:legacyCredential.email,accountId:legacyCredential.account_uuid,orgId:legacyCredential.organization_uuid,orgName:legacyCredential.org_name});
  const normalized = requests.map((request: any,index) => ({method:request.method,url:requestedURLs[index],headers:request.headers,body:request.body}));
  console.log(JSON.stringify({source:"NeoPi OWN Anthropic auth rule, OAuth-code/refresh engines and bootstrap hook; capture_oauth.ts with fake credentials only",authURL,before,after,credential,refreshBefore,refreshAfter,refreshed,requests:normalized.slice(0,loginRequests),legacyCredential,legacyRequests:normalized.slice(loginRequests),storedExpirySkewMillis:300000},null,2));
} finally { globalThis.fetch = nativeFetch; server.stop(true); }
