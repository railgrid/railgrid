<script setup lang="ts">
import { ArrowLeft, ArrowRight, LoaderCircle, RefreshCw } from 'lucide-vue-next'
import { computed, inject, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { api } from '../api'
import { contextGenerationKey } from '../context'
import { formatDatabricksError } from '../errors'
import {
  createOperationLocks,
  operationKey,
} from '../refresh'
import { resourceNameError } from '../resourceName'
import type { DatabricksPrerequisiteKind } from '../journey'
import type { Connection } from '../types'

const emit = defineEmits<{
  (event: 'cancel'): void
  (event: 'created', name: string): void
  (event: 'prerequisite', kind: DatabricksPrerequisiteKind): void
}>()

const connections = ref<Connection[]>([])
const loading = ref(false)
const loaded = ref(false)
const loadError = ref<string | null>(null)
const submitting = ref(false)
const formError = ref<string | null>(null)
const nameInput = ref<HTMLInputElement | null>(null)
const formErrorRef = ref<HTMLElement | null>(null)
const operations = createOperationLocks()
const contextGeneration = inject(contextGenerationKey, ref(0))

// Route-owned forms may be replaced while a read or write is in flight. Every
// async continuation must belong to the mounted form and latest attempt that
// started it.
let mounted = false
let readGeneration = 0
let mutationGeneration = 0

interface ReadToken {
  generation: number
  context: number
}

const form = reactive({
  name: '',
  connectionRef: '',
  warehouseID: '',
})

const hasConnections = computed(() => connections.value.length > 0)

function isCurrentRead(generation: number, expectedContext: number): boolean {
  return mounted && generation === readGeneration && contextGeneration.value === expectedContext
}

function isCurrentMutation(generation: number, expectedContext: number): boolean {
  return mounted && generation === mutationGeneration && contextGeneration.value === expectedContext
}

async function load(): Promise<ReadToken | null> {
  const generation = ++readGeneration
  const expectedContext = contextGeneration.value
  loading.value = true
  loaded.value = false
  loadError.value = null
  try {
    const availableConnections = await api.listConnections()
    if (!isCurrentRead(generation, expectedContext)) return null
    connections.value = availableConnections
    if (!connections.value.some(connection => connection.name === form.connectionRef)) {
      form.connectionRef = connections.value[0]?.name ?? ''
    }
    loaded.value = true
    return { generation, context: expectedContext }
  } catch (error) {
    if (!isCurrentRead(generation, expectedContext)) return null
    loadError.value = formatDatabricksError(error)
    return null
  } finally {
    if (isCurrentRead(generation, expectedContext)) loading.value = false
  }
}

async function focusFormError(message: string, generation: number, expectedContext: number): Promise<void> {
  if (!isCurrentMutation(generation, expectedContext)) return
  formError.value = message
  await nextTick()
  if (isCurrentMutation(generation, expectedContext)) formErrorRef.value?.focus()
}

async function submit(): Promise<void> {
  if (!mounted || submitting.value) return
  const generation = ++mutationGeneration
  const expectedContext = contextGeneration.value
  formError.value = null
  if (!loaded.value) {
    await focusFormError('Connection list is still loading. Retry the read before creating a warehouse.', generation, expectedContext)
    return
  }
  if (!form.name || !form.connectionRef || !form.warehouseID) {
    await focusFormError('Name, connection, and warehouse ID are required.', generation, expectedContext)
    return
  }
  const nameError = resourceNameError(form.name, 'Name')
  if (nameError) {
    await focusFormError(nameError, generation, expectedContext)
    return
  }
  const desiredName = form.name.trim()
  const payload = {
    name: desiredName,
    connectionRef: form.connectionRef,
    warehouseID: form.warehouseID,
  }
  const lock = operationKey('warehouse', desiredName)
  if (!operations.acquire(lock, 'creating')) {
    await focusFormError(`Warehouse "${desiredName}" already has an update in progress.`, generation, expectedContext)
    return
  }
  submitting.value = true
  try {
    // Re-read both authoritative collections immediately before applying. A
    // list page can be stale while this route-owned form remains open.
    const [existing, availableConnections] = await Promise.all([
      api.listWarehouses(),
      api.listConnections(),
    ])
    if (!isCurrentMutation(generation, expectedContext)) return
    operations.reconcile('warehouse', existing.map(({ name, uid }) => ({ name, uid })))
    if (operations.isTombstoned(lock)) {
      await focusFormError(`Warehouse "${desiredName}" is still being removed. Retry after the list refresh confirms it is gone.`, generation, expectedContext)
      return
    }
    if (existing.some(warehouse => warehouse.name === desiredName)) {
      await focusFormError(`Warehouse "${desiredName}" already exists.`, generation, expectedContext)
      return
    }
    if (!availableConnections.some(connection => connection.name === payload.connectionRef)) {
      await focusFormError('Selected connection is no longer available in this workspace.', generation, expectedContext)
      return
    }
    const created = await api.saveWarehouse(payload)
    if (!isCurrentMutation(generation, expectedContext)) return
    emit('created', created.name)
  } catch (error) {
    await focusFormError(formatDatabricksError(error), generation, expectedContext)
  } finally {
    operations.release(lock)
    if (isCurrentMutation(generation, expectedContext)) submitting.value = false
  }
}

function cancel(): void {
  if (!submitting.value) emit('cancel')
}

onMounted(() => {
  mounted = true
  void load().then(readToken => {
    if (readToken && isCurrentRead(readToken.generation, readToken.context)) nameInput.value?.focus()
  })
})

onBeforeUnmount(() => {
  mounted = false
  readGeneration += 1
  mutationGeneration += 1
})
</script>

<template>
  <section class="page k-create-page" :aria-busy="loading || submitting ? 'true' : 'false'">
    <button class="k-btn k-btn--ghost k-back-action" type="button" :disabled="submitting" @click="cancel">
      <ArrowLeft :size="14" aria-hidden="true" /> Warehouses
    </button>
    <header class="k-create-header">
      <h2 class="k-create-title">Register warehouse</h2>
      <p class="k-create-description">Register a Databricks SQL warehouse for table imports.</p>
    </header>

    <form class="k-create-surface" @submit.prevent="submit">
      <div class="k-create-body">
      <p v-if="loading" class="muted" role="status"><LoaderCircle class="spin" :size="14" aria-hidden="true" /> Loading connections…</p>
      <p v-if="loadError" class="error" role="alert" aria-live="assertive">
        <span>Could not load warehouse prerequisites: {{ loadError }}</span>
        <button class="k-btn k-btn--ghost" type="button" @click="load"><RefreshCw :size="14" aria-hidden="true" /> Retry</button>
      </p>
      <p v-if="loaded && !hasConnections" class="prerequisite" role="status">
        <span class="prerequisite-copy">Add a connection before registering a warehouse.</span>
        <button class="k-btn k-btn--ghost prerequisite-action" type="button" @click="emit('prerequisite', 'connection')">
          Create connection <ArrowRight :size="14" :stroke-width="1.75" aria-hidden="true" />
        </button>
      </p>
        <div class="field">
          <label class="field-label" for="warehouse-connection">Connection</label>
          <select id="warehouse-connection" class="k-input" v-model="form.connectionRef" :disabled="loading || submitting || !hasConnections" required aria-required="true" aria-describedby="warehouse-connection-hint warehouse-form-error" :aria-invalid="!!formError">
            <option value="" disabled>Select connection</option>
            <option v-for="conn in connections" :key="conn.name" :value="conn.name">{{ conn.name }}</option>
          </select>
          <span id="warehouse-connection-hint" class="field-hint">The Databricks workspace connection this warehouse belongs to.</span>
        </div>
        <div class="field">
          <label class="field-label" for="warehouse-name">Object name</label>
          <input id="warehouse-name" ref="nameInput" class="k-input" v-model="form.name" :disabled="loading || submitting" placeholder="orders-sql" autocomplete="off" required aria-required="true" aria-describedby="warehouse-name-hint warehouse-form-error" :aria-invalid="!!formError" />
          <span id="warehouse-name-hint" class="field-hint">How this warehouse is referred to from faros. Use lowercase letters, numbers, and hyphens; the name is preserved exactly.</span>
        </div>
        <div class="field">
          <label class="field-label" for="warehouse-id">Warehouse ID</label>
          <input id="warehouse-id" class="k-input" v-model="form.warehouseID" :disabled="loading || submitting" placeholder="abc123def4567890" autocomplete="off" required aria-required="true" aria-describedby="warehouse-id-hint warehouse-form-error" :aria-invalid="!!formError" />
          <span id="warehouse-id-hint" class="field-hint">Use the warehouse’s 16-character ID. The connection token needs “Can use” permission.</span>
          <details class="field-disclosure">
            <summary>Where to find the warehouse ID</summary>
            <p>In Databricks, open SQL → SQL Warehouses → your warehouse → Connection details. Copy the value after <code>/sql/1.0/warehouses/</code>, not the numeric <code>?o=</code> workspace ID.</p>
          </details>
        </div>
      </div>
      <div class="k-create-actions">
        <span v-if="formError" id="warehouse-form-error" ref="formErrorRef" class="error" role="alert" aria-live="assertive" tabindex="-1">{{ formError }}</span>
        <button class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="cancel">Cancel</button>
        <button class="k-btn k-btn--primary" type="submit" :disabled="loading || submitting || !hasConnections">{{ submitting ? 'Registering…' : 'Register warehouse' }}</button>
      </div>
    </form>
  </section>
</template>
