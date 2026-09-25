---
layout: default
title: MCP Architecture
nav_order: 9
description: "How railgrid aggregates Model Context Protocol (MCP) tools from every provider into one endpoint"
---

# MCP Architecture
{: .no_toc }

How railgrid exposes a single Model Context Protocol (MCP) endpoint that federates
the tools of every **provider** — including the `edges` provider, whose tools
reach connected edges through their tunnels.
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
https://<hub>/services/mcpserver/{cluster}/apis/railgrid.ai/v1alpha1/mcpservers/{name}/mcp
```

That single endpoint is filled, **per request**, from exactly **one** source:
**provider federation**. Every *Ready* provider visible to the verified caller
has its own `/mcp` endpoint fetched over HTTP and its tools re-exposed as
`<provider>__<tool>`.

> **There is no in-binary tool registry any more.** The former `mcp`,
> `kubernetesedges` and `serveredges` built-ins were folded into the standalone
> `edges` provider (#435) and the aggregate moved hub-side to
> `pkg/hub/mcpaggregate/`. Edge tools — `kubernetes_*`, `linux_*`, per-`Service`
> tools — now arrive by federation like everything else, as `edges__*`, from
> `providers/edges/internal/tunnel/mcp_root.go` and `mcp_service.go`. Nothing
> compiles a tool into the hub binary.

Alongside the tools, the aggregate serves a **declaration**: the resource
`railgrid://providers/capabilities` lists what every visible provider says it
can do, straight from the validated catalog — see
[Capability discovery](#capability-discovery-railgridproviderscapabilities).

Every tool runs **as the caller**, authorized by the caller's RBAC in the
tenant workspace. There is no provider-wide identity. Platform providers receive
the caller's own bearer; an organization's own (bring-your-own) providers
receive a short-lived delegated token for the same user and workspace instead,
and never the caller's bearer — see
[Org-owned providers](#org-owned-bring-your-own-providers).

---

## The aggregate endpoint

The aggregate is a plain hub handler, always on — never edge-dependent, and an
empty but valid MCP server when nothing is registered.

- **Mounting** — [`pkg/hub/server.go`](https://github.com/railgrid/railgrid/blob/main/pkg/hub/server.go) builds it with
  `mcpaggregate.New(...)` and mounts it under
  `apiurl.PathPrefixMCPServer` (`/services/mcpserver`), with the prefix
  stripped.
- **Handler** — [`pkg/hub/mcpaggregate/handler.go`](https://github.com/railgrid/railgrid/blob/main/pkg/hub/mcpaggregate/handler.go) sees
  `/{cluster}/apis/railgrid.ai/v1alpha1/mcpservers/{name}/mcp`, verifies the
  bearer against that cluster and MCPServer, then composes an aggregate
  `mcp.Server` for the verified caller.

The server is built **fresh per request** (stateless), so every `tools/list`
reflects the live readiness of every provider. Each provider's discovered tools
and instructions are cached briefly per caller and refreshed in the background
([`discovery.go`](https://github.com/railgrid/railgrid/blob/main/pkg/hub/mcpaggregate/discovery.go)).

`MCPServer.status.URL` carries this endpoint URL for a given server, and the
portal renders the connect/setup command for it — see
[Per-MCPServer credentials](#authentication--identity).

## Edge tools: the `edges` provider, federated like any other

Edge tools are **not** special. The `edges` provider serves its own
streamable-HTTP MCP handler (reached by the hub at
`/services/providers/edges/mcp`), and the aggregate federates it exactly as it
federates `infrastructure` or `code`. Its tools surface as `edges__*`.

- **Per-edge and per-Service tools** are registered by
  [`providers/edges/internal/tunnel/mcp_root.go`](https://github.com/railgrid/railgrid/blob/main/providers/edges/internal/tunnel/mcp_root.go)
  and
  [`mcp_service.go`](https://github.com/railgrid/railgrid/blob/main/providers/edges/internal/tunnel/mcp_service.go),
  fresh per request, from the edges the **caller** can see in the addressed
  workspace.
- **Reaching one edge's own MCP endpoint** is a declared data-plane verb — a
  kcp custom subresource on the hub's kcp front door, not a separate mount:

  ```
  /clusters/{clusterID}/apis/edges.railgrid.ai/v1alpha1/{resource}/{name}/mcp
  ```

  where `{resource}` is `kubernetesclusters` or `services`
  (`providers/edges/internal/tunnel/grammar.go`, `VerbMCP`; declared in
  `providers/edges/manifest.yaml` under `spec.dataPlane.verbs`). It is
  authorized like every other verb: a real `GET` of the object as the caller,
  then a `SelfSubjectAccessReview` for **`create` on `{resource}/mcp`**,
  name-scoped. The old `/agent`-mounted per-edge MCP route and the wildcard
  `proxy` verb that used to gate `k8s`, `ssh`, service proxy and MCP alike are
  gone, not aliased.

The call still crosses the edge's **reverse tunnel**; what changed is that the
tunnel is terminated by the edges provider rather than by the hub binary, and
the coordinate is a contract path rather than a hub-internal mount.

## Provider federation — the one source

Every provider runs as its own process with its own `/mcp` HTTP handler, and is
folded into the aggregate over HTTP.

**Discovery.** Providers are registered via a `ProviderCatalogEntry` and kept
in an in-memory registry with a `BackendURL` and a heartbeat
([`pkg/hub/providers/registry.go`](https://github.com/railgrid/railgrid/blob/main/pkg/hub/providers/registry.go)). `Provider.Ready()` requires
valid endpoints and a fresh heartbeat (TTL ~90s).

**Enumeration.** The hub wires `mcpaggregate.RegistryEnumerator`
([`pkg/hub/mcpaggregate/enumerator.go`](https://github.com/railgrid/railgrid/blob/main/pkg/hub/mcpaggregate/enumerator.go)) into the aggregate. It is
called with the **verified caller** — the Org, Workspace and user (or
ServiceAccount) the bearer verifier resolved from the cluster in the request
path, never anything from request headers — and lists
`Registry.ListForOrg(caller's Org)`: every platform provider plus that Org's
own. A platform provider's MCP URL is `BackendURL + "/mcp"`. An org-owned
provider's is reached over its edge route (below). Targets are sorted by name,
so the aggregate's tool list is stable.

**Federation.** Per request,
[`pkg/hub/mcpaggregate/federation.go`](https://github.com/railgrid/railgrid/blob/main/pkg/hub/mcpaggregate/federation.go):

1. enumerates Ready providers,
2. `POST`s `tools/list` to each `{BackendURL}/mcp`,
3. registers every returned tool on the aggregate as **`<provider>__<tool>`**
   (e.g. `infrastructure__provision`), proxying `tools/call` straight through.

It also merges each provider's server-level MCP `instructions` into the
aggregate's, so operator-authored guidance (a Home Assistant `Service`'s
`spec.instructions`, say) reaches the client. A provider that fails
`tools/list`, or a tool whose schema fails `AddTool`, is **logged and
skipped** — one bad provider never poisons the aggregate.

The provider's own MCP handler — e.g.
[`providers/infrastructure/mcpserver/server.go`](https://github.com/railgrid/railgrid/blob/main/providers/infrastructure/mcpserver/server.go) — is an ordinary
streamable-HTTP MCP server built fresh per request.

## Capability discovery: `railgrid://providers/capabilities`

`tools/list` answers *"what can I call right now"*. It does not answer *"what
does this platform declare it can do"* — that question is about the contract,
and the tool list is a poor proxy for it: a provider that is Ready but slow to
answer `tools/list` contributes nothing, tool names are provider-local prose,
and a data-plane verb (`kubernetesclusters/ssh`) usually has no tool at all.

The second question is answered from the **validated provider registry** — the
same `CatalogEntry` declarations the hub already admits and enforces against.
The aggregate serves them as an MCP resource
([`pkg/hub/mcpaggregate/capabilities.go`](https://github.com/railgrid/railgrid/blob/main/pkg/hub/mcpaggregate/capabilities.go)):

```
railgrid://providers/capabilities      application/json
```

Its scope is exactly federation's scope: one entry per **Ready** provider
**visible to the verified caller's Org**, in the same sorted enumeration order,
projected by `RegistryEnumerator` from `spec.actions` and
`spec.dataPlane.verbs`. A provider that declares neither is omitted rather than
listed empty.

```json
{
  "tenant": "2v9k1q...",
  "mcpServer": "default",
  "providers": [
    {
      "provider": "code",
      "displayName": "Code",
      "actions": [
        {
          "id": "branches/v1",
          "name": "branches",
          "version": "v1",
          "displayName": "List branches",
          "description": "List a bounded page of branch names from a registered repository.",
          "boundResource": {
            "apiVersion": "code.railgrid.ai/v1alpha1",
            "kind": "Repository",
            "resource": "repositories"
          },
          "readOnly": true,
          "risk": "low",
          "consent": { "required": false },
          "schemaDigest": "sha256:9f2c…"
        }
      ],
      "verbs": []
    },
    {
      "provider": "infrastructure",
      "displayName": "Infrastructure",
      "actions": [],
      "verbs": [
        {
          "coordinate": "instances/log",
          "resource": "instances",
          "verb": "log",
          "description": "Stream the instance's logs.",
          "stream": true,
          "readOnly": true
        }
      ]
    }
  ]
}
```

**It is a declaration, not a directory.** No provider URL, no data-plane path,
no credential, no transport handle of any kind crosses this boundary — nor do
the registry's compiled schema validators or execution limits. A coordinate is
an identifier the hub owns; reaching it still goes through the federated tool,
the action transport or the data-plane route, and is still authorized there by
the caller's own RBAC. Publishing a coordinate grants nothing.

A verb carries no schema and no digest, deliberately: a data-plane verb is a
coordinate and a transport, not a request/response contract. That is what an
action is, and an action's `schemaDigest` is what pins it to a contract
version.

The `coordinate` spelling — `<resource>/<verb>` — is not cosmetic. It is the
same string the hub uses for the RBAC subresource and for a scoped-identity
capability, so what a model reads here is literally what an operator would
grant.

### Joining the declaration to the tool list

A model that has read the resource still has to know *which tool implements
which declaration*. Names do not tell it: `code__list_branches` backs
`branches/v1`, and nothing in either string says so.

So a provider names the coordinate on its own tool's `_meta`, as a plain
string:

```json
{ "name": "list_branches", "_meta": { "railgrid": { "action": "branches/v1" } } }
{ "name": "dev_logs",      "_meta": { "railgrid": { "verb":   "instances/log" } } }
```

**That claim is a hint, never trusted as given.** The aggregate resolves it
against the coordinates the hub has *admitted* for that provider, and what it
writes on the proxy tool is the registry's value, not the provider's:

```json
{
  "name": "code__list_branches",
  "_meta": {
    "railgrid": {
      "provider": "code",
      "action": { "id": "branches/v1", "…": "…", "schemaDigest": "sha256:9f2c…" }
    }
  }
}
```

A tool claiming a coordinate its provider does not declare — or another
provider's coordinate, or a malformed claim — gets **no `_meta` at all**. A
provider therefore cannot advertise a contract it never published, borrow a
neighbour's coordinate, or assert a schema digest it did not register. Nothing
else from a provider's `_meta` is forwarded.

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
   workspace for (user, provider), `railgrid-du-<hash>` — and `X-Railgrid-User`
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
// pkg/hub/mcpaggregate/federation.go
cli := newProviderMCPClient(cfg.BearerToken, cfg.Cluster)
//                          └ caller's token   └ tenant cluster ID (→ X-Railgrid-Tenant + X-Railgrid-Cluster)
```

- `cfg.BearerToken` is the token the client authenticated the **aggregate**
  request with, after the verifier accepted it.
- `cfg.Cluster` is the tenant workspace's kcp logical-cluster ID parsed off
  the MCPServer URL, forwarded as both `X-Railgrid-Tenant` and `X-Railgrid-Cluster`
  on every federated call — the same pair the hub backend proxy injects, so a
  provider sees one identity contract whichever way it is reached.

So the identity flows end-to-end:

```
AI client ──Bearer T──▶ hub aggregate endpoint        (T = the MCPServer's SA token)
                          │ verify T against {cluster} + {mcpserver}
                          │ build one mcp.Server (stateless, per request)
                          └─ federation (platform provider): POST {provider BackendURL}/mcp
                               Authorization: Bearer T
                               X-Railgrid-Tenant: {cluster}
                               X-Railgrid-Cluster: {cluster}
                             (org-owned provider: POST via edges tunnel,
                               Authorization: Bearer <delegated token>, never T)
                                    │
                                    ▼
                        out-of-process provider (own /mcp)
                          identity = { cluster: X-Railgrid-Cluster (= X-Railgrid-Tenant), token: Bearer T }
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
`railgrid.ai/mcpservers`, restricted to the requested server name (cluster-scoped,
no namespace). The review uses the identity, groups, UID and extras returned by
TokenReview. Access is denied unless explicitly allowed; review failures fail
closed before any federation. The original caller bearer is still forwarded to
platform providers, so this permission grants endpoint admission, not
downstream provider rights.
New App Studio project identities receive `use` on `mcpservers/default`; existing
identity roles are not migrated by this change.

A hub user bearer (static token or OIDC,
as used by `railgrid mcp` and the e2e suites) is accepted instead when the hub's
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
  `MCPServer` controller in [`pkg/hub/controllers/mcpserver/`](https://github.com/railgrid/railgrid/blob/main/pkg/hub/controllers/mcpserver/).
- **Scoped token permissions.** The ServiceAccount is bound to a generated
  ClusterRole `railgrid:mcpserver:<name>` in the tenant workspace, never to
  `cluster-admin`. The controller regenerates the role on every reconcile
  (including the 60s tools refresh) from the tenant's `APIBindings`: each
  `status.boundResources[]` group/resource gets `get,list,watch` plus
  `create,update,patch,delete`; `spec.readOnly` drops the write verbs. On top
  of that it grants the RBAC coordinates provider data planes check via
  SubjectAccessReview as the caller: verb `proxy` on `edges.railgrid.ai`
  objects, `create` on `infrastructure.railgrid.ai` `<instance>/exec` (dropped
  for readOnly), and `create` on `<resource>/<action>` for every action
  declared in the platform provider catalog whose resource is bound (read-only
  actions survive readOnly) — plus read-only `core.kcp.io/logicalclusters` and
  `selfsubjectaccessreviews` create
  (`pkg/hub/controllers/mcpserver/rbac.go`, `dataPlaneGrants`). Nothing grants
  secrets, service accounts, RBAC, or APIBinding access, so a leaked token
  cannot escalate.

  > **Known gap.** The `edges.railgrid.ai` entry still spells the **wildcard
  > `proxy` verb**, which the edges provider retired: it now gates every
  > data-plane call with `create` on the `{resource}/{verb}` subresource
  > (`kubernetesclusters/k8s`, `kubernetesclusters/mcp`, `services/proxy`,
  > `services/mcp`, …). Until `dataPlaneGrants` emits those subresources, an
  > MCPServer token is denied on the edges data plane and the `proxy` rule it
  > does get authorizes nothing. The coordinates to emit are no longer a
  > guess: they are exactly the `{resource}/{verb}` pairs the edges provider
  > publishes at `railgrid://providers/capabilities` (below). The Enable-time
  > `edges-proxy` ClusterRole that used to carry a parallel copy of this
  > grant was deleted on 2026-09-20 — it granted the *provider's*
  > ServiceAccount, and no provider authenticates that way any more.
- **No provider-wide identity.** A federated provider must perform its tenant
  work as the forwarded caller token, scoped to the workspace whose cluster ID
  is in `X-Railgrid-Cluster` / `X-Railgrid-Tenant`. The infrastructure provider does this in
  [`providers/infrastructure/tenant/client.go`](https://github.com/railgrid/railgrid/blob/main/providers/infrastructure/tenant/client.go): the tenant client is
  built per-(tenant, caller) from the request token; the provider's own
  credentials are never used for tenant work.
- **Federation routes to published endpoints, not backends.** The aggregator
  reaches each platform provider through its registered `BackendURL`/`/mcp`
  surface, and each org-owned provider through its hub-recorded edge route,
  with the caller's identity forwarded — never into the provider's runtime
  cluster, DB, or internal Services. This is the cross-provider half of the
  platform [provider-isolation rule](./providers.md#provider-isolation-the-cross-provider-boundary).

## Adding a new integration

There is one way in: **be a provider**. Tools cannot be compiled into the hub.

1. Serve a streamable-HTTP MCP handler at **`/mcp`** on your backend. Register
   it through `provider-sdk/serve.New(Options{MCP: ...})` — `/mcp` and
   `/mcp/sse` are route classes of the fixed server layout, and `serve.New`
   refuses anything outside it. Mirror `providers/infrastructure/mcpserver/`.
2. Register a `CatalogEntry` and **heartbeat** so the hub marks you `Ready`
   with a reachable `BackendURL`. The aggregate fetches `{BackendURL}/mcp`.
3. **Honour the forwarded identity.** Read the caller from each request:
   `X-Railgrid-Cluster` (the tenant workspace's kcp logical-cluster ID; the
   hub sends the same value as `X-Railgrid-Tenant`) and
   `Authorization: Bearer <token>` for the credential (see
   `providers/infrastructure/mcpserver/context.go`).
   Do all tenant work **as that token**, scoped to that workspace — never with a
   provider-wide service account.
4. **A tool is a projection, not a third access path.** Every tool must wrap a
   read the caller could have done through the bound CR, or a declared verb
   the caller could have invoked as a custom subresource on `/clusters/{id}`
   — run through the same executor and the same authorization as the verb. A
   tool may not hold a credential or reach a coordinate the caller would not
   be granted directly.
5. Your tools appear in the aggregate as `<your-provider>__<tool>`
   automatically, on the **one** aggregate endpoint; clients do not add each
   provider separately.

## Request lifecycle

1. Client opens the aggregate URL with `Authorization: Bearer <token>`.
2. Hub routes to `mcpaggregate.Handler`, which verifies the bearer against the
   cluster and MCPServer named in the path before anything else happens.
3. A fresh `mcp.Server` is built:
   - the `railgrid://about` resource is added,
   - federation enumerates the Ready providers visible to the verified caller
     and registers their `/mcp` tools as `<provider>__<tool>`, merging their
     server-level instructions and stamping each tool's validated
     `_meta.railgrid` coordinate,
   - `railgrid://providers/capabilities` is added from the same enumeration —
     from the registry, so a Ready provider whose `/mcp` is slow or absent
     still has its declared contract published.
4. The composed server answers `tools/list` / `tools/call`.
5. Federated `tools/call` is forwarded to the provider's `/mcp` with
   `X-Railgrid-Tenant` / `X-Railgrid-Cluster` (the cluster ID) and — for a platform
   provider — the caller's bearer, or —
   for an org-owned provider — over the edge tunnel with a delegated token.

## Resilience notes

- **Stateless per request** — readiness is always current. Only a provider's
  discovered tool list and instructions are cached briefly, per caller, with a
  background refresh (`pkg/hub/mcpaggregate/discovery.go`).
- **Fault isolation** — a provider failing `tools/list`, or a single tool
  failing schema validation, is logged and skipped; `AddTool` panics are
  recovered.
- **Naming collisions** — every tool is namespaced `<provider>__<tool>` and
  targets are registered in sorted order, so the tool list is deterministic and
  a duplicate surfaces as a logged `AddTool` error, never a silent override.

## Key files

| Concern | File |
| --- | --- |
| Aggregate handler + mount prefix | `pkg/hub/mcpaggregate/handler.go`, `pkg/apiurl/urls.go` (`PathPrefixMCPServer`) |
| Hub mounting | `pkg/hub/server.go` |
| Tenant-scoped provider enumerator | `pkg/hub/mcpaggregate/enumerator.go` |
| Bearer verification + verified caller | `pkg/hub/mcpaggregate/verifier.go` |
| Federation (tools/list, tools/call, instructions) | `pkg/hub/mcpaggregate/federation.go` |
| Capability resource + tool `_meta` coordinates | `pkg/hub/mcpaggregate/capabilities.go` |
| Per-caller tool/instruction discovery cache | `pkg/hub/mcpaggregate/discovery.go` |
| Org-owned provider route (edge hop + delegated token) | `pkg/hub/providers/org_provider_route.go`, `pkg/hub/providers/proxy_edge.go` |
| Edge tools + per-edge MCP verb | `providers/edges/internal/tunnel/mcp_root.go`, `mcp_service.go`, `grammar.go` |
| Provider registry / readiness | `pkg/hub/providers/registry.go` |
| Backend proxy (header/token forwarding) | `pkg/hub/providers/proxy.go` |
| Example out-of-process provider MCP | `providers/infrastructure/mcpserver/` |
| Caller-scoped tenant client | `providers/infrastructure/tenant/client.go` |
| Per-MCPServer SA token + scoped role | `pkg/hub/controllers/mcpserver/` (`rbac.go`), `apis/railgrid/v1alpha1/types_mcpserver.go` (`status.tokenSecretRef`) |
