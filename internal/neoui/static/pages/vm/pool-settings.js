// How a stage draws on its provider's accounts: which accounts, how one is picked,
// where it stops (per-account and pool thresholds over a usage window), the
// conditions it serves under, and, for admins, which Lua functions it uses. These
// settings are about capacity; the model's own properties live beside them in the
// stage card.

import { h, fmtPct, plural, clear } from "../../lib/dom.js";
import { meterFor, accountHealth } from "../../lib/providers.js";
import { section, field, input, seg, picker, btn, drawLabel, DRAW_MODES, canonicalDraw, WINDOW_WORDS, stagePool, select } from "../../lib/components.js";
import { ruleBuilder, toText, TEMPLATES } from "../../lib/rules.js";
import { drawPicker } from "../../lib/draw-tiles.js";

export const WINDOWS = ["5h", "day", "week", "month"];
export const LUA_SLOTS = [
	{ key: "eligible", label: "Account condition", builtin: "Built-in: every account with room", hint: "Returns true or false. false skips that account for this request." },
	{ key: "perAccount", label: "Per-account threshold", builtin: "Built-in: the slider above", hint: "Returns a number from 0 to 1. It can only lower the threshold above, never raise it." },
	{ key: "cap", label: "Pool threshold", builtin: "Built-in: the slider above", hint: "Returns a number from 0 to 1, asked once per stage. It can only lower the pool threshold above." },
];

const bold = (text) => h("b", null, text);
const pct = (v) => Number((v * 100).toPrecision(15));

// A usage bar: the account's current usage fills the track; the bright handle
// is where this stage stops drawing from it. Drag the handle or type a number.
function usageBar({ used, limit, window: win, label, onChange, disabled = false }) {
	const range = h("input", { type: "range", class: "ub-range", min: 1, max: 100, step: 1, value: Math.round(limit * 100), "aria-label": `${label}: stop at percent of ${WINDOW_WORDS[win] ?? win} usage`, disabled });
	const num = input({ type: "number", min: 1, max: 100, step: 1, value: Math.round(limit * 100), class: "input sm ub-num", "aria-label": `${label}: stop at percent`, disabled });
	const fill = h("div", { class: "ub-fill" });
	const words = h("div", { class: "ub-words" });
	const paint = () => {
		const l = Number(range.value) / 100;
		range.style.setProperty("--limit", `${range.value}%`);
		fill.style.width = `${Math.round((used ?? 0) * 100)}%`;
		fill.classList.toggle("over", used !== null && used >= l);
		words.replaceChildren(
			used === null ? h("span", { class: "ink-3" }, `no ${WINDOW_WORDS[win] ?? win} meter`) : h("span", null, h("b", null, fmtPct(used)), ` of ${WINDOW_WORDS[win] ?? win} used`),
			h("span", { class: "spacer" }),
			h("span", null, "stops at ", h("b", null, `${range.value}%`)),
		);
	};
	const commit = (v) => { const n = Math.min(100, Math.max(1, Math.round(v))); range.value = String(n); num.value = String(n); paint(); onChange(n / 100); };
	range.addEventListener("input", () => { num.value = range.value; paint(); });
	range.addEventListener("change", () => commit(Number(range.value)));
	num.addEventListener("change", () => { if (Number.isFinite(num.valueAsNumber)) commit(num.valueAsNumber); });
	paint();
	return h("div", { class: `usage-bar ${disabled ? "disabled" : ""}` }, h("div", { class: "ub-track" }, fill, range), h("div", { class: "ub-row" }, words, h("span", { class: "ub-numwrap" }, num, "%")));
}

// One line for a closed "Accounts and limits" panel.
export function poolLine(vault, s) {
	const n = stagePool(s, vault.accounts).length;
	return `${plural(n, "account")} · ${drawLabel(s.draw).toLowerCase()} · until ${fmtPct(s.perAccount, null)} each, ${fmtPct(s.cap, null)} pool · ${WINDOW_WORDS[s.window]}${s.condition ? " · with conditions" : ""}`;
}

// env: { models, vault, ruleFields(), luaNames(), isAdmin, onDrawCatalogue(fn) }
export function poolSettings(env, s, { onChange }) {
	const providerId = () => s.model.split("/")[0];
	const pool = () => env.vault.accounts.filter((a) => a.providerId === providerId());
	let showAllAccounts = false;

	const update = () => {
		onChange();
		renderAccounts();
		renderSentences();
		renderDraw();
		renderSummary();
	};

	// --- accounts ---
	const accountsSection = h("div");
	// "until N%" per account: the stage threshold by default, or this account's
	// own override, so one person can be allowed 50% of X and 70% of Y.
	const limitOf = (a) => s.accountLimits?.[a.id] ?? s.perAccount;
	const dupe = (a) => pool().some((b) => b !== a && b.label === a.label);
	const otherMeters = (a) => { const ks = WINDOWS.filter((w) => meterFor(a, w)); return ks.length ? `It reports ${ks.map((w) => WINDOW_WORDS[w]).join(" and ")} usage; switch the window to use one.` : ""; };
	const setLimit = (a, v) => {
		const next = { ...(s.accountLimits ?? {}) };
		if (!Number.isFinite(v) || Math.abs(v - s.perAccount) < 0.0005) delete next[a.id];
		else next[a.id] = Math.max(0.01, Math.min(1, v));
		s.accountLimits = Object.keys(next).length ? next : undefined;
		update();
	};
	const renderAccounts = () => {
		clear(accountsSection);
		const all = pool();
		const picked = stagePool(s, env.vault.accounts);
		const allSeg = seg([{ value: "all", label: `All ${all.length}` }, { value: "pick", label: "Pick" }], s.accounts === "all" ? "all" : "pick", (v) => {
			s.accounts = v === "all" ? "all" : all.map((a) => a.id);
			update();
		});
		allSeg.setAttribute("aria-label", "Which accounts this stage may use");
		accountsSection.append(allSeg);
		if (!all.length) accountsSection.append(h("p", { class: "hint" }, "No accounts hold this provider yet. Add one in the Pool (Add provider access), then pick it here."));
		const shown = showAllAccounts ? all : all.slice(0, 8);
		for (const a of shown) {
			const on = picked.includes(a);
			const m = meterFor(a, s.window);
			const health = accountHealth(a);
			accountsSection.append(h("label", { class: "vm-acct" },
				h("input", {
					type: "checkbox", checked: on, "aria-label": `Use ${a.label}`,
					onChange: (e) => {
						const set = new Set(s.accounts === "all" ? all.map((x) => x.id) : s.accounts);
						if (e.target.checked) set.add(a.id); else set.delete(a.id);
						s.accounts = [...set];
						update();
					},
				}),
				h("div", { class: "lt vm-acct-main" },
					h("div", { class: "main row", style: { gap: "8px" } }, h("code", null, a.label), dupe(a) ? h("span", { class: "tag", title: "The same account is in the pool twice: once signed in here and once shared from NeoPi. Either one serves; keep whichever you prefer." }, "also via " + (a.kind === "imported" || a.kind === "neopi" ? "sign-in" : "NeoPi")) : null, s.accountLimits?.[a.id] !== undefined ? h("span", { class: "tag own", title: `This account has its own stopping point; the stage default is ${fmtPct(s.perAccount, null)}` }, "own limit") : null),
					h("div", { class: "sub" }, [a.plan, health.text].filter(Boolean).join(" · ")),
					m
						? usageBar({ used: m.used, limit: limitOf(a), window: s.window, label: a.label, onChange: (v) => setLimit(a, v), disabled: !on })
						: h("div", { class: "sub nometer" }, `No ${WINDOW_WORDS[s.window]} meter on this account: it is always available and no percentage applies. ${otherMeters(a)}`),
				),
			));
		}
		if (all.length > shown.length) accountsSection.append(btn(`Show all ${all.length}`, { cls: "outline sm", onClick: () => { showAllAccounts = true; renderAccounts(); } }));
		if (s.accounts !== "all" && !s.accounts.length) accountsSection.append(h("div", { class: "error", style: { marginTop: "6px" } }, "Pick at least one account, or this stage can never serve."));
	};

	// --- draw ---
	const drawHint = h("p", { class: "hint", style: { margin: "6px 0 0" } });
	const renderDrawHint = () => { const m = DRAW_MODES.find((mode) => mode.value === canonicalDraw(s.draw)); drawHint.textContent = m ? (m.doc ?? m.hint) : ""; };
	// Which modes this stage can use right now, with the reason when it cannot.
	const drawUnavailable = () => {
		const picked = stagePool(s, env.vault.accounts);
		const metered = picked.filter((a) => meterFor(a, s.window)).length;
		const priced = picked.filter((a) => a.face?.plan?.monthlyUSD > 0).length;
		const out = {};
		for (const m of DRAW_MODES) {
			if (m.needs?.includes("meter") && picked.length && !metered) out[m.value] = `none of the selected accounts reports ${WINDOW_WORDS[s.window]} usage`;
			if (m.needs?.includes("price") && picked.length && !priced) out[m.value] = "no plan prices on these accounts";
		}
		return out;
	};
	const drawHost = h("div");
	const renderDraw = () => {
		drawHost.replaceChildren(drawPicker({
			modes: DRAW_MODES.map((m) => ({ id: m.value, label: m.label, short: m.hint, doc: m.doc ?? m.hint, omniroute: m.omniroute })),
			value: canonicalDraw(s.draw),
			unavailable: drawUnavailable(),
			onPick: (v) => { s.draw = v; renderDrawHint(); update(); },
		}));
	};
	env.onDrawCatalogue(() => { if (drawHost.isConnected) { renderDraw(); renderDrawHint(); } });

	// --- thresholds ---
	const sentenceEach = h("p", { class: "vm-sentence" });
	const sentencePool = h("p", { class: "vm-sentence" });
	const meterNote = h("div", { class: "vm-note" });
	function renderSentences() {
		const overrides = Object.keys(s.accountLimits ?? {}).length;
		const n = stagePool(s, env.vault.accounts).length;
		sentenceEach.replaceChildren("An account can serve while its ", bold(WINDOW_WORDS[s.window]), " usage is below ", bold(fmtPct(s.perAccount, null)), overrides ? ` (default; ${overrides === 1 ? "one account has" : `${overrides} accounts have`} its own stopping point on its bar).` : ".");
		sentencePool.replaceChildren("The stage is skipped once the ", bold(n === 1 ? "selected account's" : `${n} selected accounts' average`), " ", bold(WINDOW_WORDS[s.window]), " usage reaches ", bold(fmtPct(s.cap, null)), ".");
		const picked = stagePool(s, env.vault.accounts);
		const unmetered = picked.filter((a) => !meterFor(a, s.window)).length;
		meterNote.classList.toggle("hidden", !picked.length || unmetered === 0);
		meterNote.textContent = unmetered === picked.length
			? `${picked.length === 1 ? "This account reports" : "These accounts report"} no ${WINDOW_WORDS[s.window]} usage, so these two percentages have nothing to measure and the stage is always available. Choose a window ${picked.length === 1 ? "it reports" : "they report"}, or use a rule that does not need usage.`
			: `${plural(unmetered, "account")} without a ${WINDOW_WORDS[s.window]} meter ${unmetered === 1 ? "is" : "are"} always available; the percentages apply to the others.`;
	}
	const threshold = (label, key, hint) => {
		const num = input({ type: "number", min: 0, max: 100, step: 0.5, value: pct(s[key]), class: "input vm-num", "aria-label": `${label} (percent)` });
		const range = h("input", { type: "range", class: "vm-range", min: 0, max: 100, step: 0.5, value: pct(s[key]), "aria-label": label });
		const paint = () => range.style.setProperty("--fill", `${range.value}%`);
		paint();
		range.addEventListener("input", () => { s[key] = Number(range.value) / 100; num.value = range.value; paint(); update(); });
		num.addEventListener("input", () => {
			const v = num.valueAsNumber;
			if (!Number.isFinite(v)) return;
			s[key] = Math.min(100, Math.max(0, v)) / 100;
			range.value = String(pct(s[key]));
			paint();
			update();
		});
		num.addEventListener("blur", () => { num.value = String(pct(s[key])); });
		return h("div", { class: "vm-threshold" }, h("div", { class: "row" }, h("label", { class: "vm-t-label", for: (num.id ||= `vm-t-${key}-${s.id}`) }, label), h("span", { class: "spacer" }), h("span", { class: "vm-numwrap" }, num, "%")), range, hint);
	};

	// --- the condition this stage serves under, beyond the percentages ---
	const codeOut = h("pre", { class: "code", style: { whiteSpace: "pre-wrap" } });
	const rules = ruleBuilder({ value: s.condition ?? null, fields: env.ruleFields(), onChange: (tree) => { if (tree) s.condition = tree; else delete s.condition; codeOut.textContent = toText(s.condition); onChange(); renderSummary(); } });
	codeOut.textContent = toText(s.condition);
	const templateBtn = btn("Start from a template", { cls: "sm ghost" });
	templateBtn.setAttribute("aria-haspopup", "dialog");
	templateBtn.addEventListener("click", () => picker(templateBtn, {
		placeholder: "Templates",
		items: TEMPLATES.map((t, i) => ({ value: i, label: t.label, sub: toText(t.tree) })),
		onPick: (i) => { s.condition = structuredClone(TEMPLATES[i].tree); rules.set(s.condition); codeOut.textContent = toText(s.condition); onChange(); renderSummary(); },
	}));
	const rulesSection = h("div", null,
		h("p", { class: "hint", style: { marginTop: 0 } }, "Checked on top of the percentages. A rule about an account applies to each selected account; the others decide whether this model is tried at all. Missing facts count as false."),
		rules.el,
		h("div", { class: "row", style: { gap: "6px", marginTop: "8px" } }, templateBtn, h("span", { class: "spacer" }), h("span", { class: "ink-3", style: { fontSize: "12px" } }, "as text")),
		h("div", { class: "rules-code" }, codeOut),
	);

	// --- Lua functions this stage uses (admins; only when the script defines some) ---
	const luaHost = h("div");
	function renderLua() {
		const names = env.luaNames();
		luaHost.replaceChildren();
		if (!env.isAdmin || !names.length) return;
		luaHost.append(section("Lua functions",
			h("p", { class: "hint", style: { marginTop: 0 } }, "Pick which of the script's functions this stage uses. Lua can only make a stage stricter."),
			...LUA_SLOTS.map((slot) => field(slot.label, select([{ value: "", label: slot.builtin }, ...names.map((n) => ({ value: n, label: n }))], s.lua?.[slot.key] ?? "", (v) => {
				const next = { ...(s.lua ?? {}) };
				if (v) next[slot.key] = v; else delete next[slot.key];
				if (Object.keys(next).length) s.lua = next; else delete s.lua;
				onChange();
			}), slot.hint)),
		));
	}

	const summaryLine = h("span", { class: "vm-more-line" });
	function renderSummary() { summaryLine.textContent = poolLine(env.vault, s); }

	renderAccounts();
	renderDraw();
	renderDrawHint();
	renderSentences();
	renderSummary();
	renderLua();

	const el = h("div", { class: "vm-pool" },
		section("Accounts in the pool", accountsSection),
		section(
			"Serves a request while",
			field(
				"Measured over",
				seg(WINDOWS.map((w) => ({ value: w, label: WINDOW_WORDS[w] ?? w })), s.window, (v) => { s.window = v; update(); }),
				"Which of the provider's usage windows the percentages read. Providers differ: Claude reports weekly (and sometimes 5-hour), Codex 5-hour and weekly, API keys usually nothing unless you add a local meter.",
			),
			meterNote,
			threshold("Each account's usage below", "perAccount", sentenceEach),
			threshold("Selected accounts' average below", "cap", sentencePool),
			h("details", { class: "vm-advanced" },
				h("summary", null, "Advanced"),
				field(
					"Whose usage counts",
					seg([
						{ value: "all", label: "Everyone's", title: "One usage number per account, shared by everyone granted" },
						{ value: "user", label: "Only this person's", title: "Each person is measured against their own usage" },
					], s.per, (v) => { s.per = v; update(); }),
					"Meters reported by the provider, like Claude and Codex subscriptions, always count everyone's usage. This choice only affects local token meters.",
				),
			),
		),
		Object.assign(section("How to draw", h("p", { class: "hint", style: { marginTop: 0 } }, "Among the accounts that may serve, which one gets this request. Names in grey are what OmniRoute calls the same thing."), drawHost, drawHint), { className: "section span-all" }),
		Object.assign(section("Serve this model only when", rulesSection), { className: "section span-all" }),
		Object.assign(h("div", { class: "span-all" }, luaHost), {}),
	);
	return { el, summaryLine, renderLua, refresh: () => { renderAccounts(); renderSentences(); renderDraw(); renderSummary(); } };
}
