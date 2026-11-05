// Runs NeoPi's OWN Cursor provider the way its auth gateway does (an OpenAI
// chat body through openai-chat-server parseRequest, then streamSimple with
// the gateway's options) against a scripted loopback HTTP/2 Cursor server,
// recording every frame the client writes and every event it emits.
//
// Each scenario is a list of exchanges, one per Run request. An exchange is
// a script of server steps: send frames, await N client frames, end the
// stream (optionally with a Connect end-of-stream frame). The server bytes
// are recorded too, so the Go test replays the identical script.
//
// crypto.randomUUID is pinned to a counter and the process works in /tmp, so
// request ids, message ids and the shell working directory are reproducible.
//
//   bun capture_cursor_npi_runtime.ts > cursor_npi_runtime.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const http2 = await import("node:http2");
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { parseRequest } = await import(root + "/pi-ai/src/providers/openai-chat-server.ts");
const { isCursorExecResolved } = await import(root + "/pi-ai/src/utils/block-symbols.ts");
const { deterministicUuid } = await import(root + "/pi-ai/src/utils/deterministic-id.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { requireSupportedEffort, resolveWireModelId } = await import(root + "/pi-catalog/src/model-thinking.ts");
const P = await import(root + "/pi-catalog/src/discovery/cursor-proto.ts");
const { create, toBinary, encodeJsonValue } = await import(root + "/pi-catalog/src/discovery/protobuf.ts");

process.chdir("/tmp");
let uuidCounter = 0;
crypto.randomUUID = (() => `00000000-0000-4000-8000-${String(++uuidCounter).padStart(12, "0")}`) as any;

const apiKey = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjYXB0dXJlIn0.c2lnbmF0dXJl";
const b64 = (bytes: Uint8Array) => Buffer.from(bytes).toString("base64");
const frame = (payload: Uint8Array, flags = 0) => {
	const out = new Uint8Array(5 + payload.length);
	out[0] = flags;
	new DataView(out.buffer).setUint32(1, payload.length, false);
	out.set(payload, 5);
	return out;
};
const server = (message: any) => frame(toBinary(P.AgentServerMessageSchema, create(P.AgentServerMessageSchema, { message })));
const update = (c: string, value: any) => server({ case: "interactionUpdate", value: create(P.InteractionUpdateSchema, { message: { case: c, value } }) });
const exec = (id: number, execId: string, c: string, value: any) =>
	server({ case: "execServerMessage", value: create(P.ExecServerMessageSchema, { id, execId, message: { case: c, value } }) });
const kv = (id: number, c: string, value: any) => server({ case: "kvServerMessage", value: create(P.KvServerMessageSchema, { id, message: { case: c, value } }) });
const query = (id: number, c: string | undefined, value: any) =>
	server({ case: "interactionQuery", value: create(P.InteractionQuerySchema, { id, query: c ? { case: c, value } : { case: undefined } }) });
const checkpoint = (state: any) => server({ case: "conversationCheckpointUpdate", value: create(P.ConversationStateStructureSchema, state) });
const endStream = (body: unknown) => frame(new TextEncoder().encode(JSON.stringify(body)), 2);
const raw = (bytes: number[]) => frame(new Uint8Array(bytes));
const sha = (text: string) => new Bun.CryptoHasher("sha256").update(text).digest() as Uint8Array;
const mcpCall = (toolCallId: string, name: string, args: Record<string, unknown>) =>
	create(P.ToolCallSchema, {
		tool: {
			case: "mcpToolCall",
			value: create(P.McpToolCallSchema, {
				args: create(P.McpArgsSchema, {
					name,
					toolName: name,
					toolCallId,
					providerIdentifier: "pi-agent",
					args: Object.fromEntries(Object.entries(args).map(([k, v]) => [k, encodeJsonValue(v as any)])),
				}),
			}),
		},
	});
const turnEnded = (usage: any) => update("turnEnded", create(P.TurnEndedUpdateSchema, usage));
const text = (t: string) => update("textDelta", create(P.TextDeltaUpdateSchema, { text: t }));

const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
const lookup = {
	type: "function",
	function: {
		name: "lookup",
		description: "Look up a term.",
		parameters: {
			type: "object",
			properties: { q: { type: "string" }, mode: { anyOf: [{ type: "string" }, { type: "null" }] } },
			required: ["q"],
			anyOf: [{ properties: { q: { type: "string" } } }, { properties: { id: { type: "number" } }, required: ["id"] }],
		},
	},
};
const nativeTool = (name: string) => ({ type: "function", function: { name, description: `${name} tool`, parameters: { type: "object", properties: {} } } });

type Step = { send: Uint8Array[] } | { await: number } | { end: unknown | null };
type Scenario = { name: string; body: any; reasoning?: string; exchanges: Step[][] };
const scenarios: Scenario[] = [];

const terse = JSON.stringify({ role: "system", content: "Be terse." });
scenarios.push({
	name: "text",
	body: { model: "claude-4.5-sonnet", prompt_cache_key: "conv-a", messages: [{ role: "system", content: "Be terse." }, { role: "user", content: "Say hi." }] },
	exchanges: [
		[
			{ send: [text("Hel"), text("lo"), update("tokenDelta", create(P.TokenDeltaUpdateSchema, { tokens: 3 }))] },
			{
				send: [
					checkpoint({ rootPromptMessagesJson: [sha(terse)], previousWorkspaceUris: ["file:///w"], mode: 1, tokenDetails: { usedTokens: 120, maxTokens: 1000 } }),
					turnEnded({ inputTokens: 50n, outputTokens: 3n, cacheReadTokens: 10n }),
				],
			},
			{ end: {} },
		],
	],
});
scenarios.push({
	name: "history-handoff",
	reasoning: "high",
	body: {
		model: "claude-fable-5-high",
		prompt_cache_key: "conv-a",
		reasoning_effort: "high",
		tools: [lookup, nativeTool("bash"), nativeTool("write")],
		messages: [
			{ role: "system", content: "Be terse." },
			{ role: "user", content: "Say hi." },
			{ role: "assistant", content: "Hello", reasoning_content: "think", tool_calls: [{ id: "call|1", type: "function", function: { name: "lookup", arguments: '{"q":"x"}' } }] },
			{ role: "tool", tool_call_id: "call|1", content: "found" },
			{ role: "user", content: [{ type: "text", text: "Look:" }, { type: "image_url", image_url: { url: `data:image/png;base64,${png}` } }] },
		],
	},
	exchanges: [
		[
			{
				send: [
					update("thinkingDelta", create(P.ThinkingDeltaUpdateSchema, { text: "plan" })),
					update("thinkingCompleted", create(P.ThinkingCompletedUpdateSchema, {})),
					text("Checking."),
					update("toolCallStarted", create(P.ToolCallStartedUpdateSchema, { callId: "env1", toolCall: mcpCall("tc_1", "lookup", { q: "y" }) })),
					update("partialToolCall", create(P.PartialToolCallUpdateSchema, { callId: "env1", argsTextDelta: '{"q":' })),
					update("partialToolCall", create(P.PartialToolCallUpdateSchema, { callId: "env1", argsTextDelta: '{"q":"yy"}' })),
					update("toolCallCompleted", create(P.ToolCallCompletedUpdateSchema, { callId: "env1", toolCall: mcpCall("tc_1", "lookup", { q: "yy" }) })),
					exec(1, "ex1", "mcpArgs", create(P.McpArgsSchema, { name: "lookup", toolName: "lookup", toolCallId: "tc_1", providerIdentifier: "pi-agent", args: { q: encodeJsonValue("yy") } })),
				],
			},
			{ await: 1 },
			{ send: [exec(2, "ex2", "mcpArgs", create(P.McpArgsSchema, { name: "lookup", toolName: "lookup", toolCallId: "tc_2", providerIdentifier: "pi-agent", args: { q: encodeJsonValue("z") } }))] },
			{ await: 2 },
			{ send: [turnEnded({ inputTokens: 900n, outputTokens: 40n, cacheWriteTokens: 100n, reasoningTokens: 12n })] },
			{ end: {} },
		],
	],
});
scenarios.push({
	name: "native-burst",
	body: { model: "composer-2.5", prompt_cache_key: "conv-b", tools: [nativeTool("bash"), nativeTool("read"), lookup], messages: [{ role: "user", content: "Work." }] },
	exchanges: [
		[
			{
				send: [
					exec(2, "r", "readArgs", create(P.ReadArgsSchema, { path: "/a" })),
					exec(3, "s", "shellStreamArgs", create(P.ShellArgsSchema, { command: "ls", workingDirectory: "" })),
					exec(4, "g", "grepArgs", create(P.GrepArgsSchema, { pattern: "", glob: "*.go" })),
					exec(5, "c", "requestContextArgs", create(P.RequestContextArgsSchema, {})),
					exec(6, "m", "mcpStateExecArgs", create(P.McpStateExecArgsSchema, {})),
					exec(7, "h", "executeHookArgs", create(P.ExecuteHookArgsSchema, { request: create(P.ExecuteHookRequestSchema, { request: { case: "preToolUse", value: create(P.PreToolUseRequestQuerySchema, {}) } }) })),
					server({ case: "execServerMessage", value: create(P.ExecServerMessageSchema, { id: 8, execId: "u" }) }),
					exec(9, "p", "piBashArgs", create(P.PiBashExecArgsSchema, { command: "x" })),
					exec(10, "l", "listMcpResourcesExecArgs", create(P.ListMcpResourcesExecArgsSchema, {})),
					exec(11, "a", "mcpArgs", create(P.McpArgsSchema, { name: "lookup", toolName: "lookup", toolCallId: "tc_9", smartModeApprovalOnly: true, args: {} })),
					exec(16, "o", "mcpArgs", create(P.McpArgsSchema, { name: "lookup", toolName: "lookup", toolCallId: "tc_8", providerIdentifier: "pi-agent", args: { q: encodeJsonValue("burst") } })),
					exec(12, "w", "writeArgs", create(P.WriteArgsSchema, { path: "/b", fileText: "hi", toolCallId: "tw" })),
					exec(13, "q", "conversationSearchArgs", create(P.ConversationSearchArgsSchema, { query: "q" })),
					exec(14, "sh", "shellArgs", create(P.ShellArgsSchema, { command: "pwd", workingDirectory: "/srv" })),
					exec(15, "gd", "gitDiffRequest", create(P.GetDiffRequestSchema, {})),
					kv(1, "getBlobArgs", create(P.GetBlobArgsSchema, { blobId: sha("missing") })),
					kv(2, "setBlobArgs", create(P.SetBlobArgsSchema, { blobId: sha("x"), blobData: new TextEncoder().encode("x") })),
					kv(3, "getBlobArgs", create(P.GetBlobArgsSchema, { blobId: sha("x") })),
				],
			},
			{ await: 24 },
			{ send: [text("done"), turnEnded({ outputTokens: 1n })] },
			{ end: {} },
		],
	],
});
scenarios.push({
	name: "queries",
	body: { model: "composer-2.5", prompt_cache_key: "conv-c", messages: [{ role: "user", content: [{ type: "text", text: "Search." }] }] },
	exchanges: [
		[
			{
				send: [
					query(1, "webSearchRequestQuery", create(P.WebSearchRequestQuerySchema, {})),
					query(2, "askQuestionInteractionQuery", create(P.AskQuestionInteractionQuerySchema, {})),
					// AgentServerMessage{interactionQuery: {id: 3, field 20 = {}}}: an unmodelled query case.
					raw([0x3a, 0x05, 0x08, 0x03, 0xa2, 0x01, 0x00]),
					query(4, "setupVmEnvironmentArgs", create(P.SetupVmEnvironmentArgsSchema, {})),
					query(5, "createPlanRequestQuery", create(P.CreatePlanRequestQuerySchema, {})),
				],
			},
			{ await: 4 },
			{ send: [text("ok"), turnEnded({ inputTokens: 5n, outputTokens: 1n })] },
			{ end: {} },
		],
	],
});
const errorDetails = b64(toBinary(P.ErrorDetailsSchema, create(P.ErrorDetailsSchema, { error: 41, details: create(P.CustomErrorDetailsSchema, { title: "Too many", detail: " Slow down " }) })));
scenarios.push({
	name: "structured-error-rotation",
	body: { model: "composer-2.5", prompt_cache_key: "conv-d", messages: [{ role: "user", content: "Go." }] },
	exchanges: [[{ end: { error: { code: "resource_exhausted", message: "Error", details: [{ type: "aiserver.v1.ErrorDetails", value: errorDetails, debug: { error: "ERROR_RESOURCE_EXHAUSTED" } }] } } }]],
});
scenarios.push({
	name: "after-rotation",
	body: { model: "composer-2.5", prompt_cache_key: "conv-d", messages: [{ role: "user", content: "Go." }] },
	exchanges: [[{ send: [text("fresh"), turnEnded({ outputTokens: 1n })] }, { end: {} }]],
});
scenarios.push({
	name: "generic-error",
	body: { model: "composer-2.5", prompt_cache_key: "conv-g", messages: [{ role: "user", content: "Go." }] },
	exchanges: [[{ end: { error: { code: "unavailable", message: "Error", extra: { region: "x" } } } }]],
});
scenarios.push({
	name: "retry-fresh",
	body: { model: "composer-2.5", prompt_cache_key: "conv-e", messages: [{ role: "user", content: "Again." }] },
	exchanges: [[{ end: null }], [{ send: [text("second"), turnEnded({ outputTokens: 2n })] }, { end: {} }]],
});
scenarios.push({
	name: "retry-checkpoint",
	body: { model: "composer-2.5", prompt_cache_key: "conv-h", messages: [{ role: "user", content: "Resume." }] },
	exchanges: [
		[{ send: [text("part"), checkpoint({ rootPromptMessagesJson: [sha("x")], pendingToolCalls: ["p1"] })] }, { end: null }],
		[{ send: [text(" rest"), turnEnded({ outputTokens: 4n })] }, { end: {} }],
	],
});
scenarios.push({
	name: "not-found-fallback",
	reasoning: "high",
	body: { model: "gpt-5.4", prompt_cache_key: "conv-f", reasoning_effort: "high", messages: [{ role: "user", content: "Hi." }] },
	exchanges: [[{ end: { error: { code: "not_found", message: "model not found" } } }], [{ send: [text("found"), turnEnded({ outputTokens: 1n })] }, { end: {} }]],
});
scenarios.push({
	name: "step-completed",
	body: { model: "composer-2.5", messages: [{ role: "system", content: "S1" }, { role: "user", content: "Think." }] },
	exchanges: [[{ send: [text("answer"), update("stepCompleted", create(P.StepCompletedUpdateSchema, {}))] }, { end: null }]],
});

// The gateway's deriveSessionId (auth-gateway/server.ts, not exported).
function deriveSessionId(modelId: string, context: any): string {
	const parts: string[] = [modelId];
	if (context.systemPrompt && context.systemPrompt.length > 0) parts.push(context.systemPrompt.join("\n\n"));
	if (context.tools && context.tools.length > 0) parts.push(JSON.stringify(context.tools));
	const first = context.messages?.[0];
	if (first) parts.push(JSON.stringify({ role: first.role, content: first.content }));
	return deterministicUuid(parts.join("\u0000"));
}

const heartbeat = b64(toBinary(P.AgentClientMessageSchema, create(P.AgentClientMessageSchema, { message: { case: "clientHeartbeat", value: create(P.ClientHeartbeatSchema, {}) } })));
const decodeFrames = (buffer: Buffer) => {
	const frames: { flags: number; payload: string }[] = [];
	let offset = 0;
	while (offset + 5 <= buffer.length) {
		const length = buffer.readUInt32BE(offset + 1);
		if (offset + 5 + length > buffer.length) break;
		frames.push({ flags: buffer[offset], payload: b64(buffer.subarray(offset + 5, offset + 5 + length)) });
		offset += 5 + length;
	}
	return { frames, rest: buffer.subarray(offset) };
};

let pending: Step[][] = [];
let recorded: any[] = [];
const h2 = http2.createServer();
h2.on("stream", (stream: any, headers: any) => {
	const script = pending.shift();
	const record: any = { path: headers[":path"], headers: Object.fromEntries(Object.entries(headers).filter(([k]) => !k.startsWith(":") || k === ":method" || k === ":path")), frames: [] };
	recorded.push(record);
	let buffer = Buffer.alloc(0);
	let waiter: (() => void) | undefined;
	let wanted = 0;
	stream.on("data", (chunk: Buffer) => {
		buffer = Buffer.concat([buffer, chunk]);
		const { frames, rest } = decodeFrames(buffer);
		buffer = Buffer.from(rest);
		// Heartbeats ride a 5s timer; they are no part of the protocol trace.
		record.frames.push(...frames.filter(f => f.payload !== heartbeat));
		if (waiter && record.frames.length >= wanted) {
			const w = waiter;
			waiter = undefined;
			w();
		}
	});
	stream.respond({ ":status": 200, "content-type": "application/connect+proto" });
	(async () => {
		for (const step of script ?? []) {
			if ("send" in step) stream.write(Buffer.concat(step.send.map(f => Buffer.from(f))));
			else if ("await" in step) {
				// Count every client frame after the run request itself.
				wanted = step.await + 1;
				if (record.frames.length < wanted) await new Promise<void>(resolve => (waiter = resolve));
				await new Promise(resolve => setTimeout(resolve, 50));
			} else if (step.end === null) stream.end();
			else stream.end(Buffer.from(endStream(step.end)));
		}
	})();
});
await new Promise<void>(resolve => h2.listen(0, "127.0.0.1", resolve));
const baseUrl = `http://127.0.0.1:${(h2.address() as any).port}`;

const models = new Map(getBundledModels("cursor").map((model: any) => [model.id, model]));
const fixtures: any[] = [];
try {
	for (const scenario of scenarios) {
		uuidCounter = scenario.name === "after-rotation" ? uuidCounter : 0;
		const startCounter = uuidCounter;
		pending = scenario.exchanges.map(steps => steps);
		recorded = [];
		const parsed = parseRequest(scenario.body);
		const bundled = models.get(parsed.modelId);
		if (!bundled) throw new Error(`no bundled cursor model ${parsed.modelId}`);
		const model = { ...bundled, baseUrl };
		const key = parsed.options.promptCacheKey ?? deriveSessionId(parsed.modelId, parsed.context);
		const events: any[] = [];
		let final: any;
		const stream = streamSimple(model, parsed.context, {
			apiKey,
			signal: new AbortController().signal,
			cursorExternalToolExecutor: true,
			promptCacheKey: key,
			sessionId: key,
			reasoning: parsed.options.reasoning,
			providerRetryWait: async () => {},
		} as any);
		for await (const event of stream) {
			switch (event.type) {
				case "text_delta":
				case "thinking_delta":
					events.push({ type: event.type, delta: event.delta });
					break;
				case "toolcall_start": {
					const block = event.partial.content[event.contentIndex];
					events.push({ type: event.type, id: block.id, name: block.name, resolved: isCursorExecResolved(block) });
					break;
				}
				case "toolcall_delta":
					events.push({ type: event.type, delta: event.delta });
					break;
				case "toolcall_end":
					events.push({ type: event.type, id: event.toolCall.id, name: event.toolCall.name, arguments: JSON.stringify(event.toolCall.arguments), resolved: isCursorExecResolved(event.toolCall) });
					break;
				case "done":
					final = { stopReason: event.message.stopReason, usage: event.message.usage, content: event.message.content.map((b: any) => b.type) };
					break;
				case "error":
					final = { stopReason: event.error.stopReason, errorMessage: event.error.errorMessage, errorStatus: event.error.errorStatus };
					break;
			}
		}
		fixtures.push({
			name: scenario.name,
			body: scenario.body,
			conversationKey: key,
			uuidStart: startCounter,
			model: {
				id: model.id,
				name: model.name,
				requestModelId: model.requestModelId,
				cursorMaxMode: model.cursorMaxMode,
				cursorMaxModeRoutes: model.cursorMaxModeRoutes,
				cursorModelRoutes: model.cursorModelRoutes,
				cursorModelParameters: model.cursorModelParameters,
				effortRouting: model.thinking?.effortRouting,
				requiresCursorToolSchemaProjection: model.requiresCursorToolSchemaProjection === true,
			},
			// streamSimple's cursor branch: the effort-routed wire id.
			wireModelId: resolveWireModelId(model, parsed.options.reasoning && model.reasoning ? requireSupportedEffort(model, parsed.options.reasoning) : undefined),
			exchanges: scenario.exchanges.map(steps =>
				steps.map(step => ("send" in step ? { send: step.send.map(b64) } : "await" in step ? step : { end: step.end === null ? null : b64(endStream(step.end).subarray(5)) })),
			),
			requests: recorded,
			events,
			final,
		});
	}
} finally {
	h2.close();
}
console.log(JSON.stringify({ source: "NeoPi OWN pi-ai providers/cursor.ts via streamSimple (auth-gateway options)", cwd: process.cwd(), scenarios: fixtures }, (_k, v) => (typeof v === "bigint" ? Number(v) : v)));
