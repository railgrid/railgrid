<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  Archive,
  Loader2,
  Mail,
  MailOpen,
  MessageSquare,
  Pin,
  PinOff,
  Plus,
  Search,
} from 'lucide-vue-next'
import type { ProjectAssistantThread } from './types'

const props = withDefaults(defineProps<{
  threads: readonly ProjectAssistantThread[]
  activeThreadID: string
  unreadThreadIDs?: readonly string[]
  pinnedThreadIDs?: readonly string[]
  disabled?: boolean
  busy?: boolean
  loading?: boolean
  selectingThreadID?: string
  actioningThreadID?: string
}>(), {
  disabled: false,
  busy: false,
  loading: false,
  selectingThreadID: '',
  actioningThreadID: '',
  unreadThreadIDs: () => [],
  pinnedThreadIDs: () => [],
})

const emit = defineEmits<{
  select: [threadID: string]
  create: []
  archive: [threadID: string]
  togglePin: [threadID: string]
  setUnread: [threadID: string, unread: boolean]
}>()

const ANCHORED_STORAGE_KEY = 'faros:app-studio:thread-rail-anchored:v1'
const WIDTH_STORAGE_KEY = 'faros:app-studio:thread-rail-width:v1'
const DEFAULT_WIDTH = 224
const MIN_WIDTH = 192
const MAX_WIDTH = 384
const CHAT_MIN_WIDTH = 240
const THREAD_RAIL_PANEL_ID = 'app-studio-thread-rail'
const ACTIVE_THREAD_FADE = [
  'linear-gradient(to left, var(--color-accent-subtle) 0%, var(--color-accent-subtle) 58%, transparent 100%)',
  'linear-gradient(to left, var(--color-surface-raised) 0%, var(--color-surface-raised) 58%, transparent 100%)',
].join(', ')
const RESTING_THREAD_FADE = 'linear-gradient(to left, var(--color-surface-raised) 0%, var(--color-surface-raised) 58%, transparent 100%)'
const HOVER_THREAD_FADE = 'linear-gradient(to left, var(--color-surface-hover) 0%, var(--color-surface-hover) 58%, transparent 100%)'
const root = ref<HTMLElement | null>(null)
const railPanel = ref<HTMLElement | null>(null)
const searchInput = ref<HTMLInputElement | null>(null)
const actionMenu = ref<HTMLElement | null>(null)
const anchored = ref(true)
const interactionExpanded = ref(false)
const mobileOpen = ref(false)
const mobileViewport = ref(false)
const mobileReturnFocus = ref<HTMLElement | null>(null)
const railWidth = ref(DEFAULT_WIDTH)
const availableWidthCap = ref(MAX_WIDTH)
const resizing = ref(false)
const query = ref('')
const contextMenu = ref<{ threadID: string; left: number; top: number } | null>(null)
const contextMenuReturnFocus = ref<HTMLElement | null>(null)
let hoverOpenTimer: ReturnType<typeof setTimeout> | undefined
let closeTimer: ReturnType<typeof setTimeout> | undefined
let railResizeObserver: ResizeObserver | undefined

const expanded = computed(() => anchored.value || interactionExpanded.value)
const visibleExpanded = computed(() => expanded.value && (!mobileViewport.value || mobileOpen.value))
const effectiveWidth = computed(() => Math.min(railWidth.value, availableWidthCap.value))
// The rail is an in-flow flex item only while it is anchored on desktop. A
// collapsed or hover-expanded rail keeps its panel absolute so it can preview
// over the conversation without reserving any conversation width.
const layoutWidth = computed(() => (
  !mobileViewport.value && !mobileOpen.value && anchored.value ? effectiveWidth.value : 0
))
const railStyle = computed(() => ({ '--thread-rail-width': `${effectiveWidth.value}px` }))
const unreadThreadIDSet = computed(() => new Set(
  props.unreadThreadIDs.filter((threadID) => threadID !== props.activeThreadID),
))
const pinnedThreadIDSet = computed(() => new Set(props.pinnedThreadIDs))
const filteredThreads = computed(() => {
  const needle = query.value.trim().toLocaleLowerCase()
  if (!needle) return props.threads
  return props.threads.filter((thread) => displayTitle(thread).toLocaleLowerCase().includes(needle))
})
const threadSections = computed(() => {
  const pinned = filteredThreads.value.filter((thread) => pinnedThreadIDSet.value.has(thread.id))
  const regular = filteredThreads.value.filter((thread) => !pinnedThreadIDSet.value.has(thread.id))
  return [
    { id: 'pinned', label: 'Pinned', threads: pinned },
    { id: 'threads', label: pinned.length ? 'Threads' : '', threads: regular },
  ].filter((section) => section.threads.length)
})
const contextMenuThread = computed(() => props.threads.find((thread) => thread.id === contextMenu.value?.threadID))

function displayTitle(thread: ProjectAssistantThread): string {
  return thread.title?.trim() || 'New thread'
}

function clampWidth(value: number, max = MAX_WIDTH): number {
  const fallback = Math.min(max, DEFAULT_WIDTH)
  if (!Number.isFinite(value)) return fallback
  return Math.round(Math.min(max, Math.max(MIN_WIDTH, value)))
}

function readStoredWidth(): number {
  try {
    const stored = localStorage.getItem(WIDTH_STORAGE_KEY)
    if (stored === null || !stored.trim()) return DEFAULT_WIDTH
    return clampWidth(Number(stored))
  } catch {
    return DEFAULT_WIDTH
  }
}

function persistWidth() {
  try {
    localStorage.setItem(WIDTH_STORAGE_KEY, String(railWidth.value))
  } catch {
    // Width persistence is best effort; the current UI state remains valid.
  }
}

function updateAvailableWidthCap() {
  const available = root.value?.parentElement?.getBoundingClientRect().width
  if (!Number.isFinite(available) || !available || available <= 0) {
    availableWidthCap.value = MAX_WIDTH
    return
  }
  availableWidthCap.value = Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, Math.floor(available - CHAT_MIN_WIDTH)))
}

function setWidth(value: number, persist = true) {
  const next = clampWidth(value, availableWidthCap.value)
  if (next === railWidth.value) return
  railWidth.value = next
  if (persist) persistWidth()
}

function clearTimers() {
  if (hoverOpenTimer) clearTimeout(hoverOpenTimer)
  if (closeTimer) clearTimeout(closeTimer)
  hoverOpenTimer = undefined
  closeTimer = undefined
}

function clearHoverOpenTimer() {
  if (hoverOpenTimer) clearTimeout(hoverOpenTimer)
  hoverOpenTimer = undefined
}

function clearCloseTimer() {
  if (closeTimer) clearTimeout(closeTimer)
  closeTimer = undefined
}

function currentFocusedElement(): HTMLElement | null {
  if (typeof document === 'undefined') return null
  return document.activeElement instanceof HTMLElement ? document.activeElement : null
}

function restoreFocus(target: HTMLElement | null) {
  if (!target) return
  void nextTick(() => {
    if (target.isConnected && !target.hasAttribute('disabled')) target.focus()
  })
}

function open(focusSearch = false, returnFocus?: HTMLElement | null) {
  clearTimers()
  syncMobileViewport()
  if (mobileViewport.value) {
    if (!mobileOpen.value) mobileReturnFocus.value = returnFocus ?? currentFocusedElement()
    mobileOpen.value = true
  }
  interactionExpanded.value = true
  if (focusSearch) void nextTick(() => searchInput.value?.focus())
}

function close(options: { restoreFocus?: boolean } = {}) {
  const shouldRestoreFocus = options.restoreFocus ?? true
  // The context menu belongs to the rail. If the rail is explicitly closed,
  // retire its teleported menu in the same transition instead of leaving a
  // floating action surface behind.
  if (contextMenu.value) closeContextMenu()
  syncMobileViewport()
  if (mobileOpen.value) {
    mobileOpen.value = false
    interactionExpanded.value = false
    query.value = ''
    const returnFocus = mobileReturnFocus.value
    mobileReturnFocus.value = null
    if (shouldRestoreFocus) restoreFocus(returnFocus)
    return
  }
  if (anchored.value) return
  interactionExpanded.value = false
  query.value = ''
}

function scheduleHoverOpen() {
  clearCloseTimer()
  if (anchored.value || expanded.value || hoverOpenTimer) return
  hoverOpenTimer = setTimeout(() => {
    hoverOpenTimer = undefined
    open()
  }, 180)
}

function scheduleClose() {
  clearHoverOpenTimer()
  clearCloseTimer()
  if (contextMenu.value) return
  if (resizing.value) return
  if (anchored.value && !mobileOpen.value) return
  closeTimer = setTimeout(() => {
    closeTimer = undefined
    if (!root.value?.matches(':focus-within')) close()
  }, 320)
}

function isMobileViewport() {
  return typeof window !== 'undefined' && window.matchMedia('(max-width: 767px)').matches
}

function syncMobileViewport() {
  const next = isMobileViewport()
  const crossedToDesktop = mobileViewport.value && !next
  mobileViewport.value = next
  if (!crossedToDesktop) return
  clearHoverOpenTimer()
  clearCloseTimer()
  mobileOpen.value = false
  interactionExpanded.value = false
  mobileReturnFocus.value = null
  query.value = ''
}

function focusThread(threadID: string) {
  if (!threadID || !visibleExpanded.value) return
  void nextTick(() => {
    const target = Array.from(root.value?.querySelectorAll<HTMLButtonElement>('button[data-thread-id]') ?? [])
      .find((button) => button.dataset.threadId === threadID)
    if (target && !target.disabled) target.focus()
  })
}

function previewEnter() {
  if (isMobileViewport()) return
  scheduleHoverOpen()
}

function previewLeave() {
  if (isMobileViewport()) return
  scheduleClose()
}

function startResize(event: PointerEvent) {
  if (isMobileViewport() || !expanded.value || !railPanel.value) return
  event.preventDefault()
  event.stopPropagation()
  clearTimers()
  resizing.value = true
  window.addEventListener('pointermove', resizeFromPointer)
  window.addEventListener('pointerup', stopResize)
  window.addEventListener('pointercancel', stopResize)
}

function resizeFromPointer(event: PointerEvent) {
  const panel = railPanel.value
  if (!resizing.value || !panel) return
  const rect = panel.getBoundingClientRect()
  if (!Number.isFinite(rect.left) || !Number.isFinite(event.clientX)) return
  setWidth(event.clientX - rect.left, false)
}

function stopResize() {
  const wasResizing = resizing.value
  resizing.value = false
  window.removeEventListener('pointermove', resizeFromPointer)
  window.removeEventListener('pointerup', stopResize)
  window.removeEventListener('pointercancel', stopResize)
  if (wasResizing) persistWidth()
  if (wasResizing && !railPanel.value?.matches(':hover')) scheduleClose()
}

function handleResizeKeydown(event: KeyboardEvent) {
  const delta = event.key === 'ArrowLeft' ? -16 : event.key === 'ArrowRight' ? 16 : 0
  if (!delta || !expanded.value || isMobileViewport()) return
  event.preventDefault()
  clearTimers()
  setWidth(effectiveWidth.value + delta)
}

function handleFocusOut(event: FocusEvent) {
  const next = event.relatedTarget
  if (next instanceof Node && root.value?.contains(next)) return
  scheduleClose()
}

function toggleAnchored() {
  if (mobileOpen.value) {
    close()
    return
  }
  anchored.value = !anchored.value
  interactionExpanded.value = false
  try {
    localStorage.setItem(ANCHORED_STORAGE_KEY, anchored.value ? '1' : '0')
  } catch {
    // Layout persistence is best effort; the current UI state remains valid.
  }
}

function togglePanel(returnFocus?: HTMLElement | null) {
  clearTimers()
  if (contextMenu.value) closeContextMenu()
  syncMobileViewport()
  if (mobileViewport.value) {
    if (mobileOpen.value) close()
    else open(false, returnFocus)
    return
  }
  toggleAnchored()
}

function selectThread(threadID: string) {
  if (props.disabled || props.busy || props.selectingThreadID) return
  emit('select', threadID)
  if (mobileOpen.value) close()
}

function createThread() {
  if (props.disabled || props.busy || props.selectingThreadID) return
  emit('create')
  if (mobileOpen.value) close()
}

function togglePin(threadID: string) {
  emit('togglePin', threadID)
  closeContextMenu(true)
}

function toggleUnread(threadID: string) {
  if (threadID === props.activeThreadID) {
    closeContextMenu(true)
    return
  }
  emit('setUnread', threadID, !unreadThreadIDSet.value.has(threadID))
  closeContextMenu(true)
}

function archiveThread(threadID: string) {
  if (props.disabled || props.busy || props.actioningThreadID) return
  emit('archive', threadID)
  closeContextMenu(true)
}

function showContextMenu(threadID: string, left: number, top: number, returnFocus: HTMLElement | null = null) {
  const menuWidth = 192
  const menuHeight = 124
  const viewportWidth = typeof window === 'undefined' ? left + menuWidth : window.innerWidth
  const viewportHeight = typeof window === 'undefined' ? top + menuHeight : window.innerHeight
  contextMenuReturnFocus.value = returnFocus
  contextMenu.value = {
    threadID,
    left: Math.max(8, Math.min(left, viewportWidth - menuWidth - 8)),
    top: Math.max(8, Math.min(top, viewportHeight - menuHeight - 8)),
  }
  void nextTick(() => actionMenu.value?.querySelector<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')?.focus())
}

function openContextMenu(event: MouseEvent, threadID: string) {
  const currentTarget = event.currentTarget
  const returnFocus = currentTarget instanceof HTMLButtonElement
    ? currentTarget
    : currentTarget instanceof HTMLElement
      ? currentTarget.querySelector<HTMLButtonElement>('button')
      : currentFocusedElement()
  showContextMenu(threadID, event.clientX, event.clientY, returnFocus)
}

function handleThreadKeydown(event: KeyboardEvent, threadID: string) {
  if (!(event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10'))) return
  event.preventDefault()
  const target = event.currentTarget
  if (!(target instanceof HTMLElement)) return
  const rect = target.getBoundingClientRect()
  showContextMenu(threadID, rect.right - 8, rect.top + 8, target)
}

function closeContextMenu(restoreReturnFocus = false) {
  const returnFocus = contextMenuReturnFocus.value
  contextMenu.value = null
  contextMenuReturnFocus.value = null
  if (restoreReturnFocus === true) restoreFocus(returnFocus)
}

function dismissContextMenu() {
  const wasOpen = Boolean(contextMenu.value)
  closeContextMenu()
  if (wasOpen) scheduleClose()
}

function handleContextMenuFocusOut(event: FocusEvent) {
  const next = event.relatedTarget
  if (next instanceof Node && actionMenu.value?.contains(next)) return
  dismissContextMenu()
}

function handleContextMenuKeydown(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const menu = actionMenu.value
  if (!menu) return
  const items = Array.from(menu.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)'))
  if (!items.length) return
  const currentIndex = items.indexOf(document.activeElement as HTMLButtonElement)
  let nextIndex = currentIndex
  if (event.key === 'Home') nextIndex = 0
  else if (event.key === 'End') nextIndex = items.length - 1
  else if (currentIndex < 0) nextIndex = event.key === 'ArrowUp' ? items.length - 1 : 0
  else nextIndex = (currentIndex + (event.key === 'ArrowUp' ? -1 : 1) + items.length) % items.length
  event.preventDefault()
  event.stopPropagation()
  items[nextIndex]?.focus()
}

function handleDocumentPointerDown(event: PointerEvent) {
  if (!contextMenu.value) return
  const target = event.target
  if (target instanceof Node && actionMenu.value?.contains(target)) return
  dismissContextMenu()
}

function handleWindowResize() {
  syncMobileViewport()
  updateAvailableWidthCap()
  closeContextMenu()
}

function handleEscape() {
  if (mobileOpen.value || !anchored.value) close()
}

onMounted(() => {
  syncMobileViewport()
  try {
    const stored = localStorage.getItem(ANCHORED_STORAGE_KEY)
    anchored.value = stored === null ? true : stored === '1'
  } catch {
    anchored.value = true
  }
  railWidth.value = readStoredWidth()
  updateAvailableWidthCap()
  if (typeof ResizeObserver !== 'undefined' && root.value?.parentElement) {
    railResizeObserver = new ResizeObserver(updateAvailableWidthCap)
    railResizeObserver.observe(root.value.parentElement)
  }
  document.addEventListener('pointerdown', handleDocumentPointerDown, true)
  window.addEventListener('blur', dismissContextMenu)
  window.addEventListener('resize', handleWindowResize)
  window.addEventListener('scroll', dismissContextMenu, true)
})

onBeforeUnmount(() => {
  stopResize()
  clearTimers()
  railResizeObserver?.disconnect()
  document.removeEventListener('pointerdown', handleDocumentPointerDown, true)
  window.removeEventListener('blur', dismissContextMenu)
  window.removeEventListener('resize', handleWindowResize)
  window.removeEventListener('scroll', dismissContextMenu, true)
})

defineExpose({
  open: () => open(false),
  openAndFocus: () => open(true),
  expanded: visibleExpanded,
  layoutWidth,
  panelID: THREAD_RAIL_PANEL_ID,
  toggle: togglePanel,
  focusThread,
  previewEnter,
  previewLeave,
})
</script>

<template>
  <aside
    ref="root"
    :id="THREAD_RAIL_PANEL_ID"
    class="z-40 h-full shrink-0"
    :style="railStyle"
    :class="[
      mobileOpen ? 'absolute inset-y-0 left-0 block w-64' : 'relative hidden md:block',
      !mobileOpen && (anchored ? 'md:w-[var(--thread-rail-width)]' : 'md:w-0'),
      resizing ? 'transition-none' : 'transition-[width] duration-200',
    ]"
    aria-label="Project conversation threads"
    @pointerenter="scheduleHoverOpen"
    @pointerleave="scheduleClose"
    @focusin="open()"
    @focusout="handleFocusOut"
    @keydown.esc.stop="handleEscape"
  >
    <div
      v-show="!expanded"
      class="absolute inset-y-0 left-0 z-10 w-4 cursor-e-resize"
      aria-hidden="true"
      @pointerenter="scheduleHoverOpen"
    />
    <div
      ref="railPanel"
      class="absolute inset-y-0 left-0 flex overflow-hidden bg-surface-raised"
      :class="[
        expanded ? (mobileOpen ? 'w-64 border-r border-border-subtle' : 'w-[var(--thread-rail-width)] border-r border-border-subtle') : 'w-0',
        expanded && (!anchored || mobileOpen) ? 'shadow-2xl' : '',
        resizing ? 'transition-none' : 'transition-[width,box-shadow] duration-200',
      ]"
      @pointerenter="scheduleHoverOpen"
      @pointerleave="scheduleClose"
    >
      <div v-show="expanded" class="flex min-w-0 flex-1 flex-col" :aria-hidden="!expanded">
        <div class="flex h-14 shrink-0 items-center gap-2 border-b border-border-subtle px-3">
          <MessageSquare class="h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" />
          <span class="min-w-0 flex-1 truncate text-[13px] font-semibold text-text-primary">Threads</span>
        </div>

        <div class="grid shrink-0 gap-2 border-b border-border-subtle p-2.5">
          <button
            type="button"
            class="flex h-8 w-full items-center justify-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-3 text-[12px] font-medium text-accent transition hover:bg-accent/20 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="disabled || busy || Boolean(selectingThreadID)"
            :title="disabled ? 'Finish or stop the current run before starting another thread' : 'New thread'"
            @click="createThread"
          >
            <Loader2 v-if="busy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
            <Plus v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
            New thread
          </button>
          <label class="relative block">
            <span class="sr-only">Search threads</span>
            <Search class="pointer-events-none absolute left-2.5 top-2 h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" />
            <input
              ref="searchInput"
              v-model="query"
              type="search"
              class="h-8 w-full rounded-md border border-border-subtle bg-surface pl-8 pr-2 text-[12px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
              placeholder="Search threads"
            />
          </label>
        </div>

        <div class="min-h-0 flex-1 overflow-y-auto p-1.5" :aria-busy="loading ? 'true' : undefined">
          <div v-if="loading && !threads.length" class="grid gap-1.5 p-1" role="status" aria-label="Loading threads">
            <span class="sr-only">Loading threads…</span>
            <div v-for="width in ['w-4/5', 'w-3/5', 'w-2/3']" :key="width" class="shimmer h-10 rounded-md bg-surface-overlay" :class="width" />
          </div>
          <div v-else-if="filteredThreads.length" class="grid gap-2" aria-label="Assistant threads">
            <section v-for="section in threadSections" :key="section.id">
              <div v-if="section.label" class="px-2 pb-1 pt-1 text-[10px] font-semibold uppercase tracking-wide text-text-muted">
                {{ section.label }}
              </div>
              <ul class="grid gap-0.5" :aria-label="section.label || 'Threads'">
                <li v-for="thread in section.threads" :key="thread.id">
                  <div
                    class="group relative flex h-8 min-w-0 items-center overflow-hidden rounded-md transition"
                    :class="activeThreadID === thread.id
                      ? 'bg-accent-subtle text-accent hover:bg-accent-subtle focus-within:bg-accent-subtle'
                      : 'text-text-secondary hover:bg-surface-hover focus-within:bg-surface-hover'"
                    @contextmenu.prevent="openContextMenu($event, thread.id)"
                  >
                    <button
                      type="button"
                      :data-thread-id="thread.id"
                      class="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-md bg-transparent py-1 pl-2 pr-1 text-left transition hover:bg-transparent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-50"
                      :disabled="disabled || busy || Boolean(selectingThreadID)"
                      :aria-current="activeThreadID === thread.id ? 'page' : undefined"
                      :aria-busy="selectingThreadID === thread.id || actioningThreadID === thread.id ? 'true' : undefined"
                      :title="displayTitle(thread)"
                      @click="selectThread(thread.id)"
                      @keydown="handleThreadKeydown($event, thread.id)"
                    >
                      <span
                        v-if="unreadThreadIDSet.has(thread.id)"
                        class="h-1.5 w-1.5 shrink-0 rounded-full"
                        :class="thread.status === 'active' ? 'bg-success' : 'bg-accent'"
                        aria-label="Unread thread"
                      />
                      <span v-else class="h-1.5 w-1.5 shrink-0" aria-hidden="true" />
                      <span class="min-w-0 flex-1 truncate text-[12px] font-medium">{{ displayTitle(thread) }}</span>
                      <Loader2 v-if="selectingThreadID === thread.id || actioningThreadID === thread.id" class="h-3.5 w-3.5 shrink-0 animate-spin text-accent" :stroke-width="1.75" aria-label="Updating thread" />
                    </button>
                    <div
                      aria-hidden="true"
                      class="pointer-events-none absolute inset-y-0 right-0 z-10 w-8 transition-opacity group-hover:opacity-0 group-focus-within:opacity-0"
                      :style="{ backgroundImage: activeThreadID === thread.id ? ACTIVE_THREAD_FADE : RESTING_THREAD_FADE }"
                    />
                    <div
                      class="pointer-events-none absolute inset-y-0 right-1.5 z-20 flex w-24 items-center gap-0.5 pl-8 pr-1 opacity-0 transition group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:pointer-events-auto group-focus-within:opacity-100"
                      :style="{ backgroundImage: activeThreadID === thread.id ? ACTIVE_THREAD_FADE : HOVER_THREAD_FADE }"
                    >
                      <button
                        type="button"
                        class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition hover:bg-transparent hover:text-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
                        :disabled="Boolean(selectingThreadID) || Boolean(actioningThreadID)"
                        :title="pinnedThreadIDSet.has(thread.id) ? 'Unpin thread' : 'Pin thread'"
                        :aria-label="pinnedThreadIDSet.has(thread.id) ? 'Unpin thread' : 'Pin thread'"
                        @click.stop="togglePin(thread.id)"
                      >
                        <PinOff v-if="pinnedThreadIDSet.has(thread.id)" class="h-3 w-3" :stroke-width="1.75" />
                        <Pin v-else class="h-3 w-3" :stroke-width="1.75" />
                      </button>
                      <button
                        type="button"
                        class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition hover:bg-transparent hover:text-danger focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-danger/40 disabled:cursor-not-allowed disabled:opacity-40"
                        title="Archive thread"
                        aria-label="Archive thread"
                        :disabled="disabled || busy || Boolean(actioningThreadID)"
                        @click.stop="archiveThread(thread.id)"
                      >
                        <Archive class="h-3 w-3" :stroke-width="1.75" />
                      </button>
                    </div>
                  </div>
                </li>
              </ul>
            </section>
          </div>
          <div v-else class="px-3 py-6 text-center text-[12px] leading-5 text-text-muted">
            {{ query ? 'No threads match this search.' : 'No threads yet.' }}
          </div>
        </div>

      </div>
      <div
        v-if="expanded && !mobileOpen"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize thread panel"
        tabindex="0"
        class="absolute inset-y-0 right-0 z-20 hidden w-1.5 cursor-col-resize items-center justify-center bg-transparent transition-colors hover:bg-accent/40 focus-visible:bg-accent/40 focus-visible:outline-none md:flex"
        :aria-valuemin="MIN_WIDTH"
        :aria-valuemax="availableWidthCap"
        :aria-valuenow="effectiveWidth"
        @pointerdown="startResize"
        @keydown="handleResizeKeydown"
      />
    </div>
  </aside>

  <Teleport to="#app-studio-overlay-root">
    <div
      v-if="contextMenu && contextMenuThread"
      ref="actionMenu"
      role="menu"
      :aria-label="`Actions for ${displayTitle(contextMenuThread)}`"
      data-thread-context-menu
      class="fixed z-[200] w-48 rounded-md border border-border-default bg-surface-overlay p-1 shadow-2xl"
      :style="{ left: `${contextMenu.left}px`, top: `${contextMenu.top}px` }"
      @focusout="handleContextMenuFocusOut"
      @keydown="handleContextMenuKeydown"
      @keydown.esc.stop.prevent="closeContextMenu(true)"
    >
      <button
        type="button"
        role="menuitem"
        class="flex h-8 w-full items-center gap-2 rounded-sm px-2 text-left text-[12px] text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus:bg-surface-hover focus:outline-none"
        @click="togglePin(contextMenuThread.id)"
      >
        <PinOff v-if="pinnedThreadIDSet.has(contextMenuThread.id)" class="h-3.5 w-3.5" :stroke-width="1.75" />
        <Pin v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
        {{ pinnedThreadIDSet.has(contextMenuThread.id) ? 'Unpin' : 'Pin' }}
      </button>
      <button
        type="button"
        role="menuitem"
        class="flex h-8 w-full items-center gap-2 rounded-sm px-2 text-left text-[12px] text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus:bg-surface-hover focus:outline-none"
        :disabled="contextMenuThread.id === activeThreadID"
        @click="toggleUnread(contextMenuThread.id)"
      >
        <MailOpen v-if="contextMenuThread.id === activeThreadID || unreadThreadIDSet.has(contextMenuThread.id)" class="h-3.5 w-3.5" :stroke-width="1.75" />
        <Mail v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
        {{ contextMenuThread.id === activeThreadID || unreadThreadIDSet.has(contextMenuThread.id) ? 'Mark read' : 'Mark unread' }}
      </button>
      <div class="my-1 h-px bg-border-subtle" />
      <button
        type="button"
        role="menuitem"
        class="flex h-8 w-full items-center gap-2 rounded-sm px-2 text-left text-[12px] text-danger transition hover:bg-danger-subtle focus:bg-danger-subtle focus:outline-none disabled:cursor-not-allowed disabled:opacity-40"
        :disabled="disabled || busy || Boolean(actioningThreadID)"
        @click="archiveThread(contextMenuThread.id)"
      >
        <Archive class="h-3.5 w-3.5" :stroke-width="1.75" />
        Archive
      </button>
    </div>
  </Teleport>
</template>
