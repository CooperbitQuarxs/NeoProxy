import { h, fmtAgo, fmtInt, mask } from "../lib/dom.js";
import { engine, invalidate } from "../lib/client.js";
import { pageHead, card, btn, badge, status, search, seg, table, empty, kv, copyLine, inspector, section, toast, dialog, confirm, spinner, errorState, field, input, select } from "../lib/components.js";

const providers = ["codex", "anthropic", "gemini", "antigravity", "qwen", "iflow", "kimi", "devin", "xai"];
const nameOf = (entry) => entry.name || entry.id;

function stateOf(entry) {
	if (entry.disabled || entry.status === "disabled") return { id: "disabled", kind: "", label: "Disabled", text: "This credential is excluded from routing until you enable it." };
	if (entry.status === "expired" || /token expired/i.test(entry.status_message || "")) return { id: "expired", kind: "bad", label: "Expired", text: "The token has expired. Refresh it or sign in again to use this credential." };
	if (entry.unavailable || ["error", "invalid", "cooling", "cooldown"].includes(entry.status) || entry.next_retry_after) return { id: "error", kind: "bad", label: "Error", text: "The engine cannot use this credential right now. Check its status details before refreshing it." };
	return { id: "healthy", kind: "ok", label: "Healthy", text: "This credential is enabled and available to serve requests." };
}

// The engine returns the whole auth record. Show what an admin acts on, in
// plain words; everything secret-looking stays out.
function detailRows(entry) {
	const cooldowns = Array.isArray(entry.cooldowns) ? entry.cooldowns.length : 0;
	const file = typeof entry.path === "string" ? entry.path.split("/").pop() : null;
	return [
		["Provider", entry.provider || entry.type || "–"],
		["Account", entry.email || (entry.account_type === "api_key" ? mask(entry.account) : entry.account) || "–"],
		["Type", entry.account_type === "api_key" ? "API key" : entry.account_type === "oauth" ? "OAuth login" : entry.account_type || "–"],
		file ? ["File", h("code", { title: entry.path }, file)] : null,
		entry.runtime_only ? ["Source", "runtime only (not on disk)"] : null,
		entry.created_at ? ["Created", fmtAgo(entry.created_at)] : null,
		entry.modtime ? ["Modified", fmtAgo(entry.modtime)] : null,
		entry.last_refresh ? ["Last refreshed", fmtAgo(entry.last_refresh)] : null,
		entry.expired ? ["Expires", entry.expired] : null,
		["Failures", fmtInt(entry.failed ?? 0)],
		["Cooldowns", cooldowns ? `${cooldowns} active` : "none"],
		entry.next_retry_after ? ["Next retry", fmtAgo(entry.next_retry_after)] : null,
		entry.siwc_bind_supported ? ["Sign-in-with-Claude", entry.siwc_bound ? "bound" : "not bound"] : null,
		entry.auth_index ? ["Index", h("code", null, String(entry.auth_index))] : null,
	];
}

export async function render(ctx) {
	const { root } = ctx;
	let entries = [], models = new Map(), selected = ctx.params[0] ?? null, query = "", filter = "all", disposed = false, activeDialog;
	const add = () => {
		activeDialog?.close();
		activeDialog = addCredential(reload);
	};
	const addButton = () => btn("Add credential", { icon: "plus", cls: "primary", onClick: add });
	const refreshAll = btn("Refresh all", { icon: "refresh", cls: "outline", onClick: async () => {
		refreshAll.disabled = true;
		try {
			const response = await engine.post("credentials/refresh", { all: true });
			const results = response.results ?? [];
			const refreshed = results.filter((result) => result.success).length;
			const failed = results.filter((result) => !result.success).length;
			toast(`Refreshed ${fmtInt(refreshed)} credentials; ${fmtInt(failed)} failed`, failed ? "warn" : "ok");
			await reload();
		} catch (error) { toast(`Refresh failed: ${error.message}`, "bad"); }
		finally { refreshAll.disabled = false; }
	} });
	root.append(pageHead({ title: "Credentials", desc: "The engine's own auth files under auths/. Kept for proxies that run without a NeoPi core; while a core is linked these are the fallback, not the source.", actions: [refreshAll, addButton()] }));
	const host = h("div");
	root.append(h("div", { class: "row wrap", style: { marginBottom: "14px" } },
		search("Search name, email or provider", (value) => { query = value.toLowerCase(); renderTable(); }),
		seg([{ value: "all", label: "All" }, { value: "healthy", label: "Healthy" }, { value: "disabled", label: "Disabled" }, { value: "failed", label: "Expired or error" }], filter, (value) => { filter = value; renderTable(); })),
		card({ flush: true, body: host }));

	function close() {
		selected = null;
		ctx.setInspector(null);
		renderTable();
	}
	function renderTable() {
		const rows = entries.filter((entry) => {
			const state = stateOf(entry).id;
			return (filter === "all" || filter === state || filter === "failed" && ["expired", "error"].includes(state)) && (!query || [entry.name, entry.id, entry.email, entry.account, entry.label, entry.provider].some((value) => String(value ?? "").toLowerCase().includes(query)));
		});
		host.replaceChildren(table([
			{ key: "provider", label: "Provider", render: (entry) => badge(entry.provider || entry.type || "unknown"), sortBy: (entry) => entry.provider || "" },
			{ key: "name", label: "Name", clip: true, width: "26%", render: (entry) => h("code", { title: nameOf(entry) }, nameOf(entry)), sortBy: nameOf },
			{ key: "account", label: "Account/email", clip: true, render: (entry) => h("span", { class: "ink-2" }, entry.email || (entry.account_type === "api_key" ? mask(entry.account) : entry.account) || "–") },
			{ key: "state", label: "State", render: (entry) => { const state = stateOf(entry); return status(state.kind, state.label); } },
			{ key: "refresh", label: "Last refreshed", num: true, render: (entry) => fmtAgo(entry.last_refresh), sortBy: (entry) => new Date(entry.last_refresh || 0).getTime() },
			{ key: "models", label: "Models served", num: true, render: (entry) => fmtInt(models.get(nameOf(entry))?.models?.length) },
		], rows, { onRow: open, keyOf: nameOf, selectedKey: selected, emptyState: entries.length
			? empty({ icon: "search", title: "No matching credentials", hint: "Try another name, email or state." })
			: empty({ icon: "shield", title: "No local credential files", hint: "Sign in to a provider or upload an auth file. With a NeoPi core linked you usually do not need any.", action: addButton() }) }));
	}
	async function act(entry, operation, message) {
		try { await operation(); toast(message, "ok"); await reload(); }
		catch (error) { toast(error.message, "bad"); }
	}
	function open(entry) {
		selected = nameOf(entry);
		renderTable();
		const state = stateOf(entry);
		const modelData = models.get(selected);
		ctx.setInspector(inspector({ title: selected, sub: entry.provider || entry.type, onClose: close,
			body: [section("State", status(state.kind, state.label), h("p", { class: "ink-2" }, state.text)),
				section("Details", kv(detailRows(entry))),
				modelData?.models ? section("Models", modelData.models.length ? h("div", { class: "list" }, modelData.models.map((model) => h("div", { class: "li" }, h("code", { style: { overflow: "auto", whiteSpace: "nowrap" } }, model.id)))) : h("p", { class: "ink-2" }, "No models are registered for this credential.")) : null,
				modelData?.error ? section("Models", errorState(modelData.error, reload)) : null],
			foot: h("div", { class: "row wrap" }, btn("Refresh token", { icon: "refresh", cls: "outline", onClick: () => act(entry, () => engine.post("credentials/refresh", { name: entry.name, auth_index: entry.auth_index }), `Refreshed ${nameOf(entry)}`) }),
				btn(entry.disabled ? "Enable" : "Disable", { icon: "power", cls: "outline", onClick: () => act(entry, () => engine.patch("credentials/status", { name: entry.name, auth_index: entry.auth_index, disabled: !entry.disabled }), `${entry.disabled ? "Enabled" : "Disabled"} ${nameOf(entry)}`) }),
				!entry.runtime_only ? btn("Download", { icon: "download", cls: "outline", onClick: () => act(entry, async () => {
					const response = await engine.raw("GET", "credentials/download", { query: { name: entry.name } });
					if (!response.ok) throw new Error("Download failed");
					const url = URL.createObjectURL(await response.blob());
					const link = h("a", { href: url, download: entry.name });
					document.body.append(link); link.click(); link.remove();
					setTimeout(() => URL.revokeObjectURL(url), 1000);
				}, `Downloaded ${nameOf(entry)}`) }) : null,
				!entry.runtime_only ? btn("Delete", { icon: "trash", cls: "danger", onClick: async () => {
					if (await confirm({ title: `Delete ${nameOf(entry)}?`, text: "This removes the local auth file. Sign in again or upload a file to restore it.", confirmLabel: "Delete", danger: true })) await act(entry, () => engine.del("credentials", { query: { name: entry.name } }), `Deleted ${nameOf(entry)}`);
				} }) : null) }));
	}
	async function reload() {
		invalidate("credentials");
		try {
			const data = await engine.credentials();
			const next = data.files ?? [];
			const nextModels = await Promise.all(next.map(async (entry) => {
				try { return [nameOf(entry), await engine.get("credentials/models", { query: { name: nameOf(entry) } })]; }
				catch (error) { return [nameOf(entry), { error }]; }
			}));
			if (disposed) return;
			entries = next; models = new Map(nextModels);
			renderTable();
			if (selected) { const entry = entries.find((item) => nameOf(item) === selected); if (entry) open(entry); else close(); }
		} catch (error) { if (!disposed) host.replaceChildren(errorState(error, reload)); }
	}
	host.append(spinner());
	await reload();
	const escape = (event) => { if (event.key === "Escape" && !document.querySelector("[role=dialog]")) close(); };
	document.addEventListener("keydown", escape);
	return () => { disposed = true; activeDialog?.close(); document.removeEventListener("keydown", escape); ctx.setInspector(null); };
}

function addCredential(reload) {
	let mode = "login", provider = "codex", poll = 0, timeout = 0, generation = 0, closed = false;
	const stop = () => { generation++; clearTimeout(poll); clearTimeout(timeout); };
	const result = h("div");
	const file = input({ type: "file", accept: ".json,application/json", multiple: true });
	const login = h("div", null, field("Provider", select(providers.map((name) => ({ value: name, label: name })), provider, (value) => { provider = value; stop(); result.replaceChildren(); })), result);
	const upload = h("div", { hidden: true }, field("Credential files", file, "Upload JSON auth files from a previous sign-in."));
	const chooser = seg([{ value: "login", label: "Sign in" }, { value: "upload", label: "Upload file" }], mode, (value) => { mode = value; login.hidden = value !== "login"; upload.hidden = value !== "upload"; stop(); result.replaceChildren(); });
	const dlg = dialog({ title: "Add credential", body: [field("Method", chooser), login, upload], onClose: () => { closed = true; stop(); },
		actions: (close) => [btn("Cancel", { onClick: close }), btn("Continue", { cls: "primary", onClick: async (event) => {
			const button = event.currentTarget;
			button.disabled = true;
			stop(); const current = generation;
			try {
				if (mode === "upload") {
					if (!file.files.length) { toast("Choose a file first", "warn"); return; }
					const body = new FormData();
					for (const item of file.files) body.append("file", item, item.name);
					const response = await engine.post("credentials", undefined, { body });
					const failed = response.failed?.length ?? 0;
					toast(failed ? `Uploaded ${fmtInt(response.uploaded ?? 0)} credential files; ${fmtInt(failed)} failed` : "Uploaded credential files", failed ? "warn" : "ok");
					close(); await reload(); return;
				}
				result.replaceChildren(spinner("Starting sign-in"));
				const response = await engine.get("oauth/auth-url", { query: { provider } });
				if (closed || current !== generation) return;
				if (!response.url || !response.state) throw new Error("The engine did not return a sign-in URL and state");
				const urlLine = copyLine(response.url, "sign-in URL");
				urlLine.querySelector("code").style.cssText = "overflow:auto;white-space:nowrap;min-width:0";
				result.replaceChildren(section("Sign in", urlLine, h("a", { class: "btn outline", href: response.url, target: "_blank", rel: "noopener noreferrer" }, "Open"), response.user_code ? field("Device code", copyLine(response.user_code, "device code")) : null, h("p", { class: "ink-2" }, "Complete sign-in in the provider's window. Waiting for up to three minutes.")));
				const fail = (message) => { stop(); result.replaceChildren(h("p", { class: "error" }, message)); toast(message, "bad"); };
				timeout = setTimeout(() => fail("Sign-in timed out. Start again to get a new link."), 180000);
				const check = async () => {
					try {
						const state = await engine.get("oauth/status", { query: { state: response.state } });
						if (closed || current !== generation) return;
						if (state.status === "ok") { toast(`Signed in to ${provider}`, "ok"); close(); await reload(); }
						else if (state.status === "error") fail(`Sign-in failed: ${state.error || "Provider rejected sign-in"}`);
						else poll = setTimeout(check, 2000);
					} catch (error) { if (!closed && current === generation) fail(`Sign-in failed: ${error.message}`); }
				};
				poll = setTimeout(check, 2000);
			} catch (error) { if (!closed && current === generation) { result.replaceChildren(h("p", { class: "error" }, error.message)); toast(error.message, "bad"); } }
			finally { button.disabled = false; }
		} })] });
	return dlg;
}
