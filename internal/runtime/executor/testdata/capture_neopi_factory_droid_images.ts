// Runs NeoPi's OWN generateFactoryDroidImage over every Factory Droid
// generate_image registry model, built for a signed-in account, against a
// loopback capture server only; the bearer is fake and no request can reach
// Factory. Each scenario is recorded beside the OpenAI Images request a
// client sends NeoProxy for it and the images NeoPi decoded from the reply.
// Writes testdata/neopi_factory_droid/images.json.gz.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_factory_droid_images.ts
import { mkdirSync } from "node:fs";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { generateFactoryDroidImage } = await import(root + "/pi-ai/src/images/factory-droid.ts");
const { factoryDroidRegistry } = await import(root + "/pi-catalog/src/compat/factory-droid.ts");
const { buildFactoryDroidModel } = await import(root + "/pi-catalog/src/discovery/factory-droid.ts");

const jwt = (payload: unknown) => "eyJhbGciOiJub25lIn0." + Buffer.from(JSON.stringify(payload)).toString("base64url") + ".fake";
const apiKey = jwt({ sub: "user_fixture", external_org_id: "org-fixture", exp: 4102444800 });
const identity = { orgId: "org-fixture", region: "global", inferenceRegion: "global" };
const sessionId = "capture-session-factory-droid-0001";

const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
const jpeg = "/9j/4AAQSkZJRgABAQEASABIAAD/2wBDAP//////////////////////////////////////////////////////////////////////////////////////wgALCAABAAEBAREA/8QAFBABAAAAAAAAAAAAAAAAAAAAAP/aAAgBAQABPxA=";
const imagesReply = JSON.stringify({ created: 1, data: [{ b64_json: png }], usage: { input_tokens: 7, output_tokens: 11 } });
const geminiReply =
	`data: ${JSON.stringify({ candidates: [{ content: { role: "model", parts: [{ text: "Here it is." }] } }] })}\n\n` +
	`data: ${JSON.stringify({ candidates: [{ content: { role: "model", parts: [{ inlineData: { mimeType: "image/png", data: png } }] }, finishReason: "STOP" }], usageMetadata: { promptTokenCount: 7, candidatesTokenCount: 11, totalTokenCount: 18 } })}\n\n`;

let captured: any[] = [];
let gemini = false;
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody: await request.text() });
		return gemini
			? new Response(geminiReply, { headers: { "content-type": "text/event-stream" } })
			: new Response(imagesReply, { headers: { "content-type": "application/json" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;
const outDir = join(import.meta.dir, "neopi_factory_droid");
mkdirSync(outDir, { recursive: true });

const dataURL = (image: { data: string; mimeType: string }) => `data:${image.mimeType};base64,${image.data}`;

try {
	const registry = factoryDroidRegistry().filter((entry: any) => entry.policy.image);
	const fixtures: any[] = [];
	for (const entry of registry) {
		const model = buildFactoryDroidModel(entry, { ...identity, apiProviders: entry.policy.rotation });
		gemini = entry.policy.wire === "google-generate";
		const scenarios: { name: string; request: any; downstream: any; endpoint: string }[] = [];
		const add = (name: string, request: any) => {
			const downstream: any = { model: model.id, prompt: request.prompt };
			if (request.count !== undefined) downstream.n = request.count;
			if (request.inputImages) downstream.images = request.inputImages.map(dataURL);
			if (request.aspectRatio || request.imageSize) {
				downstream.image_config = {
					...(request.aspectRatio ? { aspect_ratio: request.aspectRatio } : {}),
					...(request.imageSize ? { image_size: request.imageSize } : {}),
				};
			}
			const endpoint = request.inputImages ? "/v1/images/edits" : "/v1/images/generations";
			scenarios.push({ name: `${model.id}/${name}`, request, downstream, endpoint });
		};
		add("generate", { prompt: "A red square." });
		add("edit", { prompt: "Make it blue.", inputImages: [{ data: png, mimeType: "image/png" }, { data: jpeg, mimeType: "IMAGE/JPEG" }] });
		if (gemini) {
			add("aspect-size", { prompt: "A red square.", aspectRatio: "16:9", imageSize: "2K" });
			add("aspect-only", { prompt: "A red square.", aspectRatio: "1:1" });
		} else {
			add("count", { prompt: "A red square.", count: 2 });
		}
		for (const scenario of scenarios) {
			captured = [];
			let requestedURL = "";
			const redirectFetch = (async (input: any, init: any) => {
				requestedURL = input instanceof Request ? input.url : String(input);
				const u = new URL(requestedURL);
				if (u.origin !== "https://api.factory.ai") throw new Error("Unexpected non-capture request " + requestedURL);
				const headers = new Headers(init?.headers);
				headers.set("host", u.host);
				return fetch(local + u.pathname + u.search, { ...init, headers, tls: undefined });
			}) as typeof fetch;
			const result = await generateFactoryDroidImage(model, scenario.request, { apiKey, sessionId, fetch: redirectFetch });
			if (captured.length !== 1) throw new Error(`Capture ${scenario.name} saw ${captured.length} requests`);
			fixtures.push({
				name: scenario.name,
				model: model.id,
				endpoint: scenario.endpoint,
				downstream: scenario.downstream,
				images: result.images.map((image: any) => image.data),
				requestedURL,
				...captured[0],
			});
		}
	}
	await Bun.write(join(outDir, "images.json.gz"), Bun.gzipSync(JSON.stringify({ source: "NeoPi OWN generateFactoryDroidImage over factoryDroidRegistry image rows", apiKey, sessionId, identity, fixtures })));
	console.log(`images: ${registry.length} models, ${fixtures.length} fixtures`);
} finally {
	server.stop(true);
}
