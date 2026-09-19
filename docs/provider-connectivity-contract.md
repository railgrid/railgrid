# Provider connectivity contract — how providers connect to the platform

This doc pins down the contracts every provider should follow for *how it
reaches data*: the **UI data path** (contract 1), the **platform API access
path** (contract 2), and the **provider-to-provider path** (contract 3). It
explains the hub plumbing that enforces them, how the portal authenticates,
which providers conform today, and where the deliberate exceptions are.

Contracts 1 and 2 are about a provider reaching the **platform** (kcp, through
the hub's kcp proxy). Contract 3 is about a provider reaching **another
provider** — and the rule there is that it does so *only* through that
provider's published API, never into its backend. See
[`providers.md` §"Provider isolation"](./providers.md#provider-isolation-the-cross-provider-boundary)
for the principle in full; this doc gives the concrete access mechanics.

It is the companion to [`providers.md`](./providers.md) (the provider plane
overview), [`provider-scoping.md`](./provider-scoping.md) (Global/Org/Personal
scoping), and [`security.md`](./security.md) (auth setup).

---

## Restore-from-reboot summary

- There are **three** legitimate data paths. UI data flows through the
  hub's **kcp proxy** at `/clusters/{cluster}` as plain Kubernetes REST
  (contract 1); backend/controller code reaches kcp
  either as a **non-privileged provider ServiceAccount via an
  APIExportEndpointSlice** *or* as the **caller using their forwarded bearer
  token**, scoped to the tenant workspace (contract 2); and one provider
  reaches **another** provider only through that provider's **published
  APIExport resources + VW subresources, as the caller** — never into its
  backend (contract 3).
- The hub exposes providers through **two different proxies** with **different
  token handling**: the UI proxy forwards **no token**, the backend proxy
  forwards the caller's `Authorization` header **as-is**.
- "Token not known to the provider" means the **provider's backend server**.
  The provider's **in-browser micro-frontend** runs in the user's browser and
  calls the hub's kcp proxy directly through the host-owned `railgridContext.fetch`,
  which attaches the caller's bearer for it.
- **Every provider is standalone** and satisfies contract 2 — none holds an
  admin client. The in-process built-ins (`mcp`, `kubernetesedges`,
  `serveredges`), which ran inside the hub process on the hub's **admin** kcp
  config and violated contract 2 by construction, were removed in #435.
- `kuery` and `app-studio` drive their UI through their own **REST** backends
  rather than the kcp proxy, so the bearer token reaches their backend — a
  contract-1 divergence (defensible: kuery is SQL-backed, app-studio streams chat).

---

## The two contracts

**Contract 1 — UI data path.** The provider's UI micro-frontend reads and
writes data through the hub's **kcp proxy** (`/clusters/{cluster}/apis/…`,
plain Kubernetes REST via the shared `portalkit` kube client), which authorizes
the caller by workspace membership and forwards to kcp as that user. The
provider's **backend server** is not on the UI data path and does not receive
the user's bearer token for UI purposes.

**Contract 2 — API access path.** The provider's backend reaches the kube/kcp
API **without any admin/root client**. It uses one of two scoped mechanisms:

- **(2a) Controller / sync** — a non-privileged ServiceAccount minted in the
  provider's own workspace (`root:railgrid:providers:{name}`), driving a
  multicluster manager off the provider's **APIExportEndpointSlice** virtual
  workspace, bounded by the APIExport's `tenantScoped` permission claims.
- **(2b) Per-request** — the provider drops its own credential and acts **as
  the caller**, using the bearer token forwarded by the hub, scoped to the
  workspace whose kcp logical-cluster ID arrives in `X-Railgrid-Tenant` /
  `X-Railgrid-Cluster` (both carry the ID; the workspace path is never sent).

Both 2a and 2b are admin-free. New providers should pick one (or use 2a for
controllers and 2b for request-driven endpoints, like `code` and
`infrastructure` do) and never construct a kcp-admin / root client.

**Contract 3 — provider-to-provider path.** When provider A needs something
provider B owns, A goes through **B's published API**, not B's backend:

- **B's `APIExport` resources** — A binds B's `APIExport` (an `APIBinding`
  in the tenant workspace) and reads/writes B's CRs over the normal
  `/clusters/...` path. This is the control-plane channel (spec/status).
- **B's published data-plane subresources** on those resources (for example
  `{template-resource}/{name}/log`, `…/proxy/{path}`, or a
  component-scoped `…/{name}/components/{component}/sync`) — for streams,
  proxies, and other verbs that aren't plain CRUD. B serves them against
  *its* backend; A never sees it.

Both are invoked **as the caller** (forwarded bearer token, scoped to the
workspace — same identity as 2b) and **routed by binding, never by a
backend URL**. A must never hold a credential into B's runtime cluster /
database / internal Service, call B's internal endpoints directly, or encode
B's backend topology in its own config. The owning provider is the single
holder of its backend credential. This is the access mechanics behind
[`providers.md` §"Provider isolation"](./providers.md#provider-isolation-the-cross-provider-boundary);
the canonical example is the App Studio → infrastructure data-plane
decoupling ([`app-studio-runtime-decoupling.md`](./app-studio-runtime-decoupling.md)).

---

## Pillar 2 route classes

The three contracts above say *how* a provider authenticates. This section
says *what may exist* on the one backend origin the hub gives every provider
behind `/services/providers/{name}/*` (`pkg/hub/providers/proxy.go`). The list
is **closed**: a route that is not one of these classes is a deviation, even
when it authorizes correctly.

| Class | Shape | Auth | Reference |
|---|---|---|---|
| **(a) Data-plane verb** | `/{root}/clusters/{clusterID}/{resource}/{name}/{verb}[/{tail}]` and `.../{name}/components/{c}/{verb}[/{tail}]` | caller bearer; two gates | infrastructure `dataplane/handler.go` |
| **(a′) Action** | `/actions/clusters/{clusterID}/{resource}/{name}/{action}/{version}`; body `{"input":{}}`; `actionwire` envelope | caller bearer; two gates | code `actions/server.go`, databricks |
| **(b) MCP projection** | `/mcp`, `/mcp/sse` | caller bearer + `X-Railgrid-Cluster` for addressing | `pkg/hub/mcpaggregate/enumerator.go` federates exactly this path |
| **(c) Health** | `/healthz`, `/readyz` | none | `provider-sdk/vwhealth.Handler` |
| **(d) Browser OAuth** | `/oauth/{provider}/{start,callback,config}` | signed state; the popup may `postMessage` to its opener | code `oauthgithub/oauth.go` |
| **(e) Hub-only** | `/workload-identities/*` | refused by the proxy for callers | `pkg/hub/providers/proxy.go` |
| **(f) Agent tunnel** | `/agent/clusters/{clusterID}/{resource}/{name}/proxy` | join token or edge ServiceAccount + SAR | edges `agent_proxy_builder_v2.go` |
| **(g) Signed inbound webhook** | `/webhooks/{kind}/{clusterID}/{name}/{token}` | HMAC token; acts as the provider ServiceAccount | agents `api/server.go` |

Rules:

1. **Nothing else. No `/api/*`.** If a UI needs to list, create or edit a
   thing, the thing is a bound CR and the UI reads it over `/clusters/{id}`
   (contract 1). A backend route that mirrors a CR is a deviation even when
   it is authorized correctly.
2. **The bearer is the only trust root.** The hub strips inbound
   `X-Railgrid-*` and re-injects `X-Railgrid-User` plus the cluster ID as
   both `X-Railgrid-Tenant` and `X-Railgrid-Cluster`. A provider may read
   those headers for **addressing** and **labels**; it must never derive
   authorization from them. Where the path carries the cluster ID, **the path
   wins and must equal the header** — a request whose header disagrees is
   refused before any gate runs.
3. **Two gates, as the caller, on every verb.** Gate 1: a **real GET** of the
   addressed resource with the caller's token, which proves visibility
   against the live object *and* yields the object, so the handler can pin
   its UID and spec. Gate 2: a `SelfSubjectAccessReview` for **`create`** on
   the virtual subresource `{resource}/{verb}`, name-scoped. The hub
   materializes grants as exactly that rule
   (`pkg/hub/serviceaccounts/workload_identity.go`), so any other verb string
   breaks workload identities. See
   [provider-actions.md](./provider-actions.md).
4. **Per-request tenant client, credential dropped.** Build the client for
   `<hub>/clusters/{clusterID}` from the provider kubeconfig's host and CA
   **only**, with the caller's bearer (`providers/*/tenant/client.go`). Cache
   per (cluster, token hash). This is contract 2b, stated as a route rule.
5. **Cross-provider access is by binding, not by string.** Provider A reaches
   provider B only through B's bound CRs and B's data-plane verbs, as the
   caller, resolving the target from the binding or from a `status` field B
   publishes. No foreign credential, no foreign Secret read, no hardcoded
   `/services/providers/<b>/...` format string. This is contract 3.
6. **No dev bypass in release binaries.** A switch such as
   `RAILGRID_DEV_ALLOW_TENANT_QUERY`, which takes the bearer or the tenant
   from the query string, must be compiled out of shipped binaries.

The shared server-kit that parses these paths, runs the two gates, enforces
declared limits and writes the envelope is `provider-sdk/dataplane`, landing
under
[roadmap/provider-contract-remediation.md](./roadmap/provider-contract-remediation.md)
§0.2. Write a new provider against it rather than hand-rolling a parser and
gates — four incompatible dialects exist because every provider wrote its
own.

---

## Pillar 1 carve-outs

Desired state and durable status live **in the object**: anything a tenant can
see, or that a reconcile must recover from, is a `spec`/`status` field on the
tenant's object or on a provider-private kind in the provider workspace. Not
Postgres, not SQLite, not a PVC, not process memory (AGENTS.md §5.8).

Two carve-outs are sanctioned, and only these two:

- **Transient artifacts.** An uploaded bundle or a git snapshot may sit on a
  PVC while the CR carries its reference and digest — provided the artifact is
  consumed-and-deleted or swept on a fixed TTL, and **nothing is lost if it is
  gone**. It is a cache, never the authority. (code `commitbundle/store.go`,
  `actions/snapshots.go`; factory's artifact blob store.)
- **Projection kinds.** A provider that must keep high-volume execution data
  outside kcp (chat turns, run transcripts, usage rows) still gives that data
  a tenant-visible identity as a **CR** (`Session`, `Run`). The CR's status is
  the projection of the store, and a **finalizer purges the store** when the
  CR goes. (app-studio `controller/session/controller.go`.)

Controllers are watch-driven multicluster-runtime reconcilers on
`provider-sdk/apiexportprovider` (one workqueue per consuming workspace,
`req.ClusterName` selects the client), running under
`provider-sdk/leaderelection.Run`, with readiness attached to the provider's
watch state through `provider-sdk/vwhealth` and heartbeat `CanSend` gated on
it. `RequeueAfter` is allowed for exactly **three** things:

1. **pacing a system that cannot be watched** (an external API with no
   webhook or watch);
2. **backing off a failed call**; and
3. **waking at a computed lifecycle deadline** — a specific, derived time the
   object's own spec/status implies, such as an expiry or a timeout
   (infrastructure `lifecycle.go`).

A blanket `resyncPeriod`, a "safety" resync, or a fixed ticker that re-lists
and re-applies is **not** one of them. If a loop exists because a watch is
missing, add the watch.

---

## The hub plumbing (and what it does with the token)

Two proxies back every provider, defined in
[`pkg/hub/providers/proxy.go`](../pkg/hub/providers/proxy.go):

| Proxy | Path | Token handling |
|-------|------|----------------|
| **UI proxy** (`NewUIProxy`, `proxy.go:52`) | `/ui/providers/{name}/*` | Static assets only. Injects `X-Railgrid-Base-Path`. **No token forwarded.** First-party providers are served from an embedded FS (`LocalUIAssets`). |
| **Backend proxy** (`NewBackendProxy`, `proxy.go:90`) | `/services/providers/{name}/*` | **Forwards the caller's `Authorization` header as-is**, and additionally injects `X-Railgrid-User` plus the tenant workspace's kcp logical-cluster ID as both `X-Railgrid-Tenant` and `X-Railgrid-Cluster`, resolved from the token. Inbound `X-Railgrid-*` headers are **always stripped** first (anti-spoofing, `proxy.go:114`). |

The identity injected by the backend proxy is resolved by the
**TenantResolver** ([`pkg/hub/provider_tenant_resolver.go`](../pkg/hub/provider_tenant_resolver.go),
`resolve` at `:104`): caller token → `User` CR → `Organization` →
`Status.WorkspacePath`. It honors the sidebar's `X-Railgrid-Org` /
`X-Railgrid-Workspace` selection (validated against the user's
`UserMembershipIndex`) and falls back to the user's personal org. Failures are
best-effort: anonymous `/healthz` probes still pass through with no identity
headers, they do not 401.

The **kcp proxy** ([`pkg/server/proxy/proxy.go`](../pkg/server/proxy/proxy.go))
is the hub-side surface that contract 1 targets. It verifies the bearer,
authorizes the requested `/clusters/{clusterID}` against the caller's
`UserMembershipIndex` (the "Option A" design in
[`hub-proxy-workspace-access.md`](./hub-proxy-workspace-access.md),
`pkg/server/proxy/authorizer.go`), and forwards to kcp **authenticated as the
caller** — so every read and write runs with the user's own RBAC, in any
workspace they are a member of, not only their `DefaultCluster`. The provider's
backend server is never in this loop.

> The key consequence: the only way the user's token reaches a provider's
> **backend** is via the backend proxy (`/services/providers/{name}/*`). A
> provider that does all UI data through the kcp proxy keeps its backend off
> the token path entirely.

---

## How the portal authenticates

```
User opens portal
  → LoginPage: "Sign in with SSO" (OIDC/Dex) or paste a static token
  → OIDC: GET /auth/authorize (PKCE verifier in sessionStorage)
        → Dex → GET /auth/callback?code=…
        → hub exchanges code (PKCE), verifies ID token, seeds the User CR
        → hub returns a LoginResponse (idToken, refreshToken, expiresAt, …)
  → Static: POST /auth/token-login with Authorization: Bearer <token>
        → hub constant-time-compares against configured tokens, seeds User CR
  → portal stores it in localStorage["railgrid-auth"]
        { idToken, refreshToken, expiresAt, issuerUrl, clientId,
          email, userId, clusterName }
```

Anchors: portal `portal/src/pages/LoginPage.vue`,
`portal/src/auth/token.ts` (storage + offline OIDC refresh),
`portal/src/stores/auth.ts`; hub `pkg/server/auth/handler.go` (OIDC
authorize/callback + `seedUser`), `pkg/server/proxy/proxy.go` (`token-login`,
bearer dispatch at `:248`).

**Attaching the token to data requests.** Provider bundles never attach the
token themselves: they call through the host-owned `railgridContext.fetch`
(`portal/src/providers/providerFetch.ts`), which injects
`Authorization: Bearer <token>` plus `X-Railgrid-Org` / `X-Railgrid-Workspace` from
the host's own state and allows only the provider's own
`/services/providers/{name}/` and `/ui/providers/{name}/`, `/clusters/`,
`/api/orgs/{org}/`, and GET/HEAD `/api/providers`. The shared kube client
(`provider-sdk/portalkit/kube.ts`, `createKubeClient`) builds
`/clusters/{cluster}/apis/{group}/{version}/{resource}` (core group:
`/clusters/{cluster}/api/v1/…`) on top of that fetch: creates are `POST`, full
updates `PUT`, partial updates merge-patch, create-or-update is server-side
apply (`application/apply-patch+yaml`, `force=true`), and deletes carry
`DeleteOptions` preconditions. The host portal's own pages (including the MCP
page) use hub REST under `/api/orgs/…`.

**Hub-side verification** (`pkg/server/proxy/proxy.go:248`) dispatches by token
shape:

| Token type | Source | Verification |
|------------|--------|--------------|
| OIDC ID token | Dex / external IdP | signature against the IdP JWKS, then `sub` → `User` CR |
| Static bearer token | hub `--static-auth-tokens` | constant-time compare (dev / air-gapped) |
| kcp ServiceAccount token | kcp-minted | signature verified by kcp (provider/agent/inter-service) |

**Passing context to provider micro-frontends.** `ProviderFrame.vue`
(`portal/src/pages/ProviderFrame.vue:151`) sets a **`railgridContext` property on
the provider's custom element** (not a postMessage handshake — that part of
older docs is stale):

```js
el.railgridContext = {
  subPath, basePath,            // routing
  fetch,                        // host-owned fetch: attaches bearer + tenant headers
  user: auth.user,              // { email, userId }
  tenant: auth.clusterName,     // kcp logical cluster
  orgUUID, workspaceUUID,       // sidebar selection
  theme,                        // light | dark | system
}
```

It re-pushes on theme change, token refresh, and workspace switch. The provider
bundle wraps `railgridContext.fetch` (`portalkit/tenant.ts` `providerFetch(ctx)`)
and builds its kube client from it (`portalkit/kube.ts`
`createKubeClient({ fetch, cluster: ctx.tenant })`); the bearer is attached by
the host, not by the bundle.

> The bundle still executes as trusted code in the portal document, but it
> reaches the hub only through the host fetch and its allowlist. Contract 1's
> "token not known to the provider" is about the provider's **server**, which
> only sees the token if the UI calls `/services/providers/{name}/*`.

---

## Contract 1 conformance — UI via the kcp proxy

| Provider | UI data path | Verdict |
|----------|--------------|---------|
| `code` | kube REST through `/clusters/{cluster}` (`portalkit` kube client) for all CRUD; one backend probe (`/services/providers/code/oauth/github/config`) | ✅ Conforms |
| `infrastructure` | kube REST through `/clusters/{cluster}` only; backend serves no template/instance REST | ✅ Conforms |
| `edges` | kube REST through `/clusters/{cluster}` for its CRs; the edge data plane (kubectl proxy, SSH, per-edge MCP) is its own backend, called as the caller | ✅ Conforms |
| `databricks` | kube REST through `/clusters/{cluster}` for its CRs | ✅ Conforms |
| `kuery` | **REST** to `/services/providers/kuery/api/{edges,query}` — token reaches backend | ❌ Diverges |
| `app-studio` | **REST** to `/services/providers/app-studio/api/projects/*` — token reaches backend; only its resource picker reads bound CRs through the kcp proxy | ❌ Diverges |

The former in-process built-ins (`mcp`, `kubernetesedges`, `serveredges`) no
longer ship a provider UI; the hub portal's own MCP page is hub REST under
`/api/orgs/…`.

`kuery` is backed by its own SQL store (it syncs edge data into SQLite and
answers queries from there) and `app-studio` streams chat/messages — neither
maps cleanly onto kube CRUD, so each has a real reason to run a *backend*.
That reason does not extend to serving CRUD facades over their own CRs, which
is what they do today and what Pillar 2 rule 1 forbids; both are being
migrated onto verbs on bound resources.

MCP endpoints (`/services/.../mcp`) are an **AI-agent** surface, not the human
UI; `code` and `infrastructure` keep their UI on the kcp proxy even though their
MCP servers receive the token by design.

---

## Contract 2 conformance — scoped API access, no admin client

| Provider | Mechanism | Verdict |
|----------|-----------|---------|
| `code` | (2a) `apiexport.New(...)` multicluster mgr off the endpointslice + (2b) caller-token tenant client for MCP | ✅ Conforms |
| `infrastructure` | init/serve split: admin kubeconfig only in one-shot `init`, then a **minted SA** + endpointslice for serve; (2b) caller-token factory for MCP; KRO writes go to a separate runtime cluster | ✅ Conforms |
| `kuery` | (2a) minted provider SA + APIExportEndpointSlice for edge discovery; `[get,list,watch] edges`, `tenantScoped` | ✅ Conforms |
| `app-studio` | (2b) clears the provider credential, builds the tenant client from the forwarded caller token; `tenantScoped` secret claims | ✅ Conforms |

### How the hub provisions a conforming (2a) provider

[`pkg/hub/providers/provision.go`](../pkg/hub/providers/provision.go):

1. `EnsureProviderWorkspace` creates `root:railgrid:providers:{name}`.
2. `EnsureProviderSA` creates `system:serviceaccount:default:provider`, granted
   cluster-admin **only inside its own workspace** — its single privilege.
3. `MintProviderKubeconfig` mints a long-lived SA-token kubeconfig pointing at
   `{hub}/clusters/root:railgrid:providers:{name}`, delivered to the provider as
   the `railgrid-provider-kubeconfig` Secret.
4. `ApplyAPIExport` registers the provider's permission claims; `ApplyBindGrant`
   lets `system:authenticated` tenants bind the export.

The provider then builds a multicluster manager off its APIExportEndpointSlice
(e.g. `providers/code/controller_manager.go`,
`providers/infrastructure/install/endpointslice.go`). The (2b) per-request
factory (`providers/*/tenant/client.go`) strips the provider's own client cert,
keeps only the CA, and authenticates with the **caller's** bearer token against
`{host}/clusters/{tenantPath}`.

---

## Contract 3 conformance — provider-to-provider via published API only

| Interaction | Mechanism | Verdict |
|----------|-----------|---------|
| `app-studio` → `infrastructure` (Template development data plane) | Selected Template instance (control plane) + infrastructure provider data-plane subresources at `/dataplane/clusters/{workspace}/{resource}/{name}/…`, including component-scoped verbs, called as the tenant user | ✅ Conforms |
| `app-studio` → runtime cluster (historical) | Formerly held `APP_STUDIO_RUNTIME_KUBECONFIG`, a direct second credential into infrastructure's runtime cluster | ❌ Removed anti-pattern; retained only as historical context in [`app-studio-runtime-decoupling.md`](./app-studio-runtime-decoupling.md) |
| `kuery` → `Edge`s | reads bound `Edge` CRs / engages clusters through the hub's edges-proxy as the caller, never the edge provider's backend | ✅ Conforms |

The shape that conforms: address a **bound resource** (or a subresource on
it), authenticate as the **caller**, and let the binding decide which
provider's backend answers. The shape that doesn't: a kubeconfig / DSN / URL
in provider A's config that points straight at provider B's cluster, DB, or
internal Service.

---

## Known divergences (and why)

1. **`kuery` / `app-studio` (and `agents`, `quickstart`) drive their UI
   through their own REST, not the kcp proxy.** Contract-2-clean — every call
   is made as the caller — but the routes mirror CRs, which Pillar 2 rule 1
   forbids, and the token reaches their backend. This is a **deviation being
   migrated**, not a sanctioned second model; see
   [roadmap/provider-contract-remediation.md](./roadmap/provider-contract-remediation.md).
   A non-kcp store (kuery's SQL index) or a streaming surface (app-studio's
   chat) justifies a *backend*, but the backend must then serve Pillar 2
   classes — verbs on bound resources — not CRUD facades.

---

## Checklist for a new provider

- [ ] UI reads/writes go through `/clusters/{cluster}` kube REST via the
      `portalkit` kube client (contract 1). The backend is **not** a data
      path; there is no `/api/*`.
- [ ] **Every backend route is one of the eight classes** in §"Pillar 2 route
      classes" — (a) data-plane verb, (a′) action, (b) `/mcp`, (c) health,
      (d) browser OAuth, (e) hub-only, (f) agent tunnel, (g) signed webhook.
      Name the class for each route you add. If you cannot, the route does
      not belong on the backend.
- [ ] Verbs are served on the grammar
      `/{root}/clusters/{clusterID}/{resource}/{name}/{verb}` through
      `provider-sdk/dataplane`, with **gate 1 a real GET** as the caller and
      **gate 2 an SSAR for `create`** on `{resource}/{verb}`, name-scoped.
      Path cluster must equal `X-Railgrid-Cluster`.
- [ ] No kcp-admin / root client anywhere in the provider (contract 2); the
      admin credential appears only in the one-shot `init`.
- [ ] Controllers run as the **minted provider SA** off the
      **APIExportEndpointSlice** (2a), under `leaderelection.Run`, with
      `/readyz` and heartbeat `CanSend` from `vwhealth`; declare
      `tenantScoped` permission claims for exactly the resources/verbs you
      need. `RequeueAfter` only for the three sanctioned uses in §"Pillar 1
      carve-outs".
- [ ] Request-driven endpoints act as the **caller** via the forwarded token
      (2b): build the tenant client from `Authorization` + the cluster ID in
      `X-Railgrid-Cluster` (`X-Railgrid-Tenant` carries the same ID), drop the
      provider's own credential. Never parse a workspace path out of a header.
- [ ] Never trust inbound `X-Railgrid-*` headers in the provider — the backend
      proxy strips and re-injects them; treat them as hub-asserted only.
- [ ] Durable tenant state is on the CR. If it is not, it is one of the two
      carve-outs (transient artifact, or projection kind with a purge
      finalizer) and the architecture doc says so.
- [ ] To reach **another provider**, bind its `APIExport` and call its CRs /
      data-plane verbs as the caller (contract 3). Never hold a credential
      into another provider's backend (runtime cluster, DB, internal
      Service) or hardcode its backend URL.

---

## Code anchors

| Concern | Anchor |
|---------|--------|
| UI proxy (no token) | `pkg/hub/providers/proxy.go:52` |
| Backend proxy (forwards token + injects identity) | `pkg/hub/providers/proxy.go:90` |
| Tenant resolution (token → workspace path) | `pkg/hub/provider_tenant_resolver.go:104` |
| kcp proxy (membership-gated, caller-scoped) | `pkg/server/proxy/proxy.go`, `pkg/server/proxy/authorizer.go` |
| Provider provisioning (workspace, SA, kubeconfig) | `pkg/hub/providers/provision.go` |
| Portal login / token storage | `portal/src/pages/LoginPage.vue`, `portal/src/auth/token.ts` |
| Host-owned provider fetch (allowlist + bearer) | `portal/src/providers/providerFetch.ts` |
| Provider kube client (`/clusters/{cluster}` REST) | `provider-sdk/portalkit/kube.ts` |
| `railgridContext` push to micro-frontend | `portal/src/pages/ProviderFrame.vue:151` |
| Hub bearer dispatch / verification | `pkg/server/proxy/proxy.go:248` |
| (2a) endpointslice multicluster mgr | `providers/code/controller_manager.go` |
| (2b) caller-token tenant factory | `providers/*/tenant/client.go` |

