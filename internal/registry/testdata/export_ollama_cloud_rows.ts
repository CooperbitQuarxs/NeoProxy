// Exports NeoPi's OWN bundled Ollama Cloud rows (getBundledModels(
// "ollama-cloud"), already built: thinking, the resolved ollama-chat compat
// and omitMaxOutputTokens) for NeoProxy's registry, with each row's identity
// class (which picks its stream markup healing). Writes
// internal/registry/models/ollama_cloud_neopi.json.gz.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks export_ollama_cloud_rows.ts
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");

const models = getBundledModels("ollama-cloud").map((model: any) => {
	if (model.api !== "ollama-chat") throw new Error(`unexpected api ${model.api} for ${model.id}`);
	return {
		id: model.id,
		name: model.name,
		api: model.api,
		provider: model.provider,
		baseUrl: model.baseUrl,
		reasoning: model.reasoning,
		input: model.input,
		contextWindow: model.contextWindow ?? 0,
		maxTokens: model.maxTokens ?? 0,
		...(model.thinking ? { thinking: model.thinking } : {}),
		compat: model.compat,
		omitMaxOutputTokens: model.omitMaxOutputTokens === true,
		identityClass: model.identity?.class ?? "unknown",
		cost: { input: model.cost?.input ?? 0, output: model.cost?.output ?? 0, cacheRead: model.cost?.cacheRead ?? 0, cacheWrite: model.cost?.cacheWrite ?? 0 },
	};
});
const out = join(import.meta.dir, "..", "models", "ollama_cloud_neopi.json.gz");
await Bun.write(out, Bun.gzipSync(JSON.stringify({ source: 'NeoPi OWN getBundledModels("ollama-cloud")', models })));
console.log(`ollama-cloud catalog: ${models.length} rows`);
