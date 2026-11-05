// Runs NeoPi's OWN generateImage (openai-images transport, generateOpenAIImage)
// for the image rows NeoProxy serves from NeoPi's Chat Completions providers,
// against a loopback server only: every request is redirected to 127.0.0.1 and
// the keys are fakes. Writes testdata/neopi_images.fixtures.json.gz: each
// scenario's NeoPi request, the upstream requests NeoPi sent (URL, headers,
// body) and the replies they got.
//
// Run from the NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file>
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const worktree = process.env.NEOPROXY_WORKTREE ?? "/mnt/alias/m2/agents/worktrees/neoproxy-singularity";
const outFile = join(worktree, "internal/runtime/executor/testdata/neopi_images.fixtures.json.gz");

const { generateImage } = await import(root + "/pi-ai/src/images/index.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { PROVIDER_DESCRIPTORS } = await import(root + "/pi-catalog/src/provider-models/descriptors.ts");
const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
const cacheDir = mkdtempSync(join(tmpdir(), "neopi-images-"));

const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";

// SingularityAPI's image rows come from its live roster: the capabilities
// listing moves them onto openai-images.
async function singularityImageRows(): Promise<any[]> {
	const descriptor = PROVIDER_DESCRIPTORS.find((entry: any) => entry.providerId === "singularityapi-dev");
	const listing = JSON.stringify({
		data: [
			{ id: "flux-1-schnell", name: "FLUX.1 schnell", capabilities: [{ endpoint: "/v1/images/generations" }] },
			{ id: "gpt-image-2", capabilities: [{ endpoint: "/v1/images/generations" }] },
		],
	});
	const options = descriptor.createModelManagerOptions({
		apiKey: "sk-sapi-fixturekey00000001",
		fetch: async () => new Response(listing, { status: 200, headers: { "content-type": "application/json" } }),
	});
	options.cacheDbPath = join(cacheDir, "singularityapi-dev.db");
	const resolved = await resolveProviderModels(options, "online");
	return resolved.models.filter((model: any) => model.api === "openai-images");
}

const models = [
	...(await singularityImageRows()),
	...getBundledModels("deepinfra").filter((model: any) => model.api === "openai-images"),
];
if (models.length < 3) throw new Error(`expected 3 image rows, got ${models.length}`);

type Reply = { status: number; body: unknown };
const ok: Reply = { status: 200, body: { created: 1, data: [{ b64_json: png }], usage: { input_tokens: 3, output_tokens: 5 } } };
const scenarios: { name: string; request: any; replies: Reply[] }[] = [
	{ name: "generate", request: { prompt: "A red cube." }, replies: [ok] },
	{ name: "generate-sized", request: { prompt: "Two blue spheres.", count: 2, imageSize: "1536x1024" }, replies: [ok] },
	{ name: "edit", request: { prompt: "Make it green.", inputImages: [{ data: png, mimeType: "image/png" }] }, replies: [ok] },
	{
		name: "edit-fallback",
		request: { prompt: "Make it green.", inputImages: [{ data: png, mimeType: "image/png" }] },
		replies: [{ status: 404, body: { error: { message: "no edits" } } }, ok],
	},
	{ name: "error", request: { prompt: "Bad." }, replies: [{ status: 400, body: { error: { message: "bad prompt" } } }] },
];

let replies: Reply[] = [];
let captured: any[] = [];
const server = Bun.serve({
	port: 0,
	async fetch(request) {
		captured.push({
			path: new URL(request.url).pathname,
			method: request.method,
			headers: Object.fromEntries(request.headers),
			body: await request.text(),
		});
		const reply = replies[Math.min(captured.length - 1, replies.length - 1)];
		return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "content-type": "application/json" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;

const apiKeyFor = (provider: string) => (provider === "singularityapi-dev" ? "sk-sapi-fixturekey00000001" : `fixture-key-${provider}-00000001`);

const fixtures: any[] = [];
for (const model of models) {
	for (const scenario of scenarios) {
		replies = scenario.replies;
		captured = [];
		const requestedURLs: string[] = [];
		const redirectFetch = (async (input: any, init: any) => {
			const url = new URL(input instanceof Request ? input.url : String(input));
			requestedURLs.push(url.toString());
			const headers = new Headers(init?.headers);
			headers.set("host", url.host);
			return fetch(local + url.pathname + url.search, { ...init, headers });
		}) as typeof fetch;
		let result: any;
		let error: string | undefined;
		try {
			result = await generateImage(model, scenario.request, { apiKey: apiKeyFor(model.provider), fetch: redirectFetch });
		} catch (cause) {
			error = String(cause);
		}
		fixtures.push({
			name: `${model.provider}/${model.id}/${scenario.name}`,
			provider: model.provider,
			model: model.id,
			apiKey: apiKeyFor(model.provider),
			request: scenario.request,
			replies: scenario.replies.map(reply => ({ status: reply.status, body: JSON.stringify(reply.body) })),
			requests: captured.map((request, i) => ({ ...request, url: requestedURLs[i] })),
			...(error ? { error } : { images: result.images.length }),
		});
	}
}
server.stop(true);
await Bun.write(outFile, Bun.gzipSync(JSON.stringify({ source: "NeoPi OWN generateImage over openai-images rows", fixtures })));
console.error(`${fixtures.length} image fixtures, ${fixtures.filter(f => f.error).length} errors`);
