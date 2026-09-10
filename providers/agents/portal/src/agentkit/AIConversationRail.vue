<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIConversationRail owns the collapsible, searchable conversation list and its
  keyboard/focus behavior. Callers provide neutral items and explicitly enable
  the lifecycle actions they support. Storage is namespaced by the caller's
  scope so one tenant, user, or project cannot inherit another's layout state.
-->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
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
  Trash2,
} from 'lucide-vue-next'
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AIConversationItem, AIConversationRailCapabilities, AIConversationRailLabels } from './ai'

const props = withDefaults(defineProps<{
  threads: readonly AIConversationItem[]
  activeThreadID: string
  unreadThreadIDs?: readonly string[]
  pinnedThreadIDs?: readonly string[]
  disabled?: boolean
  busy?: boolean
  loading?: boolean
  selectingThreadID?: string
  actioningThreadID?: string
  capabilities?: AIConversationRailCapabilities
  labels?: AIConversationRailLabels
  /** Caller-owned scope for persisted width and anchored state. */
  storageScope?: string
  /** The host overlay root used for the context menu. */
  overlayTarget?: string
  panelId?: string
  ariaLabel?: string
  deleteLabel?: string
}>(), {
  disabled: false,
  busy: false,
  loading: false,
  selectingThreadID: '',
  actioningThreadID: '',
  unreadThreadIDs: () => [],
  pinnedThreadIDs: () => [],
  capabilities: () => ({}),
  labels: () => ({}),
  storageScope: '',
  overlayTarget: 'body',
  panelId: 'k-ai-conversation-rail',
  ariaLabel: 'Conversation threads',
  deleteLabel: 'Delete conversation',
})

const emit = defineEmits<{
  select: [threadID: string]
  create: []
  archive: [threadID: string]
  togglePin: [threadID: string]
  setUnread: [threadID: string, unread: boolean]
  delete: [threadID: string]
}>()

ensureAgentUIStyles()

const DEFAULT_WIDTH = 224
const MIN_WIDTH = 192
const MAX_WIDTH = 384
const CHAT_MIN_WIDTH = 240
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
const layoutWidth = computed(() => (
  !mobileViewport.value && !mobileOpen.value && anchored.value ? effectiveWidth.value : 0
))
const railStyle = computed(() => ({ '--k-ai-conversation-rail-width': `${effectiveWidth.value}px` }))
const capabilities = computed(() => ({
  create: props.capabilities.create === true,
  pin: props.capabilities.pin === true,
  unread: props.capabilities.unread === true,
  archive: props.capabilities.archive === true,
  delete: props.capabilities.delete === true,
}))
const labels = computed(() => ({
  heading: 'Threads',
  create: 'New thread',
  createDisabled: 'Finish or stop the current run before starting another thread',
  search: 'Search threads',
  loading: 'Loading threads',
  list: 'Assistant threads',
  unread: 'Unread thread',
  updating: 'Updating thread',
  pin: 'Pin thread',
  unpin: 'Unpin thread',
  archive: 'Archive thread',
  pinned: 'Pinned',
  threads: 'Threads',
  pinMenu: 'Pin',
  unpinMenu: 'Unpin',
  markRead: 'Mark read',
  markUnread: 'Mark unread',
  archiveMenu: 'Archive',
  resize: 'Resize conversation panel',
  empty: 'No threads yet.',
  emptySearch: 'No threads match this search.',
  ...props.labels,
}))
const persistenceEnabled = computed(() => Boolean(String(props.storageScope || '').trim()))
const storagePrefix = computed(() => {
  // Encode the complete caller-owned scope so distinct tenant/project/user
  // values cannot collapse onto one preference key after sanitization.
  const scope = String(props.storageScope || 'default').trim() || 'default'
  return `faros:portalkit:ai-conversation-rail:${encodeURIComponent(scope)}`
})
const anchoredStorageKey = computed(() => `${storagePrefix.value}:anchored:v1`)
const widthStorageKey = computed(() => `${storagePrefix.value}:width:v1`)
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
    { id: 'pinned', label: labels.value.pinned, threads: pinned },
    { id: 'threads', label: pinned.length ? labels.value.threads : '', threads: regular },
  ].filter((section) => section.threads.length)
})
const contextMenuThread = computed(() => props.threads.find((thread) => thread.id === contextMenu.value?.threadID))

function displayTitle(thread: AIConversationItem): string {
  return thread.title?.trim() || labels.value.create
}

function clampWidth(value: number, max = MAX_WIDTH): number {
  const fallback = Math.min(max, DEFAULT_WIDTH)
  if (!Number.isFinite(value)) return fallback
  return Math.round(Math.min(max, Math.max(MIN_WIDTH, value)))
}

function readStoredWidth(): number {
  if (!persistenceEnabled.value) return DEFAULT_WIDTH
  try {
    const stored = localStorage.getItem(widthStorageKey.value)
    if (stored === null || !stored.trim()) return DEFAULT_WIDTH
    return clampWidth(Number(stored))
  } catch {
    return DEFAULT_WIDTH
  }
}

function restoreStoredLayout(): void {
  try {
    const stored = persistenceEnabled.value ? localStorage.getItem(anchoredStorageKey.value) : null
    anchored.value = stored === null ? true : stored === '1'
  } catch {
    anchored.value = true
  }
  railWidth.value = readStoredWidth()
}

function persistWidth(): void {
  if (!persistenceEnabled.value) return
  try {
    localStorage.setItem(widthStorageKey.value, String(railWidth.value))
  } catch {
    // Layout persistence is best effort; the current UI state remains valid.
  }
}

function updateAvailableWidthCap(): void {
  const available = root.value?.parentElement?.getBoundingClientRect().width
  if (!Number.isFinite(available) || !available || available <= 0) {
    availableWidthCap.value = MAX_WIDTH
    return
  }
  availableWidthCap.value = Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, Math.floor(available - CHAT_MIN_WIDTH)))
}

function setWidth(value: number, persist = true): void {
  const next = clampWidth(value, availableWidthCap.value)
  if (next === railWidth.value) return
  railWidth.value = next
  if (persist) persistWidth()
}

function clearTimers(): void {
  if (hoverOpenTimer) clearTimeout(hoverOpenTimer)
  if (closeTimer) clearTimeout(closeTimer)
  hoverOpenTimer = undefined
  closeTimer = undefined
}

function clearHoverOpenTimer(): void {
  if (hoverOpenTimer) clearTimeout(hoverOpenTimer)
  hoverOpenTimer = undefined
}

function clearCloseTimer(): void {
  if (closeTimer) clearTimeout(closeTimer)
  closeTimer = undefined
}

function currentFocusedElement(): HTMLElement | null {
  if (typeof document === 'undefined') return null
  return document.activeElement instanceof HTMLElement ? document.activeElement : null
}

function restoreFocus(target: HTMLElement | null): void {
  if (!target) return
  void nextTick(() => {
    if (target.isConnected && !target.hasAttribute('disabled')) target.focus()
  })
}

function open(focusSearch = false, returnFocus?: HTMLElement | null): void {
  clearTimers()
  syncMobileViewport()
  if (mobileViewport.value) {
    if (!mobileOpen.value) mobileReturnFocus.value = returnFocus ?? currentFocusedElement()
    mobileOpen.value = true
  }
  interactionExpanded.value = true
  if (focusSearch) void nextTick(() => searchInput.value?.focus())
}

function close(options: { restoreFocus?: boolean } = {}): void {
  const shouldRestoreFocus = options.restoreFocus ?? true
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

function scheduleHoverOpen(): void {
  clearCloseTimer()
  if (anchored.value || expanded.value || hoverOpenTimer) return
  hoverOpenTimer = setTimeout(() => {
    hoverOpenTimer = undefined
    open()
  }, 180)
}

function scheduleClose(): void {
  clearHoverOpenTimer()
  clearCloseTimer()
  if (contextMenu.value || resizing.value) return
  if (anchored.value && !mobileOpen.value) return
  closeTimer = setTimeout(() => {
    closeTimer = undefined
    if (!root.value?.matches(':focus-within')) close()
  }, 320)
}

function isMobileViewport(): boolean {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia('(max-width: 767px)').matches
}

function syncMobileViewport(): void {
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

function focusThread(threadID: string): void {
  if (!threadID || !visibleExpanded.value) return
  void nextTick(() => {
    const target = Array.from(root.value?.querySelectorAll<HTMLButtonElement>('button[data-thread-id]') ?? [])
      .find((button) => button.dataset.threadId === threadID)
    if (target && !target.disabled) target.focus()
  })
}

function previewEnter(): void {
  if (isMobileViewport()) return
  scheduleHoverOpen()
}

function previewLeave(): void {
  if (isMobileViewport()) return
  scheduleClose()
}

function startResize(event: PointerEvent): void {
  if (isMobileViewport() || !expanded.value || !railPanel.value) return
  event.preventDefault()
  event.stopPropagation()
  clearTimers()
  resizing.value = true
  window.addEventListener('pointermove', resizeFromPointer)
  window.addEventListener('pointerup', stopResize)
  window.addEventListener('pointercancel', stopResize)
}

function resizeFromPointer(event: PointerEvent): void {
  const panel = railPanel.value
  if (!resizing.value || !panel) return
  const rect = panel.getBoundingClientRect()
  if (!Number.isFinite(rect.left) || !Number.isFinite(event.clientX)) return
  setWidth(event.clientX - rect.left, false)
}

function stopResize(): void {
  const wasResizing = resizing.value
  resizing.value = false
  window.removeEventListener('pointermove', resizeFromPointer)
  window.removeEventListener('pointerup', stopResize)
  window.removeEventListener('pointercancel', stopResize)
  if (wasResizing) persistWidth()
  if (wasResizing && !railPanel.value?.matches(':hover')) scheduleClose()
}

function handleResizeKeydown(event: KeyboardEvent): void {
  const delta = event.key === 'ArrowLeft' ? -16 : event.key === 'ArrowRight' ? 16 : 0
  if (!delta || !expanded.value || isMobileViewport()) return
  event.preventDefault()
  clearTimers()
  setWidth(effectiveWidth.value + delta)
}

function handleFocusOut(event: FocusEvent): void {
  const next = event.relatedTarget
  if (next instanceof Node && root.value?.contains(next)) return
  scheduleClose()
}

function toggleAnchored(): void {
  if (mobileOpen.value) {
    close()
    return
  }
  anchored.value = !anchored.value
  interactionExpanded.value = false
  if (persistenceEnabled.value) {
    try {
      localStorage.setItem(anchoredStorageKey.value, anchored.value ? '1' : '0')
    } catch {
      // Layout persistence is best effort; the current UI state remains valid.
    }
  }
}

function togglePanel(returnFocus?: HTMLElement | null): void {
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

function selectThread(threadID: string): void {
  if (props.disabled || props.busy || props.selectingThreadID) return
  emit('select', threadID)
  if (mobileOpen.value) close()
}

function createThread(): void {
  if (!capabilities.value.create || props.disabled || props.busy || props.selectingThreadID) return
  emit('create')
  if (mobileOpen.value) close()
}

function togglePin(threadID: string): void {
  if (!capabilities.value.pin) return
  emit('togglePin', threadID)
  closeContextMenu(true)
}

function toggleUnread(threadID: string): void {
  if (!capabilities.value.unread) return
  if (threadID === props.activeThreadID) {
    closeContextMenu(true)
    return
  }
  emit('setUnread', threadID, !unreadThreadIDSet.value.has(threadID))
  closeContextMenu(true)
}

function archiveThread(threadID: string): void {
  if (!capabilities.value.archive || props.disabled || props.busy || props.actioningThreadID) return
  emit('archive', threadID)
  closeContextMenu(true)
}

function deleteThread(threadID: string): void {
  if (!capabilities.value.delete || props.disabled || props.busy || props.actioningThreadID) return
  emit('delete', threadID)
  closeContextMenu(true)
}

function clampContextMenuPosition(left: number, top: number, menuWidth: number, menuHeight: number): { left: number; top: number } {
  const viewportWidth = typeof window === 'undefined' ? left + menuWidth : window.innerWidth
  const viewportHeight = typeof window === 'undefined' ? top + menuHeight : window.innerHeight
  return {
    left: Math.max(8, Math.min(left, viewportWidth - menuWidth - 8)),
    top: Math.max(8, Math.min(top, viewportHeight - menuHeight - 8)),
  }
}

async function positionAndFocusContextMenu(threadID: string): Promise<void> {
  await nextTick()
  const menu = actionMenu.value
  if (!menu || contextMenu.value?.threadID !== threadID) return
  const rect = menu.getBoundingClientRect()
  contextMenu.value = { threadID, ...clampContextMenuPosition(contextMenu.value.left, contextMenu.value.top, rect.width, rect.height) }
  await nextTick()
  if (contextMenu.value?.threadID !== threadID) return
  menu.querySelector<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')?.focus()
}

function showContextMenu(threadID: string, left: number, top: number, returnFocus: HTMLElement | null = null): void {
  const menuWidth = 192
  contextMenuReturnFocus.value = returnFocus
  contextMenu.value = { threadID, ...clampContextMenuPosition(left, top, menuWidth, 0) }
  void positionAndFocusContextMenu(threadID)
}

function openContextMenu(event: MouseEvent, threadID: string): void {
  if (!capabilities.value.pin && !capabilities.value.unread && !capabilities.value.archive && !capabilities.value.delete) return
  const currentTarget = event.currentTarget
  const returnFocus = currentTarget instanceof HTMLButtonElement
    ? currentTarget
    : currentTarget instanceof HTMLElement
      ? currentTarget.querySelector<HTMLButtonElement>('button')
      : currentFocusedElement()
  showContextMenu(threadID, event.clientX, event.clientY, returnFocus)
}

function handleThreadKeydown(event: KeyboardEvent, threadID: string): void {
  if (!(event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10'))) return
  event.preventDefault()
  const target = event.currentTarget
  if (!(target instanceof HTMLElement)) return
  const rect = target.getBoundingClientRect()
  showContextMenu(threadID, rect.right - 8, rect.top + 8, target)
}

function closeContextMenu(restoreReturnFocus = false): void {
  const returnFocus = contextMenuReturnFocus.value
  contextMenu.value = null
  contextMenuReturnFocus.value = null
  if (restoreReturnFocus) restoreFocus(returnFocus)
}

function dismissContextMenu(): void {
  const wasOpen = Boolean(contextMenu.value)
  closeContextMenu()
  if (wasOpen) scheduleClose()
}

function handleContextMenuFocusOut(event: FocusEvent): void {
  const next = event.relatedTarget
  if (next instanceof Node && actionMenu.value?.contains(next)) return
  dismissContextMenu()
}

function handleContextMenuKeydown(event: KeyboardEvent): void {
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

function handleDocumentPointerDown(event: PointerEvent): void {
  if (!contextMenu.value) return
  const target = event.target
  if (target instanceof Node && actionMenu.value?.contains(target)) return
  dismissContextMenu()
}

function handleWindowResize(): void {
  syncMobileViewport()
  updateAvailableWidthCap()
  closeContextMenu()
}

function handleEscape(): void {
  if (mobileOpen.value || !anchored.value) close()
}

onMounted(() => {
  syncMobileViewport()
  restoreStoredLayout()
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

watch(() => props.storageScope, () => {
  restoreStoredLayout()
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
  close,
  expanded: visibleExpanded,
  layoutWidth,
  panelID: props.panelId,
  toggle: togglePanel,
  focusThread,
  previewEnter,
  previewLeave,
})
</script>

<template>
  <aside
    ref="root"
    :id="panelId"
    class="k-ai-conversation-rail"
    :style="railStyle"
    :class="[
      mobileOpen ? 'k-ai-conversation-rail--mobile-open' : 'k-ai-conversation-rail--desktop',
      !mobileOpen && (anchored ? 'k-ai-conversation-rail--anchored' : 'k-ai-conversation-rail--collapsed'),
      resizing ? 'k-ai-conversation-rail--resizing' : '',
    ]"
    :aria-label="ariaLabel"
    @pointerenter="scheduleHoverOpen"
    @pointerleave="scheduleClose"
    @focusin="open()"
    @focusout="handleFocusOut"
    @keydown.esc.stop="handleEscape"
  >
    <div v-show="!expanded" class="k-ai-conversation-rail__peek" aria-hidden="true" @pointerenter="scheduleHoverOpen" />
    <div
      ref="railPanel"
      class="k-ai-conversation-rail__panel"
      :class="{ 'k-ai-conversation-rail__panel--expanded': expanded, 'k-ai-conversation-rail__panel--overlay': expanded && (!anchored || mobileOpen) }"
      @pointerenter="scheduleHoverOpen"
      @pointerleave="scheduleClose"
    >
      <div v-show="expanded" class="k-ai-conversation-rail__content" :aria-hidden="!expanded">
        <div class="k-ai-conversation-rail__header">
          <MessageSquare class="k-ai-conversation-rail__header-icon" :stroke-width="1.75" aria-hidden="true" />
          <span class="k-ai-conversation-rail__header-title">{{ labels.heading }}</span>
        </div>

        <div class="k-ai-conversation-rail__toolbar">
          <button
            v-if="capabilities.create"
            type="button"
            class="k-ai-conversation-rail__create"
            :disabled="disabled || busy || Boolean(selectingThreadID)"
            :title="disabled ? labels.createDisabled : labels.create"
            @click="createThread"
          >
            <Loader2 v-if="busy" class="k-ai-conversation-rail__button-icon k-ai-conversation-rail__button-icon--spin" :stroke-width="1.75" aria-hidden="true" />
            <Plus v-else class="k-ai-conversation-rail__button-icon" :stroke-width="1.75" aria-hidden="true" />
            {{ labels.create }}
          </button>
          <label class="k-ai-conversation-rail__search">
            <span class="sr-only">{{ labels.search }}</span>
            <Search class="k-ai-conversation-rail__search-icon" :stroke-width="1.75" aria-hidden="true" />
            <input ref="searchInput" v-model="query" type="search" :placeholder="labels.search" />
          </label>
        </div>

        <div class="k-ai-conversation-rail__list" :aria-busy="loading ? 'true' : undefined">
          <div v-if="loading && !threads.length" class="k-ai-conversation-rail__loading" role="status" :aria-label="labels.loading">
            <span class="sr-only">{{ labels.loading }}…</span>
            <span v-for="width in ['80%', '60%', '66%']" :key="width" class="k-ai-conversation-rail__skeleton" :style="{ width }" />
          </div>
          <div v-else-if="filteredThreads.length" class="k-ai-conversation-rail__sections" :aria-label="labels.list">
            <section v-for="section in threadSections" :key="section.id">
              <div v-if="section.label" class="k-ai-conversation-rail__section-label">{{ section.label }}</div>
              <ul :aria-label="section.label || labels.list">
                <li v-for="thread in section.threads" :key="thread.id">
                  <div
                    class="k-ai-conversation-rail__item"
                    :class="activeThreadID === thread.id ? 'is-active' : ''"
                    @contextmenu.prevent="openContextMenu($event, thread.id)"
                  >
                    <button
                      type="button"
                      :data-thread-id="thread.id"
                      class="k-ai-conversation-rail__item-select"
                      :disabled="disabled || busy || Boolean(selectingThreadID)"
                      :aria-current="activeThreadID === thread.id ? 'page' : undefined"
                      :aria-busy="selectingThreadID === thread.id || actioningThreadID === thread.id ? 'true' : undefined"
                      :title="displayTitle(thread)"
                      @click="selectThread(thread.id)"
                      @keydown="handleThreadKeydown($event, thread.id)"
                    >
                      <span v-if="unreadThreadIDSet.has(thread.id)" class="k-ai-conversation-rail__unread" :class="thread.status === 'active' ? 'is-live' : ''" :aria-label="labels.unread" />
                      <span v-else class="k-ai-conversation-rail__unread k-ai-conversation-rail__unread--empty" aria-hidden="true" />
                      <span class="k-ai-conversation-rail__item-title">{{ displayTitle(thread) }}</span>
                      <Loader2 v-if="selectingThreadID === thread.id || actioningThreadID === thread.id" class="k-ai-conversation-rail__item-spinner" :stroke-width="1.75" :aria-label="labels.updating" />
                    </button>
                    <div aria-hidden="true" class="k-ai-conversation-rail__item-fade" :style="{ backgroundImage: activeThreadID === thread.id ? ACTIVE_THREAD_FADE : RESTING_THREAD_FADE }" />
                    <div v-if="capabilities.pin || capabilities.archive || capabilities.delete" class="k-ai-conversation-rail__item-actions" :style="{ backgroundImage: activeThreadID === thread.id ? ACTIVE_THREAD_FADE : HOVER_THREAD_FADE }">
                      <button
                        v-if="capabilities.pin"
                        type="button"
                        class="k-ai-conversation-rail__item-action"
                        :disabled="Boolean(selectingThreadID) || Boolean(actioningThreadID)"
                        :title="pinnedThreadIDSet.has(thread.id) ? labels.unpin : labels.pin"
                        :aria-label="pinnedThreadIDSet.has(thread.id) ? labels.unpin : labels.pin"
                        @click.stop="togglePin(thread.id)"
                      >
                        <PinOff v-if="pinnedThreadIDSet.has(thread.id)" :stroke-width="1.75" aria-hidden="true" />
                        <Pin v-else :stroke-width="1.75" aria-hidden="true" />
                      </button>
                      <button
                        v-if="capabilities.archive"
                        type="button"
                        class="k-ai-conversation-rail__item-action k-ai-conversation-rail__item-action--archive"
                        :disabled="disabled || busy || Boolean(actioningThreadID)"
                        :title="labels.archive"
                        :aria-label="labels.archive"
                        @click.stop="archiveThread(thread.id)"
                      >
                        <Archive :stroke-width="1.75" aria-hidden="true" />
                      </button>
                      <button
                        v-if="capabilities.delete"
                        type="button"
                        class="k-ai-conversation-rail__item-action k-ai-conversation-rail__item-action--archive"
                        :disabled="disabled || busy || Boolean(actioningThreadID)"
                        :title="deleteLabel"
                        :aria-label="deleteLabel"
                        @click.stop="deleteThread(thread.id)"
                      >
                        <Trash2 :stroke-width="1.75" aria-hidden="true" />
                      </button>
                    </div>
                  </div>
                </li>
              </ul>
            </section>
          </div>
          <div v-else class="k-ai-conversation-rail__empty">
            {{ query ? labels.emptySearch : labels.empty }}
          </div>
        </div>
      </div>
      <div
        v-if="expanded && !mobileOpen"
        role="separator"
        aria-orientation="vertical"
        :aria-label="labels.resize"
        tabindex="0"
        class="k-ai-conversation-rail__resize"
        :aria-valuemin="MIN_WIDTH"
        :aria-valuemax="availableWidthCap"
        :aria-valuenow="effectiveWidth"
        @pointerdown="startResize"
        @keydown="handleResizeKeydown"
      />
    </div>
  </aside>

  <Teleport :to="overlayTarget">
    <div
      v-if="contextMenu && contextMenuThread"
      ref="actionMenu"
      role="menu"
      :aria-label="`Actions for ${displayTitle(contextMenuThread)}`"
      class="k-ai-conversation-rail__context-menu"
      :style="{ left: `${contextMenu.left}px`, top: `${contextMenu.top}px` }"
      @focusout="handleContextMenuFocusOut"
      @keydown="handleContextMenuKeydown"
      @keydown.esc.stop.prevent="closeContextMenu(true)"
    >
      <button v-if="capabilities.pin" type="button" role="menuitem" class="k-ai-conversation-rail__menu-item" @click="togglePin(contextMenuThread.id)">
        <PinOff v-if="pinnedThreadIDSet.has(contextMenuThread.id)" :stroke-width="1.75" aria-hidden="true" />
        <Pin v-else :stroke-width="1.75" aria-hidden="true" />
        {{ pinnedThreadIDSet.has(contextMenuThread.id) ? labels.unpinMenu : labels.pinMenu }}
      </button>
      <button v-if="capabilities.unread" type="button" role="menuitem" class="k-ai-conversation-rail__menu-item" :disabled="contextMenuThread.id === activeThreadID" @click="toggleUnread(contextMenuThread.id)">
        <MailOpen v-if="contextMenuThread.id === activeThreadID || unreadThreadIDSet.has(contextMenuThread.id)" :stroke-width="1.75" aria-hidden="true" />
        <Mail v-else :stroke-width="1.75" aria-hidden="true" />
        {{ contextMenuThread.id === activeThreadID || unreadThreadIDSet.has(contextMenuThread.id) ? labels.markRead : labels.markUnread }}
      </button>
      <div v-if="capabilities.archive && (capabilities.pin || capabilities.unread)" class="k-ai-conversation-rail__menu-divider" />
      <button v-if="capabilities.archive" type="button" role="menuitem" class="k-ai-conversation-rail__menu-item k-ai-conversation-rail__menu-item--danger" :disabled="disabled || busy || Boolean(actioningThreadID)" @click="archiveThread(contextMenuThread.id)">
        <Archive :stroke-width="1.75" aria-hidden="true" />
        {{ labels.archiveMenu }}
      </button>
      <div v-if="capabilities.delete && (capabilities.pin || capabilities.unread || capabilities.archive)" class="k-ai-conversation-rail__menu-divider" />
      <button v-if="capabilities.delete" type="button" role="menuitem" class="k-ai-conversation-rail__menu-item k-ai-conversation-rail__menu-item--danger" :disabled="disabled || busy || Boolean(actioningThreadID)" @click="deleteThread(contextMenuThread.id)">
        <Trash2 :stroke-width="1.75" aria-hidden="true" />
        {{ deleteLabel }}
      </button>
    </div>
  </Teleport>
</template>
