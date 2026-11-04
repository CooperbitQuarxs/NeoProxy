import { useState } from "react";
import { useConfigureWindow } from "../api/queries";
import type { AccessItem, UsageWindow, WindowKind } from "../api/types";
import { fmtCompact, fmtInt } from "../lib/format";
import { Button, Field, Input, Notice, cx } from "../ui/components";
import { useToast } from "../ui/toast";

export const PERIODS: Array<{ kind: WindowKind; label: string; every: string }> = [
	{ kind: "5h", label: "5 hours", every: "every 5 hours" },
	{ kind: "day", label: "Day", every: "every day" },
	{ kind: "week", label: "Week", every: "every week" },
	{ kind: "month", label: "Month", every: "every month" },
];
const PRESETS = [1_000_000, 5_000_000, 20_000_000, 100_000_000];

export function periodName(kind: string) {
	return PERIODS.find((p) => p.kind === kind)?.label.toLowerCase() ?? kind;
}

export const configuredWindows = (item: AccessItem) => (item.windows ?? []).filter((w) => w.source === "configured-metered" || w.unit === "tokens");
export const hasBudget = (item: AccessItem) => configuredWindows(item).length > 0;

function until(ts?: number) {
	if (!ts) return "";
	const m = Math.round((ts - Date.now()) / 60000);
	if (m <= 0) return "resets now";
	if (m < 60) return `resets in ${m} min`;
	const h = Math.round(m / 60);
	if (h < 48) return `resets in ${h} h`;
	return `resets in ${Math.round(h / 24)} days`;
}

function Meter({ w, onEdit }: { w: UsageWindow; onEdit?: () => void }) {
	const frac = Math.max(0, Math.min(1, w.used ?? (w.capacity ? (w.usedTokens ?? 0) / w.capacity : 0)));
	const tone = frac >= 0.9 ? "bad" : frac >= 0.7 ? "warn" : "good";
	return (
		<div className="meter">
			<div className="meter-head">
				<strong>Per {periodName(w.kind)}</strong>
				<span className="muted num">
					{w.unit === "tokens" && w.capacity ? `${fmtCompact(w.usedTokens ?? 0)} of ${fmtCompact(w.capacity)} tokens` : w.known ? `${Math.round(frac * 100)}% used` : "Usage unknown"}
				</span>
				<span className="grow" />
				<span className="faint">{until(w.resetsAt)}</span>
				{onEdit && (
					<Button size="sm" variant="ghost" onClick={onEdit}>
						Change
					</Button>
				)}
			</div>
			<div className={cx("meter-bar", `tone-${tone}`)} role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(frac * 100)} aria-label={`Per ${periodName(w.kind)} budget used`}>
				<span style={{ width: `${frac * 100}%` }} />
			</div>
		</div>
	);
}

export function BudgetSection({ item, canEdit }: { item: AccessItem; canEdit: boolean }) {
	const configured = configuredWindows(item);
	const provider = (item.providerWindows ?? []).filter((w) => w.known);
	const [editing, setEditing] = useState<WindowKind | null>(null);
	return (
		<div className="budget">
			<h3 className="sheet-h3">Budget for virtual models</h3>
			<p className="muted budget-lede">
				Virtual models only draw from this access within a token budget you set here. NeoProxy counts input and output tokens against it and resets it on schedule. The provider's own limits still apply on top.
			</p>
			{configured.length === 0 && !editing && (
				<Notice
					tone="warn"
					title="No budget yet, so virtual models skip this access"
					action={canEdit ? <Button size="sm" variant="primary" onClick={() => setEditing("day")}>Set a budget</Button> : undefined}
				>
					People granted the provider directly can still use it. Set a budget to let virtual models route here.
				</Notice>
			)}
			{configured.map((w) => (editing === w.kind ? null : <Meter key={w.kind} w={w} onEdit={canEdit ? () => setEditing(w.kind as WindowKind) : undefined} />))}
			{editing && <BudgetEditor item={item} initial={configured.find((w) => w.kind === editing)} initialKind={editing} onDone={() => setEditing(null)} />}
			{canEdit && configured.length > 0 && !editing && configured.length < PERIODS.length && (
				<Button size="sm" icon="plus" onClick={() => setEditing(PERIODS.find((p) => !configured.some((w) => w.kind === p.kind))!.kind)}>
					Add another period
				</Button>
			)}
			{provider.length > 0 && (
				<div className="provider-quota">
					<p className="field-label">Reported by the provider</p>
					{provider.map((w) => (
						<Meter key={`p-${w.kind}`} w={w} />
					))}
					<p className="faint">This is the provider's own count. NeoProxy can't change it; it only avoids access that's near its limit.</p>
				</div>
			)}
		</div>
	);
}

function BudgetEditor({ item, initial, initialKind, onDone }: { item: AccessItem; initial?: UsageWindow; initialKind: WindowKind; onDone: () => void }) {
	const [kind, setKind] = useState<WindowKind>(initialKind);
	const [tokens, setTokens] = useState(initial?.capacity ? String(initial.capacity) : "");
	const configure = useConfigureWindow();
	const toast = useToast();
	const n = Number(tokens.replace(/[_,\s]/g, ""));
	const valid = Number.isSafeInteger(n) && n > 0;
	const period = PERIODS.find((p) => p.kind === kind)!;

	async function save() {
		try {
			await configure.mutateAsync({ item, kind, capacityTokens: n, startsAt: initial?.startsAt ?? Date.now() });
			toast(`Budget set: ${fmtCompact(n)} tokens ${period.every}`);
			onDone();
		} catch (e) {
			toast((e as Error).message, "bad");
		}
	}

	return (
		<div className="budget-editor">
			<Field label="Resets">
				<div className="segmented" role="radiogroup" aria-label="Budget period">
					{PERIODS.map((p) => (
						<button key={p.kind} role="radio" aria-checked={kind === p.kind} className={cx("segment", kind === p.kind && "is-on")} onClick={() => setKind(p.kind)}>
							{p.label}
						</button>
					))}
				</div>
			</Field>
			<Field label="Tokens per period" hint={valid ? `${fmtInt(n)} tokens ${period.every}. A virtual model stage set to “${period.label.toLowerCase()}” can draw from this.` : "A whole number of tokens. Input and output both count."} htmlFor="budget-tokens">
				<Input id="budget-tokens" inputMode="numeric" value={tokens} onChange={(e) => setTokens(e.target.value)} placeholder="5000000" />
			</Field>
			<div className="row wrap">
				{PRESETS.map((p) => (
					<button key={p} className={cx("preset", n === p && "is-on")} onClick={() => setTokens(String(p))}>
						{fmtCompact(p)}
					</button>
				))}
			</div>
			<div className="row budget-actions">
				<Button variant="ghost" onClick={onDone}>
					Cancel
				</Button>
				<Button variant="primary" disabled={!valid} busy={configure.isPending} onClick={save}>
					Save budget
				</Button>
			</div>
		</div>
	);
}
