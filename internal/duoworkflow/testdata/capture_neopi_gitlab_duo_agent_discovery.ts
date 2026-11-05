// Executes NeoPi's OWN GitLab Duo Agent discovery (pi-catalog
// discovery/gitlab-duo-workflow.ts): fetchGitLabDuoWorkflowModels, the model
// roster for a namespace, and discoverGitLabDuoWorkflowRuntimeNamespace, the
// namespace a turn runs in. A scripted gitlab.com answers in process; every
// request and the result or error are recorded per scenario. cwd is an empty
// directory, so the git-remote step finds no project, as for a proxy with no
// client workspace. Nothing leaves loopback.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_gitlab_duo_agent_discovery.ts > gitlab_duo_agent_discovery.json
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const discovery = await import(root + "/pi-catalog/src/discovery/gitlab-duo-workflow.ts");
const cwd = mkdtempSync(join(tmpdir(), "gitlab-duo-agent-discovery-"));
const apiKey = "glpat-discovery-capture";

type Answer = { status?: number; body?: unknown; headers?: Record<string, string> };
type Route = (method: string, path: string, body: any) => Answer | undefined;
const json = (body: unknown, headers: Record<string, string> = {}): Answer => ({ status: 200, body, headers });
const models = (availability: unknown): Answer => json({ data: { aiChatAvailableModels: availability } });
const ref = (name: string, refId: string) => ({ name, ref: refId });

let requests: any[] = [];
let route: Route = () => undefined;
// The wire as Bun sends it: requests are forwarded to this loopback server,
// which records each request with its answer from the scenario's route.
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(request) {
		const url = new URL(request.url);
		const headers = Object.fromEntries(request.headers);
		delete headers.host;
		const text = await request.text();
		const answer = route(request.method, url.pathname + url.search, text ? JSON.parse(text) : undefined) ?? { status: 404, body: { message: "404 Not Found" } };
		const response = { status: answer.status ?? 200, headers: { "content-type": "application/json", ...answer.headers }, body: answer.body === undefined ? "" : JSON.stringify(answer.body) };
		requests.push({ method: request.method, url: "https://gitlab.com" + url.pathname + url.search, headers, ...(text ? { body: text } : {}), response });
		return new Response(response.body || null, { status: response.status, headers: response.headers });
	},
});
const nativeFetch = globalThis.fetch;
const loopbackFetch = (async (input: any, init?: any) => {
	const url = new URL(input instanceof Request ? input.url : String(input));
	if (url.origin !== "https://gitlab.com") throw new Error("Unexpected GitLab discovery URL: " + url.href);
	return nativeFetch(`http://127.0.0.1:${server.port}${url.pathname}${url.search}`, init);
}) as typeof fetch;

const groupsPage1 = [
	{ id: 101, full_path: "alpha", path: "alpha" },
	{ id: 102, full_path: "beta", path: "beta", duo_features_enabled: true },
];
const groupsPage2 = [{ id: 103, full_path: "gamma", path: "gamma", duo_core_features_enabled: true }, { name: "no id" }];
const graphModels = (byGroup: Record<string, Answer>): Route => (method, path, body) => {
	if (method === "POST" && path === "/api/graphql" && String(body?.query).includes("aiChatAvailableModels")) {
		return byGroup[body.variables.rootNamespaceId] ?? models(null);
	}
	return undefined;
};
const groups: Route = (method, path) => {
	if (method !== "GET" || !path.startsWith("/api/v4/groups?")) return undefined;
	return new URL("https://x" + path).searchParams.get("page") === "1" ? json(groupsPage1, { "x-next-page": "2" }) : json(groupsPage2, { "x-next-page": "" });
};
const both = (...routes: Route[]): Route => (method, path, body) => {
	for (const route of routes) {
		const answer = route(method, path, body);
		if (answer) return answer;
	}
	return undefined;
};

const scenarios: { name: string; env?: Record<string, string>; config?: Record<string, string>; route: Route }[] = [
	{
		name: "groups-preferred-paged",
		route: both(groups, graphModels({
			"gid://gitlab/Group/102": models(null),
			"gid://gitlab/Group/103": models({ defaultModel: ref("Claude Sonnet 4.6", "claude_sonnet_4_6_vertex"), selectableModels: [ref("Claude Sonnet 4.6", "claude_sonnet_4_6_vertex"), ref("GPT-5", "gpt_5"), { ref: "  " }, { name: "nameless" }, ref("", "gemini_2_5_pro")], pinnedModel: null }),
		})),
	},
	{
		name: "groups-pinned",
		route: both(groups, graphModels({ "gid://gitlab/Group/102": models({ defaultModel: ref("Claude Haiku", "claude_haiku_4_5"), selectableModels: [ref("GPT-5", "gpt_5")], pinnedModel: ref("Claude Opus", "claude_opus_4_8") }) })),
	},
	{
		name: "groups-default-only",
		route: both(groups, graphModels({ "gid://gitlab/Group/102": models({ defaultModel: ref("Mystery", "mystery_model"), selectableModels: [] }) })),
	},
	{
		name: "namespace-override",
		env: { GITLAB_DUO_NAMESPACE_ID: "555" },
		route: both(
			(method, path) => (method === "GET" && path === "/api/v4/groups/555" ? json({ id: 555, full_path: "override-group", root_namespace_id: 500 }) : undefined),
			graphModels({ "gid://gitlab/Group/555": models({ selectableModels: [ref("GPT-5", "gpt_5")] }), "gid://gitlab/Group/500": models({ selectableModels: [ref("GPT-5", "gpt_5")] }) }),
		),
	},
	{
		name: "namespace-override-gid-without-models",
		env: { GITLAB_DUO_NAMESPACE_ID: "gid://gitlab/Group/556" },
		route: both(groups, graphModels({ "gid://gitlab/Group/103": models({ selectableModels: [ref("GPT-5", "gpt_5")] }) })),
	},
	{
		name: "project-numeric-graphql-root",
		env: { GITLAB_DUO_PROJECT_ID: "4242" },
		route: both(
			(method, path) => (method === "GET" && path === "/api/v4/projects/4242" ? json({ id: 4242, path_with_namespace: "team/sub/app", namespace: { id: 77, full_path: "team/sub" } }) : undefined),
			(method, path, body) => (method === "POST" && path === "/api/graphql" && String(body?.query).includes("rootAncestor") ? json({ data: { project: { namespace: { id: "gid://gitlab/Group/77", rootAncestor: { id: "gid://gitlab/Group/70" } } } } }) : undefined),
			graphModels({ "gid://gitlab/Group/70": models({ selectableModels: [ref("GPT-5", "gpt_5")] }) }),
		),
	},
	{
		name: "project-path-rest-root",
		config: { projectPath: "team/app" },
		route: both(
			(method, path) => (method === "GET" && path === "/api/v4/projects/team%2Fapp" ? json({ id: 9, path_with_namespace: "team/app", namespace: { id: 70, root_namespace_id: 70 } }) : undefined),
			graphModels({ "70": models({ selectableModels: [ref("GPT-5", "gpt_5")] }), "gid://gitlab/Group/70": models({ selectableModels: [ref("GPT-5", "gpt_5")] }) }),
		),
	},
	{ name: "graphql-failure", route: both(groups, (method, path) => (method === "POST" && path === "/api/graphql" ? { status: 500, body: { message: "boom" } } : undefined)) },
	{ name: "groups-unauthorized", route: (method, path) => (method === "GET" && path.startsWith("/api/v4/groups?") ? { status: 401, body: { message: "401 Unauthorized" } } : undefined) },
];

const results: any[] = [];
for (const scenario of scenarios) {
	for (const key of ["GITLAB_DUO_NAMESPACE_ID", "GITLAB_DUO_PROJECT_ID", "GITLAB_DUO_PROJECT_PATH"]) delete process.env[key];
	Object.assign(process.env, scenario.env ?? {});
	route = scenario.route;
	const config = { apiKey, baseUrl: "https://gitlab.com/", fetch: loopbackFetch, cwd, ...scenario.config };
	const run = async (call: () => Promise<unknown>) => {
		requests = [];
		let result: unknown = null;
		let error: string | null = null;
		try {
			result = await call();
		} catch (cause: any) {
			error = String(cause?.message ?? cause);
		}
		return { requests, result, error };
	};
	results.push({
		name: scenario.name,
		env: scenario.env ?? {},
		config: scenario.config ?? {},
		models: await run(() => discovery.fetchGitLabDuoWorkflowModels(config)),
		runtime: await run(() => discovery.discoverGitLabDuoWorkflowRuntimeNamespace(config)),
	});
}
for (const key of ["GITLAB_DUO_NAMESPACE_ID", "GITLAB_DUO_PROJECT_ID", "GITLAB_DUO_PROJECT_PATH"]) delete process.env[key];
server.stop(true);
console.log(JSON.stringify({ source: "NeoPi OWN gitlab-duo-agent discovery (fetchGitLabDuoWorkflowModels, discoverGitLabDuoWorkflowRuntimeNamespace)", apiKey, fallback: discovery.buildGitLabDuoWorkflowFallbackModel(), scenarios: results }, null, 2));
