// Users: who may ask the proxy for what. A person's page says, in order: which
// virtual models they may use and how much of each one's thresholds (their
// allotment), which providers they may call directly, their limits, their keys,
// and the sign-ins they contribute to the pool.

import { h, icon, fmtAgo, fmtIn, fmtTime, fmtDate, fmtCompact, plural, clear, copyText } from "../lib/dom.js";
import { neo, onChange, ApiError } from "../lib/client.js";
import { pageHead, card, btn, badge, avatar, status, servedName, providerLabel, providerDot, search, field, input, select, seg, toggle, dialog, confirm, toast, empty, spinner, errorState, outcomeBadge, table, healthStatus } from "../lib/components.js";

export async function render(ctx) {
	if (ctx.params[0]) return detail(ctx, ctx.params[0]);
	return list(ctx);
}

const roleBadge = (u) => badge(u.role, u.role === "admin" ? "accent" : u.role === "service" ? "violet" : "");
const stateStatus = (u) => (u.state === "active" ? status("ok", "Active") : u.state === "invited" ? status("warn", "Invited") : status("bad", "Suspended"));

// ---------- shared by Users, My models and Access keys ----------

const pct = (x) => `${+(x * 100).toFixed(1)}%`;

// The allotment, in words. `who` is the grammatical subject ("shayna", "You"),
// `them` the object ("them", "you"). The worked example uses a real stage of the
// virtual model when it has one, so the number is not abstract.
export function allotmentSentence({ who, them = "them", allotment, vm }) {
	const stage = vm?.stages?.find((s) => s.cap > 0);
	const example = stage?.cap ?? 0.5;
	if (allotment >= 1) return h("span", null, who, " may use every stage until ", h("b", null, "100%"), " of its thresholds.");
	if (allotment <= 0) return h("span", null, who, " may use every stage until ", h("b", null, "0%"), " of each threshold: nothing is available to ", them, ".");
	return h("span", null, who, " may use every stage until ", h("b", null, pct(allotment)), " of each threshold: a stage set to ", pct(example), " stops at ", pct(example * allotment), " for ", them, ".");
}

// What a key's expiry looks like: quiet when never, plain when future, red once past.
export function expiryCell(key, now = Date.now()) {
	if (!key.expiresAt) return h("span", { class: "ink-3" }, "never");
	const past = key.expiresAt <= now;
	const soon = !past && key.expiresAt - now < 7 * 864e5;
	return h("span", { class: `key-exp ${past ? "bad" : soon ? "warn" : ""}`, title: fmtDate(key.expiresAt) },
		past ? `expired ${fmtAgo(key.expiresAt, now)}` : `in ${fmtIn(key.expiresAt, now).replace(/ \d+h$/, "")}`,
		h("span", { class: "cell-sub" }, new Date(key.expiresAt).toLocaleDateString("en-GB", { day: "2-digit", month: "short", year: "numeric" })));
}

function scopeCell(key, accounts, providers) {
	if (!key.accountIds?.length) return h("span", { class: "ink-2" }, "every account this person may use");
	return h("span", { class: "key-scope" }, key.accountIds.map((id) => {
		const a = accounts.find((x) => x.id === id);
		const p = a && providers.find((x) => x.id === a.providerId);
		return h("span", { class: "tag", title: id }, p ? providerDot(p, 7) : null, a ? `${providerLabel(p).main} · ${a.label}` : "an account");
	}));
}

// A person's keys as a table. `onRevoke` is omitted when the viewer may not revoke;
// `whoOf` adds a Person column and `onOpen` makes rows open that person.
export function keysTable({ keys, accounts = [], providers = [], onRevoke, scoped = true, emptyHint, whoOf, onOpen }) {
	const columns = [
		whoOf ? { key: "who", label: "Person", render: (k) => h("span", { class: "row" }, avatar(whoOf(k)), h("span", { class: "cell-main" }, whoOf(k).handle)) } : null,
		{ key: "key", label: "Key", render: (k) => h("div", null, h("code", { class: "cell-main" }, k.prefix), h("div", { class: "cell-sub" }, k.label)) },
		scoped ? { key: "scope", label: "Scope", wrap: true, render: (k) => scopeCell(k, accounts, providers) } : null,
		{ key: "created", label: "Created", render: (k) => h("span", { class: "ink-2" }, fmtAgo(k.createdAt)) },
		{ key: "last", label: "Last used", render: (k) => (k.lastUsedAt ? h("span", { class: "ink-2" }, fmtAgo(k.lastUsedAt)) : h("span", { class: "ink-3" }, "never")) },
		{ key: "expires", label: "Expires", render: (k) => expiryCell(k) },
		onRevoke ? { key: "x", label: "", width: "84px", render: (k) => btn("Revoke", { cls: "danger sm", onClick: () => onRevoke(k) }) } : null,
	].filter(Boolean);
	return table(columns, keys, { onRow: onOpen, emptyState: h("div", { class: "ink-3", style: { padding: "14px 16px" } }, emptyHint ?? "No keys yet.") });
}

// Accounts a person's keys can draw from. Owners only reach what they contribute;
// everyone else reaches any sign-in that has a contributor.
export function drawableAccounts(user, accounts) {
	return accounts.filter((a) => a.ownerId && (user.role !== "owner" || a.ownerId === user.id));
}

// Mint a key: label, optional expiry, optional account scope. Calls
// `onMinted(key)` once minted, then shows the secret once.
export function mintKeyDialog({ user, accounts = [], providers = [], onMinted }) {
	const ownerRole = user.role === "owner";
	let label = "", expiry = "never", date = "", limited = ownerRole;
	const scope = new Set();
	const err = h("div", { class: "error", role: "alert" });
	const when = h("div", { class: "hint" });
	const dateInput = input({ type: "date", min: new Date().toISOString().slice(0, 10), onInput: (e) => { date = e.target.value; showWhen(); } });
	const dateRow = h("div", { class: "hidden" }, dateInput);
	const expiresAt = () => {
		if (expiry === "never") return undefined;
		if (expiry === "date") return date ? new Date(`${date}T23:59:59`).getTime() : NaN;
		return Date.now() + Number(expiry) * 864e5;
	};
	function showWhen() {
		dateRow.classList.toggle("hidden", expiry !== "date");
		const at = expiresAt();
		when.textContent = at === undefined ? "The key works until you revoke it." : Number.isNaN(at) ? "Pick the last day the key should work." : `The key stops working on ${fmtDate(at)}.`;
	}
	showWhen();

	const groups = h("div", { class: "scope-groups" });
	function renderScopes(value = "") {
		const q = value.trim().toLowerCase();
		clear(groups);
		for (const provider of providers) {
			const lab = providerLabel(provider);
			const rows = accounts.filter((a) => a.providerId === provider.id && (!q || [a.label, lab.main, lab.source].some((t) => t?.toLowerCase().includes(q))));
			if (!rows.length) continue;
			groups.append(h("div", { class: "scope-group" },
				h("div", { class: "scope-prov", title: provider.id }, providerDot(provider, 8), [lab.main, lab.source].filter(Boolean).join(" · ")),
				rows.map((a) => {
					const t = toggle(scope.has(a.id), (on) => (on ? scope.add(a.id) : scope.delete(a.id)), a.label);
					t.title = a.id;
					return t;
				}),
			));
		}
		if (!groups.childElementCount) groups.append(h("p", { class: "ink-3" }, q ? "No accounts match." : "This person cannot draw from any account yet."));
	}
	renderScopes();
	const picker = h("div", { class: limited ? "" : "hidden" }, accounts.length > 8 ? search("Filter accounts by provider or label", renderScopes, { "aria-label": "Filter accounts" }) : null, groups);
	const scopeField = accounts.length
		? field("Accounts", h("div", { class: "scope-field" },
			ownerRole ? null : seg([{ value: "all", label: "Every account they may use" }, { value: "some", label: "Only these accounts" }], limited ? "some" : "all", (v) => { limited = v === "some"; picker.classList.toggle("hidden", !limited); }),
			picker),
		ownerRole ? "An owner's key must name at least one account they contribute." : "A scope can only narrow what the person may draw from, never widen it.")
		: null;

	dialog({
		title: `Mint a key for ${user.handle}`,
		sub: "A key is only as powerful as the person: it reaches the virtual models and direct providers granted above. It never signs in to the dashboard.",
		body: [
			field("Label", input({ placeholder: "What will use this key, e.g. Voyager on the laptop", autofocus: true, onInput: (e) => (label = e.target.value) })),
			field("Expires", h("div", { class: "scope-field" },
				seg([{ value: "never", label: "Never" }, { value: "30", label: "30 days" }, { value: "90", label: "90 days" }, { value: "date", label: "On a date" }], expiry, (v) => { expiry = v; showWhen(); }),
				dateRow, when)),
			scopeField,
			err,
		],
		actions: (close) => [
			btn("Cancel", { onClick: close }),
			btn("Mint key", { icon: "key", cls: "primary", onClick: async (event) => {
				err.textContent = "";
				const at = expiresAt();
				if (Number.isNaN(at) || (at !== undefined && at <= Date.now())) { err.textContent = "Choose a day in the future for the key to expire."; return; }
				if (limited && !scope.size) { err.textContent = "Pick at least one account, or switch to every account they may use."; return; }
				const button = event.currentTarget;
				button.disabled = true;
				try {
					const { key, secret } = await neo.createKey(user.id, label.trim() || "unnamed", limited ? [...scope] : undefined, at);
					close();
					onMinted?.(key);
					revealSecret(key, secret);
				} catch (e) { err.textContent = e.message; button.disabled = false; }
			} }),
		],
	});
}

function revealSecret(key, secret) {
	dialog({
		title: "Copy the key now",
		sub: "This is the only time it is shown. NeoProxy keeps a hash, not the key.",
		body: [
			h("div", { class: "secret-line" }, h("code", null, secret), btn("Copy", { icon: "copy", cls: "outline", onClick: async () => { const ok = await copyText(secret); toast(ok ? "Copied" : "Clipboard unavailable", ok ? "ok" : "warn"); } })),
			h("div", { class: "hint", style: { marginTop: "8px" } }, key.expiresAt ? `It stops working on ${fmtDate(key.expiresAt)}.` : "It works until it is revoked."),
		],
		actions: (close) => [btn("Done", { cls: "primary", onClick: close })],
	});
}

// ---------- the list ----------

async function list(ctx) {
	const { root } = ctx;
	root.append(
		pageHead({
			title: "Users",
			desc: "People sign in with a username and password; services use keys. What each person may use is a grant on a virtual model, or direct access to a provider.",
			actions: [btn("Create user", { icon: "plus", cls: "primary", onClick: () => invite(ctx) })],
		}),
	);
	const host = h("div");
	root.append(host, spinner());
	let users, vms, usage, requests;
	try {
		[users, vms, usage, requests] = await Promise.all([neo.users(), neo.virtualModels(), neo.usage(), neo.requests({ limit: 200 })]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();
	// Usage is a share of each account's local meter. The honest summary of "how
	// much has this person drawn" is the busiest meter, not a sum of unlike windows.
	const drawn = (u) => {
		let max = 0;
		for (const vm of Object.values(usage[u.id] ?? {})) for (const st of Object.values(vm)) for (const v of Object.values(st)) max = Math.max(max, v);
		return max;
	};
	const lastSeen = (u) => Math.max(requests.find((r) => r.userId === u.id)?.at ?? 0, ...u.keys.map((k) => k.lastUsedAt ?? 0));
	host.append(
		card({
			flush: true,
			body: table(
				[
					{ key: "who", label: "User", render: (u) => h("div", { class: "row" }, avatar(u), h("div", null, h("div", { class: "cell-main" }, u.handle), h("div", { class: "cell-sub" }, u.name))), sortBy: (u) => u.handle },
					{ key: "role", label: "Role", render: roleBadge, sortBy: (u) => u.role },
					{ key: "state", label: "State", render: stateStatus, sortBy: (u) => u.state },
					{ key: "models", label: "Sees", wrap: true, render: (u) => (u.grants.length || u.providerAccess.length
						? h("span", { class: "row wrap", style: { gap: "4px 10px" } }, u.grants.map((g) => servedName(vms.find((v) => v.id === g.virtualModel)?.served ?? "neoproxy/?", { virtual: true })), u.providerAccess.length ? badge(`+${u.providerAccess.length} direct`) : null)
						: h("span", { class: "ink-3" }, "nothing granted")) },
					{ key: "drawn", label: "Drawn", num: true, render: (u) => (drawn(u) ? h("span", { title: "Share of the busiest metered account window this person has used" }, pct(drawn(u))) : h("span", { class: "ink-3", title: "Nothing drawn from a metered account in the current windows" }, "–")), sortBy: drawn },
					{ key: "limits", label: "Limits", render: (u) => h("span", { class: "ink-2" }, u.limits.rpm ? `${u.limits.rpm} rpm` : "no rpm limit", u.limits.tokensPerDay ? ` · ${fmtCompact(u.limits.tokensPerDay)} tok/day` : "") },
					{ key: "keys", label: "Keys", num: true, render: (u) => String(u.keys.length), sortBy: (u) => u.keys.length },
					{ key: "last", label: "Last request", num: true, render: (u) => (lastSeen(u) ? fmtAgo(lastSeen(u)) : h("span", { class: "ink-3" }, "never")), sortBy: lastSeen },
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
	const draft = { handle: "", name: "", password: "", role: "member", state: "active", grants: [], providerAccess: [], limits: { rpm: null, tokensPerDay: null }, keys: [] };
	const err = h("div", { class: "error hidden" });
	dialog({
		title: "Create a user",
		sub: "Set a password for a dashboard identity. A service uses keys and cannot sign in to this dashboard. Grant models on the next screen.",
		body: [
			field("Handle", input({ mono: true, placeholder: "lena", onInput: (e) => (draft.handle = e.target.value.trim().toLowerCase()) }), "Lowercase, what appears in logs."),
			field("Name", input({ placeholder: "Lena", onInput: (e) => (draft.name = e.target.value) })),
			field("Initial password", input({ type: "password", autocomplete: "new-password", onInput: (e) => (draft.password = e.target.value) }), "Required for human dashboard login. Never returned in user records."),
			field(
				"Role",
				seg(
					[
						{ value: "member", label: "Member" },
						{ value: "owner", label: "Owner" },
						{ value: "service", label: "Service" },
						{ value: "admin", label: "Admin" },
					],
					draft.role,
					(v) => (draft.role = v),
				),
				"Owners contribute sign-ins and manage their own accounts, not organization policy. Services are machines; admins manage this organization.",
			),
			err,
		],
		actions: (close) => [
			btn("Cancel", { onClick: close }),
			btn("Create", {
				cls: "primary",
				onClick: async () => {
					try {
						const u = await neo.saveUser(draft);
						close();
						toast(`Created ${u.handle}`);
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

// ---------- one person ----------

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
	const saveBtn = btn("Saved", { icon: "save", cls: "primary", disabled: true, onClick: save });
	const setDirty = (v = true) => {
		saveBtn.replaceChildren(icon("save", 15), v ? "Save changes" : "Saved");
		saveBtn.disabled = !v;
	};
	async function save() {
		try {
			const saved = await neo.saveUser(draft);
			Object.assign(user, saved);
			delete draft.password;
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
			actions: [suspendBtn, saveBtn],
		}),
	);
	root.append(card({
		title: "Identity and access",
		body: h("div", { class: "grid c2" },
			field("Name", input({ value: draft.name, onInput: (e) => { draft.name = e.target.value; setDirty(); } })),
			field("Role", select(["admin", "owner", "member", "service"].map((role) => ({ value: role, label: role })), draft.role, (v) => { draft.role = v; setDirty(); }), "Owner contributes sign-ins to the pool; it does not grant organization administration."),
			field("State", select(["active", "invited", "suspended"].map((state) => ({ value: state, label: state })), draft.state, (v) => { draft.state = v; setDirty(); }), "Suspension revokes access immediately."),
			field("Reset password", input({ type: "password", autocomplete: "new-password", placeholder: "Leave empty to keep current password", onInput: (e) => { if (e.target.value) draft.password = e.target.value; else delete draft.password; setDirty(); } }), "Changing a password revokes existing sessions and keys."),
		),
	}));

	// ----- grants -----
	const grantsHost = h("div", { class: "list" });
	const addHost = h("div", { class: "grant-add" });
	const drawnFor = (vmId) => {
		let max = 0;
		for (const st of Object.values(usage[user.id]?.[vmId] ?? {})) for (const v of Object.values(st)) max = Math.max(max, v);
		return max;
	};
	function renderGrants() {
		clear(grantsHost);
		if (!draft.grants.length) grantsHost.append(h("div", { class: "ink-3", style: { padding: "12px 16px" } }, "No virtual models yet. This person cannot ask for a neoproxy/ name until one is granted below."));
		for (const g of draft.grants) {
			const vm = vms.find((v) => v.id === g.virtualModel);
			if (!vm) continue;
			const sentence = h("div", { class: "allot-sentence" });
			const slider = h("input", { type: "range", min: 0, max: 100, step: 1, value: Math.round(g.allotment * 100), "aria-label": `Allotment for ${vm.name}`, class: "allot-slider" });
			const number = h("input", { type: "number", min: 0, max: 100, step: 1, value: Math.round(g.allotment * 100), "aria-label": `Allotment for ${vm.name} in percent`, class: "input allot-num" });
			const paint = () => sentence.replaceChildren(allotmentSentence({ who: user.handle, allotment: g.allotment, vm }));
			const apply = (raw) => {
				const n = Math.max(0, Math.min(100, Math.round(Number(raw))));
				if (Number.isNaN(n)) return;
				g.allotment = n / 100;
				slider.value = n;
				if (document.activeElement !== number) number.value = n;
				paint();
				setDirty();
			};
			slider.addEventListener("input", (e) => apply(e.target.value));
			number.addEventListener("input", (e) => e.target.value !== "" && apply(e.target.value));
			number.addEventListener("blur", () => { number.value = Math.round(g.allotment * 100); });
			paint();
			const drawn = drawnFor(vm.id);
			grantsHost.append(h("div", { class: "grant" },
				h("div", { class: "grant-who" },
					h("div", { class: "cell-main" }, servedName(vm.served, { virtual: true })),
					h("div", { class: "cell-sub" }, vm.description || plural(vm.stages.length, "stage"), drawn ? ` · drawn so far: ${pct(drawn)} of its busiest meter` : "")),
				h("div", { class: "allot-ctl" }, slider, number, h("span", { class: "ink-3" }, "%")),
				btn(null, { icon: "x", cls: "icon sm", title: `Remove ${vm.name} from ${user.handle}`, onClick: () => { draft.grants = draft.grants.filter((x) => x !== g); setDirty(); renderGrants(); } }),
				sentence,
			));
		}
		renderAdd();
	}
	function renderAdd() {
		clear(addHost);
		const granted = new Set(draft.grants.map((g) => g.virtualModel));
		const remaining = vms.filter((vm) => !granted.has(vm.id));
		if (!vms.length) { addHost.append(h("span", { class: "ink-3" }, "There are no virtual models yet. Create one under Virtual models, then grant it here.")); return; }
		if (!remaining.length) { addHost.append(h("span", { class: "ink-3" }, "Every virtual model is already granted.")); return; }
		let pick = remaining[0].id;
		addHost.append(
			select(remaining.map((vm) => ({ value: vm.id, label: vm.served })), pick, (v) => (pick = v), { "aria-label": "Virtual model to grant" }),
			btn("Grant at 100%", { icon: "plus", cls: "outline", onClick: () => { draft.grants.push({ virtualModel: pick, allotment: 1 }); setDirty(); renderGrants(); } }),
		);
	}
	renderGrants();
	root.append(card({
		title: "Virtual models this person may use",
		sub: "Each virtual model has stages with thresholds: how full an account may get, and when to skip a stage. The allotment scales every threshold in that chain for this person; it is a share of those thresholds, not of the provider's quota.",
		flush: true,
		body: [grantsHost, addHost],
	}));

	// ----- direct provider access -----
	const providers = vault.providers.filter((p) => p.kind !== "neopi-gateway");
	const exposedModels = (p) => models.filter((m) => m.providerId === p.id && m.exposed);
	const directHost = h("div", { class: "direct" });
	const expanded = new Set();
	function renderDirect() {
		clear(directHost);
		for (const p of providers) {
			const on = draft.providerAccess.includes(p.id);
			const ms = exposedModels(p);
			const row = h("div", { class: `direct-row ${on ? "on" : ""}` });
			const sw = toggle(on, (v) => {
				draft.providerAccess = v ? [...draft.providerAccess, p.id] : draft.providerAccess.filter((x) => x !== p.id);
				setDirty();
				if (!v) expanded.delete(p.id);
				renderDirect();
			}, providerLabel(p).main, { disabled: !p.exposed && !on });
			row.append(h("div", { class: "direct-head" }, providerDot(p, 9), sw, h("span", { class: "ink-3 direct-count" }, !p.exposed ? "hidden from direct use" : ms.length ? plural(ms.length, "model") : "no exposed models")));
			if (on) {
				const open = expanded.has(p.id);
				const shown = open ? ms : ms.slice(0, 8);
				row.append(h("div", { class: "direct-models" },
					ms.length ? shown.map((m) => h("span", { class: "tag mono", title: m.display }, servedName(m.served))) : h("span", { class: "ink-3" }, "Nothing to call yet: expose some of this provider's models under Models."),
					ms.length > 8 ? h("button", { type: "button", class: "link-btn", onClick: () => { open ? expanded.delete(p.id) : expanded.add(p.id); renderDirect(); } }, open ? "show fewer" : `show all ${ms.length}`) : null));
			}
			directHost.append(row);
		}
		if (!providers.length) directHost.append(h("div", { class: "ink-3" }, "No providers are connected yet."));
	}
	renderDirect();
	root.append(card({
		title: "Direct provider access",
		sub: "Direct access bypasses virtual models and their budgets; the engine routing strategy applies.",
		body: [directHost, h("div", { class: "hint", style: { marginTop: "10px" } }, "Turn a provider on and this person may call every model listed under it as neoproxy/<provider>/<model>.")],
	}));

	// ----- limits -----
	root.append(card({
		title: "Limits",
		sub: "Hard ceilings on top of allotments. Leave a field empty for no limit.",
		body: h("div", { class: "grid c2" },
			field("Requests per minute", input({ type: "number", min: 0, value: draft.limits.rpm ?? "", placeholder: "no limit", onInput: (e) => { draft.limits.rpm = e.target.value ? Number(e.target.value) : null; setDirty(); } }), "Across all of their keys."),
			field("Tokens per day", input({ type: "number", min: 0, step: 100000, value: draft.limits.tokensPerDay ?? "", placeholder: "no limit", onInput: (e) => { draft.limits.tokensPerDay = e.target.value ? Number(e.target.value) : null; setDirty(); } }), "Input and output together."),
		),
	}));

	// ----- keys -----
	const drawable = drawableAccounts(user, vault.accounts);
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
		keysHost.replaceChildren(keysTable({ keys: user.keys, accounts: vault.accounts, providers: vault.providers, onRevoke: revoke, emptyHint: "No keys. Mint one and hand it over; the key itself is shown once." }));
	}
	renderKeys();
	root.append(card({
		title: "Keys",
		sub: "What this person's tools send as the bearer token. Each key is its own line in Activity.",
		actions: [btn("Mint key", { icon: "plus", cls: "outline sm", onClick: () => mintKeyDialog({ user, accounts: drawable, providers: vault.providers, onMinted: (key) => { user.keys.push(key); renderKeys(); } }) })],
		flush: true,
		body: keysHost,
	}));

	// ----- sign-ins this person contributes -----
	const owned = vault.accounts.filter((a) => a.ownerId === user.id);
	if (owned.length || user.role === "owner" || user.role === "admin") {
		const ownedHost = h("div");
		let allOwned = false;
		const renderOwned = () => {
			const shown = allOwned ? owned : owned.slice(0, 6);
			ownedHost.replaceChildren(
				owned.length
					? table([
						{ key: "p", label: "Provider", render: (a) => { const p = vault.providers.find((x) => x.id === a.providerId); return h("span", { class: "row" }, providerDot(p, 8), providerLabel(p).main); } },
						{ key: "l", label: "Account", render: (a) => h("span", { class: "cell-main" }, a.label) },
						{ key: "pl", label: "Plan", render: (a) => h("span", { class: "ink-2" }, a.plan || "–") },
						{ key: "h", label: "Health", render: (a) => healthStatus(a) },
					], shown, { onRow: (a) => ctx.navigate(`pool/${a.id}`) })
					: h("div", { class: "ink-3", style: { padding: "14px 16px" } }, `${user.handle} contributes no sign-ins yet.`),
				owned.length > 6 ? h("div", { style: { padding: "8px 16px" } }, h("button", { type: "button", class: "link-btn", onClick: () => { allOwned = !allOwned; renderOwned(); } }, allOwned ? "show fewer" : `show all ${owned.length}`)) : null,
			);
		};
		renderOwned();
		root.append(card({
			title: "Sign-ins they contribute",
			sub: "The contributor may rename, pause, refresh and remove a sign-in; an administrator decides which stages draw from it. Reassigning does not reveal the token to anyone; the proxy holds it.",
			actions: [btn("Open in Pool", { icon: "arrowRight", cls: "sm", onClick: () => ctx.navigate("pool") })],
			flush: true,
			body: [
				ownedHost,
				h("div", { class: "hint", style: { padding: "0 16px 14px" } }, "Reassigning changes only whose sign-in this is on the proxy's record: who may manage it, and whose keys it can serve. Owner-role people only draw from what they contribute, so their keys limited to a reassigned sign-in stop reaching it at once. Nothing is copied or exposed."),
			],
		}));
	}

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
				: empty({ title: "Nothing yet", hint: "Requests with this person's keys will show here." }),
		}),
	);
	setDirty(false);
}
