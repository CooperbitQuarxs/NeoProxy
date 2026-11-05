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
	coreweave: m.coreWeaveModelManagerOptions,
	"cline-pass": m.clinePassModelManagerOptions,
	aimlapi: m.aimlApiModelManagerOptions,
	xiaomi: m.xiaomiModelManagerOptions,
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
	// Provider-shaped rows: the fields each provider's own mapper reads.
	const shaped: Record<string, unknown[]> = {
		aiand: [
			{ id: "zz-aiand-r", description: "  Described  ", capabilities: ["reasoning", "vision"], context_window: 200000, reasoning_efforts: ["low", "high", "bogus"], reasoning_effort_default: "high" },
			{ id: "zz-aiand-default-off", capabilities: ["reasoning"], reasoning_efforts: ["minimal", "max"], reasoning_effort_default: "medium" },
			{ id: "zz-aiand-plain", name: "Plain", description: "   ", capabilities: [], context_window: "x" },
			{ id: bundled[5], capabilities: ["reasoning"], reasoning_efforts: ["low", "medium"] },
		],
		baseten: [
			{ id: "zai-org/GLM-5.3", supported_features: ["reasoning", "tools"], input_modalities: ["text", "image"], context_length: 200000 },
			{ id: "zai-org/GLM-5.1", supported_features: ["reasoning"] },
			{ id: "moonshotai/Kimi-K3", supported_features: ["reasoning_effort"] },
			{ id: "deepseek-ai/DeepSeek-V4.1-Flash", supported_features: ["reasoning"], max_completion_tokens: 32000 },
			{ id: "openai/gpt-oss-120b", supported_features: ["reasoning", "tools"] },
			{ id: "meta/llama-new", supported_features: ["reasoning"] },
			{ id: bundled[5], supported_features: [], input_modalities: ["text"] },
		],
		coralbricks: [
			{ id: "zz-coral-off", supports_chat: false },
			{ id: "zz-pair", supports_reasoning: false },
			{ id: "zz-pair-thinking", supports_reasoning: true, reasoning: { supported_efforts: ["low", "high"], mandatory: true } },
			{ id: "zz-coral-r", supports_reasoning: true, supports_image_input: true, context_length: 64000, reasoning: { supported_efforts: ["none", "low", "high"], default_effort: "high", mandatory: false, disable: { reasoning_effort: "none" } } },
			{ id: "zz-coral-mandatory", supports_reasoning: true, reasoning: { supported_efforts: ["medium"], mandatory: true } },
			{ id: "zz-coral-empty", supports_reasoning: true, reasoning: { supported_efforts: [], disable: null } },
			{ id: "zz-coral-flag-only", supports_reasoning: true },
			{ id: bundled[5], supports_reasoning: false, supports_image_input: false },
		],
		deepinfra: [
			{ id: bundled[0], metadata: { tags: ["chat", "reasoning_effort", "vision"], context_length: 131072, max_tokens: 131072 } },
			{ id: "zz-di-chat", metadata: { tags: ["chat"], context_length: 64000, max_tokens: 8000 } },
			{ id: "zz-di-chat", metadata: { tags: ["chat", "reasoning"], context_length: 1000 } },
			{ id: "zz-di-tts", metadata: { tags: ["tts"] } },
			{ id: " zz-di-space ", metadata: { tags: ["chat", "reasoning", "vlm"] } },
			{ id: bundled[1], metadata: { tags: ["chat"], context_length: 1024 } },
			{ id: "zz-di-nometa" },
		],
		inception: [
			{ id: "zz-inc", supported_endpoints: ["chat.completions"], input_modalities: ["text", "image"], context_length: 128000, max_output_length: 16000 },
			{ id: bundled[0], name: "Inc Live", supported_endpoints: ["chat.completions", "fim"] },
			{ id: "zz-fim", supported_endpoints: ["fim"] },
		],
		novita: [
			{ id: "zz-nov", display_name: "Nov Display", status: 1, endpoints: ["chat/completions"], max_output_tokens: 8000, context_size: 131072, features: ["reasoning", "function-calling"], input_modalities: ["image"] },
			{ id: bundled[0], title: "Title Name", endpoints: ["chat/completions"], max_output_tokens: 4000, features: [] },
			{ id: "AI_INFER_TEST_x", endpoints: ["chat/completions"], max_output_tokens: 10 },
			{ id: "zz-inactive", status: 0, endpoints: ["chat/completions"], max_output_tokens: 10 },
			{ id: "zz-no-cap", endpoints: ["chat/completions"] },
		],
		sambanova: [{ id: "zz-Whisper-x" }, { id: "E5-Mistral-7B-Instruct" }, { id: "zz-clip-v" }],
		qianfan: [{ id: "zz-pair" }, { id: "zz-pair-thinking" }],
		synthetic: [
			{ id: "zz-syn-none", reasoning_parameters: { efforts: ["none"] } },
			{ id: "zz-syn-tiers", supported_features: ["reasoning", "tools"], reasoning_parameters: { efforts: ["low", "high"] }, input_modalities: ["text"], max_output_length: 32000 },
			{ id: "zz-syn-none-low", reasoning_parameters: { efforts: ["none", "low"] }, supports_vision: true, max_tokens: 16000 },
			{ id: "zz-syn-flag", supports_reasoning: true, max_output_length: null, max_tokens: 12000 },
			{ id: bundled[5], reasoning_parameters: { efforts: ["none"] }, input_modalities: ["text"] },
		],
		"wafer-serverless": [
			{ id: "GLM-5.4", wafer: { display_name: "Wafer GLM", capabilities: { reasoning: true, vision: true }, context_length: 200000, provider: "zai" } },
			{ id: "zz-wafer-r", wafer: { capabilities: { reasoning: true } } },
			{ id: bundled[5], max_model_len: 32000, wafer: { capabilities: { reasoning: false } } },
			{ id: "zz-wafer-big", wafer: { context_length: 1000000 } },
			{ id: "zz-wafer-qwen", wafer: { capabilities: { reasoning: true }, provider: "Alibaba Qwen" } },
			{ id: "zz-wafer-other", wafer: { capabilities: { reasoning: true }, provider: "deepseek" } },
			{ id: "Kimi-K3.5", wafer: { capabilities: { reasoning: true } } },
		],
		stepfun: [
			{ id: "stepaudio-2" }, { id: "Step-Image-Edit" },
			{ id: "zz-step-r", reasoning_effort_support_list: ["low", "high", "bogus"] },
			{ id: bundled[5], reasoning_effort_support_list: ["low"] },
		],
		helmcode: [{ id: "rerank" }, { id: "Kokoro" }, { id: "zz-helm-new", name: "Helm New" }],
		aimlapi: [
			{ id: "openai/tts-1-hd" }, { id: "flux-pro/v1.1" }, { id: "text-embedding-3-large" }, { id: "google/veo-3" },
			{ id: "openai/whisper-1" }, { id: "Stable-Image-Ultra" }, { id: "zz-lab/zz-chat-new", name: "ZZ Chat" },
			{ id: "anthropic/claude-sonnet-4-6" },
		],
		"xiaomi-token-plan-sgp": [{ id: "mimo-v3-tts" }, { id: bundled[0], display_name: "Display Name" }, { id: "zz-xiaomi-asr-x" }],
		"yolo-auto": [
			{ id: bundled[0], thinking: ["low", "high"] },
			{ id: "zz-yolo-r", thinking: ["medium", "bogus"] },
			{ id: "zz-yolo-plain", thinking: [] },
		],
		"zhipu-coding-plan": [
			{ id: "glm-5.4" }, { id: "glm-5.3-flash" }, { id: "glm-4.7-flash" }, { id: "glm-5v-turbo" }, { id: "glm-5.2-thinking" }, { id: "glm-4" },
			{ id: "glm-7.1" },
		],
	};
	list.push(...(shaped[provider] ?? []));
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

// ClinePass reads two hosts at once: Cline's recommended-models roster (the
// authoritative ids) and OpenRouter's public catalog (enrichment only), so
// its scenarios answer each host with its own reply.
type Scenario = { name: string; replies: Reply[]; expectFetchFails?: boolean; roster?: Reply; live?: Reply; apiKey?: string };

function clinePassScenarios(bundled: string[]): Scenario[] {
	const pass = [
		...bundled.filter(id => !id.includes("/")).map(id => ({ id: `cline-pass/${id}` })),
		{ id: "cline-pass/glm-5.3" },
		{ id: "cline-pass/kimi-k2.6" },
		{ id: "cline-pass/mimo-v2.5" },
		{ id: " cline-pass/zz-pass-live " },
		{ id: "cline-pass/zz-pass-unknown" },
		{ id: "cline-pass/glm-5.1" },
		{ id: "cline-pass/claude-sonnet-4-6" },
		{ id: "cline-pass/gpt-5.4-thinking" },
		{ id: "cline-pass/" },
		{ id: "unprefixed-model" },
		{ id: 5 },
		{ name: "no id" },
		null,
	];
	const free = [
		...bundled.filter(id => id.includes("/")).map(id => ({ id })),
		{ id: "deepseek/deepseek-v4-flash" },
		{ id: "poolside/laguna-s-2.1:free" },
		{ id: "cline-free/deepseek-v4.1-flash" },
		{ id: "qwen/qwen3-coder:free" },
		{ id: "zz-lab/zz-free-live" },
		{ id: "zz-lab/zz-free-slug" },
		{ id: "zz-lab/zz-free-unknown" },
		{ id: "zz-lab/zz-free-nbsp" },
		{ id: "[Tag] zz-lab/glm-5.1 [x]" },
		{ id: "cline-pass/should-skip" },
		{ id: "glm-5.3" },
		{ id: "  " },
		{ id: 7 },
	];
	const live = json({
		data: [
			{ id: "zz-vendor/zz-pass-live", name: "Vendor: ZZ Pass Live", context_length: 400000, top_provider: { max_completion_tokens: 50000 }, pricing: { prompt: "0.000001", completion: "0.000002", input_cache_read: "0.0000001" }, supported_parameters: ["tools", "reasoning"], architecture: { modality: "text+image->text" } },
			{ id: "zz-lab/zz-free-live", name: "ZZ Free Live (free)", context_length: 100000, top_provider: { max_completion_tokens: 0 }, pricing: { prompt: "0", completion: "0" }, supported_parameters: ["tools"], architecture: { modality: "text->text" } },
			{ id: "other/zz-free-slug", name: "Other: Slug Model", context_length: "x", supported_parameters: ["reasoning"] },
			{ id: "zz-lab/zz-free-nbsp", name: "Vendor:\u00a0NBSP Model (free)\u00a0", supported_parameters: [] },
			{ id: "zz-vendor/zz-pass-unknown-not", name: "Not it" },
			{ id: "", name: "blank" },
			7,
		],
	});
	const both = json({ clinePass: pass, free });
	return [
		{ name: "roster + live", replies: [], roster: both, live },
		{ name: "roster, live down", replies: [], roster: both, live: json({ error: "down" }, 503) },
		{ name: "roster, live not json", replies: [], roster: both, live: { status: 200, body: "oops" } },
		{ name: "roster, live empty", replies: [], roster: both, live: json({ data: [] }) },
		{ name: "pass only", replies: [], roster: json({ clinePass: pass }), live },
		{ name: "free not array", replies: [], roster: json({ clinePass: pass, free: { id: "x" } }), live },
		{ name: "no valid pass ids", replies: [], roster: json({ clinePass: [{ id: "unprefixed" }], free }), live },
		{ name: "missing clinePass", replies: [], roster: json({ free }), live },
		{ name: "http 500", replies: [], roster: json({ error: "boom" }, 500), live },
		{ name: "not json", replies: [], roster: { status: 200, body: "oops" }, live },
	];
}

function scenarios(provider: string, bundled: string[]): Scenario[] {
	const list = entries(provider, bundled);
	if (provider === "cline-pass") return clinePassScenarios(bundled);
	if (provider === "fireworks") {
		return [
			{ name: "control-plane pages", replies: fireworksPages(bundled) },
			{ name: "control-plane empty", replies: [json({ models: [] })] },
			{ name: "http 500", replies: [json({ error: "boom" }, 500)] },
			{ name: "not json", replies: [{ status: 200, body: "oops" }] },
			{ name: "not an object", replies: [json([1, 2])] },
		];
	}
	const generic: Scenario[] = [
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
	if (provider !== "xiaomi") return generic;
	// A Token Plan key on the standard provider walks the regional clusters,
	// SGP then AMS then CN, until one answers.
	const tp = "tp-capture-key";
	return [
		...generic,
		{ name: "token plan: sgp", apiKey: tp, replies: [json({ data: list })] },
		{ name: "token plan: ams after sgp 401", apiKey: tp, replies: [json({ error: "no" }, 401), json({ data: list })] },
		{ name: "token plan: cn after two failures", apiKey: "ttp-capture-key", replies: [json({ error: "boom" }, 500), { status: 200, body: "oops" }, json({ data: list })] },
		{ name: "token plan: every region fails", apiKey: tp, replies: [json({ error: "no" }, 401)] },
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
			const reply = scenario.roster
				? url.hostname === "openrouter.ai"
					? scenario.live!
					: scenario.roster
				: scenario.replies[Math.min(call++, scenario.replies.length - 1)];
			return new Response(reply.body, { status: reply.status, headers: { "content-type": "application/json" } });
		};
		const options = build({ apiKey: scenario.apiKey ?? apiKey, fetch: fetchImpl });
		options.cacheDbPath = join(cacheDir, `${provider}-${results.length}.db`);
		const resolved = await resolveProviderModels(options, "online");
		results.push({
			provider,
			scenario: scenario.name,
			replies: scenario.replies,
			...(scenario.apiKey ? { apiKey: scenario.apiKey } : {}),
			...(scenario.roster ? { roster: scenario.roster, live: scenario.live } : {}),
			requests,
			source: resolved.source,
			stale: resolved.stale,
			models: resolved.models.map((model: any) => ({
				id: model.id,
				api: model.api,
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
	// Unbundled reasoning ids with no close bundled sibling: the generic
	// ladder and compat NeoPi's rule engine gives each provider.
	["synthetic", "reasoning", "data envelope", "zz-syn-flag"],
	["novita", "reasoning", "data envelope", "zz-nov"],
	["deepinfra", "reasoning", "data envelope", "zz-di-space"],
	["wafer-serverless", "reasoning", "data envelope", "zz-wafer-r"],
	["zhipu-coding-plan", "reasoning", "data envelope", "glm-7.1"],
	// Qianfan lists no reasoning flags, so its generic ladder shows only on a
	// collapsed bare/thinking pair (derivePairThinkingSurface).
	["qianfan", "reasoning", "data envelope", "zz-pair"],
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
// The runtime exclude-models rules (compat/rules.json behavior.excludeModels)
// the discovery filters apply through isExcludedModel.
const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;
const excludeFile = join(dirname(derivedFile), "exclude_models.json");
writeFileSync(excludeFile, JSON.stringify(rules.behavior.excludeModels));
console.log(`wrote ${rules.behavior.excludeModels.length} exclude rules to ${excludeFile}`);
console.log(`wrote ${results.length} scenarios to ${outFile}`);

// ClinePass discovery inputs the Go port embeds: Cline's curated per-id
// metadata (provider-models/cline-pass.ts) and NeoPi's bundled proxy-reference
// index (identity/bundled.ts) with the vocabulary its candidate expansion
// strips, which resolves an id the ClinePass bundle lacks to its upstream row.
const { CLINE_PASS_MODEL_METADATA } = await import(root + "/pi-catalog/src/provider-models/cline-pass.ts");
const { getBundledModelReferenceIndex } = await import(root + "/pi-catalog/src/identity/bundled.ts");
const { discoveryVocabulary, collapseVocabulary } = await import(root + "/pi-catalog/src/compat/taxonomy.ts");
const referenceIndex = getBundledModelReferenceIndex();
const referenceRows: any[] = [];
const referenceRowIndex = new Map<any, number>();
const referenceRow = (model: any): number => {
	let index = referenceRowIndex.get(model);
	if (index === undefined) {
		index = referenceRows.length;
		referenceRowIndex.set(model, index);
		referenceRows.push({ provider: model.provider, id: model.id, name: model.name, reasoning: model.reasoning, input: model.input, contextWindow: model.contextWindow ?? null, maxTokens: model.maxTokens ?? null });
	}
	return index;
};
const discovery = discoveryVocabulary();
const collapse = collapseVocabulary();
const markerSuffixes = [
	...discovery.billingVariantSuffixes,
	...[...discovery.trailingMarkers, ...discovery.referenceOnlyTrailingMarkers].flatMap((marker: string) => [`-${marker}`, `:${marker}`]),
	...collapse.suffixes.map((rule: any) => rule.suffix),
	...collapse.routingVariants.map((rule: any) => rule.suffix),
];
const clinePassFile = join(dirname(derivedFile), "cline_pass.json.gz");
// The row NeoPi's ClinePass manager builds for each curated id: its catalog
// rules derive the effort map and compat from the id, which the port cannot
// evaluate, and the curated spec fixes everything else about the row.
const curatedBuilt: Record<string, { thinking: unknown; compat: unknown }> = {};
for (const [id, metadata] of Object.entries(CLINE_PASS_MODEL_METADATA) as [string, any][]) {
	const roster = metadata.tier === "free"
		? { clinePass: [{ id: "cline-pass/zz-anchor" }], free: [{ id }] }
		: { clinePass: [{ id: `cline-pass/${id}` }] };
	const fetchImpl = async (input: any) => {
		const url = new URL(typeof input === "string" ? input : input.url);
		if (url.hostname === "openrouter.ai") return new Response("unavailable", { status: 503 });
		return new Response(JSON.stringify(roster), { status: 200, headers: { "content-type": "application/json" } });
	};
	const options = m.clinePassModelManagerOptions({ apiKey, fetch: fetchImpl });
	options.cacheDbPath = join(cacheDir, `cline-pass-curated-${Object.keys(curatedBuilt).length}.db`);
	const resolved = await resolveProviderModels(options, "online");
	const model = resolved.models.find((candidate: any) => candidate.id === id);
	if (!model) throw new Error(`NeoPi built no ClinePass row for curated ${id}`);
	curatedBuilt[id] = { thinking: model.thinking ?? null, compat: model.compat };
}
writeFileSync(
	clinePassFile,
	Bun.gzipSync(
		JSON.stringify({
			source: "NeoPi OWN pi-catalog provider-models/cline-pass.ts CLINE_PASS_MODEL_METADATA and identity/bundled.ts getBundledModelReferenceIndex",
			metadata: CLINE_PASS_MODEL_METADATA,
			curatedBuilt,
			reference: {
				markerSuffixes,
				exact: Object.fromEntries([...referenceIndex.exact].map(([key, model]) => [key, referenceRow(model)])),
				suffixAlias: Object.fromEntries([...referenceIndex.suffixAlias].map(([key, model]) => [key, referenceRow(model)])),
				rows: referenceRows,
			},
		}),
	),
);
console.log(`wrote ClinePass metadata and ${referenceRows.length} reference rows to ${clinePassFile}`);
