---
layout: default
title: MCP Architecture
nav_order: 9
description: "How faros aggregates Model Context Protocol (MCP) tools from in-binary edges and out-of-process providers into one endpoint"
---

# MCP Architecture
{: .no_toc }

How faros exposes a single Model Context Protocol (MCP) endpoint that federates
tools from connected **edges** (compiled into the hub) and from **providers**
that run as separate processes.
{: .fs-6 .fw-300 }

<details open markdown="block">
  <summary>Table of contents</summary>
  {: .text-delta }
1. TOC
{:toc}
</details>

---

## TL;DR

There is **one** MCP endpoint a client connects to — the *aggregate MCPServer
virtual workspace*, served by the hub:

```
https://<hub>/services/mcpserver/{cluster}/apis/faros.sh/v1alpha1/mcpservers/{name}/mcp
```

That single endpoint is filled, **per request**, from two sources:

1. **In-binary tool families** — edge tool sets (`kubernetes`, `linux`) compiled
   into the hub and registered at `init()` via `aggregate.RegisterToolFamily`.
2. **Out-of-process provider federation** — every *Ready* provider (e.g. the
   infrastructure provider, which runs as its own process) has its `/mcp`
   endpoint fetched over HTTP and its tools re-exposed as `<provider>__<tool>`.

Every tool runs **as the caller**, authorized by the caller's RBAC in the
tenant workspace. There is no provider-wide identity. Platform providers receive
the caller's own bearer; an organization's own (bring-your-own) providers
receive a short-lived delegated token for the same user and workspace instead,
and never the caller's bearer — see
[Org-owned providers](#org-owned-bring-your-own-providers).

---

## The aggregate endpoint

The MCP surface is registered as a faros *built-in provider* and mounted by the
hub as a virtual workspace.

- **Registration** — [`providers/mcp/manifest.go`](https://github.com/faroshq/faros/blob/main/providers/mcp/manifest.go) calls
  `providers.RegisterBuiltin(...)` with
  `VirtualWorkspaceMount = apiurl.PathPrefixMCPServer` (`/services/mcpserver`)
  and `VirtualWorkspaceHandler = mcpvirtual.Build`.
- **Mounting** — [`pkg/hub/server.go`](https://github.com/faroshq/faros/blob/main/pkg/hub/server.go) loops over `providers.AllBuiltins()`
  and mounts each builtin's VW handler at its prefix.
- **Handler** — [`providers/mcp/virtual/builder.go`](https://github.com/faroshq/faros/blob/main/providers/mcp/virtual/builder.go) `Build()` parses
  `/{cluster}/apis/faros.sh/v1alpha1/mcpservers/{name}/mcp`, reads the
  `MCPServer` CR (for the edge selector + toolset config), then composes an
  aggregate `mcp.Server`.

The server is built **fresh per request** (stateless), so every `tools/list`
reflects the current edge inventory and the live readiness of every provider.

`MCPServer.status.URL` carries this endpoint URL for a given server, and the
portal renders the connect/setup command for it — see
[Per-MCPServer credentials](#authentication--identity).

## Source 1 — in-binary tool families (edges)

Edge providers contribute their tools by registering a `ToolFamily` at package
`init()`:

- The registry is **in-process and `init()`-only** —
  [`providers/mcp/aggregate/registry.go`](https://github.com/faroshq/faros/blob/main/providers/mcp/aggregate/registry.go) (`RegisterToolFamily`,
  `RegisteredFamilies`). A `ToolFamily` has a `Name`, an `EdgeType`, and a
  `Register(srv, familyCtx)` callback invoked once per request.
- **Kubernetes edges** — [`providers/kubernetesedges/mcp/family.go`](https://github.com/faroshq/faros/blob/main/providers/kubernetesedges/mcp/family.go)
  registers `{Name: "kubernetes", EdgeType: "kubernetes"}`. The family is wired
  in via a side-effect import in
  [`providers/kubernetesedges/manifest.go`](https://github.com/faroshq/faros/blob/main/providers/kubernetesedges/manifest.go).
- **Server (Linux) edges** — [`providers/serveredges/mcp/family.go`](https://github.com/faroshq/faros/blob/main/providers/serveredges/mcp/family.go)
  registers `{Name: "linux", EdgeType: "server"}`.

At request time, [`providers/mcp/aggregate/aggregatemcp.go`](https://github.com/faroshq/faros/blob/main/providers/mcp/aggregate/aggregatemcp.go) `newServer`
iterates `RegisteredFamilies()` and calls each `Register(...)`, filtering edges
by `EdgeType` against the `MCPServer`'s selector. An edge tool call is proxied
to the actual edge over its **agent-proxy / tunnel** connection (tracked in the
hub's connection manager), not over plain HTTP.

> Because the registry is `init()`-only, an out-of-process provider **cannot**
> register an in-binary family. Out-of-process integrations use federation
> (Source 2).

## Source 2 — out-of-process provider federation

Providers that run as their own process (own binary, own `/mcp` HTTP handler)
are folded into the same aggregate over HTTP.

**Discovery.** Providers are registered via a `ProviderCatalogEntry` and kept
in an in-memory registry with a `BackendURL` and a heartbeat
([`pkg/hub/providers/registry.go`](https://github.com/faroshq/faros/blob/main/pkg/hub/providers/registry.go)). `Provider.Ready()` requires
valid endpoints and a fresh heartbeat (TTL ~90s).

**Enumeration.** The hub wires `mcpaggregate.RegistryEnumerator`
([`pkg/hub/mcpaggregate/enumerator.go`](https://github.com/faroshq/faros/blob/main/pkg/hub/mcpaggregate/enumerator.go)) into the aggregate. It is
called with the **verified caller** — the Org, Workspace and user (or
ServiceAccount) the bearer verifier resolved from the cluster in the request
path, never anything from request headers — and lists
`Registry.ListForOrg(caller's Org)`: every platform provider plus that Org's
own. A platform provider's MCP URL is `BackendURL + "/mcp"`. An org-owned
provider's is reached over its edge route (below). Targets are sorted by name,
so the aggregate's tool list is stable.

**Federation.** Per request,
[`providers/mcp/aggregate/provider_proxy.go`](https://github.com/faroshq/faros/blob/main/providers/mcp/aggregate/provider_proxy.go) `registerProviderTools`:

1. enumerates Ready providers,
2. `POST`s `tools/list` to each `{BackendURL}/mcp`,
3. registers every returned tool on the aggregate as **`<provider>__<tool>`**
   (e.g. `infrastructure__provision`), proxying `tools/call` straight through.

A provider that fails `tools/list`, or a tool whose schema fails `AddTool`, is
**logged and skipped** — one bad provider never poisons the aggregate.

The provider's own MCP handler — e.g.
[`providers/infrastructure/mcpserver/server.go`](https://github.com/faroshq/faros/blob/main/providers/infrastructure/mcpserver/server.go) — is an ordinary
streamable-HTTP MCP server built fresh per request.

### Org-owned (bring-your-own) providers

An organization can run its own copy of a provider in its own cluster
([byo-providers.md](./byo-providers.md)) — including one with a platform
provider's name, e.g. a self-hosted `infrastructure`. The aggregate federates
those too, under three rules:

1. **Tenant scoping.** Only the caller's own Org's providers are listed. The
   Org comes from the verifier (the cluster's `kcp.io/path`, checked against the
   user's Membership or the ServiceAccount's TokenReview in that cluster), so a
   caller cannot claim another Org, and another Org's tools never appear or
   receive a request.
2. **Shadowing.** An Org's provider replaces the platform provider of the same
   name for that Org, exactly as `/services/providers/{name}` does. At most one
   `<name>__*` tool set is registered. If the Org's copy cannot be federated
   for a request (rule 3), the platform copy does **not** come back in its
   place — the Org replaced it, so its tools would act on the wrong backend.
3. **No bearer crosses.** An org-owned provider's `BackendURL` names an address
   inside the tenant's cluster; the hub never dials it. Its `/mcp` is reached
   through the platform `edges` provider's tunnel via the hub-owned Service
   (`providers.ProviderProxy.OrgProviderRoute` — the same edge hop and the same
   delegated-token swap the backend proxy uses for
   `/services/providers/{name}`). The request carries a **delegated user
   token** — a ten-minute ServiceAccount token minted in the caller's team
   workspace for (user, provider), `faros-du-<hash>` — and `X-Faros-User`
   naming the human. The federation client does not even attach the caller's
   bearer to such a request, and the transport refuses to send the delegated
   token anywhere but that provider's edge route. When no delegated token can
   be minted, the provider is **skipped for that request** (logged at V(1)):
   - a **ServiceAccount** bearer (the MCPServer's own token from the portal
     connect snippet, App Studio project identities, other workloads) has no
     human to delegate for;
   - an **org-scope** cluster (the Org workspace itself, no team workspace)
     has nowhere to mint the account;
   - no issuer wired, mint failure, or an unusable edge route.

   It is never reached with the caller's bearer as a fallback.

Why this is safe: the tuple a delegated token is minted from — Org, Workspace,
user — is exactly what the verifier proved (membership in that workspace), the
provider is from that Org's own catalog, and the token is scoped by kcp to that
one workspace and expires in ten minutes. The worst a tenant-run provider can do
with it is what the user could already do in that workspace with `kubectl`,
which is the same bound the backend proxy already accepts for org-owned
providers.

The MCPServer status controller enumerates as the server's own ServiceAccount in
the server's tenant: its `status.federatedProviders` reflects the Org's
shadowing and, for the reason above, lists no org-owned providers.

## Authentication & identity

This is the part future integrations most need to get right.

For a **platform** provider the federation client is created with the
**caller's** credentials, not the hub's or the provider's (org-owned providers
get a delegated token instead — see
[above](#org-owned-bring-your-own-providers)):

```go
// providers/mcp/aggregate/provider_proxy.go
cli := newProviderMCPClient(cfg.BearerToken, cfg.Cluster)
//                          └ caller's token   └ tenant workspace (→ X-Faros-Tenant)
```

- `cfg.BearerToken` is the token the client authenticated the **aggregate**
  request with (`builder.ExtractBearerToken(r)`).
- `cfg.Cluster` is the tenant workspace parsed off the MCPServer URL, forwarded
  as the `X-Faros-Tenant` header on every federated call.

So the identity flows end-to-end:

```
AI client ──Bearer T──▶ hub aggregate VW              (T = the MCPServer's SA token)
                          │ build one mcp.Server (stateless, per request)
                          ├─ in-binary families ─────▶ edges (agent-proxy / tunnel)
                          └─ federation (platform provider): POST {provider BackendURL}/mcp
                               Authorization: Bearer T
                               X-Faros-Tenant: {cluster}
                             (org-owned provider: POST via edges tunnel,
                               Authorization: Bearer <delegated token>, never T)
                                    │
                                    ▼
                        out-of-process provider (own /mcp)
                          identity = { tenant: X-Faros-Tenant, token: Bearer T }
                          tenant client uses T, scoped to {cluster}
                          → acts AS the caller, authorized by the caller's RBAC
```

**The hub verifies the bearer before federating.** The aggregate handler
(`pkg/hub/mcpaggregate`) never forwards an unverified token. Before it builds
the per-request server it runs a `TokenReview` in the tenant cluster named by
the URL and requires the reviewed identity to be that MCPServer's own
ServiceAccount (`system:serviceaccount:default:{name}-mcp`, the account the
`MCPServer` controller provisions). Other authenticated tenant ServiceAccounts
must pass a `SubjectAccessReview` in that same workspace for verb `use` on
`faros.sh/mcpservers`, restricted to the requested server name (cluster-scoped,
no namespace). The review uses the identity, groups, UID and extras returned by
TokenReview. Access is denied unless explicitly allowed; review failures fail
closed before any federation. The original caller bearer is still forwarded to
platform providers, so this permission grants endpoint admission, not
downstream provider rights.
New App Studio project identities receive `use` on `mcpservers/default`; existing
identity roles are not migrated by this change.

A hub user bearer (static token or OIDC,
as used by `faros mcp` and the e2e suites) is accepted instead when the hub's
normal identity path resolves it and the user holds a live Membership covering
the cluster's Organization or Workspace per the `UserMembershipIndex`. Anything
else is answered with `401` (unrecognised) or `403` (valid, but for another
tenant or MCPServer) and no provider is contacted. A successful verification
also yields the caller's tenant — the Org and Workspace from the cluster's
`kcp.io/path` (resolved for ServiceAccount bearers too, only after TokenReview
passes; a lookup outage is a 503, not a guess) plus the user name for a human
bearer — which is what provider enumeration is scoped to. Successful
verifications, with that tenant, are cached by `sha256(token)+cluster+name` for
60 seconds (including workload authorization and membership, so revocation
takes up to 60 seconds), and uncached
attempts are rate-limited per client address with the same limiter that
protects `/api/auth/token-login`, so the endpoint cannot be used as a token
oracle against providers.

Two consequences:

- **Per-MCPServer credentials.** The bearer token a client uses is a per-server,
  long-lived (legacy) ServiceAccount token, published by reference on
  `MCPServer.status.tokenSecretRef` (the token itself never lands in the CR; the
  portal reads the Secret to render the connect command). A user OIDC token
  would expire and silently break a long-lived MCP connection — see the
  `MCPServer` controller in [`pkg/hub/controllers/mcpserver/`](https://github.com/faroshq/faros/blob/main/pkg/hub/controllers/mcpserver/).
- **Scoped token permissions.** The ServiceAccount is bound to a generated
  ClusterRole `faros:mcpserver:<name>` in the tenant workspace, never to
  `cluster-admin`. The controller regenerates the role on every reconcile
  (including the 60s tools refresh) from the tenant's `APIBindings`: each
  `status.boundResources[]` group/resource gets `get,list,watch` plus
  `create,update,patch,delete`; `spec.readOnly` drops the write verbs. On top
  of that it grants the RBAC coordinates provider data planes check via
  SubjectAccessReview as the caller — verb `proxy` on `edges.faros.sh` objects
  (tunnel `k8s`/`ssh`/`mcp` subresources, kept for readOnly servers because
  read-only tools cannot reach an edge without it), `create` on
  `infrastructure.faros.sh` `<instance>/exec` (dropped for readOnly), and
  `create` on `<resource>/<action>` for every action declared in the platform
  provider catalog whose resource is bound (read-only actions survive
  readOnly) — plus read-only `core.kcp.io/logicalclusters` and
  `selfsubjectaccessreviews` create. Nothing grants secrets, service accounts,
  RBAC, or APIBinding access, so a leaked token cannot escalate.
- **No provider-wide identity.** A federated provider must perform its tenant
  work as the forwarded caller token, scoped to the workspace from
  `X-Faros-Tenant`. The infrastructure provider does this in
  [`providers/infrastructure/tenant/client.go`](https://github.com/faroshq/faros/blob/main/providers/infrastructure/tenant/client.go): the tenant client is
  built per-(tenant, caller) from the request token; the provider's own
  credentials are never used for tenant work.
- **Federation routes to published endpoints, not backends.** The aggregator
  reaches each platform provider through its registered `BackendURL`/`/mcp`
  surface, and each org-owned provider through its hub-recorded edge route,
  with the caller's identity forwarded — never into the provider's runtime
  cluster, DB, or internal Services. This is the cross-provider half of the
  platform [provider-isolation rule](./providers.md#provider-isolation-the-cross-provider-boundary).

## Adding a new integration

### A) As an in-binary edge tool family

Use this when your tools ship inside the hub binary and target connected edges.

1. Implement a `ToolFamily` and register it at `init()`:
   ```go
   func init() {
       aggregatemcp.RegisterToolFamily(aggregatemcp.ToolFamily{
           Name:     "myfamily",
           EdgeType: "myedgetype",
           Register: registerMyTools, // wire mcp tools onto the per-request srv
       })
   }
   ```
2. Ensure the package is imported for its side effect from your provider's
   `manifest.go` (mirror `providers/kubernetesedges/manifest.go`).
3. Resolve your edges from the `FamilyContext` and proxy calls over the
   agent-proxy/tunnel.

### B) As an out-of-process provider

Use this when your integration runs as its own process/binary.

1. Serve a streamable-HTTP MCP handler at **`/mcp`** on your backend
   (mirror `providers/infrastructure/mcpserver/`).
2. Register a `ProviderCatalogEntry` and **heartbeat** so the hub marks you
   `Ready` with a reachable `BackendURL`. The aggregate fetches `{BackendURL}/mcp`.
3. **Honour the forwarded identity.** Read the caller from each request:
   `X-Faros-Tenant` for the tenant workspace and `Authorization: Bearer <token>`
   for the credential (see `providers/infrastructure/mcpserver/context.go`).
   Do all tenant work **as that token**, scoped to that workspace — never with a
   provider-wide service account.
4. Your tools appear in the aggregate as `<your-provider>__<tool>` automatically.

Either way, tools surface on the **one** aggregate endpoint; clients don't add
each provider separately.

## Request lifecycle

1. Client opens the aggregate URL with `Authorization: Bearer <token>`.
2. Hub routes to `mcpvirtual.Build` → `aggregatemcp.Handler`.
3. A fresh `mcp.Server` is built:
   - each registered `ToolFamily.Register` runs (edges, filtered by selector),
   - `list_targets` + the `faros://about` resource are added,
   - `registerProviderTools` enumerates Ready providers and federates their
     `/mcp` tools as `<provider>__<tool>`.
4. The composed server answers `tools/list` / `tools/call`.
5. Federated `tools/call` is forwarded to the provider's `/mcp` with
   `X-Faros-Tenant` and — for a platform provider — the caller's bearer, or —
   for an org-owned provider — over the edge tunnel with a delegated token.

## Resilience notes

- **Stateless per request** — readiness/inventory is always current; nothing is
  cached across requests.
- **Fault isolation** — a provider failing `tools/list`, or a single tool
  failing schema validation, is logged and skipped; `AddTool` panics are
  recovered.
- **Naming collisions** — provider tools register *after* in-binary tools, so a
  provider shipping a tool named like a platform tool surfaces as an `AddTool`
  duplicate error (logged), never a silent override.

## Key files

| Concern | File |
| --- | --- |
| Aggregate VW handler | `providers/mcp/virtual/builder.go` |
| Built-in registration / mount prefix | `providers/mcp/manifest.go`, `pkg/apiurl/urls.go` |
| Hub mounting | `pkg/hub/server.go` |
| Tenant-scoped provider enumerator | `pkg/hub/mcpaggregate/enumerator.go` |
| Bearer verification + verified caller | `pkg/hub/mcpaggregate/verifier.go` |
| Org-owned provider route (edge hop + delegated token) | `pkg/hub/providers/org_provider_route.go`, `pkg/hub/providers/proxy_edge.go` |
| In-binary family registry | `providers/mcp/aggregate/registry.go` |
| Aggregate composition | `providers/mcp/aggregate/aggregatemcp.go` |
| Out-of-process federation | `providers/mcp/aggregate/provider_proxy.go` |
| Edge families | `providers/kubernetesedges/mcp/family.go`, `providers/serveredges/mcp/family.go` |
| Provider registry / readiness | `pkg/hub/providers/registry.go` |
| Backend proxy (header/token forwarding) | `pkg/hub/providers/proxy.go` |
| Example out-of-process provider MCP | `providers/infrastructure/mcpserver/` |
| Caller-scoped tenant client | `providers/infrastructure/tenant/client.go` |
| Per-MCPServer SA token + scoped role | `pkg/hub/controllers/mcpserver/` (`rbac.go`), `apis/faros/v1alpha1/types_mcpserver.go` (`status.tokenSecretRef`) |
