// About: what this proxy is, which build is running, where its pieces live, and
// the keyboard shortcuts in one compact sheet.

import { h, icon } from "../lib/dom.js";
import { engine, neo, ApiError } from "../lib/client.js";
import { pageHead, card, copyLine } from "../lib/components.js";
import { pages } from "./index.js";

const repository = "https://github.com/PsychedelicShayna/NeoProxy";
const ext = { target: "_blank", rel: "noreferrer" };

function linkRow({ href, ic, title, sub, external = true }) {
	return h("a", { class: "ab-link", href, ...(external ? ext : {}) },
		h("span", { class: "ab-link-icon" }, icon(ic, 16)),
		h("span", { class: "ab-link-text" }, h("span", { class: "ab-link-title" }, title), h("span", { class: "ab-link-sub" }, sub)),
		icon(external ? "external" : "arrowRight", 14),
	);
}

export async function render(ctx) {
	const { root } = ctx;
	const role = ctx.session.role;
	root.append(pageHead({ title: "About", desc: "What this proxy is, which build is running, and where to look next." }));

	const facts = h("dl", { class: "ab-facts" },
		h("div", null, h("dt", null, "Version"), h("dd", null, h("span", { class: "ab-wait" }, "Checking…"))),
		h("div", null, h("dt", null, "Commit"), h("dd", null, "—")),
		h("div", null, h("dt", null, "Latest upstream"), h("dd", null, "—")),
	);
	const hero = h("section", { class: "card ab-hero" },
		h("div", { class: "ab-hero-main" },
			h("img", { class: "ab-mark", src: "mark.svg", alt: "" }),
			h("div", { class: "ab-hero-text" },
				h("h2", null, "NeoProxy"),
				h("p", null, "One address for every model your team may use. NeoProxy holds the provider accounts, decides which one answers each request, and serves NeoPi, NeoDeck, NeoCode and any compatible app under ", h("code", null, "neoproxy/"), "."),
			),
		),
		facts,
	);

	const panelRow = linkRow({ href: "/management.html", ic: "settings", title: "Legacy control panel", sub: "The engine's original panel. It signs in with the management key, not your account." });
	const links = card({
		title: "Links",
		cls: "ab-links",
		flush: true,
		body: h("div", { class: "ab-link-list" },
			linkRow({ href: repository, ic: "fork", title: "PsychedelicShayna/NeoProxy", sub: "Source code, releases and issues." }),
			linkRow({ href: "https://github.com/router-for-me/CLIProxyAPI", ic: "cpu", title: "CLIProxyAPI", sub: "The upstream engine this fork is built from." }),
			linkRow({ href: `${repository}/blob/nightly/docs/design/neo-design-language.md`, ic: "sun", title: "Neo design language", sub: "Why the dashboard looks the way it does." }),
			panelRow,
		),
	});

	const visible = pages.filter((page) => page.chord && (!page.roles || page.roles.includes(role)));
	const key = (...keys) => h("span", { class: "ab-keys" }, keys.map((k) => h("kbd", null, k)));
	const keyboard = card({
		title: "Keyboard",
		sub: "Press G, then a letter, to jump to a page.",
		cls: "ab-kb",
		body: [
			h("div", { class: "ab-kb-grid" }, visible.map((page) => h("div", { class: "ab-kb-row" }, key(page.chord[1].toUpperCase()), h("span", null, page.title)))),
			h("div", { class: "ab-kb-more" },
				h("div", { class: "ab-kb-row" }, key("/"), h("span", null, "Search on the page")),
				h("div", { class: "ab-kb-row" }, key("."), h("span", null, "Reload the data")),
				h("div", { class: "ab-kb-row" }, key("?"), h("span", null, "All shortcuts")),
				h("div", { class: "ab-kb-row" }, key("Esc"), h("span", null, "Close or leave a field")),
			),
		],
	});

	root.append(hero, h("div", { class: "ab-grid" }, links, keyboard));

	const setFact = (i, value) => facts.children[i].querySelector("dd").replaceChildren(value);
	if (role !== "admin") {
		setFact(0, h("span", { class: "ink-3" }, "Shown to admins"));
		facts.children[1].remove();
		facts.children[1].remove();
		return;
	}

	const [coreResult, latestResult, configResult] = await Promise.allSettled([neo.core(), engine.latestVersion(), engine.config()]);
	if (configResult.status === "fulfilled" && configResult.value?.management?.["disable-control-panel"]) {
		panelRow.replaceWith(h("div", { class: "ab-link off" },
			h("span", { class: "ab-link-icon" }, icon("settings", 16)),
			h("span", { class: "ab-link-text" }, h("span", { class: "ab-link-title" }, "Legacy control panel"), h("span", { class: "ab-link-sub" }, "Turned off on this engine. ", h("a", { class: "link-text", href: "#/config" }, "Serve it again in Configuration"), ".")),
		));
	}
	const denied = (result) => result.status === "rejected" && result.reason instanceof ApiError && [401, 403].includes(result.reason.status);
	if (denied(coreResult) && denied(latestResult)) { setFact(0, h("span", { class: "ink-3" }, "Not available")); return; }
	const version = coreResult.status === "fulfilled" ? coreResult.value?.version || null : null;
	// Local and nightly builds end their version with the short commit hash (`…local.0a10dd70`).
	const commit = version && /(?:^|[.+-])g?([0-9a-f]{7,40})$/.exec(version)?.[1];
	const channel = !version ? null : /nightly/.test(version) ? "nightly" : /local|dev/.test(version) ? "local build" : "release";
	const latest = latestResult.status === "fulfilled" ? latestResult.value?.["latest-version"] || latestResult.value?.latest_version || latestResult.value?.tag_name || latestResult.value?.version : null;
	setFact(0, version ? h("span", { class: "ab-ver" }, copyLine(version, "version"), channel ? h("span", { class: "ab-pill" }, channel) : null) : h("span", { class: "ink-3" }, "Not reported"));
	setFact(1, commit
		? h("a", { class: "ab-commit", href: `${repository}/commit/${commit}`, ...ext }, h("code", null, commit.slice(0, 8)), icon("external", 12))
		: h("span", { class: "ink-3" }, version ? "Not in this version string" : "Not reported"));
	setFact(2, latestResult.status === "rejected"
		? h("span", { class: "ink-3" }, "Could not check")
		: latest ? h("span", { title: "The latest CLIProxyAPI release. NeoProxy versions on its own track, so the two are not compared." }, "CLIProxyAPI ", h("code", null, latest)) : h("span", { class: "ink-3" }, "Not reported"));
}
