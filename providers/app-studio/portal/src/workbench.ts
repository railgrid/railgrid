export type WorkbenchBuiltInTab = 'preview' | 'review' | 'providers' | 'publishing' | 'launcher'
export type WorkbenchTabKind = WorkbenchBuiltInTab | 'provider'

export interface WorkbenchProviderToolRef {
  id: string
  providerName: string
  title: string
  subtitle: string
  path: string
  iconURL?: string
}

export interface WorkbenchTabDescriptor {
  id: string
  kind: WorkbenchTabKind
  title: string
  subtitle?: string
  closeable: boolean
  providerTool?: WorkbenchProviderToolRef
}

export interface WorkbenchState {
  tabs: WorkbenchTabDescriptor[]
  activeTabID: string
}

export type WorkbenchTabDropPlacement = 'before' | 'after'

const builtInTabs: Record<WorkbenchBuiltInTab, WorkbenchTabDescriptor> = {
  preview: {
    id: 'preview',
    kind: 'preview',
    title: 'Preview',
    closeable: true,
  },
  review: {
    id: 'review',
    kind: 'review',
    title: 'Review',
    closeable: true,
  },
  providers: {
    id: 'providers',
    kind: 'providers',
    title: 'Providers',
    closeable: true,
  },
  publishing: {
    id: 'publishing',
    kind: 'publishing',
    title: 'Publish & Promote',
    closeable: true,
  },
  launcher: {
    id: 'launcher',
    kind: 'launcher',
    title: 'New tab',
    closeable: true,
  },
}

export function createDefaultWorkbenchState(): WorkbenchState {
  return {
    tabs: [cloneWorkbenchTab(builtInTabs.preview), cloneWorkbenchTab(builtInTabs.launcher)],
    activeTabID: builtInTabs.launcher.id,
  }
}

export function openWorkbenchBuiltInTab(state: WorkbenchState, kind: WorkbenchBuiltInTab): WorkbenchState {
  const tab = cloneWorkbenchTab(builtInTabs[kind])
  return upsertWorkbenchTab(state, tab, true)
}

export function openWorkbenchProviderTool(state: WorkbenchState, tool: WorkbenchProviderToolRef): WorkbenchState {
  return upsertWorkbenchTab(
    state,
    {
      id: providerWorkbenchTabID(tool),
      kind: 'provider',
      title: tool.title,
      subtitle: tool.subtitle,
      closeable: true,
      providerTool: { ...tool },
    },
    true,
  )
}

export function activateWorkbenchTab(state: WorkbenchState, tabID: string): WorkbenchState {
  if (!state.tabs.some((tab) => tab.id === tabID)) return normalizeWorkbenchState(state)
  return { ...state, activeTabID: tabID }
}

export function closeWorkbenchTab(state: WorkbenchState, tabID: string): WorkbenchState {
  const currentIndex = state.tabs.findIndex((tab) => tab.id === tabID)
  const current = state.tabs[currentIndex]
  if (!current?.closeable) return normalizeWorkbenchState(state)

  const tabs = state.tabs.filter((tab) => tab.id !== tabID)
  if (state.activeTabID !== tabID) return normalizeWorkbenchState({ ...state, tabs })

  const fallback = tabs[Math.max(0, currentIndex - 1)] ?? tabs[tabs.length - 1] ?? builtInTabs.launcher
  return normalizeWorkbenchState({ tabs, activeTabID: fallback.id })
}

export function updateWorkbenchProviderToolPath(state: WorkbenchState, tabID: string, path: string): WorkbenchState {
  return normalizeWorkbenchState({
    ...state,
    tabs: state.tabs.map((tab) => {
      if (tab.id !== tabID || tab.kind !== 'provider' || !tab.providerTool) return tab
      return {
        ...tab,
        providerTool: {
          ...tab.providerTool,
          path,
        },
      }
    }),
  })
}

export function reorderWorkbenchTab(
  state: WorkbenchState,
  draggedTabID: string,
  targetTabID: string,
  placement: WorkbenchTabDropPlacement = 'before',
): WorkbenchState {
  if (draggedTabID === targetTabID) return normalizeWorkbenchState(state)
  const draggedIndex = state.tabs.findIndex((tab) => tab.id === draggedTabID)
  const targetIndex = state.tabs.findIndex((tab) => tab.id === targetTabID)
  if (draggedIndex < 0 || targetIndex < 0) return state

  const tabs = [...state.tabs]
  const [dragged] = tabs.splice(draggedIndex, 1)
  const adjustedTargetIndex = targetIndex > draggedIndex ? targetIndex - 1 : targetIndex
  const insertIndex = placement === 'after' ? adjustedTargetIndex + 1 : adjustedTargetIndex
  tabs.splice(insertIndex, 0, dragged)
  return normalizeWorkbenchState({ ...state, tabs })
}

export function providerWorkbenchTabID(tool: Pick<WorkbenchProviderToolRef, 'id'>): string {
  return `provider:${tool.id}`
}

function upsertWorkbenchTab(state: WorkbenchState, tab: WorkbenchTabDescriptor, activate: boolean): WorkbenchState {
  const tabs = state.tabs.some((item) => item.id === tab.id)
    ? state.tabs.map((item) => (item.id === tab.id ? tab : item))
    : [...state.tabs, tab]
  return normalizeWorkbenchState({
    tabs,
    activeTabID: activate ? tab.id : state.activeTabID,
  })
}

function normalizeWorkbenchState(state: WorkbenchState): WorkbenchState {
  const tabs = state.tabs.length > 0
    ? state.tabs
    : [cloneWorkbenchTab(builtInTabs.launcher)]
  const activeTabID = tabs.some((tab) => tab.id === state.activeTabID)
    ? state.activeTabID
    : tabs[0]?.id ?? builtInTabs.launcher.id
  return { tabs, activeTabID }
}

function cloneWorkbenchTab(tab: WorkbenchTabDescriptor): WorkbenchTabDescriptor {
  return {
    ...tab,
    ...(tab.providerTool ? { providerTool: { ...tab.providerTool } } : {}),
  }
}
