<!-- CANONICAL SOURCE — edit here, then run make sync-portalkit. -->
<script setup lang="ts" generic="T extends { id: string; name: string; model: string; endpoint: string; status: string; configured: boolean; default?: boolean; tone?: 'success' | 'danger' | 'muted' }">
import { computed, ref, watch } from 'vue'
import { Search } from 'lucide-vue-next'
import LayoutSelector from '../portalkit/LayoutSelector.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { readLayoutPreference, writeLayoutPreference } from '../portalkit/layoutPreference'
import { ensureAgentUIStyles } from '../agentkit/styles'

const props = defineProps<{ rows: T[]; preferenceKey: string }>()
ensureAgentUIStyles()
const layout = ref(readLayoutPreference(props.preferenceKey))
const query = ref('')
watch(layout, value => writeLayoutPreference(props.preferenceKey, value))
const filtered = computed(() => {
  const needle = query.value.trim().toLowerCase()
  return props.rows.filter(row => [row.name, row.model, row.endpoint, row.status].some(value => value.toLowerCase().includes(needle)))
})
const tableRows = computed(() => props.rows.map(row => ({ ...row, status: row.configured ? row.status : 'Needs credential' })))
const tableFilters = [{ key: 'model', label: 'Model' }, { key: 'status', label: 'Status' }]
const columns = [
  { key: 'name', label: 'Name', primary: true },
  { key: 'model', label: 'Model' },
  { key: 'endpoint', label: 'Endpoint / runtime' },
  { key: 'status', label: 'Status', fullValue: (row: Record<string, unknown>) => row.configured ? String(row.status) : 'Needs credential' },
  { key: 'actions', label: 'Actions' },
]
</script>

<template>
  <div class="k-model-collection">
    <div class="k-model-collection__toolbar">
      <label v-if="layout === 'grid'" class="k-model-collection__search">
        <Search :size="16" :stroke-width="1.75" aria-hidden="true" />
        <input v-model="query" type="search" class="k-input" aria-label="Search models" placeholder="Search models…" />
      </label>
      <span v-if="layout === 'grid'" class="k-model-collection__count" role="status">{{ filtered.length }}{{ query.trim() ? ` of ${rows.length}` : '' }} model{{ rows.length === 1 ? '' : 's' }}</span>
      <LayoutSelector v-model="layout" aria-label="Model layout" grid-label="Cards" />
    </div>
    <template v-if="layout === 'grid'">
      <div v-if="filtered.length" class="k-model-grid"><slot name="cards" :rows="filtered" /></div>
      <p v-else class="k-model-collection__empty">No models match your search.</p>
    </template>
    <ResourceTable v-else v-model:query="query" :rows="tableRows" searchable search-placeholder="Search models…" :search-keys="['name', 'model', 'endpoint', 'status']" :filters="tableFilters" search-empty-text="No models match your search." :columns="columns" row-key="id" aria-label="Models" :loaded="true" empty-text="No models match your search.">
      <template #name="{ row }">{{ row.name }} <StatusBadge v-if="row.default" status="Default" tone="muted" /></template>
      <template #status="{ row }">
        <StatusBadge :status="row.configured ? String(row.status) : 'Needs credential'" :tone="row.configured ? (row as T).tone || 'muted' : 'warning'" />
        <slot name="details" :row="row as T" />
      </template>
      <template #actions="{ row }"><slot name="actions" :row="row as T" /></template>
    </ResourceTable>
  </div>
</template>
