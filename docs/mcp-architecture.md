---
layout: default
title: MCP Architecture
nav_order: 7
description: "How kedge aggregates Model Context Protocol (MCP) tools from in-binary edges and out-of-process providers into one endpoint"
---

# MCP Architecture
{: .no_toc }

How kedge exposes a single Model Context Protocol (MCP) endpoint that federates
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
https://<hub>/services/mcpserver/{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp
```

That single endpoint is filled, **per request**, from two sources:

1. **In-binary tool families** — edge tool sets (`kubernetes`, `linux`) compiled
   into the hub and registered at `init()` via `aggregate.RegisterToolFamily`.
2. **Out-of-process provider federation** — every *Ready* provider (e.g. the
   infrastructure provider, which runs as its own process) has its `/mcp`
   endpoint fetched over HTTP and its tools re-exposed as `<provider>__<tool>`.

The caller's bearer token and tenant are forwarded all the way through, so
every tool runs **as the caller**, authorized by the caller's RBAC in the
tenant workspace. There is no provider-wide identity.

---

## The aggregate endpoint

The MCP surface is registered as a kedge *built-in provider* and mounted by the
hub as a virtual workspace.

- **Registration** — [`providers/mcp/manifest.go`](https://github.com/faroshq/kedge/blob/main/providers/mcp/manifest.go) calls
  `providers.RegisterBuiltin(...)` with
  `VirtualWorkspaceMount = apiurl.PathPrefixMCPServer` (`/services/mcpserver`)
  and `VirtualWorkspaceHandler = mcpvirtual.Build`.
- **Mounting** — [`pkg/hub/server.go`](https://github.com/faroshq/kedge/blob/main/pkg/hub/server.go) loops over `providers.AllBuiltins()`
  and mounts each builtin's VW handler at its prefix.
- **Handler** — [`providers/mcp/virtual/builder.go`](https://github.com/faroshq/kedge/blob/main/providers/mcp/virtual/builder.go) `Build()` parses
  `/{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp`, reads the
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
  [`providers/mcp/aggregate/registry.go`](https://github.com/faroshq/kedge/blob/main/providers/mcp/aggregate/registry.go) (`RegisterToolFamily`,
  `RegisteredFamilies`). A `ToolFamily` has a `Name`, an `EdgeType`, and a
  `Register(srv, familyCtx)` callback invoked once per request.
- **Kubernetes edges** — [`providers/kubernetesedges/mcp/family.go`](https://github.com/faroshq/kedge/blob/main/providers/kubernetesedges/mcp/family.go)
  registers `{Name: "kubernetes", EdgeType: "kubernetes"}`. The family is wired
  in via a side-effect import in
  [`providers/kubernetesedges/manifest.go`](https://github.com/faroshq/kedge/blob/main/providers/kubernetesedges/manifest.go).
- **Server (Linux) edges** — [`providers/serveredges/mcp/family.go`](https://github.com/faroshq/kedge/blob/main/providers/serveredges/mcp/family.go)
  registers `{Name: "linux", EdgeType: "server"}`.

At request time, [`providers/mcp/aggregate/aggregatemcp.go`](https://github.com/faroshq/kedge/blob/main/providers/mcp/aggregate/aggregatemcp.go) `newServer`
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
([`pkg/hub/providers/registry.go`](https://github.com/faroshq/kedge/blob/main/pkg/hub/providers/registry.go)). `Provider.Ready()` requires
valid endpoints and a fresh heartbeat (TTL ~90s).

**Enumeration.** The hub wires a `ProviderEnumerator` into the aggregate
([`pkg/hub/server.go`](https://github.com/faroshq/kedge/blob/main/pkg/hub/server.go) `SetProviderEnumerator`) that returns each
Ready provider's MCP URL as `BackendURL + "/mcp"`.

**Federation.** Per request,
[`providers/mcp/aggregate/provider_proxy.go`](https://github.com/faroshq/kedge/blob/main/providers/mcp/aggregate/provider_proxy.go) `registerProviderTools`:

1. enumerates Ready providers,
2. `POST`s `tools/list` to each `{BackendURL}/mcp`,
3. registers every returned tool on the aggregate as **`<provider>__<tool>`**
   (e.g. `infrastructure__provision`), proxying `tools/call` straight through.

A provider that fails `tools/list`, or a tool whose schema fails `AddTool`, is
**logged and skipped** — one bad provider never poisons the aggregate.

The provider's own MCP handler — e.g.
[`providers/infrastructure/mcpserver/server.go`](https://github.com/faroshq/kedge/blob/main/providers/infrastructure/mcpserver/server.go) — is an ordinary
streamable-HTTP MCP server built fresh per request.

## Authentication & identity

This is the part future integrations most need to get right.

The federation client is created with the **caller's** credentials, not the
hub's or the provider's:

```go
// providers/mcp/aggregate/provider_proxy.go
cli := newProviderMCPClient(cfg.BearerToken, cfg.Cluster)
//                          └ caller's token   └ tenant workspace (→ X-Kedge-Tenant)
```

- `cfg.BearerToken` is the token the client authenticated the **aggregate**
  request with (`builder.ExtractBearerToken(r)`).
- `cfg.Cluster` is the tenant workspace parsed off the MCPServer URL, forwarded
  as the `X-Kedge-Tenant` header on every federated call.

So the identity flows end-to-end:

```
AI client ──Bearer T──▶ hub aggregate VW              (T = the MCPServer's SA token)
                          │ build one mcp.Server (stateless, per request)
                          ├─ in-binary families ─────▶ edges (agent-proxy / tunnel)
                          └─ federation: POST {provider BackendURL}/mcp
                               Authorization: Bearer T
                               X-Kedge-Tenant: {cluster}
                                    │
                                    ▼
                        out-of-process provider (own /mcp)
                          identity = { tenant: X-Kedge-Tenant, token: Bearer T }
                          tenant client uses T, scoped to {cluster}
                          → acts AS the caller, authorized by the caller's RBAC
```

Two consequences:

- **Per-MCPServer credentials.** The bearer token a client uses is a per-server,
  long-lived (legacy) ServiceAccount token, published by reference on
  `MCPServer.status.tokenSecretRef` (the token itself never lands in the CR; the
  portal reads the Secret to render the connect command). A user OIDC token
  would expire and silently break a long-lived MCP connection — see the
  `MCPServer` controller in [`providers/mcp/controllers/`](https://github.com/faroshq/kedge/blob/main/providers/mcp/controllers/).
- **No provider-wide identity.** A federated provider must perform its tenant
  work as the forwarded caller token, scoped to the workspace from
  `X-Kedge-Tenant`. The infrastructure provider does this in
  [`providers/infrastructure/tenant/client.go`](https://github.com/faroshq/kedge/blob/main/providers/infrastructure/tenant/client.go): the tenant client is
  built per-(tenant, caller) from the request token; the provider's own
  credentials are never used for tenant work.
- **Federation routes to published endpoints, not backends.** The aggregator
  reaches each provider through its registered `BackendURL`/`/mcp` surface
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
   `X-Kedge-Tenant` for the tenant workspace and `Authorization: Bearer <token>`
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
   - `list_targets` + the `kedge://about` resource are added,
   - `registerProviderTools` enumerates Ready providers and federates their
     `/mcp` tools as `<provider>__<tool>`.
4. The composed server answers `tools/list` / `tools/call`.
5. Federated `tools/call` is forwarded to the provider's `/mcp` with the
   caller's bearer token + `X-Kedge-Tenant`.

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
| Hub mounting + provider enumerator | `pkg/hub/server.go` |
| In-binary family registry | `providers/mcp/aggregate/registry.go` |
| Aggregate composition | `providers/mcp/aggregate/aggregatemcp.go` |
| Out-of-process federation | `providers/mcp/aggregate/provider_proxy.go` |
| Edge families | `providers/kubernetesedges/mcp/family.go`, `providers/serveredges/mcp/family.go` |
| Provider registry / readiness | `pkg/hub/providers/registry.go` |
| Backend proxy (header/token forwarding) | `pkg/hub/providers/proxy.go` |
| Example out-of-process provider MCP | `providers/infrastructure/mcpserver/` |
| Caller-scoped tenant client | `providers/infrastructure/tenant/client.go` |
| Per-MCPServer SA token | `providers/mcp/controllers/`, `apis/kedge/v1alpha1/types_mcpserver.go` (`status.tokenSecretRef`) |
