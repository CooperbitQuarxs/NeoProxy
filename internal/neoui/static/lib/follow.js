// "Follow newest": a stage can name a pattern instead of one model, such as
// codex/gpt-*-luna, and always serve the newest exposed model that matches. The
// proxy resolves it on its own; this mirrors the rule so the editor can say
// which model a pattern points at today. A `*` never crosses a `/`, and "newest"
// compares the numbers in the id as version tuples: [6, 1] > [6] > none.

const numbers = (id) => (id.match(/\d+/g) ?? []).map(Number);

export function compareVersions(a, b) {
	const x = numbers(a);
	const y = numbers(b);
	for (let i = 0; i < Math.max(x.length, y.length); i++) {
		const d = (x[i] ?? -1) - (y[i] ?? -1);
		if (d) return d;
	}
	return a < b ? -1 : a > b ? 1 : 0;
}

export function globRegex(glob) {
	return new RegExp(`^${glob.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]*")}$`);
}

// Exposed models matching `glob`, newest first.
export function followMatches(glob, models) {
	if (!glob || !glob.includes("*")) return [];
	const re = globRegex(glob);
	return models.filter((m) => m.exposed && re.test(m.id)).sort((a, b) => compareVersions(b.id, a.id));
}

// Patterns worth offering for a model id: the first version run becomes `*`, so
// codex/gpt-6.1-sol offers codex/gpt-*-sol and claude/claude-opus-5-5 offers
// claude/claude-opus-*.
export function suggestGlobs(modelId) {
	const slash = modelId.indexOf("/");
	const provider = modelId.slice(0, slash + 1);
	const name = modelId.slice(slash + 1);
	const out = [];
	const run = /\d+(?:[.-]\d+)*/;
	if (run.test(name)) out.push(provider + name.replace(run, "*"));
	// Nested ids (a gateway's openai/gpt-6-luna): also wildcard the last segment's version.
	const last = name.lastIndexOf("/");
	if (last > 0 && run.test(name.slice(last + 1))) {
		const alt = provider + name.slice(0, last + 1) + name.slice(last + 1).replace(run, "*");
		if (!out.includes(alt)) out.push(alt);
	}
	return out;
}
