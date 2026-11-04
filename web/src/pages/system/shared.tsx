import { motion, useReducedMotion } from "motion/react";
import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { Button, Dialog, Tip, cx } from "../../ui/components";

// ---------- Section header (subordinate to the page title) ----------
export function SubHead({ title, lede, actions }: { title: ReactNode; lede?: ReactNode; actions?: ReactNode }) {
	return (
		<header className="sys-head">
			<div className="grow">
				<h2 className="sys-title">{title}</h2>
				{lede && <p className="sys-lede">{lede}</p>}
			</div>
			{actions && <div className="sys-head-actions">{actions}</div>}
		</header>
	);
}

// ---------- Segmented control: a styled radio group ----------
export function Segmented<T extends string>({ value, onChange, options, label, size }: { value: T; onChange: (v: T) => void; options: Array<{ value: T; label: ReactNode }>; label: string; size?: "sm" }) {
	const id = useId();
	const reduce = useReducedMotion();
	const refs = useRef<Array<HTMLButtonElement | null>>([]);
	const onKey = (e: KeyboardEvent, i: number) => {
		const d = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
		if (!d) return;
		e.preventDefault();
		const n = (i + d + options.length) % options.length;
		onChange(options[n].value);
		refs.current[n]?.focus();
	};
	return (
		<div className={cx("seg", size === "sm" && "seg-sm")} role="radiogroup" aria-label={label}>
			{options.map((o, i) => {
				const on = o.value === value;
				return (
					<button
						key={o.value}
						ref={(el) => {
							refs.current[i] = el;
						}}
						type="button"
						role="radio"
						aria-checked={on}
						tabIndex={on ? 0 : -1}
						className={cx("seg-item", on && "is-on")}
						onClick={() => onChange(o.value)}
						onKeyDown={(e) => onKey(e, i)}
					>
						{on && <motion.span layoutId={`seg-${id}`} className="seg-thumb" transition={reduce ? { duration: 0 } : { type: "spring", stiffness: 520, damping: 40 }} />}
						<span className="seg-label">{o.label}</span>
					</button>
				);
			})}
		</div>
	);
}

// ---------- Confirmation dialog: says what happens before it happens ----------
export function ConfirmDialog({
	open,
	onOpenChange,
	title,
	description,
	children,
	confirmLabel,
	cancelLabel = "Cancel",
	danger,
	busy,
	onConfirm,
}: {
	open: boolean;
	onOpenChange: (v: boolean) => void;
	title: ReactNode;
	description: ReactNode;
	children?: ReactNode;
	confirmLabel: string;
	cancelLabel?: string;
	danger?: boolean;
	busy?: boolean;
	onConfirm: () => void;
}) {
	return (
		<Dialog
			open={open}
			onOpenChange={onOpenChange}
			title={title}
			description={description}
			footer={
				<>
					<Button variant="ghost" onClick={() => onOpenChange(false)}>
						{cancelLabel}
					</Button>
					<Button variant={danger ? "danger" : "primary"} busy={busy} onClick={onConfirm}>
						{confirmLabel}
					</Button>
				</>
			}
		>
			{children ?? <span className="sys-empty-body" />}
		</Dialog>
	);
}

// ---------- Unsaved-changes guard ----------
// Catches in-app links (sidebar, sub-navigation, cards) and tab close while a
// form has unsaved edits, and asks before discarding them.
export function useUnsavedGuard(dirty: boolean, what: string) {
	const [pending, setPending] = useState<string | null>(null);
	const dirtyRef = useRef(dirty);
	dirtyRef.current = dirty;
	useEffect(() => {
		const onClick = (e: MouseEvent) => {
			if (!dirtyRef.current || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey) return;
			const a = (e.target as Element | null)?.closest?.("a[href^='#/']") as HTMLAnchorElement | null;
			if (!a) return;
			const href = a.getAttribute("href")!;
			if (href === location.hash) return;
			e.preventDefault();
			setPending(href);
		};
		const onUnload = (e: BeforeUnloadEvent) => {
			if (!dirtyRef.current) return;
			e.preventDefault();
		};
		document.addEventListener("click", onClick, true);
		window.addEventListener("beforeunload", onUnload);
		return () => {
			document.removeEventListener("click", onClick, true);
			window.removeEventListener("beforeunload", onUnload);
		};
	}, []);
	const dialog = (
		<ConfirmDialog
			open={pending !== null}
			onOpenChange={(v) => !v && setPending(null)}
			title="Leave without saving?"
			description={`You have unsaved changes to ${what}. If you leave now they are thrown away; nothing on the server changes.`}
			confirmLabel="Discard and leave"
			cancelLabel="Keep editing"
			danger
			onConfirm={() => {
				const to = pending;
				setPending(null);
				dirtyRef.current = false;
				if (to) location.hash = to;
			}}
		/>
	);
	return dialog;
}

// ---------- Ctrl/Cmd+S ----------
export function useSaveShortcut(onSave: () => void, enabled: boolean) {
	const ref = useRef(onSave);
	ref.current = onSave;
	useEffect(() => {
		if (!enabled) return;
		const on = (e: globalThis.KeyboardEvent) => {
			if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
				e.preventDefault();
				ref.current();
			}
		};
		document.addEventListener("keydown", on);
		return () => document.removeEventListener("keydown", on);
	}, [enabled]);
}

// ---------- Facts (definition list) ----------
export function Facts({ items }: { items: Array<[ReactNode, ReactNode]> }) {
	return (
		<dl className="sys-facts">
			{items.map(([k, v], i) => (
				<div key={i}>
					<dt>{k}</dt>
					<dd>{v}</dd>
				</div>
			))}
		</dl>
	);
}

// ---------- Server key glyph ----------
// Server keys are not inference keys, so they never borrow the key glyph.
// This is a server unit with an open access slot.
export function ServerKeyGlyph({ size = 18 }: { size?: number }) {
	return (
		<svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
			<rect x="3" y="4" width="18" height="7" rx="2" />
			<rect x="3" y="13" width="18" height="7" rx="2" />
			<path d="M7 7.5h5M7 16.5h5" />
			<circle cx="17" cy="7.5" r="1" fill="currentColor" stroke="none" />
			<circle cx="17" cy="16.5" r="1" fill="currentColor" stroke="none" />
		</svg>
	);
}

// Download is its own concept (import already means "copy from NeoPi").
export function DownloadGlyph({ size = 17 }: { size?: number }) {
	return (
		<svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
			<path d="M12 4v11M7.5 10.5 12 15l4.5-4.5" />
			<path d="M5 19.5h14" />
		</svg>
	);
}

/** Icon-only button for a locally drawn glyph; same look as IconButton. */
export function GlyphButton({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
	return (
		<Tip label={label}>
			<button type="button" className="icon-btn" aria-label={label} onClick={onClick}>
				{children}
			</button>
		</Tip>
	);
}

// ---------- Formatting ----------
export function fmtBytes(n: number | undefined) {
	if (n == null) return "";
	if (n < 1024) return `${n} bytes`;
	if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB`;
	return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** "0.1.0-nightly.20261104.local.c0ff2a08" → parts for display. */
export function parseVersion(v: string | undefined) {
	if (!v) return null;
	const m = v.match(/^v?(\d+\.\d+\.\d+)(?:-(\w+)\.(\d{4})(\d{2})(\d{2}))?(?:\.(\w+))?(?:\.([0-9a-f]{6,}))?/);
	if (!m) return { raw: v, release: v };
	const [, release, channel, y, mo, d, flavor, commit] = m;
	const date = y ? new Date(Number(y), Number(mo) - 1, Number(d)).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) : undefined;
	return { raw: v, release, channel, date, flavor, commit };
}

/** Where apps should point. The dev server proxies, so use the engine's own address there. */
export function clientBase(engineEndpoint?: string) {
	if (import.meta.env.DEV && engineEndpoint) return engineEndpoint.replace(/\/+$/, "");
	return location.origin;
}
