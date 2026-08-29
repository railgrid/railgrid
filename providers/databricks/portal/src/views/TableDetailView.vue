<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Activity, Ellipsis, RefreshCw, Table2, Warehouse } from 'lucide-vue-next'
import ResourceTable from '../portalkit/ResourceTable.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { api } from '../api'
import ConditionsPanel from '../portalkit/ConditionsPanel.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourceStatCards, { type ResourceStatCard } from '../portalkit/ResourceStatCards.vue'
import { confirmDialog } from '../portalkit/confirm'
import type { ErrorResponse, Table, TableColumn } from '../types'
import {
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  createOperationLocks,
  FAST_REFRESH_MS,
  operationKey,
  STABLE_REFRESH_MS,
  type AdaptiveRefreshTimer,
  type LatestRefreshController,
  type ResourceRefreshMode,
} from '../refresh'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ (e: 'back'): void }>()

const table = ref<Table | null>(null)
const loading = ref(false)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const loaded = ref(false)
const error = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const deleting = ref(false)
const schemaCache = ref<{ uid?: string; generation?: number; refreshedAt?: string; columns: TableColumn[] } | null>(null)
const actionsMenu = ref<HTMLDetailsElement | null>(null)
let poll!: AdaptiveRefreshTimer
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

type StatTone = 'default' | 'success' | 'warning' | 'danger'

function statTone(status: string | undefined): StatTone {
  if (!status) return 'default'
  if (status === 'Ready') return 'success'
  if (/fail|error|attention|unavailable/i.test(status)) return 'danger'
  return 'warning'
}

const readState = computed<boolean | null>(() => {
  if (loaded.value) return true
  if (error.value) return false
  return loading.value ? false : null
})

const statCards = computed<ResourceStatCard[]>(() => [
  {
    id: 'status',
    label: 'Status',
    value: deleting.value ? 'Deleting' : table.value?.status || '—',
    detail: deleting.value ? 'Deletion in progress' : hint.value || undefined,
    icon: Activity,
    tone: statTone(deleting.value ? 'Deleting' : table.value?.status),
  },
  {
    id: 'columns',
    label: 'Columns',
    value: table.value?.columns.length ?? '—',
    detail: schemaTruncated.value ? 'Schema cache truncated' : undefined,
    icon: Table2,
  },
  {
    id: 'warehouse',
    label: 'Warehouse',
    value: table.value?.warehouseRef || '—',
    icon: Warehouse,
    mono: true,
  },
])

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

function requestRefresh(mode: ResourceRefreshMode): void {
  if (mode === 'foreground') {
    refreshMode.value = 'foreground'
    loading.value = true
  }
  refresh.request(mode)
}

function load(): void {
  requestRefresh('foreground')
}

function pollCadence(): number {
  const stable = loaded.value && !error.value && !deleting.value && !!table.value &&
    table.value.status === 'Ready' && operationPhase(table.value.name) !== 'deleting'
  return stable ? STABLE_REFRESH_MS : FAST_REFRESH_MS
}

poll = createAdaptiveRefreshTimer(() => requestRefresh('background'), pollCadence)

function operationLocked(name: string): boolean {
  return operations.isLocked(operationKey('table', name))
}

function operationPhase(name: string) {
  return operations.phase(operationKey('table', name))
}

function goBack() {
  if (deleting.value || (table.value && operationLocked(table.value.name))) return
  emit('back')
}

async function remove() {
  const current = table.value
  if (!current || deleting.value) return
  const ok = await confirmDialog({
    title: `Delete table "${current.name}"?`,
    message: 'App Studio guidance and Databricks MCP tools will no longer be able to inspect this tableRef.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  const lock = operationKey('table', current.name)
  if (!operations.acquire(lock, 'deleting')) {
    mutationError.value = `Table "${current.name}" already has an operation in progress.`
    return
  }
  deleting.value = true
  mutationError.value = null
  try {
    await api.deleteTable(current.name)
    operations.tombstone(lock, current.uid)
    emit('back')
  } catch (e) {
    mutationError.value = errMessage(e)
  } finally {
    deleting.value = false
    operations.release(lock)
  }
}

function deleteFromMenu() {
  actionsMenu.value?.removeAttribute('open')
  void remove()
}

refresh = createLatestRefreshController(async (requestID, mode) => {
  refreshMode.value = mode
  if (mode === 'foreground') loading.value = true
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
    if (refresh.isCurrent(requestID)) {
      if (mode === 'foreground') loading.value = false
      poll.schedule()
    }
  }
})

watch(() => props.name, () => {
  table.value = null
  schemaCache.value = null
  loaded.value = false
  deleting.value = false
  error.value = null
  mutationError.value = null
  refresh.invalidate()
  load()
})

onMounted(() => {
  load()
})
onUnmounted(() => {
  poll.stop()
  refresh.stop()
})
</script>

<template>
  <section class="databricks-resource-detail">
    <ResourceBackLink
      href="/ui/providers/databricks/tables"
      :disabled="deleting || (!!table && operationLocked(table.name))"
      @back="goBack"
    >
      Tables
    </ResourceBackLink>

    <ResourcePage
      :title="table?.name || name"
      kind="Table"
      :loaded="readState"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="loaded && !!error"
      retryable
      @retry="load"
    >
      <template #meta>
        <span>Databricks</span>
      </template>
      <template v-if="table" #status><StatusBadge :status="deleting ? 'Deleting' : table.status" :tone="deleting ? 'warning' : null" :title="table.message" /></template>
      <template #actions>
        <div class="databricks-resource-actions" role="group" aria-label="Table actions">
          <button class="k-btn k-btn--ghost icon-text" type="button" :disabled="loading || deleting || (!!table && operationLocked(table.name))" :aria-busy="loading || undefined" @click="load">
            <RefreshCw :size="14" :class="{ spin: loading }" aria-hidden="true" />
            {{ loading ? 'Refreshing…' : 'Refresh' }}
          </button>
          <details ref="actionsMenu" class="databricks-resource-menu">
            <summary class="k-btn k-btn--ghost" aria-label="More table actions">
              <Ellipsis :size="16" aria-hidden="true" />
              <span class="sr-only">More actions</span>
            </summary>
            <div class="databricks-resource-menu-popover">
              <button type="button" class="databricks-resource-menu-item" :disabled="!table || loading || deleting || operationLocked(table?.name || name)" @click="deleteFromMenu">
                {{ operationPhase(table?.name || name) === 'deleting' ? 'Deleting table…' : 'Delete table' }}
              </button>
            </div>
          </details>
        </div>
      </template>
      <template #summary><ResourceStatCards :cards="statCards" density="compact" aria-label="Table summary" /></template>
      <template #body>
        <p v-if="deleting" class="warning deletion-progress" role="status" aria-live="polite">Deleting this table. The last successful snapshot remains visible until the hub confirms removal.</p>
        <p v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
          <span>{{ mutationError }}</span>
          <button class="k-btn k-btn--ghost" type="button" @click="mutationError = null">Dismiss</button>
        </p>

        <div v-if="table" class="databricks-resource-sections">
          <ResourceSectionCard id="table-overview" eyebrow="Table" title="Overview" description="References, fully qualified name, timestamps, and reconciliation details.">
            <dl class="props">
          <dt>Connection</dt><dd><code>{{ table.connectionRef }}</code></dd>
          <dt>Warehouse</dt><dd><code>{{ table.warehouseRef }}</code></dd>
          <dt>Catalog</dt><dd><code>{{ table.catalog }}</code></dd>
          <dt>Schema</dt><dd><code>{{ table.schema }}</code></dd>
          <dt>Table</dt><dd><code>{{ table.table }}</code></dd>
          <dt>Full name</dt><dd><code>{{ table.fullName }}</code></dd>
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
          </ResourceSectionCard>

          <ResourceSectionCard id="table-schema" eyebrow="Schema" title="Columns" description="The latest bounded schema snapshot reported by Databricks." >
            <template #actions><span class="muted">{{ schemaTruncated ? `${table.columns.length} cached columns` : `${table.columns.length} columns` }}</span></template>
            <p v-if="schemaTruncated" class="warning" role="status">{{ schemaNotice }}</p>
            <ResourceTable
          :columns="[
            { key: 'name', label: 'Column', primary: true },
            { key: 'type', label: 'Type' },
            { key: 'nullableLabel', label: 'Nullable' },
            { key: 'comment', label: 'Comment' },
          ]"
          :rows="schemaRows"
          aria-label="Table schema columns"
          searchable
          search-placeholder="Search columns…"
          :filters="[{ key: 'type', label: 'Type' }, { key: 'nullableLabel', label: 'Nullable' }]"
          paginated
          :page-size="25"
          row-key="name"
          :loaded="schemaLoaded"
          :loading="schemaPending"
          :refresh-mode="refreshMode"
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
          </ResourceSectionCard>

          <ResourceSectionCard id="table-conditions" eyebrow="Diagnostics" title="Conditions" description="Controller conditions and observed generation for this table.">
            <ConditionsPanel
              :conditions="table.conditions"
              :generation="table.generation"
              :observed-generation="table.observedGeneration"
              empty-text="No conditions yet. Table validation has not reported status for this resource."
            />
          </ResourceSectionCard>
        </div>
      </template>
    </ResourcePage>
  </section>
</template>
