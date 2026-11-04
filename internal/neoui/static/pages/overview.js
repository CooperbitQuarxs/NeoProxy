// Overview: the headline numbers, then an access map: three columns, providers
// → virtual models → people, joined by uniform lines that mean "draws on" and
// "is granted to" (setup, not traffic). Select a row to trace what it touches;
// select it again to open it. Under it, the live request stream and the
// things that need the admin's attention. Phones lead with what needs doing
// and the stream, and fold the map into a summary with one card per virtual model.

import { h, svgEl, clear, icon, fmtPct, fmtAgo, fmtIn, fmtInt, fmtTime, plural } from "../lib/dom.js";
import { neo, onChange } from "../lib/client.js";
import { pageHead, card, btn, avatar, ring, servedName, outcomeBadge, empty, spinner, errorState, meterRing, preferredMeter, providerDot } from "../lib/components.js";
import { accountHealth } from "../lib/providers.js";
import { explainDecision } from "../lib/explain.js";
import { requestsSince, DAY } from "../lib/ledger.js";

export async function render(ctx) {
	const { root } = ctx;
	root.append(
		pageHead({
			title: "Overview",
			desc: "What is set up, who may use it, and how requests are going.",
			actions: [btn("New virtual model", { icon: "plus", cls: "primary", onClick: () => ctx.navigate("virtual-models/new") })],
		}),
	);
	const host = h("div", { class: "ov" });
	root.append(host, spinner());
	let world;
	try {
		const [vault, models, vms, users, usage, day, core] = await Promise.all([neo.vault(), neo.models(), neo.virtualModels(), neo.users(), neo.usage(), requestsSince(Date.now() - DAY), neo.core()]);
		world = { ...vault, models, vms, users, usage, requests: day.rows, requestsComplete: day.complete, core };
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	const flow = flowBoard(world, ctx);
	const recent = streamCard(world, ctx);
	host.append(statsStrip(world), flow.el, h("div", { class: "grid c2-1 ov-lower" }, recent.el, attentionCard(world, ctx)));

	const off = onChange((w) => w !== "requests" && ctx.rerender());
	return () => {
		off();
		flow.dispose();
		recent.dispose();
	};
}

// ---------- headline numbers ----------
// Each number is a link to the page that manages it, in the flow's order.

function statsStrip(world) {
	// world.requests is the whole last 24 hours (lib/ledger.js), unless it held
	// more than the dashboard loads; then every number says it is a lower bound.
	const today = world.requests;
	const full = world.requestsComplete;
	const atLeast = (n) => (full ? fmtInt(n) : `${fmtInt(n)}+`);
	const held = today.filter((r) => r.outcome === "limited" || r.outcome === "queued");
	const failed = today.filter((r) => r.outcome === "error");
	const healthy = world.accounts.filter((a) => accountHealth(a).kind === "ok").length;
	const attention = world.accounts.length - healthy;
	const served = world.models.filter((m) => m.exposed && world.providers.find((p) => p.id === m.providerId)?.exposed).length;
	const active = world.users.filter((u) => u.state === "active").length;
	const services = world.users.filter((u) => u.role === "service").length;
	const servedPct = today.length ? Math.round(((today.length - held.length - failed.length) / today.length) * 100) : null;
	const cell = (href, label, value, sub, tone = "") =>
		h("a", { class: "ov-stat", href: `#/${href}` }, h("span", { class: "label" }, label), h("span", { class: `value ${tone}` }, value), h("span", { class: "sub" }, sub));
	const flag = (text, tone) => h("span", { class: `ov-flag ${tone}` }, text);
	return h(
		"nav",
		{ class: "card rise ov-stats", "aria-label": "At a glance" },
		cell("pool", "Provider accounts", String(world.accounts.length), attention ? [`${healthy} healthy · `, flag(`${attention} ${attention === 1 ? "needs" : "need"} a look`, "warn")] : "all healthy"),
		cell("virtual-models", "Virtual models", String(world.vms.length), `${world.vms.filter((vm) => world.users.some((u) => u.grants.some((g) => g.virtualModel === vm.id))).length} granted · ${served} direct models`),
		cell("users", "People", String(world.users.length), `${active} active${services ? ` · ${plural(services, "service")}` : ""}`),
		cell("activity", "Requests, last\u00a024\u00a0h", atLeast(today.length), servedPct === null ? "none in the last day" : `${full ? "" : `of the latest ${fmtInt(today.length)}: `}${servedPct}% served${failed.length ? ` · ${fmtInt(failed.length)} failed` : ""}`),
		cell("activity?outcome=limited", "Held by limits, last\u00a024\u00a0h", atLeast(held.length), held.length ? `${plural(new Set(held.map((r) => r.userId)).size, "person", "people")} affected` : "nobody was held back", held.length ? "warn" : ""),
	);
}

// ---------- flow board ----------

function flowBoard(world, ctx) {
	const { providers, accounts, models, vms, users } = world;
	let selected = null; // "p:<id>" | "v:<id>" | "u:<id>"
	const modelName = (id) => models.find((m) => m.id === id)?.display ?? id;
	const providerOf = (id) => providers.find((p) => p.id === id);
	const grantedTo = (vm) => users.filter((u) => u.grants.some((g) => g.virtualModel === vm.id));
	// The providers a virtual model draws on: its steps, and the final rule's model when it passes requests on.
	const drawsOn = (vm) => [...new Set([...vm.stages.map((s) => s.model), ...(vm.terminal?.kind === "passthrough" && vm.terminal.model ? [vm.terminal.model] : [])].map((m) => m.split("/")[0]))];

	// Lines mean a relationship, so they are all the same width: "draws on" and "granted to".
	const edges = [];
	for (const vm of vms) for (const pid of drawsOn(vm)) edges.push({ from: `p:${pid}`, to: `v:${vm.id}` });
	for (const u of users) for (const g of u.grants) edges.push({ from: `v:${g.virtualModel}`, to: `u:${u.id}` });
	// Direct provider access has no line (it would cross the middle column); tracing a person or a provider lights it.
	const direct = [];
	for (const u of users) for (const pid of u.providerAccess ?? []) direct.push({ from: `p:${pid}`, to: `u:${u.id}` });

	const rowEls = new Map();
	// Selecting a row traces it; selecting it again opens it (so a double-click opens too).
	const frow = (key, { lead, main, sub, trail, href }) => {
		const el = h(
			"button",
			{
				type: "button",
				class: "frow",
				"aria-pressed": "false",
				dataset: { key },
				onClick: () => (selected === key && href ? ctx.navigate(href) : select(key)),
				title: "Select to trace it, select again to open it",
			},
			lead,
			h("div", { class: "ft" }, h("div", { class: "main" }, main), h("div", { class: "sub" }, sub)),
			trail,
		);
		rowEls.set(key, el);
		return el;
	};

	const provCol = h("div", { class: "col col-pool" }, h("div", { class: "col-label" }, "Providers", h("span", { class: "count" }, `· ${plural(accounts.length, "account")}`)));
	const feeds = (p) => vms.some((vm) => drawsOn(vm).includes(p.id));
	const provUsage = (p) => {
		const used = accounts.filter((a) => a.providerId === p.id).map((a) => preferredMeter(a)?.used).filter(Number.isFinite);
		return used.length ? used.reduce((x, y) => x + y, 0) / used.length : null;
	};
	const ordered = [...providers].sort((a, b) => Number(feeds(b)) - Number(feeds(a)) || (provUsage(b) ?? -1) - (provUsage(a) ?? -1) || a.name.localeCompare(b.name));
	const idle = ordered.filter((p) => !feeds(p));
	const provRow = (p) => {
		const accs = accounts.filter((a) => a.providerId === p.id);
		const avg = provUsage(p);
		const metered = accs.filter((a) => preferredMeter(a)).length;
		const unhealthy = accs.filter((a) => accountHealth(a).kind !== "ok").length;
		const sample = accs.find((a) => preferredMeter(a));
		const lead = sample
			? ring({ used: avg, size: 30, label: false, title: `${fmtPct(avg)} of the ${preferredMeter(sample).kind} window used, averaged over ${plural(metered, "metered account")}` })
			: meterRing(accs[0] ?? { meters: [] }, { size: 30 });
		const windows = accs.length === 1 && accs[0].meters?.length ? (accs[0].meters.filter((m) => m.kind === "5h" || m.kind === "week").length ? accs[0].meters.filter((m) => m.kind === "5h" || m.kind === "week") : accs[0].meters.slice(0, 2)) : null;
		return frow(`p:${p.id}`, {
			lead,
			main: h("span", { class: "row", style: { gap: "7px" } }, providerDot(p, 7), p.name),
			sub: !p.exposed ? "hidden from users" : unhealthy ? `${plural(accs.length, "account")} · ${unhealthy} ${unhealthy === 1 ? "needs" : "need"} attention` : windows ? `1 account · ${windows.map((m) => `${m.kind} ${fmtPct(m.used)} used`).join(" · ")}` : `${plural(accs.length, "account")} · ${avg === null ? "no usage meter" : `${fmtPct(avg)} used on average`}`,
			trail: p.exposed ? null : icon("eyeOff", 14),
			href: "pool",
		});
	};
	for (const p of ordered.filter(feeds)) provCol.append(provRow(p));
	// Providers no virtual model draws on fold away so the lines stay readable.
	if (idle.length) {
		const fold = h("div", { class: "fold" });
		let open = false;
		const toggleRow = h(
			"button",
			{ type: "button", class: "frow fold-toggle", "aria-expanded": "false", onClick: () => { open = !open; toggleRow.setAttribute("aria-expanded", String(open)); fold.classList.toggle("open", open); toggleLabel.textContent = open ? "Hide them" : `${idle.length} more providers`; requestAnimationFrame(draw); } },
			h("span", { class: "fold-dots" }, idle.slice(0, 6).map((p) => providerDot(p, 6))),
			h("div", { class: "ft" }, h("div", { class: "main" }, ""), h("div", { class: "sub" }, "no virtual model draws on them")),
			icon("chevronDown", 14),
		);
		const toggleLabel = toggleRow.querySelector(".main");
		toggleLabel.textContent = `${idle.length} more providers`;
		for (const p of idle) fold.append(provRow(p));
		provCol.append(toggleRow, fold);
	}

	const chainWords = (vm) => {
		const first = vm.stages[0] ? modelName(vm.stages[0].model) : "no models yet";
		const more = vm.stages.length - 1;
		return `${first}${more > 0 ? ` + ${plural(more, "fallback")}` : ""}`;
	};
	const grantWords = (list) => (list.length ? `granted to ${plural(list.length, "person", "people")}` : "granted to nobody yet");
	const vmCol = h("div", { class: "col col-vms" }, h("div", { class: "col-label" }, "Virtual models", h("span", { class: "count" }, `· ${vms.length}`)));
	for (const vm of vms) {
		vmCol.append(
			frow(`v:${vm.id}`, {
				lead: h("span", { class: "avatar", style: { borderRadius: "8px" } }, icon("route", 14)),
				main: servedName(vm.served, { virtual: true }),
				sub: `${chainWords(vm)} · ${grantWords(grantedTo(vm))}`,
				href: `virtual-models/${vm.id}`,
			}),
		);
	}

	const userCol = h("div", { class: "col col-users" }, h("div", { class: "col-label" }, "People", h("span", { class: "count" }, `· ${users.length}`)));
	for (const u of users) {
		const last = world.requests.find((r) => r.userId === u.id);
		const directN = (u.providerAccess ?? []).length;
		userCol.append(
			frow(`u:${u.id}`, {
				lead: avatar(u),
				main: u.handle,
				sub: u.state !== "active" ? u.state : `${u.role} · ${plural(u.grants.length, "virtual model")}${directN ? ` + ${directN} direct` : ""}${last ? ` · last request ${fmtAgo(last.at)}` : ""}`,
				href: `users/${u.id}`,
			}),
		);
	}

	const lines = svgEl("svg", { class: "ribbons", "aria-hidden": "true" });
	const board = h("div", { class: "flow" }, lines, provCol, h("div"), vmCol, h("div"), userCol);
	const boardHost = h("div", { class: "flow-host" }, board);
	const pathEls = [];

	const draw = () => {
		clear(lines);
		pathEls.length = 0;
		const b = board.getBoundingClientRect();
		if (!b.width) return;
		lines.setAttribute("viewBox", `0 0 ${b.width} ${b.height}`);
		for (const e of edges) {
			const a = rowEls.get(e.from)?.getBoundingClientRect();
			const c = rowEls.get(e.to)?.getBoundingClientRect();
			if (!a?.height || !c?.height) continue;
			const x1 = a.right - b.left;
			const y1 = a.top + a.height / 2 - b.top;
			const x2 = c.left - b.left;
			const y2 = c.top + c.height / 2 - b.top;
			const mx = (x1 + x2) / 2;
			const p = svgEl("path", { d: `M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`, "stroke-width": "2", class: e.from.startsWith("p:") ? "" : "cyan" });
			p.dataset.from = e.from;
			p.dataset.to = e.to;
			lines.append(p);
			pathEls.push(p);
		}
		applySelection();
	};

	const legend = () => [h("span", { class: "flow-legend" }, h("span", null, h("i", { style: { background: "var(--accent-b)" } }), "virtual model draws on provider"), h("span", null, h("i", { style: { background: "var(--accent-a)" } }), "virtual model granted to person")), h("span", { class: "spacer" }), h("span", { class: "fact flow-hint" }, "Select a row to trace it, select it again to open it. Arrow keys move between rows.")];
	const foot = h("div", { class: "card-foot flow-foot" }, legend());
	const endWords = { passthrough: "then its final rule passes requests on without limits", queue: "then queues", limit: "then refuses with a limit error" };

	function applySelection() {
		for (const [key, el] of rowEls) {
			el.setAttribute("aria-pressed", String(selected === key));
			el.classList.remove("lit", "dim");
		}
		for (const p of pathEls) p.classList.remove("lit", "dim");
		if (!selected) return;
		// Trace away from the selection: a provider lights the virtual models that
		// draw on it and who they are granted to; a person lights their virtual
		// models and the providers behind them; a virtual model lights one hop each way.
		// Direct provider access lights too, with no line.
		const lit = new Set([selected]);
		const litEdges = new Set();
		const downstream = (key) => {
			for (const e of edges) if (e.from === key) { lit.add(e.to); litEdges.add(e); downstream(e.to); }
		};
		const upstream = (key) => {
			for (const e of edges) if (e.to === key) { lit.add(e.from); litEdges.add(e); upstream(e.from); }
		};
		if (selected.startsWith("p:")) { downstream(selected); for (const e of direct) if (e.from === selected) lit.add(e.to); }
		else if (selected.startsWith("u:")) { upstream(selected); for (const e of direct) if (e.to === selected) lit.add(e.from); }
		else for (const e of edges) if (e.from === selected || e.to === selected) { lit.add(e.from); lit.add(e.to); litEdges.add(e); }
		for (const [key, el] of rowEls) if (key !== selected) el.classList.add(lit.has(key) ? "lit" : "dim");
		for (const p of pathEls) p.classList.add([...litEdges].some((e) => e.from === p.dataset.from && e.to === p.dataset.to) ? "lit" : "dim");
		renderFoot();
	}

	const names = (list) => list.join(", ");
	function renderFoot() {
		clear(foot);
		if (!selected) {
			foot.append(...legend());
			return;
		}
		const [kind, id] = selected.split(":");
		if (kind === "p") {
			const p = providerOf(id);
			const touched = vms.filter((vm) => drawsOn(vm).includes(id));
			const directUsers = users.filter((u) => (u.providerAccess ?? []).includes(id));
			foot.append(h("span", { class: "flow-sum" }, h("b", null, p.name), ` · ${plural(accounts.filter((a) => a.providerId === id).length, "account")} · ${touched.length ? `${plural(touched.length, "virtual model")} ${touched.length === 1 ? "draws" : "draw"} on it: ${names(touched.map((v) => v.name))}` : "no virtual model draws on it"}${directUsers.length ? ` · direct access: ${names(directUsers.map((u) => u.handle))}` : ""}`), h("span", { class: "spacer" }), btn("Open in Pool", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate("pool") }));
		} else if (kind === "v") {
			const vm = vms.find((x) => x.id === id);
			const granted = grantedTo(vm);
			foot.append(h("span", { class: "flow-sum" }, servedName(vm.served, { virtual: true }), ` · ${vm.stages.map((s) => modelName(s.model)).join(" → ") || "no models"} ${endWords[vm.terminal?.kind] ?? endWords.limit} · ${granted.length ? `granted to ${names(granted.map((u) => u.handle))}` : "granted to nobody yet"}`), h("span", { class: "spacer" }), btn("Open editor", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate(`virtual-models/${vm.id}`) }));
		} else {
			const u = users.find((x) => x.id === id);
			const own = u.grants.map((g) => vms.find((v) => v.id === g.virtualModel)?.name).filter(Boolean);
			const dir = (u.providerAccess ?? []).map((pid) => providerOf(pid)?.name ?? pid);
			foot.append(h("span", { class: "flow-sum" }, h("b", null, u.handle), own.length ? ` is granted ${names(own)}` : " is granted no virtual models", dir.length ? ` and has direct access to ${names(dir)}` : ", and has no direct provider access"), h("span", { class: "spacer" }), btn("Open user", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate(`users/${u.id}`) }));
		}
	}

	function select(key) {
		selected = selected === key ? null : key;
		applySelection();
		if (!selected) renderFoot();
	}

	// Phones: a summary, and on request one card per virtual model holding what it
	// draws on and who it is granted to together, so nothing is traced across lists.
	const feeding = providers.filter(feeds);
	const grantedPeople = users.filter((u) => u.grants.length).length;
	const phoneList = h("div", { class: "fp-list", hidden: true });
	const toggleText = h("span", null, `Show the ${plural(vms.length, "virtual model")}`);
	const phoneToggle = h("button", { type: "button", class: "fp-toggle", "aria-expanded": "false", onClick: () => { const open = phoneList.hidden; phoneList.hidden = !open; phoneToggle.setAttribute("aria-expanded", String(open)); toggleText.textContent = open ? "Hide the virtual models" : `Show the ${plural(vms.length, "virtual model")}`; } }, toggleText, icon("chevronDown", 14));
	const phoneCard = (vm) => {
		const granted = grantedTo(vm);
		const chip = (href, lead, text, sub) => h("a", { class: "fp-chip", href: `#/${href}` }, lead, h("span", null, text), sub ? h("span", { class: "fp-chip-sub" }, sub) : null);
		return h("div", { class: "fp-card", hidden: true },
			h("div", { class: "fp-k" }, "Draws on"),
			h("div", { class: "fp-chips" }, drawsOn(vm).map((pid) => { const p = providerOf(pid); return chip("pool", providerDot(p, 7), p?.name ?? pid, plural(accounts.filter((a) => a.providerId === pid).length, "account")); })),
			h("div", { class: "fp-k" }, "Steps"),
			h("p", { class: "fact fp-chain" }, `${vm.stages.map((s) => modelName(s.model)).join(" → ") || "No models yet"}, ${endWords[vm.terminal?.kind] ?? endWords.limit}.`),
			h("div", { class: "fp-k" }, "Granted to"),
			granted.length ? h("div", { class: "fp-chips" }, granted.map((u) => chip(`users/${u.id}`, avatar(u, "sm"), u.handle))) : h("p", { class: "fact" }, "Nobody yet."),
			btn("Open editor", { icon: "arrowRight", cls: "sm outline", onClick: () => ctx.navigate(`virtual-models/${vm.id}`) }),
		);
	};
	for (const vm of vms) {
		const body = phoneCard(vm);
		const row = h("button", { type: "button", class: "fp-row", "aria-expanded": "false", onClick: () => {
			const open = body.hidden;
			for (const other of phoneList.querySelectorAll(".fp-row[aria-expanded=true]")) { other.setAttribute("aria-expanded", "false"); other.nextElementSibling.hidden = true; }
			body.hidden = !open;
			row.setAttribute("aria-expanded", String(open));
		} },
		h("span", { class: "avatar", style: { borderRadius: "8px" } }, icon("route", 14)),
		h("span", { class: "ft" }, h("span", { class: "main" }, servedName(vm.served, { virtual: true })), h("span", { class: "sub" }, `${drawsOn(vm).map((pid) => providerOf(pid)?.name ?? pid).join(", ") || "no provider"} · ${grantWords(grantedTo(vm))}`)),
		icon("chevronDown", 14));
		phoneList.append(row, body);
	}
	const phone = h("div", { class: "flow-phone" },
		h("p", { class: "fact fp-sum" }, `${plural(vms.length, "virtual model")} ${vms.length === 1 ? "draws" : "draw"} on ${plural(accounts.filter((a) => feeding.some((p) => p.id === a.providerId)).length, "account")} at ${plural(feeding.length, "provider")} and ${vms.length === 1 ? "is" : "are"} granted to ${plural(grantedPeople, "person", "people")}.`),
		phoneToggle,
		phoneList,
	);

	const el = card({
		title: "Access map",
		sub: "Which providers each virtual model draws on, and who it is granted to. It shows setup, not traffic; for traffic, see Recent requests.",
		cls: "ov-flow",
		flush: true,
		body: [boardHost, phone],
		rise: true,
	});
	el.append(foot);
	// Up and Down move within a column, Left and Right jump to the nearest row in the next column.
	board.addEventListener("keydown", (e) => {
		const row = e.target.closest?.(".frow");
		if (!row || e.altKey || e.ctrlKey || e.metaKey || !["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"].includes(e.key)) return;
		const cols = [provCol, vmCol, userCol];
		const rowsOf = (col) => [...col.querySelectorAll(".frow")].filter((x) => x.offsetParent);
		const ci = cols.findIndex((c) => c.contains(row));
		let target;
		if (e.key === "ArrowUp" || e.key === "ArrowDown") {
			const list = rowsOf(cols[ci]);
			target = list[list.indexOf(row) + (e.key === "ArrowDown" ? 1 : -1)];
		} else {
			const y = row.getBoundingClientRect().top;
			for (let c = ci + (e.key === "ArrowRight" ? 1 : -1); c >= 0 && c < cols.length && !target; c += e.key === "ArrowRight" ? 1 : -1) {
				const list = rowsOf(cols[c]);
				target = list.reduce((best, x) => (!best || Math.abs(x.getBoundingClientRect().top - y) < Math.abs(best.getBoundingClientRect().top - y) ? x : best), null);
			}
		}
		if (!target) return;
		e.preventDefault();
		target.focus();
	});
	const ro = new ResizeObserver(() => draw());
	ro.observe(board);
	requestAnimationFrame(draw);
	const esc = (e) => {
		if (e.key === "Escape" && selected && !document.querySelector('[role="dialog"]')) select(selected);
	};
	document.addEventListener("keydown", esc);
	return { el, dispose: () => { ro.disconnect(); document.removeEventListener("keydown", esc); } };
}

// ---------- stream ----------

const STREAM_ROWS = 10;

function streamCard(world, ctx) {
	const list = h("div", { class: "stream ov-stream" });
	const isVirtual = (asked) => asked.replace(/^neoproxy\//, "").split("/").length === 1;
	const rowFor = (r, isNew = false) => {
		const u = world.users.find((x) => x.id === r.userId);
		const d = r.decision;
		const model = d.model ? world.models.find((m) => m.id === d.model) : null;
		const acc = d.accountId ? world.accounts.find((a) => a.id === d.accountId) : null;
		const why = d.outcome === "ok" ? null : explainDecision(r, world);
		return h(
			"a",
			{ class: `srow ${isNew ? "new" : ""}`, href: `#/activity/${encodeURIComponent(r.id)}`, title: "Open this request in Activity" },
			h("span", { class: "t" }, fmtTime(r.at)),
			h("span", { class: "who row" }, avatar(u ?? { handle: "?" }, "sm"), h("span", { class: "nm" }, u?.handle ?? "removed user")),
			h("span", { class: "route asked" }, servedName(r.asked, { virtual: isVirtual(r.asked) })),
			h(
				"span",
				{ class: "route ans" },
				d.outcome === "ok"
					? [icon("arrowRight", 12), h("span", { class: "hop" }, model?.display ?? d.model), acc ? h("span", { class: "ink-3 hop acc" }, `· ${acc.label}`) : null]
					: [icon("stop", 12), h("span", { class: `hop ${d.outcome === "error" ? "bad" : "held"}`, title: why.sentences.join(" ") }, why.headline)],
			),
			h("span", { class: "out" }, outcomeBadge(r.outcome)),
		);
	};
	for (const r of world.requests.slice(0, STREAM_ROWS)) list.append(rowFor(r));
	const live = h("span", { class: "act-live", role: "status" }, h("i"), "Live");
	if (!world.requests.length) list.append(empty({ icon: "activity", title: "No requests yet", hint: "Requests appear here as soon as a client uses this proxy." }));
	const unsubscribe = neo.subscribeStream((r) => {
		list.querySelector(".empty")?.remove();
		list.prepend(rowFor(r, true));
		while (list.children.length > STREAM_ROWS) list.lastChild.remove();
	}, (error) => {
		live.classList.toggle("off", Boolean(error));
		live.lastChild.textContent = error ? "Updates paused" : "Live";
		live.title = error ? `Could not refresh: ${error.message}` : "New requests appear as they arrive";
	}, world.requests);
	const el = card({
		title: "Recent requests",
		sub: "Open one to see why it went where it did.",
		actions: [live, btn("All activity", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate("activity") })],
		flush: true,
		body: list,
		cls: "ov-recent",
	});
	return { el, dispose: unsubscribe };
}

// ---------- attention ----------

// What is wrong with an account and what to do, in plain words; the row opens the account.
function plainHealth(hs) {
	const text = hs.text ?? "";
	if (/sign-in expired/i.test(text)) return "Its sign-in has expired. Open it and sign in again.";
	if (/disabled/i.test(text)) return "Turned off, so no request uses it. Open it to turn it back on.";
	if (/last test failed/i.test(text)) return "Its last test failed. Open it to test again or see why.";
	if (/window spent/i.test(text)) return `${text[0].toUpperCase()}${text.slice(1)}; it is skipped until the window resets.`;
	return `${text[0]?.toUpperCase() ?? ""}${text.slice(1)}. Open it to see why.`;
}

function attentionCard(world, ctx) {
	const items = [];
	for (const a of world.accounts) {
		const p = world.providers.find((x) => x.id === a.providerId);
		const who = `${p?.name ?? a.providerId} · ${a.label}`;
		const hs = accountHealth(a);
		if (hs.kind === "bad") items.push({ kind: "bad", title: who, text: plainHealth(hs), href: `pool/${a.id}` });
		else if (a.state === "cooldown") items.push({ kind: "warn", title: who, text: `Cooling down, back in ${fmtIn(a.cooldownUntil)}.`, href: `pool/${a.id}` });
		else if (hs.kind === "warn") items.push({ kind: "warn", title: who, text: plainHealth(hs), href: `pool/${a.id}` });
		for (const m of a.meters ?? []) if (m.used >= 0.9 && m.used < 1 && a.state === "healthy") items.push({ kind: "warn", title: who, text: `${m.kind} window at ${fmtPct(m.used)}, resets in ${fmtIn(m.resetsAt)}.`, href: `pool/${a.id}` });
		if ((a.usage?.failed24h ?? 0) > 0 && a.usage.failed24h >= Math.max(3, (a.usage.requests24h ?? 0) * 0.3)) items.push({ kind: "warn", title: who, text: `${a.usage.failed24h} of ${a.usage.requests24h} requests failed in the last day.`, href: `activity?outcome=error` });
	}
	if (!world.vms.length) items.push({ kind: "", title: "No virtual model yet", text: "People can only use direct provider models.", href: "virtual-models/new" });
	for (const u of world.users) {
		if (u.state === "invited") items.push({ kind: "", title: u.handle, text: "Invited, but has no key yet.", href: `users/${u.id}` });
		if (u.state === "suspended") items.push({ kind: "", title: u.handle, text: "Suspended; the key still exists.", href: `users/${u.id}` });
	}
	const day = Date.now() - 86_400_000;
	const limitedUsers = new Map();
	for (const r of world.requests) if (r.outcome === "limited" && r.at > day) limitedUsers.set(r.userId, (limitedUsers.get(r.userId) ?? 0) + 1);
	for (const [uid, n] of limitedUsers) {
		// A removed user cannot be helped from here; the Held by limits number still counts them.
		const u = world.users.find((x) => x.id === uid);
		if (!u) continue;
		items.push({ kind: "warn", title: u.handle, text: `Hit a limit ${n === 1 ? "once" : `${n} times`} in the last day.`, href: `activity?user=${uid}&outcome=limited` });
	}
	if (!world.core.linked) items.unshift({ kind: "bad", title: "Engine unreachable", text: "The engine connection is down; check the Pool page.", href: "pool" });
	const rank = { bad: 0, warn: 1, "": 2 };
	items.sort((a, b) => rank[a.kind] - rank[b.kind]);
	const body = items.length
		? h(
				"div",
				{ class: "list ov-attn" },
				items.map((it) => h("a", { class: "li clickable", href: `#/${it.href}` }, h("span", { class: `dot ${it.kind}` }), h("div", { class: "lt" }, h("div", { class: "main" }, it.title), h("div", { class: "sub" }, it.text)), icon("chevronRight", 14))),
			)
		: empty({ icon: "check", title: "Nothing needs you", hint: "Every account is healthy and nobody is stuck on a limit." });
	return card({ title: "Needs attention", sub: items.length ? `${plural(items.length, "thing")} to look at` : "All clear", flush: true, body, cls: "ov-attention" });
}
