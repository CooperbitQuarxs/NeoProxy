// Runs NeoPi's OWN model managers (resolveProviderModels over each provider's
// <provider>ModelManagerOptions) for the OpenAI Chat Completions keys providers
// against scripted /models answers, and records the request each discovery
// sends beside the roster NeoPi resolves. Nothing leaves the machine: the fetch
// override answers every URL itself (the models.dev catalog answers 503).
//
// Writes neopi_completions_discovery.json.gz beside this script. Run from the
// NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file>
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const outFile = process.env.OUT_FILE ?? join(dirname(new URL(import.meta.url).pathname), "neopi_completions_discovery.json.gz");

const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
const m = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");

const managers: Record<string, (config: any) => any> = {
	cerebras: m.cerebrasModelManagerOptions,
	deepseek: m.deepseekModelManagerOptions,
	fireworks: m.fireworksModelManagerOptions,
	groq: m.groqModelManagerOptions,
	huggingface: m.huggingfaceModelManagerOptions,
	mistral: m.mistralModelManagerOptions,
	moonshot: m.moonshotModelManagerOptions,
	nanogpt: m.nanoGptModelManagerOptions,
	nvidia: m.nvidiaModelManagerOptions,
	together: m.togetherModelManagerOptions,
	"alibaba-token-plan": m.alibabaTokenPlanModelManagerOptions,
	aiand: m.aiandModelManagerOptions,
	baseten: m.basetenModelManagerOptions,
	coralbricks: m.coralbricksModelManagerOptions,
	deepinfra: m.deepinfraModelManagerOptions,
	firepass: m.firepassModelManagerOptions,
	"gmi-cloud": m.gmiCloudModelManagerOptions,
	helmcode: m.helmcodeModelManagerOptions,
	inception: m.inceptionModelManagerOptions,
	novita: m.novitaModelManagerOptions,
	qianfan: m.qianfanModelManagerOptions,
	"qwen-portal": m.qwenPortalModelManagerOptions,
	sambanova: m.sambanovaModelManagerOptions,
	stepfun: m.stepfunModelManagerOptions,
	synthetic: m.syntheticModelManagerOptions,
	venice: m.veniceModelManagerOptions,
	"wafer-serverless": m.waferServerlessModelManagerOptions,
	"xiaomi-token-plan-sgp": (config: any) => m.xiaomiModelManagerOptions({ ...config, providerId: "xiaomi-token-plan-sgp", tokenPlanRegion: "sgp" }),
	"xiaomi-token-plan-ams": (config: any) => m.xiaomiModelManagerOptions({ ...config, providerId: "xiaomi-token-plan-ams", tokenPlanRegion: "ams" }),
	"xiaomi-token-plan-cn": (config: any) => m.xiaomiModelManagerOptions({ ...config, providerId: "xiaomi-token-plan-cn", tokenPlanRegion: "cn" }),
	"yolo-auto": m.yoloAutoModelManagerOptions,
	"zhipu-coding-plan": m.zhipuCodingPlanModelManagerOptions,
};
for (const [id, build] of Object.entries(managers)) if (typeof build !== "function") throw new Error(`no NeoPi manager for ${id}`);

const apiKey = "sk-capture-key";
const cacheDir = mkdtempSync(join(tmpdir(), "neopi-discovery-"));

type Reply = { status: number; body: string };
const json = (value: unknown, status = 200): Reply => ({ status, body: JSON.stringify(value) });

// An entry list that exercises the mapper: bundled ids with live overrides,
// unknown ids, malformed rows and a duplicate.
function entries(provider: string, bundled: string[]): unknown[] {
	const list: unknown[] = [
		{ id: bundled[0], name: "Live Name", context_length: 12345, max_completion_tokens: 2345, object: "model", owned_by: "x" },
		{ id: bundled[1] },
		{ id: bundled[2], context_length: 4096, max_completion_tokens: 4096 },
		{ id: bundled[3], name: "", context_length: "77000", max_completion_tokens: 0 },
		{ id: bundled[4], name: null, max_output_tokens: 6000 },
		{ id: "zz-new-model" },
		{ id: "zz-new-named", name: "Zed Named", context_length: 32000, max_completion_tokens: 8000 },
		{ id: "zz-new-null-name", name: null, context_length: -5, max_completion_tokens: "x" },
		{ id: "zz-new-detailed", name: "Detailed", context_length: 65536, max_output_tokens: 9000 },
		{ id: "" },
		{ name: "no id" },
		5,
		null,
		{ id: bundled[1], name: "Duplicate of second" },
	];
	if (provider === "nanogpt") {
		list.push(
			{ id: "zz-embedding-x" },
			{ id: "zz-base:thinking" },
			{ id: "zz-base" },
			{ id: "zz-sub:thinking:high" },
			{ id: "zz-image-gen" },
			{ id: "zz-Speech-Z" },
			{ id: bundled[5] + ":thinking" },
			{ id: "zz-pricing", pricing: { prompt: "0.000001", completion: "0.000002" } },
		);
	}
	if (provider === "moonshot") {
		list.push(
			{ id: "kimi-k2.9-thinking" },
			{ id: "kimi-k2.9" },
			{ id: "kimi-k2-new-turbo" },
			{ id: "kimi-k3-0801" },
			{ id: "kimi-k3-0801-thinking" },
			{ id: "kimi-latest" },
			{ id: "moonshot-v1-new" },
			{ id: "kimi-thinking-preview" },
			{ id: "Kimi-K3" },
		);
	}
	if (provider === "alibaba-token-plan") {
		list.push(
			{ id: "text-embedding-v9" },
			{ id: "qwen-image-x" },
			{ id: "wan2.7-x" },
			{ id: "fun-asr-x" },
			{ id: "happyhorse-1" },
			{ id: "qwen-audio-turbo" },
			{ id: "Text-Embedding-V3" },
			{ id: "deepseek-v4-new" },
			{ id: "deepseek-v3.2" },
			{ id: "DeepSeek-V4-Pro-0813" },
			{ id: "deepseek-pro-x" },
			{ id: "glm-5.1" },
			{ id: "kimi-k2.7-code" },
			{ id: "minimax-m2.5" },
			{ id: "qwen3.9-plus" },
			{ id: "qwen3.8-max" },
		);
	}
	return list;
}

// Control-plane records (GET /v1/accounts/fireworks/models) over two pages.
function fireworksPages(bundled: string[]): Reply[] {
	const wire = (id: string) => "accounts/fireworks/models/" + id.replace(/(?<=\d)\.(?=\d)/g, "p");
	const page1 = {
		models: [
			{ name: wire(bundled[0]), displayName: "Live Name", contextLength: 12345, supportsImageInput: true, supportsTools: true, supportsServerless: true, state: "READY" },
			{ name: wire(bundled[1]), supportsServerless: true, state: "READY" },
			{ name: wire(bundled[2]), displayName: "  ", contextLength: 4096, supportsImageInput: false, supportsTools: false, supportsServerless: true, state: "READY" },
			{ name: wire(bundled[3]), displayName: "Not Ready", contextLength: 777, supportsServerless: true, state: "CREATING" },
			{ name: "accounts/fireworks/models/zz-new-model", supportsServerless: true },
			{ name: "accounts/fireworks/models/zz-new-1p5-vl", displayName: "Zed VL", contextLength: 64000, supportsImageInput: true, supportsTools: true, supportsServerless: true, state: "READY" },
			{ name: "accounts/fireworks/models/zz-not-serverless", supportsServerless: false, state: "READY" },
			{ name: "accounts/fireworks/models/zz-no-tools", displayName: "No Tools", contextLength: 8192, supportsTools: false, supportsServerless: true, state: "READY" },
			{ displayName: "no name", supportsServerless: true },
			{ name: "accounts/fireworks/models/deepseek-v4-zz", displayName: "DeepSeek V4 ZZ", contextLength: 100000, supportsServerless: true, state: "READY" },
		],
		nextPageToken: "page-2",
	};
	const page2 = {
		models: [
			{ name: "accounts/fireworks/models/zz-page-two", displayName: "Page Two", contextLength: 16000, supportsServerless: true, state: "READY" },
			{ name: wire(bundled[0]), displayName: "Live Name Two", contextLength: 54321, supportsServerless: true, state: "READY" },
		],
	};
	return [json(page1), json(page2)];
}

type Scenario = { name: string; replies: Reply[]; expectFetchFails?: boolean };

function scenarios(provider: string, bundled: string[]): Scenario[] {
	const list = entries(provider, bundled);
	if (provider === "fireworks") {
		return [
			{ name: "control-plane pages", replies: fireworksPages(bundled) },
			{ name: "control-plane empty", replies: [json({ models: [] })] },
			{ name: "http 500", replies: [json({ error: "boom" }, 500)] },
			{ name: "not json", replies: [{ status: 200, body: "oops" }] },
			{ name: "not an object", replies: [json([1, 2])] },
		];
	}
	return [
		{ name: "data envelope", replies: [json({ object: "list", data: list })] },
		{ name: "models envelope", replies: [json({ models: list })] },
		{ name: "result envelope", replies: [json({ result: list })] },
		{ name: "items envelope", replies: [json({ items: list })] },
		{ name: "bare array", replies: [json(list)] },
		{ name: "nested data", replies: [json({ data: { models: list } })] },
		{ name: "empty data", replies: [json({ data: [] })] },
		{ name: "http 401", replies: [json({ error: "no" }, 401)] },
		{ name: "http 500", replies: [json({ error: "boom" }, 500)] },
		{ name: "not json", replies: [{ status: 200, body: "oops" }] },
		{ name: "no list", replies: [json({ object: "list" })] },
	];
}

// Resolved compats are deduplicated: the same few shapes repeat across a roster.
const compats: any[] = [];
const compatKeys = new Map<string, number>();
const compatIndex = (compat: any): number => {
	const key = JSON.stringify(compat ?? null);
	let index = compatKeys.get(key);
	if (index === undefined) {
		index = compats.length;
		compats.push(compat ?? null);
		compatKeys.set(key, index);
	}
	return index;
};

const results: any[] = [];
for (const [provider, build] of Object.entries(managers)) {
	const bundledModels = getBundledModels(provider);
	const bundled: string[] = bundledModels.map((model: any) => model.id);
	// Rows with distinguishable metadata come first so the overrides are visible.
	const sorted = [...bundledModels].sort((a: any, b: any) => (b.contextWindow ?? 0) - (a.contextWindow ?? 0));
	const ids = sorted.map((model: any) => model.id);
	const pick = ids.length >= 6 ? ids : [...ids, ...bundled, ...bundled, ...bundled, ...bundled, ...bundled].slice(0, 6);
	for (const scenario of scenarios(provider, pick)) {
		const requests: any[] = [];
		let call = 0;
		const fetchImpl = async (input: any, init: any) => {
			const url = new URL(typeof input === "string" ? input : input.url);
			if (url.hostname === "catalog.stencil.so") return new Response("unavailable", { status: 503 });
			requests.push({
				method: init?.method ?? "GET",
				path: url.pathname,
				query: Object.fromEntries(url.searchParams),
				headers: Object.fromEntries(Object.entries(init?.headers ?? {}).map(([k, v]) => [k.toLowerCase(), v])),
			});
			const reply = scenario.replies[Math.min(call++, scenario.replies.length - 1)];
			return new Response(reply.body, { status: reply.status, headers: { "content-type": "application/json" } });
		};
		const options = build({ apiKey, fetch: fetchImpl });
		options.cacheDbPath = join(cacheDir, `${provider}-${results.length}.db`);
		const resolved = await resolveProviderModels(options, "online");
		results.push({
			provider,
			scenario: scenario.name,
			replies: scenario.replies,
			requests,
			source: resolved.source,
			stale: resolved.stale,
			models: resolved.models.map((model: any) => ({
				id: model.id,
				name: model.name,
				reasoning: model.reasoning,
				input: model.input,
				contextWindow: model.contextWindow ?? null,
				maxTokens: model.maxTokens ?? null,
				baseUrl: model.baseUrl,
				compatIndex: compatIndex(model.compat),
				...(model.thinking ? { thinking: model.thinking } : {}),
				...(model.supportsTools === false ? { supportsTools: false } : {}),
			})),
		});
	}
}

writeFileSync(outFile, Bun.gzipSync(JSON.stringify({ apiKey, compats, results })));

// What NeoPi's rule engine derives for a discovered row the bundle lacks and
// whose reasoning the mapper switched on. The Go port cannot run the rule
// engine, so it embeds these per provider/kind (see
// models/neopi_discovery/derived.json.gz).
const find = (provider: string, scenario: string, id: string) =>
	results.find(r => r.provider === provider && r.scenario === scenario)?.models.find((model: any) => model.id === id);
const derivedSource: Array<[string, string, string, string]> = [
	["nanogpt", "reasoning", "data envelope", "zz-base"],
	["fireworks", "reasoning", "control-plane pages", "zz-new-model"],
	["fireworks", "deepseek-v4", "control-plane pages", "deepseek-v4-zz"],
	["moonshot", "kimi-k3", "data envelope", "kimi-k3-0801"],
	["moonshot", "kimi-k2", "data envelope", "kimi-k2-new-turbo"],
	["alibaba-token-plan", "deepseek-v4", "data envelope", "deepseek-v4-new"],
];
const derived: any[] = derivedSource.map(([provider, kind, scenario, id]) => {
	const model = find(provider, scenario, id);
	if (!model) throw new Error(`no ${id} in ${provider}/${scenario}`);
	const { effortRouting: _routing, ...thinking } = model.thinking ?? {};
	return { provider, kind, id, reasoning: model.reasoning, input: model.input, contextWindow: model.contextWindow, maxTokens: model.maxTokens, thinking, compat: compats[model.compatIndex] };
});
// Token Plan's model manager uses NeoPi's authored seed rows (not the generated
// bundle) as the reference for every listed seed id: their explicit thinking
// ladder wins over the rule-derived one in the merge, so it is exported per id.
const { seedModels } = await import(root + "/pi-catalog/src/compat/providers.ts");
for (const seed of seedModels("alibaba-token-plan") as any[]) {
	if (!seed.thinking) continue;
	derived.push({ provider: "alibaba-token-plan", kind: `seed:${seed.id}`, id: seed.id, reasoning: seed.reasoning, input: seed.input, contextWindow: seed.contextWindow ?? null, maxTokens: seed.maxTokens ?? null, thinking: seed.thinking, compat: null });
}
const derivedFile = process.env.DERIVED_FILE ?? join(dirname(new URL(import.meta.url).pathname), "../models/neopi_discovery/derived.json.gz");
mkdirSync(dirname(derivedFile), { recursive: true });
writeFileSync(derivedFile, Bun.gzipSync(JSON.stringify(derived)));
console.log(`wrote ${derived.length} derived rows to ${derivedFile}`);
console.log(`wrote ${results.length} scenarios to ${outFile}`);
