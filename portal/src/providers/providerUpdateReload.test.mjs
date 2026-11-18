import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'railgrid-vite-provider-update-reload'),
  configFile: false,
  optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  server: { middlewareMode: true, hmr: false },
})
const { reloadForProviderUpdate, clearProviderUpdateReload, PROVIDER_UPDATE_RELOAD_WINDOW_MS } =
  await vite.ssrLoadModule('/src/providers/providerUpdateReload.ts')
test.after(() => vite.close())

function memoryStorage() {
  const items = new Map()
  return { getItem: k => items.get(k) ?? null, setItem: (k, v) => { items.set(k, String(v)) }, removeItem: k => { items.delete(k) } }
}

test('reloads once for a provider update, then refuses to loop within the window', () => {
  const storage = memoryStorage()
  let reloads = 0
  const reload = () => { reloads++ }
  assert.equal(reloadForProviderUpdate('code', { storage, reload, now: 1_000 }), true)
  assert.equal(reloadForProviderUpdate('code', { storage, reload, now: 2_000 }), false)
  assert.equal(reloads, 1)
  // Another provider, or the same one after the window, reloads again.
  assert.equal(reloadForProviderUpdate('edges', { storage, reload, now: 3_000 }), true)
  assert.equal(reloadForProviderUpdate('code', { storage, reload, now: 3_000 + PROVIDER_UPDATE_RELOAD_WINDOW_MS }), true)
  assert.equal(reloads, 3)
})

test('without usable session storage it never reloads', () => {
  let reloads = 0
  const storage = { getItem() { throw new Error('blocked') }, setItem() { throw new Error('blocked') } }
  assert.equal(reloadForProviderUpdate('code', { storage, reload: () => { reloads++ }, now: 1 }), false)
  assert.equal(reloads, 0)
})

test('a corrupt or future-dated marker does not block the reload', () => {
  const storage = memoryStorage()
  let reloads = 0
  const reload = () => { reloads++ }
  storage.setItem('railgrid:provider-update-reload', '{not json')
  assert.equal(reloadForProviderUpdate('code', { storage, reload, now: 5 }), true)
  storage.setItem('railgrid:provider-update-reload', JSON.stringify({ name: 'code', at: 10_000 }))
  assert.equal(reloadForProviderUpdate('code', { storage, reload, now: 5 }), true)
  assert.equal(reloads, 2)
})

test('a successful mount clears only its own provider guard', () => {
  const storage = memoryStorage()
  let reloads = 0
  const reload = () => { reloads++ }
  assert.equal(reloadForProviderUpdate('code', { storage, reload, now: 1_000 }), true)
  clearProviderUpdateReload('edges', { storage })
  assert.equal(reloadForProviderUpdate('code', { storage, reload, now: 2_000 }), false)
  clearProviderUpdateReload('code', { storage })
  // A second deploy inside the window gets its page load again.
  assert.equal(reloadForProviderUpdate('code', { storage, reload, now: 3_000 }), true)
  assert.equal(reloads, 2)
  assert.doesNotThrow(() => clearProviderUpdateReload('code', { storage: { getItem() { throw new Error('blocked') }, removeItem() {} } }))
})
