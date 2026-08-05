# vibe-studio: wizard-first app builder provider

Status: design proposal (2026-08-01). Replaces app-studio.

## 0. Executive summary

vibe-studio is a new provider that replaces app-studio with a deliberately lighter
architecture and a different front door: instead of dropping the user into a blank
chat, it starts with a **wizard** that collects intent (what app, for whom, what
data, what integrations), **recommends an infrastructure Template** from the live
catalog, provisions a dev sandbox from the template's scaffold, and only then
enters conversational building ("vibing") with live preview, build, and promotion
to production.

The pipeline it automates already exists and is proven — it is exactly the
`kedge-create-app` skill flow (`list_templates → describe_template → provision
kedgeMode=development → dev_sync → dev_logs → preview → repo → CI image →
provision production`). vibe-studio is that flow with a product UI on top and a
thin harness in the middle.

Size target: **≤10k non-test Go LOC** (app-studio is 39k, of which 19.6k is the
assistant harness alone). The harness budget is ~1.5k LOC by adopting the agents
provider's engine shape instead of app-studio's.

Key decisions (details in §4–§6):

| Decision | Choice |
|---|---|
| Harness framework | Eino, **raw `BaseChatModel`** (agents-engine shape), not `adk`/`deep`/middlewares |
| Wizard mechanics | one free-form box → model-proposed structured questions (≤3 rounds) → Blueprint approval |
| Phase enforcement | **tool visibility per phase** (host-side), not prompt text, not transcript string-matching |
| Session state | append-only event log in Postgres (fold → state), SSE projection to portal |
| Run model | runs/messages/events in Postgres; **one CRD** (`Project`) in kcp |
| Data plane | reuse infra provider dataplane verbs via `dataplane_client.go` — no runtime kubeconfig |
| Preview | instance `status.url` + edge-readiness probe (no preview-gateway, no console signer) |
| Promotion | app-studio's second-environment model (`kedgeMode: production` + built digests), kept |

## 1. Market analysis

Research date 2026-08-01. Full sources in the research transcript; headline facts:

### 1.1 The field

| | Lovable | Bolt.new | v0 (Vercel) | Replit Agent | Firebase Studio | Base44 (Wix) |
|---|---|---|---|---|---|---|
| Scale | ~$400–500M ARR, $6.6B→$12B talks | ~$100M ARR trajectory, ~$700M val | ~$30–70M ARR est. | $150M+ ARR, $9B val | — | ~$50M ARR |
| Intake | signup-segmentation wizard only, then blank prompt | blank prompt | blank prompt | **plan approval + clarifying Qs** (only major one) | app-blueprint approval (**sunset 2027-03**) | blank prompt |
| Template-grounded gen | no (house stack from scratch) | no | design-system registry (UI only) | loose | yes, 60+ (dying) | no |
| Preview | cloud sandbox (Modal/gVisor) | browser WebContainers | Vercel sandbox | cloud VM (NixOS/GCP) | cloud IDE | cloud |
| Prod hosting | theirs (+GitHub 2-way sync) | theirs (Bolt Cloud) | Vercel only | theirs | user's GCP project | theirs, no export |
| Env promotion | none (version restore only) | none | Vercel previews (frontend) | checkpoints/rollback | none | none |
| BYO / self-host | no | Azure own-tenant (enterprise, 2026) | no | no | no | no |

Newcomer of note: Emergent.sh ($100M ARR in 8 months, runs on k8s pods
internally, markets code export). OSS demand signal: Dyad (local, BYO-key, 1M+
downloads) and Sandboxd ("self-hosted Lovable") prove the no-lock-in appetite.

### 1.2 The gaps (= vibe-studio's positioning)

1. **Wizard-first intake is unoccupied.** Nobody maps structured intent capture
   to an infrastructure recommendation. Replit's plan-approval step (bills only
   after approval) and Tempo's PRD-first flow prove structured intake converts;
   Firebase Studio was the closest full analog and Google is killing it
   (signups closed 2026-06-22, dead 2027-03-22).
2. **Template-grounded generation attacks the two biggest trust failures.**
   Every documented security disaster (Lovable CVE-2025-48757: 170+ apps with
   missing RLS; the April 2026 BOLA breach) came from per-prompt from-scratch
   backend generation. A curated, security-reviewed scaffold where the AI fills
   in app logic — not auth, not policies — is a structurally different risk
   class. It also attacks complaint #1 (paying for the model to flail): builds
   start from a known-good running skeleton, not a blank page.
3. **dev→prod promotion of the whole stack is a product primitive nobody has.**
   Replit has rollbacks, Vercel has frontend previews; nobody promotes app +
   backing infra (DB, secrets, domains). Kubernetes/kro is exactly the right
   machinery, and same-template-same-substrate erases the "works in preview,
   breaks in prod" class that is architectural (unfixable) for WebContainers.
4. **BYO compute / self-host has zero commercial coverage** below enterprise
   tier. kedge's edges + BYO-cluster direction is the only native story here.
5. **Pricing rage is universal** (Lovable credit burn on failed fixes, v0
   billing failed generations, Replit's effort-based $1,000-weeks). Two lessons
   to bake in: never bill the user for the model repairing its own mistakes
   without visibility, and copy Replit's one good idea — the wizard/plan phase
   is cheap or free; metering starts at approved build work.

Honest headwind: incumbents win on 60-seconds-to-wow from one sentence. The
wizard must not feel like a form — see §5: it is one free-text box plus at most
one screen of generated questions with recommended answers, skippable, and the
sandbox provisions concurrently with the final wizard step.

### 1.3 Go framework verdict

- **Eino (cloudwego)**: 12.5k stars, weekly releases, production-proven at
  ByteDance. The only Go framework where interrupt/resume + CheckPointStore and
  automatic stream handling are mature shipped features. First-party `claude`
  and `openai` model components. Costs: pre-1.0 churn, docs Chinese-first,
  heavy optional abstraction (which we simply don't use).
- google/adk-go: strong newcomer (v2.1.0), re-evaluate in 6–12 months; today
  its multi-provider and checkpoint stories are younger.
- langchaingo: stale (last push Jan 2026) — no. genkit-go: Anthropic via
  OpenAI-compat shim + Google gravity — no.
- Raw Anthropic/OpenAI SDKs + own loop: lightest deps but rebuilds
  checkpointing; not worth it given agents' engine already exists on Eino.

**Decision: keep Eino, but consume it the way the agents provider does** — raw
`BaseChatModel` + `schema.Message`, own ~500-LOC loop, own 140-LOC durable
interrupt checkpoint. Both kedge harnesses converge on one dependency and one
engine idiom. The `adk`/`deep`/middleware stack that app-studio uses is where
its 19.6k LOC came from; none of it is required for this product. MCP surfaces
(serving `/mcp`) follow the existing provider pattern; prefer the official
`modelcontextprotocol/go-sdk` (v1.x) for new code.

## 2. What app-studio taught us (autopsy)

Measured on 2026-08-01:

- 39,096 non-test Go LOC; `api/assistant_*.go` alone is **19,617** (~50%).
- The heavyweight half bought: an 8-phase state machine whose phase is
  **re-derived by string-matching the transcript** (known defect), a turn
  classifier that costs an extra LLM call per turn with a 60-keyword fallback,
  three overlapping run-exclusivity layers, a 1,530-LOC Eino checkpoint
  serializer that only pays off at interrupts it doesn't durably cover, a
  947-LOC browser-console capability signer for a loop the improvement plan
  admits still isn't closed, and a ~2,200-LOC audit/disclosure pipeline.
- The portal's `App.vue` is a single 5,100-LOC file; `node_modules` is in-tree.
- Meanwhile the parts that deliver the actual product are small and good:
  scaffold hydration (405), build-config generation (342), promotion (390),
  data-plane client (156), lifecycle checkpoints (247), workspace FileStore
  with snapshot/undo (~1,400). The agents provider proves the harness itself
  needs ~840 LOC, not 19,617.

Live bugs vibe-studio must not re-inherit: promote can ship an untethered image
digest; `delete_file` never propagates to git (commit bundle carries only
path+content); background work holding a caller's bearer for multi-minute runs;
single-replica-only due to no durable run lease; four list-response shapes and
`err.Error()` leaked into status messages.

## 3. Product flow

```
┌─────────┐   ┌──────────────┐   ┌───────────┐   ┌──────────────┐   ┌─────────┐
│ Intake  │ → │ Blueprint    │ → │ Provision │ → │ Studio       │ → │ Ship    │
│ 1 text  │   │ template rec │   │ sandbox + │   │ vibe loop +  │   │ build → │
│ box +   │   │ + questions  │   │ scaffold  │   │ live preview │   │ promote │
│ ≤3 Q    │   │ + approval   │   │ + repo    │   │ + sync       │   │ to prod │
│ rounds  │   │              │   │ (parallel)│   │              │   │         │
└─────────┘   └──────────────┘   └───────────┘   └──────────────┘   └─────────┘
```

1. **Intake.** One free-form box: "What do you want to build?" The model gets
   the live template catalog (`list_templates` + `describe_template`) and must
   respond with a single structured `propose_blueprint` tool call: app summary,
   recommended template + why, key assumptions, and 0–3 clarifying questions
   (each 2–5 options, exactly one marked recommended; host renders the form and
   appends the free-text escape hatch). Questions only when decision-blocking
   (template choice, data model, integrations) — never "any edge cases?".
   Hard cap: 3 propose iterations, then force the blueprint to review.
2. **Blueprint review.** A card, not a chat message: name, template, components,
   integrations to connect (GitHub connection, secrets), what the wizard will
   provision, success criteria. Buttons: **Create app** / **Adjust**. This is
   the billing meter's starting line.
3. **Provision.** On approval, concurrently: create `Project`, create/adopt the
   repo (code provider), provision the template with `kedgeMode: development`,
   hydrate the workspace from the scaffold tag, run the initial commit, write
   `.kedge/build.json` + the Railpack workflow. The portal shows the four
   lifecycle checkpoints (template / git / ci / production) filling in.
4. **Studio.** Chat + file tools + `dev_sync` + preview pane (instance
   `status.url`) + logs/verify tools. Undo = workspace snapshot restore.
5. **Ship.** `check_build` waits for digests; **Promote** creates/updates the
   artifact-mode prod environment (same template, `kedgeMode: production`,
   pinned digests). Promotion is repeatable; dev keeps running.

The wizard is skippable for experts: "I know what I want" jumps straight to a
template picker (rendered from `Template.spec.schema` via the existing
DynamicForm pattern) and an empty-or-scaffold choice.

## 4. Architecture

### 4.1 Provider shape

Standard kedge provider (quickstart pattern): own Go module
`github.com/faroshq/provider-vibe-studio`, subcommands `init | serve`, one port
serving portal element `kedge-provider-vibe-studio`, `/api/*`, `/mcp`,
`/healthz`; `sdkinstall.Bootstrap` init; CatalogEntry with permission claims on
infrastructure (templates, instances, dataplane), code (repositories, commits,
checkouts, packages), and its own APIExport.

State stores, mirroring the agents decision ("runs are not CRDs so schema and
execution reality cannot drift"):

- **kcp**: one CRD, `Project` (`vibe.kedge.faros.sh/v1alpha1`) — a trimmed copy
  of app-studio's `Project` (repository binding, template name, environments +
  bindings, sharing intent). No message/run types in kube.
- **Postgres**: `sessions`, `session_events` (append-only, ordinal-keyed),
  `runs`, `approval_preferences`. No envelope encryption in v1 (dropped 980
  LOC; revisit if a compliance requirement actually lands).
- **PVC FileStore**: reuse app-studio's `workspace/` package (symlink-safe,
  atomic writes, revision-CAS snapshots, retention sweeper) as-is.

No runtime kubeconfig, ever. All runtime access via the infra provider's
dataplane verbs with the caller's forwarded bearer (`dataplane_client.go`
pattern, 156 LOC). All kube access through the hub as the calling user.

### 4.2 Event-sourced sessions (the codex/kimchi lesson)

The single internal contract is a submission/event pair, in Go just two
channels and a switch:

- `Submission{ID, Op}` — Ops: `UserInput`, `WizardAnswer`, `ApproveBlueprint`,
  `ApprovalDecision`, `Interrupt`, `Undo`, `Promote`.
- `Event{SubmissionID, Ordinal, Msg}` — every long-running tool emits the
  uniform **begin / delta / end** triple; approvals are events out +
  correlated Ops back in (pending approvals = `map[id]chan Decision`), never
  callbacks — headless/API/replay fall out for free.

Events append to `session_events` with ordinals; a dedicated writer goroutine
(never blocks the loop). The portal consumes an SSE projection of a **narrow,
stable external event vocabulary** (session.started, turn.started/completed,
item.started/delta/completed, approval.requested, error) — the wide internal
enum stays internal. Resume = fold events; crash-safety comes from the log, not
from serializing framework state (this replaces app-studio's 1,530-LOC Eino
checkpoint serializer; the only durable checkpoint is the agents-style ~140-LOC
interrupt record for pending approvals).

### 4.3 Harness

The agents engine, lifted: raw Eino `BaseChatModel`, `StreamTurn` /
`ResumeTurnWithTools`, tool loop bounded by max-tool-turns, one tool wrapper
doing approval gating + event emission. Budget ~1.5k LOC including tools
plumbing. Explicitly **no**: adk/deep agents, summarization middleware (use
simple oldest-first truncation with a token budget in v1), toolsearch
middleware (tool count is ~25), turn profiles, LLM turn routing.

**Phase = tool visibility, enforced by the host.** One declarative catalog
(kimchi's tool-catalog pattern):

| Phase | Tools visible |
|---|---|
| `intake` | `list_templates`, `describe_template`, `propose_blueprint` |
| `studio` | files (read/list/search/write/apply_patch/delete/mkdir), `sync_dev`, `get_runtime_status`, `get_runtime_logs`, `restart_runtime`, `set_runtime_env`, `verify_runtime`, `get_preview_url`, `commit_files`, `check_build`, `get_build_logs`, `rebuild`, `get_checkpoints`, `ask_user` |
| `ship` (a studio sub-mode, not a separate loop) | + `promote` |

During intake the model *cannot* write files — not because a prompt asks
nicely, but because the tools aren't in the request. Phase transitions are host
decisions (blueprint approved → studio), recorded as events; the model never
infers its phase from the transcript.

Two pure functions own control flow (kimchi's two-layer machine, both
unit-testable with zero mocks):

- `NextAction(state) → Action` — priority-ordered; its output is appended to
  every tool result as a `Next action:` line.
- `Apply(state, Command) → (state, error)` — all transitions; time/uuid
  injected; no I/O.

Model config: per-tenant LLM settings as today (Anthropic default, OpenAI via
Eino's component). `ask_user` follows kimchi's routing idea: interactive
sessions render a form; MCP/API sessions get an auto-answer policy.

### 4.4 Reused from app-studio (near-verbatim, ~2.5k LOC)

`dataplane_client.go`, `project_checkpoints.go` (the 4-checkpoint model with
auto/manual remediation is the product's progress UI), `project_scaffold.go`,
`project_build_config.go` (Railpack workflow gen), `project_promote.go`,
`project_hydrate.go`, `preview_edge.go`, `deployment_defaults.go`, the
workspacePath→component routing core of `development_sync.go`, `workspace/`
FileStore + snapshots, provider skeleton files. Fix on the way in: `delete_file`
must carry deletions into the commit bundle (code provider `commit_files`
already accepts them or gets a `deletePaths` field); promotion must verify the
digest is reachable from the committed tree before flipping the environment.

### 4.5 Dropped from app-studio (with their LOC)

Phase state machine (2,307), turn profiles + LLM router (522+597), work items /
execution authority / plan grants / tombstones (~3,500 + store), Eino
checkpoint serialization (1,530), preview-console capability signer + Vite shim
(~1,300), audit/action-feed/tool-summary disclosure pipeline (~2,200), store
envelope encryption (980), the federated-MCP allowlist client (vibe-studio
calls infra/code through typed clients like agents does, not through a
hand-rolled MCP JSON-RPC client).

Browser-console capture (the one dropped feature with real value) returns later
as a Template-declared dataplane verb on the infra side, not as a vibe-studio
subsystem — that's the correct home per the dataplane-decoupling direction.

### 4.6 Portal

Small Vue 3 app, portalkit-vue components, **no in-tree node_modules**, files
capped by review at ~400 LOC. Views: `WizardView` (intake box → generated
question form → blueprint card), `StudioView` (chat + preview iframe + tabs:
activity/logs/environments), `ShipPanel` (checkpoints, build status, promote).
The SSE client speaks only the narrow external vocabulary. Wizard question
rendering reuses the DynamicForm approach (schema-driven), so the model's
proposed questions and a Template's own input schema render through one
component.

### 4.7 Identity and multi-replica

- Interactive path: caller's bearer, as today.
- Anything that outlives a request (build watch, promote watch): per-project
  ServiceAccount (agents' `agentidentity.go` pattern) — never a stored user
  bearer.
- Runs claimed via optimistic CAS on the Postgres run row (agents' scheduler
  pattern) → multi-replica safe from day one; no chart-pinned replica 1.

## 5. Wizard specification (condensed)

Prompt-side rules (distilled from kimchi's ferment scoping, which shipped and
converged):

- Ask only decision-blocking questions; never ask what a safe reversible
  default answers. "Create a TODO app" gets zero questions and sensible
  defaults, not a stack interrogation.
- ≤3 questions per round, ≤2 rounds expected, hard stop at 3 propose
  iterations. Options are single concrete choices, no "X or Y" labels, exactly
  one recommended; the host renders ★ and the custom-answer row.
- The model emits payloads; the host owns all UI and all state transitions. The
  model never restates questions or the blueprint in chat.
- Template recommendation must name a template that exists in the tenant's
  catalog *right now* (the tool result carries the catalog; hallucinated names
  fail validation and retry).

Blueprint schema (the `propose_blueprint` tool):

```json
{
  "title": "...", "summary": "one sentence",
  "template": {"name": "application", "reason": "..."},
  "values": {"...": "template input values derived from answers"},
  "integrations": [{"kind": "github", "status": "connected|needed"}],
  "assumptions": ["..."],
  "success_criteria": ["user-visible, testable"],
  "questions": [ { "id": "q1", "text": "...", "options": [
      {"label": "...", "recommended": true}, {"label": "..."} ] } ]
}
```

`questions` non-empty → host renders the form, answers come back as one
`WizardAnswer` op with "answers override recommendations, re-emit once with
questions: []". `questions` empty → blueprint card → approval starts provision.

## 6. Delivery plan

Phases ship independently; each ends runnable behind the provider's own enable
gate. app-studio stays untouched and running throughout.

- **Phase 0 — skeleton + contract (small). ✅ DONE 2026-08-01.** Provider
  module (`providers/vibe-studio/`), `Project` CRD, chart, catalog entry,
  Postgres + memory stores, event log + SSE projection, `NextAction`/`Apply`
  state machines with tests. No LLM yet: the canned `ScriptedEngine` drives
  the event contract end-to-end (verified: full wizard flow over HTTP against
  the real binary), and the portal element renders the whole flow.
- **Phase 1 — wizard (medium). ✅ DONE 2026-08-01.** Eino engine ported from
  the agents provider (`engine/`), `session.Engine` takes a `TurnContext`
  (tenant/cluster/token/session + delta and activity sinks). Intake drives a
  forced `propose_blueprint` tool validated against the live Template
  catalog; unconfigured tenants fall back to `ScriptedEngine`, which now
  states *why* rather than echoing. Per-tenant model config, then per-project:
  see the Models section below.
- **Phase 2 — studio (large). ✅ DONE 2026-08-01.** Approve provisions for
  real: Project CR → reconciler-created instance → scaffold fetched from the
  template's tag-locked repo → routed into the sandbox over the infra data
  plane → preview URL sourced from `Project.status` (reconciler truth).
  Studio turns carry file tools (list/read/write/delete over a
  `vibe_workspace_files` table, each write hot-syncing to the sandbox) plus
  logs/restart. Streaming deltas, a durable tool-activity trail
  (`turn.activity` events → the build ledger), resumable provisioning, and
  orphaned-turn recovery. Portal rebuilt: full-bleed two-pane studio
  (conversation | Preview/Code/Status), CodeMirror editor with save→sync,
  KRM-driven project listing, session/project delete.
  Git: the Project reconciler creates the code-provider `Repository`
  (autoInit) and the provision flow seeds it once with the scaffold through
  the code provider's `commit_files` MCP tool (bundle contents are
  code-provider-local, so CR-only seeding is impossible).
- **Phase 3 — ship (medium). 🟡 IN PROGRESS.** Commit-from-workspace ✅,
  promote ✅ (see 6c), build ⬜ (the `ci` checkpoint stays `pending` until it
  lands, and promotion currently takes image references from the user rather
  than from a build).
  Exit criterion: prod URL serving the built digest; repeat-promotion works.

  **No build-config generator is needed** (2026-08-02). Every scaffold already
  ships `.github/workflows/build.yaml`: smoke test, then one Railpack image
  per component pushed to `ghcr.io/<owner>/<repo>/<component>` tagged
  `sha-<commit>` (component names match the template's, per the ONE NAME
  RULE). vibe-studio was *dropping* it — `scaffoldSkippedPath` filtered
  `.github/`, so seeded projects had no CI at all. Keeping it means the
  pipeline arrives with the scaffold and the commit reconciler's pushes to the
  default branch trigger it; app-studio's YAML generator does not get ported.
  Requires the `workflow` OAuth scope on the Code connection — without it the
  host rejects the *whole* commit, so the git checkpoint explains that in
  those words (`explainGitError`).

  What remains for build: resolve each component's digest from the Code
  provider's `Package.status.versions[]` by the tag `sha-<committedRevision>`
  into `Project.status.build`, mirror run state into the `ci` checkpoint via
  `RepositoryBuildStatus` (including the failure-log tail, so the model can
  fix its own build), and prefill/enforce promote from those digests. One more
  gap: Actions-published ghcr packages are private, so production needs an
  imagePullSecret minted from the connection token (app-studio's
  cross-provider bridge pattern) before a promoted image will pull.
- **Phase 4 — hardening + surfaces (medium). ⬜.** MCP surface (`vibe__*`
  tools so agents/CLI can drive the same flow — the Session CR makes a CLI
  attach trivial), approval preferences, retention, multi-replica soak,
  expert fast-path (skip wizard), template-picker page.
- **Phase 5 — migration/retirement. ⬜.** Read-only importer for app-studio
  Projects (same logical shape), portal switch of the default builder entry
  point, app-studio marked deprecated in its CatalogEntry, removal in a later
  cleanup once tenants are off it.

### 6a. Landed beyond the original plan

- **The first build turn starts itself** (2026-08-02). Entering studio used to
  leave the session idle: the sandbox served the scaffold's hello-world until
  the user typed something like "build it" — asking them to state an intent
  the wizard had already collected and they had already approved.
  `CmdProvisionCompleted` now also emits the opening instruction (title,
  summary, success criteria, "edit the scaffold, don't re-bootstrap") as a
  user message, so `NextAction` returns `run-studio-turn` and the normal turn
  machinery takes it. Emitting an event rather than special-casing the engine
  keeps it in the transcript and replayable. The session view kicks owed turns
  (single-flighted), because only the API process runs the engine while
  provisioning finishes in the reconciler — so the first turn starts when
  someone has the studio open, which is when it was created.

- **Models are CRs** (`Model` + `Session.spec.modelRef`): keys in Secrets,
  never in the CR; a Models menu configures them, and each project picks its
  model from a picker, changeable mid-project. Resolution: session's model →
  workspace default (annotation) → only model → legacy single Secret
  (`kedge-projects-llm`, so app-studio-configured workspaces keep working).
- **ONE NAME RULE for template components** (2026-08-01). Component name ==
  its workspace directory, enforced by CEL on
  `Template.spec.development.components` (`.` allowed for a single root
  component). `application`'s components renamed `frontend|backend` →
  `web|api`, cascading through graph resource ids, schema inputs
  (`webImage`/`apiPort`/…), status fields, data-plane keys, and object names;
  its agent guidance now teaches the vocabulary so every consumer inherits
  one description. vibe-studio fails loudly when a scaffold's layout matches
  no component. Rationale: the name/path duality caused repeated bugs (sync
  routing, then `get_logs api` vs component `backend`). **Breaking**:
  existing instances and `.kedge/build.json` must be recreated.

Non-goals for v1: multi-user collaboration on one session, browser-console
capture (returns as an infra dataplane verb), per-message billing/metering
(but every event carries token usage so metering can be added without
re-architecture), mobile targets. (BYO model keys is no longer a non-goal —
it shipped as the `Model` CRD + Models menu.)

Known gaps worth naming: studio edits never reach git after the seed commit
(Phase 3); the workspace is per-session, so a new session does not inherit an
existing project's files (reopen the project's session to iterate); file
tools write whole files with no patch/diff tool; there is no stop button for
a running turn (needs a cancel op); real VS Code in the browser would mean
running openvscode-server as a template component behind the data-plane
proxy rather than bundling Monaco in the micro-frontend.

## 6b. KRM-first revision (2026-08-01)

Review outcome after the first live phases: the control plane must be fully
kube-native — every lifecycle-bearing thing is a CR with spec/status and a
reconciler; `kubectl get` tells the whole story. The event log does not move:
**control plane in CRs, conversation data plane in the store** (token-rate
appends and message bodies would melt etcd revisions; same reasoning as the
agents provider's "runs are not CRDs" and Argo/Tekton keeping logs out of CRs).

Object model:

```
Session (vibe.kedge.faros.sh, cluster-scoped)         ← NEW: the root object
  spec:  intent, projectRef{name}, paused
  status: phase (Intake|Review|Provisioning|Studio), checkpoints[],
          activeTurn{id,startedAt}, messageCount, lastOrdinal, conditions
  printer: PHASE, PROJECT, MESSAGES, AGE

Project (existing)
  spec:  displayName, template, repository binding, environments[].bindings[]
  spec.sessionRef{name}                                ← replaces the label
  metadata.ownerReferences: [Session]                  ← delete session ⇒ GC project
  status: phase, repository{phase,url}, environments (reconciler-mirrored)

Instances / Repository: owned via labels as today; Repository is never
GC'd (holds user code) — deliberate exception, documented on the field.
```

Reconciler responsibilities (all in the existing multicluster manager):

- **Session reconciler** (new): mirrors store-derived state (phase,
  checkpoints, counts, active turn) into Session.status — the store stays
  authoritative for the conversation, the CR is its control-plane projection.
  Owns provisioning convergence (today's HTTP `runProvision` moves here):
  project/instance/repo waits, scaffold sync, seed commit.
- **Project reconciler** (existing): instances + repository + status mirror.

Credentials: reconciler steps that today ride the caller's bearer (template
read, dataplane sync, seed commit via aggregate MCP) switch to a **per-project
ServiceAccount** minted at approve time (the agents provider's agentidentity
pattern; needs serviceaccounts/secrets/rbac claims). The caller's bearer is
then used exactly once — at approve, to create the Session/Project pair and
the SA — and never captured for background work again.

The HTTP API shrinks to: submissions → events + Session/Project spec writes
as the caller; views → fold(store) + read CR status. The portal's home page
reads Projects+Sessions (already true for Projects).

Migration steps: (A) Session CRD + status-mirror reconciler + sessionRef/
ownerRef wiring; (B) move provisioning into the Session reconciler behind the
per-project SA; (C) delete the HTTP-side provision/preview/seed backfills.

**Status (2026-08-02): (A) ✅ done, (B) ✅ done, (C) ⬜ outstanding.**

(A) landed as `Session` (cluster-scoped, `vsess`): `spec.intent`,
`spec.projectRef`, `spec.modelRef`; `status` mirrors phase / active turn /
last ordinal / checkpoints, refreshed by the Session reconciler every 30s.
Its `vibe.kedge.faros.sh/purge` finalizer **purges the store** (events,
workspace files, listing row — keyed by session id + the tenant annotation
that bridges the CR and store keyspaces), and `Project` carries an
ownerReference to it, so `kubectl delete session X` cascades:
project → instances → sandbox, with the repository deliberately surviving.
`kubectl get sessions.vibe` now tells the conversation's whole story, and the
UI's delete actions go through the same CR deletion — no UI-only path.

(B) landed: provisioning convergence runs in the Session reconciler under a
per-session ServiceAccount (`kedge-vibe-<hash>`, minted with its ClusterRole,
binding, and legacy token Secret as children of the Session — kcp has no
TokenRequest). The caller's bearer is now used exactly once, at approve, to
write the Session/Project pair. Git commits converge on the same loop: the
reconciler compares `status.workspaceRevision` against
`status.committedRevision` and commits whatever the last turn changed, with a
message derived from the events since `committedOrdinal`.

(C) is what is left — the HTTP layer still carries backfill paths that the
reconciler now duplicates.

## 6c. Promotion (2026-08-02)

Promotion is a **spec write**, and that is the whole design. The Project
reconciler already converges every binding in every environment, so appending
a `production` environment to `Project.spec.environments` — same template,
`kedgeMode: production`, images pinned into the inputs the template declares —
is enough to get a production instance created, updated, and torn down by
exactly the machinery that runs the development sandbox. There is no
promotion controller, no promotion pipeline, and re-promoting a newer image is
another write to the same environment.

What makes this work is the **development contract on the Project spec**:
`spec.development.components[].imageInput` records which template input each
component's image belongs in (copied from the Template at approve time, so no
reconciler ever has to read Templates — they ride virtual storage with a
separate identity). Promote reads that contract, so it stays template-agnostic.

Two guards, both deliberate:

- **The digest tether.** Promotion refuses while
  `status.workspaceRevision != status.committedRevision`, and refuses when no
  commit exists at all. Production runs a git revision — the promoted
  environment records it in `spec.environments[].revision` and mirrors it into
  status — so what is running is always traceable to a tree. This is the
  app-studio bug (§2) that vibe-studio does not inherit.
- **No partial ships.** A component with an `imageInput` and no image fails
  the whole promotion with the component named, rather than deploying half an
  app on scaffold defaults.

The development environment keeps running throughout: production is a second
environment, not a mode flip, and the two instances get distinct names
(`<project>-prod`) and addresses. The Session reconciler mirrors the
production environment's status into the `production` checkpoint, so the
Status tab reports "live on 2613af6 — https://…" without the API server
watching anything.

The remaining gap is where the image references come from: until build lands
they are typed into the ship panel. The API shape does not change when builds
arrive — the build status simply fills the same map.

## 7. Risks

- **Wizard friction vs time-to-wow.** Mitigations: zero-question path for
  simple prompts, provision starts concurrently with blueprint approval
  rendering, skippable wizard. Measure: time from first keystroke to preview.
- **Eino pre-1.0 churn (v0.9 → v0.10 alpha).** Surface area is deliberately
  tiny (BaseChatModel + streams); pin versions; the agents provider co-moves.
- **Template catalog breadth.** The wizard is only as good as the catalog; a
  thin catalog makes recommendations feel canned. Near-term: application,
  simple-webapp, worker, database, redis-cache, cron-job cover the wizard's
  vocabulary; the marketplace/edges direction widens it.
- **Two builders during transition.** Bounded by Phase 5; no shared state
  between the providers, so no coupling risk — only portal-entry confusion,
  handled by feature-flagging the default entry point.
