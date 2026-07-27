# App Studio Conversation Resilience

## Status

Approved for implementation.

## Problem

App Studio currently treats the initiating HTTP streaming request as the
assistant run. The portal aborts that request when its Vue component unmounts,
the backend derives Eino execution from `r.Context()`, and most assistant text,
status, and tool progress remains in request-local state until the assistant
pauses or finishes.

Navigating away, refreshing, suspending the tab, or temporarily losing network
connectivity can therefore cancel the run and discard its in-progress
conversation state. Returning to the project only reloads the last persisted
messages, so progress often appears all at once when the final message is
eventually saved.

## Decision

Assistant execution will be owned by the App Studio server lifecycle rather
than by an individual HTTP subscriber.

Starting a turn creates durable user, assistant, and run records before
execution begins. The browser receives stable identifiers and subscribes to a
separate reconnectable event stream. Closing that stream only detaches the
subscriber. It does not cancel the run.

The server persists a canonical, revisioned snapshot of the current assistant
message. Every emitted snapshot is complete enough to replace the browser's
current representation. A reconnecting client receives the latest snapshot
immediately and then future revisions; it does not replay every missed
transition.

This first version supports App Studio's enforced single-replica deployment.
It does not introduce Redis, distributed leases, or automatic execution resume
after a provider restart.

## Goals

- Continue assistant work when every browser subscriber disconnects.
- Reconstruct current assistant text, actions, status, and interrupts after
  navigation, refresh, network loss, or tab suspension.
- Avoid duplicate messages when a start request is retried.
- Make the Stop control cancel the server-side run.
- Preserve existing tenant isolation, encryption, action sanitization, Eino
  checkpoint, permission, and input semantics.
- Allow multiple tabs to observe the same run without blocking execution.

## Non-goals

- Automatic model or tool execution recovery after a provider restart.
- Replaying every transient status as an animation.
- Multi-replica execution ownership.
- Making the pre-project naming and repository-creation portion of
  `/api/projects/stream` recoverable. Resilience begins once the Project and
  first user message exist.
- Replacing Eino's TurnLoop or checkpoint mechanism.

## Global Constraints

- Work only under `providers/app-studio` plus this design document.
- Extend existing store, run-manager, Eino callback, and message-view seams;
  do not add a new service, framework, or external broker.
- A request or subscriber context must never own assistant execution.
- Persist before publishing every snapshot revision.
- Store only sanitized user-visible action metadata. Never persist bearer
  tokens, HTTP requests, raw tool arguments/results, credentials, or
  unsanitized model errors.
- Preserve message-content and checkpoint/audit encryption through the
  encrypted store wrapper.
- Keep one nonterminal run per tenant/project. A concurrent start returns
  conflict instead of preempting the active run.
- Use test-driven development: every behavior change starts with a failing
  test that demonstrates the missing behavior.
- Keep the legacy POST streaming route as an adapter during this change.
- The deployment remains single-replica.

## Durable Model

Extend `store.AssistantRun` with:

- `ClientRequestID`, a client-generated UUID used for idempotent submission.
- `ActiveMessageID`, the assistant message receiving the current segment.
- `Revision`, a monotonically increasing snapshot revision.
- Terminal `failed` and `interrupted` statuses.

The current assistant message remains the durable display snapshot. Its
metadata stores the run ID, revision, current user-facing status, provisional
marker, existing sanitized `assistantActions`, existing `assistantInterrupt`,
and whether the development preview needs refresh.

Add atomic store operations for:

1. Creating a user message, assistant placeholder, and run.
2. Saving a revisioned run plus every assistant message affected by that
   transition.
3. Finding a run by client request ID.
4. Loading the latest run for a project.

Postgres receives an additive schema migration and scoped indexes for client
request idempotency and one nonterminal run. Memory and encrypted stores must
provide the same behavior.

## HTTP and Stream Contract

### Start

`POST /api/projects/{project}/messages`

```json
{
  "content": "Add authentication",
  "clientRequestID": "uuid"
}
```

Returns `202 Accepted` with persisted user and assistant messages plus:

```json
{
  "run": {
    "id": "run-id",
    "status": "running",
    "revision": 1,
    "activeMessageID": "message-id",
    "updatedAt": "timestamp"
  }
}
```

Repeating the same scoped `clientRequestID` returns the existing run and does
not append another user message. A different request while a run is
nonterminal returns `409 Conflict` with that run's current snapshot.

### Recover

`GET /api/projects/{project}/assistant/runs/latest`

Returns the latest run snapshot, including terminal state, or `204 No Content`.
Returning terminal snapshots avoids a race when a run completes between
loading message history and checking active state.

### Subscribe

`GET /api/projects/{project}/assistant/{run}/stream?afterRevision=N`

The stream uses complete snapshots:

```text
id: 12
event: snapshot
data: {...}
```

- Send the latest complete snapshot immediately.
- Accept `Last-Event-ID` and `afterRevision` for deduplication.
- Send the current snapshot when revisions were missed.
- Send keepalive comments every 15 seconds.
- Close after a terminal snapshot.
- Use a capacity-one subscriber channel that replaces stale queued snapshots.

Existing resume and abort URLs remain. Resume becomes asynchronous and returns
`202` with the newly running snapshot. Abort cancels the actual server worker
and persists `aborted`.

The existing POST streaming route remains an adapter over the new supervisor
for compatibility. The portal uses the start-and-subscribe contract.

## Execution Semantics

The existing project run manager becomes a supervisor keyed by organization,
workspace, and project. It owns:

- A context derived from the provider lifecycle.
- An explicit cancellation function.
- The current snapshot and revision.
- Coalescing subscribers.
- The current Eino execution segment.

The snapshot accumulator persists semantic milestones immediately: status
changes, tool start/finish, permission/input interrupts, provisional resets,
preview-refresh changes, and terminal state. Text chunks are coalesced to at
most one persistence write every 250 milliseconds. It flushes before semantic
and terminal transitions.

Persistence failure terminates further orchestration, attempts a detached
`failed` snapshot, and logs the failure. Subscriber write failure only removes
that subscriber.

A provider shutdown cancels workers and flushes `interrupted` within the
existing shutdown window. After a hard restart, a persisted `running` row with
no in-memory supervisor is reconciled to `interrupted` when the project is next
read or a new run is started. Pending permission/input runs remain resumable
because their Eino checkpoint is already durable.

## Portal Semantics

Extract snapshot merge and revision handling from `App.vue` into a small
testable module.

On project entry:

1. Load persisted messages and the latest run snapshot.
2. Merge the snapshot by stable assistant message ID.
3. Subscribe from its revision when nonterminal.
4. Ignore duplicate or older revisions.

On submit:

1. Generate a client request UUID.
2. Render the optimistic user message.
3. Start the run and replace optimistic records with persisted records.
4. Subscribe using the returned run ID and revision.

Unmount, route change, and tab suspension abort only the subscription. On
`focus`, `pageshow`, or `online`, reload the latest snapshot and reconnect with
delays of 1, 2, 4, 8, then at most 10 seconds. Stop calls the abort endpoint
before disconnecting.

## Failure Semantics

| Event | Result |
| --- | --- |
| Navigate, refresh, or lose network | Run continues; latest snapshot restores the UI. |
| Slow subscriber | Queued snapshots coalesce; execution is unaffected. |
| Explicit Stop | Server execution is cancelled and persisted `aborted`. |
| Permission/input pause | Snapshot and Eino checkpoint remain resumable. |
| Graceful provider shutdown | Running work becomes `interrupted`. |
| Provider crash/restart | Stale `running` state becomes `interrupted` on access. |
| Persistence failure | Run becomes `failed`; orchestration stops. |
| Duplicate submission | Existing run is returned. |
| Concurrent new submission | Returns `409` with current run. |

## Migration

- Existing completed and aborted rows remain readable with defaulted columns.
- Legacy running rows are treated as interrupted.
- Legacy pending permission/input rows derive their active message from
  existing interrupt metadata on first access and persist the association.
- The provider binary and embedded portal deploy together.
- Existing message retention also applies to run snapshots.

## Task 1: Durable store contract

Implement the additive AssistantRun fields, statuses, schema migration,
idempotency and active-run indexes, atomic create/save operations, latest and
client-request lookup, and parity across Postgres, memory, and encrypted stores.

Tests must first demonstrate atomic creation, duplicate request recovery,
single-active-run conflict, monotonic revision compare-and-swap, latest-run
lookup, encryption, and retention/deletion behavior.

## Task 2: Server-owned supervisor and recovery API

Refactor the run manager into a server-lifecycle-owned supervisor and durable
snapshot accumulator. Add start, latest, reconnectable SSE, asynchronous
resume, true abort, orphan reconciliation, and legacy stream adapter behavior.
Route current Eino callbacks through persist-before-publish snapshots while
retaining checkpoint and action-sanitization behavior.

Tests must first demonstrate that cancelling the initiating/subscriber request
does not cancel execution; reconnect receives the latest and future revisions;
slow subscribers do not block; explicit abort cancels; checkpoints survive;
orphaned runs become interrupted; persistence failures are failed rather than
completed; and tenant scopes cannot cross-subscribe.

## Task 3: Portal recovery and reconnection

Add the pure snapshot reducer, API types/client calls, project-entry recovery,
submission idempotency, reconnect loop, multiple-tab-safe merge, and true Stop
behavior. Unmount must disconnect only the subscription.

Tests must first demonstrate stable-ID merge, old/duplicate revision rejection,
reconstruction after route/reload, reconnect backoff, no duplicate optimistic
messages, and abort-before-disconnect behavior.

## Task 4: Integration and compatibility

Connect the first-project flow to the supervisor after the Project and first
message exist, retain the legacy streaming adapter, add structured lifecycle
logs without sensitive content, and update App Studio documentation.

Run all provider Go tests, every portal Node test, portal typecheck/build,
focused race tests for supervisor/store packages, and a Tilt browser scenario
that navigates away during a tool action and observes current action state and
the eventual final response on return.

## Acceptance Criteria

- Cancelling the originating browser request does not cancel assistant work.
- Returning to a project reconstructs current assistant content and accumulated
  action history from one latest-snapshot response.
- Live updates continue without resubmitting the prompt.
- Retries and reconnects do not duplicate user or assistant messages.
- Stop terminates backend work.
- Permission/input prompts remain actionable after refresh.
- A provider restart produces an explicit `interrupted` state rather than an
  indefinitely running spinner.
