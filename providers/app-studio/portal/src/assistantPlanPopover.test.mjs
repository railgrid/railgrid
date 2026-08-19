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
  assert.match(appSource, /assistantProgressStopping\(message\) \? 'Stopping after' : 'Working for'/)
  assert.match(appSource, /function assistantProgressHeaderVisible\(message:[\s\S]*message\.progress \|\| \(assistantMessageOwnsActiveRun\(message\) && !assistantProgressClosed\(message\)\)/)
  assert.match(appSource, /<template v-if="assistantProgressHeaderVisible\(message\)">/)
  assert.match(appSource, /flex min-h-7 flex-wrap items-center gap-2 border-b border-border-subtle pb-1/)
  assert.match(appSource, /assistantDurationTimer = window\.setInterval[\s\S]*assistantDurationNowMs\.value = Date\.now\(\)/)
  assert.match(appSource, /function projectMessageAssistantStatus\(message:[\s\S]*normalizeAssistantRunStatus\(message\.metadata\?\.assistantStatus\)/)
  assert.match(appSource, /function assistantProgressClosed\(message:[\s\S]*assistantRunTerminal\(assistantRunStatusForMessage\(message\)\)/)
  assert.match(appSource, /message\.viewStatus === 'interrupted'[\s\S]*text-warning\/80[\s\S]*<span>Interrupted<\/span>/)
  assert.match(appSource, /v-if="message\.viewStatus === 'interrupted' && !message\.progress"[\s\S]*role="status"[\s\S]*Interrupted/)
  assert.doesNotMatch(appSource, /Interrupted before completion/)
  assert.match(appSource, /v-if="assistantProgressClosed\(message\)"/)
  assert.match(appSource, /:aria-expanded="assistantProgressExpanded\(message\)"/)
  assert.match(appSource, /parseAssistantProgress\(message\.metadata\?\.assistantProgress\)/)
  assert.match(appSource, /rawItem\.data\?\.assistantProgress[\s\S]*assistantProgress: rawItem\.data\.assistantProgress/)
  assert.match(appSource, /v-if="message\.actionFeed\?\.length && !message\.progress"/)
  assert.match(appSource, /v-show="assistantProgressExpanded\(message\)"[\s\S]*v-for="\(traceBlock, traceIndex\) in assistantTraceBlocks\(message\)"[\s\S]*traceBlock\.kind === 'actions'[\s\S]*renderMessageContent\(traceBlock\.message, 'assistant'\)/)
  assert.match(appSource, /:message-id="`\$\{message\.id\}-trace-\$\{traceIndex\}`"/)
  assert.match(appSource, /activeAssistantRun\?\.activeMessageID === message\.id \? 'status'[\s\S]*aria-live="messageStreaming && activeAssistantRun\?\.activeMessageID === message\.id \? 'polite'/)
  assert.match(appSource, /v-if="conversationWorkingLabel"[\s\S]*role="status"/)
  const workingStatus = appSource.match(/<div\s+v-if="conversationWorkingLabel"[\s\S]*?<\/div>\s*<\/div>/)?.[0] ?? ''
  assert.match(workingStatus, /animate-pulse/)
  assert.doesNotMatch(workingStatus, /Loader2|animate-spin/)
  assert.match(workingStatus, /\{\{ conversationWorkingLabel \}\}/)
  assert.match(workingStatus, /rounded-full/)
  assert.match(workingStatus, /animation-delay:120ms/)
  assert.match(workingStatus, /animation-delay:240ms/)
  assert.match(appSource, /if \(activePlanMessage\.value\) return 'Running'/)
  assert.match(appSource, /assistantStopRequested\.value \|\| activeAssistantRun\?\.status === 'stopping'\) return 'Stopping…'/)
  assert.match(appSource, /status === 'running' \|\| status === 'working'[\s\S]*return 'Running'/)
  assert.match(workingStatus, /conversation-running-ripple/)
  assert.doesNotMatch(workingStatus, /animate-pulse font-medium/)
  assert.match(appSource, /<AssistantPlanPopover[\s\S]*v-if="activePlanMessage"/)
  assert.match(appSource, /<AssistantPlanDisclosure[\s\S]*v-if="assistantPlanDisclosureVisible\(message\)"/)
  assert.match(appSource, /function assistantPlanDisclosureVisible\(message: ProjectMessageView\)/)
  assert.doesNotMatch(appSource, /Plan ended/)
  assert.doesNotMatch(appSource, /Plan completed/)
  assert.doesNotMatch(appSource, /if \(activePlanMessage\.value\) return ''/)
})

test('keeps the active plan step spinner rotating until its status changes', async () => {
  const source = await readFile(new URL('./AssistantPlanSteps.vue', import.meta.url), 'utf8')
  assert.match(source, /v-else-if="step\.status === 'in_progress'"[\s\S]*animate-spin/)
  assert.doesNotMatch(source, /motion-reduce:animate-none/)
})

test('protects live plan progress from stale sequence or revision events', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(appSource, /function assistantPlanEventIsNewer\(/)
  assert.match(appSource, /if \(incomingValue < currentValue\) return false/)
  assert.match(appSource, /metadata\.assistantPlanRevision/)
  assert.match(appSource, /metadata\.assistantPlanSequence/)
  assert.match(appSource, /rawItem\.type === 'plan' && rawItem\.data[\s\S]*assistantPlanEventIsNewer\(metadata, rawItem, event\)/)
})

test('renders the running label with a Codex-style gradient ripple', async () => {
  const style = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  assert.match(style, /@keyframes app-studio-running-ripple/)
  assert.match(style, /\.conversation-running-ripple[\s\S]*linear-gradient[\s\S]*background-clip: text[\s\S]*animation: app-studio-running-ripple 1\.65s linear infinite/)
  assert.match(style, /prefers-reduced-motion: reduce[\s\S]*\.conversation-running-ripple[\s\S]*animation: none/)
})

test('offers an explicit Default-mode implementation turn after the latest completed plan', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(appSource, /function canImplementPlan\(message:[\s\S]*assistantRunCanImplementPlan\(run\)/)
  assert.match(appSource, /function implementPlan\(message:[\s\S]*assistantIntent\.value = 'default'[\s\S]*replaceAssistantComposerText\('Implement the plan above\.'\)/)
  assert.match(appSource, /v-if="canImplementPlan\(message\)"[\s\S]*Implement plan/)
})

test('programmatic active-project prompts replace rich composer state atomically', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const replacement = appSource.match(/function replaceAssistantComposerText\(value: string\) \{[\s\S]*?\n\}/)?.[0] ?? ''
  assert.match(replacement, /clearSelectedTurnAttachments\(\)/)
  assert.match(replacement, /prompt\.value = value/)
  assert.match(replacement, /assistantComposerParts\.value = \[\{ type: 'text', text: value \}\]/)

  const starter = appSource.slice(appSource.indexOf('async function applyStarterPrompt'), appSource.indexOf('async function applyLandingCategory'))
  assert.match(starter, /replaceAssistantComposerText\(value\)/)
  assert.match(starter, /assistantComposerRef\.value\?\.focus\(\)/)
  assert.doesNotMatch(starter, /promptRef/)

  const implementation = appSource.slice(appSource.indexOf('async function implementPlan'), appSource.indexOf('function applyAssistantSnapshot'))
  assert.match(implementation, /replaceAssistantComposerText\('Implement the plan above\.'\)/)
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
