// A virtual model's properties as small chips: "Speed · Fast" with a lock when
// every request gets it, an open lock when the caller may still choose. Used by the
// catalogue, the editor's summary line and the chain cards, so a model reads the
// same everywhere. `specs` is optional: with it the chips carry the model's own
// labels, without it they fall back to the stored ids and raw values.
// A setting with conditions says its exception in words ("High above 100k
// request tokens"), never a bare "when"; `propDetails` spells every property out
// for the people who use the model, in a disclosure that works by touch and keys.

import { h, icon } from "./dom.js";
import { describeSetting, conditionalRules, formatValue, compactNumber, modeWords } from "./param-model.js";

// Labels for the common properties when the model's specs can't be read (members
// can't load them), so a person reads "Reasoning effort", not an id.
const KNOWN_LABELS = { effort: "Reasoning effort", context: "Context window", speed: "Speed", verbosity: "Verbosity", summary: "Reasoning summary" };

export function stageProps(stage, specs = []) {
	return Object.entries(stage.params ?? {}).map(([id, setting]) => {
		const spec = specs?.find((s) => s.id === id);
		const d = describeSetting(spec, id, setting);
		return { ...d, label: spec ? d.label : KNOWN_LABELS[id] ?? d.label, setting, spec };
	});
}

// ---------- conditions in words ----------

const DAYS = { monday: "Monday", tuesday: "Tuesday", wednesday: "Wednesday", thursday: "Thursday", friday: "Friday", saturday: "Saturday", sunday: "Sunday" };
const WINDOW = { "5h": "5-hour", day: "daily", week: "weekly", month: "monthly" };
const list = (xs) => (xs.length < 2 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs.at(-1)}`);
const asList = (v) => (Array.isArray(v) ? v : [v]);
const pct = (v) => `${Math.round(Number(v) * 100)}%`;
const hour = (v) => `${String(v).padStart(2, "0")}:00`;
const NUM = { lt: "below", lte: "at most", gt: "above", gte: "at least", eq: "exactly", neq: "other than" };

function leafWords(n) {
	const { field, op, value: v } = n;
	if (op === "exists") return `when ${field} is known`;
	if (field === "request.tokens") return `${NUM[op] ?? op} ${compactNumber(Number(v), "tokens")} request tokens`;
	if (field === "time.hour") return op === "gte" || op === "gt" ? `from ${hour(op === "gt" ? Number(v) + 1 : v)}` : op === "lt" || op === "lte" ? `before ${hour(op === "lte" ? Number(v) + 1 : v)}` : `at ${hour(v)}`;
	if (field === "time.weekday") {
		const days = list(asList(v).map((d) => DAYS[d] ?? d));
		return op === "nin" || op === "neq" ? `except on ${days}` : `on ${days}`;
	}
	if (field === "user.handle") return op === "neq" || op === "nin" ? `except for ${list(asList(v))}` : `for ${list(asList(v))}`;
	if (field === "user.role") return op === "neq" || op === "nin" ? `except for ${list(asList(v).map((r) => `${r}s`))}` : `for ${list(asList(v).map((r) => `${r}s`))}`;
	if (field === "request.stream") return v ? "when streamed" : "when not streamed";
	if (field === "request.effort") return `when you ask for ${list(asList(v))} effort`;
	const usage = field.match(/^(account|pool)\.(usage|average)(?:\.(5h|day|week|month))?$/);
	if (usage) {
		const whose = usage[1] === "account" ? "its account is" : "its accounts average";
		return `while ${whose} ${NUM[op] ?? op} ${pct(v)} of ${usage[3] ? `${WINDOW[usage[3]]} ` : ""}usage`;
	}
	const shown = typeof v === "string" ? `“${v}”` : Array.isArray(v) ? list(v.map(String)) : String(v);
	return `when ${field} is ${NUM[op] ?? op} ${shown}`;
}

// A condition tree ({all|any|not|leaf}) as one plain phrase.
export function conditionWords(node) {
	if (!node) return "always";
	if ("not" in node) return `unless ${conditionWords(node.not)}`;
	const key = Array.isArray(node.all) ? "all" : Array.isArray(node.any) ? "any" : null;
	if (key) return node[key].map(conditionWords).join(key === "all" ? " and " : " or ") || "always";
	return leafWords(node);
}

const valueWords = (p, value) => (p.spec ? formatValue(p.spec, value) : typeof value === "number" && value >= 1000 ? compactNumber(value, "tokens") : typeof value === "string" ? value.replace(/^./, (c) => c.toUpperCase()) : String(value));

// "Medium; High above 100k request tokens": the value most requests get, then each exception.
export function settingWords(p) {
	const exceptions = conditionalRules(p.setting).map((r) => `${valueWords(p, r.value)} ${conditionWords(r.when)}`);
	if (p.onlyConditional) return `${exceptions.join("; ")}; otherwise the model's own`;
	return [p.text, ...exceptions].join("; ");
}

// A conditional setting keeps a short chip ("Reasoning effort Medium*"); its
// exception is written once, in plain text, by `propNotes` below the chips.
export function propChip(p, { short = false } = {}) {
	const how = modeWords[p.mode] ?? p.mode;
	return h("span", { class: `prop ${p.mode} ${p.conditional ? "cond" : ""}`, title: `${p.label}: ${p.conditional ? settingWords(p) : p.text} (${how})` },
		icon(p.mode === "force" ? "lock" : "unlock", 11),
		short ? null : h("span", { class: "prop-k" }, p.label),
		h("span", { class: "prop-v" }, p.onlyConditional ? "sometimes" : p.text, p.conditional ? h("span", { class: "prop-star", "aria-hidden": "true" }, "*") : null),
	);
}

export function propChips(stage, specs, opts) {
	return stageProps(stage, specs).map((p) => propChip(p, opts));
}

// "* High above 100k request tokens." — every exception of a stage, once, as text.
export function propNotes(stage, specs) {
	const notes = stageProps(stage, specs).filter((p) => p.conditional)
		.map((p) => conditionalRules(p.setting).map((r) => `${valueWords(p, r.value)} ${conditionWords(r.when)}`).join("; ") + (p.onlyConditional ? `; otherwise the model's own ${p.label.toLowerCase()}` : ""));
	return notes.length ? h("div", { class: "prop-notes" }, notes.map((t) => h("span", null, `* ${t.replace(/^./, (c) => c.toUpperCase())}.`))) : null;
}

// What a property costs in usage, in words, when the model says so.
function costWords(p) {
	const values = [p.setting?.rules ?? []].flat().map((r) => r.value);
	if (p.spec?.kind === "choice") {
		const notes = values.map((v) => p.spec.options?.find((o) => o.value === v)).filter((o) => o?.multiplier > 1 && o.note);
		return notes.length ? `${notes[0].label}: ${notes[0].note}.` : "";
	}
	const band = (p.spec?.bands ?? []).find((b) => values.some((v) => typeof v === "number" && v > b.from));
	return band ? `Above ${compactNumber(band.from, "tokens")}: ${band.note.replace(/^./, (c) => c.toLowerCase())}.` : "";
}

// Every property of a stage, spelled out for the people who use it. A native
// <details>, so it opens by click, tap, Enter or Space; nothing hides in a hover title.
// `entries`: [{ name, stage, specs }]. Models with the same settings share one
// line ("GPT-6 Luna, GPT-6 Astra: Speed Fast"), so a chain reads once, not per stage.
export function propDetails(entries, { summary = "What these settings mean", bare = false } = {}) {
	const groups = [];
	for (const { name, stage, specs } of entries) {
		const props = stageProps(stage, specs);
		if (!props.length) continue;
		const sig = JSON.stringify(stage.params);
		const same = groups.find((g) => g.sig === sig);
		if (same) same.names.push(name); else groups.push({ sig, names: [name], props });
	}
	if (!groups.length) return null;
	const named = groups.length > 1 || groups[0].names.length > 1 || entries.length > 1;
	const body = h("div", { class: "prop-more-body" }, groups.map((g) => h("div", { class: "prop-more-group" },
		named ? h("div", { class: "prop-more-who" }, g.names.join(", ")) : null,
		h("dl", { class: "prop-more-list" }, g.props.map((p) => [
			h("dt", null, icon(p.mode === "force" ? "lock" : "unlock", 12), p.label),
			h("dd", null,
				h("span", { class: "prop-more-v" }, settingWords(p), "."),
				" ",
				p.mode === "force" ? "Set by your admin; your tool can't change it." : "The default; your tool may ask for something else.",
				costWords(p) ? h("span", { class: "prop-more-cost" }, " ", costWords(p)) : null),
		])))));
	// `bare`: just the list, for a page that already has its own disclosure.
	if (bare) return body;
	return h("details", { class: "prop-more" }, h("summary", null, icon("chevronRight", 13), h("span", null, summary)), body);
}
