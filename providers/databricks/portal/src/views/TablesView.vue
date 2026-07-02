<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { Pencil, Plus, RefreshCw, Trash2 } from 'lucide-vue-next'
import ResourceTable from '../components/ResourceTable.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { api } from '../api'
import { confirmDialog } from '../components/confirm'
import { importPrerequisiteMessage, nextValidWarehouseRef, warehousesForConnection } from '../tableRefs'
import type { Connection, ErrorResponse, Table, Warehouse } from '../types'

const emit = defineEmits<{ (e: 'open', name: string): void }>()

const connections = ref<Connection[]>([])
const warehouses = ref<Warehouse[]>([])
const tables = ref<Table[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const showForm = ref(false)
const editing = ref<string | null>(null)
const submitting = ref(false)
const formError = ref<string | null>(null)
const selectedTable = ref('')
let timer: number | undefined

const form = reactive({
  name: '',
  connectionRef: '',
  warehouseRef: '',
  catalog: '',
  schema: '',
  table: '',
})

const rows = computed<Array<Record<string, unknown>>>(() =>
  tables.value.map(t => ({
    ...t,
    columnCount: t.columns.length ? String(t.columns.length) : '-',
  })),
)

const selected = computed(() => tables.value.find(t => t.name === selectedTable.value) ?? null)
const schemaRows = computed<Array<Record<string, unknown>>>(() =>
  (selected.value?.columns ?? []).map(c => ({ ...c, nullableLabel: c.nullable ? 'yes' : 'no' })),
)
const tableImportBlocker = computed(() => loading.value ? '' : importPrerequisiteMessage(connections.value, warehouses.value))
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
  if (loading.value) return
  if (tableImportBlocker.value) return
  resetForm()
  showForm.value = true
}

function editTable(row: Record<string, unknown>) {
  const table = row as unknown as Table
  editing.value = table.name
  form.name = table.name
  form.connectionRef = table.connectionRef
  form.warehouseRef = table.warehouseRef
  form.catalog = table.catalog
  form.schema = table.schema
  form.table = table.table
  selectedTable.value = table.name
  formError.value = null
  showForm.value = true
}

async function load() {
  loading.value = true
  error.value = null
  try {
    const [connList, warehouseList, tableList] = await Promise.all([
      api.listConnections(),
      api.listWarehouses(),
      api.listTables(),
    ])
    connections.value = connList
    warehouses.value = warehouseList
    tables.value = tableList
    if (!form.connectionRef) form.connectionRef = connList[0]?.name ?? ''
    if (connList.length && !connList.some(c => c.name === form.connectionRef)) form.connectionRef = connList[0].name
    form.warehouseRef = nextValidWarehouseRef(warehouseList, form.connectionRef, form.warehouseRef)
    if (!selectedTable.value && tableList.length) selectedTable.value = tableList[0].name
    if (selectedTable.value && !tableList.some(table => table.name === selectedTable.value)) {
      selectedTable.value = tableList[0]?.name ?? ''
    }
  } catch (e) {
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    loading.value = false
  }
}

async function submit() {
  formError.value = null
  if (tableImportBlocker.value) {
    formError.value = tableImportBlocker.value
    return
  }
  if (!form.name || !form.connectionRef || !form.warehouseRef || !form.catalog || !form.schema || !form.table) {
    formError.value = 'all table fields are required'
    return
  }
  if (!formWarehouses.value.some(warehouse => warehouse.name === form.warehouseRef)) {
    formError.value = 'selected warehouse must belong to the selected connection'
    return
  }
  submitting.value = true
  try {
    const saved = await api.saveTable({
      name: form.name,
      connectionRef: form.connectionRef,
      warehouseRef: form.warehouseRef,
      catalog: form.catalog,
      schema: form.schema,
      table: form.table,
    })
    selectedTable.value = saved.name
    resetForm()
    showForm.value = false
    await load()
  } catch (e) {
    formError.value = errMessage(e)
  } finally {
    submitting.value = false
  }
}

async function remove(row: Record<string, unknown>) {
  const table = row as unknown as Table
  const ok = await confirmDialog({
    title: `Delete table "${table.name}"?`,
    message: 'App Studio guidance and Databricks MCP tools will no longer be able to inspect this tableRef.',
    confirmLabel: 'Delete',
  })
  if (!ok) return
  try {
    await api.deleteTable(table.name)
    if (selectedTable.value === table.name) selectedTable.value = ''
    await load()
  } catch (e) {
    error.value = errMessage(e)
  }
}

watch(() => form.connectionRef, connectionRef => {
  form.warehouseRef = nextValidWarehouseRef(warehouses.value, connectionRef, form.warehouseRef)
})

onMounted(() => {
  load()
  timer = window.setInterval(load, 5000)
})
onUnmounted(() => window.clearInterval(timer))
</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Tables</h2>
        <p class="page-meta">Imported table handles that App Studio can use by tableRef.</p>
      </div>
      <div class="actions">
        <button class="secondary icon-text" type="button" :disabled="loading" @click="load">
          <RefreshCw class="button-icon" :stroke-width="1.75" />
          Refresh
        </button>
        <button class="primary icon-text" type="button" :disabled="loading || !!tableImportBlocker" :title="tableImportBlocker" @click="showForm ? (showForm = false) : startCreate()">
          <Plus class="button-icon" :stroke-width="1.75" />
          {{ showForm ? 'Cancel' : 'Import table' }}
        </button>
      </div>
    </header>

    <p v-if="tableImportBlocker" class="empty">{{ tableImportBlocker }}</p>

    <div v-if="showForm" class="panel">
      <div class="panel-head">
        <h3 class="panel-title">{{ editing ? 'Update table' : 'Import table' }}</h3>
      </div>
      <form class="form-grid" @submit.prevent="submit">
        <label class="field">
          <span class="field-label">Name</span>
          <input v-model="form.name" :disabled="!!editing" autocomplete="off" placeholder="order-history" />
        </label>
        <label class="field">
          <span class="field-label">Connection</span>
          <select v-model="form.connectionRef">
            <option value="" disabled>Select connection</option>
            <option v-for="conn in connections" :key="conn.name" :value="conn.name">{{ conn.name }}</option>
          </select>
        </label>
        <label class="field">
          <span class="field-label">Warehouse</span>
          <select v-model="form.warehouseRef">
            <option value="" disabled>{{ formWarehouses.length ? 'Select warehouse' : 'No warehouses for this connection' }}</option>
            <option v-for="wh in formWarehouses" :key="wh.name" :value="wh.name">{{ wh.name }}</option>
          </select>
        </label>
        <label class="field">
          <span class="field-label">Catalog</span>
          <input v-model="form.catalog" autocomplete="off" placeholder="sales" />
        </label>
        <label class="field">
          <span class="field-label">Schema</span>
          <input v-model="form.schema" autocomplete="off" placeholder="gold" />
        </label>
        <label class="field">
          <span class="field-label">Table</span>
          <input v-model="form.table" autocomplete="off" placeholder="order_history" />
        </label>
        <div class="form-actions span-2">
          <button class="primary" type="submit" :disabled="submitting">{{ submitting ? 'Saving...' : 'Save' }}</button>
          <button class="secondary" type="button" @click="() => { resetForm(); showForm = false }">Cancel</button>
          <span v-if="formError" class="error">{{ formError }}</span>
        </div>
      </form>
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
      :loading="loading && rows.length === 0"
      :error="error"
      @row-click="(row) => emit('open', String(row.name))"
    >
      <template #name="{ value }"><span class="mono strong">{{ value }}</span></template>
      <template #fullName="{ value }"><span class="mono">{{ value }}</span></template>
      <template #warehouseRef="{ value }"><span class="mono">{{ value }}</span></template>
      <template #columnCount="{ value }"><span>{{ value }}</span></template>
      <template #status="{ row }">
        <StatusBadge :status="String(row.status)" />
        <span v-if="row.message" class="row-message">{{ row.message }}</span>
      </template>
      <template #actions="{ row }">
        <div class="row-actions">
          <button class="icon-button" type="button" title="Edit" @click.stop="editTable(row)">
            <Pencil class="button-icon" :stroke-width="1.75" />
          </button>
          <button class="icon-button danger" type="button" title="Delete" @click.stop="remove(row)">
            <Trash2 class="button-icon" :stroke-width="1.75" />
          </button>
        </div>
      </template>
    </ResourceTable>

    <section class="panel">
      <div class="panel-head">
        <h3 class="panel-title">Schema</h3>
      </div>
      <label class="field">
        <span class="field-label">TableRef</span>
        <select v-model="selectedTable">
          <option value="" disabled>Select table</option>
          <option v-for="table in tables" :key="table.name" :value="table.name">{{ table.name }}</option>
        </select>
      </label>
      <p v-if="selected" class="muted">
        <span class="mono">{{ selected.fullName }}</span>
      </p>
      <ResourceTable
        :columns="[
          { key: 'name', label: 'Column' },
          { key: 'type', label: 'Type' },
          { key: 'nullableLabel', label: 'Nullable' },
          { key: 'comment', label: 'Comment' },
        ]"
        :rows="schemaRows"
        :interactive="false"
        empty-text="No columns have been reported yet."
      >
        <template #name="{ value }"><span class="mono strong">{{ value }}</span></template>
        <template #type="{ value }"><span class="mono">{{ value }}</span></template>
      </ResourceTable>
    </section>
  </section>
</template>
