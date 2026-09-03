import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'
import { createServer } from 'vite'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
})
test.after(async () => vite?.close())

function message(turn, role, options = {}) {
  return {
    id: `${role}-${turn}`,
    projectID: 'demo',
    role,
    content: options.content ?? `${role} ${turn}`,
    createdAt: `2026-09-03T00:${String(turn).padStart(2, '0')}:00Z`,
    metadata: role === 'assistant'
      ? { assistantTurnID: `turn-${turn}`, assistantMessageID: `assistant-${turn}`, assistantStatus: options.status ?? 'completed' }
      : {},
  }
}

function turnMessages(turn, options = {}) {
  return [message(turn, 'user', options), message(turn, 'assistant', options)]
}

test('keeps the mounted transcript to one turn window while older history remains navigable', () => {
  const olderStart = app.indexOf('async function loadOlderAssistantThreadItems')
  const latestStart = app.indexOf('async function returnToLatestAssistantThreadItems', olderStart)
  assert.ok(olderStart >= 0 && latestStart > olderStart)

  const older = app.slice(olderStart, latestStart)
  assert.match(older, /listAssistantThreadItemPage\(props\.ctx, projectName, threadID, beforeSequence\)/)
  assert.match(older, /messages\.value = projectAssistantThreadItems\(page\.items, projectName\)/)
  assert.doesNotMatch(older, /messages\.value\s*=\s*\[\.\.\./)
  assert.doesNotMatch(older, /mergeAssistantThreadMessages/)

  const latest = app.slice(latestStart, app.indexOf('\n\nfunction latestAssistantThreadRun', latestStart))
  assert.match(latest, /listAssistantThreadItemPage\(props\.ctx, projectName, threadID\)/)
  assert.match(latest, /commitAssistantThreadItemPage\(page\)/)
  assert.match(app, /'Load earlier messages'/)
  assert.match(app, /'Return to latest'/)
  assert.match(app, /mergeLiveAssistantThreadMessages\(messages\.value, projected, explicitLiveAssistantThreadMessageIDs\(\)\)/)
  assert.match(app, /if \(!keepOlderWindow\) commitAssistantThreadItemPage\(threadPage\)/)
})

test('history navigation announces only the operation in progress and marks the transcript busy', () => {
  const olderStart = app.indexOf('async function loadOlderAssistantThreadItems')
  const latestStart = app.indexOf('async function returnToLatestAssistantThreadItems', olderStart)
  const latestEnd = app.indexOf('\n\nfunction latestAssistantThreadRun', latestStart)
  const older = app.slice(olderStart, latestStart)
  const latest = app.slice(latestStart, latestEnd)
  assert.match(app, /const assistantThreadHistoryOperation = ref<'earlier' \| 'latest' \| null>\(null\)/)
  assert.match(older, /assistantThreadOlderLoading\.value = true\s+assistantThreadHistoryOperation\.value = 'earlier'/)
  assert.match(latest, /assistantThreadOlderLoading\.value = true\s+assistantThreadHistoryOperation\.value = 'latest'/)

  const returnControlStart = app.indexOf('ref="assistantThreadReturnLatestRef"')
  const earlierControlStart = app.indexOf('ref="assistantThreadLoadEarlierRef"', returnControlStart)
  const earlierControlEnd = app.indexOf('<div v-if="assistantThreadOlderError"', earlierControlStart)
  const returnControl = app.slice(returnControlStart, earlierControlStart)
  const earlierControl = app.slice(earlierControlStart, earlierControlEnd)
  assert.match(returnControl, /<Loader2 v-if="assistantThreadHistoryOperation === 'latest'"/)
  assert.match(returnControl, /assistantThreadHistoryOperation === 'latest' \? 'Returning to latest messages…' : 'Return to latest'/)
  assert.doesNotMatch(returnControl, /Loading earlier messages/)
  assert.match(earlierControl, /<Loader2 v-if="assistantThreadHistoryOperation === 'earlier'"/)
  assert.match(earlierControl, /assistantThreadHistoryOperation === 'earlier' \? 'Loading earlier messages…' : 'Load earlier messages'/)
  assert.doesNotMatch(earlierControl, /Returning to latest messages/)
  assert.match(app, /ref="messagesRef"[\s\S]*?:aria-busy="messageStreaming \|\| conversationLoading \|\| conversationRefreshing \|\| assistantThreadOlderLoading"/)
})

test('an empty bounded history page keeps continuation and return controls visible', () => {
  assert.match(
    app,
    /v-else-if="messages\.length === 0 && !assistantThreadViewingOlderHistory && !assistantThreadOlderCursor && !assistantThreadOlderError"/,
  )
  const historySurface = app.indexOf('assistantThreadOlderCursor || assistantThreadViewingOlderHistory || assistantThreadOlderError')
  const messageLoop = app.indexOf('v-for="message in conversationMessages"', historySurface)
  assert.ok(historySurface >= 0 && messageLoop > historySurface)
})

test('a 20-turn latest window drops turn one when turn 21 arrives while retaining its live delta', async () => {
  const { mergeLiveAssistantThreadMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const current = Array.from({ length: 20 }, (_, index) => turnMessages(index + 1)).flat()
  current.push(...turnMessages(21, { status: 'running', content: 'live turn 21 delta' }))
  const projected = Array.from({ length: 20 }, (_, index) => turnMessages(index + 2, {
    status: index === 19 ? 'running' : 'completed',
    content: index === 19 ? 'live turn 21' : undefined,
  })).flat()

  const next = mergeLiveAssistantThreadMessages(
    current,
    projected,
    new Set(['user-21', 'assistant-21']),
  )

  assert.equal(next.length, 40)
  assert.equal(next.some(({ id }) => id === 'user-1' || id === 'assistant-1'), false)
  assert.equal(next.find(({ id }) => id === 'assistant-21')?.content, 'live turn 21 delta')
})

test('consecutive older-page jumps replace rather than accumulate durable turns', async () => {
  const { mergeLiveAssistantThreadMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const latest = Array.from({ length: 20 }, (_, index) => turnMessages(index + 41)).flat()
  const middle = Array.from({ length: 20 }, (_, index) => turnMessages(index + 21)).flat()
  const oldest = Array.from({ length: 20 }, (_, index) => turnMessages(index + 1)).flat()

  const middleWindow = mergeLiveAssistantThreadMessages(latest, middle, new Set())
  const oldestWindow = mergeLiveAssistantThreadMessages(middleWindow, oldest, new Set())

  assert.deepEqual(middleWindow.map(({ id }) => id), middle.map(({ id }) => id))
  assert.deepEqual(oldestWindow.map(({ id }) => id), oldest.map(({ id }) => id))
  assert.equal(oldestWindow.length, 40)
})

test('a stale page cannot roll back a newer terminal stream revision', async () => {
  const { mergeLiveAssistantThreadMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const current = [message(21, 'assistant', { content: 'Completed response' })]
  current[0].metadata.assistantRevision = 9
  current[0].metadata.assistantStatus = 'completed'
  const projected = [message(21, 'assistant', { content: 'Completed' })]
  projected[0].metadata.assistantRevision = 8
  projected[0].metadata.assistantStatus = 'running'

  const [merged] = mergeLiveAssistantThreadMessages(current, projected, new Set())

  assert.equal(merged.content, 'Completed response')
  assert.equal(merged.metadata.assistantRevision, 9)
  assert.equal(merged.metadata.assistantStatus, 'completed')
})

test('older-page navigation suppresses the global scroll-to-latest watcher', () => {
  assert.match(app, /suppressNextConversationAutoScroll = true[\s\S]*messages\.value = projectAssistantThreadItems\(page\.items, projectName\)/)
  assert.match(app, /if \(suppressNextConversationAutoScroll\) \{[\s\S]*suppressNextConversationAutoScroll = false[\s\S]*return/)
})

test('history page replacement restores focus and gives both controls coarse-pointer targets', async () => {
  const { restoreAssistantHistoryFocus } = await vite.ssrLoadModule('/src/assistantHistoryFocus.ts')
  const focused = []
  const disabledPreferred = { disabled: true, focus: () => focused.push('disabled') }
  const enabledFallback = { disabled: false, focus: () => focused.push('fallback') }
  const transcript = { focus: () => focused.push('transcript') }

  assert.equal(restoreAssistantHistoryFocus({ loading: true, preferred: enabledFallback, fallback: null, transcript }), false)
  assert.deepEqual(focused, [])
  assert.equal(restoreAssistantHistoryFocus({ loading: false, preferred: disabledPreferred, fallback: enabledFallback, transcript }), true)
  assert.deepEqual(focused, ['fallback'])
  assert.equal(restoreAssistantHistoryFocus({ loading: false, preferred: null, fallback: null, transcript }), true)
  assert.deepEqual(focused, ['fallback', 'transcript'])

  assert.match(app, /restoreAssistantThreadHistoryControlFocus\(Boolean\(page\.nextCursor\), requestSerial, projectName, threadID\)/)
  assert.match(app, /restoreAssistantThreadLatestFocus\(requestSerial, projectName, threadID\)/)
  assert.match(app, /commitAssistantThreadItemPage\(page[^)]*\)[\s\S]*restoreAssistantThreadHistoryControlFocus/)
  assert.match(app, /ref="messagesRef"[\s\S]*aria-label="Conversation transcript"[\s\S]*tabindex="-1"/)
  assert.match(app, /ref="assistantThreadReturnLatestRef"[\s\S]*?class="app-studio-touch-target[^\"]*"/)
  assert.match(app, /ref="assistantThreadLoadEarlierRef"[\s\S]*?class="app-studio-touch-target[^\"]*"/)
})

test('returning to latest coordinates bottom scroll and visible transcript focus', () => {
  const latestStart = app.indexOf('async function returnToLatestAssistantThreadItems')
  const latestEnd = app.indexOf('\n\nfunction latestAssistantThreadRun', latestStart)
  const latest = app.slice(latestStart, latestEnd)
  assert.match(latest, /suppressNextConversationAutoScroll = true\s+messages\.value = projectAssistantThreadItems\(page\.items, projectName\)/)
  assert.match(latest, /commitAssistantThreadItemPage\(page\)\s+await restoreAssistantThreadLatestFocus\(requestSerial, projectName, threadID\)/)

  const focusStart = app.indexOf('async function restoreAssistantThreadLatestFocus')
  const focusEnd = app.indexOf('\n}\n\nasync function loadOlderAssistantThreadItems', focusStart)
  const focus = app.slice(focusStart, focusEnd)
  assert.match(focus, /transcript\.scrollTop = transcript\.scrollHeight\s+transcript\.focus\(\{ preventScroll: true \}\)/)
})

test('superseding thread operations cancel an older-page loading latch', () => {
  assert.match(app, /function beginAssistantThreadRequest\(\): number \{[\s\S]*assistantThreadOlderLoading\.value = false[\s\S]*return \+\+assistantThreadRequestSerial/)
  assert.match(app, /function commitAssistantThreadItemPage\([\s\S]*assistantThreadOlderLoading\.value = false/)
  assert.doesNotMatch(app, /assistantThreadRequestSerial \+= 1/)
  assert.doesNotMatch(app, /(?:const assistantThreadLoadSerial|const renameRequestSerial|const archiveRequestSerial) = \+\+assistantThreadRequestSerial/)

  const selection = app.slice(app.indexOf('async function selectAssistantThread'), app.indexOf('\nasync function createAssistantThread'))
  assert.match(selection, /const assistantThreadLoadSerial = beginAssistantThreadRequest\(\)/)
  assert.match(selection, /commitAssistantThreadItemPage\(page\)/)
})

test('an accepted send from older history replaces the whole window with the live tail', () => {
  const sendStart = app.indexOf('async function sendMessage')
  const acceptedStart = app.indexOf('const page = await api.listAssistantThreadItemPage(props.ctx, projectName, canonicalThreadID)', sendStart)
  const snapshotStart = app.indexOf('const applied = applyAssistantSnapshot', acceptedStart)
  assert.ok(sendStart >= 0 && acceptedStart > sendStart && snapshotStart > acceptedStart)

  const accepted = app.slice(acceptedStart, snapshotStart)
  assert.match(accepted, /const items = page\.items/)
  const staleGuard = accepted.indexOf("if (!firstSendIsCurrent() || activeAssistantThreadID.value !== canonicalThreadID) return false")
  const windowReplacement = accepted.indexOf('messages.value = canonicalMessages.map(toProjectMessageView)')
  assert.ok(staleGuard >= 0 && windowReplacement > staleGuard, 'stale send responses must be rejected before replacing the mounted page')
  assert.match(accepted, /if \(assistantThreadViewingOlderHistory\.value\) \{[\s\S]*messages\.value = canonicalMessages\.map\(toProjectMessageView\)[\s\S]*\}[\s\S]*commitAssistantThreadItemPage\(page\)/)
  assert.doesNotMatch(accepted, /mergeAssistantThreadMessages/)
  assert.doesNotMatch(accepted, /messages\.value\s*=\s*\[\.\.\.messages\.value/)
})

test('active-run recovery replaces rather than merges an older history page', () => {
  const recoveryStart = app.indexOf('async function recoverAssistantConversation')
  const recoveryEnd = app.indexOf('\nfunction ', recoveryStart + 1)
  assert.ok(recoveryStart >= 0 && recoveryEnd > recoveryStart)

  const recovery = app.slice(recoveryStart, recoveryEnd)
  assert.match(recovery, /const viewingOlderHistory = assistantThreadViewingOlderHistory\.value/)
  assert.match(recovery, /const keepOlderWindow = viewingOlderHistory && !turn/)
  assert.match(recovery, /projectAssistantThreadItems\(items, projectName, \(!viewingOlderHistory && Boolean\(turn\)\) \|\| preserveExistingHistory\)/)
})
