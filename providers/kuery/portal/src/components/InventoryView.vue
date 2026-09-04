<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import type { ObjectResult } from '../api'
import type { FarosContext } from '../element'
import {
  applyInventoryPage, beginInventoryRequest, changeInventoryPager, createInventoryPager,
  isCurrentInventoryRequest, normalizeInventoryFilter,
} from '../inventory-pager'
import { age, edgeName, errorMessage, resourceLabel, useKueryApi } from '../kuery'
import ResourceTable from '../portalkit/ResourceTable.vue'
import type { ResourceTableChange, TableFilterDefinition } from '../portalkit/table'

const props = defineProps<{ context: FarosContext | null; edges: string[] }>()
const emit = defineEmits<{ inspect: [row: ObjectResult] }>()
const context = computed(() => props.context)
const { api } = useKueryApi(context)
const pager = ref(createInventoryPager(50))
const rows = ref<Array<Record<string, unknown>>>([])
const loaded = ref(false)
const loading = ref(false)
const error = ref('')
const warnings = ref<string[]>([])
const kindInput = ref('')
const namespaceInput = ref('')
let controller: AbortController | null = null

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
  loading.value = true
  error.value = ''
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
  } catch (reason) {
    if (!isCurrentInventoryRequest(pager.value, request.id)) return
    const message = errorMessage(reason, 'Retry this page or narrow the exact filters.')
    if (message) error.value = message
  } finally {
    if (controller === currentController && isCurrentInventoryRequest(pager.value, request.id)) loading.value = false
  }
}

function change(change: ResourceTableChange): void {
  pager.value = changeInventoryPager(pager.value, change)
  void load()
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
  <section class="kuery-panel k-card" aria-labelledby="inventory-title">
    <div class="kuery-panel-head"><div><h2 id="inventory-title" class="kuery-panel-title">Fleet inventory</h2><p class="meta">Server-paginated inventory across connected edges. Name, Kind, and Namespace searches are exact and apply to the full fleet query.</p></div></div>
    <form class="kuery-toolbar" aria-label="Filter fleet inventory" @submit.prevent="applyFacetFilters">
      <label>
        <span class="kuery-sr-only">Exact Kind</span>
        <input id="inventory-kind-filter" v-model="kindInput" class="k-input kuery-control" type="text" autocomplete="off" placeholder="Kind (exact, e.g. Deployment)">
      </label>
      <label>
        <span class="kuery-sr-only">Exact Namespace</span>
        <input id="inventory-namespace-filter" v-model="namespaceInput" class="k-input kuery-control" type="text" autocomplete="off" placeholder="Namespace (exact)">
      </label>
      <button class="k-btn k-btn--primary" type="submit">Apply filters</button>
    </form>
    <ResourceTable
      :columns="columns" :rows="rows" row-key="_key" aria-label="Fleet inventory" :loaded="loaded" :loading="loading"
      refresh-mode="background" :error="error" :stale="loaded && !!error" retryable searchable search-placeholder="Exact resource name…"
      :filters="filters" pagination-mode="server" :page="pager.page" :page-size="pager.pageSize" :page-size-options="[25, 50, 100]"
      :query="pager.query" :filter-values="pager.filters" :cursor="pager.cursor" :page-info="pager.pageInfo"
      empty-text="No synced objects. Connect an edge, then retry." combined-filter-empty-text="No objects match the exact search and selected filters."
      :row-aria-label="row => `Inspect ${resourceLabel(row._object as ObjectResult)}`" @change="change" @row-click="inspect" @retry="load"
    >
      <template #edge="{ row }"><code>{{ row.edge }}</code></template>
      <template #name="{ row }"><span class="name">{{ row.name }}</span></template>
    </ResourceTable>
    <div v-if="pager.paginationGap" class="kuery-warning" role="alert">Kuery reports more matching objects, but did not provide a continuation cursor. Narrow the exact filters or retry this page.<span v-if="warnings.length"> {{ warnings.join(' ') }}</span></div>
    <div v-else-if="warnings.length" class="kuery-warning" role="status">{{ warnings.join(' ') }}</div>
  </section>
</template>
