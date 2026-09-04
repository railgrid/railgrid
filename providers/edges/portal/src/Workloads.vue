<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted, onActivated, watch } from 'vue'
import { RefreshCw, Plus, ChevronRight, ChevronDown, Store, Rocket, Boxes, Server } from 'lucide-vue-next'
import { listWorkloads, listWorkloadsPage, deleteWorkload, listEdges } from './api'
import type { Workload, Edge, ErrorResponse } from './types'
import { confirmDialog } from './portalkit/confirm'
import ResourceTable from './portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from './portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import FirstRunGuide from './portalkit/FirstRunGuide.vue'
import { MARKETPLACE_CATEGORIES, type MarketplaceApp } from './marketplace'
import { isCompleteFirstCursorPage, type ResourceTableChange, type TableFilterDefinition, type TablePageInfo } from './portalkit/table'
import { createFullListReadCoordinator, createInFlightReadCoordinator, hasActiveTableFilters, sameTableRequest, tablePageInfo as makeTablePageInfo, type PaginationMode, type TableRequestState } from './pagination'
import {
  FAST_REFRESH_MS,
  STABLE_REFRESH_MS,
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  type ResourceRefreshMode,
} from './refresh'

const props = defineProps<{ result?: string | null }>()
const emit = defineEmits<{
  create: []
  deploy: [app: MarketplaceApp]
  dismissResult: []
  connectEdge: []
}>()

const workloads = ref<Workload[]>([])
const edges = ref<Edge[]>([])
const loading = ref(true)
const loaded = ref(false)
const error = ref<string | null>(null)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const foregroundLoading = computed(() => loading.value && refreshMode.value === 'foreground')
const workloadColumns = [
  { key: 'expand', label: '', ariaLabel: 'Expand' },
  { key: 'name', label: 'Name', primary: true },
  { key: 'image', label: 'Image' },
  { key: 'placement', label: 'Placement' },
  { key: 'status', label: 'Status' },
  { key: 'ready', label: 'Ready', align: 'end' as const },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]
const workloadRows = computed<Array<Record<string, unknown>>>(() => workloads.value.map(workload => ({
  ...workload,
  expand: '',
  image: workload.image || '—',
  strategy: workload.strategy || 'Spread',
  placement: `${workload.strategy || 'Spread'} · ${selectorText(workload.selector)}`,
  status: workload.phase || 'Pending',
  ready: `${workload.readyReplicas ?? 0}/${workload.replicas ?? 1}`,
  actions: '',
})))
const kubernetesEdges = computed(() => edges.value.filter(edge => edge.type === 'kubernetes'))
const hasKubernetesEdges = computed(() => kubernetesEdges.value.length > 0)
const firstRunDismissed = ref(false)
const showFirstRun = computed(() => !firstRunDismissed.value && loaded.value && !error.value && workloads.value.length === 0 && isCompleteFirstCursorPage({
  page: tablePage.value,
  cursor: tableCursor.value,
  pageInfo: tablePageInfo.value,
}) && !hasActiveTableFilters(tableQuery.value, filterValues.value))
const workloadJourney = [
  { label: 'Kubernetes edge', description: 'Connect the cluster that will run the workload.' },
  { label: 'Workload and placement', description: 'Choose an image or chart and the edges it should target.' },
  { label: 'Placements running', description: 'Agents apply the workload and report readiness per edge.' },
]

function handleFirstRunPrimary(): void {
  if (hasKubernetesEdges.value) emit('create')
  else emit('connectEdge')
}

watch(hasKubernetesEdges, (available) => {
  if (!available) firstRunDismissed.value = false
})

const WORKLOAD_STRATEGY_OPTIONS = [
  { value: 'Spread', label: 'Spread' },
  { value: 'Singleton', label: 'Singleton' },
]
const WORKLOAD_STATUS_OPTIONS = [
  { value: 'Pending', label: 'Pending' },
  { value: 'Running', label: 'Running' },
  { value: 'Failed', label: 'Failed' },
  { value: 'Unknown', label: 'Unknown' },
]

type WorkloadFilterValues = {
  strategy: string
  status: string
}

type WorkloadPaginationMode = PaginationMode

const tableMode = ref<WorkloadPaginationMode>('server')
const tablePage = ref(1)
const tablePageSize = ref(10)
const tableQuery = ref('')
const filterValues = ref<WorkloadFilterValues>({ strategy: '', status: '' })
const tableCursor = ref<string | null>(null)
const tablePageInfo = ref<TablePageInfo | null>(null)
// Client-side filtering is authoritative only after a complete, query-
// independent cursor walk has committed. During a pending walk, keep the
// last complete rows visible and let newer query/filter edits join the same
// in-flight read rather than starting another walk.
const clientAuthorityReady = ref(false)
const clientReadPending = ref(false)
const completeWorkloadRead = createFullListReadCoordinator(() => listWorkloads())
// Edge joins are query-independent but must stay fresh for every server or
// client refresh. Share an in-flight join across clear/re-entry without
// retaining it as a long-lived cache.
const edgeRead = createInFlightReadCoordinator(() => listEdges())
const workloadFilters: TableFilterDefinition[] = [
  { key: 'strategy', label: 'Strategy', options: WORKLOAD_STRATEGY_OPTIONS },
  { key: 'status', label: 'Status', allLabel: 'Any status', options: WORKLOAD_STATUS_OPTIONS },
]

const showMarket = ref(true)

const expanded = ref<string | null>(null)
function toggle(name: string) {
  expanded.value = expanded.value === name ? null : name
}

type WorkloadTableRequest = Omit<TableRequestState, 'filters'> & { filters: WorkloadFilterValues }

let stopped = false
let clientReadToken = 0
let forceNextCompleteRead = false

function cloneWorkloadFilters(filters: WorkloadFilterValues): WorkloadFilterValues {
  return { strategy: filters.strategy, status: filters.status }
}

function currentWorkloadRequest(): WorkloadTableRequest {
  const filters = cloneWorkloadFilters(filterValues.value)
  return {
    mode: tableMode.value,
    active: hasActiveTableFilters(tableQuery.value, filters),
    page: tablePage.value,
    pageSize: tablePageSize.value,
    query: tableQuery.value,
    filters,
    cursor: tableCursor.value,
  }
}

function workloadRequestIsCurrent(requestID: number, request: WorkloadTableRequest): boolean {
  const current = currentWorkloadRequest()
  return !stopped && workloadRefresh.isCurrent(requestID) && sameTableRequest(current, request)
}

// A complete client read is query-independent. Query/filter-only changes are
// allowed to update the controlled table state while its target + edge join is
// pending; page, page-size, mode, mutation, and context changes still fail
// this guard through the stable request dimensions below.
function workloadClientReadIsCurrent(requestID: number, request: WorkloadTableRequest, readGeneration: number): boolean {
  const current = currentWorkloadRequest()
  return !stopped && workloadRefresh.isCurrent(requestID) && tableMode.value === 'client' &&
    current.active && current.page === request.page &&
    current.pageSize === request.pageSize && current.cursor === request.cursor &&
    completeWorkloadRead.generation() === readGeneration && clientAuthorityReady.value === false
}

const poller = createAdaptiveRefreshTimer(() => {
  void refresh('background')
}, () => {
  if (!loaded.value || error.value) return FAST_REFRESH_MS
  const unsettledWorkload = workloads.value.some(workload => (workload.phase || 'Pending').toLowerCase() !== 'running')
  const unsettledEdge = edges.value.some(edge => !edge.connected || ['pending', 'provisioning', 'deleting'].includes((edge.phase || '').toLowerCase()))
  return unsettledWorkload || unsettledEdge ? FAST_REFRESH_MS : STABLE_REFRESH_MS
})

const workloadRefresh = createLatestRefreshController(async (requestID, mode) => {
  const readToken = ++clientReadToken
  const request = currentWorkloadRequest()
  const wasClientAuthorityReady = clientAuthorityReady.value
  const forceCompleteRead = request.mode === 'client' && (wasClientAuthorityReady || forceNextCompleteRead)
  forceNextCompleteRead = false
  if (forceCompleteRead) {
    clientAuthorityReady.value = false
    // Keep the visible complete rows while a polling/CRUD replacement is in
    // flight, but make the old cache non-authoritative. Query edits during
    // this window therefore join the active walk instead of queueing another
    // forced walk; a failed replacement also cannot be mistaken for ready
    // data on the next edit.
    completeWorkloadRead.clear()
  }
  clientReadPending.value = request.mode === 'client'
  const readGeneration = completeWorkloadRead.generation()
  refreshMode.value = mode
  loading.value = true
  if (mode === 'foreground') error.value = null
  // Do not render an unfiltered page as though it matched a newly-entered
  // query/filter. Same-page polling keeps cached rows visible until the
  // replacement arrives.
  if (mode === 'foreground' && request.active && request.mode === 'server') {
    workloads.value = []
    tablePageInfo.value = null
  }
  try {
    if (request.active || request.mode === 'client') {
      // A query-independent full read is shared across rapid query/filter
      // edits. Polling and CRUD refreshes force one fresh walk; query changes
      // while that walk is pending join it and commit the newest request.
      const shouldForceCompleteRead = forceCompleteRead || (
        request.mode === 'client' && !wasClientAuthorityReady &&
        completeWorkloadRead.peek() !== null && !completeWorkloadRead.pending()
      )
      // The full source and its supporting edge join form one query-
      // independent transition. Query/filter edits leave this request alive;
      // the stable client guard below commits the latest reactive state once
      // both reads settle.
      const [nextWorkloads, nextEdges] = await Promise.all([
        completeWorkloadRead.read(shouldForceCompleteRead),
        edgeRead.read(),
      ])
      if (!workloadClientReadIsCurrent(requestID, request, readGeneration)) {
        return
      }
      workloads.value = nextWorkloads
      edges.value = nextEdges
      tableMode.value = 'client'
      tablePage.value = 1
      tableCursor.value = null
      tablePageInfo.value = null
      clientAuthorityReady.value = true
    } else {
      const [nextPage, nextEdges] = await Promise.all([
        listWorkloadsPage({
          limit: request.pageSize,
          ...(request.cursor ? { continue: request.cursor } : {}),
        }),
        edgeRead.read(),
      ])
      if (!workloadRequestIsCurrent(requestID, request)) return
      workloads.value = nextPage.items
      edges.value = nextEdges
      tableCursor.value = request.cursor
      tablePageInfo.value = makeTablePageInfo(nextPage.continue)
    }
    loaded.value = true
    error.value = null
  } catch (e) {
    const current = request.mode === 'client' && request.active
      ? workloadClientReadIsCurrent(requestID, request, readGeneration)
      : workloadRequestIsCurrent(requestID, request)
    if (!current) return
    error.value = (e as ErrorResponse)?.message ?? 'Failed to load workloads'
  } finally {
    if (clientReadToken === readToken) clientReadPending.value = false
    if (!stopped && workloadRefresh.isCurrent(requestID)) loading.value = false
    poller.schedule()
  }
})

function refresh(mode: ResourceRefreshMode | Event = 'foreground', forceClientRead = false) {
  if (forceClientRead) forceNextCompleteRead = true
  const requestedMode = typeof mode === 'string' ? mode : 'foreground'
  if (requestedMode === 'foreground') {
    refreshMode.value = 'foreground'
    loading.value = true
  }
  return workloadRefresh.request(requestedMode)
}

function handleWorkloadTableChange(change: ResourceTableChange) {
  const wasClientMode = tableMode.value === 'client'
  const canReuseCurrentServerPage = !wasClientMode && isCompleteFirstCursorPage({
    page: tablePage.value,
    cursor: tableCursor.value,
    pageInfo: tablePageInfo.value,
  })
  filterValues.value = {
    strategy: change.filters.strategy ?? '',
    status: change.filters.status ?? '',
  }
  tablePage.value = change.page
  tablePageSize.value = change.pageSize
  tableQuery.value = change.query
  tableCursor.value = change.cursor
  tablePageInfo.value = null

  const active = hasActiveTableFilters(tableQuery.value, filterValues.value)
  if (!active) {
    tableMode.value = 'server'
    clientAuthorityReady.value = false
    // Returning from local filtering always starts at the first bounded page;
    // ordinary unfiltered page navigation preserves its incoming cursor.
    if (wasClientMode || change.reason === 'query' || change.reason === 'filter') {
      tablePage.value = 1
      tableCursor.value = null
    }
    completeWorkloadRead.clear()
    workloads.value = []
    void refresh()
    return
  }

  // A terminal first server page is already a complete authority. Promote it
  // synchronously so entering a query never causes an unnecessary full walk.
  if (canReuseCurrentServerPage) {
    completeWorkloadRead.seed(workloads.value)
    tableMode.value = 'client'
    clientAuthorityReady.value = true
    tablePage.value = 1
    tableCursor.value = null
    tablePageInfo.value = null
    return
  }

  // Once a complete source is resident, table changes are local. During the
  // pending transition, query/filter-only changes are absorbed by the stable
  // client guard; other dimensions invalidate and restart the read.
  if (tableMode.value === 'client') {
    if (!clientAuthorityReady.value) {
      if (clientReadPending.value && (change.reason === 'query' || change.reason === 'filter')) return
      void refresh()
    }
    return
  }

  // Entering a query/filter changes the authority from one server page to a
  // complete source. Do not expose the partial page as a local result.
  tableMode.value = 'client'
  clientAuthorityReady.value = false
  tablePage.value = 1
  tableCursor.value = null
  tablePageInfo.value = null
  workloads.value = []
  void refresh()
}

async function onDelete(w: Workload) {
  if (!(await confirmDialog({ title: `Delete workload "${w.name}"?`, message: 'Its Deployments on every edge are removed.', danger: true, confirmLabel: 'Delete' }))) return
  try {
    await deleteWorkload(w.name)
    await refresh('foreground', true)
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Delete failed'
  }
}

onMounted(() => { void refresh('foreground') })
// Workload create/deploy is route-owned while this collection remains cached.
// Refresh on return to reconcile the newly submitted workload immediately and
// retain the table's current query/filter/page state during that read.
onActivated(() => {
  if (!stopped) void refresh('foreground', true)
})
onUnmounted(() => {
  stopped = true
  workloadRefresh.stop()
  poller.stop()
})

function selectorText(s?: Record<string, string>): string {
  if (!s || !Object.keys(s).length) return 'all edges'
  return Object.entries(s).map(([k, v]) => `${k}=${v}`).join(', ')
}
function workloadEdges(row: Record<string, unknown>): NonNullable<Workload['edges']> {
  return Array.isArray(row.edges) ? row.edges as NonNullable<Workload['edges']> : []
}
function workloadTone(status: unknown): 'success' | 'danger' | null {
  const phase = String(status).toLowerCase()
  if (phase === 'running') return 'success'
  if (phase === 'failed') return 'danger'
  return null
}

function workloadRowAriaLabel(row: Record<string, unknown>): string {
  return `Toggle workload ${String(row.name)}`
}
</script>

<template>
  <div class="edges-app">
    <header class="edges-header">
      <div>
        <h1>Workloads</h1>
        <p>Deploy a workload across matching Kubernetes edges. Each edge's agent runs it locally.</p>
      </div>
      <div v-if="!showFirstRun" class="header-actions">
        <button class="k-btn k-btn--ghost" :disabled="foregroundLoading" @click="refresh">
          <RefreshCw :size="14" :class="{ spin: foregroundLoading }" /> {{ foregroundLoading ? 'Refreshing…' : 'Refresh' }}
        </button>
        <button
          type="button"
          class="k-btn k-btn--primary"
          @click="emit('create')"
        >
          <Plus :size="14" aria-hidden="true" /> New workload
        </button>
      </div>
    </header>

    <div v-if="props.result" class="banner success" role="status" aria-live="polite">
      {{ props.result }}
      <button type="button" class="k-btn k-btn--ghost compact-control" @click="emit('dismissResult')">Dismiss</button>
    </div>

    <FirstRunGuide
      v-if="showFirstRun"
      :title="hasKubernetesEdges ? 'Deploy your first workload' : 'Connect a Kubernetes edge first'"
      :description="hasKubernetesEdges
        ? 'Deploy a container manually or start from a pinned marketplace chart.'
        : 'Workloads are scheduled only onto KubernetesCluster edges.'"
      :primary-label="hasKubernetesEdges ? 'Create workload' : 'Connect edge'"
      :secondary-label="hasKubernetesEdges ? 'Browse marketplace' : ''"
      :steps="workloadJourney"
      :current-step="hasKubernetesEdges ? 1 : 0"
      journey-label="Workload deployment path"
      @primary="handleFirstRunPrimary"
      @secondary="firstRunDismissed = true"
    >
      <template #icon><component :is="hasKubernetesEdges ? Boxes : Server" aria-hidden="true" /></template>
    </FirstRunGuide>

    <!-- Marketplace -->
    <div v-else class="market k-card">
      <button
        type="button"
        class="market-head"
        :aria-expanded="showMarket"
        aria-controls="edges-marketplace-body"
        @click="showMarket = !showMarket"
      >
        <component :is="showMarket ? ChevronDown : ChevronRight" :size="16" aria-hidden="true" />
        <Store :size="16" aria-hidden="true" />
        <span class="market-head-title">Marketplace</span>
        <span class="muted">one-click self-hosted apps, deployed as Helm workloads onto an edge</span>
      </button>
      <div v-if="showMarket" id="edges-marketplace-body" class="market-body">
        <div v-if="!hasKubernetesEdges" class="muted pad">
          Connect a KubernetesCluster edge first — marketplace apps deploy onto one.
        </div>
        <div v-for="grp in MARKETPLACE_CATEGORIES" :key="grp.category" class="market-cat">
          <div class="market-cat-label">{{ grp.category }}</div>
          <div class="market-grid">
            <div v-for="app in grp.apps" :key="app.type" class="market-card k-card">
              <div class="market-card-top">
                <span class="market-name">{{ app.label }}</span>
                <span class="k-badge k-badge--muted">{{ app.category }}</span>
              </div>
              <p class="market-desc">{{ app.description }}</p>
              <div class="market-meta muted mono">{{ app.chart.chart }}@{{ app.chart.version }} · :{{ app.port }}</div>
              <button class="k-btn k-btn--primary compact-control" :disabled="!hasKubernetesEdges" @click="emit('deploy', app)">
                <Rocket :size="13" /> Deploy
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <ResourceTable
      v-if="!showFirstRun"
      :columns="workloadColumns"
      :rows="workloadRows"
      aria-label="Workloads"
      row-key="name"
      :row-aria-label="workloadRowAriaLabel"
      :loaded="loaded"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="loaded && !!error"
      retryable
      searchable
      search-placeholder="Search workloads…"
      :filters="workloadFilters"
      :pagination-mode="tableMode"
      :page="tablePage"
      :page-size="tablePageSize"
      :query="tableQuery"
      :filter-values="filterValues"
      :cursor="tableCursor"
      :page-info="tablePageInfo"
      paginated
      empty-text="No workloads yet. Create one to deploy it across matching edges."
      @retry="refresh"
      @change="handleWorkloadTableChange"
      @row-click="(row) => toggle(String(row.name))"
    >
      <template #expand="{ row }">
        <button
          type="button"
          class="k-table-action"
          :aria-label="`Toggle workload ${String(row.name)}`"
          :aria-expanded="expanded === row.name"
          @click.stop="toggle(String(row.name))"
        >
          <component :is="expanded === row.name ? ChevronDown : ChevronRight" :size="14" />
        </button>
      </template>
      <template #name="{ value }"><span class="name">{{ value }}</span></template>
      <template #image="{ value }"><span class="mono muted">{{ value }}</span></template>
      <template #placement="{ value }"><span class="muted">{{ value }}</span></template>
      <template #status="{ value }"><StatusBadge :status="String(value)" :tone="workloadTone(value)" /></template>
      <template #ready="{ value }"><span class="mono">{{ value }}</span></template>
      <template #actions="{ row }"><div class="row-actions"><ResourceTableDeleteButton :label="`Delete workload ${String(row.name)}`" @click="onDelete(row as unknown as Workload)" /></div></template>
      <template #after-row="{ row, columnCount }">
        <tr v-if="expanded === row.name" class="detail-row">
          <td :colspan="columnCount">
            <div class="es-head">Per-edge status</div>
            <div v-if="workloadEdges(row).length === 0" class="muted">Not scheduled onto any edge yet (no edge matches the selector, or agents haven't reported).</div>
            <div v-else class="es-list">
              <div v-for="edge in workloadEdges(row)" :key="edge.edgeName" class="es-item">
                <span class="es-name">{{ edge.edgeName }}</span>
                <StatusBadge :status="edge.phase || 'Pending'" :tone="workloadTone(edge.phase || 'Pending')" />
                <span class="es-ready mono">{{ edge.readyReplicas ?? 0 }} ready</span>
                <span v-if="edge.message" class="muted es-msg">{{ edge.message }}</span>
              </div>
            </div>
          </td>
        </tr>
      </template>
    </ResourceTable>
  </div>
</template>
