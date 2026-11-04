// The catalogue of virtual models: each one reads as a model plus the properties
// it always carries, with its fallbacks (if any) underneath.

import { h, icon, fmtAgo, plural, clear } from "../../lib/dom.js";
import { neo, onChange } from "../../lib/client.js";
import { pageHead, card, btn, avatar, servedName, providerDot, providerLabel, spinner, errorState, empty, search, vmSearchText } from "../../lib/components.js";
import { propChips } from "../../lib/vm-props.js";

export async function catalogue(ctx) {
	const { root } = ctx;
	let query = "";
	let limit = 24;
	const summary = h("span", { class: "ink-3", role: "status", "aria-live": "polite" });
	root.append(pageHead({
		title: "Virtual models",
		desc: h("span", null, "Names callers ask for, like ", h("code", null, "neoproxy/luna-fast"), ": a model with properties that are always set, such as Fast mode or a context window, and optionally fallbacks. Direct provider models follow the ", h("a", { class: "link-text", href: "#/routing" }, "routing strategy"), " instead."),
		actions: [
			search("Search by name, model or property", (value) => { query = value.trim().toLowerCase(); limit = 24; paint(); }),
			btn("New virtual model", { icon: "plus", cls: "primary", onClick: () => ctx.navigate("virtual-models/new") }),
		],
	}));
	const host = h("div", { class: "vm-cards" });
	root.append(summary, host, spinner());
	let vms, models, vault, users;
	const specs = new Map();
	try {
		[vms, models, vault, users] = await Promise.all([neo.virtualModels(), neo.models(), neo.vault(), neo.users()]);
		// Chips carry the model's own labels, so load the specs of every model that has properties set.
		const wanted = new Set(vms.flatMap((vm) => vm.stages.filter((s) => Object.keys(s.params ?? {}).length).map((s) => s.model)));
		await Promise.allSettled([...wanted].map((m) => neo.modelParams(m).then((r) => specs.set(m, r?.specs ?? []))));
	} catch (error) {
		root.lastChild.replaceWith(errorState(error, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	const modelOf = (stage) => models.find((m) => m.id === stage.model);
	function stageRow(stage, { first = false, index = 0 } = {}) {
		const m = modelOf(stage);
		const provider = vault.providers.find((p) => p.id === (m?.providerId ?? stage.model.split("/")[0]));
		const label = providerLabel(provider);
		const chips = propChips(stage, specs.get(stage.model));
		return h("div", { class: `vm-row ${first ? "first" : ""}` },
			first ? providerDot(provider, 10) : h("span", { class: "vm-idx sm", "aria-hidden": "true" }, String(index + 1)),
			h("div", { class: "lt" },
				h("div", { class: "vm-row-name" }, h("b", null, m?.display ?? "Unavailable model"), stage.follow ? h("span", { class: "prop follow", title: `Follows the newest model matching ${stage.follow}` }, icon("link", 11), h("span", { class: "prop-v" }, "newest")) : null, h("span", { class: "ink-3" }, `${label.main}${label.source ? ` · ${label.source}` : ""}`)),
				chips.length ? h("div", { class: "vm-row-chips" }, chips) : first ? h("div", { class: "ink-3 vm-row-none" }, "No properties set: the model's own behaviour") : null,
			),
		);
	}

	function paint() {
		clear(host);
		const matches = vms.filter((vm) => !query || vmSearchText(vm, models, vault.providers).includes(query));
		summary.textContent = `${matches.length === vms.length ? `${plural(vms.length, "virtual model")}` : `Showing ${matches.length} of ${vms.length} virtual models`}`;
		if (!matches.length) {
			host.append(card({ body: empty({
				icon: "route",
				title: vms.length ? "No virtual models match" : "No virtual models yet",
				hint: vms.length ? "Search by the upstream model, provider, property or served name." : "Create a name like neoproxy/luna-fast: pick a model and set the properties it should always have.",
				action: vms.length ? null : btn("New virtual model", { icon: "plus", cls: "primary", onClick: () => ctx.navigate("virtual-models/new") }),
			}) }));
			return;
		}
		for (const vm of matches.slice(0, limit)) {
			const granted = users.filter((user) => user.grants.some((grant) => grant.virtualModel === vm.id));
			const author = users.find((user) => user.id === vm.updatedBy || user.handle === vm.updatedBy)?.handle ?? "an administrator";
			const [first, ...rest] = vm.stages;
			const open = () => ctx.navigate(`virtual-models/${vm.id}`);
			const node = card({
				title: h("span", { class: "row" }, servedName(vm.served, { virtual: true })),
				sub: vm.description || null,
				actions: [
					h("span", { class: "row", style: { gap: "2px", marginRight: "6px" } }, granted.slice(0, 4).map((user) => avatar(user, "sm")), granted.length > 4 ? h("span", { class: "ink-3", style: { fontSize: "12px", marginLeft: "4px" } }, `+${granted.length - 4}`) : null),
					btn("Edit", { icon: "arrowRight", cls: "outline sm", onClick: open, title: `Edit ${vm.served}` }),
				],
				body: [
					first ? stageRow(first, { first: true }) : null,
					rest.length ? h("div", { class: "vm-fallbacks" },
						h("div", { class: "vm-fallbacks-title" }, icon("fork", 12), `then ${plural(rest.length, "fallback")}`),
						rest.slice(0, 3).map((s, i) => stageRow(s, { index: i + 1 })),
						rest.length > 3 ? h("div", { class: "ink-3 vm-more-fb" }, `and ${rest.length - 3} more`) : null,
					) : null,
				],
				foot: [
					h("span", { class: "vm-granted" }, granted.length ? `Granted to ${granted.length === 1 ? granted[0].handle : plural(granted.length, "person")}` : "Granted to nobody yet"),
					h("span", { class: "spacer" }),
					h("span", { class: "ink-3 vm-updated", title: vm.updatedBy }, `Updated ${fmtAgo(vm.updatedAt)} by ${author}`),
				],
			});
			node.dataset.vm = vm.id;
			// The whole card opens the editor; that is what someone on this page wants.
			node.classList.add("vm-card");
			node.addEventListener("click", (e) => { if (!e.target.closest("button, a, input, select, textarea")) open(); });
			host.append(node);
		}
		if (matches.length > limit) host.append(btn("Show more", {
			cls: "outline sm vm-show-more",
			onClick: () => {
				const next = matches[limit].id;
				limit += 24;
				paint();
				host.querySelector(`[data-vm="${CSS.escape(next)}"] .btn`)?.focus();
			},
		}));
	}
	paint();
	const off = onChange((what) => what === "virtual-models" && ctx.rerender());
	return () => off();
}
