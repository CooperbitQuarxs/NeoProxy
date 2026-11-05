// Runs NeoPi's OWN GitHub Copilot usage provider (pi-ai usage/github-copilot.ts
// githubCopilotUsageProvider.fetchUsage) for an OAuth sign-in against scripted
// answers for /copilot_internal/user and /user, recording the requests it
// sends beside the limits it reports or the error it throws. Nothing leaves the
// machine: the fetch override answers every URL itself.
//
// Writes neopi_github_copilot_usage.json beside this script. Run from the
// NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file>
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { githubCopilotUsageProvider } = await import(root + "/pi-ai/src/usage/github-copilot.ts");

type Reply = { status: number; body: string };
const json = (value: unknown, status = 200): Reply => ({ status, body: JSON.stringify(value) });
const detail = (entitlement: unknown, remaining: unknown, extra: Record<string, unknown> = {}) => ({
	entitlement,
	remaining,
	percent_remaining: typeof entitlement === "number" && typeof remaining === "number" && entitlement > 0 ? (remaining / entitlement) * 100 : 100,
	unlimited: false,
	overage_count: 0,
	overage_permitted: false,
	quota_id: "premium_interactions",
	quota_remaining: remaining,
	...extra,
});
const unlimited = { entitlement: 0, remaining: 0, percent_remaining: 100, unlimited: true, overage_count: 0, overage_permitted: false, quota_id: "chat", quota_remaining: 0 };
const internal = (snapshots: unknown, extra: Record<string, unknown> = {}) =>
	json({ copilot_plan: "individual", quota_reset_date: "2026-12-01", quota_snapshots: snapshots, ...extra });
const login = json({ login: "octocat", id: 1 });

type Scenario = { name: string; enterpriseUrl?: string; internal: Reply; user?: Reply };
const scenarios: Scenario[] = [
	{ name: "premium-limited", internal: internal({ premium_interactions: detail(300, 120), chat: unlimited, completions: unlimited }), user: login },
	{ name: "free-plan-all-limited", internal: internal({ premium_interactions: detail(50, 0, { overage_count: 5, overage_permitted: true }), chat: detail(50, 10, { quota_id: "chat" }), completions: detail(2000, 1900, { quota_id: "completions" }) }, { copilot_plan: "free" }), user: login },
	{ name: "warning", internal: internal({ premium_interactions: detail(1000, 50), chat: unlimited }), user: login },
	{ name: "premium-unlimited", internal: internal({ premium_interactions: { ...unlimited, quota_id: "premium_interactions" }, chat: unlimited, completions: unlimited }), user: login },
	{ name: "malformed-details", internal: internal({ premium_interactions: { entitlement: 300, remaining: 10, unlimited: false }, chat: "nope", completions: detail("2000", "1500", { quota_remaining: undefined, percent_remaining: "75", overage_count: "2" }) }, { quota_reset_date: "not a date" }), user: login },
	{ name: "no-snapshots", internal: json({ copilot_plan: "business", quota_reset_date: "2026-12-01T00:00:00Z" }), user: login },
	{ name: "user-lookup-fails", internal: internal({ premium_interactions: detail(300, 299) }), user: { status: 500, body: "{}" } },
	{ name: "user-without-login", internal: internal({ premium_interactions: detail(300, 299) }), user: json({ id: 1 }) },
	{ name: "enterprise", enterpriseUrl: "octo.ghe.com", internal: internal({ premium_interactions: detail(300, 120) }), user: login },
	{ name: "enterprise-api-host", enterpriseUrl: "api.octo.ghe.com", internal: internal({ premium_interactions: detail(300, 120) }), user: login },
	{ name: "enterprise-url", enterpriseUrl: "https://octo.ghe.com/", internal: internal({ premium_interactions: detail(300, 120) }), user: login },
	{ name: "internal-401", internal: { status: 401, body: '{"message":"Bad credentials"}' } },
	{ name: "internal-403", internal: { status: 403, body: '{"message":"Forbidden"}' } },
	{ name: "internal-500", internal: { status: 500, body: "{}" } },
	{ name: "internal-not-object", internal: json([1, 2]) },
];

const token = "gho_usage_fixture";
const results: any[] = [];
for (const scenario of scenarios) {
	const requests: any[] = [];
	const fetchImpl = async (input: any, init: any) => {
		const url = new URL(typeof input === "string" ? input : input.url);
		requests.push({ method: init?.method ?? "GET", url: url.toString(), headers: Object.fromEntries(Object.entries(new Headers(init?.headers ?? {}).toJSON()).map(([k, v]) => [k.toLowerCase(), v])) });
		const reply = url.pathname.endsWith("/copilot_internal/user") ? scenario.internal : (scenario.user ?? { status: 404, body: "{}" });
		return new Response(reply.body, { status: reply.status, statusText: reply.status === 200 ? "OK" : "Error", headers: { "content-type": "application/json" } });
	};
	const credential: any = { type: "oauth", accessToken: token, refreshToken: token, ...(scenario.enterpriseUrl ? { enterpriseUrl: scenario.enterpriseUrl } : {}) };
	let report: any = null;
	let error: any = null;
	try {
		report = await githubCopilotUsageProvider.fetchUsage({ provider: "github-copilot", credential }, { fetch: fetchImpl as any });
	} catch (thrown: any) {
		error = { status: thrown?.status ?? null, message: String(thrown?.message ?? thrown) };
	}
	results.push({
		...scenario,
		requests,
		report: report && { limits: report.limits, metadata: { accountId: report.metadata?.accountId ?? null, plan: report.metadata?.plan ?? null, quotaResetDate: report.metadata?.quotaResetDate ?? null } },
		error,
	});
}
const out = join(dirname(new URL(import.meta.url).pathname), "neopi_github_copilot_usage.json");
writeFileSync(out, JSON.stringify({ source: "NeoPi OWN githubCopilotUsageProvider.fetchUsage (oauth)", token, results }, null, 1));
console.log(`wrote ${results.length} scenarios to ${out}`);
for (const r of results) console.log(r.name, r.requests.length, "requests", r.report ? `${r.report.limits.length} limits` : r.error ? `error ${r.error.status}` : "null");
