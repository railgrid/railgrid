<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { GridLayout, GridItem } from 'grid-layout-plus'
import AppLayout from '@/components/AppLayout.vue'
import DashboardTile from '@/components/DashboardTile.vue'
import WelcomeWizard from '@/components/WelcomeWizard.vue'
import { useProvidersStore } from '@/stores/providers'
import { useTenantStore } from '@/stores/tenant'
import { useDashboardLayoutStore } from '@/stores/dashboardLayout'
import { Puzzle, Plus, RotateCcw, Check, LayoutDashboard, LayoutGrid, Rocket } from 'lucide-vue-next'

// The dashboard iterates the catalog and mounts one <DashboardTile> per
// ready provider. Each provider may register a
// <faros-dashboard-tile-{name}> custom element in its main.js — that
// element owns its own data fetch, summary rendering, and click-through
// URLs. Providers without a tile drop out of the grid entirely.
//
// On top of that, the grid is user-customisable: a "Customize" toggle
// turns tiles into draggable/resizable cells with a remove affordance,
// and the arrangement (positions, sizes, hidden set) is persisted per
// workspace via the dashboardLayout store. The store reconciles that
// remembered layout against the live provider set on every change, so
// the per-workspace enablement gate below stays authoritative.
//
// The dashboard is edge-agnostic: edge onboarding (the wizard) now lives in the
// edges provider's own portal, shown there when the workspace has no edges yet.

const providers = useProvidersStore()
const tenant = useTenantStore()
const dash = useDashboardLayoutStore()
const { layout, addable } = storeToRefs(dash)

onMounted(() => {
  if (!providers.loaded) providers.load()
})

// Gated providers must match the side-nav "enabled" predicate exactly:
// built-in providers (kubernetes-edges, server-edges, mcp, …) always
// appear because they ship with the hub and need no per-workspace consent,
// but third-party providers (infrastructure, quickstart, anything custom)
// only show up when the current workspace has an APIBinding for them.
// Without this gate the dashboard kept rendering a tile for a disabled
// third-party provider — clicking it landed on a 403 "this provider is not
// enabled in your workspace" wall.
const gated = computed(() =>
  providers.items
    .filter((p) => {
      if (!p.ready || !p.hasUI) return false
      if (p.builtinRoute || p.builtin) return true
      return providers.isEnabled(p.name)
    })
    .sort((a, b) => a.displayName.localeCompare(b.displayName)),
)

// Candidate tiles are every gated provider, and every one of them gets a
// card. A provider that ships no <faros-dashboard-tile-*> element renders a
// launcher body instead of being dropped, so the grid never reflows on
// probe results and is never wrongly empty because a bundle was slow.
const candidateNames = computed(() => gated.value.map((p) => p.name))

// Responsive column count follows the dashboard's actual content width, not
// the browser viewport. AppLayout caps this column at max-w-7xl and the nav can
// consume different amounts of the viewport, so viewport sizing made cards
// unusably narrow at both phone and ultrawide sizes.
const pageRef = ref<HTMLElement | null>(null)
const pageWidth = ref(1280)
const TILE_GAP = 16
const MIN_TILE_WIDTH = 240
const MAX_DASHBOARD_COLUMNS = 4
let pageResizeObserver: ResizeObserver | null = null
let fallbackResize: (() => void) | null = null

onMounted(() => {
  const page = pageRef.value
  if (!page) return
  const measure = () => {
    pageWidth.value = page.clientWidth
  }
  measure()
  if (typeof ResizeObserver !== 'undefined') {
    pageResizeObserver = new ResizeObserver(measure)
    pageResizeObserver.observe(page)
  } else {
    fallbackResize = measure
    window.addEventListener('resize', measure)
  }
})

onUnmounted(() => {
  pageResizeObserver?.disconnect()
  if (fallbackResize) window.removeEventListener('resize', fallbackResize)
})

const responsiveCols = computed(() => {
  const columns = Math.floor((pageWidth.value + TILE_GAP) / (MIN_TILE_WIDTH + TILE_GAP))
  return Math.max(1, Math.min(MAX_DASHBOARD_COLUMNS, columns))
})

// Feed the layout store the live provider set + active tenant + column
// count. It reconciles geometry/hidden against this and updates
// `layout`/`addable`, rendering the localStorage cache immediately and
// then the hub's authoritative copy.
watch(
  [() => tenant.orgUUID, () => tenant.workspaceUUID, candidateNames, responsiveCols] as const,
  ([org, ws, names, cols]) => dash.sync(org, ws, names, cols),
  { immediate: true },
)

// The initial shimmer covers the providers fetch. The cached layout then
// renders synchronously, so the grid appears already settled.
const initializing = computed(() => providers.loading)

// --- First-run welcome ---
//
// Nothing is enabled by default: every provider ships as a CatalogEntry with an
// APIExport that must be bound per workspace. So a fresh account's dashboard and
// side nav are both genuinely empty, and the plain "no providers enabled" line
// that used to sit here assumed the reader already knew what a provider was.
// When the workspace has zero bindings we show the guided intro instead.
//
// Dismissal is per workspace and local-only: workspaces are independent
// clusters, so having set one up says nothing about the next, and a display
// preference doesn't warrant server-side state. Dismissing does not disable the
// entry point — the header keeps a "Getting started" button so the flow is
// reachable again, which also makes the local-only storage harmless if it is
// cleared or the user switches browsers.
const WELCOME_KEY = 'faros:portal:welcome-dismissed'

function readDismissed(): string[] {
  try {
    const raw = localStorage.getItem(WELCOME_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === 'string') : []
  } catch {
    return []
  }
}

const dismissedWorkspaces = ref<string[]>(readDismissed())

// Explicitly re-opened from the header. Outranks both the dismissal list and
// the has-bindings check, so the intro stays reachable once set up.
const welcomeForced = ref(false)

const welcomeDismissed = computed(() =>
  !!tenant.workspaceUUID && dismissedWorkspaces.value.includes(tenant.workspaceUUID),
)

// An empty binding map only means "nothing enabled here" once we know it was
// fetched for the workspace we're looking at — otherwise the boot fetch and
// every workspace switch would briefly flash the welcome flow at users who are
// already set up. `providers.loaded` is not enough: load() flips it before it
// awaits the bindings call.
const bindingsCurrent = computed(
  () => !!tenant.workspaceUUID && providers.bindingsWorkspace === tenant.workspaceUUID,
)

// Latched, not derived. Enabling a provider from inside step 2 flips
// hasAnyEnabled, and a plain computed would unmount the wizard the instant the
// first Enable succeeded — dropping the user onto the dashboard mid-flow,
// before they ever saw the step confirming what they just turned on. So the
// condition only ever opens the flow; closing it is the user's decision.
const welcomeOpen = ref(false)

watch(
  [
    () => providers.loaded,
    bindingsCurrent,
    () => providers.hasAnyEnabled,
    welcomeDismissed,
  ] as const,
  ([loaded, current, anyEnabled, dismissed]) => {
    if (loaded && current && !anyEnabled && !dismissed) welcomeOpen.value = true
  },
  { immediate: true },
)

const showWelcome = computed(() => welcomeOpen.value || welcomeForced.value)

function dismissWelcome() {
  welcomeForced.value = false
  welcomeOpen.value = false
  const ws = tenant.workspaceUUID
  if (!ws || dismissedWorkspaces.value.includes(ws)) return
  // Cap the list: it grows one entry per workspace ever visited, and only the
  // recent tail can still matter.
  const next = [...dismissedWorkspaces.value, ws].slice(-50)
  dismissedWorkspaces.value = next
  try {
    localStorage.setItem(WELCOME_KEY, JSON.stringify(next))
  } catch {
    // Private-mode/quota failures only cost a repeat showing.
  }
}

// Switching workspaces re-evaluates from scratch: the new workspace may be
// freshly created and needs its own intro, and the latch above must not carry
// the previous workspace's decision over. bindingsCurrent goes false until the
// refetch for the new workspace lands, so the latch re-arms on real data.
watch(
  () => tenant.workspaceUUID,
  () => {
    welcomeForced.value = false
    welcomeOpen.value = false
  },
)

const providerFor = (name: string) => providers.byName(name)

// --- Customize mode ---
const editMode = ref(false)
const addOpen = ref(false)

function toggleEdit() {
  editMode.value = !editMode.value
  if (!editMode.value) addOpen.value = false
}
function onAdd(name: string) {
  dash.unhide(name)
  if (addable.value.length === 0) addOpen.value = false
}
function onRemove(name: string) {
  dash.hide(name)
}

// Persist geometry only after a user drag/resize settles. GridLayout's
// layout-updated event also fires for mount and prop-driven responsive reflow,
// so treating it as an edit would overwrite the authoritative wide layout
// whenever the surrounding content column changes size.
let persistTimer: ReturnType<typeof setTimeout> | null = null
function onUserLayoutUpdated() {
  if (persistTimer) clearTimeout(persistTimer)
  persistTimer = setTimeout(() => dash.persist(), 300)
}

onUnmounted(() => {
  if (persistTimer) clearTimeout(persistTimer)
})

// grid-layout-plus disables selection only after a drag/resize has started.
// Cover the whole customize session so the initiating pointer movement cannot
// leave text highlighted beneath the tile. Normal dashboard browsing keeps
// native text selection.
function onGridSelectStart(event: Event) {
  if (editMode.value) event.preventDefault()
}
</script>

<template>
  <AppLayout>
    <div ref="pageRef">
    <div v-if="initializing" class="mt-20 flex flex-col items-center justify-center">
      <div class="shimmer h-8 w-8 rounded-xl" />
      <div class="shimmer mt-4 h-3 w-40 rounded" />
    </div>

    <template v-else>
      <!-- Fresh workspace: the guided intro replaces the grid entirely. It is
           the page at that point, not a banner above an empty one. -->
      <WelcomeWizard v-if="showWelcome" @dismiss="dismissWelcome" />

      <div v-else-if="gated.length === 0" class="flex items-start gap-3 rounded-xl border border-border-subtle bg-surface-raised/60 p-4 text-[13px] text-text-muted">
        <Puzzle class="mt-0.5 h-4 w-4 text-text-muted" :stroke-width="1.75" />
        <div>
          <div class="font-medium text-text-secondary">No providers enabled in this workspace</div>
          <div class="mt-1 text-xs">
            Enable a provider from the <router-link to="/providers" class="text-accent hover:text-accent-hover">catalog</router-link> to see a dashboard summary,
            or walk through the <button type="button" class="k-btn k-btn--ghost border-0 bg-transparent p-0 text-accent hover:bg-transparent hover:text-accent-hover" @click="welcomeForced = true">getting started guide</button>.
            Each provider is enabled per workspace.
          </div>
        </div>
      </div>

      <template v-else>
        <!-- Page heading and customize controls. -->
        <header class="mb-6 flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
          <div class="min-w-0">
            <h1 class="flex items-center gap-2 text-xl font-semibold text-text-primary">
              <LayoutDashboard class="h-5 w-5 flex-shrink-0 text-accent" :stroke-width="1.75" />
              Dashboard
            </h1>
            <p class="mt-1 text-sm text-text-muted">Provider summaries for the active workspace.</p>
          </div>
          <div class="flex w-full flex-wrap items-center gap-2 md:w-auto md:justify-end">
            <template v-if="editMode">
              <!-- Add a previously-removed tile back. -->
              <div class="relative">
                <button
                  type="button"
                  class="k-btn k-btn--ghost min-h-11 px-3 text-[12px] disabled:cursor-not-allowed disabled:opacity-50 md:min-h-0 md:py-1.5"
                  :disabled="addable.length === 0"
                  :aria-expanded="addOpen"
                  aria-controls="dashboard-add-menu"
                  @click="addOpen = !addOpen"
                >
                  <Plus class="h-4 w-4" :stroke-width="1.75" /> Add tile
                </button>
                <div
                  v-if="addOpen && addable.length"
                  id="dashboard-add-menu"
                  class="absolute right-0 z-20 mt-1 max-h-64 w-56 overflow-auto rounded-lg border border-border-subtle bg-surface-overlay py-1 shadow-lg"
                >
                  <button
                    v-for="name in addable"
                    :key="name"
                    type="button"
                    class="k-menu-item"
                    @click="onAdd(name)"
                  >
                    <Puzzle class="h-4 w-4 flex-shrink-0 text-text-muted" :stroke-width="1.75" />
                    <span class="truncate">{{ providerFor(name)?.displayName ?? name }}</span>
                  </button>
                </div>
              </div>
              <button
                type="button"
                class="k-btn k-btn--ghost min-h-11 px-3 text-[12px] md:min-h-0 md:py-1.5"
                @click="dash.reset()"
              >
                <RotateCcw class="h-4 w-4" :stroke-width="1.75" /> Reset
              </button>
              <button
                type="button"
                class="k-btn k-btn--primary min-h-11 px-3 text-[12px] md:min-h-0 md:py-1.5"
                @click="toggleEdit"
              >
                <Check class="h-4 w-4" :stroke-width="1.75" /> Done
              </button>
            </template>
            <template v-else>
              <!-- Re-entry point for the intro. Kept out of edit mode so the
                   customize controls stay a single coherent group. -->
              <button
                type="button"
                class="k-btn k-btn--primary min-h-11 px-3 text-[12px] md:min-h-0 md:py-1.5"
                @click="welcomeForced = true"
              >
                <Rocket class="h-4 w-4" :stroke-width="1.75" /> Getting started
              </button>
              <button
                type="button"
                class="k-btn k-btn--ghost min-h-11 px-3 text-[12px] md:min-h-0 md:py-1.5"
                @click="toggleEdit"
              >
                <LayoutGrid class="h-4 w-4" :stroke-width="1.75" /> Customize
              </button>
            </template>
          </div>
        </header>

        <!-- Nothing on the grid. Two distinct reasons, so the message must
             match: either the user removed tiles that can be added back
             (addable > 0), or the enabled providers here simply ship no
             dashboard tile (addable === 0) — in which case there is nothing
             to "add", so point at the catalog instead of a dead control. -->
        <div
          v-if="layout.length === 0"
          class="flex items-start gap-3 rounded-xl border border-border-subtle bg-surface-raised/60 p-4 text-[13px] text-text-muted"
        >
          <LayoutGrid class="mt-0.5 h-4 w-4 text-text-muted" :stroke-width="1.75" />
          <div v-if="addable.length > 0">
            <div class="font-medium text-text-secondary">Your dashboard is empty</div>
            <div class="mt-1 text-xs">
              You've removed all tiles. Use <button type="button" class="k-btn k-btn--ghost border-0 bg-transparent p-0 text-accent hover:bg-transparent hover:text-accent-hover" @click="editMode = true; addOpen = true">Customize → Add tile</button> to bring them back.
            </div>
          </div>
          <div v-else>
            <div class="font-medium text-text-secondary">No dashboard tiles here yet</div>
            <div class="mt-1 text-xs">
              The providers enabled in this workspace don't publish a dashboard tile. Enable another from the
              <router-link to="/providers" class="text-accent hover:text-accent-hover">catalog</router-link>,
              or open a provider from the side navigation.
            </div>
          </div>
        </div>

        <GridLayout
          v-else
          v-model:layout="layout"
          :class="editMode ? 'select-none' : undefined"
          :col-num="responsiveCols"
          :row-height="90"
          :margin="[TILE_GAP, TILE_GAP]"
          :is-draggable="editMode"
          :is-resizable="editMode"
          :is-bounded="true"
          :vertical-compact="true"
          @selectstart="onGridSelectStart"
        >
          <GridItem
            v-for="item in layout"
            :key="item.i"
            :x="item.x"
            :y="item.y"
            :w="item.w"
            :h="item.h"
            :i="item.i"
            :min-w="1"
            :min-h="1"
            drag-ignore-from=".tile-no-drag"
            @moved="onUserLayoutUpdated"
            @resized="onUserLayoutUpdated"
          >
            <DashboardTile
              v-if="providerFor(item.i)"
              :provider="providerFor(item.i)!"
              :edit-mode="editMode"
              @remove="onRemove"
            />
          </GridItem>
        </GridLayout>
      </template>
    </template>
    </div>
  </AppLayout>
</template>
