# Limits, quotas and billing: `contrib-metering` as the platform's metering provider

Status: **NOT IMPLEMENTED.** First written 26 October 2026 around kcp
`ResourceQuota`; rewritten 28 October 2026 after two things changed: the
global, cluster-aware validating admission webhook
([kcp-dev/kcp#4288](https://github.com/kcp-dev/kcp/pull/4288)) merged and is
in the kcp build railgrid pins, and `railgrid/contrib-metering` was created
to hold the billing and metering provider. Section 2 describes current code
and is accurate as of 28 October. Everything from section 3 on is a
proposal: contrib-metering is not yet deployed with any railgrid platform
(phase 0, the deployable itself, is in progress in `railgrid/contrib-metering`:
one `metering` binary with `init`, the chart, the operator portal and the
Tilt library exist there, uncommitted as of 28 October; the hub's
`--kcp-admission-config-file` and audit flags landed the same day,
uncommitted), no `provider-sdk/usage`, no
`MembershipReport` emitter in the hub, no `/api/admin/plans`, no
`/api/orgs/{org}/billing/*` or `/api/billing/stripe/webhook` routes, no
"Plans" or "Billing" admin pages and no Stripe code in railgrid. When a phase
lands, update this line and move the document out of `docs/roadmap/`.

Companion to [organizations.md](../organizations.md) (O-5, O-6),
[provider-scoping.md](../provider-scoping.md) (P-3, P-4),
[security.md](../security.md), [byo-providers.md](../byo-providers.md) and
[install-external-kcp.md](../install-external-kcp.md).

## 1. The target

A platform admin says, in one place, what a **free** account gets and what a
paid one gets:

```
free:   1 workspace, providers {agents, edges, app-studio},
        3 agents, 2 edges, 2 projects, 600 request-cost units/min,
        $10/month of model spend
team:   5 workspaces, every platform provider, 25 agents, 20 edges,
        20 projects, 6000 request-cost units/min, $200/month
```

and that holds **at the kcp shard** for every write path (portal and CLI
through the hub's `/clusters/{id}` proxy, provider controllers through their
APIExport virtual workspace, MCP tools, and direct kcp access in the external
install). Usage is metered per organization, exported to a billing backend,
and the plan an organization is on can come from Stripe. The admin area shows
plan, usage, overrides and membership per organization; the tenant sees their
plan and usage and a clear "limit reached" instead of a bare 403.

| Concern | Example | Home |
|---|---|---|
| Object-count caps per provider kind | max 3 `Agent`, 2 `KubernetesCluster`, 2 `Project` | contrib-metering's enforcement webhook, fired by kcp's global admission plugin |
| Request-rate cap | 600 weighted request-cost units per minute per org | contrib-metering's audit sink (measure) plus the same webhook (deny) |
| Allowed providers per tier | free tier cannot enable `databricks` | the hub's Enable flow and catalog, with the webhook as the backstop on `APIBinding` creation |
| Account gates | suspend an org | `core.kcp.io/inactive`, written by the hub |
| Spend caps | $10/month of model usage | providers, reading the org's effective limits |
| Metering and invoices | object-hours, request cost, USD spend | contrib-metering's aggregator and Stripe backend |
| Which plan an org is on | free, team, comped, Stripe-entitled | the hub (admin REST and Stripe), written to the org's `Entitlement` |

## 2. Where we are (28 October 2026)

### 2.1 railgrid

- **kcp build.** `go.mod` replaces `kcp-dev/kcp` and its staging modules with
  `github.com/mjudeikis/kcp` at `9790f69ef` (branch `custom-subresources-2`),
  which contains upstream `main` including the merged global webhook
  (`037a899ce`). `k8s.io/*` stays on the kcp-dev/kubernetes fork
  `20260602065202-e006560fc76a`. Embedded mode builds the server in-process
  (`pkg/hub/kcp/embedded.go`: `serveroptions.NewOptions` → `Complete` →
  `server.NewConfig` → `server.NewServer`); it sets secure serving, etcd
  ports, shard URLs, token file, OIDC and batteries, and **nothing for
  admission or audit**. The hub exposes no flag that reaches
  `GenericControlPlane.Admission.ConfigFile` or the audit options. The
  external install (`hack/install/06-kcp-shards.sh`) uses kcp-operator
  v0.10.0, whose `RootShard` and `Shard` accept `extraArgs`, `extraVolumes`
  and `extraVolumeMounts`; `Shard.spec.audit` (policy ConfigMap plus webhook)
  exists in that version, `RootShard` was not checked for it.
- **Provider provisioning.** A platform provider is an `admin.railgrid.ai`
  `Provider` object in `root:railgrid:providers` (`apis/admin/v1alpha1/types_provider.go`);
  the provisioning reconciler (`pkg/hub/providers`, wired at
  `pkg/hub/server.go:1211-1241`) creates `root:railgrid:providers:<name>`,
  the `provider` ServiceAccount and the kubeconfig Secret in
  `root:railgrid:system:providers`. `make install-provider-<name>` applies
  the `Provider` and the `CatalogEntry`; the provider's own `init` applies
  its schemas and APIExport from `RAILGRID_KCP_DIR`. The `provider`
  WorkspaceType (`config/kcp/workspacetype-provider.yaml`) has
  `limitAllowedChildren: none`, so a provider workspace cannot have child
  workspaces. The organization type (`workspacetype-organization.yaml`)
  extends `root:universal`, binds `tenants.railgrid.ai` by default and allows
  `workspace` children, which allow `edge` children.
- **Catalog visibility.** `GET /api/providers` returns every platform
  provider plus the Org's own (`pkg/hub/providers/registry.go` `ListForOrg`).
  `CatalogEntry` has `category` but no visibility or audience field, so every
  registered provider is offered to every tenant. The portal treats
  `serving.ui` presence as the signal to render a card
  (`portal/src/stores/providers.ts`).
- **Enable flow.** `POST /api/orgs/{org}/workspaces/{ws}/providers/{name}/enable`
  (`pkg/hub/restapi/providers_enable.go`) writes the `APIBinding` as
  kcp-admin. `provider-sdk/install.ApplyBindGrant` grants `bind` on every
  platform export to `system:authenticated`, and tenants are `cluster-admin`
  in their workspaces, so a hand-written `APIBinding` bypasses the hub.
- **Organizations.** The bootstrap controller
  (`pkg/hub/controllers/organization`) provisions each org's workspace under
  `root:railgrid:tenants`, its default workspace, binding, admin access, MCP
  server and index; the soft-delete controller handles the 30-day cascade.
  `createWorkspace` (`pkg/hub/restapi/workspaces.go:129`) calls
  `EnsureChildWorkspace` and friends. The hub's `core.railgrid.ai` export
  already claims `tenancy.kcp.io/workspaces`, `apis.kcp.io/apibindings`,
  core `secrets`/`configmaps`/`namespaces`/`serviceaccounts` and RBAC, so a
  hub reconciler can watch every tenant workspace's children and bindings.
  `pkg/hub/quota` (O-5/O-6) exists and nothing calls it.
- **Admin surface.** `/api/admin/*` with `--admin-users`:
  `GET /access`, `GET /users`, `GET /organizations`, `GET/POST/DELETE
  /providers[/{name}]`, kubeconfig and credential rotation
  (`pkg/hub/admin/handler.go:48-86`). Portal sections at `/bonkers/providers`,
  `/organizations`, `/users` (`portal/src/router/routes.ts:110-117`).
- **Provider-side limits** are spend only (`Agent.spec.budget`, app-studio's
  `APP_STUDIO_ORG_MONTHLY_USD_CAP`); no provider counts objects or reads an
  Organization. See the 26 October notes in git history for file references.
- **Dev loop.** `make tilt` serves the hub with a self-signed
  `certs/apiserver.crt` that is its own CA; providers from other checkouts
  plug in through `EXTERNAL_PROVIDERS_DIR` and a `hack/tilt/providers.tilt`
  library exporting `railgrid_providers(...)` ([external-providers-tilt.md](../external-providers-tilt.md)).

### 2.2 kcp at `9790f69ef` (upstream main plus the subresource work)

- **Global validating webhook** (`pkg/admission/globalwebhook/plugin.go`,
  plugin `apis.kcp.io/GlobalValidatingWebhook`, e2e
  `test/e2e/admissionwebhook/globalwebhook_test.go`). One
  `ValidatingWebhookConfiguration` is read from the plugin's section of
  `--admission-control-config-file` (an `AdmissionConfiguration` whose
  `plugins[].path` points at the VWC file); the flag is on kcp's allow-list
  (`pkg/server/options/flags.go:162`). The plugin is inert without config,
  handles Connect/Create/Update/Delete, builds one inner validating-webhook
  plugin per logical cluster (LRU of 4096, warm HTTP clients), applies API
  defaulting to the parsed VWC (so a missing selector matches everything),
  and stamps `kcp.io/cluster` on the object (new object for create/update,
  old object for delete) for the duration of the callout. It deliberately
  does **not** stamp `kcp.io/path`; the webhook resolves cluster → path from
  the LogicalCluster if it needs it. Only URL `clientConfig` with a
  `caBundle` is supported. It sits in the plugin order before
  `objectcountlimit` and `kubequota`.
- **Audit.** kcp exposes the kube audit flags (`audit-policy-file`,
  `audit-webhook-config-file`, `audit-webhook-mode` and the batch tunables),
  and stamps the logical cluster on every audit event.
- **Still relevant from v0.33.0:** `core.kcp.io/inactive` suspension,
  `core.kcp.io/max-total-objects` and `--logical-cluster-total-object-limit`,
  `bind` as an RBAC verb at APIBinding admission, `KCPKubeResourceQuota`
  with cluster-scoped quotas (no longer the enforcement path here),
  WorkspaceType `extend`, initializers and terminators, `reservedmetadata`
  protection of `*kcp.io` keys.
- **Not in kcp and not needed by this plan:** hierarchical quota, a managed
  `ResourceQuota` annotation, label promotion from Workspace to
  LogicalCluster, cross-shard usage replication. Section 12.

### 2.3 `contrib-metering`

`github.com/railgrid/contrib-metering` was created on 27 October 2026 and is
empty. The work lives in the `kcp-dev/contrib-metering` checkout (module
path still `github.com/kcp-dev/contrib-metering`, kcp staging modules
replaced with local paths, a sizeable uncommitted tree). What it is, as of
that checkout:

- **Model** (`sdk/apis/metering/v1alpha1`): `Plan` (`spec.limits[]{meter,
  shape: Level|Consumption, enforce: Meter|Soft|Hard, max}`, plus
  `spec.backend[meter]{eventName, priceID}`), `Account` (`spec.members`:
  explicit `{cluster, path}` list or selector; `status.customerRef`,
  `memberClusters`, `usage`, `window`), `Entitlement` (`spec.account`,
  `spec.plan`, `spec.overrides[]`; `status.effectiveLimits`, `usage`,
  `window`), `MeteringConfig` (`spec.target` GVR plus selector, `meters[]`
  with CEL `quantity`, `account.resolve: Membership|PathPrefix|NearestType`),
  `UsageRecord` (`subjectCluster`, `meter`, grid `window`, absolute
  `quantity`, deterministic name so re-emitting upserts), `MeteringBackend`,
  and the new **`MembershipReport`** (`spec.account`, `spec.reporter`,
  `spec.members[]{cluster, path, labels}`): a platform-asserted statement of
  which workspaces belong to an account, unioned across reporters.
- **Exports.** Four APIExports in the metering system workspace: `metering-provider`
  (MeteringConfig, UsageRecord; bound by providers in their own workspace to
  emit usage), `metering-store` (Account, Entitlement, Plan; bound in one
  store workspace the controller writes), `metering` (the tenant read view:
  Plan plus a projected copy of the tenant's Entitlement, read-only by
  MaximalPermissionPolicy), and `metering-platform` (MembershipReport; bound
  only in a hub-controlled platform workspace so a third-party provider that
  holds a `metering-provider` binding cannot forge membership).
- **Controller** (`cmd/metering-controller`, multicluster-runtime): usage
  reconciler folding UsageRecords into per-window sums per account,
  entitlement controller computing `effectiveLimits` and `status.usage`,
  membership runnable (explicit list, optional label/selector, and
  `--membership-path` MembershipReports; the `workspaces` meter is derived
  from membership cardinality), entitlement projection into member
  workspaces through the user export, flush to backends (`noop`, `openmeter`,
  `stripe`, fan-out), GORM persistence, GC, retention, optional
  `billing` WorkspaceType mixin with initializer and terminator. Since 29
  October **kcp is the inbox and the store is the ledger**: `--db-dsn` is
  required, the usage reconciler writes each record's sample to the store
  synchronously and then deletes the record (resourceVersion precondition,
  so a concurrent re-statement wins), and a record stays in kcp only while
  it cannot be consumed (unknown subject, store down). The arithmetic that
  forced it: 100 organizations with 3 workspaces and 3 meters each emitting
  hourly is 648,000 objects at 30 days, about 1.3 GB of etcd and the whole
  of the controller's informer memory; the audit sink's per-minute records
  alone are 1.4 million. As a buffer the object count is the ingest lag.
- **Enforcement webhook** (`cmd/metering-webhook`, `pkg/webhook`): serves
  `/validate`; refreshes MeteringConfigs and Entitlements on an interval;
  resolves the account from the object's `kcp.io/cluster` through the
  membership index; for each matching meter with a `Hard` limit denies a
  CREATE that would exceed `max`, adds the quantity on CREATE and subtracts
  it on DELETE in a live counter seeded from `Entitlement.status.usage`.
  Identity-agnostic (kcp-admin is denied too). `--fail-open=true` allows the
  request when the account cannot be resolved. Deployed behind kcp's global
  webhook with `failurePolicy: Ignore` and a 3 s timeout.
- **Audit sink** (`cmd/metering-audit`, `pkg/audit`): receives kcp audit
  events in batch mode, weights verbs APF-style (`pkg/audit/cost.go`), keeps
  a trailing per-account rate and emits `request_cost_rpm` as a Level
  UsageRecord; reporter-scoped so replicas sum.
- **Example plans** (`config/examples`): `free` is
  `request_cost_rpm: Hard 600` and `workspaces: Hard 1`.
- **What it does not do:** no central sampler (providers self-meter), no
  notion of allowed providers or APIs, no cross-shard usage replication, no
  subscription or checkout handling in the Stripe backend (usage export
  only), no e2e for enforcement yet.

### 2.4 Invariants the design has to meet

1. **Enforced at the shard**, for every write path in 2.1.
2. **Out of the tenant's reach.** Plans, Accounts, Entitlements and
   MembershipReports live in workspaces no tenant identity can read; the
   webhook is wired at shard startup, not as a workspace object.
3. **Admin-overridable per organization**, visible in the admin area and to
   the tenant.
4. **Fail safe for reconcilers.** Caps go on kinds a user creates on
   purpose; derived kinds (Run, Placement, Session, Instance created by
   app-studio) are measured, never denied.
5. **Metering degraded must not break tenants.** The webhook fails open and
   the hub-side gates (Enable flow, O-5) keep holding; the audit sink is
   asynchronous.
6. **Works in both install modes** with the pinned kcp and no railgrid
   kcp patch.

## 3. The shape

```
                         ┌──────────────── kcp shard ────────────────┐
  portal / CLI / MCP ──► │ front door ─► admission chain             │
  provider VW writes ──► │   …, GlobalValidatingWebhook ─────────────┼──► metering-webhook  (deny over Hard)
                         │   audit (batch) ──────────────────────────┼──► metering-audit    (request cost)
                         └───────────────────────────────────────────┘            │
                                                                                  ▼ UsageRecords
  providers (agents, edges, app-studio, …) ── UsageRecords (object counts) ──► metering-controller
                                                                                  │ usage, effectiveLimits
  hub ── MembershipReport (org → workspaces) ─────────────────────────────────────┤
  hub ── Account + Entitlement (plan, overrides) ─────────────────────────────────┤
  hub ◄── Entitlement.status (plan, limits, usage) ── admin + tenant REST ◄───────┘
  hub ◄──► Stripe (customer, checkout, portal, entitlements)   metering ──► Stripe meter events
```

| Component | Owns | Runs as |
|---|---|---|
| kcp shard | the global webhook and audit callouts; `inactive` and the total-object limit | existing |
| contrib-metering | Plan, Account, Entitlement, MembershipReport, MeteringConfig, UsageRecord; the controller, the enforcement webhook, the audit sink; backends; its own operator API and portal | one standalone Helm release (three Deployments) with a kcp kubeconfig in values; owns `root:railgrid:system:metering`; registers with nothing |
| hub | which workspaces form an org (membership), which plan an org is on (admin REST, Stripe), the Enable gate and catalog filter, suspension, kcp flag plumbing, admin and tenant REST | existing hub |
| providers | counting their own user-intent kinds per tenant workspace and emitting UsageRecords | each provider, through `provider-sdk/usage` |
| portal | admin Plans and Billing sections, tenant Plan and usage page, "limit reached" mapping | host portal, no provider micro-frontend |

Metering is **not** a provider in railgrid's sense (decided 28 October
after trying it): no admin `Provider` record, no CatalogEntry, no heartbeat,
no catalog visibility, no tenant-facing anything. It is a deployable like
kcp itself: a chart whose values name the kcp kubeconfig and the workspace,
an init container that bootstraps that workspace from embedded manifests
(the one thing it keeps from the provider pattern), and its own operator
API and portal behind a bearer token. The shard reaches it through startup
flags; the hub reaches it by writing into its workspace as kcp-admin.

## 4. Workspace layout and identities

```
root:railgrid:system:metering               created by `metering init` (type universal) with the kubeconfig in values
                                            APIExports: metering-provider, metering-store, metering, metering-platform
                                            self-bound: metering-provider, metering-store, metering-platform
                                            → Plans, Accounts, Entitlements, MembershipReports, MeteringConfigs,
                                              the audit sink's UsageRecords; written by the deployment's identity or the hub
root:railgrid:providers:<provider>          + APIBinding to metering-provider (hub reconciler, landed 29 Oct), emits UsageRecords
root:railgrid:tenants:<org>                 + APIBinding to metering (hub-provisioned, read-only view; phase 2)
```

- Everything lives in the **metering workspace**. `metering init` creates
  it under its parent when the kubeconfig may (an admin kubeconfig), applies
  the exports from the manifests embedded in the binary, so the deployment's
  identity is the export owner its controllers need for the virtual
  workspaces (`--provider-endpointslice`, `--store-endpointslice`), and
  then **self-binds** `metering-provider`, `metering-store` and
  `metering-platform` in that workspace. The earlier draft put the store
  and platform in sibling system workspaces; that cannot work, because kcp
  scopes a ServiceAccount token to the logical cluster that minted it
  (`authentication.kcp.io/scopes`), so the provider SA could never write a
  sibling workspace, and the `provider` WorkspaceType forbids children
  anyway. Self-binding keeps one writer identity per object: the provider SA
  and the hub as kcp-admin, with `--store-path` and `--membership-path`
  defaulting to the metering workspace. The user export `metering` is not
  self-bound (it would clash with the store binding's plans and
  entitlements); the hub binds it in org workspaces when the tenant
  read-only view lands.
- **Account per organization.** `Account.metadata.name` is the org UUID,
  `spec.displayName` the org's display name. `Entitlement.metadata.name` is
  the org UUID too, `spec.account` the same. Membership is asserted by the
  hub in one `MembershipReport` per org (`spec.reporter: railgrid-hub`,
  name `railgrid-hub.<orgUUID>`) listing the org workspace, every child
  workspace and every edge workspace with `{cluster, path}`. Explicit
  `Account.spec.members` stays empty; label and selector membership are not
  used.
- **Why not the `billing` WorkspaceType mixin.** Extending the `organization`
  type with `billing` would make kcp's initializer create the Account, but it
  only applies to workspaces created after the extension (type labels are
  stamped at creation), it needs the metering export to exist before the
  hub's own WorkspaceTypes are applied, and it puts a provider's initializer
  in the org creation path. The hub already has an organization bootstrap
  reconciler; adding "ensure Account, Entitlement and MembershipReport" to it
  covers existing and new orgs with one code path, and the soft-delete
  cascade deletes them. The controller's `--boundary-workspacetype` is left
  at its default and simply never matches.
- **Membership keeps the hub as the only writer.** The `metering-platform`
  export is bound only in the metering workspace, where the hub writes
  MembershipReports as kcp-admin and the metering controller reads them. A
  provider that binds `metering-provider` in its own workspace can emit
  usage but cannot move a workspace between accounts.

## 5. The policy model

contrib-metering's `Plan` is used as-is; the railgrid `Plan` kind proposed
on 26 October is dropped. railgrid ships its plans and metering
configuration as YAML under `config/metering/` in this repo, applied by the
hub once the metering provider is Ready (a reconciler on the admin
`Provider` object's Ready condition, not a one-shot).

### 5.1 Meters

| Meter | Shape | Emitted by | Enforced on | Notes |
|---|---|---|---|---|
| `workspaces` | Level | derived from MembershipReport cardinality | `tenancy.kcp.io` `workspaces` CREATE | replaces O-6; edge workspaces excluded by the hub's report (they are not user intent) |
| `agents` | Level | agents provider | `agents.railgrid.ai` `agents` | |
| `edges` | Level | edges provider | `edges.railgrid.ai` `kubernetesclusters`, `linuxservers`, `macosservers` | one meter, three targets |
| `projects` | Level | app-studio provider | `ai.railgrid.ai` `projects` | |
| `instances` | Level | infrastructure provider | measured only | derived from projects; invariant 4 |
| `repositories` | Level | code provider | `code.railgrid.ai` `repositories` | |
| `providers.<name>` | Level | hub (from bindings it writes) | `apis.kcp.io` `apibindings` labelled `railgrid.ai/provider=<name>` | allowed-providers backstop, 5.3 |
| `request_cost_rpm` | Level | audit sink | every write in a member workspace | rate limit; `Hard` denies writes while over the cap, reads are never denied |
| `agents_usd`, `app_studio_usd` | Consumption | providers | provider-side cap (5.4) | exported to Stripe meters |

One `MeteringConfig` per enforced target, e.g.

```yaml
apiVersion: metering.contrib.kcp.io/v1alpha1
kind: MeteringConfig
metadata: { name: agents }
spec:
  target: { group: agents.railgrid.ai, resource: agents }
  meters:
    - { name: agents, type: Gauge, quantity: "1" }
  account: { resolve: Membership }
```

Adding a capped kind is one file in `config/metering/` plus the group in the
webhook rules (7.2); no hub or provider code.

### 5.2 Plans and overrides

```yaml
apiVersion: metering.contrib.kcp.io/v1alpha1
kind: Plan
metadata: { name: free }
spec:
  displayName: Free
  limits:
    - { meter: workspaces,       shape: Level, enforce: Hard, max: "1" }
    - { meter: agents,           shape: Level, enforce: Hard, max: "3" }
    - { meter: edges,            shape: Level, enforce: Hard, max: "2" }
    - { meter: projects,         shape: Level, enforce: Hard, max: "2" }
    - { meter: request_cost_rpm, shape: Level, enforce: Hard, max: "600" }
    - { meter: providers.databricks, shape: Level, enforce: Hard, max: "0" }
    - { meter: providers.kuery,      shape: Level, enforce: Hard, max: "0" }
    - { meter: agents_usd,       shape: Consumption, enforce: Soft, max: "10" }
```

- `free` is the controller's `--default-plan`; `team` is the first paid
  plan; `enterprise` has `Meter` rows only.
- Per-organization overrides are `Entitlement.spec.overrides[]` (same row
  shape, merged over the plan by the entitlement controller into
  `status.effectiveLimits`), written only through `/api/admin`. Per-workspace
  overrides do not exist: usage is pooled per account, which is what an org
  is.
- **One Account per organization; the free tier is bounded by a cap on free
  organizations per user** (landed 31 October, uncommitted; it replaced, the
  same day, a layout that pooled a user's organizations into an Account
  `user-<name>`, see docs/organizations.md O-5a). The hub's organization
  reconciler writes the Account, a MembershipReport and an Entitlement whose
  plan follows `Organization.spec.plan` (admin only,
  `PUT /api/admin/organizations/{uuid}/plan`; the default plan while empty;
  overrides are preserved, and a plan an admin set on the Entitlement
  directly is kept until spec.plan speaks). `createOrg` applies two per-user
  caps, hub flags a platform admin overrides in `User.spec.quota`:
  `--default-orgs-per-user` (10) on the organizations the user created and
  `--free-orgs-per-user` (1) on the non-personal organizations on the free
  plan the user administers today, read from their membership index. The
  free cap is off while no metering deployment is discovered. There is no
  `organizations` meter and no MeteringConfig for Organizations: the hub is
  the only gate, and the caps are soft.
- Which plan an org is on: `Organization.spec.plan` (manual, admin-only,
  wins), else `Organization.status.billing.entitledPlan` (Stripe, section
  10), else the default. The hub's billing reconciler resolves this and
  writes `Entitlement.spec.plan`; the Entitlement is the only thing the
  metering side reads.

### 5.3 Allowed providers

Three layers, outermost first:

1. **Catalog.** The hub consults the org's `effectiveLimits` when listing
   and enabling: a `providers.<name>` row with `max: "0"` renders the card
   as "not in your plan" and `Enable` returns 403 with an upgrade hint.
   (Metering itself has no CatalogEntry, so no visibility field is needed;
   the one added and reverted on 28 October is gone.)
2. **Webhook backstop.** The hub labels every APIBinding it writes with
   `railgrid.ai/provider=<name>`; a `MeteringConfig` per provider targets
   `apibindings` with that selector and emits `providers.<name>` with
   quantity 1. A hand-written binding without the label is caught by layer 3.
3. **Bind grant.** `ApplyBindGrant`'s `system:authenticated` subject is
   replaced by the hub's identity (the hub writes bindings as kcp-admin
   anyway), so a tenant cannot bind directly. This was in the original plan
   and stays.

### 5.4 Spend caps

kcp cannot see tokens. The plan carries `agents_usd` and `app_studio_usd`
rows; providers already keep USD ledgers (`Agent.spec.budget` checks,
app-studio's `OrganizationSpend`). Delivery: contrib-metering projects each
org's Entitlement into its member workspaces through the `metering` user
export (`pkg/controllers/projection.go`), so a provider reads
`entitlements.metering.contrib.kcp.io` in the tenant workspace with a
read-only `spec.requires` claim. That needs one hub-side change: a `requires`
entry on a `platform`-visibility provider must not become an Enable-time
dependency edge, because the user export is bound by the hub in every org
workspace rather than enabled by the tenant. Until that lands, providers
keep their env-configured fallbacks. Providers also emit the USD meters as
Consumption UsageRecords so Stripe can invoice overage (section 10).

## 6. Enforcement, end to end

A user creates an `Agent` from the portal:

1. The hub proxy forwards `POST /clusters/<ws>/apis/agents.railgrid.ai/v1alpha1/agents` to kcp as the user.
2. Admission runs; `GlobalValidatingWebhook` matches the VWC rule for
   `agents.railgrid.ai`, stamps `kcp.io/cluster: <ws>` on the object and
   calls `https://metering-webhook…/validate`.
3. `metering-webhook` maps the GroupResource to the `agents` MeteringConfig,
   resolves `<ws>` → account `<orgUUID>` through the membership index built
   from the hub's MembershipReports, finds `agents: Hard 3` in the org's
   `effectiveLimits`, checks the live counter (seeded from
   `Entitlement.status.usage.agents`, which the agents provider's
   UsageRecords keep current) and either allows and adds 1, or denies with
   `metering: agents limit exceeded for account <orgUUID> (limit 3)`.
4. kcp returns the 403; the hub proxy passes it through; `portalkit` maps the
   `metering:` prefix to the shared "limit reached" surface with a link to
   the plan page. The CLI and MCP see the same message.

Properties worth stating:

- **Seeding and drift.** The counter is authoritative usage plus in-flight
  deltas. Authoritative usage lags by one window plus grace; the delta
  covers the gap. A webhook restart reseeds from the Entitlement and may
  under-count until the next window, which errs towards allowing, matching
  invariant 5. Window granularity for Level meters should be one minute
  (`--window-granularity=1m`), not the hour default.
- **Provider VW writes.** Whether admission runs for writes through an
  APIExport virtual workspace is **unverified**; the first e2e in section 11
  settles it. If it does not, provider-created objects are still measured
  (UsageRecords) but not denied, which is acceptable for derived kinds and
  must be documented for user-intent kinds a provider creates on a user's
  behalf (an MCP `create_agent` call lands via the front door, so it is
  covered).
- **Rate limit.** `request_cost_rpm` is computed by the audit sink from
  weighted verbs and emitted per minute; the webhook denies writes while the
  org is over the cap. Reads are never denied (the audit hook cannot, and
  the webhook does not see them). The hub's pre-auth limiters stay as they
  are.
- **Suspension** is `core.kcp.io/inactive` on every workspace of the org,
  written by the hub on `Organization.spec.suspended` and during the
  soft-delete grace period; kcp then refuses everything including provider
  writes. Not a metering concern.
- **Total objects.** Keep `--logical-cluster-total-object-limit` as a
  shard-wide backstop in both install modes; the per-workspace
  `max-total-objects` reconciler from the first draft is dropped.
- **Multi-replica, multi-shard.** The webhook counter is per replica but is
  seeded from one account-wide number, so replicas disagree only by their
  own in-flight deltas. The audit sink's `--reporter-id` makes shard and
  replica counts sum. Measurement across shards is correct as long as every
  shard runs the same hooks (7.2).

## 7. Wiring the shard

### 7.1 Embedded kcp (Tilt, `make e2e`, single-binary installs)

Three hub flags (landed 28 October, uncommitted), generic rather than
metering-specific, in `pkg/hub/options.go`, `cmd/railgrid-hub/main.go` and
`pkg/hub/kcp/embedded.go`:

```
--kcp-admission-config-file      AdmissionConfiguration → GenericControlPlane.Admission.GenericAdmission.ConfigFile
--kcp-audit-policy-file          → GenericControlPlane.Audit.PolicyFile          (set both or neither;
--kcp-audit-webhook-config-file  → GenericControlPlane.Audit.WebhookOptions.ConfigFile   batch mode is kube's default)
```

The hub does not write the files: whoever deploys the metering provider
does, because the webhook URL, its CA and the enforced groups are the
provider's facts. Under Tilt, contrib-metering's `hack/tilt/providers.tilt`
exports `railgrid_shard_config(host_ports, kcp_loopback_proxy)`, which
railgrid's Tiltfile calls right after `config.parse()`, before the hub
command is built. It generates the webhook's self-signed serving cert once
under the checkout's `.local/kcp/` (the same pair the chart's TLS Secret is
fed from), writes `admission.yaml`, `globalwebhook.yaml` (7.2, with the
webhook's host port and that cert as `caBundle`), `audit-policy.yaml` and
`audit-webhook.kubeconfig`, and returns the paths, which the Tiltfile turns
into the three flags. The host ports come from railgrid's checkout allocator
(`METERING_PORT`, `METERING_WEBHOOK_PORT`, `METERING_AUDIT_PORT`), so the
embedded kcp on the host reaches the pods through Tilt's port forwards. A
checkout without `railgrid_shard_config` changes nothing for the hub.

### 7.2 External kcp (kcp-operator)

The metering chart (`deploy/charts/metering`) gains a `shardConfig` section
that renders a ConfigMap with `admission.yaml`, `globalwebhook.yaml`,
`audit-policy.yaml` and a Secret with `audit-webhook.kubeconfig`, CA bundles
filled from the cert-manager Certificates the chart already issues.
`hack/install/06-kcp-shards.sh` mounts them on both `RootShard` and `Shard`
through `extraVolumes`/`extraVolumeMounts` and adds to `extraArgs`:

```
--admission-control-config-file=/etc/kcp/metering/admission.yaml
--audit-policy-file=/etc/kcp/metering/audit-policy.yaml
--audit-webhook-config-file=/etc/kcp/metering/audit-webhook.kubeconfig
--audit-webhook-mode=batch
--logical-cluster-total-object-limit=5000
```

(`Shard.spec.audit` could carry the audit half; using `extraArgs` for all of
it keeps root and non-root shards identical.) Every shard must carry the
same config: a shard without the webhook is a shard without enforcement.

The VWC itself:

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata: { name: railgrid-metering }
webhooks:
  - name: enforce.metering.railgrid.ai
    clientConfig: { url: https://metering-webhook.<ns>.svc:9443/validate, caBundle: … }
    rules:
      - apiGroups: [agents.railgrid.ai, edges.railgrid.ai, ai.railgrid.ai, code.railgrid.ai]
        apiVersions: ["*"]
        resources: ["*"]
        operations: [CREATE, DELETE]
        scope: "*"
      - apiGroups: [tenancy.kcp.io]
        apiVersions: ["*"]
        resources: [workspaces]
        operations: [CREATE, DELETE]
        scope: "*"
      - apiGroups: [apis.kcp.io]
        apiVersions: ["*"]
        resources: [apibindings]
        operations: [CREATE, DELETE]
        scope: "*"
    failurePolicy: Ignore
    timeoutSeconds: 3
    sideEffects: None
    admissionReviewVersions: [v1]
```

Groups are explicit and resources are `*` so that a new capped kind in an
existing provider group needs only a `MeteringConfig`; a new provider
**group** is a shard flag change, which is an operator action by design.
UPDATE is not matched: Level meters count objects, not edits. Status
subresources are not matched by `resources: ["*"]`.

#### Tilt against the kcp-operator (`make tilt-cluster`, landed 29 October)

`Tiltfile.cluster` now runs the same wiring against operator-managed shards,
uncommitted:

- **The kcp image is the fork.** The commit `go.mod` pins
  (`replace github.com/kcp-dev/kcp => github.com/mjudeikis/kcp …`) is built
  from the kcp checkout's git objects by `hack/scripts/build-kcp-image.sh`
  (the fork's own Dockerfile, cached per commit, `kind load`ed) and every
  component (RootShard, Shard, FrontProxy + proxy) runs it. `--kcp-build=<ref
  or working tree>` builds something else, `--kcp-image-repo/-tag` skips the
  build (an upstream tag runs without enforcement). The build from a
  `git archive` has no `.git`, so kcp's Makefile stamps `v0.0.0-local`.
- **Shard config is a Secret plus `extraArgs`.** `railgrid_shard_config` in
  contrib-metering gained `mount_path` and an in-cluster mode
  (`host_ports=False`: `https://metering-webhook.<ns>.svc.cluster.local/validate`,
  `http://metering-audit.<ns>.svc.cluster.local:9444/audit`) and returns
  `files`. `Tiltfile.cluster` publishes them as Secret `default/kcp-shard-config`,
  mounted at `/etc/kcp/railgrid` in the root shard and every other shard
  with the three flags, through a `KCP_SHARD_CONFIG_FILE` hook added to kcp's
  `contrib/tilt/Tiltfile.static` (`deploy_kcp` gained `extra_volumes` /
  `extra_volume_mounts`). The hook lives on the fork branch, so `--kcp-dir`
  has to be a checkout of it; the stack fails loudly otherwise.
- **The bootstrap wall (§12) is gone on the hub side.** The hub's own
  bootstrap helper (`pkg/util/confighelpers`) and the 11 `config/kcp`
  manifests now use `bootstrap.railgrid.ai/create-only` instead of kcp's
  `bootstrap.kcp.io/create-only`, which kcp's reserved-metadata admission
  lets only system identities write. kcp-admin can set a railgrid key
  anywhere, so the embedded loopback-client special case was removed too and
  the pinned fork commit works as is for `tilt-cluster` and the external
  install. Objects created under the old key keep it (system-reserved, no
  admin can remove it, and a blind update of a Workspace fails validation on
  `spec.URL`/`spec.cluster` anyway), so the helper honors both keys when
  deciding to skip. The two Tilt hooks (`KCP_SHARD_CONFIG_FILE`, `extra_volumes`)
  are the only uncommitted kcp changes left, on `custom-subresources-2` in
  the local kcp checkout.

`hack/install/06-kcp-shards.sh` (the external install) still has none of
this; it needs the same Secret and `extraArgs`/`extraVolumes` block, with
cert-manager-issued CA bundles instead of the Tilt self-signed pair.

## 8. Hub changes

| Area | Change |
|---|---|
| `pkg/hub/options.go`, `cmd/railgrid-hub/main.go`, `pkg/hub/kcp/embedded.go` | `--metering-*` flags; write admission and audit files; set the kcp options (7.1) |
| `pkg/hub/kcp/bootstrap.go`, `config/kcp/` | nothing new: the hub's org reconciler and config applier write into `root:railgrid:system:metering` as kcp-admin (the deployment self-binds its exports there) |
| `pkg/hub/metering/organization.go` (landed 29 Oct, uncommitted) | **organization reconciler** on the same manager as the org bootstrap reconciler: per `Organization`, ensure `Account` (display name, server-side apply), `Entitlement` on `--metering-default-plan` (default `free`; create-only, so a plan an admin or billing later sets is never reset) and the `railgrid-hub.<org>` `MembershipReport` (org workspace plus every child workspace, `{cluster, path}`, written only when the set changes); all three deleted when the Organization is gone; a no-op until discovery reports metering installed, retried every minute until then. Existing organizations are covered on the first pass. Membership is refreshed on a 5-minute requeue; a `tenancy.kcp.io/workspaces` watch per org workspace is the follow-up that replaces the timer. `Organization.spec.plan` landed 31 Oct: the Entitlement's plan follows it (the default plan while empty, overrides preserved, a plan an admin set directly on the Entitlement kept until spec.plan speaks), written through `PUT /api/admin/organizations/{uuid}/plan`. Still to build here: `spec.suspended`, `status.billing` |
| `pkg/hub/metering` (landed 29 Oct, uncommitted: discovery + read-only proxy) | **discovery**: polls `--metering-workspace` for the ConfigMap `default/metering-endpoints` and Secret `default/metering-admin` that `metering init` publishes; `GET /api/admin/metering` (installed, version, readyz) and `GET /api/admin/metering/{collection}` proxy the metering API with the published token behind the admin gate, which is what the Bonkers **Metering** section renders (the hub portal authenticates with a bearer in localStorage, so an iframe of the metering portal would have no auth; native tables over the proxy instead). Still to build: the workspace watch for the membership reporter (above); **config applier**: applies `config/metering/` (Plans into `store`, MeteringConfigs into the provider workspace) when the `metering` admin `Provider` is Ready and on file change at startup; **billing reconciler** (section 10) |
| `pkg/hub/metering/provider.go` (landed 29 Oct, uncommitted) | **provider binding reconciler** on the admin multicluster manager, per `Provider`: once discovery reports metering installed (the feature gate; until then every Provider is retried a minute later and nothing is written), it creates the `metering-provider` APIBinding in `status.workspacePath` against `--metering-workspace`, labelled `metering.railgrid.ai/managed-by=railgrid-hub`, and re-checks until kcp reports it Bound. Additive only: a binding is never removed (kcp refuses while bound objects exist, and the records are the provider's usage history) and a same-named binding to another export is left alone and logged. The narrow `railgrid:provider` role gained `usagerecords` and `meteringconfigs` in `metering.contrib.kcp.io`. Verified live under Tilt: all nine platform provider workspaces Bound within a minute of the hub restart. Org-owned providers (`root:railgrid:tenants:<org>:providers:<name>`) are not covered |
| `pkg/hub/providers` | still to build: Enable refuses `providers.<name>: 0` rows; bindings labelled `railgrid.ai/provider` |
| `provider-sdk/install` | `ApplyBindGrant` subject becomes the hub identity |
| `pkg/hub/metering/admin.go` (landed 29 Oct, uncommitted) | the admin writes, straight into the metering workspace as kcp-admin behind the admin gate, each logged with the caller: `PUT /api/admin/metering/plans/{name}` (create or update displayName and limits; the backend meter mapping is preserved), `DELETE …/plans/{name}` (refused for the default plan and for a plan an entitlement still uses), `PUT /api/admin/metering/entitlements/{name}` (plan, which must exist, and overrides). Limits are validated against the PlanLimit schema plus the rule the CRD cannot say: Soft and Hard need a numeric max. `GET /api/admin/metering` reports `writable` and `defaultPlan`. The metering controller recomputes `effectiveLimits` on its sample interval (one minute); a Plan watch in its entitlement reconciler would make that immediate |
| `pkg/hub/admin`, `pkg/hub/restapi` | still to build: `GET/PATCH /api/admin/organizations/{org}/entitlement` (suspended); `POST …/resync`; `GET /api/orgs/{org}/billing` (plan, effective limits, usage, membership count, subscription state); the O-5 check in `createOrg` wired to `pkg/hub/quota` at last |
| `pkg/hub/quota` | O-5 (landed 31 Oct): the two per-user caps (`Caps`, overridden by `User.spec.quota`, `IsFree` on `Organization.spec.plan`) and the checks `createOrg` runs, the free one counting the free organizations the user administers from their membership index; O-6 deleted |
| `pkg/hub/metering/plans.go` (landed 31 Oct) | plan existence check for the admin plan route. A user reconciler (Account + Entitlement per User) and an Entitlement-backed `organizations` cap landed the same morning and were removed again that day with the move back to one Account per organization |
| portal | Bonkers **Metering** section (landed 29 Oct: status, endpoints, and the five collections as read-only tables through the proxy; "not installed" with the Helm hint otherwise). Plan and entitlement editors landed 29 Oct: new/edit/delete plan with a per-meter limits editor, and per entitlement a plan select plus overrides, through the write routes above. Still to build: **Billing** per org (plan, overrides editor, usage bars from `Entitlement.status`, members, conditions, suspend, Stripe state, resync); org settings **Plan and usage** page; `portalkit` "limit reached" mapping for the `metering:` 403 prefix and `N of M` hints next to create buttons |
| `Makefile`, Tilt, charts | `install-provider-metering`, `init-provider-metering`; `METERING_ENABLED` through the external-providers contract; hub chart `metering.*` values that set the flags |
| e2e | `test/e2e/suites/metering` (section 11) |

Nothing here needs a metering micro-frontend: the admin pages are host
portal pages reading hub REST, which reads the store as kcp-admin.

## 9. Provider changes

- **`provider-sdk/usage`** (landed 29 Oct, uncommitted): `usage.Setup(mgr,
  usage.Options{Meters, Provider, Granularity})` on the provider's
  leader-elected multicluster manager registers one watch-driven reconciler
  per resource-counter meter. Any change to a counted object in a tenant
  workspace re-counts that workspace from the manager's cache (the sum of
  the meter's kinds, typed or unstructured) and server-side-applies one
  `UsageRecord` for the current grid window (hourly by default) into the
  provider's own workspace, named with the contract's deterministic
  formula (`usage.RecordName`, pinned by test against contrib-metering's
  `UsageRecordName`) and labelled `usage.railgrid.ai/meter` and
  `usage.railgrid.ai/subject`. Workspaces with objects are re-stated every
  window; zero is stated once. The **feature gate** is the API itself: the
  emitter probes for `usagerecords` in the provider workspace and pauses,
  re-checking every minute and logging the state change once, while the
  resource is not served (`metering-provider` not bound) or not permitted
  for the provider's identity; a provider carries no flag and no metering
  dependency. Consumption meters (USD ledgers) are still to build. The
  binding to `metering-provider` is provisioned by the hub (section 8), so
  a provider declares nothing in `spec.requires`.
- Emitted today, each from a `usage.go` next to the provider's controller
  manager: **agents** → `agents`; **app-studio** → `projects`; **code** →
  `repositories`; **edges** → `edges` (sum of KubernetesCluster, LinuxServer,
  MacOSServer) and `services`; **infrastructure** → `instances` (counted
  unstructured, sharing the instance controller's informer; its
  `infrastructure-runtime` role gained the `usagerecords` rule); **kuery** →
  `savedviews`; **quickstart** → `greetings` as the reference. Still to
  emit: `agents_usd`, `app_studio_usd`. Verified live under Tilt on 29 Oct:
  code and edges stated `repositories`, `edges` and `services` for the dev
  workspace within a minute of rebuilding.
- Each provider's `TestUsageMetersMatchScheme` runs `usage.Validate` against
  the scheme its manager uses, so a kind missing from the scheme fails the
  build rather than the first leadership term. A declaration of the meters
  in `config/metering/` is still to come with the config applier.
- Spend enforcement reads the projected Entitlement (5.4) once the
  `requires` semantics for platform providers exist; until then unchanged.

## 10. Stripe

The division is: the **hub** owns the customer relationship (one Stripe
Customer per Organization, Checkout and Customer Portal sessions, the
webhook route, mapping active entitlements to a plan) and writes the result
to `Entitlement.spec.plan`; **contrib-metering** owns usage export (its
Stripe backend pushes meter events with `Plan.spec.backend[meter].priceID`
and dedupes by window). Neither enforces; the webhook does.

- **Customer identity.** Both sides must land on the same Customer.
  Convention: `metadata[kcp_account]=<orgUUID>` and
  `Idempotency-Key: org-<uuid>` on create; the hub creates first (it needs
  the id for Checkout) and stores it in
  `Organization.status.billing.customerID`; contrib-metering's backend
  looks up by the same metadata before creating. Verify the backend's lookup
  does that rather than always creating.
- **Mapping.** Product and recurring Price per paid Plan; Entitlement
  Feature `plan.<name>` per Plan (`Plan` has no field for it, so the hub
  keeps the map in `config/metering/stripe.yaml` or a `Plan` annotation);
  add-on features map to `Entitlement.spec.overrides` rows. Subscription
  status mirrors into `status.billing.subscriptionStatus`.
- **Flows.** `POST /api/orgs/{org}/billing/checkout {plan}` and
  `POST /api/orgs/{org}/billing/portal` (org admins). A public,
  signature-verified `POST /api/billing/stripe/webhook` only enqueues the
  org; the billing reconciler re-reads active entitlements and the
  subscription from Stripe and writes `status.billing` and the Entitlement's
  plan. Replays, reordering and the 10-entitlement summary cap are harmless
  because the reconciler never trusts the event body. Periodic resync and an
  admin button cover lost deliveries.
- **Downgrade and non-payment.** Losing the plan feature drops the org to
  the default plan; creates stop at the new limits, data stays. `past_due`
  is a banner; `unpaid` or `canceled` revokes. Suspension is never automatic.
- **Metered spend.** Providers' Consumption UsageRecords flow through the
  controller to Stripe meters; overage is a metered Price on the same
  subscription.
- **Config and safety.** `--stripe-secret-key` (restricted key: Customers
  write, Checkout and Portal sessions write, Entitlements and Subscriptions
  read) and `--stripe-webhook-secret` on the hub; the metering chart gets its
  own key limited to Customers read and Meter Events write. `livemode` check,
  per-peer rate limit on the webhook route, `stripe listen` in Tilt,
  `stripe-mock` in e2e. Self-hosted installs and org-owned providers use the
  manual plan path.

## 11. Phases

**Phase 0, land contrib-metering** (in progress, 28 October). The checkout is
at `railgrid/contrib-metering`: module renamed, the local-path `replace`
directives swapped for the `mjudeikis/kcp` pseudo-versions railgrid pins, one
`metering` binary (`init | controller | webhook | audit`), a standalone chart
with an init container and a kcp kubeconfig in values, the operator API and
portal behind a bearer token, and `hack/tilt/providers.tilt` with the
`metering-init` action and `railgrid_shard_config`
(`make tilt EXTERNAL_PROVIDERS_DIR=../contrib-metering`). `init` creates the
workspace, applies the four exports from the embedded manifests and
self-binds three of them (section 4). Still to do: commit and push, one e2e
for the webhook against a real kcp, the end-to-end denial check
(`hack/dev/deny-test.sh`) run under Tilt.

**Phase 1, enforcement without billing.** Hub flags and embedded wiring
(7.1); the hub's writes into `root:railgrid:system:metering`;
organization reconciler writes Account, Entitlement, MembershipReport
(landed 29 Oct); membership reporter on a workspace watch; config applier with `free` and `team` and the
MeteringConfigs for `workspaces`, `agents`, `edges`, `projects`,
`providers.*`; `provider-sdk/usage` in agents, edges, app-studio; Enable gate
and catalog filter; bind grant narrowed; admin Plans and Billing sections;
`portalkit` limit-reached; `make e2e-metering`: free org denied at the 4th
agent through the front door and through MCP, admin override lifts it,
second org unaffected, provider VW write behaviour recorded, webhook down
means allow.

**Phase 2, rate and operations.** Audit sink and `request_cost_rpm`;
external-install wiring (7.2) and the `installexternal` e2e; suspension;
tenant Plan and usage page; `instances`, `repositories` meters; Postgres for
the controller in the hub chart.

**Phase 3, Stripe.** Hub billing reconciler, Checkout and Portal routes,
webhook route, admin Stripe state and resync; contrib-metering Stripe
backend with price ids; sandbox in Tilt.

**Phase 4, spend.** `requires` on platform-visibility providers; agents and
app-studio read the projected Entitlement; USD Consumption meters exported;
`AgentPhaseSuspended` written on breach.

## 12. kcp items that remain

| Item | Status | Needed for |
|---|---|---|
| Admission for APIExport VW writes | unverified; e2e in phase 1 | deciding whether provider-created user-intent kinds are denied or only measured |
| `RootShard.spec.audit` in kcp-operator | unchecked; the `extraArgs` path works regardless | 7.2 tidiness |
| Cross-shard usage replication | not in kcp; sinks sum by reporter so measurement is already shard-safe | nothing in this plan; revisit if the controller ever needs to wildcard-list across shards |
| `kcp.io/path` on webhook objects | rejected upstream on purpose | nothing; membership is by cluster |
| Hub bootstrap vs. the fork's `reservedmetadata` rework | `bootstrap.kcp.io/create-only` is system-reserved in the pinned fork and the hub bootstrapped as kcp-admin, so it looped on `workspace-railgrid.yaml`, fataled after 15 min and never started its controllers. **Embedded mode fixed on 28 Oct** (uncommitted): `Bootstrapper.WithBootstrapConfig` applies the embedded manifests with the server's loopback client (`EmbeddedKCP.LoopbackConfig`), the identity kcp uses for its own bootstrap; every other hub write keeps kcp-admin | closed 29 Oct without a kcp change: the annotation is railgrid's own `bootstrap.railgrid.ai/create-only` now (only `pkg/util/confighelpers` reads it), so any kcp admin can apply the manifests and the loopback special case is gone. Objects created under the old key keep it for good (system-reserved) and the helper skips them under either key; verified on the running Tilt stack after the first attempt at a blind update looped on Workspace `spec.URL cannot be unset` |
| `CachedResource` renamed `ClusterCachedResource` (+ `ClusterCachedResourceEndpointSlice`, `spec.clusterCachedResource`); no `storage.virtual.identityHash` on APIExport | in the pinned fork; contrib-metering manifests updated | nothing further |
| `provider` WorkspaceType has no tenancy API | railgrid config; moot now that metering is not a provider | the `billing` WorkspaceType mixin is not used; the controller runs without a boundary type (`--boundary-workspacetype` empty) and accounts come from MembershipReports |
| A workspace-scoped identity cannot wildcard-list on the shard | kcp RBAC | cross-workspace reads go through the export virtual workspace (`Options.UsageClient`); same-workspace reads are scoped to the provider workspace |
| Managed `ResourceQuota` annotation, hierarchical quota, label promotion | dropped | nothing |

## 12.1 Findings from the first end-to-end denial (29 October)

The first "free org denied at its workspaces limit" through kcp's global
plugin under Tilt surfaced three things, all fixed in the uncommitted trees:

- **Tilt forwarded the webhook and audit host ports to the controller pod.**
  contrib-metering's Tilt library declared all three `port_forward`s on the
  controller resource; a Tilt forward targets a pod of the resource it is
  declared on, so 19443 and 19444 reached the controller pod (and the webhook
  one also named the Service port 443 instead of the container port 9443).
  kcp saw `EOF`, the webhook's `failurePolicy: Ignore` let everything
  through, and the audit sink received nothing. Each forward now sits on its
  own Deployment with the container port.
- **The webhook's live counter was seeded from `Account.status.usage`, which
  never carries `workspaces`.** That meter is derived from membership
  cardinality by the entitlement controller and published only in
  `Entitlement.status.usage`, so after every webhook restart the first create
  was judged against zero. The store now seeds from the entitlements first
  and lets account usage fill the rest (`seedCounter`).
- **No MeteringConfig existed, so the webhook had no rule for workspaces.**
  `init` seeds none and the hub's config applier (section 8) is not built;
  `config/examples/meteringconfig-workspaces.yaml` is the one every railgrid
  install needs, and the Tilt seed applies it with the plans. Until the
  applier exists, an operator applies it by hand.

Two things to decide, both visible in the admin area: the `workspaces` usage
counts the organization's own workspace (the hub reports it as a member, and
the webhook needs it there to attribute child creates), so `free` at
`workspaces: 1` allows no child workspace at all; and membership refreshes on
the hub's 5-minute requeue, so a workspace created between passes is
unmetered (fail-open) until the next one. A Workspace watch in the hub's
organization reconciler and a `Watches(Plan)` in contrib-metering's
entitlement reconciler close the two gaps.

## 13. Open questions

1. **Services on edges** are created by discovery as well as by the user;
   measure only until discovery learns to stop at a cap with a condition.
2. **Edge workspaces** are left out of the `workspaces` meter by the hub's
   report; confirm nothing capped is written there.
3. **Downgrade grace.** Freeze immediately or after a grace window; and
   whether suspension needs the audit-telemetry notification path.
4. **Personal organizations**: settled 31 October. One Account and
   Entitlement per *organization*, the personal one included; what bounds
   the free tier is the hub's cap on free organizations per user (O-5), not
   a shared allowance.
5. **Who pays for a personal org** when a team org is on Stripe: its own
   account, which stays on the free plan; the team org is the one on
   Stripe. Whether trials are a Stripe trial or a plan is still open.
6. **Window granularity vs. storage.** Settled 29 October by making kcp a
   buffer: records are deleted once their sample is in the store, so window
   count no longer multiplies kcp objects. Granularity now only sets the
   store's row rate (hourly counters for 100 organizations are 21,600 rows a
   day) and how promptly a level is re-stated.
7. **Webhook identity exemption.** The webhook denies kcp-admin too. That is
   wanted for the Enable gate but may bite hub bootstrap writes (default
   bindings, MCP server objects) if those groups are ever added to the
   rules; keep system groups out of the VWC or exempt `system:masters` in
   the handler.
