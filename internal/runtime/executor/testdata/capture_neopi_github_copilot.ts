// Runs NeoPi's OWN streamSimple over every bundled model of a routed host
// (NEOPI_PROVIDER: github-copilot, the default, commandcode, or litellm, whose
// rows NeoPi's own manager discovers from a scripted proxy; each row on
// its own API) against a loopback capture server only; the key is fake and
// no request can reach the host. Each scenario is recorded beside the
// downstream request a client sends NeoProxy for it, in the format the
// model's API takes: Claude Messages for anthropic-messages, Chat
// Completions for openai-completions, Responses (through NeoPi's own server
// boundary) for openai-responses.
// Writes testdata/neopi_<provider>/<api>.json.gz.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     NEOPI_PROVIDER=github-copilot COPILOT_APIS=anthropic-messages,openai-completions,openai-responses \
//     bun --preserve-symlinks capture_neopi_github_copilot.ts
import { mkdirSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const wires = (process.env.COPILOT_APIS ?? "anthropic-messages,openai-completions,openai-responses").split(",").filter(Boolean);
const provider = process.env.NEOPI_PROVIDER ?? "github-copilot";
// The host each provider's rows are served from, which the capture answers.
const origins: Record<string, string> = { "github-copilot": "https://api.githubcopilot.com", commandcode: "https://api.commandcode.ai", litellm: "http://localhost:4000" };
const origin = origins[provider];
if (!origin) throw new Error(`no capture origin for ${provider}`);
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
const { litellmModelManagerOptions } = await import(root + "/pi-catalog/src/provider-models/openai-compat.ts");
// A Responses client reaches NeoPi through its server boundary, which keeps
// each user item's native form for replay; the other wires' clients map onto
// NeoPi's own messages.
const { parseRequest } = await import(root + "/pi-ai/src/providers/openai-responses-server.ts");

// The bearer: a GitHub OAuth token used directly by Copilot, a Command Code
// Provider API key.
const apiKey = provider === "commandcode" ? "user_capturefixturekey00000000000000" : provider === "litellm" ? "sk-capture-litellm-key" : "gho_capturefixturetoken0000000000000000";
const sessionId = `capture-session-${provider}-0001`;

// LiteLLM bundles no rows: its groups come from the proxy's
// /model_group/info (recorded as listing, with the models.dev catalog, so
// NeoProxy discovers the same rows). OpenAI-backed groups run on Responses.
const liteLLMListing = JSON.stringify({
	data: [
		{ model_group: "gpt-6-proxy", providers: ["openai"], mode: "chat", max_input_tokens: 400000, max_output_tokens: 128000, supports_vision: true, supports_reasoning: true, supported_openai_params: ["tools", "tool_choice", "reasoning_effort"] },
		{ model_group: "o4-mini", mode: "chat", supports_vision: false, supports_reasoning: true },
		{ model_group: "claude-opus-5-5", providers: ["anthropic"], mode: "chat", supports_vision: true },
	],
});
const liteLLMModelsDev = JSON.stringify({ openai: { models: { "o4-mini": { name: "o4-mini", reasoning: true, tool_call: true, modalities: { input: ["text", "image"] }, cost: { input: 1.1, output: 4.4 }, limit: { context: 200000, output: 100000 } } } } });
async function rowsFor(wire: string): Promise<any[]> {
	if (provider !== "litellm") return getBundledModels(provider).filter((entry: any) => entry.api === wire);
	const options = litellmModelManagerOptions({
		apiKey,
		fetch: async (input: any) => {
			const url = new URL(typeof input === "string" ? input : input.url);
			if (url.hostname === "catalog.stencil.so") return new Response(liteLLMModelsDev, { status: 200, headers: { "content-type": "application/json" } });
			if (url.pathname === "/model_group/info") return new Response(liteLLMListing, { status: 200, headers: { "content-type": "application/json" } });
			return new Response("not found", { status: 404 });
		},
	});
	options.cacheDbPath = join(mkdtempSync(join(tmpdir(), "neopi-litellm-")), "litellm.db");
	return (await resolveProviderModels(options, "online")).models.filter((entry: any) => entry.api === wire);
}

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
const responsesMessage = { id: "msg_capture", type: "message", role: "assistant", status: "completed", content: [{ type: "output_text", text: "Captured.", annotations: [] }] };
const responsesSse = [
	{ type: "response.created", response: { id: "resp_capture", status: "in_progress", output: [] } },
	{ type: "response.output_item.added", output_index: 0, item: { ...responsesMessage, status: "in_progress", content: [] } },
	{ type: "response.content_part.added", item_id: "msg_capture", output_index: 0, content_index: 0, part: { type: "output_text", text: "", annotations: [] } },
	{ type: "response.output_text.delta", item_id: "msg_capture", output_index: 0, content_index: 0, delta: "Captured." },
	{ type: "response.output_text.done", item_id: "msg_capture", output_index: 0, content_index: 0, text: "Captured." },
	{ type: "response.output_item.done", output_index: 0, item: responsesMessage },
	{ type: "response.completed", response: { id: "resp_capture", status: "completed", output: [responsesMessage], usage: { input_tokens: 3, output_tokens: 1, total_tokens: 4 } } },
].map(event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join("");
const sseFor = (wire: string) =>
	wire === "anthropic-messages" ? anthropicSse : wire === "google-generate" ? googleSse : wire === "openai-responses" ? responsesSse : completionsSse;

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
		return { role: "user", parts: [{ functionResponse: { name: "lookup", response: { output: turn.text } } }] };
	});
	const body: any = { contents };
	if (extra.system) body.systemInstruction = { parts: [{ text: extra.system }] };
	if (extra.tools) body.tools = [{ functionDeclarations: extra.tools.map((tool: any) => ({ name: tool.name, description: tool.description, parametersJsonSchema: tool.parameters })) }];
	const generationConfig: any = {};
	if (opts.maxTokens) generationConfig.maxOutputTokens = opts.maxTokens;
	if (opts.temperature !== undefined) generationConfig.temperature = opts.temperature;
	if (opts.disableReasoning) generationConfig.thinkingConfig = { thinkingBudget: 0 };
	else if (opts.reasoning) generationConfig.thinkingConfig = { thinkingLevel: opts.reasoning };
	if (Object.keys(generationConfig).length) body.generationConfig = generationConfig;
	return body;
};
const responsesDownstream = (model: any, turns: Turn[], opts: any, extra: any) => {
	const input: any[] = [];
	for (const turn of turns) {
		if (turn.kind === "user")
			input.push({ role: "user", content: turn.image ? [{ type: "input_text", text: turn.text }, { type: "input_image", image_url: `data:image/png;base64,${png}` }] : [{ type: "input_text", text: turn.text }] });
		else if (turn.kind === "call") input.push({ type: "function_call", call_id: "call_capture_1", name: turn.tool ?? "lookup", arguments: JSON.stringify(toolArgs(turn.tool ?? "lookup")) });
		else input.push({ type: "function_call_output", call_id: "call_capture_1", output: turn.text });
	}
	const body: any = { model: model.id, input, stream: true };
	if (extra.system) body.instructions = extra.system;
	if (extra.tools) body.tools = extra.tools.map((tool: any) => ({ type: "function", name: tool.name, description: tool.description, parameters: tool.parameters }));
	if (opts.maxTokens) body.max_output_tokens = opts.maxTokens;
	if (opts.temperature !== undefined) body.temperature = opts.temperature;
	if (opts.disableReasoning) body.reasoning = { effort: "none" };
	else if (opts.reasoning) body.reasoning = { effort: opts.reasoning };
	if (opts.textVerbosity) body.text = { verbosity: opts.textVerbosity };
	if (opts.toolChoice !== undefined) body.tool_choice = opts.toolChoice;
	return body;
};
const downstreamFor = (wire: string) =>
	wire === "anthropic-messages" ? claudeDownstream : wire === "google-generate" ? geminiDownstream : wire === "openai-responses" ? responsesDownstream : chatDownstream;

const outDir = join(import.meta.dir, `neopi_${provider.replaceAll("-", "_")}`);
mkdirSync(outDir, { recursive: true });
try {
	for (const wire of wires) {
		currentWire = wire;
		const registry = await rowsFor(wire);
		const scenarios: any[] = [];
		for (const model of registry) {
			const efforts: string[] = model.thinking?.efforts ?? [];
		const add = (name: string, turns: Turn[], opts: any, extra: any = {}) => {
			const downstream = downstreamFor(wire)(model, turns, opts, extra);
			const context = wire === "openai-responses"
				? parseRequest(downstream).context
				: { ...(extra.system ? { systemPrompt: [extra.system] } : {}), messages: neopiTurns(model, turns, wire === "anthropic-messages"), ...(extra.tools ? { tools: extra.tools } : {}) };
			scenarios.push({ name: `${model.id}/${name}`, model, context, options: opts, downstream });
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
		if (wire === "openai-responses") {
			add("verbosity", hello, { reasoning: efforts[0], textVerbosity: "low" });
			add("forced-named", ask, { reasoning: efforts[0], toolChoice: { type: "function", name: "lookup" } }, { tools: [lookup] });
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
				if (u.origin !== origin) throw new Error("Unexpected non-capture request " + requestedURL);
				const headers = new Headers(init?.headers);
				headers.set("host", u.host);
				return fetch(local + u.pathname + u.search, { ...init, headers, tls: undefined });
			}) as typeof fetch;
			const result = await streamSimple(scenario.model, scenario.context, { apiKey, sessionId, fetch: redirectFetch, ...scenario.options }).result();
			if (result.stopReason !== "stop") throw new Error(`Capture ${scenario.name} failed: ${result.errorMessage}`);
			if (captured.length !== 1) throw new Error(`Capture ${scenario.name} saw ${captured.length} requests`);
			fixtures.push({ name: scenario.name, model: scenario.model.id, options: scenario.options, downstream: scenario.downstream, requestedURL, ...captured[0] });
		}
		await Bun.write(join(outDir, `${wire}.json.gz`), Bun.gzipSync(JSON.stringify({
			source: `NeoPi OWN streamSimple over ${provider === "litellm" ? "discovered" : "bundled"} ${provider} ${wire} rows`,
			wire,
			apiKey,
			sessionId,
			...(provider === "litellm" ? { listing: liteLLMListing, modelsDev: liteLLMModelsDev } : {}),
			fixtures,
		})));
		console.log(`${wire}: ${registry.length} models, ${fixtures.length} fixtures`);
	}
} finally {
	server.stop(true);
}
