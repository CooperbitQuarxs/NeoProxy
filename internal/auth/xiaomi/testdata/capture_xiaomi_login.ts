// Runs NeoPi's OWN Xiaomi MiMo login (ai/src/registry/oauth/xiaomi.ts
// loginXiaomi) over scripted answers through a fake fetch: nothing leaves the
// process. Each case records every probe NeoPi sends (URL, headers, body) and
// the outcome. A case scripts one answer per probe; the last answer repeats.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_xiaomi_login.ts > xiaomi_login_capture.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { loginXiaomi } = await import(root + "/pi-ai/src/registry/oauth/xiaomi.ts");

type Step = { status?: number; body?: unknown; networkError?: boolean };
const ok: Step = { body: { choices: [{ message: { content: "pong" } }] } };
const unauthorized: Step = { status: 401, body: { error: { message: "Invalid API Key" } } };
const cases: { name: string; input: string; steps: Step[] }[] = [
	{ name: "standard-ok", input: "  sk-standard-key  ", steps: [ok] },
	{ name: "standard-401", input: "sk-standard-key", steps: [unauthorized] },
	{ name: "standard-500", input: "sk-standard-key", steps: [{ status: 500, body: "upstream exploded" }] },
	{ name: "standard-empty-body", input: "sk-standard-key", steps: [{ status: 403, body: "" }] },
	{ name: "standard-network", input: "sk-standard-key", steps: [{ networkError: true }] },
	{ name: "token-plan-sgp", input: "tp-plan-key", steps: [ok] },
	{ name: "token-plan-ams-after-401", input: "tp-plan-key", steps: [unauthorized, ok] },
	{ name: "token-plan-cn-after-network", input: "ttp-plan-key", steps: [{ networkError: true }, unauthorized, ok] },
	{ name: "token-plan-all-401", input: "tp-plan-key", steps: [unauthorized] },
	{ name: "token-plan-429-stops", input: "tp-plan-key", steps: [unauthorized, { status: 429, body: { error: { message: "slow down" } } }] },
	{ name: "token-plan-all-network", input: "tp-plan-key", steps: [{ networkError: true }] },
	{ name: "empty", input: "   ", steps: [ok] },
];

const results: any[] = [];
for (const item of cases) {
	const requests: any[] = [];
	let call = 0;
	const fetch = (async (input: any, init: any) => {
		const step = item.steps[Math.min(call++, item.steps.length - 1)];
		requests.push({ url: String(input), method: init?.method ?? "GET", headers: Object.fromEntries(new Headers(init?.headers)), body: init?.body ?? null });
		if (step.networkError) throw new TypeError("fetch failed");
		const text = typeof step.body === "string" ? step.body : JSON.stringify(step.body ?? {});
		return new Response(text, { status: step.status ?? 200, headers: { "content-type": "application/json" } });
	}) as typeof globalThis.fetch;
	let value: string | null = null;
	let error: string | null = null;
	let errorName: string | null = null;
	try {
		value = await loginXiaomi({ fetch, onAuth: () => {}, onPrompt: async () => item.input, onProgress: () => {} } as any);
	} catch (e) {
		error = String((e as Error).message);
		errorName = (e as Error).name;
	}
	results.push({ name: item.name, input: item.input, steps: item.steps, requests, value, error, errorName });
}
console.log(JSON.stringify({ source: "NeoPi OWN loginXiaomi", results }, null, 2));
