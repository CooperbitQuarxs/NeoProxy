// Executes NeoPi's OWN GitLab Duo Agent runtime end to end: an OpenAI chat
// body through openai-chat-server parseRequest, then streamSimple over the
// gitlab-duo-agent model with a provider session that lives across turns.
// GitLab's REST API answers in process (through the request's fetch, so
// transportFetch shapes the wire); the Duo Workflow WebSocket is a scripted
// stand-in for the global WebSocket, delivering each step's frames after the
// client's sends. Every REST request, socket, sent frame and emitted event is
// recorded per turn. Nothing leaves the process.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_gitlab_duo_agent_runtime.ts > gitlab_duo_agent_runtime.json
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { parseRequest } = await import(root + "/pi-ai/src/providers/openai-chat-server.ts");
const { buildGitLabDuoWorkflowModelSpec } = await import(root + "/pi-catalog/src/discovery/gitlab-duo-workflow.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const cwd = mkdtempSync(join(tmpdir(), "gitlab-duo-agent-runtime-"));
const model = buildModel(buildGitLabDuoWorkflowModelSpec({ name: "Claude Sonnet 4.6", ref: "claude_sonnet_4_6_vertex" }));

// --- GitLab REST, in process -------------------------------------------------
let rest: any[] = [];
let workflowCounter = 1000;
const restServer = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const path = url.pathname + url.search;
		const headers = Object.fromEntries(request.headers);
		delete headers.host;
		const text = await request.text();
		let status = 200;
		let body: unknown = {};
		if (request.method === "GET" && url.pathname === "/api/v4/groups") body = [{ id: 70, full_path: "team", path: "team" }];
		else if (request.method === "PUT" && url.pathname === "/api/v4/groups/70") body = { id: 70 };
		else if (request.method === "GET" && url.pathname === "/api/v4/groups/70/projects") body = [{ id: 4242, path_with_namespace: "team/app" }];
		else if (request.method === "POST" && url.pathname === "/api/v4/ai/duo_workflows/direct_access") body = { gitlab_rails: { base_url: "https://gitlab.com", token: "wf-rails-token" } };
		else if (request.method === "POST" && url.pathname === "/api/v4/ai/duo_workflows/workflows") body = { id: ++workflowCounter };
		else if (request.method === "PATCH" && url.pathname.startsWith("/api/v4/ai/duo_workflows/workflows/")) body = { status: "stopped" };
		else if (request.method === "POST" && url.pathname === "/api/graphql") body = { data: { aiChatAvailableModels: { defaultModel: { name: "Claude Sonnet 4.6", ref: "claude_sonnet_4_6_vertex" }, selectableModels: [{ name: "Claude Sonnet 4.6", ref: "claude_sonnet_4_6_vertex" }], pinnedModel: null } } };
		else {
			status = 404;
			body = { message: "404 Not Found" };
		}
		const response = { status, body: JSON.stringify(body) };
		rest.push({ method: request.method, url: "https://gitlab.com" + path, headers, ...(text ? { body: text } : {}), response });
		return new Response(response.body, { status, headers: { "content-type": "application/json" } });
	},
});
const nativeFetch = globalThis.fetch;
const loopbackFetch = (async (input: any, init?: any) => {
	const url = new URL(input instanceof Request ? input.url : String(input));
	if (url.origin !== "https://gitlab.com") throw new Error("Unexpected GitLab URL: " + url.href);
	return nativeFetch(`http://127.0.0.1:${restServer.port}${url.pathname}${url.search}`, init);
}) as typeof fetch;

// --- The Duo Workflow WebSocket, scripted -------------------------------------
type Frame = string | { close: number; reason: string };
let script: Frame[][][] = [];
let sockets: any[] = [];
class ScriptedSocket {
	onopen: ((event: unknown) => void) | null = null;
	onmessage: ((event: { data: unknown }) => void) | null = null;
	onerror: ((event: unknown) => void) | null = null;
	onclose: ((event: { code: number; reason: string }) => void) | null = null;
	record: any;
	steps: Frame[][];
	sends = 0;
	constructor(url: string, options: { headers: Record<string, string> }) {
		const index = sockets.length;
		this.steps = script[index] ?? [];
		this.record = { url, headers: options.headers, sent: [], closedByClient: false };
		sockets.push(this.record);
		setTimeout(() => this.onopen?.({}), 0);
	}
	send(data: string) {
		this.record.sent.push(data);
		const frames = this.steps[this.sends++] ?? [];
		let delay = 0;
		for (const frame of frames) {
			setTimeout(() => {
				if (typeof frame === "string") this.onmessage?.({ data: frame });
				else this.onclose?.({ code: frame.close, reason: frame.reason });
			}, ++delay);
		}
	}
	close() {
		if (this.record.closedByClient) return;
		this.record.closedByClient = true;
		setTimeout(() => this.onclose?.({ code: 1005, reason: "" }), 0);
	}
}
(globalThis as any).WebSocket = ScriptedSocket;

// --- Frames --------------------------------------------------------------------
const request = (content: string) => ({ message_type: "request", content });
const reasoning = (id: string, content: string) => ({ message_type: "agent", message_sub_type: "reasoning", message_id: id, content });
const agent = (id: string, content: string) => ({ message_type: "agent", message_id: id, content });
const toolEntry = (content: string) => ({ message_type: "tool", content });
const checkpoint = (log: unknown[], status: string, usage?: { total: number; max: number }) =>
	JSON.stringify({
		newCheckpoint: { checkpoint: JSON.stringify({ channel_values: { ui_chat_log: log } }), status },
		...(usage ? { agent_context_usage: { "Chat Agent": { total_tokens: usage.total, max_tokens: usage.max } } } : {}),
	});
const action = (requestID: string, toolName: string, args: unknown) => JSON.stringify({ requestID, runMCPTool: { toolName, args: JSON.stringify(args) } });
const failed = (message: string) => JSON.stringify({ workflowStatus: { status: "FAILED" }, message });
const generic = "There was an error processing your request in the Duo Agent Platform, please contact support if the issue persists.";

const lookup = { type: "function", function: { name: "lookup", description: "Look a thing up.", parameters: { type: "object", properties: { query: { type: "string" } }, required: ["query"] } } };
const system = { role: "system", content: "You are NeoPi." };
const ask = { role: "user", content: "What's the weather?" };
const called = (id: string) => ({ role: "assistant", content: null, tool_calls: [{ id, type: "function", function: { name: "lookup", arguments: JSON.stringify({ query: "weather" }) } }] });
const result = (id: string, content: string) => ({ role: "tool", tool_call_id: id, content });
const body = (messages: unknown[], tools?: unknown[]) => ({ model: "claude_sonnet_4_6_vertex", messages, ...(tools ? { tools } : {}) });
const stuck = checkpoint([request("What's the weather?"), reasoning("r1", "Let me look.")], "RUNNING");

const scenarios: { name: string; turns: unknown[]; script: Frame[][][] }[] = [
	{
		name: "text",
		turns: [body([system, { role: "user", content: "Hi" }])],
		script: [[[
			checkpoint([request("Hi"), reasoning("r1", "Thinking")], "RUNNING"),
			checkpoint([request("Hi"), reasoning("r1", "Thinking about it"), agent("a1", "Hello")], "RUNNING"),
			checkpoint([request("Hi"), reasoning("r1", "Thinking about it"), agent("a1", "Hello there")], "INPUT_REQUIRED", { total: 1234, max: 1_000_000 }),
		]]],
	},
	{
		name: "tool-loop",
		turns: [body([system, ask], [lookup]), body([system, ask, called("req-7"), result("req-7", "Sunny")], [lookup])],
		script: [[
			[checkpoint([request("What's the weather?"), reasoning("r1", "Let me look.")], "RUNNING"), action("req-7", "lookup", { query: "weather" })],
			[
				checkpoint([request("What's the weather?"), reasoning("r1", "Let me look."), toolEntry("Sunny"), agent("a1", "It is sunny.")], "RUNNING"),
				checkpoint([request("What's the weather?"), reasoning("r1", "Let me look."), toolEntry("Sunny"), agent("a1", "It is sunny.")], "FINISHED"),
			],
		]],
	},
	{
		name: "steer",
		turns: [body([system, ask], [lookup]), body([system, ask, called("req-7"), result("req-7", "Sunny"), { role: "user", content: "Actually, tomorrow?" }], [lookup])],
		script: [
			[[stuck, action("req-7", "lookup", { query: "weather" })]],
			[[checkpoint([request("Actually, tomorrow?"), agent("b1", "Tomorrow: rain.")], "INPUT_REQUIRED")]],
		],
	},
	{
		name: "step-limit",
		turns: [body([system, { role: "user", content: "Long task" }])],
		script: [
			[[checkpoint([request("Long task"), agent("a1", "Working")], "RUNNING"), failed("The workflow reached its maximum step limit and could not complete.")]],
			[[checkpoint([request("Long task"), agent("a1", "Working"), agent("a2", "Done")], "FINISHED")]],
		],
	},
	{
		name: "generic-error-exhausted",
		turns: [body([system, { role: "user", content: "Flaky" }])],
		script: [[[failed(generic)]], [[failed(generic)]]],
	},
	{
		name: "failed",
		turns: [body([system, { role: "user", content: "Break" }])],
		script: [[[checkpoint([request("Break"), agent("a1", "Partial")], "RUNNING"), failed("Model exploded")]]],
	},
	{
		name: "close-4400",
		turns: [body([system, { role: "user", content: "Rejected" }])],
		script: [[[{ close: 4400, reason: "Flow configuration failed security validation" }]]],
	},
	{
		name: "approval",
		turns: [body([system, { role: "user", content: "Needs approval" }])],
		script: [
			[[JSON.stringify({ workflowStatus: { status: "TOOL_CALL_APPROVAL_REQUIRED" } })]],
			[[checkpoint([request("Needs approval"), agent("a1", "Approved run")], "FINISHED")]],
		],
	},
	{
		name: "pause",
		turns: [body([system, { role: "user", content: "Two parts" }]), body([system, { role: "user", content: "Two parts" }, { role: "assistant", content: "Part one" }])],
		script: [[[
			checkpoint([request("Two parts"), agent("a1", "Part one"), toolEntry("t"), agent("a2", "Part two")], "RUNNING"),
			checkpoint([request("Two parts"), agent("a1", "Part one"), toolEntry("t"), agent("a2", "Part two!")], "FINISHED"),
		]]],
	},
	{
		name: "stall",
		turns: [body([system, ask], [lookup]), body([system, ask, called("req-7"), result("req-7", "Sunny")], [lookup])],
		script: [
			[[stuck, action("req-7", "lookup", { query: "weather" })], [stuck, action("req-8", "lookup", { query: "weather" })]],
			[[checkpoint([request("What's the weather?"), agent("c1", "Sunny again.")], "FINISHED")]],
		],
	},
];

const summarize = (message: any) => ({
	content: message.content.map((block: any) =>
		block.type === "text" ? { type: "text", text: block.text } : block.type === "thinking" ? { type: "thinking", thinking: block.thinking } : { type: block.type, id: block.id, name: block.name, arguments: block.arguments },
	),
	stopReason: message.stopReason,
	...(message.stopDetails ? { stopDetails: message.stopDetails } : {}),
	...(message.errorMessage ? { errorMessage: message.errorMessage } : {}),
	usage: { input: message.usage.input, totalTokens: message.usage.totalTokens },
});

const out: any[] = [];
let scenarioIndex = 0;
for (const scenario of scenarios) {
	script = scenario.script;
	sockets = [];
	const providerSessionState = new Map();
	const turns: any[] = [];
	for (const turnBody of scenario.turns) {
		rest = [];
		const socketsBefore = sockets.length;
		const sentBefore = sockets.map(socket => socket.sent.length);
		const parsed = parseRequest(turnBody);
		const events: any[] = [];
		let final: any = null;
		const stream = streamSimple(model, parsed.context, {
			// A key per scenario keeps NeoPi's per-account namespace cache apart.
			apiKey: `gitlab-oauth-runtime-${scenarioIndex}`,
			sessionId: `session-${scenario.name}`,
			fetch: loopbackFetch,
			providerSessionState,
			cwd,
		} as any);
		for await (const event of stream) {
			const entry: any = { type: event.type };
			if ("contentIndex" in event) entry.contentIndex = event.contentIndex;
			if ("delta" in event) entry.delta = event.delta;
			events.push(entry);
			if (event.type === "done") final = summarize(event.message);
			if (event.type === "error") final = summarize(event.error);
		}
		// Let a stop PATCH fired after the terminal event land.
		await Bun.sleep(20);
		turns.push({
			body: JSON.stringify(turnBody),
			rest,
			socketsOpened: sockets.slice(socketsBefore).map(socket => ({ url: socket.url, headers: socket.headers })),
			sent: sockets.map((socket, index) => ({ socket: index, frames: socket.sent.slice(sentBefore[index] ?? 0) })).filter(entry => entry.frames.length > 0),
			closed: sockets.map((socket, index) => (socket.closedByClient ? index : -1)).filter(index => index >= 0),
			events,
			final,
		});
	}
	out.push({ name: scenario.name, apiKey: `gitlab-oauth-runtime-${scenarioIndex}`, script: scenario.script, turns });
	scenarioIndex++;
}
restServer.stop(true);
console.log(JSON.stringify({ source: "NeoPi OWN gitlab-duo-agent streamSimple over a scripted Duo Workflow socket", model: { id: model.id, api: model.api, provider: model.provider, baseUrl: model.baseUrl }, scenarios: out }, null, 2));
