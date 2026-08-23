<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { RefreshCw, Plus, Check } from 'lucide-vue-next'
import {
  listServices, listServicesPage, createKubeEdgeService, deleteEdgeService, listEdges,
  fetchServiceCatalog,
} from './api'
import type { CatalogEntry } from './api'
import type { EdgeService, EdgeServiceDraft, Edge, ErrorResponse } from './types'
import { confirmDialog } from './portalkit/confirm'
import ResourceTable from './portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from './portalkit/ResourceTableDeleteButton.vue'
import ResourceTableEditButton from './portalkit/ResourceTableEditButton.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import ServiceEdit from './ServiceEdit.vue'
import { isCompleteFirstCursorPage, type ResourceTableChange, type TableFilterDefinition, type TablePageInfo } from './portalkit/table'
import { createFullListReadCoordinator, createInFlightReadCoordinator, hasActiveTableFilters, sameTableRequest, tablePageInfo as makeTablePageInfo, type PaginationMode, type TableRequestState } from './pagination'

// Service type catalog — fetched from the backend (svccatalog.All()) so the form
// never drifts from the provider's auth/probe knowledge. Each entry seeds the
// port/scheme and describes the credential fields the UI collects.
const catalog = ref<CatalogEntry[]>([])
function catalogFor(t?: string): CatalogEntry | undefined {
  return catalog.value.find((c) => c.type === t)
}
// Types grouped by category (first-seen order) for <optgroup> rendering.
const CATALOG_GROUPS = computed(() =>
  catalog.value.reduce<{ category: string; items: CatalogEntry[] }[]>((groups, c) => {
    const cat = c.category || 'Other'
    let g = groups.find((x) => x.category === cat)
    if (!g) { g = { category: cat, items: [] }; groups.push(g) }
    g.items.push(c)
    return groups
  }, []),
)
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
// schemeLocked types (e.g. UniFi is always https) pin the scheme select.
const createSchemeLocked = computed(() => !!catalogFor(draft.value.serviceType)?.schemeLocked)
function onTypeChange() {
  const c = catalogFor(draft.value.serviceType)
  if (c) {
    if (c.defaultPort) draft.value.port = c.defaultPort
    if (c.defaultScheme) draft.value.scheme = c.defaultScheme
  }
  resetTargetMode()
}

const services = ref<EdgeService[]>([])
const edges = ref<Edge[]>([])
const loading = ref(true)
const loaded = ref(false)
const error = ref<string | null>(null)
const serviceColumns = [
  { key: 'name', label: 'Name' },
  { key: 'edgeName', label: 'Edge' },
  { key: 'typeLabel', label: 'Type' },
  { key: 'target', label: 'Target' },
  { key: 'status', label: 'Status' },
  { key: 'credentials', label: 'Creds' },
  { key: 'actions', label: '' },
]
const serviceRows = computed<Array<Record<string, unknown>>>(() => services.value.map(service => ({
  ...service,
  edgeName: service.edgeName || '—',
  typeLabel: catalogFor(service.serviceType)?.displayName || service.serviceType || '—',
  target: `${service.host || `${service.targetNamespace ? `${service.targetNamespace}/` : ''}${service.targetName || '—'}`}:${service.port || ''}`,
  status: service.phase || 'Pending',
  credentials: service.hasCredentials ? 'Configured' : 'Missing',
  actions: '',
})))

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

const showCreate = ref(false)
const busy = ref(false)
const draft = ref<EdgeServiceDraft>({
  name: '',
  edgeName: '',
  serviceType: 'home-assistant',
  targetNamespace: '',
  targetName: '',
  scheme: 'http',
  host: '',
  port: 8123,
  instructions: '',
})

// How the service is reached is an explicit choice, independent of the edge kind:
//   host — dial an address directly (agent loopback, or a LAN device like a UniFi
//          console). Works on either edge kind.
//   kube — reach a named Kubernetes Service by cluster DNS (KubernetesCluster only).
const targetMode = ref<'host' | 'kube'>('host')

const selectedEdgeIsServer = computed(
  () => edges.value.find((e) => e.name === draft.value.edgeName)?.type === 'server',
)

// Sensible default target mode when the edge or type changes: LAN-style services
// (UniFi) and LinuxServer edges default to host; KubernetesCluster edges default
// to a cluster Service. The user can override with the toggle.
function resetTargetMode() {
  // Types flagged hostRequired live on the edge LAN (e.g. a UniFi console), not
  // on the agent loopback, so they default to host addressing.
  const needsHost = !!catalogFor(draft.value.serviceType)?.hostRequired
  targetMode.value = selectedEdgeIsServer.value || needsHost ? 'host' : 'kube'
}

function toggleCreate() {
  showCreate.value = !showCreate.value
  if (showCreate.value) resetTargetMode()
}

// spec.host is a bare hostname/IP (scheme + port are separate fields). If the
// user pastes a full URL, split it into host + scheme + port for convenience.
// Any path is dropped — services proxy at the root.
function applyHostUrl() {
  const raw = draft.value.host?.trim()
  if (!raw || !/^https?:\/\//i.test(raw)) return
  try {
    const u = new URL(raw)
    draft.value.scheme = u.protocol.replace(':', '')
    draft.value.host = u.hostname
    draft.value.port = Number(u.port) || (u.protocol === 'https:' ? 443 : 80)
  } catch {
    /* not a valid URL — leave the field as typed */
  }
}

// Edit opens a dedicated per-service page (ServiceEdit.vue) that hosts the
// provider info, config, credentials and status. Held as local state (no shell
// router), the same way App.vue drives the Edges Detail view.
const editing = ref<EdgeService | null>(null)
function openEdit(s: EdgeService) {
  editing.value = s
}
// After a save in the edit page, refresh the list and re-seed the open page from
// the fresh object so status/conditions update in place.
async function onEditSaved() {
  const name = editing.value?.name
  await refresh(true)
  if (name) editing.value = services.value.find((s) => s.name === name) ?? null
}

type ServiceTableRequest = Omit<TableRequestState, 'filters'> & { filters: ServiceFilterValues }

let latestRefreshID = 0
let stopped = false
let clientReadToken = 0

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
  return !stopped && latestRefreshID === requestID && sameTableRequest(current, request)
}

// A complete client read is query-independent. Query/filter-only changes are
// allowed to update the controlled table state while its target + edge join is
// pending; page, page-size, mode, mutation, and context changes still fail
// this guard through the stable request dimensions below.
function serviceClientReadIsCurrent(requestID: number, request: ServiceTableRequest, readGeneration: number): boolean {
  const current = currentServiceRequest()
  return !stopped && latestRefreshID === requestID && tableMode.value === 'client' &&
    current.active && current.page === request.page &&
    current.pageSize === request.pageSize && current.cursor === request.cursor &&
    completeServiceRead.generation() === readGeneration && clientAuthorityReady.value === false
}

// The optional event union keeps direct template @click/@retry bindings
// type-safe while mutation callers can request a forced client read.
async function refresh(forceClientRead: boolean | Event = false) {
  const requestID = ++latestRefreshID
  const readToken = ++clientReadToken
  const request = currentServiceRequest()
  const wasClientAuthorityReady = clientAuthorityReady.value
  const forceCompleteRead = request.mode === 'client' && (wasClientAuthorityReady || forceClientRead === true)
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
  loading.value = true
  error.value = null
  // Do not render an unfiltered page as though it matched a newly-entered
  // query/filter. Same-page polling keeps cached rows visible until the
  // replacement arrives.
  if (request.active && request.mode === 'server') {
    services.value = []
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
      edges.value = nextEdges
      tableCursor.value = request.cursor
      tablePageInfo.value = makeTablePageInfo(nextPage.continue)
    }
    loaded.value = true
    error.value = null
    if (!draft.value.edgeName && edges.value.length) draft.value.edgeName = edges.value[0].name
  } catch (e) {
    const current = request.mode === 'client' && request.active
      ? serviceClientReadIsCurrent(requestID, request, readGeneration)
      : serviceRequestIsCurrent(requestID, request)
    if (!current) return
    error.value = (e as ErrorResponse)?.message ?? 'Failed to load services'
  } finally {
    if (clientReadToken === readToken) clientReadPending.value = false
    if (!stopped && latestRefreshID === requestID) loading.value = false
  }
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
  void refresh()
}

const canCreate = computed(
  () =>
    !!draft.value.name.trim() &&
    !!draft.value.edgeName &&
    // host mode: host optional (empty = agent loopback). kube mode: need a target.
    (targetMode.value === 'host' || !!draft.value.targetName.trim()),
)

async function onCreate() {
  if (!canCreate.value) return
  if (targetMode.value === 'host') applyHostUrl() // normalize a pasted URL
  busy.value = true
  error.value = null
  try {
    const byHost = targetMode.value === 'host'
    await createKubeEdgeService({
      name: draft.value.name.trim(),
      edgeName: draft.value.edgeName,
      edgeKind: selectedEdgeIsServer.value ? 'LinuxServer' : 'KubernetesCluster',
      serviceType: draft.value.serviceType,
      targetNamespace: draft.value.targetNamespace.trim() || 'default',
      targetName: byHost ? '' : draft.value.targetName.trim(),
      scheme: draft.value.scheme || 'http',
      host: byHost ? draft.value.host?.trim() || undefined : undefined,
      port: Number(draft.value.port) || 8123,
      instructions: draft.value.instructions?.trim() || undefined,
    })
    showCreate.value = false
    draft.value = { name: '', edgeName: edges.value[0]?.name ?? '', serviceType: 'home-assistant', targetNamespace: '', targetName: '', scheme: 'http', host: '', port: 8123, instructions: '' }
    await refresh(true)
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Create failed'
  } finally {
    busy.value = false
  }
}

async function onDelete(s: EdgeService) {
  if (!(await confirmDialog({ title: `Delete service "${s.name}"?`, message: 'Its MCP tools stop being exposed.', danger: true, confirmLabel: 'Delete' }))) return
  try {
    await deleteEdgeService(s.name)
    await refresh(true)
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Delete failed'
  }
}

onMounted(() => {
  loadCatalog()
  refresh()
})
const timer = setInterval(refresh, 10000)
onUnmounted(() => {
  stopped = true
  latestRefreshID += 1
  clearInterval(timer)
})

function serviceRowAriaLabel(row: Record<string, unknown>): string {
  return `Open service ${String(row.name)}`
}
</script>

<template>
  <ServiceEdit
    v-if="editing"
    :service="editing"
    :catalog="catalog"
    :edges="edges"
    @back="editing = null"
    @saved="onEditSaved"
  />
  <div v-else class="edges-app">
    <header class="edges-header">
      <div>
        <h1>Services</h1>
        <p>Services running next to your edges (e.g. Home Assistant). Attach a token to make one Ready, and give it AI guidance — its tools appear in the MCP endpoint.</p>
      </div>
      <div class="header-actions">
        <button class="k-btn k-btn--ghost" :disabled="loading" @click="refresh">
          <RefreshCw :size="14" :class="{ spin: loading }" /> Refresh
        </button>
        <button
          type="button"
          class="k-btn k-btn--primary"
          :aria-expanded="showCreate"
          aria-controls="edges-service-create"
          @click="toggleCreate"
        >
          <Plus :size="14" aria-hidden="true" /> New service
        </button>
      </div>
    </header>

    <!-- Create form -->
    <div v-if="showCreate" id="edges-service-create" class="wiz-card k-card">
      <h3>New service</h3>
      <div class="row" style="gap: 12px; align-items: flex-start;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Name</span>
          <input v-model="draft.name" class="k-input" placeholder="ha" />
        </label>
        <label class="fld" style="flex: 1;">
          <span class="lbl">Edge</span>
          <select v-model="draft.edgeName" class="k-input" @change="resetTargetMode">
            <option v-for="e in edges" :key="e.name" :value="e.name">{{ e.name }} ({{ e.type === 'server' ? 'LinuxServer' : 'KubernetesCluster' }})</option>
          </select>
        </label>
      </div>
      <div class="row" style="gap: 12px; align-items: flex-start;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Type</span>
          <select v-model="draft.serviceType" class="k-input" @change="onTypeChange">
            <optgroup v-for="g in CATALOG_GROUPS" :key="g.category" :label="g.category">
              <option v-for="c in g.items" :key="c.type" :value="c.type">{{ c.displayName }}</option>
            </optgroup>
          </select>
        </label>
        <label class="fld" style="flex: 0 0 120px;">
          <span class="lbl">Scheme</span>
          <select v-model="draft.scheme" class="k-input" :disabled="createSchemeLocked" :title="createSchemeLocked ? 'Fixed by the service type' : ''">
            <option value="http">http</option>
            <option value="https">https</option>
          </select>
        </label>
        <label class="fld" style="flex: 0 0 120px;">
          <span class="lbl">Port</span>
          <input v-model="draft.port" type="number" min="1" max="65535" class="k-input" />
        </label>
      </div>
      <!-- Target: an explicit choice, independent of the edge kind. -->
      <label class="fld">
        <span class="lbl">Target</span>
        <div class="row" style="gap: 16px;">
          <label style="display: flex; align-items: center; gap: 6px; cursor: pointer;">
            <input type="radio" value="host" v-model="targetMode" /> Host / IP
          </label>
          <label style="display: flex; align-items: center; gap: 6px; cursor: pointer;" :style="{ opacity: selectedEdgeIsServer ? 0.5 : 1 }">
            <input type="radio" value="kube" v-model="targetMode" :disabled="selectedEdgeIsServer" /> Kubernetes Service
          </label>
        </div>
      </label>
      <!-- Host: dial an address directly (loopback, or a LAN device like UniFi). -->
      <div v-if="targetMode === 'host'" class="row" style="gap: 12px; align-items: flex-start;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Host {{ catalogFor(draft.serviceType)?.hostRequired ? '(required)' : '(blank = agent loopback)' }}</span>
          <input v-model="draft.host" class="k-input" @blur="applyHostUrl" placeholder="192.168.1.1, myui.example.com, or paste https://myui.example.com — blank = 127.0.0.1" />
          <span v-if="catalogFor(draft.serviceType)?.hostHelp" class="muted" style="font-size: 12px; margin-top: 4px;">{{ catalogFor(draft.serviceType)?.hostHelp }}</span>
        </label>
      </div>
      <!-- Kubernetes Service: reach it by cluster DNS. -->
      <div v-else class="row" style="gap: 12px; align-items: flex-start;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Target namespace</span>
          <input v-model="draft.targetNamespace" class="k-input" placeholder="home" />
        </label>
        <label class="fld" style="flex: 1;">
          <span class="lbl">Target service name</span>
          <input v-model="draft.targetName" class="k-input" placeholder="home-assistant" />
        </label>
      </div>
      <label class="fld">
        <span class="lbl">AI instructions (optional)</span>
        <textarea v-model="draft.instructions" class="k-input" rows="3" placeholder="Gates are cover.gate_main. Living room light is light.living_room."></textarea>
      </label>
      <div class="wiz-actions">
        <button class="k-btn k-btn--ghost" @click="showCreate = false">Cancel</button>
        <button class="k-btn k-btn--primary" :disabled="busy || !canCreate" @click="onCreate">Create</button>
      </div>
    </div>

    <ResourceTable
      :columns="serviceColumns"
      :rows="serviceRows"
      row-key="name"
      :row-aria-label="serviceRowAriaLabel"
      :loaded="loaded"
      :loading="loading"
      :error="error"
      :stale="loaded && !!error"
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
      <template #name="{ value }"><span class="name">{{ value }}</span></template>
      <template #edgeName="{ value }"><span class="muted">{{ value }}</span></template>
      <template #typeLabel="{ value }"><span class="mono muted">{{ value }}</span></template>
      <template #target="{ value }"><span class="mono muted">{{ value }}</span></template>
      <template #status="{ value }"><StatusBadge :status="String(value)" /></template>
      <template #credentials="{ row }"><Check v-if="row.hasCredentials" :size="16" class="ok-check" /><span v-else class="muted">—</span></template>
      <template #actions="{ row }"><div class="row-actions"><ResourceTableEditButton :label="`Edit service ${String(row.name)}`" @click="openEdit(row as unknown as EdgeService)" /><ResourceTableDeleteButton :label="`Delete service ${String(row.name)}`" @click="onDelete(row as unknown as EdgeService)" /></div></template>
    </ResourceTable>
  </div>
</template>
