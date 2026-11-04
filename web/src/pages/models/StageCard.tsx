import { Reorder, useDragControls } from "motion/react";
import { useId } from "react";
import { Button, Field, Input, Notice, ProviderMark, Select, Tip, cx } from "../../ui/components";
import { Icon } from "../../ui/icons";
import type { BudgetWindow, Draw, Effort, LuaPolicy, NeoAccount, NeoModel, NeoProvider, Stage, Usage } from "./data";
import { Glyph } from "./glyphs";
import { ModelPicker } from "./ModelPicker";
import { DRAW_COPY, LUA_NAME_RE, WINDOW_COPY, colorOf, labelProvider, modelName, pct, providerOfModel, stagePool, stageSentence, windowGap } from "./route";

type Catalog = { models: NeoModel[]; providers: NeoProvider[]; accounts: NeoAccount[] };

const LUA_ROWS: Array<{ key: keyof LuaPolicy; label: string; builtin: (s: Stage) => string; suggested: string; hint: string }> = [
	{ key: "eligible", label: "Which accounts may answer", builtin: () => "Every healthy account in the stage", suggested: "reported_room", hint: "Returns true or false for each account. False leaves that account out." },
	{ key: "perAccount", label: "Share per account", builtin: (s) => `${pct(s.perAccount)} of each account's window`, suggested: "available_share", hint: "Returns a fraction from 0 to 1, like 0.05 for 5%. It can only lower the share set above." },
	{ key: "cap", label: "Budget for the whole stage", builtin: (s) => `${pct(s.cap)} in total`, suggested: "pool_share", hint: "Returns a fraction from 0 to 1. Called once per stage, without an account." },
];

export function StageCard({
	stage,
	index,
	count,
	expanded,
	onToggle,
	onChange,
	onMove,
	onRemove,
	catalog,
	usage,
	vmId,
	onEditScript,
}: {
	stage: Stage;
	index: number;
	count: number;
	expanded: boolean;
	onToggle: () => void;
	onChange: (next: Stage) => void;
	onMove: (delta: number) => void;
	onRemove: () => void;
	catalog: Catalog;
	usage: Usage | undefined;
	vmId: string | null;
	onEditScript: () => void;
}) {
	const controls = useDragControls();
	const uid = useId();
	const label = labelProvider(providerOfModel(stage.model, catalog.models), catalog.providers);
	const pool = stagePool(stage, catalog.accounts, catalog.models);
	const set = (patch: Partial<Stage>) => onChange({ ...stage, ...patch });
	const drawn = (accountId: string) => {
		if (!vmId || !usage) return 0;
		let n = 0;
		for (const byVm of Object.values(usage)) n += byVm?.[vmId]?.[stage.id]?.[accountId] ?? 0;
		return n;
	};
	const missingModel = !catalog.models.some((m) => m.id === stage.model);
	const gap = windowGap(stage, pool.chosen);
	const unmeasured = !missingModel && pool.chosen.length > 0 && gap.measured === 0;

	return (
		<Reorder.Item
			value={stage}
			dragListener={false}
			dragControls={controls}
			className={cx("mstage", expanded && "is-open")}
			style={{ ["--p" as string]: colorOf(label) }}
			aria-label={`Stage ${index + 1} of ${count}, ${modelName(stage.model, catalog.models)}`}
			onKeyDown={(e) => {
				if (e.altKey && (e.key === "ArrowUp" || e.key === "ArrowDown") && !(e.target as HTMLElement).closest("input, textarea, [role=combobox]")) {
					e.preventDefault();
					onMove(e.key === "ArrowUp" ? -1 : 1);
				}
			}}
		>
			<div className="mstage-head">
				<Tip label="Drag to reorder">
					<button
						className="mstage-grip"
						aria-label="Drag to reorder"
						onPointerDown={(e) => {
							e.preventDefault();
							controls.start(e);
						}}
						tabIndex={-1}
					>
						<Glyph name="grip" size={16} />
					</button>
				</Tip>
				<span className="mstage-num num" aria-hidden="true">
					{index + 1}
				</span>
				<button className="mstage-summary" onClick={onToggle} aria-expanded={expanded} aria-controls={`${uid}-body`}>
					<ProviderMark provider={label.colorKey} name={label.name} size={28} />
					<span className="grow">
						<span className="mstage-model">
							{modelName(stage.model, catalog.models)}
							<span className="faint mstage-prov">{label.shared ? `${label.name} via ${label.source}` : label.name}</span>
						</span>
						<span className="mstage-sentence">{missingModel ? "This model isn't available any more. Pick another one." : stageSentence(stage, pool.chosen.length)}</span>
						{unmeasured && <span className="mstage-warn">No access has a {WINDOW_COPY[stage.window].noun} budget, so this stage is skipped. Set one on the Pool page.</span>}
					</span>
				</button>
				<div className="mstage-tools">
					<Tip label="Move up">
						<button className="icon-btn" aria-label={`Move stage ${index + 1} up`} disabled={index === 0} onClick={() => onMove(-1)}>
							<Glyph name="up" size={17} />
						</button>
					</Tip>
					<Tip label="Move down">
						<button className="icon-btn" aria-label={`Move stage ${index + 1} down`} disabled={index === count - 1} onClick={() => onMove(1)}>
							<Glyph name="down" size={17} />
						</button>
					</Tip>
					<Button size="sm" variant={expanded ? "secondary" : "ghost"} onClick={onToggle} aria-controls={`${uid}-body`} aria-expanded={expanded}>
						{expanded ? "Done" : "Edit"}
					</Button>
				</div>
			</div>

			{expanded && (
				<div className="mstage-body" id={`${uid}-body`}>
					<Field label="Model" hint="Only models that are turned on appear. Switching to another provider resets the account choice below.">
						<ModelPicker
							title="Choose the model for this stage"
							value={stage.model}
							models={catalog.models}
							providers={catalog.providers}
							accounts={catalog.accounts}
							onPick={(m) => set({ model: m.id, accounts: m.providerId === providerOfModel(stage.model, catalog.models) ? stage.accounts : "all" })}
							trigger={
								<button className={cx("select-trigger mmodel-trigger", missingModel && "is-invalid")}>
									<ProviderMark provider={label.colorKey} name={label.name} size={20} />
									<span className="grow mmodel-trigger-text">
										{modelName(stage.model, catalog.models)} <span className="faint">{label.name}</span>
									</span>
									<Icon name="chevron" size={16} className="select-chevron" />
								</button>
							}
						/>
					</Field>

					<fieldset className="mstage-fieldset">
						<legend className="field-label">Accounts that can answer</legend>
						<div className="mchoice-row">
							<button className={cx("mchoice", stage.accounts === "all" && "is-on")} aria-pressed={stage.accounts === "all"} onClick={() => set({ accounts: "all" })}>
								<strong>Every {label.name} account</strong>
								<span>{pool.all.length === 1 ? "1 right now" : `${pool.all.length} right now`}; accounts added later join on their own.</span>
							</button>
							<button
								className={cx("mchoice", stage.accounts !== "all" && "is-on")}
								aria-pressed={stage.accounts !== "all"}
								onClick={() => stage.accounts === "all" && set({ accounts: pool.all.map((a) => a.id) })}
							>
								<strong>Only the ones I pick</strong>
								<span>Useful to keep a personal account out, or to reserve one for this route.</span>
							</button>
						</div>
						{stage.accounts !== "all" && (
							<ul className="macct-list">
								{pool.all.length === 0 && <li className="faint">No accounts for {label.name} in the pool.</li>}
								{pool.all.map((a) => {
									const on = (stage.accounts as string[]).includes(a.id);
									return (
										<li key={a.id}>
											<label className="macct">
												<input
													type="checkbox"
													checked={on}
													onChange={(e) => {
														const ids = new Set(stage.accounts as string[]);
														if (e.target.checked) ids.add(a.id);
														else ids.delete(a.id);
														set({ accounts: pool.all.map((x) => x.id).filter((id) => ids.has(id)) });
													}}
												/>
												<span className={cx("dot", a.state === "healthy" ? "dot-good" : a.state === "cooldown" ? "dot-warn" : "dot-bad")} aria-hidden="true" />
												<span className="grow">
													<span className="mono">{a.label}</span>
													<span className="faint macct-meta">{a.state === "healthy" ? "Healthy" : a.state === "cooldown" ? "Cooling down" : a.state === "expired" ? "Sign-in expired" : "Unavailable"}{a.plan ? `, ${a.plan}` : ""}</span>
												</span>
												{vmId && <span className="faint num">{pct(drawn(a.id))} drawn here</span>}
											</label>
										</li>
									);
								})}
							</ul>
						)}
						{stage.accounts !== "all" && (stage.accounts as string[]).length === 0 && <p className="field-error">Pick at least one account, or this stage can never answer.</p>}
					</fieldset>

					<fieldset className="mstage-fieldset">
						<legend className="field-label">When several accounts can answer</legend>
						<div className="mseg" role="radiogroup" aria-label="How to choose an account">
							{(Object.keys(DRAW_COPY) as Draw[]).map((d) => (
								<button key={d} role="radio" aria-checked={stage.draw === d} className={cx("mseg-btn", stage.draw === d && "is-on")} onClick={() => set({ draw: d })}>
									{DRAW_COPY[d].title}
								</button>
							))}
						</div>
						<p className="field-hint">{DRAW_COPY[stage.draw].explain}</p>
					</fieldset>

					<fieldset className="mstage-fieldset">
						<legend className="field-label">Budget</legend>
						<div className="mbudget">
							<div className="mbudget-line">
								<span>Each account gives up to</span>
								<PercentInput value={stage.perAccount} onChange={(v) => set({ perAccount: v })} label="Share of each account's budget" />
								<span>of its budget per</span>
								<Select
									className="minline-select"
									ariaLabel="Budget window"
									value={stage.window}
									onValueChange={(v) => set({ window: v as BudgetWindow })}
									options={(Object.keys(WINDOW_COPY) as BudgetWindow[]).map((w) => ({ value: w, label: WINDOW_COPY[w].noun }))}
								/>
							</div>
							<div className="mbudget-line">
								<span>The whole stage stops at</span>
								<PercentInput value={stage.cap} onChange={(v) => set({ cap: v })} label="Budget for the whole stage" />
								<span>in total</span>
							</div>
							<div className="mbudget-line">
								<span>Count this budget</span>
								<div className="mseg" role="radiogroup" aria-label="Who the budget is counted for">
									<button role="radio" aria-checked={stage.per === "user"} className={cx("mseg-btn", stage.per === "user" && "is-on")} onClick={() => set({ per: "user" })}>
										for each person
									</button>
									<button role="radio" aria-checked={stage.per === "all"} className={cx("mseg-btn", stage.per === "all" && "is-on")} onClick={() => set({ per: "all" })}>
										once, shared by everyone
									</button>
								</div>
							</div>
						</div>
						<p className="field-hint">
							Percentages are shares of each account's budget per {WINDOW_COPY[stage.window].noun} and reset with it. When a stage reaches its budget, the request moves on to the next stage. A person's grant can scale this: half an allotment means half the budget.
						</p>
						{unmeasured ? (
							<Notice tone="warn">
								No access has a {WINDOW_COPY[stage.window].noun} budget, so this stage is skipped. <a href="#/">Set one on the Pool page.</a>
								{gap.otherKinds.length > 0 && ` Some has a budget per ${gap.otherKinds.map((k) => WINDOW_COPY[k].noun).join(" or ")}; pick that period above to use it.`}
							</Notice>
						) : (
							gap.missing > 0 &&
							pool.chosen.length > 0 && (
								<p className="field-hint mwarn-text">
									{gap.missing === 1 ? "One account has" : `${gap.missing} accounts have`} no {WINDOW_COPY[stage.window].noun} budget and will be skipped. <a href="#/">Set one on the Pool page.</a>
								</p>
							)
						)}
						{stage.cap < stage.perAccount && pool.chosen.length > 0 && <p className="field-hint mwarn-text">The stage total is lower than one account's share, so the total is what limits it.</p>}
					</fieldset>

					<Field label="Reasoning effort" hint="Forcing an effort overrides what the client asks for on this stage.">
						<Select
							value={stage.effort ?? "caller"}
							onValueChange={(v) => set({ effort: v === "caller" ? null : (v as Effort) })}
							options={[
								{ value: "caller", label: "Let the client choose" },
								{ value: "low", label: "Always low" },
								{ value: "medium", label: "Always medium" },
								{ value: "high", label: "Always high" },
								{ value: "max", label: "Always max" },
							]}
						/>
					</Field>

					<details className="mstage-advanced" open={!!stage.lua}>
						<summary>Script conditions (advanced)</summary>
						<p className="field-hint">Most stages don't need this. A routing script can decide which accounts may answer, or calculate shares from live quota reports. Pick a function from the routing script for any of these; the rest stay built in.</p>
						{LUA_ROWS.map((row) => {
							const fn = stage.lua?.[row.key];
							const scripted = fn !== undefined;
							const valid = !scripted || LUA_NAME_RE.test(fn);
							return (
								<div key={row.key} className="mlua-row">
									<Field label={row.label} hint={scripted ? (valid ? row.hint : undefined) : undefined} error={valid ? undefined : "Use a function name: letters, digits and underscores, without quotes or ()."}>
										<div className="mlua-controls">
											<Select
												ariaLabel={`${row.label}: built in or script`}
												value={scripted ? "lua" : "builtin"}
												onValueChange={(mode) => {
													const lua = { ...stage.lua };
													if (mode === "lua") lua[row.key] = fn ?? row.suggested;
													else delete lua[row.key];
													set({ lua: Object.keys(lua).length ? lua : undefined });
												}}
												options={[
													{ value: "builtin", label: "Built in", description: row.builtin(stage) },
													{ value: "lua", label: "Script function", description: "Defined in the routing script" },
												]}
											/>
											{scripted && <Input mono aria-label={`${row.label} function name`} value={fn} placeholder={row.suggested} maxLength={64} onChange={(e) => set({ lua: { ...stage.lua, [row.key]: e.target.value.trim() } })} />}
										</div>
									</Field>
								</div>
							);
						})}
						<div>
							<Button size="sm" variant="ghost" onClick={onEditScript}>
								Open the routing script
							</Button>
						</div>
					</details>

					<div className="mstage-foot">
						<Button variant="danger" size="sm" icon="trash" onClick={onRemove}>
							Remove stage
						</Button>
					</div>
				</div>
			)}
		</Reorder.Item>
	);
}

function PercentInput({ value, onChange, label }: { value: number; onChange: (v: number) => void; label: string }) {
	const shown = Number((value * 100).toPrecision(12));
	return (
		<span className="mpct">
			<input
				className="input num"
				type="number"
				min={0}
				max={100}
				step={0.5}
				aria-label={`${label}, percent`}
				value={Number.isFinite(shown) ? shown : 0}
				onChange={(e) => {
					const n = Number(e.target.value);
					if (Number.isFinite(n)) onChange(Math.min(100, Math.max(0, n)) / 100);
				}}
			/>
			<span aria-hidden="true">%</span>
		</span>
	);
}
