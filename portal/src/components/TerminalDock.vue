<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useTerminalSessionsStore, type TerminalSession } from '@/stores/terminalSessions'
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
  // Honor AppLayout's CSS variables so the dock never slides under the side/bottom nav.
  const insets = {
    left: 'var(--app-inset-left, 0px)',
    right: 'var(--app-inset-right, 0px)',
    bottom: 'var(--app-inset-bottom, 0px)',
  }
  if (store.panelState.isMinimized) return { ...insets, height: '36px' }
  if (store.panelState.isFullscreen) return { ...insets, height: 'calc(100vh - 16px - var(--app-inset-bottom, 0px))', top: '16px' }
  return { ...insets, height: `${store.panelState.height}px` }
})

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
  () => store.sessions.map((s) => s.id).join(','),
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
function onResize(e: MouseEvent) {
  if (!resizing.value) return
  const delta = resizeStartY - e.clientY
  const next = Math.max(160, Math.min(window.innerHeight - 64, resizeStartHeight + delta))
  store.updatePanelState({ height: next })
  resizeAll()
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
  const container = document.querySelector('.terminal-dock .panel-content') as HTMLElement | null
  if (!container) return
  const containerSize = isVertical ? container.clientWidth : container.clientHeight
  if (containerSize === 0) return
  const deltaPct = (delta / containerSize) * 100
  const sizes = [...paneStartSizes]
  const li = paneIndex
  const ri = paneIndex + 1
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
  const maxH = window.innerHeight - 64
  if (store.panelState.height > maxH) store.updatePanelState({ height: maxH })
  resizeAll()
}

onMounted(() => {
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
    class="terminal-dock fixed z-40 flex flex-col border-t border-border-subtle bg-surface-raised/95 shadow-[0_-8px_24px_-8px_rgba(0,0,0,0.4)] backdrop-blur"
    :class="{ 'rounded-t-2xl': !store.panelState.isFullscreen }"
    :style="panelStyle"
  >
    <!-- Resize handle (top edge) -->
    <div
      v-if="!store.panelState.isMinimized && !store.panelState.isFullscreen"
      class="absolute -top-1 left-0 right-0 h-2 cursor-ns-resize transition-colors hover:bg-accent/30"
      @mousedown="startResize"
    />

    <!-- Header: tabs + controls -->
    <div class="flex h-9 shrink-0 items-center justify-between gap-2 border-b border-border-subtle bg-surface-overlay/40 px-2">
      <div class="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
        <div
          v-for="(session, index) in store.sessions"
          :key="session.id"
          :class="[
            'group flex h-7 max-w-[200px] min-w-[120px] shrink-0 cursor-pointer items-center gap-1.5 rounded-md border px-2.5 text-[11px] transition-all',
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
          @click="store.setActiveSession(session.id)"
        >
          <TerminalSquare
            class="h-3 w-3 shrink-0"
            :class="session.id === store.activeSessionId ? 'text-accent' : 'text-text-muted'"
            :stroke-width="1.75"
          />
          <span class="min-w-0 flex-1 truncate font-mono text-[11px]">{{ session.displayName }}</span>
          <button
            v-if="store.sessions.length > 1 && store.panelState.splitLayout !== 'single'"
            class="flex h-4 w-4 shrink-0 items-center justify-center rounded text-text-muted opacity-0 transition-all hover:bg-surface-hover hover:text-accent group-hover:opacity-100"
            :title="session.isPinned ? 'Unpin from split view' : 'Pin to split view'"
            @click.stop="store.toggleSessionPin(session.id)"
          >
            <component :is="session.isPinned ? Pin : PinOff" class="h-2.5 w-2.5" :stroke-width="2" />
          </button>
          <button
            class="flex h-4 w-4 shrink-0 items-center justify-center rounded text-text-muted opacity-0 transition-all hover:bg-danger-subtle hover:text-danger group-hover:opacity-100"
            title="Close"
            @click.stop="store.closeSession(session.id)"
          >
            <X class="h-2.5 w-2.5" :stroke-width="2.5" />
          </button>
        </div>
      </div>

      <div class="flex shrink-0 items-center gap-0.5">
        <button
          v-if="store.sessions.length > 1"
          class="flex h-6 w-6 items-center justify-center rounded text-text-muted transition-colors hover:bg-surface-hover hover:text-accent"
          :title="splitTitle"
          @click="store.cycleSplitLayout()"
        >
          <component :is="splitIcon" class="h-3 w-3" :stroke-width="1.75" />
        </button>
        <button
          class="flex h-6 w-6 items-center justify-center rounded text-text-muted transition-colors hover:bg-surface-hover hover:text-accent"
          :title="store.panelState.isFullscreen ? 'Exit fullscreen' : 'Fullscreen'"
          @click="store.toggleFullscreen()"
        >
          <component :is="store.panelState.isFullscreen ? Minimize2 : Maximize2" class="h-3 w-3" :stroke-width="1.75" />
        </button>
        <button
          class="flex h-6 w-6 items-center justify-center rounded text-text-muted transition-colors hover:bg-surface-hover hover:text-accent"
          :title="store.panelState.isMinimized ? 'Restore' : 'Minimize'"
          @click="store.toggleMinimize()"
        >
          <component :is="store.panelState.isMinimized ? ChevronUp : ChevronDown" class="h-3 w-3" :stroke-width="1.75" />
        </button>
        <button
          class="flex h-6 w-6 items-center justify-center rounded text-text-muted transition-colors hover:bg-danger-subtle hover:text-danger"
          title="Close all"
          @click="store.closeAllSessions()"
        >
          <X class="h-3 w-3" :stroke-width="2" />
        </button>
      </div>
    </div>

    <!-- Content area -->
    <div
      v-if="!store.panelState.isMinimized"
      class="panel-content relative min-h-0 flex-1 overflow-hidden bg-[#0a0a0f]"
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
          class="pane-resize-handle"
          :class="resizeHandleClass"
          @mousedown="startPaneResize($event, getVisibleIndex(session))"
        />
      </template>
    </div>
  </div>
</template>

<style scoped>
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
  background: rgb(var(--border) / 1);
}
.panel-content.layout-grid .terminal-pane {
  min-height: 180px;
  position: relative;
}

.terminal-pane.active-pane {
  outline: 1px solid rgb(var(--accent) / 0.5);
  outline-offset: -1px;
}

.pane-resize-handle {
  background: transparent;
  z-index: 10;
  position: relative;
  transition: background 0.15s;
}
.pane-resize-handle:hover {
  background: rgb(var(--accent) / 0.4);
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
</style>
