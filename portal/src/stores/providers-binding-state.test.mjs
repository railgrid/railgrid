import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import test from 'node:test'
import ts from 'typescript'

const root = path.resolve(new URL('../../../', import.meta.url).pathname)
const storeSource = fs.readFileSync(path.join(root, 'portal', 'src', 'stores', 'providers.ts'), 'utf8')

// The portal does not ship a test runner. Compile the real store with its
// three runtime dependencies replaced by tiny deterministic doubles so these
// tests exercise refreshBindings/enable/disable rather than a copied model.
const sourceWithoutImports = storeSource
  .replace("import { useTenantStore } from './tenant'\n", '')
  .replace("import { scopedPath } from '@/portalkit/navigation'\n", '')
  .replace("import { defineStore } from 'pinia'\n", '')
  .replace("import { computed, ref } from 'vue'\n", '')
  .replace("import { authFetch } from '@/auth/session'\n", '')
const navigationSource = fs.readFileSync(path.join(root, 'portal/src/portalkit/navigation.ts'), 'utf8')
const harness = `
${navigationSource}
const useTenantStore = () => globalThis.__tenantSelection

const ref = (value) => ({ value })
const computed = (getter) => ({ get value() { return getter() } })
const defineStore = (_name, setup) => () => {
  const state = setup()
  return new Proxy(state, {
    get(target, key) {
      const value = Reflect.get(target, key)
      return value && typeof value === 'object' && 'value' in value ? value.value : value
    },
  })
}
const authFetch = (...args) => globalThis.__providerAuthFetch(...args)
`
const { outputText } = ts.transpileModule(`${harness}\n${sourceWithoutImports}`, {
  compilerOptions: {
    target: ts.ScriptTarget.ES2022,
    module: ts.ModuleKind.ESNext,
  },
})
const storeModule = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

let persistedSelection = null
globalThis.localStorage = {
  getItem() {
    return persistedSelection
  },
  setItem(_key, value) {
    persistedSelection = value
  },
}

function setSelection(orgUUID, workspaceUUID, workspaceMode = 'workspace') {
  globalThis.__tenantSelection = { orgUUID, workspaceUUID: workspaceMode === 'organization' ? null : workspaceUUID, workspaceMode }
  persistedSelection = JSON.stringify({ orgUUID, workspaceUUID, workspaceMode })
}

function response(body, { ok = true, status = 200, statusText = 'OK' } = {}) {
  return {
    ok,
    status,
    statusText,
    json: async () => body,
    text: async () => '',
  }
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

async function establishProjection(store, body = {
  bindingNamesByProvider: { edges: 'binding-edges' },
  bindingsByProvider: { edges: { bindingName: 'binding-edges', exportPath: 'root:railgrid:providers:edges', selfHosted: false } },
}) {
  const pending = deferred()
  globalThis.__providerAuthFetch = () => pending.promise
  const request = store.refreshBindings()
  pending.resolve(response(body))
  await request
}

test('same-scope refresh retains navigation projection through loading and error', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  await establishProjection(store)

  const pending = deferred()
  globalThis.__providerAuthFetch = () => pending.promise
  const request = store.refreshBindings()

  assert.equal(store.bindingsLoadState, 'loading')
  assert.equal(store.bindingsStale, true)
  assert.deepEqual(store.bindingNamesByProvider, { edges: 'binding-edges' })
  assert.equal(store.bindingsOrgUUID, 'org-a')
  assert.equal(store.bindingsWorkspace, 'workspace-a')

  pending.reject(new Error('temporary gateway failure'))
  await assert.rejects(request, /temporary gateway failure/)
  assert.equal(store.bindingsLoadState, 'error')
  assert.equal(store.bindingsError, 'temporary gateway failure')
  assert.equal(store.bindingsStale, true)
  assert.deepEqual(store.bindingNamesByProvider, { edges: 'binding-edges' })
})

test('cross-scope and organization-only transitions clear before any new request', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  await establishProjection(store)

  setSelection('org-b', 'workspace-b')
  const next = deferred()
  globalThis.__providerAuthFetch = () => next.promise
  const request = store.refreshBindings()
  assert.equal(store.bindingsLoadState, 'loading')
  assert.equal(store.bindingsStale, false)
  assert.deepEqual(store.bindingNamesByProvider, {})
  assert.deepEqual(store.bindingsByProvider, {})
  assert.equal(store.bindingsOrgUUID, null)
  assert.equal(store.bindingsWorkspace, null)
  assert.equal(store.bindingsRequestOrgUUID, 'org-b')
  assert.equal(store.bindingsRequestWorkspaceUUID, 'workspace-b')

  next.resolve(response({ bindingNamesByProvider: { code: 'binding-code' } }))
  await request
  assert.deepEqual(store.bindingNamesByProvider, { code: 'binding-code' })
  assert.equal(store.bindingsOrgUUID, 'org-b')
  assert.equal(store.bindingsWorkspace, 'workspace-b')

  setSelection('org-b', null, 'organization')
  await store.refreshBindings()
  assert.equal(store.bindingsLoadState, 'idle')
  assert.equal(store.bindingsStale, false)
  assert.deepEqual(store.bindingNamesByProvider, {})
  assert.deepEqual(store.bindingsByProvider, {})
  assert.equal(store.bindingsOrgUUID, null)
  assert.equal(store.bindingsWorkspace, null)
})

test('late results from a previous scope cannot repopulate the active projection', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  await establishProjection(store)

  const old = deferred()
  const current = deferred()
  let requestNumber = 0
  globalThis.__providerAuthFetch = () => (++requestNumber === 1 ? old.promise : current.promise)
  const oldRequest = store.refreshBindings()

  setSelection('org-b', 'workspace-b')
  const currentRequest = store.refreshBindings()
  assert.deepEqual(store.bindingNamesByProvider, {})

  old.resolve(response({ bindingNamesByProvider: { edges: 'stale-old-binding' } }))
  await oldRequest
  assert.deepEqual(store.bindingNamesByProvider, {})
  assert.equal(store.bindingsLoadState, 'loading')
  assert.equal(store.bindingsRequestOrgUUID, 'org-b')
  assert.equal(store.bindingsRequestWorkspaceUUID, 'workspace-b')

  current.resolve(response({ bindingNamesByProvider: { code: 'current-binding' } }))
  await currentRequest
  assert.deepEqual(store.bindingNamesByProvider, { code: 'current-binding' })
  assert.equal(store.bindingsOrgUUID, 'org-b')
  assert.equal(store.bindingsWorkspace, 'workspace-b')
})

test('successful empty responses replace an established projection authoritatively', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  await establishProjection(store)

  const pending = deferred()
  globalThis.__providerAuthFetch = () => pending.promise
  const request = store.refreshBindings()
  assert.deepEqual(store.bindingNamesByProvider, { edges: 'binding-edges' })
  pending.resolve(response({ bindingNamesByProvider: {}, bindingsByProvider: {} }))
  await request

  assert.equal(store.bindingsLoadState, 'ready')
  assert.equal(store.bindingsStale, false)
  assert.deepEqual(store.bindingNamesByProvider, {})
  assert.deepEqual(store.bindingsByProvider, {})
  assert.equal(store.bindingsOrgUUID, 'org-a')
  assert.equal(store.bindingsWorkspace, 'workspace-a')
})

test('disable keeps the current projection until its post-write refresh completes', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  await establishProjection(store)

  const refresh = deferred()
  globalThis.__providerAuthFetch = (url, init) => {
    if (init?.method === 'POST') return Promise.resolve(response(null, { status: 204, statusText: 'No Content' }))
    return refresh.promise
  }
  const request = store.disable({ name: 'edges' })
  for (let i = 0; i < 10 && store.bindingsLoadState !== 'loading'; i++) await Promise.resolve()
  assert.equal(store.bindingsLoadState, 'loading')
  assert.equal(store.bindingsStale, true)
  assert.deepEqual(store.bindingNamesByProvider, { edges: 'binding-edges' })

  refresh.resolve(response({ bindingNamesByProvider: {}, bindingsByProvider: {} }))
  await request
  assert.equal(store.bindingsLoadState, 'ready')
  assert.deepEqual(store.bindingNamesByProvider, {})
})

test('enable keeps the current projection until its post-write refresh completes', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  await establishProjection(store)

  const refresh = deferred()
  globalThis.__providerAuthFetch = (url, init) => {
    if (init?.method === 'POST') return Promise.resolve(response(null))
    return refresh.promise
  }
  const request = store.enable({
    name: 'edges',
    displayName: 'Edges',
    ready: true,
    serving: { ui: {}, backend: {} },
    export: { name: 'edges.providers.railgrid.ai', path: 'root:railgrid:providers:edges' },
  }, [])
  for (let i = 0; i < 10 && store.bindingsLoadState !== 'loading'; i++) await Promise.resolve()
  assert.equal(store.bindingsLoadState, 'loading')
  assert.equal(store.bindingsStale, true)
  assert.deepEqual(store.bindingNamesByProvider, { edges: 'binding-edges' })

  refresh.resolve(response({ bindingNamesByProvider: { edges: 'binding-edges' } }))
  await request
  assert.equal(store.bindingsLoadState, 'ready')
  assert.equal(store.bindingsStale, false)
  assert.deepEqual(store.bindingNamesByProvider, { edges: 'binding-edges' })
})

test('enable replaces an older platform catalog read before exposing the org binding', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  const platform = { name: 'code', scope: 'global', displayName: 'Code', ready: true, serving: { ui: {} }, export: { name: 'code.export', path: 'root:railgrid:providers:code' } }
  const org = { ...platform, scope: 'org', ownerOrg: 'org-a', export: { ...platform.export, path: 'root:railgrid:tenants:org-a:providers:code' } }
  const oldCatalog = deferred()
  let catalogReads = 0
  globalThis.__providerAuthFetch = (url, init) => {
    if (init?.method === 'POST') return Promise.resolve(response(null))
    if (url === '/api/providers') {
      catalogReads += 1
      return catalogReads === 1 ? oldCatalog.promise : Promise.resolve(response({ items: [org] }))
    }
    return Promise.resolve(response({ bindingNamesByProvider: { code: 'code' }, bindingsByProvider: { code: { bindingName: 'code', exportPath: org.export.path, selfHosted: true, ready: true } } }))
  }
  const staleRead = store.load('org-a')
  await store.enable(platform, [])
  assert.equal(catalogReads, 2)
  assert.equal(store.byName('code').scope, 'org')
  assert.equal(store.bindingsByProvider.code.selfHosted, true)
  oldCatalog.resolve(response({ items: [platform] }))
  await staleRead
  assert.equal(store.byName('code').scope, 'org', 'a pre-enable catalog response must not reinstall the platform bundle selection')
})

test('workspace destinations follow the exact bound copy across org shadows and outages', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  const platform = { name: 'edges', displayName: 'Platform Edges', scope: 'global', ready: true,
    export: { path: 'root:railgrid:providers:edges', name: 'edges.export' },
    serving: { ui: { mainJSIntegrity: 'sha384-platform', children: [{ displayName: 'Clusters', builtinRoute: 'clusters' }] } } }
  const org = { ...platform, displayName: 'Org Edges', scope: 'org', ownerOrg: 'org-a',
    export: { path: 'root:tenants:org-a:providers:edges', name: 'edges.export' },
    serving: { ui: { mainJSIntegrity: 'sha384-org' } } }
  store.items.push(org)
  for (const shadowReady of [true, false]) {
    org.ready = shadowReady
    await establishProjection(store, {
      bindingNamesByProvider: { edges: 'edges' },
      bindingsByProvider: { edges: { bindingName: 'edges', exportPath: platform.export.path,
        exportName: platform.export.name, ready: true, boundProvider: platform } },
    })
    assert.equal(store.byName('edges'), org, 'catalog keeps the org installation choice')
    assert.equal(store.workspaceProvider('edges'), platform, 'workspace retains the bound platform copy')
    assert.equal(store.workspaceProvider('edges').serving.ui.mainJSIntegrity, 'sha384-platform')
    assert.deepEqual(store.enabledNavItems.map(p => [p.name, p.label]), [['edges', 'Platform Edges']])
    assert.match(store.enabledNavItems[0].children[0].to, /\/providers\/edges\/clusters$/)
  }
  await establishProjection(store, {
    bindingNamesByProvider: { edges: 'edges' },
    bindingsByProvider: { edges: { bindingName: 'edges', exportPath: org.export.path,
      exportName: org.export.name, ready: true, selfHosted: true, boundProvider: org } },
  })
  assert.equal(store.workspaceProvider('edges'), org, 'native org binding selects org bundle')
  assert.equal(store.workspaceProvider('edges').ready, false, 'actual provider outage remains visible')
  assert.equal(store.enabledNavItems[0].label, 'Org Edges')
  // An absent registration and a wrong-copy DTO must retain the destination,
  // but neither may fall back to a same-name shadow bundle.
  for (const boundProvider of [undefined, platform, { ...org, export: { ...org.export, name: 'wrong-export' } }]) {
    await establishProjection(store, {
      bindingNamesByProvider: { edges: 'edges' },
      bindingsByProvider: { edges: { bindingName: 'edges', exportPath: org.export.path,
        exportName: org.export.name, ready: true, boundProvider } },
    })
    assert.equal(store.workspaceProvider('edges'), undefined)
    assert.equal(store.enabledNavItems[0].name, 'edges')
    assert.deepEqual(store.enabledNavItems[0].children, [])
  }
  store.items.splice(0)
  assert.equal(store.enabledNavItems[0].name, 'edges', 'removed catalog copy remains a reviewable bound destination')
  setSelection('org-b', 'workspace-b')
  assert.equal(store.workspaceProvider('edges'), undefined, 'never reuse another workspace projection')
})

test('known API-only bound providers do not acquire sidebar destinations', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  await establishProjection(store, {
    bindingNamesByProvider: { api: 'api' },
    bindingsByProvider: { api: { bindingName: 'api', exportPath: 'root:api', boundProvider: {
      name: 'api', displayName: 'API only', ready: true, export: { path: 'root:api' },
    } } },
  })
  assert.equal(store.workspaceProvider('api').name, 'api')
  assert.deepEqual(store.enabledNavItems, [])
})

test('SRI retry refreshes the native bound copy rather than the shadow catalog', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  const body = {
    bindingNamesByProvider: { edges: 'edges' },
    bindingsByProvider: { edges: { bindingName: 'edges', exportPath: 'root:platform:edges', boundProvider: {
      name: 'edges', ready: true, export: { path: 'root:platform:edges' },
      serving: { ui: { mainJSIntegrity: 'sha384-fresh-platform' } },
    } } },
  }
  await establishProjection(store, body)
  const requests = []
  globalThis.__providerAuthFetch = async path => { requests.push(path); return response(body) }
  assert.equal(await store.refreshWorkspaceMainJSIntegrity('edges'), 'sha384-fresh-platform')
  assert.equal(requests.length, 1)
  assert.match(requests[0], /\/providers\/enabled$/)
})

test('enabled dependencies and onboarding follow the actual copy while org offers are unavailable', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  const edges = { name: 'edges', scope: 'global', displayName: 'Platform Edges', ready: true,
    export: { path: 'root:railgrid:providers:edges', name: 'edges.export' }, serving: { ui: {} } }
  const agents = { ...edges, name: 'agents', displayName: 'Platform Agents',
    export: { path: 'root:railgrid:providers:agents', name: 'agents.export' }, requires: [{ provider: 'edges' }] }
  store.items.push({ ...edges, scope: 'org', ownerOrg: 'org-a', ready: false },
    { ...agents, scope: 'org', ownerOrg: 'org-a', ready: false, requires: [{ provider: 'missing-org-only-dependency' }] })
  const body = {
    bindingNamesByProvider: { edges: 'edges', agents: 'agents' },
    bindingsByProvider: Object.fromEntries([edges, agents].map(p => [p.name, {
      bindingName: p.name, exportPath: p.export.path, exportName: p.export.name, ready: true, boundProvider: p,
    }])),
  }
  await establishProjection(store, body)
  assert.equal(store.hasMissingDependencies({ requires: [{ provider: 'edges' }] }), false)
  assert.equal(store.hasMissingDependencies(agents), false, 'a healthy native dependency must not be blocked by its unready org offer')

  const wizard = fs.readFileSync(path.join(root, 'portal/src/components/WelcomeWizard.vue'), 'utf8')
  const target = wizard.match(/const target = computed\(\(\) => ([^\n]+)\)/)[1]
  const plan = wizard.match(/const plan = computed\(\(\) => ([^\n]+)\)/)[1]
  const planningSource = fs.readFileSync(path.join(root, 'portal/src/lib/onboardingPlan.ts'), 'utf8')
  const planning = await import(`data:text/javascript;base64,${Buffer.from(ts.transpileModule(planningSource, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
  }).outputText).toString('base64')}`)
  const goal = { value: 'agents' }
  assert.equal(new Function('providers', 'goal', `return ${target}`)(store, goal).scope, 'global')
  const rows = new Function('providers', 'goal', 'planFor', `return ${plan}`)(store, goal, planning.planFor)
  assert.deepEqual(rows.map(p => [p.name, p.provider.scope]), [['edges', 'global'], ['agents', 'global']])
  assert.equal(planning.setupReadyFor(true, true, rows, store.isEnabled), true)

  for (const changed of [{ ready: false }, { terminating: true }, { boundProvider: undefined }]) {
    const invalid = structuredClone(body)
    Object.assign(invalid.bindingsByProvider.edges, changed)
    await establishProjection(store, invalid)
    assert.equal(store.hasMissingDependencies({ requires: [{ provider: 'edges' }] }), true)
    assert.equal(store.hasMissingDependencies(agents), true)
  }
  await establishProjection(store, body)
  setSelection('org-b', 'workspace-b')
  assert.equal(store.hasMissingDependencies({ requires: [{ provider: 'edges' }] }), true, 'old workspace bindings cannot satisfy a new scope')
})
