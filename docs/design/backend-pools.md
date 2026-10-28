# Owned engine accounts and token metering

Slice B extends the existing engine auth manager and per-auth model registry through `internal/neopool`. Secrets stay in the existing auth files and engine API-key configuration, never SQLite or dashboard responses. Public `acct_…` IDs hash the stable engine auth ID; typed internal snapshots retain `EngineAuthID` for the executor pin. Labels are opaque masked account suffixes, not full emails or keys. Compatibility provider IDs preserve engine names such as `openai-compatible-proof-real`.

## Ownership and authority

Existing credentials remain unowned and visible only to organization admins until an admin explicitly assigns them. Bootstrap does not silently claim an existing operator's credentials. Owners cannot claim an account by supplying an ID. Completed engine OAuth login and new API-key creation carry the authenticated initiating session user into a separate pre-persistence ownership guard. Relogin cannot overwrite another owner's credential. Operator maintenance logins without a dashboard session stay unowned. Plugin login state retains the original initiating owner rather than attributing completion to whoever polls it.

A's account-scope validator is installed as `identity.SetAccountScopeValidator(pool.ScopeOwned)`. Owner keys require a nonempty scope whose every ID currently belongs to that owner. Key authentication revalidates ownership after transfers. C additionally passes `Reservation.ScopeOwnerID` for owner keys, so the reserve transaction checks ownership again before committing. Current grants remain A/C authority: scoped account IDs never create a grant.

Attempts retain the owner at reservation time. A transfer changes current inventory and key eligibility, not historical attribution. Owner statistics select this immutable owner column in SQL. A new owner sees no prior owner's request/user/token detail; current account window totals can still include earlier consumption of that same account.

## Honest measurement

The engine's provider cooldown/expiry/disabled eligibility is projected through its actual selector predicate; elapsed cooldown flags do not permanently strand an account. No refresh engine is added.

Upstream quota watermarks are not universally convertible into exact token denominators. This slice does not invent provider capacity or treat request counts as quota fractions. Accounts without an explicit configured meter have `windows: []`; their budgeted stages are unavailable. Direct/passthrough execution may still run without a budget denominator and remains attributable.

An owner or admin can explicitly configure a local token-capacity meter. It is returned as `source: "configured-metered"`, `unit: "tokens"`, `known: true`, with capacity, actual/estimated used tokens, exact start/reset timestamps and a clamped display fraction. This is a proxy policy meter, **not** a claim about upstream subscription quota. Five-hour, day and week meters use fixed elapsed periods anchored at `startsAt`; month meters use UTC calendar months, clamping short months to their last day without anchor drift. A meter cannot be changed while attempts remain in its active period.

The ledger debits canonical engine token accounting, including Anthropic cache-read/write input. Cached tokens are a subset of total input, not an extra charge. Actual executor AuthID is checked against the pinned account. A reservation uses a conservative caller-supplied upper bound; missing output/input bounds must not be fabricated by B. Failed attempts with reported usage settle those exact tokens. Dispatched work with missing/partial usage, including cancellation, retains the reservation bound as explicitly estimated consumption. `unknownAttempts` remains visible even for budget-free work with no bound. `Release` is only for attempts never dispatched. Duplicate reservation/settlement identities are idempotent only when their complete contents agree. Retried execution uses a distinct attempt ID under the same request ID and is charged separately.

`tokens.cached` and owner `cachedTokens` project the canonical input cache-read bucket, including an explicit zero. The legacy `Detail.CachedTokens` display field is not independent usage and cannot override a canonical breakdown. Real executor parsers populate the canonical cache-read bucket. The regression test supplies that bucket explicitly, then carries captured usage through settlement, current-window usage and owner statistics: 10 input plus 3 output with 4 cached input debits 13 tokens, not 17. Anthropic's separate upstream input/cache buckets are normalized by the existing canonical accounting layer before this projection.

## Additional HTTP operations

All paths below are under `/v8/neo`, use A's cookie session middleware and CSRF/same-origin protection, and never grant management-secret authority to owners. Standard vault/catalogue/exposure routes follow `backend-contracts.md`.

| Method and path | Request | Result / authority |
|---|---|---|
| `PATCH /vault/accounts/:id` | `{ownerId}` | `204`; admin-only explicit assignment |
| `PUT /vault/accounts/:id/windows/:kind` | `{capacityTokens, startsAt}` | `204`; current owner or admin; `409` while that meter has active-period attempts |
| `GET /owner/stats` | No owner selector | `{accounts:[{accountId,ownerId,inputTokens,outputTokens,cachedTokens,estimatedTokens,reservedTokens,unknownAttempts,attempts}]}`; owner only own historical/current rows, admin all |
| `POST /vault/api-keys` | `{providerId,apiKey}` | `201 {accountId}`; owner/admin, append-only existing engine credential configuration |
| `POST /vault/login/:provider` | No arbitrary engine options | Existing engine `{status:"ok",url,state}`; owner/admin; state bound to initiator |
| `GET /vault/login/status?state=` | Initiated state | Existing engine `{status:"wait"\|"ok"\|"error",error?}`; same initiating user only |
| `POST /vault/login/callback` | `{provider?,state,code?,error?}` | Existing engine callback response; same initiating user and provider only |

API-key creation allows only built-in `claude`, `codex`, `gemini`, `xai`, `meta` with fixed official endpoints. Existing duplicate keys are rejected, not adopted. Owners cannot provide URLs, config patches, headers, filenames or another account's auth ID. Custom compatibility credentials remain an organization-admin Engine configuration operation followed by explicit ownership assignment.

OAuth delegates existing native/plugin flows (`anthropic` aliases the engine's `claude` start operation); absent plugins return an honest unavailable response. It does not manufacture supported providers or authorization URLs. Poll/callback bindings last 35 minutes to cover the engine's 30-minute device flows. Provider credentials and refresh remain in those existing flows. No `/v0` routes are added or changed; plugin wrapper initiation uses the existing v8 callback contract.

Vault reads use SQL-owned account IDs for owners. Member inventory uses C's current-grant account projection and redacts owner IDs and account-wide meters; member `/usage` remains their own per-user usage. Provider/model exposure mutation is organization-admin-only. Raw engine auth IDs, quota headers and credentials are never serialized.

## Routing integration

`ReserveAndAdvance(ctx, reservation, func(*sql.Tx) error)` owns the immediate SQLite transaction for both budget admission and C's durable cursor CAS. The callback uses only the supplied transaction; it never opens another Store operation, runs a script, waits, or performs network I/O. A duplicate attempt does not advance a cursor twice. Callback failure rolls back the reservation. Reservation/settlement changes broadcast through `Changes`; reset boundaries come from snapshots. C polls engine eligibility on its queue wake path because engine runtime health changes independently of SQLite.

`WithAttempt(ctx,id)` installs a synchronous usage observer before the existing asynchronous usage-sink queue. At completion C obtains `capture.Settlement(accountID)` and settles using a non-cancelled accounting context. C owns virtual model validation, current-grant checks, `/usage` stage/window projection, final exposure/health checks and actual execution/fallback. A missing or inconsistent actual-account report is not permission to substitute the resolver's guess.

## B verification receipt — 2026-10-28

The actual server constructor now initializes this pool through `server_neo_pool.go` on the shared identity database, installs current ownership validation and registers cookie-authenticated pool routes. `TestNeoPoolAttachedServerHTTP` starts that constructor behind a real localhost HTTP listener, logs admin and owner sessions in through `/v8/session`, and exercises anonymous rejection, unowned inventory, admin assignment, owner metering, secret redaction, no claim-by-ID and no owner management/exposure authority.

Final focused commands, run from the B worktree, passed:

```sh
gofmt -w internal/neopool/attempt_test.go internal/api/server_neo_pool_test.go sdk/cliproxy/usage/observer.go
flock /run/user/1000/npi-afk-light.lock /home/shayna/.local/share/polite-relay/polite --mem --label neoproxy-b-final-pools-20261028 -- env TMPDIR=/mnt/alias/m2/agents/scratch/neoproxy-b/tmp go test ./internal/neopool ./sdk/cliproxy/usage ./internal/api/handlers/management -run 'Test(Attempt|Inventory|Owner|ConcurrentBudget|CursorAndReservation|ResetsRetries|UnknownMeter|HTTPEnforces|Credential|AccountOwnership|PluginCompletion|Owned|EnsureTokenBreakdown)' -count=1
flock /run/user/1000/npi-afk-light.lock /home/shayna/.local/share/polite-relay/polite --mem --label neoproxy-b-final-api-20261028 -- env TMPDIR=/mnt/alias/m2/agents/scratch/neoproxy-b/tmp go test ./internal/api ./internal/neopool -run 'TestNeoPoolAttachedServerHTTP|TestElapsedEngineCooldownBecomesEligibleWithoutRefreshLoop' -count=1
flock /run/user/1000/npi-afk-heavy.lock /home/shayna/.local/share/polite-relay/polite --mem --label neoproxy-b-final-build-20261028 -- env TMPDIR=/mnt/alias/m2/agents/scratch/neoproxy-b/tmp go build -buildvcs=false -o /mnt/alias/m2/agents/scratch/neoproxy-b/cli-proxy-api ./cmd/server
```

The first test command passed all three selected packages; the second passed both selected packages, including the real HTTP attachment smoke and elapsed-cooldown regression. The actual `cmd/server` build exited successfully. The cached-projection failure was an incomplete synthetic usage record, not an extra-debit production path: the test now supplies the canonical cache-read field used by real parsers, while additional regression coverage proves canonical-zero precedence and a 13-token ledger debit with cached input.

B is recommended for integration at 90% confidence. Live OAuth/provider enrollment was not performed against external accounts; deterministic existing-flow tests cover initiator retention, ownership checks before persistence and append-only API-key registration. C owns real scoped inference, routing and member catalogue visibility; E owns real provider fallback proof. No deployment or nightly installation was performed by B.
