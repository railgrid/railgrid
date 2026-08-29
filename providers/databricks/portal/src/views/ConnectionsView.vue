<script setup lang="ts">
import { ExternalLink } from 'lucide-vue-next'
import { computed, onActivated, onMounted, onUnmounted, ref } from 'vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { api } from '../api'
import { confirmDialog } from '../portalkit/confirm'
import { isCompleteFirstCursorPage, type ResourceTableChange } from '../portalkit/table'
import type { Connection, ErrorResponse } from '../types'
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
  cloneConnectionFilters,
  CONNECTION_FILTERS,
  databricksHybridTransition,
  databricksServerPageTransition,
  DATABRICKS_PAGE_SIZE,
  EMPTY_CONNECTION_FILTERS,
  hasActiveFilters,
  pageInfo as toPageInfo,
  serverCursorChange,
  type ConnectionFilterValues,
  type DatabricksPaginationMode,
} from '../databricksPagination'

const emit = defineEmits<{ (e: 'open', name: string): void; (e: 'create'): void }>()

const connections = ref<Connection[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const operations = createOperationLocks()
const completeRead = createCoalescedRead(() => api.listConnections())
const serverPageRead = createCoalescedRead(() => {
  const request = currentConnectionRequest()
  return api.listConnectionsPage({
    limit: request.pageSize,
    ...(request.cursor ? { continue: request.cursor } : {}),
  })
})
const connectionMode = ref<DatabricksPaginationMode>('server')
const connectionPage = ref(1)
const connectionPageSize = ref(DATABRICKS_PAGE_SIZE)
const connectionQuery = ref('')
const connectionFilters = ref<ConnectionFilterValues>(cloneConnectionFilters(EMPTY_CONNECTION_FILTERS))
const connectionCursor = ref<string | null>(null)
const connectionPageInfo = ref<ReturnType<typeof toPageInfo> | null>(null)
const rows = computed<Array<Record<string, unknown>>>(() => connections.value
  .filter(conn => !operations.isTombstoned(operationKey('connection', conn.name), conn.uid))
  .map(conn => ({ ...conn })))

let poll!: AdaptiveRefreshTimer
let refresh!: LatestRefreshController
let mounted = false
let fullWalkPending = false
let serverPageReadPending = false
let activatedOnce = false
let authorityGeneration = 0

function invalidateCompleteAuthority(): void {
  authorityGeneration += 1
  completeRead.invalidate()
  serverPageRead.invalidate()
}

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
  const stable = loaded.value && !error.value && connections.value.every(connection =>
    connection.status === 'Ready' && operationPhase(connection.name) !== 'deleting')
  return stable ? STABLE_REFRESH_MS : FAST_REFRESH_MS
}

poll = createAdaptiveRefreshTimer(() => requestRefresh('background'), pollCadence)

function operationLocked(name: string): boolean {
  return operations.isLocked(operationKey('connection', name))
}

function operationPhase(name: string) {
  return operations.phase(operationKey('connection', name))
}

function openResource(name: string): void {
  if (!operationLocked(name)) emit('open', name)
}

interface ConnectionRequest {
  mode: DatabricksPaginationMode
  active: boolean
  page: number
  pageSize: number
  query: string
  filters: ConnectionFilterValues
  cursor: string | null
}

function currentConnectionRequest(): ConnectionRequest {
  return {
    mode: connectionMode.value,
    active: hasActiveFilters(connectionQuery.value, connectionFilters.value),
    page: connectionPage.value,
    pageSize: connectionPageSize.value,
    query: connectionQuery.value,
    filters: cloneConnectionFilters(connectionFilters.value),
    cursor: connectionCursor.value,
  }
}

function connectionRequestIsCurrent(requestID: number, request: ConnectionRequest): boolean {
  const current = currentConnectionRequest()
  return refresh.isCurrent(requestID) &&
    current.mode === request.mode &&
    current.active === request.active &&
    current.page === request.page &&
    current.pageSize === request.pageSize &&
    current.query === request.query &&
    current.cursor === request.cursor &&
    current.filters.authType === request.filters.authType &&
    current.filters.status === request.filters.status
}

function handleConnectionChange(change: ResourceTableChange): void {
  const canReuseCurrentServerPage = connectionMode.value === 'server' && isCompleteFirstCursorPage({
    page: connectionPage.value,
    cursor: connectionCursor.value,
    pageInfo: connectionPageInfo.value,
  })
  const filters: ConnectionFilterValues = {
    authType: change.filters.authType || '',
    status: change.filters.status || '',
  }
  const active = hasActiveFilters(change.query, filters)
  const serverChange = serverCursorChange(change)
  connectionPage.value = change.page
  connectionPageSize.value = change.pageSize
  connectionQuery.value = change.query
  connectionFilters.value = filters
  connectionCursor.value = change.cursor
  connectionPageInfo.value = null

  if (!active) {
    invalidateCompleteAuthority()
    fullWalkPending = false
    connectionMode.value = 'server'
    connections.value = []
    connectionPage.value = serverChange.page
    connectionCursor.value = serverChange.cursor
    load()
    return
  }

  const transition = databricksHybridTransition({
    mode: connectionMode.value,
    active,
    completeFirstPage: canReuseCurrentServerPage,
    fullWalkPending: fullWalkPending || serverPageReadPending,
  })
  // Once a complete walk is loaded, ResourceTable handles query/filter/page
  // changes locally. Only the server -> client transition needs another read.
  if (transition.mode === 'client' && transition.reuseRows) {
    connectionMode.value = 'client'
    connectionPage.value = 1
    connectionCursor.value = null
    connectionPageInfo.value = null
    operations.reconcile('connection', connections.value.map(({ name, uid }) => ({ name, uid })))
    return
  }
  if (!transition.reload) {
    if (transition.mode === 'server' && connectionMode.value === 'server') connections.value = []
    return
  }
  if (transition.clearRows || connectionMode.value === 'server') connections.value = []
  fullWalkPending = true
  load(true)
}

async function remove(row: Record<string, unknown>) {
  const conn = row as unknown as Connection
  const ok = await confirmDialog({
    title: `Delete connection "${conn.name}"?`,
    message: 'Warehouses and tables that reference this connection will stop working.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  const lock = operationKey('connection', conn.name)
  if (!operations.acquire(lock, 'deleting')) {
    mutationError.value = `Connection "${conn.name}" already has an operation in progress.`
    return
  }
  mutationError.value = null
  try {
    await api.deleteConnection(conn)
    invalidateCompleteAuthority()
    operations.tombstone(lock, conn.uid)
    connections.value = connections.value.filter(item => item.name !== conn.name)
    load()
  } catch (e) {
    mutationError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

refresh = createLatestRefreshController(async (requestID, mode) => {
  const request = currentConnectionRequest()
  let walkGeneration: number | undefined
  let serverPageGeneration: number | undefined
  refreshMode.value = mode
  if (mode === 'foreground') loading.value = true
  // Never render the unfiltered server page as the result of a newly entered
  // query. Same-mode polling keeps its cached rows visible.
  if (request.active && request.mode === 'server') {
    connections.value = []
    connectionPageInfo.value = null
  }
  try {
    if (request.active || request.mode === 'client') {
      fullWalkPending = true
      walkGeneration = authorityGeneration
      const next = await completeRead.request()
      if (!mounted) return
      if (walkGeneration !== authorityGeneration) return
      const current = currentConnectionRequest()
      if (!current.active && current.mode === 'server') return
      connections.value = next
      if (current.mode === 'server') {
        connectionMode.value = 'client'
        connectionPage.value = 1
      }
      connectionCursor.value = null
      connectionPageInfo.value = null
      operations.reconcile('connection', next.map(({ name, uid }) => ({ name, uid })))
      loaded.value = true
      error.value = null
      fullWalkPending = false
    } else {
      if (!connectionRequestIsCurrent(requestID, request)) return
      serverPageReadPending = true
      serverPageGeneration = authorityGeneration
      const next = await serverPageRead.request()
      serverPageReadPending = false
      if (!mounted || serverPageGeneration !== authorityGeneration) return
      const currentAfterPage = currentConnectionRequest()
      const currentIsActive = currentAfterPage.active || currentAfterPage.mode === 'client'
      const nextPageInfo = toPageInfo(next.continue)
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
        const connectionList = await completeRead.request()
        if (!mounted) return
        if (walkGeneration !== authorityGeneration) return
        const current = currentConnectionRequest()
        if (!current.active && current.mode === 'server') return
        connections.value = connectionList
        if (current.mode === 'server') {
          connectionMode.value = 'client'
          connectionPage.value = 1
        }
        connectionCursor.value = null
        connectionPageInfo.value = null
        operations.reconcile('connection', connectionList.map(({ name, uid }) => ({ name, uid })))
      } else if (currentIsActive) {
        connections.value = next.items
        connectionCursor.value = request.cursor
        connectionPageInfo.value = nextPageInfo
        if (pageTransition.promoteToClient) {
          connectionMode.value = 'client'
          connectionPage.value = 1
          connectionCursor.value = null
          connectionPageInfo.value = null
          operations.reconcile('connection', next.items.map(({ name, uid }) => ({ name, uid })))
        }
      } else {
        if (!connectionRequestIsCurrent(requestID, request)) return
        connections.value = next.items
        connectionCursor.value = request.cursor
        connectionPageInfo.value = nextPageInfo
        if (isCompleteFirstCursorPage({
          page: request.page,
          cursor: request.cursor,
          pageInfo: connectionPageInfo.value,
        })) {
          operations.reconcile('connection', next.items.map(({ name, uid }) => ({ name, uid })))
        }
      }
    }
    loaded.value = true
    error.value = null
  } catch (e) {
    fullWalkPending = false
    serverPageReadPending = false
    const current = currentConnectionRequest()
    const staleWalk = walkGeneration !== undefined && walkGeneration !== authorityGeneration
    const staleServerPage = serverPageGeneration !== undefined && serverPageGeneration !== authorityGeneration
    const staleServerRequest = !(current.active || current.mode === 'client') && !connectionRequestIsCurrent(requestID, request)
    if (!mounted || staleWalk || staleServerPage || staleServerRequest) return
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    fullWalkPending = false
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
  serverPageRead.stop()
})
</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Connections</h2>
        <p class="page-meta">Databricks workspaces available to tables in this faros workspace.</p>
      </div>
      <div class="actions">
        <button class="k-btn k-btn--primary" type="button" @click="emit('create')">Add connection</button>
      </div>
    </header>

    <div v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
      <span>{{ mutationError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="mutationError = null">Dismiss</button>
    </div>

    <ResourceTable
      :columns="[
        { key: 'name', label: 'Name', primary: true },
        { key: 'host', label: 'Workspace host' },
        { key: 'authType', label: 'Auth' },
        { key: 'status', label: 'Status' },
        { key: 'actions', label: '', ariaLabel: 'Actions' },
      ]"
      :rows="rows"
      aria-label="Databricks connections"
      searchable
      search-placeholder="Search connections…"
      :filters="CONNECTION_FILTERS"
      paginated
      :pagination-mode="connectionMode"
      :page="connectionPage"
      :page-size="connectionPageSize"
      :query="connectionQuery"
      :filter-values="connectionFilters"
      :cursor="connectionCursor"
      :page-info="connectionPageInfo"
      row-key="name"
      :loaded="loaded"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="loaded && !!error"
      retryable
      empty-text="No connections yet."
      :row-aria-label="(row) => `Open connection ${String(row.name)}`"
      @retry="load"
      @change="handleConnectionChange"
      @row-click="(row) => openResource(String(row.name))"
    >
      <template #name="{ value }"><button class="k-btn k-btn--ghost k-table-resource-link" type="button" :disabled="operationLocked(String(value))" @click.stop="openResource(String(value))">{{ value }}</button></template>
      <template #host="{ value }">
        <a
          v-if="value"
          :href="String(value)"
          target="_blank"
          rel="noopener"
          :title="String(value)"
          :aria-label="`Open Databricks workspace ${String(value)} in a new tab`"
          @click.stop
        >open <ExternalLink :size="12" aria-hidden="true" /></a>
        <span v-else class="muted">—</span>
      </template>
      <template #authType="{ value }"><span class="k-badge k-badge--muted">{{ value }}</span></template>
      <template #status="{ row }">
        <StatusBadge :status="String(row.status)" :title="String(row.message || '')" :aria-label="row.message ? `${String(row.status)}: ${String(row.message)}` : String(row.status)" />
      </template>
      <template #actions="{ row }">
        <div class="row-actions">
          <ResourceTableDeleteButton
            :label="`Delete connection ${String(row.name)}`"
            :busy-label="`Deleting connection ${String(row.name)}…`"
            :busy="operationPhase(String(row.name)) === 'deleting'"
            :disabled="operationLocked(String(row.name))"
            @click="remove(row)"
          />
        </div>
      </template>
    </ResourceTable>
  </section>
</template>
