<script setup lang="ts">
import { computed, nextTick, onActivated, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import SplitCreateButton from '../components/SplitCreateButton.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import ResourceTableEditButton from '../portalkit/ResourceTableEditButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { api } from '../api'
import { confirmDialog } from '../portalkit/confirm'
import { isCompleteFirstCursorPage, type ResourceTableChange } from '../portalkit/table'
import { importPrerequisiteMessage, nextValidWarehouseRef, warehousesForConnection } from '../tableRefs'
import type { Connection, ErrorResponse, Table, Warehouse } from '../types'
import {
  createCoalescedRead,
  createLatestRefreshController,
  createOperationLocks,
  operationKey,
  type LatestRefreshController,
  type ResourceRefreshMode,
} from '../refresh'
import { resourceNameError } from '../resourceName'
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

const emit = defineEmits<{ (e: 'open', name: string): void; (e: 'create', mode: 'manual' | 'browse'): void }>()

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
const showForm = ref(false)
const editing = ref<string | null>(null)
const submitting = ref(false)
const formError = ref<string | null>(null)
const nameInput = ref<HTMLInputElement | null>(null)
const formErrorRef = ref<HTMLElement | null>(null)
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
  completeRead.invalidate()
  supportRead.invalidate()
  serverPageRead.invalidate()
  forceNextLoad = true
}

const form = reactive({
  name: '',
  connectionRef: '',
  warehouseRef: '',
  catalog: '',
  schema: '',
  table: '',
})

const visibleTables = computed(() => tables.value.filter(table => !operations.isTombstoned(operationKey('table', table.name), table.uid)))
const rows = computed<Array<Record<string, unknown>>>(() =>
  visibleTables.value.map(t => ({
    ...t,
    columnCount: t.columns.length ? String(t.columns.length) : '-',
  })),
)

const tableImportBlocker = computed(() => !loaded.value ? '' : importPrerequisiteMessage(connections.value, warehouses.value))
const formWarehouses = computed(() => warehousesForConnection(warehouses.value, form.connectionRef))
const filterDefinitions = computed(() => tableFilters(warehouses.value))

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function resetForm() {
  editing.value = null
  form.name = ''
  form.connectionRef = connections.value[0]?.name ?? ''
  form.warehouseRef = warehouses.value[0]?.name ?? ''
  form.catalog = ''
  form.schema = ''
  form.table = ''
  formError.value = null
}

function closeForm() {
  resetForm()
  showForm.value = false
}

function editTable(row: Record<string, unknown>) {
  const table = row as unknown as Table
  if (operationLocked(table.name)) return
  editing.value = table.name
  form.name = table.name
  form.connectionRef = table.connectionRef
  form.warehouseRef = table.warehouseRef
  form.catalog = table.catalog
  form.schema = table.schema
  form.table = table.table
  formError.value = null
  showForm.value = true
}

function load(): void
function load(force: boolean): void
function load(event: Event): void
function load(forceOrEvent: boolean | Event = false): void {
  const force = typeof forceOrEvent === 'boolean' ? forceOrEvent : forceNextLoad
  forceNextLoad = false
  if (!force && (fullWalkPending || supportReadPending || serverPageReadPending)) return
  refreshMode.value = 'foreground'
  loading.value = true
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

async function focusFormError(message: string) {
  formError.value = message
  await nextTick()
  formErrorRef.value?.focus()
}

async function submit() {
  formError.value = null
  mutationError.value = null
  if (!loaded.value) {
    await focusFormError('Table list is still loading. Retry the read before saving a table.')
    return
  }
  if (tableImportBlocker.value) {
    await focusFormError(tableImportBlocker.value)
    return
  }
  if (!form.name || !form.connectionRef || !form.warehouseRef || !form.catalog || !form.schema || !form.table) {
    await focusFormError('All table fields are required.')
    return
  }
  const nameError = resourceNameError(form.name, 'Name')
  if (nameError) {
    await focusFormError(nameError)
    return
  }
  const desiredName = form.name.trim()
  const lock = operationKey('table', desiredName)
  if (!operations.acquire(lock, editing.value ? 'saving' : 'creating')) {
    await focusFormError(`Table "${desiredName}" already has an update in progress.`)
    return
  }
  submitting.value = true
  try {
    // The visible table/supporting rows may be partial cursor pages. Resolve
    // every duplicate and foreign-key check against complete authoritative
    // collections before saving.
    const [existingTables, availableConnections, availableWarehouses] = await Promise.all([
      completeRead.request(),
      api.listConnections(),
      api.listWarehouses(),
    ])
    operations.reconcile('table', existingTables.map(({ name, uid }) => ({ name, uid })))
    if (operations.isTombstoned(lock)) {
      await focusFormError(`Table "${desiredName}" is still being removed. Retry after the list refresh confirms it is gone.`)
      return
    }
    const duplicate = existingTables.find(table => table.name === desiredName)
    if (duplicate && duplicate.name !== editing.value) {
      await focusFormError(`Table "${desiredName}" already exists.`)
      return
    }
    if (!availableConnections.some(connection => connection.name === form.connectionRef)) {
      await focusFormError('Selected connection is no longer available in this workspace.')
      return
    }
    if (!availableWarehouses.some(warehouse => warehouse.name === form.warehouseRef && warehouse.connectionRef === form.connectionRef)) {
      await focusFormError('Selected warehouse must belong to the selected connection.')
      return
    }
    await api.saveTable({
      name: desiredName,
      connectionRef: form.connectionRef,
      warehouseRef: form.warehouseRef,
      catalog: form.catalog,
      schema: form.schema,
      table: form.table,
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
    operations.tombstone(lock, table.uid)
    tables.value = tables.value.filter(item => item.name !== table.name)
    load()
  } catch (e) {
    mutationError.value = errMessage(e)
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
        if (isCompleteFirstCursorPage({
          page: request.page,
          cursor: request.cursor,
          pageInfo: tablePageInfo.value,
        })) {
          operations.reconcile('table', tablePageResult.items.map(({ name, uid }) => ({ name, uid })))
        }
      }
    }
    loaded.value = true
    error.value = null
    if (!form.connectionRef) form.connectionRef = connections.value[0]?.name ?? ''
    if (connections.value.length && !connections.value.some(c => c.name === form.connectionRef)) form.connectionRef = connections.value[0].name
    form.warehouseRef = nextValidWarehouseRef(warehouses.value, form.connectionRef, form.warehouseRef)
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
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    fullWalkPending = false
    supportReadPending = false
    serverPageReadPending = false
    if (refresh.isCurrent(requestID) && mode === 'foreground') loading.value = false
  }
})

watch(() => form.connectionRef, connectionRef => {
  form.warehouseRef = nextValidWarehouseRef(warehouses.value, connectionRef, form.warehouseRef)
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
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Tables</h2>
        <p class="page-meta">Imported table handles that App Studio can use by tableRef.</p>
      </div>
      <div class="actions">
        <button class="k-btn k-btn--ghost icon-text" type="button" :disabled="loading" :aria-busy="loading || undefined" @click="load">
          <RefreshCw class="button-icon" :class="{ spin: loading }" :stroke-width="1.75" />
          {{ loading ? 'Refreshing…' : 'Refresh' }}
        </button>
        <SplitCreateButton kind="table" :disabled="submitting" @manual="emit('create', 'manual')" @browse="emit('create', 'browse')" />
      </div>
    </header>

    <p v-if="tableImportBlocker" class="empty">{{ tableImportBlocker }}</p>

    <div v-if="showForm" class="databricks-resource-panel k-card">
      <div class="databricks-resource-panel-head">
        <h3 class="databricks-resource-panel-title">Update table</h3>
      </div>
      <div v-if="tableImportBlocker" class="warning" role="status">
        {{ tableImportBlocker }}
      </div>
      <form class="form-grid" @submit.prevent="submit">
        <label class="field" for="table-name">
          <span class="field-label">Name</span>
          <input id="table-name" class="k-input" ref="nameInput" v-model="form.name" :disabled="!!editing || submitting" autocomplete="off" placeholder="order-history" required aria-required="true" aria-describedby="table-name-hint table-form-error" :aria-invalid="!!formError" />
          <span id="table-name-hint" class="field-hint">The stable tableRef exposed to App Studio. Use lowercase letters, numbers, and hyphens; the name is preserved exactly.</span>
        </label>
        <label class="field" for="table-connection">
          <span class="field-label">Connection</span>
          <select id="table-connection" class="k-input" v-model="form.connectionRef" :disabled="submitting" required aria-required="true" aria-describedby="table-connection-hint table-form-error" :aria-invalid="!!formError">
            <option value="" disabled>Select connection</option>
            <option v-for="conn in connections" :key="conn.name" :value="conn.name">{{ conn.name }}</option>
          </select>
          <span id="table-connection-hint" class="field-hint">The Databricks workspace connection for this table.</span>
        </label>
        <label class="field" for="table-warehouse">
          <span class="field-label">Warehouse</span>
          <select id="table-warehouse" class="k-input" v-model="form.warehouseRef" :disabled="submitting" required aria-required="true" aria-describedby="table-warehouse-hint table-form-error" :aria-invalid="!!formError">
            <option value="" disabled>{{ formWarehouses.length ? 'Select warehouse' : 'No warehouses for this connection' }}</option>
            <option v-for="wh in formWarehouses" :key="wh.name" :value="wh.name">{{ wh.name }}</option>
          </select>
          <span id="table-warehouse-hint" class="field-hint">A warehouse that belongs to the selected connection.</span>
        </label>
        <label class="field" for="table-catalog">
          <span class="field-label">Catalog</span>
          <input id="table-catalog" class="k-input" v-model="form.catalog" :disabled="submitting" autocomplete="off" placeholder="sales" required aria-required="true" aria-describedby="table-catalog-hint table-form-error" :aria-invalid="!!formError" />
          <span id="table-catalog-hint" class="field-hint">The Databricks catalog containing the table.</span>
        </label>
        <label class="field" for="table-schema">
          <span class="field-label">Schema</span>
          <input id="table-schema" class="k-input" v-model="form.schema" :disabled="submitting" autocomplete="off" placeholder="gold" required aria-required="true" aria-describedby="table-schema-hint table-form-error" :aria-invalid="!!formError" />
          <span id="table-schema-hint" class="field-hint">The Databricks schema containing the table.</span>
        </label>
        <label class="field" for="table-table">
          <span class="field-label">Table</span>
          <input id="table-table" class="k-input" v-model="form.table" :disabled="submitting" autocomplete="off" placeholder="order_history" required aria-required="true" aria-describedby="table-table-hint table-form-error" :aria-invalid="!!formError" />
          <span id="table-table-hint" class="field-hint">The exact table identifier in the selected catalog and schema.</span>
        </label>
        <div class="form-actions span-2">
          <button class="k-btn k-btn--primary" type="submit" :disabled="submitting">{{ submitting ? 'Saving...' : 'Save' }}</button>
          <button class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="closeForm">Cancel</button>
          <span v-if="formError" id="table-form-error" ref="formErrorRef" class="error" role="alert" aria-live="assertive" tabindex="-1">{{ formError }}</span>
        </div>
      </form>
    </div>

    <div v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
      <span>{{ mutationError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="mutationError = null">Dismiss</button>
    </div>

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
            @click="editTable(row)"
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

  </section>
</template>
