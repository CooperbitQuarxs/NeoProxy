// A person's keys: the table that lists them and the dialog that mints one.
// Shared by Users, Access keys, My models and Connect, so a key reads the same
// everywhere: its label first, the prefix as the quiet id, what it reaches,
// when it was last used and when it stops working.

import { h, icon, fmtAgo, fmtIn, fmtDate, plural, clear, copyText } from "./dom.js";
import { neo } from "./client.js";
import { btn, iconBtn, avatar, servedName, providerLabel, providerDot, search, field, input, seg, dialog, toast, table } from "./components.js";
import { multiSelect } from "./multiselect.js";

// What a key's expiry looks like: quiet when never, plain when future, red once past.
export function expiryCell(key, now = Date.now()) {
	if (!key.expiresAt) return h("span", { class: "ax-fact" }, "never");
	const past = key.expiresAt <= now;
	const soon = !past && key.expiresAt - now < 7 * 864e5;
	return h("span", { class: `key-exp ${past ? "bad" : soon ? "warn" : ""}`, title: fmtDate(key.expiresAt) },
		past ? `expired ${fmtAgo(key.expiresAt, now)}` : `in ${fmtIn(key.expiresAt, now).replace(/ \d+h$/, "")}`,
		h("span", { class: "cell-sub" }, new Date(key.expiresAt).toLocaleDateString("en-GB", { day: "2-digit", month: "short", year: "numeric" })));
}

// One way to name an account everywhere: provider, then its email or label
// ("Claude · MoonBnuuy@protonmail.com", "Z.ai · key …CS5w"). The short id is
// added only when another account of `others` would read the same.
export function accountLabel(a, providers = [], others = []) {
	const p = providers.find((x) => x.id === a.providerId);
	const prov = p ? providerLabel(p).main : a.providerId;
	const name = a.kind === "apiKey" && a.label.startsWith("…") ? `key ${a.label}` : a.label;
	const twin = !/…/.test(a.label) && others.some((o) => o.id !== a.id && o.providerId === a.providerId && o.label === a.label);
	return `${prov} · ${name}${twin ? ` · …${a.id.slice(-6)}` : ""}`;
}

function scopeCell(key, accounts, providers) {
	const narrowed = key.models?.length ? h("span", { class: "pk-chip", title: key.models.join("\n") }, icon("box", 11), plural(key.models.length, "direct model")) : null;
	if (!key.accountIds?.length) return h("span", { class: "key-scope" }, h("span", { class: "ax-fact" }, narrowed ? "every account, only" : "everything they may use"), narrowed);
	return h("span", { class: "key-scope" }, key.accountIds.map((id) => {
		const a = accounts.find((x) => x.id === id);
		const p = a && providers.find((x) => x.id === a.providerId);
		const name = a ? accountLabel(a, providers, accounts) : "an account no longer in the pool";
		return h("span", { class: "pk-chip", title: name }, p ? providerDot(p, 7) : null, h("span", { class: "pk-chip-v" }, name));
	}), narrowed);
}

// A person's keys as a table. `onRevoke` is omitted when the viewer may not revoke;
// `whoOf` adds a Person column and `onOpen` makes rows open that person.
export function keysTable({ keys, accounts = [], providers = [], onRevoke, scoped = true, emptyHint, whoOf, onOpen }) {
	const columns = [
		whoOf ? { key: "who", label: "Person", render: (k) => h("span", { class: "row" }, avatar(whoOf(k), "sm"), h("span", { class: "cell-main" }, whoOf(k).handle)) } : null,
		{ key: "key", label: "Key", render: (k) => h("div", { class: "pk-key" }, h("div", { class: "cell-main" }, k.label || "Unnamed key"), h("code", { class: "cell-sub" }, `${k.prefix}…`)) },
		scoped ? { key: "scope", label: "Reaches", wrap: true, render: (k) => scopeCell(k, accounts, providers) } : null,
		{ key: "last", label: "Last used", render: (k) => (k.lastUsedAt ? h("span", { class: "ink-2", title: fmtDate(k.lastUsedAt) }, fmtAgo(k.lastUsedAt)) : h("span", { class: "ax-fact" }, "never used")) },
		{ key: "created", label: "Made", render: (k) => h("span", { class: "ink-3", title: fmtDate(k.createdAt) }, fmtAgo(k.createdAt)) },
		{ key: "expires", label: "Expires", render: (k) => expiryCell(k) },
		onRevoke ? { key: "x", label: "", width: "48px", render: (k) => h("span", { class: "pk-acts", onClick: (e) => e.stopPropagation(), onKeydown: (e) => e.stopPropagation() }, iconBtn("trash", `Revoke ${k.label || k.prefix}`, () => onRevoke(k), "danger")) } : null,
	].filter(Boolean);
	return table(columns, keys, { onRow: onOpen, stack: true, emptyState: h("div", { class: "ax-fact pk-empty" }, emptyHint ?? "No keys yet.") });
}

// Accounts a person's keys can draw from. Owners only reach what they contribute;
// everyone else reaches any sign-in that has a contributor.
export function drawableAccounts(user, accounts) {
	return accounts.filter((a) => a.ownerId && (user.role !== "owner" || a.ownerId === user.id));
}

// Mint a key: label, optional expiry, account scope, optional direct models
// (`models`: the provider models the person may call directly). Calls
// `onMinted(key, secret)` once minted, then shows the secret once.
// Accounts are an explicit choice, like Direct models: "All accounts" or "Only
// selected accounts". Only selected needs at least one tick, so clearing the
// ticks never quietly widens the key back to everything. A summary of what the
// key will reach sits beside Mint key. `label` pre-fills the label field.
export function mintKeyDialog({ user, accounts = [], providers = [], models = [], onMinted, label: presetLabel = "" }) {
	const ownerRole = user.role === "owner";
	let label = presetLabel, expiry = "never", date = "";
	let accountMode = ownerRole ? "some" : "all";
	const scope = new Set();
	const keyModels = new Set();
	let narrowModels = false;
	const err = h("div", { class: "error pk-err", role: "alert" });
	// An account error goes once the account choice is satisfied.
	const clearAccountErr = () => { if (err.dataset.about === "accounts") { err.textContent = ""; delete err.dataset.about; } };
	const when = h("div", { class: "hint" });
	const dateInput = input({ type: "date", min: new Date().toISOString().slice(0, 10), "aria-label": "Last day the key works", onInput: (e) => { date = e.target.value; showWhen(); } });
	const dateRow = h("div", { class: "hidden" }, dateInput);
	const expiresAt = () => {
		if (expiry === "never") return undefined;
		if (expiry === "date") return date ? new Date(`${date}T23:59:59`).getTime() : NaN;
		return Date.now() + Number(expiry) * 864e5;
	};
	function showWhen() {
		dateRow.classList.toggle("hidden", expiry !== "date");
		const at = expiresAt();
		when.textContent = at === undefined ? "The key works until it is revoked." : Number.isNaN(at) ? "Pick the last day the key should work." : `The key stops working on ${fmtDate(at)}.`;
		paintSummary();
	}
	// What the key will reach, in one plain line beside Mint key.
	const summary = h("div", { class: "pk-summary", "aria-live": "polite" });
	function paintSummary() {
		const picked = accounts.filter((a) => scope.has(a.id));
		const reach = accountMode === "all"
			? `every account ${user.handle} may use`
			: picked.length === 1 ? accountLabel(picked[0], providers, accounts) : picked.length ? plural(picked.length, "account") : "no account yet";
		const at = expiresAt();
		summary.replaceChildren("Reaches ",
			h("b", { class: accountMode === "some" && !picked.length ? "pk-need" : "" }, reach),
			narrowModels ? ` · ${plural(keyModels.size, "direct model")} only` : "",
			" · ", at === undefined ? "never expires" : Number.isNaN(at) ? "pick an expiry day" : `until ${new Date(at).toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" })}`);
	}
	showWhen();

	// ----- accounts -----
	const scopeNote = h("div", { class: "pk-scope-note", "aria-live": "polite" });
	const clearScope = btn("Clear selection", { cls: "ghost sm", onClick: () => { scope.clear(); renderScopes(filter); groups.querySelector("input")?.focus(); } });
	const paintNote = () => {
		clearScope.style.visibility = scope.size ? "" : "hidden";
		scopeNote.replaceChildren(scope.size
			? h("span", null, "Only ", h("b", null, plural(scope.size, "account")), ` of ${accounts.length}`)
			: h("span", { class: "pk-need" }, ownerRole ? "Tick at least one account you contribute" : "Tick at least one account"));
		if (scope.size) clearAccountErr();
		paintSummary();
	};
	const groups = h("div", { class: "scope-groups pk-scope" });
	let filter = "";
	function renderScopes(value = "") {
		filter = value;
		const q = value.trim().toLowerCase();
		clear(groups);
		for (const provider of providers) {
			const lab = providerLabel(provider);
			const rows = accounts.filter((a) => a.providerId === provider.id && (!q || [a.label, lab.main, lab.source].some((t) => t?.toLowerCase().includes(q))));
			if (!rows.length) continue;
			groups.append(h("div", { class: "scope-group", role: "group", "aria-label": lab.main },
				h("div", { class: "scope-prov", title: provider.id }, providerDot(provider, 8), [lab.main, lab.source].filter(Boolean).join(" · ")),
				rows.map((a) => h("label", { class: "pk-acct", title: a.id },
					h("input", { type: "checkbox", checked: scope.has(a.id), onChange: (e) => { e.target.checked ? scope.add(a.id) : scope.delete(a.id); paintNote(); } }),
					h("span", { class: "pk-acct-label" }, accountLabel(a, providers, accounts).replace(/^[^·]+· /, "")),
					h("span", { class: "pk-acct-sub" }, [a.plan && !/^subscription$/i.test(a.plan) ? a.plan : "", { subscription: "signed in", apiKey: "API key", imported: "imported", neopi: "shared from NeoPi", file: "credential file" }[a.kind]].filter(Boolean).join(" · ")))),
			));
		}
		if (!groups.childElementCount) groups.append(h("p", { class: "ax-fact" }, q ? "No accounts match." : "This person cannot draw from any account yet."));
		paintNote();
	}
	renderScopes();
	const picker = h("div", { class: `scope-field pk-pick ${accountMode === "some" ? "" : "hidden"}` },
		h("div", { class: "pk-scope-head" }, scopeNote, clearScope),
		accounts.length > 8 ? search("Filter accounts by provider or label", renderScopes, { "aria-label": "Filter accounts" }) : null,
		groups);
	const scopeField = accounts.length
		? field("Accounts", h("div", { class: "scope-field" },
			ownerRole ? null : modeSeg(seg([{ value: "all", label: `All accounts ${user.handle} may use` }, { value: "some", label: "Only selected accounts" }], accountMode, (v) => {
				accountMode = v;
				if (v === "all") clearAccountErr();
				picker.classList.toggle("hidden", v !== "some");
				paintSummary();
				if (v === "some") picker.querySelector("input")?.focus();
			})),
			picker),
		ownerRole ? "An owner's key must name at least one account they contribute." : "A key can only narrow what the person may draw from, never widen it.")
		: null;

	// "Only these models": the same list interaction as the person's direct models.
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
				h("span", { class: "dm-name" }, m.display), servedName(m.served))),
			list.length > shown.length ? h("div", { class: "dm-more" }, `${shown.length} of ${list.length.toLocaleString()} shown; search to find the rest`) : "",
			list.length ? "" : h("div", { class: "dm-more" }, "No models match."),
		);
		mCount.textContent = keyModels.size ? `${plural(keyModels.size, "model")} ticked. Drag or Shift+click to select several, Space ticks them.` : "Tick the models this key may call. Drag or Shift+click to select several, Space ticks them.";
		paintSummary();
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
	// Rarely needed, so it stays behind one link until asked for.
	const limitBtn = h("button", { type: "button", class: "pk-limit", "aria-expanded": "false", onClick: () => {
		narrowModels = !narrowModels;
		limitBtn.setAttribute("aria-expanded", String(narrowModels));
		limitBtn.lastChild.textContent = narrowModels ? "Allow every direct model again" : "Limit direct models";
		modelScope.classList.toggle("hidden", !narrowModels);
		if (narrowModels) renderModelScope();
		paintSummary();
	} }, icon("box", 13), h("span", null, "Limit direct models"));
	const modelField = models.length
		? h("div", { class: "field pk-models" }, limitBtn, modelScope,
			h("div", { class: "hint" }, "Narrows only neoproxy/<provider>/<model> calls. Virtual models follow the person's grants."))
		: null;

	dialog({
		title: `Mint a key for ${user.handle}`,
		sub: `The key reaches what ${user.handle} may use and nothing more. It never signs in to the dashboard.`,
		body: [
			field("Label", input({ placeholder: "What will use this key, e.g. NeoCode on the laptop", value: presetLabel, autofocus: true, onInput: (e) => (label = e.target.value) })),
			field("Expires", h("div", { class: "scope-field" },
				seg([{ value: "never", label: "Never" }, { value: "30", label: "30 days" }, { value: "90", label: "90 days" }, { value: "date", label: "On a date" }], expiry, (v) => { expiry = v; showWhen(); if (v === "date") dateInput.focus(); }),
				dateRow, when)),
			scopeField,
			modelField,
			err,
		],
		actions: (close) => [
			summary,
			btn("Cancel", { onClick: close }),
			btn("Mint key", { icon: "key", cls: "primary", onClick: async (event) => {
				err.textContent = "";
				const at = expiresAt();
				if (Number.isNaN(at) || (at !== undefined && at <= Date.now())) { err.textContent = "Choose a day in the future for the key to expire."; return; }
				if (accountMode === "some" && !scope.size) { err.textContent = ownerRole ? "Tick at least one account you contribute." : "Tick at least one account, or choose All accounts."; err.dataset.about = "accounts"; picker.querySelector("input")?.focus(); return; }
				if (narrowModels && !keyModels.size) { err.textContent = "Tick at least one model, or switch to every direct model they may call."; return; }
				const button = event.currentTarget;
				button.disabled = true;
				try {
					const { key, secret } = await neo.createKey(user.id, label.trim() || "unnamed", accountMode === "some" ? [...scope] : undefined, at, narrowModels ? [...keyModels] : undefined);
					close();
					onMinted?.(key, secret);
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

// A two-way choice that becomes full-width radio cards on a phone (css/access.css .pk-mode).
function modeSeg(el) {
	el.classList.add("pk-mode");
	el.setAttribute("role", "radiogroup");
	for (const b of el.children) {
		b.setAttribute("role", "radio");
		const sync = () => b.setAttribute("aria-checked", b.getAttribute("aria-pressed"));
		sync();
		new MutationObserver(sync).observe(b, { attributes: true, attributeFilter: ["aria-pressed"] });
	}
	return el;
}
