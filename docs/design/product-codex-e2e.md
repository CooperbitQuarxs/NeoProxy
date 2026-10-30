# Real Codex product workflow verification

This is the continuation of the deployed-product order, not a rerun of the historical Groq slice-E proof. The reusable runner is `scripts/product_e2e.py`; the reviewed machine receipt is [`product-codex-receipt-20261029.json`](product-codex-receipt-20261029.json).

## Observed result

The actual installed NeoProxy nightly binary, source `f57e8655f06171aa2f32349f520d223111f86803`, SHA-256 `5528ee89e1264b040b5ec2c48cb54bfa15420a37b1d2c657e031f66504593453`, passed two independent two-cycle runs on private `127.0.0.1:18527`:

- **12 genuine upstream requests:** OpenAI Chat Completions, OpenAI Responses and Anthropic Messages, each in non-streaming and streaming mode, repeated across the two runs.
- **Four ordinary member login/restore/logout cycles:** authenticated discovery returned only the current granted `neoproxy/office-cheap` virtual selector; logout revoked the server-side session.
- **15 actual authority transitions/checks:** member/owner administration boundaries, owner pool-key scoping, direct-provider denial, live grant removal, key revocation, suspension, reactivation, and password-change invalidation plus fresh login.
- **Two focused proof-safety tests:** insufficient cycles cannot be called repeated proof, and receipt filenames cannot escape private scratch. These tests do not simulate provider inference.

Every real turn returned `5` to the innocuous prompt “What is 2 plus 3? Reply with only the single digit.” Each persisted request reported 317 input tokens and 5 output tokens, selected real concrete model `codex/gpt-6-luna`, and used account `acct_eed9c93a79ed2b3385b037a198f38a8c`. The final owner statistics reported 12 attempts, 3,804 input tokens, 60 output tokens, zero unknown attempts and zero outstanding reservations. The configured-metered ledger fraction was `0.003864`, exactly 3,864 reported tokens divided by the explicitly configured 1,000,000-token day capacity. This denominator is a local configured budget, not a claim about upstream quota.

The pool contains **one genuine authorized Codex account**. A singleton pool was explicitly accepted for this proof. No second account was fabricated, no successful mock was substituted, no Groq inference was reused, and no Haiku call was made.

The initial credential-upload attempt used the wrong v8 path and returned 404. The harness was corrected from legacy `auth-files` naming to documented `/v8/management/credentials`; this was a runner error, not evidence of a backend defect. All subsequent real runtime workflows passed, so this PR does not invent an unnecessary backend fix.

## Isolation and account acquisition

All configuration, state, credentials and captures are private under `/mnt/alias/m2/agents/scratch/neoproxy-product`. The runner fixes the listener to `127.0.0.1:18527`; it never targets production `8317` or the UI lane `18517`.

The existing proxy credential directory had no JSON accounts. Under the explicit product-order authorization, the runner read one specifically selected active `openai-codex` credential, ID 1, from the canonical credential SQLite store using a read-only connection. The SQL projection selected only `access`, `accountId` and `expires`; it did **not** select or copy any refresh token, export other accounts, modify the source store, or edit owner settings. The access lease expires at epoch milliseconds `1793806431490`.

The selected access lease was registered through the existing authenticated v8 credential-upload interface. Before uploading it, the runner required the scoped configuration read at `/v8/management/config/oauth/auth-dir` to equal the scratch auth directory. The administrator explicitly assigned the resulting account to the scratch account owner, configured its metered day window, created the virtual model and member grant, and minted a key narrowed to that account. Passwords, operator secrets, inference keys and provider tokens remain in mode-0600 scratch files or process memory, never argv or committed receipts.

Each server child has a fresh scratch `HOME`, `XDG_*`, `TMPDIR`, `PI_CODING_AGENT_DIR` and an allowlisted environment. Debug/request logging is disabled and known secret values are scrubbed from captured output. The native NeoPi connection worker receives only the **path** to private `access.json`, then uses the member's actual username/password flow to obtain its own scoped key. A hand-written `models.yml` is not the product connection UX.

## Reusable procedure

Preparation is deliberately explicit and refuses to overwrite an existing private lane:

```sh
python -B scripts/product_e2e.py prepare --credential-id <specifically-authorized-id>
```

The default source is the canonical `~/.omp/agent/agent.db`; use `--source-db` only for another explicitly authorized credential store. This is a minimum access-lease acquisition command, not a general export command. `--credential-id` is mandatory.

Start the already built, owner-authorized nightly binary with its actual source revision. A long-lived serving process uses the polite relay but must not hold the test lock indefinitely:

```sh
/home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-product-lane -- \
  python -B scripts/product_e2e.py serve \
    --binary <actual-nightly-binary> --source-sha <actual-build-source-sha>
```

Then provision once:

```sh
python -B scripts/product_e2e.py bootstrap
```

The private `/mnt/alias/m2/agents/scratch/neoproxy-product/access.json` then contains the endpoint, administrator/owner/member login details, scoped key, account IDs and virtual selector. Do not print, commit, publish or pass its secrets as command-line arguments. Completed bootstrap refuses to run again; use the existing state for subsequent exercises rather than re-importing credentials or resetting meters.

Run the focused authority and genuine inference exercises through the shared light lock and polite resource limits:

```sh
flock /run/user/1000/npi-afk-light.lock \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-product-authority -- \
  python -B scripts/product_e2e.py authority

flock /run/user/1000/npi-afk-light.lock \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-product-real -- \
  python -B scripts/product_e2e.py exercise \
    --cycles 2 --receipt product-receipt.json
```

The first cycle is non-streaming and the second streaming. The script requires at least two cycles, checks every wire's real reply and correlated persisted account/model/positive usage, and captures current owner statistics and virtual-stage usage. Authority probes use a separate temporary user, restore the existing owner's grants, revoke their temporary owner key and suspend their own probe user; they do not change the member credentials being used by the native-connection worker.

Focused runner-safety tests:

```sh
flock /run/user/1000/npi-afk-light.lock \
  /home/shayna/.local/share/polite-relay/polite --mem \
  --label neoproxy-product-focused -- \
  python -B -m unittest discover -s scripts -p test_product_e2e.py -v
```

The shared relay caps aggregate CPU to five logical CPUs; `--mem` requests 4 GiB memory pressure and a 6 GiB per-job hard limit. No new grants, full suites or unbounded bypasses are needed. No Go code changed, so no new server build was run by this slice.

## Sustained engine-owned refresh: separate from this lease proof

**The access-only lease is not durable refresh ownership and must not be presented as such for Utopia.** Copying a broker refresh token into a second writer risks invalidating an existing account; this proof deliberately does not do it.

The existing sustained provisioning seam is an independent, owner-attributed **NeoProxy engine OAuth login**:

1. Sign into NeoProxy as the account owner.
2. `POST /v8/neo/vault/login/codex` with the session cookie and same-origin/CSRF protection. The engine returns its authorization URL and state.
3. Complete that independent provider sign-in and return `{provider: "codex", state, code}` to `POST /v8/neo/vault/login/callback` for the same owner.
4. Poll `GET /v8/neo/vault/login/status?state=...` to observe completion. The engine stores the newly provisioned credential and its refresh authority, and binds the account to the authenticated initiating owner.

`internal/neopool/http_login.go` narrows owner input, persists state/owner attribution and delegates to the existing management OAuth implementation; `BindLogin` provides the ownership hook. A deployed instance needs its correct public callback/trust configuration and completion of this **new engine-owned login**, not transfer of an existing profile's refresh token. Initiating and completing that deployment login are not claimed by this local receipt.

The parent owns Utopia deployment, integration and merge order. Native NeoPi connection, the prescribed GUI review loop and platform releases are separate workers' deliverables. This receipt proves the backend/user/account/meter/protocol lane, not completion of the entire deployed-product order.
