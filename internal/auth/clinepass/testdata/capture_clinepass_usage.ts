// Runs NeoPi's OWN ClinePass usage provider (ai/src/usage/cline-pass.ts) over
// fixed answers through a fake fetch: no request leaves the process. Each case
// records the requests NeoPi makes (the quota route, then the account route
// for its identity) and the report (or error) it returns.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_clinepass_usage.ts > clinepass_usage_capture.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { clinePassUsageProvider } = await import(root + "/pi-ai/src/usage/cline-pass.ts");

const apiKey = "sk_clinepass-usage-capture-0000";
type Reply = { status?: number; body: unknown };
const cases: { name: string; baseUrl?: string; usage: Reply; account?: Reply }[] = [
	{
		name: "three-windows",
		usage: {
			body: {
				data: {
					limits: [
						{ type: "five_hour", percentUsed: 42.5, resetsAt: "2026-11-05T05:00:00.000Z" },
						{ type: "weekly", percentUsed: 91, resetsAt: "2026-11-09T00:00:00Z" },
						{ type: "monthly", percentUsed: 120, resetsAt: "not a date" },
					],
				},
			},
		},
		account: { body: { data: { email: "  dev@example.com ", id: "acct_123" } } },
	},
	{
		name: "bare-payload",
		baseUrl: "https://gateway.example/cline/api/v1//",
		usage: { body: { limits: [{ type: "five_hour", percentUsed: -3 }, { type: "daily", percentUsed: 10 }, { type: "weekly", percentUsed: "50" }, { type: "weekly" }, null] } },
		account: { body: { email: "", id: 7 } },
	},
	{ name: "account-fails", usage: { body: { data: { limits: [{ type: "weekly", percentUsed: 0 }] } } }, account: { status: 500, body: { error: "x" } } },
	{ name: "account-not-json", usage: { body: { data: { limits: [{ type: "weekly", percentUsed: 5 }] } } }, account: { body: "oops" } },
	{ name: "no-valid-limits", usage: { body: { data: { limits: [{ type: "yearly", percentUsed: 1 }] } } } },
	{ name: "limits-not-array", usage: { body: { data: { limits: { type: "weekly" } } } } },
	{ name: "not-an-object", usage: { body: [1, 2] } },
	{ name: "no-plan", usage: { status: 404, body: { error: "No plan history found for user" } } },
	{ name: "not-found-other", usage: { status: 404, body: { error: "missing" } } },
	{ name: "rejected", usage: { status: 401, body: { error: "bad key" } } },
	{ name: "forbidden", usage: { status: 403, body: { error: "nope" } } },
	{ name: "transient", usage: { status: 503, body: { error: "down" } } },
];

const results: any[] = [];
for (const item of cases) {
	const requests: any[] = [];
	const fetch = (async (input: any, init: any) => {
		const url = String(input);
		requests.push({ url, method: init?.method ?? "GET", headers: Object.fromEntries(new Headers(init?.headers)) });
		const reply = url.endsWith("/users/me") ? (item.account ?? { status: 500, body: {} }) : item.usage;
		const text = typeof reply.body === "string" ? reply.body : JSON.stringify(reply.body);
		return new Response(text, { status: reply.status ?? 200, headers: { "content-type": "application/json" } });
	}) as typeof globalThis.fetch;
	let report: any = null;
	let error: string | undefined;
	try {
		report = await clinePassUsageProvider.fetchUsage(
			{ provider: "cline-pass", credential: { type: "api_key", apiKey }, ...(item.baseUrl ? { baseUrl: item.baseUrl } : {}) } as any,
			{ fetch, logger: undefined } as any,
		);
	} catch (e) {
		error = String((e as Error).message);
	}
	results.push({
		name: item.name,
		baseUrl: item.baseUrl ?? "",
		usage: { status: item.usage.status ?? 200, body: item.usage.body },
		account: item.account ? { status: item.account.status ?? 200, body: item.account.body } : null,
		requests,
		error: error ?? null,
		report: report && {
			email: report.metadata?.email ?? "",
			accountId: report.metadata?.accountId ?? "",
			limits: report.limits.map((limit: any) => ({
				id: limit.id,
				windowId: limit.scope.windowId,
				durationMs: limit.window?.durationMs ?? 0,
				resetsAt: limit.window?.resetsAt ?? 0,
				used: limit.amount.used,
				usedFraction: limit.amount.usedFraction,
				status: limit.status ?? "",
			})),
		},
	});
}
console.log(JSON.stringify({ source: "NeoPi OWN clinePassUsageProvider.fetchUsage", apiKey, results }, null, 2));
