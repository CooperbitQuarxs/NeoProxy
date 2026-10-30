# Private office services

The units in [`deploy/systemd`](../deploy/systemd/) run two actual products as a
non-root user:

- `neoproxy-office.service` runs the NeoProxy HTTPS server and owns its account,
  user, routing and usage state.
- `npi-office.socket` starts a real `npi --mode rpc --no-ui` agent for each private
  Unix-socket connection. This is not an authentication-broker-only service.

Use a persistent user manager (`loginctl enable-linger USER`, once, as root).
There is no NixOS rebuild or change to another service's configuration.

## Files and identity

Install official release binaries in versioned directories, with `current`
symlinks pointing to the selected versions:

```text
~/.local/lib/neoproxy-office/VERSION/neoproxy
~/.local/lib/neoproxy-office/current
~/.local/lib/npi-office/VERSION/npi
~/.local/lib/npi-office/current
~/.config/neoproxy-office/config.yaml
~/.config/neoproxy-office/npi.env
~/.local/share/neoproxy-office/pki/ca.crt
~/.local/share/neoproxy-office/pki/server.crt
~/.local/share/neoproxy-office/pki/server.key
~/.local/share/neoproxy-office/npi-profile/
~/.local/share/neoproxy-office/workspace/
~/.local/share/neoproxy-office/tmp/
~/.local/state/neoproxy-office/auths/
```

Keep configuration, profile, state and private-key directories mode `0700`, and
secret-bearing files mode `0600`. Do not copy an existing NeoPi profile or an
authentication broker's refresh tokens. The service profile is deliberately
separate from the user's ordinary `~/.omp` profile.

The first officially published NeoPi build containing native NeoProxy login is
[`v18.10.3-nightly.fd5698a0772a.41001835518.1`](https://github.com/PsychedelicShayna/neopi/releases/tag/v18.10.3-nightly.fd5698a0772a.41001835518.1),
from commit `fd5698a0772a555cb554b860d8f3208a0f7026a5`. The unqualified version
`18.10.3` is insufficient: earlier builds with that version lack this feature.
Verify downloaded assets against that release's `SHA256SUMS.txt` before running
them. No source checkout or Bun installation is needed to use the native binary.

## NeoProxy and TLS

The server configuration uses canonical v8 fields:

- `server.host`: the specific private VPN address, not an all-interface bind.
- `server.port`: the chosen unoccupied port.
- `server.tls.enable`: `true`.
- `server.tls.cert` and `server.tls.key`: absolute certificate and key paths.
- `oauth.auth-dir`: the absolute private engine credential directory above.
- `management.secret-key`: a separately generated operator credential.

`auth.dir` is not the credential-directory setting. An omitted effective
`oauth.auth-dir` falls back to `~/.cli-proxy-api`; that would defeat the intended
profile separation. Verify the effective path before importing credentials.
Systemd `%h` and `%S` specifiers belong in unit files, not server YAML.

If the certificate uses a private CA, transfer the CA certificate through an
already authenticated channel. Verify its identity and the certificate's IP or
DNS SAN. Trust it only for the intended client processes, for example with
`NODE_EXTRA_CA_CERTS`; do not disable certificate checking or alter the user's
global trust store. A verified health probe is:

```sh
curl --cacert /path/to/office-ca.crt https://VPN_HOST:PORT/healthz
```

Initial administrator bootstrap requires the operator credential and the
management remote-access policy. When bootstrapping through the VPN listener,
permit authenticated remote management on that private bind. Dashboard user
sessions, owner permissions and scoped inference keys remain separate from the
operator credential. Never give an inference client the operator secret.

Provision an independent engine-owned provider credential. An access-only lease
can prove an inference path, but its expiry does not prove durable refresh.
Complete provider consent normally; do not reuse a competing broker's refresh
authority. Assign the resulting account to its owner, configure explicitly named
local metered windows, create the desired virtual model and grant it to the
member identity used by the agent. A local token meter is not a vendor quota.

## Native client and agent profile

Use native NeoPi login with the dedicated profile and process-scoped CA trust:

```sh
PI_CODING_AGENT_DIR="$HOME/.local/share/neoproxy-office/npi-profile" \
NODE_EXTRA_CA_CERTS="$HOME/.local/share/neoproxy-office/pki/ca.crt" \
  "$HOME/.local/lib/npi-office/current/npi" login neoproxy --url https://VPN_HOST:PORT
```

The command prompts for username and a hidden password. It stores the minted
scoped inference key, not the dashboard password or session cookie. Owners must
explicitly select their owned account pool. Discover models with `npi models
neoproxy --json`; use the returned native selector in `NPI_MODEL` below, or assign
it to a role through the existing `modelRoles` setting.

Create `~/.config/neoproxy-office/npi.env` with the selected model, for example:

```ini
NPI_MODEL=neoproxy/neoproxy/office-cheap
```

The repeated prefix is intentional: the provider is `neoproxy`, and the server's
model ID is `neoproxy/office-cheap`. Do not rewrite the server ID or hand-author a
model catalogue.

On NixOS, the official GNU/Linux binary can use the host's existing `nix-ld`.
Include the host's actual `NIX_LD` and `NIX_LD_LIBRARY_PATH` in this dedicated
`npi.env`, plus a `PATH` containing its system tools and a `TMPDIR` under the
private disk-backed directory above. Verify `npi --version` and
`npi --smoke-test` with that environment. Do not hand-assemble another binary or
change the global Nix daemon's limits.

## Start, use and stop

Copy the three unit files to `~/.config/systemd/user/`, then run:

```sh
systemctl --user daemon-reload
systemctl --user enable --now neoproxy-office.service npi-office.socket
systemctl --user status neoproxy-office.service npi-office.socket
```

NeoProxy retries a failed start, including a private VPN address not yet being
available during boot. The socket is mode `0600` inside a mode `0700` directory
at `$XDG_RUNTIME_DIR/neoproxy-office/npi.sock`; it is not an Internet listener.
Each accepted connection gets its own templated `npi-office@.service` process.
The workspace and profile remain persistent when the connection ends.

RPC uses newline-delimited JSON. Read the `ready` frame, send a `prompt` command
with an ID, and wait for its successful admission response. A response alone is
not a model answer. Keep the write half open until the matching `prompt_result`
reports `status: "completed"`; if `sessionSettled` is false, also wait for
`session_settled`. Then close the write half and continue reading through EOF to
drain pending output. Closing stdin is session termination, not a request to
finish an admitted generation. The public protocol is documented in
[NeoPi's RPC guide](https://github.com/PsychedelicShayna/neopi/blob/fd5698a0772a555cb554b860d8f3208a0f7026a5/docs/rpc.md).

Do not run this RPC process as an ordinary service with `/dev/null` stdin: EOF
correctly ends it. Socket activation supplies the real connection as stdin and
stdout; stderr goes to the journal without contaminating the protocol stream.

To stop only these services:

```sh
systemctl --user stop npi-office.socket neoproxy-office.service
```

For an upgrade, retain the prior version directory, atomically switch each
`current` symlink, and restart only the affected service. Keep the previous
binary until the new one launches and an authenticated request succeeds. Stop
existing `npi-office@` connections before replacing their selected version if a
fully drained cutover is required. Never reset unrelated configuration, account
state, keys or services to make a deployment check pass.
