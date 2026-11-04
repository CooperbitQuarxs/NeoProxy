import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { useEffect } from "react";
import type { PageProps } from "../../app/App";
import { navigate } from "../../app/router";
import { PageHeader, cx } from "../../ui/components";
import { About } from "./About";
import { Configuration } from "./Configuration";
import { Logs } from "./Logs";
import { Overview } from "./Overview";
import { Plugins } from "./Plugins";
import { Routing } from "./Routing";
import { ServerKeys } from "./ServerKeys";
import { SECTIONS, type SectionId } from "./sections";
import "./system.css";

export function SystemPage({ params }: PageProps) {
	const requested = (params[0] ?? "overview") as SectionId;
	const section = SECTIONS.find((s) => s.id === requested) ? requested : "overview";
	const reduce = useReducedMotion();
	useEffect(() => {
		if (params[0] && section !== params[0]) navigate("system");
	}, [params, section]);
	useEffect(() => {
		const t = SECTIONS.find((s) => s.id === section)!.title;
		document.title = section === "overview" ? "System · NeoProxy" : `${t} · System · NeoProxy`;
	}, [section]);

	return (
		<>
			<PageHeader title="System" lede="The engine underneath the pool. Day to day you won't need anything here; each part below says when you would." />
			<div className="sys">
				<nav className="sys-nav" aria-label="System sections">
					<ul>
						{SECTIONS.map((s) => {
							const on = s.id === section;
							return (
								<li key={s.id}>
									<a href={s.id === "overview" ? "#/system" : `#/system/${s.id}`} className={cx("sys-nav-item", on && "is-on")} aria-current={on ? "page" : undefined}>
										{on && <motion.span layoutId="sys-nav-rail" className="sys-nav-rail" transition={reduce ? { duration: 0 } : { type: "spring", stiffness: 500, damping: 40 }} />}
										{s.title}
									</a>
								</li>
							);
						})}
					</ul>
				</nav>
				<div className="sys-body">
					<AnimatePresence mode="wait" initial={false}>
						<motion.div
							key={section}
							className="sys-panel"
							initial={reduce ? false : { opacity: 0, y: 6 }}
							animate={{ opacity: 1, y: 0 }}
							exit={reduce ? undefined : { opacity: 0, y: -4 }}
							transition={{ duration: 0.16, ease: [0.22, 1, 0.36, 1] }}
						>
							{section === "overview" && <Overview />}
							{section === "routing" && <Routing />}
							{section === "server-keys" && <ServerKeys />}
							{section === "logs" && <Logs />}
							{section === "plugins" && <Plugins />}
							{section === "configuration" && <Configuration />}
							{section === "about" && <About />}
						</motion.div>
					</AnimatePresence>
				</div>
			</div>
		</>
	);
}
