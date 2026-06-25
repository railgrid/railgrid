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
  openWorkbenchBuiltInTab,
  openWorkbenchProviderTool,
  reorderWorkbenchTab,
} = await import(moduleURL)

const connectionsTool = {
  id: 'code/connections',
  providerName: 'code',
  title: 'Connections',
  subtitle: 'Code',
  path: 'connections',
}

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

test('publishing is a closeable built-in tab that opens from the launcher catalog', () => {
  const withPublishing = openWorkbenchBuiltInTab(createDefaultWorkbenchState(), 'publishing')
  const closedPublishing = closeWorkbenchTab(withPublishing, 'publishing')

  assert.deepEqual(withPublishing.tabs[withPublishing.tabs.length - 1], {
    id: 'publishing',
    kind: 'publishing',
    title: 'Publishing',
    closeable: true,
  })
  assert.equal(withPublishing.activeTabID, 'publishing')
  assert.equal(closedPublishing.tabs.some((tab) => tab.id === 'publishing'), false)
  assert.equal(closedPublishing.activeTabID, 'launcher')
})

test('opens publishing once and activates it when requested again', () => {
  const initial = createDefaultWorkbenchState()
  const once = openWorkbenchBuiltInTab(initial, 'publishing')
  const twice = openWorkbenchBuiltInTab(once, 'publishing')

  assert.equal(twice.tabs.filter((tab) => tab.id === 'publishing').length, 1)
  assert.equal(twice.activeTabID, 'publishing')
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
