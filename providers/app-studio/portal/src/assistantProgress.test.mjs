import assert from 'node:assert/strict'
import test from 'node:test'

import { formatAssistantWorkedDuration, parseAssistantProgress } from './assistantProgress.ts'

test('parses the bounded versioned assistant progress contract', () => {
  assert.deepEqual(parseAssistantProgress({
    version: 1,
    messages: ['I found the existing structure.', 'I’m verifying the finished change.'],
    messageSequences: [1, 4],
    workedDurationMs: 83_400,
  }), {
    version: 1,
    messages: ['I found the existing structure.', 'I’m verifying the finished change.'],
    messageSequences: [1, 4],
    workedDurationMs: 83_400,
  })
})

test('rejects malformed, unknown, and oversized progress metadata', () => {
  assert.equal(parseAssistantProgress({ version: 2, messages: ['Update'], workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: ['Update'], workedDurationMs: 1, raw: 'secret' }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: [' unsafe '], workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: ['unsafe\u0000text'], workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: ['界'.repeat(201)], workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: Array(33).fill('Update'), workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: ['Update'], messageSequences: [1, 2], workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: ['Update'], messageSequences: [0], workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: ['First', 'Second'], messageSequences: [2, 1], workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: ['Update'], messageSequences: [10_001], workedDurationMs: 1 }), undefined)
})

test('formats Codex-style worked durations', () => {
  assert.equal(formatAssistantWorkedDuration(200), '1s')
  assert.equal(formatAssistantWorkedDuration(83_400), '1m 23s')
  assert.equal(formatAssistantWorkedDuration(3_780_000), '1h 3m')
})
