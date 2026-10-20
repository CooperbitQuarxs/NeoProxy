import { h, card, body, badge, btn, status, wip, kv } from "../ui.js";
import { pageHead } from "./shared.js";

export const neopi = {
	id: "neopi",
	title: "NeoPi link",
	icon: "link",
	key: "n",
	wip: true,
	async render({ navigate }) {
		return [
			pageHead("NeoPi link", "The connection between this proxy and the NeoPi core that owns the credentials."),
			card({
				cls: "centre",
				title: ["Link status", badge("not linked", "warn")],
				sub: "NeoProxy is running on its own credential files. No NeoPi core is configured.",
				body: body([
					kv([
						["State", status("warn", "Not linked")],
						["Core endpoint", h("span", { class: "mono", style: { color: "var(--ink-3)" } }, "none configured")],
						["Auth broker", h("span", { class: "mono", style: { color: "var(--ink-3)" } }, "—")],
						["Credential source", "local auth directory (see Credentials)"],
					]),
				]),
				foot: [h("span", null, "Until the link exists, every credential below is managed here, not in NeoPi."), btn("Credentials", { cls: "sm", icon: "arrowRight", onClick: () => navigate("credentials") })],
			}),
			wip({
				title: "Linking to a NeoPi core",
				text: "This page will configure and show the live link to a NeoPi core daemon: the socket it listens on, the identity this proxy authenticates with, and which of NeoPi's providers are exposed through the proxy.",
				items: [
					"Choose a core: local socket, or a remote core over TLS.",
					"Authenticate the proxy against NeoPi's auth broker and show the granted scope.",
					"Pick which NeoPi providers and models are served, and under which names.",
					"Show link health: last heartbeat, token refreshes handled by the core, failover state.",
				],
				links: [btn("Overview", { cls: "sm outline", onClick: () => navigate("overview") }), btn("Usage & quotas", { cls: "sm outline", onClick: () => navigate("usage") })],
			}),
		];
	},
};
