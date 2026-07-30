import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'

const source = await readFile(new URL('./conversationResilience.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 } })
const state = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

const message = (id, content) => ({ id, projectID: 'p', role: 'assistant', content, createdAt: '2026-01-01T00:00:00Z' })
const snapshot = (revision, content, status = 'running') => ({ run: { id: 'run-1', status, revision, activeMessageID: 'a-1' }, message: message('a-1', content) })

test('mergeConversationSnapshot keeps the stable assistant message ID and rejects older or duplicate revisions', () => {
  const initial = { messages: [message('u-1', 'hello'), message('a-1', 'old')], runs: {} }
  const current = state.mergeConversationSnapshot(initial, snapshot(2, 'new'))
  const old = state.mergeConversationSnapshot(current, snapshot(1, 'stale'))
  const duplicate = state.mergeConversationSnapshot(current, snapshot(2, 'duplicate'))
  assert.deepEqual(current.messages.map(({ id, content }) => ({ id, content })), [{ id: 'u-1', content: 'hello' }, { id: 'a-1', content: 'new' }])
  assert.equal(current.messages.filter((item) => item.id === 'a-1').length, 1)
  assert.strictEqual(old, current)
  assert.strictEqual(duplicate, current)
})

test('an adaptive run can gain a work item mid-run and later suspend without duplicating its message', () => {
  const adaptive = {
    ...snapshot(1, 'Inspecting safely'),
    run: { ...snapshot(1, 'Inspecting safely').run, mode: 'adaptive' },
  }
  const promoted = {
    ...snapshot(2, 'Action proposed'),
    run: { ...snapshot(2, 'Action proposed').run, mode: 'new', workItemID: 'work-item-1' },
  }
  const suspended = {
    ...snapshot(3, 'Continue when ready', 'interrupted'),
    run: { ...snapshot(3, 'Continue when ready', 'interrupted').run, mode: 'new', workItemID: 'work-item-1' },
  }

  const started = state.mergeConversationSnapshot({ messages: [], runs: {} }, adaptive)
  const withWorkItem = state.mergeConversationSnapshot(started, promoted)
  const terminal = state.mergeConversationSnapshot(withWorkItem, suspended)

  assert.equal(withWorkItem.runs['run-1'].workItemID, 'work-item-1')
  assert.equal(withWorkItem.runs['run-1'].mode, 'new')
  assert.equal(terminal.runs['run-1'].status, 'interrupted')
  assert.equal(terminal.runs['run-1'].workItemID, 'work-item-1')
  assert.equal(terminal.messages.filter((item) => item.id === 'a-1').length, 1)
  assert.equal(terminal.messages[0].content, 'Continue when ready')
})

test('first-project durable start replaces its optimistic user message without duplicating it', () => {
  const optimistic = message('optimistic-client-1', 'ship it')
  const persisted = { ...optimistic, id: 'user-1' }
  const result = state.replaceOptimisticUserMessage([message('prior', 'earlier'), optimistic], optimistic.id, persisted)
  assert.deepEqual(result.map((item) => item.id), ['prior', 'user-1'])
})

test('reload ordering keeps a tied user message before its assistant response', () => {
  const tiedAt = '2026-07-28T19:42:00Z'
  const assistant = { ...message('msg-0000', 'done'), createdAt: tiedAt }
  const user = { ...message('msg-ffff', 'build it'), role: 'user', createdAt: tiedAt }
  const later = { ...message('msg-later', 'next'), role: 'user', createdAt: '2026-07-28T19:50:00Z' }

  const result = state.orderConversationMessages([assistant, user, later])

  assert.deepEqual(result.map((item) => item.id), ['msg-ffff', 'msg-0000', 'msg-later'])
})

test('first-project retry reuses the created project and durable request identity', () => {
  const pending = state.newFirstProjectSubmission('ship it', 'request-1')
  assert.deepEqual(state.firstProjectStartPlan(pending), { createProject: true, projectName: '', content: 'ship it', clientRequestID: 'request-1' })
  const created = state.firstProjectSubmissionWithProject(pending, 'demo')
  const firstRun = state.firstProjectStartPlan(created)
  assert.deepEqual(
    state.assistantRunStartPayload(firstRun.content, firstRun.clientRequestID),
    { content: 'ship it', clientRequestID: 'request-1', assistantAction: 'auto' },
  )
  assert.deepEqual(
    state.assistantRunStartPayload('continue', 'request-2'),
    { content: 'continue', clientRequestID: 'request-2', assistantAction: 'auto' },
  )
  assert.deepEqual(
    state.assistantRunStartPayload('change the theme', 'request-3', 'build'),
    { content: 'change the theme', clientRequestID: 'request-3', assistantAction: 'build' },
  )
  assert.equal(state.firstProjectSubmissionAccepted(created, { id: 'user-1', content: 'ship it' }), true)
  assert.equal(state.firstProjectSubmissionAccepted(created, { id: 'user-2', content: 'different' }), false)
})

test('first-project pending submission matches the project/message handoff into normal send', () => {
  const pending = state.firstProjectSubmissionWithProject(state.newFirstProjectSubmission('ship it', 'request-1'), 'demo')
  assert.equal(state.firstProjectSubmissionMatches(pending, 'demo', 'ship it'), true)
  assert.equal(state.firstProjectSubmissionMatches(pending, 'other', 'ship it'), false)
  assert.equal(state.firstProjectSubmissionMatches(pending, 'demo', 'different'), false)
})

test('message retry identity is bound to the requested operation', () => {
  const build = { content: 'ship it', assistantAction: 'build' }
  const ask = { content: 'ship it', assistantAction: 'ask' }
  const continuation = { content: 'ship it', assistantAction: 'continue', workItemID: 'work-1', workItemRevision: 3 }
  assert.notEqual(state.assistantRunStartFingerprint('demo', build), state.assistantRunStartFingerprint('demo', ask))
  assert.notEqual(state.assistantRunStartFingerprint('demo', build), state.assistantRunStartFingerprint('other', build))
  assert.notEqual(state.assistantRunStartFingerprint('demo', continuation), state.assistantRunStartFingerprint('demo', { ...continuation, workItemRevision: 4 }))
})

test('conflict recovery only accepts the run created for the exact retry identity and operation', () => {
  const request = { content: 'continue', clientRequestID: 'request-1', assistantAction: 'continue', workItemID: 'work-1', workItemRevision: 3 }
  const run = { id: 'run-1', status: 'running', mode: 'continue', workItemID: 'work-1', revision: 1, activeMessageID: 'a-1', clientRequestID: 'request-1' }
  assert.equal(state.assistantRunMatchesStartRequest(run, request), true)
  assert.equal(state.assistantRunMatchesStartRequest({ ...run, clientRequestID: 'request-2' }, request), false)
  assert.equal(state.assistantRunMatchesStartRequest({ ...run, mode: 'new' }, request), false)
  assert.equal(state.assistantRunMatchesStartRequest({ ...run, workItemID: 'work-2' }, request), false)
})

test('first-project generation rejects late replies after navigation and a new attempt has a fresh key', () => {
  const pending = state.firstProjectSubmissionWithProject(state.newFirstProjectSubmission('ship it', 'request-1'), 'demo')
  assert.equal(state.firstProjectSubmissionIsCurrent(pending, 2, 2, 'demo', 'demo', 'draft-1'), true)
  assert.equal(state.firstProjectSubmissionIsCurrent(pending, 2, 3, 'demo', 'demo', 'draft-1'), false)
  assert.equal(state.firstProjectSubmissionIsCurrent(pending, 2, 2, 'demo', '', 'draft-1'), false)
  assert.notEqual(state.newFirstProjectSubmission('ship it', 'request-2').clientRequestID, pending.clientRequestID)
})

test('equal revision rehydrates active controls but an older active snapshot cannot revive a terminal run', () => {
  const active = snapshot(4, 'waiting', 'pending_input')
  const terminal = snapshot(5, 'done', 'completed')
  assert.equal(state.canHydrateConversationRun(active.run, active.run), true)
  assert.equal(state.canHydrateConversationRun(terminal.run, active.run), false)
})

test('a stale nonterminal snapshot is rejected and cannot be used to attach a subscription', () => {
  const current = snapshot(4, 'newer')
  const stale = snapshot(3, 'older')
  const result = state.acceptConversationSnapshot(current.run, stale.run)
  assert.equal(result.accepted, false)
  assert.deepEqual(result.current, current.run)
})

test('a delayed different run cannot replace the accepted run for the same project', () => {
  const current = snapshot(4, 'newer')
  const delayed = { ...snapshot(1, 'older'), run: { ...snapshot(1, 'older').run, id: 'run-old' } }
  const result = state.acceptScopedConversationSnapshot('project-a', 'project-a', current.run, 'project-a', delayed.run)
  assert.equal(result.accepted, false)
  assert.equal(result.current.id, 'run-1')
})

test('a start response may replace a terminal prior run even when its revision resets to one', () => {
  const prior = snapshot(9, 'done', 'completed')
  const next = { ...snapshot(1, 'new'), run: { ...snapshot(1, 'new').run, id: 'run-2' } }
  const result = state.acceptScopedConversationSnapshot('project-a', 'project-a', prior.run, 'project-a', next.run, 'start')
  assert.equal(result.accepted, true)
  assert.equal(result.current.id, 'run-2')
})

test('a delayed latest response for an old run cannot replace a newer start', () => {
  const current = { ...snapshot(1, 'new'), run: { ...snapshot(1, 'new').run, id: 'run-2' } }
  const old = snapshot(9, 'old', 'completed')
  const result = state.acceptScopedConversationSnapshot('project-a', 'project-a', current.run, 'project-a', old.run, 'latest', 'run-1')
  assert.equal(result.accepted, false)
  assert.equal(result.current.id, 'run-2')
})

test('a snapshot captured for a project is rejected after selection changes', () => {
  const incoming = snapshot(1, 'old project')
  const result = state.acceptScopedConversationSnapshot('project-b', 'project-a', undefined, 'project-a', incoming.run, 'latest')
  assert.equal(result.accepted, false)
})

test('a successful abort snapshot immediately makes the run terminal and non-provisional', () => {
  const stopped = state.abortedConversationSnapshot(snapshot(4, 'working'))
  assert.equal(stopped.run.status, 'aborted')
  assert.equal(stopped.run.revision, 5)
  assert.equal(stopped.message.metadata.assistantStatus, 'Aborted')
  assert.equal(stopped.message.metadata.assistantProvisional, false)
})

test('normalizes supervisor snapshot projectName into the portal projectID contract', () => {
  const normalized = state.normalizeSnapshotMessage({ id: 'a-1', projectName: 'project-a', role: 'assistant', content: 'hello', createdAt: '2026-01-01T00:00:00Z' })
  assert.equal(normalized.projectID, 'project-a')
})

test('conversation run controller reconnects from the accepted revision with capped exponential backoff', async () => {
  const calls = []
  const scheduled = []
  const delays = []
  const controller = new state.ConversationRunController({
    connect: async (_runID, afterRevision) => { calls.push(afterRevision); throw new Error('network') },
    abort: async () => {},
    setTimeout: (fn, delay) => { delays.push(delay); scheduled.push({ fn, delay }); return scheduled.length },
    clearTimeout: () => {},
  })
  controller.start('run-1', 3)
  for (let index = 0; index < 4; index++) {
    await Promise.resolve()
    const next = scheduled.shift()
    assert.ok(next, `expected retry ${index + 1}`)
    next.fn()
  }
  await Promise.resolve()
  assert.deepEqual(calls, [3, 3, 3, 3, 3])
  assert.deepEqual(delays, [1_000, 2_000, 4_000, 8_000, 10_000])
  controller.disconnect()
})

test('a healthy snapshot resets reconnect backoff and stale callbacks are ignored after a new run starts', async () => {
  const scheduled = []
  const calls = []
  let latestDisconnect
  let controller
  controller = new state.ConversationRunController({
    connect: async (runID, _revision, setDisconnect) => { calls.push(runID); setDisconnect(() => { latestDisconnect = runID }); throw new Error('network') }, abort: async () => {},
    setTimeout: (fn, delay) => { scheduled.push({ fn, delay }); return scheduled.length }, clearTimeout: () => {},
  })
  controller.start('run-1', 1)
  await Promise.resolve()
  controller.markHealthySnapshot(2)
  scheduled.shift().fn()
  await Promise.resolve()
  assert.equal(scheduled.at(-1).delay, 1_000)
  const stale = scheduled.shift()
  controller.start('run-2', 1)
  controller.setDisconnect(() => { latestDisconnect = 'run-2' })
  const callsBeforeStaleTimer = [...calls]
  stale.fn()
  await Promise.resolve()
  assert.deepEqual(calls, callsBeforeStaleTimer)
  assert.equal(scheduled.at(-1).delay, 1_000)
  controller.disconnect()
  assert.equal(latestDisconnect, 'run-2')
})

test('stop aborts then disconnects before best-effort recovery failure', async () => {
  const events = []
  const controller = new state.ConversationRunController({
    connect: async () => { events.push('connect') },
    abort: async () => { events.push('abort') },
    recover: async () => { events.push('recover'); throw new Error('latest unavailable') },
    setTimeout: () => 0,
    clearTimeout: () => {},
  })
  controller.start('run-1', 0)
  await Promise.resolve()
  controller.setDisconnect(() => events.push('disconnect'))
  await controller.stop()
  assert.deepEqual(events, ['connect', 'abort', 'disconnect', 'recover'])
})
