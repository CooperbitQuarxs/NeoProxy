// Runs NeoPi's OWN Codex provider with provider session state (its default
// WebSocket transport) against a loopback capture server. Fake bearer only.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamOpenAICodexResponses } = await import(root + "/pi-ai/src/providers/openai-codex-responses.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { fetchCodexModels } = await import(root + "/pi-catalog/src/discovery/codex.ts");
import { readFile } from "node:fs/promises";
import { homedir } from "node:os";

const accountId = "account-capture-fake";
const jwtPart = (value: unknown) => Buffer.from(JSON.stringify(value)).toString("base64url");
const token = `${jwtPart({ alg: "none" })}.${jwtPart({ "https://api.openai.com/auth": { chatgpt_account_id: accountId } })}.fake`;
const sessionId = "capture-session-ws-0001";
let responseCounter = 0;
const events = (id: string, text: string) => [
	{ type: "response.created", response: { id, status: "in_progress", output: [] } },
	{ type: "response.output_item.added", output_index: 0, item: { id: `msg_${id}`, type: "message", role: "assistant", status: "in_progress", content: [] } },
	{ type: "response.content_part.added", item_id: `msg_${id}`, output_index: 0, content_index: 0, part: { type: "output_text", text: "", annotations: [] } },
	{ type: "response.output_text.delta", item_id: `msg_${id}`, output_index: 0, content_index: 0, delta: text },
	{ type: "response.output_text.done", item_id: `msg_${id}`, output_index: 0, content_index: 0, text },
	{ type: "response.output_item.done", output_index: 0, item: { id: `msg_${id}`, type: "message", role: "assistant", status: "completed", content: [{ type: "output_text", text, annotations: [] }] } },
	{ type: "response.completed", response: { id, status: "completed", output: [{ id: `msg_${id}`, type: "message", role: "assistant", status: "completed", content: [{ type: "output_text", text, annotations: [] }] }], usage: { input_tokens: 3, output_tokens: 1, total_tokens: 4 } } },
];
let handshakes: any[] = [];
let frames: any[] = [];
// The steered turn holds its response open until the steer arrives, then
// rejects it, so the pump sends exactly one frame and the turn completes.
let steerNext = false;
let finishSteered: (() => void) | undefined;
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	fetch(request, srv) {
		const url = new URL(request.url);
		handshakes.push({ path: url.pathname + url.search, headers: Object.fromEntries(request.headers) });
		if (srv.upgrade(request)) return undefined;
		return new Response("upgrade required", { status: 426 });
	},
	websocket: {
		message(ws, message) {
			const text = typeof message === "string" ? message : new TextDecoder().decode(message);
			frames.push(text);
			const frame = JSON.parse(text);
			if (frame.type === "response.steer") {
				ws.send(JSON.stringify({ type: "response.steer.failed", steer: { previous_response_id: frame.previous_response_id }, error: { code: "capture_rejected", message: "Captured." } }));
				finishSteered?.();
				finishSteered = undefined;
				return;
			}
			if (frame.type !== "response.create") return;
			responseCounter++;
			const all = events(`resp_${responseCounter}`, "Captured.");
			if (steerNext) {
				steerNext = false;
				for (const event of all.slice(0, 4)) ws.send(JSON.stringify(event));
				finishSteered = () => {
					for (const event of all.slice(4)) ws.send(JSON.stringify(event));
				};
				return;
			}
			for (const event of all) ws.send(JSON.stringify(event));
		},
	},
});
const local = `http://127.0.0.1:${server.port}`;
const terraEntry = { slug: "gpt-6.1-terra", display_name: "GPT-6.1 Terra", context_window: 500000, max_context_window: 922000, supported_reasoning_levels: [{ effort: "low" }, { effort: "medium" }, { effort: "high" }, { effort: "xhigh" }], prefer_websockets: true, priority: 1, service_tiers: [{ id: "priority" }] };
const discovery = await fetchCodexModels({ accessToken: token, accountId, fetchFn: Object.assign(async () => Response.json({ models: [terraEntry] }), { preconnect() {} }) as any });
const raw = discovery?.models.find((model: any) => model.id === "gpt-6.1-terra");
const model = buildModel({ ...raw, baseUrl: local });
const userText = (text: string) => ({ role: "user", content: [{ type: "input_text", text }] });
const reply = (id: string) => ({ role: "assistant", content: [{ type: "text", text: "Captured.", textSignature: JSON.stringify({ v: 1, id: `msg_${id}` }) }], api: "openai-codex-responses", provider: "openai-codex", model: "gpt-6.1-terra", usage: { input: 3, output: 1, totalTokens: 4, cost: { input: 0, output: 0, total: 0 } }, stopReason: "stop", timestamp: 1 });
const outputItem = (id: string) => ({ id: `msg_${id}`, type: "message", role: "assistant", status: "completed", content: [{ type: "output_text", text: "Captured.", annotations: [] }] });

const providerSessionState = new Map();
const turns: any[] = [];
try {
	const conversation: any[] = [];
	const downstreamInput: any[] = [];
	for (const [index, prompt, effort, steer] of [[1, "Say hello.", "high", ""], [2, "Again.", "high", ""], [3, "Think less.", "low", ""], [4, "Count slowly.", "low", "Change course."]] as const) {
		handshakes = [];
		frames = [];
		steerNext = steer !== "";
		let claimed = false;
		let steerOutcome = "";
		const liveSteering = steer === "" ? undefined : {
			async wait() {},
			async claim(signal: AbortSignal) {
				if (claimed) {
					await new Promise(resolve => signal.addEventListener("abort", resolve, { once: true }));
					return undefined;
				}
				claimed = true;
				return { messages: [{ role: "user", content: steer, timestamp: 1 }], accept() { steerOutcome = "accepted"; }, reject() { steerOutcome = "rejected"; } };
			},
		};
		conversation.push({ role: "user", content: prompt });
		downstreamInput.push(userText(prompt));
		const result = await streamOpenAICodexResponses(model, { systemPrompt: ["You are terse."], messages: [...conversation] } as any, {
			apiKey: token,
			sessionId,
			reasoning: effort,
			providerSessionState,
			liveSteering,
		} as any).result();
		if (result.stopReason !== "stop") throw new Error(`turn ${index}: ${result.errorMessage}`);
		if (steer !== "" && steerOutcome !== "rejected") throw new Error(`turn ${index}: steering ${steerOutcome || "never claimed"}`);
		turns.push({ name: `ws-turn-${index}`, downstream: { model: "gpt-6.1-terra", instructions: "You are terse.", input: [...downstreamInput], reasoning: { effort } }, handshakes, frames, ...(steer !== "" ? { steerOutcome } : {}) });
		conversation.push(result);
		downstreamInput.push(outputItem(`resp_${index}`));
	}
	const installId = (await readFile(homedir() + "/.omp/install-id", "utf8")).trim();
	console.log(JSON.stringify({ source: "NeoPi OWN streamOpenAICodexResponses (WebSocket)", installId, accountId, token, sessionId, baseUrl: local, discovery: { models: [terraEntry] }, turns }, null, 2));
} finally {
	for (const state of providerSessionState.values()) (state as any).close?.();
	server.stop(true);
}
