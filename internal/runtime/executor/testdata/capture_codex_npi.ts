// Runs NeoPi's OWN Codex provider against a loopback capture server only.
// The bearer is a fake unsigned JWT; no request can reach OpenAI.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamOpenAICodexResponses } = await import(root + "/pi-ai/src/providers/openai-codex-responses.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { fetchCodexModels } = await import(root + "/pi-catalog/src/discovery/codex.ts");
const { USER_AGENT } = await import(root + "/pi-utils/src/dirs.ts");
import { readFile } from "node:fs/promises";
import { homedir } from "node:os";

const accountId = "account-capture-fake";
const jwtPart = (value: unknown) => Buffer.from(JSON.stringify(value)).toString("base64url");
const token = `${jwtPart({ alg: "none" })}.${jwtPart({ "https://api.openai.com/auth": { chatgpt_account_id: accountId } })}.fake`;
const sessionId = "capture-session-0001";
const events = [
	{ type: "response.created", response: { id: "resp_capture", status: "in_progress", output: [] } },
	{ type: "response.output_item.added", output_index: 0, item: { id: "msg_capture", type: "message", role: "assistant", status: "in_progress", content: [] } },
	{ type: "response.content_part.added", item_id: "msg_capture", output_index: 0, content_index: 0, part: { type: "output_text", text: "", annotations: [] } },
	{ type: "response.output_text.delta", item_id: "msg_capture", output_index: 0, content_index: 0, delta: "Captured." },
	{ type: "response.output_text.done", item_id: "msg_capture", output_index: 0, content_index: 0, text: "Captured." },
	{ type: "response.output_item.done", output_index: 0, item: { id: "msg_capture", type: "message", role: "assistant", status: "completed", content: [{ type: "output_text", text: "Captured.", annotations: [] }] } },
	{ type: "response.completed", response: { id: "resp_capture", status: "completed", output: [{ id: "msg_capture", type: "message", role: "assistant", status: "completed", content: [{ type: "output_text", text: "Captured.", annotations: [] }] }], usage: { input_tokens: 3, output_tokens: 1, total_tokens: 4 } } },
];
const sse = events.map(event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join("");
let captured: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const raw = new Uint8Array(await request.arrayBuffer());
		const encoding = request.headers.get("content-encoding");
		const text = encoding === "zstd" ? new TextDecoder().decode(Bun.zstdDecompressSync(raw)) : new TextDecoder().decode(raw);
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody: text });
		return new Response(sse, { headers: { "content-type": "text/event-stream" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;
// The account roster entry mirrors the source discovery capture; the model is
// materialized by NeoPi's own discovery, as NeoProxy registers it.
const terraEntry = { slug: "gpt-6.1-terra", display_name: "GPT-6.1 Terra", context_window: 500000, max_context_window: 922000, supported_reasoning_levels: [{ effort: "low" }, { effort: "medium" }, { effort: "high" }, { effort: "xhigh" }], prefer_websockets: true, priority: 1, service_tiers: [{ id: "priority" }] };
const discovery = await fetchCodexModels({ accessToken: token, accountId, fetchFn: Object.assign(async () => Response.json({ models: [terraEntry] }), { preconnect() {} }) as any });
const bundled = discovery?.models.find((model: any) => model.id === "gpt-6.1-terra");
if (!bundled) throw new Error("discovered gpt-6.1-terra is missing");

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const lookupWire = { type: "function", ...lookup };
const assistant = (content: unknown[]) => ({ role: "assistant", content, api: "openai-codex-responses", provider: "openai-codex", model: "gpt-6.1-terra", usage: { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } }, stopReason: "toolUse", timestamp: 1 });
const userText = (text: string) => ({ role: "user", content: [{ type: "input_text", text }] });
// User messages carry no timestamp: a proxy has none, so both sides use Date.now().
// `downstream` is the equivalent Responses request a client sends NeoProxy.
const scenarios: any[] = [
	{ name: "plain-high", reasoning: "high", messages: [{ role: "user", content: "Say hello." }], systemPrompt: ["You are terse."],
		downstream: { model: "gpt-6.1-terra", instructions: "You are terse.", input: [userText("Say hello.")], reasoning: { effort: "high" }, stream: true, temperature: 0.2, max_output_tokens: 99, store: true } },
	{ name: "no-effort", messages: [{ role: "user", content: "Say hello." }],
		downstream: { model: "gpt-6.1-terra", input: [userText("Say hello.")] } },
	{ name: "custom-tool", reasoning: "high", tools: [lookup], messages: [{ role: "user", content: "Use lookup." }],
		downstream: { model: "gpt-6.1-terra", input: [userText("Use lookup.")], tools: [lookupWire], reasoning: { effort: "high" } } },
	{ name: "tool-history", reasoning: "high", tools: [lookup], messages: [
		{ role: "user", content: "Use lookup." },
		assistant([{ type: "toolCall", id: "call:1#🦊", name: "lookup", arguments: { term: "a" } }]),
		{ role: "toolResult", toolCallId: "call:1#🦊", toolName: "lookup", content: [{ type: "text", text: "Found." }], isError: false, timestamp: 2 },
	], downstream: { model: "gpt-6.1-terra", input: [userText("Use lookup."), { type: "function_call", call_id: "call:1#🦊", name: "lookup", arguments: "{\"term\":\"a\"}" }, { type: "function_call_output", call_id: "call:1#🦊", output: "Found." }], tools: [lookupWire], reasoning: { effort: "high" } } },
	{ name: "developer-prompts", reasoning: "medium", systemPrompt: ["First.", "Second."], messages: [{ role: "user", content: "Hi." }],
		downstream: { model: "gpt-6.1-terra", instructions: "First.", input: [{ type: "message", role: "developer", content: [{ type: "input_text", text: "Second." }] }, userText("Hi.")], reasoning: { effort: "medium" } } },
	{ name: "clamped-max-and-text", reasoning: "max", textVerbosity: "low", messages: [{ role: "user", content: "Hi." }],
		downstream: { model: "gpt-6.1-terra", input: [userText("Hi.")], reasoning: { effort: "max" }, text: { verbosity: "low" } } },
];
const fixtures: any[] = [];
try {
	for (const official of [false, true]) {
		const baseUrl = official ? "https://chatgpt.com/backend-api" : local;
		const model = buildModel({ ...bundled, baseUrl });
		for (const scenario of scenarios) {
			captured = [];
			let requestedURL = "";
			const redirectFetch = (async (input: any, init: any) => {
				requestedURL = input instanceof Request ? input.url : String(input);
				const target = new URL(requestedURL);
				const headers = new Headers(init?.headers);
				headers.set("host", target.host);
				return fetch(local + target.pathname + target.search, { ...init, headers });
			}) as typeof fetch;
			const context = { messages: scenario.messages, ...(scenario.tools ? { tools: scenario.tools } : {}), ...(scenario.systemPrompt ? { systemPrompt: scenario.systemPrompt } : {}) };
			const result = await streamOpenAICodexResponses(model, context as any, {
				apiKey: token,
				sessionId,
				...(scenario.reasoning ? { reasoning: scenario.reasoning } : {}),
				...(scenario.textVerbosity ? { textVerbosity: scenario.textVerbosity } : {}),
				fetch: redirectFetch,
			}).result();
			if (result.stopReason !== "stop") throw new Error(`${scenario.name}: ${result.errorMessage}`);
			fixtures.push({ name: (official ? "official/" : "local/") + scenario.name, transport: "sse", baseUrl, requestedURL, scenario, requests: captured });
		}
	}
	const installId = (await readFile(homedir() + "/.omp/install-id", "utf8")).trim();
	console.log(JSON.stringify({ source: "NeoPi OWN streamOpenAICodexResponses", userAgent: USER_AGENT, installId, accountId, token, sessionId, response: sse, discovery: { models: [terraEntry] }, model: bundled, fixtures }, null, 2));
} finally {
	server.stop(true);
}
