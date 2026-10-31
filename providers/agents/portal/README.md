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

## Chat model and reasoning

The conversation composer uses AgentKit's shared `AIModelReasoningPicker`, as
App Studio does. Model and effort choices apply only to the current chat.
`GET/PATCH agents/{name}/session/{sessionID}` reads and saves these preferences;
it never patches the Agent or its ModelCredential. Each run pins the effective
choice so approval resumes and recovery retain it. New chats, background runs,
and spawned workers continue to use the Agent defaults. Explicit selections
require access to the selected ModelCredential when saved and when a new chat
run starts.

Session reads wait for pending writes to that same chat, including across view
remounts. An unsent chat with saved preferences remains selected after reload.
Chats that inherit the Agent default refresh their model and effort options when
that default changes; explicit chat choices stay fixed. Model selection is disabled while a
preference request is pending, without closing the reasoning picker.

Effort choices come from the shared server-side model capability catalog.
Custom endpoints and unknown models show only model selection. Agents currently
uses Chat Completions, so reasoning levels requiring the Responses transport
are not offered. Effort is remembered separately for each credential in a chat.
Switching back to a credential whose remembered effort is no longer supported
resets that effort to model default; valid remembered choices are retained.

The first user message also starts a separate metadata request to generate a
thread title with the model selected for that chat. Title generation runs
asynchronously and does not delay the reply. When the saved title arrives, the
conversation header and thread rail update together; if generation fails, the
existing first-message preview remains the label.
The title attempt is one-shot: failures, timeouts, invalid output, or a temporary
store error leave the preview in place, and later turns do not retry.

Saved model preferences make an empty chat visible in the thread list, even before
its first message. Delete unused empty chats to free preference quota; opening
a new chat never silently evicts another chat’s saved choices. The list retains
up to 100 conversations and 100 empty chats independently, so drafts cannot
displace the available conversation history and remain accessible for cleanup.

## MCP Apps result views

Successful tool results may include a provider-owned MCP App view. The portal
keeps the app inside the conversation as a compact card and opens that same
result in a details pane when requested. The app owns its rich result UI; the
portal offers raw captured JSON only through an explicit disclosure. Source
references are attached to a citation only when the exact successful tool
receipt is present, and source-backed bytes or app HTML are requested again
through the attended MCP API before either view is mounted. Capture metadata is
the only artifact state cached by the browser.

Provider apps are default-deny: the attended backend supplies an independent
provider, resource, and content pin only after its operator-reviewed allowlist
and current resource checks pass. The portal requires that pin to match the app
metadata and verifies the exact HTML bytes before creating the frame. A missing
or mismatched pin leaves the authorized raw result available through the
explicit data disclosure, but never mounts app HTML. Only enable reviewed
providers whose apps are trusted with the full result and tool input after limited credential-like key-name filtering.
That filter cannot guarantee removal of secrets from arbitrary values.

This is a read-only host profile. It sends the recorded result text and
recorded arguments after limited credential-like key-name filtering to the iframe. The host grants no tools, resource
reads, prompts, sampling, downloads, chat messages, host navigation, or model
context updates, and rejects requests for those capabilities. App HTML must
match the independent backend-reviewed SHA-256 pin. The host inserts a restrictive CSP
and uses nested frames with opaque origins, scripts-only sandbox flags, no
referrer, and no host credentials. These controls are defense in depth, not a
claim that arbitrary HTML is harmless: a sandboxed app can still attempt to
navigate its own frame, and a later navigated document may make network
requests. App HTML must therefore be treated as provider-controlled code that
can see the complete disclosed result. Do not include secrets in app HTML or
captured results; only enable reviewed providers and account for the residual
own-frame navigation and network-request risk.

The static sandbox proxy is currently served only from the platform Agents
bundle path (`/ui/providers/agents`). The host context exposes the same
`basePath` for platform and org-owned bundles, so the portal checks the actual
Agents script URL and refuses to load the platform proxy when that URL carries
the host's `grant` query parameter. Org-owned Agents overrides need a
grant-aware auxiliary asset URL before MCP App frames can be enabled there.

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
