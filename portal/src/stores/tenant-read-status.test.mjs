import assert from 'node:assert/strict'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import test from 'node:test'
import { createServer } from 'vite'
import { createPinia, setActivePinia } from 'pinia'

// This is an executable contract check for the settings read-state bridge.
// Two mounted settings consumers can ask for the same target while one read is
// being replaced. The status bridge must report the latest shared-target
// metadata, while a late response from an older generation stays fenced out.
const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'faros-vite-tenant-read-status'),
  configFile: false,
  optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  resolve: { alias: { '@': new URL('../', import.meta.url).pathname } },
  server: { middlewareMode: true, hmr: false, ws: false },
})
const { useTenantStore } = await vite.ssrLoadModule('/src/stores/tenant.ts')
test.after(() => vite.close())

function installStorage() {
  const values = new Map()
  globalThis.localStorage = {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: (key) => values.delete(key),
    clear: () => values.clear(),
  }
}

function installWindow() {
  const previousWindow = globalThis.window
  const previousCustomEvent = globalThis.CustomEvent
  globalThis.window = { dispatchEvent: () => true }
  if (typeof globalThis.CustomEvent !== 'function') {
    globalThis.CustomEvent = class CustomEvent {
      constructor(type, init = {}) {
        this.type = type
        this.detail = init.detail
      }
    }
  }
  return () => {
    if (previousWindow === undefined) delete globalThis.window
    else globalThis.window = previousWindow
    if (previousCustomEvent === undefined) delete globalThis.CustomEvent
    else globalThis.CustomEvent = previousCustomEvent
  }
}

function response(status, body = '') {
  return new Response(body, {
    status,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
  })
}

test('same-target status uses the latest generation and fences late older responses', async () => {
  installStorage()
  const restoreWindow = installWindow()
  setActivePinia(createPinia())
  const store = useTenantStore()
  const pending = []
  const realFetch = globalThis.fetch
  globalThis.fetch = (input, init) => new Promise((resolve) => {
    pending.push({ input, init, resolve })
  })

  try {
    const first = store.listOrgMembers('org-a')
    const replaced = store.listOrgMembers('org-a')
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(pending.length, 2)

    // The replacement is the latest shared-target generation and succeeds.
    // A late denial from the older generation must not replace its metadata.
    pending[1].resolve(response(200, JSON.stringify({ items: [{ user: 'bob', role: 'admin', orgUUID: 'org-a' }] })))
    assert.equal((await replaced).length, 1)
    pending[0].resolve(response(403))
    assert.deepEqual(await first, [])

    const latest = store.listReadStatus('org-members', 'org-a')
    assert.equal(latest?.status, 200)
    assert.equal(latest?.denied, false)
    assert.equal(store.listReadDenied('org-members', 'org-a'), false)
  } finally {
    globalThis.fetch = realFetch
    restoreWindow()
  }
})

test('list status distinguishes transport, server, and authorization failures', async () => {
  installStorage()
  const restoreWindow = installWindow()
  setActivePinia(createPinia())
  const store = useTenantStore()
  const realFetch = globalThis.fetch
  const queued = []
  globalThis.fetch = () => {
    const next = queued.shift()
    if (next instanceof Error) return Promise.reject(next)
    return Promise.resolve(next)
  }

  try {
    queued.push(response(500))
    assert.deepEqual(await store.listOrgMembers('org-a'), [])
    assert.equal(store.listReadStatus('org-members', 'org-a')?.status, 500)
    assert.equal(store.listReadDenied('org-members', 'org-a'), false)
    assert.match(store.listReadError('org-members', 'org-a'), /500/)

    queued.push(new Error('network offline'))
    assert.deepEqual(await store.listOrgMembers('org-a'), [])
    assert.equal(store.listReadStatus('org-members', 'org-a')?.status, null)
    assert.equal(store.listReadDenied('org-members', 'org-a'), false)
    assert.match(store.listReadError('org-members', 'org-a'), /network offline/)

    queued.push(response(401))
    assert.deepEqual(await store.listOrgMembers('org-a'), [])
    assert.equal(store.listReadStatus('org-members', 'org-a')?.status, 401)
    assert.equal(store.listReadDenied('org-members', 'org-a'), true)

    queued.push(response(403))
    assert.deepEqual(await store.listOrgMembers('org-a'), [])
    assert.equal(store.listReadStatus('org-members', 'org-a')?.status, 403)
    assert.equal(store.listReadDenied('org-members', 'org-a'), true)

    queued.push(response(200, JSON.stringify({ items: [{ user: 'alice', role: 'member', orgUUID: 'org-a' }] })))
    assert.equal((await store.listOrgMembers('org-a')).length, 1)
    assert.equal(store.listReadStatus('org-members', 'org-a')?.status, 200)
    assert.equal(store.listReadDenied('org-members', 'org-a'), false)
    assert.equal(store.listReadError('org-members', 'org-a'), null)
  } finally {
    globalThis.fetch = realFetch
    restoreWindow()
  }
})

test('late same-target read cannot cross an organization authority revision', async () => {
  installStorage()
  const restoreWindow = installWindow()
  setActivePinia(createPinia())
  const store = useTenantStore()
  const realFetch = globalThis.fetch
  const pending = []
  globalThis.fetch = (input) => new Promise((resolve) => {
    pending.push({ input: String(input), resolve })
  })

  try {
    const oldRead = store.listOrgMembers('org-a')
    store.selectOrg('org-b')
    await new Promise((resolve) => setImmediate(resolve))
    const oldRequest = pending.find((request) => request.input.includes('/api/orgs/org-a/memberships'))
    assert.ok(oldRequest)
    oldRequest.resolve(response(403))
    assert.deepEqual(await oldRead, [])

    // The old response can finish, but its selection revision is no longer
    // current. It must not become an authorization result for either context.
    assert.equal(store.listReadStatus('org-members', 'org-a'), null)
    assert.equal(store.listReadDenied('org-members', 'org-a'), false)
  } finally {
    globalThis.fetch = realFetch
    restoreWindow()
  }
})
