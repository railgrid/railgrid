<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useTerminalSessionsStore } from '@/stores/terminalSessions'
import { useTenantStore } from '@/stores/tenant'
import TerminalDock from '@/components/TerminalDock.vue'
import CliQuickstartModal from '@/components/CliQuickstartModal.vue'
import UserProfileModal from '@/components/UserProfileModal.vue'
import TenantContextChip from '@/components/TenantContextChip.vue'
import ThemeSwitch from '@/components/ThemeSwitch.vue'
import FirstWorkspaceWizard from '@/components/FirstWorkspaceWizard.vue'
import { Hexagon, LayoutDashboard, LogOut, Zap, GripHorizontal, GripVertical, Pin, Terminal, Puzzle, Dot, Settings, ShieldAlert } from 'lucide-vue-next'
import { useProvidersStore } from '@/stores/providers'
import { useAdminStore } from '@/stores/admin'
import { categoryIcons, fallbackCategoryIcon } from '@/lib/categoryIcons'

const auth = useAuthStore()
const terminalStore = useTerminalSessionsStore()
const providersStore = useProvidersStore()
const tenantStore = useTenantStore()
const adminStore = useAdminStore()

// Probe platform-admin access once so the sidebar can show the /bonkers entry
// only to allowlisted identities. Non-admins get a single quiet 403 and the
// menu item stays hidden.
onMounted(() => { void adminStore.checkAccess() })
const layoutProps = defineProps<{ fullBleed?: boolean }>()

const route = useRoute()
const router = useRouter()

// Empty-org guard: when the active org has zero workspaces (after fetch
// completes), every workspace-scoped page would try to query a cluster
// that doesn't exist. Replace the slot with the create-workspace wizard
// instead. Pages that don't need a workspace — the tenant settings page
// and the providers catalog — render normally so the user keeps a
// non-blocked path to manage orgs/workspaces.
//
// workspacesByOrg[uuid] is undefined while the initial fetch is in
// flight; we only show the wizard once it lands as an empty array, so
// the UI doesn't flash the wizard between an org switch and the fetch
// resolving.
const showWorkspaceWizard = computed(() => {
  if (!auth.isAuthenticated) return false
  const orgUUID = tenantStore.orgUUID
  if (!orgUUID) return false
  const list = tenantStore.workspacesByOrg[orgUUID]
  if (!list || list.length > 0) return false
  const path = route.path
  if (path === '/tenant' || path.startsWith('/tenant/')) return false
  if (path === '/providers') return false
  return true
})

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
  layoutProps.fullBleed ? 'h-full min-h-0' : '',
])

const now = ref(new Date())
let timer: ReturnType<typeof setInterval>

onMounted(() => {
  timer = setInterval(() => { now.value = new Date() }, 1000)
})
onUnmounted(() => clearInterval(timer))

const pad = (n: number) => String(n).padStart(2, '0')
const timeStr = computed(() =>
  `${pad(now.value.getHours())}:${pad(now.value.getMinutes())}:${pad(now.value.getSeconds())}`
)

interface NavItem {
  label: string
  to: string
  // Either a lucide component (static) or a URL string (dynamic provider icon).
  icon?: unknown
  iconURL?: string | null
}

// Static items above the Providers section. Everything provider-shaped
// (Edges, MCP, Workloads, etc.) flows through providersStore — those
// items get categorized + sub-nav treatment below. Dashboard is the
// only true platform-wide page.
// Settings used to live here but moved to the sidebar's bottom action
// area (near Theme / Logout) — top-of-nav placement made it compete
// with the providers nav and read as a peer of Dashboard, which it is
// not. The horizontal/floating docks render it as a dedicated icon
// button in the right-side action area instead.
const staticNavItems: NavItem[] = [
  { label: 'Dashboard', to: '/', icon: LayoutDashboard },
]

// Catalog link sits at the top of the Providers section as a header that
// also routes to the full catalog page when clicked.
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
// before each group so the bar reads as "Static | Kubernetes: x y |
// MCP: z | Other: w" instead of a flat icon parade.
interface HorizontalSection {
  key: string
  label: string | null
  icon: unknown | null
  items: NavItem[]
}
const horizontalNavSections = computed<HorizontalSection[]>(() => {
  const sections: HorizontalSection[] = []
  sections.push({ key: 'static', label: null, icon: null, items: [...staticNavItems] })
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
  // Providers catalog link goes last so it acts as "+" rather than a
  // sibling of the items.
  sections.push({ key: 'catalog', label: null, icon: null, items: [providersHeaderItem] })
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

const showCliModal = ref(false)
const showProfileModal = ref(false)

// --- Draggable dock with edge-snap (all 4 edges) ---
type DockMode = 'float' | 'left' | 'right' | 'top' | 'bottom'
const DOCK_STORAGE_KEY = 'kedge-dock-state'
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
    if (s.x >= 0 && s.y >= 0 && s.x < window.innerWidth && s.y < window.innerHeight) return s
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
  localStorage.removeItem(DOCK_STORAGE_KEY)
}

onMounted(() => {
  window.addEventListener('mousemove', onDragMove)
  window.addEventListener('mouseup', onDragEnd)
})
onUnmounted(() => {
  window.removeEventListener('mousemove', onDragMove)
  window.removeEventListener('mouseup', onDragEnd)
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
  const left = isVerticalDock.value && dockState.value.mode === 'left' ? '12rem' : '0px'
  const right = isVerticalDock.value && dockState.value.mode === 'right' ? '12rem' : '0px'
  const bottom = isHorizontalDock.value && dockState.value.mode === 'bottom' ? '44px' : '0px'
  return {
    '--app-inset-left': left,
    '--app-inset-right': right,
    '--app-inset-bottom': bottom,
  }
})
</script>

<template>
  <div class="cross-grid relative flex h-screen bg-surface" :class="layoutClass" :style="layoutInsetsStyle">
    <!-- Ambient orbs -->
    <div class="pointer-events-none fixed inset-0 overflow-hidden">
      <div class="absolute -top-40 left-1/3 h-80 w-80 rounded-full bg-accent/4 blur-[160px]" />
      <div class="absolute bottom-1/3 right-1/4 h-64 w-64 rounded-full bg-success/3 blur-[140px]" />
      <div class="absolute top-1/2 left-3/4 h-48 w-48 rounded-full bg-accent/3 blur-[120px]" />
    </div>

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
      class="relative z-50 flex h-full w-48 flex-shrink-0 flex-col overflow-hidden border-border-subtle bg-surface-raised/80 py-3 px-2 backdrop-blur-xl"
      :class="dockState.mode === 'left' ? 'order-first border-r' : 'order-last border-l'"
    >
      <!-- Drag handle + Logo -->
      <div class="flex items-center gap-2 px-2 mb-1">
        <div
          class="flex h-6 w-6 cursor-grab items-center justify-center rounded-lg text-text-muted/30 transition-colors hover:text-text-muted"
          @mousedown="onDragStart"
        >
          <GripVertical class="h-3 w-3" :stroke-width="2" />
        </div>
        <div class="relative flex h-7 w-7 items-center justify-center">
          <div class="absolute inset-0 rounded-lg bg-accent/20 blur-md" />
          <div class="relative flex h-7 w-7 items-center justify-center rounded-lg border border-accent/25 bg-surface-overlay/80">
            <Hexagon class="h-3.5 w-3.5 text-accent" :stroke-width="2.5" />
          </div>
        </div>
        <span class="text-[11px] font-bold tracking-tight text-text-primary">KEDGE</span>
        <div class="flex items-center gap-0.5 rounded-full border border-success/20 bg-success-subtle px-1.5 py-px">
          <Zap class="h-2 w-2 text-success" :stroke-width="2.5" fill="currentColor" />
          <span class="text-[7px] font-semibold uppercase tracking-widest text-success">Live</span>
        </div>
      </div>

      <div class="mx-2 my-2 h-px bg-border-default/50" />

      <!-- Active org/workspace context. Compact selector + link to the
           full /tenant settings page. Sits above the static nav so the
           "where am I" cue is the first thing users see. -->
      <TenantContextChip variant="sidebar" />

      <div class="mx-2 my-2 h-px bg-border-default/50" />

      <!-- Scrollable nav region. With many providers this is the only
           part of the dock that grows, so it scrolls internally instead
           of squishing the rows and pushing the footer controls off
           screen. min-h-0 lets it shrink below its content height inside
           the flex column; the header above and footer below stay pinned. -->
      <div class="-mr-1 flex min-h-0 flex-1 flex-col overflow-y-auto pr-1">
      <!-- Static nav items (Dashboard, Workloads) -->
      <router-link
        v-for="item in staticNavItems"
        :key="item.to"
        :to="item.to"
        class="flex items-center gap-2.5 rounded-xl px-3 py-2 text-[11px] font-medium transition-all duration-200"
        :class="isActive(item.to) ? 'bg-accent/15 text-accent' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary'"
      >
        <component :is="item.icon" class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
        <span>{{ item.label }}</span>
      </router-link>

      <!-- Provider categories render as non-clickable section dividers:
           a thin rule with the category icon + label inline, then the
           providers in that category as indented rows. Children of a
           provider (e.g. Workloads under Kubernetes) get one more level
           of indent, with a leading dot glyph for visual hierarchy. -->
      <template v-for="group in providersStore.categorizedNavItems.groups" :key="'cat-' + group.name">
        <div class="mt-3 mb-1 flex items-center gap-2 px-3">
          <component :is="categoryIcon(group.icon)" class="h-3 w-3 flex-shrink-0 text-text-muted/70" :stroke-width="2" />
          <span class="text-[9px] font-semibold uppercase tracking-wider text-text-muted/70">{{ group.name }}</span>
          <div class="h-px flex-1 bg-border-default/40" />
        </div>
        <template v-for="item in group.items" :key="item.to">
          <router-link
            :to="item.to"
            class="flex items-center gap-2.5 rounded-xl px-3 py-1.5 text-[11px] font-medium transition-all duration-200"
            :class="isActive(item.to) ? 'bg-accent/15 text-accent' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary'"
          >
            <img v-if="item.iconURL" :src="item.iconURL" alt="" class="h-3.5 w-3.5 flex-shrink-0 object-contain" />
            <Puzzle v-else class="h-3.5 w-3.5 flex-shrink-0" :stroke-width="1.75" />
            <span>{{ item.label }}</span>
          </router-link>
          <router-link
            v-for="child in item.children"
            :key="'c-' + child.to"
            :to="child.to"
            class="flex items-center gap-2 rounded-xl py-1.5 pr-3 pl-8 text-[11px] font-medium transition-all duration-200"
            :class="isActive(child.to) ? 'bg-accent/15 text-accent' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary'"
          >
            <Dot class="h-3.5 w-3.5 flex-shrink-0 -ml-1" :stroke-width="3" />
            <span>{{ child.label }}</span>
          </router-link>
        </template>
      </template>

      <!-- Uncategorized providers (third-party with no spec.category) sit
           under their own divider so the rhythm of the sidebar stays
           consistent. -->
      <template v-if="providersStore.categorizedNavItems.uncategorized.length">
        <div class="mt-3 mb-1 flex items-center gap-2 px-3">
          <Puzzle class="h-3 w-3 flex-shrink-0 text-text-muted/70" :stroke-width="2" />
          <span class="text-[9px] font-semibold uppercase tracking-wider text-text-muted/70">Other</span>
          <div class="h-px flex-1 bg-border-default/40" />
        </div>
        <template v-for="item in providersStore.categorizedNavItems.uncategorized" :key="'u-' + item.to">
          <router-link
            :to="item.to"
            class="flex items-center gap-2.5 rounded-xl px-3 py-1.5 text-[11px] font-medium transition-all duration-200"
            :class="isActive(item.to) ? 'bg-accent/15 text-accent' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary'"
          >
            <img v-if="item.iconURL" :src="item.iconURL" alt="" class="h-3.5 w-3.5 flex-shrink-0 object-contain" />
            <Puzzle v-else class="h-3.5 w-3.5 flex-shrink-0" :stroke-width="1.75" />
            <span>{{ item.label }}</span>
          </router-link>
          <router-link
            v-for="child in item.children"
            :key="'uc-' + child.to"
            :to="child.to"
            class="flex items-center gap-2 rounded-xl py-1.5 pr-3 pl-8 text-[11px] font-medium transition-all duration-200"
            :class="isActive(child.to) ? 'bg-accent/15 text-accent' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary'"
          >
            <Dot class="h-3.5 w-3.5 flex-shrink-0 -ml-1" :stroke-width="3" />
            <span>{{ child.label }}</span>
          </router-link>
        </template>
      </template>

      <!-- Providers catalog link sits at the end as a slim tertiary link;
           the rest of the section above is the actual provider tree. -->
      <div class="mt-3 mb-1 flex items-center gap-2 px-3">
        <div class="h-px flex-1 bg-border-default/40" />
      </div>
      <router-link
        :to="providersHeaderItem.to"
        class="flex items-center gap-2.5 rounded-xl px-3 py-1.5 text-[10px] font-medium uppercase tracking-wider transition-all duration-200"
        :class="isActive(providersHeaderItem.to, true) ? 'bg-accent/15 text-accent' : 'text-text-muted/80 hover:bg-surface-overlay/50 hover:text-text-secondary'"
      >
        <Puzzle class="h-3.5 w-3.5 flex-shrink-0" :stroke-width="1.75" />
        <span>{{ providersHeaderItem.label }}</span>
      </router-link>
      </div>
      <!-- end scrollable nav region -->

      <div class="mx-2 my-2 h-px bg-border-default/50" />

      <!-- CLI quickstart -->
      <button
        class="flex items-center gap-2.5 rounded-xl px-3 py-2 text-[11px] font-medium text-text-muted transition-all hover:bg-surface-overlay/50 hover:text-text-secondary"
        title="Install the kedge CLI"
        @click="showCliModal = true"
      >
        <Terminal class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
        <span>CLI</span>
      </button>

      <!-- Settings (formerly at the top of the nav). Sits alongside the
           other workspace-level preferences here. -->
      <router-link
        to="/tenant"
        class="flex items-center gap-2.5 rounded-xl px-3 py-2 text-[11px] font-medium transition-all duration-200"
        :class="isActive('/tenant') ? 'bg-accent/15 text-accent' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary'"
      >
        <Settings class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
        <span>Settings</span>
      </router-link>

      <!-- Platform admin (/bonkers): only rendered for allowlisted identities
           (adminStore.isAdmin, set by the access probe on mount). -->
      <router-link
        v-if="adminStore.isAdmin"
        to="/bonkers"
        class="flex items-center gap-2.5 rounded-xl px-3 py-2 text-[11px] font-medium transition-all duration-200"
        :class="isActive('/bonkers') ? 'bg-accent/15 text-accent' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary'"
      >
        <ShieldAlert class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
        <span>Platform admin</span>
      </router-link>

      <!-- Theme segmented control: shows all three options so users can
           pick directly instead of cycling through unknown next states.
           Icon-only — tooltips carry the per-option label. -->
      <div class="px-1 py-1">
        <ThemeSwitch variant="sidebar" />
      </div>

      <!-- Status -->
      <button
        v-if="auth.user"
        class="flex w-full items-center gap-2 rounded-lg px-3 py-1.5 text-left transition-colors hover:bg-surface-overlay/50"
        title="Show your identity (email / user ID)"
        @click="showProfileModal = true"
      >
        <div class="h-1.5 w-1.5 rounded-full bg-success flex-shrink-0" />
        <span class="font-mono text-[9px] text-text-muted truncate hover:text-accent">{{ auth.user.email }}</span>
      </button>
      <span class="px-3 font-mono text-[9px] tabular-nums text-text-muted/50">
        {{ timeStr }}
      </span>

      <div class="mx-2 my-2 h-px bg-border-default/50" />

      <!-- Undock -->
      <button
        class="flex items-center gap-2.5 rounded-xl px-3 py-2 text-[11px] font-medium text-text-muted/40 transition-all hover:text-accent"
        @click="resetDockPos"
      >
        <Pin class="h-3.5 w-3.5 flex-shrink-0" :stroke-width="2" />
        <span>Undock</span>
      </button>

      <!-- Logout -->
      <button
        class="flex items-center gap-2.5 rounded-xl px-3 py-2 text-[11px] font-medium text-text-muted transition-all hover:bg-danger-subtle hover:text-danger"
        @click="handleLogout"
      >
        <LogOut class="h-3.5 w-3.5 flex-shrink-0" :stroke-width="2" />
        <span>Logout</span>
      </button>
    </aside>

    <!-- HORIZONTAL BAR (top or bottom) -->
    <nav
      v-if="isHorizontalDock"
      ref="dockedRef"
      class="relative z-50 flex w-full flex-shrink-0 items-center gap-1.5 border-border-subtle bg-surface-raised/80 px-4 py-1.5 backdrop-blur-xl"
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
        <div class="relative flex h-6 w-6 items-center justify-center">
          <div class="absolute inset-0 rounded-md bg-accent/20 blur-md" />
          <div class="relative flex h-6 w-6 items-center justify-center rounded-md border border-accent/25 bg-surface-overlay/80">
            <Hexagon class="h-3 w-3 text-accent" :stroke-width="2.5" />
          </div>
        </div>
        <span class="text-[11px] font-bold tracking-tight text-text-primary">KEDGE</span>
        <div class="flex items-center gap-0.5 rounded-full border border-success/20 bg-success-subtle px-1.5 py-px">
          <Zap class="h-2 w-2 text-success" :stroke-width="2.5" fill="currentColor" />
          <span class="text-[8px] font-semibold uppercase tracking-widest text-success">Live</span>
        </div>
      </div>

      <div class="mx-0.5 h-5 w-px bg-border-default/40" />

      <!-- Tenant context chip (horizontal variant) -->
      <TenantContextChip variant="horizontal" />

      <div class="mx-0.5 h-5 w-px bg-border-default/40" />

      <!-- Nav sections: labels always visible, category chips between
           groups so providers don't all look like Puzzle icons. The
           sections live in their own flex-1 track that scrolls
           horizontally; items are shrink-0 so a long provider list
           overflows into the scroll area instead of compressing every
           link until the labels collide. This track also replaces the
           old flex-1 spacer — it pushes the right-side controls to the
           edge while staying scrollable. -->
      <div class="flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto kedge-nav-scroll">
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
          class="flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-xl px-2.5 py-1 text-[11px] font-medium transition-all duration-200"
          :class="isActive(item.to) ? 'bg-accent/15 text-accent' : 'text-text-muted hover:bg-surface-overlay/40 hover:text-text-secondary'"
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
      <span v-if="auth.clusterName" class="px-1 font-mono text-[9px] tracking-wider text-text-muted">
        {{ auth.clusterName }}
      </span>
      <button
        class="flex items-center gap-1 rounded-md border border-border-subtle px-1.5 py-1 text-text-muted transition-all hover:border-accent/30 hover:text-accent"
        title="Install the kedge CLI"
        @click="showCliModal = true"
      >
        <Terminal class="h-3 w-3" :stroke-width="1.75" />
        <span class="text-[8px] font-semibold uppercase tracking-wider">CLI</span>
      </button>
      <router-link
        to="/tenant"
        class="flex h-7 w-7 items-center justify-center rounded-lg transition-all"
        :class="isActive('/tenant') ? 'text-accent' : 'text-text-muted hover:text-text-secondary'"
        title="Tenant settings"
      >
        <Settings class="h-3.5 w-3.5" :stroke-width="2" />
      </router-link>
      <ThemeSwitch variant="compact" />
      <span class="px-0.5 font-mono text-[9px] tabular-nums tracking-wider text-text-muted/50">
        {{ timeStr }}
      </span>
      <button
        v-if="auth.user"
        class="flex items-center gap-1 rounded-full border border-border-subtle bg-surface-overlay/50 px-2 py-1 backdrop-blur transition-colors hover:border-accent/40"
        title="Show your identity (email / user ID)"
        @click="showProfileModal = true"
      >
        <div class="h-1.5 w-1.5 rounded-full bg-success" />
        <span class="font-mono text-[9px] text-text-muted hover:text-accent">{{ auth.user.email }}</span>
      </button>

      <div class="mx-0.5 h-5 w-px bg-border-default/40" />

      <button
        class="flex h-7 w-7 items-center justify-center rounded-lg text-text-muted/50 transition-all hover:text-accent"
        title="Undock to floating bar"
        @click="resetDockPos"
      >
        <Pin class="h-3 w-3" :stroke-width="2" />
      </button>

      <button
        class="flex h-7 w-7 items-center justify-center rounded-lg text-text-muted transition-all hover:bg-danger-subtle hover:text-danger"
        title="Logout"
        @click="handleLogout"
      >
        <LogOut class="h-3 w-3" :stroke-width="2" />
      </button>
    </nav>

    <!-- Main content -->
    <main
      :class="mainClass"
      :style="mainStyle"
    >
      <div class="dot-grid pointer-events-none absolute inset-0 opacity-40" />
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
        wrapper rebuilds. The chrome above (sidebar, TenantContextChip,
        popover) stays mounted because the key sits on the slot
        wrapper, not the layout shell.

        When the active org has no workspaces we never get a clusterName,
        so swap the slot out for the create-workspace wizard. The wizard
        triggers tenant.createWorkspace, which selects the new workspace,
        which sets auth.clusterName, which re-keys this wrapper and
        restores the original page.
      -->
      <div :key="auth.clusterName ?? 'unauth'" :class="slotClass">
        <FirstWorkspaceWizard v-if="showWorkspaceWizard" />
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
      <div class="island flex max-w-[calc(100vw-2rem)] items-center gap-1 rounded-2xl px-2 py-1.5">
        <div
          class="island-nav flex h-8 w-5 cursor-grab items-center justify-center rounded-lg text-text-muted/30 transition-colors hover:text-text-muted"
          :class="{ 'cursor-grabbing': isDragging }"
          @mousedown="onDragStart"
        >
          <GripHorizontal class="h-3 w-3" :stroke-width="2" />
        </div>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <div class="flex items-center gap-1.5 px-1.5">
          <div class="relative flex h-6 w-6 items-center justify-center">
            <div class="absolute inset-0 rounded-md bg-accent/20 blur-md" />
            <div class="relative flex h-6 w-6 items-center justify-center rounded-md border border-accent/25 bg-surface-overlay/80 backdrop-blur">
              <Hexagon class="h-3 w-3 text-accent" :stroke-width="2.5" />
            </div>
          </div>
          <span class="text-[11px] font-bold tracking-tight text-text-primary">KEDGE</span>
          <div class="flex items-center gap-0.5 rounded-full border border-success/20 bg-success-subtle px-1.5 py-px">
            <Zap class="h-2 w-2 text-success" :stroke-width="2.5" fill="currentColor" />
            <span class="text-[8px] font-semibold uppercase tracking-widest text-success">Live</span>
          </div>
        </div>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <TenantContextChip variant="horizontal" />

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <div class="flex min-w-0 items-center gap-1 overflow-x-auto kedge-nav-scroll">
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
            class="island-nav flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-xl px-2.5 py-1 text-[11px] font-medium transition-all duration-200"
            :class="isActive(item.to) ? 'active bg-accent/15 text-accent' : 'text-text-muted hover:text-text-secondary'"
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

        <span v-if="auth.clusterName" class="px-1 font-mono text-[9px] tracking-wider text-text-muted">
          {{ auth.clusterName }}
        </span>
        <router-link
          to="/tenant"
          class="island-nav flex h-7 w-7 items-center justify-center rounded-lg transition-all duration-200"
          :class="isActive('/tenant') ? 'text-accent' : 'text-text-muted hover:text-text-secondary'"
          title="Tenant settings"
        >
          <Settings class="h-3.5 w-3.5" :stroke-width="2" />
        </router-link>
        <ThemeSwitch variant="compact" />
        <span class="px-0.5 font-mono text-[9px] tabular-nums tracking-wider text-text-muted/50">
          {{ timeStr }}
        </span>
        <button
          v-if="auth.user"
          class="flex items-center gap-1 rounded-full border border-border-subtle bg-surface-overlay/50 px-2 py-1 backdrop-blur transition-colors hover:border-accent/40"
          title="Show your identity (email / user ID)"
          @click="showProfileModal = true"
        >
          <div class="h-1.5 w-1.5 rounded-full bg-success" />
          <span class="font-mono text-[9px] text-text-muted hover:text-accent">{{ auth.user.email }}</span>
        </button>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <button
          v-if="hasCustomPos && !isDragging"
          class="island-nav flex h-7 w-7 items-center justify-center rounded-lg text-text-muted/50 transition-all duration-200 hover:text-accent"
          title="Reset to default position"
          @click="resetDockPos"
        >
          <Pin class="h-3 w-3" :stroke-width="2" />
        </button>

        <button
          class="island-nav flex h-7 w-7 items-center justify-center rounded-lg text-text-muted transition-all duration-200 hover:bg-danger-subtle hover:text-danger"
          title="Logout"
          @click="handleLogout"
        >
          <LogOut class="h-3 w-3" :stroke-width="2" />
        </button>
      </div>
    </div>

    <!-- Global SSH terminal dock (persists across route changes) -->
    <TerminalDock />

    <!-- CLI quickstart modal -->
    <CliQuickstartModal v-if="showCliModal" @close="showCliModal = false" />

    <!-- User identity modal (email / user ID for sharing) -->
    <UserProfileModal v-if="showProfileModal" @close="showProfileModal = false" />
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
.kedge-nav-scroll {
  scrollbar-width: thin;
  scrollbar-color: var(--color-text-muted) transparent;
}
.kedge-nav-scroll::-webkit-scrollbar {
  height: 4px;
}
.kedge-nav-scroll::-webkit-scrollbar-track {
  background: transparent;
}
.kedge-nav-scroll::-webkit-scrollbar-thumb {
  background-color: var(--color-text-muted);
  border-radius: 9999px;
}
</style>
