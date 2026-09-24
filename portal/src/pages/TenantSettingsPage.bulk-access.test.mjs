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
const { pruneClientSelectionKeys } = await vite.ssrLoadModule('/src/portalkit/table.ts')
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

function orgMemberScopeWatcherText() {
  const statement = parsed.statements.find((node) => ts.isExpressionStatement(node) &&
    ts.isCallExpression(node.expression) && node.expression.expression.getText(parsed) === 'watch' &&
    node.getText(parsed).includes('orgMemberBulkScopeGeneration.value++'))
  assert.ok(statement, 'the production organization member bulk scope watcher exists in the page')
  return statement.getText(parsed)
}

function failedOrgMemberScopeWatcherText() {
  const statement = parsed.statements.find((node) => ts.isExpressionStatement(node) &&
    ts.isCallExpression(node.expression) && node.expression.expression.getText(parsed) === 'watch' &&
    node.getText(parsed).includes('failedOrgMemberRemovals.value = []') &&
    node.getText(parsed).includes('() => route.fullPath'))
  assert.ok(statement, 'the production failed-member retry scope watcher exists in the page')
  return statement.getText(parsed)
}

const pageFunctionNames = [
  'captureSettingsBulkContext',
  'isCurrentSettingsBulkContext',
  'captureOrgMemberBulkContext',
  'isCurrentOrgMemberBulkContext',
  'currentOrgMemberContext',
  'workspaceScopeDescription',
  'serializeBulkItem',
  'orgMemberBulkSnapshot',
  'clearFailedOrgMemberRemoval',
  'rememberFailedOrgMemberRemoval',
  'orgMemberRetryIneligibleReason',
  'memberBulkName',
  'wsMemberRowSelectable',
  'wsMemberRowSelectionDisabledReason',
  'orgMemberRowSelectable',
  'orgMemberRowSelectionDisabledReason',
  'orgMemberSelectionLabel',
  'onAddOrgMember',
  'onChangeOrgMemberRole',
  'onRemoveOrgMember',
  'onDeleteSelectedSAs',
  'onRemoveSelectedWsMembers',
  'onRemoveSelectedOrgMembers',
  'onRetryFailedOrgMemberRemovals',
  'onRevokeSelectedAppAccess',
]
const pageBulkVariableNames = ['saBulk', 'wsMemberBulk', 'orgMemberSingleMutationBusy', 'orgMemberBulk', 'orgMemberBulkLocked', 'failedOrgMemberRemovals', 'appAccessBulk']
const extractedPageCode = ts.transpileModule([
  ...pageFunctionNames.map(functionText),
  ...pageBulkVariableNames.map(variableText),
  'const anySettingsSingleMutationBusy = computed(() => saCreateBusy.value || Object.keys(saBusy.value).length > 0 || Object.keys(wsMemberBusy.value).length > 0 || Object.keys(appAccessBusy.value).length > 0)',
  'const anySettingsBulkBusy = computed(() => saBulk.busy.value || wsMemberBulk.busy.value || appAccessBulk.busy.value)',
  'const anySettingsAccessMutationBusy = computed(() => anySettingsBulkBusy.value || anySettingsSingleMutationBusy.value)',
  'const selectedSAKeys = saBulk.selectedKeys',
  'const selectedWsMemberKeys = wsMemberBulk.selectedKeys',
  'const selectedOrgMemberKeys = orgMemberBulk.selectedKeys',
  'const selectedAppAccessKeys = appAccessBulk.selectedKeys',
].join('\n'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
}).outputText

function fixture() {
  const scope = { org: 'org-a', workspace: 'ws-a' }
  const readStatus = reactive({ appAccessDenied: false })
  let api
  const state = {
    confirmations: [],
    mutations: [],
    orgRemovals: [],
    orgSingleMutations: [],
    refreshes: [],
    failures: new Map(),
    orgMembersOnReload: null,
    removeOrgMembershipBeforeFailure: new Set(),
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
    removeOrgMember(org, user, cascade) {
      state.orgRemovals.push([org, user, cascade])
      return mutate('removeOrgMember', org, null, user)
    },
    addOrgMember(org, user, role) {
      state.orgSingleMutations.push(['addOrgMember', org, user, role])
      return mutate('addOrgMember', org, null, user)
    },
    patchOrgMemberRole(org, user, role) {
      state.orgSingleMutations.push(['patchOrgMemberRole', org, user, role])
      return mutate('patchOrgMemberRole', org, null, user)
    },
    revokeAppAccessGrant(org, workspace, key) { return mutate('revokeAppAccessGrant', org, workspace, key) },
  })

  async function mutate(method, org, workspace, key) {
    state.mutations.push({ method, org, workspace, key })
    if (state.mutation) return state.mutation(method, org, workspace, key)
    const failure = state.failures.get(`${method}:${key}`)
    if (failure) {
      // A cascading DELETE can remove the membership CR before a later
      // organization-index or child-workspace cleanup step fails.
      if (method === 'removeOrgMember' && state.removeOrgMembershipBeforeFailure.has(key) && state.orgMembersOnReload) {
        state.orgMembersOnReload = state.orgMembersOnReload.filter((row) => row.user !== key)
      }
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
    activeOrg: ref({ uuid: scope.org, displayName: 'Team A', role: 'admin', personal: false }),
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
    orgMembers: ref([
      { user: 'alice', role: 'member', email: 'alice@example.com', userDisplayName: 'Alice' },
      { user: 'bob', role: 'member', email: 'bob@example.com', userDisplayName: 'Bob' },
      { user: 'workspace-admin', role: 'admin', email: 'admin@example.com', userDisplayName: 'Admin' },
    ]),
    orgMembersHasSnapshot: ref(true),
    orgMembersLoading: ref(false),
    orgMembersError: ref(null),
    orgMembersReadDenied: ref(false),
    orgMemberBusy: ref({}),
    orgMemberContextGeneration: 1,
    orgMemberBulkScopeGeneration: ref(1),
    orgBusy: ref(false),
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
    reloadOrgMembers: async (org = scope.org) => {
      state.refreshes.push({ kind: 'organization members', org })
      if (!state.orgMembersOnReload) return
      context.orgMembers.value = state.orgMembersOnReload.map((row) => ({ ...row }))
    },
    reloadAppAccessGrants: async () => state.refreshes.push({ kind: 'app access', org: scope.org, ws: scope.workspace }),
    endSAOperation(uuid) {
      const next = { ...context.saBusy.value }
      delete next[uuid]
      context.saBusy.value = next
    },
  }
  context.organizationSettingsOrg = computed(() => context.activeOrg.value)
  context.organizationTargetUUID = computed(() => context.organizationSettingsOrg.value?.uuid ?? null)
  context.canManageOrgMembers = computed(() => context.organizationSettingsOrg.value?.role === 'admin' && !context.organizationSettingsOrg.value?.deletionRequestedAt)
  context.canAddOrgMembers = computed(() => context.canManageOrgMembers.value && !context.orgMembersReadDenied.value)
  api = runInNewContext(`${extractedPageCode}\n({ saBulk, wsMemberBulk, orgMemberBulk, failedOrgMemberRemovals, appAccessBulk, onDeleteSelectedSAs, onRemoveSelectedWsMembers, onRemoveSelectedOrgMembers, onRetryFailedOrgMemberRemovals, onRevokeSelectedAppAccess, wsMemberRowSelectable, wsMemberRowSelectionDisabledReason, orgMemberRowSelectable, orgMemberRowSelectionDisabledReason, orgMemberSelectionLabel, onAddOrgMember, onChangeOrgMemberRole, onRemoveOrgMember })`, context)

  function moveToWorkspace(org, workspace) {
    scope.org = org
    scope.workspace = workspace
    tenant.orgUUID = org
    tenant.workspaceUUID = workspace
    context.activeOrg.value = { uuid: org, displayName: org === 'org-a' ? 'Team A' : 'Team B', role: 'admin', personal: false }
    context.selectedWorkspaceUUID.value = workspace
    context.selWs.value = { uuid: workspace, displayName: workspace === 'ws-a' ? 'Production' : 'Staging', role: 'admin' }
    context.route.fullPath = `/${org}/${workspace}/settings/workspaces`
    context.settingsBulkScopeGeneration.value++
    api.saBulk.resetSelection()
    api.wsMemberBulk.resetSelection()
    api.appAccessBulk.resetSelection()
  }

  function enterOrganization() {
    context.activeSection.value = 'organizations'
    context.route.fullPath = `/${scope.org}/${scope.workspace}/settings/organizations`
  }

  function moveToOrganization(org) {
    scope.org = org
    tenant.orgUUID = org
    context.activeOrg.value = { uuid: org, displayName: org === 'org-a' ? 'Team A' : 'Team B', role: 'admin', personal: false }
    context.route.fullPath = `/${org}/settings/organizations`
  }

  return { api, context, scope, state, readStatus, moveToWorkspace, enterOrganization, moveToOrganization }
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

function mountProductionOrgMemberScopeWatcher(h) {
  const watcherScope = effectScope()
  watcherScope.run(() => runInNewContext(`${orgMemberScopeWatcherText()}\n${failedOrgMemberScopeWatcherText()}`, {
    watch,
    route: h.context.route,
    tenant: h.context.tenant,
    organizationTargetUUID: h.context.organizationTargetUUID,
    activeSection: h.context.activeSection,
    canManageOrgMembers: h.context.canManageOrgMembers,
    orgMembersReadDenied: h.context.orgMembersReadDenied,
    orgBusy: h.context.orgBusy,
    auth: h.context.auth,
    orgMemberBulkScopeGeneration: h.context.orgMemberBulkScopeGeneration,
    orgMemberBulk: h.api.orgMemberBulk,
    failedOrgMemberRemovals: h.api.failedOrgMemberRemovals,
  }))
  return watcherScope
}

function pruneOrgMemberSelectionToCurrentRows(h) {
  h.api.orgMemberBulk.selectedKeys.value = pruneClientSelectionKeys(
    h.api.orgMemberBulk.selectedKeys.value,
    h.context.orgMembers.value.map((row) => ({ key: row.user, selectable: true })),
  )
}

async function startFailedBobRemoval({ membershipRemoved = true } = {}) {
  const h = fixture()
  h.enterOrganization()
  h.state.failures.set('removeOrgMember:bob', 'HTTP 500: organization cleanup failed')
  h.state.orgMembersOnReload = h.context.orgMembers.value.filter((row) =>
    row.user !== 'alice' && (row.user !== 'bob' || !membershipRemoved))
  if (membershipRemoved) h.state.removeOrgMembershipBeforeFailure.add('bob')
  h.api.orgMemberBulk.selectedKeys.value = ['alice', 'bob']
  await h.api.onRemoveSelectedOrgMembers(['alice', 'bob'])
  if (membershipRemoved) pruneOrgMemberSelectionToCurrentRows(h)
  return h
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

test('organization bulk removal retries a failed cascade after its membership row and table selection disappear', async () => {
  const h = await startFailedBobRemoval()

  assert.deepEqual(h.state.orgRemovals, [
    ['org-a', 'alice', true],
    ['org-a', 'bob', true],
  ], 'every bulk removal uses the store’s child-workspace cascade')
  assert.match(h.state.confirmations[0].title, /Remove 2 selected members/)
  assert.match(h.state.confirmations[0].message, /organization "Team A" \(UUID org-a\)/)
  assert.match(h.state.confirmations[0].message, /lose organization-level access and membership in every child workspace in this organization/i)
  assert.match(h.state.confirmations[0].message, /Alice \(alice@example\.com\) · alice/)
  assert.match(h.state.confirmations[0].message, /Bob \(bob@example\.com\) · bob/)
  assert.deepEqual(h.api.orgMemberBulk.selectedKeys.value, [], 'the table drops selection for the membership CR that disappeared on the failed request')
  assert.equal(h.context.orgMembers.value.some((row) => row.user === 'bob'), false, 'the refreshed roster no longer contains the failed member')
  assert.deepEqual(h.api.orgMemberBulk.outcomes.value.map(({ key, succeeded }) => ({ key, succeeded })), [
    { key: 'alice', succeeded: true },
    { key: 'bob', succeeded: false },
  ])
  assert.equal(h.context.orgMembers.value.some((row) => row.user === 'alice'), false)
  assert.deepEqual(h.state.refreshes, [{ kind: 'organization members', org: 'org-a' }])
  assert.deepEqual(Array.from(h.api.failedOrgMemberRemovals.value, ({ user }) => user), ['bob'],
    'the failed attempt remains available independently of roster rows and selected keys')

  await h.api.onRetryFailedOrgMemberRemovals()

  assert.deepEqual(h.state.orgRemovals, [
    ['org-a', 'alice', true],
    ['org-a', 'bob', true],
    ['org-a', 'bob', true],
  ], 'the separate retry uses the original member identity and keeps the cascade enabled')
  assert.deepEqual(h.api.orgMemberBulk.selectedKeys.value, [], 'retry does not reconstruct table selection')
  assert.deepEqual(Array.from(h.api.failedOrgMemberRemovals.value, ({ user }) => user), ['bob'],
    'a repeat cleanup failure remains retryable')
  assert.equal(h.state.confirmations.length, 2, 'each retry attempt gets its own confirmation')
  assert.match(h.state.confirmations[1].title, /retry/i)
  assert.match(h.state.confirmations[1].message, /bob/i)

  h.state.failures.delete('removeOrgMember:bob')
  await h.api.onRetryFailedOrgMemberRemovals()

  assert.equal(h.state.orgRemovals.length, 4)
  assert.deepEqual(h.state.orgRemovals.slice(2), [
    ['org-a', 'bob', true],
    ['org-a', 'bob', true],
  ])
  assert.deepEqual(h.api.orgMemberBulk.selectedKeys.value, [])
  assert.deepEqual(Array.from(h.api.failedOrgMemberRemovals.value), [], 'successful cleanup removes the retry record')
  assert.equal(h.context.orgMembers.value.some((row) => row.user === 'bob'), false)
})

test('organization retry refuses a member who has become the current user', async () => {
  const h = await startFailedBobRemoval()
  const mutationsBeforeRetry = h.state.orgRemovals.length
  const confirmationsBeforeRetry = h.state.confirmations.length
  h.context.auth.self = { user: 'bob' }

  await h.api.onRetryFailedOrgMemberRemovals()

  assert.equal(h.state.orgRemovals.length, mutationsBeforeRetry)
  assert.equal(h.state.confirmations.length, confirmationsBeforeRetry)
  assert.deepEqual(Array.from(h.api.failedOrgMemberRemovals.value, ({ user }) => user), ['bob'],
    'the self guard does not discard the unresolved cleanup attempt')
})

test('organization retry revalidates a still-present member against the failed-attempt snapshot', async () => {
  const h = await startFailedBobRemoval({ membershipRemoved: false })
  h.api.orgMemberBulk.selectedKeys.value = []
  const confirmation = deferred()
  h.state.confirm = () => confirmation.promise
  const mutationsBeforeRetry = h.state.orgRemovals.length

  const pending = h.api.onRetryFailedOrgMemberRemovals()
  await flushMicrotasks()
  assert.equal(h.state.confirmations.length, 2, 'the retry prompts for the retained attempted member')
  h.context.orgMembers.value = h.context.orgMembers.value.map((row) => row.user === 'bob'
    ? { ...row, email: 'changed@example.com' }
    : row)
  confirmation.resolve(true)
  await pending

  assert.equal(h.state.orgRemovals.length, mutationsBeforeRetry, 'a changed live member is not removed using the stale confirmation')
  assert.deepEqual(h.api.orgMemberBulk.selectedKeys.value, [])
  assert.deepEqual(Array.from(h.api.failedOrgMemberRemovals.value, ({ user }) => user), ['bob'], 'the stale failed attempt remains visible for review')
})

for (const [changeName, changeScope] of [
  ['organization scope', (h) => {
    h.moveToOrganization('org-b')
    h.moveToOrganization('org-a')
  }],
  ['admin authority', (h) => {
    h.context.activeOrg.value = { ...h.context.activeOrg.value, role: 'member' }
    h.context.activeOrg.value = { ...h.context.activeOrg.value, role: 'admin' }
  }],
]) {
  test(`a pending organization retry is retired after ${changeName} changes`, async () => {
    const h = await startFailedBobRemoval()
    const confirmation = deferred()
    h.state.confirm = () => confirmation.promise
    const watcherScope = mountProductionOrgMemberScopeWatcher(h)
    try {
      const generationBeforeRetry = h.context.orgMemberBulkScopeGeneration.value
      const mutationsBeforeRetry = h.state.orgRemovals.length
      const pending = h.api.onRetryFailedOrgMemberRemovals()
      await flushMicrotasks()
      assert.equal(h.state.confirmations.length, 2, 'the retry has reached its confirmation prompt')

      changeScope(h)
      assert.ok(h.context.orgMemberBulkScopeGeneration.value > generationBeforeRetry, 'the production org scope watcher observes the transition')
      assert.deepEqual(Array.from(h.api.failedOrgMemberRemovals.value), [], 'the production watcher clears attempts as soon as the scope changes')
      confirmation.resolve(true)
      await pending

      assert.equal(h.state.orgRemovals.length, mutationsBeforeRetry, 'a stale retry cannot dispatch after its scope or authority changed')
      assert.deepEqual(Array.from(h.api.failedOrgMemberRemovals.value), [], 'retiring the context clears the old organization retry record')
    } finally {
      watcherScope.stop()
    }
  })
}

test('organization bulk selection excludes self and requires a verified readable roster', async () => {
  const h = fixture()
  h.enterOrganization()
  assert.equal(h.api.orgMemberRowSelectable({ user: 'workspace-admin' }), false)
  assert.match(h.api.orgMemberRowSelectionDisabledReason({ user: 'workspace-admin' }), /individual remove action/)
  assert.equal(h.api.orgMemberRowSelectable({ user: 'alice' }), true)
  h.context.activeOrg.value = { ...h.context.activeOrg.value, personal: true }
  assert.equal(h.api.orgMemberRowSelectable({ user: 'alice' }), true, 'personal organizations keep the same admin member-management authority')
  h.context.activeOrg.value = { ...h.context.activeOrg.value, personal: false }
  assert.match(h.api.orgMemberSelectionLabel({ user: 'alice' }), /Alice \(alice@example\.com\).*Team A/)

  h.context.auth.self = null
  assert.equal(h.api.orgMemberRowSelectable({ user: 'alice' }), false)
  assert.match(h.api.orgMemberRowSelectionDisabledReason({ user: 'alice' }), /identity is still loading/)
  h.api.orgMemberBulk.selectedKeys.value = ['alice']
  await h.api.onRemoveSelectedOrgMembers(['alice'])
  assert.deepEqual(h.state.confirmations, [])
  assert.deepEqual(h.state.orgRemovals, [])

  h.context.auth.self = { user: 'workspace-admin' }
  h.context.orgMembersHasSnapshot.value = false
  assert.equal(h.api.orgMemberRowSelectable({ user: 'alice' }), false)
  assert.match(h.api.orgMemberRowSelectionDisabledReason({ user: 'alice' }), /verify the current organization member list/i)
  await h.api.onRemoveSelectedOrgMembers(['alice'])
  assert.deepEqual(h.state.confirmations, [])

  h.context.orgMembersHasSnapshot.value = true
  h.context.orgMembersError.value = 'Organization members could not be refreshed.'
  assert.equal(h.api.orgMemberRowSelectable({ user: 'alice' }), false, 'stale rows cannot be selected for destructive work')
  await h.api.onRemoveSelectedOrgMembers(['alice'])
  assert.deepEqual(h.state.orgRemovals, [])

  h.context.orgMembersError.value = null
  h.api.orgMemberBulk.selectedKeys.value = ['alice']
  const watcherScope = mountProductionOrgMemberScopeWatcher(h)
  try {
    const generation = h.context.orgMemberBulkScopeGeneration.value
    h.context.orgMembersReadDenied.value = true
    assert.equal(h.context.orgMemberBulkScopeGeneration.value, generation + 1)
    assert.deepEqual(h.api.orgMemberBulk.selectedKeys.value, [], 'a denied roster clears existing selection immediately')
    await h.api.onRemoveSelectedOrgMembers([])
    assert.deepEqual(h.state.confirmations, [])
    assert.deepEqual(h.state.orgRemovals, [])
  } finally {
    watcherScope.stop()
  }
})

test('organization route A-to-B-to-A during confirmation retires the captured scope', async () => {
  const h = fixture()
  h.enterOrganization()
  const confirmation = deferred()
  h.state.confirm = () => confirmation.promise
  const watcherScope = mountProductionOrgMemberScopeWatcher(h)
  try {
    const initialRoute = h.context.route.fullPath
    h.api.orgMemberBulk.selectedKeys.value = ['alice']
    const pending = h.api.onRemoveSelectedOrgMembers(['alice'])
    await flushMicrotasks()

    h.context.route.fullPath = '/org-a/settings/organizations?view=members'
    h.context.route.fullPath = initialRoute
    confirmation.resolve(true)
    await pending

    assert.deepEqual(h.state.orgRemovals, [])
    assert.deepEqual(h.state.refreshes, [])
    assert.deepEqual(h.api.orgMemberBulk.selectedKeys.value, [])
    assert.deepEqual(h.api.orgMemberBulk.outcomes.value, [])
  } finally {
    watcherScope.stop()
  }
})

test('organization A-to-B-to-A while a removal is in flight retires queued work and local updates', async () => {
  const h = fixture()
  h.enterOrganization()
  const firstMutation = deferred()
  h.state.mutation = async (_method, _org, _workspace, key) => key === 'alice' ? firstMutation.promise : true
  const watcherScope = mountProductionOrgMemberScopeWatcher(h)
  try {
    h.api.orgMemberBulk.selectedKeys.value = ['alice', 'bob']
    const pending = h.api.onRemoveSelectedOrgMembers(['alice', 'bob'])
    for (let attempt = 0; attempt < 8 && h.state.orgRemovals.length === 0; attempt++) await Promise.resolve()
    assert.deepEqual(h.state.orgRemovals, [['org-a', 'alice', true]])

    h.moveToOrganization('org-b')
    h.moveToOrganization('org-a')
    firstMutation.resolve(true)
    await pending

    assert.deepEqual(h.state.orgRemovals, [['org-a', 'alice', true]], 'the retired run sends no queued request')
    assert.deepEqual(h.state.refreshes, [])
    assert.deepEqual(h.api.orgMemberBulk.selectedKeys.value, [])
    assert.deepEqual(h.api.orgMemberBulk.outcomes.value, [])
    assert.equal(h.context.orgMembers.value.some((row) => row.user === 'alice'), true,
      'a stale completion cannot remove a row from the current organization snapshot')
  } finally {
    watcherScope.stop()
  }
})

test('permission loss and restoration during confirmation still retires organization removal', async () => {
  const h = fixture()
  h.enterOrganization()
  const confirmation = deferred()
  h.state.confirm = () => confirmation.promise
  const watcherScope = mountProductionOrgMemberScopeWatcher(h)
  try {
    h.api.orgMemberBulk.selectedKeys.value = ['alice']
    const pending = h.api.onRemoveSelectedOrgMembers(['alice'])
    await flushMicrotasks()

    h.context.activeOrg.value = { ...h.context.activeOrg.value, role: 'member' }
    h.context.activeOrg.value = { ...h.context.activeOrg.value, role: 'admin' }
    confirmation.resolve(true)
    await pending

    assert.deepEqual(h.state.orgRemovals, [])
    assert.deepEqual(h.api.orgMemberBulk.selectedKeys.value, [])
    assert.deepEqual(h.api.orgMemberBulk.outcomes.value, [])
  } finally {
    watcherScope.stop()
  }
})

test('organization single-row and bulk member mutations cannot overlap', async () => {
  const h = fixture()
  h.enterOrganization()
  h.context.orgMemberBusy.value = { bob: true }
  h.api.orgMemberBulk.selectedKeys.value = ['alice']
  await h.api.onRemoveSelectedOrgMembers(['alice'])
  assert.deepEqual(h.state.confirmations, [], 'an active row mutation blocks the bulk confirmation')
  assert.deepEqual(h.state.orgRemovals, [])

  h.context.orgMemberBusy.value = {}
  const confirmation = deferred()
  const mutation = deferred()
  h.state.confirm = () => confirmation.promise
  h.state.mutation = () => mutation.promise
  h.api.orgMemberBulk.selectedKeys.value = ['alice']
  const pending = h.api.onRemoveSelectedOrgMembers(['alice'])
  await flushMicrotasks()
  confirmation.resolve(true)
  await flushMicrotasks()
  assert.equal(h.api.orgMemberBulk.busy.value, true)

  await h.api.onRemoveOrgMember('bob')
  await h.api.onChangeOrgMemberRole('bob', 'admin')
  assert.equal(await h.api.onAddOrgMember('carol', 'member'), false)
  assert.equal(h.state.confirmations.length, 1, 'a row removal cannot open a competing confirmation')
  assert.deepEqual(h.state.orgSingleMutations, [], 'add and role changes cannot dispatch during bulk removal')

  mutation.resolve(true)
  await pending
  assert.deepEqual(h.state.orgRemovals, [['org-a', 'alice', true]])
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
