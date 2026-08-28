<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, nextTick, watch, watchEffect } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useTerminalSessionsStore } from '@/stores/terminalSessions'
import { useTenantStore } from '@/stores/tenant'
import { setLayoutInsets } from '@/composables/useLayoutInsets'
import { useSidebarExpansion } from '@/composables/useSidebarExpansion'
import CliQuickstartModal from '@/components/CliQuickstartModal.vue'
import AccountAccessMenu from '@/components/AccountAccessMenu.vue'
import WorkspaceSwitcher from '@/components/WorkspaceSwitcher.vue'
import FirstWorkspaceWizard from '@/components/FirstWorkspaceWizard.vue'
import { Hexagon, LayoutDashboard, Zap, GripHorizontal, GripVertical, Puzzle, Dot, PanelLeftClose, PanelLeftOpen, ChevronDown, CircleHelp, ExternalLink, Loader2 } from 'lucide-vue-next'
import { useProvidersStore } from '@/stores/providers'
import { useAdminStore } from '@/stores/admin'
import { categoryIcons, fallbackCategoryIcon } from '@/lib/categoryIcons'

const auth = useAuthStore()
const terminalStore = useTerminalSessionsStore()
const providersStore = useProvidersStore()
const tenantStore = useTenantStore()
const adminStore = useAdminStore()

// Probe platform-admin access once so the account menu can show the /bonkers entry
// only to allowlisted identities. Non-admins get a single quiet 403 and the
// menu item stays hidden.
onMounted(() => { void adminStore.checkAccess() })
const layoutProps = defineProps<{ fullBleed?: boolean }>()

const route = useRoute()
const router = useRouter()

// Empty-org guard: when the active org has zero workspaces (after fetch
// completes), every workspace-scoped page would try to query a cluster
// that doesn't exist. Replace the slot with the create-workspace wizard
// instead. Pages that don't need a workspace — the settings page
// and the providers catalog — render normally so the user keeps a
// non-blocked path to manage orgs/workspaces.
//
// workspacesByOrg[uuid] is undefined while the initial fetch is in
// flight; we only show the wizard once it lands as an empty array, so
// the UI doesn't flash the wizard between an org switch and the fetch
// resolving.
const showWorkspaceWizard = computed(() => {
  if (!auth.token) return false
  const orgUUID = tenantStore.orgUUID
  if (!orgUUID) return false
  const list = tenantStore.workspacesByOrg[orgUUID]
  const activeWorkspace = tenantStore.activeWorkspace
  if (activeWorkspace?.clusterName) return false
  if (!tenantStore.workspaceSelectionHydrated) return false
  if (!list || list.some((workspace) => !workspace.deletionRequestedAt)) return false
  const path = route.path
  if (path === '/settings' || path.startsWith('/settings/')) return false
  if (path === '/providers') return false
  if (path === '/organizations' || path.startsWith('/organizations/')) return false
  return true
})

// Do not mount workspace-scoped content against a selected workspace whose
// kcp cluster is still provisioning (or whose backing row disappeared). The
// list guard avoids a hard-refresh flash while the persisted workspace is
// being hydrated; once the list arrives, readiness is authoritative.
const showWorkspacePending = computed(() => {
  const path = route.path
  if (path === '/settings' || path.startsWith('/settings/')) return false
  if (path === '/providers') return false
  if (path === '/organizations' || path.startsWith('/organizations/')) return false
  if (!auth.token) return false
  if (!tenantStore.workspaceSelectionHydrated) return true
  if (!tenantStore.workspaceUUID) return true
  return !tenantStore.activeWorkspaceUsable
})

// An explicit organization switch intentionally leaves workspaceUUID empty.
// Keep workspace-scoped routes from rendering against auth.clusterName's
// previous transport target while the user is in that org-only state. The
// settings and catalog surfaces are deliberately workspace-optional.
watchEffect(() => {
  if (!auth.token || !tenantStore.orgUUID || tenantStore.workspaceUUID) return
  const list = tenantStore.workspacesByOrg[tenantStore.orgUUID]
  if (!list || list.length === 0) return
  const path = route.path
  if (path === '/settings' || path.startsWith('/settings/') || path === '/providers' || path === '/organizations' || path.startsWith('/organizations/')) return
  void router.replace('/settings/workspaces')
})

// Keep the routed slot suppressed for the whole navigation transition, but
// defer the explanatory shell so a fast workspace switch does not flash a
// status message. Watching the token (rather than only its boolean state)
// resets the timer for a newer switch that starts before an older navigation
// settles.
const WORKSPACE_TRANSITION_INDICATOR_DELAY_MS = 200
const showWorkspaceTransitionIndicator = ref(false)
let workspaceTransitionTimer: ReturnType<typeof setTimeout> | undefined

function clearWorkspaceTransitionTimer(): void {
  if (workspaceTransitionTimer === undefined) return
  clearTimeout(workspaceTransitionTimer)
  workspaceTransitionTimer = undefined
}

watch(
  () => tenantStore.workspaceTransitionToken,
  (token) => {
    clearWorkspaceTransitionTimer()
    showWorkspaceTransitionIndicator.value = false
    if (token === null) return
    workspaceTransitionTimer = setTimeout(() => {
      workspaceTransitionTimer = undefined
      if (tenantStore.workspaceTransitionToken === token) {
        showWorkspaceTransitionIndicator.value = true
      }
    }, WORKSPACE_TRANSITION_INDICATOR_DELAY_MS)
  },
  { immediate: true, flush: 'sync' },
)

const mainPaddingBottom = computed(() => {
  if (!terminalStore.isVisible || terminalStore.sessions.length === 0) return undefined
  if (terminalStore.panelState.isFullscreen) return undefined
  const h = terminalStore.panelState.isMinimized ? 40 : terminalStore.panelState.height
  return `${h + 16}px`
})

const mainClass = computed(() => [
  'relative z-10 min-h-0 flex-1',
  layoutProps.fullBleed ? 'overflow-hidden p-0' : 'overflow-y-auto px-8 py-5',
])

const mainStyle = computed(() => {
  if (layoutProps.fullBleed || !mainPaddingBottom.value) return undefined
  return { paddingBottom: mainPaddingBottom.value }
})

const slotClass = computed(() => [
  'relative z-10',
  // Single source of truth for page content width: every non-full-bleed page
  // renders in the SAME centered max-w-5xl column so the layout doesn't shift
  // when navigating. Pages must NOT add their own mx-auto/max-w-* wrapper —
  // that reintroduces per-page width drift. Full-bleed provider workbenches opt
  // out and manage their own width.
  layoutProps.fullBleed ? 'h-full min-h-0' : 'mx-auto w-full max-w-5xl',
])

interface NavItem {
  label: string
  to: string
  // Either a lucide component (static) or a URL string (dynamic provider icon).
  icon?: unknown
  iconURL?: string | null
}

// Static destinations precede categorized provider entries. Everything
// provider-shaped (Edges, MCP, Workloads, etc.) flows through providersStore —
// those items get categorized + sub-nav treatment below. Dashboard is the
// only true platform-wide page; the Providers catalog is its adjacent entry.
// Settings lives in the account-and-access menu rather than competing
// with providers as a primary destination. The same menu is shared by
// vertical, horizontal, and floating chrome.
const staticNavItems: NavItem[] = [
  { label: 'Dashboard', to: '/', icon: LayoutDashboard },
]

// Catalog link sits immediately below Dashboard and routes to the full
// provider catalog page when clicked.
const providersHeaderItem: NavItem = { label: 'Providers', to: '/providers', icon: Puzzle }

// Resolve a category's Lucide component from the icon-name registry.
// Categories the hub doesn't know (third-party ad-hoc) get a fallback.
function categoryIcon(name: string | null): unknown {
  if (!name) return fallbackCategoryIcon
  return categoryIcons[name] ?? fallbackCategoryIcon
}

// horizontalNavSections shape: the horizontal + floating docks need to
// distinguish what would otherwise be a stream of identical Puzzle
// icons. Group items by category and render a tiny category chip
// before each group so the bar reads as "Dashboard | Providers | Kubernetes:
// x y | MCP: z | Other: w" instead of a flat icon parade.
interface HorizontalSection {
  key: string
  label: string | null
  icon: unknown | null
  items: NavItem[]
}
const horizontalNavSections = computed<HorizontalSection[]>(() => {
  const sections: HorizontalSection[] = []
  sections.push({ key: 'static', label: null, icon: null, items: [...staticNavItems, providersHeaderItem] })
  const cat = providersStore.categorizedNavItems
  for (const g of cat.groups) {
    sections.push({
      key: 'g-' + g.name,
      label: g.name,
      icon: categoryIcon(g.icon),
      items: g.items.map((p) => ({ label: p.label, to: p.to, iconURL: p.iconURL })),
    })
  }
  if (cat.uncategorized.length) {
    sections.push({
      key: 'uncat',
      label: 'Other',
      icon: fallbackCategoryIcon,
      items: cat.uncategorized.map((p) => ({ label: p.label, to: p.to, iconURL: p.iconURL })),
    })
  }
  return sections
})

// isActive lights up a nav row when the current route matches its target.
// `exact` opts out of prefix matching for links whose URL is a parent of
// other nav entries — the Providers catalog (/providers) is a sibling of
// /providers/{name} provider frames in the nav, so a prefix match would
// double-highlight both rows when you're inside a provider. `/providers`
// is treated as exact by default since every flat-nav loop renders both
// the catalog row and the per-provider rows.
function isActive(path: string, exact = false) {
  if (path === '/' || path === '/providers' || exact) return route.path === path
  return route.path === path || route.path.startsWith(path + '/')
}

function handleLogout() {
  auth.logout()
  router.push('/login')
}

function retryWorkspaceHydration() {
  if (!tenantStore.orgUUID) return
  void tenantStore.fetchWorkspaces(tenantStore.orgUUID, {
    selectDefault: tenantStore.workspaceMode !== 'organization',
  })
}

const showCliModal = ref(false)

// --- Collapsible sidebar rail ---
// The vertical dock defaults to a 56px icon rail so the canvas isn't taxed by
// a permanent 192px label column; labels expand on click and the choice
// persists per browser. Collapsed rows are icon-only with a native title
// tooltip (design-book §6 "Sidebar rail").
const { sidebarExpanded, toggleSidebar } = useSidebarExpansion()

// --- Collapsible nav groups (expanded sidebar only) ---
// Category groups and provider sub-nav toggle on click and persist per
// browser. A group holding the active route is always forced open so
// navigation state is never hidden from the user; the stored preference
// takes effect again once they navigate elsewhere.
const NAV_GROUPS_KEY = 'faros-nav-collapsed-groups'
function loadCollapsedGroups(): Record<string, boolean> {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(NAV_GROUPS_KEY) || '{}')
    if (parsed && typeof parsed === 'object') return parsed as Record<string, boolean>
  } catch { /* ignore */ }
  return {}
}
const collapsedGroups = ref<Record<string, boolean>>(loadCollapsedGroups())
function toggleNavGroup(key: string) {
  collapsedGroups.value = { ...collapsedGroups.value, [key]: !collapsedGroups.value[key] }
  localStorage.setItem(NAV_GROUPS_KEY, JSON.stringify(collapsedGroups.value))
}
function navGroupHasActive(items: Array<{ to: string; children?: Array<{ to: string }> }>): boolean {
  return items.some((i) => isActive(i.to) || (i.children ?? []).some((c) => isActive(c.to)))
}
function isNavGroupOpen(key: string, items: Array<{ to: string; children?: Array<{ to: string }> }>): boolean {
  if (navGroupHasActive(items)) return true
  return !collapsedGroups.value[key]
}

// --- Draggable dock with edge-snap (all 4 edges) ---
type DockMode = 'float' | 'left' | 'right' | 'top' | 'bottom'
const DOCK_STORAGE_KEY = 'faros-dock-state'
const SNAP_THRESHOLD = 80

const floatRef = ref<HTMLElement | null>(null)
const dockedRef = ref<HTMLElement | null>(null)
const isDragging = ref(false)
const nearEdge = ref<DockMode | null>(null)

interface DockState {
  mode: DockMode
  x: number
  y: number
}

function loadDockState(): DockState {
  try {
    const raw = localStorage.getItem(DOCK_STORAGE_KEY)
    if (!raw) return { mode: 'left', x: -1, y: -1 }
    const s = JSON.parse(raw) as DockState
    if (['left', 'right', 'top', 'bottom'].includes(s.mode)) return s
    if (s.mode === 'float') {
      // The float MODE is the user's choice and must survive every refresh;
      // only the parked position depends on the viewport. Discarding the
      // whole state when x/y fell outside the current window (smaller
      // window, different zoom, devtools open) silently flipped the nav
      // back to the left rail — the layout changed from refresh to refresh
      // depending on window size at load. Clamp the position instead.
      if (s.x < 0 || s.y < 0) return { mode: 'float', x: -1, y: -1 }
      return {
        mode: 'float',
        x: Math.max(0, Math.min(s.x, window.innerWidth - 300)),
        y: Math.max(0, Math.min(s.y, window.innerHeight - 48)),
      }
    }
  } catch { /* ignore */ }
  return { mode: 'left', x: -1, y: -1 }
}

function saveDockState() {
  localStorage.setItem(DOCK_STORAGE_KEY, JSON.stringify(dockState.value))
}

const dockState = ref<DockState>(loadDockState())

const isDocked = computed(() => !isDragging.value && dockState.value.mode !== 'float')
const isVerticalDock = computed(() => isDocked.value && (dockState.value.mode === 'left' || dockState.value.mode === 'right'))
const isHorizontalDock = computed(() => isDocked.value && (dockState.value.mode === 'top' || dockState.value.mode === 'bottom'))
const showFloat = computed(() => !isDocked.value)

let dragOffset = { x: 0, y: 0 }
let dragSize = { w: 300, h: 48 }
const dragPos = ref<{ x: number; y: number }>({ x: 0, y: 0 })

function onDragStart(e: MouseEvent) {
  const el = dockedRef.value || floatRef.value
  if (!el) return

  const rect = el.getBoundingClientRect()
  dragOffset.x = e.clientX - rect.left
  dragOffset.y = e.clientY - rect.top

  isDragging.value = true

  nextTick(() => {
    const floatEl = floatRef.value
    if (floatEl) {
      dragSize.w = floatEl.offsetWidth
      dragSize.h = floatEl.offsetHeight
    }
  })

  dragPos.value = {
    x: Math.max(0, e.clientX - dragOffset.x),
    y: Math.max(0, e.clientY - dragOffset.y),
  }

  e.preventDefault()
}

function onDragMove(e: MouseEvent) {
  if (!isDragging.value) return

  const x = Math.max(0, Math.min(window.innerWidth - dragSize.w, e.clientX - dragOffset.x))
  const y = Math.max(0, Math.min(window.innerHeight - dragSize.h, e.clientY - dragOffset.y))
  dragPos.value = { x, y }

  // Detect closest edge
  const distL = e.clientX
  const distR = window.innerWidth - e.clientX
  const distT = e.clientY
  const distB = window.innerHeight - e.clientY
  const minDist = Math.min(distL, distR, distT, distB)

  if (minDist < SNAP_THRESHOLD) {
    if (minDist === distL) nearEdge.value = 'left'
    else if (minDist === distR) nearEdge.value = 'right'
    else if (minDist === distT) nearEdge.value = 'top'
    else nearEdge.value = 'bottom'
  } else {
    nearEdge.value = null
  }
}

function onDragEnd() {
  if (!isDragging.value) return

  if (nearEdge.value) {
    dockState.value = { mode: nearEdge.value, x: -1, y: -1 }
  } else {
    dockState.value = { mode: 'float', x: dragPos.value.x, y: dragPos.value.y }
  }

  isDragging.value = false
  nearEdge.value = null
  saveDockState()
}

function resetDockPos() {
  dockState.value = { mode: 'float', x: -1, y: -1 }
  // Persist the state we just rendered. Removing the key instead meant the
  // session showed the default float pill while the next refresh loaded the
  // left rail — the same what-you-see-isn't-what-reloads mismatch as the
  // float-position validation above.
  saveDockState()
}

onMounted(() => {
  window.addEventListener('mousemove', onDragMove)
  window.addEventListener('mouseup', onDragEnd)
})
onUnmounted(() => {
  window.removeEventListener('mousemove', onDragMove)
  window.removeEventListener('mouseup', onDragEnd)
  clearWorkspaceTransitionTimer()
  // TerminalDock outlives routed layouts. Never carry this page's dock
  // clearance into standalone shells such as platform admin or login.
  setLayoutInsets({ left: '0px', right: '0px', bottom: '0px' })
})

const isDefaultFloat = computed(() => !isDragging.value && dockState.value.mode === 'float' && dockState.value.x < 0)
const hasCustomPos = computed(() => dockState.value.mode !== 'float' || dockState.value.x >= 0)

const floatStyle = computed(() => {
  if (isDragging.value) {
    return { left: `${dragPos.value.x}px`, top: `${dragPos.value.y}px` }
  }
  if (dockState.value.mode === 'float' && dockState.value.x >= 0) {
    return { left: `${dockState.value.x}px`, top: `${dockState.value.y}px` }
  }
  return {}
})

// Layout direction based on dock mode
const layoutClass = computed(() => {
  if (isVerticalDock.value) return 'flex-row'
  return 'flex-col'
})

// CSS-variable insets so fixed-position overlays (like the terminal dock) can
// avoid sliding under the side/bottom nav docks.
const layoutInsetsStyle = computed<Record<string, string>>(() => {
  const railWidth = sidebarExpanded.value ? '12rem' : '3.5rem'
  const left = isVerticalDock.value && dockState.value.mode === 'left' ? railWidth : '0px'
  const right = isVerticalDock.value && dockState.value.mode === 'right' ? railWidth : '0px'
  const bottom = isHorizontalDock.value && dockState.value.mode === 'bottom' ? '44px' : '0px'
  return {
    '--app-inset-left': left,
    '--app-inset-right': right,
    '--app-inset-bottom': bottom,
  }
})

// The terminal dock is a persistent singleton mounted at the app root (App.vue),
// *outside* this component's DOM subtree — so it can't inherit the inset vars from
// the inline style below. Publish them to a shared reactive singleton the dock
// reads directly, keeping it clear of the side/bottom nav docks.
watchEffect(() => {
  setLayoutInsets({
    left: layoutInsetsStyle.value['--app-inset-left'],
    right: layoutInsetsStyle.value['--app-inset-right'],
    bottom: layoutInsetsStyle.value['--app-inset-bottom'],
  })
})
</script>

<template>
  <div class="relative flex h-screen bg-surface" :class="layoutClass" :style="layoutInsetsStyle">
    <!-- Edge snap hint overlays -->
    <Transition name="fade">
      <div v-if="nearEdge === 'left'" class="fixed inset-y-0 left-0 z-[60] w-48 rounded-r-xl bg-accent/10 border-r-2 border-accent/40" />
    </Transition>
    <Transition name="fade">
      <div v-if="nearEdge === 'right'" class="fixed inset-y-0 right-0 z-[60] w-48 rounded-l-xl bg-accent/10 border-l-2 border-accent/40" />
    </Transition>
    <Transition name="fade">
      <div v-if="nearEdge === 'top'" class="fixed inset-x-0 top-0 z-[60] h-11 rounded-b-xl bg-accent/10 border-b-2 border-accent/40" />
    </Transition>
    <Transition name="fade">
      <div v-if="nearEdge === 'bottom'" class="fixed inset-x-0 bottom-0 z-[60] h-11 rounded-t-xl bg-accent/10 border-t-2 border-accent/40" />
    </Transition>

    <!-- VERTICAL SIDEBAR (left or right) -->
    <aside
      v-if="isVerticalDock"
      ref="dockedRef"
      class="relative z-50 flex h-full flex-shrink-0 flex-col overflow-hidden border-border-default bg-surface-raised py-3 px-2 transition-[width] duration-200"
      :class="[dockState.mode === 'left' ? 'order-first border-r' : 'order-last border-l', sidebarExpanded ? 'w-48' : 'w-14']"
    >
      <!-- Drag handle + Logo. Collapsed rail stacks the same pieces
           vertically; the wordmark and Live chip only exist expanded. -->
      <div class="mb-1 flex items-center gap-2 px-2" :class="sidebarExpanded ? '' : 'flex-col gap-1.5 px-0'">
        <div
          class="flex h-6 w-6 cursor-grab items-center justify-center rounded-lg text-text-muted/30 transition-colors hover:text-text-muted"
          @mousedown="onDragStart"
        >
          <GripVertical class="h-3 w-3" :stroke-width="2" />
        </div>
        <div class="flex h-7 w-7 items-center justify-center rounded-lg border border-border-default bg-surface-overlay">
          <Hexagon class="h-3.5 w-3.5 text-accent" :stroke-width="2" />
        </div>
        <template v-if="sidebarExpanded">
          <span class="type-display text-[11px] font-bold tracking-[0.08em] text-text-primary">FAROS</span>
          <div class="flex items-center gap-0.5 rounded-sm border border-success/20 bg-success-subtle px-1.5 py-px">
            <Zap class="h-2 w-2 text-success" :stroke-width="2.5" fill="currentColor" />
            <span class="text-[7px] font-semibold uppercase tracking-widest text-success">Live</span>
          </div>
        </template>
        <button
          type="button"
          class="k-btn k-btn--ghost flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition-colors hover:bg-surface-overlay/50 hover:text-text-secondary"
          :class="sidebarExpanded ? 'ml-auto' : ''"
          :title="sidebarExpanded ? 'Collapse sidebar' : 'Expand sidebar'"
          @click="toggleSidebar"
        >
          <component :is="sidebarExpanded ? PanelLeftClose : PanelLeftOpen" class="h-3.5 w-3.5" :stroke-width="1.75" />
        </button>
      </div>

      <div class="mx-2 my-2 h-px bg-border-default/50" />

      <!-- Workspace is the frequent operating context, so it stays prominent
           above navigation. Organization switching lives separately with the
           account controls at the bottom of the shell. -->
      <WorkspaceSwitcher :variant="sidebarExpanded ? 'sidebar' : 'compact'" />

      <div class="mx-2 my-2 h-px bg-border-default/50" />

      <!-- Scrollable nav region. With many providers this is the only
           part of the dock that grows, so it scrolls internally instead
           of squishing the rows and pushing the footer controls off
           screen. min-h-0 lets it shrink below its content height inside
           the flex column; the header above and footer below stay pinned. -->
      <div class="-mr-1 flex min-h-0 flex-1 flex-col overflow-y-auto pr-1">
      <!-- Static nav items (Dashboard, Providers) -->
      <router-link
        v-for="item in staticNavItems"
        :key="item.to"
        :to="item.to"
        class="flex items-center gap-2.5 rounded-md px-3 py-2 text-[11px] font-medium transition-all duration-200"
        :class="[isActive(item.to) ? 'bg-accent/15 text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary', sidebarExpanded ? '' : 'justify-center']"
        :title="sidebarExpanded ? undefined : item.label"
        :aria-label="sidebarExpanded ? undefined : item.label"
      >
        <component :is="item.icon" class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
        <span v-if="sidebarExpanded">{{ item.label }}</span>
      </router-link>

      <!-- Providers catalog is the primary destination immediately below
           Dashboard; provider categories and their entries follow it. -->
      <router-link
        :to="providersHeaderItem.to"
        class="flex items-center gap-2.5 rounded-md px-3 py-1.5 text-[10px] font-medium uppercase tracking-wider transition-all duration-200"
        :class="[isActive(providersHeaderItem.to, true) ? 'bg-accent/15 text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-muted/80 hover:bg-surface-overlay/50 hover:text-text-secondary', sidebarExpanded ? '' : 'justify-center']"
        :title="sidebarExpanded ? undefined : providersHeaderItem.label"
        :aria-label="sidebarExpanded ? undefined : providersHeaderItem.label"
      >
        <Puzzle class="h-3.5 w-3.5 flex-shrink-0" :stroke-width="1.75" />
        <span v-if="sidebarExpanded">{{ providersHeaderItem.label }}</span>
      </router-link>

      <!-- Provider categories render as non-clickable section dividers:
           a thin rule with the category icon + label inline, then the
           providers in that category as indented rows. Children of a
           provider (e.g. Workloads under Kubernetes) get one more level
           of indent, with a leading dot glyph for visual hierarchy. -->
      <template v-for="group in providersStore.categorizedNavItems.groups" :key="'cat-' + group.name">
        <!-- Category header doubles as the group toggle when expanded. The
             chevron rotates closed; a group holding the active route stays
             forced open (isNavGroupOpen). -->
        <button
          v-if="sidebarExpanded"
          type="button"
          class="k-btn k-btn--text mt-3 mb-1 flex w-full items-center justify-start gap-2 px-3 py-0 text-left"
          :title="isNavGroupOpen('cat:' + group.name, group.items) ? 'Collapse ' + group.name : 'Expand ' + group.name"
          @click="toggleNavGroup('cat:' + group.name)"
        >
          <component :is="categoryIcon(group.icon)" class="h-3 w-3 flex-shrink-0 text-text-muted/70" :stroke-width="2" />
          <span class="text-[9px] font-semibold uppercase tracking-wider text-text-muted/70">{{ group.name }}</span>
          <div class="h-px flex-1 bg-border-default/40" />
          <ChevronDown
            class="h-3 w-3 flex-shrink-0 text-text-muted/70 transition-transform duration-200"
            :class="isNavGroupOpen('cat:' + group.name, group.items) ? '' : '-rotate-90'"
            :stroke-width="2"
          />
        </button>
        <div v-else class="mx-3 mt-3 mb-1 h-px bg-border-default/40" :title="group.name" />
        <template v-if="!sidebarExpanded || isNavGroupOpen('cat:' + group.name, group.items)">
          <template v-for="item in group.items" :key="item.to">
            <div
              class="group/nav flex items-center gap-2.5 rounded-md px-3 py-1.5 text-[11px] font-medium transition-all duration-200"
              :class="[isActive(item.to) ? 'bg-accent/15 text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary', sidebarExpanded ? '' : 'justify-center']"
              :title="sidebarExpanded ? undefined : item.label"
            >
              <router-link
                :to="item.to"
                class="flex min-w-0 flex-1 items-center gap-2.5"
                :class="sidebarExpanded ? '' : 'justify-center'"
                :aria-label="sidebarExpanded ? undefined : item.label"
              >
                <img v-if="item.iconURL" :src="item.iconURL" alt="" class="h-3.5 w-3.5 flex-shrink-0 object-contain" />
                <Puzzle v-else class="h-3.5 w-3.5 flex-shrink-0" :stroke-width="1.75" />
                <span v-if="sidebarExpanded" class="min-w-0 flex-1 truncate">{{ item.label }}</span>
              </router-link>
              <!-- Sub-nav toggle is a sibling of the route link so each
                   control has one clear keyboard activation target. Hidden
                   on the rail (children don't render there). -->
              <button
                v-if="sidebarExpanded && item.children?.length"
                type="button"
                class="k-btn k-btn--text -mr-1 flex h-4 w-4 flex-shrink-0 items-center justify-center rounded-sm p-0 text-text-muted/70 hover:text-text-secondary"
                :title="isNavGroupOpen('item:' + item.to, item.children) ? 'Hide ' + item.label + ' pages' : 'Show ' + item.label + ' pages'"
                @click="toggleNavGroup('item:' + item.to)"
              >
                <ChevronDown
                  class="h-3 w-3 transition-transform duration-200"
                  :class="isNavGroupOpen('item:' + item.to, item.children) ? '' : '-rotate-90'"
                  :stroke-width="2"
                />
              </button>
            </div>
            <template v-if="sidebarExpanded && item.children?.length && isNavGroupOpen('item:' + item.to, item.children)">
              <router-link
                v-for="child in item.children"
                :key="'c-' + child.to"
                :to="child.to"
                class="flex items-center gap-2 rounded-md py-1.5 pr-3 pl-8 text-[11px] font-medium transition-all duration-200"
                :class="isActive(child.to) ? 'bg-accent/15 text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary'"
              >
                <Dot class="h-3.5 w-3.5 flex-shrink-0 -ml-1" :stroke-width="3" />
                <span>{{ child.label }}</span>
              </router-link>
            </template>
          </template>
        </template>
      </template>

      <!-- Uncategorized providers (third-party with no spec.category) sit
           under their own divider so the rhythm of the sidebar stays
           consistent. -->
      <template v-if="providersStore.categorizedNavItems.uncategorized.length">
        <button
          v-if="sidebarExpanded"
          type="button"
          class="k-btn k-btn--text mt-3 mb-1 flex w-full items-center justify-start gap-2 px-3 py-0 text-left"
          :title="isNavGroupOpen('cat:Other', providersStore.categorizedNavItems.uncategorized) ? 'Collapse Other' : 'Expand Other'"
          @click="toggleNavGroup('cat:Other')"
        >
          <Puzzle class="h-3 w-3 flex-shrink-0 text-text-muted/70" :stroke-width="2" />
          <span class="text-[9px] font-semibold uppercase tracking-wider text-text-muted/70">Other</span>
          <div class="h-px flex-1 bg-border-default/40" />
          <ChevronDown
            class="h-3 w-3 flex-shrink-0 text-text-muted/70 transition-transform duration-200"
            :class="isNavGroupOpen('cat:Other', providersStore.categorizedNavItems.uncategorized) ? '' : '-rotate-90'"
            :stroke-width="2"
          />
        </button>
        <div v-else class="mx-3 mt-3 mb-1 h-px bg-border-default/40" title="Other" />
        <template v-if="!sidebarExpanded || isNavGroupOpen('cat:Other', providersStore.categorizedNavItems.uncategorized)">
          <template v-for="item in providersStore.categorizedNavItems.uncategorized" :key="'u-' + item.to">
            <div
              class="flex items-center gap-2.5 rounded-md px-3 py-1.5 text-[11px] font-medium transition-all duration-200"
              :class="[isActive(item.to) ? 'bg-accent/15 text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary', sidebarExpanded ? '' : 'justify-center']"
              :title="sidebarExpanded ? undefined : item.label"
            >
              <router-link
                :to="item.to"
                class="flex min-w-0 flex-1 items-center gap-2.5"
                :class="sidebarExpanded ? '' : 'justify-center'"
                :aria-label="sidebarExpanded ? undefined : item.label"
              >
                <img v-if="item.iconURL" :src="item.iconURL" alt="" class="h-3.5 w-3.5 flex-shrink-0 object-contain" />
                <Puzzle v-else class="h-3.5 w-3.5 flex-shrink-0" :stroke-width="1.75" />
                <span v-if="sidebarExpanded" class="min-w-0 flex-1 truncate">{{ item.label }}</span>
              </router-link>
              <button
                v-if="sidebarExpanded && item.children?.length"
                type="button"
                class="k-btn k-btn--text -mr-1 flex h-4 w-4 flex-shrink-0 items-center justify-center rounded-sm p-0 text-text-muted/70 hover:text-text-secondary"
                :title="isNavGroupOpen('item:' + item.to, item.children) ? 'Hide ' + item.label + ' pages' : 'Show ' + item.label + ' pages'"
                @click="toggleNavGroup('item:' + item.to)"
              >
                <ChevronDown
                  class="h-3 w-3 transition-transform duration-200"
                  :class="isNavGroupOpen('item:' + item.to, item.children) ? '' : '-rotate-90'"
                  :stroke-width="2"
                />
              </button>
            </div>
            <template v-if="sidebarExpanded && item.children?.length && isNavGroupOpen('item:' + item.to, item.children)">
              <router-link
                v-for="child in item.children"
                :key="'uc-' + child.to"
                :to="child.to"
                class="flex items-center gap-2 rounded-md py-1.5 pr-3 pl-8 text-[11px] font-medium transition-all duration-200"
                :class="isActive(child.to) ? 'bg-accent/15 text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary'"
              >
                <Dot class="h-3.5 w-3.5 flex-shrink-0 -ml-1" :stroke-width="3" />
                <span>{{ child.label }}</span>
              </router-link>
            </template>
          </template>
        </template>
      </template>

      </div>
      <!-- end scrollable nav region -->

      <div class="mx-2 my-2 h-px bg-border-default/50" />

      <a
        href="https://faros.sh/docs/"
        target="_blank"
        rel="noreferrer noopener"
        aria-label="Help — open Faros documentation"
        class="mb-2 flex items-center rounded-md text-text-muted transition-colors hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
        :class="sidebarExpanded ? 'w-full gap-2 px-2.5 py-2' : 'h-8 w-8 justify-center p-0'"
        :title="sidebarExpanded ? undefined : 'Help'"
      >
        <CircleHelp class="h-4 w-4 shrink-0" :stroke-width="1.75" aria-hidden="true" />
        <span v-if="sidebarExpanded" class="text-[11px] font-medium">Help</span>
        <ExternalLink v-if="sidebarExpanded" class="ml-auto h-3 w-3 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
      </a>

      <!-- Identity, access, and the infrequent organization context share one
           account flyout. Workspace remains the separate operating control. -->
      <AccountAccessMenu
        :expanded="sidebarExpanded"
        :show-platform-admin="adminStore.isAdmin === true"
        show-undock
        @cli="showCliModal = true"
        @undock="resetDockPos"
        @logout="handleLogout"
      />
    </aside>

    <!-- HORIZONTAL BAR (top or bottom) -->
    <nav
      v-if="isHorizontalDock"
      ref="dockedRef"
      class="relative z-50 flex w-full flex-shrink-0 items-center gap-1.5 border-border-default bg-surface-raised px-4 py-1.5"
      :class="dockState.mode === 'top' ? 'order-first border-b' : 'order-last border-t'"
    >
      <!-- Drag handle -->
      <div
        class="flex h-7 w-5 cursor-grab items-center justify-center rounded-lg text-text-muted/30 transition-colors hover:text-text-muted"
        @mousedown="onDragStart"
      >
        <GripHorizontal class="h-3 w-3" :stroke-width="2" />
      </div>

      <div class="mx-0.5 h-5 w-px bg-border-default/40" />

      <!-- Logo -->
      <div class="flex items-center gap-1.5 px-1">
        <div class="flex h-6 w-6 items-center justify-center rounded-md border border-border-default bg-surface-overlay">
          <Hexagon class="h-3 w-3 text-accent" :stroke-width="2.5" />
        </div>
        <span class="type-display text-[11px] font-bold tracking-[0.08em] text-text-primary">FAROS</span>
        <div class="flex items-center gap-0.5 rounded-sm border border-success/20 bg-success-subtle px-1.5 py-px">
          <Zap class="h-2 w-2 text-success" :stroke-width="2.5" fill="currentColor" />
          <span class="text-[8px] font-semibold uppercase tracking-widest text-success">Live</span>
        </div>
      </div>

      <div class="mx-0.5 h-5 w-px bg-border-default/40" />

      <!-- Frequent operating context -->
      <WorkspaceSwitcher variant="horizontal" />

      <div class="mx-0.5 h-5 w-px bg-border-default/40" />

      <!-- Nav sections: labels always visible, category chips between
           groups so providers don't all look like Puzzle icons. The
           sections live in their own flex-1 track that scrolls
           horizontally; items are shrink-0 so a long provider list
           overflows into the scroll area instead of compressing every
           link until the labels collide. This track also replaces the
           old flex-1 spacer — it pushes the right-side controls to the
           edge while staying scrollable. -->
      <div class="flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto faros-nav-scroll">
      <template v-for="(section, sIdx) in horizontalNavSections" :key="section.key">
        <div
          v-if="section.label"
          class="ml-1 flex shrink-0 items-center gap-1 rounded-md border border-border-subtle/60 bg-surface-overlay/40 px-1.5 py-0.5"
          :title="section.label"
        >
          <component v-if="section.icon" :is="section.icon" class="h-3 w-3 text-text-muted/80" :stroke-width="2" />
          <span class="text-[8px] font-semibold uppercase tracking-wider text-text-muted/80">
            {{ section.label }}
          </span>
        </div>
        <router-link
          v-for="item in section.items"
          :key="item.to"
          :to="item.to"
          class="flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1 text-[11px] font-medium transition-all duration-200"
          :class="isActive(item.to) ? 'bg-accent/15 text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-muted hover:bg-surface-overlay/40 hover:text-text-secondary'"
          :title="item.label"
        >
          <img v-if="item.iconURL" :src="item.iconURL" alt="" class="h-3.5 w-3.5 shrink-0 object-contain" />
          <component v-else-if="item.icon" :is="item.icon" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
          <Puzzle v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
          <span>{{ item.label }}</span>
        </router-link>
        <div
          v-if="sIdx < horizontalNavSections.length - 1"
          class="mx-0.5 h-4 w-px shrink-0 bg-border-default/30"
        />
      </template>
      </div>

      <!-- Status -->
      <span v-if="tenantStore.activeWorkspace?.clusterName" class="px-1 font-mono text-[9px] tracking-wider text-text-muted">
        {{ tenantStore.activeWorkspace.clusterName }}
      </span>
      <AccountAccessMenu
        :show-platform-admin="adminStore.isAdmin === true"
        show-undock
        @cli="showCliModal = true"
        @undock="resetDockPos"
        @logout="handleLogout"
      />
    </nav>

    <!-- Main content -->
    <main
      :class="mainClass"
      :style="mainStyle"
    >
      <!--
        Keying the slot on auth.clusterName forces the active page to
        unmount + remount when the user switches workspace or org. The
        v0.0.63 fix retargets /graphql/{cluster} so new queries hit the
        right shard, but pages keep displaying the previous workspace's
        payload until the next poll fires (10s+ for MCP/edges), and
        provider micro-frontends carry their own Pinia/URQL caches the
        URL change never invalidates. Unmounting here resets every host
        page's useGraphQLQuery state; ProviderFrame's own watch on
        auth.clusterName re-creates its custom element post-flush so
        the new mountRef div doesn't render empty after the slot
        wrapper rebuilds. The chrome above (sidebar and context switchers)
        stays mounted because the key sits on the slot
        wrapper, not the layout shell.

        When the active org has no workspaces we never get a clusterName,
        so swap the slot out for the create-workspace wizard. A selected
        workspace whose cluster is still provisioning gets a separate pending
        shell; it must never render workspace-scoped content against the old
        auth cluster.
      -->
      <div :key="auth.clusterName ?? 'unauth'" :class="slotClass">
        <div
          v-if="tenantStore.workspaceTransitioning"
          class="flex min-h-52 flex-col items-center justify-center px-4 py-12 text-center"
          aria-busy="true"
        >
          <div v-if="showWorkspaceTransitionIndicator" class="flex flex-col items-center">
            <Loader2 class="h-5 w-5 animate-spin text-accent" :stroke-width="1.75" aria-hidden="true" />
            <p class="mt-3 text-[13px] font-semibold text-text-primary">Switching workspace…</p>
          </div>
        </div>
        <FirstWorkspaceWizard v-else-if="showWorkspaceWizard" />
        <div v-else-if="showWorkspacePending" class="flex min-h-52 flex-col items-center justify-center px-4 py-12 text-center">
          <Loader2 v-if="tenantStore.workspaceLoadState !== 'error'" class="h-5 w-5 animate-spin text-accent" :stroke-width="1.75" aria-hidden="true" />
          <p class="mt-3 text-[13px] font-semibold text-text-primary">
            {{ tenantStore.workspaceLoadState === 'error' ? 'Workspace data is unavailable' : 'Workspace is still provisioning' }}
          </p>
          <p v-if="tenantStore.workspaceLoadState === 'error'" class="mt-1 max-w-sm text-[11px] text-danger">
            {{ tenantStore.error ?? 'The workspace list could not be loaded.' }}
          </p>
          <p v-else class="mt-1 max-w-sm text-[11px] text-text-muted">
            This workspace will become available when its operating cluster is ready. Workspace-scoped tools stay paused until then.
          </p>
          <div class="mt-4 flex flex-wrap items-center justify-center gap-2">
            <button v-if="tenantStore.workspaceLoadState === 'error'" type="button" class="k-btn k-btn--ghost text-[11px]" @click="retryWorkspaceHydration">
              Retry
            </button>
            <router-link to="/settings/workspaces" class="k-btn k-btn--ghost text-[11px]">Manage workspaces</router-link>
          </div>
        </div>
        <slot v-else />
      </div>
    </main>

    <!-- FLOATING MODE (also shown during drag) -->
    <div
      v-if="showFloat"
      ref="floatRef"
      class="fixed z-50 select-none"
      :class="{
        'bottom-4 left-1/2 -translate-x-1/2': isDefaultFloat,
        'cursor-grabbing': isDragging,
      }"
      :style="floatStyle"
    >
      <div class="island flex max-w-[calc(100vw-2rem)] items-center gap-1 rounded-xl px-2 py-1.5">
        <div
          class="island-nav flex h-8 w-5 cursor-grab items-center justify-center rounded-lg text-text-muted/30 transition-colors hover:text-text-muted"
          :class="{ 'cursor-grabbing': isDragging }"
          @mousedown="onDragStart"
        >
          <GripHorizontal class="h-3 w-3" :stroke-width="2" />
        </div>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <div class="flex items-center gap-1.5 px-1.5">
          <div class="flex h-6 w-6 items-center justify-center rounded-md border border-border-default bg-surface-overlay">
            <Hexagon class="h-3 w-3 text-accent" :stroke-width="2.5" />
          </div>
          <span class="type-display text-[11px] font-bold tracking-[0.08em] text-text-primary">FAROS</span>
          <div class="flex items-center gap-0.5 rounded-sm border border-success/20 bg-success-subtle px-1.5 py-px">
            <Zap class="h-2 w-2 text-success" :stroke-width="2.5" fill="currentColor" />
            <span class="text-[8px] font-semibold uppercase tracking-widest text-success">Live</span>
          </div>
        </div>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <WorkspaceSwitcher variant="horizontal" />

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <div class="flex min-w-0 items-center gap-1 overflow-x-auto faros-nav-scroll">
        <template v-for="(section, sIdx) in horizontalNavSections" :key="section.key">
          <div
            v-if="section.label"
            class="ml-1 flex shrink-0 items-center gap-1 rounded-md border border-border-subtle/60 bg-surface-overlay/40 px-1.5 py-0.5"
            :title="section.label"
          >
            <component v-if="section.icon" :is="section.icon" class="h-3 w-3 text-text-muted/80" :stroke-width="2" />
            <span class="text-[8px] font-semibold uppercase tracking-wider text-text-muted/80">
              {{ section.label }}
            </span>
          </div>
          <router-link
            v-for="item in section.items"
            :key="item.to"
            :to="item.to"
            class="island-nav flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1 text-[11px] font-medium transition-all duration-200"
            :class="isActive(item.to) ? 'active bg-accent/15 text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-muted hover:text-text-secondary'"
            :title="item.label"
          >
            <img v-if="item.iconURL" :src="item.iconURL" alt="" class="h-3.5 w-3.5 shrink-0 object-contain" />
            <component v-else-if="item.icon" :is="item.icon" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
            <Puzzle v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
            <span>{{ item.label }}</span>
          </router-link>
          <div
            v-if="sIdx < horizontalNavSections.length - 1"
            class="mx-0.5 h-4 w-px shrink-0 bg-border-default/30"
          />
        </template>
        </div>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <span v-if="tenantStore.activeWorkspace?.clusterName" class="px-1 font-mono text-[9px] tracking-wider text-text-muted">
          {{ tenantStore.activeWorkspace.clusterName }}
        </span>
        <AccountAccessMenu
          :show-platform-admin="adminStore.isAdmin === true"
          :show-undock="hasCustomPos && !isDragging"
          undock-label="Reset position"
          @cli="showCliModal = true"
          @undock="resetDockPos"
          @logout="handleLogout"
        />
      </div>
    </div>

    <!-- CLI quickstart modal -->
    <CliQuickstartModal v-if="showCliModal" @close="showCliModal = false" />

  </div>
</template>

<style scoped>
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.15s ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}

/* Slim, unobtrusive scrollbar for the horizontal provider-nav tracks in
   the top/bottom and floating docks. Without this the default chunky
   scrollbar eats vertical space in the thin bar. */
.faros-nav-scroll {
  scrollbar-width: thin;
  scrollbar-color: var(--color-text-muted) transparent;
}
.faros-nav-scroll::-webkit-scrollbar {
  height: 4px;
}
.faros-nav-scroll::-webkit-scrollbar-track {
  background: transparent;
}
.faros-nav-scroll::-webkit-scrollbar-thumb {
  background-color: var(--color-text-muted);
  border-radius: 2px;
}
</style>
