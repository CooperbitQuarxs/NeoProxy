// Product surfaces use the proxy-owned backend. Engine administration remains
// restricted to organization-admin sessions.

import * as overview from "./overview.js";
import * as pool from "./pool.js";
import * as models from "./models.js";
import * as virtualModels from "./virtual-models.js";
import * as users from "./users.js";
import * as activity from "./activity.js";
import * as mine from "./mine.js";
import * as connect from "./connect.js";
import * as keys from "./keys.js";
import * as logs from "./logs.js";
import * as routing from "./routing.js";
import * as config from "./config.js";
import * as plugins from "./plugins.js";
import * as about from "./about.js";

export const groups = [
	{ id: "core", label: "NeoProxy" },
	{ id: "serving", label: "Serving" },
	{ id: "you", label: "You" },
	{ id: "engine", label: "Engine" },
];

const ADMIN = ["admin"];

export const pages = [
	{ id: "overview", title: "Overview", group: "core", icon: "overview", chord: "go", roles: ADMIN, render: overview.render },
	{ id: "pool", title: "Pool", group: "core", icon: "shield", chord: "gn", roles: ["admin", "owner"], render: pool.render },
	{ id: "models", title: "Models", group: "serving", icon: "box", chord: "gm", roles: ADMIN, render: models.render },
	{ id: "virtual-models", title: "Virtual models", group: "serving", icon: "route", chord: "gv", roles: ADMIN, render: virtualModels.render },
	{ id: "users", title: "Users", group: "serving", icon: "users", chord: "gu", roles: ADMIN, render: users.render },
	{ id: "activity", title: "Activity", group: "serving", icon: "activity", chord: "ga", render: activity.render },
	{ id: "mine", title: "My models", group: "you", icon: "user", chord: "gy", roles: ["user", "member", "owner"], render: mine.render },
	{ id: "connect", title: "Connect", group: "you", icon: "plug", chord: "gt", render: connect.render },
	{ id: "keys", title: "Access keys", group: "engine", icon: "key", chord: "gk", roles: ADMIN, render: keys.render },
	{ id: "logs", title: "Logs", group: "engine", icon: "scroll", chord: "gl", roles: ADMIN, render: logs.render },
	{ id: "routing", title: "Routing", group: "engine", icon: "shuffle", chord: "gr", roles: ADMIN, render: routing.render },
	{ id: "config", title: "Configuration", group: "engine", icon: "settings", chord: "gf", roles: ADMIN, render: config.render },
	{ id: "plugins", title: "Plugins", group: "engine", icon: "plug", chord: "gp", roles: ADMIN, render: plugins.render },
	{ id: "about", title: "About", group: "engine", icon: "info", chord: "gi", render: about.render },
	// Old links: the Core & vault, Account pools and Credentials pages are now the one Pool page.
	...["core", "accounts", "credentials"].map((id) => ({ id, title: "Pool", group: "core", icon: "shield", roles: ["admin", "owner"], hidden: true, render: (ctx) => ctx.navigate(["pool", ...ctx.params].join("/")) })),
];
