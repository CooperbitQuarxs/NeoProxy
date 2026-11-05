// Runs NeoPi's OWN Factory Droid discovery (pi-catalog discovery/factory-droid:
// fetchFactoryDroidModels and factoryDroidSeedModels) over synthetic
// feature-flag and managed-settings payloads, recording the requests it
// sends and the account-scoped specs it returns. Writes JSON to the path in
// argv[2]:
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_factory_droid_discovery.ts <out.json.gz>
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const out = process.argv[2];
if (!out) throw new Error("output path is required");
const { factoryDroidRegistry } = await import(root + "/pi-catalog/src/compat/factory-droid.ts");
const { fetchFactoryDroidModels, factoryDroidSeedModels } = await import(root + "/pi-catalog/src/discovery/factory-droid.ts");

const registry = factoryDroidRegistry();
const featureFlags = [...new Set(registry.map((m: any) => m.policy.entitlement.featureFlag).filter(Boolean))] as string[];
const deprecationFlags = [...new Set(registry.map((m: any) => m.policy.entitlement.deprecationFlag).filter(Boolean))] as string[];
const allOn = Object.fromEntries(featureFlags.map(flag => [flag, true]));
const byWire = (wire: string) => registry.filter((m: any) => m.policy.wire === wire).map((m: any) => m.spec.id);
const anthropic = byWire("anthropic-messages");
const completions = byWire("openai-completions");
const aliased = registry.find((m: any) => m.policy.policyAliases.length > 0);
const fast = registry.filter((m: any) => m.policy.entitlement.baseVariant).map((m: any) => m.spec.id);
const optIn = registry.filter((m: any) => m.policy.entitlement.requiresExplicitOptIn).map((m: any) => m.spec.id);
const images = registry.filter((m: any) => m.policy.image).map((m: any) => m.spec.id);
const family = registry.find((m: any) => m.policy.routingFamily && m.policy.rotation.length > 1);
const multi = registry.find((m: any) => m.policy.rotation.length > 2);

const token = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1c2VyX2Zha2UifQ.fake";
const json = (body: unknown, status = 200) => ({ status, body: JSON.stringify(body) });
const scenarios: any[] = [
	{ name: "all-flags-no-settings", flags: json({ flags: allOn }), settings: { status: 404, body: "{}" } },
	{ name: "all-flags-allow-all-images", flags: json({ flags: allOn }), settings: json({ settings: { modelPolicy: { allowAllFactoryModels: true }, imageGenerationEnabled: true } }) },
	{ name: "no-flags-empty-policy", flags: json({ flags: {} }), settings: json({ settings: {} }) },
	{ name: "deprecated", flags: json({ flags: { ...allOn, ...Object.fromEntries(deprecationFlags.map(flag => [flag, true])) } }), settings: json({ settings: { modelPolicy: {} } }) },
	{ name: "blocked-and-opt-in", flags: json({ flags: allOn }), settings: json({ settings: { modelPolicy: { blockedModelIds: [anthropic[0], 7], requireExplicitOptInModelIds: optIn.slice(0, 2) } } }) },
	{ name: "allowlist", flags: json({ flags: allOn }), settings: json({ settings: { modelPolicy: { allowedModelIds: [anthropic[1], completions[0], ...(aliased ? [aliased.policy.policyAliases[0]] : [])] } } }) },
	{ name: "deny-all", flags: json({ flags: allOn }), settings: json({ settings: { modelPolicy: { allowAllFactoryModels: false } } }) },
	{ name: "fast-withdrawn", flags: json({ flags: allOn }), settings: json({ settings: { modelPolicy: { isFastModelsAllowed: false } } }) },
	{ name: "images-narrowed", flags: json({ flags: allOn }), settings: json({ settings: { imageGenerationAllowedModelIds: images.slice(0, 1) } }) },
	{ name: "images-disabled", flags: json({ flags: allOn }), settings: json({ settings: { imageGenerationEnabled: false } }) },
	{
		name: "live-routing",
		flags: json({
			flags: allOn,
			configs: {
				provider_routing: {
					models: { [multi.spec.id]: [multi.policy.rotation[1], "unknown_upstream", multi.policy.rotation[0]] },
					defaults: family ? { [family.policy.routingFamily]: [...family.policy.rotation].reverse() } : {},
					blockedProviders: { [registry[0].spec.id]: [registry[0].policy.rotation[0]], bogus: [1] },
				},
			},
		}),
		settings: json({ settings: { modelPolicy: { allowAllFactoryModels: true } } }),
	},
	{ name: "empty-routed-rotation", flags: json({ flags: allOn, configs: { provider_routing: { models: { [registry[0].spec.id]: [] } } } }), settings: json({ settings: {} }) },
	{ name: "eu-scope", scope: { region: "eu", orgId: "org-eu" }, flags: json({ flags: allOn }), settings: json({ settings: { modelPolicy: {} } }) },
	{ name: "us-inference", scope: { region: "global", inferenceRegion: "us", orgId: "org-us" }, flags: json({ flags: allOn }), settings: json({ settings: { modelPolicy: {} } }) },
	{ name: "flags-rejected", flags: { status: 500, body: "{}" }, settings: json({ settings: {} }) },
	{ name: "flags-without-flags", flags: json({ gates: {} }), settings: json({ settings: {} }) },
	{ name: "flags-array", flags: json({ flags: [] }), settings: json({ settings: {} }) },
	{ name: "settings-unreadable", flags: json({ flags: allOn }), settings: { status: 200, body: "{not json" } },
];
const results: any[] = [];
for (const scenario of scenarios) {
	const requests: any[] = [];
	const fetchImpl = (async (input: any, init: any) => {
		const url = String(input);
		requests.push({ url, headers: Object.fromEntries(new Headers(init?.headers)) });
		const reply = url.endsWith("/api/feature-flags") ? scenario.flags : scenario.settings;
		return new Response(reply.body, { status: reply.status, headers: { "content-type": "application/json" } });
	}) as typeof fetch;
	const models = await fetchFactoryDroidModels({ apiKey: token, fetch: fetchImpl, ...(scenario.scope ?? {}) });
	results.push({ ...scenario, requests, models });
}
const seeds = ["global", "eu", "us"].map(region => {
	const scope = region === "us" ? { region: "global", inferenceRegion: "us" as const } : { region };
	return { scope, models: factoryDroidSeedModels(scope) };
});
await Bun.write(out, Bun.gzipSync(JSON.stringify({ source: "NeoPi OWN fetchFactoryDroidModels / factoryDroidSeedModels", token, scenarios: results, seeds })));
console.log(`${results.length} discovery scenarios, ${seeds.length} seeds`);
