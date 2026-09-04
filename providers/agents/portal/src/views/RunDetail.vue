<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, onUpdated, ref, watch } from 'vue'
import {
  Check,
  ChevronRight,
  Circle,
  KeyRound,
  RefreshCw,
  Wrench,
  X,
} from 'lucide-vue-next'
import { Marked } from 'marked'
import DOMPurify from 'dompurify'
import type { ApiClient } from '../api'
import { hashFor, type Route } from '../router'
import type { AppStore, ServerEvent } from '../store'
import type { ResourceRefreshMode } from '../portalkit/page-state'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import {
  fmtDuration,
  fmtTime,
  fmtTokens,
  fmtUSD,
  prettyJSON,
  type RunDetail as Run,
  type RunPhase,
  type RunStep,
  type RunSummary,
} from '../types'
import StatusBadge from '../portalkit/StatusBadge.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import { toast } from '../ui/toast'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'

const props = withDefaults(defineProps<{
  store: AppStore
  api: ApiClient
  runId: string
  authorityEpoch?: number
}>(), { authorityEpoch: 0 })

const emit = defineEmits<{ navigate: [route: Route] }>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const root = ref<HTMLElement | null>(null)
const run = ref<Run | null>(null)
const error = ref<string | null>(null)
const loading = ref(false)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const expanded = ref(new Set<string>())
const resolvingInboxID = ref('')
const cancellingRuns = ref(new Set<string>())
const now = ref(Date.now())
let pollHandle = 0
let tickHandle = 0
let requestGeneration = 0
let boundStore: AppStore | null = null

const LIVE_PHASES = new Set<RunPhase>(['Pending', 'Running', 'PendingApproval'])
const marked = new Marked({ gfm: true, breaks: true })

const phaseMeta: Record<RunPhase, { label: string; cls: string; tone: 'success' | 'warning' | 'danger' }> = {
  Pending: { label: 'Pending', cls: 'pending', tone: 'warning' },
  Running: { label: 'Running', cls: 'running', tone: 'success' },
  PendingApproval: { label: 'Needs approval', cls: 'approval', tone: 'warning' },
  Succeeded: { label: 'Succeeded', cls: 'ok', tone: 'success' },
  Failed: { label: 'Failed', cls: 'failed', tone: 'danger' },
  Aborted: { label: 'Aborted', cls: 'aborted', tone: 'danger' },
}

const fanOutGranted = computed(() => {
  void revision.value
  const current = run.value
  if (!current) return false
  const tools = props.store.agent(current.agent)?.spec?.tools
  const families = (current.class === 'interactive' ? tools?.interactive : tools?.background)?.families || []
  return families.includes('spawn')
})

const childSummary = computed(() => {
  const children = run.value?.children || []
  const running = children.filter(child => child.phase === 'Running').length
  const queued = children.filter(child => child.phase === 'Pending').length
  const waiting = children.filter(child => child.phase === 'PendingApproval').length
  const done = children.filter(child => child.phase === 'Succeeded').length
  const problems = children.filter(child => child.phase === 'Failed' || child.phase === 'Aborted').length
  return {
    live: running + queued > 0,
    workers: children.filter(child => child.trigger === 'spawn').length,
    text: [
      running ? `${running} running` : '',
      queued ? `${queued} queued` : '',
      waiting ? `${waiting} awaiting approval` : '',
      done ? `${done} done` : '',
      problems ? `${problems} failed` : '',
    ].filter(Boolean).join(' · '),
  }
})
const childRows = computed<Array<Record<string, unknown>>>(() => (run.value?.children || []).map(child => ({
  id: child.id,
  agent: child.agent,
  kind: child.trigger === 'spawn' ? 'worker' : 'delegated',
  input: child.inputPreview || '—',
  phase: child.phase,
  duration: child.durationMS ? fmtDuration(child.durationMS) : '—',
  usage: `${fmtTokens(child.inputTokens + child.outputTokens)} · ${fmtUSD(child.usdMicros)}`,
  child,
})))
const asChild = (row: Record<string, unknown>): RunSummary => row.child as RunSummary
const childAriaLabel = (row: Record<string, unknown>): string => `Open run ${asChild(row).id}`

function stopLive(): void {
  if (pollHandle) window.clearInterval(pollHandle)
  if (tickHandle) window.clearInterval(tickHandle)
  pollHandle = 0
  tickHandle = 0
}

function syncLive(current: Run | null): void {
  if (!current || !LIVE_PHASES.has(current.phase)) {
    stopLive()
    return
  }
  if (!pollHandle) pollHandle = window.setInterval(() => void load('background'), 3000)
  if (!tickHandle) tickHandle = window.setInterval(() => { now.value = Date.now() }, 1000)
}

async function load(mode: ResourceRefreshMode = 'foreground'): Promise<void> {
  const requestedRunID = props.runId
  const generation = ++requestGeneration
  const authority = captureAuthority()
  loading.value = true
  refreshMode.value = mode
  try {
    const next = await authority.api.getRun(requestedRunID)
    if (generation !== requestGeneration || requestedRunID !== props.runId || !authorityIsCurrent(authority)) return
    run.value = next
    error.value = null
  } catch (cause) {
    if (generation !== requestGeneration || requestedRunID !== props.runId || !authorityIsCurrent(authority)) return
    error.value = (cause as Error).message
  }
  if (generation === requestGeneration && requestedRunID === props.runId && authorityIsCurrent(authority)) {
    loading.value = false
    syncLive(run.value)
  }
}

function toggle(id: string): void {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

async function cancel(): Promise<void> {
  const authority = captureAuthority()
  const id = props.runId
  if (cancellingRuns.value.has(id)) return
  cancellingRuns.value = new Set(cancellingRuns.value).add(id)
  try {
    await authority.api.cancelRun(id)
    if (!authorityIsCurrent(authority) || id !== props.runId) return
    toast('ok', 'Cancelling the run…')
    void load()
  } catch (cause) {
    if (authorityIsCurrent(authority) && id === props.runId) toast('error', `Cancel failed: ${(cause as Error).message}`)
  } finally {
    const next = new Set(cancellingRuns.value)
    next.delete(id)
    cancellingRuns.value = next
  }
}

async function resolve(inboxID: string, decision: 'approve' | 'deny'): Promise<void> {
  if (resolvingInboxID.value) return
  const authority = captureAuthority()
  const id = props.runId
  resolvingInboxID.value = inboxID
  try {
    await authority.api.resolveInbox(inboxID, decision)
    if (!authorityIsCurrent(authority) || id !== props.runId) return
    toast('ok', decision === 'approve' ? 'Approved — the run is resuming.' : 'Denied.')
    void authority.store.load('inbox')
    void load()
  } catch (cause) {
    if (authorityIsCurrent(authority) && id === props.runId) toast('error', `Could not ${decision}: ${(cause as Error).message}`)
  } finally {
    if (resolvingInboxID.value === inboxID) resolvingInboxID.value = ''
  }
}

function onServerEvent(event: Event): void {
  const detail = (event as CustomEvent<ServerEvent>).detail
  const mine = detail.data.id === props.runId
    || detail.data.parentRunID === props.runId
    || run.value?.children?.some(child => child.id === detail.data.id)
  if (detail.type === 'run' && mine) void load('background')
  if (detail.type === 'inbox' && detail.data.runID === props.runId) void load('background')
}

function bindStore(store: AppStore): void {
  if (boundStore === store) return
  boundStore?.removeEventListener('server', onServerEvent as EventListener)
  boundStore = store
  boundStore.addEventListener('server', onServerEvent as EventListener)
}

function markdownHTML(source: string): string {
  const raw = marked.parse(source || '', { async: false })
  const clean = DOMPurify.sanitize(raw, { USE_PROFILES: { html: true } })
  const template = document.createElement('template')
  template.innerHTML = clean
  template.content.querySelectorAll('a[href]').forEach(anchor => {
    anchor.setAttribute('target', '_blank')
    anchor.setAttribute('rel', 'noopener noreferrer')
  })
  return template.innerHTML
}

function attachCodeCopy(container: ParentNode | null): void {
  container?.querySelectorAll<HTMLPreElement>('.agents-body pre').forEach(pre => {
    if (pre.dataset.copyWired) return
    pre.dataset.copyWired = '1'
    const button = document.createElement('button')
    button.type = 'button'
    button.className = 'agents-code-copy'
    button.textContent = 'Copy'
    button.setAttribute('aria-label', 'Copy code block')
    button.addEventListener('click', () => {
      const text = pre.querySelector('code')?.textContent ?? pre.textContent ?? ''
      void navigator.clipboard?.writeText(text).then(
        () => {
          button.textContent = 'Copied'
          window.setTimeout(() => { button.textContent = 'Copy' }, 1500)
        },
        () => { button.textContent = 'Failed' },
      )
    })
    pre.appendChild(button)
  })
}

function stepClass(step: RunStep): string {
  return step.outcome === 'error' ? 'err' : step.outcome === 'pending_approval' ? 'wait' : 'ok'
}

function elapsed(current: Run): string {
  return fmtDuration(Math.max(0, now.value - new Date(current.startedAt || current.createdAt).getTime()))
}

function openChild(child: RunSummary): void {
  emit('navigate', { kind: 'run', id: child.id })
}

onMounted(() => {
  bindStore(props.store)
  void load()
})
watch(() => props.store, bindStore, { flush: 'sync' })
watch(() => props.runId, () => {
  requestGeneration += 1
  resolvingInboxID.value = ''
  stopLive()
  run.value = null
  error.value = null
  loading.value = false
  expanded.value = new Set()
  void load()
})
onUpdated(() => { void nextTick(() => attachCodeCopy(root.value)) })
onBeforeUnmount(() => {
  boundStore?.removeEventListener('server', onServerEvent as EventListener)
  boundStore = null
  requestGeneration += 1
  stopLive()
})
</script>

<template>
  <div ref="root" class="agents-detail">
    <ResourceBackLink :href="hashFor({ kind: 'menu', menu: 'activity' })" @back="emit('navigate', { kind: 'menu', menu: 'activity' })">Activity</ResourceBackLink>
    <ResourcePage
      title="Run"
      kind="Run"
      :subtitle="runId"
      :loaded="!!run"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="!!run && !!error"
      retryable
      @retry="load('foreground')"
    >
      <template v-if="run" #status>
        <StatusBadge class="agents-phase" :class="`agents-phase-${phaseMeta[run.phase].cls}`" :status="phaseMeta[run.phase].label" :tone="phaseMeta[run.phase].tone" />
      </template>
      <template v-if="run" #actions>
        <div class="agents-detail-actions" role="group" aria-label="Run actions">
        <button v-if="LIVE_PHASES.has(run.phase)" class="k-btn k-btn--ghost secondary" type="button" :disabled="cancellingRuns.has(runId)" :aria-busy="cancellingRuns.has(runId) || undefined" @click="cancel"><X :stroke-width="1.75" aria-hidden="true" /> {{ cancellingRuns.has(runId) ? 'Cancelling…' : 'Cancel run' }}</button>
        <button class="k-btn k-btn--ghost secondary" type="button" :disabled="loading" :aria-busy="loading || undefined" @click="load('foreground')"><RefreshCw :stroke-width="1.75" aria-hidden="true" /> {{ loading ? 'Refreshing…' : 'Refresh' }}</button>
        </div>
      </template>
      <template v-if="run" #body>
      <ResourceSectionCard title="Run details">
        <div class="agents-runmeta">
          <div class="agents-runmeta-cell"><span class="agents-runmeta-k">agent</span><span class="agents-runmeta-v"><button class="k-btn k-btn--ghost agents-linkbtn" type="button" @click="emit('navigate', { kind: 'agent', name: run.agent, tab: 'config' })">{{ run.agent }}</button></span></div>
          <div class="agents-runmeta-cell"><span class="agents-runmeta-k">trigger</span><span class="agents-runmeta-v"><span class="mono">{{ run.trigger }}</span> <span class="muted">({{ run.class }})</span></span></div>
          <div v-if="run.sessionID" class="agents-runmeta-cell"><span class="agents-runmeta-k">session</span><span class="agents-runmeta-v mono">{{ run.sessionID }}</span></div>
          <div class="agents-runmeta-cell"><span class="agents-runmeta-k">started</span><span class="agents-runmeta-v">{{ fmtTime(run.startedAt || run.createdAt) }}</span></div>
          <div class="agents-runmeta-cell"><span class="agents-runmeta-k">duration</span><span class="agents-runmeta-v"><template v-if="run.durationMS">{{ fmtDuration(run.durationMS) }}</template><span v-else-if="LIVE_PHASES.has(run.phase)" class="agents-elapsed"><span class="agents-spinner" aria-hidden="true"></span>{{ elapsed(run) }}</span><template v-else>—</template></span></div>
          <div class="agents-runmeta-cell"><span class="agents-runmeta-k">usage</span><span class="agents-runmeta-v">{{ fmtTokens(run.inputTokens) }} in · {{ fmtTokens(run.outputTokens) }} out · {{ fmtUSD(run.usdMicros) }}</span></div>
          <div v-if="run.attempt && run.attempt > 1" class="agents-runmeta-cell"><span class="agents-runmeta-k">attempt</span><span class="agents-runmeta-v">{{ run.attempt }}</span></div>
          <div v-if="run.parentRunID" class="agents-runmeta-cell"><span class="agents-runmeta-k">parent</span><span class="agents-runmeta-v"><button class="k-btn k-btn--ghost agents-linkbtn" type="button" @click="emit('navigate', { kind: 'run', id: run.parentRunID! })">{{ run.parentRunID.slice(0, 8) }}</button></span></div>
        </div>
        <div v-if="run.input" class="agents-runinput"><span class="agents-runmeta-k">input</span><pre>{{ run.input }}</pre></div>
      </ResourceSectionCard>

      <ResourceSectionCard v-if="run.phase === 'PendingApproval' && run.pending" title="Approval required">
        <div class="agents-approval" role="group" aria-label="Tool approval required">
          <div class="agents-approval-head"><KeyRound :stroke-width="1.75" aria-hidden="true" /> Paused — approval required for <span class="mono">{{ run.pending.tool }}</span></div>
          <pre v-if="run.pending.args" class="agents-approval-args">{{ prettyJSON(run.pending.args) }}</pre>
          <div class="agents-approval-actions">
            <button class="k-btn k-btn--primary" type="button" :disabled="!!resolvingInboxID" :aria-busy="resolvingInboxID === run.pending.inboxID || undefined" @click="resolve(run.pending.inboxID, 'approve')"><Check :stroke-width="1.75" aria-hidden="true" /> {{ resolvingInboxID === run.pending.inboxID ? 'Resolving…' : 'Approve & resume' }}</button>
            <button class="k-btn k-btn--ghost secondary" type="button" :disabled="!!resolvingInboxID" @click="resolve(run.pending.inboxID, 'deny')"><X :stroke-width="1.75" aria-hidden="true" /> Deny</button>
          </div>
        </div>
      </ResourceSectionCard>

      <ResourceSectionCard v-if="(run.phase === 'Failed' || run.phase === 'Aborted') && run.message" title="Error">
        <div class="agents-err" role="alert">{{ run.message }}</div>
        <template v-if="run.output"><h3>Partial output</h3><div class="agents-body" v-html="markdownHTML(run.output)"></div></template>
      </ResourceSectionCard>
      <ResourceSectionCard v-else-if="run.output || run.message" title="Output">
        <div class="agents-body" v-html="markdownHTML(run.output || run.message || '')"></div>
        <div v-if="run.sources?.length" class="agents-runsources"><span class="agents-runmeta-k">sources</span><ul><li v-for="source in run.sources" :key="source"><a :href="source" target="_blank" rel="noopener noreferrer">{{ source }}</a></li></ul></div>
      </ResourceSectionCard>

      <ResourceSectionCard :title="`Steps (${run.steps.length})`">
        <p v-if="!run.steps.length" class="agents-hint"><Wrench :stroke-width="1.75" aria-hidden="true" /> This run made no tool calls.</p>
        <ol v-else class="agents-timeline">
          <li v-for="(step, index) in run.steps" :key="step.id" class="agents-step" :class="`is-${stepClass(step)}`">
            <button class="k-btn k-btn--ghost agents-step-head" type="button" :aria-expanded="expanded.has(step.id)" @click="toggle(step.id)">
              <span class="agents-step-n">{{ index + 1 }}</span><span class="agents-step-name mono">{{ step.tool }}</span><span class="agents-step-meta">{{ step.outcome }}<template v-if="step.durationMS"> · {{ fmtDuration(step.durationMS) }}</template> · {{ fmtTime(step.at) }}</span><span class="agents-toolcard-chev" :class="{ open: expanded.has(step.id) }"><ChevronRight :stroke-width="1.75" aria-hidden="true" /></span>
            </button>
            <div v-if="expanded.has(step.id)" class="agents-step-body"><div v-if="step.args" class="agents-kv"><span>args</span><pre>{{ prettyJSON(step.args) }}</pre></div><div v-if="step.error" class="agents-kv"><span>error</span><pre class="err">{{ step.error }}</pre></div><div v-if="step.result" class="agents-kv"><span>result</span><pre>{{ prettyJSON(step.result) }}</pre></div></div>
          </li>
        </ol>
      </ResourceSectionCard>

      <ResourceSectionCard v-if="!run.children?.length && fanOutGranted" title="Child runs (0)">
        <p class="agents-hint"><Circle :stroke-width="1.75" aria-hidden="true" /><template v-if="run.steps.some(step => step.tool === 'spawn')"> This run called <span class="mono">spawn</span> but no worker runs were recorded — check the steps above for the error it came back with.</template><template v-else> Research fan-out is enabled and the agent was told how to use it, but it answered this request directly rather than splitting it up. That is the right call for a narrow question — a fan-out you do not need is just slower. For a request with genuinely independent parts, phrasing them explicitly ("compare X, Y and Z") makes the split obvious.</template></p>
      </ResourceSectionCard>
      <ResourceSectionCard v-else-if="run.children?.length" :title="`Child runs (${run.children.length})`" :description="childSummary.workers ? `${childSummary.workers} spawned worker${childSummary.workers === 1 ? '' : 's'}` : ''">
        <p class="agents-child-summary" :class="{ 'is-live': childSummary.live }"><span v-if="childSummary.live" class="agents-spinner" aria-hidden="true"></span>{{ childSummary.text }} <span v-if="childSummary.live" class="muted">— this updates as they finish</span></p>
        <ResourceTable
          :columns="[{ key: 'agent', label: 'Agent', primary: true }, { key: 'kind', label: 'Kind' }, { key: 'input', label: 'Input' }, { key: 'phase', label: 'Phase' }, { key: 'duration', label: 'Duration' }, { key: 'usage', label: 'Usage' }]"
          :rows="childRows"
          row-key="id"
          aria-label="Child runs"
          variant="simple"
          :loaded="true"
          :interactive="true"
          :row-aria-label="childAriaLabel"
          @row-click="openChild(asChild($event))"
        >
          <template #agent="{ row }"><strong>{{ asChild(row).agent }}</strong></template>
          <template #kind="{ row }"><span class="muted mono">{{ asChild(row).trigger === 'spawn' ? 'worker' : 'delegated' }}</span></template>
          <template #input="{ row }"><span class="agents-cell-task muted">{{ asChild(row).inputPreview || '—' }}</span></template>
          <template #phase="{ row }"><StatusBadge class="agents-phase" :class="`agents-phase-${phaseMeta[asChild(row).phase].cls}`" :status="phaseMeta[asChild(row).phase].label" :tone="phaseMeta[asChild(row).phase].tone" /></template>
          <template #duration="{ row }"><span class="muted">{{ asChild(row).durationMS ? fmtDuration(asChild(row).durationMS) : '—' }}</span></template>
          <template #usage="{ row }"><span class="muted mono">{{ fmtTokens(asChild(row).inputTokens + asChild(row).outputTokens) }} · {{ fmtUSD(asChild(row).usdMicros) }}</span></template>
        </ResourceTable>
      </ResourceSectionCard>
      </template>
    </ResourcePage>
  </div>
</template>
