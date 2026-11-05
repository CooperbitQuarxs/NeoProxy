// Builds NeoPi's own gitlab-duo-agent model rows: the static fallback the
// model manager seeds with, and the rows discovery builds for a spread of
// GitLab model refs (each context-window rule and the default).
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_gitlab_duo_agent_rows.ts > gitlab_duo_agent_rows.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const discovery = await import(root + "/pi-catalog/src/discovery/gitlab-duo-workflow.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");

const refs = [
	{ name: "Claude Sonnet 4.6", ref: "claude_sonnet_4_6_vertex" },
	{ name: "Claude Opus 4.8", ref: "claude_opus_4_8" },
	{ name: "Claude Haiku 4.5", ref: "claude-haiku-4-5" },
	{ name: "Gemini 2.5 Pro", ref: "gemini_2_5_pro" },
	{ name: "GPT-5 Codex", ref: "gpt_5_codex" },
	{ name: "Mistral Large", ref: "mistral_large" },
];
console.log(
	JSON.stringify(
		{
			source: "NeoPi OWN gitlab-duo-agent rows: buildModel over the fallback seed and buildGitLabDuoWorkflowModelSpec",
			fallback: buildModel(discovery.buildGitLabDuoWorkflowFallbackModel()),
			discovered: refs.map(ref => ({ ref, row: buildModel(discovery.buildGitLabDuoWorkflowModelSpec(ref, "https://gitlab.com/", "70")) })),
		},
		null,
		2,
	),
);
