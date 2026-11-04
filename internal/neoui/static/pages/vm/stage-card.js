// One model of a virtual model: which model (pinned, or the newest match of a
// pattern), the properties it is always served with, and how it draws on accounts.
// A virtual model with one model shows this as a single card and nothing else;
// a chain shows one of these per model in a grid.

import { h, icon } from "../../lib/dom.js";
import { btn, iconBtn, modelPicker, providerDot, providerLabel, servedName, seg, input, spinner, errorState } from "../../lib/components.js";
import { groupSpecs, valueError } from "../../lib/param-model.js";
import { paramTile, orphanTile } from "../../lib/params.js";
import { propChips } from "../../lib/vm-props.js";
import { followMatches, suggestGlobs } from "../../lib/follow.js";
import { poolSettings, poolLine } from "./pool-settings.js";

export const MAX_STAGES = 128;
export const newStageId = () => `s${Math.random().toString(36).slice(2, 7)}`;
export const newStage = (model) => ({ id: newStageId(), model, accounts: "all", draw: "least-used", perAccount: 0.5, cap: 0.7, window: "5h", per: "all", params: {} });

// env: { models, vault, usableModels(), specsFor(id), paramFields(), ruleFields(), luaNames(), isAdmin, onDrawCatalogue(fn), touch(), toast(text, kind) }
// opts: { chain, index, count, open, seed, onRemove(), onMove(dir) }
export function stageCard(env, s, opts) {
	const { chain = false, index = 0, count = 1 } = opts;
	let expanded = !chain || Boolean(opts.open);
	let pickerOpen = !s.model;
	let specs = [];
	let token = 0;
	let pool = null;
	let seed = opts.seed ?? null;
	let lit = false;

	const providerId = () => s.model.split("/")[0];
	const model = () => env.models.find((m) => m.id === s.model);
	const provider = () => env.vault.providers.find((p) => p.id === providerId());

	const head = h("div", { class: "vm-stage-head" });
	const sourceHost = h("div", { class: "vm-source" });
	const pickerHost = h("div", { class: "vm-picker-host" });
	const chipsHost = h("div", { class: "vm-stage-chips" });
	const paramsHost = h("div", { class: "vm-params" });
	const more = h("details", { class: "vm-more" });
	const moreSummary = h("summary", null, h("span", { class: "vm-more-title" }, icon("sliders", 14), "Accounts, limits and rules"), h("span", { class: "vm-more-line" }));
	const bodyHost = h("div", { class: "vm-stage-body" }, sourceHost, pickerHost, paramsHost, more);
	const el = h("section", { class: `card vm-stage ${chain ? "chain" : "single"}`, dataset: { stage: s.id }, "aria-label": chain ? `Model ${index + 1} of ${count}` : "Model" }, head, chipsHost, bodyHost);

	const lineText = () => poolLine(env.vault, s);
	const renderMoreSummary = () => { moreSummary.querySelector(".vm-more-line").textContent = lineText(); };
	more.append(moreSummary);
	renderMoreSummary();
	// The accounts panel is built the first time it opens (and again after the provider changes).
	function buildPool() {
		pool?.el.remove();
		pool = poolSettings(env, s, { onChange: () => { renderMoreSummary(); renderChips(); env.touch(); } });
		more.append(pool.el);
	}
	more.addEventListener("toggle", () => { if (more.open && !pool) buildPool(); });

	// ----- head -----
	function renderHead() {
		const m = model();
		const p = provider();
		const label = providerLabel(p);
		const title = chain ? "h3" : "h2";
		const role = !chain ? null : index === 0 ? "First choice" : `Fallback ${index}`;
		const toolsLeft = [];
		if (chain) {
			toolsLeft.push(
				iconBtn("arrowLeft", `Move ${m?.display ?? "this model"} earlier`, () => opts.onMove(-1), "sm"),
				iconBtn("arrowRight", `Move ${m?.display ?? "this model"} later`, () => opts.onMove(1), "sm"),
			);
			toolsLeft[0].disabled = index === 0;
			toolsLeft[1].disabled = index === count - 1;
		}
		const tools = h("div", { class: "vm-stage-tools" },
			...toolsLeft,
			chain ? iconBtn("trash", `Remove ${m?.display ?? "this model"} from the chain`, () => opts.onRemove(), "sm danger") : null,
			chain ? btn(expanded ? "Done" : "Edit", { icon: expanded ? "chevronUp" : "sliders", cls: "outline sm", onClick: () => setExpanded(!expanded), title: expanded ? "Collapse this model" : "Edit this model's properties and limits" }) : null,
		);
		tools.querySelector(".btn:last-child")?.setAttribute("aria-expanded", String(expanded));
		more.classList.toggle("hidden", !s.model);
		head.replaceChildren(
			chain ? h("span", { class: "vm-idx", "aria-hidden": "true" }, String(index + 1)) : providerDot(p, 12),
			h("div", { class: "lt" },
				role ? h("div", { class: "vm-role" }, role, lit ? h("span", { class: "badge ok", style: { marginLeft: "8px" } }, "a request lands here") : null) : (lit ? h("div", { class: "vm-role" }, h("span", { class: "badge ok" }, "a request lands here")) : null),
				h(title, { class: "vm-model-name" }, chain ? providerDot(p, 9) : null, m?.display ?? (s.model ? "Unavailable model" : "Choose a model")),
				s.model ? h("div", { class: "vm-model-sub" }, `${label.main}${label.source ? ` · ${label.source}` : ""}`, h("span", { class: "vm-sub-id" }, servedName(`neoproxy/${s.model}`))) : h("div", { class: "vm-model-sub" }, "Pick the provider's model below"),
			),
			tools,
		);
	}

	// ----- model source: pinned, or follow the newest match -----
	function resolveFollow() {
		const matches = followMatches(s.follow, env.usableModels());
		if (matches[0] && matches[0].id !== s.model) {
			s.model = matches[0].id;
			modelChanged();
		}
		return matches;
	}
	function renderSource() {
		const following = s.follow !== undefined;
		const mode = seg([{ value: "pin", label: "This model", title: "Always serve exactly this model" }, { value: "follow", label: "Newest match", title: "Serve the newest model that matches a pattern, so new versions need no edit" }], following ? "follow" : "pin", (v) => {
			if (v === "follow") s.follow = suggestGlobs(s.model)[0] ?? "";
			else delete s.follow;
			env.touch();
			renderSource();
			if (v === "follow") resolveFollow();
			renderHead();
			renderChips();
		});
		mode.setAttribute("aria-label", "Which model this serves");
		const buttons = [mode, btn(pickerOpen ? "Close the list" : s.model ? "Change model" : "Choose model", { icon: pickerOpen ? "chevronUp" : "box", cls: "outline sm", onClick: () => { pickerOpen = !pickerOpen; renderSource(); renderPicker(); }, title: following ? "Pick a model to base the pattern on" : "Pick a different model" })];
		sourceHost.replaceChildren(h("div", { class: "vm-source-row" }, ...buttons));
		buttons[1].setAttribute("aria-expanded", String(pickerOpen));
		if (!following) return;
		const matches = followMatches(s.follow, env.usableModels());
		const patternId = `vm-follow-${s.id}`;
		const resolution = h("div", { class: "vm-follow-res", "aria-live": "polite" });
		const paintResolution = (list) => {
			const top = list[0];
			resolution.className = `vm-follow-res ${top ? "" : "bad"}`;
			resolution.replaceChildren(top
				? h("span", null, "Serves ", h("b", null, top.display), " ", h("code", null, top.id), list.length > 1 ? h("span", { class: "ink-3" }, ` · newest of ${list.length} matches`) : h("span", { class: "ink-3" }, " · the only match"))
				: h("span", null, s.follow && s.follow.includes("*") ? "No exposed model matches yet. It will keep serving the model above until one does." : "Add a * where the version goes, like codex/gpt-*-luna."));
		};
		const pattern = input({ class: "input mono", id: patternId, value: s.follow ?? "", placeholder: "codex/gpt-*-luna", spellcheck: "false", autocomplete: "off", "aria-describedby": `${patternId}-r` });
		resolution.id = `${patternId}-r`;
		pattern.addEventListener("input", () => {
			s.follow = pattern.value.trim();
			paintResolution(resolveFollow());
			renderHead();
			env.touch();
		});
		const suggestions = suggestGlobs(s.model).filter((g) => g !== s.follow);
		sourceHost.append(h("div", { class: "vm-follow" },
			h("label", { for: patternId, class: "vm-follow-label" }, "Pattern ", h("span", { class: "ink-3" }, "* stands for the version")),
			pattern,
			suggestions.length ? h("div", { class: "chips", role: "group", "aria-label": "Pattern suggestions" }, suggestions.map((g) => h("button", { type: "button", class: "chip-btn", onClick: () => { pattern.value = g; pattern.dispatchEvent(new Event("input")); } }, g))) : null,
			resolution,
		));
		paintResolution(matches);
	}

	function renderPicker() {
		pickerHost.replaceChildren();
		if (!pickerOpen) return;
		pickerHost.append(
			modelPicker({
				providers: env.vault.providers, models: env.models, accounts: env.vault.accounts, value: s.model,
				onPick: (m) => {
					if (s.model && m.providerId !== providerId()) {
						s.accounts = "all";
						delete s.lua;
						if (more.open) { s.model = m.id; buildPool(); } else { pool?.el.remove(); pool = null; }
					}
					s.model = m.id;
					if (s.follow !== undefined) s.follow = suggestGlobs(m.id)[0] ?? "";
					pickerOpen = false;
					modelChanged();
					renderSource();
					renderPicker();
					sourceHost.querySelector(".btn")?.focus({ preventScroll: true });
				},
			}),
			h("div", { class: "hint", style: { marginTop: "6px" } }, "Only providers the core holds and you serve. Hidden providers and models do not appear."),
		);
	}

	function modelChanged() {
		renderHead();
		renderMoreSummary();
		renderChips();
		loadParams();
		env.touch();
	}

	// ----- properties -----
	function renderChips() {
		const chips = propChips(s, specs);
		chipsHost.classList.toggle("hidden", expanded);
		chipsHost.replaceChildren(
			...(chips.length ? chips : [h("span", { class: "ink-3" }, "No properties set: the model behaves as it does by default.")]),
			h("span", { class: "vm-stage-pool" }, lineText()),
		);
	}

	async function loadParams() {
		const mine = ++token;
		if (!s.model) { specs = []; paramsHost.replaceChildren(); return; }
		paramsHost.replaceChildren(h("div", { class: "vm-loading" }, spinner("Loading this model's properties")));
		try {
			specs = await env.specsFor(s.model);
		} catch (e) {
			if (mine !== token) return;
			specs = [];
			const failure = errorState(e, loadParams);
			paramsHost.replaceChildren(failure);
			return;
		}
		if (mine !== token) return;
		if (seed) {
			// A new fallback starts with the properties of the one before it, where this model supports them.
			for (const [id, setting] of Object.entries(seed)) {
				const spec = specs.find((x) => x.id === id);
				if (spec && !(s.params?.[id]) && (setting.rules ?? []).every((r) => !valueError(spec, r.value))) (s.params ??= {})[id] = structuredClone(setting);
			}
			seed = null;
			env.touch();
		}
		renderParams();
		renderChips();
		const lost = Object.keys(s.params ?? {}).filter((id) => !specs.some((x) => x.id === id));
		if (lost.length) env.toast(`${model()?.display ?? "This model"} does not offer ${lost.length === 1 ? "one of the properties you set" : `${lost.length} of the properties you set`}. Review ${lost.length === 1 ? "it" : "them"} under “Needs attention”.`, "warn");
	}

	function renderParams() {
		paramsHost.replaceChildren();
		const params = (s.params ??= {});
		const orphans = Object.keys(params).filter((id) => !specs.some((x) => x.id === id));
		const counts = [];
		const refreshCounts = () => counts.forEach(([node, list]) => {
			const n = list.filter((x) => params[x.id]).length;
			node.textContent = n ? `${n} set` : "";
		});
		const onTile = (spec) => (next) => {
			if (next) params[spec.id] = next; else delete params[spec.id];
			refreshCounts();
			renderChips();
			env.touch();
		};
		if (orphans.length) {
			paramsHost.append(h("section", { class: "prm-group attention" },
				h("h4", { class: "prm-group-title" }, "Needs attention"),
				h("div", { class: "prm-grid" }, orphans.map((id) => orphanTile({ id, setting: params[id], modelName: model()?.display ?? "This model", onRemove: () => { delete params[id]; renderParams(); renderChips(); env.touch(); } }))),
			));
		}
		if (!specs.length && !orphans.length) {
			paramsHost.append(h("div", { class: "vm-noprops" }, icon("sliders", 18), h("div", null, h("div", { class: "vm-noprops-title" }, "No adjustable properties"), h("div", { class: "hint" }, "NeoProxy lists a property only when the model's own facts say it is supported, so nothing here is guessed. Its accounts and limits are still below."))));
			return;
		}
		for (const { group, specs: list } of groupSpecs(specs)) {
			const count = h("span", { class: "prm-group-count" });
			counts.push([count, list]);
			const section = h("section", { class: "prm-group", "aria-label": group });
			// A group lays out beside its neighbours until one of its tiles needs the room (a slider, a rule).
			const onLayout = () => section.classList.toggle("wide", Boolean(section.querySelector(".prm.wide")));
			section.append(
				h("h4", { class: "prm-group-title" }, group, count),
				h("div", { class: "prm-grid" }, list.map((spec) => paramTile({ spec, setting: params[spec.id], fields: env.paramFields(), onChange: onTile(spec), onLayout }))),
			);
			onLayout();
			paramsHost.append(section);
		}
		refreshCounts();
	}

	function setExpanded(next) {
		expanded = next;
		el.classList.toggle("open", expanded);
		bodyHost.classList.toggle("hidden", !expanded);
		renderHead();
		renderChips();
		if (expanded && !specs.length && s.model) loadParams();
		if (expanded) head.scrollIntoView({ block: "nearest", behavior: "smooth" });
	}

	renderHead();
	renderSource();
	renderPicker();
	renderChips();
	el.classList.toggle("open", expanded);
	bodyHost.classList.toggle("hidden", !expanded);
	// Chain cards load their properties up front too, so the collapsed chips carry real labels.
	loadParams();

	return {
		el,
		stage: s,
		setLit(on) { lit = on; el.classList.toggle("lit", on); renderHead(); },
		reloadParams: loadParams,
		refreshLua() { pool?.renderLua(); },
		focusEdit() { el.querySelector(".vm-stage-tools .btn:last-child, .vm-source .btn")?.focus(); },
		get specs() { return specs; },
	};
}
