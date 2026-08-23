<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowLeft } from 'lucide-vue-next'
import ResourceTable from '../portalkit/ResourceTable.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { api } from '../api'
import ConditionsPanel from '../portalkit/ConditionsPanel.vue'
import { confirmDialog } from '../portalkit/confirm'
import type { ErrorResponse, Table, TableColumn } from '../types'
import { createLatestRefreshController, createOperationLocks, operationKey, type LatestRefreshController } from '../refresh'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ (e: 'back'): void }>()

const table = ref<Table | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const schemaCache = ref<{ uid?: string; generation?: number; refreshedAt?: string; columns: TableColumn[] } | null>(null)
let timer: number | undefined
let refresh!: LatestRefreshController
const operations = createOperationLocks()

const ready = computed(() => table.value?.conditions.find(c => c.type === 'Ready'))
const reconciled = computed(() =>
  !!table.value &&
  table.value.observedGeneration !== undefined &&
  table.value.generation !== undefined &&
  table.value.observedGeneration >= table.value.generation,
)
const schemaRows = computed<Array<Record<string, unknown>>>(() =>
  (table.value?.columns ?? []).map(c => ({ ...c, nullableLabel: c.nullable ? 'yes' : 'no' })),
)
const schemaTruncated = computed(() => ready.value?.reason === 'SchemaTruncated')
const schemaNotice = computed(() => schemaTruncated.value ? (ready.value?.message || 'The schema cache contains only a bounded prefix of the Databricks columns.') : '')
const schemaPending = computed(() => !loaded.value || (!!table.value && table.value.status !== 'Ready'))
const schemaCached = computed(() => !!schemaCache.value)
const schemaLoaded = computed(() => loaded.value && (!table.value || table.value.status === 'Ready' || schemaCached.value))
const schemaError = computed(() => {
  if (!loaded.value) return error.value
  if (table.value?.status === 'Retrying') return table.value.message || 'The table schema could not be validated. The controller will retry.'
  if (table.value?.status === 'Needs attention') return table.value.message || 'The table schema needs attention before it can be displayed.'
  if (table.value?.status === 'Pending' && schemaCached.value) return `Schema refresh is pending; showing cached columns. ${table.value.message || ''}`.trim()
  if (table.value?.status === 'Status unavailable' && schemaCached.value) return 'Schema status is unavailable; showing cached columns until the controller reports a result.'
  return null
})

const hint = computed(() => {
  const tbl = table.value
  if (!tbl) return ''
  if (tbl.status === 'Ready') return ''
  if (!tbl.conditions.length || !reconciled.value) {
    return 'Waiting for the table controller to validate the Databricks table schema. This usually takes a few seconds after import.'
  }
  switch (ready.value?.reason) {
    case 'WarehouseUnavailable':
      return `Warehouse "${tbl.warehouseRef}" could not be read. Check that it still exists in this workspace.`
    case 'WarehouseConnectionMismatch':
      return `Table connection "${tbl.connectionRef}" does not match warehouse "${tbl.warehouseRef}".`
    case 'ConnectionUnavailable':
      return `Connection "${tbl.connectionRef}" could not be read. Check that it still exists in this workspace.`
    case 'CredentialUnavailable':
      return `The credential for connection "${tbl.connectionRef}" could not be read. Check the connection's Secret.`
    case 'UnsupportedTableType':
      return 'Databricks metric views are not supported yet. Import a standard table or view, or wait for future metric-view support.'
    case 'ValidationFailed':
      return 'Databricks rejected the table schema validation request. The catalog, schema, or table may be wrong, or the token may not have access.'
    default:
      return ready.value?.message || tbl.message || 'The table is not ready yet.'
  }
})

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function applySchemaCache(next: Table): Table {
  if (next.status === 'Ready') {
    schemaCache.value = { uid: next.uid, generation: next.generation, refreshedAt: next.refreshedAt, columns: [...next.columns] }
    return next
  }
  if (next.status === 'Needs attention') {
    schemaCache.value = null
    return next
  }
  const cached = schemaCache.value
  if (cached && cached.uid === next.uid && cached.generation === next.generation) {
    return { ...next, columns: [...cached.columns], refreshedAt: cached.refreshedAt }
  }
  schemaCache.value = null
  return next
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

async function remove() {
  if (!table.value) return
  const ok = await confirmDialog({
    title: `Delete table "${table.value.name}"?`,
    message: 'App Studio guidance and Databricks MCP tools will no longer be able to inspect this tableRef.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  const lock = operationKey('table', table.value.name)
  if (!operations.acquire(lock, 'deleting')) {
    mutationError.value = `Table "${table.value.name}" already has an operation in progress.`
    return
  }
  mutationError.value = null
  try {
    await api.deleteTable(table.value.name)
    operations.tombstone(lock, table.value.uid)
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
    const next = await api.getTable(props.name)
    if (refresh.isCurrent(requestID)) {
      table.value = applySchemaCache(next)
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
  table.value = null
  schemaCache.value = null
  loaded.value = false
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
    <button class="link back" type="button" :disabled="!!table && operationLocked(table.name)" @click="emit('back')"><ArrowLeft :size="14" aria-hidden="true" /> Tables</button>

    <header class="page-head">
      <div>
        <h2 class="page-title">{{ table?.name || name }}</h2>
        <p class="page-meta">
          <span v-if="table?.status === 'Ready'">validated against <code>{{ table.fullName }}</code></span>
          <span v-else-if="table"><code>{{ table.fullName }}</code></span>
          <span v-else class="muted">not validated yet</span>
        </p>
      </div>
      <StatusBadge v-if="table" :status="table.status" :title="table.message" />
    </header>

    <div v-if="error && !table" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ error }}</span>
      <button class="secondary" type="button" @click="load">Retry</button>
    </div>
    <p v-else-if="loading && !table" class="muted" role="status" aria-live="polite">Loading…</p>
    <div v-if="error && table" class="error read-error" role="alert" aria-live="assertive">
      <span>Showing cached table data. {{ error }}</span>
      <button class="secondary" type="button" @click="load">Retry</button>
    </div>
    <span v-else-if="loading && table" class="sr-only" role="status" aria-live="polite">Updating…</span>
    <div v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
      <span>{{ mutationError }}</span>
      <button class="secondary" type="button" @click="mutationError = null">Dismiss</button>
    </div>

    <template v-if="table">
      <div v-if="hint" class="databricks-resource-panel k-card">
        <h3 class="databricks-resource-panel-title">Status</h3>
        <p class="muted">{{ hint }}</p>
      </div>

      <div class="databricks-resource-panel k-card">
        <h3 class="databricks-resource-panel-title">Overview</h3>
        <dl class="props">
          <dt>Connection</dt><dd><code>{{ table.connectionRef }}</code></dd>
          <dt>Warehouse</dt><dd><code>{{ table.warehouseRef }}</code></dd>
          <dt>Catalog</dt><dd><code>{{ table.catalog }}</code></dd>
          <dt>Schema</dt><dd><code>{{ table.schema }}</code></dd>
          <dt>Table</dt><dd><code>{{ table.table }}</code></dd>
          <dt>Columns</dt>
          <dd>
            <span v-if="schemaTruncated">{{ table.columns.length }} (schema cache truncated)</span>
            <span v-else>{{ table.columns.length }}</span>
          </dd>
          <dt v-if="table.refreshedAt">Refreshed</dt><dd v-if="table.refreshedAt">{{ table.refreshedAt }}</dd>
          <dt v-if="table.creationTimestamp">Created</dt><dd v-if="table.creationTimestamp">{{ table.creationTimestamp }}</dd>
          <dt v-if="table.observedGeneration !== undefined">Reconciled</dt>
          <dd v-if="table.observedGeneration !== undefined">
            <span v-if="reconciled" class="muted">up to date (generation {{ table.generation }})</span>
            <span v-else class="warning">controller has not caught up (spec {{ table.generation }}, observed {{ table.observedGeneration }})</span>
          </dd>
        </dl>
      </div>

      <div class="databricks-resource-panel k-card">
        <div class="databricks-resource-panel-head">
          <h3 class="databricks-resource-panel-title">Schema</h3>
          <span class="muted">{{ schemaTruncated ? `${table.columns.length} cached columns` : `${table.columns.length} columns` }}</span>
        </div>
        <p v-if="schemaTruncated" class="warning" role="status">{{ schemaNotice }}</p>
        <ResourceTable
          :columns="[
            { key: 'name', label: 'Column' },
            { key: 'type', label: 'Type' },
            { key: 'nullableLabel', label: 'Nullable' },
            { key: 'comment', label: 'Comment' },
          ]"
          :rows="schemaRows"
          row-key="name"
          :loaded="schemaLoaded"
          :loading="schemaPending"
          :error="schemaError"
          :stale="schemaPending && schemaCached"
          retryable
          :interactive="false"
          empty-text="No columns have been reported yet."
          @retry="load"
        >
          <template #name="{ value }"><span class="mono strong">{{ value }}</span></template>
          <template #type="{ value }"><span class="mono">{{ value }}</span></template>
        </ResourceTable>
      </div>

      <ConditionsPanel
        :conditions="table.conditions"
        :generation="table.generation"
        :observed-generation="table.observedGeneration"
        empty-text="No conditions yet. Table validation has not reported status for this resource."
      />

      <div class="actions">
        <button class="danger resource-delete-button" type="button" :disabled="operationLocked(table.name)" @click="remove">{{ operationPhase(table.name) === 'deleting' ? 'Deleting table…' : 'Delete table' }}</button>
      </div>
    </template>
  </section>
</template>
