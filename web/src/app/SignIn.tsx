import { useState, type FormEvent } from "react";
import { useBootstrap, useSignIn } from "../api/queries";
import { Button, Field, Input, Notice } from "../ui/components";
import { providerColor } from "../lib/providers";

// A still composition of the pool mosaic: the product's signature, shown before
// there is any data to draw it with.
const ART: Array<[string, number, number, number, number]> = [
	["codex", 0, 0, 7, 6],
	["claude", 7, 0, 5, 4],
	["antigravity", 7, 4, 3, 2],
	["xai", 10, 4, 2, 2],
	["codex", 0, 6, 4, 3],
	["openrouter", 4, 6, 3, 3],
	["deepseek", 7, 6, 2, 3],
	["kimi", 9, 6, 3, 3],
];

export function SignInScreen() {
	const [mode, setMode] = useState<"signin" | "setup">("signin");
	const [handle, setHandle] = useState("");
	const [password, setPassword] = useState("");
	const [secret, setSecret] = useState("");
	const signIn = useSignIn();
	const bootstrap = useBootstrap();
	const busy = signIn.isPending || bootstrap.isPending;
	const error = (mode === "signin" ? signIn.error : bootstrap.error) as Error | null;

	function submit(e: FormEvent) {
		e.preventDefault();
		if (mode === "signin") signIn.mutate({ handle: handle.trim(), password });
		else bootstrap.mutate({ handle: handle.trim(), password, secret });
	}

	return (
		<div className="gate">
			<div className="gate-art" aria-hidden="true">
				{ART.map(([p, x, y, w, h], i) => (
					<span key={i} className="gate-tile" style={{ gridColumn: `${x + 1} / span ${w}`, gridRow: `${y + 1} / span ${h}`, background: providerColor(p), animationDelay: `${i * 70}ms` }} />
				))}
			</div>
			<form className="gate-card" onSubmit={submit}>
				<div className="gate-brand">
					<img src="/neo/mark.svg" alt="" width={26} height={26} />
					<span>NeoProxy</span>
				</div>
				<h1 className="gate-title">{mode === "signin" ? "Sign in" : "Set up this server"}</h1>
				<p className="gate-lede">
					{mode === "signin"
						? "Use your NeoProxy username and password. Apps connect with inference keys, not with this login."
						: "Create the first administrator. This works once: after an administrator exists, setup closes."}
				</p>
				<Field label="Username" htmlFor="handle">
					<Input id="handle" autoComplete="username" value={handle} onChange={(e) => setHandle(e.target.value)} required autoFocus />
				</Field>
				<Field label="Password" htmlFor="password" hint={mode === "setup" ? "12 to 72 characters." : undefined}>
					<Input id="password" type="password" autoComplete={mode === "setup" ? "new-password" : "current-password"} value={password} onChange={(e) => setPassword(e.target.value)} required />
				</Field>
				{mode === "setup" && (
					<Field
						label="Operator secret"
						htmlFor="secret"
						hint={
							<>
								The <code>management.secret-key</code> generated in this server's config before first start. Used once to authorize setup; never stored by the dashboard.
							</>
						}
					>
						<Input id="secret" type="password" autoComplete="off" value={secret} onChange={(e) => setSecret(e.target.value)} required />
					</Field>
				)}
				{error && (
					<Notice tone="bad" title={mode === "signin" ? "Couldn't sign in" : "Setup failed"}>
						{error.message === "invalid credentials" ? "That username and password don't match an account on this server." : error.message}
					</Notice>
				)}
				<Button type="submit" variant="primary" size="lg" busy={busy} className="gate-submit">
					{mode === "signin" ? "Sign in" : "Create administrator"}
				</Button>
				<button type="button" className="link-btn" onClick={() => setMode(mode === "signin" ? "setup" : "signin")}>
					{mode === "signin" ? "Setting up a new server?" : "Back to sign in"}
				</button>
			</form>
		</div>
	);
}
