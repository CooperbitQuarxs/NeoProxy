# Slice C routing receipt

## Scope and attachment

Branch `feat/backend-routing`, PR #12, targets `nightly` and depends on #8
(identity) and #9 (account inventory and ledger). The runnable dependency chain
includes parent startup attachment `73bc0201` and the pool import-cycle correction
`b90aead0`, integrated at `32b9ddca`. Initial routing source and HTTP regressions
were published in signed `cc8364df`. The current runtime checkpoint is
`db009f52fa5478bf6fe96bb9dbcacf7bfedc6728`, including the explicit-text-bound
correction `648304a7`, isolated versioned routing migration `828864c0`, and
structured Anthropic document coverage. Earlier ready receipt `841d884c` is
superseded by these corrections and the final checks below.

`Server.initNeoRouting` constructs `neoroute.New` over the shared identity store
and pool ledger, attaches the resolver to the existing shared handlers, registers
session-authenticated product routes, installs current-grant account visibility,
and enables consumer inference last. Consumer `/v1/models` dispatches to the
scoped catalogue; existing operator-key listing remains separate. No owner
profile, credential store or settings were used for these checks.

The F evaluator attachment is deliberately not imported by C: an unscripted
virtual model works without it, while saving or running configured scripts
without an evaluator fails closed. F consumes the runnable C checkpoint and adds
its evaluator on its own branch. E's genuine provider/NeoPi role-selected proof
is separate and is not claimed by this receipt.

## Exercised behavior

- Persisted virtual models and grants; finite budget/account/provider validation;
  direct model and passthrough ownership/key/exposure checks.
- Real SQLite reservations and settlement, a durable alternate cursor, rollback
  on rejected admission, estimated debit for unknown usage, and account-attributed
  reported usage without double-counting cached input.
- Cancellable queues, grant and suspension wakeups, eligibility changes, hard user
  limits, pre-output fallback, and no replay after partial streaming output.
- Read-only simulation, representative retained JavaScript fixture-oracle parity,
  session-scoped activity/usage/model views, and current-grant catalogue refresh.
- Bounded client business JSON supplied to scripts before provider injection;
  typed script diagnostics; resolver-owned proposal checks and current authority
  reload after evaluation. A configured script is never silently ignored.
- Actual shared auth manager, protocol translators and HTTP provider executor
  across Chat Completions, Responses and Messages. The shared-handler fault test
  also checks that configured final payload rules override stage effort and that
  inner fallback cannot reach a third account outside the selected scope.
- An actual private attached HTTP server across all three public inference routes,
  with first-stage HTTP 503 and fallback success, persisted correlation and usage,
  scoped discovery, raw/direct denial, and immediate grant revocation.

Only local upstream fixtures are used by the HTTP tests. Their replies establish
real proxy execution and translation, not real provider inference.

## Commands and results

All tests used the shared light lock and the existing polite relay with `--mem`.
The actual build used the heavy lock. The relay's aggregate CPU quota was `5s`
(five logical CPUs); per-job memory limits were 4 GiB high and 6 GiB maximum.
`TMPDIR=/mnt/alias/m2/agents/scratch/neoproxy-c-tmp` was set for Go commands.
Touched Go files were formatted with `gofmt -w` before checks.

Initial focused run:

```sh
flock /run/user/1000/npi-afk-light.lock \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-c-final-20261028 -- \
  go test ./internal/neoroute ./sdk/api/handlers ./internal/api \
  -run 'Test(Durable|RealFailure|AtomicReservation|BudgetFree|Queued|QueueWakes|PartialStream|Script|UserLimits|Resolve|Fixture|Virtual|Simulation|Usage|Request|Neo)' \
  -count=1
```

The shared-handler package passed. The routing oracle's not-granted case failed
because its Go nil grants serialized as JavaScript `null` rather than `[]`.
The attached-server test's second and third protocols succeeded via fallback,
but correctly inherited the first protocol's injected credential cooldown, so
they did not perform the newly expected first-stage fault. These were test-fixture
defects, not an import-graph failure or passing run. Corrected the oracle's empty
array and explicitly disabled cooldown only on the private HTTP test credentials,
so each protocol independently exercises the fault. No production cooldown
behavior was changed.

Affected cases and the policy/session visibility cases not selected above:

```sh
flock /run/user/1000/npi-afk-light.lock \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-c-corrected-20261028 -- \
  go test ./internal/neoroute ./internal/api \
  -run 'Test(FixtureOracleParity|HTTPRequests|ModelsRefresh|PolicyAuthority|DirectAndPassthrough|NeoAttachedServer)' \
  -count=1
```

Passed: routing `0.487s`, API `0.126s`. Together with the successful tests in the
initial focused run, this exercises every Slice C routing test present before the
additional concurrent-router regression. Existing manual readiness/revocation,
worker-registration and management-authority regressions passed in the initial
run; only the obsolete constructor pre-attachment assertion was removed.

Additional cross-router transaction regression:

```sh
flock /run/user/1000/npi-afk-light.lock \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-c-atomic-20261028 -- \
  go test ./internal/neoroute \
  -run '^TestConcurrentRoutersReserveAndAdvanceOneDurableCursor$' -count=1
```

Passed: routing `0.119s`. Eight concurrent requests through two resolver instances
sharing the real single-connection database committed exactly eight cursor
advances, split evenly across the two accounts, with actual settled token usage
and no remaining reservations.

Actual server build:

```sh
flock /run/user/1000/npi-afk-heavy.lock \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-c-server-20261028 -- \
  go build -buildvcs=false \
  -o /mnt/alias/m2/agents/scratch/neoproxy-c-server ./cmd/server
```

Passed with exit status 0. `-buildvcs=false` means the binary does not embed the
source commit; this initial binary used the C production source published as
`cc8364df`. The later correction was rebuilt and launched separately below.

The built executable was launched through the polite relay with `--local-model`,
a private config, private empty auth directory, and private application database,
bound only to `127.0.0.1:19432`. It reached the listening-ready condition. Native
HTTP checks observed authenticated `/v1/models` returning 200 with an empty real
catalogue, and unauthenticated `/v8/neo/virtual-models` returning 401. `/health`
returned 404; no health endpoint success is claimed. The private process exited
cleanly with status 0 after SIGTERM. Private config and data remain under
`/mnt/alias/m2/agents/scratch/neoproxy-c-tmp`; no credentials are committed here.

## Final correction and persistence acceptance

F's real installed-evaluator test exposed a genuine runtime defect after the
initial ready receipt: ordinary string messages with explicit `max_tokens` could
fail when a custom model had context length zero. The gjson projection used to
detect non-text input returned an existing empty array, so `Exists()` falsely
required a catalogue ceiling. The initial HTTP fixtures had known context lengths
and therefore did not expose this. Source fix `648304a7` inspects structured media
instead of projection presence or matching ordinary text. Explicit output bounds
now work for text requests without catalogue context; actual image/audio/file
inputs still require the known context ceiling. Shared-handler and attached-server
fixtures now deliberately have context length zero for all three protocols.

Parent also identified the agreed migration convention missing from C's initial
schema. Source fix `828864c0` uses routing-owned `neoroute_schema` in one short
transaction, adopts initial unversioned tables without dropping data, rejects
unsupported versions before product DDL, and atomically commits the initial
schema and version. It does not change identity's `PRAGMA user_version` or B's
`neo_pool_schema`. Real tests close/reopen the database, check legacy adoption and
version isolation, reject a future version, and prove rollback after DDL failure.

After these coherent corrections, `gofmt -w` was applied to their touched files.
The affected focused checks used the same scratch `TMPDIR`, relay and locks:

```sh
flock /run/user/1000/npi-afk-light.lock \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-c-bounds-schema-20261028 -- \
  go test ./internal/neoroute ./sdk/api/handlers ./internal/api \
  -run 'Test(UpperBound|RoutingMigration|Durable|ConcurrentRouters|AtomicReservation|NeoRouteThreeProtocol|NeoAttachedServer)' \
  -count=1
```

Passed: routing `1.028s`, shared handlers `0.501s`, actual API `0.127s`.
This includes actual three-protocol HTTP fault/fallback at context length zero,
not merely a pure upper-bound test.

```sh
flock /run/user/1000/npi-afk-heavy.lock \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-c-bounds-schema-build-20261028 -- \
  go build -buildvcs=false \
  -o /mnt/alias/m2/agents/scratch/neoproxy-c-server ./cmd/server
```

Passed with exit status 0, using runtime source `828864c0`. The corrected binary
was launched again through `polite --mem`, using the same isolated private config,
auth directory and application database on `127.0.0.1:19432`. This also exercises
adoption of the initial smoke database's previously unversioned routing tables.
The listener became ready; authenticated `/v1/models` returned 200 and an empty
catalogue; unauthenticated `/v8/neo/virtual-models` returned 401. SIGTERM exited 0.
F was sent the signed runtime checkpoint for its own affected evaluator test.

Final media followup `db009f52` also recognizes structured Anthropic PDF document
blocks as non-text media. The additional document regression and all upper-bound
cases passed (`0.013s`) using the same light-lock/relay command with
`--label neoproxy-c-media-final-20261028 -- go test ./internal/neoroute -run
'^TestUpperBound' -count=1`. No additional suites were run.

### Final corrected binary provenance

- Source: `db009f52fa5478bf6fe96bb9dbcacf7bfedc6728`.
- Binary: `/mnt/alias/m2/agents/scratch/neoproxy-c-server`.
- SHA-256: `0b2e2a3ef2c675289422589bc2832999df6c8f561adc5aa642621e5280fc25dd`.
- Build working directory:
  `/mnt/alias/m2/agents/worktrees/neoproxy-backend-c`.
- Exact final build command (passed, exit 0):

```sh
env TMPDIR=/mnt/alias/m2/agents/scratch/neoproxy-c-tmp \
  flock /run/user/1000/npi-afk-heavy.lock \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-c-media-final-build-20261028 -- \
  go build -buildvcs=false \
  -o /mnt/alias/m2/agents/scratch/neoproxy-c-server ./cmd/server
```

The final binary was launched from the private scratch working directory with:

```sh
env NEOPROXY_DATA_DIR=/mnt/alias/m2/agents/scratch/neoproxy-c-tmp/smoke-data \
  MANAGEMENT_PASSWORD= GITHUB_TOKEN= \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-c-media-launch-20261028 -- \
  /mnt/alias/m2/agents/scratch/neoproxy-c-server \
  --config /mnt/alias/m2/agents/scratch/neoproxy-c-tmp/server-smoke.yaml \
  --local-model
```

This exact final binary reached listening-ready on localhost. Its authenticated
`GET /v1/models` returned 200 with `{"data":[],"object":"list"}`; unauthenticated
`GET /v8/neo/virtual-models` returned 401. It exited 0 after SIGTERM. The binary
contains C without F's evaluator; F separately reported its own full evaluator
checks and installed three-protocol request-dependent Lua test passing against
C's corrected runtime. That is F's receipt, not a C claim of E provider proof.

## Integration notes

B's later `641cc1e5` contains test-fixture/cache regression documentation only;
B reported no runtime or interface correction beyond the already consumed
`b90aead0`. It is not an additional runtime dependency of C.

Confidence recommendation: 90% for merging C into nightly after Neptune's shared
integration review. Real-provider availability and F's subprocess evaluator are
not established by C's fixtures; their separate receipts remain necessary for the
combined delivery. C must not be represented as E completion.

The signed source checkpoint `cc8364df` accidentally used literal backslash-n
separators in its commit body. Its model attribution is present but not parsed as
Git trailers. The subsequent signed receipt commit uses actual newlines and
records that formatting correction without rewriting published history.
