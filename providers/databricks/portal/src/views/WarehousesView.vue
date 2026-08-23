<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref } from 'vue'
import SplitCreateButton from '../components/SplitCreateButton.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { api } from '../api'
import { confirmDialog } from '../portalkit/confirm'
import { isCompleteFirstCursorPage, type ResourceTableChange } from '../portalkit/table'
import type { Connection, ErrorResponse, Warehouse } from '../types'
import { createCoalescedRead, createLatestRefreshController, createOperationLocks, operationKey, type LatestRefreshController } from '../refresh'
import { resourceNameError } from '../resourceName'
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

const emit = defineEmits<{ (e: 'open', name: string): void; (e: 'browse', trigger?: HTMLElement): void }>()

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

const showForm = ref(false)
const submitting = ref(false)
const formError = ref<string | null>(null)
const nameInput = ref<HTMLInputElement | null>(null)
const formErrorRef = ref<HTMLElement | null>(null)
let timer: number | undefined
let refresh!: LatestRefreshController
let mounted = false
let fullWalkPending = false
let supportReadPending = false
let serverPageReadPending = false
let forceNextLoad = false
let authorityGeneration = 0

function invalidateCompleteAuthority(): void {
  authorityGeneration += 1
  completeRead.invalidate()
  supportRead.invalidate()
  serverPageRead.invalidate()
  forceNextLoad = true
}

const rows = computed<Array<Record<string, unknown>>>(() => warehouses.value
  .filter(wh => !operations.isTombstoned(operationKey('warehouse', wh.name), wh.uid))
  .map(wh => ({ ...wh })))

const form = reactive({
  name: '',
  connectionRef: '',
  warehouseID: '',
})

const filterDefinitions = computed(() => warehouseFilters(connections.value))

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function resetForm() {
  form.name = ''
  form.connectionRef = connections.value[0]?.name ?? ''
  form.warehouseID = ''
  formError.value = null
}

function load(): void
function load(force: boolean): void
function load(event: Event): void
function load(forceOrEvent: boolean | Event = false): void {
  const force = typeof forceOrEvent === 'boolean' ? forceOrEvent : forceNextLoad
  forceNextLoad = false
  if (!force && (fullWalkPending || supportReadPending || serverPageReadPending)) return
  refresh.request()
}

function operationLocked(name: string): boolean {
  return operations.isLocked(operationKey('warehouse', name))
}

function operationPhase(name: string) {
  return operations.phase(operationKey('warehouse', name))
}

function openResource(name: string): void {
  if (!operationLocked(name)) emit('open', name)
}

function startCreate() {
  resetForm()
  showForm.value = true
  void nextTick(() => nameInput.value?.focus())
}

function closeForm() {
  resetForm()
  showForm.value = false
}

function browseCatalog(trigger?: HTMLElement) {
  if (showForm.value) closeForm()
  emit('browse', trigger)
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
    await focusFormError('Warehouse list is still loading. Retry the read before creating a warehouse.')
    return
  }
  if (!form.name || !form.connectionRef || !form.warehouseID) {
    await focusFormError('Name, connection, and warehouse ID are required.')
    return
  }
  const nameError = resourceNameError(form.name, 'Name')
  if (nameError) {
    await focusFormError(nameError)
    return
  }
  const desiredName = form.name.trim()
  const lock = operationKey('warehouse', desiredName)
  if (!operations.acquire(lock, 'creating')) {
    await focusFormError(`Warehouse "${desiredName}" already has an update in progress.`)
    return
  }
  submitting.value = true
  try {
    // A server page is not a complete duplicate or foreign-key check. Read
    // both authoritative collections before applying the new warehouse.
    const [existing, availableConnections] = await Promise.all([
      completeRead.request(),
      api.listConnections(),
    ])
    operations.reconcile('warehouse', existing.map(({ name, uid }) => ({ name, uid })))
    if (operations.isTombstoned(lock)) {
      await focusFormError(`Warehouse "${desiredName}" is still being removed. Retry after the list refresh confirms it is gone.`)
      return
    }
    if (existing.some(warehouse => warehouse.name === desiredName)) {
      await focusFormError(`Warehouse "${desiredName}" already exists.`)
      return
    }
    if (!availableConnections.some(connection => connection.name === form.connectionRef)) {
      await focusFormError('Selected connection is no longer available in this workspace.')
      return
    }
    await api.saveWarehouse({
      name: desiredName,
      connectionRef: form.connectionRef,
      warehouseID: form.warehouseID,
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

refresh = createLatestRefreshController(async requestID => {
  const request = currentWarehouseRequest()
  let walkGeneration: number | undefined
  let supportGeneration: number | undefined
  let serverPageGeneration: number | undefined
  loading.value = true
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
    if (connections.value.length && !connections.value.some(c => c.name === form.connectionRef)) {
      form.connectionRef = connections.value[0].name
    }
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
      <SplitCreateButton kind="warehouse" :disabled="submitting" @manual="startCreate" @browse="browseCatalog" />
    </header>

    <p v-if="loaded && !connections.length" class="empty">Add a connection first, then import warehouses under it.</p>

    <div v-if="showForm" class="databricks-resource-panel k-card">
      <h3 class="databricks-resource-panel-title">New warehouse</h3>
      <form class="form" @submit.prevent="submit">
        <div class="field">
          <label class="field-label" for="warehouse-connection">Connection</label>
          <select id="warehouse-connection" class="k-input" v-model="form.connectionRef" :disabled="submitting" required aria-required="true" aria-describedby="warehouse-connection-hint warehouse-form-error" :aria-invalid="!!formError">
            <option v-for="conn in connections" :key="conn.name" :value="conn.name">{{ conn.name }}</option>
          </select>
          <span id="warehouse-connection-hint" class="field-hint">The Databricks workspace connection this warehouse belongs to.</span>
        </div>
        <div class="field">
          <label class="field-label" for="warehouse-name">Object name</label>
          <input id="warehouse-name" class="k-input" ref="nameInput" v-model="form.name" :disabled="submitting" placeholder="orders-sql" autocomplete="off" required aria-required="true" aria-describedby="warehouse-name-hint warehouse-form-error" :aria-invalid="!!formError" />
          <span id="warehouse-name-hint" class="field-hint">How this warehouse is referred to from faros. Use lowercase letters, numbers, and hyphens; the name is preserved exactly.</span>
        </div>
        <div class="field">
          <label class="field-label" for="warehouse-id">Warehouse ID</label>
          <input id="warehouse-id" class="k-input" v-model="form.warehouseID" :disabled="submitting" placeholder="abc123def4567890" autocomplete="off" required aria-required="true" aria-describedby="warehouse-id-hint warehouse-form-error" :aria-invalid="!!formError" />
          <span id="warehouse-id-hint" class="field-hint">In Databricks: SQL → SQL Warehouses → open the warehouse. Use the 16-character ID from Connection details (/sql/1.0/warehouses/&lt;id&gt;), not the numeric ?o= workspace ID. The token identity needs “Can use” permission.</span>
        </div>
        <div class="actions">
          <button class="k-btn k-btn--primary" type="submit" :disabled="submitting">{{ submitting ? 'Creating…' : 'Create' }}</button>
          <button class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="closeForm">Cancel</button>
          <span v-if="formError" id="warehouse-form-error" ref="formErrorRef" class="error" role="alert" aria-live="assertive" tabindex="-1">{{ formError }}</span>
        </div>
      </form>
    </div>

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
        <button class="k-btn k-btn--ghost databricks-inline-action" type="button" :disabled="operationLocked(String(value))" @click.stop="openResource(String(value))">{{ value }}</button>
      </template>
      <template #connectionRef="{ value }">{{ value }}</template>
      <template #warehouseID="{ value }"><code>{{ value }}</code></template>
      <template #state="{ row }">{{ row.state || '—' }}</template>
      <template #status="{ row }">
        <StatusBadge :status="String(row.status)" />
        <span v-if="row.message" class="row-message">{{ row.message }}</span>
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
