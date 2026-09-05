<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Braces, Network, TableProperties } from 'lucide-vue-next'

import type { ObjectResult } from './api'
import type { FarosContext } from './element'
import { createKueryRequestContext, errorMessage } from './kuery'
import ImpactView from './components/ImpactView.vue'
import InventoryView from './components/InventoryView.vue'
import PlaygroundView from './components/PlaygroundView.vue'
import TopologyView from './components/TopologyView.vue'
import Tabs from './portalkit/Tabs.vue'

const props = defineProps<{ state: { context: FarosContext | null } }>()
const context = computed(() => props.state.context)
const requestContext = computed(() => createKueryRequestContext(context.value))
const identity = computed(() => requestContext.value.scopeIdentity)
const token = computed(() => requestContext.value.token)
type TabID = 'topology' | 'inventory' | 'playground'
const active = ref<TabID>('topology')
const visited = ref<Record<TabID, boolean>>({ topology: true, inventory: false, playground: false })
const impact = ref<ObjectResult | null>(null)
const edges = ref<string[]>([])
const edgesLoaded = ref(false)
const edgesLoading = ref(false)
const edgesError = ref('')
let edgesController: AbortController | null = null
let edgesRequestID = 0

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

async function loadEdges(): Promise<void> {
  const requestID = ++edgesRequestID
  const request = requestContext.value
  const requestIdentity = request.identity
  edgesController?.abort()
  edgesController = null
  const isCurrent = (): boolean =>
    edgesRequestID === requestID &&
    requestContext.value.identity === requestIdentity
  if (!request.basePath || !request.token) {
    if (isCurrent()) edgesLoading.value = false
    return
  }
  const controller = new AbortController()
  edgesController = controller
  edgesLoading.value = true
  edgesError.value = ''
  try {
    const response = await fetch(`${request.basePath}/api/edges`, {
      credentials: 'same-origin', headers: request.headers, signal: controller.signal,
    })
    const body = await response.text()
    if (!isCurrent()) return
    if (!response.ok) throw new Error(`Edge discovery failed (${response.status}): ${body.slice(0, 200)}`)
    const parsed = body ? JSON.parse(body) as { edges?: string[] } : {}
    if (!isCurrent()) return
    edges.value = parsed.edges ?? []
    edgesLoaded.value = true
  } catch (error) {
    if (!isCurrent()) return
    const message = errorMessage(error, 'Retry edge discovery.')
    if (message) edgesError.value = message
  } finally {
    if (isCurrent()) {
      edgesLoading.value = false
      if (edgesController === controller) edgesController = null
    }
  }
}

watch(identity, () => {
  impact.value = null
  edges.value = []
  edgesLoaded.value = false
  edgesLoading.value = false
  edgesError.value = ''
  void loadEdges()
}, { immediate: true })

watch([identity, token], ([currentIdentity, currentToken], [previousIdentity, previousToken]) => {
  if (currentIdentity === previousIdentity && currentToken !== previousToken) void loadEdges()
})
onBeforeUnmount(() => { edgesRequestID += 1; edgesController?.abort(); edgesController = null })
</script>

<template>
  <div class="kuery-shell">
    <ImpactView v-if="impact" :key="`${identity}:impact`" :context="context" :anchor="impact" @back="impact = null" @inspect="impact = $event" />
    <div v-show="!impact" class="kuery-collection-surfaces">
      <div class="kuery-topbar">
        <Tabs :tabs="tabs" :active="active" aria-label="Kuery views" @select="selectTab" />
        <span class="k-badge" :class="edges.length ? 'k-badge--success' : 'k-badge--warning'" role="status" aria-live="polite" aria-atomic="true">
          {{ edgesLoading && !edgesLoaded ? 'Discovering edges' : `${edges.length} edge${edges.length === 1 ? '' : 's'} engaged` }}
        </span>
      </div>
      <div v-if="edgesError" class="kuery-inline-error" role="alert">
        <span>{{ edgesError }}</span><button type="button" class="k-btn k-btn--ghost" @click="loadEdges">Retry</button>
      </div>
      <TopologyView v-show="active === 'topology'" :key="`${identity}:topology`" :context="context" :edges="edges" :active="!impact && active === 'topology'" @inspect="impact = $event" />
      <InventoryView v-if="visited.inventory" v-show="active === 'inventory'" :key="`${identity}:inventory`" :context="context" :edges="edges" @inspect="impact = $event" />
      <PlaygroundView v-if="visited.playground" v-show="active === 'playground'" :key="`${identity}:playground`" :context="context" :active="!impact && active === 'playground'" />
    </div>
  </div>
</template>
