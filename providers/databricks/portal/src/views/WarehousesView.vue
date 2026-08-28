<script setup lang="ts">
import { computed, onActivated, onMounted, onUnmounted, ref } from 'vue'
import SplitCreateButton from '../components/SplitCreateButton.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { api } from '../api'
import { confirmDialog } from '../portalkit/confirm'
import { isCompleteFirstCursorPage, type ResourceTableChange } from '../portalkit/table'
import type { Connection, ErrorResponse, Warehouse } from '../types'
import {
  createAdaptiveRefreshTimer,
  createCoalescedRead,
  createLatestRefreshController,
  createOperationLocks,
  FAST_REFRESH_MS,
  operationKey,
  STABLE_REFRESH_MS,
  type AdaptiveRefreshTimer,
  type LatestRefreshController,
  type ResourceRefreshMode,
} from '../refresh'
import {
  cloneWarehouseFilters,
  databricksHybridTransition,
  databricksServerPageTransition,
  DATABRICKS_PAGE_SIZE,
  EMPTY_WAREHOUSE_FILTERS,
  hasActiveFilters,
  pageInfo as toPageInfo,
  serverCursorChange,
  type DatabricksPaginationMode,
  type WarehouseFilterValues,
  warehouseFilters,
} from '../databricksPagination'

const emit = defineEmits<{ (e: 'open', name: string): void; (e: 'create', mode: 'manual' | 'browse'): void }>()

const connections = ref<Connection[]>([])
const warehouses = ref<Warehouse[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const operations = createOperationLocks()
const completeRead = createCoalescedRead(() => api.listWarehouses())
const supportRead = createCoalescedRead(() => api.listConnections())
const serverPageRead = createCoalescedRead(() => {
  const request = currentWarehouseRequest()
  return api.listWarehousesPage({
    limit: request.pageSize,
    ...(request.cursor ? { continue: request.cursor } : {}),
  })
})
const warehouseMode = ref<DatabricksPaginationMode>('server')
const warehousePage = ref(1)
const warehousePageSize = ref(DATABRICKS_PAGE_SIZE)
const warehouseQuery = ref('')
const warehouseFiltersValue = ref<WarehouseFilterValues>(cloneWarehouseFilters(EMPTY_WAREHOUSE_FILTERS))
const warehouseCursor = ref<string | null>(null)
const warehousePageInfo = ref<ReturnType<typeof toPageInfo> | null>(null)

let poll!: AdaptiveRefreshTimer
let refresh!: LatestRefreshController
let mounted = false
let fullWalkPending = false
let supportReadPending = false
let serverPageReadPending = false
let activatedOnce = false
let authorityGeneration = 0

function invalidateCompleteAuthority(): void {
  authorityGeneration += 1
  completeRead.invalidate()
  supportRead.invalidate()
  serverPageRead.invalidate()
}

const rows = computed<Array<Record<string, unknown>>>(() => warehouses.value
  .filter(wh => !operations.isTombstoned(operationKey('warehouse', wh.name), wh.uid))
  .map(wh => ({ ...wh })))

function warehouseStateTone(state: unknown): 'success' | 'warning' | 'danger' | 'muted' {
  switch (String(state || '').toUpperCase()) {
    case 'RUNNING': return 'success'
    case 'PENDING':
    case 'STARTING':
    case 'STOPPING':
    case 'DELETING': return 'warning'
    case 'FAILED': return 'danger'
    default: return 'muted'
  }
}

const filterDefinitions = computed(() => warehouseFilters(connections.value))

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

const refreshMode = ref<ResourceRefreshMode>('foreground')

function requestRefresh(mode: ResourceRefreshMode): void {
  if (mode === 'foreground') {
    refreshMode.value = 'foreground'
    loading.value = true
  }
  refresh.request(mode)
}

function load(): void
function load(force: boolean): void
function load(mode: ResourceRefreshMode): void
function load(forceOrMode: boolean | ResourceRefreshMode = false): void {
  const mode = typeof forceOrMode === 'string' ? forceOrMode : 'foreground'
  requestRefresh(mode)
}

function pollCadence(): number {
  const stable = loaded.value && !error.value && warehouses.value.every(warehouse =>
    warehouse.status === 'Ready' && operationPhase(warehouse.name) !== 'deleting')
  return stable ? STABLE_REFRESH_MS : FAST_REFRESH_MS
}

poll = createAdaptiveRefreshTimer(() => requestRefresh('background'), pollCadence)

function operationLocked(name: string): boolean {
  return operations.isLocked(operationKey('warehouse', name))
}

function operationPhase(name: string) {
  return operations.phase(operationKey('warehouse', name))
}

function openResource(name: string): void {
  if (!operationLocked(name)) emit('open', name)
}

interface WarehouseRequest {
  mode: DatabricksPaginationMode
  active: boolean
  page: number
  pageSize: number
  query: string
  filters: WarehouseFilterValues
  cursor: string | null
}

function currentWarehouseRequest(): WarehouseRequest {
  return {
    mode: warehouseMode.value,
    active: hasActiveFilters(warehouseQuery.value, warehouseFiltersValue.value),
    page: warehousePage.value,
    pageSize: warehousePageSize.value,
    query: warehouseQuery.value,
    filters: cloneWarehouseFilters(warehouseFiltersValue.value),
    cursor: warehouseCursor.value,
  }
}

function warehouseRequestIsCurrent(requestID: number, request: WarehouseRequest): boolean {
  const current = currentWarehouseRequest()
  return refresh.isCurrent(requestID) &&
    current.mode === request.mode &&
    current.active === request.active &&
    current.page === request.page &&
    current.pageSize === request.pageSize &&
    current.query === request.query &&
    current.cursor === request.cursor &&
    current.filters.connectionRef === request.filters.connectionRef &&
    current.filters.state === request.filters.state &&
    current.filters.status === request.filters.status
}

function handleWarehouseChange(change: ResourceTableChange): void {
  const canReuseCurrentServerPage = warehouseMode.value === 'server' && isCompleteFirstCursorPage({
    page: warehousePage.value,
    cursor: warehouseCursor.value,
    pageInfo: warehousePageInfo.value,
  })
  const filters: WarehouseFilterValues = {
    connectionRef: change.filters.connectionRef || '',
    state: change.filters.state || '',
    status: change.filters.status || '',
  }
  const active = hasActiveFilters(change.query, filters)
  const serverChange = serverCursorChange(change)
  warehousePage.value = change.page
  warehousePageSize.value = change.pageSize
  warehouseQuery.value = change.query
  warehouseFiltersValue.value = filters
  warehouseCursor.value = change.cursor
  warehousePageInfo.value = null

  if (!active) {
    invalidateCompleteAuthority()
    fullWalkPending = false
    warehouseMode.value = 'server'
    warehouses.value = []
    warehousePage.value = serverChange.page
    warehouseCursor.value = serverChange.cursor
    load()
    return
  }

  const transition = databricksHybridTransition({
    mode: warehouseMode.value,
    active,
    completeFirstPage: canReuseCurrentServerPage,
    fullWalkPending: fullWalkPending || supportReadPending || serverPageReadPending,
  })
  if (transition.mode === 'client' && transition.reuseRows) {
    warehouseMode.value = 'client'
    warehousePage.value = 1
    warehouseCursor.value = null
    warehousePageInfo.value = null
    operations.reconcile('warehouse', warehouses.value.map(({ name, uid }) => ({ name, uid })))
    return
  }
  if (!transition.reload) {
    if (transition.mode === 'server' && warehouseMode.value === 'server') warehouses.value = []
    return
  }
  if (transition.clearRows || warehouseMode.value === 'server') warehouses.value = []
  fullWalkPending = true
  load(true)
}

async function remove(row: Record<string, unknown>) {
  const wh = row as unknown as Warehouse
  const ok = await confirmDialog({
    title: `Delete warehouse "${wh.name}"?`,
    message: 'Tables that reference this warehouse will stop refreshing schema metadata.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  const lock = operationKey('warehouse', wh.name)
  if (!operations.acquire(lock, 'deleting')) {
    mutationError.value = `Warehouse "${wh.name}" already has an operation in progress.`
    return
  }
  mutationError.value = null
  try {
    await api.deleteWarehouse(wh.name)
    invalidateCompleteAuthority()
    operations.tombstone(lock, wh.uid)
    warehouses.value = warehouses.value.filter(item => item.name !== wh.name)
    load()
  } catch (e) {
    mutationError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

refresh = createLatestRefreshController(async (requestID, mode) => {
  const request = currentWarehouseRequest()
  let walkGeneration: number | undefined
  let supportGeneration: number | undefined
  let serverPageGeneration: number | undefined
  refreshMode.value = mode
  if (mode === 'foreground') loading.value = true
  if (request.active && request.mode === 'server') {
    warehouses.value = []
    warehousePageInfo.value = null
  }
  try {
    supportReadPending = true
    supportGeneration = authorityGeneration
    const availableConnections = await supportRead.request()
    supportReadPending = false
    if (!mounted || supportGeneration !== authorityGeneration) return
    connections.value = availableConnections

    const currentAfterSupport = currentWarehouseRequest()
    if (currentAfterSupport.active || currentAfterSupport.mode === 'client') {
      fullWalkPending = true
      walkGeneration = authorityGeneration
      const warehouseList = await completeRead.request()
      if (!mounted) return
      if (walkGeneration !== authorityGeneration) return
      const current = currentWarehouseRequest()
      if (!current.active && current.mode === 'server') return
      warehouses.value = warehouseList
      if (current.mode === 'server') {
        warehouseMode.value = 'client'
        warehousePage.value = 1
      }
      warehouseCursor.value = null
      warehousePageInfo.value = null
      operations.reconcile('warehouse', warehouseList.map(({ name, uid }) => ({ name, uid })))
      loaded.value = true
      error.value = null
      fullWalkPending = false
    } else {
      if (!warehouseRequestIsCurrent(requestID, request)) return
      serverPageReadPending = true
      serverPageGeneration = authorityGeneration
      const warehousePageResult = await serverPageRead.request()
      serverPageReadPending = false
      if (!mounted || serverPageGeneration !== authorityGeneration) return
      const currentAfterPage = currentWarehouseRequest()
      const currentIsActive = currentAfterPage.active || currentAfterPage.mode === 'client'
      const nextPageInfo = toPageInfo(warehousePageResult.continue)
      const pageTransition = databricksServerPageTransition({
        active: currentIsActive,
        page: request.page,
        cursor: request.cursor,
        pageInfo: nextPageInfo,
      })
      if (currentIsActive && pageTransition.startFullWalk) {
        // A page captured before query entry is not visible authority unless
        // it is an explicit complete first page. Do not flash its rows.
        fullWalkPending = true
        walkGeneration = authorityGeneration
        const warehouseList = await completeRead.request()
        if (!mounted) return
        if (walkGeneration !== authorityGeneration) return
        const current = currentWarehouseRequest()
        if (!current.active && current.mode === 'server') return
        warehouses.value = warehouseList
        if (current.mode === 'server') {
          warehouseMode.value = 'client'
          warehousePage.value = 1
        }
        warehouseCursor.value = null
        warehousePageInfo.value = null
        operations.reconcile('warehouse', warehouseList.map(({ name, uid }) => ({ name, uid })))
      } else if (currentIsActive) {
        warehouses.value = warehousePageResult.items
        warehouseCursor.value = request.cursor
        warehousePageInfo.value = nextPageInfo
        if (pageTransition.promoteToClient) {
          warehouseMode.value = 'client'
          warehousePage.value = 1
          warehouseCursor.value = null
          warehousePageInfo.value = null
          operations.reconcile('warehouse', warehousePageResult.items.map(({ name, uid }) => ({ name, uid })))
        }
      } else {
        if (!warehouseRequestIsCurrent(requestID, request)) return
        warehouses.value = warehousePageResult.items
        warehouseCursor.value = request.cursor
        warehousePageInfo.value = nextPageInfo
        if (isCompleteFirstCursorPage({
          page: request.page,
          cursor: request.cursor,
          pageInfo: warehousePageInfo.value,
        })) {
          operations.reconcile('warehouse', warehousePageResult.items.map(({ name, uid }) => ({ name, uid })))
        }
      }
    }
    loaded.value = true
    error.value = null
  } catch (e) {
    fullWalkPending = false
    supportReadPending = false
    serverPageReadPending = false
    const current = currentWarehouseRequest()
    const staleWalk = walkGeneration !== undefined && walkGeneration !== authorityGeneration
    const staleSupport = supportGeneration !== undefined && supportGeneration !== authorityGeneration
    const staleServerPage = serverPageGeneration !== undefined && serverPageGeneration !== authorityGeneration
    const staleServerRequest = !(current.active || current.mode === 'client') && !warehouseRequestIsCurrent(requestID, request)
    if (!mounted || staleWalk || staleSupport || staleServerPage || staleServerRequest) return
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    fullWalkPending = false
    supportReadPending = false
    serverPageReadPending = false
    if (refresh.isCurrent(requestID)) {
      if (mode === 'foreground') loading.value = false
      poll.schedule()
    }
  }
})

onMounted(() => {
  mounted = true
  load()
})
onActivated(() => {
  if (!activatedOnce) {
    activatedOnce = true
    return
  }
  load('foreground')
})
onUnmounted(() => {
  mounted = false
  invalidateCompleteAuthority()
  poll.stop()
  refresh.stop()
  completeRead.stop()
  supportRead.stop()
  serverPageRead.stop()
})

</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Warehouses</h2>
        <p class="page-meta">SQL warehouses available to imported Databricks tables. Click one to inspect status and defaults.</p>
      </div>
      <SplitCreateButton kind="warehouse" @manual="emit('create', 'manual')" @browse="emit('create', 'browse')" />
    </header>

    <p v-if="loaded && !connections.length" class="empty">Add a connection first, then import warehouses under it.</p>

    <div v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
      <span>{{ mutationError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="mutationError = null">Dismiss</button>
    </div>

    <ResourceTable
      :columns="[
        { key: 'name', label: 'Name' },
        { key: 'connectionRef', label: 'Connection' },
        { key: 'warehouseID', label: 'Warehouse ID' },
        { key: 'state', label: 'State' },
        { key: 'status', label: 'Status' },
        { key: 'actions', label: '' },
      ]"
      :rows="rows"
      searchable
      search-placeholder="Search warehouses…"
      :filters="filterDefinitions"
      paginated
      :pagination-mode="warehouseMode"
      :page="warehousePage"
      :page-size="warehousePageSize"
      :query="warehouseQuery"
      :filter-values="warehouseFiltersValue"
      :cursor="warehouseCursor"
      :page-info="warehousePageInfo"
      row-key="name"
      :loaded="loaded"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="loaded && !!error"
      retryable
      empty-text="No warehouses yet."
      :row-aria-label="(row) => `Open warehouse ${String(row.name)}`"
      @retry="load"
      @change="handleWarehouseChange"
      @row-click="(row) => openResource(String(row.name))"
    >
      <template #name="{ value }">
        <button class="k-btn k-btn--ghost k-table-resource-link" type="button" :disabled="operationLocked(String(value))" @click.stop="openResource(String(value))">{{ value }}</button>
      </template>
      <template #connectionRef="{ value }">{{ value }}</template>
      <template #warehouseID="{ value }">{{ value }}</template>
      <template #state="{ row }"><StatusBadge v-if="row.state" :status="String(row.state)" :tone="warehouseStateTone(row.state)" /><span v-else class="muted">—</span></template>
      <template #status="{ row }">
        <StatusBadge :status="String(row.status)" :title="String(row.message || '')" :aria-label="row.message ? `${String(row.status)}: ${String(row.message)}` : String(row.status)" />
      </template>
      <template #actions="{ row }">
        <div class="row-actions">
          <ResourceTableDeleteButton
            :label="`Delete warehouse ${String(row.name)}`"
            :busy-label="`Deleting warehouse ${String(row.name)}…`"
            :busy="operationPhase(String(row.name)) === 'deleting'"
            :disabled="operationLocked(String(row.name))"
            @click="remove(row)"
          />
        </div>
      </template>
    </ResourceTable>

  </section>
</template>
