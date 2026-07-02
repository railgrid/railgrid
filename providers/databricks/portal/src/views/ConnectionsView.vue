<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import ResourceTable from '../components/ResourceTable.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { api } from '../api'
import { confirmDialog } from '../components/confirm'
import type { Connection, ErrorResponse } from '../types'

const emit = defineEmits<{ (e: 'open', name: string): void }>()

const connections = ref<Connection[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const rows = computed<Array<Record<string, unknown>>>(() => connections.value.map(conn => ({ ...conn })))

// connection form
const showForm = ref(false)
const name = ref('')
const host = ref('')
const token = ref('')
const submitting = ref(false)
const formError = ref<string | null>(null)
let timer: number | undefined

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
}

async function load() {
  loading.value = true
  error.value = null
  try {
    connections.value = await api.listConnections()
  } catch (e) {
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    loading.value = false
  }
}

async function submit() {
  formError.value = null
  if (!name.value || !host.value || !token.value) {
    formError.value = 'name, workspace host, and token are required'
    return
  }
  submitting.value = true
  try {
	    await api.saveConnection({
	      name: name.value,
	      host: host.value,
	      token: token.value,
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
  const conn = row as unknown as Connection
  const ok = await confirmDialog({
    title: `Delete connection "${conn.name}"?`,
    message: 'Warehouses and tables that reference this connection will stop working.',
    confirmLabel: 'Delete',
  })
  if (!ok) return
  try {
    await api.deleteConnection(conn)
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
        <h2 class="page-title">Connections</h2>
        <p class="page-meta">Databricks workspaces available to tables in this kedge workspace.</p>
      </div>
      <div class="actions">
        <button class="primary" type="button" @click="showForm ? (showForm = false) : startCreate()">
          {{ showForm ? 'Cancel' : 'Add connection' }}
        </button>
      </div>
    </header>

    <div v-if="showForm" class="panel">
      <h3 class="panel-title">Connect with a token</h3>
      <form class="form" @submit.prevent="submit">
        <div class="field"><span class="field-label">Name</span><input v-model="name" autocomplete="off" placeholder="orders-prod" /></div>
        <div class="field"><span class="field-label">Workspace host</span><input v-model="host" autocomplete="off" placeholder="https://dbc-example.cloud.databricks.com" /></div>
	        <div class="field"><span class="field-label">Token</span><input v-model="token" type="password" autocomplete="off" placeholder="Paste token" /></div>
        <div class="actions">
          <button class="primary" type="submit" :disabled="submitting">{{ submitting ? 'Connecting...' : 'Create' }}</button>
          <button class="secondary" type="button" @click="() => { resetForm(); showForm = false }">Cancel</button>
          <span v-if="formError" class="error">{{ formError }}</span>
        </div>
        <p class="muted">The token is stored as a Secret in your workspace; the provider validates it and shows the status below.</p>
      </form>
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
      :loading="loading && !connections.length"
      :error="error"
      empty-text="No connections yet."
      @row-click="(row) => emit('open', String(row.name))"
    >
      <template #name="{ value }"><button class="link" type="button" @click.stop="emit('open', String(value))">{{ value }}</button></template>
      <template #host="{ value }"><code>{{ value }}</code></template>
      <template #authType="{ value }">{{ value }}</template>
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
