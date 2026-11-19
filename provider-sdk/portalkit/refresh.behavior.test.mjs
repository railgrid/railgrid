import assert from 'node:assert/strict'
import test from 'node:test'

import { createAdaptiveRefreshTimer, createLatestRefreshController, createResourcePageReader } from './refresh.ts'

test('background refresh waits through a hidden tab, wakes once and stops listening on teardown', t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const document = Object.assign(new EventTarget(), { hidden: true })
  const window = new EventTarget()
  Object.assign(globalThis, { document, window })
  const read = t.mock.fn()
  const timer = createAdaptiveRefreshTimer(read)
  try {
    timer.schedule()
    t.mock.timers.tick(30_000)
    window.dispatchEvent(new Event('online'))
    assert.equal(read.mock.callCount(), 0)
    document.hidden = false
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('online'))
    assert.equal(read.mock.callCount(), 1)
    timer.schedule()
    window.dispatchEvent(new Event('online'))
    assert.equal(read.mock.callCount(), 2)
    timer.schedule()
    timer.stop()
    document.dispatchEvent(new Event('visibilitychange'))
    t.mock.timers.tick(30_000)
    assert.equal(read.mock.callCount(), 2)
  } finally {
    timer.stop()
    delete globalThis.document
    delete globalThis.window
  }
})

test('a queued foreground read finishes even when another background read follows it', async () => {
  const reads = []
  const controller = createLatestRefreshController((id, mode) => new Promise(resolve => reads.push({ id, mode, resolve })))
  const initial = controller.request('background')
  controller.invalidate()
  const foreground = controller.request('foreground')
  reads[0].resolve()
  await Promise.resolve()
  const background = controller.request('background')
  reads[1].resolve()
  await Promise.all([initial, foreground])
  assert.deepEqual(reads.map(read => read.mode), ['background', 'foreground', 'background'])
  controller.stop()
  await background
  reads[2].resolve()
})

test('page navigation consumes its cursor once and a refresh follows the new snapshot', async () => {
  const reader = createResourcePageReader()
  const request = { page: 2, pageSize: 1, cursor: 'old' }
  const calls = []
  const list = async ({ continue: cursor }) => {
    calls.push(cursor)
    return cursor ? { items: [cursor === 'old' ? 'deleted' : 'replacement'] } : { items: ['first'], continue: 'new' }
  }
  reader.navigate('old')
  assert.deepEqual((await reader.read(request, list, () => true)).result.items, ['deleted'])
  assert.deepEqual((await reader.read(request, list, () => true)).result.items, ['replacement'])
  assert.deepEqual(calls, ['old', undefined, 'new'])
  const last = await reader.read(request, async () => ({ items: ['first'] }), () => true)
  assert.deepEqual(last, { page: 1, cursor: null, result: { items: ['first'] } })
})

test('a superseded page walk stops before following another cursor', async () => {
  let current = true
  const reader = createResourcePageReader()
  const page = await reader.read({ page: 3, pageSize: 10, cursor: 'old' }, async () => {
    current = false
    return { continue: 'new' }
  }, () => current)
  assert.equal(page, null)
})

test('deep background walks back off while foreground refresh remains immediate', async t => {
  t.mock.timers.enable({ apis: ['Date'] })
  const reader = createResourcePageReader()
  const request = { page: 7, pageSize: 10, cursor: '6' }
  const list = t.mock.fn(async ({ continue: cursor }) => ({
    continue: String(Number(cursor ?? 0) + 1),
  }))
  const current = () => true

  reader.navigate(request.cursor)
  await reader.read(request, list, current)
  assert.equal(list.mock.callCount(), 1)

  t.mock.timers.tick(30_000)
  assert.equal(await reader.read(request, list, current, 'background'), null)
  assert.equal(list.mock.callCount(), 1)

  await reader.read(request, list, current)
  assert.equal(list.mock.callCount(), 8)

  t.mock.timers.tick(89_999)
  assert.equal(await reader.read(request, list, current, 'background'), null)
  t.mock.timers.tick(1)
  assert.equal((await reader.read(request, list, current, 'background')).page, 7)
  assert.equal(list.mock.callCount(), 15)
})
