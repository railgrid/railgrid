import assert from 'node:assert/strict'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, effectScope, reactive, ref, watch } from 'vue'
import { createServer } from 'vite'

const source = readFileSync(new URL('./TenantSettingsPage.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('TenantSettingsPage.ts', script, ts.ScriptTarget.Latest, true)

const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'railgrid-vite-settings-bulk-page-test'),
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

async function flushMicrotasks() {
  for (let i = 0; i < 8; i++) await Promise.resolve()
}

function functionText(name) {
  const declaration = parsed.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === name)
  assert.ok(declaration, `${name} exists in the production page`)
  return declaration.getText(parsed)
}

function variableText(name) {
  const statement = parsed.statements.find((node) => ts.isVariableStatement(node) &&
    node.declarationList.declarations.some((declaration) => ts.isIdentifier(declaration.name) && declaration.name.text === name))
  assert.ok(statement, `${name} exists in the production page`)
  return statement.getText(parsed)
}

function scopeWatcherText() {
  const statement = parsed.statements.find((node) => ts.isExpressionStatement(node) &&
    ts.isCallExpression(node.expression) && node.expression.expression.getText(parsed) === 'watch' &&
    node.getText(parsed).includes('settingsBulkScopeGeneration.value++'))
  assert.ok(statement, 'the production scope watcher exists in the page')
  return statement.getText(parsed)
}

const pageFunctionNames = [
  'captureSettingsBulkContext',
  'isCurrentSettingsBulkContext',
  'workspaceScopeDescription',
  'serializeBulkItem',
  'memberBulkName',
  'wsMemberRowSelectable',
  'wsMemberRowSelectionDisabledReason',
  'onDeleteSelectedSAs',
  'onRemoveSelectedWsMembers',
  'onRevokeSelectedAppAccess',
]
const pageBulkVariableNames = ['saBulk', 'wsMemberBulk', 'appAccessBulk']
const extractedPageCode = ts.transpileModule([
  ...pageFunctionNames.map(functionText),
  ...pageBulkVariableNames.map(variableText),
  'const anySettingsSingleMutationBusy = computed(() => saCreateBusy.value || Object.keys(saBusy.value).length > 0 || Object.keys(wsMemberBusy.value).length > 0 || Object.keys(appAccessBusy.value).length > 0)',
  'const anySettingsBulkBusy = computed(() => saBulk.busy.value || wsMemberBulk.busy.value || appAccessBulk.busy.value)',
  'const anySettingsAccessMutationBusy = computed(() => anySettingsBulkBusy.value || anySettingsSingleMutationBusy.value)',
  'const selectedSAKeys = saBulk.selectedKeys',
  'const selectedWsMemberKeys = wsMemberBulk.selectedKeys',
  'const selectedAppAccessKeys = appAccessBulk.selectedKeys',
].join('\n'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
}).outputText

function fixture() {
  const scope = { org: 'org-a', workspace: 'ws-a' }
  const readStatus = reactive({ appAccessDenied: false })
  const state = {
    confirmations: [],
    mutations: [],
    refreshes: [],
    failures: new Map(),
    mutation: null,
    confirm: null,
    error: null,
  }
  const tenant = reactive({
    orgUUID: scope.org,
    workspaceUUID: scope.workspace,
    workspaceMode: 'workspace',
    error: null,
    clearError() { tenant.error = null; state.error = null },
    listReadDenied(kind) { return kind === 'app-access' && readStatus.appAccessDenied },
    deleteServiceAccount(org, workspace, key) { return mutate('deleteServiceAccount', org, workspace, key) },
    removeWorkspaceMember(org, workspace, key) { return mutate('removeWorkspaceMember', org, workspace, key) },
    revokeAppAccessGrant(org, workspace, key) { return mutate('revokeAppAccessGrant', org, workspace, key) },
  })

  async function mutate(method, org, workspace, key) {
    state.mutations.push({ method, org, workspace, key })
    if (state.mutation) return state.mutation(method, org, workspace, key)
    const failure = state.failures.get(`${method}:${key}`)
    if (failure) {
      tenant.error = failure
      state.error = failure
      return false
    }
    return true
  }

  const context = {
    ref,
    computed,
    watch,
    useSettingsBulkAction,
    tenant,
    auth: reactive({ token: 'test-token', self: { user: 'workspace-admin' } }),
    route: reactive({ fullPath: '/org-a/ws-a/settings/workspaces' }),
    activeSection: ref('workspaces'),
    activeOrg: ref({ uuid: scope.org, displayName: 'Team A' }),
    selectedWorkspaceUUID: ref(scope.workspace),
    selWs: ref({ uuid: scope.workspace, displayName: 'Production', role: 'admin' }),
    canEditWs: ref(true),
    settingsBulkScopeGeneration: ref(1),
    pageDisposed: false,
    selectedTarget: () => ({ org: scope.org, ws: scope.workspace }),
    isCurrentTarget: (target) => target.org === scope.org && target.ws === scope.workspace,
    sas: ref([
      { uuid: 'sa-a', displayName: 'Build bot', role: 'member' },
      { uuid: 'sa-b', displayName: 'Release bot', role: 'member' },
    ]),
    sasHasSnapshot: ref(true),
    sasLoading: ref(false),
    sasError: ref(null),
    sasReadDenied: ref(false),
    saCreateBusy: ref(false),
    saBusy: ref({}),
    wsMembers: ref([
      { user: 'alice', role: 'member', email: 'alice@example.com', userDisplayName: 'Alice' },
      { user: 'bob', role: 'member', email: 'bob@example.com', userDisplayName: 'Bob' },
      { user: 'workspace-admin', role: 'admin', email: 'admin@example.com', userDisplayName: 'Admin' },
    ]),
    wsMembersHasSnapshot: ref(true),
    wsMembersLoading: ref(false),
    wsMembersError: ref(null),
    wsMembersReadDenied: ref(false),
    wsMemberBusy: ref({}),
    appAccessGrants: ref([
      { binding: 'grant-a', app: 'Nightly', user: 'alice' },
      { binding: 'grant-b', app: 'Preview', user: 'bob' },
    ]),
    appAccessHasSnapshot: ref(true),
    appAccessLoading: ref(false),
    appAccessError: ref(null),
    appAccessBusy: ref({}),
    confirmDialog: (options) => {
      state.confirmations.push(options)
      return state.confirm ? state.confirm(options) : Promise.resolve(true)
    },
    reloadSAs: async () => state.refreshes.push({ kind: 'service accounts', org: scope.org, ws: scope.workspace }),
    reloadWsMembers: async () => state.refreshes.push({ kind: 'workspace members', org: scope.org, ws: scope.workspace }),
    reloadAppAccessGrants: async () => state.refreshes.push({ kind: 'app access', org: scope.org, ws: scope.workspace }),
    endSAOperation(uuid) {
      const next = { ...context.saBusy.value }
      delete next[uuid]
      context.saBusy.value = next
    },
  }
  const api = runInNewContext(`${extractedPageCode}\n({ saBulk, wsMemberBulk, appAccessBulk, onDeleteSelectedSAs, onRemoveSelectedWsMembers, onRevokeSelectedAppAccess, wsMemberRowSelectable, wsMemberRowSelectionDisabledReason })`, context)

  function moveToWorkspace(org, workspace) {
    scope.org = org
    scope.workspace = workspace
    tenant.orgUUID = org
    tenant.workspaceUUID = workspace
    context.activeOrg.value = { uuid: org, displayName: org === 'org-a' ? 'Team A' : 'Team B' }
    context.selectedWorkspaceUUID.value = workspace
    context.selWs.value = { uuid: workspace, displayName: workspace === 'ws-a' ? 'Production' : 'Staging', role: 'admin' }
    context.route.fullPath = `/${org}/${workspace}/settings/workspaces`
    context.settingsBulkScopeGeneration.value++
    api.saBulk.resetSelection()
    api.wsMemberBulk.resetSelection()
    api.appAccessBulk.resetSelection()
  }

  return { api, context, scope, state, readStatus, moveToWorkspace }
}

function mountProductionScopeWatcher(h) {
  const watcherScope = effectScope()
  watcherScope.run(() => runInNewContext(scopeWatcherText(), {
    watch,
    route: h.context.route,
    tenant: h.context.tenant,
    activeOrg: h.context.activeOrg,
    selectedWorkspaceUUID: h.context.selectedWorkspaceUUID,
    activeSection: h.context.activeSection,
    canEditWs: h.context.canEditWs,
    wsMembersReadDenied: h.context.wsMembersReadDenied,
    sasReadDenied: h.context.sasReadDenied,
    auth: h.context.auth,
    settingsBulkScopeGeneration: h.context.settingsBulkScopeGeneration,
    saBulk: h.api.saBulk,
    wsMemberBulk: h.api.wsMemberBulk,
    appAccessBulk: h.api.appAccessBulk,
  }))
  return watcherScope
}

const operationFamilies = [
  {
    name: 'service account deletion',
    action: 'saBulk',
    handler: 'onDeleteSelectedSAs',
    keys: ['sa-a', 'sa-b'],
    failedKey: 'sa-b',
    method: 'deleteServiceAccount',
    rows: 'sas',
    refreshKind: 'service accounts',
    title: /Delete 2 selected service accounts/,
    scopeText: /Workspace "Production" \(UUID ws-a\).*organization "Team A" \(UUID org-a\)/s,
    consequence: /active tokens from working/i,
  },
  {
    name: 'workspace member removal',
    action: 'wsMemberBulk',
    handler: 'onRemoveSelectedWsMembers',
    keys: ['alice', 'bob'],
    failedKey: 'bob',
    method: 'removeWorkspaceMember',
    rows: 'wsMembers',
    refreshKind: 'workspace members',
    title: /Remove 2 selected members/,
    scopeText: /Workspace "Production" \(UUID ws-a\).*organization "Team A" \(UUID org-a\)/s,
    consequence: /lose workspace access/i,
  },
  {
    name: 'app access grant revocation',
    action: 'appAccessBulk',
    handler: 'onRevokeSelectedAppAccess',
    keys: ['grant-a', 'grant-b'],
    failedKey: 'grant-b',
    method: 'revokeAppAccessGrant',
    rows: 'appAccessGrants',
    refreshKind: 'app access',
    title: /Revoke 2 selected app access grants/,
    scopeText: /Workspace "Production" \(UUID ws-a\).*organization "Team A" \(UUID org-a\)/s,
    consequence: /lose access to these private apps/i,
  },
]

for (const operation of operationFamilies) {
  test(`${operation.name}: scoped confirmation, partial failure retention, and retry`, async () => {
    const h = fixture()
    h.state.failures.set(`${operation.method}:${operation.failedKey}`, 'Permission denied: HTTP 403')
    const action = h.api[operation.action]
    action.selectedKeys.value = operation.keys

    await h.api[operation.handler](operation.keys)

    assert.deepEqual(h.state.mutations.map(({ method, org, workspace, key }) => ({ method, org, workspace, key })), [
      { method: operation.method, org: 'org-a', workspace: 'ws-a', key: operation.keys[0] },
      { method: operation.method, org: 'org-a', workspace: 'ws-a', key: operation.failedKey },
    ])
    assert.deepEqual(action.selectedKeys.value, [operation.failedKey])
    assert.deepEqual(action.outcomes.value.map(({ key, succeeded }) => ({ key, succeeded })), [
      { key: operation.keys[0], succeeded: true },
      { key: operation.failedKey, succeeded: false },
    ])
    assert.match(h.state.confirmations[0].title, operation.title)
    assert.match(h.state.confirmations[0].message, operation.scopeText)
    assert.match(h.state.confirmations[0].message, operation.consequence)
    assert.equal(h.state.refreshes.length, 1)
    assert.equal(h.state.refreshes[0].kind, operation.refreshKind)
    assert.equal(h.state.refreshes[0].org, 'org-a')
    assert.equal(h.state.refreshes[0].ws, 'ws-a')
    assert.equal(h.context[operation.rows].value.some((row) => {
      const key = row.uuid ?? row.user ?? row.binding
      return key === operation.keys[0]
    }), false, 'a successful mutation is removed from the local snapshot')

    h.state.failures.delete(`${operation.method}:${operation.failedKey}`)
    await h.api[operation.handler]([operation.failedKey])
    assert.deepEqual(action.selectedKeys.value, [])
    assert.equal(h.state.mutations.length, 3)
    assert.equal(h.state.refreshes.length, 2)
    assert.deepEqual(h.state.confirmations[1].message.match(operation.scopeText)?.[0] !== undefined, true)
  })
}

test('workspace bulk removal excludes self and stays disabled until the stable User CR identity loads', async () => {
  const h = fixture()
  const action = h.api.wsMemberBulk
  assert.equal(h.api.wsMemberRowSelectable({ user: 'workspace-admin' }), false)
  assert.match(h.api.wsMemberRowSelectionDisabledReason({ user: 'workspace-admin' }), /individual remove action/)

  h.context.auth.self = null
  assert.equal(h.api.wsMemberRowSelectable({ user: 'alice' }), false)
  assert.match(h.api.wsMemberRowSelectionDisabledReason({ user: 'alice' }), /identity is still loading/)
  action.selectedKeys.value = ['alice']
  await h.api.onRemoveSelectedWsMembers(['alice'])
  assert.deepEqual(h.state.mutations, [])
  assert.deepEqual(h.state.confirmations, [])
})

test('scope change A-to-B-to-A during confirmation blocks dispatch and keeps the old result out of the new page', async () => {
  const h = fixture()
  const confirmation = deferred()
  h.state.confirm = () => confirmation.promise
  h.api.saBulk.selectedKeys.value = ['sa-a']
  const pending = h.api.onDeleteSelectedSAs(['sa-a'])
  await flushMicrotasks()

  h.moveToWorkspace('org-a', 'ws-b')
  h.moveToWorkspace('org-a', 'ws-a')
  confirmation.resolve(true)
  await pending

  assert.deepEqual(h.state.mutations, [])
  assert.deepEqual(h.state.refreshes, [])
  assert.deepEqual(h.api.saBulk.selectedKeys.value, [])
  assert.deepEqual(h.api.saBulk.outcomes.value, [])
})

test('scope change A-to-B-to-A while the first request is in flight retires queued work and stale local updates', async () => {
  const h = fixture()
  const firstMutation = deferred()
  h.state.mutation = async (_method, _org, _workspace, key) => {
    if (key === 'sa-a') return firstMutation.promise
    return true
  }
  h.api.saBulk.selectedKeys.value = ['sa-a', 'sa-b']
  const pending = h.api.onDeleteSelectedSAs(['sa-a', 'sa-b'])
  for (let attempt = 0; attempt < 8 && h.state.mutations.length === 0; attempt++) await Promise.resolve()
  assert.deepEqual(h.state.mutations.map(({ key }) => key), ['sa-a'])

  h.moveToWorkspace('org-a', 'ws-b')
  h.moveToWorkspace('org-a', 'ws-a')
  firstMutation.resolve(true)
  await pending

  assert.deepEqual(h.state.mutations.map(({ key }) => key), ['sa-a'])
  assert.deepEqual(h.state.refreshes, [])
  assert.deepEqual(h.api.saBulk.selectedKeys.value, [])
  assert.deepEqual(h.api.saBulk.outcomes.value, [])
  assert.equal(h.context.sas.value.some((row) => row.uuid === 'sa-a'), true, 'the old request cannot publish into a different workspace snapshot')
})

test('bulk permission loss during confirmation prevents a write', async () => {
  const h = fixture()
  const confirmation = deferred()
  h.state.confirm = () => confirmation.promise
  h.api.appAccessBulk.selectedKeys.value = ['grant-a']
  const pending = h.api.onRevokeSelectedAppAccess(['grant-a'])
  await flushMicrotasks()

  h.context.canEditWs.value = false
  h.context.settingsBulkScopeGeneration.value++
  h.api.appAccessBulk.resetSelection()
  confirmation.resolve(true)
  await pending

  assert.deepEqual(h.state.mutations, [])
  assert.deepEqual(h.state.refreshes, [])
  assert.deepEqual(h.api.appAccessBulk.selectedKeys.value, [])
})

test('the production scope watcher ignores initial app-grant snapshot completion but retires an in-flight batch on grant-read denial', async () => {
  const h = fixture()
  h.context.appAccessHasSnapshot.value = false
  const watcherScope = mountProductionScopeWatcher(h)
  try {
    const startingGeneration = h.context.settingsBulkScopeGeneration.value
    const firstMutation = deferred()
    h.state.mutation = () => firstMutation.promise
    h.api.saBulk.selectedKeys.value = ['sa-a']

    const firstRun = h.api.onDeleteSelectedSAs(['sa-a'])
    for (let attempt = 0; attempt < 8 && h.state.mutations.length === 0; attempt++) await Promise.resolve()
    assert.equal(h.state.mutations.length, 1, 'the service-account request has started')

    h.context.appAccessHasSnapshot.value = true
    assert.equal(h.context.settingsBulkScopeGeneration.value, startingGeneration,
      'a successful first grant-list snapshot is not a scope transition')
    assert.deepEqual(h.api.saBulk.selectedKeys.value, ['sa-a'],
      'the unrelated service-account selection remains intact while its request is pending')

    firstMutation.resolve(true)
    await firstRun
    assert.equal(h.state.refreshes.length, 1, 'the still-current service-account batch refreshes normally')
    assert.equal(h.context.sas.value.some((row) => row.uuid === 'sa-a'), false)

    const secondMutation = deferred()
    h.state.mutation = () => secondMutation.promise
    h.api.saBulk.selectedKeys.value = ['sa-b']
    const secondRun = h.api.onDeleteSelectedSAs(['sa-b'])
    for (let attempt = 0; attempt < 8 && h.state.mutations.length < 2; attempt++) await Promise.resolve()
    assert.equal(h.state.mutations.length, 2, 'a second service-account request has started')

    const generationBeforeDenial = h.context.settingsBulkScopeGeneration.value
    h.readStatus.appAccessDenied = true
    assert.equal(h.context.settingsBulkScopeGeneration.value, generationBeforeDenial + 1,
      'the production watcher treats a newly denied grant read as an access-scope change')
    assert.deepEqual(h.api.saBulk.selectedKeys.value, [],
      'grant-read permission loss clears selections across the settings tables')

    const refreshCountBeforeStaleCompletion = h.state.refreshes.length
    secondMutation.resolve(true)
    await secondRun
    assert.equal(h.state.refreshes.length, refreshCountBeforeStaleCompletion,
      'a batch retired by the watcher does not refresh using its old permission scope')
    assert.equal(h.context.sas.value.some((row) => row.uuid === 'sa-b'), true,
      'the retired request does not publish a stale local row update')
  } finally {
    watcherScope.stop()
  }
})

test('conflicting row and cross-table operations cannot start during a bulk mutation', async () => {
  const h = fixture()
  h.context.saBusy.value = { 'sa-a': 'issue' }
  h.api.saBulk.selectedKeys.value = ['sa-a']
  await h.api.onDeleteSelectedSAs(['sa-a'])
  assert.deepEqual(h.state.confirmations, [], 'a single-account operation conflicts with bulk delete')
  assert.deepEqual(h.state.mutations, [])

  h.context.saBusy.value = {}
  const confirmation = deferred()
  const mutation = deferred()
  h.state.confirm = () => confirmation.promise
  h.state.mutation = () => mutation.promise
  h.api.saBulk.selectedKeys.value = ['sa-a']
  const first = h.api.onDeleteSelectedSAs(['sa-a'])
  await flushMicrotasks()
  assert.equal(h.api.saBulk.busy.value, false, 'the open confirmation must leave the public busy state idle')
  assert.equal(h.state.confirmations.length, 1)
  confirmation.resolve(true)
  await flushMicrotasks()
  assert.equal(h.api.saBulk.busy.value, true)
  assert.equal(h.state.mutations.length, 1)

  h.api.appAccessBulk.selectedKeys.value = ['grant-a']
  await h.api.onRevokeSelectedAppAccess(['grant-a'])
  assert.equal(h.state.confirmations.length, 1, 'a different table cannot open a competing confirmation')
  assert.equal(h.state.mutations.length, 1)
  mutation.resolve(true)
  await first
  assert.deepEqual(h.state.mutations.map(({ method }) => method), ['deleteServiceAccount'])
})
