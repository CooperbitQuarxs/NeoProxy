// My models: what a signed-in member may use. Each granted virtual model is one
// card: a single sentence saying whether it is available and how much is left
// (from the server's own simulation), the model with its settings, the fallback
// chain on one line, and one Details disclosure holding the settings in words
// and how the limit is worked out. Then direct provider access (compact rows,
// full name on demand), their keys and their recent requests.

import { h, icon, fmtTime, fmtCompact, fmtIn, plural, copyText } from "../lib/dom.js";
import { neo, engine } from "../lib/client.js";
import { pageHead, card, btn, badge, servedName, toast, empty, spinner, errorState, field, input, dialog, confirm, providerDot, providerLabel, table, outcomeBadge, search } from "../lib/components.js";
import { keysTable, mintKeyDialog, drawableAccounts } from "../lib/person-keys.js";
import { directModelsFor } from "../lib/access.js";
import { propChips, propNotes, propDetails } from "../lib/vm-props.js";
import { loadVmSpecs } from "../lib/vm-line.js";

const WINDOW_WORDS = { "5h": "5-hour", day: "daily", week: "weekly", month: "monthly" };
const pct = (x) => `${+(x * 100).toFixed(1)}%`;
// Connect, opened on one model: "Use it" carries the choice.
export const connectHref = (served) => `#/connect?model=${encodeURIComponent(served)}`;

const OUTCOME = {
	ok: ["available", "ok"],
	queued: ["waiting", "cyan"],
	limited: ["limit reached", "warn"],
};
const whenAt = (ts) => `in ${fmtIn(ts)} (${new Date(ts).toLocaleString("en-GB", { weekday: "short", hour: "2-digit", minute: "2-digit" })})`;

export async function render(ctx) {
	const { root, session } = ctx;
	const head = pageHead({
		title: "My models",
		desc: "The models you may ask for, how much of each you may use, and the keys that reach them. Everything is served under a neoproxy/ name; which provider answers is decided for you.",
		actions: [btn("Connect a tool", { icon: "plug", cls: "outline", href: "#/connect" })],
	});
	head.classList.add("ax-head");
	root.append(head);
	const host = h("div", { class: "my" });
	root.append(host, spinner());
	let user, vms, models, vault, usage, requests, specs, sims;
	try {
		[user, vms, models, vault, usage, requests] = await Promise.all([neo.user(session.userId), neo.virtualModels(), neo.models(), neo.vault(), neo.usage(), neo.requests({ limit: 10 })]);
		const mineVms = user.grants.map((g) => vms.find((v) => v.id === g.virtualModel)).filter(Boolean);
		[specs, sims] = await Promise.all([
			loadVmSpecs(mineVms),
			Promise.all(mineVms.map((vm) => neo.simulate({ asked: vm.served, userId: user.id }).catch((e) => ({ error: e })))),
		]);
		sims = new Map(mineVms.map((vm, i) => [vm.id, sims[i]]));
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	const modelOf = (stage) => models.find((m) => m.id === stage.model);
	const displayOf = (id) => models.find((m) => m.id === id)?.display ?? id.split("/").slice(1).join("/") ?? id;
	function modelLine(stage) {
		const m = modelOf(stage);
		const provider = vault.providers.find((p) => p.id === (m?.providerId ?? stage.model.split("/")[0]));
		const chips = propChips(stage, specs.get(stage.model));
		return h("div", { class: "my-model" },
			h("div", { class: "vm-row-name" }, providerDot(provider, 9), h("b", null, m?.display ?? "Unavailable model"), h("span", { class: "ink-3" }, providerLabel(provider).main)),
			chips.length ? h("div", { class: "vm-row-chips" }, chips) : null,
			propNotes(stage, specs.get(stage.model)));
	}

	// The routing facts behind a card, from the server's simulation. Each account
	// serves this person while that account's own usage is below its budget
	// (`candidates[].used` / `.budget`, already scaled by their share); the whole
	// stage stops once the accounts' average reaches `cap`. The two are never mixed:
	// "left" is a per-account budget minus that same account's usage.
	function facts(vm, g, sim) {
		const steps = (sim.trail ?? []).filter((t) => t.kind === "stage");
		const step = steps.find((t) => t.index === sim.stageIndex) ?? steps.at(-1);
		const stage = vm.stages[step?.index ?? 0];
		const win = WINDOW_WORDS[stage?.window] ?? stage?.window;
		const live = (step?.candidates ?? []).filter((c) => c.metered);
		let best = live.length ? Math.max(...live.map((c) => Math.max(0, c.budget - c.used))) : null;
		// With one account its usage is also the average, so the stage cap applies to it directly.
		if (live.length === 1) best = Math.min(best, Math.max(0, step.cap - live[0].used));
		const skipped = steps.filter((t) => t.skipped && t.index < (sim.stageIndex ?? Infinity));
		return { step, stage, win, best, metered: (step?.metered ?? 0) > 0, skipped, next: vm.stages[(step?.index ?? 0) + 1] };
	}

	function lead(vm, g, sim) {
		if (sim.error) return h("span", null, "Couldn't check your allowance right now.");
		if (g.allotment <= 0) return h("span", null, h("b", null, "Not available"), ": your share of this model is 0%.");
		const f = facts(vm, g, sim);
		const reset = sim.resetsAt ? `resets ${whenAt(sim.resetsAt)}` : "reset time unavailable";
		if (sim.outcome === "limited") return h("span", null, h("b", { class: "my-warn" }, "Limit reached"), ` · ${reset}. `, sim.message ? `${sim.message}.` : "");
		if (sim.outcome === "queued") return h("span", null, h("b", null, "Requests wait"), ` · they go through when the limit ${sim.resetsAt ? `resets ${whenAt(sim.resetsAt)}` : "resets (time unavailable)"}.`);
		if (sim.outcome !== "ok") return h("span", { class: "pl-bad" }, sim.message || "Not available right now.");
		const name = displayOf(sim.model);
		const then = f.next ? ` before ${displayOf(f.next.model)} takes over` : "";
		const prefix = f.skipped.length
			? [h("b", null, `Available on ${name}`), ` · ${displayOf(vm.stages[f.skipped[0].index].model)} is at your limit (reset time unavailable)`]
			: [h("b", null, "Available now")];
		if (f.metered && f.best !== null) return h("span", null, ...prefix, ` · the best account has `, h("b", null, pct(f.best)), ` of its ${f.win} usage left for you${then}.`);
		return h("span", null, ...prefix, f.skipped.length ? "." : ` · no ${f.win} limit is counted on its accounts yet${then ? `; if it's busy, ${displayOf(f.next.model)} takes over` : ""}.`);
	}

	function details(vm, g, sim) {
		const f = facts(vm, g, sim);
		const share = g.allotment;
		const stage = f.stage;
		const name = displayOf(f.step?.model ?? stage?.model ?? "");
		const settings = propDetails(vm.stages.map((s) => ({ name: displayOf(s.model), stage: s, specs: specs.get(s.model) })), { bare: true });
		return h("details", { class: "prop-more my-details" },
			h("summary", null, icon("chevronRight", 13), h("span", null, "Details")),
			h("div", { class: "my-details-body" },
				stage ? h("p", null, `Each account serves you until it is ${pct(stage.perAccount * share)} used`, share < 1 ? ` (${pct(stage.perAccount)} × your ${pct(share)} share)` : "", `; ${name} stops for you once its accounts average ${pct(stage.cap * share)}`, share < 1 ? ` (${pct(stage.cap)} × ${pct(share)})` : "", `. `,
					stage.per === "all" ? "Usage counts everyone's requests on those accounts." : "Only your own requests count.") : null,
				stage ? h("p", null, f.metered || sim.outcome !== "ok"
					? (sim.resetsAt ? `Next reset ${whenAt(sim.resetsAt)}.` : "Reset time unavailable: the accounts' reset times aren't shared with members.")
					: `Its accounts don't report a ${f.win} reading, so nothing here limits you; only the provider's own limit can.`) : null,
				settings));
	}

	// ----- granted virtual models -----
	const grid = h("div", { class: "vm-cards my-cards" });
	if (!user.grants.length) host.append(card({ body: empty({ icon: "route", title: "Nothing granted yet", hint: "Your admin has not given you a virtual model yet. Ask for one, then come back here." }) }));
	for (const g of user.grants) {
		const vm = vms.find((v) => v.id === g.virtualModel);
		if (!vm) continue;
		const sim = sims.get(vm.id);
		const [state, kind] = sim.error ? ["unknown", ""] : OUTCOME[sim.outcome] ?? ["unavailable", "bad"];
		const [first, ...rest] = vm.stages;
		const at = sim.error ? -1 : sim.stageIndex ?? -1;
		const chain = rest.length ? h("div", { class: "my-chain" }, "Then ", rest.map((s, i) => [i ? " → " : "", at === i + 1 ? h("b", null, displayOf(s.model), " (answers now)") : displayOf(s.model)])) : null;
		grid.append(card({
			cls: "vm-card my-card",
			title: h("span", { class: "row" }, servedName(vm.served, { virtual: true }), badge(state, kind)),
			sub: vm.description || null,
			actions: [btn("Use it", { icon: "arrowRight", cls: "outline sm", href: connectHref(vm.served), title: `Connect a tool to ${vm.served}` })],
			body: [
				h("p", { class: "my-lead" }, lead(vm, g, sim)),
				first ? modelLine(first) : null,
				chain,
				details(vm, g, sim),
			],
		}));
	}
	if (grid.childElementCount) host.append(grid);

	// ----- direct provider access -----
	// One compact row per model: its name and Use (opens Connect on it). The exact
	// callable name, with a labelled Copy, opens under the name on demand.
	const callable = (m) => m.served ?? `neoproxy/${m.id}`;
	const directRow = (m) => h("div", { class: "my-dm", role: "listitem" },
		h("details", { class: "my-dm-d" },
			h("summary", null, icon("chevronRight", 12), h("span", { class: "my-dm-name" }, m.display)),
			h("div", { class: "my-dm-id" }, h("code", null, callable(m)),
				btn("Copy name", { icon: "copy", cls: "ghost sm", onClick: async () => { const ok = await copyText(callable(m)); toast(ok ? `Copied ${callable(m)}` : "Clipboard unavailable", ok ? "ok" : "warn"); } }))),
		btn("Use", { icon: "arrowRight", cls: "outline sm", href: connectHref(callable(m)), title: `Connect a tool to ${callable(m)}` }));
	const SHOWN = 6;
	if (user.providerAccess.length) {
		host.append(card({
			title: "Direct provider access",
			sub: "You may also call these providers' models by name. Open a model for its exact name. Direct calls have no fallbacks.",
			body: h("div", { class: "direct" }, user.providerAccess.map((pid) => {
				const p = vault.providers.find((x) => x.id === pid);
				const ms = directModelsFor(user, models, pid);
				const total = models.filter((m) => m.providerId === pid && m.exposed && !m.virtualOnly).length;
				const listHost = h("div", { class: "my-dms", role: "list", "aria-label": `${providerLabel(p).main} models` });
				let open = false, query = "";
				const paint = () => {
					const hits = ms.filter((m) => !query || `${m.display}\n${callable(m)}`.toLowerCase().includes(query));
					const shown = open || query ? hits : hits.slice(0, SHOWN);
					listHost.replaceChildren(...shown.map(directRow),
						hits.length ? "" : h("p", { class: "ax-fact" }, "No model matches."),
						!query && ms.length > SHOWN ? h("button", { type: "button", class: "my-more", "aria-expanded": String(open), onClick: () => { open = !open; paint(); listHost.querySelector(".my-more")?.focus(); } },
							icon(open ? "chevronUp" : "chevronDown", 13), open ? "Show fewer" : `Show all ${ms.length} models`) : "");
				};
				paint();
				return h("div", { class: "direct-row on" },
					h("div", { class: "direct-head" }, providerDot(p, 9), h("span", null, providerLabel(p).main), h("span", { class: "fact direct-count" }, user.modelAccess?.[pid] ? `${ms.length} of ${plural(total, "model")}` : plural(ms.length, "model"))),
					ms.length > 8 ? h("div", { class: "my-dm-find" }, search(`Find a ${providerLabel(p).main} model`, (v) => { query = v.trim().toLowerCase(); paint(); }, { "aria-label": `Find a ${providerLabel(p).main} model` })) : null,
					ms.length ? listHost : null);
			})),
		}));
	}

	// ----- keys -----
	const keysHost = h("div");
	async function revoke(k) {
		if (!(await confirm({ title: `Revoke ${k.label || k.prefix}?`, text: "Anything using this key stops working now. This cannot be undone.", confirmLabel: "Revoke key", danger: true }))) return;
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
	host.append(card({
		title: "My keys",
		sub: "A key reaches what you may use and nothing more. Give each tool its own key, with an expiry if it is temporary.",
		actions: [btn("Mint a key", { icon: "key", cls: "primary sm", onClick: () => mintKeyDialog({ user, accounts: user.role === "owner" ? drawableAccounts(user, vault.accounts) : [], providers: vault.providers, models: directModelsFor(user, models), onMinted: (key) => { user.keys.push(key); renderKeys(); } }) })],
		flush: true,
		body: keysHost,
	}));

	// ----- recent -----
	host.append(card({
		title: "My recent requests",
		flush: true,
		body: requests.length
			? table([
				{ key: "t", label: "Time", render: (r) => h("span", { class: "mono ink-3" }, fmtTime(r.at)) },
				{ key: "asked", label: "Asked for", render: (r) => servedName(r.asked, { virtual: r.asked.split("/").length === 2 }) },
				{ key: "tok", label: "Tokens", num: true, render: (r) => (r.outcome === "ok" ? fmtCompact(r.tokens.in + r.tokens.out) : "–") },
				{ key: "out", label: "Outcome", render: (r) => outcomeBadge(r.outcome) },
			], requests, { stack: true })
			: h("div", { class: "ak-none" }, "Nothing yet. Requests made with your keys show here."),
	}));

	// ----- password -----
	host.append(card({
		cls: "my-pass",
		title: "Dashboard password",
		sub: "Separate from your keys. Changing it signs you out everywhere and revokes your keys.",
		actions: [btn("Change password", { cls: "outline sm", onClick: () => {
			let currentPassword = "", password = "";
			const err = h("div", { class: "error", role: "alert" });
			dialog({ title: "Change password", sub: "Every dashboard session and every key of yours is revoked. Sign in again and mint new keys after saving.", body: [
				field("Current password", input({ type: "password", autocomplete: "current-password", onInput: (e) => { currentPassword = e.target.value; } })),
				field("New password", input({ type: "password", autocomplete: "new-password", minlength: 12, onInput: (e) => { password = e.target.value; } }), "12 to 72 characters."), err,
			], actions: (close) => [btn("Cancel", { onClick: close }), btn("Save password", { cls: "primary", onClick: async () => {
				try { await engine.post(`/v8/neo/users/${encodeURIComponent(user.id)}/password`, { currentPassword, password }); close(); toast("Password changed. Sign in again."); ctx.rerender(); }
				catch (e) { err.textContent = e.message; }
			} })] });
		} })],
	}));
}
