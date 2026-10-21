// Small DOM helpers. No framework: the dashboard is a handful of pages that
// render from JSON, and a hyperscript function is enough to keep them readable.

export function h(tag, attrs, ...children) {
	const el = document.createElement(tag);
	if (attrs && typeof attrs === "object" && !(attrs instanceof Node) && !Array.isArray(attrs)) {
		for (const [k, v] of Object.entries(attrs)) {
			if (v === null || v === undefined || v === false) continue;
			if (k === "class") el.className = v;
			else if (k === "style" && typeof v === "object") Object.assign(el.style, v);
			else if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2).toLowerCase(), v);
			else if (k === "html") el.innerHTML = v;
			else if (k === "dataset") Object.assign(el.dataset, v);
			else if (v === true) el.setAttribute(k, "");
			else el.setAttribute(k, String(v));
		}
	} else if (attrs !== undefined) {
		children.unshift(attrs);
	}
	append(el, children);
	return el;
}

export function append(el, children) {
	for (const c of children.flat(Infinity)) {
		if (c === null || c === undefined || c === false) continue;
		el.append(c instanceof Node ? c : document.createTextNode(String(c)));
	}
	return el;
}

export function clear(el) {
	while (el.firstChild) el.removeChild(el.firstChild);
	return el;
}

export function svg(pathData, size = 16, extra = {}) {
	const el = document.createElementNS("http://www.w3.org/2000/svg", "svg");
	el.setAttribute("viewBox", "0 0 24 24");
	el.setAttribute("width", size);
	el.setAttribute("height", size);
	el.setAttribute("fill", "none");
	el.setAttribute("stroke", "currentColor");
	el.setAttribute("stroke-width", "2");
	el.setAttribute("stroke-linecap", "round");
	el.setAttribute("stroke-linejoin", "round");
	el.setAttribute("aria-hidden", "true");
	for (const [k, v] of Object.entries(extra)) el.setAttribute(k, v);
	el.innerHTML = pathData;
	return el;
}

// Lucide-style paths, inlined so the app has no asset dependency.
export const icons = {
	overview: '<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/>',
	link: '<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>',
	gauge: '<path d="M12 14l4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/>',
	users: '<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/>',
	key: '<path d="m15.5 7.5 2.3 2.3a1 1 0 0 0 1.4 0l2.1-2.1a1 1 0 0 0 0-1.4L19 4"/><path d="m21 2-9.6 9.6"/><circle cx="7.5" cy="15.5" r="5.5"/>',
	shield: '<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/>',
	activity: '<path d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2"/>',
	scroll: '<path d="M8 21h12a2 2 0 0 0 2-2v-2H10v2a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v3h4"/><path d="M19 17V5a2 2 0 0 0-2-2H4"/><path d="M15 8h-5"/><path d="M15 12h-5"/>',
	layers: '<path d="M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83z"/><path d="m22 17.65-9.17 4.16a2 2 0 0 1-1.66 0L2 17.65"/><path d="m22 12.65-9.17 4.16a2 2 0 0 1-1.66 0L2 12.65"/>',
	plug: '<path d="M12 22v-5"/><path d="M9 8V2"/><path d="M15 8V2"/><path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z"/>',
	settings: '<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/>',
	route: '<circle cx="6" cy="19" r="3"/><path d="M9 19h8.5a3.5 3.5 0 0 0 0-7h-11a3.5 3.5 0 0 1 0-7H15"/><circle cx="18" cy="5" r="3"/>',
	info: '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/>',
	external: '<path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>',
	menu: '<path d="M4 5h16"/><path d="M4 12h16"/><path d="M4 19h16"/>',
	sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/>',
	moon: '<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/>',
	monitor: '<rect width="20" height="14" x="2" y="3" rx="2"/><path d="M8 21h8"/><path d="M12 17v4"/>',
	refresh: '<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>',
	trash: '<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/>',
	plus: '<path d="M5 12h14"/><path d="M12 5v14"/>',
	search: '<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>',
	copy: '<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>',
	check: '<path d="M20 6 9 17l-5-5"/>',
	x: '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>',
	arrowLeft: '<path d="m12 19-7-7 7-7"/><path d="M19 12H5"/>',
	arrowRight: '<path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>',
	inbox: '<polyline points="22 12 16 12 14 15 10 15 8 12 2 12"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/>',
	hammer: '<path d="m15 12-8.373 8.373a1 1 0 1 1-3-3L12 9"/><path d="m18 15 4-4"/><path d="m21.5 11.5-1.914-1.914A2 2 0 0 1 19 8.172V7l-2.26-2.26a6 6 0 0 0-4.202-1.756L9 2.96l.92.82A6.18 6.18 0 0 1 12 8.4V10l2 2h1.172a2 2 0 0 1 1.414.586L18.5 14.5"/>',
	upload: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" x2="12" y1="3" y2="15"/>',
	download: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" x2="12" y1="15" y2="3"/>',
	power: '<path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.77.04"/>',
	logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" x2="9" y1="12" y2="12"/>',
	spark: '<path d="M12 3v3"/><path d="M12 18v3"/><path d="m5.6 5.6 2.1 2.1"/><path d="m16.3 16.3 2.1 2.1"/><path d="M3 12h3"/><path d="M18 12h3"/><path d="m5.6 18.4 2.1-2.1"/><path d="m16.3 7.7 2.1-2.1"/>',
	save: '<path d="M15.2 3a2 2 0 0 1 1.4.6l3.8 3.8a2 2 0 0 1 .6 1.4V19a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z"/><path d="M17 21v-7a1 1 0 0 0-1-1H8a1 1 0 0 0-1 1v7"/><path d="M7 3v4a1 1 0 0 0 1 1h7"/>',
};

export function icon(name, size = 16) {
	return svg(icons[name] ?? icons.info, size);
}

// ---------- formatting ----------

export function fmtInt(n) {
	if (n === null || n === undefined || Number.isNaN(Number(n))) return "-";
	return new Intl.NumberFormat("en-US").format(Math.round(Number(n)));
}

export function fmtCompact(n) {
	n = Number(n);
	if (!Number.isFinite(n)) return "-";
	const abs = Math.abs(n);
	if (abs < 1000) return String(Math.round(n));
	if (abs < 1e6) return `${(n / 1e3).toFixed(abs < 1e4 ? 1 : 0)}K`;
	if (abs < 1e9) return `${(n / 1e6).toFixed(abs < 1e7 ? 1 : 0)}M`;
	return `${(n / 1e9).toFixed(1)}B`;
}

export function fmtPct(part, whole) {
	if (!whole) return "0.0%";
	return `${((100 * part) / whole).toFixed(1)}%`;
}

export function fmtAgo(ts) {
	if (!ts) return "-";
	const t = typeof ts === "number" ? ts : Date.parse(ts);
	if (!Number.isFinite(t)) return String(ts);
	const s = Math.max(0, (Date.now() - t) / 1000);
	if (s < 45) return "just now";
	if (s < 3600) return `${Math.round(s / 60)}m ago`;
	if (s < 86400) return `${Math.round(s / 3600)}h ago`;
	return `${Math.round(s / 86400)}d ago`;
}

export function fmtDate(ts) {
	if (!ts) return "-";
	const t = typeof ts === "number" ? ts : Date.parse(ts);
	if (!Number.isFinite(t)) return String(ts);
	return new Date(t).toLocaleString();
}

export function fmtDuration(ms) {
	if (!Number.isFinite(ms)) return "-";
	if (ms < 1000) return `${Math.round(ms)}ms`;
	if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
	return `${Math.round(ms / 60000)}m`;
}

export function mask(secret) {
	if (!secret) return "";
	const s = String(secret);
	if (s.length <= 10) return `${s.slice(0, 2)}…`;
	return `${s.slice(0, 6)}…${s.slice(-4)}`;
}

// ---------- widgets ----------

export function stat({ label, value, sub, bar, mono }) {
	return h(
		"div",
		{ class: "stat" },
		h("div", { class: "stat-label" }, label),
		h("div", { class: `stat-value${mono ? " mono" : ""}` }, value),
		sub !== undefined ? h("div", { class: "stat-sub" }, sub) : null,
		bar ? h("div", { class: `stat-bar ${bar === true ? "" : bar}` }) : null,
	);
}

export function card({ title, sub, actions, body, foot, cls = "" }) {
	return h(
		"section",
		{ class: `card ${cls}` },
		title || actions
			? h(
					"header",
					{ class: "card-header" },
					h("div", null, h("h2", { class: "card-title" }, title), sub ? h("p", { class: "card-sub" }, sub) : null),
					actions ? h("div", { class: "row" }, actions) : null,
				)
			: null,
		body,
		foot ? h("footer", { class: "card-foot" }, foot) : null,
	);
}

export function body(children, cls = "") {
	return h("div", { class: `card-body ${cls}` }, children);
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

export function btn(label, { icon: ic, cls = "", onClick, title, disabled, type = "button", href } = {}) {
	const attrs = { class: `btn ${cls}`, title, disabled, onClick };
	if (href) {
		attrs.href = href;
		if (/^https?:/.test(href)) {
			attrs.target = "_blank";
			attrs.rel = "noreferrer";
		}
		return h("a", attrs, ic ? icon(ic, 15) : null, label);
	}
	attrs.type = type;
	return h("button", attrs, ic ? icon(ic, 15) : null, label);
}

export function empty({ icon: ic = "inbox", title, hint, action }) {
	return h("div", { class: "empty" }, icon(ic, 22), h("div", { class: "title" }, title), hint ? h("div", { class: "hint" }, hint) : null, action);
}

export function table(columns, rows, { onRow, selectedKey, keyOf, emptyState } = {}) {
	if (!rows.length && emptyState) return emptyState;
	const tbl = h(
		"table",
		{ class: "table" },
		h("thead", null, h("tr", null, columns.map((c) => h("th", { class: c.cls, style: c.width ? { width: c.width } : null }, c.label)))),
		h(
			"tbody",
			null,
			rows.map((r) => {
				const key = keyOf ? keyOf(r) : undefined;
				const tr = h(
					"tr",
					{ class: onRow ? "clickable" : "", "aria-selected": selectedKey !== undefined && key === selectedKey ? "true" : null, tabindex: onRow ? 0 : null },
					columns.map((c) => h("td", { class: c.cls }, c.render(r))),
				);
				if (onRow) {
					tr.addEventListener("click", (e) => {
						if (e.target.closest("button, a, input")) return;
						onRow(r);
					});
					tr.addEventListener("keydown", (e) => {
						if (e.key === "Enter") onRow(r);
					});
				}
				return tr;
			}),
		),
	);
	return h("div", { class: "table-wrap" }, tbl);
}

export function seg(options, value, onChange) {
	const el = h("div", { class: "seg", role: "group" });
	for (const o of options) {
		el.append(
			h(
				"button",
				{ type: "button", "aria-pressed": o.value === value ? "true" : "false", onClick: () => onChange(o.value) },
				o.label,
				o.count !== undefined ? h("span", { class: "count" }, o.count) : null,
			),
		);
	}
	return el;
}

export function kv(pairs) {
	return h(
		"dl",
		{ class: "kv" },
		pairs.filter(Boolean).map(([k, v]) => [h("dt", null, k), h("dd", null, v)]),
	);
}

export function field(label, control, hint) {
	return h("div", { class: "field" }, h("label", null, label), control, hint ? h("div", { class: "hint" }, hint) : null);
}

export function toggle(checked, onChange, label) {
	const input = h("input", { type: "checkbox" });
	input.checked = Boolean(checked);
	input.addEventListener("change", () => onChange(input.checked));
	return h("label", { class: "toggle" }, input, label ? h("span", null, label) : null);
}

export async function copyText(text) {
	try {
		await navigator.clipboard.writeText(text);
		toast("Copied", "ok");
	} catch {
		toast("Clipboard unavailable", "warn");
	}
}

export function copyLine(text, label) {
	const code = h("code", { title: text }, text);
	return h(
		"div",
		{ class: "copy-line" },
		label ? h("span", { class: "dim", style: { color: "var(--ink-3)", fontSize: "12px", flex: "none" } }, label) : null,
		code,
		btn("", {
			icon: "copy",
			cls: "icon sm",
			title: "Copy",
			onClick: () => copyText(text),
		}),
	);
}

export function wip({ title, text, items = [], links = [] }) {
	return h(
		"div",
		{ class: "wip" },
		h("div", { class: "wip-mark" }, icon("hammer", 18)),
		h("h3", null, title, " ", badge("not implemented yet", "accent")),
		h("p", null, text),
		items.length ? h("ul", null, items.map((i) => h("li", null, i))) : h("span"),
		links.length ? h("div", { class: "links", style: { gridColumn: "2" } }, links) : null,
	);
}

export function toast(text, kind = "") {
	const host = document.getElementById("toasts");
	const el = h("div", { class: "toast", role: "status" }, dot(kind || "accent"), h("span", null, text));
	host.append(el);
	setTimeout(() => el.remove(), 3600);
}

export function modal({ title, sub, body: content, actions, onClose }) {
	const dlg = h(
		"dialog",
		{ class: "modal" },
		h("div", { class: "modal-head" }, h("h2", { class: "card-title" }, title), sub ? h("p", { class: "card-sub" }, sub) : null),
		h("div", { class: "modal-body" }, content),
		h("div", { class: "modal-foot" }, actions),
	);
	dlg.addEventListener("close", () => {
		dlg.remove();
		onClose?.();
	});
	dlg.addEventListener("click", (e) => {
		if (e.target === dlg) dlg.close();
	});
	document.body.append(dlg);
	dlg.showModal();
	return dlg;
}

export function confirm({ title, text, confirmLabel = "Confirm", danger = false }) {
	const { promise, resolve } = Promise.withResolvers();
	let ok = false;
	const dlg = modal({
		title,
		body: h("p", { style: { margin: 0, color: "var(--ink-2)" } }, text),
		actions: [
			btn("Cancel", { onClick: () => dlg.close() }),
			btn(confirmLabel, {
				cls: danger ? "danger outline" : "primary",
				onClick: () => {
					ok = true;
					dlg.close();
				},
			}),
		],
		onClose: () => resolve(ok),
	});
	return promise;
}

export function sparkline(values, max) {
	const m = max ?? Math.max(1, ...values);
	return h(
		"div",
		{ class: "spark" },
		values.map((v) => h("i", { style: { height: `${Math.max(4, (100 * v) / m)}%` } })),
	);
}
