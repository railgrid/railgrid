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

function organizationFixture() {
  const confirmation = deferred()
  const calls = []
  const context = {
    organizationSettingsOrg: { value: { uuid: 'org-a', displayName: 'Team A', personal: false } },
    organizationTargetUUID: { value: 'org-a' },
    canEditOrg: { value: true },
    route: { fullPath: '/org-a/workspace-a/settings/organizations' },
    confirmDialog: () => confirmation.promise,
    tenant: { deleteOrg: async (target) => { calls.push(target); return true } },
    managedOrgTargetUUID: { value: null },
    managedOrgSnapshot: { value: null },
    expectedOrgLifecycleRefresh: { value: null },
    orgBusy: { value: false },
    clearManagedOrgSnapshot: () => {},
    toast: () => {},
  }
  return { context, calls, confirmation, remove: loadFunction('onDeleteOrg', context) }
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
