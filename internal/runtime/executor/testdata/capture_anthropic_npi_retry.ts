// Execute NeoPi's public stream entry point, not the dormant client retry loop.
// All Anthropic requests use a fake credential and the same local capture server.
import { readFile } from "node:fs/promises";
const modules = process.env.NEOPI_SOURCE_MODULES;
if (!modules) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamAnthropic } = await import(modules + "/pi-ai/src/providers/anthropic.ts");
const { buildModel } = await import(modules + "/pi-catalog/src/build.ts");
const fixture = JSON.parse(await readFile(new URL("anthropic_npi_requests.json", import.meta.url), "utf8"));
const baseline = fixture.fixtures.find((item: any) => item.name === "official/plain-high");
const now = Date.parse("2026-11-04T00:00:00Z");
const originalNow = Date.now;
const originalRandom = Math.random;
Date.now = () => now;
Math.random = () => 0.5;
const frame = (data: any) => `event: ${data.type}\ndata: ${JSON.stringify(data)}\n\n`;
const start = frame({ type: "message_start", message: { id: "msg_retry", type: "message", role: "assistant", model: "claude-opus-5-5", content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 1, output_tokens: 0 } } });
const block = (type: string, fields = {}) => frame({ type: "content_block_start", index: 0, content_block: { type, ...fields } });
const error = frame({ type: "error", error: { type: "overloaded_error", message: "Overloaded" } });
const end = (reason = "end_turn", output = 1) => frame({ type: "message_delta", delta: { stop_reason: reason }, usage: { output_tokens: output } }) + frame({ type: "message_stop" });
const text = frame({ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "Partial." } });
const cases: any[] = [];
const http = (name: string, status: number, headers = {}, body = "Transient failure", failures = 1) => cases.push({ name, status, headers, body, failures });
for (const status of [400, 401, 403, 408, 409, 429, 500, 529]) http(`http-${status}`, status);
for (const status of [400, 409, 429, 529]) for (const value of ["true", "false"]) http(`http-${status}-retry-${value}`, status, { "x-should-retry": value });
for (const status of [429, 529]) {
  http(`http-${status}-seconds`, status, { "retry-after": "2" });
  http(`http-${status}-short-hint`, status, { "retry-after": "0.1" });
  http(`http-${status}-over-cap`, status, { "retry-after": "61" });
}
http("both-hints-use-maximum", 529, { "retry-after-ms": "500", "retry-after": "2" });
http("fractional-ms-ceiling", 529, { "retry-after-ms": "2000.1" });
http("reset-seconds", 529, { "x-ratelimit-reset": "2" });
http("reset-ms", 529, { "x-ratelimit-reset-ms": "2000" });
http("reset-epoch", 529, { "x-ratelimit-reset": String(now / 1000 + 2) });
http("date-hint", 529, { "retry-after": new Date(now + 2000).toUTCString() });
http("past-date-hint", 529, { "retry-after": new Date(now - 2000).toUTCString() });
http("invalid-numeric-hint", 529, { "retry-after-ms": "2000ms" });
http("retry-budget", 529, {}, "Overloaded", 100);
http("account-rate-limit", 429, {}, "This request would exceed your account's rate limit. Please try again later.");
http("monthly-spend-limit", 429, {}, "This request would exceed your account's monthly spend limit. Please try again later.");
http("usage-limit-code", 429, {}, JSON.stringify({ type: "error", error: { type: "usage_limit_reached", message: "Limit reached" } }));
http("quota-code", 429, {}, JSON.stringify({ type: "error", error: { type: "insufficient_quota", message: "Quota exhausted" } }));
const sse = (name: string, body: string, headers = {}, failures = 1) => cases.push({ name, status: 200, headers, body, failures });
sse("sse-error-before-start", error);
sse("sse-error-after-start", start + error);
sse("sse-error-ignores-http-hint", start + error, { "retry-after": "61" });
sse("sse-error-after-text-start", start + block("text", { text: "" }) + error);
sse("sse-error-after-tool-start", start + block("tool_use", { id: "tool_1", name: "_lookup", input: {} }) + error);
sse("sse-error-after-thinking-start", start + block("thinking", { thinking: "" }) + error);
sse("sse-error-after-server-tool-start", start + block("server_tool_use", { id: "tool_1", name: "web_search", input: {} }) + error);
sse("sse-error-after-text", start + block("text", { text: "" }) + text + error);
sse("sse-empty-transport", "");
sse("sse-truncated-after-start", start);
sse("sse-truncated-after-text-start", start + block("text", { text: "" }));
sse("sse-block-before-start", block("text", { text: "" }));
sse("sse-malformed-frame-then-valid", 'event: message_start\ndata: {\n\n' + fixture.response);
sse("empty-completion", start + end());
sse("empty-completion-budget", start + end(), {}, 100);
sse("empty-completion-with-output", start + end("end_turn", 2));
sse("empty-max-tokens", start + end("max_tokens"));
sse("empty-pause-turn", start + end("pause_turn"));
sse("zero-argument-tool", start + block("tool_use", { id: "tool_1", name: "_lookup", input: {} }) + frame({ type: "content_block_stop", index: 0 }) + end("tool_use"));
let scenario: any;
let requests: any[] = [];
const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
  const path = new URL(request.url).pathname;
  if (path === "/__retry/setup") { scenario = await request.json(); requests = []; return Response.json({ ok: true }); }
  if (path === "/__retry/capture") return Response.json(requests);
  const rawBody = await request.text();
  requests.push({ method: request.method, path: new URL(request.url).pathname + new URL(request.url).search, headers: Object.fromEntries(request.headers), rawBody });
  const fail = requests.length <= scenario.failures;
  return new Response(fail ? scenario.body : fixture.response, { status: fail ? scenario.status : 200, headers: { "content-type": fail && scenario.status !== 200 ? "application/json" : "text/event-stream", ...(fail ? scenario.headers : {}) } });
} });
const local = process.env.NEOPI_CAPTURE_URL ?? `http://127.0.0.1:${server.port}`;
const model = buildModel({ id: "claude-opus-5-5", name: "Claude Opus 5.5", api: "anthropic-messages", provider: "anthropic", baseUrl: "https://api.anthropic.com", reasoning: true, input: ["text", "image"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 1000000, maxTokens: 128000 });
try {
  const results = [];
  for (const test of cases) {
    await fetch(local + "/__retry/setup", { method: "POST", body: JSON.stringify(test) });
    const waits: number[] = [];
    const result = await streamAnthropic(model, { messages: [{ role: "user", content: "Say hello.", timestamp: 0 }] }, {
      apiKey: fixture.token, isOAuth: true, thinkingEnabled: true, effort: "high", maxTokens: 64,
      sessionId: fixture.sessionId, metadata: { account_uuid: fixture.accountId },
      providerRetryWait: async (delay: number) => { waits.push(delay); },
      fetch: async (input: any, init: any) => {
        const url = new URL(input instanceof Request ? input.url : String(input));
        if (url.origin !== "https://api.anthropic.com") throw new Error("Unexpected upstream");
        const headers = new Headers(init?.headers);
        headers.set("host", url.host);
        return fetch(local + url.pathname + url.search, { ...init, headers, tls: undefined });
      },
    }).result();
    const captured = await (await fetch(local + "/__retry/capture")).json() as any[];
    for (const request of captured) {
      if (request.method !== baseline.method || request.path !== baseline.path || request.rawBody !== baseline.rawBody || JSON.stringify(Object.entries(request.headers).sort()) !== JSON.stringify(Object.entries(baseline.headers).sort())) throw new Error(`Wire request changed in ${test.name}`);
    }
    results.push({ ...test, attempts: captured.length, waits, success: result.stopReason !== "error" && result.stopReason !== "aborted" });
  }
  console.log(JSON.stringify({ source: "NeoPi streamAnthropic: client maxRetries=0, provider max retries=10", now, random: 0.5, cases: results }, null, 2));
} finally {
  Date.now = originalNow;
  Math.random = originalRandom;
  server.stop(true);
}
