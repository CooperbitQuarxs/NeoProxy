// Exports NeoPi's OWN bundled Amazon Bedrock rows (getBundledModels(
// "amazon-bedrock"), already built: thinking, the resolved
// bedrock-converse-stream compat and tool-result image hoisting) for
// NeoProxy's registry. Writes
// internal/registry/models/amazon_bedrock_neopi.json.gz.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks export_amazon_bedrock_rows.ts
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");

const models = getBundledModels("amazon-bedrock").map((model: any) => {
	if (model.api !== "bedrock-converse-stream") throw new Error(`unexpected api ${model.api} for ${model.id}`);
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
		requiresToolResultImageHoisting: model.requiresToolResultImageHoisting === true,
		cost: { input: model.cost?.input ?? 0, output: model.cost?.output ?? 0, cacheRead: model.cost?.cacheRead ?? 0, cacheWrite: model.cost?.cacheWrite ?? 0 },
	};
});
const out = join(import.meta.dir, "..", "models", "amazon_bedrock_neopi.json.gz");
await Bun.write(out, Bun.gzipSync(JSON.stringify({ source: 'NeoPi OWN getBundledModels("amazon-bedrock")', models })));
console.log(`amazon-bedrock catalog: ${models.length} rows`);
