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

function deferred() {
  let resolve
  let reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

function refreshHarness(authFetch) {
  const context = {
    loading: { value: false }, refreshMode: { value: 'foreground' }, queuedRefreshMode: { value: null },
    loaded: { value: false }, status: { value: null }, items: { value: {} }, error: { value: null },
    lastCheckedAt: { value: '' }, diagnosticNow: { value: 0 }, pendingAssignments: { value: [] },
    disposed: false, refreshRunning: false, queuedRefresh: null,
    refreshOrganizations: () => {}, checkPending: () => {}, authFetch,
  }
  context.foregroundLoading = { get value() {
    return context.loading.value && (context.refreshMode.value === 'foreground' || context.queuedRefreshMode.value === 'foreground')
  } }
  context.performRefresh = productionFunction('performRefresh', context)
  context.runRefreshQueue = productionFunction('runRefreshQueue', context)
  context.refresh = productionFunction('refresh', context)
  return context
}

function response(value) {
  return { ok: true, json: async () => value }
}

async function flushUntil(predicate, message) {
  for (let i = 0; i < 20; i++) {
    if (predicate()) return
    await Promise.resolve()
  }
  assert.fail(message)
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
  const context = refreshHarness(async path => response(path === '/api/admin/metering' ? { installed: true, collections: ['plans'] } : { items: [{ metadata: { name: 'free' } }] }))
  context.refreshOrganizations = () => { organizationReads++; return new Promise(() => {}) }
  await context.refresh()
  assert.equal(context.loaded.value, true)
  assert.equal(context.loading.value, false)
  assert.equal(context.items.value.plans[0].metadata.name, 'free')
  assert.equal(organizationReads, 1)
})

test('archive waits for a serialized fresh read behind a pending background plan read', async () => {
  const stalePlanRead = deferred()
  const archivedPlanRead = deferred()
  const firstPlanReadStarted = deferred()
  const secondPlanReadStarted = deferred()
  let planReads = 0
  let activePlanReads = 0
  let maxActivePlanReads = 0
  let statusReads = 0
  const context = refreshHarness(path => {
    if (path === '/api/admin/metering') {
      statusReads++
      return Promise.resolve(response({ installed: true, writable: true, defaultPlan: 'free', collections: ['plans'] }))
    }
    assert.equal(path, '/api/admin/metering/plans')
    planReads++
    activePlanReads++
    maxActivePlanReads = Math.max(maxActivePlanReads, activePlanReads)
    const read = planReads === 1 ? stalePlanRead : archivedPlanRead
    if (planReads === 1) firstPlanReadStarted.resolve()
    else secondPlanReadStarted.resolve()
    return read.promise.then(result => { activePlanReads--; return result }, error => { activePlanReads--; throw error })
  })
  context.status.value = { writable: true, defaultPlan: 'free' }
  context.items.value = { plans: [{ metadata: { name: 'team' }, spec: { displayName: 'Team' } }] }
  context.writable = { value: true }
  context.busy = { value: null }
  context.actionError = { value: null }
  context.notice = { value: '' }
  context.planFor = name => context.items.value.plans?.find(plan => plan.metadata.name === name)
  context.isPlanArchived = plan => plan?.metadata.annotations?.['metering.railgrid.ai/archived'] === 'true'
  context.planLabel = name => context.planFor(name)?.spec?.displayName ?? name
  context.confirmDialog = async () => true
  const writes = []
  context.write = async (...args) => { writes.push(args) }

  const archive = productionFunction('archivePlan', context)
  const initialBackgroundRead = context.refresh(false, 'background')
  await firstPlanReadStarted.promise
  let archiveFinished = false
  const archived = archive('team').then(() => { archiveFinished = true })
  await flushUntil(() => context.queuedRefreshMode.value === 'foreground', 'archive should enqueue foreground reconciliation behind the active read')

  assert.equal(archiveFinished, false, 'archive stays busy while its post-write read is queued')
  assert.equal(context.busy.value, 'team')
  assert.equal(context.foregroundLoading.value, true, 'queued foreground work is announced immediately')
  assert.equal(planReads, 1, 'the queued read does not overlap the active Plan read')
  assert.deepEqual(writes, [['DELETE', 'plans/team']])

  stalePlanRead.resolve(response({ items: [{ metadata: { name: 'team' }, spec: { displayName: 'Team' } }] }))
  await secondPlanReadStarted.promise
  assert.equal(archiveFinished, false, 'the mutation does not finish until the fresh read completes')
  assert.equal(statusReads, 2)
  assert.equal(maxActivePlanReads, 1, 'reads are serialized')

  archivedPlanRead.resolve(response({ items: [{
    metadata: { name: 'team', annotations: { 'metering.railgrid.ai/archived': 'true' } },
    spec: { displayName: 'Team' },
  }] }))
  await archived
  await initialBackgroundRead

  assert.equal(archiveFinished, true)
  assert.equal(context.isPlanArchived(context.items.value.plans[0]), true, 'the final snapshot reflects the successful archive')
  assert.equal(context.busy.value, null)
  assert.equal(context.loading.value, false)
  assert.equal(context.foregroundLoading.value, false)
  assert.equal(planReads, 2)
  assert.equal(statusReads, 2)
})

test('unmount drops queued refreshes and ignores a response from the active read', async () => {
  const activePlanRead = deferred()
  const planReadStarted = deferred()
  let requests = 0
  const originalPlan = { metadata: { name: 'retained' } }
  const originalItems = { plans: [originalPlan] }
  const context = refreshHarness(path => {
    requests++
    if (path === '/api/admin/metering') return Promise.resolve(response({ installed: true, collections: ['plans'], workspace: 'snapshot' }))
    planReadStarted.resolve()
    return activePlanRead.promise
  })
  context.items.value = originalItems
  context.loaded.value = true
  context.lastCheckedAt.value = 'previous-check'
  context.diagnosticNow.value = 123
  const originalError = context.error.value

  const active = context.refresh(false, 'background')
  await planReadStarted.promise
  const queued = context.refresh(true, 'foreground')
  assert.equal(context.queuedRefreshMode.value, 'foreground')
  productionFunction('disposeRefreshes', context)()
  await queued
  assert.equal(context.queuedRefresh, null)
  assert.equal(context.queuedRefreshMode.value, null)

  activePlanRead.resolve(response({ items: [{ metadata: { name: 'late' } }] }))
  await active
  await context.refresh(true, 'foreground')

  assert.equal(requests, 2, 'the queued and post-unmount refreshes never issue requests')
  assert.deepEqual(context.status.value, { installed: true, collections: ['plans'], workspace: 'snapshot' })
  assert.equal(context.items.value, originalItems, 'the late response cannot replace the last snapshot')
  assert.equal(context.loaded.value, true)
  assert.equal(context.lastCheckedAt.value, 'previous-check')
  assert.equal(context.diagnosticNow.value, 123)
  assert.equal(context.error.value, originalError)
})

test('organization failure preserves its last snapshot and independent metering state', async () => {
  const context = {
    organizationsLoading: { value: false }, organizationsLoaded: { value: true },
    organizationError: { value: null }, organizationsCheckedAt: { value: 'previous-check' },
    orgs: { value: [{ name: 'retained' }] }, read: async () => { throw new Error('unavailable') }, checkPending: () => {}, disposed: false,
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

test('archive action preserves referenced plans and communicates retained limits and in-flight work', async () => {
  const plans = [
    { metadata: { name: 'free' }, spec: { displayName: 'Free' } },
    { metadata: { name: 'team' }, spec: { displayName: 'Team' } },
  ]
  const confirmations = []
  const writes = []
  let refreshes = 0
  let closed = 0
  const context = {
    planFor: name => plans.find(plan => plan.metadata.name === name),
    isPlanArchived: plan => plan?.metadata.annotations?.['metering.railgrid.ai/archived'] === 'true',
    status: { value: { defaultPlan: 'free' } }, writable: { value: true }, busy: { value: null },
    error: { value: null }, foregroundLoading: { value: false }, actionError: { value: null }, notice: { value: '' }, disposed: false,
    planLabel: name => plans.find(plan => plan.metadata.name === name)?.spec?.displayName ?? name,
    confirmDialog: async options => { confirmations.push(options); return true },
    write: async (...args) => writes.push(args), refresh: async () => { refreshes++ },
    selectedCollectionKey: { value: 'plans' }, selectedResourceId: { value: 'team' }, closeResource: () => { closed++ },
  }
  const archive = productionFunction('archivePlan', context)

  await archive('free')
  assert.equal(confirmations.length, 0, 'the platform default remains protected')

  // Existing references no longer block archiving.
  context.planReferenceCount = () => 4
  context.planHasReferences = () => true
  await archive('team')

  assert.equal(confirmations.length, 1)
  assert.equal(confirmations[0].title, 'Archive plan “Team”?')
  assert.match(confirmations[0].message, /Existing organization assignments and limits will stay in effect/)
  assert.match(confirmations[0].message, /New assignments will be blocked/)
  assert.match(confirmations[0].message, /Requests already in flight may finish/)
  assert.equal(confirmations[0].confirmLabel, 'Archive plan')
  assert.equal(confirmations[0].danger, undefined, 'archiving does not use destructive delete styling')
  assert.deepEqual(writes, [['DELETE', 'plans/team']])
  assert.match(context.notice.value, /archived.*Existing assignments and limits remain in effect/)
  assert.equal(refreshes, 1)
  assert.equal(closed, 0, 'the archived plan remains open for inspection')
})

test('archive action refuses a plan that became archived while confirmation was open', async () => {
  const plan = { metadata: { name: 'team' } }
  let writes = 0
  const context = {
    planFor: () => plan,
    isPlanArchived: item => item?.metadata.annotations?.['metering.railgrid.ai/archived'] === 'true',
    status: { value: { defaultPlan: 'free' } }, writable: { value: true }, busy: { value: null },
    error: { value: null }, foregroundLoading: { value: false }, actionError: { value: null }, notice: { value: '' }, disposed: false,
    planLabel: name => name,
    confirmDialog: async () => { plan.metadata.annotations = { 'metering.railgrid.ai/archived': 'true' }; return true },
    write: async () => { writes++ }, refresh: async () => {},
  }
  await productionFunction('archivePlan', context)('team')
  assert.equal(writes, 0)
})
