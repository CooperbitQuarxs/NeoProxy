// Models: everything the proxy serves, as a tree by provider. Never a flat
// list. Virtual models sit on top because they are what users should reach
// for; provider models follow, grouped and collapsible, each with a switch.
// Provider rows are selectable (lib/multiselect.js): sweep or Shift+click a
// range, then Space serves or hides them all in one request, or the bar at
// the bottom gives them to a person as direct models.

import { h, icon, fmtCompact, fmtInt, plural, clear } from "../lib/dom.js";
import { neo, onChange } from "../lib/client.js";
import { pageHead, card, btn, tag, toggle, search, seg, servedName, providerLabel, vmSearchText, vmIdentity, empty, spinner, errorState, toast, confirm, picker, avatar } from "../lib/components.js";
import { propChips } from "../lib/vm-props.js";
import { multiSelect, selectionBar } from "../lib/multiselect.js";
import { matchModel, modelName } from "../lib/access.js";

const debounce = (fn, ms = 150) => { let timer; return (...args) => { clearTimeout(timer); timer = setTimeout(() => fn(...args), ms); }; };

export async function render(ctx) {
	const { root } = ctx;
	root.append(
		pageHead({
			title: "Models",
			desc: "What this proxy serves. Users ask for virtual models; a provider's models are reachable directly only where the provider allows it. A switch here changes what every user sees.",
		}),
	);
	const host = h("div");
	root.append(host, spinner());
	let models, vault, vms, users, requests;
	try {
		[models, vault, vms, users, requests] = await Promise.all([neo.models(), neo.vault(), neo.virtualModels(), neo.users(), neo.requests({ limit: 200 })]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	let query = "";
	let show = "all"; // all | served | hidden
	let vmLimit = 25;
	const vmSearch = new Map(vms.map((vm) => [vm.id, vmSearchText(vm, models, vault.providers)]));
	const providers = new Map(vault.providers.map((p) => [p.id, p]));
	// Big providers (nanogpt, nous, vercel have hundreds) start collapsed and render a page of rows at a time.
	const BIG_GROUP = 40;
	const ROW_PAGE = 60;
	const byProvider = new Map();
	const haystack = new Map();
	for (const m of models) {
		if (!byProvider.has(m.providerId)) byProvider.set(m.providerId, []);
		byProvider.get(m.providerId).push(m);
		const p = providers.get(m.providerId);
		const label = p ? providerLabel(p) : { main: "", source: "" };
		haystack.set(m.id, [m.display, m.name, m.id, label.main, label.source, ...(m.tags ?? [])].filter(Boolean).join("\n").toLowerCase());
	}
	const openState = new Map(); // provider id -> true/false once the person has toggled it
	const rowsShown = new Map(); // provider id -> how many rows are rendered
	// Recent request counts per served name, computed once instead of per row.
	const recent = new Map();
	for (const r of requests) {
		const keys = new Set([r.asked, r.decision?.model ? `neoproxy/${r.decision.model}` : null]);
		for (const key of keys) if (key) recent.set(key, (recent.get(key) ?? 0) + 1);
	}
	const reqCount = (served) => recent.get(served) ?? 0;

	const summary = h("span", { class: "ink-3 summary", role: "status", "aria-live": "polite", style: { fontSize: "12.5px" } });
	const toolbar = h(
		"div",
		{ class: "row models-toolbar", style: { marginBottom: "14px" } },
		search("Filter by name, provider or tag", debounce((v) => {
			query = v.trim().toLowerCase();
			vmLimit = 25;
			rowsShown.clear();
			renderTree();
			renderVMs();
			retainSelection();
		})),
		seg(
			[
				{ value: "all", label: "All" },
				{ value: "served", label: "Served" },
				{ value: "hidden", label: "Hidden" },
			],
			show,
			(v) => {
				show = v;
				renderTree();
				retainSelection();
			},
		),
		h("span", { class: "spacer" }),
		summary,
	);
	host.append(toolbar);

	// Virtual teasers share the provider search, not its exposure filter.
	const vmList = h("div", { class: "list" });
	const vmMore = btn("Show 25 more", { cls: "outline sm", onClick: () => {
		const firstNew = vmLimit;
		vmLimit += 25;
		renderVMs();
		vmList.children[firstNew]?.focus();
	} });
	const vmCard = card({
		title: "Virtual models",
		sub: "Names users ask for. Shared-source models appear here as generated 1:1 virtual models; yours appear alongside them.",
		actions: [btn("Manage", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate("virtual-models") })],
		flush: true,
		body: [vmList, h("div", { style: { padding: "12px 16px" } }, vmMore)],
	});
	host.append(vmCard);

	const treeCard = card({ title: "Provider models", sub: "A switch serves or hides one model; hiding a provider stops every route that draws from it. To change several at once, drag across rows or Shift+click, then press Space.", flush: true, body: h("div", { class: "tree" }) });
	host.append(treeCard);
	const tree = treeCard.querySelector(".tree");

	function renderVMs() {
		const matches = vms.filter((vm) => !query || vmSearch.get(vm.id).includes(query));
		clear(vmList);
		for (const vm of matches.slice(0, vmLimit)) {
			vmList.append(h("button", {
				type: "button", class: "li clickable vm-teaser", title: vm.id,
				onClick: () => ctx.navigate(`virtual-models/${vm.id}`),
			},
			h("div", { class: "lt" }, h("div", { class: "main" }, servedName(vm.served, { virtual: true })), h("div", { class: "sub" }, `${vmIdentity(vm, models, vault.providers)} · ${plural(users.filter((u) => u.grants.some((g) => g.virtualModel === vm.id)).length, "user")} · ${reqCount(vm.served)} requests recently`)),
			h("div", { class: "vm-teaser-props" }, propChips(vm.stages[0] ?? {})),
			icon("chevronRight", 14)));
		}
		if (!matches.length) vmList.append(empty({ title: "No virtual models match", hint: "Try a provider, an upstream model name or a virtual name." }));
		vmMore.classList.toggle("hidden", matches.length <= vmLimit);
		const local = models.filter((m) => !m.virtualOnly);
		const servedLocal = local.filter((m) => m.exposed && providers.get(m.providerId)?.exposed).length;
		const sharedOnly = models.length - local.length;
		summary.textContent = [`${fmtInt(servedLocal)} of ${fmtInt(local.length)} provider models served`, sharedOnly ? `${fmtInt(sharedOnly)} shared-source (virtual only)` : null, plural(vms.length, "virtual model"), query ? `${fmtInt(matches.length)} virtual ${matches.length === 1 ? "match" : "matches"}` : null].filter(Boolean).join(" · ");
	}

	function matches(m, p) {
		if (show === "served" && !(m.exposed && p.exposed)) return false;
		if (show === "hidden" && m.exposed && p.exposed) return false;
		return !query || haystack.get(m.id).includes(query);
	}

	// Rows carry no listeners: the tree delegates the switch (data-act="serve")
	// and selection, so a 640-model provider costs DOM nodes, not handlers.
	function modelRow(m, p) {
		const on = m.exposed && p.exposed;
		return h(
			"div",
			{ class: `model-row ${on ? "" : "off"}`, role: "option", "aria-selected": "false", tabindex: "-1", "data-key": m.id, "aria-label": `${m.display}, ${on ? "served" : "hidden"}` },
			h("div", null, h("div", { class: "cell-main" }, m.display), h("div", { class: "cell-sub" }, servedName(m.served))),
			h("div", { class: "tags" }, (m.tags ?? []).map(tag)),
			h("div", { class: "num ink-3" }, m.context ? `${fmtCompact(m.context)} ctx` : "–"),
			h("div", { class: "num ink-3" }, `${reqCount(m.served)} req`),
			h("button", { type: "button", class: "switch", role: "switch", "aria-checked": String(m.exposed), "aria-label": `Serve ${m.display}`, title: p.exposed ? null : "Serve the provider first", tabindex: "-1", disabled: !p.exposed, "data-act": "serve" }, h("span", { class: "track" })),
		);
	}

	// Serve, hide or invert every model of a provider that matches the current
	// search: one request for all of them, however many there are.
	function bulk(p, list, label) {
		const scope = query ? `the ${fmtInt(list.length)} matching` : `all ${fmtInt(list.length)}`;
		const run = async (mode) => {
			const set = {};
			for (const m of list) {
				const next = mode === "invert" ? !m.exposed : mode === "serve";
				if (m.exposed !== next) set[m.id] = next;
			}
			const n = Object.keys(set).length;
			if (!n) { toast(mode === "invert" ? "Nothing to invert" : `Already ${mode === "serve" ? "served" : "hidden"}`); return; }
			if (n > 50 && !(await confirm({ title: `${{ serve: "Serve", hide: "Hide", invert: "Invert" }[mode]} ${fmtInt(n)} models?`, text: `${label.main}: ${scope} models${query ? ` matching “${query}”` : ""}. ${mode === "hide" ? "Every user loses them at once." : mode === "serve" ? "Every user with direct access gains them at once." : "Served ones are hidden and hidden ones served."}`, confirmLabel: { serve: "Serve them", hide: "Hide them", invert: "Invert" }[mode], danger: mode === "hide" }))) return;
			try {
				const r = await neo.setModelsExposed(set);
				for (const m of list) if (m.id in set) m.exposed = set[m.id];
				toast(`${fmtInt(r.changed ?? n)} models ${mode === "invert" ? "inverted" : mode === "serve" ? "served" : "hidden"}${r.unknown?.length ? ` · ${r.unknown.length} unknown skipped` : ""}`);
			} catch (e) { toast(e.message, "bad"); }
			renderTree();
			renderVMs();
		};
		return h("div", { class: "seg sm bulk", role: "group", "aria-label": `${label.main}: ${scope} models` },
			h("button", { type: "button", onClick: () => run("serve"), title: `Serve ${scope} ${label.main} models` }, icon("eye", 13), "Serve ", scope),
			h("button", { type: "button", onClick: () => run("hide"), title: `Hide ${scope} ${label.main} models` }, icon("eyeOff", 13), "Hide"),
			h("button", { type: "button", onClick: () => run("invert"), title: `Invert ${scope}: served become hidden and hidden become served` }, icon("shuffle", 13), "Invert"),
		);
	}

	function renderTree() {
		const frag = document.createDocumentFragment();
		let any = false;
		for (const p of vault.providers) {
			const all = byProvider.get(p.id) ?? [];
			const list = all.filter((m) => matches(m, p));
			if (!list.length) continue;
			any = true;
			const label = providerLabel(p);
			const accs = vault.accounts.filter((a) => a.providerId === p.id);
			// A person's own choice wins; otherwise a search opens its matches and big providers stay shut.
			const isOpen = openState.has(p.id) ? openState.get(p.id) : query ? true : all.length <= BIG_GROUP;
			const servedCount = p.exposed ? all.filter((m) => m.exposed).length : 0;
			let providerSwitch;
			providerSwitch = toggle(p.exposed, async (v) => {
				try {
					await neo.setProviderExposed(p.id, v);
				} catch (e) {
					providerSwitch.setAttribute("aria-checked", String(!v));
					toast(e.message, "bad");
					return;
				}
				p.exposed = v;
				toast(v ? `${label.main} served` : `${label.main} hidden from everyone`);
				renderTree();
				renderVMs();
			}, `Serve ${label.main}`);
			frag.append(
				h(
					"div",
					{ class: `group-row ${p.exposed ? "" : "off"}`, "aria-expanded": String(isOpen) },
					h(
						"button",
						{
							type: "button",
							class: "name",
							onClick: () => {
								openState.set(p.id, !isOpen);
								renderTree();
							},
							"aria-expanded": String(isOpen),
							"aria-label": `${isOpen ? "Collapse" : "Expand"} ${label.main}, ${plural(all.length, "model")}`,
						},
						h("span", { class: "caret" }, icon("chevronDown", 14)),
						h("span", { title: p.id }, label.main),
						h("span", { class: "ink-3", style: { fontWeight: 400 } }, [label.source, p.kind === "neopi-gateway" ? "virtual models only" : `neoproxy/${p.id}/…`].filter(Boolean).join(" · ")),
					),
					h("div", { class: "meta" },
						h("span", null, p.exposed ? `${fmtInt(servedCount)} of ${plural(all.length, "model")} served` : `hidden · ${plural(all.length, "model")}`),
						list.length !== all.length ? h("span", null, `${fmtInt(list.length)} shown`) : null,
						h("span", null, plural(accs.length, "account")),
						h("span", null, p.kind === "neopi-gateway" ? "grant virtual models in Users" : `${plural(users.filter((u) => u.providerAccess.includes(p.id)).length, "user")} direct`)),
					p.exposed && list.length > 1 ? bulk(p, list, label) : null,
					providerSwitch,
				),
			);
			if (!isOpen) continue;
			const shown = Math.min(list.length, rowsShown.get(p.id) ?? ROW_PAGE);
			const rows = h("div", { class: "model-rows", role: "listbox", "aria-multiselectable": "true", "aria-label": `${label.main} models` });
			for (let i = 0; i < shown; i++) rows.append(modelRow(list[i], p));
			frag.append(rows);
			if (shown < list.length) frag.append(h("div", { class: "model-more" },
				btn(`Show ${fmtInt(Math.min(ROW_PAGE * 2, list.length - shown))} more`, { cls: "outline sm", onClick: () => { rowsShown.set(p.id, shown + ROW_PAGE * 2); renderTree(); } }),
				h("span", { class: "ink-3" }, `${fmtInt(shown)} of ${fmtInt(list.length)} shown${query ? "" : ". Search to jump to one."}`)));
		}
		if (!any) frag.append(!vault.providers.length && !query && show === "all"
			? empty({ icon: "shield", title: "No provider models yet", hint: "Add a provider credential and its models appear here.", action: btn("Add provider access", { icon: "plus", cls: "outline", onClick: () => ctx.navigate("pool") }) })
			: empty({ icon: "search", title: "No models match", hint: "Try a provider name, a model id, or a tag like fast or reasoning." }));
		tree.replaceChildren(frag);
		ms.refresh();
	}

	// ----- selection: Space and the bar act on every selected model at once -----
	const modelById = new Map(models.map((m) => [m.id, m]));
	const matchingIn = (providerId) => {
		const p = providers.get(providerId);
		return p ? (byProvider.get(providerId) ?? []).filter((m) => matches(m, p)) : [];
	};
	const verbs = { serve: "served", hide: "hidden", invert: "inverted" };
	// Apply {modelId: exposed} in one request; the toast can put it back.
	async function applyExposure(set, verb, { undoable = true, note = "" } = {}) {
		const ids = Object.keys(set);
		const before = Object.fromEntries(ids.map((id) => [id, modelById.get(id).exposed]));
		try {
			const r = await neo.setModelsExposed(set);
			for (const id of ids) modelById.get(id).exposed = set[id];
			const n = r?.changed ?? ids.length;
			const text = ids.length === 1 ? `${modelById.get(ids[0]).display} ${verb}` : `${fmtInt(n)} models ${verb}`;
			const undo = undoable ? h("button", { type: "button", class: "link-btn toast-act", onClick: (e) => { e.currentTarget.closest(".toast")?.remove(); applyExposure(before, "restored", { undoable: false }); } }, "Undo") : null;
			toast([text, note, undo], "ok", undoable ? 6000 : 2800);
		} catch (e) {
			toast(e.message, "bad");
		}
		renderTree();
		renderVMs();
	}
	// mode: serve | hide | invert | toggle. Toggle hides them when every one is
	// served and serves them all otherwise, so a mixed selection ends up uniform.
	function changeExposure(keys, mode) {
		const list = keys.map((k) => modelById.get(k)).filter((m) => m && providers.get(m.providerId)?.exposed);
		const skipped = keys.length - list.length;
		if (mode === "toggle") mode = list.length && list.every((m) => m.exposed) ? "hide" : "serve";
		const set = {};
		for (const m of list) {
			const next = mode === "invert" ? !m.exposed : mode === "serve";
			if (m.exposed !== next) set[m.id] = next;
		}
		const skipNote = skipped ? `${plural(skipped, "model")} on hidden providers left alone; serve the provider first` : "";
		if (!Object.keys(set).length) { toast(skipNote || `Already ${verbs[mode]}`, skipped ? "warn" : "ok"); return; }
		applyExposure(set, verbs[mode], { note: skipNote ? `; ${skipNote}` : "" });
	}

	const directSummary = (u) => (u.providerAccess.length ? `direct: ${u.providerAccess.map((pid) => providerLabel(providers.get(pid)).main).join(", ")}` : "no direct access yet");
	function givePicker(anchor) {
		const chosen = [...ms.selected].map((k) => modelById.get(k)).filter(Boolean);
		const direct = chosen.filter((m) => !m.virtualOnly && providers.get(m.providerId)?.kind !== "neopi-gateway");
		if (!direct.length) { toast("Shared-source models are reached through virtual models: grant one in Users instead.", "warn"); return; }
		picker(anchor, {
			items: users.map((u) => ({ value: u.id, label: u.handle, sub: `${u.name ? `${u.name}, ` : ""}${u.role}; ${directSummary(u)}`, lead: avatar(u) })),
			placeholder: "Find a person",
			empty: "No one matches",
			onPick: (id) => giveTo(users.find((u) => u.id === id), direct, chosen.length - direct.length),
		});
		// The bar sits at the bottom of the page, so open the list above it and
		// let it grow upwards as the search narrows it.
		const pop = document.body.lastElementChild;
		if (pop?.classList.contains("picker-pop")) {
			pop.style.top = "auto";
			pop.style.bottom = `${document.documentElement.clientHeight - anchor.getBoundingClientRect().top - scrollY + 8}px`;
		}
	}
	// Add the models to the person's direct access: a provider they lack is
	// turned on for just these models; one they have with a list gains them; one
	// they have without a list already serves every model.
	async function giveTo(u, list, shared) {
		const access = new Set(u.providerAccess);
		const allow = structuredClone(u.modelAccess ?? {});
		let added = 0;
		for (const m of list) {
			const name = modelName(m);
			if (!access.has(m.providerId)) {
				access.add(m.providerId);
				allow[m.providerId] = [name];
				added++;
			} else if (allow[m.providerId] && !matchModel(allow[m.providerId], name)) {
				allow[m.providerId].push(name);
				added++;
			}
		}
		if (!added) { toast(`${u.handle} can already call ${list.length === 1 ? "it" : "all of them"} directly`); return; }
		try {
			Object.assign(u, await neo.saveUser({ ...u, providerAccess: [...access], modelAccess: allow, password: undefined }));
		} catch (e) {
			toast(e.message, "bad");
			return;
		}
		const hidden = list.filter((m) => !m.exposed || !providers.get(m.providerId)?.exposed).length;
		toast([
			`${u.handle} can now call ${plural(added, "more model")} directly`,
			hidden ? `; ${fmtInt(hidden)} ${hidden === 1 ? "is" : "are"} hidden, so serve ${hidden === 1 ? "it" : "them"} first` : "",
			shared ? `; ${fmtInt(shared)} shared-source skipped` : "",
			h("button", { type: "button", class: "link-btn toast-act", onClick: () => ctx.navigate(`users/${u.id}`) }, `Open ${u.handle}`),
		], "ok", 7000);
		renderTree();
	}

	const bar = selectionBar({
		actions: [
			{ label: "Serve", icon: "eye", title: "Serve the selected models", onClick: () => changeExposure([...ms.selected], "serve") },
			{ label: "Hide", icon: "eyeOff", title: "Hide the selected models from everyone", onClick: () => changeExposure([...ms.selected], "hide") },
			{ label: "Invert", icon: "shuffle", title: "Served ones are hidden, hidden ones served", onClick: () => changeExposure([...ms.selected], "invert") },
			{ label: "Give to person…", icon: "user", cls: "accent", title: "Let one person call the selected models directly", onClick: (e) => givePicker(e.currentTarget) },
		],
		onClear: () => { ms.clear(); ms.focus(); },
	});
	bar.el.addEventListener("keydown", (e) => { if (e.key === "Escape" && !document.querySelector(".picker-pop")) { ms.clear(); ms.focus(); } });
	const ms = multiSelect(tree, {
		row: ".model-row",
		groupKeys: (el) => matchingIn(modelById.get(el.dataset.key)?.providerId).map((m) => m.id),
		onChange: (sel) => bar.update(sel.size),
		onSpace: (keys) => changeExposure(keys, "toggle"),
	});
	// Filtering keeps only the selected models that still match.
	function retainSelection() {
		const keep = [...ms.selected].filter((k) => { const m = modelById.get(k); return m && matches(m, providers.get(m.providerId)); });
		if (keep.length !== ms.selected.size) ms.set(keep);
	}
	// The one switch handler for every row.
	tree.addEventListener("click", (e) => {
		const sw = e.target.closest('[data-act="serve"]');
		const m = sw && !sw.disabled && modelById.get(sw.closest(".model-row")?.dataset.key);
		if (!m) return;
		sw.setAttribute("aria-checked", String(!m.exposed));
		applyExposure({ [m.id]: !m.exposed }, m.exposed ? "hidden" : "served");
	});
	root.append(bar.el);

	renderTree();
	renderVMs();
	const off = onChange((w) => {
		if (w === "vault" || w === "models") {
			renderTree();
			renderVMs();
		}
	});
	return () => { off(); ms.destroy(); };
}
