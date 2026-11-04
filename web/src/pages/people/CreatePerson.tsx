import { useState } from "react";
import { navigate } from "../../app/router";
import { Button, Dialog, Field, Input } from "../../ui/components";
import { useToast } from "../../ui/toast";
import { useCreateUser, type Role } from "./api";
import { ROLE_COPY, ROLE_ORDER, generatePassword, passwordProblem } from "./copy";
import { PasswordInput, RolePicker } from "./widgets";

const HANDLE = /^[a-z][a-z0-9-]{1,31}$/;

export function CreatePersonDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
	const create = useCreateUser();
	const toast = useToast();
	const [name, setName] = useState("");
	const [handle, setHandle] = useState("");
	const [handleTouched, setHandleTouched] = useState(false);
	const [password, setPassword] = useState("");
	const [role, setRole] = useState<Role>("member");
	const [tried, setTried] = useState(false);
	const [error, setError] = useState<string>();
	const service = role === "service";

	const suggested = name
		.toLowerCase()
		.normalize("NFKD")
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^[^a-z]+|-+$/g, "")
		.slice(0, 32);
	const effectiveHandle = handleTouched ? handle : suggested;
	const handleError = !effectiveHandle ? "Pick a handle." : !HANDLE.test(effectiveHandle) ? "2 to 32 characters: lowercase letters, digits and dashes, starting with a letter." : undefined;
	const pwError = service ? undefined : !password ? "Set a password, or generate one." : passwordProblem(password);

	const reset = () => {
		setName("");
		setHandle("");
		setHandleTouched(false);
		setPassword("");
		setRole("member");
		setTried(false);
		setError(undefined);
	};
	const close = (v: boolean) => {
		onOpenChange(v);
		if (!v) setTimeout(reset, 250);
	};

	const submit = async () => {
		setTried(true);
		setError(undefined);
		if (handleError || pwError) return;
		try {
			const u = await create.mutateAsync({
				handle: effectiveHandle,
				name: name.trim() || effectiveHandle,
				role,
				state: "active",
				password: service ? undefined : password,
				providerAccess: [],
				grants: [],
				limits: { rpm: null, tokensPerDay: null },
			});
			toast(`Added ${u.name || u.handle}. Next, give them a model and a key.`);
			close(false);
			navigate(`people/${encodeURIComponent(u.id)}`);
		} catch (e) {
			const msg = (e as Error).message;
			setError(/exists|conflict/i.test(msg) ? `Someone already uses the handle “${effectiveHandle}”. Pick another.` : `NeoProxy didn't add them: ${msg}. Check the fields and try again.`);
		}
	};

	return (
		<Dialog
			open={open}
			onOpenChange={close}
			wide
			title="Add a person"
			description="Create an account for someone in your office, or for an app. Next you'll give them models to use and issue keys for their apps."
			footer={
				<>
					<Button variant="ghost" onClick={() => close(false)}>
						Cancel
					</Button>
					<Button variant="primary" busy={create.isPending} onClick={submit}>
						Add {service ? "service" : "person"}
					</Button>
				</>
			}
		>
			<form
				className="create-grid"
				onSubmit={(e) => {
					e.preventDefault();
					submit();
				}}
			>
				<div className="stack-lg">
					<Field label="Name" htmlFor="c-name" hint={service ? "What the app is, for example “CI runner”." : "How they appear across the dashboard."}>
						<Input id="c-name" autoFocus maxLength={200} value={name} onChange={(e) => setName(e.target.value)} placeholder={service ? "CI runner" : "Lena Park"} />
					</Field>
					<Field label="Handle" htmlFor="c-handle" hint={service ? "A short id, shown in Activity." : "What they type to sign in. Shown in Activity."} error={tried || handleTouched ? handleError : undefined}>
						<Input
							id="c-handle"
							mono
							value={effectiveHandle}
							placeholder="lena"
							onChange={(e) => {
								setHandleTouched(true);
								setHandle(e.target.value.toLowerCase());
							}}
						/>
					</Field>
					{service ? (
						<div className="pp-inline-note">Services don't sign in to this dashboard, so they don't need a password. You'll issue them an inference key next.</div>
					) : (
						<Field label="Password" htmlFor="c-pw" hint="12 to 72 characters. Share it with them privately; they can change it after signing in." error={tried ? pwError : undefined}>
							<PasswordInput id="c-pw" value={password} onChange={setPassword} generate={generatePassword} />
						</Field>
					)}
				</div>
				<div className="field">
					<span className="field-label" id="c-role-label">
						Role
					</span>
					<RolePicker label="Role" value={role} onChange={setRole} roles={ROLE_ORDER} />
					<p className="field-hint">{ROLE_COPY[role].summary} You can change this later.</p>
				</div>
				{error && (
					<p className="field-error create-error" role="alert">
						{error}
					</p>
				)}
				<button type="submit" hidden />
			</form>
		</Dialog>
	);
}
