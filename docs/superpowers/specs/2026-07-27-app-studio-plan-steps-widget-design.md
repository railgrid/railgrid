# App Studio Plan Steps Widget Design

## Goal

Let users understand the named steps behind assistant progress such as
`0 of 5 steps` without turning the chat into a verbose tool log or exposing raw
model, tool, prompt, or reasoning payloads.

The plan steps widget is a user-facing progress view. It remains distinct from
the existing expandable action trace, which explains the technical tools and
results involved in carrying out those steps.

## Approved behavior

1. Show a compact plan summary on any assistant message with structured plan
   progress, including while it is running and after it completes.
2. Keep the plan collapsed by default.
3. Clicking the summary expands the complete ordered list of step names and
   statuses; clicking it again collapses the list.
4. Render `completed`, `in_progress`, and `pending` states with the existing
   App Studio status icons and design tokens.
5. Update the open widget in place as new progress snapshots arrive.
6. Persist the latest safe plan snapshot so refresh and stream reconnection do
   not lose the step list.
7. Keep the existing technical action trace separate and independently
   expandable.
8. Preserve count-only behavior when `APP_STUDIO_TOOL_DISCLOSURE=minimal`.

## Current behavior and constraint

Deep Agent's `write_todos` tool supplies an ordered list shaped as:

```text
todos: [{ content, activeForm, status }]
```

The Eino phase middleware currently validates that list, derives an active
label and completed/total count, then emits only a string through `OnStatus`.
The structured list is discarded. The normal action-disclosure path also
intentionally drops the raw `write_todos` JSON because that tool has no safe
argument summarizer.

The portal therefore cannot reconstruct the five steps safely. Parsing action
arguments in the browser would weaken the existing disclosure boundary, and
using the approved mutation plan would not provide live per-step status.

## Architecture

### Typed plan snapshot

Add a private assistant plan snapshot type containing an ordered, bounded list:

```text
plan:
  steps:
    - content: "Inspect the current quote form"
      activeForm: "Inspecting the current quote form"
      status: completed
```

After a successful, phase-authorized `write_todos` call, the phase middleware
will parse and validate the payload once. In summary disclosure mode it will:

1. sanitize and bound `content` and `activeForm` with the same safe-text
   treatment used by the current progress label;
2. derive the existing human-readable status string;
3. emit the typed snapshot through a dedicated internal callback; and
4. emit the existing `OnStatus` value for compatibility.

The callback is internal to the App Studio assistant run pipeline. It is not a
new public endpoint or a raw Eino event.

The assistant instruction will require concise, user-facing step descriptions
and prohibit secrets, raw file contents, prompts, or internal reasoning in todo
labels. Backend normalization and known-secret redaction remain defense in
depth; they do not attempt to semantically classify arbitrary model text.

In minimal disclosure mode, the middleware will continue emitting only the
count status and will not emit named plan steps.

### Persistence and streaming

The assistant worker will retain the latest plan snapshot in its run-local
metadata state. The existing snapshot persistence path will write it to the
active assistant message as `assistantPlan`, alongside `assistantStatus` and
`assistantActions`.

Every metadata reconstruction path must explicitly carry the current
`assistantPlan`, including the normal transition constructor, the
preserve-existing-metadata constructor, abort, resume/claim, failure, orphan
interrupt, checkpoint, and terminal transitions. This prevents a later state
change from erasing a successfully persisted plan.

Existing latest-snapshot responses and the snapshot SSE used by the current
portal already transmit message metadata, so no new transport is required for
this UI. A plan callback persists and publishes a new snapshot using the same
revision ordering rules as status and action updates.

The retained legacy event SSE selectively adapts known fields rather than
streaming message metadata. Adding a new legacy plan event is out of scope; its
consumers continue receiving the existing count status. This feature targets
the snapshot stream used by the App Studio portal.

Only the latest complete plan snapshot is authoritative. A later `write_todos`
call replaces the prior list atomically; the backend does not merge individual
steps or retain plan history.

### Portal model

The portal will validate `metadata.assistantPlan` before adopting it:

- `steps` must be an array;
- each step must have a non-empty `content`;
- status must be `pending`, `in_progress`, or `completed`; and
- optional `activeForm` must be a string.

The portal will independently enforce the backend's 50-step maximum and
display-label byte limit before rendering persisted `unknown` metadata.

Invalid plan metadata is ignored. The current `assistantStatus` working label
remains the fallback, so older messages and mixed-version deployments continue
to render as they do today.

### Disclosure interaction

An assistant message with plan metadata will render a compact button above its
response content and separately from the action trace:

```text
▸ 0 of 5 steps · Adding the quote form
```

The button will use `aria-expanded`, `aria-controls`, a stable panel ID, and the
existing focus-visible treatment. Expanded content will be an ordered list:

```text
✓ Inspect the current quote flow
◌ Add the submission endpoint
◌ Add the quote form
◌ Update the category and list state
◌ Verify the development preview
```

State presentation:

- `completed`: success-colored check icon;
- `in_progress`: accent-colored animated loader, with `activeForm` available as
  the current activity label;
- `pending`: muted square or circle icon.

The widget stays collapsed on first appearance. User expansion state is kept by
message ID while live snapshots replace the plan contents, so progress updates
do not unexpectedly close it. Clicking the same summary toggles it closed.

The existing action trace keeps its own expansion state and remains collapsed
by default. No new shared accordion abstraction or dependency is introduced.

## Safety and data bounds

- Never persist or return raw `write_todos` JSON.
- Accept only the documented fields and statuses.
- Retain the existing maximum of 50 steps and 64 KiB input.
- Normalize whitespace, apply the existing known-secret redactor, require
  non-empty sanitized step content, and bound every displayed label.
- Persist no arbitrary extra fields. Todo labels are bounded model-authored
  user-facing text, in the same disclosure class as assistant prose; the
  redactor is defense in depth, not proof of semantic content.
- Instruct the model not to place prompts, reasoning, tool results, file
  contents, or credentials in todo labels.
- A failed or denied `write_todos` invocation cannot update the plan.
- `write_todos` remains non-authorizing and available only after approval in
  the existing mutate, verify, and repair phases.
- Minimal disclosure remains count-only and does not persist names.

## Failure behavior

- Malformed or unsupported todo payload: emit neither a new status nor a new
  plan snapshot.
- Tool invocation failure: preserve the prior plan snapshot and surface the
  existing tool failure behavior.
- Missing plan metadata: render the current working status only.
- Invalid persisted plan metadata: ignore it without breaking the conversation.
- Stream disconnect or refresh: recover the latest plan from durable message
  metadata.
- Terminal assistant message: retain the final plan snapshot so users can
  inspect the completed or last-known steps.

## Validation

Backend tests will prove:

- a successful authorized `write_todos` call emits matching status and typed
  plan callbacks;
- the plan is sanitized, bounded, ordered, and replacement-based;
- malformed, denied, or failed calls do not update the plan;
- minimal disclosure emits no named plan snapshot;
- assistant snapshots persist and replay `assistantPlan`.

Portal tests will prove:

- valid metadata becomes an ordered plan model;
- invalid statuses and malformed data are rejected;
- completed/total and active labels are derived correctly;
- disclosure expansion is keyed by message and survives live content updates;
- older messages without `assistantPlan` keep their existing fallback.

Verification will include App Studio provider tests, portal tests, typecheck,
build, focused browser interaction in `inspire-me-daily`, and an independent
review of the final diff.

## Scope

In scope:

- App Studio assistant backend callbacks and durable metadata;
- App Studio portal plan parsing and disclosure UI;
- focused tests and live verification.

Out of scope:

- changing how the model chooses or decomposes steps;
- exposing chain-of-thought or raw model events;
- merging plan steps with the technical action trace;
- adding plan editing, reordering, history, or cross-run aggregation;
- changing the existing approval boundary.
