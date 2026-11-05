// Runs NeoPi's OWN Command Code usage provider (ai/src/usage/commandcode.ts)
// over fixed answers through a fake fetch: no request leaves the process.
// Each case records the requests NeoPi makes (the identity route, then the
// org's credits route) and the report (or error) it returns.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_commandcode_usage.ts > commandcode_usage_capture.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { commandCodeUsageProvider } = await import(root + "/pi-ai/src/usage/commandcode.ts");

const apiKey = "user_commandcode-usage-capture-0000";
type Reply = { status?: number; body: unknown };
const whoami: Reply = { body: { user: { id: "user_123", email: "dev@example.com" }, org: { id: "org/1 a+b", login: "acme" } } };
const plan = (windowLimits: unknown, credits: unknown = { monthlyCredits: 400, purchasedCredits: 25.5, freeCredits: 0, planId: "pro" }): Reply => ({
	body: { credits, windowLimits },
});
const cases: { name: string; baseUrl?: string; whoami: Reply; credits?: Reply }[] = [
	{
		name: "plan-windows",
		whoami,
		credits: plan({
			fiveHour: { used: 45, cap: 50, resetAt: 1793854800, exceeded: false },
			weekly: { used: 120, cap: 1000, resetAt: "1794268800000" },
		}),
	},
	{
		name: "exceeded-under-cap",
		whoami,
		credits: plan({ fiveHour: { used: 10, cap: 50, exceeded: true, resetAt: "2026-11-05T05:00:00Z" }, weekly: { used: 1000, cap: 1000, resetAt: 1794268800123 } }),
	},
	{
		name: "unknown-fractions",
		whoami,
		credits: plan({ fiveHour: { used: 3, cap: 0, resetAt: -5 }, weekly: { used: 7 } }),
	},
	{
		name: "unusable-windows",
		whoami,
		credits: plan({ fiveHour: { used: "3", cap: 50 }, weekly: "x" }),
	},
	{ name: "pay-as-you-go", whoami: { body: { data: { user: { id: "user_9" } } } }, credits: { body: { data: { credits: { purchasedCredits: 0 } } } } },
	{ name: "windows-not-object", whoami, credits: plan([1, 2]) },
	{ name: "no-org", whoami: { body: { user: { id: "user_5", email: "" }, org: { id: "", login: 3 } } }, credits: plan(undefined, { freeCredits: -2 }) },
	{ name: "no-credit-numbers", whoami, credits: plan(undefined, { monthlyCredits: "5", planId: "free" }) },
	{ name: "credits-not-object", whoami, credits: { body: { credits: [5] } } },
	{ name: "credits-rejected", whoami, credits: { status: 401, body: { error: "bad key" } } },
	{ name: "credits-forbidden", whoami, credits: { status: 403, body: { error: "nope" } } },
	{ name: "credits-not-json", whoami, credits: { body: "oops" } },
	{ name: "whoami-rejected", whoami: { status: 401, body: { error: "bad key" } } },
	{ name: "whoami-forbidden", whoami: { status: 403, body: { error: "nope" } } },
	{ name: "whoami-transient", whoami: { status: 503, body: { error: "down" } } },
	{ name: "whoami-no-user", whoami: { body: { user: { id: 7 } } } },
	{ name: "whoami-not-object", whoami: { body: [1] } },
	{ name: "configured-base", baseUrl: "https://CC.Example:8443/provider/v1", whoami, credits: plan({ weekly: { used: 1, cap: 2 } }) },
	{ name: "unparseable-base", baseUrl: "not a url", whoami, credits: plan({ weekly: { used: 1, cap: 2 } }) },
];

const results: any[] = [];
for (const item of cases) {
	const requests: any[] = [];
	const fetch = (async (input: any, init: any) => {
		const url = String(input);
		requests.push({ url, method: init?.method ?? "GET", headers: Object.fromEntries(new Headers(init?.headers)) });
		const reply = new URL(url).pathname === "/alpha/whoami" ? item.whoami : (item.credits ?? { status: 500, body: {} });
		const text = typeof reply.body === "string" ? reply.body : JSON.stringify(reply.body);
		return new Response(text, { status: reply.status ?? 200, headers: { "content-type": "application/json" } });
	}) as typeof globalThis.fetch;
	let report: any = null;
	let error: string | undefined;
	try {
		report = await commandCodeUsageProvider.fetchUsage(
			{ provider: "commandcode", credential: { type: "api_key", apiKey }, ...(item.baseUrl ? { baseUrl: item.baseUrl } : {}) } as any,
			{ fetch, logger: undefined } as any,
		);
	} catch (e) {
		error = String((e as Error).message);
	}
	const balance = report?.limits.find((limit: any) => limit.id === "commandcode:balance");
	results.push({
		name: item.name,
		baseUrl: item.baseUrl ?? "",
		whoami: { status: item.whoami.status ?? 200, body: item.whoami.body },
		credits: item.credits ? { status: item.credits.status ?? 200, body: item.credits.body } : null,
		requests,
		error: error ?? null,
		report: report && {
			accountId: report.metadata?.accountId ?? "",
			email: report.metadata?.email ?? "",
			orgId: report.metadata?.orgId ?? "",
			orgName: report.metadata?.orgName ?? "",
			planType: report.metadata?.planType ?? "",
			balance: balance?.amount.remaining ?? null,
			limits: report.limits
				.filter((limit: any) => limit.id !== "commandcode:balance")
				.map((limit: any) => ({
					id: limit.id,
					windowId: limit.window?.id ?? "",
					durationMs: limit.window?.durationMs ?? 0,
					resetsAt: limit.window?.resetsAt ?? 0,
					used: limit.amount.used,
					cap: limit.amount.limit ?? null,
					usedFraction: limit.amount.usedFraction ?? null,
					status: limit.status ?? "",
				})),
		},
	});
}
console.log(JSON.stringify({ source: "NeoPi OWN commandCodeUsageProvider.fetchUsage", apiKey, results }, null, 2));
