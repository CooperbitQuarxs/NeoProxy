import { useEffect, useState } from "react";
import { navigate } from "../../app/router";
import { providerColor, providerInfo } from "../../lib/providers";
import { Avatar, Button, CopyButton, Dialog, Skeleton, cx } from "../../ui/components";
import type { RequestRecord } from "../people/api";
import { fmtDuration } from "../people/widgets";
import { answered, fmt, outcomeOf, story, totalTokens, type Lookup, type Tone } from "./story";

const timeFmt = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
const dayFmt = new Intl.DateTimeFormat(undefined, { weekday: "long", month: "long", day: "numeric" });
const fullFmt = new Intl.DateTimeFormat(undefined, { weekday: "short", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" });

export function dayLabel(at: number) {
	const d = new Date(at);
	const today = new Date();
	const y = new Date();
	y.setDate(today.getDate() - 1);
	if (d.toDateString() === today.toDateString()) return "Today";
	if (d.toDateString() === y.toDateString()) return "Yesterday";
	return dayFmt.format(d);
}

export function OutcomeTag({ outcome }: { outcome: string }) {
	const o = outcomeOf(outcome);
	return (
		<span className={cx("otag", `otag-${o.tone}`)}>
			<span className="otag-dot" aria-hidden="true" />
			{o.word}
		</span>
	);
}

export function Answered({ r, L }: { r: RequestRecord; L: Lookup }) {
	const a = answered(r, L);
	if (a.model)
		return (
			<span className="answered">
				<span className="answered-swatch" style={{ background: providerColor(a.provider ?? "") }} aria-hidden="true" />
				<span className="answered-text">
					<span className="answered-model">{a.model}</span>
					{a.detail && <span className="faint answered-detail">{a.detail}</span>}
				</span>
			</span>
		);
	return <span className={cx("answered-why", r.outcome === "error" ? "is-bad" : "")}>{a.text || "Not answered"}</span>;
}

/** One ledger row as a button; `compact` drops the person and numbers. */
export function LedgerRow({ r, L, showPerson, compact, fresh, selected, onOpen }: { r: RequestRecord; L: Lookup; showPerson?: boolean; compact?: boolean; fresh?: boolean; selected?: boolean; onOpen: (r: RequestRecord) => void }) {
	const vm = L.vm(r.decision.virtualModel);
	const tokens = totalTokens(r);
	return (
		<li className={cx("lrow", compact && "is-compact", showPerson && "has-person", fresh && "is-fresh", selected && "is-selected")}>
			<button type="button" onClick={() => onOpen(r)} aria-label={`${outcomeOf(r.outcome).word} request for ${r.asked} at ${timeFmt.format(r.at)}`}>
				<span className="lrow-time num">{timeFmt.format(r.at)}</span>
				{showPerson && (
					<span className="lrow-person">
						<Avatar name={L.person(r.userId)} size={22} />
						<span className="lrow-ellipsis">{L.person(r.userId)}</span>
					</span>
				)}
				<span className="lrow-asked">
					<span className="lrow-ellipsis">{vm?.name ?? r.asked.replace(/^neoproxy\//, "")}</span>
					{!compact && vm && <code className="faint lrow-ellipsis">{r.asked}</code>}
				</span>
				<span className="lrow-answer">
					<Answered r={r} L={L} />
				</span>
				<span className="lrow-outcome">
					<OutcomeTag outcome={r.outcome} />
				</span>
				{!compact && (
					<>
						<span className="lrow-num num">{tokens ? fmt(tokens) : <span className="faint">none</span>}</span>
						<span className="lrow-num num">{r.latencyMs ? fmtDuration(r.latencyMs) : ""}</span>
					</>
				)}
			</button>
		</li>
	);
}

export function LedgerSkeleton({ rows = 6 }: { rows?: number }) {
	return (
		<ul className="ledger" aria-busy="true">
			{Array.from({ length: rows }, (_, i) => (
				<li key={i} className="lrow-skel">
					<Skeleton w={64} h={12} />
					<Skeleton w="22%" h={12} />
					<Skeleton w="30%" h={12} />
					<Skeleton w={60} h={18} r={9} />
				</li>
			))}
		</ul>
	);
}

/** Side sheet that tells the routing story of one request. */
export function RequestSheet({ record, L, admin, onClose, onFilterPerson }: { record?: RequestRecord; L: Lookup; admin: boolean; onClose: () => void; onFilterPerson?: (id: string) => void }) {
	const [shown, setShown] = useState(record);
	useEffect(() => {
		if (record) setShown(record);
	}, [record]);
	const r = record ?? shown;
	const vm = r ? L.vm(r.decision.virtualModel) : undefined;
	return (
		<Dialog
			sheet
			open={!!record}
			onOpenChange={(v) => !v && onClose()}
			title={r ? <span className="rs-title">{vm?.name ?? r.asked}</span> : ""}
			description={r ? `${admin ? `${L.person(r.userId)}, ` : ""}${fullFmt.format(r.at)}` : undefined}
		>
			{r && <SheetBody r={r} L={L} admin={admin} onClose={onClose} onFilterPerson={onFilterPerson} />}
		</Dialog>
	);
}

const TONE_CLASS: Record<Tone, string> = { good: "is-good", warn: "is-warn", bad: "is-bad", accent: "is-accent", idle: "is-idle" };

function SheetBody({ r, L, admin, onClose, onFilterPerson }: { r: RequestRecord; L: Lookup; admin: boolean; onClose: () => void; onFilterPerson?: (id: string) => void }) {
	const steps = story(r, L, admin);
	const key = L.keyLabel(r.userId, r.keyId);
	const raw = JSON.stringify(r, null, 2);
	const o = outcomeOf(r.outcome);
	return (
		<>
			<div className={cx("rs-outcome", TONE_CLASS[o.tone])}>
				<OutcomeTag outcome={r.outcome} />
				<span className="muted">{r.outcome === "ok" ? `in ${fmtDuration(r.latencyMs)}` : steps[steps.length - 1].title.replace(/^[^:]+:\s*/, "")}</span>
			</div>

			<section>
				<h3 className="rs-h3">How it was routed</h3>
				<ol className="story">
					{steps.map((s, i) => (
						<li key={i} className={cx("story-step", TONE_CLASS[s.tone])} style={{ ["--i" as string]: i }}>
							<span className="story-node" aria-hidden="true" style={s.provider ? { ["--p" as string]: providerColor(s.provider) } : undefined} />
							<div className="story-text">
								<strong>{s.title}</strong>
								{s.mono && s.mono !== s.title && <code className="faint story-mono">{s.mono}</code>}
								{s.detail && <p className="muted">{s.detail}</p>}
								{s.provider && <span className="story-provider faint">{providerInfo(s.provider).name}</span>}
							</div>
						</li>
					))}
				</ol>
			</section>

			<dl className="rs-facts">
				{admin && (
					<div>
						<dt>Person</dt>
						<dd className="row">
							<Avatar name={L.person(r.userId)} size={20} />
							{L.person(r.userId)}
						</dd>
					</div>
				)}
				<div>
					<dt>Key</dt>
					<dd>{key ?? (r.keyId ? "A key that has since been revoked" : "No key recorded")}</dd>
				</div>
				{r.client && (
					<div>
						<dt>App</dt>
						<dd className="mono rs-wrap">{r.client}</dd>
					</div>
				)}
				<div>
					<dt>Tokens</dt>
					<dd className="num">{r.outcome === "ok" ? `${fmt(r.tokens?.in ?? 0)} in, ${fmt(r.tokens?.out ?? 0)} out${r.tokens?.cached ? `, ${fmt(r.tokens.cached)} from cache` : ""}` : "None used"}</dd>
				</div>
				<div>
					<dt>Time taken</dt>
					<dd className="num">{fmtDuration(r.latencyMs) || "Not recorded"}</dd>
				</div>
				<div>
					<dt>Delivery</dt>
					<dd>{r.stream ? "Streamed as it was written" : "Sent in one piece"}</dd>
				</div>
				{r.decision.effort && (
					<div>
						<dt>Reasoning effort</dt>
						<dd>{r.decision.effort}</dd>
					</div>
				)}
			</dl>

			<details className="rs-raw">
				<summary>Raw record</summary>
				<div className="rs-raw-body">
					<div className="row between">
						<code className="faint">{r.id}</code>
						<CopyButton text={raw} label="Copy raw record" />
					</div>
					<pre>
						<code>{raw}</code>
					</pre>
				</div>
			</details>

			{admin && (
				<div className="row wrap rs-actions">
					<Button
						onClick={() => {
							onClose();
							navigate(`people/${encodeURIComponent(r.userId)}`);
						}}
					>
						Open {L.person(r.userId)}
					</Button>
					{onFilterPerson && (
						<Button variant="ghost" onClick={() => onFilterPerson(r.userId)}>
							Only show their requests
						</Button>
					)}
				</div>
			)}
		</>
	);
}
