import { h, fmtInt, fmtTime, mask } from "../lib/dom.js";
import { engine, invalidate, ApiError } from "../lib/client.js";
import { pageHead, card, btn, kv, section, toast, confirm, spinner, errorState } from "../lib/components.js";

function structuredValue(value, secret = false) {
	if (Array.isArray(value)) return h("code", null, value.map((item) => {
		const text = typeof item === "object" && item !== null ? JSON.stringify(redact(item)) : String(item);
		return secret ? mask(text) : text;
	}).join(", "));
	if (secret) return h("code", null, mask(typeof value === "string" ? value : JSON.stringify(value) ?? ""));
	if (value && typeof value === "object") return kv(Object.entries(value).map(([key, item]) => [h("code", null, key), structuredValue(item, /secret|token|key|password/i.test(key))]));
	return h("code", null, value === null ? "null" : String(value));
}

function redact(value) {
	if (Array.isArray(value)) return value.map(redact);
	if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, /secret|token|key|password/i.test(key) ? mask(typeof item === "string" ? item : JSON.stringify(item) ?? "") : redact(item)]));
	return value;
}

export async function render(ctx) {
	const { root } = ctx;
	let baseline = null;
	let loadedAt = null;
	let busy = false;
	let showing = false;
	let structuredLoad = 0;
	const error = h("div", { class: "inline-error hidden", id: "config-save-error", role: "alert" });
	const foot = h("span", { class: "mono" });
	const editor = h("textarea", {
		class: "textarea yaml-editor",
		spellcheck: "false",
		wrap: "off",
		autocapitalize: "off",
		autocomplete: "off",
		"aria-label": "config.yaml",
		"aria-describedby": "config-save-error",
		style: { minHeight: "60vh" },
		onInput: () => { update(); if (!error.classList.contains("hidden")) showError(null); },
		onKeydown: (e) => {
			if (e.key !== "Tab") return;
			e.preventDefault();
			editor.setRangeText("  ", editor.selectionStart, editor.selectionEnd, "end");
			update();
		},
	});
	const saveButton = btn("Save changes", { icon: "save", cls: "primary", disabled: true, onClick: save });
	const reloadButton = btn("Reload", { icon: "refresh", cls: "outline", onClick: () => reload() });
	root.append(pageHead({
		title: "Configuration",
		desc: "The engine's config.yaml, as it is on disk. Everything the dashboard changes elsewhere ends up here; edit by hand only for settings that have no page yet.",
		actions: [reloadButton, saveButton],
	}));
	const host = h("div");
	const loading = spinner();
	root.append(host, loading);
	const structuredHost = h("div");
	const showButton = btn("Show", { cls: "outline sm", onClick: () => {
		showing = !showing;
		showButton.textContent = showing ? "Hide" : "Show";
		structuredHost.parentElement.classList.toggle("hidden", !showing);
		if (showing) loadStructured();
	} });
	const structuredCard = card({ title: "Structured view", actions: [showButton], body: structuredHost });
	structuredHost.parentElement.classList.add("hidden");

	// Save errors stay on the page, above the editor, until the text changes or a save works.
	function showError(message) {
		if (message) editor.setAttribute("aria-invalid", "true");
		else editor.removeAttribute("aria-invalid");
		if (!message) { error.classList.add("hidden"); error.replaceChildren(); return; }
		const line = /\bline (\d+)/i.exec(message)?.[1];
		error.replaceChildren(
			h("div", { class: "inline-error-title" }, "config.yaml was not saved"),
			h("pre", { class: "inline-error-text" }, message),
			h("div", { class: "inline-error-foot" }, "The running config is unchanged and your edits are still in the editor.", line ? btn(`Go to line ${line}`, { cls: "outline sm", onClick: () => goToLine(Number(line)) }) : null),
		);
		error.classList.remove("hidden");
		error.scrollIntoView({ block: "nearest", behavior: "smooth" });
	}

	function goToLine(number) {
		const text = editor.value.split("\n");
		const start = text.slice(0, Math.max(0, number - 1)).reduce((sum, row) => sum + row.length + 1, 0);
		editor.focus();
		editor.setSelectionRange(start, start + (text[number - 1]?.length ?? 0));
		editor.scrollTop = Math.max(0, (number - 4) * parseFloat(getComputedStyle(editor).lineHeight || "19"));
	}

	function update() {
		saveButton.disabled = busy || baseline === null || editor.value === baseline;
		foot.textContent = `${fmtInt(editor.value.split("\n").length)} lines · ${fmtInt(new TextEncoder().encode(editor.value).length)} bytes · last loaded ${fmtTime(loadedAt)}`;
	}

	async function loadStructured() {
		const id = ++structuredLoad;
		structuredHost.replaceChildren(spinner());
		try {
			const config = await engine.config();
			if (id !== structuredLoad) return;
			structuredHost.replaceChildren(...Object.entries(config).map(([key, value]) => section(key, structuredValue(value, /secret|token|key|password/i.test(key)))));
		} catch (e) {
			if (id === structuredLoad) structuredHost.replaceChildren(errorState(e, loadStructured));
		}
	}

	async function reload(initial = false) {
		if (busy) return;
		if (baseline !== null && editor.value !== baseline && !(await confirm({ title: "Discard your edits?", text: "Reload config.yaml from disk without saving these changes.", confirmLabel: "Discard edits", danger: true }))) return;
		busy = true;
		update();
		try {
			const yaml = await engine.configYaml();
			editor.value = typeof yaml === "string" ? yaml : JSON.stringify(yaml, null, 2);
			baseline = editor.value;
			loadedAt = Date.now();
			showError(null);
			if (initial || !host.contains(editor)) host.replaceChildren(card({
				title: "config.yaml",
				sub: "The engine validates the file before it replaces the running config; if it refuses, the reason shows here and nothing changes.",
				body: [error, editor],
				foot,
			}), structuredCard);
			invalidate("config");
			if (showing) await loadStructured();
			if (!initial) toast("Reloaded config.yaml");
		} catch (e) {
			if (baseline === null) host.replaceChildren(errorState(e, () => reload(true)));
			else toast(e.message || "Could not reload config.yaml", "bad");
		} finally {
			loading.remove();
			busy = false;
			update();
		}
	}

	async function save() {
		if (saveButton.disabled) return;
		const text = editor.value;
		busy = true;
		update();
		showError(null);
		try {
			await engine.put("config.yaml", undefined, { body: text, headers: { "Content-Type": "application/yaml" } });
			invalidate("config");
			baseline = text;
			loadedAt = Date.now();
			toast("Saved config.yaml");
			if (showing) await loadStructured();
		} catch (e) {
			const body = e instanceof ApiError && e.body && typeof e.body === "object" ? e.body : null;
			const code = typeof body?.error === "string" ? body.error : "";
			// The engine sometimes answers with a bare code; say what it means, keep the code.
			const meaning = { invalid_body: "The engine could not parse this as YAML. Look for bad indentation, an unclosed bracket or quote, or a stray colon.", config_must_be_object: "The top level must be a mapping of settings, not a list or a single value.", invalid_yaml: "The engine could not parse this as YAML." }[code];
			const message = body?.message || e.message || "Could not save config.yaml";
			showError(body?.message ? message : meaning ? `${meaning}\n(${code})` : message);
		} finally {
			busy = false;
			update();
		}
	}

	await reload(true);
	const shortcut = (e) => {
		if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
			e.preventDefault();
			save();
		}
	};
	document.addEventListener("keydown", shortcut);
	return () => {
		structuredLoad++;
		document.removeEventListener("keydown", shortcut);
	};
}
