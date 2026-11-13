<script setup lang="ts">
import { useGettingStartedFocus } from '@/composables/gettingStartedFocus'
import { useAuthStore } from '@/stores/auth'
import { claimFirstWelcome, welcomeIdentity } from '@/lib/firstWelcome'
import { useScopedNavigation } from '@/composables/useScopedNavigation'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { GridLayout, GridItem } from 'grid-layout-plus'
import AppLayout from '@/components/AppLayout.vue'
import DashboardTile from '@/components/DashboardTile.vue'
import DashboardUsageTile from '@/components/DashboardUsageTile.vue'
import WelcomeWizard from '@/components/WelcomeWizard.vue'
import ActionMenu, { type ActionMenuItem } from '@/portalkit/ActionMenu.vue'
import { useProvidersStore, type ProviderDTO } from '@/stores/providers'
import { useTenantStore } from '@/stores/tenant'
import { ADD_PROVIDER_TILE_ID, useDashboardLayoutStore } from '@/stores/dashboardLayout'
import { Puzzle, RotateCcw, Check, LayoutDashboard, LayoutGrid, Plus, Rocket } from 'lucide-vue-next'
import { gettingStartedReadState } from '@/lib/gettingStarted'

const { scopePath } = useScopedNavigation()
const route = useRoute()
const router = useRouter()

// The dashboard iterates the catalog and mounts one <DashboardTile> per
// ready provider. Each provider may register a
// <railgrid-dashboard-tile-{name}> custom element in its main.js — that
// element owns its own data fetch, summary rendering, and click-through
// URLs. A provider without a tile still gets a launcher card in the grid.
//
// On top of that, the grid is user-customisable: a "Customize" toggle
// turns tiles into draggable/resizable cells with a remove affordance,
// and the arrangement (positions, sizes, hidden set) is persisted per
// workspace via the dashboardLayout store. The store reconciles that
// remembered layout against the live provider set on every change, so
// the per-workspace enablement gate below stays authoritative.
//
const providers = useProvidersStore()
const tenant = useTenantStore()
const auth = useAuthStore()
const dash = useDashboardLayoutStore()
const { layout, catalogTile, addable, columns } = storeToRefs(dash)

onMounted(() => {
  if (!providers.loaded) providers.load()
})

// Use the same workspace selection as navigation. The merged catalog is an
// installation offer and may describe a different same-named org copy.
const gated = computed(() =>
  providers.enabledNavItems
    .map(item => providers.workspaceProvider(item.name))
    .filter((p): p is ProviderDTO => !!p?.ready && !!p.serving?.ui)
    .sort((a, b) => a.displayName.localeCompare(b.displayName)),
)

// Candidate tiles are every gated provider, and every one of them gets a
// card. A provider that ships no <railgrid-dashboard-tile-*> element renders a
// launcher body instead of being dropped, so the grid never reflows on
// probe results and is never wrongly empty because a bundle was slow.
const candidateNames = computed(() => gated.value.map((p) => p.name))

// Responsive column count follows the dashboard's actual content width, not
// the browser viewport. AppLayout's column is fluid and the nav can consume
// different amounts of the viewport, so viewport sizing made cards unusably
// narrow at both phone and ultrawide sizes.
const pageRef = ref<HTMLElement | null>(null)
const pageWidth = ref(1280)
const TILE_GAP = 16
const GRID_ROW_HEIGHT = 90
const MIN_TILE_WIDTH = 320
const MAX_DASHBOARD_COLUMNS = 8
// The shell can mount this content after hydration or replace it on a
// cluster switch. Always observe the current element, including remounts.
watch(pageRef, (page, _previous, onCleanup) => {
  if (!page) return
  const measure = () => {
    if (page.clientWidth > 0) pageWidth.value = page.clientWidth
  }
  measure()
  if (typeof ResizeObserver !== 'undefined') {
    const observer = new ResizeObserver(measure)
    observer.observe(page)
    onCleanup(() => observer.disconnect())
  } else {
    window.addEventListener('resize', measure)
    onCleanup(() => window.removeEventListener('resize', measure))
  }
}, { flush: 'post' })

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

// The guide and dashboard share the same catalog and binding reads. Treat the
// binding set as empty only after both reads are settled for the active scope.
const gettingStartedStatus = computed(() => gettingStartedReadState({
  hasScope: !!tenant.orgUUID && !!tenant.workspaceUUID,
  catalogLoaded: providers.loaded,
  catalogLoading: providers.loading,
  catalogMatchesOrganization: providers.catalogOrgUUID === tenant.orgUUID,
  catalogError: !!providers.error,
  bindingsMatchScope: providers.bindingsOrgUUID === tenant.orgUUID &&
    providers.bindingsWorkspace === tenant.workspaceUUID,
  bindingsState: providers.bindingsLoadState,
  bindingsStale: providers.bindingsStale,
}))
const enabledBindingCount = computed(() => Object.keys(providers.bindingNamesByProvider).length)
const workspaceDataReady = computed(() => gettingStartedStatus.value === 'ready')

// Once per signed-in account in this browser, independent of Workspace.
const welcomeUser = computed(() => welcomeIdentity(auth))
const welcomeOpen = ref(false)
const gettingStartedTrigger = ref<HTMLButtonElement | null>(null)
const guideForced = ref(false)
// The audience chosen on the onboarding page; empty when the guide was not
// opened as that page's first task.
const firstTask = ref<'' | 'personal' | 'team'>('')
const showWelcome = computed(() => welcomeOpen.value || guideForced.value)

const guideShownScope = ref<string | null>(null)
const currentScope = computed(() => `${tenant.orgUUID ?? ''}/${tenant.workspaceUUID ?? ''}`)
const guideVisible = computed(() => workspaceDataReady.value || (
  gettingStartedStatus.value !== 'unscoped' &&
  guideShownScope.value === currentScope.value &&
  providers.catalogOrgUUID === tenant.orgUUID &&
  providers.bindingsOrgUUID === tenant.orgUUID &&
  providers.bindingsWorkspace === tenant.workspaceUUID
))
watch([showWelcome, workspaceDataReady, currentScope], ([open, ready, scope]) => {
  if (open && ready) guideShownScope.value = scope
}, { immediate: true })
useGettingStartedFocus(computed(() => showWelcome.value && guideVisible.value))
const readBanner = ref<HTMLElement | null>(null)
const retrying = ref(false)

// Keep the initial shimmer for the first catalog request, but leave the guide
// mounted while its own Retry action reloads provider state.
const initializing = computed(() => providers.loading && !showWelcome.value && !retrying.value && gettingStartedStatus.value !== 'unscoped')

watch(
  [gettingStartedStatus, welcomeUser, currentScope, () => route.query.firstTask] as const,
  ([status, user, _scope, request]) => {
    if (status === 'ready' && user && (request === 'personal' || request === 'team')) {
      // The query only says which audience to start in. Whether the guide
      // opens at all is still the once-per-user latch, so a bookmarked or
      // history URL cannot reopen a guide that was already dismissed.
      if (claimFirstWelcome(user)) {
        firstTask.value = request
        welcomeOpen.value = true
      }
      const query = { ...route.query }
      delete query.firstTask
      void router.replace({ query })
      return
    }
    if (status === 'ready' && claimFirstWelcome(user)) welcomeOpen.value = true
  },
  { immediate: true },
)

async function retryWorkspaceData() {
  if (!tenant.orgUUID || retrying.value) return
  const scope = currentScope.value
  const banner = readBanner.value
  banner?.focus()
  retrying.value = true
  try {
    await providers.load(tenant.orgUUID)
  } finally {
    retrying.value = false
    await nextTick()
    if (scope === currentScope.value &&
      (document.activeElement === banner || document.activeElement === document.body)) {
      (readBanner.value ?? pageRef.value)?.focus()
    }
  }
}

function openGettingStarted() {
  guideForced.value = true
}

async function dismissWelcome() {
  welcomeOpen.value = false
  guideForced.value = false
  firstTask.value = ''
  await nextTick()
  gettingStartedTrigger.value?.focus()
}

watch(
  [currentScope, welcomeUser, () => tenant.workspaceMode] as const,
  () => {
    guideForced.value = false
    welcomeOpen.value = false
    guideShownScope.value = null
    firstTask.value = ''
  },
  { flush: 'sync' },
)

const providerFor = (name: string) => gated.value.find(provider => provider.name === name)

// --- Customize mode ---
const editMode = ref(false)
const addProviderLabel = 'Add provider'
const catalogTileOptions: ActionMenuItem[] = [{ id: 'remove', label: 'Remove Add provider tile', tone: 'danger' }]
const gridItems = computed(() => {
  // Follow visual reading order in browse mode, with the catalog link last.
  const providers = editMode.value ? layout.value : [...layout.value].sort((a, b) => a.y - b.y || a.x - b.x)
  return catalogTile.value ? [...providers, catalogTile.value] : providers
})
// Size the grid for both the provider layout and the display-only shortcut.
const gridHeight = computed(() => {
  const rows = gridItems.value.reduce((bottom, tile) => Math.max(bottom, tile.y + tile.h), 0)
  return rows * (GRID_ROW_HEIGHT + TILE_GAP) + TILE_GAP
})
const addMenuItems = computed<ActionMenuItem[]>(() => addable.value.map((name) => ({
  id: name,
  label: name === ADD_PROVIDER_TILE_ID ? addProviderLabel : providerFor(name)?.displayName ?? name,
  tone: 'neutral',
})))

function toggleEdit() {
  editMode.value = !editMode.value
}
function onAdd(name: string) {
  dash.unhide(name)
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
  const scope = currentScope.value
  persistTimer = setTimeout(() => {
    if (scope === currentScope.value) dash.persist()
  }, 300)
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
    <div ref="pageRef" tabindex="-1">
    <div v-if="initializing" class="mt-20 flex flex-col items-center justify-center">
      <div class="shimmer h-8 w-8 rounded-xl" />
      <div class="shimmer mt-4 h-3 w-40 rounded" />
    </div>

    <template v-else>
      <div v-if="!workspaceDataReady && (!showWelcome || !guideVisible || gettingStartedStatus === 'error')" ref="readBanner" tabindex="-1" class="mb-6 rounded-xl border border-border-subtle bg-surface-raised/60 p-4 text-sm text-text-muted" role="status" aria-live="polite">
        <template v-if="gettingStartedStatus === 'error'">
          <p>Couldn’t load this workspace’s providers. Try again to see what’s available.</p>
          <button type="button" class="k-btn k-btn--ghost mt-3" @click="retryWorkspaceData">Retry</button>
        </template>
        <p v-else-if="gettingStartedStatus === 'unscoped'">Select a workspace to see its providers.</p>
        <p v-else>Loading this workspace’s providers…</p>
      </div>
      <WelcomeWizard
        v-if="showWelcome"
        :key="`${tenant.orgUUID ?? ''}/${tenant.workspaceUUID ?? ''}`"
        v-show="guideVisible"
        :start-at-goal="guideForced || !!firstTask"
        :first-task="firstTask"
        @dismiss="dismissWelcome"
      />

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
              <ActionMenu
                :items="addMenuItems"
                label="Add tile"
                :disabled="addable.length === 0"
                show-label
                @select="onAdd"
              />
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
                ref="gettingStartedTrigger"
                type="button"
                class="k-btn k-btn--primary min-h-11 px-3 text-[12px] md:min-h-0 md:py-1.5"
                @click="openGettingStarted"
              >
                <Rocket class="h-4 w-4" :stroke-width="1.75" /> Getting started
              </button>
              <button
                v-if="gated.length > 0"
                type="button"
                class="k-btn k-btn--ghost min-h-11 px-3 text-[12px] md:min-h-0 md:py-1.5"
                @click="toggleEdit"
              >
                <LayoutGrid class="h-4 w-4" :stroke-width="1.75" /> Customize
              </button>
            </template>
          </div>
        </header>

        <div v-if="gated.length === 0 && workspaceDataReady" class="mb-6 flex items-start gap-3 rounded-xl border border-border-subtle bg-surface-raised/60 p-4 text-[13px] text-text-muted">
          <Puzzle class="mt-0.5 h-4 w-4 text-text-muted" :stroke-width="1.75" />
          <div>
            <div v-if="enabledBindingCount > 0" class="font-medium text-text-secondary">No providers are ready to open</div>
            <div v-else class="font-medium text-text-secondary">No providers enabled in this workspace</div>
            <div v-if="enabledBindingCount > 0" class="mt-1 text-xs">
              This workspace has provider access, but none is ready to open from the dashboard yet. Browse the
              <router-link :to="scopePath('/providers')" class="text-accent hover:text-accent-hover">provider catalog</router-link>
              for readiness and capability details.
            </div>
            <div v-else class="mt-1 text-xs">
              Browse provider capabilities in the
              <router-link :to="scopePath('/providers')" class="text-accent hover:text-accent-hover">provider catalog</router-link>.
              Each provider is enabled per workspace, and some access requests require administrator approval.
            </div>
          </div>
        </div>

        <template v-if="gated.length > 0">
        <!-- Nothing on the grid. Two distinct reasons, so the message must
             match: either the user removed tiles that can be added back
             (addable > 0), or the enabled providers here simply ship no
             dashboard tile (addable === 0) — in which case there is nothing
             to "add", so point at the catalog instead of a dead control. -->
        <div
          v-if="gridItems.length === 0"
          class="flex items-start gap-3 rounded-xl border border-border-subtle bg-surface-raised/60 p-4 text-[13px] text-text-muted"
        >
          <LayoutGrid class="mt-0.5 h-4 w-4 text-text-muted" :stroke-width="1.75" />
          <div v-if="addable.length > 0">
            <div class="font-medium text-text-secondary">Your dashboard is empty</div>
            <div class="mt-1 text-xs">
              You've removed all tiles. Use <button type="button" class="k-btn k-btn--ghost border-0 bg-transparent p-0 text-accent hover:bg-transparent hover:text-accent-hover" @click="editMode = true">Customize → Add tile</button> to bring them back.
            </div>
          </div>
          <div v-else>
            <div class="font-medium text-text-secondary">No dashboard tiles here yet</div>
            <div class="mt-1 text-xs">
              The providers enabled in this workspace don't publish a dashboard tile. Enable another from the
              <router-link :to="scopePath('/providers')" class="text-accent hover:text-accent-hover">catalog</router-link>,
              or open a provider from the side navigation.
            </div>
          </div>
        </div>

        <GridLayout
          v-else
          :layout="layout"
          :class="editMode ? 'select-none' : undefined"
          :style="{ height: `${gridHeight}px` }"
          :auto-size="false"
          :col-num="columns"
          :row-height="GRID_ROW_HEIGHT"
          :margin="[TILE_GAP, TILE_GAP]"
          :is-draggable="editMode"
          :is-resizable="editMode"
          :is-bounded="true"
          :vertical-compact="true"
          @selectstart="onGridSelectStart"
        >
          <GridItem
            v-for="item in gridItems"
            :key="item.i"
            :x="item.x"
            :y="item.y"
            :w="item.w"
            :h="item.h"
            :i="item.i"
            :static="item.i === ADD_PROVIDER_TILE_ID"
            :min-w="1"
            :min-h="1"
            drag-ignore-from=".tile-no-drag"
            @moved="onUserLayoutUpdated"
            @resized="onUserLayoutUpdated"
          >
              <component
                v-if="item.i === ADD_PROVIDER_TILE_ID"
                :is="editMode ? 'div' : 'router-link'"
                :to="editMode ? undefined : scopePath('/providers')"
                class="relative flex h-full flex-col items-center justify-center gap-2 rounded-xl border bg-surface-raised/80 text-sm font-medium text-text-primary"
                :class="editMode ? 'border-accent/40 ring-1 ring-accent/30' : 'border-border-subtle transition-colors hover:border-accent/50 hover:bg-surface-raised focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent'"
              >
                <Plus class="h-6 w-6 text-text-muted" :stroke-width="1.5" aria-hidden="true" />
                {{ addProviderLabel }}
                <ActionMenu
                  v-if="editMode"
                  class="tile-no-drag absolute right-3 top-3"
                  :items="catalogTileOptions"
                  label="Add provider tile options"
                  @select="onRemove(ADD_PROVIDER_TILE_ID)"
                />
              </component>
              <DashboardTile
                v-else-if="providerFor(item.i)"
                :provider="providerFor(item.i)!"
                :edit-mode="editMode"
                @remove="onRemove"
              />
          </GridItem>
        </GridLayout>
        </template>

        <!-- Match the grid's outer gutters and follow every provider row.
             The grid already leaves TILE_GAP below its last row. -->
        <div
          v-if="tenant.workspaceUUID"
          :style="{ marginInline: `${TILE_GAP}px`, marginTop: gated.length > 0 && gridItems.length === 0 ? `${TILE_GAP}px` : undefined }"
        >
          <DashboardUsageTile />
        </div>
      </template>
    </template>
    </div>
  </AppLayout>
</template>
