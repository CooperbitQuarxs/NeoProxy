import { h, clear, icon, svg } from "./lib/dom.js";
import { session, neo, engine, onUnauthorized, invalidate, ApiError } from "./lib/client.js";
import { btn, toast, field, input, iconBtn, card, dialog, dismissDialogs } from "./lib/components.js";
import { pages, groups } from "./pages/index.js";

const root = document.getElementById("app");

// ---------- theme ----------
const THEME_KEY = "neo-theme";
function themeMode() {
	return localStorage.getItem(THEME_KEY) || "auto";
}
function applyTheme(mode) {
	if (mode === "auto") delete document.documentElement.dataset.theme;
	else document.documentElement.dataset.theme = mode;
}
function cycleTheme() {
	const next = { auto: "light", light: "dark", dark: "auto" }[themeMode()];
	localStorage.setItem(THEME_KEY, next);
	applyTheme(next);
	toast(`Theme: ${next}`);
	render();
}

// ---------- routing ----------
export function route() {
	const hash = location.hash.replace(/^#\/?/, "");
	const [path, query = ""] = hash.split("?");
	const segs = path.split("/").filter(Boolean);
	return { id: segs[0] || "overview", params: segs.slice(1), query: new URLSearchParams(query) };
}
export function navigate(to) {
	location.hash = `#/${to.replace(/^\/+/, "")}`;
}
window.addEventListener("hashchange", () => render());

// ---------- session ----------
let unauthorized = false;
onUnauthorized(() => {
	if (unauthorized) return;
	unauthorized = true;
	toast("Your session expired or was revoked. Sign in again.", "bad");
	session.clear();
	render();
});

async function signOut() {
	try {
		await neo.signOut();
		dismissDialogs();
		render();
	} catch (e) {
		toast(`Sign out failed: ${e.message}. Your session may still be active.`, "bad");
	}
}

export function currentSession() {
	return session.get();
}

function allowed(page, s) {
	if (!page.roles) return true;
	return page.roles.includes(s?.role);
}

// ---------- chrome ----------
const mark = () => h("img", { class: "mark", src: "mark.svg", alt: "" });

function topbar(page, s, onMenu) {
	return h(
		"header",
		{ class: "topbar" },
		h("button", { class: "btn icon menu-btn", onClick: onMenu, "aria-label": "Menu", style: { display: "none" } }, icon("menu")),
		h("a", { class: "brand", href: "#/overview" }, mark(), "NeoProxy", h("span", { class: "crumb" }, "/"), h("span", { class: "crumb-page" }, page.title)),
		h("span", { class: "spacer" }),
		h("span", { class: "chip", id: "core-chip" }, h("span", { class: "dot" }), "Core", h("span", { class: "mono" }, "checking")),
		h("span", { class: "chip", id: "proxy-chip" }, h("span", { class: "dot" }), "Proxy", h("span", { class: "mono" }, "checking")),
		iconBtn({ auto: "monitor", light: "sun", dark: "moon" }[themeMode()], `Theme: ${themeMode()}`, cycleTheme),
		h("span", { class: "chip" }, `${s.handle} · ${s.role === "user" ? "member" : s.role}`),
		iconBtn("logout", `Sign out ${s.handle}`, signOut),
	);
}

async function refreshChips() {
	const core = document.getElementById("core-chip");
	const proxy = document.getElementById("proxy-chip");
	try {
		const c = await neo.core();
		if (core) {
			core.querySelector(".dot").className = `dot ${c.linked ? "live" : "warn"}`;
			core.querySelector(".mono").textContent = c.linked ? c.host : "not linked";
			core.title = c.linked ? `Connected to ${c.endpoint} as ${c.identity}` : "NeoProxy engine is not connected";
		}
	} catch (e) {
		if (core) { core.querySelector(".dot").className = "dot bad"; core.querySelector(".mono").textContent = "unknown"; core.title = e.message; }
	}
	try {
		const ok = await engine.health();
		if (proxy) {
			proxy.querySelector(".dot").className = `dot ${ok ? "ok" : "bad"}`;
			proxy.querySelector(".mono").textContent = ok ? location.host : "down";
		}
	} catch {
		if (proxy) {
			proxy.querySelector(".dot").className = "dot bad";
			proxy.querySelector(".mono").textContent = "unreachable";
		}
	}
}

function sidebar(current, s) {
	const el = h("nav", { class: "sidebar", "aria-label": "Sections" });
	for (const g of groups) {
		const items = pages.filter((p) => p.group === g.id && !p.hidden && allowed(p, s));
		if (!items.length) continue;
		el.append(
			h(
				"div",
				{ class: "group" },
				h("div", { class: `group-label ${g.id === "core" ? "core" : ""}` }, g.label),
				items.map((p) =>
					h(
						"a",
						{ class: "nav-row", href: `#/${p.id}`, "aria-current": p.id === current.id ? "page" : null },
						icon(p.icon, 16),
						h("span", { class: "label" }, p.title),
						p.chord ? h("kbd", null, p.chord.toUpperCase()) : null,
					),
				),
			),
		);
	}
	el.append(
		h(
			"div",
			{ class: "foot" },
			h("div", null, h("kbd", null, "G"), " then a letter to jump"),
			h("div", null, h("kbd", null, "/"), " search on the page"),
			h("div", null, h("kbd", null, "?"), " all shortcuts"),
		),
	);
	return el;
}

// ---------- gate ----------
function gate(reason, bootstrap = false) {
	dismissDialogs();
	clear(root);
	let handle = "", password = "", operatorSecret = "", busy = false;
	const err = h("div", { class: "error hidden", role: "alert" });
	const submitBtn = btn(bootstrap ? "Create first admin" : "Sign in", { cls: "primary", type: "submit" });
	const submit = async (e) => {
		e.preventDefault();
		if (busy || !handle || !password || (bootstrap && !operatorSecret)) return;
		busy = true;
		submitBtn.disabled = true;
		err.classList.add("hidden");
		try {
			const s = bootstrap ? await neo.bootstrap(handle.trim(), password, operatorSecret) : await neo.signIn(handle.trim(), password);
			password = operatorSecret = "";
			unauthorized = false;
			toast(`Signed in as ${s.handle}`);
			const destination = s.role === "admin" ? "overview" : s.role === "owner" ? "accounts" : "mine";
			if (route().id === destination) render();
			else navigate(destination);
		} catch (ex) {
			err.textContent = ex instanceof ApiError ? ex.message : "Could not reach the proxy";
			err.classList.remove("hidden");
		} finally {
			busy = false;
			submitBtn.disabled = false;
		}
	};
	root.append(h("div", { class: "gate" }, card({
		rise: true,
		body: h("form", { onSubmit: submit },
			h("div", { class: "brand" }, mark(), "NeoProxy"),
			h("h1", null, bootstrap ? "Set up NeoProxy" : "Sign in"),
			h("p", { class: "lead" }, reason ?? (bootstrap ? "Create the first organization admin. Setup closes after this account exists." : "Use your NeoProxy username and password. Inference keys are for clients, not dashboard login.")),
			field("Username", input({ name: "username", autocomplete: "username", required: true, autofocus: true, onInput: (e) => (handle = e.target.value) })),
			field("Password", input({ name: "password", type: "password", autocomplete: bootstrap ? "new-password" : "current-password", required: true, onInput: (e) => (password = e.target.value) })),
			bootstrap ? field("Operator management secret", input({ type: "password", autocomplete: "off", required: true, onInput: (e) => (operatorSecret = e.target.value) }), "Used once to authorize bootstrap. Never saved by this dashboard.") : null,
			err,
			h("div", { class: "row" }, btn(bootstrap ? "Back to sign in" : "First-time setup", { onClick: () => gate(null, !bootstrap) }), h("span", { class: "spacer" }), submitBtn),
			h("div", { class: "foot" }, location.protocol === "https:" ? "Your session uses an HttpOnly cookie." : "Local HTTP is not encrypted. Do not use this connection across an untrusted network."),
		),
	})));
}

// ---------- render ----------
let cleanup = null;
let lastId = null;
let sidebarEl = null;
let renderGeneration = 0;
let expiryTimer;

export async function render() {
	const generation = ++renderGeneration;
	clearTimeout(expiryTimer);
	if (cleanup) { cleanup(); cleanup = null; }
	const s = session.get();
	if (!s) return gate();
	if (Number.isFinite(s.expiresAt)) {
		const remaining = s.expiresAt - Date.now();
		if (remaining <= 0) { session.clear(); return gate("Your session expired. Sign in again."); }
		expiryTimer = setTimeout(() => { if (session.get() === s) render(); }, Math.min(remaining, 2_147_483_647));
	}
	const r = route();
	let page = pages.find((p) => p.id === r.id);
	if (!page) page = pages.find((p) => p.id === (s.role === "admin" ? "overview" : s.role === "owner" ? "accounts" : "mine"));
	const denied = !allowed(page, s);
	document.title = `${page.title} · NeoProxy`;
	clear(root);
	sidebarEl = sidebar(page, s);
	const pageEl = h("div", { class: "page", id: "page" });
	const panel = h("div", { class: "panel" }, pageEl);
	const shell = h(
		"div",
		{ class: "shell" },
		topbar(page, s, () => sidebarEl.classList.toggle("open")),
		sidebarEl,
		h("main", { class: "shell-main" }, panel),
	);
	root.append(shell);
	if (window.innerWidth <= 900) shell.querySelector(".menu-btn").style.display = "";
	refreshChips();

	const ctx = {
		root: pageEl,
		params: r.params,
		query: r.query,
		session: s,
		navigate,
		setInspector(el) {
			panel.querySelector(".inspector")?.remove();
			if (el) panel.append(el);
		},
		rerender: () => render(),
	};
	try {
		if (denied) {
			pageEl.append(h("div", { class: "empty" }, icon("shield", 22), h("div", { class: "title" }, "Access denied"), h("div", { class: "hint" }, `The ${s.role === "user" ? "member" : s.role} role cannot open this organization administration page.`)));
			return;
		}
		const dispose = (await page.render(ctx)) ?? null;
		if (generation === renderGeneration) cleanup = dispose;
		else dispose?.();
	} catch (e) {
		if (generation !== renderGeneration) return;
		clear(pageEl);
		pageEl.append(h("div", { class: "empty" }, icon("info", 22), h("div", { class: "title" }, "This page failed to render"), h("div", { class: "hint" }, e?.message ?? String(e))));
		console.error(e);
	}
	lastId = page.id;
}

// ---------- keyboard ----------
let chord = null;
let chordTimer = 0;
document.addEventListener("keydown", (e) => {
	const t = e.target;
	const typing = t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable);
	if (e.key === "Escape" && typing) {
		t.blur();
		return;
	}
	if (typing || e.metaKey || e.ctrlKey || e.altKey) return;
	if (chord === "g") {
		chord = null;
		clearTimeout(chordTimer);
		const p = pages.find((x) => x.chord && x.chord[1] === e.key.toLowerCase());
		if (p) {
			e.preventDefault();
			navigate(p.id);
		}
		return;
	}
	if (e.key === "g") {
		chord = "g";
		chordTimer = setTimeout(() => (chord = null), 1200);
		return;
	}
	if (e.key === "/") {
		const s = document.querySelector("#page input[type=search]");
		if (s) {
			e.preventDefault();
			s.focus();
		}
		return;
	}
	if (e.key === ".") {
		e.preventDefault();
		invalidate();
		render();
		return;
	}
	if (e.key === "?") {
		e.preventDefault();
		showShortcuts();
	}
});

function showShortcuts() {
	const s = session.get();
	const rows = pages.filter((p) => p.chord && allowed(p, s)).map((p) => h("div", { class: "row", style: { padding: "4px 0" } }, h("kbd", null, "G"), h("kbd", null, p.chord[1].toUpperCase()), h("span", null, p.title)));
	dialog({
		title: "Keyboard",
		body: h(
			"div",
			null,
			rows,
			h("div", { class: "row", style: { padding: "4px 0", marginTop: "8px" } }, h("kbd", null, "/"), h("span", null, "Focus the page search")),
			h("div", { class: "row", style: { padding: "4px 0" } }, h("kbd", null, "."), h("span", null, "Reload page data")),
			h("div", { class: "row", style: { padding: "4px 0" } }, h("kbd", null, "Esc"), h("span", null, "Close the inspector or leave a field")),
		),
		actions: (close) => [btn("Close", { cls: "primary", onClick: close })],
	});
}

applyTheme(themeMode());
clear(root);
root.append(h("div", { class: "gate" }, "Restoring session…"));
try { await neo.restore(); await render(); }
catch (e) {
	clear(root);
	root.append(h("div", { class: "gate" }, card({ title: "Cannot restore session", body: h("p", { role: "alert" }, e.message), actions: [btn("Retry", { onClick: () => location.reload() })] })));
}
