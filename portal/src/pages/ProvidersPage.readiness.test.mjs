import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { createServer } from 'vite'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { createPinia, setActivePinia, disposePinia } from 'pinia'

const source = readFileSync(new URL('./ProvidersPage.vue', import.meta.url), 'utf8')
const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'railgrid-vite-provider-binding-action'),
  configFile: false,
  resolve: { alias: { '@': new URL('../', import.meta.url).pathname } },
  optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  server: { middlewareMode: true, hmr: false },
})
const { providerBindingAction } = await vite.ssrLoadModule('/src/lib/providerBindingAction.ts')
const { useProvidersStore } = await vite.ssrLoadModule('/src/stores/providers.ts')
test.after(() => vite.close())

test('provider cards distinguish unavailable providers from pending work', () => {
  assert.match(source, /!p\.ready \? 'Not ready'/)
  assert.match(source, /p\.readinessMessage \|\| 'Provider is unavailable\.'/)
  assert.match(source, /<template v-if="p\.apiExportName">/)
  assert.match(source, /v-else-if="bindingAction\(p\) === 'enable'"/)
  assert.match(source, /v-else-if="bindingAction\(p\) === 'disable'"[\s\S]*@click="onDisable\(p\)"/)
  assert.doesNotMatch(source, /Provider is starting/)
  assert.match(source, /p\.hasUI && p\.ready && \(!p\.apiExportName \|\| providers\.isEnabled\(p\.name\)\)/)
})

test('first-consumer providers remain available in onboarding while dependencies stay gated', () => {
  const pinia = createPinia()
  setActivePinia(pinia)
  try {
    const store = useProvidersStore()
    store.items = [
      { name: 'code', displayName: 'Code', apiExportName: 'code.providers.railgrid.ai', ready: false },
      { name: 'infrastructure', displayName: 'Infrastructure', apiExportName: 'infrastructure.providers.railgrid.ai', ready: false },
      { name: 'builtin', displayName: 'Builtin', ready: true },
    ]
    assert.deepEqual(store.enableable.map(p => p.name), ['code', 'infrastructure'])
    assert.equal(store.hasMissingDependencies({ dependencies: [{ name: 'infrastructure' }] }), true)
    store.items[0].ready = true
    assert.deepEqual(store.enableable.map(p => p.name), ['code', 'infrastructure'])
  } finally {
    disposePinia(pinia)
  }
})

test('provider binding actions cover readiness and enabled state independently', () => {
  const state = (ready, enabled) => ({ hasAPIExport: true, ready, enabled, disabling: false })
  assert.equal(providerBindingAction(state(true, false)), 'enable')
  assert.equal(providerBindingAction(state(false, false)), 'enable')
  assert.equal(providerBindingAction(state(true, true)), 'disable')
  assert.equal(providerBindingAction(state(false, true)), 'disable')
  assert.equal(providerBindingAction({ ...state(true, true), disabling: true }), null)
  assert.equal(providerBindingAction({ ...state(true, false), hasAPIExport: false }), null)
  assert.equal(providerBindingAction({ ...state(false, false), disabling: true }), null)
  assert.equal(providerBindingAction({ ...state(false, false), hasAPIExport: false }), null)
})
