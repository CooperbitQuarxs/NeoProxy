// Executes NeoPi's OWN zaiUsageProvider against synthetic quota payloads on a
// loopback server. The quota request is captured; the model-usage request
// that follows only feeds report metadata.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { zaiUsageProvider } = await import(root + "/pi-ai/src/usage/zai.ts");
const nativeFetch = globalThis.fetch;
const key = "fixtureid0000000000000000000000.fixturesecret00000";
const reset = 4102444800000;
let spec: any, requests: any[];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		requests.push({ method: request.method, url: "https://api.z.ai" + url.pathname + url.search, headers: Object.fromEntries(request.headers), body: await request.text() });
		if (url.pathname === "/api/monitor/usage/quota/limit") return Response.json(spec.quota, { status: spec.status ?? 200 });
		return Response.json({ code: 200, success: true, data: {} });
	},
});
const captureFetch = Object.assign(
	async (input: any, options?: any) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		if (url.origin !== "https://api.z.ai") throw new Error("Unexpected usage URL: " + url.href);
		const headers = new Headers(options?.headers);
		headers.set("host", url.host);
		return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`, { ...options, headers });
	},
	{ preconnect: nativeFetch.preconnect },
);
const scenarios: any[] = [];
try {
	for (spec of [
		{
			name: "coding-plan-credits",
			quota: {
				code: 200,
				success: true,
				data: {
					level: "pro",
					limits: [
						{ type: "CREDIT_LIMIT", unit: 3, number: 5, usage: 12000, currentValue: 1438, percentage: 11, nextResetTime: reset },
						{ type: "CREDIT_LIMIT", unit: 6, number: 1, usage: 60000, currentValue: 61000, percentage: 100, nextResetTime: reset / 1000 },
						{ type: "TIME_LIMIT", unit: 5, number: 1, usage: 1000, currentValue: 10, percentage: 1, usageDetails: [{ modelCode: "search-prime", usage: 4 }, { modelCode: "web-reader", usage: 3 }, { modelCode: "zread", usage: 3 }] },
					],
				},
			},
		},
		{
			name: "tokens-and-requests",
			quota: {
				code: 200,
				success: true,
				data: {
					limits: [
						{ type: "TOKENS_LIMIT", unit: 3, number: 5, percentage: "37", nextResetTime: String(reset) },
						{ type: "TIME_LIMIT", unit: 4, number: 1, usage: 500, currentValue: 125 },
						{ type: "TOKENS_LIMIT", unit: 9, number: 2 },
						{ type: "UNKNOWN_LIMIT", unit: 3, number: 5, percentage: 50 },
					],
				},
			},
		},
		{ name: "no-coding-plan", quota: { code: 500, msg: "当前用户不存在coding plan", success: false } },
		{ name: "invalid-response", quota: { code: 1001, msg: "token expired", success: false } },
		{ name: "http-error", status: 401, quota: { msg: "unauthorized" } },
	]) {
		requests = [];
		const report = await zaiUsageProvider.fetchUsage(
			{ provider: "zai", credential: { type: "api_key", apiKey: key } } as any,
			{ fetch: captureFetch } as any,
		);
		scenarios.push({
			...spec,
			plan: report?.metadata?.planType ?? null,
			limits:
				report?.limits.map((limit: any) => ({
					id: limit.id,
					windowId: limit.window.id,
					durationMs: limit.window.durationMs ?? null,
					resetsAt: limit.window.resetsAt ?? null,
					usedFraction: limit.amount.usedFraction ?? null,
					shared: limit.scope.shared,
				})) ?? null,
			quotaRequest: requests[0],
		});
	}
	console.log(JSON.stringify({ source: "NeoPi OWN zaiUsageProvider", key, scenarios }, null, 2));
} finally {
	server.stop(true);
}
