# App Studio Active Plan Dock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Anchor the active assistant plan immediately above the App Studio message composer and remove it as soon as the run becomes terminal.

**Architecture:** Keep the existing backend and durable plan metadata unchanged. Add one pure selector for the active streaming plan, move the disclosure UI into a focused Vue component, and mount that component between the scrollable transcript and composer while leaving technical traces in assistant messages.

**Tech Stack:** Vue 3, TypeScript, Vite, Node test runner, Vue server renderer, Tailwind CSS v4, lucide-vue-next.

## Global Constraints

- Render exactly one plan dock for the active streaming assistant run.
- Place the dock after the scrollable transcript and immediately before the composer form.
- Remove the dock when the run completes, fails, aborts, or is interrupted.
- Do not render historical plan widgets inside assistant messages.
- Keep technical action traces attached to messages with independent disclosure state.
- Preserve plan disclosure state across live revisions of the same active assistant message.
- Preserve the existing plan generation, sanitization, persistence, and metadata validation contracts.
- Use only shared design tokens and existing icon/style conventions.

---

### Task 1: Move the active plan into the chat footer dock

**Files:**
- Create: `providers/app-studio/portal/src/AssistantPlanDock.vue`
- Create: `providers/app-studio/portal/src/assistantPlanDock.test.mjs`
- Modify: `providers/app-studio/portal/src/assistantPlan.ts`
- Modify: `providers/app-studio/portal/src/assistantPlan.test.mjs`
- Modify: `providers/app-studio/portal/src/App.vue:42, 385-480, 3431-3783`
- Modify: `providers/app-studio/portal/package.json`

**Interfaces:**
- Consumes: `AssistantPlan`, `assistantPlanSummary`, and `assistantPlanStepStatusLabel` from `assistantPlan.ts`.
- Produces: `activeAssistantPlanMessage<T>(messages, activeMessageID, streaming)`, returning the matching assistant message only when streaming and its parsed `plan` exists.
- Produces: `<AssistantPlanDock :message-id :plan />`, a locally stateful disclosure whose component identity is keyed by the active message ID.

- [ ] **Step 1: Add failing lifecycle tests for active-plan selection**

Extend `assistantPlan.test.mjs` with literal fixtures that prove:

```js
const messages = [
  { id: 'assistant-old', role: 'assistant', plan: oldPlan },
  { id: 'assistant-active', role: 'assistant', plan: activePlan },
]

assert.equal(activeAssistantPlanMessage(messages, 'assistant-active', true)?.id, 'assistant-active')
assert.equal(activeAssistantPlanMessage(messages, 'assistant-active', false), undefined)
assert.equal(activeAssistantPlanMessage(messages, 'missing', true), undefined)
assert.equal(activeAssistantPlanMessage(
  [{ id: 'assistant-active', role: 'assistant' }],
  'assistant-active',
  true,
), undefined)
```

The production change this catches is selecting a historical plan or retaining
the dock after streaming becomes false.

- [ ] **Step 2: Run the selector test and verify RED**

Run:

```bash
cd providers/app-studio/portal
npm run test:assistant-plan
```

Expected: FAIL because `activeAssistantPlanMessage` is not exported.

- [ ] **Step 3: Implement the minimal active-plan selector**

Add to `assistantPlan.ts`:

```ts
export interface AssistantPlanMessage {
  id: string
  role: string
  plan?: AssistantPlan
}

export function activeAssistantPlanMessage<T extends AssistantPlanMessage>(
  messages: T[],
  activeMessageID: string | undefined,
  streaming: boolean,
): (T & { plan: AssistantPlan }) | undefined {
  if (!streaming || !activeMessageID) return undefined
  const message = messages.find((item) => item.id === activeMessageID && item.role === 'assistant')
  if (!message?.plan) return undefined
  return { ...message, plan: message.plan }
}
```

- [ ] **Step 4: Run the selector test and verify GREEN**

Run:

```bash
cd providers/app-studio/portal
npm run test:assistant-plan
```

Expected: all assistant plan tests pass.

- [ ] **Step 5: Add a failing rendered disclosure test**

Create `assistantPlanDock.test.mjs`. Start a Vite middleware server, load the
real SFC with `ssrLoadModule('/src/AssistantPlanDock.vue')`, render it with
`createSSRApp` and `renderToString`, and assert literal user-visible behavior:

```js
assert.match(html, /1 of 3 steps · Adding the quote submission form/)
assert.match(html, /aria-expanded="false"/)
assert.match(html, /Adding the quote submission form/)
assert.match(html, /Verifying the preview/)
```

The production change this catches is a dock that loses its summary,
accessibility state, or step content.

- [ ] **Step 6: Run the dock test and verify RED**

Run:

```bash
cd providers/app-studio/portal
node --test src/assistantPlanDock.test.mjs
```

Expected: FAIL because `AssistantPlanDock.vue` does not exist.

- [ ] **Step 7: Create the focused disclosure component**

Create `AssistantPlanDock.vue` with:

```vue
<script setup lang="ts">
import { ref } from 'vue'
import { Check, ChevronRight, Loader2, Square } from 'lucide-vue-next'
import {
  assistantPlanStepStatusLabel,
  assistantPlanSummary,
  type AssistantPlan,
} from './assistantPlan'

const props = defineProps<{ messageId: string; plan: AssistantPlan }>()
const expanded = ref(false)
const panelID = `app-studio-assistant-plan-${props.messageId.replace(/[^a-zA-Z0-9_-]/g, '-')}`
</script>

<template>
  <div class="shrink-0 border-t border-border-subtle bg-surface-raised/95 px-4 py-2" aria-live="polite">
    <div class="mx-auto w-full max-w-[820px]">
      <button
        type="button"
        class="group inline-flex max-w-full items-center gap-2 rounded-md py-1 text-left text-[12px] text-text-secondary transition hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
        :aria-expanded="expanded"
        :aria-controls="panelID"
        @click="expanded = !expanded"
      >
        <ChevronRight
          class="h-3.5 w-3.5 shrink-0 transition-transform"
          :class="expanded ? 'rotate-90' : ''"
          :stroke-width="1.75"
        />
        <span class="min-w-0 truncate font-medium text-text-primary">{{ assistantPlanSummary(plan) }}</span>
      </button>
      <ol
        v-show="expanded"
        :id="panelID"
        class="mt-2 grid max-h-64 gap-1.5 overflow-auto rounded-lg border border-border-subtle bg-surface/80 p-2"
      >
        <li
          v-for="(step, index) in plan.steps"
          :key="`${messageId}-plan-${index}`"
          class="flex min-w-0 items-center gap-2 rounded-md border px-2.5 py-2 text-[12px] leading-5"
          :class="step.status === 'completed'
            ? 'border-success/30 bg-success-subtle text-success'
            : step.status === 'in_progress'
              ? 'border-accent/30 bg-accent-subtle text-accent'
              : 'border-border-subtle bg-surface-raised text-text-muted'"
        >
          <Check v-if="step.status === 'completed'" class="h-3.5 w-3.5 shrink-0" :stroke-width="2" />
          <Loader2 v-else-if="step.status === 'in_progress'" class="h-3.5 w-3.5 shrink-0 animate-spin" :stroke-width="1.75" />
          <Square v-else class="h-3 w-3 shrink-0" :stroke-width="1.75" />
          <span class="sr-only">{{ assistantPlanStepStatusLabel(step.status) }}</span>
          <span class="min-w-0 text-text-primary">{{ step.content }}</span>
        </li>
      </ol>
    </div>
  </div>
</template>
```

- [ ] **Step 8: Run the dock test and verify GREEN**

Run:

```bash
cd providers/app-studio/portal
node --test src/assistantPlanDock.test.mjs
```

Expected: the rendered summary, accessibility, and content assertions pass.

- [ ] **Step 9: Integrate the dock into the chat column**

In `App.vue`:

1. Import `AssistantPlanDock` and `activeAssistantPlanMessage`.
2. Remove `expandedAssistantPlanMessageID`, `toggleAssistantPlan`, and
   `assistantPlanPanelID`.
3. Add:

```ts
const activePlanMessage = computed(() =>
  activeAssistantPlanMessage(
    messages.value,
    activeAssistantRun?.activeMessageID,
    messageStreaming.value,
  ),
)
```

4. Make `conversationWorkingLabel` suppress its fallback only when
   `activePlanMessage.value` exists, not when any historical assistant message
   has a plan.
5. Delete the per-message `v-if="message.plan"` disclosure block.
6. Immediately after the closing tag of `messagesRef` and before the composer
   form, mount:

```vue
<AssistantPlanDock
  v-if="activePlanMessage"
  :key="activePlanMessage.id"
  :message-id="activePlanMessage.id"
  :plan="activePlanMessage.plan"
/>
```

Keep the per-message technical trace block unchanged.

- [ ] **Step 10: Register and run all focused portal tests**

Add:

```json
"test:assistant-plan-dock": "node --test src/assistantPlanDock.test.mjs"
```

Run:

```bash
cd providers/app-studio/portal
npm run test:assistant-plan
npm run test:assistant-plan-dock
npm run test:assistant-progress
npm run test:conversation-resilience
npm run typecheck
npm run build
```

Expected: all tests pass, typechecking succeeds, and Vite builds production
assets successfully.

- [ ] **Step 11: Verify the live browser behavior**

In the running App Studio project:

1. Start a deterministic active run with a valid multi-step plan.
2. Confirm the dock is below the scrolling transcript and above the composer.
3. Expand it, apply a same-message live plan revision, and confirm it remains
   expanded.
4. Expand a technical trace and confirm neither disclosure changes the other.
5. Apply a terminal snapshot and confirm the plan dock disappears.
6. Confirm the browser console is clean.

- [ ] **Step 12: Commit**

```bash
git add providers/app-studio/portal/package.json \
  providers/app-studio/portal/src/App.vue \
  providers/app-studio/portal/src/AssistantPlanDock.vue \
  providers/app-studio/portal/src/assistantPlan.ts \
  providers/app-studio/portal/src/assistantPlan.test.mjs \
  providers/app-studio/portal/src/assistantPlanDock.test.mjs
git commit -m "feat(app-studio): dock active plan above composer"
```
