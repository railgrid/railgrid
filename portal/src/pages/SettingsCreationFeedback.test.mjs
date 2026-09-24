import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, effectScope, nextTick, reactive, ref, watch } from 'vue'

const source = readFileSync(new URL('./TenantSettingsPage.vue', import.meta.url), 'utf8').match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const ast = ts.createSourceFile('settings.ts', source, ts.ScriptTarget.Latest, true)
function fn(name) {
  const node = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
  assert.ok(node, `missing ${name}`)
  return node.getText(ast)
}
const controls = source.slice(source.indexOf('const memberDialog ='), source.indexOf('\nfunction fmtDate('))
const functions = ['isCurrentCreationFeedback', 'currentOrgMemberContext', 'currentOrganizationTarget',
  'isCurrentWsMembersContext', 'isCurrentServiceAccountContext', 'invalidateWsMembersRequests',
  'invalidateServiceAccountRequests', 'reloadOrgMembers', 'reloadWsMembers', 'reloadSAs',
  'onAddOrgMember', 'onAddWsMember', 'onCreateSA']

function deferred() {
  let resolve
  const promise = new Promise(done => { resolve = done })
  return { promise, resolve }
}

function setup(kind) {
  const scope = effectScope()
  const refresh = deferred()
  const state = { error: null, denied: false, listCalls: 0, mutations: 0, actions: [], revealed: [] }
  const tenant = reactive({ orgUUID: 'org-a', clearError() {},
    listReadError: () => state.error, listReadDenied: () => state.denied,
    listOrgMembers: list, listWorkspaceMembers: list, listServiceAccounts: list,
    addOrgMember: mutate, addWorkspaceMember: mutate,
    createServiceAccount: async (_org, _ws, name, role) => {
      state.mutations++
      return { uuid: `sa-${state.mutations}`, displayName: name, role }
    },
  })
  function list() { state.listCalls++; return refresh.promise }
  async function mutate() { state.mutations++; return true }
  const values = { ref, computed, watch, tenant, nextTick,
    route: reactive({ fullPath: `/settings/${kind === 'org' ? 'organizations' : 'workspaces'}` }),
    activeSection: ref(kind === 'org' ? 'organizations' : 'workspaces'),
    organizationTargetUUID: ref('org-a'), selectedWorkspaceUUID: ref('ws-a'),
    selWs: ref({ uuid: 'ws-a' }), canEditWs: ref(true), canManageOrgMembers: ref(true),
    saCreateBusy: ref(false), saTableQuery: ref(''), saTableRevision: ref(0),
    creationFeedbackGeneration: 0, pageDisposed: false,
    orgMembersRequest: 0, orgMemberContextGeneration: 0,
    wsMembersRequestGeneration: 0, wsMembersContextGeneration: 0,
    serviceAccountRequestGeneration: 0, serviceAccountContextGeneration: 0,
    toast: (_kind, _text, options) => state.actions.push(options.action),
    document: { getElementById: () => ({ scrollIntoView() {} }) },
  }
  for (const prefix of ['orgMembers', 'wsMembers', 'sas']) {
    values[prefix] = ref([])
    values[`${prefix}Loading`] = ref(false)
    values[`${prefix}HasSnapshot`] = ref(false)
    values[`${prefix}ReadDenied`] = ref(false)
    values[`${prefix}Error`] = ref(null)
  }
  values.orgMemberBusy = ref({}); values.wsMemberBusy = ref({})
  values.selectedTarget = () => ({ org: tenant.orgUUID, ws: values.selectedWorkspaceUUID.value })
  values.isCurrentTarget = target => target.org === tenant.orgUUID && target.ws === values.selectedWorkspaceUUID.value
  const code = ts.transpileModule([...functions.map(fn), controls].join('\n'), { compilerOptions: { target: ts.ScriptTarget.ES2020 } }).outputText
  const api = scope.run(() => runInNewContext(`${code}\n({ memberDialog, createSADialogOpen, wsMemberList, orgMemberList, canAddWsMembers, canAddOrgMembers, canCreateSA, openMemberDialog, openServiceAccountDialog, reloadOrgMembers, reloadWsMembers, reloadSAs, onAddOrgMember, onAddWsMember, onCreateSA })`, values))
  api.wsMemberList.value = api.orgMemberList.value = { reveal(user) { state.revealed.push(user) } }
  const definitions = {
    org: { prefix: 'orgMembers', open: () => api.openMemberDialog('organization'), visible: () => api.memberDialog.value === 'organization', reload: api.reloadOrgMembers, submit: api.onAddOrgMember },
    workspace: { prefix: 'wsMembers', open: () => api.openMemberDialog('workspace'), visible: () => api.memberDialog.value === 'workspace', reload: api.reloadWsMembers, submit: api.onAddWsMember },
    sa: { prefix: 'sas', open: api.openServiceAccountDialog, visible: () => api.createSADialogOpen.value, reload: api.reloadSAs, submit: api.onCreateSA },
  }
  return { ...definitions[kind], values, state, api, refresh, stop: () => scope.stop() }
}

for (const kind of ['org', 'workspace', 'sa']) {
  test(`${kind}: transient initial read errors keep the active draft mounted; denial remains sticky until a successful read`, async () => {
    const h = setup(kind)
    try {
      const pending = h.reload()
      h.open()
      h.state.error = 'HTTP 500'
      h.refresh.resolve([])
      await pending; await nextTick()
      assert.equal(h.visible(), true)
      h.state.error = 'HTTP 403'; h.state.denied = true
      await h.reload(); await nextTick()
      assert.equal(h.visible(), false)
      assert.equal(h.values[`${h.prefix}ReadDenied`].value, true)
      h.state.denied = false; h.state.error = 'HTTP 503'
      await h.reload(); await nextTick()
      assert.equal(h.values[`${h.prefix}ReadDenied`].value, true, 'a transport failure cannot undo an access denial')
      h.state.error = null
      await h.reload(); await nextTick()
      assert.equal(h.values[`${h.prefix}ReadDenied`].value, false)
    } finally { h.stop() }
  })

  test(`${kind}: mutation completion is independent of a pending refresh and earlier reveal actions survive later mutations`, async () => {
    const h = setup(kind)
    try {
      let result = 'pending'
      void h.submit('alice', 'member').then(value => { result = value })
      for (let i = 0; i < 10; i++) await Promise.resolve()
      assert.equal(result, true, 'successful creation must resolve before the follow-up GET')
      assert.equal(h.state.listCalls, 1)
      assert.equal(h.values[`${h.prefix}Loading`].value, true)
      const firstAction = h.state.actions[0]
      await h.submit('bob', 'member')
      firstAction.run()
      if (kind === 'sa') assert.equal(h.values.saTableQuery.value, 'sa-1')
      else assert.deepEqual(h.state.revealed, ['alice'])
      h.state.revealed.length = 0; h.values.saTableQuery.value = ''
      h.values.route.fullPath = '/dashboard'
      h.values.route.fullPath = `/settings/${kind === 'org' ? 'organizations' : 'workspaces'}`
      firstAction.run()
      assert.equal(h.values.saTableQuery.value, '')
      assert.deepEqual(h.state.revealed, [], 'leaving and returning must not revive old feedback')
      h.refresh.resolve([])
    } finally { h.stop() }
  })

  test(`${kind}: permission loss/restoration and disposal retire earlier reveal actions`, async () => {
    const h = setup(kind)
    try {
      await h.submit('alice', 'member')
      const action = h.state.actions[0]
      const permission = kind === 'org' ? h.values.canManageOrgMembers : h.values.canEditWs
      permission.value = false; permission.value = true
      action.run()
      assert.deepEqual(h.state.revealed, [])
      assert.equal(h.values.saTableQuery.value, '')
      await h.submit('bob', 'member')
      h.values.pageDisposed = true
      h.state.actions[1].run()
      assert.deepEqual(h.state.revealed, [])
      assert.equal(h.values.saTableQuery.value, '')
      h.refresh.resolve([])
    } finally { h.stop() }
  })
}
