// Pool: every sign-in, API key and shared NeoPi that NeoProxy can draw on,
// grouped by provider. Rows show the one meter per window (provider-reported,
// local, or none), health, traffic and owner. A row opens a drawer on the right
// with everything about that account as a settings list: usage, the local
// meter (edited in place), switches and the virtual models that draw from it.
// Accounts that need a person (expired, failing, spent) sort first.

import { h, svg, replace, icon, fmtAgo, fmtIn, fmtPct, fmtCompact, fmtDate, fmtInt, plural } from "../lib/dom.js";
import { neo, engine, invalidate, onChange } from "../lib/client.js";
import { accountHealth, canonicalProviderId, humanMessage } from "../lib/providers.js";
import { pageHead, card, btn, iconBtn, badge, tag, toggle, search, seg, select, empty, spinner, errorState, kv, servedName, confirm, toast, ring, meterRing, healthStatus, copyLine } from "../lib/components.js";
import { openTestDialog } from "./pool-test.js";
import { openAddAccess, KIND_COPY, hueVars } from "./pool-add.js";

const KINDS = ["5h", "day", "week", "month"];
const KIND_WORD = { "5h": "5h", day: "day", week: "week", month: "month" };
const KIND_LONG = { "5h": "Every 5 hours", day: "Every day", week: "Every week", month: "Every month" };
const KIND_NOUN = { "5h": "5-hour", day: "Daily", week: "Weekly", month: "Monthly" };
const SEVERITY = { bad: 0, warn: 1, ok: 2, muted: 3 };
const MORE = '<circle cx="12" cy="5" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="12" cy="19" r="1"/>';
const accountPath = (id) => `/v8/neo/vault/accounts/${encodeURIComponent(id)}`;

// Survives re-renders so a repaint after any change keeps the person's place.
// `collapsed` is the unfiltered layout. A search or the attention filter opens
// every matching group; groups collapsed while it is active go in `shutFiltered`,
// which empties whenever the query or filter changes. `drafts` holds unsaved
// meter edits per account and window, so a background refresh never eats them.
const ui = { query: "", filter: "all", collapsed: new Set(), shutFiltered: new Set(), tests: new Map(), pending: new Set(), drafts: new Map() };
const shutGroups = () => (ui.query.trim() || ui.filter === "attention" ? ui.shutFiltered : ui.collapsed);

const notYet = (e) => e?.status === 404 && typeof e.body === "string";
const explain = (e) => (notYet(e) ? "This server doesn't support that yet." : e?.message || String(e));

// Token counts people type: 500k, 2M, 1.5m, 2,000,000.
function parseTokens(text) {
	const m = String(text ?? "").trim().replace(/[,_\s]/g, "").match(/^(\d+(?:\.\d+)?)([kmb])?$/i);
	if (!m) return NaN;
	const n = Number(m[1]) * ({ k: 1e3, m: 1e6, b: 1e9 }[m[2]?.toLowerCase()] ?? 1);
	return Number.isFinite(n) ? Math.round(n) : NaN;
}
function shortTokens(n) {
	for (const [unit, size] of [["B", 1e9], ["M", 1e6], ["k", 1e3]]) {
		const v = Math.round((n / size) * 1000) / 1000;
		if (n >= size && v * size === n) return `${v}${unit}`;
	}
	return String(n);
}
const localInput = (ts) => new Date(ts - new Date(ts).getTimezoneOffset() * 60000).toISOString().slice(0, 16);
const whenWords = (ts) => new Date(ts).toLocaleString("en-GB", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

export async function render(ctx) {
	const admin = ctx.session.role === "admin";
	const me = ctx.session.userId;
	const S = { vault: null, vms: [], users: [], sources: [] };
	let selectedId = ctx.params[0] ?? null;
	let disposed = false, loading = 0, closeMenuFn = null, timer = 0;

	const pageHost = h("div", { class: "pool" });
	const head = pageHead({
		title: "Pool",
		desc: admin
			? "Everything NeoProxy can draw on: signed-in subscriptions, API keys and shared NeoPi accounts. Virtual models decide which of them answers a request."
			: "The provider access you contribute. It is pooled with the rest of your organization; you can test it, pause it or remove it here.",
		actions: [btn("Add provider access", { icon: "plus", cls: "primary", onClick: () => openAdd() })],
	});
	head.classList.add("ax-head");
	ctx.root.append(head, pageHost, spinner());

	// ---------- data ----------
	async function fetchAll() {
		const [vault, vms, users, sources] = await Promise.all([
			neo.vault(),
			neo.virtualModels().catch(() => S.vms),
			admin ? neo.users().catch(() => S.users) : S.users,
			admin ? neo.sources().catch(() => []) : [],
		]);
		Object.assign(S, { vault, vms, users, sources: Array.isArray(sources) ? sources : (sources?.sources ?? []) });
	}
	async function reload() {
		const mine = ++loading;
		try { await fetchAll(); } catch (e) { if (!disposed) toast(`Couldn't refresh the pool: ${e.message}`, "bad"); return; }
		if (!disposed && mine === loading) paint();
	}

	const providers = () => S.vault.providers;
	const provOf = (a) => providers().find((p) => p.id === a.providerId);
	const accounts = () => S.vault.accounts.filter((a) => admin || a.ownerId === me);
	const person = (id) => {
		const p = S.vault.people.find((x) => x.id === id) ?? S.users.find((x) => x.id === id);
		return p ? p.name || p.handle : id ? "Unknown person" : "Unassigned";
	};
	const contributors = () => {
		const owners = S.users.filter((u) => ["admin", "owner"].includes(u.role) && (u.state ?? "active") === "active");
		const list = owners.length ? owners : S.vault.people;
		return list.map((u) => ({ id: u.id, handle: u.handle, name: u.name }));
	};
	const sev = (a) => accountHealth(a).kind;
	const attention = (a) => sev(a) === "bad" || sev(a) === "warn";
	const modelsOf = (a) => a.face?.models ?? a.modelIds ?? [];
	const openAdd = (initial = {}) => openAddAccess({ session: ctx.session, catalog: S.vault.catalog, people: contributors(), initial });

	function kindLine(a) {
		const src = a.sourceId ? S.sources.find((s) => s.id === a.sourceId) : null;
		const kind = { subscription: "Signed in", apiKey: "API key", imported: a.face?.refresh === "locked" ? "Copied from NeoPi" : "Imported from NeoPi", neopi: src ? `Shared via ${src.label}` : "Shared from NeoPi", file: "Credential file" }[a.kind] ?? "Account";
		const plan = a.plan && !/^subscription$/i.test(a.plan) ? a.plan : "";
		return a.kind === "apiKey" ? plan || kind : [plan, kind].filter(Boolean).join(" · ");
	}
	const labelText = (a) => (a.kind === "apiKey" && a.label.startsWith("…") ? `Key ${a.label}` : a.label);

	// ---------- the row menu ----------
	function closeMenu() { closeMenuFn?.(); closeMenuFn = null; }
	function openMenu(anchor, items) {
		closeMenu();
		// Focus goes back to the anchor before the item runs, so a dialog it opens
		// returns focus there on close instead of to the page.
		const menu = h("div", { class: "pool-menu", role: "menu" }, items.map((it) => (it === "-" ? h("div", { class: "pool-menu-sep", role: "separator" }) : h("button", { type: "button", role: "menuitem", class: `pool-menu-item ${it.danger ? "danger" : ""}`, disabled: it.disabled, onClick: () => { closeMenu(); anchor.focus?.({ preventScroll: true }); it.onClick(); } }, icon(it.icon, 14), h("span", null, it.label)))));
		document.body.append(menu);
		const r = anchor.getBoundingClientRect(), w = menu.offsetWidth, ht = menu.offsetHeight;
		menu.style.left = `${Math.max(8, Math.min(r.right - w, innerWidth - w - 8))}px`;
		menu.style.top = `${r.bottom + 6 + ht > innerHeight ? Math.max(8, r.top - ht - 6) : r.bottom + 6}px`;
		const buttons = [...menu.querySelectorAll("button:not(:disabled)")];
		buttons[0]?.focus();
		const onKey = (e) => {
			if (e.key === "Escape") { e.preventDefault(); e.stopImmediatePropagation(); closeMenu(); anchor.focus?.(); }
			else if (e.key === "ArrowDown" || e.key === "ArrowUp") { e.preventDefault(); const i = buttons.indexOf(document.activeElement); buttons[(i + (e.key === "ArrowDown" ? 1 : -1) + buttons.length) % buttons.length]?.focus(); }
			else if (e.key === "Tab") closeMenu();
		};
		const onDown = (e) => { if (!menu.contains(e.target)) closeMenu(); };
		document.addEventListener("keydown", onKey, true);
		document.addEventListener("pointerdown", onDown, true);
		closeMenuFn = () => { menu.remove(); document.removeEventListener("keydown", onKey, true); document.removeEventListener("pointerdown", onDown, true); };
	}

	const isDisabled = (a) => a.face?.health === "disabled";
	const reportsUsage = (a) => ["subscription", "imported"].includes(a.kind) && !!(a.providerWindows?.length || ["claude", "codex"].includes(provOf(a)?.canonicalId));
	const expired = (a) => a.state === "expired" || a.face?.health === "expired";
	const signInAgain = (a) => openAdd({ provider: provOf(a)?.canonicalId, method: "subscription" });
	// An imported sign-in is a copy NeoProxy never refreshes (NeoPi keeps the
	// original working), so once it runs out the fix is a sign-in of its own here.
	const isCopy = (a) => a.kind === "imported" && a.face?.refresh === "locked";
	const reimport = () => openAdd({ method: "imported" });
	const fixHint = (a) => (expired(a) && isCopy(a) ? "This copy of NeoPi's sign-in has run out. Sign in here to give NeoProxy its own, or copy a fresh one from NeoPi." : null);
	const setDisabled = (a, off) => act(a, () => neo.updateAccess(a.id, { disabled: off }), off ? "Account turned off. No virtual model draws from it." : "Account turned on");
	function actionsFor(a) {
		const acts = a.face?.actions ?? [];
		const p = provOf(a);
		return [
			{ id: "test", label: "Test this account", icon: "play", run: () => runTest(a) },
			{ id: "payload", label: "Test payload…", icon: "scroll", run: () => openTestDialog(a, { provider: p, onChange: () => reload() }) },
			{ id: "meter", label: "Set a NeoProxy cap", icon: "sliders", run: () => openDrawer(a.id, { focus: firstMeterKey(a) }) },
			expired(a) && isCopy(a) ? { id: "reimport", label: "Copy again from NeoPi", icon: "download", run: reimport } : null,
			acts.includes("refresh") ? { id: "refresh", label: "Refresh sign-in", icon: "refresh", run: () => act(a, () => neo.refreshAccess(a.id), "Sign-in refreshed") } : null,
			reportsUsage(a) && !expired(a) ? { id: "usage", label: "Refresh usage", icon: "activity", run: () => act(a, () => neo.refreshQuota(a.id), "Usage refreshed") } : null,
			acts.includes("disable") ? { id: "toggle", label: isDisabled(a) ? "Turn on" : "Turn off", icon: "power", run: () => setDisabled(a, !isDisabled(a)) } : null,
			acts.includes("remove") ? "-" : null,
			acts.includes("remove") ? { id: "remove", label: "Remove from the pool…", icon: "trash", danger: true, run: () => removeAccount(a) } : null,
		].filter(Boolean);
	}

	async function act(a, fn, ok) {
		ui.pending.add(a.id);
		paint();
		try {
			const r = await fn();
			toast(typeof ok === "function" ? ok(r) : ok);
		} catch (e) { toast(explain(e), "bad"); }
		finally { ui.pending.delete(a.id); if (!disposed) paint(); }
	}

	async function runTest(a) {
		if (ui.tests.get(a.id)?.running) return;
		ui.tests.set(a.id, { running: true });
		paint();
		try {
			const r = await neo.testAccount(a.id);
			ui.tests.set(a.id, { ...r, at: r.checkedAt ?? Date.now() });
		} catch (e) {
			ui.tests.set(a.id, notYet(e) ? { unavailable: true } : { ok: false, error: e.message, at: Date.now() });
		}
		if (!disposed) paint();
	}

	async function removeAccount(a) {
		const what = a.kind === "apiKey" ? "The key is deleted from NeoProxy. Virtual models stop drawing from it; the provider still has the key on its side, so revoke it there too if you no longer want it valid."
			: a.kind === "neopi" ? "It leaves the pool. NeoPi keeps its own sign-in."
				: "The sign-in is deleted from NeoProxy and virtual models stop drawing from it. To use this account again, sign in again.";
		if (!(await confirm({ title: `Remove ${labelText(a)}?`, text: what, confirmLabel: "Remove from the pool", danger: true }))) return;
		if (selectedId === a.id) closeDrawer({ restore: false });
		await act(a, () => neo.removeAccess(a.id), "Removed from the pool");
	}

	// ---------- pieces of a row ----------
	function testLine(a, { full = false } = {}) {
		const t = ui.tests.get(a.id);
		if (t?.running) return { kind: "", text: "Testing…" };
		if (t?.unavailable) return { kind: "warn", text: "Test not available on this server yet" };
		if (t) return t.ok
			? { kind: "ok", text: `Test passed · ${t.latencyMs} ms${t.reply ? ` · “${t.reply}”` : ""}${full && t.model ? ` · ${t.model}` : ""}` }
			: { kind: "bad", text: `Test failed: ${t.error || "no reply from the provider"}${t.providerMessage ? ` · ${humanMessage(t.providerMessage, 120)}` : ""}` };
		const p = a.face?.lastProbe;
		if (full && p) return p.ok ? { kind: "ok", text: `Last test passed ${fmtAgo(p.checkedAt)} · ${p.latencyMs} ms` } : { kind: "bad", text: `Last test failed ${fmtAgo(p.checkedAt)}: ${p.error || "no reply"}` };
		return null;
	}

	function trafficSentence(a) {
		const u = a.usage ?? a.face?.usage;
		if (!u?.requests24h) return "Ready to serve. No requests in the last 24 hours.";
		return `${plural(u.requests24h, "request")} in the last 24 hours${u.failed24h ? `, ${u.failed24h} failed` : ", none failed"}.`;
	}

	function meterCell(a) {
		// Quiet on purpose: most API keys have no meter. The drawer explains what that means.
		if (!a.meters.length) return h("span", { class: "pl-meter none", title: "No usage reading and no NeoProxy routing cap" }, h("span", { class: "pl-meter-v", "aria-label": "No usage reading" }, "—"));
		// Each meter is a Tab stop whose attribution (provider-reported or local)
		// shows on hover and on keyboard focus, not only in a native title.
		return h("span", { class: "pl-meters" }, a.meters.map((m) => {
			const ringEl = meterRing(a, { size: 28, kind: m.kind });
			ringEl.removeAttribute("title");
			return h("span", { class: "pl-meter", tabindex: "0" },
				ringEl,
				h("span", { class: "pl-meter-text" },
					h("span", { class: "pl-meter-v" }, h("span", { class: "pl-meter-k" }, KIND_WORD[m.kind]), fmtPct(m.used)),
					h("span", { class: "pl-meter-r" }, [m.source === "local" ? "NeoProxy cap" : null, m.resetsAt ? `resets in ${fmtIn(m.resetsAt)}` : null].filter(Boolean).join(" · "))),
				h("span", { class: "pl-meter-tip" }, `${fmtPct(m.used)} of the ${m.kind} window used, ${m.source === "provider" ? "reported by the provider" : "counted by NeoProxy against its routing cap"}`));
		}));
	}

	// One short phrase: how busy it was today, else when it was last used.
	function trafficCell(a) {
		const u = a.usage ?? a.face?.usage;
		if (!u) return h("span", { class: "ink-3" }, "–");
		if (u.requests24h) return h("span", { class: "pl-traffic" }, h("span", { class: "pl-main", title: u.lastUsedAt ? `Last used ${fmtAgo(u.lastUsedAt)}` : null }, `${fmtCompact(u.requests24h)} ${u.requests24h === 1 ? "request" : "requests"}`), u.failed24h ? h("span", { class: "pl-bad" }, `${u.failed24h} failed`) : null);
		return h("span", { class: "pl-traffic" }, h("span", { class: "pl-sub" }, u.lastUsedAt ? `Last used ${fmtAgo(u.lastUsedAt)}` : "Never used"));
	}

	function accountRow(a) {
		const s = sev(a);
		const t = testLine(a);
		const pending = ui.pending.has(a.id) || ui.tests.get(a.id)?.running;
		const row = h("div", {
			class: "pool-acct", role: "button", tabindex: "0", "data-acct": a.id, "aria-label": `${labelText(a)}, ${accountHealth(a).text}. Open details`, "aria-expanded": String(selectedId === a.id), "aria-selected": String(selectedId === a.id), "data-sev": s === "ok" ? null : s,
			onClick: () => toggleDrawer(a.id),
			onKeydown: (e) => { if ((e.key === "Enter" || e.key === " ") && e.target === row) { e.preventDefault(); toggleDrawer(a.id, { focus: "drawer" }); } },
		},
			h("div", { class: "pl-id" },
				h("div", { class: `pl-label ${a.kind === "apiKey" && a.label.startsWith("…") ? "mono" : ""}`, title: a.label }, labelText(a)),
				h("div", { class: "pl-sub" }, kindLine(a)),
				t ? h("div", { class: `pl-test ${t.kind}` }, t.text) : null),
			h("div", { class: "pl-cell pl-meter-cell" }, meterCell(a)),
			h("div", { class: "pl-cell pl-health", title: accountHealth(a).hint ?? "" }, healthStatus(a)),
			h("div", { class: "pl-cell pl-traffic-cell" }, trafficCell(a)),
			h("div", { class: "pl-cell pl-owner", title: person(a.ownerId) }, person(a.ownerId)),
			h("div", { class: "pl-acts", onClick: (e) => e.stopPropagation(), onKeydown: (e) => e.stopPropagation() },
				expired(a)
					? btn("Sign in again", { icon: "arrowRight", cls: "primary sm", title: `Sign in again to ${provOf(a)?.name ?? "the provider"}`, onClick: () => signInAgain(a) })
					: btn(pending && ui.tests.get(a.id)?.running ? "Testing…" : "Test", { icon: "play", cls: "outline sm", title: `Test ${labelText(a)}`, disabled: !!pending, onClick: () => runTest(a) }),
				(() => {
					const more = iconBtn("x", `More actions for ${labelText(a)}`, () => openMenu(more, actionsFor(a).map((x) => (x === "-" ? x : { label: x.label, icon: x.icon, danger: x.danger, onClick: x.run }))));
					replace(more, svg(MORE, 16)); more.setAttribute("aria-haspopup", "menu"); return more;
				})()),
		);
		return row;
	}

	// ---------- the provider groups ----------
	function groupsFor(list) {
		const q = ui.query.trim().toLowerCase();
		const out = [];
		for (const p of providers()) {
			let accs = list.filter((a) => a.providerId === p.id);
			if (!admin && !accs.length) continue;
			const providerMatch = q && [p.name, p.vendor].some((s) => s?.toLowerCase().includes(q));
			if (q && !providerMatch) accs = accs.filter((a) => [a.label, labelText(a), a.plan, person(a.ownerId), kindLine(a)].some((s) => s?.toLowerCase().includes(q)));
			if (ui.filter === "attention") accs = accs.filter(attention);
			if ((q && !providerMatch && !accs.length) || (ui.filter === "attention" && !accs.length)) continue;
			accs.sort((x, y) => SEVERITY[sev(x)] - SEVERITY[sev(y)] || x.label.localeCompare(y.label));
			out.push({ p, accs, worst: accs.length ? Math.min(...accs.map((a) => SEVERITY[sev(a)])) : 9 });
		}
		out.sort((x, y) => x.worst - y.worst || x.p.name.localeCompare(y.p.name));
		return out;
	}

	function providerBlock({ p, accs }) {
		const shut = shutGroups();
		const open = !shut.has(p.id);
		const kind = p.kind === "oauth" ? "sign-in" : p.kind === "neopi-gateway" ? "via NeoPi" : "API key";
		const bad = accs.filter(attention).length;
		const head = h("div", { class: "pool-prov-head" },
			h("button", { type: "button", class: "pool-prov-toggle", "data-prov": p.id, "aria-expanded": String(open), "aria-label": `${p.name}, ${plural(accs.length, "account")}`, onClick: () => {
				shut.has(p.id) ? shut.delete(p.id) : shut.add(p.id);
				paint();
				// The repaint replaces this button; keep keyboard focus on its successor.
				document.querySelector(`.pool-prov-toggle[data-prov="${CSS.escape(p.id)}"]`)?.focus({ preventScroll: true });
			} },
				h("span", { class: "pool-chev" }, icon("chevronDown", 14)),
				(() => { const dot = h("span", { class: "prov-dot" }); hueVars(dot, p); dot.style.width = dot.style.height = "10px"; return dot; })(),
				h("span", { class: "pool-prov-name" }, p.name),
				p.vendor && p.vendor.toLowerCase() !== p.name.toLowerCase() ? h("span", { class: "pool-prov-vendor" }, p.vendor) : null,
				tag(kind),
				h("span", { class: "pool-prov-count" }, accs.length ? plural(accs.length, "account") : "no accounts yet"),
				bad ? badge(`${bad} need${bad === 1 ? "s" : ""} attention`, "bad") : null),
			admin ? h("span", { class: "pool-serve" }, h("span", { class: `pool-serve-l ${p.exposed ? "" : "off"}`, "aria-hidden": "true" }, p.exposed ? "Served" : [h("span", { class: "l-long" }, "Hidden from users"), h("span", { class: "l-short" }, "Hidden")]), toggle(p.exposed, async (v) => {
				try { await neo.setProviderExposed(p.id, v); toast(v ? `${p.name} is served again` : `${p.name} hidden from users`); }
				catch (e) { toast(explain(e), "bad"); throw e; }
			}, `Serve ${p.name}`, { showLabel: false })) : null);
		return h("section", { class: `pool-prov ${open ? "open" : ""}`, "aria-label": p.name },
			head,
			open && accs.length ? h("div", { class: "pool-accts" }, accs.map(accountRow)) : null);
	}

	// ---------- the account drawer ----------
	function drawnBy(a) {
		const p = provOf(a);
		const out = [];
		for (const vm of S.vms) {
			const places = [];
			vm.stages.forEach((s, i) => {
				const pid = s.model.split("/")[0];
				if (pid !== p?.id && canonicalProviderId(pid) !== p?.canonicalId) return;
				if (s.accounts !== "all" && !s.accounts.includes(a.id)) return;
				places.push({ s, i });
			});
			if (places.length) out.push({ vm, places });
		}
		return out;
	}
	const firstMeterKey = (a) => `cap-${(a.windows ?? []).find((w) => w.capacity)?.kind ?? "day"}`;

	const D = { el: null, titles: null, acts: null, body: null, id: null };
	const outside = (e) => {
		if (!D.el || D.el.contains(e.target) || e.target.closest?.(".pool-acct, .dialog-backdrop, .picker-pop, .pool-menu, .toast")) return;
		closeDrawer({ restore: false });
	};
	const escKey = (e) => {
		if (e.key !== "Escape" || !D.el || e.defaultPrevented || closeMenuFn || document.querySelector(".dialog-backdrop, .picker-pop")) return;
		e.preventDefault();
		closeDrawer();
	};
	function ensureDrawer() {
		if (D.el) return;
		D.titles = h("div", { class: "pd-titles" });
		D.acts = h("div", { class: "pd-acts" });
		D.body = h("div", { class: "pd-body" });
		D.el = h("aside", { class: "pool-drawer", role: "region", "aria-labelledby": "pd-title", tabindex: "-1" },
			h("div", { class: "pd-head" }, D.titles, D.acts, iconBtn("x", "Close details (Esc)", () => closeDrawer())),
			D.body);
		document.body.append(D.el);
		document.body.classList.add("has-pool-drawer");
		requestAnimationFrame(() => D.el?.classList.add("open"));
		document.addEventListener("pointerdown", outside, true);
		document.addEventListener("keydown", escKey, true);
	}
	function closeDrawer({ restore = true } = {}) {
		const id = selectedId;
		selectedId = null;
		if (D.el) {
			const el = D.el;
			el.classList.remove("open");
			setTimeout(() => el.remove(), 200);
			D.el = null;
			D.id = null;
			document.body.classList.remove("has-pool-drawer");
			document.removeEventListener("pointerdown", outside, true);
			document.removeEventListener("keydown", escKey, true);
		}
		if (location.hash.startsWith("#/pool/")) history.replaceState(null, "", "#/pool");
		if (!disposed) paint();
		if (restore && id) {
			const row = document.querySelector(`.pool-acct[data-acct="${CSS.escape(id)}"]`);
			row?.focus({ preventScroll: true });
			row?.scrollIntoView({ block: "nearest" });
		}
	}
	function openDrawer(id, { focus } = {}) {
		selectedId = id;
		history.replaceState(null, "", `#/pool/${encodeURIComponent(id)}`);
		paint();
		if (!D.el) return;
		// From the keyboard, land on the drawer's first action so the focus ring shows where you are.
		const target = focus === "drawer" ? D.el.querySelector(".pd-acts button") ?? D.el : focus ? D.el.querySelector(`[data-focus="${CSS.escape(focus)}"]`) : null;
		target?.focus({ preventScroll: true });
		if (target && focus !== "drawer") target.scrollIntoView({ block: "center" });
	}
	const toggleDrawer = (id, opts) => (selectedId === id ? closeDrawer() : openDrawer(id, opts));

	const group = (title, help, ...rows) => h("section", { class: "pd-group" },
		h("h3", { class: "pd-group-title" }, title),
		help ? h("p", { class: "pd-group-help" }, help) : null,
		h("div", { class: "pd-list" }, rows));
	const setRow = (label, help, control, extra) => h("div", { class: "pd-row" },
		h("div", { class: "pd-row-text" }, h("div", { class: "pd-row-label" }, label), help ? h("div", { class: "pd-row-help" }, help) : null),
		control ? h("div", { class: "pd-row-ctl" }, control) : null,
		extra ?? null);

	// One compact row per window: the reading routing uses (the provider's quota
	// when it reports this window, else the NeoProxy cap; neoroute windowFor) with
	// its reset. A cap that is only a backup shows as one muted line. The token
	// input appears only after Add cap or Edit cap; Enter saves.
	function windowRow(a, kind) {
		const pw = (a.providerWindows ?? []).find((x) => x.kind === kind && x.known);
		const w = (a.windows ?? []).find((x) => x.kind === kind && x.capacity);
		const drafts = ui.drafts.get(a.id) ?? {};
		const saved = w ? shortTokens(w.capacity) : "";
		const draft = () => drafts[kind];
		const setDraft = (patch) => {
			drafts[kind] = { cap: draft()?.cap ?? saved, start: draft()?.start ?? null, ...patch };
			ui.drafts.set(a.id, drafts);
			paintEdit();
		};
		const provName = provOf(a)?.name ?? "the provider";
		const capInput = h("input", { class: "input pd-cap-input", type: "text", inputmode: "decimal", autocomplete: "off", spellcheck: "false", placeholder: "e.g. 2M", value: draft()?.cap ?? saved, "data-focus": `cap-${kind}`, "aria-label": `${KIND_LONG[kind]}: NeoProxy cap in tokens`,
			onInput: (e) => setDraft({ cap: e.target.value }),
			onKeydown: (e) => { if (e.key === "Enter") { e.preventDefault(); save(); } if (e.key === "Escape" && draft()) { e.preventDefault(); e.stopPropagation(); cancel(); } },
		});
		const shown = pw ?? (w?.known ? w : null);
		const resets = (x) => (x?.resetsAt ? `resets in ${fmtIn(x.resetsAt)}` : null);
		const join = (parts) => parts.filter(Boolean).flatMap((x, i) => (i ? [" · ", x] : [x]));
		const reading = h("div", { class: "pd-row-help" });
		const capBtn = h("span", { class: "pd-row-ctl" });
		const edit = h("div", { class: "pd-edit" });
		const flashKey = `${a.id}:${kind}`;
		const row = h("div", { class: `pd-row pd-win ${ui.flash === flashKey ? "just-saved" : ""}` },
			shown ? ring({ used: shown.used, size: 28 }) : null,
			h("div", { class: "pd-row-text" }, h("div", { class: "pd-row-label" }, KIND_LONG[kind]), reading),
			capBtn, edit);
		const startOf = () => (draft()?.start ? new Date(draft().start).getTime() : w?.startsAt ?? Date.now());
		const open = () => { setDraft({}); capInput.focus(); };
		const cancel = () => { delete drafts[kind]; capInput.value = saved; paintEdit(); capBtn.querySelector("button")?.focus(); };
		function paintEdit() {
			const d = draft();
			const since = () => h("button", { type: "button", class: "pd-inline", "data-focus": `from-${kind}`, title: "Change when this window starts", onClick: () => { setDraft({ start: localInput(w.startsAt) }); edit.querySelector(".pd-start")?.focus(); } }, `since ${whenWords(w.startsAt)}`);
			reading.replaceChildren(
				...join(pw
					? [`${fmtPct(pw.used)} used, reported by ${provName}`, resets(pw)]
					: w ? [`${fmtCompact(w.usedTokens ?? 0)} of ${saved} tokens · NeoProxy cap`, resets(w), d ? null : since()]
						: ["No limit"]),
				pw && w ? h("div", { class: "pd-backup" }, `NeoProxy cap ${saved} (${fmtCompact(w.usedTokens ?? 0)} counted): backup, used only if ${provName} stops reporting`) : "");
			const cb = d ? null : btn(w ? "Edit cap" : "Add cap", { cls: `${w ? "outline" : "ghost"} sm`, onClick: open, title: `${w ? "Edit" : "Add"} the ${KIND_NOUN[kind].toLowerCase()} NeoProxy cap` });
			if (cb) cb.dataset.focus = `cap-${kind}`;
			capBtn.replaceChildren(cb ?? "");
			row.classList.toggle("dirty", !!d);
			if (!d) { edit.replaceChildren(); return; }
			const n = parseTokens(d.cap);
			const bad = !(n > 0);
			const startInput = h("input", { class: "input pd-start", type: "datetime-local", value: localInput(startOf()), "data-focus": `start-${kind}`, "aria-label": `${KIND_LONG[kind]}: window starts`, onInput: (e) => { drafts[kind] = { ...drafts[kind], start: e.target.value }; }, onKeydown: (e) => { if (e.key === "Enter") { e.preventDefault(); save(); } } });
			// The input stays put (moving it would drop focus mid-typing); only the rest repaints.
			if (!edit.contains(capInput)) edit.replaceChildren(h("label", { class: "pd-cap" }, h("span", { class: "pd-unit" }, "NeoProxy cap"), capInput, h("span", { class: "pd-unit" }, "tokens")), h("span", { class: "pd-edit-rest" }));
			edit.querySelector(".pd-edit-rest").replaceChildren(
				h("div", { class: `pd-edit-note ${bad ? "bad" : ""}` }, bad
					? (d.cap.trim() ? "Type a number of tokens, like 500k or 2M." : w ? "A cap can't be removed here yet; set a larger one instead." : "Type a number of tokens, like 2M.")
					: `${fmtInt(n)} tokens ${KIND_LONG[kind].toLowerCase()}, counted from`),
				bad ? "" : startInput,
				h("span", { class: "spacer" }),
				btn("Cancel", { cls: "ghost sm", onClick: cancel }),
				btn("Save", { cls: "primary sm", disabled: bad, onClick: save }));
		}
		async function save() {
			const d = draft();
			const capacityTokens = parseTokens(d?.cap ?? saved);
			if (!d || !(capacityTokens > 0) || !Number.isSafeInteger(capacityTokens)) { capInput.focus(); return; }
			const startsAt = startOf();
			if (!Number.isFinite(startsAt)) { toast("Pick a valid start time.", "bad"); return; }
			try {
				await engine.put(`${accountPath(a.id)}/windows/${encodeURIComponent(kind)}`, { capacityTokens, startsAt });
				delete drafts[kind];
				ui.flash = flashKey;
				setTimeout(() => { if (ui.flash === flashKey) ui.flash = null; }, 2500);
				invalidate("vault");
				toast(`${KIND_NOUN[kind]} NeoProxy cap saved: ${fmtCompact(capacityTokens)} tokens`);
				await reload();
			} catch (e) { toast(explain(e), "bad"); }
		}
		paintEdit();
		return row;
	}

	function drawerContent(a) {
		const p = provOf(a);
		const hs = accountHealth(a);
		const t = testLine(a, { full: true });
		const acts = a.face?.actions ?? [];
		const pending = ui.pending.has(a.id) || ui.tests.get(a.id)?.running;
		const drawn = drawnBy(a);
		const canReassign = admin && acts.includes("reassign") && contributors().length > 1;

		const titles = [
			h("h2", { id: "pd-title" }, labelText(a)),
			h("div", { class: "pd-sub" }, (() => { const dot = h("span", { class: "prov-dot" }); hueVars(dot, p); return dot; })(), `${p?.name ?? "Provider"} · ${kindLine(a)}`),
		];
		const testBtn = btn(ui.tests.get(a.id)?.running ? "Testing…" : "Test", { icon: "play", cls: "outline sm", disabled: !!pending, onClick: () => runTest(a), title: "Send one short request through this account" });
		const headActs = expired(a) ? [btn("Sign in again", { icon: "arrowRight", cls: "primary sm", onClick: () => signInAgain(a) }), testBtn] : [testBtn];

		const status = h("div", { class: `pd-status ${hs.kind}` },
			h("div", { class: "pd-status-main" }, healthStatus(a), h("span", { class: "pd-status-text" }, hs.kind === "ok" ? trafficSentence(a) : fixHint(a) ?? hs.hint)),
			t ? h("div", { class: `pd-test ${t.kind}` }, t.text) : h("div", { class: "pd-test" }, "Not tested yet. Test sends one short request to check the key or sign-in."),
			h("div", { class: "pd-status-acts" },
				expired(a) && isCopy(a) ? btn("Copy again from NeoPi", { icon: "download", cls: "outline sm", onClick: reimport }) : null,
				btn("Test payload…", { icon: "scroll", cls: "outline sm", onClick: () => openTestDialog(a, { provider: p, onChange: () => reload() }) }),
				a.face?.probeConfig ? h("span", { class: "pd-recipe" }, "Test uses ", h("code", null, a.face.probeConfig.model), ` on ${a.face.probeConfig.endpoint}`) : null));

		const settings = [
			acts.includes("disable") ? setRow("In use", isDisabled(a) ? "Off: no virtual model draws from it until you turn it back on." : "On: the virtual models that list it may draw from it.",
				toggle(!isDisabled(a), (v) => setDisabled(a, !v), `${labelText(a)} in use`, { showLabel: false, disabled: !!pending })) : null,
			setRow("Contributor", canReassign ? "Whose access this is. Owners only see and test what they contribute." : null,
				canReassign ? select(contributors().map((u) => ({ value: u.id, label: u.name || u.handle })), a.ownerId ?? "", async (ownerId) => {
					try { await neo.updateAccess(a.id, { ownerId }); toast("Contributor changed"); } catch (e) { toast(explain(e), "bad"); }
					reload();
				}, { "aria-label": "Contributor" }) : h("span", { class: "ink-2" }, person(a.ownerId))),
			a.kind !== "apiKey" ? setRow("Sign-in", { auto: "NeoProxy refreshes it automatically.", locked: "A copy of NeoPi's sign-in. NeoProxy never refreshes it, so NeoPi's own keeps working; the copy stops when it expires.", neopi: "NeoPi refreshes it; NeoProxy only borrows it." }[a.face?.refresh] ?? null,
				acts.includes("refresh") ? btn("Refresh now", { icon: "refresh", cls: "outline sm", disabled: !!pending, onClick: () => act(a, () => neo.refreshAccess(a.id), "Sign-in refreshed") }) : null) : null,
			reportsUsage(a) && !expired(a) ? setRow("Usage report", "Ask the provider for the latest numbers now instead of waiting for the next check.",
				btn("Refresh usage", { icon: "activity", cls: "outline sm", disabled: !!pending, onClick: () => act(a, () => neo.refreshQuota(a.id), "Usage refreshed") })) : null,
		].filter(Boolean);

		const prefix = `${a.providerId}/`;
		const ids = modelsOf(a).map((m) => (m.startsWith(prefix) ? m.slice(prefix.length) : m));

		const body = [
			status,
			group("Usage and limits", expired(a)
				? "Usage unavailable until signed in again."
				: a.kind === "apiKey" && !(a.providerWindows ?? []).length
					? "API keys don't report usage. A NeoProxy cap makes NeoProxy count the tokens it sends here and stop virtual models at the cap."
					: h("span", null, "Provider quota: usage the provider reports.", h("br"), "NeoProxy cap: a token limit NeoProxy counts itself, used only when the provider doesn't report that window."),
				KINDS.map((k) => windowRow(a, k))),
			group("Settings", null, settings),
			group("Virtual models that draw from it", null, drawn.length
				? drawn.map(({ vm, places }) => h("button", { type: "button", class: "pd-row pd-link", onClick: () => ctx.navigate(`virtual-models/${vm.id}`) },
					h("div", { class: "pd-row-text" },
						h("div", { class: "pd-row-label" }, servedName(vm.served ?? `neoproxy/${vm.name}`, { virtual: true })),
						h("div", { class: "pd-row-help" }, places.map(({ i, s }) => `${i === 0 ? "first choice" : `fallback ${i}`}: ${s.model.split("/").slice(1).join("/") || s.model}`).join(" · "))),
					icon("chevronRight", 14)))
				: h("div", { class: "pd-row pd-none" }, "No virtual model draws from it yet. Direct provider names (neoproxy/<provider>/<model>) can still reach it.")),
			ids.length ? group("Models", null, h("details", { class: "pd-row pd-models" },
				h("summary", null, h("span", { class: "pd-row-label" }, `${plural(ids.length, "model")} available`), h("span", { class: "pd-row-help" }, ids.slice(0, 3).join(", ") + (ids.length > 3 ? ", …" : ""))),
				h("div", { class: "pd-model-list" }, ids.map((m) => h("code", { title: m }, m))))) : null,
			group("Details", null, h("div", { class: "pd-row pd-kv" }, kv([
				["Provider", p ? `${p.name}${p.vendor && p.vendor.toLowerCase() !== p.name.toLowerCase() ? ` · ${p.vendor}` : ""}` : a.providerId],
				["Kind", { subscription: "Signed-in account", apiKey: "API key", imported: "Imported from NeoPi", neopi: "Shared from a NeoPi", file: "Credential file" }[a.kind] ?? a.kind],
				["Added", a.face?.createdAt ? fmtDate(a.face.createdAt) : "–"],
				["Last used", (a.usage ?? a.face?.usage)?.lastUsedAt ? fmtAgo((a.usage ?? a.face.usage).lastUsedAt) : "never"],
				["Id", copyLine(a.id, "account id")],
			]))),
			acts.includes("remove") ? h("div", { class: "pd-danger" },
				h("div", { class: "pd-row-text" }, h("div", { class: "pd-row-label" }, "Remove from the pool"), h("div", { class: "pd-row-help" }, a.kind === "apiKey" ? "Deletes the key from NeoProxy. Revoke it at the provider too." : "Virtual models stop drawing from it at once.")),
				btn("Remove…", { icon: "trash", cls: "danger sm", disabled: !!pending, onClick: () => removeAccount(a) })) : null,
		];
		return { titles, headActs, body };
	}

	function paintDrawer() {
		const a = selectedId ? accounts().find((x) => x.id === selectedId) : null;
		if (!a) { if (D.el) closeDrawer({ restore: false }); else selectedId = null; return; }
		const fresh = !D.el || D.id !== a.id;
		ensureDrawer();
		const active = document.activeElement;
		const focusKey = D.el.contains(active) ? active.dataset?.focus : null;
		const hadFocus = D.el.contains(active) && active !== D.el;
		const caret = focusKey && typeof active.selectionStart === "number" ? active.selectionStart : null;
		const scroll = fresh ? 0 : D.body.scrollTop;
		const { titles, headActs, body } = drawerContent(a);
		replace(D.titles, ...titles);
		replace(D.acts, ...headActs);
		replace(D.body, ...body);
		D.id = a.id;
		D.el.setAttribute("aria-label", `${labelText(a)} details`);
		D.body.scrollTop = scroll;
		// Every control gets a stable key so focus survives the repaint.
		for (const el of D.el.querySelectorAll("button, input, select, summary")) if (!el.dataset.focus) el.dataset.focus = (el.getAttribute("aria-label") || el.textContent || "").trim().slice(0, 60);
		const back = focusKey ? D.el.querySelector(`[data-focus="${CSS.escape(focusKey)}"]`) : null;
		if (back) {
			back.focus({ preventScroll: true });
			if (caret !== null) try { back.setSelectionRange(caret, caret); } catch { /* not a text field */ }
		} else if (hadFocus) {
			D.el.focus({ preventScroll: true });
		}
	}

	// ---------- page ----------
	const toolbarHost = h("div"), listHost = h("div", { class: "pool-list" }), sourcesHost = h("div");
	let segHost, summaryHost;

	const collapseHost = h("span");
	function buildToolbar() {
		const box = search("Find a provider, email or key", (v) => { ui.query = v; ui.shutFiltered.clear(); paint(); }, { value: ui.query, "aria-label": "Find in the pool" });
		// Escape on an empty search also drops the attention filter: one key back to everything.
		box.querySelector("input").addEventListener("keydown", (e) => { if (e.key === "Escape" && !e.target.value && ui.filter !== "all") { ui.filter = "all"; ui.shutFiltered.clear(); paint(); } });
		segHost = h("span");
		summaryHost = h("span", { class: "pool-summary", role: "status", "aria-live": "polite" });
		return h("div", { class: "pool-toolbar" }, box, segHost, h("span", { class: "spacer" }), summaryHost, collapseHost);
	}
	function updateToolbar() {
		const accs = accounts();
		const n = accs.filter(attention).length;
		replace(segHost, seg([{ value: "all", label: "All" }, { value: "attention", label: n ? `Needs attention (${n})` : "Needs attention" }], ui.filter, (v) => { ui.filter = v; ui.shutFiltered.clear(); paint(); }));
		replace(summaryHost, plural(accs.length, "account"), " · ", plural(new Set(accs.map((a) => a.providerId)).size, "provider"), n ? [" · ", h("span", { class: "pl-bad" }, `${n} need${n === 1 ? "s" : ""} attention`)] : accs.length ? " · all healthy" : "");
		const shut = shutGroups();
		const anyOpen = providers().some((p) => !shut.has(p.id));
		replace(collapseHost, btn(anyOpen ? "Collapse all" : "Expand all", { cls: "ghost sm", icon: anyOpen ? "chevronUp" : "chevronDown", onClick: () => { if (anyOpen) for (const p of providers()) shut.add(p.id); else shut.clear(); paint(); } }));
	}

	function emptyState() {
		const kinds = ["subscription", "apiKey", ...(admin ? ["neopi", "imported"] : []), "file"];
		return card({ cls: "pool-empty", body: [
			empty({ icon: "shield", title: admin ? "Your pool is empty" : "You haven't contributed anything yet", hint: admin ? "Add provider access to start serving models. Pick whichever way suits the account you have." : "Provider access you add here is pooled with the rest of your organization.", action: btn("Add provider access", { icon: "plus", cls: "primary", onClick: () => openAdd() }) }),
			h("div", { class: "pool-ways" }, kinds.map((k) => h("button", { type: "button", class: "pool-way", onClick: () => openAdd(k === "subscription" || k === "apiKey" ? {} : { method: k }) }, h("span", { class: "pool-way-title" }, KIND_COPY[k].title), h("span", { class: "pool-way-sub" }, KIND_COPY[k].summary)))),
		] });
	}

	function sourcesCard() {
		if (!admin || !S.sources.length) return null;
		const run = async (fn, ok) => { try { const r = await fn(); toast(typeof ok === "function" ? ok(r) : ok); } catch (e) { toast(explain(e), "bad"); } };
		return card({
			title: "Shared NeoPi connections",
			sub: "These accounts live in a NeoPi. It keeps the credentials and refreshes them; if it goes offline, its accounts pause until it's back.",
			flush: true,
			body: h("div", { class: "list" }, S.sources.map((s) => {
				const ok = s.status === "connected";
				return h("div", { class: "li" },
					h("div", { class: "lt" },
						h("div", { class: "main row" }, s.label, ok ? badge("connected", "ok") : s.enabled === false ? badge("disabled") : badge(s.status || "unknown", "warn")),
						h("div", { class: "sub" }, [s.url, `${plural(s.accountCount ?? 0, "account")}, ${plural(s.modelCount ?? 0, "model")}`, s.checkedAt ? `checked ${fmtAgo(s.checkedAt)}` : "not checked yet"].join(" · "))),
					btn("Sync now", { icon: "refresh", cls: "outline sm", disabled: s.enabled === false, onClick: () => run(() => neo.syncSource(s.id), "Source synced") }),
					btn(s.enabled === false ? "Enable" : "Disable", { cls: "outline sm", onClick: () => run(() => neo.saveSource({ id: s.id, label: s.label, url: s.url, ownerId: s.ownerId, enabled: s.enabled === false }), s.enabled === false ? "Connection enabled" : "Connection disabled") }),
					btn("Disconnect", { cls: "danger sm", onClick: async () => {
						if (await confirm({ title: `Disconnect ${s.label}?`, text: "Requests stop going through this NeoPi. Its accounts leave the pool; usage history stays. NeoPi keeps its own credentials.", confirmLabel: "Disconnect", danger: true })) await run(() => neo.deleteSource(s.id), "Disconnected");
					} }));
			})),
		});
	}

	function paint() {
		if (disposed) return;
		const all = accounts();
		const hasList = providers().length && (admin || all.length);
		toolbarHost.hidden = !(hasList && all.length);
		updateToolbar();
		if (!hasList) replace(listHost, emptyState());
		else {
			// Keep the focused row or control where it was across the repaint.
			const active = document.activeElement;
			const focusKey = listHost.contains(active) ? active.dataset.focus : null;
			const groups = groupsFor(all);
			const filtered = ui.filter === "attention" && !ui.query;
			const oneOwner = new Set(all.map((a) => a.ownerId)).size <= 1;
			listHost.classList.toggle("pool-table", groups.length > 0);
			listHost.classList.toggle("no-owner", oneOwner);
			replace(listHost, ...(groups.length
				? [h("div", { class: "pool-cols", "aria-hidden": "true" }, h("span", null, "Account"), h("span", null, "Usage"), h("span", null, "Health"), h("span", null, "Activity"), oneOwner ? null : h("span", null, "Contributor"), h("span")), ...groups.map(providerBlock)]
				: [card({ body: empty({ icon: "search", title: filtered ? "Nothing needs attention" : "Nothing matches", hint: filtered ? "Every account is signed in, within its limits and passing its tests." : "Try a provider name, an email or the end of a key." }) })]));
			if (!all.length) listHost.prepend(emptyState());
			for (const el of listHost.querySelectorAll(".pool-acct, .pool-acct button, .pool-acct .pl-meter, .pool-prov-head button")) {
				const scope = el.closest("[data-acct]")?.dataset.acct ?? el.closest(".pool-prov")?.getAttribute("aria-label") ?? "";
				el.dataset.focus = el.classList.contains("pool-acct") ? `row:${scope}` : `${scope}:${el.classList.contains("pool-prov-toggle") ? "toggle" : (el.getAttribute("aria-label") || el.textContent).trim()}`;
			}
			if (focusKey) listHost.querySelector(`[data-focus="${CSS.escape(focusKey)}"]`)?.focus({ preventScroll: true });
		}
		replace(sourcesHost, sourcesCard() ?? "");
		paintDrawer();
	}

	try { await fetchAll(); }
	catch (e) { ctx.root.lastChild.replaceWith(errorState(e, () => ctx.rerender())); return; }
	ctx.root.lastChild.remove();
	toolbarHost.append(buildToolbar());
	pageHost.append(toolbarHost, listHost, sourcesHost);
	paint();
	if (selectedId && D.el) D.el.focus({ preventScroll: true });

	const off = onChange((what) => { if (what === "vault") reload(); });
	timer = setInterval(() => { if (!document.hidden) reload(); }, 60_000);
	return () => {
		disposed = true; off(); clearInterval(timer); closeMenu();
		if (D.el) { D.el.remove(); D.el = null; document.body.classList.remove("has-pool-drawer"); document.removeEventListener("pointerdown", outside, true); document.removeEventListener("keydown", escKey, true); }
	};
}
