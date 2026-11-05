// Runs NeoPi's OWN openai-completions provider through streamSimple (the agent
// entry) against a scripted loopback SSE server only (every request is
// redirected to 127.0.0.1, the key is a fake), one scripted upstream response
// per scenario, and records the AssistantMessage NeoPi builds from it.
//
// The Go port of the response loop (neopi_completions_stream.go) replays the
// same raw SSE bodies and is diffed against this output.
//
// Writes testdata/neopi_completions_stream.json. Run from the NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks <this file>
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const worktree = process.env.NEOPROXY_WORKTREE ?? "/mnt/alias/m2/agents/worktrees/neoproxy-openai-keys";
const outFile = process.env.OUT_FILE ?? join(worktree, "internal/runtime/executor/testdata/neopi_completions_stream.json");

const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");

const base = getBundledModels("cerebras").find((model: any) => model.api === "openai-completions");
if (!base) throw new Error("no bundled cerebras completions model");

type Scenario = {
	name: string;
	// Raw SSE body NeoPi's transport reads.
	body: string;
	// Compat fields the scenario sets on the model (the Go side reads the same ones).
	compat?: Record<string, unknown>;
	headers?: Record<string, string>;
};

const frame = (value: unknown) => `data: ${JSON.stringify(value)}\n\n`;
const chunk = (delta: unknown, finish: string | null = null, extra: Record<string, unknown> = {}, choiceExtra: Record<string, unknown> = {}) => ({
	id: "chatcmpl-scripted",
	object: "chat.completion.chunk",
	created: 7,
	model: "scripted",
	choices: [{ index: 0, delta, finish_reason: finish, ...choiceExtra }],
	...extra,
});
const sse = (...parts: Array<unknown | string>) =>
	parts.map(part => (typeof part === "string" && part.startsWith("RAW:") ? part.slice(4) : frame(part))).join("");
const done = "RAW:data: [DONE]\n\n";
const usageChunk = (usage: unknown) => ({ id: "chatcmpl-scripted", object: "chat.completion.chunk", created: 7, model: "scripted", choices: [], usage });
const call = (index: number | undefined, id: string | undefined, name: string | undefined, args: unknown) => ({
	...(index === undefined ? {} : { index }),
	...(id === undefined ? {} : { id }),
	type: "function",
	function: { ...(name === undefined ? {} : { name }), ...(args === undefined ? {} : { arguments: args }) },
});

const scenarios: Scenario[] = [
	{
		name: "text-basic",
		body: sse(chunk({ role: "assistant", content: "Hel" }), chunk({ content: "lo." }), chunk({}, "stop", { usage: { prompt_tokens: 10, completion_tokens: 3, total_tokens: 13, prompt_tokens_details: { cached_tokens: 4 } } }), done),
	},
	{
		name: "text-trailing-usage-chunk",
		body: sse(chunk({ role: "assistant", content: "Hi" }), chunk({}, "stop"), usageChunk({ prompt_tokens: 20, completion_tokens: 5, total_tokens: 25, prompt_tokens_details: { cached_tokens: 8 } }), done),
	},
	{
		name: "text-array-content",
		body: sse(chunk({ content: [{ type: "text", text: "A" }, "b", { type: "image", text: "x" }, { text: "c" }] }), chunk({ content: { type: "text", text: "d" } }), chunk({}, "stop"), done),
	},
	{
		name: "reasoning-content",
		body: sse(chunk({ reasoning_content: "think " }), chunk({ reasoning_content: "more" }), chunk({ content: "answer" }), chunk({}, "stop"), done),
	},
	{
		name: "reasoning-alias-priority",
		body: sse(chunk({ reasoning_content: "a", reasoning: "a", reasoning_text: "a" }), chunk({ reasoning: "b" }), chunk({ reasoning_text: "c" }), chunk({ content: "x" }), chunk({}, "stop"), done),
	},
	{
		name: "reasoning-cumulative",
		compat: { reasoningDeltasMayBeCumulative: true },
		body: sse(chunk({ reasoning_content: "ab" }), chunk({ reasoning_content: "abc" }), chunk({ reasoning_content: "abcd" }), chunk({ content: "ok" }), chunk({ reasoning_content: "abcd" }), chunk({}, "stop"), done),
	},
	{
		name: "reasoning-delta-not-cumulative",
		body: sse(chunk({ reasoning_content: "ab" }), chunk({ reasoning_content: "abc" }), chunk({ content: "ok" }), chunk({}, "stop"), done),
	},
	{
		name: "litellm-thinking-blocks",
		body: sse(
			chunk({ reasoning_content: "dup", thinking_blocks: [{ type: "thinking", thinking: "deep ", signature: "" }] }),
			chunk({ thinking_blocks: [{ type: "thinking", thinking: "", signature: "sig1" }] }),
			chunk({ thinking_blocks: [{ type: "thinking", thinking: "next", signature: "" }, { type: "redacted_thinking", data: "zzz" }] }),
			chunk({ content: "done" }),
			chunk({}, "stop"),
			done,
		),
	},
	{
		name: "mistral-content-parts",
		compat: { mistralReasoningContentParts: true },
		body: sse(
			chunk({ content: [{ type: "thinking", thinking: [{ type: "text", text: "pondering " }] }] }),
			chunk({ reasoning_content: "ignored while typed thinking present", content: [{ type: "thinking", thinking: [{ type: "text", text: "more" }] }, { type: "text", text: "Hello" }] }),
			chunk({ content: "!" }),
			chunk({}, "stop"),
			done,
		),
	},
	{
		name: "deepseek-strip",
		compat: { stripDeepseekSpecialTokens: true },
		body: sse(chunk({ content: "Hello <｜tool_calls" }), chunk({ content: "_begin｜> world" }), chunk({ content: " <｜DSML｜x｜>" }), chunk({ content: " end <" }), chunk({}, "stop"), done),
	},
	{
		name: "deepseek-strip-only-token-chunk",
		compat: { stripDeepseekSpecialTokens: true },
		body: sse(chunk({ content: "a" }), chunk({ content: "<|tool_calls_end|>" }), chunk({ content: " b" }), chunk({}, "stop"), done),
	},
	{
		name: "tool-basic",
		body: sse(
			chunk({ role: "assistant", tool_calls: [call(0, "call_1", "lookup", "")] }),
			chunk({ tool_calls: [call(0, undefined, undefined, '{"te')] }),
			chunk({ tool_calls: [call(0, undefined, undefined, 'rm":"x"}')] }),
			chunk({}, "tool_calls", { usage: { prompt_tokens: 5, completion_tokens: 2, total_tokens: 7 } }),
			done,
		),
	},
	{
		name: "tool-finish-stop-promoted",
		body: sse(chunk({ tool_calls: [call(0, "call_1", "lookup", '{"term":"y"}')] }), chunk({}, "stop"), done),
	},
	{
		name: "tool-text-then-tool",
		body: sse(chunk({ content: "Calling." }), chunk({ tool_calls: [call(0, "call_1", "lookup", '{"term":"z"}')] }), chunk({}, "tool_calls"), done),
	},
	{
		name: "tool-parallel-index-interleaved",
		body: sse(
			chunk({ tool_calls: [call(0, "call_a", "lookup", '{"term":')] }),
			chunk({ tool_calls: [call(1, "call_b", "lookup", '{"term":')] }),
			chunk({ tool_calls: [call(0, undefined, undefined, '"a"}')] }),
			chunk({ tool_calls: [call(1, undefined, undefined, '"b"}')] }),
			chunk({}, "tool_calls"),
			done,
		),
	},
	{
		name: "tool-parallel-same-index-distinct-ids",
		body: sse(
			chunk({ tool_calls: [call(0, "call_a", "lookup", '{"term":"a"}')] }),
			chunk({ tool_calls: [call(0, "call_b", "lookup", '{"term":"b"}')] }),
			chunk({}, "tool_calls"),
			done,
		),
	},
	{
		name: "tool-fresh-id-per-fragment",
		body: sse(
			chunk({ tool_calls: [call(0, "call_a", "lookup", '{"te')] }),
			chunk({ tool_calls: [call(0, "call_b", undefined, 'rm":"q"}')] }),
			chunk({}, "tool_calls"),
			done,
		),
	},
	{
		name: "tool-unkeyed-batch",
		body: sse(
			chunk({ tool_calls: [call(undefined, undefined, "first", '{"a":'), call(undefined, undefined, "second", '{"b":')] }),
			chunk({ tool_calls: [call(undefined, undefined, undefined, "1}"), call(undefined, undefined, undefined, "2}")] }),
			chunk({}, "tool_calls"),
			done,
		),
	},
	{
		name: "tool-no-id",
		body: sse(chunk({ tool_calls: [call(0, undefined, "lookup", '{"term":"n"}')] }), chunk({}, "tool_calls"), done),
	},
	{
		name: "tool-name-after-id",
		body: sse(chunk({ tool_calls: [call(0, "call_1", undefined, "")] }), chunk({ tool_calls: [call(0, undefined, "lookup", '{"term":"late"}')] }), chunk({}, "tool_calls"), done),
	},
	{
		name: "tool-empty-arguments",
		body: sse(chunk({ tool_calls: [call(0, "call_1", "ping", "")] }), chunk({}, "tool_calls"), done),
	},
	{
		name: "tool-whitespace-arguments",
		body: sse(chunk({ tool_calls: [call(0, "call_1", "ping", "  ")] }), chunk({}, "tool_calls"), done),
	},
	{
		name: "tool-object-arguments",
		body: sse(
			chunk({ tool_calls: [call(0, "call_1", "lookup", { term: "a", list: [1] })] }),
			chunk({ tool_calls: [call(0, undefined, undefined, { term: "ab", list: [1, 2], extra: { k: "v" } })] }),
			chunk({ tool_calls: [call(0, undefined, undefined, { extra: { k: "vw", z: 1 } })] }),
			chunk({}, "tool_calls"),
			done,
		),
	},
	{
		name: "tool-reasoning-then-tool",
		body: sse(chunk({ reasoning_content: "plan" }), chunk({ tool_calls: [call(0, "call_1", "lookup", '{"term":"r"}')] }), chunk({ reasoning_content: " more" }), chunk({}, "tool_calls"), done),
	},
	{ name: "finish-length", body: sse(chunk({ content: "cut" }), chunk({}, "length"), done) },
	{ name: "finish-max-tokens", body: sse(chunk({ content: "cut" }), chunk({}, "MAX_TOKENS"), done) },
	{ name: "finish-uppercase-stop", body: sse(chunk({ content: "ok" }), chunk({}, "STOP"), done) },
	{ name: "finish-function-call", body: sse(chunk({ tool_calls: [call(0, "call_1", "lookup", "{}")] }), chunk({}, "function_call"), done) },
	{ name: "finish-content-filter", body: sse(chunk({ content: "x" }), chunk({}, "content_filter"), done) },
	{ name: "finish-network-error", body: sse(chunk({ content: "x" }), chunk({}, "network_error"), done) },
	{ name: "finish-error", body: sse(chunk({ content: "x" }), chunk({}, "error"), done) },
	{ name: "finish-insufficient-resource", body: sse(chunk({ content: "x" }), chunk({}, "insufficient_system_resource"), done) },
	{ name: "finish-unspecified", body: sse(chunk({ content: "x" }), chunk({}, "FINISH_REASON_UNSPECIFIED"), done) },
	{ name: "finish-unknown-value", body: sse(chunk({ content: "x" }), chunk({}, "weird_reason"), done) },
	{
		name: "native-finish-malformed",
		body: sse(chunk({ content: "x" }), chunk({}, "stop", {}, { native_finish_reason: "MALFORMED_FUNCTION_CALL" }), done),
	},
	{
		name: "native-finish-benign",
		body: sse(chunk({ content: "x" }), chunk({}, "stop", {}, { native_finish_reason: "STOP" }), done),
	},
	{
		name: "repetition-finish-reason",
		body: sse(chunk({ content: "loop loop" }), chunk({}, "repetition"), done),
	},
	{
		name: "repetition-stop-reason",
		body: sse(chunk({ reasoning_content: "loop" }), chunk({}, "stop", {}, { stop_reason: "repetition_detected" }), done),
	},
	{
		name: "empty-length-context-error",
		compat: { emptyLengthFinishIsContextError: true },
		body: sse(chunk({ role: "assistant" }), chunk({}, "length"), done),
	},
	{
		name: "empty-length-with-text",
		compat: { emptyLengthFinishIsContextError: true },
		body: sse(chunk({ content: "partial" }), chunk({}, "length"), done),
	},
	{
		name: "empty-length-thinking-only",
		compat: { emptyLengthFinishIsContextError: true },
		body: sse(chunk({ reasoning_content: "hmm" }), chunk({}, "length"), done),
	},
	{
		name: "inband-rate-limit-object",
		body: sse(chunk({ content: "x" }), { error: { type: "rate_limit_error", message: "Slow down" } }, done),
	},
	{
		name: "inband-numeric-code",
		body: sse({ error: { code: 429, message: "Too many requests right now" } }, done),
	},
	{
		name: "inband-flat-code-status",
		body: sse({ code: 503, message: "upstream busy" }, done),
	},
	{
		name: "inband-overloaded-code-string",
		body: sse({ error: { code: "overloaded_error", message: "Overloaded" } }, done),
	},
	{
		name: "inband-non-retryable-quota",
		body: sse({ error: { code: "insufficient_quota", message: "You exceeded your current quota" } }, done),
	},
	{
		name: "inband-error-string",
		body: sse({ error: "backend exploded" }, done),
	},
	{
		name: "inband-error-with-status-500",
		body: sse({ error: { code: "500", message: "Internal failure" } }, done),
	},
	{
		name: "inband-error-type-status",
		body: sse({ error: { type: "SERVICE_UNAVAILABLE", message: "try later" } }, done),
	},
	{
		name: "inband-error-null-ignored",
		body: sse(chunk({ content: "fine" }, null, { error: null }), chunk({}, "stop"), done),
	},
	{
		name: "inband-text-frame-429",
		body: "data: 429 Too Many Requests\n\n",
	},
	{
		name: "inband-text-frame-html-throttle",
		body: "data: <html><body>503 Service Unavailable</body></html>\n\n",
	},
	{
		name: "invalid-json-frame",
		body: "data: {not json\n\n" + sse(chunk({ content: "x" }), chunk({}, "stop"), done),
	},
	{
		name: "usage-deepseek-hit-miss",
		body: sse(chunk({ content: "x" }), chunk({}, "stop", { usage: { prompt_tokens: 100, completion_tokens: 10, total_tokens: 110, prompt_cache_hit_tokens: 60, prompt_cache_miss_tokens: 40 } }), done),
	},
	{
		name: "usage-top-level-cached",
		body: sse(chunk({ content: "x" }), chunk({}, "stop", { usage: { prompt_tokens: 50, completion_tokens: 5, total_tokens: 55, cached_tokens: 20 } }), done),
	},
	{
		name: "usage-cache-write-and-reasoning",
		body: sse(
			chunk({ content: "x" }),
			chunk({}, "stop", { usage: { prompt_tokens: 80, completion_tokens: 12, total_tokens: 92, prompt_tokens_details: { cached_tokens: 30, cache_write_tokens: 10 }, completion_tokens_details: { reasoning_tokens: 7 } } }),
			done,
		),
	},
	{
		name: "usage-vertex-cached-content",
		body: sse(chunk({ content: "x" }), chunk({}, "stop", { usage: { prompt_tokens: 40, completion_tokens: 4, total_tokens: 44, cachedContentTokenCount: 15 } }), done),
	},
	{
		name: "usage-in-choice",
		body: sse(chunk({ content: "x" }), chunk({}, "stop", {}, { usage: { prompt_tokens: 9, completion_tokens: 1, total_tokens: 10 } }), done),
	},
	{
		name: "usage-fireworks-header",
		headers: { "fireworks-cached-prompt-tokens": "12" },
		body: sse(chunk({ content: "x" }), chunk({}, "stop", { usage: { prompt_tokens: 30, completion_tokens: 3, total_tokens: 33 } }), done),
	},
	{
		name: "usage-fireworks-header-body-wins",
		headers: { "fireworks-cached-prompt-tokens": "12" },
		body: sse(chunk({ content: "x" }), chunk({}, "stop", { usage: { prompt_tokens: 30, completion_tokens: 3, total_tokens: 33, prompt_tokens_details: { cached_tokens: 5 } } }), done),
	},
	{
		name: "usage-two-payloads-last-wins",
		body: sse(chunk({ content: "x" }, null, { usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } }), chunk({}, "stop", { usage: { prompt_tokens: 7, completion_tokens: 2, total_tokens: 9, prompt_tokens_details: { cached_tokens: 3 } } }), done),
	},
	{
		name: "usage-late-details-chunk",
		body: sse(
			chunk({ content: "x" }),
			chunk({}, "stop", { usage: { prompt_tokens: 7, completion_tokens: 2, total_tokens: 9, prompt_tokens_details: { cached_tokens: 0 } } }),
			usageChunk({ prompt_tokens: 7, completion_tokens: 2, total_tokens: 9, prompt_tokens_details: { cached_tokens: 3 } }),
			done,
		),
	},
	{ name: "eof-no-finish-with-content", body: sse(chunk({ content: "partial" })) },
	{ name: "eof-no-finish-no-content", body: sse(chunk({ role: "assistant" })) },
	{ name: "eof-empty-body", body: "" },
	{ name: "done-without-finish", body: sse(chunk({ content: "x" }), done) },
	{ name: "finish-then-eof-without-done", body: sse(chunk({ content: "x" }), chunk({}, "stop")) },
	{ name: "tool-eof-without-finish", body: sse(chunk({ tool_calls: [call(0, "call_1", "lookup", '{"te')] })) },
	{
		name: "sse-multiline-comments-crlf",
		body:
			": keepalive\r\nevent: message\r\ndata: " +
			JSON.stringify(chunk({ content: "a" })) +
			"\r\n\r\n:ping\r\n\r\n" +
			JSON.stringify(chunk({}, "stop"), null, 1)
				.split("\n")
				.map(line => `data: ${line}`)
				.join("\r\n") +
			"\r\n\r\ndata: [DONE]\r\n\r\n",
	},
	{
		name: "trailing-truncated-container",
		body: sse(chunk({ content: "ok" }), chunk({}, "stop")) + 'data: {"id":"chatcmpl-scripted","choices":[{"inde',
	},
	{
		name: "refusal-and-role-only",
		body: sse(chunk({ role: "assistant", content: "" }), chunk({ refusal: "no" }), chunk({ content: "yes" }), chunk({}, "stop"), done),
	},
	// StreamMarkupHealing: leaked template markup in the content channel.
	{
		name: "heal-thinking-tags",
		compat: { streamMarkupHealingPattern: "thinking" },
		body: sse(chunk({ content: "before<thi" }), chunk({ content: "nk>reasoning</think>after" }), chunk({}, "stop"), done),
	},
	{
		name: "heal-thinking-suppressed-by-native-reasoning",
		compat: { streamMarkupHealingPattern: "thinking" },
		body: sse(chunk({ reasoning_content: "native" }), chunk({ content: "<think>leaked</think>visible" }), chunk({}, "stop"), done),
	},
	{
		name: "heal-kimi-tool-call-promotes-stop",
		compat: { streamMarkupHealingPattern: "kimi" },
		body: sse(
			chunk({ content: "Reading.<|tool_calls_section_begin|><|tool_call_begin|>functions.read:0<|tool_call_" }),
			chunk({ content: 'argument_begin|>{"path":"a.txt"}<|tool_call_end|><|tool_calls_section_end|>' }),
			chunk({}, "stop"),
			done,
		),
	},
	{
		name: "heal-dsml-envelope",
		compat: { streamMarkupHealingPattern: "dsml" },
		body: sse(
			chunk({ content: "Let me run it.\n<｜DSML｜tool_calls>\n<｜DSML｜invoke name=\"bash\">\n<｜DSML｜parameter name=\"cmd\" string=\"true\">ls" }),
			chunk({ content: " -la</｜DSML｜parameter>\n</｜DSML｜invoke>\n</｜DSML｜tool_calls>" }),
			chunk({}, "stop"),
			done,
		),
	},
	{
		name: "heal-dsml-template-tokens",
		compat: { streamMarkupHealingPattern: "dsml" },
		body: sse(chunk({ content: "<｜tool▁calls▁begin｜><｜tool▁call▁begin｜>read<｜tool▁sep｜>{\"p\":1}<｜tool▁call▁end｜><｜tool▁calls▁end｜>" }), chunk({}, "tool_calls"), done),
	},
	{
		name: "heal-with-structured-tool-calls",
		compat: { streamMarkupHealingPattern: "kimi" },
		body: sse(
			chunk({ content: "x<|tool_calls_section_begin|><|tool_call_begin|>functions.a:0<|tool_call_argument_begin|>{}<|tool_call_end|><|tool_calls_section_end|>y", tool_calls: [call(0, "call_struct", "real", '{"k":1}')] }),
			chunk({}, "tool_calls"),
			done,
		),
	},
	{
		name: "heal-qwen-xml",
		compat: { streamMarkupHealingPattern: "qwen" },
		body: sse(chunk({ content: "pre<tool_calls><read path=\"a\"/></tool_calls>" }), chunk({ content: "post" }), chunk({}, "stop"), done),
	},
	{
		name: "heal-unterminated-thinking-at-end",
		compat: { streamMarkupHealingPattern: "dsml" },
		body: sse(chunk({ content: "a<think>still thinking" }), chunk({}, "length"), done),
	},
];

let current: Scenario | undefined;
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch() {
		return new Response(current!.body, { headers: { "content-type": "text/event-stream", ...(current!.headers ?? {}) } });
	},
});
const local = `http://127.0.0.1:${server.port}`;
const redirectFetch = (async (input: any, init: any) => {
	const url = new URL(input instanceof Request ? input.url : String(input));
	const headers = new Headers(init?.headers);
	headers.set("host", url.host);
	return fetch(local + url.pathname + url.search, { ...init, headers, tls: undefined });
}) as typeof fetch;

const user = { role: "user", content: "Hi.", timestamp: 0 };
const results: any[] = [];
for (const scenario of scenarios) {
	current = scenario;
	const model = { ...base, compat: { ...base.compat, streamMarkupHealingPattern: undefined, stripDeepseekSpecialTokens: false, reasoningDeltasMayBeCumulative: false, emptyLengthFinishIsContextError: false, mistralReasoningContentParts: false, ...(scenario.compat ?? {}) } };
	const message = await streamSimple(model, { messages: [user] }, { apiKey: "fixture-key", fetch: redirectFetch, maxHttpAttempts: 1, acceptEmptyResponse: true }).result();
	results.push({
		name: scenario.name,
		body: scenario.body,
		compat: scenario.compat ?? {},
		headers: scenario.headers ?? {},
		neopi: {
			stopReason: message.stopReason,
			errorMessage: message.errorMessage ?? null,
			errorStatus: message.errorStatus ?? null,
			content: message.content.map((block: any) => {
				if (block.type === "text") return { type: "text", text: block.text };
				if (block.type === "thinking") return { type: "thinking", thinking: block.thinking };
				if (block.type === "toolCall") return { type: "toolCall", id: block.id, name: block.name, arguments: block.arguments };
				return { type: block.type };
			}),
			usage: message.usage
				? { input: message.usage.input, output: message.usage.output, cacheRead: message.usage.cacheRead, cacheWrite: message.usage.cacheWrite, totalTokens: message.usage.totalTokens, reasoningTokens: message.usage.reasoningTokens ?? 0 }
				: null,
		},
	});
}
server.stop(true);
await Bun.write(outFile, JSON.stringify({ source: "NeoPi OWN streamSimple over a bundled cerebras completions row with scripted SSE responses", model: base.id, scenarios: results }, null, 1));
console.error(`${results.length} scenarios -> ${outFile}`);
