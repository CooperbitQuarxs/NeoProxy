// Exports NeoPi's OWN resolved xAI catalogs for NeoProxy's registry
// (internal/registry/models/xai_neopi.json): the xai-oauth KDL seed run
// through buildModel, as NeoPi's model manager materializes it, and the
// bundled catalog of the paid `xai` (API key) provider.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { buildXaiOAuthStaticSeed } = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");
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
	...(model.thinking ? { thinking: { efforts: model.thinking.efforts, ...(model.thinking.effortMap ? { effortMap: model.thinking.effortMap } : {}) } } : {}),
	...(model.api === "openai-responses" ? { compat: model.compat } : {}),
});
const models = buildXaiOAuthStaticSeed().map((seed: any) => project(buildModel(seed)));
const apiKeyModels = getBundledModels("xai").map(project);
// Probe ids from the live roster and its media siblings: the exclusion rule decides
// which discovered rows never reach the chat catalog.
const probes = ["grok-imagine-image-quality", "grok-imagine-image-2.0", "grok-imagine-video", "grok-imagine-video-1.5", "grok-stt-1", "grok-voice-1", "grok-4.7", "grok-imagine-image"];
const excluded = Object.fromEntries(probes.map(id => [id, isExcludedModel("xai-oauth", id)]));
console.log(JSON.stringify({ source: "NeoPi OWN buildXaiOAuthStaticSeed + buildModel; getBundledModels(\"xai\")", models, apiKeyModels, excluded }, null, 2));
