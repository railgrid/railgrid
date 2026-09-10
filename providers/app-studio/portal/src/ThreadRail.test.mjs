import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
})
test.after(async () => vite?.close())

const railSource = await readFile(new URL('./agentkit/AIConversationRail.vue', import.meta.url), 'utf8')

async function renderRail(overrides = {}) {
  const { default: AIConversationRail } = await vite.ssrLoadModule('/src/agentkit/AIConversationRail.vue')
  return renderToString(createSSRApp(AIConversationRail, {
    threads: [
      { id: 'thread-1', title: 'Add toast notifications', status: 'active', createdAt: '2026-08-26T00:00:00Z', updatedAt: '2026-08-26T00:00:00Z' },
      { id: 'thread-2', title: 'Fix authentication', status: 'idle', createdAt: '2026-08-25T00:00:00Z', updatedAt: '2026-08-25T00:00:00Z' },
    ],
    activeThreadID: 'thread-1',
    unreadThreadIDs: ['thread-2'],
    pinnedThreadIDs: ['thread-2'],
    capabilities: { create: true, pin: true, unread: true, archive: true },
    panelId: 'app-studio-thread-rail',
    ariaLabel: 'Project conversation threads',
    storageScope: 'project-fixture',
    ...overrides,
  }))
}

test('renders the project-scoped shared rail with lifecycle actions and no management footer', async () => {
  const html = await renderRail()

  assert.match(html, /aria-label="Project conversation threads"/)
  assert.match(html, /id="app-studio-thread-rail"/)
  assert.match(html, /Search threads/)
  assert.match(html, /Add toast notifications/)
  assert.match(html, /Fix authentication/)
  assert.match(html, /aria-current="page"/)
  assert.match(html, /class="is-active k-ai-conversation-rail__item"/)
  assert.match(html, /aria-label="Unread thread"/)
  assert.match(html, /Pinned/)
  assert.match(html, /title="Unpin thread"/)
  assert.match(html, /aria-label="Archive thread"/)
  assert.match(html, /class="k-ai-conversation-rail__create"/)
  assert.doesNotMatch(html, /Manage threads|Running|Archived/)
  assert.doesNotMatch(html, /border-t border-border-subtle p-2/)
})

test('exposes shared anchored, mobile, focus, and project-scoped persistence behavior', () => {
  assert.match(railSource, /@pointerenter="scheduleHoverOpen"/)
  assert.match(railSource, /@pointerleave="scheduleClose"/)
  assert.match(railSource, /@focusin="open\(\)"/)
  assert.match(railSource, /@focusout="handleFocusOut"/)
  assert.match(railSource, /@keydown\.esc\.stop="handleEscape"/)
  assert.match(railSource, /const anchored = ref\(true\)/)
  assert.match(railSource, /const persistenceEnabled = computed\(\(\) => Boolean\(String\(props\.storageScope \|\| ''\)\.trim\(\)\)\)/)
  assert.match(railSource, /const anchoredStorageKey = computed\(\(\) => `\$\{storagePrefix\.value\}:anchored:v1`\)/)
  assert.match(railSource, /const widthStorageKey = computed\(\(\) => `\$\{storagePrefix\.value\}:width:v1`\)/)
  assert.match(railSource, /stored === null \? true : stored === '1'/)
  assert.match(railSource, /function togglePanel\(returnFocus\?: HTMLElement \| null\)/)
  assert.match(railSource, /else open\(false, returnFocus\)[\s\S]*toggleAnchored\(\)/)
  assert.match(railSource, /mobileOpen \? 'k-ai-conversation-rail--mobile-open' : 'k-ai-conversation-rail--desktop'/)
  assert.match(railSource, /!mobileOpen && \(anchored \? 'k-ai-conversation-rail--anchored' : 'k-ai-conversation-rail--collapsed'\)/)
  assert.match(railSource, /const visibleExpanded = computed\(\(\) => expanded\.value && \(!mobileViewport\.value \|\| mobileOpen\.value\)\)/)
  assert.match(railSource, /const layoutWidth = computed\(\(\) => \([\s\S]*!mobileViewport\.value[\s\S]*!mobileOpen\.value[\s\S]*anchored\.value[\s\S]*effectiveWidth\.value[\s\S]*: 0\n\)\)/)
  assert.match(railSource, /v-show="!expanded" class="k-ai-conversation-rail__peek"/)
  assert.match(railSource, /defineExpose\(\{[\s\S]*openAndFocus[\s\S]*expanded[\s\S]*layoutWidth[\s\S]*panelID: props\.panelId[\s\S]*toggle: togglePanel[\s\S]*previewEnter[\s\S]*previewLeave/)
  assert.match(railSource, /const mobileViewport = ref\(false\)/)
  assert.match(railSource, /const mobileReturnFocus = ref<HTMLElement \| null>\(null\)/)
  assert.match(railSource, /const crossedToDesktop = mobileViewport\.value && !next/)
  assert.match(railSource, /mobileOpen\.value = false[\s\S]*interactionExpanded\.value = false[\s\S]*mobileReturnFocus\.value = null[\s\S]*query\.value = ''/)
  assert.match(railSource, /if \(mobileViewport\.value\) \{[\s\S]*mobileOpen\.value = true/)
  assert.match(railSource, /if \(!mobileOpen\.value\) mobileReturnFocus\.value = returnFocus \?\? currentFocusedElement\(\)/)
  assert.match(railSource, /const returnFocus = mobileReturnFocus\.value[\s\S]*restoreFocus\(returnFocus\)/)
})

test('keeps preview timers stable and closes mobile or collapsed rails through Escape', () => {
  assert.match(railSource, /function scheduleHoverOpen\(\): void \{\n  clearCloseTimer\(\)/)
  assert.match(railSource, /if \(anchored\.value \|\| expanded\.value \|\| hoverOpenTimer\) return/)
  assert.match(railSource, /function scheduleClose\(\): void \{\n  clearHoverOpenTimer\(\)\n  clearCloseTimer\(\)/)
  assert.match(railSource, /if \(contextMenu\.value \|\| resizing\.value\) return/)
  assert.match(railSource, /function close\(options: \{ restoreFocus\?: boolean \} = \{\}\): void[\s\S]*if \(contextMenu\.value\) closeContextMenu\(\)/)
  assert.match(railSource, /function togglePanel\(returnFocus\?: HTMLElement \| null\): void \{\n  clearTimers\(\)/)
  assert.match(railSource, /function togglePanel\(returnFocus\?: HTMLElement \| null\): void \{[\s\S]*if \(contextMenu\.value\) closeContextMenu\(\)/)
  assert.match(railSource, /function previewEnter\(\): void \{[\s\S]*scheduleHoverOpen\(\)/)
  assert.match(railSource, /function previewLeave\(\): void \{[\s\S]*scheduleClose\(\)/)
  assert.match(railSource, /function isMobileViewport\(\): boolean[\s\S]*max-width: 767px/)
  assert.match(railSource, /function handleEscape\(\): void \{[\s\S]*if \(mobileOpen\.value \|\| !anchored\.value\) close\(\)/)
})

test('keeps shared rail width bounded, persisted, and keyboard/pointer resizable', () => {
  assert.match(railSource, /const DEFAULT_WIDTH = 224/)
  assert.match(railSource, /const MIN_WIDTH = 192/)
  assert.match(railSource, /const MAX_WIDTH = 384/)
  assert.match(railSource, /const CHAT_MIN_WIDTH = 240/)
  assert.match(railSource, /return Math\.round\(Math\.min\(max, Math\.max\(MIN_WIDTH, value\)\)\)/)
  assert.match(railSource, /railWidth\.value = readStoredWidth\(\)/)
  assert.match(railSource, /function persistWidth\(\): void \{[\s\S]*if \(!persistenceEnabled\.value\) return[\s\S]*localStorage\.setItem\(widthStorageKey\.value/)
  assert.match(railSource, /availableWidthCap\.value = Math\.max\(MIN_WIDTH, Math\.min\(MAX_WIDTH, Math\.floor\(available - CHAT_MIN_WIDTH\)\)\)/)
  assert.match(railSource, /ref="railPanel"/)
  assert.match(railSource, /v-if="expanded && !mobileOpen"[\s\S]*role="separator"/)
  assert.match(railSource, /:aria-valuemin="MIN_WIDTH"[\s\S]*:aria-valuemax="availableWidthCap"[\s\S]*:aria-valuenow="effectiveWidth"/)
  assert.match(railSource, /@pointerdown="startResize"/)
  assert.match(railSource, /@keydown="handleResizeKeydown"/)
  assert.match(railSource, /window\.addEventListener\('pointermove', resizeFromPointer\)/)
  assert.match(railSource, /window\.removeEventListener\('pointercancel', stopResize\)/)
  assert.match(railSource, /function handleWindowResize\(\): void \{[\s\S]*updateAvailableWidthCap\(\)[\s\S]*closeContextMenu\(\)/)
  assert.match(railSource, /window\.addEventListener\('resize', handleWindowResize\)/)
  assert.match(railSource, /window\.removeEventListener\('resize', handleWindowResize\)/)
  assert.match(railSource, /resizing \? 'k-ai-conversation-rail--resizing' : ''/)
  assert.match(railSource, /event\.key === 'ArrowLeft' \? -16 : event\.key === 'ArrowRight' \? 16 : 0/)
  assert.match(railSource, /setWidth\(event\.clientX - rect\.left, false\)/)
  assert.match(railSource, /if \(wasResizing\) persistWidth\(\)/)
})

test('filters titles, owns lifecycle events, and gates every optional capability', () => {
  assert.match(railSource, /const capabilities = computed\(\(\) => \(\{[\s\S]*create: props\.capabilities\.create === true[\s\S]*pin: props\.capabilities\.pin === true[\s\S]*unread: props\.capabilities\.unread === true[\s\S]*archive: props\.capabilities\.archive === true[\s\S]*delete: props\.capabilities\.delete === true/)
  assert.match(railSource, /const filteredThreads = computed/)
  assert.match(railSource, /displayTitle\(thread\)\.toLocaleLowerCase\(\)\.includes\(needle\)/)
  assert.match(railSource, /emit\('select', threadID\)/)
  assert.match(railSource, /emit\('create'\)/)
  assert.match(railSource, /emit\('archive', threadID\)/)
  assert.match(railSource, /emit\('delete', threadID\)/)
  assert.match(railSource, /emit\('togglePin', threadID\)/)
  assert.match(railSource, /emit\('setUnread', threadID, !unreadThreadIDSet\.value\.has\(threadID\)\)/)
  assert.match(railSource, /v-if="capabilities\.create"/)
  assert.match(railSource, /v-if="capabilities\.pin"/)
  assert.match(railSource, /v-if="capabilities\.archive"/)
  assert.match(railSource, /v-if="capabilities\.delete"/)
  assert.match(railSource, /const unreadThreadIDSet = computed\(\(\) => new Set\([\s\S]*props\.unreadThreadIDs\.filter\(\(threadID\) => threadID !== props\.activeThreadID\)/)
  assert.doesNotMatch(railSource, /window\.confirm|window\.alert|emit\('manage'/)
})

test('never presents the active thread as unread and preserves pinned ordering', async () => {
  const html = await renderRail({
    threads: [{ id: 'active', title: 'Current work', status: 'idle', createdAt: '2026-08-26T00:00:00Z', updatedAt: '2026-08-26T00:00:00Z' }],
    activeThreadID: 'active',
    unreadThreadIDs: ['active'],
    pinnedThreadIDs: ['active'],
  })
  assert.doesNotMatch(html, /aria-label="Unread thread"/)
  assert.match(html, /Pinned/)

  const grouped = await renderRail({
    threads: [
      { id: 'regular', title: 'Regular', status: 'idle' },
      { id: 'pinned', title: 'Pinned first', status: 'idle' },
    ],
    activeThreadID: 'regular',
    unreadThreadIDs: [],
    pinnedThreadIDs: ['pinned'],
  })
  const pinnedLabel = grouped.indexOf('k-ai-conversation-rail__section-label">Pinned')
  const threadsLabel = grouped.indexOf('k-ai-conversation-rail__section-label">Threads')
  assert.ok(pinnedLabel >= 0 && threadsLabel > pinnedLabel)
  assert.ok(grouped.indexOf('Pinned first') < grouped.indexOf('Regular'))
})

test('provides accessible context-menu actions and restores focus after dismissal', () => {
  assert.match(railSource, /@contextmenu\.prevent="openContextMenu\(\$event, thread\.id\)"/)
  assert.match(railSource, /event\.key === 'ContextMenu' \|\| \(event\.shiftKey && event\.key === 'F10'\)/)
  assert.match(railSource, /role="menu"/)
  assert.match(railSource, /role="menuitem"/)
  assert.match(railSource, /markRead: 'Mark thread read'/)
  assert.match(railSource, /markUnread: 'Mark thread unread'/)
  assert.match(railSource, /archiveMenu: 'Archive thread'/)
  assert.match(railSource, /<Teleport :to="overlayTarget">/)
  assert.match(railSource, /const rect = menu\.getBoundingClientRect\(\)/)
  assert.match(railSource, /clampContextMenuPosition\(contextMenu\.value\.left, contextMenu\.value\.top, rect\.width, rect\.height\)/)
  assert.match(railSource, /const contextMenuReturnFocus = ref<HTMLElement \| null>\(null\)/)
  assert.match(railSource, /function focusThread\(threadID: string\)[\s\S]*button\[data-thread-id\][\s\S]*target\.focus\(\)/)
  assert.match(railSource, /:data-thread-id="thread\.id"/)
  assert.match(railSource, /@keydown="handleContextMenuKeydown"/)
  assert.match(railSource, /function closeContextMenuAfterTab\(\): void[\s\S]*closeContextMenu\(\)[\s\S]*returnFocus\?\.focus\(\)/)
  assert.match(railSource, /if \(event.key === 'Tab'\) \{[\s\S]*closeContextMenuAfterTab\(\)/)
  assert.match(railSource, /@keydown\.esc\.stop\.prevent="closeContextMenu\(true\)"/)
  assert.match(railSource, /\['ArrowDown', 'ArrowUp', 'Home', 'End'\]/)
  assert.match(railSource, /querySelectorAll<HTMLButtonElement>\('\[role="menuitem"\]:not\(:disabled\)'\)/)
  assert.match(railSource, /showContextMenu\(threadID, rect\.right - 8, rect\.top \+ 8, target\)/)
  assert.match(railSource, /function togglePin\(threadID: string\): void \{[\s\S]*closeContextMenu\(true\)/)
  assert.match(railSource, /function toggleUnread\(threadID: string\): void \{[\s\S]*closeContextMenu\(true\)/)
  assert.match(railSource, /function archiveThread\(threadID: string\): void \{[\s\S]*closeContextMenu\(true\)/)
  assert.match(railSource, /function deleteThread\(threadID: string\): void \{[\s\S]*closeContextMenu\(true\)/)
  assert.match(railSource, /@focusout="handleContextMenuFocusOut"/)
  assert.match(railSource, /function handleContextMenuFocusOut\(event: FocusEvent\)[\s\S]*dismissContextMenu\(\)/)
  assert.match(railSource, /function dismissContextMenu\(\): void \{[\s\S]*const wasOpen = Boolean\(contextMenu\.value\)[\s\S]*closeContextMenu\(\)[\s\S]*if \(wasOpen\) scheduleClose\(\)/)
  assert.match(railSource, /window\.addEventListener\('blur', dismissContextMenu\)/)
  assert.match(railSource, /window\.addEventListener\('scroll', dismissContextMenu, true\)/)
})
