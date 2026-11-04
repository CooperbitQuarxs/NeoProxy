// Executes NeoPi's OWN Devin login (the compiled `oauth-code` auth rule
// through registry/engine's DeclarativeOAuthCodeFlow) against synthetic token
// responses, with PKCE randomness and the clock pinned, recording the
// authorize URL, the token request on the wire and the projected credential.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { DeclarativeOAuthCodeFlow, resolveCallbackOptions } = await import(root + "/pi-ai/src/registry/engine/oauth-code.ts");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;
const policy = rules.auth.providers.find((entry: any) => entry.id === "devin");
if (!policy) throw new Error("devin auth rule missing");

const nativeFetch = globalThis.fetch;
const nativeRandom = crypto.getRandomValues.bind(crypto);
const nativeNow = Date.now;
const now = 1_800_000_000_000;
const jwt = (payload: unknown) => "eyJhbGciOiJub25lIn0." + Buffer.from(JSON.stringify(payload)).toString("base64url") + ".fake";

let spec: any;
let requests: any[];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const headers = Object.fromEntries(request.headers);
		delete headers.host;
		requests.push({ method: request.method, url: "https://api.devin.ai" + url.pathname + url.search, headers, body: await request.text() });
		return new Response(spec.body, { status: spec.status, headers: { "content-type": "application/json" } });
	},
});
const captureFetch = Object.assign(
	async (input: any, options?: any) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		if (url.origin !== "https://api.devin.ai") throw new Error("Unexpected Devin auth capture URL: " + url.href);
		return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`, { ...options, signal: undefined });
	},
	{ preconnect: nativeFetch.preconnect },
);

const redirectUri = `http://${policy.login.callback.hostname}:${policy.login.callback.port}${policy.login.callback.path}`;
const state = "11111111-2222-4333-8444-555555555555";
const scenarios: any[] = [];
try {
	for (spec of [
		{ name: "jwt-token", status: 200, body: JSON.stringify({ token: jwt({ sub: "devin-user", exp: 1_900_000_000 }) }) },
		{ name: "opaque-token", status: 200, body: JSON.stringify({ token: "opaque-session-token" }) },
		{ name: "code-with-state-fragment", code: "auth-code#echoed-state", status: 200, body: JSON.stringify({ token: jwt({ exp: 1_900_000_000 }) }) },
		{ name: "missing-token", status: 200, body: JSON.stringify({ error: "nope" }) },
		{ name: "rejected", status: 400, body: JSON.stringify({ error: "invalid_grant", error_description: "Code expired" }) },
	]) {
		requests = [];
		let counter = 0;
		crypto.getRandomValues = (<T extends ArrayBufferView>(array: T) => {
			const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
			for (let i = 0; i < bytes.length; i++) bytes[i] = (counter++ * 7 + 3) & 0xff;
			return array;
		}) as typeof crypto.getRandomValues;
		Date.now = () => now;
		const options = await resolveCallbackOptions(policy.login.callback, policy.id);
		const flow = new DeclarativeOAuthCodeFlow({ fetch: captureFetch } as any, policy.login, policy, options);
		const authorize = await flow.generateAuthUrl(state, redirectUri);
		let credentials: any = null;
		let error: string | null = null;
		try {
			credentials = await flow.exchangeToken(spec.code ?? "auth-code", state, redirectUri);
		} catch (cause: any) {
			error = String(cause?.message ?? cause);
		}
		crypto.getRandomValues = nativeRandom;
		Date.now = nativeNow;
		scenarios.push({ name: spec.name, code: spec.code ?? "auth-code", response: { status: spec.status, body: spec.body }, authorize, requests, credentials, error });
	}
} finally {
	crypto.getRandomValues = nativeRandom;
	Date.now = nativeNow;
	server.stop(true);
}
console.log(
	JSON.stringify(
		{ source: "NeoPi OWN devin oauth-code rule via DeclarativeOAuthCodeFlow", now, redirectUri, state, callback: policy.login.callback, refresh: policy.refresh, pasteCode: policy.pasteCode, scenarios },
		null,
		2,
	),
);
