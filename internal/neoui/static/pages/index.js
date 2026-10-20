import { overview } from "./overview.js";
import { neopi } from "./neopi.js";
import { usage } from "./usage.js";
import { accounts } from "./accounts.js";
import { credentials } from "./credentials.js";
import { access } from "./access.js";
import { traffic } from "./traffic.js";
import { logs } from "./logs.js";
import { routing } from "./routing.js";
import { providers } from "./providers.js";
import { plugins } from "./plugins.js";
import { configuration } from "./configuration.js";
import { about } from "./about.js";

export const pages = Object.fromEntries(
	[overview, neopi, usage, accounts, credentials, access, traffic, logs, routing, providers, plugins, configuration, about].map((p) => [p.id, p]),
);

// Information architecture. NeoPi is the centrepiece: first group, first page.
// The engine's own providers are kept, but demoted to "Also supported".
export const groups = [
	{ label: "NeoPi", centre: true, pages: ["overview", "neopi", "usage", "accounts"] },
	{ label: "Proxy", pages: ["credentials", "access", "traffic", "logs", "routing"] },
	{ label: "Also supported", pages: ["providers", "plugins"] },
	{ label: "System", pages: ["configuration", "about"] },
];
