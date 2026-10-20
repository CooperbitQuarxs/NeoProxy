import { h, card, body, badge, btn, table, stat, fmtInt, fmtPct, fmtAgo, wip, empty, toast, status } from "../ui.js";
import { pageHead, providerLabel, statusKind, groupBy } from "./shared.js";

function quotaRows(entries) {
	const rows = [];
	for (const e of entries) {
		const q = e.quota;
		const signals = q?.signals ?? (Array.isArray(q) ? q : null);
		if (signals && typeof signals === "object") {
			for (const [key, sig] of Object.entries(signals)) {
				if (!sig || typeof sig !== "object") continue;
				rows.push({ cred: e, window: key, ...sig, observed: q.observed_at });
			}
		}
		for (const [model, mq] of Object.entries(e.model_quotas ?? {})) {
			if (!mq || typeof mq !== "object") continue;
			rows.push({ cred: e, window: model, ...mq, observed: mq.observed_at ?? q?.observed_at });
		}
	}
	return rows;
}

function pct(sig) {
	const used = Number(sig.used ?? sig.used_percent ?? sig.percent_used);
	const remaining = Number(sig.remaining ?? sig.remaining_percent ?? sig.percent_remaining);
	if (Number.isFinite(used)) return used <= 1 ? used * 100 : used;
	if (Number.isFinite(remaining)) return 100 - (remaining <= 1 ? remaining * 100 : remaining);
	return null;
}

export const usage = {
	id: "usage",
	title: "Usage & quotas",
	icon: "gauge",
	key: "u",
	wip: true,
	async render({ load, api, invalidate, reload, navigate }) {
		const creds = await load.credentials();
		const entries = creds?.files ?? [];
		const quotaCapable = entries.filter((e) => e.supports_quota);
		const rows = quotaRows(entries);
		const byProvider = groupBy(entries, (e) => e.provider || "unknown");

		const providerTable = table(
			[
				{ label: "Provider", render: (r) => h("div", { class: "cell-stack" }, h("span", null, providerLabel(r.id)), h("span", { class: "sub" }, r.id)) },
				{ label: "Credentials", cls: "num", render: (r) => fmtInt(r.list.length) },
				{ label: "Requests", cls: "num", render: (r) => fmtInt(r.ok + r.failed) },
				{ label: "Failed", cls: "num", render: (r) => fmtInt(r.failed) },
				{ label: "Error rate", cls: "num", render: (r) => badge(fmtPct(r.failed, r.ok + r.failed), r.failed / Math.max(1, r.ok + r.failed) > 0.05 ? "bad" : "ok") },
				{ label: "Quota probes", cls: "num", render: (r) => fmtInt(r.list.filter((e) => e.supports_quota).length) },
			],
			[...byProvider.entries()].map(([id, list]) => ({ id, list, ok: list.reduce((a, e) => a + (e.success ?? 0), 0), failed: list.reduce((a, e) => a + (e.failed ?? 0), 0) })).sort((a, b) => b.ok + b.failed - (a.ok + a.failed)),
			{ emptyState: empty({ title: "No credentials", hint: "Usage is counted per credential; add one under Credentials." }), onRow: (r) => navigate(`credentials?provider=${encodeURIComponent(r.id)}`) },
		);

		const quotaTable = table(
			[
				{ label: "Credential", render: (r) => h("div", { class: "cell-stack" }, h("span", null, r.cred.label || r.cred.email || r.cred.name), h("span", { class: "sub" }, providerLabel(r.cred.provider))) },
				{ label: "Window / model", cls: "id", render: (r) => r.window },
				{
					label: "Used",
					cls: "num",
					render: (r) => {
						const p = pct(r);
						if (p === null) return h("span", { class: "dim" }, "-");
						return h("span", null, `${p.toFixed(1)}%`, h("span", { class: "bar-track" }, h("span", { class: "bar-fill", style: { width: `${Math.min(100, p)}%`, background: p > 90 ? "var(--bad)" : p > 70 ? "var(--warn)" : "var(--accent-a)" } })));
					},
				},
				{ label: "Resets", cls: "dim", render: (r) => (r.resets_at || r.reset_at || r.reset ? fmtAgo(r.resets_at || r.reset_at || r.reset).replace(" ago", "") : "-") },
				{ label: "Observed", cls: "dim", render: (r) => fmtAgo(r.observed) },
			],
			rows,
			{
				emptyState: empty({
					title: quotaCapable.length ? "No quota observations yet" : "No quota-capable credentials",
					hint: quotaCapable.length ? "Fetch quotas to ask each provider for its current windows." : "Subscription logins (Codex, Claude, Antigravity, …) expose quota windows; API keys do not.",
				}),
			},
		);

		const fetchBtn = btn("Fetch quotas", {
			icon: "refresh",
			cls: "outline sm",
			disabled: !quotaCapable.length,
			onClick: async () => {
				try {
					await api.post("/v0/management/quota/fetch", {});
					invalidate("credentials");
					toast("Quota fetch requested", "ok");
					setTimeout(reload, 1500);
				} catch (e) {
					toast(`Quota fetch failed: ${e.message}`, "bad");
				}
			},
		});

		return [
			pageHead("Usage & quotas", "What this proxy has spent per provider, and the quota windows the providers report."),
			h(
				"div",
				{ class: "stats" },
				stat({ label: "Requests via proxy", value: fmtInt(entries.reduce((a, e) => a + (e.success ?? 0) + (e.failed ?? 0), 0)), sub: "since the proxy started", bar: true }),
				stat({ label: "Quota-capable credentials", value: fmtInt(quotaCapable.length), sub: `${entries.length} total` }),
				stat({ label: "Quota windows observed", value: fmtInt(rows.length), sub: rows.length ? `latest ${fmtAgo(rows.map((r) => r.observed).filter(Boolean).sort().pop())}` : "none yet" }),
				stat({ label: "Cooling", value: fmtInt(entries.filter((e) => statusKind(e) === "warn").length), sub: "credentials waiting on a retry window", bar: entries.some((e) => statusKind(e) === "warn") ? "warn" : "muted" }),
			),
			card({
				cls: "centre",
				title: ["Mirrored from NeoPi", badge("not implemented yet", "accent")],
				sub: "NeoPi already tracks API-equivalent cost, weekly subscription windows and per-provider headroom. Once linked, that view appears here unchanged.",
				body: body(
					wip({
						title: "NeoPi usage mirror",
						text: "The same numbers NeoPi's /stats shows (cost, requests, tokens, cache rate, subscription usage per provider) rendered for the traffic that passed through this proxy.",
						items: ["Weekly and five-hour windows per subscription, as NeoPi reports them.", "API-equivalent cost for proxied requests, by client key and by model.", "Headroom warnings before a window is exhausted, so routing can fail over early."],
						links: [btn("NeoPi link", { cls: "sm outline", onClick: () => navigate("neopi") })],
					}),
					"tight",
				),
			}),
			card({ title: "By provider", sub: "Counted by this proxy since it started; resets on restart.", body: h("div", { class: "card-body flush" }, providerTable) }),
			card({ title: "Provider quota windows", sub: "What subscription-backed credentials report about their remaining allowance.", actions: fetchBtn, body: h("div", { class: "card-body flush" }, quotaTable) }),
		];
	},
};
