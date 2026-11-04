// Dynamic model properties. The proxy tells us what one model supports (a list of
// Specs: kind, options, range, default, price bands, what it does on the wire) and
// everything here renders from that description. Nothing in this file knows what
// "speed" or "context" mean: a choice is a segmented control, a toggle is a switch,
// a range is a slider with its landmarks and price bands, a set is chips. A
// property that is set carries a mode (always, or unless the caller chooses) and
// optional "only when" rules built with the same condition builder as routing.

import { h, icon, svg } from "./dom.js";
import { btn, seg, toggle, select } from "./components.js";
import { ruleBuilder, toText } from "./rules.js";
import { clone, fmtScalar, formatValue, startValue, valueError, specModes, conditionalRules, describeSetting, modeWords } from "./param-model.js";

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

// ---------- value controls ----------

let serial = 0;
const uid = (p) => `${p}-${++serial}`;

// Options that carry a note ("1.5x speed · 2.5× usage") read best as small cards: a radio
// group, so the arrow keys move between them and screen readers announce "1 of 3".
function optionCards(spec, value, onValue, label) {
	const name = uid("prm-opt");
	const wrap = h("div", { class: "opt-cards", role: "radiogroup", "aria-label": label });
	for (const o of spec.options ?? []) {
		wrap.append(h("label", { class: "opt-card", title: same(o.value, spec.default) ? "The model's own behaviour" : undefined },
			h("input", { type: "radio", name, checked: same(o.value, value), onChange: () => onValue(o.value) }),
			h("span", { class: "opt-label" }, o.label, same(o.value, spec.default) ? h("span", { class: "opt-tag" }, "default") : null),
			o.note ? h("span", { class: "opt-note" }, o.note) : null,
		));
	}
	return wrap;
}

function choiceControl(spec, value, onValue, label) {
	const options = spec.options ?? [];
	const noted = options.some((o) => o.note);
	if (noted && options.length <= 3) return optionCards(spec, value, onValue, label);
	const roomy = options.length <= 6 && options.reduce((n, o) => n + String(o.label).length, 0) <= 44;
	if (!roomy) {
		return select(options.map((o) => ({ value: JSON.stringify(o.value), label: o.label })), JSON.stringify(value), (v) => onValue(JSON.parse(v)), { "aria-label": label });
	}
	// Many options with notes: a segmented control, and the chosen option explains itself underneath.
	const note = h("div", { class: "prm-optnote" });
	const paintNote = (v) => { const o = options.find((x) => same(x.value, v)); note.textContent = o?.note ?? ""; note.hidden = !o?.note; };
	const el = seg(
		options.map((o) => ({ value: o.value, title: [o.note, same(o.value, spec.default) ? "The model's own behaviour" : null].filter(Boolean).join(" · ") || undefined, label: same(o.value, spec.default) && noted ? h("span", { class: "prm-opt" }, o.label, h("span", { class: "opt-dot", "aria-label": "default" })) : o.label })),
		value,
		(v) => { paintNote(v); onValue(v); },
	);
	el.setAttribute("aria-label", label);
	el.classList.add("prm-seg");
	paintNote(value);
	return noted ? h("div", { class: "prm-choice" }, el, note) : el;
}

function setControl(spec, value, onValue, label) {
	const wrap = h("div", { class: "prm-set" });
	const group = h("div", { class: "chips", role: "group", "aria-label": label });
	const note = h("span", { class: "prm-note" });
	let current = Array.isArray(value) ? [...value] : [];
	const paint = () => {
		const total = spec.options.length;
		note.textContent = !current.length ? "pick at least one" : current.length === total ? "everything the model offers" : `${current.length} of ${total} allowed`;
		note.classList.toggle("bad", current.length === 0);
		for (const b of group.children) b.setAttribute("aria-pressed", String(current.some((v) => same(v, JSON.parse(b.dataset.v)))));
	};
	for (const o of spec.options ?? []) {
		group.append(h("button", {
			type: "button", class: "chip-btn", dataset: { v: JSON.stringify(o.value) }, title: o.note || undefined,
			onClick: () => {
				const on = current.some((v) => same(v, o.value));
				current = spec.options.map((x) => x.value).filter((v) => (same(v, o.value) ? !on : current.some((c) => same(c, v))));
				paint();
				onValue([...current]);
			},
		}, o.label));
	}
	paint();
	wrap.append(group, note);
	return wrap;
}

function numberControl(spec, value, onValue, label) {
	const id = uid("prm-num");
	const err = h("div", { class: "error prm-error", role: "alert", hidden: true });
	const num = h("input", { class: "input prm-num mono", type: "number", id, min: spec.min, max: spec.max, step: spec.step || "any", value: value ?? "", "aria-label": label });
	num.addEventListener("input", () => {
		const v = num.valueAsNumber;
		const bad = Number.isFinite(v) ? valueError(spec, v) : "Enter a number.";
		err.hidden = !bad;
		err.textContent = bad ?? "";
		num.setAttribute("aria-invalid", String(Boolean(bad)));
		if (!bad) onValue(v);
	});
	num.addEventListener("blur", () => { if (!err.hidden) { num.value = String(value ?? spec.default ?? spec.min); err.hidden = true; num.removeAttribute("aria-invalid"); } });
	return h("div", { class: "prm-numwrap" }, h("div", { class: "row" }, num, spec.unit ? h("span", { class: "ink-3" }, spec.unit) : null, h("span", { class: "prm-note" }, `${fmtScalar(spec, spec.min)} to ${fmtScalar(spec, spec.max)}`)), err);
}

// A slider over [min, max] drawn on top of the model's price bands, with its landmarks
// as buttons that snap to them and an exact numeric field beside it.
function rangeControl(spec, value, onValue, label) {
	const { min, max } = spec;
	const step = spec.step || 1;
	const span = max - min || 1;
	const frac = (v) => Math.min(1, Math.max(0, (v - min) / span));
	// The bands cover only where the price changes; fill the gaps with the plain price so the track is drawn end to end.
	const given = (spec.bands ?? []).map((b) => ({ ...b, from: Math.max(min, b.from ?? min), to: Math.min(max, b.to ?? max) })).filter((b) => b.to > b.from).sort((a, b) => a.from - b.from);
	const bands = [];
	let at = min;
	for (const b of given) {
		if (b.from > at) bands.push({ from: at, to: b.from, multiplier: 1, note: "standard price" });
		bands.push(b);
		at = Math.max(at, b.to);
	}
	if (at < max) bands.push({ from: at, to: max, multiplier: 1, note: "standard price" });
	const multipliers = [...new Set(bands.map((b) => b.multiplier ?? 1))].sort((a, b) => a - b);
	const bandClass = (b) => `b${Math.min(2, multipliers.indexOf(b.multiplier ?? 1))}`;
	const layer = (cls) => h("div", { class: `rng-bands ${cls}`, "aria-hidden": "true" }, bands.map((b) => h("i", { class: `rng-band ${bandClass(b)}`, style: { left: `${frac(b.from) * 100}%`, width: `${(frac(b.to) - frac(b.from)) * 100}%` }, title: b.note || (b.multiplier ? `${b.multiplier}× price` : "") })));
	const lit = layer("lit");
	const rangeId = uid("prm-range");
	const range = h("input", { type: "range", class: "rng-input", id: rangeId, min, max, step, value, "aria-label": label });
	const track = h("div", { class: "rng-track" }, layer("dim"), lit, h("div", { class: "rng-ticks", "aria-hidden": "true" }, (spec.marks ?? []).map((m) => h("i", { style: { "--p": frac(m.value) } }))), range);
	const read = h("output", { class: "rng-read", for: rangeId });
	const where = h("span", { class: "rng-where" });
	const num = h("input", { class: "input prm-num mono", type: "number", min, max, step, value, "aria-label": `${label}, exact value` });
	const err = h("div", { class: "error prm-error", role: "alert", hidden: true });
	let current = value;
	// A band covers (from, to]: prices change above a window, so its boundary value is still the cheaper band's. The first band includes the minimum.
	const bandOf = (v) => bands.find((b, i) => (v > b.from || i === 0) && v <= b.to) ?? bands.at(-1);
	const marks = h("div", { class: "rng-marks", role: "group", "aria-label": `${label}: landmarks` }, (spec.marks ?? []).map((m) => h("button", {
		type: "button", class: "rng-mark", dataset: { v: String(m.value) }, title: m.note || undefined, onClick: () => commit(m.value, true),
	}, h("b", null, m.label || fmtScalar(spec, m.value)), m.label ? h("span", null, fmtScalar(spec, m.value)) : null)));
	const legend = h("ul", { class: "rng-legend" }, bands.length > 1 || bands[0]?.note ? bands.map((b) => h("li", null, h("i", { class: `rng-sw ${bandClass(b)}` }), `${fmtScalar(spec, b.from)}–${fmtScalar(spec, b.to)}`, b.note ? h("span", { class: "ink-3" }, ` · ${b.note}`) : (b.multiplier && b.multiplier !== 1 ? h("span", { class: "ink-3" }, ` · ${b.multiplier}× price`) : null))) : []);
	function paint() {
		const p = frac(current);
		track.style.setProperty("--p", String(p));
		read.textContent = fmtScalar(spec, current);
		const b = bandOf(current);
		where.textContent = b && (b.multiplier ?? 1) !== 1 ? `${b.multiplier}× price band` : b && multipliers.length > 1 ? "standard price" : "";
		where.className = `rng-where ${b && (b.multiplier ?? 1) !== 1 ? "up" : ""}`;
		range.setAttribute("aria-valuetext", `${fmtScalar(spec, current)}${where.textContent ? `, ${where.textContent}` : ""}`);
		for (const m of marks.children) m.setAttribute("aria-pressed", String(Number(m.dataset.v) === current));
	}
	function commit(v, syncInputs) {
		current = v;
		if (syncInputs) { range.value = String(v); }
		num.value = String(v);
		err.hidden = true;
		num.removeAttribute("aria-invalid");
		paint();
		onValue(v);
	}
	range.addEventListener("input", () => commit(Number(range.value), false));
	num.addEventListener("input", () => {
		const v = num.valueAsNumber;
		const bad = Number.isFinite(v) ? valueError(spec, v) : "Enter a number.";
		err.hidden = !bad;
		err.textContent = bad ?? "";
		num.setAttribute("aria-invalid", String(Boolean(bad)));
		if (!bad) { current = v; range.value = String(v); paint(); onValue(v); }
	});
	num.addEventListener("blur", () => { if (!err.hidden) { num.value = String(current); err.hidden = true; num.removeAttribute("aria-invalid"); } });
	paint();
	return h("div", { class: "rng" },
		h("div", { class: "rng-top" }, h("div", { class: "rng-value" }, read, where), h("div", { class: "rng-exact" }, num, spec.unit ? h("span", { class: "ink-3" }, spec.unit) : null)),
		track, marks, legend, err);
}

// The control for one value of one spec.
export function valueControl(spec, value, onValue, label) {
	switch (spec.kind) {
		case "choice": return choiceControl(spec, value, onValue, label);
		case "set": return setControl(spec, value, onValue, label);
		case "range": return rangeControl(spec, value, onValue, label);
		case "number": return numberControl(spec, value, onValue, label);
		case "toggle": {
			const words = h("span", { class: "prm-note" }, value ? "On" : "Off");
			const sw = toggle(Boolean(value), (v) => { words.textContent = v ? "On" : "Off"; onValue(v); }, label, { showLabel: false });
			return h("div", { class: "row", style: { gap: "10px" } }, sw, words);
		}
		default: return h("span", { class: "ink-3" }, `${spec.kind} is not editable here`);
	}
}

// ---------- one property ----------

const MODE = {
	force: { label: "Always", icon: "lock", title: "Applied to every request, whatever the caller sends. Click to let callers choose." },
	default: { label: "Caller can change", icon: "unlock", title: "Used only when the caller sends nothing for it. Click to always apply." },
};

// One row per spec. The control is always live: it shows the model's own value until
// you change it, and changing it is what sets the property. Once set, a pill says how
// it applies (always, or only when the caller sends nothing) and a reset puts it back.
// `setting` is the stored setting (or undefined); `onChange(next | undefined)` gets a
// fresh object on every edit; `fields` are the condition facts a rule may read.
export function paramTile({ spec, setting, fields, onChange, onLayout }) {
	let s = setting ? clone(setting) : null;
	const conditional = new WeakSet(conditionalRules(s));
	const open = new WeakSet();
	const el = h("div", { class: "prm", dataset: { param: spec.id } });
	const helpId = uid("prm-help");
	const emit = () => onChange(s ? clone(s) : undefined);
	const isCond = (r) => Boolean(r.when) || conditional.has(r);
	const modes = specModes(spec);
	const firstMode = modes.includes("force") ? "force" : modes[0];
	const status = h("div", { class: "prm-status" });
	const tail = h("div", { class: "prm-tail" });

	function ruleBlock(rule) {
		const text = h("code", { class: "prm-when" });
		const builderHost = h("div", { class: "prm-builder", hidden: !open.has(rule) });
		const hint = h("div", { class: "hint", hidden: Boolean(rule.when) }, "No conditions yet: this applies to every request. Add one, or remove this rule.");
		const setText = () => { text.textContent = rule.when ? toText(rule.when) : "(no conditions yet)"; hint.hidden = Boolean(rule.when); };
		const toggleBtn = h("button", { type: "button", class: "prm-when-btn", "aria-expanded": String(open.has(rule)), title: "Edit the conditions" }, text, icon("chevronDown", 12));
		const builder = ruleBuilder({ value: rule.when ?? null, fields, lead: "", empty: "Add a rule to say when this value applies.", onChange: (tree) => { if (tree) rule.when = tree; else delete rule.when; setText(); emit(); } });
		builderHost.append(builder.el, hint);
		toggleBtn.addEventListener("click", () => {
			if (open.has(rule)) open.delete(rule); else open.add(rule);
			builderHost.hidden = !open.has(rule);
			toggleBtn.setAttribute("aria-expanded", String(open.has(rule)));
		});
		setText();
		const conds = s.rules.filter(isCond);
		const pos = conds.indexOf(rule);
		const move = (dir) => {
			const a = s.rules.indexOf(rule);
			const other = s.rules.indexOf(conds[pos + dir]);
			[s.rules[a], s.rules[other]] = [s.rules[other], s.rules[a]];
			emit(); paint();
			el.querySelectorAll(".prm-rule")[pos + dir]?.querySelector(dir < 0 ? ".prm-up" : ".prm-down")?.focus();
		};
		return h("div", { class: "prm-rule", role: "group", "aria-label": `Rule ${pos + 1} for ${spec.label}` },
			h("div", { class: "prm-rule-head" },
				h("span", { class: "prm-lead" }, pos === 0 ? "Only when" : "Else when"),
				toggleBtn,
				h("span", { class: "spacer" }),
				conds.length > 1 ? h("span", { class: "row", style: { gap: "2px" } },
					h("button", { type: "button", class: "icon-btn prm-up", title: "Check this rule earlier", "aria-label": "Move rule earlier", disabled: pos === 0, onClick: () => move(-1) }, icon("arrowUp", 13)),
					h("button", { type: "button", class: "icon-btn prm-down", title: "Check this rule later", "aria-label": "Move rule later", disabled: pos === conds.length - 1, onClick: () => move(1) }, icon("arrowDown", 13)),
				) : null,
				h("button", { type: "button", class: "icon-btn danger", title: "Remove this rule", "aria-label": "Remove rule", onClick: () => { s.rules = s.rules.filter((r) => r !== rule); if (!s.rules.length) s = null; emit(); paint(); } }, icon("x", 13)),
			),
			builderHost,
			h("div", { class: "prm-then" }, h("span", { class: "prm-lead" }, "Use"), h("div", { class: "prm-value" }, valueControl(spec, rule.value, (v) => { rule.value = v; emit(); }, `${spec.label} when the conditions hold`))),
		);
	}

	// A new rule starts from the value it would most likely replace, nudged so it is not a no-op.
	function addRule() {
		s ??= { mode: firstMode, rules: [] };
		const conds = s.rules.filter(isCond);
		const base = s.rules.find((r) => !isCond(r));
		const from = (base ?? conds.at(-1))?.value ?? spec.default ?? startValue(spec);
		const options = spec.options ?? [];
		const here = options.findIndex((o) => same(o.value, from));
		const rule = { value: spec.kind === "choice" && options.length > 1 ? options[(here + 1) % options.length].value : clone(from) };
		conditional.add(rule); open.add(rule);
		s.rules.splice(base ? s.rules.indexOf(base) : s.rules.length, 0, rule);
		emit(); paint();
		el.querySelectorAll(".prm-rule")[conds.length]?.querySelector(".rule-field, .rule-adds .btn")?.focus();
	}

	// The pill, reset and footer change with every edit; the value control does not, so a
	// slider keeps its drag and a segmented control keeps focus while the property gets set.
	function paintChrome() {
		el.classList.toggle("on", Boolean(s));
		status.replaceChildren();
		if (s) {
			const m = MODE[s.mode] ?? MODE.force;
			status.append(modes.length > 1
				? h("button", { type: "button", class: `prm-mode ${s.mode}`, title: m.title, "aria-label": `${spec.label}: ${m.label}. ${m.title}`, onClick: () => { s.mode = s.mode === "force" ? "default" : "force"; emit(); paintChrome(); status.querySelector(".prm-mode")?.focus(); } }, icon(m.icon, 11), m.label)
				: h("span", { class: "prm-mode force fixed", title: "This property is always applied" }, icon("lock", 11), "Always"));
			status.append(h("button", { type: "button", class: "icon-btn prm-reset", title: "Back to the model's default", "aria-label": `Reset ${spec.label} to the model's default`, onClick: () => { s = null; emit(); paint(); el.querySelector(".prm-value button, .prm-value input")?.focus(); } }, icon("refresh", 13)));
		}
		tail.replaceChildren(
			h("button", { type: "button", class: "btn sm ghost prm-add", title: "Use a different value only for requests that match conditions", onClick: addRule }, icon("plus", 13), "Only when…"),
			s && spec.realize ? h("span", { class: "prm-realize", title: spec.realize }, icon("info", 12), h("span", null, spec.realize)) : null,
		);
	}

	// The tile repaints itself, then tells its group so a tile that grew can take the whole row.
	const paint = () => { render(); onLayout?.(); };
	function render() {
		el.replaceChildren();
		const conds = (s?.rules ?? []).filter(isCond);
		el.classList.toggle("wide", spec.kind === "range" || conds.length > 0);
		el.append(h("div", { class: "prm-head" },
			h("div", { class: "prm-title" }, h("div", { class: "prm-label" }, spec.label), h("div", { class: "prm-help", id: helpId }, spec.help)),
			status,
		));
		const body = h("div", { class: "prm-body", "aria-describedby": helpId });
		for (const rule of conds) body.append(ruleBlock(rule));
		const base = s?.rules.find((r) => !isCond(r));
		const control = valueControl(spec, base ? base.value : (spec.default ?? startValue(spec)), (v) => {
			if (!s) s = { mode: firstMode, rules: [] };
			const b = s.rules.find((r) => !isCond(r));
			if (b) b.value = v; else s.rules.push({ value: v });
			emit(); paintChrome();
		}, conds.length ? `${spec.label} otherwise` : spec.label);
		body.append(h("div", { class: "prm-then base" }, conds.length ? h("span", { class: "prm-lead" }, "Otherwise") : null, h("div", { class: "prm-value" }, control)));
		body.append(tail);
		el.append(body);
		paintChrome();
	}
	paint();
	return el;
}

// A stored setting the model no longer offers (after repointing the model).
export function orphanTile({ id, setting, modelName, onRemove }) {
	const d = describeSetting(null, id, setting);
	return h("div", { class: "prm on orphan wide", dataset: { param: id } },
		h("div", { class: "prm-head" },
			h("span", { class: "prm-warn" }, icon("info", 16)),
			h("div", { class: "prm-title" }, h("div", { class: "prm-label" }, d.label, h("span", { class: "tag warn", style: { marginLeft: "8px" } }, "not offered")), h("div", { class: "prm-help" }, `${modelName} does not have this property, so saving would be refused. It was set to ${d.text} (${modeWords[d.mode] ?? d.mode}).`)),
			btn("Remove", { icon: "trash", cls: "sm danger", onClick: onRemove }),
		),
	);
}
