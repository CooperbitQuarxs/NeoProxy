import { h, card, body, badge, btn, kv, status, fmtAgo } from "../ui.js";
import { pageHead, legacyLink } from "./shared.js";

export const about = {
	id: "about",
	title: "About",
	icon: "info",
	key: "b",
	async render({ load }) {
		let latest = null;
		let latestErr = null;
		try {
			latest = await load.latestVersion();
		} catch (ex) {
			latestErr = ex;
		}
		const version = latest?.latest_version || latest?.tag_name || latest?.version;
		return [
			pageHead("About", "What NeoProxy is and where its pieces come from."),
			h(
				"div",
				{ class: "grid cols-2-even" },
				card({
					cls: "centre",
					title: ["NeoProxy", badge("Neo ecosystem", "accent")],
					body: body([
						h("p", { style: { margin: "0 0 10px", color: "var(--ink-2)" } }, "A fork of CLIProxyAPI, kept as the engine, with a new unified dashboard designed in the same language as NeoPi's web surfaces."),
						kv([
							["NeoPi", "the core: credentials, inference, auth for everything"],
							["NeoProxy", "serves that inference as standard API endpoints"],
							["NeoDeck", "the team deck; uses NeoProxy as its provider"],
						]),
					]),
					foot: [h("span", null, "Design notes live in docs/design/neo-design-language.md."), btn("Repository", { cls: "sm", icon: "external", href: "https://github.com/PsychedelicShayna/NeoProxy" })],
				}),
				card({
					title: "Engine",
					sub: "CLIProxyAPI by Luis Pater and Router-For.ME, MIT licensed.",
					body: body(
						kv([
							["Upstream", h("a", { href: "https://github.com/router-for-me/CLIProxyAPI", target: "_blank", rel: "noreferrer" }, "router-for-me/CLIProxyAPI")],
							["Latest upstream release", latestErr ? status("warn", "unavailable", latestErr.message.slice(0, 40)) : version ? h("span", { class: "mono" }, version) : "-"],
							latest?.published_at ? ["Published", fmtAgo(latest.published_at)] : null,
							["Management API", h("span", { class: "mono" }, "/v8/management")],
							["Legacy console", h("a", { href: "/management.html" }, "/management.html")],
						]),
					),
					foot: [h("span", null, "The upstream control panel still ships; everything this dashboard does not cover is reachable there."), legacyLink("Open")],
				}),
			),
		];
	},
};
