<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Braces, Network, TableProperties } from 'lucide-vue-next'

import type { ObjectResult } from './api'
import type { RailgridContext } from './element'
import { createKueryRequestContext, errorMessage } from './kuery'
import { ensurePlaygroundView, kubeClientFor, listEdges, listSavedViews, playgroundViewName, type SavedView } from './savedviews'
import ImpactView from './components/ImpactView.vue'
import InventoryView from './components/InventoryView.vue'
import PlaygroundView from './components/PlaygroundView.vue'
import TopologyView from './components/TopologyView.vue'
import Tabs from './portalkit/Tabs.vue'

const props = defineProps<{ state: { context: RailgridContext | null } }>()
const context = computed(() => props.state.context)
const requestContext = computed(() => createKueryRequestContext(context.value))
const identity = computed(() => requestContext.value.scopeIdentity)
const token = computed(() => requestContext.value.token)
type TabID = 'topology' | 'inventory' | 'playground'
const active = ref<TabID>('topology')
const visited = ref<Record<TabID, boolean>>({ topology: true, inventory: false, playground: false })
const impact = ref<ObjectResult | null>(null)
const edges = ref<string[]>([])
const savedViews = ref<SavedView[]>([])
// The signed-in user's scratch SavedView. Every ad-hoc query in this shell
// runs as the 'run' verb on it, so there is no query surface outside the
// tenant's own RBAC.
const scratchView = ref('')
const loaded = ref(false)
const loading = ref(false)
const error = ref('')
let requestID = 0

const tabs = [
  { id: 'topology', label: 'Topology', icon: Network },
  { id: 'inventory', label: 'Inventory', icon: TableProperties },
  { id: 'playground', label: 'Playground', icon: Braces },
]

function selectTab(id: string): void {
  if (id === 'topology' || id === 'inventory' || id === 'playground') {
    active.value = id
    visited.value[id] = true
  }
}

/**
 * load reads the workspace directly: its edges are KubernetesCluster CRs from
 * the edges provider's own binding, and its saved views are kuery's. Neither
 * goes through the provider any more — the provider has exactly one route, the
 * query verb, and a listing is not a query.
 *
 * It also makes sure the caller's scratch view exists, because every ad-hoc
 * query below runs as a verb on it.
 */
async function load(): Promise<void> {
  const current = ++requestID
  const request = requestContext.value
  const isCurrent = (): boolean => requestID === current
  const kube = kubeClientFor(request)
  if (!kube) {
    // No host transport or no workspace yet. Not an error: the shell renders
    // its empty state until the host hands one over.
    if (isCurrent()) loading.value = false
    return
  }
  loading.value = true
  error.value = ''
  try {
    const name = await playgroundViewName(request.user)
    await ensurePlaygroundView(kube, name, request.user)
    const [discovered, views] = await Promise.all([listEdges(kube), listSavedViews(kube)])
    if (!isCurrent()) return
    scratchView.value = name
    edges.value = discovered
    savedViews.value = views
    loaded.value = true
  } catch (reason) {
    if (!isCurrent()) return
    const message = errorMessage(reason, 'Retry, or check that Edges and Kuery are both enabled in this workspace.')
    if (message) error.value = message
  } finally {
    if (isCurrent()) loading.value = false
  }
}

watch(identity, () => {
  impact.value = null
  edges.value = []
  savedViews.value = []
  scratchView.value = ''
  loaded.value = false
  loading.value = false
  error.value = ''
  void load()
}, { immediate: true })

// A token refresh on an older host changes nothing the kube client cares
// about, but it does fence an in-flight read, so re-run once it settles.
watch([identity, token], ([currentIdentity, currentToken], [previousIdentity, previousToken]) => {
  if (currentIdentity === previousIdentity && currentToken !== previousToken) void load()
})
onBeforeUnmount(() => { requestID += 1 })
</script>

<template>
  <div class="kuery-shell">
    <ImpactView v-if="impact" :key="`${identity}:impact`" :context="context" :anchor="impact" :saved-view="scratchView" @back="impact = null" @inspect="impact = $event" />
    <div v-show="!impact" class="kuery-collection-surfaces">
      <div class="kuery-topbar">
        <Tabs :tabs="tabs" :active="active" aria-label="Kuery views" @select="selectTab" />
        <span class="k-badge" :class="edges.length ? 'k-badge--success' : 'k-badge--warning'" role="status" aria-live="polite" aria-atomic="true">
          {{ loading && !loaded ? 'Discovering edges' : `${edges.length} edge${edges.length === 1 ? '' : 's'} connected` }}
        </span>
        <span v-if="loaded" class="k-badge" role="status">
          {{ savedViews.length }} saved view{{ savedViews.length === 1 ? '' : 's' }}
        </span>
      </div>
      <div v-if="error" class="kuery-inline-error" role="alert">
        <span>{{ error }}</span><button type="button" class="k-btn k-btn--ghost" @click="load">Retry</button>
      </div>
      <TopologyView v-show="active === 'topology'" :key="`${identity}:topology`" :context="context" :edges="edges" :saved-view="scratchView" :active="!impact && active === 'topology'" @inspect="impact = $event" />
      <InventoryView v-if="visited.inventory" v-show="active === 'inventory'" :key="`${identity}:inventory`" :context="context" :edges="edges" :saved-view="scratchView" @inspect="impact = $event" />
      <PlaygroundView v-if="visited.playground" v-show="active === 'playground'" :key="`${identity}:playground`" :context="context" :saved-view="scratchView" :active="!impact && active === 'playground'" />
    </div>
  </div>
</template>
