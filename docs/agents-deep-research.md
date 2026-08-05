# Deep research — ad-hoc sub-agents, fan-out, and long multi-step runs

> **Status (2026-08-03): all six phases implemented.** Companion doc:
> [agents-invocation-api.md](agents-invocation-api.md) (invoking agents from
> outside). This doc is about what happens *inside* one run: spawning scoped
> sub-agents, running them in parallel, and surviving the length of a real
> research task.
>
> **What shipped:** `spawn`/`join` tools with concurrent workers, subset-only
> tool grants, depth 2, the `background` model purpose for cheap workers,
> parsed `Sources:`, `Run.Output`/`Run.Sources` on the run record and the run
> API, a `spawn` tool family with a portal toggle, `web_fetch maxChars`,
> session compaction with the `compaction` model purpose, an in-turn context
> budget in the engine, periodic run checkpoints, and a recovery sweep that
> resumes runs a restarted replica dropped. See "Implementation notes" at the
> end for the places the design and the code deliberately differ.

## Problem

An agent asked to "research X thoroughly" today runs a single flat ReAct loop
(`engine/engine.go:256`) with strictly serial tool calls
(`engine/engine.go:288-336`), a last-40-message context window with no
compaction (`api/agents.go:31`, documented gap in
[agents-provider-architecture.md](agents-provider-architecture.md) §"Context
compaction"), and web tools tuned for chat (5 search hits, 12 KB page clip —
`tools/web.go:31,345`). The one sub-agent primitive, `delegate`
(`tools/core.go:35`, `api/toolset.go:63-115`), is real but narrow:

- targets only a **pre-configured Agent CR** named in `spec.delegates` — no
  ad-hoc worker with a caller-supplied prompt/toolset/model;
- **serial**, fan-out capped at 3, depth hard-capped at 1
  (`api/toolset.go:67,74`);
- child returns a bare string — no sources, no structure.

And the substrate is fragile for long work: the in-process executor drops
queued jobs on restart (`executor/executor.go:86`), `Running` rows go stale
with no sweep (manual force-abort only, `api/runs.go:187`), and interactive
runs die with the HTTP request (`api/agents.go:565`).

What already exists and is load-bearing for this design — do not rebuild:

- **Run lineage**: `ParentRunID` in the store, `runDetail.Children`
  (`api/runs.go:178`) — the run tree is already queryable and rendered.
- **Budget rollup**: child usage re-checked against the parent
  (`api/toolset.go:103`); the spend cap fences any fan-out.
- **Checkpoint/resume machinery**: `engine/checkpoint.go` serializes the
  whole turn (messages, pending calls, usage, iteration) and
  `ResumeTurnWithTools` rehydrates it; `ClaimRun` makes resume
  multi-replica-safe. Only approvals *trigger* it today — the mechanism is
  generic.
- **Self-scheduling**: `schedule_create` with `type: wakeup`
  (`tools/core.go:152`) — parked work survives restarts even though
  in-flight runs don't.
- **Purpose-keyed model resolution**: `spec.models` is a `map[purpose]cred`
  and `Profiles.Resolve(purpose)` (`llm/profiles.go:115`) is implemented —
  but only `"chat"` is ever passed. `background` and `compaction` are
  declared, documented, and dead.
- **Single execution path**: everything goes through `executeTask`
  (`api/run.go:161`) and every tool through `wrapTool`'s approval-gating,
  redaction, and tracing (`api/toolset.go:260`). New run kinds inherit all of
  it.

## Goals

1. **Ad-hoc sub-agents**: a run can spawn a scoped worker with an inline
   task, a *narrowed* subset of its own tools, and a cheaper model — without
   an admin pre-creating delegate Agent CRs.
2. **Parallel fan-out with a join**: N sub-agents at once, results collected.
3. **A research run survives its own length**: context compaction, and runs
   that outlive replica restarts (phased).
4. **A structured result**: findings plus sources, not a clipped string.

## Non-goals

- A second runner (claude-code pod). Still the right long-term answer for
  multi-hour autonomous work
  ([agents-provider-research.md](agents-provider-research.md) §recommendation)
  but it is blocked on the infra `agent-workspace` Template and is not needed
  for research runs in the minutes-to-an-hour band this design targets.
- Multi-provider orchestration — a sub-agent here is always a child run of
  the same agent in the same workspace. Cross-agent delegation is the
  `delegate` tool and, cross-workspace, the invocation API doc.
- Replacing `delegate`. It keeps its meaning ("hand this to *that* other
  configured agent"); `spawn` below is a different verb ("do this part of
  *my* task with a scoped worker").

## Design

### 1. `spawn` — ad-hoc sub-agent tool

New core tool, wired in `buildToolset` beside `delegate`:

```
spawn {
  task:        string   // required — the sub-task, self-contained
  instructions: string  // optional — extra system guidance for the worker
  tools:       []string // optional — families ⊆ the CALLING RUN's grant;
                        // default: ["web"] — search + fetch only
  maxToolTurns: int     // optional, default 8, cap 16
}
→ { taskId }            // immediately; see join
```

- The worker is an **anonymous child run of the same Agent CR**: same
  identity, same workspace, `Trigger: RunTriggerSpawn` (new constant beside
  the existing set in `types_run.go`), `ParentRunID` set,
  `SessionID: "spawn:<parentRunID>:<n>"`. It appears in `runDetail.Children`
  and the portal run tree with zero new UI plumbing.
- **Authorization is subsetting, not granting**: the worker's toolset must be
  a subset of what the calling run itself holds. Nothing new is reachable, so
  there is no new authz surface — spawn needs no allow-list the way
  `delegate` does. `spawn` itself is granted per class like any family
  (`spec.tools.background.families` gains `"spawn"`); default off.
- Worker system prompt = a fixed sub-agent preamble ("you are a scoped worker;
  your final message is returned verbatim to the caller as data; end with a
  `Sources:` list of URLs you relied on") + the parent agent's persona +
  `instructions`. Fresh context — no parent history, no memory injection
  (workers are stateless; the parent owns synthesis and memory).
- **Model**: workers resolve `Profiles.Resolve("background")`, falling back
  to `"chat"` when unset — this finally gives `spec.models["background"]` its
  purpose (cheap fan-out) instead of deleting it. `Resolve` already
  implements the fallback order.
- Approval posture: workers inherit the calling run's class. A worker that
  hits an approval gate does **not** park the whole research run — it
  terminates with a "needed approval for <tool>; not attempted" result, the
  same philosophy as delegation's paused-child note (`api/toolset.go:108`).
  Gate-requiring work belongs in the parent, where the inbox flow exists.
- Budget: identical rollup to delegation — worker usage counts against the
  parent's run budget and the agent's spend caps.

### 2. `join` — the collect point, and where parallelism lives

```
join { taskIds: []string, timeoutSeconds?: int }   // default 300, cap 900
→ [ { taskId, phase, result, sources[] } ]
```

- `spawn` enqueues the child run and returns immediately; children execute
  **concurrently** on their own goroutines (bounded, below). `join` blocks
  until all listed children reach a terminal phase or the timeout, returning
  partial results with phases for stragglers (which keep running; a second
  `join` can collect them).
- This puts fan-out/join **above** the engine rather than inside it. The
  engine's serial tool loop (`engine/engine.go:288`) stays untouched — the
  parent makes N cheap `spawn` calls (serial, milliseconds each) and one
  `join`. We deliberately do NOT parallelize engine tool execution in this
  design: it touches every tool's assumptions (checkpointing order,
  approval interrupts mid-batch, transcript ordering) for a gain that
  spawn/join already delivers where it matters.
- **Concurrency bound**: per-run worker semaphore, default 4 concurrent
  children (`spec.limits.maxConcurrentSpawns`, cap 8). Total spawns per run:
  default 10, cap 20 (`spec.limits.maxSpawnsPerRun`). These replace nothing —
  `delegate` keeps its own 3-cap.
- **Depth**: workers can spawn iff depth < 2. Depth tracked by walking
  `ParentRunID` at spawn time (one indexed query). A depth-2 worker gets no
  `spawn` tool, exactly how depth-1 is enforced for `delegate` today
  (`api/toolset.go:67`).
- Children run within the parent's process and context lifetime. If the
  parent is cancelled, children are cancelled (they share the detached run
  context). If the replica dies, parent and children die together — one
  failure domain, honestly stated, addressed in §5.

### 3. Structured results

`store.Run.Output` (added by the invocation doc's phase 1) holds the worker's
final text. For workers we additionally parse the trailing `Sources:` block
into `Run.Sources []string` (best-effort; absent is fine). `join` returns
both. Result clip for spawn results delivered back into the parent's context:
**8 KiB** per worker (vs. the 1.5 KiB tool-observation replay clip at
`api/run.go:431` — synthesis needs the material; the clip stays at 12 KiB→8
KiB deliberate loss, and the full text remains in the store and the portal).

Web tools get one research affordance: `web_fetch` accepts
`maxBytes` up to 64 KiB (default stays 12 KiB) — granted the same way, the
worker's instructions decide when a full read is worth the tokens.

### 4. Context compaction (the other half of "long")

Implements the sketch in
[agents-provider-improvement-plan.md](agents-provider-improvement-plan.md)
§3.2, which this design depends on rather than restates:

- Token-estimate the assembled context in `assembleTurnCtx`
  (`api/run.go:390`); at >70% of the model's `ContextWindow`
  (`llm/catalog.go:27` — today display-only), summarize the older history
  with `Profiles.Resolve("compaction")` and store the summary as a session
  artifact prepended on replay.
- This gives the *parent* run headroom for many join results. Workers don't
  need compaction (fresh short contexts by construction).

### 5. Run durability (phased, mechanism already exists)

Minutes-scale research must survive a deploy. Three steps, ordered by
cost/benefit:

1. **Startup sweep** (small): on boot, scan `Running` rows owned by no live
   run (no registry entry), older than a grace period. With a checkpoint →
   phase back to `Pending` for re-resume; without → `Failed` with "provider
   restarted mid-run" (today this requires a human to notice and force-abort,
   `api/runs.go:187`).
2. **Periodic checkpointing** (medium): checkpoint every N engine iterations
   (say 4) for runs with `Trigger ∈ {api, spawn, schedule}`, not only at
   approval interrupts. `engine.Checkpoint` already serializes everything
   needed; `ClaimRun` already makes resume replica-safe. Resume re-executes
   at most the tool calls since the last checkpoint (tools are assumed
   retry-tolerant, which `wait`-loop guidance already presumes).
3. **Auto-resume**: the sweep from (1) enqueues checkpointed runs through
   `resumeApprovedRun`'s machinery (`api/resume.go:50`), generalized to a
   `resumeRun` that doesn't require `PendingApproval`.

The claude-code pod runner remains the escalation path beyond this
(multi-hour, filesystem-heavy work) and is explicitly out of scope here.

### 6. The deep-research recipe (no new machinery)

With §1–§3 in place, "deep research" is a *prompting pattern*, not a feature
flag: an agent granted `spawn` + `web` decomposes the question, spawns a
worker per sub-topic (each: search → fetch → summarize with sources), joins,
optionally spawns a second verification wave against the claims, then
synthesizes. The run tree in the portal *is* the research trace;
`GET /api/runs/{id}` (with output + children) *is* the report envelope;
delivery is `notify` or the invocation API's poll/wait. We ship a documented
system-prompt template for this rather than a hardcoded pipeline — the same
loop covers audits, comparisons, and monitoring sweeps.

## Limits summary

| Knob | Default | Cap | Where |
|---|---|---|---|
| Concurrent workers per run | 4 | 8 | `spec.limits.maxConcurrentSpawns` |
| Spawns per run | 10 | 20 | `spec.limits.maxSpawnsPerRun` |
| Spawn depth | 2 | 2 | derived from `ParentRunID` chain |
| Worker `maxToolTurns` | 8 | 16 | `spawn` arg |
| `join` timeout | 300 s | 900 s | `join` arg |
| Worker result into parent context | 8 KiB | — | replay clip |
| `web_fetch` maxBytes | 12 KiB | 64 KiB | tool arg |
| Spend | unchanged | unchanged | existing budget rollup |

`delegate` keeps its existing caps and semantics untouched.

## Phases

| Phase | Contents | Depends on | Status |
|---|---|---|---|
| 1 | `spawn`/`join`: tool shape, subsetting, `RunTriggerSpawn`, lineage, `background` model purpose | invocation doc phase 1 (`Run.Output`) | **done** |
| 2 | Concurrency: per-run semaphore, parallel children, partial `join` | 1 | **done** |
| 3 | Structured results (`Sources`), 8 KiB replay clip, `web_fetch` maxChars | 1 | **done** |
| 4 | Compaction (improvement-plan §3.2) + in-turn context budget | — (independent) | **done** |
| 5 | Durability: periodic checkpoints → sweep → auto-resume | — (independent) | **done** |
| 6 | Research prompt template + portal run-tree polish | 1–3 | **done** |

Phases 1–3 shipped together (the concurrency was small once the coordinator
existed, and serial-first would have shipped a version nobody wanted). Phases 4
and 5 followed as the "survives its own length" half.

### Phase 4 as built: two different overflow problems

The design treated context as one problem. It is two, and they need different
mechanisms:

1. **Across turns.** A long-lived session (a channel conversation, a schedule
   replying for months) assembles more history than the window holds. Fixed by
   **session compaction** (`api/compact.go`): before assembling a turn, if the
   estimated context exceeds 70% of the model's window, everything older than
   the newest 10 messages is summarized with the `compaction` model and stored
   as a `SessionSummary` row; replay substitutes the summary for those messages.
   Compacting again folds the previous summary in, so one row always stands for
   the whole prefix. `/new` (DeleteSession) clears it, since it would otherwise
   replay a conversation the user just wiped.
2. **Within one turn.** A single turn's tool observations can be individually
   huge — a research parent joining ten workers, several full-page fetches —
   and those observations live only in the wire conversation, never in the
   transcript compaction operates on. Fixed by **`engine.TurnConfig
   .ContextBudgetTokens`** (`engine/context.go`): before each model call, if the
   conversation exceeds 80% of the window, the oldest tool observations are
   clipped *in place* to a stub that says it was shortened. Messages are never
   removed — the OpenAI wire format requires every `tool_call` to be answered by
   a matching tool message, so dropping one would make the request invalid.
   Trimming escalates: first the observations outside the recent window, then,
   if still over, the recent ones too, always sparing the newest.

The two thresholds are ordered on purpose (70% then 80%): compaction is
lossy-but-summarized and gets first chance; clipping loses detail the model
explicitly asked for and is the fallback. Token counts are a documented
4-bytes-per-token heuristic (`llm.EstimateTokens`) rather than a real
tokenizer — the provider talks to any OpenAI-compatible endpoint, so the true
tokenizer varies per model, and this decision only needs to be roughly right.

### Phase 5 as built

- **Periodic checkpoints.** `engine.Callbacks.OnCheckpoint` fires every 4
  iterations at a point where no tool call is in flight, and
  `checkpointRecorder` (`api/recover.go`) persists it while the phase stays
  `Running`. The snapshot deliberately carries **no pending call**, so resuming
  re-asks the model instead of re-running a tool — one extra model call buys
  freedom from any assumption that tools are safe to repeat.
- **The sweep.** `sweepStaleRuns` runs at startup and on every scheduler tick.
  It lists `Running`/`Pending` runs across all tenants (`ListUnfinishedRuns`,
  the one store query that ignores `Scope`), skips any executing on this
  replica, and for the rest either resumes from the checkpoint or fails them
  with a message that says the provider restarted. `PendingApproval` is never
  swept: it is waiting for a person, not stranded.
- **Auto-resume.** `resumeApprovedRun` was generalized to `resumeRun` +
  `resumeIntent`, so recovery reuses the approval machinery (`ClaimRun` for
  multi-replica safety, checkpoint rehydration, delivery to the run's channel).
  Tenant access comes from the background executor's virtual workspace, as the
  agent's own ServiceAccount — the same identity a scheduled run uses.
- **Attempt cap.** Each resume increments `Run.Attempt` *before* resuming, and
  the third gives up. A run that crashes the provider would otherwise be
  resumed on every restart, turning one bad run into a crash loop.

## Testing

Shipped (`api/spawn_test.go`, `tools/spawn_test.go`, `store/postgres_test.go`,
portal `config.test.ts` / `activity.test.ts`):

- Subsetting: an ungranted family is dropped rather than failing the worker,
  and an all-ungranted request narrows to core only; `edges` is never passable.
- Depth: a depth-1 worker still gets `spawn`, a depth-2 worker does not (and
  loses `join` with it).
- Worker policy: `notify`/`ask`/`memory_save`/`schedule_*`/`delegate` are
  filtered out, asserted against a parent that *does* have them so the test
  proves the filter rather than an absence.
- Worker context: no memory injection, no session history, preamble +
  persona + parent instructions + parent task present.
- Coordinator: per-run cap refused with a message naming the limit; the
  semaphore's peak concurrency is both bounded *and* reached (a serial
  implementation fails this); a cancelled parent aborts queued workers; join
  timeout reports partial results and a straggler is collected by a later
  join; failures and approval gates are reported distinctly, and a gated
  worker's partial text is *not* presented as an answer.
- Tool layer: the family is absent unless both closures are injected; array
  arguments survive the shapes models actually emit (JSON array, bare string,
  comma-separated); the description states the limits that will be enforced.
- Store: output/sources round-trip on Postgres against the real DDL, and no
  sources clears to nil rather than a JSON null.
- Portal: fan-out grant on/off, background grant cleared with it, and the
  families rebuild preserving `spawn`.

Phase 4–5 coverage (`api/compact_test.go`, `api/recover_test.go`,
`engine/context_test.go`, `store/postgres_test.go`):

- Compaction runs end-to-end against a fake OpenAI-compatible streaming
  endpoint (`newFakeLLM`), so the fold, the summary contents, the accounting,
  and the prompt actually sent are all asserted rather than mocked away.
- A small session is not compacted and the model is not called at all; a large
  one folds all but the newest 10 messages; a second pass advances `ThroughAt`
  and hands the previous summary to the summarizer to merge; a worker is skipped
  entirely; a missing credential degrades without wedging the run.
- Replay substitutes the summary and does not also replay the folded messages;
  the summary is framed so the model cannot mistake it for a user turn.
- Trimming: budget 0 disables it; under-budget is untouched; over-budget clips
  oldest-first and converges; the system prompt, the user's turn, and the newest
  observation always survive; message count and `ToolCallID`s are preserved
  (the tool-pairing invariant); a clipped observation announces the gap.
- Checkpoints fire on schedule, carry no pending call, are off by default, and a
  resumed turn checkpoints relative to where it restarted.
- The sweep: fails runs with no checkpoint, resumes checkpointed ones with the
  cluster from the reverse tenant mapping, leaves fresh and locally-live runs
  alone, never touches `PendingApproval`, gives up after the attempt cap, and
  fails with a specific reason when resume is unconfigured, errors, or has no
  workspace mapping.
- `checkpointRecorder` keeps the phase `Running`, preserves the delivery target,
  and refuses to clobber an approval checkpoint.
- Postgres: summary upsert/read/delete-with-session, reverse tenant lookup, and
  `ListUnfinishedRuns` ordering, phase filtering, and per-row scope — verified
  against a real database with the migration applied in place.

End-to-end (`api/research_e2e_test.go`) — a whole research pass through the real
`executeTask` path, with only the model endpoint faked (`scriptedLLM`, an
OpenAI-compatible SSE server that plays the parent and the workers by reading the
conversation it is sent):

- The parent decomposes into one `spawn` per sub-question **in a single assistant
  turn** (as a real model does), calls `join` exactly once, and synthesizes.
  Asserting "one join" is the guard against the failure the prompt warns about —
  joining per spawn silently serializes the fan-out.
- Workers run on the cheap `background` model while the parent runs on `chat`,
  asserted per request.
- The run tree records a child per worker with `Trigger: spawn`, its own output,
  and its `Sources` parsed out of the prose (and *not* left in it).
- All spend lands in the agent's single usage bucket — the property that makes
  the absence of an explicit rollup correct rather than a leak.
- A worker whose tool call is refused by the real SSRF guard still reports, the
  refusal is recorded as a failed step on the worker's own run, and the pass
  still completes.
- A worker that retries a failing tool forever is stopped by exactly the
  `maxToolTurns` its parent gave it, ending `Succeeded` with the engine's
  truncation marker rather than burning budget in a loop.
- Without the `spawn` grant there is no fan-out and no child runs: the tools are
  simply absent and the agent answers as best it can.
- The preset's grant list is checked against the toolset it actually produces, so
  a preset cannot drift from the tools it is meant to enable.
- Stream lifetime: `detachedStreamContext` keeps the run context alive after the
  request context is cancelled (and preserves request-scoped values), and a run
  executed on a detached context whose caller has already gone away still reaches
  `Succeeded` with its answer on the run record *and* in the transcript.
- Portal: the still-working banner appears for `Running` and `PendingApproval`,
  not for a finished run; "Stop it" cancels and clears it; a finish event clears
  it and reloads the transcript; a failed run lookup never breaks the view.

## How results come back

Workers do not call back. `spawn` starts a child run and returns a task id; the
parent stays in its own tool loop and blocks inside `join`, which waits on the
children and hands their answers back as one tool observation. In-process, no
queue, no webhook. A worker's own token stream goes nowhere (`spawn` wires no
`OnDelta`), and `closeTools` makes the parent's run wait for its workers, so a
run is never "done" with work still outstanding.

The parent's answer is delivered per trigger:

| Trigger | Delivery |
|---|---|
| Portal chat | SSE on the open request: `tool_start`/`tool_end` per spawn and join, then the parent's `delta`s, then `done`. Worker progress is not in the stream — the run tree in the **Runs** tab is that view, and it refreshes as each child's phase changes. |
| Channel (Telegram/Slack/Discord) | Fully background: the executor runs the fan-out and pushes the answer back to the channel the user typed in, clipped to 3500 chars. |
| Schedule / trigger | `notify` to the source's `channelRef`, else the agent's primary channel. |
| Programmatic | Poll `GET /api/runs/{id}` for phase + `output` + `sources` + `children`. There is no completion webhook (invocation doc, phase 4). |

**A chat run outlives its stream.** `chat` used to pass `r.Context()` straight
into `executeTask`, so closing the tab cancelled the run — and its workers with
it — throwing away minutes of real spend and leaving a transcript that looked
like the reply never came. It now splits the two
(`detachedStreamContext`, `api/http.go`): writes stop when the client goes, the
run keeps going, and the answer lands in the session transcript and on the run
record. Reopening the chat rehydrates it; `POST /api/runs/{id}/cancel` is still
how a user stops it deliberately.

The handler stays parked on the run rather than returning early, which keeps the
`ResponseWriter` valid for every write (net/http forbids using it after the
handler returns) at the cost of one idle goroutine per abandoned run, bounded by
the run's own timeout. Reopening a session while a run is still in flight shows a
banner — "this chat has a run still working" — with links to watch or stop it, so
the reply does not look lost and the user does not re-ask and pay twice.

Recovery keeps the same asymmetry on purpose: a resumed **channel** or
**schedule** run delivers its answer to the channel, while a resumed **chat** run
only updates the transcript and emits a run event, because there is no stream to
write to.

## How a user turns it on

There is **no keyword and no mode**. Fan-out is a tool grant plus a prompt, and
both are handed over by the **"Research agent" preset** in the create wizard
(portal `presets.ts`), which sets the persona below and grants
`core` + `web` + `spawn` in one step. After that, asking a decomposable question
in chat is enough.

Without the preset it is two manual steps — Config → Tools & toolsets →
Research fan-out → *Spawn workers*, then paste the persona — which is why the
preset exists: the capability was reachable but undiscoverable.

The preset only writes defaults. Everything it sets stays editable, nothing is
stored on the agent to mark it as a preset, and no behavior keys off one. A
prompt the user typed in the wizard is never overwritten.

## The research prompt template

Fan-out is a capability, not a workflow — the recipe lives in the agent's
system prompt (`RESEARCH_PROMPT` in the portal, kept in sync with this section).
A starting point for a research agent granted `spawn` + `web`:

```
When a question is broad enough to have independent parts, research it in
parallel instead of serially:

1. Decompose it into 3–6 sub-questions that do not depend on each other's
   answers. If one genuinely depends on another, do that one yourself first.
2. spawn one worker per sub-question. Each task must stand alone — the worker
   cannot see this conversation, so restate every name, date, version and
   constraint it needs. Ask for specifics, not a summary.
3. Call join ONCE, after starting all of them. Calling join after each spawn
   makes the whole thing serial and slow.
4. Read the findings critically. Where two workers disagree, or a claim is
   load-bearing and thinly sourced, spawn a second short wave to check just
   that claim.
5. Answer in your own voice, with the sources the workers reported. Say what
   the evidence does not cover — the gaps are usually the useful part.

Do the judgement yourself. A worker reports; you decide.
```

The run tree in the portal (`GET /api/runs/{id}` → children) is the trace:
each worker is a child run with its own steps, output, sources, and cost.

### Watching a fan-out

The run-detail page is the live view. Above the child table it tallies what the
workers are doing — "2 running · 1 queued · 3 done" with a spinner while any are
outstanding — and the table updates as each finishes; click a worker to see its
own steps.

Two details make that work, and both were missing at first:

- Run events carry `parentRunID`. Without it a client could only refresh for
  children it had *already loaded*, so a worker spawned after the page opened
  stayed invisible until a manual reload — which is what made a fan-out look like
  nothing was happening.
- **Running and queued are counted separately.** With concurrency 4 and ten
  workers spawned, six sit on the semaphore; collapsing them into one "in
  progress" number makes a queued worker indistinguishable from a stuck one.

Chat shows the same run as `spawn` and `join` tool rows, so you can see how many
workers started, but per-worker progress lives in the run tree.

## Implementation notes — where the code differs from this design

Recorded because each one was a deliberate correction found while building, not
a shortcut.

1. **No budget rollup for spawn, by design.** The design said "identical rollup
   to delegation". Wrong: a worker is the *same agent* in the same scope, so
   `executeTask`'s own `AddUsage` already lands its spend in the parent's
   bucket. Delegation needs an explicit rollup only because its child is a
   different agent. Adding one here would double-count. `checkBudget` at the top
   of every worker means a fan-out that blows the cap starts failing workers
   instead of silently overspending.
2. **Model purpose resolves through `spec.models`, not `Profiles.Resolve`.**
   The design pointed at `llm.Profiles.Resolve(purpose)`, which serves the
   legacy multi-profile Secret. The live path is `spec.models[purpose]` naming a
   per-credential Secret, so the work was a new `buildModelForPurpose` that
   falls back `background` → `chat` (`api/agents.go`). `Profiles.Resolve`
   remains unused. Only spawned workers use `background`; scheduled runs were
   deliberately left on `chat` so this change alters no existing agent's
   behavior.
3. **`spawn` is a tool family, so it is a grant.** Not stated in the design.
   Adding it to `knownToolFamilies` created a trap in the portal, which rebuilds
   families from wired connections on every tool toggle and would have switched
   fan-out off behind the user's back — hence `STANDALONE_FAMILIES` in
   `conn-defs.ts` and the `current` argument to `familiesForConns`.
4. **Workers lose more of `core` than the design implied.** `notify`, `ask`,
   `memory_save`, `schedule_*` and `delegate` are filtered out
   (`workerExcludedTools`): ten workers each pushing to a channel, filling the
   inbox with questions nobody can answer in time, writing memory notes, or
   rescheduling the agent is all cost and no benefit. They keep `wait` and
   `memory_list`.
5. **A cancelled parent needs a re-check after the semaphore.** When the parent
   is cancelled at the same moment a slot frees, both `select` cases are ready
   and Go picks randomly — a cancelled run could still start work. Found by the
   test, fixed with a `runCtx.Err()` check after acquiring.
6. **`join` with no ids collects everything outstanding**, and marks what it
   reported, so a second bare `join` does not re-dump the same findings. The
   design left the no-args case unspecified.
7. **The parent's task is quoted to each worker** (`workerRun.ParentTask`). A
   worker that knows nothing about the whole tends to answer a subtly different
   question than the one that was needed.
8. **`web_fetch`'s knob is `maxChars`, not `maxBytes`** — it clips extracted
   text, not bytes — and the body read cap rose from 200 KiB to 1 MiB so a large
   page can actually yield 64 KiB of text.
9. **`buildToolset` was reordered.** The delegate closure used to be wired
   before the grant was resolved; spawn needs the effective family list to
   compute what a worker may inherit, so family resolution now comes first.
10. **The coordinator takes an `exec` seam.** `spawnCoordinator.exec` is
    `s.executeTask` in production; the indirection exists so the fan-out logic
    is testable without a live model, which is what made the concurrency and
    cancellation tests possible at all.

### Phase 4–5 notes

11. **The design's "compaction fixes the parent's join headroom" was wrong.**
    Join results are tool observations inside one engine turn; they never reach
    the transcript session compaction operates on. That is why phase 4 shipped as
    *two* mechanisms (see "Phase 4 as built") rather than the one the design
    described. Session compaction alone would have left the exact case this
    feature exists for unfixed.
12. **`Profiles.Resolve` stays unused for compaction too.** Same reason as note
    2: the live path is `spec.models[purpose]` → per-credential Secret, so
    compaction uses `buildModelForPurpose(..., PurposeCompaction)` with the same
    purpose → chat fallback. All three declared purposes are now genuinely wired:
    `chat`, `background` (workers), `compaction`.
13. **Compaction is billed to the agent.** It is a real model call, so its usage
    goes through `AddUsage` on the agent's rolling window. A compaction that were
    free-of-charge in the accounting would make budgets lie.
14. **Recovery checkpoints carry no pending call, by choice.** The design
    worried about tool idempotency on resume ("tools are assumed
    retry-tolerant"). Taking the snapshot *between* rounds removes the question:
    there is nothing half-done to repeat, and resume costs one model call.
15. **`Attempt` is incremented before the resume, not after.** Incrementing
    after would never run for the failure mode that matters — a run that takes
    the process down — so the cap would never engage.
16. **The sweep needed a reverse tenant lookup.** A stored run knows its
    `(org, workspace)` but not the logical cluster whose virtual workspace can
    resume it, and every other store read gets the cluster from the request.
    Hence `FindClusterForScope` and an index on `agents_tenants (org, workspace)`.
17. **`ListUnfinishedRuns` is the only store query that ignores `Scope`.**
    Called out in the interface comment, because it breaks the invariant every
    other method holds — a restart has to find work it has no request context
    for.
18. **`staleRunGrace` (15 min) must exceed the checkpoint interval.** A healthy
    long-running run rewrites its row every 4 tool rounds; if the grace period
    were shorter than that gap the sweep would fight live work on other replicas.
    The two constants are coupled and documented as such.
19. **Cost attribution was quietly wrong for workers.** `executeTask` priced
    every run with `primaryModelName` (the chat model), so a worker on the cheap
    background model was billed at the expensive rate. Now it prices with the
    purpose-resolved model it actually called — a phase-1 bug this work surfaced.
20. **`engine.TurnConfig` replaced the bare `maxIters` parameter.** Three new
    per-turn knobs (iterations, context budget, checkpoint interval) as positional
    arguments would have been unreadable; the struct also lets callers pass only
    what they mean, with `normalized()` applying defaults in one place.

### Discoverability note (2026-08-03)

The original design said "deep research is a prompting pattern, not a feature
flag". That is still the right architecture — but taken literally it shipped a
capability with no front door: a user had to know to grant a tool family *and*
to write a decomposition prompt, and there is deliberately no keyword or mode in
chat. The "Research agent" preset closes that without introducing a mode: it is
defaults on the create form, writing the same fields the Config pane writes.

Two smaller things this surfaced, both left as they are:

- `createAgentRequest` gained `interactiveFamilies` / `backgroundFamilies`. A
  preset that created an agent and then patched it would have shown the user a
  half-configured agent for one round trip.
- The provider serves the portal bundle at `/main.js`; the hub's UI proxy strips
  `/ui/providers/agents` before forwarding. A request to the *prefixed* path hits
  the provider's SPA index fallback and returns 200 with HTML — fine behind the
  hub, but it makes hand-testing the provider directly misleading. Fetch
  `/main.js` when smoke-testing a provider process on its own.

### Streaming-lifetime note (2026-08-03)

Detaching the chat run from its stream is the smaller of the two fixes that were
on the table. The larger one — chat POSTs, gets a `runID`, and subscribes to
`/api/events` for progress — is still the right end state: it makes the client a
follower rather than an owner, removes the parked goroutine, and converges with
the invocation API's `POST /api/agents/{name}/runs`. It was not done here because
it reshapes the chat view's streaming model, and the data-loss surprise was worth
removing on its own first.

Two things to know if picking that up:

- `startDetachedRun` (`api/run.go`) is already the pattern; the chat handler now
  differs from it only in holding the stream open.
- `/api/events` is an in-process bus, so a follow-the-run UI is only reliable
  behind a single replica until that is addressed (invocation doc, absent item 9).
