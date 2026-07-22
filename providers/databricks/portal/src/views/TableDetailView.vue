<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { api } from '../api'
import ConditionsPanel from '../portalkit/ConditionsPanel.vue'
import { confirmDialog } from '../portalkit/confirm'
import type { ErrorResponse, Table } from '../types'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ (e: 'back'): void }>()

const table = ref<Table | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
let timer: number | undefined

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

const hint = computed(() => {
  const tbl = table.value
  if (!tbl) return ''
  if (tbl.status === 'Ready') return ''
  if (!tbl.conditions.length || !reconciled.value) {
    return 'Waiting for the table controller to describe the Databricks table. This usually takes a few seconds after import.'
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
	    case 'ValidationFailed':
	      return 'Databricks rejected the table describe request. The catalog, schema, or table may be wrong, or the token may not have access.'
	    default:
      return ready.value?.message || tbl.message || 'The table is not ready yet.'
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
    table.value = await api.getTable(props.name)
  } catch (e) {
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    loading.value = false
  }
}

async function remove() {
  if (!table.value) return
  const ok = await confirmDialog({
    title: `Delete table "${table.value.name}"?`,
    message: 'App Studio guidance and Databricks MCP tools will no longer be able to inspect this tableRef.',
    confirmLabel: 'Delete',
  })
  if (!ok) return
  try {
    await api.deleteTable(table.value.name)
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
    <button class="link back" type="button" @click="emit('back')">← Tables</button>

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

    <p v-if="error" class="error">{{ error }}</p>
    <p v-else-if="loading && !table" class="muted">Loading…</p>

    <template v-else-if="table">
      <div v-if="hint" class="panel">
        <h3 class="panel-title">Status</h3>
        <p class="muted">{{ hint }}</p>
      </div>

      <div class="panel">
        <h3 class="panel-title">Overview</h3>
        <dl class="props">
          <dt>Connection</dt><dd><code>{{ table.connectionRef }}</code></dd>
          <dt>Warehouse</dt><dd><code>{{ table.warehouseRef }}</code></dd>
          <dt>Catalog</dt><dd><code>{{ table.catalog }}</code></dd>
          <dt>Schema</dt><dd><code>{{ table.schema }}</code></dd>
          <dt>Table</dt><dd><code>{{ table.table }}</code></dd>
          <dt>Columns</dt><dd>{{ table.columns.length }}</dd>
          <dt v-if="table.refreshedAt">Refreshed</dt><dd v-if="table.refreshedAt">{{ table.refreshedAt }}</dd>
          <dt v-if="table.creationTimestamp">Created</dt><dd v-if="table.creationTimestamp">{{ table.creationTimestamp }}</dd>
          <dt v-if="table.observedGeneration !== undefined">Reconciled</dt>
          <dd v-if="table.observedGeneration !== undefined">
            <span v-if="reconciled" class="muted">up to date (generation {{ table.generation }})</span>
            <span v-else class="warning">controller has not caught up (spec {{ table.generation }}, observed {{ table.observedGeneration }})</span>
          </dd>
        </dl>
      </div>

      <div class="panel">
        <div class="panel-head">
          <h3 class="panel-title">Schema</h3>
          <span class="muted">{{ table.columns.length }} columns</span>
        </div>
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
      </div>

      <ConditionsPanel
        :conditions="table.conditions"
        :generation="table.generation"
        :observed-generation="table.observedGeneration"
        empty-text="No conditions yet. Table validation has not reported status for this resource."
      />

      <div class="actions">
        <button class="danger" type="button" @click="remove">Delete table</button>
      </div>
    </template>
  </section>
</template>
