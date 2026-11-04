// Plain-language explanation of one routing decision, in the teammate's words.
// `headline` is one short clause for a table row; `sentences` is the full
// account for a detail panel; `hint` says what to do next (empty when nothing
// helps); `status` is the HTTP status the client saw, when it is known.
// Reason strings are the ones neoroute writes (resolve.go, execute.go).

import { fmtIn } from "./dom.js";
import { stageSkip, statusLabel, pctLabel } from "./components.js";

const capFirst = (s) => (s ? s[0].toUpperCase() + s.slice(1) : s);

function upstreamHeadline(status) {
	if (status === 401) return "provider rejected the sign-in (HTTP 401)";
	if (status === 403) return "provider forbade the request (HTTP 403)";
	if (status === 404) return "model missing at the provider (HTTP 404)";
	if (status === 429) return "provider rate limit (HTTP 429)";
	if (status >= 500) return `provider error (HTTP ${status})`;
	return status ? `provider error (HTTP ${status})` : "provider error";
}

// The HTTP status the client was given, derived from the stored reason.
// Upstream failures carry the provider's own status on the failed trail step.
export function failureStatusOf(d) {
	const failed = [...(d.trail ?? [])].reverse().find((t) => t.failureStatus);
	if (d.outcome === "limited" || d.outcome === "queued") return 429;
	switch (d.reason) {
		case "unknown-model": return 404;
		case "no-healthy-account": return 503;
		case "invalid-script-proposal":
		case "script-error": return 422;
		case "not-granted":
		case "no-provider-access":
		case "neoproxy_suspended":
		case "neoproxy_forbidden": return 403;
		case "unauthorized":
		case "neoproxy_unauthorized": return 401;
		case "upstream-error": return failed?.failureStatus ?? null;
		default: return null;
	}
}

export function explainDecision(record, { models = [], accounts = [], users = [], vms = [], mine = false, now = Date.now() } = {}) {
	const d = record.decision ?? { outcome: record.outcome, trail: [] };
	const modelName = (id) => models.find((m) => m.id === id)?.display ?? id;
	const acctLabel = (id) => accounts.find((a) => a.id === id)?.label ?? "an account in the pool";
	const who = users.find((u) => u.id === record.userId)?.handle ?? (mine ? "you" : "this user");
	const trail = d.trail ?? [];
	const stages = trail.filter((t) => t.kind === "stage");
	const passed = stages.filter((t) => t.skipped);
	const clauses = passed.map((t) => `stage ${t.index + 1} ${modelName(t.model)} ${stageSkip(t, { accounts, who }).clause}`);
	const list = clauses.join("; ");
	const lead = stages.length > 1 && passed.length === stages.length ? `Every stage was skipped: ${list}.` : list ? `${capFirst(list)}.` : "";
	const term = trail.find((t) => t.kind === "terminal")?.terminal;
	const vm = d.virtualModel ? vms.find((v) => v.id === d.virtualModel) : null;
	const fail = [...trail].reverse().find((t) => t.failureStatus);
	const status = failureStatusOf(d);
	const resets = d.resetsAt ? (d.resetsAt > now ? ` Resets in ${fmtIn(d.resetsAt, now)}.` : " That window has since reset.") : "";
	const adminOr = (admin, member) => (mine ? member : admin);
	const sentences = [];
	let headline = "";
	let hint = "";
	const say = (...s) => sentences.push(...s.filter(Boolean));

	if (d.outcome === "ok") {
		const where = `${modelName(d.model)} on ${acctLabel(d.accountId)}`;
		if (d.direct || trail.some((t) => t.kind === "direct")) {
			headline = "served directly";
			say(`Served directly by ${where}.`, "Direct provider models skip virtual models, so no stage thresholds applied.");
		} else if (Number.isInteger(d.stageIndex)) {
			const n = d.stageIndex + 1;
			const stage = stages.find((t) => t.index === d.stageIndex && !t.skipped);
			headline = `served by stage ${n}`;
			if (clauses.length) say(`${capFirst(list)}.`, `Stage ${n} (${where}) then answered.`);
			else if (stage && stage.metered > 0) say(`Stage ${n} answered with ${where}, with the pool at ${pctLabel(stage.total)} of its ${pctLabel(stage.cap)} threshold for ${who}.`);
			else say(`Stage ${n} answered with ${where}${stage ? ", an unmetered pool that is always available" : ""}.`);
		} else {
			headline = "served by fall through";
			say(lead, `The chain then fell through to ${where}, which is picked without a budget.`);
		}
		return { headline, sentences, hint, status: null };
	}

	if (d.outcome === "limited" && d.reason === "user-limit") {
		headline = "personal limit reached";
		say(`${capFirst(who)} reached ${mine ? "your" : "their"} own request or daily token ceiling, so the request was refused before any stage was tried (HTTP 429).`);
		hint = adminOr("Raise this person's limits on their user page, or wait for the minute or day to roll over.", "Wait for the minute or day to roll over, or ask an admin to raise your limits.");
	} else if (d.outcome === "limited") {
		headline = stages.length > 1 ? "limit reached on every stage" : "limit reached";
		const message = term?.message || d.message;
		say(lead, "The chain ended with a limit error (HTTP 429).", message ? `The client was told “${message}”.` : "", resets.trim());
		hint = adminOr(`Raise the thresholds on ${vm ? `the ${vm.name} chain` : "the chain"}, give ${who} a larger allotment, or wait for the reset.`, "Wait for the reset, or ask an admin to raise your allotment.");
	} else if (d.outcome === "queued") {
		headline = "queued until a stage frees up";
		say(lead, "The chain queues instead of failing, so this request waits for a stage that can serve.", resets.trim());
	} else {
		switch (d.reason) {
			case "unknown-model":
				headline = "unknown model";
				say(`Nothing on this proxy is called “${record.asked}” (HTTP 404).`, "Ask for neoproxy/<name> of a virtual model, or neoproxy/<provider>/<model> for a direct one.");
				hint = "Check the exact name under Models or Virtual models.";
				break;
			case "no-provider-access":
				headline = "no access to that provider";
				say(d.message ? `${d.message} (HTTP 403).` : `${capFirst(who)} has no direct access to that provider (HTTP 403).`);
				hint = adminOr(`Allow the provider on ${who}'s user page, or send ${who} through a virtual model.`, "Use a virtual model, or ask an admin for access.");
				break;
			case "not-granted":
				headline = "model not granted";
				say(`${capFirst(who)} has no grant on ${vm ? `the ${vm.name} virtual model` : "that virtual model"} (HTTP 403).`);
				hint = adminOr(`Grant ${who} this model on their user page.`, "Ask an admin to grant you this model.");
				break;
			case "no-healthy-account":
				headline = "no healthy account";
				say(lead, "No healthy account could serve it, so the request failed (HTTP 503).");
				hint = adminOr("Check the account in the Pool: it may be cooling down, signed out, or out of tokens. Test it there to try the provider now.", "Try again shortly, or tell an admin.");
				break;
			case "upstream-error": {
				headline = upstreamHeadline(fail?.failureStatus);
				if (fail) {
					say(`${capFirst(clauses.find((c) => c.includes("failed")) ?? `the provider answered with HTTP ${fail.failureStatus}, ${statusLabel(fail.failureStatus)}`)}.`);
					if (stages.length && passed.length === stages.length) say("No other stage could take over.");
				} else {
					const target = d.model ? `${modelName(d.model)} on ${acctLabel(d.accountId)}` : "the provider";
					say(lead, `The chain fell through to ${target}, and the provider answered with an error. The status was not recorded.`);
				}
				hint = fail?.failureStatus === 401 ? adminOr("Sign in to that account again from the Pool.", "Tell an admin; the account behind this model needs reconnecting.")
					: fail?.failureStatus === 429 ? "That account hit the provider's own rate limit and will be skipped while it cools down."
					: fail?.failureStatus === 404 ? "The provider no longer offers this model; update the stage's model."
					: adminOr("Look in Logs for the provider's response.", "Try again shortly.");
				break;
			}
			case "invalid-script-proposal":
				headline = "routing script error";
				say("The routing script proposed a stage or account that is not allowed, so nothing was tried (HTTP 422).");
				hint = adminOr("Fix the script on the virtual model's stage.", "Tell an admin.");
				break;
			case "script-error":
				headline = "routing script error";
				say(`The routing script failed${d.message ? `: ${d.message}` : ""} (HTTP 422).`);
				hint = adminOr("Fix the script on the virtual model's stage.", "Tell an admin.");
				break;
			case "cancelled":
				headline = "client disconnected";
				say("The client hung up before an answer was finished, so the request was cancelled.");
				break;
			case "unauthorized":
			case "neoproxy_unauthorized":
				headline = "key rejected (HTTP 401)";
				say(d.message ? `${d.message}.` : "", "The inference key was rejected (HTTP 401).");
				hint = adminOr("Create a new key for this person on their user page.", "Ask an admin for a new key.");
				break;
			case "neoproxy_suspended":
				headline = "user suspended (HTTP 403)";
				say("This person is suspended, so every request is refused (HTTP 403).");
				hint = adminOr("Reinstate them on their user page.", "Ask an admin.");
				break;
			case "neoproxy_forbidden":
				headline = "not authorised (HTTP 403)";
				say(d.message ? `${d.message}.` : "", "The key's scope no longer allows this (HTTP 403).");
				break;
			case "routing-error":
				headline = "routing error";
				say("The router failed before it could pick an account. This is a proxy fault, not a limit.");
				hint = adminOr("Look in Logs around this time.", "Try again shortly.");
				break;
			case "reservation-error":
				headline = "could not reserve tokens";
				say("The router could not reserve token budget for this request, so it was not sent.");
				break;
			case "settlement-error":
				headline = "could not record usage";
				say("The provider answered, but the router could not record the token usage afterwards.");
				break;
			default:
				headline = d.reason || "error";
				say(d.message || (d.reason ? `The request failed with reason “${d.reason}”.` : "The request failed. The ledger did not record why."));
		}
	}
	return { headline, sentences, hint, status };
}
