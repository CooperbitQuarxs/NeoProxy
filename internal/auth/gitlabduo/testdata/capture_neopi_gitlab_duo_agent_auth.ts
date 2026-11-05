// Executes NeoPi's OWN GitLab Duo Agent sign-in and refresh (the compiled
// `gitlab-duo-agent` auth rule through registry/engine's
// DeclarativeOAuthCodeFlow and createRefresh) against synthetic gitlab.com
// token responses, with PKCE randomness and the clock pinned. The rule is
// manual-only: the browser returns to GitLab's VS Code redirect and the user
// pastes that URL back, so parseCallbackInput's reading of pasted input is
// recorded too. Nothing leaves loopback.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_gitlab_duo_agent_auth.ts > gitlab_duo_agent_auth.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { DeclarativeOAuthCodeFlow, resolveCallbackOptions } = await import(root + "/pi-ai/src/registry/engine/oauth-code.ts");
const { createRefresh } = await import(root + "/pi-ai/src/registry/engine/refresh.ts");
const { parseCallbackInput } = await import(root + "/pi-ai/src/registry/oauth/callback-server.ts");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;
const policy = rules.auth.providers.find((entry: any) => entry.id === "gitlab-duo-agent");
if (!policy) throw new Error("gitlab-duo-agent auth rule missing");

const nativeFetch = globalThis.fetch;
const nativeRandom = crypto.getRandomValues.bind(crypto);
const nativeNow = Date.now;
const now = 1_800_000_000_000;

let spec: any;
let requests: any[];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const headers = Object.fromEntries(request.headers);
		delete headers.host;
		requests.push({ method: request.method, url: "https://gitlab.com" + url.pathname + url.search, headers, body: await request.text() });
		return new Response(spec.body, { status: spec.status, headers: { "content-type": "application/json" } });
	},
});
const captureFetch = Object.assign(
	async (input: any, options?: any) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		if (url.origin !== "https://gitlab.com") throw new Error("Unexpected GitLab auth capture URL: " + url.href);
		return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`, { ...options, signal: undefined });
	},
	{ preconnect: nativeFetch.preconnect },
);

const pin = () => {
	let counter = 0;
	crypto.getRandomValues = (<T extends ArrayBufferView>(array: T) => {
		const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
		for (let i = 0; i < bytes.length; i++) bytes[i] = (counter++ * 7 + 3) & 0xff;
		return array;
	}) as typeof crypto.getRandomValues;
	Date.now = () => now;
};
const unpin = () => {
	crypto.getRandomValues = nativeRandom;
	Date.now = nativeNow;
	globalThis.fetch = nativeFetch;
};

const created = Math.floor(now / 1000) - 600;
const token = (extra: any = {}) => JSON.stringify({ access_token: "glo-access-1", token_type: "Bearer", expires_in: 7200, refresh_token: "glo-refresh-1", scope: "api", created_at: created, ...extra });
const state = "11111111-2222-4333-8444-555555555555";
const logins: any[] = [];
const refreshes: any[] = [];
// What a user may paste back: the VS Code redirect, its query alone, or the code.
const pastes = [
	`vscode://gitlab.gitlab-workflow/authentication?code=pasted-code&state=${state}`,
	`  vscode://gitlab.gitlab-workflow/authentication?code=pasted-code&state=other-state  `,
	"vscode://gitlab.gitlab-workflow/authentication?state=only-state",
	`?code=pasted-code&state=${state}`,
	"code=pasted-code",
	`pasted-code#${state}`,
	"pasted-code",
	"",
].map(input => ({ input, parsed: parseCallbackInput(input) }));
try {
	for (spec of [
		{ name: "token", status: 200, body: token() },
		{ name: "token-without-created-at", status: 200, body: token({ created_at: undefined }) },
		{ name: "token-without-refresh", status: 200, body: token({ refresh_token: undefined }) },
		{ name: "missing-access-token", status: 200, body: JSON.stringify({ refresh_token: "r", expires_in: 7200 }) },
		{ name: "rejected", status: 400, body: JSON.stringify({ error: "invalid_grant", error_description: "The provided authorization grant is invalid" }) },
	]) {
		requests = [];
		pin();
		const options = await resolveCallbackOptions(policy.login.callback, policy.id);
		const redirectUri = options.redirectUri;
		const flow = new DeclarativeOAuthCodeFlow({ fetch: captureFetch } as any, policy.login, policy, options);
		const authorize = await flow.generateAuthUrl(state, redirectUri);
		let credentials: any = null;
		let error: string | null = null;
		try {
			credentials = await flow.exchangeToken("auth-code", state, redirectUri);
		} catch (cause: any) {
			error = String(cause?.message ?? cause);
		}
		unpin();
		logins.push({ name: spec.name, response: { status: spec.status, body: spec.body }, callback: options, redirectUri, authorize, requests, credentials, error });
	}
	const refresh = createRefresh(policy);
	if (!refresh) throw new Error("gitlab-duo-agent has no refresh");
	const stored = { access: "glo-access-0", refresh: "glo-refresh-0", expires: now - 1000, email: "duo@example.com" };
	for (spec of [
		{ name: "rotated", status: 200, body: token({ access_token: "glo-access-2", refresh_token: "glo-refresh-2" }) },
		{ name: "unrotated", status: 200, body: token({ access_token: "glo-access-2", refresh_token: undefined }) },
		{ name: "without-created-at", status: 200, body: token({ access_token: "glo-access-2", created_at: undefined }) },
		{ name: "rejected", status: 400, body: JSON.stringify({ error: "invalid_grant", error_description: "The provided authorization grant is invalid" }) },
	]) {
		requests = [];
		pin();
		globalThis.fetch = captureFetch as any;
		let credentials: any = null;
		let error: string | null = null;
		try {
			credentials = await refresh(stored as any);
		} catch (cause: any) {
			error = String(cause?.message ?? cause);
		}
		unpin();
		refreshes.push({ name: spec.name, stored, response: { status: spec.status, body: spec.body }, requests, credentials, error });
	}
} finally {
	unpin();
	server.stop(true);
}
console.log(JSON.stringify({ source: "NeoPi OWN gitlab-duo-agent oauth-code rule via DeclarativeOAuthCodeFlow, createRefresh and parseCallbackInput", now, state, login: policy.login, refresh: policy.refresh, logins, refreshes, pastes }, null, 2));
