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

const edges = ref<Edge[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
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

async function refresh() {
  loading.value = true
  error.value = null
  try {
    edges.value = await listEdges()
    // Auto-open the wizard the first time we confirm the workspace has no edges.
    if (!firstLoadDone.value) {
      firstLoadDone.value = true
      if (edges.value.length === 0) wizardOpen.value = true
    }
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Failed to load edges'
  } finally {
    loading.value = false
  }
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
  () => [props.ctx?.token, props.ctx?.tenant] as const,
  ([token, tenant]) => {
    contextGeneration.value += 1
    setToken(token ?? null)
    setTenant(tenant ?? null)
    if (tenant) refresh()
  },
  { immediate: true },
)

// Light polling so status/connected updates without a manual refresh.
const timer = setInterval(() => {
  if (props.ctx?.tenant && !loading.value) refresh()
}, 10000)
onUnmounted(() => clearInterval(timer))

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
    <Tabs
      v-if="!wizardOpen && !selected"
      :tabs="edgeRouteTabs"
      :active="view"
      aria-label="Edges sections"
      @select="(id) => navigate(id === 'edges' ? '' : id)"
    />

    <!-- Workloads view. -->
    <Workloads v-if="view === 'workloads' && !wizardOpen && !selected" />

    <!-- Services view. -->
    <Services v-else-if="view === 'services' && !wizardOpen && !selected" />

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
        <button class="k-btn k-btn--ghost" :disabled="loading" @click="refresh">
          <RefreshCw :size="14" :class="{ spin: loading }" /> Refresh
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
