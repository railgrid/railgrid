import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./authenticationState.ts', import.meta.url), 'utf8')
const component = await readFile(new URL('./ProjectAuthentication.vue', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const { hasPersistedProductionRuntime, shouldLoadAuthenticationGrants } = await import(moduleURL)

const script = component.match(/<script setup[^>]*>([\s\S]*?)<\/script>/)?.[1]
assert.ok(script, 'ProjectAuthentication.vue should contain a setup script')
const sourceWithoutImports = script.replace(/^import[^\n]*\n/gm, '')
const helperSource = source.replace(/^export /gm, '')
const harness = `
const ref = (value) => ({ value })
const computed = (getter) => ({ get value() { return getter() } })
const watch = () => {}
const onBeforeUnmount = () => {}
const defineProps = () => ({ ctx: null, project: { uid: 'project-uid', name: 'demo' } })
let authResponse = { enabled: true, supported: true, environments: [] }
let authError = null
let grantsResponse = { items: [] }
let grantsError = null
const apiCalls = { authentication: 0, grants: 0, approvals: [] }
const resetScenario = (scenario = {}) => {
  authResponse = scenario.response || { enabled: true, supported: true, environments: [] }
  authError = scenario.authError || null
  grantsResponse = scenario.grantsResponse || { items: [] }
  grantsError = scenario.grantsError || null
  apiCalls.authentication = 0
  apiCalls.grants = 0
  apiCalls.approvals.length = 0
}
const api = {
  getProjectAuthentication: async () => {
    apiCalls.authentication++
    if (authError) throw authError
    return authResponse
  },
  getProjectAuthenticationGrants: async () => {
    apiCalls.grants++
    if (grantsError) throw grantsError
    return grantsResponse
  },
  configureProjectAuthentication: async () => authResponse,
  addProjectAuthenticationGrant: async (_ctx, _name, user, appID) => {
    apiCalls.approvals.push({ user, appID })
    return { items: [] }
  },
  removeProjectAuthenticationGrant: async () => ({ items: [] }),
}
${helperSource}
${sourceWithoutImports}
export { apiCalls, approve, error, grants, load, productionRuntimeExists, resetScenario, state, user }
`
const { outputText: harnessOutput } = ts.transpileModule(harness, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const harnessURL = `data:text/javascript;base64,${Buffer.from(harnessOutput).toString('base64')}`
const caller = await import(harnessURL)

test('does not request authentication grants before production has a persisted runtime', () => {
  assert.equal(shouldLoadAuthenticationGrants([]), false, 'unpromoted project should not list grants')
  assert.equal(shouldLoadAuthenticationGrants([
    { environment: 'production', runtimeExists: false, configured: false, runtimeReady: false },
  ]), false, 'production binding without a persisted Instance should not list grants')
  assert.equal(hasPersistedProductionRuntime([
    { environment: 'production', runtimeExists: false, configured: false, runtimeReady: false },
  ]), false)
})

test('lists grants for a persisted production runtime even while it is starting or unhealthy', () => {
  const environments = [{ environment: 'production', runtimeExists: true, configured: false, runtimeReady: false }]
  assert.equal(shouldLoadAuthenticationGrants(environments), true)
  assert.equal(hasPersistedProductionRuntime(environments), true)
})

test('keeps malformed duplicate targets and older unknown responses on the fail-closed list path', () => {
  assert.equal(shouldLoadAuthenticationGrants([
    { environment: 'production', runtimeExists: false },
    { environment: 'production', runtimeExists: false },
  ]), true, 'duplicate production entries must reach the hub proof check')
  assert.equal(shouldLoadAuthenticationGrants([
    { environment: 'production' },
  ]), true, 'an older response cannot positively establish absence')
})

test('loads auth configuration without requesting grants before production exists', async () => {
  caller.resetScenario({ response: { enabled: true, supported: true, environments: [] } })
  await caller.load()
  assert.equal(caller.apiCalls.authentication, 1)
  assert.equal(caller.apiCalls.grants, 0)
  assert.equal(caller.error.value, '')
  assert.equal(caller.productionRuntimeExists.value, false)
})

test('does not request grants for a production binding without a persisted Instance', async () => {
  caller.resetScenario({ response: { enabled: true, supported: true, environments: [
    { environment: 'production', runtimeExists: false, configured: false, runtimeReady: false },
  ] } })
  await caller.load()
  assert.equal(caller.apiCalls.grants, 0)
  assert.equal(caller.error.value, '')
  assert.equal(caller.productionRuntimeExists.value, false)
})

test('lists grants and permits approval for an existing but unready production runtime', async () => {
  caller.resetScenario({ response: { enabled: true, supported: true, environments: [
    { environment: 'production', runtimeExists: true, configured: false, runtimeReady: false },
  ] } })
  await caller.load()
  assert.equal(caller.apiCalls.grants, 1)
  assert.equal(caller.error.value, '')
  assert.equal(caller.productionRuntimeExists.value, true)

  caller.user.value = 'existing-user'
  await caller.approve()
  assert.deepEqual(caller.apiCalls.approvals, [{ user: 'existing-user', appID: 'project-uid/production' }])
})

test('still asks the hub to validate duplicate or ambiguous production targets', async () => {
  for (const environments of [
    [
      { environment: 'production', runtimeExists: false },
      { environment: 'production', runtimeExists: false },
    ],
    [{ environment: 'production' }],
  ]) {
    caller.resetScenario({
      response: { enabled: true, supported: true, environments },
      grantsError: new Error('403 caller_context_denied'),
    })
    await caller.load()
    assert.equal(caller.apiCalls.grants, 1)
    assert.match(caller.error.value, /caller_context_denied/)
  }
})

test('surfaces a transient authentication configuration read failure without listing grants', async () => {
  caller.resetScenario({ authError: new Error('temporary authentication lookup failure') })
  await caller.load()
  assert.equal(caller.apiCalls.grants, 0)
  assert.match(caller.error.value, /temporary authentication lookup failure/)
})
