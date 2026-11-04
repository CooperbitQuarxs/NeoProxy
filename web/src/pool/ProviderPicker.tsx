import * as RPopover from "@radix-ui/react-popover";
import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import type { ProviderInfo } from "../api/types";
import { ProviderMark, cx } from "../ui/components";
import { Icon } from "../ui/icons";

// Lists this long get a filter box; shorter ones are quicker to scan.
const SEARCH_FROM = 9;

function matches(p: ProviderInfo, q: string): boolean {
	return [p.name, p.vendor, p.id].some((s) => s.toLowerCase().includes(q));
}

/** Provider choice with each provider's mark, in the closed field as well as the
 *  list, and type-to-filter once the catalogue gets long. A combobox: focus stays
 *  in the filter box while arrow keys move through the list. */
export function ProviderPicker({ id, providers, value, onChange }: { id?: string; providers: ProviderInfo[]; value: string; onChange: (id: string) => void }) {
	const [open, setOpen] = useState(false);
	const [query, setQuery] = useState("");
	const [active, setActive] = useState(0);
	const listId = useId();
	const listRef = useRef<HTMLDivElement>(null);
	const searchable = providers.length >= SEARCH_FROM;
	const selected = providers.find((p) => p.id === value);

	const shown = useMemo(() => {
		const q = query.trim().toLowerCase();
		return q ? providers.filter((p) => matches(p, q)) : providers;
	}, [providers, query]);

	useEffect(() => {
		if (!open) return;
		setQuery("");
		setActive(Math.max(0, providers.findIndex((p) => p.id === value)));
	}, [open, providers, value]);

	useEffect(() => {
		listRef.current?.querySelector<HTMLElement>(`[data-index="${active}"]`)?.scrollIntoView({ block: "nearest" });
	}, [active, shown]);

	const choose = (p: ProviderInfo | undefined) => {
		if (!p) return;
		onChange(p.id);
		setOpen(false);
	};

	const onKeyDown = (e: KeyboardEvent) => {
		const last = shown.length - 1;
		if (e.key === "ArrowDown") setActive((i) => (i >= last ? 0 : i + 1));
		else if (e.key === "ArrowUp") setActive((i) => (i <= 0 ? last : i - 1));
		else if (e.key === "Home") setActive(0);
		else if (e.key === "End") setActive(last);
		else if (e.key === "Enter") choose(shown[active]);
		else return;
		e.preventDefault();
	};

	const optionId = (i: number) => `${listId}-${i}`;

	return (
		<RPopover.Root open={open} onOpenChange={setOpen}>
			<RPopover.Trigger id={id} className="select-trigger pp-trigger" aria-haspopup="listbox">
				<span>
					{selected ? (
						<>
							<ProviderMark provider={selected.id} name={selected.name} hue={selected.hue} size={22} />
							<span className="pp-name">{selected.name}</span>
							{selected.vendor !== selected.name && <span className="pp-vendor">{selected.vendor}</span>}
						</>
					) : (
						<span className="faint">Choose a provider…</span>
					)}
				</span>
				<span className="select-chevron">
					<Icon name="chevron" size={16} />
				</span>
			</RPopover.Trigger>
			<RPopover.Portal>
				<RPopover.Content className="select-content pp-content" align="start" sideOffset={6} onKeyDown={searchable ? undefined : onKeyDown} onOpenAutoFocus={(e) => {
					if (!searchable) {
						e.preventDefault();
						listRef.current?.focus();
					}
				}}>
					{searchable && (
						<div className="search pp-search">
							<Icon name="search" size={16} />
							<input
								autoFocus
								role="combobox"
								aria-label="Find a provider"
								aria-expanded
								aria-controls={listId}
								aria-activedescendant={shown.length ? optionId(active) : undefined}
								placeholder={`Find among ${providers.length} providers`}
								value={query}
								onChange={(e) => {
									setQuery(e.target.value);
									setActive(0);
								}}
								onKeyDown={onKeyDown}
							/>
						</div>
					)}
					<div ref={listRef} id={listId} role="listbox" aria-label="Providers" tabIndex={searchable ? -1 : 0} aria-activedescendant={!searchable && shown.length ? optionId(active) : undefined} className="select-viewport pp-list">
						{shown.length === 0 && <p className="pp-empty">No provider matches “{query.trim()}”.</p>}
						{shown.map((p, i) => (
							<div
								key={p.id}
								id={optionId(i)}
								data-index={i}
								role="option"
								aria-selected={p.id === value}
								className="select-item"
								data-highlighted={i === active ? "" : undefined}
								onMouseMove={() => setActive(i)}
								onClick={() => choose(p)}
							>
								<span className="select-leading">
									<ProviderMark provider={p.id} name={p.name} hue={p.hue} size={22} />
								</span>
								<span className="select-text">
									<span>{p.name}</span>
									{p.vendor !== p.name && <span className="select-desc">{p.vendor}</span>}
								</span>
								<span className={cx("select-check", p.id !== value && "pp-hidden")}>
									<Icon name="check" size={15} />
								</span>
							</div>
						))}
					</div>
				</RPopover.Content>
			</RPopover.Portal>
		</RPopover.Root>
	);
}
