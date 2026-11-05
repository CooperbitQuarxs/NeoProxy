// Runs NeoPi's OWN cursorUsageProvider.fetchUsage (pi-ai usage/cursor.ts)
// against scripted /auth/usage, usage-summary, GetPlanInfo and GetMe
// replies, recording every request on the wire and the report it builds.
//
//   bun capture_neopi_cursor_usage.ts > neopi_cursor_usage_capture.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { cursorUsageProvider } = await import(root + "/pi-ai/src/usage/cursor.ts");

const nativeFetch = globalThis.fetch;
const jwt = (payload: unknown) => "eyJhbGciOiJub25lIn0." + Buffer.from(JSON.stringify(payload)).toString("base64url") + ".fake";
const access = jwt({ sub: "github|1245379", exp: 1_900_000_000 });

type Reply = { status: number; body: unknown };
let script: Record<string, Reply>;
let requests: any[];
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
		const reply = script[origin + url.pathname];
		if (!reply) return new Response("unscripted", { status: 500 });
		return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "content-type": "application/json" } });
	},
});
const captureFetch = Object.assign(
	async (input: any, init?: any) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		const headers = new Headers(init?.headers);
		headers.set("x-capture-origin", url.origin);
		return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`, { ...init, headers, redirect: "manual" });
	},
	{ preconnect: nativeFetch.preconnect },
);

const ok = (body: unknown): Reply => ({ status: 200, body });
const api = "https://api2.cursor.sh";
const scenarios = [
	{
		name: "plan-rails",
		baseUrl: undefined,
		email: "stored@example.invalid",
		script: {
			[api + "/auth/usage"]: ok({ "gpt-4": { numRequests: 0, maxRequestUsage: null }, startOfMonth: "2026-10-15T00:00:00.000Z" }),
			"https://cursor.com/api/usage-summary": ok({
				billingCycleStart: "2026-10-15T00:00:00.000Z",
				billingCycleEnd: "2026-11-15T00:00:00.000Z",
				membershipType: "pro",
				individualUsage: {
					plan: { enabled: true, used: 1500, limit: 2000, remaining: 500, autoPercentUsed: 12.5, apiPercentUsed: 40, totalPercentUsed: 30 },
					onDemand: { enabled: true, used: 100, limit: 5000 },
				},
			}),
			[api + "/aiserver.v1.DashboardService/GetMe"]: ok({ authId: "github|1245379", email: " me@example.invalid " }),
		},
	},
	{
		name: "unified",
		baseUrl: undefined,
		email: undefined,
		script: {
			[api + "/auth/usage"]: { status: 503, body: {} },
			"https://cursor.com/api/usage-summary": ok({
				membershipType: "unified_pro",
				billingCycleEnd: "2026-11-15T00:00:00.000Z",
				individualUsage: { plan: { enabled: true, used: 300, limit: 1000, totalPercentUsed: 35 }, onDemand: { enabled: false } },
			}),
			[api + "/aiserver.v1.DashboardService/GetPlanInfo"]: ok({ planInfo: { includedUsageResetsAt: "1800500000000" } }),
			[api + "/aiserver.v1.DashboardService/GetMe"]: ok({ authId: "github|1245379", email: "me@example.invalid" }),
		},
	},
	{
		name: "overall-and-requests",
		baseUrl: undefined,
		email: "stored@example.invalid",
		script: {
			[api + "/auth/usage"]: ok({ "gpt-4": { numRequests: 5, maxRequestUsage: 500 }, "gpt-4-32k": { numRequests: 0 }, startOfMonth: 1790000000 }),
			"https://cursor.com/api/usage-summary": ok({
				startOfMonth: 1790000000,
				individualUsage: { overall: { enabled: true, used: 0, limit: 10000, remaining: 2500 }, onDemand: { enabled: true, used: 50, limit: null } },
			}),
			[api + "/aiserver.v1.DashboardService/GetMe"]: ok({ authId: "auth0|someone-else", email: "other@example.invalid" }),
		},
	},
	{
		name: "custom-host",
		baseUrl: "https://cursor.example/",
		email: undefined,
		script: { "https://cursor.example/auth/usage": ok({ planUsage: { used: 12, limit: 20 }, "usd-bucket": { usdUsed: "3", usdLimit: "0" } }) },
	},
	{ name: "nothing", baseUrl: undefined, email: undefined, script: {} },
];

const captured: any[] = [];
try {
	for (const scenario of scenarios) {
		script = scenario.script;
		requests = [];
		const report = await cursorUsageProvider.fetchUsage(
			{ provider: "cursor", baseUrl: scenario.baseUrl, credential: { type: "oauth", accessToken: access, accountId: "acct-1", email: scenario.email } } as any,
			{ fetch: captureFetch } as any,
		);
		requests.sort((a, b) => (a.url < b.url ? -1 : a.url > b.url ? 1 : 0));
		captured.push({
			name: scenario.name,
			baseUrl: scenario.baseUrl ?? "",
			email: scenario.email ?? "",
			replies: Object.fromEntries(Object.entries(scenario.script).map(([url, reply]) => [url, { status: reply.status, body: JSON.stringify(reply.body) }])),
			requests,
			report: report ? { provider: report.provider, limits: report.limits, metadata: report.metadata ?? {} } : null,
		});
	}
	console.log(JSON.stringify({ source: "NeoPi OWN cursorUsageProvider", token: access, scenarios: captured }, null, 2));
} finally {
	server.stop(true);
}
