import { useMemo, useState } from "react";
import type { PageProps } from "../../app/App";
import { navigate } from "../../app/router";
import { ago } from "../../lib/format";
import { Avatar, Button, Chip, EmptyState, Notice, PageHeader, SearchInput, Section, Skeleton, StatusDot, Tip, cx } from "../../ui/components";
import { Icon } from "../../ui/icons";
import { useLedger, useUsers, useVirtualModels, type RequestRecord, type User, type VirtualModel } from "./api";
import { ROLE_COPY, ROLE_ORDER, STATE_COPY, displayName } from "./copy";
import { CreatePersonDialog } from "./CreatePerson";
import { PersonDetail } from "./PersonDetail";
import { SelfView } from "./SelfView";
import { Meter, RoleBadge } from "./widgets";
import "./people.css";

export function PeoplePage({ session, params }: PageProps) {
	if (session.role !== "admin") return <SelfView session={session} />;
	if (params[0]) return <PersonDetail key={params[0]} id={decodeURIComponent(params[0])} session={session} />;
	return <PeopleList session={session} />;
}

const LEDGER_WINDOW = 200;

function PeopleList({ session }: { session: PageProps["session"] }) {
	const users = useUsers();
	const vms = useVirtualModels();
	const ledger = useLedger({}, { limit: LEDGER_WINDOW });
	const [creating, setCreating] = useState(false);
	const [q, setQ] = useState("");

	const stats = useMemo(() => usageStats(ledger.data ?? []), [ledger.data]);
	const list = useMemo(() => {
		const all = [...(users.data ?? [])].sort((a, b) => (stats.last.get(b.id) ?? lastKeyUse(b)) - (stats.last.get(a.id) ?? lastKeyUse(a)) || a.handle.localeCompare(b.handle));
		const needle = q.trim().toLowerCase();
		return needle ? all.filter((u) => `${u.name} ${u.handle} ${ROLE_COPY[u.role]?.name}`.toLowerCase().includes(needle)) : all;
	}, [users.data, stats, q]);
	const total = users.data?.length ?? 0;

	return (
		<>
			<PageHeader
				title="People"
				lede="Everyone who can use the pool: what each person may ask for, their limits, and the inference keys their apps use."
				actions={
					<Button variant="primary" icon="plus" onClick={() => setCreating(true)}>
						Add a person
					</Button>
				}
			/>

			{users.isError && (
				<Notice tone="bad" title="Couldn't load people">
					{(users.error as Error).message}. NeoProxy may be restarting; this page retries on its own.
				</Notice>
			)}

			<Section
				title={total ? `${total === 1 ? "1 person" : `${total} people`}` : "Everyone"}
				description="Select someone to give them models, set limits, or issue and revoke their keys."
				actions={total > 6 ? <SearchInput value={q} onChange={setQ} placeholder="Find a person" /> : undefined}
			>
				<div className="panel people">
					<div className="people-head" aria-hidden="true">
						<span>Person</span>
						<span>Role</span>
						<span>Status</span>
						<span>Can use</span>
						<span>
							<Tip label={`Share of all tokens used across the last ${LEDGER_WINDOW} requests.`}>
								<span className="people-head-tip">Share of use</span>
							</Tip>
						</span>
						<span>Keys</span>
						<span>Last request</span>
					</div>
					{users.isPending ? (
						<ul className="people-rows">
							{[0, 1, 2].map((i) => (
								<li key={i} className="people-skel">
									<Skeleton w={34} h={34} r={17} />
									<Skeleton w="26%" h={14} />
									<Skeleton w="14%" h={14} />
									<Skeleton w="20%" h={14} />
								</li>
							))}
						</ul>
					) : list.length === 0 ? (
						<EmptyState title={q ? "Nobody matches" : "Nobody here yet"}>{q ? "Try part of their name or handle." : "Add the first person to start sharing the pool."}</EmptyState>
					) : (
						<ul className="people-rows">
							{list.map((u) => (
								<PersonRow key={u.id} user={u} self={u.id === session.userId} vms={vms.data ?? []} share={stats.share.get(u.id) ?? 0} last={Math.max(stats.last.get(u.id) ?? 0, lastKeyUse(u))} />
							))}
						</ul>
					)}
				</div>
				{total === 1 && !users.isPending && (
					<p className="people-hint muted">
						It's just you so far. <button className="link-btn" onClick={() => setCreating(true)}>Add a person</button> to share the pool, or add a service for an app that runs on its own.
					</p>
				)}
			</Section>

			<Section title="Roles" description="What each role can do. A person has one role; change it on their page at any time.">
				<ul className="role-legend">
					{ROLE_ORDER.map((r) => (
						<li key={r} className="panel role-legend-item">
							<span className="role-legend-name">
								<span className={`role-dot role-${r}`} aria-hidden="true" />
								{ROLE_COPY[r].name}
							</span>
							<p className="muted">{ROLE_COPY[r].can}</p>
						</li>
					))}
				</ul>
			</Section>

			<CreatePersonDialog open={creating} onOpenChange={setCreating} />
		</>
	);
}

function lastKeyUse(u: User) {
	return Math.max(0, ...(u.keys ?? []).map((k) => k.lastUsedAt ?? 0));
}

function usageStats(rows: RequestRecord[]) {
	const tokens = new Map<string, number>();
	const last = new Map<string, number>();
	let total = 0;
	for (const r of rows) {
		const t = (r.tokens?.in ?? 0) + (r.tokens?.out ?? 0);
		tokens.set(r.userId, (tokens.get(r.userId) ?? 0) + t);
		total += t;
		if (r.at > (last.get(r.userId) ?? 0)) last.set(r.userId, r.at);
	}
	const share = new Map<string, number>();
	for (const [id, t] of tokens) share.set(id, total ? t / total : 0);
	return { share, last };
}

function canUseText(u: User, vms: VirtualModel[]) {
	const names = (u.grants ?? []).map((g) => vms.find((v) => v.id === g.virtualModel)?.name).filter(Boolean) as string[];
	if (!names.length) return undefined;
	if (names.length <= 2) return names.join(" and ");
	return `${names[0]}, ${names[1]} and ${names.length - 2} more`;
}

function PersonRow({ user: u, self, vms, share, last }: { user: User; self: boolean; vms: VirtualModel[]; share: number; last: number }) {
	const st = STATE_COPY[u.state] ?? STATE_COPY.active;
	const canUse = canUseText(u, vms);
	const keys = u.keys?.length ?? 0;
	return (
		<li>
			<button type="button" className={cx("person-row", u.state === "suspended" && "is-suspended")} onClick={() => navigate(`people/${encodeURIComponent(u.id)}`)}>
				<span className="person-who">
					<Avatar name={u.name || u.handle} size={34} />
					<span className="person-who-text">
						<strong>
							<span className="person-name">{displayName(u)}</span>
							{self && <Chip tone="accent">You</Chip>}
						</strong>
						<code className="faint">{u.handle}</code>
					</span>
				</span>
				<span className="person-role">
					<RoleBadge role={u.role} />
				</span>
				<span className="person-state">
					<StatusDot tone={st.tone} />
					{st.name}
				</span>
				<span className={cx("person-can", !canUse && "faint")}>{canUse ?? (u.providerAccess?.length ? "Direct provider models" : "Nothing yet")}</span>
				<span className="person-share">
					<Meter value={share} label={`${Math.round(share * 100)}% of recent use`} />
					<span className="num faint">{share ? `${share < 0.01 ? "<1" : Math.round(share * 100)}%` : "0%"}</span>
				</span>
				<span className={cx("person-keys", !keys && "faint")}>
					<Icon name="key" size={14} />
					<span className="num">{keys}</span>
				</span>
				<span className="person-last faint">{last ? ago(last) : "Never"}</span>
				<Icon name="chevronRight" size={16} className="faint person-chev" />
			</button>
		</li>
	);
}
