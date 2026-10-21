// Core link: the NeoPi core the proxy serves from, and the vault it holds.
// Providers can be hidden from users here; accounts show their quota windows
// and which virtual models draw from them.

import { h, icon, fmtAgo, fmtPct, fmtIn, plural } from "../lib/dom.js";
import { neo, sources, onChange } from "../lib/client.js";
import { pageHead, card, kv, badge, status, toggle, btn, ring, windowsRow, accountState, inspector, section, servedName, spinner, errorState, toast } from "../lib/components.js";

export async function render(ctx) {
	const { root } = ctx;
	root.append(
		pageHead({
			title: "Core link",
			desc: "The NeoPi core this proxy serves from. Every credential below is held by NeoPi; the proxy borrows it per request and never stores a token of its own.",
			preview: sources.core === "fixture",
		}),
	);
	const host = h("div");
	root.append(host, spinner());
	let core, vault, vms, usage, users;
	try {
		[core, vault, vms, usage, users] = await Promise.all([neo.core(), neo.vault(), neo.virtualModels(), neo.usage(), neo.users()]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	host.append(
		card({
			title: h("span", { class: "row" }, "Link", core.linked ? badge("linked", "ok") : badge("not linked", "warn")),
			sub: core.linked ? `Heartbeat ${fmtAgo(core.heartbeatAt)} · NeoPi ${core.version} on ${core.host}` : "No NeoPi core is configured. The proxy is serving from its own credential files.",
			actions: [btn("Re-link", { icon: "refresh", cls: "outline", onClick: () => toast("Re-link is handled by the core; nothing to do while linked") })],
			body: h(
				"div",
				{ class: "grid c2", style: { paddingTop: "4px" } },
				kv([
					["Socket", h("code", null, core.endpoint)],
					["Identity", h("code", null, core.identity)],
					["Scopes", h("span", { class: "row wrap" }, core.scopes.map((s) => badge(s)))],
				]),
				h(
					"p",
					{ class: "ink-2", style: { margin: 0 } },
					"The core decides which providers exist and keeps their tokens fresh. This proxy decides who may use them and how much. If the core goes away, every virtual model falls to its terminal step until it is back.",
				),
			),
		}),
	);

	const selected = { id: ctx.params[0] ?? null };
	const vaultCard = card({ title: "Vault", sub: `${plural(vault.providers.length, "provider")}, ${plural(vault.accounts.length, "account")} signed in on the core. Hiding a provider removes every one of its models from every user, including direct access.`, flush: true, body: h("div") });
	host.append(vaultCard);
	const listHost = vaultCard.querySelector(".card-body");

	const drawers = (accountId) => {
		const out = [];
		for (const vm of vms) for (const s of vm.stages) {
			const pid = s.model.split("/")[0];
			if (pid !== vault.accounts.find((a) => a.id === accountId)?.providerId) continue;
			if (s.accounts !== "all" && !s.accounts.includes(accountId)) continue;
			let used = 0;
			const per = [];
			for (const u of users) {
				const v = usage[u.id]?.[vm.id]?.[s.id]?.[accountId] ?? 0;
				if (v > 0) per.push([u.handle, v]);
				used += v;
			}
			out.push({ vm, stage: s, used, per });
		}
		return out;
	};

	const renderList = () => {
		listHost.replaceChildren();
		const tree = h("div", { class: "list" });
		for (const p of vault.providers) {
			const accs = vault.accounts.filter((a) => a.providerId === p.id);
			tree.append(
				h(
					"div",
					{ class: "li", style: { background: "var(--panel)" } },
					h("div", { class: "lt" }, h("div", { class: "main row" }, p.name, h("span", { class: "ink-3", style: { fontWeight: 400 } }, p.vendor), badge(p.kind === "oauth" ? "oauth" : "api key")), h("div", { class: "sub" }, p.exposed ? `served as neoproxy/${p.id}/… · ${plural(accs.length, "account")}` : "hidden from every user")),
					toggle(p.exposed, async (v) => {
						await neo.setProviderExposed(p.id, v);
						toast(v ? `${p.name} is served again` : `${p.name} hidden from users`);
					}, `Serve ${p.name}`),
				),
			);
			for (const a of accs) {
				const d = drawers(a.id);
				tree.append(
					h(
						"button",
						{ type: "button", class: "li clickable", "aria-selected": String(selected.id === a.id), style: { paddingLeft: "32px" }, onClick: () => open(a) },
						h("div", { class: "lt" }, h("div", { class: "main row" }, h("code", null, a.label), h("span", { class: "ink-3", style: { fontWeight: 400 } }, a.plan)), h("div", { class: "sub" }, d.length ? `drawn by ${[...new Set(d.map((x) => x.vm.name))].join(", ")}` : "no virtual model draws from this account")),
						windowsRow(a.windows),
						h("div", { style: { width: "130px", textAlign: "right" } }, accountState(a)),
					),
				);
			}
		}
		listHost.append(tree);
	};

	const open = (a) => {
		selected.id = a.id;
		renderList();
		const p = vault.providers.find((x) => x.id === a.providerId);
		const d = drawers(a.id);
		ctx.setInspector(
			inspector({
				title: `${p.name} · ${a.label}`,
				sub: `${a.plan} · ${a.state}`,
				onClose: () => {
					selected.id = null;
					renderList();
					ctx.setInspector(null);
				},
				body: [
					section(
						"Quota windows",
						a.windows.map((w) => h("div", { class: "ring-row", style: { marginBottom: "8px" } }, ring({ used: w.used, size: 40 }), h("div", { class: "ring-text" }, h("div", { class: "main" }, `${w.kind} window`), h("div", { class: "sub" }, `${fmtPct(w.used)} used · resets in ${fmtIn(w.resetsAt)}`)))),
						a.state === "cooldown" ? h("div", { class: "ink-2", style: { fontSize: "12.5px" } }, `Cooling down after a rate limit; back in ${fmtIn(a.cooldownUntil)}.`) : null,
						a.state === "expired" ? h("div", { class: "ink-2", style: { fontSize: "12.5px" } }, "The refresh token was rejected. Sign in again from NeoPi (", h("code", null, "npi auth"), ") and the core will pick it up.") : null,
					),
					section(
						"Drawn by",
						d.length
							? d.map((x) =>
									h(
										"div",
										{ style: { marginBottom: "10px" } },
										h("div", { class: "row" }, servedName(x.vm.served, { virtual: true }), h("span", { class: "ink-3" }, `stage ${x.vm.stages.indexOf(x.stage) + 1}`), h("span", { class: "spacer" }), h("span", { class: "num ink-2" }, fmtPct(x.used, 1))),
										x.per.length ? h("div", { class: "ink-3", style: { fontSize: "12px", marginTop: "2px" } }, x.per.map(([handle, v]) => `${handle} ${fmtPct(v, 1)}`).join(" · ")) : h("div", { class: "ink-3", style: { fontSize: "12px" } }, "nothing drawn yet this window"),
									),
								)
							: h("div", { class: "ink-3" }, "No virtual model includes this account. Direct-access users can still reach it."),
					),
					section("Account", kv([["Provider", p.name], ["Kind", p.kind === "oauth" ? "OAuth login held by NeoPi" : "API key held by NeoPi"], ["Id", h("code", null, a.id)]])),
				],
				foot: [btn("Open in Virtual models", { icon: "arrowRight", cls: "outline", onClick: () => ctx.navigate("virtual-models") })],
			}),
		);
	};

	renderList();
	if (selected.id) {
		const a = vault.accounts.find((x) => x.id === selected.id);
		if (a) open(a);
	}

	host.append(
		card({
			title: "Local credential files",
			sub: "The engine's own auth directory. Kept for proxies that run without a core; demoted while one is linked.",
			actions: [btn("Credentials", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate("credentials") })],
			body: h("p", { class: "ink-2", style: { margin: "4px 0 0" } }, "Files under ", h("code", null, "auths/"), " are still watched and still work, but a linked core takes precedence for any provider it holds."),
		}),
	);

	const off = onChange((what) => what === "vault" && renderList());
	return () => off();
}
