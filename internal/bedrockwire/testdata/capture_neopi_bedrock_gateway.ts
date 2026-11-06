// Runs NeoPi's OWN Amazon Bedrock Converse provider behind its OWN auth
// gateway: an OpenAI chat body goes to startAuthGateway's
// /v1/chat/completions, which parses it, builds the stream options
// (buildStreamOptions), maps them for bedrock-converse-stream
// (mapOptionsForApi) and runs streamBedrock over the bundled amazon-bedrock
// row with the account's key. The gateway's upstream fetch seam records every
// Converse request (URL, headers, body) and answers with scripted eventstream
// frames built by NeoPi's own test encoder; nothing reaches a host. The
// gateway's client-facing reply is recorded as sent.
//
// Writes testdata/neopi_bedrock_gateway.json.gz.
//
//   cd <neopi-nightly> && env -u AWS_REGION -u AWS_DEFAULT_REGION -u AWS_PROFILE \
//     -u AWS_BEARER_TOKEN_BEDROCK -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY \
//     HOME=$(mktemp -d) NEOPI_CHECKOUT=$PWD bun <this file>
import { writeFileSync } from "node:fs";
import { join } from "node:path";

const checkout = process.env.NEOPI_CHECKOUT ?? "/home/shayna/source/github/PsychedelicShayna/neopi-nightly";
const { startAuthGateway } = await import(join(checkout, "packages/ai/src/auth-gateway/index.ts"));
const { AuthStorage } = await import(join(checkout, "packages/ai/src/auth-storage.ts"));
const { getBundledModel } = await import(join(checkout, "packages/catalog/src/models.ts"));
const { encodeBedrockFrame } = await import(join(checkout, "packages/ai/test/helpers/bedrock-stream.ts"));

const API_KEY = "bedrock-api-key-fixture";
const PROVIDER = "amazon-bedrock";

type Frame =
	| { event: string; payload: unknown }
	| { exception: string; message: string }
	| { error: string; message: string };
type Reply = { status: number; frames?: Frame[]; body?: string; contentType?: string };
type Scenario = { name: string; body: any; replies: Reply[]; env?: Record<string, string> };

const encodeFrame = (frame: Frame): Uint8Array => {
	const text = (value: unknown) => new TextEncoder().encode(typeof value === "string" ? value : JSON.stringify(value));
	if ("event" in frame) return encodeBedrockFrame({ ":message-type": "event", ":event-type": frame.event, ":content-type": "application/json" }, text(frame.payload));
	if ("exception" in frame) return encodeBedrockFrame({ ":message-type": "exception", ":exception-type": frame.exception, ":content-type": "application/json" }, text({ message: frame.message }));
	return encodeBedrockFrame({ ":message-type": "error", ":error-code": frame.error, ":error-message": frame.message }, new Uint8Array(0));
};
const concat = (parts: Uint8Array[]) => {
	const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
	let off = 0;
	for (const p of parts) {
		out.set(p, off);
		off += p.length;
	}
	return out;
};

let replies: Reply[] = [];
let upstream: any[] = [];
const fetchSeam = async (input: any, init?: any) => {
	const request = input instanceof Request ? input : new Request(String(input), init);
	const headers: Record<string, string> = {};
	request.headers.forEach((value, key) => {
		headers[key] = value;
	});
	upstream.push({ method: request.method, url: request.url, headers, body: await request.text() });
	const reply = replies.shift();
	if (!reply) return new Response("no scripted reply", { status: 599 });
	if (reply.frames) {
		return new Response(concat(reply.frames.map(encodeFrame)), { status: reply.status, headers: { "content-type": "application/vnd.amazon.eventstream" } });
	}
	return new Response(reply.body ?? "", { status: reply.status, headers: { "content-type": reply.contentType ?? "application/json" } });
};

// Event payloads Bedrock sends, keyed by event type in the frame header.
const start = { event: "messageStart", payload: { role: "assistant" } };
const text = (index: number, value: string) => ({ event: "contentBlockDelta", payload: { contentBlockIndex: index, delta: { text: value } } });
const reasoning = (index: number, value: string) => ({ event: "contentBlockDelta", payload: { contentBlockIndex: index, delta: { reasoningContent: { text: value } } } });
const signature = (index: number, value: string) => ({ event: "contentBlockDelta", payload: { contentBlockIndex: index, delta: { reasoningContent: { signature: value } } } });
const toolStart = (index: number, id: string, name: string) => ({ event: "contentBlockStart", payload: { contentBlockIndex: index, start: { toolUse: { toolUseId: id, name } } } });
const toolInput = (index: number, input: string) => ({ event: "contentBlockDelta", payload: { contentBlockIndex: index, delta: { toolUse: { input } } } });
const stop = (index: number) => ({ event: "contentBlockStop", payload: { contentBlockIndex: index } });
const messageStop = (stopReason: string) => ({ event: "messageStop", payload: { stopReason } });
const metadata = (usage: Record<string, number>) => ({ event: "metadata", payload: { usage, metrics: { latencyMs: 120 } } });
const usage = { inputTokens: 120, outputTokens: 30, totalTokens: 150 };
const ok = (...frames: Frame[]): Reply => ({ status: 200, frames });
const hello = ok(start, text(0, "Hello"), text(0, " there"), stop(0), messageStop("end_turn"), metadata(usage));

const user = (content: any) => ({ role: "user", content });
const chat = (model: string, messages: any[], extra: Record<string, unknown> = {}) => ({ model, messages, stream: true, stream_options: { include_usage: true }, ...extra });
const lookup = { type: "function", function: { name: "lookup", description: "Look a thing up.", parameters: { type: "object", properties: { query: { type: "string" } }, required: ["query"] } } };
const bare = { type: "function", function: { name: "ping", parameters: { type: "object", properties: {} } } };
const image = "data:image/png;base64,iVBORw0KGgo=";
const toolHistory = [
	user("Find the capital of France."),
	{ role: "assistant", content: null, tool_calls: [{ id: "call_1", type: "function", function: { name: "lookup", arguments: '{"query":"capital of France"}' } }, { id: "call_2", type: "function", function: { name: "lookup", arguments: '{"query":"population"}' } }] },
	{ role: "tool", tool_call_id: "call_1", content: "Paris" },
	{ role: "tool", tool_call_id: "call_2", content: "2.1 million" },
];

const OPUS = "us.anthropic.claude-opus-5-5";
const OPUS46 = "us.anthropic.claude-opus-4-6-v1";
const HAIKU = "global.anthropic.claude-haiku-5-5";
const SONNET45 = "eu.anthropic.claude-sonnet-4-5-20250929-v1:0";
const OPUS45 = "us.anthropic.claude-opus-4-5-20251101-v1:0";
const GPT = "global.openai.gpt-5.6-sol";
const DEEPSEEK = "deepseek.v3.2";
const NOVA = "eu.amazon.nova-2-lite-v1:0";
const LLAMA = "meta.llama3-1-70b-instruct-v1:0";

const scenarios: Scenario[] = [
	{ name: "adaptive claude plain", body: chat(OPUS, [user("Say hello.")]), replies: [hello] },
	{ name: "adaptive claude with system and sampling", body: chat(OPUS, [{ role: "system", content: "Be terse." }, { role: "developer", content: "Answer in English." }, user("Say hello.")], { temperature: 0.4, top_p: 0.9, stop: ["END"], max_tokens: 2000 }), replies: [hello] },
	{ name: "adaptive claude high effort capped", body: chat(OPUS, [user("Think, then answer.")], { reasoning_effort: "high", max_tokens: 4000 }), replies: [ok(start, reasoning(0, "Let me think."), reasoning(0, " Done."), signature(0, "sig-abc"), stop(0), text(1, "Answer."), stop(1), messageStop("end_turn"), metadata({ inputTokens: 200, outputTokens: 50, cacheReadInputTokens: 64, cacheWriteInputTokens: 32, totalTokens: 346 }))] },
	{ name: "adaptive claude effort uncapped", body: chat(OPUS, [user("Hi")], { reasoning_effort: "minimal" }), replies: [hello] },
	{ name: "adaptive claude without display", body: chat(OPUS46, [user("Hi")], { reasoning_effort: "medium", max_tokens: 1000 }), replies: [hello] },
	{ name: "haiku disabled thinking", body: chat(HAIKU, [user("Hi")]), replies: [hello] },
	{ name: "haiku with effort", body: chat(HAIKU, [user("Hi")], { reasoning_effort: "low" }), replies: [hello] },
	{ name: "budget claude low capped", body: chat(SONNET45, [user("Hi")], { reasoning_effort: "low", max_tokens: 1000 }), replies: [hello] },
	{ name: "budget claude high uncapped", body: chat(SONNET45, [user("Hi")], { reasoning_effort: "high" }), replies: [hello] },
	{ name: "budget effort claude", body: chat(OPUS45, [user("Hi")], { reasoning_effort: "xhigh", max_tokens: 64000 }), replies: [hello] },
	{ name: "effort gpt", body: chat(GPT, [user("Hi")], { reasoning_effort: "medium", max_tokens: 3000 }), replies: [hello] },
	{ name: "budget deepseek", body: chat(DEEPSEEK, [user("Hi")], { reasoning_effort: "high" }), replies: [ok(start, reasoning(0, "unsigned thought"), stop(0), text(1, "Hi!"), stop(1), messageStop("end_turn"), metadata(usage))] },
	{ name: "non-reasoning llama", body: chat(LLAMA, [user("Hi")], { reasoning_effort: "high", max_tokens: 500 }), replies: [hello] },
	{ name: "tools auto with tool call reply", body: chat(OPUS, [user("Look up Paris.")], { tools: [lookup, bare] }), replies: [ok(start, text(0, "Checking."), stop(0), toolStart(1, "tooluse_1", "lookup"), toolInput(1, '{"que'), toolInput(1, 'ry":"Paris"}'), stop(1), messageStop("tool_use"), metadata(usage))] },
	{ name: "tools required", body: chat(LLAMA, [user("Look up Paris.")], { tools: [lookup], tool_choice: "required" }), replies: [hello] },
	{ name: "named tool with reasoning", body: chat(OPUS, [user("Look up Paris.")], { tools: [lookup], tool_choice: { type: "function", function: { name: "lookup" } }, reasoning_effort: "high" }), replies: [hello] },
	{ name: "named tool budget claude with reasoning", body: chat(SONNET45, [user("Look up Paris.")], { tools: [lookup], tool_choice: { type: "function", function: { name: "lookup" } }, reasoning_effort: "high" }), replies: [hello] },
	{ name: "tool history with tools", body: chat(OPUS, toolHistory, { tools: [lookup] }), replies: [hello] },
	{ name: "tool history without tools", body: chat(OPUS, toolHistory), replies: [hello] },
	{ name: "tool history tool choice none", body: chat(OPUS, toolHistory, { tool_choice: "none" }), replies: [hello] },
	{ name: "tool history tool choice none with tools", body: chat(OPUS, toolHistory, { tools: [lookup], tool_choice: "none" }), replies: [hello] },
	{ name: "images and multipart", body: chat(OPUS, [user([{ type: "text", text: "What is this?" }, { type: "image_url", image_url: { url: image } }, { type: "text", text: "   " }])]), replies: [hello] },
	{ name: "assistant reasoning replay", body: chat(NOVA, [user("Hi"), { role: "assistant", content: "Hello!", reasoning_content: "They greeted me." }, user("How are you?")], { reasoning_effort: "low" }), replies: [hello] },
	{ name: "empty messages skipped", body: chat(OPUS, [user(""), user("   "), { role: "assistant", content: "" }, user("Real question")]), replies: [hello] },
	{ name: "prompt cache key", body: chat(OPUS, [{ role: "system", content: "Cached system." }, user("Hi")], { prompt_cache_key: "session-123" }), replies: [hello] },
	{ name: "max tokens stop", body: chat(OPUS, [user("Long")], { max_tokens: 5 }), replies: [ok(start, text(0, "Trunc"), stop(0), messageStop("max_tokens"), metadata(usage))] },
	{ name: "context window stop", body: chat(OPUS, [user("Long")]), replies: [ok(start, text(0, "Trunc"), stop(0), messageStop("model_context_window_exceeded"), metadata(usage))] },
	{ name: "guardrail stop", body: chat(OPUS, [user("Bad")]), replies: [ok(start, text(0, "Partial"), stop(0), messageStop("guardrail_intervened"), metadata(usage))] },
	{ name: "content filtered stop", body: chat(OPUS, [user("Bad")]), replies: [ok(start, messageStop("content_filtered"), metadata(usage))] },
	{ name: "unknown stop reason", body: chat(OPUS, [user("Hi")]), replies: [ok(start, text(0, "x"), stop(0), messageStop("something_new"), metadata(usage))] },
	{ name: "throttling exception", body: chat(OPUS, [user("Hi")]), replies: [ok(start, { exception: "throttlingException", message: "Too many requests" })] },
	{ name: "internal server exception after text", body: chat(OPUS, [user("Hi")]), replies: [ok(start, text(0, "Partial"), { exception: "InternalServerException", message: "boom" })] },
	{ name: "unknown exception", body: chat(OPUS, [user("Hi")]), replies: [ok(start, { exception: "weirdException", message: "odd" })] },
	{ name: "error frame", body: chat(OPUS, [user("Hi")]), replies: [ok(start, { error: "ModelStreamErrorException", message: "stream broke" })] },
	{ name: "http 403", body: chat(OPUS, [user("Hi")]), replies: [{ status: 403, body: '{"message":"User is not authorized"}' }] },
	{ name: "http 400 validation", body: chat(OPUS, [user("Hi")]), replies: [{ status: 400, body: '{"message":"The provided model identifier is invalid."}' }] },
	{ name: "http 429 then ok", body: chat(OPUS, [user("Hi")]), replies: [{ status: 429, body: '{"message":"Too many tokens"}' }, hello] },
	{ name: "http 500 then ok", body: chat(OPUS, [user("Hi")]), replies: [{ status: 500, body: '{"message":"internal"}' }, hello] },
	{ name: "truncated stream", body: chat(OPUS, [user("Hi")]), replies: [ok(start, text(0, "Hel"))] },
	{ name: "region eu ambient with eu profile", env: { AWS_REGION: "eu-central-1" }, body: chat(SONNET45, [user("Hi")]), replies: [hello] },
	{ name: "region eu ambient with us profile", env: { AWS_REGION: "eu-central-1" }, body: chat(OPUS, [user("Hi")]), replies: [hello] },
	{ name: "region eu ambient with global profile", env: { AWS_REGION: "eu-central-1" }, body: chat(HAIKU, [user("Hi")]), replies: [hello] },
	{ name: "region default region env with bare id", env: { AWS_DEFAULT_REGION: "ap-northeast-1" }, body: chat(LLAMA, [user("Hi")]), replies: [hello] },
	{ name: "region default for geo", body: chat("apac.anthropic.claude-sonnet-4-20250514-v1:0", [user("Hi")]), replies: [hello] },
	// A ladder with max but no xhigh: the gateway fits xhigh to the nearest tier below.
	{ name: "xhigh on a max ladder capped", body: chat(OPUS46, [user("Hi")], { reasoning_effort: "xhigh", max_tokens: 1000 }), replies: [hello] },
	{ name: "xhigh on opus 4.7 capped", body: chat("us.anthropic.claude-opus-4-7", [user("Hi")], { reasoning_effort: "xhigh", max_tokens: 1000 }), replies: [hello] },
	{ name: "max on an xhigh ladder", body: chat(OPUS45, [user("Hi")], { reasoning_effort: "max" }), replies: [hello] },
	// Usage framing follows stream_options.include_usage.
	{ name: "usage not requested", body: chat(OPUS, [user("Hi")], { stream_options: undefined }), replies: [hello] },
	{ name: "usage declined", body: chat(OPUS, [user("Hi")], { stream_options: { include_usage: false } }), replies: [hello] },
];

const results: any[] = [];
for (const scenario of scenarios) {
	const saved: Record<string, string | undefined> = {};
	for (const [k, v] of Object.entries(scenario.env ?? {})) {
		saved[k] = process.env[k];
		process.env[k] = v;
	}
	replies = [...scenario.replies];
	upstream = [];
	const storage = await AuthStorage.create(":memory:", { usageProviderResolver: () => undefined });
	storage.keys.setRuntime(PROVIDER, API_KEY);
	const gateway = startAuthGateway({
		bind: "127.0.0.1:0",
		bearerTokens: ["gateway-token"],
		storage,
		resolveModel: (id: string) => {
			const bare = id.startsWith(`${PROVIDER}/`) ? id.slice(PROVIDER.length + 1) : id;
			return getBundledModel(PROVIDER, bare);
		},
		fetch: fetchSeam,
		version: "capture",
	});
	let status = 0;
	let reply = "";
	try {
		const response = await fetch(`${gateway.url}/v1/chat/completions`, {
			method: "POST",
			headers: { Authorization: "Bearer gateway-token", "Content-Type": "application/json" },
			body: JSON.stringify(scenario.body),
		});
		status = response.status;
		reply = await response.text();
	} finally {
		await gateway.close();
		storage.close();
		for (const [k, v] of Object.entries(saved)) {
			if (v === undefined) delete process.env[k];
			else process.env[k] = v;
		}
	}
	results.push({ name: scenario.name, ...(scenario.env ? { env: scenario.env } : {}), body: JSON.stringify(scenario.body), replies: scenario.replies, upstream, status, reply });
}

const out = join(import.meta.dir, "neopi_bedrock_gateway.json.gz");
writeFileSync(out, Bun.gzipSync(JSON.stringify({
	source: "NeoPi OWN auth-gateway /v1/chat/completions → streamBedrock over bundled amazon-bedrock rows, scripted eventstream upstream",
	apiKey: API_KEY,
	results,
})));
console.log(`wrote ${results.length} scenarios to ${out}`);
