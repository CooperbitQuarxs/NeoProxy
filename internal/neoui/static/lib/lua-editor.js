// A small Lua editor for virtual-model policies: a <textarea> laid over a <pre>
// that paints the same text with syntax colours, plus a line-number gutter.
// The tokenizer is deliberately tiny: it only has to colour what a policy script
// uses, and to find the global functions a stage may name.

import { h } from "./dom.js";

const KEYWORDS = new Set(["and", "break", "do", "else", "elseif", "end", "for", "function", "goto", "if", "in", "local", "not", "or", "repeat", "return", "then", "until", "while"]);
const CONSTANTS = new Set(["true", "false", "nil"]);
const BUILTINS = new Set(["assert", "error", "ipairs", "math", "next", "pairs", "pcall", "select", "string", "table", "tonumber", "tostring", "type"]);

const NUMBER = /0[xX][0-9a-fA-F]+(?:\.[0-9a-fA-F]*)?(?:[pP][+-]?\d+)?|\d+\.?\d*(?:[eE][+-]?\d+)?|\.\d+(?:[eE][+-]?\d+)?/y;
const IDENT = /[A-Za-z_][A-Za-z0-9_]*/y;
const LONG_OPEN = /\[(=*)\[/y;

function matchAt(re, src, i) {
	re.lastIndex = i;
	const m = re.exec(src);
	return m ? m[0] : null;
}

// Returns the end index of a long bracket `[==[ … ]==]` that opens at `i`, or -1.
function longBracketEnd(src, i) {
	LONG_OPEN.lastIndex = i;
	const open = LONG_OPEN.exec(src);
	if (!open) return -1;
	const close = `]${open[1]}]`;
	const end = src.indexOf(close, i + open[0].length);
	return end === -1 ? src.length : end + close.length;
}

// Token kinds: comment string number keyword const fn call builtin ident op space.
export function tokenize(src) {
	const tokens = [];
	let i = 0;
	let afterFunction = false;
	const push = (t, s) => { tokens.push({ t, s }); i += s.length; };
	while (i < src.length) {
		const c = src[i];
		if (/\s/.test(c)) {
			let j = i + 1;
			while (j < src.length && /\s/.test(src[j])) j++;
			push("space", src.slice(i, j));
		} else if (c === "-" && src[i + 1] === "-") {
			let end = src[i + 2] === "[" ? longBracketEnd(src, i + 2) : -1;
			if (end === -1) {
				end = src.indexOf("\n", i);
				if (end === -1) end = src.length;
			}
			push("comment", src.slice(i, end));
		} else if (c === "[" && longBracketEnd(src, i) !== -1) {
			push("string", src.slice(i, longBracketEnd(src, i)));
		} else if (c === '"' || c === "'") {
			let j = i + 1;
			while (j < src.length && src[j] !== c && src[j] !== "\n") j += src[j] === "\\" ? 2 : 1;
			push("string", src.slice(i, Math.min(src.length, src[j] === c ? j + 1 : j)));
		} else if (/[0-9]/.test(c) || (c === "." && /[0-9]/.test(src[i + 1] ?? ""))) {
			push("number", matchAt(NUMBER, src, i));
		} else if (/[A-Za-z_]/.test(c)) {
			const word = matchAt(IDENT, src, i);
			let kind = "ident";
			if (KEYWORDS.has(word)) kind = "keyword";
			else if (CONSTANTS.has(word)) kind = "const";
			else if (afterFunction) kind = "fn";
			else if (BUILTINS.has(word)) kind = "builtin";
			else if (/^\s*\(/.test(src.slice(i + word.length, i + word.length + 8))) kind = "call";
			if (word === "function") afterFunction = true;
			push(kind, word);
		} else {
			if (c === "(") afterFunction = false;
			push("op", c);
		}
	}
	return tokens;
}

// Names of the global functions a script defines (`function name(` and
// `name = function(`). Local functions are not reachable by name from a stage.
export function luaFunctions(src) {
	const tokens = tokenize(src).filter((t) => t.t !== "space" && t.t !== "comment");
	const names = [];
	for (let i = 0; i < tokens.length; i++) {
		const t = tokens[i];
		if (t.t === "keyword" && t.s === "function") {
			const name = tokens[i + 1];
			const open = tokens[i + 2];
			if (name?.t === "fn" && open?.s === "(" && tokens[i - 1]?.s !== "local") names.push(name.s);
		}
	}
	for (let i = 0; i + 3 < tokens.length; i++) {
		const [name, eq, fn] = tokens.slice(i, i + 3);
		const before = tokens[i - 1];
		if ((name.t === "ident" || name.t === "call") && eq.s === "=" && fn.s === "function" && before?.s !== "local" && before?.s !== "." && before?.s !== ":" && !names.includes(name.s)) names.push(name.s);
	}
	return [...new Set(names)];
}

function highlight(src) {
	const frag = document.createDocumentFragment();
	let plain = "";
	const flush = () => { if (plain) { frag.append(document.createTextNode(plain)); plain = ""; } };
	for (const { t, s } of tokenize(src)) {
		if (t === "space" || t === "ident" || t === "op") { plain += s; continue; }
		flush();
		frag.append(h("span", { class: `lua-${t}` }, s));
	}
	flush();
	// A trailing newline needs a character after it or <pre> drops the last line.
	if (src.endsWith("\n") || src === "") frag.append(document.createTextNode(" "));
	return frag;
}

const LINE = 20;

export function luaEditor({ value = "", onInput, label = "Lua source", describedBy, placeholder } = {}) {
	const gutter = h("div", { class: "lua-gutter", "aria-hidden": "true" });
	const code = h("code");
	const pre = h("pre", { class: "lua-pre", "aria-hidden": "true" }, code);
	const area = h("textarea", { class: "lua-input", spellcheck: "false", autocapitalize: "off", autocomplete: "off", autocorrect: "off", wrap: "off", "aria-label": label, "aria-describedby": describedBy, placeholder }, value);
	const body = h("div", { class: "lua-body" }, pre, area);
	const el = h("div", { class: "lua-editor" }, gutter, body);

	const paint = () => {
		const text = area.value;
		code.replaceChildren(highlight(text));
		const lines = text.split("\n").length;
		if (gutter.childElementCount !== lines) gutter.replaceChildren(...Array.from({ length: lines }, (_, n) => h("div", null, String(n + 1))));
		el.style.height = `${Math.max(260, Math.min(520, lines * LINE + 24))}px`;
	};
	const sync = () => {
		pre.scrollTop = area.scrollTop;
		pre.scrollLeft = area.scrollLeft;
		gutter.scrollTop = area.scrollTop;
	};
	const changed = () => { paint(); sync(); onInput?.(area.value); };

	// Tab inserts two spaces (and indents a selected block). Esc leaves the field, so keyboard users are never trapped.
	const insert = (text) => {
		if (!document.execCommand("insertText", false, text)) {
			area.setRangeText(text, area.selectionStart, area.selectionEnd, "end");
			changed();
		}
	};
	area.addEventListener("keydown", (e) => {
		if (e.key === "Tab" && !e.ctrlKey && !e.metaKey && !e.altKey) {
			e.preventDefault();
			const { selectionStart: a, selectionEnd: b, value: v } = area;
			const multiline = v.slice(a, b).includes("\n");
			if (!multiline && !e.shiftKey) return insert("  ");
			const from = v.lastIndexOf("\n", a - 1) + 1;
			let to = v.indexOf("\n", b);
			if (to === -1) to = v.length;
			const block = v.slice(from, to).split("\n");
			const next = block.map((line) => (e.shiftKey ? line.replace(/^ {1,2}/, "") : `  ${line}`)).join("\n");
			area.setSelectionRange(from, to);
			insert(next);
			area.setSelectionRange(from, from + next.length);
		} else if (e.key === "Enter" && !e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey && area.selectionStart === area.selectionEnd) {
			const v = area.value;
			const lineStart = v.lastIndexOf("\n", area.selectionStart - 1) + 1;
			const indent = /^[ \t]*/.exec(v.slice(lineStart, area.selectionStart))[0];
			if (indent) { e.preventDefault(); insert(`\n${indent}`); }
		}
	});
	area.addEventListener("input", changed);
	area.addEventListener("scroll", sync);
	wireCompletion(area, insert);
	paint();
	return {
		el,
		area,
		setValue(next) {
			area.value = next;
			changed();
		},
	};
}

// ---- completion ----
// What a policy can read, so nobody has to guess names. Typing `ctx.` or a
// prefix of a known path opens the list; Enter or Tab accepts, Esc closes.
export const LUA_COMPLETIONS = [
	["ctx.asked", "the model the caller asked for"],
	["ctx.protocol", "openai, openai-response or claude"],
	["ctx.userId", "who is asking (id)"],
	["ctx.user.handle", "who is asking (username)"],
	["ctx.user.role", "admin, owner, member or service"],
	["ctx.request", "the decoded request body"],
	["ctx.request.messages", "chat messages, when the protocol has them"],
	["ctx.request.prompt", "text of the last user message"],
	["ctx.request.effort", "the effort the caller asked for"],
	["ctx.allotment", "this person's share, 0 to 1"],
	["ctx.stage", "{ stageId, model, window, total, cap, allotment }"],
	["ctx.stage.total", "how full the pool is, 0 to 1"],
	["ctx.stage.cap", "where the stage stops, 0 to 1"],
	["ctx.account", "{ accountId, providerId, state, windows, providerWindows }"],
	["ctx.account.state", "healthy, cooldown, expired or disabled"],
	["ctx.account.providerWindows", "usage windows the provider reports"],
	["ctx.window", "this account's meter for the stage's window: { kind, known, used, resetsAt }"],
	["ctx.window.used", "0 to 1"],
	["ctx.used", "how much of the window the account has used, 0 to 1"],
	["ctx.budget", "the threshold the stage allows for this account, 0 to 1"],
	["ctx.time.weekday", "monday … sunday, server time"],
	["ctx.time.hour", "0 to 23, server time"],
	["ctx.time.date", "YYYY-MM-DD"],
	["ipairs(", "iterate an array: for i, v in ipairs(t) do … end"],
	["pairs(", "iterate a table: for k, v in pairs(t) do … end"],
	["string.find(", "string.find(s, pattern)"],
	["string.sub(", "string.sub(s, from, to)"],
	["string.lower(", "lowercase"],
	["string.match(", "string.match(s, pattern)"],
	["math.min(", "smallest of its arguments"],
	["math.max(", "largest of its arguments"],
];

function wireCompletion(area, insert) {
	const pop = h("div", { class: "lua-complete", role: "listbox" });
	let items = [], active = 0, from = 0;
	const close = () => { pop.remove(); items = []; };
	const wordBefore = () => {
		const v = area.value.slice(0, area.selectionStart);
		const m = /[A-Za-z_][\w.]*$/.exec(v);
		return m ? { word: m[0], from: area.selectionStart - m[0].length } : null;
	};
	const paint = () => {
		pop.replaceChildren(...items.map(([name, doc], i) => h("div", { class: `lua-complete-item ${i === active ? "active" : ""}`, role: "option", "aria-selected": String(i === active), onMousedown: (e) => { e.preventDefault(); accept(i); } }, h("code", null, name), h("span", null, doc))));
	};
	const accept = (i) => {
		const [name] = items[i] ?? [];
		if (!name) return close();
		area.setSelectionRange(from, area.selectionStart);
		insert(name);
		close();
	};
	const update = () => {
		const w = wordBefore();
		if (!w || w.word.length < 2) return close();
		const needle = w.word.toLowerCase();
		items = LUA_COMPLETIONS.filter(([n]) => n.toLowerCase().startsWith(needle) && n.toLowerCase() !== needle).slice(0, 9);
		if (!items.length) return close();
		from = w.from;
		active = 0;
		paint();
		if (!pop.isConnected) area.parentElement.append(pop);
		// Place under the caret line: cheap approximation using line count.
		const line = area.value.slice(0, area.selectionStart).split("\n").length;
		pop.style.top = `${Math.min(line * LINE + 8 - area.scrollTop, area.clientHeight - 40)}px`;
		pop.style.left = "48px";
	};
	area.addEventListener("input", update);
	area.addEventListener("click", close);
	area.addEventListener("blur", () => setTimeout(close, 120));
	area.addEventListener("keydown", (e) => {
		if (!items.length) { if (e.key === " " && e.ctrlKey) { e.preventDefault(); update(); } return; }
		if (e.key === "ArrowDown") { e.preventDefault(); active = (active + 1) % items.length; paint(); }
		else if (e.key === "ArrowUp") { e.preventDefault(); active = (active - 1 + items.length) % items.length; paint(); }
		else if (e.key === "Enter" || e.key === "Tab") { e.preventDefault(); e.stopImmediatePropagation(); accept(active); }
		else if (e.key === "Escape") { e.preventDefault(); e.stopImmediatePropagation(); close(); }
	}, true);
}

// A worked example against the real contract: every function takes the single
// `ctx` argument and returns exactly one value (internal/neoscript/policy.go).
export const LUA_EXAMPLE = `-- Policy functions for a virtual model.
-- Each takes one argument, ctx, and returns exactly one value.
-- Name them in a stage under "Lua functions"; a stage can use any or none.
--
-- ctx is read-only. In every function:
--   ctx.asked       the model the caller asked for, e.g. "neoproxy/opus"
--   ctx.protocol    "openai", "openai-response" or "claude"
--   ctx.userId      who is asking
--   ctx.request     the decoded request body (ctx.request.messages, ...)
--   ctx.allotment   this person's allotment, 0 to 1
--   ctx.stage       { stageId, model, window, total, cap, allotment }
--                   total = how full the pool is, cap = where the stage stops
-- In eligible and per_account, for the account being considered:
--   ctx.account     { accountId, providerId, state, windows, providerWindows }
--   ctx.window      this account's meter for the stage's window:
--                   { kind, known, used, resetsAt } (used is 0 to 1)
--   ctx.used        how much of that window the account has used, 0 to 1
--   ctx.budget      the threshold the stage already allows for it, 0 to 1

-- true keeps the account in play, false skips it for this request.
function eligible(ctx)
  -- Provider-reported meters can have namespaced kinds like "zai:tokens:5h".
  for _, w in ipairs(ctx.account.providerWindows or {}) do
    local five_hour = w.kind == "5h" or string.sub(w.kind, -3) == ":5h"
    if five_hour and w.known and w.used >= 0.9 then
      return false -- the provider says this account is nearly out of 5h room
    end
  end
  return true
end

-- A fraction of the window, not a percent: 0.25 means "until 25%".
-- Lua can only lower what the stage allows, never raise it.
function per_account(ctx)
  if ctx.window.known and ctx.window.used > 0.5 then
    return 0.25 -- half spent already: ease off
  end
  return 0.5
end

-- Where the whole stage stops, as a fraction of the pool.
-- No ctx.account here: it is asked once per stage.
function cap(ctx)
  if ctx.stage.total > 0.6 then
    return 0.6
  end
  return 0.7
end
`;
