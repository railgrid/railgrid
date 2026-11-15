<script setup lang="ts">
import { ref, computed, nextTick, onMounted, onUnmounted, onActivated, watch } from 'vue'
import { ArrowRight, RefreshCw, Plus, Check, Plug, Server } from 'lucide-vue-next'
import {
  listServices, listServicesPage, deleteEdgeService, listEdges,
  fetchServiceCatalog,
} from './api'
import type { CatalogEntry } from './api'
import type { EdgeService, Edge, ErrorResponse } from './types'
import { confirmDialog } from './portalkit/confirm'
import { portalHref } from './portalkit/navigation'
import { edgeConnectPath, serviceCreatePath, serviceDetailPath } from './routes'
import { serviceTarget } from './serviceTarget'
import { toast } from './portalkit/toast'
import ResourceTable from './portalkit/ResourceTable.vue'
import ActionMenu from './portalkit/ActionMenu.vue'
import InlineNotification from './portalkit/InlineNotification.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import ServiceEdit from './ServiceEdit.vue'
import FirstRunGuide from './portalkit/FirstRunGuide.vue'
import { isCompleteFirstCursorPage, type ResourceTableChange, type TableFilterDefinition, type TablePageInfo } from './portalkit/table'
import { createFullListReadCoordinator, createInFlightReadCoordinator, hasActiveTableFilters, sameTableRequest, tablePageInfo as makeTablePageInfo, type PaginationMode, type TableRequestState } from './pagination'
import {
  FAST_REFRESH_MS,
  STABLE_REFRESH_MS,
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  type ResourceRefreshMode,
} from './refresh'

const props = defineProps<{ selectedName?: string | null }>()
const emit = defineEmits<{
  navigate: [name: string | null, options?: { replace?: boolean }]
  create: []
  connectEdge: []
}>()

// Service type catalog — fetched from the backend (svccatalog.All()) so the form
// never drifts from the provider's auth/probe knowledge. Each entry seeds the
// port/scheme and describes the credential fields the UI collects.
const catalog = ref<CatalogEntry[]>([])
function catalogFor(t?: string): CatalogEntry | undefined {
  return catalog.value.find((c) => c.type === t)
}
function credentialLabel(service: EdgeService): string {
  const entry = catalogFor(service.serviceType)
  if (!entry) return service.hasCredentials ? 'Configured' : 'Not checked'
  if (!entry.auth || entry.auth === 'none') return 'Not required'
  if (service.hasCredentials) return 'Configured'
  return entry.credential.optional ? 'Not configured (optional)' : 'Missing'
}
// A one-entry fallback so the form still works if the catalog fetch fails.
const GENERIC_FALLBACK: CatalogEntry = {
  type: 'generic', displayName: 'Generic HTTP service', category: 'Other',
  defaultPort: 80, defaultScheme: 'http', auth: 'bearer',
  credential: { optional: true, packing: 'single', fields: [{ key: 'token', label: 'Bearer token (optional)', secret: true }] },
}
async function loadCatalog() {
  try {
    catalog.value = await fetchServiceCatalog()
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Failed to load service catalog'
    if (!catalog.value.length) catalog.value = [GENERIC_FALLBACK]
  }
}
const services = ref<EdgeService[]>([])
const edges = ref<Edge[]>([])
const loading = ref(true)
const loaded = ref(false)
const currentSnapshot = ref(false)
const collectionPage = ref<HTMLElement | null>(null)
const error = ref<string | null>(null)
const deletingService = ref<string | null>(null)
const deleteFailure = ref<{ name: string; message: string } | null>(null)
const servicesHeading = ref<HTMLElement | null>(null)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const foregroundLoading = computed(() => loading.value && refreshMode.value === 'foreground')
const serviceColumns = [
  { key: 'name', label: 'Service', primary: true },
  { key: 'status', label: 'Status' },
  { key: 'edgeName', label: 'Edge' },
  { key: 'typeLabel', label: 'Type' },
  { key: 'target', label: 'Target' },
  { key: 'credentials', label: 'Credentials' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]
const serviceRows = computed<Array<Record<string, unknown>>>(() => services.value.map(service => ({
  ...service,
  edgeName: service.edgeName || '—',
  typeLabel: catalogFor(service.serviceType)?.displayName || service.serviceType || '—',
  target: serviceTarget(service),
  status: service.phase || 'Pending',
  credentials: credentialLabel(service),
  actions: '',
})))
const showFirstRun = computed(() => loaded.value && currentSnapshot.value && services.value.length === 0 && isCompleteFirstCursorPage({
  page: tablePage.value,
  cursor: tableCursor.value,
  pageInfo: tablePageInfo.value,
}) && !hasActiveTableFilters(tableQuery.value, filterValues.value))
const hasEdges = computed(() => edges.value.length > 0)
const serviceJourney = [
  { label: 'Edge', description: 'Connect the cluster or server that can reach the service.' },
  { label: 'Service endpoint', description: 'Choose its type, address, protocol, and port.' },
  { label: 'Credentials and Ready', description: 'Add credentials when required and let the controller verify it.' },
]

function handleFirstRunPrimary(): void {
  if (hasEdges.value) emit('create')
  else emit('connectEdge')
}

const firstRunHref = computed(() => portalHref(`/ui/providers/edges/${hasEdges.value
  ? serviceCreatePath()
  : edgeConnectPath('create/service', { cancelPath: 'services' })}`))

function followFirstRunLink(event: MouseEvent): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  handleFirstRunPrimary()
}

function followCreateLink(event: MouseEvent): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  emit('create')
}

const SERVICE_STATUS_OPTIONS = [
  { value: 'Pending', label: 'Pending' },
  { value: 'Detected', label: 'Detected' },
  { value: 'Ready', label: 'Ready' },
  { value: 'Unreachable', label: 'Unreachable' },
]

type ServiceFilterValues = {
  edgeName: string
  typeLabel: string
  status: string
}

type ServicePaginationMode = PaginationMode

const tableMode = ref<ServicePaginationMode>('server')
const tablePage = ref(1)
const tablePageSize = ref(10)
const tableQuery = ref('')
const filterValues = ref<ServiceFilterValues>({ edgeName: '', typeLabel: '', status: '' })
const tableCursor = ref<string | null>(null)
const tablePageInfo = ref<TablePageInfo | null>(null)
// Client-side filtering is authoritative only after a complete, query-
// independent cursor walk has committed. During a pending walk, keep the
// last complete rows visible and let newer query/filter edits join the same
// in-flight read rather than starting another walk.
const clientAuthorityReady = ref(false)
const clientReadPending = ref(false)
const completeServiceRead = createFullListReadCoordinator(() => listServices())
// Edge joins are query-independent but must stay fresh for every server or
// client refresh. Share an in-flight join across clear/re-entry without
// retaining it as a long-lived cache.
const edgeRead = createInFlightReadCoordinator(() => listEdges())

const serviceFilters = computed<TableFilterDefinition[]>(() => {
  const types = new Map<string, string>()
  catalog.value.forEach(entry => types.set(entry.displayName, entry.displayName))
  return [
    {
      key: 'edgeName',
      label: 'Edge',
      control: 'combobox',
      searchPlaceholder: 'Find an edge…',
      options: edges.value.map(edge => ({ value: edge.name, label: edge.name })),
    },
    {
      key: 'typeLabel',
      label: 'Type',
      options: [...types].map(([value, label]) => ({ value, label })),
    },
    {
      key: 'status',
      label: 'Status',
      allLabel: 'Any status',
      options: SERVICE_STATUS_OPTIONS,
    },
  ]
})

// The selected resource identity is URL-owned by App.vue. Keep the list's
// table/search/pagination state local, and only retain a row snapshot as an
// optimistic seed while ServiceEdit performs its exact getService read.
const editing = ref<EdgeService | null>(null)
function openEdit(s: EdgeService) {
  editing.value = s
  emit('navigate', s.name)
}
function openServiceLink(event: MouseEvent, service: EdgeService): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  openEdit(service)
}
function closeEdit(replace = false) {
  editing.value = null
  emit('navigate', null, replace ? { replace: true } : undefined)
}
function syncRouteSelection() {
  if (props.selectedName === undefined || props.selectedName === null) {
    editing.value = null
    return
  }
  editing.value = services.value.find((s) => s.name === props.selectedName) ?? null
}
watch(() => props.selectedName, syncRouteSelection, { immediate: true })

// After a save in the edit page, refresh the list and re-seed the open page
// from the fresh object so table joins/status remain current. ServiceEdit also
// refreshes its exact snapshot, so this does not depend on the visible cursor.
async function onEditSaved() {
  const name = props.selectedName ?? editing.value?.name
  await refresh('foreground', true)
  if (name) editing.value = services.value.find((s) => s.name === name) ?? editing.value
}

async function onEditDeleted() {
  const name = props.selectedName ?? editing.value?.name
  await refresh('foreground', true)
  if (name && props.selectedName === name) closeEdit(true)
}

type ServiceTableRequest = Omit<TableRequestState, 'filters'> & { filters: ServiceFilterValues }

let stopped = false
let clientReadToken = 0
let forceNextCompleteRead = false

function cloneServiceFilters(filters: ServiceFilterValues): ServiceFilterValues {
  return { edgeName: filters.edgeName, typeLabel: filters.typeLabel, status: filters.status }
}

function currentServiceRequest(): ServiceTableRequest {
  const filters = cloneServiceFilters(filterValues.value)
  return {
    mode: tableMode.value,
    active: hasActiveTableFilters(tableQuery.value, filters),
    page: tablePage.value,
    pageSize: tablePageSize.value,
    query: tableQuery.value,
    filters,
    cursor: tableCursor.value,
  }
}

function serviceRequestIsCurrent(requestID: number, request: ServiceTableRequest): boolean {
  const current = currentServiceRequest()
  return !stopped && serviceRefresh.isCurrent(requestID) && sameTableRequest(current, request)
}

// A complete client read is query-independent. Query/filter-only changes are
// allowed to update the controlled table state while its target + edge join is
// pending; page, page-size, mode, mutation, and context changes still fail
// this guard through the stable request dimensions below.
function serviceClientReadIsCurrent(requestID: number, request: ServiceTableRequest, readGeneration: number): boolean {
  const current = currentServiceRequest()
  return !stopped && serviceRefresh.isCurrent(requestID) && tableMode.value === 'client' &&
    current.active && current.page === request.page &&
    current.pageSize === request.pageSize && current.cursor === request.cursor &&
    completeServiceRead.generation() === readGeneration && clientAuthorityReady.value === false
}

const poller = createAdaptiveRefreshTimer(() => {
  void refresh('background')
}, () => {
  if (!loaded.value || error.value) return FAST_REFRESH_MS
  const unsettledService = services.value.some(service => (service.phase || 'Pending').toLowerCase() !== 'ready')
  const unsettledEdge = edges.value.some(edge => !edge.connected || ['pending', 'provisioning', 'deleting'].includes((edge.phase || '').toLowerCase()))
  return unsettledService || unsettledEdge ? FAST_REFRESH_MS : STABLE_REFRESH_MS
})

const serviceRefresh = createLatestRefreshController(async (requestID, mode) => {
  const readToken = ++clientReadToken
  const request = currentServiceRequest()
  const wasClientAuthorityReady = clientAuthorityReady.value
  const forceCompleteRead = request.mode === 'client' && (wasClientAuthorityReady || forceNextCompleteRead)
  forceNextCompleteRead = false
  if (forceCompleteRead) {
    clientAuthorityReady.value = false
    // Keep the visible complete rows while a polling/CRUD replacement is in
    // flight, but make the old cache non-authoritative. Query edits during
    // this window therefore join the active walk instead of queueing another
    // forced walk; a failed replacement also cannot be mistaken for ready
    // data on the next edit.
    completeServiceRead.clear()
  }
  clientReadPending.value = request.mode === 'client'
  const readGeneration = completeServiceRead.generation()
  refreshMode.value = mode
  loading.value = true
  // Keep a failed read visible while retrying so the focused Retry control
  // remains available and the previous snapshot is still marked stale.
  // Do not render an unfiltered page as though it matched a newly-entered
  // query/filter. Same-page polling keeps cached rows visible until the
  // replacement arrives.
  if (mode === 'foreground' && request.active && request.mode === 'server') {
    services.value = []
    currentSnapshot.value = false
    tablePageInfo.value = null
  }
  try {
    if (request.active || request.mode === 'client') {
      // A query-independent full read is shared across rapid query/filter
      // edits. Polling and CRUD refreshes force one fresh walk; query changes
      // while that walk is pending join it and commit the newest request.
      const shouldForceCompleteRead = forceCompleteRead || (
        request.mode === 'client' && !wasClientAuthorityReady &&
        completeServiceRead.peek() !== null && !completeServiceRead.pending()
      )
      // The full source and its supporting edge join form one query-
      // independent transition. Query/filter edits leave this request alive;
      // the stable client guard below commits the latest reactive state once
      // both reads settle.
      const [nextServices, nextEdges] = await Promise.all([
        completeServiceRead.read(shouldForceCompleteRead),
        edgeRead.read(),
      ])
      if (!serviceClientReadIsCurrent(requestID, request, readGeneration)) {
        return
      }
      services.value = nextServices
      currentSnapshot.value = true
      edges.value = nextEdges
      tableMode.value = 'client'
      tablePage.value = 1
      tableCursor.value = null
      tablePageInfo.value = null
      clientAuthorityReady.value = true
    } else {
      const [nextPage, nextEdges] = await Promise.all([
        listServicesPage({
          limit: request.pageSize,
          ...(request.cursor ? { continue: request.cursor } : {}),
        }),
        edgeRead.read(),
      ])
      if (!serviceRequestIsCurrent(requestID, request)) return
      services.value = nextPage.items
      currentSnapshot.value = true
      edges.value = nextEdges
      tableCursor.value = request.cursor
      tablePageInfo.value = makeTablePageInfo(nextPage.continue)
    }
    loaded.value = true
    error.value = null
  } catch (e) {
    const current = request.mode === 'client' && request.active
      ? serviceClientReadIsCurrent(requestID, request, readGeneration)
      : serviceRequestIsCurrent(requestID, request)
    if (!current) return
    const table = collectionPage.value?.querySelector?.<HTMLElement>('[data-resource-table]')
    const focused = typeof document !== 'undefined' ? document.activeElement : null
    const losesTableFocus = !currentSnapshot.value && !!focused && !!table?.contains(focused)
    error.value = (e as ErrorResponse)?.message ?? 'Failed to load services'
    if (losesTableFocus) {
      await nextTick()
      if (!focused?.isConnected && document.activeElement === document.body) table?.querySelector<HTMLButtonElement>('.k-table__retry')?.focus()
    }
  } finally {
    if (clientReadToken === readToken) clientReadPending.value = false
    if (!stopped && serviceRefresh.isCurrent(requestID)) loading.value = false
    poller.schedule()
  }
})

function refresh(mode: ResourceRefreshMode | Event = 'foreground', forceClientRead = false) {
  if (forceClientRead) forceNextCompleteRead = true
  const requestedMode = typeof mode === 'string' ? mode : 'foreground'
  if (requestedMode === 'foreground') {
    refreshMode.value = 'foreground'
    loading.value = true
  }
  return serviceRefresh.request(requestedMode)
}

async function refreshFromHeader(): Promise<void> {
  if (foregroundLoading.value) return
  const focused = typeof document !== 'undefined' ? document.activeElement : null
  await refresh('foreground')
  await nextTick()
  if (focused && !focused.isConnected && document.activeElement === document.body) servicesHeading.value?.focus()
}

function handleServiceTableChange(change: ResourceTableChange) {
  const wasClientMode = tableMode.value === 'client'
  const canReuseCurrentServerPage = !wasClientMode && isCompleteFirstCursorPage({
    page: tablePage.value,
    cursor: tableCursor.value,
    pageInfo: tablePageInfo.value,
  })
  filterValues.value = {
    edgeName: change.filters.edgeName ?? '',
    typeLabel: change.filters.typeLabel ?? '',
    status: change.filters.status ?? '',
  }
  tablePage.value = change.page
  tablePageSize.value = change.pageSize
  tableQuery.value = change.query
  tableCursor.value = change.cursor
  tablePageInfo.value = null

  const active = hasActiveTableFilters(tableQuery.value, filterValues.value)
  if (!active) {
    tableMode.value = 'server'
    clientAuthorityReady.value = false
    // Returning from local filtering always starts at the first bounded page;
    // ordinary unfiltered page navigation preserves its incoming cursor.
    if (wasClientMode || change.reason === 'query' || change.reason === 'filter') {
      tablePage.value = 1
      tableCursor.value = null
    }
    completeServiceRead.clear()
    services.value = []
    currentSnapshot.value = false
    void refresh()
    return
  }

  // A terminal first server page is already a complete authority. Promote it
  // synchronously so entering a query never causes an unnecessary full walk.
  if (canReuseCurrentServerPage) {
    completeServiceRead.seed(services.value)
    tableMode.value = 'client'
    clientAuthorityReady.value = true
    tablePage.value = 1
    tableCursor.value = null
    tablePageInfo.value = null
    return
  }

  // Once a complete source is resident, table changes are local. During the
  // pending transition, query/filter-only changes are absorbed by the stable
  // client guard; other dimensions invalidate and restart the read.
  if (tableMode.value === 'client') {
    if (!clientAuthorityReady.value) {
      if (clientReadPending.value && (change.reason === 'query' || change.reason === 'filter')) return
      void refresh()
    }
    return
  }

  // Entering a query/filter changes the authority from one server page to a
  // complete source. Do not expose the partial page as a local result.
  tableMode.value = 'client'
  clientAuthorityReady.value = false
  tablePage.value = 1
  tableCursor.value = null
  tablePageInfo.value = null
  services.value = []
  currentSnapshot.value = false
  void refresh()
}

async function onDelete(s: EdgeService) {
  if (deletingService.value) return
  if (!(await confirmDialog({ title: 'Delete service', message: `"${s.name}" will be deleted, and its MCP tools will no longer be available.`, danger: true, confirmLabel: 'Delete service' }))) return
  if (deletingService.value || stopped) return
  deletingService.value = s.name
  try {
    await deleteEdgeService(s.name)
    if (stopped) return
    deleteFailure.value = null
    toast('info', `Service deletion requested for ${s.name}.`)
    const focusedAction = typeof document !== 'undefined' ? document.activeElement : null
    await refresh('foreground', true)
    await nextTick()
    if (focusedAction && !focusedAction.isConnected && document.activeElement === document.body) servicesHeading.value?.focus()
  } catch (e) {
    if (!stopped) deleteFailure.value = { name: s.name, message: (e as ErrorResponse)?.message ?? 'The request failed.' }
  } finally {
    deletingService.value = null
  }
}

async function retryFirstRun() {
  if (loading.value) return
  const focused = typeof document !== 'undefined' ? document.activeElement : null
  await refresh('foreground')
  await nextTick()
  if (focused && !focused.isConnected && document.activeElement === document.body) servicesHeading.value?.focus()
}

onMounted(() => {
  loadCatalog()
  refresh()
})
// The collection stays cached while a route-owned create/detail page is open.
// Revalidate on return so a successful mutation is visible without discarding
// the user's query, filters, cursor, or page state.
onActivated(() => {
  if (!stopped) void refresh('foreground', true)
})
onUnmounted(() => {
  stopped = true
  serviceRefresh.stop()
  poller.stop()
})

function serviceRowAriaLabel(row: Record<string, unknown>): string {
  return `Open service ${String(row.name)}`
}
</script>

<template>
  <ServiceEdit
    v-if="props.selectedName !== undefined && props.selectedName !== null"
    :service="editing"
    :service-name="props.selectedName"
    :catalog="catalog"
    :edges="edges"
    @back="closeEdit"
    @saved="onEditSaved"
    @deleted="onEditDeleted"
  />
  <div v-else ref="collectionPage" class="edges-app">
    <header class="edges-header">
      <div>
        <h1 ref="servicesHeading" tabindex="-1">Services</h1>
        <p>Manage apps reachable from your edges and expose their tools to AI.</p>
      </div>
      <div v-if="(loaded || error) && !showFirstRun" class="header-actions">
        <button type="button" class="k-btn k-btn--ghost" aria-label="Refresh services" :aria-disabled="foregroundLoading || undefined" :aria-busy="foregroundLoading || undefined" @click="refreshFromHeader">
          <RefreshCw :size="14" :class="{ spin: foregroundLoading }" aria-hidden="true" /> {{ foregroundLoading ? 'Refreshing…' : 'Refresh' }}
        </button>
        <a
          class="k-btn k-btn--primary"
          :href="portalHref(`/ui/providers/edges/${serviceCreatePath()}`)"
          @click="followCreateLink"
        >
          <Plus :size="14" aria-hidden="true" /> New service
        </a>
      </div>
    </header>

    <InlineNotification v-if="showFirstRun && error" tone="error" :announce="refreshMode === 'background' ? 'polite' : 'assertive'" title="Could not refresh services" :message="`Showing the last successful result. ${error}`" action-label="Retry" action-busy-label="Retrying…" :action-busy="loading" @action="retryFirstRun" />
    <FirstRunGuide
      v-if="showFirstRun"
      :title="hasEdges ? 'Expose your first service' : 'Connect an edge first'"
      :description="hasEdges
        ? 'Declare an app reachable from an edge, then add credentials when its service type requires them.'
        : 'A Service must run beside an edge before Railgrid can expose its endpoint and tools.'"
      :primary-label="hasEdges ? 'Create service' : 'Connect edge'"
      :steps="hasEdges ? [] : serviceJourney"
      :current-step="hasEdges ? 1 : 0"
      journey-label="Service setup path"
    >
      <template #icon><component :is="hasEdges ? Plug : Server" aria-hidden="true" /></template>
      <template #actions><a class="k-btn k-btn--primary" :href="firstRunHref" @click="followFirstRunLink">{{ hasEdges ? 'Create service' : 'Connect edge' }} <ArrowRight :stroke-width="1.75" aria-hidden="true" /></a></template>
    </FirstRunGuide>

    <div v-else class="services-table">
      <InlineNotification v-if="deleteFailure" class="services-delete-error" tone="error" :title="`Could not delete ${deleteFailure.name}`" :message="`${deleteFailure.message} Try again from the service's action menu.`" />
      <ResourceTable
        :columns="serviceColumns"
        :rows="serviceRows"
        aria-label="Services"
        row-key="name"
        :row-aria-label="serviceRowAriaLabel"
        :loaded="loaded && (currentSnapshot || !error)"
        :loading="loading"
        :refresh-mode="refreshMode"
        :error="error"
        :stale="currentSnapshot && !!error"
        retryable
        searchable
        search-placeholder="Search services…"
        :filters="serviceFilters"
        :pagination-mode="tableMode"
        :page="tablePage"
        :page-size="tablePageSize"
        :query="tableQuery"
        :filter-values="filterValues"
        :cursor="tableCursor"
        :page-info="tablePageInfo"
        paginated
        empty-text="No services yet. Create one to expose its tools through MCP."
        @retry="refresh"
        @change="handleServiceTableChange"
        @row-click="(row) => openEdit(row as unknown as EdgeService)"
      >
        <template #name="{ value, row }">
          <span class="service-name">
            <a class="k-btn k-btn--ghost k-table-resource-link" :href="portalHref(`/ui/providers/edges/${serviceDetailPath(String(row.name))}`)" @click.stop="openServiceLink($event, row as unknown as EdgeService)">{{ value }}</a>
            <span class="service-mobile-status"><StatusBadge :status="String(row.status)" /></span>
          </span>
        </template>
        <template #edgeName="{ value }"><span class="muted">{{ value }}</span></template>
        <template #typeLabel="{ value }"><span class="mono muted">{{ value }}</span></template>
        <template #target="{ value }"><span class="mono muted">{{ value }}</span></template>
        <template #status="{ value }"><StatusBadge :status="String(value)" /></template>
        <template #credentials="{ row }"><span :class="['credentials-status', row.hasCredentials ? 'credentials-status--configured' : 'muted']"><Check v-if="row.hasCredentials" :size="16" class="ok-check" aria-hidden="true" /><span>{{ row.credentials }}</span></span></template>
        <template #actions="{ row }"><ActionMenu :label="`More actions for service ${String(row.name)}`" :busy="deletingService === row.name" :busy-label="`Deleting service ${String(row.name)}…`" :disabled="!!deletingService && deletingService !== row.name" :items="[{ id: 'edit', label: 'Edit service' }, { id: 'delete', label: 'Delete service', tone: 'danger' }]" @select="(action) => action === 'edit' ? openEdit(row as unknown as EdgeService) : onDelete(row as unknown as EdgeService)" /></template>
      </ResourceTable>
    </div>
  </div>
</template>
