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
// Cloudflare's transport routes each row per request (prepareModel); the
// exported catalog and the port keep the routed rows, so discovered rows are
// recorded routed too.
const { cloudflareAiGatewayTransport } = await import(root + "/pi-ai/src/registry/cloudflare-ai-gateway.ts");
const routed = (provider: string, model: any) =>
	provider === "cloudflare-ai-gateway" ? cloudflareAiGatewayTransport.prepareModel(model) : model;

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
	"singularityapi-dev": m.singularityApiDevModelManagerOptions,
	"singularityapi-tech": m.singularityApiTechModelManagerOptions,
	"charm-hyper": m.charmHyperModelManagerOptions,
	"alibaba-coding-plan": m.alibabaCodingPlanModelManagerOptions,
	kilo: m.kiloModelManagerOptions,
	siliconflow: m.siliconflowModelManagerOptions,
	"siliconflow-cn": m.siliconflowCnModelManagerOptions,
	vllm: m.vllmModelManagerOptions,
	"lm-studio": m.lmStudioModelManagerOptions,
	zenmux: m.zenmuxModelManagerOptions,
	"cloudflare-ai-gateway": m.cloudflareAiGatewayModelManagerOptions,
	commandcode: m.commandCodeModelManagerOptions,
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
			// Tariffs decide whether a thinking pair collapses: unequal stays
			// apart, equal folds; a non-USD card is unpriced.
			{ id: "zz-aiand-pair", currency: "usd", input_per_1m: "1", output_per_1m: "2" },
			{ id: "zz-aiand-pair-thinking", currency: "usd", input_per_1m: "3", output_per_1m: 4, capabilities: ["reasoning"] },
			{ id: "zz-aiand-same", input_per_1m: 1, output_per_1m: 2 },
			{ id: "zz-aiand-same-thinking", input_per_1m: 1, output_per_1m: 2, capabilities: ["reasoning"] },
			{ id: "zz-aiand-eur", currency: "eur", input_per_1m: 5, output_per_1m: 6 },
		],
		baseten: [
			{ id: "zai-org/GLM-5.3", supported_features: ["reasoning", "tools"], input_modalities: ["text", "image"], context_length: 200000 },
			{ id: "zai-org/GLM-5.1", supported_features: ["reasoning"] },
			{ id: "moonshotai/Kimi-K3", supported_features: ["reasoning_effort"] },
			{ id: "deepseek-ai/DeepSeek-V4.1-Flash", supported_features: ["reasoning"], max_completion_tokens: 32000 },
			{ id: "openai/gpt-oss-120b", supported_features: ["reasoning", "tools"] },
			{ id: "meta/llama-new", supported_features: ["reasoning"] },
			{ id: bundled[5], supported_features: [], input_modalities: ["text"] },
			{ id: "zz-bt-priced", pricing: { prompt: "0.0000006", completion: 0.0000022, input_cache_read: "x" } },
			{ id: "zz-bt-junk", pricing: "x" },
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
			{ id: "zz-di-priced", metadata: { tags: ["chat"], pricing: { input_tokens: 0.75, output_tokens: 2.5, cache_read_tokens: 0.1 }, discount: 0.35 } },
			{ id: "zz-di-bad-discount", metadata: { tags: ["chat"], pricing: { input_tokens: 1, output_tokens: "2" }, discount: 1.5 } },
		],
		inception: [
			{ id: "zz-inc", supported_endpoints: ["chat.completions"], input_modalities: ["text", "image"], context_length: 128000, max_output_length: 16000 },
			{ id: bundled[0], name: "Inc Live", supported_endpoints: ["chat.completions", "fim"] },
			{ id: "zz-fim", supported_endpoints: ["fim"] },
			{ id: "zz-inc-priced", supported_endpoints: ["chat.completions"], pricing: { prompt: "0.00000025", completion: "0.000001" } },
			{ id: bundled[1], supported_endpoints: ["chat.completions"], pricing: "x" },
		],
		novita: [
			{ id: "zz-nov", display_name: "Nov Display", status: 1, endpoints: ["chat/completions"], max_output_tokens: 8000, context_size: 131072, features: ["reasoning", "function-calling"], input_modalities: ["image"] },
			{ id: bundled[0], title: "Title Name", endpoints: ["chat/completions"], max_output_tokens: 4000, features: [] },
			{ id: "AI_INFER_TEST_x", endpoints: ["chat/completions"], max_output_tokens: 10 },
			{ id: "zz-inactive", status: 0, endpoints: ["chat/completions"], max_output_tokens: 10 },
			{ id: "zz-no-cap", endpoints: ["chat/completions"] },
			{ id: "zz-nov-priced", endpoints: ["chat/completions"], max_output_tokens: 8000, input_token_price_per_m: 5000, output_token_price_per_m: "20000", pricing: { input_cache_read: { price_per_m: 1250 } } },
			{ id: "zz-nov-cache-flat", endpoints: ["chat/completions"], max_output_tokens: 8000, input_token_price_per_m: 1, pricing: { input_cache_read: 9 } },
		],
		sambanova: [
			{ id: "zz-Whisper-x" }, { id: "E5-Mistral-7B-Instruct" }, { id: "zz-clip-v" },
			{ id: "zz-sn-priced", pricing: { prompt: "0.0000006", completion: "0.0000012", input_cache_read: null } },
			{ id: "zz-sn-partial", pricing: { prompt: "0.000001" } },
			{ id: "zz-sn-free", pricing: { prompt: 0, completion: 0 } },
			{ id: "zz-sn-bad-cache", pricing: { prompt: 0.000001, completion: 0.000002, input_cache_write: "x" } },
		],
		nanogpt: [
			{ id: "zz-nano-priced", pricing: { unit: "per_million_tokens", prompt: 0.5, completion: "2", cacheReadInputPer1kTokens: 0.0001, cacheWriteInputPer1kTokens: -1 } },
			{ id: "zz-nano-other-unit", pricing: { unit: "per_token", prompt: 1, completion: 2 } },
		],
		qianfan: [{ id: "zz-pair" }, { id: "zz-pair-thinking" }],
		synthetic: [
			{ id: "zz-syn-none", reasoning_parameters: { efforts: ["none"] } },
			{ id: "zz-syn-tiers", supported_features: ["reasoning", "tools"], reasoning_parameters: { efforts: ["low", "high"] }, input_modalities: ["text"], max_output_length: 32000 },
			{ id: "zz-syn-none-low", reasoning_parameters: { efforts: ["none", "low"] }, supports_vision: true, max_tokens: 16000 },
			{ id: "zz-syn-flag", supports_reasoning: true, max_output_length: null, max_tokens: 12000 },
			{ id: bundled[5], reasoning_parameters: { efforts: ["none"] }, input_modalities: ["text"] },
			{ id: "zz-syn-priced", pricing: { prompt: "$0.00000045", completion: "0.0000018", input_cache_reads: " $0.0000001 " } },
			{ id: "zz-syn-half", pricing: { prompt: "0.000001", input_cache_writes: "0.000002" } },
		],
		"wafer-serverless": [
			{ id: "GLM-5.4", wafer: { display_name: "Wafer GLM", capabilities: { reasoning: true, vision: true }, context_length: 200000, provider: "zai" } },
			{ id: "zz-wafer-r", wafer: { capabilities: { reasoning: true } } },
			{ id: bundled[5], max_model_len: 32000, wafer: { capabilities: { reasoning: false } } },
			{ id: "zz-wafer-big", wafer: { context_length: 1000000 } },
			{ id: "zz-wafer-qwen", wafer: { capabilities: { reasoning: true }, provider: "Alibaba Qwen" } },
			{ id: "zz-wafer-other", wafer: { capabilities: { reasoning: true }, provider: "deepseek" } },
			{ id: "Kimi-K3.5", wafer: { capabilities: { reasoning: true } } },
			{ id: "zz-wafer-priced", wafer: { pricing: { input_cents_per_million: 60, output_cents_per_million: "220", cache_read_cents_per_million: -1 } } },
		],
		stepfun: [
			{ id: "stepaudio-2" }, { id: "Step-Image-Edit" },
			{ id: "zz-step-r", reasoning_effort_support_list: ["low", "high", "bogus"] },
			{ id: bundled[5], reasoning_effort_support_list: ["low"] },
		],
		helmcode: [{ id: "rerank" }, { id: "Kokoro" }, { id: "zz-helm-new", name: "Helm New" }],
		// Unbundled ids NeoPi resolves through its bundled reference index.
		"gmi-cloud": [
			{ id: "zai-org/GLM-5.3", name: "GLM 5.3" }, { id: "deepseek-ai/DeepSeek-V4-Pro" }, { id: "moonshotai/Kimi-K3" },
			{ id: "Qwen/Qwen3-Coder-480B-A35B-Instruct" }, { id: "openai/gpt-oss-120b" },
		],
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
		// Per-endpoint capabilities pick each row's transport and limits.
		"singularityapi-dev": [
			{
				id: "deepseek-v4-flash",
				name: "DeepSeek V4 Flash",
				capabilities: [
					{
						endpoint: "/v1/chat/completions",
						context_window_tokens: 1048576,
						maximum_output_tokens: 384000,
						default_output_tokens: 8192,
						pricing: { input_per_million_usd: "0.140000000000", output_per_million_usd: "0.280000000000" },
					},
				],
			},
			{ id: "deepseek-v4-pro", capabilities: [{ endpoint: "/v1/chat/completions", context_window_tokens: "1048576", maximum_output_tokens: -1 }] },
			{ id: "gpt-5.6-sol", capabilities: [{ endpoint: "/v1/responses" }, { endpoint: "/v1/chat/completions", context_window_tokens: 400000, maximum_output_tokens: 128000 }] },
			{ id: "flux-1-schnell", name: "FLUX.1 schnell", capabilities: [{ endpoint: "/v1/images/generations", pricing: {} }] },
			{ id: "gpt-image-2", capabilities: [{ endpoint: "/v1/images/generations" }] },
			{ id: "flux-pro-1.1" },
			{ id: "zz-image-new", capabilities: ["bogus", { endpoint: "/v1/images/generations" }] },
			{ id: "zz-both", capabilities: [{ endpoint: "/v1/images/generations" }, { endpoint: "/v1/chat/completions", context_window_tokens: 64000 }] },
			{ id: "kimi-k3", capabilities: "not-an-array" },
			{ id: "glm-5.3", capabilities: [null, 5, { endpoint: "/v1/chat/completions", context_window_tokens: 0 }] },
			// An image-only member never folds into a chat thinking variant.
			{ id: "zz-sapi-pair", capabilities: [{ endpoint: "/v1/images/generations" }] },
			{ id: "zz-sapi-pair-thinking", capabilities: [{ endpoint: "/v1/chat/completions", context_window_tokens: 64000 }] },
		],
		"singularityapi-tech": [
			{ id: "deepseek-ai/DeepSeek-V4.1-Flash" },
			{ id: "deepseek-v4-flash-0731" },
			{ id: "deepseek-v4.1-flash", name: "  Alias  " },
			{ id: "zz-lane-new" },
		],
		// The advertised effort vocabulary is the ladder; none is the off switch.
		"charm-hyper": [
			{
				id: "glm-5.3",
				display_name: "GLM 5.3",
				context_window: 200000,
				max_output_tokens: 131072,
				capabilities: { vision: false },
				pricing: { input: "0.6", output: 2.2, cache_hit: 0, cache_create: 0 },
				reasoning: { effort_levels: [{ value: "none" }, { value: "low" }, { value: "medium" }, { value: "high" }, { value: "low" }], default_effort_level: "medium" },
			},
			{ id: "glm-5.1", context_window: "200000", max_output_tokens: 131072, reasoning: { effort_levels: [{ value: "low" }, { value: "high" }], default_effort_level: "max" } },
			{ id: "minimax-m2.7", context_window: 196608, max_output_tokens: 131072 },
			{ id: "kimi-k3", capabilities: { vision: true }, reasoning: { effort_levels: [{ value: "minimal" }, "bogus", { value: 3 }, { value: "xhigh" }, { value: "max" }] } },
			{ id: "zz-hyper-none-only", reasoning: { effort_levels: [{ value: "none" }] } },
			{ id: "zz-hyper-junk", display_name: "  ", context_window: -1, capabilities: "x", reasoning: "x" },
			{ id: "zz-hyper-pair", pricing: { input: 1, output: 2 } },
			{ id: "zz-hyper-pair-thinking", pricing: { input: 3, output: 4 }, reasoning: { effort_levels: [{ value: "low" }, { value: "high" }] } },
			{ id: "zz-hyper-same", pricing: { input: 1, output: 2, cache_create: -3 } },
			{ id: "zz-hyper-same-thinking", pricing: { input: 1, output: 2 }, reasoning: { effort_levels: [{ value: "low" }] } },
		],
		// Kilo prices per token (perTokenPricingCost); rows otherwise keep
		// the listing defaults.
		kilo: [
			{ id: "zz-kilo-priced", name: "Kilo Priced", context_length: 131072, pricing: { prompt: "0.0000006", completion: "0.0000022", input_cache_read: "0.00000015", input_cache_writes: "0.0000007" } },
			{ id: "zz-kilo-free", pricing: { prompt: "0", completion: "0" } },
			{ id: "zz-kilo-junk-pricing", pricing: "x" },
		],
		// SiliconFlow's bare listing: models.dev, the bundled reference index
		// or nothing hydrates each id; non-chat ids drop.
		siliconflow: [
			{ id: "zai-org/GLM-5.1" },
			{ id: "deepseek-ai/DeepSeek-V4-Pro", name: "Live V4 Pro", context_length: 777777, max_completion_tokens: 4321 },
			{ id: "moonshotai/Kimi-K3" },
			{ id: "Qwen/Qwen3-235B-A22B-Instruct-2507", name: "Live Qwen" },
			{ id: "openai/gpt-oss-120b" },
			{ id: "anthropic/claude-sonnet-4.5" },
			{ id: "BAAI/bge-m3" },
			{ id: "Kwai-Kolors/Kolors" },
			{ id: "  " },
			{ id: "zz-silicon-new" },
		],
		"siliconflow-cn": [{ id: "Pro/zai-org/GLM-5.1" }, { id: "deepseek-ai/DeepSeek-V4-Pro" }, { id: "FunAudioLLM/CosyVoice2-0.5B" }, { id: "zz-silicon-cn-new" }],
		// vLLM serves its context as max_model_len; Qwen 3.8+ always thinks.
		vllm: [
			{ id: "Qwen/Qwen3.8-32B", max_model_len: 32768 },
			{ id: "Qwen/Qwen3-32B", max_model_len: "40960" },
			{ id: "qwen3.9-coder", max_model_len: -1 },
			{ id: "meta-llama/Llama-4-Scout-17B-16E-Instruct" },
		],
		// LM Studio's listing; its native metadata answers per scenario.
		"lm-studio": [{ id: "qwen/qwen3.8-vl-8b" }, { id: "google/gemma-4-12b" }, { id: "lm-judge" }, { id: "lm-judge:2" }, { id: "zz-lms-cap" }],
		// ZenMux: Claude ids (by class, or by owner) move to the Anthropic
		// route; prices come from bucketed pricings, cache writes preferring
		// the 1-hour tier, then 5-minute, then the flat one.
		zenmux: [
			{
				id: "anthropic/claude-haiku-5.5",
				display_name: "Claude Haiku 5.5 (live)",
				owned_by: "anthropic",
				capabilities: { reasoning: true },
				input_modalities: ["text", "image"],
				context_length: 300000,
				max_completion_tokens: 64000,
				pricings: {
					prompt: [{ value: 1, unit: "perMTokens" }],
					completion: [{ value: "5" }],
					input_cache_read: [{ value: 0.1 }],
					input_cache_write_1_h: [{ value: 2 }],
					input_cache_write_5_min: [{ value: 1.25 }],
				},
			},
			{ id: "anthropic/claude-fable-5.1", owned_by: "Anthropic", pricings: { prompt: ["junk", { value: "x" }, { value: 3 }], input_cache_write_1_h: [{ value: 0 }], input_cache_write_5_min: [{ value: 3.75 }] } },
			{ id: "zz-lab/claude-sonnet-9", display_name: "  ", capabilities: { reasoning: "yes" }, input_modalities: "image", pricings: { input_cache_write: [{ value: 6 }] } },
			{ id: "zz-lab/house-model", owned_by: "ANTHROPIC", capabilities: { reasoning: true }, context_length: "65536" },
			{ id: "openai/gpt-5.6", display_name: "GPT 5.6", capabilities: { reasoning: true }, input_modalities: ["image"], pricings: { prompt: [{ value: 1.25 }], completion: [{ value: 10 }] } },
			{ id: "zz-zenmux-new", pricings: "junk", context_length: -1 },
		],
		// Command Code: Claude ids route to Messages at the provider root,
		// the route table's ids to Responses, everything else to Chat
		// Completions; rows start neutral and KDL policy fills them. The
		// listed seed id gives way to the appended seed.
		commandcode: [
			{ id: "claude-sonnet-5", name: "Claude Sonnet 5 (live)", context_length: 1000000 },
			{ id: "claude-zz-next", name: "Claude Next" },
			{ id: "gpt-5.5", context_length: "400000" },
			{ id: "deepseek/deepseek-v4-pro", context_length: 1048576 },
			{ id: "deepseek/deepseek-v4.1-flash" },
			{ id: "zai-org/GLM-5.2", context_length: 0 },
			{ id: "MiniMaxAI/MiniMax-M2.7", name: "  " },
			{ id: "typesafe/jev", name: "Listed Jev" },
			{ id: "zz-cc-new", name: "Brand New", context_length: 131072 },
		],
	};
	list.push(...(shaped[provider] ?? []));
	return list;
}

// The models.dev payload (catalog.stencil.so) SiliconFlow's manager joins
// against: hydration, tool-call and kind filtering, junk values.
function siliconFlowModelsDev(provider: string): unknown {
	return {
		[provider]: {
			models: {
				"zai-org/GLM-5.1": { name: "GLM-5.1", reasoning: true, tool_call: true, modalities: { input: ["text"] }, cost: { input: 1, output: 3.2, cache_read: 0.2 }, limit: { context: 202752, output: 131072 } },
				"Pro/zai-org/GLM-5.1": { name: "  ", reasoning: true, modalities: { input: ["text", "image"] }, limit: { context: "202752", output: 0 } },
				"deepseek-ai/DeepSeek-V4-Pro": { name: "DeepSeek V4 Pro", reasoning: true, tool_call: false, modalities: { input: ["text"] }, cost: { input: 0.55, output: 2.19 }, limit: { context: 1048576, output: 65536 } },
				"moonshotai/Kimi-K3": { name: "Kimi K3", reasoning: "yes", tool_call: true, modalities: { input: ["text", "image", "video"] }, limit: { context: 262144, output: 32768 } },
				"BAAI/bge-m3": { name: "BGE M3", kind: "embedding", modalities: { input: ["text"] }, limit: { context: 8192 } },
				"anthropic/claude-sonnet-4.5": { name: "Sonnet via SF", kind: "chat", limit: { context: -5, output: "x" } },
				"zz-models-dev-only": { name: "Not Listed", tool_call: true, limit: { context: 4096, output: 1024 } },
				"zz-not-a-record": 5,
			},
		},
		"other-provider": { models: { "zz-silicon-new": { name: "Wrong Provider", limit: { context: 1 } } } },
	};
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
// baseUrl is the provider base an account configures (NeoPi's provider
// baseUrl override), absent for the manager's default.
type Scenario = { name: string; replies: Reply[]; expectFetchFails?: boolean; roster?: Reply; live?: Reply; apiKey?: string; keyless?: boolean; modelsDev?: Reply; native?: { v0?: Reply; v1?: Reply }; baseUrl?: string };

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
		// Built without a key: a manager with a public listing still fetches
		// (without Authorization); the others serve the bundled roster.
		{ name: "no key", keyless: true, replies: [json({ object: "list", data: list })] },
	];
	if (provider === "lm-studio") {
		// LM Studio's native /api/v0 (input, served window) and /api/v1
		// (decision judges) answers; without them only the listing counts.
		const v0 = json({
			data: [
				{ id: "qwen/qwen3.8-vl-8b", type: "vlm", max_context_length: 131072 },
				{ id: "google/gemma-4-12b", type: "LLM", state: "loaded", loaded_context_length: 8192, max_context_length: 131072 },
				{ id: "zz-lms-cap", type: "llm", state: "unloaded", loaded_context_length: 4096, capabilities: ["Vision", 5], context_length: "16384" },
				{ id: "", type: "vlm" },
				"junk",
			],
		});
		const v1 = json({
			models: [
				{ key: "lm-judge", type: "decision", has_vision_adapter: true, max_context_length: 4096, loaded_instances: [{ id: "lm-judge:2", config: { context_length: 2048 } }, "junk"] },
				{ key: "google/gemma-4-12b", type: "llm" },
			],
		});
		const listing = json({ object: "list", data: list });
		return [
			...generic,
			{ name: "native metadata", native: { v0, v1 }, replies: [listing] },
			{ name: "native v1 only", native: { v0: json({ error: "no" }, 404), v1 }, replies: [listing] },
			{ name: "native v0 only", native: { v0, v1: { status: 200, body: "oops" } }, replies: [listing] },
			{ name: "native junk", native: { v0: json({ data: "x" }), v1: json({ models: {} }) }, replies: [listing] },
		];
	}
	if (provider === "siliconflow" || provider === "siliconflow-cn") {
		// models.dev answers each scenario; without it only the bundled
		// reference index hydrates.
		const modelsDev = json(siliconFlowModelsDev(provider));
		return [
			...generic,
			{ name: "models.dev", modelsDev, replies: [json({ object: "list", data: list })] },
			{ name: "models.dev, empty listing", modelsDev, replies: [json({ data: [] })] },
			{ name: "models.dev not json", modelsDev: { status: 200, body: "oops" }, replies: [json({ data: list })] },
			{ name: "models.dev without provider", modelsDev: json({ elsewhere: {} }), replies: [json({ data: list })] },
			// JSON.parse keeps the last of duplicate properties, at every depth.
			{
				name: "models.dev duplicate keys",
				modelsDev: {
					status: 200,
					body: `{"${provider}":{"models":{"zai-org/GLM-5.1":{"name":"Old","limit":{"context":300000,"output":90}},"zai-org/GLM-5.1":{"name":"New","reasoning":true,"limit":{"context":10,"output":5},"limit":{"context":64000,"output":4000}}}},"${provider}":{"models":{"zai-org/GLM-5.1":{"name":"Last Provider","limit":{"context":32000}}}}}`,
				},
				replies: [json({ data: list })],
			},
		];
	}
	if (provider === "cloudflare-ai-gateway") {
		// An Anthropic /v1/models listing on the configured base, else on the
		// unfilled gateway placeholders; the login credential is the raw key.
		const listing = json({ object: "list", data: list });
		const credential = JSON.stringify({ token: "cfut_capture", accountId: "acct", gatewayId: "gw" });
		return [
			...generic,
			{ name: "structured credential", apiKey: credential, replies: [listing] },
			{ name: "configured base", baseUrl: "https://gw.example/acct/gw/anthropic/", replies: [listing] },
			{ name: "configured v1 base", baseUrl: "https://gw.example/custom/v1", apiKey: credential, replies: [listing] },
			{ name: "oauth-shaped key", apiKey: "sk-ant-oat01-capture", baseUrl: "https://gw.example/acct/gw/anthropic", replies: [listing] },
		];
	}
	if (provider === "zenmux") {
		// A configured Anthropic-route base names the OpenAI route; any other
		// path keeps its own listing and puts Claude rows on /api/anthropic.
		const listing = json({ object: "list", data: list });
		return [
			...generic,
			{ name: "anthropic-route base", baseUrl: "https://eu.zenmux.example/api/anthropic/", replies: [listing] },
			{ name: "custom path base", baseUrl: "https://proxy.example/zen/v2", replies: [listing] },
		];
	}
	if (provider === "commandcode") {
		// A configured base is normalized to the /provider root: a trailing
		// /v1 is dropped, so Claude rows use the root and the rest /v1.
		const listing = json({ object: "list", data: list });
		return [
			...generic,
			{ name: "v1 base", baseUrl: "https://cc.example/provider/v1/", replies: [listing] },
			{ name: "root base", baseUrl: "https://cc.example/root", replies: [listing] },
		];
	}
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
			if (url.hostname === "catalog.stencil.so") {
				const reply = scenario.modelsDev ?? { status: 503, body: "unavailable" };
				return new Response(reply.body, { status: reply.status, headers: { "content-type": "application/json" } });
			}
			requests.push({
				method: init?.method ?? "GET",
				path: url.pathname,
				query: Object.fromEntries(url.searchParams),
				headers: Object.fromEntries(Object.entries(init?.headers ?? {}).map(([k, v]) => [k.toLowerCase(), v])),
			});
			const nativeKey = provider !== "lm-studio" ? undefined : url.pathname === "/api/v0/models" ? "v0" : url.pathname === "/api/v1/models" ? "v1" : undefined;
			const reply = nativeKey
				? (scenario.native?.[nativeKey] ?? { status: 404, body: "{}" })
				: scenario.roster
					? url.hostname === "openrouter.ai"
						? scenario.live!
						: scenario.roster
					: scenario.replies[Math.min(call++, scenario.replies.length - 1)];
			return new Response(reply.body, { status: reply.status, headers: { "content-type": "application/json" } });
		};
		const options = build({
			apiKey: scenario.keyless ? undefined : (scenario.apiKey ?? apiKey),
			fetch: fetchImpl,
			...(scenario.baseUrl ? { baseUrl: scenario.baseUrl } : {}),
		});
		options.cacheDbPath = join(cacheDir, `${provider}-${results.length}.db`);
		const resolved = await resolveProviderModels(options, "online");
		results.push({
			provider,
			scenario: scenario.name,
			replies: scenario.replies,
			...(scenario.apiKey ? { apiKey: scenario.apiKey } : {}),
			...(scenario.baseUrl ? { baseUrl: scenario.baseUrl } : {}),
			...(scenario.keyless ? { keyless: true } : {}),
			...(scenario.modelsDev ? { modelsDev: scenario.modelsDev } : {}),
			...(scenario.native ? { native: scenario.native } : {}),
			...(scenario.roster ? { roster: scenario.roster, live: scenario.live } : {}),
			requests,
			source: resolved.source,
			stale: resolved.stale,
			models: resolved.models.map((raw: any) => routed(provider, raw)).map((model: any) => ({
				id: model.id,
				api: model.api,
				name: model.name,
				reasoning: model.reasoning,
				input: model.input,
				contextWindow: model.contextWindow ?? null,
				maxTokens: model.maxTokens ?? null,
				baseUrl: model.baseUrl,
				...(model.requestModelId && model.requestModelId !== model.id ? { requestModelId: model.requestModelId } : {}),
				compatIndex: compatIndex(model.compat),
				...(model.thinking ? { thinking: model.thinking } : {}),
				...(model.kind ? { kind: model.kind } : {}),
				...(model.supportsTools === false ? { supportsTools: false } : {}),
				// The tariff decides whether NeoPi collapses a thinking pair.
				cost: { input: model.cost?.input ?? 0, output: model.cost?.output ?? 0, cacheRead: model.cost?.cacheRead ?? 0, cacheWrite: model.cost?.cacheWrite ?? 0 },
			})),
		});
	}
}

writeFileSync(outFile, Bun.gzipSync(JSON.stringify({ apiKey, compats, results })));

// Token Plan's model manager takes NeoPi's authored seed specs (not the
// generated bundle) as its static rows and the reference for every listed
// seed id, so they are exported whole.
const { seedModels } = await import(root + "/pi-catalog/src/compat/providers.ts");
const seeds = (seedModels("alibaba-token-plan") as any[]).map(seed => ({
	id: seed.id,
	name: seed.name,
	reasoning: seed.reasoning,
	input: seed.input,
	contextWindow: seed.contextWindow ?? null,
	maxTokens: seed.maxTokens ?? null,
	...(seed.thinking ? { thinking: seed.thinking } : {}),
	...(seed.compat ? { compat: seed.compat } : {}),
}));
const discoveryDir = join(dirname(new URL(import.meta.url).pathname), "../models/neopi_discovery");
mkdirSync(discoveryDir, { recursive: true });
const seedsFile = join(discoveryDir, "alibaba_token_plan_seeds.json.gz");
writeFileSync(seedsFile, Bun.gzipSync(JSON.stringify(seeds)));
console.log(`wrote ${seeds.length} Token Plan seed specs to ${seedsFile}`);
// The runtime exclude-models rules (compat/rules.json behavior.excludeModels)
// the discovery filters apply through isExcludedModel.
const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;
const excludeFile = join(discoveryDir, "exclude_models.json");
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
		referenceRows.push({ provider: model.provider, id: model.id, name: model.name, reasoning: model.reasoning, input: model.input, contextWindow: model.contextWindow ?? null, maxTokens: model.maxTokens ?? null, ...(model.thinking ? { thinking: model.thinking } : {}), cost: { input: model.cost?.input ?? 0, output: model.cost?.output ?? 0, cacheRead: model.cost?.cacheRead ?? 0, cacheWrite: model.cost?.cacheWrite ?? 0 } });
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
const clinePassFile = join(discoveryDir, "cline_pass.json.gz");
writeFileSync(
	clinePassFile,
	Bun.gzipSync(
		JSON.stringify({
			source: "NeoPi OWN pi-catalog provider-models/cline-pass.ts CLINE_PASS_MODEL_METADATA and identity/bundled.ts getBundledModelReferenceIndex",
			metadata: CLINE_PASS_MODEL_METADATA,
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
