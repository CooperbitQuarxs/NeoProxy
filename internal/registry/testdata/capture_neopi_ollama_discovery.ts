// Runs NeoPi's OWN local Ollama discovery, the path its runtime takes: the
// model registry registers `ollama` as an implicit discoverable provider
// (coding-agent config/model-registry.ts #addImplicitDiscoverableProviders),
// which excludes the pi-catalog model manager, so a local server is listed by
// coding-agent config/model-discovery.ts discoverOllamaModels. Every request
// is answered by a scripted fetch; nothing reaches a host.
//
// Writes testdata/neopi_ollama_discovery.json.gz: per scenario the requests
// NeoPi sent and the rows it built (with resolved compat), plus NeoPi's
// environment helpers (getImplicitOllamaBaseUrl, getOllamaContextLengthOverride).
//
//   HOME=$(mktemp -d) NEOPI_CHECKOUT=<neopi-nightly> bun capture_neopi_ollama_discovery.ts
//
// NEOPI_CHECKOUT is a NeoPi nightly checkout with dependencies installed; its
// coding-agent config, catalog and ai sources must match the pinned nightly.
import { writeFileSync } from "node:fs";
import { join } from "node:path";

const checkout = process.env.NEOPI_CHECKOUT ?? "/home/shayna/source/github/PsychedelicShayna/neopi-nightly";
const discovery = await import(join(checkout, "packages/coding-agent/src/config/model-discovery.ts"));

type Reply = { status: number; body: string };
const json = (value: unknown, status = 200): Reply => ({ status, body: JSON.stringify(value) });
// tags answers GET /api/tags; shows answers POST /api/show by the model its
// body names (absent: 404).
type Scenario = { name: string; baseUrl?: string; tags: Reply; shows?: Record<string, Reply>; contextLength?: string };

const showQwen = json({ capabilities: ["completion", "tools", "Thinking"], parameters: "stop \"<|im_end|>\"\nnum_ctx 8192", model_info: { "general.architecture": "qwen3", "qwen3.context_length": 40960 } });
const shows: Record<string, Reply> = {
	"qwen3:8b": showQwen,
	"a:1": json({ capabilities: { vision: true, thinking: true }, model_info: { "a.context_length": 131072 } }),
	"c:1": json({ capabilities: ["completion", "IMAGE"], model_info: { context_length: "65536" } }),
	"d:1": json({ error: "boom" }, 500),
	"e:1": json({ model_info: { "llama.num_ctx": 4096, "llama.context_length": 0, "llama.context_window": 9000 } }),
	"f:1": json(null),
	"g:1": json(5),
	"h:1": { status: 200, body: "oops" },
	"i:1": json({ capabilities: "thinking", parameters: "num_ctx 0\n", model_info: { "x.context_length": 2048.5 } }),
	"j:1": json({ capabilities: [], parameters: "num_ctx    16384  \nrepeat_penalty 1", model_info: { "x.context_length": 4096 } }),
	"k:1": json({ capabilities: ["vision", 7], parameters: "num_ctx abc", model_info: { "x.context_length": 70000 } }),
};
const roster = json({
	models: [
		{ name: "qwen3:8b", model: "qwen3:8b", size: 5200000000 },
		{ name: "Alpha Pretty", model: "a:1" },
		{ name: "c:1" },
		{ model: "d:1" },
		{ name: "qwen3:8b", model: "qwen3:8b" },
		{},
		{ model: "", name: "e:1" },
		{ name: "", model: "f:1" },
		{ name: null, model: "g:1" },
		{ model: null, name: "h:1" },
		{ model: "i:1", name: "i:1" },
		{ model: "j:1" },
		{ model: "k:1" },
		5,
		"text",
	],
});
const scenarios: Scenario[] = [
	{ name: "roster with show metadata", tags: roster, shows },
	{ name: "every show fails", tags: roster },
	{ name: "empty roster", tags: json({ models: [] }) },
	{ name: "roster without models", tags: json({ tags: [] }) },
	{ name: "models null", tags: json({ models: null }) },
	{ name: "tags down", tags: json({ error: "x" }, 503) },
	{ name: "tags unauthorized", tags: json({ error: "no" }, 401) },
	{ name: "tags not json", tags: { status: 200, body: "oops" } },
	{ name: "tags null", tags: json(null) },
	{ name: "tags a list", tags: json([{ model: "x:1" }]) },
	{ name: "models not a list", tags: json({ models: { name: "x:1" } }) },
	{ name: "null entry", tags: json({ models: [{ model: "x:1" }, null] }), shows },
	{ name: "non-string ids", tags: json({ models: [{ model: 7 }, { model: true, name: "t" }, { name: 5 }, { model: "qwen3:8b" }] }), shows },
	{ name: "non-string name", tags: json({ models: [{ model: "qwen3:8b", name: 17 }, { model: "a:1", name: false }] }), shows },
	{ name: "context length override", tags: json({ models: [{ model: "qwen3:8b" }, { model: "j:1" }] }), shows, contextLength: "50000" },
	{ name: "small context length override", tags: json({ models: [{ model: "qwen3:8b" }] }), shows, contextLength: " 4096 " },
	{ name: "unusable context length override", tags: json({ models: [{ model: "qwen3:8b" }] }), shows, contextLength: "1.5" },
	{ name: "configured base with path", baseUrl: "http://box.lan:11434/some/path/v1", tags: json({ models: [{ model: "qwen3:8b" }] }), shows },
	{ name: "configured https base", baseUrl: "https://ollama.example", tags: json({ models: [{ model: "a:1" }] }), shows },
	{ name: "configured base with default port", baseUrl: "http://BOX.lan:80/", tags: json({ models: [{ model: "a:1" }] }), shows },
	{ name: "configured ipv6 base", baseUrl: "http://[::1]:11500/v1", tags: json({ models: [{ model: "a:1" }] }), shows },
	{ name: "unparsable base", baseUrl: "not a url", tags: json({ models: [{ model: "a:1" }] }), shows },
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
		const url = new URL(typeof input === "string" ? input : input.url);
		const body = typeof init?.body === "string" ? init.body : undefined;
		requests.push({
			method: init?.method ?? "GET",
			url: url.href,
			headers: Object.fromEntries(Object.entries(init?.headers ?? {}).map(([k, v]) => [k.toLowerCase(), v])),
			...(body !== undefined ? { body } : {}),
		});
		let reply: Reply = { status: 404, body: "not found" };
		if (url.pathname.endsWith("/api/tags")) reply = scenario.tags;
		else if (url.pathname.endsWith("/api/show")) reply = scenario.shows?.[String(JSON.parse(body ?? "{}").model)] ?? reply;
		return new Response(reply.body, { status: reply.status, headers: { "content-type": "application/json" } });
	};
	let models: any[] | null = null;
	let error: string | undefined;
	try {
		// The implicit provider: Responses rows, no configured headers.
		models = await discovery.discoverModelsByProviderType(
			{ provider: "ollama", api: "openai-responses", baseUrl: scenario.baseUrl, discovery: { type: "ollama" } },
			{ fetch },
		);
	} catch (e: any) {
		error = String(e?.message ?? e);
	}
	if (models && scenario.contextLength !== undefined) {
		// model-registry.ts #normalizeDiscoverableModels applies the override
		// to every ollama Responses row.
		Bun.env.OLLAMA_CONTEXT_LENGTH = scenario.contextLength;
		const override = discovery.getOllamaContextLengthOverride();
		delete Bun.env.OLLAMA_CONTEXT_LENGTH;
		if (override !== undefined) {
			models = models.map((model: any) => ({ ...model, contextWindow: override, maxTokens: Math.min(override, discovery.DISCOVERY_DEFAULT_MAX_TOKENS) }));
		}
	}
	results.push({
		scenario: scenario.name,
		...(scenario.baseUrl !== undefined ? { baseUrl: scenario.baseUrl } : {}),
		...(scenario.contextLength !== undefined ? { contextLength: scenario.contextLength } : {}),
		tags: scenario.tags,
		...(scenario.shows ? { shows: scenario.shows } : {}),
		requests,
		...(error !== undefined ? { error } : {}),
		models: models ? models.map(pick) : null,
	});
}

// NeoPi's environment helpers, for an account that configures no address.
const implicitBase: any[] = [];
for (const env of [
	{},
	{ OLLAMA_BASE_URL: "  http://gpu.lan:11434/x  " },
	{ OLLAMA_BASE_URL: "http://a:1", OLLAMA_HOST: "b:2" },
	{ OLLAMA_HOST: "0.0.0.0" },
	{ OLLAMA_HOST: ":11500" },
	{ OLLAMA_HOST: "//gpu.lan" },
	{ OLLAMA_HOST: "https://gpu.lan" },
	{ OLLAMA_HOST: "gpu.lan:9999" },
	{ OLLAMA_HOST: "ftp://gpu.lan" },
	{ OLLAMA_HOST: "   " },
	{ OLLAMA_BASE_URL: "   ", OLLAMA_HOST: "GPU.lan" },
]) {
	for (const name of ["OLLAMA_BASE_URL", "OLLAMA_HOST"]) delete Bun.env[name];
	Object.assign(Bun.env, env);
	implicitBase.push({ env, base: discovery.getImplicitOllamaBaseUrl() });
}
for (const name of ["OLLAMA_BASE_URL", "OLLAMA_HOST"]) delete Bun.env[name];
const contextOverride: any[] = [];
for (const value of ["", "   ", "8192", " 4096 ", "0", "-5", "1.5", "abc", "1e3", "0x10", "9007199254740993"]) {
	Bun.env.OLLAMA_CONTEXT_LENGTH = value;
	contextOverride.push({ value, override: discovery.getOllamaContextLengthOverride() ?? null });
}
delete Bun.env.OLLAMA_CONTEXT_LENGTH;

const out = join(import.meta.dir, "neopi_ollama_discovery.json.gz");
writeFileSync(out, Bun.gzipSync(JSON.stringify({
	source: "NeoPi OWN coding-agent config/model-discovery.ts discoverModelsByProviderType({discovery: {type: \"ollama\"}})",
	maxTokensDefault: discovery.DISCOVERY_DEFAULT_MAX_TOKENS,
	results,
	implicitBase,
	contextOverride,
})));
console.log(`wrote ${results.length} scenarios to ${out}`);
