// Routing: how NeoProxy picks an account. Virtual models carry their own chain;
// direct provider models use the strategy here. Every setting saves as soon as
// it changes, like the Configuration page, and resting accounts can be woken.

import { h, icon, fmtInt, plural, debounce } from "../lib/dom.js";
import { engine, invalidate, ApiError } from "../lib/client.js";
import { pageHead, card, btn, table, empty, inspector, section, spinner, errorState, toast, confirm } from "../lib/components.js";

// Canonical channels in internal/registry/model_definitions.go:499-537.
const CHANNELS = ["claude", "gemini", "gemini-interactions", "vertex", "aistudio", "codex", "kimi", "antigravity", "xai", "devin", "meta"];
const CHANNEL_NAMES = { claude: "Claude", gemini: "Gemini", "gemini-interactions": "Gemini Interactions", vertex: "Vertex AI", aistudio: "AI Studio", codex: "ChatGPT / Codex", kimi: "Kimi", antigravity: "Antigravity", xai: "xAI / Grok", devin: "Devin", meta: "Meta AI" };
const channelName = (c) => CHANNEL_NAMES[c] ?? c;

// Who answers eight requests in a row, for three accounts (A has weight 2 under weighted).
const STRATEGIES = [
	{ value: "round-robin", label: "Round robin", note: "Take turns across the provider's healthy accounts. Spreads usage evenly.", seq: "ABCABCAB" },
	{ value: "weighted-round-robin", label: "Weighted", note: "Take turns, but an account with a bigger weight gets a bigger share.", seq: "AABCAABC" },
	{ value: "fill-first", label: "Fill first", note: "Use the first account until it can't serve, then the next. Keeps the others fresh.", seq: "AAAAA!BB" },
];

const SETTINGS = [
	{ title: "When a request fails", rows: [
		{ path: ["retry", "request-retry"], kind: "int", label: "Extra retry rounds", hint: "After every eligible account has failed once, start over this many times.", unit: "rounds", min: 0, zero: "No extra rounds" },
		{ path: ["retry", "max-retry-credentials"], kind: "int", label: "Accounts per round", hint: "How many different accounts one round may try.", unit: "accounts", min: 0, zero: "All of them" },
		{ path: ["retry", "max-retry-interval"], kind: "int", label: "Longest wait for a resting account", hint: "Before another round, wait at most this long for an account to come back.", unit: "seconds", min: 0, zero: "Don't wait" },
	] },
	{ title: "Resting accounts", rows: [
		{ path: ["cooldown", "disable-cooling"], kind: "bool", invert: true, label: "Rest an account after errors", hint: "After a rate limit or failure, skip that account for a while instead of trying it again at once." },
		{ path: ["cooldown", "transient-error-cooldown-seconds"], kind: "int", label: "Rest after brief provider errors", hint: "For timeouts and 5xx answers. 0 keeps the built-in rest; -1 turns it off.", unit: "seconds", min: -1, zero: "Built-in", negative: "Off" },
		{ path: ["cooldown", "save-cooldown-status"], kind: "bool", label: "Remember rests across restarts", hint: "A resting account stays resting after the engine restarts." },
	] },
	{ title: "Conversations and names", rows: [
		{ path: ["session-affinity"], kind: "bool", label: "Keep a conversation on one account", hint: "Provider-side prompt caching keeps working. If that account fails, the conversation moves." },
		{ path: ["force-model-prefix"], kind: "bool", label: "Require a prefix for prefixed accounts", hint: "Accounts with a prefix answer only requests that name it, such as team/kimi-k2." },
	] },
];

const getIn = (o, p) => p.reduce((v, k) => v?.[k], o);
const nest = (p, v) => p.reduceRight((acc, k) => ({ [k]: acc }), v);

function remaining(seconds) {
	if (seconds < 60) return `${fmtInt(Math.max(1, seconds))}s`;
	if (seconds < 3600) return `${Math.round(seconds / 60)} min`;
	const hrs = Math.floor(seconds / 3600);
	const min = Math.round((seconds % 3600) / 60);
	return min ? `${hrs} h ${min} min` : `${hrs} h`;
}

const REASONS = { quota: "Quota used up", rate_limit: "Rate limited", rate_limited: "Rate limited", transient: "Provider error", auth: "Sign-in problem", error: "Failed", model_not_supported: "Model not offered" };
const reasonText = (c) => REASONS[c.reason] ?? (c.reason ? c.reason.replace(/[_-]+/g, " ").replace(/^./, (x) => x.toUpperCase()) : "Resting");

export async function render(ctx) {
	const { root } = ctx;
	root.append(pageHead({
		title: "Routing",
		desc: "How NeoProxy picks the account that answers a request, and what it does when one fails. Changes save as you make them.",
	}));
	const host = h("div", { class: "rt" });
	const loading = spinner();
	root.append(host, loading);
	let config;
	try { config = await engine.config(); }
	catch (e) { loading.replaceWith(errorState(e, () => ctx.rerender())); return; }
	loading.remove();
	const routing = structuredClone(config.routing ?? {});
	let disposed = false;

	// Each change is its own small PATCH, so nothing waits behind a Save button.
	async function save(path, value) {
		await engine.patch("config", { routing: nest(path, value) });
		invalidate("config");
		let o = routing;
		for (const k of path.slice(0, -1)) o = o[k] ??= {};
		o[path.at(-1)] = value;
	}
	const stateEl = () => h("span", { class: "set-state", "aria-live": "polite" });
	function flash(el, ok, message) {
		el.className = `set-state ${ok ? "ok" : ""}`;
		el.replaceChildren(...(ok ? [icon("check", 13), "Saved"] : []));
		if (ok) setTimeout(() => { if (el.textContent === "Saved") { el.className = "set-state"; el.replaceChildren(); } }, 2400);
		else if (message) toast(message, "bad");
	}

	// ---------- the two kinds of names ----------

	host.append(card({
		title: "Two kinds of model names",
		sub: "The name an app asks for decides which rule picks the account.",
		cls: "rt-paths",
		body: h("div", { class: "rt-path-list" },
			h("div", { class: "rt-path" },
				h("code", { class: "rt-name" }, "neoproxy/", h("span", { class: "vm" }, "<name>")),
				icon("arrowRight", 15),
				h("div", { class: "rt-path-what" }, h("strong", null, "Virtual model"), h("span", null, "Its own chain of models decides, each with a draw mode such as most room left or take turns."), h("a", { class: "link-text", href: "#/virtual-models" }, "Virtual models", icon("arrowRight", 12))),
			),
			h("div", { class: "rt-path" },
				h("code", { class: "rt-name" }, "neoproxy/", h("span", { class: "prov" }, "<provider>"), "/<model>"),
				icon("arrowRight", 15),
				h("div", { class: "rt-path-what" }, h("strong", null, "Direct model"), h("span", null, "Goes straight to that provider. The strategy below picks among its accounts.")),
			),
		),
	}));

	// ---------- strategy ----------

	const stratState = stateEl();
	const name = "rt-strategy";
	let current = routing.strategy || "round-robin";
	const opts = h("div", { class: "rt-strats", role: "radiogroup", "aria-label": "Strategy for direct models" });
	for (const s of STRATEGIES) {
		const input = h("input", { type: "radio", name, value: s.value, checked: current === s.value });
		input.addEventListener("change", async () => {
			if (!input.checked) return;
			const before = current;
			current = s.value;
			stratState.className = "set-state busy";
			stratState.replaceChildren("Saving…");
			try { await save(["strategy"], s.value); flash(stratState, true); }
			catch (e) { current = before; for (const r of opts.querySelectorAll("input")) r.checked = r.value === before; flash(stratState, false, e.message); }
		});
		opts.append(h("label", { class: "rt-strat" },
			input,
			h("span", { class: "rt-strat-top" }, h("span", { class: "rt-strat-name" }, s.label), s.value === "round-robin" ? h("span", { class: "opt-tag" }, "default") : null),
			h("span", { class: "rt-strat-note" }, s.note),
			h("span", { class: "rt-seq", "aria-hidden": "true" }, [...s.seq].map((c) => (c === "!" ? h("i", { class: "rt-full", title: "A is used up" }, "×") : h("i", { class: `rt-a${c}` }, c)))),
		));
	}
	host.append(card({
		title: "Strategy for direct models",
		sub: "How a direct model picks among the provider's accounts.",
		body: [opts, h("div", { class: "rt-seq-legend" }, h("span", { class: "rt-seq" }, h("i", { class: "rt-aA" }, "A"), h("i", { class: "rt-aB" }, "B"), h("i", { class: "rt-aC" }, "C")), h("span", null, "Eight requests in a row, answered by three accounts. Under weighted, A has twice the weight."), h("span", { class: "spacer" }), stratState)],
	}));

	// ---------- retries, rests, conversations ----------

	let uid = 0;
	function numberRow(spec) {
		const id = `rt-l-${++uid}`;
		const st = stateEl();
		const value = getIn(routing, spec.path) ?? 0;
		const el = h("input", { class: "input mono cfg-num", type: "number", step: 1, min: spec.min, inputmode: "numeric", value: String(value), "aria-labelledby": id });
		const note = h("span", { class: "cfg-note" });
		const err = h("div", { class: "set-err", role: "alert", hidden: true });
		const paint = () => {
			const n = Number(el.value);
			note.textContent = el.value === "" ? "" : n === 0 ? spec.zero ?? "" : n < 0 ? spec.negative ?? "" : n >= 60 && spec.unit === "seconds" && n % 60 === 0 ? (n % 3600 === 0 ? plural(n / 3600, "hour") : plural(n / 60, "minute")) : "";
			note.hidden = !note.textContent;
		};
		let last = String(value);
		const commit = async () => {
			if (el.value === last) return;
			const n = Number(el.value);
			if (el.value.trim() === "" || !Number.isInteger(n) || n < spec.min) { err.replaceChildren(icon("info", 13), h("span", null, `Enter a whole number, ${fmtInt(spec.min)} or more.`)); err.hidden = false; return; }
			err.hidden = true;
			st.className = "set-state busy";
			st.replaceChildren("Saving…");
			try { await save(spec.path, n); last = el.value; flash(st, true); }
			catch (e) { flash(st, false, e.message); }
		};
		el.addEventListener("input", () => { paint(); err.hidden = true; });
		el.addEventListener("change", commit);
		el.addEventListener("keydown", (e) => {
			if (e.key === "Enter") { e.preventDefault(); commit(); }
			else if (e.key === "Escape") { el.value = last; paint(); err.hidden = true; }
		});
		paint();
		return row(spec, id, h("div", { class: "cfg-numwrap" }, el, h("span", { class: "cfg-unit" }, spec.unit), note), st, err);
	}

	function switchRow(spec) {
		const id = `rt-l-${++uid}`;
		const st = stateEl();
		const stored = Boolean(getIn(routing, spec.path));
		const shown = spec.invert ? !stored : stored;
		const words = h("span", { class: "cfg-onoff", "aria-hidden": "true" }, shown ? "On" : "Off");
		const el = h("button", { type: "button", class: "switch", role: "switch", "aria-checked": String(shown), "aria-labelledby": id }, h("span", { class: "track" }));
		const set = (v) => { el.setAttribute("aria-checked", String(v)); words.textContent = v ? "On" : "Off"; };
		let pending = false;
		el.addEventListener("click", async () => {
			if (pending) return;
			const next = el.getAttribute("aria-checked") !== "true";
			set(next);
			pending = true;
			el.setAttribute("aria-busy", "true");
			try { await save(spec.path, spec.invert ? !next : next); flash(st, true); }
			catch (e) { set(!next); flash(st, false, e.message); }
			finally { pending = false; el.removeAttribute("aria-busy"); }
		});
		return row(spec, id, h("span", { class: "cfg-switch", onClick: (e) => { if (e.target === words) el.click(); } }, el, words), st);
	}

	function row(spec, id, control, st, err = null) {
		return h("div", { class: "cfg-row" },
			h("div", { class: "cfg-label" }, h("span", { class: "cfg-name", id }, spec.label), h("p", { class: "cfg-hint" }, spec.hint)),
			h("div", { class: "cfg-control" }, h("div", { class: "cfg-line" }, control, st), err),
		);
	}

	host.append(card({
		title: "Retries and rests",
		sub: "What happens when an account can't answer. These apply to direct models and to every stage of a virtual model.",
		body: h("div", { class: "rt-groups" }, SETTINGS.map((g) => h("div", { class: "cfg-group" },
			h("div", { class: "cfg-group-head" }, h("h3", { class: "cfg-group-title" }, g.title)),
			h("div", { class: "cfg-list" }, g.rows.map((spec) => (spec.kind === "int" ? numberRow(spec) : switchRow(spec)))),
		))),
	}));

	// ---------- resting accounts ----------

	const restBody = h("div", null, spinner());
	const wakeAll = btn("Wake all", { icon: "zap", cls: "outline", disabled: true, onClick: () => wake(resting) });
	let resting = [];
	async function loadResting() {
		try {
			invalidate("credentials");
			const response = await engine.credentials();
			if (disposed) return;
			const list = Array.isArray(response) ? response : response?.files ?? response?.credentials ?? [];
			resting = list.filter((c) => (c.auth_index ?? c.authIndex) && (c.cooldowns ?? []).some((x) => x.remaining_seconds > 0));
			paintResting(list.length);
		} catch (e) { if (!disposed) restBody.replaceChildren(errorState(e, loadResting)); }
	}
	function paintResting(total) {
		wakeAll.disabled = !resting.length;
		if (!resting.length) {
			restBody.replaceChildren(h("div", { class: "rt-calm" }, icon("check", 16), h("div", null, h("strong", null, "Every account is ready."), h("span", null, ` None of the ${plural(total, "account")} is resting. An account rests after a rate limit or provider error, and shows here with the time left.`))));
			return;
		}
		restBody.replaceChildren(table([
			{ key: "who", label: "Account", render: (c) => h("div", { class: "rt-acct" }, h("span", { class: "rt-prov" }, c.label || c.email || c.name), h("span", { class: "ink-3" }, channelName(c.provider ?? c.type ?? ""))) },
			{ key: "why", label: "Why", wrap: true, render: (c) => (c.cooldowns ?? []).filter((x) => x.remaining_seconds > 0).map((x) => h("div", null, reasonText(x), x.model_key ? h("span", { class: "ink-3" }, ` · ${x.model_key}`) : null, x.http_status ? h("span", { class: "ink-3" }, ` · HTTP ${x.http_status}`) : null)) },
			{ key: "left", label: "Back in", num: true, render: (c) => remaining(Math.max(...c.cooldowns.map((x) => x.remaining_seconds))) },
			{ key: "act", label: "", render: (c) => btn("Wake", { cls: "ghost sm", onClick: (e) => { e.stopPropagation(); wake([c]); } }) },
		], resting, { keyOf: (c) => c.auth_index ?? c.id, stack: true }));
	}
	async function wake(list) {
		if (!list.length) return;
		if (list.length > 1 && !(await confirm({ title: `Wake ${plural(list.length, "account")}?`, text: "They become eligible again right away, even if the provider is still limiting them.", confirmLabel: "Wake all" }))) return;
		wakeAll.disabled = true;
		let n = 0;
		try {
			for (const c of list) { await engine.post("routing/cooldown/reset", { auth_index: c.auth_index ?? c.authIndex }); n++; }
			toast(n === 1 ? `${list[0].label || list[0].name} is ready again` : `${fmtInt(n)} accounts are ready again`);
		} catch (e) { toast(e instanceof ApiError ? e.message : String(e), "bad"); }
		await loadResting();
	}
	host.append(card({ title: "Resting now", sub: "Accounts NeoProxy is skipping for a while after errors. Wake one to make it eligible again at once.", actions: [wakeAll], body: restBody, flush: true, cls: "rt-rest" }));

	// ---------- built-in model lists ----------

	const modelsBody = h("div", null, spinner());
	let definitions = [];
	let query = "";
	let selected = null;
	const idOf = (m) => (typeof m === "string" ? m : m.id);
	const close = () => { selected = null; ctx.setInspector(null); paintModels(); };
	const open = (row) => {
		selected = row.channel;
		paintModels();
		const q = query.trim().toLowerCase();
		const ids = row.models.map(idOf);
		const shown = q ? ids.filter((id) => id.toLowerCase().includes(q)) : ids;
		ctx.setInspector(inspector({ title: channelName(row.channel), sub: `${plural(row.models.length, "model")} built in${q && shown.length !== ids.length ? `, ${fmtInt(shown.length)} matching “${query.trim()}”` : ""}`, onClose: close, body: [section(null, h("div", { class: "list" }, shown.map((id) => h("div", { class: "li" }, h("code", null, id)))))] }));
	};
	function paintModels() {
		const q = query.trim().toLowerCase();
		const rows = definitions.map((d) => ({ ...d, hits: q ? d.models.filter((m) => idOf(m).toLowerCase().includes(q)).length : d.models.length }))
			.filter((d) => !q || d.channel.includes(q) || channelName(d.channel).toLowerCase().includes(q) || d.hits);
		modelsBody.replaceChildren(table([
			{ key: "channel", label: "Provider", width: "220px", render: (r) => h("span", { class: "rt-prov", title: r.channel }, channelName(r.channel)), sortBy: (r) => channelName(r.channel) },
			{ key: "sample", label: "For example", clip: true, render: (r) => h("span", { class: "ink-3 mono rt-sample" }, r.models.slice(0, 3).map(idOf).join(", ")) },
			{ key: "count", label: q ? "Matching" : "Models", num: true, width: "110px", render: (r) => (q && r.hits !== r.models.length ? `${fmtInt(r.hits)} of ${fmtInt(r.models.length)}` : fmtInt(r.models.length)), sortBy: (r) => r.models.length },
		], rows, { onRow: open, keyOf: (r) => r.channel, selectedKey: selected, stack: true, emptyState: empty({ icon: "box", title: "Nothing matches", hint: q ? `No provider or built-in model mentions “${query.trim()}”.` : "The engine publishes no built-in model lists." }) }));
	}
	const loadModels = async () => {
		try {
			const results = await Promise.all(CHANNELS.map(async (channel) => {
				try { const result = await engine.get(`routing/model-definitions/${channel}`); return { channel, models: result?.models ?? [] }; }
				catch (e) { if (e instanceof ApiError && e.status === 404) return null; throw e; }
			}));
			if (disposed) return;
			definitions = results.filter((r) => r && r.models.length);
			paintModels();
		} catch (e) { if (!disposed) modelsBody.replaceChildren(errorState(e, loadModels)); }
	};
	const filter = h("div", { class: "search" }, icon("search", 15), h("input", { class: "input", type: "search", placeholder: "Find a provider or model", "aria-label": "Find a provider or model", onInput: debounce((e) => { query = e.target.value; paintModels(); }, 100), onSearch: (e) => { if (!e.target.value) { query = ""; paintModels(); } } }));
	host.append(card({ title: "Built-in model lists", sub: "The models each provider offers out of the box, as the engine knows them. Pick one to see every model id.", actions: [filter], flush: true, body: modelsBody, cls: "rt-models" }));

	const escape = (e) => { if (e.key === "Escape" && selected) close(); };
	document.addEventListener("keydown", escape);
	await Promise.all([loadResting(), loadModels()]);
	return () => { disposed = true; document.removeEventListener("keydown", escape); };
}
