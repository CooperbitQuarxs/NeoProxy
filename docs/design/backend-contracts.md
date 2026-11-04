# Backend contracts the NeoProxy dashboard assumes

The dashboard at `/neo/` was designed ahead of the backend. Its product pages initially used an in-memory fixture (now the routing test oracle at `internal/neoroute/testdata/fixture.js`) behind one client. This document specifies the real backend in the existing Go CLIProxyAPI-derived engine. NeoProxy owns provider accounts, credentials and refresh. NeoPi is an inference client, not the credential source. The authorized implementation and delivery status are tracked in `backend-plan.md`. Where the dashboard makes a decision the backend must honour, it is marked **must**. Where it only displays what it is given, the field is marked *display*.

Production product pages must use the real engine adapter; an unavailable backend is an error, not a fallback to demo accounts or generated requests. `fixture.js` remains importable only as the routing reference oracle. A source chip is display metadata, not proof of a live implementation or an authorization boundary. The delivery plan records whether the actual endpoint and browser checks have passed.

The existing management API (`/v8/management/...`) remains the engine administration interface. Organization-admin sessions may use its dashboard operations; member and account-owner sessions must not acquire management-secret authority. Existing operator management keys remain maintenance credentials, not ordinary dashboard logins or per-user inference keys. The first supported inference protocols are OpenAI Chat Completions, OpenAI Responses and Anthropic Messages.

## Vocabulary

| Term | Meaning |
|---|---|
| **Core** | The running NeoProxy Go engine. It owns provider login, credential storage, refresh and inference execution. |
| **Provider** | A credential family supported by the proxy: `codex`, `anthropic`, `xai`, `zai`, `cerebras`, `github`, `gemini`, … |
| **Account** | One proxy-managed signed-in identity or API credential for a provider, with an owning user and its own quota windows. A provider may have several. |
| **Window** | A provider quota period for one account: `5h`, `day`, `week`, `month`. Usage is a fraction `0..1` of that window. |
| **Model** | A provider's model, served as `neoproxy/<provider>/<model>`. |
| **Virtual model** | A name NeoProxy serves that is not a provider's model: `neoproxy/<name>` (`default`, `small`, `heavy`, …). Defined by the admin as a chain. |
| **Stage** | One step of a chain: a model, a pool of accounts, a draw policy, a budget, a window. |
| **Terminal** | What happens when every stage is spent: limit error, queue, or fall through. |
| **User** | A human who signs into NeoProxy with username/password, or a service principal using per-user inference keys. Roles are organization admin (`admin`), account owner (`owner`), `member`, or `service`. |
| **Grant** | A user's access to a virtual model, with an allotment that scales every budget in that chain. |
| **Direct access** | A user's permission to ask for `neoproxy/<provider>/<model>` by name, bypassing virtual models. Off by default for members. |
| **Allotment** | A per-grant multiplier `0..1` applied to every stage budget in a chain. `0.5` means half the sips and half the cap. |

## Naming rules

- Consumer inference keys use model ids beginning with `neoproxy/` on the three day-one inference endpoints. The prefix means "served by NeoProxy"; downstream (NeoPi, NeoDeck, NeoCode) treats `neoproxy` as one provider. Existing configured engine/operator keys retain their existing selectors and protocols; new consumer keys cannot use unsupported legacy surfaces.
- `neoproxy/<provider>/<model>` is a provider model. The provider segment is the proxy engine's provider id.
- `neoproxy/<name>` with exactly one segment after the prefix is a virtual model. Names are `^[a-z][a-z0-9-]{0,31}$` and **must** not collide with a provider id (the dashboard refuses a name that equals a served model id; the backend should refuse one that equals a provider id too).
- `GET /v1/models` **must** list, for the calling key's user: every granted virtual model first, then every provider model the user has direct access to and that is served (provider exposed and model exposed). Members with no direct access see only `neoproxy/<name>` entries.

## Authentication and sessions

The dashboard uses username/password login with server-side sessions. Provider account credentials, dashboard sessions, user inference keys and operator management secrets are separate authorities.

```
POST /v8/session
  { handle: string, password: string }
→ 200 { role: "admin" | "owner" | "user", userId, handle, expiresAt }
  Set-Cookie: neoproxy_session=<opaque session>; HttpOnly; SameSite=Strict; Path=/
→ 401 { error: { message } }   // invalid login; do not disclose which field failed
→ 403 { error: { message } }   // user suspended or not yet active

GET /v8/session
→ 200 { role, userId, handle, expiresAt }
→ 401 { error: { message } }

DELETE /v8/session
→ 204                         // revoke the server-side session and clear its cookie

POST /v8/session/bootstrap
  Authorization: Bearer <operator management secret>
  { handle: string, name?: string, password: string }
→ 201 { role: "admin", userId, handle, expiresAt } + session cookie
→ 409 { error: { message } }   // bootstrap already completed
```

Password verifiers and session-token hashes are stored in SQLite; plaintext passwords and session tokens are never returned in user records or logs. Sessions expire and are revoked on logout. Password changes and suspension invalidate existing access. The cookie is `Secure` when HTTPS is used. Initial local HTTP operation must not pretend to provide transport confidentiality; HTTPS deployment is a later task. State-changing cookie-authenticated requests must enforce same-origin/CSRF protection.

Session origin, cookie security and the core endpoint scheme use the actual
accepted transport. The shared HTTP/RESP multiplexer exposes TLS connection state
only for encrypted connections; plaintext wrappers must not advertise an empty
TLS state to Go's HTTP server. Same-origin checks remain strict for both schemes.

Sessions last 12 hours without sliding renewal. A `service` principal cannot sign
into the dashboard. Session responses map `member` to the dashboard role `user`;
user records retain `member`. Passwords are 12–72 UTF-8 bytes. Organization-admin
user creation/update accepts a write-only `password` field. Self-service password
changes use `POST /v8/neo/users/:id/password { currentPassword, password }`;
administrators may reset another user's password without its current value.
Changing a password revokes all existing sessions and inference keys. Suspension
revokes sessions but retains keys, which are rejected while the user is suspended.

Every dashboard mutation, including login/bootstrap/logout, requires an exact
same-origin `Origin` header or, for clients omitting `Origin`,
`X-Neoproxy-CSRF: 1`. Cross-site fetch metadata is rejected. The header is not an
authentication credential; cookie authority and current server-side roles are
still checked. An inference key cannot register a `/v1/ws` backend worker, mint a
realtime client secret, or enter management/plugin administration.

The first organization admin is provisioned through an explicit bootstrap operation guarded by the existing operator management authority, not open registration or a default password. Bootstrap must not permit creation of another organization admin once setup has completed. Username/password sessions are the only ordinary dashboard login; an inference key does not become a dashboard session.

A user inference key has format `nk_<48 hex>`. The dashboard displays `nk_xxxx…xxxx`; the server stores only its hash and rejects revocation immediately. Every request and `/v1/models` listing is restricted to that user's current grants and any narrower account scope on the key. Owners may mint keys over their own account pools, never another owner's pool.

Responses also accepts authenticated WebSocket upgrades at `GET /v1/responses`
and `GET /backend-api/codex/responses`. Consumer sockets retain the scoped
principal and normalize each `response.create` or `response.append` continuation
through the same virtual-model resolver as HTTP inference. Current key validity,
grants and narrower account scope are checked for every generating turn; opening
a socket does not freeze authority or expose the engine's global model catalogue.
The bridge retains conversation history so each turn may select a currently
authorized account without depending on a previous account's response ID.
Selected Codex accounts still use their configured upstream WebSocket transport;
consumer frames do not enter direct Codex duplex passthrough. Existing
engine/operator-key native and duplex socket paths are unchanged.

An owner key must name only accounts currently owned by that user. A foreign,
missing, mixed-ownership or unrestricted scope is rejected with HTTP 403
`neoproxy_forbidden`, without creating a key. Unexpected database errors remain
server errors rather than being mislabeled as authorization denials.

Organization admins manage users and organization-wide policy. Account owners manage their own provider accounts and pool-scoped keys, and see only statistics for their owned accounts. Ownership is not organization-admin authority. Member/service access is scoped to their grants, own user and requests regardless of supplied query parameters.

All routes below live under `/v8/neo/` unless stated. Errors are `{ error: { message: string, code?: string } }` with the appropriate status. Timestamps are epoch milliseconds. Fractions are `0..1` floats.

## Core

```
GET /v8/neo/core
→ {
    linked: boolean,
    endpoint: string,        // this proxy's inference endpoint, e.g. "http://127.0.0.1:8317"
    identity: string,        // this proxy's instance identity, e.g. "neoproxy@localhost"
    version: string,         // running NeoProxy engine version
    host: string,            // short host name shown in the topbar chip
    heartbeatAt: number,     // last successful heartbeat
    scopes: string[]         // authenticated session capabilities, e.g. "models:read", "usage:read"
  }
```

`linked` reports that the dashboard is connected to its authoritative NeoProxy engine. It does not mean a connection to a NeoPi credential broker. Core status must be real process/status data, not the old fixture's claimed NeoPi identity.

## Vault

```
GET /v8/neo/vault
→ { providers: Provider[], accounts: Account[] }

Provider {
  id: string,              // "codex"
  name: string,            // "Codex"
  vendor: string,          // "OpenAI"       display
  kind: "oauth" | "api-key",
  exposed: boolean         // served to users at all
}

Account {
  id: string,              // stable, opaque: "codex-a"
  providerId: string,
  ownerId: string,          // owning NeoProxy user; only org admins may reassign another user's account
  label: string,           // masked identity: "shayna@…", "key …7f3a". NEVER a full email or key.
  plan: string,            // "Pro", "Max 20×"   display
  state: "healthy" | "cooldown" | "expired" | "disabled",
  cooldownUntil?: number,  // when state is cooldown
  windows: Window[]
}

Window { kind: "5h" | "day" | "week" | "month", used: number /*0..1*/, resetsAt: number }
```

```
PATCH /v8/neo/vault/providers/:id   { exposed: boolean }
```

Hiding a provider **must** remove every one of its models from every user, including direct access, and make every stage that targets it unservable (the chain skips it as `unhealthy`).

Account health, credentials, refresh and quota-window observations come from the proxy's existing provider managers and login/API-key flows. Each account has a persisted `ownerId`. Provider tokens stay in the engine's existing credential store; ownership, exposure, users, sessions, keys, grants, routing and usage state live in SQLite under the engine's data directory. Missing quota measurements must remain unknown or explicitly configured; they must not be invented as zero use.

### Access extras (pool page)

```
POST /v8/neo/access/:id/test            { model?, prompt? } → { ok, model, latencyMs, status?, reply?, error?, checkedAt }
POST /v8/neo/access/:id/quota/refresh   → { item }   (409: provider has no usage endpoint; 502: provider did not answer)
AccessItem.lastProbe?  { ok, latencyMs, checkedAt, error? }
AccessItem.providerWindows[] / Account.providerWindows[]  Window{ kind, known, source: "provider-reported", unit: "percent", capacity: 100, used, startsAt, resetsAt }
```

`test` sends one real request pinned to that account's credential (admin or the owning owner only), budget-free; it lifts the probed model's cooldown first so the request reaches the provider. Trail steps carry `metered` (count of metered accounts in the average) and candidates carry `metered` and `window` so the dashboard can say "no meter".

## Catalogue

```
GET /v8/neo/models
→ Model[]

Model {
  id: string,              // "<provider>/<model>", e.g. "codex/gpt-6-astra"
  providerId: string,
  name: string,            // "gpt-6-astra"
  display: string,         // "GPT-6 Astra"
  served: string,          // "neoproxy/codex/gpt-6-astra"
  tags: string[],          // free-form: "reasoning", "fast", "code", "vision", "voice", … display
  context: number,         // context window in tokens; 0 if not applicable
  exposed: boolean
}

PATCH /v8/neo/models/:id   { exposed: boolean }    // :id is the url-encoded "<provider>/<model>"
```

The catalogue is derived from the proxy engine's registered provider models and accounts. `exposed` is the admin's switch and is persisted by the proxy. NeoPi discovers the authorized served catalogue through `/v1/models` using a user inference key.

## Virtual models

```
GET    /v8/neo/virtual-models            → VirtualModel[]
GET    /v8/neo/virtual-models/:id        → VirtualModel
GET    /v8/neo/virtual-models/draws      → { draws: DrawMode[], outOfScope: { omniroute, reason }[] }
POST   /v8/neo/virtual-models            VirtualModel (without id) → 201 VirtualModel
PUT    /v8/neo/virtual-models/:id        VirtualModel → VirtualModel
DELETE /v8/neo/virtual-models/:id        → 204   (also removes every grant that references it)

VirtualModel {
  id: string,              // opaque, "vm-heavy"
  name: string,            // "heavy"
  served: string,          // "neoproxy/heavy" (derived; server fills it)
  description: string,     // one sentence shown to users
  stages: Stage[],         // ordered; at least one
  terminal: Terminal,
  script?: string,         // optional admin-authored routing source; absent = standard chain
  updatedAt: number,
  updatedBy: string        // handle
}

Stage {
  id: string,              // stable within the virtual model; the dashboard generates "s<rand>" for new ones
  model: string,           // "<provider>/<model>"
  accounts: "all" | string[],   // account ids within that provider; "all" follows the pool as it changes
  draw: DrawMode.id,       // see GET /virtual-models/draws; legacy "fill" = "priority", "alternate" = "round-robin"
  weights?: { [accountId]: number },  // "weighted" draw: finite ≥ 0; missing = 1; all zero = uniform
  fitContext?: boolean,   // skip the stage ("context") when request tokens exceed the model's own window (an imposed params.context window is always enforced)
  perAccount: number,      // 0..1: draw from an account while its window usage is below this
  accountLimits?: { [accountId]: number },  // per-account override of perAccount (0..1]
  cap: number,             // 0..1: skip the stage once the pool's AVERAGE usage reaches this
  window: "5h" | "day" | "week" | "month",
  per: "user" | "all",     // local token meters only: count this user's usage or everyone's
  follow?: string,         // glob such as "codex/gpt-*-luna": the stage's model is the newest exposed model matching it
                           // (numbers in the id compared as a version tuple, ties by id; `*` does not cross "/");
                           // `model` is then the currently resolved model, filled in by the server on every read
  params?: { [paramId]: ParamSetting },   // the model's always-set properties, see "Parameters" below
  lua?: {                 // named functions defined in VirtualModel.script
    eligible?: string,    // function(ctx) -> boolean, per authorized account
    perAccount?: string,  // function(ctx) -> finite 0..1 window fraction
    cap?: string          // function(ctx) -> finite 0..1 stage fraction
  }
}

Terminal {
  kind: "limit" | "queue" | "passthrough",
  message: string,         // returned to the caller on limit (and while queued)
  model?: string           // for passthrough: "<provider>/<model>" served with no budget
}

DrawMode {                 // one Go table, neoroute.DrawModes; validation accepts these ids and aliases
  id: string, label: string, short: string, doc: string,
  omniroute?: string,      // the OmniRoute strategy name(s) it corresponds to
  aliases?: string[],      // older ids that resolve to this mode
  needs: ("meter" | "weights" | "price" | "history")[]
}
```

### Parameters (a virtual model is a model plus properties that are always set)

A virtual model with one stage is "a model and its settings"; the fallback chain is an optional extra. Which settings exist is **derived from the model's discovered facts**, never hard-coded in the dashboard (`internal/neoparams`).

```
GET /v8/neo/model-params?model=<provider/model>     (admin session)
→ { model: string, specs: Spec[] }                   404 for a model outside the inventory

Spec {
  id: string,            // stable: "speed" | "context" | "effort" | "efforts" | "reasoning_summary" | "verbosity" | "temperature" | "top_p" | "max_output"
  label, help: string, group: "Performance" | "Context" | "Reasoning" | "Sampling" | "Output",
  kind: "choice" | "toggle" | "range" | "set" | "number",
  options?: [{ value, label, note?, multiplier? }],     // choice, set
  min, max, step: number,                               // always present for range and number
  default: any,                                         // the model's own behaviour when unset (null = provider default)
  marks?: [{ value, label, note? }],                    // range landmarks: standard window, maximum
  bands?: [{ from, to, multiplier, note? }],            // price bands, only from published facts
  unit?: "tokens",
  modes?: ["force"],                                    // omitted = "force" and "default" both accepted
  realize: string                                       // what it does on the wire, in a sentence
}

ParamSetting { mode: "force" | "default", rules: [{ when?: Condition, value }] }
```

- A parameter is offered only when a fact supports it; no facts, no parameters. Codex models that live discovery describes (`ModelInfo.Codex`) are authoritative: their discovered `service_tiers`, `context_window` and `max_context_window` decide speed and context, so an account without Ultrafast is not offered it; the Codex client catalogue is the fallback for undiscovered models. `speed` (standard/fast/ultrafast): Fast sends `service_tier=priority`, Ultrafast `service_tier=ultrafast` (usage multipliers 2.5× and 8×), or Fast switches to a `<id>-fast` / `<id>-build-fast` sibling model of the same provider. `context` is a range from the model's standard window up to its maximum window (and down to 16k) with the long-context price band where one is published (Codex: ×2 above the standard window; Grok 4.7: ×2 above 256k); it has no wire flag, the router advertises it (`/v1/models` `context_length`) and refuses larger prompts. `effort` and `efforts` (the allowed set) come from the model's reasoning levels. `reasoning_summary` and `verbosity` come from the Codex client catalogue; `temperature`, `top_p` and `max_output` only when `supported_parameters` lists them and discovery does not say the provider strips sampling parameters.
- `force` overrides whatever the caller sent; `default` applies only when the caller sent nothing for that parameter. `context` and `efforts` are always enforced and reject `default`. `efforts`: a caller effort inside the set passes through, anything else (or none) becomes the highest allowed. `effort` wins over `efforts`.
- The first rule whose `when` matches wins; a rule without `when` is the fallback and must come last (400 otherwise). `when` uses the same condition tree and evaluator as stage conditions (`/virtual-models/fields`), restricted to request-scope fields: no `account.*`, no `pool.*`, because parameters are decided before accounts are drawn.
- Validation checks every value against the stage model's specs: parameter exists, kind, option set, range, mode, rules. A stage's `follow` must match at least one exposed model (400 otherwise).
- Schema v3 migrates a stored stage's `effort` to `params.effort` and `efforts` to `params.efforts` (both `force`) and removes the old fields. Decisions recorded before v3 may still show `effort`; nothing reads it.
- **Apply path.** The resolver puts `Decision.params: [{id, value, realize, via}]` on the decision (and `params` on the chosen trail step). `via` is `tier`, `body`, `model`, `suffix` or `window`. `sdk/api/handlers/handlers_neoroute.go › neoPrepared` writes them into the client-protocol body before translation: `service_tier` (Chat, Responses; `service_tier` + `speed:"fast"` for Messages), `text.verbosity` / `verbosity`, `reasoning.summary` (Responses), `temperature`, `top_p`, `max_tokens` / `max_output_tokens`; the reasoning effort is the model suffix as before and a sibling-fast swap is already the stage's served model (`Decision.model`). Translators, the thinking applier, executor built-ins and finally the configured payload rules run after this, so payload rules keep the last word.
- **Context.** `fitContext` and the context parameter use the stage's effective window (the imposed one, else the model's). A stage whose window is too small is skipped (`context`); when every remaining stage is, the request is refused with HTTP 400 `{ error: { code: "context_length_exceeded", type: "invalid_request_error", message } }` (unless the terminal is a passthrough, which still serves). `GET /v1/models` reports a virtual model's `context_length` as its first stage's window (the widest value when conditional) and its fixed properties in `description`.

Validation the backend **must** enforce: name pattern; unique name; not equal to any provider id or served model id; at least one stage; every stage's model exists in the proxy catalogue and belongs to a configured provider; `accounts` ids belong to that provider and satisfy the caller/key's ownership scope; `perAccount ≤ cap` is *not* required. A cap lower than one sip is allowed and simply ends the stage early.

### Usage

```
GET /v8/neo/usage
→ { [userId]: { [virtualModelId]: { [stageId]: { [accountId]: number /*0..1*/ } } } }
```

The fraction of an account's **current** window (the stage's `window` kind) that this user has drawn through this stage. The backend keeps this per `(user, virtualModel, stage, account, window)` and resets it when the account's window resets. For stages with `per: "all"` the backend may still report per-user rows; the dashboard sums them.

This is the one data structure the simulator, the rings, the flow-board ribbons and the member view all read. Get it right and every page is right.

### Resolution (the algorithm)

`internal/neoroute/testdata/fixture.js › resolve()` is the reference implementation. Given a request for `asked` from `user` at time `at`:

0. **Redirect** (see Redirects below): if an enabled redirect matches `asked` for this caller, resolution continues as if the caller had asked for its `to`. One hop only; the decision carries `redirect` and a leading `{ kind: "redirect" }` trail step.
1. Strip `neoproxy/`. If the remainder has one segment it is a virtual model name; otherwise it is `<provider>/<model>`.
2. **Provider model**: the user **must** have `providerId` in `providerAccess`, the model **must** match the user's `modelAccess[providerId]` when that entry exists and the key's `models` when the key has them (else `no-model-access`, 403), the provider and model **must** be exposed, and a healthy account **must** exist. Serve from the first healthy account (the engine's own routing strategy applies here). No budget is applied. Outcome `ok` with `direct: true`. `GET /v1/models` lists exactly the direct models that pass the same checks.
3. **Virtual model**: the user **must** hold a grant; `allot = grant.allotment`.
4. For each stage in order:
   - `pool` = accounts of the stage's provider, filtered by `stage.accounts`.
   - The **meter** of `a` for the stage's window is: a current provider-reported window (`Account.providerWindows`, polled from Claude and Codex every five minutes) if there is one, else the local configured token meter (`Account.windows`), else none. An account with no meter is always a candidate with `used(a) = 0` and is left out of the average.
   - `used(a)` = the provider window's fraction; or, for a local meter, the account-wide fraction (`per: "all"`) or this user's fraction through this stage (`per: "user"`).
   - If the **average** of `used(a)` over metered pool members `≥ stage.cap × allot` → skip (`cap`).
   - `limit(a)` = `stage.accountLimits[a.id]` if present, else `stage.perAccount`. `candidates` = pool members that are `healthy` and have `used(a) < limit(a) × allot`. None → skip (`unhealthy` if any pool member is not healthy, else `per-account`).
   - The ledger re-checks the chosen account inside the reservation transaction with the same meter choice (`Reservation.providerUsed`), so simulation and execution agree.
   - The stage's model is its `follow` match when set. Its parameters are evaluated first (first matching rule per parameter, then force/default against what the caller sent): a sibling-fast speed swaps the model before any account check, and an imposed `context` window skips the stage (`context`) when the prompt exceeds it.
   - Candidates keep the stage's account order: the explicit `accounts` list's order, or account id order for `"all"`. With `fitContext` (or draw `fit-context`) the stage is skipped (`context`) before this when the model's `context` is known and smaller than the request's estimated tokens.
   - Choose by `draw` (the trail step records `draw` and a `drawNote`): `priority` → first candidate (OmniRoute fill-first: an account stays a candidate until its threshold); `round-robin` → durable per-stage cursor; `least-used` → lowest `used`; `headroom` → most `limit − used` (unmetered = ∞); `random` → uniform, never the stage's previous pick; `strict-random` → uniform; `weighted` → random by `weights`; `quota-weighted` → random by headroom (unmetered = 1); `p2c` → two uniform picks, the lower `used`; `reset-soonest` / `reset-latest` → the metered candidate whose window resets soonest / latest (no reset time last); `sticky` → the account that last served the stage successfully, else most headroom, never the account that just failed; `cache-affinity` → the account bound to the hash of the first 2 KiB of system text + opening user message (per-stage LRU of 4096), else most headroom; `cheapest` → plan monthly price ascending, unpriced API keys last, ties to headroom; `fit-context` → most headroom; `auto` → highest 0.5×headroom + 0.3×24h success rate + 0.2×(1 − min-max normalized 24h p50 latency). Ties go to stage order. Sticky, cache-affinity and the random no-repeat memory are in-process and reset when the virtual model is saved.
   - Serve. Record usage against `(user, vm, stage, account)`. Outcome `ok` with `stageIndex`, `stageId`, `model` (the served model: the sibling when speed swaps models), `accountId` and `params` (what the stage's parameters set on this request).
5. No stage served → the terminal:
   - `limit` → HTTP 429, body `{ error: { message: terminal.message, code: "neoproxy_limit", virtualModel, resetsAt } }`. Outcome `limited`.
   - `queue` → hold the request until some stage can serve (bounded by the client's timeout; the backend should send keep-alive on streaming responses). Outcome `queued`.
   - `passthrough` → serve `terminal.model` as in step 2 but without the direct-access check. Outcome `ok` retains `virtualModel`, `model` and `accountId`, but has no `stageId`, `stageIndex` or `direct` flag. It does not draw a stage budget. A missing or hidden target returns `error` with `reason: "unknown-model"`; a target without a healthy account returns `error` with `reason: "no-healthy-account"`. The simulator highlights the terminal and explains the selected model/account or the failure instead of reporting a limit.
6. User-level `limits.rpm` and `limits.tokensPerDay` are hard ceilings checked before step 4; exceeding them is a 429 with `code: "neoproxy_user_limit"`.

The decision **must** be recorded on the request (see Requests › `decision`), including the `trail`, so the Activity page can explain it.

```
POST /v8/neo/simulate   { asked: string, userId: string, keyId?: string, protocol?: "openai" | "openai-response" | "claude", request?: JSON }
→ Decision
```

Pure: the server runs the same resolver against current grants and usage without recording activity, admitting a request, reserving usage or advancing a cursor. The dashboard simulates only saved models and invalidates the result as soon as a draft changes; save before running it again. Optional protocol and credential-free business-request JSON let a configured script inspect representative input. Simulation never grants a hypothetical 100% allotment or runs a local production fixture fallback. `keyId` (one of `userId`'s live keys, else 400) simulates a request made with that inference key: its account scope and key-scoped redirects apply. Redirects apply to simulation exactly as to execution.

Execution reserves a conservative token upper bound against explicitly configured
metered windows before dispatch, then replaces that reservation with the existing
engine reporter's actual account-attributed input/output usage. Text requests use
an explicit protocol output maximum plus input bytes; requests without a usable
bound require a known catalogue context ceiling. Unknown provider quota remains
unknown. An executed attempt with no trustworthy usage report retains an explicitly
estimated upper-bound debit rather than inventing zero usage. Cached tokens are a
subset of input tokens and are not counted twice.

The alternate cursor and ledger reservation commit in one SQLite transaction.
Simulation reads the cursor, current grants and current-window usage, but never
advances the cursor, admits a request, reserves usage or records an activity row.
Queue waits wake on current authority, inventory or ledger changes and reset
boundaries; cancellation is inherited from the client. Queued streams emit SSE
comments while waiting, without replaying any delivered answer.

Actual upstream failure records `skipped: "upstream-error"` and `failureStatus`
on the failed stage and demotes to a later eligible stage before output. Once an
answer chunk is delivered, subsequent errors terminate that stream; they do not
restart the chain. `X-NeoProxy-Request-ID` identifies the persisted request record.

A configured `script` is limited to 16 KiB, requires an installed evaluator, and
cannot silently fall back to the ordinary chain on an evaluator error. A stage's
optional `lua` object names boolean eligibility and numeric allocation/cap
functions in that source. `eligible(ctx)` runs per currently authorized candidate;
false removes that account. `perAccount(ctx)` runs per candidate and `cap(ctx)`
once per stage. Numeric results must be finite fractions in 0..1 (0.05 = 5%),
before grant scaling. Effective limits are `min(static ceiling, Lua result) ×
allotment`; the real ledger reserves these effective limits atomically, including
in-flight usage. Lua cannot restore an excluded account or raise a hard ceiling.

Callback `ctx` is deeply read-only and contains `asked`, `protocol`, `userId`,
`request`, `stage`, `allotment`; account functions also receive `account`,
`window` (the configured local budget window), `used` and `budget` (grant-scaled
stage usage and hard per-account budget). `account.providerWindows` are separate
honest native reports, never local token denominators. Unknown/missing/expired
reports are not unused quota or proof that a vendor has no five-hour window.
There are no credentials, files, network or ambient provider-plan assumptions.

Policy-only chunks define functions without a return. Existing selector chunks
may still return exactly `{stageId, accountId}`; when combined with callbacks,
that selection must survive the reduced eligible set. Static stages retain their
draw order. Wrong types, nonfinite/out-of-range limits, missing functions and
runtime errors fail closed with stage/function diagnostics, rather than trying a
different route. The resolver reloads current authority after evaluation and
uses the same callbacks in simulation, execution and reservation rechecks.
Saving requires an organization-admin dashboard session and validates syntax;
Try it validates behavior against the saved model and selected user's real
grants. Simulation makes no token reservation or promise about a later request's
capacity. Ordinary member views do not expose source.


### Redirects

```
GET    /v8/neo/redirects        → Redirect[]            (admin; oldest first)
POST   /v8/neo/redirects        Redirect → 201 Redirect (admin; 400 { error: { message } } on validation)
PUT    /v8/neo/redirects/:id    Redirect → 200 Redirect (admin; 404 unknown id)
DELETE /v8/neo/redirects/:id    → 204                   (admin)

Redirect {
  id: string,                  // "rd_…", server-assigned
  from: string,                // what callers ask for: "neoproxy/<vm>" or "neoproxy/<provider>/<model>"; may end in "*" (prefix)
  to: string,                  // one existing virtual model or catalogue model, no "*"
  scope: { users?: string[], keys?: string[] },  // empty = everyone; keys = only those inference keys (users is then filled with their owners)
  enabled: boolean,
  bypassAccess: boolean,       // default false; see below
  note: string,                // ≤ 280 chars
  createdAt: number, updatedAt: number
}

Decision.redirect / TrailStep{kind:"redirect"}.redirect = {
  id, from /* the model asked for */, to, pattern? /* the rule's from when it is a prefix */, note?, bypassAccess?
}
```

`from` and `to` are accepted with or without the `neoproxy/` prefix and stored with it. Only asks that carry the `neoproxy/` prefix are matched. For one request the most specific matching enabled redirect wins: a key rule over a person rule over an everyone rule, then an exact `from` over a prefix, then the oldest. Redirects never chain: the target is resolved directly. Validation rejects a rule that redirects to itself (including a prefix matching its own `to`), a rule whose `to` is the `from` of another enabled rule (or whose `from` matches another enabled rule's `to`) in an overlapping scope, and a second enabled rule with the same `from` and scope.

**A redirect never grants access.** The redirected request goes through the target's ordinary checks as if the caller had asked for `to`: a grant for a virtual model, provider/model access for a direct model, key account scope and budgets. **`bypassAccess: true`** (administrators only, since only they can save redirects) is the exception: the target's grant and direct-access checks are skipped, an ungranted virtual model is drawn at a 100% allotment, and account ownership and key account scope still apply.

`GET /v1/models` hides nothing because of a redirect; a model that a caller can reach only through a redirect (its `to` is usable by them, or the rule bypasses access) is listed too, because the caller still asks for it. The request record keeps the caller's `asked`.

## Users

```
GET    /v8/neo/users                → User[]
GET    /v8/neo/users/:id            → User            (member: own id only)
POST   /v8/neo/users                User (without id, keys) → 201 User
PUT    /v8/neo/users/:id            User → User     (keys are not editable here)
DELETE /v8/neo/users/:id            → 204            // admin; last active admin is protected
POST   /v8/neo/users/:id/keys       { label, accountIds?: string[], models?: string[], expiresAt?: number } → 201 { key: Key, secret: string }   // secret shown once
DELETE /v8/neo/users/:id/keys/:keyId → 204

User {
  id: string,              // "u-santiago"
  handle: string,          // "santiago"  ^[a-z][a-z0-9-]{1,31}$  unique
  name: string,
  role: "admin" | "owner" | "member" | "service",
  state: "active" | "invited" | "suspended",
  keys: Key[],
  providerAccess: string[],    // provider ids with direct access
  modelAccess?: { [providerId]: string[] },  // narrows direct access per provider; see below
  grants: Grant[],
  limits: { rpm: number | null, tokensPerDay: number | null },
  createdAt: number
}
Key   { id: string, prefix: string /*"nk_xxxx…xxxx"*/, label: string, accountIds?: string[] /*narrower scope*/, models?: string[] /*narrower direct models*/, createdAt: number, lastUsedAt: number | null, expiresAt?: number }
Grant { virtualModel: string /*VirtualModel.id*/, allotment: number /*0..1*/ }
```

- **Model patterns.** A pattern is a model id or a prefix ending in one `*` (`gpt-5*`, `*`); no whitespace, at most 200 bytes, 1 to 2000 distinct patterns per list. `modelAccess[p]` patterns match the model id without its `<p>/` prefix; key `models` match the full `<provider>/<model>` id.
- `modelAccess`: a provider in `providerAccess` without an entry serves every exposed model, including ones added later; with an entry only the matching ones. An entry must not be empty (turn the provider off instead). Entries for providers outside `providerAccess` are dropped on save. On `PUT`, omitting `modelAccess` keeps the stored allowlist; `{}` clears it.
- Key `models` narrows the key's direct models further and never widens the user's; omitted or `[]` means no narrowing. Virtual models are governed by grants alone.

- `invited` users have no keys; issuing a key **should** move them to `active`.
- `suspended` users keep their keys; every request with them fails 403 `code: "neoproxy_suspended"`.
- An `admin` user's username/password session opens the organization administration dashboard. An `owner` session manages only its own accounts, scoped keys and account statistics. A `service` user is a machine such as NeoDeck or CI and uses inference keys. No user's inference key is an organization-admin dashboard session.

## Requests

```
GET /v8/neo/requests?limit=&userId=&outcome=&asked=&before=   → Request[]   (newest first; member: own only)
GET /v8/neo/requests/:id                                       → Request

Request {
  id: string,              // "req_3f02"
  at: number,
  userId: string,
  keyId: string | null,
  client: string,          // best-effort from User-Agent / X-Client: "npi", "NeoDeck", "NeoCode", "curl"
  asked: string,           // the model id as sent
  decision: Decision,
  tokens: { in: number, out: number, cached: number },
  latencyMs: number,
  outcome: "ok" | "limited" | "queued" | "error",
  stream: boolean
}

Decision {
  outcome: "ok" | "limited" | "queued" | "error",
  direct?: true,
  virtualModel?: string, stageIndex?: number, stageId?: string,
  model?: string, accountId?: string,
  params?: [{ id: string, value: any, realize: string, via?: "tier" | "body" | "model" | "suffix" | "window" }],
  reason?: "unknown-model" | "no-provider-access" | "no-healthy-account" | "not-granted" | "limit" | "queue" | "context_length_exceeded",
  message?: string,
  trail: TrailStep[]
}
TrailStep =
  | { kind: "stage", index, stageId, model, params?: Applied[], total, cap, candidates: [{accountId, used, budget}], chosen?: accountId, skipped?: "cap" | "per-account" | "unhealthy" | "context" | "upstream-error" | "script" | "lua-policy" | "lua-cap", failureStatus?: number, policy?: {stageId, cap?: number, accounts: [{accountId, eligible: boolean, limit?: number}]} }
  | { kind: "terminal", terminal: Terminal }
  | { kind: "direct", model, accountId }
  | { kind: "denied", providerId? , virtualModel? }
  | { kind: "no-model", asked }
```

A live stream for the Overview ticker is wanted but not assumed: `GET /v8/neo/requests/stream` as SSE emitting `Request` objects. Until it exists the dashboard polls `GET /v8/neo/requests?limit=14`.

## What the dashboard never asks for

- Full emails, full keys, raw tokens, auth-file contents. `Account.label` and `Key.prefix` are already masked server-side.
- Provider pricing or guessed cost. Quota fractions, token counts and monetary cost are distinct. Proxy-owned usage attribution and per-owner statistics come from its own ledger; a NeoPi client's local `/stats` is not the authoritative account ledger.
- Anything from a member session beyond their own user, their own requests, the catalogue, the virtual models they are granted, and the core's `linked` flag.

## How this maps onto NeoPi

NeoProxy is the account, credential and refresh authority. Users log provider accounts into the existing Go engine directly through its provider login flows or API-key configuration. NeoPi connects as an inference client with a scoped user API key and discovers `neoproxy/<name>` entries from `/v1/models`. A NeoPi model role may select one of those virtual models; that client-side role is separate from the proxy's admin/owner/member roles and from the server-side virtual-model chain.

The Go engine is retained for this delivery; there is no rewrite into NeoPi and no dependency on a NeoPi core socket, vault export or credential broker. Proxy-owned state is persisted in SQLite under the engine's data directory. The local end-to-end proof uses a scratch `PI_CODING_AGENT_DIR`, assigns a scratch NeoPi role to a discovered virtual model and proves a real role-selected turn with first-stage failure and fallback success over all three day-one protocols. It must never alter the owner's NeoPi profile.

## Identity persistence and build requirements

`NEOPROXY_DATA_DIR` selects the engine-owned application data directory. Otherwise
an engine with a config file uses `data/neoproxy.sqlite` beside the absolute
config-file path. Provider credential/auth directories are not application state
directories. Empty-config SDK construction does not create a database unless the
environment explicitly selects one or the caller injects `api.WithNeoStore`.
Injected stores remain the caller's responsibility to close. Normal servers
close their own store on shutdown and report initialization errors from `Start`.

Server attachment authenticates reserved `nk_` credentials before dynamic routes.
Only the three day-one inference endpoints and `/v1/models` accept these consumer
principals. Until the scoped catalogue and routing implementation is attached,
they return `503 neoproxy_unavailable`; they never fall through to legacy raw-model
execution or an unfiltered catalogue. Integration calls
`Server.EnableNeoInference()` only after both real scoped paths are installed.
Existing configured engine keys keep their existing inference authority, separate
from dashboard sessions. `Store.Changes()` broadcasts committed identity-policy
changes so queued resolvers can wake and recheck `RefreshPrincipal`; extensions
may call `NotifyChanges()` after committing ownership changes.

Identity uses `github.com/mattn/go-sqlite3 v1.14.52`, requiring `CGO_ENABLED=1`
and a C compiler. The module is pinned to upstream commit
`b0be46fa28d17ee0b65c79774ac0dad84b6db068` (2026-09-05), with Go sum
`h1:wVbm2Qnf4OXkqhBTSPuCRZDRnxfbVrrmiCEroVdog8U=`. Its bundled SQLite is
3.53.4, matching the 2026-07-24 source ID published in SQLite's
[release history](https://www.sqlite.org/changes.html). This includes the
WAL-reset corruption fix absent from the initially inspected 3.50.4 bundle.
The [upstream advisory page](https://github.com/mattn/go-sqlite3/security/advisories)
listed no published advisories at intake.

Before compiling the dependency, inspection covered its module manifest
(no dependency graph or install hooks), CGO build directives, driver registration,
connection initialization/DSN handling, statement preparation/execution, and
extension loading. No process/network execution was found in the inspected Go
surfaces. This is targeted source vetting, not a line-by-line audit of the bundled
SQLite amalgamation or a claim that it has no defects. Application SQL is fixed
and parameterized; callers cannot supply DSNs, SQL, extensions or database files.
The driver is registered without extension loaders or connection hooks. The
database is mode `0600`, newly created data directories are `0700`, foreign keys
are enabled on every connection, and migrations run in immediate transactions.
Account inventory and routing extend this same `neo.Store.DB()` database.
