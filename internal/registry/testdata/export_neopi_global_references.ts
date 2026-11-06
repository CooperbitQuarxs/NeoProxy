// Exports NeoPi's OWN global bundled-reference map (provider-models/
// bundled-references.ts: what createReferenceResolver answers for an id its
// provider's bundle lacks) as spec-stage rows, with their sparse authored
// compat, for discovery mappers that fall back to it (GitHub Copilot,
// LiteLLM). Each id is resolved by NeoPi itself, so the bare-id provider
// filter and its tie-breaks are NeoPi's. Beside them it exports what
// loadModelsDevReferences reads from NeoPi's rules: every provider's runner
// apis (kindApis) and the reference-isolated providers. Also exports the
// api-routes rules (compat/rules.json behavior.apiRoutes) apiRouteFor reads.
//
// Writes internal/registry/models/neopi_discovery/global_references.json.gz
// and api_routes.json. Run from the NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file>
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getBundledModels, getBundledProviders } = await import(root + "/pi-catalog/src/models.ts");
const { createReferenceResolver } = await import(root + "/pi-catalog/src/provider-models/bundled-references.ts");
const { providerEntries } = await import(root + "/pi-catalog/src/compat/providers.ts");

// An empty provider map leaves only the global fallback.
const resolve = createReferenceResolver(new Map());
const ids = new Set<string>();
for (const provider of getBundledProviders()) for (const model of getBundledModels(provider) as any[]) ids.add(model.id);

const references: any[] = [];
for (const id of [...ids].sort()) {
	const spec: any = resolve(id);
	if (!spec) continue;
	references.push({
		id: spec.id,
		name: spec.name,
		api: spec.api,
		provider: spec.provider,
		reasoning: spec.reasoning === true,
		input: spec.input,
		contextWindow: spec.contextWindow ?? null,
		maxTokens: spec.maxTokens ?? null,
		...(spec.requestModelId ? { requestModelId: spec.requestModelId } : {}),
		...(spec.thinking ? { thinking: spec.thinking } : {}),
		...(spec.compat && Object.keys(spec.compat).length > 0 ? { compat: spec.compat } : {}),
		cost: { input: spec.cost?.input ?? 0, output: spec.cost?.output ?? 0, cacheRead: spec.cost?.cacheRead ?? 0, cacheWrite: spec.cost?.cacheWrite ?? 0 },
	});
}

const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;
const kindApis: Record<string, Record<string, string>> = {};
for (const [provider, compiled] of Object.entries(providerEntries()) as [string, any][]) {
	if (compiled?.kindApis && Object.keys(compiled.kindApis).length > 0) kindApis[provider] = compiled.kindApis;
}
const referenceIsolatedProviders: string[] = rules.behavior.referenceIsolatedProviders;
// isLikelyOpenAIResponsesId and isExcludedDiscoveryMode read these.
const openaiResponsesHeuristic = rules.behavior.openaiResponsesHeuristic;
const excludeDiscoveryModes = rules.behavior.excludeDiscoveryModes;

const out = join(dirname(new URL(import.meta.url).pathname), "..", "models", "neopi_discovery", "global_references.json.gz");
writeFileSync(out, Bun.gzipSync(JSON.stringify({
	source: "NeoPi OWN createReferenceResolver global fallback (bundled-references.ts), providerEntries kindApis, behavior.referenceIsolatedProviders, behavior.openaiResponsesHeuristic, behavior.excludeDiscoveryModes",
	references,
	kindApis,
	referenceIsolatedProviders,
	openaiResponsesHeuristic,
	excludeDiscoveryModes,
})));

const routesOut = join(dirname(new URL(import.meta.url).pathname), "..", "models", "neopi_discovery", "api_routes.json");
writeFileSync(routesOut, JSON.stringify(rules.behavior.apiRoutes));
console.log(`wrote ${rules.behavior.apiRoutes.length} api-route tables to ${routesOut}`);
console.log(`wrote ${references.length} global references of ${ids.size} bundled ids to ${out}`);
