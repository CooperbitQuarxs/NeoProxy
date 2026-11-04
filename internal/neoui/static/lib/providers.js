// Presentation layer over the vault. The routing API (`/vault`) identifies
// providers by engine ids such as `openai-compatible-nvidia` and accounts by
// opaque ids; the access API (`/access`, `/access/providers`) knows the human
// side: display names, vendors, hues, emails, plans, health and recent usage.
// `enrichVault` joins the two so every page can speak about "NVIDIA NIM" and
// "eternal@…" instead of ids, and about one meter per account.

const FALLBACK = {
	codex: { name: "ChatGPT / Codex", vendor: "OpenAI", hue: 165 },
	claude: { name: "Claude", vendor: "Anthropic", hue: 45 },
	gemini: { name: "Gemini", vendor: "Google", hue: 255 },
	xai: { name: "Grok / xAI", vendor: "xAI", hue: 0 },
	meta: { name: "Meta AI", vendor: "Meta", hue: 280 },
	openrouter: { name: "OpenRouter", vendor: "OpenRouter", hue: 300 },
	cerebras: { name: "Cerebras", vendor: "Cerebras", hue: 95 },
	zai: { name: "Z.ai", vendor: "Zhipu AI", hue: 190 },
	nvidia: { name: "NVIDIA NIM", vendor: "NVIDIA", hue: 135 },
	huggingface: { name: "Hugging Face", vendor: "Hugging Face", hue: 85 },
	cursor: { name: "Cursor", vendor: "Anysphere", hue: 75 },
	custom: { name: "OpenAI-compatible server", vendor: "Any", hue: 0 },
};

// `openai-compatible-nvidia` → `nvidia`; everything else is already canonical.
export function canonicalProviderId(engineId = "") {
	return engineId.replace(/^openai-compatible-/, "");
}

export function providerHueStyle(hue, chroma = 0.13) {
	return { "--p-h": String(hue ?? 0), "--p-c": String(chroma) };
}

function titleCase(id) {
	return id.split(/[-_]/).filter(Boolean).map((w) => w[0].toUpperCase() + w.slice(1)).join(" ");
}

// Catalog lookup that never returns nothing: unknown providers get a readable
// name from their id and a hue derived from it, so the UI stays colourful and
// consistent between reloads.
export function providerInfo(catalog, engineId) {
	const cid = canonicalProviderId(engineId);
	const hit = catalog?.find((p) => p.id === cid) ?? catalog?.find((p) => p.id === engineId);
	if (hit) return { ...hit, engineId, canonicalId: cid };
	const fb = FALLBACK[cid];
	if (fb) return { id: cid, canonicalId: cid, engineId, chroma: 0.13, methods: [], plans: [], ...fb };
	let hash = 0;
	for (const ch of cid) hash = (hash * 31 + ch.charCodeAt(0)) >>> 0;
	return { id: cid, canonicalId: cid, engineId, name: titleCase(cid), vendor: "", hue: hash % 360, chroma: 0.11, methods: [], plans: [] };
}

// The one meter the dashboard talks about for an account: provider-reported
// windows win, configured local token meters come next, and "no meter" means
// the account is always available to the stages that use it.
// Exactly the router's rules (internal/neoroute/resolve.go windowFor): a
// provider reading counts while known and not yet reset; a local meter needs
// a positive capacity and a window that has started and not yet reset.
const providerCurrent = (w, now) => w.known && (!w.resetsAt || w.resetsAt > now);
const localCurrent = (w, now) => w.known && w.capacity > 0 && w.startsAt <= now && w.resetsAt > now;
export function meterFor(account, kind, now = Date.now()) {
	const live = (account.providerWindows ?? []).find((w) => w.kind === kind && providerCurrent(w, now));
	if (live) return { ...live, source: "provider" };
	const local = (account.windows ?? []).find((w) => w.kind === kind && localCurrent(w, now));
	if (local) return { ...local, source: "local" };
	return null;
}

export function meters(account) {
	const out = [];
	for (const kind of ["5h", "day", "week", "month"]) {
		const m = meterFor(account, kind);
		if (m) out.push(m);
	}
	return out;
}

// Health in words a teammate understands, with the worst signal first.
export function accountHealth(account) {
	const face = account.face;
	if (account.state === "expired" || face?.health === "expired") return { kind: "bad", text: "Sign-in expired", hint: face?.healthMessage || "Sign in again or refresh the credential." };
	if (account.state === "cooldown") return { kind: "warn", text: "Cooling down", hint: "The provider asked us to back off. Routing resumes automatically." };
	if (face?.health === "disabled") return { kind: "muted", text: "Disabled", hint: "Turned off by an admin. No stage draws from it." };
	if (face?.health && face.health !== "healthy") return { kind: "warn", text: face.healthMessage || titleCase(face.health), hint: face.healthMessage };
	if (face?.lastProbe && face.lastProbe.ok === false) return { kind: "warn", text: "Last test failed", hint: face.lastProbe.error };
	const hot = meters(account).find((m) => m.used >= 1);
	if (hot) return { kind: "warn", text: `${hot.kind} window spent`, hint: `Resets ${new Date(hot.resetsAt).toLocaleTimeString()}.` };
	return { kind: "ok", text: "Healthy", hint: face?.usage?.requests24h ? `${face.usage.requests24h} requests in the last day` : "No requests in the last day" };
}

// Mutates and returns the vault so existing pages keep their shapes.
export function enrichVault(vault, access, catalog) {
	const byId = new Map((access?.items ?? []).map((i) => [i.id, i]));
	for (const p of vault.providers) {
		const info = providerInfo(catalog, p.id);
		p.engineId = p.id;
		p.canonicalId = info.canonicalId;
		p.name = info.name;
		p.vendor = info.vendor || p.vendor;
		p.hue = info.hue;
		p.chroma = info.chroma;
		p.methods = info.methods;
		p.catalog = info;
	}
	for (const a of vault.accounts) {
		const face = byId.get(a.id);
		a.face = face ?? null;
		if (face) {
			a.label = face.label || a.label;
			a.plan = face.plan?.name || a.plan;
			a.kind = face.kind;
			a.health = face.health;
			a.usage = face.usage;
			if (!a.providerWindows?.length && face.providerWindows?.length) a.providerWindows = face.providerWindows;
		}
		a.meters = meters(a);
	}
	vault.catalog = catalog ?? [];
	vault.people = access?.people ?? [];
	vault.totals = access?.totals ?? null;
	return vault;
}
