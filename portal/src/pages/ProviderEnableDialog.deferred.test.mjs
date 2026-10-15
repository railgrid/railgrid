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
const computed = (getter) => ({ get value() { return getter() } })
const watchCalls = []
const watch = (source, callback) => watchCalls.push({ source, callback })
const onMounted = () => {}
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
const confirmDialog = async () => false
const categoryIcons = {}
const fallbackCategoryIcon = null
const providerBindingAction = () => null
const providerStore = {
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
  supported: false,
  items: [],
  eligibleInstallTargets: [],
  installTargetsEligible: false,
  installTargetsReason: null,
  installTargetsLoaded: false,
  installTargetsLoading: false,
  installTargetsError: null,
  isSelfHosted: () => false,
  load: () => undefined,
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
export {
  actionError,
  busy,
  openEnableDialog,
  closeEnableDialog,
  dialogProvider,
  dialogScope,
  onDialogConfirm,
  providerStore,
  tenantStore,
  authStore,
  watchCalls,
  toastCalls,
  ${isWizard ? 'props, plan, goal, goalExplicit, step, audience, restore, storageKey, persist, selectAudience, canOpen, setupReady, setupDataCurrent, canSkipSetup, memberJourney, explore, exploring,' : 'onDisable, disableOperations,'}
}
`
  const { outputText } = ts.transpileModule(harness, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.ESNext,
    },
  })
  const caller = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
  return {
    ...caller,
    setScope(orgUUID, workspaceUUID = 'workspace-a') {
      caller.tenantStore.orgUUID = orgUUID
      caller.tenantStore.workspaceUUID = workspaceUUID
      for (const watchCall of caller.watchCalls) {
        if (watchCall.source === caller.dialogScope) watchCall.callback()
      }
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
