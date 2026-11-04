// Plugins: whether this engine loads them, what is installed, what the store
// offers, and how to write one. Every claim on this page is read from the engine
// (`plugins_enabled`, `plugins_dir`, the store response) or from the source files
// it cites. The three parts are tabs, so the page stays one screen tall.

import { h, icon, fmtInt, plural, debounce } from "../lib/dom.js";
import { engine, invalidate, ApiError } from "../lib/client.js";
import { pageHead, card, btn, iconBtn, badge, toggle, empty, inspector, section, kv, copyLine, spinner, errorState, toast, confirm } from "../lib/components.js";

const REPO = "https://github.com/PsychedelicShayna/NeoProxy";
const source = (path, dir = false) => `${REPO}/${dir ? "tree" : "blob"}/HEAD/${path}`;
const STORE_PAGE = 12;
const KINDS_SHOWN = 8;

// The engine HTML-escapes strings it returns; we set text, so undo that.
const ENTITIES = { "&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": '"', "&#39;": "'", "&#34;": '"' };
const plain = (value) => String(value ?? "").replace(/&(?:amp|lt|gt|quot|#39|#34);/g, (entity) => ENTITIES[entity]);
const nameOf = (plugin) => plain(plugin.metadata?.name || plugin.name || plugin.id);
const repoUrl = (repo) => (!repo ? null : /^https?:/.test(repo) ? repo : `https://github.com/${repo}`);
const link = (href, text) => h("a", { href, target: "_blank", rel: "noreferrer", class: "plug-link" }, text, icon("external", 12));
const filePath = (path, dir) => link(source(path, dir), h("code", null, path));
const titleCase = (s) => s.replace(/^./, (c) => c.toUpperCase());

function failure(e) {
	const body = e instanceof ApiError && e.body && typeof e.body === "object" ? e.body : null;
	return { code: typeof body?.error === "string" ? body.error : e instanceof ApiError ? String(e.status) : "", message: plain(body?.message || e.message || "The request failed") };
}

function mark(name) {
	const letters = plain(name).replace(/[^A-Za-z0-9 ]/g, "").split(/\s+/).filter(Boolean).slice(0, 2).map((word) => word[0].toUpperCase()).join("") || "?";
	let hue = 0;
	for (const ch of name) hue = (hue * 31 + ch.charCodeAt(0)) % 360;
	return h("span", { class: "plug-mark", style: { "--plug-h": String(hue) }, "aria-hidden": "true" }, letters);
}

function listOf(result) {
	return Array.isArray(result?.plugins) ? result.plugins : Array.isArray(result) ? result : [];
}

export async function render(ctx) {
	const { root } = ctx;
	let installedInfo = { plugins_enabled: false, plugins_dir: "plugins", plugins: [] };
	let installedError = null;
	let storeInfo = null;
	let storeError = null;
	let storeQuery = "";
	let storeKind = "all";
	let storeLimit = STORE_PAGE;
	let selected = null;
	let disposed = false;
	let selectionRevision = 0;
	let tab = ["installed", "store", "build"].includes(ctx.query?.get("tab")) ? ctx.query.get("tab") : null;
	// Per-store-plugin outcome of the last install attempt, kept across repaints.
	const outcomes = new Map();
	const installing = new Set();

	const statusBody = h("div", { class: "plug-status" }, spinner());
	const installedBody = h("div", null, spinner());
	const storeBody = h("div", null, spinner());

	const loading = () => Boolean(installedInfo.plugins_enabled);
	const dirText = () => plain(installedInfo.plugins_dir || storeInfo?.plugins_dir || "plugins");
	const installed = () => listOf(installedInfo);

	async function load() {
		invalidate("plugin");
		const [installedResult, storeResult] = await Promise.allSettled([engine.plugins(), engine.pluginStore()]);
		if (disposed) return;
		installedError = installedResult.status === "rejected" ? installedResult.reason : null;
		if (installedResult.status === "fulfilled") installedInfo = { ...installedInfo, ...installedResult.value, plugins: listOf(installedResult.value) };
		storeError = storeResult.status === "rejected" ? storeResult.reason : null;
		if (storeResult.status === "fulfilled") storeInfo = storeResult.value;
		paintStatus();
		paintInstalled();
		paintKinds();
		paintStore();
		paintTabs();
		if (selected && !installed().some((plugin) => plugin.id === selected)) closeInspector();
	}

	// ---------- is it on ----------

	function paintStatus() {
		if (installedError) { statusBody.replaceChildren(errorState(installedError, load)); return; }
		const on = loading();
		let control;
		control = toggle(on, async (value) => {
			control.disabled = true;
			try {
				await engine.patch("config", { plugins: { enabled: value } });
				invalidate("plugin");
				toast(value ? "Plugin loading is on" : "Plugin loading is off");
				await load();
				statusBody.querySelector(".switch")?.focus();
			} catch (e) {
				control.setAttribute("aria-checked", String(!value));
				toast(failure(e).message, "bad");
			} finally { control.disabled = false; }
		}, "Load plugins");
		statusBody.classList.toggle("on", on);
		statusBody.replaceChildren(
			h("div", { class: "plug-status-main" },
				h("span", { class: `plug-status-icon ${on ? "on" : ""}` }, icon(on ? "check" : "power", 16)),
				h("div", { class: "plug-status-text" },
					h("div", { class: "plug-status-head" }, on ? "Plugins load on this engine" : "Plugins are off on this engine"),
					h("p", null, on
						? `Plugins switched on below run when the engine starts or reloads its settings.`
						: "You can install plugins now; none of them runs until loading is on."),
				),
				control,
			),
			h("dl", { class: "plug-facts" },
				h("div", null, h("dt", null, "Folder"), h("dd", null, copyLine(dirText(), "plugin folder"))),
				h("div", null, h("dt", null, "Found there"), h("dd", null, plural(installed().length, "plugin"))),
				h("div", null, h("dt", null, "Needs"), h("dd", null, "An engine built with cgo, as release builds are")),
			),
		);
	}

	// ---------- installed ----------

	function stateOf(plugin) {
		if (plugin.effective_enabled) return { kind: "ok", text: "running", hint: "Loaded and active." };
		if (plugin.enabled && !loading()) return { kind: "warn", text: "on, loading is off", hint: "Switched on, but plugin loading is off for the whole engine." };
		if (plugin.enabled && !plugin.registered) return { kind: "warn", text: "on, not loaded", hint: "Switched on, but the engine did not register it. Check the engine log for the loader's error." };
		return { kind: "", text: "off", hint: "Switched off." };
	}

	function closeInspector() {
		selected = null;
		selectionRevision++;
		ctx.setInspector(null);
		paintInstalled();
	}

	function openInspector(plugin) {
		selected = plugin.id;
		const revision = ++selectionRevision;
		paintInstalled();
		const quotaBody = h("div");
		const authIndex = ctx.query?.get("auth_index");
		const name = nameOf(plugin);
		const loadQuota = async () => {
			quotaBody.replaceChildren(spinner());
			try {
				const quota = await engine.get(`plugins/${encodeURIComponent(plugin.id)}/quota`, { query: { auth_index: authIndex } });
				if (disposed || revision !== selectionRevision) return;
				const pairs = quota && typeof quota === "object" ? Object.entries(quota) : [["Quota", quota]];
				quotaBody.replaceChildren(kv(pairs.map(([key, value]) => [titleCase(key.replace(/[_-]+/g, " ")), value && typeof value === "object" ? kv(Object.entries(value).map(([k, v]) => [k, String(v)])) : h("code", null, String(value))])));
			} catch (e) { if (!disposed && revision === selectionRevision) quotaBody.replaceChildren(errorState(e, loadQuota)); }
		};
		const clearQuota = btn("Clear quota", { cls: "danger", onClick: async () => {
			if (!(await confirm({ title: `Clear quota for ${name}?`, text: "The selected credential's quota state will be cleared.", confirmLabel: "Clear quota", danger: true }))) return;
			clearQuota.disabled = true;
			try {
				await engine.del(`plugins/${encodeURIComponent(plugin.id)}/quota`, { query: { auth_index: authIndex } });
				invalidate("plugin");
				toast(`Cleared quota for ${name}`);
				if (!disposed && selected === plugin.id) await loadQuota();
			} catch (e) { toast(failure(e).message, "bad"); }
			finally { clearQuota.disabled = false; }
		} });
		const state = stateOf(plugin);
		const fields = plugin.config_fields?.length ? plugin.config_fields : plugin.metadata?.config_fields ?? [];
		const repo = repoUrl(plugin.metadata?.github_repository);
		const body = [
			section("Plugin", kv([
				["State", h("div", null, h("span", { class: "status" }, h("span", { class: `dot ${state.kind}` }), state.text), h("div", { class: "hint" }, state.hint))],
				["Version", h("code", null, plugin.metadata?.version || "not reported")],
				plugin.metadata?.author ? ["Author", plain(plugin.metadata.author)] : null,
				repo ? ["Source", link(repo, plain(plugin.metadata.github_repository))] : null,
				["File", plugin.path ? h("code", null, plain(plugin.path)) : "not found in the plugin folder"],
				["Adds", [plugin.supports_oauth ? `sign-in for ${plain(plugin.oauth_provider || "a provider")}` : null, plugin.supports_quota ? "quota reporting" : null, plugin.menus?.length ? `${plural(plugin.menus.length, "management page")}` : null].filter(Boolean).join(", ") || (plugin.registered ? "request handling" : "not known until it loads")],
			])),
		];
		if (fields.length) body.push(section("Settings it reads", h("ul", { class: "plug-fields" }, fields.map((field) => h("li", null, h("code", null, field.name), field.type ? h("span", { class: "ink-3" }, ` ${field.type}`) : null, field.description ? h("div", { class: "ink-3" }, plain(field.description)) : null))), h("p", { class: "hint" }, "Set them under ", h("code", null, `plugins.configs.${plugin.id}`), " in ", h("a", { href: "#/config?view=file" }, "Configuration"), ".")));
		if (plugin.supports_quota) {
			quotaBody.append(authIndex ? spinner() : h("p", { class: "ink-2" }, "Quota is tracked per account; open an account on the Pool page to see it."));
			body.push(section("Quota", quotaBody));
		}
		ctx.setInspector(inspector({ title: name, sub: plugin.id, onClose: closeInspector, body, foot: plugin.supports_quota && authIndex ? [clearQuota] : null }));
		if (plugin.supports_quota && authIndex) loadQuota();
	}

	function installedRow(plugin) {
		const name = nameOf(plugin);
		const state = stateOf(plugin);
		let control;
		control = toggle(Boolean(plugin.enabled), async (enabled) => {
			control.disabled = true;
			try {
				await engine.patch(`config/plugins/configs/${encodeURIComponent(plugin.id)}`, { enabled });
				invalidate("plugin");
				plugin.enabled = enabled;
				toast(`${name} ${enabled ? "on" : "off"}`);
				await load();
			} catch (e) {
				control.setAttribute("aria-checked", String(!enabled));
				toast(failure(e).message, "bad");
			} finally { control.disabled = false; }
		}, `Switch ${name} on or off`, { showLabel: false });
		const stop = (e) => e.stopPropagation();
		return h("div", { class: "li clickable plug-row", role: "button", tabindex: 0, "aria-selected": String(selected === plugin.id), onClick: () => openInspector(plugin), onKeydown: (e) => { if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); openInspector(plugin); } } },
			mark(name),
			h("div", { class: "lt" }, h("div", { class: "main" }, name), h("div", { class: "sub" }, h("span", { class: "mono" }, plugin.id), plugin.metadata?.version ? ` · version ${plain(plugin.metadata.version)}` : "")),
			badge(state.text, state.kind),
			h("span", { class: "row", onClick: stop, onKeydown: stop }, control, iconBtn("trash", `Remove ${name}`, async () => {
				if (!(await confirm({ title: `Remove ${name}?`, text: "The engine deletes its file, its settings and its saved quota state, and it stops running. You can install it again from the store.", confirmLabel: "Remove plugin", danger: true }))) return;
				try {
					await engine.del(`plugins/${encodeURIComponent(plugin.id)}`);
					outcomes.delete(plugin.id);
					toast(`Removed ${name}`);
					if (selected === plugin.id) closeInspector();
					await load();
				} catch (e) { toast(failure(e).message, "bad"); }
			}, "danger sm")),
		);
	}

	function paintInstalled() {
		if (installedError) { installedBody.replaceChildren(errorState(installedError, load)); return; }
		const list = installed();
		installedBody.replaceChildren(list.length
			? h("div", { class: "list" }, list.map(installedRow))
			: empty({ icon: "plug", title: "No plugins installed", hint: `Nothing in ${dirText()} yet. Install one from the store, or drop in a library you built.`, action: h("div", { class: "row wrap plug-empty-acts" }, btn("Browse the store", { cls: "primary", icon: "search", onClick: () => setTab("store", true) }), btn("Build your own", { cls: "outline", onClick: () => setTab("build", true) })) }));
	}

	// ---------- store ----------

	// The most common kinds, merged across spelling, as one-click filters.
	function kinds() {
		const counts = new Map();
		for (const plugin of storeInfo?.plugins ?? []) {
			for (const key of new Set((plugin.tags ?? []).map((tag) => plain(tag).toLowerCase()))) {
				const entry = counts.get(key) ?? { label: titleCase(key), n: 0 };
				entry.n++;
				counts.set(key, entry);
			}
		}
		return [...counts.entries()].sort((a, b) => b[1].n - a[1].n || a[0].localeCompare(b[0])).slice(0, KINDS_SHOWN).map(([value, entry]) => ({ value, ...entry }));
	}

	async function install(plugin, update) {
		const name = plain(plugin.name || plugin.id);
		const ok = await confirm({
			title: `${update ? "Update" : "Install"} ${name}?`,
			text: `The engine downloads ${plugin.version ? `version ${plain(plugin.version)} ` : "it "}from ${plain(plugin.repository || plugin.source_name || "the store")}, checks its checksum, writes it into ${dirText()} and switches it on. A plugin runs inside the engine and sees every request, so install code you trust.`,
			confirmLabel: update ? "Update plugin" : "Install plugin",
		});
		if (!ok) return;
		installing.add(plugin.store_id ?? plugin.id);
		outcomes.delete(plugin.id);
		paintStore();
		try {
			const result = await engine.post(`plugins/store/${encodeURIComponent(plugin.id)}/install`, {}, { query: { source: plugin.source_id } });
			outcomes.set(plugin.id, { kind: "ok", text: `Installed ${plain(result?.version || plugin.version || "")}`.trim() + (result?.path ? ` at ${plain(result.path)}.` : "."), more: result?.plugins_enabled === false ? "Plugin loading is off, so it will not run until you turn it on at the top." : result?.restart_required ? "Restart the engine to finish." : null });
			toast(`${update ? "Updated" : "Installed"} ${name}`);
		} catch (e) {
			const { code, message } = failure(e);
			outcomes.set(plugin.id, { kind: "bad", text: message, code });
		} finally {
			installing.delete(plugin.store_id ?? plugin.id);
		}
		await load();
	}

	function storeItem(plugin) {
		const name = plain(plugin.name || plugin.id);
		const has = plugin.installed || installed().some((entry) => entry.id === plugin.id);
		const busy = installing.has(plugin.store_id ?? plugin.id);
		const outcome = outcomes.get(plugin.id);
		const needsAuth = plugin.auth_required && !plugin.auth_configured;
		const homepage = repoUrl(plugin.repository || plugin.homepage);
		let action;
		if (busy) action = btn("Installing…", { cls: "outline sm", disabled: true });
		else if (has && plugin.update_available) action = btn(`Update to ${plain(plugin.version)}`, { cls: "primary sm", onClick: () => install(plugin, true) });
		else if (has) action = h("span", { class: "plug-installed" }, icon("check", 13), `Installed${plugin.installed_version ? ` ${plain(plugin.installed_version)}` : ""}`);
		else action = btn("Install", { cls: "outline sm", icon: "download", onClick: () => install(plugin, false) });
		const tags = [...new Set((plugin.tags ?? []).map((tag) => titleCase(plain(tag))))].slice(0, 3);
		return h("article", { class: "plug-item" },
			h("div", { class: "plug-item-head" }, mark(name), h("div", { class: "lt" }, h("div", { class: "main" }, name), h("div", { class: "sub" }, plugin.author ? `by ${plain(plugin.author)}` : plugin.id, plugin.version ? h("span", { class: "plug-ver" }, ` · v${plain(plugin.version).replace(/^v/, "")}`) : null))),
			h("p", { class: "plug-desc" }, plain(plugin.description) || "No description."),
			tags.length ? h("div", { class: "plug-tags" }, tags.map((t) => h("span", { class: "plug-tag" }, t))) : null,
			needsAuth ? h("p", { class: "plug-note warn" }, "This download needs credentials. Add a matching rule under ", h("code", null, "plugins.store-auth"), " in Configuration first.") : null,
			h("div", { class: "plug-foot" }, action, h("span", { class: "spacer" }), homepage ? link(homepage, "Source") : null),
			outcome ? h("div", { class: `plug-result ${outcome.kind}`, role: outcome.kind === "bad" ? "alert" : "status" },
				h("div", { class: "plug-result-head" }, outcome.kind === "bad" ? "Install failed" : "Done", outcome.code ? h("code", null, outcome.code) : null, iconBtn("x", "Dismiss", () => { outcomes.delete(plugin.id); paintStore(); }, "sm")),
				h("pre", { class: "plug-result-text" }, outcome.text),
				outcome.more ? h("div", { class: "plug-result-more" }, outcome.more) : null) : null,
		);
	}

	const kindBar = h("div", { class: "plug-kinds", role: "group", "aria-label": "Kind" });
	const storeList = h("div");
	const storeFoot = h("div", { class: "plug-more" });
	const summary = h("span", { class: "plug-summary", role: "status" });
	const storeSearch = h("input", { class: "input", type: "search", placeholder: "Search the store", "aria-label": "Search the store", autocomplete: "off" });
	storeSearch.addEventListener("input", debounce(() => { storeQuery = storeSearch.value; storeLimit = STORE_PAGE; paintStore(); }, 120));
	storeSearch.addEventListener("search", () => { if (!storeSearch.value) { storeQuery = ""; paintStore(); } });

	// The kind buttons are built once per load and only re-marked, so focus and arrow keys survive a filter.
	function paintKinds() {
		const all = storeInfo?.plugins?.length ?? 0;
		const options = [{ value: "all", label: "All", n: all }, ...kinds()];
		kindBar.replaceChildren(...options.map((o) => h("button", { type: "button", class: "plug-kind", dataset: { kind: o.value }, "aria-pressed": String(storeKind === o.value), onClick: () => setKind(o.value) }, o.label, h("span", { class: "plug-kind-n" }, fmtInt(o.n)))));
	}
	function setKind(value) {
		storeKind = value;
		storeLimit = STORE_PAGE;
		for (const b of kindBar.children) b.setAttribute("aria-pressed", String(b.dataset.kind === value));
		paintStore();
	}
	kindBar.addEventListener("keydown", (e) => {
		if (!["ArrowLeft", "ArrowRight"].includes(e.key)) return;
		const items = [...kindBar.children];
		const i = items.indexOf(document.activeElement);
		if (i === -1) return;
		e.preventDefault();
		items[(i + (e.key === "ArrowRight" ? 1 : -1) + items.length) % items.length].focus();
	});

	function paintStore() {
		if (storeError) { storeBody.replaceChildren(errorState(storeError, load)); return; }
		if (!storeInfo) return;
		const plugins = storeInfo.plugins ?? [];
		const needle = storeQuery.trim().toLowerCase();
		const rows = plugins.filter((plugin) => (storeKind === "all" || (plugin.tags ?? []).some((tag) => plain(tag).toLowerCase() === storeKind)) && (!needle || `${plugin.name} ${plugin.id} ${plugin.description} ${plugin.author} ${(plugin.tags ?? []).join(" ")}`.toLowerCase().includes(needle)));
		const errors = storeInfo.source_errors ?? [];
		storeList.replaceChildren(...[
			errors.length ? h("div", { class: "plug-result bad", role: "alert" }, h("div", { class: "plug-result-head" }, "Some store sources did not load"), h("pre", { class: "plug-result-text" }, errors.map((err) => `${plain(err.source_name || err.source_id)}: ${plain(err.message)}`).join("\n"))) : null,
			rows.length ? h("div", { class: "plug-grid" }, rows.slice(0, storeLimit).map(storeItem)) : empty({ icon: "search", title: plugins.length ? "No plugins match" : "The store is empty", hint: plugins.length ? "Try another word, or show all kinds." : "The configured store sources returned no plugins.", action: plugins.length && (needle || storeKind !== "all") ? btn("Show everything", { cls: "outline", onClick: () => { storeSearch.value = ""; storeQuery = ""; setKind("all"); } }) : null }),
		].filter(Boolean));
		storeFoot.replaceChildren(rows.length > storeLimit ? btn(`Show ${fmtInt(Math.min(STORE_PAGE * 2, rows.length - storeLimit))} more`, { cls: "outline", onClick: () => { storeLimit += STORE_PAGE * 2; paintStore(); } }) : null);
		summary.textContent = `${fmtInt(Math.min(storeLimit, rows.length))} of ${fmtInt(rows.length)}${rows.length !== plugins.length ? ` matching` : ""}${storeInfo.sources?.length ? ` · from ${storeInfo.sources.map((src) => plain(src.name)).join(" and ")}` : ""}`;
	}
	storeBody.replaceChildren(h("div", { class: "plug-store" },
		h("div", { class: "plug-toolbar" }, h("div", { class: "search" }, icon("search", 15), storeSearch), summary),
		kindBar,
		storeList,
		storeFoot,
		h("details", { class: "plug-how" },
			h("summary", null, icon("info", 14), "How installing works"),
			h("p", null, "The store doesn't say which engine versions or systems a plugin supports. That is settled when you press Install: the engine looks for a release file built for its own system, checks its checksum, and tells you on the card if either is missing. Installing doesn't prove a plugin works; once loaded, it shows as running under Installed.")),
	));

	// ---------- build your own ----------

	const steps = [
		["Start from an example", h("span", null, "Copy ", filePath("examples/plugin/simple/go", true), " (the full skeleton) or a single-capability one such as ", filePath("examples/plugin/executor/go", true), ". It exports ", h("code", null, "cliproxy_plugin_init"), ", answers ", h("code", null, "plugin.register"), " with its metadata, and handles the calls the engine sends. Each example exists in Go, C and Rust.")],
		["Build a shared library", h("span", null, "With cgo on: ", h("code", { class: "plug-cmd" }, "go build -buildmode=c-shared -o my-plugin.so ."), " (", h("code", null, ".dylib"), " on macOS, ", h("code", null, ".dll"), " on Windows). ", h("code", null, "make -C examples/plugin build"), " builds every example.")],
		["Drop it in the folder", () => h("span", null, "Put it in ", h("code", null, dirText()), " as ", h("code", null, "<id>.so"), " or ", h("code", null, "<id>-v<version>.so"), ", built for the engine's system. Turn on plugin loading at the top, press Reload, and switch the plugin on under Installed.")],
	];
	const buildBody = () => h("div", { class: "plug-write" },
		h("div", null,
			h("p", { class: "ink-2", style: { marginTop: 0 } }, "A plugin is a native library (", h("code", null, ".so"), ", ", h("code", null, ".dylib"), " or ", h("code", null, ".dll"), ") the engine loads into its own process. It can add a provider or sign-in, route or schedule requests, rewrite them, report usage, or serve its own management pages. No store needed."),
			h("ol", { class: "plug-steps" }, steps.map(([title, body]) => h("li", null, h("strong", null, title), h("div", { class: "ink-2" }, typeof body === "function" ? body() : body)))),
		),
		h("div", { class: "plug-src" },
			h("div", { class: "plug-src-title" }, "Where it's defined"),
			kv([
				["Call protocol", h("div", null, filePath("sdk/pluginabi/types.go"), h("div", { class: "hint" }, "ABI and schema versions, method names such as ", h("code", null, "plugin.register"), "."))],
				["Capabilities", h("div", null, filePath("sdk/pluginapi/types.go"), h("div", { class: "hint" }, "Metadata, settings, and the provider, executor, scheduler and router interfaces."))],
				["Loader", h("div", null, filePath("internal/pluginhost/loader_unix.go"), h("div", { class: "hint" }, "Opens the library and looks up ", h("code", null, "cliproxy_plugin_init"), ". Windows: ", h("code", null, "loader_windows.go"), "."))],
				["Discovery", h("div", null, filePath("internal/pluginhost/platform.go"), h("div", { class: "hint" }, "Which file names count, and the ", h("code", null, "<dir>/<os>/<cpu>/"), " lookup."))],
				["Examples", h("div", null, filePath("examples/plugin", true), h("div", { class: "hint" }, "Each capability in Go, C and Rust, with a Makefile."))],
			]),
		),
	);
	const buildHost = h("div");

	// ---------- tabs ----------

	const panels = { installed: installedBody, store: storeBody, build: buildHost };
	const tabBar = h("div", { class: "plug-tabs", role: "tablist", "aria-label": "Plugins" });
	const panel = h("div", { class: "plug-panel", role: "tabpanel", tabindex: -1 });
	const tabDefs = () => [
		{ id: "installed", label: "Installed", n: installed().length },
		{ id: "store", label: "Store", n: storeInfo?.plugins?.length ?? null },
		{ id: "build", label: "Build your own", n: null },
	];
	function paintTabs() {
		tab ??= installed().length ? "installed" : "store";
		const focused = tabBar.contains(document.activeElement);
		tabBar.replaceChildren(...tabDefs().map((t) => h("button", {
			type: "button", role: "tab", id: `plug-tab-${t.id}`, "aria-selected": String(tab === t.id), tabindex: tab === t.id ? 0 : -1, "aria-controls": "plug-panel",
			onClick: () => setTab(t.id),
		}, t.label, t.n !== null ? h("span", { class: "plug-tab-n" }, fmtInt(t.n)) : null)));
		panel.id = "plug-panel";
		panel.setAttribute("aria-labelledby", `plug-tab-${tab}`);
		if (tab === "build" && !buildHost.childElementCount) buildHost.append(buildBody());
		if (panel.firstChild !== panels[tab]) panel.replaceChildren(panels[tab]);
		if (focused) tabBar.querySelector('[aria-selected="true"]')?.focus();
	}
	function setTab(id, focusPanel = false) {
		tab = id;
		history.replaceState(null, "", `#/plugins?tab=${id}`);
		paintTabs();
		if (focusPanel) (id === "store" ? storeSearch : panel).focus({ preventScroll: true });
	}
	tabBar.addEventListener("keydown", (e) => {
		const order = tabDefs().map((t) => t.id);
		const i = order.indexOf(tab);
		let next = null;
		if (e.key === "ArrowRight") next = order[(i + 1) % order.length];
		else if (e.key === "ArrowLeft") next = order[(i - 1 + order.length) % order.length];
		else if (e.key === "Home") next = order[0];
		else if (e.key === "End") next = order.at(-1);
		if (!next) return;
		e.preventDefault();
		setTab(next);
		tabBar.querySelector('[aria-selected="true"]')?.focus();
	});

	// ---------- page ----------

	root.append(
		pageHead({ title: "Plugins", desc: "Native extensions that run inside the engine: new providers and sign-ins, routing, usage reports.", actions: [btn("Reload", { icon: "refresh", cls: "outline", onClick: load })] }),
		card({ body: statusBody, cls: "plug-status-card" }),
		h("div", { class: "plug-tabwrap" }, tabBar, panel),
	);
	const escape = (e) => { if (e.key === "Escape" && selected) closeInspector(); };
	document.addEventListener("keydown", escape);
	await load();
	return () => { disposed = true; selectionRevision++; document.removeEventListener("keydown", escape); };
}
