import { h, card, body, badge, btn, wip, kv, status, fmtInt } from "../ui.js";
import { pageHead } from "./shared.js";

export const accounts = {
	id: "accounts",
	title: "Accounts",
	icon: "users",
	key: "a",
	wip: true,
	async render({ load, navigate }) {
		let keys = [];
		try {
			keys = (await load.config())?.access?.["api-keys"] ?? [];
		} catch {}
		return [
			pageHead("Accounts", "Who may use this proxy, and with what."),
			card({
				title: "Today",
				sub: "Access is a flat list of client keys with no identity attached.",
				body: body([
					kv([
						["Identity", status("warn", "none", "keys are anonymous")],
						["Client keys", `${fmtInt(keys.length)} configured`],
						["Management", "one shared management key"],
					]),
				]),
				foot: [h("span", null, "Keys are managed on the Access keys page."), btn("Access keys", { cls: "sm", icon: "arrowRight", onClick: () => navigate("access") })],
			}),
			wip({
				title: "Accounts and permissions",
				text: "NeoDeck, NeoProxy and NeoPi's auth broker are three logins today. This page is where they become one: a person signs in once, and what they can reach through the proxy is a set of restrictions on that identity rather than a key in a list.",
				items: [
					"Per-person client keys, issued and revoked here, with the usage they generate attributed to them.",
					"Scopes: which providers and models an account may call, and whether it may touch the management API.",
					"Shared identity with NeoDeck, so a team member on Utopia has the same account everywhere.",
					"An audit trail of who changed what in this proxy.",
				],
				links: [btn("NeoPi link", { cls: "sm outline", onClick: () => navigate("neopi") }), btn("Access keys", { cls: "sm outline", onClick: () => navigate("access") })],
			}),
		];
	},
};
