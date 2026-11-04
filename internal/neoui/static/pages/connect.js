// Connect: everything a teammate needs to point a tool at this proxy. The
// address, the model names they may ask for, a key, and copy-ready snippets.
// Admins see it too, so they can walk someone through it.

import { h, icon, fmtAgo } from "../lib/dom.js";
import { neo } from "../lib/client.js";
import { pageHead, card, btn, servedName, copyLine, kv, seg, empty, spinner, errorState, toast, dialog, field, input } from "../lib/components.js";

const origin = () => location.origin;

function snippetBlock(code, label) {
	const pre = h("pre", { class: "code snippet", tabindex: 0 }, code);
	const copy = btn("Copy", {
		icon: "copy",
		cls: "sm outline",
		onClick: async () => {
			try { await navigator.clipboard.writeText(code); toast(`Copied the ${label}`); }
			catch { toast("Could not copy; select the text instead", "bad"); }
		},
	});
	return h("div", { class: "snippet-wrap" }, h("div", { class: "snippet-head" }, h("span", null, label), copy), pre);
}

function snippets(base, model, key) {
	const k = key || "<your key>";
	return {
		neocode: {
			label: "NeoCode",
			text: `In NeoCode: Models → NeoProxy → Connect.\nPaste this address, then your member key when asked.\n\n  Address  ${base}\n  Key      ${k}\n\nNo /v1 on the address; NeoCode adds it. Your models appear as ${model}.`,
		},
		neopi: {
			label: "NeoPi",
			text: `# ~/.config/npi/providers.yaml\nneoproxy:\n  base_url: ${base}\n  api_key: ${k}\n\n# then pick a model with its full name, e.g.\nnpi --model ${model}`,
		},
		curl: {
			label: "curl",
			text: `curl ${base}/v1/chat/completions \\\n  -H "Authorization: Bearer ${k}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${model}","messages":[{"role":"user","content":"Say hello."}]}'`,
		},
		openai: {
			label: "OpenAI SDK",
			text: `from openai import OpenAI\n\nclient = OpenAI(base_url="${base}/v1", api_key="${k}")\nreply = client.chat.completions.create(\n    model="${model}",\n    messages=[{"role": "user", "content": "Say hello."}],\n)\nprint(reply.choices[0].message.content)`,
		},
		anthropic: {
			label: "Anthropic SDK",
			text: `import anthropic\n\nclient = anthropic.Anthropic(base_url="${base}", api_key="${k}")\nreply = client.messages.create(\n    model="${model}",\n    max_tokens=256,\n    messages=[{"role": "user", "content": "Say hello."}],\n)\nprint(reply.content[0].text)`,
		},
	};
}

export async function render(ctx) {
	const { root, session } = ctx;
	root.append(pageHead({ title: "Connect", desc: "Point any OpenAI- or Anthropic-compatible tool at this proxy. Ask for a model by its NeoProxy name and the switch does the rest." }));
	const host = h("div");
	root.append(host, spinner());
	let me, vms, models;
	try {
		[me, vms, models] = await Promise.all([neo.user(session.userId), neo.virtualModels(), neo.models().catch(() => [])]);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	const granted = vms.filter((vm) => session.role === "admin" || me.grants.some((g) => g.virtualModel === vm.id));
	const direct = (me.providerAccess ?? []).flatMap((pid) => models.filter((m) => m.providerId === pid && m.exposed).map((m) => m.served ?? `neoproxy/${m.id}`));
	const names = [...granted.map((vm) => vm.served), ...direct];
	let model = names[0] ?? "neoproxy/default";
	let key = "";
	let tool = "neocode";
	const base = origin();

	// 1. Address
	host.append(
		card({
			title: "Address",
			sub: "The same address serves the OpenAI chat, responses and models endpoints and the Anthropic messages endpoint.",
			body: h("div", { class: "connect-grid" },
				kv([
					["Base URL", copyLine(base, "base URL")],
					["OpenAI-compatible", copyLine(`${base}/v1`, "OpenAI base URL")],
					["Anthropic-compatible", copyLine(`${base}/v1/messages`, "Anthropic messages URL")],
					["Model list", copyLine(`${base}/v1/models`, "models URL")],
				]),
				h("p", { class: "hint" }, "Keys go in ", h("code", null, "Authorization: Bearer <key>"), " (or ", h("code", null, "x-api-key"), " for Anthropic clients). Browser sessions never work as API keys."),
			),
		}),
	);

	// 2. Models you may ask for
	const modelList = h("div", { class: "list connect-models" });
	let showAllDirect = false;
	const renderModels = () => {
		modelList.replaceChildren();
		if (!names.length) {
			modelList.append(empty({ icon: "route", title: "Nothing granted yet", hint: session.role === "admin" ? "Grant yourself a virtual model under Users, or create one." : "Ask an admin to grant you a virtual model." }));
			return;
		}
		const shown = showAllDirect ? names : names.filter((n, i) => granted.some((v) => v.served === n) || i < granted.length + 5);
		for (const name of shown) {
			const vm = granted.find((v) => v.served === name);
			const g = me.grants.find((x) => x.virtualModel === vm?.id);
			modelList.append(
				h("button", { type: "button", class: `li clickable connect-model ${model === name ? "selected" : ""}`, "aria-pressed": String(model === name), onClick: () => { model = name; renderModels(); renderSnippet(); } },
					h("div", { class: "lt" },
						h("div", { class: "main" }, servedName(name, { virtual: !!vm })),
						h("div", { class: "sub" }, vm ? `${vm.stages.length} stage${vm.stages.length === 1 ? "" : "s"}${vm.description ? ` · ${vm.description}` : ""}${g && g.allotment < 1 ? ` · you may use up to ${Math.round(g.allotment * 100)}% of each threshold` : ""}` : "direct provider model, no fallback"),
					),
					icon(model === name ? "check" : "chevronRight", 14),
				),
			);
		}
		const hidden = names.length - shown.length;
		if (hidden > 0) modelList.append(h("button", { type: "button", class: "li clickable", onClick: () => { showAllDirect = true; renderModels(); } }, h("div", { class: "lt" }, h("div", { class: "main ink-2" }, `Show ${hidden} more direct models`)), icon("chevronDown", 14)));
	};
	renderModels();
	host.append(card({ title: "Models you may ask for", sub: "Virtual models fall back through their stages for you; direct models go straight to one provider.", flush: true, body: modelList }));

	// 3. Key
	const keyBody = h("div");
	const renderKeys = () => {
		keyBody.replaceChildren();
		const list = me.keys ?? [];
		if (!list.length) keyBody.append(h("p", { class: "ink-2" }, "You have no key yet. Mint one; the secret is shown once and the snippets below fill in automatically."));
		else keyBody.append(h("div", { class: "list" }, list.map((k) => h("div", { class: "li" }, h("div", { class: "lt" }, h("div", { class: "main" }, h("code", null, `${k.prefix}…`), " ", k.label), h("div", { class: "sub" }, `made ${fmtAgo(k.createdAt)}${k.lastUsedAt ? ` · last used ${fmtAgo(k.lastUsedAt)}` : " · never used"}${k.expiresAt ? ` · expires ${new Date(k.expiresAt).toLocaleDateString()}` : ""}`))))));
		keyBody.append(h("p", { class: "hint" }, "A key is only as powerful as you are: it reaches the models listed above and nothing else."));
	};
	renderKeys();
	host.append(
		card({
			title: "Your key",
			actions: [btn("Mint a key", { icon: "plus", cls: "primary sm", onClick: () => mintKey() })],
			body: keyBody,
		}),
	);

	async function mintKey() {
		let label = "";
		const dlg = dialog({
			title: "Mint a key",
			sub: "Name it after the tool or machine that will use it.",
			body: field("Label", input({ placeholder: "laptop · NeoCode", autofocus: true, onInput: (e) => (label = e.target.value) })),
			actions: [
				btn("Cancel", { onClick: () => dlg.close() }),
				btn("Mint key", {
					cls: "primary",
					onClick: async () => {
						if (!label.trim()) return;
						try {
							const r = await neo.createKey(me.id, label.trim());
							me = await neo.user(me.id);
							key = r.secret;
							dlg.close();
							renderKeys();
							renderSnippet();
							toast("Key minted; it is filled into the snippets on this page until you leave it");
						} catch (e) {
							toast(e.message, "bad");
						}
					},
				}),
			],
		});
	}

	// 4. Snippets
	const snippetHost = h("div");
	const picker = h("div");
	const renderSnippet = () => {
		const all = snippets(base, model, key);
		picker.replaceChildren(seg(Object.entries(all).map(([id, s]) => ({ value: id, label: s.label })), tool, (v) => { tool = v; renderSnippet(); }));
		snippetHost.replaceChildren(snippetBlock(all[tool].text, `${all[tool].label} snippet`));
	};
	renderSnippet();
	host.append(card({ title: "Paste into your tool", sub: key ? "Your new key is filled in. It will not be shown again after you leave this page." : "Snippets use <your key> until you mint one above.", body: h("div", null, picker, snippetHost) }));
}
