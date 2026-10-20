import { h, card, body, badge, btn, stat, toast, toggle, field, kv } from "../ui.js";
import { pageHead } from "./shared.js";

const STRATEGIES = [
	{ value: "round-robin", label: "Round robin", blurb: "Each request goes to the next healthy credential." },
	{ value: "weighted-round-robin", label: "Weighted round robin", blurb: "Like round robin, honouring per-credential weights." },
	{ value: "fill-first", label: "Fill first", blurb: "Use the highest-priority credential until it cools down, then the next." },
];

export const routing = {
	id: "routing",
	title: "Routing",
	icon: "route",
	key: "r",
	async render({ load, api, invalidate, reload }) {
		const config = await load.config();
		const r = config?.routing ?? {};
		const retry = r.retry ?? {};
		const cooldown = r.cooldown ?? {};
		const patch = async (obj, msg) => {
			try {
				await api.patch("config", { routing: obj });
				invalidate("config");
				toast(msg, "ok");
				reload();
			} catch (ex) {
				toast(`Save failed: ${ex.message}`, "bad");
			}
		};
		const num = (label, key, value, hint) => {
			const input = h("input", { class: "input mono", type: "number", min: 0, value: value ?? 0, style: { width: "120px" } });
			input.addEventListener("change", () => patch({ retry: { [key]: Number(input.value) } }, `${label} saved`));
			return field(label, input, hint);
		};
		const strategyCards = h(
			"div",
			{ class: "tiles" },
			STRATEGIES.map((s) =>
				h(
					"button",
					{ class: "tile", type: "button", "aria-pressed": (r.strategy || "round-robin") === s.value ? "true" : "false", style: (r.strategy || "round-robin") === s.value ? { borderColor: "var(--accent-line)", background: "var(--raised)" } : null, onClick: () => patch({ strategy: s.value }, `Strategy: ${s.label}`) },
					h("div", { class: "tile-head" }, h("span", { class: `dot ${(r.strategy || "round-robin") === s.value ? "accent" : ""}` }), s.label),
					h("div", { class: "tile-sub" }, s.blurb),
				),
			),
		);

		return [
			pageHead("Routing", "How a request picks a credential, and what happens when one fails."),
			h(
				"div",
				{ class: "stats" },
				stat({ label: "Strategy", value: r.strategy || "round-robin", mono: true }),
				stat({ label: "Retries", value: String(retry["request-retry"] ?? 0), sub: `across ${retry["max-retry-credentials"] || "all"} credentials` }),
				stat({ label: "Session affinity", value: r["session-affinity"] ? "on" : "off", mono: true, sub: r["session-affinity"] ? `TTL ${r["session-affinity-ttl"] || "1h"}` : "requests may hop credentials" }),
				stat({ label: "Cooling", value: cooldown["disable-cooling"] ? "off" : "on", mono: true, sub: cooldown["disable-cooling"] ? "failed credentials are retried immediately" : "failed credentials rest before reuse" }),
			),
			card({ title: "Strategy", sub: "Applied to every provider that has more than one credential.", body: body(strategyCards) }),
			h(
				"div",
				{ class: "grid cols-2-even" },
				card({
					title: "Retries",
					sub: "A failed upstream call can be retried on another credential before the client sees an error.",
					body: body(h("div", { class: "stack", style: { gap: "14px" } }, num("Request retries", "request-retry", retry["request-retry"], "0 disables retries."), num("Max credentials per request", "max-retry-credentials", retry["max-retry-credentials"], "0 means every available credential."), num("Max retry interval (s)", "max-retry-interval", retry["max-retry-interval"]))),
				}),
				card({
					title: "Behaviour",
					body: body(
						h(
							"div",
							{ class: "stack", style: { gap: "12px" } },
							toggle(r["session-affinity"], (v) => patch({ "session-affinity": v }, v ? "Session affinity on" : "Session affinity off"), "Session affinity: keep a conversation on one credential"),
							toggle(r["force-model-prefix"], (v) => patch({ "force-model-prefix": v }, v ? "Model prefix required" : "Model prefix optional"), "Require provider prefix on model names"),
							toggle(!cooldown["disable-cooling"], (v) => patch({ cooldown: { "disable-cooling": !v } }, v ? "Cooling on" : "Cooling off"), "Cool down credentials after failures"),
							toggle(cooldown["save-cooldown-status"], (v) => patch({ cooldown: { "save-cooldown-status": v } }, "Saved"), "Persist cooldown state across restarts"),
						),
					),
					foot: [h("span", null, "Quota fail-over (switch project, preview model) and model aliases live in the configuration."), btn("Configuration", { cls: "sm", icon: "arrowRight", href: "#/configuration" })],
				}),
			),
		];
	},
};
