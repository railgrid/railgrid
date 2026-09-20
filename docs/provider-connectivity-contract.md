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
  workspace, bounded by the APIExport's `tenantScoped` permission claims —
  and, for a core-group claim on `secrets`, by that claim's **label selector**
  (see "Label-scoped claims" below).
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

   Which cluster the hub injects therefore follows the path. On a route that
   names one — `/{root}/clusters/{id}/…`, the grammar of contract 4 — the hub
   injects **that** ID, after authorizing the caller for it with the same
   membership check the kcp proxy applies to `/clusters/{id}`; a caller who is
   not a member is refused with **403 at the hub**, before the provider is
   dialled. An `X-Railgrid-Org` / `X-Railgrid-Workspace` selection (the
   portal's sidebar) **loses to the path** on such a route: it steers the
   default, and the default is not what is being addressed. Every other route
   — MCP, OAuth callbacks, webhooks, `/healthz` — names no cluster and keeps
   the caller's resolved workspace, selection headers included. An anonymous
   request is unchanged: no identity headers, no hub-side refusal.
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

### Declare the verbs you serve

A class (a) verb is **declared** in the CatalogEntry, beside the actions:

```yaml
spec:
  dataPlane:
    verbs:
      - resource: instances     # a resource this provider's own APIExport serves
        verb: exec              # no version, no slash — the {resource}/{verb} coordinate
        description: "Open an interactive shell or run a command in the instance."
        stream: true            # upgrades or streams rather than returning one response
        readOnly: false         # does not mutate the resource or what it fronts
```

Declaring a verb **grants nothing and serves nothing**. The provider still
enforces it with the two gates above, exactly as before. What the declaration
buys is that the coordinate is machine-readable:

- The hub scoped-identity service will only mint a `create` capability on
  `{resource}/{verb}` for a verb the owning provider declares. Before this
  field existed, `exec`, `proxy`, `ssh` and `delegate` lived only in provider
  code, so no cross-provider capability for any of them could be minted at all
  — see §"Scoped identities", clause C.
- Consumers read `dataPlaneVerbs` off `/api/providers` instead of hardcoding a
  coordinate nobody validates (rule 5, and review X-8).

A verb is on a resource of the provider's **own** API group — declaring
`dataPlane.verbs` without `spec.apiExport` is rejected — and a standard
Kubernetes verb (`get`, `list`, `create`, …) may not be one, because
`instances/get` reads like the ordinary `get` on `instances` and is not. A
malformed declaration fails closed: the provider leaves the registry rather
than keeping a stale, wider verb surface.

Actions (`spec.actions`) and data-plane verbs (`spec.dataPlane.verbs`) are two
declarations of the same RBAC coordinate. An action is versioned, schema'd and
request/response; a data-plane verb is unversioned and streaming or proxying.
Declare each capability as exactly one of them. A verb served on the *actions*
grammar whose body is too large to describe under `limits.maxInputBytes` —
an uncatalogued large-upload verb, like the code provider's
`repositories/stage_snapshot` and `repositories/stage_commit_bundle` — is
still declared, as a data-plane verb, because clause C mints nothing for a
coordinate that appears in neither list (see
[provider-actions.md](./provider-actions.md) §"Uncatalogued large-upload
verbs").

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
| **Backend proxy** (`NewBackendProxy`, `proxy.go:90`) | `/services/providers/{name}/*` | **Forwards the caller's `Authorization` header as-is**, and additionally injects `X-Railgrid-User` plus a kcp logical-cluster ID as both `X-Railgrid-Tenant` and `X-Railgrid-Cluster`. On a data-plane route (`/{root}/clusters/{id}/…`) that ID is the one **in the path**, injected only after the caller is authorized for it (403 otherwise); on any other route it is the workspace resolved from the token. Inbound `X-Railgrid-*` headers are **always stripped** first (anti-spoofing, `proxy.go:114`). |

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

## Label-scoped claims — a claim is per resource, not per name

A kcp permission claim names a **group/resource**. It has no notion of a name
or a prefix. So a provider that needs to write one Secret used to be granted
every Secret, in every workspace that enabled it — the tenant's own cloud
credentials, and every other provider's backend credential alongside them.
That is mechanism M3 in
[cross-provider-simplification.md](./cross-provider-simplification.md), the
side-door behind every implicit credential hand-off the audit found, and the
thing that made it possible for one provider to read another's git PAT without
the owning provider ever being consulted.

The fix is the selector kcp added to permission claims. **Every Secret a
provider owns carries `railgrid.ai/owner: <provider>`, and the claim is scoped
to that label.**

```yaml
# manifest.yaml — the one place a claim is written
permissionClaims:
  - resource: secrets
    verbs: [get, list, watch, create, update, delete]
    tenantScoped: true
    selector:
      matchLabels:
        railgrid.ai/owner: agents
```

`provider-sdk/cmd/apiexportgen` renders that as the kcp APIExport claim's
`defaultSelector`; the hub writes the same `matchLabels` onto the accepted
claim's `selector` in each tenant's `APIBinding`
([`pkg/hub/kcp/bootstrap.go`](../pkg/hub/kcp/bootstrap.go), `claimSelector`).
The label key and its helpers live in
[`provider-sdk/claimscope`](../provider-sdk/claimscope/claimscope.go).

**What kcp actually enforces**, in both directions of the APIExport virtual
workspace:

| Direction | Behaviour |
|---|---|
| Labelling | kcp's permission-claim labeler stamps the internal `claimed.internal.apis.kcp.io/<export>` marker on an object **only if the accepted claim's selector matches its labels** (`pkg/permissionclaim`, `LabelsFor`). The selector is not part of the claim's hash, so narrowing a claim never invalidates the marker on objects that still match. |
| Read | The virtual workspace ANDs that marker into every LIST/WATCH/DELETECOLLECTION, and a GET on an object without it returns **404, not 403** (`virtual-workspace-framework`, `forwardingregistry.WithLabelSelector`). An unlabelled Secret does not exist as far as the provider is concerned. |
| Write | Virtual-workspace admission **adds** the claim's `matchLabels` to an object the provider creates or updates, refuses the write if a key is already present with a different value ("protected label … must have value …"), and refuses a CREATE/UPDATE/DELETE whose object does not match the selector (`pkg/virtual/apiexport/admission`). |

Two consequences worth stating plainly:

- **A write that goes through the export does not need the label set in Go** —
  kcp adds it. A write that goes anywhere else does: the portal writing as the
  user, the provider's own workspace-admin client, an edge agent with its own
  credential. Those are stamped explicitly, because a Secret written without
  the label saves without complaint and is then invisible to the provider
  forever. Every such writer in this tree sets it.
- **`matchExpressions` is not offered.** kcp deliberately does not synthesize
  labels for one, so a provider declaring a `matchExpressions` selector could
  not create the objects it claims. `matchLabels` only.

**Tenant-written Secrets the provider must READ** are not solved by the claim.
Three routes, in order of preference:

1. **Read as the caller** through a data-plane verb — the provider drops its
   own credential and kcp's ordinary RBAC decides. The agents provider's
   `model-test` and `model-discover` verbs do this, as does infrastructure's
   `cloud-credentials` read; neither needs a claim at all.
2. **Have the tenant label it.** Where there is no caller to borrow — an
   unattended agent run, a reconciler acting on a `spec.…SecretRef` — the
   tenant (or the provider's own portal) writes `railgrid.ai/owner: <provider>`
   on the Secret. The label *is* the consent, and it is per Secret rather than
   per workspace.
3. **Not the hub identity service.** It never mints a core-group rule for
   anyone (`pkg/hub/identity/policy.go`, `core_group_forbidden`), so there is
   no scoped-identity path to a Secret.

**Enforcement.** `hack/verify-provider-contract.mjs` (`claim-selector`)
refuses a core-group `secrets` claim with no `selector.matchLabels` at review
time, and `provider-sdk/install.ValidateClaimScopes` refuses the same export at
provider init. `claims-parity` compares the scope on both copies, so a manifest
that narrows a claim and an APIExport that does not is a violation, not a
silent blanket grant.

**Upgrades.** kcp makes an accepted claim's selector **immutable**, and the hub
only ever creates a tenant `APIBinding` (it no-ops on AlreadyExists). A
workspace that enabled the provider before its claim was narrowed therefore
keeps its wider `matchAll` binding until the provider is disabled and
re-enabled there; kcp reports the gap as `PermissionClaimsValid=False` /
`PermissionClaimsMismatch` on the binding, which does not stop it being
`Bound`. Re-accept
(`POST /api/admin/providers/{name}/claims/reaccept`) does not fix it either,
for the same reason: it preserves the selector on claims the binding already
carries.

---

## Scoped identities — asking the hub instead of minting

A provider often needs a credential for one of its own objects: a background
agent run that has no human behind it, an edge agent reconnecting over its own
tunnel, a per-workspace execution identity. Three providers each solved this by
minting a ServiceAccount themselves, with a ClusterRole over somebody else's
API group and a legacy token Secret that never expires and that nothing ever
collects. That is finding M7, and it is now closed structurally.

**A provider does not mint identities. It asks the hub.**

Providers therefore hold no `serviceaccounts`, `clusterroles` or
`clusterrolebindings` permission claims. A claim on those types is a contract
violation, not a design choice.

### The service

| Route | Method | What it does |
|---|---|---|
| `/api/identities` | `POST` | Create or refresh. Idempotent on the owner tuple; returns a fresh token every time. |
| `/api/identities?provider={p}[&clusterID=…][&owner={kind}/{name}[/{uid}]&group=…]` | `GET` | List the caller's own records. Never returns a token — none is kept. |
| `/api/identities/{name}?provider={p}` | `DELETE` | Revoke: deletes the ServiceAccount (killing every outstanding token), its ClusterRole, its binding and the record. Idempotent. |

`POST` body:

```json
{
  "owner": {
    "provider": "agents", "kind": "Agent", "group": "agents.railgrid.ai",
    "version": "v1alpha1", "resource": "agents",
    "name": "scheduler", "uid": "8f0c…"
  },
  "clusterID": "root:railgrid:tenants:{org}:{ws}",
  "rules": [ { "apiGroups": ["…"], "resources": ["…"], "verbs": ["…"], "resourceNames": ["…"] } ],
  "ttlSeconds": 3600
}
```

`200` response:

```json
{ "token": "…", "tokenType": "Bearer", "expiresAt": "…", "serviceAccount": "railgrid-si-…", "name": "si-…" }
```

Unknown fields are rejected, so nothing can be smuggled past the policy.

### Authentication

`/api/identities` carries no auth middleware: its callers are providers, not
people. It authenticates exactly as the heartbeat does — bearer → TokenReview
**in the provider's own workspace** → the subject must be
`system:serviceaccount:default:provider`. A provider names itself in
`owner.provider`, and the hub verifies that claim against the bearer, so naming
another provider is a rejection rather than an escalation. Refusals say only
what class they are (`401`/`403`/`503`); the reason stays in the hub's log.

The App Studio workload exchange
(`POST /api/provider-actions/workload/exchange`) is unchanged on the wire and
keeps its own attestation: the runtime proves it is the pod the Project
environment describes, via the infrastructure provider's
`/workload-identities/review`. Underneath it is now an adapter over this same
service, so a workload identity is recorded and collected like every other.

### What may be asked for

Every rule is checked before anything is written, and one refused rule refuses
the whole request. Five shapes are admitted:

| Clause | Shape | Name-scoped? |
|---|---|---|
| A — own group | any verb on resources of an API group the **requesting provider serves** | optional |
| B — foreign read | `get` on resources of another provider's group, where that provider is **bound in the tenant workspace** | **required** |
| C — foreign verb | `create` on `{resource}/{verb}`, where `{verb}` is **declared by that provider** for that resource — as a catalog action (`spec.actions`) or a data-plane verb (`spec.dataPlane.verbs`) | **required** |
| D — platform | a fixed, closed allowlist every workspace-scoped identity needs to function at all | where the API allows it |
| E — composition | CRUD on a kind of another provider that the requester **declares it composes** (`spec.dependencies[].composes`) and a workspace or org **admin accepted** in this workspace | `create`/`list`/`watch` **no**; `get`/`update`/`patch`/`delete` **required** |

#### Who owns an API group

Clauses A, B, C and E all turn on one question — *which provider serves this
API group?* — and there is exactly one place that answers it: **`spec.resources[].group`
on the provider's own APIExport**, read by the catalog controller out of the
provider's workspace and published as `CatalogEntry.status.apiGroups` (and as
`apiGroups` on `/api/providers`).

It is **not** the APIExport's name, and the two are different for most
providers:

| Provider | `spec.apiExport.name` | groups it actually serves |
|---|---|---|
| `edges` | `edges.providers.railgrid.ai` | `edges.railgrid.ai` |
| `infrastructure` | `infrastructure.providers.railgrid.ai` | `infrastructure.railgrid.ai` |
| `code` | `code.providers.railgrid.ai` | `code.railgrid.ai` |
| `app-studio` | `ai.railgrid.ai` | `ai.railgrid.ai` |
| `kuery` | `kuery.providers.railgrid.ai` | `kuery.providers.railgrid.ai` |

The export name is what a tenant's APIBinding references
(`spec.reference.export.name`), and that is the only thing it is used for —
`IsBound` keys on it, correctly. Everything that reasons about *groups* reads
the list. An export may serve several groups, and a provider that mints
schemas at runtime (infrastructure does: its generated export ships with no
resources at all) only has them on the live object, so the file in the chart
is not a substitute either.

The read rides the CatalogEntry's normal reconcile rather than a watch: the
catalog controller's clients are the `providers.railgrid.ai` APIExport virtual
workspace, which serves `catalogentries` and nothing else, so APIExports are
not watchable from there. A provider's `init` writes the export and then
starts heartbeating, and a heartbeating entry reconciles every 30s; an entry
whose groups are still unknown requeues on the same cadence until they resolve.

Until the export can be read, the provider serves **no** group as far as the
policy is concerned. Every rule naming one is refused with `unknown_group` —
including the provider's own, under clause A — and the CatalogEntry carries
`APIGroupsUnknown=True` saying why. Guessing the group from the export name is
what this replaces: it made the edges agent's request for its own
`edges.railgrid.ai` come back `unknown_group`, and it would have rejected
kuery's and App Studio's `composes` declarations the moment their dependencies
registered. A read that fails *after* one succeeded does not retract anything:
`status.apiGroups` carries the last successful read across replicas and
restarts.

Clause E is evaluated **after D and before B/C**, so a rule on a kind the
requester declared as a composition is always measured against that
composition's verbs and the tenant's acceptance, never admitted by the weaker
foreign-read clause because it happened to ask for `get`. A rule that mixes a
composed kind with an ordinary foreign read is left to clause B: declaring a
composition must never cost a provider access it already had.

Clause D is a closed list, identical for every provider, checked **before**
clause A so owning a group cannot widen it:

| Group | Admitted | Why it is not negotiable |
|---|---|---|
| `authorization.k8s.io` | `create` on `selfsubjectaccessreviews` | the only way an identity can ask what it may do; a self-review answers about the caller, so it can never confer anything the caller lacks |
| `authentication.k8s.io` | `create` on `selfsubjectreviews` | same, for who the caller is |
| `core.kcp.io` | `get` on `logicalclusters`, `resourceNames: ["cluster"]` **only** | several data planes refuse to send anything until the identity has confirmed which workspace it is in |
| `coordination.k8s.io` | `get,create,update,patch,delete` on **named** `leases` | leader election and occupancy accounting; no identity needs a Lease it did not name |
| `railgrid.ai` | `use` on **named** `mcpservers` | the exact verb the MCP aggregate reviews before admitting a caller (`pkg/hub/mcpaggregate/verifier.go`). Admission confers nothing downstream: federation keeps forwarding the caller's own bearer, so the identity still reaches only the providers it already could |
| `apis.kcp.io` | `get` on **named** `apibindings` | an identity allowed to reach a foreign group must learn which provider serves it *in this workspace*, from the binding rather than a compiled-in string (review X-8) |

Anything else in those groups is refused — a non-self review, another
workspace's LogicalCluster, `shards`, `apiexports`, `list` on Leases,
`list`/`watch` on MCPServers or APIBindings, and any verb on an MCPServer
other than `use` (owning the object is the tenant's decision, not an
identity's).

Two of these are `get`-by-name where today's interactive code paths **list**.
That is deliberate, and it is a change the consumers absorb: the hub names each
APIBinding after the provider it enables
(`pkg/hub/restapi/providers_enable.go`), so anyone who knows which provider
they want already knows the name, while a `list` would hand a background
identity the full inventory of what a tenant has enabled.

Everything else is refused with a code the caller sees: the core API group
(Secrets above all — review X-4), every wildcard, any unnamed rule outside the
caller's own group, any verb the owning provider has not declared, and any
write on another provider's objects **except** through clause E below, which a
tenant admin has to accept first.

Outside clause E, `list` and `watch` on a **foreign** group are refused
outright. Kubernetes RBAC does not apply `resourceNames` to collection
requests, so "get/list/watch on named resources" cannot be expressed: the rule
either authorizes nothing or authorizes reading every object of that kind in
the workspace. A consumer that needs a collection reads it by name, or the
owning provider publishes a list API of its own — or declares a composition and
is granted one.

#### Clause E — composition

A product is rarely one provider: an App Studio project IS an infrastructure
`Instance` plus a code `Repository`, and App Studio's reconciler has to create
and manage those objects in the tenant's workspace. Clause E is the only shape
in this policy that writes another provider's objects, and every condition is
re-checked on every mint:

1. The requester's CatalogEntry declares `composes {group, resource}` on a
   dependency **Q** (`spec.dependencies[].composes`, validated fail-closed by
   the catalog controller: no wildcards, ordinary Kubernetes verbs only, and
   the group must be one Q serves — checked against Q's `status.apiGroups`, and
   skipped while Q is unregistered or its groups are still unknown, since
   charts reconcile in no particular order).
2. `group` really is a group Q serves, per the registry, right now.
3. Q's export is **bound** in this tenant workspace.
4. A workspace or org admin **accepted** the composition here — recorded as
   `compose:<group>/<resource>` at `workspace` scope in the provider's `Grant`
   (`pkg/hub/hubaccess`), written by the Enable dialog.
5. Every requested verb is in the **declared** verb list.

Refusals: `composition_not_declared`, `composition_not_granted`,
`composition_verb_not_declared`, `composition_rule_shape`, plus
`provider_not_bound` when Q is not enabled here.

The shapes, and why they are what they are:

| Verbs | `resourceNames` | Why |
|---|---|---|
| `create` | **must be absent** | a create request has no name yet, so a name-scoped `create` rule authorizes *nothing* |
| `list`, `watch` | **must be absent** | RBAC does not apply `resourceNames` to collection requests |
| `get`, `update`, `patch`, `delete` | **required** | the object exists and has a name, and a composing reconciler knows it — it created it |

`list` and `watch` are refused outright on a foreign group under clause B and
admitted here, and the difference is the whole argument for the clause. The
holder is a ServiceAccount **inside one tenant workspace**, with a ClusterRole
the hub wrote and reconciles; a list it authorizes returns the objects of that
one workspace — the workspace whose admin accepted the composition, of a kind
they accepted. Bounding a reconciler to a workspace is precisely what a scoped
identity is for, so "every Instance in this workspace" is the intended scope,
not an escape from one. A reconciler cannot work without a watch, and refusing
it would push providers to poll by name or to take a permission claim, which is
worse on every axis.

**Why not a permission claim.** The other way a provider could reach a
first-party group is an APIExport permission claim, and it is the wrong tool
here. A claim on a `*.railgrid.ai` group pins to **one export's
`identityHash`** (AGENTS.md §5.7), which is per-installation and stamped at
`init`; the moment an Org self-hosts the dependency, every claim-holder is
still pinned to the platform copy and kcp reports the binding as healthy while
serving none of the claimed resources (this is what
`GET …/providers/enabled` surfaces as `staleClaims`). A composition names the
dependency by NAME and resolves through whatever export is bound in that
workspace, so it survives a BYO swap. It is also revocable by the party who
should hold that power: a claim lives in the consumer's own APIExport, a
composition in the tenant's `Grant`, which Disable deletes.

Compositions follow `--provider-hub-access-platform-default` exactly as hub
access does — a platform provider composes what it declares where nobody
entitled to decide has, an org-owned one always needs an explicit acceptance.

### What the hub guarantees

- **An owner.** Every identity names a real object in a tenant workspace,
  verified to exist with that UID before anything is minted. The ServiceAccount
  name is a hash of the owner tuple **including the UID**, so a deleted and
  recreated object never inherits its predecessor's credential.
- **A TTL.** Tokens are TokenRequest-minted: ten minutes for workload
  attestation, one hour for provider-asserted, capped at twenty-four hours
  whatever is asked for. No legacy token Secrets.
- **A record.** One `ScopedIdentity` in `root:railgrid:system:tenants` — a
  workspace no tenant, provider or user identity can reach — carrying the
  owner, the cluster, the attestation and the exact accepted rules. The token
  is never written there, or anywhere.
- **Collection.** A hub reconciler sweeps the records and deletes the identity
  when its owner is gone. It has no cross-workspace owner watch (the owners
  span every workspace and several providers' groups, whose CRDs arrive with
  their APIExports), so it re-probes each record at its own token TTL: an
  orphan is collected within one TTL of the last refresh, against *never*
  before. A provider wanting immediate revocation calls `DELETE` from its own
  delete reconciler rather than waiting for the sweep.
- **Reconciled rules.** A record's rules are applied, not merely created: a
  narrowed grant shrinks the ClusterRole, and an out-of-band widening is
  reverted on the next sweep.
- **A real group owner.** Group ownership is resolved from what each
  provider's APIExport actually serves, never from its name, and a provider
  whose export the hub has not read owns nothing. There is no shape in which a
  guess about who serves a group produces a grant.

### The client

`provider-sdk/identityclient` is the supported way to call it:

```go
client, _ := identityclient.New(identityclient.Options{Provider: "agents"})
source := identityclient.NewTokenSource(client, identityclient.Request{
    Owner:     identityclient.Owner{Kind: "Agent", Group: "agents.railgrid.ai", Version: "v1alpha1",
                                     Resource: "agents", Name: agent.Name, UID: string(agent.UID)},
    ClusterID: clusterID,
    Rules:     rules,
})
token, err := source.Token(ctx)   // refreshes at 80% of the TTL
...
client.Release(ctx, clusterID, owner)  // on the owner's delete path
```

It authenticates with `hubclient.ResolveHubToken` — the same provider
service-account token the heartbeat uses. A `*identityclient.Error` reports
whether retrying could help: a policy refusal is permanent, an unreachable hub
is not.


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
| Scoped identity service | `pkg/hub/identity/` |
| Scoped identity policy | `pkg/hub/identity/policy.go` |
| Group ownership (registry → policy) | `pkg/hub/identity/policy.go` `RegistryCatalog.GroupOwner` |
| APIExport → served groups | `pkg/hub/providers/provision.go` `ResolveAPIExportGroups` |
| The single identity minter | `pkg/hub/serviceaccounts/scoped_identity.go` |
| Scoped identity record | `apis/tenancy/v1alpha1/types_scoped_identity.go` |
| Scoped identity routes | `pkg/hub/restapi/identities.go` |
| Scoped identity client | `provider-sdk/identityclient/` |

