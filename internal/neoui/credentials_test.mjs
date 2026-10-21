import assert from "node:assert/strict";
import { test } from "node:test";
import { refreshSummary } from "./static/pages/credentials.js";

test("batch refresh does not present HTTP 200 with failed credentials as success", () => {
	assert.deepEqual(refreshSummary([
		{ id: "fake-codex.json", success: false, error: "invalid token" },
		{ id: "fake-claude.json", success: false, error: "invalid grant" },
	]), { text: "Refresh finished: 0 refreshed, 2 failed", kind: "bad" });
});

test("batch refresh reports mixed results without hiding failures", () => {
	assert.deepEqual(refreshSummary([
		{ id: "one.json", success: true },
		{ id: "two.json", success: false, error: "invalid grant" },
	]), { text: "Refresh finished: 1 refreshed, 1 failed", kind: "warn" });
});

test("batch refresh reports successful and empty batches", () => {
	assert.deepEqual(refreshSummary([{ id: "one.json", success: true }]), {
		text: "Refresh finished: 1 refreshed, 0 failed", kind: "ok",
	});
	assert.deepEqual(refreshSummary([]), { text: "No credentials to refresh", kind: "warn" });
});
