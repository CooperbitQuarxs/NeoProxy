import { api, auth, onUnauthorized, invalidate, load } from "./api.js";
import { h, clear, icon, btn, toast, card, body, field, toggle, svg } from "./ui.js";
import { pages, groups } from "./pages/index.js";

const root = document.getElementById("app");

// ---------- theme ----------
const THEME_KEY = "neo-theme";
function applyTheme(mode) {
	if (mode === "light" || mode === "dark") document.documentElement.dataset.theme = mode;
	else delete document.documentElement.dataset.theme;
}
function themeMode() {
	return localStorage.getItem(THEME_KEY) || "auto";
}
function cycleTheme() {
	const order = ["auto", "dark", "light"];
	const next = order[(order.indexOf(themeMode()) + 1) % order.length];
	if (next === "auto") localStorage.removeItem(THEME_KEY);
	else localStorage.setItem(THEME_KEY, next);
	applyTheme(next);
	render();
}

// ---------- routing ----------
export function route() {
	const hash = location.hash.replace(/^#\/?/, "");
	const [path, qs] = hash.split("?");
	const parts = path.split("/").filter(Boolean);
	return { page: parts[0] || "overview", parts: parts.slice(1), query: new URLSearchParams(qs || "") };
}
export function navigate(to) {
	location.hash = `#/${to.replace(/^\/+/, "")}`;
}
window.addEventListener("hashchange", () => render());

// ---------- shell ----------
let unauthorized = false;
onUnauthorized(() => {
	if (!unauthorized) {
		unauthorized = true;
		render();
	}
});

const mark = () => {
	const el = svg(
		'<defs><linearGradient id="neo-mark" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#ed4abf"/><stop offset=".5" stop-color="#945ff9"/><stop offset="1" stop-color="#5ad8e6"/></linearGradient></defs><path fill="url(#neo-mark)" stroke="none" d="M10 14h44v9H43v33h-9V23h-9v22h-9V23H10z"/>',
		22,
		{ viewBox: "0 0 64 64", class: "topbar-mark" },
	);
	el.removeAttribute("stroke");
	return el;
};

function topbar(current, sidebarToggle) {
	const theme = themeMode();
	return h(
		"header",
		{ class: "topbar" },
		btn("", { icon: "menu", cls: "icon topbar-menu", title: "Navigation", onClick: sidebarToggle }),
		h("div", { class: "topbar-brand" }, mark(), h("span", null, "NeoProxy"), h("span", { class: "topbar-slash" }, "/"), h("span", { class: "topbar-title" }, current?.title ?? "")),
		h("div", { class: "topbar-spacer" }),
		h(
			"div",
			{ class: "topbar-actions" },
			h("span", { class: "chip", id: "health-chip", title: "Proxy health" }, h("span", { class: "dot" }), h("strong", null, "checking")),
			btn("", {
				icon: theme === "light" ? "sun" : theme === "dark" ? "moon" : "monitor",
				cls: "icon",
				title: `Theme: ${theme} (click to change)`,
				onClick: cycleTheme,
			}),
			btn("", { icon: "external", cls: "icon", title: "Legacy console (upstream management panel)", href: "/management.html" }),
			auth.key ? btn("", { icon: "logout", cls: "icon", title: "Forget management key", onClick: signOut }) : null,
		),
	);
}

function sidebar(current) {
	const el = h("nav", { class: "sidebar", "aria-label": "Sections" });
	for (const g of groups) {
		const group = h("div", { class: "nav-group" }, h("div", { class: `nav-group-label ${g.centre ? "centre" : ""}` }, g.label));
		for (const id of g.pages) {
			const p = pages[id];
			group.append(
				h(
					"a",
					{ class: "nav-row", href: `#/${id}`, "aria-current": current?.id === id ? "page" : null, title: p.title },
					icon(p.icon),
					h("span", { class: "nav-label" }, p.title),
					p.wip ? h("span", { class: "nav-wip" }, "wip") : null,
					h("span", { class: "nav-kbd" }, h("kbd", null, "G"), " ", h("kbd", null, p.key.toUpperCase())),
				),
			);
		}
		el.append(group);
	}
	el.append(
		h(
			"div",
			{ class: "sidebar-foot" },
			h("div", { class: "kbd-row" }, h("kbd", null, "G"), " then letter to jump"),
			h("div", { class: "kbd-row" }, h("kbd", null, "."), " reload page data"),
			h("div", { class: "kbd-row" }, h("kbd", null, "?"), " all shortcuts"),
		),
	);
	return el;
}

async function healthCheck() {
	const chip = document.getElementById("health-chip");
	if (!chip) return;
	try {
		const res = await fetch("/healthz", { cache: "no-store" });
		const ok = res.ok;
		clear(chip).append(h("span", { class: `dot ${ok ? "live" : "bad"}` }), h("strong", null, ok ? "Proxy up" : "Proxy down"), h("span", { class: "mono" }, location.host));
	} catch {
		clear(chip).append(h("span", { class: "dot bad" }), h("strong", null, "Unreachable"));
	}
}

function signOut() {
	auth.clear();
	invalidate();
	unauthorized = true;
	render();
}

// ---------- gate ----------
function gate(reason) {
	const input = h("input", { class: "input mono", type: "password", placeholder: "management key", autocomplete: "current-password", autofocus: true, style: { width: "100%" } });
	let remember = false;
	const err = h("div", { class: "callout bad", hidden: true });
	const form = h(
		"form",
		{
			onSubmit: async (e) => {
				e.preventDefault();
				const key = input.value.trim();
				if (!key) return;
				auth.set(key, remember);
				try {
					await api.get("config");
					unauthorized = false;
					invalidate();
					render();
				} catch (ex) {
					err.hidden = false;
					err.textContent = ex.status === 401 || ex.status === 403 ? "That key was rejected. If you are not on localhost, remote management must be allowed in the configuration." : `Could not reach the management API: ${ex.message}`;
					auth.clear();
				}
			},
		},
		field("Management key", input, "The secret from management.secret-key in config.yaml."),
		toggle(false, (v) => (remember = v), "Remember on this browser"),
		err,
		h("div", { class: "row", style: { justifyContent: "flex-end", marginTop: "4px" } }, btn("Open dashboard", { cls: "primary", type: "submit" })),
	);
	return h(
		"div",
		{ class: "gate" },
		card({
			body: body([
				h("div", { style: { padding: "6px 0 14px" } }, h("div", { class: "gate-mark" }, mark()), h("h1", null, "NeoProxy"), h("p", { class: "card-sub", style: { marginTop: "4px" } }, reason || "The dashboard talks to the management API in your browser. Enter the key to continue.")),
				form,
			]),
		}),
	);
}

// ---------- render ----------
let currentCleanup = null;
let lastPageId = null;

export async function render() {
	const r = route();
	const current = pages[r.page] ?? pages.overview;
	const needsAuth = !auth.key || unauthorized;
	document.title = `${current.title} · NeoProxy`;

	currentCleanup?.();
	currentCleanup = null;

	clear(root);
	let sidebarEl = null;
	const toggleSidebar = () => {
		if (!sidebarEl) return;
		sidebarEl.dataset.open = sidebarEl.dataset.open === "true" ? "false" : "true";
	};
	root.append(topbar(current, toggleSidebar));
	healthCheck();

	if (needsAuth) {
		root.append(gate(unauthorized && auth.key ? "The management API rejected the stored key." : undefined));
		if (unauthorized) auth.clear();
		return;
	}

	sidebarEl = sidebar(current);
	const panel = h("main", { class: "shell-panel", id: "panel", tabindex: -1 });
	root.append(h("div", { class: "shell" }, sidebarEl, panel));

	const ctx = { h, route: r, navigate, toast, reload: () => render(), load, api, invalidate };
	try {
		const result = await current.render(ctx);
		if (result instanceof Node) panel.append(result);
		else if (Array.isArray(result)) panel.append(...result.filter(Boolean));
		if (typeof current.cleanup === "function") currentCleanup = current.cleanup;
		if (result && typeof result.cleanup === "function") currentCleanup = result.cleanup;
	} catch (ex) {
		panel.append(
			h("div", { class: "page-head" }, h("div", null, h("h1", { class: "page-title" }, current.title))),
			h("div", { class: "callout bad" }, "This page failed to load: ", ex.message),
		);
		console.error(ex);
	}
	if (lastPageId !== current.id) {
		window.scrollTo(0, 0);
		lastPageId = current.id;
	}
}

// ---------- keyboard ----------
let chord = null;
let chordTimer = 0;
document.addEventListener("keydown", (e) => {
	if (e.target.closest("input, textarea, select, [contenteditable]") || e.metaKey || e.ctrlKey || e.altKey) return;
	if (document.querySelector("dialog[open]")) return;
	if (chord === "g") {
		chord = null;
		clearTimeout(chordTimer);
		const hit = Object.values(pages).find((p) => p.key === e.key.toLowerCase());
		if (hit) {
			e.preventDefault();
			navigate(hit.id);
		}
		return;
	}
	if (e.key === "g" || e.key === "G") {
		chord = "g";
		chordTimer = setTimeout(() => (chord = null), 1200);
		return;
	}
	if (e.key === ".") {
		invalidate();
		render();
		toast("Reloaded");
		return;
	}
	if (e.key === "?") {
		import("./pages/index.js").then(() => showShortcuts());
	}
});

function showShortcuts() {
	import("./ui.js").then(({ modal, kv }) => {
		const dlg = modal({
			title: "Keyboard shortcuts",
			body: kv([
				...Object.values(pages).map((p) => [h("span", null, h("kbd", null, "G"), " ", h("kbd", null, p.key.toUpperCase())), p.title]),
				[h("kbd", null, "."), "Reload the current page's data"],
				[h("kbd", null, "Esc"), "Close dialogs"],
			]),
			actions: [btn("Close", { cls: "primary", onClick: () => dlg.close() })],
		});
	});
}

applyTheme(themeMode());
render();
