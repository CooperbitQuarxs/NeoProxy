// Run from an isolated HOME with NeoPi's source packages on Bun's module path.
// All fetches are redirected to this local capture server; the OAuth key is fake.
const sourceModules = process.env.NEOPI_SOURCE_MODULES;
if (!sourceModules) throw new Error("NEOPI_SOURCE_MODULES must point to the isolated @oh-my-pi source symlinks");
const {streamAnthropic} = await import(sourceModules + "/pi-ai/src/providers/anthropic.ts");
const {buildModel} = await import(sourceModules + "/pi-catalog/src/build.ts");
import { readFile } from "node:fs/promises";
import { homedir } from "node:os";

const sessionId = "11111111-1111-4111-8111-111111111111";
const accountId = "22222222-2222-4222-8222-222222222222";
const token = "sk-ant-oat-parity-fake";
const fixtures: unknown[] = [];
let captured: any;
const events = [
  {type: "message_start", message: {id: "msg_capture", type: "message", role: "assistant", model: "claude-opus-5-5", content: [], stop_reason: null, stop_sequence: null, usage: {input_tokens: 1, output_tokens: 0}}},
  {type: "content_block_start", index: 0, content_block: {type: "text", text: ""}},
  {type: "content_block_delta", index: 0, delta: {type: "text_delta", text: "Captured."}},
  {type: "content_block_stop", index: 0},
  {type: "message_delta", delta: {stop_reason: "end_turn", stop_sequence: null}, usage: {output_tokens: 1}},
  {type: "message_stop"}
];
const sse = events.map(event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join("");
const server = Bun.serve({hostname: "127.0.0.1", port: 0, async fetch(request) {
  if (new URL(request.url).pathname === "/__capture/latest") return Response.json(captured);
  const rawBody = await request.text();
  captured = {method: request.method, path: new URL(request.url).pathname + new URL(request.url).search, headers: Object.fromEntries(request.headers), rawBody, body: JSON.parse(rawBody)};
  return new Response(sse, {headers: {"content-type": "text/event-stream"}});
}});
const local = process.env.NEOPI_CAPTURE_URL ?? `http://127.0.0.1:${server.port}`;
try {
  for (const official of [false, true]) {
    const baseUrl = official ? "https://api.anthropic.com" : local;
    const model = buildModel({id:"claude-opus-5-5", name:"Claude Opus 5.5", api:"anthropic-messages", provider:"anthropic", baseUrl, reasoning:true, input:["text","image"], cost:{input:0,output:0,cacheRead:0,cacheWrite:0}, contextWindow:1000000, maxTokens:128000});
    for (const name of ["plain-high", "custom-tool", "stream-high", "thinking-off"]) {
      const text = name === "custom-tool" ? "Use lookup." : name === "stream-high" ? "Stream hello." : "Say hello.";
      const tools = name === "custom-tool" ? [{name:"lookup",description:"Look up a term.",parameters:{type:"object",properties:{term:{type:"string"}},required:["term"]}}] : undefined;
      const off = name === "thinking-off";
      const context = {messages:[{role:"user" as const,content:text,timestamp:0}], ...(tools ? {tools} : {})};
      let requestedURL = "";
      const redirectFetch = (async (input: any, init: any) => {
        requestedURL = input instanceof Request ? input.url : String(input);
        const u = new URL(requestedURL);
        if (u.origin !== baseUrl) throw new Error("Unexpected non-capture request");
        const headers = new Headers(init?.headers);
        headers.set("host", u.host);
        return fetch(local + u.pathname + u.search, {...init, headers, tls:undefined});
      }) as typeof fetch;
      const result = await streamAnthropic(model, context, {apiKey:token,isOAuth:true,thinkingEnabled:!off,...(!off ? {effort:"high" as const} : {}),maxTokens:64,sessionId,metadata:{account_uuid:accountId},fetch:redirectFetch}).result();
      if (result.stopReason !== "stop") throw new Error("Capture failed: " + result.errorMessage);
      const input = {model:model.id,messages:[{role:"user",content:text}],max_tokens:64,...(off ? {thinking:{type:"disabled"}} : {thinking:{type:"adaptive"},output_config:{effort:"high"}}),...(tools ? {tools:tools.map(tool=>({name:tool.name,description:tool.description,input_schema:tool.parameters}))} : {})};
      if (process.env.NEOPI_CAPTURE_URL) captured = await (await fetch(local+"/__capture/latest")).json();
      fixtures.push({name:(official?"official/":"local/")+name, baseUrl, input, downstreamStream:name === "stream-high", requestedURL, ...captured});
    }
  }
  const installId = (await readFile(homedir()+"/.omp/install-id", "utf8")).trim();
  const output = {source:"NeoPi packages/ai/src/providers/anthropic.ts; Claude Code 2.1.280 / SDK 0.112.1", installId, sessionId, accountId, token, response:sse, fixtures};
  console.log(JSON.stringify(output,null,2));
} finally { server.stop(true); }
