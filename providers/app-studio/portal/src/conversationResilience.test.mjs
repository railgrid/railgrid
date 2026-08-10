import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'

const source = await readFile(new URL('./conversationResilience.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 } })
const state = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

const message = (id, content) => ({ id, projectID: 'p', role: 'assistant', content, createdAt: '2026-01-01T00:00:00Z' })
const snapshot = (revision, content, status = 'running') => ({ run: { id: 'run-1', mode: 'default', status, revision, activeMessageID: 'a-1' }, message: message('a-1', content) })

test('start fingerprint changes with one-turn skill, resource, and inline-part selections', () => {
  const base = { content: 'inspect this', collaborationMode: 'default' }
  const resource = { provider: 'demo', resourceRef: { apiVersion: 'demo.example.io/v1', kind: 'Widget', resource: 'widgets', name: 'one' } }
  const plain = state.assistantRunStartFingerprint('p', base)
  assert.notEqual(state.assistantRunStartFingerprint('p', { ...base, skills: ['project:one'] }), plain)
  assert.notEqual(state.assistantRunStartFingerprint('p', { ...base, contextResources: [resource] }), plain)
  const inline = [{ type: 'text', text: 'inspect ' }, { type: 'resource', resourceIndex: 0 }]
  assert.notEqual(state.assistantRunStartFingerprint('p', { ...base, contentParts: inline }), plain)
  assert.notEqual(
    state.assistantRunStartFingerprint('p', { ...base, contentParts: inline }),
    state.assistantRunStartFingerprint('p', { ...base, contentParts: [{ type: 'resource', resourceIndex: 0 }, { type: 'text', text: 'inspect ' }] }),
  )
})

test('server-derived structured content trims text and remaps sorted resource indexes', () => {
  const resources = [
    { provider: 'zeta', resourceRef: { apiVersion: 'apps.example/v1', kind: 'Table', resource: 'tables', name: 'orders' } },
    { provider: 'alpha', resourceRef: { apiVersion: 'apps.example/v1', kind: 'Table', resource: 'tables', name: 'customers' } },
  ]
  assert.equal(
    state.assistantRunExpectedServerContent({
      content: 'browser-only prose',
      contextResources: resources,
      contentParts: [
        { type: 'text', text: ' inspect ' },
        { type: 'resource', resourceIndex: 0 },
        { type: 'text', text: ' with ' },
        { type: 'skill', skillID: ' team:review ' },
        { type: 'resource', resourceIndex: 1 },
      ],
    }),
    'inspect [@resource:zeta/apps.example/v1/Table/tables/orders] with [@skill:team:review][@resource:alpha/apps.example/v1/Table/tables/customers]',
  )
  assert.equal(
    state.assistantRunExpectedServerContent({ content: '  plain retry  ' }),
    'plain retry',
  )
  assert.equal(
    state.assistantRunExpectedServerContent({ content: '', contentParts: [{ type: 'skill', skillID: 'team:review' }] }),
    '[@skill:team:review]',
  )
})

test('accepted start failures consume the rich draft and use server-derived conflict content', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const sendMessage = appSource.slice(appSource.indexOf('async function sendMessage'), appSource.indexOf('function cancelMessageStream'))
  assert.match(sendMessage, /let startPostAccepted = false/)
  assert.match(sendMessage, /startPostAccepted = true[\s\S]*clearSelectedTurnAttachments\(\)/)
  const acceptedFailure = sendMessage.slice(sendMessage.indexOf('if \(startPostAccepted\)'), sendMessage.indexOf("if (e instanceof ProjectAPIRequestError && e.status === 409)"))
  assert.match(acceptedFailure, /pendingMessageSubmission = null/)
  assert.match(acceptedFailure, /pendingFirstProjectSubmission = null/)
  assert.match(acceptedFailure, /Turn accepted, but the conversation could not be refreshed/)
  assert.doesNotMatch(acceptedFailure, /prompt\.value = content/)
  assert.match(sendMessage, /assistantRunExpectedServerContent\(payload\)/)
  assert.match(sendMessage, /persistedPrompt\?\.content === expectedServerContent/)
})

test('assistantRunTerminal recognizes run and display forms of every closed outcome', () => {
  for (const status of ['completed', 'failed', 'interrupted', 'aborted', 'Completed', 'Failed', 'Interrupted', 'Aborted']) {
    assert.equal(state.assistantRunTerminal(status), true, status)
  }
  for (const status of ['running', 'stopping', 'pending_permission', 'pending_input', 'Working', undefined]) {
    assert.equal(state.assistantRunTerminal(status), false, String(status))
  }
})

test('normalizeAssistantRunStatus validates and normalizes persisted display statuses', () => {
  assert.equal(state.normalizeAssistantRunStatus('Interrupted'), 'interrupted')
  assert.equal(state.normalizeAssistantRunStatus(' pending_input '), 'pending_input')
  assert.equal(state.normalizeAssistantRunStatus('Suspended'), undefined)
  assert.equal(state.normalizeAssistantRunStatus(undefined), undefined)
})

test('Q&A request and resolution reconcile one run before its terminal snapshot', () => {
  const initial = snapshot(1, 'waiting').run
  const requested = state.reconcileAssistantRunInterrupt(initial, 'input.requested', 'request-1')
  assert.equal(requested.status, 'pending_input')
  assert.equal(requested.requestID, 'request-1')
  assert.equal(requested.revision, 2)

  const resolved = state.reconcileAssistantRunInterrupt(requested, 'input.resolved', 'request-1')
  assert.equal(resolved.status, 'running')
  assert.equal(resolved.requestID, undefined)
  assert.equal(resolved.revision, 3)

  const completed = state.reconcileAssistantRunTerminal(resolved, 'completed')
  assert.equal(completed.status, 'completed')
  assert.equal(completed.revision, 4)
  assert.equal(state.assistantRunRequiresLiveControls(completed), false)
})

test('replayed Q&A events are idempotent and terminal state cannot be reopened', () => {
  const requested = state.reconcileAssistantRunInterrupt(snapshot(1, 'waiting').run, 'input.requested', 'request-1')
  assert.strictEqual(state.reconcileAssistantRunInterrupt(requested, 'input.requested', 'request-1'), requested)
  const completed = state.reconcileAssistantRunTerminal(requested, 'completed')
  assert.strictEqual(state.reconcileAssistantRunInterrupt(completed, 'input.resolved', 'request-1'), completed)
})

test('a stale resolution cannot clear a newer pending Q&A request', () => {
  const requestA = state.reconcileAssistantRunInterrupt(snapshot(1, 'waiting').run, 'input.requested', 'request-a')
  const requestB = state.reconcileAssistantRunInterrupt(requestA, 'input.requested', 'request-b')
  const staleResolution = state.reconcileAssistantRunInterrupt(requestB, 'input.resolved', 'request-a')

  assert.strictEqual(staleResolution, requestB)
  assert.equal(staleResolution.status, 'pending_input')
  assert.equal(staleResolution.requestID, 'request-b')
  assert.equal(staleResolution.revision, requestB.revision)
})

test('replayed resolution for an already-running request is idempotent', () => {
  const requested = state.reconcileAssistantRunInterrupt(snapshot(1, 'waiting').run, 'input.requested', 'request-1')
  const resolved = state.reconcileAssistantRunInterrupt(requested, 'input.resolved', 'request-1')
  const replay = state.reconcileAssistantRunInterrupt(resolved, 'input.resolved', 'request-1')

  assert.strictEqual(replay, resolved)
  assert.equal(replay.status, 'running')
  assert.equal(replay.requestID, undefined)
  assert.equal(replay.revision, 3)
})

test('replayed request events without a request ID remain idempotent', () => {
  const first = state.reconcileAssistantRunInterrupt(snapshot(1, 'waiting').run, 'input.requested')
  const replay = state.reconcileAssistantRunInterrupt(first, 'input.requested')

  assert.strictEqual(replay, first)
  assert.equal(replay.status, 'pending_input')
  assert.equal(replay.requestID, undefined)
  assert.equal(replay.revision, 2)
})

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

test('steering appends a new assistant segment after the steered user item', () => {
  const initial = {
    messages: [
      { ...message('u-1', 'build it'), role: 'user', createdAt: '2026-01-01T00:00:00.000000Z' },
      { ...message('a-1', 'working'), createdAt: '2026-01-01T00:00:00.000001Z' },
      { ...message('u-2', 'also add tests'), role: 'user', createdAt: '2026-01-01T00:00:01.000000Z' },
    ],
    runs: { 'run-1': { ...snapshot(1, 'working').run, revision: 1 } },
  }
  const steered = state.mergeConversationSnapshot(initial, {
    run: { ...snapshot(2, 'continued').run, revision: 2, activeMessageID: 'a-2' },
    message: { ...message('a-2', 'continued'), createdAt: '2026-01-01T00:00:01.000001Z' },
  })

  assert.deepEqual(steered.messages.map((item) => item.id), ['u-1', 'a-1', 'u-2', 'a-2'])
})

test('a collaboration mode remains fixed across revisions without duplicating its message', () => {
  const startedSnapshot = {
    ...snapshot(1, 'Inspecting safely'),
    run: { ...snapshot(1, 'Inspecting safely').run, mode: 'plan' },
  }
  const revised = {
    ...snapshot(2, 'Plan ready'),
    run: { ...snapshot(2, 'Plan ready').run, mode: 'plan' },
  }
  const completed = {
    ...snapshot(3, 'Plan ready', 'completed'),
    run: { ...snapshot(3, 'Plan ready', 'completed').run, mode: 'plan' },
  }

  const started = state.mergeConversationSnapshot({ messages: [], runs: {} }, startedSnapshot)
  const revisedState = state.mergeConversationSnapshot(started, revised)
  const terminal = state.mergeConversationSnapshot(revisedState, completed)

  assert.equal(revisedState.runs['run-1'].mode, 'plan')
  assert.equal(terminal.runs['run-1'].status, 'completed')
  assert.equal(terminal.runs['run-1'].mode, 'plan')
  assert.equal(terminal.messages.filter((item) => item.id === 'a-1').length, 1)
  assert.equal(terminal.messages[0].content, 'Plan ready')
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
    { content: 'ship it', clientRequestID: 'request-1', collaborationMode: 'default' },
  )
  assert.deepEqual(
    state.assistantRunStartPayload('continue', 'request-2'),
    { content: 'continue', clientRequestID: 'request-2', collaborationMode: 'default' },
  )
  assert.deepEqual(
    state.assistantRunStartPayload('plan a theme change', 'request-3', 'plan'),
    { content: 'plan a theme change', clientRequestID: 'request-3', collaborationMode: 'plan' },
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
  const normal = { content: 'ship it', collaborationMode: 'default' }
  const plan = { content: 'ship it', collaborationMode: 'plan' }
  const review = { content: 'check it', collaborationMode: 'review' }
  assert.notEqual(state.assistantRunStartFingerprint('demo', normal), state.assistantRunStartFingerprint('demo', plan))
  assert.notEqual(state.assistantRunStartFingerprint('demo', plan), state.assistantRunStartFingerprint('demo', review))
  assert.notEqual(state.assistantRunStartFingerprint('demo', normal), state.assistantRunStartFingerprint('other', normal))
  assert.notEqual(state.assistantRunStartFingerprint('demo', normal), state.assistantRunStartFingerprint('demo', { ...normal, content: 'different' }))
})

test('conflict recovery only accepts the run created for the exact retry identity and operation', () => {
  const request = { content: 'continue', clientRequestID: 'request-1', collaborationMode: 'default' }
  const run = { id: 'run-1', status: 'running', mode: 'default', revision: 1, activeMessageID: 'a-1', clientRequestID: 'request-1' }
  assert.equal(state.assistantRunMatchesStartRequest(run, request), true)
  assert.equal(state.assistantRunMatchesStartRequest({ ...run, clientRequestID: 'request-2' }, request), false)
  assert.equal(state.assistantRunMatchesStartRequest({ ...run, mode: 'plan' }, request), false)
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

test('nonterminal runs require live controls and terminal runs do not', () => {
  const pending = snapshot(4, 'waiting', 'pending_input').run
  const completed = { ...pending, status: 'completed' }

  assert.equal(state.assistantRunRequiresLiveControls(pending), true)
  assert.equal(state.assistantRunRequiresLiveControls(completed), false)
})

test('plan implementation requires a successful completed plan run', () => {
  const completed = { ...snapshot(4, 'plan', 'completed').run, mode: 'plan' }
  assert.equal(state.assistantRunCanImplementPlan(completed), true)
  assert.equal(state.assistantRunCanImplementPlan({ ...completed, error: { message: 'provider failed' } }), false)
  assert.equal(state.assistantRunCanImplementPlan({ ...completed, mode: 'default' }), false)
  assert.equal(state.assistantRunCanImplementPlan({ ...completed, status: 'running' }), false)
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

test('a successful stop snapshot immediately makes the run interrupted and non-provisional', () => {
	const stopped = state.abortedConversationSnapshot(snapshot(4, 'working'))
	assert.equal(stopped.run.status, 'interrupted')
  assert.equal(stopped.run.revision, 5)
	assert.equal(stopped.message.metadata.assistantStatus, 'Interrupted')
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
