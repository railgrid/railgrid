import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
const source = fs.readFileSync(new URL('./admin-relations.ts', import.meta.url), 'utf8')
const context = { exports: {} }
runInNewContext(ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText, context)
const { platformProvider, providerUsages, workspaceReferences, organizationReturn } = context.exports
const platform = { name: 'code', scope: 'platform', exportPath: 'root:railgrid:providers:code' }
const owned = { name: 'code', scope: 'organization', exportPath: 'root:railgrid:tenants:org:providers:code' }
test('same-name organization provider never resolves to platform registration or usage', () => {
  const providers = [{ name: 'code', apiExportPath: platform.exportPath }]
  assert.equal(platformProvider(owned, providers), undefined)
  assert.equal(platformProvider({ ...platform, exportPath: owned.exportPath }, providers), undefined)
  assert.equal(platformProvider(platform, providers), providers[0])
  const orgs = [{ name: 'org', displayName: 'Team', workspaces: [
    { uuid: 'a', displayName: 'Platform', providers: ['code'], providerReferences: [platform] },
    { uuid: 'b', displayName: 'Owned', providers: ['code'], providerReferences: [{ ...owned, terminating: true }] },
  ] }]
  assert.equal(providerUsages(platform, orgs).length, 1)
  assert.equal(providerUsages(owned, orgs)[0].displayName, 'Owned')
  assert.equal(providerUsages(owned, orgs)[0].state, 'Disabling')
})
test('legacy names stay unknown and unavailable reads do not invent bindings', () => {
  assert.equal(workspaceReferences({ providers: ['code'] })[0].scope, 'unknown')
  assert.equal(workspaceReferences({ providers: ['code'], providersUnavailable: true }).length, 0)
  assert.equal(providerUsages({ name: 'code', scope: 'unknown', exportPath: '' }, [{ name: 'org', workspaces: [{ providers: ['code'] }] }]).length, 0)
})
test('Metering return accepts only the selected organization and a fixed internal route', () => {
  assert.equal(organizationReturn({ returnOrganization: 'other', returnTo: 'https://example.com' }, 'org'), null)
  const target = organizationReturn({ returnOrganization: 'org', returnTo: 'https://example.com' }, 'org')
  assert.equal(target.path, '/bonkers/organizations')
  assert.equal(target.query.organization, 'org')
})

const { bindingRows, bindingKey, tableState, tableContext, changeTableQuery, providerReferenceLocation } = context.exports
test('organization inventory keeps separate rows for each workspace binding', () => {
  const org = { name: 'org', workspaces: [
    { uuid: 'one', displayName: 'Production', providers: ['code'], providerReferences: [platform] },
    { uuid: 'two', displayName: 'Staging', providers: ['code'], providerReferences: [{ ...platform, terminating: true }] },
    { uuid: 'three', displayName: 'Owned', providers: ['code'], providerReferences: [owned] },
  ] }
  const rows = bindingRows(org, [{ name: 'code', displayName: 'Code', apiExportPath: platform.exportPath, registered: true, ready: false }])
  assert.equal(rows.length, 3)
  assert.equal(new Set(rows.map(row => row.key)).size, 3)
  assert.equal(rows[1].bindingState, 'Disabling')
  assert.equal(rows[0].health, 'Not ready')
  assert.equal(rows[2].health, 'Not reported here')
  assert.equal(rows[0].key, bindingKey('one', platform))
})
test('related links and reload preserve URL-backed search, paging and sort without accepting arbitrary context', () => {
  const query = changeTableQuery({ organization: 'org', returnTo: 'https://example.com' }, 'bindings', {
    query: 'Code & tools', page: 2, pageSize: 25, sort: { key: 'workspace', direction: 'desc' },
  })
  const link = providerReferenceLocation(platform, 'org', 'one', query, 'organization')
  assert.equal(link.query.returnTo, undefined)
  assert.equal(link.query.originView, 'organization')
  const restored = tableState(link.query, 'bindings', ['displayName', 'workspace'])
  assert.equal(restored.query, 'Code & tools')
  assert.equal(restored.page, 2)
  assert.equal(restored.pageSize, 25)
  assert.equal(restored.sort.key, 'workspace')
  assert.equal(restored.sort.direction, 'desc')
  assert.equal(tableContext({ token: 'not-table-state', unknownQuery: 'no' }).token, undefined)
  const result = organizationReturn({ ...link.query, returnOrganization: 'org' }, 'org')
  assert.equal(result.query.bindingsQuery, 'Code & tools')
})
test('invalid URL table values fall back to supported controls', () => {
  const result = tableState({ bindingsQuery: ['invalid'], bindingsPage: '-3', bindingsSize: '17', bindingsSort: 'not-a-column', bindingsDirection: 'unexpected' }, 'bindings', ['displayName'])
  assert.equal(result.query, '')
  assert.equal(result.page, 1)
  assert.equal(result.pageSize, 10)
  assert.equal(result.sort.key, 'displayName')
})
