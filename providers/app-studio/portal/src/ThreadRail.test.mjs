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

test('renders the project-scoped thread drawer without a redundant management footer', async () => {
  const { default: ThreadRail } = await vite.ssrLoadModule('/src/ThreadRail.vue')
  const html = await renderToString(createSSRApp(ThreadRail, {
    threads: [
      { id: 'thread-1', title: 'Add toast notifications', status: 'active', createdAt: '2026-08-26T00:00:00Z', updatedAt: '2026-08-26T00:00:00Z' },
      { id: 'thread-2', title: 'Fix authentication', status: 'idle', createdAt: '2026-08-25T00:00:00Z', updatedAt: '2026-08-25T00:00:00Z' },
    ],
    activeThreadID: 'thread-1',
    unreadThreadIDs: ['thread-2'],
    pinnedThreadIDs: ['thread-2'],
  }))

  assert.match(html, /aria-label="Project conversation threads"/)
  assert.match(html, /id="app-studio-thread-rail"/)
  assert.doesNotMatch(html, /Collapse side panel|Anchor side panel/)
  assert.match(html, /Search threads/)
  assert.match(html, /Add toast notifications/)
  assert.match(html, /Fix authentication/)
  assert.doesNotMatch(html, /Conversation|Running|Archived/)
  assert.match(html, /aria-current="page"/)
  assert.match(html, /bg-accent-subtle text-accent hover:bg-accent-subtle/)
  assert.match(html, /Unread thread/)
  assert.match(html, /Pinned/)
  assert.match(html, /Unpin thread/)
  assert.match(html, /Archive thread/)
  assert.doesNotMatch(html, /Manage threads/)
  assert.doesNotMatch(html, /border-t border-border-subtle p-2/)
})

test('defaults anchored, exposes toggle and hover preview controls, and retains hover expansion when collapsed', async () => {
  const source = await readFile(new URL('./ThreadRail.vue', import.meta.url), 'utf8')

  assert.match(source, /@pointerenter="scheduleHoverOpen"/)
  assert.match(source, /@pointerleave="scheduleClose"/)
  assert.match(source, /@focusin="open\(\)"/)
  assert.match(source, /@focusout="handleFocusOut"/)
  assert.match(source, /@keydown\.esc\.stop="handleEscape"/)
  assert.match(source, /const anchored = ref\(true\)/)
  assert.match(source, /ANCHORED_STORAGE_KEY = 'faros:app-studio:thread-rail-anchored:v1'/)
  assert.match(source, /stored === null \? true : stored === '1'/)
  assert.match(source, /function togglePanel\(returnFocus\?: HTMLElement \| null\)/)
  assert.match(source, /else open\(false, returnFocus\)[\s\S]*toggleAnchored\(\)/)
  assert.doesNotMatch(source, /PanelLeftClose|PanelLeftOpen|Collapse side panel|Anchor side panel/)
  assert.match(source, /anchored \? 'md:w-\[var\(--thread-rail-width\)\]' : 'md:w-0'/)
 assert.match(source, /expanded \? \(mobileOpen \? 'w-64 border-r border-border-subtle' : 'w-\[var\(--thread-rail-width\)\] border-r border-border-subtle'\) : 'w-0'/)
  assert.match(source, /const layoutWidth = computed\(\(\) => \([\s\S]*!mobileViewport\.value[\s\S]*!mobileOpen\.value[\s\S]*anchored\.value[\s\S]*effectiveWidth\.value[\s\S]*: 0\n\)\)/)
 assert.match(source, /v-show="!expanded"[\s\S]*w-4 cursor-e-resize/)
  assert.match(source, /expanded \? \(mobileOpen \? 'w-64 border-r border-border-subtle' : 'w-\[var\(--thread-rail-width\)\] border-r border-border-subtle'\) : 'w-0'[\s\S]*@pointerenter="scheduleHoverOpen"[\s\S]*@pointerleave="scheduleClose"/)
  assert.doesNotMatch(source, /w-11/)
  assert.match(source, /defineExpose\(\{[\s\S]*openAndFocus[\s\S]*expanded[\s\S]*layoutWidth[\s\S]*panelID: THREAD_RAIL_PANEL_ID[\s\S]*toggle: togglePanel[\s\S]*previewEnter[\s\S]*previewLeave/)
  assert.match(source, /const mobileViewport = ref\(false\)/)
  assert.match(source, /const visibleExpanded = computed\(\(\) => expanded\.value && \(!mobileViewport\.value \|\| mobileOpen\.value\)\)/)
  assert.match(source, /expanded: visibleExpanded/)
  assert.match(source, /const THREAD_RAIL_PANEL_ID = 'app-studio-thread-rail'/)
  assert.match(source, /:id="THREAD_RAIL_PANEL_ID"/)
  assert.match(source, /function syncMobileViewport\(\)[\s\S]*const next = isMobileViewport\(\)[\s\S]*mobileViewport\.value = next/)
  assert.match(source, /const crossedToDesktop = mobileViewport\.value && !next/)
  assert.match(source, /mobileOpen\.value = false[\s\S]*interactionExpanded\.value = false[\s\S]*mobileReturnFocus\.value = null[\s\S]*query\.value = ''/)
  assert.match(source, /if \(mobileViewport\.value\) \{[\s\S]*mobileOpen\.value = true/)
  assert.match(source, /mobileOpen \? 'absolute inset-y-0 left-0 block w-64'/)
  assert.match(source, /Finish or stop the current run before starting another thread/)
})

test('keeps preview timers stable across rail re-entry and cancels them on toggle', async () => {
  const source = await readFile(new URL('./ThreadRail.vue', import.meta.url), 'utf8')

  assert.match(source, /function scheduleHoverOpen\(\) \{\n  clearCloseTimer\(\)\n  if \(anchored\.value \|\| expanded\.value \|\| hoverOpenTimer\) return/)
  assert.match(source, /function scheduleClose\(\) \{\n  clearHoverOpenTimer\(\)\n  clearCloseTimer\(\)/)
  assert.match(source, /function scheduleClose\(\) \{[\s\S]*if \(contextMenu\.value\) return/)
  assert.match(source, /function close\(options: \{ restoreFocus\?: boolean \} = \{\}[\s\S]*if \(contextMenu\.value\) closeContextMenu\(\)/)
  assert.match(source, /function togglePanel\(returnFocus\?: HTMLElement \| null\) \{\n  clearTimers\(\)/)
  assert.match(source, /function togglePanel\(returnFocus\?: HTMLElement \| null\) \{[\s\S]*if \(contextMenu\.value\) closeContextMenu\(\)/)
  assert.match(source, /function previewEnter\(\) \{[\s\S]*scheduleHoverOpen\(\)/)
  assert.match(source, /function previewLeave\(\) \{[\s\S]*scheduleClose\(\)/)
  assert.match(source, /function isMobileViewport\(\) \{[\s\S]*max-width: 767px/)
  assert.match(source, /const mobileReturnFocus = ref<HTMLElement \| null>\(null\)/)
  assert.match(source, /if \(!mobileOpen\.value\) mobileReturnFocus\.value = returnFocus \?\? currentFocusedElement\(\)/)
  assert.match(source, /const returnFocus = mobileReturnFocus\.value[\s\S]*restoreFocus\(returnFocus\)/)
})

test('supports a persisted, bounded desktop width with pointer and keyboard resize controls', async () => {
  const source = await readFile(new URL('./ThreadRail.vue', import.meta.url), 'utf8')

  assert.match(source, /WIDTH_STORAGE_KEY = 'faros:app-studio:thread-rail-width:v1'/)
  assert.match(source, /DEFAULT_WIDTH = 224/)
  assert.match(source, /MIN_WIDTH = 192/)
  assert.match(source, /MAX_WIDTH = 384/)
  assert.match(source, /CHAT_MIN_WIDTH = 240/)
  assert.match(source, /return Math\.round\(Math\.min\(max, Math\.max\(MIN_WIDTH, value\)\)\)/)
  assert.match(source, /railWidth\.value = readStoredWidth\(\)/)
  assert.match(source, /localStorage\.setItem\(WIDTH_STORAGE_KEY, String\(railWidth\.value\)\)/)
  assert.match(source, /availableWidthCap\.value = Math\.max\(MIN_WIDTH, Math\.min\(MAX_WIDTH, Math\.floor\(available - CHAT_MIN_WIDTH\)\)\)/)
  assert.match(source, /ref="railPanel"/)
  assert.match(source, /v-if="expanded && !mobileOpen"[\s\S]*role="separator"/)
  assert.match(source, /:aria-valuemin="MIN_WIDTH"[\s\S]*:aria-valuemax="availableWidthCap"[\s\S]*:aria-valuenow="effectiveWidth"/)
  assert.match(source, /@pointerdown="startResize"/)
  assert.match(source, /@keydown="handleResizeKeydown"/)
  assert.match(source, /window\.addEventListener\('pointermove', resizeFromPointer\)/)
  assert.match(source, /window\.removeEventListener\('pointercancel', stopResize\)/)
  assert.match(source, /function handleWindowResize\(\) \{[\s\S]*updateAvailableWidthCap\(\)[\s\S]*closeContextMenu\(\)/)
  assert.match(source, /window\.addEventListener\('resize', handleWindowResize\)/)
  assert.match(source, /window\.removeEventListener\('resize', handleWindowResize\)/)
  assert.match(source, /resizing \? 'transition-none' : 'transition-\[width,box-shadow\] duration-200'/)
  assert.match(source, /event\.key === 'ArrowLeft' \? -16 : event\.key === 'ArrowRight' \? 16 : 0/)
  assert.match(source, /setWidth\(event\.clientX - rect\.left, false\)/)
  assert.match(source, /if \(wasResizing\) persistWidth\(\)/)
})

test('filters by title and preserves lifecycle event ownership in the rail component', async () => {
  const source = await readFile(new URL('./ThreadRail.vue', import.meta.url), 'utf8')

  assert.match(source, /const filteredThreads = computed/)
  assert.match(source, /displayTitle\(thread\)\.toLocaleLowerCase\(\)\.includes\(needle\)/)
  assert.match(source, /emit\('select', threadID\)/)
  assert.match(source, /emit\('create'\)/)
  assert.doesNotMatch(source, /emit\('manage'\)|Manage threads/)
  assert.match(source, /v-if="unreadThreadIDSet\.has\(thread\.id\)"/)
  assert.doesNotMatch(source, /window\.confirm|window\.alert/)
})

test('provides row hover actions and an accessible right-click action menu', async () => {
  const source = await readFile(new URL('./ThreadRail.vue', import.meta.url), 'utf8')

  assert.match(source, /@contextmenu\.prevent="openContextMenu\(\$event, thread\.id\)"/)
  assert.match(source, /event\.key === 'ContextMenu'.*event\.shiftKey && event\.key === 'F10'/)
  assert.match(source, /role="menu"/)
  assert.match(source, /role="menuitem"/)
  assert.match(source, /Mark read/)
  assert.match(source, /Mark unread/)
  assert.match(source, /Archive/)
  assert.match(source, /class="app-studio-touch-target group relative flex h-8 min-w-0 items-center overflow-hidden rounded-md transition"/)
  assert.match(source, /class="app-studio-touch-target flex h-8 min-w-0 flex-1 items-center gap-2 rounded-md bg-transparent/)
  assert.match(source, /class="min-w-0 flex-1 truncate text-\[12px\] font-medium"/)
  assert.match(source, /ACTIVE_THREAD_FADE = \[[\s\S]*var\(--color-accent-subtle\)[\s\S]*var\(--color-surface-raised\)/)
  assert.match(source, /RESTING_THREAD_FADE = 'linear-gradient\(to left, var\(--color-surface-raised\)/)
  assert.match(source, /HOVER_THREAD_FADE = 'linear-gradient\(to left, var\(--color-surface-hover\)/)
  assert.match(source, /class="pointer-events-none absolute inset-y-0 right-0 z-10 w-8 transition-opacity group-hover:opacity-0 group-focus-within:opacity-0"/)
  assert.match(source, /backgroundImage: activeThreadID === thread\.id \? ACTIVE_THREAD_FADE : RESTING_THREAD_FADE/)
  assert.match(source, /class="app-studio-touch-visible pointer-events-none absolute inset-y-0 right-1\.5 z-20 flex w-24 items-center gap-0\.5 pl-8 pr-1 opacity-0 transition group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:pointer-events-auto group-focus-within:opacity-100 \[@media\(hover:none\)\]:w-32 \[@media\(any-pointer:coarse\)\]:w-32"/)
  assert.match(source, /class="app-studio-touch-target flex h-7 w-7 shrink-0 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition hover:bg-transparent hover:text-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent\/40"/)
  assert.match(source, /class="app-studio-touch-target flex h-7 w-7 shrink-0 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition hover:bg-transparent hover:text-danger focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-danger\/40 disabled:cursor-not-allowed disabled:opacity-40"/)
  assert.doesNotMatch(source, /class="pointer-events-none absolute inset-y-0 right-0 z-20 flex items-center gap-0\.5 pl-8 pr-1/)
  assert.match(source, /backgroundImage: activeThreadID === thread\.id \? ACTIVE_THREAD_FADE : HOVER_THREAD_FADE/)
  assert.doesNotMatch(source, /class="flex h-8 shrink-0 items-center gap-0\.5 px-1 opacity-0 transition/)
  assert.doesNotMatch(source, /statusLabel|Conversation|Running|Archived/)
  assert.doesNotMatch(source, /absolute right-1\.5 top-1\/2/)
  assert.match(source, /border-0 bg-transparent p-0 text-text-muted/)
  assert.doesNotMatch(source, /group-hover:pointer-events-auto[\s\S]*rounded-md border border-border-subtle bg-surface-raised\/95 p-0\.5 opacity-0 shadow-sm/)
  assert.match(source, /emit\('archive', threadID\)/)
  assert.match(source, /emit\('togglePin', threadID\)/)
  assert.match(source, /emit\('setUnread', threadID, !unreadThreadIDSet\.value\.has\(threadID\)\)/)
  assert.match(source, /const unreadThreadIDSet = computed\(\(\) => new Set\([\s\S]*props\.unreadThreadIDs\.filter\(\(threadID\) => threadID !== props\.activeThreadID\)/)
  assert.match(source, /function toggleUnread\(threadID: string\) \{[\s\S]*if \(threadID === props\.activeThreadID\) \{[\s\S]*closeContextMenu\(true\)[\s\S]*return/)
  assert.match(source, /:disabled="contextMenuThread\.id === activeThreadID"/)
  assert.match(source, /<Teleport to="#app-studio-overlay-root">/)
  assert.doesNotMatch(source, /<Teleport to="body">/)
  assert.match(source, /data-thread-context-menu[\s\S]*class="fixed \[z-index:var\(--app-studio-z-menu\)\] w-48 rounded-md border border-border-default bg-surface-overlay p-1 shadow-2xl"/)
  assert.match(source, /const rect = menu\.getBoundingClientRect\(\)/)
  assert.match(source, /clampContextMenuPosition\(contextMenu\.value\.left, contextMenu\.value\.top, rect\.width, rect\.height\)/)
  assert.doesNotMatch(source, /const menuHeight = 124/)
  assert.match(source, /const contextMenuReturnFocus = ref<HTMLElement \| null>\(null\)/)
  assert.match(source, /function focusThread\(threadID: string\)[\s\S]*button\[data-thread-id\][\s\S]*target\.focus\(\)/)
  assert.match(source, /:data-thread-id="thread\.id"/)
  assert.match(source, /@keydown="handleContextMenuKeydown"/)
  assert.match(source, /@keydown\.esc\.stop\.prevent="closeContextMenu\(true\)"/)
  assert.match(source, /function handleContextMenuKeydown\(event: KeyboardEvent\)/)
  assert.match(source, /\['ArrowDown', 'ArrowUp', 'Home', 'End'\]/)
  assert.match(source, /querySelectorAll<HTMLButtonElement>\('\[role="menuitem"\]:not\(:disabled\)'\)/)
  assert.match(source, /showContextMenu\(threadID, rect\.right - 8, rect\.top \+ 8, target\)/)
  assert.match(source, /function togglePin\(threadID: string\) \{[\s\S]*closeContextMenu\(true\)/)
  assert.match(source, /function toggleUnread\(threadID: string\) \{[\s\S]*closeContextMenu\(true\)/)
  assert.match(source, /function archiveThread\(threadID: string\) \{[\s\S]*closeContextMenu\(true\)/)
  assert.match(source, /@focusout="handleContextMenuFocusOut"/)
  assert.match(source, /function handleContextMenuFocusOut\(event: FocusEvent\) \{[\s\S]*const next = event\.relatedTarget[\s\S]*if \(next instanceof Node && actionMenu\.value\?\.contains\(next\)\) return[\s\S]*dismissContextMenu\(\)/)
  assert.match(source, /function dismissContextMenu\(\) \{[\s\S]*const wasOpen = Boolean\(contextMenu\.value\)[\s\S]*closeContextMenu\(\)[\s\S]*if \(wasOpen\) scheduleClose\(\)/)
  assert.match(source, /if \(target instanceof Node && actionMenu\.value\?\.contains\(target\)\) return\n  dismissContextMenu\(\)/)
  assert.match(source, /window\.addEventListener\('blur', dismissContextMenu\)/)
  assert.match(source, /window\.addEventListener\('scroll', dismissContextMenu, true\)/)
})

test('never presents the active thread as unread', async () => {
  const { default: ThreadRail } = await vite.ssrLoadModule('/src/ThreadRail.vue')
  const html = await renderToString(createSSRApp(ThreadRail, {
    threads: [
      { id: 'active', title: 'Current work', status: 'idle', createdAt: '2026-08-26T00:00:00Z', updatedAt: '2026-08-26T00:00:00Z' },
    ],
    activeThreadID: 'active',
    unreadThreadIDs: ['active'],
  }))

  assert.doesNotMatch(html, /aria-label="Unread thread"/)
})

test('groups pinned threads above the regular thread section', async () => {
  const source = await readFile(new URL('./ThreadRail.vue', import.meta.url), 'utf8')

  assert.match(source, /const threadSections = computed/)
  assert.match(source, /filter\(\(thread\) => pinnedThreadIDSet\.value\.has\(thread\.id\)\)/)
  assert.match(source, /\{ id: 'pinned', label: 'Pinned', threads: pinned \}/)
  assert.match(source, /\{ id: 'threads', label: pinned\.length \? 'Threads' : '', threads: regular \}/)
})
