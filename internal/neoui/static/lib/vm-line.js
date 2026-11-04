// A virtual model in one or two lines, the way the Virtual models list reads it:
// the model that answers first, the properties it always carries, and how many
// fallbacks stand behind it. Used where a person picks or reviews the models
// they may ask for (Connect, My models).

import { h, icon, plural } from "./dom.js";
import { neo } from "./client.js";
import { providerDot, providerLabel } from "./components.js";
import { propChips } from "./vm-props.js";

// Loads the parameter specs of every model the given virtual models start with,
// so the chips carry the model's own labels. Never throws.
export async function loadVmSpecs(vms) {
	const specs = new Map();
	const wanted = new Set(vms.flatMap((vm) => vm.stages.filter((s) => Object.keys(s.params ?? {}).length).map((s) => s.model)));
	await Promise.allSettled([...wanted].map((m) => neo.modelParams(m).then((r) => specs.set(m, r?.specs ?? []))));
	return specs;
}

export function vmLine(vm, { models = [], providers = [], specs = new Map(), fallbacks = true } = {}) {
	const [first, ...rest] = vm.stages ?? [];
	if (!first) return h("div", { class: "vml" }, h("span", { class: "ink-3" }, "No model yet"));
	const m = models.find((x) => x.id === first.model);
	const provider = providers.find((p) => p.id === (m?.providerId ?? first.model.split("/")[0]));
	const chips = propChips(first, specs.get(first.model), { short: false });
	return h("div", { class: "vml" },
		h("div", { class: "vml-model" }, providerDot(provider, 8), h("b", null, m?.display ?? first.model.split("/").slice(1).join("/")), h("span", { class: "ink-3" }, providerLabel(provider).main)),
		chips.length || (fallbacks && rest.length) ? h("div", { class: "vml-chips" }, chips, fallbacks && rest.length ? h("span", { class: "vml-fb", title: rest.map((s) => models.find((x) => x.id === s.model)?.display ?? s.model).join(", ") }, icon("fork", 11), `then ${plural(rest.length, "fallback")}`) : null) : null);
}
