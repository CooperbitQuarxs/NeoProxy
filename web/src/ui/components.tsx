import * as RDialog from "@radix-ui/react-dialog";
import * as RSelect from "@radix-ui/react-select";
import * as RSwitch from "@radix-ui/react-switch";
import * as RTabs from "@radix-ui/react-tabs";
import * as RTooltip from "@radix-ui/react-tooltip";
import { AnimatePresence, motion } from "motion/react";
import { forwardRef, useState, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode } from "react";
import { Icon, type IconName } from "./icons";
import { providerColor } from "../lib/providers";
import { providerLogo } from "../lib/logos";

const cx = (...parts: unknown[]) => parts.filter((p) => typeof p === "string" && p).join(" ");
export { cx };

// ---------- Buttons ----------
type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
	variant?: "primary" | "secondary" | "ghost" | "danger";
	size?: "sm" | "md" | "lg";
	icon?: IconName;
	busy?: boolean;
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
	{ variant = "secondary", size = "md", icon, busy, className, children, disabled, ...rest },
	ref,
) {
	return (
		<button
			ref={ref}
			className={cx("btn", `btn-${variant}`, size !== "md" && `btn-${size}`, busy && "is-busy", className)}
			disabled={disabled || busy}
			{...rest}
		>
			{busy ? <span className="spinner" aria-hidden="true" /> : icon ? <Icon name={icon} size={size === "sm" ? 15 : 17} /> : null}
			{children && <span>{children}</span>}
		</button>
	);
});

export function IconButton({ icon, label, className, ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { icon: IconName; label: string }) {
	return (
		<Tip label={label}>
			<button className={cx("icon-btn", className)} aria-label={label} {...rest}>
				<Icon name={icon} size={17} />
			</button>
		</Tip>
	);
}

// ---------- Tooltip ----------
export function Tip({ label, children, side = "top" }: { label: ReactNode; children: ReactNode; side?: "top" | "bottom" | "left" | "right" }) {
	return (
		<RTooltip.Root delayDuration={250}>
			<RTooltip.Trigger asChild>{children}</RTooltip.Trigger>
			<RTooltip.Portal>
				<RTooltip.Content side={side} sideOffset={6} className="tooltip">
					{label}
				</RTooltip.Content>
			</RTooltip.Portal>
		</RTooltip.Root>
	);
}

// ---------- Fields ----------
export function Field({ label, hint, error, children, htmlFor, optional }: { label: ReactNode; hint?: ReactNode; error?: ReactNode; children: ReactNode; htmlFor?: string; optional?: boolean }) {
	return (
		<div className={cx("field", error && "has-error")}>
			<label className="field-label" htmlFor={htmlFor}>
				{label}
				{optional && <span className="field-optional">optional</span>}
			</label>
			{children}
			{error ? <p className="field-error">{error}</p> : hint ? <p className="field-hint">{hint}</p> : null}
		</div>
	);
}

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & { mono?: boolean }>(function Input({ className, mono, ...rest }, ref) {
	return <input ref={ref} className={cx("input", mono && "mono", className)} {...rest} />;
});

export function SearchInput({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder: string }) {
	return (
		<div className="search">
			<Icon name="search" size={16} />
			<input value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} aria-label={placeholder} />
		</div>
	);
}

// ---------- Select (styled, accessible; never the native popup) ----------
export type SelectOption = { value: string; label: ReactNode; description?: ReactNode; leading?: ReactNode; disabled?: boolean };
export type SelectGroup = { label?: string; options: SelectOption[] };

export function Select({
	value,
	onValueChange,
	options,
	groups,
	placeholder = "Choose…",
	id,
	ariaLabel,
	className,
}: {
	value: string | undefined;
	onValueChange: (v: string) => void;
	options?: SelectOption[];
	groups?: SelectGroup[];
	placeholder?: string;
	id?: string;
	ariaLabel?: string;
	className?: string;
}) {
	const allGroups = groups ?? [{ options: options ?? [] }];
	return (
		<RSelect.Root value={value} onValueChange={onValueChange}>
			<RSelect.Trigger id={id} className={cx("select-trigger", className)} aria-label={ariaLabel}>
				<RSelect.Value placeholder={<span className="faint">{placeholder}</span>} />
				<RSelect.Icon className="select-chevron">
					<Icon name="chevron" size={16} />
				</RSelect.Icon>
			</RSelect.Trigger>
			<RSelect.Portal>
				<RSelect.Content className="select-content" position="popper" sideOffset={6}>
					<RSelect.Viewport className="select-viewport">
						{allGroups.map((g, gi) => (
							<RSelect.Group key={gi}>
								{g.label && <RSelect.Label className="select-group-label">{g.label}</RSelect.Label>}
								{g.options.map((o) => (
									<RSelect.Item key={o.value} value={o.value} disabled={o.disabled} className="select-item">
										{o.leading && <span className="select-leading">{o.leading}</span>}
										<span className="select-text">
											<RSelect.ItemText>{o.label}</RSelect.ItemText>
											{o.description && <span className="select-desc">{o.description}</span>}
										</span>
										<RSelect.ItemIndicator className="select-check">
											<Icon name="check" size={15} />
										</RSelect.ItemIndicator>
									</RSelect.Item>
								))}
							</RSelect.Group>
						))}
					</RSelect.Viewport>
				</RSelect.Content>
			</RSelect.Portal>
		</RSelect.Root>
	);
}

// ---------- Switch ----------
export function Switch({ checked, onCheckedChange, label, id, disabled }: { checked: boolean; onCheckedChange: (v: boolean) => void; label?: ReactNode; id?: string; disabled?: boolean }) {
	return (
		<span className="switch-row">
			<RSwitch.Root id={id} className="switch" checked={checked} onCheckedChange={onCheckedChange} disabled={disabled}>
				<RSwitch.Thumb className="switch-thumb" />
			</RSwitch.Root>
			{label && <label htmlFor={id}>{label}</label>}
		</span>
	);
}

// ---------- Tabs ----------
export function Tabs({ value, onValueChange, items, children }: { value: string; onValueChange: (v: string) => void; items: Array<{ value: string; label: ReactNode; count?: number }>; children?: ReactNode }) {
	return (
		<RTabs.Root value={value} onValueChange={onValueChange} className="tabs">
			<RTabs.List className="tabs-list">
				{items.map((t) => (
					<RTabs.Trigger key={t.value} value={t.value} className="tab">
						{t.label}
						{t.count !== undefined && <span className="tab-count num">{t.count}</span>}
					</RTabs.Trigger>
				))}
			</RTabs.List>
			{children}
		</RTabs.Root>
	);
}
export const TabPanel = RTabs.Content;

// ---------- Dialog and side sheet ----------
export function Dialog({
	open,
	onOpenChange,
	title,
	description,
	children,
	footer,
	wide,
	sheet,
}: {
	open: boolean;
	onOpenChange: (v: boolean) => void;
	title: ReactNode;
	description?: ReactNode;
	children: ReactNode;
	footer?: ReactNode;
	wide?: boolean;
	sheet?: boolean;
}) {
	return (
		<RDialog.Root open={open} onOpenChange={onOpenChange}>
			<AnimatePresence>
				{open && (
					<RDialog.Portal forceMount>
						<RDialog.Overlay asChild forceMount>
							<motion.div className="overlay" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.18 }} />
						</RDialog.Overlay>
						<RDialog.Content asChild forceMount aria-describedby={description ? undefined : undefined}>
							<motion.div
								className={cx(sheet ? "sheet" : "dialog", wide && "is-wide")}
								initial={sheet ? { x: 40, opacity: 0 } : { y: 14, opacity: 0, scale: 0.985 }}
								animate={sheet ? { x: 0, opacity: 1 } : { y: 0, opacity: 1, scale: 1 }}
								exit={sheet ? { x: 40, opacity: 0 } : { y: 8, opacity: 0, scale: 0.99 }}
								transition={{ type: "spring", stiffness: 420, damping: 36, mass: 0.8 }}
							>
								<header className="dialog-head">
									<div className="grow">
										<RDialog.Title className="dialog-title">{title}</RDialog.Title>
										{description ? <RDialog.Description className="dialog-desc">{description}</RDialog.Description> : <RDialog.Description className="sr-only">{typeof title === "string" ? title : "Dialog"}</RDialog.Description>}
									</div>
									<RDialog.Close asChild>
										<button className="icon-btn" aria-label="Close">
											<Icon name="close" size={17} />
										</button>
									</RDialog.Close>
								</header>
								<div className="dialog-body">{children}</div>
								{footer && <footer className="dialog-foot">{footer}</footer>}
							</motion.div>
						</RDialog.Content>
					</RDialog.Portal>
				)}
			</AnimatePresence>
		</RDialog.Root>
	);
}

// ---------- Small display pieces ----------
export function Chip({ tone = "neutral", children, icon }: { tone?: "neutral" | "good" | "warn" | "bad" | "accent"; children: ReactNode; icon?: IconName }) {
	return (
		<span className={cx("chip", `chip-${tone}`)}>
			{icon && <Icon name={icon} size={13} />}
			{children}
		</span>
	);
}

export function StatusDot({ tone }: { tone: "good" | "warn" | "bad" | "idle" }) {
	return <span className={cx("dot", `dot-${tone}`)} aria-hidden="true" />;
}

function hashHue(s: string) {
	let h = 0;
	for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) % 360;
	return h;
}

export function Avatar({ name, size = 28 }: { name: string; size?: number }) {
	const initials = name
		.split(/[\s._@-]+/)
		.filter(Boolean)
		.slice(0, 2)
		.map((p) => p[0]?.toUpperCase())
		.join("");
	return (
		<span className="avatar" style={{ width: size, height: size, fontSize: size * 0.4, ["--h" as string]: hashHue(name) }} aria-hidden="true">
			{initials || "?"}
		</span>
	);
}

// A provider's color square with its logo drawn in ink; a monogram when no logo exists.
export function ProviderMark({ provider, name, hue, size = 22 }: { provider: string; name?: string; hue?: number; size?: number }) {
	const logo = providerLogo(provider);
	const label = (name ?? provider).replace(/[^A-Za-z0-9]/g, "");
	return (
		<span className="pmark" style={{ width: size, height: size, fontSize: size * 0.46, background: providerColor(provider, hue) }} aria-hidden="true">
			{logo ? <ProviderLogo provider={provider} size={Math.round(size * 0.62)} /> : label.slice(0, 1).toUpperCase()}
		</span>
	);
}

// The logo glyph alone, in the current text color. Renders nothing without a logo.
export function ProviderLogo({ provider, size = 16, className }: { provider: string; size?: number; className?: string }) {
	const logo = providerLogo(provider);
	if (!logo) return null;
	return <span className={cx("plogo", className)} style={{ width: size, height: size, WebkitMaskImage: `url("${logo}")`, maskImage: `url("${logo}")` }} aria-hidden="true" />;
}

export function EmptyState({ title, children, action, art }: { title: ReactNode; children?: ReactNode; action?: ReactNode; art?: ReactNode }) {
	return (
		<div className="empty">
			{art}
			<h3 className="empty-title">{title}</h3>
			{children && <div className="empty-text">{children}</div>}
			{action && <div className="empty-action">{action}</div>}
		</div>
	);
}

export function Notice({ tone = "info", title, children, action }: { tone?: "info" | "warn" | "bad" | "good"; title?: ReactNode; children?: ReactNode; action?: ReactNode }) {
	const icon: IconName = tone === "info" ? "info" : tone === "good" ? "check" : "alert";
	return (
		<div className={cx("notice", `notice-${tone}`)} role={tone === "bad" ? "alert" : undefined}>
			<Icon name={icon} size={17} />
			<div className="grow">
				{title && <p className="notice-title">{title}</p>}
				{children && <div className="notice-text">{children}</div>}
			</div>
			{action}
		</div>
	);
}

export function Skeleton({ h = 14, w = "100%", r = 6 }: { h?: number; w?: number | string; r?: number }) {
	return <span className="skeleton" style={{ height: h, width: w, borderRadius: r }} />;
}

export function Kbd({ children }: { children: ReactNode }) {
	return <kbd className="kbd">{children}</kbd>;
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
	const [done, setDone] = useState(false);
	return (
		<IconButton
			icon={done ? "check" : "copy"}
			label={done ? "Copied" : label}
			onClick={async () => {
				await navigator.clipboard.writeText(text);
				setDone(true);
				setTimeout(() => setDone(false), 1400);
			}}
		/>
	);
}

export function CodeLine({ children, copy }: { children: string; copy?: boolean }) {
	return (
		<div className="codeline">
			<code>{children}</code>
			{copy !== false && <CopyButton text={children} />}
		</div>
	);
}

export function Section({ title, description, actions, children, className }: { title?: ReactNode; description?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
	return (
		<section className={cx("section", className)}>
			{(title || actions) && (
				<header className="section-head">
					<div className="grow">
						{title && <h2 className="section-title">{title}</h2>}
						{description && <p className="section-desc">{description}</p>}
					</div>
					{actions && <div className="row">{actions}</div>}
				</header>
			)}
			{children}
		</section>
	);
}

export function PageHeader({ title, lede, actions, children }: { title: ReactNode; lede?: ReactNode; actions?: ReactNode; children?: ReactNode }) {
	return (
		<header className="page-head">
			<div className="page-head-main">
				<h1 className="page-title">{title}</h1>
				{lede && <p className="page-lede">{lede}</p>}
				{children}
			</div>
			{actions && <div className="page-actions">{actions}</div>}
		</header>
	);
}
