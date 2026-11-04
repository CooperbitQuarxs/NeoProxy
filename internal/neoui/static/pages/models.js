// Models: everything the proxy serves, as a tree by provider. Never a flat
// list. Virtual models sit on top because they are what users should reach
// for; provider models follow, grouped and collapsible, each with a switch.

import { h, icon, fmtCompact, fmtInt, plural, clear } from "../lib/dom.js";
import { neo, onChange } from "../lib/client.js";
import { pageHead, card, btn, tag, toggle, search, seg, servedName, chainRail, providerLabel, vmSearchText, vmIdentity, empty, spinner, errorState, toast, confirm } from "../lib/components.js";

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

	const treeCard = card({ title: "Provider models", sub: "All / Served / Hidden filters only these provider models. Local providers may have direct selectors; shared-source models use stored virtual names. Hiding a provider disables all routes that draw from it.", flush: true, body: h("div", { class: "tree" }) });
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
			h("div", { class: "rail-clip" }, chainRail(vm, { models, providers: vault.providers, accounts: vault.accounts, compact: true })),
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

	function modelRow(m, p) {
		let control;
		control = toggle(m.exposed, async (v) => {
			try {
				await neo.setModelExposed(m.id, v);
			} catch (e) {
				control.setAttribute("aria-checked", String(!v));
				toast(e.message, "bad");
				return;
			}
			m.exposed = v;
			toast(v ? `${m.display} served` : `${m.display} hidden`);
			renderTree();
			renderVMs();
		}, `Serve ${m.display}`, { disabled: !p.exposed, showLabel: false });
		return h(
			"div",
			{ class: `model-row ${m.exposed && p.exposed ? "" : "off"}` },
			h("div", null, h("div", { class: "cell-main" }, m.display), h("div", { class: "cell-sub" }, servedName(m.served))),
			h("div", { class: "tags" }, (m.tags ?? []).map(tag)),
			h("div", { class: "num ink-3" }, m.context ? `${fmtCompact(m.context)} ctx` : "–"),
			h("div", { class: "num ink-3" }, `${reqCount(m.served)} req`),
			control,
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
			for (let i = 0; i < shown; i++) frag.append(modelRow(list[i], p));
			if (shown < list.length) frag.append(h("div", { class: "model-more" },
				btn(`Show ${fmtInt(Math.min(ROW_PAGE * 2, list.length - shown))} more`, { cls: "outline sm", onClick: () => { rowsShown.set(p.id, shown + ROW_PAGE * 2); renderTree(); } }),
				h("span", { class: "ink-3" }, `${fmtInt(shown)} of ${fmtInt(list.length)} shown${query ? "" : ". Search to jump to one."}`)));
		}
		if (!any) frag.append(!vault.providers.length && !query && show === "all"
			? empty({ icon: "shield", title: "No provider models yet", hint: "Add a provider credential and its models appear here.", action: btn("Add provider access", { icon: "plus", cls: "outline", onClick: () => ctx.navigate("pool") }) })
			: empty({ icon: "search", title: "No models match", hint: "Try a provider name, a model id, or a tag like fast or reasoning." }));
		tree.replaceChildren(frag);
	}
	renderTree();
	renderVMs();
	const off = onChange((w) => {
		if (w === "vault" || w === "models") {
			renderTree();
			renderVMs();
		}
	});
	return () => off();
}
