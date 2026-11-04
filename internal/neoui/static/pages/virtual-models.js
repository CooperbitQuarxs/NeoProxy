// Virtual models: names the proxy serves that are not a provider's model.
// Each one is a chain of stages; a stage is a real model drawn from a pool of
// accounts until their meters reach a threshold. The catalogue shows every chain
// at a glance; the editor is a rail you click into, with a docked inspector for
// the selected stage, an optional Lua policy, and a simulator that shows where a
// user's request would land and why.

import { h, icon, fmtPct, fmtAgo, plural, clear } from "../lib/dom.js";
import { neo, onChange, ApiError } from "../lib/client.js";
import { meterFor, accountHealth } from "../lib/providers.js";
import { pageHead, card, btn, avatar, servedName, chainRail, inspector, section, field, input, select, seg, modelPicker, trail, meterRing, providerDot, providerLabel, confirm, toast, empty, spinner, errorState, drawLabel, search, vmIdentity, vmSearchText, DRAW_MODES, WINDOW_WORDS, stagePool, stageLine } from "../lib/components.js";
import { luaEditor, luaFunctions, LUA_EXAMPLE } from "../lib/lua-editor.js";

export async function render(ctx) {
	if (ctx.params[0]) return editor(ctx, ctx.params[0]);
	return catalogue(ctx);
}

// ---------- catalogue ----------

async function catalogue(ctx) {
	const { root } = ctx;
	let query = "", limit = 25;
	const summary = h("span", { class: "ink-3", role: "status", "aria-live": "polite" });
	root.append(pageHead({
		title: "Virtual models",
		desc: h("span", null, "Names users ask for, like ", h("code", null, "neoproxy/default"), ". A request goes to the first stage with room. Direct provider models follow the ", h("a", { class: "link-text", href: "#/routing" }, "routing strategy"), " instead."),
		actions: [
			search("Search by provider, model or name", (value) => { query = value.trim().toLowerCase(); limit = 25; paint(); }),
			btn("New virtual model", { icon: "plus", cls: "primary", onClick: () => ctx.navigate("virtual-models/new") }),
		],
	}));
	const host = h("div");
	root.append(summary, host, spinner());
	let vms, models, vault, users;
	try {
		[vms, models, vault, users] = await Promise.all([neo.virtualModels(), neo.models(), neo.vault(), neo.users()]);
	} catch (error) {
		root.lastChild.replaceWith(errorState(error, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();
	function paint() {
		if (!vms) return;
		clear(host);
		const matches = vms.filter((vm) => !query || vmSearchText(vm, models, vault.providers).includes(query));
		summary.textContent = `Showing ${Math.min(limit, matches.length)} of ${matches.length} matching virtual models · ${vms.length} total`;
		if (!matches.length) {
			host.append(card({ body: empty({
				icon: "route",
				title: vms.length ? "No virtual models match" : "No virtual models yet",
				hint: vms.length ? "Search by the upstream model, provider, source or served name." : "Create a name like neoproxy/default and choose its stages.",
			}) }));
			return;
		}
		for (const vm of matches.slice(0, limit)) {
			const granted = users.filter((user) => user.grants.some((grant) => grant.virtualModel === vm.id));
			const author = users.find((user) => user.id === vm.updatedBy || user.handle === vm.updatedBy)?.handle ?? "an administrator";
			const node = card({
				title: h("span", { class: "row" }, servedName(vm.served, { virtual: true })),
				sub: vm.description || vmIdentity(vm, models, vault.providers),
				actions: [
					h("span", { class: "row", style: { gap: "2px", marginRight: "6px" } }, granted.slice(0, 5).map((user) => avatar(user, "sm")), granted.length > 5 ? h("span", { class: "ink-3", style: { fontSize: "12px", marginLeft: "4px" } }, `+${granted.length - 5}`) : null),
					btn("Edit", { icon: "arrowRight", cls: "outline sm", onClick: () => ctx.navigate(`virtual-models/${vm.id}`) }),
				],
				body: chainRail(vm, { models, providers: vault.providers, accounts: vault.accounts }),
				foot: [
					h("span", null, `Granted to ${granted.length ? granted.map((user) => user.handle).join(", ") : "nobody yet"}`),
					h("span", { class: "spacer" }),
					h("span", { class: "ink-3", title: vm.updatedBy }, `Updated ${fmtAgo(vm.updatedAt)} by ${author}`),
				],
			});
			node.dataset.vm = vm.id;
			host.append(node);
		}
		if (matches.length > limit) host.append(btn("Show 25 more", {
			cls: "outline sm",
			onClick: () => {
				const next = matches[limit].id;
				limit += 25;
				paint();
				host.querySelector(`[data-vm="${CSS.escape(next)}"] .btn`)?.focus();
			},
		}));
	}
	paint();
	const off = onChange((what) => what === "virtual-models" && ctx.rerender());
	return () => off();
}

// ---------- editor ----------

const MAX_STAGES = 128;
const newStageId = () => `s${Math.random().toString(36).slice(2, 7)}`;
const bold = (text) => h("b", null, text);
const pct = (v) => Number((v * 100).toPrecision(15));
const WINDOWS = ["5h", "day", "week", "month"];
const LUA_SLOTS = [
	{ key: "eligible", label: "Account condition", builtin: "Built-in: every account with room", hint: "Returns true or false. false skips that account for this request." },
	{ key: "perAccount", label: "Per-account threshold", builtin: "Built-in: the slider above", hint: "Returns a number from 0 to 1. It can only lower the threshold above, never raise it." },
	{ key: "cap", label: "Pool threshold", builtin: "Built-in: the slider above", hint: "Returns a number from 0 to 1, asked once per stage. It can only lower the pool threshold above." },
];

async function editor(ctx, id) {
	const { root } = ctx;
	const isNew = id === "new";
	const isAdmin = ctx.session.role === "admin";
	root.append(spinner());
	let vm, models, vault, users;
	try {
		[models, vault, users] = await Promise.all([neo.models(), neo.vault(), neo.users()]);
		vm = isNew
			? { id: null, name: "", served: "neoproxy/", description: "", stages: [], terminal: { kind: "limit", message: "" } }
			: await neo.virtualModel(id);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.navigate("virtual-models")));
		return;
	}
	root.lastChild.remove();

	const draft = structuredClone(vm);
	let dirty = isNew;
	let selected = matchMedia("(max-width: 900px)").matches ? null : draft.stages[0]?.id ?? null;
	let inspectorOpen = selected !== null;
	let changeModelFor = null; // stage id whose model picker opens expanded (a stage just added)
	let simUser = users.find((u) => u.role === "member" && u.state === "active")?.id ?? users[0]?.id;
	let simResult = null;
	const errors = {};
	let scriptChanged = () => {}; // set by the open stage inspector to refresh its Lua pickers
	let luaTimer = 0; // debounces re-reading the script's function names

	// ----- head -----
	const saveBtn = btn("Save", { icon: "save", cls: "primary", onClick: save });
	const head = pageHead({
		back: { href: "#/virtual-models", label: "Virtual models" },
		title: h("span", { class: "row", id: "vm-title" }, isNew ? "New virtual model" : servedName(draft.served, { virtual: true })),
		desc: isNew ? "Name it, add stages and save. Then try it against a user's actual grants." : null,
		actions: [
			isNew ? null : btn("Delete", { icon: "trash", cls: "danger", onClick: remove }),
			btn("Discard", { cls: "outline", onClick: () => (dirty ? discard() : ctx.navigate("virtual-models")) }),
			saveBtn,
		],
	});
	root.append(head);
	let resetSimulation = () => {};
	const setDirty = (v = true) => {
		dirty = v;
		saveBtn.textContent = "";
		saveBtn.append(icon("save", 15), dirty ? "Save changes" : "Saved");
		saveBtn.disabled = !dirty;
		if (v) resetSimulation();
	};

	// ----- identity -----
	const nameInput = input({
		mono: true,
		value: draft.name,
		placeholder: "default, small, heavy, review…",
		style: { paddingLeft: "92px" },
		onInput: (e) => {
			draft.name = e.target.value.trim().toLowerCase();
			draft.served = `neoproxy/${draft.name}`;
			errors.name = /^[a-z][a-z0-9-]{0,31}$/.test(draft.name) ? null : "lowercase letters, digits and dashes";
			if (models.some((m) => m.id === draft.name)) errors.name = "collides with a served model";
			renderIdentityErrors();
			setDirty();
		},
	});
	const nameWrap = h("div", { style: { position: "relative" } }, h("span", { class: "served", style: { position: "absolute", left: "10px", top: "7px", pointerEvents: "none" }, "aria-hidden": "true" }, h("span", { class: "pre" }, "neoproxy/")), nameInput);
	const nameHint = "What users type as the model id. Keep it a role, not a brand: default, small, heavy.";
	const nameField = field("Name", nameWrap, nameHint);
	const renderIdentityErrors = () => {
		const hint = nameField.querySelector(".hint, .error");
		hint.className = errors.name ? "error" : "hint";
		hint.textContent = errors.name ?? nameHint;
	};
	root.append(
		card({
			title: "Identity",
			body: h(
				"div",
				{ class: "grid c2", style: { paddingTop: "4px" } },
				nameField,
				field("Description", h("textarea", { class: "textarea", style: { minHeight: "60px" }, placeholder: "One sentence a user would understand.", onInput: (e) => { draft.description = e.target.value; setDirty(); } }, draft.description), "Shown to users next to the name."),
			),
		}),
	);

	// ----- chain -----
	const railHost = h("div");
	const reorderStatus = h("div", { class: "sr-only", role: "status" });
	root.append(
		card({
			title: "Chain",
			sub: "A request tries stage 1 first and moves on when a stage is spent. When every stage is spent, the chain ends the way you choose. Click a stage to edit it; drag it, or press Alt+← and Alt+→, to reorder.",
			actions: [btn("Add stage", { icon: "plus", cls: "outline sm", onClick: addStage })],
			body: [railHost, reorderStatus],
		}),
	);
	function renderRail(focusStage) {
		const focused = railHost.contains(document.activeElement) ? document.activeElement : null;
		focusStage ??= focused?.closest("[data-stage]")?.dataset.stage;
		const scroll = railHost.querySelector(".stages")?.scrollLeft ?? 0;
		clear(railHost);
		railHost.append(
			chainRail(draft, {
				models,
				providers: vault.providers,
				accounts: vault.accounts,
				lit: simResult,
				selected: inspectorOpen ? selected : null,
				editor: true,
				onSelect: (s) => {
					const target = s === "terminal" ? "terminal" : s.id;
					if (inspectorOpen && selected === target) {
						closeInspector();
						return;
					}
					selected = target;
					inspectorOpen = true;
					renderRail(selected);
					renderInspector();
				},
				onAdd: addStage,
				onReorder: (next, info) => {
					if (!next) {
						reorderStatus.textContent = `Already the ${info.edge} stage`;
						return;
					}
					draft.stages = next;
					setDirty();
					renderRail(info.id);
					if (selected === info.id) renderInspector();
					const stage = next[info.to];
					reorderStatus.textContent = `Moved ${models.find((m) => m.id === stage.model)?.display ?? stage.model} to stage ${info.to + 1} of ${next.length}`;
				},
			}),
		);
		const scroller = railHost.querySelector(".stages");
		scroller.scrollLeft = scroll;
		if (focusStage) {
			const node = railHost.querySelector(`[data-stage="${CSS.escape(focusStage)}"]`);
			node?.focus({ preventScroll: true });
			node?.scrollIntoView({ inline: "nearest", block: "nearest" });
		}
		const t = document.getElementById("vm-title");
		if (t && !isNew) {
			clear(t);
			t.append(servedName(draft.served, { virtual: true }));
		}
	}
	function closeInspector() {
		inspectorOpen = false;
		scriptChanged = () => {};
		renderRail();
		ctx.setInspector(null);
	}

	// A new stage starts as a copy of the last one (accounts, draw mode, window,
	// thresholds, effort, Lua) on the next model nobody in the chain uses yet, so a
	// fallback never means re-entering numbers.
	function nextModel(prev) {
		const exposed = new Set(vault.providers.filter((p) => p.exposed).map((p) => p.id));
		const usable = models.filter((m) => m.exposed && exposed.has(m.providerId));
		const inChain = new Set(draft.stages.map((s) => s.model));
		const live = new Set(vault.accounts.filter((a) => a.state === "healthy").map((a) => a.providerId));
		// Prefer the closest sibling of the last model that sorts below it: after
		// claude-opus-4-8 comes claude-opus-4-7, not an unrelated model.
		const shared = (a, b) => { let n = 0; while (n < a.length && a[n] === b[n]) n++; return n; };
		const sameProvider = prev ? usable.filter((m) => m.providerId === prev.model.split("/")[0]).sort((a, b) => shared(b.id, prev.model) - shared(a.id, prev.model) || (b.id < prev.model) - (a.id < prev.model) || (a.id < b.id ? 1 : -1)) : [];
		return (sameProvider.find((m) => !inChain.has(m.id))
			?? usable.find((m) => live.has(m.providerId) && !inChain.has(m.id))
			?? sameProvider[0]
			?? usable[0])?.id;
	}
	function addStage() {
		if (draft.stages.length >= MAX_STAGES) { toast(`A chain holds at most ${MAX_STAGES} stages.`, "warn"); return; }
		const prev = draft.stages.at(-1);
		const model = nextModel(prev);
		if (!model) { toast("No exposed provider models are available. Add an account and expose its catalogue first.", "warn"); return; }
		const stage = prev
			? structuredClone(prev)
			: { accounts: "all", draw: "least-used", perAccount: 0.5, cap: 0.7, window: "5h", per: "all", effort: null };
		stage.id = newStageId();
		stage.model = model;
		if (prev && model.split("/")[0] !== prev.model.split("/")[0]) {
			stage.accounts = "all";
			delete stage.lua;
		}
		draft.stages.push(stage);
		selected = stage.id;
		inspectorOpen = true;
		changeModelFor = stage.id;
		setDirty();
		renderRail(selected);
		renderInspector();
	}

	// ----- Lua policy (admin) -----
	const scriptHint = "Up to 16 KiB. Saving checks the syntax. There are no files, network, modules or credentials, and errors fail closed: nothing is routed around a broken script.";
	let luaOpen = false;
	let luaApi = null;
	let luaCard = null;
	let scriptError = null;
	const luaHost = h("div", { class: "vm-lua" });
	const luaStatus = h("div", { class: "hint", style: { marginTop: "8px" } });
	const fnList = h("div", { class: "vm-fns" });
	let knownFns = "";
	const refreshFns = () => {
		const names = luaFunctions(draft.script ?? "");
		const key = names.join(",");
		fnList.replaceChildren(...(names.length
			? ["Functions a stage can use: ", ...names.map((n) => h("code", null, n))]
			: ["No functions yet. Define them as ", h("code", null, "function name(ctx)"), " and pick them in a stage."]));
		if (key !== knownFns) {
			knownFns = key;
			scriptChanged();
		}
	};
	const isScriptError = (e) => e instanceof ApiError && (["neoproxy_script", "neoproxy_script_error"].includes(e.body?.error?.code) || /\b(?:script|Lua)\b/i.test(e.message));
	function scriptMessage(message) {
		let readable = String(message);
		draft.stages.forEach((stage, index) => {
			const name = models.find((model) => model.id === stage.model)?.display ?? "Unavailable model";
			readable = readable.replaceAll(`stage "${stage.id}"`, `Stage ${index + 1} (${name})`);
		});
		return readable;
	}
	function setScriptError(message) {
		scriptError = message;
		if (!luaApi) return;
		luaApi.area.setAttribute("aria-invalid", String(Boolean(message)));
		luaStatus.className = message ? "error" : "hint";
		luaStatus.textContent = message ? scriptMessage(message) : scriptHint;
		luaStatus.title = message || "";
	}
	function openLua({ focus = true } = {}) {
		if (!isAdmin) return;
		luaOpen = true;
		paintLua();
		if (matchMedia("(max-width: 900px)").matches && inspectorOpen) closeInspector();
		if (focus) {
			luaApi.area.focus({ preventScroll: true });
			luaApi.el.scrollIntoView({ block: "center", behavior: "smooth" });
		}
	}
	const luaToggle = btn("Open editor", { cls: "outline sm", onClick: () => { luaOpen = !luaOpen; paintLua(); if (luaOpen) luaApi.area.focus({ preventScroll: true }); } });
	function paintLua() {
		luaHost.classList.toggle("hidden", !luaOpen);
		luaToggle.textContent = luaOpen ? "Hide editor" : draft.script?.trim() ? "Open editor" : "Write a script";
		luaToggle.setAttribute("aria-expanded", String(luaOpen));
	}
	if (isAdmin) {
		luaApi = luaEditor({
			value: draft.script ?? "",
			label: "Lua pool policy",
			describedBy: "vm-lua-hint",
			placeholder: "-- Optional. Leave empty to use the sliders in each stage.",
			onInput: (value) => {
				draft.script = value;
				if (scriptError) setScriptError(null);
				setDirty();
				clearTimeout(luaTimer);
				luaTimer = setTimeout(refreshFns, 250);
			},
		});
		let luaTimer = 0;
		luaStatus.id = "vm-lua-hint";
		luaStatus.textContent = scriptHint;
		luaHost.append(
			luaApi.el,
			luaStatus,
			h("p", { class: "hint", style: { margin: "4px 0 0" } }, "Tab inserts two spaces. Press Esc to leave the editor."),
			h("details", { class: "vm-contract" },
				h("summary", null, "What each function receives and returns"),
				h("div", { class: "vm-contract-body" },
					h("p", null, h("code", null, "function eligible(ctx)"), " returns ", h("code", null, "true"), " or ", h("code", null, "false"), ". ", h("code", null, "function per_account(ctx)"), " and ", h("code", null, "function cap(ctx)"), " return a fraction from 0 to 1: ", h("code", null, "0.25"), " means 25%, not 25 points. You choose the names; each stage picks the ones it uses."),
					h("p", null, "Lua can only make a stage stricter. The stage's own thresholds, each person's allotment, account health and token reservations still apply. ", h("code", null, "ctx.account"), ", ", h("code", null, "ctx.window"), ", ", h("code", null, "ctx.used"), " and ", h("code", null, "ctx.budget"), " exist for the two account functions only; ", h("code", null, "cap"), " is asked once per stage. Everything on ", h("code", null, "ctx"), " is read-only."),
					h("p", null, "Provider meters are in ", h("code", null, "ctx.account.providerWindows"), " and local token meters in ", h("code", null, "ctx.account.windows"), ". Check ", h("code", null, "known"), " before using ", h("code", null, "used"), "; kinds can be namespaced, like ", h("code", null, "zai:tokens:5h"), "."),
				),
			),
			(() => {
				const kinds = [...new Set(vault.accounts.flatMap((account) => (account.providerWindows ?? []).map((window) => window.kind)))].sort();
				return kinds.length ? h("p", { class: "hint" }, `Meter kinds your accounts report right now: ${kinds.join(", ")}`) : null;
			})(),
		);
		luaCard = card({
			title: "Lua policy · optional",
			sub: "The sliders in each stage cover most chains. Write Lua when a stage needs a rule they can't express, like skipping an account while its provider reports a hot 5h window.",
			actions: [
				btn("Start from an example", { cls: "outline sm", onClick: startFromExample }),
				luaToggle,
			],
			body: [fnList, luaHost],
		});
		luaOpen = Boolean(draft.script?.trim()) || draft.stages.some((s) => s.lua);
		paintLua();
		refreshFns();
	}
	async function startFromExample() {
		if (luaApi.area.value.trim() && !(await confirm({ title: "Replace your script?", text: "The example takes the place of the Lua source in the editor. Nothing is saved until you save the model.", confirmLabel: "Replace with the example", danger: true }))) return;
		luaApi.setValue(LUA_EXAMPLE);
		luaApi.area.scrollTop = 0;
		openLua({ focus: false });
		luaApi.el.scrollIntoView({ block: "center", behavior: "smooth" });
		toast("Example added. Pick its functions in a stage under Lua functions.");
	}

	// ----- simulator -----
	const simHost = h("div");
	let simProtocol = "openai", simPayload = "";
	const simProtocolSel = select([
		{ value: "openai", label: "OpenAI Chat Completions" },
		{ value: "openai-response", label: "OpenAI Responses" },
		{ value: "claude", label: "Anthropic Messages" },
	], simProtocol, (value) => { simProtocol = value; runSim(); }, { "aria-label": "Simulation protocol" });
	const payloadInput = h("textarea", {
		class: "textarea mono", rows: 6, spellcheck: "false", "aria-label": "Simulation request JSON",
		placeholder: 'Optional client JSON, e.g. {"messages":[{"role":"user","content":"Example request"}]}',
		onInput: (e) => {
			simPayload = e.target.value;
			++simGeneration;
			simResult = null;
			simHost.replaceChildren(h("div", { class: "ink-3" }, "Request changed. Run simulation to evaluate this payload."));
			renderRail();
		},
	});
	const simUserSel = select(
		users.map((u) => ({ value: u.id, label: `${u.handle} · ${u.role}` })),
		simUser,
		(v) => {
			simUser = v;
			runSim();
		},
		{ "aria-label": "Simulate as user" },
	);
	root.append(
		card({
			title: "Try it",
			sub: "Where would a request from this person land right now? The lit stage on the chain is the answer, and the sentence below says why. It uses the saved model and today's meters; nothing is sent upstream or recorded.",
			actions: [h("div", { style: { width: "220px" } }, simUserSel), btn("Run simulation", { icon: "refresh", cls: "outline sm", onClick: runSim })],
			body: [
				h("details", { style: { marginBottom: "14px" } },
					h("summary", null, "Protocol & request payload"),
					field("Protocol", simProtocolSel),
					field("Client request JSON · optional", payloadInput, "Read-only script input.request. Use messages for Chat/Anthropic or input for Responses. The saved model and actual grants remain authoritative. Script request bodies are capped at 256 KiB; the combined request/usage/health snapshot has its own 256 KiB cap and can reject smaller bodies."),
				),
				simHost,
			],
		}),
	);
	let simGeneration = 0;
	resetSimulation = () => {
		++simGeneration;
		simResult = null;
		simHost.replaceChildren(h("div", { class: "ink-3" }, "Save changes before running a real simulation."));
		renderRail();
	};
	// One sentence for why the request lands where it does (or why it ends).
	function explain(result) {
		const steps = (result.trail ?? []).filter((t) => t.kind === "stage");
		const label = (t) => `Stage ${t.index + 1} (${models.find((m) => m.id === t.model)?.display ?? t.model})`;
		const accountName = (accountId) => vault.accounts.find((a) => a.id === accountId)?.label ?? "an account in the pool";
		const skipped = steps.filter((t) => t.skipped).map((t) => {
			if (t.skipped === "cap") return `${label(t)} is skipped: its pool averages ${fmtPct(t.total, null)} and the stage stops at ${fmtPct(t.cap, null)}`;
			if (t.skipped === "per-account") return `${label(t)} is skipped: every account in it is at its threshold`;
			if (t.skipped === "unhealthy") return `${label(t)} is skipped: no healthy account in its pool can serve`;
			if (t.skipped === "upstream-error") return `${label(t)} failed upstream, so the chain moved on`;
			return `${label(t)} is skipped by the Lua policy`;
		});
		const landed = steps.find((t) => t.chosen && !t.skipped);
		let ending;
		if (landed) {
			const c = landed.candidates?.find((x) => x.accountId === landed.chosen);
			const meter = !c ? "" : c.metered ? `, which has used ${fmtPct(c.used, null)} of its ${fmtPct(c.budget, null)} threshold` : ", which has no meter and is always available";
			ending = `It lands on ${label(landed).replace(/^Stage/, "stage")} using ${accountName(landed.chosen)}${meter}`;
		} else {
			const end = (result.trail ?? []).find((t) => t.kind === "terminal");
			ending = end ? `Every stage is spent, so the chain ends: ${end.terminal.kind === "queue" ? "the request waits for a window to reset" : end.terminal.kind === "passthrough" ? "it falls through to a direct model" : "the caller gets a limit error"}` : result.message ?? "The request does not reach a stage";
		}
		const who = users.find((u) => u.id === simUser);
		const allot = who?.grants.find((g) => g.virtualModel === draft.id)?.allotment;
		const prefix = who && allot !== undefined && allot !== 1 ? `${who.handle} may use up to ${fmtPct(allot, null)} of every threshold. ` : "";
		return `${prefix}${[...skipped, ending].join(". ")}.`;
	}
	async function runSim() {
		const generation = ++simGeneration;
		simResult = null;
		clear(simHost);
		if (dirty || isNew || !simUser) {
			simHost.append(h("div", { class: "ink-3" }, !simUser ? "No users available." : "Save changes before running a real simulation."));
			renderRail();
			return;
		}
		simHost.append(spinner());
		try {
			let payload;
			if (simPayload.trim()) {
				try { payload = JSON.parse(simPayload); }
				catch (e) { throw new Error(`Invalid simulation request JSON: ${e.message}`); }
				if (!payload || Array.isArray(payload) || typeof payload !== "object") throw new Error("Simulation request JSON must be an object.");
			}
			const result = await neo.simulate({ asked: vm.served, userId: simUser, protocol: simProtocol, request: payload });
			if (generation !== simGeneration || !simHost.isConnected) return;
			simResult = result;
			simHost.replaceChildren(h("p", { class: "vm-why" }, explain(result)), trail(result, { models, accounts: vault.accounts, users, admin: isAdmin }));
		} catch (e) {
			if (generation !== simGeneration || !simHost.isConnected) return;
			const failure = isScriptError(e)
				? empty({ icon: "info", title: "Routing script result rejected", hint: scriptMessage(e.message), action: isAdmin ? btn("Edit script", { cls: "outline", onClick: () => openLua() }) : null })
				: errorState(e, runSim);
			failure.title = e.message;
			simHost.replaceChildren(failure);
		}
		renderRail();
	}

	// ----- grants -----
	const grantHost = h("div", { class: "list" });
	const allotmentLine = (a) => (a >= 1 ? "may use up to 100% of every threshold" : `up to ${fmtPct(a, null)} of every threshold`);
	function renderGrants() {
		clear(grantHost);
		if (isNew) {
			grantHost.append(h("div", { class: "ink-3", style: { padding: "12px 16px" } }, "Save first, then grant it to users from their page."));
			return;
		}
		const granted = users.filter((u) => u.grants.some((g) => g.virtualModel === draft.id));
		if (!granted.length) grantHost.append(h("div", { class: "ink-3", style: { padding: "12px 16px" } }, "Nobody yet. Grant it from a user's page."));
		for (const u of granted) {
			const g = u.grants.find((x) => x.virtualModel === draft.id);
			grantHost.append(h("button", { type: "button", class: "li clickable", onClick: () => ctx.navigate(`users/${u.id}`) }, avatar(u), h("div", { class: "lt" }, h("div", { class: "main" }, u.handle), h("div", { class: "sub" }, allotmentLine(g.allotment))), icon("chevronRight", 14)));
		}
	}
	root.append(card({ title: "Granted to", sub: "A person's allotment multiplies every threshold in this chain. At 30%, a stage that stops at 50% stops at 15% for them.", flush: true, body: grantHost }));
	renderGrants();
	if (luaCard) root.append(luaCard);

	// ----- inspector -----
	function renderInspector() {
		if (!inspectorOpen || !selected) return ctx.setInspector(null);
		const stageId = selected;
		const stage = draft.stages.find((item) => item.id === stageId);
		if (stageId !== "terminal" && !stage) return ctx.setInspector(null);
		ctx.setInspector(stageId === "terminal" ? terminalInspector() : stageInspector(stage), {
			restoreFocus: () => railHost.querySelector(`[data-stage="${CSS.escape(stageId)}"]`)?.focus({ preventScroll: true }),
		});
	}
	function stageInspector(s) {
		const stageNumber = () => draft.stages.indexOf(s) + 1;
		const providerId = () => s.model.split("/")[0];
		const model = () => models.find((m) => m.id === s.model);
		const provider = () => vault.providers.find((p) => p.id === providerId());
		const pool = () => vault.accounts.filter((a) => a.providerId === providerId());
		const meteredCount = () => stagePool(s, vault.accounts).filter((a) => meterFor(a, s.window)).length;
		let pickerOpen = changeModelFor === s.id;
		changeModelFor = null;
		let showAllAccounts = false;

		const aside = inspector({
			title: "Stage",
			sub: "Stage",
			onClose: closeInspector,
			body: [],
			foot: [],
		});
		const titleEl = aside.querySelector("h2");
		const subEl = aside.querySelector(".ins-head .sub");
		const refreshHead = () => {
			titleEl.textContent = `Stage ${stageNumber()} · ${model()?.display ?? s.model}`;
			subEl.textContent = `${drawLabel(s.draw)} · ${stageLine(s, 1, meteredCount() > 0)}`;
			aside.setAttribute("aria-label", titleEl.textContent);
		};

		// Everything below the head repaints from the stage; controls that are mid-drag stay put.
		const update = () => {
			setDirty();
			refreshHead();
			renderAccounts();
			renderSentences();
			renderEffortNote();
		};

		// --- model ---
		const currentModel = h("div", { class: "vm-current" });
		const pickerHost = h("div");
		const renderCurrentModel = () => {
			const p = provider();
			const label = providerLabel(p);
			clear(currentModel);
			currentModel.append(
				providerDot(p, 10),
				h("div", { class: "lt" }, h("div", { class: "main" }, model()?.display ?? "Unavailable model"), h("div", { class: "sub" }, `${label.main}${label.source ? ` · ${label.source}` : ""}`), h("div", { class: "id" }, servedName(`neoproxy/${s.model}`))),
				btn(pickerOpen ? "Done" : "Change model", { cls: "outline sm", onClick: () => { pickerOpen = !pickerOpen; renderCurrentModel(); renderPicker(); } }),
			);
		};
		const renderPicker = () => {
			clear(pickerHost);
			if (!pickerOpen) return;
			pickerHost.append(
				modelPicker({
					providers: vault.providers,
					models,
					accounts: vault.accounts,
					value: s.model,
					onPick: (m) => {
						if (m.providerId !== providerId()) {
							s.accounts = "all";
							delete s.lua;
							renderLuaPickers();
						}
						s.model = m.id;
						renderCurrentModel();
						update();
					},
				}),
				h("div", { class: "hint", style: { marginTop: "6px", fontSize: "12px", color: "var(--ink-3)" } }, "Only providers the core holds and you serve. Hidden providers and models do not appear."),
			);
		};

		// --- accounts ---
		const accountsSection = h("div");
		const renderAccounts = () => {
			clear(accountsSection);
			const all = pool();
			const picked = stagePool(s, vault.accounts);
			accountsSection.append(
				seg(
					[
						{ value: "all", label: `All ${all.length}` },
						{ value: "pick", label: "Pick" },
					],
					s.accounts === "all" ? "all" : "pick",
					(v) => {
						s.accounts = v === "all" ? "all" : all.map((a) => a.id);
						update();
					},
				),
			);
			if (!all.length) accountsSection.append(h("p", { class: "hint" }, "No accounts hold this provider yet. Add one under Credentials, then pick it here."));
			const shown = showAllAccounts ? all : all.slice(0, 8);
			for (const a of shown) {
				const on = picked.includes(a);
				const m = meterFor(a, s.window);
				const health = accountHealth(a);
				accountsSection.append(
					h(
						"label",
						{ class: "vm-acct" },
						h("input", {
							type: "checkbox",
							checked: on,
							"aria-label": `Use ${a.label}`,
							onChange: (e) => {
								const set = new Set(s.accounts === "all" ? all.map((x) => x.id) : s.accounts);
								if (e.target.checked) set.add(a.id);
								else set.delete(a.id);
								s.accounts = [...set];
								update();
							},
						}),
						meterRing(a, { size: 26, kind: s.window, budget: s.perAccount }),
						h(
							"div",
							{ class: "lt" },
							h("div", { class: "main" }, h("code", null, a.label)),
							h("div", { class: "sub" }, [a.plan, health.text, m ? `${fmtPct(m.used)} of ${WINDOW_WORDS[s.window]} used` : null].filter(Boolean).join(" · ")),
							m ? null : h("div", { class: "sub nometer" }, "No meter — always available; thresholds do not apply"),
						),
						m ? h("span", { class: "until", title: `Drawn from while its ${s.window} usage is below ${fmtPct(s.perAccount, null)}` }, `until ${fmtPct(s.perAccount, null)}`) : null,
					),
				);
			}
			if (all.length > shown.length) accountsSection.append(btn(`Show all ${all.length}`, { cls: "outline sm", onClick: () => { showAllAccounts = true; renderAccounts(); } }));
			if (s.accounts !== "all" && !s.accounts.length) accountsSection.append(h("div", { class: "error", style: { marginTop: "6px" } }, "Pick at least one account, or this stage can never serve."));
		};

		// --- draw ---
		const drawHint = h("p", { class: "hint", style: { margin: "6px 0 0" } });
		const renderDrawHint = () => { drawHint.textContent = DRAW_MODES.find((mode) => mode.value === s.draw)?.hint ?? ""; };

		// --- thresholds ---
		const sentenceEach = h("p", { class: "vm-sentence" });
		const sentencePool = h("p", { class: "vm-sentence" });
		const meterNote = h("div", { class: "vm-note" });
		function renderSentences() {
			sentenceEach.replaceChildren("Draw from each account while its ", WINDOW_WORDS[s.window], " usage is below ", bold(fmtPct(s.perAccount, null)), ".");
			sentencePool.replaceChildren("Skip this stage once the pool averages ", bold(fmtPct(s.cap, null)), ".");
			const picked = stagePool(s, vault.accounts);
			const unmetered = picked.filter((a) => !meterFor(a, s.window)).length;
			meterNote.classList.toggle("hidden", !picked.length || unmetered === 0);
			meterNote.textContent = unmetered === picked.length
				? `No meter on ${picked.length === 1 ? "this account" : "these accounts"} for ${s.window} — always available; thresholds do not apply. Pick another window to meter them.`
				: `${plural(unmetered, "account")} without a ${s.window} meter ${unmetered === 1 ? "is" : "are"} always available; thresholds do not apply to ${unmetered === 1 ? "it" : "them"}.`;
		}
		const threshold = (label, key, hint) => {
			const num = input({ type: "number", min: 0, max: 100, step: 0.5, value: pct(s[key]), class: "input vm-num", "aria-label": `${label} (percent)` });
			const range = h("input", { type: "range", class: "vm-range", min: 0, max: 100, step: 0.5, value: pct(s[key]), "aria-label": label });
			const paint = () => range.style.setProperty("--fill", `${range.value}%`);
			paint();
			range.addEventListener("input", () => {
				s[key] = Number(range.value) / 100;
				num.value = range.value;
				paint();
				update();
			});
			num.addEventListener("input", () => {
				const v = num.valueAsNumber;
				if (!Number.isFinite(v)) return;
				s[key] = Math.min(100, Math.max(0, v)) / 100;
				range.value = String(pct(s[key]));
				paint();
				update();
			});
			num.addEventListener("blur", () => { num.value = String(pct(s[key])); });
			return h("div", { class: "vm-threshold" }, h("div", { class: "row" }, h("label", { for: num.id || (num.id = `vm-t-${key}-${s.id}`) }, label), h("span", { class: "spacer" }), h("span", { class: "vm-numwrap" }, num, "%")), range, hint);
		};

		// --- Lua functions ---
		const luaSection = h("div");
		function renderLuaPickers() {
			clear(luaSection);
			if (!isAdmin) return;
			const names = luaFunctions(draft.script ?? "");
			luaSection.append(h("p", { class: "hint", style: { marginTop: 0 } }, names.length ? "Optional. Pick functions from your Lua policy to make this stage stricter than its sliders." : "Optional. Write functions in the Lua policy card, then pick them here."));
			for (const slot of LUA_SLOTS) {
				const current = s.lua?.[slot.key] ?? "";
				const options = [{ value: "", label: slot.builtin }, ...names.map((n) => ({ value: n, label: n }))];
				if (current && !names.includes(current)) options.push({ value: current, label: `${current} (not defined in the script)` });
				luaSection.append(field(slot.label, select(options, current, (value) => {
					if (value) (s.lua ??= {})[slot.key] = value;
					else {
						delete s.lua?.[slot.key];
						if (s.lua && !Object.keys(s.lua).length) delete s.lua;
					}
					setDirty();
				}), slot.hint));
			}
			luaSection.append(btn(names.length ? "Edit Lua policy" : "Write a Lua policy", { cls: "outline sm", onClick: () => openLua() }));
		}
		scriptChanged = () => { if (aside.isConnected) renderLuaPickers(); };

		const effortNote = h("p", { class: "hint", style: { margin: "6px 0 0" } });
		const renderEffortNote = () => { effortNote.textContent = s.effort ? `Requests served by this stage always run at ${s.effort} effort, whatever the caller asked for.` : "The caller's own effort setting is passed through."; };

		renderCurrentModel();
		renderPicker();
		renderAccounts();
		renderDrawHint();
		renderSentences();
		renderLuaPickers();
		renderEffortNote();
		refreshHead();
		aside.querySelector(".ins-body").append(
			section("Model", currentModel, pickerHost),
			section("Accounts in the pool", accountsSection),
			section(
				"How to draw",
				seg(DRAW_MODES.map((mode) => ({ value: mode.value, label: mode.label, title: mode.hint })), s.draw, (v) => { s.draw = v; renderDrawHint(); update(); }),
				drawHint,
			),
			section(
				"Thresholds",
				meterNote,
				threshold("Each account", "perAccount", sentenceEach),
				threshold("The whole pool", "cap", sentencePool),
				field(
					"Usage window",
					seg(
						WINDOWS.map((w) => ({ value: w, label: w })),
						s.window,
						(v) => { s.window = v; update(); },
					),
					"Thresholds read the accounts' usage over this window.",
				),
				h("details", { class: "vm-advanced" },
					h("summary", null, "Advanced"),
					field(
						"Whose usage counts",
						seg(
							[
								{ value: "all", label: "Everyone's", title: "One usage number per account, shared by everyone granted" },
								{ value: "user", label: "Only this person's", title: "Each person is measured against their own usage" },
							],
							s.per,
							(v) => { s.per = v; update(); },
						),
						"Meters reported by the provider, like Claude and Codex subscriptions, always count everyone's usage. This choice only affects local token meters.",
					),
				),
			),
			isAdmin ? section("Lua functions", luaSection) : null,
			section(
				"Effort",
				select(
					[
						{ value: "", label: "Caller's choice" },
						{ value: "low", label: "Force low" },
						{ value: "medium", label: "Force medium" },
						{ value: "high", label: "Force high" },
						{ value: "max", label: "Force max" },
					],
					s.effort ?? "",
					(v) => { s.effort = v || null; update(); },
					{ "aria-label": "Effort" },
				),
				effortNote,
			),
		);
		aside.querySelector(".ins-foot").append(
			btn("Remove stage", {
				icon: "trash",
				cls: "danger",
				onClick: () => {
					draft.stages = draft.stages.filter((x) => x !== s);
					selected = draft.stages[0]?.id ?? null;
					inspectorOpen = selected !== null;
					setDirty();
					renderInspector();
				},
			}),
		);
		return aside;
	}
	function terminalInspector() {
		const t = draft.terminal;
		const msgField = field("Message to the caller", h("textarea", { class: "textarea", style: { minHeight: "80px" }, placeholder: "Your heavy allotment for this week is used up.", onInput: (e) => { t.message = e.target.value; setDirty(); } }, t.message ?? ""), "Returned in the error body so the user knows why, and when to try again.");
		const passField = h("div", { class: t.kind === "passthrough" ? "" : "hidden" });
		const renderPass = () => {
			clear(passField);
			passField.append(
				field(
					"Fall through to",
					modelPicker({
						providers: vault.providers,
						models: models.filter((m) => !m.virtualOnly),
						accounts: vault.accounts,
						value: t.model ?? null,
						onPick: (m) => {
							t.model = m.id;
							setDirty();
						},
					}),
					"Served without a budget or a separate direct-access grant. Source-backed models require a metered stage and are not offered here.",
				),
			);
		};
		renderPass();
		return inspector({
			title: "When every stage is spent",
			sub: "The chain's end",
			onClose: closeInspector,
			body: [
				section(
					"Then",
					seg(
						[
							{ value: "limit", label: "Limit error" },
							{ value: "queue", label: "Queue" },
							{ value: "passthrough", label: "Fall through" },
						],
						t.kind,
						(v) => {
							t.kind = v;
							passField.classList.toggle("hidden", v !== "passthrough");
							setDirty();
						},
					),
					h("p", { class: "ink-2", style: { fontSize: "12.5px", margin: "8px 0 0" } }, "A limit error is honest and immediate: HTTP 429 with your message. Queueing holds the request until a window resets, which suits batch callers. Falling through keeps serving from a model with no budget at all."),
				),
				section("", msgField),
				passField,
			],
		});
	}

	// ----- save / discard / delete -----
	async function save() {
		if (errors.name || !draft.name) {
			errors.name ??= "a name is required";
			renderIdentityErrors();
			nameInput.focus();
			return;
		}
		const defined = new Set(luaFunctions(draft.script ?? ""));
		for (const [index, stage] of draft.stages.entries()) {
			if (stage.accounts !== "all" && !stage.accounts.length) {
				selected = stage.id;
				inspectorOpen = true;
				renderRail(stage.id);
				renderInspector();
				toast(`Stage ${index + 1} has no accounts. Pick at least one.`, "warn");
				return;
			}
			const missing = Object.values(stage.lua ?? {}).filter((name) => !defined.has(name));
			if (missing.length) {
				selected = stage.id;
				inspectorOpen = true;
				renderRail(stage.id);
				renderInspector();
				openLua({ focus: false });
				setScriptError(`Stage ${index + 1} uses ${missing.join(", ")}, which the script does not define. Add the function, or choose Built-in in the stage.`);
				return;
			}
		}
		try {
			const saved = await neo.saveVirtualModel(draft);
			setScriptError(null);
			toast(`Saved neoproxy/${saved.name}`);
			if (isNew) ctx.navigate(`virtual-models/${saved.id}`);
			else {
				Object.assign(vm, saved);
				Object.assign(draft, saved);
				setDirty(false);
				runSim();
			}
		} catch (e) {
			if (isAdmin && isScriptError(e)) {
				openLua({ focus: false });
				setScriptError(e.message);
				toast("Not saved: the routing script has an error.", "bad");
			} else toast(e instanceof ApiError ? e.message : "Could not save", "bad");
		}
	}
	async function discard() {
		if (await confirm({ title: "Discard changes?", text: "The chain goes back to the last saved version.", confirmLabel: "Discard", danger: true })) ctx.navigate("virtual-models");
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
	renderRail();
	renderInspector();
	runSim();
	return () => {
		clearTimeout(luaTimer);
	};
}
