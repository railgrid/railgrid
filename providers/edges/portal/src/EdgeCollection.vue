<script setup lang="ts">
import { computed, onActivated, ref } from 'vue'
import { Boxes, Plus, RefreshCw, Server } from 'lucide-vue-next'
import FirstRunGuide from './portalkit/FirstRunGuide.vue'
import ResourceTable from './portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from './portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import type { ResourceRefreshMode } from './refresh'
import type { Edge } from './types'

const props = defineProps<{
  edges: Edge[]
  loaded: boolean
  loading: boolean
  refreshMode: ResourceRefreshMode
  error: string | null
  foregroundLoading: boolean
}>()

const emit = defineEmits<{
  activated: []
  refresh: []
  open: [row: Record<string, unknown>]
  delete: [edge: Edge]
  connect: []
}>()

const edgeColumns = [
  { key: 'name', label: 'Name', primary: true },
  { key: 'typeLabel', label: 'Type' },
  { key: 'status', label: 'Status' },
  { key: 'agentVersion', label: 'Agent' },
  { key: 'lastHeartbeat', label: 'Last heartbeat' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]

const edgeRows = computed(() => props.edges.map(edge => ({
  ...edge,
  rowKey: `${edge.type}/${edge.name}`,
  typeLabel: edge.type === 'server' ? 'Server' : 'Kubernetes',
  status: edge.connected ? 'Connected' : (edge.phase || 'Disconnected'),
  agentVersion: edge.agentVersion || '—',
  lastHeartbeat: relativeTime(edge.lastHeartbeatTime),
  actions: '',
})))
const tableQuery = ref('')
const tableFilters = ref<Record<string, string>>({ typeLabel: '', status: '' })
const hasActiveTableFilters = computed(() => !!tableQuery.value.trim() || Object.values(tableFilters.value).some(Boolean))
const showFirstRun = computed(() => props.loaded && !props.error && props.edges.length === 0 && !hasActiveTableFilters.value)
const edgeJourney = [
  { label: 'Configure edge', description: 'Choose its workspace name, type, and scheduling labels.' },
  { label: 'Install agent', description: 'Run the generated Helm or CLI command with a one-time join token.' },
  { label: 'Connected', description: 'The agent opens its outbound tunnel and reports its version.' },
]

function edgeRowAriaLabel(row: Record<string, unknown>): string {
  return `Open ${row.type === 'server' ? 'server' : 'Kubernetes'} edge ${String(row.name)}`
}

function relativeTime(timestamp?: string): string {
  if (!timestamp) return '—'
  const date = new Date(timestamp).getTime()
  if (Number.isNaN(date)) return '—'
  const seconds = Math.max(0, Math.floor((Date.now() - date) / 1000))
  if (seconds < 60) return `${seconds}s ago`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

// App keeps this component alive while detail/connect routes are active. The
// table therefore retains its own query, filters, cursor, page, and scroll
// state; App revalidates the authoritative rows when the collection returns.
onActivated(() => emit('activated'))
</script>

<template>
  <div class="edges-app">
    <header class="edges-header">
      <div>
        <h1>Edges</h1>
        <p>Kubernetes clusters and Linux/SSH servers connected to this workspace.</p>
      </div>
      <div v-if="!showFirstRun" class="header-actions">
        <button class="k-btn k-btn--ghost" :disabled="props.foregroundLoading" @click="emit('refresh')">
          <RefreshCw :size="14" :class="{ spin: props.foregroundLoading }" /> {{ props.foregroundLoading ? 'Refreshing…' : 'Refresh' }}
        </button>
        <button class="k-btn k-btn--primary" @click="emit('connect')">
          <Plus :size="14" /> Connect edge
        </button>
      </div>
    </header>

    <FirstRunGuide
      v-if="showFirstRun"
      title="Connect your first edge"
      description="Connect a Kubernetes cluster or Linux server. The Faros agent dials out, so the target needs no inbound firewall rule, VPN, or public IP."
      primary-label="Connect edge"
      :steps="edgeJourney"
      journey-label="Edge connection path"
      @primary="emit('connect')"
    >
      <template #icon><Server aria-hidden="true" /></template>
    </FirstRunGuide>

    <ResourceTable
      v-else
      :columns="edgeColumns"
      :rows="edgeRows"
      aria-label="Edges"
      row-key="rowKey"
      :row-aria-label="edgeRowAriaLabel"
      :loaded="props.loaded"
      :loading="props.loading"
      :refresh-mode="props.refreshMode"
      :error="props.error"
      retryable
      searchable
      v-model:query="tableQuery"
      v-model:filter-values="tableFilters"
      search-placeholder="Search edges…"
      :filters="[{ key: 'typeLabel', label: 'Type' }, { key: 'status', label: 'Status', allLabel: 'Any status' }]"
      paginated
      :page-size="10"
      empty-text="No edges connected yet. Connect an edge to get started."
      @retry="emit('refresh')"
      @row-click="emit('open', $event)"
    >
      <template #name="{ value, row }"><button class="k-btn k-btn--ghost k-table-resource-link" type="button" @click.stop="emit('open', row)">{{ value }}</button></template>
      <template #typeLabel="{ value, row }"><span class="k-badge k-badge--muted"><component :is="row.type === 'server' ? Server : Boxes" :size="12" />{{ value }}</span></template>
      <template #status="{ value }"><StatusBadge :status="String(value)" /></template>
      <template #agentVersion="{ value }"><span class="mono muted">{{ value }}</span></template>
      <template #lastHeartbeat="{ value }"><span class="muted">{{ value }}</span></template>
      <template #actions="{ row }"><div class="row-actions"><ResourceTableDeleteButton :label="`Delete edge ${String(row.name)}`" @click="emit('delete', row as unknown as Edge)" /></div></template>
    </ResourceTable>
  </div>
</template>
