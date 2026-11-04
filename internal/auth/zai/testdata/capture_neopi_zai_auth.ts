// Executes NeoPi's OWN zai-coding-plan sign-in (the compiled oauth-code rule
// through DeclarativeOAuthCodeFlow, then its zai-mint-key after-exchange hook)
// against synthetic, fail-closed responses on a loopback server.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { DeclarativeOAuthCodeFlow, resolveCallbackOptions } = await import(root + "/pi-ai/src/registry/engine/oauth-code.ts");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;
const policy = rules.auth.providers.find((entry: any) => entry.id === "zai-coding-plan");
if (!policy) throw new Error("zai-coding-plan auth rule missing");

const tokenOK = { code: 0, msg: "success", data: { zai: { access_token: "oauth-access-fake" }, user: { email: "Zai@Example.Invalid", id: 4242 } } };
const login = { code: 200, msg: "ok", success: true, data: { access_token: "biz-token-fake" } };
const customer = {
	code: 200,
	success: true,
	data: {
		organizations: [
			{ organizationId: "org-other", isDefault: false, projects: [{ projectId: "proj-other", isDefault: true }] },
			{ organizationId: "org-main", isDefault: true, projects: [{ projectId: "proj-first", isDefault: false }, { projectId: "proj-main", isDefault: true }] },
		],
	},
};
const keysWith = (keys: unknown[]) => ({ code: 200, success: true, data: { list: keys } });
const copied = { code: 200, success: true, data: { secretKey: "secret-fake-0000" } };
type Reply = { status?: number; body: unknown };
let spec: any;
let requests: any[];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const original = request.headers.get("x-capture-url")!;
		const headers = Object.fromEntries(request.headers);
		delete headers["x-capture-url"];
		requests.push({ method: request.method, url: original, headers, body: await request.text() });
		const path = new URL(original).pathname;
		let reply: Reply | undefined;
		if (path === "/api/v1/oauth/token") reply = spec.token ?? { body: tokenOK };
		else if (path === "/api/auth/z/login") reply = spec.login ?? { body: login };
		else if (path === "/api/biz/customer/getCustomerInfo") reply = spec.customer ?? { body: customer };
		else if (path.includes("/copy/")) reply = spec.copy ?? { body: copied };
		else if (path.endsWith("/api_keys") && request.method === "GET") reply = spec.list ?? { body: keysWith([{ name: "zcode-api-key", apiKey: "ak-zcode" }, { name: "oh-my-pi", apiKey: "ak-existing" }]) };
		else if (path.endsWith("/api_keys") && request.method === "POST") reply = spec.create ?? { body: { code: 200, success: true, data: { apiKey: "ak-created" } } };
		if (!reply) return new Response("unexpected " + url.pathname, { status: 599 });
		return Response.json(reply.body, { status: reply.status ?? 200 });
	},
});
const captureFetch = Object.assign(
	async (input: any, options?: any) => {
		const target = new URL(input instanceof Request ? input.url : String(input));
		if (target.origin !== "https://zcode.z.ai" && target.origin !== "https://api.z.ai") throw new Error("Unexpected Z.ai capture URL: " + target.href);
		const headers = new Headers(options?.headers);
		headers.set("host", target.host);
		headers.set("x-capture-url", target.href);
		return fetch(`http://127.0.0.1:${server.port}${target.pathname}${target.search}`, { ...options, headers, signal: undefined });
	},
	{ preconnect: fetch.preconnect },
);

const scenarios: any[] = [];
try {
	for (spec of [
		{ name: "existing-key" },
		{ name: "creates-key", list: { body: keysWith([{ name: "zcode-api-key", apiKey: "ak-zcode" }]) } },
		{ name: "bare-array-list", list: { body: { code: 200, success: true, data: [{ name: "oh-my-pi", apiKey: "ak-bare" }] } } },
		{ name: "code-with-state-fragment", code: "the-code#fragment-state" },
		{ name: "token-http-error", token: { status: 400, body: { code: 400, msg: "invalid code" } } },
		{ name: "token-envelope-error", token: { body: { code: 1001, msg: "authorization code expired" } } },
		{ name: "login-envelope-error", login: { body: { code: 500, msg: "login rejected", success: false } } },
		{ name: "login-no-token", login: { body: { code: 200, success: true, data: {} } } },
		{ name: "no-organization", customer: { body: { code: 200, success: true, data: { organizations: [] } } } },
		{ name: "create-without-api-key", list: { body: keysWith([]) }, create: { body: { code: 200, success: true, data: {} } } },
		{ name: "copy-without-secret", copy: { body: { code: 200, success: true, data: {} } } },
		{ name: "list-http-error", list: { status: 401, body: { msg: "expired" } } },
	]) {
		requests = [];
		const controller = new AbortController();
		const ctrl = { fetch: captureFetch, signal: controller.signal, onProgress() {}, onAuth() {} } as any;
		const options = await resolveCallbackOptions(policy.login.callback, policy.id);
		const flow = new DeclarativeOAuthCodeFlow(ctrl, policy.login, policy, options);
		const state = "0123456789abcdef0123456789abcdef";
		const redirectUri = options.redirectUri;
		let credential: unknown, error: string | undefined;
		const auth = await flow.generateAuthUrl(state, redirectUri);
		try {
			credential = await flow.exchangeToken(spec.code ?? "the-code", state, redirectUri);
		} catch (failure) {
			error = String(failure instanceof Error ? failure.message : failure);
		}
		scenarios.push({ ...spec, state, redirectUri, auth, credential, error, requests });
	}
	console.log(JSON.stringify({ source: "NeoPi OWN DeclarativeOAuthCodeFlow (zai-coding-plan) + zai-mint-key hook", scenarios }, null, 2));
} finally {
	server.stop(true);
}
