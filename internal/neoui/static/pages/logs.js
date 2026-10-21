import { h, icon, fmtAgo, fmtInt, fmtTime } from "../lib/dom.js";
import { engine, invalidate, ApiError } from "../lib/client.js";
import { pageHead, card, btn, iconBtn, seg, search, toggle, empty, spinner, errorState, toast, confirm } from "../lib/components.js";

function levelOf(text) {
	if (/\[(error|fatal)\]/i.test(text)) return "err";
	if (/\[warn ?\]/i.test(text)) return "warn";
	return "";
}

export async function render(ctx) {
	const { root } = ctx;
	root.append(pageHead({ title: "Logs", desc: "The engine's own log tail and its saved error dumps. Request-level routing lives on the Activity page." }));
	let limit = 200;
	let level = "all";
	let query = "";
	let lines = [];
	let loadedAt;
	let logsAvailable = false;
	let revision = 0;
	let disposed = false;
	const logBody = h("div");
	const foot = h("span");
	const dumpBody = h("div", null, spinner());
	const settingsBody = h("div", null, spinner());
	const paintLines = () => {
		if (!logsAvailable) return;
		const visible = lines.filter((line) => (level === "all" || levelOf(line.text) === level) && line.text.toLowerCase().includes(query.toLowerCase()));
		const pre = h("pre", { class: "code", style: { maxHeight: "60vh", margin: 0, border: 0, borderRadius: 0 } }, visible.map((line) => h("span", { class: levelOf(line.text) }, h("span", { class: "ln" }, line.number), line.text, "\n")));
		logBody.replaceChildren(visible.length ? pre : empty({ icon: "scroll", title: lines.length ? "No matching lines" : "No log lines", hint: lines.length ? "Try another level or a shorter search." : "New engine messages will appear here when you reload." }));
		pre.scrollTop = pre.scrollHeight;
		foot.textContent = `${fmtInt(visible.length)} lines · last reload ${fmtTime(loadedAt)}`;
	};
	const reloadLogs = async () => {
		const current = ++revision;
		logsAvailable = false;
		logBody.replaceChildren(spinner());
		try {
			const config = await engine.config();
			if (disposed || current !== revision) return;
			if (!(config.observability?.logs?.["logging-to-file"] ?? config.observability?.["logging-to-file"])) {
				lines = [];
				foot.textContent = "No lines loaded";
				logBody.replaceChildren(empty({ icon: "scroll", title: "File logging is off", hint: "File logging is off; turn it on under What gets logged" }));
				return;
			}
			const result = await engine.logs(limit);
			if (disposed || current !== revision) return;
			let entries = Array.isArray(result) ? result : result?.lines ?? result?.logs ?? [];
			if (typeof entries === "string") entries = entries.split("\n");
			lines = entries.map((line, i) => ({ number: i + 1, text: typeof line === "string" ? line : line.message ?? line.line ?? JSON.stringify(line) }));
			loadedAt = Date.now();
			logsAvailable = true;
			paintLines();
		} catch (e) {
			if (disposed || current !== revision) return;
			lines = [];
			foot.textContent = "No lines loaded";
			logBody.replaceChildren(e instanceof ApiError && e.status === 400 && /logging to file disabled/i.test(e.message)
				? empty({ icon: "scroll", title: "File logging is off", hint: "File logging is off; turn it on under What gets logged" })
				: errorState(e, reloadLogs));
		}
	};
	const reloadDumps = async () => {
		try {
			const files = (await engine.errorLogs())?.files ?? [];
			if (disposed) return;
			dumpBody.replaceChildren(files.length ? h("div", { class: "list" }, files.map((file) => {
				const name = typeof file === "string" ? file : file.name;
				const modified = file.modified ?? file.mod_time ?? file.modtime;
				return h("div", { class: "li" }, icon("scroll", 15), h("div", { class: "lt" }, h("div", { class: "main" }, h("code", null, name)), h("div", { class: "sub" }, [file.size != null ? `${fmtInt(file.size)} bytes` : null, modified ? fmtAgo(modified) : null].filter(Boolean).join(" · "))), iconBtn("download", `Download ${name}`, async () => {
					try {
						const response = await engine.raw("GET", `observability/logs/errors/${encodeURIComponent(name)}`);
						if (!response.ok) throw new Error(`Could not download ${name}`);
						const url = URL.createObjectURL(await response.blob());
						const link = h("a", { href: url, download: name });
						document.body.append(link);
						link.click();
						link.remove();
						setTimeout(() => URL.revokeObjectURL(url), 1000);
					} catch (e) { toast(e.message, "bad"); }
				}));
			})) : empty({ icon: "scroll", title: "No error dumps", hint: "The engine writes one here when an upstream request fails hard." }));
		} catch (e) {
			if (!disposed) dumpBody.replaceChildren(errorState(e, reloadDumps));
		}
	};
	root.append(
		h("div", { class: "row", style: { marginBottom: "14px" } },
			seg([100, 200, 500, 1000].map((n) => ({ value: n, label: String(n), title: `Last ${n} lines` })), limit, (value) => { limit = value; reloadLogs(); }),
			seg([{ value: "all", label: "All" }, { value: "warn", label: "Warnings" }, { value: "err", label: "Errors" }], level, (value) => { level = value; paintLines(); }),
			search("Filter log lines", (value) => { query = value; paintLines(); }), h("span", { class: "spacer" }),
			btn("Reload", { icon: "refresh", cls: "outline", onClick: () => Promise.all([reloadLogs(), reloadDumps()]) }),
			btn("Clear logs", { cls: "danger", onClick: async () => {
				if (!(await confirm({ title: "Clear logs?", text: "The engine's log files are emptied. Saved error dumps are kept.", confirmLabel: "Clear logs", danger: true }))) return;
				try { await engine.del("observability/logs"); invalidate("logs"); toast("Logs cleared"); await reloadLogs(); }
				catch (e) { toast(e.message, "bad"); }
			} }),
		),
		card({ flush: true, body: logBody, foot }),
		card({ title: "Error dumps", flush: true, body: dumpBody }),
		card({ title: "What gets logged", body: settingsBody }),
	);
	const loadSettings = async () => {
		try {
			const config = await engine.config();
			if (disposed) return;
			const obs = config.observability ?? {};
			const setting = (label, checked, patch, message, reload = false) => {
				let control;
				control = toggle(checked, async (value) => {
					control.disabled = true;
					try {
						await engine.patch("config", { observability: patch(value) });
						invalidate("config");
						toast(`${message} ${value ? "on" : "off"}`);
						if (reload) await reloadLogs();
					} catch (e) { control.setAttribute("aria-checked", String(!value)); toast(e.message, "bad"); }
					finally { control.disabled = false; }
				}, label);
				return control;
			};
			settingsBody.replaceChildren(h("div", { class: "row wrap" },
				setting("Log every request", Boolean(obs.logs?.["request-log"]), (v) => ({ logs: { "request-log": v } }), "Request log"),
				setting("Debug logging", Boolean(obs.logs?.debug), (v) => ({ logs: { debug: v } }), "Debug logging"),
				setting("Log to file", Boolean(obs.logs?.["logging-to-file"] ?? obs["logging-to-file"]), (v) => ({ logs: { "logging-to-file": v } }), "File logging", true),
			));
		} catch (e) { if (!disposed) settingsBody.replaceChildren(errorState(e, loadSettings)); }
	};
	await Promise.all([reloadLogs(), reloadDumps(), loadSettings()]);
	return () => { disposed = true; };
}
