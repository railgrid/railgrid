<script setup lang="ts">
import { useGettingStartedFocus } from '@/composables/gettingStartedFocus'
import { useAuthStore } from '@/stores/auth'
import { claimFirstWelcome, welcomeIdentity } from '@/lib/firstWelcome'
import { useScopedNavigation } from '@/composables/useScopedNavigation'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { GridLayout, GridItem } from 'grid-layout-plus'
import { dashboardAgentReloadDraftKey, useAgentOverlayStore } from '@/stores/agentOverlay'
import AppLayout from '@/components/AppLayout.vue'
import AIPrimaryAction from '@/agentkit/AIPrimaryAction.vue'
import DashboardTile from '@/components/DashboardTile.vue'
import DashboardUsageTile from '@/components/DashboardUsageTile.vue'
import WelcomeWizard from '@/components/WelcomeWizard.vue'
import ActionMenu, { type ActionMenuItem } from '@/portalkit/ActionMenu.vue'
import { useDelayedLoading } from '@/portalkit/useDelayedLoading'
import { useProvidersStore, type ProviderDTO } from '@/stores/providers'
import { useTenantStore } from '@/stores/tenant'
import { ADD_PROVIDER_TILE_ID, useDashboardLayoutStore } from '@/stores/dashboardLayout'
import { ArrowDown, Bot, Boxes, Check, ChevronRight, GitBranch, Hexagon, LayoutGrid, Network, PanelsTopLeft, Plus, Puzzle, Rocket, RotateCcw, Search } from 'lucide-vue-next'
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
const assistant = useAgentOverlayStore()
const agentsProvider = computed(() => gated.value.find(provider => provider.name === 'agents'))
const assistantInput = ref<HTMLTextAreaElement | null>(null)
const assistantDraft = ref('')
const assistantBusy = ref(false)
const shortcutsRequested = ref(false)
const shortcutRoot = ref<HTMLElement | null>(null)
const shortcutNav = ref<HTMLElement | null>(null)
const showPreparingChat = useDelayedLoading(computed(() => assistant.dashboardPreparing), 250)
const canSendQuestion = computed(() => !!assistantDraft.value.trim() && !!assistant.dashboardSubmit && !assistant.dashboardPreparing)

function sendQuestion() {
  const prompt = assistantDraft.value.trim()
  if (!prompt || !assistant.dashboardSubmit || assistant.dashboardPreparing) return
  if (assistant.dashboardSubmit(prompt, assistantInput.value)) {
    shortcutsRequested.value = false
    assistantDraft.value = ''
    assistantBusy.value = false
  } else {
    assistantBusy.value = true
  }
}

function submitWithEnter(event: KeyboardEvent) {
  if (event.isComposing) return
  event.preventDefault()
  sendQuestion()
}

watch(() => assistant.preferenceKey, key => {
  assistantDraft.value = ''
  assistantBusy.value = false
  shortcutsRequested.value = false
  if (key) {
    try {
      const storageKey = dashboardAgentReloadDraftKey(key)
      assistantDraft.value = sessionStorage.getItem(storageKey) ?? ''
      sessionStorage.removeItem(storageKey)
    } catch { /* Session storage can be unavailable. */ }
  }
}, { immediate: true, flush: 'sync' })


// Responsive column count follows the dashboard's actual content width, not
// the browser viewport. AppLayout's column is fluid and the nav can consume
// different amounts of the viewport, so viewport sizing made cards unusably
// narrow at both phone and ultrawide sizes.
const pageRef = ref<HTMLElement | null>(null)
const pageWidth = ref(1280)
const pageViewportHeight = ref<number>()
const TILE_GAP = 16
const GRID_ROW_HEIGHT = 90
const MIN_TILE_WIDTH = 320
const MAX_DASHBOARD_COLUMNS = 8
// The shell can mount this content after hydration or replace it on a
// cluster switch. Always observe the current element, including remounts.
watch(pageRef, (page, _previous, onCleanup) => {
  if (!page) return
  const scrollViewport = page.closest('main')
  const measure = () => {
    if (page.clientWidth > 0) pageWidth.value = page.clientWidth
    if (scrollViewport && scrollViewport.clientHeight > 0) {
      // The shell's scroll viewport also accounts for horizontal navigation.
      const paddingTop = Number.parseFloat(getComputedStyle(scrollViewport).paddingTop)
      pageViewportHeight.value = scrollViewport.clientHeight - paddingTop
    }
  }
  measure()
  if (typeof ResizeObserver !== 'undefined') {
    const observer = new ResizeObserver(measure)
    observer.observe(page)
    if (scrollViewport) observer.observe(scrollViewport)
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
const editMode = ref(false)
const showComposerLanding = computed(() => workspaceDataReady.value && !!agentsProvider.value &&
  !assistant.preferencesLoading && assistant.personalAgent !== null && !!assistant.selectedAgent &&
  !assistant.dashboardExpanded && !editMode.value && (!!assistant.dashboardSubmit || assistant.dashboardPreparing),
)
const dashboardHeader = ref<HTMLElement | null>(null)
const dashboardHeaderHeight = ref(0)
const composerSlot = ref<HTMLElement | null>(null)
const composerDock = ref<HTMLElement | null>(null)
const reservedComposerHeight = ref<number>()
const composerPinned = computed(() => showComposerLanding.value && reservedComposerHeight.value !== undefined)
const composerCompact = computed(() => !showComposerLanding.value || composerPinned.value)
const composerForm = ref<HTMLFormElement | null>(null)
const composerFrame = ref<HTMLElement | null>(null)
const composerMoving = ref(false)
let composerMoves: Animation[] = []

function stopComposerMove() {
  composerMoves.forEach(move => move.cancel())
  composerMoves = []
  composerMoving.value = false
}

watch([pageRef, dashboardHeader, composerSlot, showComposerLanding], ([page, header, slot, featured], _, onCleanup) => {
  const viewport = page?.closest('main')
  if (!viewport || !header || !slot || !featured) {
    reservedComposerHeight.value = undefined
    return
  }

  const updateDock = () => {
    if (!slot.isConnected || !showComposerLanding.value) return
    const { top, height } = slot.getBoundingClientRect()
    const atDock = top <= viewport.getBoundingClientRect().top + 8
    const atBottom = viewport.scrollTop >= viewport.scrollHeight - viewport.clientHeight - 1
    if (viewport.scrollTop > 0 && (atDock || atBottom)) {
      // Preserve the landing geometry while the form is in the dock.
      reservedComposerHeight.value ??= height
    } else {
      reservedComposerHeight.value = undefined
    }
  }

  const measureDock = () => {
    dashboardHeaderHeight.value = header.offsetHeight
    void nextTick(updateDock)
  }

  measureDock()
  viewport.addEventListener('scroll', updateDock, { passive: true })
  const observer = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(measureDock) : undefined
  observer?.observe(viewport)
  observer?.observe(header)
  onCleanup(() => {
    viewport.removeEventListener('scroll', updateDock)
    observer?.disconnect()
  })
}, { flush: 'post' })

watch([composerPinned, showComposerLanding], async ([pinned, featured], [wasPinned], onCleanup) => {
  if (!featured) {
    stopComposerMove()
    return
  }
  const composer = shortcutRoot.value
  if (!composer || pinned === wasPinned) return

  const form = composerForm.value
  const frame = composerFrame.value
  const canAnimate = frame && form && typeof composer.animate === 'function' &&
    !window.matchMedia('(prefers-reduced-motion: reduce)').matches
  // Capture the painted position before Vue moves the same form between its
  // landing slot and dock. This also makes a quick reversal start mid-motion.
  const first = canAnimate ? frame.getBoundingClientRect() : undefined
  const firstRadius = canAnimate ? getComputedStyle(frame).borderRadius : undefined
  const controls = canAnimate ? Array.from(form.children)
    .filter(control => control !== frame)
    .map(control => ({ control, first: control.getBoundingClientRect() }))
    : []
  stopComposerMove()

  const focused = document.activeElement as HTMLElement | null
  const restoreFocus = focused && focused !== document.body && composer.contains(focused)
  const requested = shortcutsRequested.value
  let superseded = false
  onCleanup(() => { superseded = true })
  await nextTick()
  if (superseded || !composer.isConnected) return
  if (restoreFocus && focused.isConnected && document.activeElement === document.body) {
    focused.focus({ preventScroll: true })
    shortcutsRequested.value = requested
  }
  if (!frame || !first?.width || !first.height) return

  const last = frame.getBoundingClientRect()
  if (!last.width || !last.height) return
  const offsetX = (first.left + first.right - last.left - last.right) / 2
  const offsetY = first.top - last.top
  const timing = { duration: 240, easing: 'cubic-bezier(.2, .15, .28, 1)' }
  const controlOffsets = controls.map(({ control, first }) => {
    const last = control.getBoundingClientRect()
    return { control, x: first.left - last.left - offsetX, y: first.top - last.top - offsetY }
  })

  // Only the empty frame changes size. The live controls translate so their
  // text, icons, and focus indicators keep their proportions.
  const move = composer.animate([
    { transform: `translate(${offsetX}px, ${offsetY}px)` },
    { transform: 'none' },
  ], timing)
  const resize = frame.animate([
    {
      inset: `-1px ${last.right - first.right + offsetX - 1}px ${last.bottom - first.bottom + offsetY - 1}px ${first.left - last.left - offsetX - 1}px`,
      borderRadius: firstRadius,
    },
    { inset: '-1px', borderRadius: getComputedStyle(frame).borderRadius },
  ], timing)
  composerMoves = [move, resize, ...controlOffsets.map(({ control, x, y }) => control.animate([
    { transform: `translate(${x}px, ${y}px)` },
    { transform: 'none' },
  ], timing))]
  composerMoving.value = true
  move.onfinish = () => {
    if (composerMoves[0] === move) stopComposerMove()
  }
})
onUnmounted(stopComposerMove)

const workspaceShortcutOptions = [
  { provider: 'app-studio', label: 'Build an app', path: '/providers/app-studio/~new', icon: PanelsTopLeft },
  { provider: 'code', label: 'Browse code repositories', path: '/providers/code/repositories', icon: GitBranch },
  { provider: 'agents', label: 'Manage agents', path: '/providers/agents', icon: Bot },
  { provider: 'edges', label: 'Connect an edge', path: '/providers/edges/connect/edge', icon: Network },
  { provider: 'infrastructure', label: 'Browse application templates', path: '/providers/infrastructure/templates', icon: Boxes },
  { provider: 'kuery', label: 'Explore Kubernetes resources', path: '/providers/kuery', icon: Search },
]
const workspaceShortcuts = computed(() => workspaceDataReady.value
  ? workspaceShortcutOptions.flatMap(shortcut => {
    const { provider } = shortcut
    if (!providers.isEnabled(provider)) return []
    const entry = providers.workspaceProvider(provider)
    const binding = providers.bindingsByProvider[provider]
    if (!entry?.serving?.ui && binding?.ready !== false && !binding?.terminating) return []
    return [{ ...shortcut, disabled: !entry?.ready || binding?.ready === false || !!binding?.terminating }]
  }).sort((a, b) => Number(a.disabled) - Number(b.disabled))
  : [])
const resourcesHeading = ref<HTMLHeadingElement | null>(null)
const resourcesSection = ref<HTMLElement | null>(null)

function showWorkspaceResources() {
  resourcesHeading.value?.focus({ preventScroll: true })
  resourcesSection.value?.scrollIntoView({ block: 'start' })
}
const shortcutsOpen = computed(() =>
  shortcutsRequested.value && !assistantDraft.value.trim() && !assistant.dashboardExpanded && workspaceShortcuts.value.length > 0,
)
watch(() => assistant.dashboardExpanded, expanded => {
  if (expanded) shortcutsRequested.value = false
}, { flush: 'sync' })

watch(shortcutsRequested, (requested, _, onCleanup) => {
  if (!requested) return
  // Shortcut links suppress mousedown's focus change so Safari cannot move
  // focus outside the drawer and dismiss it before the link receives its click.
  function dismissFromOutside(event: Event) {
    if (event.type === 'focusin' && (event.target === document.body || event.target === document.documentElement)) return
    if (!shortcutRoot.value || !event.composedPath().includes(shortcutRoot.value)) {
      shortcutsRequested.value = false
    }
  }
  document.addEventListener('pointerdown', dismissFromOutside, true)
  document.addEventListener('focusin', dismissFromOutside)
  onCleanup(() => {
    document.removeEventListener('pointerdown', dismissFromOutside, true)
    document.removeEventListener('focusin', dismissFromOutside)
  })
})

function dismissShortcuts(event: KeyboardEvent) {
  if (event.isComposing || !shortcutsOpen.value) return
  event.preventDefault()
  event.stopPropagation()
  assistantInput.value?.focus()
  shortcutsRequested.value = false
}

async function focusFirstShortcut(event: KeyboardEvent) {
  if (event.isComposing || assistantDraft.value.trim() || !workspaceShortcuts.value.some(shortcut => !shortcut.disabled)) return
  event.preventDefault()
  shortcutsRequested.value = true
  await nextTick()
  if (shortcutsOpen.value) shortcutNav.value?.querySelector('a')?.focus()
}

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
  [gettingStartedStatus, welcomeUser, currentScope, () => route.query.firstTask, () => assistant.defaultAgentLoading || assistant.preferencesLoading, () => assistant.selectedAgent] as const,
  ([status, user, _scope, request, agentLoading, selectedAgent]) => {
    if (status === 'ready' && user && !agentLoading && (request === 'personal' || request === 'team')) {
      // The query only says which audience to start in. Whether the guide
      // opens at all is still the once-per-user latch, so a bookmarked or
      // history URL cannot reopen a guide that was already dismissed.
      if (!selectedAgent && claimFirstWelcome(user)) {
        firstTask.value = request
        welcomeOpen.value = true
      }
      const query = { ...route.query }
      delete query.firstTask
      void router.replace({ query })
      return
    }
    if (status === 'ready' && !agentLoading && !selectedAgent && claimFirstWelcome(user)) welcomeOpen.value = true
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
const customizeTrigger = ref<HTMLButtonElement | null>(null)
const doneEditingTrigger = ref<HTMLButtonElement | null>(null)
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

async function toggleEdit() {
  editMode.value = !editMode.value
  await nextTick()
  const trigger = editMode.value ? doneEditingTrigger.value : customizeTrigger.value
  trigger?.focus()
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
    <div ref="pageRef" tabindex="-1"
      :style="pageViewportHeight === undefined ? undefined : { '--dashboard-viewport': `${pageViewportHeight}px` }">
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
        <header ref="dashboardHeader" class="flex flex-wrap items-center gap-2"
          :class="showComposerLanding ? 'justify-end' : 'mb-6 justify-between'">
          <h1 v-if="!showComposerLanding" class="text-[27px] font-semibold leading-tight tracking-tight text-text-primary">Workspace overview</h1>
          <div class="flex flex-wrap items-center gap-2">
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
                ref="doneEditingTrigger"
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
                data-track="open_getting_started"
                @click="openGettingStarted"
              >
                <Rocket class="h-4 w-4 max-[360px]:hidden" :stroke-width="1.75" aria-hidden="true" /> Getting started
              </button>
              <button
                v-if="gated.length > 0"
                ref="customizeTrigger"
                type="button"
                class="k-btn k-btn--ghost min-h-11 px-3 text-[12px] md:min-h-0 md:py-1.5"
                @click="toggleEdit"
              >
                <LayoutGrid class="h-4 w-4 max-[360px]:hidden" :stroke-width="1.75" aria-hidden="true" /> Customize
              </button>
            </template>
          </div>
        </header>

        <div ref="composerDock" class="dashboard-composer-dock"></div>
        <div :class="{ 'dashboard-landing': showComposerLanding }"
          :style="showComposerLanding ? { '--dashboard-header-height': `${dashboardHeaderHeight}px` } : undefined">
          <div ref="composerSlot" :class="{ 'dashboard-composer-slot': showComposerLanding }"
            :style="composerPinned ? { minBlockSize: `${reservedComposerHeight}px` } : undefined">
            <Teleport :to="composerDock" :disabled="!composerPinned || !composerDock">
              <section v-if="agentsProvider && (assistant.preferencesLoading || assistant.personalAgent !== null)"
                class="dashboard-agent-handoff"
                :class="{
                  'dashboard-agent-handoff--collapsed': assistant.dashboardExpanded,
                  'dashboard-agent-handoff--pinned': composerPinned,
                  'dashboard-agent-handoff--moving': composerMoving,
                }"
                :inert="assistant.dashboardExpanded" aria-label="Railgrid Agents">
                <div class="dashboard-agent-clip" :class="{ 'dashboard-agent-clip--overflow-visible': shortcutsOpen || composerMoving }">
                  <div class="grid gap-3" :class="composerCompact ? (composerPinned ? '' : 'mx-4 mb-6') : 'dashboard-composer-content'">
                    <template v-if="!assistant.preferencesLoading && assistant.selectedAgent">
                      <h1 v-if="showComposerLanding" :class="composerPinned ? 'sr-only' : 'dashboard-composer-heading'">What should we work on together?</h1>
                      <div ref="shortcutRoot" class="relative" @keydown.esc="dismissShortcuts">
                        <form ref="composerForm" class="dashboard-composer border border-transparent" :class="{ 'dashboard-composer--compact': composerCompact }"
                          @submit.prevent="sendQuestion">
                          <span ref="composerFrame" class="dashboard-composer-frame" aria-hidden="true"></span>
                          <span class="dashboard-composer-label flex items-center gap-2 text-xs text-text-secondary">
                            <Hexagon class="size-4 text-accent" :stroke-width="1.75" aria-hidden="true" />
                            <span v-if="!composerCompact">Railgrid Agents</span>
                          </span>
                          <textarea id="dashboard-agent-composer" ref="assistantInput" v-model="assistantDraft"
                            class="dashboard-composer-input min-w-0 resize-none bg-transparent text-text-primary outline-none placeholder:text-text-muted [field-sizing:content]"
                            aria-label="Message Railgrid Agents" placeholder="Ask about apps, edges, or infrastructure…" maxlength="4000"
                            :aria-controls="workspaceShortcuts.length ? 'dashboard-workspace-shortcuts' : undefined"
                            :rows="composerCompact ? 1 : 2" @focus="shortcutsRequested = !assistant.sidePanelClosing" @click="shortcutsRequested = true"
                            @keydown.down.exact="focusFirstShortcut" @keydown.enter.exact="submitWithEnter" />
                          <AIPrimaryAction class="dashboard-composer-send" type="submit" :disabled="!canSendQuestion" :loading="showPreparingChat" />
                        </form>
                        <p class="sr-only" role="status">{{ showPreparingChat ? 'Preparing chat…' : '' }}</p>
                        <nav v-if="workspaceShortcuts.length" id="dashboard-workspace-shortcuts" ref="shortcutNav"
                          aria-label="Workspace shortcuts" class="dashboard-shortcuts rounded-md border border-border-default bg-surface-raised p-1"
                          :class="{ 'dashboard-shortcuts--open': shortcutsOpen }" :inert="!shortcutsOpen">
                          <p class="px-2 py-1 text-xs font-medium text-text-secondary">Go to</p>
                          <component v-for="shortcut in workspaceShortcuts" :key="shortcut.provider"
                            :is="shortcut.disabled ? 'span' : 'router-link'"
                            :to="shortcut.disabled ? undefined : scopePath(shortcut.path)"
                            :role="shortcut.disabled ? 'link' : undefined" :aria-disabled="shortcut.disabled ? 'true' : undefined"
                            class="k-menu-item min-h-11 md:min-h-9" @mousedown.left.prevent>
                            <component :is="shortcut.icon" class="size-4 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
                            <span class="grow">{{ shortcut.label }}</span>
                            <ChevronRight class="size-4 shrink-0" :stroke-width="1.75" aria-hidden="true" />
                          </component>
                        </nav>
                      </div>
                      <div v-if="assistantBusy" class="flex flex-wrap items-center gap-2">
                        <p role="status" class="text-sm text-text-secondary">Your question is still in the dashboard draft. Check the chat, then try again.</p>
                        <button v-if="assistant.dashboardSubmit" type="button" class="k-btn k-btn--ghost"
                          @click="assistant.dashboardSubmit('', assistantInput)">Open chat</button>
                      </div>
                      <p v-else-if="!assistant.dashboardPreparing && !assistant.dashboardSubmit" role="status" class="text-sm text-text-secondary">
                        Chat is unavailable. <router-link :to="scopePath('/providers/agents')" class="text-accent hover:text-accent-hover">Check the Agents provider</router-link>.
                      </p>
                    </template>
                    <div v-else class="dashboard-agent grid gap-3 rounded-lg border p-3 sm:p-4">
                      <div class="flex min-w-0 items-center gap-2.5">
                        <span class="grid size-7 shrink-0 place-items-center rounded-md bg-accent-subtle text-accent" aria-hidden="true">
                          <Hexagon class="size-4" :stroke-width="1.75" />
                        </span>
                        <h2 class="text-base font-semibold">Railgrid Agents</h2>
                        <span v-if="tenant.activeWorkspace?.displayName" class="ml-auto hidden min-w-0 truncate font-mono text-xs text-text-secondary sm:block">
                          {{ tenant.activeWorkspace.displayName }}
                        </span>
                      </div>
                      <p v-if="assistant.defaultAgentLoading || assistant.preferencesLoading" role="status" class="text-sm text-text-secondary">Checking workspace assistant…</p>
                      <template v-else>
                        <p class="text-sm text-text-secondary">
                          Choose an agent to chat with from your dashboard. Ask what a resource does, get help troubleshooting a service, or find your way around this workspace.
                        </p>
                        <router-link class="k-btn k-btn--ghost justify-self-start"
                          :to="scopePath('/providers/agents') + '#/assistant-settings'">Set up workspace assistant</router-link>
                      </template>
                    </div>
                  </div>
                </div>
              </section>
            </Teleport>
          </div>
        </div>

        <section ref="resourcesSection" aria-labelledby="workspace-resources-heading"
          :class="{ 'dashboard-resources--featured': showComposerLanding }">
          <div class="flex min-h-11 items-center gap-1"
            :class="showComposerLanding ? 'mx-4 border-t border-border-subtle pt-2' : 'mb-2'">
            <h2 id="workspace-resources-heading" ref="resourcesHeading" tabindex="-1" class="text-base font-semibold">Workspace resources</h2>
            <button v-if="showComposerLanding" type="button" class="k-btn k-btn--text !px-2 text-text-secondary"
              aria-label="Scroll to workspace resources" @click="showWorkspaceResources">
              <ArrowDown class="size-4" :stroke-width="1.75" aria-hidden="true" />
            </button>
          </div>

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
                You've removed all tiles. Use <button type="button" class="k-btn k-btn--ghost border-0 bg-transparent p-0 text-accent hover:bg-transparent hover:text-accent-hover" @click="toggleEdit">Customize → Add tile</button> to bring them back.
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
        </section>
      </template>
    </template>
    </div>
  </AppLayout>
</template>

<style scoped>
.dashboard-landing {
  display: grid;
  /* Leave room for the resources heading and the first tile headers. */
  min-block-size: calc(var(--dashboard-viewport, 100dvh - 3rem) - var(--dashboard-header-height) - 7rem);
}

.dashboard-resources--featured {
  --dashboard-resources-top: 5.25rem;

  /* Short grids still need enough scroll space to reach the pinned composer. */
  min-block-size: calc(var(--dashboard-viewport, 100dvh - 3rem) - var(--dashboard-resources-top));
  scroll-margin-block-start: var(--dashboard-resources-top);
}

.dashboard-composer-slot {
  align-self: center;
  padding-block: 1.5rem;
}

@media (max-height: 640px) {
  .dashboard-composer-slot {
    padding-block: 0.75rem;
  }
}

.dashboard-composer-dock {
  position: sticky;
  inset-block-start: 0;
  z-index: 20;
  height: 0;
  overflow-anchor: none;
}

.dashboard-composer-dock .dashboard-agent-handoff--pinned {
  position: absolute;
  /* Cover the shell's py-5 inset so tiles don't show above the bar. */
  inset-block-start: -1.25rem;
  inset-inline: 0;
  padding: 1.25rem 1rem 0.5rem;
  background: var(--color-surface);
}

.dashboard-composer-content {
  inline-size: min(100%, 42rem);
  margin-inline: auto;
}

.dashboard-composer-heading {
  margin-block-end: 0.5rem;
  color: var(--color-text-primary);
  font-size: clamp(1.5rem, 3.5vw, 2.125rem);
  font-weight: 600;
  line-height: 1.15;
  letter-spacing: -0.025em;
  text-align: center;
  text-wrap: balance;
}

.dashboard-composer {
  position: relative;
  display: grid;
  grid-template-areas: 'input input' 'label send';
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 0.5rem;
  padding: 0.75rem;
  border-radius: 6px;
}

.dashboard-composer--compact {
  grid-template-areas: 'label input send';
  grid-template-columns: 2.25rem minmax(0, 1fr) auto;
  gap: 0.375rem;
  padding: 0.25rem;
  border-radius: 4px;
}

.dashboard-composer-frame {
  position: absolute;
  inset: -1px;
  pointer-events: none;
  border: 1px solid color-mix(in srgb, var(--color-accent) 45%, var(--color-border-default));
  border-radius: inherit;
  background: var(--color-surface-raised);
}

.dashboard-composer > :not(.dashboard-composer-frame) {
  position: relative;
}

.dashboard-composer-label {
  grid-area: label;
  padding-inline-start: 0.25rem;
}

.dashboard-composer--compact .dashboard-composer-label {
  justify-content: center;
  padding-inline-start: 0;
}

.dashboard-composer-input {
  grid-area: input;
  min-block-size: 4rem;
  max-block-size: 9rem;
  padding: 0.25rem;
  font-size: 1rem;
}

.dashboard-composer--compact .dashboard-composer-input {
  min-block-size: 2.25rem;
  max-block-size: 8rem;
  padding: 0.5rem 0;
  font-size: 0.875rem;
}

.dashboard-agent-handoff--pinned .dashboard-composer-input {
  max-block-size: 2.25rem;
  line-height: 1.25rem;
  scrollbar-width: none;
}

@media (any-pointer: coarse) {
  .dashboard-composer--compact .dashboard-composer-input {
    min-block-size: 2.75rem;
    font-size: 1rem;
  }

  .dashboard-agent-handoff--pinned .dashboard-composer-input {
    max-block-size: 2.75rem;
  }
}

.dashboard-composer-send {
  grid-area: send;
}

.dashboard-composer:focus-within .dashboard-composer-frame {
  border-color: var(--color-accent);
}

.dashboard-composer:has(textarea:focus-visible) .dashboard-composer-frame {
  outline: 2px solid var(--color-accent);
  outline-offset: -2px;
}

.dashboard-agent-handoff {
  position: relative;
  display: grid;
  grid-template-rows: 1fr;
  transition: grid-template-rows 220ms cubic-bezier(.2, 0, .2, 1);
}

.dashboard-agent-clip {
  min-height: 0;
  /* Restoring focus during the handoff must not scroll this wrapper. */
  overflow: clip;
  transition: opacity 140ms ease;
}

.dashboard-agent-handoff--collapsed {
  grid-template-rows: 0fr;
}

.dashboard-agent-handoff--collapsed .dashboard-agent-clip {
  opacity: 0;
}

.dashboard-agent-handoff--moving {
  z-index: 20;
}

.dashboard-agent-clip--overflow-visible {
  /* The drawer and the moving composer can extend outside this wrapper. */
  overflow: visible;
}

.dashboard-shortcuts {
  position: absolute;
  inset-block-start: 100%;
  inset-inline: 0;
  z-index: calc(var(--k-layer-menu, 1000) - 10);
  margin-block-start: 4px;
  max-block-size: min(22rem, 52dvh);
  overflow-y: auto;
  overscroll-behavior: contain;
  box-shadow: 0 8px 24px color-mix(in srgb, var(--color-text-primary) 10%, transparent);
  visibility: hidden;
  pointer-events: none;
  opacity: 0;
  transform: translateY(-4px);
}

.dashboard-shortcuts--open {
  visibility: visible;
  pointer-events: auto;
  opacity: 1;
  transform: translateY(0);
  transition: transform 180ms cubic-bezier(.16, 1, .3, 1), opacity 140ms ease;
}

@media (prefers-reduced-motion: reduce) {
  .dashboard-agent-handoff,
  .dashboard-agent-clip,
  .dashboard-shortcuts {
    transition: none;
  }
}

.dashboard-agent {
  border-color: color-mix(in srgb, var(--color-accent) 22%, var(--color-border-default));
  background: color-mix(in srgb, var(--color-surface-raised), var(--color-accent) 5%);
}
</style>
