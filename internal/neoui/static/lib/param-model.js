// Pure facts about model properties: how a Spec is read, how a stored setting is
// described, and whether a value is allowed. No DOM here, so lists, summaries and
// the editor all share it.

import { fmtInt } from "./dom.js";

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
export const clone = (v) => (v === undefined ? v : structuredClone(v));

export const GROUP_ORDER = ["Performance", "Context", "Reasoning", "Sampling", "Output"];

export const humanize = (id) => String(id).replace(/[_-]+/g, " ").replace(/^./, (c) => c.toUpperCase());

// ---------- reading specs ----------

export function groupSpecs(specs) {
	const groups = new Map();
	for (const spec of specs) {
		const name = spec.group || "Other";
		if (!groups.has(name)) groups.set(name, []);
		groups.get(name).push(spec);
	}
	const rank = (g) => { const i = GROUP_ORDER.indexOf(g); return i < 0 ? GROUP_ORDER.length : i; };
	return [...groups].sort((a, b) => rank(a[0]) - rank(b[0])).map(([group, list]) => ({ group, specs: list }));
}

// A spec may restrict how it can be applied (`modes: ["force"]`); otherwise both work.
export const specModes = (spec) => (spec.modes?.length ? spec.modes : ["force", "default"]);

export const optionOf = (spec, value) => (spec.options ?? []).find((o) => same(o.value, value));

export function compactNumber(v, unit) {
	if (unit === "tokens") {
		if (v >= 1_000_000 && v % 100_000 === 0) return `${v / 1_000_000}M`;
		if (v >= 1000 && v % 1000 === 0) return `${v / 1000}k`;
	}
	return fmtInt(v);
}

// "300k" for a token count, "0.7" for a temperature; unit appended when it is not obvious.
export function fmtScalar(spec, v) {
	if (typeof v !== "number") return String(v);
	if (spec.unit === "tokens") return compactNumber(v, "tokens");
	const text = Number.isInteger(v) ? fmtInt(v) : String(Number(v.toFixed(4)));
	return spec.unit ? `${text} ${spec.unit}` : text;
}

export function formatValue(spec, value) {
	if (value === null || value === undefined) return "–";
	switch (spec.kind) {
		case "choice": return optionOf(spec, value)?.label ?? String(value);
		case "set": return Array.isArray(value) && value.length ? value.map((v) => optionOf(spec, v)?.label ?? String(v)).join(", ") : "any";
		case "toggle": return value ? "On" : "Off";
		default: return fmtScalar(spec, value);
	}
}

// What a fresh setting starts as: something other than the model's own behaviour,
// since setting a property to what it already is does nothing.
export function startValue(spec) {
	switch (spec.kind) {
		case "choice": return (spec.options?.find((o) => !same(o.value, spec.default)) ?? spec.options?.[0])?.value ?? spec.default ?? null;
		case "toggle": return !spec.default;
		case "set": return (spec.options ?? []).map((o) => o.value);
		default: return spec.default ?? spec.min ?? 0;
	}
}

// null when the value is allowed for this spec, otherwise the reason it is not.
export function valueError(spec, value) {
	switch (spec.kind) {
		case "choice": return optionOf(spec, value) ? null : "Not one of the options this model offers.";
		case "set":
			if (!Array.isArray(value) || !value.length) return "Pick at least one.";
			return value.every((v) => optionOf(spec, v)) ? null : "Includes a level this model does not offer.";
		case "toggle": return typeof value === "boolean" ? null : "Choose on or off.";
		default:
			if (typeof value !== "number" || !Number.isFinite(value)) return "Enter a number.";
			if (value < spec.min || value > spec.max) return `Between ${fmtScalar(spec, spec.min)} and ${fmtScalar(spec, spec.max)}.`;
			return null;
	}
}

// ---------- reading settings ----------

// A setting is { mode, rules: [{ when?, value }] }. Rules are tried in order and the
// first whose `when` holds wins; a rule with no `when` is the fallback.
export const fallbackRule = (setting) => setting?.rules?.find((r) => !r.when);
export const conditionalRules = (setting) => (setting?.rules ?? []).filter((r) => r.when);

// One short label for a setting, for chips and the summary line.
export function describeSetting(spec, id, setting) {
	const label = spec?.label ?? humanize(id);
	const base = fallbackRule(setting);
	const conds = conditionalRules(setting);
	const first = base ?? conds[0];
	const raw = first?.value;
	const text = spec ? formatValue(spec, raw) : typeof raw === "number" ? (Number.isInteger(raw) && raw >= 1000 ? compactNumber(raw, "tokens") : String(raw)) : typeof raw === "boolean" ? (raw ? "On" : "Off") : Array.isArray(raw) ? raw.join(", ") : typeof raw === "string" ? humanize(raw) : "–";
	return { id, label, text, mode: setting?.mode ?? "force", conditional: conds.length > 0, onlyConditional: !base && conds.length > 0, count: setting?.rules?.length ?? 0 };
}

export const modeWords = { force: "always", default: "unless the caller sets it" };

// The problems a stage's settings have against the model's specs. Empty = savable.
export function paramIssues(params, specs) {
	const issues = [];
	for (const [id, setting] of Object.entries(params ?? {})) {
		const spec = specs.find((s) => s.id === id);
		if (!spec) { issues.push({ id, text: `${humanize(id)} is not offered by this model.` }); continue; }
		const rules = setting.rules ?? [];
		if (!rules.length) issues.push({ id, text: `${spec.label} has no value.` });
		rules.forEach((rule, i) => {
			const error = valueError(spec, rule.value);
			if (error) issues.push({ id, text: `${spec.label}: ${error}` });
			if (!rule.when && i < rules.length - 1) issues.push({ id, text: `${spec.label}: a rule with no conditions must come last.` });
		});
		if (!specModes(spec).includes(setting.mode)) issues.push({ id, text: `${spec.label} can only be applied as: ${specModes(spec).map((m) => modeWords[m]).join(", ")}.` });
	}
	return issues;
}

