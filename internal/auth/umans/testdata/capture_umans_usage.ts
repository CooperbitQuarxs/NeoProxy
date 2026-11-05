// Runs NeoPi's OWN umans usage provider (ai/src/usage/umans.ts) over fixed
// /v1/usage answers through a fake fetch: no request leaves the process. Each
// case records the request NeoPi makes and the report (or error) it returns.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { umansUsageProvider } = await import(root + "/pi-ai/src/usage/umans.ts");

const apiKey = "sk-umans-usage-capture-0000";
const cases: { name: string; baseUrl?: string; status?: number; body: unknown }[] = [
	{
		name: "weighted-with-burst",
		body: {
			plan: { display_name: "Pro" },
			limits: { requests: { limit: 200, hard_cap: 400, window_seconds: 18000 }, concurrency: { limit: 4 } },
			window: { started_at: "2026-11-04T10:00:00Z", resets_at: "2026-11-04T15:00:00.000Z", remaining_minutes: 100 },
			usage: { requests_in_window: 250, remaining_requests: 0, weighted_in_window: 190.5, weighted_remaining_requests: 9.5, concurrent_sessions: 1, priority: { low: true } },
		},
	},
	{
		name: "legacy-raw-only",
		baseUrl: "https://api.code.umans.ai/v1/",
		body: { limits: { requests: { limit: 100 } }, window: { resets_at: "not a date" }, usage: { requests_in_window: 120, remaining_requests: 0 } },
	},
	{
		name: "weighted-without-burst",
		body: { limits: { requests: { limit: 100, hard_cap: null } }, usage: { requests_in_window: 300, weighted_in_window: 95 } },
	},
	{
		name: "burst-without-raw",
		baseUrl: "https://gateway.example/team/umans/V1",
		body: { limits: { requests: { limit: 100, hard_cap: 300 } }, usage: { weighted_in_window: 40 } },
	},
	{ name: "concurrency-only", body: { limits: { concurrency: { limit: 2 } }, usage: { concurrent_sessions: 2 } } },
	{ name: "nothing-metered", body: { plan: { display_name: "Free" } } },
	{ name: "not-an-object", body: [1, 2] },
	{ name: "rejected", status: 401, body: { error: "bad key" } },
	{ name: "transient", status: 503, body: { error: "down" } },
];

const results: any[] = [];
for (const item of cases) {
	let request: any;
	const fetch = (async (input: any, init: any) => {
		request = { url: String(input), method: init?.method ?? "GET", headers: Object.fromEntries(new Headers(init?.headers)) };
		return new Response(JSON.stringify(item.body), { status: item.status ?? 200, headers: { "content-type": "application/json" } });
	}) as typeof globalThis.fetch;
	let report: any = null;
	let error: string | undefined;
	try {
		report = await umansUsageProvider.fetchUsage(
			{ provider: "umans", credential: { type: "api_key", apiKey }, ...(item.baseUrl ? { baseUrl: item.baseUrl } : {}) } as any,
			{ fetch, logger: undefined } as any,
		);
	} catch (e) {
		error = String((e as Error).message);
	}
	results.push({
		name: item.name,
		baseUrl: item.baseUrl ?? "",
		status: item.status ?? 200,
		body: item.body,
		request,
		error: error ?? null,
		report: report && {
			plan: report.metadata?.plan ?? "",
			notes: report.notes ?? [],
			limits: report.limits.map((limit: any) => ({
				id: limit.id,
				windowId: limit.scope.windowId,
				durationMs: limit.window?.durationMs ?? 0,
				resetsAt: limit.window?.resetsAt ?? 0,
				used: limit.amount.used ?? null,
				limit: limit.amount.limit ?? null,
				usedFraction: limit.amount.usedFraction ?? null,
				status: limit.status ?? "",
			})),
		},
	});
}
console.log(JSON.stringify({ source: "NeoPi OWN umansUsageProvider.fetchUsage", apiKey, results }, null, 2));
