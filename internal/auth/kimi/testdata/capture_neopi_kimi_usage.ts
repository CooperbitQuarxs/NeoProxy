// Executes NeoPi's OWN kimiUsageProvider against synthetic /coding/v1/usages
// payloads on a loopback server, recording the request it sends and the
// limits it reports.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { kimiUsageProvider } = await import(root + "/pi-ai/src/usage/kimi.ts");
const { getKimiCommonHeaders } = await import(root + "/pi-ai/src/registry/oauth/kimi.ts");
const nativeFetch = globalThis.fetch;
const token = "kimi-usage-capture-token";
const reset = "2100-01-01T00:00:00Z";
let spec: any, requests: any[];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		requests.push({ method: request.method, url: "https://api.kimi.com" + url.pathname + url.search, headers: Object.fromEntries(request.headers) });
		return Response.json(spec.payload, { status: spec.status ?? 200 });
	},
});
const captureFetch = Object.assign(
	async (input: any, options?: any) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		if (url.origin !== "https://api.kimi.com") throw new Error("Unexpected usage URL: " + url.href);
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
			name: "weekly-and-5h",
			payload: {
				usage: { limit: "100", used: "37", remaining: "63", resetTime: reset },
				limits: [{ window: { duration: 300, timeUnit: "TIME_UNIT_MINUTE" }, detail: { limit: "100", used: "12", remaining: "88", resetTime: reset } }],
			},
		},
		{
			name: "remaining-only-and-aggregates",
			payload: {
				usage: { limit: 2000, remaining: 500, resetTime: reset },
				limits: [
					{ window: { duration: 5, timeUnit: "TIME_UNIT_HOUR" }, detail: { limit: 50, remaining: 50 } },
					{ name: "Daily", window: { duration: 1, timeUnit: "TIME_UNIT_DAY" }, detail: { limit: 10, used: 15, resetAt: 4102444800 } },
				],
				usages: { limit_5h: { used_ratio: 0.9 }, limit_month_total: { used_ratio: 0.25, resetTime: reset }, limit_month_code: { used_ratio: 1.4 } },
			},
		},
		{
			name: "total-quota",
			payload: { totalQuota: { limit: 1000, used: 250, window: { duration: 7, timeUnit: "TIME_UNIT_DAY" } } },
		},
		{ name: "no-plan", payload: {} },
		{ name: "invalid-response", payload: { usage: { note: "nothing" } } },
		{ name: "http-error", status: 401, payload: { error: "unauthorized" } },
	]) {
		requests = [];
		const report = await kimiUsageProvider.fetchUsage(
			{ provider: "kimi-code", credential: { type: "oauth", accessToken: token, accountId: "acct-1" } } as any,
			{ fetch: captureFetch } as any,
		);
		scenarios.push({
			...spec,
			limits:
				report?.limits.map((limit: any) => ({
					label: limit.label,
					windowId: limit.window?.id ?? null,
					durationMs: limit.window?.durationMs ?? null,
					resetsAt: limit.window?.resetsAt ?? null,
					usedFraction: limit.amount.usedFraction ?? null,
				})) ?? null,
			request: requests[0],
		});
	}
	console.log(JSON.stringify({ source: "NeoPi OWN kimiUsageProvider", token, kimiHeaders: getKimiCommonHeaders(), scenarios }, null, 2));
} finally {
	server.stop(true);
}
