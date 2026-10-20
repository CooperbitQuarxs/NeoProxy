import { h, card, body, badge, btn, table, stat, fmtInt, fmtPct, fmtAgo, empty, toast, toggle, sparkline, status } from "../ui.js";
import { pageHead, providerLabel, statusKind, sumBuckets, groupBy } from "./shared.js";

export const traffic = {
	id: "traffic",
	title: "Traffic",
	icon: "activity",
	key: "t",
	async render({ load, api, invalidate, reload, navigate }) {
		const [creds, config] = await Promise.all([load.credentials(), load.config()]);
		const entries = creds?.files ?? [];
		const buckets = sumBuckets(entries);
		const totals = entries.reduce((a, e) => ({ ok: a.ok + (e.success ?? 0), failed: a.failed + (e.failed ?? 0) }), { ok: 0, failed: 0 });
		const requests = totals.ok + totals.failed;
		const lastMinutes = buckets.slice(-10).reduce((a, b) => a + b.success + b.failed, 0);
		const requestLog = Boolean(config?.observability?.["request-log"]);
		let errorFiles = [];
		try {
			const r = await load.errorLogs();
			errorFiles = r?.files ?? [];
		} catch {}

		const byModel = new Map();
		for (const e of entries) {
			for (const [model, mq] of Object.entries(e.model_quotas ?? {})) {
				const cur = byModel.get(model) ?? { model, creds: 0 };
				cur.creds++;
				byModel.set(model, cur);
			}
		}

		const busiest = [...entries].sort((a, b) => (b.success ?? 0) + (b.failed ?? 0) - ((a.success ?? 0) + (a.failed ?? 0))).slice(0, 12);
		const max = Math.max(1, ...busiest.map((e) => (e.success ?? 0) + (e.failed ?? 0)));

		return [
			pageHead("Traffic", "Requests through this proxy, by credential. Counters live in memory and reset on restart."),
			h(
				"div",
				{ class: "stats" },
				stat({ label: "Requests", value: fmtInt(requests), sub: `${fmtInt(lastMinutes)} in the last 10 minutes`, bar: true }),
				stat({ label: "Succeeded", value: fmtInt(totals.ok), bar: "ok" }),
				stat({ label: "Failed", value: fmtInt(totals.failed), sub: fmtPct(totals.failed, requests), bar: totals.failed / Math.max(1, requests) > 0.05 ? "bad" : "muted" }),
				stat({ label: "Request log", value: requestLog ? "on" : "off", mono: true, sub: requestLog ? "full payloads kept on disk" : "only errors are kept" }),
				stat({ label: "Error dumps", value: fmtInt(errorFiles.length), sub: "files under the log directory" }),
			),
			h(
				"div",
				{ class: "grid cols-2" },
				card({
					title: "Per minute",
					sub: buckets.length ? `${buckets[0].time} → ${buckets[buckets.length - 1].time}` : "No requests observed since start",
					body: body(
						buckets.length
							? [
									sparkline(buckets.map((b) => b.success + b.failed)),
									h("div", { class: "row between", style: { marginTop: "8px", fontSize: "12px", color: "var(--ink-3)" } }, h("span", null, status("ok", `${fmtInt(buckets.reduce((a, b) => a + b.success, 0))} ok`)), h("span", null, status("bad", `${fmtInt(buckets.reduce((a, b) => a + b.failed, 0))} failed`))),
								]
							: empty({ icon: "activity", title: "Quiet", hint: "Send a request through one of the endpoints and it will show up here." }),
					),
				}),
				card({
					title: "Logging",
					sub: "What the proxy writes to disk for each request.",
					body: body([
						h(
							"div",
							{ class: "stack", style: { gap: "12px" } },
							toggle(requestLog, async (v) => {
								try {
									await api.patch("config", { observability: { "request-log": v } });
									invalidate("config");
									toast(v ? "Request log enabled" : "Request log disabled", "ok");
								} catch (ex) {
									toast(`Could not change request log: ${ex.message}`, "bad");
								}
							}, "Log full request and response payloads"),
							toggle(Boolean(config?.observability?.logs?.debug), async (v) => {
								try {
									await api.patch("config", { observability: { logs: { debug: v } } });
									invalidate("config");
									toast(v ? "Debug logging on" : "Debug logging off", "ok");
								} catch (ex) {
									toast(`Could not change debug: ${ex.message}`, "bad");
								}
							}, "Debug logging"),
						),
					]),
					foot: [h("span", null, "Application and error logs are on the Logs page."), btn("Logs", { cls: "sm", icon: "arrowRight", onClick: () => navigate("logs") })],
				}),
			),
			card({
				title: "Busiest credentials",
				sub: "Where the requests actually went.",
				actions: btn("All credentials", { cls: "sm", icon: "arrowRight", onClick: () => navigate("credentials") }),
				body: h(
					"div",
					{ class: "card-body flush" },
					table(
						[
							{ label: "Credential", render: (e) => h("div", { class: "cell-stack" }, h("span", null, e.label || e.email || e.name), h("span", { class: "sub" }, providerLabel(e.provider))) },
							{ label: "Status", render: (e) => status(statusKind(e), e.disabled ? "disabled" : e.status || "active") },
							{ label: "Requests", cls: "num", render: (e) => h("span", null, fmtInt((e.success ?? 0) + (e.failed ?? 0)), h("span", { class: "bar-track" }, h("span", { class: "bar-fill", style: { width: `${(100 * ((e.success ?? 0) + (e.failed ?? 0))) / max}%` } }))) },
							{ label: "Failed", cls: "num", render: (e) => fmtInt(e.failed ?? 0) },
							{ label: "Error rate", cls: "num", render: (e) => badge(fmtPct(e.failed ?? 0, (e.success ?? 0) + (e.failed ?? 0)), (e.failed ?? 0) / Math.max(1, (e.success ?? 0) + (e.failed ?? 0)) > 0.05 ? "bad" : "ok") },
							{ label: "Last 10 min", render: (e) => sparkline((e.recent_requests ?? []).slice(-10).map((b) => (b.success ?? 0) + (b.failed ?? 0))) },
						],
						busiest,
						{ onRow: (e) => navigate(`credentials?select=${encodeURIComponent(e.name || e.id)}`), emptyState: empty({ title: "No credentials", hint: "Nothing can carry traffic yet." }) },
					),
				),
			}),
		];
	},
};
