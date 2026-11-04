// Activity: every request, with how it was routed and, when it failed, why.
// Admins see everyone; members see their own. Selecting a row docks a
// plain-words explanation, the routing steps and the raw record on the right.

import { h, svgEl, icon, fmtCompact, fmtDuration, fmtIn, clear } from "../lib/dom.js";
import { neo } from "../lib/client.js";
import { pageHead, card, btn, badge, avatar, servedName, search, select, seg, table, inspector, section, kv, trail, outcomeBadge, empty, spinner, errorState, copyLine, toast } from "../lib/components.js";
import { jsonView } from "../lib/json.js";
import { explainDecision } from "../lib/explain.js";

const LIMIT = 200;
const BUCKETS = 24;

// ---------- formatting ----------

const dayKey = (ts) => new Date(ts).toDateString();
function stamp(ts, { seconds = true } = {}) {
	const d = new Date(ts);
	const time = d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", ...(seconds ? { second: "2-digit" } : {}) });
	return dayKey(ts) === new Date().toDateString() ? time : `${d.toLocaleDateString("en-GB", { day: "2-digit", month: "short" })} ${time}`;
}
function spanLabel(ms) {
	const m = Math.round(ms / 60000);
	if (m < 1) return "under a minute";
	if (m < 90) return `${m} min`;
	const hr = Math.round(m / 60);
	if (hr < 48) return `${hr} h`;
	return `${Math.round(hr / 24)} days`;
}
const trimPct = (x) => `${x.toFixed(1).replace(/\.0$/, "")}%`;
function percentile(sorted, p) {
	if (!sorted.length) return null;
	return sorted[Math.max(0, Math.ceil(p * sorted.length) - 1)];
}

// ---------- page ----------

export async function render(ctx) {
	const { root, session } = ctx;
	const mine = session.role !== "admin";
	root.append(
		pageHead({
			title: "Activity",
			desc: mine ? "Your requests, how each one was answered, and why when it was not." : "Every request through the proxy: who asked, what they asked for, which stage and account answered, and why when nothing did.",
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

	const filters = { userId: mine ? session.userId : ctx.query.get("user") ?? "", outcome: ctx.query.get("outcome") ?? "", q: ctx.query.get("q") ?? "" };
	let selectedId = ctx.params[0] ? decodeURIComponent(ctx.params[0]) : null;
	let all = [];
	let signature = "";
	let loading = false;
	let disposed = false;
	let focusId = null;

	const accounts = mine ? [] : vault.accounts;
	const modelName = (id) => models.find((m) => m.id === id)?.display ?? id;
	const acctLabel = (id) => accounts.find((a) => a.id === id)?.label ?? "an account in the pool";
	const userOf = (r) => users.find((x) => x.id === r.userId);
	const explain = (r) => explainDecision(r, { models, accounts, users, vms, mine });
	const vmOf = (r) => (r.decision.virtualModel ? vms.find((v) => v.id === r.decision.virtualModel) : null);

	// ---------- layout ----------

	const stripHost = h("div", { class: "act-stats-host" });
	const segEl = seg(
		[
			{ value: "", label: "All" },
			{ value: "ok", label: "Served" },
			{ value: "limited", label: "Limited" },
			{ value: "queued", label: "Queued" },
			{ value: "error", label: "Errors" },
		],
		filters.outcome,
		(v) => {
			filters.outcome = v;
			setUrl(selectedId);
			renderTable();
		},
	);
	const searchEl = search("Filter by model name", (v) => {
		filters.q = v.trim();
		setUrl(selectedId);
		renderStrip();
		renderCounts();
		renderTable();
	});
	searchEl.querySelector("input").value = filters.q;
	const toolbar = h(
		"div",
		{ class: "row wrap page-toolbar act-toolbar" },
		searchEl,
		mine
			? null
			: select([{ value: "", label: "Everyone" }, ...users.map((u) => ({ value: u.id, label: u.handle }))], filters.userId, (v) => {
					filters.userId = v;
					setUrl(selectedId);
					load();
				}, { style: { width: "160px" }, "aria-label": "Filter by user" }),
		segEl,
		h("span", { class: "spacer" }),
		btn("Reload", { icon: "refresh", cls: "sm", onClick: () => load() }),
	);
	const tableHost = h("div", { class: `act-table ${mine ? "mine" : ""}` });
	root.append(stripHost, toolbar, card({ flush: true, body: tableHost }));

	// ---------- data ----------

	function matchesQuery(r) {
		if (!filters.q) return true;
		const needle = filters.q.toLowerCase();
		const d = r.decision;
		const hay = [r.asked, d.model, d.model ? modelName(d.model) : "", vmOf(r)?.name].filter(Boolean).join(" ").toLowerCase();
		return hay.includes(needle);
	}
	const statsRows = () => all.filter(matchesQuery);
	const visibleRows = () => statsRows().filter((r) => !filters.outcome || r.outcome === filters.outcome);

	async function load({ quiet = false } = {}) {
		if (loading || disposed) return;
		loading = true;
		try {
			const rows = await neo.requests({ limit: LIMIT, userId: filters.userId || undefined });
			if (disposed) return;
			const next = rows.map((r) => `${r.id}:${r.outcome}`).join("|");
			if (quiet && next === signature && all.length) return;
			signature = next;
			all = rows;
			renderStrip();
			renderCounts();
			renderTable();
		} catch (e) {
			if (!quiet || !all.length) tableHost.replaceChildren(errorState(e, () => load()));
		} finally {
			loading = false;
		}
	}

	// ---------- failure-rate strip ----------

	function renderStrip() {
		const rows = statsRows();
		if (!rows.length) {
			stripHost.replaceChildren();
			return;
		}
		const ok = rows.filter((r) => r.outcome === "ok");
		const limited = rows.filter((r) => r.outcome === "limited");
		const queued = rows.filter((r) => r.outcome === "queued");
		const errors = rows.filter((r) => r.outcome === "error");
		const lat = ok.map((r) => r.latencyMs).sort((a, b) => a - b);
		const times = rows.map((r) => r.at);
		const t0 = Math.min(...times);
		const t1 = Math.max(...times);
		const servedPct = (ok.length / rows.length) * 100;
		const servedText = ok.length === rows.length ? "100%" : trimPct(Math.min(servedPct, 99.9));
		const servedTone = servedPct >= 95 ? "ok" : servedPct >= 70 ? "warn" : "bad";
		const reasons = new Map();
		for (const r of errors) reasons.set(explain(r).headline, (reasons.get(explain(r).headline) ?? 0) + 1);
		const topReason = [...reasons.entries()].sort((a, b) => b[1] - a[1])[0];
		const cell = (label, value, sub, tone = "") => h("div", { class: "cell" }, h("div", { class: "label" }, label), h("div", { class: `value ${tone}` }, value), sub ? h("div", { class: "sub" }, sub) : null);
		stripHost.replaceChildren(
			h(
				"section",
				{ class: "card act-stats", "aria-label": "Failure rate for the loaded requests" },
				cell("Requests", String(rows.length), rows.length >= LIMIT ? `latest ${LIMIT}` : t1 > t0 ? `over ${spanLabel(t1 - t0)}` : "loaded"),
				cell("Served", servedText, `${ok.length} of ${rows.length} answered`, servedTone),
				cell("Limited", String(limited.length), queued.length ? `plus ${queued.length} queued` : "stopped by a threshold", limited.length ? "warn" : ""),
				cell("Errors", String(errors.length), topReason ? `mostly ${topReason[0]}` : "none", errors.length ? "bad" : ""),
				cell("Latency", lat.length ? fmtDuration(percentile(lat, 0.5)) : "–", lat.length ? `median · p95 ${fmtDuration(percentile(lat, 0.95))}` : "no served requests"),
				h("div", { class: "cell spark-cell" }, h("div", { class: "label" }, "Requests over time"), sparkline(rows, t0, t1), h("div", { class: "spark-axis" }, h("span", null, stamp(t0, { seconds: false })), h("span", { class: "legend" }, h("i", { class: "ok" }), "served", h("i", { class: "warn" }), "limited", h("i", { class: "bad" }), "errors"), h("span", null, stamp(t1, { seconds: false })))),
			),
		);
	}

	function sparkline(rows, t0, t1) {
		const width = Math.max(1, t1 - t0);
		const buckets = Array.from({ length: BUCKETS }, () => ({ ok: 0, held: 0, bad: 0 }));
		for (const r of rows) {
			const b = buckets[Math.min(BUCKETS - 1, Math.floor(((r.at - t0) / width) * BUCKETS))];
			if (r.outcome === "ok") b.ok++;
			else if (r.outcome === "error") b.bad++;
			else b.held++;
		}
		const peak = Math.sqrt(Math.max(1, ...buckets.map((b) => b.ok + b.held + b.bad)));
		const W = 240;
		const H = 44;
		const step = W / BUCKETS;
		const svg = svgEl("svg", { class: "spark", viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none", role: "img", "aria-label": `Requests over time in ${BUCKETS} slices: ${rows.filter((r) => r.outcome === "ok").length} served, ${rows.filter((r) => r.outcome === "error").length} errors` });
		buckets.forEach((b, i) => {
			const x = i * step + 1;
			const w = step - 2;
			const from = t0 + (width * i) / BUCKETS;
			const total = b.ok + b.held + b.bad;
			const g = svgEl("g", {}, svgEl("title", {}, `${stamp(from)}: ${b.ok} served, ${b.held} limited or queued, ${b.bad} errors`));
			if (!total) g.append(svgEl("rect", { class: "none", x, y: H - 1.5, width: w, height: 1.5 }));
			let y = H;
			for (const [cls, n] of [["ok", b.ok], ["warn", b.held], ["bad", b.bad]]) {
				if (!n) continue;
				const hh = Math.max(2, (Math.sqrt(total) / peak) * (H - 2) * (n / total));
				y -= hh;
				g.append(svgEl("rect", { class: cls, x, y, width: w, height: hh - (cls === "ok" && (b.held || b.bad) ? 0.75 : 0), rx: 1 }));
			}
			svg.append(g);
		});
		return svg;
	}

	// ---------- toolbar counts ----------

	function renderCounts() {
		const rows = statsRows();
		const counts = { "": rows.length };
		for (const r of rows) counts[r.outcome] = (counts[r.outcome] ?? 0) + 1;
		const labels = { "": "All", ok: "Served", limited: "Limited", queued: "Queued", error: "Errors" };
		[...segEl.children].forEach((b, i) => {
			const key = ["", "ok", "limited", "queued", "error"][i];
			b.replaceChildren(labels[key], h("span", { class: "seg-n" }, String(counts[key] ?? 0)));
		});
	}

	// ---------- table ----------

	function answeredBy(r) {
		const d = r.decision;
		if (d.outcome === "ok") {
			const a = accounts.find((x) => x.id === d.accountId);
			const where = Number.isInteger(d.stageIndex) ? ` · stage ${d.stageIndex + 1}` : d.virtualModel ? " · fall through" : " · direct";
			const text = `${modelName(d.model)}${where}${a ? ` · ${a.label}` : ""}`;
			return h("span", { title: text }, modelName(d.model), h("span", { class: "ink-3" }, where), a ? h("span", { class: "ink-3" }, ` · ${a.label}`) : null);
		}
		const text = `${d.outcome === "error" && d.model ? `${modelName(d.model)} · ` : ""}${explain(r).headline}`;
		return h("span", { class: d.outcome === "error" ? "act-fail" : "ink-3", title: d.message ?? text }, text);
	}

	function renderTable() {
		const rows = visibleRows();
		const scroller = root;
		const before = scroller.scrollTop;
		const heightBefore = tableHost.offsetHeight;
		const wasTop = before < 8;
		clear(tableHost);
		tableHost.append(
			table(
				[
					{ key: "t", label: "Time", width: "1%", render: (r) => h("span", { class: "mono ink-3", dataset: { request: r.id } }, stamp(r.at)) },
					...(mine ? [] : [{ key: "who", label: "User", width: "1%", render: (r) => { const u = userOf(r); return h("span", { class: "row", title: u?.handle ?? r.userId }, avatar(u ?? { handle: "?" }, "sm"), h("span", { class: "act-handle" }, u?.handle ?? r.userId)); } }]),
					{ key: "client", label: "Client", clip: true, width: "15%", render: (r) => h("span", { class: "ink-2", title: r.client }, r.client) },
					{ key: "asked", label: "Asked for", clip: true, width: "30%", render: (r) => servedName(r.asked, { virtual: r.asked.replace(/^neoproxy\//, "").split("/").length === 1 }) },
					{ key: "served", label: "Answered by", clip: true, width: "50%", render: answeredBy },
					{ key: "tok", label: "Tokens", width: "1%", num: true, render: (r) => (r.outcome === "ok" ? fmtCompact(r.tokens.in + r.tokens.out) : "–") },
					{ key: "lat", label: "Latency", width: "1%", num: true, render: (r) => fmtDuration(r.latencyMs) },
					{ key: "out", label: "", width: "1%", render: (r) => outcomeBadge(r.outcome, r.decision) },
				],
				rows,
				{ onRow: toggle, selectedKey: selectedId, emptyState: empty({ icon: "activity", title: all.length ? "No requests match" : "No requests yet", hint: all.length ? "Change the filters to see more of the loaded requests." : "Requests show up here as soon as a client uses the proxy.", action: all.length && (filters.q || filters.outcome) ? btn("Clear filters", { cls: "outline", onClick: clearFilters }) : null }) },
			),
			all.length >= LIMIT ? h("div", { class: "act-foot" }, `Showing the latest ${LIMIT} requests. Pick a user to narrow it down.`) : null,
		);
		if (!wasTop) scroller.scrollTop = before + (tableHost.offsetHeight - heightBefore);
	}

	function clearFilters() {
		filters.q = "";
		filters.outcome = "";
		searchEl.querySelector("input").value = "";
		[...segEl.children].forEach((b, i) => b.setAttribute("aria-pressed", String(i === 0)));
		setUrl(selectedId);
		renderStrip();
		renderCounts();
		renderTable();
	}

	function markSelected() {
		for (const tr of tableHost.querySelectorAll("tbody tr")) {
			const id = tr.querySelector("[data-request]")?.dataset.request;
			tr.setAttribute("aria-selected", String(id === selectedId));
		}
	}

	// ---------- url ----------

	function setUrl(id) {
		const q = new URLSearchParams();
		if (!mine && filters.userId) q.set("user", filters.userId);
		if (filters.outcome) q.set("outcome", filters.outcome);
		if (filters.q) q.set("q", filters.q);
		const hash = `#/activity${id ? `/${encodeURIComponent(id)}` : ""}${q.size ? `?${q}` : ""}`;
		if (location.hash !== hash) history.replaceState(null, "", hash);
	}

	// ---------- inspector ----------

	function toggle(r) {
		if (r.id === selectedId) close();
		else open(r);
	}

	function focusRow() {
		const row = tableHost.querySelector(`[data-request="${CSS.escape(focusId ?? "")}"]`)?.closest("tr");
		(row ?? tableHost.querySelector(".table-scroll") ?? toolbar.querySelector("input"))?.focus({ preventScroll: true });
	}

	function close() {
		focusId = selectedId;
		selectedId = null;
		markSelected();
		setUrl(null);
		ctx.setInspector(null);
	}

	function open(r, { scroll = false } = {}) {
		selectedId = r.id;
		focusId = r.id;
		markSelected();
		setUrl(r.id);
		if (scroll) tableHost.querySelector(`[data-request="${CSS.escape(r.id)}"]`)?.closest("tr")?.scrollIntoView({ block: "nearest" });
		const d = r.decision;
		const u = userOf(r);
		const key = u?.keys?.find((k) => k.id === r.keyId);
		const vm = vmOf(r);
		const who = u?.handle ?? (mine ? "you" : "this user");
		const { sentences, hint, status } = explain(r);
		const answeredStage = Number.isInteger(d.stageIndex) ? ` · stage ${d.stageIndex + 1}` : d.virtualModel ? " · fall through" : d.model ? " · direct" : "";
		let rawBuilt = false;
		const raw = h("details", { class: "act-raw" }, h("summary", null, icon("chevronRight", 13), "Raw record"));
		raw.addEventListener("toggle", () => {
			if (!raw.open || rawBuilt) return;
			rawBuilt = true;
			raw.append(jsonView(r, { collapsedDepth: 1 }));
		});
		const el = inspector({
			title: servedName(r.asked, { virtual: r.asked.replace(/^neoproxy\//, "").split("/").length === 1 }),
			sub: `${new Date(r.at).toLocaleString("en-GB", { day: "2-digit", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" })} · ${r.id.slice(0, 12)}…`,
			onClose: close,
			body: [
				h("div", { class: "row wrap act-badges" }, outcomeBadge(r.outcome, d), status ? badge(`HTTP ${status}`, "outline") : null, d.effort ? badge(`effort ${d.effort}`, "violet") : null, r.stream ? badge("stream") : null),
				section("What happened", h("div", { class: `act-why ${r.outcome}` }, h("p", null, sentences.join(" ")), hint ? h("p", { class: "next" }, h("span", null, "What to do: "), hint) : null)),
				section("How it was routed", trail(d, { models, accounts, users, admin: !mine, who })),
				section(
					"Request facts",
					kv([
						["Asked for", servedName(r.asked, { virtual: r.asked.replace(/^neoproxy\//, "").split("/").length === 1 })],
						d.outcome === "ok" ? ["Answered by", `${modelName(d.model)}${answeredStage}`] : null,
						d.outcome === "ok" && !mine ? ["Account", acctLabel(d.accountId)] : null,
						d.outcome !== "ok" && d.model ? ["Tried", `${modelName(d.model)} on ${acctLabel(d.accountId)}`] : null,
						vm ? ["Virtual model", h("span", { class: "mono" }, vm.name)] : null,
						["User", u ? h("span", { class: "row" }, avatar(u, "sm"), u.handle) : h("span", null, h("span", { class: "ink-3" }, "Removed user · "), h("code", null, r.userId))],
						key ? ["Key", h("code", null, `${key.prefix} · ${key.label}`)] : null,
						["Client", r.client],
						["Streaming", r.stream ? "yes" : "no"],
						["Tokens", r.outcome === "ok" ? `${fmtCompact(r.tokens.in)} in · ${fmtCompact(r.tokens.out)} out${r.tokens.cached ? ` · ${fmtCompact(r.tokens.cached)} cached` : ""}` : "none, nothing was served"],
						["Latency", fmtDuration(r.latencyMs)],
						["Request id", copyLine(r.id, "request id")],
					]),
				),
				raw,
			],
			foot: !mine ? [vm ? btn("Open chain", { icon: "route", cls: "outline", onClick: () => ctx.navigate(`virtual-models/${vm.id}`) }) : null, btn("Open user", { icon: "user", cls: "outline", onClick: () => ctx.navigate(`users/${r.userId}`) })] : null,
		});
		el.setAttribute("aria-label", `Request ${r.asked} for ${who}`);
		ctx.setInspector(el, { restoreFocus: focusRow });
	}

	// ---------- start ----------

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
	// The stream tells us when something new arrived; refetching keeps the strip,
	// counts and table consistent and never touches the selection.
	const stop = neo.subscribeStream(() => load({ quiet: true }), () => {});
	return () => {
		disposed = true;
		stop();
	};
}
