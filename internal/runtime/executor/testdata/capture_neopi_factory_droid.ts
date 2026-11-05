// Runs NeoPi's OWN streamSimple over every Factory Droid registry model, built
// for a signed-in account (buildFactoryDroidModel with the registry rotation),
// against a loopback capture server only; the bearer is fake and no request
// can reach Factory. Each scenario is recorded beside the downstream request
// a client sends NeoProxy for it, in the format the model's wire takes:
// Claude Messages for anthropic-messages, Chat Completions for
// openai-completions, Gemini generateContent for google-generate. Writes
// testdata/neopi_factory_droid/<wire>.json.gz.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     FACTORY_WIRES=anthropic-messages,openai-completions,google-generate \
//     bun --preserve-symlinks capture_neopi_factory_droid.ts
import { mkdirSync } from "node:fs";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const wires = (process.env.FACTORY_WIRES ?? "anthropic-messages,openai-completions,google-generate").split(",").filter(Boolean);
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { factoryDroidRegistry } = await import(root + "/pi-catalog/src/compat/factory-droid.ts");
const { buildFactoryDroidModel } = await import(root + "/pi-catalog/src/discovery/factory-droid.ts");

const jwt = (payload: unknown) => "eyJhbGciOiJub25lIn0." + Buffer.from(JSON.stringify(payload)).toString("base64url") + ".fake";
const apiKey = jwt({ sub: "user_fixture", external_org_id: "org-fixture", exp: 4102444800 });
const identity = { orgId: "org-fixture", region: "global", inferenceRegion: "global" };
const sessionId = "capture-session-factory-droid-0001";

// Minimal successful streams per wire; the request is what is compared.
const anthropicSse = [
	{ type: "message_start", message: { id: "msg_capture", type: "message", role: "assistant", model: "capture", content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 3, output_tokens: 0 } } },
	{ type: "content_block_start", index: 0, content_block: { type: "text", text: "" } },
	{ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "Captured." } },
	{ type: "content_block_stop", index: 0 },
	{ type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 1 } },
	{ type: "message_stop" },
].map(event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join("");
const completionsSse =
	`data: ${JSON.stringify({ id: "capture", object: "chat.completion.chunk", model: "capture", choices: [{ index: 0, delta: { role: "assistant", content: "Captured." }, finish_reason: null }] })}\n\n` +
	`data: ${JSON.stringify({ id: "capture", object: "chat.completion.chunk", model: "capture", choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 3, completion_tokens: 1, total_tokens: 4 } })}\n\n` +
	"data: [DONE]\n\n";
const googleSse = `data: ${JSON.stringify({ candidates: [{ content: { role: "model", parts: [{ text: "Captured." }] }, finishReason: "STOP" }], usageMetadata: { promptTokenCount: 3, candidatesTokenCount: 1, totalTokenCount: 4 } })}\n\n`;
const sseFor = (wire: string) => (wire === "anthropic-messages" ? anthropicSse : wire === "google-generate" ? googleSse : completionsSse);

let captured: any[] = [];
let currentWire = "";
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody: await request.text() });
		return new Response(sseFor(currentWire), { headers: { "content-type": "text/event-stream" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const webSearch = { name: "web_search", description: "Search the web.", parameters: { type: "object", properties: { query: { type: "string" } }, required: ["query"] } };
const dottedName = "my.tool/name is long enough to exceed the route tool name bound sixty-four";
const dotted = { name: dottedName, description: "A very long tool name.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";

type Turn = { kind: "user"; text?: string; image?: boolean; thinking?: string; unsigned?: boolean; tool?: string } | { kind: "call"; thinking?: string; unsigned?: boolean; tool?: string } | { kind: "result"; text: string; isError?: boolean; tool?: string };
const hello: Turn[] = [{ kind: "user", text: "Say hello." }];
const ask: Turn[] = [{ kind: "user", text: "Use a tool." }];
const historyOf = (call: { thinking?: string; unsigned?: boolean; tool?: string } = {}): Turn[] => [
	{ kind: "user", text: "Use a tool." },
	{ kind: "call", ...call },
	{ kind: "result", text: "Found.", tool: call.tool },
];
const history: Turn[] = historyOf();
const describe: Turn[] = [{ kind: "user", text: "Describe.", image: true }];

const neopiTurns = (model: any, turns: Turn[], withThinking: boolean) =>
	turns.map(turn => {
		if (turn.kind === "user")
			return { role: "user", content: turn.image ? [{ type: "text", text: turn.text }, { type: "image", data: png, mimeType: "image/png" }] : turn.text, timestamp: 0 };
		if (turn.kind === "call")
			return {
				role: "assistant",
				content: [
					...(withThinking
						? [{ type: "thinking", thinking: turn.thinking ?? "Need a tool.", ...(turn.unsigned ? {} : { thinkingSignature: "" }) }]
						: []),
					{ type: "toolCall", id: "call_capture_1", name: turn.tool ?? "lookup", arguments: toolArgs(turn.tool ?? "lookup") },
				],
				api: model.api,
				provider: model.provider,
				model: model.id,
				usage,
				stopReason: "toolUse",
				timestamp: 0,
			};
		return { role: "toolResult", toolCallId: "call_capture_1", toolName: turn.tool ?? "lookup", content: [{ type: "text", text: turn.text }], isError: !!turn.isError, timestamp: 0 };
	});

const toolArgs = (tool: string) => (tool === "web_search" ? { query: "a" } : { term: "a" });

// Downstream requests per client format.
const claudeDownstream = (model: any, turns: Turn[], opts: any, extra: any) => {
	const body: any = {
		model: model.id,
		messages: turns.map(turn => {
			if (turn.kind === "user")
				return { role: "user", content: turn.image ? [{ type: "text", text: turn.text }, { type: "image", source: { type: "base64", media_type: "image/png", data: png } }] : turn.text };
		if (turn.kind === "call")
			return {
				role: "assistant",
				content: [
					{ type: "thinking", thinking: turn.thinking ?? "Need a tool.", ...(turn.unsigned ? {} : { signature: "" }) },
					{ type: "tool_use", id: "call_capture_1", name: turn.tool ?? "lookup", input: toolArgs(turn.tool ?? "lookup") },
				],
			};
			return { role: "user", content: [{ type: "tool_result", tool_use_id: "call_capture_1", content: [{ type: "text", text: turn.text }], ...(turn.isError ? { is_error: true } : {}) }] };
		}),
		stream: true,
	};
	if (extra.system) body.system = extra.system;
	if (extra.tools) body.tools = extra.tools.map((tool: any) => ({ name: tool.name, description: tool.description, input_schema: tool.parameters }));
	if (opts.maxTokens) body.max_tokens = opts.maxTokens;
	if (opts.temperature !== undefined) body.temperature = opts.temperature;
	if (opts.disableReasoning) body.thinking = { type: "disabled" };
	else if (opts.reasoning) Object.assign(body, { thinking: { type: "adaptive" }, output_config: { effort: opts.reasoning } });
	if (opts.toolChoice === "any") body.tool_choice = { type: "any" };
	else if (opts.toolChoice?.type === "tool") body.tool_choice = { type: "tool", name: opts.toolChoice.name };
	return body;
};
const chatDownstream = (model: any, turns: Turn[], opts: any, extra: any) => {
	const messages: any[] = extra.system ? [{ role: "system", content: extra.system }] : [];
	for (const turn of turns) {
		if (turn.kind === "user")
			messages.push({ role: "user", content: turn.image ? [{ type: "text", text: turn.text }, { type: "image_url", image_url: { url: `data:image/png;base64,${png}` } }] : turn.text });
		else if (turn.kind === "call")
			messages.push({ role: "assistant", content: null, tool_calls: [{ id: "call_capture_1", type: "function", function: { name: turn.tool ?? "lookup", arguments: JSON.stringify(toolArgs(turn.tool ?? "lookup")) } }] });
		else messages.push({ role: "tool", tool_call_id: "call_capture_1", content: turn.text });
	}
	const body: any = { model: model.id, messages, stream: true };
	if (extra.tools) body.tools = extra.tools.map((tool: any) => ({ type: "function", function: tool }));
	if (opts.maxTokens) body.max_tokens = opts.maxTokens;
	if (opts.temperature !== undefined) body.temperature = opts.temperature;
	if (opts.disableReasoning) body.reasoning_effort = "none";
	else if (opts.reasoning) body.reasoning_effort = opts.reasoning;
	if (opts.toolChoice !== undefined) body.tool_choice = opts.toolChoice;
	return body;
};
const geminiDownstream = (model: any, turns: Turn[], opts: any, extra: any) => {
	const contents = turns.map(turn => {
		if (turn.kind === "user")
			return { role: "user", parts: turn.image ? [{ text: turn.text }, { inlineData: { mimeType: "image/png", data: png } }] : [{ text: turn.text }] };
		if (turn.kind === "call") return { role: "model", parts: [{ functionCall: { name: "lookup", args: { term: "a" } } }] };
		return { role: "user", parts: [{ functionResponse: { name: "lookup", response: { result: turn.text } } }] };
	});
	const body: any = { contents };
	if (extra.system) body.systemInstruction = { parts: [{ text: extra.system }] };
	if (extra.tools) body.tools = [{ functionDeclarations: extra.tools.map((tool: any) => ({ name: tool.name, description: tool.description, parametersJsonSchema: tool.parameters })) }];
	const generationConfig: any = {};
	if (opts.maxTokens) generationConfig.maxOutputTokens = opts.maxTokens;
	if (opts.temperature !== undefined) generationConfig.temperature = opts.temperature;
	if (opts.disableReasoning) generationConfig.thinkingConfig = { thinkingLevel: "minimal" };
	else if (opts.reasoning) generationConfig.thinkingConfig = { thinkingLevel: opts.reasoning };
	if (Object.keys(generationConfig).length) body.generationConfig = generationConfig;
	return body;
};
const downstreamFor = (wire: string) => (wire === "anthropic-messages" ? claudeDownstream : wire === "google-generate" ? geminiDownstream : chatDownstream);

const outDir = join(import.meta.dir, "neopi_factory_droid");
mkdirSync(outDir, { recursive: true });
try {
	for (const wire of wires) {
		currentWire = wire;
		const registry = factoryDroidRegistry().filter((entry: any) => entry.policy.wire === wire && !entry.policy.image);
		const scenarios: any[] = [];
		for (const entry of registry) {
			const model = buildFactoryDroidModel(entry, { ...identity, apiProviders: entry.policy.rotation });
			const efforts: string[] = model.thinking?.efforts ?? [];
		const add = (name: string, turns: Turn[], opts: any, extra: any = {}) => {
			const context = { ...(extra.system ? { systemPrompt: [extra.system] } : {}), messages: neopiTurns(model, turns, wire === "anthropic-messages"), ...(extra.tools ? { tools: extra.tools } : {}) };
			scenarios.push({ name: `${model.id}/${name}`, model, context, options: opts, downstream: downstreamFor(wire)(model, turns, opts, extra) });
		};
		add("plain", hello, {});
		for (const effort of efforts) add(`effort-${effort}`, hello, { reasoning: effort });
		add("off", hello, { disableReasoning: true });
		add("off-sampling", hello, { disableReasoning: true, temperature: 0.3, maxTokens: 300 });
		add("system-tool", ask, { reasoning: efforts[0], maxTokens: 512 }, { system: "A source system.", tools: [lookup] });
		add("tool-history", history, { reasoning: efforts[0] }, { tools: [lookup] });
		if (wire === "anthropic-messages") {
			add("tool-result-blocks", historyOf(), { reasoning: efforts[0] }, { tools: [lookup] });
			add("blank-thinking-history", historyOf({ thinking: "   " }), { reasoning: efforts[0] }, { tools: [lookup] });
			add("unsigned-thinking-history", historyOf({ unsigned: true }), { reasoning: efforts[0] }, { tools: [lookup] });
			add("builtin-tool-name", historyOf({ tool: "web_search" }), { reasoning: efforts[0] }, { tools: [lookup, webSearch] });
			add("forced-any", ask, { reasoning: efforts[0], toolChoice: "any" }, { tools: [lookup] });
			add("forced-named", ask, { reasoning: efforts[0], toolChoice: { type: "tool", name: "lookup" } }, { tools: [lookup] });
		}
		if (wire === "openai-completions") {
			add("long-tool-name", historyOf({ tool: dottedName }), { reasoning: efforts[0] }, { tools: [lookup, dotted] });
			add("forced-named", ask, { reasoning: efforts[0], toolChoice: { type: "function", function: { name: "lookup" } } }, { tools: [lookup] });
		}
		if (model.input.includes("image")) add("user-image", describe, { reasoning: efforts[0] });
		}
		const fixtures: any[] = [];
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
			const result = await streamSimple(scenario.model, scenario.context, { apiKey, sessionId, oauthIdentity: identity, fetch: redirectFetch, ...scenario.options }).result();
			if (result.stopReason !== "stop") throw new Error(`Capture ${scenario.name} failed: ${result.errorMessage}`);
			if (captured.length !== 1) throw new Error(`Capture ${scenario.name} saw ${captured.length} requests`);
			fixtures.push({ name: scenario.name, model: scenario.model.id, options: scenario.options, downstream: scenario.downstream, requestedURL, ...captured[0] });
		}
		await Bun.write(join(outDir, `${wire}.json.gz`), Bun.gzipSync(JSON.stringify({ source: `NeoPi OWN streamSimple over factoryDroidRegistry ${wire} rows`, wire, apiKey, sessionId, identity, fixtures })));
		console.log(`${wire}: ${registry.length} models, ${fixtures.length} fixtures`);
	}
} finally {
	server.stop(true);
}
