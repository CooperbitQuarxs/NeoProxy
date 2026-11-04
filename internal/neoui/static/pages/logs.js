// Logs: the engine's own log tail, the error reports it saves, and the three
// switches that decide what it writes. Request-level routing is on Activity.

import { h, icon, fmtAgo, fmtInt, fmtTime, plural } from "../lib/dom.js";
import { engine, invalidate, ApiError } from "../lib/client.js";
import { pageHead, card, btn, iconBtn, seg, search, select, toggle, empty, spinner, errorState, toast, confirm, sheet, kv, badge } from "../lib/components.js";
import { jsonView } from "../lib/json.js";
import { reportInfo, parseReport, reportFacts, reportFactsOf, reportHeadline, fetchReport } from "../lib/error-report.js";

// Engine lines read `[time] [request-id] [level] message`. Lines without that
// prefix (stack traces, multi-line messages) belong to the line above them.
const PREFIX = /^\[[^\]]*\] \[[^\]]*\] \[(\w+)\s*\]/;
// The same prefix taken apart: time, request id, level, optional source, message.
const PARTS = /^\[([^\]]*)\] \[([^\]]*)\] \[(\w+)\s*\]\s?(?:\[([^\]]+)\]\s?)?([\s\S]*)$/;
// Access lines written by this page's own log polling; showing them would make the log watch itself.
const SELF = /\| GET\s+"\/v\d+\/management\/observability\/logs(?:\?|")/;
// An HTTP access line: `200 |  31ms |  127.0.0.1 | GET  "/v8/neo/core"`.
const ACCESS = /^\s*(\d{3}) \|\s*(\S+) \|\s*(\S+) \|\s*([A-Z]+)\s+"([^"]*)"\s*$/;
// Calls the dashboard itself makes (its own API and sign-in), as opposed to client traffic.
const DASHBOARD = /^\/(?:v\d+\/(?:neo|management|session)\b|neo\/)/;
const accessOf = (text) => {
	const m = PARTS.exec(text);
	const a = m ? ACCESS.exec(m[5]) : null;
	return a ? { status: Number(a[1]), took: a[2], client: a[3], method: a[4], path: a[5] } : null;
};
// One line, laid out as columns: number, time, level, message, then where it came from.
// `mark` highlights search matches in the message; `markMeta` also in the request id and source.
function renderLine(number, text, rank, mark = (s) => s, markMeta = false) {
	const m = PARTS.exec(text);
	const ln = h("span", { class: "ln" }, number);
	if (!m) return h("div", { class: `log-line cont ${CLASS[rank] ?? ""}` }, ln, h("span", { class: "tx" }, mark(text)));
	const [, time, rid, lvl, src, msg] = m;
	const word = lvl.toLowerCase();
	const a = ACCESS.exec(msg);
	const meta = markMeta ? mark : (s) => s;
	const body = a
		? [h("span", { class: `lg-st s${a[1][0]}` }, a[1]), h("span", { class: "lg-method" }, a[4]), h("span", { class: "lg-path" }, mark(a[5])), h("span", { class: "lg-took", title: `from ${a[3]}` }, a[2])]
		: [mark(msg)];
	return h(
		"div",
		{ class: `log-line parsed ${a ? "access" : ""} ${CLASS[rank] ?? ""}` },
		ln,
		h("span", { class: "lt-time", title: time }, time.split(" ").pop()),
		h("span", { class: `lt-lvl ${word}` }, word === "warning" ? "warn" : word),
		h("span", { class: "tx" }, body, rid && !/^-+$/.test(rid) ? h("span", { class: "lt-rid" }, meta(rid)) : null, src && !a ? h("span", { class: "lt-src" }, meta(src)) : null),
	);
}
// What a search looks at: the message (and, for access lines, status, method and
// path); the request id and source file only when asked for.
function searchable(text, withMeta) {
	if (withMeta) return text;
	const m = PARTS.exec(text);
	if (!m) return text;
	const a = ACCESS.exec(m[5]);
	return a ? `${a[1]} ${a[4]} ${a[5]}` : m[5];
}

// ---------- error reports ----------
// Read as a summary, then one titled part per section, with headers folded away.
const SECTION_NAMES = { "REQUEST INFO": "Request", HEADERS: "Request headers", "REQUEST BODY": "What the client sent", "API RESPONSE": "What the provider answered", RESPONSE: "What the client got back" };
const sectionName = (raw) => SECTION_NAMES[raw] ?? (/^API REQUEST (\d+)$/.exec(raw) ? `Sent to the provider, try ${/\d+$/.exec(raw)[0]}` : raw.charAt(0) + raw.slice(1).toLowerCase());
function reportView(text) {
	const sections = parseReport(text);
	const f = reportFacts(sections);
	const summary = h(
		"div",
		{ class: "lg-sum" },
		h("div", { class: "lg-sum-head" }, f.status ? badge(`HTTP ${f.status}`, "bad") : null, h("span", null, f.message ? reportHeadline(f) : "The report does not say why the request failed.")),
		kv([
			f.asked ? ["Asked for", h("code", null, f.asked)] : null,
			f.sentModel ? ["Sent as", h("code", null, f.provider ? `${f.provider} / ${f.sentModel}` : f.sentModel)] : null,
			f.account ? ["Account", f.account] : null,
			f.tries ? ["Upstream", h("code", null, f.upstream ?? "–")] : null,
			f.tries > 1 ? ["Tries", String(f.tries)] : null,
		]),
	);
	const partView = (p) =>
		p.kind === "pairs" ? kv(p.pairs.map(([k, v]) => [k, h("span", { class: "mono" }, v)]))
		: p.kind === "json" ? jsonView(p.value, { collapsedDepth: 2 })
		: p.kind === "label" ? h("div", { class: "lg-rep-label" }, p.text)
		: h("div", { class: "lg-rep-text mono" }, p.text);
	const blocks = sections.filter((s) => s.parts.length).map((s) => {
		const folded = /HEADERS/.test(s.title);
		const body = s.parts.map(partView);
		const el = h("details", { class: "lg-rep", open: !folded }, h("summary", null, icon("chevronRight", 13), sectionName(s.title)), h("div", { class: "lg-rep-body" }, body));
		return el;
	});
	return [summary, ...blocks];
}
const RANK = { panic: 3, fatal: 3, error: 3, warn: 2, warning: 2, info: 1, debug: 0, trace: 0 };
const CLASS = { 3: "err", 2: "warn", 0: "dim" };
const LEVEL_MIN = { all: 0, warn: 2, err: 3 };
const LIMITS = [100, 200, 500, 1000];
function rankOf(text, previous) {
	const match = PREFIX.exec(text);
	return match ? RANK[match[1].toLowerCase()] ?? 1 : previous;
}
const debounce = (fn, ms = 140) => { let timer; return (...args) => { clearTimeout(timer); timer = setTimeout(() => fn(...args), ms); }; };
const FOLLOW_MS = 2500;
const DUMPS_SHOWN = 6;

const fmtBytes = (n) => (n < 1024 ? `${n} B` : n < 1024 * 1024 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1024 / 1024).toFixed(1)} MB`);
const whenLabel = (ts) => new Date(ts).toLocaleString("en-GB", { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" });

export async function render(ctx) {
	const { root } = ctx;
	root.append(pageHead({ title: "Logs", desc: "The engine's own log and the error reports it keeps. How each request was routed is on Activity." }));
	// The dashboard's own calls are hidden by default, so keep enough lines that real ones remain.
	let limit = 1000;
	let level = LEVEL_MIN[ctx.query.get("level")] !== undefined ? ctx.query.get("level") : "all";
	let query = (ctx.query.get("q") ?? "").trim().toLowerCase();
	let follow = true;
	let showDashboard = false; // the dashboard's own API calls are hidden unless asked for
	let lines = []; // { number, text, rank }
	let nextNumber = 1;
	let cursor = "";
	let loadedAt;
	let logsAvailable = false;
	let revision = 0;
	let disposed = false;
	let polling = false;
	let timer;
	let fileSwitch = null; // the "Write to a file" switch, once settings load
	let requestLogOn = false; // with full request logs on, the engine keeps no separate error reports
	let dumpSheet = null;
	const logBody = h("div", { class: "lg-body" });
	const footText = h("span", { class: "lg-foot-text" });
	const dumpBody = h("div", null, spinner());
	const settingsBody = h("div", null, spinner());
	let view = null; // the scrolling log element while lines are shown
	let lastResult = null; // what the view last showed, for the foot
	// Level and the dashboard switch decide which lines exist; the search then picks among them.
	let withMeta = ctx.query.get("meta") === "1"; // search source files and request ids too
	const visible = (line) => line.rank >= LEVEL_MIN[level] && (showDashboard || !line.dash);
	const matches = (line) => !query || searchable(line.text, withMeta).toLowerCase().includes(query);
	// Matches are marked so a search reads at a glance.
	const marked = (text) => {
		if (!query) return text;
		const out = [];
		const lower = text.toLowerCase();
		let from = 0;
		for (let at = lower.indexOf(query); at !== -1; at = lower.indexOf(query, from)) {
			out.push(text.slice(from, at), h("mark", null, text.slice(at, at + query.length)));
			from = at + query.length;
		}
		out.push(text.slice(from));
		return out;
	};
	const lineEl = (line) => renderLine(line.number, line.text, line.rank, matches(line) ? marked : (s) => s, withMeta);
	const atBottom = () => !view || view.scrollHeight - view.scrollTop - view.clientHeight < 40;
	const scrollToEnd = () => { if (view) view.scrollTop = view.scrollHeight; };
	const setUrl = () => {
		if (disposed || !/^#\/logs(?:[/?]|$)/.test(location.hash)) return;
		const q = new URLSearchParams();
		if (query) q.set("q", query);
		if (withMeta) q.set("meta", "1");
		if (level !== "all") q.set("level", level);
		const hash = `#/logs${q.size ? `?${q}` : ""}`;
		if (location.hash !== hash) history.replaceState(null, "", hash);
	};

	// ---------- toolbar ----------

	const followControl = toggle(follow, (value) => {
		setFollow(value);
		if (value) { scrollToEnd(); poll(); }
		if (lastResult) paintFoot(lastResult);
	}, "Follow new lines");
	const setFollow = (value) => {
		follow = value;
		followControl.setAttribute("aria-checked", String(value));
		schedule();
	};
	const searchEl = search("Search log messages", debounce((value) => { query = value.trim().toLowerCase(); setUrl(); paintLines(); }), { "aria-label": "Search log messages", value: query });
	const metaToggle = toggle(withMeta, (value) => { withMeta = value; setUrl(); paintLines(); }, "Include source files and request ids");
	metaToggle.classList.add("lg-meta");
	const levelSeg = seg([{ value: "all", label: "All", title: "Every line" }, { value: "warn", label: "Warnings", title: "Warnings and errors" }, { value: "err", label: "Errors", title: "Errors only" }], level, (value) => { level = value; setUrl(); paintLines(); });
	const limitSelect = select(LIMITS.map((n) => ({ value: String(n), label: `Last ${n} lines` })), String(limit), (value) => { limit = Number(value); reloadLogs(); }, { class: "select lg-limit", "aria-label": "How many lines to keep" });
	const toolbar = h("div", { class: "row wrap lg-toolbar" },
		searchEl,
		metaToggle,
		levelSeg,
		limitSelect,
		h("span", { class: "spacer" }),
		followControl,
		iconBtn("refresh", "Reload the log and error reports", () => Promise.all([reloadLogs(), reloadDumps()]), "sm"),
	);
	const clearBtn = btn("Clear log", { icon: "trash", cls: "ghost sm lg-clear", onClick: async () => {
		if (!(await confirm({ title: "Clear the engine log?", text: "The engine's log files are emptied. Saved error reports are kept.", confirmLabel: "Clear log", danger: true }))) return;
		try { await engine.del("observability/logs"); invalidate("logs"); toast("Log cleared"); await reloadLogs(); }
		catch (e) { toast(e.message, "bad"); }
	} });
	const dashToggle = h("button", { type: "button", class: "lg-dash", onClick: () => { showDashboard = !showDashboard; paintLines(); } });
	const foot = h("div", { class: "card-foot lg-foot" }, footText, dashToggle, h("span", { class: "spacer" }), clearBtn);
	const logCard = h("section", { class: "card rise lg-card" }, toolbar, logBody, foot);

	// ---------- the log ----------

	const paintFoot = ({ shown, matched, bursts, burstHits }) => {
		const filtered = level !== "all" || !showDashboard;
		const parts = [];
		const inside = bursts ? `, ${burstHits === matched ? "all" : fmtInt(burstHits)} inside ${plural(bursts, "folded configuration reload")}` : "";
		if (query) parts.push(`${fmtInt(matched)} ${matched === 1 ? "line matches" : "lines match"} “${query}” in ${withMeta ? "messages, sources and request ids" : "messages"}${inside}`);
		else parts.push(filtered ? `${fmtInt(shown)} of ${fmtInt(lines.length)} lines shown` : `${fmtInt(lines.length)} lines`);
		if (follow) parts.push("following");
		if (loadedAt) parts.push(`updated ${fmtTime(loadedAt)}`);
		footText.textContent = parts.join(" · ");
		const dash = lines.filter((line) => line.dash).length;
		dashToggle.hidden = !dash;
		dashToggle.textContent = showDashboard ? "Hide the dashboard's own calls" : `Show ${fmtInt(dash)} calls from this dashboard`;
	};
	const showControls = (on) => {
		toolbar.hidden = !on;
		foot.hidden = !on;
	};
	// A configuration reload writes a burst of lines, most of them "updated (0 -> 0 rules)".
	// A burst reads as one line naming what really changed, which opens to the raw
	// lines. While searching it stays one line: the changes that match lead, with a
	// count of matching lines; a burst with no match is left out.
	const RELOAD_SRC = /^(config_reload|clients)\.go:/;
	const viewItems = (shown) => {
		const out = [];
		let matched = 0;
		let bursts = 0;
		let burstHits = 0;
		for (let i = 0; i < shown.length;) {
			const src = PARTS.exec(shown[i].text)?.[4] ?? "";
			let j = i + 1;
			if (/^config_reload\.go:/.test(src)) {
				while (j < shown.length && RELOAD_SRC.test(PARTS.exec(shown[j].text)?.[4] ?? "")) {
					if (/config file changed/.test(PARTS.exec(shown[j].text)?.[5] ?? "")) break;
					j++;
				}
			}
			const burst = shown.slice(i, j);
			i = j;
			if (burst.length < 3) {
				for (const line of burst) if (matches(line)) { matched++; out.push(lineEl(line)); }
				continue;
			}
			const hits = burst.filter(matches).length;
			if (query && !hits) continue;
			matched += hits;
			if (query) { bursts++; burstHits += hits; }
			const all = burst.map((l) => /^\s*([\w.-]+): (.+ -> .+)$/.exec(PARTS.exec(l.text)?.[5] ?? "")).filter(Boolean).map((m) => ({ text: `${m[1]} ${m[2].replace(" -> ", " → ")}`, real: !/\(0 -> 0\b/.test(m[2]) }));
			const found = query ? all.filter((c) => c.text.toLowerCase().includes(query)) : [];
			const changes = (found.length ? found : all.filter((c) => c.real)).slice(0, 6);
			const more = (found.length ? found : all.filter((c) => c.real)).length - changes.length;
			const m = PARTS.exec(burst[0].text);
			const label = query ? `${hits} of ${burst.length} lines match` : `${burst.length} lines`;
			const inner = h("div", { class: "lg-burst-lines", hidden: true }, burst.map(lineEl));
			const toggle = h("button", { type: "button", class: "lg-burst-btn", "aria-expanded": "false", onClick: () => { const open = inner.hidden; inner.hidden = !open; toggle.setAttribute("aria-expanded", String(open)); toggle.textContent = open ? "hide lines" : label; } }, label);
			out.push(h("div", { class: "lg-burst" },
				h("div", { class: "log-line parsed" }, h("span", { class: "ln" }), h("span", { class: "lt-time", title: m[1] }, m[1].split(" ").pop()), h("span", { class: "lt-lvl info" }, "info"),
					h("span", { class: "tx" }, h("span", { class: "lg-burst-t" }, "Configuration reloaded"), changes.length ? [": ", changes.map((c, k) => [k ? ", " : "", marked(c.text)]), more > 0 ? ` and ${more} more` : ""] : h("span", { class: "lg-q" }, ", nothing that matters changed"), toggle)),
				inner));
		}
		return { items: out, matched, bursts, burstHits, shown: shown.length };
	};
	const paintLines = () => {
		if (!logsAvailable) return;
		const shown = lines.filter(visible);
		const result = viewItems(shown);
		if (!result.items.length) {
			view = null;
			const onlyDashboard = lines.length && !query && level === "all" && !showDashboard;
			const reset = () => { query = ""; level = "all"; showDashboard = true; searchEl.querySelector("input").value = ""; for (const b of levelSeg.children) b.setAttribute("aria-pressed", String(b === levelSeg.firstChild)); setUrl(); paintLines(); };
			logBody.replaceChildren(empty({
				icon: "scroll",
				title: !lines.length ? "The log is empty" : onlyDashboard ? "Nothing but this dashboard's own calls" : "No matching lines",
				hint: !lines.length ? "New engine messages appear here as they are written." : onlyDashboard ? "The engine has logged no client traffic or messages in these lines." : query && !withMeta ? "The search looks at messages. Try another level, a shorter search, or include source files and request ids." : "Try another level or a shorter search.",
				action: lines.length ? btn(onlyDashboard ? "Show them anyway" : "Show every line", { cls: "outline", onClick: reset }) : null,
			}));
		} else {
			view = h("div", { class: "log-view lg-view", tabindex: 0, role: "log", "aria-label": "Engine log", onScroll: () => { if (follow && !atBottom()) setFollow(false); } }, result.items);
			logBody.replaceChildren(view);
			if (query) view.querySelector("mark")?.closest(".log-line")?.scrollIntoView({ block: "center" });
			else scrollToEnd();
		}
		lastResult = result;
		paintFoot(result);
	};
	// Keep the newest `limit` lines; returns how many were new.
	const ingest = (entries, replace) => {
		if (replace) { lines = []; nextNumber = 1; }
		let previous = lines.length ? lines[lines.length - 1].rank : 1;
		for (const entry of entries) {
			const text = typeof entry === "string" ? entry : entry.message ?? entry.line ?? JSON.stringify(entry);
			// Skip blank lines and the access lines this page's own polling writes.
			if (!text.trim() || SELF.test(text)) continue;
			previous = rankOf(text, previous);
			lines.push({ number: nextNumber++, text, rank: previous, dash: DASHBOARD.test(accessOf(text)?.path ?? "") });
		}
		if (lines.length > limit) lines = lines.slice(lines.length - limit);
	};
	const turnOnFile = async (button) => {
		button.disabled = true;
		try {
			await engine.patch("config", { observability: { logs: { "logging-to-file": true } } });
			invalidate("config");
			fileSwitch?.setAttribute("aria-checked", "true");
			toast("The engine now writes its log to a file");
			await reloadLogs();
		} catch (e) {
			toast(e.message, "bad");
			button.disabled = false;
		}
	};
	const failed = (e, retry) => {
		lines = [];
		logsAvailable = false;
		view = null;
		showControls(false);
		if (e instanceof ApiError && e.status === 400 && /logging to file disabled/i.test(e.message)) {
			const on = btn("Turn on file logging", { icon: "power", cls: "primary", onClick: () => turnOnFile(on) });
			logBody.replaceChildren(empty({ icon: "scroll", title: "The engine is not keeping a log file", hint: "It only shows its log here while it writes one. Turn it on to see new lines as they happen.", action: on }));
		} else logBody.replaceChildren(errorState(e, retry));
	};
	const reloadLogs = async () => {
		const current = ++revision;
		logsAvailable = false;
		if (!lines.length) logBody.replaceChildren(spinner());
		try {
			const result = await engine.get("observability/logs", { query: { limit } });
			if (disposed || current !== revision) return;
			let entries = Array.isArray(result) ? result : result?.lines ?? result?.logs ?? [];
			if (typeof entries === "string") entries = entries.split("\n");
			cursor = result?.["next-cursor"] ?? "";
			ingest(entries, true);
			loadedAt = Date.now();
			logsAvailable = true;
			showControls(true);
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
				ingest(entries, false);
				if (!view) paintLines();
				else {
					const stick = atBottom();
					lastResult = viewItems(lines.filter(visible));
					view.replaceChildren(...lastResult.items);
					if (stick) scrollToEnd();
					paintFoot(lastResult);
				}
			} else if (lastResult) paintFoot(lastResult);
		} catch (e) {
			if (!disposed && current === revision) { setFollow(false); toast(`Stopped following: ${e.message}`, "bad"); }
		} finally { polling = false; schedule(); }
	};
	function schedule() {
		clearTimeout(timer);
		if (follow && !disposed) timer = setTimeout(poll, FOLLOW_MS);
	}

	// ---------- error reports ----------

	const download = async (name) => {
		try {
			const url = URL.createObjectURL(await fetchReport(name));
			const link = h("a", { href: url, download: name });
			document.body.append(link);
			link.click();
			link.remove();
			setTimeout(() => URL.revokeObjectURL(url), 1000);
		} catch (e) { toast(e.message, "bad"); }
	};
	// A report reads by what went wrong: the status and the provider's (or router's)
	// words lead, then where it went and when; the endpoint and size are details.
	const statusBadge = (status) => (status ? badge(`HTTP ${status}`, Number(status) === 429 ? "warn" : "bad") : null);
	const reportMeta = (f, info) => [f.asked ? f.asked.replace(/^neoproxy\//, "") : null, f.sentModel ? `sent as ${f.provider ? `${f.provider} / ` : ""}${f.sentModel}` : null, f.account, whenLabel(info.at), info.route].filter(Boolean).join(" · ");
	const openDump = (file, info) => {
		const host = h("div", { class: "lg-report" }, spinner());
		const title = h("span", { class: "lg-dump-title" }, h("span", null, "Error report"), h("code", null, info.route));
		dumpSheet = sheet({
			title,
			sub: `${whenLabel(info.at)} · ${info.route}${file.size != null ? ` · ${fmtBytes(file.size)}` : ""}`,
			columns: false,
			keep: ".lg-dumps .li",
			body: host,
			foot: [btn("Download", { icon: "download", cls: "outline sm", onClick: () => download(file.name) })],
			onClose: () => { dumpSheet = null; },
		});
		dumpSheet.el.classList.add("lg-sheet");
		reportFactsOf(file.name).then((f) => title.replaceChildren(statusBadge(f.status) ?? "", h("span", { class: "lg-dump-head" }, reportHeadline(f)))).catch(() => {});
		fetchReport(file.name).then((blob) => blob.text()).then((text) => host.replaceChildren(...reportView(text))).catch((e) => host.replaceChildren(errorState(e, () => openDump(file, info))));
	};
	const dumpRow = ({ file, info }) => {
		const head = h("div", { class: "main lg-dump-main" }, h("span", { class: "lg-q" }, "Reading the report…"));
		const meta = h("div", { class: "sub lg-dump-meta" }, [whenLabel(info.at), info.route].join(" · "));
		reportFactsOf(file.name).then((f) => {
			head.replaceChildren(statusBadge(f.status) ?? "", h("span", { class: "lg-dump-head" }, reportHeadline(f)));
			meta.textContent = reportMeta(f, info);
		}).catch(() => head.replaceChildren(h("code", null, info.route)));
		return h("div", { class: "li clickable", role: "button", tabindex: 0, title: "Read this report",
			onClick: (e) => { if (!e.target.closest(".btn")) openDump(file, info); },
			onKeydown: (e) => { if ((e.key === "Enter" || e.key === " ") && e.target === e.currentTarget) { e.preventDefault(); openDump(file, info); } } },
			h("span", { class: "lg-dump-ic" }, icon("scroll", 14)),
			h("div", { class: "lt" }, head, meta),
			h("span", { class: "lg-ago" }, fmtAgo(info.at)),
			h("span", { class: "lg-size" }, file.size != null ? fmtBytes(file.size) : ""),
			iconBtn("download", `Download ${file.name}`, () => download(file.name), "sm"),
		);
	};
	let showAllDumps = false;
	let dumpFiles = [];
	// `?at=<ms>` (from an Activity request) narrows the list to reports from that moment.
	let near = Number(ctx.query.get("at")) || 0;
	let nearOpened = false;
	const NEAR_MS = 3000;
	const paintDumps = () => {
		if (!dumpFiles.length) {
			dumpBody.replaceChildren(requestLogOn
				? empty({ icon: "scroll", title: "Kept with the full request log", hint: "While every request is logged in full, failures are not saved separately here. Turn that off below to collect error reports again." })
				: empty({ icon: "check", title: "No error reports", hint: "The engine saves one here when an upstream request fails hard." }));
			return;
		}
		const all = dumpFiles.map((file) => ({ file, info: reportInfo(file.name, file.modified == null ? null : file.modified * 1000) })).sort((a, b) => b.info.at - a.info.at);
		const files = near ? all.filter(({ info }) => Math.abs(info.at - near) <= NEAR_MS) : all;
		const shown = showAllDumps || near ? files : files.slice(0, DUMPS_SHOWN);
		const nearBar = near
			? h("div", { class: "lg-near" }, icon("clock", 14), h("span", null, files.length ? `${files.length === 1 ? "The report" : `${files.length} reports`} from ${whenLabel(near)}` : `No report from ${whenLabel(near)}`), h("span", { class: "spacer" }), btn(`Show all ${all.length}`, { cls: "ghost sm", onClick: () => { near = 0; history.replaceState(null, "", location.hash.replace(/([?&])at=\d+&?/, "$1").replace(/[?&]$/, "")); paintDumps(); } }))
			: null;
		dumpBody.replaceChildren(
			nearBar ?? "",
			h("div", { class: "list lg-dumps" }, shown.map(dumpRow)),
			!near && files.length > DUMPS_SHOWN ? h("button", { type: "button", class: "lg-more", onClick: () => { showAllDumps = !showAllDumps; paintDumps(); } }, showAllDumps ? "Show fewer" : `Show all ${files.length}`, icon(showAllDumps ? "chevronUp" : "chevronDown", 13)) : "",
		);
		if (near && !nearOpened) {
			nearOpened = true;
			dumpsCard.scrollIntoView({ block: "start" });
			if (files.length === 1) openDump(files[0].file, files[0].info);
		}
	};
	const reloadDumps = async () => {
		try {
			const files = (await engine.errorLogs())?.files ?? [];
			if (disposed) return;
			dumpFiles = files.map((file) => (typeof file === "string" ? { name: file } : file));
			dumpsCard.querySelector(".card-head .sub").textContent = dumpFiles.length ? `${dumpFiles.length} saved, newest first: what went wrong, where it went, when.` : requestLogOn ? "Paused while every request is logged in full" : "Saved when an upstream request fails hard";
			paintDumps();
		} catch (e) {
			if (!disposed) dumpBody.replaceChildren(errorState(e, reloadDumps));
		}
	};

	// ---------- what gets logged ----------

	const loadSettings = async () => {
		try {
			const config = await engine.config();
			if (disposed) return;
			const obs = config.observability ?? {};
			const row = (label, help, checked, patch, message, after) => {
				let control;
				control = toggle(checked, async (value) => {
					control.disabled = true;
					try {
						await engine.patch("config", { observability: patch(value) });
						invalidate("config");
						toast(`${message} ${value ? "on" : "off"}`);
						await after?.(value);
					} catch (e) { control.setAttribute("aria-checked", String(!value)); toast(e.message, "bad"); }
					finally { control.disabled = false; }
				}, label, { showLabel: false });
				return { el: h("div", { class: "lg-set" }, h("div", { class: "lg-set-t" }, h("div", { class: "lg-set-name" }, label), h("div", { class: "lg-set-help" }, help)), control), control };
			};
			const file = row("Write to a file", "Needed for the log above: the engine shows the lines it has written to its log file.", Boolean(obs.logs?.["logging-to-file"] ?? obs["logging-to-file"]), (v) => ({ logs: { "logging-to-file": v } }), "File logging", () => reloadLogs());
			fileSwitch = file.control;
			requestLogOn = Boolean(obs.logs?.["request-log"]);
			settingsBody.replaceChildren(
				file.el,
				row("Log every request in full", "Saves each request and its answer. While this is on, failures are not saved as separate error reports.", requestLogOn, (v) => ({ logs: { "request-log": v } }), "Full request log", (v) => { requestLogOn = v; return reloadDumps(); }).el,
				row("Debug detail", "Much more output. Turn it on only while chasing a problem.", Boolean(obs.logs?.debug), (v) => ({ logs: { debug: v } }), "Debug logging").el,
			);
			if (requestLogOn) reloadDumps();
		} catch (e) { if (!disposed) settingsBody.replaceChildren(errorState(e, loadSettings)); }
	};

	const dumpsCard = card({ title: "Error reports", sub: " ", flush: true, body: dumpBody, cls: "lg-dumps-card" });
	root.append(
		logCard,
		h("div", { class: "grid c2-1 lg-lower" }, dumpsCard, card({ title: "What gets logged", sub: "Changes apply at once.", flush: true, body: settingsBody, cls: "lg-settings" })),
	);
	showControls(false);
	await Promise.all([reloadLogs(), reloadDumps(), loadSettings()]);
	return () => { disposed = true; clearTimeout(timer); dumpSheet?.close(); };
}
