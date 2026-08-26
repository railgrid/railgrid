import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'
import { computed, ref } from 'vue'

async function transpiledModule(path, replacements = {}) {
  const source = await readFile(new URL(path, import.meta.url), 'utf8')
  let { outputText } = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  })
  for (const [from, to] of Object.entries(replacements)) outputText = outputText.replaceAll(from, to)
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
}

const workbench = await transpiledModule('./workbench.ts')
const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const workbenchURL = `data:text/javascript;base64,${Buffer.from(
  ts.transpileModule(await readFile(new URL('./workbench.ts', import.meta.url), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
  }).outputText,
).toString('base64')}`
const persistence = await transpiledModule('./workbenchPersistence.ts', { "from './workbench'": `from '${workbenchURL}'` })

const scope = {
  tenant: 'tenant/a',
  orgUUID: 'org-1',
  workspaceUUID: 'workspace-1',
  userSub: 'user@example.test',
  project: 'project/one',
}

const connectionsTool = {
  id: 'code/connections',
  providerName: 'code',
  title: 'Connections',
  subtitle: 'Code',
  path: 'connections',
}

function memoryStorage(initial = {}) {
  const values = new Map(Object.entries(initial))
  return {
    values,
    getItem(key) { return values.get(key) ?? null },
    setItem(key, value) { values.set(key, value) },
    removeItem(key) { values.delete(key) },
  }
}

test('round-trips stable tab order, active tab, and provider nested path', () => {
  let state = workbench.createDefaultWorkbenchState()
  state = workbench.openWorkbenchBuiltInTab(state, 'publishing')
  state = workbench.openWorkbenchBuiltInTab(state, 'history')
  state = workbench.openWorkbenchBuiltInTab(state, 'settings')
  state = workbench.openWorkbenchProviderTool(state, { ...connectionsTool, provider: { token: 'must-not-persist' } })
  state = workbench.updateWorkbenchProviderToolPath(state, 'provider:code/connections', 'connections/detail')
  const storage = memoryStorage()

  persistence.writeWorkbenchPersistence(scope, state, storage)
  const key = persistence.workbenchPersistenceStorageKey(scope)
  const saved = JSON.parse(storage.getItem(key))
  assert.deepEqual(saved, {
    version: 1,
    tabs: [
      { kind: 'preview' },
      { kind: 'launcher' },
      { kind: 'publishing' },
      { kind: 'history' },
      { kind: 'settings' },
      { kind: 'provider', id: 'code/connections', path: 'connections/detail' },
    ],
    activeTabID: 'provider:code/connections',
  })
  assert.equal(saved.tabs[5].title, undefined)
  assert.equal(state.tabs[5].providerTool.provider, undefined)

  const restored = persistence.restoreWorkbenchState(
    persistence.readWorkbenchPersistence(scope, storage),
    [{ ...connectionsTool, title: 'Canonical connections title' }],
  )
  assert.deepEqual(restored.tabs.map((tab) => tab.id), ['preview', 'launcher', 'publishing', 'history', 'settings', 'provider:code/connections'])
  assert.equal(restored.activeTabID, 'provider:code/connections')
  assert.equal(restored.tabs[5].title, 'Canonical connections title')
  assert.equal(restored.tabs[5].providerTool.path, 'connections/detail')
})

test('migrates the legacy deployments tab and active id to source History', () => {
  const parsed = persistence.parseWorkbenchPersistence(JSON.stringify({
    version: 1,
    tabs: [{ kind: 'preview' }, { kind: 'deployments' }],
    activeTabID: 'deployments',
  }))
  assert.deepEqual(parsed, {
    version: 1,
    tabs: [{ kind: 'preview' }, { kind: 'history' }],
    activeTabID: 'history',
  })

  const restored = persistence.restoreWorkbenchState(parsed, [])
  assert.deepEqual(restored.tabs.map((tab) => tab.id), ['preview', 'history'])
  assert.equal(restored.activeTabID, 'history')
  assert.equal(restored.tabs[1].title, 'History')
})

test('drops the retired Threads tab while preserving the rest of a saved layout', () => {
  const parsed = persistence.parseWorkbenchPersistence(JSON.stringify({
    version: 1,
    tabs: [{ kind: 'preview' }, { kind: 'threads' }, { kind: 'settings' }],
    activeTabID: 'threads',
  }))
  assert.deepEqual(parsed, {
    version: 1,
    tabs: [{ kind: 'preview' }, { kind: 'settings' }],
    activeTabID: '',
  })

  const restored = persistence.restoreWorkbenchState(parsed, [])
  assert.deepEqual(restored.tabs.map((tab) => tab.id), ['preview', 'settings'])
  assert.equal(restored.activeTabID, 'preview')
})

test('scope keys isolate tenant, organization, workspace, user, and project', () => {
  const storage = memoryStorage()
  const state = workbench.createDefaultWorkbenchState()
  persistence.writeWorkbenchPersistence(scope, state, storage)

  for (const field of ['tenant', 'orgUUID', 'workspaceUUID', 'userSub', 'project']) {
    const other = { ...scope, [field]: `${scope[field]}-other` }
    assert.notEqual(persistence.workbenchPersistenceStorageKey(scope), persistence.workbenchPersistenceStorageKey(other))
    assert.equal(persistence.readWorkbenchPersistence(other, storage), null)
  }
  assert.equal(persistence.workbenchPersistenceStorageKey({ ...scope, userSub: '' }), null)
})

test('catalog fingerprints remain usable and isolated when persistence identity is incomplete', () => {
  const incomplete = { ...scope, userSub: null }
  assert.equal(persistence.workbenchPersistenceContextKey(incomplete), null)

  const currentFingerprint = persistence.workbenchCatalogContextFingerprint(incomplete)
  assert.equal(
    currentFingerprint,
    persistence.workbenchCatalogContextFingerprint({ ...incomplete, userSub: '' }),
  )
  assert.notEqual(
    currentFingerprint,
    persistence.workbenchCatalogContextFingerprint({ ...incomplete, workspaceUUID: 'workspace-2' }),
  )
  assert.notEqual(
    currentFingerprint,
    persistence.workbenchCatalogContextFingerprint({ ...incomplete, userSub: 'different-user' }),
  )

  const resolved = persistence.resolveWorkbenchProviderTool(
    { id: connectionsTool.id, path: 'connections/detail' },
    [connectionsTool],
    true,
  )
  assert.equal(resolved.id, connectionsTool.id)
  assert.equal(resolved.path, 'connections/detail')
})

test('reactive catalog readiness invalidates provider-tool derivation', () => {
  assert.match(appSource, /const providerCatalogContextKey = ref<string \| null>\(null\)/)
  assert.match(appSource, /const providerCatalogLoaded = ref\(false\)/)
  assert.match(appSource, /providerCatalogLoaded\.value = true/)

  const incompleteContext = { ...scope, userSub: null }
  const currentContextKey = persistence.workbenchCatalogContextFingerprint(incompleteContext)
  const catalogLoaded = ref(false)
  const catalogContextKey = ref(null)
  const providers = ref([])
  const providerTools = computed(() => {
    if (!catalogLoaded.value || catalogContextKey.value !== currentContextKey) return []
    return providers.value.flatMap((provider) => (provider.children ?? []).map((child) => ({
      id: `${provider.name}/${child.builtinRoute}`,
      title: child.displayName,
    })))
  })
  const provider = {
    name: 'code',
    children: [{ displayName: 'Connections', builtinRoute: 'connections' }],
  }

  assert.deepEqual(providerTools.value, [])
  providers.value = [provider]
  assert.deepEqual(providerTools.value, [], 'provider data must stay hidden until its catalog is marked ready')

  catalogContextKey.value = currentContextKey
  catalogLoaded.value = true
  assert.deepEqual(providerTools.value, [{ id: 'code/connections', title: 'Connections' }])

  catalogContextKey.value = persistence.workbenchCatalogContextFingerprint({ ...incompleteContext, workspaceUUID: 'other-workspace' })
  assert.deepEqual(providerTools.value, [], 'a context change must invalidate the derived tools')
})

test('malformed, unknown, duplicate, oversized, and invalid payloads fall back safely', () => {
  const duplicate = JSON.stringify({ version: 1, tabs: [{ kind: 'preview' }, { kind: 'preview' }], activeTabID: 'preview' })
  const unknownVersion = JSON.stringify({ version: 2, tabs: [{ kind: 'preview' }], activeTabID: 'preview' })
  const unknownTab = JSON.stringify({ version: 1, tabs: [{ kind: 'not-a-tab' }], activeTabID: 'not-a-tab' })
  const oversized = 'x'.repeat(persistence.MAX_WORKBENCH_SERIALIZED_LENGTH + 1)
  assert.equal(persistence.parseWorkbenchPersistence('{not json'), null)
  assert.equal(persistence.parseWorkbenchPersistence(duplicate), null)
  assert.equal(persistence.parseWorkbenchPersistence(unknownVersion), null)
  assert.equal(persistence.parseWorkbenchPersistence(unknownTab), null)
  assert.equal(persistence.parseWorkbenchPersistence(oversized), null)

  const invalidActive = persistence.parseWorkbenchPersistence(JSON.stringify({
    version: 1,
    tabs: [{ kind: 'preview' }, { kind: 'launcher' }],
    activeTabID: 'missing',
  }))
  const restored = persistence.restoreWorkbenchState(invalidActive)
  assert.deepEqual(restored.tabs.map((tab) => tab.id), ['preview', 'launcher'])
  assert.equal(restored.activeTabID, 'preview')
})

test('catalog failure retains provider identities while successful reconciliation refreshes metadata and prunes removed tools', () => {
  const persisted = {
    version: 1,
    tabs: [
      { kind: 'preview' },
      { kind: 'provider', id: 'code/connections', path: 'connections/detail' },
      { kind: 'provider', id: 'edges/services', path: 'services/one' },
    ],
    activeTabID: 'provider:code/connections',
  }
  const unresolved = persistence.restoreWorkbenchState(persisted)
  assert.deepEqual(unresolved.tabs.map((tab) => tab.id), ['preview', 'provider:code/connections', 'provider:edges/services'])
  assert.equal(unresolved.tabs[1].providerTool.path, 'connections/detail')
  const storage = memoryStorage()
  persistence.writeWorkbenchPersistence(scope, unresolved, storage)
  assert.deepEqual(
    persistence.readWorkbenchPersistence(scope, storage).tabs.filter((tab) => tab.kind === 'provider').map((tab) => tab.id),
    ['code/connections', 'edges/services'],
  )

  const reconciled = persistence.reconcileWorkbenchProviderTabs(unresolved, [{
    ...connectionsTool,
    title: 'Connections from catalog',
  }])
  assert.deepEqual(reconciled.tabs.map((tab) => tab.id), ['preview', 'provider:code/connections'])
  assert.equal(reconciled.tabs[1].title, 'Connections from catalog')
  assert.equal(reconciled.tabs[1].providerTool.path, 'connections/detail')
  assert.equal(reconciled.activeTabID, 'provider:code/connections')
})

test('active provider resolution rejects stale catalogs until the current catalog is ready', () => {
  const restoredRef = { id: 'code/connections', path: 'connections/detail' }
  const staleCatalogTool = { ...connectionsTool, title: 'Previous workspace connections' }
  assert.equal(
    persistence.resolveWorkbenchProviderTool(restoredRef, [staleCatalogTool], false),
    null,
  )
  const current = persistence.resolveWorkbenchProviderTool(restoredRef, [staleCatalogTool], true)
  assert.equal(current.title, 'Previous workspace connections')
  assert.equal(current.path, 'connections/detail')
})

test('storage exceptions are best effort and project deletion cleanup is safe', () => {
  const throwing = {
    getItem() { throw new Error('blocked') },
    setItem() { throw new Error('full') },
    removeItem() { throw new Error('blocked') },
  }
  assert.doesNotThrow(() => persistence.readWorkbenchPersistence(scope, throwing))
  assert.doesNotThrow(() => persistence.writeWorkbenchPersistence(scope, workbench.createDefaultWorkbenchState(), throwing))
  assert.doesNotThrow(() => persistence.removeWorkbenchPersistence(scope, throwing))

  const storage = memoryStorage()
  persistence.writeWorkbenchPersistence(scope, workbench.createDefaultWorkbenchState(), storage)
  assert.notEqual(persistence.readWorkbenchPersistence(scope, storage), null)
  persistence.removeWorkbenchPersistence(scope, storage)
  assert.equal(persistence.readWorkbenchPersistence(scope, storage), null)
})
