// Runs NeoPi's OWN Devin usage provider (devinUsageProvider.fetchUsage, the
// GetUserStatus seat-management RPC) against loopback GetUserStatus responses
// covering quota-billed, credit-billed, hidden-quota and identity-fallback
// plans, recording the request on the wire and the report built from each.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const proto = await import(root + "/pi-catalog/src/discovery/devin-proto.ts");
const { create, toBinary } = await import(root + "/pi-catalog/src/discovery/protobuf.ts");
const { devinUsageProvider } = await import(root + "/pi-ai/src/usage/devin.ts");

const token = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjYXB0dXJlIn0.c2lnbmF0dXJl";
const ts = (seconds: number, nanos = 0) => create(proto.TimestampSchema, { seconds: BigInt(seconds), nanos });
const planInfo = (fields: any) => create(proto.PlanInfoSchema, fields);
const devinInfo = (orgId: string, accountDisplayName: string) => create(proto.DevinPlanInfoSchema, { orgId, accountDisplayName });

const scenarios: { name: string; status?: number; response?: any }[] = [
	{
		name: "quota-plan",
		response: {
			userStatus: {
				email: " pilot@example.com ",
				userId: "user-1",
				teamId: "team-1",
				teamsTier: proto.TeamsTier.DEVIN_PRO,
				planStatus: {
					planStart: ts(1_790_000_000),
					planEnd: ts(1_792_592_000, 500_000_000),
					dailyQuotaRemainingPercent: 55,
					weeklyQuotaRemainingPercent: 80,
					dailyQuotaResetAtUnix: 1_790_100_000n,
					weeklyQuotaResetAtUnix: 1_790_500_000n,
				},
			},
			planInfo: planInfo({ planName: "Devin Pro", billingStrategy: proto.BillingStrategy.QUOTA, devinInfo: devinInfo("org-1", "Pilot Org") }),
		},
	},
	{
		name: "credit-plan",
		response: {
			userStatus: {
				userId: "user-2",
				teamId: "team-2",
				planStatus: {
					planStart: ts(1_790_000_000),
					planEnd: ts(1_792_592_000),
					usedPromptCredits: 120,
					availablePromptCredits: 380,
					usedFlowCredits: 10,
					availableFlowCredits: 0,
					overageBalanceMicros: 2_500_000n,
				},
			},
			planInfo: planInfo({ teamsTier: proto.TeamsTier.DEVIN_MAX, billingStrategy: proto.BillingStrategy.CREDITS, monthlyPromptCredits: 500 }),
		},
	},
	{
		name: "hidden-quota",
		response: {
			userStatus: {
				userId: "user-3",
				planStatus: {
					dailyQuotaRemainingPercent: 0,
					weeklyQuotaRemainingPercent: 140,
					dailyQuotaResetAtUnix: 1_790_100_000n,
					weeklyQuotaResetAtUnix: 1_790_500_000_000n,
				},
			},
			planInfo: planInfo({ planName: "Teams", hideDailyQuota: true }),
		},
	},
	{
		name: "nested-plan-info",
		response: {
			userStatus: {
				teamsTier: proto.TeamsTier.TRIAL,
				planStatus: {
					planInfo: planInfo({ billingStrategy: proto.BillingStrategy.QUOTA, monthlyFlexCreditPurchaseAmount: 50 }),
					usedFlexCredits: 60,
					availableFlexCredits: 5,
					dailyQuotaRemainingPercent: 0,
					weeklyQuotaRemainingPercent: 0,
				},
			},
		},
	},
	{ name: "no-user-status", response: {} },
	{ name: "http-error", status: 503 },
];

const encode = (response: any) => {
	const status = response.userStatus
		? create(proto.UserStatusSchema, {
				...response.userStatus,
				...(response.userStatus.planStatus ? { planStatus: create(proto.PlanStatusSchema, response.userStatus.planStatus) } : {}),
			})
		: undefined;
	return toBinary(
		proto.GetUserStatusResponseSchema,
		create(proto.GetUserStatusResponseSchema, { ...(status ? { userStatus: status } : {}), ...(response.planInfo ? { planInfo: response.planInfo } : {}) }),
	);
};

let current: (typeof scenarios)[number];
let request: any;
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(incoming) {
		const body = new Uint8Array(await incoming.arrayBuffer());
		const headers = Object.fromEntries(incoming.headers);
		delete headers.host;
		request = { method: incoming.method, path: new URL(incoming.url).pathname, headers, body: Buffer.from(body).toString("base64") };
		if (current.status) return new Response("unavailable", { status: current.status });
		return new Response(encode(current.response), { headers: { "content-type": "application/proto" } });
	},
});
const baseUrl = `http://127.0.0.1:${server.port}/`;
const fixtures: any[] = [];
try {
	for (const scenario of scenarios) {
		current = scenario;
		request = undefined;
		const report = await devinUsageProvider.fetchUsage(
			{ provider: "devin", credential: { type: "oauth", accessToken: ` ${token} ` }, baseUrl } as any,
			{ fetch },
		);
		if (report) delete (report as any).fetchedAt;
		fixtures.push({
			name: scenario.name,
			status: scenario.status ?? 200,
			response: scenario.status ? null : Buffer.from(encode(scenario.response)).toString("base64"),
			request,
			report: report ?? null,
		});
	}
} finally {
	server.stop(true);
}
console.log(
	JSON.stringify(
		{ source: "NeoPi OWN devinUsageProvider.fetchUsage against loopback GetUserStatus", token, fixtures },
		(_key, value) => (typeof value === "bigint" ? Number(value) : value),
		2,
	),
);
