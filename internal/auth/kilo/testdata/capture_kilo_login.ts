// Executes NeoPi's OWN loginKilo (registry/oauth/kilo.ts) against scripted
// replies through its fetch hook: no request leaves the machine, and the
// five-second poll pause is skipped. Records every request, the device code
// NeoPi shows, and the token or error it ends with.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_kilo_login.ts > kilo_login_capture.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { loginKilo } = await import(root + "/pi-ai/src/registry/oauth/kilo.ts");

(Bun as any).sleep = async () => {};

type Reply = { status: number; body?: unknown };
const code = { code: "ABCD-1234", verificationUrl: "https://app.kilo.ai/device?code=ABCD-1234", expiresIn: 600 };
const ok = (body: unknown): Reply => ({ status: 200, body });
const scenarios: { name: string; replies: Reply[] }[] = [
	{ name: "approved-after-pending", replies: [ok(code), { status: 202 }, ok({ status: "approved", token: "kilo-token-0001" })] },
	{ name: "pending-status-then-approved", replies: [ok(code), ok({ status: "pending" }), ok({ status: "approved", token: "kilo-token-0002" })] },
	{ name: "approved-without-token", replies: [ok(code), ok({ status: "approved" }), ok({ status: "approved", token: "kilo-token-0003" })] },
	{ name: "denied-403", replies: [ok(code), { status: 403 }] },
	{ name: "expired-410", replies: [ok(code), { status: 410 }] },
	{ name: "denied-status", replies: [ok(code), ok({ status: "denied" })] },
	{ name: "expired-status", replies: [ok(code), ok({ status: "expired" })] },
	{ name: "poll-500", replies: [ok(code), { status: 500, body: "boom" }] },
	{ name: "initiate-429", replies: [{ status: 429 }] },
	{ name: "initiate-500", replies: [{ status: 500 }] },
	{ name: "initiate-missing-fields", replies: [ok({ code: "ABCD-1234", verificationUrl: "" })] },
	{ name: "initiate-zero-expiry", replies: [ok({ ...code, expiresIn: 0 })] },
];

const results: any[] = [];
for (const scenario of scenarios) {
	const requests: any[] = [];
	let call = 0;
	const fetchImpl = async (input: any, init: any) => {
		const url = typeof input === "string" ? input : input.url;
		requests.push({ method: init?.method ?? "GET", url, headers: Object.fromEntries(new Headers(init?.headers)), body: init?.body ?? null });
		const reply = scenario.replies[Math.min(call++, scenario.replies.length - 1)];
		const body = reply.body === undefined ? null : typeof reply.body === "string" ? reply.body : JSON.stringify(reply.body);
		return new Response(body, { status: reply.status, headers: { "content-type": "application/json" } });
	};
	let shown: any;
	let result: any;
	try {
		const credentials = await loginKilo({ fetch: fetchImpl, onAuth: (info: any) => (shown = info) } as any);
		result = { token: credentials.access, refresh: credentials.refresh };
	} catch (error: any) {
		result = { error: String(error?.message ?? error), kind: error?.kind ?? null, status: error?.status ?? null };
	}
	results.push({ name: scenario.name, replies: scenario.replies, requests, shown: shown ?? null, result });
}
console.log(JSON.stringify({ source: "NeoPi OWN loginKilo", deviceAuthBaseUrl: "https://api.kilo.ai/api/device-auth", results }, null, 2));
