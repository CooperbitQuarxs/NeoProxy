import { AnimatePresence, motion } from "motion/react";
import { createContext, useCallback, useContext, useState, type ReactNode } from "react";
import { Icon } from "./icons";

type Tone = "good" | "bad" | "info";
type Toast = { id: number; tone: Tone; text: ReactNode };
const Ctx = createContext<(text: ReactNode, tone?: Tone) => void>(() => {});

export function ToastProvider({ children }: { children: ReactNode }) {
	const [toasts, setToasts] = useState<Toast[]>([]);
	const push = useCallback((text: ReactNode, tone: Tone = "good") => {
		const id = Date.now() + Math.random();
		setToasts((t) => [...t, { id, tone, text }]);
		setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), tone === "bad" ? 7000 : 3800);
	}, []);
	return (
		<Ctx.Provider value={push}>
			{children}
			<div className="toasts" role="status" aria-live="polite">
				<AnimatePresence initial={false}>
					{toasts.map((t) => (
						<motion.div
							key={t.id}
							layout
							className={`toast toast-${t.tone}`}
							initial={{ opacity: 0, y: 12, scale: 0.98 }}
							animate={{ opacity: 1, y: 0, scale: 1 }}
							exit={{ opacity: 0, x: 24 }}
							transition={{ type: "spring", stiffness: 500, damping: 38 }}
						>
							<Icon name={t.tone === "bad" ? "alert" : t.tone === "info" ? "info" : "check"} size={16} />
							<span>{t.text}</span>
						</motion.div>
					))}
				</AnimatePresence>
			</div>
		</Ctx.Provider>
	);
}

export const useToast = () => useContext(Ctx);
