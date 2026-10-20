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
| **NeoPi** | The core. Holds the credentials and provides inference and authentication to everything else. |
| **NeoProxy** | Serves that inference as standard API endpoints to any client or SDK. This repository. |
| **NeoDeck** | The team-facing web deck: inbox, workspaces, automations. Uses NeoProxy as its provider. |

In the NeoProxy dashboard, NeoPi is the default and first provider. The other
providers the engine supports remain available and are listed under
"Also supported".

## Status

Early. The engine is production-tested upstream; the new dashboard is in active
development and some of its pages are marked as not implemented yet.

## Building

NeoProxy is a Go program. Build it like upstream:

```sh
go build -o neoproxy ./cmd/server
```

Configuration follows upstream's `config.example.yaml`.

## Credit and licence

NeoProxy is derived from
[router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI),
originally written by Luis Pater and maintained by Router-For.ME, released under
the MIT License. The upstream README is preserved verbatim at
[docs/upstream/README.upstream.md](docs/upstream/README.upstream.md), with its
Chinese and Japanese translations beside it.

This fork is also released under the [MIT License](LICENSE). The original
copyright notices are retained.
