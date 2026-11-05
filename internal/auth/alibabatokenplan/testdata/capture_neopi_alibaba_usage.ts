// Executes NeoPi's OWN alibabaTokenPlanUsageProvider against synthetic console
// responses on a loopback server. Every request (method, URL, headers, body) and
// the resulting report are recorded.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_alibaba_usage.ts > neopi_alibaba_usage_capture.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { alibabaTokenPlanUsageProvider } = await import(root + "/pi-ai/src/usage/alibaba-token-plan.ts");
const nativeFetch = globalThis.fetch;
const CN = "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1";
const token = "sk-sp-fixture0123456789";
const reset = 4102444800000;
let spec: any;
let requests: any[];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const original = request.headers.get("x-capture-url")!;
		const headers = Object.fromEntries(request.headers);
		delete headers["x-capture-url"];
		const body = await request.text();
		requests.push({ method: request.method, url: original, headers, body });
		const isUsage = request.method === "POST";
		const reply = isUsage ? spec.usage : spec.session;
		const status = reply?.status ?? 200;
		if (typeof reply?.body === "string") return new Response(reply.body, { status });
		return Response.json(reply?.body ?? {}, { status });
	},
});
const captureFetch = Object.assign(
	async (input: any, options?: any) => {
		const target = new URL(input instanceof Request ? input.url : String(input));
		const headers = new Headers(options?.headers);
		headers.set("host", target.host);
		headers.set("x-capture-url", target.href);
		return nativeFetch(`http://127.0.0.1:${server.port}${target.pathname}${target.search}`, { ...options, headers });
	},
	{ preconnect: nativeFetch.preconnect },
);

const cookie = "cna=abc; login_aliyunid_csrf=csrf-fixture; other=1";
const intlSession = { body: { data: { secToken: "sec-intl", accountId: "acct-1" } } };
const cnSession = { body: '<html><script>window.ALIYUN_CONSOLE_CONFIG={SEC_TOKEN: "sec-cn", other: 1}</script></html>' };
const usageData = {
	per5HourPercentage: 25,
	per5HourResetTime: reset,
	per1WeekPercentage: "0.5",
	per1WeekResetTime: String(reset / 1000),
	per1MonthPercentage: 100,
	per1MonthResetTime: reset,
};
const ok = (data: any) => ({ body: { successResponse: true, data: { success: true, ...data } } });

const scenarios: any[] = [];
try {
	for (spec of [
		{ name: "intl-flat", credential: JSON.stringify({ token, cookie }), session: intlSession, usage: ok({ data: usageData }) },
		{ name: "intl-json-string-data", credential: JSON.stringify({ token, cookie }), session: intlSession, usage: ok({ Data: JSON.stringify({ data: usageData }) }) },
		{ name: "intl-datav2", credential: JSON.stringify({ token, cookie }), session: intlSession, usage: ok({ DataV2: { data: usageData } }) },
		{ name: "intl-csrf-fallback", credential: JSON.stringify({ token, cookie: "csrf=csrf-fallback; a=b" }), session: { body: { data: { secToken: "sec-intl", userId: 4242 } } }, usage: ok({ data: usageData }) },
		{ name: "intl-no-csrf", credential: JSON.stringify({ token, cookie: "a=b" }), session: intlSession, usage: ok({ data: usageData }) },
		{ name: "intl-monthly-only", credential: JSON.stringify({ token, cookie }), session: intlSession, usage: ok({ data: { per1MonthPercentage: "0.75", per1MonthResetTime: reset } }) },
		{ name: "intl-fractions", credential: JSON.stringify({ token, cookie }), session: intlSession, usage: ok({ data: { per5HourPercentage: 0, per1WeekPercentage: 1, per1WeekResetTime: -5, per1MonthPercentage: -1 } }) },
		{ name: "intl-explicit-base", credential: JSON.stringify({ token, cookie, baseUrl: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1" }), session: intlSession, usage: ok({ data: usageData }) },
		{ name: "china-flat", credential: JSON.stringify({ token, cookie, baseUrl: CN }), session: cnSession, usage: ok({ data: usageData }) },
		{ name: "china-json-string-data", credential: JSON.stringify({ token, cookie, baseUrl: CN }), session: cnSession, usage: ok({ Data: JSON.stringify({ DataV2: { data: usageData } }) }) },
		{ name: "china-no-sec-token", credential: JSON.stringify({ token, cookie, baseUrl: CN }), session: { body: "<html></html>" }, usage: ok({ data: usageData }) },
		{ name: "intl-session-http-error", credential: JSON.stringify({ token, cookie }), session: { status: 403, body: {} }, usage: ok({ data: usageData }) },
		{ name: "intl-session-invalid", credential: JSON.stringify({ token, cookie }), session: { body: { data: {} } }, usage: ok({ data: usageData }) },
		{ name: "intl-usage-http-error", credential: JSON.stringify({ token, cookie }), session: intlSession, usage: { status: 500, body: {} } },
		{ name: "intl-usage-unsuccessful", credential: JSON.stringify({ token, cookie }), session: intlSession, usage: { body: { successResponse: false, data: {} } } },
		{ name: "intl-usage-rejected", credential: JSON.stringify({ token, cookie }), session: intlSession, usage: { body: { successResponse: true, data: { success: false, errorCode: "Forbidden.Login" } } } },
		{ name: "intl-usage-empty", credential: JSON.stringify({ token, cookie }), session: intlSession, usage: ok({ data: {} }) },
		{ name: "no-cookie-bare-key", credential: token },
		{ name: "no-cookie-bundle", credential: JSON.stringify({ token }) },
	]) {
		requests = [];
		const params = { provider: "alibaba-token-plan", credential: { type: "api_key", apiKey: spec.credential } } as any;
		const supported = alibabaTokenPlanUsageProvider.supports(params);
		const report = supported ? await alibabaTokenPlanUsageProvider.fetchUsage(params, { fetch: captureFetch } as any) : null;
		scenarios.push({
			name: spec.name,
			credential: spec.credential,
			session: spec.session ?? null,
			usage: spec.usage ?? null,
			supported,
			report: report && {
				source: report.metadata?.source ?? null,
				accountId: report.metadata?.accountId ?? null,
				limits: report.limits.map((limit: any) => ({
					id: limit.id,
					windowId: limit.window.id,
					durationMs: limit.window.durationMs ?? null,
					resetsAt: limit.window.resetsAt ?? null,
					usedFraction: limit.amount.usedFraction,
					status: limit.status,
					accountId: limit.scope.accountId ?? null,
				})),
			},
			requests,
		});
	}
	console.log(JSON.stringify({ source: "NeoPi OWN alibabaTokenPlanUsageProvider", cookie, scenarios }, null, 2));
} finally {
	server.stop(true);
}
