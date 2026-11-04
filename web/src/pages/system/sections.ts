export type SectionId = "overview" | "routing" | "server-keys" | "logs" | "plugins" | "configuration" | "about";

// One line per section: what it is, and when you would go there.
export const SECTIONS: Array<{ id: SectionId; title: string; when: string }> = [
	{ id: "overview", title: "Overview", when: "Server status and the addresses apps connect to." },
	{ id: "routing", title: "Routing", when: "When one provider has several accounts: how NeoProxy picks between them and retries." },
	{ id: "server-keys", title: "Server keys", when: "Only for old clients that can't use a person's inference key." },
	{ id: "logs", title: "Logs", when: "When something fails and you need the engine's own messages." },
	{ id: "plugins", title: "Plugins", when: "Add engine extensions, such as request rewriting or quota reporting." },
	{ id: "configuration", title: "Configuration", when: "Settings with no page of their own, edited as YAML." },
	{ id: "about", title: "About", when: "What NeoProxy is, which version runs, and where to read more." },
];
