import { meters, meterFor, accountHealth, providerHueStyle } from "./providers.js";
// Shared widgets. Every page composes these; pages never write raw markup for
// a card, table, ring, chain or chip.

import { h, svgEl, icon, append, clear, fmtPct, fmtCompact, copyText, plural } from "./dom.js";

// ---------- page chrome ----------

export function pageHead({ title, desc, actions = [], back }) {
	return h(
		"div",
		{ class: "page-head" },
		h(
			"div",
			{ class: "titles" },
			back ? h("a", { class: "back", href: back.href }, icon("arrowLeft", 14), back.label) : null,
			h("h1", null, title),
			desc ? h("p", { class: "desc" }, desc) : null,
		),
		actions.length ? h("div", { class: "actions" }, actions) : null,
	);
}

export function card({ title, sub, actions, body, foot, cls = "", flush = false, rise = true }) {
	return h(
		"section",
		{ class: `card ${rise ? "rise" : ""} ${cls}` },
		title || actions
			? h(
					"div",
					{ class: "card-head" },
					h("div", { class: "titles" }, h("div", { class: "title" }, title), sub ? h("div", { class: "sub" }, sub) : null),
					actions ? h("div", { class: "actions" }, actions) : null,
				)
			: null,
		body ? h("div", { class: `card-body ${flush ? "flush" : ""}` }, body) : null,
		foot ? h("div", { class: "card-foot" }, foot) : null,
	);
}

export function strip(cells) {
	return h(
		"div",
		{ class: "strip" },
		cells.map((c) =>
			h(
				"div",
				{ class: "cell" },
				h("div", { class: "label" }, c.label),
				h("div", { class: `value ${c.mono ? "mono" : ""}` }, c.value),
				c.sub ? h("div", { class: "sub" }, c.sub) : null,
			),
		),
	);
}

export function btn(label, { icon: ic, cls = "", onClick, title, disabled, href, type = "button" } = {}) {
	const content = [ic ? icon(ic, 15) : null, label];
	if (href) return h("a", { class: `btn ${cls}`, href, title, "aria-label": title }, content);
	return h("button", { class: `btn ${cls}`, type, onClick, title, "aria-label": title, disabled }, content);
}

export function iconBtn(ic, title, onClick, cls = "") {
	return btn(null, { icon: ic, cls: `icon ${cls}`, title, onClick });
}

export function badge(text, kind = "") {
	return h("span", { class: `badge ${kind}` }, text);
}

export function dot(kind = "") {
	return h("span", { class: `dot ${kind}` });
}

export function status(kind, text, sub) {
	return h("span", { class: "status" }, dot(kind), text, sub ? h("span", { class: "sub" }, sub) : null);
}

export function tag(text) {
	return h("span", { class: "tag" }, text);
}

// `neoproxy/codex/gpt-6-astra` rendered with the prefix quiet and the model
// loud; virtual names get the accent.
export function servedName(name, { virtual = false } = {}) {
	const parts = name.replace(/^neoproxy\//, "").split("/");
	if (parts.length === 1) return h("span", { class: "served" }, h("span", { class: "pre" }, "neoproxy/"), h("span", { class: virtual ? "vm" : "" }, parts[0]));
	return h("span", { class: "served" }, h("span", { class: "pre" }, "neoproxy/"), h("span", { class: "prov" }, `${parts[0]}/`), parts.slice(1).join("/"));
}

export function avatar(user, size = "") {
	const initials = (user.name || user.handle || "?")
		.split(/\s+/)
		.map((s) => s[0])
		.slice(0, 2)
		.join("")
		.toUpperCase();
	return h("span", { class: `avatar ${user.role} ${size}`, title: user.name }, user.role === "service" ? icon("cpu", 13) : initials);
}

// ---------- controls ----------

export function input(attrs = {}) {
	return h("input", { class: `input ${attrs.mono ? "mono" : ""}`, ...attrs });
}

export function select(options, value, onChange, attrs = {}) {
	return h(
		"select",
		{ class: "select", onChange: (e) => onChange(e.target.value), ...attrs },
		options.map((o) => h("option", { value: o.value, selected: o.value === value }, o.label)),
	);
}

export function search(placeholder, onInput, attrs = {}) {
	return h("div", { class: "search" }, icon("search", 15), input({ type: "search", placeholder, onInput: (e) => onInput(e.target.value), ...attrs }));
}

let fieldSerial = 0;
export function field(label, control, hint, error) {
	const target = control?.matches?.("input,textarea,select") ? control : control?.querySelector?.("input,textarea,select");
	if (target && !target.id) target.id = `neo-field-${++fieldSerial}`;
	return h("div", { class: "field" }, h("label", { for: target?.id }, label), control, error ? h("div", { class: "error" }, error) : hint ? h("div", { class: "hint" }, hint) : null);
}

export function seg(options, value, onChange) {
	const el = h("div", { class: "seg", role: "group" });
	for (const o of options) {
		el.append(
			h(
				"button",
				{
					type: "button",
					"aria-pressed": String(o.value === value),
					onClick: () => {
						for (const b of el.children) b.setAttribute("aria-pressed", String(b === btnEl));
						onChange(o.value);
					},
					title: o.title,
				},
				o.label,
			),
		);
		const btnEl = el.lastChild;
	}
	return el;
}

export function toggle(checked, onChange, label, { disabled = false, showLabel = true } = {}) {
	const el = h(
		"button",
		{
			type: "button",
			class: "switch",
			role: "switch",
			"aria-checked": String(checked),
			"aria-label": label,
			disabled,
			onClick: async () => {
				const next = el.getAttribute("aria-checked") !== "true";
				el.setAttribute("aria-checked", String(next));
				try {
					await onChange(next);
				} catch (e) {
					el.setAttribute("aria-checked", String(!next));
					throw e;
				}
			},
		},
		h("span", { class: "track" }),
		label && showLabel ? h("span", null, label) : null,
	);
	return el;
}

// ---------- rings (.neo-ring) ----------

// A ring whose fill is `used` (0..1 of the account window) and whose dashed
// marker is `budget` (0..1). Size in px. The label shows the used percentage.
export function ring({ used = null, budget = null, size = 36, label = true, large = false, title }) {
	const r = size / 2 - 3;
	const c = 2 * Math.PI * r;
	if (!Number.isFinite(used)) return h("span", { class: `neo-ring ${large ? "lg" : ""}`, title: title ?? "Usage unknown", role: "img", "aria-label": "Usage unknown", style: { width: `${size}px`, height: `${size}px` } }, h("span", { class: "ring-label" }, "?"));
	const u = Math.max(0, Math.min(1, used));
	const kind = budget !== null && u >= budget ? "bad" : budget !== null && u >= budget * 0.8 ? "warn" : u >= 0.9 ? "bad" : u >= 0.7 ? "warn" : "";
	const el = h("span", { class: `neo-ring ${kind} ${large ? "lg" : ""}`, title, role: "img", "aria-label": `${fmtPct(u, budget !== null ? null : 0)} used${budget !== null ? ` of ${fmtPct(budget, null)} allowed` : ""}` });
	const sv = svgEl("svg", { width: size, height: size, viewBox: `0 0 ${size} ${size}` });
	sv.append(svgEl("circle", { class: "track", cx: size / 2, cy: size / 2, r, "stroke-width": 3, fill: "none" }));
	if (budget !== null) {
		sv.append(
			svgEl("circle", {
				class: "budget",
				cx: size / 2,
				cy: size / 2,
				r: r + 0,
				"stroke-width": 1,
				fill: "none",
				"stroke-dasharray": `${c * Math.min(1, budget)} ${c}`,
			}),
		);
	}
	sv.append(svgEl("circle", { class: "fill", cx: size / 2, cy: size / 2, r, "stroke-width": 3, fill: "none", "stroke-dasharray": `${c} ${c}`, "stroke-dashoffset": c * (1 - u) }));
	el.append(sv);
	if (label) el.append(h("span", { class: "ring-label" }, fmtPct(u)));
	return el;
}

export function ringRow({ used, budget, main, sub, size = 36 }) {
	return h("div", { class: "ring-row" }, ring({ used, budget, size }), h("div", { class: "ring-text" }, h("div", { class: "main" }, main), sub ? h("div", { class: "sub" }, sub) : null));
}

// ---------- table ----------

export function table(columns, rows, { onRow, selectedKey, keyOf = (r) => r.id, emptyState, sort } = {}) {
	if (!rows.length && emptyState) return emptyState;
	let sortKey = sort?.key ?? null;
	let sortDir = sort?.dir ?? "desc";
	const tbl = h("table", { class: "table" });
	const thead = h("thead");
	const tbody = h("tbody");
	tbl.append(thead, tbody);
	const renderHead = () => {
		clear(thead);
		thead.append(
			h(
				"tr",
				null,
				columns.map((c) =>
					h(
						"th",
						{
							class: `${c.num ? "num" : ""} ${c.sortBy ? "sortable" : ""}`,
							style: c.width ? { width: c.width } : null,
							scope: "col",
							"aria-sort": c.sortBy ? (sortKey === c.key ? (sortDir === "asc" ? "ascending" : "descending") : "none") : null,
						},
						c.sortBy ? h("button", {
							type: "button",
							class: "sort-button",
							dataset: { sort: c.key },
							onClick: (e) => {
								const focused = document.activeElement === e.currentTarget;
								if (sortKey === c.key) sortDir = sortDir === "asc" ? "desc" : "asc";
								else {
									sortKey = c.key;
									sortDir = "desc";
								}
								renderHead();
								renderBody();
								if (focused) thead.querySelector(`[data-sort="${CSS.escape(c.key)}"]`)?.focus({ preventScroll: true });
							},
						}, c.label, sortKey === c.key ? h("span", { "aria-hidden": "true" }, sortDir === "asc" ? " ↑" : " ↓") : null) : c.label,
					),
				),
			),
		);
	};
	const renderBody = () => {
		clear(tbody);
		let list = rows;
		if (sortKey) {
			const col = columns.find((c) => c.key === sortKey);
			list = rows.slice().sort((a, b) => {
				const va = col.sortBy(a);
				const vb = col.sortBy(b);
				const cmp = typeof va === "number" ? va - vb : String(va).localeCompare(String(vb));
				return sortDir === "asc" ? cmp : -cmp;
			});
		}
		for (const r of list) {
			const key = keyOf(r);
			tbody.append(
				h(
					"tr",
					{
						class: onRow ? "clickable" : "",
						"aria-selected": selectedKey !== undefined ? String(selectedKey === key) : null,
						onClick: onRow ? () => onRow(r) : null,
						tabindex: onRow ? 0 : null,
						onKeydown: onRow ? (event) => {
							if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return;
							event.preventDefault();
							onRow(r);
						} : null,
					},
					columns.map((c) => h("td", { class: `${c.num ? "num" : ""} ${c.wrap ? "wrap" : ""} ${c.clip ? "clip" : ""}` }, c.render(r))),
				),
			);
		}
	};
	renderHead();
	renderBody();
	return h("div", { class: "table-scroll", tabindex: 0, role: "region", "aria-label": "Table; scroll horizontally for more columns" }, tbl);
}

export function empty({ icon: ic = "inbox", title, hint, action }) {
	return h("div", { class: "empty" }, icon(ic, 22), h("div", { class: "title" }, title), hint ? h("div", { class: "hint" }, hint) : null, action);
}

export function kv(pairs) {
	return h(
		"dl",
		{ class: "kv" },
		pairs.filter(Boolean).map(([k, v]) => [h("dt", null, k), h("dd", null, v)]),
	);
}

export function copyLine(text, label) {
	const el = h("span", { class: "row" }, h("code", null, text));
	el.append(
		iconBtn("copy", `Copy ${label ?? ""}`.trim(), async () => {
			toast((await copyText(text)) ? "Copied" : "Clipboard unavailable", "warn");
		}, "sm"),
	);
	return el;
}

// ---------- inspector (docked right panel) ----------

export function inspector({ title, sub, body, foot, onClose }) {
	return h(
		"aside",
		{ class: "inspector", "aria-label": title },
		h(
			"div",
			{ class: "ins-head" },
			h("div", { class: "titles" }, h("h2", null, title), sub ? h("div", { class: "sub" }, sub) : null),
			onClose ? iconBtn("x", "Close", onClose, "sm") : null,
		),
		h("div", { class: "ins-body" }, body),
		foot ? h("div", { class: "ins-foot" }, foot) : null,
	);
}

export function section(title, ...children) {
	return h("div", { class: "section" }, title ? h("div", { class: "section-title" }, title) : null, children);
}

// ---------- feedback ----------

export function toast(text, kind = "ok", ms = 2800) {
	const host = document.getElementById("toasts");
	const el = h("div", { class: "toast", role: "status" }, dot(kind), text);
	host.append(el);
	setTimeout(() => el.remove(), ms);
	return el;
}

const openDialogs = new Set();
export function dismissDialogs() {
	for (const close of [...openDialogs]) close();
}

export function dialog({ title, sub, body, actions, onClose }) {
	const backdrop = h("div", { class: "dialog-backdrop", onClick: (e) => e.target === backdrop && close() });
	const close = () => {
		if (!openDialogs.delete(close)) return;
		backdrop.remove();
		document.removeEventListener("keydown", esc);
		onClose?.();
	};
	const esc = (e) => e.key === "Escape" && close();
	openDialogs.add(close);
	document.addEventListener("keydown", esc);
	backdrop.append(h("div", { class: "dialog", role: "dialog", "aria-modal": "true", "aria-label": title }, h("h2", null, title), sub ? h("div", { class: "sub" }, sub) : null, body, actions ? h("div", { class: "actions" }, actions(close)) : null));
	document.body.append(backdrop);
	backdrop.querySelector("input,button,textarea,select")?.focus();
	return { close };
}

export function confirm({ title, text, confirmLabel = "Confirm", danger = false }) {
	// Resolve before closing: close() fires onClose, which must only count as a
	// cancel when nothing has been decided yet.
	let settled = false;
	const { promise, resolve } = Promise.withResolvers();
	const decide = (v, close) => {
		if (!settled) {
			settled = true;
			resolve(v);
		}
		close();
	};
	dialog({
		title,
		sub: text,
		actions: (close) => [
			btn("Cancel", { onClick: () => decide(false, close) }),
			btn(confirmLabel, { cls: danger ? "danger outline" : "primary", onClick: () => decide(true, close) }),
		],
		onClose: () => {
			if (!settled) {
				settled = true;
				resolve(false);
			}
		},
	});
	return promise;
}

export function spinner(text = "Loading") {
	return h("div", { class: "empty" }, h("div", { class: "hint" }, text, "…"));
}

export function errorState(err, retry) {
	return empty({ icon: "info", title: "Could not load", hint: err?.message ?? String(err), action: retry ? btn("Try again", { icon: "refresh", cls: "outline", onClick: retry }) : null });
}

// Catalogue responses are snapshots; share their id indexes across large lists.
const catalogueIndexes = new WeakMap();
function catalogueIndex(rows) {
	let index = catalogueIndexes.get(rows);
	if (!index) {
		index = new Map(rows.map((row) => [row.id, row]));
		catalogueIndexes.set(rows, index);
	}
	return index;
}

export function providerLabel(provider) {
	if (!provider) return { main: "Unavailable provider", source: "" };
	return {
		main: provider.kind === "neopi-gateway" ? provider.vendor || "Shared provider" : provider.name,
		source: provider.sourceLabel || "",
	};
}

export function vmIdentity(vm, models, providers) {
	const stages = vm.stages ?? [];
	if (stages.length !== 1) return `${stages.length} stages`;
	const model = catalogueIndex(models).get(stages[0].model);
	const provider = catalogueIndex(providers).get(model?.providerId ?? stages[0].model.split("/")[0]);
	const label = providerLabel(provider);
	return `${model?.display ?? model?.name ?? "Unavailable model"} · ${label.main}${label.source ? ` · ${label.source}` : ""}`;
}

export function vmSearchText(vm, models, providers) {
	const modelIndex = catalogueIndex(models);
	const providerIndex = catalogueIndex(providers);
	const parts = [vm.name, vm.served, vm.description];
	for (const stage of vm.stages ?? []) {
		const model = modelIndex.get(stage.model);
		const label = providerLabel(providerIndex.get(model?.providerId ?? stage.model.split("/")[0]));
		parts.push(stage.model, model?.display, model?.name, label.main, label.source);
	}
	return parts.filter(Boolean).join(" ").toLowerCase();
}

// ---------- provider → model picker ----------

// Never a flat list: a provider column (with account counts) and the models of
// the selected provider. `models` and `providers` are the catalogue; `accounts`
// gives per-provider counts.
export function modelPicker({ providers, models, accounts, value, onPick, onlyExposed = true }) {
	const availableProviders = providers.filter((p) => !onlyExposed || p.exposed);
	const providerIds = new Set(availableProviders.map((p) => p.id));
	const availableModels = models.filter((m) => providerIds.has(m.providerId) && (!onlyExposed || m.exposed));
	const accountCounts = new Map();
	for (const account of accounts) {
		if (account.state === "healthy") accountCounts.set(account.providerId, (accountCounts.get(account.providerId) ?? 0) + 1);
	}
	let providerId = value ? value.split("/")[0] : availableProviders[0]?.id;
	let query = "";
	const el = h("div", { class: "picker" });
	const left = h("div", { class: "p-col", role: "group", "aria-label": "Providers" });
	const right = h("div", { class: "p-col", role: "group", "aria-label": "Models" });
	const filter = h("input", {
		class: "input picker-filter",
		type: "search",
		placeholder: "Search all providers and models",
		"aria-label": "Search all providers and models",
		onInput: (event) => {
			query = event.target.value.trim().toLowerCase();
			render();
		},
	});
	el.append(filter, left, right);
	const focusItem = (kind, id) => {
		if (el.isConnected) el.querySelector(`[data-${kind}="${CSS.escape(id)}"]`)?.focus({ preventScroll: true });
	};
	function render(focusKind, focusId) {
		const leftScroll = left.scrollTop;
		clear(left);
		clear(right);
		const matches = new Map();
		for (const model of availableModels) {
			if (query && !`${model.display} ${model.name} ${model.id}`.toLowerCase().includes(query)) continue;
			let list = matches.get(model.providerId);
			if (!list) matches.set(model.providerId, list = []);
			list.push(model);
		}
		const shownProviders = availableProviders.filter((p) => !query || matches.has(p.id));
		if (!shownProviders.some((p) => p.id === providerId)) providerId = shownProviders[0]?.id;
		const groups = new Map();
		for (const provider of shownProviders) {
			const label = providerLabel(provider);
			const group = label.source || "This proxy";
			if (!groups.has(group)) groups.set(group, []);
			groups.get(group).push({ provider, label });
		}
		for (const [group, entries] of groups) {
			left.append(h("div", { class: "p-group" }, group));
			for (const { provider, label } of entries) {
				const healthy = accountCounts.get(provider.id) ?? 0;
				const count = query ? matches.get(provider.id).length : healthy;
				left.append(h("button", {
					type: "button",
					class: `p-item ${healthy ? "" : "disabled"}`,
					"aria-pressed": String(provider.id === providerId),
					dataset: { provider: provider.id },
					title: `${label.main}${label.source ? ` · ${label.source}` : ""} · ${healthy} healthy accounts · ${provider.id}`,
					onClick: () => {
						providerId = provider.id;
						render("provider", provider.id);
					},
				}, providerDot(provider, 8), h("span", { class: "name" }, label.main), h("span", { class: "count", title: query ? "Matching models" : "Healthy accounts" }, String(count))));
			}
		}
		for (const model of matches.get(providerId) ?? []) {
			right.append(h("button", {
				type: "button",
				class: "p-item",
				"aria-pressed": String(model.id === value),
				dataset: { model: model.id },
				onClick: () => {
					value = model.id;
					onPick(model);
					render("model", model.id);
				},
			}, h("span", { class: "name", title: model.id }, model.display)));
		}
		if (!right.children.length) right.append(h("div", { class: "empty", style: { padding: "20px" } }, h("div", { class: "hint" }, "No models match")));
		left.scrollTop = leftScroll;
		if (focusKind) focusItem(focusKind, focusId);
	}
	render();
	return el;
}

// ---------- chain rail (.neo-chain) ----------

// How a stage picks among the accounts of its pool, in the words the editor uses.
export const DRAW_MODES = [
	{ value: "least-used", label: "Most room left", hint: "Each request goes to the account with the most room left." },
	{ value: "alternate", label: "Take turns", hint: "Requests go to each account in turn." },
	{ value: "fill", label: "First with room", hint: "Use the first account until it reaches its threshold, then the next." },
];
export const WINDOW_WORDS = { "5h": "5h", day: "daily", week: "weekly", month: "monthly" };

export function drawLabel(draw) {
	return (DRAW_MODES.find((mode) => mode.value === draw) ?? DRAW_MODES[0]).label;
}

// The accounts a stage may draw from, and how full their meters are for the
// stage's window. `avg` is null when none of them has a meter.
export function stagePool(stage, accounts) {
	const provider = stage.model.split("/")[0];
	return accounts.filter((a) => a.providerId === provider && (stage.accounts === "all" || stage.accounts.includes(a.id)));
}

export function poolUsage(pool, window) {
	const used = pool.map((a) => meterFor(a, window)).filter(Boolean).map((m) => m.used);
	return { metered: used.length, avg: used.length ? used.reduce((n, u) => n + u, 0) / used.length : null };
}

// "until 50% · pool 70% · 5h": one line that says where a stage stops. `allot`
// scales both thresholds for one person's grant.
export function stageLine(stage, allot = 1, metered = true, redacted = false) {
	if (redacted) return `until ${fmtPct(stage.perAccount * allot, null)} · pool ${fmtPct(stage.cap * allot, null)} · ${stage.window}`;
	if (!metered) return "no meter · always available";
	return `until ${fmtPct(stage.perAccount * allot, null)} · pool ${fmtPct(stage.cap * allot, null)} · ${stage.window}`;
}

// Renders a virtual model's stages as a rail: request → stages (scrolls
// sideways, any number) → terminal. `lit` marks the stage a simulated request
// would land on; `onSelect` makes nodes selectable; `onReorder` enables drag and
// Alt+Arrow keys; `onAdd` adds a stage button after the last stage. `compact`
// draws one-line chips for lists that only need the shape of the chain.
// `redacted` is for members: the vault hides account usage from them, so the
// rail states the thresholds without claiming to know how full the pool is.
export function chainRail(vm, { models, providers, accounts, lit = null, selected = null, onSelect, onAdd, onReorder, compact = false, editor = false, allot = 1, redacted = false } = {}) {
	const rail = h("div", { class: `neo-chain ${compact ? "compact" : ""} ${editor ? "editor" : ""}` });
	const stages = h("div", { class: "stages", role: "list", "aria-label": "Stages", tabindex: onSelect ? null : 0 });
	const link = () => h("div", { class: "link", "aria-hidden": "true" }, icon("arrowRight", 14));
	const litIdx = lit ? (lit.stageIndex ?? vm.stages.length) : null;
	if (!compact) rail.append(
		h(
			"div",
			{ class: "node request" },
			h("div", { class: "n-head" }, "request"),
			h("div", { class: "n-model" }, servedName(vm.served ?? `neoproxy/${vm.name}`, { virtual: true })),
		),
		link(),
	);
	rail.append(stages);
	vm.stages.forEach((s, i) => {
		if (i > 0) stages.append(link());
		const model = models.find((m) => m.id === s.model);
		const provider = catalogueIndex(providers).get(model?.providerId ?? s.model.split("/")[0]);
		const label = providerLabel(provider);
		const isLit = litIdx === i;
		const name = model?.display ?? "Unavailable model";
		if (compact) {
			stages.append(h("div", { class: "node", role: "listitem", dataset: { stage: s.id }, title: s.model }, h("div", { class: "n-head" }, h("span", { class: "idx" }, `stage ${i + 1}`), s.effort ? h("span", { class: "badge violet" }, s.effort) : null), h("div", { class: "n-model" }, name)));
			return;
		}
		const pool = stagePool(s, accounts);
		const usage = poolUsage(pool, s.window);
		const budget = s.cap * allot;
		// A redacted (personal) rail never judges "spent" from pool-wide meters:
		// the simulation's lit stage is the authority there.
		const spent = !redacted && usage.avg !== null && usage.avg >= budget;
		const detail = `${plural(pool.length, "account")} in the pool · ${drawLabel(s.draw).toLowerCase()}`;
		const node = h(
			"div",
			{
				class: `node stage ${isLit ? "lit" : ""} ${spent ? "spent" : ""}`,
				role: "listitem",
				"aria-selected": onSelect ? String(selected === s.id) : null,
				tabindex: onSelect ? 0 : null,
				onClick: onSelect ? () => onSelect(s) : null,
				onKeydown: (e) => {
					if (onReorder && e.altKey && !e.ctrlKey && !e.metaKey && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
						e.preventDefault();
						const j = e.key === "ArrowLeft" ? i - 1 : i + 1;
						if (j < 0 || j >= vm.stages.length) return onReorder(null, { id: s.id, edge: j < 0 ? "first" : "last" });
						const next = vm.stages.slice();
						[next[i], next[j]] = [next[j], next[i]];
						onReorder(next, { id: s.id, to: j });
					} else if (onSelect && (e.key === "Enter" || e.key === " ")) {
						e.preventDefault();
						onSelect(s);
					}
				},
				"aria-keyshortcuts": onReorder ? "Alt+ArrowLeft Alt+ArrowRight" : null,
				"aria-label": `Stage ${i + 1} of ${vm.stages.length}, ${name}, ${detail}, ${stageLine(s, allot, usage.metered > 0, redacted)}`,
				draggable: onReorder ? "true" : null,
				dataset: { stage: s.id },
			},
			h(
				"div",
				{ class: "n-head" },
				h("span", { class: "idx" }, String(i + 1)),
				providerDot(provider, 8),
				h("span", { class: "prov", title: `${label.main}${label.source ? ` · ${label.source}` : ""}` }, label.main),
				h("span", { class: "spacer" }),
				s.effort ? h("span", { class: "badge violet" }, s.effort) : null,
				s.lua ? h("span", { class: "badge", title: "This stage uses Lua functions" }, "Lua") : null,
			),
			h("div", { class: "n-model", title: s.model }, name),
			h(
				"div",
				{ class: "n-pool", title: detail },
				redacted
					? h("span", { class: "neo-ring hidden-usage", title: "How full the pool is right now is shown to admins; Try it tells you where your request lands", role: "img", "aria-label": "Usage shown to admins", style: { width: "22px", height: "22px" } })
					: usage.avg === null
						? meterRing({}, { size: 22 })
						: ring({ used: usage.avg, budget, size: 22, label: false, title: `The pool averages ${fmtPct(usage.avg)} of its ${s.window} meters; this stage stops at ${fmtPct(budget, null)}` }),
				h("span", null, plural(pool.length, "account")),
			),
			h("div", { class: "n-line" }, stageLine(s, allot, usage.metered > 0, redacted)),
			onReorder ? h("span", { class: "grip", title: "Drag to reorder" }, icon("grip", 14)) : null,
		);
		if (onReorder) wireDrag(node, rail, vm, onReorder);
		stages.append(node);
	});
	if (onAdd) stages.append(h("div", { class: "link adder" }, h("button", { type: "button", class: "add", onClick: onAdd, title: "Add a stage after the last one", "aria-label": "Add stage" }, icon("plus", 14))));
	rail.append(link());
	const term = vm.terminal ?? { kind: "limit" };
	rail.append(
		h(
			"div",
			{
				class: `node terminal ${litIdx === vm.stages.length ? "lit" : ""}`,
				role: "listitem",
				"aria-selected": onSelect ? String(selected === "terminal") : null,
				tabindex: onSelect ? 0 : null,
				onClick: onSelect ? () => onSelect("terminal") : null,
				dataset: { stage: "terminal" },
				onKeydown: onSelect ? (e) => {
					if (e.key === "Enter" || e.key === " ") {
						e.preventDefault();
						onSelect("terminal");
					}
				} : null,
			},
			h("div", { class: "n-head" }, compact ? "then" : "if all are spent"),
			h("div", { class: "n-model" }, compact ? (term.kind === "queue" ? "queue" : term.kind === "passthrough" ? "fall through" : "limit error") : term.kind === "queue" ? "Queue until a window resets" : term.kind === "passthrough" ? "Fall through" : "Return a limit error"),
			compact ? null : h("div", { class: "n-line" }, term.kind === "queue" ? "the caller waits" : term.kind === "passthrough" ? servedName(`neoproxy/${term.model ?? ""}`) : "HTTP 429"),
		),
	);
	// When the stages overflow, fade the clipped edge so the pinned terminal
	// does not look like it is covering a card.
	const edges = () => {
		const more = stages.scrollWidth - stages.clientWidth > 2;
		stages.classList.toggle("overflow-end", more && stages.scrollLeft + stages.clientWidth < stages.scrollWidth - 2);
		stages.classList.toggle("overflow-start", more && stages.scrollLeft > 2);
	};
	stages.addEventListener("scroll", edges, { passive: true });
	new ResizeObserver(edges).observe(stages);
	requestAnimationFrame(edges);
	return rail;
}

function wireDrag(node, rail, vm, onReorder) {
	node.addEventListener("dragstart", (e) => {
		e.dataTransfer.setData("text/plain", node.dataset.stage);
		e.dataTransfer.effectAllowed = "move";
		node.classList.add("dragging");
	});
	node.addEventListener("dragend", () => {
		node.classList.remove("dragging");
		for (const n of rail.querySelectorAll(".drop-before")) n.classList.remove("drop-before");
	});
	node.addEventListener("dragover", (e) => {
		e.preventDefault();
		node.classList.add("drop-before");
	});
	node.addEventListener("dragleave", () => node.classList.remove("drop-before"));
	node.addEventListener("drop", (e) => {
		e.preventDefault();
		const from = e.dataTransfer.getData("text/plain");
		const to = node.dataset.stage;
		if (!from || from === to) return;
		const ids = vm.stages.map((s) => s.id);
		const fi = ids.indexOf(from);
		const ti = ids.indexOf(to);
		if (fi === -1 || ti === -1) return;
		const next = vm.stages.slice();
		const [moved] = next.splice(fi, 1);
		next.splice(ti, 0, moved);
		onReorder(next, { id: moved.id, to: ti });
	});
}

// ---------- trail ----------

// 0.62 -> "62%", 0.125 -> "12.5%". Budgets are fractions of a window.
export function pctLabel(x) {
	const v = Math.round(Number(x) * 1000) / 10;
	return Number.isFinite(v) ? `${v}%` : "–";
}

// What an upstream HTTP status means for the person reading the ledger.
export function statusLabel(status) {
	if (status === 401) return "sign-in rejected";
	if (status === 403) return "forbidden";
	if (status === 404) return "model missing at provider";
	if (status === 429) return "provider rate limit";
	if (status >= 500) return "provider error";
	return "provider error";
}

// Why a stage was passed over: `short` heads a routing step, `clause` completes
// "stage 1 <model> ..." inside a sentence.
export function stageSkip(t, { accounts = [], who = "this user" } = {}) {
	const acct = (id) => accounts.find((a) => a.id === id)?.label ?? "an account in the pool";
	const lua = t.policy?.accounts?.length && t.policy.accounts.every((a) => !a.eligible);
	switch (t.skipped) {
		case "cap":
			return t.cap > 0
				? { short: `Over its ${pctLabel(t.cap)} threshold for ${who}, pool at ${pctLabel(t.total)}`, clause: `was over its ${pctLabel(t.cap)} threshold for ${who} (at ${pctLabel(t.total)})` }
				: { short: `Its threshold is 0% for ${who}, so it is off`, clause: `has a 0% threshold for ${who}, so it is switched off` };
		case "per-account":
			return { short: "Every account has used its per-account share", clause: "had every account at its per-account share" };
		case "unhealthy":
			return { short: "No healthy account could serve it", clause: "had no healthy account" };
		case "script":
			return { short: "The routing script chose another stage", clause: "was passed over by the routing script" };
		case "lua-cap":
			return { short: `Over its Lua threshold for ${who}, pool at ${pctLabel(t.total)} of ${pctLabel(t.cap)}`, clause: `was over its Lua threshold for ${who} (at ${pctLabel(t.total)} of ${pctLabel(t.cap)})` };
		case "lua-policy":
			return lua
				? { short: "Every account's Lua condition returned false", clause: "had every account's Lua condition return false" }
				: { short: "The Lua allocation is spent for every eligible account", clause: "had its Lua allocation spent on every eligible account" };
		case "upstream-error": {
			const status = t.failureStatus ? `HTTP ${t.failureStatus}, ${statusLabel(t.failureStatus)}` : "a provider error";
			return { short: `${t.chosen ? acct(t.chosen) : "The provider"} failed with ${status}`, clause: `failed${t.chosen ? ` on ${acct(t.chosen)}` : ""} with ${status}` };
		}
		default:
			return t.skipped ? { short: `Skipped: ${t.skipped}`, clause: `was skipped (${t.skipped})` } : null;
	}
}

function candidateList(t, accName) {
	const list = t.candidates ?? [];
	if (!list.length) return null;
	const row = (c) => {
		const used = Math.max(0, Math.min(1, Number(c.used) || 0));
		const budget = Math.max(0, Math.min(1, Number(c.budget) || 0));
		return h(
			"div",
			{ class: `cand ${c.accountId === t.chosen ? "chosen" : ""}` },
			h("span", { class: "acct", title: accName(c.accountId) }, accName(c.accountId)),
			c.metered
				? h("span", { class: "cand-bar", role: "img", "aria-label": `${pctLabel(c.used)} used of ${pctLabel(c.budget)} allowed`, title: `${pctLabel(c.used)} used of ${pctLabel(c.budget)} allowed` }, h("i", { style: { width: `${used * 100}%` } }), h("b", { style: { left: `${budget * 100}%` } }))
				: h("span", { class: "cand-nometer" }, "no meter"),
			h("span", { class: "num" }, c.metered ? `${pctLabel(c.used)}/${pctLabel(c.budget)}` : "always on"),
		);
	};
	const head = list.slice(0, 3).map(row);
	const rest = list.slice(3);
	return h("div", { class: "cands" }, head, rest.length ? h("details", null, h("summary", null, `${rest.length} more ${rest.length === 1 ? "account" : "accounts"}`), rest.map(row)) : null);
}

export function trail(decision, { models = [], accounts = [], users, admin = false, who = "this user" }) {
	const steps = [];
	const modelName = (id) => models.find((m) => m.id === id)?.display ?? id;
	const accName = (id) => accounts.find((a) => a.id === id)?.label ?? "an account in the pool";
	const ok = decision.outcome === "ok";
	const step = (cls, n, state, what, why, extra) =>
		h("div", { class: `step ${cls}` }, h("div", { class: "n" }, n), h("div", { class: "body" }, h("div", { class: "head" }, h("span", { class: "what" }, what), state ? h("span", { class: "state" }, state) : null), why ? h("div", { class: "why" }, why) : null, extra));
	for (const t of decision.trail ?? []) {
		if (t.kind === "stage") {
			const answered = t.chosen && !t.skipped;
			const cls = t.skipped === "upstream-error" ? "failed" : answered ? (ok ? "served" : "tried") : "skipped";
			const state = cls === "failed" ? "failed" : cls === "served" ? "answered" : cls === "tried" ? "tried" : "skipped";
			let why;
			if (t.skipped) why = stageSkip(t, { accounts, who })?.short;
			else if (t.chosen && t.metered > 0) why = `${accName(t.chosen)} · pool at ${pctLabel(t.total)} of the ${pctLabel(t.cap)} threshold${ok ? "" : ` · request ${decision.outcome}`}`;
			else if (t.chosen) why = `${accName(t.chosen)} · no meter, so it is always available${ok ? "" : ` · request ${decision.outcome}`}`;
			else why = "No account selected";
			const policy = admin && t.policy ? h("details", { open: t.skipped?.startsWith("lua") },
				h("summary", null, "Lua policy results"),
				t.policy.cap !== undefined ? h("div", { class: "why" }, `Calculated stage cap: ${pctLabel(t.policy.cap)} before grant scaling; effective ceiling ${pctLabel(t.cap)}.`) : null,
				(t.policy.accounts ?? []).map((a) => {
					const candidate = (t.candidates ?? []).find((c) => c.accountId === a.accountId);
					return h("div", { class: "why" },
						`${accName(a.accountId)}: condition ${a.eligible ? "true" : "false"}${a.limit !== undefined ? `; calculated allocation ${pctLabel(a.limit)} before grant scaling` : ""}; ${candidate ? `effective budget ${pctLabel(candidate.budget)}, used ${pctLabel(candidate.used)}` : "not an eligible candidate"}.`);
				}),
			) : null;
			const cands = admin && !t.skipped ? candidateList(t, accName) : null;
			steps.push(step(cls, `${t.index + 1}`, state, modelName(t.model), why, [cands, policy]));
		} else if (t.kind === "terminal") {
			const kind = t.terminal?.kind;
			if (kind === "passthrough") {
				steps.push(step(ok ? "served" : "end error", "end", ok ? "answered" : "failed", "Fell through", ok ? `${modelName(decision.model)} on ${accName(decision.accountId)}, chosen without a budget` : `${decision.model ? `${modelName(decision.model)} on ${accName(decision.accountId)}: ` : ""}the provider returned an error`));
			} else {
				const queued = kind === "queue";
				steps.push(step(`end ${queued ? "queued" : "limited"}`, "end", null, queued ? "Queued" : "Limit error", t.terminal?.message || (queued ? "Waits for a stage that can serve." : "No stage could serve, so the client got a limit error.")));
			}
		} else if (t.kind === "direct") {
			steps.push(step(ok ? "served" : "end error", "→", ok ? "answered" : "failed", modelName(t.model), `Direct provider model · ${accName(t.accountId)}`));
		} else if (t.kind === "denied") {
			steps.push(step("end error", "✕", null, "Not allowed", t.virtualModel ? `${who} has no grant on this virtual model` : `${who} has no direct access to ${t.providerId}`));
		} else {
			steps.push(step("end error", "✕", null, "Unknown model", t.asked ? `Nothing is named “${t.asked}” on this proxy` : null));
		}
	}
	if (!steps.length) steps.push(step("end", "–", null, "No routing steps recorded", decision.message ?? null));
	return h("div", { class: "trail" }, steps);
}

// The badge a request row carries. Pass the decision to explain errors on hover.
export function outcomeBadge(outcome, decision) {
	const detail = decision?.message ?? decision?.reason;
	const el =
		outcome === "ok" ? badge("served", "ok")
		: outcome === "limited" ? badge("limited", "warn")
		: outcome === "queued" ? badge("queued", "cyan")
		: badge("error", "bad");
	if (detail && outcome !== "ok") el.title = detail;
	return el;
}

function fmtInWindow(ts, now) {
	const m = Math.max(0, Math.round((ts - now) / 60000));
	if (m < 60) return `${m}m`;
	const hr = Math.floor(m / 60);
	if (hr < 24) return `${hr}h ${m % 60}m`;
	return `${Math.floor(hr / 24)}d ${hr % 24}h`;
}

// ---------- meters ----------
// The ring every page draws for an account: provider-reported usage when the
// provider tells us, the local token meter when the admin configured one, and
// an open ring when there is no meter at all (the account is always available).
export function preferredMeter(account) {
	const ms = account.meters ?? meters(account);
	return ms.find((m) => m.kind === "5h") ?? ms.find((m) => m.kind === "day") ?? ms.find((m) => m.kind === "week") ?? ms[0] ?? null;
}

export function meterRing(account, { size = 30, label = false, budget = null, kind } = {}) {
	const m = kind ? meterFor(account, kind) : preferredMeter(account);
	if (!m) {
		return h(
			"span",
			{ class: "neo-ring unmetered", title: "No meter: this account is always available to the stages that use it", role: "img", "aria-label": "No meter", style: { width: `${size}px`, height: `${size}px` } },
			svgEl("svg", { width: size, height: size, viewBox: `0 0 ${size} ${size}` }, svgEl("circle", { class: "track", cx: size / 2, cy: size / 2, r: size / 2 - 3, "stroke-width": 3, fill: "none", "stroke-dasharray": "2 4" })),
			label ? h("span", { class: "ring-label" }, "∞") : null,
		);
	}
	const src = m.source === "provider" ? "reported by the provider" : "local token meter";
	return ring({ used: m.used, budget, size, label, title: `${fmtPct(m.used)} of the ${m.kind} window used (${src}) · resets in ${fmtInWindow(m.resetsAt, Date.now())}` });
}

// Human health line for an account row.
export function healthStatus(account) {
	const hs = accountHealth(account);
	return status(hs.kind === "muted" ? "" : hs.kind, hs.text, undefined);
}

export function providerDot(provider, size = 8) {
	return h("span", { class: "prov-dot", style: { ...providerHueStyle(provider?.hue, provider?.chroma), width: `${size}px`, height: `${size}px` } });
}

export { append, clear };
