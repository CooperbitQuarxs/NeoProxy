// Users: who may ask the proxy for what. A person's page says, in order: which
// virtual models they may use and how much of each one's thresholds (their
// allotment), which providers they may call directly, their limits, their keys,
// and the sign-ins they contribute to the pool.

import { h, icon, fmtAgo, fmtIn, fmtTime, fmtDate, fmtCompact, plural, clear, copyText } from "../lib/dom.js";
import { neo, onChange, ApiError } from "../lib/client.js";
import { pageHead, card, btn, badge, avatar, status, servedName, providerLabel, providerDot, search, field, input, select, seg, toggle, dialog, confirm, toast, empty, spinner, errorState, outcomeBadge, table, healthStatus } from "../lib/components.js";
import { multiSelect, selectionBar } from "../lib/multiselect.js";
import { matchModel, modelName, directModelsFor } from "../lib/access.js";

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
	const narrowed = key.models?.length ? h("span", { class: "key-models", title: key.models.join("\n") }, `only ${plural(key.models.length, "direct model")}`) : null;
	if (!key.accountIds?.length) return h("span", { class: "key-scope" }, h("span", { class: "ink-2" }, "every account this person may use"), narrowed);
	return h("span", { class: "key-scope" }, key.accountIds.map((id) => {
		const a = accounts.find((x) => x.id === id);
		const p = a && providers.find((x) => x.id === a.providerId);
		return h("span", { class: "tag", title: id }, p ? providerDot(p, 7) : null, a ? `${providerLabel(p).main} · ${a.label}` : "an account");
	}), narrowed);
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

// Mint a key: label, optional expiry, optional account scope, optional direct
// models (`models`: the provider models the person may call directly). Calls
// `onMinted(key)` once minted, then shows the secret once.
export function mintKeyDialog({ user, accounts = [], providers = [], models = [], onMinted }) {
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

	// "Only these models": the same list interaction as the person's direct models.
	const keyModels = new Set();
	let narrowModels = false;
	let mQuery = "";
	const mList = h("div", { class: "dm-list", role: "listbox", "aria-multiselectable": "true", "aria-label": "Direct models this key may call" });
	const mCount = h("div", { class: "dm-note" });
	const mVisible = () => models.filter((m) => !mQuery || `${m.id}\n${m.display}`.toLowerCase().includes(mQuery));
	function renderModelScope() {
		const list = mVisible();
		const shown = list.slice(0, 150);
		mList.replaceChildren(
			...shown.map((m) => h("div", { class: "dm-row", role: "option", "aria-selected": "false", tabindex: "-1", "data-key": m.id },
				h("button", { type: "button", class: "dm-check", role: "checkbox", "aria-checked": String(keyModels.has(m.id)), "aria-label": `Allow ${m.display}`, tabindex: "-1", "data-act": "check" }, icon("check", 12)),
				servedName(m.served))),
			list.length > shown.length ? h("div", { class: "dm-more" }, `${shown.length} of ${list.length.toLocaleString()} shown; search to find the rest`) : "",
			list.length ? "" : h("div", { class: "dm-more" }, "No models match."),
		);
		mCount.textContent = keyModels.size ? `${plural(keyModels.size, "model")} ticked. Drag or Shift+click to select several, Space ticks them.` : "Tick the models this key may call. Drag or Shift+click to select several, Space ticks them.";
		msel.refresh();
	}
	const setKeyModels = (ids, on) => {
		for (const id of ids) if (on) keyModels.add(id); else keyModels.delete(id);
		renderModelScope();
	};
	const msel = multiSelect(mList, { row: ".dm-row", onSpace: (keys) => setKeyModels(keys, !keys.every((k) => keyModels.has(k))) });
	mList.addEventListener("click", (e) => {
		const id = e.target.closest('[data-act="check"]')?.closest(".dm-row")?.dataset.key;
		if (id) setKeyModels([id], !keyModels.has(id));
	});
	const modelScope = h("div", { class: "hidden" }, h("div", { class: "dm key-dm" }, models.length > 8 ? search("Find a model", (v) => { mQuery = v.trim().toLowerCase(); renderModelScope(); }, { "aria-label": "Find a model" }) : null, mList, mCount));
	const modelField = models.length
		? field("Direct models", h("div", { class: "scope-field" },
			seg([{ value: "all", label: "Every direct model they may call" }, { value: "some", label: "Only these models" }], "all", (v) => {
				narrowModels = v === "some";
				modelScope.classList.toggle("hidden", !narrowModels);
				if (narrowModels) renderModelScope();
			}),
			modelScope),
		"Narrows only neoproxy/<provider>/<model> calls. Virtual models follow the person's grants.")
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
			modelField,
			err,
		],
		actions: (close) => [
			btn("Cancel", { onClick: close }),
			btn("Mint key", { icon: "key", cls: "primary", onClick: async (event) => {
				err.textContent = "";
				const at = expiresAt();
				if (Number.isNaN(at) || (at !== undefined && at <= Date.now())) { err.textContent = "Choose a day in the future for the key to expire."; return; }
				if (limited && !scope.size) { err.textContent = "Pick at least one account, or switch to every account they may use."; return; }
				if (narrowModels && !keyModels.size) { err.textContent = "Tick at least one model, or switch to every direct model they may call."; return; }
				const button = event.currentTarget;
				button.disabled = true;
				try {
					const { key, secret } = await neo.createKey(user.id, label.trim() || "unnamed", limited ? [...scope] : undefined, at, narrowModels ? [...keyModels] : undefined);
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
		const unticked = Object.entries(draft.modelAccess ?? {}).filter(([pid, pats]) => draft.providerAccess.includes(pid) && !pats.length).map(([pid]) => providerLabel(vault.providers.find((p) => p.id === pid)).main);
		if (unticked.length) { toast(`Tick at least one ${unticked.join(", ")} model, or turn that provider's direct access off.`, "bad", 5000); return; }
		try {
			const saved = await neo.saveUser(draft);
			Object.assign(user, saved);
			user.modelAccess = saved.modelAccess; // omitted when empty
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
	// A provider that is on serves every served model unless the person has a
	// list for it (modelAccess[provider]); then only the ticked models.
	const providers = vault.providers.filter((p) => p.kind !== "neopi-gateway");
	const modelById = new Map(models.map((m) => [m.id, m]));
	const providerModels = (pid) => models.filter((m) => m.providerId === pid && !m.virtualOnly);
	const patternsOf = (pid) => draft.modelAccess?.[pid];
	const allowed = (m) => { const pats = patternsOf(m.providerId); return !pats || matchModel(pats, modelName(m)); };
	// Everything served, plus anything hidden that was ticked for them.
	const listed = (pid) => providerModels(pid).filter((m) => m.exposed || (patternsOf(pid) ?? []).includes(modelName(m)));
	const dmQuery = new Map();
	const dmShown = new Map();
	const DM_PAGE = 100;
	const visibleIn = (pid) => { const q = dmQuery.get(pid) ?? ""; return listed(pid).filter((m) => !q || `${m.id}\n${m.display}`.toLowerCase().includes(q)); };
	function setPatterns(pid, pats) {
		draft.modelAccess = { ...(draft.modelAccess ?? {}) };
		if (pats) draft.modelAccess[pid] = pats;
		else delete draft.modelAccess[pid];
		setDirty();
	}
	function setAllowed(list, on) {
		for (const [pid, chosen] of Map.groupBy(list, (m) => m.providerId)) {
			let pats = patternsOf(pid);
			if (!pats) {
				if (on) continue; // every model is already allowed
				pats = providerModels(pid).filter((m) => m.exposed).map(modelName);
			}
			// Unticking a model a wildcard covers: spell the wildcard out first.
			if (!on && pats.some((p) => p.endsWith("*"))) pats = [...new Set(pats.flatMap((p) => (p.endsWith("*") ? providerModels(pid).map(modelName).filter((n) => matchModel([p], n)) : [p])))];
			const next = new Set(pats);
			for (const m of chosen) {
				const name = modelName(m);
				if (!on) next.delete(name);
				else if (!matchModel([...next], name)) next.add(name);
			}
			setPatterns(pid, [...next]);
			paintProvider(pid);
		}
	}
	function countText(p) {
		if (!p.exposed) return "hidden from direct use";
		const served = providerModels(p.id).filter((m) => m.exposed);
		if (!served.length) return "no served models";
		if (!patternsOf(p.id)) return ["all ", h("b", null, served.length.toLocaleString()), served.length === 1 ? " model" : " models"];
		return [h("b", null, served.filter(allowed).length.toLocaleString()), ` of ${plural(served.length, "model")}`];
	}
	function dmRow(m) {
		return h("div", { class: `dm-row ${m.exposed ? "" : "off"}`, role: "option", "aria-selected": "false", tabindex: "-1", "data-key": m.id, "aria-label": `${m.display}, ${allowed(m) ? "allowed" : "not allowed"}` },
			h("button", { type: "button", class: "dm-check", role: "checkbox", "aria-checked": String(allowed(m)), "aria-label": `Allow ${m.display}`, tabindex: "-1", "data-act": "check" }, icon("check", 12)),
			servedName(m.served),
			m.exposed ? null : h("span", { class: "tag warn", title: "Hidden on the Models page: nobody can call it until it is served" }, "hidden"));
	}
	const directHost = h("div", { class: "direct" });
	const expanded = new Set();
	const blocks = new Map(); // provider id -> { p, count, list, modeHost }
	function paintProvider(pid) {
		const b = blocks.get(pid);
		if (!b) return;
		b.count.replaceChildren(...[countText(b.p)].flat());
		if (!b.list) return;
		b.modeHost.replaceChildren(seg([{ value: "all", label: "Every model, new ones too" }, { value: "only", label: "Only the ticked ones" }], patternsOf(pid) ? "only" : "all", (v) => {
			if (v === "all") setPatterns(pid, undefined);
			else if (!patternsOf(pid)) setPatterns(pid, providerModels(pid).filter((m) => m.exposed).map(modelName));
			paintProvider(pid);
		}));
		const list = visibleIn(pid);
		const shown = Math.min(list.length, dmShown.get(pid) ?? DM_PAGE);
		b.list.replaceChildren(
			...list.slice(0, shown).map(dmRow),
			shown < list.length ? h("div", { class: "dm-more" }, h("button", { type: "button", class: "link-btn", "data-act": "more", "data-pid": pid }, `Show ${Math.min(DM_PAGE, list.length - shown)} more`), `${shown.toLocaleString()} of ${list.length.toLocaleString()} shown`) : "",
			list.length ? "" : h("div", { class: "dm-more" }, "No models match."),
		);
		dms.refresh();
	}
	function renderDirect() {
		clear(directHost);
		blocks.clear();
		for (const p of providers) {
			const on = draft.providerAccess.includes(p.id);
			const lab = providerLabel(p).main;
			const sw = toggle(on, (v) => {
				draft.providerAccess = v ? [...draft.providerAccess, p.id] : draft.providerAccess.filter((x) => x !== p.id);
				if (!v) {
					expanded.delete(p.id);
					if (patternsOf(p.id)) setPatterns(p.id, undefined);
				}
				setDirty();
				renderDirect();
			}, lab, { disabled: !p.exposed && !on });
			const open = on && expanded.has(p.id);
			const b = { p, count: h("span", { class: "ink-3 direct-count" }), list: null, modeHost: null };
			blocks.set(p.id, b);
			const choose = on && listed(p.id).length ? h("button", { type: "button", class: "link-btn", "aria-expanded": String(open), onClick: () => { if (open) expanded.delete(p.id); else expanded.add(p.id); renderDirect(); } }, open ? "Done" : "Choose models") : null;
			const row = h("div", { class: `direct-row ${on ? "on" : ""}` }, h("div", { class: "direct-head" }, providerDot(p, 9), sw, b.count, choose));
			if (open) {
				b.list = h("div", { class: "dm-list", role: "listbox", "aria-multiselectable": "true", "aria-label": `${lab} models ${user.handle} may call` });
				b.modeHost = h("span");
				row.append(h("div", { class: "dm" },
					h("div", { class: "dm-tools" },
						b.modeHost,
						listed(p.id).length > 8 ? search(`Find a ${lab} model`, (v) => {
							dmQuery.set(p.id, v.trim().toLowerCase());
							dmShown.delete(p.id);
							const shown = new Set(visibleIn(p.id));
							const keep = [...dms.selected].filter((k) => modelById.get(k)?.providerId !== p.id || shown.has(modelById.get(k)));
							if (keep.length !== dms.selected.size) dms.set(keep);
							paintProvider(p.id);
						}, { "aria-label": `Find a ${lab} model`, value: dmQuery.get(p.id) ?? "" }) : null,
						h("button", { type: "button", class: "link-btn", onClick: () => setAllowed(visibleIn(p.id), true) }, "Tick all shown"),
						h("button", { type: "button", class: "link-btn", onClick: () => setAllowed(visibleIn(p.id), false) }, "Untick all shown")),
					b.list,
					h("div", { class: "dm-note" }, "Drag across models or Shift+click to select several, then press Space to tick or untick them together. Ctrl+A selects every model shown.")));
			}
			directHost.append(row);
			paintProvider(p.id);
		}
		if (!providers.length) directHost.append(h("div", { class: "ink-3" }, "No providers are connected yet."));
		dms.refresh();
	}
	const selectedModels = () => [...dms.selected].map((k) => modelById.get(k)).filter(Boolean);
	const dmBar = selectionBar({
		noun: "models selected",
		actions: [
			{ label: "Allow", icon: "check", title: "Tick the selected models", onClick: () => setAllowed(selectedModels(), true) },
			{ label: "Don't allow", icon: "x", title: "Untick the selected models", onClick: () => setAllowed(selectedModels(), false) },
		],
		onClear: () => { dms.clear(); dms.focus(); },
	});
	const dms = multiSelect(directHost, {
		row: ".dm-row",
		groupKeys: (el) => visibleIn(modelById.get(el.dataset.key)?.providerId).map((m) => m.id),
		onChange: (sel) => dmBar.update(sel.size),
		onSpace: (keys) => { const list = keys.map((k) => modelById.get(k)).filter(Boolean); setAllowed(list, !list.every(allowed)); },
	});
	directHost.addEventListener("click", (e) => {
		const check = e.target.closest('[data-act="check"]');
		const m = check && modelById.get(check.closest(".dm-row")?.dataset.key);
		if (m) { setAllowed([m], !allowed(m)); return; }
		const more = e.target.closest('[data-act="more"]');
		if (more) { dmShown.set(more.dataset.pid, (dmShown.get(more.dataset.pid) ?? DM_PAGE) + DM_PAGE); paintProvider(more.dataset.pid); }
	});
	renderDirect();
	root.append(card({
		title: "Direct provider access",
		sub: "Direct access bypasses virtual models and their budgets; the engine routing strategy applies.",
		body: [directHost, h("div", { class: "hint", style: { marginTop: "10px" } }, "Turn a provider on and this person may call its served models as neoproxy/<provider>/<model>. Choose models to narrow that to a list; a key can narrow it further.")],
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
		actions: [btn("Mint key", { icon: "plus", cls: "outline sm", onClick: () => mintKeyDialog({ user, accounts: drawable, providers: vault.providers, models: directModelsFor(user, models), onMinted: (key) => { user.keys.push(key); renderKeys(); } }) })],
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
	root.append(dmBar.el);
	setDirty(false);
}
