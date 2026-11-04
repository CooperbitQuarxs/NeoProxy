// Exports NeoPi's OWN resolved `zai` catalog for NeoProxy's registry
// (internal/registry/models/zai_neopi.json). The zai provider has no live
// discovery fetch: NeoPi serves its bundled rows (models.dev plus the KDL
// seed), minus the exclude-models rule, each on the API the api-routes rule
// assigns (Anthropic Messages, or OpenAI Completions for GLM-5.3-Flash).
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { isExcludedModel } = await import(root + "/pi-catalog/src/compat/behavior.ts");

const project = (model: any) => ({
	id: model.id,
	name: model.name,
	api: model.api,
	provider: model.provider,
	baseUrl: model.baseUrl,
	reasoning: model.reasoning,
	input: model.input,
	contextWindow: model.contextWindow ?? 0,
	maxTokens: model.maxTokens ?? 0,
	...(model.thinking
		? {
				thinking: {
					efforts: model.thinking.efforts,
					mode: model.thinking.mode,
					...(model.thinking.requiresEffort ? { requiresEffort: true } : {}),
					...(model.thinking.supportsDisplay !== undefined ? { supportsDisplay: model.thinking.supportsDisplay } : {}),
				},
			}
		: {}),
	...(model.api === "anthropic-messages" ? { anthropicCompat: model.compat } : {}),
	...(model.api === "openai-completions" ? { completionsCompat: model.compat } : {}),
});
const bundled = getBundledModels("zai");
const models = bundled.filter((model: any) => !isExcludedModel("zai", model.id)).map(project);
// Probe ids for the exclusion rule: Claude Code's [1m] selector never reaches the catalog.
const probes = ["glm-5.2[1m]", "glm-5.3", "glm-5.3-flash"];
const excluded = Object.fromEntries(probes.map(id => [id, isExcludedModel("zai", id)]));
console.log(JSON.stringify({ source: "NeoPi OWN getBundledModels(\"zai\") minus isExcludedModel", models, excluded }, null, 2));
