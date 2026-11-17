<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowRight, Search, X } from 'lucide-vue-next'
import TemplateCard from '../components/TemplateCard.vue'
import { api, isContextChangedError } from '../api'
import LayoutSelector from '../portalkit/LayoutSelector.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableFilter from '../portalkit/ResourceTableFilter.vue'
import InlineNotification from '../portalkit/InlineNotification.vue'
import { readLayoutPreference, writeLayoutPreference, type LayoutMode } from '../portalkit/layoutPreference'
import { portalHref } from '../portalkit/navigation'
import { useDelayedLoading } from '../portalkit/useDelayedLoading'
import { filterTableRows } from '../portalkit/table'
import type { Template } from '../types'

const emit = defineEmits<{
  (e: 'select', name: string): void
  (e: 'navigate', view: string): void
}>()

const loading = ref(true)
const loaded = ref(false)
const error = ref<string | null>(null)
const initialReadPending = computed(() => !loaded.value && loading.value && !error.value)
const showInitialLoading = useDelayedLoading(initialReadPending)
const heading = ref<HTMLElement | null>(null)
const gridWarning = ref<HTMLElement | null>(null)
const templates = ref<Template[]>([])
const category = ref('')
const cloud = ref('')
const query = ref('')
const gridSearch = ref<HTMLInputElement | null>(null)
const page = ref<HTMLElement | null>(null)
const searchKeys = ['identity', 'name', 'description', 'category', 'kind']
const layoutPreferenceKey = 'railgrid:portal:infrastructure:templates-layout'
const layout = ref<LayoutMode>(readLayoutPreference(layoutPreferenceKey))
let requestSerial = 0

const tableColumns = [
  { key: 'identity', label: 'Template', primary: true },
  { key: 'category', label: 'Category' },
  { key: 'kind', label: 'Kind' },
  { key: 'version', label: 'Version' },
  { key: 'exposure', label: 'Exposure' },
]

const categoryOptions = computed(() => filterOptions(templates.value.map(t => t.category || 'Other')))
const cloudOptions = computed(() => filterOptions(templates.value.flatMap(t => t.cloud ? [t.cloud] : [])))

function filterOptions(values: string[]) {
  return Array.from(new Set(values)).sort().map(value => ({ value, label: value }))
}

const filtered = computed(() => templates.value.filter(t => {
  if (category.value && (t.category || 'Other') !== category.value) return false
  if (cloud.value && t.cloud !== cloud.value) return false
  return true
}))

const tableRows = computed<Array<Record<string, unknown>>>(() => filtered.value.map(template => ({
  identity: template.displayName || template.name,
  name: template.name,
  description: template.description,
  category: template.category || 'Other',
  kind: template.kind || '—',
  version: template.version ? `v${template.version}` : '—',
  exposure: exposureLabel(template.exposure),
})))
const gridTemplates = computed(() => {
  const names = new Set(filterTableRows(tableRows.value, query.value, searchKeys, {}).map(row => row.name))
  return filtered.value.filter(template => names.has(template.name))
})

watch(layout, mode => writeLayoutPreference(layoutPreferenceKey, mode))

async function load() {
  if (loading.value && requestSerial > 0) return
  const serial = ++requestSerial
  loading.value = true
  try {
    const r = await api.listTemplates()
    if (serial !== requestSerial) return
    const restoreFocus = !!gridWarning.value?.contains(document.activeElement)
    templates.value = r.items || []
    loaded.value = true
    error.value = null
    if (restoreFocus) {
      await nextTick()
      if (document.activeElement === document.body) heading.value?.focus()
    }
  } catch (e: unknown) {
    if (serial !== requestSerial || isContextChangedError(e)) return
    error.value = (e as { message?: string }).message ?? 'failed to load templates'
  } finally {
    if (serial === requestSerial) loading.value = false
  }
}
onMounted(load)
onUnmounted(() => { requestSerial += 1 })

async function clearFilters() {
  category.value = ''
  cloud.value = ''
  query.value = ''
  await nextTick()
  ;(gridSearch.value ?? page.value?.querySelector<HTMLInputElement>('input[type="search"]'))?.focus()
}

function clearSearch() {
  query.value = ''
  gridSearch.value?.focus()
}

function exposureLabel(exposure: Template['exposure']): string {
  const value = exposure || 'internal'
  return value.charAt(0).toUpperCase() + value.slice(1)
}

function selectTemplateRow(row: Record<string, unknown>) {
  if (typeof row.name === 'string') emit('select', row.name)
}

function templateHref(name: string): string {
  return portalHref(`/ui/providers/infrastructure/templates/${encodeURIComponent(name)}`)
}

function followTemplateLink(event: MouseEvent, row: Record<string, unknown>): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  selectTemplateRow(row)
}

function followInstancesLink(event: MouseEvent): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  emit('navigate', 'instances')
}
</script>

<template>
  <section ref="page" class="page" :aria-busy="loading">
    <header class="page-head">
      <div>
        <h2 ref="heading" class="page-title" tabindex="-1">Templates</h2>
        <p class="page-meta">Choose a template to provision resources in this workspace.</p>
      </div>
      <a class="k-btn k-btn--ghost" :href="portalHref('/ui/providers/infrastructure/instances')" @click="followInstancesLink">My instances <ArrowRight :size="14" aria-hidden="true" /></a>
    </header>

    <div class="filters">
      <div v-if="layout === 'grid'" class="k-table__search" role="search">
        <Search class="k-table__search-icon" :stroke-width="1.75" aria-hidden="true" />
        <input ref="gridSearch" v-model="query" class="k-table__search-input" type="search" aria-label="Search Infrastructure templates" placeholder="Search templates…" autocomplete="off">
        <button v-if="query" class="k-table__search-clear" type="button" aria-label="Clear search" @click="clearSearch"><X :stroke-width="1.75" aria-hidden="true" /></button>
      </div>
      <ResourceTableFilter
        v-if="categoryOptions.length > 1"
        v-model="category"
        class="catalog-filter"
        :definition="{ key: 'category', label: 'Category' }"
        :options="categoryOptions"
      />
      <ResourceTableFilter
        v-if="cloudOptions.length > 0"
        v-model="cloud"
        class="catalog-filter"
        :definition="{ key: 'cloud', label: 'Cloud' }"
        :options="cloudOptions"
      />
      <LayoutSelector v-model="layout" aria-label="Template layout" />
    </div>

    <template v-if="layout === 'grid'">
      <span v-if="loaded && loading" class="sr-only" role="status" aria-live="polite">Updating template catalog…</span>
      <div v-if="loaded && error" ref="gridWarning">
        <InlineNotification tone="error" :message="`Showing the last successful result. ${error}`" action-label="Retry" action-busy-label="Retrying…" :action-busy="loading" @action="load" />
      </div>
      <div
        v-if="initialReadPending"
        class="catalog-loading-grid k-delayed-loading"
        :role="showInitialLoading ? 'status' : undefined"
        :aria-live="showInitialLoading ? 'polite' : undefined"
        :aria-busy="showInitialLoading ? 'true' : undefined"
        :aria-label="showInitialLoading ? 'Loading templates' : undefined"
        :aria-hidden="showInitialLoading ? undefined : 'true'"
      >
        <div v-for="i in 6" :key="i" class="catalog-loading-card k-card" aria-hidden="true">
          <div class="shimmer page-loading-line page-loading-line-short" />
          <div class="shimmer page-loading-line" />
          <div class="shimmer page-loading-line page-loading-line-mid" />
        </div>
      </div>
      <div v-else-if="!loaded && error" ref="gridWarning">
        <InlineNotification tone="error" :message="error" action-label="Retry" action-busy-label="Retrying…" :action-busy="loading" @action="load" />
      </div>
      <div v-else-if="templates.length === 0" class="empty-state" role="status">
        <span>No infrastructure templates are available in this workspace.</span>
        <button v-if="!error" type="button" class="k-btn k-btn--ghost" @click="load">Refresh catalog</button>
      </div>
      <div v-else-if="gridTemplates.length === 0" class="empty-state" role="status">
        <span>No templates match the current search and filters.</span>
        <button type="button" class="k-btn k-btn--ghost" @click="clearFilters">{{ query ? 'Clear search and filters' : 'Clear filters' }}</button>
      </div>
      <div v-else class="grid">
        <TemplateCard
          v-for="t in gridTemplates"
          :key="t.name"
          :template="t"
          :href="templateHref(t.name)"
          @select="emit('select', $event)"
        />
      </div>
    </template>

    <template v-else>
      <ResourceTable
        v-model:query="query"
        :columns="tableColumns"
        :rows="tableRows"
        aria-label="Infrastructure templates"
        searchable
        search-placeholder="Search templates…"
        :search-keys="searchKeys"
        paginated
        row-key="name"
        :loaded="loaded"
        :loading="loading"
        :error="error"
        :stale="loaded && !!error"
        retryable
        :inline-empty="!!category || !!cloud"
        :empty-text="templates.length === 0 ? 'No infrastructure templates are available in this workspace.' : 'No templates match the current filters.'"
        interactive
        :row-aria-label="(row) => `Provision template ${String(row.identity)}`"
        @retry="load"
        @row-click="selectTemplateRow"
      >
        <template #identity="{ value, row }">
          <a class="k-btn k-btn--ghost k-table-resource-link text-left" :href="templateHref(String(row.name))" @click.stop="followTemplateLink($event, row)">
            {{ value }}
          </a>
        </template>
        <template #kind="{ value }"><span class="cell-mono">{{ value }}</span></template>
        <template #version="{ value }"><span class="cell-mono">{{ value }}</span></template>
        <template #exposure="{ value }"><span class="k-badge k-badge--muted k-badge--label">{{ value }}</span></template>
      </ResourceTable>
      <div v-if="loaded && !error && templates.length === 0" class="empty-followup">
        <button type="button" class="k-btn k-btn--ghost" @click="load">Refresh catalog</button>
      </div>
      <div v-else-if="loaded && gridTemplates.length === 0 && (category || cloud)" class="empty-followup">
        <button type="button" class="k-btn k-btn--ghost" @click="clearFilters">{{ query ? 'Clear search and filters' : 'Clear filters' }}</button>
      </div>
    </template>
  </section>
</template>
