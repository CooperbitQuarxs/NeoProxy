// Overview: the flow board. Three fixed columns, proxy vault → virtual
// models → users, joined by ribbons whose weight is how much each pair has
// drawn this window. Select any row to see what it touches. Under it, the
// live request stream and the things that need the admin's attention.

import { h, svgEl, clear, icon, fmtPct, fmtAgo, fmtIn, fmtTime, fmtCompact, plural } from "../lib/dom.js";
import { neo, onChange } from "../lib/client.js";
import { pageHead, card, strip, btn, avatar, ring, servedName, outcomeBadge, status, empty, spinner, errorState, meterRing, preferredMeter, providerDot } from "../lib/components.js";
import { accountHealth } from "../lib/providers.js";
import { explainDecision } from "../lib/explain.js";

export async function render(ctx) {
	const { root } = ctx;
	root.append(
		pageHead({
			title: "Overview",
			desc: "Who draws what from where: the pool of provider accounts on the left, the virtual models you shaped in the middle, and the people and services drawing from them on the right.",
			actions: [btn("New virtual model", { icon: "plus", cls: "primary", onClick: () => ctx.navigate("virtual-models/new") })],
		}),
	);
	const host = h("div");
	root.append(host, spinner());
	let world;
	try {
		const [vault, models, vms, users, usage, requests, core] = await Promise.all([neo.vault(), neo.models(), neo.virtualModels(), neo.users(), neo.usage(), neo.requests({ limit: 14 }), neo.core()]);
		world = { ...vault, models, vms, users, usage, requests, core };
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	const hour = Date.now() - 3600_000;
	const lastHour = world.requests.filter((r) => r.at > hour);
	const limited = world.requests.filter((r) => r.outcome === "limited" || r.outcome === "queued");
	const flow = flowBoard(world, ctx);
	host.append(
		flow.el,
		card({
			flush: true,
			body: strip([
				{ label: "Served models", value: String(world.models.filter((m) => m.exposed && world.providers.find((p) => p.id === m.providerId)?.exposed).length), sub: `${world.vms.length} virtual · ${world.providers.filter((p) => p.exposed).length} providers exposed` },
				{ label: "Accounts in the vault", value: String(world.accounts.length), sub: `${world.accounts.filter((a) => a.state === "healthy").length} healthy` },
				{ label: "Users", value: String(world.users.length), sub: `${world.users.filter((u) => u.state === "active").length} active · ${world.users.filter((u) => u.role === "service").length} services` },
				{ label: "Recent requests, last hour", value: String(lastHour.length), sub: `within the latest ${world.requests.length} ledger records · ${lastHour.filter((r) => r.outcome !== "ok").length} not served` },
				{ label: "Held back by limits", value: String(limited.length), sub: "recent, see Activity" },
			]),
		}),
	);

	const recent = streamCard(world, ctx);
	host.append(h("div", { class: "grid c2-1" }, recent.el, attentionCard(world, ctx)));

	const off = onChange((w) => w !== "requests" && ctx.rerender());
	return () => {
		off();
		flow.dispose();
		recent.dispose();
	};
}

// ---------- flow board ----------

function flowBoard(world, ctx) {
	const { providers, accounts, models, vms, users, usage } = world;
	let selected = null; // {kind, id}

	// edges: provider → vm (weight = stage usage on that provider), vm → user (weight = user's usage of vm)
	const pvEdges = [];
	for (const vm of vms) {
		const byProv = new Map();
		for (const s of vm.stages) {
			const pid = s.model.split("/")[0];
			let w = 0;
			for (const u of Object.values(usage)) for (const a of Object.entries(u?.[vm.id]?.[s.id] ?? {})) w += a[1];
			byProv.set(pid, (byProv.get(pid) ?? 0) + w + 0.004);
		}
		for (const [pid, w] of byProv) pvEdges.push({ from: `p:${pid}`, to: `v:${vm.id}`, w });
	}
	const vuEdges = [];
	for (const u of users) {
		for (const g of u.grants) {
			let w = 0;
			for (const st of Object.values(usage[u.id]?.[g.virtualModel] ?? {})) for (const v of Object.values(st)) w += v;
			vuEdges.push({ from: `v:${g.virtualModel}`, to: `u:${u.id}`, w: w + 0.004 });
		}
	}
	const edges = [...pvEdges, ...vuEdges];

	const rowEls = new Map();
	const frow = (key, { lead, main, sub, trail, href }) => {
		const el = h(
			"button",
			{
				type: "button",
				class: "frow",
				"aria-pressed": "false",
				dataset: { key },
				onClick: () => select(key),
				onDblclick: () => href && ctx.navigate(href),
				title: "Click to trace, double-click to open",
			},
			lead,
			h("div", { class: "ft" }, h("div", { class: "main" }, main), h("div", { class: "sub" }, sub)),
			trail,
		);
		rowEls.set(key, el);
		return el;
	};

	const provCol = h("div", { class: "col col-pool" }, h("div", { class: "col-label" }, "Pool", h("span", { class: "count" }, `${accounts.length} accounts`)));
	const feeds = (p) => vms.some((vm) => vm.stages.some((s) => s.model.startsWith(`${p.id}/`)));
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
		return frow(`p:${p.id}`, {
			lead,
			main: h("span", { class: "row", style: { gap: "7px" } }, providerDot(p, 7), p.name),
			sub: !p.exposed ? "hidden from users" : unhealthy ? `${plural(accs.length, "account")} · ${unhealthy} need attention` : accs.length === 1 && accs[0].meters?.length ? `${(accs[0].meters.filter((m) => m.kind === "5h" || m.kind === "week").length ? accs[0].meters.filter((m) => m.kind === "5h" || m.kind === "week") : accs[0].meters.slice(0, 2)).map((m) => `${m.kind} ${fmtPct(m.used)}`).join(" · ")}` : `${plural(accs.length, "account")} · ${avg === null ? "no meter" : `${fmtPct(avg)} used`}`,
			trail: p.exposed ? null : icon("eyeOff", 14),
			href: "pool",
		});
	};
	for (const p of ordered.filter(feeds)) provCol.append(provRow(p));
	// Providers no chain draws from fold away so the ribbons stay readable.
	if (idle.length) {
		const fold = h("div", { class: "fold" });
		let open = false;
		const toggleRow = h(
			"button",
			{ type: "button", class: "frow fold-toggle", "aria-expanded": "false", onClick: () => { open = !open; toggleRow.setAttribute("aria-expanded", String(open)); fold.classList.toggle("open", open); toggleLabel.textContent = open ? "Hide them" : `${idle.length} more providers`; requestAnimationFrame(draw); } },
			h("span", { class: "fold-dots" }, idle.slice(0, 6).map((p) => providerDot(p, 6))),
			h("div", { class: "ft" }, h("div", { class: "main ink-2" }, ""), h("div", { class: "sub" }, "no chain draws from them")),
			icon("chevronDown", 14),
		);
		const toggleLabel = toggleRow.querySelector(".main");
		toggleLabel.textContent = `${idle.length} more providers`;
		for (const p of idle) fold.append(provRow(p));
		provCol.append(toggleRow, fold);
	}

	const vmCol = h("div", { class: "col col-vms" }, h("div", { class: "col-label" }, "Virtual models", h("span", { class: "count" }, `${vms.length}`)));
	for (const vm of vms) {
		const granted = users.filter((u) => u.grants.some((g) => g.virtualModel === vm.id)).length;
		vmCol.append(
			frow(`v:${vm.id}`, {
				lead: h("span", { class: "avatar", style: { borderRadius: "8px" } }, icon("route", 14)),
				main: servedName(vm.served, { virtual: true }),
				sub: `${plural(vm.stages.length, "stage")} · ${plural(granted, "user")}`,
				href: `virtual-models/${vm.id}`,
			}),
		);
	}

	const userCol = h("div", { class: "col col-users" }, h("div", { class: "col-label" }, "Users", h("span", { class: "count" }, `${users.length}`)));
	for (const u of users) {
		const recent = world.requests.filter((r) => r.userId === u.id);
		userCol.append(
			frow(`u:${u.id}`, {
				lead: avatar(u),
				main: u.handle,
				sub: u.state !== "active" ? u.state : `${u.role} · ${plural(u.grants.length, "model")} · last ${fmtAgo(recent[0]?.at)}`,
				href: `users/${u.id}`,
			}),
		);
	}

	const ribbons = svgEl("svg", { class: "ribbons" });
	const board = h("div", { class: "flow" }, ribbons, provCol, h("div"), vmCol, h("div"), userCol);
	const boardHost = h("div", { class: "flow-host" }, board);
	const pathEls = [];

	const draw = () => {
		clear(ribbons);
		pathEls.length = 0;
		const b = board.getBoundingClientRect();
		ribbons.setAttribute("viewBox", `0 0 ${b.width} ${b.height}`);
		const maxW = Math.max(...edges.map((e) => e.w), 0.01);
		for (const e of edges) {
			const a = rowEls.get(e.from)?.getBoundingClientRect();
			const c = rowEls.get(e.to)?.getBoundingClientRect();
			if (!a || !c) continue;
			const x1 = a.right - b.left;
			const y1 = a.top + a.height / 2 - b.top;
			const x2 = c.left - b.left;
			const y2 = c.top + c.height / 2 - b.top;
			const mx = (x1 + x2) / 2;
			const p = svgEl("path", {
				d: `M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`,
				"stroke-width": (1.5 + 12 * (e.w / maxW)).toFixed(1),
				class: e.from.startsWith("p:") ? "" : "cyan",
			});
			p.dataset.from = e.from;
			p.dataset.to = e.to;
			ribbons.append(p);
			pathEls.push(p);
		}
		applySelection();
	};

	const foot = h("div", { class: "card-foot" }, h("span", { class: "flow-legend" }, h("span", null, h("i", { style: { background: "var(--accent-b)" } }), "vault → virtual model"), h("span", null, h("i", { style: { background: "var(--accent-a)" } }), "virtual model → user"), h("span", { class: "ink-3" }, "ribbon weight is usage this window")), h("span", { class: "spacer" }), h("span", { class: "ink-3" }, "Select a row to trace it; double-click opens it"));

	function applySelection() {
		for (const [key, el] of rowEls) {
			el.setAttribute("aria-pressed", String(selected === key));
			el.classList.remove("lit", "dim");
		}
		for (const p of pathEls) p.classList.remove("lit", "dim");
		if (!selected) return;
		// Trace away from the selection: a provider lights the virtual models it
		// feeds and their users; a user lights their virtual models and the
		// providers behind them; a virtual model lights one hop each way.
		const lit = new Set([selected]);
		const litEdges = new Set();
		const downstream = (key) => {
			for (const e of edges) if (e.from === key) { lit.add(e.to); litEdges.add(e); downstream(e.to); }
		};
		const upstream = (key) => {
			for (const e of edges) if (e.to === key) { lit.add(e.from); litEdges.add(e); upstream(e.from); }
		};
		if (selected.startsWith("p:")) downstream(selected);
		else if (selected.startsWith("u:")) upstream(selected);
		else {
			for (const e of edges) if (e.from === selected || e.to === selected) { lit.add(e.from); lit.add(e.to); litEdges.add(e); }
		}
		for (const [key, el] of rowEls) if (key !== selected) el.classList.add(lit.has(key) ? "lit" : "dim");
		for (const p of pathEls) {
			const on = [...litEdges].some((e) => e.from === p.dataset.from && e.to === p.dataset.to);
			p.classList.add(on ? "lit" : "dim");
		}
		renderFoot();
	}

	function renderFoot() {
		clear(foot);
		if (!selected) {
			foot.append(h("span", { class: "flow-legend" }, h("span", null, h("i", { style: { background: "var(--accent-b)" } }), "vault → virtual model"), h("span", null, h("i", { style: { background: "var(--accent-a)" } }), "virtual model → user"), h("span", { class: "ink-3" }, "ribbon weight is usage this window")), h("span", { class: "spacer" }), h("span", { class: "ink-3" }, "Select a row to trace it; double-click opens it"));
			return;
		}
		const [kind, id] = selected.split(":");
		if (kind === "p") {
			const p = providers.find((x) => x.id === id);
			const touched = vms.filter((vm) => vm.stages.some((s) => s.model.startsWith(`${id}/`)));
			foot.append(h("span", null, h("b", null, p.name), ` feeds ${plural(touched.length, "virtual model")}: `, touched.map((v) => v.name).join(", ") || "none"), h("span", { class: "spacer" }), btn("Open in Pool", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate("pool") }));
		} else if (kind === "v") {
			const vm = vms.find((x) => x.id === id);
			const granted = users.filter((u) => u.grants.some((g) => g.virtualModel === vm.id));
			foot.append(h("span", null, servedName(vm.served, { virtual: true }), ` · ${vm.stages.map((s) => models.find((m) => m.id === s.model)?.display ?? s.model).join(" → ")} → ${vm.terminal.kind} · granted to ${granted.map((u) => u.handle).join(", ") || "nobody"}`), h("span", { class: "spacer" }), btn("Open editor", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate(`virtual-models/${vm.id}`) }));
		} else {
			const u = users.find((x) => x.id === id);
			foot.append(h("span", null, h("b", null, u.handle), ` sees ${u.grants.map((g) => vms.find((v) => v.id === g.virtualModel)?.name).filter(Boolean).join(", ") || "no virtual models"}`, u.providerAccess.length ? ` and has direct access to ${u.providerAccess.length} providers` : ", no direct provider access"), h("span", { class: "spacer" }), btn("Open user", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate(`users/${u.id}`) }));
		}
	}

	function select(key) {
		selected = selected === key ? null : key;
		applySelection();
		if (!selected) renderFoot();
	}

	const el = card({
		title: "Flow",
		sub: "Every ribbon is a grant or a stage; the thicker, the more it has drawn this window.",
		flush: true,
		body: boardHost,
		rise: true,
	});
	el.append(foot);
	const ro = new ResizeObserver(() => draw());
	ro.observe(board);
	requestAnimationFrame(draw);
	const esc = (e) => {
		if (e.key === "Escape" && selected) select(selected);
	};
	document.addEventListener("keydown", esc);
	return { el, dispose: () => { ro.disconnect(); document.removeEventListener("keydown", esc); } };
}

// ---------- stream ----------

function streamCard(world, ctx) {
	const list = h("div", { class: "stream" });
	const rowFor = (r, isNew = false) => {
		const u = world.users.find((x) => x.id === r.userId);
		const d = r.decision;
		const model = d.model ? world.models.find((m) => m.id === d.model) : null;
		const acc = d.accountId ? world.accounts.find((a) => a.id === d.accountId) : null;
		const open = () => ctx.navigate(`activity/${r.id}`);
		return h(
			"div",
			{ class: `srow ${isNew ? "new" : ""}`, onClick: open, onKeydown: (e) => { if ((e.key === "Enter" || e.key === " ") && e.target === e.currentTarget) { e.preventDefault(); open(); } }, tabindex: 0, role: "link" },
			h("span", { class: "t" }, fmtTime(r.at)),
			h("span", { class: "who row" }, avatar(u ?? { handle: "?" }, "sm"), u?.handle ?? r.userId),
			h("span", { class: "route" }, servedName(r.asked, { virtual: !r.asked.split("/").slice(1).includes("/") && r.asked.split("/").length === 2 })),
			h(
				"span",
				{ class: "route" },
				d.outcome === "ok" ? [icon("arrowRight", 12), h("span", { class: "hop" }, model?.display ?? d.model), acc ? h("span", { class: "ink-3 hop acc" }, `· ${acc.label}`) : null] : [icon("stop", 12), h("span", { class: "hop ink-3", title: explainDecision(r, world).sentences.join(" ") }, explainDecision(r, world).headline)],
			),
			h("span", { class: "num ink-3" }, r.outcome === "ok" ? fmtCompact(r.tokens.in + r.tokens.out) : "–"),
			h("span", { style: { textAlign: "right" } }, outcomeBadge(r.outcome)),
		);
	};
	for (const r of world.requests) list.append(rowFor(r));
	const pollState = h("span", { class: "ink-3" }, "Polling every 5s");
	if (!world.requests.length) list.append(empty({ icon: "activity", title: "No requests yet", hint: "Real requests appear here after clients use this proxy." }));
	const unsubscribe = neo.subscribeStream((r) => {
		list.querySelector(".empty")?.remove();
		list.prepend(rowFor(r, true));
		if (list.children.length > 14) list.lastChild.remove();
	}, (error) => {
		pollState.textContent = error ? `Polling failed: ${error.message}` : "Polling every 5s";
		pollState.className = error ? "error" : "ink-3";
	}, world.requests);
	const el = card({
		title: h("span", { class: "row" }, "Recent requests"),
		sub: "Durable server activity, refreshed from the real request ledger.",
		actions: [pollState, btn("All activity", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate("activity") })],
		flush: true,
		body: list,
	});
	return { el, dispose: unsubscribe };
}

// ---------- attention ----------

function attentionCard(world, ctx) {
	const items = [];
	for (const a of world.accounts) {
		const p = world.providers.find((x) => x.id === a.providerId);
		const who = `${p?.name ?? a.providerId} · ${a.label}`;
		const hs = accountHealth(a);
		if (hs.kind === "bad") items.push({ kind: "bad", text: `${who}: ${hs.text.toLowerCase()}. ${hs.hint ?? ""}`.trim(), href: `pool/${a.id}` });
		else if (a.state === "cooldown") items.push({ kind: "warn", text: `${who}: cooling down, back in ${fmtIn(a.cooldownUntil)}`, href: `pool/${a.id}` });
		else if (hs.kind === "warn") items.push({ kind: "warn", text: `${who}: ${hs.text.toLowerCase()}${hs.hint ? `. ${hs.hint}` : ""}`, href: `pool/${a.id}` });
		for (const m of a.meters ?? []) if (m.used >= 0.9 && m.used < 1 && a.state === "healthy") items.push({ kind: "warn", text: `${who}: ${m.kind} window at ${fmtPct(m.used)}, resets ${fmtIn(m.resetsAt)}`, href: `pool/${a.id}` });
		if ((a.usage?.failed24h ?? 0) > 0 && a.usage.failed24h >= Math.max(3, (a.usage.requests24h ?? 0) * 0.3)) items.push({ kind: "warn", text: `${who}: ${a.usage.failed24h} of ${a.usage.requests24h} requests failed in the last day`, href: `activity?outcome=error` });
	}
	if (!world.vms.length) items.push({ kind: "", text: "No virtual model yet: people can only use direct provider models", href: "virtual-models/new" });
	for (const u of world.users) {
		if (u.state === "invited") items.push({ kind: "", text: `${u.handle} is invited but has no key yet`, href: `users/${u.id}` });
		if (u.state === "suspended") items.push({ kind: "", text: `${u.handle} is suspended; its key still exists`, href: `users/${u.id}` });
	}
	const limitedUsers = new Map();
	for (const r of world.requests) if (r.outcome === "limited") limitedUsers.set(r.userId, (limitedUsers.get(r.userId) ?? 0) + 1);
	for (const [uid, n] of limitedUsers) {
		const u = world.users.find((x) => x.id === uid);
		items.push({ kind: "warn", text: `${u?.handle ?? uid} hit a limit ${n} ${n === 1 ? "time" : "times"} recently`, href: `activity?user=${uid}&outcome=limited` });
	}
	if (!world.core.linked) items.unshift({ kind: "bad", text: "Engine connection unavailable; inspect the pool page", href: "pool" });
	const body = items.length
		? h(
				"div",
				{ class: "list" },
				items.map((it) => h("button", { type: "button", class: "li clickable", onClick: () => ctx.navigate(it.href) }, h("span", { class: `dot ${it.kind}` }), h("div", { class: "lt" }, h("div", { class: "main", style: { fontWeight: 400, whiteSpace: "normal" } }, it.text)), icon("chevronRight", 14))),
			)
		: empty({ icon: "check", title: "Nothing needs you", hint: "Every account is healthy and no user is stuck on a limit." });
	return card({ title: "Needs attention", sub: `${items.length || "No"} ${items.length === 1 ? "thing" : "things"} to look at`, flush: true, body });
}
