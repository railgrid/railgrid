# Decoupling App Studio from the runtime cluster

Status: **Design proposal, not implemented.**
Author: 2026-06-27
Related: [`app-studio-sandbox-runtime.md`](./app-studio-sandbox-runtime.md) (current runtime contract), [`infrastructure-architecture.md`](./infrastructure-architecture.md) (the kcp-native infra provider this builds on), [`provider-connectivity-contract.md`](./provider-connectivity-contract.md) (the two data paths), `providers/infrastructure/install/templates/sandbox-runner.yaml`, `pkg/virtual/builder/edges_proxy_builder.go` (the proven VW-proxy pattern).

## Summary

App Studio today holds a **second kubeconfig** — `APP_STUDIO_RUNTIME_KUBECONFIG` — pointed at the Kubernetes cluster where `SandboxRunner` workloads actually run. It uses that credential directly for the live development data plane: stream logs, push file sync, restart, probe preview readiness, read the per-runner control-token Secret, proxy preview traffic, and delete the runtime namespace on teardown.

That credential is the wrong coupling. It assumes App Studio and the infrastructure provider share one runtime cluster, owned by whoever deploys App Studio. It blocks **BYO compute**: a tenant whose workspace is backed by a *different* infrastructure provider (a different `InfrastructureProvider`, a different APIExport, a different runtime cluster) cannot be served, because App Studio only knows its own runtime kubeconfig.

This document proposes removing App Studio's runtime credential entirely and moving the data plane to where the runtime cluster is already owned — the **infrastructure provider**. The infra provider exposes the data-plane operations as **subresources on the workload instance** (`sandboxrunners/{name}/log`, `…/proxy/{path}`, `…/sync`, `…/restart`), served by a virtual workspace. App Studio calls those subresources as the **tenant user**, over the same authenticated kcp path it already uses to create the `SandboxRunner`. Because the APIBinding routes the call to the provider that backs the workspace, BYO compute falls out for free — App Studio carries no per-provider logic and no runtime credential.

The contract of *which* data-plane verbs exist, and *how* each one resolves to a runtime Service/Secret/port, is declared **per Template** so it generalizes to any infrastructure workload, not just sandbox runners.

## Restore-from-reboot summary

- **Today:** App Studio owns the runtime data plane and holds `APP_STUDIO_RUNTIME_KUBECONFIG`. The infra provider owns resource composition (the `sandbox-runner` Template + kro) and the runtime-cluster client, but exposes **no** data-plane access to the workloads it creates.
- **Proposed:** the infra provider serves data-plane verbs as **VW subresources** on workload instances; App Studio calls them as the tenant user and drops its runtime kubeconfig.
- **Why it's BYO-native:** routing is via APIBinding → APIExport → provider. The provider that backs a workspace serves that workspace's data plane against *its* runtime cluster. App Studio issues the same request regardless.
- **Generality:** verbs + target-resolution are declared in `Template.spec.dataPlane`, resolved by one generic handler. SandboxRunner is the first consumer; a DB shell or app log tail is the next.
- **Proven vehicle:** `edges_proxy_builder.go` already does service-proxy + exec/port-forward upgrades + WebSocket over a VW subresource path. The transport is not novel; only the contract is.
- **Auth:** every data-plane call is authorized by a tenant-scoped `GET` on the instance CR using the caller's forwarded bearer token (kcp RBAC). No provider-wide credential gates the data plane.

## 1. Why this matters

### 1.1 The coupling we want to remove

App Studio's `Server` carries three runtime fields ([`providers/app-studio/api/server.go`](../providers/app-studio/api/server.go)):

```go
runtimeConfig  *rest.Config         // runtime cluster API + TLS
runtimeClient  kubernetes.Interface // Secrets, Endpoints, Namespaces
runtimeDynamic dynamic.Interface    // ReferenceGrants
```

Loaded from `APP_STUDIO_RUNTIME_KUBECONFIG` in `main.go`. Every live-development feature is a *direct* call to the runtime cluster:

| Operation | Today (App Studio → runtime cluster) | Source |
|---|---|---|
| Logs | `GET …/services/{ctrl}:control/proxy/logs` + `X-Sandbox-Control-Token` | `api/development_runtime.go` |
| Restart / sync | `POST …/services/{ctrl}:control/proxy/{restart,sync}` | `api/development_runtime.go` |
| Preview readiness | probe `…/services/{prev}:preview/proxy/` + read `Endpoints` | `api/development_runtime.go` |
| Control token | `Secrets(ns).Get({runner}-control)` → `data.token` | `api/development_runtime.go` |
| Namespace GC | `Namespaces().Delete(runtimeNamespace)` on project teardown | `api/provider_resources.go` |
| Preview ReferenceGrant | dynamic `ReferenceGrant` Get/Create/Update | `api/development_sync.go` |

`runtimeTargetFromInstance` reconstructs the runtime Service/Secret refs from the `SandboxRunner` status and validates them against the runner name before use. That resolution logic is exactly what becomes declarative in §3.

### 1.2 What it blocks

- **BYO compute.** The runtime kubeconfig is App-Studio-deployment-global. A workspace bound to a different infra provider — say a customer running their own runtime cluster behind their own `InfrastructureProvider`/APIExport — cannot be served. App Studio has one runtime cred and no way to pick another per workspace.
- **Duplicated ownership.** The infra provider already holds a live client to the runtime cluster (kro backend + operator, see [`infrastructure-architecture.md`](./infrastructure-architecture.md)). App Studio holding a *second* client to the *same* cluster is redundant when they share one, and impossible when they don't.
- **Config leakage.** Runner image defaults and preview-route wiring (host, parent Gateway, backend Service) live in App Studio env (`APP_STUDIO_SANDBOX_*_IMAGE`, `APP_STUDIO_PREVIEW_*`) even though they are properties of the infrastructure that runs the workload. See `api/deployment_defaults.go`, `api/provider_resources.go`.

### 1.3 What stays in App Studio

App Studio keeps the **product** concerns: the Project capability contract, the file workspace, signed preview URLs, the assistant. It loses only the **runtime credential** and the direct data-plane calls — those become requests to the infra provider.

## 2. Design principles

1. **The runtime cluster has exactly one owner: the infrastructure provider.** Whoever materializes a workload also serves its data plane. No other component holds a runtime credential.
2. **Data-plane verbs are subresources on the workload instance.** `sandboxrunners/{name}/log` is to a `SandboxRunner` what `pods/log` is to a Pod. This keeps the model k8s-native and routable by binding.
3. **Routing is binding-driven, never URL-driven.** App Studio never resolves a provider backend URL. It calls a subresource on a resource it is already bound to; kcp routes to the provider that exports it. This is the BYO mechanism.
4. **The verb set is declared, not hardcoded.** `Template.spec.dataPlane` says which subresources exist and how each resolves to a runtime target. One generic handler serves all templates.
5. **The data plane authorizes as the caller.** Per [`provider-connectivity-contract.md`](./provider-connectivity-contract.md) contract 2, the provider drops its own credential and acts as the forwarded bearer token. A data-plane call is gated by the caller's RBAC on the instance, not a provider-wide cred.

## 3. The Template data-plane contract

Add a `dataPlane` block to `Template.spec` ([`providers/infrastructure/apis/v1alpha1/types_template.go`](../providers/infrastructure/apis/v1alpha1/types_template.go)). It declares the verbs a template's instances expose and how each maps to a runtime endpoint. Resolution reads the instance **status** refs the backend already publishes — there is no new trust surface beyond what `runtimeTargetFromInstance` validates today.

```yaml
# Template.spec.dataPlane — sandbox-runner.yaml
dataPlane:
  # Control-token secret the provider injects as X-Sandbox-Control-Token.
  # Resolved from instance status; empty => no token header.
  tokenSecretRef: status.controlSecretRef          # {name, namespace}
  endpoints:
    log:
      serviceRef:   status.controlServiceRef        # {name, namespace}
      port:         control
      upstreamPath: /logs
      methods:      [GET]
      stream:       true                            # long-poll / follow
    sync:
      serviceRef:   status.controlServiceRef
      port:         control
      upstreamPath: /sync
      methods:      [POST]
    restart:
      serviceRef:   status.controlServiceRef
      port:         control
      upstreamPath: /restart
      methods:      [POST]
    proxy:
      serviceRef:   status.previewServiceRef        # preview Service
      port:         preview
      upstreamPath: /                               # caller path appended
      methods:      [GET, POST, HEAD]
      upgrade:      true                            # ws / SSE preview
    status:
      from: instanceStatus                          # served from CR status, no runtime hop
```

A generic resolver turns `(instance, endpointName) → {serviceNamespace, serviceName, portName, upstreamPath, tokenSecretRef}`, applying the same name-binding validation `runtimeTargetFromInstance` does now (status refs must match the runner name / live in the expected namespace, so forged status cannot redirect to arbitrary Services or Secrets — see [`app-studio-sandbox-runtime.md`](./app-studio-sandbox-runtime.md) §Capability Boundary). Phase 0 builds and unit-tests this resolver against the sandbox-runner contract with **no behavior change**.

## 4. The infra provider data-plane virtual workspace

### 4.1 Request shape

```
GET  /services/providers/infrastructure/dataplane/clusters/{ws}/sandboxrunners/{name}/log
GET  …/sandboxrunners/{name}/proxy/{path...}
POST …/sandboxrunners/{name}/sync
POST …/sandboxrunners/{name}/restart
     …/sandboxrunners/{name}/status     (served from the instance status; no runtime hop)
```

The `/services/providers/infrastructure` prefix is the hub backend proxy; the provider's serve mux sees `/dataplane/clusters/{ws}/{resource}/{name}/{verb}[/{tail}]` (§6.1). The `{ws}` segment carries colons (`root:kedge:orgs:acme`) and is a single path segment.

### 4.2 Per-request flow

1. **Identity** — extract `X-Kedge-{Tenant,User}` + bearer token (reuse `providers/infrastructure/mcpserver/context.go` `identityFromRequest`).
2. **Authorize** — `GET` the instance CR through the tenant client `For(tenantPath, token)` ([`providers/infrastructure/tenant/client.go`](../providers/infrastructure/tenant/client.go)). Success means the caller has RBAC on the instance; failure short-circuits. **This is the authz gate** — no provider-wide credential is consulted to decide access.
3. **Resolve** — load the instance's Template `dataPlane` contract; resolve the requested endpoint to a runtime Service/Secret/port (§3).
4. **Token** — read the control-token Secret from the **runtime cluster** (the provider holds this client; App Studio no longer does).
5. **Proxy** — forward to `…/api/v1/namespaces/{ns}/services/{svc}:{port}/proxy/{upstreamPath}` with `X-Sandbox-Control-Token`. For `upgrade: true`/`stream: true`, hijack and bidi-copy exactly as `edges_proxy_builder.go` does for exec/port-forward/WebSocket.

### 4.3 Wiring the runtime client into the serve process

Today the runtime-cluster client lives in the kro backend / operator. The data-plane handler runs in the provider **serve** process, which already gets the runtime kubeconfig mounted (the operator wires it). Add the runtime client to the server `Deps` so the handler can read the control Secret and reach the service-proxy. No new credential is introduced — it is the credential the provider already owns.

## 5. App Studio after cutover — **DONE (Phase 3)**

- **Delete** `runtimeConfig` / `runtimeClient` / `runtimeDynamic`, `loadRuntimeConfig`, `APP_STUDIO_RUNTIME_KUBECONFIG`, and the `runtimeKubeconfig` chart wiring.
- **Rewrite** `api/development_runtime.go` / `api/development_sync.go` handlers to call the VW subresources as the tenant user (forwarding the caller's bearer token over App Studio's existing tenant kcp client) instead of the runtime cluster.
  - `logs` → `GET …/sandboxrunners/{name}/log`
  - `sync` → `POST …/sandboxrunners/{name}/sync`
  - `restart` → `POST …/sandboxrunners/{name}/restart`
  - preview readiness/proxy → `…/sandboxrunners/{name}/proxy/…`
  - `status` → unchanged (already reads the CR status — control plane).
  - namespace GC → drop the explicit `Namespaces().Delete`; deleting the `SandboxRunner` CR + kro/finalizer GCs the namespace.
  - ReferenceGrant → folded into the Template's kro RGD (§6.2), removed from App Studio.
- **Keep** signed preview URLs and preview-token signing — that is App Studio product logic. Only the runtime *probe* moves to the `/proxy` subresource.

After this, App Studio's `SandboxRunner` values shrink to roughly `{projectRef}` (§6.2 moves the rest to infra).

## 6. Open decisions

### 6.1 VW transport vehicle — **decided in Phase 1**

The "subresource on the instance" semantics can be realized two ways:

| Vehicle | Pros | Cons |
|---|---|---|
| **kcp APIExport subresource** on the instance kind | Purest model; reachable via the normal bound-resource API path App Studio already uses; no per-workspace URL mapping | Unproven that kcp APIExport supports arbitrary custom subresources on CRD-backed resources |
| **Provider serve mux behind the hub backend proxy** (`/services/providers/infrastructure/dataplane/…`, workspace in the path) | **Proven in-repo** — the provider already serves `/mcp` this way, and `edges_proxy_builder.go` shows proxy + upgrades + WebSocket through the hub; no kcp-VW machinery | Workspace addressed explicitly in the URL rather than implied by the bound resource; the handler re-authorizes against the instance itself |

**Decision (Phase 1):** the **provider serve mux** vehicle. The handler mounts at `dataplane.PathPrefix` (`/dataplane/`) on the provider's existing HTTP server and is reached through the hub backend proxy with the caller's bearer token forwarded as-is. The URL carries the workspace explicitly —
`/dataplane/clusters/<ws>/<resource>/<name>/<verb>[/<tail>]` — and the handler authorizes by fetching the instance **as the caller** (a tenant-scoped GET; 403/404 is the gate). This keeps the k8s-native subresource *semantics* while avoiding the unproven kcp-APIExport-subresource path. BYO compute still falls out of the binding: App Studio resolves which provider backs a workspace and routes there; a future migration to a true APIExport subresource can swap the transport without touching the resolver or the handler logic.

### 6.2 Move runner config ownership to infra

- **Images** — runner / token-generator image defaults move to the Template / infra config. App Studio stops setting `APP_STUDIO_SANDBOX_*_IMAGE`; the chart guard added in `faroshq/kedge#362` moves to infra. (#362 remains the correct interim prod fix until then.)
- **Preview routing** — `previewRouteEnabled` + host / parentGateway / backend derivation move from App Studio (`normalizeSandboxRunnerPreviewRouteValues`) to infra, which already has `application.baseDomain` + gateway in its `InfrastructureProvider` spec.
- **ReferenceGrant** — fold the cross-namespace `ReferenceGrant` into the sandbox-runner kro RGD (it already emits the HTTPRoute) rather than App Studio creating it imperatively.

### 6.3 Streaming through two proxies

Data-plane streams traverse the hub backend proxy **and** the kcp front-proxy. `edges_proxy_builder.go` already tunnels upgrades through the hub; reuse that path and validate logs-follow / preview-WebSocket end-to-end early in Phase 1.

## 7. Phasing

| Phase | Deliverable | Risk |
|---|---|---|
| **0** | `Template.spec.dataPlane` API + generic resolver + unit tests; annotate `sandbox-runner.yaml`. No behavior change. | Low — pure additive logic |
| **1** | Infra data-plane VW handler (log/sync/restart/proxy/readiness); runtime client in serve `Deps`; register the VW. **Spike §6.1** here. e2e against a kind runtime. | **High — transport spike + streaming** |
| **2** | Move runner config to infra (§6.2): image defaults, preview-route derivation, ReferenceGrant + namespace GC into the kro lifecycle. | Medium |
| **3** | App Studio cutover (§5): rewrite handlers to call subresources; delete the runtime credential and chart wiring. | Medium |
| **4** | BYO validation: a second `InfrastructureProvider`/APIExport backed by a different runtime cluster serves a bound workspace with **zero App Studio changes**. Docs + dead-field cleanup. | Low |

Phases 0–1 are the de-risking core (prove the contract + the transport). Phase 3 is the payoff: App Studio loses the runtime credential. Phase 0 is independently mergeable and useful regardless of how the Phase 1 spike resolves.

## 8. Security notes

- The data plane is gated by the **caller's** RBAC on the instance (step 4.2.2), consistent with contract 2. A forged or stale instance status cannot redirect the proxy: the resolver re-applies the name-binding validation that `runtimeTargetFromInstance` performs today.
- The runtime credential's blast radius shrinks from "App Studio + infra both hold it" to "infra only," and the minimal runtime role from [`app-studio-sandbox-runtime.md`](./app-studio-sandbox-runtime.md) §Runtime-kubeconfig-RBAC now applies to the infra provider's serve account.
- The control-token Secret is read provider-side; it never transits App Studio.
- The untrusted-code caveats in [`app-studio-sandbox-runtime.md`](./app-studio-sandbox-runtime.md) §Current-Security-Caveats are unchanged by this work — they remain a runtime-isolation TODO independent of where the data plane lives.
