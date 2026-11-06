// Runs NeoPi's OWN LM Studio discovery, the path its runtime takes: the model
// registry registers `lm-studio` as an implicit discoverable provider
// (coding-agent config/model-registry.ts #addImplicitDiscoverableProviders:
// Chat Completions rows at LM_STUDIO_BASE_URL, else http://127.0.0.1:1234/v1),
// which excludes the catalog's model manager. A server is listed by coding-agent
// config/model-discovery.ts discoverOpenAIModelsList with discovery type
// lm-studio: GET <base>/models beside LM Studio's native /api/v0/models and
// /api/v1/models, the key as a bearer when the account holds a real one. Every
// request is answered by a scripted fetch; nothing reaches a host.
//
// Writes testdata/neopi_lmstudio_runtime.json.gz.
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
type Scenario = { name: string; baseUrl?: string; apiKey?: string; replies: Record<string, Reply> };

const local = "http://127.0.0.1:1234";
const listing = json({
	object: "list",
	data: [
		// Ids NeoPi's bundled reference index knows, and ones it does not.
		{ id: "qwen/qwen3-32b", object: "model", owned_by: "organization_owner" },
		{ id: "openai/gpt-oss-20b", object: "model" },
		{ id: "google/gemma-3-12b", object: "model" },
		{ id: "qwen/qwen3.8-vl-8b", object: "model" },
		{ id: "lm-judge", object: "model" },
		{ id: "text-embedding-nomic-embed-text-v1.5", object: "model" },
		{ id: "zz-unknown-local", object: "model" },
		// Limit and capability spellings a /models row may carry.
		{ id: "zz-limits-flat", context_length: 65536, max_output_tokens: 8192 },
		{ id: "zz-limits-nested", limits: { max_input_tokens: 100000, max_output_tokens: 28000 } },
		{ id: "zz-limits-model-len", max_model_len: "40960", max_tokens: 4096, max_completion_tokens: 2048 },
		{ id: "zz-limits-top", top_provider: { max_completion_tokens: 16384 }, context_length: 0 },
		{ id: "zz-modalities", input_modalities: ["text", "image"], architecture: { input_modalities: ["text"] } },
		{ id: "zz-embed-out", output_modalities: ["embeddings"] },
		{ id: "zz-reasoning", supported_reasoning_levels: [{ effort: "low" }, { effort: "high" }], default_reasoning_level: "low" },
		{ id: "zz-metadata", metadata: { display_name: "  Pretty Name  ", context_window: 32000 } },
		{ id: "" },
		{ object: "model" },
	],
});
const nativeV0 = json({
	data: [
		{ id: "qwen/qwen3.8-vl-8b", type: "vlm", max_context_length: 131072 },
		{ id: "google/gemma-3-12b", type: "llm", state: "loaded", loaded_context_length: 8192, max_context_length: 131072, capabilities: ["tool_use"] },
	],
});
const nativeV1 = json({ models: [{ key: "lm-judge", type: "decision", has_vision_adapter: true, max_context_length: 4096 }] });
const full = { [`${local}/v1/models`]: listing, [`${local}/api/v0/models`]: nativeV0, [`${local}/api/v1/models`]: nativeV1 };
// Native answers' edge cases: a type in any case, vision as a capability
// among junk, a string window, an empty id, junk entries, and a decision
// model's loaded instances.
const edgeV0 = json({
	data: [
		{ id: "qwen/qwen3.8-vl-8b", type: "vlm", max_context_length: 131072 },
		{ id: "google/gemma-4-12b", type: "LLM", state: "loaded", loaded_context_length: 8192, max_context_length: 131072 },
		{ id: "zz-lms-cap", type: "llm", state: "unloaded", loaded_context_length: 4096, capabilities: ["Vision", 5], context_length: "16384" },
		{ id: "", type: "vlm" },
		"junk",
	],
});
const edgeV1 = json({
	models: [
		{ key: "lm-judge", type: "decision", has_vision_adapter: true, max_context_length: 4096, loaded_instances: [{ id: "lm-judge:2", config: { context_length: 2048 } }, "junk"] },
		{ key: "google/gemma-4-12b", type: "llm" },
	],
});
const edgeListing = json({ object: "list", data: [{ id: "qwen/qwen3.8-vl-8b" }, { id: "google/gemma-4-12b" }, { id: "lm-judge" }, { id: "lm-judge:2" }, { id: "zz-lms-cap" }] });
const scenarios: Scenario[] = [
	{ name: "server with native metadata", replies: full },
	{ name: "server with a key", apiKey: "sk-lms", replies: full },
	{ name: "native metadata absent", replies: { [`${local}/v1/models`]: listing } },
	{ name: "native metadata unreadable", replies: { [`${local}/v1/models`]: listing, [`${local}/api/v0/models`]: { status: 200, body: "oops" }, [`${local}/api/v1/models`]: json({ error: "x" }, 500) } },
	{ name: "native metadata unreachable", replies: { [`${local}/v1/models`]: listing, [`${local}/api/v0/models`]: "unreachable", [`${local}/api/v1/models`]: "unreachable" } },
	{ name: "native metadata edge cases", replies: { [`${local}/v1/models`]: edgeListing, [`${local}/api/v0/models`]: edgeV0, [`${local}/api/v1/models`]: edgeV1 } },
	{ name: "native v1 only", replies: { [`${local}/v1/models`]: edgeListing, [`${local}/api/v0/models`]: json({ error: "no" }, 404), [`${local}/api/v1/models`]: edgeV1 } },
	{ name: "native v0 only", replies: { [`${local}/v1/models`]: edgeListing, [`${local}/api/v0/models`]: edgeV0, [`${local}/api/v1/models`]: { status: 200, body: "oops" } } },
	{ name: "native junk", replies: { [`${local}/v1/models`]: edgeListing, [`${local}/api/v0/models`]: json({ data: "x" }), [`${local}/api/v1/models`]: json({ models: {} }) } },
	{ name: "listing data not a list", replies: { [`${local}/v1/models`]: json({ data: { id: "x" } }) } },
	{ name: "listing data a string", replies: { [`${local}/v1/models`]: json({ data: "abc" }) } },
	{ name: "listing null entry", replies: { [`${local}/v1/models`]: json({ data: [{ id: "zz-unknown-local" }, null] }) } },
	{ name: "listing non-string id", replies: { [`${local}/v1/models`]: json({ data: [{ id: 7 }] }) } },
	{ name: "listing valid then non-string id", replies: { [`${local}/v1/models`]: json({ data: [{ id: "zz-unknown-local" }, { id: 0 }, { id: true }] }) } },
	{ name: "listing null", replies: { [`${local}/v1/models`]: json(null) } },
	{ name: "listing down", replies: { [`${local}/v1/models`]: json({ error: "x" }, 503), [`${local}/api/v0/models`]: nativeV0 } },
	{ name: "listing unauthorized", apiKey: "sk-wrong", replies: { [`${local}/v1/models`]: json({ error: "no" }, 401) } },
	{ name: "listing unreachable", replies: { [`${local}/v1/models`]: "unreachable" } },
	{ name: "listing not json", replies: { [`${local}/v1/models`]: { status: 200, body: "oops" } } },
	{ name: "listing without data", replies: { [`${local}/v1/models`]: json({ object: "list" }) } },
	{ name: "listing a bare list", replies: { [`${local}/v1/models`]: json([{ id: "qwen/qwen3-32b" }]) } },
	{ name: "empty listing", replies: { [`${local}/v1/models`]: json({ data: [] }) } },
	{ name: "configured base without v1", baseUrl: "http://gpu.lan:1234", replies: { "http://gpu.lan:1234/v1/models": json({ data: [{ id: "qwen/qwen3-32b" }] }) } },
	{ name: "configured base with slashes", baseUrl: "http://gpu.lan:1234/v1//", replies: { "http://gpu.lan:1234/v1/models": json({ data: [{ id: "qwen/qwen3-32b" }] }) } },
	{ name: "configured subpath base", baseUrl: "https://proxy.example/lms/", replies: { "https://proxy.example/lms/v1/models": json({ data: [{ id: "zz-unknown-local" }] }) } },
	{ name: "unparsable base", baseUrl: "not a url", replies: {} },
];

const pick = (model: any) => ({
	id: model.id,
	name: model.name,
	api: model.api,
	...(model.kind ? { kind: model.kind } : {}),
	baseUrl: model.baseUrl,
	reasoning: model.reasoning,
	input: model.input,
	contextWindow: model.contextWindow ?? null,
	maxTokens: model.maxTokens ?? null,
	...(model.thinking ? { thinking: model.thinking } : {}),
	...(model.supportsTools === false ? { supportsTools: false } : {}),
	compat: model.compat ?? null,
});

const results: any[] = [];
for (const scenario of scenarios) {
	const requests: any[] = [];
	const fetch = async (input: any, init: any) => {
		const url = new URL(typeof input === "string" ? input : input.url);
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
		// The implicit provider: Chat Completions rows, no configured headers.
		models = await discovery.discoverModelsByProviderType(
			{ provider: "lm-studio", api: "openai-completions", baseUrl: scenario.baseUrl ?? `${local}/v1`, discovery: { type: "lm-studio" } },
			{ fetch, getBearerApiKeyResolver: async () => scenario.apiKey },
		);
	} catch (e: any) {
		error = `${e?.name ?? "Error"}: ${e?.message ?? e}`;
	}
	results.push({
		scenario: scenario.name,
		baseUrl: scenario.baseUrl ?? `${local}/v1`,
		...(scenario.apiKey ? { apiKey: scenario.apiKey } : {}),
		replies: scenario.replies,
		requests,
		...(error !== undefined ? { error } : {}),
		models: models ? models.map(pick) : null,
	});
}

const out = join(import.meta.dir, "neopi_lmstudio_runtime.json.gz");
writeFileSync(out, Bun.gzipSync(JSON.stringify({
	source: "NeoPi OWN coding-agent config/model-discovery.ts discoverModelsByProviderType({discovery: {type: \"lm-studio\"}})",
	results,
})));
console.log(`wrote ${results.length} scenarios to ${out}`);
