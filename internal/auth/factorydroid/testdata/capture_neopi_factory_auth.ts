// Executes NeoPi's OWN factory-droid device login and refresh (the compiled
// auth rule through registry/engine, with its factory-droid-region hook)
// against synthetic, fail-closed WorkOS and Factory responses. Writes the
// scenarios as JSON to stdout:
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_factory_auth.ts > neopi_factory_auth_capture.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { createDeviceCodeLogin } = await import(root + "/pi-ai/src/registry/engine/device-code.ts");
const { createRefresh } = await import(root + "/pi-ai/src/registry/engine/refresh.ts");
const { authPolicyFor } = await import(root + "/pi-catalog/src/compat/auth.ts");
const policy = authPolicyFor("factory-droid");
if (!policy) throw new Error("factory-droid auth rule missing");
const nativeFetch = globalThis.fetch;
const nativeSleep = Bun.sleep;
const jwt = (payload: unknown) => "eyJhbGciOiJub25lIn0." + Buffer.from(JSON.stringify(payload)).toString("base64url") + ".fake";
const orgAccess = jwt({ sub: "user_fake", email: "claim@example.test", external_org_id: "org-factory-fake", exp: 4102444800 });
const bareAccess = jwt({ sub: "user_fake", exp: 4102444800 });
const scopedAccess = jwt({ sub: "user_fake", external_org_id: "org-factory-scoped", exp: 4102444900 });
const user = { id: "user_fake", email: "body@example.test" };
const tokens = { access_token: orgAccess, refresh_token: "workos-refresh-fake", user, organization_id: "org_workos_fake" };
const device = { device_code: "device-code-fake", user_code: "FAKE-CODE", verification_uri: "https://auth.factory.ai/device", verification_uri_complete: "https://auth.factory.ai/device?user_code=FAKE-CODE", interval: 5, expires_in: 300 };
const hosts = ["https://api.workos.com", "https://api.factory.ai", "https://api.eu.factory.ai"];
let spec: any, requests: any[], waits: number[], polls: number, refreshes: number;
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const origin = request.headers.get("x-capture-origin")!;
		const headers = Object.fromEntries(request.headers);
		delete headers["x-capture-origin"];
		const body = await request.text();
		requests.push({ method: request.method, url: origin + url.pathname + url.search, headers, body });
		const answer = (value: any) => Response.json(value?.body ?? {}, { status: value?.status ?? 200 });
		if (url.pathname === "/user_management/authorize/device") return Response.json(spec.device ?? device, { status: spec.deviceStatus ?? 200 });
		if (url.pathname === "/user_management/authenticate") {
			if (body.includes("grant_type=refresh_token")) return answer(spec.refreshes[Math.min(refreshes++, spec.refreshes.length - 1)]);
			return answer(spec.polls[Math.min(polls++, spec.polls.length - 1)]);
		}
		if (url.pathname === "/api/cli/whoami") {
			const auth = request.headers.get("authorization") ?? "";
			const entry = (spec.whoami ?? []).find((candidate: any) => auth.includes(candidate.token ?? "")) ?? { status: 200, body: {} };
			return answer(entry);
		}
		if (url.pathname === "/api/cli/org") return answer(spec.org ?? { status: 404 });
		return new Response("unexpected", { status: 599 });
	},
});
const captureFetch = Object.assign(
	async (input: any, options?: any) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		if (!hosts.includes(url.origin)) throw new Error("Unexpected Factory auth capture URL: " + url.href);
		const headers = new Headers(options?.headers);
		headers.set("host", url.host);
		headers.set("x-capture-origin", url.origin);
		return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`, { ...options, headers, signal: undefined });
	},
	{ preconnect: nativeFetch.preconnect },
);
const login = createDeviceCodeLogin(policy.login, "factory-droid");
const refresh = createRefresh(policy)!;
const stored = {
	access: "old-access",
	refresh: "workos-refresh-stored",
	expires: 0,
	email: "stored@example.test",
	accountId: "user_fake",
	orgId: "org-factory-fake",
	activeOrganizationId: "org_workos_fake",
	region: "eu",
	inferenceRegion: "eu",
};
const scenarios: any[] = [];
try {
	globalThis.fetch = captureFetch as typeof fetch;
	Bun.sleep = (async (duration: number) => {
		waits.push(duration);
	}) as typeof Bun.sleep;
	for (spec of [
		{ kind: "login", name: "pending-slow-down-then-authorized", polls: [{ status: 400, body: { error: "authorization_pending" } }, { status: 400, body: { error: "slow_down" } }, { status: 200, body: tokens }], whoami: [{ status: 200, body: { orgId: "org-factory-fake", region: "global", inferenceRegion: "us" } }] },
		{ kind: "login", name: "whoami-eu-residency", polls: [{ status: 200, body: tokens }], whoami: [{ status: 200, body: { orgId: "org-factory-fake", region: "eu" } }] },
		{ kind: "login", name: "whoami-rejected", polls: [{ status: 200, body: tokens }], whoami: [{ status: 403, body: { error: "forbidden" } }] },
		{
			kind: "login",
			name: "no-org-scopes-to-workos-org",
			polls: [{ status: 200, body: { access_token: bareAccess, refresh_token: "workos-refresh-bare", user } }],
			whoami: [{ token: bareAccess, status: 200, body: {} }, { token: scopedAccess, status: 200, body: { orgId: "org-factory-scoped", region: "global" } }],
			org: { status: 200, body: { workosOrgIds: ["org_workos_scoped"] } },
			refreshes: [{ status: 200, body: { access_token: scopedAccess, refresh_token: "workos-refresh-scoped" } }],
		},
		{ kind: "login", name: "no-org-unresolved", polls: [{ status: 200, body: { access_token: bareAccess, refresh_token: "r", user } }], whoami: [{ status: 200, body: {} }], org: { status: 404 } },
		{ kind: "login", name: "missing-refresh-token", polls: [{ status: 200, body: { access_token: orgAccess, user } }] },
		{ kind: "login", name: "access-denied", polls: [{ status: 400, body: { error: "access_denied" } }] },
		{ kind: "login", name: "expired-token", polls: [{ status: 400, body: { error: "expired_token" } }] },
		{ kind: "login", name: "unknown-error", polls: [{ status: 400, body: { error: "invalid_client", error_description: "Client is not allowed" } }] },
		{ kind: "login", name: "device-rejected", deviceStatus: 400, device: { error: "invalid_client" }, polls: [] },
		{ kind: "refresh", name: "refresh-same-org", refreshes: [{ status: 200, body: { access_token: orgAccess, refresh_token: "workos-refresh-rotated", organization_id: "org_workos_fake" } }], whoami: [{ status: 200, body: { orgId: "org-factory-fake" } }] },
		{ kind: "refresh", name: "refresh-whoami-fails-keeps-scope", refreshes: [{ status: 200, body: { access_token: orgAccess, refresh_token: "workos-refresh-rotated" } }], whoami: [{ status: 500, body: {} }] },
		{ kind: "refresh", name: "refresh-org-changed", refreshes: [{ status: 200, body: { access_token: scopedAccess, refresh_token: "workos-refresh-rotated", organization_id: "org_workos_other" } }], whoami: [{ status: 200, body: {} }] },
		{ kind: "refresh", name: "refresh-rejected", refreshes: [{ status: 401, body: { error: "invalid_grant", error_description: "Refresh token revoked" } }] },
	]) {
		requests = [];
		waits = [];
		polls = 0;
		refreshes = 0;
		let auth: unknown, credential: unknown, error: string | undefined;
		const before = Date.now();
		try {
			credential =
				spec.kind === "login"
					? await login({ fetch: captureFetch, onAuth: (value: unknown) => { auth = value; } } as any)
					: await refresh({ ...stored } as any);
		} catch (failure) {
			error = String(failure instanceof Error ? failure.message : failure);
		}
		scenarios.push({ ...spec, auth, credential, error, before, after: Date.now(), waits, requests });
	}
	console.log(JSON.stringify({ source: "NeoPi OWN createDeviceCodeLogin/createRefresh (factory-droid) with the factory-droid-region hook", stored, normalizations: ["synthetic credentials only", "Bun.sleep records requested waits without sleeping", "x-capture-origin names the host the request was addressed to"], scenarios }, null, 2));
} finally {
	globalThis.fetch = nativeFetch;
	Bun.sleep = nativeSleep;
	server.stop(true);
}
