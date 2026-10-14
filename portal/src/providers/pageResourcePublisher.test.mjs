import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom', cacheDir: join(tmpdir(), 'railgrid-vite-page-resource'),
  configFile: false, optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  server: { middlewareMode: true, hmr: false },
})
const { createPageResourcePublisher } = await vite.ssrLoadModule('/src/providers/pageResourcePublisher.ts')
const { normalizePageResource } = await vite.ssrLoadModule('/src/portalkit/page-context.ts')
test.after(() => vite.close())
const hint = { apiVersion: 'infrastructure.railgrid.ai/v1alpha1', kind: 'Instance', resource: 'instances', name: 'cache', readTool: 'get_instance' }

test('resource hints reject malformed coordinates and strip unknown content', () => {
  for (const [key, value] of Object.entries({ apiVersion: 'not-an-api', kind: 'instance/instructions', resource: '../instances', name: 'cache\u007f', namespace: 'bad.namespace', uid: 'x'.repeat(65), readTool: 'get_instance;delete' })) {
    assert.equal(normalizePageResource({ ...hint, [key]: value }), null, key)
  }
  assert.equal(normalizePageResource({ ...hint, readTool: 'x'.repeat(65) }), null)
  assert.equal(normalizePageResource({ ...hint, name: 'cache\n' }), null)
  assert.deepEqual(normalizePageResource({ ...hint, spec: { password: 'never-share' } }), hint)
  assert.deepEqual(normalizePageResource({ ...hint, apiVersion: 'v1' }), { ...hint, apiVersion: 'v1' })
})

test('only the supported provider and read coordinates can publish', () => {
  const received = []
  assert.equal(createPageResourcePublisher('agents', () => true, v => received.push(v)), undefined)
  const publish = createPageResourcePublisher('infrastructure', () => true, v => received.push(v))
  for (const extra of [{ apiVersion: 'agents.railgrid.ai/v1alpha1' }, { kind: 'Secret' }, { resource: 'secrets' }, { readTool: 'delete_everything_now' }]) publish({ ...hint, ...extra })
  assert.deepEqual(received, [null, null, null, null])
  received.length = 0
  publish(hint)
  publish({ ...hint, readTool: 'delete_instance' })
  assert.deepEqual(received, [hint, null])
  publish(null)
  assert.equal(received.at(-1), null)
  assert.notEqual(received[0], hint)
})

test('retained callbacks cannot publish or clear after losing route ownership', () => {
  let current = true
  const received = []
  const publish = createPageResourcePublisher('infrastructure', () => current, v => received.push(v))
  publish(hint)
  current = false
  publish({ ...hint, name: 'other-workspace' })
  publish(null)
  assert.deepEqual(received, [hint])
})
