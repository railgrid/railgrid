// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import test from 'node:test'
import ts from 'typescript'

const portalRoot = path.resolve(new URL('../..', import.meta.url).pathname)
let nextHarnessInstance = 0

function deferred() {
  let resolve
  let reject
  const promise = new Promise((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

async function loadCaller(fileName) {
  const source = await readFile(path.join(portalRoot, 'src', fileName), 'utf8')
  const script = source.match(/<script setup[^>]*>([\s\S]*?)<\/script>/)?.[1]
  assert.ok(script, `${fileName} should contain a setup script`)

  const flowSource = (await readFile(path.join(portalRoot, 'src/composables/useProviderEnableFlow.ts'), 'utf8'))
    .replace(/^import[\s\S]*?from ['"][^'"]+['"]\n/gm, '')
  const isWizard = fileName.includes('WelcomeWizard')
  let helpers = ''
  if (isWizard) {
    for (const name of ['onboardingCopy', 'onboardingPlan', 'onboardingState']) {
      const helper = await readFile(path.join(portalRoot, `src/lib/${name}.ts`), 'utf8')
      helpers += `\n${helper.replace(/^import[\s\S]*?from ['"][^'"]+['"]\n/gm, '')}`
    }
  }

  // Compile the real caller script with deterministic doubles for Vue and its
  // stores. This keeps the deferred request behavior under test without
  // copying the caller's fencing logic into a second implementation.
  const sourceWithoutImports = script.replace(/^import[\s\S]*?from ['"][^'"]+['"]\n/gm, '')
  const harness = `
const useScopedNavigation = () => ({ scopePath: (path) => path })

const ref = (value) => ({ value })
const computed = (getter) => typeof getter === 'function'
  ? ({ get value() { return getter() } })
  : ({ get value() { return getter.get() }, set value(value) { getter.set(value) } })
const watchCalls = []
const watch = (source, callback) => watchCalls.push({ source, callback })
const routeState = { query: {}, hash: '', params: { orgID: 'org-a', workspaceID: 'workspace-a' } }
const routeHistory = [{ query: {}, hash: '' }]
let routeHistoryIndex = 0
function applyRoute(location, replace = false) {
  const current = { query: { ...location.query }, hash: location.hash ?? routeState.hash }
  if (replace) routeHistory[routeHistoryIndex] = current
  else {
    routeHistory.splice(routeHistoryIndex + 1)
    routeHistory.push(current)
    routeHistoryIndex++
  }
  routeState.query = { ...current.query }
  routeState.hash = current.hash
}
const routerState = {
  push: async location => applyRoute(location),
  replace: async location => applyRoute(location, true),
  back: () => { if (routeHistoryIndex > 0) { routeHistoryIndex--; routeState.query = { ...routeHistory[routeHistoryIndex].query }; routeState.hash = routeHistory[routeHistoryIndex].hash } },
  forward: () => { if (routeHistoryIndex + 1 < routeHistory.length) { routeHistoryIndex++; routeState.query = { ...routeHistory[routeHistoryIndex].query }; routeState.hash = routeHistory[routeHistoryIndex].hash } },
}
function resetRouteState() {
  routeState.query = {}
  routeState.hash = ''
  routeHistory.splice(0, routeHistory.length, { query: {}, hash: '' })
  routeHistoryIndex = 0
}
const useRoute = () => routeState
const useRouter = () => routerState
const documentListeners = new Map()
const document = {
  visibilityState: 'visible',
  addEventListener(event, callback) {
    const listeners = documentListeners.get(event) ?? new Set()
    listeners.add(callback)
    documentListeners.set(event, listeners)
  },
  removeEventListener(event, callback) {
    documentListeners.get(event)?.delete(callback)
  },
  dispatchEvent(event) {
    for (const callback of documentListeners.get(event) ?? []) callback()
  },
}
const onMounted = () => {}
const unmountCallbacks = []
const onUnmounted = (callback) => unmountCallbacks.push(callback)
const timers = new Map()
const timerDelays = new Map()
let nextTimer = 1
const setTimeout = (callback, delay = 0) => { const id = nextTimer++; timers.set(id, callback); timerDelays.set(id, delay); return id }
const clearTimeout = (id) => { timerDelays.delete(id); return timers.delete(id) }
const nextTick = async () => {}
const defineEmits = () => () => {}
const defineProps = () => ({ startAtGoal: false })
const AlertCircle = {}
const AlertTriangle = {}
const ArrowLeft = {}
const ArrowRight = {}
const ArrowUpCircle = {}
const Boxes = {}
const Building2 = {}
const Check = {}
const CheckCircle2 = {}
const ExternalLink = {}
const FolderTree = {}
const GripHorizontal = {}
const GripVertical = {}
const Hexagon = {}
const LayoutDashboard = {}
const Loader2 = {}
const PanelLeftClose = {}
const PanelLeftOpen = {}
const Plus = {}
const Puzzle = {}
const RefreshCw = {}
const Rocket = {}
const Search = {}
const Server = {}
const Settings2 = {}
const Sparkles = {}
const X = {}
const toastCalls = []
const toast = (...args) => { toastCalls.push(args) }
let confirmBehavior = false
const confirmCalls = []
const confirmDialog = options => {
  confirmCalls.push(options)
  const result = typeof confirmBehavior === 'function' ? confirmBehavior(options) : confirmBehavior
  return Promise.resolve(result)
}
const setConfirmBehavior = value => { confirmBehavior = value }
const categoryIcons = {}
const fallbackCategoryIcon = null
const providerBindingAction = () => null
const providerStore = {
  loaded: false,
  bindingsByProvider: {},
  bindingsLoadState: 'ready',
  bindingsStale: false,
  bindingsOrgUUID: 'org-a',
  bindingsWorkspace: 'workspace-a',
  bindingsRequestOrgUUID: 'org-a',
  bindingsRequestWorkspaceUUID: 'workspace-a',
  refreshBindings: async () => {},
  enableable: [],
  selfHostable: [],
  items: [],
  categories: [],
  enable: async () => undefined,
  disable: async () => undefined,
  isEnabled: () => false,
  isSelfHosted: () => false,
  isSelfManaged: () => false,
  missingDependencies: () => [],
  hasMissingDependencies: () => false,
  dependencyLabels: () => [],
  byName: () => null,
  load: () => undefined,
}
const orgProviderStore = {
  resetForOrganization: () => {
    orgProviderStore.items = []
    orgProviderStore.byName = {}
    orgProviderStore.loaded = false
    orgProviderStore.loading = false
  },
  supported: false,
  loaded: false,
  loading: false,
  byName: {},
  items: [],
  eligibleInstallTargets: [],
  installTargetsEligible: false,
  installTargetsReason: null,
  installTargetsLoaded: false,
  installTargetsLoading: false,
  installTargetsError: null,
  isSelfHosted: () => false,
  load: async () => false,
  loadInstallTargets: () => undefined,
  register: async () => null,
  instructions: async () => null,
  remove: async () => undefined,
}
const tenantStore = { orgUUID: 'org-a', workspaceUUID: 'workspace-a', workspaceMode: 'workspace', activeWorkspace: null, activeOrg: null }
const useProvidersStore = () => providerStore
const useOrgProvidersStore = () => orgProviderStore
const useTenantStore = () => tenantStore
const authStore = { user: { userId: 'alice', email: '' }, self: null }
const useAuthStore = () => authStore
const welcomeIdentity = (auth) => auth.user?.userId || auth.user?.email || auth.self?.rbacIdentity || ''
${helpers}
${flowSource}
${sourceWithoutImports}
${isWizard ? '' : 'function setInventoryPollingMounted(value) { inventoryPollingMounted = value }'}
export {
  actionError,
  busy,
  openEnableDialog,
  closeEnableDialog,
  dialogProvider,
  dialogScope,
  onDialogConfirm,
  providerStore,
  orgProviderStore,
  tenantStore,
  authStore,
  watchCalls,
  timers, timerDelays, unmountCallbacks,
  toastCalls,
  ${isWizard ? 'props, plan, goal, goalExplicit, step, audience, restore, storageKey, persist, selectAudience, canOpen, setupReady, setupDataCurrent, canSkipSetup, memberJourney, explore, exploring,' : 'onDisable, disableOperations, hasPendingDeletion, route, router, setTab, startInstall, cancelInstall, resumeManualInstall, showInstructions, canManageSelfHostedCredentials, activeRegistration, finishInstall, selfHostedHeading, selfHostNotice, removeSelfHosted, selfHostRemoval, removalOperations, clearSelfHostRemoval, confirmingRemoval, selfHostError, trackDeletingSelfHostedProviders, normalizeProviderRoute, installProvider, inventoryScope, currentTenantScope, refreshOrgProviderInventory, restartInventoryPolling, stopInventoryPolling, stopSelfHostRemovalPolling, onInventoryVisibilityChange, document, setInventoryPollingMounted, resetRouteState,'}
  confirmCalls,
  setConfirmBehavior,
}
`
  const { outputText } = ts.transpileModule(harness, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.ESNext,
    },
  })
  const caller = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}#caller-${nextHarnessInstance++}`)
  return {
    ...caller,
    setScope(orgUUID, workspaceUUID = 'workspace-a') {
      caller.tenantStore.orgUUID = orgUUID
      caller.tenantStore.workspaceUUID = workspaceUUID
      for (const watchCall of caller.watchCalls) {
        if (watchCall.source === caller.dialogScope) watchCall.callback()
      }
    },
    resetRoute() {
      caller.resetRouteState()
    },
    takeTimer() {
      const [id, callback] = caller.timers.entries().next().value ?? []
      if (id === undefined) return null
      const delay = caller.timerDelays.get(id)
      caller.timers.delete(id)
      caller.timerDelays.delete(id)
      return { id, callback, delay }
    },
    setVisibility(state) {
      caller.document.visibilityState = state
      caller.document.dispatchEvent('visibilitychange')
    },
  }
}

const callers = await Promise.all([
  loadCaller('pages/ProvidersPage.vue'),
  loadCaller('components/WelcomeWizard.vue'),
])

for (const [index, caller] of callers.entries()) {
  const label = index === 0 ? 'ProvidersPage' : 'WelcomeWizard'

  test(`${label} reports a dismissed same-scope deferred failure as a toast`, async () => {
    caller.toastCalls.length = 0
    caller.setScope('org-a', 'workspace-a')
    const provider = { name: 'edges', displayName: 'Edges', requires: [] }
    if (label === 'WelcomeWizard') {
      caller.tenantStore.activeWorkspace = { role: 'admin' }
      caller.tenantStore.activeOrg = { role: 'member' }
    }
    const pending = deferred()
    caller.providerStore.enable = () => pending.promise
    caller.openEnableDialog(provider)
    assert.equal(caller.dialogProvider.value, provider)

    const request = caller.onDialogConfirm([])
    await Promise.resolve()
    assert.equal(caller.busy.value.edges, true)

    caller.closeEnableDialog()
    pending.reject(new Error('deferred gateway failure'))
    await request

    assert.equal(caller.actionError.value, null)
    assert.equal(caller.toastCalls.length, 1)
    assert.equal(caller.toastCalls[0][0], 'error')
    assert.match(caller.toastCalls[0][1], /Could not enable Edges: deferred gateway failure/)
    assert.equal(caller.toastCalls[0][2].scope, 'org-a/workspace-a/workspace')
  })

  test(`${label} drops a deferred failure after a scope change`, async () => {
    caller.toastCalls.length = 0
    caller.setScope('org-a', 'workspace-a')
    const provider = { name: 'code', displayName: 'Code', requires: [] }
    if (label === 'WelcomeWizard') {
      caller.tenantStore.activeWorkspace = { role: 'admin' }
      caller.tenantStore.activeOrg = { role: 'member' }
    }
    const pending = deferred()
    caller.providerStore.enable = () => pending.promise
    caller.openEnableDialog(provider)

    const request = caller.onDialogConfirm([])
    await Promise.resolve()
    caller.setScope('org-b')
    pending.reject(new Error('old-scope failure'))
    await request

    assert.equal(caller.actionError.value, null)
    assert.equal(caller.toastCalls.length, 0)
    caller.setScope('org-a')
    caller.closeEnableDialog()
  })
}


test('WelcomeWizard orders transitive dependencies and tolerates missing and cyclic catalogs', () => {
  const caller = callers[1]
  const items = [
    { name: 'app-studio', requires: [{ provider: 'code' }, { provider: 'infrastructure' }] },
    { name: 'code', requires: [{ provider: 'edges' }] },
    { name: 'edges', requires: [{ provider: 'code' }] },
  ]
  caller.providerStore.byName = name => items.find(p => p.name === name)
  caller.goal.value = 'app-studio'
  assert.deepEqual(caller.plan.value.map(p => p.name), ['edges', 'code', 'infrastructure', 'app-studio'])
  assert.equal(caller.plan.value[2].provider, undefined)
})

test('ProvidersPage ignores a deferred disable failure after a same-provider enable starts in the new scope', async () => {
  const caller = callers[0]
  caller.setScope('org-a', 'workspace-a')
  caller.actionError.value = null
  const provider = { name: 'edges', displayName: 'Edges', requires: [] }
  const staleDisable = deferred()
  const currentEnable = deferred()
  caller.providerStore.disable = () => staleDisable.promise
  caller.providerStore.enable = () => currentEnable.promise

  const disabling = caller.onDisable(provider)
  assert.equal(caller.busy.value.edges, true)
  caller.setScope('org-b')
  caller.openEnableDialog(provider)
  const enabling = caller.onDialogConfirm([])
  assert.equal(caller.busy.value.edges, true)

  staleDisable.reject(new Error('old workspace disable failed'))
  await disabling
  assert.equal(caller.busy.value.edges, true, 'the stale disable cannot release the new enable busy state')
  assert.equal(caller.actionError.value, null, 'the stale disable cannot replace the current scope error')

  currentEnable.resolve()
  await enabling
  assert.equal(caller.busy.value.edges, undefined)
})

test('WelcomeWizard fails closed until admin access is confirmed, including the team audience', () => {
  const caller = callers[1]
  const beforeWorkspace = caller.tenantStore.activeWorkspace
  const beforeOrg = caller.tenantStore.activeOrg
  const provider = { name: 'edges', displayName: 'Edges', requires: [] }
  try {
    caller.audience.value = 'team'
    caller.tenantStore.activeWorkspace = null
    caller.tenantStore.activeOrg = null
    assert.equal(caller.memberJourney.value, true)
    caller.openEnableDialog(provider)
    assert.equal(caller.dialogProvider.value, null, 'unknown roles must not open consent')
    assert.equal(caller.setupDataCurrent.value, false, 'unknown roles need the check-again state')

    caller.tenantStore.activeWorkspace = { role: 'member' }
    caller.tenantStore.activeOrg = { role: 'member' }
    assert.equal(caller.memberJourney.value, true)
    caller.openEnableDialog(provider)
    assert.equal(caller.dialogProvider.value, null, 'team audience never grants enable access to a member')

    caller.tenantStore.activeWorkspace = { role: 'admin' }
    assert.equal(caller.memberJourney.value, false)
    caller.openEnableDialog(provider)
    assert.equal(caller.dialogProvider.value, provider)
    caller.closeEnableDialog()
  } finally {
    caller.tenantStore.activeWorkspace = beforeWorkspace
    caller.tenantStore.activeOrg = beforeOrg
  }
})

test('WelcomeWizard only opens an enabled, ready Provider with UI and satisfied dependencies', () => {
  const caller = callers[1]
  const provider = { name: 'edges', ready: false, serving: { ui: {} } }
  caller.providerStore.byName = () => provider
  caller.goal.value = 'edges'
  caller.providerStore.isEnabled = () => true
  assert.equal(caller.canOpen.value, false)
  provider.ready = true
  assert.equal(caller.canOpen.value, true)
  caller.providerStore.hasMissingDependencies = () => true
  assert.equal(caller.canOpen.value, false)
  caller.providerStore.hasMissingDependencies = () => false
  provider.serving = {}
  assert.equal(caller.canOpen.value, false)
  provider.serving = { ui: {} }
  caller.providerStore.isEnabled = () => false
  assert.equal(caller.canOpen.value, false)
})

test('WelcomeWizard restores valid choices per Workspace and ignores corrupt preferences', () => {
  const caller = callers[1]
  const storage = new Map()
  const oldStorage = globalThis.localStorage
  globalThis.localStorage = { getItem: key => storage.get(key) ?? null }
  try {
    storage.set(caller.storageKey.value, JSON.stringify({ audience: 'personal', goal: 'edges', goalExplicit: true, step: 2 }))
    caller.restore()
    assert.equal(caller.audience.value, 'personal')
    assert.equal(caller.goal.value, 'edges')
    assert.equal(caller.goalExplicit.value, true)
    assert.equal(caller.step.value, 2)
    caller.tenantStore.workspaceUUID = 'workspace-b'
    caller.restore()
    assert.equal(caller.step.value, 0)
    assert.equal(caller.goal.value, 'edges')
    assert.equal(caller.goalExplicit.value, false)
    storage.set(caller.storageKey.value, JSON.stringify({ audience: 'personal', goal: 'edges', step: 1 }))
    caller.restore()
    assert.equal(caller.goal.value, 'edges')
    assert.equal(caller.goalExplicit.value, true)
    storage.set(caller.storageKey.value, '{broken')
    assert.doesNotThrow(() => caller.restore())
    storage.set(caller.storageKey.value, JSON.stringify({ audience: 'unknown', goal: 'edges', step: 2 }))
    caller.restore()
    assert.equal(caller.step.value, 0)
  } finally {
    globalThis.localStorage = oldStorage
    caller.tenantStore.workspaceUUID = 'workspace-a'
  }
})

test('WelcomeWizard follows the audience default until a goal is explicitly chosen', () => {
  const caller = callers[1]
  caller.audience.value = 'team'
  caller.goal.value = 'edges'
  caller.goalExplicit.value = false

  caller.selectAudience('personal')
  assert.equal(caller.goal.value, 'app-studio')
  caller.selectAudience('explore')
  assert.equal(caller.goal.value, 'agents')

  caller.goal.value = 'infrastructure'
  caller.goalExplicit.value = true
  caller.selectAudience('team')
  assert.equal(caller.goal.value, 'infrastructure')
})

test('WelcomeWizard persists whether the selected goal is intentional', () => {
  const caller = callers[1]
  const storage = new Map()
  const oldStorage = globalThis.localStorage
  globalThis.localStorage = {
    getItem: key => storage.get(key) ?? null,
    setItem: (key, value) => storage.set(key, value),
  }
  try {
    caller.audience.value = 'personal'
    caller.goal.value = 'edges'
    caller.goalExplicit.value = true
    caller.step.value = 1
    caller.persist()
    assert.deepEqual(JSON.parse(storage.get(caller.storageKey.value)), {
      step: 1,
      audience: 'personal',
      goal: 'edges',
      goalExplicit: true,
    })
  } finally {
    globalThis.localStorage = oldStorage
  }
})

test('WelcomeWizard forwards accepted compositions to the provider enable request', async () => {
  const caller = callers[1]
  const beforeWorkspace = caller.tenantStore.activeWorkspace
  const beforeOrg = caller.tenantStore.activeOrg
  caller.tenantStore.activeWorkspace = { role: 'admin' }
  caller.tenantStore.activeOrg = { role: 'member' }
  const provider = { name: 'app-studio', displayName: 'App Studio', requires: [] }
  const acceptedClaims = [{ group: 'apps.railgrid.ai', resource: 'projects' }]
  const acceptedHubAccess = [{ capability: 'provider-proxy', scope: 'workspace' }]
  const acceptedCompositions = [{ provider: 'code', group: 'code.railgrid.ai', resource: 'repositories' }]
  const calls = []
  caller.providerStore.enable = async (...args) => calls.push(args)
  caller.openEnableDialog(provider)

  await caller.onDialogConfirm(acceptedClaims, acceptedHubAccess, acceptedCompositions)

  assert.deepEqual(calls, [[provider, acceptedClaims, acceptedHubAccess, acceptedCompositions]])
  assert.equal(caller.dialogProvider.value, null)
  caller.tenantStore.activeWorkspace = beforeWorkspace
  caller.tenantStore.activeOrg = beforeOrg
})


test('WelcomeWizard exploration continues without choosing or enabling a Provider', () => {
  const caller = callers[1]
  caller.goalExplicit.value = true
  caller.goal.value = 'app-studio'
  caller.step.value = 1
  caller.explore()
  assert.equal(caller.step.value, 2)
  assert.equal(caller.goal.value, 'explore')
  assert.equal(caller.exploring.value, true)
  assert.deepEqual(caller.plan.value, [])
  const oldStorage = globalThis.localStorage
  globalThis.localStorage = { getItem: () => JSON.stringify({ audience: 'explore', goal: 'explore', step: 2 }) }
  try {
    caller.restore()
    assert.equal(caller.step.value, 2)
    assert.equal(caller.exploring.value, true)
  } finally { globalThis.localStorage = oldStorage }
})


test('manual onboarding re-entry opens goals while automatic entry resumes the saved step', () => {
  const caller = callers[1]
  const oldStorage = globalThis.localStorage
  globalThis.localStorage = {
    getItem: () => JSON.stringify({ audience: 'personal', goal: 'agents', goalExplicit: true, step: 2 }),
  }
  try {
    caller.props.startAtGoal = true
    caller.restore()
    assert.equal(caller.step.value, 1)
    assert.equal(caller.audience.value, 'personal')
    assert.equal(caller.goal.value, 'agents')
    caller.props.startAtGoal = false
    caller.restore()
    assert.equal(caller.step.value, 2)
    assert.equal(caller.goal.value, 'agents')
    globalThis.localStorage.getItem = () => null
    caller.restore()
    assert.equal(caller.step.value, 0, 'new automatic entry starts with context')
    caller.props.startAtGoal = true
    caller.restore()
    assert.equal(caller.step.value, 1, 'explicit entry without storage still opens goals')
  } finally {
    caller.props.startAtGoal = false
    globalThis.localStorage = oldStorage
  }
})


test('only members with current, fully ready prerequisites can bypass setup', () => {
  const caller = callers[1]
  const store = caller.providerStore
  const tenant = caller.tenantStore
  const beforeStore = { ...store }
  const beforeTenant = { ...tenant }
  const items = [
    { name: 'app-studio', ready: true, serving: { ui: {} }, requires: [{ provider: 'code' }] },
    { name: 'code', ready: true, requires: [{ provider: 'infrastructure' }] },
    { name: 'infrastructure', ready: true },
  ]
  try {
    Object.assign(tenant, { activeOrg: { role: 'member' }, activeWorkspace: { role: 'member' } })
    Object.assign(store, {
      loaded: true, loading: false, error: null, catalogOrgUUID: tenant.orgUUID,
      bindingsOrgUUID: tenant.orgUUID, bindingsWorkspace: tenant.workspaceUUID,
      bindingsLoadState: 'ready', bindingsStale: false,
      byName: name => items.find(item => item.name === name),
      isEnabled: () => true, hasMissingDependencies: () => false,
    })
    caller.goal.value = 'app-studio'
    caller.audience.value = 'personal'
    assert.equal(caller.canSkipSetup.value, true)
    caller.audience.value = 'team'
    assert.equal(caller.canSkipSetup.value, false, 'team setup keeps its review even for members')
    caller.audience.value = 'personal'
    tenant.activeOrg.role = 'admin'
    assert.equal(caller.canSkipSetup.value, false)
    tenant.activeOrg.role = 'member'
    tenant.activeWorkspace.role = 'admin'
    assert.equal(caller.canSkipSetup.value, false)
    tenant.activeWorkspace.role = 'member'
    store.isEnabled = name => name !== 'infrastructure'
    assert.equal(caller.canSkipSetup.value, false, 'transitive requirements must be enabled')
    assert.equal(caller.setupReady.value, false, 'final launch also needs transitive prerequisites')
    store.isEnabled = () => true
    items[2].ready = false
    assert.equal(caller.canSkipSetup.value, false, 'transitive requirements must be ready')
    items[2].ready = true
    for (const [key, value] of [['loading', true], ['bindingsStale', true], ['bindingsLoadState', 'error'], ['bindingsWorkspace', 'another-workspace'], ['catalogOrgUUID', 'another-org']]) {
      const previous = store[key]
      store[key] = value
      assert.equal(caller.canSkipSetup.value, false, key)
      assert.equal(caller.setupReady.value, false, 'final launch: ' + key)
      store[key] = previous
    }
    items[0].serving = {}
    assert.equal(caller.canSkipSetup.value, false, 'a web UI must exist')
  } finally {
    Object.assign(store, beforeStore)
    Object.assign(tenant, beforeTenant)
  }
})


test('wizard progress is isolated between accounts in the same Workspace', () => {
  const caller = callers[1]
  const original = globalThis.localStorage
  const saved = new Map()
  globalThis.localStorage = { getItem: key => saved.get(key) ?? null, setItem: (key, value) => saved.set(key, value) }
  try {
    caller.authStore.user.userId = 'alice'
    caller.audience.value = 'personal'
    caller.goal.value = 'app-studio'
    caller.goalExplicit.value = true
    caller.step.value = 2
    caller.persist()
    const aliceKey = caller.storageKey.value
    caller.authStore.user.userId = 'bob'
    assert.notEqual(caller.storageKey.value, aliceKey)
    caller.restore()
    assert.equal(caller.step.value, 0)
    assert.equal(caller.audience.value, 'team')
    caller.authStore.user.userId = 'alice'
    caller.restore()
    assert.equal(caller.step.value, 2)
    assert.equal(caller.audience.value, 'personal')
    caller.authStore.user.userId = ''
    assert.equal(caller.storageKey.value, '')
    caller.persist()
    assert.equal(saved.has(''), false)
  } finally {
    caller.authStore.user.userId = 'alice'
    globalThis.localStorage = original
  }
})


test('ProvidersPage refreshes terminating bindings through completion and cancels pending work on unmount', async () => {
  const caller = await loadCaller('pages/ProvidersPage.vue')
  const deletionWatch = caller.watchCalls.find(w => typeof w.source === 'function' && String(w.source).includes('hasPendingDeletion'))
  assert.ok(deletionWatch)
  caller.providerStore.bindingsByProvider = { kuery: { terminating: true } }
  let refreshes = 0
  const pending = deferred()
  caller.providerStore.refreshBindings = async () => {
    refreshes++
    await pending.promise
    caller.providerStore.bindingsByProvider = {}
  }
  deletionWatch.callback()
  assert.equal(caller.timers.size, 1)
  const timer = caller.takeTimer()
  const read = timer.callback()
  assert.equal(refreshes, 1)
  assert.equal(caller.timers.size, 0, 'no overlapping poll while the read is pending')
  pending.resolve()
  await read
  assert.equal(caller.hasPendingDeletion(), false)
  assert.equal(caller.timers.size, 0, 'completion stops polling')

  caller.providerStore.bindingsByProvider = { kuery: { terminating: true } }
  deletionWatch.callback()
  const nextTimer = caller.takeTimer()
  const late = deferred()
  caller.providerStore.refreshBindings = () => late.promise
  const lateRead = nextTimer.callback()
  caller.unmountCallbacks.forEach(callback => callback())
  late.resolve()
  await lateRead
  assert.equal(caller.timers.size, 0, 'unmount fences an in-flight read from rescheduling')
})

test('ProvidersPage restores Self-Hosting navigation and normalizes legacy management URLs without reading credentials', () => {
  const caller = callers[0]
  caller.resetRoute()
  caller.route.hash = '#providers'
  caller.route.query = { billing: 'portal' }

  caller.setTab('self-hosting')
  assert.deepEqual(caller.route.query, { billing: 'portal', tab: 'self-hosting' })
  caller.startInstall({ name: 'kuery' })
  assert.deepEqual(caller.route.query, { billing: 'portal', tab: 'self-hosting', install: 'kuery' })
  caller.resumeManualInstall()
  assert.deepEqual(caller.route.query, { billing: 'portal', tab: 'self-hosting', install: 'kuery', manual: '1' })
  assert.equal(caller.route.hash, '#providers')

  caller.router.back()
  assert.deepEqual(caller.route.query, { billing: 'portal', tab: 'self-hosting' })
  caller.router.forward()
  assert.equal(caller.route.query.install, 'kuery')
  assert.equal(caller.route.query.manual, '1', 'manual choice shares the install history entry')
  caller.cancelInstall()
  assert.equal(caller.route.query.install, undefined)
  assert.equal(caller.route.query.manual, undefined)
  assert.equal(caller.route.query.tab, 'self-hosting')

  caller.orgProviderStore.supported = true
  caller.orgProviderStore.loaded = true
  caller.orgProviderStore.items = [{ name: 'kuery', displayName: 'Kuery' }]
  caller.orgProviderStore.byName = { kuery: caller.orgProviderStore.items[0] }
  caller.inventoryScope.value = caller.currentTenantScope.value
  let instructionReads = 0
  caller.orgProviderStore.instructions = async () => { instructionReads++; return { kubeconfig: 'must-not-be-read' } }
  caller.route.query = { billing: 'portal', manage: 'kuery' }
  caller.normalizeProviderRoute()
  assert.deepEqual(caller.route.query, { billing: 'portal', tab: 'self-hosting' })
  assert.equal(instructionReads, 0, 'legacy links return to the list without fetching scoped credentials')
})

test('ProvidersPage Ready completion clears wizard route state and returns focus to the list', async () => {
  const caller = await loadCaller('pages/ProvidersPage.vue')
  caller.resetRoute()
  caller.route.query = { tab: 'self-hosting', install: 'quickstart', manual: '1', manage: 'quickstart', keep: 'yes' }
  caller.orgProviderStore.items = [{ name: 'quickstart', displayName: 'Quickstart' }]
  caller.orgProviderStore.loaded = true
  caller.inventoryScope.value = caller.currentTenantScope.value
  let focusCount = 0
  caller.selfHostedHeading.value = { focus: () => { focusCount++ } }

  await caller.finishInstall('quickstart')

  assert.deepEqual(caller.route.query, { tab: 'self-hosting', keep: 'yes' })
  assert.match(caller.selfHostNotice.value, /Quickstart is ready\. Workspace access is managed in Catalog\./)
  assert.equal(focusCount, 1)

  caller.route.query = { tab: 'self-hosting', install: 'quickstart' }
  caller.selfHostNotice.value = null
  focusCount = 0
  const resolveNavigations = []
  const originalReplace = caller.router.replace
  caller.router.replace = () => new Promise(resolve => { resolveNavigations.push(resolve) })
  const completion = caller.finishInstall('quickstart')
  caller.setScope('org-b', 'workspace-b')
  for (const resolve of resolveNavigations) resolve()
  await completion
  assert.equal(caller.selfHostNotice.value, null, 'a stale Ready response cannot announce completion in another org')
  assert.equal(focusCount, 0)
  caller.router.replace = originalReplace
  caller.setScope('org-a', 'workspace-a')
})

test('ProvidersPage refuses to disable a changed workspace binding after migration confirmation', async () => {
  const caller = await loadCaller('pages/ProvidersPage.vue')
  caller.setScope('org-a', 'workspace-a')
  caller.tenantStore.activeOrg = { role: 'admin', catalogEntryCreation: 'admins' }
  const provider = { name: 'quickstart', displayName: 'Quickstart', scope: 'org', shadowsPlatform: true, ready: true, export: { name: 'quickstart.providers.railgrid.ai' } }
  caller.providerStore.items = [provider]
  caller.providerStore.byName = name => name === provider.name ? provider : null
  caller.providerStore.isSelfManaged = item => item?.scope === 'org'
  caller.providerStore.isEnabled = () => true
  const originalBinding = { bindingName: 'binding-quickstart', exportPath: 'root:providers:quickstart', selfHosted: false }
  const changedBinding = { ...originalBinding, bindingName: 'binding-quickstart-new', selfHosted: true }
  caller.providerStore.bindingsByProvider = { quickstart: originalBinding }
  let writes = 0
  caller.providerStore.disable = async () => { writes++ }
  const pendingRefresh = deferred()
  caller.providerStore.refreshBindings = async () => {
    await pendingRefresh.promise
    caller.providerStore.bindingsByProvider = { quickstart: changedBinding }
  }
  caller.setConfirmBehavior(true)

  const disabling = caller.onDisable(provider)
  await Promise.resolve()
  assert.equal(caller.confirmCalls.at(-1).title, 'Disable the platform copy of Quickstart?')
  pendingRefresh.resolve()
  await disabling

  assert.equal(writes, 0, 'a binding that changed during confirmation is never disabled')
  assert.match(caller.actionError.value, /binding changed while confirmation was open/)
})

test('ProvidersPage waits for a fresh organization inventory before choosing install review or resume', async () => {
  const caller = callers[0]
  caller.resetRoute()
  caller.route.query = { tab: 'self-hosting', install: 'kuery' }
  caller.orgProviderStore.supported = true
  caller.orgProviderStore.loaded = false
  caller.orgProviderStore.loading = false
  caller.orgProviderStore.items = []
  caller.orgProviderStore.byName = {}
  caller.providerStore.loaded = true
  caller.providerStore.selfHostable = [{ name: 'kuery', displayName: 'Kuery' }]

  const pending = deferred()
  caller.orgProviderStore.load = async () => {
    await pending.promise
    caller.orgProviderStore.items = [{ name: 'kuery', displayName: 'Kuery', registered: true, ready: false }]
    caller.orgProviderStore.byName = { kuery: caller.orgProviderStore.items[0] }
    caller.orgProviderStore.loaded = true
    return true
  }
  const request = caller.refreshOrgProviderInventory()
  assert.equal(caller.installProvider.value, null, 'stale or absent inventory must not decide whether to resume')
  pending.resolve()
  await request
  assert.equal(caller.installProvider.value.resume, true, 'an existing org provider opens in status-only resume mode')
  assert.equal(caller.installProvider.value.manual, false)
  assert.equal(caller.route.query.install, 'kuery', 'the intent remains in the URL across refresh')

  caller.inventoryScope.value = null
  caller.orgProviderStore.loaded = false
  caller.orgProviderStore.items = []
  caller.orgProviderStore.byName = {}
  caller.orgProviderStore.load = async () => {
    caller.orgProviderStore.loaded = true
    return true
  }
  await caller.refreshOrgProviderInventory()
  caller.normalizeProviderRoute()
  assert.equal(caller.installProvider.value.resume, false, 'a missing registration preserves the initial review step')
  assert.equal(caller.route.query.install, 'kuery')
})

test('ProvidersPage keeps routed install mounted during same-scope readiness polling', () => {
  const caller = callers[0]
  caller.resetRoute()
  const provider = { name: 'kuery', displayName: 'Kuery', registered: true, ready: false }
  caller.orgProviderStore.supported = true
  caller.orgProviderStore.loaded = true
  caller.orgProviderStore.loading = false
  caller.orgProviderStore.items = [provider]
  caller.orgProviderStore.byName = { kuery: provider }
  caller.providerStore.loaded = true
  caller.providerStore.selfHostable = [{ name: 'kuery', displayName: 'Kuery' }]
  caller.inventoryScope.value = caller.currentTenantScope.value
  caller.route.query = { tab: 'self-hosting', install: 'kuery' }

  assert.equal(caller.installProvider.value.resume, true)
  caller.orgProviderStore.loading = true
  assert.equal(caller.installProvider.value.resume, true, 'a status poll cannot unmount the running wizard')

  caller.route.query = { tab: 'self-hosting', install: 'kuery', manage: 'kuery' }
  caller.normalizeProviderRoute()
  assert.equal(caller.route.query.install, undefined, 'legacy management routes exit the wizard to the Self-Hosting list')
  assert.equal(caller.route.query.manage, undefined)
  assert.equal(caller.route.query.tab, 'self-hosting')
})

test('ProvidersPage clears malformed, unknown, and old-scope route state safely', () => {
  const caller = callers[0]
  caller.resetRoute()
  caller.orgProviderStore.supported = true
  caller.orgProviderStore.loaded = true
  caller.orgProviderStore.loading = false
  caller.orgProviderStore.items = []
  caller.orgProviderStore.byName = {}
  caller.providerStore.loaded = true
  caller.providerStore.selfHostable = []
  caller.inventoryScope.value = caller.currentTenantScope.value

  caller.route.query = { keep: 'yes', tab: ['self-hosting', 'catalog'], install: '../kuery' }
  caller.normalizeProviderRoute()
  assert.deepEqual(caller.route.query, { keep: 'yes' }, 'duplicate or malformed query values return to the safe default')

  caller.route.query = { tab: 'self-hosting', manage: 'missing-provider' }
  caller.normalizeProviderRoute()
  assert.deepEqual(caller.route.query, { tab: 'self-hosting' }, 'legacy management routes normalize to the list')

  caller.route.query = { tab: 'self-hosting', install: 'kuery', manage: 'kuery' }
  caller.setScope('org-b', 'workspace-b')
  assert.deepEqual(caller.route.query, {}, 'a tenant-scope change removes old tab and provider selections')

  caller.resetRoute()
  caller.orgProviderStore.loaded = true
  caller.orgProviderStore.items = [{ name: 'kuery', displayName: 'Kuery', registered: true, ready: false }]
  caller.orgProviderStore.byName = { kuery: caller.orgProviderStore.items[0] }
  caller.providerStore.loaded = true
  caller.providerStore.selfHostable = [{ name: 'kuery', displayName: 'Kuery' }]
  caller.inventoryScope.value = caller.currentTenantScope.value
  caller.route.query = { tab: 'self-hosting', install: 'kuery', manual: '1' }
  assert.equal(caller.installProvider.value.resume, true)
  assert.equal(caller.installProvider.value.manual, true, 'a persisted manual selection resumes the manual progress path')

  caller.orgProviderStore.items = []
  caller.orgProviderStore.byName = {}
  caller.route.query = { tab: 'self-hosting', install: 'kuery', manual: '1' }
  caller.normalizeProviderRoute()
  assert.equal(caller.installProvider.value.resume, false, 'an unregistered provider starts at installation review')
  assert.equal(caller.installProvider.value.manual, false, 'a stale manual marker cannot resume an unregistered provider')
  assert.equal(caller.route.query.install, 'kuery', 'the selected provider remains available for review')
  assert.equal(caller.route.query.manual, undefined, 'fresh inventory removes the stale manual marker')

  caller.route.query = { tab: 'self-hosting', install: 'kuery', manual: ['1', '1'] }
  caller.normalizeProviderRoute()
  assert.equal(caller.route.query.manual, undefined, 'malformed manual route markers are discarded')

  caller.route.query = { tab: 'self-hosting', manual: '1' }
  caller.normalizeProviderRoute()
  assert.equal(caller.route.query.manual, undefined, 'manual mode cannot outlive its install route')

  caller.providerStore.selfHostable = []
  caller.route.query = { tab: 'self-hosting', install: 'missing', manual: '1' }
  caller.normalizeProviderRoute()
  assert.equal(caller.route.query.install, undefined, 'unknown manual routes are discarded')
  assert.equal(caller.route.query.manual, undefined)
})

test('ProvidersPage classifies a fresh manual deep link as review without page-side mutation', () => {
  const caller = callers[0]
  caller.resetRoute()
  caller.orgProviderStore.supported = true
  caller.orgProviderStore.loaded = true
  caller.orgProviderStore.loading = false
  caller.orgProviderStore.items = []
  caller.orgProviderStore.byName = {}
  caller.providerStore.loaded = true
  caller.providerStore.selfHostable = [{ name: 'kuery', displayName: 'Kuery' }]
  caller.providerStore.byName = name => name === 'kuery' ? { name, displayName: 'Kuery' } : null
  caller.inventoryScope.value = caller.currentTenantScope.value
  caller.route.query = { tab: 'self-hosting', install: 'kuery', manual: '1' }
  let registrations = 0
  let instructionReads = 0
  caller.orgProviderStore.register = async () => { registrations++; return null }
  caller.orgProviderStore.instructions = async () => { instructionReads++; return null }

  caller.normalizeProviderRoute()

  assert.deepEqual(caller.route.query, { tab: 'self-hosting', install: 'kuery' })
  assert.deepEqual(caller.installProvider.value, {
    name: 'kuery',
    displayName: 'Kuery',
    resume: false,
    manual: false,
  })
  assert.equal(registrations, 0, 'route normalization must not create an organization provider')
  assert.equal(instructionReads, 0, 'fresh install review must not fetch registered-provider credentials')
})

test('ProvidersPage does not fetch self-hosted credentials for members under admin-only policy, even when the handler is called directly', async () => {
  const caller = callers[0]
  caller.activeRegistration.value = null
  caller.tenantStore.activeOrg = { role: 'member', catalogEntryCreation: 'admins' }
  let reads = 0
  caller.orgProviderStore.instructions = async () => {
    reads++
    return { provider: { name: 'kuery' }, kubeconfig: 'must-not-be-read' }
  }

  assert.equal(caller.canManageSelfHostedCredentials.value, false)
  await caller.showInstructions('kuery')
  assert.equal(reads, 0)
  assert.equal(caller.activeRegistration.value, null)
})

test('ProvidersPage preserves credential access for admins and members allowed by organization policy', async () => {
  const caller = callers[0]
  let reads = 0
  caller.orgProviderStore.instructions = async name => {
    reads++
    return { provider: { name }, kubeconfig: 'fixture-secret' }
  }

  for (const activeOrg of [
    { role: 'admin', catalogEntryCreation: 'admins' },
    { role: 'member', catalogEntryCreation: 'members' },
  ]) {
    caller.activeRegistration.value = null
    caller.tenantStore.activeOrg = activeOrg
    assert.equal(caller.canManageSelfHostedCredentials.value, true)
    await caller.showInstructions('kuery')
    assert.equal(caller.activeRegistration.value.provider.name, 'kuery')
  }
  assert.equal(reads, 2)
})

test('ProvidersPage serializes removal confirmations and leaves the provider untouched when canceled', async () => {
  const caller = callers[0]
  caller.tenantStore.activeOrg = { role: 'admin', catalogEntryCreation: 'admins' }
  caller.tenantStore.orgUUID = 'org-a'
  caller.tenantStore.workspaceUUID = 'workspace-a'
  caller.confirmCalls.length = 0
  let resolveConfirmation
  caller.setConfirmBehavior(() => new Promise(resolve => { resolveConfirmation = resolve }))
  let removes = 0
  caller.orgProviderStore.remove = async () => { removes++ }

  const first = caller.removeSelfHosted('quickstart')
  await Promise.resolve()
  assert.equal(caller.confirmingRemoval.value, true)
  assert.equal(caller.confirmCalls.length, 1)
  assert.equal(caller.selfHostRemoval.value.quickstart, 'confirming')

  await caller.removeSelfHosted('quickstart')
  assert.equal(caller.confirmCalls.length, 1, 'a repeated click must not replace the pending singleton confirmation')
  resolveConfirmation(false)
  await first
  assert.equal(removes, 0)
  assert.equal(caller.confirmingRemoval.value, false)
  assert.equal(caller.selfHostRemoval.value.quickstart, undefined)
})

test('ProvidersPage keeps an accepted workspace deletion visible and polls until inventory confirms removal', async () => {
  const caller = callers[0]
  caller.route.query = { tab: 'self-hosting' }
  caller.setInventoryPollingMounted(true)
  caller.tenantStore.activeOrg = { role: 'admin', catalogEntryCreation: 'admins' }
  caller.tenantStore.orgUUID = 'org-a'
  caller.tenantStore.workspaceUUID = 'workspace-a'
  caller.orgProviderStore.loaded = true
  caller.orgProviderStore.items = [{ name: 'quickstart', displayName: 'Quickstart', deleting: false }]
  caller.orgProviderStore.byName = { quickstart: caller.orgProviderStore.items[0] }
  caller.inventoryScope.value = caller.currentTenantScope.value
  caller.setConfirmBehavior(true)
  caller.confirmCalls.length = 0
  let removes = 0
  caller.orgProviderStore.remove = async () => {
    removes++
    const deleting = { name: 'quickstart', displayName: 'Quickstart', deleting: true, ready: false }
    caller.orgProviderStore.items = [deleting]
    caller.orgProviderStore.byName = { quickstart: deleting }
  }

  await caller.removeSelfHosted('quickstart')
  assert.equal(removes, 1)
  assert.equal(caller.orgProviderStore.byName.quickstart.deleting, true)
  assert.equal(caller.selfHostRemoval.value.quickstart, 'deleting')
  assert.equal(caller.selfHostNotice.value, null)
  assert.equal(caller.timers.size, 1)
  assert.equal(caller.timerDelays.values().next().value, 2000, 'removal checks start at the fast cadence')

  caller.orgProviderStore.load = async () => {
    caller.orgProviderStore.items = []
    caller.orgProviderStore.byName = {}
    return true
  }
  const timer = caller.takeTimer()
  await timer.callback()
  assert.equal(caller.selfHostRemoval.value.quickstart, undefined)
  assert.equal(caller.selfHostNotice.value, null, 'removal completion does not leave a persistent inline notice')
  assert.equal(caller.toastCalls.length, 1)
  assert.equal(caller.toastCalls[0][0], 'ok')
  assert.match(caller.toastCalls[0][1], /Quickstart was removed from self-hosting/)
  assert.equal(caller.toastCalls[0][2].scope, 'org-a/workspace-a/workspace')
  assert.equal(caller.timers.size, 1, 'the idle Self-Hosting health poll resumes after removal completes')
  assert.equal(caller.timerDelays.values().next().value, 15000)
  caller.stopInventoryPolling()
})

test('ProvidersPage restarts polling for a deleting workspace restored from inventory and stops it on scope exit', async () => {
  const caller = callers[0]
  caller.route.query = { tab: 'self-hosting' }
  caller.setInventoryPollingMounted(true)
  caller.tenantStore.orgUUID = 'org-a'
  caller.tenantStore.workspaceUUID = 'workspace-a'
  caller.orgProviderStore.loaded = true
  const deleting = { name: 'quickstart', deleting: true, ready: false }
  caller.orgProviderStore.items = [deleting]
  caller.orgProviderStore.byName = { quickstart: deleting }
  caller.inventoryScope.value = caller.currentTenantScope.value

  caller.trackDeletingSelfHostedProviders()
  assert.equal(caller.selfHostRemoval.value.quickstart, 'deleting')
  assert.equal(caller.timers.size, 1)

  caller.setScope('org-b', 'workspace-b')
  assert.equal(caller.selfHostRemoval.value.quickstart, undefined)
  assert.equal(caller.timers.size, 0, 'leaving the organization scope must stop stale deletion polls')
})

test('ProvidersPage rechecks role and organization after removal confirmation before issuing DELETE', async () => {
  const caller = callers[0]
  caller.tenantStore.activeOrg = { role: 'admin', catalogEntryCreation: 'admins' }
  caller.tenantStore.orgUUID = 'org-a'
  caller.tenantStore.workspaceUUID = 'workspace-a'
  caller.confirmCalls.length = 0
  let resolveConfirmation
  caller.setConfirmBehavior(() => new Promise(resolve => { resolveConfirmation = resolve }))
  let removes = 0
  caller.orgProviderStore.remove = async () => { removes++ }

  const pending = caller.removeSelfHosted('quickstart')
  await Promise.resolve()
  caller.tenantStore.activeOrg = { role: 'member', catalogEntryCreation: 'admins' }
  resolveConfirmation(true)
  await pending
  assert.equal(removes, 0, 'a role revoked while confirmation is open must block the request')
  assert.equal(caller.selfHostRemoval.value.quickstart, undefined)
})

test('ProvidersPage clears deletion polling when the page unmounts', () => {
  const caller = callers[0]
  caller.route.query = { tab: 'self-hosting' }
  caller.setInventoryPollingMounted(true)
  caller.tenantStore.orgUUID = 'org-a'
  caller.tenantStore.workspaceUUID = 'workspace-a'
  caller.orgProviderStore.loaded = true
  const deleting = { name: 'quickstart', deleting: true, ready: false }
  caller.orgProviderStore.items = [deleting]
  caller.orgProviderStore.byName = { quickstart: deleting }
  caller.inventoryScope.value = caller.currentTenantScope.value
  caller.trackDeletingSelfHostedProviders()
  assert.equal(caller.timers.size, 1)

  caller.unmountCallbacks.forEach(callback => callback())
  assert.equal(caller.selfHostRemoval.value.quickstart, undefined)
  assert.equal(caller.timers.size, 0)
})

test('ProvidersPage pauses workspace-removal polling outside the visible Self-Hosting list and resumes immediately', async () => {
  const caller = await loadCaller('pages/ProvidersPage.vue')
  caller.route.query = { tab: 'self-hosting' }
  caller.setInventoryPollingMounted(true)
  caller.document.addEventListener('visibilitychange', caller.onInventoryVisibilityChange)
  caller.tenantStore.orgUUID = 'org-a'
  caller.tenantStore.workspaceUUID = 'workspace-a'
  const deleting = { name: 'quickstart', deleting: true, ready: false }
  caller.orgProviderStore.loaded = true
  caller.orgProviderStore.items = [deleting]
  caller.orgProviderStore.byName = { quickstart: deleting }
  caller.inventoryScope.value = caller.currentTenantScope.value
  let reads = 0
  caller.orgProviderStore.load = async () => { reads++; return true }

  caller.trackDeletingSelfHostedProviders()
  assert.equal(caller.timers.size, 1)
  assert.equal(caller.timerDelays.values().next().value, 2000)

  caller.setVisibility('hidden')
  assert.equal(caller.timers.size, 0, 'hidden pages retain the operation but clear its timer')
  assert.equal(caller.selfHostRemoval.value.quickstart, 'deleting')

  caller.setVisibility('visible')
  const resumed = caller.takeTimer()
  assert.equal(resumed.delay, 0, 'returning to the visible list resumes with an immediate inventory check')
  await resumed.callback()
  assert.equal(reads, 1)
  assert.equal(caller.selfHostRemoval.value.quickstart, 'deleting')
  assert.equal(caller.timers.size, 1)
  assert.equal(caller.timerDelays.values().next().value, 2000)

  caller.route.query = { tab: 'catalog' }
  caller.restartInventoryPolling()
  assert.equal(caller.timers.size, 0, 'Catalog does not poll an in-progress removal')
  caller.route.query = { tab: 'self-hosting', install: 'kuery' }
  caller.restartInventoryPolling()
  assert.equal(caller.timers.size, 0, 'the install wizard does not poll an in-progress removal')
  caller.route.query = { tab: 'self-hosting' }
  caller.restartInventoryPolling()
  assert.equal(caller.timerDelays.values().next().value, 0, 'returning to the Self-Hosting list resumes immediately')

  caller.stopSelfHostRemovalPolling()
  assert.equal(caller.timers.size, 0)
})

test('ProvidersPage resumes a replacement removal after a stale hidden-page request settles', async () => {
  const caller = await loadCaller('pages/ProvidersPage.vue')
  caller.route.query = { tab: 'self-hosting' }
  caller.setInventoryPollingMounted(true)
  caller.document.addEventListener('visibilitychange', caller.onInventoryVisibilityChange)
  caller.tenantStore.orgUUID = 'org-a'
  caller.tenantStore.workspaceUUID = 'workspace-a'
  const deleting = { name: 'quickstart', deleting: true, ready: false }
  caller.orgProviderStore.loaded = true
  caller.orgProviderStore.items = [deleting]
  caller.orgProviderStore.byName = { quickstart: deleting }
  caller.inventoryScope.value = caller.currentTenantScope.value
  const pending = deferred()
  let reads = 0
  caller.orgProviderStore.load = async () => { reads++; await pending.promise; return true }

  caller.trackDeletingSelfHostedProviders()
  const original = caller.takeTimer()
  const originalRequest = original.callback()
  await Promise.resolve()
  caller.setVisibility('hidden')

  const oldOperation = caller.removalOperations.get('quickstart')
  caller.clearSelfHostRemoval('quickstart', oldOperation)
  caller.orgProviderStore.items = [deleting]
  caller.orgProviderStore.byName = { quickstart: deleting }
  caller.trackDeletingSelfHostedProviders()
  caller.setVisibility('visible')
  assert.equal(caller.timers.size, 0, 'the replacement waits while the old request owns the inventory read')

  pending.resolve()
  await originalRequest
  assert.equal(caller.selfHostRemoval.value.quickstart, 'deleting')
  assert.equal(caller.timers.size, 1, 'the old request settling schedules the current operation')
  assert.equal(caller.timerDelays.values().next().value, 0, 'the current operation checks immediately after the stale read')

  const replacement = caller.takeTimer()
  await replacement.callback()
  assert.equal(reads, 2)
  assert.equal(caller.timerDelays.values().next().value, 2000)
  caller.stopSelfHostRemovalPolling()
})

test('ProvidersPage slows a long-running removal after five successful fast checks', async () => {
  const caller = await loadCaller('pages/ProvidersPage.vue')
  caller.route.query = { tab: 'self-hosting' }
  caller.setInventoryPollingMounted(true)
  caller.orgProviderStore.loaded = true
  const deleting = { name: 'quickstart', deleting: true, ready: false }
  caller.orgProviderStore.items = [deleting]
  caller.orgProviderStore.byName = { quickstart: deleting }
  caller.inventoryScope.value = caller.currentTenantScope.value
  caller.orgProviderStore.load = async () => true

  caller.trackDeletingSelfHostedProviders()
  for (let check = 0; check < 5; check++) {
    const timer = caller.takeTimer()
    assert.equal(timer.delay, 2000, `successful check ${check + 1} uses the fast interval`)
    await timer.callback()
    assert.equal(caller.timerDelays.values().next().value, check === 4 ? 15000 : 2000, 'the poll slows after five successful checks')
  }

  caller.stopSelfHostRemovalPolling()
  assert.equal(caller.timers.size, 0)
})

test('ProvidersPage backs off removal refresh failures and keeps the deleting row visible', async () => {
  const caller = await loadCaller('pages/ProvidersPage.vue')
  caller.route.query = { tab: 'self-hosting' }
  caller.setInventoryPollingMounted(true)
  caller.orgProviderStore.loaded = true
  caller.orgProviderStore.error = null
  const deleting = { name: 'quickstart', deleting: true, ready: false }
  caller.orgProviderStore.items = [deleting]
  caller.orgProviderStore.byName = { quickstart: deleting }
  caller.inventoryScope.value = caller.currentTenantScope.value
  caller.orgProviderStore.load = async () => {
    caller.orgProviderStore.error = 'temporary inventory failure'
    return false
  }

  caller.trackDeletingSelfHostedProviders()
  const expectedDelays = [2000, 4000, 8000, 15000, 15000]
  for (let index = 0; index < expectedDelays.length; index++) {
    const timer = caller.takeTimer()
    assert.equal(timer.delay, expectedDelays[index])
    await timer.callback()
    assert.equal(caller.selfHostRemoval.value.quickstart, 'deleting', 'a failed refresh preserves deletion progress')
    assert.equal(caller.orgProviderStore.byName.quickstart, deleting, 'a failed refresh preserves the last known inventory')
  }

  caller.stopSelfHostRemovalPolling()
  assert.equal(caller.timers.size, 0)
})

test('ProvidersPage blocks install, management, and credential actions while a provider workspace is deleting', async () => {
  const caller = callers[0]
  caller.resetRoute()
  caller.tenantStore.activeOrg = { role: 'admin', catalogEntryCreation: 'admins' }
  caller.orgProviderStore.loaded = true
  caller.orgProviderStore.items = [{ name: 'quickstart', deleting: true, ready: false }]
  caller.orgProviderStore.byName = { quickstart: caller.orgProviderStore.items[0] }
  caller.inventoryScope.value = caller.currentTenantScope.value
  caller.providerStore.loaded = true
  caller.providerStore.selfHostable = [{ name: 'quickstart' }]
  let reads = 0
  caller.orgProviderStore.instructions = async () => { reads++; return { kubeconfig: 'secret' } }

  caller.route.query = { tab: 'self-hosting', install: 'quickstart', manual: '1' }
  caller.normalizeProviderRoute()
  assert.equal(caller.installProvider.value, null)
  assert.equal(caller.route.query.install, undefined)
  assert.equal(caller.route.query.manual, undefined)

  caller.route.query = { tab: 'self-hosting', manage: 'quickstart' }
  caller.normalizeProviderRoute()
  assert.equal(caller.route.query.manage, undefined)
  caller.startInstall({ name: 'quickstart' })
  await caller.showInstructions('quickstart')
  assert.equal(caller.route.query.install, undefined)
  assert.equal(caller.route.query.manage, undefined)
  assert.equal(reads, 0)
})


test('ProvidersPage refreshes idle health only while visible and fences polling after leaving', async () => {
  const caller = await loadCaller('pages/ProvidersPage.vue')
  caller.route.query = { tab: 'self-hosting' }
  caller.setInventoryPollingMounted(true)
  caller.document.addEventListener('visibilitychange', caller.onInventoryVisibilityChange)
  let reads = 0
  const pending = deferred()
  caller.orgProviderStore.load = async () => { reads++; await pending.promise; return true }
  caller.restartInventoryPolling(false)
  assert.equal(caller.timers.size, 1)
  const timer = caller.takeTimer()
  const request = timer.callback()
  assert.equal(reads, 1)
  assert.equal(caller.timers.size, 0, 'wait for the read before scheduling the next poll')
  caller.setVisibility('hidden')
  pending.resolve()
  await request
  assert.equal(caller.timers.size, 0, 'hidden tabs do not restart an in-flight poll')
  caller.orgProviderStore.load = async () => { reads++; return true }
  caller.setVisibility('visible')
  await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
  assert.equal(reads, 2, 'returning to the visible tab refreshes immediately')
  caller.route.query = { tab: 'self-hosting', install: 'kuery' }
  caller.restartInventoryPolling()
  assert.equal(caller.timers.size, 0, 'the wizard owns its own status polling')
  caller.route.query = { tab: 'catalog' }
  caller.restartInventoryPolling()
  assert.equal(reads, 2, 'Catalog does not poll the self-hosting list')
  caller.route.query = { tab: 'self-hosting' }
  caller.restartInventoryPolling(false)
  assert.equal(caller.timers.size, 1)
  for (const unmount of caller.unmountCallbacks) unmount()
  assert.equal(caller.timers.size, 0, 'unmount removes scheduled polling')
})
