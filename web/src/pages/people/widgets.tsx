import { useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { Button, Dialog, Input, Tip, cx } from "../../ui/components";
import type { Role } from "./api";
import { ROLE_COPY } from "./copy";
import "./widgets.css";

// Small controls these pages need that the shared kit lacks. Candidates for src/ui.

/** Password field with show/hide and an optional generator. */
export function PasswordInput({ id, value, onChange, generate, autoComplete = "new-password", placeholder }: { id?: string; value: string; onChange: (v: string) => void; generate?: () => string; autoComplete?: string; placeholder?: string }) {
	const [shown, setShown] = useState(false);
	return (
		<div className="pw">
			<Input id={id} mono={shown} type={shown ? "text" : "password"} autoComplete={autoComplete} value={value} placeholder={placeholder} spellCheck={false} onChange={(e) => onChange(e.target.value)} />
			<button type="button" className="pw-toggle" onClick={() => setShown((s) => !s)} aria-pressed={shown}>
				{shown ? "Hide" : "Show"}
			</button>
			{generate && (
				<Button
					type="button"
					size="sm"
					variant="ghost"
					onClick={() => {
						onChange(generate());
						setShown(true);
					}}
				>
					Generate
				</Button>
			)}
		</div>
	);
}

/** Roles as radio cards so each choice carries its explanation. */
export function RolePicker({ value, onChange, roles, label }: { value: Role; onChange: (r: Role) => void; roles: Role[]; label: string }) {
	const name = useId();
	return (
		<div className="role-cards" role="radiogroup" aria-label={label}>
			{roles.map((r) => (
				<label key={r} className={cx("role-card", value === r && "is-on")}>
					<input type="radio" name={name} value={r} checked={value === r} onChange={() => onChange(r)} />
					<span className="role-card-name">
						<span className={`role-dot role-${r}`} aria-hidden="true" />
						{ROLE_COPY[r].name}
					</span>
					<span className="role-card-text">{ROLE_COPY[r].can}</span>
				</label>
			))}
		</div>
	);
}

export function RoleBadge({ role }: { role: string }) {
	const copy = ROLE_COPY[role as Role];
	return (
		<Tip label={copy ? copy.can : role}>
			<span className="role-badge" tabIndex={0}>
				<span className={`role-dot role-${role}`} aria-hidden="true" />
				{copy?.name ?? role}
			</span>
		</Tip>
	);
}

/** Segmented control: a styled radio group with arrow-key movement. */
export function Segmented<T extends string>({ value, onChange, options, label }: { value: T; onChange: (v: T) => void; options: Array<{ value: T; label: ReactNode; count?: number }>; label: string }) {
	const refs = useRef<Array<HTMLButtonElement | null>>([]);
	const onKey = (e: KeyboardEvent, i: number) => {
		const dir = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
		if (!dir) return;
		e.preventDefault();
		const next = (i + dir + options.length) % options.length;
		onChange(options[next].value);
		refs.current[next]?.focus();
	};
	return (
		<div className="seg" role="radiogroup" aria-label={label}>
			{options.map((o, i) => (
				<button
					key={o.value}
					ref={(el) => {
						refs.current[i] = el;
					}}
					type="button"
					role="radio"
					aria-checked={value === o.value}
					tabIndex={value === o.value ? 0 : -1}
					className={cx("seg-item", value === o.value && "is-on")}
					onClick={() => onChange(o.value)}
					onKeyDown={(e) => onKey(e, i)}
				>
					{o.label}
					{o.count !== undefined && <span className="seg-count num">{o.count}</span>}
				</button>
			))}
		</div>
	);
}

/** A thin horizontal meter, 0..1. */
export function Meter({ value, tone = "accent", label }: { value: number; tone?: "accent" | "warn" | "bad"; label?: string }) {
	const v = Math.max(0, Math.min(1, value));
	return (
		<span className={cx("meter", `meter-${tone}`)} role={label ? "img" : undefined} aria-label={label}>
			<span className="meter-fill" style={{ width: `${(v * 100).toFixed(1)}%` }} />
		</span>
	);
}

/** Yes/no confirmation that names the consequences before anything happens. */
export function Confirm({
	open,
	onOpenChange,
	title,
	children,
	confirmLabel,
	onConfirm,
	busy,
	danger = true,
}: {
	open: boolean;
	onOpenChange: (v: boolean) => void;
	title: ReactNode;
	children: ReactNode;
	confirmLabel: string;
	onConfirm: () => void;
	busy?: boolean;
	danger?: boolean;
}) {
	return (
		<Dialog
			open={open}
			onOpenChange={onOpenChange}
			title={title}
			footer={
				<>
					<Button variant="ghost" onClick={() => onOpenChange(false)}>
						Cancel
					</Button>
					<Button variant={danger ? "danger" : "primary"} busy={busy} onClick={onConfirm}>
						{confirmLabel}
					</Button>
				</>
			}
		>
			<div className="confirm-text">{children}</div>
		</Dialog>
	);
}

export function fmtDuration(ms: number | undefined) {
	if (!ms && ms !== 0) return "";
	if (ms < 1000) return `${Math.round(ms)} ms`;
	if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)} s`;
	const m = Math.floor(ms / 60_000);
	return `${m} min ${Math.round((ms % 60_000) / 1000)} s`;
}
