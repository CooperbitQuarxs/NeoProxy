import assert from "node:assert/strict";
import test from "node:test";
import { resolve } from "./static/lib/fixture.js";

function exhaustedRoute() {
	const user = { id: "member", providerAccess: [], grants: [{ virtualModel: "vm-route", allotment: 1 }] };
	const world = {
		providers: [{ id: "primary", exposed: true }, { id: "fallback", exposed: true }],
		models: [
			{ id: "primary/model", providerId: "primary", exposed: true },
			{ id: "fallback/model", providerId: "fallback", exposed: true },
		],
		accounts: [
			{ id: "primary-ready", providerId: "primary", state: "healthy" },
			{ id: "fallback-cooling", providerId: "fallback", state: "cooldown" },
			{ id: "fallback-ready", providerId: "fallback", state: "healthy" },
		],
		virtualModels: [{
			id: "vm-route",
			name: "route",
			stages: [{ id: "stage", model: "primary/model", accounts: "all", draw: "fill", perAccount: 0.1, cap: 0.1, per: "user", window: "week" }],
			terminal: { kind: "passthrough", model: "fallback/model" },
		}],
		usage: { member: { "vm-route": { stage: { "primary-ready": 0.1 } } } },
	};
	return { user, world };
}

function simulate({ user, world }) {
	return resolve({ asked: "neoproxy/route", user, at: 0 }, world);
}

test("an exhausted granted route falls through without direct access or a stage debit", () => {
	const state = exhaustedRoute();
	const usage = structuredClone(state.world.usage);
	const result = simulate(state);
	assert.equal(result.outcome, "ok");
	assert.equal(result.virtualModel, "vm-route");
	assert.equal(result.model, "fallback/model");
	assert.equal(result.accountId, "fallback-ready");
	assert.equal(result.stageId, undefined);
	assert.equal(result.stageIndex, undefined);
	assert.equal(result.direct, undefined);
	assert.deepEqual(result.trail.map(step => [step.kind, step.skipped]), [["stage", "cap"], ["terminal", undefined]]);
	assert.deepEqual(state.world.usage, usage);
});

for (const [name, change, reason] of [
	["missing target", state => { delete state.world.virtualModels[0].terminal.model; }, "unknown-model"],
	["hidden model", state => { state.world.models[1].exposed = false; }, "unknown-model"],
	["hidden provider", state => { state.world.providers[1].exposed = false; }, "unknown-model"],
	["no healthy account", state => { state.world.accounts[2].state = "expired"; }, "no-healthy-account"],
]) {
	test(`fall-through reports an error for ${name}, not a limit or success`, () => {
		const state = exhaustedRoute();
		change(state);
		const result = simulate(state);
		assert.equal(result.outcome, "error");
		assert.equal(result.reason, reason);
		assert.equal(result.virtualModel, "vm-route");
		assert.equal(result.accountId, undefined);
		assert.equal(result.trail.at(-1).terminal.kind, "passthrough");
	});
}

test("fall-through does not bypass the virtual-model grant", () => {
	const state = exhaustedRoute();
	state.user.grants = [];
	const result = simulate(state);
	assert.equal(result.outcome, "error");
	assert.equal(result.reason, "not-granted");
	assert.equal(result.accountId, undefined);
});

test("a stage that can serve takes precedence over fall-through", () => {
	const state = exhaustedRoute();
	state.world.usage = {};
	const result = simulate(state);
	assert.equal(result.outcome, "ok");
	assert.equal(result.model, "primary/model");
	assert.equal(result.accountId, "primary-ready");
	assert.equal(result.stageId, "stage");
	assert.equal(result.stageIndex, 0);
	assert.equal(result.trail.at(-1).kind, "stage");
});

for (const [kind, outcome] of [["limit", "limited"], ["queue", "queued"]]) {
	test(`an exhausted ${kind} terminal retains its outcome`, () => {
		const state = exhaustedRoute();
		state.world.virtualModels[0].terminal = { kind, message: "Wait for the reset." };
		const result = simulate(state);
		assert.equal(result.outcome, outcome);
		assert.equal(result.reason, kind);
		assert.equal(result.message, "Wait for the reset.");
		assert.equal(result.accountId, undefined);
	});
}
