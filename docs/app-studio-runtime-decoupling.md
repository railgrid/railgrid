# Decoupling App Studio from the runtime cluster

Status: **Design proposal / historical rationale.** The current runtime boundary
is documented in [`app-studio-sandbox-runtime.md`](./app-studio-sandbox-runtime.md);
the sections below intentionally retain the proposed end state and alternatives
and should not be read as the current API contract.
References below to `SandboxRunner`, runner images, signed preview URLs, and
`APP_STUDIO_RUNTIME_KUBECONFIG` describe the superseded implementation or a
proposal alternative only.
Author: 2026-06-27
Related: [`app-studio-sandbox-runtime.md`](./app-studio-sandbox-runtime.md) (current runtime contract), [`infrastructure-architecture.md`](./infrastructure-architecture.md) (the kcp-native infra provider this builds on), [`provider-connectivity-contract.md`](./provider-connectivity-contract.md) (the two data paths), `providers/infrastructure/apis/v1alpha1/types_template.go`, `providers/infrastructure/dataplane/`, `pkg/virtual/builder/edges_proxy_builder.go` (the proven VW-proxy pattern).

## Summary

The proposal was written against an earlier implementation in which App Studio
held a **second kubeconfig** — `APP_STUDIO_RUNTIME_KUBECONFIG` — pointed at the
Kubernetes cluster where `SandboxRunner` workloads ran. That historical baseline
used the credential directly for logs, sync, restart, readiness, preview proxy,
control-token reads, and namespace cleanup.

That credential is the wrong coupling. It assumes App Studio and the infrastructure provider share one runtime cluster, owned by whoever deploys App Studio. It blocks **BYO compute**: a tenant whose workspace is backed by a *different* infrastructure provider (a different `InfrastructureProvider`, a different APIExport, a different runtime cluster) cannot be served, because App Studio only knows its own runtime kubeconfig.

This document proposed removing App Studio's runtime credential entirely and
moving the data plane to where the runtime cluster is already owned — the
**infrastructure provider**. The proposal described subresources on the
Template-selected workload instance (`{resource}/{name}/log`,
`…/proxy/{path}`, `…/sync`, `…/restart`) served by the owning provider. App
Studio would call those subresources as the **tenant user** over the same
authenticated kcp path used to create the instance. Because the binding routes
the call to the provider that backs the workspace, BYO compute falls out for
free — App Studio carries no per-provider logic or runtime credential.

The contract of *which* data-plane verbs exist, and *how* each one resolves to a runtime Service/Secret/port, is declared **per Template** so it generalizes to any infrastructure workload, not just sandbox runners.

## Historical proposal summary

- **Historical baseline:** App Studio owned the runtime data plane and held `APP_STUDIO_RUNTIME_KUBECONFIG`. The infra provider owned resource composition and the runtime-cluster client, but exposed **no** data-plane access to the workloads it created.
- **Proposed:** the infra provider serves data-plane verbs as **VW subresources** on workload instances; App Studio calls them as the tenant user and drops its runtime kubeconfig.
- **Why it's BYO-native:** routing is via APIBinding → APIExport → provider. The provider that backs a workspace serves that workspace's data plane against *its* runtime cluster. App Studio issues the same request regardless.
- **Generality:** verbs + target-resolution are declared in `Template.spec.dataPlane`, resolved by one generic handler. A development-capable application instance is the reference consumer; a DB shell or app log tail is the next.
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

> This work is the reference implementation of the platform-wide
> [provider-isolation rule](./providers.md#provider-isolation-the-cross-provider-boundary)
> (contract 3 in [`provider-connectivity-contract.md`](./provider-connectivity-contract.md)):
> App Studio's `APP_STUDIO_RUNTIME_KUBECONFIG` is a direct credential into
> another provider's backend — exactly what the rule forbids — and this
> design replaces it with calls to the infrastructure provider's published
> subresources.

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

The `/services/providers/infrastructure` prefix is the hub backend proxy; the provider's serve mux sees `/dataplane/clusters/{ws}/{resource}/{name}/{verb}[/{tail}]` (§6.1). The `{ws}` segment carries colons (`root:railgrid:orgs:acme`) and is a single path segment.

### 4.2 Per-request flow

1. **Identity** — extract `X-Railgrid-{Tenant,User}` + bearer token (reuse `providers/infrastructure/mcpserver/context.go` `identityFromRequest`).
2. **Authorize** — `GET` the instance CR through the tenant client `For(tenantPath, token)` ([`providers/infrastructure/tenant/client.go`](../providers/infrastructure/tenant/client.go)). Success means the caller has RBAC on the instance; failure short-circuits. **This is the authz gate** — no provider-wide credential is consulted to decide access.
3. **Resolve** — load the instance's Template `dataPlane` contract; resolve the requested endpoint to a runtime Service/Secret/port (§3).
4. **Token** — read the control-token Secret from the **runtime cluster** (the provider holds this client; App Studio no longer does).
5. **Proxy** — forward to `…/api/v1/namespaces/{ns}/services/{svc}:{port}/proxy/{upstreamPath}` with `X-Sandbox-Control-Token`. For `upgrade: true`/`stream: true`, hijack and bidi-copy exactly as `edges_proxy_builder.go` does for exec/port-forward/WebSocket.

### 4.3 Wiring the runtime client into the serve process

Today the runtime-cluster client lives in the kro backend / operator. The data-plane handler runs in the provider **serve** process, which already gets the runtime kubeconfig mounted (the operator wires it). Add the runtime client to the server `Deps` so the handler can read the control Secret and reach the service-proxy. No new credential is introduced — it is the credential the provider already owns.

## 5. Proposed App Studio cutover (historical target)

The following bullets describe the target cutover from the original design.
The current implementation has already converged on the caller-authenticated
Template/data-plane boundary described in
[`app-studio-sandbox-runtime.md`](./app-studio-sandbox-runtime.md); keep this
section as the proposal's rationale and migration checklist, not as a live
contract.

- **Delete** `runtimeConfig` / `runtimeClient` / `runtimeDynamic`, `loadRuntimeConfig`, `APP_STUDIO_RUNTIME_KUBECONFIG`, and the `runtimeKubeconfig` chart wiring.
- **Rewrite** `api/development_runtime.go` / `api/development_sync.go` handlers to call the VW subresources as the tenant user (forwarding the caller's bearer token over App Studio's existing tenant kcp client) instead of the runtime cluster.
  - `logs` → `GET …/sandboxrunners/{name}/log`
  - `sync` → `POST …/sandboxrunners/{name}/sync`
  - `restart` → `POST …/sandboxrunners/{name}/restart`
  - preview readiness/proxy → `…/sandboxrunners/{name}/proxy/…`
  - `status` → unchanged (already reads the CR status — control plane).
  - namespace GC → drop the explicit `Namespaces().Delete`; deleting the `SandboxRunner` CR + kro/finalizer GCs the namespace.
  - ReferenceGrant → folded into the Template's kro RGD (§6.2), removed from App Studio.
- **Proposal-only preview decision:** keep signed preview URLs and preview-token
  signing if a future private-preview design requires them. The current
  Template-backed preview returns the instance's ordinary `status.url` after
  the App Studio edge probe; it does not mint a signed preview token.

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

- **Images — DONE (Phase 2a).** The kro backend now substitutes `${railgrid.sandboxRunnerImage}` / `${railgrid.sandboxTokenGeneratorImage}` (from `RAILGRID_SANDBOX_RUNNER_IMAGE` / `RAILGRID_SANDBOX_TOKEN_GENERATOR_IMAGE`) into the sandbox-runner RGD, and the instance-schema image fields are now optional (deprecated, ignored). App Studio's continued injection is harmless dead data removed in Phase 3; its `#362` chart guard remains the safety net until then. The infra chart wires the env on both serve paths and rejects partial image config. The `runnerImage`/`tokenGeneratorImage` schema fields are removed in a later cleanup once App Studio stops sending them.
- **Preview routing — TODO (Phase 2b).** `previewRouteEnabled` + host / parentGateway / backend derivation move from App Studio (`normalizeSandboxRunnerPreviewRouteValues`) to infra, which already has `application.baseDomain` + gateway in its `InfrastructureProvider` spec. Needs kro RGD CEL work (compute the per-instance host from `${schema.spec.name}` + a base-domain token), best validated against a runtime cluster.
- **ReferenceGrant — TODO (Phase 2b).** Fold the cross-namespace `ReferenceGrant` into the sandbox-runner kro RGD (it already emits the HTTPRoute) rather than App Studio creating it imperatively.

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

---

## Addendum: the coordinate comes from the binding (19 September 2026)

[provider-contract-remediation.md](./roadmap/provider-contract-remediation.md)
§9 Cut C.2–C.4.

### The provider name is read, not compiled in

App Studio used to build every infrastructure data-plane URL from a constant:

```go
const infraDataPlaneProvider = "infrastructure"
u := hub + fmt.Sprintf("/services/providers/%s/dataplane/clusters/%s/%s/%s", …)
```

Which provider serves a dependency is a fact about the **tenant's workspace**,
not about App Studio's configuration: a workspace may have enabled the platform
copy of infrastructure or its own self-hosted one, and they answer to different
names under `/services/providers/{name}/`. That was
[cross-provider-simplification.md](./cross-provider-simplification.md) X-8.

`api/provider_binding.go` resolves the name from the workspace's own
`APIBinding` for the dependency's APIExport, **as the caller** — a caller who
cannot see the binding cannot use the provider either — and caches it per
(cluster, export), because it is a property of the workspace and not of who
asked.

It **gets the binding by name** rather than listing them. The hub names each
binding after the provider it enables
([`pkg/hub/restapi/providers_enable.go`](../pkg/hub/restapi/providers_enable.go)),
so the name is derivable from the export; reading the object then earns its
keep through the assertion that it really serves the export asked for, and a
binding that does not is refused rather than guessed from. The reason it is a
`get` is the hub's scoped-identity policy: a background identity may `get` an
APIBinding it names and may not list them, because a list would hand it the
full inventory of what a tenant has enabled
([provider-connectivity-contract.md](./provider-connectivity-contract.md)
§"Scoped identities", clause D). A dependency that is not enabled here is
reported as exactly that.

The rest of the URL comes from `dataplane.ProviderPath`, the inverse of the
parser the serving provider uses, so the grammar has one spelling in the tree
and a segment that would not parse back (a component name with a slash, a
workspace path where a cluster ID belongs) fails here rather than being minted
and sent. Both the data-plane client (`api/dataplane_client.go`) and the
integration action gateway (`api/integrations.go`) go through it.

`infraDependencyName` survives as a **label** — it names the dependency in the
project view — and is never a URL segment.

### The pull secret is a typed reference, not a name

At promote, App Studio asks the Code provider for an image-pull credential
(`connections/{n}/mint_registry_token/v1`, as the caller), writes it as a
`dockerconfigjson` Secret, and then **names that Secret** on the production
binding — which the Project controller copies onto the instance's
`spec.imagePullSecretRef`. The infrastructure provider bridges the Secret it is
pointed at, instead of guessing `<instance>-registry`
([provider-contract-review.md](./provider-contract-review.md) M8,
[infrastructure-architecture.md](./infrastructure-architecture.md) §12.2).

App Studio no longer reads the Code `Connection`'s Secret at all. When the
action returns an expiry, it is recorded as an annotation on the pull Secret,
so an operator debugging an `ImagePullBackOff` finds the answer on the object.

Minting stays best-effort: a public image needs no pull credential, so a
failure leaves the reference unset and logs why rather than blocking a
promotion.

---

## Addendum: the surface is verbs now (19 September 2026)

[provider-contract-remediation.md](./roadmap/provider-contract-remediation.md)
§9 Cut C.1 and C.5.

App Studio served about ninety-five routes under `/api/projects/*`. They were
three different things wearing one shape: CRUD facades over the Project CR,
Postgres CRUD for conversation threads, and the calls that were always verbs —
promote, sync, restart, logs, hydrate, turns. The hub cannot authorize any of
it per resource, because nothing in the path says which object is being acted
on in a way RBAC can see.

### The grammar

```
/dataplane/clusters/{id}/projects/{project}/{verb}[/{tail…}]
/dataplane/clusters/{id}/sessions/{thread}/{verb}[/{tail…}]
/dataplane/clusters/{id}/studios/studio/{verb}
```

62 verbs — 41 on `Project`, 13 on `Session`, 8 on the workspace `Studio` —
declared in `manifest.yaml` and the chart, and checked against the served
table by `TestDataPlaneVerbsMatchManifest`. `main.go` builds its surface with
`provider-sdk/serve`, which has no `/api/*` field at all, so the deviation this
provider had the most of is now one it cannot express.

**Three things the move made true that the old routes could not:**

- **The workspace comes from the path.** `identityFromRequest` no longer reads
  `X-Railgrid-Tenant` for the cluster; it takes the one the dispatcher parsed
  and both gates ran against. A forged header cannot move a request.
- **A conversation's project comes off the gated Session.** The old route had
  `{project}` and `{thread}` as two independent segments a caller could
  mismatch. Now `spec.projectRef` is read from the object gate 1 returned.
- **A tail is not a verb.** `integrations/{alias}`, `preview-grants/{id}`,
  `skill/{package}` keep one grant for the set, instead of needing one per
  member of it.

### What the Studio is for

A workspace-wide call — create a project, plan one, list importable
repositories — had no object, so it had no grant. It hangs off the
per-workspace `Studio` singleton now. Gate 1 is a real GET, so the portal
creates the Studio with the kube client before the first such call in a fresh
workspace (`ensureStudio` in `portal/src/api.ts`); the reconciler fills in the
service references afterwards.

### Deliberately not done here

- **`listProjects` is a kube read**, not a verb: a Project is a bound CR
  (Pillar 1). Anything joined — live instance status, the commit ledger, the
  pod-local source-revision fence — comes from `projects/{p}/view`, which is
  the whole reason that verb exists.
- **`projects/{p}/delete` is still orchestration behind a verb.** Moving it
  into a Project finalizer is Cut D. The route does not have to wait for the
  authority to move, and gating it now is strictly better than a `DELETE` on a
  facade.
- **`create-project` still ends with pod-local `sourceRevision` state.** Also
  Cut D. Again, a property of what the verb does, not of where it lives.
- **`/metrics` moved to the internal listener.** It was beside `/api/*` on the
  public port, reachable by any caller the hub proxied; it is not a Pillar 2
  route class, and `serve.New` has no field for it.

### The two headers that remain, and what they are not

`X-Railgrid-User` is a display label. `X-Railgrid-Project` is new and is a
**routing hint only**: a session verb names the conversation, not the project,
and the replica that owns a project's workspace volume has to be chosen before
the body is read — so the portal says which project a conversation belongs to.
Nothing is authorized from it. A forged value sends the request to the wrong
replica, which then authorizes it exactly as this one would have, because the
gates run on the `Session` against the caller's own RBAC wherever it lands.
The worst a lie buys is a wasted hop.


## Addendum: two credentials, and which is which (19 September 2026)

§9 Cut C.3.4. Getting this split wrong is the easiest mistake in the file, and
the first cut of it made the mistake, so it is written down.

**The provider reconciles as the provider.** Creating a project's bound
`Instance`, converging the backing `Repository`, watching both, tearing them
down on a finalizer — that is this provider's own background work. It runs as
the provider's ServiceAccount through its **APIExport virtual workspace**, with
tenant-scoped permission claims the workspace accepted at Enable
(AGENTS.md §5.4). App Studio's `manifest.yaml` therefore claims
`infrastructure.railgrid.ai/instances` (get/list/watch/create/update/delete),
`code.railgrid.ai/repositories` (the same minus delete — repositories hold user
code and outlive the project) and `code.railgrid.ai/repositorycommits`
(read-only). The reconcilers use the multicluster manager's client for
`req.ClusterName` and nothing else; there is no second client, no per-workspace
token, and no watch machinery of App Studio's own — the kinds are claimed, so
one wildcard informer per shard already serves them, and `builder.Watches` with
a mapping function back to the owning Project is the whole of it. The
`controller/tenantwatch` package that used to do this by hand is gone.

Those are **first-party** claims, so each carries an `identityHash` pinning the
exact APIExport that serves it, stamped at init from `RAILGRID_IDENTITY_HASHES`
(chart value `apiExport.identityHashes`; see the chart README). That is the
real cost of this design and it should be stated plainly: an APIExport pins one
identity per claimed resource for **every** consuming workspace at once, so one
App Studio installation serves workspaces bound to one copy of infrastructure
and one copy of code. An organization self-hosting either runs its own App
Studio with its own hashes.

**A project acts as itself with a scoped identity.** The hub-minted per-project
identity (`controller/project/identity.go`) is not a reconciliation credential
and never was one. It is what something acting *as the project*, with no human
behind it, presents: `use` on the workspace's default `MCPServer`, `get` on the
APIBindings that say where a dependency answers, `get` on the exact Instance,
Repository and Connection the project is bound to, and `create` on the declared
`instances/{verb}` subresources plus `connections/mint_registry_token`. The
`get`s are not for this provider's own reads — those go over the virtual
workspace — they are what **gate 1** on the serving side needs, since a
data-plane verb is authorized by re-reading the addressed object as the caller
before anything runs.

**The one place the two meet** is the commit. A `RepositoryCommit` is a pointer
at a source bundle held in the Code provider's own store
(`providers/code/commitbundle`), and only that provider can put bytes there, so
App Studio asks for the commit through the Code provider's `commit_files` MCP
tool — as the project identity, which is what the aggregate admits on `use` —
and then follows the `RepositoryCommit` it created over the claimed, read-only
watch. That is why the claim on `repositorycommits` is read-only and why the
identity keeps its MCP grant: a claim would let this provider write the CR, but
not the bundle it has to point at.
