import assert from 'node:assert/strict'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'railgrid-vite-settings-bulk-action'),
  configFile: false,
  optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  resolve: { alias: { '@': new URL('../', import.meta.url).pathname } },
  server: { middlewareMode: true, hmr: false, ws: false },
})
const { useSettingsBulkAction } = await vite.ssrLoadModule('/src/composables/useSettingsBulkAction.ts')
test.after(() => vite.close())

function deferred() {
  let resolve
  const promise = new Promise((done) => { resolve = done })
  return { promise, resolve }
}

function fixture(items = [
  { key: 'sa-a', name: 'Build bot', identity: 'user:builder' },
  { key: 'sa-b', name: 'Release bot', identity: 'user:release' },
  { key: 'sa-c', name: 'Deploy bot', identity: 'user:deployer' },
]) {
  const scope = {
    org: 'org-a',
    workspace: 'ws-a',
    route: '/settings/workspaces',
    generation: 1,
    admin: true,
    section: 'workspaces',
  }
  const state = {
    items: new Map(items.map((item) => [item.key, { ...item }])),
    confirmations: [],
    mutations: [],
    successes: [],
    refreshes: [],
    currentError: null,
    mutation: null,
    confirm: null,
  }
  const action = useSettingsBulkAction({
    captureContext: () => ({ ...scope }),
    isContextCurrent: (context) =>
      context.generation === scope.generation &&
      context.org === scope.org &&
      context.workspace === scope.workspace &&
      context.route === scope.route &&
      context.admin === scope.admin &&
      context.admin &&
      context.section === scope.section &&
      context.section === 'workspaces',
    resolveItems: (_context, keys) => keys.flatMap((key) => {
      const item = state.items.get(key)
      return item ? [item] : []
    }),
    snapshotItem: (item) => JSON.stringify({ key: item.key, name: item.name, identity: item.identity }),
    ineligibleReason: (_context, item) => item.identity === 'auth.self'
      ? 'The current workspace admin cannot be removed.'
      : item.busyOperation
        ? `A ${item.busyOperation} operation is already in progress.`
        : null,
    confirm: async (context, confirmedItems) => {
      state.confirmations.push({ context, items: confirmedItems.map((item) => ({ ...item })) })
      return state.confirm ? state.confirm(context, confirmedItems) : true
    },
    mutate: async (context, item) => {
      state.mutations.push({ context, key: item.key })
      return state.mutation ? state.mutation(context, item) : true
    },
    clearError: () => { state.currentError = null },
    readError: () => state.currentError,
    onSuccess: (context, item) => state.successes.push({ context, key: item.key }),
    refresh: async (context) => { state.refreshes.push(context) },
    fallbackError: 'The request did not complete.',
  })
  return { action, scope, state }
}

async function flushMicrotasks() {
  for (let i = 0; i < 8; i++) await Promise.resolve()
}

test('bulk actions confirm resolved names, process sequentially, and keep only failed keys selected for retry', async () => {
  const h = fixture()
  h.action.selectedKeys.value = ['sa-a', 'sa-b', 'sa-c']
  const firstMutation = deferred()
  const dispatched = []
  h.state.mutation = async (_context, item) => {
    dispatched.push(item.key)
    if (item.key === 'sa-a') return firstMutation.promise
    if (item.key === 'sa-b') {
      h.state.currentError = 'Service account is protected.'
      return false
    }
    return true
  }

  const pending = h.action.run()
  await flushMicrotasks()
  assert.deepEqual(h.state.confirmations[0].items.map((item) => item.name), ['Build bot', 'Release bot', 'Deploy bot'])
  assert.deepEqual(dispatched, ['sa-a'], 'the next DELETE waits for the preceding request')
  assert.equal(h.action.busy.value, true)

  firstMutation.resolve(true)
  await pending
  assert.deepEqual(dispatched, ['sa-a', 'sa-b', 'sa-c'])
  assert.deepEqual(h.action.selectedKeys.value, ['sa-b'])
  assert.deepEqual(h.action.outcomes.value.map(({ key, succeeded, error }) => ({ key, succeeded, error })), [
    { key: 'sa-a', succeeded: true, error: undefined },
    { key: 'sa-b', succeeded: false, error: 'Service account is protected.' },
    { key: 'sa-c', succeeded: true, error: undefined },
  ])
  assert.equal(h.state.refreshes.length, 1)
  assert.equal(h.action.busy.value, false)

  h.state.mutation = async () => true
  await h.action.run()
  assert.deepEqual(h.action.selectedKeys.value, [])
  assert.deepEqual(h.state.confirmations[1].items.map((item) => item.key), ['sa-b'])
  assert.equal(h.state.refreshes.length, 2)
  assert.equal(h.action.outcomes.value[0].succeeded, true)
})

test('a selection changed and restored while confirmation is open still invalidates the request', async () => {
  const h = fixture()
  const confirmation = deferred()
  h.state.confirm = () => confirmation.promise
  h.action.selectedKeys.value = ['sa-a', 'sa-b']

  const pending = h.action.run()
  await flushMicrotasks()
  h.action.selectedKeys.value = []
  h.action.selectedKeys.value = ['sa-a', 'sa-b']
  confirmation.resolve(true)
  await pending

  assert.deepEqual(h.state.mutations, [])
  assert.deepEqual(h.action.selectedKeys.value, ['sa-a', 'sa-b'])
  assert.equal(h.state.refreshes.length, 0)
})

test('an A-to-B-to-A route/workspace transition retires a pending confirmation', async () => {
  const h = fixture()
  const confirmation = deferred()
  h.state.confirm = () => confirmation.promise
  h.action.selectedKeys.value = ['sa-a']

  const pending = h.action.run()
  await flushMicrotasks()
  h.scope.workspace = 'ws-b'
  h.scope.route = '/org-a/ws-b/settings/workspaces'
  h.scope.generation++
  h.action.resetSelection()
  h.scope.workspace = 'ws-a'
  h.scope.route = '/settings/workspaces'
  h.scope.generation++
  h.action.resetSelection()
  confirmation.resolve(true)
  await pending

  assert.deepEqual(h.state.mutations, [])
  assert.deepEqual(h.action.selectedKeys.value, [])
  assert.deepEqual(h.action.outcomes.value, [])
  assert.equal(h.state.refreshes.length, 0)
})

test('permission loss before confirmation closes the dispatch gate', async () => {
  const h = fixture()
  const confirmation = deferred()
  h.state.confirm = () => confirmation.promise
  h.action.selectedKeys.value = ['sa-a']
  const pending = h.action.run()
  await flushMicrotasks()

  h.scope.admin = false
  h.scope.generation++
  h.action.resetSelection()
  confirmation.resolve(true)
  await pending

  assert.deepEqual(h.state.mutations, [])
  assert.deepEqual(h.state.refreshes, [])
  assert.deepEqual(h.action.selectedKeys.value, [])
})

test('permission loss during a request stops queued work and suppresses stale outcomes', async () => {
  const h = fixture()
  const firstMutation = deferred()
  h.action.selectedKeys.value = ['sa-a', 'sa-b']
  h.state.mutation = async (_context, item) => {
    if (item.key === 'sa-a') return firstMutation.promise
    return true
  }

  const pending = h.action.run()
  await flushMicrotasks()
  assert.deepEqual(h.state.mutations.map(({ key }) => key), ['sa-a'])
  h.scope.admin = false
  h.scope.generation++
  h.action.resetSelection()
  firstMutation.resolve(true)
  await pending

  assert.deepEqual(h.state.mutations.map(({ key }) => key), ['sa-a'])
  assert.deepEqual(h.action.selectedKeys.value, [])
  assert.deepEqual(h.action.outcomes.value, [])
  assert.equal(h.state.refreshes.length, 0)
})

test('a successful request still refreshes after an in-flight selection change without restoring cleared keys', async () => {
  const h = fixture()
  const firstMutation = deferred()
  h.action.selectedKeys.value = ['sa-a', 'sa-b']
  h.state.mutation = async (_context, item) => item.key === 'sa-a' ? firstMutation.promise : true

  const pending = h.action.run()
  await flushMicrotasks()
  h.action.selectedKeys.value = []
  firstMutation.resolve(true)
  await pending

  assert.deepEqual(h.state.mutations.map(({ key }) => key), ['sa-a'])
  assert.deepEqual(h.action.selectedKeys.value, [])
  assert.equal(h.state.refreshes.length, 1)
})

test('confirmation revalidates row identity and reports unresolved self membership or conflicting work', async () => {
  const h = fixture([
    { key: 'member-self', name: 'current user', identity: 'auth.self' },
    { key: 'sa-busy', name: 'Busy bot', identity: 'user:busy', busyOperation: 'issue' },
    { key: 'sa-a', name: 'Build bot', identity: 'user:builder' },
  ])
  h.action.selectedKeys.value = ['member-self']
  await h.action.run()
  assert.deepEqual(h.state.confirmations, [], 'an auth.self row is not confirmable')
  assert.deepEqual(h.state.mutations, [])

  h.action.selectedKeys.value = ['sa-busy']
  await h.action.run()
  assert.deepEqual(h.state.confirmations, [], 'a service account with another operation is not confirmable')
  assert.deepEqual(h.state.mutations, [])

  h.action.selectedKeys.value = ['sa-a']
  const confirmation = deferred()
  h.state.confirm = () => confirmation.promise
  const pending = h.action.run()
  await flushMicrotasks()
  h.state.items.set('sa-a', { key: 'sa-a', name: 'Renamed bot', identity: 'user:builder' })
  confirmation.resolve(true)
  await pending
  assert.deepEqual(h.state.mutations, [])
  assert.match(h.action.outcomes.value[0].error, /changed while confirmation was open/)
  assert.equal(h.state.refreshes.length, 0)
})

test('a duplicate run cannot overlap an open confirmation or dispatched request while the prompt stays publicly idle', async () => {
  const h = fixture()
  const confirmation = deferred()
  const mutation = deferred()
  h.state.confirm = () => confirmation.promise
  h.state.mutation = () => mutation.promise
  h.action.selectedKeys.value = ['sa-a']

  const first = h.action.run()
  await flushMicrotasks()
  assert.equal(h.action.busy.value, false, 'opening a modal must not disable its trigger before focus is captured')
  await h.action.run()
  assert.equal(h.state.confirmations.length, 1)
  assert.deepEqual(h.state.mutations, [])
  confirmation.resolve(true)
  await flushMicrotasks()
  assert.equal(h.action.busy.value, true, 'the public busy state begins after confirmation and target revalidation')
  assert.equal(h.state.mutations.length, 1)
  await h.action.run()
  assert.equal(h.state.confirmations.length, 1)
  assert.equal(h.state.mutations.length, 1)
  mutation.resolve(true)
  await first
  assert.deepEqual(h.state.mutations.map(({ key }) => key), ['sa-a'])
  assert.equal(h.action.busy.value, false)
})
