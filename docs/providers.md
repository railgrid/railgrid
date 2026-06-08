# Providers — extending kedge with provider UIs, virtual workspaces, and APIs

**Status:** Design draft (ready for phase-1 implementation)
**Owner:** TBD
**Last updated:** 2026-05-22

---

## Restore-from-reboot summary

> This section exists so a fresh Claude Code session (or a returning human)
> can pick up the work without re-reading the conversation history.

**Where we are:** design phase complete. No code written yet. The branch is
`mcp.example` on a clean tree apart from this doc and a stray `bob` file.

**Goal in one sentence:** make kedge pluggable so third parties can ship
"providers" that bring an `APIExport`, optional UI, optional backend HTTP
service, and optional controllers — all installed via Helm, discovered and
wired up by hub controllers, surfaced in the portal under
`/providers/{name}`, proxied to avoid CORS.

**Decisions already pinned** (don't re-litigate; jump to §"Hub changes" for
the how):

| # | Decision | Rationale |
|---|---|---|
| 0 | API group = **`providers.kedge.faros.sh`** (separate from `kedge.faros.sh`) | Catalog entries and bindings are platform-owner-only. Excluding them from the `core.faros.sh` merged APIExport keeps them out of tenant workspaces. Tenants interact via portal/hub mediation, not raw CR access |
| 1 | Terminology = **provider** (not "addon") | `root:kedge:providers` already exists; first-party kedge `APIExport`s already live there |
| 2 | UI embedding = **iframe via hub proxy** | Same-origin → no CORS. Any frontend stack. Module Federation rejected (Vue lock-in + build coupling) |
| 3 | Provider workspace = `root:kedge:providers:{name}`, **auto-created by hub** on `CatalogEntry` admission | Chart needs no kcp credentials |
| 4 | Distribution = **one Helm chart per provider**, targets *host cluster only* | All kcp work owned by hub catalog controller |
| 5 | Registration = **hybrid**: chart creates `CatalogEntry` shell; provider pod heartbeats every 30s (`POST /api/providers/{name}/heartbeat`, TTL 90s) | Declarative install + runtime liveness |
| 6 | VW = **APIExport-only by default**; `spec.virtualWorkspace.url` is an opt-in escape hatch under `/services/providers/{name}/vw/*` | Most providers won't need a VW; lowers bar |
| 7 | Provider→kcp identity = SA `provider` in the provider's workspace; hub mints kubeconfig and writes it as Secret `kedge-provider-kubeconfig` in the provider's host namespace; **24h token rotation** | Reuses existing exec-credential pattern from `pkg/server/proxy/proxy.go` |
| 8 | Schema delivery = **inline** in `CatalogEntry.spec.apiExport.schemas[].body`; hub parses + applies | Solves chicken-and-egg of "chart can't apply to workspace that doesn't exist yet" |
| 9 | PermissionClaim acceptance = **auto-accept-all** at Enable time, but ONLY for claims marked `tenantScoped: true`. Non-tenant-scoped claims refused unless admin sets `kedge.faros.sh/accept-untrusted-claims=true` on the `CatalogEntry` | Simplest safe default; per-claim toggles deferred to v2 |
| 10 | Tenant Enable = **direct kcp `APIBinding` in the tenant workspace**. No `ProviderBinding` CRD — kcp-native. Catalog controller grants tenants `bind` verb on each provider's APIExport once the provider is Ready. Permission-claim safety enforced by `MaximalPermissionPolicy` on the APIExport (kcp). | Simpler, kcp-native; fewer moving parts. Audit/inventory queries fan out across tenant workspaces (acceptable). |

**Deferred (do NOT block phase 1):**

- GraphQL discovery of provider CRs after `APIBinding` lands — **must work
  by end of phase 3**; gateway already does APIExport-based discovery for
  first-party CRs, so expected to "just work", but needs validation. If it
  doesn't, file follow-up; do not gate phase 1–2.
- Cross-provider dependencies — **explicitly out of scope** for v1. A
  provider's controller can error out if its prerequisite APIExport isn't
  bound.
- Heartbeat over kcp leases instead of HTTP — possible v2 simplification.
- Per-permission-claim UI toggles — v2.

**Next concrete step:** implement phase 1. See §"Phase 1 implementation
plan" for the backend file-by-file checklist; phase 2 (portal wiring) is
detailed under §"Portal changes" + §"Phase 2 implementation plan".

**Portal integration anchors** (referenced throughout):

- Layout + side nav: [portal/src/components/AppLayout.vue](../portal/src/components/AppLayout.vue) — hardcoded `navItems` at lines 48-53 becomes computed
- Bootstrap point: [portal/src/App.vue](../portal/src/App.vue) — auth detect + load providers store before render
- Static routes: [portal/src/router/index.ts](../portal/src/router/index.ts)
- GraphQL queries: [portal/src/graphql/queries/](../portal/src/graphql/queries/) (new `providers.ts`)
- Dev proxy: [portal/vite.config.ts](../portal/vite.config.ts)
- CSP injection point: [pkg/hub/portal.go](../pkg/hub/portal.go) — middleware around the embedded SPA handler

---

## Goal

Make kedge a pluggable platform. A *provider* is a self-contained extension
that brings:

1. An **`APIExport`** in kcp that user tenants bind to consume the
   provider — the *one required piece*.
2. A **UI** (micro-frontend, any stack) shown inside the kedge portal —
   optional.
3. Optional **controllers** reconciling the provider's resources.
4. Optional **custom HTTP backend** (REST/GraphQL/WebSocket) for the UI
   to talk to, proxied through the hub.
5. Optional **virtual workspace** (advanced) for non-CRD verbs.

A user opens the portal, browses the "Providers" view (catalog), clicks
**Enable** on a provider, and:

- The provider's APIs become available in their tenant workspace via an
  `APIBinding`.
- The provider's UI (if any) appears under `/providers/{name}` in the
  portal — proxied through the hub, so it is same-origin and there are no
  CORS concerns.

## Why "provider" (terminology)

The kcp workspace `root:kedge:providers` already exists and is where
kedge's own `APIExport`s live (`kedge.faros.sh`, `tenancy.kedge.faros.sh`,
`core.faros.sh`). See
[config/kcp/workspace-providers.yaml](../config/kcp/workspace-providers.yaml)
and [config/kcp/embed.go](../config/kcp/embed.go).

A third-party provider therefore lives at `root:kedge:providers:{name}` —
sibling to the first-party providers, with identical mechanics. No new
top-level workspace, no new vocabulary.

## Non-goals (v1)

- Hot-reloading provider controllers inside the hub process (providers run
  as separate Deployments).
- Cross-provider dependency resolution / version compatibility matrices.
- A public provider marketplace / registry. Distribution is Helm chart +
  `kubectl apply`.
- Per-provider auth policies (single OIDC at the hub).
- Per-permission-claim consent UI (v1: accept-all on Enable; per-claim
  toggles deferred to v2).

---

## Architecture overview

```
┌──────────────────────────────────────────────────────────────────────┐
│                            kedge-hub                                  │
│                                                                       │
│  /ui/*                       → embedded SPA (Vue portal)              │
│  /ui/providers/{p}/*         → reverse proxy → catalog.spec.ui.url    │
│  /services/providers/{p}/*   → reverse proxy → catalog.spec.backend.url│
│  /clusters/*, /services/agent-proxy, /services/mcp ... (unchanged)    │
│  /api/providers/{p}/heartbeat (POST, provider-SA-authed)              │
│                                                                       │
│  Catalog controller: watches CatalogEntry                     │
│    - auto-creates root:kedge:providers:{p} sub-workspace              │
│    - creates `provider` ServiceAccount in that workspace              │
│    - writes kedge-provider-kubeconfig Secret to provider's namespace  │
│    - applies inline bootstrap (APIResourceSchema, APIExport)          │
│    - rebuilds proxy routing table; tracks heartbeats                  │
│                                                                       │
│  Tenants APIBind to provider APIExports DIRECTLY in their workspace   │
│    - Portal calls kcp as the user to create the APIBinding            │
│    - Catalog controller pre-grants tenants `bind` verb cluster-wide   │
│    - Permission safety = MaximalPermissionPolicy on the APIExport     │
└──────────────────────────────────────────────────────────────────────┘
        │ kcp                              │ HTTP (in-cluster Service)
        ▼                                  ▼
┌────────────────────────────┐   ┌────────────────────────────────────┐
│ root:kedge:providers:cost  │   │ Provider pod (e.g. cost)           │
│   APIExport cost.faros.sh  │◄──│   - mounts kedge-provider-kubeconfig│
│   APIResourceSchema(s)     │   │   - runs controllers against kcp    │
│   SA: provider             │   │   - serves UI on :3000 (optional)   │
└────────────────────────────┘   │   - serves backend HTTP on :8080    │
        ▲                        │   - heartbeats hub every 30s        │
        │ APIBinding (kcp        └────────────────────────────────────┘
        │ serves natively)
┌────────────────────────────┐
│ root:kedge:tenants:alice   │
│   (user workspace — sees   │
│    cost CRs natively)      │
└────────────────────────────┘
```

Single origin from the browser's perspective: every request goes to
`kedge.example.com`. The hub fans out to providers internally.

**Key clarification on traffic flow:** provider CRs are served by kcp via
the normal `/clusters/...` path on the hub — the same flow as kedge's own
CRDs today. The `/services/providers/{name}` proxy is *only* for the
provider's own custom HTTP backend (REST/GraphQL/WS), not for CR traffic.

---

## CRDs

Two new CRDs, both in the kedge API group, both first-party (added to the
existing `kedge.faros.sh` `APIExport`).

### `CatalogEntry` (cluster-scoped, in `root:kedge:providers`)

Installed by an administrator via the provider's Helm chart, which targets
the host Kubernetes cluster API. The hub's catalog controller projects it
into kcp.

```yaml
apiVersion: providers.kedge.faros.sh/v1alpha1
kind: CatalogEntry
metadata:
  name: cost-insights
spec:
  displayName: "Cost Insights"
  description: "Per-edge cost attribution and forecasting."
  vendor: "Acme Cloud"
  version: "1.2.0"
  iconURL: "/ui/providers/cost-insights/icon.svg"  # served via UI proxy

  # Host-cluster namespace where the provider Deployment runs. Hub writes
  # the kedge-provider-kubeconfig Secret here.
  serviceAccountNamespace: "cost-insights"

  # OPTIONAL: micro-frontend. Omit if provider has no UI.
  ui:
    url: "http://cost-insights-ui.cost-insights.svc.cluster.local"
    indexPath: "/"

  # OPTIONAL: custom HTTP backend (NOT for CR traffic — CRs go via kcp).
  # Omit if provider only exposes CRs.
  backend:
    url: "http://cost-insights.cost-insights.svc.cluster.local:8080"
    healthPath: "/healthz"

  # OPTIONAL: opt-in to serving a kcp virtual workspace for non-CRD verbs.
  # Omit for v1 — only needed if provider needs custom resource verbs.
  virtualWorkspace:
    url: "http://cost-insights.cost-insights.svc.cluster.local:6443"

  # REQUIRED: the APIExport the provider owns. Hub creates the workspace,
  # applies the inline schema(s), then creates the APIExport.
  apiExport:
    name: "cost.faros.sh"
    # Inline APIResourceSchema docs the hub applies on first reconcile.
    # Multiple schemas allowed; one APIExport references them all.
    schemas:
      - groupResource: "greetings.cost.faros.sh"
        # The full v1alpha1 APIResourceSchema body as a string. Hub parses
        # and applies. Kept inline so the chart needs no kcp access.
        body: |
          apiVersion: apis.kcp.io/v1alpha1
          kind: APIResourceSchema
          metadata:
            name: v260522-abc.greetings.cost.faros.sh
          spec: { ... }
    # PermissionClaims declared on the APIExport itself (kcp-enforced).
    # Mirrored here as informational for the Enable dialog.
    permissionClaims:
      - resource: configmaps
        verbs: [get, list, watch]
        # Tenant-scoped flag tells the binding controller this is safe to
        # auto-accept. Out-of-tenant claims are refused.
        tenantScoped: true

status:
  # Filled by catalog controller
  workspace: "root:kedge:providers:cost-insights"
  apiExportRef:
    workspace: "root:kedge:providers:cost-insights"
    name: "cost.faros.sh"
  endpoints:
    ui: "http://cost-insights-ui.cost-insights.svc.cluster.local"
    backend: "http://cost-insights.cost-insights.svc.cluster.local:8080"

  # Filled by heartbeat. provider.Ready = true iff heartbeat within TTL
  # AND (no backend declared OR backend healthz is 200).
  lastHeartbeat: "2026-05-22T10:15:00Z"
  reportedVersion: "1.2.0"
  ready: true

  conditions:
    - type: WorkspaceReady
    - type: APIExportReady
    - type: BackendHealthy   # only present if .spec.backend set
    - type: Ready
```

### Tenant Enable = direct kcp `APIBinding` (no second CRD)

We deliberately do NOT ship a `ProviderBinding` CRD. Tenants enable a
provider by creating a vanilla kcp `APIBinding` in their own workspace,
pointing at the provider's `APIExport`. This is the kcp-native pattern;
adding a second CRD would only re-wrap what `APIBinding` already does.

```yaml
# Created in the tenant's workspace (e.g. root:kedge:tenants:alice)
# by the portal, calling kcp as the user when they click Enable.
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: cost-insights
spec:
  reference:
    export:
      path: "root:kedge:providers:cost-insights"
      name: "cost.faros.sh"
  permissionClaims:
    - resource: configmaps
      verbs: [get, list, watch]
      state: Accepted
```

**Why this works safely:**

- **Tenants need `bind` verb on the provider's `APIExport`.** kcp doesn't
  grant it by default. The hub's catalog controller pre-grants
  `bind` cluster-wide for each provider once its `CatalogEntry`
  reaches Ready (via a `ClusterRole` aggregated to the tenant identity).
  Without this grant, the tenant's `APIBinding` create fails with 403.
- **Permission claims are gated by kcp's `MaximalPermissionPolicy`** on
  each provider's `APIExport`. A tenant cannot accept a claim outside
  their workspace because the export's `MaximalPermissionPolicy` refuses.
  The provider chart declares the maximum claim set; users pick from it.
- **Audit and inventory** ("who enabled X?") = list `APIBindings` across
  tenant workspaces filtered by `reference.export.path`. Acceptable at
  current scale; revisit if it ever isn't.
- **Uninstall** (admin deletes `CatalogEntry`) leaves orphan
  `APIBindings` in tenant workspaces — kcp flips them NotReady (broken
  reference). The catalog controller's deletion hook walks tenant
  workspaces and removes them.
- **Disable** = tenant deletes their own `APIBinding`. No special API.

---

## Hub changes

### 1. Catalog controller (`pkg/hub/controllers/providercatalog/`)

Watches `CatalogEntry` in `root:kedge:providers`. On each
reconcile:

1. **Sub-workspace**: ensure `root:kedge:providers:{name}` exists. Use
   the existing kcp tenancy client. Created with type `universal`,
   `bootstrap.kcp.io/create-only: "true"`.
2. **Provider ServiceAccount**: ensure a `ServiceAccount` named
   `provider` exists in that workspace, bound to `cluster-admin` on the
   workspace (admin within its own sandbox, nothing outside).
3. **Kubeconfig Secret**: mint a token for the SA, build an exec-credential
   kubeconfig pointing at the hub URL with cluster
   `root:kedge:providers:{name}`, write it as Secret
   `kedge-provider-kubeconfig` in `spec.serviceAccountNamespace` of the
   *host* cluster. Idempotent. Rotate token every 24h (set
   `kubernetes.io/service-account-token` style annotation).
4. **Schema + APIExport apply**: parse `spec.apiExport.schemas[].body`,
   apply each as an `APIResourceSchema` in the workspace, then
   apply/update the `APIExport` referencing them.
5. **Registry upsert**: push (Name, UIURL, BackendURL, VWURL, Ready) into
   the in-process `Registry` (below).

The controller runs in the hub. It uses the hub's existing controller
manager and the kcp admin client.

### 2. In-memory routing registry (`pkg/hub/providers/`)

```go
type Registry struct {
    mu     sync.RWMutex
    byName map[string]*Provider
}

type Provider struct {
    Name       string
    UIURL      *url.URL  // may be nil
    BackendURL *url.URL  // may be nil
    Ready      bool
    Version    string
}

func (r *Registry) Get(name string) (*Provider, bool)
func (r *Registry) List() []*Provider
func (r *Registry) Upsert(p *Provider)
func (r *Registry) Delete(name string)
```

Pure in-memory; rebuilt on hub restart from the `CatalogEntry`
list. No external store.

### 3. Heartbeat endpoint

```
POST /api/providers/{name}/heartbeat
Authorization: Bearer <provider-SA-token>
Content-Type: application/json

{ "version": "1.2.0", "buildTime": "...", "status": "healthy" }
```

- Authenticates the bearer token against the SA in
  `root:kedge:providers:{name}`. Rejects any other identity.
- Updates `CatalogEntry.status.lastHeartbeat` and
  `reportedVersion`.
- TTL: 90 seconds. Catalog controller flips `Ready=false` if no heartbeat
  within TTL.
- Cheap: providers heartbeat every 30s; tiny payload.

### 4. Generic provider proxy

Two route prefixes registered in [pkg/hub/server.go](../pkg/hub/server.go):

```go
// New paths in pkg/api/url/paths.go
const (
    PathPrefixProvidersUI      = "/ui/providers"
    PathPrefixProvidersBackend = "/services/providers"
)

router.PathPrefix(apiurl.PathPrefixProvidersUI + "/").Handler(
    providers.NewUIProxy(registry, logger))
router.PathPrefix(apiurl.PathPrefixProvidersBackend + "/").Handler(
    providers.NewBackendProxy(registry, authMiddleware, logger))
```

Proxy behavior:

- Parse `{name}` from path: `/ui/providers/cost-insights/foo` → name=`cost-insights`, rest=`/foo`.
- Look up in registry; **404** if unknown, **503** if not Ready.
- Backend proxy: requires standard kedge auth middleware; forwards the
  user's `Authorization` header and adds `X-Kedge-User`, `X-Kedge-Tenant`.
- UI proxy: no auth requirement on static assets; injects
  `X-Kedge-Base-Path: /ui/providers/{name}` so the provider can rewrite
  absolute links.
- Standard `httputil.ReverseProxy` with header sanitization.

Note: if `spec.virtualWorkspace.url` is set, the backend proxy also
recognizes a `/services/providers/{name}/vw/*` sub-path and routes it to
the VW URL instead. This is the opt-in advanced path.

### 5. Catalog controller's RBAC + enable plumbing

When the catalog controller (`pkg/hub/controllers/providercatalog/`)
reconciles a `CatalogEntry`, it additionally:

1. **Grants tenants `bind` verb on the provider's `APIExport`.**
   The controller creates / updates a `ClusterRole` named
   `kedge:providers:bind:{name}` in the provider's workspace with rules
   `[apiGroups: ["apis.kcp.io"], resources: ["apiexports"], verbs: ["bind"], resourceNames: ["{name}"]]`,
   and a `ClusterRoleBinding` aggregating that role to the tenant-identity
   group (`system:authenticated` is too broad — we use the same identity
   subject used by the existing tenant `APIBinding` to `core.faros.sh`).
2. **Sets `MaximalPermissionPolicy` on the provider's `APIExport`** to
   the union of claims declared in
   `CatalogEntry.spec.apiExport.permissionClaims` that are marked
   `tenantScoped`. This is the kcp-enforced safety wall: tenants cannot
   accept a claim that escapes their workspace.
3. **Cleanup on delete.** When the `CatalogEntry` is deleted, the
   controller walks tenant workspaces, lists `APIBindings` whose
   `reference.export.path` matches this provider's workspace, and deletes
   them. Best-effort; orphans flip NotReady on their own anyway.

There is no separate "binding reconciler" — the tenant's `APIBinding`
itself is the reconciled state, and kcp handles its lifecycle.

### 6. Bootstrap

The kcp bootstrap in [pkg/hub/bootstrap](../pkg/hub/bootstrap) already
creates `root:kedge:providers`. We add:

- `APIResourceSchema` and `APIExport` for `CatalogEntry` in the
  `providers.kedge.faros.sh` group (admin-only — bound only in
  `root:kedge:providers`, never in tenant workspaces, hence excluded from
  the merged `core.faros.sh` APIExport).
- New embed paths for these schemas in
  [config/kcp/embed.go](../config/kcp/embed.go).

No new workspaces in bootstrap; provider sub-workspaces are created
lazily on `CatalogEntry` admission.

---

## Portal changes

The portal is Vue 3 + Pinia + urql + Vite, with a single shared layout
([portal/src/components/AppLayout.vue](../portal/src/components/AppLayout.vue))
that every page wraps. Routes are static today
([portal/src/router/index.ts](../portal/src/router/index.ts)) and the side
nav reads a hardcoded `navItems` const at
[portal/src/components/AppLayout.vue:48-53](../portal/src/components/AppLayout.vue#L48-L53).
Both become provider-aware.

### Files to create

| Path | Purpose |
|---|---|
| `portal/src/stores/providers.ts` | Pinia store: catalog list, current user's bindings, derived nav items, route registration |
| `portal/src/router/providers.ts` | `registerProviderRoutes(bindings)` — idempotent `router.addRoute()` calls |
| `portal/src/graphql/queries/providers.ts` | `LIST_PROVIDER_CATALOG_ENTRIES`, `LIST_PROVIDER_BINDINGS`, plus result types |
| `portal/src/pages/ProvidersPage.vue` | The `/providers` catalog view (grid of cards, Enable/Disable) |
| `portal/src/pages/ProviderFrame.vue` | Per-provider iframe host; handles postMessage handshake, loading state, theme propagation |
| `portal/src/components/ProviderEnableDialog.vue` | Modal listing `permissionClaims` (read from `CatalogEntry.spec.apiExport.permissionClaims` via `/api/providers`); on confirm, the portal POSTs an `APIBinding` directly to kcp in the user's workspace with the claims marked `Accepted` |
| `portal/sdk/index.ts` (new package `@kedge/provider-sdk`) | `useKedge()` composable for providers' UIs: token, user, tenant, theme, `onNavigate` |
| `portal/sdk/package.json`, `tsconfig.json`, `README.md` | SDK packaging — publish to npm or include as workspace |

### Files to edit

| Path | Edit |
|---|---|
| [portal/src/App.vue](../portal/src/App.vue) | After `auth.detectAuthMode()`, if authenticated, `await providersStore.load()` before rendering `<router-view />`. Show loading spinner during. This guarantees dynamic routes exist *before* Vue tries to match a deep link like `/providers/cost/foo`. |
| [portal/src/router/index.ts](../portal/src/router/index.ts) | Add static catalog route `{ path: '/providers', name: 'providers', component: () => import('@/pages/ProvidersPage.vue') }` **before** the `:pathMatch(.*)*` not-found route at line 62. Provider sub-routes added dynamically by the store. |
| [portal/src/components/AppLayout.vue](../portal/src/components/AppLayout.vue) | Replace the static `navItems` array (lines 48-53) with a `computed` that merges static items with `providersStore.enabledNavItems`. Add a static "Providers" entry (catalog browser) before the dynamic block. Render dynamic items with `<img :src="iconURL">` instead of `<component :is="icon">` so providers can use their own icons. |
| [portal/src/graphql/mutations.ts](../portal/src/graphql/mutations.ts) | Add `CREATE_PROVIDER_BINDING`, `DELETE_PROVIDER_BINDING` |
| [portal/vite.config.ts](../portal/vite.config.ts) | Add proxy entries so dev-mode shell on `:3000` forwards `/services` and `/ui/providers/*` to the hub at `:9443`. The `/ui/providers/*` rule must take precedence over Vite's own `/ui/` static serving (use `bypass: () => undefined` only for that prefix). |
| [pkg/hub/portal.go](../pkg/hub/portal.go) | Add `Content-Security-Policy` header to portal HTML responses: `default-src 'self'; frame-src 'self'; img-src 'self' data:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'`. `frame-src 'self'` permits provider iframes (proxied = same-origin). |

### Reactive providers store

The current implementation lives at
[portal/src/stores/providers.ts](../portal/src/stores/providers.ts). It
holds a single `items: ProviderDTO[]` array loaded from the hub's
admin-mediated `/api/providers`. Today every authenticated user sees every
installed provider in the nav.

**Phase 3 change** (when direct-APIBinding Enable lands): split into two
sources:

- `catalog: ProviderDTO[]` — what's installed on the platform (hub
  `/api/providers`, unchanged).
- `enabled: APIBinding[]` — what the *current user* has bound, queried
  via kcp's APIBinding list in the user's workspace, filtered by
  `reference.export.path` starting with `root:kedge:providers:`.

`enabledNavItems` becomes `enabled.filter(ready).map(...)`. The catalog
page shows union with status badges (Available / Enabled / Pending).

### Route registration (sketch)

```ts
// portal/src/router/providers.ts
import { router } from './index'

const registered = new Set<string>()

export function registerProviderRoutes(names: string[]) {
  for (const name of names) {
    if (registered.has(name)) continue
    router.addRoute({
      path: `/providers/${name}/:rest(.*)*`,
      name: `provider-${name}`,
      component: () => import('@/pages/ProviderFrame.vue'),
      props: route => ({
        providerName: name,
        subPath: route.params.rest ?? '',
      }),
    })
    registered.add(name)
  }
}
```

### `ProviderFrame.vue` (concrete)

```vue
<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import { useProvidersStore } from '@/stores/providers'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { useRouter } from 'vue-router'

const props = defineProps<{ providerName: string; subPath: string }>()
const providers = useProvidersStore()
const auth = useAuthStore()
const theme = useThemeStore()
const router = useRouter()
const iframe = ref<HTMLIFrameElement | null>(null)

const entry = computed(() =>
  providers.catalog.find(c => c.metadata.name === props.providerName)
)

// Cache-bust on version change so a provider chart upgrade doesn't show
// stale assets.
const src = computed(() => {
  const v = entry.value?.status?.reportedVersion ?? '0'
  return `/ui/providers/${props.providerName}/${props.subPath}?v=${v}`
})

// postMessage handshake. Only respond to messages whose source is OUR
// iframe; only post back to that iframe's contentWindow.
function onMessage(e: MessageEvent) {
  if (e.source !== iframe.value?.contentWindow) return
  if (e.data?.type === 'kedge.ready') {
    iframe.value?.contentWindow?.postMessage({
      type: 'kedge.context',
      token: auth.token,
      user: auth.user,
      tenant: auth.clusterName,
      theme: theme.mode,
      basePath: `/ui/providers/${props.providerName}`,
    }, window.location.origin)
  } else if (e.data?.type === 'kedge.navigate') {
    // Provider wants to update browser URL (e.g. /providers/cost/foo)
    router.push(`/providers/${props.providerName}/${e.data.path}`)
  }
}

onMounted(() => window.addEventListener('message', onMessage))
onUnmounted(() => window.removeEventListener('message', onMessage))
</script>

<template>
  <AppLayout>
    <div v-if="!entry?.status?.ready" class="loading-state">
      Provider starting…
    </div>
    <iframe
      v-else
      ref="iframe"
      :src="src"
      class="w-full h-full border-0"
      sandbox="allow-scripts allow-same-origin allow-forms allow-popups"
      :title="entry.spec.displayName"
    />
  </AppLayout>
</template>
```

### Provider SDK (`@kedge/provider-sdk`) — concrete

```ts
// portal/sdk/index.ts
import { ref, onMounted, onUnmounted } from 'vue'

export interface KedgeContext {
  token: string
  user: { email: string; userId: string }
  tenant: string         // logical cluster name
  theme: 'light' | 'dark' | 'system'
  basePath: string       // e.g. /ui/providers/cost
}

export function useKedge() {
  const ctx = ref<KedgeContext | null>(null)

  function onMessage(e: MessageEvent) {
    if (e.source !== window.parent) return
    if (e.data?.type === 'kedge.context') ctx.value = e.data
  }

  onMounted(() => {
    window.addEventListener('message', onMessage)
    // Tell the shell we're ready to receive context
    window.parent.postMessage({ type: 'kedge.ready' }, '*')
  })
  onUnmounted(() => window.removeEventListener('message', onMessage))

  function navigate(path: string) {
    window.parent.postMessage({ type: 'kedge.navigate', path }, '*')
  }

  return { ctx, navigate }
}
```

Optional — a provider's UI works without the SDK; it just won't share
state (no token, no theme, no synced URL).

### Deep-link behavior

User pastes `https://kedge.example.com/ui/#/providers/cost/forecasts`
into a fresh browser. Sequence:

1. Vue boots, `App.vue` `onMounted` calls `auth.detectAuthMode()`.
2. If not authenticated → `router.beforeEach` redirects to `/login` (no
   change from today).
3. If authenticated → `await providersStore.load()`. This populates the
   store AND calls `registerProviderRoutes(...)` *before* the first
   `<router-view />` render.
4. Vue Router resolves `/providers/cost/forecasts` → `ProviderFrame.vue`
   with `providerName=cost`, `subPath=forecasts`. Iframe loads
   `/ui/providers/cost/forecasts`.

The key is awaiting the store load in `App.vue` before rendering. Without
that, the not-found route swallows the deep link.

### Dev-mode wiring

Vite dev server serves `/ui/*` as Vue assets and proxies `/apis`,
`/healthz` to the hub today. We add:

```ts
// vite.config.ts (excerpt)
server: {
  port: 3000,
  proxy: {
    '/apis':     { target: 'https://localhost:9443', changeOrigin: true, secure: false, ws: true },
    '/healthz':  { target: 'https://localhost:9443', changeOrigin: true, secure: false },
    // NEW:
    '/services': { target: 'https://localhost:9443', changeOrigin: true, secure: false, ws: true },
    // /ui/providers/{name}/* MUST go to hub, NOT vite's static dir.
    // Vite proxy matches first; rewrite-strip not needed because hub
    // expects the full path.
    '/ui/providers': { target: 'https://localhost:9443', changeOrigin: true, secure: false },
  },
},
```

In production the hub already proxies these routes directly — no Vite in
the picture.

### Providers catalog page

`/providers` (`ProvidersPage.vue`) — grid of cards from
`providersStore.catalog`. Each card shows:

- Icon (`<img>` from `entry.spec.iconURL` — proxied via hub).
- Display name, vendor, version, description.
- Status badge: Available / Enabled (= an `APIBinding` exists in your
  workspace) / Pending (provider not Ready).
- Primary button:
  - **Enable** when not bound → opens `ProviderEnableDialog.vue` listing
    `permissionClaims`; on confirm, the portal POSTs the `APIBinding`
    directly to kcp in the user's workspace.
  - **Disable** when bound → confirm + delete the user's `APIBinding`.
  - **Re-accept** when the catalog's `permissionClaims` no longer match
    what the user's `APIBinding` has accepted → re-shows the dialog with
    the new claims highlighted; user confirm = patch the `APIBinding`.

`ProviderEnableDialog.vue` lists `permissionClaims` from the
`CatalogEntry`, distinguishes `tenantScoped` vs non
(non-tenant-scoped claims show a red warning explaining the admin
override needed). Confirm → calls the mutation, sets
`acceptedClaimsHash` to a SHA256 of the sorted claims list.

---

## Provider author experience

A provider ships as one Helm chart. **Chart only targets the host
cluster — never kcp directly.** All kcp interactions are owned by the
hub's catalog controller.

```
provider-cost-insights/
├── Chart.yaml
├── values.yaml
└── templates/
    ├── namespace.yaml
    ├── serviceaccount.yaml
    ├── deployment.yaml          # provider pod (controllers + UI + backend)
    ├── service.yaml             # ClusterIP services for UI and backend
    └── catalogentry.yaml        # CatalogEntry (with inline schemas)
```

`helm install cost-insights ./chart` →

1. Provider Deployment starts. Reads
   `/var/run/secrets/kedge/kedge-provider-kubeconfig` (mounted from the
   Secret the hub will write).
2. `CatalogEntry` is applied to the host cluster API.
3. Hub catalog controller picks it up:
   a. Creates `root:kedge:providers:cost-insights` workspace.
   b. Creates `provider` SA in that workspace.
   c. Mints token, writes `kedge-provider-kubeconfig` Secret to
      `cost-insights` namespace.
   d. Applies `APIResourceSchema` + `APIExport` to the workspace.
4. Provider pod's controller-runtime manager sees the kubeconfig file
   appear (or retries until it does), starts reconciling its own CRs.
5. Provider starts heartbeating; `status.ready=true`.
6. Users see it in `/providers`, click Enable, get an `APIBinding`.

### Alternative: self-bootstrap via an init container

The flow above is **hub-provisioned** — the hub catalog controller owns
all kcp interactions and mints `kedge-provider-kubeconfig`. A provider
may instead **self-bootstrap** with an init container that holds a kcp
workspace-admin kubeconfig, which lets it be installed into any cluster
with no hub provisioning step. The infrastructure provider supports this
via `bootstrap.enabled=true` (see
[providers/infrastructure](../providers/infrastructure/README.md#b-self-bootstrap-with-an-init-container-bootstrapenabledtrue)).

The key simplification: **one kubeconfig, shared by init and serve.** Two
sources, set by `bootstrap.kubeconfigSource`:

**`hubMinted` (default)** — clean division of responsibility:

```
Platform admin                         Provider owner
─────────────                          ─────────────
applies CatalogEntry                   helm install … --set bootstrap.enabled=true
   │                                       │
   ▼  hub catalog controller               ▼  pod scheduled, waits for the Secret
creates root:kedge:providers:<name>    init container (`<provider> init`)
mints kubeconfig (cluster-admin          uses kedge-provider-kubeconfig to install
  in the workspace)                       CRDs / CachedResource / APIExport
HostSecretWriter writes it as            │
  kedge-provider-kubeconfig              ▼  serve container, SAME Secret, runs
```

The minted `provider` SA is **cluster-admin within the provider
workspace** (`EnsureProviderSA`), so it's powerful enough to do init's
installs *and* run serve. The init/serve volume is **not** `optional` —
the pod blocks until the hub delivers the Secret, giving natural ordering.
Requires the hub to run with `--kubeconfig` so its `HostSecretWriter`
([pkg/hub/providers/secretwriter.go](../pkg/hub/providers/secretwriter.go))
can write into the provider's cluster.

**`supplied`** — fully standalone, no hub: you provide a
workspace-admin kubeconfig (`bootstrap.kcpKubeconfig` /
`kcpKubeconfigSecretRef`) and own the prerequisites (workspace exists,
kubeconfig targets it).

Trade-offs vs. hub-provisioned (model A):

- **hubMinted needs no separate credential** — the platform already minted
  one; the provider owner never handles a kcp admin kubeconfig.
- **Simpler than the old mint-to-Secret approach**: no second token, no
  mid-pod Secret write, no extra RBAC.
- **Privilege**: serve runs with cluster-admin-in-workspace rather than a
  narrow scoped SA. For strict least-privilege, prefer model A with a
  manual init.

All models converge on the same runtime contract: the serve container
mounts a kubeconfig at `/var/run/secrets/kedge/kedge-provider-kubeconfig`
and talks to kcp with it. Only *which identity* and *who supplies the
Secret* differ.

### Minimal provider backend contract

A provider's backend (if it declares one) MUST:

- Heartbeat: `POST /api/providers/{name}/heartbeat` to the hub every 30s
  (helper in the SDK).
- `GET /healthz` → 200 when ready (used by hub for `BackendHealthy`).

A provider's controller (the kcp-talking part) MUST:

- Wait for `kedge-provider-kubeconfig` Secret to appear before starting.
- Use the kubeconfig's `provider` SA identity. The SA only has rights in
  the provider's own workspace; cross-workspace access is via the
  `APIExport`'s VirtualWorkspace endpoint (kcp serves this natively
  using the APIExport's identity).

A provider's UI MUST:

- Serve static assets such that internal links are relative or rooted at
  `/ui/providers/{name}/`. Use `X-Kedge-Base-Path` from the proxy if a
  build-time base is needed.

---

## Security considerations

- **Auth token forwarding** (backend proxy): the user's bearer token is
  forwarded to the provider backend. Operators MUST trust the providers
  they install. Same trust model as installing any cluster operator.
- **Provider→kcp isolation**: provider SAs are scoped to their own
  workspace. Cross-tenant access only via the APIExport mechanism, which
  kcp gates by `permissionClaims`.
- **Permission claim gate**: the binding controller refuses any claim not
  marked `tenantScoped`. An override exists
  (`kedge.faros.sh/accept-untrusted-claims=true`) but is admin-only
  (host-cluster RBAC on the `CatalogEntry` resource).
- **iframe sandboxing**: `sandbox` attribute set; no
  `allow-top-navigation`.
- **CSP**: hub portal CSP allows `frame-src 'self'` only.
- **Internal-only services**: providers should be `ClusterIP`. Hub is the
  only public ingress. Network policies recommended.
- **Heartbeat token**: provider SA token is short-lived (24h); rotation
  handled by the catalog controller.

---

## Phased delivery

| Phase | Scope | Verifiable outcome |
|---|---|---|
| 1 | `CatalogEntry` CRD + catalog controller (workspace + SA + Secret + schema apply) + registry + heartbeat endpoint + backend proxy | An example provider's chart installs, hub provisions everything, provider pod heartbeats, `/services/providers/example/*` reaches the backend |
| 2 | UI proxy + `ProviderFrame.vue` + dynamic routes + providers store + AppLayout nav integration + CSP + dev proxy | A static "hello" provider UI loads inside the portal at `/providers/hello`, side nav shows it, theme + token propagate via postMessage |
| 3 | Catalog controller adds RBAC grant (`ClusterRole` + binding for tenant identity) + `MaximalPermissionPolicy` apply on the provider's APIExport. Portal: EnableDialog + direct `APIBinding` create against kcp + nav filter to user's APIBindings + GraphQL validation of bound CRs. | Users can enable/disable from the portal; an `APIBinding` lands in their workspace; provider CRs visible AND queryable via embedded GraphQL gateway. |
| 4 | Provider SDK + example chart in `examples/provider-hello/` | Third party can copy the example and ship a working provider end-to-end |
| 5 | Hardening: RBAC fuzz, cache-bust verification, e2e tests, optional `virtualWorkspace` opt-in, claim re-acceptance flow on chart upgrade | Ready to declare stable |

## Deferred items

1. **GraphQL discovery of provider CRs** — REQUIRED by end of phase 3, not
   optional. Once a tenant workspace has an `APIBinding` to a provider's
   `APIExport`, the embedded GraphQL gateway MUST expose the bound CRs in
   that workspace's schema. The gateway already discovers schemas via
   APIExport for first-party kedge resources (see
   [pkg/hub/graphql.go](../pkg/hub/graphql.go) and
   [cmd/graphql/main.go](../cmd/graphql/main.go) — points at
   `root:kedge:providers`). Expected to work transparently, but validate in
   phase 3 with the example provider's `Greeting` CR appearing in GraphQL.
   If discovery is not automatic, the binding controller will need to
   trigger a gateway refresh — file as a follow-up task, do NOT block phase
   1 or 2.
2. **Cross-provider dependencies** — out of scope for v1.
3. **Heartbeat over kcp leases** — possible v2 simplification.
4. **Per-permission-claim UI toggles** — v2.

---

## Phase 1 implementation plan

Phase 1 = the full backend skeleton, no portal changes yet. Verifiable by
installing a stub provider chart and curling
`/services/providers/example/healthz` through the hub.

### What landed (current tree)

Use these as the authoritative source — the Phase 1A skeleton is in
place. The list below is descriptive, not prescriptive.

| Path | Purpose |
|---|---|
| [apis/providers/v1alpha1/types_catalogentry.go](../apis/providers/v1alpha1/types_catalogentry.go) | `CatalogEntry` Go type (admin-only group `providers.kedge.faros.sh`) |
| [apis/providers/v1alpha1/groupversion_info.go](../apis/providers/v1alpha1/groupversion_info.go) | Scheme registration for the new group |
| [config/crds/providers.kedge.faros.sh_catalogentries.yaml](../config/crds/providers.kedge.faros.sh_catalogentries.yaml) | Host-cluster CRD (codegen) |
| [config/kcp/apiresourceschema-catalogentries.providers.kedge.faros.sh.yaml](../config/kcp/apiresourceschema-catalogentries.providers.kedge.faros.sh.yaml) | kcp APIResourceSchema (codegen) |
| [config/kcp/apiexport-providers.kedge.faros.sh.yaml](../config/kcp/apiexport-providers.kedge.faros.sh.yaml) | Admin-only APIExport (excluded from `core.faros.sh` merge) |
| [hack/gen-core-apiexport/main.go](../hack/gen-core-apiexport/main.go) | Excludes `apiexport-providers.kedge.faros.sh.yaml` from the merged tenant-facing core export |
| [pkg/hub/providers/registry.go](../pkg/hub/providers/registry.go) | In-memory routing table |
| [pkg/hub/providers/proxy.go](../pkg/hub/providers/proxy.go) | `NewUIProxy`, `NewBackendProxy` reverse proxies |
| [pkg/hub/providers/controller.go](../pkg/hub/providers/controller.go) | Catalog reconciler (Phase 1A: URL parse → registry upsert + Ready condition). Phase 1B will add workspace/SA/Secret/schema apply; Phase 3 will add the RBAC `bind`-verb grant + `MaximalPermissionPolicy` apply. |
| [pkg/hub/providers/api.go](../pkg/hub/providers/api.go) | `GET /api/providers` admin-mediated list endpoint backing the portal |
| [pkg/hub/portal_security.go](../pkg/hub/portal_security.go) | `WithPortalSecurityHeaders` middleware (CSP) — applied to both embedded SPA and `--portal-dev-url` proxy |
| [pkg/apiurl/urls.go](../pkg/apiurl/urls.go) | `PathPrefixProvidersUI`, `PathPrefixProvidersProxy` constants |
| [pkg/hub/server.go](../pkg/hub/server.go) | Route registration; second multicluster manager bound to `providers.kedge.faros.sh` for the catalog controller |
| [pkg/hub/scheme.go](../pkg/hub/scheme.go) | Registers the new providers group |
| [pkg/hub/kcp/bootstrap.go](../pkg/hub/kcp/bootstrap.go) | `ensureProvidersSelfBinding` — APIBinding in `root:kedge:providers` so catalog entries can live there |
| [providers/quickstart/](../providers/quickstart/) | Reference provider — Go binary, Dockerfile, `manifest.yaml`, README |
| [portal/src/stores/providers.ts](../portal/src/stores/providers.ts) | Pinia store fetching `/api/providers` |
| [portal/src/router/providers.ts](../portal/src/router/providers.ts) | Dynamic `/providers/:name/:rest(.*)*` route registration |
| [portal/src/pages/ProvidersPage.vue](../portal/src/pages/ProvidersPage.vue) | Catalog grid |
| [portal/src/pages/ProviderFrame.vue](../portal/src/pages/ProviderFrame.vue) | Iframe host + postMessage handshake |
| [portal/src/components/AppLayout.vue](../portal/src/components/AppLayout.vue) | `navItems` computed, merges static + provider entries; renders icon URLs |
| [portal/vite.config.ts](../portal/vite.config.ts) | Dev proxy entries for `/api/providers`, `/services/providers`, `/ui/providers` |

### Key code anchors (from current tree)

- Route registration block: [pkg/hub/server.go:307-359](../pkg/hub/server.go#L307-L359)
- Exec-credential kubeconfig pattern (model for provider kubeconfig
  minting): [pkg/server/proxy/proxy.go](../pkg/server/proxy/proxy.go)
- Existing APIExport YAML (template for the new one's permissionClaims):
  [config/kcp/apiexport-kedge.faros.sh.yaml](../config/kcp/apiexport-kedge.faros.sh.yaml)
- Bootstrap entry point: `pkg/hub/bootstrap` + invocation around
  [pkg/hub/server.go:280-301](../pkg/hub/server.go#L280-L301)
- kcp embedded FS: [config/kcp/embed.go](../config/kcp/embed.go)
- Static path constants live in `pkg/api/url/` (referenced as `apiurl` in
  `pkg/hub/server.go`)
- Workspace YAML pattern:
  [config/kcp/workspace-providers.yaml](../config/kcp/workspace-providers.yaml)

### Phase 1 verification recipe

1. `make codegen && make build` — clean build.
2. Start the hub against an embedded kcp:
   `./bin/kedge-hub --embedded-kcp --static-auth-tokens=test:user-default`.
3. Apply a stub `CatalogEntry`:
   ```yaml
   apiVersion: kedge.faros.sh/v1alpha1
   kind: CatalogEntry
   metadata: { name: hello }
   spec:
     displayName: Hello
     vendor: kedge
     version: 0.0.1
     serviceAccountNamespace: default
     backend:
       url: http://localhost:8081  # any local HTTP responder
       healthPath: /healthz
     apiExport:
       name: hello.example.com
       schemas:
         - groupResource: greetings.hello.example.com
           body: |
             apiVersion: apis.kcp.io/v1alpha1
             kind: APIResourceSchema
             metadata: { name: v260522-stub.greetings.hello.example.com }
             spec: { ... minimal valid schema ... }
   ```
4. Observe in hub logs:
   - workspace `root:kedge:providers:hello` created
   - SA `provider` created
   - Secret `kedge-provider-kubeconfig` written to `default` namespace
   - APIResourceSchema + APIExport applied
   - registry shows `hello` once stub backend returns 200 on `/healthz`
5. `curl -H "Authorization: Bearer test" \
   http://localhost:9443/services/providers/hello/healthz` → reaches the
   stub backend (matches the body it serves).
6. POST a heartbeat with the SA token from the Secret →
   `status.lastHeartbeat` updates.
7. Delete the `CatalogEntry` → registry entry removed, Secret
   cleaned up. (Workspace deletion is a v2 concern — leave it for now,
   note in code as TODO.)

### What phase 1 deliberately does NOT do

- No portal changes.
- No tenant Enable/Disable flow yet — every authenticated user sees every
  installed provider. Phase 3 adds the per-tenant `APIBinding` create from
  the portal and filters the nav.
- No GraphQL validation.
- No Helm example chart yet (phase 4).
- No `virtualWorkspace` opt-in path (phase 5).

---

## Phase 2 implementation plan (portal)

Phase 2 = the full portal wiring. Verifiable by serving a static "hello"
provider UI and seeing it load inside the portal frame.

See §"Portal changes" above for the file create/edit lists. Order of
operations:

1. **CSP first** ([pkg/hub/portal.go](../pkg/hub/portal.go)) — without
   `frame-src 'self'` the iframe is blocked. Add a small middleware that
   sets the header on portal HTML responses only.
2. **UI proxy** — `pkg/hub/providers/proxy.go` (already created in phase
   1) gets the `NewUIProxy` handler wired into the router. Existing
   backend proxy stays.
3. **GraphQL queries** + **Pinia store** + **route registration helper** —
   landed together; nothing depends on order between them.
4. **App.vue** — await `providersStore.load()` before mounting
   `<router-view />`. Critical for deep-link bootstrapping.
5. **AppLayout.vue** — replace static `navItems` with computed.
6. **ProvidersPage.vue + ProviderFrame.vue** — render the catalog and
   frame.
7. **EnableDialog.vue** — wired only enough to display the claims; the
   actual mutation lands in phase 3 (binding controller).
8. **Provider SDK** — published as workspace package; consumed by the
   example provider in phase 4.

### Phase 2 verification recipe

1. With phase 1 deployed, install a stub `CatalogEntry` with a
   simple HTTP server behind `spec.ui.url` that serves an
   `index.html` containing `<h1>hello provider</h1>` and a small script
   that calls `useKedge()` (or just `postMessage({ type: 'kedge.ready' })`
   directly).
2. Open the portal in a browser. Side nav and `/providers` show the new
   provider immediately — Phase 1A/2 do not gate visibility per tenant.
   Phase 3 adds the Enable/Disable flow and the nav filter.
4. Click it. URL becomes `/providers/hello`. Iframe loads.
5. Open browser devtools → confirm:
   - `POST` request from iframe arrived with auth token (visible in
     iframe's console if the stub echoes it).
   - No CSP violations.
   - No CORS errors.
6. Toggle theme in the shell — if the stub iframe handles `kedge.context`
   re-broadcasts, its background flips. (Optional check.)
7. Reload the deep link `https://kedge.example.com/ui/#/providers/hello`
   in a fresh tab → still works (proves the store loads before route
   resolution).

### What phase 2 deliberately does NOT do

- Catalog Enable/Disable buttons (UI present, mutation is phase 3).
- Per-claim consent toggles (phase 3 ships only the all-or-nothing
  dialog).
- WebSocket support in the backend proxy (add in phase 5 if needed).
- Example provider chart (phase 4).

---

## Example: a minimal provider

Tracked under `examples/provider-hello/` once phase 1 lands. Structure: one
Go binary serving `/healthz` + `/api/hello` + a static `index.html`; one
controller using `kedge-provider-kubeconfig` to manage a `Greeting` CR;
Helm chart from §"Provider author experience".
