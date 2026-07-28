# App Studio Plan Steps Widget Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist Deep Agent's sanitized `write_todos` steps and render them in App Studio as a compact, independently expandable plan checklist.

**Architecture:** The Eino phase middleware converts a successful, authorized `write_todos` invocation into an internal typed plan callback plus the existing status callback. The assistant supervisor stores the latest plan in durable message metadata carried by every transition and the current snapshot SSE. The portal validates that unknown metadata through a pure helper and renders it separately from the technical action trace.

**Tech Stack:** Go 1.24, Eino ADK, Vue 3, TypeScript 5.7, Node test runner, Vite.

## Global Constraints

- Never persist or return raw `write_todos` JSON.
- Accept at most 50 ordered steps and only `pending`, `in_progress`, or `completed`.
- Normalize and apply the existing known-secret redactor to `content` and `activeForm`; bound each label to 120 UTF-8 bytes.
- Treat labels as model-authored user-facing prose, not as chain-of-thought or raw tool disclosure.
- Preserve `APP_STUDIO_TOOL_DISCLOSURE=minimal` as count-only with no named plan snapshot.
- A failed, denied, malformed, or phase-ineligible `write_todos` call must not update plan metadata.
- Keep `write_todos` non-authorizing and behind the existing approved mutate/verify/repair phase gate.
- The newest complete plan replaces the previous plan atomically; do not merge steps or retain plan history.
- Carry `assistantPlan` through every metadata reconstruction and terminal transition.
- Target the snapshot SSE used by the App Studio portal; do not add a legacy SSE plan event.
- Keep plan disclosure independent from the existing technical action trace.
- Keep the plan collapsed by default and retain its open state by assistant message ID while snapshots update.
- Use existing design tokens and Lucide icons; add no dependency or shared accordion abstraction.

---

### Task 1: Emit a typed, sanitized plan snapshot

**Files:**
- Modify: `providers/app-studio/api/llm.go`
- Modify: `providers/app-studio/api/assistant_eino_phase.go`
- Modify: `providers/app-studio/api/assistant_eino_phase_test.go`
- Modify: `providers/app-studio/api/assistant_eino_engine.go`
- Test: `providers/app-studio/api/assistant_eino_phase_test.go`

**Interfaces:**
- Produces:

```go
type projectAssistantPlanStep struct {
	Content    string `json:"content"`
	ActiveForm string `json:"activeForm,omitempty"`
	Status     string `json:"status"`
}

type projectAssistantPlanSnapshot struct {
	Steps []projectAssistantPlanStep `json:"steps"`
}

type projectAssistantStreamCallbacks struct {
	// existing callbacks...
	OnPlan func(projectAssistantPlanSnapshot)
}
```

- `projectEinoAssistantTodoProgress` returns one parsed result used for both callbacks:

```go
func projectEinoAssistantTodoProgress(
	argumentsInJSON string,
	includeLabels bool,
) (projectAssistantPlanSnapshot, string)
```

- Consumes the existing `projectEinoAssistantSafeText`,
  `projectEinoAssistantTodoProgressMaxItems`,
  `projectEinoAssistantTodoProgressMaxInputBytes`, and
  `projectEinoAssistantTodoProgressMaxLabelBytes`.

- [ ] **Step 1: Write failing callback and sanitization tests**

Add focused tests that invoke the wrapped `write_todos` endpoint with literal
todos and assert:

```go
wantPlan := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
	{Content: "Inspect files", ActiveForm: "Inspecting files", Status: "completed"},
	{Content: "Update filters token=[REDACTED]", ActiveForm: "Updating filters token=[REDACTED]", Status: "in_progress"},
	{Content: "Verify preview", ActiveForm: "Verifying preview", Status: "pending"},
}}
```

The successful call must emit exactly one matching `OnPlan` value and the
literal status `Updating filters token=[REDACTED] · 1 of 3 steps`. Add
separate cases proving empty sanitized content, malformed JSON, a failed
endpoint, multiple in-progress steps, unsupported status, and more than 50
steps emit no plan. Extend the minimal-disclosure test to assert zero plan
callbacks and `1 of 2 steps complete`.

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
cd providers/app-studio
go test ./api -run 'TestProjectEinoAssistantPhaseWriteTodos' -count=1
```

Expected: compile or assertion failure because `OnPlan` and
`projectAssistantPlanSnapshot` do not exist.

- [ ] **Step 3: Implement the minimal typed parser and callback**

Define the plan types next to `projectAssistantStreamCallbacks`. Refactor the
existing status parser so it validates once, sanitizes both label fields, and
returns a zero plan in minimal mode. After the wrapped endpoint succeeds:

```go
plan, status := projectEinoAssistantTodoProgress(
	argumentsInJSON,
	!projectAssistantToolDisclosureMinimal,
)
if status != "" && m.req.StreamCallbacks.OnStatus != nil {
	m.req.StreamCallbacks.OnStatus(status)
}
if len(plan.Steps) > 0 && m.req.StreamCallbacks.OnPlan != nil {
	m.req.StreamCallbacks.OnPlan(plan)
}
```

Keep callback emission after endpoint success. Update the Deep Agent
instruction to require concise user-facing todo labels and prohibit secrets,
raw file contents, prompts, tool results, and internal reasoning in those
labels.

- [ ] **Step 4: Run focused and engine tests and verify GREEN**

Run:

```bash
cd providers/app-studio
go test ./api -run 'TestProjectEinoAssistantPhaseWriteTodos|TestProjectEinoAssistant.*Instruction' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 1**

```bash
git add providers/app-studio/api/llm.go providers/app-studio/api/assistant_eino_phase.go providers/app-studio/api/assistant_eino_phase_test.go providers/app-studio/api/assistant_eino_engine.go
git commit -m "feat(app-studio): emit sanitized assistant plan steps"
```

---

### Task 2: Persist and preserve the latest plan in assistant snapshots

**Files:**
- Modify: `providers/app-studio/api/assistant_supervisor_http.go`
- Modify: `providers/app-studio/api/assistant_supervisor.go`
- Modify: `providers/app-studio/api/assistant_checkpoint.go`
- Modify: `providers/app-studio/api/assistant_supervisor_http_test.go`
- Modify: `providers/app-studio/api/assistant_supervisor_test.go`
- Test: `providers/app-studio/api/assistant_supervisor_http_test.go`
- Test: `providers/app-studio/api/assistant_supervisor_test.go`

**Interfaces:**
- Consumes `projectAssistantPlanSnapshot` and
  `projectAssistantStreamCallbacks.OnPlan` from Task 1.
- Produces metadata key:

```go
const projectAssistantMetadataPlan = "assistantPlan"
```

- Extend durable state:

```go
type projectAssistantDurableMetadataState struct {
	status      string
	provisional bool
	toolCalls   []projectToolCallStreamEvent
	plan        *projectAssistantPlanSnapshot
}
```

- Extend `projectAssistantDurableMetadataForTransition` with a final
  `plan *projectAssistantPlanSnapshot` argument.

- [ ] **Step 1: Write failing durable-transition tests**

Extend `TestProjectAssistantDurableMetadataTracksEveryTransition` with a
literal two-step plan and assert `metadata["assistantPlan"]` equals that plan.
Add a test that calls `projectAssistantDurableMetadataFromExisting` for
running, interrupted, aborted, failed, claimed, and completed statuses and
asserts each returned map preserves the same `assistantPlan` value.

Add a worker/supervisor test engine that invokes:

```go
req.StreamCallbacks.OnPlan(projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
	{Content: "Inspect project", ActiveForm: "Inspecting project", Status: "completed"},
	{Content: "Verify preview", ActiveForm: "Verifying preview", Status: "in_progress"},
}})
```

Assert both the live subscribed snapshot and the terminal stored message retain
the plan and use monotonically increasing revisions.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
cd providers/app-studio
go test ./api -run 'TestProjectAssistantDurableMetadata|TestProjectAssistantSupervisor.*Plan' -count=1
```

Expected: compile or assertion failure because durable state and constructors do
not carry `assistantPlan`.

- [ ] **Step 3: Implement plan persistence and transition preservation**

Add the metadata key and constructor parameter. Store the plan only when
non-nil and non-empty:

```go
if plan != nil && len(plan.Steps) > 0 {
	metadata[projectAssistantMetadataPlan] = *plan
}
```

In `projectAssistantDurableMetadataFromExisting`, copy the existing plan value
when present. In `persistProjectAssistantDurableMetadata`, pass `state.plan`,
and if the newly built map has no plan, carry the existing message plan before
replacing `message.Metadata`.

Wire `OnPlan` in fresh and resumed workers:

```go
OnPlan: func(plan projectAssistantPlanSnapshot) {
	state.plan = &plan
	recordSnapshotErr(persistMetadata(ctx, nil))
},
```

Use the equivalent `metadataState` callback in checkpoint resume. Update every
constructor call with either the current plan or `nil`. Preserve plan through
all paths that call `projectAssistantDurableMetadataFromExisting`.

- [ ] **Step 4: Run supervisor tests and verify GREEN**

Run:

```bash
cd providers/app-studio
go test ./api -run 'TestProjectAssistantDurableMetadata|TestProjectAssistantSupervisor|TestProjectAssistantSnapshot' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

```bash
git add providers/app-studio/api/assistant_supervisor_http.go providers/app-studio/api/assistant_supervisor.go providers/app-studio/api/assistant_checkpoint.go providers/app-studio/api/assistant_supervisor_http_test.go providers/app-studio/api/assistant_supervisor_test.go
git commit -m "feat(app-studio): persist assistant plan snapshots"
```

---

### Task 3: Validate plan metadata and derive compact progress in the portal

**Files:**
- Create: `providers/app-studio/portal/src/assistantPlan.ts`
- Create: `providers/app-studio/portal/src/assistantPlan.test.mjs`
- Modify: `providers/app-studio/portal/package.json`
- Test: `providers/app-studio/portal/src/assistantPlan.test.mjs`

**Interfaces:**
- Produces:

```ts
export type AssistantPlanStepStatus = 'pending' | 'in_progress' | 'completed'

export interface AssistantPlanStep {
  content: string
  activeForm?: string
  status: AssistantPlanStepStatus
}

export interface AssistantPlan {
  steps: AssistantPlanStep[]
}

export interface AssistantPlanProgress {
  completed: number
  total: number
  activeLabel: string
}

export function parseAssistantPlan(value: unknown): AssistantPlan | undefined
export function assistantPlanProgress(plan: AssistantPlan): AssistantPlanProgress
```

- [ ] **Step 1: Write failing pure-helper tests**

Use the existing TypeScript-transpile test pattern from
`assistantProgress.test.mjs`. Assert the parser:

- accepts a literal ordered three-step plan;
- rejects arrays with zero or more than 50 steps;
- rejects missing/blank content and unsupported statuses;
- rejects labels longer than 120 UTF-8 bytes, including a multi-byte fixture;
- rejects non-string `activeForm`;
- ignores arbitrary extra top-level or step fields by returning only the
  documented fields.

Assert `assistantPlanProgress` returns the literal:

```js
{
  completed: 1,
  total: 3,
  activeLabel: 'Updating the quote form',
}
```

for one completed, one in-progress, and one pending step, preferring
`activeForm` and falling back to `content`.

- [ ] **Step 2: Run the helper test and verify RED**

Run:

```bash
cd providers/app-studio/portal
node --test src/assistantPlan.test.mjs
```

Expected: failure because `assistantPlan.ts` does not exist.

- [ ] **Step 3: Implement the minimal parser and progress helper**

Use `TextEncoder` for UTF-8 byte limits. Validate unknown input without type
casts escaping the checks. Return a new object containing only `content`,
optional `activeForm`, and `status`; never return the original metadata object.
Count completed steps and select the single in-progress step's `activeForm` or
`content`.

Add:

```json
"test:assistant-plan": "node --test src/assistantPlan.test.mjs"
```

to the portal scripts.

- [ ] **Step 4: Run portal helper tests and typecheck and verify GREEN**

Run:

```bash
cd providers/app-studio/portal
npm run test:assistant-plan
npm run test:assistant-progress
npm run typecheck
```

Expected: PASS.

- [ ] **Step 5: Commit Task 3**

```bash
git add providers/app-studio/portal/src/assistantPlan.ts providers/app-studio/portal/src/assistantPlan.test.mjs providers/app-studio/portal/package.json
git commit -m "feat(app-studio): parse assistant plan metadata"
```

---

### Task 4: Render the accessible expandable plan checklist

**Files:**
- Modify: `providers/app-studio/portal/src/App.vue`
- Test: `providers/app-studio/portal/src/assistantPlan.test.mjs`

**Interfaces:**
- Consumes `AssistantPlan`, `parseAssistantPlan`, and `assistantPlanProgress`
  from Task 3.
- Extends `ProjectMessageView` with `plan?: AssistantPlan`.

- [ ] **Step 1: Add failing presentation-helper assertions**

Extend `assistantPlan.ts` with a presentation helper:

```ts
export function assistantPlanSummary(plan: AssistantPlan): string
```

Add tests expecting:

```text
1 of 3 steps · Updating the quote form
3 of 3 steps
```

The second result has no trailing separator when there is no active item.

- [ ] **Step 2: Run the helper test and verify RED**

Run:

```bash
cd providers/app-studio/portal
npm run test:assistant-plan
```

Expected: failure because `assistantPlanSummary` is not exported.

- [ ] **Step 3: Implement the summary and App.vue disclosure**

Import `ChevronRight` plus the Task 3 helpers and type. Add:

```ts
const expandedAssistantPlanMessageID = ref<string | null>(null)

function toggleAssistantPlan(messageID: string) {
  expandedAssistantPlanMessageID.value =
    expandedAssistantPlanMessageID.value === messageID ? null : messageID
}
```

Parse `message.metadata?.assistantPlan` in `toProjectMessageView`. Add a compact
button before the technical trace with `aria-expanded`, `aria-controls`, a
stable panel ID, and the existing focus-visible ring. Rotate `ChevronRight`
when open. The button text is `assistantPlanSummary(message.plan)`.

Render an ordered list in the controlled panel:

- completed: `Check`, success tokens;
- in progress: animated `Loader2`, accent tokens;
- pending: `Square`, muted tokens.

Display `step.content` in the list; use `activeForm` only in the collapsed
summary. Keep plan and trace expansion state independent. If the newest
assistant message has plan metadata, suppress the duplicate bottom
`conversationWorkingLabel`; otherwise retain today's fallback.

- [ ] **Step 4: Run portal tests, typecheck, and build and verify GREEN**

Run:

```bash
cd providers/app-studio/portal
npm run test:assistant-plan
npm run test:assistant-progress
npm run test:conversation-resilience
npm run typecheck
npm run build
```

Expected: PASS.

- [ ] **Step 5: Run provider verification**

Run:

```bash
cd providers/app-studio
go test ./... -count=1
```

Then run from the repository root:

```bash
git diff --check
```

Expected: both commands exit 0.

- [ ] **Step 6: Commit Task 4**

```bash
git add providers/app-studio/portal/src/App.vue providers/app-studio/portal/src/assistantPlan.ts providers/app-studio/portal/src/assistantPlan.test.mjs
git commit -m "feat(app-studio): add expandable plan steps"
```

- [ ] **Step 7: Refresh and verify the live App Studio interaction**

Run:

```bash
tilt trigger app-studio
tilt get uiresource app-studio -o yaml
```

Wait until `Ready=True` and `runtimeStatus: ok`. In the signed-in App Studio
browser, open `inspire-me-daily`, start or resume a multi-step implementation,
and verify:

1. the compact `N of M steps` summary appears;
2. click expands all named ordered steps;
3. a second click collapses it;
4. live status updates do not close an open list;
5. refresh retains the latest plan;
6. the technical action trace remains separately collapsible.
