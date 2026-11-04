// A small collapsible JSON tree for raw records. Keys are muted, scalars take
// the dashboard's token colours, containers fold with a count, long strings and
// long arrays are cut with an inline "show more", and one button copies the
// whole value. Used by the Activity inspector; any page may import jsonView.

import { h, icon, copyText } from "./dom.js";

const STRING_CUT = 140;
const ITEMS_CUT = 40;

export function jsonText(value) {
	return JSON.stringify(value, null, 2) ?? "undefined";
}

function count(n, one, many) {
	return `${n} ${n === 1 ? one : many}`;
}

function isContainer(v) {
	return v !== null && typeof v === "object";
}

function scalar(v) {
	if (v === null) return h("span", { class: "jv-null" }, "null");
	if (typeof v === "string") return stringNode(v);
	return h("span", { class: typeof v === "boolean" ? "jv-bool" : "jv-num" }, String(v));
}

function stringNode(s) {
	const quoted = (text) => JSON.stringify(text);
	if (s.length <= STRING_CUT) return h("span", { class: "jv-str" }, quoted(s));
	const text = h("span", { class: "jv-str" }, quoted(s.slice(0, STRING_CUT)).slice(0, -1) + "…");
	const wrap = h("span", { class: "jv-long" }, text);
	const more = h("button", {
		type: "button",
		class: "jv-more",
		onClick: () => {
			const open = more.getAttribute("aria-expanded") === "true";
			more.setAttribute("aria-expanded", String(!open));
			text.textContent = open ? quoted(s.slice(0, STRING_CUT)).slice(0, -1) + "…" : quoted(s);
			more.textContent = open ? `show all ${s.length} characters` : "show less";
		},
		"aria-expanded": "false",
	}, `show all ${s.length} characters`);
	wrap.append(" ", more);
	return wrap;
}

// One `key: value` line. Containers render a fold button and their children.
function node(key, value, depth, collapsedDepth, opts) {
	const label = key === null ? null : h("span", { class: "jv-key" }, typeof key === "number" ? `${key}` : JSON.stringify(key), h("span", { class: "jv-colon" }, ": "));
	if (!isContainer(value)) return h("div", { class: "jv-line" }, label, scalar(value));

	const isArray = Array.isArray(value);
	const entries = isArray ? value.map((v, i) => [i, v]) : Object.entries(value);
	const [open, close] = isArray ? ["[", "]"] : ["{", "}"];
	const summary = isArray ? count(entries.length, "item", "items") : count(entries.length, "key", "keys");
	if (!entries.length) return h("div", { class: "jv-line" }, label, h("span", { class: "jv-punct" }, open + close));

	const children = h("div", { class: "jv-children" });
	let filled = false;
	const fill = () => {
		if (filled) return;
		filled = true;
		const shown = entries.slice(0, ITEMS_CUT);
		for (const [k, v] of shown) children.append(node(isArray ? null : k, v, depth + 1, collapsedDepth, opts));
		if (entries.length > shown.length) {
			const rest = entries.length - shown.length;
			const more = h("button", {
				type: "button",
				class: "jv-more jv-rest",
				onClick: () => {
					more.remove();
					for (const [k, v] of entries.slice(ITEMS_CUT)) children.append(node(isArray ? null : k, v, depth + 1, collapsedDepth, opts));
				},
			}, `show ${rest} more ${isArray ? (rest === 1 ? "item" : "items") : rest === 1 ? "key" : "keys"}`);
			children.append(h("div", { class: "jv-line" }, more));
		}
	};

	const hint = h("span", { class: "jv-hint" }, ` ${summary} `);
	const closer = h("span", { class: "jv-punct" }, close);
	const toggle = h("button", { type: "button", class: "jv-toggle", "aria-expanded": "false" }, icon("chevronRight", 12), label, h("span", { class: "jv-punct" }, open));
	const line = h("div", { class: "jv-container" }, toggle, hint, closer);
	const tail = h("div", { class: "jv-line jv-tail" }, h("span", { class: "jv-punct" }, close));
	line.append(children, tail);

	const set = (expanded) => {
		if (expanded) fill();
		toggle.setAttribute("aria-expanded", String(expanded));
		line.classList.toggle("open", expanded);
	};
	toggle.addEventListener("click", () => set(toggle.getAttribute("aria-expanded") !== "true"));
	line.jvSet = set;
	set(depth < collapsedDepth);
	return line;
}

// jsonView(value, { collapsedDepth }): containers shallower than collapsedDepth
// start open (default 1: the top level is open, everything below is folded).
export function jsonView(value, { collapsedDepth = 1, copy = true } = {}) {
	const body = h("div", { class: "jv-body" });
	const root = node(null, value, 0, collapsedDepth, {});
	body.append(root);
	const setAll = (expanded) => {
		// Opening a container creates its children lazily, so sweep until the tree stops growing.
		for (let pass = 0, seen = -1; pass < 16; pass++) {
			const all = body.querySelectorAll(".jv-container");
			if (all.length === seen) break;
			seen = all.length;
			for (const c of all) c.jvSet(expanded);
		}
		if (!expanded) root.jvSet?.(true);
	};
	const copyBtn = h("button", {
		type: "button",
		class: "jv-tool",
		title: "Copy as JSON",
		onClick: async () => {
			const ok = await copyText(jsonText(value));
			copyBtn.classList.toggle("done", ok);
			copyBtn.lastChild.textContent = ok ? "Copied" : "Clipboard unavailable";
			setTimeout(() => {
				copyBtn.classList.remove("done");
				copyBtn.lastChild.textContent = "Copy";
			}, 1600);
		},
	}, icon("copy", 13), h("span", null, "Copy"));
	const tools = h("div", { class: "jv-tools" },
		h("button", { type: "button", class: "jv-tool", onClick: () => setAll(true) }, "Expand all"),
		h("button", { type: "button", class: "jv-tool", onClick: () => setAll(false) }, "Collapse all"),
		h("span", { class: "spacer" }),
		copy ? copyBtn : null,
	);
	return h("div", { class: "jv" }, tools, body);
}
