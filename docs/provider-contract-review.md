# Provider contract review — the three pillars, and where every provider deviates

**Status:** Review, 2026-09-19. Describes the tree as it is; nothing here is a
plan unless the section says so.
**Reads as a delta on:** [providers.md](./providers.md),
[provider-connectivity-contract.md](./provider-connectivity-contract.md),
[provider-actions.md](./provider-actions.md),
[cross-provider-simplification.md](./cross-provider-simplification.md),
[AGENTS.md §5](../AGENTS.md).

---

## Status — what this audit has already moved

**This is a dated audit and is not rewritten as the tree changes.** The
remediation it produced,
[roadmap/provider-contract-remediation.md](./roadmap/provider-contract-remediation.md),
carries the authoritative per-section status line; read it before treating any
finding below as open. As of **2026-09-20**, on branch
`provider.contracts`:

**Closed.**

- **The four Pillar 2 dialects.** `provider-sdk/dataplane` owns the grammar,
  the two gates and the limits; `provider-sdk/serve` owns the server layout.
  Every in-tree provider serves through `serve.New`, and
  `hack/verify-provider-contract.mjs` runs in `make verify` with **no
  `adhoc-rest` exceptions** — so the ad-hoc `/api/*` tenant surfaces (§3.2,
  §3.6, §3.7, §3.8) are gone, not grandfathered.
- **The `invoke` verb.** The grant is `create` on `{resource}/{verb}`
  everywhere (`dataplane.SSARVerb`).
- **`spec.virtualWorkspace`,** retired from the CatalogEntry type.
- **The "one manifest, three copies" problem** (§"Cross-cutting"). A permission
  claim is written once, in `manifest.yaml`; the APIExport is generated; the
  chart copies are verified outputs.
- **The doc contradictions of Part 2,** folded into the amended contract
  (§0.1 of the plan) and into the docs this review names.
- **Provider-minted identities** (§3.5 edges, §3.6 agents): the hub identity
  service exists and both providers consume it.
- **Timer loops and readiness gaps** called out per provider: `CanSend` is now
  mandatory, `/readyz` comes from `vwhealth`, and every provider's write loops
  are leader-elected.

**Open.**

- §3.3 planner, §3.9 databricks and §3.11 factory live in `railgrid/providers`
  and have **not started** (plan §3, §7).
- §3.7 app-studio's largest deviation is partly done: Cut D (conversations and
  the project source tree) has not started.
- kuery and app-studio still mint their own workspace/project identities and
  still hold `serviceaccounts`/`clusterroles`/`clusterrolebindings` claims.
- Six providers still hold `secrets` claims that have not been narrowed to
  provider-owned material (review X-4).

---

## Why this doc exists

The provider docs pin a lot of decisions, but they are spread over six
documents written at different times, three of them "design drafts", and the
tree has moved past several of them. Someone writing a provider today has to
reconcile the docs with each other and then with the code. This review does
that once: it states the contract in three pillars, records where the docs
themselves are stale, and audits every provider (seven in-tree, three in
`railgrid/providers`) against the pillars with file anchors.

The short version:

- **Pillar 1 (KRM via APIExport/APIBinding)** is the healthy core. Every
  provider exports an APIExport through `provider-sdk/install`. But two of the
  three AI providers keep tenant-visible durable state in Postgres, two
  providers export a kind they never implement, and the reference provider
  exports a kind it never reconciles.
- **Pillar 2 (REST only as data-plane verbs on bound resources)** has one
  reference implementation (infrastructure `dataplane/`), one conforming
  action server (code), and **four different dialects** of the same idea in
  edges, agents, planner and app-studio. Two providers use verb `invoke` where
  the hub grants `create`. Two providers serve their whole tenant surface as
  ad-hoc `/api/*` CRUD over bound CRs or a database.
- **Pillar 3 (custom-element micro-frontend)** is the most consistent pillar.
  Every portal registers `<railgrid-provider-{name}>`, uses `providerFetch`,
  and ships in sync with portalkit. The deviations are data-path ones: four
  portals never use the kube client and drive everything through their own
  REST.

---

## Part 1 — The contract

What follows is the contract as the hub actually enforces it today, tightened
where the docs were vague. Each rule names the code that backs it. Rules marked
**(proposed)** are not enforced anywhere yet and are recommended here.

### Pillar 1 — APIs are kcp APIs, delivered by APIExport and consumed by APIBinding

1. **Every tenant-facing, persistent resource is a kcp API.** Go types under
   `providers/<name>/apis/v1alpha1`, generated into APIResourceSchemas
   (`make codegen-<name>-provider`), applied together with one APIExport named
   `<name>.providers.railgrid.ai` by the provider's own `init` command through
   `provider-sdk/install.Bootstrap` (`provider-sdk/install/install.go:138`).
   The hub only creates the workspace, the `provider` ServiceAccount and the
   kubeconfig Secret (`providers/quickstart/provider.yaml:1-7`,
   `pkg/hub/providers/provision.go`).
2. **Tenants consume it through an APIBinding created by the hub's Enable
   flow**, which runs as kcp-admin after checking membership and dependencies
   (`pkg/hub/restapi/providers_enable.go:20-26,129,186`). The claims a tenant
   accepts are the `tenantScoped` claims declared on the CatalogEntry;
   rejected claims go through as `state: Rejected`. *providers.md decision #10
   says "the portal calls kcp as the user"; that is not what shipped.*
3. **Desired state and durable status live in the object.** Anything a tenant
   can see or that a reconcile must recover from is a `spec`/`status` field on
   the tenant's object, or a provider-private kind in the provider workspace.
   Not Postgres, not SQLite, not a PVC, not process memory (AGENTS.md §5.8).
   Two carve-outs the tree already relies on and this review recommends
   pinning **(proposed)**:
   - *Transient artifacts* (an uploaded bundle, a git snapshot) may sit on a
     PVC while the CR carries the reference and digest, provided the artifact
     is consumed-and-deleted or swept on a fixed TTL and nothing is lost if it
     is (code `commitbundle/store.go`, `actions/snapshots.go`).
   - *Projection kinds*: if a provider must keep high-volume execution data
     outside kcp (chat turns, run transcripts), the tenant-visible identity of
     that data is still a CR (`Session`, `Run`), the CR's status is its
     projection, and a finalizer purges the store when the CR goes
     (app-studio `controller/session/controller.go:11-29`).
4. **Controllers are watch-driven multicluster-runtime reconcilers** on
   `provider-sdk/apiexportprovider` (one workqueue per consuming workspace,
   `req.ClusterName` selects the client), running under
   `provider-sdk/leaderelection.Run`, with readiness attached to the
   provider's watch state through `provider-sdk/vwhealth` and heartbeat
   `CanSend` gated on it. Reference: `providers/code/controller_manager.go:398-468`,
   `providers/code/main.go:173-213`. `RequeueAfter` is allowed for exactly
   three things: pacing a system that cannot be watched, backing off a failed
   call, and **(proposed, from infrastructure)** waking at a computed
   lifecycle deadline. A blanket `resyncPeriod` is not one of them.
5. **The provider identity never leaves its workspace.** Cross-workspace reach
   is only the APIExport virtual workspace plus accepted claims. No admin
   kubeconfig at serve time; `init` is the only admin-credentialed step.
   Per-request work happens as the caller (Pillar 2, rule 4).

### Pillar 2 — REST is only for non-standard, non-persistent verbs, in the data-plane shape

The hub gives every provider one backend origin behind
`/services/providers/{name}/*` (`pkg/hub/providers/proxy.go:111`). What may be
served there is closed:

| Class | Shape | Auth | Reference |
|---|---|---|---|
| **(a) Data-plane verb** | `/{root}/clusters/{clusterID}/{resource}/{name}/{verb}[/{tail}]` and `.../{name}/components/{c}/{verb}[/{tail}]` | caller bearer; two gates | infrastructure `dataplane/handler.go:521-576` |
| **(a′) Action** | `/actions/clusters/{clusterID}/{resource}/{name}/{action}/{version}`; body `{"input":{}}`; `actionwire` envelope | caller bearer; two gates | code `actions/server.go:74`, databricks |
| **(b) MCP projection** | `/mcp`, `/mcp/sse` | caller bearer + `X-Railgrid-Cluster` for addressing | `pkg/hub/mcpaggregate/enumerator.go:80-85` federates exactly this path |
| **(c) Health** | `/healthz`, `/readyz` | none | `provider-sdk/vwhealth.Handler` |
| **(d) Browser OAuth** | `/oauth/{provider}/{start,callback,config}` | signed state; popup may `postMessage` to its opener | code `oauthgithub/oauth.go:148-152` |
| **(e) Hub-only** | `/workload-identities/*` | refused by the proxy for callers | `pkg/hub/providers/proxy.go:463-471` |
| **(f) Agent tunnel (proposed)** | `/agent/clusters/{clusterID}/{resource}/{name}/proxy` | join token or edge SA + SAR | edges `agent_proxy_builder_v2.go:90-242` |
| **(g) Signed inbound webhook (proposed)** | `/webhooks/{kind}/{clusterID}/{name}/{token}` | HMAC token, acts as provider SA | agents `api/server.go:250-253` |

Rules:

1. **Nothing else.** No `/api/*`. If a UI needs to list, create or edit a
   thing, the thing is a bound CR and the UI reads it over `/clusters/{id}`
   (Pillar 3). A backend route that mirrors a CR is a deviation even when it
   is authorized correctly.
2. **The bearer is the only trust root.** The hub strips inbound `X-Railgrid-*`
   and re-injects `X-Railgrid-User`, and the cluster ID as both
   `X-Railgrid-Tenant` and `X-Railgrid-Cluster` (`pkg/hub/providers/proxy.go:124-176`).
   Providers may read those headers for *addressing* and *labels*; they must
   not derive authorization from them. Where the path carries the cluster
   ID, the path wins and must equal the header (code `actions/server.go:79`).
3. **Two gates, as the caller, on every verb.** Gate 1: the caller can `get`
   the addressed resource (a real GET with the caller's token, which also
   yields the object for UID pinning). Gate 2: SelfSubjectAccessReview for
   **`create`** on the virtual subresource `{resource}/{verb}`, name-scoped.
   The hub materializes grants as exactly that rule
   (`pkg/hub/serviceaccounts/workload_identity.go:435-439`), so any other verb
   string breaks workload identities.
4. **Per-request tenant client, credential dropped.** Build a client for
   `<hub>/clusters/{clusterID}` from the provider kubeconfig's host and CA
   only, with the caller's bearer (`providers/code/tenant/client.go:56-60`,
   `providers/infrastructure/tenant/client.go:54-78`). Cache per
   (cluster, token hash).
5. **Cross-provider access is by binding, not by string.** Provider A reaches
   provider B only through B's bound CRs and B's data-plane verbs, as the
   caller, resolving the target from the binding or from a `status` field B
   publishes. No foreign credential, no foreign Secret read, no hardcoded
   `/services/providers/<b>/...` format string (contract 3,
   cross-provider-simplification X-4/X-8).
6. **No dev bypass in release binaries.** `RAILGRID_DEV_ALLOW_TENANT_QUERY`
   (bearer or tenant from the query string) exists in code, infrastructure
   and kuery. It should be compiled out or removed **(proposed)**.

### Pillar 3 — The UI is a custom element loaded by the host

1. **Bundle.** `providers/<name>/portal/` builds an IIFE `main.js` (classic
   script), embedded via `assets.go`, served by the hub from the embedded FS
   at `/ui/providers/<name>/`, SRI-pinned from
   `CatalogEntry.status.ui.mainJSIntegrity`
   (`portal/src/providers/providerScriptLoader.ts`). Lazy chunks load from the
   same prefix. No other script origin (kuery's runtime `<script>` injection
   of cytoscape needs checking against this).
2. **Element.** `main.js` registers `<railgrid-provider-<name>>` and optionally
   `<railgrid-dashboard-tile-<name>>`. The host sets `railgridContext` as a
   JS property and re-pushes it on every change
   (`portal/src/providers/providerContext.ts`, `ProviderFrame.vue:387-390`).
   The element renders into light DOM so the portal stylesheet cascades;
   `ensureRailgridUIStyles()` guards the fallback.
3. **Transport.** Every hub request goes through `railgridContext.fetch`, via
   the vendored `portalkit/tenant.ts providerFetch(ctx)`. The host allowlist is
   the provider's own `/services/providers/<name>/` and `/ui/providers/<name>/`,
   `/clusters/`, `/api/orgs/<org>/`, and GET `/api/providers`
   (`portal/src/providers/providerFetch.ts:57-63`). `ctx.token` is deprecated
   and must not be read except as the portalkit fallback.
4. **Data.** Bound CRs are read and written with `portalkit/kube.ts
   createKubeClient({fetch, cluster: ctx.tenant})` over `/clusters/{id}`.
   Backend calls are limited to the Pillar 2 classes.
5. **Navigation.** Dispatch `railgrid-navigate`; build links with
   `portalkit/navigation.ts portalHref`. Sidebar sub-items come from
   `spec.ui.children` in the manifest.
6. **No iframe, no postMessage bridge to the host.** Two exceptions the tree
   already has and this review recommends naming **(proposed)**: an OAuth
   popup posting its result to `window.opener` (code), and a sandboxed
   preview iframe that is *content*, not a UI bridge (app-studio, listed in
   the hub CSP `frame-src`).
7. **Portalkit is copy-synced, never edited.** `make sync-portalkit` /
   `make verify-portalkit`. App Studio's hot-reloadable bootstrap
   (`element.ts:185-198`, `providerScriptLoader.ts:17`) is a better pattern
   than the docs describe and should become the default for new portals.

### Cross-cutting — one manifest, three copies

The CatalogEntry exists in `manifest.yaml`, `deploy/chart/templates/catalogentry.yaml`,
and its claims a third time in `init_cmd.go`; infrastructure adds a fourth
apply path (the operator embeds `manifest.yaml`,
`providers/infrastructure/operator.go:55`). `docs/roadmap/provider-authoring-plan.md`
§3.1 proposes collapsing this; until then the code provider's
`actions_catalog_test.go` (manifest/chart parity + digests) is the pattern to
copy.

---

## Part 2 — Where the docs contradict the code

These are contract-level defects: a new author reading the docs is told
something the code does not do.

| # | Doc says | Code does | Fix |
|---|---|---|---|
| D1 | `spec.virtualWorkspace.url` is routed at `/services/providers/{name}/vw/*` (`types_catalogentry.go:111-116`, providers.md decision #6, quickstart `manifest.yaml:45`) | Parsed into the registry (`controller.go:546-551`) and never used for routing; cross-provider-simplification X-3 already retires it | Delete the field or mark it deprecated in the type doc; drop from quickstart manifest |
| D2 | Enable = "portal calls kcp as the user to create the APIBinding" (providers.md #10) | Hub creates it as kcp-admin after a membership check (`providers_enable.go:20-26`) | Reword #10 |
| D3 | Action verb gate is `create` on `{resource}/{action}` (provider-actions.md) | code and planner SSAR verb **`invoke`** (`providers/code/actions/server.go:215`, `planner/internal/server/actions.go:70`); hub grants `create` (`workload_identity.go:438`) | Change both providers to `create` |
| D4 | connectivity-contract.md contract-2 table lists `mcp`, `kubernetesedges`, `serveredges` built-ins as violations | Built-ins were removed in #435 | Delete those rows and "Known divergences" #1 |
| D5 | quickstart `README.md:27-28` describes a `postMessage` handshake; `:113-147` says heartbeat, kubeconfig Secret, Helm chart are "not in this iteration"; kind `ProviderCatalogEntry` | Custom element with property push; all three exist; kind is `CatalogEntry` | Rewrite the README against `element.ts`, `main.go`, `deploy/chart/` |
| D6 | providers.md §"Example: a minimal provider" points at `examples/provider-hello/` | Directory does not exist; quickstart has no controller | Point at `providers/quickstart/` once it has one |
| D7 | AGENTS.md §5.7 lists `kuery`/`quickstart` under the "hub-proxy model" and `code`/`edges`/`infrastructure` under "cluster-in-path" as two sanctioned auth models | Only cluster-in-path matches the contract; hub-proxy model = REST-first, the deviation this review flags | Retire the "two models" framing; one model, REST only for Pillar 2 classes |
| D8 | infrastructure `manifest.yaml:98-104` describes per-template APIExport mutation | Template controller no longer touches the APIExport (`controller/template/controller.go:19-23`) | Prune |
| D9 | kuery `README.md` requires `apiExport.edgesIdentityHash` and an `edges` claim; chart says "Phase 1 skeleton" | No such value, no such claim (`init_cmd.go:28-35`) | Rewrite |
| D10 | code chart README "four schemas", README "eleven actions", manifest header "six kinds" | eight, twelve, eight | Fix counts |
| D11 | edges `README.md:3-5` lists two kinds; `manifest.yaml:7-8` "scales horizontally" | seven kinds; chart `replicaCount: 1` | Fix |
| D12 | `providers/secrets/portal/dist/` is a tracked, built provider bundle (4 files, added in #541) | No source, no manifest, no binary | Delete |
| D13 | edges `config/kcp/apiexport-edges.railgrid.ai.yaml` names the export `edges.railgrid.ai` | Export is `edges.providers.railgrid.ai` (`init_cmd.go:24`) | Regenerate or delete |

---

## Part 3 — Provider-by-provider

Legend: ✅ conforms · ⚠️ deviates · 📄 documented exception · ❌ deviates
materially. "SDK" columns are whether the package is imported in non-test Go.

> **The audit text below is as of 2026-09-19 and is deliberately not
> rewritten** — it is the record of what was found. Remediation is tracked
> per provider by the **Status 2026-09-20** column in the matrix and the
> **Status 2026-09-20** note that opens each subsection. Where the two
> disagree, the status note is the current tree and the prose is history.
> [roadmap/provider-contract-remediation.md](./roadmap/provider-contract-remediation.md)
> remains authoritative for what is planned.

### 3.1 Matrix

| Provider | Kinds | Tenant state in KRM | apiexport-provider | leader election | vwhealth + CanSend | Backend routes | REST auth | Cross-provider | UI data path | Status 2026-09-20 |
|---|---|---|---|---|---|---|---|---|---|---|
| quickstart | Greeting (YAML only, unreconciled) | n/a | ❌ none | ❌ | ❌ | `/api/hello`, `/api/stream` ad-hoc | none | – | ❌ REST only, no kube client | ✅ **closed** (plan §1): typed kind + reconciler, `greet` verb through `dataplane.Gate`, kube-client portal, `serve.New` |
| code | 8 | ✅ + 📄 transient bundles | ✅ | ✅ | ✅ | actions + mcp + oauth | ✅ two gates (verb `invoke` ⚠️) | – | ✅ kube client | ✅ **closed** (plan §2): verb is `create`; on `dataplane` + `serve`; `secrets` claim label-scoped to `railgrid.ai/owner: code` (X-4 closed); `repositories/commit/v1` action |
| infrastructure | Template, Instance | ✅ | ✅ | ✅ (2 leases) | ⚠️ vwhealth, no CanSend | dataplane + mcp + hub-only | ✅ (SSAR only on exec ⚠️) | 📄 `<instance>-registry` string | ✅ kube client | ✅ **closed** (plan §4): no admin-credential serve path, per-verb gate, one CatalogEntry source, `CanSend` wired |
| edges | 7 | ✅ (events in memory ⚠️) | ✅ | ❌ active-active by design | ❌ static healthz | edgeproxy + agent + mcp + `/catalog` | ⚠️ single wildcard verb `proxy`; `?token=` | M7 mints SAs | ✅ kube client | ✅ **closed** (plan §5): leader-elected reconcilers, per-verb SAR on the shared grammar, no bearer in query strings, hub-minted identities. `spec.edgeProxyAccess` and its Enable-time grant **deleted 2026-09-20** |
| agents | 5 | 📄 runs/transcripts in Postgres | ✅ | ✅ | ✅ | ❌ ~40 `/api/*` CRUD + s2s + webhooks | ✅ caller client | ❌ hardcoded infra path, mints SAs over infra group | ❌ REST only | ✅ **closed** (plan §8): the `/api/*` surface is gone (`adhoc-rest` has no exception), identities come from the hub service; `secrets` claim label-scoped (X-4 closed) |
| app-studio | 3 | 📄 conversations in Postgres, ❌ source tree on PVC | ✅ | ❌ | ❌ | ❌ ~95 `/api/projects/*` | ⚠️ actor from `X-Railgrid-User` | ❌ all three §2.1 findings hold | ❌ REST-first | ✅ **closed** (plan §9): Cuts A–D landed — composition is hub-minted scoped identities under tenant-consented `dependencies[].composes`; commits go through the code provider's `commit/v1` action; Session status carries turn count and activity; the source-tree ledger lives on `Project.status.workspace` and the PVC is a rebuildable cache; `secrets` claim label-scoped (X-4 closed) |
| kuery | SavedView (YAML only, dead) | ❌ tenant map in SQL | ✅ | ❌ hand-rolled leases | ❌ static healthz | ❌ `/api/query`, `/api/edges`, `/api/status` | ❌ header-only, no bearer check | ⚠️ hand-composed edgeproxy path | ❌ REST only | ✅ **closed** (plan §6): on `serve` + `dataplane` with real bearer checks; the edge-watch identity is hub-minted (owner: the tenant's kuery APIBinding, clause E composition accepted at Enable) and the export carries no claims; engagement runs on every replica sharded by `provider-sdk/sharding` Leases; `edgeProxyAccess` dropped |
| databricks | 3 | ✅ | ✅ | ✅ | ⚠️ home-grown + 5s ticker | actions + mcp + ❌ `/api/v1/discovery`, `/api/v1/registrations`, `/api/status` | ✅ two gates (`create`) | – | ✅ kube client; ⚠️ `ctx.token` | ✅ **applied in the `railgrid/providers` checkout** (plan §3.2): on `serve.New` + `dataplane`, ad-hoc `/api/v1/*` and `/api/status` gone, `CanSend` heartbeat; **pending** a published `provider-sdk` pin (the checkout carries a local `replace`) |
| planner | 3 + private ActionReceipt | ✅ | ✅ | ✅ | ✅ | actions + mcp + ❌ `/api/onboarding`, `/api/connections/*/projects` | ✅ two gates (verb `invoke` ⚠️) | – | ⚠️ hand-built `/clusters` paths | ✅ **applied in the `railgrid/providers` checkout** (plan §3.1): verb is `create`, ad-hoc `/api/*` gone, `serve.New` + `dataplane`; **pending** a published `provider-sdk` pin |
| factory | 5 (YAML + Python-generated) | ✅ + ⚠️ artifacts on PVC | ✅ | ✅ | ❌ no heartbeat; served `/readyz` static | ❌ `/api/line-setup`, `/api/worker-setup` (cluster in body) | ⚠️ caller bearer, no header cross-check | ✅ by binding + planner actions as per-workspace SA | ✅ kube client | ✅ **applied in the `railgrid/providers` checkout** (plan §7): heartbeat with `CanSend`, `/api/line-setup` and `/api/worker-setup` moved onto the grammar behind two gates, `serve.New`, typed `apis/v1alpha1` with a schema-equivalence gate replacing the Python DSL; **pending** a published `provider-sdk` pin and the controllers' move off `unstructured` (that repo's `docs/typed-apis.md`) |

### 3.2 quickstart — the reference provider teaches the wrong thing

Paths under `providers/quickstart/`.

> **Status 2026-09-20 — closed** (plan §1). Every finding below is
> remediated: `apis/v1alpha1` carries a typed `Greeting` with a reconciler
> under `leaderelection.Run` stamping `status.observedAt`; `/api/hello` and
> `/api/stream` are replaced by `POST /dataplane/clusters/{id}/greetings/{name}/greet`
> behind `dataplane.Gate` + `dataplane.Serve`; the portal reads the CR with
> `createKubeClient`; `/readyz` comes from `vwhealth` and heartbeat `CanSend`
> from the same readiness; the server is `serve.New`.
> `hack/verify-provider-contract.mjs` passes it with no exception entry.

- **Pillar 1 hollow.** Exports `Greeting` from a YAML schema
  (`deploy/chart/files/schemas/greetings...yaml`) with no Go types, no
  reconciler, no kubeconfig mounted (`deploy/chart/values.yaml:2-5`); nothing
  ever writes `status.observedAt`. `go.mod` has no controller-runtime.
- **Pillar 2.** `/api/hello` and `/api/stream` (`main.go:128-177`) are
  free-form REST; `/api/hello` echoes the inbound `X-Railgrid-*` headers and a
  token fingerprint, which shows header-reading as the identity model.
- **Pillar 3.** The element uses `providerFetch` correctly (`element.ts:123`)
  but never touches the CR; `portalkit/kube.ts` is vendored and unused. Hand
  rewrites the service base (`element.ts:106`) instead of `serviceBase()`.
- Manifest hygiene is clean (claims match three ways). README is stale (D5).
- **What conform looks like:** `apis/v1alpha1/greeting_types.go`, one
  multicluster reconciler stamping `status.observedAt` under
  `leaderelection.Run`, `/readyz` from `vwhealth`, and a portal that lists and
  creates Greetings with `createKubeClient`. Delete `/api/*`. This is the
  single highest-leverage change in the review: everything else is copied
  from here.

### 3.3 code — the reference for controllers and actions, with one real bug

Paths under `providers/code/`.

> **Status 2026-09-20 — closed** (plan §2). The verb is `create` on
> `repositories/{action}`; the hand-written path parse and gates are replaced
> by `dataplane.Gate` + `dataplane.Serve` (UID/spec pinning kept); the server
> is `serve.New`. **Still open:** the `secrets` permission claim is
> resource-wide (review X-4) — `hack/verify-provider-contract.mjs` reports it
> as a `claim-selector` violation.

- **Conforms** on all three pillars: eight kinds, `apiexportprovider` +
  `leaderelection` + `vwhealth` + `CanSend` (`controller_manager.go:398-468`,
  `main.go:173-213`); no `/api/*`; twelve catalogued actions on the
  `/actions/clusters/...` grammar; UI on the kube client with
  `providerFetch`; manifest/chart/init claims identical, parity enforced by
  `actions_catalog_test.go`.
- **❌ D3:** gate 2 checks verb `invoke` (`actions/server.go:215`); the hub
  grants `create`. Fix the string, `server_test.go:81`, README.
- **📄** `stage_snapshot` is served (`actions/server.go:74,104`) but not in the
  catalog; on-disk bundle and snapshot stores are transient artifacts. Both
  are explained only in the README; move the rationale into
  `code-provider-architecture.md` and pin the carve-out in the contract.
- **Minor:** `repositorycommit` requeues every 1s to poll the local file store
  (`controller/repositorycommit/controller.go:131`); MCP keys bundle scope on
  `X-Railgrid-Tenant` while the controller keys on cluster ID
  (`mcpserver/tools_write.go:207`); `RAILGRID_DEV_ALLOW_TENANT_QUERY`
  (`mcpserver/context.go:111`).
- **Adopt into the contract:** gate 1 as a real caller GET with UID/spec
  pinning against a provider-SA read (`actions/server.go:211,230-238`);
  path-cluster must equal header-cluster (`:79`); credential-dropping
  `tenant.ClientFactory` (`tenant/client.go:56-60`).

### 3.4 infrastructure — the data-plane reference

Paths under `providers/infrastructure/`.

> **Status 2026-09-20 — closed** (plan §4). `serve` no longer has an
> admin-credential branch and fails fast without the minted SA kubeconfig;
> the operator and Template/Instance controllers are reconcilers rather than
> tickers; every verb runs `dataplane.Gate` with SSAR `create` on
> `instances/{verb}`; the chart renders its CatalogEntry from the embedded
> `manifest.yaml`; heartbeat `CanSend` comes from `vwhealth`.

- **Conforms** on the data plane: grammar at `dataplane/handler.go:521-576`,
  resource resolved as the caller with the provider credential stripped
  (`tenant/client.go:54-78`), template taken from the authorized Instance
  never the request (`handler.go:154-166`), namespace confinement
  (`resolver.go:128-151,306-324`), caller bearer deleted before the runtime
  hop (`proxy.go:100-108`), MCP dev tools replaying the same handler
  in-process (`mcpserver/tools_dev.go:581-620`). No ad-hoc REST.
- **⚠️ Only `exec` has a gate-2 SSAR** (`authorizer.go:44-75`); every other
  verb is gated by the Instance GET alone (`handler.go:147-152`). Either SSAR
  `instances/{verb}` for all verbs, or the contract states "GET-as-gate for
  template-declared verbs, SSAR for typed verbs".
- **❌ Timer loops:** the operator re-applies CRDs/APIExport/CatalogEntry/seed
  Templates on a 60s ticker (`operator.go:62,117-160`) and its CR reconciler
  requeues every 2m unconditionally (`operator/controller.go:194`); the
  Instance controller keeps a blanket 10m `resyncPeriod`
  (`controller/instance/controller.go:97-102`).
- **❌ Serve can run with admin credentials:** when `INFRASTRUCTURE_KUBECONFIG`
  is unset serve runs the install chain (`controller_manager.go:78-103`), and
  `INFRASTRUCTURE_WORKSPACE_PATH` explicitly allows a root-scoped kubeconfig
  (`:253-264`).
- **📄 M8 still live:** `<instance>-registry` pull-secret naming and the OIDC
  bridge (`controller/instance/bridge.go:56,79-103`) couple app-studio and
  infrastructure by string.
- **Structure:** Template controller runs on a second plain manager with its
  own lease (`controller_manager.go:115-143`) instead of
  `mgr.GetLocalManager()`; `CanSend` unset (`main.go:230-234`); the operator
  is a fourth CatalogEntry apply path (`operator.go:55`).
- **Adopt into the contract:** template-declared, namespace-confined verb
  contracts with a `FromStatus` short-circuit (`resolver.go:195-197`);
  computed lifecycle deadlines as a sanctioned `RequeueAfter`
  (`lifecycle.go:52-75`); dynamic runtime-GVR watches mapped back to Instances
  instead of status polling (`controller/instance/watch.go`).

### 3.5 edges — sound state model, its own data-plane dialect

Paths under `providers/edges/`.

> **Status 2026-09-20 — closed** (plan §5). Reconcilers are leader-elected
> and the tunnel is replicated; the edge proxy is on the shared grammar with
> a per-verb SAR (`create` on `{resource}/{verb}`), so the wildcard `proxy`
> verb is gone; bearers no longer travel in query strings; agent credentials
> are TTL'd and minted by the hub identity service, closing M7.
>
> Additionally, on **2026-09-20** `CatalogEntry.spec.edgeProxyAccess` and the
> Enable-time `railgrid:provider:<name>:edges-proxy` ClusterRole/ClusterRoleBinding
> it requested were **deleted outright**. The data plane gates as the caller
> (`internal/tunnel/grammar.go`, `gateAsCaller` on a credential-dropping
> config) and every other tenant read goes through the APIExport virtual
> workspace, so the grant — which authorized the *provider's* ServiceAccount
> in the tenant workspace — had no remaining reader. The `secrets` claim is
> now narrowed by `selector.matchLabels[railgrid.ai/owner]=edges`.

- **Pillar 1 conforms:** seven kinds; tunnel liveness as Leases in the
  provider workspace with a single status writer, fully watch-driven
  (`internal/edgectrl/lifecycle_reconciler.go:132-158`); per-edge credentials
  as Secrets in the tenant workspace. In-memory: the revdial dialer map
  (re-derivable) and the edge event ring buffer (not re-derivable,
  acknowledged at `controller_manager.go:289-298`).
- **❌ No leader election, static `/healthz`, no `CanSend`**
  (`controller_manager.go:174-182`, `main.go:161,356-361`). Active-active is
  argued from the tunnel's tenant resolver needing every replica; the fix is
  a slice-backed resolver on every replica and leader-elected reconcilers.
- **⚠️ Grammar dialect:** paths carry `apis/{group}/{version}` between
  `clusters/{id}` and `{resource}`; gate 2 is one wildcard verb `proxy` on the
  resource for k8s, ssh, service proxy and MCP (`edges_proxy_builder.go:107`,
  `service_proxy.go:213`) instead of `create` on `{resource}/{verb}`.
- **⚠️ Bearer from `?token=`** (`auth.go:76-86`), consumed by the host
  terminal (`portal/src/components/TerminalInstance.vue:54`).
- **⚠️ M7:** non-expiring per-edge SA tokens and a broad shared ClusterRole
  (`rbac_reconciler.go:247-299,487`). The Enable-time edges-proxy grant
  (`pkg/hub/kcp/bootstrap.go:2169-2260`) still grants direct Secrets/
  Namespaces/status access that edges no longer uses; only `proxy` is live.
- **⚠️ Ad-hoc:** unauthenticated `/catalog` (`main.go:303-313`); per-edge MCP
  mounted under `/agent` (`agent_proxy_builder_v2.go:92-100`).
- **Pillar 3 conforms** (kube client, `providerFetch`, children, tile).
- **Adopt into the contract:** fail-closed data plane when kcp is
  unreachable (`server.go:303-328`); reading Service CRs and Secrets as the
  caller to avoid a confused deputy (`service_proxy.go:397-413`); per-edge
  `resourceNames`-scoped `proxy` grant for reconnect
  (`rbac_reconciler.go:401-415`); the agent-tunnel route as class (f).

### 3.6 agents — good plumbing, wrong surface

Paths under `providers/agents/`.

> **Status 2026-09-20 — closed** (plan §8). The ~40-route `/api/*` CRUD
> surface is gone — `adhoc-rest` has no exception entry for agents — and the
> provider serves through `serve.New` with its tunnel and webhook routes
> declared as class (f) and (g). It no longer mints ServiceAccounts or RBAC
> over the infrastructure group: identities come from the hub identity
> service under the policy's clause for tenant-consented composition.
> **Still open:** the `secrets` claim is resource-wide (X-4).

- **Pillar 1:** five kinds, `apiexportprovider` + `leaderelection` +
  `vwhealth` + `ready.Attach` (`controller_manager.go:95-153`, `main.go:110-146`).
  📄 Runs, transcripts, inbox, memories, usage in Postgres
  (`store/postgres.go:52-191`; `docs/agents-provider-architecture.md:304-307`).
  ❌ `agents_tenants` table caches kcp facts, populated only when a user opens
  the UI, so s2s fails before that (`api/s2s.go:305-310`). ❌ 30s background
  ticker with `sweepStaleRuns` (`api/background.go:388-416`) and a
  non-durable in-process job queue (`executor/executor.go:117`).
- **❌ Pillar 2:** the whole tenant surface is `/api/*` CRUD mirroring Agent,
  Schedule, Connection, Toolset, Trigger and the LLM Secret
  (`api/server.go:162-242`); the architecture doc frames "REST first" as a
  rule. Bespoke `/s2s/clusters/{c}/agents/{n}/runs` data plane authorized via
  TokenReview+SAR on `agents/delegate` using the provider kubeconfig
  (`api/s2s.go:178-276`). Inbound webhooks (`server.go:250-253`) have no
  contract class. Auth on `/api/*` itself is correct: caller-bearer tenant
  client, headers for addressing only (`api/http.go:172-200`).
- **❌ Cross-provider:** hardcoded infrastructure dataplane path
  (`tools/tools.go:143`), hardcoded hub aggregate MCP path
  (`api/agents.go:784`), per-agent SAs with a ClusterRole over
  `infrastructure.railgrid.ai` and non-expiring tokens
  (`api/agentidentity.go:65-73`).
- **❌ Pillar 3 data path:** `createKubeClient` never imported; every CR goes
  through `/services/providers/agents/api/*` (`portal/src/api.ts:195-248`).
  Transport, element, tile, styles conform.
- **❌ Claims drift three ways:** manifest declares `tokenreviews` and
  `subjectaccessreviews` (`manifest.yaml:84-91`); chart and `init_cmd.go:62-71`
  omit them. This is exactly the AGENTS.md §5.1 failure mode.
- **Adopt into the contract:** SAR on a named subresource `agents/delegate`
  for service callers; workspace path resolved from kcp as the caller
  (`api/http.go:75-95`); `detachedStreamContext` so a run outlives its SSE
  client (`api/http.go:120-123`).

### 3.7 app-studio — the largest deviation

Paths under `providers/app-studio/`.

> **Status 2026-09-20 — partly closed** (plan §9, Cuts A, B and C). The
> `/api/projects/*` surface and the `X-Railgrid-User` actor are gone; the provider
> serves through `serve.New` and authorizes as the caller. Composition landed
> on **hub-minted scoped identities**, not permission claims: first-party
> claims pin to one export's `identityHash` and break org-owned providers, so
> what app-studio composes is declared as `dependencies[].composes` on the
> CatalogEntry, accepted at Enable as a `compose:<group>/<resource>` Grant,
> and minted per object. Its only remaining claim is `secrets`.
>
> **Cut D has not started:** conversations still live in Postgres and the
> project source tree still lives on a PVC, so the Pillar 1 finding below
> stands as written. The `secrets` claim is still resource-wide (X-4).

- **Pillar 1:** Project, Session, Studio on `ai.railgrid.ai`;
  `apiexportprovider` present (`controller_manager.go:286-294`). ❌ No
  `leaderelection`, no `vwhealth`; readiness is "manager started"
  (`controller_manager.go:349-351`); chart pins one replica. 📄 Conversations,
  attachments, approval mode, thumbnails, spend in Postgres with the Session
  CR as a projection (`types_session.go:31-36`). ❌ Project source tree and
  commit ledger on a PVC (`workspace/store.go:17-18,271`). ❌ Retention
  sweepers on tickers (`main.go:510,530`), 250ms Postgres poll behind SSE
  (`api/assistant_threads.go:1275`), 15s manager restart loop
  (`controller_manager.go:70`).
- **❌ Pillar 2:** about 95 routes under `/api/projects/*`
  (`api/server.go:337-434`): CRUD facades over the Project CR (memory,
  template, repository), the LLM Secret, sharing policy; Postgres CRUD for
  threads; file CRUD over the PVC; and the verbs that *would* fit the grammar
  (promote, sync, restart, logs, hydrate, turns) served on ad-hoc paths. No
  `/mcp`. ⚠️ `X-Railgrid-User` is taken from the header as the actor for
  thread and attachment ownership (`api/http.go:66-72`,
  `api/assistant_threads.go:207,227`).
- **❌ Cross-provider:** all three findings in cross-provider-simplification
  §2.1 still hold, though every call is as the caller: infra dataplane path
  string-built (`api/dataplane_client.go:29-73`), actions URL string-built
  (`api/integrations.go:640`), code `Connection` Secret read and re-minted as
  `<instance>-registry` (`api/project_promote.go:66-127`). `dependencies`
  lists only `infrastructure` though code CRs are required.
- **Pillar 3:** element, tile, `providerFetch`, kube client for pickers
  conform; the sandboxed preview iframe + MessagePort is a documented CSP
  exception. ❌ Data layer is REST-first. Minor: `ModelConnectionCard.vue`
  one-line drift from `provider-sdk/agentkit-vue`.
- **Adopt into the contract:** hot-reloadable element wrapper with a
  bootstrap generation (`element.ts:185-198`); no first-party claims, with
  per-project identities through `tenantaccess.EnsureIdentity` acting via
  each workspace's own bindings (`init_cmd.go:23-31`,
  `controller/project/identity.go:87`); projection CR + purge finalizer.

### 3.8 kuery — pillars 1 and 2 inverted

Paths under `providers/kuery/`.

> **Status 2026-09-20 — partly closed** (plan §6, PRs 1, 2, 3 and 5). It now
> serves through `serve.New`; `/api/query`, `/api/edges` and `/api/status` are
> replaced by data-plane routes that check the bearer and run both gates
> rather than trusting `X-Railgrid-Cluster`; `/readyz` comes from `vwhealth` and the
> hand-rolled per-edge Leases are gone.
>
> The cross-provider finding is closed in a way this audit did not anticipate:
> kuery's edge-watch identity is **hub-minted**, owned by the tenant's own
> kuery APIBinding, and its export carries no permission claims at all. It
> therefore dropped `edgeProxyAccess` — which, as the audit notes below, asked
> for a grant on the provider SA that the code never used. The field itself
> was deleted platform-wide on 2026-09-20.
>
> **PR 4 is open:** kuery still mints some workspace identities of its own and
> still holds `serviceaccounts`/`clusterroles`/`clusterrolebindings` claims.

- **❌ Pillar 1:** the sole exported kind `SavedView` has no Go types, no
  reconciler, no reader anywhere (zero non-test references). The real tenant
  surface is SQLite/Postgres, and the `clusters` rows' tenant label and
  active/stale status are the source of truth for both `/api/edges` and query
  isolation (`engagement/controller.go:345-364,683-693`,
  `queryapi/handler.go:187-199`). ❌ `runOrphanSweep` 1-minute list-and-sweep
  (`controller.go:284-328`), 20s requeue heartbeat per binding
  (`controller.go:428-441`), GC ticker (`core/core.go:65,100`). ❌ No
  `leaderelection` (hand-rolled per-edge Leases, `claims.go:28-148`; manager
  on every replica); no `vwhealth`; healthy with no kubeconfig at all
  (`main.go:164-166`).
- **❌ Pillar 2, verified directly:** `/api/query` and `/api/edges` derive the
  tenant from `X-Railgrid-Cluster`/`-Tenant` only (`queryapi/handler.go:86-112`)
  and never look at the bearer; no SSAR, no tenant client; `?tenant=` bypass
  under `RAILGRID_DEV_ALLOW_TENANT_QUERY`. Safe only because the hub proxy
  strips and re-injects the headers. `/api/status` echoes per-replica
  internals.
- **⚠️ Cross-provider:** of the three defects in §2.1 item 2, the `edges`
  claim is gone and the path now targets the edges provider's mounted
  `edgeproxy` route, but it is still hand-composed (`controller.go:745-753`).
  `edgeProxyAccess: true` in the manifest requests a grant for the provider SA
  the code deliberately does not use (`controller.go:34-41`).
- **❌ Pillar 3 data path:** no kube client; `App.vue:52` gates edge discovery
  on the deprecated `ctx.token`, so a host that stops exposing it breaks the
  page; `graph.ts:8-10` injects cytoscape with a runtime `<script>` tag.
- **What conform looks like:** either implement `SavedView` (types,
  reconciler, UI) and expose query as `/clusters/{id}/savedviews/{name}/run`
  with the two gates, or drop the export and declare kuery a pure data-plane
  provider whose verbs hang off the bound `KubernetesCluster` kind.

### 3.9 databricks — the actions reference, plus three ad-hoc routes

Paths under `railgrid/providers/providers/databricks/`. Pins `provider-sdk` v0.2.0.

> **Status 2026-09-20 — not started** (plan §3.2). Lives in the external
> `railgrid/providers` repo and still pins `provider-sdk` v0.2.x, so it has
> none of the shared `dataplane`/`serve` work. Every finding below stands.

- **Conforms:** three kinds, `apiexportprovider` + `leaderelection`
  (`controller_manager.go:71,280-299`); `query_table/v1` on the actions
  grammar with gate 1 SSAR `get tables/{n}` and gate 2 SSAR **`create`**
  `tables/{n}` subresource `query_table` as the caller
  (`tenant/action.go:178-227`), provider-authority reads only after the
  gates; declared limits enforced; narrow `secrets get` claim; manifest, chart
  and init claims identical.
- **❌ Ad-hoc REST:** `/api/v1/discovery/{warehouses,catalogs,schemas,tables}`
  and `POST /api/v1/registrations` (`tenant/import.go:68-83,129`) — the
  latter creates Warehouse/Table CRs on the tenant's behalf, which the portal
  can do itself with the kube client it already uses (`portal/src/api.ts:380`).
  `/api/status` (`main.go:258-273`) is unauthenticated and echoes the caller's
  user header and bearer length.
- **⚠️ Readiness** is a home-grown `controllerHealth` plus a 5s
  `time.NewTicker` endpoint-slice watchdog (`controller_manager.go:308-330`)
  instead of `vwhealth`. The watchdog's behaviour (rebuild the manager on VW
  loss) is worth lifting into the SDK.
- **⚠️ Envelope** is a provider-local type wire-compatible with
  `actionwire`, not the SDK package. **⚠️ Portal** feeds `ctx.token` into
  `providerFetch` (`api.ts:113-121`, `App.vue:67-68`). Vendored portalkit is
  one core version behind (20 vs 22).
- **What conform looks like:** discovery as read-only actions on
  `connections` (`.../connections/{n}/discover_tables/v1`); registrations
  deleted; `/api/status` deleted; `vwhealth.Watch` + `Readiness.Attach`.

### 3.10 planner — the reconciler reference, with the `invoke` bug

Paths under `railgrid/providers/providers/planner/`. Pins `provider-sdk` v0.2.0.

> **Status 2026-09-20 — not started** (plan §3.1). External repo, still on
> `provider-sdk` v0.2.x. Every finding below stands, including the `invoke`
> verb, which the in-tree providers have all replaced with `create`.

- **Conforms:** Connection, Board, Issue exported; private `ActionReceipt`
  CRD in the provider workspace for durable write receipts
  (`internal/actionapi/install.go:20-60`) reconciled on `GetLocalManager()`
  (`internal/controllers/manager.go:170`); `leaderelection` + `vwhealth`
  `Ready.Attach` (`manager.go:112,140`); `/readyz` from `vwhealth.Handler`;
  no tickers; export trimmed to the public schemas at init
  (`init_cmd.go:88`). Seven actions on the grammar, path cluster must equal
  header cluster (`internal/server/actions.go:129`), gate 1 a real GET
  (`:62`), `actionwire` envelope (`:133`).
- **❌ D3:** gate 2 SSAR verb **`invoke`** (`actions.go:70`) and
  `examples/consumer-rbac.yaml` grants `invoke`. The hub grants `create`.
- **❌ Ad-hoc REST:** `POST /api/onboarding/projects` takes a raw Linear API
  key in the body (`internal/server/onboarding.go:56-60`) and
  `GET /api/connections/{c}/projects` (`boards.go`). Both are caller-gated
  by SSAR but outside the grammar.
- **⚠️** `GET` on the action route with `?requestId=` for receipt inspection
  (`actions.go:136`) is not in the documented POST-only grammar. **⚠️** Chart
  hard-fails on `replicaCount != 1` despite leader election
  (`deploy/chart/templates/deployment.yaml:7-8`). Heartbeat version hardcoded
  `"0.1.0"` (`main.go:96`).
- **Pillar 3:** `providerFetch` only, no `ctx.token`; but CR I/O is hand-built
  `/clusters/<tenant>/apis/...` paths (`portal/src/api.ts:81,131`) with the
  vendored `kube.ts` unused; own `href()` instead of `portalHref`.
- **Adopt into the contract:** the private receipt kind for stranded writes;
  `Retry-After` honoured in requeues; export trimming at init.

### 3.11 factory — cleanest cross-provider story, no heartbeat

Paths under `railgrid/providers/providers/factory/`. Pins `provider-sdk` v0.2.2.

> **Status 2026-09-20 — not started** (plan §7). External repo. Every finding
> below stands.

- **Layout deviates** from the checklist: entry at
  `cmd/provider-factory/main.go`, bootstrap in `internal/bootstrap/`, kinds
  as YAML schemas generated by `hack/generate-api.py` with untyped Go structs
  in `internal/api/types.go`; no `apis/v1alpha1`, no scheme, no deepcopy.
- **Conforms:** eight `mcbuilder` reconcilers on `apiexportprovider`, leader
  elected, `Ready.Attach` to `vwhealth` (`internal/global/controller.go:237-272,242`);
  `RequeueAfter` only for runner/PR/ticket polls and backoff; manifest is a
  **single source** (`manifest.yaml` == `deploy/chart/files/manifest.yaml`,
  chart renders the CatalogEntry from it, init reads claims from it,
  `internal/bootstrap/bootstrap.go:56,123-132`) — the pattern the roadmap
  wants. **Cross-provider is by binding:** a per-workspace identity SA with a
  digest-named ClusterRole (`internal/global/identity.go:21-145`), every
  tenant call through the hub as that SA (`workspace.go:123`), planner
  writes via planner's own action route (`internal/plannerkrm/http.go:68-75`)
  so planner's gates apply, Issue mirror read as bound CRs, edges `Addon`
  touched only by the portal as the user.
- **❌ No hub heartbeat anywhere;** the served `/readyz` is a static "ok"
  (`internal/health/health.go:12-21`) while `vwhealth.Handler` is only on the
  controllers container the catalog `healthPath` never reaches
  (`deployment.yaml:71-73`). The hub cannot see this provider's health.
- **❌ Ad-hoc REST:** `POST /api/line-setup` and `POST /api/worker-setup`
  (`cmd/provider-factory/main.go:89-90`) take the cluster from the **body**
  (`internal/lines/setup.go:27,61`, `internal/workersetup/handler.go:23,51`)
  with no cross-check against `X-Railgrid-Cluster`; the worker handler proxies
  the edges runner `capabilities` by hand-built edgeproxy path
  (`handler.go:62,71`).
- **⚠️** Artifact blobs on a PVC referenced by digest from `Attempt.status`
  (`internal/artifacts/store.go`) — fits the transient-artifact carve-out if
  documented. Planner and edges kinds are not claimed (bootstrap rejects
  `*.railgrid.ai` groups), so the Issue mirror is polled at the Board sync
  interval rather than watched. Stale `0.0.0-stage6e` version and
  `Stage: 3` health payload.
- **Pillar 3 conforms** (kube client, `providerFetch`, `portalHref`, children,
  portalkit provenance in `SOURCE.md`).
- **Adopt into the contract:** single-source manifest; per-workspace
  identity SA with digest-named grants; cross-provider writes through the
  other provider's action route.

---

## Part 4 — Recommended contract amendments

Things the tree does better than the docs say, or does consistently enough
that the contract should name them rather than leave each author to rediscover.

1. **Pin the verb: `create` on `{resource}/{verb}`.** One string, everywhere,
   because the hub writes that rule (D3).
2. **Two carve-outs for Pillar 1:** transient artifacts and projection kinds,
   with the conditions in Pillar 1 rule 3.
3. **Three sanctioned `RequeueAfter` uses,** adding computed deadlines.
4. **Two more Pillar 2 classes:** the agent tunnel (f) and signed inbound
   webhooks (g), each with its own auth model.
5. **Two named Pillar 3 exceptions:** OAuth popup `postMessage` and content
   iframes under the hub CSP.
6. **Gate 1 is a real GET, and path-cluster must equal header-cluster.**
7. **Retire the "two auth models" framing in AGENTS.md §5.7.** There is one
   model. A portal that only calls its own REST is a deviation to fix, not a
   model to pick.
8. **Retire `spec.virtualWorkspace`** from the CatalogEntry (D1).
9. **A shared server-kit** for path parsing, the two gates, schema and limit
   enforcement and the envelope (cross-provider-simplification P2). Four
   dialects exist because every provider wrote its own; the quickstart cannot
   demonstrate Pillar 2 without one.

---

## Part 5 — What to fix, in order

Ordered by how much it changes what a new author copies. The provider-by-
provider plan with PR breakdown, migrations and acceptance checks is
[roadmap/provider-contract-remediation.md](./roadmap/provider-contract-remediation.md).

1. **Make quickstart the contract in code** (§3.2): typed Greeting, one
   reconciler, leader election, vwhealth, kube-client UI, no `/api/*`, README
   rewritten. Point providers.md at it (D6).
2. **Fix the verb** in code and planner (D3). Two-line change, real bug.
3. **Docs drift D1, D2, D4, D5, D7–D13** in one PR.
4. **Agents claim drift** (§3.6): reconcile manifest, chart and `init_cmd.go`.
5. **Kuery**: bearer-verified query verb on a bound kind, or drop the dead
   `SavedView` export; leader election and vwhealth.
6. **Edges**: leader election, vwhealth, `CanSend`; grammar and verb
   alignment; drop `?token=`.
7. **Infrastructure**: kill the operator ticker and admin-serve paths; decide
   GET-as-gate vs per-verb SSAR and write it down.
8. **Agents and app-studio**: move CR CRUD to the kube client and keep only
   verbs on the backend; route cross-provider calls by binding; leader
   election and vwhealth for app-studio.
9. **External providers**: factory gets a heartbeat and a real `/readyz` on
   the served port; databricks and planner fold their `/api/*` discovery and
   onboarding routes into read-only actions; databricks moves to `vwhealth`
   and `actionwire`.
10. **Cross-provider identity** (M7): agents' and edges' self-minted SAs onto
    the hub identity service, per cross-provider-simplification X-5.
