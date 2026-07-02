<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import ConditionsPanel from '../components/ConditionsPanel.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { confirmDialog } from '../components/confirm'
import type { ErrorResponse, Warehouse } from '../types'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ (e: 'back'): void }>()

const warehouse = ref<Warehouse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
let timer: number | undefined

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

async function load() {
  loading.value = true
  error.value = null
  try {
    warehouse.value = await api.getWarehouse(props.name)
  } catch (e) {
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    loading.value = false
  }
}

async function remove() {
  if (!warehouse.value) return
  const ok = await confirmDialog({
    title: `Delete warehouse "${warehouse.value.name}"?`,
    message: 'Tables that reference this warehouse will stop refreshing schema metadata.',
    confirmLabel: 'Delete',
  })
  if (!ok) return
  try {
    await api.deleteWarehouse(warehouse.value.name)
    emit('back')
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
    <button class="link back" type="button" @click="emit('back')">← Warehouses</button>

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

    <p v-if="error" class="error">{{ error }}</p>
    <p v-else-if="loading && !warehouse" class="muted">Loading…</p>

    <template v-else-if="warehouse">
      <div v-if="hint" class="panel">
        <h3 class="panel-title">Status</h3>
        <p class="muted">{{ hint }}</p>
      </div>

      <div class="panel">
        <h3 class="panel-title">Overview</h3>
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

      <ConditionsPanel
        :conditions="warehouse.conditions"
        :generation="warehouse.generation"
        :observed-generation="warehouse.observedGeneration"
        empty-text="No conditions yet. Warehouse validation has not reported status for this resource."
      />

      <div class="actions">
        <button class="danger" type="button" @click="remove">Delete warehouse</button>
      </div>
    </template>
  </section>
</template>
