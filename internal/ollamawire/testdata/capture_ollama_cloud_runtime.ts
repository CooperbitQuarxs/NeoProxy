// Runs NeoPi's OWN Ollama Cloud provider the way its auth gateway does: an
// OpenAI chat body through openai-chat-server parseRequest, then streamSimple
// over the bundled ollama-cloud row with the gateway's options. Ollama's
// /api/chat is a scripted loopback server (through the request's fetch, so
// transportFetch shapes the wire); every request it receives and every event
// the stream emits are recorded. Writes ollama_cloud_runtime.json.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_ollama_cloud_runtime.ts
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { parseRequest } = await import(root + "/pi-ai/src/providers/openai-chat-server.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");

const API_KEY = "ollama-cloud-key-fixture";
const models = new Map(getBundledModels("ollama-cloud").map((model: any) => [model.id, model]));

type Reply = { status: number; body: string; headers?: Record<string, string> };
let replies: Reply[] = [];
let received: any[] = [];
const server = Bun.serve({
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const headers: Record<string, string> = {};
		request.headers.forEach((value, key) => {
			headers[key] = value;
		});
		received.push({ method: request.method, path: url.pathname, headers, body: await request.text() });
		const reply = replies.shift();
		if (!reply) return new Response("no scripted reply", { status: 599 });
		return new Response(reply.body, { status: reply.status, headers: { "content-type": "application/x-ndjson", ...reply.headers } });
	},
});
const nativeFetch = globalThis.fetch;
const loopbackFetch = (async (input: any, init?: any) => {
	const url = new URL(input instanceof Request ? input.url : String(input));
	if (url.origin !== "https://ollama.com") throw new Error("Unexpected Ollama URL: " + url.href);
	return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`, init);
}) as typeof fetch;

const ndjson = (...lines: unknown[]) => lines.map(line => (typeof line === "string" ? line : JSON.stringify(line))).join("\n") + "\n";
const msg = (message: Record<string, unknown>) => ({ model: "m", created_at: "2026-11-05T00:00:00Z", message: { role: "assistant", content: "", ...message }, done: false });
const done = (reason: string | undefined, extra: Record<string, unknown> = {}) => ({
	model: "m",
	created_at: "2026-11-05T00:00:01Z",
	message: { role: "assistant", content: "" },
	done: true,
	...(reason !== undefined ? { done_reason: reason } : {}),
	prompt_eval_count: 120,
	eval_count: 30,
	...extra,
});

const lookup = { type: "function", function: { name: "lookup", description: "Look a thing up.", parameters: { type: "object", properties: { query: { type: "string" }, limit: { type: ["integer", "null"] }, extra: {} }, required: ["query"], additionalProperties: false } } };
const write = { type: "function", function: { name: "write_file", description: "Write a file.", parameters: { type: "object", properties: { path: { type: "string" }, content: { type: "string" } }, required: ["path", "content"] } } };
const image = "data:image/png;base64,iVBORw0KGgo=";

const scenarios: { name: string; body: any; replies: Reply[] }[] = [
	{
		name: "text-thinking-usage",
		body: { model: "gpt-oss:120b", messages: [{ role: "system", content: "Be brief." }, { role: "user", content: "Hi" }], reasoning_effort: "high", temperature: 0.3, top_p: 0.9, stream: true },
		replies: [{ status: 200, body: ndjson(msg({ thinking: "Thinking " }), msg({ thinking: "more." }), msg({ content: "Hello" }), msg({ content: " there!" }), done("stop", { prompt_eval_cached_count: 100 })) }],
	},
	{
		name: "minimal-effort-no-system",
		body: { model: "glm-5.1", messages: [{ role: "user", content: [{ type: "text", text: "Part one." }, { type: "text", text: "Part two." }] }], reasoning_effort: "minimal", max_tokens: 99999 },
		replies: [{ status: 200, body: ndjson(msg({ content: "ok" }), done("stop")) }],
	},
	{
		name: "effort-none-and-unsupported-effort",
		body: { model: "deepseek-v4-pro", messages: [{ role: "user", content: "Go" }], reasoning_effort: "none" },
		replies: [{ status: 200, body: ndjson(msg({ content: "ok" }), done("stop")) }],
	},
	{
		name: "effort-max-deepseek",
		body: { model: "deepseek-v4-pro", messages: [{ role: "user", content: "Go" }], reasoning_effort: "max" },
		replies: [{ status: 200, body: ndjson(msg({ thinking: "deep" }), msg({ content: "done" }), done("stop")) }],
	},
	{
		name: "effort-on-non-reasoning-row",
		body: { model: "qwen3-coder:480b", messages: [{ role: "user", content: "Go" }], reasoning_effort: "high" },
		replies: [{ status: 200, body: ndjson(msg({ content: "ok" }), done("stop")) }],
	},
	{
		name: "tools-structured-object-args",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Find cats" }], tools: [lookup, write], tool_choice: "auto" },
		replies: [{ status: 200, body: ndjson(msg({ content: "Let me look." }), msg({ tool_calls: [{ function: { name: "lookup", arguments: { query: "cats", limit: 2 } } }] }), done("stop")) }],
	},
	{
		name: "named-tool-choice-string-args",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Write it" }], tools: [lookup, write], tool_choice: { type: "function", function: { name: "write_file" } } },
		replies: [{ status: 200, body: ndjson(msg({ tool_calls: [{ function: { name: "write_file", arguments: '{"path":"a.txt","content":"hi"}' } }, { function: { arguments: null } }] }), done("tool_calls")) }],
	},
	{
		name: "required-and-missing-named-tool",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Use something" }], tools: [lookup], tool_choice: { type: "tool", name: "absent" } },
		replies: [{ status: 200, body: ndjson(msg({ content: "none fit" }), done(undefined)) }],
	},
	{
		name: "tool-choice-none-and-required",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Answer" }], tools: [lookup], tool_choice: "none" },
		replies: [{ status: 200, body: ndjson(msg({ content: "answer" }), done("stop")) }],
	},
	{
		name: "history-transform",
		body: {
			model: "kimi-k2.6",
			messages: [
				{ role: "system", content: "You are a helper." },
				{ role: "user", content: [{ type: "text", text: "Look at this" }, { type: "image_url", image_url: { url: image } }] },
				{ role: "assistant", content: "Checking.", reasoning_content: "I should look it up.  ", tool_calls: [
					{ id: "call_1", type: "function", function: { name: "lookup", arguments: '{"query":"x"}' } },
					{ id: "call_1", type: "function", function: { name: "lookup", arguments: '{"query":"y"}' } },
					{ id: "call_2", type: "function", function: { name: "write_file", arguments: "not json" } },
					{ id: "call_bad", type: "function", function: { name: "bad name!", arguments: "{}" } },
				] },
				{ role: "tool", tool_call_id: "call_1", content: "x result" },
				{ role: "tool", tool_call_id: "call_1", content: [{ type: "text", text: "y result" }, { type: "image_url", image_url: { url: image } }] },
				{ role: "tool", tool_call_id: "call_bad", content: "bad result" },
				{ role: "tool", tool_call_id: "orphan", name: "ghost", content: "stale text" },
				{ role: "assistant", content: null, reasoning_content: "   " },
				{ role: "developer", content: "Developer note." },
				{ role: "user", content: "Continue" },
			],
			tools: [lookup, write],
		},
		replies: [{ status: 200, body: ndjson(msg({ content: "Continuing." }), done("stop")) }],
	},
	{
		name: "history-no-vision-placeholder",
		body: { model: "glm-5.2", messages: [{ role: "user", content: [{ type: "image_url", image_url: { url: image } }] }, { role: "assistant", content: "Seen.", reasoning_content: "glm thinking" }, { role: "user", content: [{ type: "text", text: "Again" }, { type: "image_url", image_url: { url: image } }] }] },
		replies: [{ status: 200, body: ndjson(msg({ content: "ok" }), done("stop")) }],
	},
	{
		name: "no-user-turn-demotes-developer",
		body: { model: "gpt-oss:120b", messages: [{ role: "system", content: "Sys." }, { role: "developer", content: "Plan approved." }] },
		replies: [{ status: 200, body: ndjson(msg({ content: "Starting." }), done("stop")) }],
	},
	{
		name: "leaked-thinking-healed",
		body: { model: "qwen3-next:80b", messages: [{ role: "user", content: "Hi" }] },
		replies: [{ status: 200, body: ndjson(msg({ content: "<think>hidden " }), msg({ content: "reasoning</think>Visible" }), msg({ content: " answer" }), done("stop")) }],
	},
	{
		name: "native-thinking-suppresses-healed",
		body: { model: "qwen3-next:80b", messages: [{ role: "user", content: "Hi" }] },
		replies: [{ status: 200, body: ndjson(msg({ thinking: "native" }), msg({ content: "<think>dup</think>Answer" }), done("stop")) }],
	},
	{
		name: "kimi-leaked-tool-call",
		body: { model: "kimi-k2-thinking", messages: [{ role: "user", content: "Look" }], tools: [lookup] },
		replies: [{ status: 200, body: ndjson(msg({ content: "Sure.<|tool_calls_section_begin|><|tool_call_begin|>functions.lookup:0<|tool_call_argument_begin|>" }), msg({ content: '{"query":"k"}<|tool_call_end|><|tool_calls_section_end|>' }), done("stop")) }],
	},
	{
		name: "length-empty-is-error",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Long" }] },
		replies: [{ status: 200, body: ndjson(done("length", { eval_count: 0 })) }],
	},
	{
		name: "length-with-content",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Long" }] },
		replies: [{ status: 200, body: ndjson(msg({ content: "partial" }), done("length")) }],
	},
	{
		name: "load-is-error",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Hi" }] },
		replies: [{ status: 200, body: ndjson(done("load", { eval_count: 0 })) }],
	},
	{
		name: "stream-error-record",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Hi" }] },
		replies: [{ status: 200, body: ndjson(msg({ content: "start" }), { error: "model crashed" }) }],
	},
	{
		name: "empty-completion-retried",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Hi" }] },
		replies: [
			{ status: 200, body: ndjson(done("stop", { eval_count: 1 })) },
			{ status: 200, body: ndjson(msg({ content: "  " }), done("stop", { eval_count: 1 })) },
		],
	},
	{
		name: "empty-completion-exhausted",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Hi" }] },
		replies: [
			{ status: 200, body: ndjson(done("stop", { eval_count: 0 })) },
			{ status: 200, body: ndjson(done("stop", { eval_count: 0 })) },
			{ status: 200, body: ndjson(done("stop", { eval_count: 0 })) },
		],
	},
	{
		name: "http-503-retried",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Hi" }] },
		replies: [
			{ status: 503, body: '{"error":"busy"}', headers: { "retry-after": "0" } },
			{ status: 200, body: ndjson(msg({ content: "ok" }), done("stop")) },
		],
	},
	{
		name: "http-500-tool-parse-not-retried",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Hi" }] },
		replies: [{ status: 500, body: '{"error":"failed to parse tool call arguments as JSON"}' }],
	},
	{
		name: "http-401",
		body: { model: "gpt-oss:120b", messages: [{ role: "user", content: "Hi" }] },
		replies: [{ status: 401, body: '{"error":"unauthorized"}' }],
	},
];

const summarize = (message: any) => ({
	stopReason: message.stopReason,
	...(message.errorMessage ? { errorMessage: message.errorMessage } : {}),
	content: message.content.map((block: any) =>
		block.type === "toolCall"
			? { type: "toolCall", id: block.name === undefined ? undefined : block.id, name: block.name, arguments: JSON.stringify(block.arguments) }
			: block.type === "thinking"
				? { type: "thinking", text: block.thinking }
				: { type: "text", text: block.text },
	),
	usage: { input: message.usage.input, output: message.usage.output, cacheRead: message.usage.cacheRead, totalTokens: message.usage.totalTokens },
});

const out: any[] = [];
for (const scenario of scenarios) {
	replies = [...scenario.replies];
	received = [];
	const parsed = parseRequest(scenario.body);
	const bundled = models.get(parsed.modelId);
	if (!bundled) throw new Error(`no bundled ollama-cloud model ${parsed.modelId}`);
	const events: any[] = [];
	let final: any = null;
	const stream = streamSimple(bundled, parsed.context, { ...parsed.options, apiKey: API_KEY, fetch: loopbackFetch } as any);
	for await (const event of stream) {
		const entry: any = { type: event.type };
		if ("contentIndex" in event) entry.contentIndex = event.contentIndex;
		if ("delta" in event) entry.delta = event.delta;
		if (event.type === "toolcall_start" || event.type === "toolcall_end") {
			const block = (event as any).partial?.content?.[(event as any).contentIndex] ?? (event as any).toolCall;
			if (block?.type === "toolCall") entry.name = block.name;
		}
		events.push(entry);
		if (event.type === "done") final = summarize(event.message);
		if (event.type === "error") final = summarize(event.error);
	}
	out.push({ name: scenario.name, body: JSON.stringify(scenario.body), replies: scenario.replies, requests: received, events, final });
}
server.stop(true);
await Bun.write(
	join(import.meta.dir, "ollama_cloud_runtime.json"),
	JSON.stringify({ source: "NeoPi OWN ollama-cloud streamSimple over openai-chat-server parseRequest against a scripted /api/chat", apiKey: API_KEY, scenarios: out }, null, "\t") + "\n",
);
console.log(`${out.length} scenarios`);
