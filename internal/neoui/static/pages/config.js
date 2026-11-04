// Configuration: the engine's settings as an editable form, and the raw file
// as a second view. Every field saves on its own through the v8 management API
// (`PUT /v8/management/config/<key path>`, `DELETE` to go back to the default),
// guarded by the ETag of the file version the page last read, so two editors
// never silently overwrite each other. Settings the catalogue knows show even
// when the file leaves them out, with their default and a fitting control.

import { h, icon, fmtInt, plural, replace, debounce } from "../lib/dom.js";
import { engine, invalidate, ApiError } from "../lib/client.js";
import { pageHead, card, btn, iconBtn, spinner, errorState, confirm, seg } from "../lib/components.js";
import { codeEditor, findKeyLine } from "../lib/code-editor.js";
import { SECTIONS, KNOWN, LEAF_HINTS, LEAF_ENUMS, LABELS } from "./config-catalog.js";

const SECTION = new Map(SECTIONS.map(([key, title, intro, opts]) => [key, { title, intro, ...opts }]));
const KNOWN_PATHS = Object.keys(KNOWN);

// Values NeoProxy owns; the API rejects changes to them.
const READ_ONLY = new Set(["config-version", "credentials.concurrency.lifecycle-config-revision", "credentials.concurrency.observation-barrier-revision", "plugins.auth-revision"]);
const READ_ONLY_LEAF = new Set(["auth_index"]);

const SECRET = /secret|api-?keys?$|bearer|token|password|passphrase|credential$|private-key/i;
const DURATION = /^(?:0|(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+)$/;
const WORDS = { url: "URL", urls: "URLs", api: "API", tls: "TLS", ttl: "TTL", id: "ID", ids: "IDs", ip: "IP", ips: "IPs", mb: "MB", http: "HTTP", https: "HTTPS", oauth: "OAuth", dns: "DNS", cpa: "CPA", ws: "WebSocket", ui: "UI", json: "JSON", udp: "UDP", ice: "ICE", xai: "xAI", github: "GitHub", openai: "OpenAI", aistudio: "AI Studio", codex: "Codex", claude: "Claude", gemini: "Gemini", vertex: "Vertex", antigravity: "Antigravity", devin: "Devin", gpt: "GPT", redis: "Redis", pprof: "pprof" };
const ITEMS_SHOWN = 20;
const CHIPS_SHOWN = 30;
const UNSET = Symbol("unset");
const reduced = () => matchMedia("(prefers-reduced-motion: reduce)").matches;

const plainPath = (path) => path.filter((s) => typeof s !== "number").join(".");
const pathId = (path) => path.join("\u0001");
const leafOf = (path) => path.filter((s) => typeof s !== "number").at(-1);
const knownSpec = (path) => (path.some((s) => typeof s === "number") ? null : KNOWN[path.join(".")] ?? null);
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

function human(key) {
	if (LABELS[key]) return LABELS[key];
	const words = String(key).split(/[-_ ]+/).filter(Boolean);
	return words.map((w, i) => WORDS[w.toLowerCase()] ?? (i === 0 ? w[0].toUpperCase() + w.slice(1) : w)).join(" ") || String(key);
}

const isScalar = (v) => v === null || ["string", "number", "boolean"].includes(typeof v);
const isGroup = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const isSecretKey = (path) => { const leaf = leafOf(path); return typeof leaf === "string" && SECRET.test(leaf); };
const isReadOnly = (path) => READ_ONLY.has(plainPath(path)) || READ_ONLY_LEAF.has(path.at(-1));

function getIn(obj, path) {
	let v = obj;
	for (const s of path) v = v?.[s];
	return v;
}
function setIn(obj, path, value) {
	let v = obj;
	for (const s of path.slice(0, -1)) {
		if (v[s] === undefined || v[s] === null || typeof v[s] !== "object") v[s] = {};
		v = v[s];
	}
	v[path.at(-1)] = value;
}
function deleteIn(obj, path) {
	const parent = getIn(obj, path.slice(0, -1));
	if (parent && typeof parent === "object") delete parent[path.at(-1)];
}

// Identity first, then plain values, then lists and groups.
const FIRST = ["name", "alias", "id", "enabled", "enable", "base-url", "host", "port"];
function orderedEntries(obj) {
	const tier = ([k, v]) => {
		const f = FIRST.indexOf(k);
		if (f !== -1) return f - 100;
		if (isScalar(v)) return 0;
		if (Array.isArray(v) && v.every(isScalar)) return 1;
		if (Array.isArray(v)) return 3;
		return 2;
	};
	return Object.entries(obj ?? {}).sort((a, b) => tier(a) - tier(b));
}

// The settings under `path`, catalogue settings first (with UNSET for those the file leaves out).
function entriesAt(obj, path) {
	const out = [];
	const seen = new Set();
	if (!path.some((s) => typeof s === "number")) {
		const prefix = path.length ? `${path.join(".")}.` : "";
		for (const p of KNOWN_PATHS) {
			if (!p.startsWith(prefix)) continue;
			const key = p.slice(prefix.length).split(".")[0];
			if (seen.has(key)) continue;
			seen.add(key);
			const value = obj?.[key];
			out.push([key, value === undefined ? (KNOWN[prefix + key] ? UNSET : {}) : value]);
		}
	}
	for (const [k, v] of orderedEntries(obj)) if (!seen.has(k)) out.push([k, v]);
	return out;
}

function errorText(e) {
	const body = e instanceof ApiError && e.body && typeof e.body === "object" ? e.body : null;
	const code = typeof body?.error === "string" ? body.error : "";
	if (code === "read_only_field") return "NeoProxy manages this value itself; it can't be changed here.";
	if (code === "precondition_failed") return "The config file changed after this page loaded it.";
	if (body?.message) return body.message;
	const meaning = {
		invalid_body: "The engine could not parse this. Look for bad indentation, an unclosed bracket or quote, or a stray colon.",
		invalid_json: "The engine could not read this value.",
		config_must_be_object: "The top level must be a mapping of settings, not a list or a single value.",
		invalid_path: "This key sits inside a list or value that can't hold it.",
		write_failed: "The engine could not write the config file. Check that it is writable.",
	}[code];
	return meaning ? `${meaning} (${code})` : e?.message || "Could not save";
}

function maskSecret(value) {
	if (!value) return "";
	const tail = value.length >= 16 ? value.slice(-4) : "";
	return `${"•".repeat(10)}${tail}`;
}

// ---------- durations ----------

const UNIT_MS = { h: 3_600_000, m: 60_000, s: 1000, ms: 1 };
const UNIT_WORDS = { h: "hours", m: "minutes", s: "seconds", ms: "ms" };
function durationMs(text) {
	let total = 0;
	for (const [, n, u] of String(text).matchAll(/(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g)) total += Number(n) * ({ ns: 1e-6, us: 1e-3, "µs": 1e-3, ms: 1, s: 1000, m: 60_000, h: 3_600_000 })[u];
	return total;
}
function splitDuration(text) {
	const ms = durationMs(text);
	if (!ms) return { n: 0, unit: /ms$/.test(text) ? "ms" : (/(h|m|s)$/.exec(text)?.[1] ?? "s") };
	for (const unit of ["h", "m", "s", "ms"]) if (ms % UNIT_MS[unit] === 0) return { n: ms / UNIT_MS[unit], unit };
	return { n: ms, unit: "ms" };
}
function durationWords(text) {
	const { n, unit } = splitDuration(text);
	if (!n) return "0";
	const word = { h: ["hour", "hours"], m: ["minute", "minutes"], s: ["second", "seconds"], ms: ["ms", "ms"] }[unit];
	return `${fmtInt(n)} ${n === 1 ? word[0] : word[1]}`;
}

function bytesWords(n) {
	const units = ["bytes", "KB", "MB", "GB", "TB"];
	let v = n;
	let i = 0;
	while (v >= 1024 && i < units.length - 1 && v % 1024 === 0) { v /= 1024; i++; }
	return i ? `${fmtInt(v)} ${units[i]}` : null;
}
function secondsWords(n) {
	if (n < 60) return null;
	if (n % 3600 === 0) return plural(n / 3600, "hour");
	if (n % 60 === 0) return plural(n / 60, "minute");
	return null;
}

// What a known setting's value reads as, for the reset button's title.
function valueWords(spec, v) {
	if (spec.kind === "bool") return (spec.invert ? !v : v) ? "on" : "off";
	if (spec.kind === "enum") return spec.options.find((o) => same(o.value, v))?.label ?? String(v);
	if (spec.kind === "int" && v === 0 && spec.zero) return spec.zero.toLowerCase();
	if (spec.kind === "int") return `${fmtInt(v)}${spec.unit ? ` ${spec.unit}` : ""}`;
	if (spec.kind === "duration") return durationWords(v);
	if (v === "" || v === undefined) return "empty";
	return String(v);
}

// ---------- the page ----------

export async function render(ctx) {
	const { root } = ctx;
	let data = null;
	let etag = null;
	let format = "yaml";
	let alive = true;
	let chain = Promise.resolve();
	let view = ctx.query?.get("view") === "file" ? "file" : "settings";
	let query = "";
	const open = new Set(); // ids of expanded entries, lists and advanced sections
	const queries = new Map(); // id -> search text for long lists
	let fields = new Map(); // id -> field controller, rebuilt each render
	let uid = 0;

	const reloadButton = btn("Reload", { icon: "refresh", cls: "outline", onClick: () => reloadAll() });
	const viewSwitch = seg([
		{ value: "settings", label: h("span", { class: "cfg-view-l" }, icon("sliders", 14), "Settings") },
		{ value: "file", label: h("span", { class: "cfg-view-l" }, icon("scroll", 14), "File", h("span", { class: "cfg-dirty-dot", title: "Unsaved edits", hidden: true })) },
	], view, (v) => setView(v));
	viewSwitch.classList.add("cfg-view");
	viewSwitch.setAttribute("aria-label", "Show");
	const dirtyDot = viewSwitch.querySelector(".cfg-dirty-dot");
	root.append(pageHead({
		title: "Configuration",
		desc: "The engine's settings, grouped by what they control. Each change saves as soon as you make it; the file view shows the same settings as text.",
		actions: [viewSwitch, reloadButton],
	}));

	// ---------- settings view: section index, search, cards ----------

	const nav = h("nav", { class: "cfg-nav", "aria-label": "Sections" });
	const findBox = h("input", { class: "input", type: "search", placeholder: "Find a setting", "aria-label": "Find a setting", autocomplete: "off", spellcheck: "false" });
	const findNote = h("span", { class: "cfg-find-note", role: "status" });
	const formHost = h("div", { class: "cfg-sections" }, spinner());
	const noMatch = h("div", { class: "cfg-nomatch", hidden: true }, icon("search", 18), h("div", null, "No setting matches. Try another word, or look in the ", h("button", { type: "button", class: "link-text", onClick: () => { const q = findBox.value; setView("file").then(() => { file.find(q); }); } }, "file"), "."));
	const settingsEl = h("div", { class: "cfg" },
		nav,
		h("div", { class: "cfg-main" },
			h("div", { class: "cfg-findbar" }, h("div", { class: "search cfg-find" }, icon("search", 15), findBox), findNote),
			formHost,
			noMatch,
		),
	);
	const fileEl = h("div", { class: "cfg-file-view" });
	const viewHost = h("div");
	root.append(viewHost);

	findBox.addEventListener("input", debounce(() => { query = findBox.value.trim().toLowerCase(); applyFilter(); }, 90));
	findBox.addEventListener("search", () => { if (!findBox.value) { query = ""; applyFilter(); } });
	findBox.addEventListener("keydown", (e) => {
		if (e.key === "Enter") {
			e.preventDefault();
			const first = formHost.querySelector(".cfg-row:not([hidden]) :is(input,select,textarea,button), .set-list:not([hidden]) :is(input,button,summary)");
			first?.focus({ preventScroll: true });
			if (first) scrollToEl(first.closest(".cfg-row, .set-list") ?? first, 120);
		}
	});

	async function setView(next) {
		view = next;
		for (const b of viewSwitch.children) b.setAttribute("aria-pressed", String(b === viewSwitch.children[next === "file" ? 1 : 0]));
		history.replaceState(null, "", next === "file" ? "#/config?view=file" : "#/config");
		viewHost.replaceChildren(next === "file" ? fileEl : settingsEl);
		if (next === "file") await file.show();
	}

	async function fetchConfig() {
		const res = await engine.raw("GET", "config");
		etag = res.headers.get("ETag");
		const f = res.headers.get("X-Config-Format");
		if (f === "toml" || f === "yaml") format = f;
		data = await res.json();
		invalidate("config");
	}

	// ---------- saving ----------

	// Saves one value. Paths address YAML keys only, so a change inside a list
	// rewrites that whole list, as the API expects. Saves run one at a time so
	// each carries the version the previous one produced.
	function send(method, path, value) {
		const run = async () => {
			const next = structuredClone(data);
			const cut = path.findIndex((s) => typeof s === "number");
			const keyPath = cut === -1 ? path : path.slice(0, cut);
			if (method === "DELETE" && cut === -1) deleteIn(next, path);
			else setIn(next, path, value);
			const headers = etag ? { "If-Match": etag } : {};
			const url = `config/${keyPath.map(encodeURIComponent).join("/")}`;
			const res = method === "DELETE" && cut === -1
				? await engine.raw("DELETE", url, { headers })
				: await engine.raw("PUT", url, { json: getIn(next, keyPath), headers });
			data = next;
			etag = res.headers.get("ETag");
			if (!etag) await fetchConfig();
			invalidate("config");
			file.formSaved();
		};
		const p = chain.then(run);
		chain = p.catch(() => {});
		return p;
	}

	// A field controller: owns the inline "Saved" tick and the error line.
	function shell(path) {
		const state = h("span", { class: "set-state", "aria-live": "polite" });
		const err = h("div", { class: "set-err", role: "alert", hidden: true });
		let timer = 0;
		const ctl = {
			path,
			state,
			err,
			busy() { clearTimeout(timer); state.className = "set-state busy"; state.replaceChildren("Saving…"); },
			ok() {
				clearTimeout(timer);
				err.hidden = true;
				state.className = "set-state ok";
				state.replaceChildren(icon("check", 13), "Saved");
				timer = setTimeout(() => { state.className = "set-state"; state.replaceChildren(); }, 2400);
			},
			fail(message) {
				clearTimeout(timer);
				state.className = "set-state";
				state.replaceChildren();
				err.replaceChildren(icon("info", 13), h("span", null, message));
				err.hidden = false;
			},
			clear() { err.hidden = true; },
			async run(method, value) {
				ctl.busy();
				try {
					await send(method, path, value);
					ctl.ok();
					return true;
				} catch (e) {
					if (e instanceof ApiError && e.status === 412) {
						// Someone else saved first: show their version, keep the explanation on this field.
						await refreshForm();
						fields.get(pathId(path))?.fail("The config file changed after this page loaded it. This field now shows the saved value; make your change again.");
						return false;
					}
					ctl.fail(errorText(e));
					return false;
				}
			},
			save(value) { return ctl.run("PUT", value); },
			reset() { return ctl.run("DELETE"); },
		};
		fields.set(pathId(path), ctl);
		return ctl;
	}

	// ---------- controls ----------

	// Text-like inputs: Enter (or leaving the field) saves, Esc puts back the saved value.
	function wireText(el, ctl, { saved, parse, onSaved }) {
		let last = saved;
		el.value = saved;
		const save = async () => {
			if (el.value === last) return;
			const parsed = parse(el.value);
			if (parsed.error) return ctl.fail(parsed.error);
			const before = last;
			last = el.value;
			if (!(await ctl.save(parsed.value))) last = before;
			else onSaved?.(parsed.value);
		};
		el.addEventListener("keydown", (e) => {
			if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); save(); }
			else if (e.key === "Escape") { el.value = last; ctl.clear(); el.dispatchEvent(new Event("revert")); }
		});
		el.addEventListener("change", save);
		el.addEventListener("input", () => ctl.clear());
		return el;
	}

	// A switch that keeps focus while it saves, so Space can toggle it again.
	function switchControl(checked, onToggle, labelId) {
		const words = h("span", { class: "cfg-onoff", "aria-hidden": "true" }, checked ? "On" : "Off");
		const el = h("button", { type: "button", class: "switch", role: "switch", "aria-checked": String(checked), "aria-labelledby": labelId }, h("span", { class: "track" }));
		const set = (v) => { el.setAttribute("aria-checked", String(v)); words.textContent = v ? "On" : "Off"; };
		let pending = false;
		el.addEventListener("click", async () => {
			if (pending) return;
			const next = el.getAttribute("aria-checked") !== "true";
			set(next);
			pending = true;
			el.setAttribute("aria-busy", "true");
			const ok = await onToggle(next);
			pending = false;
			el.removeAttribute("aria-busy");
			if (!ok) set(!next);
		});
		return h("span", { class: "cfg-switch", onClick: (e) => { if (e.target === words) el.click(); } }, el, words);
	}

	function secretControl(value, ctl, labelId) {
		const wrap = h("div", { class: "set-secret" });
		let shown = false;
		const view_ = () => {
			const text = h("code", { class: `set-secret-value ${value ? "" : "unset"}`, "aria-labelledby": labelId }, value ? (shown ? value : maskSecret(value)) : "Not set");
			replace(wrap, text,
				value ? btn(shown ? "Hide" : "Show", { icon: shown ? "eyeOff" : "eye", cls: "ghost sm", onClick: () => { shown = !shown; view_(); } }) : null,
				btn(value ? "Change…" : "Set…", { cls: "outline sm", onClick: edit }));
		};
		const edit = () => {
			const field = h("input", { class: "input mono", type: "password", autocomplete: "off", spellcheck: "false", placeholder: "New value", "aria-labelledby": labelId });
			const save = async () => {
				if (!field.value) return ctl.fail("Enter the new value, or press Esc to keep the current one.");
				if (await ctl.save(field.value)) { value = field.value; shown = false; view_(); wrap.querySelector("button:last-child")?.focus(); }
			};
			const cancel = () => { ctl.clear(); view_(); wrap.querySelector("button:last-child")?.focus(); };
			field.addEventListener("keydown", (e) => {
				if (e.key === "Enter") { e.preventDefault(); save(); }
				else if (e.key === "Escape") { e.stopPropagation(); cancel(); }
			});
			wrap.replaceChildren(field, btn("Save", { cls: "primary sm", onClick: save }), btn("Cancel", { cls: "ghost sm", onClick: cancel }));
			field.focus();
		};
		view_();
		return wrap;
	}

	function parseLoose(text) {
		const t = text.trim();
		if (t === "") return { value: null };
		if (/^(true|false)$/.test(t)) return { value: t === "true" };
		if (/^-?\d+(\.\d+)?$/.test(t)) return { value: Number(t) };
		return { value: text };
	}

	// A number with its unit, and a word for what 0 (or less) means.
	function numberControl(spec, value, ctl, labelId, onSaved) {
		const el = h("input", { class: "input mono cfg-num", type: "number", step: spec.any ? "any" : 1, min: spec.min, max: spec.max, inputmode: "numeric", "aria-labelledby": labelId });
		const note = h("span", { class: "cfg-note" });
		const paint = (v) => {
			const n = Number(v);
			let word = "";
			if (v !== "" && Number.isFinite(n)) {
				if (n === 0 && spec.zero) word = spec.zero;
				else if (n < 0 && spec.negative) word = spec.negative;
				else if (spec.unit === "bytes") word = bytesWords(n) ?? "";
				else if (spec.unit === "seconds") word = secondsWords(n) ?? "";
			}
			note.textContent = word;
			note.hidden = !word;
		};
		wireText(el, ctl, {
			saved: value === undefined || value === null ? "" : String(value),
			parse: (t) => {
				const n = Number(t);
				if (t.trim() === "" || !(spec.any ? Number.isFinite(n) : Number.isInteger(n))) return { error: spec.any ? "Enter a number." : "Enter a whole number." };
				if (spec.min !== undefined && n < spec.min) return { error: `The smallest value is ${fmtInt(spec.min)}.` };
				if (spec.max !== undefined && n > spec.max) return { error: `The largest value is ${fmtInt(spec.max)}.` };
				return { value: n };
			},
			onSaved,
		});
		el.addEventListener("input", () => paint(el.value));
		el.addEventListener("revert", () => paint(el.value));
		paint(el.value);
		return h("div", { class: "cfg-numwrap" }, el, spec.unit ? h("span", { class: "cfg-unit" }, spec.unit) : null, note);
	}

	// A duration as a number and a unit, so nobody has to know Go's duration syntax.
	function durationControl(value, ctl, labelId, onSaved) {
		const start = splitDuration(value || "0s");
		const num = h("input", { class: "input mono cfg-num", type: "number", min: 0, step: "any", inputmode: "decimal", value: String(start.n), "aria-labelledby": labelId });
		const unit = h("select", { class: "select cfg-unitsel", "aria-label": "Unit" }, Object.keys(UNIT_MS).map((u) => h("option", { value: u, selected: u === start.unit }, UNIT_WORDS[u])));
		let last = `${start.n}${start.unit}`;
		const save = async () => {
			const n = Number(num.value);
			if (num.value.trim() === "" || !Number.isFinite(n) || n < 0) return ctl.fail("Enter a length of time, such as 30 seconds or 2 hours.");
			const next = `${n}${unit.value}`;
			if (next === last) return;
			const before = last;
			last = next;
			if (!(await ctl.save(next))) last = before;
			else onSaved?.(next);
		};
		num.addEventListener("keydown", (e) => {
			if (e.key === "Enter") { e.preventDefault(); save(); }
			else if (e.key === "Escape") { const s = splitDuration(last); num.value = String(s.n); unit.value = s.unit; ctl.clear(); }
		});
		num.addEventListener("change", save);
		num.addEventListener("input", () => ctl.clear());
		unit.addEventListener("change", save);
		return h("div", { class: "cfg-numwrap" }, num, unit);
	}

	// Choices with a note read best as small cards: a radio group, so arrow keys move between them.
	function choiceControl(spec, value, ctl, labelId, onSaved) {
		const name = `cfg-opt-${++uid}`;
		let last = value;
		const wrap = h("div", { class: "opt-cards cfg-opts", role: "radiogroup", "aria-labelledby": labelId });
		const options = value === undefined || spec.options.some((o) => same(o.value, value)) ? spec.options : [{ value, label: String(value), note: "Set in the file; not one of the usual choices." }, ...spec.options];
		for (const o of options) {
			const input = h("input", { type: "radio", name, checked: same(o.value, value) });
			input.addEventListener("change", async () => {
				if (!input.checked) return;
				if (await ctl.save(o.value)) { last = o.value; onSaved?.(o.value); }
				else for (const r of wrap.querySelectorAll("input")) r.checked = r === inputs[options.findIndex((x) => same(x.value, last))];
			});
			wrap.append(h("label", { class: "opt-card" },
				input,
				h("span", { class: "opt-label" }, o.label, same(o.value, spec.default) ? h("span", { class: "opt-tag" }, "default") : null),
				o.note ? h("span", { class: "opt-note" }, o.note) : null,
			));
		}
		const inputs = [...wrap.querySelectorAll("input")];
		return wrap;
	}

	// A list of short values as chips you can remove, plus an input to add one.
	function chipList(path, list, ctl, labelId, { secret = isSecretKey(path), placeholder, add: addText, suggest = [], onSaved } = {}) {
		const numeric = list.length > 0 && list.every((v) => typeof v === "number");
		const id = pathId(path);
		let shown = false;
		let focusAdd = false;
		const wrap = h("div", { class: "set-chips", role: "group", "aria-labelledby": labelId });
		const draw = () => {
			const all = open.has(id) || list.length <= CHIPS_SHOWN;
			const visible = all ? list : list.slice(0, CHIPS_SHOWN);
			const chips = visible.map((v, i) => h("span", { class: "set-chip" },
				h("span", { class: "mono" }, secret && !shown ? maskSecret(String(v)) : String(v)),
				h("button", { type: "button", class: "set-chip-x", title: `Remove ${secret ? "this key" : v}`, "aria-label": `Remove ${secret ? `key ${i + 1}` : v}`, onClick: () => change(list.filter((_, j) => j !== i)) }, icon("x", 12)),
			));
			const add = h("input", { class: "input mono set-chip-add", type: secret ? "password" : "text", autocomplete: "off", spellcheck: "false", placeholder: addText ?? (placeholder ? `Add, e.g. ${placeholder}` : list.length ? "Add…" : "Add the first one…"), "aria-label": `Add to ${human(leafOf(path))}` });
			add.addEventListener("keydown", (e) => {
				if (e.key === "Enter") {
					e.preventDefault();
					const v = add.value.trim();
					if (!v) return;
					if (list.some((x) => String(x) === v)) return ctl.fail("That value is already in the list.");
					change([...list, numeric && Number.isFinite(Number(v)) ? Number(v) : v]);
				} else if (e.key === "Escape") { add.value = ""; ctl.clear(); }
			});
			const missing = suggest.filter((s) => !list.includes(s));
			replace(wrap,
				...chips,
				!all ? btn(`Show all ${fmtInt(list.length)}`, { cls: "ghost sm", onClick: () => { open.add(id); draw(); } }) : null,
				secret && list.length ? btn(shown ? "Hide" : "Show", { icon: shown ? "eyeOff" : "eye", cls: "ghost sm", onClick: () => { shown = !shown; draw(); } }) : null,
				add,
				...missing.map((s) => h("button", { type: "button", class: "cfg-suggest", title: `Add ${s}`, onClick: () => change([...list, s]) }, icon("plus", 11), s)),
			);
			if (focusAdd) { add.focus(); focusAdd = false; }
		};
		const change = async (next) => {
			if (await ctl.save(next)) { list = next; focusAdd = true; draw(); onSaved?.(next); }
		};
		draw();
		return wrap;
	}

	// One editable scalar the catalogue doesn't know. Returns { control, hint }.
	function scalarControl(path, value, ctl, labelId) {
		if (isReadOnly(path)) return { control: h("code", { class: "set-ro", "aria-labelledby": labelId }, value === null ? "null" : String(value)), hint: "Managed by NeoProxy." };
		if (typeof value === "boolean") return { control: switchControl(value, (v) => ctl.save(v), labelId) };
		if (typeof value === "number") return { control: numberControl({ any: true }, value, ctl, labelId) };
		if (value === null) {
			const el = h("input", { class: "input mono", placeholder: "Not set", "aria-labelledby": labelId });
			return { control: wireText(el, ctl, { saved: "", parse: parseLoose }), hint: "Not set: the built-in default applies." };
		}
		if (isSecretKey(path)) return { control: secretControl(value, ctl, labelId) };
		const choices = LEAF_ENUMS[leafOf(path)];
		if (choices) {
			const options = choices.includes(value) ? choices : [value, ...choices];
			const el = h("select", { class: "select set-select", "aria-labelledby": labelId }, options.map((o) => h("option", { value: o, selected: o === value }, o || "Default")));
			let last = value;
			el.addEventListener("change", async () => {
				const next = el.value;
				if (!(await ctl.save(next))) el.value = last;
				else last = next;
			});
			el.addEventListener("keydown", (e) => { if (e.key === "Escape") { el.value = last; ctl.clear(); } });
			return { control: el };
		}
		if (DURATION.test(value) && value !== "") return { control: durationControl(value, ctl, labelId) };
		if (value.length > 80 || value.includes("\n")) {
			const el = h("textarea", { class: "textarea mono set-long", spellcheck: "false", rows: String(Math.min(8, Math.max(2, Math.ceil(value.length / 70) + value.split("\n").length - 1))), "aria-labelledby": labelId });
			return { control: wireText(el, ctl, { saved: value, parse: (t) => ({ value: t }) }), hint: "Enter saves; Shift+Enter starts a new line." };
		}
		const el = h("input", { class: `input set-text ${/^[\w./:@$~-]*$/.test(value) ? "mono" : ""}`, spellcheck: "false", "aria-labelledby": labelId });
		return { control: wireText(el, ctl, { saved: value, parse: (t) => ({ value: t }) }) };
	}

	// ---------- rows ----------

	function labelBlock(path, key, labelId, { label, hint, restart } = {}) {
		const name = label ?? (typeof key === "number" ? `Entry ${key + 1}` : human(key));
		const raw = typeof key === "string" && name.toLowerCase() !== key.replace(/[-_]/g, " ").toLowerCase() ? key : null;
		return h("div", { class: "cfg-label" },
			h("div", { class: "cfg-name-line" },
				h("span", { class: "cfg-name", id: labelId }, name),
				restart ? h("span", { class: "cfg-tag", title: "The engine reads this when it starts" }, "after restart") : null,
			),
			hint ? h("p", { class: "cfg-hint" }, hint) : null,
			raw ? h("code", { class: "cfg-key" }, raw) : null,
		);
	}

	const findText = (...parts) => parts.filter((p) => p !== undefined && p !== null && p !== UNSET).map((p) => (typeof p === "object" ? "" : String(p))).join(" ").toLowerCase();

	// A setting the catalogue knows: its control shows the default until it is set,
	// a set value that differs from the default is marked, and reset deletes the key.
	function knownRow(path, key, spec, stored) {
		const ctl = shell(path);
		const labelId = `cfg-l-${++uid}`;
		let isSet = stored !== UNSET;
		let value = isSet ? stored : spec.default;
		const row = h("div", { class: `cfg-row ${spec.kind === "enum" ? "choice" : ""}`, dataset: { path: path.join("."), find: findText(spec.label, key, spec.hint, path.join(".")) } });
		const tools = h("span", { class: "cfg-tools" });
		const paintChrome = () => {
			const changed = isSet && spec.default !== undefined && !same(value, spec.default);
			row.classList.toggle("on", changed);
			tools.replaceChildren(...(changed
				? [h("button", { type: "button", class: "cfg-reset", title: `Back to the default: ${valueWords(spec, spec.default)}`, "aria-label": `Reset ${spec.label} to the default, ${valueWords(spec, spec.default)}`, onClick: reset }, icon("refresh", 12), "Reset")]
				: []));
			paintCounts();
		};
		const saved = (v) => { isSet = true; value = v; paintChrome(); };
		const reset = async () => {
			if (!(await ctl.reset())) return;
			isSet = false;
			value = spec.default;
			const next = knownRow(path, key, spec, UNSET);
			row.replaceWith(next);
			fields.get(pathId(path))?.ok();
			next.querySelector(".cfg-control :is(input,button,select,textarea)")?.focus();
		};
		let control;
		switch (spec.kind) {
			case "bool": {
				const shown = spec.invert ? !value : Boolean(value);
				control = switchControl(shown, async (v) => { const next = spec.invert ? !v : v; const ok = await ctl.save(next); if (ok) saved(next); return ok; }, labelId);
				break;
			}
			case "int": control = numberControl(spec, value, ctl, labelId, saved); break;
			case "duration": control = durationControl(value, ctl, labelId, saved); break;
			case "enum": control = choiceControl(spec, value, ctl, labelId, saved); break;
			case "list": control = chipList(path, Array.isArray(value) ? value : [], ctl, labelId, { secret: Boolean(spec.secret), placeholder: spec.placeholder, add: spec.add, suggest: spec.suggest, onSaved: saved }); break;
			case "secret": control = secretControl(typeof value === "string" ? value : "", ctl, labelId); break;
			default: {
				const el = h("input", { class: `input set-text ${spec.mono ? "mono" : ""}`, spellcheck: "false", placeholder: spec.placeholder ?? "", "aria-labelledby": labelId });
				control = wireText(el, ctl, { saved: value ?? "", parse: (t) => ({ value: t.trim() }), onSaved: saved });
			}
		}
		const label = labelBlock(path, key, labelId, spec);
		label.querySelector(".cfg-name-line").append(tools);
		row.append(label, h("div", { class: "cfg-control" }, h("div", { class: "cfg-line" }, control, ctl.state), ctl.err));
		paintChrome();
		return row;
	}

	function scalarRow(path, key, value) {
		const ctl = shell(path);
		const labelId = `cfg-l-${++uid}`;
		const { control, hint } = scalarControl(path, value, ctl, labelId);
		const help = LEAF_HINTS[leafOf(path)] ?? hint;
		return h("div", { class: "cfg-row", dataset: { path: plainPath(path), find: findText(human(key), key, help, value) } },
			labelBlock(path, key, labelId, { hint: help }),
			h("div", { class: "cfg-control" }, h("div", { class: "cfg-line" }, control, ctl.state), ctl.err),
		);
	}

	function chipRow(path, key, list) {
		const ctl = shell(path);
		const labelId = `cfg-l-${++uid}`;
		const help = LEAF_HINTS[leafOf(path)];
		return h("div", { class: "cfg-row", dataset: { path: plainPath(path), find: findText(human(key), key, help) } },
			labelBlock(path, key, labelId, { hint: help }),
			h("div", { class: "cfg-control" }, h("div", { class: "cfg-line" }, chipList(path, list, ctl, labelId), ctl.state), ctl.err),
		);
	}

	const editLink = (path, label = "Edit in file") => btn(label, { icon: "scroll", cls: "ghost sm set-file-link", onClick: () => openFileAt(path) });

	function entry(path, key, value) {
		const spec = knownSpec(path);
		if (spec && spec.kind !== "group" && (value === UNSET || !isGroup(value))) return knownRow(path, key, spec, value);
		if (isScalar(value)) return scalarRow(path, key, value);
		if (Array.isArray(value)) return value.every(isScalar) ? chipRow(path, key, value) : listBlock(path, key, value);
		return null;
	}

	// The settings under one mapping: plain rows in one hairline list, then each
	// group as a titled block of its own.
	function blocksFor(obj, path, trail = []) {
		const rows = [];
		const groups = [];
		for (const [key, value] of entriesAt(obj, path)) {
			const p = [...path, key];
			if (isGroup(value) && !knownSpec(p)) groups.push([key, value]);
			else { const el = entry(p, key, value); if (el) rows.push(el); }
		}
		const out = [];
		if (rows.length) out.push(h("div", { class: "cfg-list" }, rows));
		for (const [key, value] of groups) out.push(...groupBlock([...path, key], key, value, [...trail, human(key)]));
		if (!out.length) out.push(h("p", { class: "set-empty" }, "Nothing set here yet. ", editLink(path, "Add in the file")));
		return out;
	}

	// A group holding only other groups adds no heading of its own: its children
	// already carry its name in their trail.
	function groupBlock(path, key, obj, trail) {
		const inner = blocksFor(obj, path, trail);
		if (inner.length && inner.every((el) => el.classList.contains("cfg-group"))) return inner;
		return [h("div", { class: "cfg-group", dataset: { path: plainPath(path) } },
			h("div", { class: "cfg-group-head" },
				h("h3", { class: "cfg-group-title" }, trail.map((t, i) => [i ? h("span", { class: "cfg-sep", "aria-hidden": "true" }, "›") : null, h("span", null, t)])),
				iconBtn("scroll", `Edit ${trail.join(" › ")} in the file`, () => openFileAt(path), "ghost sm set-file-link"),
			),
			inner,
		)];
	}

	// A flat list of small objects (models, keys, error rules) reads best as a table.
	function flatColumns(list) {
		const cols = [];
		for (const item of list) {
			if (!item || typeof item !== "object" || Array.isArray(item)) return null;
			for (const [k, v] of Object.entries(item)) {
				if (!(isScalar(v) || (Array.isArray(v) && v.every((x) => typeof x === "string")))) return null;
				if (!cols.includes(k)) cols.push(k);
			}
		}
		if (cols.length > 6) return null;
		return cols.sort((a, b) => {
			const fa = FIRST.indexOf(a), fb = FIRST.indexOf(b);
			return (fa === -1 ? 50 : fa) - (fb === -1 ? 50 : fb) || (READ_ONLY_LEAF.has(a) ? 1 : 0) - (READ_ONLY_LEAF.has(b) ? 1 : 0);
		});
	}

	function sample(list, key) {
		for (const item of list) if (item[key] !== undefined && item[key] !== null) return item[key];
		return "";
	}

	function cellControl(path, value, kind, ctl, labelId) {
		if (Array.isArray(value) || (value === undefined && Array.isArray(kind))) return chipList(path, value ?? [], ctl, labelId);
		if (value === undefined) {
			const el = h("input", { class: "input mono sm", placeholder: "—", "aria-labelledby": labelId });
			return wireText(el, ctl, { saved: "", parse: (t) => (t.trim() === "" ? { error: "Enter a value." } : typeof kind === "number" ? { value: Number(t) } : { value: t }) });
		}
		return scalarControl(path, value, ctl, labelId).control;
	}

	function tableRows(path, list, cols, indices) {
		const head = h("div", { class: "set-trow set-thead", style: { "--cols": cols.length } }, cols.map((c) => h("span", { class: "set-th" }, human(c))), h("span"));
		const rows = indices.map((i) => {
			const item = list[i];
			const errs = h("div", { class: "set-terrs" });
			const states = h("span", { class: "set-tstate" });
			const cells = cols.map((c) => {
				const p = [...path, i, c];
				const ctl = shell(p);
				const labelId = `cfg-l-${++uid}`;
				errs.append(ctl.err);
				states.append(ctl.state);
				return h("div", { class: "set-td", dataset: { path: plainPath(p) } },
					h("span", { class: "set-td-label", id: labelId }, `${human(c)}, entry ${i + 1}`),
					cellControl(p, item[c], sample(list, c), ctl, labelId),
				);
			});
			return h("div", { class: "set-trow-wrap" }, h("div", { class: "set-trow", style: { "--cols": cols.length } }, cells, states), errs);
		});
		return h("div", { class: "set-table" }, head, rows);
	}

	function itemTitle(item, i) {
		const name = item.name ?? item.alias ?? item.id ?? item.label;
		const sub = [];
		if (name !== undefined && item["base-url"]) sub.push(item["base-url"]);
		for (const [k, v] of Object.entries(item)) if (Array.isArray(v) && v.length) sub.push(plural(v.length, human(k).toLowerCase().replace(/s$/, ""), human(k).toLowerCase()));
		return { title: name !== undefined ? String(name) : item["base-url"] ?? `Entry ${i + 1}`, sub: sub.join(" · ") };
	}

	function itemBlocks(path, list, indices) {
		return indices.map((i) => {
			const item = list[i];
			const p = [...path, i];
			const id = pathId(p);
			if (!item || typeof item !== "object" || Array.isArray(item)) return entry(p, i, item);
			const { title, sub } = itemTitle(item, i);
			const body = h("div", { class: "set-item-body" });
			const det = h("details", { class: "set-item", open: open.has(id) },
				h("summary", null, icon("chevronRight", 14), h("span", { class: "set-item-title mono" }, title), sub ? h("span", { class: "set-item-sub" }, sub) : null),
				body,
			);
			const fill = () => { if (!body.childElementCount) body.append(h("div", { class: "cfg-item-tools" }, editLink(p, "Edit this entry in the file")), ...blocksFor(item, p, [title])); };
			if (det.open) fill();
			det.addEventListener("toggle", () => { if (det.open) { open.add(id); fill(); } else open.delete(id); });
			return det;
		});
	}

	function matches(item, q) {
		if (isScalar(item)) return item !== null && String(item).toLowerCase().includes(q);
		if (Array.isArray(item)) return item.some((x) => matches(x, q));
		return Object.entries(item).some(([k, v]) => !(SECRET.test(k) && typeof v === "string") && matches(v, q));
	}

	function listBlock(path, key, list) {
		const id = pathId(path);
		const cols = flatColumns(list);
		const large = list.length > ITEMS_SHOWN;
		const host = h("div", { class: "set-list-body" });
		const count = h("span", { class: "set-count" });
		const draw = () => {
			const q = (queries.get(id) ?? "").trim().toLowerCase();
			let indices = list.map((_, i) => i);
			let note = null;
			if (large) {
				if (!open.has(id) && !q) { host.replaceChildren(); count.textContent = ""; return; }
				if (q) indices = indices.filter((i) => matches(list[i], q));
				const total = indices.length;
				const cap = q ? 50 : open.has(`${id}\u0002all`) ? total : ITEMS_SHOWN;
				indices = indices.slice(0, cap);
				count.textContent = q ? `${plural(total, "match", "matches")}` : "";
				if (total > indices.length) note = h("div", { class: "set-more" }, `Showing ${fmtInt(indices.length)} of ${fmtInt(total)}. `, q ? "Narrow the search to see the rest." : btn(`Show all ${fmtInt(total)}`, { cls: "ghost sm", onClick: () => { open.add(`${id}\u0002all`); draw(); } }));
				if (!total) { host.replaceChildren(h("p", { class: "set-empty" }, `No entry mentions “${q}”.`)); return; }
			}
			replace(host, cols ? tableRows(path, list, cols, indices) : h("div", { class: "set-items" }, itemBlocks(path, list, indices)), note);
		};
		const toggleBtn = large ? btn(open.has(id) ? "Hide" : `Show first ${ITEMS_SHOWN}`, { cls: "outline sm", onClick: () => {
			if (open.has(id)) open.delete(id); else open.add(id);
			toggleBtn.lastChild.textContent = open.has(id) ? "Hide" : `Show first ${ITEMS_SHOWN}`;
			draw();
		} }) : null;
		const finder = large ? h("input", { class: "input sm set-list-search", type: "text", placeholder: `Search ${fmtInt(list.length)} entries`, value: queries.get(id) ?? "", "aria-label": `Search ${human(key)}`, onInput: (e) => { queries.set(id, e.target.value); draw(); } }) : null;
		const hint = LEAF_HINTS[leafOf(path)];
		const block = h("div", { class: "set-list", dataset: { path: plainPath(path), find: findText(human(key), key, hint) } },
			h("div", { class: "set-list-head" },
				h("div", { class: "cfg-label" },
					h("div", { class: "cfg-name-line" }, h("span", { class: "cfg-name" }, typeof key === "number" ? `Entry ${key + 1}` : human(key)), h("span", { class: "set-count-total" }, plural(list.length, "entry", "entries"))),
					hint ? h("p", { class: "cfg-hint" }, hint) : null,
				),
				h("div", { class: "set-list-tools" }, finder, count, toggleBtn, editLink(path)),
			),
			host,
		);
		draw();
		return block;
	}

	// ---------- sections ----------

	let sectionKeys = [];
	function renderForm() {
		fields = new Map();
		const keys = Object.keys(data);
		const known = SECTIONS.map(([k]) => k).filter((k) => keys.includes(k) || KNOWN_PATHS.some((p) => p.startsWith(`${k}.`)));
		const nested = keys.filter((k) => !SECTION.has(k) && isGroup(data[k])).sort();
		const loose = keys.filter((k) => !SECTION.has(k) && !nested.includes(k) && k !== "config-version");
		sectionKeys = [...known, ...nested];
		const cards = [];
		for (const key of sectionKeys) {
			const meta = SECTION.get(key) ?? { title: human(key), intro: "Settings this page has no description for yet; they show as the file has them." };
			const value = data[key];
			const body = h("div", { class: "cfg-body" }, Array.isArray(value) ? h("div", { class: "cfg-list" }, entry([key], key, value)) : blocksFor(value ?? {}, [key]));
			const el = card({
				title: h("span", { class: "set-title" }, meta.title, meta.title.toLowerCase() !== key.replace(/-/g, " ") ? h("code", { class: "set-key" }, key) : null),
				sub: meta.intro,
				actions: [editLink([key])],
				cls: "set-card cfg-card",
				body: meta.advanced ? advancedBody(key, body) : body,
				rise: false,
			});
			el.id = `cfg-sec-${key}`;
			el.dataset.section = key;
			cards.push(el);
		}
		if (loose.length) {
			const el = card({ title: "Other settings", sub: "Top-level settings outside any group.", cls: "set-card cfg-card", rise: false, body: h("div", { class: "cfg-body" }, h("div", { class: "cfg-list" }, loose.map((k) => entry([k], k, data[k])))) });
			el.id = "cfg-sec-other";
			el.dataset.section = "other";
			cards.push(el);
			sectionKeys.push("other");
		}
		formHost.replaceChildren(...cards);
		nav.replaceChildren(h("div", { class: "cfg-nav-title" }, "Sections"), ...sectionKeys.map((k) => h("a", {
			class: "cfg-nav-link", href: `#/config`, dataset: { section: k },
			onClick: (e) => { e.preventDefault(); jumpTo(k); },
		}, h("span", { class: "cfg-nav-name" }, k === "other" ? "Other" : SECTION.get(k)?.title ?? human(k)), h("span", { class: "cfg-nav-n" }))));
		paintCounts();
		applyFilter();
		watchSections();
	}

	function advancedBody(key, body) {
		const id = `adv:${key}`;
		const n = body.querySelectorAll(".cfg-row, .set-list").length;
		const wrap = h("div", { class: "cfg-adv" });
		const paint = () => {
			const shown = open.has(id) || Boolean(query);
			body.hidden = !shown;
			bar.hidden = Boolean(query);
			toggleBtn.replaceChildren(icon("chevronDown", 14), shown ? "Hide these settings" : `Show ${plural(n, "setting")}`);
			toggleBtn.setAttribute("aria-expanded", String(shown));
		};
		const toggleBtn = btn("", { cls: "outline sm cfg-adv-btn", onClick: () => { if (open.has(id)) open.delete(id); else open.add(id); paint(); } });
		const bar = h("div", { class: "cfg-adv-bar" }, h("span", { class: "cfg-hint" }, "Change these only when the engine's log or support asks you to."), toggleBtn);
		wrap.append(bar, body);
		wrap.repaint = paint;
		paint();
		return wrap;
	}

	// Scroll the page's own scroller; scrollIntoView would also nudge the fixed shell.
	function scrollToEl(el, gap = 0) {
		const scroller = root.closest(".page") ?? root;
		const top = el.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop - gap;
		scroller.scrollTo({ top: Math.max(0, top), behavior: reduced() ? "auto" : "smooth" });
	}

	function jumpTo(key) {
		const el = formHost.querySelector(`[data-section="${key}"]`);
		if (!el) return;
		scrollToEl(el, parseFloat(getComputedStyle(el).scrollMarginTop) || 0);
		const title = el.querySelector(".card-head .title");
		if (title) { title.tabIndex = -1; title.focus({ preventScroll: true }); }
		markNav(key);
	}

	function markNav(key) {
		for (const a of nav.querySelectorAll(".cfg-nav-link")) {
			if (a.dataset.section === key) {
				a.setAttribute("aria-current", "true");
				// On narrow screens the index is a horizontal strip: keep the current one in it.
				if (nav.scrollWidth > nav.clientWidth) {
					const left = a.offsetLeft - nav.offsetLeft;
					if (left < nav.scrollLeft || left + a.offsetWidth > nav.scrollLeft + nav.clientWidth) nav.scrollTo({ left: Math.max(0, left - 16) });
				}
			}
			else a.removeAttribute("aria-current");
		}
	}

	let observer = null;
	function watchSections() {
		observer?.disconnect();
		const scroller = root.closest(".page") ?? root;
		const visible = new Map();
		observer = new IntersectionObserver((list) => {
			for (const e of list) visible.set(e.target.dataset.section, e.isIntersecting ? e.boundingClientRect.top : null);
			const first = sectionKeys.find((k) => visible.get(k) !== null && visible.get(k) !== undefined);
			if (first) markNav(first);
		}, { root: scroller, rootMargin: "-10% 0px -60% 0px" });
		for (const el of formHost.querySelectorAll(".cfg-card")) observer.observe(el);
	}

	// How many settings differ from their default, per section, on the index.
	function paintCounts() {
		for (const a of nav.querySelectorAll(".cfg-nav-link")) {
			const n = formHost.querySelector(`[data-section="${a.dataset.section}"]`)?.querySelectorAll(".cfg-row.on").length ?? 0;
			const slot = a.querySelector(".cfg-nav-n");
			slot.textContent = n ? String(n) : "";
			slot.title = n ? `${plural(n, "setting")} changed from the default` : "";
		}
	}

	function applyFilter() {
		const q = query;
		let total = 0;
		for (const sec of formHost.querySelectorAll(".cfg-card")) {
			let shown = 0;
			for (const el of sec.querySelectorAll(".cfg-row, .set-list")) {
				if (el.closest(".set-item-body")) continue;
				const hit = !q || (el.dataset.find ?? "").includes(q);
				el.hidden = !hit;
				if (hit) shown++;
			}
			for (const g of [...sec.querySelectorAll(".cfg-group")].reverse()) g.hidden = Boolean(q) && !g.querySelector(".cfg-row:not([hidden]), .set-list:not([hidden])");
			for (const l of sec.querySelectorAll(".cfg-list")) l.hidden = Boolean(q) && !l.querySelector(":scope > :not([hidden])");
			sec.hidden = Boolean(q) && !shown;
			sec.querySelector(".cfg-adv")?.repaint();
			total += shown;
			const link = nav.querySelector(`[data-section="${sec.dataset.section}"]`);
			if (link) link.hidden = sec.hidden;
		}
		noMatch.hidden = !q || total > 0;
		findNote.textContent = q ? (total ? plural(total, "setting") : "") : "";
	}

	async function refreshForm() {
		await fetchConfig();
		if (alive) renderForm();
	}

	// ---------- the file ----------

	const file = (() => {
		let editor = null;
		let baseline = null;
		let fileEtag = null;
		let busy = false;
		let loaded = false;
		let pending = null; // a key path to land on once the file is loaded
		const name = () => `config.${format}`;
		const errorBox = h("div", { class: "cfg-file-error", id: "cfg-file-error", role: "alert", hidden: true });
		const notice = h("div", { class: "set-file-notice", hidden: true });
		const stats = h("span", { class: "cfg-file-stats" });
		const findInput = h("input", { class: "input", type: "search", placeholder: "Find in file", "aria-label": "Find in file", autocomplete: "off", spellcheck: "false" });
		const findCount = h("span", { class: "cfg-find-count", role: "status" });
		const lineInput = h("input", { class: "input mono cfg-line-input", type: "number", min: 1, inputmode: "numeric", placeholder: "Line", "aria-label": "Go to line", title: "Go to line (Ctrl+G in the editor)" });
		const jumpSelect = h("select", { class: "select cfg-jump", "aria-label": "Jump to a section" });
		jumpSelect.addEventListener("change", () => { if (jumpSelect.value) land([jumpSelect.value]); jumpSelect.value = ""; });
		const paintJump = () => {
			const keys = data ? Object.keys(data).filter((k) => !READ_ONLY.has(k)) : [];
			const order = (k) => { const i = SECTIONS.findIndex(([x]) => x === k); return i === -1 ? 99 : i; };
			jumpSelect.replaceChildren(h("option", { value: "" }, "Jump to section…"), ...keys.sort((a, b) => order(a) - order(b)).map((k) => h("option", { value: k }, SECTION.get(k)?.title ?? human(k))));
		};
		const saveButton = btn("Save file", { icon: "save", cls: "primary sm", disabled: true, onClick: () => save() });
		const reloadFile = btn("Reload from disk", { icon: "refresh", cls: "ghost sm", onClick: () => load({ ask: true }) });
		let secretsShown = false;
		const secretsButton = btn("Show secrets", { icon: "eye", cls: "ghost sm", onClick: () => {
			secretsShown = !secretsShown;
			editor?.showSecrets(secretsShown);
			secretsButton.replaceChildren(icon(secretsShown ? "eyeOff" : "eye", 15), secretsShown ? "Hide secrets" : "Show secrets");
		} });
		const titleName = h("code", { class: "cfg-file-name" }, name());
		const toolbar = h("div", { class: "cfg-file-tools" },
			h("div", { class: "cfg-file-find" },
				h("div", { class: "search" }, icon("search", 15), findInput),
				findCount,
				iconBtn("arrowUp", "Previous match (Shift+Enter)", () => runFind({ backwards: true }), "sm"),
				iconBtn("arrowDown", "Next match (Enter)", () => runFind({}), "sm"),
			),
			h("div", { class: "cfg-file-goto" }, lineInput, jumpSelect),
			h("span", { class: "spacer" }),
			secretsButton,
			reloadFile,
			saveButton,
		);
		const editorHost = h("div", { class: "cfg-file-editor" }, spinner("Loading the file"));
		const foot = h("div", { class: "cfg-file-foot" },
			titleName, stats,
			h("span", { class: "spacer" }),
			h("span", { class: "cfg-keys" }, h("kbd", null, "Ctrl"), h("kbd", null, "F"), " find ", h("kbd", null, "Ctrl"), h("kbd", null, "G"), " line ", h("kbd", null, "Ctrl"), h("kbd", null, "S"), " save ", h("kbd", null, "Esc"), " leave the editor"),
		);
		fileEl.append(
			h("p", { class: "cfg-file-lede" }, "The file as it is on disk, for what the settings can't express: adding or removing entries, comments, ordering. The engine checks every save; if it refuses, nothing changes and the line it points at is marked."),
			notice, toolbar, errorBox, editorHost, foot,
		);

		const dirty = () => Boolean(editor) && editor.value !== baseline;
		const update = () => {
			saveButton.disabled = busy || !dirty();
			dirtyDot.hidden = !dirty();
			if (!editor) return;
			stats.textContent = `${plural(editor.lineCount, "line")}${dirty() ? " · edited" : ""}`;
			stats.classList.toggle("edited", dirty());
		};
		const showError = (message) => {
			if (!message) { errorBox.hidden = true; editor?.markError(null); editor?.area.removeAttribute("aria-invalid"); return; }
			const line = Number(/\bline (\d+)/i.exec(message)?.[1]) || null;
			errorBox.replaceChildren(
				icon("info", 16),
				h("div", { class: "cfg-file-error-body" },
					h("div", { class: "cfg-file-error-title" }, `${name()} was not saved`, line ? h("span", { class: "cfg-file-error-line" }, ` · line ${line}`) : null),
					h("pre", { class: "cfg-file-error-text" }, message),
					h("div", { class: "cfg-file-error-foot" }, "The running config is unchanged and your edits are still here."),
				),
				line ? btn(`Go to line ${line}`, { cls: "outline sm", onClick: () => editor.goToLine(line) }) : null,
			);
			errorBox.hidden = false;
			editor?.area.setAttribute("aria-invalid", "true");
			editor?.markError(line);
			if (line) editor?.goToLine(line, { focus: false });
		};
		const runFind = (opts) => {
			if (!editor) return;
			const r = editor.find(findInput.value, opts);
			findCount.textContent = !r ? "" : r.count ? `${fmtInt(r.index + 1)} of ${fmtInt(r.count)}` : "No matches";
			findCount.classList.toggle("none", Boolean(r && !r.count));
		};
		findInput.addEventListener("input", () => runFind({ restart: true }));
		findInput.addEventListener("keydown", (e) => {
			if (e.key === "Enter" || e.key === "F3") { e.preventDefault(); runFind({ backwards: e.shiftKey }); }
			else if (e.key === "Escape" && findInput.value) { e.stopPropagation(); findInput.value = ""; runFind({}); }
		});
		lineInput.addEventListener("keydown", (e) => {
			if (e.key === "Enter") {
				e.preventDefault();
				const n = Number(lineInput.value);
				if (editor && Number.isInteger(n) && n > 0) { editor.goToLine(n); lineInput.value = ""; }
			}
		});

		async function load({ ask = false, quiet = false } = {}) {
			if (busy) return;
			if (ask && dirty() && !(await confirm({ title: "Discard your edits?", text: `Load ${name()} from disk again without saving these changes.`, confirmLabel: "Discard edits", danger: true }))) return;
			busy = true;
			update();
			try {
				titleName.textContent = name();
				const res = await engine.raw("GET", name());
				const text = await res.text();
				fileEtag = res.headers.get("ETag");
				baseline = text;
				if (!editor) {
					editor = codeEditor({
						value: text, language: format, label: name(), describedBy: "cfg-file-error", maskKeys: SECRET,
						onInput: () => { update(); if (!errorBox.hidden) showError(null); },
						onSave: () => save(),
						onFind: () => { findInput.focus(); findInput.select(); },
						onFindNext: (back) => runFind({ backwards: back }),
						onGoto: () => { lineInput.focus(); lineInput.select(); },
					});
					editorHost.replaceChildren(editor.el);
					editor.showSecrets(secretsShown);
				} else if (editor.value !== text) {
					const top = editor.area.scrollTop;
					editor.setValue(text);
					editor.area.scrollTop = top;
				}
				loaded = true;
				paintJump();
				notice.hidden = true;
				if (!quiet) showError(null);
				if (findInput.value) runFind({ restart: true });
				if (pending) { const p = pending; pending = null; land(p); }
			} catch (e) {
				if (!editor) editorHost.replaceChildren(errorState(e, () => load()));
				else showError(errorText(e));
			} finally {
				busy = false;
				update();
			}
		}

		function land(path) {
			if (!editor) { pending = path; return; }
			const line = path?.length ? findKeyLine(editor.value, path, format) : 1;
			editor.goToLine(line, { select: "value" });
		}

		async function save() {
			if (busy || !dirty()) return;
			const text = editor.value;
			busy = true;
			update();
			showError(null);
			try {
				const res = await engine.raw("PUT", name(), { body: text, headers: { "Content-Type": format === "toml" ? "application/toml" : "application/yaml", ...(fileEtag ? { "If-Match": fileEtag } : {}) } });
				fileEtag = res.headers.get("ETag");
				baseline = text;
				busy = false;
				saveButton.replaceChildren(icon("check", 15), "Saved");
				setTimeout(() => saveButton.replaceChildren(icon("save", 15), "Save file"), 2000);
				// The engine may tidy the text (quoting, indentation); show what it wrote,
				// and bring the settings up to date.
				await load({ quiet: true });
				await refreshForm();
			} catch (e) {
				if (e instanceof ApiError && e.status === 412) showError(`${name()} changed on disk after you opened it here, probably from a setting saved in the other view. Copy your edits, choose Reload, and apply them again.`);
				else showError(errorText(e));
			} finally {
				busy = false;
				update();
			}
		}

		return {
			dirty,
			async show() { if (!loaded) await load(); },
			async reload() { if (loaded) await load(); },
			find(q) { findInput.value = q; runFind({ restart: true }); findInput.focus(); },
			async openAt(path) {
				if (!loaded) { pending = path; await load(); }
				else land(path);
			},
			// A setting was saved: the file on disk moved on.
			async formSaved() {
				if (!loaded) return;
				if (!dirty()) return load({ quiet: true });
				notice.replaceChildren(icon("info", 14), h("span", null, `A setting was saved, so ${name()} on disk is newer than your edits here. Saving now would be refused.`), btn("Load the latest (discard my edits)", { cls: "outline sm", onClick: () => load() }));
				notice.hidden = false;
			},
		};
	})();

	async function openFileAt(path) {
		await setView("file");
		await file.openAt(path);
	}

	async function reloadAll() {
		if (file.dirty() && !(await confirm({ title: "Discard your edits?", text: "Your unsaved edits to the file would be replaced with what is on disk.", confirmLabel: "Discard edits", danger: true }))) return;
		reloadButton.disabled = true;
		try {
			await refreshForm();
			await file.reload();
		} catch (e) {
			formHost.replaceChildren(errorState(e, reloadAll));
		} finally {
			reloadButton.disabled = false;
		}
	}

	viewHost.append(view === "file" ? fileEl : settingsEl);
	try {
		await fetchConfig();
		renderForm();
	} catch (e) {
		formHost.replaceChildren(errorState(e, reloadAll));
	}
	if (view === "file") await file.show();

	const beforeUnload = (e) => { if (file.dirty()) { e.preventDefault(); e.returnValue = ""; } };
	window.addEventListener("beforeunload", beforeUnload);
	return () => { alive = false; observer?.disconnect(); window.removeEventListener("beforeunload", beforeUnload); };
}
