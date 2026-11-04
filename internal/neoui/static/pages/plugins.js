// Plugins: what this engine can actually do with them, what is installed, what
// the store offers, and how to write one. Every claim on this page is read from
// the engine (`plugins_enabled`, `plugins_dir`, the store response) or from the
// source files it cites.

import { h, icon, fmtInt, plural } from "../lib/dom.js";
import { engine, invalidate, ApiError } from "../lib/client.js";
import { pageHead, card, btn, iconBtn, badge, dot, toggle, search, select, empty, inspector, section, kv, copyLine, spinner, errorState, toast, confirm } from "../lib/components.js";

const REPO = "https://github.com/PsychedelicShayna/NeoProxy";
const source = (path, dir = false) => `${REPO}/${dir ? "tree" : "blob"}/HEAD/${path}`;
const STORE_PAGE = 12;

// The engine HTML-escapes strings it returns; we set text, so undo that.
const ENTITIES = { "&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": '"', "&#39;": "'", "&#34;": '"' };
const plain = (value) => String(value ?? "").replace(/&(?:amp|lt|gt|quot|#39|#34);/g, (entity) => ENTITIES[entity]);
const nameOf = (plugin) => plain(plugin.metadata?.name || plugin.name || plugin.id);
const repoUrl = (repo) => (!repo ? null : /^https?:/.test(repo) ? repo : `https://github.com/${repo}`);
const debounce = (fn, ms = 140) => { let timer; return (...args) => { clearTimeout(timer); timer = setTimeout(() => fn(...args), ms); }; };
const link = (href, text) => h("a", { href, target: "_blank", rel: "noreferrer", class: "plug-link" }, text, icon("external", 12));
const filePath = (path, dir) => link(source(path, dir), h("code", null, path));

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
	let storeTag = "all";
	let storeLimit = STORE_PAGE;
	let selected = null;
	let disposed = false;
	let selectionRevision = 0;
	// Per-store-plugin outcome of the last install attempt, kept across repaints.
	const outcomes = new Map();
	const installing = new Set();

	const statusBody = h("div", null, spinner());
	const installedBody = h("div", null, spinner());
	const storeBody = h("div", null, spinner());
	const writeAnchor = h("div");

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
		paintStore();
		if (selected && !installed().some((plugin) => plugin.id === selected)) closeInspector();
	}

	// ---------- is it on, and what is a plugin ----------

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
			} catch (e) {
				control.setAttribute("aria-checked", String(!value));
				toast(failure(e).message, "bad");
			} finally { control.disabled = false; }
		}, "Load plugins");
		statusBody.replaceChildren(
			h("div", { class: "plug-state" },
				h("div", { class: "plug-state-text" },
					h("div", { class: "plug-state-head" }, dot(on ? "ok" : ""), h("strong", null, on ? "Plugins are on for this engine" : "Plugins are off for this engine")),
					h("p", { class: "ink-2", style: { margin: "4px 0 0" } }, on
						? `Enabled plugins in ${dirText()} are loaded when the engine starts or its configuration reloads.`
						: "Loading is controlled by plugins.enabled in config.yaml, which is off. You can install plugins now; none of them runs until this is switched on."),
				),
				control,
			),
			h("p", { class: "ink-2 plug-lede" },
				"A plugin is a native shared library: ", h("code", null, ".so"), " on Linux, ", h("code", null, ".dylib"), " on macOS, ", h("code", null, ".dll"), " on Windows. The engine opens it from the plugins directory and runs it inside its own process, so a plugin sees every request and response. It can add a provider or sign-in flow, route or schedule requests, rewrite them, report usage, or serve its own management routes. NeoProxy runs fine with none."),
			kv([
				["Directory", h("div", null, copyLine(dirText(), "plugins directory"), h("div", { class: "hint" }, "Set by ", h("code", null, "plugins.dir"), ". A relative path is relative to the engine's working directory; ", h("code", null, "<dir>/<os>/<cpu>/"), " is searched before ", h("code", null, "<dir>"), "."))],
				["Found there", plural(installed().length, "plugin")],
				["Requirement", "Only a build of the engine made with cgo can load plugins. NeoProxy release builds are made with cgo."],
			]),
			h("div", { class: "row wrap plug-jumps" },
				btn("Browse the store", { cls: "outline sm", icon: "arrowRight", onClick: () => storeCard.scrollIntoView({ behavior: "smooth", block: "start" }) }),
				btn("Write your own", { cls: "outline sm", icon: "arrowRight", onClick: () => writeAnchor.scrollIntoView({ behavior: "smooth", block: "start" }) }),
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
				quotaBody.replaceChildren(kv(pairs.map(([key, value]) => [key, h("code", null, typeof value === "object" ? JSON.stringify(value) : String(value))])));
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
				["State", h("div", null, h("span", { class: "status" }, dot(state.kind), state.text), h("div", { class: "hint" }, state.hint))],
				["Version", h("code", null, plugin.metadata?.version || "not reported")],
				plugin.metadata?.author ? ["Author", plain(plugin.metadata.author)] : null,
				repo ? ["Source", link(repo, plain(plugin.metadata.github_repository))] : null,
				["File", plugin.path ? h("code", null, plain(plugin.path)) : "not found in the plugins directory"],
				["Adds", [plugin.supports_oauth ? `sign-in for ${plain(plugin.oauth_provider || "a provider")}` : null, plugin.supports_quota ? "quota reporting" : null, plugin.menus?.length ? `${plural(plugin.menus.length, "management page")}` : null].filter(Boolean).join(", ") || (plugin.registered ? "request handling" : "not known until it loads")],
			])),
		];
		if (fields.length) body.push(section("Settings it reads", h("ul", { class: "plug-fields" }, fields.map((field) => h("li", null, h("code", null, field.name), field.type ? h("span", { class: "ink-3" }, ` ${field.type}`) : null, field.description ? h("div", { class: "ink-3" }, plain(field.description)) : null))), h("p", { class: "hint" }, "Set them under ", h("code", null, `plugins.configs.${plugin.id}`), " in ", h("a", { href: "#/config" }, "Configuration"), ".")));
		if (plugin.supports_quota) {
			quotaBody.append(authIndex ? spinner() : h("p", { class: "ink-2" }, "Quota is tracked per credential; pick a credential on the Poolls page."));
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
			: empty({ icon: "plug", title: "No plugins installed", hint: `Nothing in ${dirText()} yet. Install one from the store, or drop in a library you built.` }));
	}

	// ---------- store ----------

	const tagOptions = () => {
		const counts = new Map();
		for (const plugin of storeInfo?.plugins ?? []) for (const tag of plugin.tags ?? []) {
			const key = plain(tag).toLowerCase();
			const entry = counts.get(key) ?? { label: plain(tag), n: 0 };
			entry.n++;
			counts.set(key, entry);
		}
		return [...counts.entries()].filter(([, entry]) => entry.n >= 3).sort((a, b) => b[1].n - a[1].n || a[0].localeCompare(b[0])).map(([key, entry]) => ({ value: key, label: `${entry.label[0].toUpperCase()}${entry.label.slice(1)} (${entry.n})` }));
	};

	async function install(plugin, update) {
		const name = plain(plugin.name || plugin.id);
		const ok = await confirm({
			title: `${update ? "Update" : "Install"} ${name}?`,
			text: `The engine downloads ${plugin.version ? `version ${plain(plugin.version)} ` : "it "}from ${plain(plugin.repository || plugin.source_name || "the store")}, checks its checksum, and writes the library into ${dirText()}. It also switches the plugin on in config.yaml; it runs only while plugin loading is on. A plugin runs inside the engine with access to every request, so install code you trust.`,
			confirmLabel: update ? "Update plugin" : "Install plugin",
		});
		if (!ok) return;
		installing.add(plugin.store_id ?? plugin.id);
		outcomes.delete(plugin.id);
		paintStore();
		try {
			const result = await engine.post(`plugins/store/${encodeURIComponent(plugin.id)}/install`, {}, { query: { source: plugin.source_id } });
			outcomes.set(plugin.id, { kind: "ok", text: `Installed ${plain(result?.version || plugin.version || "")}`.trim() + (result?.path ? ` at ${plain(result.path)}.` : "."), more: result?.plugins_enabled === false ? "Plugin loading is off, so it will not run until you turn it on above." : result?.restart_required ? "Restart the engine to finish." : null });
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
		else if (has) action = badge(`installed${plugin.installed_version ? ` ${plain(plugin.installed_version)}` : ""}`, "ok");
		else action = btn("Install", { cls: "outline sm", icon: "download", onClick: () => install(plugin, false) });
		return h("article", { class: "plug-item" },
			h("div", { class: "row plug-item-head" }, mark(name), h("div", { class: "lt" }, h("div", { class: "main" }, name), h("div", { class: "sub" }, plugin.author ? `by ${plain(plugin.author)}` : plugin.id))),
			h("p", { class: "plug-desc" }, plain(plugin.description) || "No description."),
			h("div", { class: "row wrap plug-meta" },
				plugin.version ? badge(`v${plain(plugin.version).replace(/^v/, "")}`) : null,
				plugin.license ? badge(plain(plugin.license), "outline") : null,
				badge(plain(plugin.install_type || "unknown install"), "outline"),
				(plugin.tags ?? []).slice(0, 2).map((tag) => badge(plain(tag), "violet")),
				plugin.platforms?.length ? badge(plugin.platforms.map((p) => `${p.goos}/${p.goarch}`).join(", "), "outline") : null,
			),
			needsAuth ? h("p", { class: "plug-note warn" }, "This download needs credentials. Add a matching rule under ", h("code", null, "plugins.store-auth"), " in Configuration first, or the install fails.") : null,
			h("div", { class: "row plug-foot" }, action, h("span", { class: "spacer" }), homepage ? link(homepage, "Source") : null),
			outcome ? h("div", { class: `plug-result ${outcome.kind}`, role: outcome.kind === "bad" ? "alert" : "status" },
				h("div", { class: "plug-result-head" }, outcome.kind === "bad" ? "Install failed" : "Done", outcome.code ? h("code", null, outcome.code) : null, iconBtn("x", "Dismiss", () => { outcomes.delete(plugin.id); paintStore(); }, "sm")),
				h("pre", { class: "plug-result-text" }, outcome.text),
				outcome.more ? h("div", { class: "plug-result-more" }, outcome.more) : null) : null,
		);
	}

	const storeToolbar = h("div", { class: "row wrap plug-toolbar" });
	const storeList = h("div");
	const storeFoot = h("div", { class: "plug-more" });
	const tagSelect = h("div");
	function paintStore() {
		if (storeError) { storeBody.replaceChildren(errorState(storeError, load)); return; }
		if (!storeInfo) return;
		const plugins = storeInfo.plugins ?? [];
		const needle = storeQuery.trim().toLowerCase();
		const rows = plugins.filter((plugin) => (storeTag === "all" || (plugin.tags ?? []).some((tag) => plain(tag).toLowerCase() === storeTag)) && (!needle || `${plugin.name} ${plugin.id} ${plugin.description} ${plugin.author}`.toLowerCase().includes(needle)));
		tagSelect.replaceChildren(select([{ value: "all", label: "All kinds" }, ...tagOptions()], storeTag, (value) => { storeTag = value; storeLimit = STORE_PAGE; paintStore(); }, { "aria-label": "Filter by kind" }));
		const errors = storeInfo.source_errors ?? [];
		storeList.replaceChildren(...[
			errors.length ? h("div", { class: "plug-result bad", role: "alert" }, h("div", { class: "plug-result-head" }, "Some store sources did not load"), h("pre", { class: "plug-result-text" }, errors.map((err) => `${plain(err.source_name || err.source_id)}: ${plain(err.message)}`).join("\n"))) : null,
			rows.length ? h("div", { class: "plug-grid" }, rows.slice(0, storeLimit).map(storeItem)) : empty({ icon: "search", title: plugins.length ? "No plugins match" : "The store is empty", hint: plugins.length ? "Try another word, or show all kinds." : "The configured store sources returned no plugins." }),
		].filter(Boolean));
		storeFoot.replaceChildren(rows.length > storeLimit ? btn(`Show ${fmtInt(Math.min(STORE_PAGE * 2, rows.length - storeLimit))} more of ${fmtInt(rows.length - storeLimit)}`, { cls: "outline", onClick: () => { storeLimit += STORE_PAGE * 2; paintStore(); } }) : null);
		summary.textContent = `${fmtInt(rows.length)} of ${fmtInt(plugins.length)} plugins${storeInfo.sources?.length ? ` from ${storeInfo.sources.map((src) => plain(src.name)).join(" and ")}` : ""}`;
	}
	const summary = h("span", { class: "ink-3", style: { fontSize: "12.5px" }, role: "status" });
	storeToolbar.append(search("Search the store", debounce((value) => { storeQuery = value; storeLimit = STORE_PAGE; paintStore(); })), tagSelect, h("span", { class: "spacer" }), summary);
	storeBody.replaceChildren(h("div", { class: "plug-store-wrap" },
		h("div", { class: "plug-honest" },
			icon("info", 15),
			h("p", null, "The store does not publish which engine versions or OS and CPU a plugin supports. That is settled when you press Install: the engine looks for a release file built for its own OS and CPU and checks its checksum. If either is missing, the reason appears on that plugin's card. Installing does not prove a plugin works; once loaded, it shows as running under Installed.")),
		storeToolbar, storeList, storeFoot));
	const storeCard = card({ title: "Store", sub: "Community plugins published as GitHub releases.", body: storeBody, flush: true });

	// ---------- write your own ----------

	const steps = [
		["Start from an example", h("span", null, "Copy ", filePath("examples/plugin/simple/go", true), " (the full skeleton) or a single-capability one such as ", filePath("examples/plugin/executor/go", true), ". It exports ", h("code", null, "cliproxy_plugin_init"), ", answers ", h("code", null, "plugin.register"), " with its metadata and the capabilities it implements, and handles the calls the engine sends. There are Go, C and Rust versions of each.")],
		["Build a shared library", h("span", null, "With cgo on: ", h("code", { class: "plug-cmd" }, "go build -buildmode=c-shared -o my-plugin.so ."), " (", h("code", null, ".dylib"), " on macOS, ", h("code", null, ".dll"), " on Windows). ", h("code", null, "make -C examples/plugin build"), " does this for every example.")],
		["Drop it in the directory", h("span", null, "Put it in ", h("code", null, dirText()), " as ", h("code", null, "<id>.so"), " or ", h("code", null, "<id>-v<version>.so"), " for the same OS and CPU as the engine. The id may use letters, digits, dot, dash and underscore. Switch on plugin loading above, press Reload, and switch the plugin on in Installed (", h("code", null, "plugins.configs.<id>.enabled"), ")."),],
	];
	const writeBody = h("div", { class: "plug-write" },
		h("div", null,
			h("p", { class: "ink-2", style: { marginTop: 0 } }, "Nothing here needs the store: any library that exports the entry point and speaks the JSON call protocol loads from the directory."),
			h("ol", { class: "plug-steps" }, steps.map(([title, body]) => h("li", null, h("strong", null, title), h("div", { class: "ink-2" }, body)))),
		),
		h("div", null,
			h("div", { class: "plug-src-title" }, "Where it is defined in this repo"),
			kv([
				["Call protocol", h("div", null, filePath("sdk/pluginabi/types.go"), h("div", { class: "hint" }, "ABI and schema versions, method names such as ", h("code", null, "plugin.register"), "."))],
				["Capability types", h("div", null, filePath("sdk/pluginapi/types.go"), h("div", { class: "hint" }, "Metadata, config fields, and the provider, executor, scheduler and router interfaces."))],
				["Loader", h("div", null, filePath("internal/pluginhost/loader_unix.go"), h("div", { class: "hint" }, "Opens the library with dlopen and looks up ", h("code", null, "cliproxy_plugin_init"), ". Windows: ", h("code", null, "loader_windows.go"), "."))],
				["Discovery", h("div", null, filePath("internal/pluginhost/platform.go"), h("div", { class: "hint" }, "Which file names count, and the ", h("code", null, "<dir>/<os>/<cpu>/"), " lookup."))],
				["Examples", h("div", null, filePath("examples/plugin", true), h("div", { class: "hint" }, "Each capability in Go, C and Rust, with a Makefile."))],
			]),
		),
	);
	writeAnchor.append(card({ title: "Write your own", sub: "A plugin is code you build, not something only the store can provide.", body: writeBody }));

	// ---------- page ----------

	root.append(
		pageHead({ title: "Plugins", desc: "Extensions that run inside this engine. This page shows whether plugins are on here, what is installed, what the store offers, and how to build your own.", actions: [btn("Reload", { icon: "refresh", cls: "outline", onClick: load })] }),
		card({ title: "Plugins on this engine", body: statusBody }),
		card({ title: "Installed", sub: "Switch a plugin on or off, or remove it. Click one for its details.", body: installedBody, flush: true }),
		storeCard,
		writeAnchor,
	);
	const escape = (e) => { if (e.key === "Escape" && selected) closeInspector(); };
	document.addEventListener("keydown", escape);
	await load();
	return () => { disposed = true; selectionRevision++; document.removeEventListener("keydown", escape); };
}
