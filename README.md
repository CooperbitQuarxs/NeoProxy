# NeoProxy

NeoProxy is a fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).
It keeps CLIProxyAPI as the engine and replaces the management interface with a
new, unified web dashboard.

## What it is

- **The engine is CLIProxyAPI.** NeoProxy exposes OpenAI-, Anthropic- and
  Gemini-compatible endpoints and routes them to the providers the engine
  supports. The request handling, provider translators, SDK and management API
  are upstream's work; see [docs/upstream](docs/upstream/README.upstream.md) for
  the original documentation.
- **The interface is new.** The dashboard is designed in the same visual
  language as the rest of the Neo ecosystem, so that every Neo surface reads as
  part of one system rather than a collection of unrelated tools.

## Part of one ecosystem

NeoProxy is one of three pieces that are built to work together:

| Project | Role |
|---|---|
| **NeoPi** | An inference client. Connects with a user key and discovers granted models. |
| **NeoProxy** | Owns provider accounts, users and routing, and serves standard API endpoints. This repository. |
| **NeoDeck** | The team-facing web deck: inbox, workspaces, automations. Uses NeoProxy as its provider. |

The dashboard lives at `/neo/`. Models served by this proxy are named
`neoproxy/<provider>/<model>`. The admin shapes **virtual models**
(`neoproxy/default`, `neoproxy/heavy`, …) as chains of stages drawing from
proxy-owned pooled accounts. Users receive grants, allotments and scoped inference
keys; provider credentials stay in the proxy. Engine administration remains
available to organization-admin sessions, not ordinary member keys.

## Connect from a released NeoPi client

Download a native client from [NeoPi releases](https://github.com/PsychedelicShayna/neopi/releases)
that contains [native NeoProxy login](https://github.com/PsychedelicShayna/neopi/pull/602).
An older release without `npi login neoproxy --help` / `--url` is insufficient.
No repository clone, Bun or manual model/provider file is needed:

```sh
npi login neoproxy --url https://YOUR-PROXY-HOST
npi models neoproxy --json
npi
```

Use the administrator's actual HTTPS URL and answer the username/hidden-password
prompts. NeoPi mints and stores a scoped inference key, configures discovery and
logs out its temporary dashboard session. Owners explicitly narrow the key to
their owned accounts. Open `/model` to choose a granted NeoProxy model, or
`/models` to assign one through the normal role picker, then send a short prompt.
Use the exact discovered selector, not an invented alias. The full
[released-client instructions](docs/downloads.md#connect-using-a-released-neopi-client)
explain version requirements and the difference between model discovery and a
real reply.

## Downloads and setup

Choose an explicit version from [NeoProxy releases](https://github.com/PsychedelicShayna/NeoProxy/releases);
nightlies are pre-releases. Linux, Windows and macOS archives include their source
identity and must pass real SQLite bootstrap/session/restart checks before publication.
See [download, checksum, local setup and office connection instructions](docs/downloads.md).
An office member connecting to an existing service needs its HTTPS URL and user
credentials, not another locally running proxy.

The users/sessions, owned-account pools, virtual-model routing, dashboard and optional
Lua hook are implemented in the existing Go engine. The product contract and
delivery receipts live in [docs/design/backend-contracts.md](docs/design/backend-contracts.md)
and [docs/design/backend-plan.md](docs/design/backend-plan.md).

## Building

NeoProxy requires Go 1.26+ and a C compiler because its SQLite backend uses CGO:

```sh
CGO_ENABLED=1 go build -o neoproxy ./cmd/server
```

`CGO_ENABLED=0` can compile a binary with a nonfunctional SQLite stub; do not ship
or use it as an office backend. The portable Linux release keeps CGO enabled and
links musl statically instead. Configuration follows `config.example.yaml`;
[the setup instructions](docs/downloads.md) create a minimal private local instance
without shipping placeholder client keys.

## Credit and licence

NeoProxy is derived from
[router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI),
originally written by Luis Pater and maintained by Router-For.ME, released under
the MIT License. The upstream README is preserved verbatim at
[docs/upstream/README.upstream.md](docs/upstream/README.upstream.md), with its
Chinese and Japanese translations beside it.

This fork is also released under the [MIT License](LICENSE). The original
copyright notices are retained.
