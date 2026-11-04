// Runs NeoPi's OWN kimi-code provider (streamKimi through streamSimple, the
// agent entry) against a loopback capture server only. Models come from NeoPi's
// discovery mapper over the recorded /coding/v1/models envelope, resolved by
// buildModel; the token is fake, so no request can reach Kimi.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { kimiCodeModelManagerOptions } = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { getKimiCommonHeaders } = await import(root + "/pi-ai/src/registry/oauth/kimi.ts");

const apiKey = "kimi-capture-access-token";
const sessionId = "capture-session-kimi-0001";
const anthropicEvents = [
	{ type: "message_start", message: { id: "msg_capture", type: "message", role: "assistant", model: "glm", content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 3, output_tokens: 0 } } },
	{ type: "content_block_start", index: 0, content_block: { type: "text", text: "" } },
	{ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "Captured." } },
	{ type: "content_block_stop", index: 0 },
	{ type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 1 } },
	{ type: "message_stop" },
];
const anthropicSSE = anthropicEvents.map(event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join("");
const chatChunks = [
	{ id: "chatcmpl-capture", object: "chat.completion.chunk", created: 1, model: "glm-5.3-flash", choices: [{ index: 0, delta: { role: "assistant", content: "Captured." }, finish_reason: null }] },
	{ id: "chatcmpl-capture", object: "chat.completion.chunk", created: 1, model: "glm-5.3-flash", choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 3, completion_tokens: 1, total_tokens: 4 } },
];
const chatSSE = chatChunks.map(chunk => `data: ${JSON.stringify(chunk)}\n\n`).join("") + "data: [DONE]\n\n";
let captured: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const rawBody = await request.text();
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody });
		const chat = url.pathname.endsWith("/chat/completions");
		return new Response(chat ? chatSSE : anthropicSSE, { headers: { "content-type": "text/event-stream" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;
const payload = await Bun.file(import.meta.dir + "/kimi_models_live.json").text();
const discovery = kimiCodeModelManagerOptions({ apiKey, fetch: (async () => new Response(payload, { headers: { "content-type": "application/json" } })) as any });
const resolved = new Map((await discovery.fetchDynamicModels!()).map((spec: any) => [spec.id, buildModel(spec)]));
const row = (id: string) => {
	const model = resolved.get(id);
	if (!model) throw new Error("no kimi-code row " + id);
	return model;
};
const anthropicRoute = (model: any) => model.api === "anthropic-messages" || model.compat?.kimiApiFormat === "anthropic";

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
const user = (content: unknown) => ({ role: "user", content, timestamp: 0 });
const assistantCall = (model: any) => ({
	role: "assistant",
	content: [
		{ type: "thinking", thinking: "Need lookup.", thinkingSignature: "" },
		{ type: "toolCall", id: "call_capture_1", name: "lookup", arguments: { term: "a" } },
	],
	api: model.api,
	provider: "kimi-code",
	model: model.id,
	usage,
	stopReason: "toolUse",
	timestamp: 0,
});
const toolResult = (content: unknown[], isError = false) => ({ role: "toolResult", toolCallId: "call_capture_1", toolName: "lookup", content, isError, timestamp: 0 });

// Each scenario is one NeoPi context plus the request a client sends NeoProxy
// for it, in the dialect NeoProxy translates to before shaping: Anthropic
// Messages for anthropic rows, OpenAI Chat Completions for the completions row.
// Downstream thinking is what NeoProxy's thinking pipeline produces for a
// level-based model: adaptive + output_config.effort, or reasoning_effort.
type Turn = { kind: "user"; text?: string; blocks?: any[] } | { kind: "call" } | { kind: "result"; blocks: any[]; isError?: boolean };
const claudeTurns = (turns: Turn[]) =>
	turns.map(turn => {
		if (turn.kind === "user") return { role: "user", content: turn.text ?? turn.blocks };
		if (turn.kind === "call") return { role: "assistant", content: [{ type: "thinking", thinking: "Need lookup.", signature: "" }, { type: "tool_use", id: "call_capture_1", name: "lookup", input: { term: "a" } }] };
		return { role: "user", content: [{ type: "tool_result", tool_use_id: "call_capture_1", content: turn.blocks, ...(turn.isError ? { is_error: true } : {}) }] };
	});
const chatTurns = (turns: Turn[]) =>
	turns.map(turn => {
		if (turn.kind === "user") return { role: "user", content: turn.text ?? turn.blocks!.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image_url", image_url: { url: `data:image/png;base64,${b.source.data}` } })) };
		if (turn.kind === "call") return { role: "assistant", content: "", reasoning_content: "Need lookup.", tool_calls: [{ id: "call_capture_1", type: "function", function: { name: "lookup", arguments: "{\"term\":\"a\"}" } }] };
		const parts = turn.blocks.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image_url", image_url: { url: `data:image/png;base64,${b.source.data}` } }));
		return { role: "tool", tool_call_id: "call_capture_1", content: turn.blocks.some((b: any) => b.type !== "text") ? parts : turn.blocks.map((b: any) => b.text).join("\n") };
	});
const neopiTurns = (model: any, turns: Turn[]) =>
	turns.map(turn => {
		if (turn.kind === "user") return user(turn.text ?? turn.blocks!.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image", data: b.source.data, mimeType: b.source.media_type })));
		if (turn.kind === "call") return assistantCall(model);
		return toolResult(turn.blocks.map((b: any) => (b.type === "text" ? { type: "text", text: b.text } : { type: "image", data: b.source.data, mimeType: b.source.media_type })), turn.isError);
	});

const scenarios: any[] = [];
const add = (id: string, name: string, turns: Turn[], options: any, extra: { system?: string; tools?: boolean } = {}) => {
	const model = row(id);
	const context = { ...(extra.system ? { systemPrompt: [extra.system] } : {}), messages: neopiTurns(model, turns), ...(extra.tools ? { tools: [lookup] } : {}) };
	let downstream: any;
	if (anthropicRoute(model)) {
		downstream = { model: id, messages: claudeTurns(turns), stream: true };
		if (extra.system) downstream.system = extra.system;
		if (extra.tools) downstream.tools = [{ name: lookup.name, description: lookup.description, input_schema: lookup.parameters }];
		if (options.maxTokens) downstream.max_tokens = options.maxTokens;
		if (options.temperature !== undefined) downstream.temperature = options.temperature;
		if (options.disableReasoning) downstream.thinking = { type: "disabled" };
		else if (options.reasoning) Object.assign(downstream, { thinking: { type: "adaptive" }, output_config: { effort: options.reasoning } });
		if (options.toolChoice === "any") downstream.tool_choice = { type: "any" };
		else if (options.toolChoice?.type === "tool") downstream.tool_choice = { type: "tool", name: options.toolChoice.name };
	} else {
		downstream = { model: id, messages: [...(extra.system ? [{ role: "system", content: extra.system }] : []), ...chatTurns(turns)], stream: true };
		if (extra.tools) downstream.tools = [{ type: "function", function: { name: lookup.name, description: lookup.description, parameters: lookup.parameters } }];
		if (options.maxTokens) downstream.max_tokens = options.maxTokens;
		if (options.temperature !== undefined) downstream.temperature = options.temperature;
		if (options.disableReasoning) downstream.reasoning_effort = "none";
		else if (options.reasoning) downstream.reasoning_effort = options.reasoning;
		if (options.toolChoice === "any") downstream.tool_choice = "required";
		else if (options.toolChoice?.type === "tool") downstream.tool_choice = { type: "function", function: { name: options.toolChoice.name } };
	}
	scenarios.push({ name: `${id}/${name}`, model: id, context, options, downstream });
};
const hello: Turn[] = [{ kind: "user", text: "Say hello." }];
const lookupTurns: Turn[] = [{ kind: "user", text: "Use lookup." }];
const history = (blocks: any[], isError = false): Turn[] => [{ kind: "user", text: "Use lookup." }, { kind: "call" }, { kind: "result", blocks, isError }];
const text = (value: string) => ({ type: "text", text: value });
const image = { type: "image", source: { type: "base64", media_type: "image/png", data: png } };
for (const id of ["k3", "kimi-for-coding", "kimi-for-coding-highspeed"]) {
	const efforts = row(id).thinking?.efforts ?? [];
	add(id, "plain", hello, {});
	for (const effort of efforts) add(id, `effort-${effort}`, hello, { reasoning: effort });
	add(id, "off", hello, { disableReasoning: true });
	add(id, "off-sampling", hello, { disableReasoning: true, temperature: 0.3, maxTokens: 300 });
	add(id, "system-tool", lookupTurns, { reasoning: efforts[0], maxTokens: 512, temperature: 0.2 }, { system: "A source system.", tools: true });
	add(id, "tool-history", history([text("Found.")]), { reasoning: efforts[0] }, { tools: true });
	add(id, "tool-result-blocks", history([text("Line one."), text("  "), text("Line two.")]), { reasoning: efforts[0] }, { tools: true });
	add(id, "tool-result-empty-error", history([], true), { reasoning: efforts[0] }, { tools: true });
	add(id, "forced-any", lookupTurns, { reasoning: efforts[0], toolChoice: "any" }, { tools: true });
	add(id, "forced-named", lookupTurns, { reasoning: efforts[0], toolChoice: { type: "tool", name: "lookup" } }, { tools: true });
	add(id, "user-image", [{ kind: "user", blocks: [text("Describe."), image] }], { reasoning: efforts[0] });
	add(id, "tool-result-image", history([text("Shot."), image]), { reasoning: efforts[0] }, { tools: true });
}

const fixtures: any[] = [];
try {
	for (const scenario of scenarios) {
		captured = [];
		const model = row(scenario.model);
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
		fixtures.push({ name: scenario.name, model: scenario.model, api: model.api, options: scenario.options, downstream: scenario.downstream, requestedURL, ...request, body: JSON.parse(request.rawBody) });
	}
	console.log(JSON.stringify({ source: "NeoPi OWN streamSimple (streamKimi) over kimiCodeModelManagerOptions + buildModel", apiKey, sessionId, kimiHeaders: getKimiCommonHeaders(), fixtures }, null, 2));
} finally {
	server.stop(true);
}
