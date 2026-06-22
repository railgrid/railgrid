# Infrastructure provider: kcp-native architecture

Status: **Design proposal, not implemented.**
Author: 2026-06-03
Related: `providers/infrastructure/` (current REST-broker implementation), kcp `cache/v1alpha1` (CachedResource), `faroshq/kro-multicluster` (the multicluster-runtime fork of kro used by the kro backend, deployed via PR #227).

> **Revision note (rev 3):** rev 2 collapsed the platform onto kro's data model — RGDs as the user-facing template type, kro-synthesized CRDs as the user-facing instance type. The reviewer flagged this as leaking the backend through the abstraction. **kro is one backend. Terraform, native cloud APIs, anything that materializes a graph of resources from a parametric spec is another.** This rev re-introduces a platform-level type system that backends implement, with explicit guidance that tenants and the portal never see `kro.run/*`, RGDs, or anything backend-flavoured.

## Summary

The infrastructure provider today is a REST broker: tenants don't see Templates or Instances as Kubernetes resources, the "tenant" identifier is derived per request from headers, and isolation hinges on a SHA-hashed label inside a single shared management cluster. This document proposes moving the provider to a kcp-native shape with two layers:

- A **platform layer**: a `Template` CRD plus per-template CRDs (`Redis`, `Postgres`, `Application`, …), all in group `infrastructure.kedge.faros.sh`. This is the entire surface tenants and the portal see. Backend choice is a platform concern, expressed via `Template.spec.backend`, never surfaced through a tenant API.
- A **backend layer**: a small in-process interface implemented today by **kro** (via the multicluster-runtime fork already in dev), and tomorrow by terraform / cloud / whatever else. Each backend takes a platform `Template` and is responsible for reconciling instances of the per-template CRD into actual infrastructure.

The "tenant" is the kcp logical-cluster name of the workspace the instance lives in. No headers, no hashing.

## 1. Why this matters

### 1.1 The footgun we just hit

A user with an instance `test` in their workspace saw `0 / 0 / 0 / 0` on the dashboard tile despite having Pods running. Trace:

| Action | Tenant header sent | Tenant string resolver returned | `tenantHash()` → namespace |
|---|---|---|---|
| `test` provisioned (no workspace selected in sidebar) | `X-Kedge-Org: <org>` only | `root:kedge:orgs:<org>` | `b0308307e326` |
| Dashboard tile queried (after sidebar workspace pick) | `X-Kedge-Org` + `X-Kedge-Workspace` | `root:kedge:orgs:<org>:<ws>` | `9f2db7ed8014` |

Same user, same workspace, two different namespaces. The instance is real, the proxy is happy, every isolation check passes — and the data is invisible. We built a parallel identity system on top of kcp that disagrees with kcp.

### 1.2 Other costs of the current design

- **No `kubectl get`**: the catalog is hidden behind a provider-specific REST call.
- **Single-backend assumption**: REST endpoints + `tenantHash` + `kedge-tenants-*` namespacing all assume kro's flavour of "instance = label-selected CR in a shared cluster". Adding a second backend means reinventing all of it.
- **Instances aren't first-class Kubernetes objects** — no GC, no ownership graph, no audit story.
- **Per-tenant secrets via a permission claim are the only kcp-native thing happening.** The APIExport's `schemas: []` is a tell.

## 2. Design principles

1. **The platform is a small set of well-known CRDs.** Tenants and the portal only ever interact with `Template` and per-template instance kinds in group `infrastructure.kedge.faros.sh`. Nothing else is part of the public API.
2. **Backend choice is metadata on a Template, not a separate user-facing concept.** A `Redis` template might be backed by kro today, by a managed Redis API tomorrow. The tenant CR they apply doesn't change.
3. **Backends implement a narrow interface, not a data model.** Each backend is responsible for taking a `Template` and serving instances of the corresponding per-template CRD. *How* it does that — RGDs, terraform modules, cloud SDK calls — is its own business.
4. **kcp is the source of truth for identity.** Tenant = workspace logical-cluster name, embedded in the URL. No headers. No hashing.
5. **The provider binary owns the platform layer + the backends it ships with.** Today that's one backend (kro). The binary stays small; complexity lives in backends behind the interface.

## 3. Architecture

```
provider workspace (root:kedge:providers:infrastructure)
  │
  ├─ APIExport  infrastructure.providers.kedge.faros.sh
  │    schemas (every entry in group infrastructure.kedge.faros.sh):
  │      ├─ templates.infrastructure.kedge.faros.sh        ← catalog
  │      ├─ redis.infrastructure.kedge.faros.sh            ← per-template, dynamic
  │      ├─ postgres.infrastructure.kedge.faros.sh
  │      └─ application.infrastructure.kedge.faros.sh
  │    permissionClaims:
  │      - secrets    (unchanged — cloud-credentials resolution)
  │
  ├─ Template CRs                          ← platform catalog
  │      redis, postgres, application, …
  │      spec.backend = "kro" | "terraform" | "cloud" | …
  │      spec.schema  = JSON-schema for the instance kind
  │      spec.backendConfig = opaque, only the backend interprets
  │
  ├─ CachedResource publish-templates      ← projects Templates read-only
  │      spec.resource: templates.infrastructure.kedge.faros.sh
  │
  └─ <hidden, backend-private state>       ← NOT tenant-visible
        kro RGDs / kro Helm release / terraform state buckets / etc.
        live in non-tenant-bound namespaces or workspaces.
        Tenants never look here, the portal never queries here, the
        public API never mentions it.


tenant workspace (root:kedge:orgs:<org>:<ws>)
  │
  ├─ APIBinding infrastructure                       ← user clicks Enable
  │
  ├─ Visible read-only (via CachedResource):
  │      Template CRs (the catalog)
  │
  └─ Visible read-write (via APIBinding's schemas):
        Redis, Postgres, Application CRs    ← create instances


provider binary
  │
  ├─ Catalog: Template controller          ← admin-facing platform reconciler
  │      operator applies a Template CR    →
  │      ensures the per-template CRD exists in APIExport.spec.schemas
  │      hands the Template to the matching backend for backend-specific setup
  │      (for kro: author the RGD; for terraform: stage the module; …)
  │
  ├─ CachedResource provisioner            ← one-shot at startup
  │      ensures the publish-templates CachedResource exists
  │
  ├─ Backend dispatcher                    ← thin routing
  │      watches per-template CRDs across tenant workspaces (via APIExport VW)
  │      for each instance CR: look up the parent Template, dispatch to the
  │      backend the Template names, hand back status. Backend interface:
  │        ReconcileInstance(ctx, template, instance) → status, error
  │        DeleteInstance(ctx, template, instance) → error
  │
  ├─ Backends (one Go package each)
  │      kro/        — uses faroshq/kro-multicluster runtime
  │      terraform/  — sketch; not in v1
  │      cloud/      — sketch; not in v1
  │
  └─ SPA + MCP                             ← unchanged consumer
        reads Templates + per-template CRs via kcp APIs.
```

The "kro" word appears in exactly two places: the implementation of one backend (`backends/kro/`) and the operational note that we're shipping that backend first. Everything above the backend interface is backend-neutral.

## 4. Data model

### 4.1 `Template` CRD (cluster-scoped, in provider workspace)

```yaml
apiVersion: infrastructure.kedge.faros.sh/v1alpha1
kind: Template
metadata:
  name: redis
spec:
  displayName: "Redis cache"
  description: "Managed Redis with daily snapshots."
  category: Databases
  version: "0.1.0"

  # Which backend reconciles instances of this template.
  # Defaults to "kro" while we only ship one backend; an admission
  # validator will eventually enforce that the named backend is
  # registered in the provider binary.
  backend: kro

  # The CRD the platform projects into tenant workspaces.
  # name + group + version + kind together specify the per-template
  # CRD that goes into APIExport.spec.schemas.
  instanceCRD:
    group: infrastructure.kedge.faros.sh
    version: v1alpha1
    kind: Redis
    resource: redis

  # JSON-schema applied to instance.spec via the CRD's OpenAPI
  # validation. Tenant-facing; no backend specifics.
  schema:
    type: object
    properties:
      name: { type: string }
      size: { type: string, enum: [small, medium, large], default: small }
      version: { type: string, enum: ["6", "7"], default: "7" }
    required: [name]

  # Opaque to the platform. Only the named backend reads this.
  # For kro: the resource graph (semantically equivalent to an RGD).
  # For terraform: a module reference + variable mapping.
  # For cloud: provider/API/parameter mappings.
  backendConfig:
    # ↓ kro-only; the platform never inspects this content
    resources: [...]
    statusMapping: {...}

status:
  observedGeneration: 7
  registered:                  # set by the Template controller
    crdEstablished: true
    schemaInAPIExport: true
  backend:                     # set by the backend during template setup
    name: kro
    ready: true
    message: "RGD authored and accepted by kro"
```

`spec.backendConfig` is the only field the platform never interprets. The Template controller passes it untouched to the backend.

### 4.2 Per-template instance CRDs (cluster-scoped, in provider workspace; visible to tenants via APIBinding)

```yaml
apiVersion: infrastructure.kedge.faros.sh/v1alpha1
kind: Redis
metadata:
  name: my-cache
spec:
  name: my-cache
  size: small
  version: "7"
status:
  phase: Ready
  endpoint: 10.0.32.18
  conditions:
    - type: Ready
      status: "True"
      reason: AllResourcesReady
```

The CRD itself is what the Template controller adds to `APIExport.spec.schemas`. The instance CR is what tenants apply. Status is populated by whichever backend handles the parent Template — the tenant never knows.

### 4.3 What tenants and the portal see

Total surface area of the platform's tenant-facing API:

- `kubectl get templates`                                      (catalog browsing)
- `kubectl describe template redis`                            (input schema, category, etc.)
- `kubectl apply -f my-redis.yaml`                             (provision)
- `kubectl get redis my-cache`                                 (status)
- `kubectl delete redis my-cache`                              (deprovision)

No `kro`, no `rgd`, no `tenant-hash`, no `kedge-tenants-*` namespace. The portal renders Templates as catalog cards and per-template CRs as instance cards. MCP tools wrap the same kcp APIs.

## 5. Component contracts

### 5.1 Template controller (platform; new)

```
inputs:  Template CRs in the provider workspace
outputs: APIExport.spec.schemas entries
         Template.status fields
         Backend setup calls (via the Backend interface)

reconcile:
  on Template add/update:
    1. Validate spec.backend is registered. If not → status condition,
       no schema work.
    2. Ensure the CRD described by spec.instanceCRD exists in the
       provider workspace, with OpenAPI schema derived from spec.schema.
    3. Ensure that CRD is listed in APIExport.spec.schemas.
    4. Call backend.SetupTemplate(ctx, template). Record return value
       in status.backend.
    5. Set status.registered.{crdEstablished,schemaInAPIExport}.

  on Template delete:
    1. Call backend.TeardownTemplate(ctx, template).
    2. Remove the schema entry from APIExport.spec.schemas.
    3. Delete the per-template CRD.
       Existing tenant instance CRs of that kind enter a TemplateRemoved
       phase via a finalizer chain (deletion blocks until they're cleaned).

guarantees:
  - Tenants never see a CRD whose Template isn't fully set up.
  - Removing a Template removes the CRD; tenants can't create new
    instances; existing ones are GC'd through the finalizer path.
```

### 5.2 CachedResource provisioner (platform; new)

A one-shot reconcile at startup that ensures the `publish-templates` CachedResource exists:

```yaml
apiVersion: cache.kcp.io/v1alpha1
kind: CachedResource
metadata:
  name: publish-templates
spec:
  group: infrastructure.kedge.faros.sh
  resource: templates
  version: v1alpha1
  # No label selector for v1; per-tenant allowlists are §9.
```

That's it. kcp does the projection. Tenants with the APIBinding `kubectl get templates` and see the catalog.

### 5.3 Backend interface (platform; new, ~30 LOC)

```go
package infrastructure

type Backend interface {
    // Name is the value Template.spec.backend matches. Registered at
    // process startup in cmd/infrastructure-provider/main.go.
    Name() string

    // SetupTemplate is called by the Template controller after the
    // per-template CRD lands in APIExport.spec.schemas. The backend
    // does whatever it needs (kro: author an RGD; terraform: stage a
    // module) and returns a status the controller mirrors onto the
    // Template.
    SetupTemplate(ctx context.Context, tmpl *v1alpha1.Template) (BackendTemplateStatus, error)
    TeardownTemplate(ctx context.Context, tmpl *v1alpha1.Template) error

    // Run starts the backend's reconcile loop. The backend watches
    // instance CRs of the kinds it owns (via the APIExport VW) and
    // reconciles them. It blocks until ctx is cancelled.
    Run(ctx context.Context, vwConfig *rest.Config) error
}

type BackendTemplateStatus struct {
    Ready   bool
    Message string
}
```

This is the entire boundary between platform and backend. The platform doesn't know what an RGD is; the kro backend doesn't know what the CachedResource for the catalog looks like.

### 5.4 kro backend (`backends/kro/`)

Implementation of the `Backend` interface for `spec.backend: kro`.

```
Name:                "kro"

SetupTemplate:        Parse spec.backendConfig as a kro resource graph.
                      Author a ResourceGraphDefinition CR in a non-tenant-
                      bound workspace owned by this backend. Tenants
                      never see this RGD; it's an implementation detail
                      of the kro backend.

TeardownTemplate:     Delete the RGD; kro's own controller GCs the
                      synthesized CRD.

Run:                  Start the faroshq/kro-multicluster runtime
                      pointed at the APIExport virtual workspace
                      (one connection, all tenants). kro watches the
                      per-template CRs in tenant workspaces and
                      reconciles them per its RGDs — exactly what kro
                      does today.
```

Key property: the kro RGD declares the SAME group/version/kind as the platform's per-template CRD (`Redis.infrastructure.kedge.faros.sh`). kro's multicluster-runtime watches that CRD across tenant workspaces and reconciles directly. There's no in-process translation step — the CRD identity is shared, but it was authored by the platform's Template controller, not by the operator.

### 5.5 Other backends (sketch)

```
terraform backend (not v1):
   SetupTemplate:    Stage a terraform module + variable schema
                     in the backend's storage.
   TeardownTemplate: Mark module deleted; pending instances drain.
   Run:              Loop: watch instance CRs; on add, terraform plan +
                     apply; on delete, terraform destroy; sync state
                     back to instance.status.

cloud backend (not v1):
   SetupTemplate:    Validate the backend can talk to the named cloud
                     SDK with the credentials it has in scope.
   Run:              Loop: watch instance CRs; call the appropriate
                     SDK; record returned identifiers in status.
```

Adding a backend = a new Go package, a new line in `cmd/infrastructure-provider/main.go` registering it, and a `spec.backend: "<name>"` on the relevant Templates. No tenant-facing surface changes.

### 5.6 What `providers/infrastructure/server/` becomes

After PR E:
- `/healthz`
- `/mcp` — tools become kcp API calls (list Templates, apply per-template CRs)
- `/ui/*` — static SPA assets

`/api/templates`, `/api/instances` get deleted. The Go binary owns the Template controller, CachedResource provisioner, backend dispatcher, the kro backend implementation, and the SPA + MCP host. No REST broker.

## 6. Flows

### 6.1 Operator publishes a new template

```
operator                provider workspace                    APIExport                kro backend
   │                          │                                   │                         │
   │ kubectl apply            │                                   │                         │
   │   redis.template.yaml    │                                   │                         │
   ├──────────────────────────▶ Template CR created               │                         │
   │                          │                                   │                         │
   │                          │ Template controller fires         │                         │
   │                          │   ensures Redis CRD               │                         │
   │                          │   appends to APIExport.schemas[]  │                         │
   │                          ├───────────────────────────────────▶ schemas[] grows         │
   │                          │                                   │                         │
   │                          │ calls backend.SetupTemplate       │                         │
   │                          ├─────────────────────────────────────────────────────────────▶
   │                          │                                   │                         │ authors RGD
   │                          │                                   │                         │ in backend's
   │                          │                                   │                         │ private state
   │                          │                                   │                         │
   │                          ◀─────────────────────────────────────────────────────────────│ ready=true
   │                          │ Template.status updated           │                         │
                                                                  │
                                                                  ▼
                                                  tenants who APIBind now see
                                                  redis.infrastructure.kedge.
                                                  faros.sh in their workspace
                                                  AND the Template via CachedResource
```

### 6.2 Tenant provisions

```
tenant                  tenant workspace            kro backend (provider binary)        kro mgmt cluster
   │                          │                              │                                    │
   │ kubectl apply            │                              │                                    │
   │   my-cache.yaml          │                              │                                    │
   ├──────────────────────────▶ Redis CR created             │                                    │
   │                          │                              │                                    │
   │                          │ APIExport virtual workspace  │                                    │
   │                          │ delivers event to backend    │                                    │
   │                          ├──────────────────────────────▶ kro's MC runtime reconciles        │
   │                          │                              │   (matches the RGD authored        │
   │                          │                              │    in §6.1, so it knows what       │
   │                          │                              │    to materialize)                 │
   │                          │                              ├────────────────────────────────────▶ StatefulSet
   │                          │                              │                                    │ + Service
   │                          │                              │                                    │ + Pods
   │                          │                              │                                    │
   │                          │ status sync via the same VW  │                                    │
   │ kubectl get redis        ◀──────────────────────────────│                                    │
   │   ── Phase: Ready        │                              │                                    │
```

Note what tenants and the portal know about: `Template`, `Redis`. Nothing in this flow surfaces `kro.run/*`, RGDs, or any backend-specific terminology to the tenant. The kro backend handled it internally.

### 6.3 Tenant deletes

`kubectl delete redis my-cache` → APIExport VW delivers the delete → kro backend's reconcile loop removes the materialized resources → status disappears. If the tenant workspace itself is deleted, the VW stops including it; a per-backend orphan sweeper (PR C scope) cleans up management-cluster state with no source workspace.

## 7. PR breakdown

| PR | Title | Acceptance criteria |
|---|---|---|
| **A** | Template CRD + per-template CRD lifecycle | `Template` CRD added to APIExport. Template controller registered. Applying a Template creates the per-template CRD and lists it in APIExport.spec.schemas. Deleting cleans up via finalizer. Backend interface defined; a no-op stub backend used for the test. |
| **B** | CachedResource publishing Templates | A tenant workspace with the APIBinding sees Templates as a read-only resource. `kubectl get templates -A` from a tenant returns the catalog. |
| **C** | kro backend + APIExport VW wiring | Backend interface implemented for kro. The platform binary starts the kro multicluster-runtime pointed at the provider's APIExport VW. A tenant applies a Redis CR; kro reconciles it in the management cluster within 10s; status syncs back; delete propagates. Orphan sweeper for deleted tenant workspaces in scope. |
| **D** | UI + MCP migration | Portal main app and dashboard tile read Templates + per-template CRs via GraphQL. MCP tools (`infrastructure__list_templates`, `__provision`, …) become kcp API calls. Old REST endpoints get a deprecation banner. |
| **E** | Cleanup | REST handlers gone. `tenantHash`, `kedge-tenants-*` convention, per-request header identity all gone. The REST-surface e2e suite (`make e2e-infrastructure`) is removed; isolation is exercised via the kcp path. |

A through C land the new platform + the first backend (~2.5 weeks). D adds another week (mostly portal rewrites). E is a day of deletions.

## 8. Migration

Pre-v1. Clean break. PR A through C ship the new architecture in parallel with the legacy REST surface; PR D moves the UI; PR E deletes the legacy code. Existing `kedge-tenants-*` namespaces in any test cluster are orphaned and `kubectl delete ns`-able once tenants re-provision via the new path. No data-migration controller.

## 9. Open questions

1. **`Template.spec.backendConfig` shape.** The platform treats it as opaque; the kro backend interprets it as a graph (semantically an RGD). Two options:
   (a) leave it as `map[string]any` and let each backend define its own schema in its own docs,
   (b) define a Go interface `BackendSpec` with sub-types per backend, get static typing at the cost of platform-side coupling.
   Default proposal: (a) — keeps the platform binary's API stable as backends evolve.
2. **CRD versioning across templates.** When a Template's `spec.schema` changes, the CRD's served version may need to grow. Template controller needs to handle adding a new served version + marking the old one deprecated rather than overwriting; existing instances mustn't fail validation.
3. **Per-tenant catalog (allowlist).** Future feature: admin restricts which Templates a tenant can use. With this design it's a per-tenant CachedResource with a label selector, or an admission webhook on the per-template CRDs. Out of scope for v1.
4. **Cross-field validation webhooks.** OpenAPI validation in the per-template CRD covers per-field rules. Cross-field rules (e.g. "if `persistent=true`, `size` must be `large`") need an admission webhook. Each backend may want to add its own. Out of scope for v1.
5. **Backend-private state location.** The kro backend authors RGDs somewhere; where? Options: a sibling workspace `root:kedge:providers:infrastructure-kro-state`, a hidden namespace in the provider workspace, or a dedicated CRD owned by the backend. Default proposal: separate workspace per backend, owned by the platform, never bound by tenants — operational discoverability is highest there.
6. **Workspace-deletion → backend-state GC cadence.** 5-minute sweep vs. finalizer-driven. Each backend implements its own sweeper interface; the kro backend's first version is sweep-based; PR C scope.

## 10. Decisions captured

- **Tenant identity** = kcp logical-cluster name of the workspace the instance CR lives in. No headers. No hashing.
- **Templates** = `Template` CRs in the provider workspace, group `infrastructure.kedge.faros.sh`. Projected read-only via `CachedResource`. The catalog.
- **Instances** = per-template CRDs in `infrastructure.kedge.faros.sh`, declared by `Template.spec.instanceCRD`, registered by the Template controller, projected via APIExport. Tenants apply these.
- **Backends** = a Go interface with three methods (`SetupTemplate`, `TeardownTemplate`, `Run`). One implementation today (kro), the seam exists for terraform / cloud / others.
- **kro backend** uses the `faroshq/kro-multicluster` fork and authors RGDs in non-tenant-visible state. Tenants never see RGDs or anything `kro.run/*`.
- **Provider binary** = Template controller + CachedResource provisioner + backend dispatcher + registered backends + SPA + MCP host. No business logic outside backends.
- **Migration** = clean break.

## 11. What this doc is not

This is a design proposal. Code changes happen in the PRs in §7 after this doc is reviewed. Nothing in this commit changes runtime behavior.
