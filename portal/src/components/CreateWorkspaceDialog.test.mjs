import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, reactive, ref } from 'vue'

const source = readFileSync(new URL('./CreateWorkspaceDialog.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const code = ts.transpileModule(script.replace(/^import .*$/gm, ''), {
  compilerOptions: { target: ts.ScriptTarget.ES2020 },
}).outputText

function setup() {
  let dispose, orgChange
  let now = 0
  const timers = new Map()
  const navigations = []
  const route = ref({ fullPath: '/org-a/old/' })
  const auth = reactive({ token: 'test-session' })
  const state = { creates: 0, reads: [], rows: [], status: 'ready', creation: null, closed: 0, fetch: null }
  const created = { uuid: 'new', orgUUID: 'org-a', displayName: 'New workspace' }
  const tenant = reactive({
    orgUUID: 'org-a', workspaceUUID: 'old', orgLoadState: 'ready', orgError: null,
    activeOrg: { uuid: 'org-a', displayName: 'Team', role: 'admin' },
    workspacesByOrg: {}, workspaceLoadStateByOrg: {},
    async fetchWorkspaces(org, options) {
      assert.equal(options.selectDefault, false)
      if (state.fetch) await state.fetch()
      this.workspacesByOrg[org] = state.rows
      this.workspaceLoadStateByOrg[org] = state.status
    },
    beginWorkspaceTransition: () => 1, endWorkspaceTransition() {},
  })
  const api = runInNewContext(`${code}\n({ submit, checkReady, close, name, busy, created, error, timedOut, canCreate })`, {
    authFetch: async (url, options) => {
      if (options.method === 'POST') {
        assert.equal(url, '/api/orgs/org-a/workspaces')
        state.creates++
        state.createArgs = { name: JSON.parse(options.body).displayName }
        const result = state.creation ? await state.creation() : created
        return new Response(JSON.stringify(result), { status: state.createStatus || 201 })
      }
      assert.equal(url, '/api/orgs/org-a/workspaces/new')
      // The hub's requireTenantContext binds workspace GETs to BOTH headers.
      // A scoped URL alone does not select the workspace, and the existing
      // operating workspace is deliberately still "old" during preparation.
      const headers = new Headers(options.headers)
      state.reads.push({ org: headers.get('X-Railgrid-Org'), workspace: headers.get('X-Railgrid-Workspace') })
      if (headers.get('X-Railgrid-Org') !== 'org-a' || headers.get('X-Railgrid-Workspace') !== 'new') {
        return new Response(JSON.stringify({ message: 'Workspace path and tenant headers must match.' }), { status: 400 })
      }
      if (state.fetch) await state.fetch()
      const row = state.rows.find(row => row.uuid === 'new') || created
      return new Response(JSON.stringify(state.readError || row), { status: state.readStatus || (state.status === 'ready' ? 200 : 503) })
    },
    computed, ref, Error, useAuthStore: () => auth, useId: () => 'dialog', useTenantStore: () => tenant,
    useRouter: () => ({ currentRoute: route, async push(to) { navigations.push(to); tenant.workspaceUUID = to.params.workspaceID } }),
    defineEmits: () => () => { state.closed++ },
    isWorkspaceUsable: row => !!row?.clusterName && !row.deletionRequestedAt,
    watch: (_source, callback) => { orgChange = callback },
    onMounted() {}, onBeforeUnmount: callback => { dispose = callback },
    setTimeout: callback => { const id = Symbol(); timers.set(id, callback); return id },
    clearTimeout: id => timers.delete(id), Date: { now: () => now },
  })
  api.name.value = '  Development  '
  return { api, state, tenant, created, navigations, timers, route, auth, dispose: () => dispose(), changeOrg: () => orgChange(), advance: ms => { now += ms } }
}

test('creation preserves current context until the exact new workspace is usable', async () => {
  const { api, state, tenant, created, navigations } = setup()
  state.rows = [{ uuid: 'old', clusterName: 'old-cluster' }, created]
  await api.submit()
  assert.equal(tenant.workspaceUUID, 'old')
  assert.equal(navigations.length, 0)
  assert.equal(state.createArgs.name, 'Development')
  assert.equal(tenant.workspaceLoadStateByOrg['org-a'], undefined)
  state.rows = [...state.rows, { ...created, uuid: 'other', clusterName: 'other-cluster' }]
  await api.checkReady()
  assert.equal(navigations.length, 0)
  state.rows = [{ ...created, clusterName: 'new-cluster' }]
  await api.checkReady()
  assert.equal(navigations[0].params.workspaceID, 'new')
  assert.equal(state.closed, 1)
})

test('readiness addresses the new workspace while preserving the current operating context', async () => {
  const { api, state, tenant, created, navigations } = setup()
  state.rows = [{ ...created, clusterName: 'new-cluster' }]
  state.fetch = () => { assert.equal(tenant.workspaceUUID, 'old') }
  await api.submit()
  assert.equal(api.error.value, '')
  assert.deepEqual(state.reads, [{ org: 'org-a', workspace: 'new' }])
  assert.equal(navigations.length, 1)
  assert.equal(navigations[0].params.workspaceID, 'new')
  assert.equal(tenant.workspaceUUID, 'new')
  assert.equal(state.closed, 1)
})

test('duplicate submits do not create twice and readiness retries never repeat creation', async () => {
  const { api, state, advance } = setup()
  let resolve
  state.creation = () => new Promise(done => { resolve = done })
  const first = api.submit()
  await api.submit()
  assert.equal(state.creates, 1)
  resolve({ uuid: 'new', orgUUID: 'org-a' })
  await first
  advance(60_000)
  await api.checkReady()
  assert.equal(api.timedOut.value, true)
  assert.equal(api.busy.value, false)
  await api.submit()
  assert.equal(state.creates, 1)
})

test('readiness failures retain the created workspace and provide a safe retry', async () => {
  const { api, state, created, navigations } = setup()
  state.status = 'error'
  await api.submit()
  assert.match(api.error.value, /created.*readiness/)
  assert.equal(api.busy.value, false)
  state.status = 'ready'
  state.rows = [{ ...created, clusterName: 'ready' }]
  await api.submit()
  assert.equal(state.creates, 1)
  assert.equal(navigations.length, 1)
})

test('readiness contract errors show the server reason without retrying or creating again', async () => {
  const { api, state, tenant, timers } = setup()
  state.readStatus = 400
  state.readError = { message: 'Workspace path and tenant headers must match.' }
  await api.submit()
  assert.match(api.error.value, /HTTP 400/)
  assert.match(api.error.value, /Workspace path and tenant headers must match/)
  assert.equal(api.created.value.uuid, 'new')
  assert.equal(tenant.workspaceUUID, 'old')
  assert.equal(tenant.error, undefined)
  assert.equal(state.creates, 1)
  assert.equal(state.reads.length, 1)
  assert.equal(timers.size, 0)
  assert.equal(api.busy.value, false)
})

test('closing or changing organization fences late creation and readiness responses', async () => {
  for (const stage of ['creation', 'readiness']) {
    for (const action of ['close', 'dispose', 'changeOrg', 'changeRoute', 'changeSession']) {
      const env = setup()
      let resolve
      if (stage === 'creation') env.state.creation = () => new Promise(done => { resolve = done })
      else env.state.fetch = () => new Promise(done => { resolve = done })
      env.state.rows = [{ ...env.created, clusterName: 'ready' }]
      const pending = env.api.submit()
      for (let turn = 0; !resolve && turn < 100; turn++) await Promise.resolve()
      assert.equal(typeof resolve, 'function', `${stage}/${action}: request did not reach the deferred response`)
      if (action === 'close') env.api.close()
      else if (action === 'dispose') env.dispose()
      else if (action === 'changeOrg') { env.tenant.orgUUID = 'org-b'; env.changeOrg() }
      else if (action === 'changeRoute') { env.route.value.fullPath = '/org-a/another/'; env.changeOrg() }
      else { env.auth.token = 'another-session'; env.changeOrg() }
      resolve(env.created)
      await pending
      assert.equal(env.navigations.length, 0, `${stage}/${action}`)
      assert.equal(env.timers.size, 0, `${stage}/${action}`)
    }
  }
})

test('permissions and empty names guard submission', async () => {
  const { api, tenant, state } = setup()
  tenant.activeOrg.role = 'member'
  await api.submit()
  assert.equal(state.creates, 0)
  tenant.activeOrg.workspaceCreation = 'members'
  api.name.value = '  '
  await api.submit()
  assert.equal(state.creates, 0)
  api.name.value = 'Development'
  tenant.activeOrg.deletionRequestedAt = 'today'
  await api.submit()
  assert.equal(state.creates, 0)
})

test('native modal provides protected focus, label, cancel and an announced error', () => {
  assert.match(source, /<dialog/)
  assert.match(source, /dialogRef\.value\?\.showModal\(\)/)
  assert.match(source, /@cancel\.prevent="close"/)
  assert.match(source, /:aria-labelledby=/)
  assert.match(source, /autofocus/)
  assert.match(source, /role="alert"/)
})

test('POST failures stay local and preserve the draft for retry', async () => {
  const { api, state, tenant } = setup()
  state.createStatus = 403
  state.creation = () => ({ message: 'Only organization admins can create workspaces.' })
  await api.submit()
  assert.match(api.error.value, /Only organization admins/)
  assert.equal(api.name.value, '  Development  ')
  assert.equal(api.busy.value, false)
  assert.equal(tenant.error, undefined)
  api.close()
  assert.equal(tenant.error, undefined)
})

test('closing during creation keeps the returned workspace in the picker without switching', async () => {
  const { api, state, tenant, created, navigations } = setup()
  let resolve
  state.creation = () => new Promise(done => { resolve = done })
  const pending = api.submit()
  api.close()
  resolve(created)
  await pending
  assert.equal(tenant.workspacesByOrg['org-a'][0].uuid, 'new')
  assert.equal(tenant.workspaceUUID, 'old')
  assert.equal(navigations.length, 0)
})
