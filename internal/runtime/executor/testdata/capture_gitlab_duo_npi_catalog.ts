// Exports NeoPi's OWN bundled GitLab Duo rows for NeoProxy's registry. Each
// row is the catalog entry a client names (getBundledModels("gitlab-duo")),
// with the model streamGitLabDuo actually streams with: the route
// apiRouteFor("gitlab-duo", id) names, the upstream id the GitLab proxy
// takes (resolveGitLabDuoModelIdentity), and buildModel's re-resolved
// thinking and compat for that route (compat: model.compatConfig).
// Writes internal/registry/models/gitlab_duo_neopi.json.gz.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_gitlab_duo_npi_catalog.ts
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { apiRouteFor } = await import(root + "/pi-catalog/src/compat/behavior.ts");
const { resolveGitLabDuoModelIdentity } = await import(root + "/pi-catalog/src/provider-models/special.ts");

const ANTHROPIC_PROXY_URL = "https://cloud.gitlab.com/ai/v1/proxy/anthropic/";
const OPENAI_PROXY_URL = "https://cloud.gitlab.com/ai/v1/proxy/openai/v1";

const models = getBundledModels("gitlab-duo").map((model: any) => {
	const identity = resolveGitLabDuoModelIdentity(model.id);
	if (!identity) throw new Error(`no GitLab Duo identity for ${model.id}`);
	const api = apiRouteFor("gitlab-duo", model.id)?.api;
	if (api !== "anthropic-messages" && api !== "openai-completions" && api !== "openai-responses") throw new Error(`no GitLab Duo route for ${model.id}`);
	// streamGitLabDuo's inner model.
	const inner = buildModel({ ...model, id: identity.upstreamModelId, api, baseUrl: api === "anthropic-messages" ? ANTHROPIC_PROXY_URL : OPENAI_PROXY_URL, compat: model.compatConfig });
	return {
		id: model.id,
		name: model.name,
		api,
		provider: model.provider,
		baseUrl: inner.baseUrl,
		reasoning: inner.reasoning,
		input: inner.input,
		contextWindow: inner.contextWindow ?? 0,
		maxTokens: inner.maxTokens ?? 0,
		requestModelId: identity.upstreamModelId,
		...(inner.thinking ? { thinking: inner.thinking } : {}),
		compat: inner.compat,
		cost: { input: model.cost?.input ?? 0, output: model.cost?.output ?? 0, cacheRead: model.cost?.cacheRead ?? 0, cacheWrite: model.cost?.cacheWrite ?? 0 },
	};
});
const out = join(import.meta.dir, "..", "..", "..", "registry", "models", "gitlab_duo_neopi.json.gz");
await Bun.write(out, Bun.gzipSync(JSON.stringify({ source: 'NeoPi OWN getBundledModels("gitlab-duo") with streamGitLabDuo inner models', models })));
console.log(`gitlab-duo catalog: ${models.length} rows`);
