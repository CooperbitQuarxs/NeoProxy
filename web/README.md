# NeoProxy dashboard

The `/neo/` dashboard is a React + TypeScript app built with Vite. `npm run build` writes the production bundle into `../internal/neoui/static`, which the Go binary embeds. The built files are committed, so `go build` needs no Node toolchain.

## Develop against a running NeoProxy

```sh
cd web
npm install
NEOPROXY_URL=http://127.0.0.1:8318 npm run dev
```

Open http://127.0.0.1:5173/neo/. API requests (`/v8`, `/v1`, `/healthz`) are proxied to `NEOPROXY_URL` with its own `Origin`, so the server's same-origin checks accept them, and you sign in with a real dashboard account.

## Build

```sh
npm run build
```

This type-checks, then replaces `internal/neoui/static` entirely. Commit the result together with the source change.

## Layout

| Path | Contents |
| --- | --- |
| `src/app` | Shell, sidebar, sign-in and first-time setup, hash router (old `#/accounts`-style links redirect to the new pages) |
| `src/api` | Fetch client, types for `/v8/neo/access`, TanStack Query hooks |
| `src/ui` | Shared components (styled Radix primitives), icons, toasts |
| `src/pool` | Pool page: the provider-access mosaic, details sheet and the single "Add provider access" flow |
| `src/pages` | Models, People, Activity and System |
| `src/styles` | Design tokens (`base.css`), component, shell and pool styles |

Design rules: one icon per concept (the key glyph only for people's inference keys), styled controls instead of native popups, every dialog says what it does and what happens next, and provider color plus source texture carry the pool's meaning.
