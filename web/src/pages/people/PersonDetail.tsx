import { useEffect, useState } from "react";
import { navigate } from "../../app/router";
import type { Session } from "../../api/types";
import { ago } from "../../lib/format";
import { Avatar, Button, EmptyState, Field, Input, Notice, Select, Skeleton, StatusDot } from "../../ui/components";
import { Icon } from "../../ui/icons";
import { useToast } from "../../ui/toast";
import { RecentRequests } from "../activity/Recent";
import { useDeleteUser, usePoolModels, useSetPassword, useUpdateUser, useUsage, useUser, useVault, useVirtualModels, type Role, type User } from "./api";
import { ROLE_COPY, ROLE_ORDER, STATE_COPY, displayName, fmtNumberLimit, generatePassword, passwordProblem } from "./copy";
import { GrantsPanel } from "./Grants";
import { KeysPanel } from "./Keys";
import { Confirm, PasswordInput, RoleBadge } from "./widgets";

export function PersonDetail({ id, session }: { id: string; session: Session }) {
	const user = useUser(id);
	const vms = useVirtualModels();
	const models = usePoolModels();
	const vault = useVault();
	const usage = useUsage();

	if (user.isError)
		return (
			<>
				<BackLink />
				<EmptyState title="This person isn't here" action={<Button onClick={() => navigate("people")}>Back to people</Button>}>
					They may have been deleted, or the link is wrong. {(user.error as Error).message}.
				</EmptyState>
			</>
		);
	if (!user.data)
		return (
			<>
				<BackLink />
				<div className="pp-hero">
					<Skeleton w={64} h={64} r={32} />
					<div className="stack">
						<Skeleton w={240} h={32} />
						<Skeleton w={160} h={16} />
					</div>
				</div>
				<Skeleton h={220} r={14} />
			</>
		);

	const u = user.data;
	const self = u.id === session.userId;
	const accounts = (vault.data?.accounts ?? []).filter((a) => u.role !== "owner" || a.ownerId === u.id);
	const firstGrant = vms.data?.find((v) => v.id === u.grants?.[0]?.virtualModel);

	return (
		<>
			<BackLink />
			<header className="pp-hero">
				<Avatar name={u.name || u.handle} size={64} />
				<div className="grow">
					<h1 className="page-title pp-hero-name">{displayName(u)}</h1>
					<div className="pp-hero-meta">
						<code>{u.handle}</code>
						<RoleBadge role={u.role} />
						<span className="row">
							<StatusDot tone={STATE_COPY[u.state].tone} />
							{STATE_COPY[u.state].name}
						</span>
						<span className="faint">Joined {ago(u.createdAt)}</span>
					</div>
				</div>
			</header>

			{u.state === "suspended" && (
				<Notice tone="bad" title={`${displayName(u)} is suspended`}>
					{STATE_COPY.suspended.explain} Reinstate them under “Access” to restore everything as it was.
				</Notice>
			)}

			<div className="pp-detail">
				<div className="pp-main">
					<GrantsPanel user={u} vms={vms.data ?? []} models={models.data ?? []} usage={usage.data} vault={vault.data} />
					<KeysPanel user={u} accounts={accounts} exampleModel={firstGrant?.served} self={self} canUseCount={u.grants?.length ?? 0} />
					<section className="panel pp-panel">
						<header className="pp-panel-head">
							<div className="grow">
								<h2 className="pp-h2">Recent requests</h2>
								<p className="pp-desc">The last few things {displayName(u)}'s keys asked for, and how each was answered.</p>
							</div>
							<Button variant="ghost" onClick={() => navigate(`activity?user=${encodeURIComponent(u.id)}`)}>
								All of their activity
							</Button>
						</header>
						<RecentRequests session={session} userId={u.id} emptyText={u.keys?.length ? "No requests yet. They show up here as soon as one of their keys is used." : "No requests yet. Issue a key above and they show up here once an app uses it."} />
					</section>
				</div>
				<aside className="pp-aside">
					<ProfilePanel user={u} self={self} />
					<LimitsPanel user={u} />
					<PasswordPanel user={u} self={self} />
					{!self && <AccessPanel user={u} owned={(vault.data?.accounts ?? []).filter((a) => a.ownerId === u.id).length} />}
				</aside>
			</div>
		</>
	);
}

function BackLink() {
	return (
		<a className="back-crumb" href="#/people">
			<Icon name="back" size={15} />
			People
		</a>
	);
}

function ProfilePanel({ user, self }: { user: User; self: boolean }) {
	const update = useUpdateUser();
	const toast = useToast();
	const [name, setName] = useState(user.name);
	const [handle, setHandle] = useState(user.handle);
	const [role, setRole] = useState<Role>(user.role);
	useEffect(() => {
		setName(user.name);
		setHandle(user.handle);
		setRole(user.role);
	}, [user.name, user.handle, user.role]);
	const dirty = name !== user.name || handle !== user.handle || role !== user.role;
	const handleBad = !/^[a-z][a-z0-9-]{1,31}$/.test(handle);
	const roleChanged = role !== user.role;
	return (
		<section className="panel pp-panel">
			<h2 className="pp-h2">Profile</h2>
			<Field label="Name" htmlFor="p-name">
				<Input id="p-name" value={name} maxLength={200} onChange={(e) => setName(e.target.value)} />
			</Field>
			<Field label="Handle" htmlFor="p-handle" hint="What they sign in with. Shown in Activity." error={handleBad ? "2 to 32 characters: lowercase letters, digits and dashes, starting with a letter." : undefined}>
				<Input id="p-handle" mono value={handle} onChange={(e) => setHandle(e.target.value.toLowerCase())} />
			</Field>
			<Field label="Role" htmlFor="p-role" hint={ROLE_COPY[role].can}>
				<Select id="p-role" value={role} onValueChange={(v) => setRole(v as Role)} options={ROLE_ORDER.map((r) => ({ value: r, label: ROLE_COPY[r].name, description: ROLE_COPY[r].summary }))} />
			</Field>
			{roleChanged && (
				<p className="pp-inline-note">
					{self ? "Changing your own role signs you out of this dashboard." : `Changing the role signs ${displayName(user)} out of this dashboard. Their keys keep working.`}
					{role === "owner" && " Contributor keys must be limited to their own accounts, so existing keys may stop finding an account."}
				</p>
			)}
			{dirty && (
				<div className="pp-actions">
					<Button
						variant="ghost"
						onClick={() => {
							setName(user.name);
							setHandle(user.handle);
							setRole(user.role);
						}}
					>
						Undo
					</Button>
					<Button
						variant="primary"
						busy={update.isPending}
						disabled={handleBad}
						onClick={() =>
							update
								.mutateAsync({ user, change: { name: name.trim(), handle, role } })
								.then(() => toast("Profile saved"))
								.catch((e) => toast(`Couldn't save the profile: ${(e as Error).message}. ${/exists|conflict/i.test((e as Error).message) ? "That handle is taken; pick another." : "Check the fields and try again."}`, "bad"))
						}
					>
						Save profile
					</Button>
				</div>
			)}
		</section>
	);
}

function parseLimit(s: string): number | null | "bad" {
	const t = s.replace(/[,_\s]/g, "");
	if (!t) return null;
	const n = Number(t);
	return Number.isInteger(n) && n > 0 ? n : "bad";
}

function LimitsPanel({ user }: { user: User }) {
	const update = useUpdateUser();
	const toast = useToast();
	const [rpm, setRpm] = useState(user.limits?.rpm ? String(user.limits.rpm) : "");
	const [tpd, setTpd] = useState(user.limits?.tokensPerDay ? String(user.limits.tokensPerDay) : "");
	useEffect(() => {
		setRpm(user.limits?.rpm ? String(user.limits.rpm) : "");
		setTpd(user.limits?.tokensPerDay ? String(user.limits.tokensPerDay) : "");
	}, [user.limits?.rpm, user.limits?.tokensPerDay]);
	const r = parseLimit(rpm);
	const t = parseLimit(tpd);
	const dirty = r !== (user.limits?.rpm ?? null) || t !== (user.limits?.tokensPerDay ?? null);
	return (
		<section className="panel pp-panel">
			<div>
				<h2 className="pp-h2">Limits</h2>
				<p className="pp-desc">Hard ceilings for this person across all their keys and models. Leave a box empty for no limit. Requests over a limit are refused with a clear message, and show as “Limited” in Activity.</p>
			</div>
			<Field label="Requests per minute" htmlFor="l-rpm" hint="Stops runaway scripts. A person chatting rarely sends more than 10." error={r === "bad" ? "Use a whole number above zero, or leave it empty." : undefined}>
				<Input id="l-rpm" inputMode="numeric" placeholder="No limit" value={rpm} onChange={(e) => setRpm(e.target.value)} />
			</Field>
			<Field label="Tokens per day" htmlFor="l-tpd" hint={typeof t === "number" ? `${fmtNumberLimit(t, "tokens a day")}, input and output together.` : "Input and output together. A busy coding day is often 2 to 20 million."} error={t === "bad" ? "Use a whole number above zero, or leave it empty." : undefined}>
				<Input id="l-tpd" inputMode="numeric" placeholder="No limit" value={tpd} onChange={(e) => setTpd(e.target.value)} />
			</Field>
			{dirty && (
				<div className="pp-actions">
					<Button
						variant="primary"
						busy={update.isPending}
						disabled={r === "bad" || t === "bad"}
						onClick={() =>
							update
								.mutateAsync({ user, change: { limits: { rpm: r as number | null, tokensPerDay: t as number | null } } })
								.then(() => toast("Limits saved"))
								.catch((e) => toast(`Couldn't save the limits: ${(e as Error).message}`, "bad"))
						}
					>
						Save limits
					</Button>
				</div>
			)}
		</section>
	);
}

function PasswordPanel({ user, self }: { user: User; self: boolean }) {
	const setPassword = useSetPassword();
	const toast = useToast();
	const [open, setOpen] = useState(false);
	const [pw, setPw] = useState("");
	const [confirming, setConfirming] = useState(false);
	const problem = pw ? passwordProblem(pw) : undefined;
	const keyCount = user.keys?.length ?? 0;
	if (user.role === "service")
		return (
			<section className="panel pp-panel">
				<h2 className="pp-h2">Password</h2>
				<p className="pp-desc">Services don't sign in to the dashboard, so they have no password. They use inference keys only.</p>
			</section>
		);
	return (
		<section className="panel pp-panel">
			<div>
				<h2 className="pp-h2">Password</h2>
				<p className="pp-desc">{self ? "Your dashboard password." : `Set a new dashboard password for ${displayName(user)} if they forgot theirs. Send it to them privately.`}</p>
			</div>
			{!open ? (
				<div>
					<Button onClick={() => setOpen(true)}>{self ? "Change password" : "Reset password"}</Button>
				</div>
			) : (
				<>
					<Field label="New password" htmlFor="pw-new" error={problem} hint="12 to 72 characters.">
						<PasswordInput id="pw-new" value={pw} onChange={setPw} generate={generatePassword} />
					</Field>
					<div className="pp-actions">
						<Button
							variant="ghost"
							onClick={() => {
								setOpen(false);
								setPw("");
							}}
						>
							Cancel
						</Button>
						<Button variant="primary" disabled={!pw || !!problem} onClick={() => setConfirming(true)}>
							Set password
						</Button>
					</div>
				</>
			)}
			<Confirm
				open={confirming}
				onOpenChange={setConfirming}
				title={self ? "Change your password?" : `Reset ${displayName(user)}'s password?`}
				confirmLabel={keyCount ? `Set password and revoke ${keyCount === 1 ? "1 key" : `${keyCount} keys`}` : "Set password"}
				busy={setPassword.isPending}
				onConfirm={() =>
					setPassword
						.mutateAsync({ userId: user.id, password: pw })
						.then(() => {
							toast(self ? "Password changed. Sign in again with the new one." : `Password reset. ${keyCount ? "Their keys were revoked; issue new ones." : ""}`);
							setConfirming(false);
							setOpen(false);
							setPw("");
						})
						.catch((e) => toast(`Couldn't set the password: ${(e as Error).message}`, "bad"))
				}
			>
				<p>{self ? "You'll be signed out everywhere, including this tab." : `${displayName(user)} is signed out everywhere and signs in with the new password next time.`}</p>
				{keyCount > 0 && (
					<p>
						A new password also revokes {self ? "your" : "their"} {keyCount === 1 ? "inference key" : `${keyCount} inference keys`}, so apps using {keyCount === 1 ? "it" : "them"} stop working until you issue new keys.
					</p>
				)}
			</Confirm>
		</section>
	);
}

function AccessPanel({ user, owned }: { user: User; owned: number }) {
	const update = useUpdateUser();
	const remove = useDeleteUser();
	const toast = useToast();
	const [ask, setAsk] = useState<"suspend" | "delete">();
	const name = displayName(user);
	const suspended = user.state === "suspended";
	return (
		<section className="panel pp-panel">
			<h2 className="pp-h2">Access</h2>
			<div className="pp-danger">
				<div className="grow">
					<strong>{suspended ? "Reinstate" : "Suspend"}</strong>
					<p className="faint">{suspended ? "Their sign-in and keys work again, exactly as before." : "Blocks sign-in and every key right away. Nothing is deleted; you can reinstate later."}</p>
				</div>
				{suspended ? (
					<Button
						busy={update.isPending}
						onClick={() =>
							update
								.mutateAsync({ user, change: { state: "active" } })
								.then(() => toast(`${name} is active again`))
								.catch((e) => toast(`Couldn't reinstate: ${(e as Error).message}`, "bad"))
						}
					>
						Reinstate
					</Button>
				) : (
					<Button variant="danger" onClick={() => setAsk("suspend")}>
						Suspend
					</Button>
				)}
			</div>
			<div className="pp-danger">
				<div className="grow">
					<strong>Delete</strong>
					<p className="faint">
						{owned
							? `They still contribute ${owned === 1 ? "an account" : `${owned} accounts`} to the pool. Give ${owned === 1 ? "it" : "them"} another contributor or remove ${owned === 1 ? "it" : "them"} on the Pool page, then delete.`
							: `Removes ${name} and their keys for good. Their past requests stay in Activity.`}
					</p>
				</div>
				<Button variant="danger" disabled={owned > 0} onClick={() => setAsk("delete")}>
					Delete
				</Button>
			</div>
			<Confirm
				open={ask === "suspend"}
				onOpenChange={(v) => !v && setAsk(undefined)}
				title={`Suspend ${name}?`}
				confirmLabel="Suspend"
				busy={update.isPending}
				onConfirm={() =>
					update
						.mutateAsync({ user, change: { state: "suspended" } })
						.then(() => {
							toast(`${name} is suspended`);
							setAsk(undefined);
						})
						.catch((e) => toast(`Couldn't suspend: ${(e as Error).message}`, "bad"))
				}
			>
				<p>
					{name} is signed out, and every request with their {user.keys?.length ? `${user.keys.length === 1 ? "key" : `${user.keys.length} keys`}` : "keys"} is refused from now on. Their keys, models and limits are kept, so reinstating restores everything.
				</p>
			</Confirm>
			<Confirm
				open={ask === "delete"}
				onOpenChange={(v) => !v && setAsk(undefined)}
				title={`Delete ${name}?`}
				confirmLabel={`Delete ${name}`}
				busy={remove.isPending}
				onConfirm={() =>
					remove
						.mutateAsync(user)
						.then(() => {
							toast(`Deleted ${name}`);
							navigate("people");
						})
						.catch((e) => toast(`Couldn't delete ${name}: ${(e as Error).message}. If they still contribute provider access, reassign it on the Pool page first.`, "bad"))
				}
			>
				<p>This can't be undone. {name} can no longer sign in, and apps using their keys stop immediately.</p>
				<p>Their models, limits and keys are removed with them. Their past requests remain in Activity.</p>
				<p className="faint">To pause them instead, cancel and suspend.</p>
			</Confirm>
		</section>
	);
}
