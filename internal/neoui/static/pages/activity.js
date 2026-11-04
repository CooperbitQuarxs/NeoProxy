// Activity: every request in a time range, with how it was routed and, when it
// failed, why. Admins see everyone; members see their own. The range is read in
// full from the ledger (see lib/ledger.js), so the numbers and the search cover
// every request in it; when a range holds more than the page can load, the top
// of the page says so. The numbers are also the outcome filter. Selecting a row
// opens a sheet that answers "what happened and what do I do" first; the arrow
// keys (or J and K) walk the list while it is open.

import { h, icon, fmtCompact, fmtDuration, fmtAgo, fmtInt, plural, clear } from "../lib/dom.js";
import { neo } from "../lib/client.js";
import { pageHead, btn, iconBtn, badge, avatar, servedName, search, select, table, section, kv, trail, outcomeBadge, empty, spinner, errorState, copyLine, toast, sheet } from "../lib/components.js";
import { jsonView } from "../lib/json.js";
import { explainDecision, providerAdvice } from "../lib/explain.js";
import { findReport } from "../lib/error-report.js";
import { RANGES, PAGE, requestsSince, mergeLatest } from "../lib/ledger.js";

// ---------- formatting ----------

const dayKey = (ts) => new Date(ts).toDateString();
const trimPct = (x) => `${x.toFixed(1).replace(/\.0$/, "")}%`;
function percentile(sorted, p) {
	if (!sorted.length) return null;
	return sorted[Math.max(0, Math.ceil(p * sorted.length) - 1)];
}
const clock = (ts) => new Date(ts).toLocaleTimeString("en-GB");
const hhmm = (ts) => new Date(ts).toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" });
const whenShort = (ts) => new Date(ts).toLocaleString("en-GB", { weekday: "short", hour: "2-digit", minute: "2-digit" });
// "Mozilla/5.0 (…) Chrome/154 …" reads as "Chrome 154 browser"; "npi/18.14.0" as "npi 18.14.0".
function clientName(ua) {
	if (!ua) return "Unknown client";
	if (/^Mozilla\//.test(ua)) {
		for (const [token, name] of [["Edg", "Edge"], ["Firefox", "Firefox"], ["Chrome", "Chrome"], ["Safari", "Safari"]]) {
			const m = new RegExp(`${token}/(\\d+)`).exec(ua);
			if (m) return `${name} ${m[1]} browser`;
		}
		return "Web browser";
	}
	const m = /^([\w.-]+)\/(\S+)/.exec(ua);
	return m ? `${m[1]} ${m[2]}` : ua;
}
const isVirtual = (asked) => asked.replace(/^neoproxy\//, "").split("/").length === 1;
const typing = (el) => el?.closest?.("input, textarea, select, [contenteditable]:not([contenteditable=false])");

// The outcome filter. Limited also covers queued: both were held back by a limit.
const OUTCOMES = ["", "ok", "limited", "error"];
const outcomeOf = (r) => (r.outcome === "queued" ? "limited" : r.outcome);
// Repeats fold only when they are neighbours in the ledger and under two minutes apart.
const GROUP_GAP_MS = 120_000;

// ---------- page ----------

export async function render(ctx) {
	const { root, session } = ctx;
	const mine = session.role !== "admin";
	root.append(
		pageHead({
			title: "Activity",
			desc: mine ? "Your requests: what answered each one, and why." : "Every request through the proxy: who sent it, what answered, and why.",
		}),
	);
	root.append(spinner());
	let users, models, vault, vms;
	try {
		[users, models, vault, vms] = await Promise.all([mine ? neo.user(session.userId).then((u) => [u]) : neo.users(), neo.models(), neo.vault(), neo.virtualModels()]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	const askedOutcome = ctx.query.get("outcome") ?? "";
	const filters = { userId: mine ? session.userId : ctx.query.get("user") ?? "", outcome: askedOutcome === "queued" ? "limited" : OUTCOMES.includes(askedOutcome) ? askedOutcome : "", q: ctx.query.get("q") ?? "" };
	let range = RANGES.find((x) => x.id === ctx.query.get("range")) ?? RANGES[1];
	let selectedId = ctx.params[0] ? decodeURIComponent(ctx.params[0]) : null;
	let all = [];
	let complete = true;
	let oldest = 0;
	let loading = false;
	let generation = 0;
	let disposed = false;
	let focusId = null;
	let sort = { key: "t", dir: "desc" };
	let currentSheet = null;
	let loadedAt = 0;

	const accounts = mine ? [] : vault.accounts;
	const modelName = (id) => models.find((m) => m.id === id)?.display ?? id;
	const account = (id) => accounts.find((a) => a.id === id);
	const userOf = (r) => users.find((x) => x.id === r.userId);
	const explained = new Map();
	const explain = (r) => {
		if (!explained.has(r.id)) explained.set(r.id, explainDecision(r, { models, accounts, users, vms, mine }));
		return explained.get(r.id);
	};
	const vmOf = (r) => (r.decision.virtualModel ? vms.find((v) => v.id === r.decision.virtualModel) : null);
	const whereOf = (d) => (Number.isInteger(d.stageIndex) ? (d.stageIndex === 0 ? "first choice" : `fallback ${d.stageIndex}`) : d.virtualModel ? "final rule" : d.model ? "direct" : "");

	// ---------- layout ----------

	const skip = h("button", { type: "button", class: "skip-link act-skip", onClick: () => focusList() }, "Skip to the request list (J)");
	const scopeHost = h("div", { class: "act-scope", role: "status" });
	const stripHost = h("div", { class: "act-sum-host" });
	const searchEl = search(mine ? "Search model, account, reason or message" : "Search model, person, account, reason or message", (v) => {
		filters.q = v.trim();
		refresh();
	}, { "aria-label": "Search the requests in this range", value: filters.q });
	const clearBtn = btn("Clear filters", { icon: "x", cls: "ghost sm act-clear", onClick: () => clearFilters() });
	const live = h("span", { class: "act-live", role: "status" }, h("i"), "Live");
	const rangeSelect = select(RANGES.map((x) => ({ value: x.id, label: x.label })), range.id, (v) => {
		range = RANGES.find((x) => x.id === v) ?? RANGES[1];
		setUrl(selectedId);
		load();
	}, { class: "select act-range", "aria-label": "Time range" });
	const toolbar = h(
		"div",
		{ class: "row wrap page-toolbar act-toolbar" },
		searchEl,
		rangeSelect,
		mine
			? null
			: select([{ value: "", label: "Everyone" }, ...users.map((u) => ({ value: u.id, label: u.handle }))], filters.userId, (v) => {
					filters.userId = v;
					setUrl(selectedId);
					syncClear();
					load();
				}, { class: "select act-person", "aria-label": "Show requests from" }),
		clearBtn,
		h("span", { class: "spacer" }),
		h("span", { class: "act-keys", "aria-hidden": "true" }, h("kbd", null, "J"), h("kbd", null, "K"), " move in the list"),
		live,
		iconBtn("refresh", "Reload now", () => load(), "sm"),
	);
	const personSelect = toolbar.querySelector(".act-person");
	const tableHost = h("div", { class: `act-rows ${mine ? "mine" : ""}`, id: "act-requests" });
	root.append(skip, scopeHost, stripHost, toolbar, h("section", { class: "card rise act-list", "aria-label": "Requests" }, tableHost));

	// ---------- data ----------

	function matchesQuery(r) {
		if (!filters.q) return true;
		const needle = filters.q.toLowerCase();
		const d = r.decision;
		const u = userOf(r);
		const hay = [r.asked, d.model, d.model ? modelName(d.model) : "", vmOf(r)?.name, u?.handle, u?.name, d.accountId ? account(d.accountId)?.label : "", r.client, clientName(r.client), explain(r).headline, d.message, r.id].filter(Boolean).join(" ").toLowerCase();
		return hay.includes(needle);
	}
	const statsRows = () => all.filter(matchesQuery);
	const visibleRows = () => statsRows().filter((r) => !filters.outcome || outcomeOf(r) === filters.outcome);

	// The whole range, page by page; a newer load wins over an older one still running.
	async function load() {
		if (disposed) return;
		const mineGen = ++generation;
		loading = true;
		if (!all.length) tableHost.replaceChildren(spinner());
		try {
			const got = await requestsSince(Date.now() - range.ms, { userId: filters.userId });
			if (disposed || mineGen !== generation) return;
			loadedAt = Date.now();
			all = got.rows;
			complete = got.complete;
			oldest = got.oldest;
			explained.clear();
			buildGroups();
			refresh();
		} catch (e) {
			if (mineGen === generation) tableHost.replaceChildren(errorState(e, () => load()));
		} finally {
			if (mineGen === generation) loading = false;
		}
	}
	// New requests: fold the newest page in, unless more than a page arrived.
	async function loadLatest() {
		if (loading || disposed) return;
		const mineGen = generation;
		try {
			const latest = await neo.requests({ limit: PAGE, userId: filters.userId || undefined });
			if (disposed || mineGen !== generation) return;
			loadedAt = Date.now();
			const merged = mergeLatest(all, latest, complete ? Date.now() - range.ms : oldest);
			if (!merged) return load();
			const before = all.map((r) => `${r.id}:${r.outcome}`).join("|");
			if (merged.map((r) => `${r.id}:${r.outcome}`).join("|") === before) return;
			all = merged;
			buildGroups();
			refresh();
		} catch {
			// The stream reports the failure in the Live badge.
		}
	}

	function refresh() {
		setUrl(selectedId);
		syncClear();
		renderScope();
		renderStrip();
		renderTable();
	}

	function syncClear() {
		clearBtn.hidden = !(filters.q || filters.outcome || (!mine && filters.userId));
	}

	// ---------- what the numbers cover ----------

	function renderScope() {
		const who = !mine && filters.userId ? ` from ${users.find((u) => u.id === filters.userId)?.handle ?? "this person"}` : "";
		if (complete) {
			scopeHost.className = "act-scope";
			scopeHost.replaceChildren(icon("clock", 14), h("span", { class: "fact" }, h("b", null, range.label), all.length ? ` · ${plural(all.length, "request")}${who}, all loaded. The numbers and the search cover every one.` : ` · no requests${who}.`));
		} else {
			scopeHost.className = "act-scope warn";
			scopeHost.replaceChildren(icon("info", 14), h("span", { class: "fact" }, h("b", null, `${range.label}: only the latest ${fmtInt(all.length)} requests${who} are loaded`), `, back to ${whenShort(oldest)}. The numbers and the search cover those, not the older ones. Pick a person or a shorter range to see the rest.`));
		}
	}

	// ---------- numbers that filter ----------

	function renderStrip() {
		const rows = statsRows();
		if (!all.length) {
			stripHost.replaceChildren();
			return;
		}
		const ok = rows.filter((r) => r.outcome === "ok");
		const limited = rows.filter((r) => r.outcome === "limited");
		const queued = rows.filter((r) => r.outcome === "queued");
		const errors = rows.filter((r) => r.outcome === "error");
		const lat = ok.map((r) => r.latencyMs).sort((a, b) => a - b);
		const share = (n) => (rows.length ? (n === rows.length ? "100%" : trimPct(Math.min((n / rows.length) * 100, 99.9))) : "0%");
		// A low served share is shown, not coloured: Limited and Errors carry the warning.
		const reasons = new Map();
		for (const r of errors) reasons.set(explain(r).headline, (reasons.get(explain(r).headline) ?? 0) + 1);
		const topReason = [...reasons.entries()].sort((a, b) => b[1] - a[1])[0];
		const scope = complete ? range.short : `latest ${fmtInt(all.length)}`;
		const filterCell = (value, label, count, sub, tone = "") =>
			h(
				"button",
				{
					type: "button",
					class: "cell act-filter",
					"aria-pressed": String(filters.outcome === value),
					title: value ? `Show only ${label.toLowerCase()} requests` : "Show every request",
					onClick: () => {
						filters.outcome = filters.outcome === value ? "" : value;
						refresh();
					},
				},
				h("span", { class: "label" }, label),
				h("span", { class: `value ${count ? tone : ""}` }, fmtInt(count)),
				h("span", { class: "sub" }, sub),
			);
		stripHost.replaceChildren(
			h(
				"section",
				{ class: "card act-sum", "aria-label": "Requests by outcome; each number filters the list" },
				h(
					"div",
					{ class: "act-filters", role: "group", "aria-label": "Outcome" },
					filterCell("", "Requests", rows.length, filters.q ? `matching, ${scope}` : scope),
					filterCell("ok", "Served", ok.length, `${share(ok.length)} of them`),
					filterCell("limited", "Limited", limited.length + queued.length, queued.length ? `${share(limited.length + queued.length)} · ${queued.length} queued` : `${share(limited.length)} held by a limit`, "warn"),
					filterCell("error", "Errors", errors.length, topReason ? `mostly ${topReason[0]}` : "none", "bad"),
				),
				h(
					"div",
					{ class: "cell act-lat" },
					h("span", { class: "label" }, "Latency"),
					h("span", { class: "value" }, lat.length ? fmtDuration(percentile(lat, 0.5)) : "–"),
					h("span", { class: "sub" }, lat.length ? `median · p95 ${fmtDuration(percentile(lat, 0.95))}` : "nothing served"),
				),
			),
		);
	}

	// ---------- repeats ----------
	// Failures fold only when they are the same failure: same person, asked name,
	// outcome, reason, attempted model and account, status and message, and
	// neighbours in the ledger itself (not merely after a filter hid what was between).

	const expanded = new Set();
	let groupOf = new Map();
	const sigOf = (r) => {
		if (r.outcome === "ok") return null;
		const d = r.decision;
		return [r.userId, r.asked, r.outcome, d.reason, d.virtualModel, d.model, d.accountId, explain(r).status, d.message, explain(r).headline].join("|");
	};
	function buildGroups() {
		groupOf = new Map();
		for (let i = 0; i < all.length;) {
			const key = sigOf(all[i]);
			let j = i + 1;
			if (key) while (j < all.length && sigOf(all[j]) === key && all[j - 1].at - all[j].at <= GROUP_GAP_MS) j++;
			if (j - i > 1) {
				const g = { id: all[i].id };
				for (let k = i; k < j; k++) groupOf.set(all[k].id, g);
			}
			i = j;
		}
	}
	function fold(list) {
		if (sort.key !== "t" || sort.dir !== "desc") return list;
		const out = [];
		for (let i = 0; i < list.length;) {
			const g = groupOf.get(list[i].id);
			let j = i + 1;
			if (g) while (j < list.length && groupOf.get(list[j].id) === g) j++;
			const members = list.slice(i, j);
			if (members.length === 1) out.push(list[i]);
			else {
				const open = expanded.has(g.id);
				const info = { n: members.length, first: members[members.length - 1].at, last: members[0].at, key: g.id, open };
				out.push(Object.assign(Object.create(list[i]), { _group: info }));
				if (open) out.push(...members.slice(1).map((m, k) => Object.assign(Object.create(m), { _member: { of: info, last: k === members.length - 2 } })));
			}
			i = j;
		}
		return out;
	}
	const groupTime = (g) => (hhmm(g.first) !== hhmm(g.last) ? `${hhmm(g.first)}–${hhmm(g.last)}` : clock(g.first) === clock(g.last) ? `all at ${clock(g.last)}` : `${clock(g.first)}–${clock(g.last)}`);
	const groupLabel = (g) => `${g.n} requests · ${groupTime(g)}`;
	function setGroup(key, open) {
		if (open) expanded.add(key);
		else expanded.delete(key);
		renderTable();
	}

	// ---------- table ----------

	function answeredBy(r) {
		const d = r.decision;
		if (d.outcome === "ok") {
			const a = account(d.accountId);
			const where = whereOf(d);
			return h("span", { class: "act-ans", title: `${modelName(d.model)} · ${where}${a ? ` · ${a.label}` : ""}` }, h("span", { class: "m" }, modelName(d.model)), h("span", { class: "act-q" }, ` · ${where}${a ? ` · ${a.label}` : ""}`));
		}
		const text = explain(r).headline;
		const g = r._group;
		const groupBtn = g
			? h("button", { type: "button", class: "act-fold", "aria-expanded": String(g.open), "aria-label": `${groupLabel(g)}: ${g.open ? "fold them back into one row" : "show each request"}`, title: g.open ? "Fold these back into one row (Left arrow)" : "The same failure, back to back: show each request (Right arrow)", onClick: (e) => { e.stopPropagation(); setGroup(g.key, !g.open); }, onKeydown: (e) => e.stopPropagation() }, h("span", null, `${g.n} requests`), h("span", { class: "act-fold-t" }, ` · ${groupTime(g)}`), icon(g.open ? "chevronUp" : "chevronDown", 12))
			: null;
		const tried = d.model ? ` · ${modelName(d.model)}${!mine && d.accountId && account(d.accountId) ? ` on ${account(d.accountId).label}` : ""}` : "";
		return h("span", { class: `act-ans ${d.outcome === "error" ? "act-fail" : "act-held"}`, title: d.message ?? text }, h("span", { class: "act-why-t" }, text, tried ? h("span", { class: "act-q" }, tried) : null), groupBtn);
	}

	// A day label on the first row of each day, so times are never ambiguous.
	function dayLabel(ts) {
		const d = new Date(ts);
		const today = new Date();
		const yesterday = new Date(Date.now() - 86_400_000);
		if (d.toDateString() === today.toDateString()) return "Today";
		if (d.toDateString() === yesterday.toDateString()) return "Yesterday";
		return d.toLocaleDateString("en-GB", { weekday: "short", day: "numeric", month: "short" });
	}

	function timeCell(r, dayStarts) {
		const day = dayStarts.has(r.id) ? h("span", { class: "act-day" }, dayLabel(r.at)) : null;
		const mark = r._member ? h("span", { class: "act-member", "aria-hidden": "true" }, r._member.last ? "└" : "├") : null;
		return [day, h("span", { class: "act-time", dataset: { request: r.id }, title: new Date(r.at).toLocaleString("en-GB") }, mark, clock(r.at))];
	}

	function renderTable() {
		const visible = visibleRows();
		const rows = fold(visible);
		const dayStarts = new Set();
		if (sort.key === "t") rows.forEach((r, i) => { if (!i || dayKey(rows[i - 1].at) !== dayKey(r.at)) dayStarts.add(r.id); });
		const scroller = root;
		const before = scroller.scrollTop;
		const heightBefore = tableHost.offsetHeight;
		const wasTop = before < 8;
		const hadFocus = tableHost.contains(document.activeElement) ? document.activeElement.closest("tr")?.querySelector("[data-request]")?.dataset.request : null;
		const folded = rows.filter((r) => r._group && !r._group.open);
		clear(tableHost);
		tableHost.append(
			table(
				[
					{ key: "t", label: "Time", width: "1%", cls: "act-c-time", sortBy: (r) => r.at, render: (r) => timeCell(r, dayStarts) },
					...(mine ? [] : [{ key: "who", label: "Person", width: "1%", cls: "act-c-who", sortBy: (r) => userOf(r)?.handle ?? r.userId, render: (r) => { const u = userOf(r); return h("span", { class: "row act-who", title: u?.handle ?? r.userId }, avatar(u ?? { handle: "?" }, "sm"), h("span", { class: "act-handle" }, u?.handle ?? "removed user")); } }]),
					{ key: "asked", label: "Asked for", clip: true, width: "24%", cls: "act-c-asked", sortBy: (r) => r.asked, render: (r) => servedName(r.asked, { virtual: isVirtual(r.asked) }) },
					{ key: "served", label: "Answered by, or why not", clip: true, width: "52%", cls: "act-c-served", render: answeredBy },
					{ key: "tok", label: "Tokens", width: "1%", num: true, cls: "act-c-tok", sortBy: (r) => (r.outcome === "ok" ? r.tokens.in + r.tokens.out : -1), render: (r) => (r.outcome === "ok" ? fmtCompact(r.tokens.in + r.tokens.out) : h("span", { class: "ink-4" }, "–")) },
					{ key: "lat", label: "Latency", width: "1%", num: true, cls: "act-c-lat", sortBy: (r) => r.latencyMs, render: (r) => fmtDuration(r.latencyMs) },
					{ key: "out", label: "", width: "1%", cls: "act-c-out", render: (r) => outcomeBadge(r.outcome, r.decision) },
				],
				rows,
				{
					onRow: toggle,
					selectedKey: selectedId,
					sort,
					stack: true,
					emptyState: empty({ icon: "activity", title: all.length ? "No requests match" : "No requests in this range", hint: all.length ? `Nothing in the ${complete ? range.label.toLowerCase() : `latest ${fmtInt(all.length)} requests`} matches these filters.` : complete ? `Nothing was sent in the ${range.label.toLowerCase()}. Try a longer range.` : "", action: all.length && clearBtn.hidden === false ? btn("Clear filters", { cls: "outline", onClick: clearFilters }) : !all.length && range.id !== "7d" ? btn("Show the last 7 days", { cls: "outline", onClick: () => { range = RANGES[2]; rangeSelect.value = "7d"; setUrl(selectedId); load(); } }) : null }),
				},
			),
			folded.length ? h("div", { class: "act-foot fact" }, `${plural(visible.length, "request")} in ${plural(rows.length, "row")}: ${plural(folded.length, "run")} of the same failure ${folded.length === 1 ? "is" : "are"} folded. Open one with its “requests” button or the Right arrow.`) : "",
		);
		const region = tableHost.querySelector(".table-scroll");
		if (region) region.setAttribute("aria-label", "Requests; arrow keys or J and K move between rows, Enter opens one, Right and Left open and fold repeats");
		if (!wasTop) scroller.scrollTop = before + (tableHost.offsetHeight - heightBefore);
		if (hadFocus) rowEl(hadFocus)?.focus({ preventScroll: true });
	}

	// Remember the column sort the table applied so a live refresh keeps it.
	tableHost.addEventListener("click", (e) => {
		const button = e.target.closest(".sort-button");
		if (!button) return;
		const th = button.closest("th");
		sort = { key: button.dataset.sort, dir: th?.getAttribute("aria-sort") === "ascending" ? "asc" : "desc" };
	});

	const rowEl = (id) => tableHost.querySelector(`[data-request="${CSS.escape(id ?? "")}"]`)?.closest("tr");
	const rowIds = () => [...tableHost.querySelectorAll("tbody tr")].map((tr) => tr.querySelector("[data-request]")?.dataset.request);
	const rowData = (id) => fold(visibleRows()).find((r) => r.id === id);

	// Up and Down walk the rows; with the sheet open they walk the sheet too.
	function move(dir, from) {
		const ids = rowIds();
		if (!ids.length) return;
		const at = ids.indexOf(from);
		const nextId = dir === "first" ? ids[0] : dir === "last" ? ids[ids.length - 1] : ids[at < 0 ? 0 : Math.max(0, Math.min(ids.length - 1, at + dir))];
		const tr = rowEl(nextId);
		tr?.focus({ preventScroll: true });
		tr?.scrollIntoView({ block: "nearest" });
		focusId = nextId;
		if (currentSheet && nextId !== selectedId) {
			const r = all.find((x) => x.id === nextId);
			if (r) open(r);
		}
	}
	function focusList() {
		const tr = rowEl(currentSheet ? selectedId : focusId) ?? tableHost.querySelector("tbody tr");
		if (!tr) return (searchEl.querySelector("input") ?? tableHost).focus();
		tr.focus({ preventScroll: true });
		tr.scrollIntoView({ block: "nearest" });
		focusId = tr.querySelector("[data-request]")?.dataset.request ?? focusId;
	}
	const KEYS = { ArrowDown: 1, ArrowUp: -1, Home: "first", End: "last" };
	tableHost.addEventListener("keydown", (e) => {
		const tr = e.target.closest?.("tbody tr");
		if (!tr || e.target !== tr || e.altKey || e.ctrlKey || e.metaKey) return;
		const id = tr.querySelector("[data-request]")?.dataset.request;
		if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
			const r = rowData(id);
			const g = r?._group ?? r?._member?.of;
			if (!g) return;
			e.preventDefault();
			if (e.key === "ArrowRight" && r._group && !g.open) setGroup(g.key, true);
			else if (e.key === "ArrowLeft" && g.open) { focusId = g.key; setGroup(g.key, false); rowEl(g.key)?.focus({ preventScroll: true }); }
			return;
		}
		if (!(e.key in KEYS)) return;
		e.preventDefault();
		e.stopPropagation();
		move(KEYS[e.key], id);
	});
	// J and K reach and walk the list from anywhere on the page; with the sheet open, so do the arrows.
	const pageKeys = (e) => {
		if (e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey || typing(e.target) || document.querySelector(".dialog-backdrop")) return;
		if (e.target.closest?.(".jv, .act-raw")) return;
		const key = e.key.toLowerCase();
		if (key === "j" || key === "k") {
			e.preventDefault();
			const inList = tableHost.contains(document.activeElement) && document.activeElement.matches("tbody tr");
			if (!inList && !currentSheet) return focusList();
			move(key === "j" ? 1 : -1, currentSheet ? selectedId : document.activeElement.querySelector("[data-request]")?.dataset.request);
			return;
		}
		if (!currentSheet || (e.key !== "ArrowDown" && e.key !== "ArrowUp")) return;
		e.preventDefault();
		move(KEYS[e.key], selectedId);
	};
	document.addEventListener("keydown", pageKeys);

	function clearFilters() {
		filters.q = "";
		filters.outcome = "";
		searchEl.querySelector("input").value = "";
		if (!mine && filters.userId) {
			filters.userId = "";
			if (personSelect) personSelect.value = "";
			setUrl(selectedId);
			syncClear();
			load();
		}
		refresh();
	}

	function filterTo(change) {
		currentSheet?.close();
		if (change.q !== undefined) {
			filters.q = change.q;
			searchEl.querySelector("input").value = change.q;
		}
		if (change.outcome !== undefined) filters.outcome = change.outcome;
		if (change.userId !== undefined && change.userId !== filters.userId) {
			filters.userId = change.userId;
			if (personSelect) personSelect.value = change.userId;
			setUrl(null);
			syncClear();
			load();
		}
		refresh();
	}

	function markSelected() {
		for (const tr of tableHost.querySelectorAll("tbody tr")) {
			const id = tr.querySelector("[data-request]")?.dataset.request;
			tr.setAttribute("aria-selected", String(id === selectedId));
		}
	}

	// ---------- url ----------

	function setUrl(id) {
		// Never rewrite the address once the user has moved to another page.
		if (disposed || !/^#\/activity(?:[/?]|$)/.test(location.hash)) return;
		const q = new URLSearchParams();
		if (range.id !== "24h") q.set("range", range.id);
		if (!mine && filters.userId) q.set("user", filters.userId);
		if (filters.outcome) q.set("outcome", filters.outcome);
		if (filters.q) q.set("q", filters.q);
		const hash = `#/activity${id ? `/${encodeURIComponent(id)}` : ""}${q.size ? `?${q}` : ""}`;
		if (location.hash !== hash) history.replaceState(null, "", hash);
	}

	// ---------- sheet ----------

	function toggle(r) {
		if (r.id === selectedId && currentSheet) close();
		else open(r);
	}

	function focusRow() {
		(rowEl(focusId) ?? tableHost.querySelector(".table-scroll") ?? searchEl.querySelector("input"))?.focus({ preventScroll: true });
	}

	function close() {
		focusId = selectedId;
		selectedId = null;
		markSelected();
		setUrl(null);
		currentSheet?.close();
		currentSheet = null;
		focusRow();
	}

	function open(r, { scroll = false } = {}) {
		selectedId = r.id;
		focusId = r.id;
		markSelected();
		setUrl(r.id);
		if (scroll) rowEl(r.id)?.scrollIntoView({ block: "center" });
		const d = r.decision;
		const u = userOf(r);
		const key = u?.keys?.find((k) => k.id === r.keyId);
		const vm = vmOf(r);
		const who = u?.handle ?? (mine ? "you" : "this user");
		const { sentences, hint, fix, status } = explain(r);
		const where = whereOf(d);
		const acc = d.accountId ? account(d.accountId) : null;

		// 1. What happened, the provider's own words, and what to do: the answer first.
		const said = h("div", { class: "act-said", hidden: true });
		const why = h("p", { class: "act-why-text" }, sentences.join(" "));
		const hintText = h("span", { class: "act-hint-t" }, hint);
		const fixHost = h("span", { class: "act-fix" });
		const setFix = (f) => fixHost.replaceChildren(f ? btn(f.label, { icon: "arrowRight", cls: "sm outline", onClick: () => ctx.navigate(f.href) }) : "");
		setFix(fix);
		const next = h("div", { class: "act-next", hidden: !hint && !fix }, h("span", { class: "act-next-k" }, "What to do"), hintText, fixHost);
		const reportLink = h("a", { class: "act-inline-link", hidden: true }, "Read the full error report", icon("chevronRight", 12));
		const answer = h("div", { class: `act-why ${r.outcome}` }, said, why, next, h("p", { class: "act-report" }, reportLink));

		// 2. Who, what and where, as one line of links.
		const idLink = (href, main, sub, lead, title) => {
			const body = [lead, h("span", { class: "act-id-main" }, main), sub ? h("span", { class: "act-id-sub" }, sub) : null];
			return href ? h("a", { class: "act-id", href: `#/${href}`, title }, body) : h("span", { class: "act-id" }, body);
		};
		const ids = [];
		if (!mine) ids.push(idLink(u ? `users/${u.id}` : null, u?.handle ?? "removed user", key ? `key ${key.label}` : clientName(r.client), avatar(u ?? { handle: "?" }, "sm"), u ? `Open ${u.handle}` : null));
		ids.push(idLink(vm && !mine ? `virtual-models/${vm.id}` : null, vm ? vm.name : r.asked.replace(/^neoproxy\//, ""), vm ? "virtual model" : d.virtualModel ? "virtual model, since removed" : isVirtual(r.asked) ? "not a model here" : "direct model", null, vm ? `Open ${vm.name}` : null));
		if (d.model) ids.push(idLink(null, modelName(d.model), d.outcome === "ok" ? where : `tried · ${where}`));
		if (acc) ids.push(idLink(`pool/${acc.id}`, acc.label, "account", null, `Open ${acc.label} in Pool`));
		const identity = h("nav", { class: "act-ids", "aria-label": "Who sent it and where it went" }, ids.flatMap((x, i) => (i ? [h("span", { class: "act-id-sep", "aria-hidden": "true" }, "→"), x] : [x])));

		const similar = h(
			"div",
			{ class: "act-similar" },
			h("span", { class: "act-q" }, "Show only"),
			!mine && u ? h("button", { type: "button", class: "chip-link", onClick: () => filterTo({ userId: u.id }) }, `${u.handle}'s requests`) : null,
			h("button", { type: "button", class: "chip-link", onClick: () => filterTo({ q: vm ? vm.name.replace(/^neoproxy\//, "") : r.asked.replace(/^neoproxy\//, "") }) }, `requests for ${vm ? vm.name.replace(/^neoproxy\//, "") : r.asked.replace(/^neoproxy\//, "")}`),
			r.outcome !== "ok" ? h("button", { type: "button", class: "chip-link", onClick: () => filterTo({ outcome: outcomeOf(r) }) }, outcomeOf(r) === "error" ? "errors" : "limited requests") : null,
		);

		// 3. The full routing path, folded: it is the evidence, not the answer.
		const route = h("details", { class: "act-route" }, h("summary", null, icon("chevronRight", 13), "How it was routed", h("span", { class: "act-q" }, ` · ${plural((d.trail ?? []).length || 1, "step")}`)), trail(d, { models, accounts, users, admin: !mine, who }));

		let rawBuilt = false;
		const raw = h("details", { class: "act-raw" }, h("summary", null, icon("chevronRight", 13), "Raw record", h("span", { class: "ink-3" }, " · for bug reports")));
		raw.addEventListener("toggle", () => {
			if (!raw.open || rawBuilt) return;
			rawBuilt = true;
			raw.append(jsonView(r, { collapsedDepth: 1 }));
		});
		const list = rowIds();
		const at = list.indexOf(r.id);
		const prev = iconBtn("chevronUp", "Newer request (Up arrow or K)", () => move(-1, r.id), "sm");
		const nextBtn = iconBtn("chevronDown", "Older request (Down arrow or J)", () => move(1, r.id), "sm");
		prev.disabled = at <= 0;
		nextBtn.disabled = at < 0 || at >= list.length - 1;
		const asked = servedName(r.asked, { virtual: isVirtual(r.asked) });
		const g = rowData(r.id)?._group;

		const el = sheet({
			title: h("span", { class: "act-title" }, asked, outcomeBadge(r.outcome, d), status ? badge(`HTTP ${status}`, "outline") : null, ...(d.params ?? []).map((p) => badge(`${p.id.replace(/_/g, " ")} ${Array.isArray(p.value) ? p.value.join("/") : p.value}`, "violet")), r.stream ? badge("stream") : null),
			sub: `${new Date(r.at).toLocaleString("en-GB", { weekday: "short", day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" })} · ${fmtAgo(r.at)}${g && !g.open ? ` · newest of ${groupLabel(g)}` : ""}`,
			onClose: () => {
				if (currentSheet !== el) return;
				currentSheet = null;
				if (disposed) return;
				const active = document.activeElement;
				const focusBack = !active || active === document.body || el.el.contains(active);
				if (selectedId === r.id) {
					selectedId = null;
					markSelected();
					setUrl(null);
				}
				if (focusBack) rowEl(r.id)?.focus({ preventScroll: true });
			},
			body: [
				answer,
				identity,
				similar,
				route,
				section(
					"Details",
					kv([
						["Tokens", r.outcome === "ok" ? `${fmtCompact(r.tokens.in)} in · ${fmtCompact(r.tokens.out)} out${r.tokens.cached ? ` · ${fmtCompact(r.tokens.cached)} cached` : ""}` : h("span", { class: "act-q" }, "none, nothing was served")],
						["Latency", fmtDuration(r.latencyMs)],
						["Streaming", r.stream ? "yes" : "no"],
						["Client", h("span", { title: r.client }, clientName(r.client))],
						key ? ["Key", h("span", null, key.label, h("span", { class: "act-q mono" }, ` · ${key.prefix}`))] : null,
						["Request id", copyLine(r.id, "request id")],
					]),
				),
				raw,
			],
			foot: [h("span", { class: "act-step" }, prev, nextBtn)],
			columns: false,
		});
		el.el.classList.add("act-sheet");
		if (!mine && r.outcome === "error") {
			findReport(r.at, r.asked, { upstream: d.reason === "upstream-error" }).then((found) => {
				if (!found || currentSheet !== el) return;
				const f = found.facts;
				const reported = f.status ? Number(f.status) : null;
				// The provider's words lead once they are known; router codes are not provider words.
				if (f.message && f.tries > 0) {
					said.replaceChildren(h("span", { class: "act-said-k" }, reported ? `The provider answered HTTP ${reported}:` : "The provider answered:"), h("q", null, f.message));
					said.hidden = false;
					if (d.reason === "upstream-error") {
						const advice = providerAdvice(reported, f.message, { mine, vm, accountId: d.accountId, modelLabel: d.model ? modelName(d.model) : "this model" });
						hintText.textContent = advice.hint;
						setFix(advice.fix);
						next.hidden = false;
					}
				}
				reportLink.href = `#/logs?at=${r.at}`;
				reportLink.hidden = false;
				if (reported && !status && f.tries > 0) el.el.querySelector(".act-title")?.append(badge(`HTTP ${reported}`, "outline"));
			}).catch(() => {});
		}
		currentSheet = el;
		// Keep the selected row clear of the sheet.
		requestAnimationFrame(() => rowEl(r.id)?.scrollIntoView({ block: "nearest" }));
	}

	// ---------- start ----------

	syncClear();
	await load();
	if (selectedId) {
		const r = all.find((x) => x.id === selectedId) ?? (await neo.request(selectedId).catch(() => null));
		if (r && !disposed) open(r, { scroll: true });
		else if (!disposed) {
			toast("That request is no longer in the ledger.", "warn");
			selectedId = null;
			setUrl(null);
		}
	}
	// The stream says when something new arrived; the newest page is folded in
	// so the numbers and the list stay consistent and the selection is kept.
	const tick = setInterval(() => { if (loadedAt) live.title = `Updated ${fmtAgo(loadedAt)}`; }, 5000);
	const stop = neo.subscribeStream(() => loadLatest(), (error) => {
		live.classList.toggle("off", Boolean(error));
		live.lastChild.textContent = error ? "Updates paused" : "Live";
		live.title = error ? `Could not refresh: ${error.message}` : `Updated ${fmtAgo(loadedAt || Date.now())}`;
	});
	return () => {
		disposed = true;
		stop();
		clearInterval(tick);
		document.removeEventListener("keydown", pageKeys);
		currentSheet?.close();
	};
}
