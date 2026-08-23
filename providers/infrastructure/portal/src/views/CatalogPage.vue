<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowRight } from 'lucide-vue-next'
import TemplateCard from '../components/TemplateCard.vue'
import { api, isContextChangedError } from '../api'
import LayoutSelector from '../portalkit/LayoutSelector.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import { readLayoutPreference, writeLayoutPreference, type LayoutMode } from '../portalkit/layoutPreference'
import type { Template } from '../types'

const emit = defineEmits<{
  (e: 'select', name: string): void
  (e: 'navigate', view: string): void
}>()

const loading = ref(true)
const loaded = ref(false)
const error = ref<string | null>(null)
const templates = ref<Template[]>([])
const category = ref('')
const cloud = ref('')
const layoutPreferenceKey = 'faros:portal:infrastructure:templates-layout'
const layout = ref<LayoutMode>(readLayoutPreference(layoutPreferenceKey))
let requestSerial = 0

const tableColumns = [
  { key: 'identity', label: 'Template' },
  { key: 'category', label: 'Category' },
  { key: 'kind', label: 'Kind' },
  { key: 'version', label: 'Version' },
  { key: 'exposure', label: 'Exposure' },
]

const categories = computed(() => uniq(templates.value.map(t => t.category || 'Other')))
const clouds = computed(() => uniq(templates.value.flatMap(t => t.cloud ? [t.cloud] : [])))

function uniq(xs: string[]): string[] {
  return Array.from(new Set(xs)).sort()
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

watch(layout, mode => writeLayoutPreference(layoutPreferenceKey, mode))

async function load() {
  const serial = ++requestSerial
  loading.value = true
  error.value = null
  try {
    const r = await api.listTemplates()
    if (serial !== requestSerial) return
    templates.value = r.items || []
    loaded.value = true
  } catch (e: unknown) {
    if (serial !== requestSerial || isContextChangedError(e)) return
    error.value = (e as { message?: string }).message ?? 'failed to load templates'
  } finally {
    if (serial === requestSerial) loading.value = false
  }
}
onMounted(load)
onUnmounted(() => { requestSerial += 1 })

function clearFilters() {
  category.value = ''
  cloud.value = ''
}

function exposureLabel(exposure: Template['exposure']): string {
  const value = exposure || 'internal'
  return value.charAt(0).toUpperCase() + value.slice(1)
}

function selectTemplateRow(row: Record<string, unknown>) {
  if (typeof row.name === 'string') emit('select', row.name)
}
</script>

<template>
  <section class="page" :aria-busy="loading">
    <header class="page-head">
      <div>
        <h2 class="page-title">Templates</h2>
        <p class="page-meta">Pick a template to provision into your tenant scope.</p>
      </div>
      <button type="button" class="k-btn k-btn--ghost" @click="emit('navigate', 'instances')">My instances <ArrowRight :size="14" aria-hidden="true" /></button>
    </header>

    <div class="filters">
      <select v-if="categories.length > 1" v-model="category" class="k-input" aria-label="Filter templates by category">
        <option value="">All categories</option>
        <option v-for="c in categories" :key="c" :value="c">{{ c }}</option>
      </select>
      <select v-if="clouds.length > 0" v-model="cloud" class="k-input" aria-label="Filter templates by cloud">
        <option value="">All clouds</option>
        <option v-for="c in clouds" :key="c" :value="c">{{ c }}</option>
      </select>
      <LayoutSelector v-model="layout" aria-label="Template layout" />
    </div>

    <template v-if="layout === 'grid'">
      <span v-if="loaded && loading" class="sr-only" role="status" aria-live="polite">Updating template catalog…</span>
      <div v-if="loaded && error" class="stale-banner" role="alert" aria-live="assertive">
        <span>Showing the last successful result. {{ error }}</span>
        <button type="button" class="k-btn k-btn--ghost" @click="load">Retry</button>
      </div>
      <div v-if="!loaded && loading" class="catalog-loading-grid" role="status" aria-live="polite" aria-busy="true" aria-label="Loading templates">
        <div v-for="i in 6" :key="i" class="catalog-loading-card k-card" aria-hidden="true">
          <div class="shimmer page-loading-line page-loading-line-short" />
          <div class="shimmer page-loading-line" />
          <div class="shimmer page-loading-line page-loading-line-mid" />
        </div>
      </div>
      <div v-else-if="!loaded && error" class="read-error" role="alert" aria-live="assertive">
        <span>{{ error }}</span>
        <button type="button" class="k-btn k-btn--ghost" @click="load">Retry</button>
      </div>
      <div v-else-if="templates.length === 0" class="empty-state" role="status">
        <span>No infrastructure templates are available in this workspace.</span>
        <button type="button" class="k-btn k-btn--ghost" @click="load">Refresh catalog</button>
      </div>
      <div v-else-if="filtered.length === 0" class="empty-state" role="status">
        <span>No templates match the current filters.</span>
        <button type="button" class="k-btn k-btn--ghost" @click="clearFilters">Clear filters</button>
      </div>
      <div v-else class="grid">
        <TemplateCard
          v-for="t in filtered"
          :key="t.name"
          :template="t"
          @select="emit('select', $event)"
        />
      </div>
    </template>

    <template v-else>
      <ResourceTable
        :columns="tableColumns"
        :rows="tableRows"
        row-key="name"
        :loaded="loaded"
        :loading="loading"
        :error="error"
        :stale="loaded && !!error"
        retryable
        :empty-text="templates.length === 0 ? 'No infrastructure templates are available in this workspace.' : 'No templates match the current filters.'"
        interactive
        :row-aria-label="(row) => `Provision template ${String(row.identity)}`"
        @retry="load"
        @row-click="selectTemplateRow"
      >
        <template #identity="{ value, row }">
          <div class="template-card-title">{{ value }}</div>
          <div class="cell-mono">{{ row.name }}</div>
          <p class="template-card-desc">{{ row.description }}</p>
        </template>
        <template #kind="{ value }"><span class="cell-mono">{{ value }}</span></template>
        <template #version="{ value }"><span class="cell-mono">{{ value }}</span></template>
        <template #exposure="{ value }"><span class="k-badge k-badge--muted">{{ value }}</span></template>
      </ResourceTable>
      <div v-if="loaded && templates.length === 0" class="empty-followup">
        <button type="button" class="k-btn k-btn--ghost" @click="load">Refresh catalog</button>
      </div>
      <div v-else-if="loaded && filtered.length === 0" class="empty-followup">
        <button type="button" class="k-btn k-btn--ghost" @click="clearFilters">Clear filters</button>
      </div>
    </template>
  </section>
</template>
