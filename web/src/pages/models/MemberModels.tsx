import { useQuery } from "@tanstack/react-query";
import type { Session } from "../../api/types";
import { CodeLine, EmptyState, Notice, PageHeader, Section, Skeleton, cx } from "../../ui/components";
import { RouteChain } from "./Chain";
import { simulate, useModels, useUsage, useUser, useVault, useVirtualModels, type Grant, type NeoAccount, type NeoModel, type NeoProvider, type Usage, type VirtualModel } from "./data";
import { labelProvider, modelName, pct } from "./route";

const OUTCOME: Record<string, { label: string; tone: string }> = {
	ok: { label: "Available now", tone: "good" },
	queued: { label: "Would wait for room", tone: "warn" },
	limited: { label: "Your budget is used up", tone: "warn" },
};

/** What a member or contributor sees: the names they may use and how to connect. */
export function MemberModels({ session }: { session: Session }) {
	const user = useUser(session.userId);
	const vms = useVirtualModels();
	const models = useModels();
	const vault = useVault();
	const usage = useUsage();
	const base = `${location.protocol}//${location.host}`;
	const grants = user.data?.grants ?? [];
	const mine = grants.map((g) => ({ g, vm: vms.data?.find((v) => v.id === g.virtualModel) })).filter((x): x is { g: Grant; vm: VirtualModel } => !!x.vm);
	const first = mine[0]?.vm.served;
	const loading = user.isPending || vms.isPending;
	const error = user.error || vms.error;

	return (
		<>
			<PageHeader
				title="Models you can use"
				lede="Put one of these names in your client as the model, with your inference key as the API key. NeoProxy picks which provider answers, so the name keeps working when providers change."
			/>
			<p className="member-key-line">
				Need a key? <a href="#/people">Issue one under Your account</a>.
			</p>

			{error && (
				<Notice tone="bad" title="Couldn't load your models">
					{(error as Error).message}. NeoProxy may be restarting; this page retries on its own.
				</Notice>
			)}

			<Section title="Connect a client" description="Most clients ask for a base URL. Use the full endpoint only if yours wants one.">
				<div className="panel mconnect">
					<dl className="mconnect-list">
						<div>
							<dt>Base URL</dt>
							<dd>
								<CodeLine>{`${base}/v1`}</CodeLine>
							</dd>
						</div>
						<div>
							<dt>OpenAI chat completions</dt>
							<dd>
								<CodeLine>{`${base}/v1/chat/completions`}</CodeLine>
							</dd>
						</div>
						<div>
							<dt>OpenAI responses</dt>
							<dd>
								<CodeLine>{`${base}/v1/responses`}</CodeLine>
							</dd>
						</div>
						<div>
							<dt>Anthropic messages</dt>
							<dd>
								<CodeLine>{`${base}/v1/messages`}</CodeLine>
							</dd>
						</div>
						<div>
							<dt>Model</dt>
							<dd>{first ? <CodeLine>{first}</CodeLine> : <span className="faint">One of the names below, once you have a grant.</span>}</dd>
						</div>
					</dl>
					<p className="faint mconnect-note">Your dashboard sign-in isn't an API key; clients need an inference key.</p>
				</div>
			</Section>

			<Section title="Your models" description={mine.length ? "Each name routes through the stages your administrator set up. Budgets reset on their own." : undefined}>
				{loading ? (
					<Skeleton h={160} r={14} />
				) : mine.length === 0 ? (
					<div className="panel">
						<EmptyState title="Nothing granted to you yet">Your administrator decides which models you can use. Ask them to grant you one, for example neoproxy/default.</EmptyState>
					</div>
				) : (
					<ul className="member-cards">
						{mine.map(({ g, vm }) => (
							<MemberCard key={vm.id} vm={vm} grant={g} userId={session.userId} models={models.data ?? []} providers={vault.data?.providers ?? []} accounts={vault.data?.accounts ?? []} usage={usage.data} />
						))}
					</ul>
				)}
			</Section>

			{(user.data?.providerAccess.length ?? 0) > 0 && (
				<Section title="Direct provider access" description="You may also call these providers' models by name. Direct calls use the provider's own limits, not a budget.">
					<div className="panel mdirect">
						{user.data!.providerAccess.map((pid) => {
							const label = labelProvider(pid, vault.data?.providers);
							const example = models.data?.find((m) => m.providerId === pid && m.exposed);
							return (
								<div key={pid} className="mdirect-row">
									<strong>{label.name}</strong>
									<span className="faint">
										Use <code>neoproxy/{pid}/</code> followed by a model id{example ? (
											<>
												, for example <code>{example.served}</code>
											</>
										) : null}
										.
									</span>
								</div>
							);
						})}
					</div>
				</Section>
			)}
		</>
	);
}

function MemberCard({ vm, grant, userId, models, providers, accounts, usage }: { vm: VirtualModel; grant: Grant; userId: string; models: NeoModel[]; providers: NeoProvider[]; accounts: NeoAccount[]; usage: Usage | undefined }) {
	const sim = useQuery({ queryKey: ["models", "member-sim", vm.id], queryFn: () => simulate({ asked: vm.served, userId }), refetchInterval: 60_000 });
	let drawn = 0;
	for (const st of Object.values(usage?.[userId]?.[vm.id] ?? {})) for (const v of Object.values(st)) drawn += v;
	const total = grant.allotment * vm.stages.reduce((n, s) => n + s.cap, 0);
	const share = total ? Math.min(1, drawn / total) : 0;
	const outcome = sim.data ? OUTCOME[sim.data.outcome] ?? { label: "Unavailable right now", tone: "bad" } : undefined;
	const lit = sim.data ? (sim.data.stageIndex ?? (sim.data.trail?.some((t) => t.kind === "terminal") ? vm.stages.length : undefined)) : undefined;
	return (
		<li className="panel member-card">
			<div className="member-head">
				<CodeLine>{vm.served}</CodeLine>
				{outcome && <span className={cx("chip", `chip-${outcome.tone}`)}>{outcome.label}</span>}
			</div>
			{vm.description && <p className="vm-desc">{vm.description}</p>}
			<RouteChain stages={vm.stages} terminal={vm.terminal} catalog={{ models, providers, accounts }} lit={lit} />
			<div className="member-foot">
				<div className="mmeter" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(share * 100)} aria-label="Allotment used">
					<span style={{ width: `${share * 100}%` }} />
				</div>
				<span className="muted">
					{pct(share)} of your allotment used{grant.allotment !== 1 ? `, and your allotment is ${pct(grant.allotment)} of the standard one` : ""}.
				</span>
			</div>
			{sim.data && (
				<p className="faint member-now">
					{sim.data.outcome === "ok"
						? sim.data.stageIndex !== undefined
							? `Right now your requests are answered by stage ${sim.data.stageIndex + 1}, ${modelName(sim.data.model, models)}.`
							: `Right now your requests fall back to ${modelName(sim.data.model, models)}.`
						: sim.data.message || sim.data.reason || "No stage can answer right now; try again after the budget resets."}
				</p>
			)}
		</li>
	);
}
