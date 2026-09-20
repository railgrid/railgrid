# Provider contract remediation — provider by provider

Status: **IMPLEMENTED, UNCOMMITTED** on branch `provider.contracts` (working
tree on top of `91c16a31`, 20 September 2026). Plan written 19 September 2026
from the findings in [provider-contract-review.md](../provider-contract-review.md).
Landed on the branch: §0 in full (§0.1–0.5); §1; §2; §4 in full; §5 in full with
§6 PR 4; §6 in full (kuery on `provider-sdk/sharding`, engagement on every
replica); §8 in full; §9 Cuts A, B, C and D (D.1 commit action, D.2 session
status and LISTEN/NOTIFY, D.3 source-tree ledger on `Project.status.workspace`,
D.4 finalizer teardown); §10 with declarable `spec.dataPlane.verbs` and policy
clauses for platform groups, MCPServer `use` and named APIBinding `get`. Every
provider serves through `provider-sdk/serve`, every verb passes
`provider-sdk/dataplane` gates, no provider mints identities, and
`verify-provider-contract` runs with an empty exception registry.
§3 and §7 are applied in the `railgrid/providers` checkout (databricks, planner,
factory on `serve.New` + `dataplane`; factory heartbeat with `CanSend`) against
a local `replace github.com/railgrid/provider-sdk => ../../../railgrid/provider-sdk`
in each `go.mod` that must be repinned to a published SDK before merge. §7.3
landed as typed `apis/v1alpha1` Go types for factory's five kinds, generated
through controller-gen + apigen + apiexportgen, with a schema-equivalence gate
(`hack/verify-schema-equivalence.py`) proving no bound, enum, default or CEL
rule was lost against the retired Python DSL; the hand-written `manifest.yaml`
replaced `generate-api.py`. Still owed there: converting `internal/api`'s
unstructured decode path and the controllers to the typed kinds
(`docs/typed-apis.md`, "What is still owed").
Operator and tenant upgrade steps are collected in
[provider-contract-migration.md](../provider-contract-migration.md).
Open follow-ups recorded by the implementation:
- App Studio's per-workspace `Studio` singleton is created by the portal
  before the first Studio verb (every `studios/studio/{verb}` call now goes
  through one helper that ensures it; before, only `create-project` did and a
  fresh workspace 404'd on `create-readiness`). A provider-side bootstrap that
  creates the singleton when a workspace binds the export would remove the
  portal's write and make MCP or CLI callers work in a fresh workspace too;
  it needs a signal for "workspace engaged", which the tenantwatch package has.
- agents: landed. `ModelCredential` (`agents.railgrid.ai/v1alpha1`) is a
  first-class kind referencing the tenant Secret; the probe verbs are
  `modelcredentials/{name}/test` and `.../discover`, `agents/model-test` and
  `agents/model-discover` are gone, and first-run onboarding discovers model IDs
  before the first agent exists.
- Edge agents adopt a saved credential (`~/.railgrid/agent-<edge>.credential.json`)
  only when it matches their hub and cluster, and alternate with the join token
  on a 401 (`docs/edges-agent-credentials.md`); the edges e2e now gives each
  agent its own HOME. Before this, a credential left by an earlier run against
  another hub kept every kubernetes-type edge in a 401 loop.
- A commit is required before `make codegen`, `make codegen-agents-provider` and
  `make codegen-edges-provider` can mint fresh APIResourceSchema names for the
  `Run` schema changes (PR 5) and the edges kind doc-comment fixes; apigen
  derives the name from HEAD and refuses to reuse one for changed content.
- Code provider `repositories/commit/v1` (plus the declared
  `stage_commit_bundle` verb for payloads over the 1 MiB catalog ceiling)
  landed; App Studio's reconciler and the assistant's commit tool commit
  through it (§9 Cut D.1).
- App Studio composition (§9 Cut C part 4) landed on hub-minted scoped
  identities, not permission claims: first-party claims pin to one export's
  `identityHash` and break org-owned providers (AGENTS.md §5.7). The hub
  identity policy gained clause E (tenant-consented composition), declared as
  `dependencies[].composes` on the CatalogEntry and accepted at Enable as a
  `compose:<group>/<resource>` Grant; App Studio's only claim is `secrets`.
- kuery's edge-watch identity is hub-minted (owner: the tenant's kuery
  APIBinding; clause E read-only composition on `kubernetesclusters` plus
  clause C `kubernetesclusters/k8s`); its export carries no claims at all.
- Group ownership in the identity policy is resolved from each provider's
  live APIExport `spec.resources[].group` (mirrored to
  `CatalogEntry.status.apiGroups`), not from the export name; a provider whose
  export is unreadable fails closed with `APIGroupsUnknown`.
- X-4 closed: every remaining `secrets` claim is label-scoped to
  `railgrid.ai/owner: <provider>` (kcp `defaultSelector`, enforced by kcp's
  claim labeler and VW admission); kuery and factory claim nothing. Every
  Secret writer stamps the owner label. `verify-provider-contract` refuses an
  unscoped core `secrets` claim (`claim-selector`).
- One-time cleanup for existing installs: the retired Enable-time grant left a
  dead `railgrid:provider:edges:edges-proxy` ClusterRole and ClusterRoleBinding
  in every workspace that had edges enabled; delete both by hand (nothing reads
  them and no code path removes them any more).
- A tenant APIBinding accepted before a claim was narrowed keeps its `matchAll`
  selector (kcp makes it immutable) and shows `PermissionClaimsValid=False` as
  a warning; only Disable and re-Enable in that workspace recreates it with the
  narrow selector. The admin `claims/reaccept` endpoint propagates the claim
  set but deliberately keeps each existing claim's selector.
When a phase merges, replace the branch name with the PR number; when a provider
is fully conformant, delete its section.

Companion to [providers.md](../providers.md),
[provider-connectivity-contract.md](../provider-connectivity-contract.md),
[provider-actions.md](../provider-actions.md),
[cross-provider-simplification.md](../cross-provider-simplification.md) and
[provider-authoring-plan.md](./provider-authoring-plan.md).

---

## How to read this

Each provider gets its own section with a **target state**, an ordered list of
**PRs** (each independently shippable and reviewable), the **migration** it
needs for already-enabled tenants, and the **acceptance checks** that prove
it. The order of sections is the recommended order of work: it goes from the
change that fixes the most copies (quickstart) to the largest rewrite
(app-studio). Nothing in a later section blocks an earlier one, except where
a PR names a prerequisite from §0.

Sizes are rough: **S** is an afternoon, **M** a few days, **L** one to two
weeks, **XL** more.

Three rules apply to every PR below:

1. A change to permission claims edits `manifest.yaml`, re-runs
   `make codegen-<name>-provider` (which regenerates the APIExport and its
   chart copy), mirrors the chart `catalogentry.yaml`, and ships a migration
   for already-bound tenants before any code depends on the new claim
   (AGENTS.md §5.1).
2. No backwards-compatibility windows. A changed path, verb, field or route
   is replaced outright and every in-tree consumer moves in the same PR.
3. Every PR runs the provider's own `go build ./... && go test ./...`, plus
   `make verify-portalkit` and `make verify-ui-conformance` when it touches a
   portal, plus the provider's e2e suite when one exists.

---

## 0. Shared prerequisites (hub, SDK, docs)

These are small and unblock everything else. Do them first, in this order.

### 0.1 Pin the verb and fix the docs — S

- `docs/provider-actions.md`, `docs/providers.md`, `AGENTS.md §5.7`: apply
  the thirteen doc fixes D1–D13 from the review in one PR, and add the
  contract amendments from review Part 4 (verb `create`, the two Pillar 1
  carve-outs, three `RequeueAfter` uses, Pillar 2 classes (f) and (g), the
  two Pillar 3 exceptions, gate 1 as a real GET, path-cluster equals
  header-cluster).
- Retire `spec.virtualWorkspace` from `apis/providers/v1alpha1/types_catalogentry.go`
  outright, and delete `VirtualWorkspaceURL` from the registry.
- Delete `providers/secrets/portal/dist/`.
- Delete `providers/edges/config/kcp/apiexport-edges.railgrid.ai.yaml`.

### 0.2 `provider-sdk/dataplane`: the shared server-kit — M

The review found four dialects because every provider wrote its own path
parser and gates. One package ends that. It generalizes
`providers/infrastructure/dataplane/handler.go:521-576` and
`authorizer.go:44-75`, and reuses `provider-sdk/actionwire`:

```go
package dataplane

// Request is the parsed /{root}/clusters/{id}/{resource}/{name}/{verb}[/{tail}]
// or .../{name}/components/{c}/{verb}[/{tail}] route.
type Request struct {
    ClusterID, Resource, Name, Component, Verb, Tail string
    Version string // set for actions: {verb}/{version}
}

func ParsePath(root, path string) (Request, bool)

// Caller builds the credential-dropping tenant client for one request:
// host + CA from the provider kubeconfig, bearer from Authorization,
// target <hub>/clusters/{id}. Cached per (cluster, token hash).
type CallerFactory interface {
    For(clusterID, token string) (dynamic.Interface, error)
}

// Gate runs the two gates as the caller and returns the resource:
//   1. GET {resource}/{name} in the cluster (visibility; the object is
//      returned so the handler can pin UID/spec).
//   2. SSAR create on {resource}/{verb} name-scoped (the verb grant).
// It also refuses a request whose path cluster differs from
// X-Railgrid-Cluster when that header is present.
func Gate(ctx, r *http.Request, callers CallerFactory, gvr schema.GroupVersionResource, req Request) (*unstructured.Unstructured, error)

// Limits enforces the declared action limits (timeout, input/output bytes,
// result items) around an executor and writes the actionwire envelope.
type Limits struct{ Timeout time.Duration; MaxInputBytes, MaxOutputBytes, MaxResultItems int64 }
func Serve(w, r, env actionwire.Envelope, lim Limits, exec func(ctx, input json.RawMessage) (any, *actionwire.Error))
```

Ship it with a conformance test any provider can run against its own mux
(`dataplane.ConformanceTest(t, handler, fixtures)`): wrong verb denied,
foreign cluster denied, missing bearer 401, header/path mismatch 400, limits
enforced, envelope shape. Infrastructure and databricks are the first two
callers (§4, §3); everyone else migrates onto it in their own section.

### 0.3 Hub-side trims — S

- Trim the Enable-time edges-proxy grant in `pkg/hub/kcp/bootstrap.go:2169-2260`
  to `access` on `/` plus `proxy` on the three edge kinds once §5 lands the
  per-verb SAR (keep the wide grant until then; edges no longer uses it, kuery
  is verified in §6.1).
- `hubclient.HeartbeatConfig`: make `CanSend` mandatory (a nil func is an
  error and the loop does not start) so a provider cannot beat while its
  watches are dead; wire it in every provider that lacks it.
- `hack/verify-provider-contract.mjs` (new, in `make verify`): for every
  `providers/*/`, assert manifest/chart spec parity (generalize
  `providers/code/actions_catalog_test.go`), claims parity with `init_cmd.go`,
  both READMEs present, and no `/api/` route literal in `main.go` or
  `server/`. Exceptions live in a JSON file next to it with a reason, the
  same way `hack/ui-conformance-exceptions.json` works.

### 0.4 Two shipped objects: a generated APIExport and the CatalogEntry — M

A provider ships exactly two declarative objects and `init` applies them
verbatim. The **CatalogEntry** is `manifest.yaml`, hand-written, the single
source for display metadata, URLs, permission claims, actions and self-hosting.
The **APIExport** is generated: kcp's `apigen` already emits the correct
`spec.resources` (every kind with its immutable, versioned schema name), but
names the export after the API group and knows nothing about claims. A small
Go generator, `provider-sdk/cmd/apiexportgen`, runs after `apigen` in every
`codegen-<name>-provider` target: it reads `manifest.yaml`, renames the export
to `spec.apiExport.name`, stamps `spec.permissionClaims` from the manifest
(kcp shape; `identityHash` left for `init` to fill from configuration when a
first-party group requires one), and writes `config/kcp/apiexport-<name>.yaml`.
The codegen target copies it into `deploy/chart/files/apiexport.yaml` next to
the schemas.

`provider-sdk/install` then loses its claim plumbing: `Bootstrap` reads the
APIExport file and the schema files from `RAILGRID_KCP_DIR`, applies the
schemas, applies the export as generated, and still creates the endpoint slice
and bind grant (those are runtime objects, not declarations). The
`sdkinstall.PermissionClaim` list in every `init_cmd.go` is deleted, which
ends the three-copies problem at the source: the manifest is the only place a
claim is written, the generated export is verified against it by
`hack/verify-provider-contract.mjs` (`claims-parity` now compares manifest to
the generated file), and the chart copies are outputs. The `rm -f
apiexport-*.yaml` lines added in §0.1 go away.

### 0.5 `provider-sdk/serve`: one server layout — S

Handler logic is shared (§0.2) but every provider still hand-builds its mux.
`provider-sdk/serve.New(Options{Name, Readiness, Portal fs.FS, MCP, DataPlane,
Actions, HubOnly, OAuth})` returns the complete `http.Handler` with the fixed
layout: `/healthz`, `/readyz`, `/mcp` + `/mcp/sse`, `/dataplane/`,
`/actions/`, `/workload-identities/*` (hub-only), `/oauth/`, the portal file
server with SPA index fallback, request logging, and a `ServeHTTP` that routes
the grammar prefixes **before** `http.ServeMux` can rewrite `..`/`//`. It
refuses to register anything else, so a provider cannot add `/api/*` by
accident. quickstart, code, kuery and infrastructure migrate onto it in the
same change; agents, app-studio and edges migrate in their own sections.
`conformance.Test` runs against the real server, not the bare handler.

---

## 1. quickstart — make the reference the contract in code

**Target state.** A provider someone can copy that demonstrates all three
pillars in the smallest possible form: one typed kind, one reconciler, one
data-plane verb on it, a portal that reads the kind through the kube client
and calls the verb through `providerFetch`. No `/api/*`.

**PRs.**

1. **Typed Greeting + reconciler — M.** Add
   `providers/quickstart/apis/v1alpha1/{doc,groupversion_info,types_greeting}.go`,
   `make codegen-quickstart-provider` producing the schema into
   `deploy/chart/files/schemas/`. Add `controller_manager.go` copied from
   `providers/code/controller_manager.go:398-468` with a single reconciler
   that stamps `status.observedAt` and a `Ready` condition. Wire
   `apiexportprovider`, `leaderelection.Run`, `vwhealth.Watch`, `/readyz`
   from `vwhealth.Handler`, and heartbeat `CanSend` from the same readiness.
   Mount the provider kubeconfig in the chart (`values.yaml`,
   `deployment.yaml`); drop the demo `configmaps` claim.
2. **Replace `/api/hello` with a verb — S** (needs §0.2). Serve
   `POST /dataplane/clusters/{id}/greetings/{name}/greet` through
   `dataplane.Gate` + `dataplane.Serve`; delete `/api/hello`, `/api/stream`
   and the header echo. This is the one place a new author sees the two
   gates written out.
3. **Portal reads the CR — S.** `element.ts`: list and create Greetings with
   `createKubeClient({fetch: providerFetch(ctx), cluster: ctx.tenant})`,
   call `greet` through `providerFetch`, use `serviceBase()` instead of the
   hand rewrite, drop the 50 ms `basePath` poll, dispatch `railgrid-navigate`
   for the detail view, add `<railgrid-dashboard-tile-quickstart>` using
   `portalkit/dashboardtile.ts`.
4. **README rewrite — S.** Written against the code after PRs 1–3: custom
   element with property push, kind `CatalogEntry`, heartbeat, minted
   kubeconfig, Helm chart, the two gates. Point `docs/providers.md`
   §"Example: a minimal provider" at `providers/quickstart/` and delete the
   `examples/provider-hello` references.
5. **`make e2e-provider` — S.** Extend the existing quickstart e2e suite:
   enable in two workspaces, create a Greeting in each, assert
   `status.observedAt` in both and that workspace A's token cannot `greet`
   workspace B's Greeting.

**Migration.** None; nothing depends on quickstart.

**Acceptance.** All five AGENTS.md §5.4 verification bullets pass on the
quickstart; `hack/verify-provider-contract.mjs` passes with no exception
entry.

---

## 2. code — one bug and three doc gaps

**Target state.** Already the reference for controllers and actions; keep it
that way and fix the verb.

**PRs.**

1. **Verb `create` — S.** `actions/server.go:215`: SSAR `create` on
   `repositories/{action}`, nothing else. Update `server_test.go:81`, README
   `:132,142,499,507`.
2. **Document the carve-outs — S.** Move the transient bundle/snapshot store
   rationale from README into `docs/code-provider-architecture.md` and cite
   the Pillar 1 carve-out; catalogue `stage_snapshot` in the manifest with a
   `largeUpload: true` flag or move its exception into
   `docs/provider-actions.md`. Fix the "four schemas / six kinds / eleven
   actions" counts.
3. **Small loops — S.** `controller/repositorycommit/controller.go:131`:
   replace the 1 s requeue with a channel from `commitbundle.FileStore` on
   bundle arrival. `mcpserver/tools_write.go:207`: key bundle scope on
   `X-Railgrid-Cluster`, same as the controller. Gate
   `RAILGRID_DEV_ALLOW_TENANT_QUERY` behind a `dev` build tag.
4. **Adopt the kit — S** (needs §0.2). Replace the hand-written path parse and
   gates in `actions/server.go` with `dataplane.Gate` + `dataplane.Serve`,
   keeping the UID/spec pinning against the provider-SA read. Run the
   conformance test.

**Migration.** Any consumer RBAC that granted `invoke` must be rewritten to
`create`; the hub never granted `invoke`, so workload identities start working
immediately.

**Acceptance.** `make e2e-provider-actions` passes with a workload identity
invoking `branches/v1` through the hub-issued ClusterRole.

---

## 3. planner and databricks (`railgrid/providers`) — small, external

Both pin `provider-sdk` v0.2.x; each PR here bumps the pin to the release that
carries §0.2.

### 3.1 planner

1. **Verb `create` — S.** `internal/server/actions.go:70` and
   `examples/consumer-rbac.yaml`, README `:38`.
2. **Fold the two `/api/*` routes into actions — M.**
   `GET /api/connections/{c}/projects` becomes read-only action
   `connections/{c}/projects/v1` (catalogued, `readOnly: true`).
   `POST /api/onboarding/projects` goes away: the portal creates the Secret
   and the Connection first (kube client), the Connection reconciler
   validates the key and lists projects into `status.projects`; the portal
   reads status. No raw API key ever transits the provider backend.
3. **Grammar hygiene — S.** Receipt inspection moves from `GET ?requestId=`
   to `POST` with `{"inspect": "<requestId>"}`; heartbeat version from the
   build; drop the chart's `replicaCount != 1` fail (the write lock already
   lives on `ActionReceipt`).
4. **Portal — S.** Use the vendored `createKubeClient` instead of hand-built
   `/clusters/...` paths (`api.ts:81,131`); `portalHref` instead of `href()`.

### 3.2 databricks

1. **Delete `/api/status` — S.** It is unauthenticated and echoes the caller.
2. **Discovery as actions, registrations deleted — M.**
   `/api/v1/discovery/{warehouses,catalogs,schemas,tables}` become read-only
   actions on `connections/{name}` (`discover_warehouses/v1`, …, each
   `readOnly: true`, bounded result items). `POST /api/v1/registrations` is
   removed; the portal creates Warehouse/Table CRs with the kube client it
   already has (`api.ts:380`).
3. **SDK readiness and envelope — S.** Replace `controllerHealth` + the 5 s
   ticker (`controller_manager.go:308-330`) with `vwhealth.Watch` +
   `Readiness.Attach("controllers", provider)`; contribute the "rebuild the
   manager on VW loss" behaviour to `provider-sdk/apiexportprovider` if the
   SDK does not already do it. Replace the local envelope with
   `actionwire`.
4. **Portal — S.** Pass only `{fetch: ctx.fetch}` to `providerFetch`; resync
   portalkit to core version 22.

**Migration (both).** Consumer RBAC granting `invoke` is rewritten; the
removed `/api/*` routes have no consumer besides each provider's own portal,
which ships in the same PR.

**Acceptance.** `dataplane.ConformanceTest` passes against each mux; the
databricks `make e2e-provider-actions` fixture in the monorepo still passes.

---

## 4. infrastructure — keep the reference, remove the admin paths

**Target state.** The data-plane reference, with no timer loops, no
admin-credential serve path, per-verb gates, and one CatalogEntry source.

**PRs.**

1. **Serve never runs with admin credentials — M.**
   `controller_manager.go:78-103`: delete the "no `INFRASTRUCTURE_KUBECONFIG`
   → run the install chain" branch; serve requires the minted SA kubeconfig
   and fails fast otherwise. `:253-264`: delete the
   `INFRASTRUCTURE_WORKSPACE_PATH` root-scoped hint. `init` keeps the admin
   config. Update the chart README's local-dev section to run `init` then
   `serve`.
2. **Operator becomes a reconciler — M.** `operator.go:62,117-160`: replace
   the 60 s bootstrap ticker with a controller-runtime manager on the host
   cluster that `For(InfrastructureProvider)` and `Watches` the kcp objects
   it owns (CRDs, APIExport, CatalogEntry, seed Templates) through the
   provider workspace client; `RequeueAfter` only around the helm/kro install
   calls. `operator/controller.go:194`: drop the unconditional 2 m requeue.
3. **Instance controller loops — S.** Drop `resyncPeriod`
   (`controller/instance/controller.go:97-102,460`); keep computed lifecycle
   deadlines and cite the sanctioned use. Fold the Template controller onto
   `mgr.GetLocalManager()` and delete its second lease
   (`controller_manager.go:115-143`).
4. **Per-verb gate — M** (needs §0.2). Run `dataplane.Gate` for every verb:
   SSAR `create` on `instances/{verb}` (and `instances/components/{c}/{verb}`
   collapses to `instances/{verb}` for RBAC). Template-declared verbs keep
   their `FromStatus` short-circuit after the gate. The Enable flow's tenant
   RBAC for instances gains `create` on `instances/*` for workspace members,
   so humans keep working; app-studio's per-project identities gain the
   specific verbs they use (`sync`, `restart`, `log`, `proxy`, `exec`), in
   the same PR.
5. **One CatalogEntry source — S.** Chart renders `catalogentry.yaml` from the
   same embedded `manifest.yaml` the operator uses (factory's pattern:
   `deploy/chart/files/manifest.yaml` == `manifest.yaml`, verified by
   `hack/verify-provider-contract.mjs`). Prune the stale per-template
   APIExport comment (`manifest.yaml:98-104`).
6. **Typed cross-provider fields — M** (with §9.3). `Instance.spec.imagePullSecretRef`
   and `spec.oidcBridgeSecretRef` replace the `<instance>-registry` and OIDC
   bridge string conventions in `controller/instance/bridge.go:56,79-103`;
   validated by the Instance controller; app-studio (§9 Cut C) switches in
   the same PR.
7. **Small — S.** Set heartbeat `CanSend` from vwhealth (`main.go:230-234`);
   portal passes only `{fetch}`; `RAILGRID_DEV_ALLOW_TENANT_QUERY` behind a
   build tag.

**Migration.** PR 4 is the only tenant-visible change: the Enable-flow RBAC
and app-studio's per-project identities gain the verbs in the same PR.

**Acceptance.** The five §5.4 bullets; `dataplane.ConformanceTest` on the
dataplane mux; operator e2e shows a CatalogEntry edit reconciled without
waiting for a tick.

---

## 5. edges — leader election, grammar, and credentials

**Target state.** Reconcilers leader-elected; the tunnel active-active on
every replica; edgeproxy on the shared grammar with per-verb SAR; no bearer in
query strings; agent credentials TTL'd.

**PRs.**

1. **Readiness and heartbeat — S.** `/readyz` from `vwhealth.Handler`
   (`main.go:161`), `CanSend` from the same readiness (`:356-361`); the
   catalog `healthPath` points at `/readyz`. Fix README kind list and the
   "scales horizontally" claim (or make it true in PR 2).
2. **Leader-elected reconcilers, replicated tunnel — M.** Split
   `controller_manager.go:174-182`: the tunnel's tenant-config resolver reads
   from the APIExportEndpointSlice-backed cache on every replica (it only
   needs reads), while the seven reconcilers run under
   `leaderelection.Run` and are rebuilt per term as code does. The Lease-based
   liveness model and the pod-to-pod relay (`main.go:224-272`) stay. Persist
   the edge event ring buffer (`internal/events/store.go:77`) as `Event`
   objects in the tenant workspace with a bounded retention reconciler, or
   drop the feature.
3. **Grammar and verb — L** (needs §0.2). Serve
   `/edgeproxy/clusters/{id}/{resource}/{name}/{verb}` where `{resource}` is
   `kubernetesclusters`, `linuxservers`, `macosservers`, `services` and
   `{verb}` is `k8s`, `ssh`, `proxy`, `mcp`, through `dataplane.Gate` with
   SSAR `create` on `{resource}/{verb}`. Delete the old
   `/edgeproxy/clusters/{id}/apis/edges.railgrid.ai/v1alpha1/...` routes and
   the wildcard `proxy` verb. The RBAC reconciler
   (`rbac_reconciler.go:247-299,401-415`) writes the new verb set. Move per-edge MCP from the `/agent` mount to
   `/edgeproxy/.../{name}/mcp`. Update every consumer in the same PR:
   kuery (§6.3), factory (§7.2), agents (§8.3), the host `TerminalInstance.vue`,
   the hub `mcpaggregate` if it addresses edges by path.
4. **No bearer in `?token=` — M.** `auth.go:76-86`: browser WebSockets
   authenticate with a short-lived ticket minted by
   `POST /edgeproxy/clusters/{id}/{resource}/{name}/ticket` (a verb, gated
   like any other) and presented as the `Sec-WebSocket-Protocol`
   subprotocol; the host `TerminalDock`/`TerminalInstance` and the SSH CLI
   switch to it in the same PR. Delete the query-string path.
5. **Agent tunnel as class (f) — S.** Rename the agent route to
   `/agent/clusters/{id}/{resource}/{name}/proxy`; the `railgrid agent` in
   this repo moves with it (agents in the field must upgrade). Document the join-token /
   edge-SA auth model in `docs/providers.md` as class (f).
6. **Catalog into the bundle — S.** Delete unauthenticated `/catalog`
   (`main.go:303-313`); the service catalog ships as a JSON asset in the
   portal bundle and as a `status` projection on `Service` for MCP discovery.
7. **Agent credentials from the identity service — L** (with §10). Per-edge
   SA tokens become TokenRequest-minted, TTL'd, rotated by the agent on the
   `X-Railgrid-Agent-Kubeconfig` path; drop the legacy token Secrets
   (`rbac_reconciler.go:142-145,487`). Until §10 exists, at minimum bound the
   token Secret with an expiry annotation and a rotation reconciler.

**Migration.** PR 3 and PR 5 are protocol changes for every agent and every
consumer; agents in the field must upgrade. PR 4 changes the host portal.
PR 7 ships the agent-side rotation in the same PR.

**Acceptance.** Two replicas: an agent connected to replica A is reachable
from a request that lands on replica B; a failover of the lease does not
disconnect agents; `dataplane.ConformanceTest` on the edgeproxy mux; the
`edges` e2e suites (`make e2e-ssh`, `make test-edges-provider`) pass on the
new path shape.

---

## 6. kuery — decide what it is, then build that

**Target state.** Either a real `SavedView` provider whose query verb hangs
off a bound resource, or an explicitly data-plane-only provider. This plan
picks the first, because the schema is already exported and a named
resource is what the grammar needs.

**PRs.**

1. **Bearer-verified query — M** (needs §0.2; the safety fix, ship first).
   `POST /dataplane/clusters/{id}/savedviews/{name}/run` through
   `dataplane.Gate`: gate 1 GET of the SavedView as the caller proves
   membership in the cluster; gate 2 SSAR `create` on `savedviews/run`.
   The playground uses a per-user SavedView named `playground-<sha(user)>`
   the portal creates with the kube client, so ad-hoc queries are also
   gated. Delete `/api/query`, `/api/status` and the `?tenant=` bypass in
   the same PR. `/api/edges` is replaced by the portal listing
   bound `KubernetesCluster` CRs with the kube client.
2. **Typed SavedView + reconciler — M.** `apis/v1alpha1/types_savedview.go`,
   codegen, a reconciler that validates `spec.query` against the query schema
   and stamps `status.lastOpenedAt` / `Ready`. `leaderelection.Run` around
   the manager; `vwhealth` on `/readyz` and `CanSend`; a missing provider
   kubeconfig is fatal (`main.go:164-166`).
3. **Engagement as KRM — M.** The tenant label, active/stale status and lease
   owner in the SQL `clusters` table (`engagement/controller.go:683-693`)
   become a provider-private `Engagement` kind in the provider workspace
   (spec: cluster ID, edge; status: owner replica, lastSeen, phase). The
   1-minute `runOrphanSweep` becomes a reconciler on `Engagement` driven by
   the Lease watch; the 20 s per-binding requeue heartbeat becomes Lease
   renewal in the edge-watch goroutine only. The SQL index keeps only synced
   objects and is treated as a cache rebuildable from engagements. The
   hand-rolled per-edge Leases (`claims.go`) stay until §0.3's SDK sharding
   primitive exists, but their owner is recorded on the `Engagement`.
4. **Consume edges by publication — S** (after §5.3). Read the edgeproxy
   coordinate from `KubernetesCluster.status` instead of the inlined format
   string (`controller.go:745-753`); drop `edgeProxyAccess: true` from the
   manifest if the hub-side grant is confirmed unused (§0.3).
5. **Portal — S.** Gate on `ctx.fetch` presence, not `ctx.token`
   (`App.vue:52`); list edges and SavedViews with the kube client; bundle
   cytoscape into the Vite build instead of injecting a runtime `<script>`
   (`graph.ts:8-10`) so it is covered by the SRI pin. Fix README/chart
   comments (`edgesIdentityHash`, "Phase 1 skeleton").

**Migration.** `/api/query` consumers are the portal and the MCP tools; both
move in PR 1. No tenant objects exist yet, so SavedView is additive.

**Acceptance.** A bearer for workspace A cannot run a SavedView in workspace B
even with a forged `X-Railgrid-Cluster` header sent straight to the provider
pod; two replicas share engagements without a duplicate watch; the
`make e2e-kuery` suite (#761) extends to the SavedView path.

---

## 7. factory (`railgrid/providers`) — heartbeat, then routes

**Target state.** Visible to the hub, routes on the grammar, layout aligned
enough that the checklist applies.

**PRs.**

1. **Heartbeat and readiness — S.** `serve` runs `hubclient.RunHeartbeat`
   gated on a `vwhealth.Readiness` shared with the controllers container
   (both containers read the same APIExportEndpointSlice, so each can attach
   its own checker); `/readyz` on the served port comes from
   `vwhealth.Handler`; delete the static `health.go` payload and the
   `Stage` field; inject the build version.
2. **Routes onto the grammar — M** (needs §0.2 and §5.3 for the edges path).
   `POST /api/line-setup` becomes `factorylines/{name}/preview/v1`, a
   read-only action on a not-yet-created line: give it a `FactoryLine`
   resource by having the portal create the line in `Pending` first, then
   preview. `POST /api/worker-setup` becomes `workers/{name}/capabilities/v1`
   and reads the runner through the edges `services/{svc}/proxy` verb by the
   coordinate in `Service.status`, not a hand-built path. Both go through
   `dataplane.Gate`, which ends "cluster from the body".
3. **Typed APIs — M.** Add `apis/v1alpha1` Go types with deepcopy and generate
   the schemas from them (replace `hack/generate-api.py`); keep the untyped
   decode path until the controllers are converted. This is what lets
   `make codegen-<name>-provider` and the checklist apply.
4. **Artifacts carve-out — S.** Document the PVC blob store as a transient
   artifact per the Pillar 1 carve-out (digest on `Attempt.status`, hourly
   prune as a leader-owned runnable). No code change.
5. **Watch planner/edges kinds — S** (hub-side prerequisite: the identity-hash
   claim follow-up in `reconciler-architecture-review.md`). Once claims on
   `*.railgrid.ai` groups can be declared, claim `issues`, `boards`,
   `services` read-only and replace the Board-interval mirror scan with a
   `Watches` + mapping func.

**Migration.** None tenant-visible; the two `/api/*` routes are only called
by factory's own portal.

**Acceptance.** Hub shows factory Ready with heartbeats and flips it
not-Ready within 90 s when the controllers container loses its watch;
`dataplane.ConformanceTest` on the served mux.

---

## 8. agents — keep the plumbing, replace the surface

**Target state.** Bound CRs edited through the kube client; the backend serves
only verbs (chat, run, cancel, wait, test, authorize) on the grammar plus MCP,
OAuth, webhooks; runs and transcripts stay in Postgres under the projection
carve-out with a `Run` kind as their identity; cross-provider calls by
binding.

**PRs.**

1. **Claims parity + migration — S.** Add `tokenreviews` and
   `subjectaccessreviews` to `init_cmd.go:62-71` and the chart
   (`catalogentry.yaml:31-51`) to match `manifest.yaml:84-91`; ship
   `railgrid-hub` migration that re-accepts the two claims on every existing
   binding of `agents.railgrid.ai` (generalize into
   `pkg/hub/restapi` as `POST /api/admin/providers/{name}/claims/reaccept`
   so the next provider gets it free). Move `selfHosting` inside the chart's
   `agents.catalogEntry` define.
2. **Portal to the kube client — M.** Agent, Schedule, Connection, Toolset,
   Trigger and the `railgrid-agents-llm` Secret are read and written with
   `createKubeClient` (`portal/src/api.ts:195-248`); delete the corresponding
   `/api/{agents,schedules,connections,toolsets,triggers,credentials}` CRUD
   handlers (`api/server.go:162-242`) in the same PR. Reconcile the five extra vendored
   portalkit files (`confirm.ts`, `layoutPreference.ts`, `table.ts`,
   `useAnchoredPopover.ts`, `useDelayedLoading.ts`) into
   `provider-sdk/portalkit-vue` or delete them; add `ui.children`
   (Agents, Schedules, Connections).
3. **Verbs onto the grammar — L** (needs §0.2). Remaining backend verbs move
   to `/dataplane/clusters/{id}/{resource}/{name}/{verb}`: `agents/{n}/chat`
   (SSE), `agents/{n}/run`, `runs/{id}/{cancel,wait}` (see PR 4 for `runs`),
   `connections/{n}/{test,enable-inbound,authorize}`, `schedules/{n}/run`,
   `triggers/{n}/run`, `credentials`→`secrets` is dropped (PR 2). All through
   `dataplane.Gate`. The bespoke `/s2s/clusters/{id}/agents/{n}/runs`
   becomes the same `agents/{n}/run` verb: service callers pass the same two
   gates (their SA holds `create` on `agents/run`), which retires the
   `agents/delegate` SAR, the Postgres `agents_tenants` map and the
   provider-kubeconfig review client (`api/s2s.go:178-310`). Webhooks
   (`/webhooks/...`) stay, documented as class (g). Replace the hardcoded
   infra dataplane path (`tools/tools.go:143`) with the coordinate from
   `Instance.status`, and the hardcoded hub MCP path (`api/agents.go:784`)
   with the `MCPServer` object's `status.url`.
4. **`Run` as a projection kind — M.** Add `Run` to `apis/v1alpha1`
   (spec: agent ref, trigger; status: phase, startedAt, finishedAt,
   transcript ref). The executor creates the CR on start and updates status
   on transition; Postgres keeps the transcript, tool calls and usage rows
   keyed by the Run UID; a finalizer purges them. `sweepStaleRuns`
   (`api/background.go:388-416`) becomes a `Run` reconciler on the
   `startedAt`+timeout deadline (sanctioned `RequeueAfter`). Inbox items and
   memories follow the same shape or are declared Postgres-only in the
   architecture doc with the carve-out cited.
5. **Durable executor — M.** Replace the in-process queue
   (`executor/executor.go:117`) with `Run` reconciliation: a `Pending` Run is
   claimed by the leader by writing `status.owner`; a restart re-queues from
   the objects. The 30 s background ticker goes away with it.
6. **Identities from the hub — M** (with §10). Per-agent SAs over the
   infrastructure group (`api/agentidentity.go:65-73`) are replaced by
   hub-minted, TTL'd identities scoped to the exact Instances the agent's
   Toolset references. Until §10 exists, scope the ClusterRole with
   `resourceNames` and add an expiry.

**Migration.** PR 1 needs the claim re-accept before any code path uses
TokenReview. PR 4 is additive.

**Acceptance.** Portal works with `ctx.token` removed from the host; a Run
survives a provider restart and completes; workspace A's agent cannot run
against workspace B's Instance; `dataplane.ConformanceTest` on the dataplane
mux; the agents e2e suite extends to the `Run` kind.

---

## 9. app-studio — the long one, in four cuts

**Target state.** Project state on the CR; conversations under the projection
carve-out with `Session`; the source tree owned by code-provider
Repository/Commit objects; the backend reduced to verbs on the grammar;
leader election and vwhealth; cross-provider by binding. Done as four
shippable cuts, each of which leaves the product working.

**Cut A — controllers and readiness (M).**

1. `leaderelection.Run` around the manager, rebuilt per term; the API and
   assistant supervisor keep serving on every replica (they already do, with
   the replica-affinity forwarder). Drop the 15 s manager restart loop
   (`controller_manager.go:70`).
2. `vwhealth.Watch` + `Readiness.Attach("controllers", provider)`; `/readyz`
   from `vwhealth.Handler`; `CanSend` from it (already gated on controller
   health, `main.go:294-300`, so this is a swap).
3. Drop the 10 m safety resyncs (`controller/{project,studio,session}/controller.go:77/67/58`);
   keep the 5 s identity requeue as backoff only.
4. `dependencies` lists `code` as well as `infrastructure`.

**Cut B — what can move to the CR now (S).**

1. Portal writes `Project` display/description and sharing policy with a
   merge-patch on the CR via the kube client; `PATCH /api/projects/{p}` and
   `/memory` are deleted. The CRD already carries the validation those
   handlers duplicated; the reconciler stamps `status.observedGeneration`.
   The LLM registry becomes typed state: the non-secret model list and
   default live on the Studio spec, each model's key in its own Secret
   named by `secretRef`, `configured` reported in status by the reconciler
   so the portal never fetches key material; the Go `modelcatalog` is
   exported as generated JSON into the bundle for pricing. `llm-settings`
   CRUD is deleted, `test`/`discover` stay as verbs.
2. Actor identity: `api/http.go` stops reading `X-Railgrid-User` as the
   actor and derives it from a `SelfSubjectReview` with the bearer against
   `/clusters/{cluster}`, memoized per (cluster, token hash). The header
   stays a display label only.
3. **Deferred to Cut D on purpose:** `GET /api/projects[/{p}]`, `POST`,
   `DELETE`, `PUT /repository`, `PUT /template`. The project view joins
   live Instance status, the code Repository/Commit ledger and the pod-local
   `sourceRevision` fence that exists in no CR until the source tree moves to
   code-provider objects; create/delete are orchestration that must become
   controller finalizers first.

**Cut C — verbs onto the grammar (L, needs §0.2).**

1. `/dataplane/clusters/{id}/projects/{p}/{verb}` for `plan`, `promote`,
   `hydrate-workspace`, `restore-workspace`, `scaffold`, `sync-development`,
   `restart-development`, `development-logs`, `development-status`,
   `authorize-development-preview`; `sessions/{s}/{turn,steer,interrupt,events}`
   for the assistant. All through `dataplane.Gate`. The per-project identity
   already used for tenantwatch gets `create` on these verbs, and the human
   grant comes from the Enable flow's workspace-member RBAC.
2. Integrations: `/integrations/{i}/actions/{action}` stays as the consumer
   gateway but resolves the target provider's action route from the
   `providerReference` binding and the provider's catalog entry
   (`api/integrations.go:640` string-building removed).
3. Infra data-plane: coordinate from `Instance.status`
   (`api/dataplane_client.go:29-73` string removed); replaced by the
   consumer-side helper §0.2 ships.
4. Registry pull secret: consumes the typed `Instance.spec.imagePullSecretRef`
   from §4.6 and a code-provider action `connections/{n}/mint_registry_token/v1`
   that mints a scoped pull token; `api/project_promote.go:66-127` stops
   reading the code `Connection` Secret.
5. `/metrics` moves off the public backend port to an internal listener.

**Cut D — conversations and files (XL, can trail).**

1. `Session` becomes the identity of a thread: created on first turn, status
   carries phase/turn count/last activity, finalizer purges Postgres rows
   (the purge finalizer already exists). The 250 ms Postgres poll behind SSE
   (`api/assistant_threads.go:1275`) becomes `LISTEN/NOTIFY` fan-out; the
   retention sweepers (`main.go:510,530`) become a `Session` reconciler on a
   retention deadline.
2. Project source tree and commit ledger (`workspace/store.go`): the durable
   authority moves to code-provider `RepositoryCheckout`/`RepositoryCommit`
   objects (they exist for this); the PVC keeps only the working copy under
   the transient carve-out, rebuildable from the last commit. This is the
   piece that makes app-studio replica-safe
   (`docs/app-studio-replica-awareness.md`) and is why it is last.

**Migration.** Cut B deletes only facades with no non-portal consumer. Cut C
widens RBAC in the same PR as §4.4. Cut D changes where a project's files
live; ship with a one-way migration action.

**Acceptance.** Two replicas with leader election; the five §5.4 bullets;
`dataplane.ConformanceTest` on the dataplane mux; the existing app-studio
e2e and the provider-actions e2e pass at each cut; after Cut D a project
survives losing its PVC.

---

## 10. Hub identity service (unblocks §5.7, §8.6)

Not a provider, but the M7 finding (edges and agents minting non-expiring
ServiceAccounts, and factory's per-workspace SAs) cannot be closed
provider-side. Generalize `pkg/hub/workloadidentity` per
cross-provider-simplification §"One identity service": deterministic SA per
(owner kind, tuple), `resourceNames`-scoped ClusterRoles including data-plane
verb subresources, TokenRequest-minted TTL'd tokens, provider-asserted
attestation for agent/edge/session owners, GC keyed on the owning object.
Providers then drop their `serviceaccounts`/`clusterroles`/`clusterrolebindings`
claims. Size **L**, hub team; sequence after §0 and before §5.7/§8.6.

---

## Summary table

| # | Provider | PRs | Size | Depends on | Tenant-visible migration |
|---|---|---|---|---|---|
| 0 | hub/SDK/docs | 3 | M | – | `virtualWorkspace` removed |
| 1 | quickstart | 5 | M | 0.2 | none |
| 2 | code | 4 | S | 0.2 | consumer RBAC `invoke`→`create` |
| 3 | planner, databricks | 4 + 4 | M | 0.2 | consumer RBAC `invoke`→`create` |
| 4 | infrastructure | 7 | L | 0.2, 9 (typed refs) | RBAC widening with per-verb gates |
| 5 | edges | 7 | XL | 0.2, 10 | agents must upgrade; terminal ticket |
| 6 | kuery | 5 | L | 0.2, 5.3 | `/api/query` removed |
| 7 | factory | 5 | M | 0.2, 5.3 | none |
| 8 | agents | 6 | L | 0.2, 10 | claim re-accept; old routes removed |
| 9 | app-studio | 4 cuts | XL | 0.2, 4.4, 4.6, 10 | RBAC widening; per-project file migration |
| 10 | identity service | 1 | L | 0 | providers drop RBAC claims |

Critical path: **0.1 → 0.2 → 1 → 2/3 in parallel → 4 → 5 → (6, 7, 8 in
parallel) → 9**, with 10 started alongside 4 so it is ready for 5.7 and 8.6.
