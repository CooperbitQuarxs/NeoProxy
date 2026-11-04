import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { del, get, post, put } from "../../api/client";

// Identity, grants, keys and the request ledger. Shapes mirror internal/neo and
// internal/neoroute on the server (users.go, types.go, execute.go).

export type Role = "admin" | "owner" | "member" | "service";
export type UserState = "active" | "invited" | "suspended";
export type Grant = { virtualModel: string; allotment: number };
export type Limits = { rpm: number | null; tokensPerDay: number | null };
export type InferenceKey = { id: string; prefix: string; label: string; accountIds?: string[]; createdAt: number; lastUsedAt: number | null; expiresAt?: number | null };
export type User = {
	id: string;
	handle: string;
	name: string;
	role: Role;
	state: UserState;
	keys: InferenceKey[];
	providerAccess: string[];
	grants: Grant[];
	limits: Limits;
	createdAt: number;
};
export type UserInput = Pick<User, "handle" | "name" | "role" | "state" | "providerAccess" | "grants" | "limits"> & { password?: string };

export type Stage = { id: string; model: string; accounts: "all" | string[]; draw: string; perAccount: number; cap: number; window: string; per: string; effort: string | null };
export type VirtualModel = { id: string; name: string; served: string; description: string; stages: Stage[]; terminal: { kind: string; message: string; model?: string } };
export type PoolModel = { id: string; providerId: string; name: string; display: string; served: string; exposed: boolean };
export type VaultAccount = { id: string; providerId: string; ownerId?: string; label: string; plan?: string; state: string; modelIds?: string[] };
export type VaultProvider = { id: string; name: string; vendor?: string; kind?: string; exposed: boolean };
export type Vault = { accounts: VaultAccount[]; providers: VaultProvider[] };
/** userId -> virtual model -> stage -> account -> fraction of the window drawn. */
export type Usage = Record<string, Record<string, Record<string, Record<string, number>>>>;

export type TrailStep = {
	kind: "stage" | "direct" | "denied" | "terminal" | "no-model" | string;
	index?: number;
	stageId?: string;
	model?: string;
	total?: number;
	cap?: number;
	candidates?: Array<{ accountId: string; used: number; budget: number }>;
	chosen?: string;
	skipped?: string;
	terminal?: { kind: string; message: string; model?: string };
	providerId?: string;
	virtualModel?: string;
	accountId?: string;
	asked?: string;
	failureStatus?: number;
	policy?: unknown;
};
export type Decision = {
	outcome: string;
	direct?: boolean;
	virtualModel?: string;
	stageIndex?: number;
	stageId?: string;
	model?: string;
	accountId?: string;
	effort?: string | null;
	reason?: string;
	message?: string;
	resetsAt?: number;
	trail: TrailStep[] | null;
};
export type Outcome = "ok" | "limited" | "queued" | "error";
export type RequestRecord = {
	id: string;
	at: number;
	userId: string;
	keyId: string | null;
	client: string;
	asked: string;
	decision: Decision;
	tokens: { in: number; out: number; cached: number };
	latencyMs: number;
	outcome: Outcome | string;
	stream: boolean;
};

const enc = encodeURIComponent;
export const keys = {
	users: ["neo", "users"] as const,
	user: (id: string) => ["neo", "users", id] as const,
	requests: ["neo", "requests"] as const,
};

export function useUsers(enabled = true) {
	return useQuery({ queryKey: keys.users, queryFn: () => get<User[]>("users"), enabled, refetchInterval: 15_000 });
}
export function useUser(id: string | undefined) {
	return useQuery({ queryKey: keys.user(id ?? ""), queryFn: () => get<User>(`users/${enc(id!)}`), enabled: !!id });
}
export function useVirtualModels() {
	return useQuery({ queryKey: ["neo", "virtual-models"], queryFn: () => get<VirtualModel[]>("virtual-models"), staleTime: 30_000 });
}
export function usePoolModels() {
	return useQuery({ queryKey: ["neo", "models"], queryFn: () => get<PoolModel[]>("models"), staleTime: 60_000 });
}
export function useVault() {
	return useQuery({ queryKey: ["neo", "vault"], queryFn: () => get<Vault>("vault"), staleTime: 30_000 });
}
export function useUsage() {
	return useQuery({ queryKey: ["neo", "usage"], queryFn: () => get<Usage>("usage"), refetchInterval: 30_000 });
}

export type LedgerFilter = { userId?: string; outcome?: string };
export function useLedger(filter: LedgerFilter, opts: { limit?: number; live?: boolean; enabled?: boolean } = {}) {
	const { limit = 100, live = true, enabled = true } = opts;
	return useQuery({
		queryKey: [...keys.requests, filter.userId ?? "", filter.outcome ?? "", limit],
		queryFn: () => get<RequestRecord[]>("requests", { query: { limit, userId: filter.userId, outcome: filter.outcome } }),
		refetchInterval: live ? 5_000 : false,
		enabled,
	});
}
export function fetchOlder(filter: LedgerFilter, before: number, limit = 100) {
	return get<RequestRecord[]>("requests", { query: { limit, userId: filter.userId, outcome: filter.outcome, before } });
}
export function useRequestRecord(id: string | undefined, known?: RequestRecord) {
	return useQuery({ queryKey: [...keys.requests, "one", id ?? ""], queryFn: () => get<RequestRecord>(`requests/${enc(id!)}`), enabled: !!id && !known, initialData: known });
}

export function toInput(u: User): UserInput {
	return { handle: u.handle, name: u.name, role: u.role, state: u.state, providerAccess: u.providerAccess ?? [], grants: u.grants ?? [], limits: u.limits ?? { rpm: null, tokensPerDay: null } };
}

function useUserMutation<V, R>(fn: (v: V) => Promise<R>) {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: fn,
		onSuccess: (data) => {
			if (data && typeof data === "object" && "id" in data && "handle" in data) qc.setQueryData(keys.user((data as unknown as User).id), data);
			return qc.invalidateQueries({ queryKey: keys.users });
		},
	});
}

export function useCreateUser() {
	return useUserMutation((input: UserInput) => post<User>("users", input));
}
/** The server replaces a person's whole policy, so every change sends the full record. */
export function useUpdateUser() {
	return useUserMutation(({ user, change }: { user: User; change: Partial<UserInput> }) => put<User>(`users/${enc(user.id)}`, { ...toInput(user), ...change }));
}
export function useDeleteUser() {
	return useUserMutation((user: User) => del(`users/${enc(user.id)}`));
}
export function useSetPassword() {
	// A wrong current password answers 401; that must not end the dashboard session.
	return useUserMutation(({ userId, password, currentPassword }: { userId: string; password: string; currentPassword?: string }) => post(`users/${enc(userId)}/password`, { password, currentPassword }, { quiet401: true }));
}
export function useMintKey() {
	return useUserMutation(({ userId, label, accountIds }: { userId: string; label: string; accountIds?: string[] }) =>
		post<{ key: InferenceKey; secret: string }>(`users/${enc(userId)}/keys`, { label, ...(accountIds ? { accountIds } : {}) }),
	);
}
export function useRevokeKey() {
	return useUserMutation(({ userId, keyId }: { userId: string; keyId: string }) => del(`users/${enc(userId)}/keys/${enc(keyId)}`));
}
