import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./assistantProgress.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const { AssistantWorkedDurationClock, formatAssistantWorkedDuration, parseAssistantProgress } = await import(moduleURL)

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

  assert.deepEqual(parseAssistantProgress({
    version: 1,
    messages: [],
    messageSequences: [],
    workedDurationMs: 2_400,
  }), {
    version: 1,
    messages: [],
    messageSequences: [],
    workedDurationMs: 2_400,
  })

  assert.deepEqual(parseAssistantProgress({
    version: 1,
    messages: null,
    messageSequences: [],
    workedDurationMs: 99_355,
  }), {
    version: 1,
    messages: [],
    messageSequences: [],
    workedDurationMs: 99_355,
  })
})

test('rejects malformed, unknown, and oversized progress metadata', () => {
  assert.equal(parseAssistantProgress({ version: 2, messages: ['Update'], messageSequences: [1], workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: ['Update'], workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: ['Update'], messageSequences: [1], workedDurationMs: 1, raw: 'secret' }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: [' unsafe '], messageSequences: [1], workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: ['unsafe\u0000text'], messageSequences: [1], workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: ['界'.repeat(201)], messageSequences: [1], workedDurationMs: 1 }), undefined)
  assert.equal(parseAssistantProgress({ version: 1, messages: Array(33).fill('Update'), messageSequences: Array.from({ length: 33 }, (_, index) => index + 1), workedDurationMs: 1 }), undefined)
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

test('advances active worked duration, freezes pauses, and trusts terminal snapshots', () => {
  const clock = new AssistantWorkedDurationClock()
  const observation = {
    messageID: 'assistant-1',
    snapshotDurationMs: 42_000,
    nowMs: 1_000,
    ticking: true,
    terminal: false,
  }

  assert.equal(clock.observe(observation), 42_000)
  assert.equal(clock.observe({ ...observation, nowMs: 2_000 }), 43_000)
  assert.equal(clock.observe({ ...observation, nowMs: 2_500, ticking: false }), 43_500)
  assert.equal(clock.observe({ ...observation, nowMs: 8_000, ticking: false }), 43_500)
  assert.equal(clock.observe({ ...observation, nowMs: 8_000, snapshotDurationMs: 44_000, ticking: true }), 44_000)
  assert.equal(clock.observe({ ...observation, nowMs: 9_000, snapshotDurationMs: 44_000 }), 45_000)
  assert.equal(clock.observe({ ...observation, nowMs: 10_000, snapshotDurationMs: 44_700, terminal: true }), 44_700)
})

class MemoryStorage {
  values = new Map()

  getItem(key) { return this.values.get(key) ?? null }
  setItem(key, value) { this.values.set(key, value) }
  removeItem(key) { this.values.delete(key) }
}

test('persists an active duration across a same-tab hard refresh before the first snapshot', () => {
  const storage = new MemoryStorage()
  const observation = {
    messageID: 'assistant-commentary-only',
    scope: 'tenant-a:project-a',
    snapshotDurationMs: 0,
    ticking: true,
    terminal: false,
  }
  const firstClock = new AssistantWorkedDurationClock({ storage, namespace: 'app-studio' })
  assert.equal(firstClock.observe({ ...observation, nowMs: 1_000 }), 0)
  assert.equal(firstClock.observe({ ...observation, nowMs: 6_000 }), 5_000)

  // A hard refresh constructs a new clock, but session-scoped state keeps the
  // running segment continuous while the server snapshot is still zero.
  const reloadedClock = new AssistantWorkedDurationClock({ storage, namespace: 'app-studio' })
  assert.equal(reloadedClock.observe({ ...observation, nowMs: 7_000 }), 6_000)
})

test('does not accrue through a pending pause, resumes from the frozen value, and cleans terminal state', () => {
  const storage = new MemoryStorage()
  const base = { messageID: 'assistant-paused', scope: 'tenant-a:project-a', terminal: false }
  const clock = new AssistantWorkedDurationClock({ storage, namespace: 'app-studio' })
  assert.equal(clock.observe({ ...base, snapshotDurationMs: 0, nowMs: 1_000, ticking: true }), 0)
  assert.equal(clock.observe({ ...base, snapshotDurationMs: 0, nowMs: 4_000, ticking: true }), 3_000)
  assert.equal(clock.observe({ ...base, snapshotDurationMs: 0, nowMs: 5_000, ticking: false }), 4_000)

  const reloaded = new AssistantWorkedDurationClock({ storage, namespace: 'app-studio' })
  assert.equal(reloaded.observe({ ...base, snapshotDurationMs: 0, nowMs: 20_000, ticking: false }), 4_000)
  assert.equal(reloaded.observe({ ...base, snapshotDurationMs: 0, nowMs: 21_000, ticking: true }), 4_000)
  assert.equal(reloaded.observe({ ...base, snapshotDurationMs: 0, nowMs: 25_000, ticking: true }), 8_000)

  assert.equal(reloaded.observe({ ...base, snapshotDurationMs: 4_200, nowMs: 26_000, ticking: false, terminal: true }), 4_200)
  const afterTerminal = new AssistantWorkedDurationClock({ storage, namespace: 'app-studio' })
  assert.equal(afterTerminal.observe({ ...base, snapshotDurationMs: 0, nowMs: 27_000, ticking: true }), 0)
})
