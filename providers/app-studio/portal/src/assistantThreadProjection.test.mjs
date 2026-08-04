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

test('projects typed commentary items without erasing the owner trace', async () => {
  const { assistantThreadItemsToMessages, assistantThreadItemsToRuns } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'commentary-assistant-1-2', turnID: 'run-1', type: 'agentMessage', phase: 'commentary', status: 'completed',
      assistantMessageID: 'assistant-1', content: 'I found the relevant files.', sequence: 2,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'tool-1', turnID: 'run-1', type: 'dynamicToolCall', status: 'completed', assistantMessageID: 'assistant-1',
      data: { id: 'call-1', kind: 'inspect', status: 'succeeded', title: 'Read files', severity: 'normal', sequence: 1 }, sequence: 3,
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'assistant-1', turnID: 'run-1', type: 'agentMessage', phase: 'final_answer', status: 'completed',
      assistantMessageID: 'assistant-1', content: 'Here is the answer.', mode: 'default', revision: 4,
      data: { assistantProgress: { version: 1, messages: ['I found the relevant files.'], messageSequences: [2], workedDurationMs: 1_200 } },
      sequence: 4, createdAt: '2026-08-02T17:42:09Z',
    },
  ], 'demo')

  assert.deepEqual(messages.map(({ id }) => id), ['commentary-assistant-1-2', 'assistant-1'])
  assert.equal(messages[0].metadata.assistantPhase, 'commentary')
  assert.equal(messages[0].content, 'I found the relevant files.')
  assert.equal(messages[1].metadata.assistantPhase, 'final_answer')
  assert.equal(messages[1].metadata.assistantActionFeed[0].id, 'call-1')
  assert.deepEqual(messages[1].metadata.assistantProgress.messages, ['I found the relevant files.'])
  assert.deepEqual(messages[1].metadata.assistantProgress.messageSequences, [2])
  assert.equal(messages[1].metadata.assistantProgress.workedDurationMs, 1_200)
  assert.equal(assistantThreadItemsToRuns([
    { id: 'commentary-assistant-1-2', turnID: 'run-1', type: 'agentMessage', phase: 'commentary', status: 'completed', assistantMessageID: 'assistant-1', sequence: 2, createdAt: '2026-08-02T17:42:09Z' },
    { id: 'assistant-1', turnID: 'run-1', type: 'agentMessage', phase: 'final_answer', status: 'completed', assistantMessageID: 'assistant-1', sequence: 4, createdAt: '2026-08-02T17:42:09Z' },
  ])['run-1'].activeMessageID, 'assistant-1')
})

test('keeps terminal trace ordering and response surfaces after commentary collapse', async () => {
  const { assistantThreadItemsToMessages, hideCommentaryRepresentedInTrace } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const { buildAssistantTrace } = await vite.ssrLoadModule('/src/assistantTrace.ts')
  const items = [
    {
      id: 'commentary-assistant-2-1', turnID: 'run-2', type: 'agentMessage', phase: 'commentary', status: 'completed',
      assistantMessageID: 'assistant-2', content: 'I am mapping the project.', sequence: 2,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'tool-2-1', turnID: 'run-2', type: 'dynamicToolCall', status: 'completed', assistantMessageID: 'assistant-2',
      data: { id: 'call-2-1', kind: 'inspect', status: 'succeeded', title: 'Read project', severity: 'normal', sequence: 3 }, sequence: 3,
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'commentary-assistant-2-2', turnID: 'run-2', type: 'agentMessage', phase: 'commentary', status: 'completed',
      assistantMessageID: 'assistant-2', content: 'I found the edit seam.', sequence: 4,
      createdAt: '2026-08-02T17:42:11Z',
    },
    {
      id: 'tool-2-2', turnID: 'run-2', type: 'dynamicToolCall', status: 'completed', assistantMessageID: 'assistant-2',
      data: { id: 'call-2-2', kind: 'inspect', status: 'succeeded', title: 'Check tests', severity: 'normal', sequence: 5 }, sequence: 5,
      createdAt: '2026-08-02T17:42:12Z',
    },
    {
      id: 'plan-run-2', turnID: 'run-2', type: 'plan', status: 'completed', assistantMessageID: 'assistant-2',
      data: { steps: [{ content: 'Inspect', status: 'completed' }] }, sequence: 5,
      createdAt: '2026-08-02T17:42:12Z',
    },
    {
      id: 'assistant-2', turnID: 'run-2', type: 'agentMessage', phase: 'final_answer', status: 'completed',
      assistantMessageID: 'assistant-2', content: 'The answer is ready.',
      data: {
        assistantProgress: {
          version: 1,
          messages: ['I am mapping the project.', 'I found the edit seam.'],
          messageSequences: [2, 4],
          workedDurationMs: 2_400,
        },
      },
      sequence: 6, createdAt: '2026-08-02T17:42:13Z',
    },
  ]

  const messages = assistantThreadItemsToMessages(items, 'demo')
  const terminal = messages.find(({ id }) => id === 'assistant-2')
  assert.ok(terminal)
  assert.equal(terminal.content, 'The answer is ready.')
  assert.deepEqual(terminal.metadata.assistantPlan, { steps: [{ content: 'Inspect', status: 'completed' }] })
  assert.deepEqual(buildAssistantTrace(terminal.metadata.assistantProgress, terminal.metadata.assistantActionFeed), [
    { kind: 'progress', key: 'progress-0', message: 'I am mapping the project.' },
    { kind: 'actions', key: 'actions-0', items: [terminal.metadata.assistantActionFeed[0]] },
    { kind: 'progress', key: 'progress-1', message: 'I found the edit seam.' },
    { kind: 'actions', key: 'actions-1', items: [terminal.metadata.assistantActionFeed[1]] },
  ])
  assert.deepEqual(hideCommentaryRepresentedInTrace(messages).map(({ id }) => id), ['assistant-2'])
})

test('uses the same interleaved trace for an active typed-commentary owner', async () => {
  const { assistantThreadItemsToMessages, hideCommentaryRepresentedInTrace } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const { buildAssistantTrace } = await vite.ssrLoadModule('/src/assistantTrace.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-active', turnID: 'run-active', type: 'agentMessage', status: 'in_progress',
      assistantMessageID: 'assistant-active', content: '', sequence: 1,
      data: {
        assistantProgress: {
          version: 1,
          messages: ['I am mapping the project.', 'I found the edit seam.'],
          messageSequences: [2, 4],
          workedDurationMs: 1_200,
        },
      },
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'commentary-assistant-active-2', turnID: 'run-active', type: 'agentMessage', phase: 'commentary', status: 'completed', assistantMessageID: 'assistant-active',
      content: 'I am mapping the project.', sequence: 2,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'tool-active-1', turnID: 'run-active', type: 'dynamicToolCall', status: 'completed', assistantMessageID: 'assistant-active',
      data: { id: 'call-active-1', kind: 'inspect', status: 'succeeded', title: 'Read project', severity: 'normal', sequence: 3 },
      sequence: 3, createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'commentary-assistant-active-4', turnID: 'run-active', type: 'agentMessage', phase: 'commentary', status: 'completed', assistantMessageID: 'assistant-active',
      content: 'I found the edit seam.', sequence: 4, createdAt: '2026-08-02T17:42:11Z',
    },
    {
      id: 'tool-active-2', turnID: 'run-active', type: 'dynamicToolCall', status: 'in_progress', assistantMessageID: 'assistant-active',
      data: { id: 'call-active-2', kind: 'run', status: 'running', title: 'Run checks', severity: 'normal', sequence: 5 },
      sequence: 5, createdAt: '2026-08-02T17:42:12Z',
    },
  ], 'demo')

  const visible = hideCommentaryRepresentedInTrace(messages)
  assert.deepEqual(visible.map(({ id }) => id), ['assistant-active'])
  const owner = visible[0]
  assert.deepEqual(buildAssistantTrace(owner.metadata.assistantProgress, owner.metadata.assistantActionFeed), [
    { kind: 'progress', key: 'progress-0', message: 'I am mapping the project.' },
    { kind: 'actions', key: 'actions-0', items: [owner.metadata.assistantActionFeed[0]] },
    { kind: 'progress', key: 'progress-1', message: 'I found the edit seam.' },
    { kind: 'actions', key: 'actions-1', items: [owner.metadata.assistantActionFeed[1]] },
  ])
})

test('keeps commentary visible until its owner trace contains the same prose', async () => {
  const { hideCommentaryRepresentedInTrace } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = [
    {
      id: 'commentary-active-1', projectID: 'demo', role: 'assistant', content: 'Still working.',
      metadata: { assistantPhase: 'commentary', assistantMessageID: 'assistant-active', assistantCommentarySequence: 2 },
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'assistant-active', projectID: 'demo', role: 'assistant', content: '',
      metadata: {
        assistantStatus: 'running',
        assistantMessageID: 'assistant-active',
        assistantProgress: { version: 1, messages: ['An earlier update.'], messageSequences: [1], workedDurationMs: 200 },
      },
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'commentary-done-1', projectID: 'demo', role: 'assistant', content: 'Finished the checks.',
      metadata: { assistantPhase: 'commentary', assistantMessageID: 'assistant-done', assistantCommentarySequence: 2 },
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'assistant-done', projectID: 'demo', role: 'assistant', content: 'Done.',
      metadata: {
        assistantStatus: 'completed',
        assistantMessageID: 'assistant-done',
        assistantProgress: { version: 1, messages: ['Finished the checks.'], messageSequences: [2], workedDurationMs: 400 },
      },
      createdAt: '2026-08-02T17:42:10Z',
    },
  ]

  assert.deepEqual(hideCommentaryRepresentedInTrace(messages).map(({ id }) => id), [
    'commentary-active-1',
    'assistant-active',
    'assistant-done',
  ])
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

test('projects linked retrying and recovered actions without dropping recovery metadata', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-recovery', turnID: 'run-recovery', type: 'agentMessage', status: 'in_progress',
      assistantMessageID: 'assistant-recovery', content: '', sequence: 1,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'tool-prior', turnID: 'run-recovery', type: 'dynamicToolCall', status: 'completed', assistantMessageID: 'assistant-recovery',
      data: {
        id: 'prior-1', kind: 'edit', status: 'failed', title: 'Edit failed', severity: 'error', sequence: 1,
        diagnostic: { category: 'validation', message: 'The source is stale.', referenceID: 'action-prior', code: 'stale_source', operation: 'edit_file', path: 'src/App.vue', guidance: 'Read and retry.' },
      }, sequence: 2, createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'tool-retry', turnID: 'run-recovery', type: 'dynamicToolCall', status: 'in_progress', assistantMessageID: 'assistant-recovery',
      data: { id: 'retry-1', kind: 'edit', status: 'retrying', title: 'Retrying file update', severity: 'attention', sequence: 2, recoveryOf: 'prior-1' },
      sequence: 3, createdAt: '2026-08-02T17:42:11Z',
    },
    {
      id: 'tool-recovered', turnID: 'run-recovery', type: 'dynamicToolCall', status: 'completed', assistantMessageID: 'assistant-recovery',
      data: { id: 'retry-1', kind: 'edit', status: 'recovered', title: 'Recovered file update', severity: 'normal', sequence: 2, recoveryOf: 'prior-1' },
      sequence: 4, createdAt: '2026-08-02T17:42:12Z',
    },
  ], 'demo')

  const actions = messages[0].metadata.assistantActionFeed
  assert.equal(actions.length, 2)
  assert.equal(actions[0].status, 'failed')
  assert.equal(actions[1].status, 'recovered')
  assert.equal(actions[1].recoveryOf, 'prior-1')
  assert.equal(actions[0].diagnostic.code, 'stale_source')
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

test('retains persisted plan snapshots for interrupted and failed reload owners', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-interrupted-plan', turnID: 'run-interrupted-plan', type: 'agentMessage', status: 'interrupted',
      assistantMessageID: 'assistant-interrupted-plan', content: 'Stopped while implementing.', sequence: 1,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'plan-interrupted', turnID: 'run-interrupted-plan', type: 'plan', status: 'completed',
      assistantMessageID: 'assistant-interrupted-plan', data: {
        steps: [
          { content: 'Inspect the project', status: 'completed' },
          { content: 'Apply the change', status: 'pending' },
        ],
      }, sequence: 2, createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'assistant-failed-plan', turnID: 'run-failed-plan', type: 'agentMessage', status: 'failed',
      assistantMessageID: 'assistant-failed-plan', content: 'The change failed.', sequence: 3,
      createdAt: '2026-08-02T17:42:11Z',
    },
    {
      id: 'plan-failed', turnID: 'run-failed-plan', type: 'plan', status: 'completed',
      assistantMessageID: 'assistant-failed-plan', data: {
        steps: [
          { content: 'Inspect the project', status: 'completed' },
          { content: 'Run the checks', status: 'in_progress' },
        ],
      }, sequence: 4, createdAt: '2026-08-02T17:42:12Z',
    },
  ], 'demo')

  assert.equal(messages.find(({ id }) => id === 'assistant-interrupted-plan').metadata.assistantStatus, 'interrupted')
  assert.deepEqual(messages.find(({ id }) => id === 'assistant-interrupted-plan').metadata.assistantPlan.steps.map(({ status }) => status), ['completed', 'pending'])
  assert.equal(messages.find(({ id }) => id === 'assistant-failed-plan').metadata.assistantStatus, 'failed')
  assert.deepEqual(messages.find(({ id }) => id === 'assistant-failed-plan').metadata.assistantPlan.steps.map(({ status }) => status), ['completed', 'in_progress'])
})

test('retains the first replacement delta when a stale list projection arrives after the stream', async () => {
  const { mergeAssistantThreadMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const current = [{ id: 'assistant-2', projectID: 'demo', role: 'assistant', content: 'first replacement delta', metadata: { assistantRevision: 2 }, createdAt: '2026-08-02T17:42:09Z' }]
  const projected = [{ id: 'assistant-2', projectID: 'demo', role: 'assistant', content: '', metadata: { assistantRevision: 2 }, createdAt: '2026-08-02T17:42:09Z' }]
  assert.equal(mergeAssistantThreadMessages(current, projected)[0].content, 'first replacement delta')
})
