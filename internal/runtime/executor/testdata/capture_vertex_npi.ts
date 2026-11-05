// Runs NeoPi's OWN google-vertex provider (Gemini over Vertex AI) through
// streamSimple, the agent entry, against a loopback capture server only:
// every request NeoPi's fetch makes is redirected there, the keys and the
// access token are fakes, and no request can reach Google. Each scenario is
// one NeoPi context, the environment NeoPi resolves Vertex's project,
// location and credentials from, and the OpenAI Chat Completions request a
// client sends NeoProxy for it.
//
//   cd <neopi env> && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_vertex_npi.ts | gzip -9 > vertex_npi_capture.json.gz
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { streamSimple } = await import(root + "/pi-ai/src/stream.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");

const apiKey = "AQ.VertexFixtureKey000000000000000000000";
const envApiKey = "AQ.VertexEnvKey0000000000000000000000000";
const accessToken = "ya29.vertex-fixture-access-token";
const sessionId = "capture-session-vertex-0001";
const okChunk = {
	candidates: [{ content: { role: "model", parts: [{ text: "Captured." }] }, finishReason: "STOP" }],
	usageMetadata: { promptTokenCount: 3, candidatesTokenCount: 1, totalTokenCount: 4 },
};
const okSSE = `data: ${JSON.stringify(okChunk)}\r\n\r\n`;
const notFound = JSON.stringify({ error: { code: 404, message: "Publisher model not found.", status: "NOT_FOUND" } });

// Every Vertex environment variable NeoPi reads; each scenario sets its own.
const vertexEnv = [
	"GOOGLE_CLOUD_API_KEY",
	"GOOGLE_CLOUD_PROJECT",
	"GCP_PROJECT",
	"GCLOUD_PROJECT",
	"GOOGLE_VERTEX_LOCATION",
	"GOOGLE_CLOUD_LOCATION",
	"VERTEX_LOCATION",
	"GOOGLE_CLOUD_ACCESS_TOKEN",
	"CLOUDSDK_AUTH_ACCESS_TOKEN",
	"GOOGLE_APPLICATION_CREDENTIALS",
];

let captured: any[] = [];
// Status codes the capture server answers with, in order; then 200.
let statuses: number[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const rawBody = await request.text();
		captured.push({ method: request.method, path: url.pathname + url.search, headers: Object.fromEntries(request.headers), rawBody });
		const status = statuses.shift();
		if (status && status !== 200) return new Response(notFound, { status, headers: { "content-type": "application/json" } });
		return new Response(okSSE, { headers: { "content-type": "text/event-stream" } });
	},
});
const local = `http://127.0.0.1:${server.port}`;
const bundled = new Map(getBundledModels("google-vertex").filter((model: any) => model.api === "google-vertex").map((model: any) => [model.id, model]));
const row = (id: string) => {
	const model = bundled.get(id);
	if (!model) throw new Error("no bundled google-vertex row " + id);
	return model;
};

const lookup = { name: "lookup", description: "Look up a term.", parameters: { type: "object", properties: { term: { type: "string" } }, required: ["term"] } };
const other = { name: "other", description: "", parameters: { type: "object", properties: { n: { type: ["integer", "null"] }, mode: { enum: ["a", "b"] } }, additionalProperties: false } };
const usage = { input: 1, output: 1, totalTokens: 2, cost: { input: 0, output: 0, total: 0 } };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
const signature = "EggKBgEMOdbHNA==";
const user = (content: unknown) => ({ role: "user", content, timestamp: 0 });

// A turn renders to a NeoPi message and to an OpenAI Chat message.
type Turn =
	| { kind: "user"; text?: string; image?: boolean }
	| { kind: "call"; signed?: boolean }
	| { kind: "result"; text: string };
const neopiTurns = (model: any, turns: Turn[]) =>
	turns.map(turn => {
		switch (turn.kind) {
			case "user":
				return user(turn.image ? [{ type: "text", text: turn.text }, { type: "image", data: png, mimeType: "image/png" }] : turn.text);
			case "call":
				return {
					role: "assistant",
					content: [
						{ type: "thinking", thinking: "Need lookup.", thinkingSignature: "" },
						{ type: "toolCall", id: "call_capture_1", name: "lookup", arguments: { term: "a" }, ...(turn.signed ? { thoughtSignature: signature } : {}) },
					],
					api: model.api,
					provider: turn.signed ? model.provider : "neoproxy",
					model: turn.signed ? model.id : "neoproxy/" + model.id,
					usage,
					stopReason: "toolUse",
					timestamp: 0,
				};
			case "result":
				return { role: "toolResult", toolCallId: "call_capture_1", toolName: "lookup", content: [{ type: "text", text: turn.text }], isError: false, timestamp: 0 };
		}
	});
const chatTurns = (turns: Turn[]) =>
	turns.map(turn => {
		switch (turn.kind) {
			case "user":
				return {
					role: "user",
					content: turn.image ? [{ type: "text", text: turn.text }, { type: "image_url", image_url: { url: `data:image/png;base64,${png}` } }] : turn.text,
				};
			case "call":
				return {
					role: "assistant",
					content: "",
					reasoning_content: "Need lookup.",
					tool_calls: [
						{
							id: "call_capture_1",
							type: "function",
							function: { name: "lookup", arguments: JSON.stringify({ term: "a" }) },
							...(turn.signed ? { extra_content: { google: { thought_signature: signature } } } : {}),
						},
					],
				};
			case "result":
				return { role: "tool", tool_call_id: "call_capture_1", content: turn.text };
		}
	});

// env is the scenario's Vertex environment; apiKey is NeoPi's options.apiKey
// (the account's key), absent for an ADC account. statuses are the capture
// server's answers in order.
type Scenario = { name: string; model: string; context: any; options: any; downstream: any; env: Record<string, string>; apiKey?: string; statuses?: number[] };
const scenarios: Scenario[] = [];
const keyed = { env: {}, apiKey };
const add = (id: string, name: string, turns: Turn[], options: any, extra: { system?: string[]; tools?: any[] } = {}, auth: { env: Record<string, string>; apiKey?: string; statuses?: number[] } = keyed) => {
	const model = row(id);
	const context = { ...(extra.system ? { systemPrompt: extra.system } : {}), messages: neopiTurns(model, turns), ...(extra.tools ? { tools: extra.tools } : {}) };
	const downstream: any = { model: id, messages: [...(extra.system ?? []).map(text => ({ role: "system", content: text })), ...chatTurns(turns)], stream: true };
	if (extra.tools) downstream.tools = extra.tools.map(t => ({ type: "function", function: { name: t.name, description: t.description, parameters: t.parameters } }));
	if (options.maxTokens) downstream.max_tokens = options.maxTokens;
	if (options.temperature !== undefined) downstream.temperature = options.temperature;
	if (options.topP !== undefined) downstream.top_p = options.topP;
	if (options.topK !== undefined) downstream.top_k = options.topK;
	if (options.serviceTier) downstream.service_tier = options.serviceTier;
	if (options.disableReasoning) downstream.reasoning_effort = "none";
	else if (options.reasoning) downstream.reasoning_effort = options.reasoning;
	if (options.toolChoice === "any") downstream.tool_choice = "required";
	else if (options.toolChoice === "none") downstream.tool_choice = "none";
	else if (options.toolChoice?.type === "tool") downstream.tool_choice = { type: "function", function: { name: options.toolChoice.name } };
	scenarios.push({ name: `${id}/${name}`, model: id, context, options, downstream, ...auth });
};

const hello: Turn[] = [{ kind: "user", text: "Say hello." }];
const lookupTurns: Turn[] = [{ kind: "user", text: "Use lookup." }];
const history = (signed: boolean): Turn[] => [{ kind: "user", text: "Use lookup." }, { kind: "call", signed }, { kind: "result", text: "Found." }];

// Request shaping on every bundled Gemini-over-Vertex row.
for (const id of bundled.keys()) {
	const efforts: string[] = row(id).thinking?.efforts ?? [];
	const first = efforts[0];
	const level = first ? { reasoning: first } : {};
	add(id, "plain", hello, {});
	for (const effort of efforts) add(id, `effort-${effort}`, hello, { reasoning: effort });
	add(id, "off", hello, { disableReasoning: true });
	add(id, "sampling", hello, { ...level, temperature: 0.3, topP: 0.9, topK: 40, maxTokens: 300 });
	add(id, "system", hello, level, { system: ["A source system.", "Second system."] });
	add(id, "tools", lookupTurns, { ...level, maxTokens: 512 }, { system: ["A source system."], tools: [lookup, other] });
	add(id, "tool-history", history(false), level, { tools: [lookup] });
	add(id, "tool-history-signed", history(true), level, { tools: [lookup] });
	add(id, "forced-any", lookupTurns, { ...level, toolChoice: "any" }, { tools: [lookup] });
	add(id, "forced-named", lookupTurns, { ...level, toolChoice: { type: "tool", name: "lookup" } }, { tools: [lookup, other] });
	add(id, "choice-none", lookupTurns, { ...level, toolChoice: "none" }, { tools: [lookup] });
	add(id, "user-image", [{ kind: "user", text: "Describe.", image: true }], level);
	add(id, "tier-priority", hello, { ...level, serviceTier: "priority" });
	add(id, "tier-flex", hello, { ...level, serviceTier: "flex" });
}

// Endpoint and credential resolution, on one row.
const transportID = "gemini-3.5-flash";
const project = { GOOGLE_CLOUD_PROJECT: "fixture-project" };
const transports: [string, { env: Record<string, string>; apiKey?: string; statuses?: number[] }][] = [
	["key-global", { env: {}, apiKey }],
	["key-ambient-region", { env: { GOOGLE_CLOUD_LOCATION: "us-central1" }, apiKey }],
	["key-ambient-region-404-falls-back-global", { env: { GOOGLE_CLOUD_LOCATION: "us-central1" }, apiKey, statuses: [404] }],
	["key-ambient-global-404", { env: { GOOGLE_CLOUD_LOCATION: "global" }, apiKey, statuses: [404] }],
	["key-ambient-vertex-location-wins", { env: { GOOGLE_VERTEX_LOCATION: "europe-west4", GOOGLE_CLOUD_LOCATION: "us-central1", VERTEX_LOCATION: "asia-east1" }, apiKey }],
	["key-ambient-eu-multiregion", { env: { VERTEX_LOCATION: "eu" }, apiKey }],
	["key-ambient-us-multiregion", { env: { GOOGLE_CLOUD_LOCATION: "us" }, apiKey }],
	["key-ambient-region-500", { env: { GOOGLE_CLOUD_LOCATION: "us-central1" }, apiKey, statuses: [500] }],
	["key-env", { env: { GOOGLE_CLOUD_API_KEY: envApiKey } }],
	["key-sentinel-uses-env", { env: { GOOGLE_CLOUD_API_KEY: envApiKey }, apiKey: "<authenticated>" }],
	["key-na-uses-env", { env: { GOOGLE_CLOUD_API_KEY: envApiKey }, apiKey: "N/A" }],
	["key-project-ignored", { env: { ...project, GOOGLE_CLOUD_LOCATION: "us-east5" }, apiKey }],
	["adc-token", { env: { ...project, GOOGLE_CLOUD_LOCATION: "us-central1", GOOGLE_CLOUD_ACCESS_TOKEN: accessToken } }],
	["adc-token-global", { env: { ...project, GOOGLE_CLOUD_LOCATION: "global", GOOGLE_CLOUD_ACCESS_TOKEN: accessToken } }],
	["adc-token-gcloud-sdk", { env: { GCP_PROJECT: "fixture-gcp", VERTEX_LOCATION: "eu", CLOUDSDK_AUTH_ACCESS_TOKEN: accessToken } }],
	["adc-gcloud-project", { env: { GCLOUD_PROJECT: "fixture-gcloud", GOOGLE_VERTEX_LOCATION: "asia-northeast1", GOOGLE_CLOUD_ACCESS_TOKEN: accessToken } }],
	["adc-region-404-no-fallback", { env: { ...project, GOOGLE_CLOUD_LOCATION: "us-central1", GOOGLE_CLOUD_ACCESS_TOKEN: accessToken }, statuses: [404] }],
	["adc-sentinel-key", { env: { ...project, GOOGLE_CLOUD_LOCATION: "us-central1", GOOGLE_CLOUD_ACCESS_TOKEN: accessToken }, apiKey: "<authenticated>" }],
	["adc-no-project", { env: { GOOGLE_CLOUD_LOCATION: "us-central1", GOOGLE_CLOUD_ACCESS_TOKEN: accessToken } }],
	["adc-no-location", { env: { ...project, GOOGLE_CLOUD_ACCESS_TOKEN: accessToken } }],
];
for (const [name, auth] of transports) add(transportID, `transport-${name}`, hello, {}, {}, auth);
add(transportID, "transport-adc-tier-priority", hello, { serviceTier: "priority" }, {}, transports.find(([name]) => name === "adc-token")![1]);

const fixtures: any[] = [];
const saved = Object.fromEntries(vertexEnv.map(name => [name, process.env[name]]));
try {
	for (const scenario of scenarios) {
		captured = [];
		statuses = [...(scenario.statuses ?? [])];
		for (const name of vertexEnv) delete process.env[name];
		Object.assign(process.env, scenario.env);
		const requestedURLs: string[] = [];
		const redirectFetch = (async (input: any, init: any) => {
			const requested = input instanceof Request ? input.url : String(input);
			requestedURLs.push(requested);
			const u = new URL(requested);
			if (!u.hostname.endsWith("googleapis.com")) throw new Error("Unexpected non-Google request " + requested);
			const headers = new Headers(init?.headers);
			headers.set("host", u.host);
			return fetch(local + u.pathname + u.search, { ...init, headers, tls: undefined });
		}) as typeof fetch;
		const options = { ...(scenario.apiKey !== undefined ? { apiKey: scenario.apiKey } : {}), sessionId, fetch: redirectFetch, maxHttpAttempts: 1, ...scenario.options };
		const result = await streamSimple(row(scenario.model), scenario.context, options).result();
		const requests = captured.map((request, i) => ({ ...request, requestedURL: requestedURLs[i], body: JSON.parse(request.rawBody) }));
		const fixture: any = { name: scenario.name, model: scenario.model, env: scenario.env, apiKey: scenario.apiKey ?? "", statuses: scenario.statuses ?? [], options: scenario.options, downstream: scenario.downstream, requests };
		if (result.stopReason !== "stop") fixture.error = result.errorMessage;
		fixtures.push(fixture);
	}
	console.log(JSON.stringify({ source: 'NeoPi OWN streamSimple over getBundledModels("google-vertex") google-vertex rows', apiKey, envApiKey, accessToken, sessionId, fixtures }, null, 2));
} finally {
	for (const [name, value] of Object.entries(saved)) {
		if (value === undefined) delete process.env[name];
		else process.env[name] = value;
	}
	server.stop(true);
}
