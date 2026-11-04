// My models: what a signed-in member may use. The virtual models they are
// granted, each with their allotment in words and where a request lands right
// now; any direct provider access; their keys with expiry; recent requests.
// No admin surfaces; no provider internals beyond what their chain reveals.

import { h, fmtTime, fmtCompact, plural } from "../lib/dom.js";
import { neo, engine } from "../lib/client.js";
import { pageHead, card, btn, badge, servedName, kv, chainRail, toast, empty, spinner, errorState, field, input, dialog, confirm, providerDot, providerLabel, table, outcomeBadge } from "../lib/components.js";
import { allotmentSentence, keysTable, mintKeyDialog, drawableAccounts } from "./users.js";
import { directModelsFor } from "../lib/access.js";

export async function render(ctx) {
	const { root, session } = ctx;
	root.append(
		pageHead({
			title: "My models",
			desc: "The virtual models you may use, how much of each one's thresholds you have, and the keys that reach them. Everything is served under the neoproxy/ name; which provider answers is decided for you.",
		}),
	);
	const host = h("div");
	root.append(host, spinner());
	let user, vms, models, vault, usage, requests;
	try {
		[user, vms, models, vault, usage, requests] = await Promise.all([neo.user(session.userId), neo.virtualModels(), neo.models(), neo.vault(), neo.usage(), neo.requests({ limit: 10 })]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	const base = `${location.protocol}//${location.host}`;
	host.append(
		card({
			title: "Point a client here",
			sub: "Use one of your keys as the bearer token and one of the names below as the model. Dashboard sessions are not keys.",
			body: kv([
				["OpenAI Chat", h("code", null, `${base}/v1/chat/completions`)],
				["OpenAI Responses", h("code", null, `${base}/v1/responses`)],
				["Anthropic Messages", h("code", null, `${base}/v1/messages`)],
				["Model", h("code", null, user.grants.length ? `neoproxy/${vms.find((v) => v.id === user.grants[0].virtualModel)?.name}` : "ask your admin for a grant")],
			]),
		}),
	);

	if (!user.grants.length) {
		host.append(card({ body: empty({ icon: "route", title: "Nothing granted yet", hint: "Your admin has not given you a virtual model. Ask for neoproxy/default to start." }) }));
	}
	for (const g of user.grants) {
		const vm = vms.find((v) => v.id === g.virtualModel);
		if (!vm) continue;
		let sim;
		try { sim = await neo.simulate({ asked: vm.served, userId: user.id }); }
		catch (e) {
			host.append(card({ title: servedName(vm.served, { virtual: true }), body: errorState(e, () => ctx.rerender()) }));
			continue;
		}
		let drawn = 0;
		for (const st of Object.values(usage[user.id]?.[vm.id] ?? {})) for (const v of Object.values(st)) drawn = Math.max(drawn, v);
		host.append(
			card({
				title: h("span", { class: "row" }, servedName(vm.served, { virtual: true }), sim.outcome === "ok" ? badge("available", "ok") : sim.outcome === "queued" ? badge("would queue", "cyan") : sim.outcome === "limited" ? badge("limit reached", "warn") : badge("unavailable", "bad")),
				sub: vm.description,
				body: [
					h("div", { class: "allot-sentence mine-allot" }, allotmentSentence({ who: "You", them: "you", allotment: g.allotment, vm }), drawn ? ` So far you have drawn ${+(drawn * 100).toFixed(1)}% of the busiest meter.` : ""),
					chainRail(vm, { models, providers: vault.providers, accounts: vault.accounts, lit: sim, allot: g.allotment, redacted: true }),
					sim.outcome !== "ok" ? h("div", { class: "ink-2", style: { marginTop: "8px" } }, sim.message) : h("div", { class: "ink-3", style: { marginTop: "6px", fontSize: "12.5px" } }, sim.stageIndex !== undefined ? `Right now a request lands on stage ${sim.stageIndex + 1}, ${models.find((m) => m.id === sim.model)?.display}.` : `Right now a request falls through to ${models.find((m) => m.id === sim.model)?.display}.`),
				],
			}),
		);
	}

	if (user.providerAccess.length) {
		host.append(
			card({
				title: "Direct provider access",
				sub: "Direct access bypasses virtual models and their budgets; the engine routing strategy applies. You may call these providers by name.",
				body: h("div", { class: "direct" }, user.providerAccess.map((pid) => {
					const p = vault.providers.find((x) => x.id === pid);
					const ms = directModelsFor(user, models, pid);
					const total = models.filter((m) => m.providerId === pid && m.exposed && !m.virtualOnly).length;
					return h("div", { class: "direct-row on" },
						h("div", { class: "direct-head" }, providerDot(p, 9), h("span", null, providerLabel(p).main), h("span", { class: "ink-3 direct-count" }, user.modelAccess?.[pid] ? `${ms.length} of ${plural(total, "model")}` : plural(ms.length, "model"))),
						ms.length ? h("div", { class: "direct-models" }, ms.slice(0, 8).map((m) => h("span", { class: "tag mono", title: m.display }, servedName(m.served))), ms.length > 8 ? h("span", { class: "ink-3" }, `and ${ms.length - 8} more`) : null) : null);
				})),
			}),
		);
	}

	// ----- keys -----
	const keysHost = h("div");
	async function revoke(k) {
		if (!(await confirm({ title: `Revoke ${k.prefix}?`, text: `Anything using "${k.label}" stops working now. This cannot be undone.`, confirmLabel: "Revoke", danger: true }))) return;
		try {
			await neo.revokeKey(user.id, k.id);
			user.keys = user.keys.filter((x) => x.id !== k.id);
			toast("Key revoked");
			renderKeys();
		} catch (e) { toast(e.message, "bad"); }
	}
	function renderKeys() {
		keysHost.replaceChildren(keysTable({ keys: user.keys, accounts: vault.accounts, providers: vault.providers, onRevoke: revoke, scoped: user.keys.some((k) => k.accountIds?.length || k.models?.length), emptyHint: "No keys yet. Mint one for each tool that should reach the proxy." }));
	}
	renderKeys();
	host.append(
		card({
			title: "My keys",
			sub: "A key is only as powerful as you: it reaches the models above and nothing else. Give each tool its own key, with an expiry if it is temporary.",
			actions: [btn("Mint key", { icon: "plus", cls: "outline sm", onClick: () => mintKeyDialog({ user, accounts: user.role === "owner" ? drawableAccounts(user, vault.accounts) : [], providers: vault.providers, models: directModelsFor(user, models), onMinted: (key) => { user.keys.push(key); renderKeys(); } }) })],
			flush: true,
			body: keysHost,
		}),
	);

	// ----- recent -----
	host.append(
		card({
			title: "My recent requests",
			flush: true,
			body: requests.length
				? table([
					{ key: "t", label: "Time", render: (r) => h("span", { class: "mono ink-3" }, fmtTime(r.at)) },
					{ key: "asked", label: "Asked for", render: (r) => servedName(r.asked, { virtual: r.asked.split("/").length === 2 }) },
					{ key: "tok", label: "Tokens", num: true, render: (r) => (r.outcome === "ok" ? fmtCompact(r.tokens.in + r.tokens.out) : "–") },
					{ key: "out", label: "", render: (r) => outcomeBadge(r.outcome) },
				], requests)
				: empty({ title: "Nothing yet", hint: "Requests made with your keys will show here." }),
		}),
	);

	host.append(card({
		title: "Dashboard password",
		sub: "Your password is separate from provider credentials and keys.",
		actions: [btn("Change password", { cls: "outline", onClick: () => {
			let currentPassword = "", password = "";
			const err = h("div", { class: "error", role: "alert" });
			dialog({ title: "Change password", sub: "All existing dashboard sessions and keys will be revoked. Sign in again and mint replacement keys after saving.", body: [
				field("Current password", input({ type: "password", autocomplete: "current-password", onInput: (e) => { currentPassword = e.target.value; } })),
				field("New password", input({ type: "password", autocomplete: "new-password", minlength: 12, onInput: (e) => { password = e.target.value; } }), "12–72 UTF-8 bytes."), err,
			], actions: (close) => [btn("Cancel", { onClick: close }), btn("Save password", { cls: "primary", onClick: async () => {
				try { await engine.post(`/v8/neo/users/${encodeURIComponent(user.id)}/password`, { currentPassword, password }); close(); toast("Password changed. Sign in again."); ctx.rerender(); }
				catch (e) { err.textContent = e.message; }
			} })] });
		} })],
	}));
}
