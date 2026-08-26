import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./workbench.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const {
  closeWorkbenchTab,
  createDefaultWorkbenchState,
  isWorkbenchProviderShortcut,
  openWorkbenchBuiltInTab,
  openWorkbenchProviderTool,
  reorderWorkbenchTab,
  selectExistingWorkbenchTabFromLauncher,
  selectWorkbenchLauncherBuiltInTab,
  selectWorkbenchLauncherProviderTool,
} = await import(moduleURL)

const connectionsTool = {
  id: 'code/connections',
  providerName: 'code',
  title: 'Connections',
  subtitle: 'Code',
  path: 'connections',
}

test('keeps Code resource kinds and Infrastructure templates out of direct shortcuts', () => {
  for (const path of ['connections', 'repositories', 'repositorycommits', 'packages']) {
    assert.equal(isWorkbenchProviderShortcut({ providerName: 'code', path }), false, path)
  }
  assert.equal(isWorkbenchProviderShortcut({ providerName: 'infrastructure', path: 'templates' }), false)
  assert.equal(isWorkbenchProviderShortcut({ providerName: 'infrastructure', path: 'instances' }), true)
  assert.equal(isWorkbenchProviderShortcut({ providerName: 'other', path: 'repositories' }), true)
})

test('starts with preview plus an active launcher tab while review stays closed', () => {
  const state = createDefaultWorkbenchState()

  assert.deepEqual(
    state.tabs.map((tab) => ({ id: tab.id, closeable: tab.closeable })),
    [
      { id: 'preview', closeable: true },
      { id: 'launcher', closeable: true },
    ],
  )
  assert.equal(state.activeTabID, 'launcher')
})

test('opens the launcher from the plus nub without duplicating it', () => {
  const closedLauncher = closeWorkbenchTab(createDefaultWorkbenchState(), 'launcher')
  const reopened = openWorkbenchBuiltInTab(closedLauncher, 'launcher')
  const activatedAgain = openWorkbenchBuiltInTab(reopened, 'launcher')

  assert.deepEqual(closedLauncher.tabs.map((tab) => tab.id), ['preview'])
  assert.equal(activatedAgain.activeTabID, 'launcher')
  assert.equal(activatedAgain.tabs.filter((tab) => tab.id === 'launcher').length, 1)
})

test('replaces the active launcher with a selected built-in tab', () => {
  const selected = selectWorkbenchLauncherBuiltInTab(createDefaultWorkbenchState(), 'providers')

  assert.deepEqual(selected.tabs.map((tab) => tab.id), ['preview', 'providers'])
  assert.equal(selected.activeTabID, 'providers')
  assert.equal(selected.tabs.some((tab) => tab.kind === 'launcher'), false)
})

test('replaces the active launcher with a selected provider tool', () => {
  const selected = selectWorkbenchLauncherProviderTool(createDefaultWorkbenchState(), connectionsTool)

  assert.deepEqual(selected.tabs.map((tab) => tab.id), ['preview', 'provider:code/connections'])
  assert.equal(selected.activeTabID, 'provider:code/connections')
  assert.equal(selected.tabs.some((tab) => tab.kind === 'launcher'), false)
})

test('consumes the active launcher when selecting an already-open tab', () => {
  const selected = selectExistingWorkbenchTabFromLauncher(createDefaultWorkbenchState(), 'preview')

  assert.deepEqual(selected.tabs.map((tab) => tab.id), ['preview'])
  assert.equal(selected.activeTabID, 'preview')
})

test('opens a provider tool as a closeable active tab without duplicating it', () => {
  const first = openWorkbenchProviderTool(createDefaultWorkbenchState(), connectionsTool)
  const second = openWorkbenchProviderTool(first, connectionsTool)

  assert.equal(second.activeTabID, 'provider:code/connections')
  assert.equal(second.tabs.filter((tab) => tab.id === 'provider:code/connections').length, 1)
  assert.deepEqual(second.tabs[second.tabs.length - 1], {
    id: 'provider:code/connections',
    kind: 'provider',
    title: 'Connections',
    subtitle: 'Code',
    closeable: true,
    providerTool: connectionsTool,
  })
})

test('closing the active tab activates the nearest remaining tab without forcing providers open', () => {
  const withTool = openWorkbenchProviderTool(createDefaultWorkbenchState(), connectionsTool)
  const withoutTool = closeWorkbenchTab(withTool, 'provider:code/connections')

  assert.equal(withoutTool.activeTabID, 'launcher')
  assert.deepEqual(withoutTool.tabs.map((tab) => tab.id), ['preview', 'launcher'])
})

test('providers is a closeable built-in tab that opens from the launcher catalog', () => {
  const withProviders = openWorkbenchBuiltInTab(createDefaultWorkbenchState(), 'providers')
  const closedProviders = closeWorkbenchTab(withProviders, 'providers')

  assert.deepEqual(withProviders.tabs[withProviders.tabs.length - 1], {
    id: 'providers',
    kind: 'providers',
    title: 'Providers',
    closeable: true,
  })
  assert.equal(withProviders.activeTabID, 'providers')
  assert.equal(closedProviders.tabs.some((tab) => tab.id === 'providers'), false)
  assert.equal(closedProviders.activeTabID, 'launcher')
})

test('opens project settings as a closeable built-in tab', () => {
  const settings = openWorkbenchBuiltInTab(createDefaultWorkbenchState(), 'settings')
  assert.equal(settings.activeTabID, 'settings')
  assert.deepEqual(settings.tabs.find((tab) => tab.id === 'settings'), {
    id: 'settings',
    kind: 'settings',
    title: 'Project Settings',
    subtitle: 'Manage project details and preview access',
    closeable: true,
  })
})

test('opens publishing as a closeable built-in tab', () => {
  const publishing = openWorkbenchBuiltInTab(createDefaultWorkbenchState(), 'publishing')
  assert.equal(publishing.activeTabID, 'publishing')
  assert.deepEqual(publishing.tabs.find((tab) => tab.id === 'publishing'), {
    id: 'publishing',
    kind: 'publishing',
    title: 'Publishing',
    subtitle: 'Deploy and share this app',
    closeable: true,
  })
})

test('opens history as a closeable built-in tab', () => {
  const history = openWorkbenchBuiltInTab(createDefaultWorkbenchState(), 'history')
  assert.equal(history.activeTabID, 'history')
  assert.deepEqual(history.tabs.find((tab) => tab.id === 'history'), {
    id: 'history',
    kind: 'history',
    title: 'History',
    subtitle: 'Restore project files from an earlier Git commit',
    closeable: true,
  })
})

test('review is a closeable built-in tab without being forced open by default', () => {
  const defaultState = createDefaultWorkbenchState()
  const withReview = openWorkbenchBuiltInTab(defaultState, 'review')
  const closedReview = closeWorkbenchTab(withReview, 'review')

  assert.equal(defaultState.tabs.some((tab) => tab.id === 'review'), false)
  assert.deepEqual(withReview.tabs[withReview.tabs.length - 1], {
    id: 'review',
    kind: 'review',
    title: 'Review',
    closeable: true,
  })
  assert.equal(closedReview.tabs.some((tab) => tab.id === 'review'), false)
})

test('reorders tabs by moving the dragged tab before the target tab while preserving active tab', () => {
  const state = openWorkbenchProviderTool(openWorkbenchBuiltInTab(createDefaultWorkbenchState(), 'providers'), connectionsTool)
  const reordered = reorderWorkbenchTab(state, 'provider:code/connections', 'preview')

  assert.deepEqual(reordered.tabs.map((tab) => tab.id), [
    'provider:code/connections',
    'preview',
    'launcher',
    'providers',
  ])
  assert.equal(reordered.activeTabID, 'provider:code/connections')
})

test('reorders tabs by moving the dragged tab after the target tab', () => {
  const state = openWorkbenchProviderTool(openWorkbenchBuiltInTab(createDefaultWorkbenchState(), 'providers'), connectionsTool)
  const reordered = reorderWorkbenchTab(state, 'preview', 'provider:code/connections', 'after')

  assert.deepEqual(reordered.tabs.map((tab) => tab.id), [
    'launcher',
    'providers',
    'provider:code/connections',
    'preview',
  ])
  assert.equal(reordered.activeTabID, 'provider:code/connections')
})

test('does not reorder when the dragged or target tab is missing', () => {
  const state = openWorkbenchBuiltInTab(createDefaultWorkbenchState(), 'providers')

  assert.deepEqual(reorderWorkbenchTab(state, 'missing', 'preview'), state)
  assert.deepEqual(reorderWorkbenchTab(state, 'providers', 'missing'), state)
})
