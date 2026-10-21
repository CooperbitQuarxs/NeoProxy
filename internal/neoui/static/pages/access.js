import { h, card, body, badge, btn, table, stat, fmtInt, empty, toast, confirm, modal, field, mask, copyLine, copyText } from "../ui.js";
import { pageHead } from "./shared.js";

function randomKey() {
	const bytes = new Uint8Array(24);
	crypto.getRandomValues(bytes);
	return `np-${Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")}`;
}

export const access = {
	id: "access",
	title: "Access keys",
	icon: "key",
	key: "k",
	async render({ load, api, invalidate, reload }) {
		const config = await load.config();
		const keys = config?.access?.["api-keys"] ?? [];
		const apiBase = `${location.protocol}//${location.host}`;

		const save = async (next, msg) => {
			try {
				await api.put("config/access/api-keys", next);
				invalidate("config");
				toast(msg, "ok");
				reload();
			} catch (ex) {
				toast(`Save failed: ${ex.message}`, "bad");
			}
		};

		const addKey = () => {
			const input = h("input", { class: "input mono", value: randomKey(), style: { width: "100%" } });
			const dlg = modal({
				title: "New client key",
				sub: "Clients send this as their API key. It is stored in plain text in the configuration.",
				body: [field("Key", input, "Generated for you; edit if you need a specific value."), btn("Regenerate", { cls: "sm outline", icon: "refresh", onClick: () => (input.value = randomKey()) })],
				actions: [
					btn("Cancel", { onClick: () => dlg.close() }),
					btn("Add key", {
						cls: "primary",
						onClick: async () => {
							const v = input.value.trim();
							if (!v) return;
							if (keys.includes(v)) return toast("That key already exists", "warn");
							dlg.close();
							await save([...keys, v], "Key added");
						},
					}),
				],
			});
		};

		const revealed = new Set();
		const tbl = table(
			[
				{ label: "#", cls: "num", width: "40px", render: (r) => String(r.i + 1) },
				{
					label: "Key",
					cls: "id",
					render: (r) => {
						const span = h("span", null, revealed.has(r.key) ? r.key : mask(r.key));
						return h(
							"span",
							{ class: "row" },
							span,
							btn("", {
								icon: "search",
								cls: "icon sm",
								title: revealed.has(r.key) ? "Hide" : "Reveal",
								onClick: () => {
									revealed.has(r.key) ? revealed.delete(r.key) : revealed.add(r.key);
									span.textContent = revealed.has(r.key) ? r.key : mask(r.key);
								},
							}),
						);
					},
				},
				{ label: "Length", cls: "num", render: (r) => String(r.key.length) },
				{
					label: "",
					cls: "actions",
					render: (r) =>
						h(
							"span",
							{ class: "row", style: { justifyContent: "flex-end" } },
							btn("", {
								icon: "copy",
								cls: "icon sm",
								title: "Copy",
								onClick: () => copyText(r.key),
							}),
							btn("", {
								icon: "trash",
								cls: "icon sm danger",
								title: "Revoke",
								onClick: async () => {
									if (!(await confirm({ title: "Revoke this key?", text: "Clients using it will get 401 immediately.", confirmLabel: "Revoke", danger: true }))) return;
									await save(
										keys.filter((k) => k !== r.key),
										"Key revoked",
									);
								},
							}),
						),
				},
			],
			keys.map((key, i) => ({ key, i })),
			{ emptyState: empty({ title: "No client keys", hint: "Without keys every request is accepted. Add one to require authentication.", action: btn("Add key", { cls: "outline sm", icon: "plus", onClick: addKey }) }) },
		);

		return [
			pageHead("Access keys", "The keys clients present to this proxy. One list, no identity; accounts will replace it.", btn("Add key", { icon: "plus", cls: "primary", onClick: addKey })),
			h(
				"div",
				{ class: "stats" },
				stat({ label: "Client keys", value: fmtInt(keys.length), sub: keys.length ? "authentication required" : "open: no key required", bar: keys.length ? "ok" : "warn" }),
				stat({ label: "Management", value: config?.management?.["allow-remote"] ? "remote allowed" : "localhost only", mono: true, sub: "management API reachability" }),
				stat({ label: "Bind", value: `${config?.server?.host || "all interfaces"}:${config?.server?.port ?? "?"}`, mono: true, sub: "server.host and server.port" }),
			),
			card({
				title: "Client keys",
				sub: "Sent as Authorization: Bearer, x-api-key, or x-goog-api-key depending on the client.",
				body: h("div", { class: "card-body flush" }, tbl),
			}),
			card({
				title: "Connect a client",
				sub: "Example with the first key.",
				body: body(
					[
						copyLine(`OPENAI_BASE_URL=${apiBase}/v1`, "env"),
						copyLine(`OPENAI_API_KEY=${keys[0] ?? "<client key>"}`, "env"),
						copyLine(`ANTHROPIC_BASE_URL=${apiBase}`, "env"),
						copyLine(`curl ${apiBase}/v1/models -H "Authorization: Bearer ${keys[0] ?? "<client key>"}"`, "curl"),
					],
					"stack",
				),
			}),
		];
	},
};
