// Runs NeoPi's OWN loginGitHubCopilot against a scripted fetch only (no
// request can leave the process) for public github.com and a GitHub
// Enterprise domain, recording every request it makes: the device code, the
// polls (pending, slow_down, token), the plan endpoint lookup and the model
// policy enabling, where one model's chat identity is denied so the CLI retry
// is exercised. Writes testdata/neopi_github_copilot_auth.json.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_github_copilot_auth.ts
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { loginGitHubCopilot } = await import(root + "/pi-ai/src/registry/oauth/github-copilot.ts");
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");

const token = "gho_capturefixturetoken0000000000000000";
const modelIds = [...new Set(getBundledModels("github-copilot").map((m: any) => m.requestModelId ?? m.id))];
const denied = modelIds[0];

const scenarios: any[] = [];
for (const [name, input] of [["public", ""], ["enterprise", "https://company.ghe.com/"]]) {
	const requests: any[] = [];
	let polls = 0;
	const fetchImpl = (async (url: string, init: any = {}) => {
		const headers = Object.fromEntries(new Headers(init.headers));
		const body = init.body === undefined ? "" : String(init.body);
		requests.push({ method: init.method ?? "GET", url: String(url), headers, body });
		const json = (status: number, value: unknown) => new Response(JSON.stringify(value), { status, headers: { "content-type": "application/json" } });
		const u = new URL(String(url));
		if (u.pathname === "/login/device/code") {
			return json(200, { device_code: "dev-code", user_code: "ABCD-1234", verification_uri: `https://${u.host}/login/device`, interval: 5, expires_in: 900 });
		}
		if (u.pathname === "/login/oauth/access_token") {
			polls++;
			if (polls === 1) return json(200, { error: "authorization_pending" });
			if (polls === 2) return json(200, { error: "slow_down", interval: 7 });
			return json(200, { access_token: token, token_type: "bearer", scope: "read:user" });
		}
		if (u.pathname === "/copilot_internal/user") {
			return json(200, { endpoints: { api: "https://api.individual.githubcopilot.com/" } });
		}
		if (u.pathname.endsWith("/policy")) {
			const integration = new Headers(init.headers).get("Copilot-Integration-Id");
			if (u.pathname.includes(`/models/${denied}/`) && integration === "copilot-chat") return json(403, { error: { message: "denied" } });
			return json(200, { state: "enabled" });
		}
		throw new Error("Unexpected request " + url);
	}) as any;
	const credentials = await loginGitHubCopilot({
		onAuth: () => {},
		onPrompt: async () => input,
		fetch: fetchImpl,
		pollIntervalFloorMs: 1,
		pollIntervalScaleMs: 1,
	});
	scenarios.push({ name, input, credentials: { ...credentials, expires: undefined }, requests });
}
await Bun.write(join(import.meta.dir, "neopi_github_copilot_auth.json"), JSON.stringify({ source: "NeoPi OWN loginGitHubCopilot", token, modelIds, denied, scenarios }, null, 1));
console.log(`auth: ${scenarios.map(s => `${s.name} ${s.requests.length} requests`).join(", ")}`);
