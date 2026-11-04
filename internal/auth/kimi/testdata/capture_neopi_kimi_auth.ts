// Executes NeoPi's OWN kimi-code device login and refresh (the compiled auth
// rule through registry/engine, with its kimi-fingerprint headers hook)
// against synthetic, fail-closed responses.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { createDeviceCodeLogin } = await import(root + "/pi-ai/src/registry/engine/device-code.ts");
const { createRefresh } = await import(root + "/pi-ai/src/registry/engine/refresh.ts");
const { getKimiCommonHeaders } = await import(root + "/pi-ai/src/registry/oauth/kimi.ts");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;
const policy = rules.auth.providers.find((entry: any) => entry.id === "kimi-code");
if (!policy) throw new Error("kimi-code auth rule missing");
const nativeFetch = globalThis.fetch;
const nativeSleep = Bun.sleep;
const jwt = (payload: unknown) => "eyJhbGciOiJub25lIn0." + Buffer.from(JSON.stringify(payload)).toString("base64url") + ".fake";
const access = jwt({ user_id: "kimi-user-fake", sub: "kimi-sub-fake", exp: 4102444800 });
const tokens = { access_token: access, refresh_token: "kimi-refresh-fake", expires_in: 3600, token_type: "Bearer", scope: "kimi-code" };
const device = { device_code: "device-code-fake", user_code: "FAKE-CODE", verification_uri: "https://www.kimi.com/code/authorize_device", verification_uri_complete: "https://www.kimi.com/code/authorize_device?user_code=FAKE-CODE", interval: 5, expires_in: 900 };
let spec: any, requests: any[], waits: number[], poll: number;
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		requests.push({ method: request.method, url: "https://auth.kimi.com" + url.pathname + url.search, headers: Object.fromEntries(request.headers), body: await request.text() });
		if (url.pathname === "/api/oauth/device_authorization") return Response.json(spec.device ?? device, { status: spec.deviceStatus ?? 200 });
		const response = spec.polls[Math.min(poll++, spec.polls.length - 1)];
		return Response.json(response.body ?? {}, { status: response.status });
	},
});
const captureFetch = Object.assign(
	async (input: any, options?: any) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		if (url.origin !== "https://auth.kimi.com") throw new Error("Unexpected Kimi auth capture URL: " + url.href);
		const headers = new Headers(options?.headers);
		headers.set("host", url.host);
		return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`, { ...options, headers, signal: undefined });
	},
	{ preconnect: nativeFetch.preconnect },
);
const login = createDeviceCodeLogin(policy.login, "kimi-code");
const refresh = createRefresh(policy)!;
const scenarios: any[] = [];
try {
	globalThis.fetch = captureFetch as typeof fetch;
	Bun.sleep = (async (duration: number) => {
		waits.push(duration);
	}) as typeof Bun.sleep;
	for (spec of [
		{ kind: "login", name: "pending-slow-down-then-authorized", polls: [{ status: 400, body: { error: "authorization_pending" } }, { status: 400, body: { error: "slow_down" } }, { status: 200, body: tokens }] },
		{ kind: "login", name: "access-denied", polls: [{ status: 400, body: { error: "access_denied" } }] },
		{ kind: "login", name: "expired-token", polls: [{ status: 400, body: { error: "expired_token" } }] },
		{ kind: "login", name: "unknown-error", polls: [{ status: 400, body: { error: "invalid_client", error_description: "Client is not allowed" } }] },
		{ kind: "login", name: "device-rejected", deviceStatus: 400, device: { error: "invalid_client" }, polls: [] },
		{ kind: "login", name: "missing-expires-in", polls: [{ status: 200, body: { access_token: access, refresh_token: "r" } }] },
		{ kind: "refresh", name: "refresh-rotates", polls: [{ status: 200, body: { ...tokens, refresh_token: "kimi-refresh-rotated" } }] },
		{ kind: "refresh", name: "refresh-keeps-unrotated", polls: [{ status: 200, body: { access_token: access, expires_in: 1800 } }] },
		{ kind: "refresh", name: "refresh-rejected", polls: [{ status: 401, body: { error: "invalid_grant", error_description: "Refresh token revoked" } }] },
	]) {
		requests = [];
		waits = [];
		poll = 0;
		let auth: unknown, credential: unknown, error: string | undefined;
		const before = Date.now();
		try {
			credential =
				spec.kind === "login"
					? await login({ onAuth: (value: unknown) => { auth = value; } } as any)
					: await refresh({ access: "old-access", refresh: "kimi-refresh-stored", expires: 0 } as any);
		} catch (failure) {
			error = String(failure instanceof Error ? failure.message : failure);
		}
		scenarios.push({ ...spec, auth, credential, error, before, after: Date.now(), waits, requests });
	}
	console.log(JSON.stringify({ source: "NeoPi OWN createDeviceCodeLogin/createRefresh (kimi-code)", kimiHeaders: getKimiCommonHeaders(), normalizations: ["synthetic credentials only", "Bun.sleep records requested waits without sleeping", "expiry is compared relative to request time"], scenarios }, null, 2));
} finally {
	globalThis.fetch = nativeFetch;
	Bun.sleep = nativeSleep;
	server.stop(true);
}
