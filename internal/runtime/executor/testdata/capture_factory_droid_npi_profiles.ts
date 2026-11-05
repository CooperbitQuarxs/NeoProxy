// Exports NeoPi's OWN Factory Droid registry (pi-catalog compat/factory-droid
// factoryDroidRegistry) with everything the provider resolves from it:
//   - each model's account-independent build (buildFactoryDroidModel: name,
//     input, list-price cost, credits, thinking ladder and mode);
//   - for each upstream of its rotation, the route NeoPi's provider resolves
//     per request (resolveRoute: resolveModelPolicy over the wire's api and
//     the upstream, and the shared-transport row buildModel builds from it).
// Region-scoped fields (wire host, EU limits) are applied by the port. The
// export fails if a route depends on the region, so applying them afterwards
// is exact.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_factory_droid_npi_profiles.ts <out.json.gz>
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const out = process.argv[2];
if (!out) throw new Error("output path is required");
const { factoryDroidRegistry } = await import(root + "/pi-catalog/src/compat/factory-droid.ts");
const { buildFactoryDroidModel } = await import(root + "/pi-catalog/src/discovery/factory-droid.ts");
const { resolveModelPolicy } = await import(root + "/pi-catalog/src/compat/resolve.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { FACTORY_DROID_CLIENT_VERSION } = await import(root + "/pi-catalog/src/wire/factory-droid.ts");
const { quotaTierFor } = await import(root + "/pi-catalog/src/compat/behavior.ts");

// The route NeoPi resolves for one account-scoped model and upstream.
const route = (spec: any, wire: string, upstream: string) => {
	if (wire === "google-generate") {
		const resolved = resolveModelPolicy(spec, { upstream });
		return { request: resolved.request, catalog: resolved.catalog };
	}
	const resolved = resolveModelPolicy({ ...spec, api: wire }, { upstream });
	const inner = buildModel({ ...spec, api: wire, baseUrl: spec.baseUrl, compat: resolved.compat, thinking: resolved.thinking });
	return { request: resolved.request, catalog: resolved.catalog, compat: inner.compat, thinking: inner.thinking ?? null, reasoning: inner.reasoning };
};

const models = factoryDroidRegistry().map(({ spec: seed, policy }: any) => {
	const global = buildFactoryDroidModel({ spec: seed, policy }, { apiProviders: policy.rotation });
	const eu = buildFactoryDroidModel({ spec: seed, policy }, { region: "eu", inferenceRegion: "eu", apiProviders: policy.rotation });
	const routes: Record<string, unknown> = {};
	for (const upstream of policy.rotation) {
		const resolved = route(global, policy.wire, upstream);
		const regional = route({ ...eu, baseUrl: global.baseUrl, contextWindow: global.contextWindow, maxTokens: global.maxTokens }, policy.wire, upstream);
		if (JSON.stringify(resolved) !== JSON.stringify(regional)) throw new Error(`route ${seed.id}/${upstream} depends on the region`);
		routes[upstream] = resolved;
	}
	return {
		id: seed.id,
		name: global.name,
		input: global.input,
		cost: global.cost,
		reasoning: global.reasoning,
		...(global.thinking ? { thinking: global.thinking } : {}),
		...(global.factoryDroidCredits !== undefined ? { credits: global.factoryDroidCredits } : {}),
		// quota-tiers provider="factory-droid": the subscription pool it bills.
		...(quotaTierFor("factory-droid", seed.id) ? { quotaTier: quotaTierFor("factory-droid", seed.id) } : {}),
		policy,
		routes,
	};
});
await Bun.write(out, Bun.gzipSync(JSON.stringify({ source: "NeoPi OWN factoryDroidRegistry + buildFactoryDroidModel + resolveRoute", clientVersion: FACTORY_DROID_CLIENT_VERSION, models })));
console.log(`${models.length} models, ${models.reduce((n: number, m: any) => n + Object.keys(m.routes).length, 0)} routes`);
