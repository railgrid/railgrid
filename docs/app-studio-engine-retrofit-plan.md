# App Studio engine retrofit

Status: **Phases 1–4 code complete** (2026-08-06); remaining: live dev-loop
verification (2.9) and the tenant claim rollout (operating rule below)

Add permission-claim parity, deterministic reconcilers, and a real controller
substrate to App Studio. Bootstrap is already shared through provider-sdk
`init`; lifecycle actions should converge from durable desired state instead
of remaining synchronous HTTP operations acting as the caller.

Key facts about app-studio that shape the plan:

- Bootstrap is ALREADY the provider-sdk flow (`providers/app-studio/init_cmd.go`
  → `sdkinstall.Bootstrap`, chart initContainer). Nothing to port.
- `Project.spec.environments[].bindings[].resourceRef` already records
  group/version/**resource**/kind + raw values — the binding contract is
  already self-contained; the reconciler never needs to read Templates.
- No controller-runtime anywhere; `tenant/` is a per-request caller-scoped
  dynamic client over the hub's kcp proxy with NO Watch/informer loop.
  The reconciler rides the APIExport VW via kcp multicluster-provider
  (per-shard fan-out — one endpoint per shard, binding one URL hides tenants).
- Single-writer invariant: chart hard-fails `replicaCount != 1`. The manager
  rides in the same pod; no leader election. Do not scale.
- `run-provider-app-studio` already exports `RAILGRID_PROVIDER_KUBECONFIG` at
  serve time; the chart mounts the kubeconfig only into the init container.

## Phase 1 — permission-claims parity (small, do first)

App-studio claims only `secrets` today. The Project reconciler will create /
update / delete infrastructure instances in tenant workspaces, which needs
first-party claims with the infrastructure APIExport identityHash.

Claims live in THREE places that must stay in sync: `init_cmd.go` (APIExport),
`manifest.yaml` (dev register),
`deploy/chart/templates/catalogentry.yaml` (prod Enable).

- [x] 1.1 `init_cmd.go`: instance claims (`applications`, `simplewebapps`,
      `workers` @ `infrastructure.railgrid.ai`, full verbs) with
      `APP_STUDIO_INFRA_IDENTITY_HASH`; keep the `secrets` claim.
      Warn loudly when the hash env is empty (claims become inert, not broken).
- [x] 1.2 `manifest.yaml`: same claims, `tenantScoped: true`.
- [x] 1.3 `deploy/chart/templates/catalogentry.yaml`: same claims.
- [x] 1.4 Chart: `apiExport.infraIdentityHash` value → env
      `APP_STUDIO_INFRA_IDENTITY_HASH` on the **init** container;
      values.yaml comment explaining where the admin copies it from
      (/bonkers root-identities, or the infra APIExport `status.identityHash`).
- [x] 1.5 Makefile `init-provider-app-studio`: auto-discover the infra
      identityHash; the environment override wins.
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
      `ai.railgrid.ai/v1alpha1`.
- [x] 2.2 `controller_manager.go`: APIExport provider on
      endpointSlice `ai.railgrid.ai`, metrics disabled, started from
      `runServe` in a 15s retry loop (init ordering is not guaranteed),
      `errControllerDisabled` sentinel when no kubeconfig in scope. Reuses
      the existing `loadProviderConfig`.
- [x] 2.3 Chart: ALREADY DONE pre-retrofit — the serve container mounts the
      kubeconfig Secret and sets `RAILGRID_PROVIDER_KUBECONFIG`
      (deployment.yaml); `automountServiceAccountToken: false` kept.
- [x] 2.4 `controller/project/`: Project reconciler. IMPORTANT DEVIATION
      from the original plan: it lifecycles provider-resource bindings in
      **every** environment (live AND artifact), not just live — promotion
      appends the artifact-mode production binding and the old code path
      provisioned it explicitly in the promote handler; the reconciler now
      owns that too (promotion is a spec write). Finalizer
      `ai.railgrid.ai/instances`, converge-on-drift updates (spec /
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

- [x] 3.1 Claims: `repositories` @ code.railgrid.ai (identityHash via
      `APP_STUDIO_CODE_IDENTITY_HASH` / Helm `apiExport.codeIdentityHash` /
      Makefile auto-discovery) + `serviceaccounts`/`secrets`/`clusterroles`/
      `clusterrolebindings` — in all THREE claim places.
      **Superseded (2026-09-19).** Every claim listed here is gone. The
      identity-minting ones (`serviceaccounts`, `clusterroles`,
      `clusterrolebindings`) are a contract violation outright. The
      first-party ones were briefly restored and then removed again the same
      day, for the reason they were avoided the first time: a claim on a
      `*.railgrid.ai` group pins ONE serving APIExport `identityHash` for
      every consuming workspace at once, so a workspace bound to an org-owned
      infrastructure or code provider gets nothing served, silently. What the
      reconcilers need on a dependency is declared as
      `spec.dependencies[].composes` instead and granted through hub-minted
      scoped identities acting inside each tenant workspace (3.4b).
      `secrets` stays, and is now the only claim: the LLM model credentials
      and the promotion pull Secret this provider writes itself.
- [x] 3.2 CRD: `spec.repository.adopted` (additive) so the reconciler can
      tell created-by-us from imported; `make codegen-app-studio-provider`
      regenerated CRD + APIResourceSchema + chart schema.
- [x] 3.3 `hubmcp/` — MCP JSON-RPC client for `code__commit_files` via the
      hub's per-tenant aggregate MCPServer (port of vibe's
      provision/codemcp.go; the api layer keeps its own caller-token path).
      **Superseded for the reconciler (2026-09-20, §9 Cut D.1).** The
      convergence loop no longer speaks MCP: it invokes the Code provider's
      `repositories/commit/v1` action (`controller/project/commitaction.go`),
      staging through `repositories/stage-commit-bundle` when the payload is
      past the catalogue's 1 MiB input ceiling. `hubmcp` stays for its wire
      constants and encoding helpers, which the api layer and the bundle
      builder still share, and for the api layer's own MCP tools (checkout,
      build status, rebuild) — including the assistant's
      `commit_project_files`, which has NOT moved.
- [x] 3.4 `controller/project/identity.go` — per-PROJECT ServiceAccount
      (vibe's is per-session; app-studio has no Session CR): SA +
      ClusterRole (infra RO, code RW) + binding + legacy token Secret, all
      ownerRef'd to the Project → GC'd with it.
      **Superseded (2026-09-19, §9 Cut C).** The provider no longer mints
      identities: it asks the hub (`provider-sdk/identityclient`), which
      checks every rule against a policy, records what it issued and collects
      it when the owner goes. The token is TTL'd and re-minted at 80% of its
      life, and the rules are restated on every refresh, so a rebinding
      actually shrinks the grant.
      There is ONE such identity per Project (and one per Studio), and it
      carries everything: what the project acts as when something acts as it
      — `use` on the workspace MCPServer, `get` on the APIBindings, `create`
      on the declared `instances/{verb}` and
      `connections/mint-registry-token` — AND what this provider's own
      reconcilers do to the dependency objects inside that workspace, bounded
      by the composition the CatalogEntry declares
      (`spec.dependencies[].composes`; `internal/crossprovider/composition.go`).
      The reconcilers therefore hold two clients: the manager's cluster client
      over this provider's APIExport virtual workspace for the Project,
      Studio, Session and Secret kinds it owns, and a
      `tenantaccess`-built client at `{hub}/clusters/{cluster}` as the
      identity for everything belonging to infrastructure or code.
      `controller/tenantwatch` is the matching watch: per workspace, fed the
      same token, re-`Ensure`d when it rotates.
      3.1's `serviceaccounts` / `clusterroles` / `clusterrolebindings` claims
      are gone either way — a claim on those types is a contract violation
      (`docs/provider-connectivity-contract.md` §"Scoped identities").
- [x] 3.5 `controller/project/repository.go` — ensureRepository
      (create-if-missing with autoInit; NEVER creates adopted bindings;
      repositories are never deleted on Project delete — handler-side claim
      release only). Adoption itself stays caller-side (claims an existing
      CR, needs the importing user's view).
- [x] 3.6 `controller/project/commit.go` — commit convergence: workspace
      `UncommittedPaths` → `repositories/commit/v1` as the project identity
      (was `code__commit_files`; see 3.3), gated on
      (a) repository Ready, (b) project idle via `api.Server.AssistantBusy`
      (run manager + supervisor reservations), sharing the workspace
      settlement ledger (`RecordCommitSettlement`/`ReconcileCommitSettlement`)
      with the assistant's interactive commit tool so neither double-commits.
      Missing files → `{path, delete: true}` entries; oversized files stay
      dirty for an interactive commit, and binaries always travel base64
      (the action's schema declares the encoding, so there is no capability
      probe any more). Every commit is pending when it is made: the action
      returns the `RepositoryCommit`'s name and the existing watch settles
      it, so the rate-limited case is no longer a separate path through a
      parsed error string. Scope bridge: `ai.railgrid.ai/org-uuid` +
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
- Build configuration is template/scaffold-owned. App Studio no longer
  generates or injects workflow/config commits; ordinary file edits plus the
  explicit `commit_project_files` flow are the only mutation path.

Behavior change to socialize: any workspace edit left uncommitted after a
turn ends is now committed automatically ("chore: sync workspace (...)") by
the reconciler once the project is idle — git converges like vibe. The
assistant's interactive commits keep working unchanged.

## Phase 4 — Session CRs + Studio search (code complete 2026-08-06)

User request: "session in CR too, mapped to postgres — easier to track/debug"
and "studio search — do that too". Both are straight vibe ports.

- [x] 4.1 `Session` CRD (`sessions.ai.railgrid.ai`): control-plane
      projection of one assistant thread. Postgres stays authoritative; the
      CR mirrors it (`kubectl get sessions.ai` shows title/phase/active
      turn). Name = thread ID; ownerRef → Project (project deletion GCs the
      conversations); identity annotations (org/workspace UUID +
      `project-uid`) bridge to the store keyspace.
- [x] 4.2 `controller/session`: event-driven status mirror (the assistant
      supervisor signals the reconciler on turn transitions; 10m safety
      resync) + purge finalizer
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
- `manifest.yaml` and `deploy/chart/templates/catalogentry.yaml` identical
  (claims AND `dependencies[].composes`), and `make codegen-app-studio-provider`
  idempotent; there is no claim list in Go to keep in step any more
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
- 2026-09-20: Cut D.1 and D.4 (remediation §9). **D.1** — one commit path:
  `internal/codecommit` makes the Code provider's `repositories/commit/v1`
  call (staging oversized payloads through `stage-commit-bundle`), and both
  the Project reconciler and the assistant's `commit_project_files` tool go
  through it, differing only in whose bearer they carry. `code__commit_files`
  now has no caller in this provider, which retires the base64 capability
  probe with it — the action's schema declares the encoding. The assistant no
  longer settles the workspace ledger at the tool boundary: the action does
  not wait for the commit to land, so it records the pending commit and the
  `RepositoryCommit` watch settles it, the same as the reconciler's own
  commits. **D.4** — deleting a project is a DELETE of the Project CR. The
  `projects/{p}/delete` verb is gone from `api/dataplane_table.go`,
  `manifest.yaml` and the chart's `catalogentry.yaml`; the portal deletes the
  object with the kube client (UID as a precondition, the repository-deletion
  opt-in stamped as an annotation first) and polls until it disappears. The
  teardown is `controller/project/teardown.go`: stop the assistant, release or
  delete the Code Repository, delete the instances, purge conversations and
  attachments, revoke the identity, remove the working tree. The
  coding-sandbox cache is left to its Project ownerReference. Composition
  widened by one verb — `delete`, name-scoped, on `repositories` — because the
  human's own `delete` through the verb became the project identity's;
  `manifest.yaml`, the chart and `internal/crossprovider` are in step and
  `TestCompositionRulesMatchTheManifest` pins it. Also in this change set: the
  `secrets` claim is selector-scoped to `railgrid.ai/owner: app-studio`, the
  portal's LLM-credential writer stamps that label, and the promotion's
  registry pull Secret stamps `railgrid.ai/owner: infrastructure` because the
  infrastructure provider is what reads it.
- Not done in that change set (both have since landed — see the D.2 and D.3
  entries below): **D.3** (source-tree authority off the PVC) and **D.2**
  (`Session` as thread owner, `LISTEN/NOTIFY` behind SSE, retention as a
  `Session` reconciler). See `app-studio-replica-awareness.md` §"What still
  requires affinity".
- 2026-09-20: Cut D.2 (remediation §9), plus the module taken to a clean
  golangci-lint run. **D.2** — `SessionStatus` gained `turnCount` and
  `lastActivityAt` (schema bumped to `v260920-91c16a31.sessions.ai.railgrid.ai`;
  the fields are fed by a new `store.AssistantThreadActivityReader`, one query
  in Postgres and a map fold in memory). The SSE stream's 250 ms per-connection
  poll (`api/assistant_threads.go`) became Postgres `LISTEN/NOTIFY`: an append
  issues `pg_notify` on `app_studio_assistant_thread_events` with a
  length-prefixed thread key, one `pq.Listener` per process fans it out through
  `store/thread_notify.go`, and the memory store implements the same
  `AssistantThreadEventWatcher` with the broadcaster alone. The signal carries
  no payload — every reader re-reads from its own cursor, so coalescing is
  correct — and the 15 s keepalive is the safety net, which is also how a
  listener reconnect (lib/pq's nil notification wakes every subscriber) is
  covered. `runRetention` is deleted from `main.go`: retention is the Session
  reconciler's computed-deadline `RequeueAfter` at
  `status.lastActivityAt + APP_STUDIO_MESSAGE_RETENTION`, deleting the Session
  so the existing purge finalizer does the work — one owner (the leader), one
  conversation at a time, and an in-flight turn has no deadline at all. That is
  a semantic change from a fleet-wide message cutoff to per-conversation
  retention, documented in `deploy/chart/values.yaml` next to `messageRetention`
  and in `app-studio-runtime-decoupling.md`. `runAttachmentRetention` stays a
  sweep, deliberately: an unclaimed draft has no owning Session.
  **Lint** — `hack/tools/golangci-lint run ./...` went from 151 uncapped
  findings to 1, with no `//nolint`: every unchecked error handled or discarded
  with a reason, ~50 dead declarations deleted (including the whole unused
  `prepareSnapshotFile*`/`restoreFileState` chain in `workspace/snapshot.go`
  and three `recoverProjectAssistantStartReplay*` wrappers), the deprecated
  `net.Error.Temporary` and `ModelContext.Tools` uses removed (state.ToolInfos
  is the source of truth, so `refreshExecutableToolContext` stopped taking a
  ModelContext at all), `ParamsOneOf` audit contracts marshalled from
  `ToJSONSchema()` instead of an opaque struct that encoded as `{}`, and a dead
  `lastSeen` fence removed from the run-sandbox watch. The one remaining
  finding is SA1019 on `adk.State` in `api/assistant_eino_callbacks.go`: it is
  a deprecated alias for an UNEXPORTED eino type, `compose.ProcessState`
  requires exact type identity, and the callback is the only hook that can
  strip attachment bytes from the graph state while leaving them in the model
  input. The non-deprecated route is to inject attachments in a `WrapModel`
  wrapper instead of in `BeforeModelRewriteState` — a real refactor of the
  vision path, not a lint fix.
- 2026-09-20: Cut D.3 (remediation §9) — the working-copy ledger is off the
  PVC. **The type, and why it is not `RepositoryCheckout`.** The handover
  proposed new fields on the Code provider's `RepositoryCheckout`; that kind is
  a one-shot operation object (spec: a repositoryRef plus a ref; status: the
  checkout's own result) with no home for "the revision this project's working
  copy is at", and putting it there would have meant new fields on another
  provider's CRD, a `repositorycheckouts` entry in `dependencies[].composes`
  and a clause E, for state that provider never reads. The ledger is App
  Studio's own state about a project App Studio owns, so it is
  `Project.status.workspace {sourceRevision, uncommittedPaths[], pendingCommit,
  settlement}` — clause A, no composition change, no code-provider change.
  `uncommittedPaths` is bounded honestly rather than arbitrarily: the tree is
  capped at 500 files, so the largest single transition is ≤500 writes + ≤500
  deletions, `MaxItems: 1024` clears it, and each entry is capped at the 1024
  bytes `workspace.MaxProjectPathBytes` already accepts so nothing the store
  admits can fail to record. (The handover suggested 512; that would have
  rejected paths the store accepts.) Overflow is an error, never a truncation:
  a dropped path is a file that never reaches git.
  **Mechanism.** `workspace.Ledger` is an interface; `internal/projectledger`
  implements it over the status subresource with merge patches that carry
  their `resourceVersion`, so every update is a compare-and-swap that retries
  the loser instead of overwriting the winner. Which client to use is a
  per-call question, so the ledger rides the context and is attached in exactly
  three places: `identityFromRequest` (every handler that resolves a caller
  gets the CALLER's, lazily — no client is built unless the ledger is touched),
  `runProjectAssistantWorker` (a turn outlives its request), and `Reconcile`
  (the manager's client). `NewFileStore` keeps an in-process ledger for tests
  and local runs; `main.go` calls `RequireContextLedger()`, so in a deployment
  a path that forgot to attach one is an error rather than a silent return to
  pod-local authority.
  **Two simplifications fell out.** The `ai.railgrid.ai/pending-commit`
  annotation is gone: the pointer and the record it pointed at are one member
  of one object now, the reconciler branch for the two disagreeing is deleted,
  and the project identity's named `get` grant reads the same record the
  convergence loop follows. `InitializeRepositorySource` lost its
  `initial-repository` receipt file; the Project annotation the caller stamps
  is the once-only record, and the reconciler clears it.
  **One bug the move exposed and fixed**: the reconciler held `p` across ledger
  writes and then `Update`d it, which now loses a race with its own status
  write every time. Annotation writes are merge patches
  (`patchProjectAnnotation`).
  **Hydration is one `ReplaceTree`, not a `PutFile` per file** — a
  file-at-a-time rebuild would be a control-plane write per checked-out file —
  and it is marked `Committed`, a new option meaning "these bytes ARE the
  repository's": the paths come back CLEAN instead of queued for a commit that
  would push git's own content back to git.
  **What stays pod-local**: the tree itself, plus one honest cache tag
  (`workspace/tree_revision.go`) recording which revision THIS directory's
  bytes were written at — the comparison that moving the revision to the
  control plane took away, since the ledger's number is now the same
  everywhere. Absent, behind or untagged all read as stale and rebuild.
  **Chart**: `strategy: Recreate` and `replicaCount: 1` stay, for the shared
  single-session Playwright Browser alone, and `values.yaml` now says that in
  those words; the `runSandbox.mode=force` + `replicaCount > 1` `fail` stays
  (no distributed CAS); the `ReadWriteOnce` claim stays because a project with
  no repository yet has nothing to rebuild from, and `emptyDir: true` became a
  supportable production choice for installations where every project has one.
  Project affinity is kept as a cache-locality optimization and documented as
  one: correctness no longer depends on it, but rebuilding per request under
  round-robin would be a git checkout per request.
  Full module build/vet/tests green (17 packages), `golangci-lint` back at the
  single justified `adk.State` finding, `helm template` renders, codegen
  idempotent (the same run also regenerated the Session CRD, which Cut D.2 had
  left behind).
