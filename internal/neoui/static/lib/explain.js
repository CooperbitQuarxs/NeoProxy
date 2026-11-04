// Plain-language explanation of one routing decision, in the teammate's words.
// `headline` is one short clause for a table row; `sentences` is the full
// account for a detail panel, the failure first and the path that led to it
// after; `hint` says what to do next (empty when nothing helps); `fix` is where
// to do it ({ label, href }, or null); `status` is the HTTP status the client
// saw, when it is known. Reason strings are the ones neoroute writes
// (resolve.go, execute.go).

import { fmtIn } from "./dom.js";
import { stageSkip, statusLabel, pctLabel, redirectWords, redirectRule } from "./components.js";

const capFirst = (s) => (s ? s[0].toUpperCase() + s.slice(1) : s);
const lowFirst = (s) => (s ? s[0].toLowerCase() + s.slice(1) : s);

function upstreamHeadline(status) {
	if (status === 400) return "provider refused the request (HTTP 400)";
	if (status === 401) return "provider rejected the sign-in (HTTP 401)";
	if (status === 403) return "provider forbade the request (HTTP 403)";
	if (status === 404) return "model missing at the provider (HTTP 404)";
	if (status === 429) return "provider rate limit (HTTP 429)";
	return status ? `provider error (HTTP ${status})` : "provider refused the request";
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

// What to do about the provider's own answer, once its words are known (from
// the trail or the engine's error report). `where` names the model and account
// the request was sent to; `vm` the virtual model whose rule sent it there.
export function providerAdvice(status, message, { mine = false, vm = null, accountId = null, modelLabel = "this model" } = {}) {
	const text = String(message ?? "");
	const at = (admin, member, fix) => (mine ? { hint: member, fix: null } : { hint: admin, fix });
	const vmFix = vm ? { label: `Open ${vm.name}`, href: `virtual-models/${vm.id}` } : { label: "Open Virtual models", href: "virtual-models" };
	const acctFix = accountId ? { label: "Open the account", href: `pool/${accountId}` } : { label: "Open Pool", href: "pool" };
	if (/not supported|unsupported|does not exist|not found|no such model|model_not_found/i.test(text) || status === 404)
		return at(`This account does not offer ${modelLabel}. ${vm ? `Point ${vm.name}'s step at a model the account lists` : "Pick a model the account lists"}, or add an account that offers it.`, "Tell an admin: the account behind this model does not offer it.", vmFix);
	if (status === 401 || /sign.?in|expired|invalid.*(token|credential)|unauthori[sz]ed/i.test(text))
		return at("Sign in to this account again from Pool, then test it there.", "Tell an admin; the account behind this model needs reconnecting.", acctFix);
	if (status === 429 || /rate.?limit|quota|usage limit/i.test(text))
		return at("The provider's own limit for this account was reached; it is skipped while it cools down. Check its usage in Pool.", "Try again later; the provider's limit was reached.", acctFix);
	if (status === 403)
		return at("The provider refused this account. Open it in Pool to check its plan and permissions.", "Tell an admin; the provider refused the account.", acctFix);
	if (status >= 500)
		return at("The provider failed on its side. Test the account in Pool; if it fails there too, try again later.", "Try again shortly.", acctFix);
	return at("Read the provider's full answer in the error report, then test the account in Pool.", "Try again shortly, or tell an admin.", acctFix);
}

export function explainDecision(record, { models = [], accounts = [], users = [], vms = [], mine = false, now = Date.now() } = {}) {
	const d = record.decision ?? { outcome: record.outcome, trail: [] };
	const modelName = (id) => models.find((m) => m.id === id)?.display ?? id;
	const acctLabel = (id) => accounts.find((a) => a.id === id)?.label ?? "an account in the pool";
	const user = users.find((u) => u.id === record.userId);
	const who = user?.handle ?? (mine ? "you" : "this user");
	const trail = d.trail ?? [];
	const stages = trail.filter((t) => t.kind === "stage");
	const passed = stages.filter((t) => t.skipped);
	// A position in the chain as people say it; a virtual model with one model has no "first choice".
	const single = (d.virtualModel ? vms.find((v) => v.id === d.virtualModel)?.stages?.length : null) === 1 || stages.length <= 1 && !stages.some((t) => t.index > 0);
	const slot = (i) => (single ? "the model" : i === 0 ? "the first choice" : `fallback ${i}`);
	const clauses = passed.map((t) => `${slot(t.index)} ${modelName(t.model)} ${stageSkip(t, { accounts, who }).clause}`);
	const list = clauses.join("; ");
	const everySkipped = stages.length > 0 && passed.length === stages.length;
	// Budget-only skips are limits; anything else (unavailable accounts, rules, scripts) is the chain's rules.
	const onlyLimits = passed.every((t) => ["cap", "per-account", "lua-cap", "lua-policy"].includes(t.skipped));
	const lead = everySkipped ? `No account met this chain's ${onlyLimits ? "limits" : "rules"}: ${list}.` : list ? `${capFirst(list)}.` : "";
	const term = trail.find((t) => t.kind === "terminal")?.terminal;
	const finalRule = term?.kind === "passthrough";
	const vm = d.virtualModel ? vms.find((v) => v.id === d.virtualModel) : null;
	const vmWords = vm ? `the ${vm.name} virtual model` : d.virtualModel ? "a virtual model that has since been removed" : "the virtual model";
	const fail = [...trail].reverse().find((t) => t.failureStatus);
	const status = failureStatusOf(d);
	const adminOr = (admin, member) => (mine ? member : admin);
	const fixAt = (label, href) => (mine ? null : { label, href });
	const vmFix = vm ? fixAt(`Open ${vm.name}`, `virtual-models/${vm.id}`) : null;
	const userFix = user ? fixAt(`Open ${user.handle}`, `users/${user.id}`) : null;
	const sentences = [];
	let headline = "";
	let hint = "";
	let fix = null;
	const say = (...s) => sentences.push(...s.filter(Boolean));
	// A redirect comes first: everything after it is about the model it sent to.
	const redirect = d.redirect ?? trail.find((t) => t.kind === "redirect")?.redirect;
	if (redirect) say(redirectWords(redirect));
	const asked = redirect?.to ?? record.asked;

	if (d.outcome === "ok") {
		const where = `${modelName(d.model)} on ${acctLabel(d.accountId)}`;
		if (d.direct || trail.some((t) => t.kind === "direct")) {
			headline = "served directly";
			say(`Served directly by ${where}.`, "Direct provider models skip virtual models, so no virtual model limits applied.");
		} else if (Number.isInteger(d.stageIndex)) {
			const n = d.stageIndex + 1;
			const stage = stages.find((t) => t.index === d.stageIndex && !t.skipped);
			headline = single ? "served" : `served by ${slot(n - 1).replace(/^the /, "")}`;
			if (clauses.length) say(`${capFirst(list)}.`, `${capFirst(slot(n - 1))} (${where}) then answered.`);
			else if (stage && stage.metered > 0) say(`${capFirst(slot(n - 1))} answered with ${where}. Its accounts were at ${pctLabel(stage.total)} of the ${pctLabel(stage.cap)} ${who} may use.`);
			else say(`${capFirst(slot(n - 1))} answered with ${where}${stage ? ". Its accounts have no usage meter, so no limit applies to them" : ""}.`);
		} else {
			headline = "served by the chain's final rule";
			say(lead, `Its final rule then sent the request to ${where} without the chain's limits, and it answered.`, "That is how this virtual model is set up: when no step can take a request, its last rule passes it on instead of refusing it.");
		}
		if (redirect) headline += " after a redirect";
		return { headline, sentences, hint, fix: null, status: null };
	}

	if (d.outcome === "limited" && d.reason === "user-limit") {
		headline = "personal limit reached";
		say(`${capFirst(who)} reached ${mine ? "your" : "their"} own request or daily token ceiling, so the request was refused before any model was tried (HTTP 429).`, "It lifts when the minute or the day (UTC) rolls over.");
		hint = adminOr(`Raise ${who}'s request or token ceiling on their user page, or wait for the rollover.`, "Wait for the minute or day to roll over, or ask an admin to raise your limits.");
		fix = userFix;
	} else if (d.outcome === "limited") {
		headline = stages.length > 1 ? "limit reached on every model" : "limit reached";
		const message = term?.message || d.message;
		const off = passed.length && passed.every((t) => t.skipped === "cap" && !(t.cap > 0));
		const reset = d.resetsAt ? (d.resetsAt > now ? `The earliest limit resets in ${fmtIn(d.resetsAt, now)}.` : "That window has since reset, so the same request would be tried again now.") : off ? `Waiting will not help: a 0% threshold keeps this model off for ${who} until someone raises it.` : "";
		say(lead, message ? `The chain then ended with a limit error (HTTP 429) and told the client “${message}”.` : "The chain then ended with a limit error (HTTP 429).", reset);
		hint = vm || !d.virtualModel
			? adminOr(`${off ? "Raise the threshold" : "Raise the usage limits"} on ${vmWords}, or give ${who} a larger share on their user page${d.resetsAt && d.resetsAt > now ? "; or wait for the reset" : ""}.`, d.resetsAt && d.resetsAt > now ? "Wait for the reset, or ask an admin to raise your share." : "Ask an admin to raise your share.")
			: adminOr("That virtual model has since been removed, so nothing is left to change on it; new requests for it fail as an unknown model. Point the client at another virtual model.", "That model has since been removed; ask an admin which to use instead.");
		fix = vm ? vmFix : d.virtualModel ? fixAt("Open Virtual models", "virtual-models") : null;
	} else if (d.outcome === "queued") {
		headline = "queued until a model frees up";
		say(lead, "The virtual model queues instead of failing, so this request waits for a model that can serve.", d.resetsAt && d.resetsAt > now ? `The earliest limit resets in ${fmtIn(d.resetsAt, now)}.` : "");
		fix = vmFix;
	} else {
		switch (d.reason) {
			case "unknown-model":
				headline = "unknown model";
				say(`Nothing on this proxy is called “${asked}” (HTTP 404).`, "Ask for neoproxy/<name> of a virtual model, or neoproxy/<provider>/<model> for a direct one.");
				hint = redirect ? adminOr(`Point ${redirectRule(redirect)} at a model that exists, under Redirects.`, "Tell an admin: a redirect points at a model that no longer exists.") : "Check the exact name under Models or Virtual models.";
				fix = redirect ? fixAt("Open Redirects", "redirects") : fixAt("Open Models", "models");
				break;
			case "no-provider-access":
				headline = "no access to that provider";
				say(d.message ? `${d.message} (HTTP 403).` : `${capFirst(who)} has no direct access to that provider (HTTP 403).`);
				hint = adminOr(`Allow the provider on ${who}'s user page, or send ${who} through a virtual model.`, "Use a virtual model, or ask an admin for access.");
				fix = userFix;
				break;
			case "not-granted":
				headline = "model not granted";
				say(`${capFirst(who)} has no grant on ${vmWords} (HTTP 403).`);
				hint = adminOr(`Grant ${who} this model on their user page.`, "Ask an admin to grant you this model.");
				fix = userFix;
				break;
			case "no-healthy-account": {
				headline = "no available account";
				const model = modelName(term?.model ?? d.model ?? stages[stages.length - 1]?.model ?? "");
				if (finalRule) say("No account could take this request, so it failed (HTTP 503).", lead, `Its final rule then looked for any available ${model} account without the chain's limits, and found none.`);
				else say("No account could take this request, so it failed (HTTP 503).", lead);
				hint = adminOr(`Every ${model} account is signed out, cooling down, turned off, or does not offer it. Open Pool to see which, and test it there.`, "Try again shortly, or tell an admin.");
				fix = fixAt("Open Pool", "pool");
				break;
			}
			case "upstream-error": {
				headline = upstreamHeadline(fail?.failureStatus);
				const target = d.model ? `${modelName(d.model)} on ${acctLabel(d.accountId)}` : "";
				// The failure itself comes first; how the request got there follows.
				if (fail) {
					say(`${capFirst(clauses.find((c) => c.includes("failed")) ?? `the provider answered HTTP ${fail.failureStatus}, ${statusLabel(fail.failureStatus)}${target ? `, on ${target}` : ""}`)}.`);
					if (everySkipped) say("No other model could take over.");
				} else {
					say(target ? `The provider refused this request on ${target}.` : "The provider refused this request.");
					if (finalRule && lead) say(`It was sent there because ${lowFirst(lead)}`, "Its final rule then tried it without the chain's limits.");
					else if (lead) say(`Before that, ${lowFirst(lead)}`);
				}
				({ hint, fix } = providerAdvice(fail?.failureStatus ?? null, fail?.failureMessage ?? d.message, { mine, vm, accountId: d.accountId, modelLabel: d.model ? modelName(d.model) : "this model" }));
				break;
			}
			case "invalid-script-proposal":
				headline = "routing script error";
				say("The routing script proposed a model or account that is not allowed, so nothing was tried (HTTP 422).");
				hint = adminOr("Fix the routing script on the virtual model.", "Tell an admin.");
				fix = vmFix;
				break;
			case "script-error":
				headline = "routing script error";
				say(`The routing script failed${d.message ? `: ${d.message}` : ""} (HTTP 422).`);
				hint = adminOr("Fix the routing script on the virtual model.", "Tell an admin.");
				fix = vmFix;
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
				fix = userFix;
				break;
			case "neoproxy_suspended":
				headline = "user suspended (HTTP 403)";
				say("This person is suspended, so every request is refused (HTTP 403).");
				hint = adminOr("Reinstate them on their user page.", "Ask an admin.");
				fix = userFix;
				break;
			case "neoproxy_forbidden":
				headline = "not authorised (HTTP 403)";
				say(d.message ? `${d.message}.` : "", "The key's scope no longer allows this (HTTP 403).");
				fix = userFix;
				break;
			case "routing-error":
				headline = "routing error";
				say("The router failed before it could pick an account. This is a proxy fault, not a limit.");
				hint = adminOr("Look in Logs around this time.", "Try again shortly.");
				fix = fixAt("Open Logs", "logs");
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
		// A redirect never grants access; say where the gap is.
		if (redirect && !redirect.bypassAccess && (d.reason === "not-granted" || d.reason === "no-provider-access" || d.reason === "no-model-access")) {
			hint = adminOr(`${capFirst(redirectRule(redirect))} sends ${who} to ${redirect.to}, which they cannot use. Give them access to it, or turn on Bypass access on that redirect.`, `A redirect sends this model to ${redirect.to}, which you have no access to. Ask an admin.`);
			fix = fixAt("Open Redirects", "redirects");
		}
	}
	return { headline, sentences, hint, fix, status };
}
