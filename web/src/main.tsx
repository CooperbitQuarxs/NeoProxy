import "@fontsource-variable/mona-sans/wdth.css";
import "@fontsource/monaspace-neon/400.css";
import "@fontsource/monaspace-neon/500.css";
import "./styles/base.css";
import "./styles/ui.css";
import "./styles/shell.css";
import "./styles/pool.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import * as RTooltip from "@radix-ui/react-tooltip";
import { queryClient } from "./api/queries";
import { App } from "./app/App";
import { ToastProvider } from "./ui/toast";


// Development only: lets design reviews feed the UI realistic data from the
// browser console. Vite removes this branch from production builds.
if (import.meta.env.DEV) (window as unknown as { __neoQuery: typeof queryClient }).__neoQuery = queryClient;
createRoot(document.getElementById("app")!).render(
	<StrictMode>
		<QueryClientProvider client={queryClient}>
			<RTooltip.Provider delayDuration={250} skipDelayDuration={150}>
				<ToastProvider>
					<App />
				</ToastProvider>
			</RTooltip.Provider>
		</QueryClientProvider>
	</StrictMode>,
);
