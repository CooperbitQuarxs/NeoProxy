// What the Configuration page knows about the engine's settings: the sections,
// and for the common settings their type, default and meaning, so each one gets a
// fitting control and shows its default even when the file does not mention it.
// Written from the Go config structs (internal/config/config.go, config_types.go,
// sdk_config.go). Settings missing here still show, with a control guessed from
// their value.

export const SECTIONS = [
	["server", "Server", "Where NeoProxy listens, HTTPS, and whether it announces itself on your local network."],
	["access", "Access", "The keys your apps and teammates use to call NeoProxy."],
	["routing", "Routing", "How NeoProxy picks an account for direct provider models, and when it retries or rests one. Virtual models follow their own stages instead."],
	["requests", "Outgoing requests", "Proxy, timeouts and streaming keep-alives for requests sent to providers."],
	["api-keys", "Provider API keys", "Provider endpoints and keys NeoProxy calls for you, with the models each one serves."],
	["oauth", "Signed-in accounts", "Accounts added with a provider sign-in: where their tokens are kept, model aliases and per-provider options."],
	["upstream", "Provider behavior", "Provider-wide request behavior for Claude, Codex and xAI."],
	["client", "Client compatibility", "Switches that help specific client apps work through NeoProxy."],
	["multimedia", "Images and video", "Image and video generation."],
	["observability", "Logs and usage", "What NeoProxy logs, for how long, and whether it counts usage."],
	["management", "Management", "The management API and the bundled control panel."],
	["plugins", "Plugins", "Plugin loading, plugin stores and settings for each plugin."],
	["models", "Model catalogs", "Where NeoProxy gets the model lists it shows to clients."],
	["quota-exceeded", "When a quota runs out", "What NeoProxy does when a provider says a quota is used up."],
	["credentials", "Credential handling", "How long NeoProxy holds, retries and releases an account while requests run. The defaults suit almost every setup.", { advanced: true }],
];

const on = (label, hint, def = false, extra = {}) => ({ kind: "bool", label, hint, default: def, ...extra });
const num = (label, hint, def, unit, extra = {}) => ({ kind: "int", label, hint, default: def, unit, ...extra });
const text = (label, hint, def, extra = {}) => ({ kind: "text", label, hint, default: def, ...extra });
const dur = (label, hint, def, extra = {}) => ({ kind: "duration", label, hint, default: def, ...extra });

// Keyed by dotted path. Order within a group follows this table.
export const KNOWN = {
	"server.host": text("Listen address", "Leave empty to listen on every network interface; 127.0.0.1 keeps NeoProxy private to this machine.", "", { placeholder: "Every interface", mono: true, restart: true }),
	"server.port": num("Port", "The port apps connect to.", undefined, null, { min: 1, max: 65535, plain: true, restart: true }),
	"server.max-request-body-bytes": num("Largest request body", "Requests bigger than this are refused. 0 accepts any size.", 0, "bytes", { min: 0, zero: "No limit" }),
	"server.trusted-proxies": { kind: "list", label: "Trusted proxies", hint: "Addresses allowed to pass on the caller's real IP in forwarded headers.", placeholder: "10.0.0.0/8", restart: true },
	"server.commercial-mode": on("Low-memory mode", "Turns off heavier request logging to keep memory use per request low."),
	"server.github-token": { kind: "secret", label: "GitHub token", hint: "Used for GitHub requests, ahead of the GITHUB_TOKEN environment variable." },
	"server.tls.enable": on("HTTPS", "Serve HTTPS instead of HTTP. Needs a certificate and its key."),
	"server.tls.cert": text("Certificate file", "Path to the certificate, in PEM format.", "", { placeholder: "/etc/neoproxy/cert.pem", mono: true }),
	"server.tls.key": text("Private key file", "Path to the certificate's private key.", "", { placeholder: "/etc/neoproxy/key.pem", mono: true }),
	"server.discovery.enabled": on("Announce on the network", "Advertise NeoProxy with mDNS so nearby apps can find it."),
	"server.discovery.service-name": text("Announced name", "Empty uses an automatic name such as CPA-1a2b.", "", { placeholder: "Automatic" }),
	"server.discovery.service-type": text("Service type", "The DNS-SD service type it announces.", "_ai-gateway._tcp", { mono: true }),
	"server.discovery.subtypes": { kind: "list", label: "API styles", hint: "The API styles it says it speaks.", placeholder: "_responses", suggest: ["_chat-completions", "_responses", "_messages", "_generate-content", "_interactions"] },
	"server.discovery.auth-required": on("Tell clients a key is needed", "Part of the announcement; it does not change who may call.", true),
	"server.discovery.advertise-management": on("Announce the management API", "Also advertise the management endpoints."),

	"access.api-keys": { kind: "list", secret: true, label: "Access keys in the file", hint: "Anyone holding one of these keys can call NeoProxy. Keys made on the Access keys page are kept elsewhere.", add: "Paste a key and press Enter" },

	"routing.strategy": {
		kind: "enum", label: "Strategy", hint: "How a direct provider model (neoproxy/<provider>/<model>) picks among that provider's accounts.", default: "round-robin",
		options: [
			{ value: "round-robin", label: "Round robin", note: "Take turns across healthy accounts." },
			{ value: "weighted-round-robin", label: "Weighted", note: "Take turns; a bigger weight gets a bigger share." },
			{ value: "fill-first", label: "Fill first", note: "Use one account until it can't serve, then the next." },
		],
	},
	"routing.session-affinity": on("Keep conversations on one account", "Provider-side prompt caching keeps working. If that account fails, the conversation moves."),
	"routing.session-affinity-ttl": dur("Remember a conversation for", "How long a conversation stays tied to its account.", "1h"),
	"routing.session-affinity-subagents": on("Subagents share their parent's account", "Child sessions reuse the parent's account for better cache reuse.", true),
	"routing.session-affinity-rebind-on": {
		kind: "enum", label: "Move a conversation when", default: "any",
		options: [
			{ value: "any", label: "Any failure", note: "Any account failure moves the conversation." },
			{ value: "quota", label: "Quota or sign-in errors", note: "Brief errors retry elsewhere once; the conversation stays." },
		],
	},
	"routing.force-model-prefix": on("Require a prefix for prefixed accounts", "Accounts with a prefix answer only requests that name it, such as team/kimi-for-coding."),
	"routing.retry.request-retry": num("Extra retry rounds", "After every eligible account has failed once, try again this many times.", 0, "rounds", { min: 0, zero: "No extra rounds" }),
	"routing.retry.max-retry-credentials": num("Accounts per round", "How many different accounts to try in each round.", 0, "accounts", { min: 0, zero: "All of them" }),
	"routing.retry.max-retry-interval": num("Longest wait for a resting account", "Before another round, wait at most this long for an account to come back.", 0, "seconds", { min: 0, zero: "Don't wait" }),
	"routing.cooldown.disable-cooling": { kind: "bool", invert: true, label: "Rest accounts after errors", hint: "Pause an account or model for a while after it fails. A provider or key can override this.", default: false },
	"routing.cooldown.transient-error-cooldown-seconds": num("Rest after brief provider errors", "For timeouts and 5xx answers. 0 keeps the built-in rest; -1 turns it off.", 0, "seconds", { min: -1, zero: "Built-in", negative: "Off" }),
	"routing.cooldown.save-cooldown-status": on("Remember rests across restarts", "Resting accounts stay resting after the engine restarts."),

	"requests.proxy-url": text("Proxy", "Send provider requests through this proxy. A key can set its own.", "", { placeholder: "socks5://127.0.0.1:1080", mono: true }),
	"requests.tls-handshake-timeout-seconds": num("TLS handshake timeout", "Give up on a provider that accepts the connection but never finishes TLS. 0 waits forever.", 0, "seconds", { min: 0, zero: "No timeout" }),
	"requests.passthrough-headers": on("Pass provider headers to clients", "Forward the provider's response headers to the app that asked."),
	"requests.nonstream-keepalive-interval": num("Keep-alive for non-streaming answers", "Send a blank line this often so slow answers don't time out. 0 sends none.", 0, "seconds", { min: 0, zero: "Off" }),
	"requests.streaming.keepalive-seconds": num("Stream keep-alive", "Send a heartbeat on open streams this often. 0 sends none.", 0, "seconds", { min: 0, zero: "Off" }),
	"requests.streaming.send-keepalive-before-ft": on("Heartbeats before the first token", "Also send heartbeats while waiting for the first token."),
	"requests.streaming.bootstrap-retries": num("Retries before a stream starts", "Retry a stream on another account if it fails before sending anything.", 0, "retries", { min: 0, zero: "Off" }),

	"oauth.auth-dir": text("Token folder", "Folder where signed-in account tokens are saved.", "", { placeholder: "~/.cli-proxy-api", mono: true }),
	"oauth.auth-auto-refresh-workers": num("Refreshes at once", "How many accounts refresh their tokens in parallel.", 0, "accounts", { min: 0, zero: "Automatic" }),
	"oauth.providers.aistudio.ws-auth": on("Require a key for AI Studio WebSocket", "Callers of the AI Studio WebSocket API must send a key."),

	"client.codex.optimize-multi-agent-v2": on("Optimize Codex multi-agent requests", "Improves official Codex multi-agent requests across providers."),
	"client.codex.enable-apply-patch": on("Offer apply_patch to Codex", "Advertise the freeform apply_patch tool for models that support it."),

	"multimedia.disable-image-generation": {
		kind: "enum", label: "Image generation tool", default: false,
		options: [
			{ value: false, label: "On", note: "Available everywhere." },
			{ value: "chat", label: "Images API only", note: "Not added to chat requests; the images endpoints still work." },
			{ value: "passthrough", label: "As the app asks", note: "Kept only when the app sends it; the images endpoints still work." },
			{ value: true, label: "Off", note: "Removed everywhere; the images endpoints answer 404." },
		],
	},
	"multimedia.gpt-image-2-base-model": text("Base model for image requests", "Must start with gpt-. Empty uses gpt-5.4-mini.", "", { placeholder: "gpt-5.4-mini", mono: true }),
	"multimedia.video-result-auth-cache-ttl": dur("Keep a video on its account for", "Video results are fetched with the account that made them, for this long.", "3h"),

	"observability.logs.debug": on("Debug logging", "Log extra detail for troubleshooting."),
	"observability.logs.logging-to-file": on("Log to files", "Write logs to rotating files instead of the console."),
	"observability.logs.logs-max-total-size-mb": num("Log folder limit", "Delete the oldest log files beyond this total size.", 0, "MB", { min: 0, zero: "Keep everything" }),
	"observability.logs.request-log": on("Request log", "Keep a detailed log of every request."),
	"observability.logs.error-logs-max-files": num("Error logs to keep", "When the request log is off, keep this many error log files.", 10, "files", { min: 0, zero: "Keep all" }),
	"observability.usage.usage-statistics-enabled": on("Count usage", "Count usage in memory. When off, usage is discarded."),
	"observability.usage.redis-usage-queue-retention-seconds": num("Keep usage events for", "How long usage events wait for the management API to read them.", 60, "seconds", { min: 1, max: 3600 }),
	"observability.pprof.enable": on("Profiling server", "Run Go's pprof debug server."),
	"observability.pprof.addr": text("Profiling address", "Where the profiling server listens.", "", { placeholder: "127.0.0.1:6060", mono: true }),

	"management.secret-key": { kind: "secret", label: "Management key", hint: "The key for the management API, stored as plain text or a bcrypt hash." },
	"management.allow-remote": on("Allow from other machines", "Accept management API calls from other machines, not only this one."),
	"management.disable-control-panel": { kind: "bool", invert: true, label: "Serve the legacy control panel", hint: "The engine's original panel at /management.html.", default: false },
	"management.disable-auto-update-panel": { kind: "bool", invert: true, label: "Update the legacy panel", hint: "Check GitHub for control panel updates in the background.", default: false },
	"management.panel-github-repository": text("Panel updates from", "A GitHub repository URL, or its releases API address.", "", { placeholder: "https://github.com/router-for-me/Cli-Proxy-API-Management-Center", mono: true }),

	"plugins.enabled": on("Load plugins", "Plugins in the folder below run inside the engine."),
	"plugins.dir": text("Plugin folder", "Relative paths start at the engine's working directory.", "plugins", { mono: true }),
	"plugins.store-sources": { kind: "list", label: "Extra plugin stores", hint: "Store addresses, on top of the official one.", placeholder: "https://example.com/plugins.json" },

	"quota-exceeded.switch-project": on("Switch project", "Move to another Google Cloud project when one runs out."),
	"quota-exceeded.switch-preview-model": on("Switch to a preview model", "Fall back to a preview model when the main one runs out."),

	"credentials.in-flight.snapshot-interval": dur("Record running requests every", "How often running requests are recorded.", "2s"),
	"credentials.in-flight.stale-after": dur("A silent request counts as gone after", "When a request that stopped reporting is dropped.", "10s"),
};

// Hints for settings that repeat under many entries, keyed by their last key.
export const LEAF_HINTS = {
	"api-key": "Kept secret; use Change… to replace it.",
	"base-url": "The provider's API address.",
	"proxy-url": "Send this key's requests through a proxy, instead of the global one.",
	prefix: "Put this name in front of the models it serves, for example team/kimi-for-coding.",
	priority: "Higher is preferred when several match.",
	weight: "Share of requests under weighted routing. Default 1; 0 or less leaves it out.",
	disabled: "Keep it configured, but never route to it.",
	"excluded-models": "Models this entry never serves.",
	"disable-cooling": "Override the global rule for resting this after errors.",
	"request-retry": "Override the global retry rounds. Negative uses the global value; 0 means no extra rounds.",
	"request-scoped-errors": "Provider errors that fail only the one request (stop) or keep going (continue), instead of resting the account.",
	"sensitive-words": "Words disguised with invisible characters before they are sent.",
};

export const LEAF_ENUMS = {
	action: ["stop", "stop-and-cooldown", "continue", "continue-and-cooldown"],
	"client-request-id": ["on", "off", "passthrough"],
	dialect: ["chat-completions", "responses", "messages", "generate-content"],
};

export const LABELS = {
	"secret-key": "Management key",
	"openai-compatibility": "OpenAI-compatible providers",
	"auth-dir": "Token folder",
	"ws-auth": "Require a key for WebSocket",
	"api-key": "API key",
	"base-url": "Base URL",
	"request-scoped-errors": "Request-only errors",
	"in-flight": "Running requests",
	concurrency: "Concurrency",
	discovery: "Network discovery",
	tls: "HTTPS",
	retry: "Retries",
	cooldown: "Rests after errors",
	streaming: "Streaming",
	logs: "Logs",
	usage: "Usage",
	pprof: "Profiling",
};
