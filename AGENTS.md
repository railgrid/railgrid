# AGENTS.md

Orientation for AI agents (and humans) working in the **faros** repo. Read this
before making changes. It explains the architecture, where hub code ends and
provider code begins, how APIs are constructed, and the exact commands to build,
test, format, lint, and regenerate code.

> Module: `github.com/faroshq/faros` · Go workspace (`go.work`) · kcp-based
> multi-tenant control plane.

Deeper references live in [`DEVELOPERS.md`](./DEVELOPERS.md) and [`docs/`](./docs)
(per-provider architecture docs, security, organizations, graphql, mcp). Keep
those authoritative — this file is the map, not the territory.

---

## 1. What faros is

faros connects distributed Kubernetes clusters and bare-metal servers through one
control plane (the **hub**). Edge **agents** dial outbound reverse tunnels to the
hub, so clusters behind NAT/firewalls become reachable through a single
authenticated endpoint. On top of that core, faros is also a **multi-tenant
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
apis/                 First-party API types (faros, tenancy, providers groups)
  faros/v1alpha1/       Edge, MCPServer, Placement, VirtualWorkload
  tenancy/v1alpha1/     Organization, User, Membership, UserMembershipIndex, Auth
  providers/v1alpha1/   CatalogEntry (the provider manifest type)
cmd/                  Binaries
  faros/                CLI (also the agent: `faros agent run`)
  faros-hub/            Hub control-plane server
  graphql/              GraphQL gateway (listener + gateway subcommands)
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
docs/                 Architecture docs (per-provider, security, mcp, graphql)
Makefile              The single source of truth for build/test/lint/codegen
Tiltfile              Local dev loop (embedded kcp + static auth)
go.work               Workspace: root + standalone provider modules
```

`go.work` members: `.`, `provider-sdk`, every provider module under
`providers/` (`agents`, `app-studio`, `code`, `databricks`, `edges`,
`infrastructure`, `kuery`, `quickstart`), and the external
`kubernetes-graphql-gateway` + `contrib-metering` checkouts. Every provider is
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
| Build all binaries | `make build` | faros CLI + hub + graphql |
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
  `github.com/faroshq/faros` (faros imports group last).
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
4. Merged `core.faros.sh` APIExport generated from the individual exports.

Rules of thumb:
- Change a type in `apis/` → run `make codegen` and commit the generated diff.
- API lists that may need metadata later should use structs with a `name` field
  (YAML shape `- name: ...`) rather than raw `[]string`; this keeps the API
  extensible without a breaking shape change.
- kcp treats `APIResourceSchema`s as **immutable**; schema names carry a version
  segment, so regeneration creates a new schema rather than mutating one.
- CI runs `make verify-codegen` — an uncommitted generated diff fails the build.

API groups: `faros.sh`, `tenancy.faros.sh`,
`providers.faros.sh`. Provider APIs use `<name>.providers.faros.sh`.

---

## 5. Provider architecture

A **provider** is a pluggable platform extension. It can supply any of:

- An **APIExport** in kcp (custom APIs tenants bind to) — usually the core of it.
- A **UI micro-frontend** served under `/ui/providers/{name}/*`.
- A **backend HTTP service** proxied at `/services/providers/{name}/*`.
- **Controllers** reconciling provider resources.
- Optionally a custom **virtual workspace**.

### 5.1 The CatalogEntry manifest

Every provider ships a `manifest.yaml` that is a `CatalogEntry`
(`providers.faros.sh/v1alpha1`, type at
`apis/providers/v1alpha1/types_catalogentry.go`). It declares display metadata,
the UI/backend/virtual-workspace URLs, a health path, the APIExport name +
permission claims, and inline `APIResourceSchema` bodies. The hub's catalog
controller reads it and provisions the kcp side (sub-workspace, ServiceAccount,
APIExport, schemas) and registers routing/heartbeat state.

> **⚠️ `manifest.yaml` and `deploy/chart/templates/catalogentry.yaml` are TWO
> copies of the CatalogEntry that MUST stay in sync — for the WHOLE spec, not just
> claims.** `manifest.yaml` is the source-of-truth/dev copy; the chart template is
> what actually gets applied in prod. They drift silently: a change to `manifest.yaml`
> alone (a new `ui.children` sidebar item, a display field, a URL, permission claims,
> …) never reaches prod. Symptoms seen in the wild: a sidebar sub-nav item present in
> `manifest.yaml` but missing in prod (e.g. edges "Services"); Enable offering the old
> claim set. **When you touch either, mirror the change into the other.**
>
> Permission-claim changes additionally need a THIRD edit — the **APIExport** side:
> - `providers/<name>/init_cmd.go` — the `sdkinstall.PermissionClaim` list that
>   `init` stamps onto the APIExport (`spec.permissionClaims`). If this drifts, `init`
>   updates the APIExport but the deployed CatalogEntry (what the hub Enable flow
>   offers tenants) still advertises the old set — so all three must match for claims.
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
> console) — the agent's svc proxy (`pkg/agent/tunnel/svc.go`) currently allows any
> host (see `isAllowedSvcHost`, `TODO(security)`). A `Service` on a
> `KubernetesCluster` edge uses `spec.targetRef` (a cluster-DNS Service) instead.
> The portal create form (`Services.vue`) branches on the selected edge's kind.

### 5.2 Hub-side provider integration (`pkg/hub/providers/`)

| File | Role |
|------|------|
| `provision.go` | Creates kcp sub-workspace, ServiceAccount, APIExport, applies inline schemas; mints the provider kubeconfig |
| `proxy.go` | UI reverse-proxy (`/ui/providers/{name}/*`) + backend proxy (`/services/providers/{name}/*`); injects tenant/user headers |
| registry / controller / heartbeat | In-memory routing table, catalog reconcile, `POST /api/providers/{name}/heartbeat` liveness (TTL ~90s) |
| `pkg/hub/provider_tenant_resolver.go` | Resolves caller identity → tenant workspace path; injects `X-Faros-User` / `X-Faros-Tenant`, strips spoofed inbound copies |

Heartbeat: standalone providers POST every ~30s with `FAROS_HUB_URL`,
`FAROS_HUB_TOKEN`, `FAROS_PROVIDER_NAME`. A provider is "Ready" only when its
endpoints are valid and (once heartbeats have started) not stale.

### 5.3 Provider portal micro-frontends

Provider UIs are independent Vite/TS bundles in `providers/{name}/portal/`,
built to `dist/` and embedded via `//go:embed` in the provider's `assets.go`. The
portal renders them as **custom elements** (`<faros-provider-{name}>`) that
receive a `faros-context` (user, tenant, theme, basePath) via the
`postMessage` `faros.ready` → `faros.context` handshake.

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

The shared visual authority is `provider-sdk/portalkit/faros-ui.css`. The host
copy at `portal/src/assets/faros-ui.css` and each vendored
`src/portalkit/faros-ui.css` are exact sync outputs; the verifier also rejects
unmanifested canonical files and unexpected copies. Standalone bundles call
`ensureFarosUIStyles()`, which accepts the host only when its computed
`--faros-ui-canonical: 1` marker has a compatible `--faros-ui-version`. If the
host is stale, the bundle appends its exact vendored stylesheet under a
versioned fallback ID. It never overwrites an existing style element, and a
newer host stylesheet always wins.

- **`provider-sdk/portalkit/`** — plain-TS kit for the **string-building
  (vanilla-TS)** Quickstart portal:
  - `icons.ts` — `ic('name')` returns an inline SVG string (self-injects its
    `.ic` sizing). Use instead of emoji.
  - `modal.ts` — `confirmModal()` / `alertModal()` (promise-based, replaces
    native dialogs).
  - `tenant.ts` — see below.
- **`provider-sdk/portalkit-vue/`** — kit for the **Vue SFC** portals (`agents`,
  `code`, `databricks`, `edges`, `app-studio`, `infrastructure`, `kuery`, root
  `portal`):
  - `confirm.ts` + `ConfirmDialog.vue` — promise `confirmDialog()` (mount one
    `<ConfirmDialog />` at the app root).
  - `ResourceTable.vue`, `ConditionsPanel.vue`, `StatusBadge.vue`.

**`tenant.ts` is security-critical** and shared by BOTH kinds (plain TS). It owns
the ONE copy of the hub-proxy contract — `readTenant()` (localStorage
`faros:portal:tenant`), `tenantHeaders({token, json})` (`Authorization` +
`X-Faros-Org` + `X-Faros-Workspace`), and `serviceBase()` (`/ui/providers/*` →
`/services/providers/*`). The wrong header/key means 401/403, so **do not
re-inline this** — call the helper. Two auth models coexist and this only covers
the first:
- **hub-proxy model** (uses `tenant.ts`): `agents`, `app-studio` (migrated);
  `kuery`/`quickstart` read the tenant off `faros-context` instead, so they only
  use `serviceBase`.
- **cluster-in-path model** (`code`, `edges`, `infrastructure`, and databricks'
  GraphQL): address kcp by `/graphql/<cluster>` or `/services/providers/<name>`
  with just the bearer token; they don't use `tenantHeaders`.

Rule of thumb: **need a confirm, an icon, a table, a status pill, or tenant
headers → import from `portalkit`, don't reinvent.** New shared primitive → add
it to the canonical source under `provider-sdk/` and re-sync.

### 5.4 Tenant isolation in providers

Providers that talk to kcp build a **per-(tenant, caller) dynamic client**: the
hub forwards the caller's bearer token plus resolved `X-Faros-Tenant` path; the
provider's `tenant/` package (`client.go`, `credentials.go`) constructs a client
scoped to `<host>/clusters/<tenantPath>`, acting as the caller in their
workspace. See `providers/code/tenant/` and `providers/infrastructure/tenant/`
for the canonical pattern, and `docs/provider-scoping.md`.

### 5.5 Provider inventory

All providers are **standalone**: own `go.mod` under `providers/{name}/`, own
image/pod, registered at runtime via their `CatalogEntry`. There are no
built-in providers any more (the former `mcp`/`kubernetesedges`/`serveredges`
built-ins were folded into the `edges` provider in #435; the MCP aggregate now
lives hub-side in `pkg/hub/mcpaggregate/`; `projects` was folded into
`app-studio`).

| Provider | APIExport | What it does |
|----------|-----------|--------------|
| `quickstart` | `quickstart.providers.faros.sh` | **Reference provider** — minimal HTTP server + embedded Vite portal + sample `Greeting` API. Start here. |
| `edges` | `edges.providers.faros.sh` | The connectivity core: `KubernetesCluster`/`LinuxServer` edges, revdial tunnel termination, kubectl/SSH/MCP proxying, `Service` connectors (host/LAN apps → MCP tools), `Workload`/`Placement` scheduling + Helm marketplace. Single-replica (process-global dialer map). |
| `infrastructure` | `infrastructure.providers.faros.sh` | Application Templates via kro: template catalog, instance provisioning, data plane (exec/logs/etc.), app hosting + access gate |
| `code` | `code.providers.faros.sh` | Git hosting management (repos, deploy keys, collaborators, packages) behind a `GitBackend` seam; GitHub is the only real backend today |
| `databricks` | `databricks.providers.faros.sh` | Databricks SQL warehouse tables via governed `query_table` action + MCP tools; narrowest claims posture in the repo (the model citizen) |
| `agents` | `agents.faros.sh` | Long-running personal AI agents: chat, schedules, triggers, approvals, budgets, memory, multi-channel (Slack/Telegram/Discord/SMTP). Needs hub + Postgres only |
| `app-studio` | `ai.faros.sh` | Persistent AI project workspace (projects, sessions, dev sandboxes, publishing, skills) |
| `kuery` | `kuery.providers.faros.sh` | Fleet-wide object query, relationship traversal, impact analysis across connected edges + MCP tools |

Per-provider deep docs: `docs/code-provider-architecture.md`,
`docs/infrastructure-architecture.md`, `docs/kuery-provider-architecture.md`,
`docs/agents-provider-architecture.md`,
`docs/application-template-architecture.md`, `docs/edges-marketplace.md`,
`docs/mcp-architecture.md`, `docs/providers.md`, `docs/provider-publishing.md`,
`docs/provider-scoping.md`, `docs/byo-providers.md`.

The table above is the **platform** catalog, at `root:faros:providers:<name>`.
An organization can also register its own provider — one it runs itself, usually
in its own cluster — at `root:faros:tenants:<orgUUID>:providers:<name>`. Those
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
3. Write `manifest.yaml` (CatalogEntry): displayName, ui/backend URLs, health
   path, apiExport name + permission claims + schema bodies.
4. Build the portal (`providers/{name}/portal/`, embedded via `assets.go`).
5. Implement heartbeat + tenant-scoped client if it talks to kcp.
6. Add Makefile `build-{name}-provider[-portal]` + `run/install/uninstall`
   targets if standalone; add the module to `go.work`.
7. Add an e2e suite under `test/e2e/suites/` if it has tenant-isolation or
   provisioning behavior worth guarding.
8. Write **two** READMEs: `providers/{name}/README.md` (what the provider is and
   its APIs) and `providers/{name}/deploy/chart/README.md` (values reference).
   The chart README is user-facing: charts embed it into their CatalogEntry
   (`valuesDoc: |{{ .Files.Get "README.md" | nindent 10 }}`) and the portal
   renders it inline in the Self-Hosting flow. It is the answer to "what can I
   configure?" and must not go stale.
9. To offer the provider for self-hosting, declare `spec.selfHosting` in **both**
   `manifest.yaml` and `deploy/chart/templates/catalogentry.yaml` (chart
   coordinates, namespace, release name, `docsURL` → the chart README, and any
   `requiredValues`). Prefer placeholders — `{{hubURL}}`, `{{workspacePath}}`,
   `{{kubeconfigSecret}}` — over values the installer must look up. See
   `docs/byo-providers.md`.

---

## 6. Hub architecture (`pkg/hub/`, `cmd/faros-hub/`)

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
- `restapi/`, `graphql.go`, `serviceaccounts/`, `quota/`, `portal*.go` — REST
  API surface, GraphQL hook, SA management, quotas, portal serving.
- `pkg/virtual/builder/` — kcp virtual-workspace handlers: the agent-proxy
  (tunnel auth, status, SSH creds) and the multi-cluster MCP server.

The **agent** lives in `pkg/agent/` (tunnel, ssh, reporters) and ships inside the
`faros` CLI binary (`faros agent run`). The join-token → kubeconfig exchange and
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
`provider-sdk/portalkit-vue/`), plus existing host components in
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
  surrounding style; imports group faros last (goimports local-prefix).
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
  `${faros.*}` env-substitution tokens. Fixed sidecar images (e.g. the
  control-token `kubectl` job) are hardcoded literals. `${faros.*}` tokens are
  reserved for the handful of genuinely platform-global values with no universal
  default: the exposure Gateway parent (`${faros.gatewayName}` /
  `${faros.gatewayNamespace}`), the dev-overlay images
  (`${faros.devImage.<toolchain>}` / `${faros.devAgentImage}`), and the
  exposure-URL port suffix (`${faros.appPublicPort}`). A missing env must never
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

---

## 10. Cross-repo boundaries & known gotchas

faros runs on kcp; some symptoms that look like faros bugs are actually upstream:

- **GraphQL / OpenAPI proxy misbehaving** — faros serves OpenAPI/GraphQL through a
  kcp virtual workspace. Broken VW OpenAPI serving surfaces as hub-side proxy
  issues; the fix is usually kcp-side, not faros. Check the kcp VW openapi path
  before assuming the bug is in the faros gateway.
- **`kubectl get <resource>` "temporarily unavailable" for one resource in an
  APIBinding (e.g. templates), intermittently** — APIExport *virtual storage*
  (CachedResource) discovery fails when the consumer workspace is on a different
  kcp shard than the provider. It's a kcp cross-shard discovery bug, not faros
  config — don't chase the faros install code. Workaround for local dev: run a
  single kcp shard, or co-locate provider + consumer on one shard.

---

## 11. Where to look next

- `DEVELOPERS.md` — Edge CRD spec, join-token flow, proxy URL format, SSH
  internals, kcp workspace hierarchy, MCP integration, hub controllers.
- `docs/providers.md` + per-provider arch docs — provider plane deep dives.
- `docs/security.md`, `docs/organizations.md`, `docs/provider-scoping.md` —
  tenancy + isolation model.
- `docs/graphql.md` — GraphQL gateway.
- `CONTRIBUTING.md` — contribution workflow.
