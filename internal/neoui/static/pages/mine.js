// My models: what a signed-in member sees. Their catalogue with how much of
// each allotment is left, how to point a client at the proxy, and their keys.
// No admin surfaces; no provider internals beyond what their chain reveals.

import { h, icon, fmtPct, fmtAgo, fmtIn, copyText } from "../lib/dom.js";
import { neo, sources } from "../lib/client.js";
import { pageHead, card, btn, badge, servedName, ring, kv, chainRail, trail, toast, empty, spinner, errorState } from "../lib/components.js";

export async function render(ctx) {
	const { root, session } = ctx;
	root.append(
		pageHead({
			title: "My models",
			desc: "What you can ask this proxy for, and how much of each allotment is left. Everything is served under the neoproxy/ name; which provider answers is decided for you.",
			preview: sources.users === "fixture",
		}),
	);
	const host = h("div");
	root.append(host, spinner());
	let user, vms, models, vault, usage;
	try {
		[user, vms, models, vault, usage] = await Promise.all([neo.user(session.userId), neo.virtualModels(), neo.models(), neo.vault(), neo.usage()]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	const base = `${location.protocol}//${location.host}`;
	host.append(
		card({
			title: "Point a client here",
			sub: "Any OpenAI-, Anthropic- or Gemini-shaped client works. Use your key as the bearer token and one of the names below as the model.",
			body: kv([
				["OpenAI", h("code", null, `${base}/v1`)],
				["Anthropic", h("code", null, `${base}/v1/messages`)],
				["Gemini", h("code", null, `${base}/v1beta`)],
				["Model", h("code", null, user.grants.length ? `neoproxy/${vms.find((v) => v.id === user.grants[0].virtualModel)?.name}` : "ask your admin for a grant")],
			]),
		}),
	);

	if (!user.grants.length) {
		host.append(card({ body: empty({ icon: "route", title: "Nothing granted yet", hint: "Your admin has not given you a model. Ask for neoproxy/default to start." }) }));
	}
	for (const g of user.grants) {
		const vm = vms.find((v) => v.id === g.virtualModel);
		if (!vm) continue;
		const usageFor = (s, a) => usage[user.id]?.[vm.id]?.[s.id]?.[a.id] ?? 0;
		const sim = neo.resolveWith({ asked: vm.served, user, virtualModels: [vm] });
		let drawn = 0;
		for (const st of Object.values(usage[user.id]?.[vm.id] ?? {})) for (const v of Object.values(st)) drawn += v;
		const total = g.allotment * vm.stages.reduce((n, s) => n + s.cap, 0);
		host.append(
			card({
				title: h("span", { class: "row" }, servedName(vm.served, { virtual: true }), sim.outcome === "ok" ? badge("available", "ok") : sim.outcome === "queued" ? badge("would queue", "cyan") : badge("limit reached", "warn")),
				sub: vm.description,
				actions: [h("div", { class: "ring-row" }, ring({ used: total ? drawn / total : 0, size: 40, large: true }), h("div", { class: "ring-text" }, h("div", { class: "main" }, "of your allotment used"), h("div", { class: "sub" }, g.allotment < 1 ? `you have ${fmtPct(g.allotment)} of the standard allotment` : "standard allotment")))],
				body: [
					chainRail(vm, { models, accounts: vault.accounts, usageFor, lit: sim, allot: g.allotment, accountLabel: (a) => `account ${vault.accounts.filter((x) => x.providerId === a.providerId).indexOf(a) + 1}` }),
					sim.outcome !== "ok" ? h("div", { class: "ink-2", style: { marginTop: "8px" } }, sim.message) : h("div", { class: "ink-3", style: { marginTop: "6px", fontSize: "12.5px" } }, `Right now a request lands on stage ${sim.stageIndex + 1}, ${models.find((m) => m.id === sim.model)?.display}.`),
				],
			}),
		);
	}

	if (user.providerAccess.length) {
		host.append(
			card({
				title: "Direct provider access",
				sub: "Your admin let you call these providers by name. These draw from the account's own window, with no allotment.",
				body: h("div", { class: "row wrap", style: { paddingTop: "4px" } }, user.providerAccess.map((pid) => { const p = vault.providers.find((x) => x.id === pid); return p ? badge(`neoproxy/${p.id}/…`) : null; })),
			}),
		);
	}

	host.append(
		card({
			title: "Your keys",
			sub: "Ask your admin to issue or revoke keys.",
			flush: true,
			body: h(
				"div",
				{ class: "list" },
				user.keys.length ? user.keys.map((k) => h("div", { class: "li" }, icon("key", 15), h("div", { class: "lt" }, h("div", { class: "main" }, h("code", null, k.prefix), " ", h("span", { class: "ink-2", style: { fontWeight: 400 } }, k.label)), h("div", { class: "sub" }, `issued ${fmtAgo(k.createdAt)} · ${k.lastUsedAt ? `last used ${fmtAgo(k.lastUsedAt)}` : "never used"}`)))) : h("div", { class: "ink-3", style: { padding: "12px 16px" } }, "No keys yet."),
			),
		}),
	);
}
