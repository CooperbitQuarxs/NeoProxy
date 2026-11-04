// Runs NeoPi's OWN kimi-code discovery mapper over a recorded
// /coding/v1/models envelope (testdata/kimi_models_live.json) and resolves each
// row through buildModel, as NeoPi's model manager does, so the catalog rules
// (api format default, thinking format, output caps, modalities) apply. It
// also resolves NeoPi's bundled kimi-code rows, which serve an account until
// its discovery answers.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { kimiCodeModelManagerOptions } = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const payload = await Bun.file(import.meta.dir + "/kimi_models_live.json").text();
let request: any;
const fakeFetch = (async (input: any, init: any) => {
	request = { url: String(input instanceof Request ? input.url : input), headers: Object.fromEntries(new Headers(init?.headers)) };
	return new Response(payload, { headers: { "content-type": "application/json" } });
}) as typeof fetch;
const options = kimiCodeModelManagerOptions({ apiKey: "kimi-capture-token", fetch: fakeFetch });
const specs = await options.fetchDynamicModels!();
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
	completionsCompat: model.compat,
});
// streamKimi -> streamOpenAIAnthropicShim rebuilds each row as an Anthropic
// Messages model (thinking mode adaptive when thinkingFormat is kimi); that
// model's resolved compat drives the request.
const shimModel = (model: any) =>
	buildModel({
		id: model.id,
		name: model.name,
		api: "anthropic-messages",
		provider: model.provider,
		baseUrl: model.baseUrl.replace(/\/v1\/?$/, ""),
		contextWindow: model.contextWindow,
		maxTokens: model.maxTokens,
		reasoning: model.reasoning,
		...(model.compat.thinkingFormat === "kimi" && model.thinking ? { thinking: { ...model.thinking, mode: "anthropic-adaptive" } } : {}),
		input: model.input,
		cost: model.cost,
	});
const resolve = (resolved: any) => {
	const anthropic = shimModel(resolved);
	return { ...project(resolved), anthropic: { baseUrl: anthropic.baseUrl, thinking: anthropic.thinking ?? null, anthropicCompat: anthropic.compat } };
};
const models = specs.map((spec: any) => resolve(buildModel(spec)));
// The bundled rows as the NeoPiModel records the Go registry embeds
// (internal/registry/models/kimi_neopi.json): the shim's Anthropic Messages view.
const neoPiRecord = (model: any) => {
	const anthropic = shimModel(model);
	const thinking = anthropic.thinking;
	return {
		id: anthropic.id,
		name: anthropic.name,
		api: anthropic.api,
		provider: anthropic.provider,
		baseUrl: anthropic.baseUrl,
		reasoning: anthropic.reasoning,
		input: anthropic.input,
		contextWindow: anthropic.contextWindow ?? 0,
		maxTokens: anthropic.maxTokens ?? 0,
		...(thinking
			? {
					thinking: {
						efforts: thinking.efforts,
						...(thinking.effortMap ? { effortMap: thinking.effortMap } : {}),
						mode: thinking.mode,
						...(thinking.requiresEffort ? { requiresEffort: true } : {}),
						...(thinking.supportsDisplay !== undefined ? { supportsDisplay: thinking.supportsDisplay } : {}),
					},
				}
			: {}),
		anthropicCompat: anthropic.compat,
	};
};
const bundledModels = getBundledModels("kimi-code");
const bundled = bundledModels.map((model: any) => resolve(model));
if (process.argv[2] === "--registry") {
	console.log(JSON.stringify({ source: "NeoPi OWN getBundledModels(kimi-code) via the OpenAI/Anthropic shim", models: bundledModels.map(neoPiRecord) }, null, 2));
} else {
	console.log(JSON.stringify({ source: "NeoPi OWN kimiCodeModelManagerOptions.fetchDynamicModels + buildModel; getBundledModels(kimi-code)", discoveryRequest: request, models, bundled }, null, 2));
}
