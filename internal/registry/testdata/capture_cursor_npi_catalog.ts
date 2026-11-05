// Exports the NeoPi catalog data the Go Cursor discovery port reads:
//
// - internal/neopicatalog/taxonomy.json: rules.json's taxonomy (identity
//   classes and the collapse vocabulary), read by the Go taxonomy port;
// - internal/registry/models/cursor_neopi.json: NeoPi's bundled cursor rows
//   (getBundledModels), the reviewed cursor collapse families, and the policy
//   resolveModelPolicy derives per model id (the thinking ladder it derives,
//   the fields it fills into explicit thinking, and the catalog fields
//   buildModel applies).
//
// Policy is NeoPi's rule engine output, not ported code; it is exported for
// every id the cursor pipeline can build from the bundled catalog, the
// reviewed table and the given live discovery bodies.
//
//   bun capture_cursor_npi_catalog.ts <dir with AvailableModels.bin GetUsableModels.bin GetDefaultModelForCli.bin>
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const live = process.argv[2];
if (!live) throw new Error("usage: capture_cursor_npi_catalog.ts <live discovery dir>");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json")).default as any;
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { resolveModelPolicy } = await import(root + "/pi-catalog/src/compat/resolve.ts");
const { parseDatedCostCards } = await import(root + "/pi-catalog/src/pricing.ts");
const { fetchCursorUsableModels } = await import(root + "/pi-catalog/src/discovery/cursor.ts");
const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
const { cursorModelManagerOptions } = await import(root + "/pi-catalog/src/provider-models/special.ts");
const { CURSOR_DEFAULT_BASE_URL } = await import(root + "/pi-catalog/src/wire/cursor.ts");
const { THINKING_EFFORTS } = await import(root + "/pi-catalog/src/effort.ts");

const out = import.meta.dir + "/../..";

await Bun.write(
	out + "/neopicatalog/taxonomy.json",
	JSON.stringify({
		source: "NeoPi OWN pi-catalog compat/rules.json taxonomy.{classes,collapse}, behavior.cursorParameters",
		classes: rules.taxonomy.classes,
		collapse: rules.taxonomy.collapse,
		cursorParameters: rules.behavior.cursorParameters,
	}) + "\n",
);

const { specRecord } = await import(import.meta.dir + "/capture_cursor_npi_catalog_records.ts");
for (const model of getBundledModels("cursor") as any[]) {
	if (model.cost.timeBased !== undefined) throw new Error(`bundled ${model.id} has a time-based cost`);
}

const families = rules.taxonomy.collapse.variantFamilies.filter((family: any) => family.provider === "cursor");
for (const family of families) {
	for (const key of ["retiredMembers", "effortBudgets", "suppressWhenOff", "preserveAbsentEffortRoutes", "noThinking"]) {
		if (family[key] !== undefined && family[key] !== false && !(Array.isArray(family[key]) && family[key].length === 0)) {
			throw new Error(`cursor family ${family.id} uses ${key}, which the Go port does not implement`);
		}
	}
}
if (rules.taxonomy.collapse.providerAliases?.cursor !== undefined) throw new Error("cursor provider aliases are not ported");

// Every id the pipeline can build: bundled rows, reviewed families, and what
// discovery and the model manager produce from the live bodies.
const ids = new Set<string>();
const collect = (m: any) => {
	ids.add(m.id);
	if (m.requestModelId) ids.add(m.requestModelId);
	for (const target of Object.values(m.thinking?.effortRouting ?? {})) ids.add(target as string);
	for (const [key, route] of Object.entries(m.cursorModelRoutes ?? {})) {
		ids.add(key);
		ids.add((route as any).modelId);
	}
};
for (const model of getBundledModels("cursor")) collect(model);
for (const family of families) {
	if (String(family.id).includes("{rev}")) continue;
	ids.add(family.id);
	for (const member of family.members) ids.add(member);
	for (const alias of family.extraAliases ?? []) ids.add(alias);
}
const bodies: Record<string, Uint8Array | null> = {};
globalThis.fetch = Object.assign(
	async (input: any) => {
		const body = bodies[new URL(String(input instanceof Request ? input.url : input)).pathname.split("/").pop()!];
		return body ? new Response(body) : new Response("", { status: 503 });
	},
	{ preconnect: () => {} },
) as typeof fetch;
for (const fail of [[], ["AvailableModels"], ["GetUsableModels"]]) {
	for (const name of ["AvailableModels", "GetDefaultModelForCli", "GetUsableModels"]) {
		bodies[name] = fail.includes(name) ? null : new Uint8Array(await Bun.file(`${live}/${name}.bin`).arrayBuffer());
	}
	for (const spec of (await fetchCursorUsableModels({ apiKey: "x" })) ?? []) collect(spec);
	const db = `/tmp/cursor-catalog-${Date.now()}-${fail.join("")}.db`;
	const result = await resolveProviderModels({ ...cursorModelManagerOptions({ apiKey: "x" }), cacheDbPath: db }, "online");
	for (const model of result.models) collect(model);
}
// Rich-lane pricing reads the rate card of every slug a model declares.
{
	const proto = await import(root + "/pi-catalog/src/discovery/cursor-proto.ts");
	const { fromBinary } = await import(root + "/pi-catalog/src/discovery/protobuf.ts");
	const available = fromBinary(proto.AvailableModelsResponseSchema, new Uint8Array(await Bun.file(`${live}/AvailableModels.bin`).arrayBuffer()));
	for (const details of available.models) {
		for (const id of [details.name, ...details.legacySlugs, ...details.idAliases, ...details.variants.map((v: any) => v.legacySlug ?? "")]) {
			if (id.trim()) ids.add(id.trim());
		}
	}
}

const probe = (id: string, reasoning: boolean, thinking?: any) => ({
	id,
	name: id,
	api: "cursor-agent",
	provider: "cursor",
	baseUrl: CURSOR_DEFAULT_BASE_URL,
	reasoning,
	input: ["text"],
	cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
	contextWindow: 200_000,
	maxTokens: 64_000,
	...(thinking ? { thinking } : {}),
});
const CATALOG_FIELDS = [
	"contextWindowFloor",
	"inputModalities",
	"requiresCursorToolSchemaProjection",
	"maxTokensFallback",
	"maxTokensCeiling",
	"limitsPatch",
	"costPatch",
	"costFallback",
	"longContext",
	"cacheReadAtInputRate",
	"cacheWriteRate",
	"timeBased",
];
const catalogFields = (id: string, catalog: any) => {
	if (catalog.timeBased !== undefined) throw new Error(`${id}: time-based cost is not ported`);
	// Validates dated cards the way buildModel will.
	for (const axis of ["cost-patch", "cost-fallback"] as const) {
		parseDatedCostCards(axis === "cost-patch" ? catalog.costPatch : catalog.costFallback, axis);
	}
	const record: any = {};
	for (const key of CATALOG_FIELDS) {
		if (catalog[key] !== undefined) record[key] = catalog[key];
	}
	return record;
};
const policy: Record<string, any> = {};
for (const id of [...ids].sort()) {
	const derived = resolveModelPolicy(probe(id, true));
	const explicit = resolveModelPolicy(probe(id, true, { mode: "effort", efforts: [...THINKING_EFFORTS] }));
	const off = resolveModelPolicy(probe(id, false));
	const offExplicit = resolveModelPolicy(probe(id, false, { mode: "effort", efforts: [...THINKING_EFFORTS] }));
	if (off.thinking !== undefined || offExplicit.thinking !== undefined) throw new Error(`${id}: thinking without reasoning is not ported`);
	const fill: any = {};
	for (const key of ["supportsDisplay", "requiresEffort", "defaultLevel", "prefixBinding", "effortBudgets", "effortMap"]) {
		if (explicit.thinking?.[key] !== undefined) fill[key] = explicit.thinking[key];
	}
	for (const key of ["supportsDisplay", "effortBudgets", "effortMap"]) {
		if (fill[key] !== undefined) throw new Error(`${id}: thinking fill ${key} is not ported`);
	}
	const catalog = catalogFields(id, derived.catalog);
	if (JSON.stringify(catalogFields(id, off.catalog)) !== JSON.stringify(catalog)) throw new Error(`${id}: catalog depends on reasoning`);
	const entry: any = { catalog };
	if (derived.thinking !== undefined) entry.derived = derived.thinking;
	if (Object.keys(fill).length > 0) entry.fill = fill;
	policy[id] = entry;
}

await Bun.write(
	out + "/registry/models/cursor_neopi.json",
	JSON.stringify(
		{
			source: "NeoPi OWN getBundledModels(cursor) and resolveModelPolicy per id",
			bundled: getBundledModels("cursor").map(specRecord),
			policy,
		},
		null,
		1,
	) + "\n",
);
console.log(`taxonomy + ${getBundledModels("cursor").length} bundled rows, ${families.length} families, ${Object.keys(policy).length} policy ids`);
