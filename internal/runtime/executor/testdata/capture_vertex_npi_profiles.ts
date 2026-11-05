// Exports NeoPi's OWN resolved `google-vertex` Gemini catalog for NeoProxy's
// registry (internal/registry/models/google_vertex_neopi.json): the bundled
// rows NeoPi serves over Vertex AI's generateContent (api google-vertex). The
// provider has no model discovery; its Anthropic and Model Garden rows ride
// other transports and are exported with those catalogs.
//
// A model an account configures by id alone has no NeoPi record. NeoProxy
// builds it the way it builds every NeoPi provider's configured id: a bare
// non-reasoning text spec, which NeoPi's buildModel resolves under
// google-vertex (its provider compat rules included). `configured` pins that
// resolution for every id of the Gemini probe listing
// (testdata/gemini_models_probe.json) and every bundled id.
//
//   cd <neopi env> && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_vertex_npi_profiles.ts > google_vertex_neopi.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");

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
					...(model.thinking.suppressWhenOff ? { suppressWhenOff: true } : {}),
					...(model.thinking.effortBudgets ? { effortBudgets: model.thinking.effortBudgets } : {}),
				},
			}
		: {}),
	googleCompat: {
		multimodalFunctionResponse: model.compat.multimodalFunctionResponse,
		requiresSkipThoughtSignature: model.compat.requiresSkipThoughtSignature,
		requiresSkipThoughtSignatureOnFirstFunctionCall: model.compat.requiresSkipThoughtSignatureOnFirstFunctionCall,
		supportsFunctionPartId: model.compat.supportsFunctionPartId,
		dropUnsignedThinking: model.compat.dropUnsignedThinking,
		ccaLegacyParametersSchema: model.compat.ccaLegacyParametersSchema,
	},
});

const models = getBundledModels("google-vertex")
	.filter((model: any) => model.api === "google-vertex")
	.map(project);

const probe = JSON.parse(await Bun.file(import.meta.dir + "/gemini_models_probe.json").text());
const probeIds = probe.models.map((item: any) => String(item.name).replace(/^models\//, ""));
const vertexBase = getBundledModels("google-vertex").find((model: any) => model.api === "google-vertex")!.baseUrl;
const ids = [...new Set([...probeIds, ...models.map((model: any) => model.id)])];
const configured = ids.map((id: string) =>
	project(
		buildModel({
			id,
			name: id,
			api: "google-vertex",
			provider: "google-vertex",
			baseUrl: vertexBase,
			reasoning: false,
			input: ["text"],
			cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
		}),
	),
);
console.log(JSON.stringify({ source: 'NeoPi OWN getBundledModels("google-vertex") google-vertex rows; configured: bare text specs built by buildModel under google-vertex', models, configured }, null, 2));
