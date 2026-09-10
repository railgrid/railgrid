// Copyright 2026 The Faros Authors.
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

  // Compile the real caller script with deterministic doubles for Vue and its
  // stores. This keeps the deferred request behavior under test without
  // copying the caller's fencing logic into a second implementation.
  const sourceWithoutImports = script.replace(/^import[\s\S]*?from ['"][^'"]+['"]\n/gm, '')
  const harness = `
const ref = (value) => ({ value })
const computed = (getter) => ({ get value() { return getter() } })
const watch = () => {}
const onMounted = () => {}
const defineEmits = () => () => {}
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
const tenantStore = { orgUUID: 'org-a', workspaceUUID: 'workspace-a', workspaceMode: 'workspace' }
const useProvidersStore = () => providerStore
const useOrgProvidersStore = () => orgProviderStore
const useTenantStore = () => tenantStore
${sourceWithoutImports}
export {
  actionError,
  busy,
  closeEnableDialog,
  dialogProvider,
  dialogRevision,
  onDialogConfirm,
  providerStore,
  tenantStore,
  toastCalls,
}
`
  const { outputText } = ts.transpileModule(harness, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.ESNext,
    },
  })
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
}

const callers = await Promise.all([
  loadCaller('pages/ProvidersPage.vue'),
  loadCaller('components/WelcomeWizard.vue'),
])

for (const [index, caller] of callers.entries()) {
  const label = index === 0 ? 'ProvidersPage' : 'WelcomeWizard'

  test(`${label} reports a dismissed same-scope deferred failure as a toast`, async () => {
    caller.toastCalls.length = 0
    caller.tenantStore.orgUUID = 'org-a'
    caller.tenantStore.workspaceUUID = 'workspace-a'
    const provider = { name: 'edges', displayName: 'Edges', permissionClaims: [] }
    const pending = deferred()
    caller.providerStore.enable = () => pending.promise
    caller.dialogRevision.value += 1
    caller.dialogProvider.value = provider

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
    caller.tenantStore.orgUUID = 'org-a'
    caller.tenantStore.workspaceUUID = 'workspace-a'
    const provider = { name: 'code', displayName: 'Code', permissionClaims: [] }
    const pending = deferred()
    caller.providerStore.enable = () => pending.promise
    caller.dialogRevision.value += 1
    caller.dialogProvider.value = provider

    const request = caller.onDialogConfirm([])
    await Promise.resolve()
    caller.tenantStore.orgUUID = 'org-b'
    pending.reject(new Error('old-scope failure'))
    await request

    assert.equal(caller.actionError.value, null)
    assert.equal(caller.toastCalls.length, 0)
    caller.tenantStore.orgUUID = 'org-a'
    caller.dialogProvider.value = null
  })
}
