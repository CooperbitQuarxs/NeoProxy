// Condition builder: "this stage serves a request when …", built with the
// mouse. The tree is the backend's Stage.condition: {all:[…]} | {any:[…]} |
// {not:node} | {field, op, value}. Groups are parentheses; every group states
// whether all or any of its rows must hold. Code mode shows the same tree as
// one readable expression.

import { h, icon } from "./dom.js";
import { btn, input, select, picker } from "./components.js";

// Fallback field list; the server's /virtual-models/fields replaces it when present.
export const DEFAULT_FIELDS = [
	{ name: "user.handle", kind: "string", scope: "request", doc: "The username of whoever sent the request" },
	{ name: "user.role", kind: "string", scope: "request", doc: "admin, owner, member or service", values: ["admin", "owner", "member", "service"] },
	{ name: "request.model", kind: "string", scope: "request", doc: "What the caller asked for, e.g. neoproxy/default" },
	{ name: "request.effort", kind: "string", scope: "request", doc: "The reasoning effort the caller asked for, if any", values: ["low", "medium", "high", "xhigh", "max"] },
	{ name: "request.stream", kind: "bool", scope: "request", doc: "Whether the caller asked for a streamed answer" },
	{ name: "request.prompt", kind: "string", scope: "request", doc: "Text of the last user message" },
	{ name: "request.tokens", kind: "number", scope: "request", doc: "Estimated input tokens" },
	{ name: "time.weekday", kind: "string", scope: "request", doc: "monday … sunday, server time", values: ["monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"] },
	{ name: "time.hour", kind: "number", scope: "request", doc: "Hour of the day, 0–23, server time" },
	{ name: "time.date", kind: "string", scope: "request", doc: "YYYY-MM-DD, server time" },
	{ name: "account.label", kind: "string", scope: "account", doc: "The account's email or key suffix" },
	{ name: "account.plan", kind: "string", scope: "account", doc: "The plan the provider reports" },
	{ name: "account.state", kind: "string", scope: "account", doc: "healthy, cooldown, expired or disabled", values: ["healthy", "cooldown", "expired", "disabled"] },
	{ name: "account.metered", kind: "bool", scope: "account", doc: "Whether the account reports usage for this stage's window" },
	{ name: "account.usage", kind: "percent", scope: "account", doc: "Usage of this stage's window, 0–100%" },
	{ name: "account.usage.5h", kind: "percent", scope: "account", doc: "5-hour usage, when the provider reports it" },
	{ name: "account.usage.day", kind: "percent", scope: "account", doc: "Daily usage, when reported or metered" },
	{ name: "account.usage.week", kind: "percent", scope: "account", doc: "Weekly usage, when reported or metered" },
	{ name: "account.usage.month", kind: "percent", scope: "account", doc: "Monthly usage, when reported or metered" },
	{ name: "pool.size", kind: "number", scope: "request", doc: "How many of the selected accounts could serve right now" },
	{ name: "pool.average", kind: "percent", scope: "request", doc: "Average usage of the selected accounts over this stage's window" },
	{ name: "pool.average.week", kind: "percent", scope: "request", doc: "Average weekly usage of the selected accounts" },
	{ name: "pool.average.5h", kind: "percent", scope: "request", doc: "Average 5-hour usage of the selected accounts" },
];

const OPS = {
	string: [["eq", "is"], ["neq", "is not"], ["contains", "contains"], ["matches", "matches regex"], ["in", "is one of"], ["nin", "is none of"], ["exists", "is known"]],
	number: [["lt", "<"], ["lte", "≤"], ["gt", ">"], ["gte", "≥"], ["eq", "="], ["neq", "≠"], ["exists", "is known"]],
	percent: [["lt", "below"], ["lte", "at most"], ["gt", "above"], ["gte", "at least"], ["exists", "is known"]],
	bool: [["eq", "is"]],
};

export const TEMPLATES = [
	{ label: "Only on weekdays", tree: { all: [{ field: "time.weekday", op: "nin", value: ["saturday", "sunday"] }] } },
	{ label: "Only during office hours (9–18)", tree: { all: [{ field: "time.hour", op: "gte", value: 9 }, { field: "time.hour", op: "lt", value: 18 }] } },
	{ label: "Only for one person", tree: { all: [{ field: "user.handle", op: "eq", value: "" }] } },
	{ label: "Services only, never people", tree: { all: [{ field: "user.role", op: "eq", value: "service" }] } },
	{ label: "Short prompts only", tree: { all: [{ field: "request.tokens", op: "lt", value: 4000 }] } },
	{ label: "Accounts under 50% weekly, or any account on weekends", tree: { any: [{ field: "account.usage.week", op: "lt", value: 0.5 }, { field: "time.weekday", op: "in", value: ["saturday", "sunday"] }] } },
];

const isGroup = (n) => n && (Array.isArray(n.all) || Array.isArray(n.any) || "not" in n);
const kindOf = (fields, name) => fields.find((f) => f.name === name)?.kind ?? "string";
const emptyLeaf = (fields) => ({ field: fields[0]?.name ?? "user.handle", op: "eq", value: "" });

// ---- text form (code mode) ----
export function toText(node, depth = 0) {
	if (!node) return "always";
	if ("not" in node) return `not (${toText(node.not, depth + 1)})`;
	const key = Array.isArray(node.all) ? "all" : Array.isArray(node.any) ? "any" : null;
	if (key) {
		const parts = node[key].map((c) => (isGroup(c) ? `(${toText(c, depth + 1)})` : toText(c, depth + 1)));
		if (!parts.length) return "always";
		return parts.join(key === "all" ? " and " : " or ");
	}
	const v = node.value;
	const lit = (x) => (typeof x === "string" ? JSON.stringify(x) : Array.isArray(x) ? `[${x.map(lit).join(", ")}]` : String(x));
	const sym = { eq: "==", neq: "!=", lt: "<", lte: "<=", gt: ">", gte: ">=", in: "in", nin: "not in", contains: "contains", matches: "matches", exists: "exists" }[node.op] ?? node.op;
	if (node.op === "exists") return `${node.field} exists`;
	return `${node.field} ${sym} ${lit(v)}`;
}

// ---- builder ----
export function ruleBuilder({ value, fields = DEFAULT_FIELDS, onChange, lead = "serve when", empty = "No rules: the stage serves whenever the percentages above allow." }) {
	let tree = value && isGroup(value) ? structuredClone(value) : { all: value ? [structuredClone(value)] : [] };
	const root = h("div", { class: "rules" });
	// Empty nested groups are a step in editing, not a rule; they are pruned
	// from what is saved (the server refuses empty groups).
	const prune = (n) => {
		if (!n) return null;
		if ("not" in n) { const inner = prune(n.not); return inner ? { not: inner } : null; }
		const key = Array.isArray(n.all) ? "all" : Array.isArray(n.any) ? "any" : null;
		if (!key) return n;
		const kept = n[key].map(prune).filter(Boolean);
		return kept.length ? { [key]: kept } : null;
	};
	// Repainting replaces every control, so put keyboard focus back on the one that had it.
	const emit = () => {
		const keep = root.contains(document.activeElement) ? [...root.querySelectorAll("button, input, select")].indexOf(document.activeElement) : -1;
		onChange(prune(structuredClone(tree)));
		paint();
		if (keep >= 0) [...root.querySelectorAll("button, input, select")][keep]?.focus({ preventScroll: true });
	};

	function countLeaves(n) {
		if (!n) return 0;
		if ("not" in n) return countLeaves(n.not);
		const arr = n.all ?? n.any;
		if (arr) return arr.reduce((s, c) => s + countLeaves(c), 0);
		return 1;
	}

	function leafRow(leaf, parentArr, idx) {
		const kind = kindOf(fields, leaf.field);
		const ops = OPS[kind] ?? OPS.string;
		if (!ops.some(([o]) => o === leaf.op)) leaf.op = ops[0][0];
		const fdef = fields.find((f) => f.name === leaf.field);
		const fieldBtn = h("button", { type: "button", class: "rule-field", title: fdef?.doc ?? "", "aria-haspopup": "dialog" }, h("span", { class: "rule-scope" }, fdef?.scope === "account" ? "each account" : ""), h("code", null, leaf.field), icon("chevronDown", 12));
		fieldBtn.addEventListener("click", () => picker(fieldBtn, {
			placeholder: "Which fact?",
			items: fields.map((f) => ({ value: f.name, label: f.name, sub: f.doc })),
			onPick: (name) => { leaf.field = name; const k = kindOf(fields, name); leaf.op = (OPS[k] ?? OPS.string)[0][0]; leaf.value = k === "bool" ? true : k === "number" || k === "percent" ? 0 : ""; emit(); },
		}));
		const opSel = select(ops.map(([v, l]) => ({ value: v, label: l })), leaf.op, (v) => { leaf.op = v; if (["in", "nin"].includes(v) && !Array.isArray(leaf.value)) leaf.value = leaf.value ? [leaf.value] : []; if (!["in", "nin"].includes(v) && Array.isArray(leaf.value)) leaf.value = leaf.value[0] ?? ""; emit(); }, { class: "select sm rule-op", "aria-label": "comparison" });
		let valueEl = null;
		if (leaf.op === "exists") valueEl = null;
		else if (kind === "bool") valueEl = select([{ value: "true", label: "yes" }, { value: "false", label: "no" }], String(leaf.value !== false), (v) => { leaf.value = v === "true"; emit(); }, { class: "select sm" });
		else if (kind === "percent") valueEl = h("span", { class: "rule-pct" }, input({ type: "number", min: 0, max: 100, step: 1, value: Math.round((Number(leaf.value) || 0) * 100), class: "input sm rule-num", onChange: (e) => { leaf.value = Math.min(100, Math.max(0, Number(e.target.value) || 0)) / 100; emit(); } }), "%");
		else if (kind === "number") valueEl = input({ type: "number", step: 1, value: leaf.value ?? 0, class: "input sm rule-num", onChange: (e) => { leaf.value = Number(e.target.value) || 0; emit(); } });
		else if (["in", "nin"].includes(leaf.op)) valueEl = input({ value: (Array.isArray(leaf.value) ? leaf.value : []).join(", "), placeholder: fdef?.values ? fdef.values.join(", ") : "a, b, c", class: "input sm rule-text", onChange: (e) => { leaf.value = e.target.value.split(",").map((x) => x.trim()).filter(Boolean); emit(); } });
		else if (fdef?.values && leaf.op !== "matches" && leaf.op !== "contains") valueEl = select(fdef.values.map((v) => ({ value: v, label: v })), fdef.values.includes(leaf.value) ? leaf.value : fdef.values[0], (v) => { leaf.value = v; emit(); }, { class: "select sm" });
		else valueEl = input({ value: leaf.value ?? "", placeholder: leaf.op === "matches" ? "^santi.*" : "value", class: "input sm rule-text mono", onChange: (e) => { leaf.value = e.target.value; emit(); } });
		return h("div", { class: "rule-row" },
			fieldBtn, opSel, valueEl,
			h("span", { class: "spacer" }),
			h("button", { type: "button", class: "rule-x", title: "Remove this rule", "aria-label": "Remove rule", onClick: () => { parentArr.splice(idx, 1); emit(); } }, icon("x", 12)),
		);
	}

	function groupBox(node, parentArr, idx, isRoot) {
		const negated = "not" in node;
		const inner = negated ? node.not : node;
		const key = Array.isArray(inner.all) ? "all" : "any";
		const arr = inner[key];
		const box = h("div", { class: `rule-group ${isRoot ? "root" : ""} ${negated ? "negated" : ""}` });
		const head = h("div", { class: "rule-group-head" },
			isRoot ? h("span", { class: "rule-lead" }, lead) : h("span", { class: "rule-paren" }, "("),
			negated ? h("span", { class: "tag" }, "not") : null,
			h("div", { class: "seg sm" },
				h("button", { type: "button", class: key === "all" ? "on" : "", "aria-pressed": String(key === "all"), onClick: () => { if (key !== "all") { inner.all = inner.any; delete inner.any; emit(); } } }, "all of these"),
				h("button", { type: "button", class: key === "any" ? "on" : "", "aria-pressed": String(key === "any"), onClick: () => { if (key !== "any") { inner.any = inner.all; delete inner.all; emit(); } } }, "any of these"),
			),
			h("span", { class: "spacer" }),
			isRoot ? null : btn(negated ? "Un-negate" : "Negate", { cls: "ghost sm", onClick: () => { if (negated) parentArr[idx] = node.not; else parentArr[idx] = { not: node }; emit(); } }),
			isRoot ? null : h("button", { type: "button", class: "rule-x", title: "Remove this group", "aria-label": "Remove group", onClick: () => { parentArr.splice(idx, 1); emit(); } }, icon("x", 12)),
		);
		box.append(head);
		const body = h("div", { class: "rule-group-body" });
		arr.forEach((child, i) => {
			if (i > 0) body.append(h("div", { class: "rule-join" }, key === "all" ? "and" : "or"));
			body.append(isGroup(child) ? groupBox(child, arr, i, false) : leafRow(child, arr, i));
		});
		if (!arr.length) body.append(h("div", { class: "rule-empty" }, isRoot ? empty : "Empty group: it counts as true."));
		body.append(h("div", { class: "rule-adds" },
			btn("Add a rule", { icon: "plus", cls: "sm", onClick: () => { arr.push(emptyLeaf(fields)); emit(); } }),
			btn("Add a group ( … )", { cls: "sm ghost", onClick: () => { arr.push({ all: [emptyLeaf(fields)] }); emit(); } }),
		));
		box.append(body);
		if (!isRoot) box.append(h("div", { class: "rule-paren" }, ")"));
		return box;
	}

	function paint() {
		root.replaceChildren(groupBox(tree, null, 0, true));
	}
	paint();
	return {
		el: root,
		get value() { return prune(structuredClone(tree)); },
		set(next) { tree = next && isGroup(next) ? structuredClone(next) : { all: next ? [structuredClone(next)] : [] }; paint(); },
	};
}
