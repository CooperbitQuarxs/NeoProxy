import { h, card, body, badge, btn, stat, toast, confirm, kv } from "../ui.js";
import { pageHead, legacyLink } from "./shared.js";

export const configuration = {
	id: "configuration",
	title: "Configuration",
	icon: "settings",
	key: "s",
	async render({ load, api, invalidate, reload }) {
		const yaml = await load.configYaml();
		const text = typeof yaml === "string" ? yaml : JSON.stringify(yaml, null, 2);
		const original = text;
		const ta = h("textarea", { class: "textarea mono", spellcheck: "false", style: { minHeight: "60vh" } }, text);
		const dirty = h("span", { class: "badge", hidden: true }, "unsaved changes");
		ta.addEventListener("input", () => (dirty.hidden = ta.value === original));
		ta.addEventListener("keydown", (e) => {
			if (e.key === "Tab") {
				e.preventDefault();
				const s = ta.selectionStart;
				ta.setRangeText("  ", s, ta.selectionEnd, "end");
				dirty.hidden = ta.value === original;
			}
			if ((e.metaKey || e.ctrlKey) && e.key === "s") {
				e.preventDefault();
				save();
			}
		});
		const save = async () => {
			if (ta.value === original) return toast("Nothing changed");
			if (!(await confirm({ title: "Replace the configuration?", text: "The whole file is written as shown and the engine reloads it. A mistake can take the proxy down until fixed.", confirmLabel: "Save" }))) return;
			try {
				await api.put("config.yaml", undefined, { body: ta.value, headers: { "Content-Type": "application/yaml" } });
				invalidate();
				toast("Configuration saved", "ok");
				reload();
			} catch (ex) {
				toast(`Save rejected: ${ex.message}`, "bad");
			}
		};
		const lines = text.split("\n").length;
		return [
			pageHead("Configuration", "The whole engine configuration as YAML. Everything the pages above do not surface is here, including every upstream option.", [
				legacyLink("Legacy console"),
				btn("Revert", { icon: "x", cls: "outline", onClick: () => { ta.value = original; dirty.hidden = true; } }),
				btn("Save", { icon: "save", cls: "primary", onClick: save }),
			]),
			h(
				"div",
				{ class: "stats" },
				stat({ label: "Lines", value: String(lines) }),
				stat({ label: "Layout", value: "v8", mono: true, sub: "config-version 8" }),
				stat({ label: "Reload", value: "automatic", mono: true, sub: "the engine watches the file" }),
			),
			card({
				title: ["config.yaml", dirty],
				sub: "Tab inserts two spaces; Ctrl+S saves.",
				body: body(ta, "tight"),
				foot: h("span", null, "Comments are preserved on field-level writes but a full save replaces the document with what is shown here."),
			}),
		];
	},
};
