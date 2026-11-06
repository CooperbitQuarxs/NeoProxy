// Runs NeoPi's OWN llama.cpp discovery, the path its runtime takes: the model
// registry registers `llama.cpp` as an implicit discoverable provider
// (coding-agent config/model-registry.ts #addImplicitDiscoverableProviders:
// Responses rows at LLAMA_CPP_BASE_URL, else http://127.0.0.1:8080 with
// http://127.0.0.1:9931 as a fallback), listed by coding-agent
// config/model-discovery.ts discoverLlamaCppModels: GET <base>/models beside
// GET <native base>/props, the key as a bearer when the account holds a real
// one. Every request is answered by a scripted fetch; nothing reaches a host.
//
// Writes testdata/neopi_llamacpp_discovery.json.gz: per scenario the requests
// NeoPi sent and the rows it built (with resolved compat).
//
//   cd <neopi-nightly> && HOME=$(mktemp -d) NEOPI_CHECKOUT=$PWD bun <this file>
import { writeFileSync } from "node:fs";
import { join } from "node:path";

const checkout = process.env.NEOPI_CHECKOUT ?? "/home/shayna/source/github/PsychedelicShayna/neopi-nightly";
const discovery = await import(join(checkout, "packages/coding-agent/src/config/model-discovery.ts"));

type Reply = { status: number; body: string } | "unreachable";
const json = (value: unknown, status = 200): Reply => ({ status, body: JSON.stringify(value) });
// replies answers by origin + path; an absent path answers 404, and an
// "unreachable" reply throws as a refused connection does.
type Scenario = { name: string; baseUrl?: string; fallbackBaseUrls?: string[]; apiKey?: string; replies: Record<string, Reply> };

const local = "http://127.0.0.1:8080";
const qwen = { id: "Qwen3-8B-Q4_K_M.gguf", object: "model", owned_by: "llamacpp", meta: { n_ctx: 32768, n_ctx_train: 262144, vocab_type: 2 } };
const qwen38 = { id: "Qwen3.8-14B-Instruct-Q4_K_M", object: "model", meta: { n_ctx_train: 131072 } };
const llama = { id: "Llama-3.2-3B-Instruct-Q8_0", object: "model", architecture: { input_modalities: ["Text", "IMAGE"] } };
const routed = [
	{ id: "router-args", status: { value: "unloaded", args: ["llama-server", "--port", "9100", "--ctx-size", "16384"] } },
	{ id: "router-eq", status: { value: "unloaded", args: ["--ctx-size=12288"] } },
	{ id: "router-short", status: { args: ["-c", "6144"] } },
	{ id: "router-ini", status: { value: "unloaded", preset: "model = /m.gguf\nctx-size = 8192\nn-gpu-layers = 99" } },
	{ id: "router-bad", status: { args: ["--ctx-size", "auto"], preset: "ctx-size = -1" }, meta: { n_ctx_train: 4096 } },
	{ id: "", status: {} },
	{ id: 7 },
	"text",
];
const props = (extra: unknown = {}) => json({ default_generation_settings: { n_ctx: 20480, params: { n_predict: -1 } }, modalities: { vision: false }, ...(extra as object) });
const scenarios: Scenario[] = [
	{ name: "single server", replies: { [`${local}/models`]: json({ object: "list", data: [qwen, llama] }), [`${local}/props`]: props() } },
	{ name: "single server with a key", apiKey: "sk-llama", replies: { [`${local}/models`]: json({ data: [qwen] }), [`${local}/props`]: props() } },
	{ name: "router mode", replies: { [`${local}/models`]: json({ data: [...routed, qwen38] }), [`${local}/props`]: json({ default_generation_settings: { n_ctx: 0 }, n_ctx: 0 }) } },
	{ name: "props bounded generation", replies: { [`${local}/models`]: json({ data: [llama, { id: "plain" }] }), [`${local}/props`]: json({ default_generation_settings: { n_ctx: 65536, params: { n_predict: 512, max_tokens: 4096 } }, modalities: { vision: true } }) } },
	{ name: "props sentinel spellings", replies: { [`${local}/models`]: json({ data: [{ id: "plain" }] }), [`${local}/props`]: json({ n_ctx: "40000", max_tokens: " -1 " }) } },
	{ name: "props down", replies: { [`${local}/models`]: json({ data: [qwen, { id: "plain" }] }), [`${local}/props`]: json({ error: "x" }, 500) } },
	{ name: "props not json", replies: { [`${local}/models`]: json({ data: [{ id: "plain" }] }), [`${local}/props`]: { status: 200, body: "oops" } } },
	{ name: "props unreachable", replies: { [`${local}/models`]: json({ data: [{ id: "plain" }] }), [`${local}/props`]: "unreachable" } },
	{ name: "models down", replies: { [`${local}/models`]: json({ error: "x" }, 500), [`${local}/props`]: props() } },
	{ name: "models unauthorized", apiKey: "sk-wrong", replies: { [`${local}/models`]: json({ error: "no" }, 401), [`${local}/props`]: props() } },
	{ name: "models not json", replies: { [`${local}/models`]: { status: 200, body: "oops" }, [`${local}/props`]: props() } },
	{ name: "models not a list", replies: { [`${local}/models`]: json({ data: { id: "x" } }), [`${local}/props`]: props() } },
	{ name: "models a bare list", replies: { [`${local}/models`]: json([qwen]), [`${local}/props`]: props() } },
	{ name: "empty server", replies: { [`${local}/models`]: json({ data: [] }), [`${local}/props`]: props() } },
	{ name: "primary unreachable, fallback answers", fallbackBaseUrls: ["http://127.0.0.1:9931"], replies: { [`${local}/models`]: "unreachable", [`${local}/props`]: "unreachable", "http://127.0.0.1:9931/models": json({ data: [qwen] }), "http://127.0.0.1:9931/props": props() } },
	{ name: "primary refuses, no fallback", fallbackBaseUrls: ["http://127.0.0.1:9931"], replies: { [`${local}/models`]: json({ error: "x" }, 503), "http://127.0.0.1:9931/models": json({ data: [qwen] }) } },
	{ name: "every server unreachable", fallbackBaseUrls: ["http://127.0.0.1:9931"], replies: { [`${local}/models`]: "unreachable", "http://127.0.0.1:9931/models": "unreachable" } },
	{ name: "fallback refuses", fallbackBaseUrls: ["http://127.0.0.1:9931"], replies: { [`${local}/models`]: "unreachable", "http://127.0.0.1:9931/models": json({ error: "no" }, 401) } },
	{ name: "configured v1 base", baseUrl: "http://gpu.lan:8080/v1/", replies: { "http://gpu.lan:8080/v1/models": json({ data: [qwen] }), "http://gpu.lan:8080/props": props() } },
	{ name: "configured subpath base", baseUrl: "https://proxy.example/llama//", replies: { "https://proxy.example/llama/models": json({ data: [{ id: "plain" }] }), "https://proxy.example/llama/props": props() } },
	{ name: "configured base with default port", baseUrl: "http://GPU.lan:80", replies: { "http://gpu.lan/models": json({ data: [{ id: "plain" }] }), "http://gpu.lan/props": props() } },
	{ name: "unparsable base", baseUrl: "not a url", replies: {} },
];

const pick = (model: any) => ({
	id: model.id,
	name: model.name,
	api: model.api,
	baseUrl: model.baseUrl,
	reasoning: model.reasoning,
	input: model.input,
	contextWindow: model.contextWindow ?? null,
	maxTokens: model.maxTokens ?? null,
	...(model.thinking ? { thinking: model.thinking } : {}),
	compat: model.compat ?? null,
});

const results: any[] = [];
for (const scenario of scenarios) {
	const requests: any[] = [];
	const fetch = async (input: any, init: any) => {
		const href = typeof input === "string" ? input : input.url;
		const url = new URL(href);
		requests.push({
			method: init?.method ?? "GET",
			url: url.href,
			headers: Object.fromEntries(Object.entries(init?.headers ?? {}).map(([k, v]) => [k.toLowerCase(), v])),
		});
		const reply = scenario.replies[url.origin + url.pathname] ?? { status: 404, body: "not found" };
		if (reply === "unreachable") throw new TypeError("Unable to connect. Is the computer able to access the url?");
		return new Response(reply.body, { status: reply.status, headers: { "content-type": "application/json" } });
	};
	let models: any[] | null = null;
	let error: string | undefined;
	try {
		models = await discovery.discoverModelsByProviderType(
			{ provider: "llama.cpp", api: "openai-responses", baseUrl: scenario.baseUrl ?? local, discovery: { type: "llama.cpp" }, ...(scenario.fallbackBaseUrls ? { fallbackBaseUrls: scenario.fallbackBaseUrls } : {}) },
			{ fetch, getBearerApiKeyResolver: async () => scenario.apiKey },
		);
	} catch (e: any) {
		error = `${e?.name ?? "Error"}: ${e?.message ?? e}`;
	}
	results.push({
		scenario: scenario.name,
		baseUrl: scenario.baseUrl ?? local,
		...(scenario.fallbackBaseUrls ? { fallbackBaseUrls: scenario.fallbackBaseUrls } : {}),
		...(scenario.apiKey ? { apiKey: scenario.apiKey } : {}),
		replies: scenario.replies,
		requests,
		...(error !== undefined ? { error } : {}),
		models: models ? models.map(pick) : null,
	});
}

const out = join(import.meta.dir, "neopi_llamacpp_discovery.json.gz");
writeFileSync(out, Bun.gzipSync(JSON.stringify({
	source: "NeoPi OWN coding-agent config/model-discovery.ts discoverModelsByProviderType({discovery: {type: \"llama.cpp\"}})",
	defaultBaseUrls: discovery.LLAMA_CPP_DEFAULT_BASE_URLS,
	maxTokensDefault: discovery.DISCOVERY_DEFAULT_MAX_TOKENS,
	results,
})));
console.log(`wrote ${results.length} scenarios to ${out}`);
