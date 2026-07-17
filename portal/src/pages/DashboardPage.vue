<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { GridLayout, GridItem } from 'grid-layout-plus'
import AppLayout from '@/components/AppLayout.vue'
import DashboardTile from '@/components/DashboardTile.vue'
import { useProvidersStore } from '@/stores/providers'
import { useTenantStore } from '@/stores/tenant'
import { useDashboardLayoutStore, GRID_COLS } from '@/stores/dashboardLayout'
import { Puzzle, Plus, RotateCcw, Check, LayoutGrid } from 'lucide-vue-next'

// The dashboard iterates the catalog and mounts one <DashboardTile> per
// ready provider. Each provider may register a
// <kedge-dashboard-tile-{name}> custom element in its main.js — that
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

// Tiles must match the side-nav "enabled" predicate exactly: built-in
// providers (kubernetes-edges, server-edges, mcp, …) always appear
// because they ship with the hub and need no per-workspace consent,
// but third-party providers (infrastructure, quickstart, anything
// custom) only show up when the current workspace has an APIBinding
// for them. Without this gate the dashboard kept rendering a tile
// for a disabled third-party provider — clicking it landed on a 403
// "this provider is not enabled in your workspace" wall.
const tiles = computed(() =>
  providers.items
    .filter((p) => {
      if (!p.ready || !p.hasUI) return false
      if (p.builtinRoute || p.builtin) return true
      return providers.isEnabled(p.name)
    })
    .sort((a, b) => a.displayName.localeCompare(b.displayName)),
)

// Feed the layout store the live provider set + active workspace. It
// reconciles geometry/hidden against this and updates `layout`/`addable`.
const candidateNames = computed(() => tiles.value.map((p) => p.name))
watch(
  [() => tenant.workspaceUUID, candidateNames] as const,
  ([ws, names]) => dash.sync(ws, names),
  { immediate: true },
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
function onNoTile(name: string) {
  dash.markNoTile(name)
}

// Persist geometry after a drag/resize settles. The grid fires
// layout-updated on every step; debounce so we write once per gesture.
let persistTimer: ReturnType<typeof setTimeout> | null = null
function onLayoutUpdated() {
  if (persistTimer) clearTimeout(persistTimer)
  persistTimer = setTimeout(() => dash.persist(), 300)
}
</script>

<template>
  <AppLayout>
    <div v-if="providers.loading" class="mt-20 flex flex-col items-center justify-center">
      <div class="shimmer h-8 w-8 rounded-xl" />
      <div class="shimmer mt-4 h-3 w-40 rounded" />
    </div>

    <template v-else>
      <div v-if="tiles.length === 0" class="flex items-start gap-3 rounded-xl border border-border-subtle bg-surface-raised/60 p-4 text-[13px] text-text-muted">
        <Puzzle class="mt-0.5 h-4 w-4 text-text-muted" :stroke-width="1.75" />
        <div>
          <div class="font-medium text-text-secondary">No providers enabled in this workspace</div>
          <div class="mt-1 text-xs">
            Enable a provider from the <router-link to="/providers" class="text-accent hover:text-accent-hover">catalog</router-link> to see a dashboard summary.
            Each provider is enabled per workspace.
          </div>
        </div>
      </div>

      <template v-else>
        <!-- Customize controls. -->
        <div class="mb-4 flex items-center justify-between">
          <h1 class="text-[13px] font-medium text-text-secondary">Dashboard</h1>
          <div class="flex items-center gap-2">
            <template v-if="editMode">
              <!-- Add a previously-removed tile back. -->
              <div class="relative">
                <button
                  type="button"
                  class="flex items-center gap-1.5 rounded-lg border border-border-subtle bg-surface-raised px-3 py-1.5 text-[12px] font-medium text-text-secondary transition-colors hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
                  :disabled="addable.length === 0"
                  @click="addOpen = !addOpen"
                >
                  <Plus class="h-3.5 w-3.5" :stroke-width="2" /> Add tile
                </button>
                <div
                  v-if="addOpen && addable.length"
                  class="absolute right-0 z-20 mt-1 max-h-64 w-56 overflow-auto rounded-lg border border-border-subtle bg-surface-overlay py-1 shadow-lg"
                >
                  <button
                    v-for="name in addable"
                    :key="name"
                    type="button"
                    class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[12px] text-text-secondary transition-colors hover:bg-surface-raised hover:text-text-primary"
                    @click="onAdd(name)"
                  >
                    <Puzzle class="h-3.5 w-3.5 flex-shrink-0 text-text-muted" :stroke-width="1.75" />
                    <span class="truncate">{{ providerFor(name)?.displayName ?? name }}</span>
                  </button>
                </div>
              </div>
              <button
                type="button"
                class="flex items-center gap-1.5 rounded-lg border border-border-subtle bg-surface-raised px-3 py-1.5 text-[12px] font-medium text-text-secondary transition-colors hover:text-text-primary"
                @click="dash.reset()"
              >
                <RotateCcw class="h-3.5 w-3.5" :stroke-width="2" /> Reset
              </button>
              <button
                type="button"
                class="flex items-center gap-1.5 rounded-lg border border-accent/40 bg-accent/10 px-3 py-1.5 text-[12px] font-medium text-accent transition-colors hover:bg-accent/15"
                @click="toggleEdit"
              >
                <Check class="h-3.5 w-3.5" :stroke-width="2" /> Done
              </button>
            </template>
            <button
              v-else
              type="button"
              class="flex items-center gap-1.5 rounded-lg border border-border-subtle bg-surface-raised px-3 py-1.5 text-[12px] font-medium text-text-secondary transition-colors hover:text-text-primary"
              @click="toggleEdit"
            >
              <LayoutGrid class="h-3.5 w-3.5" :stroke-width="2" /> Customize
            </button>
          </div>
        </div>

        <!-- Every tile removed/hidden: nothing to show, but the catalog
             still has providers — point the user at the add menu. -->
        <div
          v-if="layout.length === 0"
          class="flex items-start gap-3 rounded-xl border border-border-subtle bg-surface-raised/60 p-4 text-[13px] text-text-muted"
        >
          <LayoutGrid class="mt-0.5 h-4 w-4 text-text-muted" :stroke-width="1.75" />
          <div>
            <div class="font-medium text-text-secondary">Your dashboard is empty</div>
            <div class="mt-1 text-xs">
              You've removed all tiles. Use <button class="text-accent hover:text-accent-hover" @click="editMode = true; addOpen = true">Customize → Add tile</button> to bring them back.
            </div>
          </div>
        </div>

        <GridLayout
          v-else
          v-model:layout="layout"
          :col-num="GRID_COLS"
          :row-height="90"
          :margin="[16, 16]"
          :is-draggable="editMode"
          :is-resizable="editMode"
          :is-bounded="true"
          :vertical-compact="true"
          @layout-updated="onLayoutUpdated"
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
          >
            <DashboardTile
              v-if="providerFor(item.i)"
              :provider="providerFor(item.i)!"
              :edit-mode="editMode"
              @no-tile="onNoTile"
              @remove="onRemove"
            />
          </GridItem>
        </GridLayout>
      </template>
    </template>
  </AppLayout>
</template>
