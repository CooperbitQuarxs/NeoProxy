import { h, mask, copyText } from "../lib/dom.js";
import { engine, neo, invalidate } from "../lib/client.js";
import { pageHead, card, btn, iconBtn, table, empty, kv, copyLine, field, input, dialog, confirm, toast, spinner, errorState, search, avatar } from "../lib/components.js";
import { keysTable } from "./users.js";

export async function render(ctx) {
	const { root } = ctx;
	let keys = [], query = "", disposed = false, activeDialog;
	const host = h("div");
	async function save(next, message) {
		await engine.put("config/access/api-keys", next);
		invalidate("config");
		keys = (await engine.config()).access?.["api-keys"] ?? [];
		toast(message, "ok");
		if (!disposed) renderTable();
	}
	function add() {
		const control = input({ mono: true, placeholder: "Enter a bearer key", autocomplete: "off", spellcheck: "false" });
		activeDialog = dialog({ title: "Add key", sub: "Clients send this bearer key directly to the engine.", body: field("Key", control),
			actions: (close) => [btn("Cancel", { onClick: close }), btn("Add key", { cls: "primary", onClick: async (event) => {
				const value = control.value.trim();
				if (!value) { control.focus(); return; }
				if (keys.includes(value)) { toast("That key already exists", "warn"); return; }
				const button = event.currentTarget;
				button.disabled = true;
				try { await save([...keys, value], "Added"); close(); }
				catch (error) { toast(`Add failed: ${error.message}`, "bad"); }
				finally { button.disabled = false; }
			} })] });
	}
	const addButton = () => btn("Add engine key", { icon: "plus", cls: "primary", onClick: add });
	const peopleHost = h("div");
	root.append(pageHead({ title: "Access keys", desc: "Two kinds of key reach this proxy. A person's key is minted on their page and can only use what that person may use. An engine key is a plain bearer key that skips users, virtual models and budgets." }),
		card({ title: "Keys issued to people", sub: "Mint and revoke on each person's page. A key past its expiry no longer works; it stays listed until revoked.", flush: true, body: peopleHost }),
		h("div", { class: "row", style: { margin: "22px 0 10px" } }, h("h2", { style: { margin: 0, fontSize: "15px" } }, "Engine keys"), h("span", { class: "spacer" }), search("Search engine keys", (value) => { query = value.toLowerCase(); renderTable(); }), addButton()),
		card({ flush: true, body: host }),
		card({ title: "Where these are used", body: kv([
			["OpenAI", copyLine(`${location.origin}/v1`, "OpenAI endpoint")],
			["Claude", copyLine(`${location.origin}/v1/messages`, "Claude endpoint")],
			["Gemini", copyLine(`${location.origin}/v1beta`, "Gemini endpoint")],
			["Responses", copyLine(`${location.origin}/v1/responses`, "Responses endpoint")],
		]) }));
	function renderTable() {
		host.replaceChildren(table([
			{ key: "key", label: "Key", render: (key) => h("code", null, mask(key)) },
			{ key: "actions", label: "", width: "100px", render: (key) => h("span", { class: "row", style: { justifyContent: "flex-end" } },
				iconBtn("copy", "Copy key", async () => { const copied = await copyText(key); toast(copied ? "Copied" : "Clipboard unavailable", copied ? "ok" : "warn"); }),
				iconBtn("trash", "Delete key", async () => {
					if (!(await confirm({ title: "Delete this key?", text: "Clients using this key will lose access to the engine.", confirmLabel: "Delete", danger: true }))) return;
					try { await save(keys.filter((value) => value !== key), "Deleted key"); }
					catch (error) { toast(`Delete failed: ${error.message}`, "bad"); }
				}, "danger")) },
		], keys.filter((key) => key.toLowerCase().includes(query)), { keyOf: (key) => key, emptyState: keys.length
			? empty({ icon: "search", title: "No matching keys", hint: "Try a different search." })
			: empty({ icon: "key", title: "No engine keys", hint: "Add one only for clients that cannot use a person's key." }) }));
	}
	async function renderPeople() {
		try {
			const [users, vault] = await Promise.all([neo.users(), neo.vault()]);
			const rows = users.flatMap((u) => u.keys.map((k) => ({ ...k, user: u })));
			peopleHost.replaceChildren(rows.length ? keysTable({
				keys: rows.sort((a, b) => (a.expiresAt ?? Infinity) - (b.expiresAt ?? Infinity)),
				accounts: vault.accounts, providers: vault.providers, whoOf: (k) => k.user, onOpen: (k) => ctx.navigate(`users/${k.user.id}`),
			}) : empty({ icon: "key", title: "No keys issued yet", hint: "Open a person on the Users page and mint one." }));
		} catch (error) { peopleHost.replaceChildren(errorState(error, renderPeople)); }
	}
	peopleHost.append(spinner());
	renderPeople();
	host.append(spinner());
	try {
		// Usage here describes upstream provider keys, not incoming bearer keys.
		// It cannot be attributed to this table's access.api-keys, so no usage column.
		const [config] = await Promise.all([engine.config(), engine.usage()]);
		keys = config.access?.["api-keys"] ?? [];
		renderTable();
	} catch (error) { host.replaceChildren(errorState(error, () => ctx.rerender())); }
	return () => { disposed = true; activeDialog?.close(); };
}
