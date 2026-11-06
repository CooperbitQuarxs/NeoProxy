// Runs NeoPi's OWN openai-completions provider through streamSimple (the agent
// entry) for the providers named in NEOPI_PROVIDERS (comma list; default: the
// openai-keys lane) against a loopback capture server only: every request is
// redirected to 127.0.0.1 and the keys are fakes, so nothing reaches a host.
//
// Writes, per provider, into OUT_DIR (default: testdata/neopi_completions):
//   <provider>.fixtures.json.gz  the captured requests beside the downstream
//                                Chat Completions body NeoProxy receives for them
//   and, into CATALOG_DIR (default: internal/registry/models/neopi_completions):
//   <provider>.json.gz           NeoPi's bundled rows with resolved compat
//                                (deduplicated) and authored compatConfig.
//
// Run from the NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file>
import { mkdirSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const worktree = process.env.NEOPROXY_WORKTREE ?? "/mnt/alias/m2/agents/worktrees/neoproxy-openai-keys";
const outDir = process.env.OUT_DIR ?? join(worktree, "internal/runtime/executor/testdata/neopi_completions");
const catalogDir = process.env.CATALOG_DIR ?? join(worktree, "internal/registry/models/neopi_completions");
mkdirSync(outDir, { recursive: true });
mkdirSync(catalogDir, { recursive: true });

const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { PROVIDER_DESCRIPTORS } = await import(root + "/pi-catalog/src/provider-models/descriptors.ts");
const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
const { seedModels } = await import(root + "/pi-catalog/src/compat/providers.ts");
// Cloudflare AI Gateway routes each model per request (api-routes): its rows
// are exported, and captured, as that transport prepares them.
const { cloudflareAiGatewayTransport } = await import(root + "/pi-ai/src/registry/cloudflare-ai-gateway.ts");
const prepareModel = (provider: string, model: any) =>
	provider === "cloudflare-ai-gateway" ? cloudflareAiGatewayTransport.prepareModel(model) : model;
const cacheDir = mkdtempSync(join(tmpdir(), "neopi-completions-"));

// Providers NeoPi bundles no rows for discover their whole roster; their
// captures run over the rows NeoPi's own manager builds from this listing,
// which the fixtures record so NeoProxy builds the same rows from it.
const discoveredRosters: Record<string, any[]> = {
	"singularityapi-dev": ["deepseek-v4-flash", "deepseek-v4-pro", "gpt-5.6-sol", "kimi-k3", "glm-5.3", "zz-gateway-new"].map(id => ({ id })),
	"singularityapi-tech": ["deepseek-ai/DeepSeek-V4.1-Flash", "deepseek-v4-flash-0731", "deepseek-v4.1-flash", "zz-lane-new"].map(id => ({ id })),
	"charm-hyper": [
		{
			id: "glm-5.3",
			display_name: "GLM 5.3",
			context_window: 200000,
			max_output_tokens: 131072,
			capabilities: { vision: false },
			reasoning: { effort_levels: [{ value: "none" }, { value: "low" }, { value: "medium" }, { value: "high" }], default_effort_level: "medium" },
		},
		{ id: "glm-5.1", context_window: 200000, max_output_tokens: 131072, reasoning: { effort_levels: [{ value: "low" }, { value: "high" }] } },
		{ id: "minimax-m2.7", context_window: 196608, max_output_tokens: 131072 },
		{ id: "kimi-k3", capabilities: { vision: true }, reasoning: { effort_levels: [{ value: "minimal" }, { value: "xhigh" }, { value: "max" }] } },
		{ id: "zz-hyper-plain" },
	],
	// SiliconFlow lists bare ids: models.dev hydrates the ids it knows, NeoPi's
	// bundled reference index the rest; embeddings and generators drop by id.
	siliconflow: ["zai-org/GLM-5.1", "deepseek-ai/DeepSeek-V4-Pro", "moonshotai/Kimi-K3", "Qwen/Qwen3-235B-A22B-Instruct-2507", "openai/gpt-oss-120b", "BAAI/bge-m3", "black-forest-labs/FLUX.1-schnell", "zz-silicon-new"].map(id => ({ id })),
	"siliconflow-cn": ["deepseek-ai/DeepSeek-V4-Pro", "Pro/zai-org/GLM-5.1", "Qwen/Qwen3-235B-A22B-Instruct-2507", "FunAudioLLM/SenseVoiceSmall", "zz-silicon-cn-new"].map(id => ({ id })),
	// Local servers list what they serve; vLLM reports its served context.
	vllm: [
		{ id: "Qwen/Qwen3.8-32B", max_model_len: 32768 },
		{ id: "Qwen/Qwen3-32B", max_model_len: 40960 },
		{ id: "meta-llama/Llama-4-Scout-17B-16E-Instruct" },
		{ id: "zz-vllm-new", max_model_len: "8192" },
	],
	// LM Studio's native metadata marks VLMs, served windows and judges.
	"lm-studio": ["qwen/qwen3.8-vl-8b", "google/gemma-4-12b", "lm-judge", "lm-judge:2", "zz-lms-new"].map(id => ({ id })),
	// LiteLLM answers /model_group/info: OpenAI groups run on Responses, a
	// decisions group judges, non-chat modes and sentinels drop, and ids are
	// hydrated from models.dev, then NeoPi's bundled references.
	litellm: [
		{
			model_group: "gpt-6-proxy",
			providers: ["openai"],
			mode: "chat",
			max_input_tokens: 400000,
			max_output_tokens: 128000,
			supports_vision: true,
			supports_reasoning: true,
			supported_openai_params: ["tools", "tool_choice", "reasoning_effort"],
			input_cost_per_token: 0.00000125,
			output_cost_per_token: 0.00001,
			cache_read_input_token_cost: 1.25e-7,
		},
		{
			model_group: "claude-opus-5-5",
			providers: ["anthropic"],
			mode: "chat",
			max_input_tokens: 1000000,
			max_output_tokens: 128000,
			supports_vision: true,
			supports_reasoning: true,
			supports_function_calling: true,
			supported_openai_params: ["tools", "tool_choice", "reasoning_effort", "thinking"],
		},
		{ model_group: "kimi-k3", providers: ["fireworks_ai"], mode: "chat", supports_vision: false },
		{ model_group: "deepseek-v4-flash", providers: ["deepseek"], mode: "chat", supports_vision: false, supports_reasoning: true },
		{ model_group: "glm-5.1", model_name: "GLM 5.1 (2x usage)", providers: ["zai"], mode: "chat", supports_vision: false },
		{ model_group: "judge-1", providers: ["openai"], mode: "decisions", supports_vision: false },
		{ model_group: "text-embed-x", providers: ["openai"], mode: "embedding" },
		{ model_group: "all-proxy-models", providers: [] },
		{ model_group: "zz-proxy-alias", providers: ["bedrock"], mode: "chat", supports_vision: false, max_input_tokens: 64000 },
	],
	// Local Ollama lists over /v1/models; its rows run on Responses, so the
	// export keeps only the endpoint and the discovery flags.
	ollama: [{ id: "qwen3:8b" }],
};

// LM Studio's native answers (GET /api/v0/models and /api/v1/models).
const lmStudioNative = {
	v0: {
		data: [
			{ id: "qwen/qwen3.8-vl-8b", type: "vlm", max_context_length: 131072 },
			{ id: "google/gemma-4-12b", type: "llm", state: "loaded", loaded_context_length: 8192, max_context_length: 131072, capabilities: ["tool_use"] },
		],
	},
	v1: { models: [{ key: "lm-judge", type: "decision", has_vision_adapter: true, max_context_length: 4096, loaded_instances: [{ id: "lm-judge:2", config: { context_length: 2048 } }] }] },
};

// The models.dev catalog (catalog.stencil.so) a manager consults for
// reference metadata, keyed by the provider whose discovery fetches it.
const modelsDevPayloads: Record<string, any> = {
	siliconflow: {
		siliconflow: {
			models: {
				"zai-org/GLM-5.1": { name: "GLM-5.1", reasoning: true, tool_call: true, modalities: { input: ["text"] }, cost: { input: 1, output: 3.2, cache_read: 0.2 }, limit: { context: 202752, output: 131072 } },
				"deepseek-ai/DeepSeek-V4-Pro": { name: "DeepSeek V4 Pro", reasoning: true, tool_call: true, modalities: { input: ["text"] }, cost: { input: 0.55, output: 2.19 }, limit: { context: 1048576, output: 65536 } },
				"moonshotai/Kimi-K3": { name: "Kimi K3", reasoning: true, tool_call: true, modalities: { input: ["text", "image"] }, limit: { context: 262144, output: 32768 } },
				"BAAI/bge-m3": { name: "BGE M3", kind: "embedding", modalities: { input: ["text"] }, limit: { context: 8192 } },
				"zz-models-dev-only": { name: "Not Listed", tool_call: true, limit: { context: 4096, output: 1024 } },
			},
		},
	},
	"siliconflow-cn": {
		"siliconflow-cn": {
			models: {
				"deepseek-ai/DeepSeek-V4-Pro": { name: "DeepSeek V4 Pro", reasoning: true, tool_call: false, modalities: { input: ["text"] }, cost: { input: 4, output: 16 }, limit: { context: 1048576, output: 65536 } },
				"Pro/zai-org/GLM-5.1": { name: "  ", reasoning: true, modalities: { input: ["text", "image"] }, limit: { context: "202752", output: 0 } },
			},
		},
	},
	// LiteLLM hydrates ids against every models.dev provider.
	litellm: {
		moonshotai: {
			models: {
				"kimi-k3": { name: "Kimi K3", reasoning: true, tool_call: true, modalities: { input: ["text", "image"] }, cost: { input: 0.6, output: 2.5, cache_read: 0.15 }, limit: { context: 262144, output: 32768 } },
			},
		},
	},
};

async function discoveredRows(provider: string, apiKey: string): Promise<{ models: any[]; listing?: string; modelsDev?: string; lmStudioV0?: string; lmStudioV1?: string }> {
	const entries = discoveredRosters[provider];
	if (!entries) return { models: [] };
	const descriptor = PROVIDER_DESCRIPTORS.find((entry: any) => entry.providerId === provider);
	const listing = provider === "litellm" ? JSON.stringify({ data: entries }) : JSON.stringify({ object: "list", data: entries.map(entry => ({ object: "model", ...entry })) });
	const modelsDev = modelsDevPayloads[provider] ? JSON.stringify(modelsDevPayloads[provider]) : undefined;
	const native = provider === "lm-studio" ? { v0: JSON.stringify(lmStudioNative.v0), v1: JSON.stringify(lmStudioNative.v1) } : undefined;
	const options = descriptor.createModelManagerOptions({
		apiKey,
		fetch: async (input: any) => {
			const url = new URL(typeof input === "string" ? input : input.url);
			if (url.hostname === "catalog.stencil.so") {
				return modelsDev ? new Response(modelsDev, { status: 200, headers: { "content-type": "application/json" } }) : new Response("unavailable", { status: 503 });
			}
			if (native && url.pathname === "/api/v0/models") return new Response(native.v0, { status: 200, headers: { "content-type": "application/json" } });
			if (native && url.pathname === "/api/v1/models") return new Response(native.v1, { status: 200, headers: { "content-type": "application/json" } });
			// LiteLLM's groups answer on /model_group/info; its other
			// management endpoints are absent.
			if (provider === "litellm") {
				return url.pathname === "/model_group/info"
					? new Response(listing, { status: 200, headers: { "content-type": "application/json" } })
					: new Response("not found", { status: 404 });
			}
			return new Response(listing, { status: 200, headers: { "content-type": "application/json" } });
		},
	});
	options.cacheDbPath = join(cacheDir, `${provider}.db`);
	const resolved = await resolveProviderModels(options, "online");
	return { models: resolved.models, listing, modelsDev, lmStudioV0: native?.v0, lmStudioV1: native?.v1 };
}

const providers = (process.env.NEOPI_PROVIDERS ?? "cerebras,deepseek,fireworks,groq,huggingface,mistral,moonshot,nanogpt,nvidia,together,alibaba-token-plan").split(",");
const maxRepresentatives = Number(process.env.MAX_MODELS_PER_PROVIDER ?? 24);

const sessionId = "capture-session-completions-0001";
const chatChunks = [
	{ id: "chatcmpl-capture", object: "chat.completion.chunk", created: 1, model: "capture", choices: [{ index: 0, delta: { role: "assistant", content: "Captured." }, finish_reason: null }] },
	{ id: "chatcmpl-capture", object: "chat.completion.chunk", created: 1, model: "capture", choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 3, completion_tokens: 1, total_tokens: 4 } },
];
const chatSSE = chatChunks.map(chunk => `data: ${JSON.stringify(chunk)}\n\n`).join("") + "data: [DONE]\n\n";
let captured: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const rawBody = await request.text();
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody });
		return new Response(chatSSE, { headers: { "content-type": "text/event-stream" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const richTool = {
	name: "rich",
	description: "A tool whose schema uses constructs hosts treat differently.",
	parameters: {
		type: "object",
		properties: {
			mode: { const: "fast" },
			kind: { type: ["string", "null"], enum: ["a", "b", null] },
			items: { type: "array", items: { type: "string" }, minItems: 1, maxItems: 3 },
			label: { type: "string", pattern: "^[a-z]+$", minLength: 1 },
			opt: { type: "integer" },
		},
		required: ["mode", "items"],
		additionalProperties: false,
	},
};
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
const user = (content: unknown) => ({ role: "user", content, timestamp: 0 });

type Turn =
	| { kind: "user"; text?: string; blocks?: any[] }
	| { kind: "call"; think?: boolean; text?: string }
	| { kind: "result"; blocks: any[]; isError?: boolean }
	| { kind: "assistant"; text: string };

const assistantCall = (model: any, turn: { think?: boolean; text?: string }) => ({
	role: "assistant",
	content: [
		...(turn.think === false ? [] : [{ type: "thinking", thinking: "Need lookup.", thinkingSignature: "reasoning_content" }]),
		...(turn.text ? [{ type: "text", text: turn.text }] : []),
		{ type: "toolCall", id: "call_capture_1", name: "lookup", arguments: { term: "a" } },
	],
	api: model.api,
	provider: model.provider,
	model: model.id,
	usage,
	stopReason: "toolUse",
	timestamp: 0,
});
const toolResult = (content: unknown[], isError = false) => ({ role: "toolResult", toolCallId: "call_capture_1", toolName: "lookup", content, isError, timestamp: 0 });

const chatTurns = (turns: Turn[], withThinking: boolean) =>
	turns.map(turn => {
		if (turn.kind === "user") return { role: "user", content: turn.text ?? turn.blocks!.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image_url", image_url: { url: `data:image/png;base64,${b.source.data}` } })) };
		if (turn.kind === "assistant") return { role: "assistant", content: turn.text };
		if (turn.kind === "call") {
			const message: any = { role: "assistant", content: turn.text ?? "" };
			if (turn.think !== false && withThinking) message.reasoning_content = "Need lookup.";
			message.tool_calls = [{ id: "call_capture_1", type: "function", function: { name: "lookup", arguments: "{\"term\":\"a\"}" } }];
			return message;
		}
		const parts = turn.blocks.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image_url", image_url: { url: `data:image/png;base64,${b.source.data}` } }));
		return { role: "tool", tool_call_id: "call_capture_1", content: turn.blocks.some((b: any) => b.type !== "text") ? parts : turn.blocks.map((b: any) => b.text).join("\n") };
	});
const neopiTurns = (model: any, turns: Turn[]) =>
	turns.map(turn => {
		if (turn.kind === "user") return user(turn.text ?? turn.blocks!.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image", data: b.source.data, mimeType: b.source.media_type })));
		if (turn.kind === "assistant") return { role: "assistant", content: [{ type: "text", text: turn.text }], api: model.api, provider: model.provider, model: model.id, usage, stopReason: "stop", timestamp: 0 };
		if (turn.kind === "call") return assistantCall(model, turn);
		return toolResult(turn.blocks.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image", data: b.source.data, mimeType: b.source.media_type })), turn.isError);
	});

const hello: Turn[] = [{ kind: "user", text: "Say hello." }];
const lookupTurns: Turn[] = [{ kind: "user", text: "Use lookup." }];
const history = (blocks: any[], isError = false, call: { think?: boolean; text?: string } = {}): Turn[] => [{ kind: "user", text: "Use lookup." }, { kind: "call", ...call }, { kind: "result", blocks, isError }];
const text = (value: string) => ({ type: "text", text: value });
const image = { type: "image", source: { type: "base64", media_type: "image/png", data: png } };
const multiTurn: Turn[] = [{ kind: "user", text: "Hi." }, { kind: "assistant", text: "Hello." }, { kind: "user", text: "Again." }];

const signatureOf = (model: any) => JSON.stringify({ compat: model.compat, reasoning: model.reasoning, thinking: model.thinking, input: model.input });

function representatives(models: any[]): any[] {
	const seen = new Map<string, any>();
	for (const model of [...models].sort((a, b) => a.id.localeCompare(b.id))) {
		const key = signatureOf(model);
		if (!seen.has(key)) seen.set(key, model);
	}
	return [...seen.values()].slice(0, maxRepresentatives);
}

// transportEnv is the provider-transport environment set for this capture,
// undefined when none is.
const TRANSPORT_ENV = ["SNOWFLAKE_ACCOUNT", "CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_GATEWAY_ID", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_ACCESS_TOKEN"];
const transportEnv = (): Record<string, string> | undefined => {
	const env = Object.fromEntries(TRANSPORT_ENV.filter(name => process.env[name]).map(name => [name, process.env[name]!]));
	return Object.keys(env).length > 0 ? env : undefined;
};

const apiKeyFor = (provider: string) =>
	provider === "alibaba-token-plan"
		? "sk-sp-fixturekey00000001"
		: provider === "singularityapi-dev"
			? "sk-sapi-fixturekey00000001"
			: `fixture-key-${provider}-00000001`;

for (const provider of providers) {
	const bundled = getBundledModels(provider).map((model: any) => prepareModel(provider, model));
	const discovered = bundled.length === 0 ? await discoveredRows(provider, apiKeyFor(provider)) : { models: [] };
	const listed = discovered.models;
	const completions = [...bundled, ...listed].filter((model: any) => model.api === "openai-completions");
	// Bundled rows NeoProxy serves: Chat Completions, Anthropic Messages and
	// Responses (a mixed-transport host's routed rows), image generation over
	// openai-images (generateOpenAIImage) and speech over openai-speech
	// (synthesizeOpenAiSpeech).
	const exported = bundled.filter((model: any) =>
		["openai-completions", "anthropic-messages", "openai-responses", "openai-images", "openai-speech"].includes(model.api),
	);
	const skipped = bundled.length - exported.length;

	// Catalog export: deduplicated compats so large rosters stay small.
	const dedupe = () => {
		const list: any[] = [];
		const index = new Map<string, number>();
		const indexOf = (compat: any) => {
			const key = JSON.stringify(compat);
			if (!index.has(key)) {
				index.set(key, list.length);
				list.push(compat);
			}
			return index.get(key)!;
		};
		return { list, indexOf };
	};
	const { list: compats, indexOf } = dedupe();
	const { list: anthropicCompats, indexOf: anthropicIndexOf } = dedupe();
	const { list: responsesCompats, indexOf: responsesIndexOf } = dedupe();
	// The provider's Chat Completions endpoint; a provider NeoPi serves only
	// over Anthropic Messages (MiniMax) keeps its bundled rows' endpoint, one
	// whose discovered rows all run on Responses (Ollama) theirs.
	const baseUrl = completions[0]?.baseUrl ?? exported[0]?.baseUrl ?? listed[0]?.baseUrl;
	const rows = exported.map((model: any) => ({
		id: model.id,
		name: model.name,
		api: model.api,
		provider: model.provider,
		baseUrl: model.baseUrl,
		reasoning: model.reasoning,
		input: model.input,
		contextWindow: model.contextWindow ?? 0,
		maxTokens: model.maxTokens ?? 0,
		// The wire id a row's transport sends (Cloudflare's stripped namespace).
		...(model.requestModelId && model.requestModelId !== model.id ? { requestModelId: model.requestModelId } : {}),
		...(model.thinking ? { thinking: model.thinking } : {}),
		...(model.kind ? { kind: model.kind } : {}),
		...(model.headers ? { headers: model.headers } : {}),
		// The authored sparse compat a discovery merge rebuilds from.
		...(model.compatConfig ? { compatConfig: model.compatConfig } : {}),
		...(model.api === "openai-completions" ? { completionsCompatIndex: indexOf(model.compat) } : {}),
		...(model.api === "anthropic-messages" ? { anthropicCompatIndex: anthropicIndexOf(model.compat) } : {}),
		...(model.api === "openai-responses" ? { responsesCompatIndex: responsesIndexOf(model.compat) } : {}),
		// The token tariff: the variant collapse keeps differently priced
		// thinking pairs apart.
		cost: { input: model.cost?.input ?? 0, output: model.cost?.output ?? 0, cacheRead: model.cost?.cacheRead ?? 0, cacheWrite: model.cost?.cacheWrite ?? 0 },
	}));
	// The model manager's authority flags decide how a listing replaces the
	// bundled rows (model-manager.ts mergeDynamicModels).
	const descriptor = PROVIDER_DESCRIPTORS.find((entry: any) => entry.providerId === provider);
	// The fixture key has each provider's credential shape, so a manager that
	// parses its key (Token Plan) still wires its listing.
	const manager = descriptor?.createModelManagerOptions({ apiKey: apiKeyFor(provider) });
	// A manager built without a key that still lists models discovers from a
	// public endpoint (Charm Hyper). Managers that parse their key may throw.
	let keyless: any;
	try {
		keyless = descriptor?.createModelManagerOptions({});
	} catch {
		keyless = undefined;
	}
	const catalog = {
		source: `NeoPi OWN getBundledModels(${JSON.stringify(provider)})`,
		provider,
		baseUrl,
		skippedNonCompletions: skipped,
		// Rule-seeded ids, which take precedence over a manager's references
		// (xiaomiModelManagerOptions).
		seeds: seedModels(provider).map((model: any) => model.id),
		discovery: {
			// Whether the model manager fetches a live listing at all.
			listing: typeof manager?.fetchDynamicModels === "function",
			// Whether that listing also runs without a key (no Authorization).
			unauthenticated: typeof keyless?.fetchDynamicModels === "function",
			// The descriptor's KDL flag only steers bundle generation; the
			// runtime model manager reads its own options.
			modelsAuthoritative: !!manager?.dynamicModelsAuthoritative,
			reasoningAuthoritative: !!manager?.dynamicReasoningAuthoritative,
			inputAuthoritative: !!manager?.dynamicInputAuthoritative,
			costAuthoritative: !!manager?.dynamicCostAuthoritative,
		},
		compats,
		anthropicCompats,
		responsesCompats,
		models: rows,
	};
	await Bun.write(join(catalogDir, `${provider}.json.gz`), Bun.gzipSync(JSON.stringify(catalog)));

	// Request captures.
	const apiKey = apiKeyFor(provider);
	const picked = representatives(completions);
	const scenarios: any[] = [];
	const add = (model: any, name: string, turns: Turn[], options: any, extra: { system?: string[]; tools?: any[] | null } = {}) => {
		const withThinking = true;
		const tools = extra.tools === undefined ? undefined : extra.tools;
		const context: any = {
			...(extra.system ? { systemPrompt: extra.system } : {}),
			messages: neopiTurns(model, turns),
			...(tools ? { tools } : tools === null ? { tools: undefined } : {}),
		};
		const downstream: any = {
			model: model.id,
			messages: [...(extra.system ?? []).map(s => ({ role: "system", content: s })), ...chatTurns(turns, withThinking)],
			stream: true,
		};
		if (tools) downstream.tools = tools.map((t: any) => ({ type: "function", function: { name: t.name, description: t.description, parameters: t.parameters } }));
		if (options.maxTokens) downstream.max_tokens = options.maxTokens;
		if (options.temperature !== undefined) downstream.temperature = options.temperature;
		if (options.topP !== undefined) downstream.top_p = options.topP;
		if (options.topK !== undefined) downstream.top_k = options.topK;
		if (options.minP !== undefined) downstream.min_p = options.minP;
		if (options.presencePenalty !== undefined) downstream.presence_penalty = options.presencePenalty;
		if (options.repetitionPenalty !== undefined) downstream.repetition_penalty = options.repetitionPenalty;
		if (options.disableReasoning) downstream.reasoning_effort = "none";
		else if (options.reasoning) downstream.reasoning_effort = options.reasoning;
		if (options.toolChoice === "any") downstream.tool_choice = "required";
		else if (options.toolChoice === "auto" || options.toolChoice === "none") downstream.tool_choice = options.toolChoice;
		else if (options.toolChoice?.type === "tool") downstream.tool_choice = { type: "function", function: { name: options.toolChoice.name } };
		scenarios.push({ name: `${model.id}/${name}`, model: model.id, context, options, downstream });
	};
	for (const model of picked) {
		const efforts: string[] = model.thinking?.efforts ?? [];
		const first = efforts[0];
		add(model, "plain", hello, {});
		if (model.reasoning && first) {
			add(model, `effort-${first}`, hello, { reasoning: first });
			const last = efforts[efforts.length - 1];
			if (last !== first) add(model, `effort-${last}`, hello, { reasoning: last });
		}
		add(model, "off", hello, { disableReasoning: true });
		add(model, "sampling", hello, { temperature: 0.3, topP: 0.9, topK: 40, minP: 0.05, presencePenalty: 0.1, repetitionPenalty: 1.1, maxTokens: 300 });
		add(model, "system-tool", lookupTurns, { reasoning: first, maxTokens: 512, temperature: 0.2 }, { system: ["A source system."], tools: [lookup] });
		add(model, "two-system", hello, { reasoning: first }, { system: ["First system.", "Second system."] });
		add(model, "rich-tool", lookupTurns, { reasoning: first }, { tools: [richTool, lookup] });
		add(model, "tool-history", history([text("Found.")], false, { text: "Calling." }), { reasoning: first }, { tools: [lookup] });
		add(model, "tool-history-nothink", history([text("Found.")], false, { think: false }), { reasoning: first }, { tools: [lookup] });
		add(model, "tool-history-no-tools", history([text("Found.")]), { reasoning: first }, { tools: null });
		add(model, "tool-result-blocks", history([text("Line one."), text("  "), text("Line two.")]), { reasoning: first }, { tools: [lookup] });
		add(model, "tool-result-empty-error", history([], true), { reasoning: first }, { tools: [lookup] });
		add(model, "tool-result-image", history([text("Shot."), image]), { reasoning: first }, { tools: [lookup] });
		add(model, "forced-any", lookupTurns, { reasoning: first, toolChoice: "any" }, { tools: [lookup] });
		add(model, "forced-named", lookupTurns, { reasoning: first, toolChoice: { type: "tool", name: "lookup" } }, { tools: [lookup] });
		add(model, "auto-choice", lookupTurns, { reasoning: first, toolChoice: "auto" }, { tools: [lookup] });
		add(model, "user-image", [{ kind: "user", blocks: [text("Describe."), image] }], { reasoning: first });
		add(model, "multi-turn", multiTurn, { reasoning: first });
	}

	const fixtures: any[] = [];
	const byId = new Map(completions.map((model: any) => [model.id, model]));
	for (const scenario of scenarios) {
		captured = [];
		const model = byId.get(scenario.model);
		let requestedURL = "";
		const redirectFetch = (async (input: any, init: any) => {
			requestedURL = input instanceof Request ? input.url : String(input);
			const u = new URL(requestedURL);
			const headers = new Headers(init?.headers);
			headers.set("host", u.host);
			return fetch(local + u.pathname + u.search, { ...init, headers, tls: undefined });
		}) as typeof fetch;
		let result: any;
		try {
			result = await streamSimple(model, scenario.context, { apiKey, sessionId, fetch: redirectFetch, ...scenario.options }).result();
		} catch (error) {
			fixtures.push({ name: scenario.name, model: scenario.model, options: scenario.options, downstream: scenario.downstream, error: String(error) });
			continue;
		}
		if (result.stopReason !== "stop") {
			fixtures.push({ name: scenario.name, model: scenario.model, options: scenario.options, downstream: scenario.downstream, error: String(result.errorMessage) });
			continue;
		}
		if (captured.length !== 1) throw new Error(`Capture ${provider}/${scenario.name} saw ${captured.length} requests`);
		fixtures.push({ name: scenario.name, model: scenario.model, options: scenario.options, downstream: scenario.downstream, requestedURL, ...captured[0] });
	}
	// A provider NeoPi serves only over Anthropic Messages has no Chat
	// Completions requests to capture.
	if (fixtures.length > 0) await Bun.write(
		join(outDir, `${provider}.fixtures.json.gz`),
		Bun.gzipSync(
			JSON.stringify({
				source: listed.length > 0 ? `NeoPi OWN streamSimple over resolveProviderModels(${JSON.stringify(provider)})` : `NeoPi OWN streamSimple over getBundledModels(${JSON.stringify(provider)})`,
				provider,
				apiKey,
				sessionId,
				...(discovered.listing ? { listing: discovered.listing } : {}),
				// The environment provider transports read (Snowflake's account,
				// Cloudflare's account and gateway ids).
				...(transportEnv() ? { env: transportEnv() } : {}),
				...(discovered.modelsDev ? { modelsDev: discovered.modelsDev } : {}),
				...(discovered.lmStudioV0 ? { lmStudioV0: discovered.lmStudioV0, lmStudioV1: discovered.lmStudioV1 } : {}),
				fixtures,
			}),
		),
	);
	console.error(`${provider}: ${completions.length} rows, ${compats.length} compats, ${picked.length} models, ${fixtures.length} fixtures, ${fixtures.filter(f => f.error).length} errors`);
}
server.stop(true);
