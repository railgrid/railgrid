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

**The Secret carries `railgrid.ai/owner: infrastructure`, and the label is the
hand-over.** Every provider's `secrets` permission claim is now selector-scoped
to the objects it owns
([provider-connectivity-contract.md](./provider-connectivity-contract.md)
§"Label-scoped claims"), which means kcp hides an object from a provider whose
selector does not match it — a GET 404s, a LIST omits it. App Studio writes the
pull Secret as the caller and never reads it back; the *infrastructure*
provider is what reads it, to mount it on the production Instance. So it is
stamped with that provider's name, not App Studio's, and falls outside App
Studio's own claim by design (`api/project_promote.go`).

The same rule applies to any Secret a **tenant** brings themselves and then
points an infrastructure Instance at — today that is
`spec.oidcBridgeSecretRef`, and tomorrow anything else an Instance references
by name. A BYO Secret must carry:

```yaml
metadata:
  labels:
    railgrid.ai/owner: infrastructure
```

Without it the reference resolves to nothing: the object exists in the
workspace and is simply not visible through the infrastructure provider's
APIExport virtual workspace, so the bridge reports the Secret as missing
rather than as unauthorized. App Studio's own writer
(`portal/src/llmRegistry.ts`, the per-model LLM credential) stamps
`railgrid.ai/owner: app-studio` for the mirror-image reason: the Studio
reconciler reads that one, so an unlabelled key never reports `configured`.

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
two cuts of it made the mistake in opposite directions, so it is written down.

**The split is by whose API surface the object lives on, not by who is
acting.** That is the sentence to keep.

**This provider's OWN kinds ride its APIExport virtual workspace.** Project,
Session, Studio — spec, status, finalizers, annotations — plus the Secrets it
writes itself (the LLM model credentials and the promotion pull credential).
The reconcilers read and write those with the multicluster manager's client for
`req.ClusterName`, as the provider's ServiceAccount, with the tenant-scoped
claims accepted at Enable (AGENTS.md §5.4). `secrets` is the ONLY permission
claim App Studio has.

**A DEPENDENCY's kinds ride the tenant workspace itself.** The bound
`Instance`s, the backing `Repository`, an in-flight `RepositoryCommit`, the
Studio's shared search and browser backends: all of it is reached at
`{hub}/clusters/{cluster}` with a client built by
`provider-sdk/tenantaccess`, authenticated as a hub-minted scoped identity —
one per Project (`controller/project/identity.go`), one per Studio
(`controller/studio/identity.go`).

### Why not a permission claim, which is the obvious answer

It was tried, and it is the thing this whole document exists to avoid.

A permission claim on a FIRST-PARTY (`*.railgrid.ai`) group must name the
APIExport that serves it, by `identityHash`. An APIExport pins exactly one
identity per claimed resource — for **every** consuming workspace at once. So
the moment one org self-hosts infrastructure or code while another uses the
platform copy, no single pin is correct, and kcp stops serving the claimed
resources to whoever mismatches. Not with an error: it serves an empty list.
A workspace bound to an org-owned dependency would watch nothing, create
nothing, and report everything Pending forever, and the provider would have no
way to tell that from a quiet tenant.

The consequence shows up in operations, too: the hashes have to be read out of
the dependency provider's workspace and passed to the chart at install
(`apiExport.identityHashes`), `init` has to fail closed without them, and
"self-host a dependency" turns into "run a second App Studio install". That
value, its helper, its init env var and its README section are all deleted.

Tenant-workspace RBAC has none of these properties. The workspace serves
whichever copy it bound, authorization is the workspace's own RBAC on the
identity's ClusterRole, and one App Studio install serves every mix.

### What the identity may do, and who said so

Not the provider, by itself. The CatalogEntry declares a **composition** per
dependency — `spec.dependencies[].composes`, in `manifest.yaml` and the chart's
copy identically — and the tenant accepts it at Enable alongside the claims.
The hub's identity policy (clause E) admits a requested rule only when a
declared composition covers it. `internal/crossprovider/composition.go` is the
Go mirror of that declaration, and a test compares the two.

App Studio declares:

| dependency | resource | verbs |
|---|---|---|
| infrastructure | `infrastructure.railgrid.ai/instances` | get, list, watch, create, update, delete |
| code | `code.railgrid.ai/repositories` | get, list, watch, create, update |
| code | `code.railgrid.ai/repositorycommits` | get, list, watch |

No `delete` on repositories: they hold user code and outlive the project. No
`create` on repositorycommits: the CR is a POINTER at a source bundle held in
the Code provider's own store (`providers/code/commitbundle`), and only that
provider can put bytes there. App Studio holds the VERB that makes one instead
— clause C on `repositories/commit` — and the Code provider writes the object
itself once the verb's two gates pass. See the commit path below.

Each composition becomes **two** rules, because Kubernetes RBAC has two shapes
and conflating them is how a grant silently authorizes nothing or everything:

- **collection** (`create`, `list`, `watch`) — unnamed. RBAC does not apply
  `resourceNames` to a collection request, so a named `list` authorizes
  nothing at all. The bound is the declared `(group, resource)`.
- **object** (`get`, `update`, `delete`) — always `resourceNames`-scoped, to
  the exact objects this Project or Studio is bound to.

So a project's identity carries, in full:

- clause D — `use` on the workspace's default `MCPServer` (for the Code MCP
  tools that are not actions: checkout, build status, rebuild), and `get` on
  the `APIBinding`s named `infrastructure` and `code` (which say WHICH provider
  serves each dependency here, and therefore which `/services/providers/{name}/`
  segment addresses it; a `get`, never a `list`);
- clause E — unnamed `create`/`list`/`watch` on `instances`, `repositories`
  and `repositorycommits`; named `get`/`update`/`delete` on its bound
  instances, named `get`/`update` on its backing repository, and named `get`
  on the `RepositoryCommit` it is currently following up (that last one
  appears when the pending-commit pointer is written and disappears when it
  settles — the rules are restated on every refresh, which is what makes a
  grant shrink);
- clause B — `get` on the named `Connection` it was created from;
- clause C — `create` on `instances/{verb}` for the eight infrastructure
  data-plane verbs this provider calls, on `connections/mint_registry_token`,
  and on `repositories/commit` plus `repositories/stage_commit_bundle` for the
  project's backing repository, all name-scoped.

A Studio's identity is the instance composition and nothing else: unnamed
`create`/`list`/`watch`, named `get`/`update`/`delete` on
`app-studio-search` and `app-studio-browser`. It calls no MCP tool and
resolves no data-plane coordinate, so it holds no clause D, B or C rule.

Nothing anywhere is on this provider's own group: the Project is read and
written over the virtual workspace, where this provider already owns the kind.

### The watch is the same credential

Because the virtual workspace does not serve the foreign kinds, the dependency
watches cannot be `builder.Watches` on the manager. `controller/tenantwatch`
holds one LIST/WATCH per tenant workspace per kind, dialled with the identity
token, `Ensure`d by each reconcile that holds one and replaced when a 401/403
proves the token in hand is dead. Events map back to the owning Project (by the
project label, the repository claim label, or the `spec.repositoryRef` of a
commit) or Studio (by its attribution label). Nothing is polled: the unnamed
`list`/`watch` half of the composition is exactly what makes this legal.

### The one place the credentials meet

The commit. App Studio asks for it on the data-plane grammar —
`POST /services/providers/{code}/actions/clusters/{id}/repositories/{name}/commit/v1`,
as the project identity, at the coordinate this workspace's own `APIBinding`
names — because only that provider can store the source bundle the
`RepositoryCommit` points at. The action returns `{commit: {name, uid}}`; App
Studio then follows that CR by name over the tenant client and mirrors the
pointer onto the Project over the virtual workspace. Two clients, one identity,
in one function.

This replaced an MCP tool call, and the reason is worth keeping. The old path
invoked `code__commit_files`, which waited up to 75 s for the commit to land
and, when it did not, failed with the `RepositoryCommit`'s name embedded in
its prose error — which the reconciler recovered with a regular expression.
There were therefore two settlement paths (a tool that answered in time, and a
tool that did not) and the second one's correctness rested on an error string.
The action has one: every commit is pending when it is made, and the watch
this package already runs is what settles it. A commit queued behind a GitHub
rate limit is no longer a special case, just a commit that takes longer.

A payload past the catalogue's 1 MiB input ceiling goes up first through
`repositories/stage_commit_bundle` — the Code provider's second uncatalogued
large-upload verb (`docs/provider-actions.md` §"Uncatalogued large-upload
verbs") — and the commit names the returned handle instead of inline files. A
generated application is exactly that payload, which is why the project
identity carries the staging verb as well.

---

## Addendum: deleting a project is deleting the object (20 September 2026)

The last piece of project lifecycle that was not a CR write is gone.
`projects/{p}/delete` was a data-plane verb whose handler ran the whole
teardown inline — stop the assistant, delete the coding-sandbox cache, release
or delete the Code Repository, add a finalizer, delete the CR, purge Postgres,
purge attachments, purge the thumbnail, delete the workspace snapshots — and a
caller whose connection dropped half way through left the project in whatever
state that step had reached.

The portal now deletes the Project CR with the kube client, through the hub's
kcp proxy, authorized by the tenant's own RBAC on the object, with the UID as a
`preconditions` so a recycled name cannot delete its replacement. Everything
the handler orchestrated is the Project's finalizer
(`providers/app-studio/controller/project/teardown.go`), which means it is
retried rather than abandoned, ordered by dependency rather than by handler
convenience, and identical whether the deletion came from the portal or from
`kubectl delete project`.

Two consequences worth naming:

- **The assistant is stopped, not consulted.** The verb answered 409 while a
  turn was running. A delete of the object cannot: by the time the finalizer
  sees it the deletionTimestamp is set. So the finalizer interrupts the run and
  waits for the project to go idle before purging anything the turn might still
  be writing to.
- **The repository-deletion opt-in moved onto the object.** It was a query
  parameter; it is now the `ai.railgrid.ai/delete-repository` annotation, which
  whoever deletes the Project stamps in the same breath. Release stays the
  default — git is the durable copy of the user's work — and an adopted
  repository is never deleted whatever the annotation says. The project
  identity's declared composition gained `delete` on `repositories`, name-scoped
  to the project's own, because the capability moved from the human's request
  to the teardown that replaced it.

The coding-sandbox cache Instance is deliberately absent from the chain: it
carries an ownerReference to its Project, so kcp's garbage collector takes it —
which is also what makes `kubectl delete project` complete.

---

## Addendum: conversations stop depending on process-local timers (20 September 2026)

Remediation §9 Cut D.2. Two things a project's conversation used to rely on
were in-process clocks rather than durable state, and both are gone.

**Streaming is pushed, not polled.** `GET .../threads/{t}/events` woke on a
250 ms ticker and re-read the thread event log from its cursor, per open
stream. That is a fixed database read rate per connected browser tab, it is
paid whether or not anything happened, and it is the same cost on every replica
serving a stream. The log now pushes: appending a thread event issues a
Postgres `NOTIFY` on `app_studio_assistant_thread_events` with the thread's
key, one `LISTEN` connection per provider process turns that into an in-process
fan-out (`store/thread_notify.go`), and each stream waits on its own
subscription. The memory store implements the same interface with the
broadcaster alone, because it only ever has one process.

Two properties are deliberate. The signal carries no payload: it is a "look
again" edge, every reader re-reads from its own sequence cursor, so a coalesced
or duplicated notification costs at most one extra read and can never skip an
event. And the 15 s keepalive tick is the safety net rather than a separate
mechanism — it already loops back through the read — so a notification lost to
a listener reconnect costs latency, never correctness. lib/pq signals a
reconnect with a nil notification; the listener answers it by waking every
subscriber, since the notifications raised while the connection was down are
gone.

The cross-replica consequence is a side effect worth stating: Postgres
delivers a notification to every listening connection, so a stream served by
replica A is now woken by an append made on replica B. Streams were already
"served anywhere" (they are a pure store read), and they stay that way — this
makes them cheap rather than making them possible.

**Retention is a deadline on an object, not a sweep.** `runRetention` in
`main.go` was a ticker on every replica deleting every message row older than
one cutoff. It could not see that a thread was still open, it ran N times over
for N replicas, and its unit was the message rather than the conversation — so
a long-lived thread could have its own history trimmed out from under it.

Retention is now the `Session` reconciler's. `SessionStatus` gained
`turnCount` and `lastActivityAt` (the newest of the thread's own update and its
turns', read through `store.AssistantThreadActivityReader`), and the reconciler
requeues at `lastActivityAt + retention` — a computed deadline, which is the
sanctioned use of `RequeueAfter` — then deletes the Session. The purge itself
is unchanged: it is the Session finalizer that already existed. So there is one
code path that removes a conversation, it runs on the controller leader only,
the whole conversation goes at once, and a Session with an in-flight turn has
no deadline at all until that turn settles.

`runAttachmentRetention` deliberately stays a cutoff sweep. A draft attachment
is an upload no turn has claimed: it is scoped to a project and an actor,
carries its own `expires_at`, and can predate any thread — so there is no
Session to hang its deadline on. Attachments a turn *did* bind are not swept
at all; they belong to the conversation and go with it when the Session's
finalizer purges the thread.

**What this addendum does not cover.** Cut D.3 — moving the source tree's
durable authority (`SourceRevision`, the uncommitted-path set, the settlement
receipt in `providers/app-studio/workspace/source_state.go`) onto code-provider
`RepositoryCheckout`/`RepositoryCommit` objects — has *not* landed. The
project's working tree and its ledgers are still pod-local, which is why the
chart's `Recreate` strategy, the `ReadWriteOnce` volume and the
`replicaCount: 1` default all remain. See
[`app-studio-replica-awareness.md`](./app-studio-replica-awareness.md)
§"What still requires affinity".

## Addendum: the working copy stops being a place (20 September 2026)

Cut D.3 of the provider-contract remediation (§9) finishes what D.1, D.2 and
D.4 started. Those moved the *commit call*, the *conversation clock* and the
*teardown* off the request path and off each replica's own timers. This one
moves the last piece of App Studio state that only existed on a disk.

### What was on the disk

Four JSON files next to every project's working tree, on a ReadWriteOnce
volume:

- `source-revision.json` — the monotonic revision the development data plane
  hands the component agent as a fence.
- `source-state.json` — the set of paths that differ from the last commit.
- `pending-commit.json` — the `RepositoryCommit` in flight and the digest and
  paths it carried.
- `commit-settlement.json` — the receipt that clears those paths once the
  commit lands.

Plus an `initial-repository` receipt making the first-commit seeding
once-only, and an `ai.railgrid.ai/pending-commit` annotation on the Project
mirroring the first field of `pending-commit.json`.

The problem was not that these were files. It was that they were **authority**
that a second replica could not read, and whose absence was indistinguishable
from a legitimate answer. A replica with no volume read "revision 1, no dirty
paths" — a clean project at its initial revision — and acted on it. That is how
an empty file list could reach a dev sandbox, and why the whole data plane had
to be kept on one pod by routing.

### Where it went, and why not where the plan said

`Project.status.workspace`, on App Studio's own kind:

```yaml
status:
  workspace:
    sourceRevision: 42
    uncommittedPaths: ["src/App.tsx", "src/theme.css"]
    pendingCommit:
      name: commit-7f3a
      repositoryRef: demo-repo
      workspaceDigest: "…"
      paths: ["src/App.tsx"]
      requestedAt: "2026-09-20T09:41:02Z"
    settlement:
      workspaceDigest: "…"
      paths: ["src/theme.css"]
      recordedAt: "2026-09-20T09:41:09Z"
```

The plan named the Code provider's `RepositoryCheckout` as the destination.
That kind is a one-shot operation object — spec is a repositoryRef plus a ref,
status is the checkout's own result — with nowhere to put "the revision this
project's working copy is at". Landing it there would have meant adding fields
to another provider's CRD, a `repositorycheckouts` entry in
`spec.dependencies[].composes`, and a clause E in the connectivity contract, in
order to store state that provider never reads. The working copy is App
Studio's own concern about a project App Studio owns, so it lives on App
Studio's own kind: clause A covers it, the composition is unchanged, and the
Code provider is untouched.

`uncommittedPaths` is bounded by derivation, not by taste. A project tree is
capped at 500 files, so the biggest transition one project can make is a
whole-tree replacement — up to 500 writes plus up to 500 deletions — and
`MaxItems: 1024` clears that with room. Each entry is capped at the same 1024
bytes the file store already accepts for a path, so nothing the store admits
can fail to be recorded. Going past the bound is a bug in some other bound, so
it is reported rather than truncated: a silently dropped path is a file that
never reaches git.

### The shape of the change

`workspace.Ledger` is an interface with two methods — `Read` and an `Update`
that takes a mutation function. Everything that used to read or write a JSON
file now goes through it, and the file store's public surface is unchanged, so
the ~20 call sites across `api/` and `controller/project/` did not move.

`internal/projectledger` is the only implementation that matters. It merge-
patches the status subresource with the `resourceVersion` it read in the patch
body, which makes every update a compare-and-swap: the loser of a race re-reads
and re-applies rather than overwriting the winner. The retry is the ledger's,
because `Update` promises callers an atomic read-modify-write.

Which client does the patching is a per-call question — the HTTP layer acts as
the caller, the reconciler acts as itself over its APIExport virtual workspace
— so the ledger rides the context, attached in exactly three places:

- `identityFromRequest`, so every handler that resolves a caller gets that
  caller's ledger without knowing it exists. It is lazy: a health probe or an
  SSE stream builds no client.
- `runProjectAssistantWorker`, because a turn outlives the request that started
  it and runs on the supervisor's context.
- `Reconcile`, with the manager's client.

`NewFileStore` keeps an in-process ledger for tests and local runs, and
`main.go` calls `RequireContextLedger()` — so in a deployment a path that
forgot to attach one is a loud error rather than a quiet return to pod-local
authority. That is what makes "the ledger is not pod-local" a property you can
check instead of a claim.

### Two things got smaller

The `ai.railgrid.ai/pending-commit` annotation is gone. It existed because the
pointer (which commit) and the record (what it carried) lived in different
places and needed mirroring; the reconciler carried a branch for the case where
a replaced volume left the pointer without its record. Both are members of
`status.workspace.pendingCommit` now, the branch is deleted, and the project
identity's named `get` grant reads the same record the convergence loop follows.

`InitializeRepositorySource` lost its `initial-repository` receipt file. The
union it performs is idempotent, and what makes it once-only is the Project
annotation the caller stamps, which the reconciler clears once the paths are in
the ledger. One durable record instead of two that could disagree across
replicas.

### One bug the move exposed

The reconciler held the Project object across ledger writes and then `Update`d
it to set an annotation. With the ledger on the same object's status, that
`Update` now loses a race with the reconciler's own ledger write, every time.
Annotation writes are merge patches naming only the key they change
(`patchProjectAnnotation`), which carry no version and cannot go stale.

### Hydration became a rebuild

A file-at-a-time rebuild would now be one control-plane write per checked-out
file, so `hydrateWorkspaceFromRepository` does one `ReplaceTree`: one revision,
one ledger update. It passes a new `Committed` option, which says what a
rebuild means — these bytes ARE the repository's — so the paths come back
**clean** rather than queued for a commit that would push git's own content
back to git.

### What is still on the disk, and what it is for

The tree. It is a cache, and it is finally treated as one.

Detecting a stale cache needed one number back: the ledger's revision is the
same on every replica now, so a replica holding a tree from five edits ago
would compare equal to itself. `workspace/tree_revision.go` records what
revision *this directory's bytes* were written at — a cache tag, never
authority. Absent, behind, or untagged all read as stale, and a stale tree is
rebuilt. Losing it costs a rebuild; it can never cost correctness.

What a lost volume still costs is the uncommitted **bytes**. Git cannot return
what was never committed, and no amount of control-plane state changes that.
What the ledger buys is that the loss is now legible — the provider knows
exactly which paths went — instead of silent.

### What this does and does not unlock

Project affinity stays, deliberately, as a cache-locality optimization:
correctness no longer depends on it, but rebuilding on whichever replica the
Service picked would mean a git checkout per request under round-robin. One
intra-cluster hop is the better trade, and it is now a trade.

`replicaCount: 1` and `strategy: Recreate` also stay, for exactly one reason
that Cut D never reached: the workspace's shared single-session Playwright
Browser is serialized inside one process while project pinning can place two
projects of the same workspace on different replicas. `deploy/chart/values.yaml`
now says that in those words, instead of listing the volume alongside it. The
`runSandbox.mode=force` + `replicaCount > 1` refusal stays too — those claims
still have no distributed CAS.

See `app-studio-replica-awareness.md` for the full accounting.
