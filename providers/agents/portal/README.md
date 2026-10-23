# Agents portal

Vite + TypeScript + [Vue 3](https://vuejs.org) micro-frontend for the agents
provider, mounted in the railgrid portal under `/ui/providers/agents/`. The Go
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
which registers the custom element `railgrid-provider-agents`, then appends the
element and assigns a `railgridContext` **property** on it:

```ts
el.railgridContext = { subPath, token, user, tenant, orgUUID, workspaceUUID, theme, basePath }
```

The element renders in **light DOM**, so the portal's `:root` design tokens
cascade in and light/dark themes match without any extra plumbing. Its own
stylesheet (`src/style.css`) is injected once, with every selector namespaced
under `railgrid-provider-agents`.

Tenant objects are read and written straight against kcp on the hub's
`/clusters/<tenant>/…` front door (`src/resources.ts`, portalkit's kube client).
Provider verbs — chat, run, trace, test, … — are kcp custom subresources on
those same objects, addressed with `kubeVerbPath` from `src/portalkit/kube.ts`
(`/clusters/<tenant>/apis/agents.railgrid.ai/v1alpha1/<resource>/<name>/<verb>`)
and fetched through the host-owned transport, which injects the bearer. Only
`/oauth/providers` and `/mcp` still go to `basePath` with `/ui/providers/`
rewritten to `/services/providers/` (the hub's service proxy) — see
`src/portalkit/tenant.ts`. The host context is authoritative for the tenant; the
localStorage copy is only a fallback.

Navigation state lives in `location.hash` (`src/router.ts`), never in the host
router.

## MCP Apps result views

Successful tool results may include a provider-owned MCP App view. The portal
keeps the app inside the conversation as a compact card and opens that same
result in a details pane when requested. The app owns its rich result UI; the
portal offers raw captured JSON only through an explicit disclosure. Source
references are attached to a citation only when the exact successful tool
receipt is present, and source-backed bytes or app HTML are requested again
through the attended MCP API before either view is mounted. Capture metadata is
the only artifact state cached by the browser.

This is a read-only host profile. It sends the recorded result text and
redacted recorded arguments to the iframe. The host grants no tools, resource
reads, prompts, sampling, downloads, chat messages, host navigation, or model
context updates, and rejects requests for those capabilities. App HTML must
match the backend-provided SHA-256 digest. The host inserts a restrictive CSP
and uses nested frames with opaque origins, scripts-only sandbox flags, no
referrer, and no host credentials. These controls are defense in depth, not a
claim that arbitrary HTML is harmless: a sandboxed app can still attempt to
navigate its own frame, and a later navigated document may make network
requests. Do not include secrets in app HTML or captured results; review the
specific provider content and browser policy before enabling a provider app.

## Layout

```
src/
  main.ts               entry: registers the custom element + injects style.css
  element.ts            thin <railgrid-provider-agents> Vue mount boundary
  App.vue               nav, routing, authority rotation, and store lifecycle
  api.ts                typed REST client + a spec-correct SSE reader
  store.ts              Slice<T> {data, loading, error} collections + /api/events subscription
  mutate.ts             the one write helper (optimistic → request → toast → refresh)
  router.ts             hash routes for the four tabs
  types.ts              entity + write DTO types, formatters
  conn-defs.ts          type-driven connection setup guides + shape/inbound derivation
  portalkit/            synced Vue kit + shared tenant/style helpers — edit upstream, not here
  ui/                   framework-neutral toast integration
  vue/                  Vue runtime bridge + framework-neutral chat projection helpers
  views/                Vue components for each surface (agents, agent config/chat, activity,
                        run detail, connections, toolsets, models, automation)
  test/                 vitest component + store tests
```

Four tabs: **Agents** · **Activity** · **Connections** (toolsets are a section)
· **Models**. Schedules and triggers are owned by their agent and edited in the
agent's Config pane, next to a live chat playground.
