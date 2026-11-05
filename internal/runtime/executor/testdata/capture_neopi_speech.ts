// Runs NeoPi's OWN synthesizeSpeech (openai-speech transport,
// synthesizeOpenAiSpeech) for the speech rows NeoProxy serves from NeoPi's
// Chat Completions providers, against a loopback server only: every request is
// redirected to 127.0.0.1 and the keys are fakes. Writes
// testdata/neopi_speech.fixtures.json.gz: each scenario's NeoPi request, the
// upstream request NeoPi sent (URL, headers, body), the reply, and the audio
// MIME type or error NeoPi returned.
//
// Run from the NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file>
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const worktree = process.env.NEOPROXY_WORKTREE ?? "/mnt/alias/m2/agents/worktrees/neoproxy-speech";
const outFile = join(worktree, "internal/runtime/executor/testdata/neopi_speech.fixtures.json.gz");

const { synthesizeSpeech } = await import(root + "/pi-ai/src/speech/index.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");

const models = getBundledModels("deepinfra").filter((model: any) => model.api === "openai-speech");
if (models.length === 0) throw new Error("no openai-speech rows");

type Reply = { status: number; body: string; contentType: string };
const audio: Reply = { status: 200, body: "ID3fake-audio-bytes", contentType: "audio/mpeg" };
const scenarios: { name: string; request: any; reply: Reply }[] = [
	{ name: "plain", request: { text: "Hello there.", format: "mp3" }, reply: audio },
	{ name: "voiced", request: { text: "Hello there.", format: "mp3", voice: "af_bella", speed: 1.25, instructions: "Warmly." }, reply: audio },
	{ name: "wav", request: { text: "Hi.", format: "wav", voice: "am_adam" }, reply: { ...audio, contentType: "audio/wav" } },
	{ name: "error", request: { text: "Hi.", format: "mp3" }, reply: { status: 422, body: '{"detail":"bad voice"}', contentType: "application/json" } },
];

let reply: Reply = audio;
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
		return new Response(reply.body, { status: reply.status, headers: { "content-type": reply.contentType } });
	},
});
const local = `http://127.0.0.1:${server.port}`;

const fixtures: any[] = [];
for (const model of models) {
	const apiKey = `fixture-key-${model.provider}-00000001`;
	for (const scenario of scenarios) {
		reply = scenario.reply;
		captured = [];
		const requestedURLs: string[] = [];
		const redirectFetch = (async (input: any, init: any) => {
			const url = new URL(input instanceof Request ? input.url : String(input));
			requestedURLs.push(url.toString());
			const headers = new Headers(init?.headers);
			headers.set("host", url.host);
			return fetch(local + url.pathname + url.search, { ...init, headers, signal: undefined });
		}) as typeof fetch;
		let result: any;
		let error: string | undefined;
		try {
			result = await synthesizeSpeech(model, scenario.request, { apiKey, fetch: redirectFetch });
		} catch (cause) {
			error = String(cause);
		}
		fixtures.push({
			name: `${model.provider}/${model.id}/${scenario.name}`,
			provider: model.provider,
			model: model.id,
			apiKey,
			request: scenario.request,
			reply: scenario.reply,
			requests: captured.map((request, i) => ({ ...request, url: requestedURLs[i] })),
			...(error ? { error } : { mimeType: result.mimeType, audio: new TextDecoder().decode(result.audio) }),
		});
	}
}
server.stop(true);
await Bun.write(outFile, Bun.gzipSync(JSON.stringify({ source: "NeoPi OWN synthesizeSpeech over openai-speech rows", fixtures })));
console.error(`${fixtures.length} speech fixtures, ${fixtures.filter(f => f.error).length} errors`);
