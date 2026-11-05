// Runs NeoPi's OWN Vertex AI credential resolution (pi-ai providers/google-auth
// getVertexAccessToken) over every credential source and type, with every
// request its fetch makes answered by this script: no request leaves the
// process. The clock is pinned, so a service account's RS256 assertion is
// deterministic. testdata/fixture_key.pem is a throwaway key made for these
// captures.
//
//   cd <neopi env> && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_vertex_adc.ts > vertex_adc_capture.json
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getVertexAccessToken, __resetVertexTokenCache } = await import(root + "/pi-ai/src/providers/google-auth.ts");

const now = Date.UTC(2026, 10, 5, 12, 0, 0);
Date.now = () => now;
const privateKey = fs.readFileSync(path.join(import.meta.dir, "fixture_key.pem"), "utf8");
const serviceAccount = {
	type: "service_account",
	project_id: "fixture-project",
	private_key_id: "fixture-key-id-0001",
	private_key: privateKey,
	client_email: "fixture-sa@fixture-project.iam.gserviceaccount.com",
	client_id: "100000000000000000001",
	token_uri: "https://oauth2.googleapis.com/token",
};
const serviceAccountNoKid = { ...serviceAccount, private_key_id: undefined };
const authorizedUser = {
	type: "authorized_user",
	client_id: "fixture-client.apps.googleusercontent.com",
	client_secret: "fixture-client-secret",
	refresh_token: "1//fixture-refresh-token",
};
const impersonated = (source: unknown, delegates?: string[]) => ({
	type: "impersonated_service_account",
	service_account_impersonation_url:
		"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/target-sa@fixture-project.iam.gserviceaccount.com:generateAccessToken",
	source_credentials: source,
	...(delegates ? { delegates } : {}),
});

const home = fs.mkdtempSync(path.join(os.tmpdir(), "vertex-adc-"));
// NeoPi resolves gcloud's user ADC under os.homedir(), which the capture
// environment's HOME fixes at launch; that file must not already exist.
const userAdc = path.join(os.homedir(), ".config", "gcloud", "application_default_credentials.json");
if (fs.existsSync(userAdc)) throw new Error(`refusing to overwrite ${userAdc}`);
const credentialsFile = path.join(home, "credentials.json");
const envNames = ["GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_ACCESS_TOKEN", "CLOUDSDK_AUTH_ACCESS_TOKEN", "GOOGLE_VERTEX_REFRESH_SKEW_MS"];

type Reply = { status: number; body: unknown } | "network-error";
type Scenario = {
	name: string;
	env?: Record<string, string>;
	file?: unknown;
	userAdc?: unknown;
	// Replies in request order; a request past the list gets a network error.
	replies: Reply[];
	// Calls of getVertexAccessToken in a row, sharing the cache.
	calls?: number;
};
const token = (accessToken: string, expiresIn = 3599) => ({ status: 200, body: { access_token: accessToken, expires_in: expiresIn, token_type: "Bearer" } });
const scenarios: Scenario[] = [
	{ name: "env-access-token", env: { GOOGLE_CLOUD_ACCESS_TOKEN: "ya29.env-token" }, file: serviceAccount, replies: [] },
	{ name: "env-cloudsdk-access-token", env: { CLOUDSDK_AUTH_ACCESS_TOKEN: "ya29.cloudsdk-token" }, replies: [] },
	{ name: "service-account", file: serviceAccount, replies: [token("ya29.sa-token")] },
	{ name: "service-account-no-key-id", file: serviceAccountNoKid, replies: [token("ya29.sa-token")] },
	{ name: "service-account-cached", file: serviceAccount, replies: [token("ya29.sa-token")], calls: 2 },
	{ name: "service-account-within-skew-refetches", file: serviceAccount, replies: [token("ya29.short", 30), token("ya29.second")], calls: 2 },
	{ name: "service-account-custom-skew", env: { GOOGLE_VERTEX_REFRESH_SKEW_MS: "10000" }, file: serviceAccount, replies: [token("ya29.short", 30)], calls: 2 },
	{ name: "service-account-exchange-fails", file: serviceAccount, replies: [{ status: 400, body: { error: "invalid_grant", error_description: "Invalid JWT Signature." } }] },
	{ name: "authorized-user", file: authorizedUser, replies: [token("ya29.user-token")] },
	{ name: "authorized-user-exchange-fails", file: authorizedUser, replies: [{ status: 401, body: { error: "invalid_client" } }] },
	{ name: "impersonated-from-service-account", file: impersonated(serviceAccount), replies: [token("ya29.source"), { status: 200, body: { accessToken: "ya29.impersonated", expireTime: "2026-11-05T13:00:00Z" } }] },
	{
		name: "impersonated-from-user-with-delegates",
		file: impersonated(authorizedUser, ["projects/-/serviceAccounts/delegate@fixture-project.iam.gserviceaccount.com"]),
		replies: [token("ya29.source"), { status: 200, body: { accessToken: "ya29.impersonated", expireTime: "2026-11-05T12:30:00Z" } }],
	},
	{ name: "impersonated-exchange-fails", file: impersonated(serviceAccount), replies: [token("ya29.source"), { status: 403, body: { error: { code: 403, message: "Permission denied." } } }] },
	{ name: "user-adc", userAdc: authorizedUser, replies: [token("ya29.user-adc")] },
	{ name: "credentials-file-beats-user-adc", file: serviceAccount, userAdc: authorizedUser, replies: [token("ya29.sa-token")] },
	{ name: "credentials-file-missing", env: { GOOGLE_APPLICATION_CREDENTIALS: path.join(home, "missing.json") }, replies: [] },
	{ name: "metadata", replies: [token("ya29.metadata")] },
	{ name: "metadata-not-ok", replies: [{ status: 404, body: "Not Found" }] },
	{ name: "no-credentials", replies: ["network-error"] },
];

const results: any[] = [];
const saved = Object.fromEntries(envNames.map(name => [name, process.env[name]]));
try {
	for (const scenario of scenarios) {
		__resetVertexTokenCache();
		for (const name of envNames) delete process.env[name];
		fs.rmSync(userAdc, { force: true });
		fs.rmSync(credentialsFile, { force: true });
		if (scenario.userAdc) {
			fs.mkdirSync(path.dirname(userAdc), { recursive: true });
			fs.writeFileSync(userAdc, JSON.stringify(scenario.userAdc));
		}
		if (scenario.file) {
			fs.writeFileSync(credentialsFile, JSON.stringify(scenario.file));
			process.env.GOOGLE_APPLICATION_CREDENTIALS = credentialsFile;
		}
		Object.assign(process.env, scenario.env ?? {});
		const replies = [...scenario.replies];
		const requests: any[] = [];
		const fetchImpl = (async (input: any, init: any) => {
			const url = input instanceof Request ? input.url : String(input);
			requests.push({ method: init?.method ?? "GET", url, headers: Object.fromEntries(new Headers(init?.headers)), body: typeof init?.body === "string" ? init.body : "" });
			const reply = replies.shift();
			if (!reply || reply === "network-error") throw new TypeError("fetch failed");
			const body = typeof reply.body === "string" ? reply.body : JSON.stringify(reply.body);
			return new Response(body, { status: reply.status, headers: { "content-type": "application/json" } });
		}) as typeof fetch;
		const tokens: string[] = [];
		let error: string | undefined;
		try {
			for (let i = 0; i < (scenario.calls ?? 1); i++) tokens.push(await getVertexAccessToken({ fetch: fetchImpl }));
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		}
		results.push({
			name: scenario.name,
			env: Object.fromEntries(Object.entries(scenario.env ?? {}).map(([name, value]) => [name, value.replaceAll(home, "<home>")])),
			file: scenario.file ?? null,
			userAdc: scenario.userAdc ?? null,
			replies: scenario.replies,
			calls: scenario.calls ?? 1,
			requests,
			tokens,
			...(error !== undefined ? { error: error.replaceAll(home, "<home>") } : {}),
		});
	}
	console.log(JSON.stringify({ source: "NeoPi OWN pi-ai providers/google-auth getVertexAccessToken", now, results }, null, 2));
} finally {
	for (const [name, value] of Object.entries(saved)) {
		if (value === undefined) delete process.env[name];
		else process.env[name] = value;
	}
	fs.rmSync(home, { recursive: true, force: true });
	fs.rmSync(userAdc, { force: true });
}
