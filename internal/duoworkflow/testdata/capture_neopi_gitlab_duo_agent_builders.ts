// Executes NeoPi's OWN GitLab Duo Agent request builders (pi-ai
// providers/gitlab-duo-workflow.ts) the way the auth gateway reaches them: an
// OpenAI chat body through openai-chat-server parseRequest, then the
// WebSocket start request (goal transcript, inline flow with the sanitized
// system prompt, MCP tools, metadata), its approval form, the WebSocket URL
// and headers, the REST bodies, and the tool-result answer. System prompts
// carry what the flow-prompt sanitizer rewrites. Nothing touches a network.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_gitlab_duo_agent_builders.ts > gitlab_duo_agent_builders.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const workflow = await import(root + "/pi-ai/src/providers/gitlab-duo-workflow.ts");
const { parseRequest } = await import(root + "/pi-ai/src/providers/openai-chat-server.ts");
const { buildGitLabDuoWorkflowModelSpec } = await import(root + "/pi-catalog/src/discovery/gitlab-duo-workflow.ts");

const lookup = { type: "function", function: { name: "lookup", description: "Look a thing up.", parameters: { type: "object", properties: { query: { type: "string" }, limit: { type: "integer", minimum: 1 } }, required: ["query"] } } };
const bare = { type: "function", function: { name: "now" } };
const sanitizerPrompts = [
	"Keep <!-- an HTML comment --> out.",
	"Unterminated <!-- comment and \\u003c!-- escaped and \\\\u003c!-- doubled.",
	"Tags: <goal>x</goal> < / SYSTEM > <ſyſtem> <truncated_tool_output/> <TRUNCATION_NOTICE >",
	"Escaped: \\u003csystem\\u003e and \\\\u003c/goal\\\\u003e and \\u003c\u0085goal\u001c\\u003e",
	"[//]: # (hidden note)\nVisible [comment]: <x> (also hidden) and a mid-line [//]: marker.",
	"Tag chars: A\u{E0041}\u{E007F} variation \u{E0100}\u{E01EF} and escaped \\udb40\\udc41 \\uDB40\\uDD00.",
	"Lone \ud800 surrogate and nested <!-- <!-- --> --> and <go<!-- x -->al>.",
	"Fixed point: <<!-- -->system> and <!<!-- -->-- y -->.",
];
const scenarios = [
	{ name: "single-turn", body: { model: "claude_sonnet_4_6_vertex", messages: [{ role: "system", content: "You are NeoPi." }, { role: "user", content: "Hello there" }] } },
	{
		name: "tool-loop",
		body: {
			model: "claude_sonnet_4_6_vertex",
			tools: [lookup, bare],
			messages: [
				{ role: "system", content: "First system." },
				{ role: "system", content: "   " },
				{ role: "system", content: "Second system." },
				{ role: "user", content: "Find the weather." },
				{ role: "assistant", content: "Looking it up.", tool_calls: [{ id: "req-1", type: "function", function: { name: "lookup", arguments: JSON.stringify({ i: "Looking up weather", query: "weather", nested: { b: 2, a: 1 }, "2": "two", "1": "one" }) } }] },
				{ role: "tool", tool_call_id: "req-1", content: "Sunny, 21C." },
				{ role: "assistant", content: "", tool_calls: [{ id: "req-2", type: "function", function: { name: "now", arguments: "{}" } }] },
				{ role: "tool", tool_call_id: "req-2", content: [{ type: "text", text: "12:00" }, { type: "text", text: "UTC" }] },
				{ role: "user", content: "And tomorrow?" },
			],
		},
	},
	{
		name: "parts-and-developer",
		body: {
			model: "gpt_5",
			messages: [
				{ role: "developer", content: "Developer note." },
				{ role: "user", content: [{ type: "text", text: "Describe this" }, { type: "image_url", image_url: { url: "data:image/png;base64,iVBORw0KGgo=" } }] },
				{ role: "assistant", content: "It is an image." },
				{ role: "user", content: "" },
			],
		},
	},
	{ name: "no-user", body: { model: "claude_sonnet_4_6_vertex", messages: [{ role: "assistant", content: "Only me." }] } },
	...sanitizerPrompts.map((prompt, index) => ({ name: `sanitize-${index}`, body: { model: "claude_sonnet_4_6_vertex", messages: [{ role: "system", content: prompt }, { role: "user", content: "hi" }, { role: "assistant", content: "yo" }, { role: "user", content: "again" }] } })),
];
const metadataVariants = [
	{ name: "full", options: { projectId: "4242", projectPath: "team/app", namespaceId: "gid://gitlab/Group/70", rootNamespaceId: "70", workflowDefinition: "ambient", inlineFlow: true }, availableModels: { pinnedModel: { name: "Claude Opus", ref: "claude_opus_4_8" } } },
	{ name: "bare", options: {}, availableModels: null },
];

const out: any = { source: "NeoPi OWN gitlab-duo-workflow builders over openai-chat-server parseRequest", scenarios: [] };
for (const scenario of scenarios) {
	const parsed = parseRequest(scenario.body);
	const model = buildGitLabDuoWorkflowModelSpec({ name: parsed.modelId, ref: parsed.modelId });
	const starts: any[] = [];
	for (const variant of metadataVariants) {
		const start = workflow.buildGitLabDuoWorkflowStartRequest("wf-123", model, parsed.context, parsed.context.tools, variant.availableModels, variant.options);
		starts.push({
			variant: variant.name,
			start: JSON.stringify({ startRequest: start }),
			approval: JSON.stringify({ startRequest: workflow.buildGitLabDuoWorkflowApprovalStartRequest(start) }),
		});
	}
	const noTools = workflow.buildGitLabDuoWorkflowStartRequest("wf-123", model, parsed.context, [], null, {});
	out.scenarios.push({ name: scenario.name, body: JSON.stringify(scenario.body), starts, noTools: JSON.stringify({ startRequest: noTools }) });
}
out.webSocket = [
	{ baseUrl: "https://gitlab.com", options: { projectId: "4242", namespaceId: "gid://gitlab/Group/70", rootNamespaceId: "70", selectedModelIdentifier: "claude_opus_4_8", workflowDefinition: "ambient" } },
	{ baseUrl: "https://gitlab.example.com/gitlab/", options: { namespaceId: "70", rootNamespaceId: "gid://gitlab/Group/70" } },
	{ baseUrl: "https://duo-workflow.runway.gitlab.net", options: { projectId: "1", namespaceId: "70", rootNamespaceId: "70", serviceEndpoint: true } },
	{ baseUrl: "http://localhost:3000", options: {} },
].map(entry => ({ ...entry, url: workflow.buildGitLabDuoWorkflowWebSocketUrl(entry.baseUrl, entry.options) }));
out.webSocketHeaders = [
	{ token: "wf-token", baseUrl: "https://gitlab.com/", projectId: "4242", namespaceId: "gid://gitlab/Group/70", rootNamespaceId: "70", extraHeaders: { "x-gitlab-realm": "saas", authorization: "Bearer minted" } },
	{ token: "wf-token" },
	{ token: "wf-token", baseUrl: "https://gitlab.example.com/gitlab" },
].map(options => ({ options, headers: workflow.buildGitLabDuoWorkflowWebSocketHeaders(options) }));
out.createBodies = [
	{ namespaceId: "70", options: { goal: "Do it" } },
	{ namespaceId: "team", options: { projectId: "team/app", goal: "" } },
	{ namespaceId: undefined, options: {} },
].map(entry => ({ ...entry, body: JSON.stringify(workflow.buildGitLabDuoWorkflowCreateBody(entry.namespaceId, entry.options)) }));
out.directAccessBodies = [
	{ rootNamespaceId: "70" },
	{ rootNamespaceId: "gid://gitlab/Group/70", projectId: "team/app" },
].map(entry => ({ ...entry, body: JSON.stringify(workflow.buildGitLabDuoWorkflowDirectAccessBody(entry.rootNamespaceId, entry.projectId, entry.workflowDefinition)) }));
out.settingsBody = JSON.stringify(workflow.buildGitLabDuoWorkflowSettingsBody());
out.stopBody = JSON.stringify(workflow.buildGitLabDuoWorkflowStopBody());
out.toolResults = [
	{ content: [{ type: "text", text: "ok" }, { type: "image", data: "AAAA", mimeType: "image/png" }, { type: "text", text: "done" }], isError: false },
	{ content: [{ type: "text", text: "boom" }], isError: true },
	{ content: [], isError: false },
].map(result => ({ ...result, answer: JSON.stringify(workflow.buildGitLabPlainTextFromToolResult({ role: "toolResult", toolCallId: "r", toolName: "t", timestamp: 0, ...result } as any)) }));
console.log(JSON.stringify(out, null, 2));
