// Executes NeoPi's OWN key-entry logins (createApiKeyLogin over the compiled
// auth rules, and loginAlibabaTokenPlan) for the providers NeoProxy serves
// through NeoPi's OpenAI Chat Completions provider, against a scripted loopback
// server. Records every request (method, URL, headers, body), the prompts, the
// progress messages and the outcome.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_keys.ts > neopi_keys_capture.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;
const { createApiKeyLogin } = await import(root + "/pi-ai/src/registry/engine/api-key.ts");
const { loginAlibabaTokenPlan } = await import(root + "/pi-ai/src/registry/oauth/alibaba-token-plan.ts");
const { loginAlibabaCodingPlan } = await import(root + "/pi-ai/src/registry/oauth/alibaba-coding-plan.ts");

const nativeFetch = globalThis.fetch;
let spec: any;
let requests: any[];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const original = request.headers.get("x-capture-url")!;
		const headers = Object.fromEntries(request.headers);
		delete headers["x-capture-url"];
		requests.push({ method: request.method, url: original, headers, body: await request.text() });
		const status = spec.status ?? 200;
		const body = spec.body ?? { data: [{ id: "fixture-model" }], choices: [{ message: { content: "pong" } }] };
		if (typeof body === "string") return new Response(body, { status });
		return Response.json(body, { status });
	},
});
const captureFetch = Object.assign(
	async (input: any, options?: any) => {
		if (spec.networkError) throw new TypeError("fetch failed");
		const target = new URL(input instanceof Request ? input.url : String(input));
		const headers = new Headers(options?.headers);
		headers.set("host", target.host);
		headers.set("x-capture-url", target.href);
		return nativeFetch(`http://127.0.0.1:${server.port}${target.pathname}${target.search}`, { ...options, headers, signal: undefined });
	},
	{ preconnect: nativeFetch.preconnect },
);

const key = "fixture-key-0123456789abcdef";
const providers = [
	"cerebras", "deepseek", "fireworks", "huggingface", "moonshot", "nanogpt", "nvidia", "together",
	"aiand", "baseten", "coralbricks", "deepinfra", "firepass", "gmi-cloud", "helmcode", "inception", "novita", "qianfan",
	"qwen-portal", "sambanova", "stepfun", "synthetic", "venice", "wafer-serverless", "xiaomi-token-plan-sgp",
	"xiaomi-token-plan-ams", "xiaomi-token-plan-cn", "yolo-auto", "zhipu-coding-plan", "cline-pass",
	"singularityapi-dev", "singularityapi-tech", "charm-hyper",
];
const statuses = [
	{ name: "ok" },
	{ name: "http-401", status: 401, body: { error: { message: "Incorrect API key provided", code: "invalid_api_key" } } },
	{ name: "http-403", status: 403, body: { error: { message: "forbidden" } } },
	{ name: "http-404", status: 404, body: { detail: "Function 'x': Not found for account" } },
	{ name: "http-410", status: 410, body: "Gone: end of life" },
	{ name: "http-429", status: 429, body: { error: { message: "slow down" } } },
	{ name: "http-500", status: 500, body: "upstream exploded" },
	{ name: "http-400-auth-text", status: 400, body: "Invalid API key" },
	{ name: "http-400-other", status: 400, body: { error: { message: "bad request" } } },
	{ name: "http-401-invalid-model", status: 401, body: { error: { message: "no such model", code: "invalid_model" } } },
	{ name: "network-error", networkError: true },
];

const scenarios: any[] = [];
async function run(provider: string, label: string, scenario: any, input: string, answers: string[]) {
	spec = scenario;
	requests = [];
	const prompts: any[] = [];
	const progress: string[] = [];
	let auth: any;
	const controller = new AbortController();
	const queue = [...answers];
	const options = {
		fetch: captureFetch,
		signal: controller.signal,
		onProgress: (message: string) => progress.push(message),
		onAuth: (info: any) => {
			auth = info;
		},
		onPrompt: async (prompt: any) => {
			prompts.push(prompt);
			return queue.shift() ?? "";
		},
	} as any;
	// MOONSHOT_BASE_URL redirects Moonshot's probe; the CoreWeave variables
	// name the project its probe must carry.
	const envNames = ["MOONSHOT_BASE_URL", "COREWEAVE_PROJECT", "WANDB_INFERENCE_PROJECT", "WANDB_PROJECT", "WANDB_ENTITY"];
	const saved = Object.fromEntries(envNames.map(name => [name, process.env[name]]));
	const envVars: Record<string, string> = { ...(scenario.env ? { MOONSHOT_BASE_URL: scenario.env } : {}), ...(scenario.envVars ?? {}) };
	for (const name of envNames) {
		if (envVars[name] !== undefined) process.env[name] = envVars[name];
		else delete process.env[name];
	}
	let result: any;
	// loginAlibabaCodingPlan validates with the global fetch, so it is pointed
	// at the capture server for the run: no request may leave the machine.
	globalThis.fetch = captureFetch as typeof fetch;
	try {
		const policy = rules.auth.providers.find((entry: any) => entry.id === provider);
		const login =
			provider === "alibaba-token-plan"
				? loginAlibabaTokenPlan
				: provider === "alibaba-coding-plan"
					? loginAlibabaCodingPlan
					: createApiKeyLogin(policy.login, policy.name);
		result = { value: await login(options) };
	} catch (error) {
		result = { error: String(error instanceof Error ? error.message : error), errorName: (error as any)?.name, errorStatus: (error as any)?.status ?? null };
	} finally {
		globalThis.fetch = nativeFetch;
		for (const name of envNames) {
			if (saved[name] === undefined) delete process.env[name];
			else process.env[name] = saved[name];
		}
	}
	scenarios.push({ provider, label, input, answers, env: scenario.env ?? null, envVars: scenario.envVars ?? null, status: scenario.status ?? 200, body: scenario.body ?? null, networkError: scenario.networkError ?? false, auth, prompts, progress, result, requests });
}

try {
	for (const provider of providers) {
		for (const scenario of statuses) await run(provider, scenario.name, scenario, key, [key]);
	}
	// CoreWeave's probe needs the W&B project from the environment.
	const project = { COREWEAVE_PROJECT: "team/project" };
	for (const scenario of statuses) await run("coreweave", scenario.name, { ...scenario, envVars: project }, key, [key]);
	await run("coreweave", "no-project", {}, key, [key]);
	await run("coreweave", "wandb-entity-project", { envVars: { WANDB_ENTITY: "team", WANDB_PROJECT: "proj" } }, key, [key]);
	await run("coreweave", "wandb-project-unqualified", { envVars: { WANDB_PROJECT: "proj" } }, key, [key]);
	// A pasted "Bearer" prefix is stripped by DeepSeek's rule only.
	await run("deepseek", "bearer-prefix", {}, "Bearer " + key, ["Bearer " + key]);
	await run("deepseek", "bearer-only", {}, "Bearer", ["Bearer"]);
	await run("cerebras", "bearer-prefix-kept", {}, "Bearer " + key, ["Bearer " + key]);
	await run("cerebras", "empty-key", {}, "", [""]);
	await run("moonshot", "base-url-env", { env: "https://proxy.example.invalid/moon/v1/" }, key, [key]);
	// Token Plan: region prompt, key prompt, optional console cookie.
	const tokenKey = "sk-sp-fixture0123456789";
	const cookie = "login_aliyunid_csrf=csrfvalue; cna=abc";
	await run("alibaba-token-plan", "intl-default", {}, tokenKey, ["", tokenKey, ""]);
	await run("alibaba-token-plan", "intl-explicit-with-cookie", {}, tokenKey, ["1", tokenKey, "Cookie: " + cookie]);
	await run("alibaba-token-plan", "china", {}, tokenKey, ["2", tokenKey, ""]);
	await run("alibaba-token-plan", "china-with-cookie", {}, tokenKey, ["2", tokenKey, cookie]);
	await run("alibaba-token-plan", "custom-url", {}, tokenKey, ["3", "https://token-plan.eu.maas.aliyuncs.com/compatible-mode/v1//", tokenKey, ""]);
	await run("alibaba-token-plan", "custom-url-empty", {}, tokenKey, ["3", "  ", tokenKey, ""]);
	await run("alibaba-token-plan", "http-401", { status: 401, body: { error: { message: "invalid_api_key" } } }, tokenKey, ["1", tokenKey, ""]);
	await run("alibaba-token-plan", "http-500", { status: 500, body: "boom" }, tokenKey, ["1", tokenKey, ""]);
	await run("alibaba-token-plan", "empty-key", {}, "", ["1", "", ""]);
	await run("alibaba-token-plan", "bad-cookie", {}, tokenKey, ["1", tokenKey, "justonecookievalue"]);
	await run("alibaba-token-plan", "bad-cookie-china", {}, tokenKey, ["2", tokenKey, "=novalue; name="]);
	// Coding Plan: endpoint prompt, then the key, checked by a chat probe on
	// the International or China endpoint or a models probe on a custom one.
	const codingKey = "sk-fixture0123456789";
	await run("alibaba-coding-plan", "intl-default", {}, codingKey, ["", codingKey]);
	await run("alibaba-coding-plan", "china", {}, codingKey, ["2", codingKey]);
	await run("alibaba-coding-plan", "custom-url", {}, codingKey, ["3", "https://coding.example.invalid/v1//", codingKey]);
	await run("alibaba-coding-plan", "intl-http-401", { status: 401, body: { error: { message: "Incorrect API key provided", code: "invalid_api_key" } } }, codingKey, ["1", codingKey]);
	await run("alibaba-coding-plan", "custom-http-401", { status: 401, body: { error: { message: "unauthorized" } } }, codingKey, ["3", "https://coding.example.invalid/v1", codingKey]);
	await run("alibaba-coding-plan", "china-http-500", { status: 500, body: "boom" }, codingKey, ["2", codingKey]);
	console.log(JSON.stringify({ source: "NeoPi OWN createApiKeyLogin + loginAlibabaTokenPlan + loginAlibabaCodingPlan", key, scenarios }, null, 2));
} finally {
	server.stop(true);
}
