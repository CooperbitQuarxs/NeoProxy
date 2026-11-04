// The virtual model editor. A virtual model is a model plus properties that are
// always set; the chain of fallbacks is an optional extra. With one model the page
// is that model's card and nothing else. "Add a fallback" turns it into a chain
// laid out as a grid of cards, each with its own properties.

import { h, icon, clear, plural } from "../../lib/dom.js";
import { neo, engine, ApiError } from "../../lib/client.js";
import { pageHead, btn, confirm, toast, spinner, errorState, setDrawModes } from "../../lib/components.js";
import { DEFAULT_FIELDS } from "../../lib/rules.js";
import { luaFunctions } from "../../lib/lua-editor.js";
import { paramIssues } from "../../lib/param-model.js";
import { propChip, stageProps } from "../../lib/vm-props.js";
import { followMatches } from "../../lib/follow.js";
import { stageCard, newStage, MAX_STAGES } from "./stage-card.js";
import { luaPolicy, isScriptError } from "./lua-policy.js";
import { tryIt } from "./try-it.js";
import { grants } from "./grants.js";
import { terminal } from "./terminal.js";

export async function editor(ctx, id) {
	const { root } = ctx;
	const isNew = id === "new";
	const isAdmin = ctx.session.role === "admin";
	root.append(spinner());
	let vm, models, vault, users;
	try {
		[models, vault, users] = await Promise.all([neo.models(), neo.vault(), neo.users()]);
		vm = isNew
			? { id: null, name: "", served: "neoproxy/", description: "", stages: [newStage("")], terminal: { kind: "limit", message: "" } }
			: await neo.virtualModel(id);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.navigate("virtual-models")));
		return;
	}
	root.lastChild.remove();

	const draft = structuredClone(vm);
	draft.stages.forEach((s) => { s.params ??= {}; });
	let dirty = isNew;
	let ruleFields = DEFAULT_FIELDS;
	const drawListeners = new Set();
	const specPromises = new Map();
	const specLists = new Map();
	const fresh = new Map(); // stage id → properties a new fallback inherits from the one before it
	const cards = new Map();
	const expandedIds = new Set();
	const errors = {};

	engine.get("/v8/neo/virtual-models/draws").then((r) => { setDrawModes(r?.draws); for (const fn of drawListeners) fn(); }).catch(() => {});
	engine.get("/v8/neo/virtual-models/fields").then((r) => { if (Array.isArray(r?.fields) && r.fields.length) ruleFields = r.fields.map((f) => ({ ...f, kind: f.kind === "number" && /usage|average/.test(f.name) ? "percent" : f.kind })); }).catch(() => {});

	const exposedProviders = () => new Set(vault.providers.filter((p) => p.exposed).map((p) => p.id));
	const env = {
		models, vault, users, draft, vm, isAdmin, isNew,
		usableModels: () => { const ok = exposedProviders(); return models.filter((m) => m.exposed && ok.has(m.providerId)); },
		// Facts a property's "only when" may read: about the request, not about accounts or pools.
		paramFields: () => ruleFields.filter((f) => f.scope === "request" && !f.name.startsWith("pool.")),
		ruleFields: () => ruleFields,
		onDrawCatalogue: (fn) => drawListeners.add(fn),
		luaNames: () => (lua ? lua.names() : luaFunctions(draft.script ?? "")),
		luaChanged: () => { for (const c of cards.values()) c.refreshLua(); },
		specsFor(model) {
			let p = specPromises.get(model);
			if (!p) {
				p = neo.modelParams(model).then((r) => { const list = r?.specs ?? []; specLists.set(model, list); return list; });
				p.catch(() => specPromises.delete(model));
				specPromises.set(model, p);
			}
			return p;
		},
		specsNow: (model) => specLists.get(model),
		toast,
		touch,
		isDirty: () => dirty,
		openLua: (o) => lua?.open(o),
		scriptMessage: (m) => m,
	};

	// ----- head: the title is the name, the line under it the description -----
	const saveBtn = btn("Save", { icon: "save", cls: "primary", onClick: save });
	const nameError = h("div", { class: "vm-name-error", id: "vm-name-error", role: "alert", hidden: true });
	// The name field is as wide as its text, measured in its own font (ch is unreliable across the mono fallbacks).
	const measure = document.createElement("canvas").getContext("2d");
	const fitName = () => {
		measure.font = getComputedStyle(nameInput).font;
		nameInput.style.width = `${Math.ceil(measure.measureText(nameInput.value || nameInput.placeholder).width) + 16}px`;
	};
	const nameInput = h("input", {
		class: "vm-title-input",
		value: draft.name,
		placeholder: "luna-fast",
		spellcheck: false,
		autocomplete: "off",
		"aria-label": "Name callers type as the model id, after neoproxy/",
		"aria-describedby": "vm-name-error",
		onInput: (e) => {
			draft.name = e.target.value.trim().toLowerCase();
			draft.served = `neoproxy/${draft.name}`;
			errors.name = /^[a-z][a-z0-9-]{0,31}$/.test(draft.name) ? null : (draft.name && !/^[a-z]/.test(draft.name) ? "Start with a letter; then lowercase letters, digits and dashes." : "A letter first, then lowercase letters, digits and dashes, up to 32.");
			if (models.some((m) => m.id === draft.name)) errors.name = "That name is already a served model.";
			renderIdentityErrors();
			fitName();
			touch();
		},
	});
	const title = h("label", { class: "vm-title", id: "vm-title", title: "Click to rename" }, h("span", { class: "pre" }, "neoproxy/"), nameInput, isNew ? h("span", { class: "badge accent" }, "new") : null);
	const descInput = h("input", { class: "vm-desc-input", value: draft.description ?? "", placeholder: "Add a one-line description people see next to the name", maxlength: 200, "aria-label": "Description", onInput: (e) => { draft.description = e.target.value; touch(); } });
	const summary = h("span", { class: "vm-summary", "aria-label": "Summary of what this model serves" });
	root.append(pageHead({
		back: { href: "#/virtual-models", label: "Virtual models" },
		title,
		desc: h("div", { class: "vm-head-desc" }, nameError, descInput, summary),
		actions: [
			isNew ? null : btn("Delete", { icon: "trash", cls: "danger", onClick: remove }),
			btn("Discard", { cls: "outline", title: "Throw away unsaved edits", onClick: () => (dirty ? discard() : toast("Nothing to discard")) }),
			saveBtn,
		],
	}));
	fitName();
	document.fonts?.ready.then(fitName);

	function setDirty(v = true) {
		dirty = v;
		saveBtn.textContent = "";
		saveBtn.append(icon("save", 15), dirty ? "Save changes" : "Saved");
		saveBtn.disabled = !dirty;
		if (v) sim?.stale();
	}
	function touch() {
		setDirty();
		renderSummary();
	}

	// The live summary: what the first model serves and the properties it is always given.
	function renderSummary() {
		const first = draft.stages[0];
		const nodes = [icon("arrowRight", 14)];
		if (!first?.model) nodes.push(h("span", { class: "ink-3" }, "Choose a model to serve"));
		else {
			const m = models.find((x) => x.id === first.model);
			const specs = env.specsNow(first.model) ?? [];
			nodes.push(h("b", { class: "vm-summary-model" }, m?.display ?? first.model));
			if (first.follow) nodes.push(h("span", { class: "prop follow", title: `Follows the newest model matching ${first.follow}` }, icon("link", 11), h("span", { class: "prop-v" }, "newest")));
			const own = stageProps(first, specs);
			for (const p of own) nodes.push(propChip(p));
			if (!own.length) nodes.push(h("span", { class: "ink-3" }, "no properties set"));
			if (draft.stages.length > 1) nodes.push(h("span", { class: "ink-3" }, `then ${plural(draft.stages.length - 1, "fallback")}`));
		}
		summary.replaceChildren(...nodes);
	}

	function renderIdentityErrors() {
		nameError.hidden = !errors.name;
		nameError.textContent = errors.name ?? "";
		nameInput.setAttribute("aria-invalid", String(Boolean(errors.name)));
	}

	// ----- models: one card, or a chain -----
	const chainHost = h("div", { class: "vm-chain" });
	const status = h("div", { class: "sr-only", role: "status" });
	let litIndex = null;

	function renderChain(focus) {
		for (const [stageId, c] of cards) if (c.expanded) expandedIds.add(stageId);
		cards.clear();
		clear(chainHost);
		const chain = draft.stages.length > 1;
		const make = (s, index) => {
			const c = stageCard(env, s, {
				chain, index, count: draft.stages.length,
				open: expandedIds.has(s.id) || fresh.has(s.id),
				seed: fresh.get(s.id),
				onRemove: () => removeStage(s),
				onMove: (dir) => moveStage(s, dir),
			});
			fresh.delete(s.id);
			cards.set(s.id, c);
			if (litIndex === index) c.setLit(true);
			return c.el;
		};
		if (!chain) {
			chainHost.append(make(draft.stages[0], 0), addFallbackPrompt());
		} else {
			chainHost.append(
				h("div", { class: "vm-chain-head" },
					h("div", { class: "titles" },
						h("h2", { class: "vm-chain-title" }, icon("fork", 16), `Chain of ${draft.stages.length} models`),
						h("p", null, "A request goes to the first model that can serve it. When a model's accounts are spent or it fails, the next one answers. Reorder with the arrows."),
					),
					btn("Add a fallback", { icon: "plus", cls: "outline sm", onClick: addStage }),
				),
				h("div", { class: "vm-chain-grid" }, draft.stages.map(make)),
			);
		}
		chainHost.append(status);
		if (focus) [...(cards.get(focus.id)?.el.querySelectorAll(focus.selector) ?? [])].find((n) => !n.disabled)?.focus({ preventScroll: true });
		renderSummary();
	}
	function addFallbackPrompt() {
		return h("div", { class: "vm-addfb" },
			h("span", { class: "vm-addfb-icon" }, icon("fork", 18)),
			h("div", { class: "lt" },
				h("div", { class: "vm-addfb-title" }, "Fallbacks ", h("span", { class: "ink-3" }, "optional")),
				h("div", { class: "vm-addfb-text" }, "If this model can't serve a request (its accounts are spent, or it errors), try another model after it. Each fallback has its own properties."),
			),
			btn("Add a fallback", { icon: "plus", cls: "outline", onClick: addStage }),
		);
	}

	const exposedUsable = () => env.usableModels();
	// A fallback starts as the next sibling of the last model that is not already in the chain.
	function nextModel(prev) {
		const usable = exposedUsable();
		const inChain = new Set(draft.stages.map((s) => s.model));
		const live = new Set(vault.accounts.filter((a) => a.state === "healthy").map((a) => a.providerId));
		const shared = (a, b) => { let n = 0; while (n < a.length && a[n] === b[n]) n++; return n; };
		const sameProvider = prev ? usable.filter((m) => m.providerId === prev.model.split("/")[0]).sort((a, b) => shared(b.id, prev.model) - shared(a.id, prev.model) || (b.id < prev.model) - (a.id < prev.model) || (a.id < b.id ? 1 : -1)) : [];
		return (sameProvider.find((m) => !inChain.has(m.id))
			?? usable.find((m) => live.has(m.providerId) && !inChain.has(m.id))
			?? sameProvider[0]
			?? usable[0])?.id;
	}
	function addStage() {
		if (draft.stages.length >= MAX_STAGES) { toast(`A chain holds at most ${MAX_STAGES} models.`, "warn"); return; }
		const prev = draft.stages.at(-1);
		if (!prev?.model) { toast("Choose the first model before adding a fallback.", "warn"); return; }
		const model = nextModel(prev);
		if (!model) { toast("No exposed provider models are available. Add an account and expose its catalogue first.", "warn"); return; }
		const stage = newStage(model);
		// Same accounts, draw mode, window and thresholds as the one before, so a fallback never means re-entering numbers.
		Object.assign(stage, structuredClone({ accounts: prev.accounts, draw: prev.draw, perAccount: prev.perAccount, cap: prev.cap, window: prev.window, per: prev.per }));
		if (prev.lua) stage.lua = structuredClone(prev.lua);
		if (prev.condition) stage.condition = structuredClone(prev.condition);
		if (model.split("/")[0] !== prev.model.split("/")[0]) { stage.accounts = "all"; delete stage.lua; }
		fresh.set(stage.id, structuredClone(prev.params ?? {}));
		draft.stages.push(stage);
		setDirty();
		renderChain({ id: stage.id, selector: ".vm-source .btn" });
		cards.get(stage.id)?.el.scrollIntoView({ block: "nearest", behavior: "smooth" });
		status.textContent = `Added a fallback: ${models.find((m) => m.id === model)?.display ?? model}`;
	}
	function removeStage(stage) {
		const index = draft.stages.indexOf(stage);
		draft.stages.splice(index, 1);
		expandedIds.delete(stage.id);
		setDirty();
		const next = draft.stages[Math.min(index, draft.stages.length - 1)];
		renderChain({ id: next.id, selector: ".vm-stage-tools .btn:last-child, .vm-source .btn" });
		status.textContent = draft.stages.length === 1 ? "Back to a single model" : `Removed; ${draft.stages.length} models left`;
	}
	function moveStage(stage, dir) {
		const a = draft.stages.indexOf(stage);
		const b = a + dir;
		if (b < 0 || b >= draft.stages.length) return;
		[draft.stages[a], draft.stages[b]] = [draft.stages[b], draft.stages[a]];
		setDirty();
		renderChain({ id: stage.id, selector: dir < 0 ? ".vm-stage-tools .btn:nth-child(1), .vm-stage-tools .btn:nth-child(2)" : ".vm-stage-tools .btn:nth-child(2), .vm-stage-tools .btn:nth-child(1)" });
		status.textContent = `Moved ${models.find((m) => m.id === stage.model)?.display ?? stage.model} to position ${b + 1} of ${draft.stages.length}`;
	}

	// ----- end of chain, Lua, access, simulation -----
	let end = null;
	const endHost = h("div");
	function renderEnd() { end = terminal(env); endHost.replaceChildren(end.el); }

	let lua = null;
	if (isAdmin) lua = luaPolicy(env);
	env.scriptMessage = (m) => (lua ? lua.scriptMessage(m) : m);
	const grantsCard = grants(env);
	const sim = tryIt({
		...env,
		onResult: (result) => {
			// Only a chain needs to point at the model that answered; a single model always does.
			litIndex = result && draft.stages.length > 1 ? (result.stageIndex ?? null) : null;
			draft.stages.forEach((s, i) => cards.get(s.id)?.setLit(litIndex === i));
		},
	});

	// ----- layout -----
	const main = h("div", { class: "vm-main" }, chainHost, endHost);
	const side = h("div", { class: "vm-side" }, sim.el, grantsCard.el, lua?.el);
	root.append(h("div", { class: "vm-shell" }, h("div", { class: "vm-layout" }, main, side)));

	// ----- save / discard / delete -----
	function describeStage(index) {
		const s = draft.stages[index];
		const name = models.find((m) => m.id === s.model)?.display ?? "the model";
		return draft.stages.length > 1 ? `Model ${index + 1} (${name})` : name;
	}
	function focusStage(index, message) {
		const s = draft.stages[index];
		expandedIds.add(s.id);
		renderChain();
		cards.get(s.id)?.el.scrollIntoView({ block: "center", behavior: "smooth" });
		toast(message, "warn");
	}
	async function save() {
		if (errors.name || !draft.name) {
			errors.name ??= "a name is required";
			renderIdentityErrors();
			nameInput.focus();
			return;
		}
		const defined = new Set(luaFunctions(draft.script ?? ""));
		for (const [index, stage] of draft.stages.entries()) {
			if (!stage.model) return focusStage(index, "Choose a model first.");
			if (stage.follow !== undefined && !followMatches(stage.follow, env.usableModels()).length && !stage.follow.includes("*")) return focusStage(index, `${describeStage(index)}: the pattern needs a * where the version goes, like codex/gpt-*-luna.`);
			if (stage.accounts !== "all" && !stage.accounts.length) return focusStage(index, `${describeStage(index)} has no accounts. Pick at least one.`);
			const specs = env.specsNow(stage.model);
			const issues = specs ? paramIssues(stage.params, specs) : [];
			if (issues.length) return focusStage(index, `${describeStage(index)}: ${issues[0].text}`);
			const missing = Object.values(stage.lua ?? {}).filter((name) => !defined.has(name));
			if (missing.length) {
				focusStage(index, `${describeStage(index)} uses ${missing.join(", ")}, which the script does not define.`);
				lua?.open({ focus: false });
				lua?.setError(`${describeStage(index)} uses ${missing.join(", ")}, which the script does not define. Add the function, or choose Built-in on the model.`);
				return;
			}
		}
		try {
			const body = structuredClone(draft);
			for (const s of body.stages) { if (!Object.keys(s.params ?? {}).length) s.params = {}; }
			const saved = await neo.saveVirtualModel(body);
			lua?.setError(null);
			toast(`Saved neoproxy/${saved.name}`);
			if (isNew) ctx.navigate(`virtual-models/${saved.id}`);
			else {
				Object.assign(vm, saved);
				// The server's copy replaces the stage objects, so the cards must rebind or their edits would land on stages that are no longer saved.
				Object.assign(draft, structuredClone(saved));
				draft.stages.forEach((s) => { s.params ??= {}; });
				setDirty(false);
				renderChain();
				renderEnd();
				sim.run();
			}
		} catch (e) {
			if (isAdmin && isScriptError(e)) {
				lua?.open({ focus: false });
				lua?.setError(e.message);
				toast("Not saved: the routing script has an error.", "bad");
			} else toast(e instanceof ApiError ? e.message : "Could not save", "bad");
		}
	}
	async function discard() {
		if (!(await confirm({ title: "Discard changes?", text: "The model goes back to the last saved version. You stay on this page.", confirmLabel: "Discard", danger: true }))) return;
		if (isNew) { ctx.navigate("virtual-models"); return; }
		ctx.rerender();
	}
	async function remove() {
		const granted = users.filter((u) => u.grants.some((g) => g.virtualModel === draft.id));
		if (await confirm({ title: `Delete neoproxy/${draft.name}?`, text: granted.length ? `${plural(granted.length, "user")} will lose it. Requests for it start failing with an unknown-model error.` : "Requests for it start failing with an unknown-model error.", confirmLabel: "Delete", danger: true })) {
			await neo.deleteVirtualModel(draft.id);
			toast(`Deleted neoproxy/${draft.name}`);
			ctx.navigate("virtual-models");
		}
	}

	setDirty(isNew);
	renderChain();
	renderEnd();
	renderSummary();
	// Specs arrive after the cards load them; the summary then gets its real labels.
	Promise.allSettled(draft.stages.filter((s) => s.model).map((s) => env.specsFor(s.model))).then(renderSummary);
	if (!isNew) sim.run();
	else requestAnimationFrame(() => nameInput.focus());
	return () => lua?.dispose();
}
