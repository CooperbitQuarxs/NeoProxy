import { AnimatePresence, motion } from "motion/react";
import { ProviderMark, cx } from "../../ui/components";
import type { Decision, NeoAccount, NeoModel, NeoProvider, Stage, Terminal } from "./data";
import { WINDOW_COPY, colorOf, labelProvider, modelName, pct, providerOfModel, stagePool, terminalLine, windowGap } from "./route";

type Catalog = { models?: NeoModel[]; providers?: NeoProvider[]; accounts?: NeoAccount[] };

function StepMark({ stage, catalog, size = 20 }: { stage: Stage | string; catalog: Catalog; size?: number }) {
	const modelId = typeof stage === "string" ? stage : stage.model;
	const label = labelProvider(providerOfModel(modelId, catalog.models), catalog.providers);
	return <ProviderMark provider={label.colorKey} name={label.name} size={size} />;
}

/** A route read left to right: stages in order, then what happens when all are out of room. */
export function RouteChain({ stages, terminal, catalog, lit }: { stages: Stage[]; terminal: Terminal; catalog: Catalog; lit?: number }) {
	return (
		<ol className="mchain" aria-label="Route">
			{stages.map((s, i) => {
				const label = labelProvider(providerOfModel(s.model, catalog.models), catalog.providers);
				return (
					<li key={s.id} className="mchain-item">
						{i > 0 && (
							<span className="mchain-link" aria-hidden="true">
								<span className="mchain-link-text">else</span>
							</span>
						)}
						<span className={cx("mchain-step", lit === i && "is-lit")} style={{ ["--p" as string]: colorOf(label) }}>
							<StepMark stage={s} catalog={catalog} />
							<span className="mchain-text">
								<span className="mchain-model">{modelName(s.model, catalog.models)}</span>
								<span className="mchain-prov">{label.shared ? `${label.name}, shared by ${label.source}` : label.name}</span>
							</span>
						</span>
					</li>
				);
			})}
			<li className="mchain-item">
				{stages.length > 0 && (
					<span className="mchain-link" aria-hidden="true">
						<span className="mchain-link-text">then</span>
					</span>
				)}
				<span className={cx("mchain-end", lit === stages.length && "is-lit")}>
					<span className="sr-only">When every stage is out of room: </span>
					{terminalLine(terminal, catalog.models)}
				</span>
			</li>
		</ol>
	);
}

type StepState = "idle" | "lit" | "skipped";

function stepStates(stages: Stage[], decision?: Decision | null): StepState[] {
	const states: StepState[] = Array(stages.length + 1).fill("idle");
	if (!decision) return states;
	for (const t of decision.trail ?? []) {
		if (t.kind === "stage" && t.index !== undefined && t.index < stages.length) states[t.index] = t.skipped ? "skipped" : "idle";
	}
	const served = decision.outcome === "ok" || decision.outcome === "queued" || decision.outcome === "limited";
	if (served && decision.stageIndex !== undefined) states[decision.stageIndex] = "lit";
	else if (decision.trail?.some((t) => t.kind === "terminal")) states[stages.length] = "lit";
	return states;
}

/** Vertical "how a request flows" preview used by the editor; updates as the draft changes. */
export function RouteFlow({ name, stages, terminal, catalog, decision }: { name: string; stages: Stage[]; terminal: Terminal; catalog: Catalog; decision?: Decision | null }) {
	const states = stepStates(stages, decision);
	return (
		<ol className="mflow" aria-label="How a request flows">
			<li className="mflow-start">
				<span className="mflow-dot" aria-hidden="true" />
				<span>
					A client asks for <code className="mflow-name">neoproxy/{name || "…"}</code>
				</span>
			</li>
			<AnimatePresence initial={false}>
				{stages.map((s, i) => {
					const label = labelProvider(providerOfModel(s.model, catalog.models), catalog.providers);
					const chosen = stagePool(s, catalog.accounts, catalog.models).chosen;
					const pool = chosen.length;
					const unmeasured = pool > 0 && windowGap(s, chosen).measured === 0;
					return (
						<motion.li
							key={s.id}
							layout="position"
							initial={{ opacity: 0, y: -6 }}
							animate={{ opacity: 1, y: 0 }}
							exit={{ opacity: 0, height: 0, marginTop: 0 }}
							transition={{ type: "spring", stiffness: 520, damping: 40 }}
							className={cx("mflow-step", `is-${states[i]}`)}
							style={{ ["--p" as string]: colorOf(label) }}
						>
							<span className="mflow-conn">{i === 0 ? "Tries first" : "If that stage has no room"}</span>
							<div className="mflow-card">
								<StepMark stage={s} catalog={catalog} size={24} />
								<div className="grow">
									<p className="mflow-title">
										<span className="faint num">{i + 1}</span> {modelName(s.model, catalog.models)}
									</p>
									<p className="mflow-sub">
										{label.shared ? `${label.name} via ${label.source}` : label.name}, {pool === 1 ? "1 account" : `${pool} accounts`}, up to {pct(s.cap)} of the budget per {WINDOW_COPY[s.window].noun},
										{s.per === "all" ? " shared by everyone" : " for each person"}
									</p>
									{unmeasured && <p className="mflow-warn">Skipped: no access has a {WINDOW_COPY[s.window].noun} budget</p>}
									{pool === 0 && <p className="mflow-warn">No account can answer this stage</p>}
								</div>
								{states[i] === "lit" && <span className="mflow-badge">Would answer</span>}
								{states[i] === "skipped" && <span className="mflow-badge is-muted">Skipped</span>}
							</div>
						</motion.li>
					);
				})}
			</AnimatePresence>
			<motion.li layout="position" className={cx("mflow-step mflow-terminal", `is-${states[stages.length]}`, decision?.outcome === "ok" && "is-answered")}>
				<span className="mflow-conn">{stages.length ? "If every stage is out of room" : "With no stages yet"}</span>
				<div className="mflow-card">
					<div className="grow">
						<p className="mflow-title">{terminalLine(terminal, catalog.models)}</p>
						{terminal.message && terminal.kind !== "passthrough" && <p className="mflow-sub">“{terminal.message}”</p>}
					</div>
					{states[stages.length] === "lit" && <span className="mflow-badge">{decision?.outcome === "ok" ? "Would answer" : "Would end here"}</span>}
				</div>
			</motion.li>
		</ol>
	);
}
