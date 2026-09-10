<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useTerminalSessionsStore, type TerminalSession } from '@/stores/terminalSessions'
import { useLayoutInsets } from '@/composables/useLayoutInsets'
import TerminalInstance from './TerminalInstance.vue'
import {
  Pin,
  PinOff,
  X,
  Maximize2,
  Minimize2,
  ChevronDown,
  ChevronUp,
  Square,
  Columns2,
  Rows2,
  Grid2x2,
  TerminalSquare,
} from 'lucide-vue-next'

const store = useTerminalSessionsStore()
const insets = useLayoutInsets()
const tabRefs = new Map<string, HTMLButtonElement>()

// Bridge: provider micro-frontends can't reach this Pinia store from
// inside their isolated Vue apps, so they dispatch a
// "faros-terminal-open" CustomEvent (see e.g.
// providers/kubernetesedges/portal/src/terminal-adapter.ts) which the
// dock catches here and forwards to its real openSession path. Listener
// is window-scoped because the provider's custom element lives in
// light DOM but events from inside its own Vue app bubble through
// document, not through the host element.
interface ProviderTerminalDetail {
  edgeName: string
  cluster: string
  displayName?: string
  forceNew?: boolean
}
function onProviderTerminalOpen(e: Event) {
  const ce = e as CustomEvent<ProviderTerminalDetail>
  if (!ce.detail?.edgeName || !ce.detail?.cluster) return
  store.openSession(ce.detail)
}
onMounted(() => window.addEventListener('faros-terminal-open', onProviderTerminalOpen))
onUnmounted(() => window.removeEventListener('faros-terminal-open', onProviderTerminalOpen))

type InstanceRef = InstanceType<typeof TerminalInstance> | null
const instanceRefs = new Map<string, InstanceRef>()
function setInstanceRef(sessionId: string, el: unknown) {
  if (el) instanceRefs.set(sessionId, el as InstanceRef)
  else instanceRefs.delete(sessionId)
}

const resizeHandleClass = computed(() =>
  store.panelState.splitLayout === 'vertical' ? 'vertical' : 'horizontal',
)

const splitIcon = computed(() => {
  switch (store.panelState.splitLayout) {
    case 'single':
      return Square
    case 'vertical':
      return Columns2
    case 'horizontal':
      return Rows2
    case 'grid':
      return Grid2x2
  }
  return Square
})

const splitTitle = computed(() => {
  switch (store.panelState.splitLayout) {
    case 'single':
      return 'Single view (click for vertical split)'
    case 'vertical':
      return 'Vertical split (click for horizontal split)'
    case 'horizontal':
      return 'Horizontal split (click for grid)'
    case 'grid':
      return 'Grid view (click for single)'
  }
  return 'Toggle split layout'
})

const panelStyle = computed<Record<string, string>>(() => {
  // Honor AppLayout's published insets so the dock never slides under the side/bottom nav.
  const base = { left: insets.left, right: insets.right, bottom: insets.bottom }
  if (store.panelState.isMinimized) return { ...base, height: '44px' }
  if (store.panelState.isFullscreen)
    return { ...base, height: `calc(100vh - 16px - ${insets.bottom})`, top: '16px' }
  return { ...base, height: `${store.panelState.height}px` }
})

const viewportHeight = ref(typeof window === 'undefined' ? 160 : window.innerHeight)
const panelHeightMax = computed(() =>
  Math.max(160, viewportHeight.value - 64),
)

function isSessionVisible(session: TerminalSession): boolean {
  if (store.panelState.splitLayout === 'single') return session.id === store.activeSessionId
  return session.isPinned
}

function getVisibleIndex(session: TerminalSession): number {
  return store.visibleSessions.findIndex((s) => s.id === session.id)
}

function shouldShowResizeHandle(session: TerminalSession): boolean {
  const layout = store.panelState.splitLayout
  if (layout === 'single' || layout === 'grid') return false
  if (!isSessionVisible(session)) return false
  const idx = getVisibleIndex(session)
  return idx >= 0 && idx < store.visibleSessions.length - 1
}

function getSessionStyle(session: TerminalSession): Record<string, string> {
  if (!isSessionVisible(session)) return { display: 'none' }
  const layout = store.panelState.splitLayout
  if (layout === 'single' || layout === 'grid') return {}
  const idx = getVisibleIndex(session)
  if (idx < 0) return {}
  const size = store.panelState.paneSizes[idx] ?? 100 / store.visibleSessions.length
  return layout === 'vertical' ? { width: `${size}%` } : { height: `${size}%` }
}

watch(
  () => store.visibleSessions.length,
  (newCount, oldCount) => {
    const layout = store.panelState.splitLayout
    if (layout === 'single' || layout === 'grid' || newCount === 0) return
    if (newCount === oldCount) return
    const current = store.panelState.paneSizes
    if (newCount > current.length) {
      store.updatePanelState({ paneSizes: Array(newCount).fill(100 / newCount) })
    } else {
      const sliced = current.slice(0, newCount)
      const total = sliced.reduce((s, n) => s + n, 0)
      store.updatePanelState({ paneSizes: sliced.map((n) => (n / total) * 100) })
    }
  },
)

function refocusActive() {
  if (!store.activeSessionId) return
  const inst = instanceRefs.get(store.activeSessionId)
  inst?.focusTerminal?.()
}

function resizeAll() {
  instanceRefs.forEach((inst) => inst?.resize?.())
}

watch(
  // Reordering changes the array order but does not change the active
  // terminal. Watching the IDs here made a keyboard reorder schedule a
  // second focus pass into xterm after the tab had already received focus.
  () => store.activeSessionId,
  () => {
    nextTick(refocusActive)
  },
  { flush: 'post' },
)

watch(
  () => [store.panelState.splitLayout, store.panelState.isMinimized, store.panelState.isFullscreen],
  () => {
    nextTick(resizeAll)
  },
)

// ---- Drag-to-reorder tabs ----------------------------------------------------
const draggedTabIndex = ref<number | null>(null)
const dragOverIndex = ref<number | null>(null)

function handleDragStart(e: DragEvent, index: number) {
  draggedTabIndex.value = index
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', String(index))
  }
}
function handleDragEnd() {
  draggedTabIndex.value = null
  dragOverIndex.value = null
}
function handleDragOver(e: DragEvent, index: number) {
  e.preventDefault()
  if (draggedTabIndex.value !== null && draggedTabIndex.value !== index) {
    dragOverIndex.value = index
  }
}
function handleDragLeave() {
  dragOverIndex.value = null
}
function handleDrop(toIndex: number) {
  if (draggedTabIndex.value !== null && draggedTabIndex.value !== toIndex) {
    store.reorderSessions(draggedTabIndex.value, toIndex)
    nextTick(refocusActive)
  }
  draggedTabIndex.value = null
  dragOverIndex.value = null
}

function setTabRef(sessionID: string, element: Element | null): void {
  if (element instanceof HTMLButtonElement) tabRefs.set(sessionID, element)
  else tabRefs.delete(sessionID)
}

function focusSessionTab(index: number): void {
  const session = store.sessions[index]
  if (!session) return
  store.setActiveSession(session.id)
  nextTick(() => tabRefs.get(session.id)?.focus())
}

function handleTabKeydown(event: KeyboardEvent, index: number): void {
  const count = store.sessions.length
  if (count === 0) return
  if (event.altKey && event.shiftKey && (event.key === 'ArrowLeft' || event.key === 'ArrowRight')) {
    event.preventDefault()
    const direction = event.key === 'ArrowLeft' ? -1 : 1
    const nextIndex = index + direction
    if (nextIndex < 0 || nextIndex >= count) return
    const sessionID = store.sessions[index]?.id
    if (!sessionID) return
    store.reorderSessions(index, nextIndex)
    nextTick(() => tabRefs.get(sessionID)?.focus())
    return
  }
  if (event.key === 'Enter' || event.key === ' ' || event.key === 'Spacebar') {
    event.preventDefault()
    store.setActiveSession(store.sessions[index]?.id ?? '')
    return
  }
  let nextIndex: number | null = null
  if (event.key === 'ArrowRight' || event.key === 'ArrowDown') nextIndex = (index + 1) % count
  else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') nextIndex = (index - 1 + count) % count
  else if (event.key === 'Home') nextIndex = 0
  else if (event.key === 'End') nextIndex = count - 1
  if (nextIndex === null) return
  event.preventDefault()
  focusSessionTab(nextIndex)
}

// ---- Panel resize ------------------------------------------------------------
const resizing = ref(false)
let resizeStartY = 0
let resizeStartHeight = 0

function startResize(e: MouseEvent) {
  resizing.value = true
  resizeStartY = e.clientY
  resizeStartHeight = store.panelState.height
  document.addEventListener('mousemove', onResize)
  document.addEventListener('mouseup', stopResize)
  document.body.style.cursor = 'ns-resize'
  document.body.style.userSelect = 'none'
}

function panelHeightBounds(): { min: number; max: number } {
  return { min: 160, max: Math.max(160, window.innerHeight - 64) }
}

function setPanelHeight(next: number): void {
  const bounds = panelHeightBounds()
  store.updatePanelState({ height: Math.max(bounds.min, Math.min(bounds.max, next)) })
  resizeAll()
}

function handlePanelResizeKeydown(event: KeyboardEvent): void {
  if (store.panelState.isMinimized || store.panelState.isFullscreen) return
  const step = event.shiftKey ? 40 : 20
  if (event.key === 'ArrowUp' || event.key === 'ArrowDown' || event.key === 'Home' || event.key === 'End') {
    event.preventDefault()
    const bounds = panelHeightBounds()
    if (event.key === 'ArrowUp') setPanelHeight(store.panelState.height + step)
    else if (event.key === 'ArrowDown') setPanelHeight(store.panelState.height - step)
    else if (event.key === 'Home') setPanelHeight(bounds.min)
    else setPanelHeight(bounds.max)
  }
}

function onResize(e: MouseEvent) {
  if (!resizing.value) return
  const delta = resizeStartY - e.clientY
  setPanelHeight(resizeStartHeight + delta)
}
function stopResize() {
  resizing.value = false
  document.removeEventListener('mousemove', onResize)
  document.removeEventListener('mouseup', stopResize)
  document.body.style.cursor = ''
  document.body.style.userSelect = ''
  nextTick(refocusActive)
}

// ---- Pane resize -------------------------------------------------------------
const paneResizing = ref(false)
let paneIndex = 0
let paneStartPos = 0
let paneStartSizes: number[] = []

function startPaneResize(e: MouseEvent, idx: number) {
  paneResizing.value = true
  paneIndex = idx
  paneStartPos = store.panelState.splitLayout === 'vertical' ? e.clientX : e.clientY
  paneStartSizes = [...store.panelState.paneSizes]
  document.addEventListener('mousemove', onPaneResize)
  document.addEventListener('mouseup', stopPaneResize)
  document.body.style.cursor = store.panelState.splitLayout === 'vertical' ? 'ew-resize' : 'ns-resize'
  document.body.style.userSelect = 'none'
}
function onPaneResize(e: MouseEvent) {
  if (!paneResizing.value) return
  const isVertical = store.panelState.splitLayout === 'vertical'
  const pos = isVertical ? e.clientX : e.clientY
  const delta = pos - paneStartPos
  adjustPaneSizes(paneIndex, delta, true)
}

function adjustPaneSizes(idx: number, delta: number, isPixelDelta = false): void {
  const isVertical = store.panelState.splitLayout === 'vertical'
  const container = document.querySelector('.terminal-dock .panel-content') as HTMLElement | null
  if (!container) return
  const containerSize = isVertical ? container.clientWidth : container.clientHeight
  if (containerSize === 0) return
  const deltaPct = isPixelDelta ? (delta / containerSize) * 100 : delta
  const sizes = isPixelDelta ? [...paneStartSizes] : [...store.panelState.paneSizes]
  const li = idx
  const ri = idx + 1
  if (ri >= sizes.length) return
  let left = Math.max(10, Math.min(90, sizes[li] + deltaPct))
  let right = Math.max(10, Math.min(90, sizes[ri] - deltaPct))
  const adjust = left + right - (sizes[li] + sizes[ri])
  left -= adjust / 2
  right -= adjust / 2
  sizes[li] = left
  sizes[ri] = right
  store.updatePanelState({ paneSizes: sizes })
  resizeAll()
}

function handlePaneResizeKeydown(event: KeyboardEvent, idx: number): void {
  const isVertical = store.panelState.splitLayout === 'vertical'
  let delta = 0
  if (isVertical && event.key === 'ArrowRight') delta = 5
  else if (isVertical && event.key === 'ArrowLeft') delta = -5
  else if (!isVertical && event.key === 'ArrowDown') delta = 5
  else if (!isVertical && event.key === 'ArrowUp') delta = -5
  else if (event.key === 'PageDown') delta = 10
  else if (event.key === 'PageUp') delta = -10
  if (delta === 0) return
  event.preventDefault()
  adjustPaneSizes(idx, delta)
}
function stopPaneResize() {
  paneResizing.value = false
  document.removeEventListener('mousemove', onPaneResize)
  document.removeEventListener('mouseup', stopPaneResize)
  document.body.style.cursor = ''
  document.body.style.userSelect = ''
  nextTick(refocusActive)
}

// ---- Window resize -----------------------------------------------------------
function onWindowResize() {
  viewportHeight.value = window.innerHeight
  const maxH = Math.max(160, window.innerHeight - 64)
  if (store.panelState.height > maxH) store.updatePanelState({ height: maxH })
  resizeAll()
}

onMounted(() => {
  viewportHeight.value = window.innerHeight
  window.addEventListener('resize', onWindowResize)
})
onUnmounted(() => {
  window.removeEventListener('resize', onWindowResize)
  if (resizing.value) stopResize()
  if (paneResizing.value) stopPaneResize()
})
</script>

<template>
  <div
    v-if="store.isVisible && store.sessions.length > 0"
    class="terminal-dock fixed z-40 flex flex-col border-t border-border-default bg-surface-raised shadow-[0_-8px_24px_-8px_rgba(0,0,0,0.5)]"
    :class="{ 'rounded-t-xl': !store.panelState.isFullscreen }"
    :style="panelStyle"
  >
    <!-- Resize handle (top edge) -->
    <div
      v-if="!store.panelState.isMinimized && !store.panelState.isFullscreen"
      class="absolute -top-1 left-0 right-0 h-2 cursor-ns-resize transition-colors hover:bg-accent/30 focus-visible:bg-accent/30 focus-visible:outline-2 focus-visible:outline-accent focus-visible:outline-offset-2"
      role="separator"
      aria-orientation="horizontal"
      aria-label="Resize terminal panel"
      :aria-valuemin="160"
      :aria-valuemax="panelHeightMax"
      :aria-valuenow="Math.round(store.panelState.height)"
      tabindex="0"
      @mousedown="startResize"
      @keydown="handlePanelResizeKeydown"
    />

    <!-- Header: tabs + controls -->
    <div class="flex min-h-11 shrink-0 items-center justify-between gap-2 border-b border-border-subtle bg-surface-overlay/40 px-2">
      <div class="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto" role="tablist" aria-label="Open terminals">
        <div
          v-for="(session, index) in store.sessions"
          :key="session.id"
          :class="[
            'terminal-tab group flex h-11 max-w-[240px] min-w-[120px] shrink-0 cursor-pointer items-center gap-1.5 rounded-md border px-2.5 text-[11px] transition-all sm:h-8',
            session.id === store.activeSessionId
              ? 'border-accent/40 bg-accent/10 text-text-primary'
              : 'border-border-subtle bg-surface-overlay/50 text-text-secondary hover:border-border hover:bg-surface-hover',
            draggedTabIndex === index ? 'opacity-40' : '',
            dragOverIndex === index ? 'border-l-2 border-l-accent' : '',
            !session.isPinned && store.panelState.splitLayout !== 'single' ? 'opacity-60' : '',
          ]"
          draggable="true"
          @dragstart="handleDragStart($event, index)"
          @dragend="handleDragEnd"
          @dragover="handleDragOver($event, index)"
          @dragleave="handleDragLeave"
          @drop.prevent="handleDrop(index)"
        >
          <button
            :ref="(element) => setTabRef(session.id, element as Element | null)"
            type="button"
            role="tab"
            :aria-selected="session.id === store.activeSessionId"
            aria-controls="terminal-panel"
            :tabindex="session.id === store.activeSessionId ? 0 : -1"
            class="terminal-tab-button flex min-h-11 min-w-0 flex-1 items-center gap-1.5 border-0 bg-transparent p-0 text-left text-inherit outline-none focus-visible:outline-2 focus-visible:outline-accent focus-visible:outline-offset-2 sm:min-h-8"
            @click="store.setActiveSession(session.id)"
            @keydown="handleTabKeydown($event, index)"
          >
            <TerminalSquare
              class="h-3 w-3 shrink-0"
              :class="session.id === store.activeSessionId ? 'text-accent' : 'text-text-muted'"
              :stroke-width="2"
              aria-hidden="true"
            />
            <span class="min-w-0 flex-1 truncate font-mono text-[11px]">{{ session.displayName }}</span>
          </button>
          <button
            v-if="store.sessions.length > 1 && store.panelState.splitLayout !== 'single'"
            type="button"
            class="terminal-tab-action k-btn k-btn--ghost flex h-11 w-11 shrink-0 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition-all hover:bg-surface-hover hover:text-accent sm:h-8 sm:w-8 sm:opacity-0 sm:group-hover:opacity-100 sm:group-focus-within:opacity-100"
            :aria-label="`${session.isPinned ? 'Unpin' : 'Pin'} ${session.displayName} ${session.isPinned ? 'from' : 'in'} split view`"
            :title="`${session.isPinned ? 'Unpin' : 'Pin'} ${session.displayName} ${session.isPinned ? 'from' : 'in'} split view`"
            @click.stop="store.toggleSessionPin(session.id)"
            @dragstart.stop.prevent
          >
            <component :is="session.isPinned ? Pin : PinOff" class="h-2.5 w-2.5" :stroke-width="2" aria-hidden="true" />
          </button>
          <button
            type="button"
            class="terminal-tab-action k-btn k-btn--ghost flex h-11 w-11 shrink-0 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition-all hover:bg-danger-subtle hover:text-danger sm:h-8 sm:w-8 sm:opacity-0 sm:group-hover:opacity-100 sm:group-focus-within:opacity-100"
            :aria-label="`Close ${session.displayName} terminal`"
            :title="`Close ${session.displayName} terminal`"
            @click.stop="store.closeSession(session.id)"
            @dragstart.stop.prevent
          >
            <X class="h-2.5 w-2.5" :stroke-width="2.5" aria-hidden="true" />
          </button>
        </div>
      </div>

      <div class="flex shrink-0 items-center gap-0.5">
        <button
          v-if="store.sessions.length > 1"
          type="button"
          class="k-btn k-btn--ghost flex h-11 w-11 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition-colors hover:bg-surface-hover hover:text-accent sm:h-8 sm:w-8"
          aria-label="Change terminal split layout"
          :title="splitTitle"
          @click="store.cycleSplitLayout()"
        >
          <component :is="splitIcon" class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
        </button>
        <button
          type="button"
          class="k-btn k-btn--ghost flex h-11 w-11 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition-colors hover:bg-surface-hover hover:text-accent sm:h-8 sm:w-8"
          :aria-label="store.panelState.isFullscreen ? 'Exit terminal fullscreen' : 'Open terminal fullscreen'"
          :title="store.panelState.isFullscreen ? 'Exit fullscreen' : 'Fullscreen'"
          @click="store.toggleFullscreen()"
        >
          <component :is="store.panelState.isFullscreen ? Minimize2 : Maximize2" class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
        </button>
        <button
          type="button"
          class="k-btn k-btn--ghost flex h-11 w-11 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition-colors hover:bg-surface-hover hover:text-accent sm:h-8 sm:w-8"
          :aria-label="store.panelState.isMinimized ? 'Restore terminal panel' : 'Minimize terminal panel'"
          :title="store.panelState.isMinimized ? 'Restore' : 'Minimize'"
          @click="store.toggleMinimize()"
        >
          <component :is="store.panelState.isMinimized ? ChevronUp : ChevronDown" class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
        </button>
        <button
          type="button"
          class="k-btn k-btn--ghost flex h-11 w-11 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition-colors hover:bg-danger-subtle hover:text-danger sm:h-8 sm:w-8"
          aria-label="Close all terminals"
          title="Close all"
          @click="store.closeAllSessions()"
        >
          <X class="h-3 w-3" :stroke-width="2" aria-hidden="true" />
        </button>
      </div>
    </div>

    <!-- Content area -->
    <div
      v-if="!store.panelState.isMinimized"
      id="terminal-panel"
      role="tabpanel"
      aria-label="Terminal sessions"
      class="panel-content relative min-h-0 flex-1 overflow-hidden bg-surface"
      :class="`layout-${store.panelState.splitLayout}`"
    >
      <template v-for="session in store.sessions" :key="session.id">
        <TerminalInstance
          :ref="(el) => setInstanceRef(session.id, el)"
          :edge-name="session.edgeName"
          :cluster="session.cluster"
          :is-active="isSessionVisible(session)"
          class="terminal-pane"
          :class="{
            'active-pane': session.id === store.activeSessionId,
            'hidden-pane': !isSessionVisible(session),
          }"
          :style="getSessionStyle(session)"
        />
        <div
          v-if="shouldShowResizeHandle(session)"
          class="pane-resize-handle focus-visible:outline-2 focus-visible:outline-accent focus-visible:outline-offset-2"
          :class="resizeHandleClass"
          role="separator"
          :aria-orientation="store.panelState.splitLayout === 'vertical' ? 'vertical' : 'horizontal'"
          :aria-label="`Resize between ${store.visibleSessions[getVisibleIndex(session)]?.displayName ?? 'terminal'} and ${store.visibleSessions[getVisibleIndex(session) + 1]?.displayName ?? 'terminal'}`"
          :aria-valuemin="10"
          :aria-valuemax="90"
          :aria-valuenow="Math.round(store.panelState.paneSizes[getVisibleIndex(session)] ?? 50)"
          tabindex="0"
          @mousedown="startPaneResize($event, getVisibleIndex(session))"
          @keydown="handlePaneResizeKeydown($event, getVisibleIndex(session))"
        />
      </template>
    </div>
  </div>
</template>

<style scoped>
/*
 * The terminal is always a dark console, even on the light theme. Pin the dark
 * palette as local CSS variables on the dock root so every token-driven child
 * utility (bg-surface-*, text-text-*, border-*) inside resolves dark and the
 * chrome reads as one intentional dark strip. Custom properties inherit, so the
 * whole subtree picks these up.
 */
.terminal-dock {
  --color-surface: #0a0b12;
  --color-surface-raised: #111320;
  --color-surface-overlay: #171927;
  --color-surface-hover: #1e2033;
  --color-border-subtle: rgba(255, 255, 255, 0.07);
  --color-border-default: rgba(255, 255, 255, 0.11);
  --color-accent: #8b6bff;
  --color-accent-hover: #a18aff;
  --color-accent-subtle: rgba(139, 107, 255, 0.14);
  --color-accent-glow: rgba(139, 107, 255, 0.3);
  --color-text-primary: #e9e9f2;
  --color-text-secondary: #8a8ca6;
  --color-text-muted: #5d5f78;
  --color-success: #2fd6a0;
  --color-success-subtle: rgba(47, 214, 160, 0.12);
  --color-warning: #f0a63a;
  --color-warning-subtle: rgba(240, 166, 58, 0.12);
  --color-danger: #ff5d5d;
  --color-danger-subtle: rgba(255, 93, 93, 0.12);
}

.terminal-pane.hidden-pane {
  display: none !important;
}

.panel-content.layout-single .terminal-pane {
  position: absolute;
  inset: 0;
}

.panel-content.layout-vertical {
  display: flex;
  flex-direction: row;
  gap: 0;
}
.panel-content.layout-vertical .terminal-pane {
  flex-shrink: 0;
  min-width: 0;
  position: relative;
}

.panel-content.layout-horizontal {
  display: flex;
  flex-direction: column;
}
.panel-content.layout-horizontal .terminal-pane {
  flex-shrink: 0;
  min-height: 0;
  position: relative;
}

.panel-content.layout-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 1px;
  background: var(--color-border-subtle);
}
.panel-content.layout-grid .terminal-pane {
  min-height: 180px;
  position: relative;
}

.terminal-pane.active-pane {
  outline: 1px solid color-mix(in srgb, var(--color-accent) 50%, transparent);
  outline-offset: -1px;
}

.pane-resize-handle {
  background: transparent;
  z-index: 10;
  position: relative;
  transition: background 0.15s;
}
.pane-resize-handle:hover {
  background: color-mix(in srgb, var(--color-accent) 40%, transparent);
}
.pane-resize-handle.vertical {
  width: 4px;
  cursor: ew-resize;
  flex-shrink: 0;
}
.pane-resize-handle.horizontal {
  height: 4px;
  cursor: ns-resize;
  flex-shrink: 0;
}

/* Tailwind's sm: compact rules also apply on a coarse-pointer tablet. Keep
 * the hybrid target contract authoritative for the tab row and its resource
 * actions, including the actions that are hover-revealed on desktop. */
@media (pointer: coarse), (any-pointer: coarse) {
  .terminal-tab,
  .terminal-tab-button,
  .terminal-tab-action {
    min-height: 44px;
  }

  .terminal-tab {
    height: 44px;
  }

  .terminal-tab-action {
    height: 44px;
    opacity: 1;
    width: 44px;
  }
}
</style>
