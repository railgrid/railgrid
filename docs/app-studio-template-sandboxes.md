# Every Template can provide a development environment

Status: **Implemented through Phases 0–4 (2026-07-04); remaining work is
portal import/catalog polish and BYO-compute validation.** The design and
future sections below remain a proposal for deferred capabilities; they do not
override the current runtime contract in
[`app-studio-sandbox-runtime.md`](./app-studio-sandbox-runtime.md).
Author: 2026-07-03
Related: [`app-studio-runtime-decoupling.md`](./app-studio-runtime-decoupling.md) (historical data-plane proposal), [`app-studio-sandbox-runtime.md`](./app-studio-sandbox-runtime.md) (current Template/data-plane boundary), [`application-template-architecture.md`](./application-template-architecture.md), [`code-provider-architecture.md`](./code-provider-architecture.md), `providers/infrastructure/install/templates/{simple-webapp,application}.yaml`.

## Summary

The original proposal started from one fixed `sandbox-runner` shape: a single
container and one control endpoint. That fixture and its signed preview gateway
were removed in Phase 4. The current implementation is Template-backed:
Projects select an infrastructure Template, the Template declares development
components and data-plane verbs, and App Studio provisions the selected
development instance with `farosMode: development`.

Development mode is therefore a capability any Template can declare. Selected
components run platform-managed dev images with a hot-reload agent, each with
its own file-sync target and reload behavior; the infrastructure provider owns
that contract. App Studio owns template selection, the workspace, git
persistence, and the assistant. Because code is persisted in the project's git
repository, the Template is swappable: re-provision under a different Template,
re-hydrate, and re-sync. The design sections below retain the future scaffold,
idle-runtime, and promotion proposals around that implemented core.

## Design principles (from the product requirement)

1. **Every template can act as a sandbox.** Dev mode hot-swaps declared
   component images (web, backend, worker) with platform-managed dev images for
   hot reload; the rest of the graph (DB, cache, routes) runs as declared.
2. **App Studio picks the template with the user.** Project creation determines
   requirements first (assistant interview + portal wizard), then selects the
   Template whose shape matches; that Template *is* the sandbox.
3. **Git is the source of truth for code.** The sandbox is disposable; the
   Template choice is changeable; repos are importable. Workspace and PVC are
   caches of the repo, never the durable copy.
4. **The infrastructure provider owns runtime mechanics.** Images, templates,
   reload steps, and the data-plane API — which must address multiple
   containers/tiers, not one runner.
5. **App Studio owns AI logic.** Prompts, tool registry, profiles, plan
   approval stay in App Studio; they consume the Template's declared contract
   (agent guidance, dev components) instead of baked-in sandbox knowledge.

## Current state — what exists, what is missing

| Principle | Implemented | Remaining |
|---|---|---|
| 1. Any Template as a development environment | Template API, per-template CRD/schema, development contract, synthesized dev overlay, platform dev-agent, and `application`/`simple-webapp` development templates are implemented and e2e-tested. | Future Template coverage and additional toolchains remain incremental work. |
| 2. Requirements → Template selection | `Project.spec.template`, live catalog selection, assistant `select_project_template`, development binding generation, and template-aware assistant context are implemented. | Portal creation-flow polish and a richer requirements interview remain product work. |
| 3. Git as source of truth | Code-provider checkout plus App Studio `hydrate-workspace` make repositories recoverable; template switching re-hydrates and re-syncs. | Portal import/catalog UX remains. |
| 4. Infra owns images/reload; multi-tier data plane | `Template.spec.dataPlane`, generic resolver/handler, component-scoped URLs, dev-agent reload rules, and caller-authenticated routing are implemented. | BYO-compute validation across a second infrastructure provider remains. |
| 5. App Studio owns AI | Prompts, local tools, profiles, Eino runtime, template context, and component-aware sync all live in their owning modules. | Future scaffolds and additional assistant guidance can build on the contract. |

## 1. The Template development contract (infra-owned)

Add `Template.spec.development` next to `dataPlane`. It declares which graph
components can be hot-swapped in dev mode and how each reloads:

```yaml
# Template.spec.development — application.yaml (illustrative)
development:
  components:
    frontend:
      workspacePath: web            # repo/workspace subdir synced here
      devImage: ${faros.devImage.node}
      workingDir: /workspace
      startCommand: "npm run dev"
      port: frontend                # named port kept from the prod graph
      reload:
        strategy: process           # process | container
        rules:
          - paths: ["package.json", "package-lock.json"]
            command: "npm install"  # run before process restart
    backend:
      workspacePath: api
      devImage: ${faros.devImage.python}
      startCommand: "uvicorn main:app --reload"
      port: backend
      reload:
        strategy: process
        rules:
          - paths: ["requirements.txt", "pyproject.toml"]
            command: "pip install -r requirements.txt"
  scaffold:                          # starter code for a fresh project,
    repository: https://github.com/faroshq/scaffold-web-api
    ref: v1                          # incl. .github/workflows building each
                                     # component's image (see §4.1a)
```

Rules:

- A **component** names a workload the Template's graph already emits (the
  frontend Deployment, the backend Deployment). Components *not* listed
  (database StatefulSet, oauth proxy) run exactly as in production mode — this
  is the point: a dev sandbox with a real Postgres next to it.
- `devImage` values are **platform-managed** `${faros.devImage.<toolchain>}`
  tokens resolved by the infrastructure provider from env/config. Tenants
  never pick dev images.
- `workspacePath` maps a workspace/repo subdirectory to the component. This is
  the contract App Studio uses to route file sync (§4) and is also the layout
  the scaffold and the assistant follow.
- `reload` is the declared reload procedure: the dev agent (§2) restarts the
  process by default; `rules` name path patterns that require a command first.
  `strategy: container` is the escape hatch for toolchains that cannot hot
  reload in place.

**Instance dev mode.** Instances gain a platform-reserved spec field
`farosMode: production | development` (injected into every template's CRD
schema by the Template controller, the same way `credentialsSecretName` is
controller-owned today). `development` is only valid when the Template declares
a `development` block.

**Rendering.** When `farosMode: development`, each declared component's
workload is rendered with a **platform coordinator**, an **app runtime
supervisor**, and a **stateless executor**. It also receives a
**per-component workspace PVC** mounted at `workingDir`, a separate
**per-component platform-state PVC** mounted at `/faros/state` for the
coordinator, a per-component control Service, and the instance-wide control
token Secret. Everything else in the graph is untouched. How the overlay is
produced is an infra-internal decision (§6.1) — the Template author writes
the `development` block, not two graphs. Rules the overlay must obey:

- **Workspace PVCs are dev-mode-only and per-component.** Each dev component
  gets its own RWO PVC holding only its `workspacePath` subtree — components
  are separate Deployments that may schedule to different nodes, so a shared
  RWO volume is not an option, and sync is routed per component anyway (§4.2).
  The production graph carries **no** code PVCs (data-service volumes like the
  Postgres PVC are the Template's own business and untouched). Development
  code PVCs are synthesized by the overlay rather than embedded in each
  production graph.
- **Container authorities are split.** The coordinator owns the public,
  token-authenticated control contract and durable session/idempotency state on
  `/faros/state`, but has no app environment or secrets. The runtime supervisor
  keeps the production app environment/secrets and starts, restarts, and
  reloads the app, but has no platform token or platform-state mount. The
  stateless executor verifies the synchronized workspace revision/SHA-256 digest
  and runs typed argv, but has no platform token, platform-state mount, app
  environment/secrets, or service-account credentials.
- **The public and internal ports are distinct.** The component Service keeps
  the token-authenticated control contract on `7070` (control) and `7071` (exec).
  Coordinator-to-runtime control binds to pod loopback `7072`, and
  coordinator-to-executor control binds to pod loopback `7073`.
- **The pod is non-root by construction.** The coordinator, runtime supervisor,
  and executor run as UID/GID `1000`, with `allowPrivilegeEscalation: false` and
  all capabilities dropped. The pod uses seccomp `RuntimeDefault`,
  `fsGroup: 1000`, and `shareProcessNamespace: false`.
- **Everything else in the app container is preserved.** The runtime supervisor
  keeps the production container's env (for example `DATABASE_URL` from the
  credentials Secret), ports, and volume wiring — a dev backend must reach the
  instance's real Postgres exactly as the prod one would. The coordinator uses
  HTTP health checks, while the runtime supervisor and stateless executor use
  internal TCP readiness checks.
- **Routing is identical in both modes.** The dev overlay keeps the
  production HTTPRoute rules verbatim (`/` → frontend, `/api` → backend): the
  gateway does the tier split, so dev servers never need per-toolchain proxy
  configuration (no Vite-shim revival).

## 2. The dev agent (infra-owned)

`providers/infrastructure/dev-agent/main.go` is the standalone
**`faros-dev-agent`** static binary. In dev mode every declared component runs
the binary in three roles:

- Injected via an **init container** that copies the agent binary from a
  platform image into a shared `emptyDir` — so `devImage` can be *any*
  toolchain image (node, python, go, jdk) with no faros-specific baking.
- The coordinator serves `/sync`, `/restart`, `/logs`, `/env`, and
  revision-bound `/exec` on the public Service's `7070`/`7071` ports. It holds
  the `X-Sandbox-Control-Token` and durable platform state.
- The runtime supervisor starts and supervises the component app process on
  internal loopback `7072`, retaining the app's environment and secrets.
- The stateless executor runs typed argv after revision/SHA-256 verification on
  internal loopback `7073`; it has no platform token, platform-state mount, app
  environment/secrets, or service-account credentials. See
  `docs/app-studio-sandboxed-exec.md`.
- Executes the Template-declared reload rules (§1) on sync: match changed paths
  → run rule command → restart the process. With `strategy: container`, the
  runtime supervisor container is restarted; the coordinator is not restarted.
- Auth stays the per-instance control token (`X-Sandbox-Control-Token`),
  presented at the public control boundary.

The platform dev images (`${faros.devImage.*}`) are plain toolchain images the
infrastructure provider curates and pins. They keep the dev agent separate from
the application toolchain instead of coupling every project to one runner
image.

## 3. Multi-component data plane (infra-owned)

`Template.spec.dataPlane` grows a `components` dimension; the flat `endpoints`
map stays for instance-level verbs (`status`, a single `proxy`):

```yaml
dataPlane:
  runtimeNamespacePath: status.runtimeNamespace
  tokenSecretPath: status.controlSecretRef
  endpoints:
    status: { fromStatus: true }
    proxy:                                    # instance-level preview (routed tier)
      servicePath: status.previewServiceRef
      port: preview
      methods: [GET, POST, HEAD]
      upgrade: true
  components:                                  # per-component verbs
    frontend:
      servicePathPrefix: status.components.frontend   # {controlServiceRef, ...}
      endpoints:
        sync:    { port: control, upstreamPath: /sync,    methods: [POST] }
        restart: { port: control, upstreamPath: /restart, methods: [POST] }
        log:     { port: control, upstreamPath: /logs,    methods: [GET], stream: true }
        exec:    { port: exec,    upstreamPath: /exec,    methods: [POST] }
    backend:
      ...
```

URL shape uses the generic data-plane handler:

```
POST …/dataplane/clusters/{ws}/applications/{name}/components/backend/sync
GET  …/dataplane/clusters/{ws}/applications/{name}/components/frontend/log
POST …/dataplane/clusters/{ws}/applications/{name}/restart      (all components)
```

The generic resolver, namespace confinement, token injection, and
authorize-as-caller flow are implemented. Instance-level verbs may fan out to
all components (`restart` restarts every dev process).

The backend publishes per-component status refs
(`status.components.<name>.{controlServiceRef,ready}`) alongside the existing
instance-level ones.

## 4. App Studio changes (product-owned)

### 4.1 Project creation: requirements → template

- `Project.spec` records the choice: a `template` reference (`{resource, kind,
  version}`) replacing the old fixed-runner assumption. The development
  environment's binding is generated from it with `farosMode: development`.
- **Projects are created template-less; the first assistant conversation
  binds one.** Creation makes only the Project + repo + empty workspace — no
  sandbox yet. The assistant's first conversation runs the requirements
  interview using its existing `infrastructure__list_templates` /
  `describe_template` tools plus `ask_follow_up` ("does this need a backend?
  persistent data? background jobs?"), recommends a Template, and on
  confirmation writes `spec.template` — which provisions the dev instance.
  This same shape serves repo import: the assistant inspects the imported
  code first, then recommends. The portal shows the catalog (filtered to
  templates with a `development` block) as a shortcut that skips the
  interview and sets `spec.template` directly.
- Once the template is bound, App Studio seeds the workspace from the
  Template's `scaffold` (via the Code provider, §5) so the directory layout
  matches the declared `workspacePath`s, then commits the scaffold as the
  repo's initial state.

### 4.1a Production images come from the project's own CI

The platform does not build images. The Template's scaffold ships
**GitHub Actions workflows** that build and push each component's image from
its `workspacePath` (so scaffold layout, `development.components`, and CI
stay coupled on the Template — §6.3). The assistant's bootstrap context
prompt instructs it to create and maintain these workflows as the project
evolves (new component ⇒ new build job). Flipping an instance to
`farosMode: production` supplies the CI-built image refs to the template's
image fields, which become **optional while `farosMode: development`**
(ignored by the dev overlay). Promotion mechanics — where image tags land,
how the flip is triggered — are a follow-up design; this document only
guarantees every project has buildable images from day one.

### 4.2 Development loop: component-aware

- **Sync** routes by path: `developmentSync` maps each changed workspace file
  through the Template's `development.components[].workspacePath` prefixes and
  POSTs per-component `…/components/{c}/sync` batches (paths relative to the
  component root). Files outside any component (README, docs) sync nowhere.
- **Logs/restart/preview** become component-scoped in API and portal: the
  workbench shows one log stream and restart control per component, and the
  preview targets the instance-level `proxy` verb (the routed tier), unchanged.
- **Assistant** context gains the chosen Template's `agent.usage`, its
  component map, and per-component status — so prompts stop hardcoding
  "the sandbox" and the model knows `api/` edits reload the backend. Tool
  registry ownership is unchanged (principle 5); only tool *implementations*
  route per component.

### 4.3 Template switching

Changing `Project.spec.template`:

1. Commit any dirty workspace state to the repo (or refuse until committed).
2. Delete the old instance (kro GC tears the graph down; PVC included — it is
   a cache).
3. Create the new Template's instance with `farosMode: development`.
4. Re-hydrate the workspace from the repo (§5) and full-sync every component.

Code survives because the repo is the source of truth; what does *not*
automatically survive is layout mismatch (the old template's `web/` vs the new
one's `frontend/`). v1 keeps this honest-but-manual: the assistant proposes the
directory moves as a normal plan/patch flow before the switch completes.

## 5. Git as source of truth (Code provider)

The missing half of the code lifecycle is **repo → workspace**:

- Code provider gains a checkout/archive capability: `RepositoryCheckout` CR +
  `code__checkout_repository` MCP tool producing a bundle (the CommitBundle
  store in reverse) that App Studio unpacks into the workspace.
- App Studio uses it in three places: **import an existing repo** into a new
  project (the repo's layout drives template recommendation — the assistant
  inspects it and suggests the matching Template), **re-hydrate** on template
  switch, and **recover** a workspace whose FS copy is lost (making
  `APP_STUDIO_WORKSPACE_ROOT` finally disposable, which it is implicitly
  assumed to be but currently is not).
- Commit flow is unchanged; auto-commit cadence (e.g. commit on every applied
  plan) is a product decision deferred until import works.

## 6. Decisions (resolved 2026-07-03)

### 6.1 How the dev overlay is rendered

| Option | Pros | Cons |
|---|---|---|
| **kro conditionals in one graph** (`includeWhen` + CEL ternaries on image/command keyed on `schema.spec.farosMode`) | One RGD, one instance kind | CEL string-templating of images/commands is exactly where kro is brittle (quoting/multiline gotchas already bitten in this repo); every template author pays the complexity |
| **Backend-synthesized overlay** — the kro backend builds the dev variant of the graph mechanically from `development` at RGD build time (same place `${faros.*}` substitution runs), producing mode-conditional resources itself | Template authors write only the `development` block; transformation is tested Go, not CEL; matches the existing "backend interprets backendConfig" boundary | The backend must understand enough of the graph to find a component's workload (needs a component→resource naming convention) |

**Decision: backend-synthesized.** The convention is small — a
component names a resource in the graph (`frontend` → the resource whose `id`
is `frontend`/`frontendDeployment`) — and it keeps CEL out of template authors'
hands, consistent with the token-substitution precedent.

### 6.2 Dev session as mode vs. separate resource

**Decision: the `farosMode` field on the instance** (§1) — dev and prod are
the same CR; toggling mode re-renders workloads in place. The alternative (a
paired `DevSession` CR referencing the instance) isolates dev churn but
doubles the API surface and breaks "the template is the sandbox" directness.
Revisit only if mode-flapping proves disruptive.

### 6.3 Scaffold ownership

**Decision: `development.scaffold` on the Template** (infra-declared): the
scaffold layout is coupled to the `workspacePath`s *and* to the CI workflows
that build each component (§4.1a), so drift between an App Studio-owned
scaffold catalog and the template contract would silently break sync routing
and builds. App Studio may still overlay product-specific files (README,
assistant hints) after hydration.

## 7. Phasing

| Phase | Deliverable | Risk |
|---|---|---|
| **0** | **DONE.** API: `Template.spec.development`, `dataPlane.components`, reserved `farosMode` schema injection; resolver + validation + unit tests. Development-capable templates carry the component and data-plane contract. | Low — additive |
| **1** | **DONE, e2e-validated on kind** (`TestE2EDevelopmentMode`, runs in the Infrastructure Template E2E workflow). `faros-dev-agent` extracted (`providers/infrastructure/dev-agent/`, `--install` injection, reload rules, `FAROS_DEV_*` configuration); `${faros.devImage.*}` / `${faros.devAgentImage}` tokens (env `FAROS_DEV_IMAGE_<TOOLCHAIN>` / `FAROS_DEV_AGENT_IMAGE`, chart + operator wired); backend-synthesized dev overlay (`backend/kro/devoverlay.go`); per-component data-plane URLs; `application` Template development block (node/node v1) + component dataPlane + CEL images-optional-in-dev rule. Both flagged kro assumptions validated live: `includeWhen` on two same-named Deployment variants is accepted and mode-splits correctly, and status expressions referencing mode-excluded resources stay unset in production and resolve in development. (Two apply-time gotchas the e2e caught, now handled in the overlay: env values need `${string(...)}` around int-typed CEL, and overlay ports/mounts must dedupe against what the workload already declares.) | ~~High~~ validated |
| **2** | **Core DONE** (portal catalog UI + creation-flow polish outstanding). `Project.spec.template` names the backing Template; selection (PUT `/api/projects/{p}/template`, assistant tool `select_project_template`) reads the Template live from the tenant catalog, tears the old dev instance down, and generates the dev binding (`farosMode: development`). Sync/logs/restart/env route per component (workspacePath prefix → `…/components/<c>/<verb>`); template previews use the instance's own `status.url`. The assistant prompt carries the bound Template (or interview guidance when none). The old fixed-runner creation default was removed (2026-07-04): new projects have no development environment until a Template is bound — by the assistant interview, the portal picker, or `PUT /template`. `select_project_template` sits in the workflow tool bundle so interview-profile turns can actually call it. | Medium |
| **3** | **Core DONE** (repo-import-at-creation UX outstanding). Code provider: `RepositoryCheckout` CR + controller (the CommitBundle flow in reverse; GitHub `RepositoryReader` reads the text tree via commit→tree→blobs, binary/oversized files skipped and reported) + `checkout_repository` MCP tool returning files inline and reclaiming the bundle. App Studio: `POST /api/projects/{p}/hydrate-workspace` reads the project repository through `code__checkout_repository`, writes the tree into the workspace (overwrite semantics), and triggers a development sync — making git the recoverable source of truth for template switches and lost workspaces. Repository import: `CreateProjectRequest.existingRepositoryRef` adopts an existing Code Repository (claim + release semantics — an adopted repository is never deleted with the project) and hydrates the workspace from it at creation; the `hydrate_workspace` assistant tool covers recovery and post-switch re-hydration. Remaining: portal import/catalog UI. | Medium |
| **4** | **DONE (2026-07-04).** All legacy sandbox-runner paths deleted outright (no migration, per the no-compat decision): the sandbox-runner template/image/Makefile targets/CI workflow, App Studio's sandbox target resolution and binding special-cases, the signed preview-URL flow and the whole preview-gateway stack (`previewgateway`/`previewtoken` packages, `preview-gateway` subcommand, chart workloads, Tilt wiring), and the `${faros.sandboxPreviewBaseDomain}` token. Templates are the only development path; previews are the instance's own URL. `DefaultNodeDevImage` is now plain `node:22-bookworm` (agent injected). Existing sandbox projects are recreated — code lives in git. Remaining: BYO-compute validation with a dev-mode template on a second infra provider. | Low |

Phases 0–1 were the de-risking core and live entirely in the infrastructure
provider. Phases 2–4 now provide the current Template-backed App Studio path;
the old fixed-runner special case and signed preview gateway are gone. The
remaining work listed above is intentionally limited to portal polish and
BYO-compute validation.

## 8. Non-goals (deferred)

- **Idle scale-to-zero.** A dev-mode `application` keeps a frontend, backend,
  and Postgres running continuously per project — strictly more than today's
  single always-on runner pod. Sleeping idle dev instances (scale dev
  Deployments to zero after inactivity, wake on preview/sync) is deliberately
  deferred to its own design; nothing here precludes it, since the dev agent
  already fronts every component's traffic.
- **Promotion mechanics.** How CI-built images flow back into a
  `farosMode: production` flip (registries, tags, triggers) — see §4.1a.

## 9. Security notes

- Dev mode never widens the data-plane trust model: per-component verbs go
  through the same authorize-as-caller + runtime-namespace confinement as
  today's flat verbs; the control token stays instance-scoped and
  provider-side.
- Platform dev images are a new supply-chain surface (they run tenant code
  with a platform-chosen toolchain); they must be pinned by digest via the
  `${faros.devImage.*}` config, mirroring the runner-image handling.
- The untrusted-code isolation caveats of
  [`app-studio-sandbox-runtime.md`](./app-studio-sandbox-runtime.md) now apply
  to *every* dev-mode component, including ones sharing a graph with real data
  services (the dev backend can reach the instance's Postgres — intended, but
  worth stating). NetworkPolicy egress rules from each Template's development
  overlay remain part of that generated graph.
