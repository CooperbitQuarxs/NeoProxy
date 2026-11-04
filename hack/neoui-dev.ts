// Serve the dashboard from the working tree against a running NeoProxy.
//   bun run hack/neoui-dev.ts [--port 8411] [--upstream http://127.0.0.1:8318]
// Static files come from internal/neoui/static (reload the page to see edits);
// everything else is forwarded to the proxy with Origin/Referer rewritten to the
// proxy's own origin, which the cookie sign-in requires.
const args = Object.fromEntries(Bun.argv.slice(2).map((a, i, all) => (a.startsWith("--") ? [a.slice(2), all[i + 1]] : [])).filter((p) => p.length));
const port = Number(args.port ?? 8411);
const upstream = (args.upstream ?? "http://127.0.0.1:8318").replace(/\/$/, "");
const dir = new URL("../internal/neoui/static/", import.meta.url).pathname;
const up = new URL(upstream);
Bun.serve({
	hostname: "127.0.0.1",
	port,
	async fetch(req) {
		const u = new URL(req.url);
		if (u.pathname === "/neo" || u.pathname.startsWith("/neo/")) {
			const rel = u.pathname.replace(/^\/neo\/?/, "");
			let f = Bun.file(dir + rel);
			if (!rel || !(await f.exists())) f = Bun.file(dir + "index.html");
			return new Response(f, { headers: { "Cache-Control": "no-cache" } });
		}
		const h = new Headers(req.headers);
		h.set("host", up.host);
		if (h.has("origin")) h.set("origin", upstream);
		if (h.has("referer")) h.set("referer", h.get("referer")!.replace(u.host, up.host));
		const r = await fetch(upstream + u.pathname + u.search, { method: req.method, headers: h, body: req.body, redirect: "manual" });
		return new Response(r.body, { status: r.status, headers: r.headers });
	},
});
console.log(`dashboard: http://127.0.0.1:${port}/neo/  →  ${upstream}`);
