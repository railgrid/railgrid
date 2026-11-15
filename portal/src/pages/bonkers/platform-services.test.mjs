import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

const source = fs.readFileSync(new URL('./platform-services.ts', import.meta.url), 'utf8')
const code = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText
const context = { exports: {} }
runInNewContext(code, context)
const { systemWorkspaceStatus, seedStatus, systemWorkspaceNameError, seedNameError, starterSeedSpec, seedExportPaths, systemWorkspaceAt } = context.exports

test('a system service reads as provisioning until the hub reports, and a refusal is not shown as progress', () => {
  assert.equal(systemWorkspaceStatus({ ready: false, reason: '' }).label, 'Provisioning')
  assert.equal(systemWorkspaceStatus({ ready: true, reason: 'Provisioned' }).label, 'Ready')
  assert.equal(systemWorkspaceStatus({ ready: false, reason: 'WorkspaceError' }).label, 'Not ready')
  for (const reason of ['NameReserved', 'WorkspaceNotOwned']) {
    const status = systemWorkspaceStatus({ ready: false, reason })
    assert.equal(status.label, 'Refused')
    assert.equal(status.tone, 'danger')
  }
})

test('a seed the hub has not looked at yet is pending, not accepted', () => {
  assert.equal(seedStatus({ accepted: '' }).label, 'Pending')
  assert.equal(seedStatus({}).label, 'Pending')
  assert.equal(seedStatus({ accepted: 'True' }).label, 'Accepted')
  assert.equal(seedStatus({ accepted: 'False' }).tone, 'danger')
})

test('system service names are one DNS label and never the hub’s own workspaces', () => {
  assert.equal(systemWorkspaceNameError(''), '')
  assert.equal(systemWorkspaceNameError('switchboard'), '')
  assert.equal(systemWorkspaceNameError('model-gateway-2'), '')
  for (const bad of ['Switchboard', 'a:b', 'a.b', '-a', 'a-', 'a b', 'x'.repeat(64)]) assert.notEqual(systemWorkspaceNameError(bad), '', bad)
  for (const reserved of ['controllers', 'providers', 'tenants']) assert.match(systemWorkspaceNameError(reserved), /hub’s own/)
  assert.equal(seedNameError('switchboard'), '')
  assert.notEqual(seedNameError('Not A Name'), '')
})

test('the starter spec points at the service it was opened from', () => {
  const spec = starterSeedSpec('root:railgrid:system:switchboard', 'switchboard.railgrid.ai')
  assert.match(spec, /path: root:railgrid:system:switchboard\n/)
  assert.match(spec, /name: switchboard\.railgrid\.ai /)
  assert.match(spec, /- name: switchboard /)
  assert.match(spec, /tenantAccess: ReadOnly/)
  // Without a service it is still a complete, editable example.
  assert.match(starterSeedSpec(), /path: root:railgrid:system:my-service\n/)
})

test('a seed’s export workspaces are listed once each and matched to system services', () => {
  const seed = { bindings: [{ exportPath: 'root:railgrid:system:a' }, { exportPath: 'root:railgrid:system:b' }, { exportPath: 'root:railgrid:system:a' }] }
  assert.deepEqual([...seedExportPaths(seed)], ['root:railgrid:system:a', 'root:railgrid:system:b'])
  assert.deepEqual([...seedExportPaths({})], [])
  const workspaces = [{ name: 'a', workspacePath: 'root:railgrid:system:a' }]
  assert.equal(systemWorkspaceAt('root:railgrid:system:a', workspaces).name, 'a')
  assert.equal(systemWorkspaceAt('root:railgrid:providers:a', workspaces), undefined)
})
