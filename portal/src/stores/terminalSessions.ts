import { defineStore } from 'pinia'
import { ref, computed, watch } from 'vue'

export interface TerminalSession {
  id: string
  edgeName: string
  cluster: string
  displayName: string
  createdAt: Date
  isPinned: boolean
}

export type SplitLayout = 'single' | 'vertical' | 'horizontal' | 'grid'

export interface PanelState {
  height: number
  isMinimized: boolean
  isFullscreen: boolean
  splitLayout: SplitLayout
  paneSizes: number[]
}

const STORAGE_KEY = 'kedge-terminal-panel'

const defaultPanelState = (): PanelState => ({
  height: 420,
  isMinimized: false,
  isFullscreen: false,
  splitLayout: 'single',
  paneSizes: [50, 50],
})

function loadPanelState(): PanelState {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return defaultPanelState()
    const parsed = JSON.parse(raw) as Partial<PanelState>
    return { ...defaultPanelState(), ...parsed, isFullscreen: false }
  } catch {
    return defaultPanelState()
  }
}

export const useTerminalSessionsStore = defineStore('terminalSessions', () => {
  const sessions = ref<TerminalSession[]>([])
  const activeSessionId = ref<string | null>(null)
  const isVisible = ref(false)
  const panelState = ref<PanelState>(loadPanelState())

  watch(
    panelState,
    (s) => {
      try {
        localStorage.setItem(STORAGE_KEY, JSON.stringify(s))
      } catch {
        // ignore quota / private-mode errors
      }
    },
    { deep: true },
  )

  const activeSession = computed(() => sessions.value.find((s) => s.id === activeSessionId.value) ?? null)
  const hasActiveSessions = computed(() => sessions.value.length > 0)
  const pinnedSessionsCount = computed(() => sessions.value.filter((s) => s.isPinned).length)

  const visibleSessions = computed(() => {
    if (panelState.value.splitLayout === 'single') {
      return activeSession.value ? [activeSession.value] : []
    }
    return sessions.value.filter((s) => s.isPinned)
  })

  function openSession(params: { edgeName: string; cluster: string; displayName?: string; forceNew?: boolean }) {
    const baseId = `${params.cluster}::${params.edgeName}`

    if (!params.forceNew) {
      const existing = sessions.value.find((s) => s.id === baseId)
      if (existing) {
        activeSessionId.value = existing.id
        isVisible.value = true
        if (panelState.value.isMinimized) panelState.value.isMinimized = false
        return existing.id
      }
    }

    const siblings = sessions.value.filter((s) => s.edgeName === params.edgeName && s.cluster === params.cluster)
    const number = siblings.length + 1
    const sessionId = params.forceNew ? `${baseId}#${Date.now()}` : baseId
    const display = params.displayName ?? params.edgeName
    const displayName = number > 1 ? `${display} #${number}` : display

    sessions.value.push({
      id: sessionId,
      edgeName: params.edgeName,
      cluster: params.cluster,
      displayName,
      createdAt: new Date(),
      isPinned: true,
    })
    activeSessionId.value = sessionId
    isVisible.value = true
    if (panelState.value.isMinimized) panelState.value.isMinimized = false
    return sessionId
  }

  function closeSession(sessionId: string) {
    const i = sessions.value.findIndex((s) => s.id === sessionId)
    if (i === -1) return
    sessions.value.splice(i, 1)
    if (activeSessionId.value === sessionId) {
      if (sessions.value.length > 0) {
        const next = sessions.value[Math.min(i, sessions.value.length - 1)]
        activeSessionId.value = next.id
      } else {
        activeSessionId.value = null
        isVisible.value = false
      }
    }
  }

  function setActiveSession(sessionId: string) {
    if (sessions.value.some((s) => s.id === sessionId)) activeSessionId.value = sessionId
  }

  function closeAllSessions() {
    sessions.value = []
    activeSessionId.value = null
    isVisible.value = false
  }

  function updatePanelState(patch: Partial<PanelState>) {
    panelState.value = { ...panelState.value, ...patch }
  }

  function toggleVisibility() {
    isVisible.value = !isVisible.value
  }

  function toggleMinimize() {
    panelState.value.isMinimized = !panelState.value.isMinimized
    if (panelState.value.isMinimized) panelState.value.isFullscreen = false
  }

  function toggleFullscreen() {
    panelState.value.isFullscreen = !panelState.value.isFullscreen
    if (panelState.value.isFullscreen) panelState.value.isMinimized = false
  }

  function setSplitLayout(layout: SplitLayout) {
    panelState.value.splitLayout = layout
  }

  function cycleSplitLayout() {
    const layouts: SplitLayout[] = ['single', 'vertical', 'horizontal', 'grid']
    const idx = layouts.indexOf(panelState.value.splitLayout)
    panelState.value.splitLayout = layouts[(idx + 1) % layouts.length]
  }

  function toggleSessionPin(sessionId: string) {
    const s = sessions.value.find((x) => x.id === sessionId)
    if (s) s.isPinned = !s.isPinned
  }

  function reorderSessions(fromIndex: number, toIndex: number) {
    if (fromIndex === toIndex) return
    const arr = [...sessions.value]
    const [moved] = arr.splice(fromIndex, 1)
    arr.splice(toIndex, 0, moved)
    sessions.value = arr
  }

  return {
    sessions,
    activeSessionId,
    isVisible,
    panelState,
    activeSession,
    hasActiveSessions,
    visibleSessions,
    pinnedSessionsCount,
    openSession,
    closeSession,
    setActiveSession,
    closeAllSessions,
    updatePanelState,
    toggleVisibility,
    toggleMinimize,
    toggleFullscreen,
    setSplitLayout,
    cycleSplitLayout,
    toggleSessionPin,
    reorderSessions,
  }
})
