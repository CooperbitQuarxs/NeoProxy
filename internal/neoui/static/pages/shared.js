import { h, btn } from "../ui.js";

export function pageHead(title, description, actions) {
	return h(
		"div",
		{ class: "page-head" },
		h("div", null, h("h1", { class: "page-title" }, title), description ? h("p", { class: "page-description" }, description) : null),
		actions ? h("div", { class: "page-actions" }, actions) : null,
	);
}

export function crumb(label, href) {
	return h("a", { class: "crumb", href }, "← ", label);
}

// Provider catalogue. NeoPi is the centrepiece; everything else is the
// engine's own upstream support, listed under "also supported".
export const PROVIDERS = {
	neopi: { label: "NeoPi", kind: "core", blurb: "The Neo core. Holds every credential and serves inference and auth to NeoProxy, NeoDeck and NeoCode." },
	codex: { label: "Codex", kind: "oauth", blurb: "OpenAI through a Codex login or API keys." },
	claude: { label: "Claude", kind: "oauth", blurb: "Anthropic through a Claude login or API keys." },
	gemini: { label: "Gemini", kind: "oauth", blurb: "Google AI Studio keys and Gemini CLI logins." },
	antigravity: { label: "Antigravity", kind: "oauth", blurb: "Google Antigravity accounts." },
	vertex: { label: "Vertex AI", kind: "key", blurb: "Google Cloud service accounts and API keys." },
	xai: { label: "xAI", kind: "oauth", blurb: "Grok through an xAI login or API keys." },
	meta: { label: "Meta", kind: "oauth", blurb: "Muse through a Meta login or Model API keys." },
	kimi: { label: "Kimi", kind: "oauth", blurb: "Moonshot Kimi Code accounts." },
	"kimi-ai": { label: "Kimi (global)", kind: "oauth", blurb: "Moonshot Kimi global accounts." },
	devin: { label: "Devin", kind: "oauth", blurb: "Cognition Devin accounts." },
	"openai-compatibility": { label: "OpenAI-compatible", kind: "key", blurb: "Any endpoint that speaks the OpenAI API." },
	"gemini-cli": { label: "Gemini CLI", kind: "oauth", blurb: "Gemini CLI OAuth logins." },
	qwen: { label: "Qwen", kind: "oauth", blurb: "Alibaba Qwen Code accounts." },
	iflow: { label: "iFlow", kind: "oauth", blurb: "iFlow accounts." },
};

export function providerLabel(id) {
	if (!id) return "unknown";
	return PROVIDERS[id]?.label ?? id;
}

export function statusKind(entry) {
	if (entry.disabled) return "muted";
	if (entry.unavailable || entry.status === "error" || entry.status === "invalid") return "bad";
	if (entry.status === "cooling" || entry.status === "cooldown" || entry.next_retry_after) return "warn";
	return "ok";
}

export function statusText(entry) {
	if (entry.disabled) return "disabled";
	if (entry.unavailable) return "unavailable";
	if (entry.status) return entry.status;
	return "active";
}

export function legacyLink(label = "Open in the legacy console") {
	return btn(label, { icon: "external", cls: "sm", href: "/management.html" });
}

export function sumBuckets(entries) {
	const byTime = new Map();
	for (const e of entries) {
		for (const b of e.recent_requests ?? []) {
			const cur = byTime.get(b.time) ?? { time: b.time, success: 0, failed: 0 };
			cur.success += b.success ?? 0;
			cur.failed += b.failed ?? 0;
			byTime.set(b.time, cur);
		}
	}
	return [...byTime.values()];
}

export function groupBy(items, keyFn) {
	const out = new Map();
	for (const it of items) {
		const k = keyFn(it);
		if (!out.has(k)) out.set(k, []);
		out.get(k).push(it);
	}
	return out;
}
