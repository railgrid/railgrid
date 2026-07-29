# Platform providers — Metering, Quotas &amp; Billing

**Status:** Design draft
**Owner:** TBD
**Last updated:** 2026-07-21
**Reads as a delta on:** [providers.md](./providers.md), [provider-scoping.md](./provider-scoping.md), [provider-connectivity-contract.md](./provider-connectivity-contract.md), [organizations.md](./organizations.md)

---

## Why this doc exists

Railgrid has one class of provider today: **tenant providers** (infrastructure,
edges, code, agents, …). A tenant *enables* them per-workspace (`APIBinding`),
and they act **as the caller** (the tenant's own bearer token).

The platform owner needs a **second class** — providers that are:

- operated by the **platform owner**, not the tenant;
- **cross-cutting** — they read/enforce across *every* account, not one
  workspace;
- surfaced in the **admin area** (`/bonkers`), not the tenant Providers page;
- **present in every account from day one**, but **inert until the owner
  enables enforcement**.

The first two are **quotas &amp; limits** and **billing (Stripe)**. This doc
defines the class ("platform providers"), the shared primitive both need (a
**Meter**), and a phased build plan.

This doc does **not** redo the tenant-provider Enable mechanic, the
Org/Workspace/Membership tree, or the `CatalogEntry` contract — those are
pinned in the docs linked above.

---

## Decisions pinned

Don't re-litigate; the body assumes these. (PP = Platform Provider.)

| # | Decision | Rationale |
|---|---|---|
| PP-1 | **Platform providers are a distinct class, not tenant providers.** They live under a new top-level `platform/` dir (parallel to `providers/`), are provisioned by the admin `Provider` CR into `root:railgrid:system:providers`, and are **never** enabled by a tenant. | Their consumer is the platform owner; treating them as tenant-enableable `CatalogEntry`s would wrongly expose them on the tenant Providers page and invert their trust model. |
| PP-2 | **Presence ≠ Enforcement.** The metering binding is *always present* in every account (observe-only, platform baseline). Enforcement (suspensions, charges) is a separate switch the owner flips. | You cannot retroactively meter — always-collecting is the only truthful model. Enforcement is a deliberate, reversible owner action. |
| PP-3 | **"Track every account" = auto-bound APIExport + fleet VW.** A platform provider owns an APIExport that is stamped into every tenant workspace by the org-bootstrap controller (Step G path), and reads its **APIExport virtual workspace** for one `/clusters/*` wildcard `LIST/WATCH`. Same kcp primitive as tenant providers; **binding authority inverted** (system stamps it, tenant can't opt out). | Reuses proven machinery; no new kcp feature. Imperative stamp (not `defaultAPIBindings`) because we need permission-claim acceptance **and** backfill into pre-existing workspaces. |
| PP-4 | **Shared Meter, Postgres-backed.** One cross-provider usage sink keyed by `(orgUUID, wsUUID, metric, window, quantity)`, generalizing the agents `agents_usage` table. High-volume usage data is **pushed** here; it is NOT modeled as CRs. | Millions of usage events can't be Kubernetes objects. One source of truth so quotas + billing + future consumers reuse it. |
| PP-5 | **Control plane = binding/VW; data plane = Meter.** The auto-bound CR (`AccountQuota` / `AccountBilling`) per account carries *status &amp; enforcement* (read tenant state, write suspension, show "80% of plan" to the tenant). High-volume *usage counts* go to the Postgres Meter. | Each half does what it's good at; keeps CR count O(accounts), not O(events). |
| PP-6 | **Enforcement is hard (auto-suspend on breach)** but ships behind a per-provider `mode: observe \| enforce` switch and a dry-run in `/bonkers`. | Owner-confirmed. Dry-run first is mandatory before suspending live tenants or charging cards. |
| PP-7 | **Billing model = plan/seat subscriptions** (Stripe Subscriptions), Meter feeds overage only. `BillingAccount` maps Org ↔ Stripe customer; secrets live in the provider's own minted sub-workspace. | Owner-confirmed. Simpler than pure usage-based; overage still uses the Meter. |
| PP-8 | **Layering: Meter → Quotas → Billing.** Meter is the shared foundation. Quotas consumes Meter + fleet VW to enforce. Billing consumes Meter to charge; a billing/payment failure loops back into a quotas suspension. | Clean dependency order; each phase is independently shippable. |
| PP-9 | **Prevention is layered (not just reactive).** Observe maintains state (`AccountQuota`); synchronous *deny* comes from admission. Ship **Layer 1 (hub-proxy middleware)** now — the proxy is already on every tenant write; add a quota gate driven by a `QuotaGate` GVR registration. Tighten with **Layer 2 (VAP + `AccountQuota` paramRef)**. **Layer 3 (global admission)** needs a kcp feature (PP-15). Residual until L3: writes that bypass the proxy, and per-workspace VAP being tenant-editable. | Watches can't prevent, only observe. The proxy is the one chokepoint railgrid already owns; VAP adds in-process CEL; global immutability is the upstream ask. |
| PP-16 | **Per-workspace object-count caps reuse kcp's existing ResourceQuota `count/<resource>.<group>`.** The quotas controller writes a controller-owned `ResourceQuota` into each tenant workspace (entries derived from the Plan + the family GVR list); kcp's `KCPKubeResourceQuota` admission enforces it — hard, synchronous, no new admission code, no fork. **Spike-gated** on two unknowns: (a) does kcp's quota evaluator discover+count bound-APIExport/CRD resources (the agent flagged discovery is "imperfect"), and (b) is the `ResourceQuota` object tenant-immutable (tenant workspace must not grant write RBAC on `resourcequotas`). | Strongest cheap enforcement available; built-in admission beats a deletable webhook. Object counts (agents, instances) are exactly what `count/` targets. Per-*org* roll-up and usage-based caps still need PP-9 layers. |
| PP-17 | **The "account" = the existing railgrid Org layer; do not invent an Account CRD.** The kcp R&D's recommended pattern (WorkspaceType boundary + separate state object + depth-pinned path resolution) is already railgrid: `organization` WorkspaceType (defaultAPIBindings + bootstrap initializer) + `Organization` CRD, orgs pinned at `root:railgrid:orgs:{uuid}`, `(org, ws)` resolved by the proxy. The billing boundary is the Org; the Meter is already keyed on it. **Add** a WorkspaceType **terminator** for billing close-out (flush final UsageRecord, block delete until exported). | Reuses a mature subsystem; the only missing piece is teardown close-out, which the terminator hook provides. |
| PP-15 | **Upstream ask: APIExport-carried admission.** An APIExport may declare admission (a `ValidatingAdmissionPolicy` or webhook ref) that kcp installs + enforces in **every** binding workspace, over its owned **and permission-claimed** resources, **immutable to the tenant**. The always-bound metering/quota export would then *gate* as well as *count*. Fork-today alternative: a compiled-in kcp shard admission plugin (`railgrid-quota`) — non-bypassable but requires patching the kcp binary. | This is the clean primitive for global, any-GVR, non-bypassable admission. It reuses the auto-bind we already do and closes the L2 tenant-edit DoS by making the policy export-owned like bound schemas. |
| PP-10 | **Providers declare what is meterable/billable via a system-scoped `MeterableResource` CR — invisible and uneditable to tenants.** It lives only in `root:railgrid:system:metering` (tenants get no binding there per P-2 hub-mediation), authored by the provider's `init` or the platform admin. It names the metric, the target resource, how it's collected, and whether it's billable. | The "what to bill/observe" metadata must be provider-authored but tenant-opaque. A separate system CR is the cleanest invisibility boundary — nothing to strip from a tenant view because tenants physically can't reach it. |
| PP-11 | **One operation: Observe.** Every count/gauge metric is collected by **platform-level, per-shard, wildcard-cluster watches** over a *discovered* GVR set — never by providers pushing. A meterable declares either a **fixed GVR** (e.g. `agents`) or a **GVR-family + its discovery source** (e.g. every per-Template `InstanceCRD`, already enumerated on the infrastructure APIExport). No `Report`/push mode. | A push mode splits the contract and lets providers self-report unaudited numbers. kcp watches are cluster-wildcarded, so one uniform watch model covers both shapes once the GVR set is discovered. High-volume *flow* metrics (tokens/USD) are the sole exception — see PP-14. |
| PP-14 | **Flow metrics are the one push exception, and are event-derived, not self-reported.** Token/USD-style metrics with no object to count are written at their event source (the agent run) into the Meter, exactly as `agents_usage` does today. | These have no countable object; a watch can't derive them. Keeping this a *narrow, named* exception (not a general push mode) preserves "everything else is Observe." |
| PP-12 | **The metering binding's observable set is controller-reconciled, not hardcoded — and system-auto-accepted.** A controller keeps the metering APIExport's `permissionClaims` = union of all `Observe` `MeterableResource` targets (identity-hash-qualified for non-core groups), and, because the binding is system-stamped, sets each account's claim `state: Accepted` directly — no tenant dialog. | This is the faithful reading of "open enough to note random provider objects": a claim list that grows automatically as providers declare meterables, accepted fleet-wide without tenant consent. Not a wildcard (kcp has none), but the same effect for the declared set. |
| PP-13 | **Plans are composed by the platform admin over the *discovered* metric catalog.** A `Plan` references metrics by name, validated against existing `MeterableResource`s. Adding a provider ⇒ new `MeterableResource` ⇒ new metric appears in the `/bonkers` picker ⇒ admin can add it to any plan. | Plans must not hardcode a metric set. Discovery-driven composition means new providers are billable/enforceable without editing the quotas/billing code. |

---

## The class: what a platform provider is

A platform provider reuses the **provider packaging** (own Go module,
micro-frontend, controllers, `provider-sdk/install` bootstrap) but wires into
the **platform plane** instead of the tenant plane:

| Aspect | Tenant provider | **Platform provider** |
|---|---|---|
| Registration | `CatalogEntry` → per-tenant `APIBinding` | admin `Provider` CR ([apis/admin/v1alpha1/types_provider.go](../apis/admin/v1alpha1/types_provider.go)), provisioned into `root:railgrid:system:providers` |
| Who enables | tenant (deliberate, per-workspace) | platform owner (once, globally) |
| Identity | acts *as the caller* (tenant token) | privileged **fleet identity**: reads its APIExport VW across `/clusters/*` |
| Cross-tenant reach | none (one workspace) | **every account**, via auto-bound export |
| UI surface | tenant "Providers" nav ([portal/src/stores/providers.ts](../portal/src/stores/providers.ts)) | **`/bonkers` admin area** ([portal/src/pages/bonkers/](../portal/src/pages/bonkers/)), gated by `/api/admin/access` |
| Backend route | `/services/providers/{name}` | `/services/admin-providers/{name}` (admin-gated) |
| Dir | `providers/<name>/` | `platform/<name>/` |

New connectivity-contract case (must be added to
[provider-connectivity-contract.md](./provider-connectivity-contract.md)): a
**fleet principal** — a privileged, cross-tenant *read* identity used only by
platform providers, distinct from the tenant-caller principal. This mirrors how
the app-actions design added the `AppRuntimePrincipal` as a new allowed caller
class; it must **not** broaden the tenant-caller contract.

---

## Presence vs Enforcement (the always-on / enable-gated split)

```
                        ┌─────────────────────────── ALWAYS ON (baseline) ──────────────────────────┐
                        │                                                                            │
 tenant providers ──emit──▶  Meter ingest (POST /services/meter)  ──▶  Postgres  usage(org,ws,metric,window,qty)
                        │                                                                            │
 every account  ◀─auto-bound  platform-metering APIExport (observe-only CR: AccountQuota/AccountBilling)
                        │                                                                            │
                        └────────────────────────────────────────────────────────────────────────┘
                                              │  (fleet VW  /clusters/*)
                        ┌─────────────────────┼──────────────────────────┐  ENABLE-GATED (mode: observe|enforce)
                        ▼                                                 ▼
              platform/quotas  (policy)                        platform/billing  (money)
       reads Meter+VW → writes Suspended                reads Meter → Stripe subscription/overage
                        └──────── payment failure → quotas suspension ───┘
```

- **Meter export + auto-bind** — installed as **platform baseline** (like
  `core.railgrid.ai`), independent of quotas/billing. Always collecting. An
  `APIBinding` must reference an existing `APIExport`, so the *export* is the
  thing that's always present; quotas/billing are just *consumers* of the data
  it makes flow.
- **Quotas / Billing** — **enable-gated** (admin `Provider` CR + `mode`
  switch). Disabling stops enforcement; the Meter keeps recording. No
  per-account churn on enable/disable, no data gap.
- **Per-account gate falls out for free:** an account with no `AccountQuota` /
  `BillingAccount` assigned is *metered but not enforced/charged* — the
  free-tier / grandfathered path, no special-casing.

---

## The auto-bind mechanism (how it reaches every account)

Two stamping mechanisms already exist in railgrid:

1. **`WorkspaceType.defaultAPIBindings`** — kcp auto-binds an export into every
   workspace of a type at creation. Used by the `organization` + `provider`
   WorkspaceTypes ([config/kcp/workspacetype-organization.yaml](../config/kcp/workspacetype-organization.yaml)).
   **Cannot** express permission-claim acceptance and only affects *new*
   workspaces.
2. **Imperative controller stamp** — a controller writes the `APIBinding` (and
   accepts claims) into each workspace. The tenant `workspace` type
   ([config/kcp/workspacetype-workspace.yaml](../config/kcp/workspacetype-workspace.yaml))
   deliberately has **no** `defaultAPIBindings`; the core `core.railgrid.ai`
   binding is stamped by the org-bootstrap controller's **Step G**
   (`EnsureChildWorkspaceRailgridBinding`,
   [pkg/hub/controllers/organization/controller.go:496](../pkg/hub/controllers/organization/controller.go#L496)).

**Use the imperative stamp** for the metering export because:

- it needs **permission claims** (to read the tenant resources it meters);
- it must **backfill** into workspaces that already exist (defaultAPIBindings
  only touch new ones);
- it's an existing code path — add one binding next to the `core.railgrid.ai`
  stamp, plus a reconcile sweep for existing accounts.

The platform provider then consumes its **APIExportEndpointSlice** (virtual
workspace) exactly as tenant providers do for their own resources — one
wildcard `LIST/WATCH` that now spans **every** account, because every account
was stamped.

---

## Provider-declared meterables + dynamic plans (the API skeleton)

This is the core of "providers carry metadata on what to bill/observe, hidden
from users; the binding is open enough to observe arbitrary provider objects;
and the admin composes plans freely."

### What "observe an arbitrary provider object" really means in kcp

kcp has **no safe wildcard permission claim** ("see every resource"). What it
*does* have, and what we use:

- **Permission claims** — an APIExport may claim resources it doesn't own, by
  `GroupResource` (+ the owning export's **identity hash** for non-core
  groups). Once a binding *accepts* the claim, those objects are served through
  the claiming export's **virtual workspace**, wildcard across `/clusters/*`.
  This is exactly how infrastructure reads each tenant's `cloud-credentials`
  Secret today.
- So "open enough to note random provider objects" = the metering export's
  **claim list is reconciled from declarations, not hardcoded**, and — because
  the metering binding is *system-stamped* (Step G), not tenant-chosen — the
  controller **auto-accepts** each new claim in every account. No tenant
  dialog, no per-provider code change.

Everything is **Observe** — a watch. The only variation is the *shape* of the
target, and both shapes resolve to the same per-shard wildcard-cluster watch:

| Metric | Target shape | How the GVR set is known |
|---|---|---|
| **number of agents** | **fixed GVR** `agents.agents.railgrid.ai` | declared directly |
| **number of instances** | **GVR-family** — each Template declares its own `InstanceCRD{Group,Version,Resource,Kind}` | *discovered*: infra's template controller **already** adds each InstanceCRD to the infrastructure APIExport ([controller/template/controller.go:184](../providers/infrastructure/controller/template/controller.go#L184)); a reconciler mirrors that list into the metering claim set |

No push. The **declaration names the shape**; a reconciler expands a family into
concrete claims + informers; the collector counts. See "Platform-level per-shard
watches" below for the kcp mechanics and the features kcp is missing.

### Platform-level per-shard watches (the kcp mechanics &amp; gaps)

A kcp watch has two dimensions — **cluster** and **resource-type**. kcp
wildcards the *cluster* dimension but **not** the resource-type dimension, and
the wildcard is served **per shard**. The metering collector is therefore
**per shard × per GVR, a wildcard-cluster informer**, keyed by the object's
`kcp.io/cluster` → `(org, ws)`. This is the *native* kcp VW-consumption pattern:
an `APIExportEndpointSlice` already lists one VW URL **per shard** (infra
consumes it via `PlatformAPIExportEndpointSlice`), so "one watcher per shard,
merge" is not exotic.

Three kcp primitives would make this clean; each has a today-workaround:

| # | Missing kcp primitive | Want | Workaround today |
|---|---|---|---|
| **1** | **Wildcard-*type* watch** — "watch all resources in group G / bearing label L", not one GVR at a time | Subscribe once for a GVR-family instead of one informer per CRD | **Claim/informer reconciler**: watch the source that mints the CRDs (Templates) and add/remove a claim + informer per GVR. Infra already tracks this list. |
| **2** | **Global cross-shard wildcard stream** | One watch across all shards | Iterate the `APIExportEndpointSlice`'s per-shard URLs, one informer each, merge. railgrid is ~single-shard today; the cache server replicates only fixed system resources, not arbitrary CRs. |
| **3** | **Native per-workspace resource counters** (like `ResourceQuota` status, per logical cluster, cross-shard) | Read a count instead of replicating every object to `len()` it | Count client-side from the informer cache. The right upstream ask for metering at scale. |

Feature **#3** is the one to push upstream (metering only wants counts, not
object replication); **#1** most improves this design's ergonomics; **#2** only
bites at multi-shard scale.

### 1. `MeterableResource` — provider metadata, tenant-invisible

Lives **only** in `root:railgrid:system:metering`. Tenants have no binding there
(P-2), so it is invisible and uneditable to them. Authored by the provider's
`init` (admin creds) or curated by the platform admin.

```go
// MeterableResource declares that a provider resource is countable/billable.
// System-scoped; never appears in a tenant workspace. Always Observe.
type MeterableResourceSpec struct {
    Provider    string          // owning provider slug (CatalogEntry)
    Metric      string          // stable, platform-unique: "agents.count", "infra.instances"
    DisplayName string
    Unit        string          // "count" | "usd-micros" | ...

    // Exactly one target shape. Both resolve to per-shard wildcard-cluster watches.
    FixedGVR  *GVRTarget        // one concrete resource
    GVRFamily *FamilyTarget     // a set discovered from a source

    Aggregation Aggregation     // Count | SumField
    SumField    string          // JSONPath when Aggregation=SumField
    Selector    *metav1.LabelSelector // optional narrowing (e.g. only Ready)

    Billable    bool            // billing charges these; all are observable
}

type GVRTarget struct {
    Group                 string // "agents.railgrid.ai"
    Resource              string // "agents"
    APIExportIdentityHash string // owning export identity (non-core groups)
}

// FamilyTarget names a source whose members become concrete GVRs the reconciler
// claims + watches, e.g. every InstanceCRD registered on an APIExport.
type FamilyTarget struct {
    // SourceAPIExport whose resource list enumerates the family
    // (infra already lists every Template InstanceCRD on its APIExport).
    SourceAPIExport       string
    APIExportIdentityHash string
    // optional filter on the family's members
    GroupPattern string
}
```

Example declarations:

```yaml
kind: MeterableResource        # in root:railgrid:system:metering
metadata: {name: agents-count}
spec:
  provider: agents
  metric: agents.count
  displayName: Agents
  unit: count
  fixedGVR:
    group: agents.railgrid.ai
    resource: agents
    apiExportIdentityHash: sha256:…   # agents' APIExport identity
  aggregation: Count
  billable: true
---
kind: MeterableResource
metadata: {name: infra-instances}
spec:
  provider: infrastructure
  metric: infra.instances
  displayName: Running instances
  unit: count
  gvrFamily:
    sourceAPIExport: infrastructure           # its resource list = every InstanceCRD
    apiExportIdentityHash: sha256:…
  aggregation: Count
  billable: true
```

### 2. Claim + informer reconciliation (the "open binding")

A controller in `platform/meter` reconciles both target shapes into one watch set:

1. **Expand** each `MeterableResource` to concrete GVRs — `fixedGVR` directly;
   `gvrFamily` by watching the source APIExport's resource list (workaround for
   kcp gap #1).
2. `platform-metering` APIExport `spec.permissionClaims` ← union of those GVRs
   (identity-hash-qualified).
3. every account's stamped `APIBinding` → set each new claim `state: Accepted`
   (system authority; no tenant consent — PP-12).
4. **per shard**, register a wildcard-cluster informer per GVR against that
   shard's metering VW URL (from the `APIExportEndpointSlice`); count per
   `(org, ws)` from the object's `kcp.io/cluster`; write to the Meter + the
   per-account status CR.

Only PP-14 flow metrics (tokens/USD) skip this — they're written at the event
source, having no object to watch.

### 3. `Plan` — composed over the discovered catalog

```go
type PlanSpec struct {
    DisplayName string
    Entries     []PlanEntry
}
type PlanEntry struct {
    Metric   string             // MUST match an existing MeterableResource.Metric
    Limit    *resource.Quantity // enforcement cap; nil = unlimited
    Included *resource.Quantity // billing: included in base price
    OveragePriceRef string      // Stripe price id for units beyond Included
}
```

```yaml
kind: Plan                      # admin-composed, system-scoped
metadata: {name: team}
spec:
  displayName: Team
  entries:
    - {metric: agents.count,    limit: "25",  included: "10", overagePriceRef: price_agents}
    - {metric: infra.instances, limit: "50",  included: "20", overagePriceRef: price_instances}
```

`/bonkers` shows a **metric picker** populated from `MeterableResource`s. Add a
new provider that ships a `MeterableResource`, and its metric appears for the
admin to drop into any plan — no code change to quotas or billing. A `Plan`
that references an unknown metric fails validation (admission/CEL).

### kcp functionality backing each piece

| Need | kcp mechanism |
|---|---|
| Observe/count a provider resource fleet-wide | APIExport **permission claim** (identity-hash-qualified) + **APIExport virtual workspace** wildcard-cluster watch, **per shard** via the `APIExportEndpointSlice` URLs |
| Watch a *dynamic family* of GVRs (no wildcard-type watch — gap #1) | reconciler mirrors a source APIExport's resource list into claims + informers |
| Accept the claim in every account without asking the tenant | binding is **system-stamped** (Step G); controller sets claim `state: Accepted` (contrast P-5 tenant consent) |
| Metadata invisible/uneditable to tenants | `MeterableResource`/`Plan` in `root:railgrid:system:metering`; **P-2 hub-mediation** denies tenants any kubeconfig reaching it |
| Open/extensible observable set | controller reconciles `APIExport.spec.permissionClaims` from declarations (APIExport spec is mutable) |
| Per-account status written back where the tenant can read it | metering export **owns** `AccountQuota`; served in each bound workspace via the VW |
| Admin composes plans over a live metric catalog | `Plan.entries[].metric` validated against `MeterableResource`; discovery = `LIST MeterableResource` |

## Prevention vs observation — where the deny lives

Observe is **reactive**: it records the 26th agent, it can't stop it. A hard cap
needs a **synchronous chokepoint on the write path**. The two compose — Observe
keeps `AccountQuota` (usage vs cap) current; admission reads it and denies
`create` at cap.

kcp's limit: **admission registration is per-logical-cluster**. A
`ValidatingWebhookConfiguration` / `ValidatingAdmissionPolicy` lives inside one
workspace. There is no kcp object that registers admission across *every*
workspace, nor one a workspace-admin can't delete. So prevention is layered:

| Layer | Mechanism | Scope | Bypass / weakness | kcp change |
|---|---|---|---|---|
| **0 — kcp ResourceQuota `count/`** (PP-16) | Controller writes a `ResourceQuota{hard: {"count/<gvr>": N}}` per workspace from the Plan; kcp's built-in `KCPKubeResourceQuota` admission enforces it. | per-workspace **object counts** (agents, instances), hard, synchronous | per-workspace only (no org roll-up); object-count only (not tokens/USD); **spike**: does kcp count CRD resources + is the RQ tenant-immutable | **none** |
| **1 — Proxy middleware** | The hub kcp proxy ([pkg/server/proxy/proxy.go](../pkg/server/proxy/proxy.go)) is already on every tenant write; add a quota gate driven by a `QuotaGate` registration (gated GVRs, `"*"` allowed). Covers what L0 can't (org roll-up, usage-based). | global for tenant traffic, any GVR, synchronous | writes not through the proxy (provider/controller-internal) | **none** |
| **2 — VAP + paramRef** | `ValidatingAdmissionPolicy` (CEL) with `paramRef → AccountQuota` (already projected by the metering binding) denies `create` at cap, in-process, no callout. | any claimed GVR | registered **per workspace** → stamp churn + tenant could edit the VAP | none (kcp has VAP) |
| **3 — Global admission** | **PP-15**: APIExport-carried, tenant-immutable admission enforced in every binding workspace; or a compiled-in shard admission plugin (fork). | truly global, any GVR, non-bypassable | needs a kcp feature (or a kcp fork) | **yes** |

Ship **Layer 1** with quotas (it catches the real case — a user creating past
cap goes through the proxy). **Layer 2** tightens to in-process CEL over the
state Observe maintains. **Layer 3** is the upstream ask that makes it airtight;
until then the residual (proxy-bypassing writes, tenant-editable VAP) is
documented, not hidden.

## Component 1 — `platform/meter` (Phase 1, foundation)

**Goal:** one always-on usage sink + the auto-bound export both later
components read.

### APIs / schema

- **Postgres** `usage` table (generalize [providers/agents/store/postgres.go](../providers/agents/store/postgres.go)
  `agents_usage`): `org_uuid, ws_uuid, metric, window_start, window_len,
  quantity, provider, updated_at`. Upsert-accumulate per window.
- **Ingest**: `POST /services/meter` (hub-owned or meter-provider-owned),
  authenticated by the reporting provider's identity; body
  `{org, ws, metric, quantity, window}`. Rate-limited, idempotent per
  `(provider, metric, window, dedupe-key)`.
- **Metric registry**: providers self-declare meters as `MeterableResource` CRs
  in `root:railgrid:system:metering` (see the section above). Start coarse:
  `agents.count` (Observe), `infra.instances` (Report), `agent.tokens`,
  `agent.usd`, `edges.nodes`, `code.repos`, `workspaces`.
- **Claim reconciler**: controller keeps the metering APIExport's
  `permissionClaims` = union of `Observe` targets and auto-accepts them in every
  stamped binding (PP-12).
- **Auto-bound export** `platform-metering.railgrid.ai` with an
  observe-only, provider-owned CR per account (schema shared by quotas/billing
  in later phases; empty/no-op in Phase 1). Permission claims: read-only on the
  resources being metered.

### Tasks

1. Scaffold `platform/meter/` from `providers/quickstart/` (module,
   `main.go` with `init`/`serve`, `provider-sdk/install` bootstrap,
   `manifest.yaml`, admin `provider.yaml`).
2. Postgres store + migration; port the `agents_usage` upsert logic into a
   shared `usage` store.
3. Meter ingest endpoint + client helper in `provider-sdk` so any provider can
   `meter.Report(...)`.
4. Add the imperative auto-bind: extend `EnsureChildWorkspaceRailgridBinding`
   (Step G) to also stamp `platform-metering`; add a reconcile sweep that
   backfills all existing tenant workspaces.
5. First two meterables end-to-end — **both Observe**, proving both target shapes:
   - **`agents.count`** (`fixedGVR`) — reconciler claims
     `agents.agents.railgrid.ai`; per-shard informer counts via the VW.
   - **`infra.instances`** (`gvrFamily`) — reconciler mirrors the infrastructure
     APIExport's InstanceCRD list into claims + informers; counts the family.
   - The **per-shard informer manager** + dynamic claim reconciler is the core of
     this phase (kcp gaps #1/#2 workaround).
   - Flow metrics `agent.tokens`/`agent.usd` (PP-14) written at the source via the
     existing `AddUsage` ([providers/agents/store/store.go](../providers/agents/store/store.go)).
6. `/bonkers` read-only **Usage** section: per-org/per-workspace usage table
   (no enforcement yet).

### Acceptance

- Every existing + new account carries the `platform-metering` binding.
- Agent runs land rows in the shared `usage` table.
- Admin can view fleet-wide usage in `/bonkers`; tenants see nothing new.

---

## Component 2 — `platform/quotas` (Phase 2, policy)

**Goal:** finally wire the tested-but-unused [pkg/hub/quota/quota.go](../pkg/hub/quota/quota.go)
library, add usage-based caps, and auto-suspend on breach behind a mode switch.

### APIs / schema

- `Plan` (cluster-scoped, system): named tier → caps per metric
  (`workspaces`, `agent.usd/month`, `edges.nodes`, …) + structural caps
  (orgs-per-user, workspaces-per-org — reuse `quota.EffectiveOrgsPerUser` /
  `EffectiveWorkspacesPerOrg`).
- `QuotaAssignment` (system): bind a `Plan` to an Org/Workspace + per-account
  overrides. (Existing `User.Spec.OrgQuota` /
  `Organization.Spec.WorkspaceQuota` become override inputs.)
- `AccountQuota` — the **auto-bound, per-account** CR the quotas controller
  writes *into the tenant workspace* via the fleet VW: current usage vs cap +
  `Suspended`/`OverQuota` condition. Tenant-visible (drives an "80% of plan"
  banner), tenant-**un-removable-meaningfully** (deletion is the residual DoS in
  PP-9).

### Enforcement chokepoints

- **Structural** (orgs/workspaces): call `quota.CheckOrgQuota` /
  `CheckWorkspaceQuota` on the live create paths
  ([pkg/hub/restapi/orgs.go](../pkg/hub/restapi/orgs.go), workspace create) —
  these helpers already exist and are tested but wired **nowhere** today.
- **Usage-based** (tokens/USD/nodes): controller reads Meter + fleet VW; on
  breach, writes `Suspended` to `AccountQuota` and (mode=`enforce`) trips a
  provider-side `checkBudget`-style deny — mirroring the agents
  `Suspended`-on-budget pattern ([providers/agents/api/run.go](../providers/agents/api/run.go)),
  but fleet-wide.
- **Object-count caps** (agents, instances): synchronous **admission** per the
  layered model above — Layer 1 (hub-proxy `QuotaGate`) with quotas; Layer 2
  (VAP + `AccountQuota` paramRef) to tighten; Layer 3 (PP-15) is the airtight
  upstream ask. `AccountQuota` is the shared state all layers read.

### Mode / dry-run

- `mode: observe | enforce` on the quotas provider config.
- `observe`: compute + surface "who *would* be suspended" in `/bonkers`; write
  nothing punitive.
- `enforce`: write `Suspended`, deny at chokepoints.

### Tasks

1. Scaffold `platform/quotas/` (class-PP provider).
2. `Plan` / `QuotaAssignment` / `AccountQuota` schemas + export (reuse metering
   export's auto-bind, or add claims for write-back).
3. Controller: fleet VW watch → evaluate Meter + caps → reconcile `AccountQuota`
   condition; mode gate.
4. Wire `quota.Check*` into `restapi` create paths.
5. `/bonkers` **Quotas** section: all orgs' plan + live usage vs cap; edit
   plans/overrides; dry-run preview.
6. Tenant portal: read `AccountQuota` → "N% of your plan" banner.

### Acceptance

- New org/workspace creation is rejected past cap (structural).
- Over-cap usage flips `AccountQuota` to `Suspended` in `enforce`; only
  previews in `observe`.
- Tenant sees their own usage-vs-plan; cannot raise their own cap.

---

## Component 3 — `platform/billing` (Phase 3, money)

**Goal:** plan/seat Stripe subscriptions; Meter feeds overage; payment failure
loops back to quotas.

### APIs / schema

- `BillingAccount` (system): Org ↔ Stripe `customer`, subscription state,
  payment status.
- `PlanPrice`: map a quotas `Plan` → Stripe `Price`/`Product` (subscription),
  plus optional overage `Price` per metric.
- Stripe secret: stored in the billing provider's **own minted sub-workspace**
  (the admin `Provider` CR already isolates a per-provider workspace +
  kubeconfig for exactly this — see [apis/admin/v1alpha1/types_provider.go](../apis/admin/v1alpha1/types_provider.go)).

### Flow

1. Owner maps `Plan` → Stripe `Price`; assigns a `BillingAccount` to an Org.
2. Controller ensures a Stripe customer + subscription per `BillingAccount`.
3. Periodic job aggregates Meter overage per account → reports to Stripe
   (metered `Price` items on the subscription).
4. Stripe invoices; **signed webhook** (`/services/admin-providers/billing/webhook`)
   updates `BillingAccount` payment status.
5. **Payment failure → quotas suspension**: billing writes a signal that
   `platform/quotas` reads and trips the account's `Suspended` condition
   (dunning grace configurable).

### Mode / dry-run

- `mode: observe | enforce`: `observe` computes invoices/overage and shows them
  in `/bonkers` **without** creating Stripe charges; `enforce` bills.

### Tasks

1. Scaffold `platform/billing/`.
2. `BillingAccount` / `PlanPrice` schemas.
3. Stripe client (customers, subscriptions, metered usage, webhooks) with the
   secret mounted from the provider sub-workspace.
4. Aggregation job Meter → Stripe; webhook handler (signature-verified).
5. Feedback edge: payment-failure → quotas `Suspended`.
6. `/bonkers` **Billing** section: accounts, plan↔price map, invoices, payment
   state, dunning.

### Acceptance

- Org gets a Stripe subscription on plan assignment.
- Overage from the Meter appears on the Stripe invoice.
- Failed payment (past grace) suspends the account via quotas.
- `observe` mode shows would-be invoices with zero Stripe charges.

---

## Cross-cutting work (touched by all phases)

- **Connectivity contract**: add the **fleet principal** case to
  [provider-connectivity-contract.md](./provider-connectivity-contract.md) and
  `AGENTS.md`; it is read-mostly, cross-tenant, platform-owned, and must not
  broaden tenant-caller or controller privileges.
- **Admin routing**: add `/services/admin-providers/{name}` +
  `/ui/admin-providers/{name}` proxies, gated by the same `/api/admin/access`
  check the `/bonkers` tree uses ([portal/src/stores/admin.ts](../portal/src/stores/admin.ts)).
  Tenants must not be able to reach these.
- **Provider registry**: teach [pkg/hub/providers/registry.go](../pkg/hub/providers/registry.go)
  about the platform class (or a parallel `platformregistry`) so platform
  providers heartbeat + route without appearing in the tenant catalog.
- **Bonkers sections**: new `UsageSection.vue`, `QuotasSection.vue`,
  `BillingSection.vue` under [portal/src/pages/bonkers/](../portal/src/pages/bonkers/).

---

## What we reuse from kcp (grounded in a kcp-side R&D pass)

A read-only discovery pass over `kcp-dev/kcp` confirmed there is **no**
metering/quota-rollup/billing/entitlement code (greenfield), but the hook points
exist:

| kcp capability | We use it for | Status |
|---|---|---|
| **ResourceQuota `count/<resource>.<group>`** (`KCPKubeResourceQuota`, per-workspace) | hard per-workspace object caps (agents, instances) — PP-16 | reuse; **spike** CRD counting + RQ immutability |
| **APIExport virtual workspace** wildcard list/watch | the whole Observe model (PP-11/12) | reuse as-is |
| **`organization` WorkspaceType + `Organization` CRD**, depth-pinned orgs | the account/billing boundary — PP-17 | reuse; add **terminator** for close-out |
| **Audit webhook** (`WithAuditEventClusterAnnotation` already stamps the workspace) | request-volume meters + the day-1 spike | reuse for observability meters (lossy → not billing-grade) |
| **Generic admission plugin** registry (`pathannotation`, `permissionclaims` as templates) | the `railgrid-quota` plugin — prevention Layer 3 | build in the kcp binary (railgrid already builds kcp) |
| **`kubequota` per-logical-cluster evaluator** pattern | template for any custom per-workspace evaluator | reference |
| **CEL** (as in `ValidatingAdmissionPolicy`) | `quantity`/`dimensions` on `MeterableResource` (adopted from the kcp R&D's `MeteringConfig`) | adopt |

Must still build (both R&D passes agree): the `UsageRecord` schema,
object-hours integration (event-sourced create/delete + reconciliation samples),
cross-shard rollup, tamper-evidence (hash-chain), idempotency keys, and
Plan/Entitlement. Account **resolution** is *not* on this list — railgrid already
has it (PP-17).

### Spikes to run first (before committing the phases)

1. **ResourceQuota counts CRDs?** Write `count/agents.agents.railgrid.ai: 1`
   into a workspace, create 2 agents, confirm the 2nd is denied — and that the
   tenant cannot edit the `ResourceQuota`. Gates PP-16.
2. **Audit-webhook → collector** with one hardcoded meter — proves the request
   pipe end-to-end with near-zero code.

## Phasing summary

| Phase | Ships | Enforcement | Depends on |
|---|---|---|---|
| **1 — Meter** | always-on usage sink, auto-bind into every account, agents emitting, `/bonkers` usage view | none (observe-only) | — |
| **2 — Quotas** | Plans, structural + usage caps, auto-suspend behind `mode`, tenant "% of plan" | structural live; usage gated by `mode` | Phase 1 |
| **3 — Billing** | Stripe subscriptions, overage from Meter, webhooks, payment→suspend | gated by `mode` | Phases 1–2 |

Each phase is independently shippable and reversible (disable ⇒ Meter keeps
recording, no data gap).

---

## Open questions

1. **Meter ownership** — hub-owned endpoint vs a dedicated `platform/meter`
   provider pod. Leaning provider (isolation, own Postgres), but the ingest
   route is hub-proxied.
2. **Metric taxonomy &amp; units** — canonical metric names/units registry; who
   validates a provider's declared `meters:`.
3. **Aggregation window** — Meter granularity (hourly?) vs billing period;
   retention.
4. **Global admission (PP-9/PP-15)** — pursue APIExport-carried admission
   upstream, or accept the shard-plugin fork? Until Layer 3, the residual is
   proxy-bypassing writes + tenant-editable per-workspace VAP.
5. **Seat definition** for plan/seat billing — Memberships? active users? —
   needs a metric.
