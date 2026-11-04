import { useState } from "react";
import type { Session } from "../../api/types";
import { ago, plural } from "../../lib/format";
import { Avatar, Button, Field, Notice, PageHeader, Skeleton, StatusDot } from "../../ui/components";
import { useToast } from "../../ui/toast";
import { RecentRequests } from "../activity/Recent";
import { useSetPassword, useUser, useVault, useVirtualModels } from "./api";
import { ROLE_COPY, STATE_COPY, displayName, fmtNumberLimit, generatePassword, passwordProblem } from "./copy";
import { KeysPanel } from "./Keys";
import { PasswordInput, RoleBadge } from "./widgets";

/** People page for non-administrators: their own account. */
export function SelfView({ session }: { session: Session }) {
	const me = useUser(session.userId);
	const vault = useVault();
	const vms = useVirtualModels();
	const u = me.data;

	return (
		<>
			<PageHeader title="Your account" lede="Your profile, the keys your apps use to reach NeoProxy, and your dashboard password." />
			{me.isError && (
				<Notice tone="bad" title="Couldn't load your account">
					{(me.error as Error).message}. NeoProxy may be restarting; this page retries on its own.
				</Notice>
			)}
			{!u ? (
				<Skeleton h={260} r={14} />
			) : (
				<div className="pp-detail">
					<div className="pp-main">
						<KeysPanel
							user={u}
							self
							accounts={(vault.data?.accounts ?? []).filter((a) => u.role !== "owner" || a.ownerId === u.id)}
							exampleModel={vms.data?.find((v) => v.id === u.grants?.[0]?.virtualModel)?.served}
							canUseCount={u.grants?.length ?? 0}
						/>
						<section className="panel pp-panel">
							<header className="pp-panel-head">
								<div className="grow">
									<h2 className="pp-h2">Your recent requests</h2>
									<p className="pp-desc">What your keys asked for lately, and how each was answered.</p>
								</div>
								<Button variant="ghost" onClick={() => (location.hash = "#/activity")}>
									All your activity
								</Button>
							</header>
							<RecentRequests session={session} userId={u.id} emptyText="Nothing yet. Requests show up here as soon as an app uses one of your keys." />
						</section>
					</div>
					<aside className="pp-aside">
						<section className="panel pp-panel">
							<div className="self-id">
								<Avatar name={u.name || u.handle} size={48} />
								<div className="grow">
									<strong className="self-name">{displayName(u)}</strong>
									<code className="faint">{u.handle}</code>
								</div>
							</div>
							<dl className="pp-facts">
								<div>
									<dt>Role</dt>
									<dd>
										<RoleBadge role={u.role} />
										<p className="faint">{ROLE_COPY[u.role]?.can}</p>
									</dd>
								</div>
								<div>
									<dt>Status</dt>
									<dd className="row">
										<StatusDot tone={STATE_COPY[u.state].tone} />
										{STATE_COPY[u.state].name}
									</dd>
								</div>
								<div>
									<dt>Models you can use</dt>
									<dd>
										{u.grants?.length ? (
											<>
												{plural(u.grants.length, "model")}. <a href="#/models">See them on Models</a>
											</>
										) : (
											"None yet. An administrator gives access to models."
										)}
									</dd>
								</div>
								{(u.limits?.rpm || u.limits?.tokensPerDay) && (
									<div>
										<dt>Your limits</dt>
										<dd>
											{u.limits.rpm ? fmtNumberLimit(u.limits.rpm, "requests a minute") : "Any number of requests a minute"}, {u.limits.tokensPerDay ? fmtNumberLimit(u.limits.tokensPerDay, "tokens a day") : "no daily token limit"}
										</dd>
									</div>
								)}
								<div>
									<dt>Joined</dt>
									<dd>{ago(u.createdAt)}</dd>
								</div>
							</dl>
							<p className="faint pp-small">To change your name or role, ask an administrator.</p>
						</section>
						{u.role !== "service" && <ChangePassword userId={u.id} keyCount={u.keys?.length ?? 0} />}
					</aside>
				</div>
			)}
		</>
	);
}

function ChangePassword({ userId, keyCount }: { userId: string; keyCount: number }) {
	const set = useSetPassword();
	const toast = useToast();
	const [open, setOpen] = useState(false);
	const [current, setCurrent] = useState("");
	const [next, setNext] = useState("");
	const [error, setError] = useState<string>();
	const problem = next ? passwordProblem(next) : undefined;
	return (
		<section className="panel pp-panel">
			<div>
				<h2 className="pp-h2">Dashboard password</h2>
				<p className="pp-desc">Only for signing in here. It's separate from your inference keys and from any provider account.</p>
			</div>
			{!open ? (
				<div>
					<Button onClick={() => setOpen(true)}>Change password</Button>
				</div>
			) : (
				<form
					className="stack"
					onSubmit={(e) => {
						e.preventDefault();
						setError(undefined);
						if (!current || !next || problem) return setError(problem ?? "Fill in both passwords.");
						set
							.mutateAsync({ userId, currentPassword: current, password: next })
							.then(() => toast("Password changed. Sign in again with the new one.", "info"))
							.catch((err) => setError(/credential|401|unauth/i.test((err as Error).message) ? "Your current password isn't right. Try again." : `Couldn't change it: ${(err as Error).message}`));
					}}
				>
					<Field label="Current password" htmlFor="me-cur">
						<PasswordInput id="me-cur" value={current} onChange={setCurrent} autoComplete="current-password" />
					</Field>
					<Field label="New password" htmlFor="me-new" hint="12 to 72 characters." error={problem}>
						<PasswordInput id="me-new" value={next} onChange={setNext} generate={generatePassword} />
					</Field>
					<Notice tone="warn" title="This signs you out everywhere">
						{keyCount > 0
							? `It also revokes your ${keyCount === 1 ? "inference key" : `${keyCount} inference keys`}, so apps using ${keyCount === 1 ? "it" : "them"} stop until you issue new ones.`
							: "Sign in again with the new password afterwards."}
					</Notice>
					{error && (
						<p className="field-error" role="alert">
							{error}
						</p>
					)}
					<div className="pp-actions">
						<Button type="button" variant="ghost" onClick={() => setOpen(false)}>
							Cancel
						</Button>
						<Button type="submit" variant="primary" busy={set.isPending}>
							Change password
						</Button>
					</div>
				</form>
			)}
		</section>
	);
}
