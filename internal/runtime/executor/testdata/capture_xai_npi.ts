// Runs NeoPi's OWN xai-oauth provider (openai-responses over api.x.ai) against
// a loopback capture server only. The bearer is a fake unsigned JWT; no request
// can reach xAI.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamOpenAIResponses } = await import(root + "/pi-ai/src/providers/openai-responses.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { buildXaiOAuthStaticSeed } = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");
const { USER_AGENT } = await import(root + "/pi-utils/src/dirs.ts");

const jwtPart = (value: unknown) => Buffer.from(JSON.stringify(value)).toString("base64url");
const token = `${jwtPart({ alg: "none" })}.${jwtPart({ sub: "xai-capture-subject", exp: 4102444800 })}.fake`;
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
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody: await request.text() });
		return new Response(sse, { headers: { "content-type": "text/event-stream" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;
const seeds = buildXaiOAuthStaticSeed();
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const paid = getBundledModels("xai");
// `provider: "xai"` scenarios run the paid (API key) provider's bundled row.
const seed = (id: string, provider = "xai-oauth") => {
	const found = (provider === "xai" ? paid : seeds).find((model: any) => model.id === id);
	if (!found) throw new Error(`missing ${provider} model ${id}`);
	return found;
};

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const lookupWire = { type: "function", ...lookup };
const unionTool = { name: "pick", description: "Pick one.", parameters: { type: "object", anyOf: [{ properties: { a: { type: "string" } }, required: ["a"] }, { properties: { b: { type: "number" } }, required: ["b"] }] } };
const unionWire = { type: "function", ...unionTool };
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const assistant = (model: string, content: unknown[], items: unknown[]) => ({ role: "assistant", content, api: "openai-responses", provider: "xai-oauth", model, usage, stopReason: "toolUse", timestamp: 1, providerPayload: { type: "openaiResponsesHistory", provider: "xai-oauth", dt: true, items } });
const userText = (text: string) => ({ role: "user", content: [{ type: "input_text", text }] });
const reasoningItem = { type: "reasoning", id: "rs_capture", summary: [], encrypted_content: "enc-capture" };
const callItem = { type: "function_call", id: "fc_capture", call_id: "call_1", name: "lookup", arguments: "{\"term\":\"a\"}" };
// `downstream` is the equivalent Responses request a client sends NeoProxy;
// `options` carries what NeoProxy maps from it onto NeoPi's stream options.
const scenarios: any[] = [
	{ name: "plain-high", model: "grok-4.6", options: { reasoning: "high", temperature: 0.2, maxTokens: 99 }, systemPrompt: ["You are terse."], messages: [{ role: "user", content: "Say hello." }],
		downstream: { model: "grok-4.6", instructions: "You are terse.", input: [userText("Say hello.")], reasoning: { effort: "high" }, stream: true, temperature: 0.2, max_output_tokens: 99, store: true } },
	{ name: "no-effort", model: "grok-4.6", options: {}, messages: [{ role: "user", content: "Say hello." }],
		downstream: { model: "grok-4.6", input: [userText("Say hello.")] } },
	{ name: "xhigh-unmapped", model: "grok-4.7", options: { reasoning: "xhigh" }, messages: [{ role: "user", content: "Think." }],
		downstream: { model: "grok-4.7", input: [userText("Think.")], reasoning: { effort: "xhigh" } } },
	{ name: "minimal-mapped", model: "grok-4.5", options: { reasoning: "minimal" }, messages: [{ role: "user", content: "Quick." }],
		downstream: { model: "grok-4.5", input: [userText("Quick.")], reasoning: { effort: "minimal" } } },
	{ name: "effort-omitted", model: "grok-build-0.1", options: { reasoning: "high" }, messages: [{ role: "user", content: "Build." }],
		downstream: { model: "grok-build-0.1", input: [userText("Build.")], reasoning: { effort: "high" } } },
	{ name: "non-reasoning", model: "grok-4.20-0309-non-reasoning", options: { reasoning: "high", topP: 0.9 }, messages: [{ role: "user", content: "Plain." }],
		downstream: { model: "grok-4.20-0309-non-reasoning", input: [userText("Plain.")], reasoning: { effort: "high" }, top_p: 0.9 } },
	{ name: "tools", model: "grok-4.6", options: { reasoning: "high", toolChoice: "auto" }, tools: [lookup, unionTool], messages: [{ role: "user", content: "Use lookup." }],
		downstream: { model: "grok-4.6", input: [userText("Use lookup.")], tools: [lookupWire, unionWire], tool_choice: "auto", reasoning: { effort: "high" } } },
	{ name: "tool-history-replay", model: "grok-4.6", options: { reasoning: "high" }, tools: [lookup], messages: [
		{ role: "user", content: "Use lookup." },
		assistant("grok-4.6", [{ type: "toolCall", id: "call_1|fc_capture", name: "lookup", arguments: { term: "a" } }], [reasoningItem, callItem]),
		{ role: "toolResult", toolCallId: "call_1|fc_capture", toolName: "lookup", content: [{ type: "text", text: "Found." }], isError: false, timestamp: 2 },
	], downstream: { model: "grok-4.6", input: [userText("Use lookup."), reasoningItem, callItem, { type: "function_call_output", call_id: "call_1", output: "Found." }], tools: [lookupWire], reasoning: { effort: "high" } } },
	{ name: "max-output-clamp", model: "grok-4.6", options: { maxTokens: 900000 }, messages: [{ role: "user", content: "Long." }],
		downstream: { model: "grok-4.6", input: [userText("Long.")], max_output_tokens: 900000 } },
	{ name: "api-key-plain-high", provider: "xai", model: "grok-4.6", options: { reasoning: "high" }, systemPrompt: ["You are terse."], messages: [{ role: "user", content: "Say hello." }],
		downstream: { model: "grok-4.6", instructions: "You are terse.", input: [userText("Say hello.")], reasoning: { effort: "high" } } },
	{ name: "api-key-code-fast", provider: "xai", model: "grok-code-fast-1", options: { reasoning: "high", temperature: 0.3 }, messages: [{ role: "user", content: "Code." }],
		downstream: { model: "grok-code-fast-1", input: [userText("Code.")], reasoning: { effort: "high" }, temperature: 0.3 } },
];
const fixtures: any[] = [];
try {
	// NeoPi resolves compat by host (a loopback base gets a different output
	// clamp and no first-event watchdog), so every fixture uses the official
	// base URL and the fetch redirect keeps traffic on the capture server.
	{
		for (const scenario of scenarios) {
			const baseUrl = "https://api.x.ai/v1";
			const model = buildModel({ ...seed(scenario.model, scenario.provider), baseUrl });
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
			const result = await streamOpenAIResponses(model, context as any, { apiKey: token, sessionId, ...scenario.options, fetch: redirectFetch }).result();
			if (result.stopReason !== "stop") throw new Error(`${scenario.name}: ${result.errorMessage}`);
			fixtures.push({ name: scenario.name, baseUrl, requestedURL, scenario, requests: captured });
		}
	}
	console.log(JSON.stringify({ source: "NeoPi OWN streamOpenAIResponses (xai-oauth)", userAgent: USER_AGENT, token, sessionId, response: sse, fixtures }, null, 2));
} finally {
	server.stop(true);
}
