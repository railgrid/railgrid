<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowLeft } from 'lucide-vue-next'
import { api } from '../api'
import ConditionsPanel from '../portalkit/ConditionsPanel.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import type { ErrorResponse, Warehouse } from '../types'
import { createLatestRefreshController, createOperationLocks, operationKey, type LatestRefreshController } from '../refresh'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ (e: 'back'): void }>()

const warehouse = ref<Warehouse | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
const editing = ref(false)
const editWarehouseID = ref('')
const saving = ref(false)
const saveError = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const editIDInput = ref<HTMLInputElement | null>(null)
const saveErrorRef = ref<HTMLElement | null>(null)
let timer: number | undefined
let refresh!: LatestRefreshController
const operations = createOperationLocks()

const ready = computed(() => warehouse.value?.conditions.find(c => c.type === 'Ready'))
const reconciled = computed(() =>
  !!warehouse.value &&
  warehouse.value.observedGeneration !== undefined &&
  warehouse.value.generation !== undefined &&
  warehouse.value.observedGeneration >= warehouse.value.generation,
)

const hint = computed(() => {
  const wh = warehouse.value
  if (!wh) return ''
  if (wh.status === 'Ready') return ''
  if (!wh.conditions.length || !reconciled.value) {
    return 'Waiting for the warehouse controller to validate the Databricks warehouse. This usually takes a few seconds after creation.'
  }
  switch (ready.value?.reason) {
    case 'ConnectionUnavailable':
      return `Connection "${wh.connectionRef}" could not be read. Check that it still exists in this workspace.`
    case 'CredentialUnavailable':
      return `The credential for connection "${wh.connectionRef}" could not be read. Check the connection's Secret.`
    case 'ValidationFailed':
      return 'Databricks rejected the warehouse lookup. The warehouse ID may be wrong, deleted, or not visible to this token.'
    default:
      return ready.value?.message || wh.message || 'The warehouse is not ready yet.'
  }
})

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function load() {
  refresh.request()
}

function operationLocked(name: string): boolean {
  return operations.isLocked(operationKey('warehouse', name))
}

function operationPhase(name: string) {
  return operations.phase(operationKey('warehouse', name))
}

function startEdit() {
  if (!warehouse.value) return
  if (operationLocked(warehouse.value.name)) return
  editWarehouseID.value = warehouse.value.warehouseID
  saveError.value = null
  editing.value = true
  void nextTick(() => editIDInput.value?.focus())
}

async function focusSaveError(message: string) {
  saveError.value = message
  await nextTick()
  saveErrorRef.value?.focus()
}

async function saveEdit() {
  if (!warehouse.value) return
  const nextID = editWarehouseID.value.trim()
  if (!nextID) {
    await focusSaveError('Warehouse ID is required.')
    return
  }
  const lock = operationKey('warehouse', warehouse.value.name)
  if (!operations.acquire(lock, 'saving')) {
    await focusSaveError(`Warehouse "${warehouse.value.name}" already has an operation in progress.`)
    return
  }
  saving.value = true
  saveError.value = null
  mutationError.value = null
  try {
    await api.saveWarehouse({
      name: warehouse.value.name,
      connectionRef: warehouse.value.connectionRef,
      warehouseID: nextID,
    })
    editing.value = false
    load()
  } catch (e) {
    await focusSaveError(errMessage(e))
  } finally {
    saving.value = false
    operations.release(lock)
  }
}

async function remove() {
  if (!warehouse.value) return
  const ok = await confirmDialog({
    title: `Delete warehouse "${warehouse.value.name}"?`,
    message: 'Tables that reference this warehouse will stop refreshing schema metadata.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  const lock = operationKey('warehouse', warehouse.value.name)
  if (!operations.acquire(lock, 'deleting')) {
    mutationError.value = `Warehouse "${warehouse.value.name}" already has an operation in progress.`
    return
  }
  mutationError.value = null
  try {
    await api.deleteWarehouse(warehouse.value.name)
    operations.tombstone(lock, warehouse.value.uid)
    emit('back')
  } catch (e) {
    mutationError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

refresh = createLatestRefreshController(async requestID => {
  loading.value = true
  try {
    const next = await api.getWarehouse(props.name)
    if (refresh.isCurrent(requestID)) {
      warehouse.value = next
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

watch(() => props.name, () => {
  warehouse.value = null
  loaded.value = false
  editing.value = false
  error.value = null
  mutationError.value = null
  refresh.invalidate()
  load()
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
    <button class="k-btn k-btn--ghost k-back-action" type="button" :disabled="!!warehouse && operationLocked(warehouse.name)" @click="emit('back')"><ArrowLeft :size="14" aria-hidden="true" /> Warehouses</button>

    <header class="page-head">
      <div>
        <h2 class="page-title">{{ warehouse?.name || name }}</h2>
        <p class="page-meta">
          <span v-if="warehouse?.status === 'Ready'">validated against warehouse <code>{{ warehouse.warehouseID }}</code></span>
          <span v-else-if="warehouse"><code>{{ warehouse.warehouseID }}</code></span>
          <span v-else class="muted">not validated yet</span>
        </p>
      </div>
      <StatusBadge v-if="warehouse" :status="warehouse.status" :title="warehouse.message" />
    </header>

    <div v-if="error && !warehouse" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ error }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="load">Retry</button>
    </div>
    <p v-else-if="loading && !warehouse" class="muted" role="status" aria-live="polite">Loading…</p>
    <div v-if="error && warehouse" class="error read-error" role="alert" aria-live="assertive">
      <span>Showing cached warehouse data. {{ error }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="load">Retry</button>
    </div>
    <span v-else-if="loading && warehouse" class="sr-only" role="status" aria-live="polite">Updating…</span>
    <div v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
      <span>{{ mutationError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="mutationError = null">Dismiss</button>
    </div>

    <template v-if="warehouse">
      <div v-if="hint" class="databricks-resource-panel k-card">
        <h3 class="databricks-resource-panel-title">Status</h3>
        <p class="muted">{{ hint }}</p>
      </div>

      <div class="databricks-resource-panel k-card">
        <h3 class="databricks-resource-panel-title">Overview</h3>
        <dl class="props">
          <dt>Connection</dt><dd><code>{{ warehouse.connectionRef }}</code></dd>
          <dt>Warehouse ID</dt><dd><code>{{ warehouse.warehouseID }}</code></dd>
          <dt>State</dt><dd>{{ warehouse.state || '—' }}</dd>
          <dt v-if="warehouse.creationTimestamp">Created</dt><dd v-if="warehouse.creationTimestamp">{{ warehouse.creationTimestamp }}</dd>
          <dt v-if="warehouse.observedGeneration !== undefined">Reconciled</dt>
          <dd v-if="warehouse.observedGeneration !== undefined">
            <span v-if="reconciled" class="muted">up to date (generation {{ warehouse.generation }})</span>
            <span v-else class="warning">controller has not caught up (spec {{ warehouse.generation }}, observed {{ warehouse.observedGeneration }})</span>
          </dd>
        </dl>
      </div>

      <div v-if="editing" class="databricks-resource-panel k-card">
        <h3 class="databricks-resource-panel-title">Edit warehouse</h3>
        <form class="form" @submit.prevent="saveEdit">
          <div class="field">
            <label class="field-label" for="warehouse-edit-id">Warehouse ID</label>
            <input id="warehouse-edit-id" class="k-input" ref="editIDInput" v-model="editWarehouseID" :disabled="saving" placeholder="abc123def4567890" autocomplete="off" required aria-required="true" aria-describedby="warehouse-edit-id-hint warehouse-edit-error" :aria-invalid="!!saveError" />
            <span id="warehouse-edit-id-hint" class="field-hint">Use the 16-character ID from SQL Warehouses → Connection details (/sql/1.0/warehouses/&lt;id&gt;), not the numeric ?o= workspace ID.</span>
          </div>
        <div class="actions">
            <button class="k-btn k-btn--primary" type="submit" :disabled="saving || operationLocked(warehouse.name)">{{ saving ? 'Saving…' : 'Save' }}</button>
            <button class="k-btn k-btn--ghost" type="button" :disabled="saving" @click="editing = false">Cancel</button>
            <span v-if="saveError" id="warehouse-edit-error" ref="saveErrorRef" class="error" role="alert" aria-live="assertive" tabindex="-1">{{ saveError }}</span>
          </div>
        </form>
      </div>

      <ConditionsPanel
        :conditions="warehouse.conditions"
        :generation="warehouse.generation"
        :observed-generation="warehouse.observedGeneration"
        empty-text="No conditions yet. Warehouse validation has not reported status for this resource."
      />

      <div class="actions">
        <button class="k-btn k-btn--ghost" type="button" :disabled="operationLocked(warehouse.name)" @click="startEdit">Edit</button>
        <button class="k-btn k-btn--danger resource-delete-button" type="button" :disabled="operationLocked(warehouse.name)" @click="remove">{{ operationPhase(warehouse.name) === 'deleting' ? 'Deleting warehouse…' : 'Delete warehouse' }}</button>
      </div>
    </template>
  </section>
</template>
