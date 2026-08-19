export type WorkbenchBuiltInTab = 'preview' | 'code' | 'review' | 'providers' | 'integrations' | 'publishing' | 'history' | 'settings' | 'skills' | 'threads' | 'launcher'
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
  code: {
    id: 'code',
    kind: 'code',
    title: 'Code',
    subtitle: 'Browse the live development workspace files',
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
  integrations: {
    id: 'integrations',
    kind: 'integrations',
    title: 'Integrations',
    closeable: true,
  },
  publishing: {
    id: 'publishing',
    kind: 'publishing',
    title: 'Publishing',
    subtitle: 'Deploy and share this app',
    closeable: true,
  },
  history: {
    id: 'history',
    kind: 'history',
    title: 'History',
    subtitle: 'Restore project files from an earlier Git commit',
    closeable: true,
  },
  settings: {
    id: 'settings',
    kind: 'settings',
    title: 'Project Settings',
    subtitle: 'Manage project details and model configuration',
    closeable: true,
  },
  skills: {
    id: 'skills',
    kind: 'skills',
    title: 'Skills',
    subtitle: 'Browse and manage assistant skills',
    closeable: true,
  },
  threads: {
    id: 'threads',
    kind: 'threads',
    title: 'Threads',
    subtitle: 'Manage assistant conversations for this project',
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

/**
 * Return the canonical descriptor for a built-in tab.
 *
 * Workbench persistence stores the stable kind rather than this presentation
 * metadata. Keeping reconstruction here means restored tabs use the same
 * labels, subtitles, and closeability as tabs opened during the current
 * session.
 */
export function canonicalWorkbenchBuiltInTab(kind: WorkbenchBuiltInTab): WorkbenchTabDescriptor {
  return cloneWorkbenchTab(builtInTabs[kind])
}

/**
 * Build a canonical provider tab from the current provider catalog entry.
 * The provider tool ref is deliberately copied so runtime/provider objects
 * cannot become part of the workbench state or its persistence boundary.
 */
export function canonicalWorkbenchProviderTab(tool: WorkbenchProviderToolRef): WorkbenchTabDescriptor {
  const { id, providerName, title, subtitle, path, iconURL } = tool
  return {
    id: providerWorkbenchTabID({ id }),
    kind: 'provider',
    title,
    subtitle,
    closeable: true,
    providerTool: {
      id,
      providerName,
      title,
      subtitle,
      path,
      ...(iconURL ? { iconURL } : {}),
    },
  }
}

export function openWorkbenchBuiltInTab(state: WorkbenchState, kind: WorkbenchBuiltInTab): WorkbenchState {
  const tab = canonicalWorkbenchBuiltInTab(kind)
  return upsertWorkbenchTab(state, tab, true)
}

export function openWorkbenchProviderTool(state: WorkbenchState, tool: WorkbenchProviderToolRef): WorkbenchState {
  return upsertWorkbenchTab(state, canonicalWorkbenchProviderTab(tool), true)
}

export function selectWorkbenchLauncherBuiltInTab(state: WorkbenchState, kind: WorkbenchBuiltInTab): WorkbenchState {
  return replaceActiveWorkbenchLauncher(state, canonicalWorkbenchBuiltInTab(kind))
}

export function selectWorkbenchLauncherProviderTool(state: WorkbenchState, tool: WorkbenchProviderToolRef): WorkbenchState {
  return replaceActiveWorkbenchLauncher(state, canonicalWorkbenchProviderTab(tool))
}

export function selectExistingWorkbenchTabFromLauncher(state: WorkbenchState, tabID: string): WorkbenchState {
  const tab = state.tabs.find((item) => item.id === tabID)
  if (!tab) return normalizeWorkbenchState(state)
  return replaceActiveWorkbenchLauncher(state, tab)
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

function replaceActiveWorkbenchLauncher(state: WorkbenchState, tab: WorkbenchTabDescriptor): WorkbenchState {
  const launcherIndex = state.tabs.findIndex((item) => item.id === state.activeTabID && item.kind === 'launcher')
  if (launcherIndex < 0) return upsertWorkbenchTab(state, tab, true)

  const existingTargetIndex = state.tabs.findIndex((item) => item.id === tab.id)
  if (existingTargetIndex >= 0 && existingTargetIndex !== launcherIndex) {
    return normalizeWorkbenchState({
      tabs: state.tabs.filter((_, index) => index !== launcherIndex),
      activeTabID: tab.id,
    })
  }

  const tabs = [...state.tabs]
  tabs.splice(launcherIndex, 1, tab)
  return normalizeWorkbenchState({ tabs, activeTabID: tab.id })
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
