# Agents backed by a harness: the edge is the harness provider

Status: **Implemented (2026-09-27), phases 1-5.**
Written 2026-09-26. The "Where we are" section describes the code as it was
BEFORE this work started and is kept as the rationale; the list below tracks what
landed.

**Phase 1 — protocol and shared client.** `runner/v1` grew `sessionID`,
`workspaceID` and a required `harnessCredential` on start and resume;
`repositoryID`/`baseCommit` became optional, and a workspace attempt runs in a
directory the runner keeps across turns with no Git at all. Both adapters take
the credential per launch — Claude Code as its one environment variable, Codex as
an `auth.json` written into the home for one launch and removed after. Neither
adapter has a credential in its configuration any more. `pkg/runner/client` is
the shared typed client, ported out of Factory with every transport and identity
check preserved, plus typed protocol errors, a discovery cache and a streaming
SSE `EventStream`. A conversational caller maps session to `taskID`, run to
`attemptID` and turn number to `attemptEpoch`, because consecutive turns of one
task must advance the epoch.

**Phase 2 — the edge is the harness provider.** `spec.harness` on the host edge
kinds, default `auto`, applied live by the agent and cached on disk for an
offline start; install flags only seed it. The agent supervises one runner per
enabled harness on an allocated loopback port, injects the runner bearer itself
so the credential never enters the workspace, and advertises each runner over
the existing discovery channel. The edges provider publishes it as a `Service`
of the new type `runner` named `<edge>-<selector>`, and stamps `status.harness`
from the capabilities response. The `Addon` kind, its controller, the
`addon-credentials` verb and the whole host-side credential path are deleted.

**Phase 3 — one lifecycle behind a `Backend` seam.** `runTurn` serves a fresh
turn and a resume, differing only by a continuation value; the duplicated resume
lifecycle is gone along with seven divergences it had accumulated, and
`executor.InProcess` is deleted because the Run object is the queue.

**Phase 4 — `spec.backend` and the harness backend.** `ModelCredential` gained
the `claude-code` and `codex` harness identities, `spec.models` moved under
`spec.backend.model`, and `backend/harness` dispatches a turn as a workspace
attempt and tails its events. The epoch is claimed durably from the store before
dispatch, so two replicas answering one message cannot collide. A harness-backed
agent does not compact: a harness owns its own session.

Bugs found and fixed along the way, each pre-existing:

- `verifyTaskPath` and `artifactSource` compared a resolved path against an
  unresolved root, so any runner whose state directory sat under a symlink
  (macOS `/var`, a mounted data volume) refused every attempt and every artifact.
- The MCPServer role dropped `addons` wholesale; the same privilege now lives on
  the edge spec, so the denial became verb-aware — an MCP token reads edges and
  never writes them.
- The shared client turned any failed response carrying a protocol code into a
  typed protocol error, discarding the status. A 401 or 403 whose body said
  `{"code":"unavailable","message":"attempt not found"}` therefore read as the
  one answer that permits a first start, so a refusal from any hop in between
  could have caused a second execution of work already running. Authorization
  statuses are now always an HTTP error.
- Deleting the worker pool removed the only ceiling on concurrent in-process
  runs. A non-blocking slot ceiling replaced it, which hands a run back to the
  queue rather than refusing it the way the old 64-slot channel's 503 did.
- The engine applied the caller's approval verdict to the first tool call of a
  RECOVERY resume, whose checkpoint has no pending call, so a recovered run's
  next tool call was answered "the user denied this".
- The agents portal wrote `spec.models` and `spec.modelFallbacks` as CR fields
  after they moved, so the apiserver pruned them: an agent created or edited
  from the portal saved cleanly, looked configured, and could not run. The patch
  path was worse than the create path, because re-picking the credential and
  saving changed nothing at all, so the obvious remedy could never work.

**Phase 5 — the portals.** The edge page gained a Harness card that renders
`status.harnesses` and writes `spec.harness`, where ticking every harness saves
`auto` rather than a pinned list that would never pick up a later install. The
agent editor gained a Backend section: the harness is never chosen there, it is
derived from the credential's provider, and the two credential pickers filter to
chat endpoints and harness identities respectively. Readiness comes only from
`status`, with an unobserved backend reading as unobserved rather than as ready,
and tool grants are replaced by an explanation for a harness-backed agent
because the API refuses them. The run views say where a turn executed: the runs
API grew `backend` on every summary, plus the runner attempt and harness session
on a detail, and Activity marks only the rows that recorded a harness so an
all-model workspace looks unchanged. A run that recorded no backend is left
alone rather than labelled `model`, because that is the API's default and not
something the row observed.

## A trap worth knowing about

`gofmt`'s doc-comment printer rewrites two adjacent apostrophes into a
typographic close-quote. A kubebuilder CEL marker that compares against an empty
string literal (`self.x != ''`) is therefore corrupted into a CEL syntax error by
whoever next runs the formatter, and the apiserver then rejects the whole CRD.
Reproduced on stock Go 1.26.2. Use `size(self.x) > 0` instead; the rule on
`ModelCredentialSpec` carries a comment saying so.

Two related notes for anyone changing an API here. `make codegen-*-provider`
refuses to rewrite an APIResourceSchema whose content changed while its
generated name did not, because the name derives from HEAD and kcp schemas are
immutable — so an API change needs a commit before codegen will name the new
schema. Deleting the schema file makes apigen write it fresh under the current
name, which is the right move only while the change is still uncommitted.

Scope: `providers/agents`, `providers/edges` (`Service`, the tunnel, the
discovery loop), `pkg/agent`, `pkg/runner`, and
`railgrid/providers/providers/factory`.

## Summary

An `Agent` today is always a hub-side Eino chat loop against an
OpenAI-compatible endpoint. The machinery to run Claude Code or Codex on an
edge already exists end to end, but it is gated behind a tenant-declared
`Addon` object, a second credential path, and a client private to Factory.
Nothing connects it to agents.

The proposal has three parts, each a simplification of something that
exists:

1. **The edge is the harness provider.** Which harnesses a machine offers
   is `spec.harness` on the edge object, default `auto` (detect what is
   installed), switchable to `none` from the edge UI at any time and
   applied live by the agent. The agent supervises the runner
   and advertises it like any other local service; the edges provider's
   existing discovery loop materialises it as a `Service` of type `runner`.
   The `Addon` kind and everything that exists only to serve it go away.
2. **The Agent brings the credential.** The tenant's `Agent` names a
   `ModelCredential` whose provider is `claude-code` or `codex`; the agents
   provider sends it with each attempt as dispatch data, the way Factory
   already sends a clone token. No harness credential is ever configured on
   the edge, materialised on disk by the edge agent, or read through a
   provider verb.
3. **One backend seam.** `Agent.spec.backend` is either a `model` (today's
   in-process loop) or a `harness` (an edge with a ready runner). The
   agents provider owns runs, transcripts, budgets, channels and approvals
   in both cases; the backend owns the turn.

No new kinds. One kind and one verb deleted. Three optional fields on the
runner protocol. One shared client.

## Where we are (2026-09-26)

### The three execution stacks

| Stack | Where the turn runs | Model access | Consumer |
|---|---|---|---|
| agents provider | in-process, `engine/engine.go:334` (Eino) | `ModelCredential`, OpenAI Chat Completions only (`llm/profiles.go:184-213`) | portal, channels, MCP `run_agent`, schedules, triggers |
| edge runner | `railgrid runner run` on a Linux/macOS edge, one `claude`/`codex` process per attempt (`pkg/runner/harness/{claude,codex}`) | credential materialised by the edge agent from a tenant Secret (`pkg/agent/addons/runner.go:425-535`) | Factory only |
| app-studio assistant | in-process Eino | its own | app-studio |

Three independent "run a model and stream turns" loops in one monorepo.
The agents provider has **no** notion of a harness: `AgentSpec` has no
backend field (`spec.runner` was deleted in the Phase 4 cleanup), the only
vendor switch is `llm.BuildModel`, and the word "claude" appears only in
comments. The original design (`docs/agents-provider-architecture.md`
§"Runner") planned a `claude-code` runner in a pod with a PVC; that never
shipped.

### What the edge side already gives us

Read `docs/edge-addons.md` and `docs/edges-agent-credentials.md` for the
full picture. The parts that matter here:

- The agent binary can supervise `railgrid runner run` as a non-root child
  with an environment built from scratch (`pkg/agent/addons/supervisor`),
  probe it, and report its harness name, version and readiness.
- The agent already advertises local services over the tunnel
  (`/api/v1/services`), and `servicectrl` materialises each one as a
  `Service` named `<edge>-<type>` with `Detected=True`
  (`internal/servicectrl/discovery_reconciler.go:82-205`). Declared and
  discovered Services coexist; discovery never removes a declared one.
- Any hub-side caller reaches a Service at
  `/clusters/{cluster}/apis/edges.railgrid.ai/v1alpha1/services/{name}/proxy/...`
  and the proxy injects the Service's credential (`service_proxy.go:339`).
- `runner/v1` is a durable attempt protocol: receipts, epochs, idempotent
  mutations, SSE events with cursors, `needs_input` clarifications, resume
  into the same session, artifacts (`pkg/runner/protocol.go`). The Claude
  adapter already takes its credential per process from a file and injects
  it into the child alone, redacting it everywhere else
  (`harness/claude/credential.go`).

### What is in the way

**1. The runner is an `Addon`, and an `Addon` is a lot of machinery.** A
tenant declares the object, the machine owner opts in with
`--allow-addon=runner --addon-user`, the agent materialises token, config,
harness home and credential under `~/.railgrid/addons/runner/<name>/`, two
halves of a gated `addon-credentials` verb move Secrets in both directions,
and `addonctrl` derives a `Service` once both agent-owned conditions and a
provider-readable token Secret line up. That is one kind, one controller,
one verb, five CEL rules, a 888-line agent package and 638 lines of docs
for what is, from the hub's point of view, "a service on port 8787 that
speaks `runner/v1`". The discovery loop already models exactly that for
seventeen other service types.

**2. The harness credential is bound to the machine, not the caller.**
`spec.runner.{codex,claude}.authSecretRef` makes the runner a single
billable identity shared by every workload that reaches it, and forces the
edge agent to hold and rotate a tenant credential on disk. Factory's
"no credential ever reaches the harness except a scoped clone token"
discipline stops at the model key.

**3. The runner protocol is coding-shaped.** `Start` refuses a request
without `repositoryID` and a 40-character `baseCommit`
(`pkg/runner/runner.go:1229`), and a new attempt always starts a fresh
harness session: `Launch.SessionID` is only ever the receipt's own session
on resume (`runner.go:701`). A conversational agent needs "next turn in
the same session, no repository".

**4. The only client is private to Factory.** `internal/runnerclient` and
`internal/edgesroute` in the factory repo are the sole implementation of
"reach a runner through the Service proxy". The protocol types live in
railgrid `pkg/runner`, the client does not.

**5. The agents provider duplicates its own lifecycle.** `executeTask`
(`api/run.go:223`) and `resumeRun` (`api/resume.go:118-215`) are two copies
of model build, toolset build, checkpointing, cost and delivery, already
diverging. Four run starters build the 25-field `taskRun`. `store.Run` and
`v1alpha1.Run` are two run models joined by a 407-line projection. The
`executor.InProcess` channel is a second, lossy queue in front of the Run
object. Adding a backend branch on top of this without first collapsing it
would double the surface again.

**6. One contract, four copies.** The Addon shape is declared in
`types_addon.go`, mirrored as unstructured structs in
`pkg/agent/addons/addon.go`, and its constants repeated in `addonctrl` and
`addon_credentials.go`. The data-plane verb list is declared in
`manifest.yaml`, `tunnel/grammar.go:107`, `subresources.go` and
`agentidentity.go`, and has drifted: `runner-auth` and `runner-token` are
granted (`agentidentity.go:95`) but no longer served.

**7. Factory is two-thirds workflow.** Only `edgesroute`, `runnerclient`,
`scheduler/{attempts,workers,transport}` and
`apis/{common,types_worker,types_attempt}` are "run a harness remotely";
the rest is ticket-to-PR product flow with its own exactly-once dispatch
engine. None of it should be copied into agents.

## Target architecture

```
  machine owner                  tenant                         hub
  ─────────────                  ──────                         ───
  LinuxServer build-01           Agent{backend: harness,        agents provider
    spec.harness.mode: auto        edgeRef: build-01,             │ run lifecycle (queue, transcript,
    (edge UI switch; agent         credentialRef: my-claude}      │ budget, inbox, channels)
     applies live, caches on disk)                                ▼
        │                                                    Backend.Turn()
        ▼                                                         │
  agent supervises runner ──advertises──▶ Service build-01-claude │
  (loopback :8787, bearer)   (discovery)   type: runner            │
        ▲                                                         │
        └──── tunnel ◀── services/build-01-claude/proxy/runner/v1 ◀┘
                          StartRequest{sessionID, workspaceID,
                                       harnessCredential (dispatch data)}
```

### 1. The edge is the harness provider

**The setting lives on the edge object.** `LinuxServerSpec` and
`MacOSServerSpec` gain

```yaml
spec:
  harness:
    mode: auto          # auto | none | explicit; CRD default auto
    enabled: [claude]   # explicit only
```

`auto` enables every harness whose binary `harness.ResolveBinary` finds on
the runner account. `none` disables the runner entirely. `explicit` pins
the set. Because the default is `auto`, every new edge and every existing
edge on upgrade offers whatever harness is on the machine with no extra
step. This is the one place the setting is decided; the edge UI, `kubectl`
and `railgrid edge create --harness` all write this field, and whoever can
update the edge can change it.

**The agent applies it live and caches it on disk.** The agent watches its
own edge object (it already holds `get`, `list` and `watch` on it) and
reconciles the local runner set from `spec.harness` without a restart: a
change to `none` stops the children, a change back starts them, a change
to the `explicit` list starts and stops the difference. On every observed
change it writes the effective setting to
`~/.railgrid/agent-<edge>.harness.json`, and on start it reads that file
first, so a machine that boots while the hub is unreachable runs what it
was last told and not what its install flags said. The discovery
reconciler requeues on an edge `spec.harness` change so the `Service`
appears or disappears within seconds rather than on the five-minute poll.
Detection itself is re-run on every agent start and on every reconcile, so
a harness installed after onboarding is picked up without touching the
hub.

**Flags only seed.** `railgrid agent join --harness <auto|none|list>` sets
the cached file when no file exists yet and is otherwise ignored; the
first observed `spec.harness` overwrites it. For an install-time opt-out
that sticks, `railgrid edge create --harness none` sets the spec itself,
and `install.sh` passes `--harness` through to it. There is no agent-side
override of the hub setting: config wins over flags, always.

On Linux the runner account is `--runner-user <account>` (today's
`--addon-user`, renamed, same `localuser` checks); when it is not given,
`install.sh` and `agent install` create a dedicated `railgrid-runner`
system account, so the default path never runs a harness as root and never
refuses for lack of a flag. On macOS the LaunchDaemon's worker account is
used.

**Disabling with work in flight.** The runner child gets the same graceful
stop as an agent upgrade: it persists the receipt, drains, and records the
attempt as `needs_input` with a restart-reconciliation blocker. The
discovered `Service` goes undetected and is removed, the agents reconciler
flips the Agent to `BackendReady=False` with reason `HarnessDisabled`, and
a parked run stays parked. State directories are kept, so re-enabling
resumes the same sessions.

**Supervision.** For each configured harness the agent runs one
`railgrid runner run --harness <name>` child on a loopback port it
allocates itself (first free port from 8787), as the runner account, with
the supervisor that exists today. State lives under
`<runner-user home>/.railgrid/runner/<harness>/` (token, `runner.json`,
harness home, `state/`, `runner.log`). The runner bearer is generated once
by the agent and stays on the host: the agent's own `/svc/**` proxy
(`pkg/agent/tunnel/svc.go:92`) adds `Authorization: Bearer` when the target
is one of its runner ports. Nothing about the runner is written to the hub
except status.

**Advertising.** The runner appears in the agent's `/api/v1/services`
answer as type `runner` with the harness name, so the discovery reconciler
materialises `Service <edge>-<harness>` with `host: 127.0.0.1`,
`scheme: http`, the allocated port, `auth: none` (the agent injects), and
`Detected=True`. The service catalog gains one entry: type `runner`, probe
`GET /runner/v1/capabilities`, no MCP tools. The validation reconciler's
probe fills `status.version` and a new `status.harness{name, version,
ready, reasons}` from the capabilities response, which is what a portal
and the agents reconciler read. Undetected on two consecutive polls means
the harness was removed from the machine, and the Service goes the way
every other discovered Service does.

**The edge heartbeat** publishes `status.harnesses: [{name, detected,
enabled, ready, version}]` on the `LinuxServer`/`MacOSServer`, replacing
`allowedAddons`, so "which of my edges can run Claude Code" is one field
on the edge before anyone reads a Service, and a harness that is installed
but switched off is visible as such.

**The edge UI.** The edge detail page gets a Harness card that renders
`status.harnesses` and writes `spec.harness`: an on/off switch (`auto` ⇄
`none`), and, expanded, per-harness checkboxes that write `explicit`. It
is an ordinary spec PATCH through the hub; the portal needs no verb and no
runner token. `railgrid edge get` prints the same table.

**Deleted.** The `Addon` kind and its CRD, `addonctrl`, the
`addon-credentials` verb and its subresource shell, `pkg/agent/addons`
(the supervisor package moves to `pkg/agent/runner`), the `--allow-addon`
flag, `allowedAddons`, the `railgrid.ai/owner: edges` labelling
requirement on harness Secrets, `docs/edge-addons.md` and the add-on half
of `docs/edges-agent-credentials.md`. The two-key trust model is preserved
in a simpler form: the machine owner's key is `--harness` at install time,
the tenant's key is creating an Agent (or a Factory Worker) that names the
edge, and reaching the runner is RBAC on `services/<name>/proxy` as it is
for every other Service. With `auto` as the default the first key is
implicit: a machine that has Claude Code or Codex installed and is joined
to a workspace is a harness host for that workspace until someone sets
`spec.harness.mode: none` in the UI or at `edge create`. That is the
intended posture; the onboarding UI and `edge create` output say so in one
line, naming the detected harnesses and where to switch them off.

### 2. The Agent brings the credential

**`ModelCredential` gains two providers.** Today's enum is
`openai-compatible | openai`. Add:

| `spec.provider` | Secret keys | What it is |
|---|---|---|
| `claude-code` | exactly one of `oauthToken`, `apiKey` | a `claude setup-token` value or an Anthropic API key |
| `codex` | `auth.json` | a Codex login session file |

The `modelcredential` reconciler already resolves the Secret and sets
`SecretResolved`/`Ready`; for these providers it validates key shape (one
key, no whitespace, parseable JSON for `auth.json`) instead of calling
`GET /models`, and `status.models` is the harness's model aliases from the
catalog. `BuildModel` refuses them, which is correct: they are not chat
endpoints, they are harness identities.

**Transport.** `StartRequest` and `ResumeRequest` gain

```json
"harnessCredential": {"kind": "claude-oauth" | "claude-apikey" | "codex-auth", "value": "…"}
```

It is dispatch data exactly like `repository.token`: stripped before the
request is fingerprinted or persisted (`runner.go` already does this for
the clone token), never logged, never on the command line. The runner
hands it to the adapter per launch: the Claude adapter sets the one
environment variable it sets today, from the launch instead of a file; the
Codex adapter writes `auth.json` into a per-attempt `CODEX_HOME` under the
attempt's worktree with mode `0600` and removes it when the attempt reaches
a terminal phase. A resume after a runner restart carries the credential
again, so nothing has to survive on disk. A start without a credential is
refused with `invalid_request`; the runner has no fallback identity.

**Who sends it.** The agents provider reads the Agent's `credentialRef`
Secret with the same `LoadCredential` path it uses for model backends and
attaches it to every start and resume. Factory does the same from a
`credentialRef` on `FactoryLine.spec.profile`; its managed-runner enrolment
form, which today stamps the Secret for the edge, goes away with the Addon.
This is the one Factory change the plan requires beyond adopting the
shared client.

### 3. One backend seam

**API.**

```yaml
apiVersion: agents.railgrid.ai/v1alpha1
kind: Agent
spec:
  backend:
    type: harness                      # model | harness; default model
    harness:
      edgeRef: {kind: LinuxServer, name: build-01}
      credentialRef: my-claude         # ModelCredential; its provider picks the harness
      model: sonnet                    # optional, passed through as StartRequest.model
      workspace: persistent            # persistent | ephemeral
  # model backend: today's fields, moved under backend.model
  # backend:
  #   type: model
  #   model:
  #     credentials: {chat: openai-main, background: cheap}
  #     fallbacks: [openrouter]
```

Rules, all enforced in the Agent reconciler's `Validated` and a new
`BackendReady` condition:

- `backend.type` selects exactly one block; the other is rejected by CEL.
- The harness is derived from `credentialRef`'s provider (`claude-code` →
  `claude`, `codex` → `codex`). There is no separate harness field to
  disagree with the credential.
- `edgeRef` must name an edge in the workspace whose discovered
  `Service <edge>-<harness>` exists with `status.harness.ready: true`. The
  reconciler copies `status.harness.{name,version}` onto
  `Agent.status.backend`. Unready is `BackendReady=False`, not a run
  failure.
- `spec.tools`, `spec.models` purposes other than `chat`, and
  `spec.modelFallbacks` are rejected on a harness-backed agent rather than
  ignored.

**Execution.** Replace the `executeTask`/`resumeRun` pair with one run
lifecycle over a `Backend`:

```go
// providers/agents/backend
type Backend interface {
    Turn(ctx context.Context, r *Run, in Input, sink EventSink) (Outcome, error)
    Continue(ctx context.Context, r *Run, answer Answer, sink EventSink) (Outcome, error)
    Cancel(ctx context.Context, r *Run) error
}
```

- `backend/model` wraps `engine.StreamTurnWithTools` and
  `engine.ResumeTurnWithTools`; checkpoint interrupts become `NeedsInput`.
  Behaviour is unchanged; this refactor also removes the `resumeRun` copy.
- `backend/harness` holds one `pkg/runner/client` per Agent. `Turn` posts
  one attempt (`taskID` = session, `attemptID` = run ID, `sessionID` = the
  harness session stored on the session row, `workspaceID` = agent name
  when `workspace: persistent`, plus the credential), then tails `/events`
  from cursor 0 and maps `progress` → `delta`/`tool_start`/`tool_end` and
  transcript rows, `needs_input` → an inbox item plus `PendingApproval`,
  `completed` → the assistant message, `failed`/`cancelled` → the terminal
  phase. `Continue` is `POST .../resume` with the clarification ID.
  `Cancel` posts cancel and observes the terminal receipt; the run shows
  `Cancelling` until it arrives, as Factory does.
- Store: a run gains `backend`, `attemptID`, `sessionID`; a session gains
  `harnessSessionID`.
- Usage: Claude Code's `stream-json` result carries cost fields, which the
  runner already forwards in the `completed` event; the harness backend
  writes them to `agents_usage` and the budget check runs before every turn
  as today. Codex reports less, so the budget bounds turns and duration
  there, and `status.backend` says so.

**Tools.** A harness-backed agent uses the harness's tools, not the hub's.
Hub tool families, `Toolset` grants, per-tool approval and the per-call
audit row do not apply to a harness turn. `notify` and `ask` survive as the
run's delivery and the runner's `needs_input`, both already modelled.
Projecting MCP `Connection`s into the harness (`--mcp-config` for Claude
Code, the Codex equivalent) is a follow-up.

**Queue.** The Run object stays the queue. `executor.InProcess` is deleted
in the same change.

### Runner protocol: `runner/v1` additive changes

| Field | Meaning | Runner behaviour |
|---|---|---|
| `sessionID` on start | continue this harness session | `Launch.SessionID` set on a fresh attempt; Claude Code `--resume`, Codex thread resume. `stale_attempt` if unknown or owned by another `workspaceID`. |
| `workspaceID` | a persistent working directory | `stateDir/workspaces/<id>`, kept across attempts. Mutually exclusive with `repositoryID`. |
| `harnessCredential` on start and resume | the caller's harness identity | dispatch data, per launch, never persisted. Required. |
| `repositoryID` + `baseCommit` | become optional | required only when `workspaceID` is absent. |

`approvedInput` stays mandatory; the agents provider fills
`{"provenance": {"agent", "run", "trigger", "workspace"}}`. `MaxTurns`
stays one: a chat turn is one attempt, and `sessionID` is what chains
them. The version string stays `runner/v1`. Factory's requests remain
valid once it adds `harnessCredential`.

### The shared client

Move `internal/runnerclient` and `internal/edgesroute` from the factory
repo into railgrid as `pkg/runner/client`, next to the protocol types:

```go
client.New(cfg *rest.Config, ref ServiceRef) (*Client, error)
(*Client).Capabilities / Start / Inspect / Events(after) / Cancel / Resume / Artifact
```

`ServiceRef{Cluster, Service, EdgeKind, EdgeName}` keeps the identity
checks Factory does today (Service name, `spec.edgeRef`, `status.url`
equal to the rendered proxy path, advertised `runnerID` and protocol).
Factory's `Worker.spec.enrollment` maps onto it one to one. The agents
provider builds one from `edgeRef` and the derived Service name.

The dependency this has on the Service proxy is now covered rather than
assumed. `test/e2e/suites/edgesconn` already proved that a chunked response
through `services/{name}/proxy` is not buffered, by timing chunk arrivals;
phase 1 added the SSE-specific case beside it, which asserts that the
`text/event-stream` content type survives the hop, that a comment keep-alive
on a live-but-quiet stream is delivered rather than held until real data
arrives, and that each `id`/`data` frame lands when it was written.

### Cross-provider access

The agents provider's `manifest.yaml` adds `requires` on
`edges.railgrid.ai` for `linuxservers`/`macosservers` (`get,list,watch`),
`services` (`get,list,watch`) and `services/proxy` (`create`), under the
provider's own identity, the clause-C route Factory already exercises. No
tenant credential rides along; the runner bearer never leaves the host.

## What gets deleted or collapsed

| Today | After |
|---|---|
| `Addon` kind, CRD, `addonctrl`, `addon-credentials` verb + shell, `pkg/agent/addons` (minus supervisor), `--allow-addon`, `allowedAddons` | `spec.harness` on the edge (default `auto`), applied live by the agent and cached on disk; runner as a discovered `Service` of type `runner`; `status.harnesses` on the edge |
| harness credential on the edge (`authSecretRef`, `materializeCodex/Claude`, credential-digest restart hash, owner-label rule) | `ModelCredential{provider: claude-code | codex}` on the hub, sent per attempt |
| `api/resume.go` lifecycle copy, four `startDetached*` starters, `executor.InProcess` | one run lifecycle over `Backend` |
| factory `internal/runnerclient`, `internal/edgesroute` | `pkg/runner/client`, imported by both |
| `Agent.spec.models` / `spec.modelFallbacks` at top level | `spec.backend.model.*` |
| `runner-auth`, `runner-token` in `agentidentity.DataPlaneVerbs` | removed; verb list generated from `grammar.go` and asserted in `verify-provider-contract` |

Not touched, deliberately: Factory's Task/Attempt engine and workflow
packages, and the three edge kinds. Factory could later be re-based on
`run_agent` with a harness-backed agent; that is a separate decision.

## Phases

1. **Protocol and client** (railgrid, then factory). `sessionID`,
   `workspaceID`, `harnessCredential`, optional repository in `pkg/runner`;
   adapters take the credential per launch. Extract `pkg/runner/client`.
   Factory adopts it and adds `profile.credentialRef`. SSE-through-proxy
   test in `edgesconn`. Fix the stale verbs.
2. **Edge as harness provider.** `spec.harness` on the edge kinds with
   CRD default `auto`, the agent's edge watch + on-disk cache,
   `--harness` as a seed on join and a real setter on `edge create`,
   `--runner-user` with a created default account, supervised runner per
   harness, local bearer injection in the svc proxy, `runner`
   catalog type, `status.harness` on Service, `status.harnesses` on the
   edge. Delete `Addon` and everything in the first row above. Factory's
   Worker enrolment already names a Service and needs no change here.
3. **Agents lifecycle collapse.** `backend.Backend`, `backend/model`,
   delete `resumeRun` and `executor.InProcess`. Behaviour-preserving.
4. **`spec.backend` and the harness backend.** `ModelCredential`
   providers, API change with codegen, `BackendReady`, `backend/harness`,
   store columns, manifest `requires`, MCP `create_agent` gains the field.
5. **Portal.** Harness card on the edge page (switch + per-harness
   checkboxes writing `spec.harness`); backend picker on the agent editor
   listing edges with a ready harness; run trace renders harness events
   with the existing tool cards. Edge onboarding UI shows detected
   harnesses and where to switch them off.
6. **Follow-ups**, each its own doc: MCP `Connection` projection into the
   harness; label-selector edge placement instead of an explicit `edgeRef`;
   a `KubernetesCluster` runner (a Deployment, not a supervised child);
   collapsing the three edge kinds.

## Decisions to pin

- **Harness availability is machine configuration; harness identity is
  caller configuration.** The machine says what it can run, the Agent says
  who is paying. Neither side holds the other's half.
- **Auto is the default, none is the opt-out.** A joined machine with a
  harness installed is a harness host. There is no separate enable step on
  either the machine or the hub.
- **The hub setting is the only setting.** `spec.harness` on the edge is
  what runs; the agent's on-disk file is a cache of it for offline starts;
  install flags seed it once and never override it.
- **The runner is a Service, not a kind.** Discovery, validation, proxy and
  RBAC are the ones every Service already has.
- **One backend per Agent, not per run.** An agent that needs both is two
  agents; delegation exists.
- **The harness owns its tools.** No hub-side tool wrapping for harness
  turns.
- **Additive protocol only.** `runner/v1` stays `runner/v1`.
- **Nothing removed keeps a shim.** The `Addon` CRD, verb and flags are
  deleted, not deprecated.
