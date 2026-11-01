import assert from 'node:assert/strict'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import test from 'node:test'
import { createServer } from 'vite'
import { createPinia, setActivePinia } from 'pinia'

const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'railgrid-vite-tenant-force-delete'),
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
  return new Response(status === 204 || status === 205 ? null : body, {
    status,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
  })
}

function initializeStore(workspaces) {
  installStorage()
  const restoreWindow = installWindow()
  setActivePinia(createPinia())
  const store = useTenantStore()
  store.activateRouteContext({
    uuid: 'org-a',
    displayName: 'Organization A',
    personal: false,
    role: 'admin',
  }, null)
  store.workspacesByOrg = {
    'org-a': workspaces.map((workspace) => ({ orgUUID: 'org-a', role: 'admin', ...workspace })),
  }
  return { store, restoreWindow }
}

function workspace(uuid, overrides = {}) {
  return { uuid, orgUUID: 'org-a', displayName: uuid, clusterName: `cluster-${uuid}`, role: 'admin', ...overrides }
}

test('forceDeleteWorkspace sends DELETE ?force=true, marks the row forced and adopts the hub projection', async () => {
  const { store, restoreWindow } = initializeStore([workspace('ws-a'), workspace('ws-b')])
  const realFetch = globalThis.fetch
  const requests = []
  globalThis.fetch = async (input, init) => {
    const url = String(input)
    requests.push({ url, init })
    if (init?.method === 'DELETE') return response(204)
    if (url.startsWith('/api/orgs/org-a/workspaces')) {
      return response(200, JSON.stringify({ items: [
        workspace('ws-a', { deletionRequestedAt: '2026-11-01T10:00:00Z', deletionForced: true }),
        workspace('ws-b'),
      ] }))
    }
    throw new Error(`Unexpected request: ${url}`)
  }

  try {
    await store.forceDeleteWorkspace('org-a', 'ws-a')

    assert.equal(requests[0].init.method, 'DELETE')
    assert.equal(requests[0].url, '/api/orgs/org-a/workspaces/ws-a?force=true')
    const headers = new Headers(requests[0].init.headers)
    assert.equal(headers.get('X-Railgrid-Org'), 'org-a')
    assert.equal(headers.get('X-Railgrid-Workspace'), 'ws-a')
    assert.equal(requests.length, 2, 'one DELETE, then one list refresh')

    const forced = store.workspacesByOrg['org-a'].find((row) => row.uuid === 'ws-a')
    assert.equal(forced.deletionForced, true)
    assert.equal(forced.deletionRequestedAt, '2026-11-01T10:00:00Z')
    assert.equal(store.workspacesByOrg['org-a'].find((row) => row.uuid === 'ws-b').deletionForced, undefined)
  } finally {
    globalThis.fetch = realFetch
    restoreWindow()
  }
})

test('forceDeleteWorkspace throws the hub message on rejection and leaves the row untouched', async () => {
  const { store, restoreWindow } = initializeStore([workspace('ws-a')])
  const realFetch = globalThis.fetch
  let requests = 0
  globalThis.fetch = async () => {
    requests++
    return response(403, JSON.stringify({ message: 'Workspace admin access is required.' }))
  }

  try {
    await assert.rejects(() => store.forceDeleteWorkspace('org-a', 'ws-a'), { message: 'Workspace admin access is required.' })
    assert.equal(requests, 1, 'no list refresh after a rejected force delete')
    const row = store.workspacesByOrg['org-a'].find((candidate) => candidate.uuid === 'ws-a')
    assert.equal(row.deletionRequestedAt, undefined)
    assert.equal(row.deletionForced, undefined)
  } finally {
    globalThis.fetch = realFetch
    restoreWindow()
  }
})
