import type { AgentTab } from './router'

export type AgentWorkbenchBuiltInTab = Exclude<AgentTab, 'chat'>
export type AgentWorkbenchTabKind = AgentWorkbenchBuiltInTab | 'launcher'

export interface AgentWorkbenchTabDescriptor {
  id: AgentWorkbenchTabKind
  kind: AgentWorkbenchTabKind
  title: string
  subtitle?: string
  closeable: boolean
}

export interface AgentWorkbenchState {
  tabs: AgentWorkbenchTabDescriptor[]
  activeTabID: AgentWorkbenchTabKind
}

export type AgentWorkbenchTabDropPlacement = 'before' | 'after'

const builtInTabs: Record<AgentWorkbenchBuiltInTab, AgentWorkbenchTabDescriptor> = {
  config: {
    id: 'config', kind: 'config', title: 'Config', subtitle: 'Manage this agent and its channels', closeable: true,
  },
  tools: {
    id: 'tools', kind: 'tools', title: 'Tools & toolsets', subtitle: 'Manage tool access and toolsets', closeable: true,
  },
  automation: {
    id: 'automation', kind: 'automation', title: 'Schedules & triggers', subtitle: 'Manage schedules and triggers', closeable: true,
  },
  runs: {
    id: 'runs', kind: 'runs', title: 'Runs', subtitle: 'Review runs, approvals, and execution details', closeable: true,
  },
}

const launcherTab: AgentWorkbenchTabDescriptor = {
  id: 'launcher',
  kind: 'launcher',
  title: 'New tab',
  subtitle: 'Open an Agent workbench surface',
  closeable: true,
}

export function createDefaultAgentWorkbenchState(): AgentWorkbenchState {
  return {
    tabs: [cloneTab(builtInTabs.config), cloneTab(launcherTab)],
    activeTabID: 'config',
  }
}

export function canonicalAgentWorkbenchTab(kind: AgentWorkbenchBuiltInTab): AgentWorkbenchTabDescriptor {
  return cloneTab(builtInTabs[kind])
}

/** Open a route-addressable surface while retaining the New tab launcher. */
export function openAgentWorkbenchTab(state: AgentWorkbenchState, kind: AgentWorkbenchBuiltInTab): AgentWorkbenchState {
  const existing = state.tabs.some(tab => tab.id === kind)
  const tabs = existing
    ? state.tabs
    : [...state.tabs, canonicalAgentWorkbenchTab(kind)]
  return normalizeAgentWorkbenchState({ tabs, activeTabID: kind })
}

export function openAgentWorkbenchLauncher(state: AgentWorkbenchState): AgentWorkbenchState {
  if (state.tabs.some(tab => tab.id === 'launcher')) {
    return normalizeAgentWorkbenchState({ ...state, activeTabID: 'launcher' })
  }
  return normalizeAgentWorkbenchState({
    tabs: [...state.tabs, cloneTab(launcherTab)],
    activeTabID: 'launcher',
  })
}

/** Select an item from the active launcher without creating duplicate tabs. */
export function selectAgentWorkbenchLauncherTab(
  state: AgentWorkbenchState,
  kind: AgentWorkbenchBuiltInTab,
): AgentWorkbenchState {
  const launcherIndex = state.tabs.findIndex(tab => tab.id === 'launcher')
  if (launcherIndex < 0 || state.activeTabID !== 'launcher') {
    return openAgentWorkbenchTab(state, kind)
  }

  const existingIndex = state.tabs.findIndex(tab => tab.id === kind)
  if (existingIndex >= 0) {
    return normalizeAgentWorkbenchState({
      tabs: state.tabs.filter(tab => tab.id !== 'launcher'),
      activeTabID: kind,
    })
  }

  const tabs = [...state.tabs]
  tabs.splice(launcherIndex, 1, canonicalAgentWorkbenchTab(kind))
  return normalizeAgentWorkbenchState({ tabs, activeTabID: kind })
}

export function activateAgentWorkbenchTab(
  state: AgentWorkbenchState,
  tabID: AgentWorkbenchTabKind,
): AgentWorkbenchState {
  if (!state.tabs.some(tab => tab.id === tabID)) return normalizeAgentWorkbenchState(state)
  return normalizeAgentWorkbenchState({ ...state, activeTabID: tabID })
}

export function closeAgentWorkbenchTab(
  state: AgentWorkbenchState,
  tabID: AgentWorkbenchTabKind,
): AgentWorkbenchState {
  const currentIndex = state.tabs.findIndex(tab => tab.id === tabID)
  const current = state.tabs[currentIndex]
  if (!current?.closeable) return normalizeAgentWorkbenchState(state)

  const tabs = state.tabs.filter(tab => tab.id !== tabID)
  if (tabs.length === 0) return createDefaultAgentWorkbenchStateForLauncher()
  if (state.activeTabID !== tabID) return normalizeAgentWorkbenchState({ ...state, tabs })

  const fallback = tabs[Math.max(0, currentIndex - 1)] ?? tabs[tabs.length - 1]
  return normalizeAgentWorkbenchState({ tabs, activeTabID: fallback.id })
}

export function reorderAgentWorkbenchTab(
  state: AgentWorkbenchState,
  draggedTabID: AgentWorkbenchTabKind,
  targetTabID: AgentWorkbenchTabKind,
  placement: AgentWorkbenchTabDropPlacement = 'before',
): AgentWorkbenchState {
  if (draggedTabID === targetTabID) return normalizeAgentWorkbenchState(state)
  const draggedIndex = state.tabs.findIndex(tab => tab.id === draggedTabID)
  const targetIndex = state.tabs.findIndex(tab => tab.id === targetTabID)
  if (draggedIndex < 0 || targetIndex < 0) return state

  const tabs = [...state.tabs]
  const [dragged] = tabs.splice(draggedIndex, 1)
  const adjustedTargetIndex = targetIndex > draggedIndex ? targetIndex - 1 : targetIndex
  const insertIndex = placement === 'after' ? adjustedTargetIndex + 1 : adjustedTargetIndex
  tabs.splice(insertIndex, 0, dragged)
  return normalizeAgentWorkbenchState({ ...state, tabs })
}

function createDefaultAgentWorkbenchStateForLauncher(): AgentWorkbenchState {
  return {
    tabs: [cloneTab(launcherTab)],
    activeTabID: 'launcher',
  }
}

function normalizeAgentWorkbenchState(state: AgentWorkbenchState): AgentWorkbenchState {
  const tabs = state.tabs.length > 0 ? state.tabs : [cloneTab(launcherTab)]
  const activeTabID = tabs.some(tab => tab.id === state.activeTabID)
    ? state.activeTabID
    : tabs[0]?.id ?? 'launcher'
  return { tabs, activeTabID }
}

function cloneTab(tab: AgentWorkbenchTabDescriptor): AgentWorkbenchTabDescriptor {
  return { ...tab }
}
