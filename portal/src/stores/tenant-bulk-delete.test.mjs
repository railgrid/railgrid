import assert from 'node:assert/strict'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import test from 'node:test'
import { createServer } from 'vite'
import { createPinia, setActivePinia } from 'pinia'

const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'railgrid-vite-tenant-bulk-delete'),
  configFile: false,
  optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  resolve: { alias: { '@': new URL('../', import.meta.url).pathname } },
  server: { middlewareMode: true, hmr: false, ws: false },
})
const { useTenantStore } = await vite.ssrLoadModule('/src/stores/tenant.ts')
const { clearAuth, saveAuth } = await vite.ssrLoadModule('/src/auth/token.ts')
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

test('deleteWorkspaces deduplicates IDs, reports partial results and does not refresh per item', async () => {
  const { store, restoreWindow } = initializeStore([workspace('ws-a'), workspace('ws-b')])
  const realFetch = globalThis.fetch
  const requests = []
  const progress = []
  store.error = 'keep existing global error'
  globalThis.fetch = async (input, init) => {
    requests.push({ url: String(input), init })
    if (String(input).endsWith('/ws-a')) return response(204)
    if (String(input).endsWith('/ws-b')) {
      return response(500, JSON.stringify({ message: 'Workspace is protected.' }))
    }
    throw new Error(`Unexpected request: ${String(input)}`)
  }

  try {
    const results = await store.deleteWorkspaces('org-a', ['ws-a', 'ws-a', 'ws-b'], {
      onProgress: (result, completed, total) => progress.push({ result, completed, total }),
    })

    assert.deepEqual(results.map(({ uuid, ok, error }) => ({ uuid, ok, error })), [
      { uuid: 'ws-a', ok: true, error: undefined },
      { uuid: 'ws-b', ok: false, error: 'Workspace is protected.' },
    ])
    assert.equal(requests.length, 2, 'only unique workspace DELETEs are sent')
    assert.ok(requests.every(({ init }) => init.method === 'DELETE'))
    for (const { url, init } of requests) {
      assert.match(url, /^\/api\/orgs\/org-a\/workspaces\/ws-[ab]$/)
      const headers = new Headers(init.headers)
      assert.equal(headers.get('X-Railgrid-Org'), 'org-a')
      assert.match(headers.get('X-Railgrid-Workspace'), /^ws-[ab]$/)
    }
    assert.deepEqual(progress.map(({ completed, total }) => ({ completed, total })), [
      { completed: 1, total: 2 },
      { completed: 2, total: 2 },
    ])
    assert.equal(store.error, 'keep existing global error', 'per-item failures stay out of the global error')
    assert.ok(store.workspacesByOrg['org-a'].find((row) => row.uuid === 'ws-a')?.deletionRequestedAt)
    assert.equal(store.workspacesByOrg['org-a'].find((row) => row.uuid === 'ws-b')?.deletionRequestedAt, undefined)

    // The bulk action itself did not list workspaces. A later lagging list
    // also cannot erase the locally acknowledged deletion state.
    assert.equal(requests.length, 2)
    globalThis.fetch = async (input, init) => {
      requests.push({ url: String(input), init })
      return response(200, JSON.stringify({ items: [workspace('ws-a'), workspace('ws-b')] }))
    }
    await store.fetchWorkspaces('org-a', { selectDefault: false })
    assert.equal(requests.length, 3)
    assert.ok(store.workspacesByOrg['org-a'].find((row) => row.uuid === 'ws-a')?.deletionRequestedAt)
  } finally {
    globalThis.fetch = realFetch
    restoreWindow()
  }
})

test('deleteWorkspaces checks cached ownership, admin role, current selection, and deletion state', async () => {
  const { store, restoreWindow } = initializeStore([
    workspace('current', { clusterName: 'current-cluster' }),
    workspace('member-only', { role: 'member' }),
    workspace('deleting', { deletionRequestedAt: '2026-01-01T00:00:00Z' }),
  ])
  store.activateRouteContext(
    { uuid: 'org-a', displayName: 'Organization A', personal: false, role: 'admin' },
    workspace('current', { clusterName: 'current-cluster' }),
  )
  store.workspacesByOrg = {
    'org-a': [
      workspace('current', { clusterName: 'current-cluster' }),
      workspace('member-only', { role: 'member' }),
      workspace('deleting', { deletionRequestedAt: '2026-01-01T00:00:00Z' }),
    ],
    'org-b': [workspace('foreign', { orgUUID: 'org-b' })],
  }
  const realFetch = globalThis.fetch
  globalThis.fetch = () => { throw new Error('validation failures must not send requests') }

  try {
    const results = await store.deleteWorkspaces('org-a', [
      'current', 'foreign', 'member-only', 'deleting', 'missing',
    ])
    assert.deepEqual(results.map((result) => result.error), [
      'Cannot delete the current workspace.',
      'Workspace belongs to another organization.',
      'Workspace administration is required.',
      'Workspace deletion is already in progress.',
      'Workspace is not present in the selected organization.',
    ])
    assert.ok(results.every((result) => !result.ok))
    assert.equal(store.error, null)
  } finally {
    globalThis.fetch = realFetch
    restoreWindow()
  }
})

test('deleteWorkspaces cancels queued requests after tenant selection changes and records in-flight responses', async () => {
  const ids = ['ws-a', 'ws-b', 'ws-c', 'ws-d']
  const { store, restoreWindow } = initializeStore(ids.map((id) => workspace(id)))
  const realFetch = globalThis.fetch
  const pending = []
  const progress = []
  globalThis.fetch = (input, init) => new Promise((resolve) => {
    pending.push({ url: String(input), init, resolve })
  })

  try {
    const deletion = store.deleteWorkspaces('org-a', ids, {
      onProgress: (result, completed, total) => progress.push({ result, completed, total }),
    })
    for (let attempt = 0; attempt < 10 && pending.length < 3; attempt++) {
      await new Promise((resolve) => setImmediate(resolve))
    }
    assert.equal(pending.length, 3, 'at most three DELETEs are dispatched concurrently')

    store.activateRouteContext({
      uuid: 'org-b', displayName: 'Organization B', personal: false, role: 'admin',
    }, null)
    pending.find((request) => request.url.endsWith('/ws-a')).resolve(response(204))
    pending.find((request) => request.url.endsWith('/ws-b')).resolve(response(403))
    pending.find((request) => request.url.endsWith('/ws-c')).resolve(response(204))

    const results = await deletion
    assert.equal(pending.length, 3, 'the fourth request remains undispatched')
    assert.deepEqual(results.map(({ uuid, ok }) => ({ uuid, ok })), [
      { uuid: 'ws-a', ok: true },
      { uuid: 'ws-b', ok: false },
      { uuid: 'ws-c', ok: true },
      { uuid: 'ws-d', ok: false },
    ])
    assert.match(results[1].error, /403/)
    assert.match(results[3].error, /^Not sent:/)
    assert.equal(progress.length, 4)
    assert.equal(progress.at(-1).completed, 4)
    assert.equal(progress.at(-1).total, 4)
    assert.equal(store.workspacesByOrg['org-a'].find((row) => row.uuid === 'ws-a')?.deletionRequestedAt, undefined,
      'a completion from the old scope does not write into its cache after the scope changes')
  } finally {
    globalThis.fetch = realFetch
    restoreWindow()
  }
})

test('deleteWorkspaces honors explicit cancellation before sending', async () => {
  const { store, restoreWindow } = initializeStore([workspace('ws-a'), workspace('ws-b')])
  const realFetch = globalThis.fetch
  let calls = 0
  globalThis.fetch = () => { calls++; throw new Error('canceled batch must not send requests') }

  try {
    const results = await store.deleteWorkspaces('org-a', ['ws-a', 'ws-b'], {
      shouldContinue: () => false,
    })
    assert.equal(calls, 0)
    assert.ok(results.every((result) => !result.ok && result.error?.startsWith('Not sent:')))
  } finally {
    globalThis.fetch = realFetch
    restoreWindow()
  }
})

test('deleteWorkspaces stops queued sends when the authenticated account changes', async () => {
  const ids = ['ws-a', 'ws-b', 'ws-c', 'ws-d']
  const { store, restoreWindow } = initializeStore(ids.map((id) => workspace(id)))
  const realFetch = globalThis.fetch
  const pending = []
  globalThis.fetch = (input, init) => new Promise((resolve) => {
    pending.push({ url: String(input), init, resolve })
  })

  try {
    const deletion = store.deleteWorkspaces('org-a', ids)
    for (let attempt = 0; attempt < 10 && pending.length < 3; attempt++) {
      await new Promise((resolve) => setImmediate(resolve))
    }
    assert.equal(pending.length, 3)

    saveAuth({
      idToken: 'next-account-token',
      expiresAt: 0,
      email: 'next@example.com',
      userId: 'next-account',
      clusterName: 'next-cluster',
    })
    pending.forEach((request) => request.resolve(response(204)))

    const results = await deletion
    assert.equal(pending.length, 3, 'no queued request uses the new account session')
    assert.ok(results.slice(0, 3).every((result) =>
      !result.ok && result.error?.startsWith('Request outcome unavailable:')))
    assert.match(results[3].error, /^Not sent: the authentication context changed/)
  } finally {
    clearAuth()
    globalThis.fetch = realFetch
    restoreWindow()
  }
})

test('late bulk delete success cannot restamp a workspace after a successful restore', async () => {
  const { store, restoreWindow } = initializeStore([workspace('ws-a')])
  const realFetch = globalThis.fetch
  let resolveDelete
  let listCalls = 0
  globalThis.fetch = (input, init = {}) => {
    const url = String(input)
    if (init.method === 'DELETE') {
      return new Promise((resolve) => { resolveDelete = resolve })
    }
    if (init.method === 'POST' && url.endsWith('/ws-a/undelete')) return Promise.resolve(response(204))
    if (url.endsWith('/api/orgs/org-a/workspaces')) {
      listCalls++
      const row = listCalls === 1
        ? workspace('ws-a', { deletionRequestedAt: '2026-09-24T00:00:00Z' })
        : workspace('ws-a')
      return Promise.resolve(response(200, JSON.stringify({ items: [row] })))
    }
    throw new Error(`Unexpected request: ${url}`)
  }

  try {
    const deletion = store.deleteWorkspaces('org-a', ['ws-a'])
    for (let attempt = 0; attempt < 10 && !resolveDelete; attempt++) {
      await new Promise((resolve) => setImmediate(resolve))
    }
    assert.equal(typeof resolveDelete, 'function', 'DELETE is in flight before the restore')

    // The delayed DELETE has reached the server, which now lists the row as
    // deleting. A successful restore reloads it as active before DELETE's
    // response is allowed to finish.
    await store.fetchWorkspaces('org-a', { selectDefault: false })
    assert.ok(store.workspacesByOrg['org-a'].find((row) => row.uuid === 'ws-a')?.deletionRequestedAt)
    assert.equal(await store.undeleteWorkspace('org-a', 'ws-a'), true)
    assert.equal(store.workspacesByOrg['org-a'].find((row) => row.uuid === 'ws-a')?.deletionRequestedAt, undefined)

    resolveDelete(response(204))
    const results = await deletion
    assert.equal(results[0].ok, true, 'the in-flight DELETE result remains truthful')
    assert.equal(store.workspacesByOrg['org-a'].find((row) => row.uuid === 'ws-a')?.deletionRequestedAt, undefined,
      'the late DELETE response cannot restore the obsolete local deleting state')

    await store.fetchWorkspaces('org-a', { selectDefault: false })
    assert.equal(listCalls, 3)
    assert.equal(store.workspacesByOrg['org-a'].find((row) => row.uuid === 'ws-a')?.deletionRequestedAt, undefined,
      'the active server row remains active after a later list merge')
  } finally {
    globalThis.fetch = realFetch
    restoreWindow()
  }
})
