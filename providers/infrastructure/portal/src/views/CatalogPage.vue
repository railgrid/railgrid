<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowRight, Cloud, ExternalLink, Search, X } from 'lucide-vue-next'
import { api, isContextChangedError } from '../api'
import DeploymentTarget from '../components/DeploymentTarget.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import { portalHref } from '../portalkit/navigation'
import { sortTableRows, type TableSort } from '../portalkit/table'
import type { Template } from '../types'

const props = defineProps<{ userId?: string }>()

const emit = defineEmits<{
  (e: 'select', name: string): void
  (e: 'navigate', view: string): void
}>()

const loading = ref(true)
const loaded = ref(false)
const error = ref<string | null>(null)
const templates = ref<Template[]>([])
const query = ref('')
const sort = ref<TableSort | null>({ key: 'category', direction: 'asc' })
const search = ref<HTMLInputElement | null>(null)
const catalog = ref<HTMLElement | null>(null)
const narrow = ref(false)
let catalogObserver: ResizeObserver | undefined
const emptyText = 'No infrastructure templates are available in this workspace.'
const searchKeys = ['identity', 'name', 'description', 'category', 'kind', 'destination']
const cloudGuideURL = 'https://railgrid.ai/docs/use/infrastructure/cloud-provisioning/'
const cloudGuideDismissed = ref(false)
const cloudGuidePreference = computed(() => props.userId
  ? `railgrid:portal:infrastructure:cloud-guide-dismissed:${props.userId}`
  : null)
let requestSerial = 0

watch(cloudGuidePreference, key => {
  try {
    cloudGuideDismissed.value = key !== null && window.localStorage.getItem(key) === '1'
  } catch {
    cloudGuideDismissed.value = false
  }
}, { immediate: true })

const tableColumns = [
  { key: 'identity', label: 'Template', primary: true, sortable: true },
  { key: 'category', label: 'Category', sortable: true },
  { key: 'version', label: 'Version' },
  { key: 'exposure', label: 'Exposure' },
  { key: 'destination', label: 'Deploys\u00a0to', ariaLabel: 'Deploys to', align: 'center' as const },
]
const visibleColumns = computed(() => narrow.value ? tableColumns.slice(0, 1) : tableColumns)

const deploymentTargets: Record<string, string> = {
  k8s: 'Kubernetes',
  kubernetes: 'Kubernetes',
  aws: 'AWS',
  'amazon-web-services': 'AWS',
  'amazon web services': 'AWS',
  azure: 'Microsoft Azure',
  'microsoft-azure': 'Microsoft Azure',
  'microsoft azure': 'Microsoft Azure',
  gcp: 'Google Cloud',
  'google-cloud': 'Google Cloud',
  'google cloud': 'Google Cloud',
}

function destination(template: Template): string {
  const target = template.cloud?.trim()
  if (!target) return 'Not specified'
  return Object.hasOwn(deploymentTargets, target.toLowerCase()) ? deploymentTargets[target.toLowerCase()] : target
}

const tableRows = computed(() => templates.value.map(template => ({
  identity: template.displayName || template.name,
  name: template.name,
  description: template.description,
  category: template.category || 'Other',
  kind: template.kind || '—',
  version: template.version ? `v${template.version}` : '—',
  exposure: exposureLabel(template.exposure),
  destination: destination(template),
})))

// Category is inline on narrow rows, so ResourceTable has no Category column to sort.
const visibleRows = computed(() => narrow.value && sort.value?.key === 'category'
  ? sortTableRows(tableRows.value, sort.value, row => row.category)
  : tableRows.value)

function exposureLabel(exposure: Template['exposure']): string {
  const value = exposure || 'internal'
  return value.charAt(0).toUpperCase() + value.slice(1)
}

async function load() {
  if (loading.value && requestSerial > 0) return
  const serial = ++requestSerial
  loading.value = true
  try {
    const r = await api.listTemplates()
    if (serial !== requestSerial) return
    templates.value = r.items || []
    loaded.value = true
    error.value = null
  } catch (e: unknown) {
    if (serial !== requestSerial || isContextChangedError(e)) return
    error.value = (e as { message?: string }).message ?? 'failed to load templates'
  } finally {
    if (serial === requestSerial) loading.value = false
  }
}
onMounted(() => {
  const element = catalog.value
  if (element && typeof ResizeObserver !== 'undefined') {
    const fitColumns = (width: number) => { narrow.value = width > 0 && width <= 512 }
    fitColumns(element.clientWidth)
    catalogObserver = new ResizeObserver(([entry]) => fitColumns(entry.contentRect.width))
    catalogObserver.observe(element)
  }
  void load()
})
onUnmounted(() => {
  catalogObserver?.disconnect()
  requestSerial += 1
})

function clearSearch() {
  query.value = ''
  search.value?.focus()
}

function dismissCloudGuide() {
  cloudGuideDismissed.value = true
  const key = cloudGuidePreference.value
  if (key) {
    try {
      window.localStorage.setItem(key, '1')
    } catch {
      // Dismissal still applies for this visit when storage is unavailable.
    }
  }
  search.value?.focus({ preventScroll: true })
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
  <section ref="catalog" class="page" :aria-busy="loading">
    <header class="page-head">
      <div>
        <h2 class="page-title" tabindex="-1">Templates</h2>
        <p class="page-meta">Choose a template to provision resources in this workspace.</p>
      </div>
      <a class="k-btn k-btn--ghost" :href="portalHref('/ui/providers/infrastructure/instances')" @click="followInstancesLink">My instances <ArrowRight :size="14" aria-hidden="true" /></a>
    </header>

    <aside v-if="!cloudGuideDismissed" class="catalog-cloud-guide k-card k-card--flat" aria-labelledby="cloud-guide-title">
      <span class="catalog-guide-icon"><Cloud :size="18" :stroke-width="1.75" aria-hidden="true" /></span>
      <div>
        <h3 id="cloud-guide-title" class="catalog-guide-title">Provision resources in your cloud</h3>
        <p class="page-meta">Use templates to create and manage resources in your organization’s cloud account.</p>
      </div>
      <a class="k-btn k-btn--text catalog-guide-link" :href="cloudGuideURL" target="_blank" rel="noopener noreferrer">
        Explore cloud provisioning <ExternalLink :size="14" :stroke-width="1.75" aria-hidden="true" />
      </a>
      <button type="button" class="k-icon-action k-icon-action--quiet catalog-guide-dismiss" aria-label="Dismiss cloud provisioning banner" @click="dismissCloudGuide">
        <X :stroke-width="1.75" aria-hidden="true" />
      </button>
    </aside>

    <div class="catalog-search k-table__search" role="search">
      <Search class="k-table__search-icon" :stroke-width="1.75" aria-hidden="true" />
      <input ref="search" v-model="query" class="k-table__search-input" type="search" aria-label="Search Infrastructure templates" placeholder="Search templates…" autocomplete="off">
      <button v-if="query" class="k-table__search-clear" type="button" aria-label="Clear search" @click="clearSearch"><X :stroke-width="1.75" aria-hidden="true" /></button>
    </div>

    <ResourceTable
      v-model:sort="sort"
      :query="query"
      :columns="visibleColumns"
      :rows="visibleRows"
      aria-label="Infrastructure templates"
      :search-keys="searchKeys"
      paginated
      row-key="name"
      :loaded="loaded"
      :loading="loading"
      :error="error"
      :stale="loaded && !!error"
      retryable
      :empty-text="emptyText"
      :search-empty-text="tableRows.length ? 'No templates match your search.' : emptyText"
      interactive
      :row-aria-label="(row) => `Provision template ${String(row.identity)}`"
      @retry="load"
      @row-click="selectTemplateRow"
    >
      <template #identity="{ value, row }">
        <a class="k-btn k-btn--ghost k-table-resource-link text-left" :class="{ 'catalog-narrow-link': narrow }" :href="templateHref(String(row.name))" @click.stop="followTemplateLink($event, row)">
          {{ value }}
        </a>
        <span v-if="narrow" class="catalog-row-details">
          <span><span class="catalog-row-label">Category</span> {{ row.category }}</span>
          <span><span class="catalog-row-label">Deploys to</span> <DeploymentTarget :label="String(row.destination)" /></span>
        </span>
      </template>
      <template #version="{ value }"><span class="cell-mono">{{ value }}</span></template>
      <template #exposure="{ value }"><span class="k-badge k-badge--muted k-badge--label">{{ value }}</span></template>
      <template #destination="{ value }"><DeploymentTarget :label="String(value)" /></template>
    </ResourceTable>

    <div v-if="loaded && !error && templates.length === 0" class="empty-followup">
      <button type="button" class="k-btn k-btn--ghost" @click="load">Refresh catalog</button>
    </div>
  </section>
</template>
