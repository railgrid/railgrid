import assert from 'node:assert/strict'
import test from 'node:test'

import { buildAssistantTrace } from './assistantTrace.ts'

const action = (id, sequence) => ({
  id,
  kind: 'inspect',
  status: 'succeeded',
  title: 'Read file',
  severity: 'normal',
  sequence,
})

test('interleaves adjacent action groups between progress prose', () => {
  assert.deepEqual(buildAssistantTrace({
    version: 1,
    messages: ['I’m mapping the project.', 'I found the edit seam.'],
    messageSequences: [1, 4],
    workedDurationMs: 18_000,
  }, [
    action('action-1', 2),
    action('action-2', 3),
    action('action-3', 5),
  ]), [
    { kind: 'progress', key: 'progress-0', message: 'I’m mapping the project.' },
    { kind: 'actions', key: 'actions-0', items: [action('action-1', 2), action('action-2', 3)] },
    { kind: 'progress', key: 'progress-1', message: 'I found the edit seam.' },
    { kind: 'actions', key: 'actions-2', items: [action('action-3', 5)] },
  ])
})
