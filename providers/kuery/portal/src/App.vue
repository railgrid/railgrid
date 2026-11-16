<script setup lang="ts">
import InlineNotification from './portalkit/InlineNotification.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { ArrowRight, Braces, ChevronDown, Inbox, Network, TableProperties } from 'lucide-vue-next'

import type { ObjectResult } from './api'
import type { RailgridContext } from './element'
import { createKueryRequestContext, safeHTTPStatus } from './kuery'
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
const shell = ref<HTMLElement | null>(null)
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

/**
 * load reads connected KubernetesCluster CRs from the edges provider's own
 * binding. It also makes sure the caller's scratch view exists, because every
 * ad-hoc query below runs as a verb on it.
 */
async function load(): Promise<void> {
  const current = ++requestID
  const request = requestContext.value
  const hostFetch = context.value?.fetch
  const isCurrent = (): boolean => requestID === current && requestContext.value.identity === request.identity && context.value?.fetch === hostFetch
  const kube = kubeClientFor(request)
  if (!kube) {
    // No host transport or no workspace yet. Not an error: the shell renders
    // its empty state until the host hands one over.
    if (isCurrent()) loading.value = false
    return
  }
  loading.value = true
  try {
    const name = await playgroundViewName(request.user)
    await ensurePlaygroundView(kube, name, request.user)
    const discovered = await listEdges(kube)
    if (!isCurrent()) return
    scratchView.value = name
    edges.value = discovered
    loaded.value = true
    error.value = ''; errorDetail.value = ''
  } catch (reason) {
    if (!isCurrent()) return
    error.value = 'Could not check this workspace.'
    const status = safeHTTPStatus(reason)
    errorDetail.value = status === null ? '' : `Workspace request returned HTTP ${status}.`
  } finally {
    if (isCurrent()) loading.value = false
  }
}

async function retryLoad(): Promise<void> {
  if (loading.value) return
  const focused = typeof document === 'undefined' ? null : document.activeElement
  await load()
  await nextTick()
  if (focused && !focused.isConnected && document.activeElement === document.body) {
    ;(shell.value?.querySelector<HTMLElement>('.k-first-run__actions button') ?? shell.value?.querySelector<HTMLElement>('.k-tabs [aria-current="page"]'))?.focus()
  }
}

watch([identity, token, () => context.value?.fetch], ([currentIdentity, currentToken, hostFetch], previous) => {
  const [previousIdentity, previousToken, previousFetch] = previous ?? []
  const authorityChanged = currentIdentity !== previousIdentity || hostFetch !== previousFetch ||
    (!hostFetch && currentToken !== previousToken)
  if (authorityChanged) {
    requestID += 1
    impact.value = null
    edges.value = []
    scratchView.value = ''
    loaded.value = false
    loading.value = false
    error.value = ''
    errorDetail.value = ''
  }
  if (authorityChanged || currentToken !== previousToken) void load()
}, { immediate: true })
onBeforeUnmount(() => { requestID += 1 })
</script>

<template>
  <div ref="shell" class="kuery-shell">
    <ImpactView v-if="impact" :key="`${identity}:impact`" :context="context" :anchor="impact" :saved-view="scratchView" @back="impact = null" @inspect="impact = $event" />
    <div v-show="!impact" class="kuery-collection-surfaces">
      <div class="kuery-topbar">
        <Tabs :tabs="tabs" :active="active" aria-label="Kuery views" @select="selectTab" />
        <StatusBadge v-if="loaded && edges.length > 0" :status="edgeStatus" tone="success" role="status" aria-live="polite" aria-atomic="true" />
      </div>
      <InlineNotification v-if="error && loaded" tone="error" :action-label="edges.length > 0 ? 'Retry' : ''" action-busy-label="Retrying…" :action-busy="loading" @action="retryLoad">
        <div class="kuery-error-copy">
          <span>Could not refresh workspace setup. The last loaded state is still shown.</span>
          <details v-if="errorDetail" class="k-resource-technical">
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
      </InlineNotification>
      <div v-if="!requestContext.ready" class="kuery-read-state" role="status">Waiting for workspace context…</div>
      <div v-else-if="loading && !loaded && !error" class="kuery-read-state" role="status">Checking workspace setup…</div>
      <InlineNotification v-else-if="error && !loaded" tone="error" action-label="Retry" action-busy-label="Retrying…" :action-busy="loading" @action="retryLoad">
        <div class="kuery-error-copy">
          <span>{{ error }}</span>
          <details v-if="errorDetail" class="k-resource-technical">
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
      </InlineNotification>
      <FirstRunGuide
        v-else-if="loaded && edges.length === 0"
        title="Connect your first Kubernetes edge"
        description="Kuery builds a searchable inventory and topology from Kubernetes resources in this workspace."
        primary-label="Connect edge"
        :steps="firstRunSteps"
        journey-label="Kuery setup"
      >
        <template #icon><Inbox :size="24" :stroke-width="1.5" aria-hidden="true" /></template>
        <template #actions><button class="k-btn k-btn--primary" type="button" @click="openEdgeConnection">Connect edge <ArrowRight :stroke-width="1.75" aria-hidden="true" /></button><button class="k-btn k-btn--ghost" type="button" :aria-disabled="loading || undefined" :aria-busy="loading || undefined" @click="retryLoad">{{ loading ? 'Checking…' : 'Check again' }}</button></template>
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
