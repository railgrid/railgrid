<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Braces, ChevronDown, Inbox, Network, TableProperties } from 'lucide-vue-next'

import type { ObjectResult } from './api'
import type { RailgridContext } from './element'
import { createKueryRequestContext } from './kuery'
import { ensurePlaygroundView, kubeClientFor, listEdges, playgroundViewName } from './savedviews'
import ImpactView from './components/ImpactView.vue'
import InventoryView from './components/InventoryView.vue'
import PlaygroundView from './components/PlaygroundView.vue'
import TopologyView from './components/TopologyView.vue'
import FirstRunGuide from './portalkit/FirstRunGuide.vue'
import Tabs from './portalkit/Tabs.vue'
import { portalHref } from './portalkit/navigation'

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
// The signed-in user's scratch SavedView. Every ad-hoc query in this shell
// runs as the 'run' verb on it, so there is no query surface outside the
// tenant's own RBAC.
const scratchView = ref('')
const loaded = ref(false)
const loading = ref(false)
const error = ref('')
const errorDetail = ref('')
let requestID = 0

const edgeStatus = computed(() => `${edges.value.length} connected Kubernetes edge${edges.value.length === 1 ? '' : 's'}`)

const firstRunSteps = [
  { label: 'Connect a Kubernetes edge', description: 'Connect a cluster from the Edges provider in this workspace.' },
  { label: 'Kuery starts syncing', description: 'Kuery makes the connected cluster available for queries.' },
  { label: 'Explore your fleet', description: 'Browse resources in Inventory and relationships in Topology.' },
] as const

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

function openEdgeConnection(): void {
  window.location.assign(portalHref('/providers/edges/connect/edge'))
}

function errorDetailText(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason)
}

/**
 * load reads connected KubernetesCluster CRs from the edges provider's own
 * binding. It also makes sure the caller's scratch view exists, because every
 * ad-hoc query below runs as a verb on it.
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
  errorDetail.value = ''
  try {
    const name = await playgroundViewName(request.user)
    await ensurePlaygroundView(kube, name, request.user)
    const discovered = await listEdges(kube)
    if (!isCurrent()) return
    scratchView.value = name
    edges.value = discovered
    loaded.value = true
  } catch (reason) {
    if (!isCurrent()) return
    error.value = 'Could not check this workspace.'
    errorDetail.value = errorDetailText(reason)
  } finally {
    if (isCurrent()) loading.value = false
  }
}

watch(identity, () => {
  impact.value = null
  edges.value = []
  scratchView.value = ''
  loaded.value = false
  loading.value = false
  error.value = ''
  errorDetail.value = ''
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
        <span v-if="loaded && edges.length > 0" class="k-badge k-badge--success" role="status" aria-live="polite" aria-atomic="true">
          {{ edgeStatus }}
        </span>
      </div>
      <div v-if="error && loaded" class="kuery-inline-error" role="alert">
        <div class="kuery-error-copy">
          <span>Could not refresh workspace setup. The last loaded state is still shown.</span>
          <details class="k-resource-technical">
            <summary class="k-resource-technical__summary">
              <span class="k-resource-technical__summary-label">Technical details</span>
              <ChevronDown class="k-resource-technical__chevron" :size="14" aria-hidden="true" />
            </summary>
            <div class="k-resource-technical__body">
              <section class="k-resource-technical__section">
                <h3 class="k-resource-technical__section-title">Workspace read</h3>
                <div class="k-resource-technical__content"><pre class="k-resource-technical__pre">{{ errorDetail }}</pre></div>
              </section>
            </div>
          </details>
        </div>
        <button type="button" class="k-btn k-btn--ghost" :disabled="loading" :aria-busy="loading" @click="load">{{ loading ? 'Retrying…' : 'Retry' }}</button>
      </div>
      <div v-if="!requestContext.ready" class="kuery-read-state" role="status">Waiting for workspace context…</div>
      <div v-else-if="loading && !loaded" class="kuery-read-state" role="status">Checking workspace setup…</div>
      <div v-else-if="error && !loaded" class="kuery-read-state kuery-error" role="alert">
        <div class="kuery-error-copy">
          <span>{{ error }}</span>
          <details class="k-resource-technical">
            <summary class="k-resource-technical__summary">
              <span class="k-resource-technical__summary-label">Technical details</span>
              <ChevronDown class="k-resource-technical__chevron" :size="14" aria-hidden="true" />
            </summary>
            <div class="k-resource-technical__body">
              <section class="k-resource-technical__section">
                <h3 class="k-resource-technical__section-title">Workspace read</h3>
                <div class="k-resource-technical__content"><pre class="k-resource-technical__pre">{{ errorDetail }}</pre></div>
              </section>
            </div>
          </details>
        </div>
        <button type="button" class="k-btn k-btn--ghost" :disabled="loading" :aria-busy="loading" @click="load">{{ loading ? 'Retrying…' : 'Retry' }}</button>
      </div>
      <FirstRunGuide
        v-else-if="loaded && edges.length === 0"
        title="Connect your first Kubernetes edge"
        description="Kuery builds a searchable inventory and topology from Kubernetes resources in this workspace."
        primary-label="Connect edge"
        secondary-label="Check again"
        :steps="firstRunSteps"
        journey-label="Kuery setup"
        @primary="openEdgeConnection"
        @secondary="load"
      >
        <template #icon><Inbox :size="24" :stroke-width="1.5" aria-hidden="true" /></template>
      </FirstRunGuide>
      <template v-else-if="loaded">
        <TopologyView v-show="active === 'topology'" :key="`${identity}:topology`" :context="context" :edges="edges" :saved-view="scratchView" :active="!impact && active === 'topology'" @inspect="impact = $event" />
        <InventoryView v-if="visited.inventory" v-show="active === 'inventory'" :key="`${identity}:inventory`" :context="context" :edges="edges" :saved-view="scratchView" @inspect="impact = $event" />
        <PlaygroundView v-if="visited.playground" v-show="active === 'playground'" :key="`${identity}:playground`" :context="context" :saved-view="scratchView" :active="!impact && active === 'playground'" />
      </template>
      <div v-else class="kuery-read-state" role="status">Checking workspace setup…</div>
    </div>
  </div>
</template>
