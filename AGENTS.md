# AGENTS.md

Orientation for AI agents (and humans) working in the **kedge** repo. Read this
before making changes. It explains the architecture, where hub code ends and
provider code begins, how APIs are constructed, and the exact commands to build,
test, format, lint, and regenerate code.

> Module: `github.com/faroshq/faros-kedge` · Go workspace (`go.work`) · kcp-based
> multi-tenant control plane.

Deeper references live in [`DEVELOPERS.md`](./DEVELOPERS.md) and [`docs/`](./docs)
(per-provider architecture docs, security, organizations, graphql, mcp). Keep
those authoritative — this file is the map, not the territory.

---

## 1. What kedge is

kedge connects distributed Kubernetes clusters and bare-metal servers through one
control plane (the **hub**). Edge **agents** dial outbound reverse tunnels to the
hub, so clusters behind NAT/firewalls become reachable through a single
authenticated endpoint. On top of that core, kedge is also a **multi-tenant
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
apis/                 First-party API types (kedge, tenancy, providers groups)
  kedge/v1alpha1/       Edge, MCPServer, Placement, VirtualWorkload
  tenancy/v1alpha1/     Organization, User, Membership, UserMembershipIndex, Auth
  providers/v1alpha1/   CatalogEntry (the provider manifest type)
cmd/                  Binaries
  kedge/                CLI (also the agent: `kedge agent run`)
  kedge-hub/            Hub control-plane server
  graphql/              GraphQL gateway (listener + gateway subcommands)
  kedge-release/        Release-tagging helper
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

`go.work` members: `.`, `providers/quickstart`, `providers/infrastructure`,
`providers/code`, `providers/kuery`, `providers/app-studio`, and the external
`kubernetes-graphql-gateway`. Standalone providers each have their own `go.mod`;
built-in providers do not (they compile into the hub binary).

---

## 3. Build / format / lint / codegen — the commands

Everything goes through the **Makefile**. Tools (controller-gen, apigen,
golangci-lint, kcp, dex) are version-pinned and installed into `hack/tools/` on
demand — never `go install` them globally.

| Task | Command | Notes |
|------|---------|-------|
| Build all binaries | `make build` | kedge CLI + hub + graphql |
| Build hub | `make build-hub` | also builds built-in provider portals |
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
  `github.com/faroshq/faros-kedge` (kedge imports group last).
- Generated files (`zz_generated*`, `vendor/`) are excluded.
- Before committing Go changes, run **`make fix-lint`** then **`make lint`**.

**Standalone providers** (their own `go.mod`) are NOT covered by the root
`make lint`/`make test`. Lint/test them from their own directory, e.g.
`cd providers/kuery && go build ./... && go test ./...`.

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

API groups: `kedge.faros.sh`, `tenancy.kedge.faros.sh`,
`providers.kedge.faros.sh`. Provider APIs use `<name>.providers.kedge.faros.sh`.

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
(`providers.kedge.faros.sh/v1alpha1`, type at
`apis/providers/v1alpha1/types_catalogentry.go`). It declares display metadata,
the UI/backend/virtual-workspace URLs, a health path, the APIExport name +
permission claims, and inline `APIResourceSchema` bodies. The hub's catalog
controller reads it and provisions the kcp side (sub-workspace, ServiceAccount,
APIExport, schemas) and registers routing/heartbeat state.

### 5.2 Hub-side provider integration (`pkg/hub/providers/`)

| File | Role |
|------|------|
| `provision.go` | Creates kcp sub-workspace, ServiceAccount, APIExport, applies inline schemas; mints the provider kubeconfig |
| `proxy.go` | UI reverse-proxy (`/ui/providers/{name}/*`) + backend proxy (`/services/providers/{name}/*`); injects tenant/user headers; serves embedded UI for built-ins (`LocalUIAssets`) |
| registry / controller / heartbeat | In-memory routing table, catalog reconcile, `POST /api/providers/{name}/heartbeat` liveness (TTL ~90s) |
| `pkg/hub/provider_tenant_resolver.go` | Resolves caller identity → tenant workspace path; injects `X-Kedge-User` / `X-Kedge-Tenant`, strips spoofed inbound copies |

Heartbeat: standalone providers POST every ~30s with `KEDGE_HUB_URL`,
`KEDGE_HUB_TOKEN`, `KEDGE_PROVIDER_NAME`. A provider is "Ready" only when its
endpoints are valid and (once heartbeats have started) not stale.

### 5.3 Provider portal micro-frontends

Provider UIs are independent Vite/TS bundles in `providers/{name}/portal/`,
built to `dist/` and embedded via `//go:embed` in the provider's `assets.go`. The
portal renders them as **custom elements** (`<kedge-provider-{name}>`) that
receive a `kedge-context` (user, tenant, theme, basePath) via the
`postMessage` `kedge.ready` → `kedge.context` handshake.

Build chain (Makefile):
- `make build-{name}-provider-portal` — `vite build` only.
- `make build-{name}-provider` — portal + Go binary (standalone providers).
- `make portal-provider-symlinks` — symlinks each built-in provider portal's
  `node_modules` to the main `portal/node_modules` so shared deps (vue, urql,
  pinia, tailwind…) resolve. Symlinks are gitignored/idempotent.

The Tilt dev loop proxies all UI to the Vite dev server (`--portal-dev-url`) and
skips the slow provider-portal builds.

### 5.4 Tenant isolation in providers

Providers that talk to kcp build a **per-(tenant, caller) dynamic client**: the
hub forwards the caller's bearer token plus resolved `X-Kedge-Tenant` path; the
provider's `tenant/` package (`client.go`, `credentials.go`) constructs a client
scoped to `<host>/clusters/<tenantPath>`, acting as the caller in their
workspace. See `providers/code/tenant/` and `providers/infrastructure/tenant/`
for the canonical pattern, and `docs/provider-scoping.md`.

### 5.5 Provider inventory

| Provider | Module | Built-in? | What it does |
|----------|--------|-----------|--------------|
| `quickstart` | own `go.mod` | standalone | **Reference provider** — minimal HTTP server + embedded Vite portal + sample `Greeting` API. Start here. |
| `code` | own `go.mod` | standalone | Source-code/repository management; controllers + tenant isolation + MCP |
| `infrastructure` | own `go.mod` | standalone | kro-based infrastructure templates; self-bootstrap example; tenant isolation |
| `kuery` | own `go.mod` | standalone | Query API + engagement; MCP server |
| `app-studio` | own `go.mod` | standalone | Application templates / project store (recently reorganized from `providers/projects`) |
| `mcp` | no `go.mod` | built-in | Aggregated multi-cluster MCP endpoints |
| `kubernetesedges` | no `go.mod` | built-in | Kubernetes-type edges UI/registration |
| `serveredges` | no `go.mod` | built-in | Server/SSH-type edges (depends on kubernetesedges) |
| `projects` | portal only | built-in route | Portal SPA route (being folded into app-studio) |

Built-ins are registered via their `manifest.go` and compiled into the hub
binary; standalone providers run as separate images/pods and register at runtime
via their `CatalogEntry`. Per-provider deep docs:
`docs/code-provider-architecture.md`, `docs/infrastructure-architecture.md`,
`docs/kuery-provider-architecture.md`, `docs/application-template-architecture.md`,
`docs/providers.md`, `docs/provider-publishing.md`, `docs/provider-scoping.md`.

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

---

## 6. Hub architecture (`pkg/hub/`, `cmd/kedge-hub/`)

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
`kedge` CLI binary (`kedge agent run`). The join-token → kubeconfig exchange and
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
| `make e2e-all` | all | Builds hub+agent images, runs everything (~30m) |

E2E knobs: `E2E_FLAGS` (e.g. `--keep-clusters` via `make e2e-keep`),
`E2E_TIMEOUT`. `standalone`/`ssh`/`oidc`/`external_kcp` build Docker images first
and load them into kind; `provider*`/`infrastructure` run binaries directly on
local ports. Framework helpers live in `test/e2e/framework/`.

### Local dev loop (Tilt)
```bash
tilt up      # portal (Vite :3000) + hub (HTTPS :9443, embedded kcp, static auth)
tilt down
curl -k https://localhost:9443/healthz
```
`Tiltfile.cluster` / `make tilt-cluster` brings up the operator-deployed
multi-shard stack used by the `tiltcluster` e2e suite.

---

## 8. UI & design standards (portal + provider micro-frontends)

**All UI must be standardized.** The main portal and every provider
micro-frontend render inside the same DOM (`<kedge-provider-{name}>` custom
elements), so they share one stylesheet and must look like one product. Do **not**
introduce per-provider fonts, ad-hoc colors, or bespoke modal/table markup. Reuse
the shared design tokens and the existing components in
`portal/src/components/`.

**Stack:** Vue 3 + TypeScript + Vite, **Tailwind CSS v4** (`@import "tailwindcss"`),
[`lucide-vue-next`](https://lucide.dev) icons. Tokens and global styles live in
`portal/src/assets/main.css` under `@theme`. Provider `.vue/.ts` files are pulled
into Tailwind's scan via `@source` directives there — a new provider portal must
be added to that list or its classes won't compile.

### Design tokens — use these, never raw hex/grays

Both dark (default) and light themes are defined as CSS variables; reference them
through the Tailwind color names, never hardcode colors.

| Token (class) | Purpose |
|---------------|---------|
| `surface`, `surface-raised`, `surface-overlay`, `surface-hover` | Background layers (page → card → popover → hover) |
| `border-subtle`, `border-default` | Hairline borders |
| `accent`, `accent-hover`, `accent-subtle`, `accent-glow` | Brand purple (`#7c5bf5`); primary actions, focus, links |
| `text-primary`, `text-secondary`, `text-muted` | Text hierarchy |
| `success`, `warning`, `danger` (+ `-subtle` variants) | Status / semantic |

Theme switches via `html.dark` / `html.light` (applied pre-paint in
`index.html`); never assume a fixed background. Glass surfaces use the `.glass`
utility (`backdrop-blur` + translucent bg), backgrounds use `.dot-grid` /
`.cross-grid` / `.noise`.

### Typography

- **Font:** Tailwind's default system sans stack (no custom web font is loaded —
  don't add one). Body uses `antialiased`.
- **Mono:** use `font-mono` for identifiers, names, tokens, URLs, YAML, and any
  technical/copyable value (it's used heavily — keep doing it).
- **Type scale (px, explicit):** `text-[10px]` / `text-[11px]` for labels and
  table headers (uppercase, `tracking-wide`), `text-[12px]`–`text-[13px]` for
  body/table cells, `text-[14px]`–`text-[18px]` for headings. Weights:
  `font-medium` (labels/buttons), `font-semibold` (headings/badges),
  `font-bold` sparingly.
- **Radius scale:** `rounded-lg` (buttons, inputs, small controls),
  `rounded-xl` (icon tiles, nested boxes), `rounded-2xl` (cards, modals, tables),
  `rounded-full` (pills/badges/dots).

### Component standards — reuse, don't reinvent

| Need | Use | Pattern |
|------|-----|---------|
| **Modal / popup** | shape of `components/ConfirmDialog.vue` | `Teleport to="body"`; full-screen `fixed inset-0 z-[100] flex items-center justify-center bg-black/50 backdrop-blur-sm`; panel `w-full max-w-md rounded-2xl border border-border-subtle bg-surface-raised p-6 shadow-2xl`; close on backdrop `@click.self` and **Esc** (`useEscapeKey` composable); icon tile + title (`text-[14px] font-semibold`) + message (`text-[12px] text-text-secondary`); actions bottom-right (`Cancel` ghost + primary). |
| **Confirm / destructive action** | `components/ConfirmDialog.vue` | Don't hand-roll; pass `title`/`message`/`confirmLabel`/`busy`. Destructive = `bg-danger`. |
| **Table / list** | `components/ResourceTable.vue` | `overflow-hidden rounded-2xl border border-border-subtle bg-surface-raised/80 backdrop-blur`; uppercase `text-[10px] tracking-[0.15em] text-text-muted` headers; `text-[13px]` cells; row hover `hover:bg-accent/[0.03]`, `group-hover:text-text-primary`; built-in loading shimmer, error, and empty (`Inbox` icon + "No data") states. Use named slots per column key for custom cells. |
| **Status / phase** | `components/StatusBadge.vue` | Pill with semantic color + dot; `ready` shows a live pulsing dot. Maps `ready/active`→success, `pending/scheduling`→warning, `terminating`/disconnected→danger. |
| Wizard / multi-step | `FirstEdgeWizard.vue`, `FirstWorkspaceWizard.vue` | |
| YAML display | `YamlViewer.vue` | |
| Tenant context | `TenantSwitcher.vue`, `TenantContextChip.vue` | |
| Theme toggle | `ThemeSwitch.vue` | |

Icons: **lucide-vue-next** only, typically `h-4 w-4` with `:stroke-width="1.75"`
(or `2` for small/close icons). Don't mix icon sets.

When a provider needs something not in `portal/src/components/`, prefer adding it
there (or matching its token/scale conventions exactly) over inventing a
provider-local variant — consistency across the embedded micro-frontends is the
whole point.

---

## 9. Conventions & guardrails

- **Always go through the Makefile** for build/test/lint/codegen — it pins tool
  versions into `hack/tools/`.
- After editing any `apis/` Go type, run `make codegen` and commit the generated
  diff; CI enforces `make verify-codegen`.
- Run `make fix-lint && make lint` before committing Go changes. Match
  surrounding style; imports group kedge last (goimports local-prefix).
- Don't hand-edit `zz_generated*` or `config/crds` / `config/kcp` outputs.
- License boilerplate is required on Go files (generated files exempt);
  `make boilerplate` adds it.
- The hub binary embeds CRDs (`pkg/hub/bootstrap/crds`) and built-in provider
  portals — rebuild the hub after changing either so the embedded FS stays in
  sync.
- Standalone providers are separate modules: changes there need their own
  build/test and `go.work` awareness; they are not in the root `./...`.
- Before merging: `make verify` is the full gate
  (boilerplate + codegen + vet + lint + build + test).
- **Infrastructure templates declare configurable inputs (container images,
  versions, sizes) as `spec.schema` fields with sane defaults** — never via
  `${kedge.*}` env-substitution tokens. Fixed sidecar images (e.g. the
  control-token `kubectl` job) are hardcoded literals. `${kedge.*}` tokens are
  reserved for the handful of genuinely platform-global values with no universal
  default: the exposure Gateway parent (`${kedge.gatewayName}` /
  `${kedge.gatewayNamespace}`), the dev-overlay images
  (`${kedge.devImage.<toolchain>}` / `${kedge.devAgentImage}`), and the
  exposure-URL port suffix (`${kedge.appPublicPort}`). A missing env must never
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

kedge runs on kcp; some symptoms that look like kedge bugs are actually upstream:

- **GraphQL / OpenAPI proxy misbehaving** — kedge serves OpenAPI/GraphQL through a
  kcp virtual workspace. Broken VW OpenAPI serving surfaces as hub-side proxy
  issues; the fix is usually kcp-side, not kedge. Check the kcp VW openapi path
  before assuming the bug is in the kedge gateway.
- **`kubectl get <resource>` "temporarily unavailable" for one resource in an
  APIBinding (e.g. templates), intermittently** — APIExport *virtual storage*
  (CachedResource) discovery fails when the consumer workspace is on a different
  kcp shard than the provider. It's a kcp cross-shard discovery bug, not kedge
  config — don't chase the kedge install code. Workaround for local dev: run a
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
