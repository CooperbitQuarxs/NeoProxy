// What happens when no model of the virtual model can serve a request: a limit
// error, a wait for a window to reset, or a fall-through to a model without a budget.

import { h } from "../../lib/dom.js";
import { field, seg, modelPicker } from "../../lib/components.js";

const KINDS = [
	{ value: "limit", label: "Limit error" },
	{ value: "queue", label: "Queue" },
	{ value: "passthrough", label: "Fall through" },
];

// env: { draft, models, vault, touch() }
export function terminal(env) {
	const t = env.draft.terminal;
	const sum = h("span", { class: "vm-end-line" });
	const describe = () => {
		sum.textContent = t.kind === "queue" ? "the request waits for a window to reset" : t.kind === "passthrough" ? `falls through to ${t.model ? env.models.find((m) => m.id === t.model)?.display ?? t.model : "a model you pick"}` : `HTTP 429${t.message ? `: “${t.message.length > 48 ? `${t.message.slice(0, 48)}…` : t.message}”` : ""}`;
	};
	const passField = h("div", { class: t.kind === "passthrough" ? "" : "hidden" });
	const renderPass = () => {
		passField.replaceChildren(field("Fall through to", modelPicker({
			providers: env.vault.providers,
			models: env.models.filter((m) => !m.virtualOnly),
			accounts: env.vault.accounts,
			value: t.model ?? null,
			onPick: (m) => { t.model = m.id; describe(); env.touch(); },
		}), "Served without a budget or a separate direct-access grant. Source-backed models require a metered stage and are not offered here."));
	};
	renderPass();
	describe();
	const kindSeg = seg(KINDS, t.kind, (v) => { t.kind = v; passField.classList.toggle("hidden", v !== "passthrough"); describe(); env.touch(); });
	kindSeg.setAttribute("aria-label", "When no model can serve");
	const el = h("details", { class: "card vm-end rise" },
		h("summary", null, h("span", { class: "vm-end-title" }, "If no model can serve"), sum),
		h("div", { class: "vm-end-body" },
			kindSeg,
			h("p", { class: "ink-2", style: { fontSize: "12.5px", margin: "8px 0 12px" } }, "A limit error is honest and immediate: HTTP 429 with your message. Queueing holds the request until a window resets, which suits batch callers. Falling through keeps serving from a model with no budget at all."),
			field("Message to the caller", h("textarea", { class: "textarea", style: { minHeight: "72px" }, placeholder: "Your heavy allotment for this week is used up.", onInput: (e) => { t.message = e.target.value; describe(); env.touch(); } }, t.message ?? ""), "Returned in the error body so the user knows why, and when to try again."),
			passField,
		),
	);
	return { el, describe };
}
