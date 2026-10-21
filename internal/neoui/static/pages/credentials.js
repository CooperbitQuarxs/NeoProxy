import { h, card, body, badge, btn, table, stat, fmtInt, fmtPct, fmtAgo, fmtDate, empty, toast, status, modal, confirm, seg, icon, kv, field, sparkline, copyLine } from "../ui.js";
import { pageHead, providerLabel, statusKind, statusText, PROVIDERS, groupBy } from "./shared.js";

const OAUTH_PROVIDERS = ["codex", "claude", "antigravity", "kimi", "kimi-ai", "xai", "devin", "meta"];

function credName(e) {
	return e.label || e.email || e.account || e.name || e.id;
}

function detail(e, { api, invalidate, reload, navigate }) {
	const kind = statusKind(e);
	const total = (e.success ?? 0) + (e.failed ?? 0);
	const act = async (label, fn, { ok = `${label} done` } = {}) => {
		try {
			await fn();
			invalidate("credentials");
			toast(ok, "ok");
			reload();
		} catch (ex) {
			toast(`${label} failed: ${ex.message}`, "bad");
		}
	};
	const actions = h(
		"div",
		{ class: "row wrap" },
		btn(e.disabled ? "Enable" : "Disable", {
			icon: "power",
			cls: "outline sm",
			onClick: () => act(e.disabled ? "Enable" : "Disable", () => api.patch("credentials/status", { name: e.name, auth_index: e.auth_index, disabled: !e.disabled })),
		}),
		btn("Refresh token", { icon: "refresh", cls: "outline sm", onClick: () => act("Refresh", () => api.post("credentials/refresh", { name: e.name, auth_index: e.auth_index })) }),
		e.name && !e.runtime_only
			? btn("Download", {
					icon: "download",
					cls: "outline sm",
					onClick: async () => {
						try {
							const res = await api.raw("GET", "credentials/download", { query: { name: e.name } });
							if (!res.ok) throw new Error(`${res.status}`);
							const blob = await res.blob();
							const a = h("a", { href: URL.createObjectURL(blob), download: e.name });
							a.click();
						} catch (ex) {
							toast(`Download failed: ${ex.message}`, "bad");
						}
					},
				})
			: null,
		e.name && !e.runtime_only
			? btn("Delete", {
					icon: "trash",
					cls: "danger sm",
					onClick: async () => {
						if (!(await confirm({ title: `Delete ${e.name}?`, text: "The credential file is removed from the auth directory. Logins can be redone; API keys live in the configuration and are unaffected.", confirmLabel: "Delete", danger: true }))) return;
						await act("Delete", () => api.del("credentials", { query: { name: e.name } }));
						navigate("credentials");
					},
				})
			: null,
	);
	const buckets = e.recent_requests ?? [];
	return card({
		title: [credName(e), badge(statusText(e), kind === "muted" ? "" : kind)],
		sub: `${providerLabel(e.provider)} · ${e.type || e.account_type || "credential"}`,
		actions: btn("", { icon: "x", cls: "icon sm", title: "Close", onClick: () => navigate("credentials") }),
		body: body([
			h(
				"div",
				{ class: "grid cols-2-even", style: { gap: "20px" } },
				kv([
					["File", e.name ? h("span", { class: "mono" }, e.name) : h("span", { style: { color: "var(--ink-3)" } }, "runtime only")],
					e.email ? ["Account", e.email] : null,
					e.account && e.account !== e.email ? ["Account id", h("span", { class: "mono" }, e.account)] : null,
					e.project_id ? ["Project", h("span", { class: "mono" }, e.project_id)] : null,
					["Priority", String(e.priority ?? 0)],
					["Source", e.source || "file"],
					e.status_message ? ["Status", e.status_message] : null,
					e.next_retry_after ? ["Retry after", fmtDate(e.next_retry_after)] : null,
					["Last refresh", fmtAgo(e.last_refresh)],
					["Updated", fmtDate(e.updated_at || e.modtime)],
				]),
				h(
					"div",
					{ class: "stack", style: { gap: "10px" } },
					h("div", { class: "stats small" }, stat({ label: "Requests", value: fmtInt(total) }), stat({ label: "Failed", value: fmtInt(e.failed ?? 0) }), stat({ label: "Error rate", value: fmtPct(e.failed ?? 0, total) })),
					buckets.length ? sparkline(buckets.map((b) => (b.success ?? 0) + (b.failed ?? 0))) : null,
					e.cooldowns && Object.keys(e.cooldowns).length ? h("div", { class: "callout warn" }, "Cooling down: ", h("code", null, JSON.stringify(e.cooldowns))) : null,
				),
			),
			h("div", { style: { marginTop: "14px" } }, actions),
		]),
	});
}

function addDialog({ api, invalidate, reload, load }) {
	let provider = "codex";
	let mode = "login";
	let poll = 0;
	const statusLine = h("div", { class: "callout", hidden: true });
	const providerSel = h("select", { class: "select", style: { width: "100%" } }, OAUTH_PROVIDERS.map((p) => h("option", { value: p }, providerLabel(p))));
	providerSel.addEventListener("change", () => (provider = providerSel.value));
	const fileInput = h("input", { class: "input", type: "file", accept: ".json,application/json", multiple: true, style: { paddingTop: "5px" } });
	const loginPane = h("div", { class: "stack", style: { gap: "10px" } }, field("Provider", providerSel, "Opens the provider's login in a new tab; the proxy waits for the callback."), statusLine);
	const uploadPane = h("div", { class: "stack", style: { gap: "10px" }, hidden: true }, field("Credential files", fileInput, "JSON files produced by a previous login (on any machine)."));
	const segHost = h("div");
	const renderSeg = () => {
		segHost.replaceChildren(
			seg(
				[
					{ label: "Sign in", value: "login" },
					{ label: "Upload file", value: "upload" },
				],
				mode,
				(v) => {
					mode = v;
					loginPane.hidden = v !== "login";
					uploadPane.hidden = v !== "upload";
					renderSeg();
				},
			),
		);
	};
	renderSeg();
	const dlg = modal({
		title: "Add credential",
		sub: "A login or an uploaded credential file. API keys are configured under Also supported → Providers.",
		body: [segHost, loginPane, uploadPane],
		actions: [
			btn("Cancel", { onClick: () => dlg.close() }),
			btn("Continue", {
				cls: "primary",
				onClick: async () => {
					if (mode === "upload") {
						if (!fileInput.files.length) return toast("Choose a file first", "warn");
						const fd = new FormData();
						for (const f of fileInput.files) fd.append("file", f, f.name);
						try {
							await api.post("credentials", undefined, { body: fd });
							invalidate("credentials");
							toast("Uploaded", "ok");
							dlg.close();
							reload();
						} catch (ex) {
							toast(`Upload failed: ${ex.message}`, "bad");
						}
						return;
					}
					try {
						statusLine.hidden = false;
						statusLine.textContent = "Starting login…";
						const r = await api.get("oauth/auth-url", { query: { provider } });
						if (r.url) window.open(r.url, "_blank", "noopener");
						statusLine.replaceChildren("Waiting for ", h("strong", null, providerLabel(provider)), " to call back… ", r.url ? h("a", { href: r.url, target: "_blank", rel: "noreferrer" }, "reopen login") : null);
						if (r.flow === "device" && r.user_code) statusLine.append(h("div", null, "Enter code ", h("code", null, r.user_code)));
						const state = r.state;
						clearInterval(poll);
						poll = setInterval(async () => {
							try {
								const s = await api.get("oauth/status", { query: { state } });
								if (s.status === "ok") {
									clearInterval(poll);
									invalidate("credentials");
									toast(`${providerLabel(provider)} signed in`, "ok");
									dlg.close();
									reload();
								} else if (s.status === "error") {
									clearInterval(poll);
									statusLine.className = "callout bad";
									statusLine.textContent = `Login failed: ${s.error || "unknown error"}`;
								}
							} catch {}
						}, 2000);
					} catch (ex) {
						statusLine.className = "callout bad";
						statusLine.textContent = `Could not start login: ${ex.message}`;
					}
				},
			}),
		],
		onClose: () => clearInterval(poll),
	});
	return dlg;
}

// The batch endpoint returns HTTP 200 even when individual refreshes fail.
export function refreshSummary(results) {
	const failed = results.filter((r) => !r.success).length;
	const succeeded = results.length - failed;
	if (!results.length) return { text: "No credentials to refresh", kind: "warn" };
	return {
		text: `Refresh finished: ${succeeded} refreshed, ${failed} failed`,
		kind: failed ? (succeeded ? "warn" : "bad") : "ok",
	};
}

export const credentials = {
	id: "credentials",
	title: "Credentials",
	icon: "shield",
	key: "c",
	async render(ctx) {
		const { load, route, navigate, api } = ctx;
		const creds = await load.credentials();
		const entries = creds?.files ?? [];
		const q = route.query;
		const filterProvider = q.get("provider") || "";
		const filterState = q.get("state") || "all";
		const search = q.get("q") || "";
		const selectedName = q.get("select") || "";
		const setQuery = (patch) => {
			const next = new URLSearchParams(q);
			for (const [k, v] of Object.entries(patch)) (v ? next.set(k, v) : next.delete(k));
			navigate(`credentials?${next}`);
		};
		if (q.get("add")) {
			setTimeout(() => {
				setQuery({ add: "" });
				addDialog(ctx);
			}, 0);
		}

		const byProvider = groupBy(entries, (e) => e.provider || "unknown");
		let rows = entries;
		if (filterProvider) rows = rows.filter((e) => (e.provider || "unknown") === filterProvider);
		if (filterState !== "all") rows = rows.filter((e) => (filterState === "off" ? e.disabled : statusKind(e) === filterState));
		if (search) {
			const s = search.toLowerCase();
			rows = rows.filter((e) => [credName(e), e.name, e.provider, e.email, e.type].filter(Boolean).some((v) => String(v).toLowerCase().includes(s)));
		}
		const selected = entries.find((e) => e.name === selectedName || e.id === selectedName);
		const counts = { all: entries.length, ok: 0, warn: 0, bad: 0, off: 0 };
		for (const e of entries) {
			if (e.disabled) counts.off++;
			else counts[statusKind(e)]++;
		}

		const searchInput = h("input", { class: "input", placeholder: "Filter by name, account, provider", value: search });
		searchInput.addEventListener("change", () => setQuery({ q: searchInput.value }));
		searchInput.addEventListener("keydown", (e) => {
			if (e.key === "Enter") setQuery({ q: searchInput.value });
		});

		const providerSeg = h(
			"select",
			{ class: "select", onChange: (e) => setQuery({ provider: e.target.value }) },
			h("option", { value: "" }, "All providers"),
			[...byProvider.keys()].sort().map((id) => h("option", { value: id, selected: id === filterProvider || null }, `${providerLabel(id)} (${byProvider.get(id).length})`)),
		);

		const tbl = table(
			[
				{ label: "Credential", render: (e) => h("div", { class: "cell-stack" }, h("span", null, credName(e)), h("span", { class: "sub" }, e.name || "runtime")) },
				{ label: "Provider", render: (e) => h("div", { class: "cell-stack" }, h("span", null, providerLabel(e.provider)), h("span", { class: "sub" }, e.type || e.account_type || "")) },
				{ label: "Status", render: (e) => status(statusKind(e), statusText(e), e.next_retry_after ? `retry ${fmtAgo(e.next_retry_after).replace(" ago", "")}` : e.status_message ? e.status_message.slice(0, 40) : undefined) },
				{ label: "Requests", cls: "num", render: (e) => fmtInt((e.success ?? 0) + (e.failed ?? 0)) },
				{ label: "Error rate", cls: "num", render: (e) => badge(fmtPct(e.failed ?? 0, (e.success ?? 0) + (e.failed ?? 0)), (e.failed ?? 0) / Math.max(1, (e.success ?? 0) + (e.failed ?? 0)) > 0.05 ? "bad" : "ok") },
				{ label: "Priority", cls: "num", render: (e) => String(e.priority ?? 0) },
				{ label: "Refreshed", cls: "dim", render: (e) => fmtAgo(e.last_refresh || e.updated_at || e.modtime) },
				{ label: "", cls: "actions", render: (e) => btn("", { icon: "arrowRight", cls: "icon sm", title: "Details", onClick: () => setQuery({ select: e.name || e.id }) }) },
			],
			rows,
			{
				onRow: (e) => setQuery({ select: e.name || e.id }),
				keyOf: (e) => e.name || e.id,
				selectedKey: selected ? selected.name || selected.id : undefined,
				emptyState: empty({
					title: entries.length ? "Nothing matches" : "No credentials yet",
					hint: entries.length ? "Loosen the filters above." : "Sign in to a provider or upload a credential file. API keys go under Providers.",
					action: entries.length ? btn("Clear filters", { cls: "outline sm", onClick: () => navigate("credentials") }) : btn("Add credential", { cls: "outline sm", icon: "plus", onClick: () => addDialog(ctx) }),
				}),
			},
		);

		return [
			pageHead("Credentials", "Every login and credential file this proxy can route through, with its health.", [
				btn("Refresh all", {
					icon: "refresh",
					cls: "outline",
					onClick: async () => {
						try {
							const response = await api.post("credentials/refresh", { all: true });
							const summary = refreshSummary(response.results ?? []);
							ctx.invalidate("credentials");
							toast(summary.text, summary.kind);
							ctx.reload();
						} catch (ex) {
							toast(`Refresh failed: ${ex.message}`, "bad");
						}
					},
				}),
				btn("Add credential", { icon: "plus", cls: "primary", onClick: () => addDialog(ctx) }),
			]),
			h(
				"div",
				{ class: "stats" },
				stat({ label: "Total", value: fmtInt(counts.all), sub: `${byProvider.size} provider${byProvider.size === 1 ? "" : "s"}` }),
				stat({ label: "Healthy", value: fmtInt(counts.ok), bar: "ok" }),
				stat({ label: "Cooling", value: fmtInt(counts.warn), sub: "waiting on a retry window", bar: counts.warn ? "warn" : "muted" }),
				stat({ label: "Failing", value: fmtInt(counts.bad), sub: "needs a new login", bar: counts.bad ? "bad" : "muted" }),
				stat({ label: "Disabled", value: fmtInt(counts.off), bar: "muted" }),
			),
			selected ? detail(selected, ctx) : null,
			card({
				title: "All credentials",
				sub: `${rows.length} of ${entries.length} shown · observed ${fmtAgo(creds?.observed_at)}`,
				actions: [
					h("div", { class: "search" }, icon("search", 14), searchInput),
					providerSeg,
					seg(
						[
							{ label: "All", value: "all", count: counts.all },
							{ label: "Healthy", value: "ok", count: counts.ok },
							{ label: "Cooling", value: "warn", count: counts.warn },
							{ label: "Failing", value: "bad", count: counts.bad },
							{ label: "Off", value: "off", count: counts.off },
						],
						filterState,
						(v) => setQuery({ state: v === "all" ? "" : v }),
					),
				],
				body: h("div", { class: "card-body flush" }, tbl),
			}),
		];
	},
};
