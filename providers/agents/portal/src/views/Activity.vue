<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Check, Clock, Inbox, MessageCircle, RefreshCw, X } from 'lucide-vue-next'
import type { ApiClient, RunFilter } from '../api'
import type { AppStore, ServerEvent } from '../store'
import { fmtDuration, fmtTime, fmtTokens, fmtUSD, type InboxItem, type RunPhase, type RunSummary } from '../types'
import ResourceTable from '../portalkit/ResourceTable.vue'
import type { ResourceRefreshMode } from '../portalkit/page-state'
import type { ResourceTableChange, TableFilterDefinition, TableFilterState, TablePageInfo } from '../portalkit/table'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { toast } from '../ui/toast'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import type { Route } from '../router'
import { approvalDisclosureAvailable } from '../approval-disclosure'
import ApprovalDisclosure from '../components/ApprovalDisclosure.vue'

const props = withDefaults(defineProps<{
  store: AppStore
  api: ApiClient
  agent?: string
  authorityEpoch?: number
}>(), { agent: '', authorityEpoch: 0 })

const emit = defineEmits<{ navigate: [route: Route] }>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

type PhaseMeta = { label: string; cls: string; tone: 'success' | 'warning' | 'danger' }
const PHASE_META: Record<RunPhase, PhaseMeta> = {
  Pending: { label: 'Pending', cls: 'pending', tone: 'warning' },
  Running: { label: 'Running', cls: 'running', tone: 'warning' },
  PendingApproval: { label: 'Needs approval', cls: 'approval', tone: 'warning' },
  Succeeded: { label: 'Succeeded', cls: 'ok', tone: 'success' },
  Failed: { label: 'Failed', cls: 'failed', tone: 'danger' },
  Aborted: { label: 'Aborted', cls: 'aborted', tone: 'danger' },
}
const PHASES: RunPhase[] = ['Pending', 'Running', 'PendingApproval', 'Succeeded', 'Failed', 'Aborted']
const PAGE = 50
const RANGES = [
  { id: '24h', label: '24h', hours: 24 },
  { id: '7d', label: '7d', hours: 24 * 7 },
  { id: '30d', label: '30d', hours: 24 * 30 },
  { id: 'all', label: 'All', hours: 0 },
] as const

const runs = ref<RunSummary[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const loaded = ref(false)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const resolvingInbox = ref(new Set<string>())
const tablePage = ref(1)
const tablePageSize = ref(PAGE)
const tableCursor = ref<string | null>(null)
const tablePageInfo = ref<TablePageInfo | null>(null)
const tableFilters = ref<TableFilterState>({ agent: '', class: '', phase: '', range: '' })
let refreshTimer = 0
let requestGeneration = 0
let boundStore: AppStore | null = null
let backgroundRefreshQueued = false

const pending = computed(() => {
  void revision.value
  return props.store.pendingInbox().filter(item => !props.agent || item.agentName === props.agent)
})
const inboxSlice = computed(() => {
  void revision.value
  return props.store.inbox
})
const agents = computed(() => {
  void revision.value
  return props.store.agents.data
})
const agentOptions = computed(() => agents.value.map(item => ({ value: item.metadata.name, label: item.metadata.name })))
const classOptions = [
  { value: 'interactive', label: 'interactive' },
  { value: 'background', label: 'background' },
]
const phaseOptions = [
  ...PHASES.map(phase => ({ value: phase, label: PHASE_META[phase].label })),
]
const filters = computed<TableFilterDefinition[]>(() => [
  ...(!props.agent ? [{
    key: 'agent',
    label: 'Agent',
    allLabel: 'All agents',
    options: agentOptions.value,
    control: 'combobox' as const,
    searchPlaceholder: 'Find agent…',
  }] : []),
  { key: 'class', label: 'Class', allLabel: 'All classes', options: classOptions },
  { key: 'phase', label: 'Phase', allLabel: 'All phases', options: phaseOptions },
  {
    key: 'range',
    label: 'Range',
    allLabel: 'All time',
    options: RANGES.filter(candidate => candidate.id !== 'all').map(candidate => ({ value: candidate.id, label: candidate.label })),
  },
])
const columns = computed(() => [
  ...(!props.agent ? [{ key: 'agent', label: 'Agent', primary: true }] : []),
  { key: 'trigger', label: 'Trigger', primary: !!props.agent },
  { key: 'input', label: 'Input' },
  { key: 'phase', label: 'Phase' },
  { key: 'duration', label: 'Duration' },
  { key: 'usage', label: 'Usage' },
  { key: 'when', label: 'When' },
])
const tableRows = computed(() => runs.value.map(run => ({ ...run, input: run.inputPreview || '—', when: run.createdAt })))

function runFilter(cursor: string | null = tableCursor.value, limit = tablePageSize.value): RunFilter {
  const hours = RANGES.find(candidate => candidate.id === tableFilters.value.range)?.hours || 0
  return {
    agent: props.agent || tableFilters.value.agent || undefined,
    class: tableFilters.value.class || undefined,
    phase: tableFilters.value.phase || undefined,
    since: hours ? new Date(Date.now() - hours * 3_600_000).toISOString() : undefined,
    cursor: cursor || undefined,
    limit,
  }
}

async function reload(mode: 'foreground' | 'background' = 'foreground'): Promise<void> {
  if (mode === 'background' && loading.value) {
    backgroundRefreshQueued = true
    return
  }
  refreshMode.value = mode
  loading.value = true
  const generation = ++requestGeneration
  const authority = captureAuthority()
  const request = runFilter()
  try {
    const page = await authority.api.listRuns(request)
    if (generation !== requestGeneration || !authorityIsCurrent(authority)) return
    runs.value = page.items
    tablePageInfo.value = {
      hasNext: !!page.nextCursor,
      nextCursor: page.nextCursor || null,
    }
    error.value = null
    loaded.value = true
  } catch (cause) {
    if (generation === requestGeneration && authorityIsCurrent(authority)) error.value = (cause as Error).message
  } finally {
    if (generation === requestGeneration && authorityIsCurrent(authority)) {
      loading.value = false
      if (backgroundRefreshQueued) {
        backgroundRefreshQueued = false
        void reload('background')
      }
    }
  }
}

function onTableChange(change: ResourceTableChange): void {
  requestGeneration += 1
  backgroundRefreshQueued = false
  tablePage.value = change.page
  tablePageSize.value = change.pageSize
  tableCursor.value = change.cursor
  tableFilters.value = { ...change.filters }
  tablePageInfo.value = null
  runs.value = []
  loaded.value = false
  error.value = null
  void reload('foreground')
}

async function resolve(item: InboxItem, decision: 'approve' | 'deny'): Promise<void> {
  if (resolvingInbox.value.has(item.id)) return
  const authority = captureAuthority()
  resolvingInbox.value = new Set(resolvingInbox.value).add(item.id)
  try {
    await authority.api.resolveInbox(item.id, decision)
    if (!authorityIsCurrent(authority)) return
    toast('ok', decision === 'approve' ? 'Approved — the run is resuming.' : 'Denied.')
    void authority.store.load('inbox')
    void reload('foreground')
  } catch (cause) {
    if (authorityIsCurrent(authority)) toast('error', `Could not ${decision}: ${(cause as Error).message}`)
  } finally {
    const next = new Set(resolvingInbox.value)
    next.delete(item.id)
    resolvingInbox.value = next
  }
}

function onServerEvent(event: Event): void {
  if ((event as CustomEvent<ServerEvent>).detail.type !== 'run' || refreshTimer) return
  refreshTimer = window.setTimeout(() => {
    refreshTimer = 0
    void reload('background')
  }, 700)
}

function bindStore(store: AppStore): void {
  if (boundStore === store) return
  boundStore?.removeEventListener('server', onServerEvent as EventListener)
  boundStore = store
  boundStore.addEventListener('server', onServerEvent as EventListener)
}

function phaseMeta(phase: RunPhase): PhaseMeta {
  return PHASE_META[phase] || { label: phase, cls: 'pending', tone: 'warning' }
}

function openRun(row: Record<string, unknown>): void {
  emit('navigate', { kind: 'run', id: String(row.id) })
}

onMounted(() => {
  bindStore(props.store)
  void reload('foreground')
})
watch(() => [props.store, props.api, props.agent] as const, () => {
  bindStore(props.store)
  requestGeneration += 1
  backgroundRefreshQueued = false
  if (refreshTimer) window.clearTimeout(refreshTimer)
  refreshTimer = 0
  runs.value = []
  tablePage.value = 1
  tableCursor.value = null
  tablePageInfo.value = null
  tableFilters.value = { ...tableFilters.value, agent: '' }
  loaded.value = false
  loading.value = false
  error.value = null
  refreshMode.value = 'foreground'
  resolvingInbox.value = new Set()
  void reload('foreground')
}, { flush: 'post' })
onBeforeUnmount(() => {
  boundStore?.removeEventListener('server', onServerEvent as EventListener)
  boundStore = null
  if (refreshTimer) window.clearTimeout(refreshTimer)
  requestGeneration += 1
})
</script>

<template>
  <div class="agents-panel k-card agents-route-panel agents-activity-panel">
    <div class="agents-panel-head agents-activity-head">
      <h3 v-if="!agent">Activity</h3>
      <button
        class="k-btn k-btn--ghost secondary agents-filter-refresh"
        type="button"
        aria-label="Refresh runs"
        :disabled="loading && refreshMode === 'foreground'"
        :aria-busy="loading && refreshMode === 'foreground' || undefined"
        @click="reload('foreground')"
      >
        <RefreshCw :class="{ 'k-spin': loading && refreshMode === 'foreground' }" :stroke-width="1.75" aria-hidden="true" />
        {{ loading && refreshMode === 'foreground' ? 'Refreshing…' : 'Refresh' }}
      </button>
    </div>

    <div v-if="inboxSlice.error && !inboxSlice.hasSnapshot" class="agents-state agents-state-error" role="alert">
      <span>{{ inboxSlice.error }}</span>
      <button class="k-btn k-btn--ghost secondary" type="button" @click="store.load('inbox')">Retry</button>
    </div>
    <template v-else>
      <div v-if="inboxSlice.error" class="agents-state agents-state-error" role="status">
        <span>Showing the last loaded data. {{ inboxSlice.error }}</span>
        <button class="k-btn k-btn--ghost secondary" type="button" @click="store.load('inbox')">Retry</button>
      </div>
      <section v-if="pending.length" class="agents-approvals">
        <component :is="agent ? 'h2' : 'h4'"><Inbox :stroke-width="1.75" aria-hidden="true" /> Needs your attention ({{ pending.length }})</component>
        <div v-for="item in pending" :key="item.id" class="agents-approval-row">
          <div class="agents-approval-body">
            <div class="agents-approval-prompt">{{ item.prompt }}</div>
            <ApprovalDisclosure v-if="item.kind === 'approval'" :tool="item.payload?.tool" :args="item.payload?.args" />
            <div class="agents-approval-meta">
              <span class="k-badge agents-badge">{{ item.agentName }}</span>
              <span class="muted">{{ fmtTime(item.createdAt) }}</span>
              <button v-if="item.runID" class="k-dashboard-action" type="button" @click="emit('navigate', { kind: 'run', id: item.runID! })">view run</button>
            </div>
          </div>
          <div class="agents-approval-actions">
            <template v-if="item.kind === 'approval'">
              <button class="k-btn k-btn--primary" type="button" :disabled="resolvingInbox.has(item.id) || !approvalDisclosureAvailable(item.payload?.tool, item.payload?.args)" :aria-busy="resolvingInbox.has(item.id) || undefined" @click="resolve(item, 'approve')"><Check :stroke-width="1.75" aria-hidden="true" /> {{ resolvingInbox.has(item.id) ? 'Resolving…' : 'Approve' }}</button>
              <button class="k-btn k-btn--ghost secondary" type="button" :disabled="resolvingInbox.has(item.id)" @click="resolve(item, 'deny')"><X :stroke-width="1.75" aria-hidden="true" /> Deny</button>
            </template>
            <span v-else class="agents-hint">Answer from the agent's channel or chat.</span>
          </div>
        </div>
      </section>
    </template>

    <ResourceTable
      :columns="columns"
      :rows="tableRows"
      aria-label="Runs"
      variant="queryable"
      row-key="id"
      :loaded="loaded"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="loaded && !!error"
      retryable
      :filters="filters"
      :filter-values="tableFilters"
      pagination-mode="server"
      :page="tablePage"
      :page-size="tablePageSize"
      :query="''"
      :page-size-options="[25, 50, 100]"
      :cursor="tableCursor"
      :page-info="tablePageInfo"
      empty-text="No runs yet. Chat with an agent or fire a schedule to see one here."
      filter-empty-text="No runs match these filters."
      :interactive="true"
      :row-aria-label="row => `Open run ${String(row.id)}`"
      @change="onTableChange"
      @retry="reload('foreground')"
      @row-click="openRun"
    >
      <template #agent="{ row }"><strong>{{ row.agent }}</strong></template>
      <template #trigger="{ row }"><span class="k-badge agents-badge" :class="row.class === 'interactive' ? 'agents-cat-tool' : ''"><MessageCircle v-if="row.class === 'interactive'" :stroke-width="1.75" aria-hidden="true" /><Clock v-else :stroke-width="1.75" aria-hidden="true" /> {{ row.trigger }}</span> <span v-if="row.parentRunID" class="k-badge agents-badge k-badge--muted agents-badge-muted">delegated</span></template>
      <template #input="{ row }"><span class="agents-cell-task muted">{{ row.input }}</span></template>
      <template #phase="{ row }"><StatusBadge class="agents-phase" :class="`agents-phase-${phaseMeta(row.phase as RunPhase).cls}`" :status="phaseMeta(row.phase as RunPhase).label" :tone="phaseMeta(row.phase as RunPhase).tone" /> <span v-if="Number(row.attempt) > 1" class="k-badge agents-badge">try {{ row.attempt }}</span></template>
      <template #duration="{ row }"><span class="muted">{{ row.durationMS ? fmtDuration(Number(row.durationMS)) : '—' }}</span></template>
      <template #usage="{ row }"><span class="muted mono">{{ fmtTokens(Number(row.inputTokens) + Number(row.outputTokens)) }}<template v-if="row.usdMicros"> · {{ fmtUSD(Number(row.usdMicros)) }}</template></span></template>
      <template #when="{ row }"><span class="muted">{{ fmtTime(String(row.when)) }}</span></template>
    </ResourceTable>
  </div>
</template>
