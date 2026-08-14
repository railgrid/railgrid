<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { api } from '../api'
import { confirmDialog } from '../portalkit/confirm'
import type { Connection, ErrorResponse } from '../types'
import { createLatestRefreshController, createOperationLocks, operationKey, type LatestRefreshController } from '../refresh'
import { resourceNameError } from '../resourceName'

const emit = defineEmits<{ (e: 'open', name: string): void }>()

const connections = ref<Connection[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const operations = createOperationLocks()
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

function load() {
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
  if (connections.value.some(connection => connection.name === desiredName)) {
    await focusFormError(`Connection "${desiredName}" already exists.`)
    return
  }
  const lock = operationKey('connection', desiredName)
  if (operations.isTombstoned(lock)) {
    await focusFormError(`Connection "${desiredName}" is still being removed. Retry after the list refresh confirms it is gone.`)
    return
  }
  if (!operations.acquire(lock, 'creating')) {
    await focusFormError(`Connection "${desiredName}" already has an update in progress.`)
    return
  }
  submitting.value = true
  try {
    await api.saveConnection({
      name: desiredName,
      host: host.value,
      token: token.value,
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
  loading.value = true
  try {
    const next = await api.listConnections()
    if (refresh.isCurrent(requestID)) {
      connections.value = next
      operations.reconcile('connection', next.map(({ name, uid }) => ({ name, uid })))
      loaded.value = true
      error.value = null
    }
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (refresh.isCurrent(requestID)) loading.value = false
  }
})

onMounted(() => {
  load()
  timer = window.setInterval(load, 5000)
})
onUnmounted(() => {
  window.clearInterval(timer)
  refresh.stop()
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
        <button class="primary" type="button" :disabled="submitting" @click="showForm ? (showForm = false) : startCreate()">
          {{ showForm ? 'Cancel' : 'Add connection' }}
        </button>
      </div>
    </header>

    <div v-if="showForm" class="panel">
      <h3 class="panel-title">Connect with a token</h3>
      <form class="form" @submit.prevent="submit">
        <div class="field">
          <label class="field-label" for="connection-name">Name</label>
          <input id="connection-name" ref="nameInput" v-model="name" :disabled="submitting" autocomplete="off" placeholder="orders-prod" required aria-required="true" aria-describedby="connection-name-hint connection-form-error" :aria-invalid="!!formError" />
          <span id="connection-name-hint" class="field-hint">How this workspace is referred to from faros. Use lowercase letters, numbers, and hyphens; the name is preserved exactly.</span>
        </div>
        <div class="field">
          <label class="field-label" for="connection-host">Workspace host</label>
          <input id="connection-host" v-model="host" :disabled="submitting" autocomplete="url" placeholder="https://dbc-example.cloud.databricks.com" required aria-required="true" aria-describedby="connection-host-hint connection-form-error" :aria-invalid="!!formError" />
          <span id="connection-host-hint" class="field-hint">Use the HTTPS root URL from the Databricks browser address bar (AWS, Azure, or GCP), with no path.</span>
        </div>
        <div class="field">
          <label class="field-label" for="connection-token">Token</label>
          <input id="connection-token" v-model="token" :disabled="submitting" type="password" autocomplete="new-password" placeholder="Paste token" required aria-required="true" aria-describedby="connection-token-hint connection-form-error" :aria-invalid="!!formError" />
          <span id="connection-token-hint" class="field-hint">Create a personal access token in Databricks: avatar → Settings → Developer → Access tokens → Manage → Generate new token. Its identity needs SELECT on the catalogs and schemas you plan to import, plus access to a running SQL warehouse.</span>
        </div>
        <div class="actions">
      <button class="primary" type="submit" :disabled="submitting">{{ submitting ? 'Connecting...' : 'Create' }}</button>
          <button class="secondary" type="button" :disabled="submitting" @click="() => { resetForm(); showForm = false }">Cancel</button>
          <span v-if="formError" id="connection-form-error" ref="formErrorRef" class="error" role="alert" aria-live="assertive" tabindex="-1">{{ formError }}</span>
        </div>
        <p class="muted">The token is stored as a Secret in your workspace; the provider validates it and shows the status below.</p>
      </form>
    </div>

    <div v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
      <span>{{ mutationError }}</span>
      <button class="secondary" type="button" @click="mutationError = null">Dismiss</button>
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
      row-key="name"
      :loaded="loaded"
      :loading="loading"
      :error="error"
      :stale="loaded && !!error"
      retryable
      empty-text="No connections yet."
      @retry="load"
      @row-click="(row) => openResource(String(row.name))"
    >
      <template #name="{ value }"><button class="link" type="button" :disabled="operationLocked(String(value))" @click.stop="openResource(String(value))">{{ value }}</button></template>
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
