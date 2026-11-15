<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import ViewValue from '../components/ViewValue.vue'
import { api, isContextChangedError } from '../api'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTimestamp from '../portalkit/ResourceTimestamp.vue'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import InlineNotification from '../portalkit/InlineNotification.vue'
import { ArrowRight, Package } from 'lucide-vue-next'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import { confirmDialog } from '../portalkit/confirm'
import { toast } from '../portalkit/toast'
import { portalHref } from '../portalkit/navigation'
import {
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  FAST_REFRESH_MS,
  sameResourceIdentity,
  STABLE_REFRESH_MS,
  type ResourceRefreshMode,
  type ResourceTombstones,
} from '../refresh'
import { isCurrentInstanceListRequest, type InstanceListRequest } from '../instanceListRequest'
import { resolve, type ResolvedValue } from '../view'
import type { Instance, TemplateView, ViewColumn } from '../types'
import { isCompleteFirstCursorPage, type ResourceTableChange, type TableFilterDefinition, type TableFilterState } from '../portalkit/table'

const emit = defineEmits<{
  (e: 'navigate', view: string): void
  (e: 'select', name: string): void
}>()
const props = defineProps<{ tombstones: ResourceTombstones }>()

const items = ref<Instance[]>([])
const error = ref<string | null>(null)
const loading = ref(false)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const loaded = ref(false)
// A prior page does not establish a snapshot for a newly requested page.
const currentSnapshot = ref(false)
const pageRef = ref<HTMLElement | null>(null)
const pageHeading = ref<HTMLElement | null>(null)
const firstRunWarning = ref<HTMLElement | null>(null)
const firstRunRetryPending = ref(false)
const deletingInstanceKey = ref<string | null>(null)
const deleteError = ref<string | null>(null)
const viewByTemplate = ref<Map<string, TemplateView>>(new Map())
const templateFilterOptions = ref<Array<{ value: string; label: string }>>([])
const tombstones = props.tombstones
const paginationMode = ref<'server' | 'client'>('server')
const page = ref(1)
const pageSize = ref(10)
const query = ref('')
const filterValues = ref<TableFilterState>({ template: '', status: '' })
const firstRun = computed(() => loaded.value && currentSnapshot.value && items.value.length === 0 && !hasActiveFilters(query.value, filterValues.value)
  && (paginationMode.value === 'client'
    ? clientAuthorityReady.value
    : isCompleteFirstCursorPage({ page: page.value, cursor: cursor.value, pageInfo: pageInfo.value })))
watch(() => firstRun.value && !!error.value, visible => {
  if (visible || typeof document === 'undefined' || !firstRunWarning.value?.contains(document.activeElement)) return
  void nextTick(() => { if (document.activeElement === document.body) pageHeading.value?.focus({ preventScroll: true }) })
}, { flush: 'pre' })
const firstRunSteps = [
  { label: 'Choose a template', description: 'Browse the infrastructure templates available in this workspace.' },
  { label: 'Configure your instance', description: 'Name the instance and set the template inputs.' },
  { label: 'Follow provisioning', description: 'The instance page shows progress and the resources it creates.' },
]
const cursor = ref<string | null>(null)
const pageInfo = ref<{ hasNext: boolean; nextCursor: string | null } | null>(null)
// Client mode becomes a ready local authority after a complete cursor walk (or
// an explicitly terminal first page) has committed. A pending walk is still
// query-independent, so query/filter edits can wait for that same read.
const clientAuthorityReady = ref(false)
let reconcileAfterNextServerRead = false
let pendingReadMode: InstanceListRequest['mode'] | null = null
let active = true
let mounted = false

const STATUS_FILTER_OPTIONS = [
  { value: 'Ready', label: 'Ready' },
  { value: 'Pending', label: 'Pending' },
  { value: 'Failed', label: 'Failed' },
  { value: 'Deleting', label: 'Deleting' },
]

const filters = computed<TableFilterDefinition[]>(() => [
    {
      key: 'template',
      label: 'Template',
      control: 'combobox',
      searchPlaceholder: 'Find a template…',
      options: templateFilterOptions.value,
  },
  {
    key: 'status',
    label: 'Status',
    allLabel: 'Any status',
    options: STATUS_FILTER_OPTIONS,
  },
])

interface DynamicColumn {
  key: string
  label: string
  definitionByTemplate: Map<string, ViewColumn>
}

const dynamicColumns = computed<DynamicColumn[]>(() => {
  const byHeader = new Map<string, Map<string, ViewColumn>>()
  for (const instance of items.value) {
    const columns = viewByTemplate.value.get(instance.template)?.columns ?? []
    for (const column of columns) {
      let definitions = byHeader.get(column.header)
      if (!definitions) {
        definitions = new Map()
        byHeader.set(column.header, definitions)
      }
      definitions.set(instance.template, column)
    }
  }
  return [...byHeader].map(([label, definitionByTemplate]) => ({
    key: `view:${label}`,
    label,
    definitionByTemplate,
  }))
})

const columns = computed(() => [
  { key: 'name', label: 'Name', primary: true },
  { key: 'template', label: 'Template' },
  ...dynamicColumns.value.map(({ key, label }) => ({ key, label })),
  { key: 'status', label: 'Status' },
  { key: 'age', label: 'Age', align: 'end' as const },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
])

function instanceKey(instance: Pick<Instance, 'name'>): string {
  return instance.name
}

function instanceIsDeleting(instance: Instance): boolean {
  return Boolean(instance.deletionTimestamp) || tombstones.has(instanceKey(instance), instance.uid)
}

const rows = computed<Array<Record<string, unknown>>>(() => items.value.map(instance => {
  const row: Record<string, unknown> = {
    name: instance.name,
    rowKey: `${instanceKey(instance)}/${instance.uid ?? ''}`,
    template: instance.template,
    status: instanceIsDeleting(instance) ? 'Deleting' : instance.phase,
    age: instance.createdAt || null,
    actions: '',
    instance,
  }
  for (const column of dynamicColumns.value) {
    const definition = column.definitionByTemplate.get(instance.template)
    row[column.key] = definition ? resolve(definition, instance) : null
  }
  return row
}))

function rowInstance(row: Record<string, unknown>): Instance {
  return row.instance as Instance
}

function resolvedValue(value: unknown): ResolvedValue | null {
  return value && typeof value === 'object' ? value as ResolvedValue : null
}

function errorMessage(error: unknown, fallback: string): string {
  const value = error as { reason?: string; message?: string }
  return value.reason ? `${value.reason}: ${value.message || fallback}` : value.message || fallback
}

function cloneFilters(values: TableFilterState): TableFilterState {
  return { template: values.template || '', status: values.status || '' }
}

function hasActiveFilters(value: string, values: TableFilterState): boolean {
  return !!value.trim() || Object.values(values).some(Boolean)
}

function currentRequest(): InstanceListRequest {
  const filters = cloneFilters(filterValues.value)
  return {
    mode: paginationMode.value,
    active: hasActiveFilters(query.value, filters),
    page: page.value,
    pageSize: pageSize.value,
    query: query.value,
    filters,
    cursor: cursor.value,
  }
}

function requestIsCurrent(requestID: number, request: InstanceListRequest): boolean {
  return isCurrentInstanceListRequest(request, currentRequest(), refresh.isCurrent(requestID))
}

function updateTemplateMetadata(templates: Array<{ name: string; displayName: string; view?: TemplateView }>) {
  const views = new Map<string, TemplateView>()
  const options = templates
    .map(template => ({ value: template.name, label: template.displayName || template.name }))
    .sort((left, right) => left.label.localeCompare(right.label, undefined, { sensitivity: 'base' }))
  for (const template of templates) if (template.view) views.set(template.name, template.view)
  viewByTemplate.value = views
  templateFilterOptions.value = options
}

function refreshCadence(): number {
  if (!loaded.value || error.value || deletingInstanceKey.value !== null ||
    items.value.some(instance => instanceIsDeleting(instance) || instance.phase !== 'Ready')) {
    return FAST_REFRESH_MS
  }
  return STABLE_REFRESH_MS
}

const pollTimer = createAdaptiveRefreshTimer(
  () => { void load('background') },
  refreshCadence,
)

const refresh = createLatestRefreshController(async (requestID, mode) => {
  const request = currentRequest()
  pendingReadMode = request.mode
  refreshMode.value = mode
  loading.value = true
  try {
    let nextItems: Instance[]
    let nextIdentities: Array<{ name: string; uid?: string }>
    let nextCursor: string | null = null
    let nextPageInfo: { hasNext: boolean; nextCursor: string | null } | null = null

    if (request.active || request.mode === 'client') {
      const result = await api.listInstances()
      if (!requestIsCurrent(requestID, request)) return
      nextItems = result.items
      nextIdentities = result.identities
    } else {
      const result = await api.listInstancesPage({
        limit: request.pageSize,
        ...(request.cursor ? { continue: request.cursor } : {}),
      })
      if (!requestIsCurrent(requestID, request)) return
      nextItems = result.items
      nextIdentities = []
      nextCursor = request.cursor
      nextPageInfo = {
        hasNext: !!result.continue,
        nextCursor: result.continue ?? null,
      }

      // A page is not an absence proof. Only the explicit identity walk may
      // reconcile a deletion marker after a list-page delete operation.
      if (reconcileAfterNextServerRead) {
        try {
          const identities = await api.listInstanceIdentities()
          if (!requestIsCurrent(requestID, request)) return
          tombstones.reconcile(identities)
          reconcileAfterNextServerRead = false
        } catch (caught) {
          if (isContextChangedError(caught)) throw caught
          // Keep the marker until a later complete identity read succeeds.
        }
      }
    }

    if (!requestIsCurrent(requestID, request)) return
    try {
      const templates = await api.listTemplates()
      if (!requestIsCurrent(requestID, request)) return
      updateTemplateMetadata(templates.items)
    } catch (caught) {
      if (isContextChangedError(caught)) throw caught
      // Instance state remains useful when optional presentation metadata is
      // temporarily unavailable. Keep the last successful view map.
    }

    if (!requestIsCurrent(requestID, request)) return
    // Once the API server has exposed deletionTimestamp, keep that UID in the
    // Deleting state even if a later cache snapshot briefly omits the field.
    for (const instance of nextItems) {
      if (instance.deletionTimestamp) tombstones.add(instanceKey(instance), instance.uid)
    }
    items.value = nextItems
    currentSnapshot.value = true
    if (request.active || request.mode === 'client') {
      // Complete cursor walks are the only full absence proofs. This remains
      // safe even while the ResourceTable is showing a client-side subset.
      tombstones.reconcile(nextIdentities)
      reconcileAfterNextServerRead = false
      clientAuthorityReady.value = true
      cursor.value = null
      pageInfo.value = null
    } else {
      clientAuthorityReady.value = false
      cursor.value = nextCursor
      pageInfo.value = nextPageInfo
    }
    loaded.value = true
    error.value = null
  } catch (caught) {
    if (!requestIsCurrent(requestID, request) || isContextChangedError(caught)) return
    const table = pageRef.value?.querySelector<HTMLElement>('[data-resource-table]')
    const focused = typeof document !== 'undefined' ? document.activeElement : null
    const losesTableFocus = !currentSnapshot.value && !!focused && !!table?.contains(focused)
    error.value = errorMessage(caught, 'failed to list instances')
    if (losesTableFocus) {
      await nextTick()
      if (!focused?.isConnected && document.activeElement === document.body) table?.querySelector<HTMLButtonElement>('.k-table__retry')?.focus()
    }
  } finally {
    if (pendingReadMode === request.mode) pendingReadMode = null
    if (refresh.isCurrent(requestID)) {
      loading.value = false
      if (mounted && active) pollTimer.schedule()
    }
  }
})

function load(mode: ResourceRefreshMode = 'foreground'): Promise<void> {
  if (mode === 'foreground' && loading.value) refreshMode.value = 'foreground'
  return refresh.request(mode)
}

async function retryFirstRun(): Promise<void> {
  if (firstRunRetryPending.value || loading.value) return
  firstRunRetryPending.value = true
  try { await load('foreground') } finally { firstRunRetryPending.value = false }
}

function resetToFirstServerPage() {
  page.value = 1
  cursor.value = null
}

function handleTableChange(change: ResourceTableChange) {
  const wasClientMode = paginationMode.value === 'client'
  const canReuseCurrentServerPage = paginationMode.value === 'server' && isCompleteFirstCursorPage({
    page: page.value,
    cursor: cursor.value,
    pageInfo: pageInfo.value,
  })
  const nextFilters = cloneFilters(change.filters)
  const activeQuery = hasActiveFilters(change.query, nextFilters)
  page.value = change.page
  pageSize.value = change.pageSize
  query.value = change.query
  filterValues.value = nextFilters
  cursor.value = change.cursor
  pageInfo.value = null

  if (!activeQuery) {
    // Returning to an empty query must switch back to the bounded page path;
    // ResourceTable also emits page one with a null cursor on clear.
    paginationMode.value = 'server'
    clientAuthorityReady.value = false
    if (wasClientMode || change.reason === 'query' || change.reason === 'filter') resetToFirstServerPage()
    items.value = []
    currentSnapshot.value = false
    void load()
    return
  }

  if (paginationMode.value === 'client') {
    // Both a pending and a ready client source are query-independent. The
    // pending walk is allowed to commit against the latest reactive query;
    // only an in-flight server read left by clear, or a page-size authority
    // change, needs a replacement read. The refresh controller serializes it
    // behind the rejected server response.
    if (pendingReadMode === 'server' || change.reason === 'page-size') {
      clientAuthorityReady.value = false
      items.value = []
      currentSnapshot.value = false
      void load()
    }
    return
  }
  // Entering a query/filter changes the data authority from one page to the
  // complete cursor walk. Clear the page immediately so old rows cannot be
  // mistaken for the new result while that walk is in flight.
  paginationMode.value = 'client'
  page.value = 1
  cursor.value = null
  if (canReuseCurrentServerPage) {
    clientAuthorityReady.value = true
    return
  }
  clientAuthorityReady.value = false
  items.value = []
  currentSnapshot.value = false
  void load()
}

async function deleteInstance(instance: Instance) {
  if (deletingInstanceKey.value !== null || instanceIsDeleting(instance)) return
  const expectedInstance = instance
  deleteError.value = null
  const confirmed = await confirmDialog({
    title: 'Delete instance',
    message: `"${instance.name}" (${instance.template}) and the resources it provisioned will be permanently deleted.`,
    irreversibleMessage: `Deleting "${instance.name}" cannot be undone.`,
    confirmLabel: 'Delete instance',
    danger: true,
    verificationText: instance.name,
    verificationLabel: 'the instance name',
  })
  if (!confirmed || !active || deletingInstanceKey.value !== null) return
  const currentInstance = items.value.find(item => item.name === expectedInstance.name)
  if (!sameResourceIdentity(expectedInstance, currentInstance) || instanceIsDeleting(currentInstance)) return

  deletingInstanceKey.value = instanceKey(currentInstance)
  try {
    await api.deleteInstance(currentInstance.name)
    if (active) {
      tombstones.add(instanceKey(currentInstance), currentInstance.uid)
      toast('info', `Instance deletion requested for ${currentInstance.name}.`)
      reconcileAfterNextServerRead = true
      // The complete source is stale until the post-delete read proves the
      // tombstone's absence/presence, so query edits during that read must
      // wait for its refreshed source rather than filtering an acknowledged
      // stale snapshot.
      clientAuthorityReady.value = false
      await load()
    }
  } catch (caught) {
    if (active && !isContextChangedError(caught)) deleteError.value = errorMessage(caught, 'delete failed')
  } finally {
    deletingInstanceKey.value = null
  }
}

function selectInstance(row: Record<string, unknown>) {
  const instance = rowInstance(row)
  if (deletingInstanceKey.value === instanceKey(instance) || instanceIsDeleting(instance)) return
  emit('select', instance.name)
}

function openInstanceLink(event: MouseEvent, row: Record<string, unknown>): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  selectInstance(row)
}

function followCatalogLink(event: MouseEvent): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  emit('navigate', 'catalog')
}

onMounted(() => {
  mounted = true
  void load('foreground')
})
onUnmounted(() => {
  active = false
  mounted = false
  pollTimer.stop()
  refresh.stop()
})
</script>

<template>
  <section ref="pageRef" class="page">
    <header class="page-head">
      <div>
        <h2 ref="pageHeading" class="page-title" tabindex="-1">My instances</h2>
        <p class="page-meta">Provisioned into the active workspace.</p>
      </div>
      <div class="instance-list-actions">
        <span class="refresh-cadence">auto-refresh {{ refreshCadence() / 1000 }}s</span>
        <a v-if="!firstRun" class="k-btn k-btn--primary" :href="portalHref('/ui/providers/infrastructure/templates')" @click="followCatalogLink">Browse templates</a>
      </div>
    </header>

    <div v-if="deleteError" class="mutation-error" role="alert" aria-live="assertive">
      <span>{{ deleteError }}</span>
      <button type="button" class="k-btn k-btn--ghost" @click="deleteError = null">Dismiss</button>
    </div>

    <template v-if="firstRun">
      <span v-if="loading" class="sr-only" role="status" aria-live="polite">Updating infrastructure instances…</span>
      <div v-if="error" ref="firstRunWarning">
        <InlineNotification tone="warning" :message="`Showing the last successful result. ${error}`" action-label="Retry" action-busy-label="Retrying…" :action-busy="loading || firstRunRetryPending" @action="retryFirstRun" />
      </div>
      <FirstRunGuide
        title="Provision your first instance"
        description="Infrastructure templates create and manage resources for this workspace. Choose a template to get started."
        primary-label="Browse templates"
        :steps="firstRunSteps"
        journey-label="Infrastructure provisioning"
      >
        <template #icon><Package :size="24" :stroke-width="1.5" aria-hidden="true" /></template>
        <template #actions><a class="k-btn k-btn--primary" :href="portalHref('/ui/providers/infrastructure/templates')" @click="followCatalogLink">Browse templates <ArrowRight :stroke-width="1.75" aria-hidden="true" /></a></template>
      </FirstRunGuide>
    </template>

    <ResourceTable
      v-else
      :columns="columns"
      :rows="rows"
      aria-label="Infrastructure instances"
      searchable
      search-placeholder="Search instances…"
      :filters="filters"
      :pagination-mode="paginationMode"
      :page="page"
      :page-size="pageSize"
      :query="query"
      :filter-values="filterValues"
      :cursor="cursor"
      :page-info="pageInfo"
      row-key="rowKey"
      :loaded="loaded && (currentSnapshot || !error)"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="currentSnapshot && !!error"
      interactive
      retryable
      empty-text="No instances on this page."
      :row-aria-label="(row) => `Open instance ${String(rowInstance(row).name)}`"
      @retry="load('foreground')"
      @change="handleTableChange"
      @row-click="selectInstance"
    >
      <template #name="{ value, row }">
        <span v-if="instanceIsDeleting(rowInstance(row)) || deletingInstanceKey === instanceKey(rowInstance(row))">{{ value }}</span>
        <a
          v-else
          class="k-btn k-btn--ghost k-table-resource-link"
          :href="portalHref(`/ui/providers/infrastructure/instances/${encodeURIComponent(rowInstance(row).name)}`)"
          :aria-label="`Open instance ${String(value)}`"
          @click.stop="openInstanceLink($event, row)"
        >{{ value }}</a>
      </template>
      <template #template="{ value }"><code>{{ value }}</code></template>
      <template v-for="column in dynamicColumns" :key="column.key" v-slot:[column.key]="{ value, row }">
        <ViewValue
          v-if="resolvedValue(value)"
          :value="resolvedValue(value)!"
          :interactive="!instanceIsDeleting(rowInstance(row))"
        />
        <span v-else class="cell-empty">—</span>
      </template>
      <template #status="{ row }">
        <StatusBadge :status="String(row.status)" :tone="String(row.status) === 'Deleting' ? 'warning' : null" />
      </template>
      <template #age="{ value }"><ResourceTimestamp :value="value ? String(value) : null" label="Instance created" /></template>
      <template #actions="{ row }">
        <div class="row-actions">
          <ResourceTableDeleteButton
            :label="`Delete instance ${rowInstance(row).name}`"
            :busy-label="`Deleting instance ${rowInstance(row).name}…`"
            :busy="deletingInstanceKey === instanceKey(rowInstance(row)) || instanceIsDeleting(rowInstance(row))"
            :disabled="deletingInstanceKey !== null || instanceIsDeleting(rowInstance(row))"
            @click="deleteInstance(rowInstance(row))"
          />
        </div>
      </template>
    </ResourceTable>

  </section>
</template>
