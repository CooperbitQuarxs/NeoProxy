// A virtual model's properties as small chips: "Speed · Fast" with a lock when
// every request gets it, an open lock when the caller may still choose. Used by the
// catalogue, the editor's summary line and the chain cards, so a model reads the
// same everywhere. `specs` is optional: with it the chips carry the model's own
// labels, without it they fall back to the stored ids and raw values.

import { h, icon } from "./dom.js";
import { describeSetting, modeWords } from "./param-model.js";

export function stageProps(stage, specs = []) {
	return Object.entries(stage.params ?? {}).map(([id, setting]) => ({ ...describeSetting(specs.find((s) => s.id === id), id, setting), setting }));
}

export function propChip(p, { short = false } = {}) {
	const how = p.conditional ? (p.onlyConditional ? "only when its conditions hold" : "with exceptions") : modeWords[p.mode] ?? p.mode;
	return h("span", { class: `prop ${p.mode} ${p.conditional ? "cond" : ""}`, title: `${p.label}: ${p.text} (${how})` },
		icon(p.mode === "force" ? "lock" : "unlock", 11),
		short ? null : h("span", { class: "prop-k" }, p.label),
		h("span", { class: "prop-v" }, p.text),
		p.conditional ? h("span", { class: "prop-c" }, "when") : null,
	);
}

export function propChips(stage, specs, opts) {
	return stageProps(stage, specs).map((p) => propChip(p, opts));
}
