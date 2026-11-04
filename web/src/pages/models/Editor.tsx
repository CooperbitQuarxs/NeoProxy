import { MotionConfig, Reorder } from "motion/react";
import { useEffect, useMemo, useRef, useState } from "react";
import type { Session } from "../../api/types";
import { navigate } from "../../app/router";
import { plural } from "../../lib/format";
import { Avatar, Button, Dialog, Field, Notice, PageHeader, ProviderMark, Skeleton, cx } from "../../ui/components";
import { Icon } from "../../ui/icons";
import { useToast } from "../../ui/toast";
import { RouteFlow } from "./Chain";
import {
	useDeleteVirtualModel,
	useModels,
	useSaveVirtualModel,
	useUsage,
	useUsers,
	useVault,
	useVirtualModel,
	useVirtualModels,
	type Decision,
	type NeoAccount,
	type NeoModel,
	type NeoProvider,
	type NeoUser,
	type Stage,
	type TerminalKind,
	type Usage,
	type VirtualModel,
	type VirtualModelDraft,
} from "./data";
import { ModelPicker } from "./ModelPicker";
import { LUA_NAME_RE, NAME_RE, TERMINAL_COPY, blankDraft, isGenerated, labelProvider, modelName, newStage, pct, providerOfModel } from "./route";
import { StageCard } from "./StageCard";
import { TryRequest, isScriptError } from "./TryRequest";

export function Editor({ id, session }: { id: string; session: Session }) {
	const isNew = id === "new";
	const vm = useVirtualModel(isNew ? undefined : id);
	const models = useModels();
	const vault = useVault();
	const vms = useVirtualModels();
	const users = useUsers();
	const usage = useUsage();

	const loading = (!isNew && vm.isPending) || models.isPending || vault.isPending || vms.isPending;
	const failed = (!isNew && vm.error) || models.error || vault.error;
	if (failed) {
		return (
			<>
				<BackLink />
				<Notice tone="bad" title={isNew ? "Couldn't load the models to route to" : "Couldn't open this virtual model"}>
					{(failed as Error).message}. {isNew ? "NeoProxy may be restarting; reload in a moment." : "It may have been deleted. Go back to the list and pick it again."}
				</Notice>
			</>
		);
	}
	if (loading) {
		return (
			<>
				<BackLink />
				<Skeleton h={40} w={360} />
				<div className="ed-grid">
					<div className="stack">
						<Skeleton h={180} r={14} />
						<Skeleton h={260} r={14} />
					</div>
					<Skeleton h={320} r={14} />
				</div>
			</>
		);
	}
	return (
		<EditorForm
			key={vm.data?.id ?? "new"}
			saved={isNew ? undefined : vm.data}
			models={models.data ?? []}
			providers={vault.data?.providers ?? []}
			accounts={vault.data?.accounts ?? []}
			others={(vms.data ?? []).filter((v) => v.id !== id)}
			users={users.data ?? []}
			usage={usage.data}
			session={session}
		/>
	);
}

function BackLink() {
	return (
		<a className="back-link" href="#/models">
			<Icon name="back" size={16} />
			All models
		</a>
	);
}

const LUA_EXAMPLE = `-- Quota report kinds may be namespaced, e.g. "zai:tokens:5h".
local function five_hour_used(ctx)
  local worst = nil
  for _, w in ipairs(ctx.account.providerWindows or {}) do
    local kind = w.kind
    if w.known and (kind == "5h" or string.sub(kind, -3) == ":5h") then
      if worst == nil or w.used > worst then worst = w.used end
    end
  end
  return worst -- nil means no known five-hour report.
end

function reported_room(ctx)
  local used = five_hour_used(ctx)
  if used == nil then return false end
  return used < 0.8
end

function available_share(ctx)
  local used = five_hour_used(ctx)
  if used == nil then return 0 end
  return math.min(0.05, (1 - used) * 0.1)
end

function pool_share(ctx)
  return 0.15
end`;

const strip = (v: VirtualModelDraft) => JSON.stringify({ name: v.name, description: v.description, stages: v.stages, terminal: v.terminal, script: v.script ?? "" });

function EditorForm({
	saved,
	models,
	providers,
	accounts,
	others,
	users,
	usage,
	session,
}: {
	saved: VirtualModel | undefined;
	models: NeoModel[];
	providers: NeoProvider[];
	accounts: NeoAccount[];
	others: VirtualModel[];
	users: NeoUser[];
	usage: Usage | undefined;
	session: Session;
}) {
	const toast = useToast();
	const save = useSaveVirtualModel();
	const remove = useDeleteVirtualModel();
	const [draft, setDraft] = useState<VirtualModelDraft>(() => structuredClone(saved ?? blankDraft()));
	const [baseline, setBaseline] = useState(() => strip(saved ?? blankDraft()));
	const [open, setOpen] = useState<string | null>(null);
	const [decision, setDecision] = useState<Decision | null>(null);
	const [touched, setTouched] = useState(false);
	const [scriptError, setScriptError] = useState<string | null>(null);
	const [scriptOpen, setScriptOpen] = useState(() => !!saved?.script?.trim() || !!saved?.stages.some((s) => s.lua));
	const [confirm, setConfirm] = useState<"delete" | "discard" | null>(null);
	const [formError, setFormError] = useState<string | null>(null);
	const nameRef = useRef<HTMLInputElement>(null);
	const scriptRef = useRef<HTMLTextAreaElement>(null);
	const isNew = !saved;
	const dirty = strip(draft) !== baseline;
	const catalog = { models, providers, accounts };
	const generated = saved ? isGenerated(saved, models) : false;
	const granted = saved ? users.filter((u) => u.grants.some((g) => g.virtualModel === saved.id)) : [];

	useEffect(() => {
		if (!dirty) return;
		const on = (e: BeforeUnloadEvent) => e.preventDefault();
		window.addEventListener("beforeunload", on);
		return () => window.removeEventListener("beforeunload", on);
	}, [dirty]);

	const name = draft.name;
	const nameIssue = useMemo(() => {
		if (!name) return "empty";
		if (!NAME_RE.test(name)) return "Use lowercase letters, digits and dashes, starting with a letter, up to 32 characters.";
		if (others.some((v) => v.name === name)) return "Another virtual model already has this name.";
		if (providers.some((p) => p.id === name) || models.some((m) => m.id === name || m.served === `neoproxy/${name}`)) return "That name is taken by a provider or one of its models. Pick a role-like name instead.";
		return null;
	}, [name, others, providers, models]);

	const set = (patch: Partial<VirtualModelDraft>) => {
		setDraft((d) => ({ ...d, ...patch }));
		setFormError(null);
	};
	const setStages = (stages: Stage[]) => set({ stages });
	const moveStage = (i: number, delta: number) => {
		const j = i + delta;
		if (j < 0 || j >= draft.stages.length) return;
		const next = draft.stages.slice();
		[next[i], next[j]] = [next[j], next[i]];
		setStages(next);
	};
	const editScript = () => {
		setScriptOpen(true);
		requestAnimationFrame(() => {
			scriptRef.current?.scrollIntoView({ block: "center", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
			scriptRef.current?.focus({ preventScroll: true });
		});
	};

	const validate = (): string | null => {
		if (nameIssue) {
			setTouched(true);
			nameRef.current?.focus();
			return nameIssue === "empty" ? "Give it a name first." : nameIssue;
		}
		if (!draft.stages.length) return "Add at least one stage, so the name has a model to answer with.";
		const noAccounts = draft.stages.findIndex((s) => s.accounts !== "all" && s.accounts.length === 0);
		if (noAccounts >= 0) {
			setOpen(draft.stages[noAccounts].id);
			return `Stage ${noAccounts + 1} has no accounts picked. Pick at least one, or choose every account.`;
		}
		const missing = draft.stages.findIndex((s) => !models.some((m) => m.id === s.model));
		if (missing >= 0) {
			setOpen(draft.stages[missing].id);
			return `Stage ${missing + 1} points at a model that isn't available any more. Pick another model.`;
		}
		for (const [i, s] of draft.stages.entries()) {
			if (!s.lua) continue;
			if (Object.values(s.lua).some((fn) => !fn || !LUA_NAME_RE.test(fn))) {
				setOpen(s.id);
				return `Stage ${i + 1} has a script function name that isn't valid. Use letters, digits and underscores.`;
			}
			if (!draft.script?.trim()) {
				editScript();
				return `Stage ${i + 1} uses script functions, but the routing script is empty. Add the script, or switch the stage back to built in.`;
			}
		}
		if (draft.terminal.kind === "passthrough" && !draft.terminal.model) return "Choose the fallback model, or pick another way to end.";
		return null;
	};

	const onSave = async () => {
		const problem = validate();
		if (problem) {
			setFormError(problem);
			return;
		}
		try {
			const result = await save.mutateAsync({ ...draft, served: `neoproxy/${draft.name}` });
			setScriptError(null);
			toast(`Saved neoproxy/${result.name}`);
			if (isNew) navigate(`models/${encodeURIComponent(result.id)}`);
			else {
				setDraft(structuredClone(result));
				setBaseline(strip(result));
			}
		} catch (e) {
			if (isScriptError(e)) {
				setScriptError((e as Error).message);
				editScript();
				setFormError("Not saved: the routing script has an error. Details are under the script.");
			} else setFormError(`Not saved: ${(e as Error).message}.`);
		}
	};

	const onDelete = async () => {
		if (!saved) return;
		try {
			await remove.mutateAsync(saved.id);
			toast(`Deleted ${saved.served}`);
			navigate("models");
		} catch (e) {
			toast(`Couldn't delete it: ${(e as Error).message}`, "bad");
			setConfirm(null);
		}
	};

	const leave = () => (dirty ? setConfirm("discard") : navigate("models"));
	const usedProviders = new Set(draft.stages.map((s) => providerOfModel(s.model, models)));
	const servedModels = models.filter((m) => m.exposed && providers.find((p) => p.id === m.providerId)?.exposed);

	return (
		<MotionConfig reducedMotion="user">
			<BackLink />
			<PageHeader
				title={
					isNew ? (
						"New virtual model"
					) : (
						<span className="mono ed-title">
							<span className="faint">neoproxy/</span>
							{saved.name}
						</span>
					)
				}
				lede={
					isNew
						? "Give it a name people will type, then choose which models answer it, in order. NeoProxy tries each stage until one has room."
						: generated
							? "NeoProxy created this route for a model shared from NeoPi. You can rename it, add fallbacks or change its budget like any other."
							: saved.description || "A name people use in their clients. Change its stages here and nobody has to touch their setup."
				}
				actions={
					<>
						{!isNew && (
							<Button variant="ghost" icon="trash" onClick={() => setConfirm("delete")}>
								Delete
							</Button>
						)}
						<Button variant="secondary" onClick={leave}>
							{dirty ? "Discard changes" : "Close"}
						</Button>
						<Button variant="primary" busy={save.isPending} disabled={!dirty && !isNew} onClick={onSave}>
							{isNew ? "Create virtual model" : dirty ? "Save changes" : "Saved"}
						</Button>
					</>
				}
			/>

			{formError && (
				<Notice tone="bad" title="Can't save yet">
					{formError}
				</Notice>
			)}

			<div className="ed-grid">
				<div className="ed-main">
					<section className="panel ed-panel" aria-labelledby="ed-name">
						<h2 id="ed-name" className="ed-panel-title">
							Name and description
						</h2>
						<Field
							label="Name"
							htmlFor="vm-name"
							error={touched && nameIssue && nameIssue !== "empty" ? nameIssue : touched && nameIssue === "empty" ? "A name is required." : undefined}
							hint={
								!nameIssue ? (
									<span className="mname-ok">
										<Icon name="check" size={14} /> People will ask for <code>neoproxy/{name}</code>
									</span>
								) : (
									"What people type as the model in their client. Name it for a job, not a brand, so you can change providers later: default, fast, review."
								)
							}
						>
							<div className={cx("mprefixed", touched && nameIssue && "is-invalid")}>
								<span className="mprefixed-pre mono" aria-hidden="true">
									neoproxy/
								</span>
								<input
									ref={nameRef}
									id="vm-name"
									className="mono"
									value={name}
									placeholder="office-default"
									autoComplete="off"
									spellCheck={false}
									maxLength={32}
									aria-invalid={touched && !!nameIssue}
									onChange={(e) => {
										set({ name: e.target.value.trim().toLowerCase() });
										setTouched(true);
									}}
								/>
							</div>
						</Field>
						{saved && name !== saved.name && !nameIssue && (
							<Notice tone="warn" title="Renaming changes what people type">
								Clients still asking for <code>{saved.served}</code> will get an unknown-model error until they switch to <code>neoproxy/{name}</code>.
							</Notice>
						)}
						<Field label="Description" htmlFor="vm-desc" optional hint="One sentence people see next to the name, like “Fast everyday model; falls back to Claude when Codex runs out.”">
							<textarea id="vm-desc" className="input" rows={2} maxLength={4096} value={draft.description} onChange={(e) => set({ description: e.target.value })} />
						</Field>
					</section>

					<section className="panel ed-panel" aria-labelledby="ed-stages">
						<header className="ed-panel-head">
							<div className="grow">
								<h2 id="ed-stages" className="ed-panel-title">
									Stages
								</h2>
								<p className="muted ed-panel-desc">
									NeoProxy tries stages from the top. A stage answers when it has a healthy account and budget left; otherwise the request moves to the next one. Drag the handle or use the arrows to reorder.
								</p>
							</div>
						</header>
						{draft.stages.length > 0 ? (
							<Reorder.Group axis="y" values={draft.stages} onReorder={setStages} className="mstages">
								{draft.stages.map((s, i) => (
									<StageCard
										key={s.id}
										stage={s}
										index={i}
										count={draft.stages.length}
										expanded={open === s.id}
										onToggle={() => setOpen((o) => (o === s.id ? null : s.id))}
										onChange={(next) => setStages(draft.stages.map((x) => (x.id === s.id ? next : x)))}
										onMove={(d) => moveStage(i, d)}
										onRemove={() => {
											setStages(draft.stages.filter((x) => x.id !== s.id));
											setOpen(null);
										}}
										catalog={catalog}
										usage={usage}
										vmId={saved?.id ?? null}
										onEditScript={editScript}
									/>
								))}
							</Reorder.Group>
						) : (
							<div className="mstages-empty">
								<p>
									<strong>No stages yet.</strong> Add the model that should answer first. You can add fallbacks after it, for example a second provider for when the first one runs out.
								</p>
							</div>
						)}
						{servedModels.length === 0 ? (
							<Notice tone="info" title="No models are being served">
								Stages pick from served provider models. <a href="#/">Add provider access on the Pool page</a>, or turn models on under <a href="#/models?view=providers">Provider models</a>.
							</Notice>
						) : (
							<ModelPicker
								title="Choose a model for the new stage"
								models={models}
								providers={providers}
								accounts={accounts}
								onPick={(m) => {
									const s = newStage(m.id, draft.stages);
									setStages([...draft.stages, s]);
									setOpen(s.id);
								}}
								trigger={
									<button className="madd-stage">
										<Icon name="plus" size={17} />
										<span>
											<strong>{draft.stages.length ? "Add a fallback stage" : "Add the first stage"}</strong>
											<span className="faint">
												{draft.stages.length
													? usedProviders.size < providers.filter((p) => p.exposed).length
														? "Tip: a different provider here keeps the name working when the first one runs out."
														: "Tried when every stage above is out of room."
													: "Pick from the models your pool serves."}
											</span>
										</span>
									</button>
								}
							/>
						)}
					</section>

					<TerminalPanel draft={draft} set={set} models={models} providers={providers} accounts={accounts} />

					{session.role === "admin" && (
						<details className="panel ed-panel ed-script" open={scriptOpen} onToggle={(e) => setScriptOpen((e.target as HTMLDetailsElement).open)}>
							<summary className="ed-panel-title">Routing script (advanced)</summary>
							<div className="ed-script-body">
							<p className="muted ed-panel-desc">
								Most virtual models don't need one. A Lua script can add conditions per account, like “only accounts below 80% of their five-hour quota”, or calculate shares. Stages use its functions by name under Script conditions. Scripts can only lower the budgets you set; health and account limits still apply.
							</p>
							<Field
								label="Lua source"
								htmlFor="vm-script"
								optional
								error={scriptError ?? undefined}
								hint="Up to 16 KiB. Saving checks the syntax; Try a request checks the functions and what they return. Scripts can't reach files, the network or credentials. If a script fails, the request fails instead of guessing."
							>
								<textarea
									ref={scriptRef}
									id="vm-script"
									className="input mono"
									rows={10}
									spellCheck={false}
									maxLength={16384}
									placeholder="Leave empty for standard routing"
									value={draft.script ?? ""}
									onChange={(e) => {
										set({ script: e.target.value });
										setScriptError(null);
									}}
								/>
							</Field>
							<details className="ed-example">
								<summary>What functions receive, and an example</summary>
								<div className="stack">
									<p className="field-hint">
										A condition function receives <code>ctx</code> and returns true or false for one account. A share function returns a fraction of the account's window (0.05 means 5%). A stage budget function returns the whole stage's fraction and gets no account. <code>ctx.request</code>, <code>ctx.stage</code>, <code>ctx.allotment</code>, and for account functions <code>ctx.account</code>, <code>ctx.window</code>, <code>ctx.used</code> and <code>ctx.budget</code> are read-only.
									</p>
									<p className="field-hint">
										<code>account.windows</code> are the budget windows NeoProxy tracks; <code>account.providerWindows</code> are quotas the provider reports. Check <code>known</code> before using <code>used</code>, and match the end of the kind name, since kinds can be namespaced like <code>zai:tokens:5h</code>.
									</p>
									<pre className="ed-code mono">{LUA_EXAMPLE}</pre>
									<p className="field-hint">
										This example only lets an account answer while its known five-hour report is under 80%, gives each account up to 5% shrinking as the report fills, and caps the stage at 15%. Accounts without a report are left out on purpose; add a built-in stage after it to use them as a fallback.
									</p>
									{(() => {
										const kinds = [...new Set(accounts.flatMap((a) => (a.providerWindows ?? []).map((w) => w.kind)))].sort();
										return kinds.length ? <p className="field-hint">Quota kinds your accounts report right now: {kinds.map((k, i) => <span key={k}>{i > 0 && ", "}<code>{k}</code></span>)}</p> : null;
									})()}
									<div>
										<Button
											size="sm"
											disabled={!!draft.script?.trim() || !draft.stages.length}
											onClick={() => {
												const target = draft.stages.find((s) => s.id === open) ?? draft.stages[0];
												set({ script: LUA_EXAMPLE, stages: draft.stages.map((s) => (s.id === target.id ? { ...s, lua: { eligible: "reported_room", perAccount: "available_share", cap: "pool_share" } } : s)) });
												setOpen(target.id);
											}}
										>
											Use this example on {draft.stages.find((s) => s.id === open) ? "the open stage" : "the first stage"}
										</Button>
										{(draft.script?.trim() || !draft.stages.length) && <p className="field-hint">{draft.stages.length ? "Clear the script box first; the example never replaces your own script." : "Add a stage first."}</p>}
									</div>
								</div>
							</details>
							</div>
						</details>
					)}

					<section className="panel ed-panel" aria-labelledby="ed-who">
						<h2 id="ed-who" className="ed-panel-title">
							Who can use it
						</h2>
						{isNew ? (
							<p className="muted">After you create it, grant it to people on the People page. Each grant can scale the budgets above with an allotment.</p>
						) : granted.length ? (
							<>
								<p className="muted ed-panel-desc">Each person's allotment scales every budget in this route: half an allotment is half of each share and half of each stage total. Change grants on the People page.</p>
								<ul className="mgrant-list">
									{granted.map((u) => {
										const g = u.grants.find((x) => x.virtualModel === saved.id)!;
										let drawnTotal = 0;
										for (const st of Object.values(usage?.[u.id]?.[saved.id] ?? {})) for (const v of Object.values(st)) drawnTotal += v;
										return (
											<li key={u.id}>
												<a className="mgrant" href={`#/people/${encodeURIComponent(u.id)}`}>
													<Avatar name={u.handle} size={28} />
													<span className="grow">
														<strong>{u.name || u.handle}</strong>
														<span className="faint">{g.allotment === 1 ? "Full allotment" : `${pct(g.allotment)} allotment`}</span>
													</span>
													<span className="faint num">{pct(drawnTotal)} drawn</span>
													<Icon name="chevronRight" size={16} className="faint" />
												</a>
											</li>
										);
									})}
								</ul>
							</>
						) : (
							<p className="muted">
								Nobody yet. Grant it on the <a href="#/people">People page</a> so people can use it with their keys.
							</p>
						)}
					</section>
				</div>

				<aside className="ed-side">
					<section className="panel mflow-panel" aria-labelledby="flow-title">
						<h2 id="flow-title" className="mside-title">
							How a request flows
						</h2>
						<RouteFlow name={name} stages={draft.stages} terminal={draft.terminal} catalog={catalog} decision={dirty ? null : decision} />
					</section>
					<TryRequest vm={saved} dirty={dirty} users={users} models={models} accounts={accounts} onDecision={setDecision} onEditScript={session.role === "admin" ? editScript : undefined} />
				</aside>
			</div>

			<Dialog
				open={confirm === "delete"}
				onOpenChange={(v) => !v && setConfirm(null)}
				title={`Delete ${saved?.served ?? ""}?`}
				description="The name stops working right away and can't be brought back."
				footer={
					<>
						<Button variant="ghost" onClick={() => setConfirm(null)}>
							Keep it
						</Button>
						<Button variant="danger" busy={remove.isPending} onClick={onDelete}>
							Delete virtual model
						</Button>
					</>
				}
			>
				<p>
					Clients asking for <code>{saved?.served}</code> will get an unknown-model error.{" "}
					{granted.length
						? `${plural(granted.length, "person", "people")} lose${granted.length === 1 ? "s" : ""} the grant: ${granted.map((u) => u.handle).join(", ")}.`
						: "Nobody is granted it, so no one loses access."}
				</p>
				<p className="muted">The provider models and accounts it routes to aren't touched.</p>
			</Dialog>

			<Dialog
				open={confirm === "discard"}
				onOpenChange={(v) => !v && setConfirm(null)}
				title="Discard your changes?"
				description={isNew ? "This virtual model hasn't been created yet." : "The route goes back to how it was last saved."}
				footer={
					<>
						<Button variant="ghost" onClick={() => setConfirm(null)}>
							Keep editing
						</Button>
						<Button variant="danger" onClick={() => navigate("models")}>
							Discard
						</Button>
					</>
				}
			>
				<p className="muted">Nothing you changed here has been applied. People keep using the saved version.</p>
			</Dialog>
		</MotionConfig>
	);
}

function TerminalPanel({
	draft,
	set,
	models,
	providers,
	accounts,
}: {
	draft: VirtualModelDraft;
	set: (p: Partial<VirtualModelDraft>) => void;
	models: NeoModel[];
	providers: NeoProvider[];
	accounts: NeoAccount[];
}) {
	const t = draft.terminal;
	const setT = (patch: Partial<VirtualModelDraft["terminal"]>) => set({ terminal: { ...t, ...patch } });
	const fallback = t.model ? labelProvider(providerOfModel(t.model, models), providers) : undefined;
	return (
		<section className="panel ed-panel" aria-labelledby="ed-end">
			<h2 id="ed-end" className="ed-panel-title">
				When every stage is out of room
			</h2>
			<p className="muted ed-panel-desc">Budgets reset with their windows. Until then, choose what a request gets.</p>
			<div className="mend-choices" role="radiogroup" aria-labelledby="ed-end">
				{(Object.keys(TERMINAL_COPY) as TerminalKind[]).map((k) => (
					<button key={k} role="radio" aria-checked={t.kind === k} className={cx("mchoice mend-choice", t.kind === k && "is-on")} onClick={() => setT({ kind: k })}>
						<span className="mend-radio" aria-hidden="true" />
						<span>
							<strong>{TERMINAL_COPY[k].title}</strong>
							<span>{TERMINAL_COPY[k].explain}</span>
						</span>
					</button>
				))}
			</div>
			{t.kind === "passthrough" ? (
				<Field label="Fallback model" hint="Answers without any budget, so use one you can afford to run freely. Models shared from NeoPi need a budgeted stage and aren't offered.">
					<ModelPicker
						title="Choose the fallback model"
						excludeShared
						value={t.model}
						models={models}
						providers={providers}
						accounts={accounts}
						onPick={(m) => setT({ model: m.id })}
						trigger={
							<button className={cx("select-trigger mmodel-trigger", !t.model && "is-empty")}>
								{fallback && <ProviderMark provider={fallback.colorKey} name={fallback.name} size={20} />}
								<span className="grow mmodel-trigger-text">
									{t.model ? (
										<>
											{modelName(t.model, models)} <span className="faint">{fallback?.name}</span>
										</>
									) : (
										<span className="faint">Choose a model</span>
									)}
								</span>
								<Icon name="chevron" size={16} className="select-chevron" />
							</button>
						}
					/>
				</Field>
			) : (
				<Field label="Message to the caller" htmlFor="vm-msg" optional hint="Sent back with the error, so people know why and when to try again.">
					<textarea
						id="vm-msg"
						className="input"
						rows={2}
						maxLength={4096}
						value={t.message}
						placeholder={t.kind === "queue" ? "Busy right now; your request will continue when the weekly budget resets." : "This week's budget for this model is used up. It resets on Monday."}
						onChange={(e) => setT({ message: e.target.value })}
					/>
				</Field>
			)}
		</section>
	);
}
