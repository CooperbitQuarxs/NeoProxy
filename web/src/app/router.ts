import { useEffect, useState } from "react";

// Hash routing keeps /neo/#/... links working from the old dashboard and needs
// no server rewrite rules.
export type Route = { page: string; params: string[]; query: URLSearchParams };

function parse(): Route {
	const hash = location.hash.replace(/^#\/?/, "");
	const [path, query = ""] = hash.split("?");
	const segs = path.split("/").filter(Boolean);
	return { page: segs[0] || "pool", params: segs.slice(1), query: new URLSearchParams(query) };
}

export function useRoute() {
	const [route, setRoute] = useState(parse);
	useEffect(() => {
		const on = () => setRoute(parse());
		window.addEventListener("hashchange", on);
		return () => window.removeEventListener("hashchange", on);
	}, []);
	return route;
}

export function navigate(to: string, { replace = false }: { replace?: boolean } = {}) {
	const hash = `#/${to.replace(/^\/+/, "")}`;
	if (replace) location.replace(hash);
	else location.hash = hash;
}

// A layer is a route that opens a panel over its parent page. Opening pushes one
// history entry and closing pops it, so close and browser Back agree and Back
// never reopens a panel that was just closed. A layer reached by a direct link
// has nothing of ours to pop, so closing replaces it with the parent instead.
export function openLayer(to: string) {
	navigate(to);
	history.replaceState({ ...(history.state ?? {}), neoLayer: true }, "");
}

export function closeLayer(parent: string) {
	if ((history.state as { neoLayer?: boolean } | null)?.neoLayer) history.back();
	else navigate(parent, { replace: true });
}

// Old page ids map onto the new five surfaces.
export const LEGACY_REDIRECTS: Record<string, string> = {
	overview: "pool",
	core: "pool",
	accounts: "pool",
	credentials: "pool",
	mine: "models",
	"virtual-models": "models",
	users: "people",
	keys: "system/server-keys",
	logs: "system/logs",
	routing: "system/routing",
	config: "system/configuration",
	plugins: "system/plugins",
	about: "system/about",
};
