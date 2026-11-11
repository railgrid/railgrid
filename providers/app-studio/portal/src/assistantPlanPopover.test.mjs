import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { createCommentVNode, createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
})
test.after(async () => vite?.close())

test('streams assistant progress inline and keeps the turn summary', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(appSource, /<AIConversationTurn[\s\S]*:turn-id="message\.id"/)
  assert.match(appSource, /import AITimestamp from '\.\/agentkit\/AITimestamp\.vue'/)
  assert.match(appSource, /<AITimestamp[\s\S]*:value="message\.createdAt"/)
  assert.match(appSource, /<template[\s\S]*v-if="\(message\.role === 'user' && userMessageHasVisibleContent\(message\)\) \|\| \(message\.role === 'assistant'/)
  assert.doesNotMatch(appSource, /expandedMessageTimestampID|messageTimestampLabel|toggleMessageTimestamp/)
  assert.match(appSource, /<AITurnProgress[\s\S]*:status="assistantTurnProgressStatus\(message\)"/)
  assert.match(appSource, /:duration="assistantWorkedLabel\(message\)"/)
  assert.match(appSource, /v-if="!assistantProgressClosed\(message\) && assistantTraceBlocks\(message\)\.length"\s*class="k-ai-turn-progress k-ai-turn-progress__content"\s*role="log"/)
  assert.doesNotMatch(appSource, /<AssistantPlanPopover|<AssistantPlanDisclosure|toggleAssistantProgress/)
  assert.match(appSource, /<template v-if="message\.role === 'user' && \(assistantAttachmentsForMessage\(message\)\.length \|\| assistantAnnotationsForMessage\(message\)\.length\)" #before>/)
  assert.match(appSource, /<AssistantActionLog[\s\S]*v-if="hasAssistantResponseContent\(message\)"/)
  assert.match(appSource, /function assistantTurnProgressStatus\(message:[\s\S]*return 'stopping'/)
  assert.match(appSource, /function assistantProgressHeaderVisible\(message:[\s\S]*message\.progress\?\.messages\.length \|\| message\.actionFeed\?\.length \|\|\s*\(assistantMessageOwnsActiveRun\(message\) && !assistantProgressClosed\(message\)\)/)
  assert.match(appSource, /A persisted progress snapshot without an authoritative run status[\s\S]*return 'pending'/)
  assert.match(appSource, /<template v-if="message\.role === \'assistant\' && assistantProgressHeaderVisible\(message\)" #progress>/)
  assert.match(appSource, /function assistantMeasuredWork\(message:[\s\S]*message\.metadata\?\.assistantWorkedDurationMS/)
  assert.match(appSource, /const activeAssistantWorkDuration = useTurnWorkDuration\(/)
  assert.match(appSource, /function assistantWorkedLabel\(message:[\s\S]*activeAssistantWorkDuration\.value : assistantMeasuredWork\(message\)/)
  assert.match(appSource, /function projectMessageAssistantStatus\(message:[\s\S]*normalizeAssistantRunStatus\(message\.metadata\?\.assistantStatus\)/)
  assert.match(appSource, /function assistantProgressClosed\(message:[\s\S]*assistantRunTerminal\(assistantRunStatusForMessage\(message\)\)/)
  assert.match(appSource, /message\.viewStatus === 'interrupted'[\s\S]*text-warning\/80[\s\S]*Interrupted/)
  assert.match(appSource, /v-if="message\.role === 'assistant' && message\.viewStatus === 'interrupted' && !assistantProgressHeaderVisible\(message\)"[\s\S]*role="status"[\s\S]*Interrupted/)
  assert.doesNotMatch(appSource, /Interrupted before completion/)
  assert.match(appSource, /parseAssistantProgress\(message\.metadata\?\.assistantProgress\)/)
  assert.match(appSource, /rawItem\.data\?\.assistantProgress[\s\S]*assistantProgress: rawItem\.data\.assistantProgress/)
  assert.doesNotMatch(appSource, /v-if="message\.actionFeed\?\.length && !message\.progress"/)
  assert.match(appSource, /v-for="\(traceBlock, traceIndex\) in assistantTraceBlocks\(message\)"[\s\S]*traceBlock\.kind === 'actions'[\s\S]*renderMessageContent\(traceBlock\.message, 'assistant'\)/)
  assert.match(appSource, /:message-id="`\$\{message\.id\}-trace-\$\{traceIndex\}`"/)
  assert.match(appSource, /activeAssistantRun\?\.activeMessageID === message\.id \? 'status'[\s\S]*aria-live="messageStreaming && activeAssistantRun\?\.activeMessageID === message\.id \? 'polite'/)
  assert.match(appSource, /<AIConversationStatus :label="conversationWorkingLabel"/)
  const workingStatus = await readFile(new URL('./agentkit/AIConversationStatus.vue', import.meta.url), 'utf8')
  assert.match(workingStatus, /role="status"/)
  assert.match(workingStatus, /k-ai-conversation-status__dot/)
  assert.doesNotMatch(workingStatus, /Loader2|animate-spin/)
  assert.match(workingStatus, /\{\{ label \}\}/)
  assert.match(workingStatus, /animation-delay:120ms/)
  assert.match(workingStatus, /animation-delay:240ms/)
  assert.match(appSource, /assistantStopRequested\.value \|\| activeAssistantRun\?\.status === 'stopping'\) return 'Stopping…'/)
  assert.match(appSource, /status === 'running' \|\| status === 'working'[\s\S]*return 'Running'/)
  assert.doesNotMatch(workingStatus, /animate-pulse font-medium/)
  assert.doesNotMatch(appSource, /Plan ended/)
  assert.doesNotMatch(appSource, /Plan completed/)
})

test('does not create an output frame for a progress-only conditional turn', async () => {
  const { default: AIConversationTurn } = await vite.ssrLoadModule('/src/agentkit/AIConversationTurn.vue')
  const { default: AITurnProgress } = await vite.ssrLoadModule('/src/agentkit/AITurnProgress.vue')
  const html = await renderToString(createSSRApp({
    render: () => h(AIConversationTurn, { role: 'assistant', turnId: 'progress-only' }, {
      progress: () => h(AITurnProgress, { status: 'running', turnId: 'progress-only' }),
      // Vue represents the parent's false v-if slot branch as a comment vnode.
      default: () => [createCommentVNode('v-if', true)],
    }),
  }))

  assert.match(html, /k-ai-turn-progress[^>]*data-status="running"/)
  assert.doesNotMatch(html, /k-ai-message__content/)
})

test('protects live plan progress from stale sequence or revision events', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(appSource, /function assistantPlanEventIsNewer\(/)
  assert.match(appSource, /if \(incomingValue < currentValue\) return false/)
  assert.match(appSource, /metadata\.assistantPlanRevision/)
  assert.match(appSource, /metadata\.assistantPlanSequence/)
  assert.match(appSource, /rawItem\.type === 'plan' && rawItem\.data[\s\S]*assistantPlanEventIsNewer\(metadata, rawItem, event\)/)
})

test('renders the running label quietly without animated gradient text', async () => {
  const style = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  const status = await readFile(new URL('./agentkit/AIConversationStatus.vue', import.meta.url), 'utf8')
  assert.match(status, /class="k-ai-conversation-status"/)
  const shared = await readFile(new URL('./agentkit/conversation.css', import.meta.url), 'utf8')
  assert.match(shared, /@media \(prefers-reduced-motion: reduce\)[\s\S]*animation: none/)
  assert.match(shared, /k-ai-conversation-status__dot[\s\S]*width: 4px;[\s\S]*height: 4px;/)
  assert.doesNotMatch(style, /@keyframes app-studio-running-ripple|background-clip: text/)
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

  const starter = appSource.slice(appSource.indexOf('async function applyStarterPrompt'), appSource.indexOf('async function applyLandingStarterPrompt'))
  assert.match(starter, /replaceAssistantComposerText\(value\)/)
  assert.match(starter, /assistantComposerRef\.value\?\.focus\(\)/)
  assert.doesNotMatch(starter, /promptRef/)

  const implementation = appSource.slice(appSource.indexOf('async function implementPlan'), appSource.indexOf('function applyAssistantSnapshot'))
  assert.match(implementation, /replaceAssistantComposerText\('Implement the plan above\.'\)/)
})

test('an action-only terminal turn retains its summary', async () => {
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
  assert.match(appSource, /<AITurnProgress[\s\S]*:duration="assistantWorkedLabel\(message\)"/)
  assert.match(appSource, /function assistantProgressClosed\(message:[\s\S]*assistantRunTerminal\(assistantRunStatusForMessage\(message\)\)/)
})
