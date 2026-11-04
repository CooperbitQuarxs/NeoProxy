import * as RDropdown from "@radix-ui/react-dropdown-menu";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type ReactNode } from "react";
import { onUnauthorized } from "../api/client";
import { useCore, useSession, useSignOut } from "../api/queries";
import type { Session } from "../api/types";
import { Avatar, cx } from "../ui/components";
import { Icon, type IconName } from "../ui/icons";
import { useToast } from "../ui/toast";
import { LEGACY_REDIRECTS, navigate, useRoute } from "./router";
import { SignInScreen } from "./SignIn";
import { PoolPage } from "../pool/PoolPage";
import { ModelsPage } from "../pages/models/ModelsPage";
import { PeoplePage } from "../pages/people/PeoplePage";
import { ActivityPage } from "../pages/activity/ActivityPage";
import { SystemPage } from "../pages/system/SystemPage";

export type PageProps = { session: Session; params: string[]; query: URLSearchParams };
type PageDef = { id: string; title: string; memberTitle?: string; icon: IconName; admin?: boolean; render: (p: PageProps) => ReactNode };

const PAGES: PageDef[] = [
	{ id: "pool", title: "Pool", icon: "pool", render: (p) => <PoolPage {...p} /> },
	{ id: "models", title: "Models", icon: "models", render: (p) => <ModelsPage {...p} /> },
	{ id: "people", title: "People", memberTitle: "Your account", icon: "people", render: (p) => <PeoplePage {...p} /> },
	{ id: "activity", title: "Activity", icon: "activity", render: (p) => <ActivityPage {...p} /> },
	{ id: "system", title: "System", icon: "system", admin: true, render: (p) => <SystemPage {...p} /> },
];

export function App() {
	const session = useSession();
	const qc = useQueryClient();
	const toast = useToast();
	useEffect(
		() =>
			onUnauthorized(() => {
				if (qc.getQueryData(["session"])) toast("Your session ended. Sign in again to continue.", "info");
				qc.setQueryData(["session"], null);
			}),
		[qc, toast],
	);
	if (session.isPending) return <div className="boot" aria-busy="true" />;
	if (!session.data) return <SignInScreen />;
	return <Shell session={session.data} />;
}

type ThemeMode = "auto" | "light" | "dark";
function useTheme(): [ThemeMode, (m: ThemeMode) => void] {
	const [mode, setMode] = useState<ThemeMode>(() => (localStorage.getItem("neo-theme") as ThemeMode) || "auto");
	useEffect(() => {
		if (mode === "auto") delete document.documentElement.dataset.theme;
		else document.documentElement.dataset.theme = mode;
		localStorage.setItem("neo-theme", mode);
	}, [mode]);
	return [mode, setMode];
}

function Shell({ session }: { session: Session }) {
	const route = useRoute();
	const admin = session.role === "admin";
	const redirect = LEGACY_REDIRECTS[route.page];
	useEffect(() => {
		if (redirect) navigate(redirect);
	}, [redirect]);
	const visible = PAGES.filter((p) => admin || !p.admin).map((p) => (!admin && p.memberTitle ? { ...p, title: p.memberTitle } : p));
	const page = visible.find((p) => p.id === route.page) ?? visible[0];
	useEffect(() => {
		document.title = `${page.title} · NeoProxy`;
	}, [page.title]);
	return (
		<div className="shell">
			<Sidebar pages={visible} current={page.id} session={session} />
			<main className="content" id="main">
				{page.render({ session, params: route.params, query: route.query })}
			</main>
		</div>
	);
}

function Sidebar({ pages, current, session }: { pages: PageDef[]; current: string; session: Session }) {
	const core = useCore();
	const signOut = useSignOut();
	const [theme, setTheme] = useTheme();
	const online = core.isSuccess;
	return (
		<nav className="sidebar" aria-label="Main">
			<a className="brand" href="#/">
				<img src="/neo/mark.svg" alt="" width={22} height={22} />
				<span className="brand-word">NeoProxy</span>
			</a>
			<ul className="nav">
				{pages.map((p) => (
					<li key={p.id}>
						<a href={`#/${p.id}`} className={cx("nav-item", p.id === current && "is-current")} aria-current={p.id === current ? "page" : undefined}>
							<Icon name={p.icon} size={18} />
							<span>{p.title}</span>
						</a>
					</li>
				))}
			</ul>
			<div className="sidebar-foot">
				<div className="server" title={core.data?.endpoint}>
					<span className={cx("dot", online ? "dot-good" : core.isError ? "dot-bad" : "dot-idle")} aria-hidden="true" />
					<span className="server-text">
						<span className="server-host">{core.data?.host ?? "Connecting…"}</span>
						<span className="server-version mono">{core.data?.version?.replace(/^(\d+\.\d+\.\d+)-nightly\.(\d{8}).*$/, "$1 nightly $2") ?? ""}</span>
					</span>
				</div>
				<RDropdown.Root>
					<RDropdown.Trigger className="me">
						<Avatar name={session.handle} size={28} />
						<span className="me-text">
							<span className="me-name">{session.handle}</span>
							<span className="me-role">{session.role === "admin" ? "Administrator" : session.role === "owner" ? "Contributor" : "Member"}</span>
						</span>
						<Icon name="more" size={16} />
					</RDropdown.Trigger>
					<RDropdown.Portal>
						<RDropdown.Content className="menu" side="top" align="start" sideOffset={8}>
							<RDropdown.Label className="menu-label">Appearance</RDropdown.Label>
							<RDropdown.RadioGroup value={theme} onValueChange={(v) => setTheme(v as ThemeMode)}>
								{(
									[
										["auto", "Match system", "auto"],
										["dark", "Dark", "moon"],
										["light", "Light", "sun"],
									] as const
								).map(([v, label, icon]) => (
									<RDropdown.RadioItem key={v} value={v} className="menu-item">
										<Icon name={icon} size={16} />
										<span className="grow">{label}</span>
										<RDropdown.ItemIndicator>
											<Icon name="check" size={15} />
										</RDropdown.ItemIndicator>
									</RDropdown.RadioItem>
								))}
							</RDropdown.RadioGroup>
							<RDropdown.Separator className="menu-sep" />
							<RDropdown.Item className="menu-item" onSelect={() => signOut.mutate()}>
								<Icon name="signout" size={16} />
								<span>Sign out</span>
							</RDropdown.Item>
						</RDropdown.Content>
					</RDropdown.Portal>
				</RDropdown.Root>
			</div>
		</nav>
	);
}
