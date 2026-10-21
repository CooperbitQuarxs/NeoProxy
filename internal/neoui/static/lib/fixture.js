// The fixture world: an in-memory NeoProxy whose NeoPi core is linked, with a
// vault of providers and accounts, a catalogue of served models, virtual
// models with demotion chains, users with grants, and a request stream. Every
// shape here is the contract in docs/design/backend-contracts.md. Nothing in
// this file touches the engine; it exists so the dashboard is fully usable
// before the backend lands, and so screenshots never carry real data.

const HOUR = 3600_000;
const now = () => Date.now();

// Deterministic pseudo-random so the stream looks the same on every load.
function rng(seed) {
	let s = seed >>> 0;
	return () => {
		s = (s * 1664525 + 1013904223) >>> 0;
		return s / 2 ** 32;
	};
}

export function createFixture() {
	const t0 = now();
	const rand = rng(28);
	const pick = (arr) => arr[Math.floor(rand() * arr.length)];

	const core = {
		linked: true,
		endpoint: "unix:///run/user/1000/npi/core.sock",
		identity: "neoproxy@utopia",
		version: "18.10.3-nightly",
		heartbeatAt: t0 - 4_000,
		scopes: ["vault:read", "usage:read", "config:write"],
		host: "utopia",
	};

	const providers = [
		{ id: "codex", name: "Codex", vendor: "OpenAI", kind: "oauth", exposed: true },
		{ id: "anthropic", name: "Anthropic", vendor: "Anthropic", kind: "oauth", exposed: true },
		{ id: "xai", name: "xAI", vendor: "xAI", kind: "api-key", exposed: true },
		{ id: "zai", name: "Z.ai", vendor: "Zhipu", kind: "api-key", exposed: true },
		{ id: "cerebras", name: "Cerebras", vendor: "Cerebras", kind: "api-key", exposed: true },
		{ id: "github", name: "GitHub Copilot", vendor: "GitHub", kind: "oauth", exposed: false },
		{ id: "gemini", name: "Gemini", vendor: "Google", kind: "oauth", exposed: true },
	];

	const win = (kind, used, hoursToReset) => ({ kind, used, resetsAt: t0 + hoursToReset * HOUR });
	const accounts = [
		{ id: "codex-a", providerId: "codex", label: "shayna@…", plan: "Pro", state: "healthy", windows: [win("5h", 0.62, 2.1), win("week", 0.49, 61)] },
		{ id: "codex-b", providerId: "codex", label: "office-1@…", plan: "Pro", state: "healthy", windows: [win("5h", 0.18, 3.4), win("week", 0.31, 88)] },
		{ id: "codex-c", providerId: "codex", label: "office-2@…", plan: "Plus", state: "cooldown", cooldownUntil: t0 + 0.4 * HOUR, windows: [win("5h", 1, 0.4), win("week", 0.77, 40)] },
		{ id: "anthropic-a", providerId: "anthropic", label: "shayna@…", plan: "Max 20×", state: "healthy", windows: [win("5h", 0.21, 4.2), win("week", 0.45, 70)] },
		{ id: "anthropic-b", providerId: "anthropic", label: "office@…", plan: "Max 5×", state: "healthy", windows: [win("5h", 0.08, 4.8), win("week", 0.12, 120)] },
		{ id: "xai-a", providerId: "xai", label: "key …7f3a", plan: "Pay as you go", state: "healthy", windows: [win("month", 0.17, 240)] },
		{ id: "zai-a", providerId: "zai", label: "key …c01d", plan: "Coding plan", state: "healthy", windows: [win("5h", 0.05, 4.9), win("month", 0.22, 300)] },
		{ id: "cerebras-a", providerId: "cerebras", label: "key …91be", plan: "Developer", state: "healthy", windows: [win("day", 0.33, 9)] },
		{ id: "github-a", providerId: "github", label: "PsychedelicShayna", plan: "Pro+", state: "expired", windows: [win("month", 0.9, 200)] },
		{ id: "gemini-a", providerId: "gemini", label: "shayna@…", plan: "AI Pro", state: "healthy", windows: [win("day", 0.4, 11)] },
	];

	const m = (providerId, id, display, tags, context, exposed = true) => ({
		id: `${providerId}/${id}`,
		providerId,
		name: id,
		display,
		served: `neoproxy/${providerId}/${id}`,
		tags,
		context,
		exposed,
	});
	const models = [
		m("codex", "gpt-6-astra", "GPT-6 Astra", ["reasoning", "intent"], 400_000),
		m("codex", "gpt-6.1-sol", "GPT-6.1 Sol", ["reasoning", "code"], 400_000),
		m("codex", "gpt-6-luna", "GPT-6 Luna", ["fast", "code"], 272_000),
		m("codex", "gpt-5.6-luna", "GPT-5.6 Luna", ["fast"], 272_000, false),
		m("codex", "gpt-live-1.1", "GPT Live 1.1", ["voice", "realtime"], 128_000),
		m("anthropic", "claude-fable-5-1", "Claude Fable 5.1", ["reasoning", "design", "orchestration"], 1_000_000),
		m("anthropic", "claude-opus-5-5", "Claude Opus 5.5", ["reasoning", "code"], 1_000_000),
		m("anthropic", "claude-sonnet-5-5", "Claude Sonnet 5.5", ["code", "fast"], 1_000_000),
		m("anthropic", "claude-haiku-5-5", "Claude Haiku 5.5", ["fast"], 200_000),
		m("xai", "grok-4.5", "Grok 4.5", ["reasoning", "search"], 1_000_000),
		m("xai", "grok-4.5-fast", "Grok 4.5 Fast", ["fast", "search"], 2_000_000),
		m("xai", "grok-stt", "Grok STT", ["speech"], 0),
		m("zai", "glm-5", "GLM-5", ["code"], 200_000),
		m("cerebras", "qwen3-coder-480b", "Qwen3 Coder 480B", ["fast", "code"], 131_000),
		m("cerebras", "llama-4-maverick", "Llama 4 Maverick", ["fast"], 131_000, false),
		m("github", "copilot-gpt-5", "Copilot GPT-5", ["code"], 128_000, false),
		m("gemini", "gemini-3-pro", "Gemini 3 Pro", ["reasoning", "vision"], 2_000_000),
		m("gemini", "gemini-3-flash", "Gemini 3 Flash", ["fast", "vision"], 1_000_000),
	];

	// Stage budgets are fractions of an account's quota window. `perAccount` is
	// how much of each selected account this stage may draw; `cap` is the total
	// across the stage's accounts. `per: "user"` scopes the budget to each
	// granted user; `per: "all"` shares one budget across everyone granted.
	const stage = (id, model, opts = {}) => ({
		id,
		model,
		accounts: opts.accounts ?? "all",
		draw: opts.draw ?? "alternate",
		perAccount: opts.perAccount ?? 0.05,
		cap: opts.cap ?? 0.2,
		window: opts.window ?? "week",
		per: opts.per ?? "user",
		effort: opts.effort ?? null,
	});
	const virtualModels = [
		{
			id: "vm-heavy",
			name: "heavy",
			served: "neoproxy/heavy",
			description: "The biggest brain a user is allowed. Drawn from the Astra pool in small sips, then Sol, then a limit.",
			stages: [
				stage("s1", "codex/gpt-6-astra", { accounts: ["codex-a", "codex-b"], draw: "alternate", perAccount: 0.05, cap: 0.2, window: "week" }),
				stage("s2", "codex/gpt-6.1-sol", { accounts: "all", draw: "least-used", perAccount: 0.1, cap: 0.2, window: "week" }),
			],
			terminal: { kind: "limit", message: "Your heavy allotment for this week is used up. It resets with the Codex weekly window." },
			updatedAt: t0 - 26 * HOUR,
			updatedBy: "shayna",
		},
		{
			id: "vm-default",
			name: "default",
			served: "neoproxy/default",
			description: "The everyday model. Sonnet across both Anthropic accounts, falling back to Luna when the 5-hour windows are hot.",
			stages: [
				stage("s1", "anthropic/claude-sonnet-5-5", { accounts: "all", draw: "least-used", perAccount: 0.15, cap: 0.3, window: "5h" }),
				stage("s2", "codex/gpt-6-luna", { accounts: "all", draw: "alternate", perAccount: 0.1, cap: 0.2, window: "5h" }),
			],
			terminal: { kind: "queue", message: "Everyone's default allotment is busy; your request waits for the next window." },
			updatedAt: t0 - 3 * 24 * HOUR,
			updatedBy: "shayna",
		},
		{
			id: "vm-small",
			name: "small",
			served: "neoproxy/small",
			description: "Cheap and quick: Haiku, then Grok Fast, then Qwen on Cerebras. Never limits; it just keeps getting cheaper.",
			stages: [
				stage("s1", "anthropic/claude-haiku-5-5", { accounts: "all", draw: "least-used", perAccount: 0.05, cap: 0.1, window: "5h" }),
				stage("s2", "xai/grok-4.5-fast", { accounts: "all", draw: "fill", perAccount: 0.1, cap: 0.1, window: "month" }),
				stage("s3", "cerebras/qwen3-coder-480b", { accounts: "all", draw: "fill", perAccount: 1, cap: 1, window: "day", per: "all" }),
			],
			terminal: { kind: "limit", message: "All small-model pools are exhausted for today." },
			updatedAt: t0 - 9 * 24 * HOUR,
			updatedBy: "shayna",
		},
		{
			id: "vm-review",
			name: "review",
			served: "neoproxy/review",
			description: "Code review and judgement calls. Opus first, Fable when Opus is spent, then stop.",
			stages: [
				stage("s1", "anthropic/claude-opus-5-5", { accounts: ["anthropic-a"], draw: "fill", perAccount: 0.1, cap: 0.1, window: "week" }),
				stage("s2", "anthropic/claude-fable-5-1", { accounts: ["anthropic-a"], draw: "fill", perAccount: 0.05, cap: 0.05, window: "week", effort: "high" }),
			],
			terminal: { kind: "limit", message: "Review allotment used for this week." },
			updatedAt: t0 - 40 * HOUR,
			updatedBy: "shayna",
		},
		{
			id: "vm-voice",
			name: "voice",
			served: "neoproxy/voice",
			description: "Dictation and live mode for the office. Grok STT for transcription, GPT Live for conversation.",
			stages: [
				stage("s1", "xai/grok-stt", { accounts: "all", draw: "fill", perAccount: 0.3, cap: 0.3, window: "month", per: "all" }),
				stage("s2", "codex/gpt-live-1.1", { accounts: ["codex-b"], draw: "fill", perAccount: 0.2, cap: 0.2, window: "week", per: "all" }),
			],
			terminal: { kind: "limit", message: "Voice budget for the office is used up this month." },
			updatedAt: t0 - 6 * HOUR,
			updatedBy: "saturn",
		},
	];

	const key = (id, label, hoursAgo, lastHoursAgo) => ({
		id,
		prefix: `nk_${id.slice(0, 4)}…${id.slice(-4)}`,
		label,
		createdAt: t0 - hoursAgo * HOUR,
		lastUsedAt: lastHoursAgo === null ? null : t0 - lastHoursAgo * HOUR,
	});
	const users = [
		{
			id: "u-shayna",
			handle: "shayna",
			name: "Shayna Kömmerling",
			role: "admin",
			state: "active",
			keys: [key("7c1e4d20a9f1", "npi on Voyager", 400, 0.02), key("b31a90ee42cc", "NeoCode", 300, 1.3)],
			providerAccess: ["codex", "anthropic", "xai", "zai", "cerebras", "gemini", "github"],
			grants: [
				{ virtualModel: "vm-heavy", allotment: 1 },
				{ virtualModel: "vm-default", allotment: 1 },
				{ virtualModel: "vm-small", allotment: 1 },
				{ virtualModel: "vm-review", allotment: 1 },
				{ virtualModel: "vm-voice", allotment: 1 },
			],
			limits: { rpm: null, tokensPerDay: null },
			createdAt: t0 - 60 * 24 * HOUR,
		},
		{
			id: "u-santiago",
			handle: "santiago",
			name: "Santiago",
			role: "member",
			state: "active",
			keys: [key("e0a7b2c91d44", "NeoDeck", 200, 0.1)],
			providerAccess: [],
			grants: [
				{ virtualModel: "vm-default", allotment: 1 },
				{ virtualModel: "vm-small", allotment: 1 },
				{ virtualModel: "vm-heavy", allotment: 0.5 },
			],
			limits: { rpm: 60, tokensPerDay: 4_000_000 },
			createdAt: t0 - 20 * 24 * HOUR,
		},
		{
			id: "u-voyager",
			handle: "voyager",
			name: "NeoDeck on Utopia",
			role: "service",
			state: "active",
			keys: [key("55d8f1e3aa07", "neodeck.service", 48, 0.01)],
			providerAccess: [],
			grants: [
				{ virtualModel: "vm-default", allotment: 1 },
				{ virtualModel: "vm-small", allotment: 1 },
				{ virtualModel: "vm-review", allotment: 1 },
				{ virtualModel: "vm-voice", allotment: 1 },
			],
			limits: { rpm: 240, tokensPerDay: null },
			createdAt: t0 - 2 * 24 * HOUR,
		},
		{
			id: "u-lena",
			handle: "lena",
			name: "Lena",
			role: "member",
			state: "invited",
			keys: [],
			providerAccess: [],
			grants: [{ virtualModel: "vm-small", allotment: 1 }],
			limits: { rpm: 30, tokensPerDay: 1_000_000 },
			createdAt: t0 - 5 * HOUR,
		},
		{
			id: "u-ci",
			handle: "ci-bot",
			name: "Ferris wheel CI",
			role: "service",
			state: "suspended",
			keys: [key("9a2c7e55b1d0", "github-actions", 500, 70)],
			providerAccess: [],
			grants: [{ virtualModel: "vm-small", allotment: 0.25 }],
			limits: { rpm: 20, tokensPerDay: 500_000 },
			createdAt: t0 - 45 * 24 * HOUR,
		},
	];

	// usage[userId][vmId][stageId][accountId] = fraction of that account's
	// window the (user, stage) pair has drawn. The simulator reads this.
	const usage = {};
	const bump = (u, vm, s, a, v) => {
		usage[u] ??= {};
		usage[u][vm] ??= {};
		usage[u][vm][s] ??= {};
		usage[u][vm][s][a] = (usage[u][vm][s][a] ?? 0) + v;
	};
	bump("u-shayna", "vm-heavy", "s1", "codex-a", 0.05);
	bump("u-shayna", "vm-heavy", "s1", "codex-b", 0.041);
	bump("u-shayna", "vm-heavy", "s2", "codex-b", 0.063);
	bump("u-shayna", "vm-default", "s1", "anthropic-a", 0.09);
	bump("u-shayna", "vm-default", "s1", "anthropic-b", 0.03);
	bump("u-shayna", "vm-review", "s1", "anthropic-a", 0.1);
	bump("u-shayna", "vm-review", "s2", "anthropic-a", 0.012);
	bump("u-santiago", "vm-heavy", "s1", "codex-a", 0.026);
	bump("u-santiago", "vm-heavy", "s1", "codex-b", 0.025);
	bump("u-santiago", "vm-heavy", "s2", "codex-a", 0.05);
	bump("u-santiago", "vm-heavy", "s2", "codex-b", 0.05);
	bump("u-santiago", "vm-default", "s1", "anthropic-a", 0.04);
	bump("u-santiago", "vm-default", "s1", "anthropic-b", 0.05);
	bump("u-santiago", "vm-small", "s1", "anthropic-b", 0.02);
	bump("u-voyager", "vm-default", "s1", "anthropic-b", 0.06);
	bump("u-voyager", "vm-default", "s2", "codex-a", 0.02);
	bump("u-voyager", "vm-small", "s1", "anthropic-b", 0.05);
	bump("u-voyager", "vm-small", "s2", "xai-a", 0.1);
	bump("u-voyager", "vm-small", "s3", "cerebras-a", 0.33);
	bump("u-voyager", "vm-review", "s1", "anthropic-a", 0.03);
	bump("u-voyager", "vm-voice", "s1", "xai-a", 0.07);
	bump("u-ci", "vm-small", "s1", "anthropic-b", 0.025);

	const clients = ["npi", "NeoDeck", "NeoCode", "curl", "github-actions"];
	const requests = [];
	const askers = [
		["u-shayna", "neoproxy/heavy"],
		["u-shayna", "neoproxy/default"],
		["u-shayna", "neoproxy/review"],
		["u-shayna", "neoproxy/anthropic/claude-fable-5-1"],
		["u-santiago", "neoproxy/default"],
		["u-santiago", "neoproxy/small"],
		["u-santiago", "neoproxy/heavy"],
		["u-voyager", "neoproxy/default"],
		["u-voyager", "neoproxy/small"],
		["u-voyager", "neoproxy/voice"],
		["u-voyager", "neoproxy/review"],
	];
	let t = t0 - 20_000;
	for (let i = 0; i < 160; i++) {
		const [userId, asked] = pick(askers);
		const user = users.find((u) => u.id === userId);
		const decision = resolve({ asked, user }, { models, virtualModels, accounts, usage, providers });
		const tin = Math.round(800 + rand() * 40_000);
		const tout = Math.round(80 + rand() * 6_000);
		requests.push({
			id: `req_${(0x3f00 + i).toString(16)}`,
			at: t,
			userId,
			keyId: user.keys[0]?.id ?? null,
			client: user.role === "service" ? "NeoDeck" : pick(clients),
			asked,
			decision,
			tokens: decision.outcome === "ok" ? { in: tin, out: tout, cached: Math.round(tin * rand() * 0.7) } : { in: 0, out: 0, cached: 0 },
			latencyMs: decision.outcome === "ok" ? Math.round(400 + rand() * 9000) : Math.round(20 + rand() * 80),
			outcome: decision.outcome,
			stream: rand() > 0.3,
		});
		t -= Math.round(5_000 + rand() * 120_000);
	}
	requests.sort((a, b) => b.at - a.at);

	const localCredentials = [
		{ id: "cred-1", provider: "codex", label: "office-2@… (legacy auth file)", state: "disabled", source: "auths/codex-office-2.json", updatedAt: t0 - 30 * 24 * HOUR },
	];

	return { core, providers, accounts, models, virtualModels, users, usage, requests, localCredentials };
}

// Resolve which stage and account would serve a request right now. This is the
// same algorithm the backend is expected to implement (backend-contracts.md
// › Resolution); the dashboard runs it locally for the simulator and the
// fixture stream.
export function resolve({ asked, user, at = now() }, world) {
	const { models, virtualModels, accounts, usage } = world;
	const trail = [];
	const bare = asked.replace(/^neoproxy\//, "");
	const vm = virtualModels.find((v) => v.name === bare);
	if (!vm) {
		const model = models.find((x) => x.id === bare);
		if (!model) return { outcome: "error", reason: "unknown-model", trail: [{ kind: "no-model", asked }] };
		if (!user.providerAccess.includes(model.providerId)) {
			return { outcome: "error", reason: "no-provider-access", model: model.id, trail: [{ kind: "denied", providerId: model.providerId }] };
		}
		const acc = accounts.filter((a) => a.providerId === model.providerId && a.state === "healthy")[0];
		if (!acc) return { outcome: "error", reason: "no-healthy-account", model: model.id, trail: [{ kind: "no-account", providerId: model.providerId }] };
		return { outcome: "ok", direct: true, model: model.id, accountId: acc.id, trail: [{ kind: "direct", model: model.id, accountId: acc.id }] };
	}
	const grant = user.grants.find((g) => g.virtualModel === vm.id);
	if (!grant) return { outcome: "error", reason: "not-granted", virtualModel: vm.id, trail: [{ kind: "denied", virtualModel: vm.id }] };
	const allot = grant.allotment ?? 1;
	for (let i = 0; i < vm.stages.length; i++) {
		const s = vm.stages[i];
		const model = models.find((x) => x.id === s.model);
		const pool = accounts.filter((a) => a.providerId === model.providerId && (s.accounts === "all" || s.accounts.includes(a.id)));
		const used = (a) => (s.per === "user" ? (usage[user.id]?.[vm.id]?.[s.id]?.[a.id] ?? 0) : sumAll(usage, vm.id, s.id, a.id));
		const perBudget = s.perAccount * allot;
		const capBudget = s.cap * allot;
		const total = pool.reduce((n, a) => n + used(a), 0);
		const step = { kind: "stage", index: i, stageId: s.id, model: s.model, total, cap: capBudget, candidates: [] };
		trail.push(step);
		if (total >= capBudget) {
			step.skipped = "cap";
			continue;
		}
		let candidates = pool.filter((a) => a.state === "healthy" && used(a) < perBudget);
		step.candidates = candidates.map((a) => ({ accountId: a.id, used: used(a), budget: perBudget }));
		if (candidates.length === 0) {
			step.skipped = pool.some((a) => a.state !== "healthy") ? "unhealthy" : "per-account";
			continue;
		}
		let chosen;
		if (s.draw === "fill") chosen = candidates[0];
		else if (s.draw === "least-used") chosen = candidates.slice().sort((a, b) => used(a) - used(b))[0];
		else {
			// alternate: rotate by request count so consecutive calls move between accounts
			const n = Math.floor(at / 1000) % candidates.length;
			chosen = candidates[n];
		}
		step.chosen = chosen.id;
		return { outcome: "ok", virtualModel: vm.id, stageIndex: i, stageId: s.id, model: s.model, accountId: chosen.id, effort: s.effort, trail };
	}
	trail.push({ kind: "terminal", terminal: vm.terminal });
	return { outcome: vm.terminal.kind === "queue" ? "queued" : "limited", virtualModel: vm.id, reason: vm.terminal.kind, message: vm.terminal.message, trail };
}

function sumAll(usage, vmId, stageId, accountId) {
	let n = 0;
	for (const u of Object.values(usage)) n += u?.[vmId]?.[stageId]?.[accountId] ?? 0;
	return n;
}
