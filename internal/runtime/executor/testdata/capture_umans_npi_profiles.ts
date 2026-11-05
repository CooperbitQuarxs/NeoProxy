// Runs NeoPi's OWN umans discovery (umansModelManagerOptions) over
// testdata/umans_models_info.json, a synthetic /v1/models/info payload that
// covers each mapper branch, and resolves every row through buildModel as
// NeoPi's model manager does, so the umans catalog rules apply. With
// --registry it exports NeoPi's bundled umans rows, which serve a key until
// its discovery answers (internal/registry/models/umans_neopi.json).
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { umansModelManagerOptions } = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");

const record = (model: any) => ({
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
					...(model.thinking.effortMap ? { effortMap: model.thinking.effortMap } : {}),
					mode: model.thinking.mode,
					...(model.thinking.requiresEffort ? { requiresEffort: true } : {}),
					...(model.thinking.defaultLevel ? { defaultLevel: model.thinking.defaultLevel } : {}),
					...(model.thinking.supportsDisplay !== undefined ? { supportsDisplay: model.thinking.supportsDisplay } : {}),
				},
			}
		: {}),
	...(model.supportsTools === false ? { supportsTools: false } : {}),
	anthropicCompat: model.compat,
});

const bundled = getBundledModels("umans");
if (process.argv[2] === "--registry") {
	console.log(JSON.stringify({ source: "NeoPi OWN getBundledModels(umans)", models: bundled.map(record) }, null, 2));
} else {
	const payload = await Bun.file(import.meta.dir + "/umans_models_info.json").text();
	let request: any;
	const fakeFetch = (async (input: any, init: any) => {
		request = { url: String(input instanceof Request ? input.url : input), method: init?.method, headers: Object.fromEntries(new Headers(init?.headers)) };
		return new Response(payload, { headers: { "content-type": "application/json" } });
	}) as typeof fetch;
	const options = umansModelManagerOptions({ apiKey: "umans-capture-key", fetch: fakeFetch });
	const specs = await options.fetchDynamicModels!();
	console.log(
		JSON.stringify(
			{
				source: "NeoPi OWN umansModelManagerOptions.fetchDynamicModels + buildModel over testdata/umans_models_info.json",
				apiKey: "umans-capture-key",
				discoveryRequest: request,
				dynamicModelsAuthoritative: options.dynamicModelsAuthoritative,
				models: specs.map((spec: any) => record(buildModel(spec))),
			},
			null,
			2,
		),
	);
}
