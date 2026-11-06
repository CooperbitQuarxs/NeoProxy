// Captures NeoPi's OWN mapModelsDevToModels over MODELS_DEV_PROVIDER_DESCRIPTORS
// (provider-models/openai-compat.ts), the mapping loadModelsDevReferences
// builds its global reference map from, for one scripted models.dev payload
// that exercises every descriptor's filter, limit fallbacks and transform,
// runner kinds, duplicate properties at every depth and junk values.
//
// Writes testdata/neopi_modelsdev_global.json. Run from the NeoPi env
// directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file>
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { mapModelsDevToModels, MODELS_DEV_PROVIDER_DESCRIPTORS } = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");

const tool = (extra: Record<string, unknown> = {}) => ({ tool_call: true, limit: { context: 200000, output: 32000 }, ...extra });
const providers: Record<string, Record<string, unknown>> = {
	"amazon-bedrock": {
		"anthropic.claude-sonnet-4-5-20250929-v1:0": tool({ name: "Claude Sonnet 4.5", reasoning: true, modalities: { input: ["text", "image"] }, cost: { input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 } }),
		"meta.llama3-3-70b-instruct-v1:0": tool({ name: "Llama 3.3 70B" }),
		"deepseek.v3-v1:0": tool(),
		"openai.gpt-5.4": tool({ name: "Excluded" }),
		"amazon.titan-text": { tool_call: false },
		"cohere.embed-v4:0": { kind: "embedding", limit: { context: 128000 } },
	},
	"google-vertex": {
		"gemini-3-pro": tool({ name: "Gemini 3 Pro", modalities: { input: ["text", "image", "pdf"] } }),
		"gemini-2.0-flash": tool({ status: "deprecated" }),
		"zai-org/glm-5": tool(),
		"claude-opus-5-5@20260101": tool({ provider: { npm: "@ai-sdk/google-vertex/anthropic" } }),
		"llama-4-maas": tool({ provider: { npm: "@ai-sdk/openai-compatible" } }),
	},
	anthropic: {
		"claude-opus-5-5": tool({ name: "Claude Opus 5.5", reasoning: true, limit: { context: 1000000, output: 128000 } }),
		"claude-3-7-sonnet-20250219": tool({ name: "Excluded Sonnet" }),
	},
	google: {
		"gemini-3-flash": tool({ name: "Gemini 3 Flash", reasoning: true }),
		"gemini-3-flash-image": { kind: "image", name: "Gemini 3 Flash Image", reasoning: true, limit: { context: 32768, output: 8192 } },
		"gemini-3-tts": { kind: "tts", tool_call: true },
		"gemini-chat-kind": { kind: "chat", tool_call: true, limit: { context: 1048576, output: 65536 } },
		"gemini-chat-kind-no-tools": { kind: "chat", tool_call: false },
		"gemini-bogus-kind": { kind: "bogus", tool_call: true },
		"gemini-null-kind": { kind: null, tool_call: true },
	},
	openai: {
		"gpt-6": tool({ name: "GPT-6", reasoning: true, limit: { context: 400000, output: 128000 } }),
		"text-embedding-4": { kind: "embedding", name: "Embedding 4", limit: { context: 8192 } },
		"gpt-6-no-tools": { reasoning: true },
	},
	groq: {
		"llama-4-scout": tool({ limit: { context: 0, output: -5 } }),
		"123": tool({ name: "Index key" }),
		"kimi-k3": tool({ name: "Kimi K3 on Groq", limit: { context: 262144, output: 16384 }, cost: { input: "1.5", output: "junk" } }),
	},
	cerebras: { "gpt-oss-120b": tool({ reasoning: true }), "qwen-no-tools": {} },
	"cline-pass": { "anthropic/claude-sonnet-4.5": tool() },
	togetherai: { "moonshotai/Kimi-K3": tool({ limit: { context: 262144 } }) },
	wandb: { "openai/gpt-oss-120b": tool({ name: "gpt-oss 120B" }), "meta-llama/Llama-4": tool() },
	nvidia: { "nvidia/nemotron-ultra": { tool_call: true, limit: { output: 4096 } } },
	xai: {
		"grok-4.6": tool({ name: "Grok 4.6", reasoning: true }),
		"grok-3-mini": tool({ name: "Grok 3 Mini", reasoning: true }),
		"grok-code-fast-1": tool({ name: "Grok Code Fast", reasoning: true }),
	},
	deepseek: { "deepseek-v4-flash": tool({ name: "DeepSeek V4 Flash", reasoning: true }), "deepseek-chat": tool() },
	zai: { "glm-5.1": tool({ name: "GLM-5.1", reasoning: true }) },
	"umans-ai": { "umans-coder": tool() },
	xiaomi: { "mimo-v3": { tool_call: true } },
	"minimax-coding-plan": { "MiniMax-M3": tool() },
	"minimax-cn-coding-plan": { "MiniMax-M3": tool({ limit: { context: 300000, output: 32000 } }) },
	"alibaba-coding-plan": { "qwen3.8-coder": tool() },
	"zhipuai-coding-plan": { "glm-5.1": tool({ limit: { context: 200000, output: 64000 } }) },
	azure: { "gpt-6": tool({ name: "GPT-6 (Azure)" }), "Llama-4-Maverick": tool() },
	"cloudflare-ai-gateway": { "claude-opus-5-5": tool() },
	"cloudflare-workers-ai": { "@cf/meta/llama-4-scout": tool(), "@cf/old/model": tool({ status: "deprecated" }) },
	mistral: { "mistral-large-3": tool({ name: "Mistral Large 3" }) },
	opencode: {
		"kimi-k3": tool(),
		"old-zen": tool({ status: "deprecated" }),
		"gpt-zen-npm": tool({ provider: { npm: "@ai-sdk/openai" } }),
		"claude-zen-npm": tool({ provider: { npm: "@ai-sdk/anthropic" } }),
		"gemini-zen-npm": tool({ provider: { npm: "@ai-sdk/google" } }),
	},
	"opencode-go": { "glm-5.1": tool() },
	"github-copilot": {
		"claude-sonnet-4.5": tool(),
		"gpt-6": tool(),
		"gemini-3-pro": { tool_call: true },
	},
	minimax: { "MiniMax-M3": tool() },
	"minimax-cn": { "MiniMax-M3": tool() },
	huggingface: { "zai-org/GLM-5.1": tool() },
	kilo: { "anthropic/claude-opus-5-5": tool() },
	moonshotai: { "kimi-k3": tool({ limit: { context: 262144, output: 32768 } }) },
	"nano-gpt": { "deepseek-v4-pro": tool() },
	synthetic: { "hf:zai-org/GLM-5.1": tool() },
	venice: { "venice-uncensored": tool() },
	"ollama-cloud": { "gpt-oss:120b": tool() },
	"xiaomi-token-plan-ams": { "mimo-v3": tool() },
	"xiaomi-token-plan-cn": { "mimo-v3": tool() },
	"xiaomi-token-plan-sgp": { "mimo-v3": tool() },
	"qwen-portal": { "qwen3-coder-plus": { tool_call: true } },
	zenmux: { "anthropic/claude-opus-5-5": tool(), "old/model": tool({ status: "deprecated" }) },
	"not-a-descriptor": { "x": tool() },
};
const payload: Record<string, unknown> = {};
for (const [key, models] of Object.entries(providers)) payload[key] = { models };
// JSON.parse keeps the last of duplicate properties at every depth: a stale
// groq block first, a duplicate models key and a duplicate model key.
let body = JSON.stringify(payload);
body = `{"groq":{"models":{"stale-groq":${JSON.stringify(tool())}}},` + body.slice(1);
body = body.replace(`"mistral":{"models":{`, `"mistral":{"models":{"gone":${JSON.stringify(tool())}},"models":{"mistral-large-3":${JSON.stringify(tool({ name: "First Large" }))},`);

const rows = (mapModelsDevToModels(JSON.parse(body), MODELS_DEV_PROVIDER_DESCRIPTORS) as any[]).map(model => ({
	id: model.id,
	name: model.name,
	api: model.api,
	provider: model.provider,
	baseUrl: model.baseUrl,
	reasoning: model.reasoning === true,
	input: model.input,
	contextWindow: model.contextWindow ?? null,
	maxTokens: model.maxTokens ?? null,
	cost: model.cost,
	...(model.kind ? { kind: model.kind } : {}),
	...(model.compat ? { compat: model.compat } : {}),
	...(model.thinking ? { thinking: model.thinking } : {}),
}));

const out = join(dirname(new URL(import.meta.url).pathname), "neopi_modelsdev_global.json");
writeFileSync(out, JSON.stringify({ source: "NeoPi OWN mapModelsDevToModels(payload, MODELS_DEV_PROVIDER_DESCRIPTORS)", payload: body, rows }, null, "\t"));
console.log(`wrote ${rows.length} rows to ${out}`);
