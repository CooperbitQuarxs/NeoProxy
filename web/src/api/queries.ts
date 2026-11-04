import { QueryClient, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, del, get, patch, post, put } from "./client";
import type { AccessItem, AccessResponse, Health, NeoPiSource, Person, ProviderInfo, Session, UsageWindow, WindowKind } from "./types";
import { FALLBACK_PROVIDERS, canonicalProvider, planFor, providerInfo } from "../lib/providers";

export const queryClient = new QueryClient({
	defaultOptions: { queries: { retry: (n, e) => !(e instanceof ApiError && e.status < 500) && n < 2, refetchOnWindowFocus: true, staleTime: 5_000 } },
});

// ---------- session ----------
export function useSession() {
	return useQuery({
		queryKey: ["session"],
		queryFn: async () => {
			try {
				return await get<Session>("/v8/session", { quiet401: true });
			} catch (e) {
				if (e instanceof ApiError && e.status === 401) return null;
				throw e;
			}
		},
		staleTime: 60_000,
	});
}

export function useSignIn() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (v: { handle: string; password: string }) => post<Session>("/v8/session", v, { quiet401: true }),
		onSuccess: (s) => qc.setQueryData(["session"], s),
	});
}

export function useBootstrap() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (v: { handle: string; name?: string; password: string; secret: string }) =>
			post<Session>("/v8/session/bootstrap", { handle: v.handle, name: v.name || v.handle, password: v.password }, { headers: { Authorization: `Bearer ${v.secret}` }, quiet401: true }),
		onSuccess: (s) => qc.setQueryData(["session"], s),
	});
}

export function useSignOut() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: () => del("/v8/session", { quiet401: true }),
		onSettled: () => {
			qc.clear();
			qc.setQueryData(["session"], null);
		},
	});
}

export function useCore() {
	return useQuery({ queryKey: ["core"], queryFn: () => get<{ version?: string; host?: string; endpoint?: string; heartbeatAt?: number }>("core"), refetchInterval: 30_000 });
}

// ---------- access (the pool) ----------
type LegacyAccount = { id: string; providerId: string; ownerId?: string; label?: string; plan?: string; state?: string; modelIds?: string[]; sourceId?: string; kind?: string; windows?: UsageWindow[]; providerWindows?: UsageWindow[] };
type LegacyVault = { accounts: LegacyAccount[]; providers: Array<{ id: string; kind?: string }> };
type LegacyCredential = {
	id: string;
	name: string;
	provider: string;
	account_type?: string;
	email?: string;
	label?: string;
	status?: string;
	status_message?: string;
	disabled?: boolean;
	unavailable?: boolean;
	created_at?: string;
	updated_at?: string;
	recent_requests?: Array<{ time: string; success: number; failed: number }>;
	success?: number;
	failed?: number;
};

const PLAN_SUFFIX = /-(free|plus|pro|team|business|enterprise|max5|max20|max)\.json$/i;

function legacyHealth(state?: string, cred?: LegacyCredential): Health {
	if (cred?.disabled) return "disabled";
	const s = (state || cred?.status || "").toLowerCase();
	if (s.includes("expire")) return "expired";
	if (s.includes("cool")) return "cooling";
	if (s.includes("limit")) return "limited";
	if (s.includes("error") || s.includes("fail") || cred?.unavailable) return "error";
	return "healthy";
}

async function composeLegacy(): Promise<AccessResponse> {
	const [vault, credsRes, users, sources] = await Promise.all([
		get<LegacyVault>("vault"),
		get<{ files: LegacyCredential[] }>("/v8/management/credentials").catch(() => ({ files: [] as LegacyCredential[] })),
		get<Array<Person & { role?: string }>>("users").catch(() => [] as Person[]),
		get<NeoPiSource[]>("vault/sources").catch(() => [] as NeoPiSource[]),
	]);
	const creds = credsRes.files ?? [];
	const items: AccessItem[] = vault.accounts.map((a) => {
		const provider = canonicalProvider(a.providerId);
		const sameProvider = creds.filter((c) => canonicalProvider(c.provider) === provider);
		const cred = sameProvider.length === 1 ? sameProvider[0] : undefined;
		const source = a.sourceId ? sources.find((s) => s.id === a.sourceId) : undefined;
		const kind: AccessItem["kind"] = source ? "neopi" : cred?.account_type === "oauth" || !cred ? "subscription" : "apiKey";
		const planId = (a.plan || cred?.name.match(PLAN_SUFFIX)?.[1] || "").toLowerCase() || undefined;
		const known = planFor(provider, planId);
		const plan = known
			? { ...known, source: "detected" as const }
			: { id: planId ?? "unknown", name: planId ? planId[0].toUpperCase() + planId.slice(1) : "Plan not set", monthlyUSD: 20, source: "default" as const };
		const buckets = (cred?.recent_requests ?? []).map((b, i) => ({ t: i, ok: b.success, fail: b.failed }));
		const requests24h = buckets.reduce((n, b) => n + b.ok + b.fail, 0);
		return {
			id: a.id,
			provider,
			kind,
			label: cred?.email || cred?.label || a.label || a.id,
			ownerId: a.ownerId ?? "",
			plan,
			health: legacyHealth(a.state, cred),
			healthMessage: cred?.status_message || undefined,
			refresh: source ? "neopi" : kind === "subscription" ? "auto" : "none",
			neopiSource: source ? { id: source.id, name: source.name } : undefined,
			models: a.modelIds ?? [],
			windows: a.windows ?? [],
			providerWindows: a.providerWindows ?? [],
			usage: { requests24h, failed24h: buckets.reduce((n, b) => n + b.fail, 0), buckets },
			createdAt: cred?.created_at ? Date.parse(cred.created_at) : undefined,
			actions: ["reassign", "setPlan", "disable", "remove", ...(kind === "subscription" ? (["refresh"] as const) : [])],
		};
	});
	return {
		items,
		providers: FALLBACK_PROVIDERS,
		people: users.map((u) => ({ id: u.id, handle: u.handle, name: u.name || u.handle, role: u.role })),
		totals: { monthlyUSD: items.reduce((n, i) => n + i.plan.monthlyUSD, 0), items: items.length, healthy: items.filter((i) => i.health === "healthy").length },
		legacy: true,
	};
}

export function useAccess() {
	return useQuery({
		queryKey: ["access"],
		queryFn: async (): Promise<AccessResponse> => {
			try {
				return await get<AccessResponse>("access");
			} catch (e) {
				if (e instanceof ApiError && e.status === 404) return composeLegacy();
				throw e;
			}
		},
		refetchInterval: 10_000,
	});
}

export function useProviders(): ProviderInfo[] {
	const access = useAccess();
	return access.data?.providers?.length ? access.data.providers : FALLBACK_PROVIDERS;
}

export function useProvider(id: string) {
	const providers = useProviders();
	return providerInfo(id, providers);
}

function invalidateAccess(qc: ReturnType<typeof useQueryClient>) {
	return qc.invalidateQueries({ queryKey: ["access"] });
}

export function useUpdateAccess() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async ({ item, change }: { item: AccessItem; change: { ownerId?: string; planId?: string; monthlyUSD?: number; label?: string; disabled?: boolean } }) => {
			try {
				return await patch(`access/${encodeURIComponent(item.id)}`, change);
			} catch (e) {
				// Older servers: owner reassignment lives on the pool account endpoint; plans can't be stored.
				if (e instanceof ApiError && e.status === 404 && change.ownerId) return patch(`vault/accounts/${encodeURIComponent(item.id)}`, { ownerId: change.ownerId });
				if (e instanceof ApiError && e.status === 404) throw new ApiError(404, "This NeoProxy version can't store that change yet. It arrives with the server update that's on its way.");
				throw e;
			}
		},
		onSuccess: () => invalidateAccess(qc),
	});
}

export function useRemoveAccess() {
	const qc = useQueryClient();
	return useMutation({ mutationFn: (item: AccessItem) => del(`access/${encodeURIComponent(item.id)}`), onSuccess: () => invalidateAccess(qc) });
}

// A budget window lets virtual models draw from this access: tokens per period.
export function useConfigureWindow() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (v: { item: AccessItem; kind: WindowKind; capacityTokens: number; startsAt: number }) =>
			put(`vault/accounts/${encodeURIComponent(v.item.id)}/windows/${encodeURIComponent(v.kind)}`, { capacityTokens: v.capacityTokens, startsAt: v.startsAt }),
		onSuccess: () => invalidateAccess(qc),
	});
}

export function useReimport() {
	const qc = useQueryClient();
	return useMutation({ mutationFn: (item: AccessItem) => post(`access/${encodeURIComponent(item.id)}/reimport`), onSuccess: () => invalidateAccess(qc) });
}

export function useRefreshAccess() {
	const qc = useQueryClient();
	return useMutation({ mutationFn: (item: AccessItem) => post(`access/${encodeURIComponent(item.id)}/refresh`), onSuccess: () => invalidateAccess(qc) });
}

export function useAddApiKey() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (v: { provider: string; apiKey: string; baseUrl?: string; label?: string; ownerId?: string; monthlyUSD?: number }) => {
			try {
				return await post<{ item?: AccessItem }>("access/api-key", v);
			} catch (e) {
				if (e instanceof ApiError && e.status === 404) return post<{ accountId: string }>("vault/api-keys", { providerId: v.provider, apiKey: v.apiKey });
				throw e;
			}
		},
		onSuccess: () => invalidateAccess(qc),
	});
}

export function useUploadFile() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async ({ file, ownerId }: { file: File; ownerId?: string }) => {
			const body = new FormData();
			body.set("file", file);
			if (ownerId) body.set("ownerId", ownerId);
			const send = (path: string) => fetch(path, { method: "POST", body, credentials: "same-origin" });
			let res = await send("/v8/neo/access/file");
			if (res.status === 404) res = await send("/v8/management/credentials");
			if (!res.ok) {
				const data = await res.json().catch(() => ({}));
				throw new ApiError(res.status, data?.error?.message ?? data?.error ?? `${res.status} ${res.statusText}`);
			}
			return res.json().catch(() => ({}));
		},
		onSuccess: () => invalidateAccess(qc),
	});
}

// Subscription sign-in: server-side OAuth with state bound to this session.
export const signIn = {
	start: (provider: string) => post<{ url?: string; state: string; status?: string; user_code?: string; verification_uri?: string }>(`vault/login/${encodeURIComponent(provider)}`, {}),
	status: (state: string) => get<{ status: string; error?: string }>("vault/login/status", { query: { state } }),
	submitCode: (provider: string, state: string, code: string) => post("vault/login/callback", { provider, state, code }),
};

// ---------- NeoPi share ----------
export function useSources() {
	return useQuery({ queryKey: ["sources"], queryFn: () => get<NeoPiSource[]>("vault/sources") });
}
export function useSaveSource() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (v: { id?: string; label: string; url: string; bearer: string; ownerId: string; enabled?: boolean }) =>
			v.id ? put(`vault/sources/${encodeURIComponent(v.id)}`, v) : post("vault/sources", { ...v, enabled: v.enabled ?? true }),
		onSuccess: () => {
			qc.invalidateQueries({ queryKey: ["sources"] });
			invalidateAccess(qc);
		},
	});
}

// ---------- One-click NeoPi sharing ----------
export type LocalNeoPi = { found: boolean; running?: boolean; url?: string; name?: string; accountCount?: number; alreadyConnected?: string | null; reason?: string; startCommand?: string };
export function useLocalNeoPi(enabled: boolean) {
	return useQuery({
		queryKey: ["neopi-local"],
		queryFn: async (): Promise<LocalNeoPi | null> => {
			try {
				return await get<LocalNeoPi>("access/neopi/local");
			} catch (e) {
				// Older servers, or a non-admin session: no detection, manual paths remain.
				if (e instanceof ApiError && [403, 404, 405].includes(e.status)) return null;
				throw e;
			}
		},
		enabled,
		retry: false,
	});
}
export function useConnectLocalNeoPi() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (v: { ownerId?: string }) => post<{ label?: string; accountCount?: number }>("access/neopi/local/connect", v),
		onSuccess: () => {
			qc.invalidateQueries({ queryKey: ["neopi-local"] });
			qc.invalidateQueries({ queryKey: ["sources"] });
			invalidateAccess(qc);
		},
	});
}
export function useConnectLink() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (v: { connectionLink: string; ownerId: string; label?: string }) => post<{ label?: string; accountCount?: number }>("vault/sources", v),
		onSuccess: () => {
			qc.invalidateQueries({ queryKey: ["sources"] });
			invalidateAccess(qc);
		},
	});
}

// ---------- NeoPi import ----------
export type ImportCandidate = { credentialId: string; provider: string; neoproxyProvider: string | null; kind: "apiKey" | "subscription"; label: string; plan?: string; expiresAt?: number; importable: boolean; reason?: string; alreadyImported?: string | null };
export function useImportCandidates(profile: string | undefined, enabled: boolean) {
	return useQuery({
		queryKey: ["import", profile ?? ""],
		queryFn: () => get<{ profile: string; credentials: ImportCandidate[] }>("access/import/neopi", { query: { profile } }),
		enabled,
		retry: false,
	});
}
export function useImport() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (v: { profile?: string; credentialIds: string[]; ownerId?: string }) => post<{ items: AccessItem[]; failed?: Array<{ credentialId: string; error: string }> }>("access/import/neopi", v),
		onSuccess: () => invalidateAccess(qc),
	});
}

// ---------- requests (live activity for the mosaic glow) ----------
export type RequestRow = { id: string; at?: number; time?: number; userId?: string; asked?: string; accountId?: string; status?: string; [k: string]: unknown };
export function useRecentRequests(limit = 30, refetchInterval = 5_000) {
	return useQuery({ queryKey: ["requests", limit], queryFn: () => get<RequestRow[]>("requests", { query: { limit } }), refetchInterval });
}
