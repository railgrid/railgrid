import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

// Exercise the production navigation functions: list/detail transitions must
// keep unrelated query context and return to the originating collection.
const source = fs.readFileSync(new URL('./MeteringSection.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('MeteringSection.ts', script, ts.ScriptTarget.Latest, true)

function navigationFixture(query = {}) {
  const route = { path: '/bonkers/metering', query }
  const context = {
    organizationReturn: (query, orgId) => query.returnOrganization === orgId && orgId ? { path: '/bonkers/organizations', query: { organization: orgId } } : null,
    selectedOrgId: { get value() { return route.query.organization || '' } },
    route, document: { activeElement: null }, HTMLElement: class {}, resourceTrigger: null, organizationTrigger: null,
    selectedCollection: { get value() { return ['plans', 'accounts', 'entitlements', 'membershipreports', 'meteringconfigs'].includes(route.query.collection) ? { key: route.query.collection } : undefined } },
    selectedCollectionKey: { get value() { return route.query.collection || '' } },
    computed: fn => ({ get value() { return fn() } }),
    router: { push(location) { route.query = location.query; return Promise.resolve() } },
  }
  const declaration = parsed.statements.flatMap(node => ts.isVariableStatement(node) ? [...node.declarationList.declarations] : [])
    .find(node => node.name.getText(parsed) === 'resourceListLocation')
  const initialization = ts.transpileModule(`const ${declaration.getText(parsed)};`, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  context.resourceListLocation = runInNewContext(`${initialization}\nresourceListLocation`, context)
  for (const name of ['openResource', 'closeResource', 'selectTab', 'selectDiagnostic', 'planOrganizationLocation', 'rememberOrganizationTrigger', 'openPlanOrganization', 'closeOrganization']) {
    const declaration = parsed.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
    const code = ts.transpileModule(declaration.getText(parsed), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
    context[name] = runInNewContext(`${code}\n${name}`, context)
  }
  const organizationList = parsed.statements.flatMap(node => ts.isVariableStatement(node) ? [...node.declarationList.declarations] : [])
    .find(node => node.name.getText(parsed) === 'organizationListLocation')
  const organizationCode = ts.transpileModule(`const ${organizationList.getText(parsed)};`, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  context.organizationListLocation = runInNewContext(`${organizationCode}\norganizationListLocation`, context)
  return context
}

test('plan navigation retains unrelated context and returns to Plans', () => {
  const s = navigationFixture({ view: 'plans', review: 'kept' })
  s.openResource('plans', { name: 'team' })
  assert.equal(s.route.query.resource, 'team')
  assert.equal(s.route.query.collection, 'plans')
  assert.equal(s.route.query.organization, undefined)
  s.closeResource()
  assert.equal(s.route.query.resource, undefined)
  assert.equal(s.route.query.collection, undefined)
  assert.equal(s.route.query.view, 'plans')
  assert.equal(s.route.query.review, 'kept')
})

test('each diagnostic collection returns to Diagnostics after a cold deep link', () => {
  for (const collection of ['accounts', 'entitlements', 'membershipreports', 'meteringconfigs']) {
    const s = navigationFixture({ collection, resource: 'org-a', review: 'kept' })
    s.closeResource()
    assert.equal(s.route.query.view, 'diagnostics')
    assert.equal(s.route.query.collection, undefined)
    assert.equal(s.route.query.resource, undefined)
    assert.equal(s.route.query.review, 'kept')
  }
})

test('tab changes are addressable and preserve unrelated query context', () => {
  const s = navigationFixture({ review: 'kept' })
  s.selectTab('diagnostics')
  assert.equal(s.route.query.view, 'diagnostics')
  s.selectTab('organizations')
  assert.equal(s.route.query.view, undefined)
  assert.equal(s.route.query.review, 'kept')
})

test('related organization drill-down returns to its parent plan, including after reload', () => {
  const s = navigationFixture({ view: 'plans', collection: 'plans', resource: 'free', review: 'kept' })
  s.openPlanOrganization({ name: 'org-a' })
  assert.equal(s.route.query.organization, 'org-a')
  assert.equal(s.route.query.resource, 'free')
  assert.equal(s.route.query.collection, 'plans')
  const reloaded = navigationFixture({ ...s.route.query })
  reloaded.closeOrganization()
  assert.equal(reloaded.route.query.organization, undefined)
  assert.equal(reloaded.route.query.resource, 'free')
  assert.equal(reloaded.route.query.view, 'plans')
  assert.equal(reloaded.route.query.review, 'kept')
})

test('Diagnostics selection and detail back links retain the selected collection', () => {
  const s = navigationFixture({ view: 'diagnostics', review: 'kept' })
  s.selectDiagnostic('accounts')
  assert.equal(s.route.query.diagnostic, 'accounts')
  s.openResource('accounts', { name: 'org-a' })
  s.closeResource()
  assert.equal(s.route.query.diagnostic, 'accounts')
  assert.equal(s.route.query.review, 'kept')
})

function productionFunction(name, context) {
  const declaration = parsed.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
  assert.ok(declaration, name)
  const code = ts.transpileModule(declaration.getText(parsed), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  return runInNewContext(`${code}\n${name}`, context)
}

test('timeouts and stopping a watch are isolated by organization and preserve saved intent', () => {
  const context = {
    pending: { value: new Map([['a', { name: 'a', started: Date.now() - 91_000, timedOut: false }], ['b', { name: 'b', started: Date.now(), timedOut: false }]]) },
    orgs: { value: [] }, organizationError: { value: null }, notice: { value: '' }, poll: 1,
    orgLabel: name => name, clears: 0, clearTimeout: () => { context.clears++ },
  }
  context.pendingAssignments = { get value() { return [...context.pending.value.values()] } }
  const check = productionFunction('checkPending', context)
  const isPending = productionFunction('assignmentPending', context)
  const stop = productionFunction('stopWatching', context)
  check()
  assert.equal(isPending('a'), false, 'a timed-out organization can be superseded')
  assert.equal(isPending('b'), true, 'another organization keeps its own active watch')
  stop('a')
  assert.equal(context.pending.value.get('b').name, 'b')
  assert.equal(context.clears, 0)
  assert.match(context.notice.value, /saved plan assignment remains in effect/)
  stop('b')
  assert.equal(context.pending.value.size, 0)
  assert.equal(context.clears, 1)
})

test('a pending organization read cannot hold successful metering reads or plan data', { timeout: 2000 }, async () => {
  let organizationReads = 0
  const context = {
    loading: { value: false }, refreshMode: { value: 'foreground' }, error: { value: null },
    loaded: { value: false }, status: { value: null }, items: { value: {} },
    lastCheckedAt: { value: '' }, diagnosticNow: { value: 0 }, pendingAssignments: { value: [] }, checkPending: () => {},
    refreshOrganizations: () => { organizationReads++; return new Promise(() => {}) },
    authFetch: async path => ({ ok: true, json: async () => path === '/api/admin/metering' ? { installed: true, collections: ['plans'] } : { items: [{ metadata: { name: 'free' } }] } }),
  }
  await productionFunction('refresh', context)()
  assert.equal(context.loaded.value, true)
  assert.equal(context.loading.value, false)
  assert.equal(context.items.value.plans[0].metadata.name, 'free')
  assert.equal(organizationReads, 1)
})

test('organization failure preserves its last snapshot and independent metering state', async () => {
  const context = {
    organizationsLoading: { value: false }, organizationsLoaded: { value: true },
    organizationError: { value: null }, organizationsCheckedAt: { value: 'previous-check' },
    orgs: { value: [{ name: 'retained' }] }, read: async () => { throw new Error('unavailable') }, checkPending: () => {},
  }
  await productionFunction('refreshOrganizations', context)()
  assert.equal(context.orgs.value[0].name, 'retained')
  assert.equal(context.organizationsLoaded.value, true)
  assert.equal(context.organizationsLoading.value, false)
  assert.match(context.organizationError.value, /Plans and raw metering resources remain available/)
  assert.equal(context.organizationsCheckedAt.value, 'previous-check')
})

test('prototype-named organizations can start, retry, and stop independent assignment watches', async () => {
  const context = { ref: value => ({ value }), orgLabel: name => name, notice: { value: '' }, poll: undefined,
    refresh: async () => {}, schedulePoll: () => {}, clearTimeout: () => {} }
  const declaration = parsed.statements.flatMap(node => ts.isVariableStatement(node) ? [...node.declarationList.declarations] : [])
    .find(node => node.name.getText(parsed) === 'pending')
  const code = ts.transpileModule(`const ${declaration.getText(parsed)};`, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  context.pending = runInNewContext(`${code}\npending`, context)
  context.pendingAssignments = { get value() { return [...context.pending.value.values()] } }
  const isPending = productionFunction('assignmentPending', context)
  const retry = productionFunction('retryPending', context)
  const stop = productionFunction('stopWatching', context)
  for (const name of ['constructor', 'toString', '__proto__']) {
    assert.equal(isPending(name), false)
    retry(name)
    assert.equal(isPending(name), false)
    context.pending.value.set(name, { name, started: 0, timedOut: true })
    retry(name)
    assert.equal(isPending(name), true)
    stop(name)
    assert.equal(isPending(name), false)
  }
  assert.equal(context.pending.value.size, 0)
})

test('plan deletion fails closed until organization references are authoritative', () => {
  const context = { organizationsLoading: { value: false }, organizationsLoaded: { value: false }, organizationError: { value: null },
    planReferenceCount: () => 0, orgs: { value: [] }, pendingAssignments: { value: [] } }
  const blocked = productionFunction('planHasReferences', context)
  assert.equal(blocked('team'), true, 'initial pending organization read')
  context.organizationsLoaded.value = true
  assert.equal(blocked('team'), false, 'authoritative empty snapshot')
  context.organizationError.value = 'unavailable'
  assert.equal(blocked('team'), true, 'failed refresh of an empty snapshot')
  context.organizationError.value = null
  context.organizationsLoading.value = true
  assert.equal(blocked('team'), true, 'retry cannot reuse an empty stale snapshot')
  context.organizationsLoading.value = false
  context.orgs.value = [{ plan: 'team' }]
  assert.equal(blocked('team'), true, 'intent without an entitlement')
  context.orgs.value = []
  context.pendingAssignments.value = [{ target: 'team' }]
  assert.equal(blocked('team'), true, 'assignment in flight')
})

test('delete action cannot bypass unknown references or a failure during confirmation', async () => {
  let blocked = true
  let confirmations = 0
  let writes = 0
  const context = { planReferenceCount: () => 0, planHasReferences: () => blocked,
    status: { value: { defaultPlan: 'free' } }, planLabel: name => name,
    confirmDialog: async () => { confirmations++; blocked = true; return true },
    busy: { value: null }, actionError: { value: null }, write: async () => { writes++ } }
  const remove = productionFunction('deletePlan', context)
  await remove('team')
  assert.equal(confirmations, 0)
  blocked = false
  await remove('team')
  assert.equal(confirmations, 1)
  assert.equal(writes, 0)
})
