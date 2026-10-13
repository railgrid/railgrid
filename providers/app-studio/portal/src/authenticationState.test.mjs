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
const {
  authenticationSupportIsError,
  authenticationSupportKind,
  authenticationSupportNotice,
  authenticationSupportStatus,
  authenticationSupportTone,
  canCreateCompatibleAuthenticationProject,
  hasPersistedProductionRuntime,
  shouldLoadAuthenticationGrants,
} = await import(moduleURL)

const script = component.match(/<script setup[^>]*>([\s\S]*?)<\/script>/)?.[1]
assert.ok(script, 'ProjectAuthentication.vue should contain a setup script')
const sourceWithoutImports = script.replace(/^import\s[\s\S]*?\sfrom\s+['"][^'"]+['"];?\s*/gm, '')
const helperSource = source.replace(/^export /gm, '')
const harness = `
const ref = (value) => ({ value })
const computed = (getter) => ({ get value() { return getter() } })
const watch = () => {}
const onBeforeUnmount = () => {}
const componentProps = { ctx: null, project: { uid: 'project-uid', name: 'demo' } }
const defineProps = () => componentProps
const emitted = []
const mockEmit = (...args) => emitted.push(args)
const defineEmits = () => mockEmit
let authResponse = { enabled: true, supported: true, environments: [] }
let authenticationResponses = []
let authError = null
let grantsResponse = { items: [] }
let grantResponses = []
let apiGrantsError = null
let configureResponse = null
let confirmResult = true
let deferredConfirmation = null
let resolveConfirmation = null
const confirmOptions = []
const apiCalls = { authentication: 0, grants: 0, configurations: [], approvals: [], revocations: [] }
const resetScenario = (scenario = {}) => {
  authResponse = scenario.response || { enabled: true, supported: true, environments: [] }
  authenticationResponses = []
  authError = scenario.authError || null
  grantsResponse = scenario.grantsResponse || { items: [] }
  grantResponses = []
  apiGrantsError = scenario.grantsError || null
  configureResponse = scenario.configureResponse || null
  confirmResult = scenario.confirmResult ?? true
  deferredConfirmation = null
  resolveConfirmation = null
  confirmOptions.length = 0
  apiCalls.authentication = 0
  apiCalls.grants = 0
  apiCalls.configurations.length = 0
  apiCalls.approvals.length = 0
  apiCalls.revocations.length = 0
  emitted.length = 0
  componentProps.ctx = null
  componentProps.project = { uid: 'project-uid', name: 'demo' }
}
const setDeferredConfirmation = () => {
  deferredConfirmation = new Promise(resolve => { resolveConfirmation = resolve })
}
const setAuthenticationResponses = (responses) => { authenticationResponses = [...responses] }
const setGrantResponses = (responses) => { grantResponses = [...responses] }
const changeContext = (ctx) => { componentProps.ctx = ctx }
const api = {
  getProjectAuthentication: async () => {
    apiCalls.authentication++
    if (authError) throw authError
    if (authenticationResponses.length) return authenticationResponses.shift()
    return authResponse
  },
  getProjectAuthenticationGrants: async () => {
    apiCalls.grants++
    if (apiGrantsError) throw apiGrantsError
    if (grantResponses.length) return grantResponses.shift()
    return grantsResponse
  },
  configureProjectAuthentication: async (_ctx, _name, enabled) => {
    apiCalls.configurations.push(enabled)
    return configureResponse || { ...authResponse, enabled }
  },
  addProjectAuthenticationGrant: async (_ctx, _name, user, appID) => {
    apiCalls.approvals.push({ user, appID })
    return { items: [] }
  },
  removeProjectAuthenticationGrant: async (_ctx, _name, grant) => {
    apiCalls.revocations.push(grant)
    return { items: [] }
  },
}
const confirmDialog = async (options) => {
  confirmOptions.push(options)
  if (deferredConfirmation) {
    const current = deferredConfirmation
    deferredConfirmation = null
    return current
  }
  return confirmResult
}
${helperSource}
${sourceWithoutImports}
export {
  apiCalls, approve, configure, confirmOptions, emitted, error, errorSource,
  grants, grantsError, grantsLoaded, grantsLoading, load, loadGrants, productionRuntimeExists,
  resetScenario, resolveConfirmation, setAuthenticationResponses, setDeferredConfirmation, setGrantResponses,
  changeContext as setContext,
  loading, notice, state, supportIsError, supportNotice, supportStatus, supportTone, user,
}
`
const { outputText: harnessOutput } = ts.transpileModule(harness, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const harnessURL = `data:text/javascript;base64,${Buffer.from(harnessOutput).toString('base64')}`
const caller = await import(harnessURL)

test('distinguishes supported, pending, unbound, and definitively unavailable templates', () => {
  const pending = { enabled: false, supported: false, supportReason: 'pending' }
  const unbound = { enabled: false, supported: false, supportReason: 'template_not_selected' }
  const undeclared = { enabled: false, supported: false, supportReason: 'not_declared' }
  const missing = { enabled: false, supported: false, supportReason: 'template_missing' }
  const invalid = { enabled: false, supported: false, supportReason: 'invalid_contract' }
  const unknown = { enabled: false, supported: false, supportReason: 'unknown_version' }

  assert.equal(authenticationSupportKind({ enabled: false, supported: true }), 'supported')
  assert.equal(authenticationSupportKind(pending), 'pending')
  assert.equal(authenticationSupportKind(unbound), 'template-unselected')
  assert.equal(authenticationSupportKind(undeclared), 'unavailable')
  assert.equal(canCreateCompatibleAuthenticationProject(pending), false)
  assert.equal(canCreateCompatibleAuthenticationProject(unbound), false)
  assert.equal(canCreateCompatibleAuthenticationProject(undeclared), true)
  assert.equal(canCreateCompatibleAuthenticationProject(missing), true)
  assert.equal(canCreateCompatibleAuthenticationProject(invalid), true)
  assert.equal(canCreateCompatibleAuthenticationProject(unknown), true)
  assert.equal(canCreateCompatibleAuthenticationProject({ ...undeclared, enabled: true }), false)
  assert.match(authenticationSupportNotice(pending), /still being checked/)
  assert.match(authenticationSupportNotice(unbound), /Development settings/)
  assert.match(authenticationSupportNotice(undeclared), /does not support Railgrid Auth/)
  assert.doesNotMatch(authenticationSupportNotice(undeclared), /does not declare/)
  assert.equal(authenticationSupportStatus(unbound), 'Choose a template')
  assert.equal(authenticationSupportTone(pending), 'warning')
  assert.equal(authenticationSupportIsError(invalid), true)
  assert.equal(authenticationSupportIsError(unknown), true)
  assert.equal(authenticationSupportIsError({ ...invalid, enabled: true }), true)

  const enabledMissing = { ...missing, enabled: true }
  assert.match(authenticationSupportNotice(enabledMissing), /^Railgrid Auth remains enabled,/)
  assert.equal(canCreateCompatibleAuthenticationProject(enabledMissing), false)
  assert.equal(authenticationSupportNotice({ enabled: false, supported: true }), '')
})

test('loads auth state without fetching production grants until access management opens', async () => {
  caller.resetScenario({ response: { enabled: true, supported: true, environments: [] } })
  await caller.load()
  assert.equal(caller.apiCalls.authentication, 1)
  assert.equal(caller.apiCalls.grants, 0)
  assert.equal(caller.error.value, '')
  assert.equal(caller.productionRuntimeExists.value, false)

  await caller.loadGrants()
  assert.equal(caller.apiCalls.grants, 0, 'there is no persisted Production app to list')
})

test('loads and manages grants for an existing but unready Production runtime', async () => {
  caller.resetScenario({ response: { enabled: true, supported: true, environments: [
    { environment: 'production', runtimeExists: true, configured: false, runtimeReady: false },
  ] } })
  await caller.load()
  assert.equal(caller.apiCalls.grants, 0, 'grant loading is deferred until access management opens')
  assert.equal(caller.productionRuntimeExists.value, true)

  await caller.loadGrants()
  assert.equal(caller.apiCalls.grants, 1)
  caller.user.value = 'existing-user'
  await caller.approve()
  assert.deepEqual(caller.apiCalls.approvals, [{ user: 'existing-user', appID: 'project-uid/production' }])
})

test('still asks the hub to validate duplicate or ambiguous Production targets on open', async () => {
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
    await caller.loadGrants()
    assert.equal(caller.apiCalls.grants, 1)
    assert.match(caller.error.value, /caller_context_denied/)
    assert.match(caller.grantsError.value, /caller_context_denied/)
  }
})

test('surfaces an authentication read failure with a retryable status error', async () => {
  caller.resetScenario({ authError: new Error('temporary authentication lookup failure') })
  await caller.load()
  assert.equal(caller.apiCalls.grants, 0)
  assert.match(caller.error.value, /temporary authentication lookup failure/)
  assert.equal(caller.errorSource.value, 'authentication')
})

test('enables supported projects and prompts the owner to finish app sign-in setup', async () => {
  caller.resetScenario({ response: { enabled: false, supported: true, environments: [] } })
  await caller.load()
  await caller.configure()

  assert.deepEqual(caller.apiCalls.configurations, [true])
  assert.equal(caller.state.value.enabled, true)
  assert.match(caller.notice.value, /Set up or review sign-in/)
  assert.doesNotMatch(caller.notice.value, /sign-in screen|access checks/)
})

test('confirms disabling and describes the unsupported re-enable limit accurately', async () => {
  caller.resetScenario({ response: { enabled: true, supported: false, supportReason: 'not_declared', environments: [] }, confirmResult: false })
  await caller.load()
  await caller.configure()
  assert.deepEqual(caller.apiCalls.configurations, [])
  assert.match(caller.confirmOptions[0].message, /protected routes to reject requests/)
  assert.match(caller.confirmOptions[0].message, /cannot turn it back on until its Railgrid Auth support is restored/)
  assert.doesNotMatch(caller.confirmOptions[0].message, /You can turn it back on/)
})

test('warns about incomplete support before disabling an enabled project', async () => {
  caller.resetScenario({ response: { enabled: true, supported: false, supportReason: 'pending', environments: [] }, confirmResult: false })
  await caller.load()
  await caller.configure()

  assert.match(caller.confirmOptions[0].message, /support check is incomplete/)
  assert.doesNotMatch(caller.confirmOptions[0].message, /cannot turn it back on/)
  assert.deepEqual(caller.apiCalls.configurations, [])
})

test('does not disable Railgrid Auth if the project context changes during confirmation', async () => {
  caller.resetScenario({ response: { enabled: true, supported: true, environments: [] } })
  await caller.load()
  caller.setDeferredConfirmation()
  const disabling = caller.configure()
  caller.setContext({ orgUUID: 'new-org', workspaceUUID: 'new-workspace', user: { userId: 'new-user' }, token: 'new-context-token' })
  await caller.load()
  caller.resolveConfirmation(true)
  await disabling
  assert.deepEqual(caller.apiCalls.configurations, [])
})

test('ignores an older authentication response after the project context changes', async () => {
  caller.resetScenario()
  const deferred = () => {
    let resolve
    const promise = new Promise(done => { resolve = done })
    return { promise, resolve }
  }
  const stale = deferred()
  const current = deferred()
  caller.setAuthenticationResponses([stale.promise, current.promise])

  const staleLoad = caller.load()
  caller.setContext({ orgUUID: 'new-org', workspaceUUID: 'new-workspace', user: { userId: 'new-user' }, token: 'new-context-token' })
  const currentLoad = caller.load()
  const currentState = { enabled: false, supported: true, environments: [] }
  current.resolve(currentState)
  await currentLoad
  stale.resolve({ enabled: true, supported: false, supportReason: 'not_declared', environments: [] })
  await staleLoad

  assert.equal(caller.state.value, currentState)
  assert.equal(caller.loading.value, false)
})

test('ignores a grant response from an earlier project context', async () => {
  caller.resetScenario({ response: { enabled: true, supported: true, environments: [
    { environment: 'production', runtimeExists: true },
  ] } })
  const deferred = () => {
    let resolve
    const promise = new Promise(done => { resolve = done })
    return { promise, resolve }
  }
  const stale = deferred()
  const current = deferred()
  caller.setGrantResponses([stale.promise, current.promise])
  await caller.load()

  const staleLoad = caller.loadGrants()
  caller.setContext({ orgUUID: 'new-org', workspaceUUID: 'new-workspace', user: { userId: 'new-user' }, token: 'new-context-token' })
  await caller.load()
  const currentLoad = caller.loadGrants()
  const currentGrants = { items: [{ name: 'new-grant', user: 'new-user' }] }
  current.resolve(currentGrants)
  await currentLoad
  stale.resolve({ items: [{ name: 'stale-grant', user: 'stale-user' }] })
  await staleLoad

  assert.deepEqual(caller.grants.value, currentGrants.items)
  assert.equal(caller.grantsLoading.value, false)
})
