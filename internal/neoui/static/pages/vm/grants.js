// Who may ask for a virtual model, managed from the model itself. A grant lives on
// the user record (PUT /users/:id), so each change saves that one user at once.

import { h, fmtPct, clear } from "../../lib/dom.js";
import { neo } from "../../lib/client.js";
import { card, btn, iconBtn, avatar, input, picker, confirm, toast } from "../../lib/components.js";

const shareWords = (a) => (a >= 1 ? "Full share" : `${fmtPct(a, null)} share`);

// env: { draft, users, isNew }
export function grants(env) {
	const { draft, users } = env;
	const host = h("div", { class: "list" });

	async function saveGrant(u, allotment) {
		const next = u.grants.filter((g) => g.virtualModel !== draft.id);
		if (allotment !== null) next.push({ virtualModel: draft.id, allotment });
		const saved = await neo.saveUser({ ...u, grants: next, password: undefined });
		Object.assign(u, saved);
		paint();
	}

	function paint() {
		clear(host);
		if (env.isNew) {
			host.append(h("div", { class: "ink-3", style: { padding: "12px 16px" } }, "Save the model first; then grant it here."));
			return;
		}
		const granted = users.filter((u) => u.grants.some((g) => g.virtualModel === draft.id));
		const others = users.filter((u) => !granted.includes(u));
		const addBtn = btn("Grant to someone…", { icon: "plus", cls: "outline sm" });
		addBtn.setAttribute("aria-haspopup", "dialog");
		addBtn.addEventListener("click", () => picker(addBtn, {
			placeholder: "Find a person or service",
			items: others.map((u) => ({ value: u.id, label: u.handle, sub: [u.name, u.role, u.state !== "active" ? u.state : null].filter(Boolean).join(" · "), lead: avatar(u, "sm") })),
			empty: users.length === granted.length ? "Everyone already has it" : "No one matches",
			onPick: async (id) => {
				const u = users.find((x) => x.id === id);
				try { await saveGrant(u, 1); toast(`${u.handle} may now ask for ${draft.served}`); }
				catch (e) { toast(e.message, "bad"); }
			},
		}));
		host.append(h("div", { class: "vm-grant-tools" }, addBtn, h("span", { class: "ink-3" }, granted.length ? `${granted.length === 1 ? "1 person" : `${granted.length} people`} may ask for this model` : "Nobody may ask for this model yet")));
		for (const u of granted) {
			const g = u.grants.find((x) => x.virtualModel === draft.id);
			const paused = g.allotment <= 0.0001;
			const num = input({ type: "number", min: 0, max: 100, step: 5, value: Math.round(g.allotment * 100), class: "input sm vm-share", "aria-label": `${u.handle}'s share`, disabled: paused });
			num.addEventListener("change", async () => {
				const v = Math.min(100, Math.max(0, Number(num.value) || 0)) / 100;
				try { await saveGrant(u, v); toast(`${u.handle}: ${fmtPct(v, null)} share`); } catch (e) { toast(e.message, "bad"); paint(); }
			});
			host.append(h("div", { class: `li vm-grant ${paused ? "paused" : ""}` },
				avatar(u),
				h("div", { class: "lt" },
					h("div", { class: "main row", style: { gap: "8px" } }, h("a", { href: `#/users/${u.id}`, class: "link-text" }, u.handle), u.role !== "member" ? h("span", { class: "tag" }, u.role) : null, paused ? h("span", { class: "tag warn" }, "paused") : null),
					h("div", { class: "sub" }, paused ? "Paused: they get the limit error until resumed" : shareWords(g.allotment))),
				iconBtn("x", `Revoke ${u.handle}'s access`, async () => {
					if (!(await confirm({ title: `Revoke ${u.handle}'s access to ${draft.served}?`, text: "Their keys stop reaching this model immediately. You can grant it again any time.", confirmLabel: "Revoke", danger: true }))) return;
					try { await saveGrant(u, null); toast(`${u.handle} no longer has ${draft.served}`); } catch (e) { toast(e.message, "bad"); }
				}, "sm"),
				h("div", { class: "vm-grant-ctl" },
					paused ? null : h("label", { class: "vm-share-wrap", title: "Scales this model's usage limits for this person: 30% turns a limit of 50% into 15%" }, "Share", num, "%"),
					btn(paused ? "Resume" : "Pause", { cls: "sm ghost", title: paused ? "Let them use it again" : "Keep the grant but stop serving them for now", onClick: async () => {
						try { await saveGrant(u, paused ? (u._resume ?? 1) : (u._resume = g.allotment, 0)); } catch (e) { toast(e.message, "bad"); }
					} }),
				),
			));
		}
	}
	paint();
	return {
		el: card({
			title: "Who may ask for it",
			sub: "People and services whose keys can use this model. A share below 100% scales its usage limits for that person.",
			flush: true,
			body: host,
			cls: "vm-grants",
		}),
		paint,
	};
}
