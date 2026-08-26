<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import { Server, Boxes, RefreshCw, Plus, Plug } from 'lucide-vue-next'
import { setToken, setTenant, listEdges, deleteEdge } from './api'
import Wizard from './Wizard.vue'
import Detail from './Detail.vue'
import Workloads from './Workloads.vue'
import Services from './Services.vue'
import ConfirmDialog from './portalkit/ConfirmDialog.vue'
import ResourceTable from './portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from './portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import Tabs from './portalkit/Tabs.vue'
import { confirmDialog } from './portalkit/confirm'
import {
  FAST_REFRESH_MS,
  STABLE_REFRESH_MS,
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  type ResourceRefreshMode,
} from './refresh'
import type { Edge, EdgeType, FarosContext, ErrorResponse } from './types'

const props = defineProps<{ ctx: FarosContext | null }>()

// Top-level view is driven by the shell route: /providers/edges → the edges
// fleet; /providers/edges/workloads → the workloads scheduled across them. The
// sidebar renders both as nav items (CatalogEntry ui.children), so switching
// happens via the menu; the in-page toggle mirrors it through navigate().
const view = computed<'edges' | 'workloads' | 'services'>(() => {
  const sub = props.ctx?.subPath ?? ''
  if (sub.startsWith('workloads')) return 'workloads'
  if (sub.startsWith('services')) return 'services'
  return 'edges'
})

// Services use a single URL-owned instance segment in addition to the
// existing list route. Keep the encoded segment intact when decoding fails so
// a malformed deep link reaches the instance read and reports not-found/error
// state instead of silently falling back to the list.
const selectedServiceName = computed<string | null>(() => {
  const sub = props.ctx?.subPath ?? ''
  if (!sub.startsWith('services/')) return null
  const encodedName = sub.slice('services/'.length)
  if (!encodedName) return ''
  try {
    return decodeURIComponent(encodedName)
  } catch {
    return encodedName
  }
})
const serviceDetailRoute = computed(() => selectedServiceName.value !== null)

const edgeRouteTabs = [
  { id: 'edges', label: 'Edges', icon: Server },
  { id: 'workloads', label: 'Workloads', icon: Boxes },
  { id: 'services', label: 'Services', icon: Plug },
] as const

// navigate pushes the shell's router via a bubbling CustomEvent the element's
// ProviderFrame host listens for. path is the trailing segment appended to
// /providers/edges/ (empty = the edges list).
const rootRef = ref<HTMLElement | null>(null)
function navigate(path: string) {
  rootRef.value?.dispatchEvent(new CustomEvent('faros-navigate', { detail: { path }, bubbles: true }))
}

function onServiceNavigate(name: string | null): void {
  navigate(name === null ? 'services' : `services/${encodeURIComponent(name)}`)
}

// The wizard shows automatically on first load when the workspace has no edges,
// and on demand via "Connect edge". It closes back to the list on completion.
const wizardOpen = ref(false)
const firstLoadDone = ref(false)

// Selected edge → detail view. Null = list.
const selected = ref<{ name: string; type: EdgeType } | null>(null)

function openDetail(e: Edge) {
  selected.value = { name: e.name, type: e.type }
}
function closeDetail() {
  selected.value = null
  refresh()
}

// The shell sidebar (Workloads/Services nav items) changes the route while
// the detail or wizard overlay is open. The route must win: the template
// gates every view on !selected/!wizardOpen, so without closing them here
// the overlay keeps rendering and the sidebar appears dead.
watch(view, (v) => {
  selected.value = null
  wizardOpen.value = false
  if (v === 'edges') refresh()
})
watch(serviceDetailRoute, (isDetail) => {
  if (isDetail) wizardOpen.value = false
})

const edges = ref<Edge[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const foregroundLoading = computed(() => loading.value && refreshMode.value === 'foreground')
// Nested list views keep their own cursor/cache authority. Remount them when
// the shell changes tenant or token so a prior workspace's rows and cursors
// cannot remain visible while the new context is loading.
const contextGeneration = ref(0)
const edgeColumns = [
  { key: 'name', label: 'Name' },
  { key: 'typeLabel', label: 'Type' },
  { key: 'status', label: 'Status' },
  { key: 'agentVersion', label: 'Agent' },
  { key: 'lastHeartbeat', label: 'Last heartbeat' },
  { key: 'actions', label: '' },
]
const edgeRows = computed<Array<Record<string, unknown>>>(() => edges.value.map(edge => ({
  ...edge,
  rowKey: `${edge.type}/${edge.name}`,
  typeLabel: edge.type === 'server' ? 'Server' : 'Kubernetes',
  status: edge.connected ? 'Connected' : (edge.phase || 'Disconnected'),
  agentVersion: edge.agentVersion || '—',
  lastHeartbeat: rel(edge.lastHeartbeatTime),
  actions: '',
})))

const poller = createAdaptiveRefreshTimer(() => {
  if (props.ctx?.tenant) void refresh('background')
}, () => {
  if (!firstLoadDone.value || error.value) return FAST_REFRESH_MS
  const unsettled = edges.value.some(edge => !edge.connected || ['pending', 'provisioning', 'deleting'].includes((edge.phase || '').toLowerCase()))
  return unsettled ? FAST_REFRESH_MS : STABLE_REFRESH_MS
})

const edgeRefresh = createLatestRefreshController(async (requestID, mode) => {
  refreshMode.value = mode
  loading.value = true
  if (mode === 'foreground') error.value = null
  try {
    const nextEdges = await listEdges()
    if (!edgeRefresh.isCurrent(requestID)) return
    edges.value = nextEdges
    error.value = null
    // Auto-open the wizard the first time we confirm the workspace has no edges.
    if (!firstLoadDone.value) {
      firstLoadDone.value = true
      if (edges.value.length === 0 && !serviceDetailRoute.value) wizardOpen.value = true
    }
  } catch (e) {
    if (!edgeRefresh.isCurrent(requestID)) return
    error.value = (e as ErrorResponse)?.message ?? 'Failed to load edges'
  } finally {
    if (edgeRefresh.isCurrent(requestID)) loading.value = false
    poller.schedule()
  }
})

function refresh(mode: ResourceRefreshMode | Event = 'foreground') {
  const requestedMode = typeof mode === 'string' ? mode : 'foreground'
  if (requestedMode === 'foreground') {
    refreshMode.value = 'foreground'
    loading.value = true
  }
  return edgeRefresh.request(requestedMode)
}

function onWizardDone() {
  wizardOpen.value = false
  refresh()
}

async function onDelete(edge: Edge) {
  if (!(await confirmDialog({ title: `Delete ${edge.type === 'server' ? 'server' : 'cluster'} "${edge.name}"?`, danger: true, confirmLabel: 'Delete' }))) return
  try {
    await deleteEdge(edge)
    await refresh()
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Delete failed'
  }
}

// Re-auth + reload whenever the shell pushes a new context (token/workspace).
watch(
  () => [props.ctx?.token, props.ctx?.tenant, props.ctx?.user?.sub] as const,
  ([token, tenant, userSub], previous) => {
    const authorityChanged = !previous || tenant !== previous[1] || userSub !== previous[2]
    setToken(token ?? null)
    setTenant(tenant ?? null)
    if (authorityChanged) {
      contextGeneration.value += 1
      edgeRefresh.invalidate()
      edges.value = []
      firstLoadDone.value = false
      loading.value = Boolean(tenant)
      error.value = null
      if (tenant) void refresh('foreground')
      return
    }
    // Token rotation within the same user/workspace is a credential refresh,
    // not a resource identity change. Preserve the authoritative snapshot and
    // quietly revalidate it with the new token.
    if (tenant) void refresh('background')
  },
  { immediate: true },
)

onUnmounted(() => {
  edgeRefresh.stop()
  poller.stop()
})

function rel(ts?: string): string {
  if (!ts) return '—'
  const d = new Date(ts).getTime()
  if (Number.isNaN(d)) return '—'
  const secs = Math.max(0, Math.floor((Date.now() - d) / 1000))
  if (secs < 60) return `${secs}s ago`
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`
  return `${Math.floor(secs / 86400)}d ago`
}

function edgeRowAriaLabel(row: Record<string, unknown>): string {
  return `Open ${row.type === 'server' ? 'server' : 'Kubernetes'} edge ${String(row.name)}`
}
</script>

<template>
  <div ref="rootRef" class="edges-app" :key="contextGeneration">
    <!-- Section nav: Edges | Workloads | Services. Mirrors the sidebar's sub-nav
         items and pushes the shell route via navigate(). Hidden while the wizard
         or a detail view is open so those flows stay focused. -->
    <template v-if="!serviceDetailRoute">
      <Tabs
        v-if="!wizardOpen && !selected"
        :tabs="edgeRouteTabs"
        :active="view"
        aria-label="Edges sections"
        @select="(id) => navigate(id === 'edges' ? '' : id)"
      />
    </template>

    <!-- Workloads view. -->
    <Workloads v-if="view === 'workloads' && !wizardOpen && !selected" />

    <!-- Services view. -->
    <!-- The original self-closing list shape remains documented for the
         conformance contract; the two concrete branches below keep the list
         and URL-owned instance paths mutually exclusive. -->
    <!-- <Services v-else-if="view === 'services' && !wizardOpen && !selected" /> -->
    <Services
      v-else-if="view === 'services' && !wizardOpen && !selected && !serviceDetailRoute"
      @navigate="onServiceNavigate"
    />
    <Services
      v-else-if="view === 'services' && !wizardOpen && !selected && serviceDetailRoute"
      :selected-name="selectedServiceName"
      @navigate="onServiceNavigate"
    />

    <!-- Onboarding / add-edge wizard (shown on first load when empty, or on demand). -->
    <Wizard v-else-if="wizardOpen" :cluster="props.ctx?.tenant ?? null" @connected="onWizardDone" />

    <!-- Per-edge detail view. -->
    <Detail
      v-else-if="selected"
      :name="selected.name"
      :type="selected.type"
      :cluster="props.ctx?.tenant ?? null"
      :token="props.ctx?.token ?? null"
      @back="closeDetail"
      @deleted="closeDetail"
    />

    <template v-else>
    <header class="edges-header">
      <div>
        <h1>Edges</h1>
        <p>Kubernetes clusters and Linux/SSH servers connected to this workspace.</p>
      </div>
      <div class="header-actions">
        <button class="k-btn k-btn--ghost" :disabled="foregroundLoading" @click="refresh">
          <RefreshCw :size="14" :class="{ spin: foregroundLoading }" /> {{ foregroundLoading ? 'Refreshing…' : 'Refresh' }}
        </button>
        <button class="k-btn k-btn--primary" @click="wizardOpen = true">
          <Plus :size="14" /> Connect edge
        </button>
      </div>
    </header>

    <ResourceTable
      :columns="edgeColumns"
      :rows="edgeRows"
      row-key="rowKey"
      :row-aria-label="edgeRowAriaLabel"
      :loaded="firstLoadDone"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      retryable
      searchable
      search-placeholder="Search edges…"
      :filters="[{ key: 'typeLabel', label: 'Type' }, { key: 'status', label: 'Status', allLabel: 'Any status' }]"
      paginated
      :page-size="10"
      empty-text="No edges connected yet. Connect an edge to get started."
      @retry="refresh"
      @row-click="(row) => openDetail(row as unknown as Edge)"
    >
      <template #name="{ value }"><span class="name">{{ value }}</span></template>
      <template #typeLabel="{ value, row }"><span class="k-badge k-badge--muted"><component :is="row.type === 'server' ? Server : Boxes" :size="12" />{{ value }}</span></template>
      <template #status="{ value }"><StatusBadge :status="String(value)" /></template>
      <template #agentVersion="{ value }"><span class="mono muted">{{ value }}</span></template>
      <template #lastHeartbeat="{ value }"><span class="muted">{{ value }}</span></template>
      <template #actions="{ row }"><div class="row-actions"><ResourceTableDeleteButton :label="`Delete edge ${String(row.name)}`" @click="onDelete(row as unknown as Edge)" /></div></template>
    </ResourceTable>
    </template>
    <ConfirmDialog />
  </div>
</template>
