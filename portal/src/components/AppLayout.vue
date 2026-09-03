<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, watch, watchEffect } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useTerminalSessionsStore } from '@/stores/terminalSessions'
import { useTenantStore } from '@/stores/tenant'
import { useSidebarExpansion } from '@/composables/useSidebarExpansion'
import { useNavigationDock } from '@/composables/useNavigationDock'
import { useDelayedLoading } from '@/portalkit/useDelayedLoading'
import CliQuickstartModal from '@/components/CliQuickstartModal.vue'
import HelpSupportModal from '@/components/HelpSupportModal.vue'
import AccountAccessMenu from '@/components/AccountAccessMenu.vue'
import ProviderNavOverflow from '@/components/ProviderNavOverflow.vue'
import WorkspaceSwitcher from '@/components/WorkspaceSwitcher.vue'
import FirstWorkspaceWizard from '@/components/FirstWorkspaceWizard.vue'
import { Hexagon, LayoutDashboard, GripHorizontal, GripVertical, Puzzle, Dot, PanelLeftClose, PanelLeftOpen, ChevronDown, CircleHelp, Loader2, RefreshCw, CircleAlert } from 'lucide-vue-next'
import { useProvidersStore } from '@/stores/providers'
import { useAdminStore } from '@/stores/admin'
import { categoryIcons, fallbackCategoryIcon } from '@/lib/categoryIcons'
import {
  flattenProviderItems,
  hasActiveNavRoute,
  isActiveRoute,
  isProviderItemActive as isProviderNavItemActive,
  type HorizontalSection,
  type NavItem,
  type ProviderRouteItem,
} from '@/lib/shellNavigation'

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
const showWorkspacePendingIndicator = useDelayedLoading(showWorkspacePending)
const showWorkspacePendingContent = computed(() =>
  tenantStore.workspaceLoadState === 'error' || showWorkspacePendingIndicator.value,
)
const workspacePendingTitle = computed(() => {
  if (tenantStore.workspaceLoadState === 'error') return 'Workspace data is unavailable'
  if (!tenantStore.workspaceSelectionHydrated) return 'Loading workspace…'
  return 'Workspace is still provisioning'
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

onUnmounted(() => {
  clearWorkspaceTransitionTimer()
})

const mainPaddingBottom = computed(() => {
  if (!terminalStore.isVisible || terminalStore.sessions.length === 0) return undefined
  if (terminalStore.panelState.isFullscreen) return undefined
  const h = terminalStore.panelState.isMinimized ? 40 : terminalStore.panelState.height
  return `${h + 16}px`
})

const mainClass = computed(() => [
  'relative z-10 min-h-0 min-w-0 flex-1',
  layoutProps.fullBleed ? 'overflow-hidden p-0' : 'overflow-y-auto px-4 py-5 sm:px-8',
])

const mainStyle = computed(() => {
  if (layoutProps.fullBleed || !mainPaddingBottom.value) return undefined
  return { paddingBottom: mainPaddingBottom.value }
})

const slotClass = computed(() => [
  'relative z-10',
  // Single source of truth for page content width: every non-full-bleed page
  // renders in the SAME fluid full-width column so the layout doesn't shift
  // when navigating. Pages must NOT add their own mx-auto/max-w-* wrapper —
  // that reintroduces per-page width drift. Wide-screen density comes from
  // responsive grid columns inside each page, not from capping the column.
  // Full-bleed provider workbenches opt out and manage their own width.
  layoutProps.fullBleed ? 'h-full min-h-0' : 'w-full',
])

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
const providersHeaderItem: NavItem = { label: 'Providers', to: '/providers', icon: Puzzle, exact: true }

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
const horizontalNavSections = computed<HorizontalSection[]>(() => {
  const sections: HorizontalSection[] = []
  sections.push({ key: 'static', label: null, icon: null, items: [...staticNavItems, providersHeaderItem] })
  const cat = providersStore.categorizedNavItems
  for (const g of cat.groups) {
    sections.push({
      key: 'g-' + g.name,
      label: g.name,
      icon: categoryIcon(g.icon),
      items: flattenProviderItems(g.items),
    })
  }
  if (cat.uncategorized.length) {
    sections.push({
      key: 'uncat',
      label: 'Other',
      icon: fallbackCategoryIcon,
      items: flattenProviderItems(cat.uncategorized),
    })
  }
  return sections
})

// Flat docks keep the platform destinations and the active provider family in
// the primary track. The overflow component owns the inactive provider menu,
// so this shell still has one source of truth for provider sections without
// turning every compact mode into a catalog wall.
const horizontalProviderSections = computed(() =>
  horizontalNavSections.value.filter((section) => section.key !== 'static'),
)

// Bind the browser route to the pure shell-navigation predicates. Keeping
// this adapter local makes the template concise while the matching rules can
// be exercised independently of Vue and router state.
const isActive = (path: string, exact = false) => isActiveRoute(route.path, path, exact)
const isProviderItemActive = (item: ProviderRouteItem) => isProviderNavItemActive(route.path, item)

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
const showHelpModal = ref(false)

// --- Collapsible sidebar rail ---
// The vertical dock defaults to a 56px icon rail so the canvas isn't taxed by
// a permanent 192px label column; labels expand on click and the choice
// persists per browser. Collapsed rows are icon-only with a native title
// tooltip (design-book §6 "Sidebar rail").
const { sidebarExpanded, toggleSidebar } = useSidebarExpansion()

const {
  dockState,
  floatRef,
  dockedRef,
  isDragging,
  nearEdge,
  isVerticalDock,
  isHorizontalDock,
  showFloat,
  isDefaultFloat,
  hasCustomPos,
  floatStyle,
  layoutClass,
  layoutInsetsStyle,
  onDragStart,
  onDragHandleKeydown,
  resetDockPos,
} = useNavigationDock(sidebarExpanded)

const dockHintId = 'faros-dock-hint'
const dockHintText = 'Drag to an edge · Shift+Arrow to dock · Enter to float'
const dockActionLabel = computed(() =>
  dockState.value.mode === 'float' ? 'Reset floating position' : 'Float navigation',
)

// --- Collapsible nav groups (expanded sidebar only) ---
// Category groups and provider sub-nav toggle on click and persist per
// browser. A group holding the active route is always forced open so
// navigation state is never hidden from the user; the stored preference
// takes effect again once they navigate elsewhere.
const NAV_GROUPS_KEY = 'faros-nav-collapsed-groups'

function browserStorage(): Storage | null {
  try {
    if (typeof window === 'undefined') return null
    return window.localStorage
  } catch {
    return null
  }
}

function loadCollapsedGroups(): Record<string, boolean> {
  try {
    const parsed: unknown = JSON.parse(browserStorage()?.getItem(NAV_GROUPS_KEY) || '{}')
    if (parsed && typeof parsed === 'object') return parsed as Record<string, boolean>
  } catch { /* ignore */ }
  return {}
}
const collapsedGroups = ref<Record<string, boolean>>(loadCollapsedGroups())
function toggleNavGroup(key: string) {
  collapsedGroups.value = { ...collapsedGroups.value, [key]: !collapsedGroups.value[key] }
  try {
    browserStorage()?.setItem(NAV_GROUPS_KEY, JSON.stringify(collapsedGroups.value))
  } catch { /* ignore unavailable or quota-limited storage */ }
}
function isNavGroupOpen(key: string, items: ProviderRouteItem[]): boolean {
  if (hasActiveNavRoute(route.path, items)) return true
  return !collapsedGroups.value[key]
}
function navGroupPanelId(key: string): string {
  return `faros-nav-group-${key.replace(/[^a-zA-Z0-9_-]+/g, '-')}`
}

const providerBindingState = computed(() => providersStore.bindingsLoadState)
const providerBindingsStale = computed(() => providersStore.bindingsStale)
const providerBindingError = computed(() => providersStore.bindingsError)
const providerBindingStatusVisible = computed(() =>
  providerBindingState.value === 'loading'
  || providerBindingState.value === 'error'
  || providerBindingsStale.value,
)
const providerBindingRetryable = computed(() => providerBindingState.value === 'error')
const providerBindingStatusLabel = computed(() => {
  if (providerBindingState.value === 'loading') return 'Refreshing provider access'
  if (providerBindingsStale.value) return 'Provider access needs refresh'
  return 'Provider access unavailable'
})

async function retryProviderBindings(): Promise<void> {
  if (providerBindingState.value === 'loading') return
  try {
    await providersStore.refreshBindings()
  } catch {
    // The store owns the visible error state; the shell keeps the retry action
    // usable without creating an unhandled rejection.
  }
}

interface ContextStatus {
  label: string
  live: boolean
  visible: boolean
  dotClass: string
  textClass: string
}

// Refresh hydration is visually quiet: a persisted workspace ID with no loaded
// row is unknown, not Pending. Once the workspace list is authoritative, the
// shell names a confirmed provisioning or unavailable state. "Live" remains
// reserved for a selected workspace whose backing cluster is known to be usable.
const contextStatus = computed<ContextStatus>(() => {
  if (tenantStore.workspaceTransitioning) {
    return { label: 'Switching', live: false, visible: true, dotClass: 'bg-warning', textClass: 'text-warning' }
  }
  if (!tenantStore.orgUUID) {
    return { label: 'Choose organization', live: false, visible: true, dotClass: 'bg-text-secondary', textClass: 'text-text-secondary' }
  }
  if (tenantStore.workspaceMode === 'organization' || !tenantStore.workspaceUUID) {
    return { label: 'Organization', live: false, visible: true, dotClass: 'bg-accent', textClass: 'text-accent' }
  }
  if (tenantStore.workspaceLoadState === 'error') {
    return { label: 'Unavailable', live: false, visible: true, dotClass: 'bg-danger', textClass: 'text-danger' }
  }
  if (tenantStore.workspaceLoadState === 'idle' || tenantStore.workspaceLoadState === 'loading') {
    return { label: 'Loading workspace', live: false, visible: false, dotClass: 'bg-text-secondary', textClass: 'text-text-secondary' }
  }
  if (tenantStore.activeWorkspace?.clusterName) {
    return { label: 'Workspace live', live: true, visible: false, dotClass: 'bg-success live-dot', textClass: 'text-success' }
  }
  if (tenantStore.activeWorkspace) {
    return { label: 'Provisioning', live: false, visible: true, dotClass: 'bg-warning', textClass: 'text-warning' }
  }
  return { label: 'Unavailable', live: false, visible: true, dotClass: 'bg-danger', textClass: 'text-danger' }
})

// Dock state, pointer/keyboard movement, persistence, and layout-inset
// publication live in useNavigationDock so this component only composes the
// resulting state into its three visual shell variants.

</script>

<template>
  <div class="relative flex h-screen bg-surface" :class="layoutClass" :style="layoutInsetsStyle">
    <span :id="dockHintId" class="sr-only">{{ dockHintText }}</span>
    <!-- Edge snap hint overlays -->
    <Transition name="fade">
      <div v-if="nearEdge === 'left'" class="fixed inset-y-0 left-0 z-[60] w-48 rounded-r-xl bg-accent/10 border-r border-accent/40" />
    </Transition>
    <Transition name="fade">
      <div v-if="nearEdge === 'right'" class="fixed inset-y-0 right-0 z-[60] w-48 rounded-l-xl bg-accent/10 border-l border-accent/40" />
    </Transition>
    <Transition name="fade">
      <div v-if="nearEdge === 'top'" class="fixed inset-x-0 top-0 z-[60] h-11 rounded-b-xl bg-accent/10 border-b border-accent/40" />
    </Transition>
    <Transition name="fade">
      <div v-if="nearEdge === 'bottom'" class="fixed inset-x-0 bottom-0 z-[60] h-11 rounded-t-xl bg-accent/10 border-t border-accent/40" />
    </Transition>

    <!-- VERTICAL SIDEBAR (left or right) -->
    <aside
      v-if="isVerticalDock"
      ref="dockedRef"
      class="relative z-50 flex h-full flex-shrink-0 flex-col overflow-hidden border-border-default bg-surface-raised py-3 px-2 transition-[width] duration-200"
      :class="[dockState.mode === 'left' ? 'order-first border-r' : 'order-last border-l', sidebarExpanded ? 'w-48' : 'w-14']"
    >
      <!-- Expanded rail header: brand identity owns the first row, while
           the dock grip remains its leading control. Exceptional context
           states get a quiet, separate line below; healthy workspace
           readiness is already communicated by WorkspaceSwitcher. -->
      <template v-if="sidebarExpanded">
        <div class="shell-vertical-brand-row mb-1 flex min-w-0 items-center gap-2 px-2">
          <button
            type="button"
            class="shell-drag-handle flex h-6 w-6 shrink-0 touch-none cursor-grab items-center justify-center rounded-lg border-0 bg-transparent p-0 text-text-secondary transition-colors hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
            aria-label="Move navigation dock"
            :aria-describedby="dockHintId"
            :aria-keyshortcuts="'ArrowLeft ArrowRight ArrowUp ArrowDown Shift+ArrowLeft Shift+ArrowRight Shift+ArrowUp Shift+ArrowDown Enter Space Home End PageUp PageDown'"
            :title="dockHintText"
            @pointerdown="onDragStart"
            @keydown="onDragHandleKeydown"
          >
            <GripVertical class="h-3 w-3" :stroke-width="2" />
          </button>
          <div class="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg border border-border-default bg-surface-overlay">
            <Hexagon class="h-3.5 w-3.5 text-accent" :stroke-width="2" />
          </div>
          <span class="shell-brand-name type-display min-w-0 truncate text-[11px] font-bold tracking-[0.08em] text-text-primary">FAROS</span>
          <button
            type="button"
            class="shell-sidebar-toggle k-btn k-btn--ghost ml-auto flex h-6 w-6 shrink-0 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-secondary transition-colors hover:bg-surface-overlay/50 hover:text-text-primary"
            aria-label="Collapse sidebar"
            title="Collapse sidebar"
            @click="toggleSidebar"
          >
            <PanelLeftClose class="h-3.5 w-3.5" :stroke-width="1.75" />
          </button>
        </div>
        <div v-if="contextStatus.visible" class="shell-vertical-ops-row mb-1 flex min-w-0 items-center px-2">
          <span
            class="shell-context-status shell-context-status--vertical flex min-w-0 flex-1 items-center gap-1 px-0 py-0"
            :class="contextStatus.textClass"
            role="status"
            aria-live="polite"
            :title="contextStatus.label"
          >
            <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="contextStatus.dotClass" aria-hidden="true" />
            <span class="min-w-0 truncate text-[10px] font-semibold uppercase tracking-widest">{{ contextStatus.label }}</span>
          </span>
        </div>
      </template>

      <!-- Collapsed rail: retain all four compact controls, with the context
           status announced to assistive technology only for exceptional
           states; healthy readiness stays with WorkspaceSwitcher. -->
      <div v-else class="shell-vertical-collapsed-header mb-1 flex flex-col items-center gap-1.5 px-0">
        <button
          type="button"
          class="shell-drag-handle flex h-6 w-6 touch-none cursor-grab items-center justify-center rounded-lg border-0 bg-transparent p-0 text-text-secondary transition-colors hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          aria-label="Move navigation dock"
          :aria-describedby="dockHintId"
          :aria-keyshortcuts="'ArrowLeft ArrowRight ArrowUp ArrowDown Shift+ArrowLeft Shift+ArrowRight Shift+ArrowUp Shift+ArrowDown Enter Space Home End PageUp PageDown'"
          :title="dockHintText"
          @pointerdown="onDragStart"
          @keydown="onDragHandleKeydown"
        >
          <GripVertical class="h-3 w-3" :stroke-width="2" />
        </button>
        <div class="flex h-7 w-7 items-center justify-center rounded-lg border border-border-default bg-surface-overlay">
          <Hexagon class="h-3.5 w-3.5 text-accent" :stroke-width="2" />
        </div>
        <button
          type="button"
          class="shell-sidebar-toggle k-btn k-btn--ghost flex h-6 w-6 shrink-0 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-secondary transition-colors hover:bg-surface-overlay/50 hover:text-text-primary"
          aria-label="Expand sidebar"
          title="Expand sidebar"
          @click="toggleSidebar"
        >
          <PanelLeftOpen class="h-3.5 w-3.5" :stroke-width="1.75" />
        </button>
        <span
          v-if="contextStatus.visible"
          class="shell-context-status flex h-4 w-4 items-center justify-center rounded-sm p-0"
          :class="contextStatus.textClass"
          role="status"
          aria-live="polite"
          :title="contextStatus.label"
        >
          <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="contextStatus.dotClass" aria-hidden="true" />
          <span class="sr-only">{{ contextStatus.label }}</span>
        </span>
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
      <nav aria-label="Primary navigation" class="-mr-1 flex min-h-0 flex-1 flex-col overflow-y-auto pr-1">
      <!-- Static nav items (Dashboard, Providers) -->
      <router-link
        v-for="item in staticNavItems"
        :key="item.to"
        :to="item.to"
        class="shell-nav-link flex items-center gap-2.5 rounded-md px-3 py-2 text-[11px] font-medium transition-colors duration-200"
        :class="[isActive(item.to, item.exact) ? 'bg-accent-subtle text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-secondary hover:bg-surface-overlay/50 hover:text-text-primary', sidebarExpanded ? '' : 'justify-center']"
        :title="sidebarExpanded ? undefined : item.label"
        :aria-label="sidebarExpanded ? undefined : item.label"
        :aria-current="isActive(item.to, item.exact) ? 'page' : undefined"
      >
        <component :is="item.icon" class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
        <span v-if="sidebarExpanded">{{ item.label }}</span>
      </router-link>

      <!-- Providers catalog is the primary destination immediately below
           Dashboard; provider categories and their entries follow it. -->
      <router-link
        :to="providersHeaderItem.to"
        class="shell-nav-link flex items-center gap-2.5 rounded-md px-3 py-2 text-[11px] font-medium transition-colors duration-200"
        :class="[isActive(providersHeaderItem.to, true) ? 'bg-accent-subtle text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-secondary hover:bg-surface-overlay/50 hover:text-text-primary', sidebarExpanded ? '' : 'justify-center']"
        :title="sidebarExpanded ? undefined : providersHeaderItem.label"
        :aria-label="sidebarExpanded ? undefined : providersHeaderItem.label"
        :aria-current="isActive(providersHeaderItem.to, true) ? 'page' : undefined"
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
          class="shell-nav-group-toggle k-btn k-btn--text mt-3 mb-1 flex w-full items-center justify-start gap-2 px-3 py-0 text-left"
          :title="isNavGroupOpen('cat:' + group.name, group.items) ? 'Collapse ' + group.name : 'Expand ' + group.name"
          :aria-expanded="isNavGroupOpen('cat:' + group.name, group.items)"
          :aria-controls="navGroupPanelId('cat:' + group.name)"
          @click="toggleNavGroup('cat:' + group.name)"
        >
          <component :is="categoryIcon(group.icon)" class="h-3 w-3 flex-shrink-0 text-text-secondary/80" :stroke-width="2" />
          <span class="text-[9px] font-semibold uppercase tracking-wider text-text-secondary/80">{{ group.name }}</span>
          <div class="h-px flex-1 bg-border-default/40" />
          <ChevronDown
            class="h-3 w-3 flex-shrink-0 text-text-secondary/80 transition-transform duration-200"
            :class="isNavGroupOpen('cat:' + group.name, group.items) ? '' : '-rotate-90'"
            :stroke-width="2"
          />
        </button>
        <div
          v-else
          class="shell-nav-category-cue mx-1 mt-3 mb-1 flex h-5 items-center justify-center rounded-sm border border-border-subtle/60 bg-surface-overlay/30 text-text-secondary"
          role="group"
          :aria-label="`Category: ${group.name}`"
          :title="`Category: ${group.name}`"
        >
          <component :is="categoryIcon(group.icon)" class="h-3 w-3" :stroke-width="2" aria-hidden="true" />
          <span class="sr-only">Category: {{ group.name }}</span>
        </div>
        <div
          :id="navGroupPanelId('cat:' + group.name)"
          :hidden="sidebarExpanded && !isNavGroupOpen('cat:' + group.name, group.items)"
        >
          <template v-for="item in group.items" :key="item.to">
            <div
              class="group/nav flex items-center gap-2.5 rounded-md px-3 py-1.5 text-[11px] font-medium transition-colors duration-200"
              :class="[isProviderItemActive(item) ? 'bg-accent-subtle text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-secondary hover:bg-surface-overlay/50 hover:text-text-primary', sidebarExpanded ? '' : 'justify-center']"
              :title="sidebarExpanded ? undefined : item.label"
            >
              <router-link
                :to="item.to"
                class="shell-nav-link flex min-w-0 flex-1 items-center gap-2.5"
                :class="sidebarExpanded ? '' : 'justify-center'"
                :aria-label="sidebarExpanded ? undefined : item.label"
                :aria-current="isProviderItemActive(item) ? 'page' : undefined"
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
                class="shell-nav-group-toggle k-btn k-btn--text -mr-1 flex h-4 w-4 flex-shrink-0 items-center justify-center rounded-sm p-0 text-text-secondary hover:text-text-primary"
                :title="isNavGroupOpen('item:' + item.to, item.children) ? 'Hide ' + item.label + ' pages' : 'Show ' + item.label + ' pages'"
                :aria-expanded="isNavGroupOpen('item:' + item.to, item.children)"
                :aria-controls="navGroupPanelId('item:' + item.to)"
                @click="toggleNavGroup('item:' + item.to)"
              >
                <ChevronDown
                  class="h-3 w-3 transition-transform duration-200"
                  :class="isNavGroupOpen('item:' + item.to, item.children) ? '' : '-rotate-90'"
                  :stroke-width="2"
                />
              </button>
            </div>
            <div
              v-if="item.children?.length"
              :id="navGroupPanelId('item:' + item.to)"
              :hidden="!sidebarExpanded || !isNavGroupOpen('item:' + item.to, item.children)"
            >
              <router-link
                v-for="child in item.children"
                :key="'c-' + child.to"
                :to="child.to"
                class="shell-nav-link flex items-center gap-2 rounded-md py-1.5 pr-3 pl-8 text-[11px] font-medium transition-colors duration-200"
                :class="isActive(child.to) ? 'bg-accent-subtle text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-secondary hover:bg-surface-overlay/50 hover:text-text-primary'"
                :aria-current="isActive(child.to) ? 'page' : undefined"
              >
                <Dot class="h-3.5 w-3.5 flex-shrink-0 -ml-1" :stroke-width="3" />
                <span>{{ child.label }}</span>
              </router-link>
            </div>
          </template>
        </div>
      </template>

      <!-- Uncategorized providers (third-party with no spec.category) sit
           under their own divider so the rhythm of the sidebar stays
           consistent. -->
      <template v-if="providersStore.categorizedNavItems.uncategorized.length">
        <button
          v-if="sidebarExpanded"
          type="button"
          class="shell-nav-group-toggle k-btn k-btn--text mt-3 mb-1 flex w-full items-center justify-start gap-2 px-3 py-0 text-left"
          :title="isNavGroupOpen('uncat', providersStore.categorizedNavItems.uncategorized) ? 'Collapse Other' : 'Expand Other'"
          :aria-expanded="isNavGroupOpen('uncat', providersStore.categorizedNavItems.uncategorized)"
          :aria-controls="navGroupPanelId('uncat')"
          @click="toggleNavGroup('uncat')"
        >
          <Puzzle class="h-3 w-3 flex-shrink-0 text-text-secondary/80" :stroke-width="2" />
          <span class="text-[9px] font-semibold uppercase tracking-wider text-text-secondary/80">Other</span>
          <div class="h-px flex-1 bg-border-default/40" />
          <ChevronDown
            class="h-3 w-3 flex-shrink-0 text-text-secondary/80 transition-transform duration-200"
            :class="isNavGroupOpen('uncat', providersStore.categorizedNavItems.uncategorized) ? '' : '-rotate-90'"
            :stroke-width="2"
          />
        </button>
        <div
          v-else
          class="shell-nav-category-cue mx-1 mt-3 mb-1 flex h-5 items-center justify-center rounded-sm border border-border-subtle/60 bg-surface-overlay/30 text-text-secondary"
          role="group"
          aria-label="Category: Other"
          title="Category: Other"
        >
          <Puzzle class="h-3 w-3" :stroke-width="2" aria-hidden="true" />
          <span class="sr-only">Category: Other</span>
        </div>
        <div
          :id="navGroupPanelId('uncat')"
          :hidden="sidebarExpanded && !isNavGroupOpen('uncat', providersStore.categorizedNavItems.uncategorized)"
        >
          <template v-for="item in providersStore.categorizedNavItems.uncategorized" :key="'u-' + item.to">
            <div
              class="flex items-center gap-2.5 rounded-md px-3 py-1.5 text-[11px] font-medium transition-colors duration-200"
              :class="[isProviderItemActive(item) ? 'bg-accent-subtle text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-secondary hover:bg-surface-overlay/50 hover:text-text-primary', sidebarExpanded ? '' : 'justify-center']"
              :title="sidebarExpanded ? undefined : item.label"
            >
              <router-link
                :to="item.to"
                class="shell-nav-link flex min-w-0 flex-1 items-center gap-2.5"
                :class="sidebarExpanded ? '' : 'justify-center'"
                :aria-label="sidebarExpanded ? undefined : item.label"
                :aria-current="isProviderItemActive(item) ? 'page' : undefined"
              >
                <img v-if="item.iconURL" :src="item.iconURL" alt="" class="h-3.5 w-3.5 flex-shrink-0 object-contain" />
                <Puzzle v-else class="h-3.5 w-3.5 flex-shrink-0" :stroke-width="1.75" />
                <span v-if="sidebarExpanded" class="min-w-0 flex-1 truncate">{{ item.label }}</span>
              </router-link>
              <button
                v-if="sidebarExpanded && item.children?.length"
                type="button"
                class="shell-nav-group-toggle k-btn k-btn--text -mr-1 flex h-4 w-4 flex-shrink-0 items-center justify-center rounded-sm p-0 text-text-secondary hover:text-text-primary"
                :title="isNavGroupOpen('item:' + item.to, item.children) ? 'Hide ' + item.label + ' pages' : 'Show ' + item.label + ' pages'"
                :aria-expanded="isNavGroupOpen('item:' + item.to, item.children)"
                :aria-controls="navGroupPanelId('item:' + item.to)"
                @click="toggleNavGroup('item:' + item.to)"
              >
                <ChevronDown
                  class="h-3 w-3 transition-transform duration-200"
                  :class="isNavGroupOpen('item:' + item.to, item.children) ? '' : '-rotate-90'"
                  :stroke-width="2"
                />
              </button>
            </div>
            <div
              v-if="item.children?.length"
              :id="navGroupPanelId('item:' + item.to)"
              :hidden="!sidebarExpanded || !isNavGroupOpen('item:' + item.to, item.children)"
            >
              <router-link
                v-for="child in item.children"
                :key="'uc-' + child.to"
                :to="child.to"
                class="shell-nav-link flex items-center gap-2 rounded-md py-1.5 pr-3 pl-8 text-[11px] font-medium transition-colors duration-200"
                :class="isActive(child.to) ? 'bg-accent-subtle text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-secondary hover:bg-surface-overlay/50 hover:text-text-primary'"
                :aria-current="isActive(child.to) ? 'page' : undefined"
              >
                <Dot class="h-3.5 w-3.5 flex-shrink-0 -ml-1" :stroke-width="3" />
                <span>{{ child.label }}</span>
              </router-link>
            </div>
          </template>
        </div>
      </template>

      <div
        v-if="providerBindingStatusVisible"
        class="shell-binding-status mx-1 mt-3 rounded-md border border-border-subtle bg-surface-overlay px-2 py-2 text-[10px] text-text-secondary"
        role="status"
        aria-live="polite"
        :title="providerBindingError || providerBindingStatusLabel"
      >
        <div class="flex items-center gap-1.5">
          <CircleAlert v-if="providerBindingRetryable" class="h-3 w-3 shrink-0 text-danger" :stroke-width="1.75" aria-hidden="true" />
          <RefreshCw v-else class="h-3 w-3 shrink-0 text-accent" :class="providerBindingState === 'loading' ? 'animate-spin' : ''" :stroke-width="1.75" aria-hidden="true" />
          <span v-if="sidebarExpanded" class="min-w-0 flex-1 truncate">{{ providerBindingStatusLabel }}</span>
          <span v-else class="sr-only">{{ providerBindingStatusLabel }}</span>
          <button
            type="button"
            class="shell-status-action k-btn k-btn--text flex h-5 w-5 shrink-0 items-center justify-center rounded-sm p-0 text-text-secondary hover:text-text-primary"
            :disabled="providerBindingState === 'loading'"
            :aria-label="providerBindingState === 'loading' ? 'Refreshing provider access' : 'Retry provider access'"
            :title="providerBindingState === 'loading' ? 'Refreshing provider access' : 'Retry provider access'"
            @click="retryProviderBindings"
          >
            <RefreshCw class="h-3 w-3" :class="providerBindingState === 'loading' ? 'animate-spin' : ''" :stroke-width="1.75" aria-hidden="true" />
            <span v-if="sidebarExpanded" class="sr-only">{{ providerBindingState === 'loading' ? 'Refreshing' : 'Retry' }}</span>
          </button>
        </div>
        <p v-if="sidebarExpanded && providerBindingError" class="mt-1 text-[10px] text-danger">{{ providerBindingError }}</p>
      </div>

      </nav>
      <!-- end scrollable nav region -->

      <div class="mx-2 my-2 h-px bg-border-default/50" />

      <button
        type="button"
        aria-label="Open help and community"
        aria-haspopup="dialog"
        aria-controls="help-support-dialog"
        :aria-expanded="showHelpModal"
        class="shell-help mb-2 flex items-center rounded-md text-text-secondary transition-colors hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
        :class="sidebarExpanded ? 'w-full gap-2 px-2.5 py-2' : 'h-8 w-8 justify-center p-0'"
        :title="sidebarExpanded ? undefined : 'Help'"
        @click="showHelpModal = true"
      >
        <CircleHelp class="h-4 w-4 shrink-0" :stroke-width="1.75" aria-hidden="true" />
        <span v-if="sidebarExpanded" class="text-[11px] font-medium">Help</span>
      </button>

      <!-- Identity, access, and the infrequent organization context share one
           account flyout. Workspace remains the separate operating control. -->
      <AccountAccessMenu
        :expanded="sidebarExpanded"
        :show-platform-admin="adminStore.isAdmin === true"
        :show-undock="dockState.mode !== 'float'"
        :undock-label="dockActionLabel"
        @cli="showCliModal = true"
        @undock="resetDockPos"
        @logout="handleLogout"
      />
    </aside>

    <!-- HORIZONTAL BAR (top or bottom) -->
    <nav
      v-if="isHorizontalDock"
      ref="dockedRef"
      aria-label="Primary navigation"
      class="faros-shell-horizontal relative z-50 flex min-w-0 w-full flex-shrink-0 items-center gap-1.5 overflow-hidden border-border-default bg-surface-raised px-4 py-1.5"
      :class="dockState.mode === 'top' ? 'order-first border-b' : 'order-last border-t'"
    >
      <!-- Drag handle -->
      <button
        type="button"
        class="shell-drag-handle flex h-7 w-5 touch-none cursor-grab items-center justify-center rounded-lg border-0 bg-transparent p-0 text-text-secondary transition-colors hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
        aria-label="Move navigation dock"
        :aria-describedby="dockHintId"
        :aria-keyshortcuts="'ArrowLeft ArrowRight ArrowUp ArrowDown Shift+ArrowLeft Shift+ArrowRight Shift+ArrowUp Shift+ArrowDown Enter Space Home End PageUp PageDown'"
        :title="dockHintText"
        @pointerdown="onDragStart"
        @keydown="onDragHandleKeydown"
      >
        <GripHorizontal class="h-3 w-3" :stroke-width="2" />
      </button>

      <div class="mx-0.5 h-5 w-px bg-border-default/40" />

      <!-- Logo -->
      <div class="shell-brand flex shrink-0 items-center gap-1.5 px-1">
        <div class="flex h-6 w-6 items-center justify-center rounded-md border border-border-default bg-surface-overlay">
          <Hexagon class="h-3 w-3 text-accent" :stroke-width="2.5" />
        </div>
        <span class="shell-brand-name type-display text-[11px] font-bold tracking-[0.08em] text-text-primary">FAROS</span>
        <div
          v-if="contextStatus.visible"
          class="shell-context-status flex items-center gap-0.5 rounded-sm border border-border-subtle bg-surface-overlay px-1.5 py-px"
          :class="contextStatus.textClass"
          role="status"
          aria-live="polite"
          :title="tenantStore.activeWorkspace?.clusterName || contextStatus.label"
        >
          <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="contextStatus.dotClass" aria-hidden="true" />
          <span class="text-[10px] font-semibold uppercase tracking-widest">{{ contextStatus.label }}</span>
        </div>
      </div>

      <div class="mx-0.5 h-5 w-px bg-border-default/40" />

      <!-- Frequent operating context -->
      <WorkspaceSwitcher variant="horizontal" />

      <div class="mx-0.5 h-5 w-px bg-border-default/40" />

      <!-- Keep platform destinations stable while the provider component
           exposes only the active family inline and puts inactive families in
           its categorized More/Browse menu. -->
      <div class="shell-route-track flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto faros-nav-scroll">
        <router-link
          v-for="item in staticNavItems"
          :key="item.to"
          :to="item.to"
          class="shell-nav-link flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1 text-[11px] font-medium transition-colors duration-200"
          :class="isActive(item.to, item.exact) ? 'bg-accent-subtle text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-secondary hover:bg-surface-overlay/40 hover:text-text-primary'"
          :title="item.label"
          :aria-label="item.label"
          :aria-current="isActive(item.to, item.exact) ? 'page' : undefined"
        >
          <component :is="item.icon" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
          <span>{{ item.label }}</span>
        </router-link>
        <router-link
          :to="providersHeaderItem.to"
          class="shell-nav-link flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1 text-[11px] font-medium transition-colors duration-200"
          :class="isActive(providersHeaderItem.to, true) ? 'bg-accent-subtle text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-secondary hover:bg-surface-overlay/40 hover:text-text-primary'"
          :title="providersHeaderItem.label"
          :aria-label="providersHeaderItem.label"
          :aria-current="isActive(providersHeaderItem.to, true) ? 'page' : undefined"
        >
          <Puzzle class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
          <span>{{ providersHeaderItem.label }}</span>
        </router-link>
        <div v-if="horizontalProviderSections.length" class="mx-0.5 h-5 w-px shrink-0 bg-border-default/40" aria-hidden="true" />
        <ProviderNavOverflow :sections="horizontalProviderSections" />
      </div>

      <div
        v-if="providerBindingStatusVisible"
        class="shell-binding-status flex shrink-0 items-center gap-1 rounded-md border border-border-subtle bg-surface-overlay px-1.5 py-1 text-[10px] text-text-secondary"
        role="status"
        aria-live="polite"
        :title="providerBindingError || providerBindingStatusLabel"
      >
        <CircleAlert v-if="providerBindingRetryable" class="h-3 w-3 shrink-0 text-danger" :stroke-width="1.75" aria-hidden="true" />
        <RefreshCw v-else class="h-3 w-3 shrink-0 text-accent" :class="providerBindingState === 'loading' ? 'animate-spin' : ''" :stroke-width="1.75" aria-hidden="true" />
        <span class="hidden xl:inline">{{ providerBindingStatusLabel }}</span>
        <button
          type="button"
          class="shell-status-action k-btn k-btn--text flex h-5 w-5 shrink-0 items-center justify-center rounded-sm p-0 text-text-secondary hover:text-text-primary"
          :disabled="providerBindingState === 'loading'"
          :aria-label="providerBindingState === 'loading' ? 'Refreshing provider access' : 'Retry provider access'"
          :title="providerBindingState === 'loading' ? 'Refreshing provider access' : 'Retry provider access'"
          @click="retryProviderBindings"
        >
          <RefreshCw class="h-3 w-3" :class="providerBindingState === 'loading' ? 'animate-spin' : ''" :stroke-width="1.75" aria-hidden="true" />
        </button>
      </div>
      <button
        type="button"
        aria-label="Open help and community"
        aria-haspopup="dialog"
        aria-controls="help-support-dialog"
        :aria-expanded="showHelpModal"
        class="shell-help flex shrink-0 items-center gap-1 rounded-md px-1.5 py-1 text-text-secondary transition-colors hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
        title="Help"
        @click="showHelpModal = true"
      >
        <CircleHelp class="h-4 w-4 shrink-0" :stroke-width="1.75" aria-hidden="true" />
        <span class="hidden xl:inline text-[10px] font-medium">Help</span>
      </button>
      <AccountAccessMenu
        :show-platform-admin="adminStore.isAdmin === true"
        :show-undock="dockState.mode !== 'float'"
        :undock-label="dockActionLabel"
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
        <div v-else-if="showWorkspacePending" class="flex min-h-52 flex-col items-center justify-center px-4 py-12 text-center" aria-busy="true">
          <div v-if="showWorkspacePendingContent" class="k-delayed-loading flex flex-col items-center" role="status" aria-live="polite">
            <Loader2 v-if="tenantStore.workspaceLoadState !== 'error'" class="h-5 w-5 animate-spin text-accent" :stroke-width="1.75" aria-hidden="true" />
            <p class="mt-3 text-[13px] font-semibold text-text-primary">{{ workspacePendingTitle }}</p>
            <p v-if="tenantStore.workspaceLoadState === 'error'" class="mt-1 max-w-sm text-[11px] text-danger">
              {{ tenantStore.error ?? 'The workspace list could not be loaded.' }}
            </p>
            <p v-else-if="tenantStore.workspaceSelectionHydrated" class="mt-1 max-w-sm text-[11px] text-text-secondary">
              This workspace will become available when its operating cluster is ready. Workspace-scoped tools stay paused until then.
            </p>
            <div class="mt-4 flex flex-wrap items-center justify-center gap-2">
              <button v-if="tenantStore.workspaceLoadState === 'error'" type="button" class="k-btn k-btn--ghost text-[11px]" @click="retryWorkspaceHydration">
                Retry
              </button>
              <router-link to="/settings/workspaces" class="k-btn k-btn--ghost text-[11px]">Manage workspaces</router-link>
            </div>
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
      <div role="navigation" aria-label="Primary navigation" class="shell-floating-chrome island flex min-w-0 max-w-[calc(100vw-2rem)] items-center gap-1 overflow-hidden rounded-xl px-2 py-1.5">
        <button
          type="button"
          class="shell-drag-handle island-nav flex h-8 w-5 shrink-0 touch-none cursor-grab items-center justify-center rounded-lg border-0 bg-transparent p-0 text-text-secondary transition-colors hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          :class="{ 'cursor-grabbing': isDragging }"
          aria-label="Move navigation dock"
          :aria-describedby="dockHintId"
          :aria-keyshortcuts="'ArrowLeft ArrowRight ArrowUp ArrowDown Shift+ArrowLeft Shift+ArrowRight Shift+ArrowUp Shift+ArrowDown Enter Space Home End PageUp PageDown'"
          :title="dockHintText"
          @pointerdown="onDragStart"
          @keydown="onDragHandleKeydown"
        >
          <GripHorizontal class="h-3 w-3" :stroke-width="2" />
        </button>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <div class="shell-brand flex shrink-0 items-center gap-1.5 px-1.5">
          <div class="flex h-6 w-6 items-center justify-center rounded-md border border-border-default bg-surface-overlay">
            <Hexagon class="h-3 w-3 text-accent" :stroke-width="2.5" />
          </div>
          <span class="shell-brand-name type-display text-[11px] font-bold tracking-[0.08em] text-text-primary">FAROS</span>
          <div
            v-if="contextStatus.visible"
            class="shell-context-status flex shrink-0 items-center gap-0.5 rounded-sm border border-border-subtle bg-surface-overlay px-1.5 py-px"
            :class="contextStatus.textClass"
            role="status"
            aria-live="polite"
            :title="tenantStore.activeWorkspace?.clusterName || contextStatus.label"
          >
            <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="contextStatus.dotClass" aria-hidden="true" />
            <span class="text-[10px] font-semibold uppercase tracking-widest">{{ contextStatus.label }}</span>
          </div>
        </div>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <WorkspaceSwitcher variant="horizontal" />

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <div class="shell-route-track flex min-w-0 items-center gap-1 overflow-x-auto faros-nav-scroll">
          <router-link
            v-for="item in staticNavItems"
            :key="item.to"
            :to="item.to"
            class="shell-nav-link island-nav flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1 text-[11px] font-medium transition-colors duration-200"
            :class="isActive(item.to, item.exact) ? 'active bg-accent-subtle text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-secondary hover:bg-surface-overlay/40 hover:text-text-primary'"
            :title="item.label"
            :aria-label="item.label"
            :aria-current="isActive(item.to, item.exact) ? 'page' : undefined"
          >
            <component :is="item.icon" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
            <span>{{ item.label }}</span>
          </router-link>
          <router-link
            :to="providersHeaderItem.to"
            class="shell-nav-link island-nav flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1 text-[11px] font-medium transition-colors duration-200"
            :class="isActive(providersHeaderItem.to, true) ? 'active bg-accent-subtle text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-secondary hover:bg-surface-overlay/40 hover:text-text-primary'"
            :title="providersHeaderItem.label"
            :aria-label="providersHeaderItem.label"
            :aria-current="isActive(providersHeaderItem.to, true) ? 'page' : undefined"
          >
            <Puzzle class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
            <span>{{ providersHeaderItem.label }}</span>
          </router-link>
          <div v-if="horizontalProviderSections.length" class="mx-0.5 h-5 w-px shrink-0 bg-border-default/40" aria-hidden="true" />
          <ProviderNavOverflow :sections="horizontalProviderSections" />
        </div>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <div
          v-if="providerBindingStatusVisible"
          class="shell-binding-status flex shrink-0 items-center gap-1 rounded-md border border-border-subtle bg-surface-overlay px-1.5 py-1 text-[10px] text-text-secondary"
          role="status"
          aria-live="polite"
          :title="providerBindingError || providerBindingStatusLabel"
        >
          <CircleAlert v-if="providerBindingRetryable" class="h-3 w-3 shrink-0 text-danger" :stroke-width="1.75" aria-hidden="true" />
          <RefreshCw v-else class="h-3 w-3 shrink-0 text-accent" :class="providerBindingState === 'loading' ? 'animate-spin' : ''" :stroke-width="1.75" aria-hidden="true" />
          <span class="hidden 2xl:inline">{{ providerBindingStatusLabel }}</span>
          <button
            type="button"
            class="shell-status-action k-btn k-btn--text flex h-5 w-5 shrink-0 items-center justify-center rounded-sm p-0 text-text-secondary hover:text-text-primary"
            :disabled="providerBindingState === 'loading'"
            :aria-label="providerBindingState === 'loading' ? 'Refreshing provider access' : 'Retry provider access'"
            :title="providerBindingState === 'loading' ? 'Refreshing provider access' : 'Retry provider access'"
            @click="retryProviderBindings"
          >
            <RefreshCw class="h-3 w-3" :class="providerBindingState === 'loading' ? 'animate-spin' : ''" :stroke-width="1.75" aria-hidden="true" />
          </button>
        </div>
        <button
          type="button"
          aria-label="Open help and community"
          aria-haspopup="dialog"
          aria-controls="help-support-dialog"
          :aria-expanded="showHelpModal"
          class="shell-help flex shrink-0 items-center gap-1 rounded-md px-1.5 py-1 text-text-secondary transition-colors hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
          title="Help"
          @click="showHelpModal = true"
        >
          <CircleHelp class="h-4 w-4 shrink-0" :stroke-width="1.75" aria-hidden="true" />
          <span class="hidden 2xl:inline text-[10px] font-medium">Help</span>
        </button>
        <AccountAccessMenu
          :show-platform-admin="adminStore.isAdmin === true"
          :show-undock="hasCustomPos && !isDragging"
          :undock-label="dockActionLabel"
          @cli="showCliModal = true"
          @undock="resetDockPos"
          @logout="handleLogout"
        />
      </div>
    </div>

    <!-- CLI quickstart modal -->
    <CliQuickstartModal v-if="showCliModal" @close="showCliModal = false" />
    <HelpSupportModal v-if="showHelpModal" @close="showHelpModal = false" />

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

/* Pointer capture is deliberately not required: the shell switches from an
   edge dock to the floating representation as soon as a drag starts, so the
   window listeners below keep a touch or mouse drag alive across that DOM
   replacement. The handle still opts out of browser panning. */
.shell-drag-handle {
  touch-action: none;
}

/* Keep the navigation usable for coarse pointers even though the visual
   system is intentionally dense on desktop. */
@media (pointer: coarse) {
  .shell-drag-handle,
  .shell-sidebar-toggle,
  .shell-nav-link,
  .shell-nav-group-toggle,
  .shell-status-action,
  .shell-help {
    min-height: 44px;
    min-width: 44px;
  }
}

/* On a narrow viewport the complete chrome becomes one scroll surface. The
   route track must not create a nested scrollbar that can starve the fixed
   brand, context, recovery, help, and account controls. */
@media (max-width: 640px) {
  .faros-shell-horizontal {
    gap: 0.25rem;
    padding-inline: 0.5rem;
    overflow-x: auto;
    overflow-y: hidden;
    overscroll-behavior-x: contain;
  }

  .faros-shell-horizontal .shell-route-track,
  .shell-floating-chrome .shell-route-track {
    flex: 0 0 auto;
    min-width: max-content;
    overflow: visible;
  }

  .faros-shell-horizontal .shell-brand-name,
  .island .shell-brand-name {
    display: none;
  }

  .faros-shell-horizontal .shell-context-status,
  .island .shell-context-status {
    max-width: 7rem;
    overflow: hidden;
  }

  .faros-shell-horizontal .shell-context-status > span:last-child,
  .island .shell-context-status > span:last-child {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .shell-floating-chrome {
    max-width: calc(100vw - 0.5rem);
    overflow-x: auto;
    overflow-y: hidden;
    overscroll-behavior-x: contain;
  }
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
