import { h, icon, fmtAgo, fmtInt, fmtTime } from "../lib/dom.js";
import { engine, invalidate, ApiError } from "../lib/client.js";
import { pageHead, card, btn, iconBtn, seg, search, toggle, empty, spinner, errorState, toast, confirm } from "../lib/components.js";

// Engine lines read `[time] [request-id] [level] message`. Lines without that
// prefix (stack traces, multi-line messages) belong to the line above them.
const PREFIX = /^\[[^\]]*\] \[[^\]]*\] \[(\w+)\s*\]/;
const RANK = { panic: 3, fatal: 3, error: 3, warn: 2, warning: 2, info: 1, debug: 0, trace: 0 };
const CLASS = { 3: "err", 2: "warn", 0: "dim" };
const LEVEL_MIN = { all: 0, warn: 2, err: 3 };
function rankOf(text, previous) {
	const match = PREFIX.exec(text);
	return match ? RANK[match[1].toLowerCase()] ?? 1 : previous;
}
const debounce = (fn, ms = 140) => { let timer; return (...args) => { clearTimeout(timer); timer = setTimeout(() => fn(...args), ms); }; };
const FOLLOW_MS = 2500;

export async function render(ctx) {
	const { root } = ctx;
	root.append(pageHead({ title: "Logs", desc: "The engine's own log tail and its saved error dumps. Request-level routing lives on the Activity page." }));
	let limit = 200;
	let level = "all";
	let query = "";
	let follow = true;
	let lines = []; // { number, text, rank }
	let nextNumber = 1;
	let cursor = "";
	let loadedAt;
	let logsAvailable = false;
	let revision = 0;
	let disposed = false;
	let polling = false;
	let timer;
	const logBody = h("div");
	const foot = h("span");
	const dumpBody = h("div", null, spinner());
	const settingsBody = h("div", null, spinner());
	let view = null; // the scrolling log element while lines are shown
	let followControl;
	const visible = (line) => line.rank >= LEVEL_MIN[level] && (!query || line.text.toLowerCase().includes(query));
	const lineEl = (line) => h("div", { class: `log-line ${CLASS[line.rank] ?? ""}` }, h("span", { class: "ln" }, line.number), h("span", { class: "tx" }, line.text));
	const atBottom = () => !view || view.scrollHeight - view.scrollTop - view.clientHeight < 40;
	const scrollToEnd = () => { if (view) view.scrollTop = view.scrollHeight; };
	const setFollow = (value) => {
		follow = value;
		followControl?.setAttribute("aria-checked", String(value));
		schedule();
	};
	const paintFoot = (shown) => {
		foot.textContent = `${fmtInt(shown)} of ${fmtInt(lines.length)} lines${follow ? " · following" : ""} · last reload ${fmtTime(loadedAt)}`;
	};
	const paintLines = () => {
		if (!logsAvailable) return;
		const shown = lines.filter(visible);
		if (!shown.length) {
			view = null;
			logBody.replaceChildren(empty({ icon: "scroll", title: lines.length ? "No matching lines" : "No log lines", hint: lines.length ? "Try another level or a shorter search." : "New engine messages will appear here." }));
		} else {
			view = h("div", { class: "log-view", tabindex: 0, role: "log", "aria-label": "Engine log", onScroll: () => { if (follow && !atBottom()) setFollow(false); } }, shown.map(lineEl));
			logBody.replaceChildren(view);
			scrollToEnd();
		}
		paintFoot(shown.length);
	};
	// Keep the newest `limit` lines; returns how many were new.
	const ingest = (entries, replace) => {
		if (replace) { lines = []; nextNumber = 1; }
		let previous = lines.length ? lines[lines.length - 1].rank : 1;
		for (const entry of entries) {
			const text = typeof entry === "string" ? entry : entry.message ?? entry.line ?? JSON.stringify(entry);
			previous = rankOf(text, previous);
			lines.push({ number: nextNumber++, text, rank: previous });
		}
		if (lines.length > limit) lines = lines.slice(lines.length - limit);
	};
	const failed = (e, retry) => {
		lines = [];
		logsAvailable = false;
		view = null;
		foot.textContent = "No lines loaded";
		logBody.replaceChildren(e instanceof ApiError && e.status === 400 && /logging to file disabled/i.test(e.message)
			? empty({ icon: "scroll", title: "File logging is off", hint: "The engine only serves a log tail when it writes logs to a file. Turn on Log to file under What gets logged, below." })
			: errorState(e, retry));
	};
	const reloadLogs = async () => {
		const current = ++revision;
		logsAvailable = false;
		logBody.replaceChildren(spinner());
		try {
			const result = await engine.get("observability/logs", { query: { limit } });
			if (disposed || current !== revision) return;
			let entries = Array.isArray(result) ? result : result?.lines ?? result?.logs ?? [];
			if (typeof entries === "string") entries = entries.split("\n");
			cursor = result?.["next-cursor"] ?? "";
			ingest(entries, true);
			loadedAt = Date.now();
			logsAvailable = true;
			paintLines();
			schedule();
		} catch (e) {
			if (!disposed && current === revision) failed(e, reloadLogs);
		}
	};
	// Follow: ask only for lines after the cursor and append them.
	const poll = async () => {
		if (disposed || !follow || !logsAvailable || !cursor || polling || document.hidden) return schedule();
		polling = true;
		const current = revision;
		try {
			const result = await engine.get("observability/logs", { query: { limit, cursor } });
			if (disposed || current !== revision) return;
			cursor = result?.["next-cursor"] || cursor;
			const entries = Array.isArray(result?.lines) ? result.lines : [];
			loadedAt = Date.now();
			if (result?.["cursor-reset"]) { ingest(entries, true); paintLines(); }
			else if (entries.length) {
				const before = lines.length ? lines[lines.length - 1].number : 0;
				ingest(entries, false);
				const added = lines.filter((line) => line.number > before && visible(line));
				if (!view) paintLines();
				else {
					const stick = atBottom();
					view.append(...added.map(lineEl));
					while (view.childElementCount > limit) view.firstElementChild.remove();
					if (stick) scrollToEnd();
					paintFoot(view.childElementCount);
				}
			} else paintFoot(view ? view.childElementCount : 0);
		} catch (e) {
			if (!disposed && current === revision) { setFollow(false); toast(`Stopped following: ${e.message}`, "bad"); }
		} finally { polling = false; schedule(); }
	};
	function schedule() {
		clearTimeout(timer);
		if (follow && !disposed) timer = setTimeout(poll, FOLLOW_MS);
	}
	const reloadDumps = async () => {
		try {
			const files = (await engine.errorLogs())?.files ?? [];
			if (disposed) return;
			dumpBody.replaceChildren(files.length ? h("div", { class: "list" }, files.map((file) => {
				const name = typeof file === "string" ? file : file.name;
				const modified = file.modified == null ? null : file.modified * 1000;
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
	followControl = toggle(follow, (value) => {
		setFollow(value);
		if (value) { scrollToEnd(); poll(); }
		paintFoot(view ? view.childElementCount : 0);
	}, "Follow");
	root.append(
		h("div", { class: "row wrap page-toolbar", style: { marginBottom: "14px" } },
			seg([100, 200, 500, 1000].map((n) => ({ value: n, label: String(n), title: `Last ${n} lines` })), limit, (value) => { limit = value; reloadLogs(); }),
			seg([{ value: "all", label: "All", title: "Every line" }, { value: "warn", label: "Warnings+", title: "Warnings and errors" }, { value: "err", label: "Errors", title: "Errors only" }], level, (value) => { level = value; paintLines(); }),
			search("Filter log lines", debounce((value) => { query = value.trim().toLowerCase(); paintLines(); })),
			followControl,
			h("span", { class: "spacer" }),
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
	return () => { disposed = true; clearTimeout(timer); };
}
