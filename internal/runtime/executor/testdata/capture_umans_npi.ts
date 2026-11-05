// Runs NeoPi's OWN umans provider through streamSimple (the agent entry)
// against a loopback capture server only; the key is fake and no request can
// reach Umans. Rows are NeoPi's bundled umans catalog plus the rows its umans
// discovery resolves from testdata/umans_models_info.json, so every thinking
// mode (budget, requires-effort, anthropic-budget-effort) is exercised.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
if (process.env.UMANS_WEBSEARCH_PROVIDER !== undefined) throw new Error("unset UMANS_WEBSEARCH_PROVIDER for the capture");
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { umansModelManagerOptions } = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");

const apiKey = "sk-umans-capture-000000000000000000000000";
const sessionId = "capture-session-umans-0001";
const events = [
	{ type: "message_start", message: { id: "msg_capture", type: "message", role: "assistant", model: "umans", content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 3, output_tokens: 0 } } },
	{ type: "content_block_start", index: 0, content_block: { type: "text", text: "" } },
	{ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "Captured." } },
	{ type: "content_block_stop", index: 0 },
	{ type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 1 } },
	{ type: "message_stop" },
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

const infoPayload = await Bun.file(import.meta.dir + "/umans_models_info.json").text();
const discovery = umansModelManagerOptions({ apiKey, fetch: (async () => new Response(infoPayload)) as typeof fetch });
const discovered = new Map((await discovery.fetchDynamicModels!()).map((spec: any) => [spec.id, buildModel(spec)]));
const bundled = new Map(getBundledModels("umans").map((model: any) => [model.id, model]));
const row = (source: "bundled" | "discovered", id: string) => {
	const model = (source === "bundled" ? bundled : discovered).get(id);
	if (!model) throw new Error(`no ${source} umans row ${id}`);
	return model;
};

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const webSearch = { name: "web_search", description: "Search the web.", parameters: { type: "object", properties: { query: { type: "string" } }, required: ["query"] } };
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";

type Turn = { kind: "user"; text?: string; blocks?: any[] } | { kind: "call"; tool?: string } | { kind: "result"; blocks: any[]; isError?: boolean; tool?: string };
const toolArgs = (tool: string) => (tool === "web_search" ? { query: "a" } : { term: "a" });
const claudeTurns = (turns: Turn[]) =>
	turns.map(turn => {
		if (turn.kind === "user") return { role: "user", content: turn.text ?? turn.blocks };
		if (turn.kind === "call") return { role: "assistant", content: [{ type: "thinking", thinking: "Need a tool.", signature: "" }, { type: "tool_use", id: "call_capture_1", name: turn.tool ?? "lookup", input: toolArgs(turn.tool ?? "lookup") }] };
		return { role: "user", content: [{ type: "tool_result", tool_use_id: "call_capture_1", content: turn.blocks, ...(turn.isError ? { is_error: true } : {}) }] };
	});
const neopiBlock = (b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image", data: b.source.data, mimeType: b.source.media_type });
const neopiTurns = (model: any, turns: Turn[]) =>
	turns.map(turn => {
		if (turn.kind === "user") return { role: "user", content: turn.text ?? turn.blocks!.map(neopiBlock), timestamp: 0 };
		if (turn.kind === "call")
			return {
				role: "assistant",
				content: [
					{ type: "thinking", thinking: "Need a tool.", thinkingSignature: "" },
					{ type: "toolCall", id: "call_capture_1", name: turn.tool ?? "lookup", arguments: toolArgs(turn.tool ?? "lookup") },
				],
				api: model.api,
				provider: "umans",
				model: model.id,
				usage,
				stopReason: "toolUse",
				timestamp: 0,
			};
		return { role: "toolResult", toolCallId: "call_capture_1", toolName: turn.tool ?? "lookup", content: turn.blocks.map(neopiBlock), isError: !!turn.isError, timestamp: 0 };
	});

const scenarios: any[] = [];
type Extra = { system?: string; tools?: any[]; webSearchProvider?: string };
const add = (source: "bundled" | "discovered", id: string, name: string, turns: Turn[], options: any, extra: Extra = {}) => {
	const model = row(source, id);
	const context = { ...(extra.system ? { systemPrompt: [extra.system] } : {}), messages: neopiTurns(model, turns), ...(extra.tools ? { tools: extra.tools } : {}) };
	const downstream: any = { model: id, messages: claudeTurns(turns), stream: true };
	if (extra.system) downstream.system = extra.system;
	if (extra.tools) downstream.tools = extra.tools.map(tool => ({ name: tool.name, description: tool.description, input_schema: tool.parameters }));
	if (options.maxTokens) downstream.max_tokens = options.maxTokens;
	if (options.temperature !== undefined) downstream.temperature = options.temperature;
	if (options.disableReasoning) downstream.thinking = { type: "disabled" };
	else if (options.reasoning) Object.assign(downstream, { thinking: { type: "adaptive" }, output_config: { effort: options.reasoning } });
	if (options.toolChoice === "any") downstream.tool_choice = { type: "any" };
	else if (options.toolChoice?.type === "tool") downstream.tool_choice = { type: "tool", name: options.toolChoice.name };
	const headers = extra.webSearchProvider ? { "X-Umans-Websearch-Provider": extra.webSearchProvider } : undefined;
	scenarios.push({ name: `${source}/${id}/${name}`, source, model: id, context, options: { ...options, ...(headers ? { headers } : {}) }, downstream, downstreamHeaders: headers ?? {} });
};
const hello: Turn[] = [{ kind: "user", text: "Say hello." }];
const askTurns: Turn[] = [{ kind: "user", text: "Use a tool." }];
const history = (blocks: any[], isError = false, tool = "lookup"): Turn[] => [{ kind: "user", text: "Use a tool." }, { kind: "call", tool }, { kind: "result", blocks, isError, tool }];
const text = (value: string) => ({ type: "text", text: value });
const image = { type: "image", source: { type: "base64", media_type: "image/png", data: png } };

const rows: ["bundled" | "discovered", string][] = [
	["bundled", "umans-coder"],
	["bundled", "umans-kimi-k3"],
	["bundled", "umans-deepseek-v4-flash-0731"],
	["bundled", "umans-glm-5.3"],
	["discovered", "umans-glm-5.2"],
	["discovered", "umans-glm-5.3"],
	["discovered", "umans-flash"],
];
for (const [source, id] of rows) {
	const model = row(source, id);
	const efforts: string[] = model.thinking?.efforts ?? [];
	const first = efforts[0];
	add(source, id, "plain", hello, {});
	for (const effort of efforts) add(source, id, `effort-${effort}`, hello, { reasoning: effort });
	add(source, id, "off", hello, { disableReasoning: true });
	add(source, id, "off-sampling", hello, { disableReasoning: true, temperature: 0.3, maxTokens: 300 });
	// A caller's max_tokens is the output it wants; thinking's budget goes on
	// top, within the model cap.
	for (const effort of efforts) add(source, id, `max-tokens-${effort}`, hello, { reasoning: effort, maxTokens: 8192 });
	add(source, id, "system-tool", askTurns, { reasoning: first, maxTokens: 512, temperature: 0.2 }, { system: "A source system.", tools: [lookup] });
	add(source, id, "tool-history", history([text("Found.")]), { reasoning: first }, { tools: [lookup] });
	add(source, id, "tool-result-blocks", history([text("Line one."), text("  "), text("Line two.")]), { reasoning: first }, { tools: [lookup] });
	add(source, id, "tool-result-empty-error", history([], true), { reasoning: first }, { tools: [lookup] });
	add(source, id, "forced-any", askTurns, { reasoning: first, toolChoice: "any" }, { tools: [lookup] });
	add(source, id, "forced-named", askTurns, { reasoning: first, toolChoice: { type: "tool", name: "lookup" } }, { tools: [lookup] });
	add(source, id, "user-image", [{ kind: "user", blocks: [text("Describe."), image] }], { reasoning: first });
	add(source, id, "tool-result-image", history([text("Shot."), image]), { reasoning: first }, { tools: [lookup] });
	// Built-in tool names: escaped like any other unless Umans' gateway web
	// search is selected, which keeps the web_search definition and choice.
	add(source, id, "builtin-escaped", askTurns, { reasoning: first, toolChoice: { type: "tool", name: "web_search" } }, { tools: [lookup, webSearch] });
	add(source, id, "gateway-web-search", askTurns, { reasoning: first, toolChoice: { type: "tool", name: "web_search" } }, { tools: [lookup, webSearch], webSearchProvider: "exa" });
	add(source, id, "gateway-web-search-history", history([text("Results.")], false, "web_search"), { reasoning: first }, { tools: [lookup, webSearch], webSearchProvider: " Native " });
	add(source, id, "gateway-invalid-provider", askTurns, { reasoning: first }, { tools: [webSearch], webSearchProvider: "bing" });
}

const fixtures: any[] = [];
try {
	for (const scenario of scenarios) {
		captured = [];
		const model = row(scenario.source, scenario.model);
		let requestedURL = "";
		const redirectFetch = (async (input: any, init: any) => {
			requestedURL = input instanceof Request ? input.url : String(input);
			const u = new URL(requestedURL);
			if (u.origin !== new URL(model.baseUrl).origin) throw new Error("Unexpected non-capture request " + requestedURL);
			const headers = new Headers(init?.headers);
			headers.set("host", u.host);
			return fetch(local + u.pathname + u.search, { ...init, headers, tls: undefined });
		}) as typeof fetch;
		const result = await streamSimple(model, scenario.context, { apiKey, sessionId, fetch: redirectFetch, ...scenario.options }).result();
		if (result.stopReason !== "stop") throw new Error(`Capture ${scenario.name} failed: ${result.errorMessage}`);
		if (captured.length !== 1) throw new Error(`Capture ${scenario.name} saw ${captured.length} requests`);
		const request = captured[0];
		fixtures.push({
			name: scenario.name,
			source: scenario.source,
			model: scenario.model,
			options: scenario.options,
			downstream: scenario.downstream,
			downstreamHeaders: scenario.downstreamHeaders,
			requestedURL,
			...request,
		});
	}
	console.log(JSON.stringify({ source: "NeoPi OWN streamSimple over getBundledModels(umans) and umansModelManagerOptions discovery", apiKey, sessionId, fixtures }, null, 2));
} finally {
	server.stop(true);
}
