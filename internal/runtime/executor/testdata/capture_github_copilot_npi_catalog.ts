// Exports NeoPi's OWN bundled GitHub Copilot rows (getBundledModels
// ("github-copilot"), each with the compat NeoPi resolved for its own API)
// for NeoProxy's registry. Writes internal/registry/models/github_copilot_neopi.json.gz.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_github_copilot_npi_catalog.ts
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");

const models = getBundledModels("github-copilot").map((model: any) => ({
	id: model.id,
	name: model.name,
	api: model.api,
	provider: model.provider,
	baseUrl: model.baseUrl,
	reasoning: model.reasoning,
	input: model.input,
	contextWindow: model.contextWindow ?? 0,
	maxTokens: model.maxTokens ?? 0,
	...(model.requestModelId && model.requestModelId !== model.id ? { requestModelId: model.requestModelId } : {}),
	...(model.thinking ? { thinking: model.thinking } : {}),
	compat: model.compat,
	cost: { input: model.cost?.input ?? 0, output: model.cost?.output ?? 0, cacheRead: model.cost?.cacheRead ?? 0, cacheWrite: model.cost?.cacheWrite ?? 0 },
}));
const out = join(import.meta.dir, "..", "..", "..", "registry", "models", "github_copilot_neopi.json.gz");
await Bun.write(out, Bun.gzipSync(JSON.stringify({ source: 'NeoPi OWN getBundledModels("github-copilot")', models })));
console.log(`github-copilot catalog: ${models.length} rows`);
