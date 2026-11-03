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
  assert.match(source, /currentWorkspaceBinding\(p\) \? providers\.workspaceProvider\(p\.name\)\?\.ready : p\.ready/)
  assert.match(source, /p\.readinessMessage \|\| 'Provider is unavailable\.'/)
  assert.match(source, /<template v-if="p\.export\?\.name">/)
  assert.match(source, /v-else-if="bindingAction\(p\) === 'enable'"/)
  assert.match(source, /v-else-if="bindingAction\(p\) === 'disable'"[\s\S]*@click="onDisable\(p\)"/)
  assert.doesNotMatch(source, /Provider is starting/)
  assert.match(source, /v-if="canOpenProvider\(p\)"/)
})

test('first-consumer providers remain available in onboarding while dependencies stay gated', () => {
  const pinia = createPinia()
  setActivePinia(pinia)
  try {
    const store = useProvidersStore()
    store.items = [
      { name: 'code', displayName: 'Code', export: { name: 'code.providers.railgrid.ai' }, ready: false },
      { name: 'infrastructure', displayName: 'Infrastructure', export: { name: 'infrastructure.providers.railgrid.ai' }, ready: false },
      { name: 'builtin', displayName: 'Builtin', ready: true },
    ]
    assert.deepEqual(store.enableable.map(p => p.name), ['code', 'infrastructure'])
    // A requires[] entry that names a provider is the dependency edge.
    assert.equal(store.hasMissingDependencies({
      requires: [{ provider: 'infrastructure', group: 'infrastructure.railgrid.ai', resources: [{ name: 'instances', verbs: ['get'] }] }],
    }), true)
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

test('Open uses bound health and UI rather than the offered replacement', () => {
  const body = source.match(/function canOpenProvider\(p: ProviderDTO\): boolean \{([\s\S]*?)\n\}/)?.[1]
  assert.ok(body)
  let bound
  let enabled = true
  let unavailable = false
  const providers = { isEnabled: () => enabled, workspaceProvider: () => bound }
  const canOpen = new Function('p', 'providers', 'bindingProjectionIsCurrent', 'bindingUnavailable', body)
  const check = offered => canOpen(offered, providers, () => true, () => unavailable)
  const replacement = { name: 'edges', ready: false, export: { name: 'edges' }, serving: { ui: {} } }
  bound = { ...replacement, scope: 'global', ready: true }
  assert.equal(check(replacement), true, 'healthy platform binding opens while org replacement is down')
  replacement.ready = true
  assert.equal(check(replacement), true)
  bound = { ...replacement, scope: 'org' }
  assert.equal(check(replacement), true, 'healthy actual org binding opens')
  bound.ready = false
  assert.equal(check(replacement), false)
  bound = undefined
  assert.equal(check(replacement), false, 'removed actual copy cannot load the shadow bundle')
  unavailable = true
  bound = { ...replacement }
  assert.equal(check(replacement), false)
  enabled = false
  unavailable = false
  assert.equal(check(replacement), false, 'unbound exported provider still needs Enable')
})

test('self-hosting connection guidance follows the actual Edges copy', () => {
  const body = source.match(/const edgesSelfHostState = computed<EdgesSelfHostState>\(\(\) => \{([\s\S]*?)\n\}\)/)?.[1]
  assert.ok(body)
  assert.match(source, /const edgesProvider = computed\(\(\) => providers\.workspaceProvider\('edges'\)\)/)
  const state = new Function('edgesProvider', 'providers', body)
  assert.equal(state({ value: { ready: true, serving: { ui: {} } } }, { isEnabled: () => true }), 'ready')
  assert.equal(state({ value: undefined }, { isEnabled: () => true }), 'unready', 'missing bound copy cannot become a catalog fallback')
  assert.equal(state({ value: undefined }, { isEnabled: () => false }), 'absent')
})
