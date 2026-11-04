import { useEffect, useRef, useState } from "react";
import { ApiError } from "../../api/client";
import { Button, Field, Notice, Select, cx } from "../../ui/components";
import { simulate, type Decision, type NeoAccount, type NeoModel, type NeoUser, type Protocol, type TrailStep, type VirtualModel } from "./data";
import { modelName, pct } from "./route";

const PROTOCOLS: Array<{ value: Protocol; label: string; description: string }> = [
	{ value: "openai", label: "OpenAI chat completions", description: "/v1/chat/completions" },
	{ value: "openai-response", label: "OpenAI responses", description: "/v1/responses" },
	{ value: "claude", label: "Anthropic messages", description: "/v1/messages" },
];

export const isScriptError = (e: unknown) => e instanceof ApiError && (e.code === "neoproxy_script" || e.code === "neoproxy_script_error" || /\b(script|Lua)\b/i.test(e.message));

/** Asks the router which stage and account would answer, without sending anything upstream. */
export function TryRequest({
	vm,
	dirty,
	users,
	models,
	accounts,
	onDecision,
	onEditScript,
}: {
	vm: VirtualModel | undefined;
	dirty: boolean;
	users: NeoUser[];
	models: NeoModel[];
	accounts: NeoAccount[];
	onDecision: (d: Decision | null) => void;
	onEditScript?: () => void;
}) {
	const granted = vm ? users.filter((u) => u.grants.some((g) => g.virtualModel === vm.id)) : [];
	const [picked, setUserId] = useState<string>();
	// Default to someone who actually has the grant, once people have loaded.
	const userId = picked ?? granted[0]?.id ?? users.find((u) => u.role !== "admin")?.id ?? users[0]?.id;
	const [protocol, setProtocol] = useState<Protocol>("openai");
	const [payload, setPayload] = useState("");
	const [busy, setBusy] = useState(false);
	const [result, setResult] = useState<Decision | null>(null);
	const [error, setError] = useState<{ message: string; script: boolean } | null>(null);
	const gen = useRef(0);

	// Any edit invalidates the last answer: it described the saved version.
	useEffect(() => {
		gen.current++;
		setResult(null);
		setError(null);
		onDecision(null);
	}, [dirty, vm?.updatedAt, onDecision]);

	const run = async () => {
		if (!vm) return;
		const g = ++gen.current;
		setBusy(true);
		setError(null);
		try {
			let request: unknown;
			if (payload.trim()) {
				try {
					request = JSON.parse(payload);
				} catch (e) {
					throw new Error(`The request JSON doesn't parse (${(e as Error).message}). Fix it or clear the box.`);
				}
				if (!request || typeof request !== "object" || Array.isArray(request)) throw new Error("The request JSON must be an object, like {\"messages\": [...]}.");
			}
			const d = await simulate({ asked: vm.served, userId, protocol, request });
			if (g !== gen.current) return;
			setResult(d);
			onDecision(d);
		} catch (e) {
			if (g !== gen.current) return;
			setResult(null);
			onDecision(null);
			setError({ message: (e as Error).message, script: isScriptError(e) });
		} finally {
			if (g === gen.current) setBusy(false);
		}
	};

	const who = users.find((u) => u.id === userId);
	return (
		<section className="panel mtry" aria-labelledby="try-title">
			<header>
				<h2 id="try-title" className="mside-title">
					Try a request
				</h2>
				<p className="muted mside-desc">See which stage and account would answer right now, using a person's real grant and today's usage. Nothing is sent to a provider and nothing is counted.</p>
			</header>
			{!vm ? (
				<p className="faint mtry-wait">Save this virtual model first, then try it here.</p>
			) : (
				<>
					<Field label="Ask as" htmlFor="try-user" hint={who && !who.grants.some((g) => g.virtualModel === vm.id) ? `${who.handle} isn't granted this model yet, so the answer will be a refusal.` : undefined}>
						<Select
							id="try-user"
							value={userId}
							onValueChange={setUserId}
							options={users.map((u) => ({
								value: u.id,
								label: u.handle,
								description: u.grants.some((g) => g.virtualModel === vm.id) ? "Granted" : u.role === "admin" ? "Administrator, not granted" : "Not granted",
							}))}
						/>
					</Field>
					<details className="mtry-more">
						<summary>Request format and body</summary>
						<div className="stack">
							<Field label="Format" htmlFor="try-proto">
								<Select id="try-proto" value={protocol} onValueChange={(v) => setProtocol(v as Protocol)} options={PROTOCOLS} />
							</Field>
							<Field label="Request body" optional htmlFor="try-body" hint="Only matters if a routing script reads the request. Use messages for chat and Anthropic, input for responses.">
								<textarea id="try-body" className="input mono" rows={4} spellCheck={false} value={payload} onChange={(e) => setPayload(e.target.value)} placeholder='{"messages": [{"role": "user", "content": "Hello"}]}' />
							</Field>
						</div>
					</details>
					{dirty && <p className="mtry-dirty">You have unsaved changes. Trying uses the saved version.</p>}
					<Button onClick={run} busy={busy} disabled={!userId}>
						Try it
					</Button>
					<div aria-live="polite">
						{error && (
							<Notice tone="bad" title={error.script ? "The routing script failed" : "Couldn't try it"} action={error.script && onEditScript ? <Button size="sm" onClick={onEditScript}>Edit script</Button> : undefined}>
								{error.message}
							</Notice>
						)}
						{result && <Outcome d={result} vm={vm} models={models} accounts={accounts} who={who?.handle ?? "this person"} />}
					</div>
				</>
			)}
		</section>
	);
}

function accountName(id: string | undefined, accounts: NeoAccount[]) {
	return accounts.find((a) => a.id === id)?.label ?? "an account in the pool";
}

function Outcome({ d, vm, models, accounts, who }: { d: Decision; vm: VirtualModel; models: NeoModel[]; accounts: NeoAccount[]; who: string }) {
	const headline = (() => {
		if (d.outcome === "ok") {
			if (d.stageIndex !== undefined) return { tone: "good", text: `Stage ${d.stageIndex + 1} would answer with ${modelName(d.model, models)}, from ${accountName(d.accountId, accounts)}.` };
			return { tone: "good", text: `The fallback would answer with ${modelName(d.model, models)}, from ${accountName(d.accountId, accounts)}.` };
		}
		if (d.outcome === "queued") return { tone: "warn", text: "The request would wait until a stage has room again." };
		if (d.outcome === "limited") return { tone: "warn", text: "Every stage is out of room, so the caller would get a limit message." };
		if (d.outcome === "denied" || d.reason === "not-granted" || d.trail?.some((t) => t.kind === "denied")) return { tone: "bad", text: `${who} isn't granted ${vm.served}. Grant it under People.` };
		if (d.reason === "unknown-model") return { tone: "bad", text: `${vm.served} isn't a model NeoProxy knows. Save it first, or check the name.` };
		return { tone: "bad", text: d.message || (d.reason ? `No stage could answer (${d.reason.replace(/-/g, " ")}).` : "No stage could answer.") };
	})();
	return (
		<div className={cx("mtry-result", `mtone-${headline.tone}`)}>
			<p className="mtry-headline">{headline.text}</p>
			{d.effort && <p className="faint">Reasoning effort set to {d.effort}.</p>}
			{d.trail?.length > 0 && (
				<ol className="mtry-trail">
					{d.trail.map((t, i) => (
						<TrailLine key={i} t={t} d={d} models={models} accounts={accounts} />
					))}
				</ol>
			)}
		</div>
	);
}

function TrailLine({ t, d, models, accounts }: { t: TrailStep; d: Decision; models: NeoModel[]; accounts: NeoAccount[] }) {
	let label = "";
	let why = "";
	let state: "served" | "skipped" | "end" = "skipped";
	if (t.kind === "stage") {
		label = `Stage ${(t.index ?? 0) + 1}, ${modelName(t.model, models)}`;
		const used = t.total !== undefined && t.cap !== undefined ? ` (${pct(t.total)} of ${pct(t.cap)} used)` : "";
		if (t.skipped === "upstream-error") why = `The provider failed${t.failureStatus ? ` with status ${t.failureStatus}` : ""}, so the next stage was tried.`;
		else if (t.skipped === "cap") why = `Skipped: the stage used its whole budget${used}.`;
		else if (t.skipped === "per-account") why = "Skipped: every account already gave its share.";
		else if (t.skipped === "unhealthy") why = "Skipped: no healthy account is available for it.";
		else if (t.skipped === "script") why = "Skipped: the routing script picked another stage.";
		else if (t.skipped === "lua-cap") why = `Skipped: the script's budget for this stage is used up${used}.`;
		else if (t.skipped === "lua-policy")
			why = t.policy?.accounts?.length && t.policy.accounts.every((a) => !a.eligible) ? "Skipped: the script's condition ruled out every account." : "Skipped: the script's share is used up for every account it allows.";
		else if (t.skipped) why = `Skipped: ${t.skipped}.`;
		else if (t.chosen) {
			state = d.outcome === "ok" ? "served" : "skipped";
			why = `Picked ${accountName(t.chosen, accounts)}${used}.`;
		} else why = "No account was picked.";
	} else if (t.kind === "terminal") {
		state = t.terminal?.kind === "passthrough" && d.outcome === "ok" ? "served" : "end";
		label = t.terminal?.kind === "passthrough" ? "Fallback" : t.terminal?.kind === "queue" ? "Wait for room" : "Limit message";
		why = t.terminal?.kind === "passthrough" ? (d.outcome === "ok" ? `${modelName(d.model, models)} from ${accountName(d.accountId, accounts)}, without a budget.` : d.message ?? d.reason ?? "") : t.terminal?.message || "No stage could answer.";
	} else if (t.kind === "direct") {
		state = d.outcome === "ok" ? "served" : "end";
		label = modelName(t.model, models);
		why = `Direct provider access, ${accountName(t.accountId, accounts)}.`;
	} else if (t.kind === "denied") {
		state = "end";
		label = "Refused";
		why = t.virtualModel ? "This person isn't granted this virtual model." : `This person can't call ${t.providerId} directly.`;
	} else {
		state = "end";
		label = "Unknown model";
		why = t.asked ?? "";
	}
	return (
		<li className={cx("mtry-step", `is-${state}`)}>
			<span className="mtry-step-dot" aria-hidden="true" />
			<span>
				<strong>{label}</strong>
				<span className="muted"> {why}</span>
				{t.policy && (
					<details className="mtry-policy">
						<summary>Script results</summary>
						{t.policy.cap !== undefined && <p className="faint">Stage budget from the script: {pct(t.policy.cap)} before grant scaling.</p>}
						{t.policy.accounts.map((a) => (
							<p key={a.accountId} className="faint">
								{accountName(a.accountId, accounts)}: condition {a.eligible ? "passed" : "failed"}
								{a.limit !== undefined ? `, share ${pct(a.limit)}` : ""}.
							</p>
						))}
					</details>
				)}
			</span>
		</li>
	);
}
