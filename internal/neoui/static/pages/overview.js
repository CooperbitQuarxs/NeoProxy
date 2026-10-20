import { h, stat, card, body, badge, btn, status, fmtInt, fmtPct, fmtAgo, sparkline, empty, copyLine, icon } from "../ui.js";
import { pageHead, providerLabel, statusKind, statusText, sumBuckets, groupBy, PROVIDERS } from "./shared.js";

async function safe(p) {
	try {
		return await p;
	} catch (e) {
		return { __error: e };
	}
}

export const overview = {
	id: "overview",
	title: "Overview",
	icon: "overview",
	key: "o",
	async render({ load, navigate }) {
		const [config, creds, version] = await Promise.all([safe(load.config()), safe(load.credentials()), safe(load.latestVersion())]);
		const entries = creds?.files ?? [];
		const totals = entries.reduce((a, e) => ({ ok: a.ok + (e.success ?? 0), failed: a.failed + (e.failed ?? 0) }), { ok: 0, failed: 0 });
		const requests = totals.ok + totals.failed;
		const buckets = sumBuckets(entries);
		const healthy = entries.filter((e) => statusKind(e) === "ok").length;
		const disabled = entries.filter((e) => e.disabled).length;
		const cooling = entries.filter((e) => statusKind(e) === "warn").length;
		const broken = entries.filter((e) => statusKind(e) === "bad").length;
		const clientKeys = config?.access?.["api-keys"] ?? [];
		const byProvider = groupBy(entries, (e) => e.provider || "unknown");
		const providersActive = [...byProvider.keys()].filter((k) => k !== "unknown");
		const strategy = config?.routing?.strategy ?? "round-robin";
		const apiBase = `${location.protocol}//${location.host}`;

		const neopiCard = card({
			cls: "centre",
			title: ["NeoPi", badge("centrepiece", "accent")],
			sub: PROVIDERS.neopi.blurb,
			actions: btn("Link status", { icon: "arrowRight", cls: "sm", onClick: () => navigate("neopi") }),
			body: body([
				h(
					"div",
					{ class: "row", style: { gap: "14px", marginBottom: "12px" } },
					status("warn", "Not linked", "no NeoPi core reachable"),
				),
				h(
					"p",
					{ style: { margin: "0 0 10px", color: "var(--ink-2)" } },
					"When linked, NeoProxy serves the credentials NeoPi holds and mirrors its usage and quota windows here. Until then this proxy runs on its own credential files below.",
				),
				h(
					"div",
					{ class: "row wrap" },
					btn("Usage & quotas", { icon: "gauge", cls: "sm outline", onClick: () => navigate("usage") }),
					btn("Accounts", { icon: "users", cls: "sm outline", onClick: () => navigate("accounts") }),
				),
			]),
		});

		const endpointsCard = card({
			title: "Endpoints",
			sub: "Point any OpenAI-, Anthropic- or Gemini-shaped client here.",
			body: body(
				[
					copyLine(`${apiBase}/v1`, "OpenAI"),
					copyLine(`${apiBase}/v1/messages`, "Claude"),
					copyLine(`${apiBase}/v1beta`, "Gemini"),
					copyLine(`${apiBase}/v1/responses`, "Responses"),
				],
				"stack",
			),
			foot: [h("span", null, clientKeys.length ? `${clientKeys.length} client key${clientKeys.length === 1 ? "" : "s"} accepted` : "No client keys: requests are unauthenticated"), btn("Access keys", { cls: "sm", icon: "arrowRight", onClick: () => navigate("access") })],
		});

		const providerRows = [...byProvider.entries()]
			.map(([id, list]) => ({ id, list, ok: list.reduce((a, e) => a + (e.success ?? 0), 0), failed: list.reduce((a, e) => a + (e.failed ?? 0), 0) }))
			.sort((a, b) => b.ok + b.failed - (a.ok + a.failed));
		const max = Math.max(1, ...providerRows.map((r) => r.ok + r.failed));

		const providersCard = card({
			title: "Also supported",
			sub: "The engine's own upstreams, by traffic through this proxy.",
			actions: btn("All providers", { icon: "arrowRight", cls: "sm", onClick: () => navigate("providers") }),
			body: providerRows.length
				? h(
						"div",
						{ class: "list" },
						providerRows.slice(0, 8).map((r, i) =>
							h(
								"div",
								{ class: "list-row clickable", onClick: () => navigate(`credentials?provider=${encodeURIComponent(r.id)}`) },
								h("span", { class: "rank" }, i + 1),
								h("div", { class: "grow" }, h("div", { class: "title" }, providerLabel(r.id)), h("div", { class: "sub" }, `${r.list.length} credential${r.list.length === 1 ? "" : "s"}`)),
								h("span", { class: "num mono", style: { color: "var(--ink-2)", fontSize: "12.5px" } }, fmtInt(r.ok + r.failed)),
								h("span", { class: "bar-track" }, h("span", { class: "bar-fill", style: { width: `${(100 * (r.ok + r.failed)) / max}%` } })),
								r.failed ? badge(fmtPct(r.failed, r.ok + r.failed), r.failed / Math.max(1, r.ok + r.failed) > 0.05 ? "bad" : "ok") : badge("0.0%", "ok"),
							),
						),
					)
				: empty({ title: "No credentials yet", hint: "Add a login or an API key under Credentials and traffic will show up here.", action: btn("Add credential", { cls: "outline sm", icon: "plus", onClick: () => navigate("credentials?add=1") }) }),
		});

		const recent = entries
			.filter((e) => e.last_refresh || e.updated_at || e.modtime)
			.sort((a, b) => Date.parse(b.updated_at || b.last_refresh || b.modtime) - Date.parse(a.updated_at || a.last_refresh || a.modtime))
			.slice(0, 6);
		const activityCard = card({
			title: "Credential activity",
			sub: "Latest refreshes and state changes.",
			actions: btn("All credentials", { icon: "arrowRight", cls: "sm", onClick: () => navigate("credentials") }),
			body: recent.length
				? h(
						"div",
						{ class: "list" },
						recent.map((e) =>
							h(
								"div",
								{ class: "list-row clickable", onClick: () => navigate(`credentials?select=${encodeURIComponent(e.name || e.id)}`) },
								status(statusKind(e), ""),
								h("div", { class: "grow" }, h("div", { class: "title" }, e.label || e.email || e.account || e.name), h("div", { class: "sub" }, `${providerLabel(e.provider)} · ${e.type || e.account_type || ""}`)),
								h("span", { class: "dim", style: { color: "var(--ink-3)", fontSize: "12px", whiteSpace: "nowrap" } }, fmtAgo(e.updated_at || e.last_refresh || e.modtime)),
							),
						),
					)
				: empty({ title: "Nothing yet", hint: "Credential refreshes and status changes land here." }),
		});

		const versionLine = version?.__error ? "version check unavailable" : version?.latest_version || version?.tag_name ? `latest upstream ${version.latest_version ?? version.tag_name}` : "";

		return [
			pageHead("Overview", "What this proxy is serving right now, with NeoPi as the core it is built around."),
			h(
				"div",
				{ class: "stats" },
				stat({ label: "Requests", value: fmtInt(requests), sub: `${fmtInt(totals.failed)} failed`, bar: true }),
				stat({ label: "Error rate", value: fmtPct(totals.failed, requests), sub: `${fmtInt(totals.ok)} succeeded`, bar: totals.failed / Math.max(1, requests) > 0.05 ? "bad" : "ok" }),
				stat({ label: "Credentials", value: fmtInt(entries.length), sub: `${healthy} healthy · ${cooling + broken} unhealthy · ${disabled} off` }),
				stat({ label: "Providers", value: fmtInt(providersActive.length), sub: providersActive.length ? providersActive.map(providerLabel).slice(0, 3).join(", ") + (providersActive.length > 3 ? "…" : "") : "none configured" }),
				stat({ label: "Routing", value: strategy, mono: true, sub: `${config?.routing?.retry?.["request-retry"] ?? 0} retries · ${config?.routing?.retry?.["max-retry-credentials"] ?? "all"} credentials` }),
			),
			buckets.length
				? card({
						title: "Recent traffic",
						sub: "Requests per minute across every credential, most recent on the right.",
						body: body([sparkline(buckets.map((b) => b.success + b.failed)), h("div", { class: "row between", style: { marginTop: "6px", fontSize: "12px", color: "var(--ink-3)", fontFamily: "var(--font-mono)" } }, h("span", null, buckets[0]?.time), h("span", null, buckets[buckets.length - 1]?.time))]),
					})
				: null,
			h("div", { class: "grid cols-2" }, neopiCard, endpointsCard),
			h("div", { class: "grid cols-2-even" }, providersCard, activityCard),
			h("div", { class: "row between", style: { marginTop: "4px", color: "var(--ink-3)", fontSize: "12px" } }, h("span", null, "NeoProxy · engine by CLIProxyAPI", versionLine ? ` · ${versionLine}` : ""), btn("About", { cls: "sm", onClick: () => navigate("about") })),
		];
	},
};
