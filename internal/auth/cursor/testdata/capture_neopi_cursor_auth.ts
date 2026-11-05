// Runs NeoPi's OWN Cursor sign-in and renewal (pi-ai registry/oauth/cursor.ts:
// loginCursorHook, refreshCursorToken, refreshCursorHook) against scripted
// responses, with PKCE bytes, uuid, clock and poll sleeps pinned, recording
// the login URL, every request on the wire, the poll delays, and the
// credential or error each flow ends in.
//
//   bun capture_neopi_cursor_auth.ts > neopi_cursor_auth_capture.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const oauth = await import(root + "/pi-ai/src/registry/oauth/cursor.ts");

const nativeFetch = globalThis.fetch;
const nativeRandom = crypto.getRandomValues.bind(crypto);
const nativeUUID = crypto.randomUUID.bind(crypto);
const nativeNow = Date.now;
const nativeSleep = Bun.sleep;
const now = 1_800_000_000_000;
const jwt = (payload: unknown) => "eyJhbGciOiJub25lIn0." + Buffer.from(JSON.stringify(payload)).toString("base64url") + ".fake";
const access = jwt({ sub: "github|1245379", exp: 1_900_000_000 });
const rotated = jwt({ sub: "github|1245379", exp: 1_900_003_600 });

type Reply = { status: number; body: string } | "network-error";
let script: Map<string, Reply[]>;
let requests: any[];
let delays: number[];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const origin = request.headers.get("x-capture-origin")!;
		const headers = Object.fromEntries(request.headers);
		delete headers.host;
		delete headers["x-capture-origin"];
		requests.push({ method: request.method, url: origin + url.pathname + url.search, headers, body: await request.text() });
		const reply = script.get(url.pathname)?.shift();
		if (!reply || reply === "network-error") return new Response("unscripted", { status: 599 });
		return new Response(reply.body, { status: reply.status, headers: { "content-type": "application/json" } });
	},
});
const captureFetch = async (input: any, init?: any) => {
	const url = new URL(input instanceof Request ? input.url : String(input));
	if (url.origin !== "https://api2.cursor.sh" && url.origin !== "https://cursor.com") throw new Error("Unexpected Cursor capture URL: " + url.href);
	if (script.get(url.pathname)?.[0] === "network-error") {
		script.get(url.pathname)!.shift();
		requests.push({ method: init?.method ?? "GET", url: url.href, networkError: true });
		throw new TypeError("fetch failed");
	}
	const headers = new Headers(init?.headers);
	headers.set("x-capture-origin", url.origin);
	return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`, { ...init, headers, signal: undefined });
};

const pin = () => {
	let counter = 0;
	crypto.getRandomValues = (<T extends ArrayBufferView>(array: T) => {
		const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
		for (let i = 0; i < bytes.length; i++) bytes[i] = (counter++ * 7 + 3) & 0xff;
		return array;
	}) as typeof crypto.getRandomValues;
	crypto.randomUUID = () => "11111111-2222-4333-8444-555555555555";
	Date.now = () => now;
	(Bun as any).sleep = async (ms: number) => {
		delays.push(ms);
	};
	globalThis.fetch = Object.assign(captureFetch, { preconnect: nativeFetch.preconnect }) as typeof fetch;
};
const unpin = () => {
	crypto.getRandomValues = nativeRandom;
	crypto.randomUUID = nativeUUID;
	Date.now = nativeNow;
	(Bun as any).sleep = nativeSleep;
	globalThis.fetch = nativeFetch;
};
const reply = (status: number, body: unknown): Reply => ({ status, body: typeof body === "string" ? body : JSON.stringify(body) });
const me = (authId: string, email: string) => reply(200, { authId, email, name: "Shay" });

const run = async (name: string, replies: Record<string, Reply[]>, flow: () => Promise<unknown>) => {
	script = new Map(Object.entries(replies).map(([path, list]) => [path, [...list]]));
	requests = [];
	delays = [];
	let result: unknown = null;
	let error: string | null = null;
	pin();
	try {
		result = await flow();
	} catch (cause: any) {
		error = String(cause?.message ?? cause);
	} finally {
		unpin();
	}
	return { name, replies, requests, delays, result, error };
};

const poll = "/auth/poll";
const me_ = "/aiserver.v1.DashboardService/GetMe";
const token = "/oauth/token";
const scenarios: any[] = [];
try {
	let loginUrl = "";
	const login = () =>
		oauth.loginCursorHook({ onAuth: ({ url }: { url: string }) => (loginUrl = url), fetch: globalThis.fetch } as any);
	for (const [name, replies] of [
		["login-after-pending", { [poll]: [reply(404, ""), reply(404, ""), reply(200, { accessToken: access, refreshToken: "refresh-1" })], [me_]: [me("github|1245379", " shay@example.test ")] }],
		["login-profile-other-account", { [poll]: [reply(200, { accessToken: access, refreshToken: "refresh-1" })], [me_]: [me("github|999", "other@example.test")] }],
		["login-profile-fails", { [poll]: [reply(200, { accessToken: access, refreshToken: "refresh-1" })], [me_]: [reply(500, "{}")] }],
		["login-opaque-token", { [poll]: [reply(200, { accessToken: "opaque", refreshToken: "refresh-1" })] }],
		["login-errors-reset-by-pending", { [poll]: [reply(500, ""), "network-error", reply(404, ""), reply(200, "not json"), reply(502, ""), reply(200, { accessToken: access, refreshToken: "r" })], [me_]: [me("github|1245379", "shay@example.test")] }],
		["login-three-errors", { [poll]: [reply(500, ""), reply(200, "not json"), "network-error"] }],
	] as const) {
		loginUrl = "";
		const scenario = await run(name, replies as any, login);
		scenarios.push({ ...scenario, loginUrl });
	}
	for (const [name, replies, credentials] of [
		["refresh-access-only", { [token]: [reply(200, { access_token: rotated })] }, { access, refresh: "refresh-1", expires: 0, email: "shay@example.test" }],
		["refresh-rotated", { [token]: [reply(200, { access_token: rotated, refresh_token: "refresh-2" })] }, { access, refresh: "refresh-1", expires: 0, email: "shay@example.test" }],
		["refresh-adds-email", { [token]: [reply(200, { access_token: rotated })], [me_]: [me("github|1245379", "shay@example.test")] }, { access, refresh: "refresh-1", expires: 0 }],
		["refresh-should-logout", { [token]: [reply(200, { access_token: "", shouldLogout: true })] }, { access, refresh: "refresh-1", expires: 0, email: "e" }],
		["refresh-no-token", { [token]: [reply(200, {})] }, { access, refresh: "refresh-1", expires: 0, email: "e" }],
		["refresh-rejected", { [token]: [reply(401, '{"error":"invalid_grant"}')] }, { access, refresh: "refresh-1", expires: 0, email: "e" }],
	] as const) {
		scenarios.push({ ...(await run(name, replies as any, () => oauth.refreshCursorHook(credentials as any))), credentials });
	}
} finally {
	unpin();
	server.stop(true);
}
console.log(
	JSON.stringify(
		{ source: "NeoPi OWN registry/oauth/cursor.ts (loginCursorHook, refreshCursorHook)", now, verifierBytes: "(i*7+3)&0xff", uuid: "11111111-2222-4333-8444-555555555555", scenarios },
		null,
		2,
	),
);
