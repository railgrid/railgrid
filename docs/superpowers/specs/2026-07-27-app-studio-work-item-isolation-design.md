# App Studio Work Item Isolation Design

**Status:** Proposed for review

**Date:** 2026-07-27

**Related designs:**

- [App Studio Conversation Resilience](../../app-studio-conversation-resilience.md)
- [App Studio Goal-Achieving Inner Loop](2026-07-23-app-studio-goal-achieving-inner-loop-design.md)
- [App Studio Stale Plan Replanning](2026-07-26-app-studio-stale-plan-replanning-design.md)
- [App Studio Workspace Mutation Grant](2026-07-27-app-studio-workspace-mutation-grant-design.md)
- [App Studio Active Plan Dock](2026-07-27-app-studio-plan-dock-design.md)

This design supersedes the cross-run grant lifetime in the Workspace Mutation
Grant design. The capability and target-path model remains valid, but a
cross-run grant belongs to one WorkItem rather than the project.

## Summary

App Studio will add one durable `AssistantWorkItem` entity representing a
specific user-requested mutation task. Existing conversation messages remain
the transcript and existing `AssistantRun` records remain execution attempts,
snapshots, and Eino checkpoints.

Messages and runs gain a nullable WorkItem ID. A mutation-capable run receives
only the current message and messages linked to its explicitly started or
selected WorkItem. Its cross-run plan grant is stored on that WorkItem. Project
conversation history, model classification, todos, and checkpoint existence
cannot select a WorkItem or supply mutation authority.

The central invariant is:

> No explicitly started or selected WorkItem means no task history, no task
> todos, no mutation tools, and no grant.

This is the smallest durable boundary that prevents unfinished work from
silently becoming part of a later unrelated request.

## Problem

App Studio currently persists:

1. a project-wide conversation transcript;
2. resumable `AssistantRun` snapshots and Eino checkpoints; and
3. one project-wide cross-turn `approved-plan-grant` stored as a reserved run.

There is no durable identity for the work being pursued. A fresh request loads
recent project-wide messages, the fallback router carries the most permissive
intent found in those messages, and the Eino engine loads the project-wide
grant. DeepAgent can then reconstruct old todos from the combined input.

### Confirmed incident shape

The `inspire-me-daily` project demonstrated the failure:

1. A quote-submission request partially changed the workspace and left an
   approved cross-turn plan.
2. A later red-theme request was interrupted.
3. A subsequent question asking whether the theme had changed was routed as
   implementation.
4. The new run received project history and project-wide authority, then
   recreated an old quote-submission todo alongside the red-theme work.

The todo did not come from restoring an old Eino checkpoint. It was
reconstructed in a fresh run because conversation context, task identity, and
mutation authority all shared project scope.

The current implementation makes the path concrete:

- `llm.go` loads up to 24 recent project messages for a new run;
- `assistant_turn_profile.go` merges intent from recent user messages using an
  escalate-only fallback;
- `assistant_eino_engine.go` loads the reserved project-wide grant into fresh
  runs; and
- `assistant_approved_plan.go` persists that grant independently of any durable
  task identity.

## Decision

Introduce one `AssistantWorkItem` table and add WorkItem identity to existing
messages and runs.

- A **Message** remains the durable user or assistant conversation record.
- An **AssistantWorkItem** is the durable mutation task and owner of cross-run
  authority.
- An **AssistantRun** remains one execution attempt and exact resumable
  checkpoint.
- Assistant-message plan metadata remains the user-visible progress projection.
- Eino remains the model, tool, middleware, TurnLoop, and checkpoint runtime.

No separate Turn, ApprovalGrant, WorkItemProgress, or WorkspaceState table is
introduced.

The ownership boundary is:

- **App Studio owns** WorkItem lifecycle, transcript selection, actor and Project
  UID validation, plan grants, run eligibility, and terminal outcomes.
- **Eino owns** agent and tool execution, exact interrupt state, targeted resume,
  checkpoint serialization, TurnLoop cancellation, and optional derived context
  summarization.

Eino executes one App Studio-selected WorkItem and checkpoints one AssistantRun.
An Eino session, checkpoint, todo, task, or summary is never the source of task
identity or mutation authority.

## Goals

- Prevent context, todos, and grants from leaking between unrelated tasks.
- Preserve intentional continuation of a selected task.
- Preserve exact permission/input checkpoint resume.
- Reuse the current message store, durable-run supervisor, plan metadata, and
  Eino integration.
- Keep mutation authorization server-owned and auditable.
- Fail closed when actor, Project UID, WorkItem, grant, repository, or target
  preconditions do not match.
- Preserve the current one-active-run-per-project contract.

## Non-goals

- General workflow scheduling or background retries.
- Multiple concurrent mutating WorkItems in one project.
- Multi-replica mutation coordination.
- A global workspace revision or whole-workspace fingerprint.
- Crash-atomic coordination of arbitrary external provider side effects.
- Process isolation or forced termination for a non-cooperative tool
  implementation.
- A separate progress store or dependency-aware task scheduler.
- Replacing Eino, DeepAgent, TurnLoop, or checkpoint serialization.
- Changing the existing product policy for which approved development actions
  may execute automatically.

## Existing Components Reused

The design extends existing boundaries instead of duplicating them:

| Existing component | Continued responsibility |
| --- | --- |
| `store.Message` | Transcript, current user request, assistant response, and plan metadata |
| `store.AssistantRun` | Attempt status, idempotency, snapshots, audit, and checkpoint |
| Active-run unique index | At most one nonterminal run per project |
| Assistant supervisor | Start, stream, persist, stop, resume, and terminalize a run |
| Plan dock metadata | Render the active run's todos and progress |
| Eino TurnLoop | Execute one fresh run |
| Eino checkpoint | Resume one exact pending run |
| Existing approval policy | Derive allowed capabilities and canonical target paths |

`AssistantRun` is not reused as the task record. A run has attempt-level
terminal states and checkpoint data, while a task may survive several attempts
and coexist with other suspended tasks. Reusing a reserved run would preserve
the current conflation.

## Durable Model

### Project Scope

Extend `store.Scope` with immutable `ProjectUID`.

Every current message, WorkItem, run, and grant query includes:

- organization UUID;
- workspace UUID;
- project name; and
- Kubernetes Project UID.

Deleting and recreating a Project with the same name creates a different UID,
so rows from the old Project cannot be loaded into the new one.

### AssistantWorkItem

Add `app_studio_assistant_work_items`:

| Field | Meaning |
| --- | --- |
| tenant/project scope | Organization, workspace, project name, and Project UID |
| `work_item_id` | Stable opaque identifier |
| `root_message_id` | User message containing the canonical task request |
| `created_by` | Stable authenticated actor |
| `status` | `active`, `suspended`, `completed`, or `cancelled` |
| `status_reason` | Bounded safe lifecycle reason |
| `revision` | Monotonic compare-and-swap revision |
| `active_run_id` | Current nonterminal attempt, when one exists |
| encrypted plan grant | Current approved plan, actor, capabilities, paths, and repository preconditions |
| `grant_revision` | Opaque revision for grant validation and revocation |
| timestamps | Created and updated time |

The root message is the canonical goal. The WorkItem does not duplicate raw
prompts, source, tool arguments, results, or credentials.

The plan-grant blob uses the existing encrypted-store behavior. It contains
only the current cross-run grant. Replacing plan scope replaces the blob and
grant revision; historical grants remain observable through run audit rather
than a second active-grant table.

At most one WorkItem may be `active` for a Project UID. Multiple suspended
WorkItems may exist.

`active_run_id` names only a nonterminal attempt. Pending and `stopping` runs
retain it. Every terminal run transition clears it atomically. Continue
atomically moves the selected WorkItem from `suspended` to `active` and assigns
the newly created run as `active_run_id`.

### Message

Add:

- non-null `project_uid`; and
- `actor_id`, required for user messages and empty for assistant messages; and
- nullable `work_item_id`.

Messages created for Build or Continue are linked to that WorkItem. Discussion
messages have no WorkItem unless the user explicitly selects one as read-only
context.

Existing assistant plan/todo metadata remains attached to the assistant
message. The portal finds progress through the selected WorkItem's active run,
or its latest run when suspended, and that run's `active_message_id`.

WorkItem membership is immutable after assignment. An ephemeral Start-task
action may perform a one-time compare-and-swap from null to a newly created
WorkItem only when the message is a user message, its actor matches
`WorkItem.created_by`, and its tenant scope and Project UID match. A unique
scoped constraint permits at most one WorkItem to use a root message.
Concurrent activation or any attempt to relink a message returns conflict.

### AssistantRun

Add:

- non-null `project_uid`;
- nullable `work_item_id`;
- `mode`: `adaptive`, `discussion`, `new`, or `continue`; and
- nullable `expected_grant_revision`.

Add nonterminal status `stopping`. Only the server-side Stop operation may set
it. A `stopping` run cannot be resumed and cannot begin another mutation.

Store validation enforces:

- `new` and `continue` require a WorkItem;
- `adaptive` starts without a WorkItem and exposes only bounded reads plus the
  plan-approval escalation control;
- a discussion may omit a WorkItem or reference one as read-only context;
- the run, initiating message, and WorkItem share tenant scope and Project UID;
- mode is derived by the server from the validated user action; the only
  allowed change is the atomic `adaptive` to `new` promotion;
- the run's expected grant revision is written only by the server when
  authorization is installed; and
- a run's WorkItem cannot change after assignment.

Resume does not create a new run mode. It claims and restores the exact existing
run.

### Store Operations

Add the minimum WorkItem operations:

- `CreateWorkItemAndAssistantRun` — atomically creates or attaches the root
  message, WorkItem, assistant placeholder, and first run;
- `PromoteAssistantRunToWorkItem` — atomically creates one WorkItem, attaches
  the adaptive run's existing root messages, and advances its mode and revision;
- `GetAssistantWorkItem`;
- `ListAssistantWorkItems`;
- `CompareAndSwapAssistantWorkItem`;
- `ApproveWorkItemPlan` — atomically stores the WorkItem grant revision and the
  owning run's expected grant revision;
- `RequestStopAssistantRun` — compare-and-swap requests Stop for the exact run
  revision or terminalizes an unclaimed pending run;
- `TransitionWorkItemAndRun` — atomically persists run outcome, WorkItem
  lifecycle, `active_run_id` clearing, checkpoint clearing, and grant
  revocation;
- `LoadMessagesForWorkItem`; and
- `LatestAssistantRunForWorkItem`.

Existing discussion-run, snapshot, idempotency, claim, and exact-resume
operations remain.

## User Actions and Routing

The request contract carries a user-selected action:

- **Auto** — the default; answer directly, inspect safely, or propose an action;
- **Ask** — explicit read-only discussion;
- **Build** — start new mutation work;
- **Continue** — start a fresh attempt for a named WorkItem; and
- **Resume** — resume an exact pending run/checkpoint.

The classifier is advisory within Auto. It cannot create authority. Auto gains
a WorkItem only when `request_project_plan_approval` atomically promotes the
current adaptive run before the user-facing permission checkpoint is saved.
Ask cannot upgrade to Build.

### Ask

Ask creates a discussion run. It receives no mutation tools and never loads a
plan grant.

If an Ask message appears to request implementation, the response may include
an ephemeral **Start task** action. Selecting it attaches the original message
to a newly created WorkItem and starts a new run. No proposed WorkItem is
persisted before selection.

A discussion may explicitly select a WorkItem for status context. It remains
read-only.

### Build

Build is an explicit portal/composer action. It atomically creates:

1. the user message;
2. a new active WorkItem rooted at that message;
3. the assistant placeholder; and
4. the first AssistantRun in `new` mode.

The structured initial-project prompt is an implicit Build action.

If another WorkItem is active but has no nonterminal run, the store suspends it
and revokes its grant before creating the new item. If a run is running,
stopping, or pending permission/input, Build returns conflict and does not
preempt it.

### Continue

Continue explicitly supplies a WorkItem ID. The server validates tenant scope,
Project UID, same-actor policy, status, and revision, then creates a fresh
AssistantRun in `continue` mode.

Free-form text such as "continue" does not select a task. The portal may present
available suspended tasks, but mutation starts only after the user selects one.

Continuation never restores a terminal run's Eino checkpoint.

### Resume

Permission/input Resume supplies the exact run, request, and interrupt
identifiers. It claims the existing nonterminal run and restores its Eino
checkpoint.

The run retains its original WorkItem. A checkpoint identifier is routing data,
not a bearer capability. Only runs in `pending_permission` or `pending_input`
are resumable. A stopped or otherwise terminal run is denied even if stale
checkpoint bytes still exist.

## Context Assembly

The router evaluates the current user message. Historical messages may clarify
a reference but cannot escalate Ask to Build or select a WorkItem.

A mutation-capable run receives:

1. the root task message;
2. the current user message;
3. a bounded set of messages linked to its WorkItem;
4. the current WorkItem plan grant;
5. a fresh project, repository, and workspace snapshot; and
6. only the tools allowed by the current action and grant.

It does not receive an undifferentiated project-wide recent-message window.

A discussion may receive broader transcript context because its tool set is
read-only. That history still cannot load a WorkItem grant, task todos, or
mutation tools.

## Grant Lifecycle

### Creation

A cross-run grant is written to the active WorkItem only after:

- an explicit Build or Continue action satisfies the existing routine
  development policy; or
- the user approves a plan.

App Studio derives capabilities and canonical target paths. Model output cannot
widen them.

Exact tool-level approval is a separate run-local alternative. Pending
exact-approval state remains only in the Eino checkpoint, bound to the matching
run/request/interrupt tuple and checkpointed tool call. The approval decision
exists only in the claimed resume request and is never copied into the WorkItem
grant.

### Validation

Immediately before a mutation, the tool boundary reloads and validates:

- authenticated actor;
- run mode is `new` or `continue`;
- run status is `running`;
- run WorkItem ID;
- active WorkItem identity and revision;
- `WorkItem.active_run_id` equals the current run ID;
- Project UID;
- repository identity and expected HEAD, when applicable; and
- expected target-file digest or missing-file marker.

It then uses one of two authorization paths. A targeted exact resume takes
precedence:

1. **Exact approval:** the existing run-claim and request-idempotency boundary
   claims the pending run once. App Studio supplies resume data only to the
   named Eino interrupt target. Eino restores the checkpointed tool call and
   invokes its stored arguments, or the explicitly approved edited arguments.
   The resume request digest includes the canonical decision, answer, and edited
   arguments, so reusing a `clientRequestID` with different edits conflicts.
   Edited arguments pass the normal tool schema, canonicalization, capability,
   target, HEAD, and digest checks before invocation; invalid edits fail closed.
   No second approval ledger or approval-consumption protocol is introduced.
   Failure or an uncertain external outcome is not replayed automatically. A
   retry is allowed only when the provider operation's existing idempotency
   contract makes it safe.
2. **Plan grant:** otherwise, the server-written run expected grant revision
   must equal the current WorkItem grant revision, and the grant must cover the
   capability and canonical target path.

The run and WorkItem must agree, and a plan grant must also agree whenever that
authorization path is used. A discussion run is denied even when it references
the same WorkItem. Session values and model arguments are untrusted hints.

### Revocation

The current grant is cleared by compare-and-swap when:

- the WorkItem completes or is cancelled;
- the run fails, aborts, is stopped, or is interrupted outside a pending
  permission/input request;
- a new task supersedes an idle active WorkItem;
- commit consumes the approved transaction; or
- plan scope changes.

Pending permission/input is nonterminal. The WorkItem plan grant normally
remains stored because the resumed agent may need it after resolving the exact
interrupt, but the one-nonterminal-run constraint permits only exact Resume of
that run to use it. Commit permission is the deliberate exception: requesting
the commit atomically replaces the grant with a fresh tombstone revision before
checkpointing. Exact Resume may execute that commit, but any later source repair
requires fresh plan approval. No separate run may consume either state.

Continuing suspended work creates a fresh run and requires current
authorization. It does not inherit a revoked grant.

Unused exact approvals are discarded on every terminal run and never enter a
Continue run.

## Mutation Serialization and Staleness

V1 retains one provider replica and one nonterminal AssistantRun per project.

The assistant supervisor retains an in-memory control handle for the active
TurnLoop. A small in-process per-project admission gate linearizes tool start
against Stop:

- A mutation enters the gate, confirms admission is open, performs final durable
  WorkItem/grant validation, registers itself as in flight, and leaves the gate
  before calling the tool. Registration is the authorization point of no return;
  the admitted call may subsequently execute and unregisters when it returns.
- Stop of a running attempt enters the same gate, closes admission, transitions
  the run to `stopping`, revokes the grant, and leaves the gate. Calls already
  registered may execute and return; no later call can be admitted.

The gate does not remain locked during model or tool execution. It is only the
single-replica linearization point between authorization and admission; Eino
still owns cancellation and safe-point execution.

Stop then uses this protocol:

1. `RequestStopAssistantRun` compares the exact run revision.
   - For `running`, it changes the run to `stopping`. The WorkItem remains
     active and retains `active_run_id`, but its plan grant is revoked in the
     same compare-and-swap. Tool-boundary validation denies a mutation that has
     not already been admitted; an already-admitted call may execute and return.
   - For `pending_permission` or `pending_input`, it atomically terminalizes the
     run, clears the checkpoint and `active_run_id`, suspends the WorkItem, and
     revokes the grant.
   - If concurrent Resume claims a pending run first, Stop re-reads the run and
     follows the running-loop path. If its TurnLoop control handle is not
     attached yet, the supervisor records the Stop request and applies it before
     or immediately when the loop starts. If Stop wins, Resume is denied.
   - For `stopping`, it joins the existing Stop operation. The same or a
     different authorized `clientRequestID` receives the current `202` or
     eventual terminal result without issuing another Eino Stop signal.
   - A terminal run returns its existing outcome idempotently.
2. For a live loop, the supervisor calls

   ```go
   loop.Stop(
       adk.WithGracefulTimeout(stopGracePeriod),
       adk.WithSkipCheckpoint(),
       adk.WithStopCause("user_stop"),
   )
   ```

3. A background supervisor waits for loop exit. Eino's timeout bounds only the
   grace period before escalation to immediate cancellation; it does not bound
   total `loop.Wait()` time. If the loop has not exited by App Studio's HTTP
   response deadline, Stop returns `202 Accepted` with status `stopping`, and
   the existing durable supervisor continues observing it.
4. On exit, App Studio preserves the execution result that actually won the
   race. For a live loop, Stop contributed when
   `errors.As(exit.ExitReason, &cancelErr)` yields an `*adk.CancelError`; the run
   becomes aborted and the WorkItem becomes suspended. If the Stop request was
   recorded before `loop.Run` and no natural result exists, Stop also wins even
   though Eino exits without `CancelError`. If the run completed or failed
   before Stop took effect, its natural terminal outcome is retained instead.

Every terminal path atomically clears `active_run_id` and the persisted
AssistantRun checkpoint. App Studio's Eino checkpoint adapter implements
`CheckPointDeleter` for best-effort cleanup of a loaded Eino checkpoint, but
terminal run eligibility is the security boundary if deletion fails. Timeout
escalation makes no claim that an external side effect was rolled back.

Every mutating tool wrapper supplies a server-owned execution timeout and
requires the implementation to return when its context is cancelled. If a tool
violates that contract, the run remains fail-closed in `stopping` with no grant
and continues blocking replacement runs; App Studio reports the unhealthy
execution for operational recovery rather than allowing overlapping workspace
mutation. Startup recovery terminalizes an abandoned `stopping` run after the
old process—and therefore its goroutines—has exited.

The design does not introduce a whole-workspace fingerprint. Fresh continuation
runs reload the workspace, and individual mutations validate repository HEAD
and target-file digest. An unrelated file change does not unnecessarily stale
the entire task.

Provider calls retain their existing idempotency and reconciliation contracts.
Coordinating unknown external outcomes across crashes or replicas is a separate
reliability problem and is not claimed by this design.

## Eino Integration

Eino remains inside the App Studio WorkItem boundary:

- Build and Continue create a fresh TurnLoop and checkpoint store.
- Exact Resume restores only the named pending run.
- Stop uses TurnLoop safe-point cancellation and checkpoint suppression; App
  Studio owns `stopping`, response deadlines, and the eventual durable outcome.
- WorkItem ID, grant revision, actor, and Project UID may be passed through
  session values for convenience.
- Mutation-capable turns expose only the tools for the current
  `approval -> mutate -> verify -> repair/commit -> report` phase. This guides
  the model's next action; it does not replace invocation-time authorization.
- Tools reload durable WorkItem state before mutation because Eino session
  values are mutable runtime context.
- Todos and PlanTask subtasks never select a WorkItem or authorize a tool.

No Eino Workflow, GraphTool, PlanTask backend, or custom task scheduler is
required for V1.

### Memory and Summarization

Eino defines Memory and Session as business-layer concerns: the application
selects and stores messages, then supplies the resulting list to the runner. In
App Studio, `LoadMessagesForWorkItem` is that boundary. Messages are filtered by
WorkItem before any model middleware sees them.

System instructions, fresh project/workspace snapshots, and durable
authorization are injected or reloaded at execution time. They are not learned
from or persisted as conversational memory.

Eino summarization is deferred until measured context-window pressure justifies
it. If added, it is derived run context applied only after WorkItem filtering.
It may not become a durable Message, goal, grant, WorkItem selector, or
authorization input. Any transcript or summary storage is scoped to the
WorkItem and run, never the project-wide conversation.

PlanTask output, if ever introduced, remains non-authoritative execution
metadata. It cannot replace `AssistantWorkItem` or the existing plan-dock
projection.

## Lifecycle Outcomes

| Run outcome | WorkItem outcome | Grant outcome |
| --- | --- | --- |
| Requested task completed | `completed` | Cleared/consumed |
| Discussion completed | Unchanged or none | Not loaded |
| Pending permission/input | Remains `active` | Plan grant retained, except commit permission stores a tombstone; exact Resume only |
| Stop requested while loop is running | Remains `active` | Cleared on request |
| Stop wins execution race | `suspended` | Cleared |
| Model/tool/persistence failure | `suspended` | Cleared |
| Provider interruption | `suspended` | Cleared |
| Stale actor/Project/grant/target | `suspended` | Cleared |
| User discards task | `cancelled` | Cleared |

Runs persist a bounded safe failure reason sufficient for the portal to explain
why work stopped. Mutation-capable turns also have pre-mutation semantic
progress bounds. A model receives a final phase-specific instruction before the
bound; if it still does not request plan approval or perform an approved source
mutation, the run fails with reason `no_progress` and the WorkItem is suspended
for a fresh Continue run. The guard does not suspend a run after source mutation
because the mutation/verification marker is run-local; the strict verification
catalog and Eino's global iteration limit remain the safety boundaries after
edits. Read-only Ask turns are not subject to this implementation-progress
bound.

## HTTP Contract

Extend message start:

```json
{
  "content": "Implement the red theme",
  "clientRequestID": "uuid",
  "assistantAction": {
    "kind": "build"
  }
}
```

Continue names a WorkItem:

```json
{
  "content": "Continue with the remaining theme work",
  "clientRequestID": "uuid",
  "assistantAction": {
    "kind": "continue",
    "workItemID": "work-item-id",
    "expectedRevision": 4
  }
}
```

Omitting `assistantAction` means Auto. The server never interprets omission as
"continue current work."

An ephemeral Start-task action resubmits the original Ask message as Build:

```json
{
  "content": "Implement the red theme",
  "clientRequestID": "uuid",
  "assistantAction": {
    "kind": "build",
    "rootMessageID": "existing-user-message-id"
  }
}
```

`rootMessageID` is accepted only for a user message with null WorkItem
membership whose actor, tenant scope, and Project UID match the caller and new
WorkItem. Any mismatch, reuse, or concurrent attachment returns conflict.

Add:

- `GET /api/projects/{project}/assistant/work-items`
- `GET /api/projects/{project}/assistant/work-items/{workItem}`
- `POST /api/projects/{project}/assistant/work-items/{workItem}/cancel`
- `POST /api/projects/{project}/assistant/{run}/stop`

Stop accepts:

```json
{
  "clientRequestID": "uuid"
}
```

It returns the idempotent terminal run result when loop exit completes within
the response deadline, or `202 Accepted` with the current `stopping` run when
execution is still exiting. Stop suspends current execution; Cancel discards a
WorkItem that has no nonterminal run and is rejected otherwise. They are
distinct operations.

The existing message-start endpoint is the single entry point for Ask, Build,
and Continue. An ephemeral Start-task action resubmits Build with the original
`rootMessageID`; the server attaches that message to the new WorkItem
idempotently.

Existing permission/input resume endpoints remain exact-run operations.

Every Build, Continue, Resume, Stop, and Cancel requires `clientRequestID`.
Idempotency binds actor, Project UID, operation, WorkItem/run identity, and
canonical request digest. Reusing the ID with different input returns conflict.

## Portal Contract

- The composer exposes explicit **Ask** and **Build** submission intent.
- Ask remains the default.
- An Ask response may expose an ephemeral **Start task** action without
  persisting a proposed WorkItem.
- The plan dock renders only plan metadata from the selected WorkItem's active
  run, or its latest run when suspended.
- Suspended tasks show **Continue** and **Discard**.
- Pending permission/input shows exact **Resume** and **Stop** controls.
- A run in `stopping` shows a noninteractive **Stopping** state and blocks new
  Ask, Build, Continue, Resume, and Discard actions.
- A status discussion may select a WorkItem as read-only context without
  exposing mutation controls.
- Starting unrelated work never silently resumes or merges another task.

## Security and Tenancy

- All current-state keys and queries include tenant scope and Project UID.
- The authenticated actor is recorded server-side; client identity is ignored.
- V1 permits only the creator to Continue, Resume, approve, Stop, or Cancel a
  WorkItem.
- Missing or unstable actor identity denies mutation.
- Paths are canonicalized before capability checks.
- Tool authorization is checked immediately before the side effect.
- Narrative plan/grant fields use existing encryption-at-rest behavior.
- Portal snapshots contain no credentials, raw source, tool arguments, tool
  results, prompts, or unsanitized errors.
- A WorkItem ID, run ID, request ID, or checkpoint ID is not a bearer
  capability.

## Deployment Assumption

This design targets a net-new deployment with no pre-existing App Studio
conversation, run, checkpoint, grant, or workspace data.

The initial schema creates messages, WorkItems, and AssistantRuns in final form
with non-null Project UID and only WorkItem-scoped cross-run grants.

## Alternatives Considered

### Router and prompt changes only

Classify only the current message and clear the project grant more often.

This is useful containment but still lets a classifier attach authority to a
misread status question. It also provides no explicit continuation identity.

### Lean AssistantWorkItem

This is the selected design. One durable task row owns cross-run authority while
messages, runs, plan metadata, and Eino checkpoints keep their existing
responsibilities.

### Full transactional workflow model

Add separate Turn, ApprovalGrant, WorkItemProgress, and WorkspaceState entities,
whole-workspace fingerprints, and a generalized external-action fence.

That model provides stronger workflow and crash-coordination guarantees, but
those guarantees are broader than the confirmed context/authority leak. It
increases schema, lifecycle, UI, and operational complexity without being
required for task isolation.

## Delivery Phases

### Phase 1: Durable isolation

- Add Project UID and WorkItem ID to message/run scope.
- Add AssistantWorkItem storage, encryption, CAS, and one-active-item index.
- Add atomic WorkItem/run creation and transition methods.
- Scope cross-run plan grants to WorkItem.
- Load mutation context only by WorkItem ID.
- Make Ask/Build/Continue explicit and preserve the existing targeted Resume
  wiring.
- Remove recent-history escalation and project-wide grant loading.

### Phase 2: Portal and execution control

- Add Ask/Build composer intent and WorkItem list/actions.
- Scope the plan dock to the active WorkItem/run.
- Retain the active TurnLoop control handle and implement graceful,
  grace-period-bounded Stop, asynchronous `stopping`, and terminal checkpoint
  suppression.
- Enforce actor, Project UID, grant revision, capability/path, HEAD, and target
  digest at the tool boundary.

No later Eino workflow or external-action phase is required to complete this
design.

## Acceptance Criteria

- A failed quote-submission task cannot place quote messages, todos, or
  authority into a later theme-status Ask.
- Ask without a selected WorkItem has no task context, grant, todos, or mutation
  tools.
- A classifier cannot upgrade Ask to Build.
- An Ask that references a WorkItem is still denied by the mutation validator
  even if a mutation tool is dispatched accidentally.
- Run mode is immutable; attempts to change discussion into `new` or `continue`
  after creation return conflict.
- Build creates a new WorkItem rooted in the explicit current request.
- Continue loads only the selected WorkItem and starts a fresh Eino run.
- Exact Resume restores only the selected pending run/checkpoint.
- A terminal run's checkpoint and run-local todos never enter a continuation
  run.
- A run cannot use a grant owned by another WorkItem.
- A stale or terminal run cannot use a grant installed for the WorkItem's
  current run.
- Exact approval resumes one claimed pending run and one named Eino interrupt
  target only. A different interrupt, an already-claimed run, and a later
  Continue run are denied.
- Reusing an exact-resume `clientRequestID` with different edited arguments
  conflicts, and the final edited arguments pass normal tool and target
  validation.
- Failure, Stop, cancellation, completion, commit, or supersession leaves no
  reusable WorkItem grant.
- Pending permission/input blocks another run until exact Resume or Stop.
- Project deletion and recreation under the same name cannot load state from
  the old Project UID.
- Actor, Project UID, grant revision, capability/path, HEAD, and target-digest
  mismatches deny mutation before the side effect.
- Stop during model or tool execution enters `stopping`, requests an Eino safe
  point, revokes the plan grant, and permits only an already-admitted call to
  return.
- The admission gate proves a tool was admitted before Stop or is denied. An
  admitted call may execute afterward, but no new call can be authorized.
- A slow or non-cooperative execution returns `202 Accepted`; the Eino grace
  timeout is not represented as a total Stop deadline.
- Natural completion or failure racing Stop retains its real outcome; only a
  contributing Stop suspends the WorkItem.
- Stop recorded before `loop.Run` is classified as the winning outcome when no
  natural result exists.
- Concurrent Stop and Resume produce one winner: Stop either terminalizes the
  pending run or follows the already-claimed running run.
- Every terminal path clears `active_run_id` and leaves no resumable terminal
  checkpoint.
- Stop timeout escalation does not claim that an unknown external side effect
  was rolled back.
- Mutating tool wrappers apply server-owned deadlines and cancellation; a
  non-cooperative tool leaves the project fail-closed in `stopping` until
  execution exits or provider-restart recovery runs.
- Root-message activation is a one-time same-actor CAS; concurrent activation
  and cross-WorkItem relinking return conflict.
- Todos remain plan-dock presentation and cannot authorize continuation or
  mutation.
- Summarization cannot include messages from another WorkItem or influence
  WorkItem selection, tool exposure, or authorization.
- Existing refresh/reconnect, encrypted-store, checkpoint, phase, and plan-dock
  behavior remains intact. Exact approval keeps the existing targeted resume
  path but cannot widen a cross-run grant.

## Verification Matrix

| Layer | Required coverage |
| --- | --- |
| Store | WorkItem CRUD/CAS, atomic create/transition/plan approval/stop, Project UID scope, one-active-item index, immutable message membership and run mode, terminal `active_run_id` clearing, exact-run claim/idempotency, encryption |
| Routing | Ask cannot escalate; Build is explicit; Continue requires selected WorkItem |
| Context | Mutation runs receive only current and WorkItem-linked messages; optional summaries are derived after that filter |
| Authorization | Mutation run mode/status, active run identity, actor, Project UID, plan-grant revision/capability or targeted exact resume, Stop-time revocation, HEAD, target digest |
| Supervisor | Pending conflict, per-project admission-gate ordering, pre-start Stop, repeated Stop, Stop-vs-Resume and Stop-vs-completion races, `stopping` response behavior, terminal WorkItem transition, active TurnLoop control |
| Eino | Fresh continuation run, named-interrupt checkpoint resume, graceful Stop and escalation, `CancelError` outcome classification, terminal checkpoint suppression, mutable session values cannot change authority |
| Tools | Mutating wrappers enforce server-owned execution deadlines and context cancellation; no tool is admitted after `stopping`, while an already-admitted call may complete |
| Progress | Plan metadata is filtered by WorkItem and remains non-authoritative |
| Portal | Ask/Build, Start task, Continue/Discard, exact pending controls, no cross-task plan dock |
| Integration | Reproduce the quote/theme sequence and prove the quote task cannot enter theme Ask or Build |
