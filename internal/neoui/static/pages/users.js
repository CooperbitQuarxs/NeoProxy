// Users: who may ask the proxy for what. A user's page is their catalogue
// (the virtual models they are granted, with an allotment each, and any direct
// provider access), their limits, their keys, and what they did recently.

import { h, icon, fmtPct, fmtAgo, fmtTime, fmtCompact, plural, clear, copyText } from "../lib/dom.js";
import { neo, sources, onChange, ApiError } from "../lib/client.js";
import { pageHead, card, btn, badge, avatar, status, servedName, ring, ringRow, field, input, select, seg, toggle, dialog, confirm, toast, empty, spinner, errorState, outcomeBadge, kv, table } from "../lib/components.js";

export async function render(ctx) {
	if (ctx.params[0]) return detail(ctx, ctx.params[0]);
	return list(ctx);
}

const roleBadge = (u) => badge(u.role, u.role === "admin" ? "accent" : u.role === "service" ? "violet" : "");
const stateStatus = (u) => (u.state === "active" ? status("ok", "Active") : u.state === "invited" ? status("warn", "Invited") : status("bad", "Suspended"));

async function list(ctx) {
	const { root } = ctx;
	root.append(
		pageHead({
			title: "Users",
			desc: "People and services that sign in with a NeoProxy key. A member only ever sees neoproxy/… names; direct provider access is a deliberate grant.",
			preview: sources.users === "fixture",
			actions: [btn("Invite user", { icon: "plus", cls: "primary", onClick: () => invite(ctx) })],
		}),
	);
	const host = h("div");
	root.append(host, spinner());
	let users, vms, usage, requests;
	try {
		[users, vms, usage, requests] = await Promise.all([neo.users(), neo.virtualModels(), neo.usage(), neo.requests({ limit: 400 })]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();
	const drawn = (u) => {
		let n = 0;
		for (const vm of Object.values(usage[u.id] ?? {})) for (const st of Object.values(vm)) for (const v of Object.values(st)) n += v;
		return n;
	};
	host.append(
		card({
			flush: true,
			body: table(
				[
					{ key: "who", label: "User", render: (u) => h("div", { class: "row" }, avatar(u), h("div", null, h("div", { class: "cell-main" }, u.handle), h("div", { class: "cell-sub" }, u.name))), sortBy: (u) => u.handle },
					{ key: "role", label: "Role", render: roleBadge, sortBy: (u) => u.role },
					{ key: "state", label: "State", render: stateStatus, sortBy: (u) => u.state },
					{ key: "models", label: "Sees", wrap: true, render: (u) => h("span", { class: "row wrap", style: { gap: "4px" } }, u.grants.map((g) => servedName(`neoproxy/${vms.find((v) => v.id === g.virtualModel)?.name ?? "?"}`, { virtual: true })), u.providerAccess.length ? badge(`+${u.providerAccess.length} direct`) : null) },
					{ key: "drawn", label: "Drawn", num: true, render: (u) => fmtPct(drawn(u), 1), sortBy: drawn },
					{ key: "limits", label: "Limits", render: (u) => h("span", { class: "ink-2" }, u.limits.rpm ? `${u.limits.rpm} rpm` : "no rpm limit", u.limits.tokensPerDay ? ` · ${fmtCompact(u.limits.tokensPerDay)} tok/day` : "") },
					{ key: "keys", label: "Keys", num: true, render: (u) => String(u.keys.length), sortBy: (u) => u.keys.length },
					{ key: "last", label: "Last request", num: true, render: (u) => fmtAgo(requests.find((r) => r.userId === u.id)?.at), sortBy: (u) => requests.find((r) => r.userId === u.id)?.at ?? 0 },
				],
				users,
				{ onRow: (u) => ctx.navigate(`users/${u.id}`), sort: { key: "last", dir: "desc" } },
			),
		}),
	);
	const off = onChange((w) => w === "users" && ctx.rerender());
	return () => off();
}

function invite(ctx) {
	const draft = { handle: "", name: "", role: "member", state: "invited", grants: [], providerAccess: [], limits: { rpm: 60, tokensPerDay: 2_000_000 }, keys: [] };
	const err = h("div", { class: "error hidden" });
	dialog({
		title: "Invite a user",
		sub: "They get an invited account with no key. Issue a key from their page when they are ready.",
		body: [
			field("Handle", input({ mono: true, placeholder: "lena", onInput: (e) => (draft.handle = e.target.value.trim().toLowerCase()) }), "Lowercase, what appears in logs."),
			field("Name", input({ placeholder: "Lena", onInput: (e) => (draft.name = e.target.value) })),
			field(
				"Role",
				seg(
					[
						{ value: "member", label: "Member" },
						{ value: "service", label: "Service" },
						{ value: "admin", label: "Admin" },
					],
					draft.role,
					(v) => (draft.role = v),
				),
				"Services are machines (NeoDeck, CI); admins see this dashboard.",
			),
			err,
		],
		actions: (close) => [
			btn("Cancel", { onClick: close }),
			btn("Invite", {
				cls: "primary",
				onClick: async () => {
					try {
						const u = await neo.saveUser(draft);
						close();
						toast(`Invited ${u.handle}`);
						ctx.navigate(`users/${u.id}`);
					} catch (e) {
						err.textContent = e.message;
						err.classList.remove("hidden");
					}
				},
			}),
		],
	});
}

async function detail(ctx, id) {
	const { root } = ctx;
	root.append(spinner());
	let user, vms, vault, models, usage, requests;
	try {
		[user, vms, vault, models, usage, requests] = await Promise.all([neo.user(id), neo.virtualModels(), neo.vault(), neo.models(), neo.usage(), neo.requests({ userId: id, limit: 12 })]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.navigate("users")));
		return;
	}
	root.lastChild.remove();
	const draft = structuredClone(user);
	let dirty = false;
	const saveBtn = btn("Saved", { icon: "save", cls: "primary", disabled: true, onClick: save });
	const setDirty = (v = true) => {
		dirty = v;
		saveBtn.replaceChildren(icon("save", 15), v ? "Save changes" : "Saved");
		saveBtn.disabled = !v;
	};
	async function save() {
		try {
			const saved = await neo.saveUser(draft);
			Object.assign(user, saved);
			toast(`Saved ${user.handle}`);
			setDirty(false);
		} catch (e) {
			toast(e instanceof ApiError ? e.message : "Could not save", "bad");
		}
	}
	const suspendBtn = btn(user.state === "suspended" ? "Reinstate" : "Suspend", {
		icon: user.state === "suspended" ? "check" : "stop",
		cls: user.state === "suspended" ? "outline" : "danger",
		onClick: async () => {
			const next = user.state === "suspended" ? "active" : "suspended";
			if (next === "suspended" && !(await confirm({ title: `Suspend ${user.handle}?`, text: "Every request with their keys fails immediately. Keys are kept; reinstating restores them.", confirmLabel: "Suspend", danger: true }))) return;
			draft.state = next;
			await save();
			ctx.rerender();
		},
	});
	root.append(
		pageHead({
			back: { href: "#/users", label: "Users" },
			title: h("span", { class: "row" }, avatar(user), user.handle, roleBadge(user), stateStatus(user)),
			desc: `${user.name} · joined ${fmtAgo(user.createdAt)}`,
			preview: sources.users === "fixture",
			actions: [suspendBtn, saveBtn],
		}),
	);

	// ----- catalogue -----
	const catHost = h("div", { class: "list" });
	const drawnFor = (vmId) => {
		let n = 0;
		for (const st of Object.values(usage[user.id]?.[vmId] ?? {})) for (const v of Object.values(st)) n += v;
		return n;
	};
	function renderCatalogue() {
		clear(catHost);
		if (!draft.grants.length) catHost.append(h("div", { class: "ink-3", style: { padding: "12px 16px" } }, "No virtual models yet. This user cannot ask for anything until one is granted."));
		for (const g of draft.grants) {
			const vm = vms.find((v) => v.id === g.virtualModel);
			if (!vm) continue;
			const pct = Math.round(g.allotment * 100);
			catHost.append(
				h(
					"div",
					{ class: "li" },
					ring({ used: g.allotment ? drawnFor(vm.id) / (g.allotment * vm.stages.reduce((n, s) => n + s.cap, 0) || 1) : 0, size: 36, title: "Share of this user's total allotment drawn this window" }),
					h("div", { class: "lt" }, h("div", { class: "main" }, servedName(vm.served, { virtual: true })), h("div", { class: "sub" }, vm.description)),
					h(
						"div",
						{ class: "row", style: { width: "260px" } },
						h("input", {
							type: "range",
							min: 0,
							max: 100,
							step: 5,
							value: pct,
							"aria-label": `Allotment for ${vm.name}`,
							style: { flex: 1, accentColor: "var(--accent)" },
							onInput: (e) => {
								g.allotment = Number(e.target.value) / 100;
								e.target.nextSibling.textContent = `${e.target.value}%`;
								setDirty();
							},
						}),
						h("span", { class: "num", style: { width: "44px" } }, `${pct}%`),
					),
					btn(null, {
						icon: "x",
						cls: "icon sm",
						title: `Revoke ${vm.name}`,
						onClick: () => {
							draft.grants = draft.grants.filter((x) => x !== g);
							setDirty();
							renderCatalogue();
						},
					}),
				),
			);
		}
		const remaining = vms.filter((v) => !draft.grants.some((g) => g.virtualModel === v.id));
		if (remaining.length) {
			catHost.append(
				h(
					"div",
					{ class: "li", style: { background: "var(--panel)" } },
					h("div", { class: "lt" }, h("div", { class: "sub" }, "Grant another")),
					h("div", { class: "row wrap" }, remaining.map((v) => btn(v.name, { icon: "plus", cls: "sm outline", onClick: () => { draft.grants.push({ virtualModel: v.id, allotment: 1 }); setDirty(); renderCatalogue(); } }))),
				),
			);
		}
	}
	renderCatalogue();
	root.append(card({ title: "Catalogue", sub: "What this user sees when they list models. The allotment scales every budget in that chain: 50% means half the sips and half the cap.", flush: true, body: catHost }));

	// ----- direct access -----
	root.append(
		card({
			title: "Direct provider access",
			sub: "Off for members by design: they reach providers only through virtual models. Turn a provider on to let this user ask for neoproxy/<provider>/<model> directly, with no budget but the account's own window.",
			body: h(
				"div",
				{ class: "row wrap", style: { paddingTop: "4px", gap: "10px 18px" } },
				vault.providers.map((p) =>
					toggle(draft.providerAccess.includes(p.id), (v) => {
						draft.providerAccess = v ? [...draft.providerAccess, p.id] : draft.providerAccess.filter((x) => x !== p.id);
						setDirty();
					}, p.name, { disabled: !p.exposed }),
				),
			),
		}),
	);

	// ----- limits + keys -----
	const keysHost = h("div", { class: "list" });
	function renderKeys() {
		clear(keysHost);
		if (!user.keys.length) keysHost.append(h("div", { class: "ink-3", style: { padding: "12px 16px" } }, "No keys. Issue one and hand it over; it is shown once."));
		for (const k of user.keys) {
			keysHost.append(
				h(
					"div",
					{ class: "li" },
					icon("key", 15),
					h("div", { class: "lt" }, h("div", { class: "main" }, h("code", null, k.prefix), " ", h("span", { class: "ink-2", style: { fontWeight: 400 } }, k.label)), h("div", { class: "sub" }, `issued ${fmtAgo(k.createdAt)} · ${k.lastUsedAt ? `last used ${fmtAgo(k.lastUsedAt)}` : "never used"}`)),
					btn("Revoke", {
						cls: "danger sm",
						onClick: async () => {
							if (!(await confirm({ title: `Revoke ${k.prefix}?`, text: `Anything using "${k.label}" stops working now.`, confirmLabel: "Revoke", danger: true }))) return;
							await neo.revokeKey(user.id, k.id);
							user.keys = user.keys.filter((x) => x.id !== k.id);
							toast("Key revoked");
							renderKeys();
						},
					}),
				),
			);
		}
	}
	renderKeys();
	root.append(
		h(
			"div",
			{ class: "grid c1-2" },
			card({
				title: "Limits",
				sub: "Hard ceilings on top of allotments.",
				body: [
					field("Requests per minute", input({ type: "number", min: 0, value: draft.limits.rpm ?? "", placeholder: "no limit", onInput: (e) => { draft.limits.rpm = e.target.value ? Number(e.target.value) : null; setDirty(); } })),
					field("Tokens per day", input({ type: "number", min: 0, step: 100000, value: draft.limits.tokensPerDay ?? "", placeholder: "no limit", onInput: (e) => { draft.limits.tokensPerDay = e.target.value ? Number(e.target.value) : null; setDirty(); } }), "Input and output together."),
				],
			}),
			card({
				title: "Keys",
				sub: "Bearer tokens this user sends. Each key is a separate line in Activity.",
				actions: [btn("Issue key", { icon: "plus", cls: "outline sm", onClick: () => issueKey(user, renderKeys) })],
				flush: true,
				body: keysHost,
			}),
		),
	);

	// ----- recent -----
	root.append(
		card({
			title: "Recent requests",
			actions: [btn("All activity", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate(`activity?user=${user.id}`) })],
			flush: true,
			body: requests.length
				? table(
						[
							{ key: "t", label: "Time", render: (r) => h("span", { class: "mono ink-3" }, fmtTime(r.at)) },
							{ key: "asked", label: "Asked for", render: (r) => servedName(r.asked, { virtual: r.asked.split("/").length === 2 }) },
							{ key: "served", label: "Served by", clip: true, width: "40%", render: (r) => (r.decision.model ? h("span", null, models.find((m) => m.id === r.decision.model)?.display ?? r.decision.model, h("span", { class: "ink-3" }, r.decision.accountId ? ` · ${vault.accounts.find((a) => a.id === r.decision.accountId)?.label}` : "")) : h("span", { class: "ink-3" }, r.decision.message ?? r.decision.reason)) },
							{ key: "tok", label: "Tokens", num: true, render: (r) => (r.outcome === "ok" ? fmtCompact(r.tokens.in + r.tokens.out) : "–") },
							{ key: "out", label: "", render: (r) => outcomeBadge(r.outcome) },
						],
						requests,
						{ onRow: (r) => ctx.navigate(`activity/${r.id}`) },
					)
				: empty({ title: "Nothing yet", hint: "Requests with this user's keys will show here." }),
		}),
	);
	setDirty(false);
}

function issueKey(user, after) {
	let label = "";
	dialog({
		title: `Issue a key for ${user.handle}`,
		sub: "Name it after the thing that will use it.",
		body: field("Label", input({ placeholder: "NeoDeck on Utopia", autofocus: true, onInput: (e) => (label = e.target.value) })),
		actions: (close) => [
			btn("Cancel", { onClick: close }),
			btn("Issue", {
				cls: "primary",
				onClick: async () => {
					const { key, secret } = await neo.createKey(user.id, label || "unnamed");
					user.keys.push(key);
					close();
					after();
					dialog({
						title: "Copy the key now",
						sub: "It is shown once. NeoProxy keeps only a hash.",
						body: h("div", { class: "row" }, h("code", { style: { flex: 1, padding: "8px 10px", background: "var(--panel)", border: "1px solid var(--line-2)", borderRadius: "8px", overflowWrap: "anywhere" } }, secret), btn("Copy", { icon: "copy", cls: "outline", onClick: async () => toast((await copyText(secret)) ? "Copied" : "Clipboard unavailable", "warn") })),
						actions: (close2) => [btn("Done", { cls: "primary", onClick: close2 })],
					});
				},
			}),
		],
	});
}
