import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

// The dashboard is embedded in the Go binary from internal/neoui/static and
// served under /neo/. During development, API calls go to a running NeoProxy;
// Origin is rewritten so its same-origin mutation checks accept the request.
const target = process.env.NEOPROXY_URL ?? "http://127.0.0.1:8318";

export default defineConfig({
	base: "/neo/",
	plugins: [react()],
	build: {
		outDir: fileURLToPath(new URL("../internal/neoui/static", import.meta.url)),
		emptyOutDir: true,
		assetsDir: "assets",
		sourcemap: false,
		chunkSizeWarningLimit: 900,
	},
	server: {
		port: 5173,
		strictPort: true,
		proxy: Object.fromEntries(
			["/v8", "/v0", "/healthz", "/v1"].map((p) => [p, { target, changeOrigin: true, headers: { Origin: target } }]),
		),
	},
});
