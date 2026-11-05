// Runs NeoPi's OWN streamSimple (the agent entry) over every bundled row a
// provider serves over Anthropic Messages, for the providers in
// NEOPI_PROVIDERS (comma list), against a loopback capture server only; the
// key is fake and no request can reach the provider. Writes
// testdata/neopi_anthropic/<provider>.json.gz: one request per scenario,
// beside the downstream Messages request a client sends NeoProxy for it.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     NEOPI_PROVIDERS=minimax,minimax-cn bun --preserve-symlinks capture_neopi_anthropic_rows.ts
//
// A provider whose transport rewrites the row's origin (Snowflake's account
// placeholder) names the origin it sends to in CAPTURE_ORIGIN.
import { mkdirSync } from "node:fs";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const providers = (process.env.NEOPI_PROVIDERS ?? "").split(",").filter(Boolean);
if (providers.length === 0) throw new Error("NEOPI_PROVIDERS is required");
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
// Cloudflare AI Gateway routes each model per request; its rows are captured
// as that transport prepares them.
const { cloudflareAiGatewayTransport } = await import(root + "/pi-ai/src/registry/cloudflare-ai-gateway.ts");
const prepareModel = (provider: string, model: any) =>
	provider === "cloudflare-ai-gateway" ? cloudflareAiGatewayTransport.prepareModel(model) : model;

const apiKey = "sk-anthropic-rows-capture-000000000000";
const CAPTURE_ENV = ["GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_VERTEX_LOCATION", "VERTEX_LOCATION", "GOOGLE_CLOUD_ACCESS_TOKEN"];
const sessionId = "capture-session-anthropic-rows-0001";
const events = [
	{ type: "message_start", message: { id: "msg_capture", type: "message", role: "assistant", model: "capture", content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 3, output_tokens: 0 } } },
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

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const webSearch = { name: "web_search", description: "Search the web.", parameters: { type: "object", properties: { query: { type: "string" } }, required: ["query"] } };
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";

// A call turn's thinking is replayed as given: `thinking` overrides its text
// and `unsigned` drops the signature field entirely.
type Turn = { kind: "user"; text?: string; blocks?: any[] } | { kind: "call"; tool?: string; thinking?: string; unsigned?: boolean } | { kind: "result"; blocks: any[]; isError?: boolean; tool?: string };
const toolArgs = (tool: string) => (tool === "web_search" ? { query: "a" } : { term: "a" });
const claudeTurns = (turns: Turn[]) =>
	turns.map(turn => {
		if (turn.kind === "user") return { role: "user", content: turn.text ?? turn.blocks };
		if (turn.kind === "call")
			return {
				role: "assistant",
				content: [
					{ type: "thinking", thinking: turn.thinking ?? "Need a tool.", ...(turn.unsigned ? {} : { signature: "" }) },
					{ type: "tool_use", id: "call_capture_1", name: turn.tool ?? "lookup", input: toolArgs(turn.tool ?? "lookup") },
				],
			};
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
					{ type: "thinking", thinking: turn.thinking ?? "Need a tool.", ...(turn.unsigned ? {} : { thinkingSignature: "" }) },
					{ type: "toolCall", id: "call_capture_1", name: turn.tool ?? "lookup", arguments: toolArgs(turn.tool ?? "lookup") },
				],
				api: model.api,
				provider: model.provider,
				model: model.id,
				usage,
				stopReason: "toolUse",
				timestamp: 0,
			};
		return { role: "toolResult", toolCallId: "call_capture_1", toolName: turn.tool ?? "lookup", content: turn.blocks.map(neopiBlock), isError: !!turn.isError, timestamp: 0 };
	});

const hello: Turn[] = [{ kind: "user", text: "Say hello." }];
const askTurns: Turn[] = [{ kind: "user", text: "Use a tool." }];
const history = (blocks: any[], isError = false, tool = "lookup", call: { thinking?: string; unsigned?: boolean } = {}): Turn[] => [
	{ kind: "user", text: "Use a tool." },
	{ kind: "call", tool, ...call },
	{ kind: "result", blocks, isError, tool },
];
const text = (value: string) => ({ type: "text", text: value });
const image = { type: "image", source: { type: "base64", media_type: "image/png", data: png } };

const outDir = join(import.meta.dir, "neopi_anthropic");
mkdirSync(outDir, { recursive: true });
try {
	for (const provider of providers) {
		const rows = (getBundledModels(provider) as any[]).map(model => prepareModel(provider, model)).filter(model => model.api === "anthropic-messages");
		if (rows.length === 0) throw new Error(`${provider} bundles no anthropic-messages rows`);
		const scenarios: any[] = [];
		const add = (model: any, name: string, turns: Turn[], opts: any, extra: { system?: string; tools?: any[] } = {}) => {
			const context = { ...(extra.system ? { systemPrompt: [extra.system] } : {}), messages: neopiTurns(model, turns), ...(extra.tools ? { tools: extra.tools } : {}) };
			const downstream: any = { model: model.id, messages: claudeTurns(turns), stream: true };
			if (extra.system) downstream.system = extra.system;
			if (extra.tools) downstream.tools = extra.tools.map(tool => ({ name: tool.name, description: tool.description, input_schema: tool.parameters }));
			if (opts.maxTokens) downstream.max_tokens = opts.maxTokens;
			if (opts.temperature !== undefined) downstream.temperature = opts.temperature;
			if (opts.disableReasoning) downstream.thinking = { type: "disabled" };
			else if (opts.reasoning) Object.assign(downstream, { thinking: { type: "adaptive" }, output_config: { effort: opts.reasoning } });
			if (opts.toolChoice === "any") downstream.tool_choice = { type: "any" };
			else if (opts.toolChoice?.type === "tool") downstream.tool_choice = { type: "tool", name: opts.toolChoice.name };
			scenarios.push({ name: `${model.id}/${name}`, model, context, options: opts, downstream });
		};
		for (const model of rows) {
			const efforts: string[] = model.thinking?.efforts ?? [];
			const first = efforts[0];
			add(model, "plain", hello, {});
			for (const effort of efforts) add(model, `effort-${effort}`, hello, { reasoning: effort });
			add(model, "off", hello, { disableReasoning: true });
			add(model, "off-sampling", hello, { disableReasoning: true, temperature: 0.3, maxTokens: 300 });
			for (const effort of efforts) add(model, `max-tokens-${effort}`, hello, { reasoning: effort, maxTokens: 8192 });
			add(model, "system-tool", askTurns, { reasoning: first, maxTokens: 512, temperature: 0.2 }, { system: "A source system.", tools: [lookup] });
			add(model, "tool-history", history([text("Found.")]), { reasoning: first }, { tools: [lookup] });
			add(model, "tool-result-blocks", history([text("Line one."), text("  "), text("Line two.")]), { reasoning: first }, { tools: [lookup] });
			add(model, "tool-result-empty-error", history([], true), { reasoning: first }, { tools: [lookup] });
			add(model, "forced-any", askTurns, { reasoning: first, toolChoice: "any" }, { tools: [lookup] });
			add(model, "forced-named", askTurns, { reasoning: first, toolChoice: { type: "tool", name: "lookup" } }, { tools: [lookup] });
			add(model, "user-image", [{ kind: "user", blocks: [text("Describe."), image] }], { reasoning: first });
			add(model, "tool-result-image", history([text("Shot."), image]), { reasoning: first }, { tools: [lookup] });
			add(model, "builtin-tool-name", askTurns, { reasoning: first, toolChoice: { type: "tool", name: "web_search" } }, { tools: [lookup, webSearch] });
			add(model, "blank-thinking-history", history([text("Found.")], false, "lookup", { thinking: "   " }), { reasoning: first }, { tools: [lookup] });
			add(model, "unsigned-thinking-history", history([text("Found.")], false, "lookup", { unsigned: true }), { reasoning: first }, { tools: [lookup] });
		}
		const fixtures: any[] = [];
		for (const scenario of scenarios) {
			captured = [];
			const model = scenario.model;
			let requestedURL = "";
			const redirectFetch = (async (input: any, init: any) => {
				requestedURL = input instanceof Request ? input.url : String(input);
				const u = new URL(requestedURL);
				const allowed = [new URL(model.baseUrl).origin, process.env.CAPTURE_ORIGIN].filter(Boolean);
				if (!allowed.includes(u.origin)) throw new Error("Unexpected non-capture request " + requestedURL);
				const headers = new Headers(init?.headers);
				headers.set("host", u.host);
				return fetch(local + u.pathname + u.search, { ...init, headers, tls: undefined });
			}) as typeof fetch;
			const result = await streamSimple(model, scenario.context, { apiKey, sessionId, fetch: redirectFetch, ...scenario.options }).result();
			if (result.stopReason !== "stop") throw new Error(`Capture ${provider} ${scenario.name} failed: ${result.errorMessage}`);
			if (captured.length !== 1) throw new Error(`Capture ${provider} ${scenario.name} saw ${captured.length} requests`);
			fixtures.push({ name: scenario.name, model: model.id, baseUrl: model.baseUrl, options: scenario.options, downstream: scenario.downstream, requestedURL, ...captured[0] });
		}
		// The environment a provider transport reads (Vertex's project,
		// location and access token).
		const env = Object.fromEntries(CAPTURE_ENV.filter(name => process.env[name]).map(name => [name, process.env[name]!]));
		await Bun.write(
			join(outDir, `${provider}.json.gz`),
			Bun.gzipSync(
				JSON.stringify({
					source: `NeoPi OWN streamSimple over getBundledModels(${JSON.stringify(provider)}) anthropic-messages rows`,
					provider,
					apiKey,
					sessionId,
					...(Object.keys(env).length > 0 ? { env } : {}),
					fixtures,
				}),
			),
		);
		console.log(`${provider}: ${rows.length} rows, ${fixtures.length} fixtures`);
	}
} finally {
	server.stop(true);
}
