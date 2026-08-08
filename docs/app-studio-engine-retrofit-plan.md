# App Studio engine retrofit — vibe-studio patterns into app-studio

Status: **Phases 1–4 code complete** (2026-08-06); remaining: live dev-loop
verification (2.9) and the tenant claim rollout (operating rule below)

Retrofit the engine patterns proven in vibe-studio (permissions, deterministic
reconcilers, bootstrap) into app-studio. Bootstrap is already shared
(provider-sdk `init`); the work is claims parity and introducing a real
controller substrate where today every lifecycle action is a synchronous HTTP
handler acting as the caller.

Reference implementations (do not diverge without a reason):

- `providers/vibe-studio/controller_manager.go` — multicluster manager +
  init-race retry loop
- `providers/vibe-studio/controller/project/` — instance lifecycling from
  self-contained bindings, finalizer teardown, status mirror
- `providers/vibe-studio/init_cmd.go` — claims with identity hashes
- `providers/vibe-studio/controller/vibesession/identity.go` — per-session SA
  (Phase 3 only)

Key facts about app-studio that shape the plan:

- Bootstrap is ALREADY the provider-sdk flow (`providers/app-studio/init_cmd.go`
  → `sdkinstall.Bootstrap`, chart initContainer). Nothing to port.
- `Project.spec.environments[].bindings[].resourceRef` already records
  group/version/**resource**/kind + raw values — the binding contract is
  already self-contained; the reconciler never needs to read Templates.
- No controller-runtime anywhere; `tenant/` GraphQL transport has NO Watch.
  The reconciler rides the APIExport VW via kcp multicluster-provider
  (per-shard fan-out — one endpoint per shard, binding one URL hides tenants).
- Single-writer invariant: chart hard-fails `replicaCount != 1`. The manager
  rides in the same pod; no leader election. Do not scale.
- `run-provider-app-studio` already exports `KEDGE_PROVIDER_KUBECONFIG` at
  serve time; the chart mounts the kubeconfig only into the init container.

## Phase 1 — permission-claims parity (small, do first)

App-studio claims only `secrets` today. The Project reconciler will create /
update / delete infrastructure instances in tenant workspaces, which needs
first-party claims with the infrastructure APIExport identityHash.

Claims live in THREE places that must stay in sync (learned the hard way in
vibe): `init_cmd.go` (APIExport), `manifest.yaml` (dev register),
`deploy/chart/templates/catalogentry.yaml` (prod Enable).

- [x] 1.1 `init_cmd.go`: instance claims (`applications`, `simplewebapps`,
      `workers` @ `infrastructure.kedge.faros.sh`, full verbs) with
      `APP_STUDIO_INFRA_IDENTITY_HASH`; keep the `secrets` claim.
      Warn loudly when the hash env is empty (claims become inert, not broken).
- [x] 1.2 `manifest.yaml`: same claims, `tenantScoped: true`.
- [x] 1.3 `deploy/chart/templates/catalogentry.yaml`: same claims.
- [x] 1.4 Chart: `apiExport.infraIdentityHash` value → env
      `APP_STUDIO_INFRA_IDENTITY_HASH` on the **init** container;
      values.yaml comment explaining where the admin copies it from
      (/bonkers root-identities, or the infra APIExport `status.identityHash`).
- [x] 1.5 Makefile `init-provider-app-studio`: auto-discover the infra
      identityHash (mirror the vibe block at the vibe init target), env
      override wins.
- [x] 1.6 Build + `helm template` render green.

Rollout caution (NOT a checkbox — an operating rule): the hub rewrites
permissionClaims only on Enable, and re-Enabling RECREATES the tenant
APIBinding and WIPES all Project CRs. For already-enabled tenants, patch
`apibinding app-studio` spec.permissionClaims by hand (copy an existing
infrastructure claim, swap `resource`; identityHash is per-APIExport).

## Phase 2 — controller substrate + Project reconciler (the real work)

Inversion: handlers stop provisioning inline as the caller; they write
`Project.spec` and the reconciler converges instances under the provider's
claimed identity, mirroring status back.

- [x] 2.1 Deps: `sigs.k8s.io/controller-runtime@v0.24.1`,
      `sigs.k8s.io/multicluster-runtime@v0.24.1`,
      `github.com/kcp-dev/multicluster-provider@v0.8.0` in
      `providers/app-studio/go.mod`; new `scheme/` package registering
      `ai.kedge.faros.sh/v1alpha1` (mirror `providers/vibe-studio/scheme`).
- [x] 2.2 `controller_manager.go`: ported from vibe — apiexport provider on
      endpointSlice `ai.kedge.faros.sh`, metrics disabled, started from
      `runServe` in a 15s retry loop (init ordering is not guaranteed),
      `errControllerDisabled` sentinel when no kubeconfig in scope. Reuses
      the existing `loadProviderConfig`.
- [x] 2.3 Chart: ALREADY DONE pre-retrofit — the serve container mounts the
      kubeconfig Secret and sets `KEDGE_PROVIDER_KUBECONFIG`
      (deployment.yaml); `automountServiceAccountToken: false` kept.
- [x] 2.4 `controller/project/`: Project reconciler. IMPORTANT DEVIATION
      from the original plan: it lifecycles provider-resource bindings in
      **every** environment (live AND artifact), not just live — promotion
      appends the artifact-mode production binding and the old code path
      provisioned it explicitly in the promote handler; the reconciler now
      owns that too (vibe semantics: promote = a spec write). Finalizer
      `ai.kedge.faros.sh/instances`, converge-on-drift updates (spec /
      labels / ownerRef; status-only changes are not drift), 15s requeue
      while not Ready, 60s drift poll when Ready, IsInvalid/invalid-binding
      errors parked in status.outputs.error instead of hot-looping.
      Desired-state + status-fold logic lives in the new shared `bindings/`
      package so api and controller can never disagree.
- [x] 2.5 Status parity: reconciler mirrors via the same fold helpers the
      old sync used, touching ONLY `status.environments` (Phase/UpdatedAt/
      unmanaged entries preserved via MergeEnvironmentStatuses).
- [x] 2.6 Handler inversion: create (projects.go) writes spec only; delete
      relies on the finalizer (+ ownerRefs); template select returns
      read-through status; promote is now a pure spec write with
      read-through. `deleteProjectProviderResources` KEPT solely for the
      failed-creation cleanup path (CR may predate the finalizer).
      `deleteProjectDevelopmentBindingResources` (template switch) also
      KEPT — the reconciler cannot sweep instances whose GVR left the spec.
- [x] 2.7 `api/provider_resources.go` reduced to read-through status +
      cleanup teardown + thin delegations into `bindings/`.
- [x] 2.8 Tests: `bindings/bindings_test.go` (desired-state self-containment,
      name fallbacks, invalid-binding taxonomy, phase extraction, status
      merge) + `controller/project/controller_test.go` (all-env selection,
      drift detection); the two api creation tests reasserted onto the
      spec-only contract. Full module: build, vet, `go test ./...` (8 pkgs),
      `helm template` — all green. `go mod tidy` run.
- [x] 2.9 Dev loop verified LIVE (2026-08-06, tilt): controller engages,
      instance + Repository materialized by the reconciler, status mirror
      Ready, Session CR projected, Studio + searxng Ready. Two bugs found
      and fixed in the process (see progress log): the Tiltfile.cluster
      kubeconfig override pointed the manager at /clusters/root (silent
      zero-reconcile), and the hub adds NEW claims to existing APIBindings
      as state=Rejected (patch to Accepted, then kick the backoff with an
      annotation). Still unobserved live: idle auto-commit + web_search
      (project was deleted before going idle) — check on the next project.

## Phase 3 — repositories + durable identity + commit convergence (code complete 2026-08-06)

Full retrofit of vibe's git model (user decision: no phasing): repo creation
is reconciler-owned, commits converge on the reconcile loop under a
per-project ServiceAccount.

- [x] 3.1 Claims: `repositories` @ code.kedge.faros.sh (identityHash via
      `APP_STUDIO_CODE_IDENTITY_HASH` / Helm `apiExport.codeIdentityHash` /
      Makefile auto-discovery) + `serviceaccounts`/`secrets`/`clusterroles`/
      `clusterrolebindings` — in all THREE claim places.
- [x] 3.2 CRD: `spec.repository.adopted` (additive) so the reconciler can
      tell created-by-us from imported; `make codegen-app-studio-provider`
      regenerated CRD + APIResourceSchema + chart schema.
- [x] 3.3 `hubmcp/` — MCP JSON-RPC client for `code__commit_files` via the
      hub's per-tenant aggregate MCPServer (port of vibe's
      provision/codemcp.go; the api layer keeps its own caller-token path).
- [x] 3.4 `controller/project/identity.go` — per-PROJECT ServiceAccount
      (vibe's is per-session; app-studio has no Session CR): SA +
      ClusterRole (infra RO, code RW) + binding + legacy token Secret, all
      ownerRef'd to the Project → GC'd with it.
- [x] 3.5 `controller/project/repository.go` — ensureRepository
      (create-if-missing with autoInit; NEVER creates adopted bindings;
      repositories are never deleted on Project delete — handler-side claim
      release only). Adoption itself stays caller-side (claims an existing
      CR, needs the importing user's view).
- [x] 3.6 `controller/project/commit.go` — commit convergence: workspace
      `UncommittedPaths` → `code__commit_files` as the project SA, gated on
      (a) repository Ready, (b) project idle via `api.Server.AssistantBusy`
      (run manager + supervisor reservations), sharing the workspace
      settlement ledger (`RecordCommitSettlement`/`ReconcileCommitSettlement`)
      with the assistant's interactive commit tool so neither double-commits.
      Missing files → deletePaths; binary/oversized files stay dirty for an
      interactive commit. Scope bridge: `ai.kedge.faros.sh/org-uuid` +
      `/workspace-uuid` annotations stamped on the Project at create
      (legacy Projects without them are skipped silently).
- [x] 3.7 Wiring: shared workspace FileStore instance (HTTP layer +
      reconciler, same PVC + ledger), `controllerDeps` into
      startControllerManager, `createProjectRepository` deleted from api.
- [x] 3.8 Tests (fake-client ensureRepository create/adopted-skip,
      repositoryReady, scopeOf) + full module build/vet/tests/chart green.

NOT ported, deliberately:
- Registry pull secret re-derivation each pass (vibe registry.go): rides
  `spec.repository.tokenSecret`, which app-studio's binding doesn't have;
  app-studio's promote-time minting already covers the need.
- Session CR mirror/purge: app-studio sessions are Postgres rows with no CR;
  nothing to mirror until that model changes.
- Build-config commits stay on the request path: they piggyback the
  interactive commit flow (`ensureProjectBuildConfig` after commit_files
  succeeds), and vibe has no build reconciler to port yet either.

Behavior change to socialize: any workspace edit left uncommitted after a
turn ends is now committed automatically ("chore: sync workspace (...)") by
the reconciler once the project is idle — git converges like vibe. The
assistant's interactive commits keep working unchanged.

## Phase 4 — Session CRs + Studio search (code complete 2026-08-06)

User request: "session in CR too, mapped to postgres — easier to track/debug"
and "studio search — do that too". Both are straight vibe ports.

- [x] 4.1 `Session` CRD (`sessions.ai.kedge.faros.sh`): control-plane
      projection of one assistant thread. Postgres stays authoritative; the
      CR mirrors it (`kubectl get sessions.ai` shows title/phase/active
      turn). Name = thread ID; ownerRef → Project (project deletion GCs the
      conversations); identity annotations (org/workspace UUID +
      `project-uid`) bridge to the store keyspace.
- [x] 4.2 `controller/session`: 30s status mirror + purge finalizer
      (deleting the Session CR deletes the thread from Postgres, reading the
      thread's own actor for the store's owner check); a projection whose
      store row is gone deletes itself. Sessions without identity
      annotations are left inert.
- [x] 4.3 API wiring: thread create → best-effort Session CR; thread delete
      → best-effort CR delete (reconciler covers both directions).
- [x] 4.4 `Studio` CRD (singleton `studio`) + `controller/studio`: one
      shared searxng instance per workspace (`app-studio-search`),
      finalizer teardown, spec-self-contained resourceRef (resolved from
      the searxng Template by the API at creation, never by the
      reconciler). `searxngs` claim added in all THREE claim places.
- [x] 4.5 `ensureStudio` on project create (single-flight per workspace,
      best-effort — no Studio means no web search, not no builds).
- [x] 4.6 Assistant tools `web_search` + `web_fetch`
      (api/assistant_web_tools.go, port of vibe webtools): search proxies
      the shared instance over the infra dataplane with the caller's
      bearer; fetch is SSRF-guarded (resolved-address dial control defeats
      DNS rebinding). Registered read-risk + parallel-safe.
- [x] 4.7 Makefile codegen now copies sessions + studios APIResourceSchemas
      into the chart; scheme registers the new types; session + studio
      reconcilers registered in the controller manager (Store dep added).
- [x] 4.8 Tests (session mirror/purge/scope, tool-order tests updated) +
      full build/vet/tests/chart green.

Rollout notes: re-run `init` (new schemas + searxngs claim) and patch
existing tenant APIBindings (claim rule below). Existing threads have no
Session CRs — only new threads are projected (a backfill sweep is possible
later if wanted). `kubectl get sessions.ai,studios.ai` needs the tenant to
rebind or the APIBinding to pick up the new schemas.

## Verification gates (every phase)

- `go build ./... && go vet ./...` in `providers/app-studio` (standalone module)
- `helm template deploy/chart` renders
- claims identical across init_cmd.go / manifest.yaml / catalogentry.yaml
- portal `vue-tsc` untouched by Phase 1–2 (no portal changes expected)

## Progress log

- 2026-08-06: plan written; assessment done (bootstrap already shared;
  binding contract already self-contained; effort concentrated in Phase 2).
- 2026-08-06: Phase 1 complete (claims in all three places + chart env +
  Makefile hash auto-discovery).
- 2026-08-06: Phase 2 code complete — `bindings/` shared package,
  `controller/project` reconciler (all-environment scope, see 2.4),
  `controller_manager.go` + retry-loop serve wiring, handlers inverted to
  spec-only writes, tests/vet/build/chart green. Behavior change to
  socialize: create/template/promote responses now report instances
  Pending; they converge asynchronously. NOT yet verified against a live
  hub (2.9) — needs a running dev stack; remember the tenant APIBinding
  claim-patch rule before testing against an already-enabled tenant.
- 2026-08-06: Phase 3 code complete (full retrofit, user opted out of
  phasing) — repositories claim + code identityHash, per-project SA,
  hubmcp client, reconciler-owned repository creation, busy-gated
  settlement-sharing commit convergence, scope annotations at create.
  All module tests/vet/build/chart green. Live verification (2.9) now
  covers Phase 3 too: expect the repo to appear without the handler
  creating it, and a dirty workspace to self-commit once idle.
