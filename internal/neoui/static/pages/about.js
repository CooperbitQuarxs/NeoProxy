import { h, icon } from "../lib/dom.js";
import { engine, neo, ApiError } from "../lib/client.js";
import { pageHead, card, kv, status, spinner, copyLine } from "../lib/components.js";
import { pages } from "./index.js";

const repository = "https://github.com/PsychedelicShayna/NeoProxy";
const externalLink = (href, label) => h("a", { href, target: "_blank", rel: "noreferrer", class: "row" }, label, icon("external", 13));

export async function render(ctx) {
	const { root } = ctx;
	root.append(pageHead({ title: "About", desc: "What this proxy is, what it runs on, and where to look next." }));
	const versionHost = h("div", null, spinner());
	const panelHost = h("div", null, h("a", { href: "/management.html", class: "row" }, "Open /management.html", icon("external", 13)), h("div", { class: "hint" }, "The engine's original panel. It signs in with the engine's management key, not your dashboard account."));
	root.append(
		h("div", { class: "grid c2-1" },
			h("div", null,
				card({
					title: "NeoProxy",
					body: [
						h("p", { class: "ink-2" }, "NeoProxy is PsychedelicShayna's fork of CLIProxyAPI. This Go engine owns provider accounts, credentials and refresh, serving NeoPi, NeoDeck, NeoCode and compatible clients under one ", h("code", null, "neoproxy/"), " namespace."),
						kv([
							["Repository", externalLink(repository, "PsychedelicShayna/NeoProxy")],
							["Engine", externalLink("https://github.com/router-for-me/CLIProxyAPI", "CLIProxyAPI")],
							["Dashboard", h("span", null, h("code", null, "/neo/"), " · Neo design language")],
							["Legacy control panel", panelHost],
						]),
					],
				}),
				card({
					title: "Keyboard",
					body: kv([
						...pages.filter((page) => page.chord).map((page) => [h("span", { class: "row" }, h("kbd", null, "G"), "then", h("kbd", null, page.chord[1].toUpperCase())), page.title]),
						[h("kbd", null, "/"), "Search on the page"],
						[h("kbd", null, "."), "Reload data"],
						[h("kbd", null, "?"), "All shortcuts"],
						[h("kbd", null, "Esc"), "Close"],
					]),
				}),
			),
			h("div", null,
				card({ title: "Version", body: versionHost }),
				card({
					title: "Design language",
					body: h("p", { class: "ink-2", style: { margin: 0 } }, "The dashboard follows NeoPi's web surfaces ", h("code", null, "/stats"), ", ", h("code", null, "/collab"), ", ", h("code", null, "/share"), ": purple-black chassis, one accent, hairlines not shadows, mono for identifiers. ", h("a", { href: `${repository}/blob/nightly/docs/design/neo-design-language.md`, target: "_blank", rel: "noreferrer" }, "Read the design language", " ", icon("external", 13)), "."),
				}),
			),
		),
	);

	if (ctx.session.role !== "admin") {
		versionHost.replaceChildren(status("", "Not available to members"));
		return;
	}

	const [coreResult, latestResult, configResult] = await Promise.allSettled([neo.core(), engine.latestVersion(), engine.config()]);
	if (configResult.status === "fulfilled" && configResult.value?.management?.["disable-control-panel"]) panelHost.replaceChildren(h("span", { class: "ink-2" }, "Turned off on this engine"), h("div", { class: "hint" }, h("code", null, "management.disable-control-panel"), " is true in config.yaml, so /management.html is not served."));
	const denied = (result) => result.status === "rejected" && result.reason instanceof ApiError && [401, 403].includes(result.reason.status);
	if (denied(coreResult) && denied(latestResult)) {
		versionHost.replaceChildren(status("", "Not available to members"));
		return;
	}
	const version = coreResult.status === "fulfilled" ? coreResult.value?.version || null : null;
	// Local and nightly builds end their version with the short commit hash (`…local.0a10dd70`).
	const commit = version && /(?:^|[.+-])g?([0-9a-f]{7,40})$/.exec(version)?.[1];
	const latest = latestResult.status === "fulfilled" ? latestResult.value?.["latest-version"] || latestResult.value?.latest_version || latestResult.value?.tag_name || latestResult.value?.version : null;
	versionHost.replaceChildren(kv([
		["Version", version ? copyLine(version, "version") : status("", "Not reported")],
		["Commit", commit ? h("span", { class: "row wrap" }, copyLine(commit, "commit"), externalLink(`${repository}/commit/${commit}`, "on GitHub")) : h("span", { class: "ink-3" }, version ? "This version string has no commit hash" : "Not reported")],
		["Upstream release", latestResult.status === "rejected" ? status("", "Could not check") : latest ? h("div", null, h("code", null, latest), h("div", { class: "hint" }, "Latest CLIProxyAPI release, the engine this fork is built from. NeoProxy versions on its own track, so the two are not compared.")) : status("", "Not reported")],
	]));
}
