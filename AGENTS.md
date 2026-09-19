# AGENTS.md

Orientation for AI agents (and humans) working in the **railgrid** repo. Read this
before making changes. It explains the architecture, where hub code ends and
provider code begins, how APIs are constructed, and the exact commands to build,
test, format, lint, and regenerate code.

> Module: `github.com/railgrid/railgrid` · Go workspace (`go.work`) · kcp-based
> multi-tenant control plane.

Deeper references live in [`DEVELOPERS.md`](./DEVELOPERS.md) and [`docs/`](./docs)
(per-provider architecture docs, security, organizations, hub proxy, mcp). Keep
those authoritative — this file is the map, not the territory.

---

## 1. What railgrid is

railgrid connects distributed Kubernetes clusters and bare-metal servers through one
control plane (the **hub**). Edge **agents** dial outbound reverse tunnels to the
hub, so clusters behind NAT/firewalls become reachable through a single
authenticated endpoint. On top of that core, railgrid is also a **multi-tenant
platform** built on [kcp](https://kcp.io): each user/team gets isolated kcp
workspaces, and **providers** extend the platform with their own APIs, UIs, and
backends.

Three planes to keep distinct:

- **Connectivity plane** — Edge/agent/tunnel/SSH/MCP (the original product).
- **Tenancy plane** — kcp workspaces, organizations, users, memberships.
- **Provider plane** — pluggable extensions (APIs + UI + backend) per tenant.

---

## 2. Repository layout

```
apis/                 First-party API types (railgrid, tenancy, providers groups)
  railgrid/v1alpha1/       Edge, MCPServer, Placement, VirtualWorkload
  tenancy/v1alpha1/     Organization, User, Membership, UserMembershipIndex, Auth
  providers/v1alpha1/   CatalogEntry (the provider manifest type)
cmd/                  Binaries
  railgrid/                CLI (also the agent: `railgrid agent run`)
  railgrid-hub/            Hub control-plane server
  release/              Release-tagging helper
pkg/                  Hub + agent + shared libraries
  hub/                  Hub server, controllers, provider integration, tenancy
  agent/                Edge agent (tunnel, ssh, reporters)
  virtual/              kcp virtual-workspace builders (agent-proxy, mcp)
  cli/ client/ util/ apiurl/ server/ version/
providers/            Provider implementations (see §5)
portal/               Main Vue.js SPA (the web console)
config/               Generated CRDs (config/crds) + kcp resources (config/kcp)
hack/                 Codegen + boilerplate + dev scripts
test/e2e/             End-to-end suites (see §7)
deploy/               Dockerfiles + Helm charts
docs/                 Architecture docs (per-provider, security, mcp, hub proxy)
docs/roadmap/         NOT IMPLEMENTED proposals — never treat a command, package or
                      endpoint named there as existing (see docs/roadmap/README.md)
Makefile              The single source of truth for build/test/lint/codegen
Tiltfile              Local dev loop (embedded kcp + static auth)
go.work               Workspace: root + standalone provider modules
```

`go.work` members: `.`, `provider-sdk`, every provider module under
`providers/` (`agents`, `app-studio`, `code`, `edges`,
`infrastructure`, `kuery`, `quickstart`), and the external `contrib-metering`
checkout. Every provider is
standalone with its own `go.mod`; none compile into the hub binary any more
(the `RegisterBuiltin` machinery in `pkg/hub/providers/builtin.go` still exists
but has no registrations).

---

## 3. Build / format / lint / codegen — the commands

Everything goes through the **Makefile**. Tools (controller-gen, apigen,
golangci-lint, kcp, dex) are version-pinned and installed into `hack/tools/` on
demand — never `go install` them globally.

| Task | Command | Notes |
|------|---------|-------|
| Build all binaries | `make build` | railgrid CLI + hub |
| Build hub | `make build-hub` | hub binary only; provider portals build per provider (§5.3) |
| Build hub w/ embedded portal | `make build-hub-portal` | `portal_embed` build tag |
| Unit tests | `make test` | all packages except `test/e2e` |
| Lint | `make lint` | `golangci-lint run ./...` |
| Auto-fix lint | `make fix-lint` | `golangci-lint run --fix` |
| Go vet | `make vet` | |
| Format | `make fix-lint` | goimports formatter runs via golangci-lint |
| Regenerate code | `make codegen` | CRDs + kcp schemas + boilerplate |
| Verify codegen clean | `make verify-codegen` | fails if `make codegen` produces a diff |
| License headers | `make boilerplate` / `make verify-boilerplate` | |
| **Everything (CI gate)** | `make verify` | boilerplate + codegen + vet + lint + build + test |

**Formatting / linting details** (`.golangci.yml`, golangci-lint v2):
- Linters: `govet`, `errcheck`, `staticcheck` (all checks), `unused`,
  `ineffassign`, `misspell`.
- Formatter: `goimports` with local-prefix
  `github.com/railgrid/railgrid` (railgrid imports group last).
- Generated files (`zz_generated*`, `vendor/`) are excluded.
- Before committing Go changes, run **`make fix-lint`** then **`make lint`**.

**Standalone providers** (their own `go.mod`) are NOT covered by the root
`make lint`/`make test`. Lint/test them from their own directory, e.g.
`cd providers/kuery && go build ./... && go test ./...`.

### Agent build cache and temporary storage

- Reuse the environment-provided `GOCACHE`, `GOTMPDIR`, and `TMPDIR` for normal
  builds and tests. They are shared, disk-backed paths that are safe for
  concurrent Go processes.
- Never override `GOCACHE`, `GOTMPDIR`, or `TMPDIR` with a path under `/tmp`;
  `/tmp` is a capacity-limited tmpfs on development machines.
- Use `go test -count=1` when fresh test execution is required. This bypasses
  Go's test-result cache and does not require recompiling dependencies into a
  fresh build cache.
- Create a fresh `GOCACHE` only when explicitly validating cold-cache behavior
  or investigating cache corruption. Place it below
  `$CODEX_BUILD_CACHE_ROOT/fresh`, run that build sequentially, and remove only
  that task-owned directory after the command finishes.

---

## 4. Codegen pipeline (how APIs become CRDs and kcp schemas)

First-party APIs live under `apis/<group>/v1alpha1/` and follow standard
Kubernetes API-machinery conventions:

- `doc.go` — package doc + `// +groupName=<group>` marker.
- `groupversion_info.go` — `GroupVersion` + scheme registration.
- `types_*.go` — Go types with kubebuilder markers
  (`//+kubebuilder:object:root=true`, `//+kubebuilder:resource:...`, etc.).
- `zz_generated.deepcopy.go` — generated; do not hand-edit.

`make codegen` (→ `hack/update-codegen-crds.sh`) runs:

1. **controller-gen object** → deepcopy methods for every `apis/` package.
2. **controller-gen crd** → CRDs into `config/crds/`, copied into
   `pkg/hub/bootstrap/crds/` (embedded into the hub binary).
3. **apigen** (kcp) → `APIResourceSchema`s + per-group `APIExport`s into
   `config/kcp/`.
4. Merged `core.railgrid.ai` APIExport generated from the individual exports.

Rules of thumb:
- Change a type in `apis/` → run `make codegen` and commit the generated diff.
- API lists that may need metadata later should use structs with a `name` field
  (YAML shape `- name: ...`) rather than raw `[]string`; this keeps the API
  extensible without a breaking shape change.
- kcp treats `APIResourceSchema`s as **immutable**; schema names carry a version
  segment, so regeneration creates a new schema rather than mutating one.
- CI runs `make verify-codegen` — an uncommitted generated diff fails the build.

API groups: `railgrid.ai`, `tenancy.railgrid.ai`,
`providers.railgrid.ai`. Provider APIs use `<name>.providers.railgrid.ai`.

---

## 5. Provider architecture

A **provider** is a pluggable platform extension. It can supply any of:

- An **APIExport** in kcp (custom APIs tenants bind to) — usually the core of it.
- A **UI micro-frontend** served under `/ui/providers/{name}/*`.
- A **backend HTTP service** proxied at `/services/providers/{name}/*`, built
  with `provider-sdk/serve` and serving only the contract's route classes.
- **Controllers** reconciling provider resources.

A provider does **not** get a virtual workspace of its own:
`spec.virtualWorkspace` is retired, and hub-only endpoints are reserved path
prefixes on `spec.backend.url` (`provider-sdk/serve.HubOnlyPrefixes`).

### 5.1 The CatalogEntry manifest

Every provider ships a `manifest.yaml` that is a `CatalogEntry`
(`providers.railgrid.ai/v1alpha1`, type at
`apis/providers/v1alpha1/types_catalogentry.go`). It declares display metadata,
the UI/backend URLs, a health path, and the APIExport name + permission claims.
It is the only place a permission claim is written: codegen turns it into the
provider's APIExport. The hub's catalog controller reads the CatalogEntry and
registers routing/heartbeat state; the provider's own `init` creates the kcp
side (schemas, APIExport, endpoint slice, bind grant).

> **⚠️ A provider ships exactly TWO declarative objects, and `init` applies both
> verbatim.**
>
> 1. The **CatalogEntry** — `manifest.yaml`, hand-written. The single source for
>    display metadata, URLs, actions, self-hosting, **and the APIExport's name and
>    permission claims**.
> 2. The **APIExport** — `config/kcp/apiexport-<exportName>.yaml`, **generated**.
>    `make codegen-<name>-provider` runs kcp's `apigen` for `spec.resources`, then
>    `provider-sdk/cmd/apiexportgen` renames the export to
>    `spec.apiExport.name` and stamps `spec.permissionClaims` from the manifest.
>
> Everything else is an **output**: `deploy/chart/files/apiexport.yaml` and
> `deploy/chart/files/schemas/` are copies the codegen target writes, and
> `deploy/chart/templates/catalogentry.yaml` is the chart's rendering of the
> CatalogEntry. Do not hand-edit an output; change the input and re-run codegen.
>
> There is **no claim list in Go**. `providers/<name>/init_cmd.go` reads
> `RAILGRID_KCP_DIR` (the chart's `files/`, baked into the image at
> `/etc/railgrid/kcp`), applies the schemas and the generated APIExport as they
> stand, and creates the endpoint slice and bind grant. The one thing `init`
> adds at runtime is `identityHash` for first-party (`*.railgrid.ai`) claim
> groups, which is per-installation and comes from configuration
> (`RAILGRID_IDENTITY_HASHES`); a missing one is a hard failure, not a silent
> unpinned claim.
>
> `deploy/chart/templates/catalogentry.yaml` still has to mirror `manifest.yaml`
> for the WHOLE spec, not just claims — it is what actually reaches prod, and the
> two drift silently (a new `ui.children` sidebar item, a display field, a URL, …
> changed in only one place never ships). `hack/verify-provider-contract.mjs`
> fails the build on all of it: `manifest-chart-parity`, `claims-parity`
> (manifest vs the generated APIExport, including `metadata.name`) and
> `export-copy` (the chart copy is byte-identical to the generated file).
>
> **Existing tenants do NOT auto-migrate.** `init` only touches the provider-side
> APIExport, never per-tenant `APIBinding`s (they live in tenant workspaces, written
> by the hub Enable flow). A tenant's binding keeps its old accepted claims until it
> re-Enables or a migration re-accepts them — and a provider that starts *requiring*
> a newly-added claim (e.g. delegated `tokenreviews`/`subjectaccessreviews` for
> data-plane auth) will break every already-enabled tenant on rollout. Ship a
> migration (re-accept claims on all existing bindings) or a compatibility fallback
> BEFORE deploying code that depends on the new claim. Symptom of the gap: provider
> logs `User "system:serviceaccount:default:provider" cannot create <resource>` and
> the binding's `status.exportPermissionClaims` lists the claim but `spec` /
> `status.appliedPermissionClaims` do not.

> **⚠️ Adding an edges "service" type touches FOUR places (edges provider).**
> The edges provider turns host/LAN apps (Home Assistant, the *arr apps, UniFi, …)
> into MCP tools via a `Service` CR with a `spec.type`. To add a type:
> 1. `providers/edges/apis/v1alpha1/types_service.go` — the `ServiceType`
>    kubebuilder enum + constant, then **`make codegen-edges-provider`** (regenerates
>    the Service CRD/APIResourceSchema/chart schema; only the services schema bumps).
> 2. `providers/edges/internal/tunnel/svc_catalog.go` — the `svcCatalog` entry
>    (default port, auth scheme, the HTTP operations exposed as MCP tools). Home
>    Assistant is the exception (hand-coded in `mcp_service.go`).
> 3. `providers/edges/portal/src/Services.vue` — the `PRESETS` array that drives the
>    **UI type dropdown**. Miss this and the type builds/works but never appears in
>    the portal (the enum/schema does NOT drive the `<select>`).
> 4. `providers/edges/contrib/manifests/<type>/` — an example `Service` manifest.
>
> Reachability: a `Service` on a `LinuxServer` edge hits the agent host loopback by
> default; `spec.host` points it at another device on the edge's LAN (e.g. a UniFi
> console) — the agent's svc proxy (`pkg/agent/tunnel/svc.go`) dials loopback
> always, cluster DNS in kubernetes mode, and any other host only inside the
> agent's `--svc-allow-cidr` ranges (link-local never), with `--svc-policy`
> `warn` (this release's default: dial + log) or `enforce` (403) deciding what a
> denial does. A `Service` on a `KubernetesCluster` edge uses `spec.targetRef`
> (a cluster-DNS Service) instead.
> The portal create form (`Services.vue`) branches on the selected edge's kind.

### 5.2 Hub-side provider integration (`pkg/hub/providers/`)

| File | Role |
|------|------|
| `provision.go` | Creates the kcp sub-workspace and the `provider` ServiceAccount, and mints the provider kubeconfig. It does **not** apply schemas or the APIExport — the provider's own `init` does, from `RAILGRID_KCP_DIR` (§5.1) |
| `proxy.go` | UI reverse-proxy (`/ui/providers/{name}/*`) + backend proxy (`/services/providers/{name}/*`); injects tenant/user headers |
| registry / controller / heartbeat | In-memory routing table, catalog reconcile, `POST /api/providers/{name}/heartbeat` liveness (TTL ~90s) |
| `pkg/hub/provider_tenant_resolver.go` + `provider_cluster_resolver.go` | Resolves caller identity → tenant workspace → kcp logical-cluster ID; the proxy injects `X-Railgrid-User` and the ID as both `X-Railgrid-Tenant` / `X-Railgrid-Cluster` (never the path), strips spoofed inbound copies |

Heartbeat: standalone providers POST every ~30s through the one shared
client in `provider-sdk/hubclient` (`ConfigFromEnv` + `RunHeartbeat`), which
reads `RAILGRID_HUB_URL`, `RAILGRID_PROVIDER_NAME`, `RAILGRID_HUB_INSECURE` and
`RAILGRID_PROVIDER_VERSION`. Do not copy the loop into a provider: TLS, token
and retry behaviour must change in one place. `HeartbeatConfig.CanSend` is
**required** — `RunHeartbeat` logs `ErrNoReadinessGate` and refuses to start
without it, so a provider whose watches are dead cannot keep reporting alive.
Wire it to the provider's real readiness (`vwhealth.Readiness.Check`, or
whatever gates `/readyz`). The beat is authenticated as
the provider's own service account: the bearer is `RAILGRID_HUB_TOKEN` if set,
otherwise the token inside `RAILGRID_PROVIDER_KUBECONFIG`
(`hubclient.ResolveHubToken`), and the hub verifies it by TokenReview in the
provider's workspace (`heartbeat_auth.go`). `--provider-heartbeat-auth=warn|enforce`
picks whether a failed check is logged or rejected (default `warn` this
release, `enforce` next); the client logs a 401/403 with what to fix. A
provider is "Ready" only when its endpoints are valid and (once heartbeats
have started) not stale.

### 5.3 Provider portal micro-frontends

Provider UIs are independent Vite/TS bundles in `providers/{name}/portal/`,
built to `dist/` and embedded via `//go:embed` in the provider's `assets.go`. The
portal loads each `/ui/providers/{name}/main.js` as a classic script into its
own document — pinned with the SRI hash the hub computed at registration
(`CatalogEntry.status.ui.mainJSIntegrity`, exposed as `mainJSIntegrity` on
`/api/providers`) — and renders the **custom element** it registers
(`<railgrid-provider-{name}>`). The host sets `element.railgridContext` (user, tenant,
orgUUID/workspaceUUID, resolved theme, basePath, subPath, and a host-owned
`fetch`) as a JS property and re-pushes it on every change; there is no iframe
and no postMessage handshake. **A provider bundle executes as fully trusted code
in the portal document.** Bundles must reach the hub through
`railgridContext.fetch` (`portalkit/tenant.ts` `providerFetch(ctx)`), which injects
`Authorization` + tenant headers and allows only the provider's own
`/services/providers/{name}/` and `/ui/providers/{name}/`, `/clusters/`,
`/api/orgs/{org}/`, and GET/HEAD `/api/providers`. `railgridContext.token`
is deprecated (one-release fallback) and will be removed. See
[`docs/providers.md`](docs/providers.md) §"Portal changes" and §"Security
considerations".

Build chain (Makefile):
- `make build-{name}-provider-portal` — `vite build` only.
- `make build-{name}-provider` — portal + Go binary (portal embedded).

The Tilt dev loop proxies all UI to the Vite dev server (`--portal-dev-url`) and
skips the slow provider-portal builds.

### 5.7 Shared portal UI kit (`portalkit`)

Shared UI primitives live under `provider-sdk/` and are **copy-synced** into each
portal's `src/portalkit/`, because the portals build self-contained (no npm
workspace / symlink — a standalone Docker build context must work). Edit the
**canonical** source, then `make sync-portalkit`; CI runs `make verify-portalkit`
(and it's in `make verify`) to fail on drift.

The shared visual authority is `provider-sdk/portalkit/railgrid-ui.css`. The host
copy at `portal/src/assets/railgrid-ui.css` and each vendored
`src/portalkit/railgrid-ui.css` are exact sync outputs; the verifier also rejects
unmanifested canonical files and unexpected copies. Standalone bundles call
`ensureRailgridUIStyles()`, which accepts the host only when its computed
`--railgrid-ui-canonical: 1` marker has a compatible `--railgrid-ui-core-version`. If the
host is stale, the bundle appends its exact vendored stylesheet under a
versioned fallback ID. It never overwrites an existing style element.

AI conversation, workbench, and model presentation is an optional layer in
`provider-sdk/agentkit/` and `provider-sdk/agentkit-vue/`. The same sync script
copies it only to explicit `AGENTKIT_PORTALS` consumers (Agents and App Studio)
as `src/agentkit/`. AgentKit depends on PortalKit; core PortalKit does not import
AgentKit or its styles. `ensureAgentUIStyles()` loads the optional CSS with its
own marker/version. Edit canonical sources and register new files/consumers in
the manifest; see `provider-sdk/agentkit/README.md` for the import mapping.

- **`provider-sdk/portalkit/`** — plain-TS kit for the **string-building
  (vanilla-TS)** Quickstart portal:
  - `icons.ts` — `ic('name')` returns an inline SVG string (self-injects its
    `.ic` sizing). Use instead of emoji.
  - `modal.ts` — `confirmModal()` / `alertModal()` (promise-based, replaces
    native dialogs).
  - `tenant.ts` — see below.
- **`provider-sdk/portalkit-vue/`** — kit for the **Vue SFC** portals (`agents`,
  `code`, `edges`, `app-studio`, `infrastructure`, `kuery`, root
  `portal`):
  - `confirm.ts` + `ConfirmDialog.vue` — promise `confirmDialog()` (mount one
    `<ConfirmDialog />` at the app root).
  - `ResourceTable.vue`, `ConditionsPanel.vue`, `StatusBadge.vue`.

**`tenant.ts` is security-critical** and shared by BOTH kinds (plain TS). It owns
the ONE copy of the hub-proxy contract — `readTenant()` (localStorage
`railgrid:portal:tenant`), `tenantHeaders({json})` (`X-Railgrid-Org` +
`X-Railgrid-Workspace`; `token` is a deprecated fallback), `providerFetch(ctx)`
(the host-owned `railgridContext.fetch` that injects `Authorization` + the tenant
scope, falling back to `fetch` + `ctx.token` on older hosts), and
`serviceBase()` (`/ui/providers/*` → `/services/providers/*`). The wrong
header/key means 401/403, so **do not re-inline this** — call the helpers and
never call the global `fetch` for a hub request.

**There is one auth model, not two.** A portal reads and writes its bound CRs
with the `portalkit` kube client over the hub's kcp proxy at
`/clusters/<cluster>`, addressing kcp by cluster in the path and
authenticating with the caller's bearer. It calls its own
`/services/providers/<name>` backend **only** for the closed set of Pillar 2
classes — a data-plane verb or action on a bound resource, `/mcp`, health,
the browser OAuth routes, the agent tunnel, and signed inbound webhooks (see
[provider-connectivity-contract.md](docs/provider-connectivity-contract.md)).
`tenantHeaders` supplies org/workspace scope where a backend call needs it;
it is addressing, never authorization.

**There is no `/api/*`.** The portals that used to drive everything through
their own REST (`agents`, `app-studio`, `kuery`, `quickstart`) have been
migrated; `provider-sdk/serve.New` refuses to register a route outside the
layout, and `hack/verify-provider-contract.mjs` fails the build on an `"/api/`
route literal in `main.go`, `server/` or `api/` (check `adhoc-rest`, currently
with **no** exceptions in `hack/provider-contract-exceptions.json`). If a
portal needs to list, create or patch something, that is a CR and the kube
client, not a new backend route.

Rule of thumb: **need a confirm, an icon, a table, a status pill, or tenant
headers → import from `portalkit`, don't reinvent.** New shared primitive → add
it to the canonical source under `provider-sdk/` and re-sync.

### 5.4 Tenant isolation in providers

Provider request handlers that talk to kcp build a **per-(tenant, caller) dynamic client**: the
hub forwards the caller's bearer token plus the tenant workspace's kcp
logical-cluster ID (in both `X-Railgrid-Tenant` and `X-Railgrid-Cluster` — the
workspace path is never sent); the provider's `tenant/` package (`client.go`,
`credentials.go`) constructs a client scoped to `<host>/clusters/<clusterID>`,
acting as the caller in their workspace. A provider that needs the org /
workspace UUIDs or the path resolves them from kcp with
`provider-sdk/tenantaccess.ResolveWorkspace` (the `LogicalCluster`
`kcp.io/path` annotation, read as the caller), never by parsing a header. See
`providers/code/tenant/` and `providers/infrastructure/tenant/` for the
canonical pattern, and `docs/provider-scoping.md`.

#### The backend surface: one layout, one grammar, two gates

A provider's HTTP server is built by
[`provider-sdk/serve`](provider-sdk/serve/serve.go) — `serve.New(Options{...})`
returns the whole `http.Handler` with the fixed layout (`/healthz`, `/readyz`,
`/mcp` + `/mcp/sse`, `/dataplane/`, `/actions/`, `/workload-identities/*`
hub-only, `/oauth/`, the portal file server with SPA fallback) and **refuses to
register anything else**, so `/api/*` cannot be added by accident.

Every tenant-facing verb is a route in the one grammar and goes through
[`provider-sdk/dataplane`](provider-sdk/dataplane/):

```
/{dataplane|actions}/clusters/{clusterID}/{resource}/{name}[/components/{c}]/{verb}[/{tail}]
```

- `dataplane.ParsePath` is the only parser. It rejects a workspace path in the
  cluster position, the legacy `apis/` resource position, and a path cluster
  that disagrees with `X-Railgrid-Cluster`.
- `dataplane.Gate` runs **both gates as the caller**, for *every* verb — not
  just the dangerous-looking ones: (1) a real `GET` of `{resource}/{name}` in
  the path's cluster with the caller's bearer, which proves visibility and
  returns the object to pin UID/spec against; then (2) a
  `SelfSubjectAccessReview` for **`create` on `{resource}/{verb}`**,
  name-scoped (`dataplane.SSARVerb`). There is no hub-side authorizer: kcp RBAC
  is the grant.
- `dataplane.Serve` enforces the declared limits and writes the `actionwire`
  envelope.
- Consumers never string-build another provider's URL — `dataplane.ProviderPath`
  renders it, from the provider name the consumer's own binding gives it.

**Declare every verb.** Each one is listed in `manifest.yaml` under
`spec.dataPlane.verbs` (`{resource, verb, description, stream?, readOnly?}`),
or, when it is versioned and schema'd, under `spec.actions`. Declaring grants
and serves nothing; what it buys is that the hub's scoped-identity service can
mint a capability for a coordinate it can **verify exists**
(`pkg/hub/identity/policy.go` clause C), instead of consumers hardcoding one
nobody validates. The declaration and the served table are kept in lockstep by
a test in the provider (e.g. `TestDataPlaneVerbsMatchManifest`). Run
`dataplane.ConformanceTest` against the real server.

#### Provider controllers and tenancy

- Controllers for tenant-owned resources must reconcile across the workspaces
  that have bound the provider's APIExport, within its platform or organization scope.
  Organization-owned or privately hosted does not imply single-workspace execution.
  Controllers for provider-owned or hosting-cluster resources may have a different
  scope; keep those clients and responsibilities explicit.
- Start from [Code's controller manager](providers/code/controller_manager.go)
  and [scoped client helper](providers/code/controller/shared/shared.go).
  Use `provider-sdk/apiexportprovider` with `multicluster-runtime`: it watches
  `APIExportEndpointSlice` endpoints and engages consuming workspaces. Resolve
  the client for each reconciliation from `req.ClusterName`; do not invent a
  separate tenant inventory or manually enumerate workspaces.
- Background controllers have no active caller. Unlike the request handlers
  above, they use the provider's ServiceAccount identity through its APIExport
  virtual workspace, with accepted permission claims for additional resources.
  Do not substitute admin credentials or retained interactive-user tokens.
- **A provider does not mint identities — it asks the hub.** Where background
  work needs a workspace-local credential (a per-agent run, a reconnecting edge
  agent, a per-project execution identity), request one from the hub's scoped
  identity service through
  [`provider-sdk/identityclient`](provider-sdk/identityclient/identityclient.go):
  TokenRequest-minted, TTL'd, `resourceNames`-scoped, garbage-collected with
  its owning object, and refused by `pkg/hub/identity/policy.go` unless it fits
  one of four clauses (own group; `get` on named foreign resources; `create` on
  a foreign `{resource}/{verb}` the owning provider **declares**; a closed
  platform allowlist). A provider therefore holds **no** `serviceaccounts`,
  `clusterroles` or `clusterrolebindings` permission claims — a claim on those
  types is a contract violation, not a design choice. (kuery and app-studio
  still carry theirs and still use `provider-sdk/tenantaccess`; that is
  outstanding debt, not the pattern to copy.)
- Keep tenant-owned desired state and durable reconciliation status in KRM
  resources in the owning workspace. Reconciliation must be idempotent and recover
  from persisted state; process memory must not be the sole authority for work
  ownership or completion. Use `provider-sdk/leaderelection` where singleton
  controller execution is required.
- Tenant scope belongs to each reconciliation. Do not restrict a shared controller
  to a fixed tenant cluster/namespace or require manually supplied per-tenant
  kubeconfigs as its normal installation contract. A fixed namespace for a
  provider-scoped leader Lease or an explicitly scoped runtime client is valid.
  Include tenant identity in shared cache, artifact, and external-operation keys.
- A single-tenant development fixture is acceptable; it must not silently become
  the production architecture. Any intentional per-tenant deployment model must
  be explicitly proposed and approved as an architectural exception.
- Connect controller watch health to readiness using `provider-sdk/vwhealth` and
  the APIExport provider's checker, as Code does. A responding HTTP server alone
  does not prove tenant resources are being reconciled.

Before claiming tenant-controller integration complete, verify:

- One provider installation reconciles identically named resources in two enabled
  workspaces with independent state, credentials, and outputs; tenant identities
  cannot read or mutate the other workspace's resources.
- A workspace enabled after controller startup is discovered and reconciled
  without manual configuration or controller deployment.
- Reconciliation recovers after a controller restart and a temporary watch failure
  without duplicate external operations.
- Failed controller watches appear in readiness, with recovery reflected when
  watching resumes. Record which checks used real kcp versus test doubles.
- Every data-plane verb passes `dataplane.ConformanceTest` against the real
  `serve.New` handler: wrong verb denied, foreign cluster denied, missing
  bearer 401, header/path mismatch 400, limits enforced, envelope shape.

### 5.8 Reconcilers, not loops

The tenancy rules above say how a tenant controller is wired; this section says
what a provider may not build instead. **Every state transition a provider owns
is a watch-driven reconciler; the only thing that runs on a clock is a call to a
system outside kcp.** No `--interval` passes, list-and-sweep functions,
in-process work queues or goroutines that own state. A timer loop scales with
tenant count instead of change rate, hides its scope in process memory, and
cannot be leader-elected or replayed. The reference rewrite is
`railgrid/providers/docs/reconciler-architecture-review.md` (planner and
factory went from four timed passes to reconcilers); read it before adding a
loop, and extend it when a controller's shape changes.

- **Every durable fact is a KRM field.** Assignment, retry deadline, last
  error, external IDs: `status` on the tenant object, or a private kind in the
  provider's own workspace (planner's `ActionReceipt`). Never a deployment
  ConfigMap, a policy file or a map in memory.
- **`RequeueAfter` has exactly two uses:** pacing a poll of a system that
  cannot be watched (a git host, a ticket source, a runner's HTTP API) and
  backing off a failed call. One kcp object learning that another changed is a
  `Watches` with a mapping func (`mchandler.ForCluster`), never a requeue and
  never a second list inside the reconcile. Provider-private kinds that are not
  exported are reconciled on `mgr.GetLocalManager()`.
- **Write loops are leader-elected; the HTTP surface is not.** The manager runs
  under `provider-sdk/leaderelection.Run` and is rebuilt per term (a stopped
  controller-runtime manager cannot restart); actions, MCP and the portal keep
  serving on every replica with per-request tenant clients (5.4). Informer
  configs get `Timeout = 0` — a client timeout severs a streaming watch — and
  per-request clients are bounded separately.
- **Cross-provider data is watched, not polled.** Mirror what another export
  publishes into your own kind and reconcile from that (factory's intake reads
  planner's `Issue` mirror, not the ticket source's actions); a loop over
  another provider's actions is how the Linear rate-limit incident happened.

Worked examples beyond Code: `providers/planner/internal/controllers/` and
`providers/factory/internal/global/` in railgrid/providers.

### 5.5 Provider inventory

All providers are **standalone**: own `go.mod` under `providers/{name}/`, own
image/pod, registered at runtime via their `CatalogEntry`. There are no
built-in providers any more (the former `mcp`/`kubernetesedges`/`serveredges`
built-ins were folded into the `edges` provider in #435; the MCP aggregate now
lives hub-side in `pkg/hub/mcpaggregate/`; `projects` was folded into
`app-studio`).

| Provider | APIExport | What it does |
|----------|-----------|--------------|
| `quickstart` | `quickstart.providers.railgrid.ai` | **Reference provider** — minimal HTTP server + embedded Vite portal + sample `Greeting` API. Start here. |
| `edges` | `edges.providers.railgrid.ai` | The connectivity core: `KubernetesCluster`/`LinuxServer` edges, revdial tunnel termination, kubectl/SSH/MCP proxying, `Service` connectors (host/LAN apps → MCP tools), `Workload`/`Placement` scheduling + Helm marketplace. Horizontally scalable: reconcilers are leader-elected, the tenant-config resolver runs active-active on every replica, and an agent's tunnel is owned by the replica it dialled and relayed to from the others over a pod-to-pod listener (chart still defaults to `replicaCount: 1`). |
| `infrastructure` | `infrastructure.providers.railgrid.ai` | Application Templates via kro: template catalog, instance provisioning, data plane (exec/logs/etc.), app hosting + access gate |
| `code` | `code.providers.railgrid.ai` | Git hosting management (repos, deploy keys, collaborators, packages) behind a `GitBackend` seam; GitHub is the only real backend today |
| `databricks` | `databricks.providers.railgrid.ai` | Databricks SQL warehouse tables via governed `query_table` action + MCP tools; private source in railgrid/providers; platform installation supported |
| `agents` | `agents.railgrid.ai` | Long-running personal AI agents: chat, schedules, triggers, approvals, budgets, memory, multi-channel (Slack/Telegram/Discord/SMTP). Needs hub + Postgres only |
| `app-studio` | `ai.railgrid.ai` | Persistent AI project workspace (projects, sessions, dev sandboxes, publishing, skills) |
| `kuery` | `kuery.providers.railgrid.ai` | Fleet-wide object query, relationship traversal, impact analysis across connected edges + MCP tools |

Per-provider deep docs: `docs/code-provider-architecture.md`,
`docs/infrastructure-architecture.md`, `docs/kuery-provider-architecture.md`,
`docs/agents-provider-architecture.md`,
`docs/application-template-architecture.md`, `docs/edges-marketplace.md`,
`docs/mcp-architecture.md`, `docs/providers.md`, `docs/provider-publishing.md`,
`docs/provider-scoping.md`, `docs/byo-providers.md`.

The table above is the **platform** catalog, at `root:railgrid:providers:<name>`.
An organization can also register its own provider — one it runs itself, usually
in its own cluster — at `root:railgrid:tenants:<orgUUID>:providers:<name>`. Those
reuse the same `provider` WorkspaceType and the same `provider-sdk/install`
path, so nothing about writing a provider changes; what differs is who
provisions the workspace (`POST /api/orgs/{org}/providers` instead of admin
onboarding) and that the catalog scopes them to the owning Org. The registry is
keyed by `(orgUUID, name)`, and `Registry.Get` stays platform-only so an Org
cannot capture a platform provider's proxy or heartbeat route by name. See
`docs/byo-providers.md`.

### 5.6 Adding / modifying a provider — checklist

1. Scaffold from `providers/quickstart/` (closest minimal example).
2. Define APIs under `apis/v1alpha1/` (or inline schemas in the manifest);
   regenerate deepcopy if you keep Go types.
3. Write `manifest.yaml` (CatalogEntry): displayName, ui/backend URLs,
   `healthPath: /readyz`, apiExport name + permission claims, **and every verb
   the backend serves** under `spec.dataPlane.verbs` (or `spec.actions`). No
   `serviceaccounts`/`clusterroles`/`clusterrolebindings` claims — identities
   come from the hub (§5.4). Then `make codegen-<name>-provider`: the APIExport
   and the chart's `files/` are **generated outputs** (§5.1), and
   `init_cmd.go` reads them from `RAILGRID_KCP_DIR` rather than carrying a
   claim list in Go.
4. Build the portal (`providers/{name}/portal/`, embedded via `assets.go`). It
   reads and writes its CRs with the `portalkit` kube client; it does not get
   its own REST surface (§5.3).
5. Build the server with `provider-sdk/serve.New` — the only sanctioned layout,
   and the thing that makes `/api/*` impossible. Serve every tenant verb
   through `provider-sdk/dataplane` (`ParsePath` → `Gate` → `Serve`), and keep
   the served verb table in lockstep with `spec.dataPlane.verbs` (§5.4).
6. Wire the heartbeat with `provider-sdk/hubclient` (`ConfigFromEnv` +
   `go RunHeartbeat`) — never a local copy — plus a tenant-scoped client if it
   talks to kcp. `CanSend` is **required**, not optional: point it at the
   provider's readiness (`vwhealth.Readiness.Check`, the same gate as
   `/readyz`), or `RunHeartbeat` refuses to start. Anything that reacts to tenant objects is a multicluster-runtime
   reconciler under leader election, not a loop (§5.8).
7. Add Makefile `build-{name}-provider[-portal]` + `run/install/uninstall`
   targets if standalone; add the module to `go.work`.
8. Add an e2e suite under `test/e2e/suites/` if it has tenant-isolation or
   provisioning behavior worth guarding, and run
   `dataplane.ConformanceTest` against the real handler.
9. Write **two** READMEs: `providers/{name}/README.md` (what the provider is and
   its APIs) and `providers/{name}/deploy/chart/README.md` (values reference).
   The chart README is user-facing: charts embed it into their CatalogEntry
   (`valuesDoc: |{{ .Files.Get "README.md" | nindent 10 }}`) and the portal
   renders it inline in the Self-Hosting flow. It is the answer to "what can I
   configure?" and must not go stale.
10. Run `make verify-provider-contract` (it is in `make verify`). It checks
    manifest/chart parity, claims parity against the **generated** APIExport,
    that the chart's export copy is byte-identical, both READMEs, and that no
    `"/api/` route literal exists in `main.go`, `server/` or `api/`. An
    exception in `hack/provider-contract-exceptions.json` needs a reason — the
    file is currently empty, and it is meant to stay that way.
11. To offer the provider for self-hosting, declare `spec.selfHosting` in **both**
    `manifest.yaml` and `deploy/chart/templates/catalogentry.yaml` (chart
    coordinates, namespace, release name, `docsURL` → the chart README, and any
    `requiredValues`). Prefer placeholders — `{{hubURL}}`, `{{workspacePath}}`,
    `{{kubeconfigSecret}}` — over values the installer must look up. See
    `docs/byo-providers.md`.

---

## 6. Hub architecture (`pkg/hub/`, `cmd/railgrid-hub/`)

The hub is the only publicly-reachable component. Key areas:

- `server.go`, `options.go`, `scheme.go` — server wiring + config + scheme.
- `bootstrap/` — embedded CRDs and kcp resources applied at startup
  (`startup_retry.go` hardens this against ordering races).
- `kcp/` — embedded/external kcp integration.
- `controllers/` — edge lifecycle reconcilers
  (`TokenReconciler`, `RBACReconciler`, `EdgeController` — see DEVELOPERS.md §
  Hub Controller Reference).
- `providers/` — provider integration (see §5.2).
- `tenant/`, `provider_tenant_resolver.go` — org/workspace middleware + identity
  resolution.
- `restapi/`, `serviceaccounts/`, `quota/`, `portal*.go` — REST API surface,
  SA management, quotas, portal serving.
- `pkg/virtual/builder/` — kcp virtual-workspace handlers: the agent-proxy
  (tunnel auth, status, SSH creds) and the multi-cluster MCP server.

The **agent** lives in `pkg/agent/` (tunnel, ssh, reporters) and ships inside the
`railgrid` CLI binary (`railgrid agent run`). The join-token → kubeconfig exchange and
the SSH/MCP request flows are documented end-to-end in `DEVELOPERS.md`.

---

## 7. Testing

### Unit tests
```bash
make test          # everything except test/e2e
make test-util     # pkg/util only (fast)
```
Standalone providers: run `go test ./...` inside the provider directory.

### E2E suites (`test/e2e/suites/`)

Each suite has a dedicated Make target. Most spin up their own hub on fixed ports
— **do not run port-colliding suites concurrently** (the targets pre-check with
`lsof`).

| Target | Suite | What it covers |
|--------|-------|----------------|
| `make e2e` / `make e2e-standalone` | `standalone` | Embedded kcp + static token, no Dex (default) |
| `make e2e-ssh` | `ssh` | SSH server-mode edges |
| `make e2e-oidc` | `oidc` | Dex OIDC auth |
| `make e2e-external-kcp` | `external_kcp` | kcp via Helm in kind |
| `make e2e-provider` | `provider` | Provider provisioning (quickstart) |
| `make e2e-provider-flags` | `providerflags` | `--providers` flag mechanics (dep validation, filtering) |
| `make e2e-tilt-cluster` | `tiltcluster` | Against a live `make tilt-cluster` multi-shard stack |
| `make e2e-install-external` | `installexternal` | Runs `hack/install/` scripts from docs/install-external-kcp.md (two-shard kcp via kcp-operator + Envoy gateway) |
| `make e2e-install-embedded` | `installembedded` | Runs `hack/install/` scripts from docs/install-embedded-kcp.md (embedded kcp + gateway) |
| `make e2e-all` | all | Builds hub+agent images, runs everything (~30m) |

E2E knobs: `E2E_FLAGS` (e.g. `--keep-clusters` via `make e2e-keep`),
`E2E_TIMEOUT`. `standalone`/`ssh`/`oidc`/`external_kcp` build Docker images first
and load them into kind; `provider*`/`infrastructure` run binaries directly on
local ports. Framework helpers live in `test/e2e/framework/`.

### Local dev loop (Tilt)
```bash
make tilt    # portal (Vite :3000) + hub (HTTPS :9443, embedded kcp, static auth)
tilt down
curl -k https://localhost:9443/healthz
```
`make tilt` wraps `tilt up -f Tiltfile` with the port/kcp conflict checks; plain
`tilt up` still works. `Tiltfile.cluster` / `make tilt-cluster` brings up the
operator-deployed multi-shard stack used by the `tiltcluster` e2e suite. Run one
or the other — both bind `:9443` and share `.kcp/`.

---

## 8. UI & design standards (portal + provider micro-frontends)

The main portal and provider micro-frontends share one design system and must
read as one product. The structured knowledge base at
[`docs/design/README.md`](docs/design/README.md) is the operational entrypoint;
the legacy [`docs/design-book.md`](docs/design-book.md) path is only a
compatibility pointer.

Before changing UI, select applicable contracts: [foundations](docs/design/foundations/)
for tokens, theme, type, geometry, icons, and integration; [patterns](docs/design/patterns/)
for page, form, navigation, resource-read, and creation composition;
[components](docs/design/components/) for reusable primitives and PortalKit
assets; [quality](docs/design/quality/) for conformance, review, exceptions, and
oddities; [AI](docs/design/ai/) for conversation, autonomy, and evidence;
[content](docs/design/content/) for product copy; and
[accessibility](docs/design/accessibility/) for keyboard, focus, semantics, and
interaction. Read relevant entries, not the whole directory.

Implementation authority remains the canonical root tokens and shared
PortalKit sources (`portal/src/assets/main.css`, `provider-sdk/portalkit/`, and
`provider-sdk/portalkit-vue/`), optional AgentKit sources
(`provider-sdk/agentkit/`, `provider-sdk/agentkit-vue/`), plus existing host components in
`portal/src/components/`. Reuse those contracts; provider-local copies are
distribution outputs, not new authorities. A new primitive or shared recipe is
added canonical-first, then propagated with `make sync-portalkit`; never invent
a provider-local variant or edit a vendored copy directly.

Treat each entry's `status`, `authority`, `implementation.state`, and
`verification` metadata as part of the contract. Draft/proposed entries and
planned, partial, or retired implementation states do not prove a shipped
surface, and unverified guidance must not be reported as verified. Update the
design entry when the implementation boundary or evidence changes.

For every UI change, preserve the shared token/component vocabulary and check
the applicable quality contract. Run the focused gates before handoff:
`make verify-design-docs`, `make verify-portalkit`, and
`make verify-ui-conformance`. The KB schema, routing rules, and exception policy
are documented in [schema.md](docs/design/schema.md) and the linked quality
entries; do not duplicate them here.

For appearance changes, add a proportionate rendered check on the affected
route or component at representative viewport sizes. Check both `html.dark`
and `html.light` whenever appearance changes; record browser evidence separately
from source, parity, and conformance gates, which do not prove rendered,
responsive, or interaction behavior.

---

## 9. Conventions & guardrails

- **Always go through the Makefile** for build/test/lint/codegen — it pins tool
  versions into `hack/tools/`.
- After editing any `apis/` Go type, run `make codegen` and commit the generated
  diff; CI enforces `make verify-codegen`.
- Run `make fix-lint && make lint` before committing Go changes. Match
  surrounding style; imports group railgrid last (goimports local-prefix).
- Don't hand-edit `zz_generated*` or `config/crds` / `config/kcp` outputs.
- License boilerplate is required on Go files (generated files exempt);
  `make boilerplate` adds it.
- The hub binary embeds CRDs (`pkg/hub/bootstrap/crds`) — rebuild the hub
  after changing them so the embedded FS stays in sync. Provider portals embed
  into their own provider binaries, not the hub.
- Standalone providers are separate modules: changes there need their own
  build/test and `go.work` awareness; they are not in the root `./...`.
- Before merging: `make verify` is the full gate
  (boilerplate + codegen + vet + lint + build + test).
- **Infrastructure templates declare configurable inputs (container images,
  versions, sizes) as `spec.schema` fields with sane defaults** — never via
  `${railgrid.*}` env-substitution tokens. Fixed sidecar images (e.g. the
  control-token `kubectl` job) are hardcoded literals. `${railgrid.*}` tokens are
  reserved for the handful of genuinely platform-global values with no universal
  default: the exposure Gateway parent (`${railgrid.gatewayName}` /
  `${railgrid.gatewayNamespace}`), the dev-overlay images
  (`${railgrid.devImage.<toolchain>}` / `${railgrid.devAgentImage}`), and the
  exposure-URL port suffix (`${railgrid.appPublicPort}`). A missing env must never
  be able to produce an empty/invalid field. See
  [`providers/infrastructure/docs/template-conventions.md`](providers/infrastructure/docs/template-conventions.md).
- **Providers are isolated; never reach into another provider's backend.** A
  provider's backend layer (its runtime/target clusters and their
  credentials, databases, internal Services, controllers, kro RGDs) is
  private to it. A provider must not hold a second credential into another
  provider's cluster/DB/service, call its internal endpoints directly, or
  hardcode its backend topology. Cross-provider access goes **only** through
  the other provider's published `APIExport` resources + virtual-workspace
  subresources, invoked **as the tenant user** and routed by binding (not by
  a backend URL). This is what makes BYO compute work and bounds blast
  radius. See [`docs/providers.md` §"Provider isolation"](docs/providers.md#provider-isolation-the-cross-provider-boundary)
  and contract 3 in
  [`docs/provider-connectivity-contract.md`](docs/provider-connectivity-contract.md).
- **Reconcilers, multicluster-runtime and KRM — always, where at all possible.**
  A provider never owns a timer loop, a sweep or an in-memory queue over kcp
  objects: it watches them through its APIExport virtual workspace with
  multicluster-runtime and keeps every piece of durable state in `status` or a
  private kind. `RequeueAfter` is reserved for pacing an external system that
  cannot be watched and for backoff. See §5.8 and
  [`docs/providers.md` §"Minimal provider backend contract"](docs/providers.md#minimal-provider-backend-contract).

---

## 10. Cross-repo boundaries & known gotchas

railgrid runs on kcp; some symptoms that look like railgrid bugs are actually upstream:

- **OpenAPI proxy misbehaving** — railgrid serves OpenAPI/discovery through a kcp
  virtual workspace. Broken VW OpenAPI serving surfaces as hub-side proxy
  issues; the fix is usually kcp-side, not railgrid. Check the kcp VW openapi path
  before assuming the bug is in the railgrid proxy.
- **`kubectl get <resource>` "temporarily unavailable" for one resource in an
  APIBinding (e.g. templates), intermittently** — APIExport *virtual storage*
  (CachedResource) discovery fails when the consumer workspace is on a different
  kcp shard than the provider. It's a kcp cross-shard discovery bug, not railgrid
  config — don't chase the railgrid install code. Workaround for local dev: run a
  single kcp shard, or co-locate provider + consumer on one shard.

---

## 11. Where to look next

- `DEVELOPERS.md` — Edge CRD spec, join-token flow, proxy URL format, SSH
  internals, kcp workspace hierarchy, MCP integration, hub controllers.
- `docs/providers.md` + per-provider arch docs — provider plane deep dives.
- `docs/security.md`, `docs/organizations.md`, `docs/provider-scoping.md` —
  tenancy + isolation model.
- `docs/hub-proxy-workspace-access.md` — the hub kcp proxy's membership-gated
  per-workspace access (`/clusters/{cluster}`).
- `CONTRIBUTING.md` — contribution workflow.

Linear and Databricks implementation, build, codegen, portal copies and release
automation live in `railgrid/providers`. Do not recreate their source directories
or add private-repository access to public CI. They remain hub-managed provider
installations; source ownership does not imply self-hosting-only support.
