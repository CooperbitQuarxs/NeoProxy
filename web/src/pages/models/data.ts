import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { del, get, patch, post, put } from "../../api/client";

// Shapes served by /v8/neo/models, /vault, /virtual-models, /users, /usage and
// /simulate (Go: internal/neopool and internal/neoroute).
export type NeoModel = {
	id: string;
	providerId: string;
	name: string;
	display: string;
	served: string;
	tags: string[];
	context: number;
	virtualOnly?: boolean;
	exposed: boolean;
};
export type NeoProvider = { id: string; name: string; vendor: string; kind: string; sourceLabel?: string; exposed: boolean };
export type NeoWindow = { kind: string; known: boolean; used: number; resetsAt?: number };
export type NeoAccount = {
	id: string;
	providerId: string;
	ownerId: string;
	label: string;
	plan: string;
	state: string;
	sourceId?: string;
	modelIds: string[];
	windows: NeoWindow[];
	providerWindows?: NeoWindow[];
};
export type Vault = { providers: NeoProvider[]; accounts: NeoAccount[] };

export type Draw = "least-used" | "alternate" | "fill";
export type BudgetWindow = "5h" | "day" | "week" | "month";
export type Effort = "low" | "medium" | "high" | "max";
export type LuaPolicy = { eligible?: string; perAccount?: string; cap?: string };
export type Stage = {
	id: string;
	model: string;
	accounts: "all" | string[];
	draw: Draw;
	perAccount: number;
	cap: number;
	window: BudgetWindow;
	per: "user" | "all";
	effort: Effort | null;
	lua?: LuaPolicy;
};
export type TerminalKind = "limit" | "queue" | "passthrough";
export type Terminal = { kind: TerminalKind; message: string; model?: string };
export type VirtualModel = {
	id: string;
	name: string;
	served: string;
	description: string;
	stages: Stage[];
	terminal: Terminal;
	script?: string;
	updatedAt?: number;
	updatedBy?: string;
};
export type VirtualModelDraft = Omit<VirtualModel, "id"> & { id: string | null };

export type Grant = { virtualModel: string; allotment: number };
export type NeoUser = {
	id: string;
	handle: string;
	name: string;
	role: string;
	state: string;
	providerAccess: string[];
	grants: Grant[];
};
/** usage[userId][virtualModelId][stageId][accountId] = fraction of the window drawn. */
export type Usage = Record<string, Record<string, Record<string, Record<string, number>>>>;

export type Candidate = { accountId: string; used: number; budget: number };
export type TrailStep = {
	kind: "stage" | "terminal" | "direct" | "denied" | "unknown" | string;
	index?: number;
	stageId?: string;
	model?: string;
	total?: number;
	cap?: number;
	candidates?: Candidate[];
	chosen?: string;
	skipped?: string;
	terminal?: Terminal;
	providerId?: string;
	virtualModel?: string;
	accountId?: string;
	asked?: string;
	failureStatus?: number;
	policy?: { cap?: number; accounts: Array<{ accountId: string; eligible: boolean; limit?: number }> };
};
export type Decision = {
	outcome: "ok" | "limited" | "queued" | "denied" | "unknown" | "error" | string;
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
	trail: TrailStep[];
};
export type Protocol = "openai" | "openai-response" | "claude";

type RequestRow = { id: string; at: number; asked: string; outcome?: string; decision?: { model?: string } };

const K = {
	models: ["models", "catalogue"] as const,
	vault: ["models", "vault"] as const,
	vms: ["models", "virtual"] as const,
	vm: (id: string) => ["models", "virtual", id] as const,
	users: ["models", "users"] as const,
	user: (id: string) => ["models", "user", id] as const,
	usage: ["models", "usage"] as const,
	requests: ["models", "requests"] as const,
};

export const useModels = () => useQuery({ queryKey: K.models, queryFn: () => get<NeoModel[]>("models"), refetchInterval: 30_000 });
export const useVault = () => useQuery({ queryKey: K.vault, queryFn: () => get<Vault>("vault"), refetchInterval: 30_000 });
export const useVirtualModels = () => useQuery({ queryKey: K.vms, queryFn: () => get<VirtualModel[]>("virtual-models"), refetchInterval: 30_000 });
export const useVirtualModel = (id: string | undefined) =>
	useQuery({ queryKey: K.vm(id ?? ""), queryFn: () => get<VirtualModel>(`virtual-models/${encodeURIComponent(id!)}`), enabled: !!id, refetchOnWindowFocus: false });
export const useUsers = (enabled = true) => useQuery({ queryKey: K.users, queryFn: () => get<NeoUser[]>("users"), enabled });
export const useUser = (id: string) => useQuery({ queryKey: K.user(id), queryFn: () => get<NeoUser>(`users/${encodeURIComponent(id)}`) });
export const useUsage = () => useQuery({ queryKey: K.usage, queryFn: () => get<Usage>("usage"), refetchInterval: 30_000 });

const DAY = 86_400_000;
/** Requests per asked name over the last 24 hours, from the newest 200 rows. */
export function useRequestCounts() {
	return useQuery({
		queryKey: K.requests,
		queryFn: () => get<RequestRow[]>("requests", { query: { limit: 200 } }),
		refetchInterval: 30_000,
		select: (rows) => {
			const since = Date.now() - DAY;
			const recent = rows.filter((r) => r.at >= since);
			const byAsked = new Map<string, number>();
			const byModel = new Map<string, number>();
			for (const r of recent) {
				byAsked.set(r.asked, (byAsked.get(r.asked) ?? 0) + 1);
				if (r.decision?.model) byModel.set(r.decision.model, (byModel.get(r.decision.model) ?? 0) + 1);
			}
			// When every one of the 200 rows is recent, counts are a lower bound.
			return { byAsked, byModel, capped: rows.length === 200 && recent.length === rows.length };
		},
	});
}

function useInvalidate() {
	const qc = useQueryClient();
	return (...keys: ReadonlyArray<readonly string[]>) => Promise.all(keys.map((queryKey) => qc.invalidateQueries({ queryKey })));
}

export function useSaveVirtualModel() {
	const invalidate = useInvalidate();
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (vm: VirtualModelDraft) => {
			const body = { name: vm.name, description: vm.description, stages: vm.stages, terminal: vm.terminal, script: vm.script ?? "" };
			return vm.id ? put<VirtualModel>(`virtual-models/${encodeURIComponent(vm.id)}`, body) : post<VirtualModel>("virtual-models", body);
		},
		onSuccess: (saved) => {
			qc.setQueryData(K.vm(saved.id), saved);
			return invalidate(K.vms);
		},
	});
}

export function useDeleteVirtualModel() {
	const invalidate = useInvalidate();
	return useMutation({ mutationFn: (id: string) => del(`virtual-models/${encodeURIComponent(id)}`), onSuccess: () => invalidate(K.vms, K.users) });
}

export function useSetModelServed() {
	const invalidate = useInvalidate();
	return useMutation({
		mutationFn: ({ id, exposed }: { id: string; exposed: boolean }) => patch(`models/${encodeURIComponent(id)}`, { exposed }),
		onSuccess: () => invalidate(K.models, ["access"]),
	});
}

export function useSetProviderServed() {
	const invalidate = useInvalidate();
	return useMutation({
		mutationFn: ({ id, exposed }: { id: string; exposed: boolean }) => patch(`vault/providers/${encodeURIComponent(id)}`, { exposed }),
		onSuccess: () => invalidate(K.vault, K.models, ["access"]),
	});
}

export const simulate = (v: { asked: string; userId?: string; protocol?: Protocol; request?: unknown }) => post<Decision>("simulate", v);
