import { h, card, body, badge, btn, table, stat, fmtInt, fmtAgo, empty, toast, confirm, seg, icon } from "../ui.js";
import { pageHead } from "./shared.js";

function classify(line) {
	const l = line.toLowerCase();
	if (/\b(error|fatal|panic)\b/.test(l)) return "err";
	if (/\b(warn|warning)\b/.test(l)) return "wrn";
	return "";
}

export const logs = {
	id: "logs",
	title: "Logs",
	icon: "scroll",
	key: "l",
	async render({ load, api, route, navigate, reload }) {
		const config = await load.config();
		const toFile = Boolean(config?.observability?.logs?.["logging-to-file"] ?? config?.observability?.["logging-to-file"]);
		const limit = Number(route.query.get("n") || 200);
		const level = route.query.get("level") || "all";
		let lines = [];
		let logErr = null;
		try {
			const r = await load.logs(limit);
			lines = Array.isArray(r) ? r : (r?.lines ?? r?.logs ?? []);
			if (typeof lines === "string") lines = lines.split("\n");
		} catch (ex) {
			logErr = ex;
		}
		let errorFiles = [];
		try {
			errorFiles = (await load.errorLogs())?.files ?? [];
		} catch {}
		const shown = lines.filter((l) => level === "all" || classify(typeof l === "string" ? l : JSON.stringify(l)) === level);
		const pre = h(
			"pre",
			{ class: "code log", style: { maxHeight: "56vh" } },
			shown.length
				? shown.map((l) => {
						const text = typeof l === "string" ? l : (l.message ?? l.line ?? JSON.stringify(l));
						return h("span", { class: classify(text) }, text, "\n");
					})
				: h("span", { style: { color: "var(--ink-3)" } }, logErr ? `Logs unavailable: ${logErr.message}` : toFile ? "No lines." : "Logging to file is off; enable it to read logs here."),
		);
		setTimeout(() => (pre.scrollTop = pre.scrollHeight), 0);

		const setQ = (patch) => {
			const next = new URLSearchParams(route.query);
			for (const [k, v] of Object.entries(patch)) (v ? next.set(k, v) : next.delete(k));
			navigate(`logs?${next}`);
		};

		return [
			pageHead("Logs", "What the engine wrote, newest at the bottom. Error dumps carry the full failing request.", [
				btn("Reload", { icon: "refresh", cls: "outline", onClick: reload }),
				btn("Clear logs", {
					icon: "trash",
					cls: "danger outline",
					onClick: async () => {
						if (!(await confirm({ title: "Clear application logs?", text: "Log files on disk are truncated. Error dumps are kept.", confirmLabel: "Clear", danger: true }))) return;
						try {
							await api.del("observability/logs");
							toast("Logs cleared", "ok");
							reload();
						} catch (ex) {
							toast(`Clear failed: ${ex.message}`, "bad");
						}
					},
				}),
			]),
			h(
				"div",
				{ class: "stats" },
				stat({ label: "Logging to file", value: toFile ? "on" : "off", mono: true, sub: toFile ? "readable here" : "turn on under Traffic → Logging", bar: toFile ? "ok" : "warn" }),
				stat({ label: "Lines loaded", value: fmtInt(lines.length), sub: `last ${limit}` }),
				stat({ label: "Errors in view", value: fmtInt(lines.filter((l) => classify(typeof l === "string" ? l : JSON.stringify(l)) === "err").length), bar: "bad" }),
				stat({ label: "Error dumps", value: fmtInt(errorFiles.length), sub: "request-level failure files" }),
			),
			card({
				title: "Application log",
				actions: [
					seg(
						[
							{ label: "All", value: "all" },
							{ label: "Warnings", value: "wrn" },
							{ label: "Errors", value: "err" },
						],
						level,
						(v) => setQ({ level: v === "all" ? "" : v }),
					),
					seg(
						[
							{ label: "200", value: "200" },
							{ label: "1k", value: "1000" },
							{ label: "5k", value: "5000" },
						],
						String(limit),
						(v) => setQ({ n: v }),
					),
				],
				body: body(pre, "tight"),
			}),
			card({
				title: "Error dumps",
				sub: "One file per failed upstream request, with the request and the provider's answer.",
				body: h(
					"div",
					{ class: "card-body flush" },
					table(
						[
							{ label: "File", cls: "id", render: (f) => (typeof f === "string" ? f : f.name) },
							{ label: "Size", cls: "num", render: (f) => (typeof f === "string" ? "-" : fmtInt(f.size)) },
							{ label: "When", cls: "dim", render: (f) => (typeof f === "string" ? "-" : fmtAgo(f.modified ?? f.mod_time ?? f.modtime)) },
							{
								label: "",
								cls: "actions",
								render: (f) => {
									const name = typeof f === "string" ? f : f.name;
									return btn("Open", {
										icon: "external",
										cls: "sm",
										onClick: async () => {
											try {
												const res = await api.raw("GET", `observability/logs/errors/${encodeURIComponent(name)}`);
												const text = await res.text();
												const w = window.open("", "_blank");
												if (w) {
													w.document.title = name;
													w.document.body.style.cssText = "background:#0b0810;color:#ededef;font:12.5px/1.5 ui-monospace,monospace;padding:16px;white-space:pre-wrap";
													w.document.body.textContent = text;
												}
											} catch (ex) {
												toast(`Could not open ${name}: ${ex.message}`, "bad");
											}
										},
									});
								},
							},
						],
						errorFiles,
						{ emptyState: empty({ icon: "check", title: "No error dumps", hint: "Failed upstream requests write a dump here." }) },
					),
				),
			}),
		];
	},
};
