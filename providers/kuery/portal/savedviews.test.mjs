import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom', cacheDir: join(tmpdir(), 'railgrid-vite-kuery-savedviews'),
  configFile: false, optimizeDeps: { noDiscovery: true },
  root: new URL('./', import.meta.url).pathname,
  server: { middlewareMode: true, hmr: false }, ssr: { external: ['vue'] },
})
const { ensurePlaygroundView, listEdgeInventory, listEdges } = await vite.ssrLoadModule('/src/savedviews.ts')
test.after(() => vite.close())

test('counts only explicitly connected edges while retaining total inventory', async () => {
  const kube = { listAll: async () => [
    { metadata: { name: 'z-connected' }, status: { connected: true } },
    { metadata: { name: 'disconnected' }, status: { connected: false } },
    { metadata: { name: 'unknown' } },
    { metadata: { name: 'a-connected' }, status: { connected: true } },
  ] }
  assert.deepEqual(await listEdgeInventory(kube), { connected: ['a-connected', 'z-connected'], total: 4 })
  assert.deepEqual(await listEdges(kube), ['a-connected', 'z-connected'])
})

test('does not create a missing view after its request becomes stale', async () => {
  let rejectRead
  let created = false
  const kube = { get: () => new Promise((_, reject) => { rejectRead = reject }), create: async () => { created = true } }
  let current = true
  const pending = ensurePlaygroundView(kube, 'view', 'user', () => current)
  current = false
  rejectRead({ status: 404 })
  await pending
  assert.equal(created, false)
})
