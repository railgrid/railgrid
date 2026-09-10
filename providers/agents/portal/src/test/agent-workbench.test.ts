import { describe, expect, it } from 'vitest'
import {
  activateAgentWorkbenchTab,
  closeAgentWorkbenchTab,
  createDefaultAgentWorkbenchState,
  openAgentWorkbenchLauncher,
  openAgentWorkbenchTab,
  reorderAgentWorkbenchTab,
  selectAgentWorkbenchLauncherTab,
} from '../agent-workbench'

function tabIDs(state: ReturnType<typeof createDefaultAgentWorkbenchState>): string[] {
  return state.tabs.map(tab => tab.id)
}

describe('agent workbench lifecycle', () => {
  it('starts with Config and a singleton New tab launcher', () => {
    const state = createDefaultAgentWorkbenchState()

    expect(tabIDs(state)).toEqual(['config', 'launcher'])
    expect(state.activeTabID).toBe('config')
  })

  it('replaces the active launcher for a new surface and removes duplicates for an existing one', () => {
    const withLauncher = openAgentWorkbenchLauncher(createDefaultAgentWorkbenchState())
    const withTools = selectAgentWorkbenchLauncherTab(withLauncher, 'tools')

    expect(tabIDs(withTools)).toEqual(['config', 'tools'])
    expect(withTools.activeTabID).toBe('tools')

    const launcherAgain = openAgentWorkbenchLauncher(withTools)
    const existingConfig = selectAgentWorkbenchLauncherTab(launcherAgain, 'config')
    expect(tabIDs(existingConfig)).toEqual(['config', 'tools'])
    expect(existingConfig.activeTabID).toBe('config')
  })

  it('reopens a closed route surface as a singleton and chooses adjacent tabs on close', () => {
    const withTools = openAgentWorkbenchTab(createDefaultAgentWorkbenchState(), 'tools')
    const closedTools = closeAgentWorkbenchTab(withTools, 'tools')
    expect(tabIDs(closedTools)).toEqual(['config', 'launcher'])
    expect(closedTools.activeTabID).toBe('launcher')

    const reopenedTools = openAgentWorkbenchTab(closedTools, 'tools')
    expect(tabIDs(reopenedTools)).toEqual(['config', 'launcher', 'tools'])
    expect(reopenedTools.activeTabID).toBe('tools')

    const closedLauncher = closeAgentWorkbenchTab(reopenedTools, 'launcher')
    expect(tabIDs(closedLauncher)).toEqual(['config', 'tools'])
    expect(closedLauncher.activeTabID).toBe('tools')
  })

  it('keeps the active tab while closing another surface and supports before/after reorder', () => {
    const open = openAgentWorkbenchTab(openAgentWorkbenchTab(createDefaultAgentWorkbenchState(), 'tools'), 'automation')
    const closedConfig = closeAgentWorkbenchTab(open, 'config')
    expect(tabIDs(closedConfig)).toEqual(['launcher', 'tools', 'automation'])
    expect(closedConfig.activeTabID).toBe('automation')

    const before = reorderAgentWorkbenchTab(closedConfig, 'automation', 'tools', 'before')
    expect(tabIDs(before)).toEqual(['launcher', 'automation', 'tools'])
    expect(before.activeTabID).toBe('automation')

    const after = reorderAgentWorkbenchTab(before, 'automation', 'tools', 'after')
    expect(tabIDs(after)).toEqual(['launcher', 'tools', 'automation'])
  })

  it('activates only tabs that are currently present', () => {
    const state = createDefaultAgentWorkbenchState()
    expect(activateAgentWorkbenchTab(state, 'runs')).toEqual(state)
    expect(activateAgentWorkbenchTab(state, 'launcher').activeTabID).toBe('launcher')
  })
})
