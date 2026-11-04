// A plain-text code editor for config files: a <textarea> laid over a <pre>
// that paints the same text with syntax colours, plus a line-number gutter.
// Same overlay approach as lua-editor.js, generalised to YAML and TOML and
// built for long files: only the lines on screen are highlighted, so a
// 4,000-line config stays responsive while typing.

import { h } from "./dom.js";

const LINE = 20; // px; must match .ce line-height
const PAD_Y = 12;
const PAD_X = 14;
const OVERSCAN = 40;

const esc = (s) => s.replace(/[&<>]/g, (c) => (c === "&" ? "&amp;" : c === "<" ? "&lt;" : "&gt;"));

// ---------- YAML ----------

const YAML_CONST = /^(?:true|false|yes|no|on|off|null|~|True|False|TRUE|FALSE|Yes|No|YES|NO|On|Off|ON|OFF|Null|NULL)$/;
const YAML_NUM = /^(?:[-+]?(?:\d[\d_]*(?:\.\d*)?(?:[eE][-+]?\d+)?|\.\d+(?:[eE][-+]?\d+)?)|0x[0-9a-fA-F_]+|0o[0-7_]+|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN)|\d{4}-\d\d-\d\d(?:[Tt ]\d\d?:\d\d:\d\d(?:\.\d+)?(?:Z|\s*[-+]\d\d?(?::\d\d)?)?)?)$/;
const YAML_KEY = /(?:"(?:[^"\\]|\\.)*"|'(?:[^']|'')*'|[^\s#"'[\]{},&*!|>%@`-][^#]*?|-[^\s#][^#]*?)(?=\s*:(?:\s|$))/y;
const YAML_BLOCK = /(?:^|:|^\s*-)\s*[|>][-+0-9]*\s*(?:#.*)?$/;

// Lines inside a `key: |` block scalar are text, whatever they look like.
function yamlStates(lines) {
	const states = new Uint8Array(lines.length);
	let block = -1;
	for (let i = 0; i < lines.length; i++) {
		const line = lines[i];
		const indent = line.length - line.trimStart().length;
		if (block >= 0) {
			if (!line.trim() || indent > block) { states[i] = 1; continue; }
			block = -1;
		}
		if (YAML_BLOCK.test(line) && !/^\s*#/.test(line)) block = indent;
	}
	return states;
}

function yamlScalar(word) {
	if (YAML_CONST.test(word)) return "const";
	if (YAML_NUM.test(word)) return "num";
	return "str";
}

function yamlLine(line, state) {
	const out = [];
	const push = (c, s) => { if (s) out.push([c, s]); };
	const lead = /^\s*/.exec(line)[0];
	push(null, lead);
	let i = lead.length;
	if (state === 1) { push("str", line.slice(i)); return out; }
	if (line[i] === "#") { push("com", line.slice(i)); return out; }
	const marker = /^(?:---|\.\.\.)(?=\s|$)/.exec(line.slice(i));
	if (marker) { push("punct", marker[0]); i += marker[0].length; }
	for (let m; (m = /^-(?:\s+|$)/.exec(line.slice(i))); i += m[0].length) {
		push("dash", "-");
		push(null, m[0].slice(1));
	}
	YAML_KEY.lastIndex = i;
	const key = YAML_KEY.exec(line);
	if (key) {
		push("key", key[0]);
		i += key[0].length;
		const colon = /^\s*:/.exec(line.slice(i))[0];
		push("punct", colon);
		i += colon.length;
	}
	let depth = 0;
	while (i < line.length) {
		const c = line[i];
		const rest = line.slice(i);
		let m;
		if ((m = /^\s+/.exec(rest))) {
			if (rest[m[0].length] === "#") { push(null, m[0]); push("com", rest.slice(m[0].length)); return out; }
			push(null, m[0]);
		} else if (c === "#" && (i === 0 || /\s/.test(line[i - 1]))) { push("com", rest); return out; }
		else if ((m = /^[&*][^\s,[\]{}]+/.exec(rest))) push("anchor", m[0]);
		else if ((m = /^![^\s,[\]{}]*/.exec(rest))) push("tag", m[0]);
		else if (c === '"') { m = /^"(?:[^"\\]|\\.)*"?/.exec(rest); push("str", m[0]); }
		else if (c === "'") { m = /^'(?:[^']|'')*'?/.exec(rest); push("str", m[0]); }
		else if ("[{".includes(c)) { depth++; m = [c]; push("punct", c); }
		else if ("]}".includes(c)) { depth = Math.max(0, depth - 1); m = [c]; push("punct", c); }
		else if (c === "," && depth) { m = [c]; push("punct", c); }
		else if ((c === "|" || c === ">") && (m = /^[|>][-+0-9]*(?=\s|$)/.exec(rest))) push("punct", m[0]);
		else {
			m = depth ? /^[^,[\]{}#]+?(?=\s*(?:[,[\]{}]|\s#|$))/.exec(rest) : /^.+?(?=\s+#|$)/.exec(rest);
			if (!m) m = [c];
			const kv = depth ? /^([^:]+?)(\s*:\s+)(.*)$/.exec(m[0]) : null;
			if (kv) { push("key", kv[1]); push("punct", kv[2]); if (kv[3]) push(yamlScalar(kv[3].trim()), kv[3]); }
			else push(yamlScalar(m[0].trim()), m[0]);
		}
		i += m[0].length;
	}
	return out;
}

// ---------- TOML ----------

const TOML_KEY = /(?:[A-Za-z0-9_-]+|"(?:[^"\\]|\\.)*"|'[^']*')(?:\s*\.\s*(?:[A-Za-z0-9_-]+|"(?:[^"\\]|\\.)*"|'[^']*'))*(?=\s*=)/y;
const TOML_DATE = /^(?:\d{4}-\d\d-\d\d(?:[Tt ]\d\d:\d\d(?::\d\d(?:\.\d+)?)?(?:[Zz]|[-+]\d\d:\d\d)?)?|\d\d:\d\d:\d\d(?:\.\d+)?)/;
const TOML_NUM = /^[-+]?(?:0x[0-9a-fA-F_]+|0o[0-7_]+|0b[01_]+|inf|nan|\d[\d_]*(?:\.[\d_]+)?(?:[eE][-+]?\d+)?)(?![\w.:-])/;

// state: "" outside strings, or the open multi-line delimiter (""" or ''').
function tomlLine(line, state) {
	const out = [];
	const push = (c, s) => { if (s) out.push([c, s]); };
	let i = 0;
	let expectKey = true;
	if (state) {
		const end = line.indexOf(state);
		if (end === -1) { push("str", line); return { out, state }; }
		push("str", line.slice(0, end + 3));
		i = end + 3;
		state = "";
		expectKey = false;
	} else {
		const lead = /^\s*/.exec(line)[0];
		push(null, lead);
		i = lead.length;
		const head = /^\[\[?[^\]#]*\]?\]?/.exec(line.slice(i));
		if (head && line[i] === "[") { push("section", head[0]); i += head[0].length; expectKey = false; }
	}
	while (i < line.length) {
		const c = line[i];
		const rest = line.slice(i);
		let m;
		if ((m = /^\s+/.exec(rest))) push(null, m[0]);
		else if (c === "#") { push("com", rest); break; }
		else if (expectKey && (TOML_KEY.lastIndex = i, (m = TOML_KEY.exec(line)))) {
			push("key", m[0]);
			i += m[0].length;
			m = /^\s*=/.exec(line.slice(i));
			push("punct", m[0]);
			expectKey = false;
		} else if ((m = /^("""|''')/.exec(rest))) {
			const end = line.indexOf(m[1], i + 3);
			if (end === -1) { push("str", rest); return { out, state: m[1] }; }
			m = [line.slice(i, end + 3)];
			push("str", m[0]);
		} else if (c === '"') { m = /^"(?:[^"\\]|\\.)*"?/.exec(rest); push("str", m[0]); }
		else if (c === "'") { m = /^'[^']*'?/.exec(rest); push("str", m[0]); }
		else if ((m = TOML_DATE.exec(rest))) push("date", m[0]);
		else if ((m = TOML_NUM.exec(rest))) push("num", m[0]);
		else if ((m = /^(?:true|false)(?![\w-])/.exec(rest))) push("const", m[0]);
		else {
			m = [c];
			push("punct", c);
			if (c === "{" || c === ",") expectKey = true;
		}
		i += m[0].length;
	}
	return { out, state };
}

function tomlStates(lines) {
	const states = new Array(lines.length);
	let state = "";
	for (let i = 0; i < lines.length; i++) {
		states[i] = state;
		state = tomlLine(lines[i], state).state;
	}
	return states;
}

function lineHTML(tokens) {
	let s = "";
	for (const [c, t] of tokens) s += c ? `<span class="ce-${c}">${esc(t)}</span>` : esc(t);
	return s;
}

// ---------- locating a key path in the text ----------

function yamlInfo(line) {
	const m = /^( *)((?:-(?: +|$))*)(.*)$/.exec(line);
	const rest = m[3];
	const blank = !m[2] && (!rest.trim() || rest.trimStart().startsWith("#"));
	YAML_KEY.lastIndex = 0;
	const k = YAML_KEY.exec(rest);
	const key = k ? k[0].replace(/^"(.*)"$|^'(.*)'$/, "$1$2") : null;
	return { indent: m[1].length, dash: m[2].length > 0, col: m[1].length + m[2].length, key, blank };
}

// The 1-based line where a JSON path (keys and array indexes) is written in a
// block-style YAML document, or the closest ancestor that could be found.
function findYamlLine(lines, path) {
	const info = lines.map(yamlInfo);
	let start = 0;
	let end = lines.length;
	let col = null;
	let best = 0;
	const blockEnd = (from, keyCol, limit) => {
		for (let i = from; i < limit; i++) {
			const x = info[i];
			if (x.blank) continue;
			if (x.indent < keyCol || (x.indent === keyCol && !x.dash)) return i;
		}
		return limit;
	};
	for (const seg of path) {
		let found = -1;
		if (typeof seg === "number") {
			let dashIndent = null;
			let count = -1;
			for (let i = start; i < end; i++) {
				const x = info[i];
				if (x.blank) continue;
				if (dashIndent === null) {
					if (!x.dash) return best + 1;
					dashIndent = x.indent;
				}
				if (x.indent < dashIndent) break;
				if (x.indent === dashIndent) {
					if (!x.dash) break;
					if (++count === seg) { found = i; break; }
				}
			}
			if (found < 0) return best + 1;
			let stop = end;
			for (let i = found + 1; i < end; i++) if (!info[i].blank && info[i].indent <= dashIndent) { stop = i; break; }
			start = found;
			end = stop;
			col = info[found].col;
		} else {
			for (let i = start; i < end; i++) {
				const x = info[i];
				if (x.blank) continue;
				if (col === null) col = x.col;
				if (x.key === seg && x.col === col) { found = i; break; }
			}
			if (found < 0) return best + 1;
			start = found + 1;
			end = blockEnd(found + 1, col, end);
			col = null;
		}
		best = found;
	}
	return best + 1;
}

function splitDotted(s) {
	const parts = [];
	const re = /\s*(?:"((?:[^"\\]|\\.)*)"|'([^']*)'|([A-Za-z0-9_-]+))\s*(?:\.|$)/y;
	let m;
	while (re.lastIndex < s.length && (m = re.exec(s))) parts.push(m[1] ?? m[2] ?? m[3]);
	return parts;
}

function findTomlLine(lines, path) {
	const want = path.map(String);
	const counters = new Map();
	let table = [];
	let best = { len: -1, line: 0 };
	const consider = (p, i) => {
		let n = 0;
		while (n < p.length && n < want.length && String(p[n]) === want[n]) n++;
		if (n === p.length && n > best.len) best = { len: n, line: i };
	};
	const resolve = (segs) => {
		const out = [];
		for (const s of segs) {
			out.push(s);
			const key = out.join("\u0000");
			if (counters.has(key)) out.push(counters.get(key));
		}
		return out;
	};
	let state = "";
	for (let i = 0; i < lines.length; i++) {
		const line = lines[i];
		const startState = state;
		state = tomlLine(line, state).state;
		if (startState) continue;
		const arr = /^\s*\[\[\s*([^\]]+?)\s*\]\]/.exec(line);
		const tbl = !arr && /^\s*\[\s*([^\]]+?)\s*\]/.exec(line);
		if (arr) {
			const segs = splitDotted(arr[1]);
			const parent = resolve(segs.slice(0, -1));
			const key = [...parent, segs.at(-1)].join("\u0000");
			counters.set(key, (counters.get(key) ?? -1) + 1);
			table = [...parent, segs.at(-1), counters.get(key)];
			// The first [[x]] header also stands for the list x itself, so a jump to
			// a nested list lands here rather than on its parent table.
			consider(table.slice(0, -1), i);
			consider(table, i);
			continue;
		}
		if (tbl) {
			table = resolve(splitDotted(tbl[1]));
			consider(table, i);
			continue;
		}
		TOML_KEY.lastIndex = (/^\s*/.exec(line)[0]).length;
		const k = TOML_KEY.exec(line);
		if (k) consider([...table, ...splitDotted(k[0])], i);
	}
	return best.line + 1;
}

export function findKeyLine(text, path, language = "yaml") {
	const lines = text.split("\n");
	return language === "toml" ? findTomlLine(lines, path) : findYamlLine(lines, path);
}

// ---------- the editor ----------

// `maskKeys` (a RegExp over key names) paints the values of matching keys, and
// the plain items of lists directly under them, as dots. Only the painting
// changes: the text, selection and copy stay real. `showSecrets(true)` lifts it.
export function codeEditor({ value = "", language = "yaml", label = "Source", describedBy, onInput, onSave, onFind, onFindNext, onGoto, maskKeys = null } = {}) {
	const gutterText = h("div", { class: "ce-gutter-text" });
	const gutterMarks = h("div", { class: "ce-marks" });
	const gutter = h("div", { class: "ce-gutter", "aria-hidden": "true" }, gutterMarks, gutterText);
	const code = h("code", { class: "ce-code" });
	const marks = h("div", { class: "ce-marks" });
	const pre = h("pre", { class: "ce-pre", "aria-hidden": "true" }, marks, code);
	const area = h("textarea", {
		class: "ce-input",
		spellcheck: "false",
		autocapitalize: "off",
		autocomplete: "off",
		autocorrect: "off",
		wrap: "off",
		"aria-label": label,
		"aria-describedby": describedBy,
	});
	area.value = value;
	const body = h("div", { class: "ce-body" }, pre, area);
	const el = h("div", { class: `ce ce-${language}` }, gutter, body);

	let lines = [];
	let states = null;
	let masked = null; // per line: 1 when a plain list item under a secret key
	let reveal_ = false;
	let maxLen = 0;
	let errorLine = null;
	let hit = null; // { line, col, len }
	let target = null; // 0-based line a jump landed on, lit until the next edit
	let frame = 0;
	let charW = 0;

	// The overlay is positioned in characters; measure one rather than trusting `ch`.
	const measure = () => {
		const probe = h("span", { style: { position: "absolute", visibility: "hidden", whiteSpace: "pre" } }, "0".repeat(100));
		code.append(probe);
		charW = probe.getBoundingClientRect().width / 100 || 7.5;
		probe.remove();
	};

	const secretLines = () => {
		const out = new Uint8Array(lines.length);
		if (!maskKeys || language !== "yaml") return out;
		let keyIndent = -1;
		let child = null;
		for (let i = 0; i < lines.length; i++) {
			const x = yamlInfo(lines[i]);
			if (x.blank) continue;
			if (keyIndent >= 0) {
				if (x.indent < keyIndent || (x.indent === keyIndent && !x.dash)) keyIndent = -1;
				else {
					if (child === null) child = x.dash ? x.indent : -1;
					if (x.dash && x.indent === child && !x.key) out[i] = 1;
					continue;
				}
			}
			if (x.key && maskKeys.test(x.key) && /:\s*(?:#.*)?$/.test(lines[i])) { keyIndent = x.col; child = null; }
		}
		return out;
	};

	const maskTokens = (tokens, listItem) => {
		if (reveal_ || !maskKeys) return tokens;
		let on = listItem;
		return tokens.map(([c, t]) => {
			if (c === "key") {
				const parts = splitDotted(t);
				on = maskKeys.test((parts.at(-1) ?? t).replace(/^["']|["']$/g, ""));
				return [c, t];
			}
			if (on && (c === "str" || c === "num" || c === "const") && t.trim()) return ["secret", t.replace(/^(["']?)(.*?)(["']?)$/, (_, a, b, z) => a + "•".repeat(b.length) + z)];
			return [c, t];
		});
	};

	const analyse = () => {
		lines = area.value.split("\n");
		states = language === "toml" ? tomlStates(lines) : yamlStates(lines);
		masked = secretLines();
		maxLen = 0;
		for (const l of lines) if (l.length > maxLen) maxLen = l.length;
		if (gutterText.dataset.n !== String(lines.length)) {
			gutterText.textContent = Array.from({ length: lines.length }, (_, n) => n + 1).join("\n");
			gutterText.dataset.n = String(lines.length);
		}
	};
	const paint = () => {
		frame = 0;
		const first = Math.max(0, Math.floor(area.scrollTop / LINE) - OVERSCAN);
		const last = Math.min(lines.length, Math.ceil((area.scrollTop + area.clientHeight) / LINE) + OVERSCAN);
		let html = "";
		for (let i = first; i < last; i++) {
			const tokens = language === "toml" ? tomlLine(lines[i], states[i]).out : yamlLine(lines[i], states[i]);
			html += lineHTML(maskTokens(tokens, masked[i] === 1)) + "\n";
		}
		code.innerHTML = html;
		// Spacers keep the painted window where the textarea has scrolled to,
		// and the min-width keeps horizontal scrolling in step with long lines elsewhere.
		code.style.paddingTop = `${first * LINE}px`;
		code.style.paddingBottom = `${(lines.length - last) * LINE + 40}px`;
		if (!charW) measure();
		code.style.minWidth = `${maxLen * charW + PAD_X * 2 + 40}px`;
		gutterText.style.paddingBottom = "40px";
		const m = [];
		const gm = [];
		if (errorLine) {
			m.push(h("div", { class: "ce-mark ce-mark-error", style: { top: `${PAD_Y + (errorLine - 1) * LINE}px` } }));
			gm.push(h("div", { class: "ce-mark ce-mark-error", style: { top: `${PAD_Y + (errorLine - 1) * LINE}px` } }));
		}
		if (target !== null && (!hit || hit.line !== target) && target !== (errorLine ?? 0) - 1) {
			m.push(h("div", { class: "ce-mark ce-mark-target", style: { top: `${PAD_Y + target * LINE}px` } }));
			gm.push(h("div", { class: "ce-mark ce-mark-target", style: { top: `${PAD_Y + target * LINE}px` } }));
		}
		if (hit) {
			m.push(h("div", { class: "ce-mark ce-mark-line", style: { top: `${PAD_Y + hit.line * LINE}px` } }));
			m.push(h("div", { class: "ce-hit", style: { top: `${PAD_Y + hit.line * LINE}px`, left: `${PAD_X + hit.col * charW}px`, width: `${hit.len * charW}px` } }));
		}
		marks.replaceChildren(...m);
		gutterMarks.replaceChildren(...gm);
		sync();
	};
	const schedule = () => { if (!frame) frame = requestAnimationFrame(paint); };
	const sync = () => {
		pre.scrollTop = area.scrollTop;
		pre.scrollLeft = area.scrollLeft;
		gutter.scrollTop = area.scrollTop;
	};
	const changed = () => {
		analyse();
		hit = null;
		target = null;
		paint();
		onInput?.(area.value);
	};

	const insert = (text) => {
		if (!document.execCommand("insertText", false, text)) {
			area.setRangeText(text, area.selectionStart, area.selectionEnd, "end");
			changed();
		}
	};
	area.addEventListener("keydown", (e) => {
		const mod = e.ctrlKey || e.metaKey;
		if (mod && e.key.toLowerCase() === "s") { e.preventDefault(); onSave?.(); return; }
		if (mod && e.key.toLowerCase() === "f" && onFind) { e.preventDefault(); onFind(); return; }
		if (mod && e.key.toLowerCase() === "g" && onGoto) { e.preventDefault(); onGoto(); return; }
		if (e.key === "F3" && onFindNext) { e.preventDefault(); onFindNext(e.shiftKey); return; }
		if (e.key === "Tab" && !mod && !e.altKey) {
			const { selectionStart: a, selectionEnd: b, value: v } = area;
			// Shift+Tab without a multi-line selection leaves the editor, as everywhere else.
			if (e.shiftKey && !v.slice(a, b).includes("\n")) return;
			e.preventDefault();
			if (!v.slice(a, b).includes("\n")) return insert("  ");
			const from = v.lastIndexOf("\n", a - 1) + 1;
			let to = v.indexOf("\n", b);
			if (to === -1) to = v.length;
			const next = v.slice(from, to).split("\n").map((l) => (e.shiftKey ? l.replace(/^ {1,2}/, "") : `  ${l}`)).join("\n");
			area.setSelectionRange(from, to);
			insert(next);
			area.setSelectionRange(from, from + next.length);
		} else if (e.key === "Enter" && !e.shiftKey && !mod && !e.altKey && area.selectionStart === area.selectionEnd) {
			const v = area.value;
			const lineStart = v.lastIndexOf("\n", area.selectionStart - 1) + 1;
			const indent = /^[ \t]*/.exec(v.slice(lineStart, area.selectionStart))[0];
			if (indent) { e.preventDefault(); insert(`\n${indent}`); }
		}
	});
	area.addEventListener("input", changed);
	area.addEventListener("scroll", () => { sync(); schedule(); });
	new ResizeObserver(schedule).observe(area);
	analyse();
	requestAnimationFrame(paint);

	const offsetOf = (line) => {
		let at = 0;
		for (let i = 0; i < line - 1 && i < lines.length; i++) at += lines[i].length + 1;
		return at;
	};
	const reveal = (line, col = 0) => {
		area.scrollTop = Math.max(0, (line - 1) * LINE - area.clientHeight / 3);
		const x = col * (charW || 7.5);
		area.scrollLeft = x > area.clientWidth - 80 ? x - 80 : 0;
		sync();
		paint();
	};

	return {
		el,
		area,
		get value() { return area.value; },
		get lineCount() { return lines.length; },
		setValue(next) {
			area.value = next;
			errorLine = null;
			changed();
		},
		// Put the caret on a line (1-based), mark it and bring it into view. By
		// default the whole line is selected; `select: "value"` selects only what
		// follows `key:` (or `key =`), so typing replaces the value.
		goToLine(line, { focus = true, select = "line" } = {}) {
			const n = Math.min(Math.max(1, line), lines.length);
			const at = offsetOf(n);
			const text = lines[n - 1] ?? "";
			let from = /^\s*/.exec(text)[0].length;
			let to = text.length;
			if (select === "value") {
				const m = /^(\s*(?:-\s+)?(?:"[^"]*"|'[^']*'|[^:#=\s][^:#=]*?)\s*[:=]\s*)(.*?)\s*(?:\s#.*)?$/.exec(text);
				if (m && m[2] && !/^[|>]/.test(m[2])) { from = m[1].length; to = from + m[2].length; }
			}
			if (focus) area.focus({ preventScroll: true });
			area.setSelectionRange(at + from, at + to);
			target = n - 1;
			hit = null;
			reveal(n);
		},
		markError(line) {
			errorLine = line && line <= lines.length ? line : null;
			paint();
		},
		// Finds `query` (case-insensitive) after (or before) the caret and
		// selects it. Returns { index, count } of the match, or null.
		find(query, { backwards = false, restart = false } = {}) {
			hit = null;
			target = null;
			if (!query) { paint(); return null; }
			const hay = area.value.toLowerCase();
			const q = query.toLowerCase();
			const all = [];
			for (let at = hay.indexOf(q); at !== -1 && all.length < 100000; at = hay.indexOf(q, at + 1)) all.push(at);
			if (!all.length) { paint(); return { index: -1, count: 0 }; }
			// `restart` keeps the current match while the query is still being typed.
			const caret = restart || backwards ? area.selectionStart : area.selectionEnd;
			let k = backwards ? all.findLastIndex((a) => a < caret) : all.findIndex((a) => a >= caret);
			if (k === -1) k = backwards ? all.length - 1 : 0;
			const at = all[k];
			area.setSelectionRange(at, at + q.length);
			const line = hay.slice(0, at).split("\n").length - 1;
			const col = at - (hay.lastIndexOf("\n", at - 1) + 1);
			hit = { line, col, len: q.length };
			reveal(line + 1, col);
			return { index: k, count: all.length };
		},
		focus() { area.focus(); },
		showSecrets(on) { reveal_ = !!on; paint(); },
	};
}
