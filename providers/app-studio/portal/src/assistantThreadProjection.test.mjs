import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
})
test.after(async () => vite?.close())

test('projects terminal worked duration from canonical agent message data', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const { parseAssistantProgress } = await vite.ssrLoadModule('/src/assistantProgress.ts')
  const messages = assistantThreadItemsToMessages([{
    id: 'assistant-1',
    turnID: 'run-1',
    type: 'agentMessage',
    status: 'completed',
    content: 'Done',
    data: {
      assistantProgress: {
        version: 1,
        messages: [],
        messageSequences: [],
        workedDurationMs: 83_400,
      },
    },
    sequence: 4,
    createdAt: '2026-08-02T17:42:09Z',
  }], 'demo')

  assert.equal(messages.length, 1)
  assert.equal(messages[0].metadata.assistantStatus, 'completed')
  assert.equal(parseAssistantProgress(messages[0].metadata.assistantProgress)?.workedDurationMs, 83_400)
})

test('keeps action items alongside agent progress in the thread projection', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([{
    id: 'assistant-1', turnID: 'run-1', type: 'agentMessage', status: 'completed', content: 'Done',
    data: { assistantProgress: { version: 1, messages: [], messageSequences: [], workedDurationMs: 2_400 } },
    sequence: 1, createdAt: '2026-08-02T17:42:09Z',
  }, {
    id: 'read-1', turnID: 'run-1', type: 'dynamicToolCall', status: 'completed', content: 'Read file',
    data: { id: 'read-1', kind: 'inspect', status: 'succeeded', title: 'Read file', severity: 'normal', sequence: 1 },
    sequence: 2, createdAt: '2026-08-02T17:42:10Z',
  }], 'demo')

  assert.equal(messages[0].metadata.assistantProgress.workedDurationMs, 2_400)
  assert.equal(messages[0].metadata.assistantActionFeed.length, 1)
})

test('deduplicates scoped live tool items by their raw action ID across status updates', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-1', turnID: 'run-1', type: 'agentMessage', status: 'in_progress',
      assistantMessageID: 'assistant-1', mode: 'default', revision: 1, content: '', sequence: 1,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'tool-assistant-1-call-1', turnID: 'run-1', type: 'dynamicToolCall', status: 'in_progress',
      assistantMessageID: 'assistant-1', data: { id: 'call-1', kind: 'run', status: 'running', title: 'Run command', severity: 'normal', sequence: 1 }, sequence: 2,
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'tool-assistant-1-call-1', turnID: 'run-1', type: 'dynamicToolCall', status: 'completed',
      assistantMessageID: 'assistant-1', data: { id: 'call-1', kind: 'run', status: 'succeeded', title: 'Run command', severity: 'normal', sequence: 1 }, sequence: 3,
      createdAt: '2026-08-02T17:42:11Z',
    },
  ], 'demo')

  const actionFeed = messages[0].metadata.assistantActionFeed
  assert.equal(actionFeed.length, 1)
  assert.equal(actionFeed[0].id, 'call-1')
  assert.equal(actionFeed[0].status, 'succeeded')
})

test('binds steered activity to its explicit assistant segment and preserves historical fallback order', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const item = (overrides) => ({
    turnID: 'run-steered', status: 'completed', sequence: 1, createdAt: '2026-08-02T17:42:09Z', ...overrides,
  })
  const messages = assistantThreadItemsToMessages([
    item({ id: 'user-1', type: 'userMessage', content: 'build it', sequence: 1 }),
    item({ id: 'assistant-1', assistantMessageID: 'assistant-1', type: 'agentMessage', mode: 'default', revision: 1, content: 'first', sequence: 2 }),
    item({
      id: 'old-tool', assistantMessageID: 'assistant-1', type: 'dynamicToolCall', sequence: 3,
      data: { id: 'old-tool', kind: 'inspect', status: 'succeeded', title: 'Read old segment', severity: 'normal', sequence: 1 },
    }),
    item({
      id: 'old-plan', assistantMessageID: 'assistant-1', type: 'plan', sequence: 4,
      data: { steps: [{ content: 'Old plan', status: 'completed' }] },
    }),
    item({ id: 'assistant-2', assistantMessageID: 'assistant-2', type: 'agentMessage', mode: 'plan', revision: 2, status: 'in_progress', content: 'replacement', sequence: 5 }),
    item({
      id: 'new-tool', assistantMessageID: 'assistant-2', type: 'dynamicToolCall', sequence: 6,
      data: { id: 'new-tool', kind: 'run', status: 'running', title: 'Run new segment', severity: 'normal', sequence: 2 },
    }),
    item({
      id: 'new-plan', assistantMessageID: 'assistant-2', type: 'plan', sequence: 7,
      data: { steps: [{ content: 'New plan', status: 'in_progress' }] },
    }),
    // Legacy activity has no segment field; it belongs to the preceding agent.
    item({
      id: 'legacy-new-tool', type: 'dynamicToolCall', sequence: 8,
      data: { id: 'legacy-new-tool', kind: 'inspect', status: 'succeeded', title: 'Legacy new segment', severity: 'normal', sequence: 3 },
    }),
  ], 'demo')

  const first = messages.find((message) => message.id === 'assistant-1')
  const replacement = messages.find((message) => message.id === 'assistant-2')
  assert.deepEqual(first.metadata.assistantActionFeed.map(({ id }) => id), ['old-tool'])
  assert.deepEqual(first.metadata.assistantPlan.steps.map(({ content }) => content), ['Old plan'])
  assert.deepEqual(replacement.metadata.assistantActionFeed.map(({ id }) => id), ['new-tool', 'legacy-new-tool'])
  assert.deepEqual(replacement.metadata.assistantPlan.steps.map(({ content }) => content), ['New plan'])
})

test('hydrates terminal run state, errors, and completed plan mode from durable items', async () => {
  const { assistantThreadItemsToMessages, assistantThreadItemsToRuns } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-failed', turnID: 'run-failed', type: 'agentMessage', status: 'failed',
      assistantMessageID: 'assistant-failed', mode: 'default', revision: 6,
      error: { message: 'provider failed', errorInfo: 'provider_error' }, content: 'Nope', sequence: 2,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'assistant-interrupted', turnID: 'run-interrupted', type: 'agentMessage', status: 'interrupted',
      assistantMessageID: 'assistant-interrupted', mode: 'default', revision: 4, content: 'Stopped', sequence: 4,
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'assistant-plan', turnID: 'run-plan', type: 'agentMessage', status: 'completed',
      assistantMessageID: 'assistant-plan', mode: 'plan', revision: 8, content: 'Plan ready', sequence: 6,
      createdAt: '2026-08-02T17:42:11Z',
    },
  ], 'demo')
  const runs = assistantThreadItemsToRuns([
    {
      id: 'assistant-failed', turnID: 'run-failed', type: 'agentMessage', status: 'failed', assistantMessageID: 'assistant-failed', mode: 'default', revision: 6,
      error: { message: 'provider failed', errorInfo: 'provider_error' }, sequence: 2, createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'assistant-interrupted', turnID: 'run-interrupted', type: 'agentMessage', status: 'interrupted', assistantMessageID: 'assistant-interrupted', mode: 'default', revision: 4,
      sequence: 4, createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'assistant-plan', turnID: 'run-plan', type: 'agentMessage', status: 'completed', assistantMessageID: 'assistant-plan', mode: 'plan', revision: 8,
      sequence: 6, createdAt: '2026-08-02T17:42:11Z',
    },
  ])

  assert.equal(messages.find((message) => message.id === 'assistant-failed').metadata.assistantStatus, 'failed')
  assert.equal(messages.find((message) => message.id === 'assistant-interrupted').metadata.assistantStatus, 'interrupted')
  assert.equal(runs['run-failed'].status, 'failed')
  assert.deepEqual(runs['run-failed'].error, { message: 'provider failed', errorInfo: 'provider_error' })
  assert.equal(runs['run-interrupted'].status, 'interrupted')
  assert.equal(runs['run-plan'].mode, 'plan')
  assert.equal(runs['run-plan'].status, 'completed')
  assert.equal(runs['run-plan'].activeMessageID, 'assistant-plan')
})

test('retains the first replacement delta when a stale list projection arrives after the stream', async () => {
  const { mergeAssistantThreadMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const current = [{ id: 'assistant-2', projectID: 'demo', role: 'assistant', content: 'first replacement delta', metadata: { assistantRevision: 2 }, createdAt: '2026-08-02T17:42:09Z' }]
  const projected = [{ id: 'assistant-2', projectID: 'demo', role: 'assistant', content: '', metadata: { assistantRevision: 2 }, createdAt: '2026-08-02T17:42:09Z' }]
  assert.equal(mergeAssistantThreadMessages(current, projected)[0].content, 'first replacement delta')
})
