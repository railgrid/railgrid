<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { api } from '../api'
import { confirmDialog } from '../portalkit/confirm'
import { isCompleteFirstCursorPage, type ResourceTableChange } from '../portalkit/table'
import type { Connection, ErrorResponse } from '../types'
import { createCoalescedRead, createLatestRefreshController, createOperationLocks, operationKey, type LatestRefreshController } from '../refresh'
import { resourceNameError } from '../resourceName'
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

const emit = defineEmits<{ (e: 'open', name: string): void }>()

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

// connection form
const showForm = ref(false)
const name = ref('')
const host = ref('')
const token = ref('')
const submitting = ref(false)
const formError = ref<string | null>(null)
const nameInput = ref<HTMLInputElement | null>(null)
const formErrorRef = ref<HTMLElement | null>(null)
let timer: number | undefined
let refresh!: LatestRefreshController
let mounted = false
let fullWalkPending = false
let serverPageReadPending = false
let forceNextLoad = false
let authorityGeneration = 0

function invalidateCompleteAuthority(): void {
  authorityGeneration += 1
  completeRead.invalidate()
  serverPageRead.invalidate()
  forceNextLoad = true
}

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function resetForm() {
  name.value = ''
  host.value = ''
  token.value = ''
  formError.value = null
}

function startCreate() {
  resetForm()
  showForm.value = true
  void nextTick(() => nameInput.value?.focus())
}

function load(): void
function load(force: boolean): void
function load(event: Event): void
function load(forceOrEvent: boolean | Event = false): void {
  const force = typeof forceOrEvent === 'boolean' ? forceOrEvent : forceNextLoad
  forceNextLoad = false
  if (!force && (fullWalkPending || serverPageReadPending)) return
  refresh.request()
}

function operationLocked(name: string): boolean {
  return operations.isLocked(operationKey('connection', name))
}

function operationPhase(name: string) {
  return operations.phase(operationKey('connection', name))
}

function openResource(name: string): void {
  if (!operationLocked(name)) emit('open', name)
}

async function focusFormError(message: string) {
  formError.value = message
  await nextTick()
  formErrorRef.value?.focus()
}

async function submit() {
  formError.value = null
  mutationError.value = null
  if (!loaded.value) {
    await focusFormError('Connection list is still loading. Retry the read before creating a connection.')
    return
  }
  if (!name.value || !host.value || !token.value) {
    await focusFormError('Name, workspace host, and token are required.')
    return
  }
  const nameError = resourceNameError(name.value, 'Name')
  if (nameError) {
    await focusFormError(nameError)
    return
  }
  const desiredName = name.value.trim()
  const lock = operationKey('connection', desiredName)
  if (!operations.acquire(lock, 'creating')) {
    await focusFormError(`Connection "${desiredName}" already has an update in progress.`)
    return
  }
  submitting.value = true
  try {
    // The visible rows may be one cursor page. Duplicate protection must use a
    // complete authoritative read before allowing the mutation.
    const existing = await completeRead.request()
    operations.reconcile('connection', existing.map(({ name, uid }) => ({ name, uid })))
    if (operations.isTombstoned(lock)) {
      await focusFormError(`Connection "${desiredName}" is still being removed. Retry after the list refresh confirms it is gone.`)
      return
    }
    if (existing.some(connection => connection.name === desiredName)) {
      await focusFormError(`Connection "${desiredName}" already exists.`)
      return
    }
    await api.saveConnection({
      name: desiredName,
      host: host.value,
      token: token.value,
    })
    invalidateCompleteAuthority()
    resetForm()
    showForm.value = false
    load()
  } catch (e) {
    await focusFormError(errMessage(e))
  } finally {
    submitting.value = false
    operations.release(lock)
  }
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

refresh = createLatestRefreshController(async requestID => {
  const request = currentConnectionRequest()
  let walkGeneration: number | undefined
  let serverPageGeneration: number | undefined
  loading.value = true
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
    if (refresh.isCurrent(requestID)) loading.value = false
  }
})

onMounted(() => {
  mounted = true
  load()
  timer = window.setInterval(load, 5000)
})
onUnmounted(() => {
  mounted = false
  invalidateCompleteAuthority()
  window.clearInterval(timer)
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
        <button class="k-btn k-btn--primary" type="button" :disabled="submitting" @click="showForm ? (showForm = false) : startCreate()">
          {{ showForm ? 'Cancel' : 'Add connection' }}
        </button>
      </div>
    </header>

    <div v-if="showForm" class="databricks-resource-panel k-card">
      <h3 class="databricks-resource-panel-title">Connect with a token</h3>
      <form class="form" @submit.prevent="submit">
        <div class="field">
          <label class="field-label" for="connection-name">Name</label>
          <input id="connection-name" class="k-input" ref="nameInput" v-model="name" :disabled="submitting" autocomplete="off" placeholder="orders-prod" required aria-required="true" aria-describedby="connection-name-hint connection-form-error" :aria-invalid="!!formError" />
          <span id="connection-name-hint" class="field-hint">How this workspace is referred to from faros. Use lowercase letters, numbers, and hyphens; the name is preserved exactly.</span>
        </div>
        <div class="field">
          <label class="field-label" for="connection-host">Workspace host</label>
          <input id="connection-host" class="k-input" v-model="host" :disabled="submitting" autocomplete="url" placeholder="https://dbc-example.cloud.databricks.com" required aria-required="true" aria-describedby="connection-host-hint connection-form-error" :aria-invalid="!!formError" />
          <span id="connection-host-hint" class="field-hint">Use the HTTPS root URL from the Databricks browser address bar (AWS, Azure, or GCP), with no path.</span>
        </div>
        <div class="field">
          <label class="field-label" for="connection-token">Token</label>
          <input id="connection-token" class="k-input" v-model="token" :disabled="submitting" type="password" autocomplete="new-password" placeholder="Paste token" required aria-required="true" aria-describedby="connection-token-hint connection-form-error" :aria-invalid="!!formError" />
          <span id="connection-token-hint" class="field-hint">Create a personal access token in Databricks: avatar → Settings → Developer → Access tokens → Manage → Generate new token. Its identity needs SELECT on the catalogs and schemas you plan to import, plus access to a running SQL warehouse.</span>
        </div>
        <div class="actions">
      <button class="k-btn k-btn--primary" type="submit" :disabled="submitting">{{ submitting ? 'Connecting...' : 'Create' }}</button>
          <button class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="() => { resetForm(); showForm = false }">Cancel</button>
          <span v-if="formError" id="connection-form-error" ref="formErrorRef" class="error" role="alert" aria-live="assertive" tabindex="-1">{{ formError }}</span>
        </div>
        <p class="muted">The token is stored as a Secret in your workspace; the provider validates it and shows the status below.</p>
      </form>
    </div>

    <div v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
      <span>{{ mutationError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="mutationError = null">Dismiss</button>
    </div>

    <ResourceTable
      :columns="[
        { key: 'name', label: 'Name' },
        { key: 'host', label: 'Workspace host' },
        { key: 'authType', label: 'Auth' },
        { key: 'status', label: 'Status' },
        { key: 'actions', label: '' },
      ]"
      :rows="rows"
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
      :error="error"
      :stale="loaded && !!error"
      retryable
      empty-text="No connections yet."
      :row-aria-label="(row) => `Open connection ${String(row.name)}`"
      @retry="load"
      @change="handleConnectionChange"
      @row-click="(row) => openResource(String(row.name))"
    >
      <template #name="{ value }"><button class="k-btn k-btn--ghost databricks-inline-action" type="button" :disabled="operationLocked(String(value))" @click.stop="openResource(String(value))">{{ value }}</button></template>
      <template #host="{ value }"><code>{{ value }}</code></template>
      <template #authType="{ value }">{{ value }}</template>
      <template #status="{ row }">
        <StatusBadge :status="String(row.status)" />
        <span v-if="row.message" class="row-message">{{ row.message }}</span>
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
