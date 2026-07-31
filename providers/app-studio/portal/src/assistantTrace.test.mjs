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

test('keeps legacy unsequenced actions visible before legacy prose', () => {
  assert.deepEqual(buildAssistantTrace({
    version: 1,
    messages: ['Legacy progress update.'],
    workedDurationMs: 1_000,
  }, [action('legacy-action', undefined)]), [
    { kind: 'actions', key: 'actions-legacy', items: [action('legacy-action', undefined)] },
    { kind: 'progress', key: 'progress-0', message: 'Legacy progress update.' },
  ])
})

test('falls back without losing content when any sequence is missing or collides', () => {
  const progress = {
    version: 1,
    messages: ['First update.', 'Second update.'],
    messageSequences: [1, 3],
    workedDurationMs: 1_000,
  }
  assert.deepEqual(buildAssistantTrace(progress, [
    action('sequenced', 2),
    action('missing', undefined),
  ]), [
    { kind: 'actions', key: 'actions-legacy', items: [action('sequenced', 2), action('missing', undefined)] },
    { kind: 'progress', key: 'progress-0', message: 'First update.' },
    { kind: 'progress', key: 'progress-1', message: 'Second update.' },
  ])
  assert.deepEqual(buildAssistantTrace(progress, [action('collision', 3)]), [
    { kind: 'actions', key: 'actions-legacy', items: [action('collision', 3)] },
    { kind: 'progress', key: 'progress-0', message: 'First update.' },
    { kind: 'progress', key: 'progress-1', message: 'Second update.' },
  ])
})
