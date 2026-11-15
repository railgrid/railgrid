import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom', cacheDir: join(tmpdir(), 'railgrid-vite-kuery-inventory-read'),
  configFile: false, optimizeDeps: { noDiscovery: true }, root: new URL('./', import.meta.url).pathname,
  server: { middlewareMode: true, hmr: false },
})
const { createInventoryReadCoordinator } = await vite.ssrLoadModule('/src/inventory-read.ts')
test.after(() => vite.close())
const deferred = () => {
  let resolve
  const promise = new Promise(accept => { resolve = accept })
  return { promise, resolve }
}
const flush = async () => { for (let i = 0; i < 6; i++) await Promise.resolve() }

test('serializes requests, fences changed filters and coalesces their latest follow-up', async () => {
  const pending = []
  const commits = []
  const busy = []
  let filter = 'initial', active = 0, maximum = 0
  const reader = createInventoryReadCoordinator(async isCurrent => {
    const captured = filter
    active++
    maximum = Math.max(maximum, active)
    const read = deferred()
    pending.push(read)
    await read.promise
    if (isCurrent()) commits.push(captured)
    active--
  }, value => busy.push(value), () => {})
  reader.request()
  filter = 'first edit'
  reader.request(true)
  filter = 'latest edit'
  reader.request(true)
  assert.equal(pending.length, 1)
  pending[0].resolve()
  await flush()
  assert.deepEqual(commits, [])
  assert.equal(pending.length, 2)
  assert.equal(maximum, 1)
  pending[1].resolve()
  await flush()
  assert.deepEqual(commits, ['latest edit'])
  assert.equal(busy.at(-1), false)
  reader.stop()
})

test('allows the current snapshot on a same-query refresh and fences work after stop', async () => {
  const pending = []
  const commits = []
  const reader = createInventoryReadCoordinator(async isCurrent => {
    const read = deferred()
    pending.push(read)
    await read.promise
    if (isCurrent()) commits.push('result')
  }, () => {}, () => {})
  reader.request()
  reader.request()
  pending[0].resolve()
  await flush()
  assert.deepEqual(commits, ['result'])
  assert.equal(pending.length, 2)
  reader.stop()
  pending[1].resolve()
  await flush()
  assert.deepEqual(commits, ['result'])
})

test('aborts an invalidated read and waits for its settlement before replacement', async () => {
  const pending = []
  let aborted = 0
  const reader = createInventoryReadCoordinator(async () => {
    const read = deferred()
    pending.push(read)
    await read.promise
  }, () => {}, () => { aborted++ })
  reader.request()
  reader.request(true)
  assert.equal(aborted, 1)
  assert.equal(pending.length, 1)
  pending[0].resolve()
  await flush()
  assert.equal(pending.length, 2)
  reader.stop()
  assert.equal(aborted, 2)
  pending[1].resolve()
})

test('replaces an abort-ignoring read after the grace period and fences its late result', async () => {
  const pending = []
  const commits = []
  const busy = []
  const reader = createInventoryReadCoordinator(async isCurrent => {
    const read = deferred()
    pending.push(read)
    await read.promise
    if (isCurrent()) commits.push(read)
  }, value => busy.push(value), () => {}, 10)
  reader.request()
  reader.request(true)
  assert.equal(pending.length, 1)
  await new Promise(resolve => setTimeout(resolve, 30))
  assert.equal(pending.length, 2)
  pending[1].resolve()
  await flush()
  assert.deepEqual(commits, [pending[1]])
  assert.equal(busy.at(-1), false)
  pending[0].resolve()
  await flush()
  assert.deepEqual(commits, [pending[1]])
  reader.stop()
})
