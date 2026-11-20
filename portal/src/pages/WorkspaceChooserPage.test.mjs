import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, nextTick, reactive, ref } from 'vue'

// Execute the component's setup and async actions, with controlled API replies
// and timers. This exercises polling, error handling and teardown rather than
// asserting the presence of source strings.
const source = readFileSync(new URL('./WorkspaceChooserPage.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const code = ts.transpileModule(script.replace(/^import .*$/gm, ''), {
  compilerOptions: { target: ts.ScriptTarget.ES2020 },
}).outputText
const selectionSource = readFileSync(new URL('../router/workspaceEntry.ts', import.meta.url), 'utf8')
const selectionCode = ts.transpileModule(selectionSource.replace(/^import .*$/gm, '').replace('export function', 'function'), {
  compilerOptions: { target: ts.ScriptTarget.ES2020 },
}).outputText
const available = row => !!row && !row.deletionRequestedAt
const usable = row => available(row) && !!row.clusterName
const preferredWorkspace = runInNewContext(`${selectionCode}\npreferredWorkspace`, {
  isWorkspaceAvailable: available, isWorkspaceUsable: usable,
})

function setup(historyState = {}) {
  let onDispose
  let onOrgChange
  let now = 0
  const timers = new Map()
  const navigations = []
  const reloads = []
  const org = { uuid: 'org-a', displayName: 'Team', role: 'admin', initialWorkspacePending: true }
  const route = { params: { orgID: 'org-a' }, query: {}, fullPath: '/org-a/workspaces' }
  const history = { state: historyState, replace: (_path, state) => { history.state = state } }
  const state = {
    org, rows: [], orgStatus: 200, workspaceStatus: 'ready',
    orgRead: null, createResult: null, createCalls: 0, navigate: null, create: null,
  }
  const tenant = reactive({
    activeOrg: org, workspacesByOrg: {}, workspaceLoadStateByOrg: {},
    async fetchWorkspaces(id) {
      this.workspacesByOrg[id] = state.rows
      this.workspaceLoadStateByOrg[id] = state.workspaceStatus
    },
    async createWorkspace() { state.createCalls++; return state.create ? state.create() : state.createResult },
  })
  const api = runInNewContext(`${code}\n({ load, retry, retryNavigation, enter, create, loading, error, createError, navigationError, opening, hasSnapshot, waiting, timedOut, creating, name, org })`, {
    ref, computed, nextTick, useRoute: () => route,
    useRouter: () => ({
      options: { history },
      replace: async to => { navigations.push(to); return state.navigate?.(to) },
    }),
    window: { location: { reload: () => reloads.push(route.fullPath) } },
    useTenantStore: () => tenant, useAuthStore: () => ({ user: { userId: 'alice' } }),
    useRouteContextStore: () => ({ blocksRoute: () => false }),
    authFetch: async () => state.orgRead ? state.orgRead() : new Response(JSON.stringify(state.org), { status: state.orgStatus }),
    isWorkspaceAvailable: available, isWorkspaceUsable: usable, preferredWorkspace,
    readOrganizationWorkspace: () => null,
    watch: (source, callback) => { if (!Array.isArray(source)) onOrgChange = callback },
    onBeforeUnmount: callback => { onDispose = callback },
    setTimeout: callback => { const key = Symbol(); timers.set(key, callback); return key },
    clearTimeout: key => timers.delete(key),
    Date: { now: () => now }, Error,
  })
  return { api, state, tenant, timers, navigations, reloads, history, route, dispose: () => onDispose(), changeOrg: () => onOrgChange(), advance: ms => { now += ms } }
}

test('direct org entry polls an empty pending bootstrap without a preparing query and enters when ready', async () => {
  const { api, state, timers, navigations } = setup()
  await api.retry()
  assert.equal(api.waiting.value, true)
  assert.equal(timers.size, 1)
  assert.equal(navigations.length, 0)
  state.org = { ...state.org, initialWorkspacePending: false }
  state.rows = [{ uuid: 'ws-a', orgUUID: 'org-a', clusterName: 'cluster-a' }]
  await api.load()
  assert.equal(navigations.length, 1)
  assert.equal(navigations[0].params.workspaceID, 'ws-a')
})

test('completed empty orgs stop waiting and do not poll based on a stale preparing query', async () => {
  const { api, state, timers, route } = setup()
  route.query.preparing = '1'
  state.org = { ...state.org, initialWorkspacePending: false }
  await api.retry()
  assert.equal(api.waiting.value, false)
  assert.equal(timers.size, 0)
})

test('a ready workspace stops showing preparation even when automatic navigation fails', async () => {
  const { api, state, timers, advance } = setup()
  await api.retry()
  timers.clear()
  advance(60_001)
  await api.load()
  assert.equal(api.timedOut.value, true)
  state.org.initialWorkspacePending = false
  state.rows = [{ uuid: 'ws-a', clusterName: 'cluster-a' }]
  state.navigate = async () => { throw new Error('Navigation unavailable') }
  await api.load()
  assert.equal(api.waiting.value, false)
  assert.equal(api.timedOut.value, false)
  assert.equal(api.navigationError.value.workspace.uuid, 'ws-a')
  assert.equal(timers.size, 0)
})

test('organization and workspace failures stop polling instead of displaying stale readiness', async () => {
  for (const failure of ['organization', 'workspace']) {
    const { api, state, timers } = setup()
    if (failure === 'organization') state.orgStatus = 503
    else state.workspaceStatus = 'error'
    await api.retry()
    assert.ok(api.error.value)
    assert.equal(api.waiting.value, false)
    assert.equal(timers.size, 0)
  }
})

test('late readiness results cannot navigate after leaving the chooser', async () => {
  const { api, state, dispose, timers, navigations } = setup()
  let resolve
  state.orgRead = () => new Promise(done => { resolve = done })
  state.rows = [{ uuid: 'ws-a', orgUUID: 'org-a', clusterName: 'cluster-a' }]
  const pending = api.retry()
  dispose()
  resolve(new Response(JSON.stringify({ ...state.org, initialWorkspacePending: false })))
  await pending
  assert.equal(navigations.length, 0)
  assert.equal(timers.size, 0)
})

test('pending initial creation respects the timeout and retry restarts polling', async () => {
  const { api, timers, advance } = setup()
  await api.retry()
  timers.clear()
  advance(60_001)
  await api.load()
  assert.equal(api.timedOut.value, true)
  assert.equal(timers.size, 0)
  await api.retry()
  assert.equal(api.timedOut.value, false)
  assert.equal(timers.size, 1)
})

test('successful creation releases its submit state even if the subsequent refresh fails', async () => {
  const { api, state } = setup()
  state.createResult = { uuid: 'ws-a', orgUUID: 'org-a' }
  state.orgStatus = 503
  api.name.value = 'Development'
  await api.create()
  assert.equal(state.createCalls, 1)
  assert.equal(api.creating.value, false)
  assert.ok(api.error.value)
})

test('reads completed organization readiness before taking the workspace snapshot', async () => {
  const { api, state, tenant, navigations } = setup()
  let resolve
  state.orgRead = () => new Promise(done => { resolve = done })
  const pending = api.retry()
  assert.equal(tenant.workspaceLoadStateByOrg['org-a'], undefined)
  // Bootstrap finishes while the organization response is in flight. Reading
  // workspaces in parallel would have captured the earlier empty list.
  state.rows = [{ uuid: 'ws-a', orgUUID: 'org-a', clusterName: 'cluster-a' }]
  resolve(new Response(JSON.stringify({ ...state.org, initialWorkspacePending: false })))
  await pending
  assert.equal(navigations.length, 1)
})

test('read retry retains its failure and snapshot while preventing duplicate reads', async () => {
  const { api, state } = setup()
  state.org.initialWorkspacePending = false
  await api.retry()
  state.orgStatus = 503
  await api.retry()
  const failure = api.error.value
  let resolve, calls = 0
  state.orgRead = () => { calls++; return new Promise(done => { resolve = done }) }
  const retry = api.retry()
  await api.retry()
  assert.equal(calls, 1)
  assert.equal(api.error.value, failure)
  assert.equal(api.hasSnapshot.value, true)
  resolve(new Response(JSON.stringify(state.org)))
  await retry
  assert.equal(api.error.value, '')
  assert.equal(api.loading.value, false)
})

test('failed creation preserves its draft and read state through a guarded explicit resubmit', async () => {
  const { api, state } = setup()
  state.org.initialWorkspacePending = false
  await api.retry()
  api.name.value = 'Development'
  state.create = async () => { throw new Error('Create unavailable') }
  await api.create()
  assert.equal(api.createError.value, 'Create unavailable')
  assert.equal(api.error.value, '')
  assert.equal(api.name.value, 'Development')
  let resolve
  state.create = () => new Promise(done => { resolve = done })
  const pending = api.create()
  await api.create()
  assert.equal(state.createCalls, 2)
  assert.equal(api.createError.value, 'Create unavailable')
  resolve({ uuid: 'ws-new' })
  await pending
  assert.equal(api.createError.value, '')
  assert.equal(api.name.value, '')
})

test('navigation retry opens the failed target rather than refreshing or creating', async () => {
  const { api, state, navigations } = setup()
  state.org.initialWorkspacePending = false
  state.rows = ['a', 'b'].map(id => ({ uuid: `ws-${id}`, clusterName: `cluster-${id}` }))
  await api.retry()
  state.navigate = async () => new Error('Navigation aborted')
  await api.enter(state.rows[1])
  assert.equal(api.navigationError.value.workspace.uuid, 'ws-b')
  assert.equal(api.error.value, '')
  let resolve
  state.navigate = () => new Promise(done => { resolve = done })
  const retry = api.retryNavigation()
  api.retryNavigation()
  assert.equal(navigations.length, 2)
  assert.equal(navigations[1].params.workspaceID, 'ws-b')
  assert.equal(state.createCalls, 0)
  assert.ok(api.navigationError.value)
  resolve()
  await retry
  assert.equal(api.navigationError.value, null)
})

test('a rejected workspace load waits for Retry, then reloads and rechecks the same workspace once', async () => {
  const rows = ['a', 'b'].map(id => ({ uuid: `ws-${id}`, clusterName: `cluster-${id}` }))
  const fixture = setup()
  fixture.state.rows = rows
  fixture.state.navigate = async () => { throw new TypeError('Failed to fetch dynamically imported module') }
  await fixture.api.retry()
  await fixture.api.enter(rows[1])
  assert.equal(fixture.api.navigationError.value.workspace.uuid, 'ws-b')
  assert.equal(fixture.navigations.length, 1)
  assert.deepEqual(fixture.reloads, [])

  fixture.api.retryNavigation()
  fixture.api.retryNavigation()
  assert.deepEqual(fixture.reloads, [fixture.route.fullPath])

  for (const available of [true, false]) {
    const afterReload = setup(fixture.history.state)
    afterReload.state.org.initialWorkspacePending = false
    afterReload.state.rows = available ? rows : [rows[0]]
    afterReload.changeOrg()
    await new Promise(resolve => setImmediate(resolve))
    assert.equal(afterReload.history.state.retryWorkspace, null)
    assert.deepEqual(afterReload.navigations.map(to => to.params.workspaceID), available ? ['ws-b'] : [])
  }
})

test('a previous organization cannot publish late creation or navigation failures', async () => {
  for (const action of ['create', 'navigation']) {
    const { api, state, route, changeOrg } = setup()
    state.org.initialWorkspacePending = false
    await api.retry()
    let reject
    const pending = new Promise((_resolve, no) => { reject = no })
    state.create = () => pending
    state.navigate = () => pending
    api.name.value = 'Old draft'
    const request = action === 'create' ? api.create() : api.enter({ uuid: 'old-workspace', clusterName: 'old-cluster' })
    route.params.orgID = 'org-b'
    state.org = { ...state.org, uuid: 'org-b' }
    changeOrg()
    reject(new Error('Old failure'))
    await request
    assert.equal(api.createError.value, '')
    assert.equal(api.navigationError.value, null)
    assert.equal(api.name.value, '')
  }
})

test('refresh clears navigation failures for removed or unusable workspaces', async () => {
  for (const unavailable of [null, { uuid: 'ws-b', clusterName: 'cluster-b', deletionRequestedAt: '2026-11-16' }]) {
    const { api, state, navigations } = setup()
    state.org.initialWorkspacePending = false
    state.rows = [{ uuid: 'ws-a', clusterName: 'cluster-a' }, { uuid: 'ws-b', clusterName: 'cluster-b' }]
    await api.retry()
    state.navigate = async () => { throw new Error('Navigation unavailable') }
    await api.enter(state.rows[1])
    assert.equal(api.navigationError.value.workspace.uuid, 'ws-b')
    state.rows = [state.rows[0], { uuid: 'ws-c', clusterName: 'cluster-c' }]
    if (unavailable) state.rows.push(unavailable)
    await api.retry()
    assert.equal(api.navigationError.value, null)
    await api.retryNavigation()
    assert.equal(navigations.length, 1)
  }
})
