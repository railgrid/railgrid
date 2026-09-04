<script setup lang="ts">
import { computed, onActivated, onMounted, onUnmounted, ref } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import DatabricksEmptyState from '../components/DatabricksEmptyState.vue'
import SplitCreateButton from '../components/SplitCreateButton.vue'
import type { DatabricksJourneyAction, DatabricksPrerequisiteKind } from '../journey'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import ResourceTableEditButton from '../portalkit/ResourceTableEditButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { api } from '../api'
import { formatDatabricksError, isTenantMissingError } from '../errors'
import { confirmDialog } from '../portalkit/confirm'
import { isCompleteFirstCursorPage, type ResourceTableChange } from '../portalkit/table'
import type { Connection, Table, Warehouse } from '../types'
import {
  createCoalescedRead,
  createLatestRefreshController,
  createOperationLocks,
  operationKey,
  type LatestRefreshController,
  type ResourceRefreshMode,
} from '../refresh'
import {
  cloneTableFilters,
  databricksHybridTransition,
  databricksServerPageTransition,
  DATABRICKS_PAGE_SIZE,
  EMPTY_TABLE_FILTERS,
  hasActiveFilters,
  pageInfo as toPageInfo,
  serverCursorChange,
  tableFilters,
  type DatabricksPaginationMode,
  type TableFilterValues,
} from '../databricksPagination'

const emit = defineEmits<{
  (e: 'open', name: string): void
  (e: 'create', mode: 'manual' | 'browse'): void
  (e: 'edit', name: string): void
  (e: 'prerequisite', kind: DatabricksPrerequisiteKind): void
}>()

const connections = ref<Connection[]>([])
const warehouses = ref<Warehouse[]>([])
const tables = ref<Table[]>([])
const loading = ref(false)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const loaded = ref(false)
const error = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const operations = createOperationLocks()
const completeRead = createCoalescedRead(() => api.listTables())
const supportRead = createCoalescedRead(() => Promise.all([
  api.listConnections(),
  api.listWarehouses(),
]))
const serverPageRead = createCoalescedRead(() => {
  const request = currentTableRequest()
  return api.listTablesPage({
    limit: request.pageSize,
    ...(request.cursor ? { continue: request.cursor } : {}),
  })
})
const tableMode = ref<DatabricksPaginationMode>('server')
const tablePage = ref(1)
const tablePageSize = ref(DATABRICKS_PAGE_SIZE)
const tableQuery = ref('')
const tableFiltersValue = ref<TableFilterValues>(cloneTableFilters(EMPTY_TABLE_FILTERS))
const tableCursor = ref<string | null>(null)
const tablePageInfo = ref<ReturnType<typeof toPageInfo> | null>(null)
const firstPageSettled = ref(false)
const pendingDeletions = new Map<string, string | undefined>()
let refresh!: LatestRefreshController
let mounted = false
let fullWalkPending = false
let supportReadPending = false
let serverPageReadPending = false
let forceNextLoad = false
let activatedOnce = false
let authorityGeneration = 0

function invalidateCompleteAuthority(): void {
  authorityGeneration += 1
  firstPageSettled.value = false
  completeRead.invalidate()
  supportRead.invalidate()
  serverPageRead.invalidate()
  forceNextLoad = true
}

const visibleTables = computed(() => tables.value.filter(table => !operations.isTombstoned(operationKey('table', table.name), table.uid)))
const rows = computed<Array<Record<string, unknown>>>(() =>
  visibleTables.value.map(t => ({
    ...t,
    columnCount: t.columns.length ? String(t.columns.length) : '-',
  })),
)
const showFirstRun = computed(() => firstPageSettled.value
  && !error.value
  && rows.value.length === 0
  && tablePage.value === 1
  && !hasActiveFilters(tableQuery.value, tableFiltersValue.value))

function handleFirstRunAction(action: DatabricksJourneyAction): void {
  if (action === 'create-connection') emit('prerequisite', 'connection')
  else if (action === 'browse-warehouses' || action === 'manual-warehouse') emit('prerequisite', 'warehouse')
  else if (action === 'browse-tables') emit('create', 'browse')
  else if (action === 'manual-table') emit('create', 'manual')
}

const filterDefinitions = computed(() => tableFilters(warehouses.value))

function load(): void
function load(force: boolean): void
function load(event: Event): void
function load(forceOrEvent: boolean | Event = false): void {
  const force = typeof forceOrEvent === 'boolean' ? forceOrEvent : forceNextLoad
  forceNextLoad = false
  if (!force && (fullWalkPending || supportReadPending || serverPageReadPending)) return
  refreshMode.value = 'foreground'
  loading.value = true
  // Keep an authoritative first-run surface mounted while it revalidates.
  // An acknowledged delete is different: hide first-run until the complete
  // first page confirms that the deleted resource is gone.
  if (pendingDeletions.size > 0) firstPageSettled.value = false
  refresh.request('foreground')
}

function operationLocked(name: string): boolean {
  return operations.isLocked(operationKey('table', name))
}

function operationPhase(name: string) {
  return operations.phase(operationKey('table', name))
}

function openResource(name: string): void {
  if (!operationLocked(name)) emit('open', name)
}

interface TableRequest {
  mode: DatabricksPaginationMode
  active: boolean
  page: number
  pageSize: number
  query: string
  filters: TableFilterValues
  cursor: string | null
}

function currentTableRequest(): TableRequest {
  return {
    mode: tableMode.value,
    active: hasActiveFilters(tableQuery.value, tableFiltersValue.value),
    page: tablePage.value,
    pageSize: tablePageSize.value,
    query: tableQuery.value,
    filters: cloneTableFilters(tableFiltersValue.value),
    cursor: tableCursor.value,
  }
}

function tableRequestIsCurrent(requestID: number, request: TableRequest): boolean {
  const current = currentTableRequest()
  return refresh.isCurrent(requestID) &&
    current.mode === request.mode &&
    current.active === request.active &&
    current.page === request.page &&
    current.pageSize === request.pageSize &&
    current.query === request.query &&
    current.cursor === request.cursor &&
    current.filters.warehouseRef === request.filters.warehouseRef &&
    current.filters.status === request.filters.status
}

function handleTableChange(change: ResourceTableChange): void {
  firstPageSettled.value = false
  const canReuseCurrentServerPage = tableMode.value === 'server' && isCompleteFirstCursorPage({
    page: tablePage.value,
    cursor: tableCursor.value,
    pageInfo: tablePageInfo.value,
  })
  const filters: TableFilterValues = {
    warehouseRef: change.filters.warehouseRef || '',
    status: change.filters.status || '',
  }
  const active = hasActiveFilters(change.query, filters)
  const serverChange = serverCursorChange(change)
  tablePage.value = change.page
  tablePageSize.value = change.pageSize
  tableQuery.value = change.query
  tableFiltersValue.value = filters
  tableCursor.value = change.cursor
  tablePageInfo.value = null

  if (!active) {
    invalidateCompleteAuthority()
    fullWalkPending = false
    tableMode.value = 'server'
    tables.value = []
    tablePage.value = serverChange.page
    tableCursor.value = serverChange.cursor
    load()
    return
  }

  const transition = databricksHybridTransition({
    mode: tableMode.value,
    active,
    completeFirstPage: canReuseCurrentServerPage,
    fullWalkPending: fullWalkPending || supportReadPending || serverPageReadPending,
  })
  if (transition.mode === 'client' && transition.reuseRows) {
    tableMode.value = 'client'
    tablePage.value = 1
    tableCursor.value = null
    tablePageInfo.value = null
    operations.reconcile('table', tables.value.map(({ name, uid }) => ({ name, uid })))
    return
  }
  if (!transition.reload) {
    if (transition.mode === 'server' && tableMode.value === 'server') tables.value = []
    return
  }
  if (transition.clearRows || tableMode.value === 'server') tables.value = []
  fullWalkPending = true
  load(true)
}

async function remove(row: Record<string, unknown>) {
  const table = row as unknown as Table
  const ok = await confirmDialog({
    title: `Delete table "${table.name}"?`,
    message: 'App Studio guidance and Databricks MCP tools will no longer be able to inspect this tableRef.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  const lock = operationKey('table', table.name)
  if (!operations.acquire(lock, 'deleting')) {
    mutationError.value = `Table "${table.name}" already has an operation in progress.`
    return
  }
  mutationError.value = null
  try {
    await api.deleteTable(table.name)
    invalidateCompleteAuthority()
    firstPageSettled.value = false
    pendingDeletions.set(table.name, table.uid)
    operations.tombstone(lock, table.uid)
    tables.value = tables.value.filter(item => item.name !== table.name)
    load()
  } catch (e) {
    mutationError.value = formatDatabricksError(e)
  } finally {
    operations.release(lock)
  }
}

refresh = createLatestRefreshController(async (requestID, mode) => {
  const request = currentTableRequest()
  let walkGeneration: number | undefined
  let supportGeneration: number | undefined
  let serverPageGeneration: number | undefined
  refreshMode.value = mode
  if (mode === 'foreground') loading.value = true
  if (request.active && request.mode === 'server') {
    tables.value = []
    tablePageInfo.value = null
  }
  try {
    supportReadPending = true
    supportGeneration = authorityGeneration
    const [availableConnections, availableWarehouses] = await supportRead.request()
    supportReadPending = false
    if (!mounted || supportGeneration !== authorityGeneration) return
    connections.value = availableConnections
    warehouses.value = availableWarehouses

    const currentAfterSupport = currentTableRequest()
    if (currentAfterSupport.active || currentAfterSupport.mode === 'client') {
      fullWalkPending = true
      walkGeneration = authorityGeneration
      const tableList = await completeRead.request()
      if (!mounted) return
      if (walkGeneration !== authorityGeneration) return
      const current = currentTableRequest()
      if (!current.active && current.mode === 'server') return
      tables.value = tableList
      if (current.mode === 'server') {
        tableMode.value = 'client'
        tablePage.value = 1
      }
      tableCursor.value = null
      tablePageInfo.value = null
      operations.reconcile('table', tableList.map(({ name, uid }) => ({ name, uid })))
      loaded.value = true
      error.value = null
      fullWalkPending = false
    } else {
      if (!tableRequestIsCurrent(requestID, request)) return
      serverPageReadPending = true
      serverPageGeneration = authorityGeneration
      const tablePageResult = await serverPageRead.request()
      serverPageReadPending = false
      if (!mounted || serverPageGeneration !== authorityGeneration) return
      const currentAfterPage = currentTableRequest()
      const currentIsActive = currentAfterPage.active || currentAfterPage.mode === 'client'
      const nextPageInfo = toPageInfo(tablePageResult.continue)
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
        const tableList = await completeRead.request()
        if (!mounted) return
        if (walkGeneration !== authorityGeneration) return
        const current = currentTableRequest()
        if (!current.active && current.mode === 'server') return
        tables.value = tableList
        if (current.mode === 'server') {
          tableMode.value = 'client'
          tablePage.value = 1
        }
        tableCursor.value = null
        tablePageInfo.value = null
        operations.reconcile('table', tableList.map(({ name, uid }) => ({ name, uid })))
      } else if (currentIsActive) {
        tables.value = tablePageResult.items
        tableCursor.value = request.cursor
        tablePageInfo.value = nextPageInfo
        if (pageTransition.promoteToClient) {
          tableMode.value = 'client'
          tablePage.value = 1
          tableCursor.value = null
          tablePageInfo.value = null
          operations.reconcile('table', tablePageResult.items.map(({ name, uid }) => ({ name, uid })))
        }
      } else {
        if (!tableRequestIsCurrent(requestID, request)) return
        tables.value = tablePageResult.items
        tableCursor.value = request.cursor
        tablePageInfo.value = nextPageInfo
        const completeFirstPage = isCompleteFirstCursorPage({
          page: request.page,
          cursor: request.cursor,
          pageInfo: tablePageInfo.value,
        })
        if (completeFirstPage) {
          operations.reconcile('table', tablePageResult.items.map(({ name, uid }) => ({ name, uid })))
          for (const [name, pendingUID] of pendingDeletions) {
            const current = tablePageResult.items.find(table => table.name === name)
            const replacement = current?.uid !== undefined && (pendingUID === undefined || current.uid !== pendingUID)
            if (!current || replacement) pendingDeletions.delete(name)
          }
          firstPageSettled.value = pendingDeletions.size === 0
        } else if (request.page === 1 && !request.cursor) {
          firstPageSettled.value = false
        }
      }
    }
    loaded.value = true
    error.value = null
  } catch (e) {
    fullWalkPending = false
    supportReadPending = false
    serverPageReadPending = false
    const current = currentTableRequest()
    const staleWalk = walkGeneration !== undefined && walkGeneration !== authorityGeneration
    const staleSupport = supportGeneration !== undefined && supportGeneration !== authorityGeneration
    const staleServerPage = serverPageGeneration !== undefined && serverPageGeneration !== authorityGeneration
    const staleServerRequest = !(current.active || current.mode === 'client') && !tableRequestIsCurrent(requestID, request)
    if (!mounted || staleWalk || staleSupport || staleServerPage || staleServerRequest) return
    error.value = isTenantMissingError(e) ? null : formatDatabricksError(e)
  } finally {
    fullWalkPending = false
    supportReadPending = false
    serverPageReadPending = false
    if (refresh.isCurrent(requestID) && mode === 'foreground') loading.value = false
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
  load(true)
})
onUnmounted(() => {
  mounted = false
  invalidateCompleteAuthority()
  refresh.stop()
  completeRead.stop()
  supportRead.stop()
  serverPageRead.stop()
})
</script>

<template>
  <section :class="['page', { 'page--first-run': showFirstRun }]">
    <header class="page-head">
      <div>
        <h2 class="page-title">Tables</h2>
        <p class="page-meta">Imported table handles that App Studio can use by tableRef.</p>
      </div>
      <div class="actions">
        <button v-if="loaded && !showFirstRun" class="k-btn k-btn--ghost icon-text" type="button" :disabled="loading" :aria-busy="loading || undefined" @click="load">
          <RefreshCw class="button-icon" :class="{ spin: loading }" :stroke-width="1.75" />
          {{ loading ? 'Refreshing…' : 'Refresh' }}
        </button>
        <SplitCreateButton v-if="loaded && !showFirstRun" kind="table" @manual="emit('create', 'manual')" @browse="emit('create', 'browse')" />
      </div>
    </header>

    <div v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
      <span>{{ mutationError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="mutationError = null">Dismiss</button>
    </div>

    <DatabricksEmptyState
      v-if="showFirstRun"
      kind="table"
      :has-connections="connections.length > 0"
      :has-warehouses="warehouses.length > 0"
      @action="handleFirstRunAction"
    />

    <div v-else class="databricks-resource-table">
      <ResourceTable
      :columns="[
        { key: 'name', label: 'TableRef', primary: true },
        { key: 'fullName', label: 'Databricks table' },
        { key: 'warehouseRef', label: 'Warehouse' },
        { key: 'columnCount', label: 'Columns', align: 'end' },
        { key: 'status', label: 'Status' },
        { key: 'actions', label: '', ariaLabel: 'Actions' },
      ]"
      :rows="rows"
      aria-label="Databricks tables"
      searchable
      search-placeholder="Search tables…"
      :filters="filterDefinitions"
      paginated
      :pagination-mode="tableMode"
      :page="tablePage"
      :page-size="tablePageSize"
      :query="tableQuery"
      :filter-values="tableFiltersValue"
      :cursor="tableCursor"
      :page-info="tablePageInfo"
      row-key="name"
      :loaded="loaded"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="loaded && !!error"
      retryable
      :row-aria-label="(row) => `Open table ${String(row.name)}`"
      @retry="load"
      @change="handleTableChange"
      @row-click="(row) => openResource(String(row.name))"
    >
      <template #name="{ value }"><button class="k-btn k-btn--ghost k-table-resource-link" type="button" :disabled="operationLocked(String(value))" @click.stop="openResource(String(value))">{{ value }}</button></template>
      <template #fullName="{ value }">{{ value }}</template>
      <template #warehouseRef="{ value }">{{ value }}</template>
      <template #columnCount="{ value }"><span class="muted">{{ value }}</span></template>
      <template #status="{ row }">
        <StatusBadge :status="String(row.status)" :title="String(row.message || '')" :aria-label="row.message ? `${String(row.status)}: ${String(row.message)}` : String(row.status)" />
      </template>
      <template #actions="{ row }">
        <div class="row-actions">
          <ResourceTableEditButton
            :label="`Edit table ${String(row.name)}`"
            :disabled="operationLocked(String(row.name))"
            @click="emit('edit', String(row.name))"
          />
          <ResourceTableDeleteButton
            :label="`Delete table ${String(row.name)}`"
            :busy-label="`Deleting table ${String(row.name)}…`"
            :busy="operationPhase(String(row.name)) === 'deleting'"
            :disabled="operationLocked(String(row.name))"
            @click="remove(row)"
          />
        </div>
      </template>
      </ResourceTable>
    </div>

  </section>
</template>
