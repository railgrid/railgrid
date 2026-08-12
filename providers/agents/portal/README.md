# Agents portal

Vite + TypeScript + [Lit](https://lit.dev) micro-frontend for the agents
provider, mounted in the faros portal under `/ui/providers/agents/`. The Go
binary embeds `portal/dist` via `assets.go`.

```
npm install
npm run build      # → portal/dist/main.js (single IIFE bundle, embedded at go build time)
npm run typecheck
npm test
```

## Host contract

There is **no iframe and no postMessage**. The host
(`portal/src/pages/ProviderFrame.vue`) injects `/ui/providers/agents/main.js`,
which registers the custom element `faros-provider-agents`, then appends the
element and assigns a `farosContext` **property** on it:

```ts
el.farosContext = { subPath, token, user, tenant, orgUUID, workspaceUUID, theme, basePath }
```

The element renders in **light DOM**, so the portal's `:root` design tokens
cascade in and light/dark themes match without any extra plumbing. Its own
stylesheet (`src/style.css`) is injected once, with every selector namespaced
under `faros-provider-agents`.

API calls go to `basePath` with `/ui/providers/` rewritten to
`/services/providers/` (the hub's service proxy), carrying the bearer token and
the `X-Faros-Org` / `X-Faros-Workspace` tenant headers — see
`src/portalkit/tenant.ts`. The host context is authoritative for the tenant; the
localStorage copy is only a fallback.

Navigation state lives in `location.hash` (`src/router.ts`), never in the host
router.

## Layout

```
src/
  main.ts               entry: registers the custom element + injects style.css
  element.ts            <faros-provider-agents> shell — nav, routing, store lifecycle
  api.ts                typed REST client + a spec-correct SSE reader
  store.ts              Slice<T> {data, loading, error} collections + /api/events subscription
  mutate.ts             the one write helper (optimistic → request → toast → refresh)
  router.ts             hash routes for the four tabs
  types.ts              entity + write DTO types, formatters
  conn-defs.ts          type-driven connection setup guides + shape/inbound derivation
  portalkit/            synced kit (tenant headers, icons, modal) — edit upstream, not here
  ui/                   base classes, icon adapter, toasts, markdown, slice states
  views/                one component per surface (agents, agent config/chat, activity,
                        run detail, connections, toolsets, models, automation)
  test/                 vitest component + store tests
```

Four tabs: **Agents** · **Activity** · **Connections** (toolsets are a section)
· **Models**. Schedules and triggers are owned by their agent and edited in the
agent's Config pane, next to a live chat playground.
