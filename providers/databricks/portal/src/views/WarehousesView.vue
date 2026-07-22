<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { api } from '../api'
import { confirmDialog } from '../portalkit/confirm'
import type { Connection, ErrorResponse, Warehouse } from '../types'

const emit = defineEmits<{ (e: 'open', name: string): void }>()

const connections = ref<Connection[]>([])
const warehouses = ref<Warehouse[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

const showForm = ref(false)
const submitting = ref(false)
const formError = ref<string | null>(null)
let timer: number | undefined

const rows = computed<Array<Record<string, unknown>>>(() => warehouses.value.map(wh => ({ ...wh })))

const form = reactive({
  name: '',
  connectionRef: '',
  warehouseID: '',
})

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

async function load() {
  loading.value = true
  error.value = null
  try {
    const [connList, warehouseList] = await Promise.all([api.listConnections(), api.listWarehouses()])
    connections.value = connList
    warehouses.value = warehouseList
    if (connList.length && !connList.some(c => c.name === form.connectionRef)) {
      form.connectionRef = connList[0].name
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
  if (!form.name || !form.connectionRef || !form.warehouseID) {
    formError.value = 'name, connection, and warehouse ID are required'
    return
  }
  submitting.value = true
  try {
	    await api.saveWarehouse({
	      name: form.name,
	      connectionRef: form.connectionRef,
	      warehouseID: form.warehouseID,
	    })
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
  const wh = row as unknown as Warehouse
  const ok = await confirmDialog({
    title: `Delete warehouse "${wh.name}"?`,
    message: 'Tables that reference this warehouse will stop refreshing schema metadata.',
    confirmLabel: 'Delete',
  })
  if (!ok) return
  try {
    await api.deleteWarehouse(wh.name)
    await load()
  } catch (e) {
    error.value = errMessage(e)
  }
}

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
        <h2 class="page-title">Warehouses</h2>
        <p class="page-meta">SQL warehouses available to imported Databricks tables. Click one to inspect status and defaults.</p>
      </div>
      <button class="primary" :disabled="!connections.length" @click="showForm = !showForm">
        {{ showForm ? 'Cancel' : 'New warehouse' }}
      </button>
    </header>

    <p v-if="!loading && !connections.length" class="empty">Add a connection first, then import warehouses under it.</p>

    <div v-if="showForm" class="panel">
      <h3 class="panel-title">New warehouse</h3>
      <form class="form" @submit.prevent="submit">
        <div class="field">
          <span class="field-label">Connection</span>
          <select v-model="form.connectionRef">
            <option v-for="conn in connections" :key="conn.name" :value="conn.name">{{ conn.name }}</option>
          </select>
        </div>
        <div class="field"><span class="field-label">Object name</span><input v-model="form.name" placeholder="orders-sql" autocomplete="off" /></div>
        <div class="field"><span class="field-label">Warehouse ID</span><input v-model="form.warehouseID" placeholder="abc123def4567890" autocomplete="off" /></div>
	        <div class="actions">
          <button class="primary" type="submit" :disabled="submitting">{{ submitting ? 'Creating…' : 'Create' }}</button>
          <span v-if="formError" class="error">{{ formError }}</span>
        </div>
      </form>
    </div>

    <ResourceTable
      v-if="connections.length || loading || error"
      :columns="[
        { key: 'name', label: 'Name' },
        { key: 'connectionRef', label: 'Connection' },
        { key: 'warehouseID', label: 'Warehouse ID' },
        { key: 'state', label: 'State' },
        { key: 'status', label: 'Status' },
        { key: 'actions', label: '' },
      ]"
      :rows="rows"
      :loading="loading && !warehouses.length"
      :error="error"
      empty-text="No warehouses yet."
      @row-click="(row) => emit('open', String(row.name))"
    >
      <template #name="{ value }">
        <button class="link" type="button" @click.stop="emit('open', String(value))">{{ value }}</button>
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
          <button class="danger" type="button" @click.stop="remove(row)">Delete</button>
        </div>
      </template>
    </ResourceTable>
  </section>
</template>
