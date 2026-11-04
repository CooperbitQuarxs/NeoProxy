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
	const error = h("div", { class: "error hidden", role: "alert" });
	const foot = h("span", { class: "mono" });
	const editor = h("textarea", {
		class: "textarea yaml-editor",
		spellcheck: "false",
		wrap: "off",
		"aria-label": "config.yaml",
		style: { minHeight: "60vh" },
		onInput: () => update(),
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
			error.classList.add("hidden");
			if (initial || !host.contains(editor)) host.replaceChildren(card({
				title: "config.yaml",
				sub: "Saved files are validated by the engine before they replace the running config.",
				body: [editor, error],
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
		error.classList.add("hidden");
		try {
			await engine.put("config.yaml", undefined, { body: text, headers: { "Content-Type": "application/yaml" } });
			invalidate("config");
			baseline = text;
			loadedAt = Date.now();
			toast("Saved config.yaml");
			if (showing) await loadStructured();
		} catch (e) {
			const message = e instanceof ApiError ? e.body?.message || e.message : e.message || "Could not save config.yaml";
			toast(message, "bad");
			error.textContent = message;
			error.classList.remove("hidden");
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
