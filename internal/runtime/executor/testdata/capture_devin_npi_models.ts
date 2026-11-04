// Exports NeoPi's OWN bundled devin seed (the rows an account serves until
// its GetCliModelConfigs discovery answers) as the NeoPiModel records the Go
// registry embeds: internal/registry/models/devin_neopi.json.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");

export const neoPiRecord = (model: any) => ({
	id: model.id,
	name: model.name,
	api: model.api,
	provider: model.provider,
	baseUrl: model.baseUrl,
	reasoning: model.reasoning,
	input: model.input,
	contextWindow: model.contextWindow ?? 0,
	maxTokens: model.maxTokens ?? 0,
	...(model.requestModelId ? { requestModelId: model.requestModelId } : {}),
	...(model.thinking
		? {
				thinking: {
					efforts: model.thinking.efforts,
					mode: model.thinking.mode,
					...(model.thinking.requiresEffort ? { requiresEffort: true } : {}),
					...(model.thinking.effortRouting ? { effortRouting: model.thinking.effortRouting } : {}),
					...(model.thinking.defaultLevel ? { defaultLevel: model.thinking.defaultLevel } : {}),
				},
			}
		: {}),
	devinCompat: { modelRouter: model.compat?.modelRouter === true, supportsParallelToolCalls: model.compat?.supportsParallelToolCalls === true },
});

if (import.meta.main) {
	console.log(JSON.stringify({ source: "NeoPi OWN getBundledModels(devin)", models: getBundledModels("devin").map(neoPiRecord) }, null, 2));
}
