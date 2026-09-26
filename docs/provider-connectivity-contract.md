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
  (contract 1); backend/controller code reaches kcp as a **non-privileged
  provider ServiceAccount via an APIExportEndpointSlice**, and a verb handler
  answers **for the identity kcp stamped** — a SubjectAccessReview on the
  caller's behalf, then acting as the provider through its export virtual
  workspace — with the caller's forwarded bearer left only on the MCP
  projection (contract 2); and one provider reaches **another** provider only
  through that provider's **published APIExport resources + custom
  subresources** — never into its backend (contract 3).
- **Every declared verb and action is a kcp custom subresource** named
  `{resource}/{verb}` on the owning provider's APIExport, so it is an ordinary
  API path a shard reverse-proxies to the provider:
  `/clusters/{id}/apis/{group}/{version}/{resource}/{name}/{verb}`. **That is
  the only transport.** The hub-proxied grammar
  (`/services/providers/{name}/{dataplane,actions}/clusters/…`) is gone — not
  aliased, not deprecated — and the hub's backend proxy carries no verbs.
- **A composed kind is reached by a permission claim again** — one with **no
  `identityHash`**, generated from `spec.requires[]`, admitted
  by a cluster-scoped `PermissionClaimPolicy` the hub applies at bootstrap.
  A verb on a composed kind is a claimed **custom subresource** reached the
  same way. The hub-minted scoped identity remains only for the calls a claim
  does not (yet) carry.
- The hub exposes providers through **two different proxies** with **different
  token handling**: the UI proxy forwards **no token**, the backend proxy
  forwards the caller's `Authorization` header **as-is** — and serves only
  MCP, browser OAuth, signed webhooks, the agent tunnel, health and the
  hub-only routes. A verb never goes through it.
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
  workspace, bounded by the permission claims generated from
  `spec.requires` (all of which are tenant-scoped by construction) —
  and, for a core-group claim on `secrets`, by that claim's **label selector**
  (see "Label-scoped claims" below).
- **(2b) Per-request** — on a **verb**, the provider holds no caller
  credential at all: kcp authenticated the caller and stamped their identity
  in requestheader headers, the provider asks a `SubjectAccessReview` on that
  identity's behalf and then acts **as itself** through its export virtual
  workspace, scoped to the logical cluster **in the path**
  (`provider-sdk/dataplane.Gate`, then `dataplane.Authorize` for any further
  question about the caller). On the **MCP projection** — the one class that
  still carries a bearer — the provider drops its own credential and acts **as
  the caller** with the token the hub forwarded, scoped to the workspace whose
  kcp logical-cluster ID arrives in `X-Railgrid-Tenant` / `X-Railgrid-Cluster`
  (both carry the ID; the workspace path is never sent).

Both 2a and 2b are admin-free. New providers should pick one (or use 2a for
controllers and 2b for request-driven endpoints, like `code` and
`infrastructure` do) and never construct a kcp-admin / root client.

**Contract 3 — provider-to-provider path.** When provider A needs something
provider B owns, A goes through **B's published API**, not B's backend:

- **B's `APIExport` resources** — A binds B's `APIExport` (an `APIBinding`
  in the tenant workspace) and reads/writes B's CRs over the normal
  `/clusters/...` path. This is the control-plane channel (spec/status).
- **B's published data-plane subresources** on those resources (for example
  `instances/log`, `instances/proxy`, `repositories/mint-clone-token`, or a
  component-scoped `…/{name}/components/{component}/sync`) — for streams,
  proxies, and other verbs that aren't plain CRUD. Each is a real kcp custom
  subresource on B's APIExport, so A addresses it as
  `/apis/{group}/{version}/{resource}/{name}/{verb}` on a kcp front door and
  lets the shard reach B. B serves them against *its* backend; A never sees
  it.

A third shape exists for the narrow case where A does not merely *read* B's
kind but **composes** it — creates and manages it as part of A's own product.
That is an identity-agnostic permission claim on A's own APIExport, generated
from `spec.requires[]`; see
[`providers.md` §"Composition"](./providers.md#composition--one-provider-building-on-anothers-kinds).
It is still B's published API: the claim resolves against whatever export that
workspace bound for B's group, and the tenant accepts it.

The first two are invoked **as the identity kcp stamps on the kube path** —
the tenant user or ServiceAccount on the hub's `/clusters/{id}`, or provider A
itself through **its own** export virtual workspace when the verb is one it
composes (`Callers.ExportVerbURL` + `ProviderHTTPClient`; end-user identity is
not carried across providers) — and **routed by binding, never by a backend
URL**. A must never hold a credential into B's runtime cluster /
database / internal Service, call B's internal endpoints directly, or encode
B's backend topology in its own config. The owning provider is the single
holder of its backend credential. This is the access mechanics behind
[`providers.md` §"Provider isolation"](./providers.md#provider-isolation-the-cross-provider-boundary);
the canonical example is the App Studio → infrastructure data-plane
decoupling ([`app-studio-runtime-decoupling.md`](./app-studio-runtime-decoupling.md)).

---

## Pillar 2 route classes

The three contracts above say *how* a provider authenticates. This section
says *what may exist* on a provider's one backend origin. Two doors open onto
it: the **kcp shard**, which reverse-proxies every declared verb to it as a
custom subresource, and the hub's **backend proxy** behind
`/services/providers/{name}/*` (`pkg/hub/providers/proxy.go`), which carries
the non-kube classes and nothing else. The list is **closed**: a route that is
not one of these classes is a deviation, even when it authorizes correctly.

| Class | Shape | Auth | Reference |
|---|---|---|---|
| **(a) Data-plane verb** | `/clusters/{clusterID}/apis/{group}/{version}/{resource}/{name}/{verb}[/{tail}][?component={c}]`, as the shard forwards it | kcp-stamped requestheader identity; **no bearer**; kcp authorized the `{resource}/{verb}` noun, the gate runs a SAR for `get` on the parent | `provider-sdk/serve.Options.Subresources`, `dataplane.ParseSubresourceRequest`, `dataplane.Gate` |
| **(a′) Action** | the same path (`{verb}` is the action's name; its `/v<n>` is **not** in the path); body `{"input":{}}`; `actionwire` envelope | same as (a) | `serve.SubresourceRoute{Action: true, Version}`, `dataplane.Serve` |
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
2. **On the backend proxy the bearer is the only trust root.** The hub
   strips inbound `X-Railgrid-*` and re-injects `X-Railgrid-User` plus the
   caller's resolved workspace's cluster ID as both `X-Railgrid-Tenant` and
   `X-Railgrid-Cluster` (the sidebar's `X-Railgrid-Org` / `X-Railgrid-Workspace`
   selection steers which). A provider may read those headers for
   **addressing** and **labels**; it must never derive authorization from
   them. No route on the proxy names a cluster in its path any more, so there
   is no path-cluster authorization at the hub and no path-versus-header
   comparison in the provider. An anonymous request is unchanged: no identity
   headers, no hub-side refusal.

   On a verb the trust root is different, not weaker: there is no bearer, and
   the caller is the identity **kcp itself** authenticated and stamped into
   `X-Remote-User`, `X-Remote-Group` and `X-Remote-Extra-*`, exactly as kcp's
   front proxy does when it reaches a shard, together with a hop counter
   (`X-Kcp-Internal-Proxy-Hops`). A provider believes those headers only
   because the connection is one it already trusts, refuses a request that
   carries none (401 — anonymous is not a fallback, and the provider's own
   identity is never a substitute), and refuses one over `dataplane.MaxHops`.
   **The URL published in the provider's `DataPlaneEndpointSlice` must
   therefore be its shard-facing address**: anyone who can reach it directly
   can claim any identity. The hub's backend proxy strips those headers from
   everything it forwards for the same reason, so the two doors cannot be
   confused. The cluster a verb acts in is the one **in the path**, and kcp
   already authorized the caller for it before proxying.
3. **The caller is authorized for every verb — by kcp, then by the gate.**
   kcp authorizes the `{resource}/{verb}` noun with ordinary RBAC before it
   proxies the request at all. It maps the HTTP method onto the RBAC verb, so
   the hub materializes every data-plane grant with verbs **`*`**
   (`pkg/hub/serviceaccounts/workload_identity.go`; clause C in §"Scoped
   identities" mints the same), and a composition claim on a
   `{resource}/{verb}` is spelled `verbs: ["*"]`. Then `dataplane.Gate`
   runs with what the provider holds — a caller's name and groups, and no
   credential. **Visibility keeps its meaning and changes its mechanism:** the
   gate creates a `SubjectAccessReview` — not the *Self* variant — for `get`
   on `{resource}/{name}` on the caller's behalf, and only then reads the
   object as itself, through its own APIExport virtual workspace, so the
   handler can pin its UID and spec. A `deletionTimestamp` denies. **The verb
   grant is not repeated:** asking again would only prove the shard did its
   job. A **foreign provider** — another provider's ServiceAccount forwarded
   through its own export virtual workspace on a `spec.requires` claim — is
   authorized by the claim kcp enforced; the gate reads the parent as the
   provider and asks no review, which would refuse an identity with no RBAC
   in the tenant workspace. See [provider-actions.md](./provider-actions.md).
4. **After the gate a handler acts as the provider, not as the caller.**
   There is no caller credential to act with. Any further question about the
   caller — may they read a second object the verb touches, may they perform
   the write the verb performs on their behalf — is `dataplane.Authorize`, a
   SubjectAccessReview run on the caller's behalf through the same client the
   gate returned. The bearer-credentialed tenant client
   (`Callers.For`, `providers/*/tenant/client.go`, built from the provider
   kubeconfig's host and CA **only**, cached per (cluster, token hash)) is
   for the MCP projection alone. This is contract 2b, stated as a route rule.
5. **Cross-provider access is by binding, not by string.** Provider A reaches
   provider B only through B's bound CRs and B's data-plane verbs — as
   **itself**, through its own export virtual workspace, on a
   `spec.requires[]` entry whose resource is `"{resource}/{verb}"`, carrying no
   verbs at all (`Callers.ExportVerbURL`, `ProviderHTTPClient`) — resolving
   the target from the binding or from a `status` field B publishes. No
   foreign credential, no foreign Secret read, no hardcoded
   `/services/providers/<b>/...` format string, and no end-user bearer
   carried across. This is contract 3.
6. **No dev bypass in release binaries.** A switch such as
   `RAILGRID_DEV_ALLOW_TENANT_QUERY`, which takes the bearer or the tenant
   from the query string, must be compiled out of shipped binaries.

### Declare the verbs you serve

A class (a) verb is **declared** in the CatalogEntry, beside the actions:

```yaml
spec:
  export:
    name: infrastructure.providers.railgrid.ai
    resources:
      - name: instances                 # a resource this export serves
        apiVersion: infrastructure.railgrid.ai/v1alpha1   # declared ONCE
        kind: Instance
        verbs:
          - name: exec        # no version, no slash — the {resource}/{verb} coordinate
            description: "Open an interactive shell or run a command in the instance."
            stream: true      # upgrades or streams rather than returning one response
            readOnly: false   # does not mutate the resource or what it fronts
        actions: [ … ]        # versioned, schema'd calls on the same resource
```

Declaring a verb **grants nothing**. The provider still authorizes every
call. What the declaration does is **publish** the coordinate: `apiexportgen`
turns every `spec.export.resources[].verbs[]` entry and every
`spec.export.resources[].actions[]` entry
into a `spec.resources[]` entry on the provider's APIExport named
`"<resource>/<verb>"` — a kcp **custom subresource** — so the verb becomes an
ordinary API path:

```
/apis/{group}/{version}/{resource}/{name}/{verb}
```

discoverable with `kubectl`, authorized by kcp as RBAC on the
`{resource}/{verb}` noun, and reverse-proxied by the serving shard to the
provider. Two further consequences of the same declaration:

- The hub scoped-identity service will only mint a capability on
  `{resource}/{verb}` (verbs `*`) for a verb the owning provider declares. Before this
  field existed, `exec`, `proxy`, `ssh` and `delegate` lived only in provider
  code, so no cross-provider capability for any of them could be minted at all
  — see §"Scoped identities", clause C.
- Consumers read `export.resources[].verbs` (and `.actions`) off
  `/api/providers` — the same four sections as the spec, `apiVersion` and
  `kind` included — instead of hardcoding a coordinate nobody validates
  (rule 5, and review X-8).

**The name is a kcp resource name, and a bad one is fatal to the whole
export.** `"<resource>/<verb>"` must match
`^[a-z][-a-z0-9]*[a-z0-9](/[a-z][-a-z0-9]*[a-z0-9])?$` — lower-case letters,
digits and hyphens, **no underscores** — and may not be `status` or `scale`,
which describe the object's own shape, are declared on the APIResourceSchema,
and are refused by kcp's APIExport admission. One rejected name makes the
export unappliable, not just its entry, so `hack/verify-provider-contract.mjs`
checks every declared coordinate (`subresource-name`) at review time and
`apiexportgen` refuses to generate. Underscored verbs are gone
(`mint_clone_token` is now `mint-clone-token`), and infrastructure's status
verb is `instances/runtime-status`.

**What the generated entry looks like.** `{name, group, schema, storage}`,
where `group` is read off the parent resource's own entry (kcp refuses a
subresource whose parent the same export does not serve), `storage.virtual.
reference` points at the provider's `DataPlaneEndpointSlice`
(`dataplane.railgrid.ai/v1alpha1`, one per provider, named after the APIExport,
whose `status.endpoints[].url` is `spec.serving.backend.url`), and `schema` names the
APIResourceSchema of the verb's own kind — the `<Verb>Request` type every
provider declares per verb in its API package (`apis/.../subresources.go`),
minted by controller-gen and `apigen` like any stored kind, shipped by
`apiexportgen` into `deploy/chart/files/schemas/` as `<verb>.<group>.yaml`, and
kept out of `spec.resources` since nothing is stored under it. The shard never
resolves it (the request is routed from the storage reference); a claimer's
APIExport virtual workspace does, to learn the kind before building the claimed
subresource. A verb without a type fails codegen. `provider-sdk/install` applies
the CRD and the slice, and waits for the CRD to be **Established**, before the
APIExport: a reference to a kind that is not established is never replicated
to the shards, and the subresource would then route nowhere, silently.

A verb is on a resource of the provider's **own** API group — a resource entry
must name an `apiVersion` whose group the export actually serves, checked by
`hack/verify-provider-contract.mjs` (`export-group`) — and a standard
Kubernetes verb (`get`, `list`, `create`, …) may not be one, because
`instances/get` reads like the ordinary `get` on `instances` and is not. A
malformed declaration fails closed: the provider leaves the registry rather
than keeping a stale, wider verb surface.

Actions (`actions[]`) and data-plane verbs (`verbs[]`) are two lists on the
same resource entry, sharing one coordinate namespace: a verb and an action of
the same name on the same resource is refused, because both would generate the
same subresource. An action is versioned, schema'd and request/response, and
its `version` is **not** part of the coordinate — `mint-clone-token` v1 publishes
`repositories/mint-clone-token`. A data-plane verb is unversioned and
streaming or proxying. Declare each capability as exactly one of them. A verb served with the *action*
envelope whose body is too large to describe under `limits.maxInputBytes` —
an uncatalogued large-upload verb, like the code provider's
`repositories/stage-snapshot` and `repositories/stage-commit-bundle` — is
still declared, as a data-plane verb, because clause C mints nothing for a
coordinate that appears in neither list (see
[provider-actions.md](./provider-actions.md) §"Uncatalogued large-upload
verbs").

**Serving them.** The shared server-kit that parses the path, runs the gate,
enforces declared limits and writes the envelope is `provider-sdk/dataplane`;
`provider-sdk/serve` owns the server layout. Write a new provider against them
rather than hand-rolling a parser and gates — four incompatible dialects once
existed because every provider wrote its own, and `serve.New` no longer mounts
a `/dataplane/` or `/actions/` prefix at all. Three things wire a verb:

- `serve.Options.Subresources`, a `"<resource>/<verb>"` → `SubresourceRoute`
  table saying which coordinates exist and whether each is a data-plane verb
  or an action (and at which version). Build it with
  `serve.SubresourcesFromCatalogEntryFile(manifestPath)` so it cannot disagree
  with what `apiexportgen` published; a coordinate absent from the table is a
  404 even when a handler would have answered, because the declaration is the
  contract. `serve.New` mounts one adapter at the raw prefix `/clusters/`,
  dispatches to `Options.DataPlane` or `Options.Actions` with the URL
  **untouched**, and hands the parsed route (`dataplane.RouteFrom`, an
  action's version restored from the table) and the stamped identity
  (`dataplane.ProxiedIdentityFrom`) to the handler through the request
  context — **the only place those context values are ever set**, so nothing
  else in the process can forge them.
- `dataplane.ParseSubresourceRequest`, `dataplane.ProxiedCaller` and
  `dataplane.CheckHops`, which parse the path (the component of a
  multi-component object arrives as `?component=`), read the stamped identity
  and bound the hop count.
- `dataplane.WithProviderConfig(cfg, exportName)` on `NewCallerFactory`,
  which makes the factory a `ProviderCallerFactory`. With no caller bearer
  there is no caller client to build, so the gate needs
  `AsProvider(clusterID)`, which acts through the export's virtual workspace
  (read from the provider's `APIExportEndpointSlice`, or pinned with
  `WithProviderEndpoint`) — the shard's own `/clusters/<id>` refuses the
  provider identity, verified against kcp-dev/kcp#4388. `Gate` fails loudly
  rather than silently acting as the provider when the factory is not one.
- The hub's backend proxy strips `X-Remote-User`, `X-Remote-Group`,
  `X-Remote-Extra-*` and `X-Kcp-Internal-Proxy-Hops` from everything it
  forwards (`pkg/hub/providers`), so nothing routed through the hub can carry
  a forged shard identity to a provider's `/clusters/…` route.

**Streaming verbs and the shard deadline.** A verb that streams (SSE, a log
tail, exec, a WebSocket upgrade) passes through kcp's reverse proxy like any
other, and the shard's request deadline is the ceiling on one streamed
response. The embedded kcp sets it to one hour
(`pkg/hub/kcp/embedded.go`, `providerVerbRequestTimeout`); an external kcp
needs the equivalent `--request-timeout` on its shards, or anything but the
verbs kube-apiserver knows to be long-running is cut at the 60s default. A
browser cannot set `Authorization` on a WebSocket upgrade, so it presents the
bearer as the Kubernetes subprotocol
`base64url.bearer.authorization.k8s.io.<base64url token>`; the hub's kcp proxy
reads it for the membership check and forwards the upgrade untouched
(`pkg/server/proxy/proxy.go`, `websocketBearer`), and kcp authenticates it
natively.

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
| **Backend proxy** (`NewBackendProxy`, `proxy.go:92`) | `/services/providers/{name}/*` — MCP, browser OAuth, signed webhooks, the agent tunnel, health and the hub-only `/workload-identities/*`; **no verbs** | **Forwards the caller's `Authorization` header as-is** for a platform provider (a delegated ServiceAccount token replaces it for an org-owned one, or when the delegation policy selects it), and additionally injects `X-Railgrid-User` plus the caller's resolved workspace's kcp logical-cluster ID as both `X-Railgrid-Tenant` and `X-Railgrid-Cluster`. Inbound `X-Railgrid-*` and `X-Remote-*` headers are **always stripped** first (anti-spoofing). |
| **kcp proxy** (`pkg/server/proxy`) | `/clusters/{id}/…` | Membership-gated, forwarded to kcp **as the caller**. Every declared verb is reached here as `/clusters/{id}/apis/{group}/{version}/{resource}/{name}/{verb}`; the shard authorizes it and reverse-proxies it to the provider with the caller's identity stamped. The hub never sees the verb as such. |

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
> **backend** is via the backend proxy (`/services/providers/{name}/*`), and
> the only class that still needs it there is MCP. A verb never carries a
> bearer: kcp holds the token, the provider gets an identity. A provider that
> does all UI data through the kcp proxy and serves no MCP keeps its backend
> off the token path entirely.

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
| `kuery` | kube REST through `/clusters/{cluster}` for SavedViews and Engagements; a query runs as the declared verb `savedviews/run`, invoked as the caller | ✅ Conforms |
| `app-studio` | kube REST through `/clusters/{cluster}` for Projects, Sessions and Threads; every other operation is a declared verb or action on those kinds, invoked as the caller | ✅ Conforms |

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
| `kuery` | (2a) minted provider SA + APIExportEndpointSlice; edges `kubernetesclusters` arrive on the same multicluster manager through the identity-agnostic claim its `spec.requires` entry generates | ✅ Conforms |
| `app-studio` | (2a) for infrastructure `instances` and code `repositories`/`repositorycommits`, through the claims its `spec.requires` entries generate, on the endpointslice manager; (2b) clears the provider credential and builds the tenant client from the forwarded caller token for request-driven work; label-scoped `secrets` claim | ✅ Conforms |

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
| `app-studio` → `infrastructure` (Template development data plane) | Selected Template instance (control plane) + infrastructure's declared verbs on `instances` as claimed custom subresources, called through app-studio's own export virtual workspace as itself on the claim its `spec.requires` declaration generates (`Callers.ExportVerbURL`), `?component=` for a component-scoped verb | ✅ Conforms |
| `app-studio` → runtime cluster (historical) | Formerly held `APP_STUDIO_RUNTIME_KUBECONFIG`, a direct second credential into infrastructure's runtime cluster | ❌ Removed anti-pattern; retained only as historical context in [`app-studio-runtime-decoupling.md`](./app-studio-runtime-decoupling.md) |
| `kuery` → `Edge`s | watches edges `KubernetesCluster`s through its own APIExport virtual workspace (the claim generated from its `spec.requires` entry) and engages a cluster through the edges provider's published `kubernetesclusters/k8s` verb at the URL that object's `status` publishes, as a hub-minted identity scoped to that workspace — never the edge provider's backend | ✅ Conforms |

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
requires:
  - resources:                  # no group == the core group
      - name: secrets
        verbs: [get, list, watch, create, update, delete]
        selector:               # MANDATORY on core secrets
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
silent blanket grant. There is only one source now — `spec.requires` — so the
comparison is a plain equality in both directions, with one asymmetry: a
`"<resource>/<verb>"` coordinate carries no verbs in the manifest and every
verb in the generated claim. A selector may be set on any plain resource entry;
a verb coordinate refuses one, because the parent claim already covers the
object the verb is invoked on.

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
| C — foreign verb | `{resource}/{verb}`, where `{verb}` is **declared by that provider** on that resource — as a catalog action (`spec.export.resources[].actions`) or a data-plane verb (`spec.export.resources[].verbs`); `ProviderDeclaredVerbs` answers both at once, because the policy draws no distinction. Whatever verbs the request spells, the rule minted is `*`: kcp maps the HTTP method onto the RBAC verb on a custom subresource, so the coordinate is the grant | **required** |
| D — platform | a fixed, closed allowlist every workspace-scoped identity needs to function at all | where the API allows it |
| E — composition | CRUD on a kind of another provider that the requester **declares it requires** (`spec.requires`) and a workspace or org **admin accepted** in this workspace | `create`/`list`/`watch` **no**; `get`/`update`/`patch`/`delete` **required** |

#### Who owns an API group

Clauses A, B, C and E all turn on one question — *which provider serves this
API group?* — and there is exactly one place that answers it: **`spec.resources[].group`
on the provider's own APIExport**, read by the catalog controller out of the
provider's workspace and published as `CatalogEntry.status.apiGroups` (and as
`apiGroups` on `/api/providers`).

It is **not** the APIExport's name, and the two are different for most
providers:

| Provider | `spec.export.name` | groups it actually serves |
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
kuery's and App Studio's `spec.requires` declarations the moment their dependencies
registered. A read that fails *after* one succeeded does not retract anything:
`status.apiGroups` carries the last successful read across replicas and
restarts.

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
write on another provider's objects.

`list` and `watch` on a **foreign** group are refused outright. Kubernetes RBAC does not apply `resourceNames` to collection
requests, so "get/list/watch on named resources" cannot be expressed: the rule
either authorizes nothing or authorizes reading every object of that kind in
the workspace. A consumer that needs a collection reads it by name, or the
owning provider publishes a list API of its own — or declares a composition and
is granted one.

#### Composition is a claim, not a clause

A product is rarely one provider: an App Studio project IS an infrastructure
`Instance` plus a code `Repository`. The requester's CatalogEntry declares one
`spec.requires` entry per API group, naming the `provider` that serves it and
the `resources` (and verbs) it needs from it;
the generator turns each resource entry into an identity-agnostic permission claim on the
requester's own APIExport and a `PermissionClaimPolicy` entry, and the tenant's
acceptance at Enable is what accepts that claim on its `APIBinding`. From then on
the requester reads, watches and writes the composed kinds through its own
export virtual workspace, as itself — and calls a dependency's verb on them as a
claimed custom subresource, which kcp forwards under the requester's identity
and the owning provider's gate trusts. There is nothing for the identity policy
to mint here, and no policy clause for it.


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

1. **The CRUD-facade REST routes (`/services/providers/{name}/api/*`) are
   gone.** `kuery`, `app-studio`, `agents` and `quickstart` used to drive their
   UIs through their own REST, mirroring CRs. `serve.New` now refuses anything
   under `/api/*`, the UIs read and write their kinds through `/clusters/{cluster}`
   kube REST, and every remaining backend operation is a declared verb or
   action on a bound resource — a custom subresource reached through the API
   path itself. `make verify-provider-contract` reports zero `adhoc-rest`
   violations with no exceptions. A non-kcp store (kuery's SQL index) or a
   streaming surface (app-studio's chat) still justifies a *backend*; it
   serves Pillar 2 classes, never a CRUD facade.
2. **The hub-proxied verb grammar is gone (2026-09-25).** Verbs and actions
   were once reachable twice — as
   `/services/providers/{name}/{dataplane,actions}/clusters/{id}/{resource}/{name}/{verb}`
   through the hub's backend proxy with the caller's bearer and two
   provider-side gates, and as the custom subresource. The second entry point
   is now the only one. `provider-sdk/dataplane` no longer has
   `ParsePath`/`ParseRequest`/`DataplaneRoot`/`ActionsRoot`/`ProviderPath`,
   `provider-sdk/serve` mounts no `/dataplane/` or `/actions/` prefix, the hub
   proxy performs no path-cluster authorization and injects no path-derived
   `X-Railgrid-Cluster`, and the edges `ticket` verb (the short-lived
   WebSocket ticket the browser terminal used to mint) is removed: the browser
   presents its bearer as the WebSocket subprotocol instead. Portals
   build verb URLs with `kubeVerbPath` (`provider-sdk/portalkit/kube.ts`);
   `serviceBase()` remains only for `/oauth` and `/mcp`. The `railgrid` CLI
   addresses verbs on the hub's `/clusters/{id}/apis/…` — an edge
   kubeconfig's `server` URL and `status.URL` are kube paths.

---

## Checklist for a new provider

- [ ] UI reads/writes go through `/clusters/{cluster}` kube REST via the
      `portalkit` kube client (contract 1). The backend is **not** a data
      path; there is no `/api/*`.
- [ ] **Every backend route is one of the classes** in §"Pillar 2 route
      classes" — (a) data-plane verb and (a′) action, both shard-forwarded
      custom subresources, (b) `/mcp`, (c) health, (d) browser OAuth, (e)
      hub-only, (f) agent tunnel, (g) signed webhook. Name the class for each
      route you add. If you cannot, the route does not belong on the backend.
- [ ] Every verb and action is **declared** in `manifest.yaml`, on the resource
      it is served on (`spec.export.resources[].verbs`, `.actions`), and every `{resource}/{verb}`
      it produces passes kcp's name rule — lower-case, digits and hyphens, no
      underscores, never `status` or `scale`. `make verify-provider-contract`
      (`subresource-name`) is the check.
- [ ] Verbs are served through `provider-sdk/dataplane`, reachable **one**
      way: the shard-forwarded kube path
      `/clusters/{clusterID}/apis/{group}/{version}/{resource}/{name}/{verb}`,
      where kcp has already authorized the `{resource}/{verb}` noun and the
      gate runs a `SubjectAccessReview` for `get` on the parent on the
      caller's behalf, then acts as the provider. No `/dataplane/` or
      `/actions/` prefix, no bearer, no `X-Railgrid-Cluster` on a verb.
- [ ] `spec.requires` carries `authorization.k8s.io/subjectaccessreviews`
      (`create`). The gate runs its SubjectAccessReview
      through the export virtual workspace, and kcp serves that builtin there
      only for an export that claims it and a binding that accepted it —
      without the claim every verb is a 500 (verified against
      kcp-dev/kcp#4388). `hack/verify-provider-contract.mjs`
      `subresource-access-claim` refuses an export with a `<resource>/<verb>`
      entry and no such claim.
- [ ] `serve.Options.Subresources` is set — from
      `serve.SubresourcesFromCatalogEntryFile`, not by hand — and the caller
      factory is built with `dataplane.WithProviderConfig(cfg, exportName)`. The URL the
      `DataPlaneEndpointSlice` publishes (from `spec.serving.backend.url`) is the
      **shard-facing** address.
- [ ] A kind of **another** provider that this provider creates and manages is
      declared in `spec.requires[]` — one entry per group, naming the
      `provider` that serves it — and nowhere else: the
      identity-agnostic permission claim, the tenant's consent and the
      `PermissionClaimPolicy` entry are all generated from it. Re-run
      `make permission-claim-policy` when it changes.
- [ ] No kcp-admin / root client anywhere in the provider (contract 2); the
      admin credential appears only in the one-shot `init`.
- [ ] Controllers run as the **minted provider SA** off the
      **APIExportEndpointSlice** (2a), under `leaderelection.Run`, with
      `/readyz` and heartbeat `CanSend` from `vwhealth`; declare exactly the
      resources and verbs you need under `spec.requires` and no more. `RequeueAfter` only for the three sanctioned uses in §"Pillar 1
      carve-outs".
- [ ] The MCP projection — the one route that still carries a bearer — acts
      as the **caller** via the forwarded token (2b): build the tenant client
      from `Authorization` + the cluster ID in `X-Railgrid-Cluster`
      (`X-Railgrid-Tenant` carries the same ID), drop the provider's own
      credential. Never parse a workspace path out of a header. A verb
      handler never does this; it asks `dataplane.Authorize` instead.
- [ ] Never trust inbound `X-Railgrid-*` headers in the provider — the backend
      proxy strips and re-injects them; treat them as hub-asserted only.
- [ ] Durable tenant state is on the CR. If it is not, it is one of the two
      carve-outs (transient artifact, or projection kind with a purge
      finalizer) and the architecture doc says so.
- [ ] To reach **another provider**, bind its `APIExport` and call its CRs /
      data-plane verbs as yourself through your own export virtual workspace,
      on a `spec.requires[]` entry whose resource is `"{resource}/{verb}"`,
      with no verbs of its own (contract 3). Never hold a credential into another
      provider's backend (runtime cluster, DB, internal Service) or hardcode
      its backend URL.

---

## Code anchors

| Concern | Anchor |
|---------|--------|
| UI proxy (no token) | `pkg/hub/providers/proxy.go:52` |
| Backend proxy (non-kube classes only; forwards token + injects identity) | `pkg/hub/providers/proxy.go` `NewBackendProxy` |
| Org-owned provider hop (through kcp at the edges `services/{name}/proxy` verb, delegated token in `X-Railgrid-Upstream-Authorization`) | `pkg/hub/providers/proxy_edge.go` |
| Verb URL construction (the one spelling) | `pkg/apiurl/urls.go` `ProviderVerbPath`, `EdgeVerbPath`; `provider-sdk/portalkit/kube.ts` `kubeVerbPath` |
| WebSocket bearer subprotocol | `pkg/server/proxy/proxy.go` `websocketBearer` |
| Shard request deadline for streaming verbs | `pkg/hub/kcp/embedded.go` `providerVerbRequestTimeout` |
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
| Subresource entries on the APIExport | `provider-sdk/apiexportgen/subresources.go` |
| Composition → identity-agnostic claim | `provider-sdk/apiexportgen/apiexportgen.go` `MergeCompositionClaims` |
| Endpoint object kcp routes a verb through | `provider-sdk/dataplaneendpoints/`, `provider-sdk/install/dataplaneendpoints.go` |
| Verb path, stamped identity, hops | `provider-sdk/dataplane/subresource.go`, `provider-sdk/dataplane/identity.go` |
| Gate (SAR as the caller, then act as the provider), `Authorize` | `provider-sdk/dataplane/gate.go` |
| Provider callers: `AsProvider`, `ExportVerbURL`, `ProviderHTTPClient` | `provider-sdk/dataplane/caller.go` |
| Subresource route table and adapter | `provider-sdk/serve/subresource.go`, `provider-sdk/serve/subresource_table.go` |
| PermissionClaimPolicy generator | `hack/generate-permission-claim-policy.mjs` → `config/kcp/permissionclaimpolicy.yaml` |
| PermissionClaimPolicy bootstrap (admin VW) | `pkg/hub/bootstrap/permissionclaimpolicy.go` |

