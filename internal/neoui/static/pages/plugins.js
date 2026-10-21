import { h } from "../lib/dom.js";
import { engine, invalidate } from "../lib/client.js";
import { pageHead, card, btn, iconBtn, badge, toggle, search, table, empty, inspector, section, kv, spinner, errorState, toast, confirm } from "../lib/components.js";

function listOf(result) {
	if (Array.isArray(result)) return result;
	if (Array.isArray(result?.plugins)) return result.plugins;
	if (Array.isArray(result?.items)) return result.items;
	return [];
}

export async function render(ctx) {
	const { root } = ctx;
	root.append(pageHead({ title: "Plugins", desc: "Engine extensions. Installed ones run inside this proxy; the store is what upstream publishes." }));
	let installed = [];
	let store = [];
	let installedError;
	let query = "";
	let selected = null;
	let disposed = false;
	let selectionRevision = 0;
	const installedBody = h("div", null, spinner());
	const storeBody = h("div", null, spinner());
	const close = () => { selected = null; selectionRevision++; ctx.setInspector(null); paintInstalled(); };
	const reload = async () => {
		invalidate("plugin");
		await load();
	};
	const matches = (plugin) => `${plugin.name ?? ""} ${plugin.id} ${plugin.description ?? ""}`.toLowerCase().includes(query.toLowerCase());
	const open = async (plugin) => {
		selected = plugin.id;
		const revision = ++selectionRevision;
		paintInstalled();
		const quotaBody = h("div");
		// The quota API is scoped to an auth_index, not a plugin-wide counter.
		const authIndex = ctx.query?.get("auth_index");
		const clearQuota = btn("Clear quota", { cls: "danger", onClick: async () => {
			if (!(await confirm({ title: `Clear quota for ${plugin.name || plugin.id}?`, text: "The selected credential's quota state will be cleared.", confirmLabel: "Clear quota", danger: true }))) return;
			clearQuota.disabled = true;
			try {
				await engine.del(`plugins/${encodeURIComponent(plugin.id)}/quota`, { query: { auth_index: authIndex } });
				invalidate("plugin");
				toast(`Cleared quota for ${plugin.name || plugin.id}`);
				if (!disposed && selected === plugin.id) await loadQuota();
			} catch (e) { toast(e.message, "bad"); }
			finally { clearQuota.disabled = false; }
		} });
		const loadQuota = async () => {
			quotaBody.replaceChildren(spinner());
			try {
				const quota = await engine.get(`plugins/${encodeURIComponent(plugin.id)}/quota`, { query: { auth_index: authIndex } });
				if (disposed || revision !== selectionRevision) return;
				const pairs = quota && typeof quota === "object" ? Object.entries(quota) : [["Quota", quota]];
				quotaBody.replaceChildren(kv(pairs.map(([key, value]) => [key, h("code", null, typeof value === "object" ? JSON.stringify(value) : String(value))])));
			} catch (e) { if (!disposed && revision === selectionRevision) quotaBody.replaceChildren(errorState(e, loadQuota)); }
		};
		quotaBody.append(authIndex ? spinner() : h("p", { class: "ink-2" }, "Quota is tracked per credential; pick a credential on the Credentials page"));
		ctx.setInspector(inspector({ title: plugin.name || plugin.id, sub: plugin.id, onClose: close, body: [section("Plugin", kv([["Version", h("code", null, plugin.version || "–")], ["Enabled", plugin.enabled === false ? "No" : "Yes"]])), section("Quota", quotaBody)], foot: authIndex ? [clearQuota] : [btn("Credentials", { cls: "outline", icon: "arrowRight", onClick: () => ctx.navigate("credentials") })] }));
		if (authIndex) await loadQuota();
	};
	function paintInstalled() {
		if (installedError) { installedBody.replaceChildren(errorState(installedError, reload)); return; }
		installedBody.replaceChildren(table([
			{ key: "name", label: "Name", render: (plugin) => h("div", null, h("div", { class: "cell-main" }, plugin.name || plugin.id), h("div", { class: "cell-sub mono" }, plugin.id)), sortBy: (plugin) => plugin.name || plugin.id },
			{ key: "version", label: "Version", render: (plugin) => h("code", null, plugin.version || "–") },
			{ key: "enabled", label: "Enabled", render: (plugin) => {
				let control;
				control = toggle(plugin.enabled !== false, async (enabled) => {
					control.disabled = true;
					try {
						await engine.patch(`/v0/management/plugins/${encodeURIComponent(plugin.id)}/enabled`, { enabled });
						invalidate("plugin");
						plugin.enabled = enabled;
						toast(`${enabled ? "Enabled" : "Disabled"} ${plugin.name || plugin.id}`);
						paintInstalled();
						if (selected === plugin.id) await open(plugin);
					} catch (e) { control.setAttribute("aria-checked", String(!enabled)); toast(e.message, "bad"); }
					finally { control.disabled = false; }
				}, `Enable ${plugin.name || plugin.id}`, { showLabel: false });
				return h("span", { onClick: (e) => e.stopPropagation(), onKeydown: (e) => e.stopPropagation() }, control);
			} },
			{ key: "actions", label: "", render: (plugin) => h("span", { class: "row", onClick: (e) => e.stopPropagation(), onKeydown: (e) => e.stopPropagation() }, iconBtn("trash", `Uninstall ${plugin.name || plugin.id}`, async () => {
				if (!(await confirm({ title: `Uninstall ${plugin.name || plugin.id}?`, text: "Its configuration and quota state are removed.", confirmLabel: "Uninstall", danger: true }))) return;
				try {
					await engine.del(`plugins/${encodeURIComponent(plugin.id)}`);
					invalidate("plugin");
					toast(`Uninstalled ${plugin.name || plugin.id}`);
					if (selected === plugin.id) close();
					await reload();
				} catch (e) { toast(e.message, "bad"); }
			}, "danger sm")) },
		], installed.filter(matches), { onRow: open, selectedKey: selected, emptyState: empty({ icon: "plug", title: installed.length && query ? "No matching plugins" : "No plugins installed", hint: installed.length && query ? "Try another name or clear the search." : "Install one from the store below." }) }));
	}
	function paintStore() {
		const installedIds = new Set(installed.map((plugin) => plugin.id));
		storeBody.replaceChildren(table([
			{ key: "name", label: "Name", wrap: true, render: (plugin) => h("div", null, h("div", { class: "cell-main" }, plugin.name || plugin.id), h("div", { class: "cell-sub" }, plugin.description || plugin.id)), sortBy: (plugin) => plugin.name || plugin.id },
			{ key: "version", label: "Version", render: (plugin) => h("code", null, plugin.version || plugin.latest || "–") },
			{ key: "install", label: "", render: (plugin) => {
				if (installedIds.has(plugin.id)) return badge("installed", "ok");
				const install = btn("Install", { cls: "outline", onClick: async () => {
					install.disabled = true;
					try { await engine.post(`plugins/store/${encodeURIComponent(plugin.id)}/install`, {}); invalidate("plugin"); toast(`Installed ${plugin.name || plugin.id}`); await reload(); }
					catch (e) { toast(e.message, "bad"); install.disabled = false; }
				} });
				return install;
			} },
		], store.filter(matches), { emptyState: empty({ icon: "plug", title: store.length && query ? "No matching plugins" : "Store unavailable", hint: store.length && query ? "Try another name or clear the search." : "The engine could not reach the plugin index." }) }));
	}
	async function load() {
		const [installedResult, storeResult] = await Promise.allSettled([engine.plugins(), engine.pluginStore()]);
		if (disposed) return;
		installedError = installedResult.status === "rejected" ? installedResult.reason : null;
		installed = installedResult.status === "fulfilled" ? listOf(installedResult.value) : [];
		store = storeResult.status === "fulfilled" ? listOf(storeResult.value) : [];
		paintInstalled();
		paintStore();
	}
	root.append(h("div", { class: "row", style: { marginBottom: "14px" } }, search("Filter plugins", (value) => { query = value; paintInstalled(); paintStore(); }), h("span", { class: "spacer" }), btn("Reload", { icon: "refresh", cls: "outline", onClick: reload })), card({ title: "Installed", flush: true, body: installedBody }), card({ title: "Store", flush: true, body: storeBody }));
	const escape = (e) => { if (e.key === "Escape" && selected) close(); };
	document.addEventListener("keydown", escape);
	await load();
	return () => { disposed = true; selectionRevision++; document.removeEventListener("keydown", escape); };
}
