// Executes NeoPi's OWN xaiOauthUsageProvider against synthetic billing payloads.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { xaiOauthUsageProvider } = await import(root + "/pi-ai/src/usage/xai-oauth.ts");
const nativeFetch = globalThis.fetch;
const jwt = (payload: unknown) => "eyJhbGciOiJub25lIn0." + Buffer.from(JSON.stringify(payload)).toString("base64url") + ".fake";
const access = jwt({ sub: "xai-subject-fake", exp: 4102444800 });
const day = 86400000;
const iso = (offset: number) => new Date(Date.now() + offset).toISOString();
const weeklyPeriod = { start: iso(-day), end: iso(6 * day), type: "BILLING_PERIOD_TYPE_WEEKLY" };
let spec: any, requests: any[];
const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
	const url = new URL(request.url);
	requests.push({ method: request.method, url: "https://cli-chat-proxy.grok.com" + url.pathname + url.search, headers: Object.fromEntries(request.headers), body: await request.text() });
	const payload = url.searchParams.get("format") === "credits" ? spec.credits : spec.monthly;
	return payload === undefined ? new Response("{}", { status: 500 }) : Response.json(payload);
} });
const captureFetch = Object.assign(async (input: any, options?: any) => {
	const url = new URL(input instanceof Request ? input.url : String(input));
	if (url.origin !== "https://cli-chat-proxy.grok.com") throw new Error("Unexpected usage URL: " + url.href);
	const headers = new Headers(options?.headers);
	headers.set("host", url.host);
	return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`, { ...options, headers, redirect: "manual" });
}, { preconnect: nativeFetch.preconnect });
const scenarios: any[] = [];
try {
	for (spec of [
		{ name: "weekly-credits", credits: { config: { currentPeriod: weeklyPeriod, creditUsagePercent: 42.5, productUsage: [{ product: "GrokBuild", usagePercent: 10 }], onDemandCap: { val: 5 }, onDemandUsed: { val: 1 } } } },
		{ name: "unified-monthly", credits: { config: { currentPeriod: weeklyPeriod, isUnifiedBillingUser: true } }, monthly: { config: { billingPeriodStart: iso(-10 * day), billingPeriodEnd: iso(20 * day), monthlyLimit: { val: 1000 }, used: { val: 250 } } } },
		{ name: "unified-monthly-unavailable", credits: { config: { currentPeriod: weeklyPeriod, isUnifiedBillingUser: true } } },
	]) {
		requests = [];
		const report = await xaiOauthUsageProvider.fetchUsage(
			{ provider: "xai-oauth", credential: { type: "oauth", accessToken: access, accountId: "xai-subject-fake", email: "grok@example.invalid" } } as any,
			{ fetch: captureFetch } as any,
		);
		scenarios.push({ ...spec, limits: report?.limits.map((limit: any) => ({ id: limit.id, used: limit.amount.used, limit: limit.amount.limit, status: limit.status, resetsAt: limit.window?.resetsAt })) ?? null, requests });
	}
	console.log(JSON.stringify({ source: "NeoPi OWN xaiOauthUsageProvider", token: access, scenarios }, null, 2));
} finally {
	server.stop(true);
}
