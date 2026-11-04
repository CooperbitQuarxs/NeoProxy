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
   for (const isOAuth of [true, false]) {
    const apiKey = isOAuth ? token : "sk-ant-api-parity-fake";
    const baseUrl = official ? "https://api.anthropic.com" : local;
   for (const modelId of ["claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-sonnet-4-5"]) {
    const model = buildModel({id:modelId, name:modelId, api:"anthropic-messages", provider:"anthropic", baseUrl, reasoning:true, input:["text","image"], cost:{input:0,output:0,cacheRead:0,cacheWrite:0}, contextWindow:1000000, maxTokens:128000});
    const names = isOAuth ? ["plain-high", "custom-tool", "stream-high", "thinking-off"] : ["plain-high", "custom-tool", "stream-high", "thinking-off", "plain", "system-metadata", "tool-history"];
    if (modelId === "claude-opus-5-5") names.push("unicode");
    for (const name of names) {
      const text = name === "unicode" ? "A🙂BC\u2028def\u2029<>& \\u2028" : name === "custom-tool" ? "Use lookup." : name === "stream-high" ? "Stream hello." : "Say hello.";
      const tools = name === "custom-tool" ? [{name:"lookup",description:"Look up a term.",parameters:{type:"object",properties:{term:{type:"string"}},required:["term"]}}] : undefined;
      const off = name === "thinking-off";
      const plain = name === "plain" || name === "system-metadata" || name === "tool-history";
      const messages: any[] = [{role:"user" as const,content:text,timestamp:0}];
      if (name === "tool-history") messages.push({role:"assistant",content:[{type:"toolCall",id:"tool:1#🦊",name:"_lookup",arguments:{term:"a"}}],api:"anthropic-messages",provider:"anthropic",model:model.id,usage:{input:1,output:1,totalTokens:2,cost:{input:0,output:0,total:0}},stopReason:"toolUse",timestamp:0},{role:"toolResult",toolCallId:"tool:1#🦊",toolName:"_lookup",content:[{type:"text",text:"Found."}],isError:false,timestamp:0});
      const context = {messages, ...(tools ? {tools} : {}), ...(name === "system-metadata" ? {systemPrompt:["A source system."]} : {})};
      let requestedURL = "";
      const redirectFetch = (async (input: any, init: any) => {
        requestedURL = input instanceof Request ? input.url : String(input);
        const u = new URL(requestedURL);
        if (u.origin !== baseUrl) throw new Error("Unexpected non-capture request");
        const headers = new Headers(init?.headers);
        headers.set("host", u.host);
        return fetch(local + u.pathname + u.search, {...init, headers, tls:undefined});
      }) as typeof fetch;
      const result = await streamAnthropic(model, context, {apiKey,isOAuth,...(!plain ? {thinkingEnabled:!off,...(!off ? {effort:"high" as const} : {})} : {}),maxTokens:64,sessionId,metadata:name === "system-metadata" ? {user_id:"caller-user"} : {account_uuid:accountId},fetch:redirectFetch}).result();
      if (result.stopReason !== "stop") throw new Error("Capture failed: " + result.errorMessage);
      const wireMessages: any[] = [{role:"user",content:text}];
      if (name === "tool-history") wireMessages.push({role:"assistant",content:[{type:"tool_use",id:"tool:1#🦊",name:"_lookup",input:{term:"a"}}]},{role:"user",content:[{type:"tool_result",tool_use_id:"tool:1#🦊",content:[{type:"text",text:"Found."}]}]});
      const activeThinking = model.thinking?.mode === "anthropic-adaptive" ? {thinking:{type:"adaptive"},output_config:{effort:"high"}} : {thinking:{type:"enabled",budget_tokens:1024}};
      const input = {model:model.id,messages:wireMessages,max_tokens:64,...(!plain ? (off ? {thinking:{type:"disabled"}} : activeThinking) : {}),...(tools ? {tools:tools.map(tool=>({name:tool.name,description:tool.description,input_schema:tool.parameters}))} : {}),...(name === "system-metadata" ? {system:[{type:"text",text:"A source system."}],metadata:{user_id:"caller-user"}} : {})};
      if (process.env.NEOPI_CAPTURE_URL) captured = await (await fetch(local+"/__capture/latest")).json();
      fixtures.push({name:(modelId === "claude-opus-5-5" ? "" : modelId+"/")+(isOAuth?"oauth/":"api-key/")+(official?"official/":"local/")+name, isOAuth, apiKey, baseUrl, input, downstreamStream:name === "stream-high", requestedURL, ...captured});
    }
  }
   }
   }
  const installId = (await readFile(homedir()+"/.omp/install-id", "utf8")).trim();
  const output = {source:"NeoPi packages/ai/src/providers/anthropic.ts; Claude Code 2.1.280 / SDK 0.112.1", installId, sessionId, accountId, token, response:sse, fixtures};
  console.log(JSON.stringify(output,null,2));
} finally { server.stop(true); }
