// One client, two adapters. `engine` talks to the management API that exists
// today (/v8/management). `neo` is the contract the dashboard assumes for the
// NeoPi-centred surfaces (core link, vault, catalogue, virtual models, users,
// request stream); it is served by the fixture until the backend lands.
// `sources` says which one each surface uses, so pages can mark preview data
// honestly without each page guessing.

import { createFixture, resolve } from "./fixture.js";

const KEY = "neo-session";

export const session = {
	get() {
		try {
			return JSON.parse(sessionStorage.getItem(KEY) || localStorage.getItem(KEY) || "null");
		} catch {
			return null;
		}
	},
	set(value, remember) {
		sessionStorage.removeItem(KEY);
		localStorage.removeItem(KEY);
		(remember ? localStorage : sessionStorage).setItem(KEY, JSON.stringify(value));
	},
	clear() {
		sessionStorage.removeItem(KEY);
		localStorage.removeItem(KEY);
	},
};

export class ApiError extends Error {
	constructor(status, message, body) {
		super(message);
		this.status = status;
		this.body = body;
	}
}

const unauthorizedListeners = new Set();
export function onUnauthorized(fn) {
	unauthorizedListeners.add(fn);
	return () => unauthorizedListeners.delete(fn);
}

async function request(method, path, { json, body, headers = {}, raw = false, query } = {}) {
	let url = path.startsWith("/") ? path : `/v8/management/${path}`;
	if (query) {
		const q = new URLSearchParams();
		for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
		const s = q.toString();
		if (s) url += (url.includes("?") ? "&" : "?") + s;
	}
	const init = { method, headers: { ...headers } };
	const s = session.get();
	if (s?.token) init.headers.Authorization = `Bearer ${s.token}`;
	if (json !== undefined) {
		init.headers["Content-Type"] = "application/json";
		init.body = JSON.stringify(json);
	} else if (body !== undefined) init.body = body;
	const res = await fetch(url, init);
	if (res.status === 401 || res.status === 403) for (const fn of unauthorizedListeners) fn(res.status);
	if (raw) return res;
	const text = await res.text();
	let data = text;
	if ((res.headers.get("content-type") || "").includes("application/json") && text) {
		try {
			data = JSON.parse(text);
		} catch {}
	}
	if (!res.ok) {
		const msg = (data && typeof data === "object" && (data.error?.message || data.error || data.message)) || `${res.status} ${res.statusText}`;
		throw new ApiError(res.status, typeof msg === "string" ? msg : JSON.stringify(msg), data);
	}
	return data;
}

// ---------- engine: the management API that exists ----------

const cache = new Map();
async function cached(key, loader, ttl = 15_000) {
	const hit = cache.get(key);
	if (hit && Date.now() - hit.at < ttl) return hit.value;
	const value = await loader();
	cache.set(key, { at: Date.now(), value });
	return value;
}
export function invalidate(prefix) {
	for (const k of [...cache.keys()]) if (!prefix || k.startsWith(prefix)) cache.delete(k);
}

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
	logs: (lines) => request("GET", "observability/logs", { query: { lines } }),
	errorLogs: () => request("GET", "observability/logs/errors"),
	health: async () => {
		const res = await fetch("/healthz", { cache: "no-store" });
		return res.ok;
	},
};

// ---------- neo: the NeoPi-centred contract, fixture-backed ----------

const world = createFixture();
const changeListeners = new Set();
export function onChange(fn) {
	changeListeners.add(fn);
	return () => changeListeners.delete(fn);
}
function changed(what) {
	for (const fn of changeListeners) fn(what);
}
const clone = (x) => structuredClone(x);
const streamSubs = new Set();
let streamTimer = null;
const latency = () => new Promise((r) => setTimeout(r, 30 + Math.random() * 60));

export const neo = {
	world, // exposed for the simulator and tests; pages must go through the methods below
	async core() {
		await latency();
		return clone(world.core);
	},
	async vault() {
		await latency();
		return { providers: clone(world.providers), accounts: clone(world.accounts) };
	},
	async setProviderExposed(id, exposed) {
		const p = world.providers.find((x) => x.id === id);
		if (!p) throw new ApiError(404, "provider not found");
		p.exposed = exposed;
		changed("vault");
	},
	async models() {
		await latency();
		return clone(world.models);
	},
	async setModelExposed(id, exposed) {
		const m = world.models.find((x) => x.id === id);
		if (!m) throw new ApiError(404, "model not found");
		m.exposed = exposed;
		changed("models");
	},
	async virtualModels() {
		await latency();
		return clone(world.virtualModels);
	},
	async virtualModel(id) {
		await latency();
		const v = world.virtualModels.find((x) => x.id === id || x.name === id);
		if (!v) throw new ApiError(404, "virtual model not found");
		return clone(v);
	},
	async saveVirtualModel(vm, actor = "shayna") {
		await latency();
		if (!/^[a-z][a-z0-9-]{0,31}$/.test(vm.name)) throw new ApiError(422, "Name must be lowercase letters, digits or dashes, up to 32 characters");
		if (world.models.some((m) => m.id === vm.name)) throw new ApiError(409, `"${vm.name}" collides with a served model`);
		const dup = world.virtualModels.find((x) => x.name === vm.name && x.id !== vm.id);
		if (dup) throw new ApiError(409, `A virtual model named "${vm.name}" already exists`);
		if (!vm.stages.length) throw new ApiError(422, "A virtual model needs at least one stage");
		const next = { ...clone(vm), served: `neoproxy/${vm.name}`, updatedAt: Date.now(), updatedBy: actor };
		const i = world.virtualModels.findIndex((x) => x.id === vm.id);
		if (i === -1) {
			next.id = vm.id || `vm-${vm.name}`;
			world.virtualModels.push(next);
		} else world.virtualModels[i] = next;
		changed("virtual-models");
		return clone(next);
	},
	async deleteVirtualModel(id) {
		await latency();
		const i = world.virtualModels.findIndex((x) => x.id === id);
		if (i === -1) throw new ApiError(404, "virtual model not found");
		world.virtualModels.splice(i, 1);
		for (const u of world.users) u.grants = u.grants.filter((g) => g.virtualModel !== id);
		changed("virtual-models");
	},
	async users() {
		await latency();
		return clone(world.users);
	},
	async user(id) {
		await latency();
		const u = world.users.find((x) => x.id === id || x.handle === id);
		if (!u) throw new ApiError(404, "user not found");
		return clone(u);
	},
	async saveUser(user) {
		await latency();
		if (!/^[a-z][a-z0-9-]{1,31}$/.test(user.handle)) throw new ApiError(422, "Handle must be lowercase letters, digits or dashes");
		const i = world.users.findIndex((x) => x.id === user.id);
		const next = clone(user);
		if (i === -1) {
			next.id = `u-${user.handle}`;
			next.createdAt = Date.now();
			next.keys ??= [];
			world.users.push(next);
		} else world.users[i] = { ...world.users[i], ...next };
		changed("users");
		return clone(next);
	},
	async createKey(userId, label) {
		await latency();
		const u = world.users.find((x) => x.id === userId);
		if (!u) throw new ApiError(404, "user not found");
		const raw = Array.from(crypto.getRandomValues(new Uint8Array(24)), (b) => b.toString(16).padStart(2, "0")).join("");
		const k = { id: raw.slice(0, 12), prefix: `nk_${raw.slice(0, 4)}…${raw.slice(-4)}`, label, createdAt: Date.now(), lastUsedAt: null };
		u.keys.push(k);
		changed("users");
		return { key: k, secret: `nk_${raw}` };
	},
	async revokeKey(userId, keyId) {
		await latency();
		const u = world.users.find((x) => x.id === userId);
		if (!u) throw new ApiError(404, "user not found");
		u.keys = u.keys.filter((k) => k.id !== keyId);
		changed("users");
	},
	async usage() {
		await latency();
		return clone(world.usage);
	},
	async requests({ limit = 100, userId, outcome, asked } = {}) {
		await latency();
		let list = world.requests;
		if (userId) list = list.filter((r) => r.userId === userId);
		if (outcome) list = list.filter((r) => r.outcome === outcome);
		if (asked) list = list.filter((r) => r.asked.includes(asked));
		return clone(list.slice(0, limit));
	},
	async request(id) {
		await latency();
		const r = world.requests.find((x) => x.id === id);
		if (!r) throw new ApiError(404, "request not found");
		return clone(r);
	},
	simulate({ asked, userId, at }) {
		const user = world.users.find((x) => x.id === userId);
		if (!user) throw new ApiError(404, "user not found");
		return resolve({ asked, user, at }, world);
	},
	resolveWith({ asked, user, virtualModels }) {
		return resolve({ asked, user }, { ...world, virtualModels: virtualModels ?? world.virtualModels });
	},
	// Live stream of new requests. Backed by a timer on the fixture; the real
	// backend is expected to expose SSE at /v8/neo/requests/stream.
	subscribeStream(fn) {
		streamSubs.add(fn);
		if (!streamTimer) streamTimer = setInterval(() => {
			const r = world.next();
			for (const f of streamSubs) f(clone(r));
		}, 7000 + Math.random() * 5000);
		return () => {
			streamSubs.delete(fn);
			if (!streamSubs.size && streamTimer) {
				clearInterval(streamTimer);
				streamTimer = null;
			}
		};
	},
	async localCredentials() {
		await latency();
		return clone(world.localCredentials);
	},
	// Session: admin signs in with the management secret (checked against the
	// engine); a user signs in with one of their NeoProxy keys (fixture lookup).
	async signIn(secret, remember) {
		if (secret.startsWith("nk_")) {
			await latency();
			const tail = secret.slice(-4);
			const user = world.users.find((u) => u.keys.some((k) => k.prefix.endsWith(tail)));
			if (!user) throw new ApiError(401, "That key is not recognised");
			if (user.state !== "active") throw new ApiError(403, `This account is ${user.state}`);
			const s = { role: user.role === "admin" ? "admin" : "user", userId: user.id, handle: user.handle, token: secret, source: "fixture" };
			session.set(s, remember);
			return s;
		}
		const probe = { role: "admin", userId: "u-shayna", handle: "shayna", token: secret, source: "engine" };
		session.set(probe, remember);
		try {
			await request("GET", "config");
		} catch (e) {
			session.clear();
			if (e instanceof ApiError && (e.status === 401 || e.status === 403)) throw new ApiError(401, "That management key was refused");
			throw e;
		}
		return probe;
	},
};

// Which adapter backs each surface. Pages use this for the preview marker.
export const sources = {
	overview: "fixture",
	core: "fixture",
	models: "fixture",
	"virtual-models": "fixture",
	users: "fixture",
	activity: "fixture",
	credentials: "engine",
	keys: "engine",
	logs: "engine",
	routing: "engine",
	config: "engine",
	plugins: "engine",
	about: "engine",
};
