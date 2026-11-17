<script setup lang="ts">
import InlineNotification from '../portalkit/InlineNotification.vue'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ChevronDown } from 'lucide-vue-next'

import type { ObjectResult } from '../api'
import type { RailgridContext } from '../element'
import {
  applyInventoryPage, beginInventoryRequest, changeInventoryPager, createInventoryPager,
  isCurrentInventoryRequest, normalizeInventoryFilter,
} from '../inventory-pager'
import { age, edgeName, isNoEngagedEdgesError, resourceLabel, safeHTTPStatus, useKueryApi } from '../kuery'
import ResourceTable from '../portalkit/ResourceTable.vue'
import type { ResourceTableChange, TableFilterDefinition } from '../portalkit/table'

const props = defineProps<{ context: RailgridContext | null; edges: string[]; savedView: string }>()
const emit = defineEmits<{ inspect: [row: ObjectResult] }>()
const context = computed(() => props.context)
const { api } = useKueryApi(context, computed(() => props.savedView))
const pager = ref(createInventoryPager(50))
const rows = ref<Array<Record<string, unknown>>>([])
const loaded = ref(false)
const snapshotKey = ref('')
const loading = ref(false)
const error = ref('')
const errorStatus = ref<number | null>(null)
const notEngaged = ref(false)
const warnings = ref<string[]>([])
const kindInput = ref('')
const namespaceInput = ref('')
const panel = ref<HTMLElement | null>(null)
let controller: AbortController | null = null

function queryKey(page: number, request: ReturnType<typeof beginInventoryRequest>['request']): string {
  return JSON.stringify([page, request.pageSize, request.cursor, request.filters.edge || '', request.filters.kind || '', request.filters.namespace || '', request.filters.name || ''])
}
const currentSnapshot = computed(() => loaded.value && snapshotKey.value === queryKey(pager.value.page, beginInventoryRequest(pager.value).request))

const columns = [
  { key: 'edge', label: 'Edge' }, { key: 'kind', label: 'Kind' },
  { key: 'namespace', label: 'Namespace' }, { key: 'name', label: 'Name', primary: true },
  { key: 'age', label: 'Age', align: 'end' as const },
]
const filters = computed<TableFilterDefinition[]>(() => [
  { key: 'edge', label: 'Edge', options: props.edges.map(value => ({ value, label: value })) },
])

function applyFacetFilters(): void {
  const filters = {
    ...pager.value.filters,
    kind: normalizeInventoryFilter(kindInput.value),
    namespace: normalizeInventoryFilter(namespaceInput.value),
  }
  pager.value = changeInventoryPager(pager.value, {
    reason: 'filter', page: 1, pageSize: pager.value.pageSize, query: pager.value.query, filters, cursor: null,
  })
  error.value = ''; errorStatus.value = null; notEngaged.value = false
  void load()
}

async function load(): Promise<void> {
  if (!api.value) return
  controller?.abort()
  const currentController = new AbortController()
  controller = currentController
  const begun = beginInventoryRequest(pager.value)
  pager.value = begun.state
  const request = begun.request
  const requestKey = queryKey(pager.value.page, request)
  loading.value = true
  try {
    const page = await api.value.inventoryPage({ pageSize: request.pageSize, cursor: request.cursor, count: true, filters: request.filters }, { signal: currentController.signal })
    if (!isCurrentInventoryRequest(pager.value, request.id)) return
    pager.value = applyInventoryPage(pager.value, request.id, page)
    rows.value = page.objects.map((object, index) => ({
      _object: object,
      _key: object.id || `${object.cluster || ''}:${object.object?.apiVersion || ''}:${object.object?.kind || ''}:${object.object?.metadata?.namespace || ''}:${object.object?.metadata?.name || index}`,
      edge: edgeName(object.cluster), kind: object.object?.kind || '—', namespace: object.object?.metadata?.namespace || '—',
      name: object.object?.metadata?.name || '—', age: age(object.object?.metadata?.creationTimestamp),
    }))
    warnings.value = page.warnings
    loaded.value = true
    snapshotKey.value = requestKey
    error.value = ''; errorStatus.value = null; notEngaged.value = false
  } catch (reason) {
    if (!isCurrentInventoryRequest(pager.value, request.id)) return
    if (isNoEngagedEdgesError(reason)) {
      error.value = ''; errorStatus.value = null
      notEngaged.value = true
      return
    }
    if (reason instanceof DOMException && reason.name === 'AbortError') return
    error.value = 'Could not load fleet inventory. Retry or adjust the exact filters.'
    errorStatus.value = safeHTTPStatus(reason)
    notEngaged.value = false
  } finally {
    if (controller === currentController && isCurrentInventoryRequest(pager.value, request.id)) loading.value = false
  }
}

function change(change: ResourceTableChange): void {
  pager.value = changeInventoryPager(pager.value, change)
  error.value = ''; errorStatus.value = null; notEngaged.value = false
  void load()
}

async function retrySync(): Promise<void> {
  if (loading.value) return
  const focused = typeof document === 'undefined' ? null : document.activeElement
  await load()
  await nextTick()
  if (focused && !focused.isConnected && document.activeElement === document.body) {
    ;(panel.value?.querySelector<HTMLElement>('.k-table__retry') ?? panel.value?.querySelector<HTMLElement>('#inventory-title'))?.focus()
  }
}

function inspect(row: Record<string, unknown>): void {
  const object = row._object as ObjectResult | undefined
  if (object) emit('inspect', object)
}

onMounted(() => void load())
watch(api, (ready, wasReady) => { if (ready && !wasReady) void load() })
watch(
  () => [pager.value.filters.kind || '', pager.value.filters.namespace || ''] as const,
  ([kind, namespace]) => { kindInput.value = kind; namespaceInput.value = namespace },
  { immediate: true },
)
onBeforeUnmount(() => controller?.abort())
</script>

<template>
  <section ref="panel" class="kuery-panel k-card" aria-labelledby="inventory-title">
    <div class="kuery-panel-head"><div><h2 id="inventory-title" class="kuery-panel-title" tabindex="-1">Fleet inventory</h2><p class="meta">Server-paginated inventory across connected edges. Name, Kind, and Namespace searches are exact and apply to the full fleet query.</p></div></div>
    <div v-if="notEngaged && !loaded" class="kuery-read-state" role="status">
      <div class="kuery-error-copy">
        <span>Your Kubernetes edge is connected, but Kuery has not started syncing it yet.</span>
        <span class="meta">This can take a moment. If it continues, ask a workspace admin to confirm Kuery and Edges are enabled here.</span>
      </div>
      <button type="button" class="k-btn k-btn--ghost" :aria-disabled="loading || undefined" :aria-busy="loading || undefined" @click="retrySync">{{ loading ? 'Checking…' : 'Check again' }}</button>
    </div>
    <template v-else>
      <InlineNotification v-if="notEngaged" tone="warning" action-label="Check again" action-busy-label="Checking…" :action-busy="loading" @action="retrySync" :message="currentSnapshot ? 'The last inventory result is still shown, but Kuery is not currently syncing an edge in this workspace.' : 'Kuery is not currently syncing an edge in this workspace. Results for this query are unavailable.'" />
      <form class="kuery-toolbar kuery-inventory-filters" aria-label="Filter fleet inventory" @submit.prevent="applyFacetFilters">
        <label>
          <span>Kind</span>
          <input id="inventory-kind-filter" v-model="kindInput" class="k-input kuery-control" type="text" autocomplete="off" placeholder="Kind (exact, e.g. Deployment)">
        </label>
        <label>
          <span>Namespace</span>
          <input id="inventory-namespace-filter" v-model="namespaceInput" class="k-input kuery-control" type="text" autocomplete="off" placeholder="Namespace (exact)">
        </label>
        <button class="k-btn k-btn--primary" type="submit">Apply filters</button>
      </form>
      <ResourceTable
        class="kuery-inventory-table"
        :columns="columns" :rows="currentSnapshot ? rows : []" row-key="_key" aria-label="Fleet inventory" :loaded="loaded" :loading="loading"
        :results-unavailable="loaded && !currentSnapshot" refresh-mode="foreground" :error="error" :stale="currentSnapshot && (!!error || notEngaged)" retryable searchable search-placeholder="Exact resource name…"
        :filters="filters" pagination-mode="server" :page="pager.page" :page-size="pager.pageSize" :page-size-options="[25, 50, 100]"
        :query="pager.query" :filter-values="pager.filters" :cursor="pager.cursor" :page-info="pager.pageInfo"
        empty-text="No synced objects. Connect an edge, then retry." combined-filter-empty-text="No objects match the exact search and selected filters."
        :row-aria-label="row => `Inspect ${resourceLabel(row._object as ObjectResult)}`" @change="change" @row-click="inspect" @retry="load"
      >
        <template #edge="{ row }"><code>{{ row.edge }}</code></template>
        <template #name="{ row }"><span class="name">{{ row.name }}</span></template>
      </ResourceTable>
      <details v-if="error && errorStatus !== null" class="k-resource-technical">
        <summary class="k-resource-technical__summary"><span class="k-resource-technical__summary-label">Technical details</span><ChevronDown class="k-resource-technical__chevron" :size="14" aria-hidden="true" /></summary>
        <div class="k-resource-technical__body"><div class="k-resource-technical__content">Inventory request returned HTTP {{ errorStatus }}.</div></div>
      </details>
      <InlineNotification v-if="currentSnapshot && pager.paginationGap" tone="warning" announce="assertive">Kuery reports more matching objects, but did not provide a continuation cursor. Narrow the exact filters or retry this page.{{ warnings.length ? ` ${warnings.join(' ')}` : '' }}</InlineNotification>
      <InlineNotification v-else-if="currentSnapshot && warnings.length" tone="warning" :message="warnings.join(' ')" />
    </template>
  </section>
</template>
