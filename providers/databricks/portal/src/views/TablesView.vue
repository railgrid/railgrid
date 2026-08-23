<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import SplitCreateButton from '../components/SplitCreateButton.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import ResourceTableEditButton from '../portalkit/ResourceTableEditButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { api } from '../api'
import { confirmDialog } from '../portalkit/confirm'
import { importPrerequisiteMessage, nextValidWarehouseRef, warehousesForConnection } from '../tableRefs'
import type { Connection, ErrorResponse, Table, Warehouse } from '../types'
import { createLatestRefreshController, createOperationLocks, operationKey, type LatestRefreshController } from '../refresh'
import { resourceNameError } from '../resourceName'

const emit = defineEmits<{ (e: 'open', name: string): void; (e: 'browse', trigger?: HTMLElement): void }>()

const connections = ref<Connection[]>([])
const warehouses = ref<Warehouse[]>([])
const tables = ref<Table[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const operations = createOperationLocks()
const showForm = ref(false)
const editing = ref<string | null>(null)
const submitting = ref(false)
const formError = ref<string | null>(null)
const nameInput = ref<HTMLInputElement | null>(null)
const formErrorRef = ref<HTMLElement | null>(null)
let refresh!: LatestRefreshController

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

// Prefill the Databricks-provided samples catalog (readable in every
// workspace) so a first import needs no lookup: samples.nyctaxi.trips.
function fillDemo() {
  if (!editing.value) form.name = 'nyctaxi-trips'
  form.catalog = 'samples'
  form.schema = 'nyctaxi'
  form.table = 'trips'
  formError.value = null
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

function load() {
  refresh.request()
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
  const duplicate = tables.value.find(table => table.name === desiredName)
  if (duplicate && duplicate.name !== editing.value) {
    await focusFormError(`Table "${desiredName}" already exists.`)
    return
  }
  const lock = operationKey('table', desiredName)
  if (operations.isTombstoned(lock)) {
    await focusFormError(`Table "${desiredName}" is still being removed. Retry after the list refresh confirms it is gone.`)
    return
  }
  if (!operations.acquire(lock, editing.value ? 'saving' : 'creating')) {
    await focusFormError(`Table "${desiredName}" already has an update in progress.`)
    return
  }
  if (!formWarehouses.value.some(warehouse => warehouse.name === form.warehouseRef)) {
    operations.release(lock)
    await focusFormError('Selected warehouse must belong to the selected connection.')
    return
  }
  submitting.value = true
  try {
    await api.saveTable({
      name: desiredName,
      connectionRef: form.connectionRef,
      warehouseRef: form.warehouseRef,
      catalog: form.catalog,
      schema: form.schema,
      table: form.table,
    })
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
    operations.tombstone(lock, table.uid)
    tables.value = tables.value.filter(item => item.name !== table.name)
    load()
  } catch (e) {
    mutationError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

refresh = createLatestRefreshController(async requestID => {
  loading.value = true
  try {
    const [connList, warehouseList, tableList] = await Promise.all([
      api.listConnections(),
      api.listWarehouses(),
      api.listTables(),
    ])
    if (!refresh.isCurrent(requestID)) return
    connections.value = connList
    warehouses.value = warehouseList
    tables.value = tableList
    operations.reconcile('connection', connList.map(({ name, uid }) => ({ name, uid })))
    operations.reconcile('warehouse', warehouseList.map(({ name, uid }) => ({ name, uid })))
    operations.reconcile('table', tableList.map(({ name, uid }) => ({ name, uid })))
    loaded.value = true
    error.value = null
    if (!form.connectionRef) form.connectionRef = connList[0]?.name ?? ''
    if (connList.length && !connList.some(c => c.name === form.connectionRef)) form.connectionRef = connList[0].name
    form.warehouseRef = nextValidWarehouseRef(warehouseList, form.connectionRef, form.warehouseRef)
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (refresh.isCurrent(requestID)) loading.value = false
  }
})

watch(() => form.connectionRef, connectionRef => {
  form.warehouseRef = nextValidWarehouseRef(warehouses.value, connectionRef, form.warehouseRef)
})
onMounted(() => {
  load()
})
onUnmounted(() => {
  refresh.stop()
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
        <button class="secondary icon-text" type="button" @click="load">
          <RefreshCw class="button-icon" :stroke-width="1.75" />
          Refresh
        </button>
        <SplitCreateButton kind="table" :disabled="submitting" @manual="startCreate" @browse="browseCatalog" />
      </div>
    </header>

    <p v-if="tableImportBlocker" class="empty">{{ tableImportBlocker }}</p>

    <div v-if="showForm" class="databricks-resource-panel k-card">
      <div class="databricks-resource-panel-head">
        <h3 class="databricks-resource-panel-title">{{ editing ? 'Update table' : 'Import table' }}</h3>
        <button v-if="!editing" class="link" type="button" :disabled="submitting" @click="fillDemo" title="Prefill samples.nyctaxi.trips — Databricks demo data available in every workspace">Fill with demo data</button>
      </div>
      <div v-if="tableImportBlocker" class="warning" role="status">
        {{ tableImportBlocker }}
      </div>
      <form class="form-grid" @submit.prevent="submit">
        <label class="field" for="table-name">
          <span class="field-label">Name</span>
          <input id="table-name" ref="nameInput" v-model="form.name" :disabled="!!editing || submitting" autocomplete="off" placeholder="order-history" required aria-required="true" aria-describedby="table-name-hint table-form-error" :aria-invalid="!!formError" />
          <span id="table-name-hint" class="field-hint">The stable tableRef exposed to App Studio. Use lowercase letters, numbers, and hyphens; the name is preserved exactly.</span>
        </label>
        <label class="field" for="table-connection">
          <span class="field-label">Connection</span>
          <select id="table-connection" v-model="form.connectionRef" :disabled="submitting" required aria-required="true" aria-describedby="table-connection-hint table-form-error" :aria-invalid="!!formError">
            <option value="" disabled>Select connection</option>
            <option v-for="conn in connections" :key="conn.name" :value="conn.name">{{ conn.name }}</option>
          </select>
          <span id="table-connection-hint" class="field-hint">The Databricks workspace connection for this table.</span>
        </label>
        <label class="field" for="table-warehouse">
          <span class="field-label">Warehouse</span>
          <select id="table-warehouse" v-model="form.warehouseRef" :disabled="submitting" required aria-required="true" aria-describedby="table-warehouse-hint table-form-error" :aria-invalid="!!formError">
            <option value="" disabled>{{ formWarehouses.length ? 'Select warehouse' : 'No warehouses for this connection' }}</option>
            <option v-for="wh in formWarehouses" :key="wh.name" :value="wh.name">{{ wh.name }}</option>
          </select>
          <span id="table-warehouse-hint" class="field-hint">A warehouse that belongs to the selected connection.</span>
        </label>
        <label class="field" for="table-catalog">
          <span class="field-label">Catalog</span>
          <input id="table-catalog" v-model="form.catalog" :disabled="submitting" autocomplete="off" placeholder="sales" required aria-required="true" aria-describedby="table-catalog-hint table-form-error" :aria-invalid="!!formError" />
          <span id="table-catalog-hint" class="field-hint">The Databricks catalog containing the table.</span>
        </label>
        <label class="field" for="table-schema">
          <span class="field-label">Schema</span>
          <input id="table-schema" v-model="form.schema" :disabled="submitting" autocomplete="off" placeholder="gold" required aria-required="true" aria-describedby="table-schema-hint table-form-error" :aria-invalid="!!formError" />
          <span id="table-schema-hint" class="field-hint">The Databricks schema containing the table.</span>
        </label>
        <label class="field" for="table-table">
          <span class="field-label">Table</span>
          <input id="table-table" v-model="form.table" :disabled="submitting" autocomplete="off" placeholder="order_history" required aria-required="true" aria-describedby="table-table-hint table-form-error" :aria-invalid="!!formError" />
          <span id="table-table-hint" class="field-hint">The exact table identifier in the selected catalog and schema.</span>
        </label>
        <div class="form-actions span-2">
          <button class="primary" type="submit" :disabled="submitting">{{ submitting ? 'Saving...' : 'Save' }}</button>
          <button class="secondary" type="button" :disabled="submitting" @click="closeForm">Cancel</button>
          <span v-if="formError" id="table-form-error" ref="formErrorRef" class="error" role="alert" aria-live="assertive" tabindex="-1">{{ formError }}</span>
        </div>
      </form>
    </div>

    <div v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
      <span>{{ mutationError }}</span>
      <button class="secondary" type="button" @click="mutationError = null">Dismiss</button>
    </div>

    <ResourceTable
      :columns="[
        { key: 'name', label: 'TableRef' },
        { key: 'fullName', label: 'Databricks table' },
        { key: 'warehouseRef', label: 'Warehouse' },
        { key: 'columnCount', label: 'Columns' },
        { key: 'status', label: 'Status' },
        { key: 'actions', label: '' },
      ]"
      :rows="rows"
      row-key="name"
      :loaded="loaded"
      :loading="loading"
      :error="error"
      :stale="loaded && !!error"
      retryable
      :row-aria-label="(row) => `Open table ${String(row.name)}`"
      @retry="load"
      @row-click="(row) => openResource(String(row.name))"
    >
      <template #name="{ value }"><button class="link mono strong" type="button" :disabled="operationLocked(String(value))" @click.stop="openResource(String(value))">{{ value }}</button></template>
      <template #fullName="{ value }"><span class="mono">{{ value }}</span></template>
      <template #warehouseRef="{ value }"><span class="mono">{{ value }}</span></template>
      <template #columnCount="{ value }"><span>{{ value }}</span></template>
      <template #status="{ row }">
        <StatusBadge :status="String(row.status)" />
        <span v-if="row.message" class="row-message">{{ row.message }}</span>
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
