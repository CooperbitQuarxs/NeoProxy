import { h, card, body, badge, btn, table, stat, fmtInt, empty, toast, confirm, status, toggle } from "../ui.js";
import { pageHead } from "./shared.js";

function listOf(r) {
	if (Array.isArray(r)) return r;
	if (Array.isArray(r?.plugins)) return r.plugins;
	if (Array.isArray(r?.items)) return r.items;
	return [];
}

export const plugins = {
	id: "plugins",
	title: "Plugins",
	icon: "plug",
	key: "i",
	async render({ load, api, invalidate, reload }) {
		let installed = [];
		let store = [];
		let err = null;
		try {
			installed = listOf(await load.plugins());
		} catch (ex) {
			err = ex;
		}
		try {
			store = listOf(await load.pluginStore());
		} catch {}
		const installedIds = new Set(installed.map((p) => p.id));

		return [
			pageHead("Plugins", "Extra providers and behaviours the engine loads at runtime.", btn("Reload", { icon: "refresh", cls: "outline", onClick: () => { invalidate("plugin"); reload(); } })),
			h(
				"div",
				{ class: "stats" },
				stat({ label: "Installed", value: fmtInt(installed.length), sub: `${installed.filter((p) => p.enabled !== false).length} enabled` }),
				stat({ label: "Available", value: fmtInt(store.length), sub: "in the plugin store" }),
			),
			card({
				title: "Installed",
				body: h(
					"div",
					{ class: "card-body flush" },
					table(
						[
							{ label: "Plugin", render: (p) => h("div", { class: "cell-stack" }, h("span", null, p.name || p.id), h("span", { class: "sub" }, p.id)) },
							{ label: "Version", cls: "id", render: (p) => p.version || "-" },
							{ label: "Kind", render: (p) => (p.kind || p.type ? badge(p.kind || p.type, "outline") : "-") },
							{ label: "State", render: (p) => status(p.enabled === false ? "muted" : "ok", p.enabled === false ? "disabled" : "enabled") },
							{
								label: "",
								cls: "actions",
								render: (p) =>
									h(
										"span",
										{ class: "row", style: { justifyContent: "flex-end" } },
										toggle(p.enabled !== false, async (v) => {
											try {
												await api.patch(`/v0/management/plugins/${encodeURIComponent(p.id)}/enabled`, { enabled: v });
												invalidate("plugin");
												toast(v ? "Enabled" : "Disabled", "ok");
												reload();
											} catch (ex) {
												toast(`Could not change ${p.id}: ${ex.message}`, "bad");
											}
										}),
										btn("", {
											icon: "trash",
											cls: "icon sm danger",
											title: "Uninstall",
											onClick: async () => {
												if (!(await confirm({ title: `Uninstall ${p.name || p.id}?`, text: "Its configuration and quota state are removed.", confirmLabel: "Uninstall", danger: true }))) return;
												try {
													await api.del(`plugins/${encodeURIComponent(p.id)}`);
													invalidate("plugin");
													toast("Uninstalled", "ok");
													reload();
												} catch (ex) {
													toast(`Uninstall failed: ${ex.message}`, "bad");
												}
											},
										}),
									),
							},
						],
						installed,
						{ emptyState: empty({ icon: "plug", title: err ? "Plugins unavailable" : "No plugins installed", hint: err ? err.message : "Install one from the store below." }) },
					),
				),
			}),
			card({
				title: "Store",
				sub: "Plugins published for this engine.",
				body: h(
					"div",
					{ class: "card-body flush" },
					table(
						[
							{ label: "Plugin", render: (p) => h("div", { class: "cell-stack" }, h("span", null, p.name || p.id), h("span", { class: "sub" }, p.description || p.id)) },
							{ label: "Version", cls: "id", render: (p) => p.version || p.latest || "-" },
							{
								label: "",
								cls: "actions",
								render: (p) =>
									btn(installedIds.has(p.id) ? "Update" : "Install", {
										icon: "download",
										cls: "sm outline",
										onClick: async () => {
											try {
												await api.post(`plugins/store/${encodeURIComponent(p.id)}/install`, {});
												invalidate("plugin");
												toast(`${p.name || p.id} installed`, "ok");
												reload();
											} catch (ex) {
												toast(`Install failed: ${ex.message}`, "bad");
											}
										},
									}),
							},
						],
						store,
						{ emptyState: empty({ icon: "inbox", title: "Store unavailable", hint: "The store list could not be fetched; the proxy may be offline or the store disabled." }) },
					),
				),
			}),
		];
	},
};
