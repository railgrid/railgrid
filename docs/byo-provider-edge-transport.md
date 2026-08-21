# Edge-fronted BYO providers

**Status:** Design + phase 1 (fail-fast gating) + phase 2 (transport) implemented
**Reads as a delta on:** [byo-providers.md](./byo-providers.md), [edges-marketplace.md](./edges-marketplace.md), [platform-internal-networking.md](./platform-internal-networking.md), [provider-connectivity-contract.md](./provider-connectivity-contract.md)

[byo-providers.md](./byo-providers.md) gets an org-owned provider *registered*:
a workspace, an APIExport, a CatalogEntry, and tenant Workspaces that can Enable
it. What it does not get is a **backend the hub can reach**. This doc closes
that, by making the edge tunnel the transport for org-owned provider backends
and by refusing the install up front when no edge is there to carry it.

## The problem

A provider has two halves. The control half — the provider watching tenant
objects through its APIExport virtual workspace — is **outbound**: the provider
dials the hub. A self-hosted provider behind NAT does that fine.

The data half is not. `/services/providers/{name}/**` is the hub reverse-proxying
to `CatalogEntry.spec.backend.url`, and the hub **dials out** to that URL
([pkg/hub/providers/proxy.go](../pkg/hub/providers/proxy.go)). Every provider in
the tree registers a `.svc.cluster.local` name, which resolves only inside the
platform cluster. [platform-internal-networking.md](./platform-internal-networking.md)
states the asymmetry plainly: *provider→hub is configurable; hub→provider is
not.*

So a tenant self-hosting the infrastructure provider gets a provider that
reconciles Instances correctly and cannot serve a single data-plane call. App
Studio's file sync, log streaming, and exec all ride
`/services/providers/infrastructure/dataplane/…`
([providers/app-studio/api/dataplane_client.go](../providers/app-studio/api/dataplane_client.go)),
and all of them dead-end.

Two non-answers, for the record:

- **Public ingress on the tenant's side.** The self-hoster exposes the provider
  backend and registers a public URL. This makes the hub dial a
  tenant-controlled host *with the calling user's bearer token attached* — a
  confused deputy pointed at an address the tenant chooses. `ParseURL`
  ([registry.go](../pkg/hub/providers/registry.go)) enforces only scheme+host,
  so there is no allowlist standing between that URL and the hub's egress.
- **The virtual-workspace relay.** `5e5db7c3` added
  [pkg/server/proxy/virtualworkspace.go](../pkg/server/proxy/virtualworkspace.go)
  and `c496f4f8` established there is no way to advertise it. Either way it is
  the *control* half. It does nothing for backend reachability.

## Why the edge is the right transport

Two things make this much smaller than it sounds.

**Only one hop needs the tunnel.** A self-hosted infrastructure provider runs in
the same cluster as the workloads it manages, so provider→workload
(`services/proxy` against its own runtime kubeconfig) is already local and
unchanged. The tunnel is needed for hub→provider-backend and nothing else.

**The mechanism already exists and is in production.** `edges.faros.sh/Service`
with `spec.edgeRef.kind: KubernetesCluster` and a `spec.targetRef` is exactly
"reverse-proxy HTTP to a Kubernetes Service inside a tenant cluster over the
agent's tunnel." The type comment on `KubeServiceRef`
([types_service.go](../providers/edges/apis/v1alpha1/types_service.go)) says it:

> The agent dials it over cluster DNS (`{name}.{namespace}.svc`); **the
> Kubernetes API server is not in the data path**, so the provider-injected
> Authorization header reaches the service untouched.

The agent side ([pkg/agent/tunnel/svc.go](../pkg/agent/tunnel/svc.go)) already
permits cluster-DNS targets in kubernetes mode. Multi-replica ownership,
lease-based dialer registry, and peer relay are solved
([connman.go](../providers/edges/internal/tunnel/connman.go),
[registry.go](../providers/edges/internal/tunnel/registry.go)). WebSocket
upgrade is handled.

## Decisions

### E-1 — An org-owned provider backend is reached over an edge, never over a URL

`CatalogEntry.spec.backend.url` stays what it is for platform providers. For an
org-owned provider the hub **ignores** it: whatever the chart wrote there is a
name in the tenant's cluster, meaningful to the tenant and not to the hub.
Routing comes from the edge binding the hub recorded at registration, not from
anything the provider self-declares. This preserves the rule the registry
already enforces for discovery — ownership is never read from a field the
provider controls ([controller.go](../pkg/hub/providers/controller.go)).

### E-2 — Self-hosting requires a connected KubernetesCluster edge, checked before anything is created

Registration takes an explicit target:

```json
{
  "name": "infrastructure",
  "sourceProvider": "infrastructure",
  "edge": { "workspace": "<wsUUID>", "name": "prod-eu" }
}
```

and the hub refuses with `409 Conflict` unless, at that moment:

1. the `edges` provider is enabled in that Workspace (an APIBinding exists), and
2. the named `KubernetesCluster` exists there with `status.connected: true`.

This is a **precondition, not a reconcile**. Registration mints a long-lived
cluster-admin credential and creates a workspace; doing that for an install that
cannot possibly work leaves the Org holding a live credential and a half-built
tree it has to clean up by hand. The same argument the Enable path already makes
for its `edgeProxyAccess` precheck ("refuse up front — before
`EnsureProviderAPIBinding` — so a raced Enable is a clean no-op the client can
retry, instead of leaving the APIBinding created but the edge-proxy grant
missing") applies with more force here, because here the partial state contains
a credential.

Fail-fast is also the only honest UX. The alternative is a registration that
succeeds, hands over Helm commands, and produces a provider stuck at
`Registered: true, Ready: false` with no indication that the missing piece is an
agent in a cluster the user never installed.

### E-3 — The UI never offers self-hosting it knows will fail

`GET /api/orgs/{org}/providers/install-targets` returns the org's connected
edges and, when there are none, *why*. The Self-Hosting tab renders from that:
no eligible edge → the self-host actions are disabled with the reason inline and
a link to the edges onboarding, rather than enabled and 409ing on click.

The endpoint is the same check `registerOrgProvider` runs, so the button state
and the server's answer cannot drift. The server check stays regardless — the
UI gate is ergonomics, not enforcement.

### E-3a — Edge discovery respects the workspace membership boundary

Edges live in team workspaces, and team workspaces have members. Both the
listing and the registration preflight project the Org's edges through the same
rule `listWorkspaces` already uses — org admins see every child workspace,
members see their UMI entries — so a member cannot read the Org's cluster
inventory out of workspaces they do not belong to, nor install a provider into
one.

Naming an invisible workspace is refused with the **same** message as naming a
nonexistent edge. A distinct 403 would turn the endpoint into an oracle for
which workspaces hold which clusters.

### E-4 — The hub owns the `Service` object, the tenant does not

At registration the hub creates
`edges.faros.sh/Service` named `provider-<name>` in the target Workspace,
pointing at `{name}.faros-provider-<name>.svc:<port>`, and marks it hub-owned.
The tenant does not hand-write it and cannot repoint it at something else
without the hub noticing — `spec.host` on a `Service` is otherwise free-form,
and [svc.go](../pkg/agent/tunnel/svc.go)'s `isAllowedSvcHost` currently returns
`true` unconditionally (its own `TODO(security)`). Tenant-authored `spec.host`
aimed at the tenant's own LAN is self-harm; the same field on the path that
carries platform traffic and platform-injected headers is not.

### E-5 — Authorization is passed through, not substituted

`serviceHTTPProxy` today replaces the caller's `Authorization` with the
Service's own token from `spec.authSecretRef`, or deletes it
([service_proxy.go](../providers/edges/internal/tunnel/service_proxy.go)). That
is right for a Home Assistant box with one long-lived token. It is wrong for a
provider backend: the infrastructure data plane's entire authorization model is
the *caller's* bearer — it 401s without one and gates every request on a
tenant-scoped `GET` of the Instance as that caller
([dataplane/handler.go](../providers/infrastructure/dataplane/handler.go)).
Substituting one shared token would collapse per-user RBAC into "anyone who can
reach the tunnel."

So `Service` gains `spec.auth`:

| value | behaviour |
|---|---|
| `secret` (default) | today's behaviour — replace with `authSecretRef`'s token |
| `passthrough` | forward the caller's `Authorization` untouched |
| `none` | strip it |

`passthrough` requires no `authSecretRef` and is what hub-owned provider
Services use. Keeping the default as `secret` means no existing Service changes
meaning.

### E-6 — The caller's kcp identity must survive the hop

The hub backend proxy strips inbound `X-Faros-User` / `X-Faros-Tenant` /
`X-Faros-Cluster` and re-injects them from its own resolvers
([proxy.go](../pkg/hub/providers/proxy.go)) — that is the identity the provider
trusts. The agent's `/svc` handler deletes only `X-Faros-Svc-Target`, so those
headers already survive. This is a property to test, not code to write; it is
listed because breaking it silently downgrades the provider's notion of who is
calling.

### E-7 — Streaming is not optional on this path

The dataplane `log` verb is `stream: true` and the hub sets `FlushInterval: -1`
precisely so tailing works. The edges Service proxy builds a plain
`httputil.ReverseProxy` with no flush interval, so responses buffer. Log tailing
through an edge would appear to hang. Same for SSE and for the
`aggregatingcrdversiondiscovery`-style long polls.

## The request path

```
browser (user token)
  → hub /services/providers/app-studio/api/…            (injects X-Faros-{User,Tenant,Cluster})
  → app-studio                                          (holds no runtime credential)
  → hub /services/providers/{org-infra}/dataplane/…
      │ registry resolves org-owned → edge-fronted transport
      ▼
    edges provider /edgeproxy/clusters/{ws}/apis/edges.faros.sh/v1alpha1/
                   services/provider-{name}/proxy/dataplane/…
      │ SAR: proxy on services/provider-{name}; Service is hub-owned; auth=passthrough
      ▼
    revdial WebSocket ──▶ agent in tenant cluster
      │ X-Faros-Svc-Target: http://{name}.faros-provider-{name}.svc:8081
      ▼
    infrastructure provider (tenant cluster)
      │ RBAC gate = GET the Instance as the caller
      ▼
    runtime kube-apiserver services/proxy   ← local, unchanged
      ▼
    faros-dev-agent in the workload pod
```

Compared to a platform provider the added hops are hub→edges-provider (in
cluster) and one revdial stream. Everything from the infrastructure provider
rightward is byte-identical to today.

## What changes where

| # | Change | Location |
|---|---|---|
| 1 | `Service.spec.auth` (`secret`/`passthrough`/`none`) | `providers/edges/apis/v1alpha1/types_service.go` |
| 2 | Honour `auth` in the proxy; `FlushInterval: -1`; close the tunnel conn | `providers/edges/internal/tunnel/service_proxy.go` |
| 3 | Edge install-target discovery (`edges` bound + `status.connected`) | `pkg/hub/kcp/edgetargets.go` |
| 4 | `GET .../providers/install-targets`; `edge` on the register body; preflight | `pkg/hub/restapi/org_providers.go` |
| 5 | Gate the Self-Hosting UI on target availability | `portal/src/stores/orgProviders.ts`, `portal/src/pages/ProvidersPage.vue` |
| 6 | Hub-owned `Service` creation at registration | `pkg/hub/kcp/edgetargets.go` |
| 7 | Edge-fronted transport in the backend proxy | `pkg/hub/providers/{registry,proxy,controller}.go` |
| 8 | Org-scoped resolution on the backend-proxy path | `pkg/hub/providers/proxy.go` |
| 9 | Binding-driven provider selection (drop the hardcoded `infrastructure`) | `providers/app-studio/api/dataplane_client.go` |

Items 1–5 were phase 1: the transport contract and the fail-fast behaviour.
6–8 are phase 2 and are now implemented; the data plane of an org-owned
provider routes over its edge.

Item 9 turned out to be subsumed by 8 for the flow the product actually
offers. App Studio addresses `/services/providers/infrastructure/...`, and the
backend proxy now resolves that name in the CALLER's Org — so an Org running
its own copy is reached without App Studio knowing anything about bindings.
The hardcoded name only matters if an Org registers a provider under a name
other than the platform provider's, which the Self-Hosting UI never does
(`register(p.name, p.name, edge)`). Binding-driven selection is still the
right answer for that case and remains open.

Two deviations from the design as written, both forced by what the hub can
actually know:

- **The Service target is read from the provider's own CatalogEntry, not
  derived.** E-4 assumed `{name}.faros-provider-{name}.svc`, but the address
  is the chart's to choose — an operator-mode install lands on
  `<release>-<chart>.<serve-ns>.svc`, which the hub cannot predict. The hub
  therefore reads `spec.backend.url` but does not trust it: it must parse as
  `<service>.<namespace>.svc[.cluster.local]`, so the worst a tenant can aim
  it at is something inside the cluster the tunnel already terminates in. An
  IP, an external host, or a bare name is refused.
- **The Service is reconciled by the catalog controller, not created at
  registration.** It cannot exist earlier: the address only appears once the
  chart has run and published its CatalogEntry. Registration records the edge
  binding; the controller reconciles the Service when the provider first
  reports itself.

## Consequences worth accepting deliberately

**The edges provider becomes a hard dependency of self-hosting.** "Bring a
cluster, we reach it" is a coherent product line, and it is strictly better than
asking self-hosters to expose a backend publicly. But an Org that wants a
self-hosted provider must now onboard an edge first, and the edges provider
becomes a availability dependency for every self-hosted provider's data plane.

**Traffic concentrates on edges-provider replicas.** Every byte of file sync and
every log stream for every self-hosted tenant transits them. The lease/relay
design scales horizontally, but this should be sized before phase 2, not after.

**A new RBAC coupling.** Today an App Studio user needs no edges permission. On
this path every workload call SARs `proxy` on `services/provider-<name>` in the
tenant Workspace. Defensible — arguably correct, since it makes "who may reach
the tenant's cluster" one auditable grant — but it is new, and a user removed
from that grant loses their sandbox rather than losing edge access.

## Known gaps

- **`isAllowedSvcHost` returns `true` unconditionally**
  ([svc.go](../pkg/agent/tunnel/svc.go)). E-4 keeps the platform-owned Service
  out of tenant hands, which bounds the new exposure, but the underlying SSRF
  boundary is still open for tenant-authored Services and should be closed with
  a per-agent allowlist.
- **One tunnel dial per request.** `edgeDeviceConnTransport` writes one request
  and reads one response over a fresh `net.Conn`; there is no keep-alive or
  pooling. Fine for SSH and Home Assistant, visible under App Studio file sync.
- **No re-validation after registration.** E-2 checks the edge at registration
  time only. An edge deleted afterwards leaves an org provider whose backend is
  unroutable, reported as not-Ready with no explanation naming the edge.
- **Single target edge.** No failover across two edges, and no way to move a
  provider to a different edge without deregistering.
- **`bind` is still not org-scoped.** Unchanged from
  [byo-providers.md](./byo-providers.md), and still the most important gap in
  this area.
