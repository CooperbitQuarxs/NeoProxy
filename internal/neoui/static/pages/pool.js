// Pool: every sign-in, API key and shared NeoPi that NeoProxy can draw on,
// grouped by provider. Rows show the one meter per window (provider-reported,
// local, or none), health, traffic and owner; the inspector holds the detail.
// Accounts that need a person (expired, failing, spent) sort first.

import { h, svg, replace, icon, fmtAgo, fmtIn, fmtPct, fmtCompact, fmtDate, plural } from "../lib/dom.js";
import { neo, engine, invalidate, onChange } from "../lib/client.js";
import { accountHealth, canonicalProviderId, humanMessage } from "../lib/providers.js";
import { pageHead, card, btn, iconBtn, badge, status, tag, toggle, search, seg, select, empty, spinner, errorState, inspector, section, kv, servedName, dialog, confirm, toast, field, input, ring, meterRing, healthStatus, sheet } from "../lib/components.js";
import { openTestDialog } from "./pool-test.js";
import { openAddAccess, KIND_COPY, hueVars } from "./pool-add.js";

const KINDS = ["5h", "day", "week", "month"];
const KIND_WORD = { "5h": "5h", day: "day", week: "week", month: "month" };
const SEVERITY = { bad: 0, warn: 1, ok: 2, muted: 3 };
const MORE = '<circle cx="12" cy="5" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="12" cy="19" r="1"/>';
const accountPath = (id) => `/v8/neo/vault/accounts/${encodeURIComponent(id)}`;

// Survives re-renders so a repaint after any change keeps the person's place.
// `collapsed` is the unfiltered layout. A search or the attention filter opens
// every matching group; groups collapsed while it is active go in `shutFiltered`,
// which empties whenever the query or filter changes.
const ui = { query: "", filter: "all", collapsed: new Set(), shutFiltered: new Set(), tests: new Map(), pending: new Set() };
const shutGroups = () => (ui.query.trim() || ui.filter === "attention" ? ui.shutFiltered : ui.collapsed);

const notYet = (e) => e?.status === 404 && typeof e.body === "string";
const explain = (e) => (notYet(e) ? "This server doesn't support that yet." : e?.message || String(e));

export async function render(ctx) {
	const admin = ctx.session.role === "admin";
	const me = ctx.session.userId;
	const S = { vault: null, vms: [], users: [], core: null, sources: [] };
	let selectedId = ctx.params[0] ?? null;
	let disposed = false, loading = 0, closeMenuFn = null, timer = 0;

	const pageHost = h("div", { class: "pool" });
	ctx.root.append(
		pageHead({
			title: "Pool",
			desc: admin
				? "Everything NeoProxy can draw on: signed-in subscriptions, API keys and shared NeoPi accounts. Virtual models decide which of them answers a request."
				: "The provider access you contribute. It is pooled with the rest of your organization; you can test it, pause it or remove it here.",
		}),
		pageHost,
		spinner(),
	);

	// ---------- data ----------
	async function fetchAll() {
		const [vault, vms, users, core, sources] = await Promise.all([
			neo.vault(),
			neo.virtualModels().catch(() => S.vms),
			admin ? neo.users().catch(() => S.users) : S.users,
			admin ? neo.core().catch(() => null) : null,
			admin ? neo.sources().catch(() => []) : [],
		]);
		Object.assign(S, { vault, vms, users, core, sources: Array.isArray(sources) ? sources : (sources?.sources ?? []) });
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
		const kind = { subscription: "Signed in", apiKey: "API key", imported: "Imported from NeoPi", neopi: src ? `Shared via ${src.label}` : "Shared from NeoPi", file: "Credential file" }[a.kind] ?? "Account";
		const plan = a.plan && !/^subscription$/i.test(a.plan) ? a.plan : "";
		return a.kind === "apiKey" ? plan || kind : [plan, kind].filter(Boolean).join(" · ");
	}
	const labelText = (a) => (a.kind === "apiKey" && a.label.startsWith("…") ? `Key ${a.label}` : a.label);

	// ---------- shared pieces ----------
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

	function actionsFor(a) {
		const acts = a.face?.actions ?? [];
		const p = provOf(a);
		const disabled = a.face?.health === "disabled";
		const reportsUsage = ["subscription", "imported"].includes(a.kind) && (a.providerWindows?.length || ["claude", "codex"].includes(p?.canonicalId));
		return [
			{ id: "test", label: "Test this account", icon: "play", run: () => runTest(a) },
			{ id: "payload", label: "Test payload…", icon: "terminal", run: () => openTestDialog(a, { provider: p, onChange: () => reload() }) },
			a.state === "expired" || a.face?.health === "expired" ? { id: "signin", label: "Sign in again", icon: "arrowRight", run: () => openAdd({ provider: p?.canonicalId, method: "subscription" }) } : null,
			acts.includes("refresh") ? { id: "refresh", label: "Refresh sign-in", icon: "refresh", run: () => act(a, () => neo.refreshAccess(a.id), "Sign-in refreshed") } : null,
			reportsUsage ? { id: "usage", label: "Refresh usage", icon: "activity", run: () => act(a, () => neo.refreshQuota(a.id), "Usage refreshed") } : null,
			{ id: "meter", label: "Configure local meter", icon: "sliders", run: () => meterDialog(a) },
			acts.includes("disable") ? { id: "toggle", label: disabled ? "Enable" : "Disable", icon: "power", run: () => act(a, () => neo.updateAccess(a.id, { disabled: !disabled }), disabled ? "Account enabled" : "Account disabled. No stage draws from it.") } : null,
			acts.includes("remove") ? { id: "remove", label: "Remove…", icon: "trash", danger: true, run: () => removeAccount(a) } : null,
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
		if (selectedId === a.id) selectedId = null;
		await act(a, () => neo.removeAccess(a.id), "Removed from the pool");
	}

	function meterDialog(a) {
		const existing = (a.windows ?? []).find((w) => w.known && w.capacity);
		let kind = existing?.kind ?? "day", capacity = existing?.capacity ? String(existing.capacity) : "";
		let startsAt = new Date(Date.now() - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 16);
		const err = h("div", { class: "error", role: "alert" });
		const capInput = input({ type: "number", min: 1, step: 1, value: capacity, placeholder: "1000000", onInput: (e) => { capacity = e.target.value; } });
		dialog({
			title: `Local meter for ${labelText(a)}`,
			sub: "Optional. API keys don't report how much you have left, so NeoProxy can count the tokens it sends through this account against a capacity you choose. Without a meter the account is always available.",
			body: [
				field("Window", select([["5h", "Every 5 hours"], ["day", "Every day"], ["week", "Every week"], ["month", "Every month"]].map(([value, label]) => ({ value, label })), kind, (v) => { kind = v; }), "Usage counts input and output tokens and starts again when the window ends."),
				field("Capacity in tokens", capInput, "A routing budget, not a provider limit or a bill."),
				field("Window starts", input({ type: "datetime-local", value: startsAt, onInput: (e) => { startsAt = e.target.value; } }), "Your local time. The window repeats from this moment."),
				err,
			],
			actions: (close) => [btn("Cancel", { onClick: close }), btn("Save meter", { cls: "primary", onClick: async (e) => {
				const button = e.currentTarget;
				err.textContent = "";
				try {
					const capacityTokens = Number(capacity), start = new Date(startsAt).getTime();
					if (!Number.isSafeInteger(capacityTokens) || capacityTokens <= 0 || !Number.isFinite(start)) throw new Error("Enter a whole number of tokens above zero and a valid start time.");
					button.disabled = true;
					await engine.put(`${accountPath(a.id)}/windows/${encodeURIComponent(kind)}`, { capacityTokens, startsAt: start });
					invalidate("vault");
					close();
					toast("Local meter saved");
					reload();
				} catch (e2) { err.textContent = explain(e2); button.disabled = false; }
			} })],
		});
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
		if (full && p) return p.ok ? { kind: "ok", text: `Passed ${fmtAgo(p.checkedAt)} · ${p.latencyMs} ms` } : { kind: "bad", text: `Failed ${fmtAgo(p.checkedAt)}: ${p.error || "no reply"}` };
		return null;
	}

	function trafficSentence(a) {
		const u = a.usage ?? a.face?.usage;
		if (!u?.requests24h) return "Ready to serve. No requests in the last 24 hours.";
		return `${plural(u.requests24h, "request")} in the last 24 hours${u.failed24h ? `, ${u.failed24h} failed` : ", none failed"}.`;
	}

	function meterCell(a) {
		if (!a.meters.length) {
			return h("span", { class: "pl-meter none", title: "No meter: this account is always available to the stages that use it" }, meterRing(a, { size: 28 }), h("span", { class: "pl-meter-text" }, h("span", { class: "pl-meter-v" }, "No meter"), h("span", { class: "pl-meter-r" }, "always available")));
		}
		// Each meter is a Tab stop whose attribution (provider-reported or local)
		// shows on hover and on keyboard focus, not only in a native title.
		return h("span", { class: "pl-meters" }, a.meters.map((m) => {
			const ringEl = meterRing(a, { size: 28, kind: m.kind });
			ringEl.removeAttribute("title");
			return h("span", { class: "pl-meter", tabindex: "0" },
				ringEl,
				h("span", { class: "pl-meter-text" },
					h("span", { class: "pl-meter-v" }, h("span", { class: "pl-meter-k" }, KIND_WORD[m.kind]), fmtPct(m.used)),
					h("span", { class: "pl-meter-r" }, [m.source === "local" ? "local" : null, m.resetsAt ? `resets in ${fmtIn(m.resetsAt)}` : null].filter(Boolean).join(" · "))),
				h("span", { class: "pl-meter-tip" }, `${fmtPct(m.used)} of the ${m.kind} window used, ${m.source === "provider" ? "reported by the provider" : "counted by the local token meter"}`));
		}));
	}

	function trafficCell(a) {
		const u = a.usage ?? a.face?.usage;
		if (!u) return h("span", { class: "ink-3" }, "–");
		const second = u.failed24h ? h("span", { class: "pl-bad" }, `${plural(u.failed24h, "failure")}`) : h("span", { class: "pl-sub" }, u.lastUsedAt ? `used ${fmtAgo(u.lastUsedAt)}` : "never used");
		return h("span", { class: "pl-traffic" }, h("span", { class: "pl-main" }, u.requests24h ? `${fmtCompact(u.requests24h)} in 24h` : "None in 24h"), second);
	}

	function accountRow(a) {
		const s = sev(a);
		const t = testLine(a);
		const pending = ui.pending.has(a.id) || ui.tests.get(a.id)?.running;
		const acts = actionsFor(a);
		const row = h("div", {
			class: "pool-acct", role: "button", tabindex: "0", "aria-selected": String(selectedId === a.id), "data-sev": s === "ok" ? null : s,
			onClick: () => select_(a),
			onKeydown: (e) => { if ((e.key === "Enter" || e.key === " ") && e.target === row) { e.preventDefault(); select_(a); } },
		},
			h("div", { class: "pl-id" },
				h("div", { class: `pl-label ${a.kind === "apiKey" && a.label.startsWith("…") ? "mono" : ""}`, title: a.label }, labelText(a)),
				h("div", { class: "pl-sub" }, kindLine(a), a.face?.refresh === "locked" ? " · refresh locked" : ""),
				t ? h("div", { class: `pl-test ${t.kind}` }, t.text) : null),
			h("div", { class: "pl-cell pl-meter-cell" }, meterCell(a)),
			h("div", { class: "pl-cell pl-health", title: accountHealth(a).hint ?? "" }, healthStatus(a)),
			h("div", { class: "pl-cell pl-traffic-cell" }, trafficCell(a)),
			h("div", { class: "pl-cell pl-owner", title: person(a.ownerId) }, person(a.ownerId)),
			h("div", { class: "pl-acts", onClick: (e) => e.stopPropagation(), onKeydown: (e) => e.stopPropagation() },
				btn(pending && ui.tests.get(a.id)?.running ? "Testing…" : "Test", { icon: "play", cls: "outline sm", title: "Test this account", disabled: !!pending, onClick: () => runTest(a) }),
				(() => { const more = iconBtn("x", "More actions", () => openMenu(more, acts.map((x) => ({ label: x.label, icon: x.icon, danger: x.danger, onClick: x.run })))); replace(more, svg(MORE, 16)); more.setAttribute("aria-haspopup", "menu"); return more; })()),
		);
		return row;
	}

	function select_(a) {
		selectedId = selectedId === a.id ? null : a.id;
		paint();
	}

	// ---------- the provider groups ----------
	function groupsFor(list) {
		const q = ui.query.trim().toLowerCase();
		const out = [];
		for (const p of providers()) {
			let accs = list.filter((a) => a.providerId === p.id);
			if (!admin && !accs.length) continue;
			const providerMatch = q && [p.name, p.vendor].some((s) => s?.toLowerCase().includes(q));
			if (q && !providerMatch) accs = accs.filter((a) => [a.label, a.plan, person(a.ownerId), kindLine(a)].some((s) => s?.toLowerCase().includes(q)));
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
			h("button", { type: "button", class: "pool-prov-toggle", "data-prov": p.id, "aria-expanded": String(open), "aria-label": `${open ? "Collapse" : "Expand"} ${p.name}`, onClick: () => {
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
				bad ? badge(`${bad} need${bad === 1 ? "s" : ""} attention`, "bad") : null,
				p.exposed ? null : badge("hidden from users", "warn")),
			admin ? toggle(p.exposed, async (v) => {
				try { await neo.setProviderExposed(p.id, v); toast(v ? `${p.name} is served again` : `${p.name} hidden from users`); }
				catch (e) { toast(explain(e), "bad"); throw e; }
			}, `Serve ${p.name}`) : null);
		return h("section", { class: `pool-prov ${open ? "open" : ""}`, "aria-label": p.name },
			head,
			open && accs.length ? h("div", { class: "pool-accts" }, accs.map(accountRow)) : null);
	}

	// ---------- inspector ----------
	function drawnBy(a) {
		const p = provOf(a);
		const out = [];
		for (const vm of S.vms) {
			const stages = [];
			vm.stages.forEach((s, i) => {
				const pid = s.model.split("/")[0];
				if (pid !== p?.id && canonicalProviderId(pid) !== p?.canonicalId) return;
				if (s.accounts !== "all" && !s.accounts.includes(a.id)) return;
				stages.push({ s, i });
			});
			if (stages.length) out.push({ vm, stages });
		}
		return out;
	}

	function windowRow(w, { local }) {
		const text = local && w.unit === "tokens" && w.capacity ? `${fmtCompact(w.usedTokens ?? w.used * w.capacity)} of ${fmtCompact(w.capacity)} tokens` : null;
		return h("div", { class: "pool-win" },
			ring({ used: w.known ? w.used : null, size: 40 }),
			h("div", { class: "pool-win-text" },
				h("div", { class: "pool-win-main" }, `${KIND_WORD[w.kind] ?? w.kind} window`, w.known ? h("span", { class: "ink-2" }, ` · ${fmtPct(w.used)} used`) : h("span", { class: "ink-3" }, " · not reported")),
				h("div", { class: "pool-win-sub" }, [text, w.known && w.resetsAt ? `resets in ${fmtIn(w.resetsAt)}` : null].filter(Boolean).join(" · ") || "\u00a0")));
	}

	function modelsSection(a) {
		const prefix = `${a.providerId}/`;
		const ids = modelsOf(a).map((m) => (m.startsWith(prefix) ? m.slice(prefix.length) : m));
		if (!ids.length) return section("Models", h("p", { class: "ink-3" }, "No models are available through this account yet. A sign-in that expired or a key that failed its check lists none."));
		const list = h("div", { class: "pool-models" });
		let all = false;
		const draw = () => {
			replace(list, 
				...(all ? ids : ids.slice(0, 12)).map((m) => h("div", { class: "pool-model", title: m }, m)),
				ids.length > 12 ? btn(all ? "Show fewer" : `Show all ${ids.length}`, { cls: "outline sm", onClick: () => { all = !all; draw(); } }) : null);
		};
		draw();
		return section("Models", h("details", { class: "pool-details" }, h("summary", null, `${plural(ids.length, "model")} available`), list));
	}

	function inspect(a) {
		const p = provOf(a);
		const hs = accountHealth(a);
		const t = testLine(a, { full: true });
		const drawn = drawnBy(a);
		const byKind = (x, y) => KINDS.indexOf(x.kind) - KINDS.indexOf(y.kind);
		const provW = [...(a.providerWindows ?? [])].sort(byKind);
		const localW = (a.windows ?? []).filter((w) => w.known).sort(byKind);
		const overlap = KINDS.some((k) => provW.some((w) => w.kind === k && w.known) && localW.some((w) => w.kind === k));
		const acts = actionsFor(a);
		const pending = ui.pending.has(a.id) || ui.tests.get(a.id)?.running;
		const manage = acts.filter((x) => !["test", "remove", "meter"].includes(x.id));
		const remove = acts.find((x) => x.id === "remove");
		const canReassign = admin && a.face?.actions?.includes("reassign") && contributors().length > 1;
		const ownerControl = canReassign
			? select(contributors().map((u) => ({ value: u.id, label: u.name || u.handle })), a.ownerId ?? "", async (ownerId) => {
				try { await neo.updateAccess(a.id, { ownerId }); toast("Contributor changed"); } catch (e) { toast(explain(e), "bad"); }
				paint();
			}, { "aria-label": "Contributor" })
			: person(a.ownerId);
		return sheet({
			title: h("span", { class: "row wrap", style: { gap: "10px" } }, labelText(a), healthStatus(a)),
			sub: `${p?.name ?? "Provider"} · ${kindLine(a)}`,
			onClose: () => { if (selectedId === a.id) { selectedId = null; if (ctx.params[0]) ctx.navigate("pool"); else paint(); } },
			body: [
				section("Health", h("div", { class: "pool-health" }, healthStatus(a), h("p", { class: "ink-2" }, hs.kind === "ok" ? trafficSentence(a) : hs.hint))),
				section("Test",
					t ? h("div", { class: `pool-testbox ${t.kind}` }, t.text) : h("p", { class: "ink-3" }, "Never tested. A test sends one short request through this account to check that its key or sign-in works."),
					a.face?.probeConfig ? h("p", { class: "ink-3" }, "Test sends ", h("code", null, a.face.probeConfig.model), " on ", a.face.probeConfig.endpoint, ".") : null,
					h("div", { class: "row", style: { gap: "8px", flexWrap: "wrap" } },
						btn(ui.tests.get(a.id)?.running ? "Testing…" : "Test this account", { icon: "play", cls: "outline", disabled: !!pending, onClick: () => runTest(a) }),
						btn("Test payload…", { icon: "terminal", cls: "outline", onClick: () => openTestDialog(a, { provider: p, onChange: () => reload() }) }))),
				section("Provider-reported usage", provW.length
					? provW.map((w) => windowRow(w, { local: false }))
					: h("p", { class: "ink-3" }, a.kind === "apiKey" ? "API keys don't report how much is left." : "This provider hasn't reported usage for this account.")),
				section("Local meter",
					localW.length ? localW.map((w) => windowRow(w, { local: true })) : h("p", { class: "ink-3" }, "No local meter. Only needed for API keys, which report no usage of their own; without one this account is always available."),
					overlap ? h("p", { class: "ink-3" }, "When the provider and a local meter cover the same window, the provider's reading is used.") : null,
					btn("Configure local meter", { icon: "sliders", cls: "outline", onClick: () => meterDialog(a) })),
				manage.length ? section("Manage", h("div", { class: "pool-manage" }, manage.map((x) => btn(x.label, { icon: x.icon, cls: "outline", disabled: !!pending, onClick: x.run })))) : null,
				modelsSection(a),
				section("Virtual models that draw from it", drawn.length
					? h("div", { class: "pool-drawn" }, drawn.map(({ vm, stages }) => h("button", { type: "button", class: "pool-drawn-row", onClick: () => ctx.navigate(`virtual-models/${vm.id}`) },
						servedName(vm.served ?? `neoproxy/${vm.name}`, { virtual: true }),
						h("span", { class: "ink-3" }, stages.map(({ i, s }) => `stage ${i + 1} · ${s.model.split("/").slice(1).join("/") || s.model}`).join(", ")),
						icon("chevronRight", 14))))
					: h("p", { class: "ink-3" }, "No virtual model draws from this account yet. Direct provider models (neoproxy/<provider>/<model>) can still reach it, following the engine's routing strategy.")),
				section("Details", kv([
					["Provider", p ? `${p.name}${p.vendor && p.vendor.toLowerCase() !== p.name.toLowerCase() ? ` · ${p.vendor}` : ""}` : a.providerId],
					["Kind", { subscription: "Signed-in account", apiKey: "API key", imported: "Imported from NeoPi", neopi: "Shared from a NeoPi", file: "Credential file" }[a.kind] ?? a.kind],
					["Sign-in", { auto: "NeoProxy refreshes it automatically", locked: "Refresh locked; it stops when the copied sign-in expires", neopi: "NeoPi refreshes it; NeoProxy only borrows it", none: "API keys don't expire on a schedule" }[a.face?.refresh] ?? "–"],
					["Contributor", ownerControl],
					["Added", a.face?.createdAt ? fmtDate(a.face.createdAt) : "–"],
					["Last used", (a.usage ?? a.face?.usage)?.lastUsedAt ? `${fmtAgo((a.usage ?? a.face.usage).lastUsedAt)}` : "Never"],
					["Id", h("code", { class: "pool-id" }, a.id)],
				])),
			],
			foot: [remove ? btn("Remove…", { icon: "trash", cls: "danger sm", disabled: !!pending, onClick: remove.run }) : null],
		});
	}

	let currentSheet = null;
	function paintInspector() {
		const a = selectedId ? accounts().find((x) => x.id === selectedId) : null;
		if (selectedId && !a) selectedId = null;
		const scroll = currentSheet?.el.querySelector(".sheet-body")?.scrollTop ?? 0;
		if (!a) { currentSheet?.close(); currentSheet = null; return; }
		currentSheet = inspect(a);
		const body = currentSheet.el.querySelector(".sheet-body");
		if (body) body.scrollTop = scroll;
	}

	// ---------- page ----------
	const headHost = h("div"), toolbarHost = h("div"), listHost = h("div", { class: "pool-list" }), sourcesHost = h("div");
	let segHost, searchBox;

	function headCard() {
		const accs = accounts();
		const n = accs.length, bad = accs.filter(attention).length;
		const c = S.core;
		const summary = h("span", { class: "pool-summary" },
			plural(n, "account"), `, ${plural(new Set(accs.map((a) => a.providerId)).size, "provider")}`,
			bad ? [", ", h("span", { class: "pl-bad" }, `${bad} need${bad === 1 ? "s" : ""} attention`)] : n ? ", all healthy" : "");
		const add = btn("Add provider access", { icon: "plus", cls: "primary", onClick: () => openAdd() });
		if (!admin) return card({ cls: "pool-link", body: h("div", { class: "pool-link-row" }, h("div", { class: "pool-link-main" }, h("div", { class: "pool-link-title" }, "Your contributions"), h("div", { class: "pool-link-meta" }, summary)), add) });
		return card({ cls: "pool-link", body: h("div", { class: "pool-link-row" },
			h("div", { class: "pool-link-main" },
				h("div", { class: "pool-link-title" }, status(c ? (c.linked ? "live" : "warn") : "bad", c ? (c.linked ? `Engine online on ${c.host || "this machine"}` : "Engine not reachable") : "Engine status unavailable"), c?.heartbeatAt ? h("span", { class: "pool-link-beat" }, `heartbeat ${fmtAgo(c.heartbeatAt)}`) : null),
				h("div", { class: "pool-link-meta" },
					c ? [c.version ? h("span", null, "NeoProxy ", h("code", null, c.version)) : null, c.endpoint ? h("span", null, "serving at ", h("code", null, c.endpoint)) : null] : null,
					summary)),
			add) });
	}

	const collapseHost = h("span");
	function buildToolbar() {
		searchBox = search("Find a provider, email or key", (v) => { ui.query = v; ui.shutFiltered.clear(); paint(); }, { value: ui.query, "aria-label": "Find in the pool" });
		segHost = h("span");
		return h("div", { class: "pool-toolbar" }, searchBox, segHost, h("span", { class: "spacer" }), collapseHost);
	}
	function updateToolbar() {
		const n = accounts().filter(attention).length;
		replace(segHost, seg([{ value: "all", label: "All" }, { value: "attention", label: n ? `Needs attention (${n})` : "Needs attention" }], ui.filter, (v) => { ui.filter = v; ui.shutFiltered.clear(); paint(); }));
		const shut = shutGroups();
		const anyOpen = providers().some((p) => !shut.has(p.id));
		replace(collapseHost, btn(anyOpen ? "Collapse all" : "Expand all", { cls: "outline sm", onClick: () => { if (anyOpen) for (const p of providers()) shut.add(p.id); else shut.clear(); paint(); } }));
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
		replace(headHost, headCard());
		const all = accounts();
		const hasList = providers().length && (admin || all.length);
		toolbarHost.hidden = !(hasList && all.length);
		updateToolbar();
		if (!hasList) replace(listHost, emptyState());
		else {
			const groups = groupsFor(all);
			const filtered = ui.filter === "attention" && !ui.query;
			replace(listHost, ...(groups.length
				? groups.map(providerBlock)
				: [card({ body: empty({ icon: "search", title: filtered ? "Nothing needs attention" : "Nothing matches", hint: filtered ? "Every account is signed in, within its limits and passing its tests." : "Try a provider name, an email or the end of a key." }) })]));
			if (!all.length) listHost.prepend(emptyState());
		}
		replace(sourcesHost, sourcesCard() ?? "");
		paintInspector();
	}

	try { await fetchAll(); }
	catch (e) { ctx.root.lastChild.replaceWith(errorState(e, () => ctx.rerender())); return; }
	ctx.root.lastChild.remove();
	toolbarHost.append(buildToolbar());
	pageHost.append(headHost, toolbarHost, listHost, sourcesHost);
	paint();

	const off = onChange((what) => { if (what === "vault") reload(); });
	timer = setInterval(() => { if (!document.hidden) reload(); }, 60_000);
	return () => { disposed = true; off(); clearInterval(timer); closeMenu(); currentSheet?.close(); };
}
