// Runs NeoPi's OWN Ollama Cloud model manager (resolveProviderModels over
// ollamaCloudModelManagerOptions) against a scripted loopback Ollama: every
// /api/tags and /api/show request it sends and the roster it resolves are
// recorded. Writes ollama_cloud_discovery.json.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_ollama_cloud_discovery.ts
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
const { ollamaCloudModelManagerOptions } = await import(root + "/pi-catalog/src/provider-models/ollama.ts");

type Reply = { status: number; body: unknown };
let tags: Reply = { status: 200, body: { models: [] } };
let shows: Record<string, Reply> = {};
let received: any[] = [];
const server = Bun.serve({
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const headers: Record<string, string> = {};
		request.headers.forEach((value, key) => {
			headers[key] = value;
		});
		const body = await request.text();
		received.push({ method: request.method, path: url.pathname, headers, body });
		let reply: Reply | undefined;
		if (url.pathname === "/api/tags") reply = tags;
		if (url.pathname === "/api/show") reply = shows[JSON.parse(body).model];
		if (!reply) return new Response("not found", { status: 404 });
		return new Response(typeof reply.body === "string" ? reply.body : JSON.stringify(reply.body), { status: reply.status, headers: { "content-type": "application/json" } });
	},
});
const nativeFetch = globalThis.fetch;
const loopbackFetch = (async (input: any, init?: any) => {
	const url = new URL(input instanceof Request ? input.url : String(input));
	if (url.origin !== "https://ollama.com") throw new Error("Unexpected Ollama URL: " + url.href);
	return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`, init);
}) as typeof fetch;

const info = (context: number) => ({ "general.architecture": "x", "x.context_length": context });
const scenarios: { name: string; tags: Reply; shows: Record<string, Reply>; apiKey?: string; baseUrl?: string }[] = [
	{
		name: "mixed-listing",
		tags: {
			status: 200,
			body: {
				models: [
					{ name: "gpt-oss:120b", model: "gpt-oss:120b" },
					{ name: "Shiny New", model: "shiny-new:7b" },
					{ name: "deepseek-v4-pro:next" },
					{ model: "qwen3-coder:480b" },
					{ model: "", name: "blank-model" },
					{ name: "glm-4.6" },
					{ model: "kimi-k2" },
					{ model: "kimi-k2-thinking" },
					{ model: "llama3.3:70b" },
					{ model: "gpt-oss:120b" },
					{ model: "mistral-large-3:675b" },
					{ model: "gemma3:4b" },
				],
			},
		},
		shows: {
			"gpt-oss:120b": { status: 200, body: { capabilities: ["completion", "tools", "thinking"], model_info: info(131072) } },
			"shiny-new:7b": { status: 200, body: { capabilities: ["completion", "vision"], model_info: { "a.num_ctx": "x", "b.num_ctx": 32768, "c.context_length": 4096 } } },
			"deepseek-v4-pro:next": { status: 200, body: { capabilities: ["completion", "thinking"], model_info: info(1048576) } },
			"qwen3-coder:480b": { status: 500, body: { error: "boom" } },
			"glm-4.6": { status: 200, body: { model_info: info(200000) } },
			"kimi-k2": { status: 200, body: { capabilities: [], model_info: info(262144) } },
			"kimi-k2-thinking": { status: 200, body: { capabilities: ["thinking"], model_info: info(262144) } },
			"llama3.3:70b": { status: 200, body: "not json" },
			"mistral-large-3:675b": { status: 200, body: { capabilities: ["vision"] } },
			"gemma3:4b": { status: 200, body: { capabilities: ["completion"], model_info: info(131072) } },
		},
	},
	{
		name: "configured-api-base",
		baseUrl: "https://ollama.com/api/",
		tags: { status: 200, body: { models: [{ model: "glm-5.2" }] } },
		shows: { "glm-5.2": { status: 200, body: { capabilities: ["thinking"], model_info: info(202752) } } },
	},
	{
		name: "empty-listing",
		tags: { status: 200, body: { models: [] } },
		shows: {},
	},
	{
		name: "no-models-key",
		tags: { status: 200, body: {} },
		shows: {},
	},
	{
		name: "tags-refused",
		tags: { status: 401, body: { error: "unauthorized" } },
		shows: {},
	},
];

const summarize = (model: any) => ({
	id: model.id,
	name: model.name,
	api: model.api,
	provider: model.provider,
	baseUrl: model.baseUrl,
	reasoning: model.reasoning,
	input: model.input,
	contextWindow: model.contextWindow ?? 0,
	maxTokens: model.maxTokens ?? 0,
	thinking: model.thinking ?? null,
	compat: model.compat,
	omitMaxOutputTokens: model.omitMaxOutputTokens === true,
	requestModelId: model.requestModelId ?? null,
});

const cacheDir = mkdtempSync(join(tmpdir(), "ollama-cloud-discovery-"));
const out: any[] = [];
for (const scenario of scenarios) {
	tags = scenario.tags;
	shows = scenario.shows;
	received = [];
	const options = ollamaCloudModelManagerOptions({ apiKey: scenario.apiKey ?? "ollama-discovery-key", fetch: loopbackFetch, ...(scenario.baseUrl ? { baseUrl: scenario.baseUrl } : {}) });
	(options as any).cacheDbPath = join(cacheDir, `${out.length}.db`);
	const resolved = await resolveProviderModels(options, "online");
	out.push({
		name: scenario.name,
		apiKey: scenario.apiKey ?? "ollama-discovery-key",
		...(scenario.baseUrl ? { baseUrl: scenario.baseUrl } : {}),
		tags: scenario.tags,
		shows: scenario.shows,
		requests: received,
		source: resolved.source,
		stale: resolved.stale,
		models: resolved.models.map(summarize),
	});
}
server.stop(true);
await Bun.write(join(import.meta.dir, "ollama_cloud_discovery.json"), JSON.stringify({ source: "NeoPi OWN resolveProviderModels(ollamaCloudModelManagerOptions) against a scripted Ollama", scenarios: out }, null, "\t") + "\n");
console.log(`${out.length} scenarios`);
