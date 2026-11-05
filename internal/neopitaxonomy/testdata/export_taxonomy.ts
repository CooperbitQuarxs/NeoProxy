// Exports NeoPi's compiled model taxonomy (catalog compat/rules.json
// `taxonomy`: classes with matchers, families, revision prefixes, skip lists
// and identity overrides, plus the collapse vocabulary) for the Go port, and
// captures what NeoPi's own classifyModel(provider, id, { lenient: true })
// answers for every bundled (provider, id) pair and a set of probe ids under
// each provider.
//
// Writes ../taxonomy.json and classify_capture.json.gz beside this script.
// Run from the NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file>
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const here = dirname(new URL(import.meta.url).pathname);

const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;
const { classifyModel, collapseVariantId, stripThinkingVariantSuffix } = await import(root + "/pi-catalog/src/compat/taxonomy.ts");
const { getBundledModels, getBundledProviders } = await import(root + "/pi-catalog/src/models.ts");

const { classes, collapse } = rules.taxonomy;
writeFileSync(
	join(here, "../taxonomy.json"),
	JSON.stringify({
		classes,
		collapse: { suffixes: collapse.suffixes, lanes: collapse.lanes, effortFamilies: collapse.effortFamilies, pairTokens: collapse.pairTokens },
	}),
);

// Ids no bundle carries: revision edge cases, namespaces, collapse suffixes.
const probes = [
	"qwen3-235b-reasoning", "model-non-thinking", "model-no-thinking", "x-thinking-2507", "-thinking", "deepseek-reasoner",
	"Model-Thinking-Pro", "glm-5.2-thinking-high", "kimi-k2-thinking-turbo", "a/b-reasoning", "thinking",
	"glm-4.5", "glm-4.5-air", "glm-4.5-flash", "glm-5.3-flash", "glm-5.2", "zai-org/GLM-5.3-FP8", "glm-4.6v",
	"glm-5v-turbo", "GLM-5.2-thinking", "glm-z1-air", "glm-4.7-flashx", "glm5.3", "glm-5-2", "glm-300.1",
	"kimi-k3", "Kimi-K3-0801", "moonshotai/kimi-k3-thinking", "kimi-k2.9-thinking", "kimi-latest",
	"deepseek-v4.1-flash", "deepseek-ai/DeepSeek-V4-Pro-0813", "deepseek-reasoner", "deepseek-r1", "deepseek-v3.2-exp",
	"openai/gpt-oss-120b", "gpt-oss-20b", "qwen3.9-plus", "Qwen/Qwen3-235B-A22B-Thinking-2507", "minimax-m3",
	"claude-sonnet-5-5", "anthropic.claude-opus-5", "gemini-3.5-pro", "gpt-6.1-sol", "gpt-5.5-high", "o3-mini",
	"zz-new-model", "", "  glm-5.2  ", "a/b/c/glm-5.1:free", "z-ai/glm-4.6:exacto", "meta-llama/Llama-4-Scout",
];

const pairs = new Map<string, Set<string>>();
const add = (provider: string, id: string) => {
	let set = pairs.get(provider);
	if (!set) pairs.set(provider, (set = new Set()));
	set.add(id);
};
const providers: string[] = [...getBundledProviders()];
for (const provider of providers) {
	for (const model of getBundledModels(provider)) add(provider, model.id);
	for (const id of probes) add(provider, id);
}
for (const id of probes) add("", id);

const cases: any[] = [];
for (const [provider, ids] of pairs) {
	for (const id of ids) {
		const identity = classifyModel(provider, id, { lenient: true });
		const collapsed = collapseVariantId(provider, id);
		cases.push([provider, id, identity.class, identity.family ?? "", identity.revision ?? "", identity.effort ?? "", identity.thinkingVariant === true, identity.logicalId ?? "", stripThinkingVariantSuffix(id) ?? "", collapsed.logicalId, collapsed.thinkingVariant]);
	}
}
writeFileSync(join(here, "classify_capture.json.gz"), Bun.gzipSync(JSON.stringify(cases)));
console.log(`wrote ${cases.length} classify cases`);
