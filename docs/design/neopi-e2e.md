# Isolated NeoPi role-selected fallback proof

`scripts/neopi_e2e.py` drives a local, private NeoProxy instance and the installed NeoPi executable. It does not change NeoPi source or read, copy, edit, or launch against an existing NeoPi profile. This is slice E of the backend contract, not a provider simulator.

## Observed real run: 2026-10-28

**Passed: all three role-selected real turns.** The reviewed, credential-redacted machine receipt is [`neopi-e2e-receipt-20261028.json`](neopi-e2e-receipt-20261028.json). It contains the complete commands, discovered selectors, role/provider definitions, wire observations, persisted trails, replies and usage.

The exercised integrated candidate was source `942fc3ba03d02aae54e8017cfebfaee54b88f7be`, binary SHA-256 `1ea16494433bdf1ae65d682842616cbfab77664df148c5453e0f83e4f2e716a4`. Neptune supplied the actual build command and A/B/C delivery pins recorded in the receipt. This proves that candidate, **not an unexercised later nightly binary**; the subsequent C PDF-only change was not in this build.

| Client protocol / actual role | Discovered selector | Proxy request correlation | Actual tokens in / out |
| --- | --- | --- | --- |
| OpenAI Chat Completions / `@proof-chat` | `neoproxy/proof-chat` | `req_bbcd42c9-ad83-4054-9fb3-c1af4ab51a89` | 85 / 109 |
| OpenAI Responses / `@proof-responses` | `neoproxy/proof-responses` | `req_6e824686-282e-40b4-9c85-5243dddd50c4` | 85 / 189 |
| Anthropic Messages / `@proof-messages` | `neoproxy/proof-messages` | `req_2147ec34-e5de-46d4-a6ef-3f05e31ea483` | 85 / 74 |

For every row, the first-stage upstream was actually called, returned the injected HTTP 503, and appeared in the persisted trail as `upstream-error`. Stage `s1` then served genuine official Groq `openai/gpt-oss-20b` inference through concrete proxy model `openai-compatible-proof-real/fallback`, account `acct_762dec8551accd17f8e24a5526de0c4c`. Each NeoPi process emitted a completed assistant message, and each transport observation matched its persisted request ID. There were no client-side retries or model fallbacks.

Actual replies, in table order:

- “Venus is the hottest planet in the Solar System.”
- “Mars has the tallest volcano in the solar system.”
- “Jupiter’s Great Red Spot is a giant storm that has raged for at least 350 years.”

Usage is actual provider-reported token usage; output includes reasoning tokens where the transport exposes them. `--thinking off` was requested, not a claim that the remote model did no internal reasoning. NeoPi's zero-valued custom-provider cost metadata is not a billing measurement or a claim of free upstream inference.

Private state stayed under `/mnt/alias/m2/agents/scratch/neoproxy-backend-e/attempt-20261028-01`; every client used its own scratch `HOME`, `XDG_*`, `TMPDIR` and `PI_CODING_AGENT_DIR`. A post-run comparison of 11 receipt/capture files found zero matches for the known real upstream/operator/password credentials or raw `nk_` inference-key pattern. The owner's profile was not read for this comparison or used as an account source. No NeoPi code, owner settings, installation or Utopia state was changed.

The 16 focused tooling/isolation/redaction tests passed separately through the shared light lock and polite relay. They are not the evidence for real inference; the three observed turns above are.

## Prerequisites and authority

- The integrated A/B/C implementation must have been explicitly delivered: sessions/bootstrap/users/keys/grants, owned accounts with configured metered windows, and virtual-model execution with recorded fallback decisions.
- Supply the actual integrated proxy binary and the source SHA from which it was built. The receipt records the supplied build SHA and separately computes the binary's SHA-256. The SHA argument is build provenance supplied by the integrator, not a claim that hashing a binary recovers its source revision.
- An authorized real upstream API credential must be inherited in an environment variable. The default is `GROQ_API_KEY`, with the official `https://api.groq.com/openai/v1` endpoint and active small `openai/gpt-oss-20b` model. Preparation authenticates `/models` and requires that model to be present. It does not import a NeoPi auth export. The live E order authorizes modest real turns using this credential.
- Python 3.11+ and the installed `npi` executable are required. The runner uses only Python's standard library; it adds no package dependency.
- All private files and build outputs belong under `/mnt/alias/m2/agents/scratch/neoproxy-backend-e`. Use a fresh child directory for every run. Do not use `/tmp`, the owner's profile, a shared production proxy, or Utopia.

Do not paste API keys into commands. Preparation reads the named environment variable in process, writes configuration with mode `0600`, and prints only non-secret inventory results. Inference keys, operator secrets, passwords, cookie state, provider credentials and raw captures are never PR artifacts.

## Prepare before integration

From the E worktree:

```sh
python scripts/neopi_e2e.py prepare \
  --scratch /mnt/alias/m2/agents/scratch/neoproxy-backend-e/attempt-<unique>
```

Preparation writes a private JSON-encoded YAML proxy config and a non-secret `setup.json`. It configures three independent first-stage fault accounts, so the previous protocol's cooldown cannot turn the next protocol into an unobserved skip. Each fault-only upstream returns HTTP 503. The one shared fallback account points at the actual remote Groq model. No successful local inference response exists in the tool.

Compatibility group names are not engine provider IDs: the `proof-real` group is registered as `openai-compatible-proof-real`, so its routed concrete selector is `openai-compatible-proof-real/fallback`. Fault groups use the corresponding `openai-compatible-proof-fault-<wire>` IDs. The runner uses these authoritative IDs for account inventory and stages, not the display/group names.

The local ports are selected during preparation and bound only to `127.0.0.1` at run time. A port collision is an operational failure; prepare a new run instead of pointing the client at an unrelated listener.

## Execute after explicit A/B/C delivery

Obtain the binary and build command/source revision from the integrator. Build it only through the shared heavy lock and polite relay, not by running a second unbounded build. Run the proof through the light lock and the same relay:

```sh
flock /run/user/1000/npi-afk-light.lock \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-e-real-turns -- \
  python scripts/neopi_e2e.py run \
    --scratch /mnt/alias/m2/agents/scratch/neoproxy-backend-e/attempt-<unique> \
    --proxy-binary /mnt/alias/m2/agents/scratch/<integrated-binary> \
    --proxy-sha <actual-integrated-source-sha> \
    --a-sha <delivered-a-commit> \
    --b-sha <delivered-b-commit> \
    --c-sha <delivered-c-commit> \
    --npi /home/shayna/.local/bin/npi
```

The shared relay caps aggregate CPU at five logical CPUs (`CPUQuota=500%`); `--mem` requests 4 GiB memory pressure / 6 GiB hard limit per job. A wait for the shared lock is not a failed proof. An unavailable relay is a prerequisite failure, not permission to bypass it.

The runner:

1. Starts a scratch NeoProxy using its own config, auth directory and SQLite data directory. It uses a fresh `HOME` and allowlisted environment; no broker, profile, plugin or ambient upstream key settings are inherited.
2. Bootstraps an isolated administrator with an independently generated operator secret/password, explicitly assigns configured accounts to that administrator, and configures a one-million-token **configured-metered** daily window per account. This is the denominator for this isolated budget proof, not an invented upstream quota measurement.
3. Creates one two-stage virtual model per protocol, a service user with only those grants, and an inference key narrowed to the exact scratch accounts. The service user has no direct-provider grants.
4. Authenticates `/v1/models` with that inference key, discovers the actual virtual selectors returned by the proxy, and only then writes each scratch `models.yml` and role assignment in `config.yml`.
5. Runs an actual NeoPi print-mode turn using `--model @proof-<wire>` over each of `openai-completions`, `openai-responses`, and `anthropic-messages`. The model has a 512-token output ceiling, tools/thinking/title generation are disabled, and the prompt asks only for a short public planet fact. These are not direct provider/model CLI requests.
6. Uses a transparent localhost HTTP observer to record the actual requested endpoint/model and proxy response correlation header. It forwards the remote response bytes without synthesizing or retrying inference. Only the separate first-stage upstream synthesizes the deliberately failing 503.
7. Requires an actual observed fault call, persisted first-stage `upstream-error`, selected fallback stage/account/model, correlated successful request, completed NeoPi assistant text, and positive real proxy token usage. A healthy-stage skip, a direct success, local echo, missing usage, uncorrelated request, or failed assistant turn cannot produce `status: passed`.

Every client has a separate scratch `HOME`, `XDG_*`, `TMPDIR`, `PI_CODING_AGENT_DIR`, working directory and private captures. The client receives only the proxy inference key, never the Groq credential. Ambient extensions, skills, rules and MCP are disabled, raw chat mode excludes coding/project context, and the environment is constructed from an allowlist. Isolation is established by these supplied paths/settings, not by opening the owner's profile to compare secrets before/after.

## Evidence and failure semantics

The private `receipt.json` records actual commands, scratch profiles, discovered selectors, role assignments, observed wire protocol, request IDs, failed upstream calls, persisted decisions, fallback account/model, assistant reply and usage. It also records the proxy binary hash, supplied source SHA and explicit A/B/C interface deliveries.

Only three successfully validated protocol receipts produce `status: passed`. A missing credential, failed authentication, unavailable backend, unsupported protocol, failed real inference or missing evidence leaves `status: not-passed` with a precise blocker; it must never be described as E passing. Debug/request logging is explicitly disabled. Known operator, login, API and inference-key values are removed before subprocess logs, client captures or receipts are written. These captures still remain private scratch files. Commit only a reviewed redacted receipt, never the captures or scratch configuration.

Focused verifier/isolation tests (not a real-inference substitute):

```sh
flock /run/user/1000/npi-afk-light.lock \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-e-tool-tests -- \
  python -B -m unittest discover -s scripts -p test_neopi_e2e.py -v
```

The unit tests exercise receipt rejection, authenticated-discovery shape and environment isolation. They make no claim about provider inference. The committed run receipt is the place to look for the real-turn result.
