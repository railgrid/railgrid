# App Studio Active Plan Dock Design

## Goal

Move App Studio's assistant plan disclosure out of individual chat messages and
anchor it to the bottom of the chat pane, immediately above the message input.
The result should match the Codex interaction model: the current implementation
plan remains visible while work is active without scrolling the transcript, and
the user can expand or collapse its steps.

## Scope

This is a portal presentation change. The existing backend plan generation,
sanitization, durable metadata, and live snapshot contracts remain unchanged.

## Behavior

- Render one plan dock for the active assistant run.
- Place the dock between the scrollable transcript and the composer form so it
  does not scroll with chat messages.
- Show the dock only when the active run is streaming and its active assistant
  message has a valid parsed plan.
- Remove the dock immediately when the run completes, fails, aborts, or is
  interrupted.
- Do not render historical plan widgets inside assistant messages.
- Keep technical action traces attached to their assistant messages and preserve
  their independent expand/collapse state.
- Preserve the plan dock's expanded/collapsed state across live revisions of the
  same active assistant message.
- When an active run has no valid plan, keep the existing working-status fallback
  visible in the transcript.

## Component Design

`App.vue` will derive an `activeAssistantPlanMessage` from the active run's
`activeMessageID`, the current streaming state, and the validated message plan.
The existing disclosure button and ordered step list will move unchanged into a
`shrink-0` dock immediately before the composer form.

The dock remains keyed by the active assistant message ID. Its disclosure
continues to use `aria-expanded`, `aria-controls`, the existing accessible
status labels, and the existing stable panel ID helper.

The assistant message loop will retain response content, interrupts, timestamps,
and technical traces, but will no longer render plan disclosures.

## Layout

The chat pane already uses a column flex layout:

1. Header
2. Scrollable message transcript
3. Active plan dock, when present
4. Composer and any approval or clarification controls

The dock will use the shared surface, border, text, and semantic status tokens.
It will not use fixed positioning, a portal, or an overlay.

## State and Data Flow

1. A live assistant snapshot replaces the active assistant message by durable ID.
2. The parsed plan on that message updates reactively.
3. The dock updates its summary and steps without changing disclosure state.
4. A terminal snapshot clears streaming state.
5. The active-plan computed value becomes empty and the dock is removed.

Durable plan metadata remains available to the runtime for recovery, but
completed plans are intentionally not rendered in the transcript or above a new
prompt.

## Testing

- Add a focused portal regression test that proves:
  - the plan dock is located after the transcript and before the composer;
  - the message loop no longer renders a per-message plan;
  - the dock is gated by the active streaming plan;
  - plan and technical-trace expansion state remain independent.
- Run the existing assistant plan, assistant progress, and conversation
  resilience tests.
- Run Vue typechecking and a production portal build.
- Verify in the browser that:
  - the dock stays above the composer while the transcript scrolls;
  - expansion and collapse work;
  - live revisions preserve disclosure state;
  - terminal completion removes the dock;
  - the technical trace remains independently usable.

## Non-goals

- Changing plan generation, persistence, validation, or disclosure policy.
- Displaying multiple or historical plans.
- Moving technical action traces into the dock.
- Keeping a completed plan visible after the run ends.
