# Backend contracts the NeoProxy dashboard assumes

The dashboard at `/neo/` was designed ahead of the backend. Everything NeoPi-centred (the core link, the vault, the served catalogue, virtual models, users, the request stream) runs today against an in-memory fixture (`internal/neoui/static/lib/fixture.js`) behind one client (`lib/client.js`). This document is the contract that fixture implements, written for whoever builds the real thing (Astra or Opus, per the owner). Where the dashboard makes a decision the backend must honour, it is marked **must**. Where the dashboard only displays what it is given, the field is marked *display*.

Pages that are fixture-backed show a quiet `preview data` chip next to their title. The chip is driven by `sources` in `lib/client.js`; flipping a surface from `"fixture"` to `"engine"` there, and pointing its methods at the real routes, is the whole frontend cut-over.

The engine's existing management API (`/v8/management/...`) is unchanged and is used as-is by the Engine pages (credentials, access keys, logs, routing, config, plugins).

## Vocabulary

| Term | Meaning |
|---|---|
| **Core** | The NeoPi process that holds every provider credential (its auth broker/gateway). NeoProxy never stores a provider token of its own while linked. |
| **Provider** | A credential family the core holds: `codex`, `anthropic`, `xai`, `zai`, `cerebras`, `github`, `gemini`, … |
| **Account** | One signed-in identity for a provider, with its own quota windows. A provider may have several (the multi-account case). |
| **Window** | A provider quota period for one account: `5h`, `day`, `week`, `month`. Usage is a fraction `0..1` of that window. |
| **Model** | A provider's model, served as `neoproxy/<provider>/<model>`. |
| **Virtual model** | A name NeoProxy serves that is not a provider's model: `neoproxy/<name>` (`default`, `small`, `heavy`, …). Defined by the admin as a chain. |
| **Stage** | One step of a chain: a model, a pool of accounts, a draw policy, a budget, a window. |
| **Terminal** | What happens when every stage is spent: limit error, queue, or fall through. |
| **User** | A human or service that signs in with a NeoProxy key. Role `admin`, `member`, or `service`. |
| **Grant** | A user's access to a virtual model, with an allotment that scales every budget in that chain. |
| **Direct access** | A user's permission to ask for `neoproxy/<provider>/<model>` by name, bypassing virtual models. Off by default for members. |
| **Allotment** | A per-grant multiplier `0..1` applied to every stage budget in a chain. `0.5` means half the sips and half the cap. |

## Naming rules

- Every model id the proxy accepts on `/v1`, `/v1/messages`, `/v1beta`, `/v1/responses` starts with `neoproxy/`. The prefix means "served by NeoProxy"; downstream (NeoPi, NeoDeck, NeoCode) treats `neoproxy` as one provider.
- `neoproxy/<provider>/<model>` is a provider model. The provider segment is the core's provider id.
- `neoproxy/<name>` with exactly one segment after the prefix is a virtual model. Names are `^[a-z][a-z0-9-]{0,31}$` and **must** not collide with a provider id (the dashboard refuses a name that equals a served model id; the backend should refuse one that equals a provider id too).
- `GET /v1/models` **must** list, for the calling key's user: every granted virtual model first, then every provider model the user has direct access to and that is served (provider exposed and model exposed). Members with no direct access see only `neoproxy/<name>` entries.

## Authentication and sessions

Two kinds of bearer token reach the dashboard's routes:

- The engine's management secret (`management.secret-key`). It is the admin session. The dashboard treats any key that passes `GET /v8/management/config` as admin.
- A **user key**, format `nk_<48 hex>`. The dashboard shows the first and last four hex characters as `nk_xxxx…xxxx` (`Key.prefix`). The backend stores only a hash and **must** reject a revoked key immediately.

```
POST /v8/session
  Authorization: Bearer <management secret | user key>
→ 200 { role: "admin" | "user", userId, handle }
→ 401 { error: { message } }   // unknown key
→ 403 { error: { message } }   // user suspended or invited (no key should exist yet)
```

`role: "user"` sessions may call only the routes marked **member** below, and the backend **must** scope them to their own user id regardless of query parameters.

All routes below live under `/v8/neo/` unless stated. Errors are `{ error: { message: string, code?: string } }` with the appropriate status. Timestamps are epoch milliseconds. Fractions are `0..1` floats.

## Core

```
GET /v8/neo/core
→ {
    linked: boolean,
    endpoint: string,        // "unix:///run/user/1000/npi/core.sock" or "tls://host:port"
    identity: string,        // what this proxy authenticates as at the core, e.g. "neoproxy@utopia"
    version: string,         // NeoPi version reported by the core
    host: string,            // short host name shown in the topbar chip
    heartbeatAt: number,     // last successful heartbeat
    scopes: string[]         // what the core granted: "vault:read", "usage:read", "config:write", …
  }
```

When `linked` is false the dashboard shows a warning on Overview and Core link and expects the engine's local credential files to be serving instead. Nothing else changes shape.

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

Account state and windows come from the core (NeoPi's auth broker already tracks usage windows per credential). The proxy does not refresh tokens itself while linked.

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

The catalogue is derived from what the core reports per provider (NeoPi's model catalog). `exposed` is the admin's switch and is persisted by the proxy.

## Virtual models

```
GET    /v8/neo/virtual-models            → VirtualModel[]
GET    /v8/neo/virtual-models/:id        → VirtualModel
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
  updatedAt: number,
  updatedBy: string        // handle
}

Stage {
  id: string,              // stable within the virtual model; the dashboard generates "s<rand>" for new ones
  model: string,           // "<provider>/<model>"
  accounts: "all" | string[],   // account ids within that provider; "all" follows the pool as it changes
  draw: "alternate" | "least-used" | "fill",
  perAccount: number,      // 0..1 of each account's window this stage may draw
  cap: number,             // 0..1 total across the stage's accounts
  window: "5h" | "day" | "week" | "month",
  per: "user" | "all",     // whether the budget is per granted user or shared by all
  effort: null | "low" | "medium" | "high" | "max"   // force the reasoning effort; null = caller's choice
}

Terminal {
  kind: "limit" | "queue" | "passthrough",
  message: string,         // returned to the caller on limit (and while queued)
  model?: string           // for passthrough: "<provider>/<model>" served with no budget
}
```

Validation the backend **must** enforce (the dashboard enforces the same client-side): name pattern; unique name; not equal to any provider id or served model id; at least one stage; each stage's model exists and its provider is held by the core; `accounts` ids belong to that provider; `perAccount ≤ cap` is *not* required (a cap lower than one sip is allowed and simply ends the stage early).

### Usage

```
GET /v8/neo/usage
→ { [userId]: { [virtualModelId]: { [stageId]: { [accountId]: number /*0..1*/ } } } }
```

The fraction of an account's **current** window (the stage's `window` kind) that this user has drawn through this stage. The backend keeps this per `(user, virtualModel, stage, account, window)` and resets it when the account's window resets. For stages with `per: "all"` the backend may still report per-user rows; the dashboard sums them.

This is the one data structure the simulator, the rings, the flow-board ribbons and the member view all read. Get it right and every page is right.

### Resolution (the algorithm)

`internal/neoui/static/lib/fixture.js › resolve()` is the reference implementation. Given a request for `asked` from `user` at time `at`:

1. Strip `neoproxy/`. If the remainder has one segment it is a virtual model name; otherwise it is `<provider>/<model>`.
2. **Provider model**: the user **must** have `providerId` in `providerAccess`, the provider and model **must** be exposed, and a healthy account **must** exist. Serve from the first healthy account (the engine's own routing strategy applies here). No budget is applied. Outcome `ok` with `direct: true`.
3. **Virtual model**: the user **must** hold a grant; `allot = grant.allotment`.
4. For each stage in order:
   - `pool` = accounts of the stage's provider, filtered by `stage.accounts`.
   - `used(a)` = the usage fraction for this stage and account, per user (`per: "user"`) or summed over all users (`per: "all"`).
   - If `Σ used(a) ≥ stage.cap × allot` → skip (`cap`).
   - `candidates` = pool members that are `healthy` and have `used(a) < stage.perAccount × allot`. None → skip (`unhealthy` if any pool member is not healthy, else `per-account`).
   - Choose: `fill` → first candidate in pool order; `least-used` → the candidate with the lowest `used`; `alternate` → rotate through candidates on successive requests (the fixture rotates by wall-clock second; the backend should keep a per-stage cursor).
   - Serve. Record usage against `(user, vm, stage, account)`. Apply `effort` if set. Outcome `ok` with `stageIndex`, `stageId`, `model`, `accountId`.
5. No stage served → the terminal:
   - `limit` → HTTP 429, body `{ error: { message: terminal.message, code: "neoproxy_limit", virtualModel, resetsAt } }`. Outcome `limited`.
   - `queue` → hold the request until some stage can serve (bounded by the client's timeout; the backend should send keep-alive on streaming responses). Outcome `queued`.
   - `passthrough` → serve `terminal.model` as in step 2 but without the direct-access check. Outcome `ok` retains `virtualModel`, `model` and `accountId`, but has no `stageId`, `stageIndex` or `direct` flag. It does not draw a stage budget. A missing or hidden target returns `error` with `reason: "unknown-model"`; a target without a healthy account returns `error` with `reason: "no-healthy-account"`. The simulator highlights the terminal and explains the selected model/account or the failure instead of reporting a limit.
6. User-level `limits.rpm` and `limits.tokensPerDay` are hard ceilings checked before step 4; exceeding them is a 429 with `code: "neoproxy_user_limit"`.

The decision **must** be recorded on the request (see Requests › `decision`), including the `trail`, so the Activity page can explain it.

```
POST /v8/neo/simulate   { asked: string, userId: string }
→ Decision
```

Pure: runs the algorithm against current usage without recording anything. The dashboard runs it locally today; the backend route exists so the two cannot drift.

## Users

```
GET    /v8/neo/users                → User[]
GET    /v8/neo/users/:id            → User            (member: own id only)
POST   /v8/neo/users                User (without id, keys) → 201 User
PUT    /v8/neo/users/:id            User → User     (keys are not editable here)
POST   /v8/neo/users/:id/keys       { label } → 201 { key: Key, secret: string }   // secret shown once
DELETE /v8/neo/users/:id/keys/:keyId → 204

User {
  id: string,              // "u-santiago"
  handle: string,          // "santiago"  ^[a-z][a-z0-9-]{1,31}$  unique
  name: string,
  role: "admin" | "member" | "service",
  state: "active" | "invited" | "suspended",
  keys: Key[],
  providerAccess: string[],    // provider ids with direct access
  grants: Grant[],
  limits: { rpm: number | null, tokensPerDay: number | null },
  createdAt: number
}
Key   { id: string, prefix: string /*"nk_xxxx…xxxx"*/, label: string, createdAt: number, lastUsedAt: number | null }
Grant { virtualModel: string /*VirtualModel.id*/, allotment: number /*0..1*/ }
```

- `invited` users have no keys; issuing a key **should** move them to `active`.
- `suspended` users keep their keys; every request with them fails 403 `code: "neoproxy_suspended"`.
- An `admin` user's key also opens the dashboard as admin. A `service` user is a machine (NeoDeck, CI); the dashboard shows it with a different avatar and nothing else differs.

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
  model?: string, accountId?: string, effort?: string | null,
  reason?: "unknown-model" | "no-provider-access" | "no-healthy-account" | "not-granted" | "limit" | "queue",
  message?: string,
  trail: TrailStep[]
}
TrailStep =
  | { kind: "stage", index, stageId, model, total, cap, candidates: [{accountId, used, budget}], chosen?: accountId, skipped?: "cap" | "per-account" | "unhealthy" }
  | { kind: "terminal", terminal: Terminal }
  | { kind: "direct", model, accountId }
  | { kind: "denied", providerId? , virtualModel? }
  | { kind: "no-model", asked }
```

A live stream for the Overview ticker is wanted but not assumed: `GET /v8/neo/requests/stream` as SSE emitting `Request` objects. Until it exists the dashboard polls `GET /v8/neo/requests?limit=14`.

## What the dashboard never asks for

- Full emails, full keys, raw tokens, auth-file contents. `Account.label` and `Key.prefix` are already masked server-side.
- Provider pricing or cost. The vault's windows are the only quota currency; NeoPi's `/stats` owns cost.
- Anything from a member session beyond their own user, their own requests, the catalogue, the virtual models they are granted, and the core's `linked` flag.

## How this maps onto NeoPi

The owner's framing is that NeoPi is the core and NeoProxy borrows from it. Concretely: `core`, `vault`, `models`, and the per-account `windows` are read from NeoPi's auth broker and model catalog over the core socket; the proxy caches them and re-reads on heartbeat. Virtual models, users, grants, keys, usage and requests are the proxy's own state (a small SQLite under the engine's data dir is enough). When NeoPi grows a native `neoproxy`-style serving mode, this document is the shape it should expose; the dashboard does not care which process answers.
