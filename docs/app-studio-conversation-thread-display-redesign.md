# App Studio Conversation Thread Display Redesign

## Proposal

Keep App Studio's Replit-inspired model of visible progress, reviewable plans,
and meaningful milestones, but make the conversation itself feel lighter and
more conversational:

1. keep the current collapsed action summary;
2. replace the expanded action cards with a compact work log;
3. replace the full-width plan dock with a floating progress capsule and
   checklist card; and
4. replace raw tool-call disclosure with a product-facing action presentation
   contract.

This is not only a visual-density change. The current backend and portal
intentionally expose summarized tool arguments, including details such as file
read offsets and limits. The redesign should change that presentation boundary
so normal users see what App Studio accomplished, what it affected, and whether
they need to act.

## Current State

### What works

- The collapsed action summary is compact and gives useful confidence that App
  Studio is doing real work.
- Actions already have useful semantic kinds and statuses.
- Many tools already produce good user-facing labels such as `Read
  src/App.vue`, `Updated src/App.vue`, and `Checked development preview`.
- The assistant plan is already validated, sanitized, persisted, streamed, and
  recoverable after refresh. No new plan transport is needed.
- Approval and clarification requests are already separate, prominent
  interaction surfaces.

### What does not work

- Expanding an action summary renders every call as a bordered card with a
  large status icon, tool badge, role label, and multiline monospace detail.
  Long runs become a second transcript inside the conversation.
- The active plan dock consumes a full horizontal row above the composer even
  when collapsed.
- The expanded action detail concatenates `arguments` and `detail`. For a file
  read, the argument summarizer includes `path`, `offset`, and `limit`, even
  though only the path normally matters to the user.
- Tool names, transport-shaped arguments, and implementation-oriented role
  labels compete visually with the user-facing action label.
- The UI counts raw tool calls. A run that inspects many files can therefore
  look more complicated than the user-visible work actually was.
- Failed and rejected metadata actions currently pass through the generic
  `tool result` role and can be projected as completed with a green check.
- `write_todos` can appear as generic tool activity even though its meaningful
  user-facing representation is the separate plan UI.
- The trace merges persisted `assistantActions` with A2UI tool cards.
  Both paths can contribute rows and technical detail, so redesigning only one
  would leave duplication and inconsistent status handling.

## Design Principles

### Keep the conversation primary

The assistant's prose, questions, approvals, and final result are the main
thread. Execution activity should provide ambient confidence without becoming
the dominant reading experience.

### Show product events, not execution mechanics

Every normal action row should answer:

- What did App Studio do?
- What project object did it affect?
- What was the outcome?
- Does the user need to do anything?

It should not answer how the tool protocol encoded the request unless the user
explicitly enters a diagnostic view.

### Use progressive disclosure

There are three levels:

1. **Glance:** a collapsed action summary or plan progress capsule.
2. **Review:** a compact work log or checklist with human-readable outcomes.
3. **Diagnose:** separately disclosed, sanitized technical information for
   failures and support workflows.

Expanding level two must not automatically expose level three.

### Preserve Replit's product model

The Replit inspiration remains visible in the concepts that matter for this
surface: an agent working in a project, a reviewable plan, visible progress,
approvals, and meaningful outcomes. Codex-like compression should shape how
routine execution is displayed, without turning App Studio into a terminal or
generic chat client.

This interpretation follows Replit's documented separation of planning from
building and its emphasis on checkpoints as meaningful review/rollback moments:

- [Plan Mode vs. Build Mode](https://docs.replit.com/learn/plan-vs-build-mode)
- [Checkpoints and Rollbacks](https://docs.replit.com/features/version-control/checkpoints-and-rollbacks)

## Proposed Experience

### 1. Compact action summary and work log

Keep the current collapsed control in each assistant turn:

```text
[✓ ✓ ◌] 5 updates · Read 6 files · updated 2 · checks passed
```

The summary should describe grouped outcomes rather than list the first three
raw calls. Its numeric total counts the user-visible work-log entries after
semantic grouping, not source tool calls or the number of affected resources.
For example, six adjacent file reads contribute one `Read 6 project files`
entry and one update to the total. It remains collapsed by default.

When expanded, render a dense, single-column work log:

```text
✓  Read 6 project files
✓  Found 3 matching references
✓  Updated src/App.vue
✓  Updated src/style.css
✓  Checked development preview                         Ready
```

Recommended behavior:

- Use 28–32 px visual rows rather than a card per action.
- Use one small status icon, one human label, and an optional terse outcome.
- Remove tool-name badges and `tool call` / `tool result` role labels.
- Avoid a border and separate background on every successful row.
- Constrain the expanded log to `min(40vh, 320px)` and scroll internally.
- Keep errors, waiting states, approvals, and commit outcomes visually
  prominent.
- Map failed and rejected actions to error/rejected presentation rather than a
  completed state.
- Let a row receive focus or be clicked only when it has useful review detail.
  Interactive rows must still expose at least a 44 px hit target even if their
  visual content is denser.
- Group adjacent low-value actions where the result is clearer as a set:
  repeated reads become `Read 6 project files`; repeated edits may become
  `Updated 3 files`.
- Never group an error, approval, user-input request, or commit milestone into
  a routine aggregate.
- Suppress `write_todos` as a work-log row because its state is already
  represented by the plan capsule.

The expanded log is reviewable history, not a live console. It updates in place
while a run is active and remains readable after completion.

### 2. Floating plan progress

Replace the full-width `AssistantPlanDock` bar with a small capsule anchored
inside the chat pane just above the composer:

```text
[Checklist] 2 of 5 · Updating preview
```

The capsule:

- is positioned at the lower-right of the transcript on desktop;
- occupies only its intrinsic width;
- shows completed count, total count, and a bounded active-step label;
- remains visible only for the active streaming plan;
- does not replace approvals, clarifications, or the send controls; and
- leaves enough bottom padding in the transcript that the collapsed capsule
  does not cover the last message.

Hovering the capsule with a fine pointer, focusing it with the keyboard, or
tapping it opens a floating checklist above it:

```text
┌ 2 of 5 steps ──────────────────────────┐
│ ✓ Inspect the current quote flow       │
│ ✓ Add the submission endpoint          │
│ ◌ Add the quote form                    │
│ □ Update category and list state       │
│ □ Verify the development preview       │
└─────────────────────────────────────────┘
```

Interaction details:

- Hover opens the card after a short delay and closes it after a forgiving
  delay so the pointer can move from capsule to card.
- Focus opens it. `Escape` closes it and restores focus.
- Click pins or unpins it so the card can be inspected without maintaining
  hover.
- Outside click closes a pinned card.
- Touch uses tap, never hover emulation.
- The card is about 280–320 px wide, has a bounded height, opens upward, and
  scrolls internally for long plans.
- Step status is communicated by text available to assistive technology, not
  color alone.
- The capsule and every interactive checklist control provide at least a 44 px
  pointer target.
- Only active-step changes and terminal state changes are announced through
  `aria-live`; every streamed action should not be announced.
- Reduced-motion preferences disable the spinning/pulsing treatment.

On narrow screens, put the capsule in the composer toolbar and open the
checklist as a compact bottom sheet. A hover-only interaction is not acceptable.

When the run finishes, remove the floating capsule. If the completed plan is
useful history, add a terse marker to the assistant turn such as `5 of 5 steps
completed`; do not restore the full checklist as a permanent horizontal bar.

### 3. Product-facing action details

The normal action model should no longer treat tool name, summarized arguments,
and summarized output as presentation-ready strings.

Introduce a presentation-oriented action shape generated by a per-tool
presenter:

```text
kind         inspect | edit | run | commit | plan | clarify | other
status       running | waiting | succeeded | failed | rejected
title        human-readable action
target       optional user-recognizable resource or path
outcome      optional terse result
count        optional grouped item count
severity     normal | attention | error
diagnostic   optional structured support reference
```

Audit records retain their existing sanitized fields and redaction guarantees;
this redesign does not make audit storage more verbose. Internal execution
state remains governed by its existing retention and security policies. The
user-facing snapshot contains only the presentation fields appropriate to the
configured disclosure policy.

Per-tool presenters belong in the backend, next to the existing action
classification and safe summarizers. This keeps tool knowledge and the
security/disclosure boundary out of Vue. The portal owns grouping, density,
popover behavior, and responsive layout.

#### Display policy

| Action class | Normal title/outcome | Hide from normal view |
|---|---|---|
| Read file | `Read src/App.vue` | offset, limit, byte ranges, tool slug |
| List/glob/search | `Read 6 project files` or `Found 3 references` | glob flags, output mode, head limit |
| Edit/create | `Updated src/App.vue` | patch syntax, replacement flags, content size |
| Run/check | `Checked development preview · Ready` | endpoint routes, provider parameters, protocol payload |
| Runtime logs | `Reviewed 80 log lines` or a safe blocker | raw logs unless explicitly requested elsewhere |
| Environment | `Updated 3 environment variables` | all values; names only when safe and useful |
| Commit | `Committed 2 files` with an optional short commit reference | repository plumbing and raw request payload |
| Todo update | represented by the plan capsule | the `write_todos` tool call itself |
| Approval/input | the existing dedicated interrupt surface | duplicate trace row unless needed as history |
| Unknown/provider tool | server-owned safe generic label | title-cased internal tool name and raw arguments |

#### Failure details

Failures need more context, but should still lead with a product explanation:

```text
! Preview check failed
  The development server did not become ready.
```

An optional `Technical details` disclosure may show only a structured,
allowlisted diagnostic object:

```text
category      timeout | permission | validation | runtime | provider | unknown
message       bounded server-owned user message selected by category
referenceID   opaque run or action identifier
```

It must not accept arbitrary provider- or presenter-supplied diagnostic text.
Unknown failures use a generic message. Raw errors, arguments, source contents,
secrets, credentials, and chain-of-thought never enter the user-facing feed or
the persisted audit record. Existing audit redaction, retention, and
secret-handling guarantees remain unchanged. A `Copy diagnostic info` action
can copy the three safe fields for issue reports without making diagnostics
part of the default reading experience.

Technical detail should be available for failed or support-relevant actions,
not repeated on every successful read.

## Architecture and Ownership

### Backend

Refine the presentation boundary in
`providers/app-studio/api/assistant_ui_events.go`:

- replace the assumption that `Tool`, `Arguments`, and `Detail` are directly
  user-facing;
- add per-tool presentation mapping;
- keep the current minimal-disclosure mode;
- ensure unknown tools fail closed to a safe generic presentation; and
- keep raw execution data in audit/run records rather than message UI metadata.

Make the new action presentation feed the sole source of action-log rows. Delete
the existing A2UI `tool call` and `tool result` card path in the same change:
the backend stops emitting those cards and the portal stops consuming them.
A2UI remains available for assistant response content and interrupt surfaces.
This avoids duplicate counts, duplicate rows, and a second path that can
reintroduce tool slugs or technical detail.

The generic summarizers in `providers/app-studio/api/llm.go` can continue to
serve model context, audit, and diagnostics. User-visible presenters should
select only the subset that has product meaning. In particular, file-read
offset and limit should not enter the normal UI action.

### Portal

Refactor the conversation-specific presentation code out of
`providers/app-studio/portal/src/App.vue`:

- a pure action-presentation/grouping module;
- a compact `AssistantActionLog` component; and
- a floating `AssistantPlanPopover` component replacing
  `AssistantPlanDock`.

This is component extraction for a bounded conversation surface, not a new
shared framework or cross-provider abstraction.

The portal must stop concatenating `action.arguments` and `action.detail`.
Diagnostics, if present, get an independent disclosure state from the work-log
expansion state.

The portal derives semantic status only from the new feed. Failed and rejected
actions must never fall through to a generic completed state.

### Fresh contract

- Define `assistantActionFeed` as the only action-presentation contract. It
  contains the new structured action items directly, with no version
  discriminator or parallel schema.
- Stop reading and writing the old `assistantActions` metadata.
- Stop emitting and consuming A2UI tool-call and tool-result cards.
- There is no requirement to render historical action traces stored in an old
  format.
- Deploy the backend and App Studio portal together as one contract change.
- Keep the existing `assistantPlan` snapshot contract unchanged.
- Preserve refresh/reconnect behavior and revision ordering.
- Preserve `APP_STUDIO_TOOL_DISCLOSURE=minimal`.

## Implementation Plan

Implement this as one coordinated backend-and-portal contract change:

1. Add backend per-tool presenters and write only the new
   `assistantActionFeed`.
2. Remove the old `assistantActions` metadata and A2UI tool-call/result paths.
3. Build `AssistantActionLog` against only the new structured feed, including
   semantic grouping, collapsed outcome summaries, and correct failure states.
4. Replace `AssistantPlanDock` with the accessible floating
   `AssistantPlanPopover`.
5. Add allowlisted structured failure diagnostics and a copyable run
   reference.
6. Remove obsolete trace parsing, card rendering, and tests rather than
   retaining parallel code paths.
7. Tune maximum heights, animation, hover delays, and mobile behavior through
   live use.

## Acceptance Criteria

### Action log

- A 20-action successful run expands into a bounded, scrollable log whose rows
  are no taller than 32 px unless a row contains an error requiring explanation.
- Successful rows show no tool slug, raw argument string, role label, read
  offset, read limit, protocol payload, or raw command.
- Errors, approvals, and waiting states remain obvious without expanding a
  diagnostic disclosure.
- The collapsed summary remains at least as informative as it is today.

### Plan progress

- The active plan consumes no full-width horizontal bar.
- Desktop users can open the checklist by hover, focus, or click.
- Keyboard and touch users can perform the same review without hover.
- The checklist never covers the composer controls.
- Stream revisions and reconnects update the open card without resetting its
  pinned state.
- Terminal runs remove the floating control.
- A stable, always-mounted offscreen `aria-live` region announces meaningful
  active-step and terminal changes even when the visible capsule unmounts.
- The mobile checklist uses dialog semantics, moves initial focus into the
  sheet, traps focus, makes the background inert, locks background scroll,
  closes on `Escape`, and restores focus to the capsule.

### Safety and resilience

- Raw tool arguments and results never become visible through the normal work
  log.
- Unknown tools fail closed to generic safe labels.
- `assistantActionFeed` is the only source of action rows; the displayed total
  counts the rows that remain after portal-side semantic grouping.
- Failed and rejected actions never render as successful or completed.
- Plan updates do not appear as duplicate generic tool actions.
- Secret-redaction and disclosure-mode tests continue to pass.
- Conversation refresh, reconnection, approval, clarification, and abort flows
  behave as before.

### Validation

- Focused unit tests for the new action feed, presentation, grouping, and plan
  popover state.
- Vue typecheck and production portal build.
- Existing assistant progress, assistant plan, plan dock replacement, and
  conversation resilience suites.
- App Studio provider tests covering disclosure modes and per-tool presenters.
- Browser verification at desktop and narrow widths with mouse, keyboard, and
  touch emulation.
- Independent review for correctness, information disclosure, accessibility,
  and regressions.

## Non-goals

- Changing how the assistant chooses or updates todos.
- Exposing reasoning, prompts, raw model events, or raw tool payloads.
- Making the plan editable or retaining plan history across runs.
- Replacing audit logs with the user-facing work log.
- Adding checkpoint history or rollback controls to the conversation trace.
- Defining provider-supplied or localized display metadata for arbitrary tools;
  unknown tools use a server-owned generic fallback in this proposal.
- Redesigning approvals, clarification prompts, or the rest of the App Studio
  workbench.
- Introducing a new UI framework or cross-provider component system.

## Decisions to Confirm Before Implementation

The recommended defaults are:

- completed plans leave a terse completion marker in the assistant turn;
- technical details are available only for failures and support-relevant
  events;
- consecutive routine reads and edits are grouped;
- mobile uses a bottom sheet for the checklist; and
- unknown and provider tools use a server-owned generic safe label.

These choices are independently adjustable, but none requires a change to the
assistant execution or plan-generation model.
