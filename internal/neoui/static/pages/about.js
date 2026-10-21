import { h, icon } from "../lib/dom.js";
import { engine, ApiError } from "../lib/client.js";
import { pageHead, card, kv, badge, status, spinner } from "../lib/components.js";
import { pages } from "./index.js";

const repository = "https://github.com/PsychedelicShayna/NeoProxy";
const externalLink = (href, label) => h("a", { href, target: "_blank", rel: "noreferrer", class: "row" }, label, icon("external", 13));

export async function render(ctx) {
	const { root } = ctx;
	root.append(pageHead({ title: "About", desc: "What this proxy is, what it runs on, and where to look next." }));
	const versionHost = h("div", null, spinner());
	root.append(
		h("div", { class: "grid c2-1" },
			card({
				title: "NeoProxy",
				body: [
					h("p", { class: "ink-2" }, "NeoProxy is PsychedelicShayna's fork of CLIProxyAPI, built to serve the credentials a NeoPi core holds to NeoDeck, NeoCode and any OpenAI-, Anthropic- or Gemini-shaped client under one ", h("code", null, "neoproxy/"), " namespace."),
					kv([
						["Repository", externalLink(repository, "PsychedelicShayna/NeoProxy")],
						["Engine", externalLink("https://github.com/router-for-me/CLIProxyAPI", "CLIProxyAPI")],
						["Dashboard", h("span", null, h("code", null, "/neo/"), " · Neo design language")],
						["Upstream panel", h("div", null, h("a", { href: "/management.html" }, "legacy control panel"), h("div", { class: "hint" }, "The engine's original panel, kept reachable."))],
					]),
				],
			}),
			card({ title: "Version", flush: true, body: versionHost }),
		),
		h("div", { class: "grid c2" },
			card({
				title: "Keyboard",
				flush: true,
				body: kv([
					...pages.filter((page) => page.chord).map((page) => [h("span", { class: "row" }, h("kbd", null, "G"), "then", h("kbd", null, page.chord[1].toUpperCase())), page.title]),
					[h("kbd", null, "/"), "Search on the page"],
					[h("kbd", null, "."), "Reload data"],
					[h("kbd", null, "?"), "All shortcuts"],
					[h("kbd", null, "Esc"), "Close"],
				]),
			}),
			card({
				title: "Design language",
				body: h("p", { class: "ink-2" }, "The dashboard follows NeoPi's web surfaces ", h("code", null, "/stats"), ", ", h("code", null, "/collab"), ", ", h("code", null, "/share"), ": purple-black chassis, one accent, hairlines not shadows, mono for identifiers. ", h("a", { href: `${repository}/blob/nightly/docs/design/neo-design-language.md`, target: "_blank", rel: "noreferrer" }, "Read the design language", " ", icon("external", 13)), "."),
			}),
		),
	);

	if (ctx.session.role !== "admin") {
		versionHost.replaceChildren(status("", "Not available to members"));
		return;
	}

	const [runningResult, latestResult] = await Promise.allSettled([
		engine.raw("GET", "config").then(async (res) => {
			if (!res.ok) throw new ApiError(res.status, "Could not read the running version");
			const config = await res.json();
			return res.headers.get("X-CPA-Version") || config.version || config["server-version"] || config.server?.version || null;
		}),
		engine.latestVersion(),
	]);
	const denied = (result) => result.status === "rejected" && result.reason instanceof ApiError && [401, 403].includes(result.reason.status);
	if (denied(runningResult) || denied(latestResult)) {
		versionHost.replaceChildren(status("", "Not available to members"));
		return;
	}
	const running = runningResult.status === "fulfilled" ? runningResult.value : null;
	const latest = latestResult.status === "fulfilled" ? latestResult.value?.["latest-version"] || latestResult.value?.latest_version || latestResult.value?.tag_name || latestResult.value?.version : null;
	const sameVersion = running && latest && String(running).replace(/^v/, "") === String(latest).replace(/^v/, "");
	versionHost.replaceChildren(kv([
		["Running", running ? h("code", null, running) : status("", "Not reported")],
		["Latest", latestResult.status === "rejected" ? status("", "Could not check") : latest ? h("span", { class: "row wrap" }, h("code", null, latest), running ? badge(sameVersion ? "up to date" : "update available", sameVersion ? "ok" : "warn") : null) : status("", "Not reported")],
	]));
}
