import { useEffect, useMemo, useRef, useState } from "react";
import type { PageProps } from "../../app/App";
import { navigate } from "../../app/router";
import { Avatar, Button, EmptyState, Notice, PageHeader, SearchInput, Select, cx } from "../../ui/components";
import { Icon } from "../../ui/icons";
import { fetchOlder, useLedger, useRequestRecord, type RequestRecord } from "../people/api";
import { fmtDuration, Segmented } from "../people/widgets";
import { LedgerRow, LedgerSkeleton, RequestSheet, dayLabel } from "./Ledger";
import { useLookup } from "./lookup";
import { fmt, totalTokens } from "./story";
import "./activity.css";

const PAGE = 100;
const OUTCOMES = [
	{ value: "", label: "All" },
	{ value: "ok", label: "Served" },
	{ value: "limited", label: "Limited" },
	{ value: "queued", label: "Queued" },
	{ value: "error", label: "Failed" },
];

export function ActivityPage({ session, params, query }: PageProps) {
	const { lookup, people, admin } = useLookup(session);
	const userId = admin ? query.get("user") ?? "" : "";
	const outcome = query.get("outcome") ?? "";
	const selectedId = params[0] ? decodeURIComponent(params[0]) : undefined;
	const [search, setSearch] = useState("");
	const [live, setLive] = useState(true);
	const filter = useMemo(() => ({ userId: userId || undefined, outcome: outcome || undefined }), [userId, outcome]);
	const ledger = useLedger(filter, { limit: PAGE, live });

	// Older pages are fetched on demand and stay put while the newest page polls.
	const [older, setOlder] = useState<RequestRecord[]>([]);
	const [olderDone, setOlderDone] = useState(false);
	const [loadingOlder, setLoadingOlder] = useState(false);
	useEffect(() => {
		setOlder([]);
		setOlderDone(false);
	}, [userId, outcome]);

	// Rows that arrived since the last poll glow once.
	const seen = useRef<Set<string> | null>(null);
	const [fresh, setFresh] = useState<Set<string>>(new Set());
	useEffect(() => {
		seen.current = null;
	}, [userId, outcome]);
	useEffect(() => {
		const rows = ledger.data;
		if (!rows) return;
		if (seen.current) {
			const added = rows.filter((r) => !seen.current!.has(r.id)).map((r) => r.id);
			if (added.length) {
				setFresh(new Set(added));
				const t = setTimeout(() => setFresh(new Set()), 2400);
				rows.forEach((r) => seen.current!.add(r.id));
				return () => clearTimeout(t);
			}
		} else seen.current = new Set(rows.map((r) => r.id));
	}, [ledger.data]);

	const all = useMemo(() => {
		const out: RequestRecord[] = [];
		const ids = new Set<string>();
		for (const r of [...(ledger.data ?? []), ...older]) if (!ids.has(r.id)) ids.add(r.id), out.push(r);
		return out;
	}, [ledger.data, older]);
	const needle = search.trim().toLowerCase();
	const rows = needle ? all.filter((r) => [r.asked, lookup.vm(r.decision.virtualModel)?.name ?? "", lookup.model(r.decision.model)].join(" ").toLowerCase().includes(needle)) : all;

	const selectedInList = all.find((r) => r.id === selectedId);
	const selected = useRequestRecord(selectedId, selectedInList);
	const filtered = !!(userId || outcome || needle);

	const setQuery = (next: { user?: string; outcome?: string }, id = selectedId) => {
		const q = new URLSearchParams();
		const u = next.user ?? userId;
		const o = next.outcome ?? outcome;
		if (u) q.set("user", u);
		if (o) q.set("outcome", o);
		const qs = q.toString();
		navigate(`activity${id ? `/${encodeURIComponent(id)}` : ""}${qs ? `?${qs}` : ""}`);
	};
	const open = (r?: RequestRecord) => setQuery({}, r?.id);

	const loadOlder = async () => {
		const last = all[all.length - 1];
		if (!last) return;
		setLoadingOlder(true);
		try {
			const page = await fetchOlder(filter, last.at, PAGE);
			setOlder((o) => [...o, ...page]);
			if (page.length < PAGE) setOlderDone(true);
		} finally {
			setLoadingOlder(false);
		}
	};
	const canLoadOlder = !olderDone && (ledger.data?.length ?? 0) >= PAGE;

	return (
		<>
			<PageHeader
				title="Activity"
				lede={admin ? "Every request to NeoProxy: who asked, what they asked for, and which account answered. Select a request to see how it was routed." : "Your requests and how each one was answered. Select one to see how it was routed."}
				actions={
					<button type="button" className={cx("live", live && "is-live")} onClick={() => setLive((v) => !v)} aria-pressed={live}>
						<span className="live-dot" aria-hidden="true" />
						{live ? "Live" : "Paused"}
						<span className="faint">{live ? "Pause" : "Resume"}</span>
					</button>
				}
			/>

			<div className="act-toolbar">
				{admin && (
					<Select
						ariaLabel="Whose requests"
						className="act-person"
						value={userId || "__all"}
						onValueChange={(v) => setQuery({ user: v === "__all" ? "" : v })}
						options={[
							{ value: "__all", label: "Everyone", leading: <span className="act-everyone" aria-hidden="true"><Icon name="people" size={14} /></span> },
							...people.map((p) => ({ value: p.id, label: p.name || p.handle, leading: <Avatar name={p.name || p.handle} size={20} /> })),
						]}
					/>
				)}
				<Segmented label="Outcome" value={outcome} onChange={(v) => setQuery({ outcome: v })} options={OUTCOMES} />
				<div className="act-search">
					<SearchInput value={search} onChange={setSearch} placeholder="Filter by model" />
				</div>
			</div>

			{ledger.isError && (
				<Notice tone="bad" title="Couldn't load activity">
					{(ledger.error as Error).message}. NeoProxy may be restarting; this page keeps retrying on its own.
				</Notice>
			)}

			{ledger.isPending ? (
				<div className="panel act-panel">
					<LedgerSkeleton rows={8} />
				</div>
			) : rows.length === 0 ? (
				<div className="panel act-panel">
					{filtered ? (
						<EmptyState
							title="Nothing matches these filters"
							action={
								<Button
									onClick={() => {
										setSearch("");
										setQuery({ user: "", outcome: "" });
									}}
								>
									Clear filters
								</Button>
							}
						>
							{needle ? "Only loaded requests are searched. " : ""}Try a different person, outcome or model name.
						</EmptyState>
					) : (
						<EmptyState
							art={
								<span className="act-empty-art" aria-hidden="true">
									<Icon name="activity" size={28} />
								</span>
							}
							title="No requests yet"
							action={
								<Button variant="primary" onClick={() => navigate(admin ? "people" : "people")}>
									{admin ? "Go to People" : "Go to your keys"}
								</Button>
							}
						>
							Requests appear here as soon as an app uses an inference key. {admin ? "Issue a key to someone on the People page, then point their app at NeoProxy." : "Issue yourself a key, then point an app at NeoProxy."}
						</EmptyState>
					)}
				</div>
			) : (
				<>
					<Summary rows={rows} />
					<div className="panel act-panel">
						<div className={cx("lhead", admin && "has-person")} aria-hidden="true">
							<span>Time</span>
							{admin && <span>Person</span>}
							<span>Asked for</span>
							<span>Answered by</span>
							<span>Outcome</span>
							<span className="lhead-num">Tokens</span>
							<span className="lhead-num">Time taken</span>
						</div>
						{groupByDay(rows).map(([day, list]) => (
							<section key={day} aria-label={day}>
								<h2 className="lday">{day}</h2>
								<ul className="ledger">
									{list.map((r) => (
										<LedgerRow key={r.id} r={r} L={lookup} showPerson={admin} fresh={fresh.has(r.id)} selected={r.id === selectedId} onOpen={open} />
									))}
								</ul>
							</section>
						))}
						{canLoadOlder && (
							<div className="act-more">
								<Button busy={loadingOlder} onClick={loadOlder}>
									Show older requests
								</Button>
							</div>
						)}
					</div>
				</>
			)}

			<RequestSheet
				record={selectedId ? selected.data : undefined}
				L={lookup}
				admin={admin}
				onClose={() => open(undefined)}
				onFilterPerson={admin ? (id) => setQuery({ user: id }, undefined) : undefined}
			/>
			{selectedId && selected.isError && (
				<Notice tone="warn" title="That request isn't in the ledger">
					It may belong to someone else, or the link is wrong.{" "}
					<button className="link-btn" onClick={() => open(undefined)}>
						Close it
					</button>
				</Notice>
			)}
		</>
	);
}

function groupByDay(rows: RequestRecord[]) {
	const groups: Array<[string, RequestRecord[]]> = [];
	for (const r of rows) {
		const label = dayLabel(r.at);
		const last = groups[groups.length - 1];
		if (last && last[0] === label) last[1].push(r);
		else groups.push([label, [r]]);
	}
	return groups;
}

function median(xs: number[]) {
	if (!xs.length) return 0;
	const s = [...xs].sort((a, b) => a - b);
	return s[Math.floor(s.length / 2)];
}

/** Totals over the requests on screen, with an outcome bar. */
function Summary({ rows }: { rows: RequestRecord[] }) {
	const counts = { ok: 0, limited: 0, queued: 0, error: 0 } as Record<string, number>;
	for (const r of rows) counts[r.outcome] = (counts[r.outcome] ?? 0) + 1;
	const served = rows.filter((r) => r.outcome === "ok");
	const tokens = rows.reduce((n, r) => n + totalTokens(r), 0);
	const lat = median(served.map((r) => r.latencyMs).filter(Boolean));
	const order: Array<[string, string]> = [
		["ok", "served"],
		["limited", "limited"],
		["queued", "queued"],
		["error", "failed"],
	];
	return (
		<div className="act-summary">
			<div className="act-stat">
				<span className="act-stat-num num">{fmt(rows.length)}</span>
				<span className="faint">{rows.length === 1 ? "request shown" : "requests shown"}</span>
			</div>
			<div className="act-stat">
				<span className="act-stat-num num">{rows.length ? `${Math.round((served.length / rows.length) * 100)}%` : "–"}</span>
				<span className="faint">served</span>
			</div>
			<div className="act-stat">
				<span className="act-stat-num num">{lat ? fmtDuration(lat) : "–"}</span>
				<span className="faint">typical time to answer</span>
			</div>
			<div className="act-stat">
				<span className="act-stat-num num">{fmt(tokens)}</span>
				<span className="faint">tokens</span>
			</div>
			<div className="act-bar-wrap">
				<div className="act-bar" role="img" aria-label={order.map(([k, w]) => `${counts[k] ?? 0} ${w}`).join(", ")}>
					{order.map(([k]) => (counts[k] ? <span key={k} className={`act-bar-seg seg-${k}`} style={{ flexGrow: counts[k] }} /> : null))}
				</div>
				<div className="act-bar-legend">
					{order.map(([k, w]) => (
						<span key={k} className={cx("act-legend", !counts[k] && "is-zero")}>
							<span className={`act-legend-dot seg-${k}`} aria-hidden="true" />
							<span className="num">{counts[k] ?? 0}</span> {w}
						</span>
					))}
				</div>
			</div>
		</div>
	);
}
