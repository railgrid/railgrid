import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

const source = fs.readFileSync(new URL('./TenantSettingsPage.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('TenantSettingsPage.ts', script, ts.ScriptTarget.Latest, true)

function loadFunction(name, context) {
  const declaration = parsed.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === name)
  assert.ok(declaration, `${name} exists in the production component`)
  const code = ts.transpileModule(declaration.getText(parsed), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText
  return runInNewContext(`${code}\n${name}`, context)
}

function deferred() {
  let resolve
  const promise = new Promise((done) => { resolve = done })
  return { promise, resolve }
}

function workspaceFixture() {
  const confirmation = deferred()
  const deletion = deferred()
  const deletionStarted = deferred()
  const calls = { delete: [], push: [], toast: [] }
  const target = { org: 'org-a', ws: 'workspace-a' }
  const context = {
    selectedTarget: () => target,
    isCurrentTarget: (candidate) => candidate.org === context.tenant.orgUUID && candidate.ws === context.tenant.workspaceUUID,
    canEditWs: { value: true },
    selWs: { value: { displayName: 'Production' } },
    wsBusy: { value: false },
    route: { fullPath: '/org-a/workspace-a/settings/workspaces' },
    confirmDialog: () => confirmation.promise,
    tenant: {
      orgUUID: 'org-a',
      workspaceUUID: 'workspace-a',
      deleteWorkspace: (...args) => { calls.delete.push(args); deletionStarted.resolve(); return deletion.promise },
    },
    router: { push: async (path) => { calls.push.push(path) } },
    toast: (...args) => calls.toast.push(args),
  }
  return { context, calls, confirmation, deletion, deletionStarted, remove: loadFunction('onDeleteWorkspace', context) }
}

test('deleting the current workspace opens a reload-safe organization inventory URL', async () => {
  const fixture = workspaceFixture()
  const pending = fixture.remove()
  fixture.confirmation.resolve(true)
  await fixture.deletionStarted.promise
  assert.deepEqual(fixture.calls.delete, [['org-a', 'workspace-a']])
  assert.equal(fixture.context.wsBusy.value, true)
  fixture.deletion.resolve(true)
  await pending
  assert.deepEqual(fixture.calls.push, ['/org-a/settings/organizations'])
  assert.equal(fixture.context.wsBusy.value, false)
  assert.match(fixture.calls.toast[0][1], /Restore it in Organization settings/)
})

for (const [name, change] of [
  ['route changed', (context) => { context.route.fullPath = '/org-a/workspace-a/' }],
  ['workspace changed', (context) => { context.tenant.workspaceUUID = 'workspace-b' }],
  ['admin access lost', (context) => { context.canEditWs.value = false }],
]) {
  test(`workspace confirmation does not delete after ${name}`, async () => {
    const fixture = workspaceFixture()
    const pending = fixture.remove()
    change(fixture.context)
    fixture.confirmation.resolve(true)
    await pending
    assert.deepEqual(fixture.calls.delete, [])
    assert.deepEqual(fixture.calls.push, [])
  })
}

for (const [name, change] of [
  ['navigation', (context) => { context.route.fullPath = '/org-a/workspace-b/settings/workspaces' }],
  ['organization change', (context) => { context.tenant.orgUUID = 'org-b' }],
]) {
  test(`late workspace delete success does not redirect newer ${name}`, async () => {
    const fixture = workspaceFixture()
    const pending = fixture.remove()
    fixture.confirmation.resolve(true)
    await fixture.deletionStarted.promise
    change(fixture.context)
    fixture.deletion.resolve(true)
    await pending
    assert.equal(fixture.calls.delete.length, 1)
    assert.deepEqual(fixture.calls.push, [])
    assert.deepEqual(fixture.calls.toast, [])
    assert.equal(fixture.context.wsBusy.value, false)
  })
}

test('failed workspace deletion leaves settings available to retry', async () => {
  const fixture = workspaceFixture()
  fixture.confirmation.resolve(true)
  fixture.deletion.resolve(false)
  await fixture.remove()
  assert.deepEqual(fixture.calls.push, [])
  assert.deepEqual(fixture.calls.toast, [])
  assert.equal(fixture.context.wsBusy.value, false)
})

function organizationFixture({ personal = false, bulkBusy = false } = {}) {
  const confirmation = deferred()
  const calls = []
  const confirmations = []
  const toasts = []
  const context = {
    organizationSettingsOrg: { value: { uuid: 'org-a', displayName: 'Team A', personal } },
    organizationTargetUUID: { value: 'org-a' },
    canEditOrg: { value: true },
    route: { fullPath: '/org-a/workspace-a/settings/organizations' },
    confirmDialog: (options) => { confirmations.push(options); return confirmation.promise },
    tenant: { deleteOrg: async (target) => { calls.push(target); return true } },
    managedOrgTargetUUID: { value: null },
    managedOrgSnapshot: { value: null },
    expectedOrgLifecycleRefresh: { value: null },
    orgBusy: { value: false },
    orgMemberBulkBusy: { value: bulkBusy },
    clearManagedOrgSnapshot: () => {},
    toast: (...args) => toasts.push(args),
  }
  return { context, calls, confirmations, toasts, confirmation, remove: loadFunction('onDeleteOrg', context) }
}

for (const [name, change] of [
  ['organization target changed', (context) => { context.organizationTargetUUID.value = 'org-b' }],
  ['route changed', (context) => { context.route.fullPath = '/org-a/workspace-a/settings/workspaces' }],
  ['admin access lost', (context) => { context.canEditOrg.value = false }],
]) {
  test(`organization confirmation does not install stale recovery state after ${name}`, async () => {
    const fixture = organizationFixture()
    const pending = fixture.remove()
    change(fixture.context)
    fixture.confirmation.resolve(true)
    await pending
    assert.deepEqual(fixture.calls, [])
    assert.equal(fixture.context.managedOrgSnapshot.value, null)
    assert.equal(fixture.context.managedOrgTargetUUID.value, null)
  })
}

test('confirmed current organization deletion preserves its recovery snapshot', async () => {
  const fixture = organizationFixture()
  fixture.confirmation.resolve(true)
  await fixture.remove()
  assert.deepEqual(fixture.calls, ['org-a'])
  assert.equal(fixture.context.managedOrgSnapshot.value.uuid, 'org-a')
  assert.ok(fixture.context.managedOrgSnapshot.value.deletionRequestedAt)
  assert.equal(fixture.context.expectedOrgLifecycleRefresh.value, null)
  assert.equal(fixture.context.orgBusy.value, false)
})

test('organization deletion cannot overlap a bulk member removal', async () => {
  const fixture = organizationFixture({ bulkBusy: true })
  await fixture.remove()
  assert.deepEqual(fixture.calls, [])
  assert.deepEqual(fixture.confirmations, [])
  assert.equal(fixture.context.managedOrgSnapshot.value, null)
  assert.equal(fixture.context.orgBusy.value, false)
})

test('personal organizations stay protected from deletion', async () => {
  const fixture = organizationFixture({ personal: true })
  await fixture.remove()
  assert.deepEqual(fixture.calls, [])
  assert.deepEqual(fixture.confirmations, [])
  assert.deepEqual(fixture.toasts, [['error', 'Personal organizations cannot be deleted.']])
  assert.equal(fixture.context.managedOrgSnapshot.value, null)
})

function workspaceBulkDeleteFixture() {
  const confirmations = []
  const deletePlans = []
  const calls = { deletes: [], reloads: [] }
  const organization = { uuid: 'org-a', displayName: 'Team A', deletionRequestedAt: null }
  const rows = [
    { uuid: 'workspace-a', orgUUID: 'org-a', displayName: 'Production', role: 'admin', deletionRequestedAt: null },
    { uuid: 'workspace-b', orgUUID: 'org-a', displayName: 'Staging', role: 'admin', deletionRequestedAt: null },
  ]
  const context = {
    selectedWorkspaceKeys: { value: ['workspace-a', 'workspace-b'] },
    workspaceDeleteBatchBusy: { value: false },
    workspaceDeleteProgress: { value: null },
    workspaceDeleteSummary: { value: null },
    workspaceDeleteScopeGeneration: 1,
    workspaceSelectionRevision: 0,
    workspaceDeleteRunSequence: 0,
    activeWorkspaceDeleteRun: 0,
    workspaceInventoryVerified: { value: true },
    workspaceListLoading: { value: false },
    restoringWorkspaceUUID: { value: null },
    activeSection: { value: 'organizations' },
    organizationTargetUUID: { value: 'org-a' },
    organizationSettingsOrg: { value: organization },
    activeOrg: { value: organization },
    workspaces: { value: rows },
    pageDisposed: false,
    route: { fullPath: '/org-a/settings/organizations' },
    tenant: {
      orgUUID: 'org-a',
      workspaceMode: 'organization',
      workspaceUUID: null,
      deleteWorkspaces: async (orgUUID, ids, options) => {
        calls.deletes.push({ orgUUID, ids: [...ids] })
        const outcomes = deletePlans.shift() ?? []
        outcomes.forEach((outcome, index) => options.onProgress(outcome, index + 1, ids.length))
        return outcomes
      },
    },
    confirmDialog: (options) => {
      const decision = deferred()
      confirmations.push({ ...decision, options })
      return decision.promise
    },
    reloadScopedWorkspaces: async (orgUUID) => { calls.reloads.push(orgUUID) },
    retryableWorkspaceDeleteIDs: { value: [] },
  }

  for (const name of [
    'workspaceBulkDeleteDisabledReason',
    'workspaceDeleteContextIsCurrent',
    'workspaceDeleteDispatchIsAllowed',
    'workspaceDeleteTargetIsEligible',
    'sameWorkspaceIDs',
    'recordWorkspaceDeleteOutcomes',
    'requestWorkspaceDeletions',
    'onRetryFailedWorkspaceDeletions',
  ]) context[name] = loadFunction(name, context)

  return { context, calls, confirmations, deletePlans }
}

test('bulk delete confirmation includes names and IDs and cancels queued work after leaving and returning', async () => {
  const fixture = workspaceBulkDeleteFixture()
  const pending = fixture.context.requestWorkspaceDeletions(['workspace-a', 'workspace-b'], false)
  assert.equal(fixture.confirmations.length, 1)
  assert.equal(fixture.confirmations[0].options.title, 'Delete 2 workspaces?')
  assert.match(fixture.confirmations[0].options.message, /recoverable 30-day grace period/)
  assert.match(fixture.confirmations[0].options.message, /Production \(UUID workspace-a\)/)
  assert.match(fixture.confirmations[0].options.message, /Staging \(UUID workspace-b\)/)

  fixture.context.route.fullPath = '/org-a/settings/workspaces'
  fixture.context.workspaceDeleteScopeGeneration++
  fixture.context.route.fullPath = '/org-a/settings/organizations'
  fixture.context.workspaceDeleteScopeGeneration++
  fixture.confirmations[0].resolve(true)
  await pending

  assert.equal(fixture.calls.deletes.length, 0)
  assert.equal(fixture.context.workspaceDeleteBatchBusy.value, false)
})

test('bulk delete keeps partial failures selected and a confirmed retry clears them', async () => {
  const fixture = workspaceBulkDeleteFixture()
  fixture.deletePlans.push(
    [
      { uuid: 'workspace-a', ok: true },
      { uuid: 'workspace-b', ok: false, error: 'failed to delete workspace: 503' },
    ],
    [{ uuid: 'workspace-b', ok: true }],
  )

  const first = fixture.context.requestWorkspaceDeletions(['workspace-a', 'workspace-b'], false)
  fixture.confirmations[0].resolve(true)
  await first
  assert.deepEqual(fixture.context.selectedWorkspaceKeys.value, ['workspace-b'])
  assert.equal(fixture.context.workspaceDeleteSummary.value.items.find((item) => item.uuid === 'workspace-b').status, 'failed')
  assert.equal(fixture.context.workspaceDeleteSummary.value.items.find((item) => item.uuid === 'workspace-b').error, 'failed to delete workspace: 503')

  fixture.context.retryableWorkspaceDeleteIDs.value = ['workspace-b']
  const retry = fixture.context.onRetryFailedWorkspaceDeletions()
  assert.equal(fixture.confirmations.length, 2)
  fixture.confirmations[1].resolve(true)
  await retry

  assert.equal(fixture.calls.deletes.length, 2)
  assert.deepEqual(fixture.calls.deletes[1], { orgUUID: 'org-a', ids: ['workspace-b'] })
  assert.deepEqual(fixture.context.selectedWorkspaceKeys.value, [])
  const retried = fixture.context.workspaceDeleteSummary.value.items.find((item) => item.uuid === 'workspace-b')
  assert.equal(retried.status, 'requested')
  assert.equal(retried.attempts, 2)
  assert.equal(fixture.context.workspaceDeleteBatchBusy.value, false)
  assert.deepEqual(fixture.calls.reloads, ['org-a', 'org-a'])
})

test('bulk delete confirmation aborts when query-driven selection changes before submit', async () => {
  const fixture = workspaceBulkDeleteFixture()
  const pending = fixture.context.requestWorkspaceDeletions(['workspace-a', 'workspace-b'], false)
  fixture.context.selectedWorkspaceKeys.value = []
  fixture.context.workspaceSelectionRevision++
  fixture.confirmations[0].resolve(true)
  await pending

  assert.equal(fixture.calls.deletes.length, 0)
  assert.deepEqual(fixture.context.selectedWorkspaceKeys.value, [])
})

test('bulk delete settles through an inventory failure without reviving cleared selection or awaiting refresh', async () => {
  const fixture = workspaceBulkDeleteFixture()
  const mutation = deferred()
  const refresh = deferred()
  fixture.context.tenant.deleteWorkspaces = () => mutation.promise
  fixture.context.reloadScopedWorkspaces = () => refresh.promise
  const pending = fixture.context.requestWorkspaceDeletions(['workspace-a', 'workspace-b'], false)
  fixture.confirmations[0].resolve(true)
  await new Promise(resolve => setImmediate(resolve))
  assert.equal(fixture.context.workspaceDeleteBatchBusy.value, true)

  // A failed concurrent list read is not a scope change. A query change can
  // still clear the selection while already-confirmed requests settle.
  fixture.context.workspaceInventoryVerified.value = false
  fixture.context.selectedWorkspaceKeys.value = []
  fixture.context.workspaceSelectionRevision++
  mutation.resolve([
    { uuid: 'workspace-a', ok: true },
    { uuid: 'workspace-b', ok: false, error: 'Permission denied' },
  ])
  await pending
  assert.equal(fixture.context.workspaceDeleteBatchBusy.value, false)
  assert.equal(fixture.context.workspaceDeleteProgress.value, null)
  assert.deepEqual(fixture.context.selectedWorkspaceKeys.value, [])
  assert.equal(fixture.context.workspaceDeleteSummary.value.items.length, 2)
  assert.equal(fixture.context.workspaceDeleteSummary.value.items[1].error, 'Permission denied')
  refresh.resolve()
})
