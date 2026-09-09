import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ configFile: false, server: { middlewareMode: true, hmr: false } })
const { useProjectCreationSubmit } = await vite.ssrLoadModule('/src/useProjectCreationSubmit.ts')
test.after(() => vite.close())

function deferred() {
  let resolve
  const promise = new Promise(done => { resolve = done })
  return { promise, resolve }
}

test('one submission owns pending readiness and creation; failed readiness allows retry', async () => {
  const gate = useProjectCreationSubmit()
  const readiness = deferred()
  const creation = deferred()
  let checks = 0
  let creates = 0
  const ready = () => { checks++; return readiness.promise }
  const create = () => { creates++; return creation.promise }
  const first = gate.run(ready, create)
  await gate.run(ready, create)
  assert.equal(gate.pending.value, true)
  assert.equal(checks, 1)
  readiness.resolve(true)
  await Promise.resolve()
  assert.equal(creates, 1)
  await gate.run(ready, create)
  assert.equal(creates, 1)
  creation.resolve()
  await first
  assert.equal(gate.pending.value, false)
  await gate.run(async () => false, create)
  assert.equal(creates, 1)
  await assert.rejects(gate.run(async () => { throw new Error('offline') }, create), /offline/)
  assert.equal(gate.pending.value, false)
  await gate.run(async () => true, async () => { creates++ })
  assert.equal(creates, 2)
})

test('cancel or scope change invalidates pending readiness without unlocking a newer submission', async () => {
  const gate = useProjectCreationSubmit()
  const oldReady = deferred()
  const newReady = deferred()
  let creates = 0
  const create = async () => { creates++ }
  const old = gate.run(() => oldReady.promise, create)
  gate.invalidate()
  const current = gate.run(() => newReady.promise, create)
  oldReady.resolve(true)
  await old
  assert.equal(creates, 0)
  assert.equal(gate.pending.value, true)
  newReady.resolve(true)
  await current
  assert.equal(creates, 1)
  assert.equal(gate.pending.value, false)
})
