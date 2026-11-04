import { enrichVault } from "./providers.js";
// Cookie-authenticated dashboard transport. Inference keys and operator secrets
// are not dashboard identities; only the explicit one-time bootstrap uses a secret.
let identity = null;
// Remove credentials left by the pre-session dashboard, never migrate them.
for (const storage of [sessionStorage, localStorage]) storage.removeItem("neo-session");
export const session = {
	get: () => identity,
	set(value) { identity = value; invalidate(); },
	clear() { identity = null; invalidate(); },
};

export class ApiError extends Error {
	constructor(status, message, body) { super(message); this.status = status; this.body = body; }
}
const unauthorizedListeners = new Set();
export function onUnauthorized(fn) { unauthorizedListeners.add(fn); return () => unauthorizedListeners.delete(fn); }

async function request(method, path, { json, body, headers = {}, raw = false, query, auth = true } = {}) {
	let url = path.startsWith("/") ? path : `/v8/management/${path}`;
	if (query) {
		const q = new URLSearchParams();
		for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
		if (q.size) url += (url.includes("?") ? "&" : "?") + q;
	}
	const init = { method, credentials: "same-origin", cache: "no-store", headers: { ...headers } };
	const owner = identity;
	if (!["GET", "HEAD", "OPTIONS"].includes(method)) init.headers["X-Neoproxy-CSRF"] = "1";
	if (json !== undefined) { init.headers["Content-Type"] = "application/json"; init.body = JSON.stringify(json); }
	else if (body !== undefined) init.body = body;
	const res = await fetch(url, init);
	// A forbidden operation is not an expired identity. Leave the session intact.
	if (res.status === 401 && auth && owner && owner === identity) for (const fn of unauthorizedListeners) fn();
	if (raw && res.ok) return res;
	const text = await res.text();
	let data = text;
	if ((res.headers.get("content-type") || "").includes("application/json") && text) {
		try { data = JSON.parse(text); } catch {}
	}
	if (!res.ok) {
		const msg = (data && typeof data === "object" && (data.error?.message || data.error || data.message)) || `${res.status} ${res.statusText}`;
		throw new ApiError(res.status, typeof msg === "string" ? msg : JSON.stringify(msg), data);
	}
	return data;
}

const cache = new Map();
async function cached(key, loader, ttl = 15_000) {
	const hit = cache.get(key);
	if (hit && Date.now() - hit.at < ttl) return hit.value;
	const owner = identity;
	const value = await loader();
	if (identity === owner) cache.set(key, { at: Date.now(), value });
	return value;
}
export function invalidate(prefix) { for (const k of cache.keys()) if (!prefix || k.startsWith(prefix)) cache.delete(k); }
export const engine = {
	get: (p, o) => request("GET", p, o),
	put: (p, json, o) => request("PUT", p, { ...o, json }),
	patch: (p, json, o) => request("PATCH", p, { ...o, json }),
	post: (p, json, o) => request("POST", p, { ...o, json }),
	del: (p, o) => request("DELETE", p, o),
	raw: (m, p, o) => request(m, p, { ...o, raw: true }),
	config: () => cached("config", () => request("GET", "config")),
	configYaml: () => request("GET", "config.yaml"),
	credentials: () => cached("credentials", () => request("GET", "credentials")),
	credentialModels: () => cached("credential-models", () => request("GET", "credentials/models"), 60_000),
	usage: () => cached("usage", () => request("GET", "observability/usage/api-keys"), 5_000),
	plugins: () => cached("plugins", () => request("GET", "plugins")),
	pluginStore: () => cached("plugin-store", () => request("GET", "plugins/store"), 60_000),
	latestVersion: () => cached("latest-version", () => request("GET", "server/latest-version"), 300_000),
	logs: (limit, cursor) => request("GET", "observability/logs", { query: { limit, cursor } }),
	errorLogs: () => request("GET", "observability/logs/errors"),
	health: async () => (await fetch("/healthz", { cache: "no-store" })).ok,
};

const changeListeners = new Set();
export function onChange(fn) { changeListeners.add(fn); return () => changeListeners.delete(fn); }
function changed(what) { invalidate("vault"); for (const fn of changeListeners) fn(what); }
const path = (p) => `/v8/neo/${p}`;
const id = encodeURIComponent;
async function mutate(method, route, json, what) {
	const value = await request(method, path(route), { json });
	// Notify after the caller has consumed the returned entity.
	setTimeout(() => changed(what), 0);
	return value;
}
export const neo = {
	core: () => request("GET", path("core")),
	// The vault joined with the access list: real names, emails, plans, health.
	vault: () => cached("vault", async () => {
		const [vault, access, providers] = await Promise.all([
			request("GET", path("vault")),
			request("GET", path("access")).catch(() => null),
			cached("providers", () => request("GET", path("access/providers")), 300_000).catch(() => null),
		]);
		return enrichVault(vault, access, providers?.providers);
	}, 5_000),
	access: () => request("GET", path("access")),
	providerCatalog: () => cached("providers", () => request("GET", path("access/providers")), 300_000),
	updateAccess: (accountId, patch) => mutate("PATCH", `access/${id(accountId)}`, patch, "vault"),
	removeAccess: (accountId) => mutate("DELETE", `access/${id(accountId)}`, undefined, "vault"),
	refreshAccess: (accountId) => mutate("POST", `access/${id(accountId)}/refresh`, {}, "vault"),
	refreshQuota: (accountId) => mutate("POST", `access/${id(accountId)}/quota/refresh`, {}, "vault"),
	testAccount: (accountId, body = {}) => mutate("POST", `access/${id(accountId)}/test`, body, "vault"),
	addApiKey: (body) => mutate("POST", "access/api-key", body, "vault"),
	uploadAccessFile: (form) => request("POST", path("access/file"), { body: form }).then((v) => { setTimeout(() => changed("vault"), 0); return v; }),
	importCandidates: (profile) => request("GET", path("access/import/neopi"), { query: { profile } }),
	importFromNeoPi: (body) => mutate("POST", "access/import/neopi", body, "vault"),
	localNeoPi: () => request("GET", path("access/neopi/local")),
	connectLocalNeoPi: (body = {}) => mutate("POST", "access/neopi/local/connect", body, "vault"),
	sources: () => request("GET", path("vault/sources")),
	saveSource: (src) => mutate(src.id ? "PUT" : "POST", `vault/sources${src.id ? `/${id(src.id)}` : ""}`, src, "vault"),
	syncSource: (sourceId) => mutate("POST", `vault/sources/${id(sourceId)}/sync`, {}, "vault"),
	deleteSource: (sourceId) => mutate("DELETE", `vault/sources/${id(sourceId)}`, undefined, "vault"),
	startLogin: (provider, query) => request("POST", path(`vault/login/${id(provider)}`), { json: {}, query }),
	loginStatus: (state) => request("GET", path("vault/login/status"), { query: { state } }),
	loginCallback: (body) => request("POST", path("vault/login/callback"), { json: body }),
	models: () => request("GET", path("models")),
	setProviderExposed: (provider, exposed) => mutate("PATCH", `vault/providers/${id(provider)}`, { exposed }, "vault"),
	setModelExposed: (model, exposed) => mutate("PATCH", `models/${id(model)}`, { exposed }, "models"),
	setModelsExposed: (set) => mutate("POST", "models/exposure", { set }, "models"),
	virtualModels: () => request("GET", path("virtual-models")),
	virtualModel: (model) => request("GET", path(`virtual-models/${id(model)}`)),
	// The adjustable properties one model supports (neoparams.Catalogue), derived from what the proxy knows about that model.
	modelParams: (model) => cached(`model-params:${model}`, () => request("GET", path("model-params"), { query: { model } }), 60_000),
	saveVirtualModel: (vm) => {
		const { name, description, stages, terminal, script } = vm;
		return mutate(vm.id ? "PUT" : "POST", `virtual-models${vm.id ? `/${id(vm.id)}` : ""}`, { name, description, stages, terminal, script }, "virtual-models");
	},
	deleteVirtualModel: (model) => mutate("DELETE", `virtual-models/${id(model)}`, undefined, "virtual-models"),
	users: () => request("GET", path("users")),
	user: (user) => request("GET", path(`users/${id(user)}`)),
	// modelAccess left undefined keeps the stored allowlist; {} clears it.
	saveUser: (user) => {
		const { handle, name, password, role, state, providerAccess, modelAccess, grants, limits } = user;
		return mutate(user.id ? "PUT" : "POST", `users${user.id ? `/${id(user.id)}` : ""}`, { handle, name, password, role, state, providerAccess, modelAccess, grants, limits }, "users");
	},
	createKey: (user, label, accountIds, expiresAt, models) => mutate("POST", `users/${id(user)}/keys`, { label, ...(accountIds ? { accountIds } : {}), ...(expiresAt ? { expiresAt } : {}), ...(models?.length ? { models } : {}) }, "users"),
	revokeKey: (user, key) => mutate("DELETE", `users/${id(user)}/keys/${id(key)}`, undefined, "users"),
	usage: () => request("GET", path("usage")),
	requests: (query = {}) => request("GET", path("requests"), { query: { limit: 100, ...query } }),
	request: (requestId) => request("GET", path(`requests/${id(requestId)}`)),
	simulate: ({ asked, userId, keyId, protocol, request: payload }) => request("POST", path("simulate"), { json: { asked, userId, keyId, protocol, request: payload } }),
	redirects: () => request("GET", path("redirects")),
	saveRedirect: (redirect) => {
		const { from, to, scope, enabled, bypassAccess, note } = redirect;
		return mutate(redirect.id ? "PUT" : "POST", `redirects${redirect.id ? `/${id(redirect.id)}` : ""}`, { from, to, scope, enabled, bypassAccess, note }, "redirects");
	},
	deleteRedirect: (redirectId) => mutate("DELETE", `redirects/${id(redirectId)}`, undefined, "redirects"),
	async restore() {
		try { const s = await request("GET", "/v8/session", { auth: false }); session.set(s); return s; }
		catch (e) { session.clear(); if (e.status === 401) return null; throw e; }
	},
	async signIn(handle, password) {
		const s = await request("POST", "/v8/session", { json: { handle, password }, auth: false });
		session.set(s); return s;
	},
	async bootstrap(handle, password, secret, name) {
		const s = await request("POST", "/v8/session/bootstrap", { json: { handle, password, name }, headers: { Authorization: `Bearer ${secret}` }, auth: false });
		session.set(s); return s;
	},
	async signOut() { await request("DELETE", "/v8/session", { auth: false }); session.clear(); },
	// Poll the durable request ledger; never synthesize traffic on an idle server.
	subscribeStream(fn, onError = () => {}, initial) {
		let stopped = false, timer;
		const seen = new Set((initial ?? []).map((r) => r.id));
		let initialized = Array.isArray(initial);
		async function poll() {
			try {
				const rows = await neo.requests({ limit: 14 });
				if (stopped) return;
				for (const r of [...rows].reverse()) { if (initialized && !seen.has(r.id)) fn(r); seen.add(r.id); }
				if (seen.size > 1000) { seen.clear(); for (const r of rows) seen.add(r.id); }
				initialized = true;
				onError(null);
			} catch (e) { if (!stopped) onError(e); }
			if (!stopped) timer = setTimeout(poll, 5000);
		}
		poll();
		return () => { stopped = true; clearTimeout(timer); };
	},
};
