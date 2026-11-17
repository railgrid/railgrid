<script setup lang="ts">
import { computed, nextTick, onActivated, ref, watch } from 'vue'
import { ArrowRight, Boxes, Laptop, Plus, RefreshCw, Server } from 'lucide-vue-next'
import FirstRunGuide from './portalkit/FirstRunGuide.vue'
import InlineNotification from './portalkit/InlineNotification.vue'
import ResourceTable from './portalkit/ResourceTable.vue'
import ResourceTimestamp from './portalkit/ResourceTimestamp.vue'
import ResourceTableDeleteButton from './portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import { portalHref } from './portalkit/navigation'
import { edgeConnectPath, edgeDetailPath } from './routes'
import type { ResourceRefreshMode } from './refresh'
import { edgeTypeLabel, type Edge } from './types'

const props = defineProps<{
  edges: Edge[]
  loaded: boolean
  loading: boolean
  refreshMode: ResourceRefreshMode
  error: string | null
  deleteFailures?: Record<string, { name: string; message: string }>
  deletingEdge?: string | null
  foregroundLoading: boolean
}>()

const emit = defineEmits<{
  activated: []
  refresh: []
  open: [row: Record<string, unknown>]
  delete: [edge: Edge]
  dismissDeleteFailure: [key: string]
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
  typeLabel: edge.type === 'macos' ? 'macOS' : edgeTypeLabel(edge.type),
  status: edge.connected ? 'Connected' : (edge.phase || 'Disconnected'),
  agentVersion: edge.agentVersion || '—',
  lastHeartbeat: edge.lastHeartbeatTime || null,
  actions: '',
})))
const tableQuery = ref('')
const tableFilters = ref<Record<string, string>>({ typeLabel: '', status: '' })
const hasActiveTableFilters = computed(() => !!tableQuery.value.trim() || Object.values(tableFilters.value).some(Boolean))
const showFirstRun = computed(() => props.loaded && props.edges.length === 0 && !hasActiveTableFilters.value)
const collection = ref<HTMLElement | null>(null)
const heading = ref<HTMLElement | null>(null)
const firstRunWarning = ref<HTMLElement | null>(null)
const refreshButton = ref<HTMLButtonElement | null>(null)
const headerRefreshPending = ref(false)
const headerRefreshBusy = computed(() => props.foregroundLoading || headerRefreshPending.value)
watch(() => props.foregroundLoading, loading => { if (!loading) headerRefreshPending.value = false }, { flush: 'sync' })
watch(() => showFirstRun.value && !!props.error, visible => {
  if (visible || typeof document === 'undefined' || !firstRunWarning.value?.contains(document.activeElement)) return
  void nextTick(() => { if (document.activeElement === document.body) heading.value?.focus({ preventScroll: true }) })
}, { flush: 'pre' })
watch(showFirstRun, visible => {
  if (!visible || typeof document === 'undefined') return
  const focused = document.activeElement
  if (focused !== refreshButton.value && !collection.value?.querySelector('.k-table--resource')?.contains(focused)) return
  void nextTick(() => {
    if (focused && !focused.isConnected && document.activeElement === document.body) heading.value?.focus()
  })
})
const edgeJourney = [
  { label: 'Configure edge', description: 'Choose its workspace name, type, and scheduling labels.' },
  { label: 'Install agent', description: 'Run the generated Helm or CLI command with a one-time join token.' },
  { label: 'Connected', description: 'The agent opens its outbound tunnel and reports its version.' },
]

function edgeRowAriaLabel(row: Record<string, unknown>): string {
  const type = String(row.typeLabel)
  return `Open ${type} edge ${String(row.name)}`
}

function edgeHref(row: Record<string, unknown>): string {
  const type = row.type === 'server' ? 'server' : row.type === 'macos' ? 'macos' : 'kubernetes'
  return portalHref(`/ui/providers/edges/${edgeDetailPath(type, String(row.name))}`)
}

function openEdgeLink(event: MouseEvent, row: Record<string, unknown>): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  emit('open', row)
}

function connectHref(): string {
  return portalHref(`/ui/providers/edges/${edgeConnectPath()}`)
}

function followConnectLink(event: MouseEvent): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  emit('connect')
}

function refreshFromHeader(): void {
  if (headerRefreshBusy.value) return
  headerRefreshPending.value = true
  emit('refresh')
}

// App keeps this component alive while detail/connect routes are active. The
// table therefore retains its own query, filters, cursor, page, and scroll
// state; App revalidates the authoritative rows when the collection returns.
onActivated(() => emit('activated'))

async function dismissDeleteFailure(key: string) {
  emit('dismissDeleteFailure', key)
  await nextTick()
  heading.value?.focus()
}
</script>

<template>
  <div ref="collection" class="edges-app">
    <header class="edges-header">
      <div>
        <h1 ref="heading" tabindex="-1">Edges</h1>
        <p>Kubernetes clusters and Linux and MacOS hosts connected to this workspace.</p>
      </div>
      <div v-if="(props.loaded || props.error) && !showFirstRun" class="header-actions">
        <button ref="refreshButton" type="button" class="k-btn k-btn--ghost" aria-label="Refresh edges" :aria-disabled="headerRefreshBusy || undefined" :aria-busy="headerRefreshBusy || undefined" @click="refreshFromHeader">
          <RefreshCw :size="14" :class="{ spin: headerRefreshBusy }" aria-hidden="true" /> {{ headerRefreshBusy ? 'Refreshing…' : 'Refresh' }}
        </button>
        <a class="k-btn k-btn--primary" :href="connectHref()" @click="followConnectLink">
          <Plus :size="14" aria-hidden="true" /> Connect edge
        </a>
      </div>
    </header>

    <InlineNotification
      v-for="(failure, key) in props.deleteFailures" :key="key"
      tone="error"
      :title="`Could not delete ${failure.name}`"
      :message="`${failure.message} Try again using the edge's delete action.`"
      dismissible @dismiss="dismissDeleteFailure(String(key))"
    />
    <div v-if="showFirstRun && props.error" ref="firstRunWarning">
      <InlineNotification tone="warning" message="Edges could not be refreshed. Showing the last successful empty result." action-label="Retry" action-busy-label="Retrying…" :action-busy="props.loading" @action="emit('refresh')" />
    </div>
    <FirstRunGuide
      v-if="showFirstRun"
      title="Connect your first edge"
      description="Connect a Kubernetes cluster or a Linux or MacOS host. The Railgrid agent dials out, so the target needs no inbound firewall rule, VPN, or public IP."
      primary-label="Connect edge"
      :steps="edgeJourney"
      journey-label="Edge connection path"
    >
      <template #icon><Server aria-hidden="true" /></template>
      <template #actions><a class="k-btn k-btn--primary" :href="connectHref()" @click="followConnectLink">Connect edge <ArrowRight :stroke-width="1.75" aria-hidden="true" /></a></template>
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
      :stale="props.loaded && !!props.error"
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
      <template #name="{ value, row }"><a class="k-btn k-btn--ghost k-table-resource-link" :href="edgeHref(row)" @click.stop="openEdgeLink($event, row)">{{ value }}</a></template>
      <template #typeLabel="{ value, row }"><span class="k-badge k-badge--muted k-badge--label"><component :is="row.type === 'server' ? Server : row.type === 'macos' ? Laptop : Boxes" :size="12" aria-hidden="true" />{{ value }}</span></template>
      <template #status="{ value }"><StatusBadge :status="String(value)" /></template>
      <template #agentVersion="{ value }"><span class="mono muted">{{ value }}</span></template>
      <template #lastHeartbeat="{ value }"><ResourceTimestamp :value="value ? String(value) : null" label="Last heartbeat" /></template>
      <template #actions="{ row }"><div class="row-actions"><ResourceTableDeleteButton :label="`Delete edge ${String(row.name)}`" :disabled="!!props.deletingEdge" :busy="props.deletingEdge === row.rowKey" @click="emit('delete', row as unknown as Edge)" /></div></template>
    </ResourceTable>
  </div>
</template>
