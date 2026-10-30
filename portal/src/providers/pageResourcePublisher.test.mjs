import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom', cacheDir: join(tmpdir(), 'railgrid-vite-page-resource'),
  configFile: false, optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  resolve: { alias: { '@': new URL('../', import.meta.url).pathname } },
  server: { middlewareMode: true, hmr: false },
})
const { createPageResourcePublisher } = await vite.ssrLoadModule('/src/providers/pageResourcePublisher.ts')
const { createAgentContextPublisher } = await vite.ssrLoadModule('/src/providers/agentContextPublisher.ts')
const { normalizePageResource } = await vite.ssrLoadModule('/src/portalkit/page-context.ts')
const { normalizeProviderAgentContext } = await vite.ssrLoadModule('/src/portalkit/agent-context.ts')
const { pageContextForOverlay, pageResourceForScope, providerAgentContextForScope } = await vite.ssrLoadModule('/src/providers/agentOverlayContext.ts')
test.after(() => vite.close())

const hint = { apiVersion: 'infrastructure.railgrid.ai/v1alpha1', kind: 'Instance', resource: 'instances', name: 'sample-app', readTool: 'get_instance' }
const providerContext = {
  version: 1,
  title: 'Purchase order',
  summary: 'Current inventory record',
  resources: [{ apiVersion: 'commerce.example.dev/v1alpha1', kind: 'PurchaseOrder', name: 'record-17' }],
  data: { status: { state: 'Pending' }, labels: ['priority'] },
}

test('resource hints reject malformed coordinates and strip unknown content', () => {
  for (const [key, value] of Object.entries({ apiVersion: 'not-an-api', kind: 'instance/instructions', resource: '../instances', name: 'sample-app\u007f', namespace: 'bad.namespace', uid: 'x'.repeat(65), readTool: 'get_instance;delete' })) {
    assert.equal(normalizePageResource({ ...hint, [key]: value }), null, key)
  }
  assert.equal(normalizePageResource({ ...hint, readTool: 'x'.repeat(65) }), null)
  assert.equal(normalizePageResource({ ...hint, name: 'sample-app\n' }), null)
  assert.deepEqual(normalizePageResource({ ...hint, spec: { password: 'never-share' } }), hint)
  assert.deepEqual(normalizePageResource({ ...hint, apiVersion: 'v1' }), { ...hint, apiVersion: 'v1' })
})

test('only Infrastructure Instance coordinates can use the legacy resource publisher', () => {
  const received = []
  assert.equal(createPageResourcePublisher('agents', () => true, value => received.push(value)), undefined)
  const publish = createPageResourcePublisher('infrastructure', () => true, value => received.push(value))
  for (const extra of [{ apiVersion: 'agents.railgrid.ai/v1alpha1' }, { kind: 'Secret' }, { resource: 'secrets' }, { readTool: 'delete_everything' }]) {
    publish({ ...hint, ...extra })
  }
  assert.deepEqual(received, [null, null, null, null])
  received.length = 0
  publish(hint)
  publish(null)
  assert.deepEqual(received, [hint, null])
  assert.notEqual(received[0], hint)
})

test('generic provider context accepts bounded JSON and copies it immutably', () => {
  const normalized = normalizeProviderAgentContext(providerContext)
  assert.deepEqual(normalized, providerContext)
  assert.notEqual(normalized, providerContext)
  assert.notEqual(normalized.resources[0], providerContext.resources[0])
  assert.notEqual(normalized.data, providerContext.data)
  assert.equal(Object.isFrozen(normalized), true)
  assert.equal(Object.isFrozen(normalized.resources), true)
  assert.equal(Object.isFrozen(normalized.resources[0]), true)
  assert.equal(Object.isFrozen(normalized.data.status), true)
  assert.equal(Object.isFrozen(normalized.data.labels), true)

  for (const extra of [
    { source: 'provider' }, { provenance: { provider: 'orders' } }, { authority: 'read' },
  ]) assert.equal(normalizeProviderAgentContext({ ...providerContext, ...extra }), null)
  assert.equal(normalizeProviderAgentContext({ ...providerContext, resources: [{ ...providerContext.resources[0], spec: { password: 'never-share' } }] }), null)
})

test('generic context rejects getters, custom prototypes, non-JSON values, and cycles without invoking getters', () => {
  let getterCalls = 0
  const accessorData = {}
  Object.defineProperty(accessorData, 'secret', { enumerable: true, get() { getterCalls++; return 'secret' } })
  assert.equal(normalizeProviderAgentContext({ ...providerContext, data: accessorData }), null)
  const accessorEnvelope = { version: 1, title: 'Bad payload' }
  Object.defineProperty(accessorEnvelope, 'data', { enumerable: true, get() { getterCalls++; return {} } })
  assert.equal(normalizeProviderAgentContext(accessorEnvelope), null)
  assert.equal(getterCalls, 0)

  assert.equal(normalizeProviderAgentContext({ ...providerContext, data: Object.create({ inherited: 'no' }) }), null)
  assert.equal(normalizeProviderAgentContext({ ...providerContext, data: { action: () => 'not JSON' } }), null)
  assert.equal(normalizeProviderAgentContext({ ...providerContext, data: { amount: Number.NaN } }), null)
  assert.equal(normalizeProviderAgentContext({ ...providerContext, data: { amount: Number.POSITIVE_INFINITY } }), null)
  const cycle = {}
  cycle.self = cycle
  assert.equal(normalizeProviderAgentContext({ ...providerContext, data: cycle }), null)
})

test('generic context enforces depth, node, collection, string, and UTF-8 bounds', () => {
  assert.equal(normalizeProviderAgentContext({ ...providerContext, title: 'x'.repeat(161) }), null)
  assert.equal(normalizeProviderAgentContext({ ...providerContext, summary: 'x'.repeat(1025) }), null)
  assert.equal(normalizeProviderAgentContext({ ...providerContext, data: { text: 'x'.repeat(2049) } }), null)
  assert.equal(normalizeProviderAgentContext({ ...providerContext, data: { items: Array.from({ length: 33 }, () => 'x') } }), null)

  let deep = 'end'
  for (let index = 0; index < 9; index++) deep = { next: deep }
  assert.equal(normalizeProviderAgentContext({ ...providerContext, data: deep }), null)
  assert.equal(normalizeProviderAgentContext({ ...providerContext, data: Object.fromEntries(Array.from({ length: 65 }, (_, index) => [`key-${index}`, index])) }), null)
  const nodeHeavy = { groups: Array.from({ length: 16 }, () => Array.from({ length: 32 }, (_, index) => index)) }
  assert.equal(normalizeProviderAgentContext({ version: 1, title: 'Many nodes', data: nodeHeavy }), null)

  assert.equal(normalizeProviderAgentContext({ ...providerContext, title: 'x'.repeat(161) }), null)
  const multibyteOversized = { a: 'é'.repeat(1024), b: 'é'.repeat(1024), c: 'é'.repeat(1024), d: 'é'.repeat(1024) }
  assert.equal(normalizeProviderAgentContext({ version: 1, title: 'UTF-8', data: multibyteOversized }), null)
})

test('generic context can be published by any provider and stale publishers are inert', () => {
  let current = true
  const received = []
  const publish = createAgentContextPublisher(() => current, value => received.push(value))
  for (const providerName of ['orders', 'catalog']) {
    publish(providerContext)
    assert.deepEqual(received.at(-1), normalizeProviderAgentContext(providerContext), providerName)
  }
  publish({ ...providerContext, provenance: { provider: 'unreviewed' } })
  assert.equal(received.at(-1), null, 'invalid publication clears the previous context')
  publish(providerContext)
  publish(null)
  assert.equal(received.at(-1), null, 'explicit null clears the current context')
  publish(providerContext)
  current = false
  const count = received.length
  publish({ ...providerContext, title: 'stale' })
  publish(null)
  assert.equal(received.length, count, 'a stale publisher cannot publish or clear')
})

test('overlay reads only current-scope snapshots and omits context when paused', () => {
  const scope = 'alice:org-a:workspace-a:/providers/orders/records/record-17'
  const normalized = normalizeProviderAgentContext(providerContext)
  assert.equal(pageResourceForScope({ scope, resource: hint }, scope), hint)
  assert.equal(providerAgentContextForScope({ scope, context: normalized }, scope), normalized)
  assert.equal(pageResourceForScope({ scope, resource: hint }, 'bob:org-b:workspace-b:/providers/orders/records/record-17'), undefined)
  assert.equal(providerAgentContextForScope({ scope, context: normalized }, 'alice:org-a:workspace-a:/providers/orders/records/record-18'), undefined)
  assert.equal(pageContextForOverlay(false, '{"providerContext":true}'), undefined)
  assert.equal(pageContextForOverlay(true, '{"providerContext":true}'), '{"providerContext":true}')
})
