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
  .replace("import { defineStore } from 'pinia'\n", '')
  .replace("import { computed, ref } from 'vue'\n", '')
  .replace("import { authFetch } from '@/auth/session'\n", '')
const harness = `
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
  bindingsByProvider: { edges: { bindingName: 'binding-edges', exportPath: 'root:faros:providers:edges', selfHosted: false } },
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
    hasUI: true,
    hasBackend: true,
    apiExportPath: 'root:faros:providers:edges',
    apiExportName: 'edges.providers.faros.sh',
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
