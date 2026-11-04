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

test('organization enable submits the reviewed snapshot and never retries changed consent', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  const provider = {
    name: 'planner', scope: 'org', consentDigest: 'reviewed-permissions',
    export: { name: 'planner.example', path: 'root:railgrid:tenants:org-a:providers:planner' },
  }
  let calls = 0
  globalThis.__providerAuthFetch = async (_url, init) => {
    calls++
    assert.equal(JSON.parse(init.body).consentDigest, 'reviewed-permissions')
    return response(null, { ok: false, status: 412, statusText: 'Precondition Failed' })
  }
  await assert.rejects(store.enable(provider, []), /412/)
  assert.equal(calls, 1)
  await assert.rejects(store.enable({ ...provider, consentDigest: undefined }, []), /Refresh the provider catalog/)
  assert.equal(calls, 1, 'missing consent snapshot must not issue a write')
})

test('SRI refresh follows a platform binding without replacing the org catalog', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  const orgEntry = { name: 'edges', scope: 'org', ownerOrg: 'org-a', shadowsPlatform: true,
    export: { name: 'edges.providers.railgrid.ai', path: 'root:railgrid:tenants:org-a:providers:edges' },
    serving: { ui: { mainJSIntegrity: 'sha384-org' } } }
  const platformEntry = { name: 'edges', scope: 'global', export: { name: 'edges.providers.railgrid.ai', path: 'root:railgrid:providers:edges' },
    serving: { ui: { mainJSIntegrity: 'sha384-platform' } } }
  globalThis.__providerAuthFetch = async (url, init) => {
    if (url === '/api/providers' && init?.headers?.['X-Railgrid-Org'] === 'org-a') return response({ items: [orgEntry] })
    if (url === '/api/providers') return response({ items: [platformEntry] })
    return response({ bindingNamesByProvider: { edges: 'edges' }, bindingsByProvider: {
      edges: { bindingName: 'edges', selfHosted: false, exportPath: 'root:railgrid:providers:edges' },
    } })
  }
  await store.load('org-a')
  await store.loadPlatformCatalog()
  let source = 'root:railgrid:providers:edges'
  globalThis.__providerAuthFetch = async (url, init) => {
    assert.equal(url, '/api/providers')
    assert.equal(init.headers, undefined, 'platform refresh must not send the selected org header')
    return response({ items: [{ name: 'edges', scope: 'global', export: { name: 'edges.providers.railgrid.ai', path: source },
      serving: { ui: { mainJSIntegrity: 'sha384-platform' } } }] })
  }
  assert.equal(await store.refreshMainJSIntegrity('edges'), 'sha384-platform')
  assert.equal(store.byName('edges').scope, 'org', 'SRI refresh must retain the enrollment catalog')
  source = 'root:unrelated:edges'
  assert.equal(await store.refreshMainJSIntegrity('edges'), null, 'a changed export must not supply a retry pin')
})

test('workspace provider metadata follows the exact bound copy instead of the shadow catalog copy', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  const platform = {
    name: 'edges', scope: 'global', displayName: 'Platform Edges', version: '2.0.0', ready: true,
    sourceCluster: 'cluster-platform',
    export: { name: 'edges.providers.railgrid.ai', path: 'root:railgrid:providers:edges' },
    serving: { ui: { children: [{ displayName: 'Workloads', builtinRoute: 'workloads' }] } },
  }
  const orgCopy = {
    name: 'edges', scope: 'org', ownerOrg: 'org-a', shadowsPlatform: true,
    displayName: 'My Edges', version: '2.0.0', ready: false, readinessMessage: 'org copy starting',
    sourceCluster: 'cluster-org-v1',
    export: { name: 'edges.providers.railgrid.ai', path: 'root:railgrid:tenants:org-a:providers:edges' },
    serving: { ui: { children: [{ displayName: 'My Workloads', builtinRoute: 'workloads' }] } },
  }
  let binding = { bindingName: 'edges', exportName: platform.export.name, exportPath: platform.export.path, exportCluster: platform.sourceCluster, selfHosted: false }
  globalThis.__providerAuthFetch = async (url, init) => {
    if (url.endsWith('/providers/enabled')) return response({
      bindingNamesByProvider: { edges: 'edges' }, bindingsByProvider: { edges: binding },
    })
    if (url === '/api/providers' && init?.headers?.['X-Railgrid-Org'] === 'org-a') return response({ items: [orgCopy] })
    if (url === '/api/providers') {
      assert.equal(init?.tenant, false, 'platform metadata reads must omit tenant scope')
      assert.equal(init?.cache, 'no-store')
      assert.equal(init?.redirect, 'error')
      assert.ok(init?.signal instanceof AbortSignal)
      return response({ items: [platform] })
    }
    throw new Error(`unexpected provider request ${url}`)
  }
  await store.load('org-a')
  await store.loadPlatformCatalog()

  assert.equal(store.byName('edges').displayName, 'My Edges', 'catalog keeps the organization enrollment copy')
  assert.equal(store.workspaceProvider('edges').displayName, 'Platform Edges')
  assert.equal(store.workspaceProvider('edges').ready, true, 'the bound platform readiness wins')
  assert.equal(store.enabledNavItems.find(item => item.name === 'edges').label, 'Platform Edges')
  assert.match(store.enabledNavItems.find(item => item.name === 'edges').children[0].to, /workloads$/)
  assert.equal(store.workspaceProviders.find(item => item.name === 'edges').version, '2.0.0')

  platform.version = '3.0.0'
  platform.ready = false
  await store.load('org-a')
  await store.loadPlatformCatalog()
  assert.equal(store.workspaceProvider('edges').version, '3.0.0', 'an explicit catalog refresh updates platform metadata')
  assert.equal(store.workspaceProvider('edges').ready, false)
  assert.equal(store.enabledNavItems.some(item => item.name === 'edges'), false, 'platform readiness, not the shadow copy, gates navigation')
  assert.equal(store.byName('edges').displayName, 'My Edges', 'platform refresh does not replace the self-host enrollment DTO')

  binding = { bindingName: 'edges', exportName: orgCopy.export.name, exportPath: orgCopy.export.path, exportCluster: orgCopy.sourceCluster, selfHosted: true }
  await store.refreshBindings()
  assert.equal(store.workspaceProvider('edges').displayName, 'My Edges')
  orgCopy.sourceCluster = 'cluster-org-v2'
  assert.equal(store.workspaceProviderState('edges'), 'missing', 'same-path source recreation with a different issuer cannot satisfy the old binding')
  binding = { bindingName: 'edges', exportName: orgCopy.export.name, exportPath: orgCopy.export.path, exportCluster: orgCopy.sourceCluster, selfHosted: true }
  await store.refreshBindings()
  assert.equal(store.workspaceProvider('edges').displayName, 'My Edges', 'the refreshed issuer selects the recreated same-name, same-version copy')
  assert.equal(store.workspaceProvider('edges').ready, false)
  assert.equal(store.workspaceProviderState('edges'), 'ready', 'source is resolved even while that copy is unready')
})

test('navigation retains last confirmed provider metadata only during a same-scope refresh', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  const platform = {
    name: 'edges', scope: 'global', displayName: 'Platform Edges', version: '2.0.0', ready: true,
    export: { name: 'edges.providers.railgrid.ai', path: 'root:railgrid:providers:edges' },
    serving: { ui: { children: [{ displayName: 'Workloads', builtinRoute: 'workloads' }] } },
  }
  const orgCopy = {
    name: 'edges', scope: 'org', ownerOrg: 'org-a', shadowsPlatform: true,
    displayName: 'Org Edges', version: '9.0.0', ready: true,
    export: { name: 'edges.providers.railgrid.ai', path: 'root:railgrid:tenants:org-a:providers:edges' },
    serving: { ui: { children: [{ displayName: 'Org Workloads', builtinRoute: 'workloads' }] } },
  }
  globalThis.__providerAuthFetch = async (url, init) => {
    if (url.endsWith('/providers/enabled')) return response({
      bindingNamesByProvider: { edges: 'edges' },
      bindingsByProvider: { edges: { bindingName: 'edges', exportName: platform.export.name, exportPath: platform.export.path, selfHosted: false } },
    })
    if (url === '/api/providers' && init?.headers?.['X-Railgrid-Org'] === 'org-a') return response({ items: [orgCopy] })
    if (url === '/api/providers') return response({ items: [platform] })
    throw new Error(`unexpected provider request ${url}`)
  }
  await store.load('org-a')
  await store.loadPlatformCatalog()
  assert.equal(store.enabledNavItems.find(item => item.name === 'edges').label, 'Platform Edges')

  const pending = deferred()
  globalThis.__providerAuthFetch = () => pending.promise
  const refresh = store.refreshBindings()
  assert.equal(store.workspaceProvider('edges'), undefined, 'runtime selection fails closed while stale')
  assert.equal(store.enabledNavItems.find(item => item.name === 'edges').label, 'Platform Edges', 'navigation retains last confirmed source metadata')
  assert.match(store.enabledNavItems.find(item => item.name === 'edges').children[0].to, /workloads$/)

  setSelection('org-b', 'workspace-b')
  assert.equal(store.workspaceProvider('edges'), undefined)
  assert.equal(store.enabledNavItems.some(item => item.name === 'edges'), false, 'a scope switch immediately clears retained navigation')
  pending.resolve(response({ bindingNamesByProvider: { edges: 'edges' } }))
  await refresh
})

test('an organization binding without its organization catalog entry never falls back to the platform copy', async () => {
  setSelection('org-a', 'workspace-a')
  const store = storeModule.useProvidersStore()
  globalThis.__providerAuthFetch = async url => {
    if (url.endsWith('/providers/enabled')) return response({
      bindingNamesByProvider: { kuery: 'kuery' },
      bindingsByProvider: { kuery: { bindingName: 'kuery', exportName: 'kuery.providers.railgrid.ai', exportPath: 'root:railgrid:tenants:org-a:providers:kuery', selfHosted: true } },
    })
    return response({ items: [{
      name: 'kuery', scope: 'global', displayName: 'Platform Kuery', ready: true,
      export: { name: 'kuery.providers.railgrid.ai', path: 'root:railgrid:providers:kuery' },
      serving: { ui: {} },
    }] })
  }
  await store.load('org-a')

  assert.equal(store.workspaceProviderState('kuery'), 'missing')
  assert.equal(store.workspaceProvider('kuery'), undefined)
  assert.equal(store.workspaceProviders.some(provider => provider.name === 'kuery'), false)
  assert.equal(store.enabledNavItems.some(item => item.name === 'kuery'), false)
})
