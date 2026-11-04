// Access keys: every key that can reach this proxy, in one place. People's keys
// first (mint one for anyone from here, revoke in place, a row opens its person),
// then the plain engine keys for clients that cannot use a person's key.

import { h, mask, copyText, plural } from "../lib/dom.js";
import { engine, neo, invalidate, onChange } from "../lib/client.js";
import { pageHead, card, btn, iconBtn, table, empty, copyLine, field, input, dialog, confirm, toast, spinner, errorState, search, avatar, picker } from "../lib/components.js";
import { keysTable, mintKeyDialog, drawableAccounts } from "../lib/person-keys.js";
import { directModelsFor } from "../lib/access.js";

export async function render(ctx) {
	const { root } = ctx;
	let engineKeys = [], users = [], vault = null, models = [], query = "", disposed = false, activeDialog;
	const peopleHost = h("div"), engineHost = h("div");
	const peopleCount = h("span"), engineCount = h("span");

	const mintBtn = btn("Mint a key", { icon: "key", cls: "primary", onClick: () => pickPerson() });
	mintBtn.setAttribute("aria-haspopup", "listbox");
	const head = pageHead({
		title: "Access keys",
		desc: "Every key that can reach this proxy. A person's key can use only what that person may use. An engine key is a plain bearer key that skips people, virtual models and budgets.",
		actions: [search("Find a key, person or label", (v) => { query = v.trim().toLowerCase(); paint(); }, { "aria-label": "Find a key" }), mintBtn],
	});
	head.classList.add("ax-head");
	root.append(
		head,
		card({ title: "People's keys", sub: peopleCount, flush: true, body: peopleHost }),
		h("div", { class: "ak-pair" },
			card({
				title: "Engine keys",
				sub: engineCount,
				actions: [btn("Add engine key", { icon: "plus", cls: "outline sm", onClick: addEngineKey })],
				flush: true,
				body: engineHost,
			}),
			card({ cls: "ak-strip", title: "Where clients send them", sub: h("span", null, "As Authorization: Bearer <key>, or x-api-key for Anthropic clients. Step-by-step snippets are on ", h("a", { class: "link-text", href: "#/connect" }, "Connect"), "."), body: h("div", { class: "ak-addr-row" },
				[["OpenAI", "/v1"], ["Anthropic", "/v1/messages"], ["Gemini", "/v1beta"], ["Responses", "/v1/responses"]].map(([label, path]) => h("span", { class: "ak-addr-item" }, h("span", { class: "ak-addr-k" }, label), copyLine(`${location.origin}${path}`, `${label} address`)))) })),
	);
	peopleHost.append(spinner());
	engineHost.append(spinner());

	// ---------- people's keys ----------
	function pickPerson() {
		if (!vault) return;
		const people = users.filter((u) => u.state !== "suspended");
		picker(mintBtn, {
			placeholder: "Mint a key for…",
			items: people.map((u) => ({ value: u.id, label: u.handle, sub: [u.name, u.role, plural(u.keys.length, "key")].filter(Boolean).join(" · "), lead: avatar(u, "sm") })),
			empty: "Nobody matches",
			onPick: (id) => {
				const user = users.find((u) => u.id === id);
				mintKeyDialog({ user, accounts: drawableAccounts(user, vault.accounts), providers: vault.providers, models: directModelsFor(user, models), onMinted: () => loadPeople() });
			},
		});
	}

	async function revoke(k) {
		if (!(await confirm({ title: `Revoke ${k.label || k.prefix}?`, text: `Anything using this key of ${k.user.handle}'s stops working now. This cannot be undone.`, confirmLabel: "Revoke key", danger: true }))) return;
		try { await neo.revokeKey(k.user.id, k.id); toast("Key revoked"); await loadPeople(); }
		catch (e) { toast(e.message, "bad"); }
	}

	const keyText = (k) => [k.user.handle, k.user.name, k.label, k.prefix, ...(k.accountIds ?? []).map((id) => vault.accounts.find((a) => a.id === id)?.label)].filter(Boolean).join("\n").toLowerCase();
	function paintPeople() {
		if (!vault) return;
		const now = Date.now();
		const all = users.flatMap((u) => u.keys.map((k) => ({ ...k, user: u })));
		// Expired keys first (they need a person to revoke or replace), then the most recently used.
		all.sort((a, b) => Number((b.expiresAt ?? Infinity) <= now) - Number((a.expiresAt ?? Infinity) <= now) || (b.lastUsedAt ?? 0) - (a.lastUsedAt ?? 0) || b.createdAt - a.createdAt);
		const rows = query ? all.filter((k) => keyText(k).includes(query)) : all;
		const people = new Set(all.map((k) => k.user.id)).size;
		peopleCount.textContent = all.length ? `${plural(all.length, "key")} held by ${plural(people, "person", "people")}. A row opens its person.` : "Minted per person; the key itself is shown once.";
		peopleHost.replaceChildren(rows.length
			? keysTable({ keys: rows, accounts: vault.accounts, providers: vault.providers, whoOf: (k) => k.user, onOpen: (k) => ctx.navigate(`users/${k.user.id}`), onRevoke: revoke })
			: all.length
				? empty({ icon: "search", title: "No keys match", hint: "Search by person, label, the start of the key or an account." })
				: empty({ icon: "key", title: "No keys issued yet", hint: "Mint one for a person; it reaches only what they may use.", action: btn("Mint a key", { icon: "key", cls: "outline sm", onClick: () => { mintBtn.focus(); pickPerson(); } }) }));
	}

	async function loadPeople() {
		try {
			[users, vault, models] = await Promise.all([neo.users(), neo.vault(), neo.models().catch(() => models)]);
			if (!disposed) paintPeople();
		} catch (error) { peopleHost.replaceChildren(errorState(error, loadPeople)); }
	}

	// ---------- engine keys ----------
	async function saveEngine(next, message) {
		await engine.put("config/access/api-keys", next);
		invalidate("config");
		engineKeys = (await engine.config()).access?.["api-keys"] ?? [];
		toast(message, "ok");
		if (!disposed) paintEngine();
	}
	function addEngineKey() {
		const control = input({ mono: true, placeholder: "The bearer key clients will send", autocomplete: "off", spellcheck: "false" });
		const err = h("div", { class: "error", role: "alert" });
		const submit = async (event) => {
			const value = control.value.trim();
			err.textContent = "";
			if (!value) { err.textContent = "Type or paste the key first."; control.focus(); return; }
			if (engineKeys.includes(value)) { err.textContent = "That key is already listed."; return; }
			const button = event?.currentTarget ?? null;
			if (button) button.disabled = true;
			try { await saveEngine([...engineKeys, value], "Engine key added"); activeDialog?.close(); }
			catch (error) { err.textContent = `Couldn't add it: ${error.message}`; }
			finally { if (button) button.disabled = false; }
		};
		control.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); submit(); } });
		activeDialog = dialog({
			title: "Add an engine key",
			sub: "Clients send it straight to the engine. It is not tied to a person, so no grants, limits or budgets apply. Prefer a person's key when you can.",
			body: [field("Key", control, "Any long random string; NeoProxy compares it exactly."), err],
			actions: (close) => [btn("Cancel", { onClick: close }), btn("Add key", { cls: "primary", onClick: submit })],
		});
	}
	function paintEngine() {
		const rows = query ? engineKeys.filter((key) => key.toLowerCase().includes(query)) : engineKeys;
		engineCount.textContent = engineKeys.length ? `${plural(engineKeys.length, "key")}. They skip people, virtual models and budgets.` : "Only for clients that cannot use a person's key.";
		engineHost.replaceChildren(rows.length
			? table([
				{ key: "key", label: "Key", render: (key) => h("code", { class: "cell-main" }, mask(key)) },
				{ key: "x", label: "", width: "88px", render: (key) => h("span", { class: "row ak-acts" },
					iconBtn("copy", "Copy key", async () => { const copied = await copyText(key); toast(copied ? "Copied" : "Clipboard unavailable", copied ? "ok" : "warn"); }),
					iconBtn("trash", "Delete key", async () => {
						if (!(await confirm({ title: "Delete this engine key?", text: "Clients using it lose access to the engine at once.", confirmLabel: "Delete key", danger: true }))) return;
						try { await saveEngine(engineKeys.filter((value) => value !== key), "Engine key deleted"); }
						catch (error) { toast(`Delete failed: ${error.message}`, "bad"); }
					}, "danger")) },
			], rows, { keyOf: (key) => key, stack: true })
			: h("div", { class: "ak-none" }, engineKeys.length ? "No engine keys match." : "No engine keys. That is the safer default."));
	}

	function paint() { paintPeople(); paintEngine(); }

	loadPeople();
	try {
		const config = await engine.config();
		engineKeys = config.access?.["api-keys"] ?? [];
		if (!disposed) paintEngine();
	} catch (error) { engineHost.replaceChildren(errorState(error, () => ctx.rerender())); }
	const off = onChange((what) => { if (what === "users" || what === "vault") loadPeople(); });
	return () => { disposed = true; off(); activeDialog?.close(); };
}
