import * as RPopover from "@radix-ui/react-popover";
import { useMemo, useRef, useState, type ReactNode } from "react";
import { ProviderMark, cx } from "../../ui/components";
import { Icon } from "../../ui/icons";
import type { NeoAccount, NeoModel, NeoProvider } from "./data";
import { colorOf, compactCtx, labelProvider } from "./route";

type Props = {
	trigger: ReactNode;
	value?: string;
	onPick: (model: NeoModel) => void;
	models: NeoModel[];
	providers: NeoProvider[];
	accounts: NeoAccount[];
	/** Shared NeoPi models need a metered stage; a fallback without a budget can't use them. */
	excludeShared?: boolean;
	title: string;
};

/** Searchable, provider-grouped model chooser. Only models people can be served appear. */
export function ModelPicker({ trigger, value, onPick, models, providers, accounts, excludeShared, title }: Props) {
	const [open, setOpen] = useState(false);
	const [query, setQuery] = useState("");
	const [active, setActive] = useState(0);
	const listRef = useRef<HTMLDivElement>(null);

	const groups = useMemo(() => {
		const q = query.trim().toLowerCase();
		const out: Array<{ provider: NeoProvider; label: ReturnType<typeof labelProvider>; healthy: number; models: NeoModel[] }> = [];
		for (const p of providers) {
			if (!p.exposed) continue;
			const label = labelProvider(p.id, providers);
			const list = models.filter(
				(m) =>
					m.providerId === p.id &&
					m.exposed &&
					!(excludeShared && m.virtualOnly) &&
					(!q || `${m.display} ${m.name} ${m.id} ${label.name} ${label.source ?? ""}`.toLowerCase().includes(q)),
			);
			if (list.length) out.push({ provider: p, label, healthy: accounts.filter((a) => a.providerId === p.id && a.state === "healthy").length, models: list });
		}
		return out;
	}, [providers, models, accounts, query, excludeShared]);
	const flat = groups.flatMap((g) => g.models);
	const hiddenCount = models.filter((m) => !m.exposed || !providers.find((p) => p.id === m.providerId)?.exposed).length;

	const pick = (m: NeoModel) => {
		onPick(m);
		setOpen(false);
		setQuery("");
	};
	const move = (delta: number) => {
		if (!flat.length) return;
		const next = (active + delta + flat.length) % flat.length;
		setActive(next);
		listRef.current?.querySelector(`[data-idx="${next}"]`)?.scrollIntoView({ block: "nearest" });
	};

	return (
		<RPopover.Root
			open={open}
			onOpenChange={(v) => {
				setOpen(v);
				if (v) {
					const idx = flat.findIndex((m) => m.id === value);
					setActive(idx >= 0 ? idx : 0);
				}
			}}
		>
			<RPopover.Trigger asChild>{trigger}</RPopover.Trigger>
			<RPopover.Portal>
				<RPopover.Content className="mpicker" align="start" sideOffset={6} collisionPadding={16} aria-label={title}>
					<div className="mpicker-search">
						<Icon name="search" size={16} />
						<input
							autoFocus
							value={query}
							placeholder="Search models and providers"
							aria-label="Search models and providers"
							role="combobox"
							aria-expanded="true"
							aria-controls="mpicker-list"
							aria-activedescendant={flat[active] ? `mpick-${active}` : undefined}
							onChange={(e) => {
								setQuery(e.target.value);
								setActive(0);
							}}
							onKeyDown={(e) => {
								if (e.key === "ArrowDown") {
									e.preventDefault();
									move(1);
								} else if (e.key === "ArrowUp") {
									e.preventDefault();
									move(-1);
								} else if (e.key === "Enter" && flat[active]) {
									e.preventDefault();
									pick(flat[active]);
								}
							}}
						/>
					</div>
					<div className="mpicker-list" id="mpicker-list" role="listbox" aria-label={title} ref={listRef}>
						{groups.length === 0 && (
							<p className="mpicker-empty">
								{query ? `No served model matches “${query}”.` : "No models are being served yet. Add provider access on the Pool page, or turn models on under Provider models."}
							</p>
						)}
						{groups.map((g) => (
							<div key={g.provider.id} role="group" aria-label={g.label.name} className="mpicker-group">
								<div className="mpicker-group-head" style={{ ["--p" as string]: colorOf(g.label) }}>
									<ProviderMark provider={g.label.colorKey} name={g.label.name} size={18} />
									<span className="mpicker-group-name">{g.label.name}</span>
									{g.label.shared && <span className="faint">shared by {g.label.source}</span>}
									<span className="grow" />
									<span className={cx("mpicker-health", !g.healthy && "is-off")}>{g.healthy ? (g.healthy === 1 ? "1 healthy account" : `${g.healthy} healthy accounts`) : "No healthy account right now"}</span>
								</div>
								{g.models.map((m) => {
									const idx = flat.indexOf(m);
									return (
										<div
											key={m.id}
											id={`mpick-${idx}`}
											data-idx={idx}
											role="option"
											aria-selected={m.id === value}
											className={cx("mpicker-item", idx === active && "is-active", m.id === value && "is-current")}
											onMouseEnter={() => setActive(idx)}
											onMouseDown={(e) => e.preventDefault()}
											onClick={() => pick(m)}
										>
											<span className="mpicker-item-name">{m.display}</span>
											<span className="mpicker-item-id mono">{m.name}</span>
											<span className="grow" />
											{m.context > 0 && <span className="faint num">{compactCtx(m.context)} context</span>}
											{m.id === value && <Icon name="check" size={15} />}
										</div>
									);
								})}
							</div>
						))}
					</div>
					{hiddenCount > 0 && <p className="mpicker-foot">{hiddenCount === 1 ? "1 model is" : `${hiddenCount} models are`} turned off under Provider models and not listed.</p>}
				</RPopover.Content>
			</RPopover.Portal>
		</RPopover.Root>
	);
}
