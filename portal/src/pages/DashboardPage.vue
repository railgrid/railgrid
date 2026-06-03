<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import DashboardTile from '@/components/DashboardTile.vue'
import FirstEdgeWizard from '@/components/FirstEdgeWizard.vue'
import { useProvidersStore } from '@/stores/providers'
import { useGraphQLQuery } from '@/composables/useGraphQL'
import { LIST_EDGES, type ListEdgesResult } from '@/graphql/queries/edges'
import { Puzzle } from 'lucide-vue-next'

// The dashboard used to query LIST_EDGES directly and render fleet
// health / connectivity / recent edges. That hardcoded "this hub is
// about Kubernetes edges" into the portal, which the BYO-provider work
// invalidated: server-edges, mcp, and any third-party provider deserve
// equal billing on the dashboard, and the portal shouldn't need to
// learn about each one's schema.
//
// Instead: iterate the catalog and mount one <DashboardTile> per
// ready provider. Each provider may register a
// <kedge-dashboard-tile-{name}> custom element in its main.js — that
// element owns its own data fetch, summary rendering, and click-through
// URLs. Providers without a tile get a fallback card linking to their
// page.
//
// LIST_EDGES is still kept here for one job: deciding whether to show
// FirstEdgeWizard on initial load. That's tenant-level onboarding (no
// edges of any kind connected yet) and lives at the dashboard level
// rather than inside any one provider's tile.

const providers = useProvidersStore()
const { data: edgesData, loading: edgesLoading, error: edgesError, refetch: refetchEdges } = useGraphQLQuery<ListEdgesResult>(LIST_EDGES, undefined, 10000)
const edges = computed(() => edgesData.value?.kedge_faros_sh?.v1alpha1?.Edges?.items ?? [])

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

// Latch the wizard on first load. Once open it stays open until the
// wizard itself completes, even if LIST_EDGES updates underneath.
const wizardOpen = ref<boolean | null>(null)
watch(
  [edgesLoading, edgesError, edges],
  () => {
    if (wizardOpen.value !== null) return
    if (edgesLoading.value) return
    if (edgesError.value) return
    wizardOpen.value = edges.value.length === 0
  },
  { immediate: true },
)
const showWizard = computed(() => wizardOpen.value === true)

function onWizardConnected() {
  wizardOpen.value = false
  refetchEdges()
}
</script>

<template>
  <AppLayout>
    <div v-if="edgesError" class="flex items-center gap-2 rounded-xl border border-danger/20 bg-danger-subtle p-4 text-[13px] text-danger">
      {{ edgesError }}
    </div>

    <div v-else-if="(edgesLoading && !edgesData) || providers.loading" class="mt-20 flex flex-col items-center justify-center">
      <div class="shimmer h-8 w-8 rounded-xl" />
      <div class="shimmer mt-4 h-3 w-40 rounded" />
    </div>

    <div v-else-if="showWizard" class="py-8">
      <FirstEdgeWizard @connected="onWizardConnected" />
    </div>

    <template v-else>
      <div v-if="tiles.length === 0" class="flex items-start gap-3 rounded-xl border border-border-subtle bg-surface-raised/60 p-4 text-[13px] text-text-muted">
        <Puzzle class="mt-0.5 h-4 w-4 text-text-muted" :stroke-width="1.75" />
        <div>
          <div class="font-medium text-text-secondary">No providers enabled in this workspace</div>
          <div class="mt-1 text-xs">
            Enable a provider from the <router-link to="/providers" class="text-accent hover:text-accent-hover">catalog</router-link> to see a dashboard summary.
            Built-in providers (Kubernetes, Linux, MCP) appear here automatically; third-party providers need a per-workspace Enable.
          </div>
        </div>
      </div>

      <div v-else class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <DashboardTile v-for="p in tiles" :key="p.name" :provider="p" />
      </div>
    </template>
  </AppLayout>
</template>
