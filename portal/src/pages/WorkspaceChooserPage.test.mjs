import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, reactive, ref } from 'vue'

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

function setup() {
  let onDispose
  let onOrgChange
  let now = 0
  const timers = new Map()
  const navigations = []
  const org = { uuid: 'org-a', displayName: 'Team', role: 'admin', initialWorkspacePending: true }
  const route = { params: { orgID: 'org-a' }, query: {} }
  const state = {
    org, rows: [], orgStatus: 200, workspaceStatus: 'ready',
    orgRead: null, createResult: null, createCalls: 0,
  }
  const tenant = reactive({
    activeOrg: org, workspacesByOrg: {}, workspaceLoadStateByOrg: {},
    async fetchWorkspaces(id) {
      this.workspacesByOrg[id] = state.rows
      this.workspaceLoadStateByOrg[id] = state.workspaceStatus
    },
    async createWorkspace() { state.createCalls++; return state.createResult },
  })
  const api = runInNewContext(`${code}\n({ load, retry, create, loading, error, waiting, timedOut, creating, name, org })`, {
    ref, computed, useRoute: () => route,
    useRouter: () => ({ replace: async to => { navigations.push(to) } }),
    useTenantStore: () => tenant, useAuthStore: () => ({ user: { userId: 'alice' } }),
    authFetch: async () => state.orgRead ? state.orgRead() : new Response(JSON.stringify(state.org), { status: state.orgStatus }),
    isWorkspaceAvailable: available, isWorkspaceUsable: usable, preferredWorkspace,
    readOrganizationWorkspace: () => null,
    watch: (_source, callback) => { onOrgChange = callback },
    onBeforeUnmount: callback => { onDispose = callback },
    setTimeout: callback => { const key = Symbol(); timers.set(key, callback); return key },
    clearTimeout: key => timers.delete(key),
    Date: { now: () => now }, Error,
  })
  return { api, state, tenant, timers, navigations, route, dispose: () => onDispose(), changeOrg: () => onOrgChange(), advance: ms => { now += ms } }
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
