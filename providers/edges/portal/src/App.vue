<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import { Server, Boxes, Plug } from 'lucide-vue-next'
import { setHostFetch, setToken, setTenant, listEdges, deleteEdge } from './api'
import Wizard from './Wizard.vue'
import Detail from './Detail.vue'
import EdgeCollection from './EdgeCollection.vue'
import Workloads from './Workloads.vue'
import Services from './Services.vue'
import ServiceCreate from './ServiceCreate.vue'
import WorkloadCreate from './WorkloadCreate.vue'
import ConfirmDialog from './portalkit/ConfirmDialog.vue'
import Tabs from './portalkit/Tabs.vue'
import { confirmDialog } from './portalkit/confirm'
import { toast } from './portalkit/toast'
import {
  FAST_REFRESH_MS,
  STABLE_REFRESH_MS,
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  type ResourceRefreshMode,
} from './refresh'
import type { Edge, EdgeType, FarosContext, ErrorResponse } from './types'
import {
  edgeConnectPath,
  edgeConnectionCancelPath,
  edgeConnectionSuccessPath,
  edgeDetailPath,
  navigationDetail,
  parseSubPath,
  serviceCreatePath,
  serviceDetailPath,
  workloadDeployPath,
  type EdgeConnectOptions,
  type EdgeRoute,
} from './routes'

const props = defineProps<{ ctx: FarosContext | null }>()

// The shell owns the provider prefix and passes only the trailing path. Every
// consequential action is represented here so browser refresh/back/forward
// return to the same page and no stale local overlay can capture the UI.
const route = computed<EdgeRoute>(() => parseSubPath(props.ctx?.subPath))
const view = computed(() => route.value.page)

const edgeRouteTabs = [
  { id: 'edges', label: 'Edges', icon: Server },
  { id: 'workloads', label: 'Workloads', icon: Boxes },
  { id: 'services', label: 'Services', icon: Plug },
] as const

// navigate pushes the shell's router via a bubbling CustomEvent the element's
// ProviderFrame host listens for. path is relative to /providers/edges/.
const rootRef = ref<HTMLElement | null>(null)
function navigate(path: string, replace = false) {
  rootRef.value?.dispatchEvent(new CustomEvent('faros-navigate', {
    detail: navigationDetail(path, replace),
    bubbles: true,
  }))
}

function selectView(id: string): void {
  // Keep the provider landing page canonical at /providers/edges; the parser
  // also accepts the explicit `edges` collection alias for deep links.
  navigate(id === 'edges' ? '' : id)
}

function onServiceNavigate(name: string | null, options?: { replace?: boolean }): void {
  navigate(name === null ? 'services' : serviceDetailPath(name), options?.replace === true)
}

// The collection owns its first-run surface once the first authoritative read
// settles. Consequential connection work remains on the explicit connect route.
const firstLoadDone = ref(false)
const workloadResult = ref<string | null>(null)

function onEdgeCreated(name: string, type: EdgeType): void {
  navigate(edgeConnectionSuccessPath(route.value.connect?.successPath, type, name), true)
}
function connectEdgeFrom(successPath: string, options: EdgeConnectOptions = {}): void {
  // Replace the route-owned form with its prerequisite. Returning from the
  // wizard then restores one form entry instead of leaving a duplicate form in
  // browser history that Back would reopen after the user exits the flow.
  navigate(edgeConnectPath(successPath, options), true)
}
function cancelEdgeConnection(): void {
  navigate(edgeConnectionCancelPath(route.value.connect?.cancelPath), true)
}
const edgeConnectionCancelLabel = computed(() => {
  const cancelPath = route.value.connect?.cancelPath
  if (cancelPath === 'services') return 'Back to services'
  if (cancelPath === 'workloads') return 'Back to workloads'
  if (cancelPath?.startsWith('create/service')) return 'Back to service setup'
  if (cancelPath?.startsWith('deploy/workload')) return 'Back to workload setup'
  return 'Back to edges'
})
function onServiceCreated(name: string): void {
  navigate(serviceDetailPath(name), true)
}
function onEdgeDeleted(): void {
  navigate('', true)
  void refresh()
}
function onWorkloadCompleted(message: string): void {
  workloadResult.value = message
  navigate('workloads', true)
}
function onWorkloadDismissResult(): void {
  workloadResult.value = null
}

function openDetail(row: Record<string, unknown>): void {
  const type = row.type === 'server' ? 'server' : 'kubernetes'
  navigate(edgeDetailPath(type, String(row.name)))
}

const edges = ref<Edge[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const foregroundLoading = computed(() => loading.value && refreshMode.value === 'foreground')
// Nested list views keep their own cursor/cache authority. Remount them when
// the shell changes tenant or token so a prior workspace's rows and cursors
// cannot remain visible while the new context is loading.
const contextGeneration = ref(0)

const poller = createAdaptiveRefreshTimer(() => {
  if (props.ctx?.tenant) void refresh('background')
}, () => {
  if (!firstLoadDone.value || error.value) return FAST_REFRESH_MS
  const unsettled = edges.value.some(edge => !edge.connected || ['pending', 'provisioning', 'deleting'].includes((edge.phase || '').toLowerCase()))
  return unsettled ? FAST_REFRESH_MS : STABLE_REFRESH_MS
})

const edgeRefresh = createLatestRefreshController(async (requestID, mode) => {
  refreshMode.value = mode
  loading.value = true
  if (mode === 'foreground') error.value = null
  try {
    const nextEdges = await listEdges()
    if (!edgeRefresh.isCurrent(requestID)) return
    edges.value = nextEdges
    error.value = null
    firstLoadDone.value = true
  } catch (e) {
    if (!edgeRefresh.isCurrent(requestID)) return
    error.value = (e as ErrorResponse)?.message ?? 'Failed to load edges'
  } finally {
    if (edgeRefresh.isCurrent(requestID)) loading.value = false
    poller.schedule()
  }
})

function refresh(mode: ResourceRefreshMode | Event = 'foreground') {
  const requestedMode = typeof mode === 'string' ? mode : 'foreground'
  if (requestedMode === 'foreground') {
    refreshMode.value = 'foreground'
    loading.value = true
  }
  return edgeRefresh.request(requestedMode)
}

function onEdgeCollectionActivated(): void {
  // The first activation happens before the initial authoritative read, so
  // avoid a duplicate request. Every later activation follows a detail/action
  // route and revalidates rows without remounting ResourceTable's state.
  if (firstLoadDone.value) void refresh('foreground')
}

async function onDelete(edge: Edge) {
  if (!(await confirmDialog({ title: `Delete ${edge.type === 'server' ? 'server' : 'cluster'} "${edge.name}"?`, danger: true, confirmLabel: 'Delete' }))) return
  const expectedContextGeneration = contextGeneration.value
  try {
    await deleteEdge(edge)
    if (contextGeneration.value !== expectedContextGeneration) return
    toast('info', `${edge.type === 'server' ? 'Server' : 'Cluster'} deletion requested for ${edge.name}.`)
    await refresh()
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Delete failed'
  }
}

// Re-auth + reload whenever the shell pushes a new context (token/workspace).
watch(
  () => [props.ctx?.token, props.ctx?.tenant, props.ctx?.user?.sub] as const,
  ([token, tenant, userSub], previous) => {
    const authorityChanged = !previous || tenant !== previous[1] || userSub !== previous[2]
    setHostFetch(props.ctx?.fetch)
    setToken(token ?? null)
    setTenant(tenant ?? null)
    if (authorityChanged) {
      contextGeneration.value += 1
      edgeRefresh.invalidate()
      edges.value = []
      firstLoadDone.value = false
      workloadResult.value = null
      loading.value = Boolean(tenant)
      error.value = null
      if (tenant) void refresh('foreground')
      return
    }
    // Token rotation within the same user/workspace is a credential refresh,
    // not a resource identity change. Preserve the authoritative snapshot and
    // quietly revalidate it with the new token.
    if (tenant) void refresh('background')
  },
  { immediate: true },
)

onUnmounted(() => {
  edgeRefresh.stop()
  poller.stop()
})

</script>

<template>
  <div ref="rootRef" class="edges-app" :key="contextGeneration">
    <Tabs
      v-if="!route.edge && !route.service && !route.connect && !route.create && !route.deploy"
      :tabs="edgeRouteTabs"
      :active="view"
      aria-label="Edges sections"
      @select="selectView"
    />

    <ServiceCreate
      v-if="route.create?.resource === 'service'"
      :key="`create-service:${contextGeneration}:${route.create.edgeType ?? 'any'}:${route.create.edgeName ?? ''}`"
      :initial-edge-type="route.create.edgeType"
      :initial-edge-name="route.create.edgeName"
      @cancel="navigate('services', true)"
      @created="onServiceCreated"
      @connect-edge="connectEdgeFrom(serviceCreatePath(route.create.edgeType, route.create.edgeName))"
    />
    <WorkloadCreate
      v-if="route.deploy?.resource === 'workload'"
      :key="`deploy-workload:${contextGeneration}:${route.deploy.mode}:${route.deploy.app ?? ''}`"
      :mode="route.deploy.mode"
      :app-type="route.deploy.app"
      @cancel="navigate('workloads', true)"
      @completed="onWorkloadCompleted"
      @connect-edge="connectEdgeFrom(workloadDeployPath(route.deploy.mode, route.deploy.app), { requiredType: 'kubernetes' })"
    />
    <Wizard
      v-if="route.connect?.resource === 'edge'"
      :cluster="props.ctx?.tenant ?? null"
      :required-type="route.connect.requiredType"
      :cancel-label="edgeConnectionCancelLabel"
      @cancel="cancelEdgeConnection"
      @created="onEdgeCreated"
    />
    <Detail
      v-if="route.edge"
      :key="`edge-detail:${contextGeneration}:${route.edge.type}:${route.edge.name}`"
      :name="route.edge.name"
      :type="route.edge.type"
      :cluster="props.ctx?.tenant ?? null"
      :token="props.ctx?.token ?? null"
      @back="navigate('', true)"
      @deleted="onEdgeDeleted"
      @add-service="navigate(serviceCreatePath(route.edge.type, route.edge.name))"
    />
    <Services
      v-if="route.page === 'services' && route.service"
      :key="`service-detail:${contextGeneration}:${route.service}`"
      :selected-name="route.service"
      @navigate="onServiceNavigate"
    />
    <!-- Keep collection instances alive while a route-owned create/detail page
         is active, so search/filter/page/scroll state returns unchanged. The
         route still owns every consequential form; this only preserves the
         read-only collection snapshot behind it. -->
    <KeepAlive>
      <Services
        v-if="route.page === 'services' && !route.service && !route.create"
        :key="`services:${contextGeneration}`"
        @navigate="onServiceNavigate"
        @create="navigate('create/service')"
        @connect-edge="connectEdgeFrom('create/service', { cancelPath: 'services' })"
      />
    </KeepAlive>
    <KeepAlive>
      <Workloads
        v-if="route.page === 'workloads' && !route.deploy"
        :key="`workloads:${contextGeneration}`"
        :result="workloadResult"
        @create="navigate('deploy/workload/manual')"
        @connect-edge="connectEdgeFrom('deploy/workload/manual', { cancelPath: 'workloads', requiredType: 'kubernetes' })"
        @deploy="(app) => navigate(workloadDeployPath('marketplace', app.type))"
        @dismiss-result="onWorkloadDismissResult"
      />
    </KeepAlive>
    <KeepAlive>
      <EdgeCollection
        v-if="route.page === 'edges' && !route.edge && !route.connect"
        :key="`edges:${contextGeneration}`"
        :edges="edges"
        :loaded="firstLoadDone"
        :loading="loading"
        :refresh-mode="refreshMode"
        :error="error"
        :foreground-loading="foregroundLoading"
        @activated="onEdgeCollectionActivated"
        @refresh="refresh"
        @open="openDetail"
        @delete="onDelete"
        @connect="navigate(edgeConnectPath())"
      />
    </KeepAlive>
    <ConfirmDialog />
  </div>
</template>
