// Runs NeoPi's OWN Cloudflare AI Gateway credential and endpoint handling:
// parseCloudflareCredential and serializeCloudflareCredential (pi-catalog
// wire/cloudflare.ts) over credential inputs, and
// cloudflareAiGatewayTransport.prepareRequest (pi-ai
// registry/cloudflare-ai-gateway.ts) over endpoint, key and
// CLOUDFLARE_ACCOUNT_ID / CLOUDFLARE_GATEWAY_ID combinations. Records each
// result or the error it throws, to the path given as the first argument.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_cloudflare.ts <out.json>
const root = process.env.NEOPI_SOURCE_MODULES;
const out = process.argv[2];
if (!root || !out) throw new Error("NEOPI_SOURCE_MODULES and an output path are required");
const { parseCloudflareCredential, serializeCloudflareCredential } = await import(root + "/pi-catalog/src/wire/cloudflare.ts");
const { CLOUDFLARE_AI_GATEWAY_BASE_URL } = await import(root + "/pi-catalog/src/wire/cloudflare-ai-gateway.ts");
const { cloudflareAiGatewayTransport } = await import(root + "/pi-ai/src/registry/cloudflare-ai-gateway.ts");
const { NO_AUTH_SENTINEL } = await import(root + "/pi-ai/src/auth-retry.ts");

const inputs = [
	"cfut_token",
	"  cfut_token \n",
	"\ufeffcfut_token\u00a0",
	"",
	"   ",
	JSON.stringify({ token: "cfut_token", accountId: "acct", gatewayId: "default" }),
	JSON.stringify({ token: " cfut_token ", accountId: " acct ", gatewayId: "" }),
	JSON.stringify({ token: "cfut_token", accountId: "\u2003acct\ufeff" }),
	JSON.stringify({ token: "cfut_token", gatewayId: 5 }),
	JSON.stringify({ token: "cfut_token", accountId: null }),
	JSON.stringify({ token: "", accountId: "acct" }),
	JSON.stringify({ token: "  " }),
	JSON.stringify({ accountId: "acct" }),
	JSON.stringify({ token: 7 }),
	JSON.stringify({ token: "cfut_token", extra: { nested: true } }),
	"{not json",
	"{}",
	"[1]",
	"null",
	"{\"token\":\"a<b>&c\"}",
];
const parse = inputs.map(input => ({ input, credential: parseCloudflareCredential(input) }));

const credentials = [
	{ token: "cfut_token" },
	{ token: " cfut_token ", accountId: " acct ", gatewayId: " gw " },
	{ token: "cfut_token", accountId: "", gatewayId: "  " },
	{ token: "a<b>&c", accountId: "\u3000acct", gatewayId: "gw\u2028" },
];
const serialize = credentials.map(credential => ({ credential, serialized: serializeCloudflareCredential(credential) }));

const bases = [
	CLOUDFLARE_AI_GATEWAY_BASE_URL + "/anthropic",
	CLOUDFLARE_AI_GATEWAY_BASE_URL + "/openai",
	CLOUDFLARE_AI_GATEWAY_BASE_URL + "/compat",
	CLOUDFLARE_AI_GATEWAY_BASE_URL + "/<account>/x",
	"https://gateway.ai.cloudflare.com/v1/acct-fixed/gw-fixed/compat",
	"https://example.test/v1",
];
const keys = [
	"cfut_plain",
	JSON.stringify({ token: "cfut_struct", accountId: "acct-key", gatewayId: "gw-key" }),
	JSON.stringify({ token: "cfut_struct", accountId: "acct-key" }),
	JSON.stringify({ token: "cfut_struct", gatewayId: "gw-key" }),
	"{not json",
	"",
];
const envs = [
	{},
	{ CLOUDFLARE_ACCOUNT_ID: "acct-env", CLOUDFLARE_GATEWAY_ID: "gw-env" },
	{ CLOUDFLARE_ACCOUNT_ID: "acct-env" },
	{ CLOUDFLARE_GATEWAY_ID: "gw-env" },
];
const names = ["CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_GATEWAY_ID", "CLOUDFLARE_AI_GATEWAY_API_KEY"];
const transport: any[] = [];
for (const base of bases) {
	for (const apiKey of keys) {
		for (const env of envs) {
			for (const name of names) delete process.env[name];
			Object.assign(process.env, env);
			const model = { id: "m", api: "openai-completions", baseUrl: base, headers: { Authorization: "Bearer stale", "x-api-key": "stale", "X-Keep": "kept" } };
			const record: any = { base, apiKey, env };
			try {
				const prepared = cloudflareAiGatewayTransport.prepareRequest(model, { apiKey });
				record.baseUrl = prepared.model.baseUrl;
				record.headers = prepared.model.headers ?? null;
				record.noAuth = prepared.options.apiKey === NO_AUTH_SENTINEL;
				record.prepared = prepared.model !== model;
			} catch (error) {
				record.error = (error as Error).message;
			}
			transport.push(record);
		}
	}
}
for (const name of names) delete process.env[name];
await Bun.write(out, JSON.stringify({ source: "NeoPi OWN pi-catalog wire/cloudflare.ts and pi-ai registry/cloudflare-ai-gateway.ts", gatewayBase: CLOUDFLARE_AI_GATEWAY_BASE_URL, parse, serialize, transport }, null, "\t") + "\n");
console.log(`${parse.length} parse, ${serialize.length} serialize, ${transport.length} transport cases`);
