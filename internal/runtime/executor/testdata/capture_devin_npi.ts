// Runs NeoPi's OWN Devin provider (streamDevin through streamSimple, the agent
// entry) against a loopback fake of Cascade's GetUserJwt, AssignModel and
// GetChatMessage RPCs, recording every request it sends. crypto.randomUUID is
// pinned per scenario so the execution id is reproducible.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { gunzipSync } = await import("node:zlib");
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const proto = await import(root + "/pi-catalog/src/discovery/devin-proto.ts");
const { create, toBinary } = await import(root + "/pi-catalog/src/discovery/protobuf.ts");
const { neoPiRecord } = await import(import.meta.dir + "/capture_devin_npi_models.ts");

const apiKey = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjYXB0dXJlIn0.c2lnbmF0dXJl";
const sessionId = "capture-cascade-0001";

let uuidCounter = 0;
crypto.randomUUID = (() => `00000000-0000-4000-8000-${String(++uuidCounter).padStart(12, "0")}`) as any;

const frame = (payload: Uint8Array, flags = 0) => {
	const out = new Uint8Array(5 + payload.length);
	out[0] = flags;
	new DataView(out.buffer).setUint32(1, payload.length);
	out.set(payload, 5);
	return out;
};
let assignedUid = "swe-1-6";
let captured: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const body = new Uint8Array(await request.arrayBuffer());
		const entry: any = { method: request.method, path: url.pathname, headers: Object.fromEntries(request.headers), body: Buffer.from(body).toString("base64") };
		if (url.pathname.endsWith("/GetChatMessage")) {
			const flags = body[0];
			const payload = body.subarray(5);
			entry.frameFlags = flags;
			entry.proto = Buffer.from(flags & 1 ? gunzipSync(payload) : payload).toString("base64");
		} else {
			entry.proto = entry.body;
		}
		captured.push(entry);
		if (url.pathname.endsWith("/GetUserJwt")) {
			return new Response(toBinary(proto.GetUserJwtResponseSchema, create(proto.GetUserJwtResponseSchema, { userJwt: "user-jwt-capture" })), { headers: { "content-type": "application/proto" } });
		}
		if (url.pathname.endsWith("/AssignModel")) {
			const assignment = create(proto.ModelAssignmentSchema, { assignmentJwt: "assignment-jwt-capture", modelUid: assignedUid });
			return new Response(toBinary(proto.AssignModelResponseSchema, create(proto.AssignModelResponseSchema, { assignment })), { headers: { "content-type": "application/proto" } });
		}
		const message = create(proto.GetChatMessageResponseSchema, { messageId: "resp-capture", deltaText: "Captured." });
		const chunks = [frame(toBinary(proto.GetChatMessageResponseSchema, message)), frame(new TextEncoder().encode("{}"), 2)];
		return new Response(new Blob(chunks), { headers: { "content-type": "application/connect+proto" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;

const seed = new Map(getBundledModels("devin").map((model: any) => [model.id, model]));
const base = { api: "devin-agent", provider: "devin", baseUrl: "https://server.codeium.com", supportsTools: true, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 200000 };
const models: Record<string, any> = {
	"swe-1-6": seed.get("swe-1-6"),
	"gpt-5-6-sol": buildModel({ ...base, id: "gpt-5-6-sol", name: "GPT-5.6 Sol", reasoning: true, input: ["text", "image"], maxTokens: 64000, requestModelId: "gpt-5-6-sol-high", thinking: { mode: "effort", efforts: ["low", "high"], effortRouting: { low: "gpt-5-6-sol-low", high: "gpt-5-6-sol-high" }, requiresEffort: true }, compat: { supportsParallelToolCalls: true } } as any),
	adaptive: buildModel({ ...base, id: "adaptive", name: "Adaptive", reasoning: false, input: ["text"], maxTokens: 64000, compat: { modelRouter: true } } as any),
	"gemini-3-1-pro": buildModel({ ...base, id: "gemini-3-1-pro", name: "Gemini 3.1 Pro", reasoning: false, input: ["text", "image"], maxTokens: 65536 } as any),
};

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" }, limit: { type: ["number", "null"], description: "Max hits." } }, required: ["term"] } };
const claudeTool = { name: lookup.name, description: lookup.description, input_schema: lookup.parameters };
const usage = { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";

// Each scenario: a NeoPi context and the Claude Messages request a client sends
// NeoProxy for the same conversation. Assistant turns are the model's own.
const user = (content: any) => ({ role: "user", content, timestamp: 0 });
const assistant = (model: any, content: any[], stopReason = "toolUse") => ({ role: "assistant", content, api: "devin-agent", provider: "devin", model: model.id, usage, stopReason, timestamp: 0 });
const result = (id: string, content: any[], isError = false) => ({ role: "toolResult", toolCallId: id, toolName: "lookup", content, isError, timestamp: 0 });

const scenarios: any[] = [];
const add = (name: string, modelId: string, context: any, downstream: any, options: any = {}) => scenarios.push({ name, model: modelId, context, downstream: { model: modelId, stream: true, ...downstream }, options });

add("plain", "swe-1-6", { messages: [user("Say hello.")] }, { messages: [{ role: "user", content: "Say hello." }] });
add("system-tools", "swe-1-6", { systemPrompt: ["First system.", "Second system."], messages: [user("Use lookup.")], tools: [lookup] }, { system: [{ type: "text", text: "First system." }, { type: "text", text: "Second system." }], messages: [{ role: "user", content: "Use lookup." }], tools: [claudeTool] });
add("sampling", "swe-1-6", { messages: [user("Count.")] }, { messages: [{ role: "user", content: "Count." }], max_tokens: 777, temperature: 0.3, top_p: 0.5, stop_sequences: ["END", "STOP"] }, { maxTokens: 777, temperature: 0.3, topP: 0.5, stopSequences: ["END", "STOP"] });
{
	const m = models["swe-1-6"];
	add("tool-history", "swe-1-6", {
		messages: [
			user("Use lookup."),
			assistant(m, [{ type: "thinking", thinking: "Need lookup.", thinkingSignature: "sig-capture" }, { type: "text", text: "Looking." }, { type: "toolCall", id: "call_capture_1", name: "lookup", arguments: { term: "a" } }]),
			result("call_capture_1", [{ type: "text", text: "Found." }]),
		],
		tools: [lookup],
	}, {
		messages: [
			{ role: "user", content: "Use lookup." },
			{ role: "assistant", content: [{ type: "thinking", thinking: "Need lookup.", signature: "sig-capture" }, { type: "text", text: "Looking." }, { type: "tool_use", id: "call_capture_1", name: "lookup", input: { term: "a" } }] },
			{ role: "user", content: [{ type: "tool_result", tool_use_id: "call_capture_1", content: [{ type: "text", text: "Found." }] }] },
		],
		tools: [claudeTool],
	});
	add("tool-result-error-image", "swe-1-6", {
		messages: [
			user("Shoot."),
			assistant(m, [{ type: "toolCall", id: "call_capture_2", name: "lookup", arguments: { term: "b" } }]),
			result("call_capture_2", [{ type: "text", text: "Failed." }, { type: "image", data: png, mimeType: "image/png" }], true),
		],
		tools: [lookup],
	}, {
		messages: [
			{ role: "user", content: "Shoot." },
			{ role: "assistant", content: [{ type: "tool_use", id: "call_capture_2", name: "lookup", input: { term: "b" } }] },
			{ role: "user", content: [{ type: "tool_result", tool_use_id: "call_capture_2", is_error: true, content: [{ type: "text", text: "Failed." }, { type: "image", source: { type: "base64", media_type: "image/png", data: png } }] }] },
		],
		tools: [claudeTool],
	});
	add("missing-tool-result", "swe-1-6", {
		messages: [user("Use lookup."), assistant(m, [{ type: "toolCall", id: "call_capture_3", name: "lookup", arguments: { term: "c" } }]), user("Never mind.")],
		tools: [lookup],
	}, {
		messages: [
			{ role: "user", content: "Use lookup." },
			{ role: "assistant", content: [{ type: "tool_use", id: "call_capture_3", name: "lookup", input: { term: "c" } }] },
			{ role: "user", content: "Never mind." },
		],
		tools: [claudeTool],
	});
	const long = "lorem ipsum ".repeat(200);
	add("large-compressed", "swe-1-6", { messages: [user(long), assistant(m, [{ type: "text", text: long }], "stop"), user("Again.")] }, { messages: [{ role: "user", content: long }, { role: "assistant", content: [{ type: "text", text: long }] }, { role: "user", content: "Again." }] });
}
add("user-image", "gpt-5-6-sol", { messages: [user([{ type: "text", text: "Describe." }, { type: "image", data: png, mimeType: "image/png" }])] }, { messages: [{ role: "user", content: [{ type: "text", text: "Describe." }, { type: "image", source: { type: "base64", media_type: "image/png", data: png } }] }], thinking: { type: "adaptive" }, output_config: { effort: "high" } }, { reasoning: "high" });
add("effort-low", "gpt-5-6-sol", { messages: [user("Think a little.")], tools: [lookup] }, { messages: [{ role: "user", content: "Think a little." }], tools: [claudeTool], thinking: { type: "adaptive" }, output_config: { effort: "low" } }, { reasoning: "low" });
add("router", "adaptive", { systemPrompt: ["Route me."], messages: [user("Earlier."), assistant(models.adaptive, [{ type: "text", text: "Ok." }], "stop"), user("Pick a model.")] }, { system: "Route me.", messages: [{ role: "user", content: "Earlier." }, { role: "assistant", content: [{ type: "text", text: "Ok." }] }, { role: "user", content: "Pick a model." }] }, { assign: "swe-1-6" });
add("router-gemini-tools", "adaptive", { messages: [user("Use lookup.")], tools: [lookup] }, { messages: [{ role: "user", content: "Use lookup." }], tools: [claudeTool] }, { assign: "MODEL_GOOGLE_GEMINI_2_5_PRO" });
add("gemini-tools", "gemini-3-1-pro", { messages: [user("Use lookup.")], tools: [lookup] }, { messages: [{ role: "user", content: "Use lookup." }], tools: [claudeTool] });

const fixtures: any[] = [];
try {
	for (const scenario of scenarios) {
		captured = [];
		uuidCounter = 0;
		assignedUid = scenario.options.assign ?? "swe-1-6";
		const model = { ...models[scenario.model], baseUrl: local };
		const { assign: _assign, ...options } = scenario.options;
		const stream = streamSimple(model, scenario.context, { apiKey, sessionId, ...options });
		let finalMessage: any;
		for await (const event of stream) {
			if (event.type === "done") finalMessage = event.message;
			if (event.type === "error") throw new Error(`${scenario.name}: ${event.error.errorMessage}`);
		}
		fixtures.push({ name: scenario.name, model: scenario.model, assign: assignedUid, downstream: scenario.downstream, requests: captured, reply: finalMessage?.content });
	}
	console.log(JSON.stringify({ source: "NeoPi OWN streamDevin via streamSimple (loopback Cascade fake)", apiKey, sessionId, models: Object.fromEntries(Object.entries(models).map(([id, m]: any) => [id, neoPiRecord(m)])), fixtures }, null, 2));
} finally {
	server.stop(true);
}
