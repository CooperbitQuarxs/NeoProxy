// Runs NeoPi's OWN streamSimple over every bundled GitLab Duo row against a
// loopback capture server only; the GitLab token is fake and no request can
// reach GitLab. streamGitLabDuo mints a direct-access token at gitlab.com and
// then dispatches to the route apiRouteFor("gitlab-duo", id) names on
// cloud.gitlab.com; both hosts are answered in process. Each scenario starts
// with an empty direct-access cache, so every fixture records the mint and
// the model request, beside the downstream request a client sends NeoProxy in
// the routed wire's format (Responses through NeoPi's own server boundary).
// Also records the direct-access edge cases: rejected and malformed mints,
// cache reuse, TTL expiry, and caller headers over the minted ones.
// Writes testdata/neopi_gitlab_duo.json.gz.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_gitlab_duo.ts
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { clearGitLabDuoDirectAccessCache } = await import(root + "/pi-ai/src/providers/gitlab-duo.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { apiRouteFor } = await import(root + "/pi-catalog/src/compat/behavior.ts");
const { parseRequest } = await import(root + "/pi-ai/src/providers/openai-responses-server.ts");
// The Anthropic route's device_id derives from this installation's id.
const { getInstallId } = await import(root + "/pi-utils/src/dirs.ts");

// A GitLab OAuth access token, exchanged for a direct-access token per stream.
const apiKey = "gitlab-oauth-capture-token-0000000000000000";
const sessionId = "capture-session-gitlab-duo-0001";
const directAccessToken = "duo-direct-access-capture-token";
const directAccessHeaders = {
	"x-gitlab-host-name": "gitlab.com",
	"x-gitlab-instance-id": "ea8bf810-1d6f-4a6a-b4fd-93e8cbd8b57f",
	"x-gitlab-realm": "saas",
	"x-gitlab-version": "18.6.0",
	"x-gitlab-global-user-id": "capture-global-user",
	"x-gitlab-feature-enabled-by-namespace-ids": "1234",
};

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

// The direct-access answer the current scenario wants.
let mint: () => Response = () => Response.json({ token: directAccessToken, headers: directAccessHeaders });
let captured: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		captured.push({ method: request.method, host: request.headers.get("host"), path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody: await request.text() });
		if (url.pathname === "/api/v4/ai/third_party_agents/direct_access") return mint();
		const sse = url.pathname.endsWith("/messages") ? anthropicSse : url.pathname.endsWith("/responses") ? responsesSse : completionsSse;
		return new Response(sse, { headers: { "content-type": "text/event-stream" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;
const redirectFetch = (async (input: any, init: any) => {
	const target = input instanceof Request ? input.url : String(input);
	const u = new URL(target);
	if (u.origin !== "https://gitlab.com" && u.origin !== "https://cloud.gitlab.com") throw new Error("Unexpected non-capture request " + target);
	const headers = new Headers(init?.headers ?? (input instanceof Request ? input.headers : undefined));
	headers.set("host", u.host);
	const body = init?.body ?? (input instanceof Request ? await input.text() : undefined);
	return fetch(local + u.pathname + u.search, { method: init?.method ?? (input instanceof Request ? input.method : "GET"), body, headers, tls: undefined });
}) as typeof fetch;

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const webSearch = { name: "web_search", description: "Search the web.", parameters: { type: "object", properties: { query: { type: "string" } }, required: ["query"] } };
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";

type Turn = { kind: "user"; text?: string; image?: boolean } | { kind: "call"; tool?: string } | { kind: "result"; text: string; tool?: string };
const hello: Turn[] = [{ kind: "user", text: "Say hello." }];
const ask: Turn[] = [{ kind: "user", text: "Use a tool." }];
const historyOf = (tool?: string): Turn[] => [{ kind: "user", text: "Use a tool." }, { kind: "call", tool }, { kind: "result", text: "Found.", tool }];
const describe: Turn[] = [{ kind: "user", text: "Describe.", image: true }];
const toolArgs = (tool: string) => (tool === "web_search" ? { query: "a" } : { term: "a" });

const neopiTurns = (model: any, turns: Turn[], withThinking: boolean) =>
	turns.map(turn => {
		if (turn.kind === "user")
			return { role: "user", content: turn.image ? [{ type: "text", text: turn.text }, { type: "image", data: png, mimeType: "image/png" }] : turn.text, timestamp: 0 };
		if (turn.kind === "call")
			return {
				role: "assistant",
				content: [
					...(withThinking ? [{ type: "thinking", thinking: "Need a tool.", thinkingSignature: "" }] : []),
					{ type: "toolCall", id: "call_capture_1", name: turn.tool ?? "lookup", arguments: toolArgs(turn.tool ?? "lookup") },
				],
				api: model.api,
				provider: model.provider,
				model: model.id,
				usage,
				stopReason: "toolUse",
				timestamp: 0,
			};
		return { role: "toolResult", toolCallId: "call_capture_1", toolName: turn.tool ?? "lookup", content: [{ type: "text", text: turn.text }], isError: false, timestamp: 0 };
	});

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
						{ type: "thinking", thinking: "Need a tool.", signature: "" },
						{ type: "tool_use", id: "call_capture_1", name: turn.tool ?? "lookup", input: toolArgs(turn.tool ?? "lookup") },
					],
				};
			return { role: "user", content: [{ type: "tool_result", tool_use_id: "call_capture_1", content: [{ type: "text", text: turn.text }] }] };
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
	if (opts.toolChoice !== undefined) body.tool_choice = opts.toolChoice;
	return body;
};
const downstreamFor = (route: string) => (route === "anthropic-messages" ? claudeDownstream : route === "openai-responses" ? responsesDownstream : chatDownstream);

const routeOf = (model: any) => apiRouteFor("gitlab-duo", model.id)?.api ?? "";
const scenario = (model: any, name: string, turns: Turn[], opts: any, extra: any = {}) => {
	const route = routeOf(model);
	const downstream = downstreamFor(route)(model, turns, opts, extra);
	const context = route === "openai-responses"
		? parseRequest(downstream).context
		: { ...(extra.system ? { systemPrompt: [extra.system] } : {}), messages: neopiTurns(model, turns, route === "anthropic-messages"), ...(extra.tools ? { tools: extra.tools } : {}) };
	return { name: `${model.id}/${name}`, model, route, context, options: opts, downstream };
};

// One stream; returns the requests it made and how it ended.
const run = async (s: any, options: any = {}) => {
	captured = [];
	const result = await streamSimple(s.model, s.context, { apiKey, sessionId, fetch: redirectFetch, ...s.options, ...options }).result();
	return { requests: captured, stopReason: result.stopReason, errorMessage: result.errorMessage };
};

const models = getBundledModels("gitlab-duo");
const fixtures: any[] = [];
const edges: any[] = [];
try {
	for (const model of models) {
		const route = routeOf(model);
		const efforts: string[] = model.thinking?.efforts ?? [];
		const scenarios = [
			scenario(model, "plain", hello, {}),
			...efforts.map(effort => scenario(model, `effort-${effort}`, hello, { reasoning: effort })),
			scenario(model, "off", hello, { disableReasoning: true }),
			scenario(model, "off-sampling", hello, { disableReasoning: true, temperature: 0.3, maxTokens: 300 }),
			scenario(model, "system-tool", ask, { reasoning: efforts[0], maxTokens: 512 }, { system: "A source system.", tools: [lookup] }),
			scenario(model, "tool-history", historyOf(), { reasoning: efforts[0] }, { tools: [lookup] }),
			scenario(model, "user-image", describe, { reasoning: efforts[0] }),
		];
		if (route === "anthropic-messages") {
			scenarios.push(scenario(model, "builtin-tool-name", historyOf("web_search"), { reasoning: efforts[0] }, { tools: [lookup, webSearch] }));
			scenarios.push(scenario(model, "forced-any", ask, { reasoning: efforts[0], toolChoice: "any" }, { tools: [lookup] }));
			scenarios.push(scenario(model, "forced-named", ask, { reasoning: efforts[0], toolChoice: { type: "tool", name: "lookup" } }, { tools: [lookup] }));
		} else if (route === "openai-responses") {
			scenarios.push(scenario(model, "forced-named", ask, { reasoning: efforts[0], toolChoice: { type: "function", name: "lookup" } }, { tools: [lookup] }));
		} else {
			scenarios.push(scenario(model, "forced-named", ask, { reasoning: efforts[0], toolChoice: { type: "function", function: { name: "lookup" } } }, { tools: [lookup] }));
		}
		for (const s of scenarios) {
			clearGitLabDuoDirectAccessCache();
			const got = await run(s);
			if (got.stopReason !== "stop") throw new Error(`Capture ${s.name} failed: ${got.errorMessage}`);
			if (got.requests.length !== 2) throw new Error(`Capture ${s.name} saw ${got.requests.length} requests`);
			fixtures.push({ name: s.name, model: model.id, route: s.route, options: s.options, downstream: s.downstream, mint: got.requests[0], request: got.requests[1] });
		}
	}

	// Direct-access edges, once per route.
	const byRoute = new Map<string, any>();
	for (const model of models) if (!byRoute.has(routeOf(model))) byRoute.set(routeOf(model), model);
	const realNow = Date.now;
	for (const [route, model] of byRoute) {
		const s = scenario(model, "edge", hello, {});
		const edge = async (name: string, answer: () => Response, options: any = {}) => {
			clearGitLabDuoDirectAccessCache();
			mint = answer;
			const got = await run(s, options);
			edges.push({ name: `${route}/${name}`, model: model.id, route, downstream: s.downstream, options, ...got });
		};
		await edge("denied-403", () => new Response("duo disabled", { status: 403 }));
		await edge("failed-500", () => new Response("boom", { status: 500 }));
		await edge("missing-token", () => Response.json({ headers: directAccessHeaders }));
		await edge("missing-headers", () => Response.json({ token: directAccessToken }));
		await edge("caller-headers", () => Response.json({ token: directAccessToken, headers: directAccessHeaders }), { headers: { "x-gitlab-realm": "caller-realm", "X-Caller": "present" } });
		mint = () => Response.json({ token: directAccessToken, headers: directAccessHeaders });
		// Cache: a second stream within the TTL reuses the token; after it, re-mints.
		clearGitLabDuoDirectAccessCache();
		const first = await run(s);
		const reused = await run(s);
		let now = realNow();
		Date.now = () => now;
		now += 25 * 60 * 1000 + 1;
		const expired = await run(s);
		Date.now = realNow;
		edges.push({ name: `${route}/cache`, model: model.id, route, downstream: s.downstream, first: first.requests.map(r => r.path), reused: reused.requests.map(r => r.path), expired: expired.requests.map(r => r.path) });
	}
	await Bun.write(join(import.meta.dir, "neopi_gitlab_duo.json.gz"), Bun.gzipSync(JSON.stringify({ source: "NeoPi OWN streamSimple over bundled gitlab-duo rows", installId: getInstallId(), apiKey, sessionId, directAccessToken, directAccessHeaders, fixtures, edges })));
	console.log(`gitlab-duo: ${models.length} models, ${fixtures.length} fixtures, ${edges.length} edges`);
	for (const e of edges) console.log(e.name, e.stopReason ?? "", (e.errorMessage ?? "").slice(0, 160), e.requests?.length ?? "", e.first ?? "", e.reused ?? "", e.expired ?? "");
} finally {
	server.stop(true);
}
