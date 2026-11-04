// The optional Lua pool policy of a virtual model (admins only). It stays shut
// unless the author opens it: the card says whether a script exists and what it
// defines, and the editor only appears on request.

import { h } from "../../lib/dom.js";
import { ApiError } from "../../lib/client.js";
import { card, btn, confirm, toast } from "../../lib/components.js";
import { luaEditor, luaFunctions, LUA_EXAMPLE } from "../../lib/lua-editor.js";

const scriptHint = "Up to 16 KiB. Saving checks the syntax. There are no files, network, modules or credentials, and errors fail closed: nothing is routed around a broken script.";

export const isScriptError = (e) => e instanceof ApiError && (["neoproxy_script", "neoproxy_script_error"].includes(e.body?.error?.code) || /\b(?:script|Lua)\b/i.test(e.message));

// env: { draft, vault, models, touch(), luaChanged() }
export function luaPolicy(env) {
	const { draft } = env;
	let open = false;
	let scriptError = null;
	let knownFns = "";
	let timer = 0;
	const host = h("div", { class: "vm-lua" });
	const status = h("div", { class: "hint", id: "vm-lua-hint", style: { marginTop: "8px" } }, scriptHint);
	const fnList = h("div", { class: "vm-fns" });
	const toggle = btn("Open editor", { cls: "outline sm", onClick: () => { open = !open; paint(); if (open) api.area.focus({ preventScroll: true }); } });
	toggle.setAttribute("aria-controls", "vm-lua-body");
	host.id = "vm-lua-body";

	const names = () => luaFunctions(draft.script ?? "");
	const refreshFns = () => {
		const list = names();
		const key = list.join(",");
		fnList.replaceChildren(...(list.length
			? ["Defines ", ...list.map((n) => h("code", null, n)), ". Pick them under a model's “Accounts, limits and rules”."]
			: [draft.script?.trim() ? "No functions yet. Define them as " : "No script. Define functions as ", h("code", null, "function name(ctx)"), " to use them from a model."]));
		if (key !== knownFns) {
			knownFns = key;
			env.luaChanged();
		}
	};
	function scriptMessage(message) {
		let readable = String(message);
		draft.stages.forEach((stage, index) => {
			const name = env.models.find((model) => model.id === stage.model)?.display ?? "Unavailable model";
			readable = readable.replaceAll(`stage "${stage.id}"`, `Model ${index + 1} (${name})`);
		});
		return readable;
	}
	function setError(message) {
		scriptError = message;
		api.area.setAttribute("aria-invalid", String(Boolean(message)));
		status.className = message ? "error" : "hint";
		status.textContent = message ? scriptMessage(message) : scriptHint;
		status.title = message || "";
	}
	function paint() {
		host.classList.toggle("hidden", !open);
		toggle.textContent = open ? "Hide editor" : draft.script?.trim() ? "Open editor" : "Write a script";
		toggle.setAttribute("aria-expanded", String(open));
		el.classList.toggle("open", open);
	}
	const api = luaEditor({
		value: draft.script ?? "",
		label: "Lua pool policy",
		describedBy: "vm-lua-hint",
		placeholder: "-- Optional. Leave empty to use the sliders in each model's limits.",
		onInput: (value) => {
			draft.script = value;
			if (scriptError) setError(null);
			env.touch();
			clearTimeout(timer);
			timer = setTimeout(refreshFns, 250);
			paint();
		},
	});
	async function startFromExample() {
		if (api.area.value.trim() && !(await confirm({ title: "Replace your script?", text: "The example takes the place of the Lua source in the editor. Nothing is saved until you save the model.", confirmLabel: "Replace with the example", danger: true }))) return;
		api.setValue(LUA_EXAMPLE);
		api.area.scrollTop = 0;
		openEditor({ focus: false });
		api.el.scrollIntoView({ block: "center", behavior: "smooth" });
		toast("Example added. Pick its functions under a model's accounts and limits.");
	}
	function openEditor({ focus = true } = {}) {
		open = true;
		paint();
		if (focus) {
			api.area.focus({ preventScroll: true });
			api.el.scrollIntoView({ block: "center", behavior: "smooth" });
		}
	}
	host.append(
		api.el,
		status,
		h("p", { class: "hint", style: { margin: "4px 0 0" } }, "Tab inserts two spaces. Press Esc to leave the editor."),
		h("details", { class: "vm-contract" },
			h("summary", null, "What each function receives and returns"),
			h("div", { class: "vm-contract-body" },
				h("p", null, h("code", null, "function eligible(ctx)"), " returns ", h("code", null, "true"), " or ", h("code", null, "false"), ". ", h("code", null, "function per_account(ctx)"), " and ", h("code", null, "function cap(ctx)"), " return a fraction from 0 to 1: ", h("code", null, "0.25"), " means 25%, not 25 points. You choose the names; each model picks the ones it uses."),
				h("p", null, "Lua can only make a model stricter. Its own thresholds, each person's allotment, account health and token reservations still apply. ", h("code", null, "ctx.account"), ", ", h("code", null, "ctx.window"), ", ", h("code", null, "ctx.used"), " and ", h("code", null, "ctx.budget"), " exist for the two account functions only; ", h("code", null, "cap"), " is asked once per model. Everything on ", h("code", null, "ctx"), " is read-only."),
				h("p", null, "Provider meters are in ", h("code", null, "ctx.account.providerWindows"), " and local token meters in ", h("code", null, "ctx.account.windows"), ". Check ", h("code", null, "known"), " before using ", h("code", null, "used"), "; kinds can be namespaced, like ", h("code", null, "zai:tokens:5h"), "."),
			),
		),
		(() => {
			const kinds = [...new Set(env.vault.accounts.flatMap((account) => (account.providerWindows ?? []).map((window) => window.kind)))].sort();
			return kinds.length ? h("p", { class: "hint" }, `Meter kinds your accounts report right now: ${kinds.join(", ")}`) : null;
		})(),
	);
	const el = card({
		title: "Lua policy",
		sub: "Optional, for rules the controls above can't express, like skipping an account while its 5h window runs hot.",
		actions: [btn("Example", { cls: "ghost sm", title: "Start from an example script", onClick: startFromExample }), toggle],
		body: [fnList, host],
		cls: "vm-lua-card",
	});
	paint();
	refreshFns();
	return {
		el,
		names,
		open: openEditor,
		setError,
		scriptMessage,
		dispose: () => clearTimeout(timer),
	};
}
