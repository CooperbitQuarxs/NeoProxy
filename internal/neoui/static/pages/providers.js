import { h, card, body, badge, btn, table, stat, fmtInt, empty, toast, modal, field, mask, confirm, icon, kv, status } from "../ui.js";
import { pageHead, PROVIDERS, providerLabel, groupBy, statusKind } from "./shared.js";

// Providers that take API-key groups under `api-keys.<id>` in the v8 config.
const KEY_PROVIDERS = ["gemini", "vertex", "codex", "claude", "xai", "meta", "openai-compatibility"];
// Providers that are reached through OAuth logins stored as credential files.
const LOGIN_PROVIDERS = ["codex", "claude", "antigravity", "gemini-cli", "kimi", "kimi-ai", "xai", "devin", "meta", "qwen", "iflow"];

function groupKeys(group) {
	if (!group) return [];
	if (Array.isArray(group.keys)) return group.keys;
	if (group["api-key"]) return [{ "api-key": group["api-key"] }];
	return [];
}

function addKeyDialog(providerId, groups, { api, invalidate, reload }) {
	const isCompat = providerId === "openai-compatibility";
	const name = h("input", { class: "input", placeholder: isCompat ? "openrouter" : `${providerId}-${groups.length + 1}`, style: { width: "100%" } });
	const key = h("input", { class: "input mono", type: "password", placeholder: "sk-…", style: { width: "100%" } });
	const base = h("input", { class: "input mono", placeholder: isCompat ? "https://openrouter.ai/api/v1" : "leave empty for the provider default", style: { width: "100%" } });
	const models = h("input", { class: "input mono", placeholder: "model-a, model-b", style: { width: "100%" } });
	const dlg = modal({
		title: `Add ${providerLabel(providerId)} key`,
		sub: "Creates a new key group in the configuration. Advanced fields (headers, proxy, retry rules, aliases) are edited under Configuration.",
		body: [
			field("Group name", name, "Shown in logs and the user agent."),
			field("API key", key),
			field("Base URL", base, isCompat ? "Required: the endpoint that speaks the OpenAI API." : "Optional override."),
			isCompat ? field("Models", models, "Comma-separated upstream model names clients may request.") : null,
		],
		actions: [
			btn("Cancel", { onClick: () => dlg.close() }),
			btn("Add", {
				cls: "primary",
				onClick: async () => {
					const k = key.value.trim();
					const n = name.value.trim() || name.placeholder;
					if (!k) return toast("The API key is required", "warn");
					if (isCompat && !base.value.trim()) return toast("The base URL is required", "warn");
					const group = { name: n, keys: [{ "api-key": k }] };
					if (base.value.trim()) group["base-url"] = base.value.trim();
					if (isCompat) group.models = models.value.split(",").map((s) => s.trim()).filter(Boolean).map((m) => ({ name: m }));
					try {
						await api.put(`config/api-keys/${providerId}`, [...groups, group]);
						invalidate("config");
						invalidate("credentials");
						toast(`${providerLabel(providerId)} key added`, "ok");
						dlg.close();
						reload();
					} catch (ex) {
						toast(`Save failed: ${ex.message}`, "bad");
					}
				},
			}),
		],
	});
}

export const providers = {
	id: "providers",
	title: "Providers",
	icon: "layers",
	key: "p",
	async render(ctx) {
		const { load, api, invalidate, reload, navigate, route } = ctx;
		const [config, creds] = await Promise.all([load.config(), load.credentials()]);
		const apiKeys = config?.["api-keys"] ?? {};
		const entries = creds?.files ?? [];
		const byProvider = groupBy(entries, (e) => e.provider || "unknown");
		const selected = route.query.get("p") || "";

		const rows = KEY_PROVIDERS.map((id) => {
			const groups = Array.isArray(apiKeys[id]) ? apiKeys[id] : [];
			const keys = groups.flatMap(groupKeys);
			return { id, groups, keys };
		});
		const loginRows = LOGIN_PROVIDERS.map((id) => ({ id, creds: byProvider.get(id) ?? [] }));
		const configuredKeys = rows.reduce((a, r) => a + r.keys.length, 0);
		const configuredLogins = loginRows.reduce((a, r) => a + r.creds.length, 0);

		const tiles = h(
			"div",
			{ class: "tiles" },
			rows.map((r) =>
				h(
					"a",
					{ class: "tile", href: `#/providers?p=${r.id}`, style: selected === r.id ? { borderColor: "var(--accent-line)", background: "var(--raised)" } : null },
					h("div", { class: "tile-head" }, h("span", { class: `dot ${r.keys.length ? "ok" : ""}` }), providerLabel(r.id), h("span", { class: "mono" }, r.keys.length ? `${r.keys.length} key${r.keys.length === 1 ? "" : "s"}` : "none")),
					h("div", { class: "tile-sub" }, PROVIDERS[r.id]?.blurb ?? ""),
				),
			),
		);

		const sel = rows.find((r) => r.id === selected);
		const detailCard = sel
			? card({
					title: [providerLabel(sel.id), badge("api keys", "outline")],
					sub: PROVIDERS[sel.id]?.blurb,
					actions: [btn("Add key", { icon: "plus", cls: "primary sm", onClick: () => addKeyDialog(sel.id, sel.groups, ctx) }), btn("", { icon: "x", cls: "icon sm", title: "Close", onClick: () => navigate("providers") })],
					body: h(
						"div",
						{ class: "card-body flush" },
						table(
							[
								{ label: "Group", render: (g) => h("div", { class: "cell-stack" }, h("span", null, g.name || "(unnamed)"), g["base-url"] ? h("span", { class: "sub" }, g["base-url"]) : null) },
								{ label: "Keys", cls: "id", render: (g) => groupKeys(g).map((k) => h("div", null, mask(k["api-key"]))) },
								{ label: "Models", cls: "num", render: (g) => (Array.isArray(g.models) ? String(g.models.length) : "-") },
								{ label: "Priority", cls: "num", render: (g) => String(g.priority ?? 0) },
								{ label: "State", render: (g) => status(g.disabled ? "muted" : "ok", g.disabled ? "disabled" : "enabled") },
								{
									label: "",
									cls: "actions",
									render: (g, i) =>
										btn("Remove", {
											icon: "trash",
											cls: "sm danger",
											onClick: async () => {
												if (!(await confirm({ title: `Remove group ${g.name || ""}?`, text: "All keys in this group stop being used immediately.", confirmLabel: "Remove", danger: true }))) return;
												try {
													await api.put(`config/api-keys/${sel.id}`, sel.groups.filter((x) => x !== g));
													invalidate("config");
													invalidate("credentials");
													toast("Group removed", "ok");
													reload();
												} catch (ex) {
													toast(`Remove failed: ${ex.message}`, "bad");
												}
											},
										}),
								},
							],
							sel.groups,
							{ emptyState: empty({ title: `No ${providerLabel(sel.id)} keys`, hint: "Add one, or sign in with an account under Credentials.", action: btn("Add key", { cls: "outline sm", icon: "plus", onClick: () => addKeyDialog(sel.id, sel.groups, ctx) }) }) },
						),
					),
					foot: [h("span", null, "Headers, proxies, retry rules and model aliases for this provider are edited in the configuration."), btn("Open configuration", { cls: "sm", icon: "arrowRight", href: "#/configuration" })],
				})
			: null;

		return [
			pageHead("Also supported", "The engine's own upstreams. Kept in full; NeoPi is the primary source and these are the extra routes.", btn("NeoPi", { icon: "link", cls: "outline accent-text", onClick: () => navigate("neopi") })),
			h(
				"div",
				{ class: "stats" },
				stat({ label: "Key providers", value: fmtInt(rows.filter((r) => r.keys.length).length), sub: `${configuredKeys} keys in ${rows.reduce((a, r) => a + r.groups.length, 0)} groups` }),
				stat({ label: "Login providers", value: fmtInt(loginRows.filter((r) => r.creds.length).length), sub: `${configuredLogins} credentials` }),
				stat({ label: "Other", value: fmtInt([...byProvider.keys()].filter((k) => !KEY_PROVIDERS.includes(k) && !LOGIN_PROVIDERS.includes(k)).length), sub: "plugin or unknown providers" }),
			),
			card({ title: "API keys", sub: "Click a provider to see its key groups.", body: body(tiles) }),
			detailCard,
			card({
				title: "Logins",
				sub: "Account-based providers appear here once you sign in under Credentials.",
				actions: btn("Sign in", { icon: "plus", cls: "sm outline", onClick: () => navigate("credentials?add=1") }),
				body: h(
					"div",
					{ class: "card-body flush" },
					table(
						[
							{ label: "Provider", render: (r) => h("div", { class: "cell-stack" }, h("span", null, providerLabel(r.id)), h("span", { class: "sub" }, PROVIDERS[r.id]?.blurb ?? "")) },
							{ label: "Credentials", cls: "num", render: (r) => fmtInt(r.creds.length) },
							{ label: "Healthy", cls: "num", render: (r) => fmtInt(r.creds.filter((e) => statusKind(e) === "ok").length) },
							{ label: "Requests", cls: "num", render: (r) => fmtInt(r.creds.reduce((a, e) => a + (e.success ?? 0) + (e.failed ?? 0), 0)) },
							{ label: "", cls: "actions", render: (r) => (r.creds.length ? btn("View", { icon: "arrowRight", cls: "sm", onClick: () => navigate(`credentials?provider=${r.id}`) }) : null) },
						],
						loginRows,
					),
				),
			}),
		];
	},
};
