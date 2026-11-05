// Runs NeoPi's OWN GitHub Copilot model manager (resolveProviderModels over
// githubCopilotModelManagerOptions) against scripted answers for the plan
// endpoint probe (api.github.com/copilot_internal/user) and GET /models, and
// records the requests discovery sends beside the roster NeoPi resolves.
// Nothing leaves the machine: the fetch override answers every URL itself.
//
// Writes github_copilot_discovery.json.gz beside this script. Run from the
// NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file>
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const outFile = process.env.OUT_FILE ?? join(dirname(new URL(import.meta.url).pathname), "github_copilot_discovery.json.gz");

const { getBundledModels, getBundledProviders } = await import(root + "/pi-catalog/src/models.ts");
const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
const { githubCopilotModelManagerOptions } = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");
const { createReferenceResolver } = await import(root + "/pi-catalog/src/provider-models/bundled-references.ts");

const bundled: any[] = getBundledModels("github-copilot");
const byApi = (api: string) => bundled.filter(model => model.api === api).map(model => model.id);
const messages = byApi("anthropic-messages");
const completions = byApi("openai-completions");
const responses = byApi("openai-responses");
if (messages.length < 2 || completions.length < 1 || responses.length < 2) throw new Error("bundle lacks rows on every API");

// Ids Copilot's bundle lacks that NeoPi's global reference fallback answers:
// one per Copilot route, and one whose reference carries wire routing
// (requestModelId or an effort-routed ladder) that must not transfer.
const globalReference = createReferenceResolver(new Map());
const copilotIds = new Set(bundled.map(model => model.id));
const crossIds: string[] = [];
for (const provider of getBundledProviders()) for (const model of getBundledModels(provider) as any[]) {
	if (!copilotIds.has(model.id) && globalReference(model.id) && !crossIds.includes(model.id)) crossIds.push(model.id);
}
crossIds.sort();
const pickCross = (test: (id: string, ref: any) => boolean) => {
	const id = crossIds.find(candidate => test(candidate, globalReference(candidate)));
	if (!id) throw new Error("no cross-provider reference for a capture case");
	return id;
};
const crossMessages = pickCross(id => /^claude-(opus|sonnet)-\d/.test(id));
const crossResponses = pickCross(id => /^gpt-5/.test(id));
const crossCompletions = pickCross(id => /^gemini-/.test(id));
const crossRouted = pickCross((_id, ref) => Boolean(ref.requestModelId) || Boolean(ref.thinking?.effortRouting));
console.log("cross-provider ids", { crossMessages, crossResponses, crossCompletions, crossRouted });

type Reply = { status: number; body: string };
const json = (value: unknown, status = 200): Reply => ({ status, body: JSON.stringify(value) });

// One /models entry with Copilot's capability, limit and billing shape.
const entry = (id: string, extra: Record<string, unknown> = {}) => ({
	id,
	name: `Live ${id}`,
	object: "model",
	capabilities: {
		type: "chat",
		limits: { max_context_window_tokens: 400000, max_prompt_tokens: 272000, max_output_tokens: 64000 },
		supports: { vision: true, tool_calls: true },
	},
	...extra,
});
const tiered = (id: string, vision?: boolean) =>
	entry(id, {
		capabilities: {
			type: "chat",
			limits: { max_context_window_tokens: 1000000, max_prompt_tokens: 200000, max_output_tokens: 32000 },
			...(vision === undefined ? {} : { supports: { vision } }),
		},
		billing: {
			token_prices: {
				default: { max_prompt_tokens: 168000, input_price: 500, output_price: 2500, cache_price: 50 },
				long_context: { max_prompt_tokens: 968000, input_price: 1000, output_price: 3750, cache_price: 100 },
			},
		},
	});

// A roster touching every mapper branch: bundled ids on each API with live
// limits, tiers and vision flags; unbundled Claude ids on both sides of the
// thinking cut; an unbundled id on the default route; ids answered by the
// global reference fallback; an embedding model; a blank name; a duplicate
// id; and limit fallbacks.
const fullRoster = [
	tiered(messages[0]),
	tiered(responses[0], false),
	entry(messages[1], { name: "  ", capabilities: { type: "chat", limits: { max_prompt_tokens: 90000, max_non_streaming_output_tokens: 8000 } } }),
	entry(completions[0], { capabilities: { type: "chat", supports: { vision: false } }, context_length: 120000, max_completion_tokens: 16000 }),
	entry(responses[1], { capabilities: { type: "chat" } }),
	entry("claude-opus-9", { capabilities: { type: "chat", limits: { max_context_window_tokens: 300000, max_output_tokens: 64000 } } }),
	entry("claude-3.5-sonnet-legacy"),
	entry("mystery-model-7", { capabilities: { type: "chat", supports: { vision: true } } }),
	entry("text-embedding-3-small", { capabilities: { type: "embeddings", limits: { max_prompt_tokens: 8191 } } }),
	{ id: "no-capabilities-model", name: "Bare" },
	entry("gpt-6-nova", { capabilities: { type: "chat", limits: { max_context_window_tokens: 500000, max_output_tokens: 100000 }, supports: { vision: true } } }),
	entry("GROK-4.9-fast"),
	entry(crossMessages, { capabilities: { type: "chat", limits: { max_context_window_tokens: 210000, max_output_tokens: 48000 } } }),
	tiered(crossResponses, true),
	entry(crossCompletions),
	entry(crossRouted, { capabilities: { type: "chat", supports: { vision: false } } }),
	entry("mystery-model-7", { name: "Mystery Again", capabilities: { type: "chat", limits: { max_output_tokens: 7000 } } }),
];

const plan = (api: string): Reply => json({ login: "octo", endpoints: { api } });
type Scenario = { name: string; apiKey: string; baseUrl?: string; user?: Reply; models: Reply };
const scenarios: Scenario[] = [
	{ name: "personal-plan-endpoint", apiKey: "gho_capture", user: plan("https://api.individual.githubcopilot.com/"), models: json({ data: fullRoster }) },
	{ name: "personal-probe-fails", apiKey: "gho_capture", user: { status: 401, body: "{}" }, models: json({ data: fullRoster }) },
	{ name: "personal-probe-not-https", apiKey: "gho_capture", user: plan("http://insecure.example"), models: json({ data: fullRoster }) },
	{ name: "enterprise-key", apiKey: JSON.stringify({ token: "gho_enterprise", enterpriseUrl: "https://octo.ghe.com" }), models: json({ data: fullRoster }) },
	{ name: "api-endpoint-key", apiKey: JSON.stringify({ token: "gho_endpoint", apiEndpoint: "https://api.business.githubcopilot.com/" }), models: json({ data: fullRoster }) },
	{ name: "custom-base-url", apiKey: "gho_capture", baseUrl: "https://copilot-proxy.example/v1", models: json({ data: fullRoster }) },
	{ name: "models-rejected", apiKey: "gho_capture", user: plan("https://api.individual.githubcopilot.com"), models: { status: 500, body: "{}" } },
	{ name: "models-empty", apiKey: "gho_capture", user: plan("https://api.individual.githubcopilot.com"), models: json({ data: [] }) },
	{ name: "models-unreadable", apiKey: "gho_capture", user: plan("https://api.individual.githubcopilot.com"), models: { status: 200, body: "{not json" } },
];

const cacheDir = mkdtempSync(join(tmpdir(), "neopi-copilot-discovery-"));
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
for (const scenario of scenarios) {
	const requests: any[] = [];
	const fetchImpl = async (input: any, init: any) => {
		const url = new URL(typeof input === "string" ? input : input.url);
		requests.push({
			method: init?.method ?? "GET",
			url: url.toString(),
			headers: Object.fromEntries(Object.entries(new Headers(init?.headers ?? {}).toJSON()).map(([k, v]) => [k.toLowerCase(), v])),
		});
		const reply = url.pathname === "/copilot_internal/user" ? (scenario.user ?? { status: 404, body: "{}" }) : scenario.models;
		return new Response(reply.body, { status: reply.status, headers: { "content-type": "application/json" } });
	};
	const options = githubCopilotModelManagerOptions({ apiKey: scenario.apiKey, fetch: fetchImpl, ...(scenario.baseUrl ? { baseUrl: scenario.baseUrl } : {}) });
	options.cacheDbPath = join(cacheDir, `copilot-${results.length}.db`);
	const resolved = await resolveProviderModels(options, "online");
	results.push({
		scenario: scenario.name,
		apiKey: scenario.apiKey,
		...(scenario.baseUrl ? { baseUrl: scenario.baseUrl } : {}),
		...(scenario.user ? { user: scenario.user } : {}),
		models: scenario.models,
		requests,
		source: resolved.source,
		stale: resolved.stale,
		resolved: resolved.models.map((model: any) => ({
			id: model.id,
			api: model.api,
			name: model.name,
			reasoning: model.reasoning,
			input: model.input,
			contextWindow: model.contextWindow ?? null,
			maxTokens: model.maxTokens ?? null,
			baseUrl: model.baseUrl,
			...(model.requestModelId && model.requestModelId !== model.id ? { requestModelId: model.requestModelId } : {}),
			...(model.contextPromotionTarget ? { contextPromotionTarget: model.contextPromotionTarget } : {}),
			headers: model.headers ?? null,
			compatIndex: compatIndex(model.compat),
			...(model.thinking ? { thinking: model.thinking } : {}),
			cost: { input: model.cost?.input ?? 0, output: model.cost?.output ?? 0, cacheRead: model.cost?.cacheRead ?? 0, cacheWrite: model.cost?.cacheWrite ?? 0 },
		})),
	});
}

writeFileSync(outFile, Bun.gzipSync(JSON.stringify({ source: "NeoPi OWN githubCopilotModelManagerOptions via resolveProviderModels", compats, results })));
console.log(`wrote ${results.length} scenarios to ${outFile}`);
for (const result of results) console.log(result.scenario, result.source, result.resolved.length, "rows,", result.requests.length, "requests");
