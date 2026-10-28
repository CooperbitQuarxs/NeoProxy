# NeoProxy backend delivery plan

## Authorized direction

The owner authorized this backend through Saturn on 2026-10-28 and delegated decisions to Neptune. Do not ask the owner to redesign it. Use the existing Go CLIProxyAPI-derived engine. NeoProxy owns provider accounts, credentials and refresh; NeoPi is a client with a per-user inference key. The reconciled `backend-contracts.md` is the product specification.

Day one supports OpenAI Chat Completions, OpenAI Responses and Anthropic Messages. Dashboard authentication is username/password plus server-side sessions. Per-user inference keys are separate and grant-scoped. HTTPS deployment is later. There is no NeoPi rewrite, credential export or core-socket dependency.

All implementation uses independent m2 topic worktrees created from `origin/nightly`, with one PR per named slice into `nightly`. Issues are disabled; do not change repository settings. This document and the PR bodies are the tracking record. Neptune owns integration and updates this document. Report milestones to Saturn: each merge SHA and the E receipt. Do not touch Utopia in this local delivery; a later deployment requires E to pass and advance notification to Saturn.

## Work and receipts

| Slice | Deliverable | Status | PR / merge receipt |
|---|---|---|---|
| Contract | Reconcile proxy authority, username/password sessions and role meanings; publish this plan before A/B/D consume the contract | Merged | PR #7; `e99cb4a8de8073db31594cb8b2c619bae89927cb` |
| A | Users, login/logout/session restore, org-admin versus account-owner/member/service authority, per-user keys, grants, SQLite lifecycle | Merged; focused persistence/security/API/middleware checks and actual server build passed | PR #8; `d49b40b37652ba0b0216c613674cb657598d961e` |
| B | Proxy-owned provider accounts, owner assignment, pooled credential inventory, per-user/account/window attribution, owner statistics and owner-scoped keys | Merged; focused ledger/ownership checks, actual localhost API authority smoke and server build passed | PR #9; `bfa0717b9fa899fd42cce66e308e80f2519fe03c` |
| C | Persisted virtual models, strict validation, ordered routing and draw cursors, budgets/windows/effort, all terminals, usage, authorized `/v1/models` and real execution | Merged; corrected context-zero bounds, versioned migration, real three-protocol HTTP, cursor/queue/authority checks and executable smoke passed | PR #12; `b9c242a0d7432eafbf64b05fae7a33d8d80d206c` |
| D | Dashboard login and every product endpoint switched to real backend; honest empty/error/unknown states; retain reference fixture resolver for C tests | Real Chromium verification in progress; actual plaintext/TLS provenance defect corrected with real-mux HTTP/HTTPS session regression | Draft PR #13; transport fix `942fc3ba`; final browser pass pending |
| E | Scratch NeoPi provider and role selecting a virtual model discovered through `/v1/models`; real first-stage failure then fallback success over all three protocols | Passed all three genuine Groq fallback turns through discovered selectors and scratch roles; no client retries/fallback or owner-profile use | Ready PR #11; receipt `b5c878bc`, candidate `942fc3ba`; `neopi-e2e-receipt-20261028.json` |
| F | Script design note first, then optional per-virtual-model routing scripts with read-only request/usage/health input and validated decision output | Merged; actual subprocess/kernel/resource recovery and installed-server request-dependent Lua inference passed across all three protocols | PR #10; `e886c6f531014fdcc80ba31bb0b9c2f5828c8b9c` |

A status is not a completion claim. Replace pending receipts only with observed commits, commands and results. A passing mocked upstream is not the E proof.

E's genuine-provider role-selected proof has passed. The shipping batch remains
in progress until D's final browser acceptance passes and the final nightly binary
is built, installed with the previous executable retained, and launch-checked.
The E receipt pins its actual tested candidate, not later nightly bytes. Local
synthetic upstream tests are not E evidence. Utopia remains untouched.

## Shared implementation contracts

These contracts assign ownership and prevent six disconnected implementations. Workers must publish concrete exported interfaces early and communicate changes before relying on them. Required dependency commits may be merged from their topic branches with signed merge commits and declared `Depends on #...` relationships. Do not rebase, refresh from the base for convenience, or bypass a real dependency by duplicating its state.

### A: persistence and identity

A owns `internal/neo`: the SQLite lifecycle and user/session/key/grant implementation. The store exposes `DB() *sql.DB` for the two real extensions B and C to share transactions and the same database, not separate SQLite files. The implementation must support migrations, private data files, foreign-key checks and deterministic transactional mutation. Choose and pin a source-vetted SQLite driver. State goes under the engine's data directory; document the resolved default and any configuration switch.

A publishes `Principal` with `UserID`, `KeyID`, `Role`, current provider access/grants, and an optional narrower `AccountIDs` key scope. Publish `WithPrincipal(context.Context, Principal)` and `PrincipalFromContext(context.Context)` and carry the trusted identity through the existing HTTP-to-executor context path. User/key suspension and revocation must be checked against current authority, not stale browser state. Key IDs and scope lists are not raw secrets.

A owns the initial server attachment and session/org-admin authorization integration. An org-admin session may use the Engine dashboard administration handlers without exposing the management secret to JavaScript. Owners and members must not gain that privilege. Ordinary dashboard key login is removed; existing management keys remain maintenance credentials. Bootstrap is explicit, guarded by operator management authority, and cannot reopen after the first administrator is provisioned.

### B: account inventory and usage ledger

B owns `internal/neopool`, its account/ownership/window/usage tables, and the account/catalogue/owner-statistics HTTP handlers. It consumes A's store and principal rather than reimplementing login or key hashing. Stable account IDs map to the existing engine auth-manager credentials; provider tokens stay in that manager's existing credential store.

B publishes typed account/provider/model/window snapshots and a charge/reservation/settlement interface to C early. Include account owner and health, provider/model exposure, known reset boundaries and the measurement source. Unknown usage/capacity is not zero. If a provider lacks a trustworthy quota denominator, use explicitly configured metered capacity and label it as such, or make that budgeted route unavailable. Do not invent an upstream quota or silently use request counts as token/cost fractions.

The ledger attributes actual use to user, account and window, with virtual-model/stage/request identity where applicable. Scope owner queries by owned account IDs in SQL/authority checks, not frontend filtering. Owner-minted keys intersect the owner's own pools and current grants; an owner must not mint access to someone else's pool. Ownership assignment and provider login completion must not allow claiming a credential by guessing an ID. Reuse existing provider login/refresh flows, with authenticated initiating-owner attribution.

### C: routing and execution

C owns `internal/neoroute`, virtual-model persistence and HTTP operations, and the integration in the shared handler/executor paths for all three day-one protocols. Existing shared paths include `sdk/api/handlers/handlers_execution.go`, `handlers_stream.go`, `model_execution.go`, and `sdk/cliproxy/auth/conductor_execution.go`. The existing execution options support a pinned credential ID; reuse that engine mechanism instead of implementing another provider executor.

Publish the typed resolver/execution interface early for F and B. The same resolver must serve simulation and actual execution, with simulation read-only. Enforce grants, exposure, key/account ownership scopes, unique valid names, valid account/provider membership, finite budgets and requested effort. Preserve asked virtual selector versus the concrete chosen provider/model/account. Stream fallback may occur before downstream output commits; do not replay a partially delivered answer as a fresh success.

Use a durable per-stage alternate cursor, transactional budget reservation/settlement and provider-window resets. Failed attempts and cancellation must release or settle reservations truthfully. Queue waits are context-cancellable and wake when eligibility changes; no permanent timeout is added to an established upstream network stream. Limit returns HTTP 429 with `neoproxy_limit`. Passthrough retains virtual identity, chooses an exposed healthy target without a separate direct grant, and does not debit a stage budget. A failed eligible stage must be able to demote so E's forced first-stage failure can be proven on real execution, not only simulation.

Retain `fixture.js` `resolve()` as a reference oracle and exercise representative canonical decisions against it. Its wall-clock alternation is not the production cursor, and known fixture omissions do not override the written contract. Do not silently turn production traffic back into fixture execution.

C adds optional `script` source to a virtual model and a typed script-hook interface. No script means the standard path, unchanged. A configured script cannot be silently ignored if no evaluator is installed. C owns final grant/ownership/exposure/health/budget checks even when F proposes a decision.

### D: real dashboard

D owns `internal/neoui/static` and its browser verification. Consume the reconciled username/password session interface with cookies; do not retain an operator secret or user inference key as dashboard login state. Handle bootstrap, logout, session expiry and access errors. Adapt owner-specific account/key/statistics screens and preserve the distinction between organization admin and account owner. Product pages must not present generated requests, demo accounts or a fictional NeoPi core as live state.

Use actual endpoints for core status, vault/catalogue/exposure, users/keys/grants, virtual models, usage, requests and simulation. Poll real requests where SSE is unavailable. Keep the fixture resolver importable for C's tests, not as a production fallback. Retain existing Engine pages using authorized org-admin sessions. Show unknown quota as unknown. Include an optional script editor using F's agreed field; only authorized administrators may save scripts.

### E: local real-turn receipt

E works only in m2 scratch and a scratch `PI_CODING_AGENT_DIR`. Never edit, copy over or launch against the owner's NeoPi profile. Add custom provider definitions in the scratch `models.yml`, using a proxy user key; discover the virtual model from the proxy's authenticated `/v1/models`. Assign a scratch NeoPi model role to that discovered selector and run through the role. A direct provider/model request alone does not satisfy this slice.

For each protocol, prove an actual NeoPi turn with first-stage upstream failure and fallback-stage success. A deliberately failing local first-stage upstream is acceptable fault injection; the successful fallback must be genuine provider inference, not a mock, fixture response or echoed prompt. Use existing authorized proxy-owned credentials or a legitimately available upstream API credential; do not mine the owner's NeoPi profile as a substitute account source. If real inference credentials are unavailable, tell Neptune exactly what is missing while all other slices continue.

Receipt: proxy/build SHA, scratch profile path, provider protocol, discovered virtual selector, role assignment, request/correlation ID, first-stage failure, actual fallback model/account, non-fabricated reply and usage, and the commands actually run. Redact all keys, cookies and provider tokens. Reusable scripts and a redacted receipt belong to E's PR; secrets and raw private captures remain in m2 scratch.

### F: optional scripted routing

Start with a committed design note, then implement after C's interface is concrete. The initial choice is a Go-native Lua evaluator through `gopher-lua`: a smaller host-facing interface and explicit library selection make read-only routing hooks easier to constrain than a broad JavaScript host. Source-vet and pin the dependency before building it. Record the actual safety controls and limitations, not an unsupported sandbox claim.

A script reads a bounded snapshot of the request, current usage and account health and returns a decision proposal. No credentials, OS/process/filesystem/network access, arbitrary Go objects, or mutation of the authoritative ledger. Bound execution and handle cancellation, invalid output and resource exhaustion without hanging the server. Explain how memory/CPU limits are enforced. Only an explicitly scripted virtual model executes code; an unscripted model takes the old resolver path. Resolver validation cannot be bypassed by a script. No asynchronous telemetry, automatic script downloads or unrelated retry framework.

## Delivery discipline

- Implementers are GPT-6.1 Sol medium/high. Each worker gets the full intake and its own m2 worktree from `origin/nightly`; no protected-branch commits or rebases.
- One integration owner, Neptune, resolves shared-file conflicts and updates this plan. Publish dependency interfaces before broad implementation. Each slice gets a signed logical commit history and its own PR to nightly, with truthful tests and any dependency PRs named.
- Sign every local commit and merge with `/home/shayna/.ssh/id_ed25519_github_signing_agents.pub`, include `Model: openai-codex/gpt-6.1-sol` and `Co-authored-by: GPT-6.1 Sol <noreply@openai.com>`, and verify signatures before pushing. Use separate short and long commit messages.
- PR/comment identity starts with the required GPT-6.1 Sol note block. Use the existing `What`, `Why`, `Testing` PR structure; label type and state (`needs-triage` where no more specific state exists). Issues are disabled.
- Workers skip build/test/lint/format runs mid-flight. After a coherent patch, run focused checks once through the shared polite relay. Go changes require gofmt and an actual server build. No full suites. Builds use the heavy flock; tests/typechecks use the light flock with 4 GiB high/6 GiB max per job. A lock wait is not a test failure. No unbounded bypass or new authentication prompts; relay trouble goes to Neptune/Saturn.
- Merge each completed slice directly into nightly when its focused tests pass; record confidence in the merge note. No canary is required for these authorized additive backend slices. After each shipping batch, build/install nightly, retain the previous binary, and verify that it starts. Keep current ordinary proxy use working during incremental delivery.
- E must pass locally before any Utopia deployment. Utopia is out of scope now. Do not change repository settings or ask the owner to supply design decisions.
