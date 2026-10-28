// Virtual models: names the proxy serves that are not a provider's model.
// Each one is a chain of stages; a stage is a real model drawn from a pool of
// accounts in measured sips. The catalogue shows every chain at a glance; the
// editor is a rail you click into, with a docked inspector for the selected
// stage and a simulator that shows where a user's request would land.

import { h, icon, fmtPct, fmtAgo, plural, clear } from "../lib/dom.js";
import { neo, onChange, ApiError } from "../lib/client.js";
import { pageHead, card, btn, badge, avatar, servedName, chainRail, inspector, section, field, input, select, seg, toggle, modelPicker, trail, ring, kv, confirm, toast, empty, spinner, errorState, drawLabel } from "../lib/components.js";

export async function render(ctx) {
	if (ctx.params[0]) return editor(ctx, ctx.params[0]);
	return catalogue(ctx);
}

// ---------- catalogue ----------

async function catalogue(ctx) {
	const { root } = ctx;
	root.append(
		pageHead({
			title: "Virtual models",
			desc: "Names you serve that are not any provider's model. Each is a chain: stages that draw from pooled accounts in measured sips, a demotion when a stage is spent, and a clear end.",
			actions: [btn("New virtual model", { icon: "plus", cls: "primary", onClick: () => ctx.navigate("virtual-models/new") })],
		}),
	);
	const host = h("div");
	root.append(host, spinner());
	let vms, models, vault, users, usage;
	try {
		[vms, models, vault, users, usage] = await Promise.all([neo.virtualModels(), neo.models(), neo.vault(), neo.users(), neo.usage()]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();
	if (!vms.length) {
		host.append(card({ body: empty({ icon: "route", title: "No virtual models yet", hint: "Create one to give users a name like neoproxy/default that you decide the meaning of.", action: btn("New virtual model", { icon: "plus", cls: "primary", onClick: () => ctx.navigate("virtual-models/new") }) }) }));
		return;
	}
	const usageFor = (s, a) => {
		let n = 0;
		for (const u of Object.values(usage)) n += u?.[vmFor(s)?.id]?.[s.id]?.[a.id] ?? 0;
		return n;
	};
	const stageOwner = new Map();
	for (const vm of vms) for (const s of vm.stages) stageOwner.set(s.id + vm.id, vm);
	const vmFor = (s) => vms.find((vm) => vm.stages.includes(s));
	for (const vm of vms) {
		const granted = users.filter((u) => u.grants.some((g) => g.virtualModel === vm.id));
		const recentLimited = 0;
		host.append(
			card({
				title: h("span", { class: "row" }, servedName(vm.served, { virtual: true })),
				sub: vm.description,
				actions: [
					h("span", { class: "row", style: { gap: "2px", marginRight: "6px" } }, granted.slice(0, 5).map((u) => avatar(u, "sm")), granted.length > 5 ? h("span", { class: "ink-3", style: { fontSize: "12px", marginLeft: "4px" } }, `+${granted.length - 5}`) : null),
					btn("Edit", { icon: "arrowRight", cls: "outline sm", onClick: () => ctx.navigate(`virtual-models/${vm.id}`) }),
				],
				body: chainRail(vm, { models, accounts: vault.accounts, usageFor }),
				foot: [
					h("span", null, `Granted to ${granted.length ? granted.map((u) => u.handle).join(", ") : "nobody yet"}`),
					h("span", { class: "spacer" }),
					h("span", { class: "ink-3" }, `Updated ${fmtAgo(vm.updatedAt)} by ${vm.updatedBy}`),
				],
			}),
		);
	}
	const off = onChange((w) => w === "virtual-models" && ctx.rerender());
	return () => off();
}

// ---------- editor ----------

const newStage = (model) => ({ id: `s${Math.random().toString(36).slice(2, 7)}`, model, accounts: "all", draw: "least-used", perAccount: 0.05, cap: 0.2, window: "week", per: "user", effort: null });

async function editor(ctx, id) {
	const { root } = ctx;
	const isNew = id === "new";
	root.append(spinner());
	let vm, models, vault, users, usage;
	try {
		[models, vault, users, usage] = await Promise.all([neo.models(), neo.vault(), neo.users(), neo.usage()]);
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
	let selected = draft.stages[0]?.id ?? null; // stage id or "terminal"
	let simUser = users.find((u) => u.role === "member" && u.state === "active")?.id ?? users[0]?.id;
	let simResult = null;
	const errors = {};

	const usageFor = (s, a) => {
		let n = 0;
		for (const u of Object.values(usage)) n += u?.[draft.id]?.[s.id]?.[a.id] ?? 0;
		return n;
	};

	// ----- head -----
	const saveBtn = btn("Save", { icon: "save", cls: "primary", onClick: save });
	const head = pageHead({
		back: { href: "#/virtual-models", label: "Virtual models" },
		title: h("span", { class: "row", id: "vm-title" }, isNew ? "New virtual model" : servedName(draft.served, { virtual: true })),
		desc: isNew ? "Name it, add stages, and try it against a user before saving." : null,
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
			renderRail();
			setDirty();
		},
	});
	const nameWrap = h("div", { style: { position: "relative" } }, h("span", { class: "served", style: { position: "absolute", left: "10px", top: "7px", pointerEvents: "none" }, "aria-hidden": "true" }, h("span", { class: "pre" }, "neoproxy/")), nameInput);
	const nameField = field("Name", nameWrap, "What users type as the model id. Keep it a role, not a brand: default, small, heavy.");
	const renderIdentityErrors = () => {
		const hint = nameField.querySelector(".hint, .error");
		hint.className = errors.name ? "error" : "hint";
		hint.textContent = errors.name ?? "What users type as the model id. Keep it a role, not a brand: default, small, heavy.";
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
	if (ctx.session.role === "admin") {
		root.append(card({
			title: "Routing script · optional",
			sub: "Lua source (maximum 16 KiB). Empty uses the standard chain. Read-only global input contains asked, protocol, userId, virtualModel, stages and account health. Return exactly {stageId='s', accountId='a'} to propose an eligible candidate.",
			body: field("Lua source", h("textarea", {
				class: "textarea mono", rows: 9, spellcheck: "false", "aria-label": "Lua routing script",
				placeholder: "Leave empty for standard routing",
				onInput: (e) => { draft.script = e.target.value; setDirty(); },
			}, draft.script ?? ""), "Save before simulating. No OS, filesystem, network or module access. Scripts cannot override grants, ownership, exposure, health or budgets. Invalid syntax/runtime results are backend errors."),
		}));
	}

	// ----- chain -----
	const railHost = h("div");
	root.append(
		card({
			title: "Chain",
			sub: "Left to right: a request arrives, each stage is tried in order, and when every stage is spent the chain ends the way you choose. Click a stage to edit it; drag it, or press Alt with ← →, to reorder.",
			body: railHost,
		}),
	);
	function renderRail() {
		clear(railHost);
		const lit = simResult;
		railHost.append(
			chainRail(draft, {
				models,
				accounts: vault.accounts,
				usageFor,
				lit,
				selected,
				editor: true,
				allot: 1,
				onSelect: (s) => {
					selected = s === "terminal" ? "terminal" : s.id;
					renderRail();
					renderInspector();
				},
				onAdd: addStage,
				onReorder: (next) => {
					draft.stages = next;
					setDirty();
					renderRail();
					runSim();
				},
			}),
		);
		railHost.querySelector('.node[aria-selected="true"]')?.scrollIntoView({ inline: "nearest", block: "nearest" });
		const t = document.getElementById("vm-title");
		if (t && !isNew) {
			clear(t);
			t.append(servedName(draft.served, { virtual: true }));
		}
	}
	function addStage() {
		const usedProviders = new Set(draft.stages.map((s) => s.model.split("/")[0]));
		const candidate = models.find((m) => m.exposed && vault.providers.find((p) => p.id === m.providerId)?.exposed && !usedProviders.has(m.providerId)) ?? models.find((m) => m.exposed);
		if (!candidate) { toast("No exposed provider models are available. Add an account and expose its catalogue first.", "warn"); return; }
		const s = newStage(candidate.id);
		draft.stages.push(s);
		selected = s.id;
		setDirty();
		renderRail();
		renderInspector();
		runSim();
	}

	// ----- simulator -----
	const simHost = h("div");
	const simUserSel = select(
		users.map((u) => ({ value: u.id, label: `${u.handle} · ${u.role}` })),
		simUser,
		(v) => {
			simUser = v;
			runSim();
		},
	);
	root.append(
		card({
			title: "Try it",
			sub: "Uses the saved model, the selected user's actual grants and current ledger. Save changes first; simulation never grants access or records usage.",
			actions: [h("div", { style: { width: "220px" } }, simUserSel)],
			body: simHost,
		}),
	);
	let simGeneration = 0;
	resetSimulation = () => {
		++simGeneration;
		simResult = null;
		simHost.replaceChildren(h("div", { class: "ink-3" }, "Save changes before running a real simulation."));
		renderRail();
	};
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
			const result = await neo.simulate({ asked: vm.served, userId: simUser });
			if (generation !== simGeneration || !simHost.isConnected) return;
			simResult = result;
			simHost.replaceChildren(trail(result, { models, accounts: vault.accounts, users }));
		} catch (e) {
			if (generation !== simGeneration || !simHost.isConnected) return;
			simHost.replaceChildren(errorState(e, runSim));
		}
		renderRail();
	}

	// ----- grants -----
	const grantHost = h("div", { class: "list" });
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
			let drawn = 0;
			for (const st of Object.values(usage[u.id]?.[draft.id] ?? {})) for (const v of Object.values(st)) drawn += v;
			grantHost.append(h("button", { type: "button", class: "li clickable", onClick: () => ctx.navigate(`users/${u.id}`) }, avatar(u), h("div", { class: "lt" }, h("div", { class: "main" }, u.handle), h("div", { class: "sub" }, `allotment ${fmtPct(g.allotment)} of every budget in this chain`)), h("span", { class: "num ink-2" }, `${fmtPct(drawn, 1)} drawn`), icon("chevronRight", 14)));
		}
	}
	root.append(card({ title: "Granted to", sub: "Each user's allotment scales every budget in this chain. Half an allotment is half the sips and half the cap.", flush: true, body: grantHost }));
	renderGrants();

	// ----- inspector -----
	function renderInspector() {
		if (!selected) return ctx.setInspector(null);
		if (selected === "terminal") return ctx.setInspector(terminalInspector());
		const s = draft.stages.find((x) => x.id === selected);
		if (!s) return ctx.setInspector(null);
		ctx.setInspector(stageInspector(s));
	}
	function stageInspector(s) {
		const idx = draft.stages.indexOf(s);
		const model = models.find((m) => m.id === s.model);
		const pool = () => vault.accounts.filter((a) => a.providerId === s.model.split("/")[0]);
		const chosen = () => pool().filter((a) => s.accounts === "all" || s.accounts.includes(a.id));
		const update = () => {
			setDirty();
			renderRail();
			runSim();
		};
		const accountsSection = h("div");
		const renderAccounts = () => {
			clear(accountsSection);
			const all = pool();
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
						renderAccounts();
					},
				),
			);
			for (const a of all) {
				const on = s.accounts === "all" || s.accounts.includes(a.id);
				const w = a.windows.find((x) => x.kind === s.window) ?? a.windows[0];
				accountsSection.append(
					h(
						"label",
						{ class: "row", style: { padding: "8px 0", borderBottom: "1px solid var(--line-1)", cursor: s.accounts === "all" ? "default" : "pointer" } },
						h("input", {
							type: "checkbox",
							checked: on,
							disabled: s.accounts === "all",
							onChange: (e) => {
								const set = new Set(s.accounts === "all" ? all.map((x) => x.id) : s.accounts);
								if (e.target.checked) set.add(a.id);
								else set.delete(a.id);
								s.accounts = [...set];
								update();
							},
						}),
						ring({ used: w?.used ?? 0, size: 24, label: false, title: `${fmtPct(w?.used ?? 0)} of the ${w?.kind} window used by everyone` }),
						h("div", { class: "lt", style: { flex: 1, minWidth: 0 } }, h("div", { style: { fontWeight: 500 } }, h("code", null, a.label)), h("div", { class: "ink-3", style: { fontSize: "12px" } }, `${a.plan} · ${a.state}`)),
						h("span", { class: "num ink-3", style: { fontSize: "12px" } }, `${fmtPct(usageFor(s, a), 1)} by this stage`),
					),
				);
			}
		};
		renderAccounts();
		const pct = (v) => Math.round(v * 1000) / 10;
		return inspector({
			title: `Stage ${idx + 1} · ${model?.display ?? s.model}`,
			sub: `${drawLabel(s.draw, chosen().length)} · ${fmtPct(s.perAccount)} each, ${fmtPct(s.cap)} cap · ${s.window}`,
			onClose: () => {
				selected = null;
				renderRail();
				ctx.setInspector(null);
			},
			body: [
				section(
					"Model",
					modelPicker({
						providers: vault.providers,
						models,
						accounts: vault.accounts,
						value: s.model,
						onPick: (m) => {
							const changedProvider = m.providerId !== s.model.split("/")[0];
							s.model = m.id;
							if (changedProvider) s.accounts = "all";
							update();
							renderInspector();
						},
					}),
					h("div", { class: "hint", style: { marginTop: "6px", fontSize: "12px", color: "var(--ink-3)" } }, "Only providers the core holds and you serve. Hidden providers and models do not appear."),
				),
				section("Accounts in the pool", accountsSection),
				section(
					"How to draw",
					seg(
						[
							{ value: "alternate", label: "Alternate", title: "Each request moves to the next account" },
							{ value: "least-used", label: "Least used", title: "Pick the account with the most room" },
							{ value: "fill", label: "Fill", title: "Use the first account until its sip is gone, then the next" },
						],
						s.draw,
						(v) => {
							s.draw = v;
							update();
						},
					),
				),
				section(
					"Budget",
					h(
						"div",
						{ class: "grid c2" },
						field("Per account", h("div", { class: "row" }, input({ type: "number", min: 0, max: 100, step: 0.5, value: pct(s.perAccount), style: { width: "80px" }, onInput: (e) => { s.perAccount = Number(e.target.value) / 100; update(); } }), h("span", { class: "ink-3" }, "%")), "of each account's window"),
						field("Cap", h("div", { class: "row" }, input({ type: "number", min: 0, max: 100, step: 0.5, value: pct(s.cap), style: { width: "80px" }, onInput: (e) => { s.cap = Number(e.target.value) / 100; update(); } }), h("span", { class: "ink-3" }, "%")), "across the whole stage"),
					),
					field(
						"Window",
						seg(
							["5h", "day", "week", "month"].map((w) => ({ value: w, label: w })),
							s.window,
							(v) => {
								s.window = v;
								update();
								renderAccounts();
							},
						),
						"Budgets are a share of this quota window and reset with it.",
					),
					field(
						"Counted",
						seg(
							[
								{ value: "user", label: "Per user", title: "Every granted user gets this budget" },
								{ value: "all", label: "Shared", title: "One budget for everyone granted" },
							],
							s.per,
							(v) => {
								s.per = v;
								update();
							},
						),
					),
				),
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
						(v) => {
							s.effort = v || null;
							update();
						},
					),
				),
			],
			foot: [
				btn("Remove stage", {
					icon: "trash",
					cls: "danger",
					onClick: () => {
						draft.stages = draft.stages.filter((x) => x !== s);
						selected = draft.stages[0]?.id ?? null;
						update();
						renderInspector();
					},
				}),
			],
		});
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
						models,
						accounts: vault.accounts,
						value: t.model ?? null,
						onPick: (m) => {
							t.model = m.id;
							setDirty();
							renderRail();
							runSim();
						},
					}),
					"Served without a budget or a separate direct-access grant; the model must be exposed and have a healthy account.",
				),
			);
		};
		renderPass();
		return inspector({
			title: "When every stage is spent",
			sub: "The chain's end",
			onClose: () => {
				selected = null;
				renderRail();
				ctx.setInspector(null);
			},
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
							renderRail();
							runSim();
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
		try {
			const saved = await neo.saveVirtualModel(draft);
			toast(`Saved neoproxy/${saved.name}`);
			if (isNew) ctx.navigate(`virtual-models/${saved.id}`);
			else {
				Object.assign(vm, saved);
				Object.assign(draft, saved);
				setDirty(false);
				runSim();
			}
		} catch (e) {
			toast(e instanceof ApiError ? e.message : "Could not save", "bad");
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
	const esc = (e) => {
		if (e.target?.closest?.("input,textarea,select")) return;
		if (e.key === "Escape" && selected) {
			selected = null;
			renderRail();
			ctx.setInspector(null);
		}
		// Alt+←/→ moves the selected stage; the keyboard path for drag-to-reorder.
		if (e.altKey && (e.key === "ArrowLeft" || e.key === "ArrowRight") && selected && selected !== "terminal") {
			const i = draft.stages.findIndex((x) => x.id === selected);
			const j = e.key === "ArrowLeft" ? i - 1 : i + 1;
			if (i === -1 || j < 0 || j >= draft.stages.length) return;
			e.preventDefault();
			const next = draft.stages.slice();
			[next[i], next[j]] = [next[j], next[i]];
			draft.stages = next;
			setDirty();
			renderRail();
			runSim();
		}
	};
	document.addEventListener("keydown", esc);
	return () => document.removeEventListener("keydown", esc);
}
