import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
})
test.after(async () => vite?.close())

test('renders the floating desktop capsule and accessible checklist', async () => {
  const { default: AssistantPlanPopover } = await vite.ssrLoadModule('/src/AssistantPlanPopover.vue')
  const html = await renderToString(createSSRApp(AssistantPlanPopover, {
    messageId: 'assistant-active',
    plan: {
      steps: [
        { content: 'Inspect the quote form', status: 'completed' },
        { content: 'Add the quote form', activeForm: 'Adding the quote form', status: 'in_progress' },
        { content: 'Verify the preview', status: 'pending' },
      ],
    },
  }))
  assert.match(html, /absolute bottom-3 right-4/)
  assert.match(html, /1 of 3 steps · Adding the quote form/)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, /Completed:/)
  assert.match(html, /In progress:/)
  assert.match(html, /Pending:/)
  assert.doesNotMatch(html, /border-t/)
})

test('renders a collapsed terminal inline disclosure with accessible immutable details', async () => {
  const { default: AssistantPlanDisclosure } = await vite.ssrLoadModule('/src/AssistantPlanDisclosure.vue')
  const html = await renderToString(createSSRApp(AssistantPlanDisclosure, {
    messageId: 'assistant-failed',
    status: 'failed',
    plan: {
      steps: [
        { content: 'Inspect the quote form', status: 'completed' },
        { content: 'Update the quote form', status: 'pending' },
      ],
    },
  }))
  assert.match(html, /Plan ended · 1 of 2 steps completed/)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, /aria-controls="app-studio-assistant-plan-details-assistant-failed"/)
  assert.match(html, /data-plan-status="failed"/)
  assert.match(html, /Inspect the quote form/)
  assert.match(html, /Update the quote form/)
})

test('preserves terminal status and persisted counts for complete, partial, interrupted, and failed plans', async () => {
  const { default: AssistantPlanDisclosure } = await vite.ssrLoadModule('/src/AssistantPlanDisclosure.vue')
  const cases = [
    { id: 'assistant-complete', status: 'completed', steps: [{ content: 'Inspect', status: 'completed' }], summary: 'Plan completed · 1 of 1 steps completed' },
    { id: 'assistant-partial', status: 'completed', steps: [{ content: 'Inspect', status: 'completed' }, { content: 'Apply', status: 'pending' }], summary: 'Plan ended · 1 of 2 steps completed' },
    { id: 'assistant-interrupted', status: 'interrupted', steps: [{ content: 'Inspect', status: 'completed' }, { content: 'Apply', status: 'in_progress' }], summary: 'Plan ended · 1 of 2 steps completed' },
    { id: 'assistant-failed', status: 'failed', steps: [{ content: 'Inspect', status: 'completed' }, { content: 'Apply', status: 'pending' }], summary: 'Plan ended · 1 of 2 steps completed' },
  ]

  for (const testCase of cases) {
    const html = await renderToString(createSSRApp(AssistantPlanDisclosure, {
      messageId: testCase.id,
      status: testCase.status,
      plan: { steps: testCase.steps },
    }))
    assert.match(html, new RegExp(testCase.summary.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
    assert.match(html, new RegExp(`data-plan-status="${testCase.status}"`))
    assert.match(html, /aria-expanded="false"/)
    assert.match(html, /aria-controls="app-studio-assistant-plan-details-/)
  }
})

test('supports explicit collapse and releases the mobile sheet at the desktop breakpoint', async () => {
  const source = await readFile(new URL('./AssistantPlanPopover.vue', import.meta.url), 'utf8')
  assert.match(source, /if \(pinned\.value\) \{[\s\S]*dismissed\.value = true/)
  assert.match(source, /desktopOpen = computed\(\(\) => !dismissed\.value/)
  assert.match(source, /matchMedia\('\(min-width: 768px\)'\)/)
  assert.match(source, /if \(event\.matches\) closeMobile\(false\)/)
  assert.match(source, /removeEventListener\('change', onDesktopBreakpoint\)/)
})

test('keeps action details, assistant progress prose, working status, and plan details as separate surfaces', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(appSource, /<AssistantActionLog[\s\S]*v-if="hasAssistantResponseContent\(message\)"/)
  assert.match(appSource, /Worked for \{\{ assistantWorkedLabel\(message\) \}\}/)
  assert.match(appSource, /Working for \{\{ assistantWorkedLabel\(message\) \}\}/)
  assert.match(appSource, /function assistantProgressHeaderVisible\(message:[\s\S]*message\.progress \|\| \(assistantMessageOwnsActiveRun\(message\) && !assistantProgressClosed\(message\)\)/)
  assert.match(appSource, /<template v-if="assistantProgressHeaderVisible\(message\)">/)
  assert.match(appSource, /flex min-h-7 flex-wrap items-center gap-2 border-b border-border-subtle pb-1/)
  assert.match(appSource, /assistantDurationTimer = window\.setInterval[\s\S]*assistantDurationNowMs\.value = Date\.now\(\)/)
  assert.match(appSource, /function projectMessageAssistantStatus\(message:[\s\S]*normalizeAssistantRunStatus\(message\.metadata\?\.assistantStatus\)/)
  assert.match(appSource, /function assistantProgressClosed\(message:[\s\S]*assistantRunTerminal\(assistantRunStatusForMessage\(message\)\)/)
  assert.match(appSource, /v-if="message\.viewStatus === 'interrupted'"[\s\S]*role="status"[\s\S]*Interrupted before completion/)
  assert.match(appSource, /v-if="message\.viewStatus === 'interrupted' && !message\.progress"[\s\S]*role="status"[\s\S]*Interrupted before completion/)
  assert.match(appSource, /v-if="assistantProgressClosed\(message\)"/)
  assert.match(appSource, /:aria-expanded="assistantProgressExpanded\(message\)"/)
  assert.match(appSource, /parseAssistantProgress\(message\.metadata\?\.assistantProgress\)/)
  assert.match(appSource, /rawItem\.data\?\.assistantProgress[\s\S]*assistantProgress: rawItem\.data\.assistantProgress/)
  assert.match(appSource, /v-if="message\.actionFeed\?\.length && !message\.progress"/)
  assert.match(appSource, /v-show="assistantProgressExpanded\(message\)"[\s\S]*v-for="\(traceBlock, traceIndex\) in assistantTraceBlocks\(message\)"[\s\S]*traceBlock\.kind === 'actions'[\s\S]*renderMessageContent\(traceBlock\.message, 'assistant'\)/)
  assert.match(appSource, /:message-id="`\$\{message\.id\}-trace-\$\{traceIndex\}`"/)
  assert.match(appSource, /activeAssistantRun\?\.activeMessageID === message\.id \? 'status'[\s\S]*aria-live="messageStreaming && activeAssistantRun\?\.activeMessageID === message\.id \? 'polite'/)
  assert.match(appSource, /v-if="conversationWorkingLabel"[\s\S]*role="status"/)
  assert.match(appSource, /if \(activePlanMessage\.value\) return 'Working'/)
  assert.match(appSource, /<AssistantPlanPopover[\s\S]*v-if="activePlanMessage"/)
  assert.match(appSource, /<AssistantPlanDisclosure[\s\S]*assistantPlanTerminalStatusForMessage\(message\)/)
  assert.doesNotMatch(appSource, /if \(activePlanMessage\.value\) return ''/)
})

test('offers an explicit Default-mode implementation turn after the latest completed plan', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(appSource, /function canImplementPlan\(message:[\s\S]*assistantRunCanImplementPlan\(run\)/)
  assert.match(appSource, /function implementPlan\(message:[\s\S]*assistantIntent\.value = 'default'[\s\S]*prompt\.value = 'Implement the plan above\.'/)
  assert.match(appSource, /v-if="canImplementPlan\(message\)"[\s\S]*Implement plan/)
})

test('an action-only terminal turn activates the collapsed combined disclosure', async () => {
  const { parseAssistantProgress } = await vite.ssrLoadModule('/src/assistantProgress.ts')
  const { buildAssistantTrace } = await vite.ssrLoadModule('/src/assistantTrace.ts')
  const { assistantRunTerminal } = await vite.ssrLoadModule('/src/conversationResilience.ts')
  const progress = parseAssistantProgress({
    version: 1,
    messages: null,
    messageSequences: [],
    workedDurationMs: 2_400,
  })
  assert.ok(progress)
  assert.equal(assistantRunTerminal('completed'), true)
  assert.deepEqual(buildAssistantTrace(progress, [{
    id: 'read-1',
    kind: 'inspect',
    status: 'succeeded',
    title: 'Read file',
    severity: 'normal',
    sequence: 1,
  }]), [{
    kind: 'actions',
    key: 'actions-0',
    items: [{
      id: 'read-1',
      kind: 'inspect',
      status: 'succeeded',
      title: 'Read file',
      severity: 'normal',
      sequence: 1,
    }],
  }])

  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(appSource, /<template v-if="assistantProgressHeaderVisible\(message\)">[\s\S]*Worked for \{\{ assistantWorkedLabel\(message\) \}\}/)
  assert.match(appSource, /function assistantProgressClosed\(message:[\s\S]*assistantRunTerminal\(assistantRunStatusForMessage\(message\)\)/)
})
