<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, onUpdated, ref, watch } from 'vue'
import {
  Check,
  Circle,
  LoaderCircle,
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
import AIActivityFeed from '../agentkit/AIActivityFeed.vue'
import AIInterrupt from '../agentkit/AIInterrupt.vue'
import AIConversationTurn from '../agentkit/AIConversationTurn.vue'
import AITranscript from '../agentkit/AITranscript.vue'
import AITurnProgress from '../agentkit/AITurnProgress.vue'
import type { AITurnProgressStatus } from '../agentkit/conversation'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
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
import type { AIActionStatus } from '../agentkit/ai'
import StatusBadge from '../portalkit/StatusBadge.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import { toast } from '../ui/toast'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import { approvalDisclosureAvailable } from '../approval-disclosure'
import ApprovalDisclosure from '../components/ApprovalDisclosure.vue'

const props = withDefaults(defineProps<{
  store: AppStore
  api: ApiClient
  runId: string
  /** When set, this detail is rendered inside an agent's Runs workbench. */
  embeddedAgent?: string
  authorityEpoch?: number
}>(), { embeddedAgent: '', authorityEpoch: 0 })

const emit = defineEmits<{ navigate: [route: Route] }>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const root = ref<HTMLElement | null>(null)
const run = ref<Run | null>(null)
const error = ref<string | null>(null)
const loading = ref(false)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const expanded = ref(new Set<string>())
const stepsExpanded = ref(true)
const inspectorOpen = ref(true)
const resolvingInboxID = ref('')
const cancellingRuns = ref(new Set<string>())
const now = ref(Date.now())
let pollHandle = 0
let tickHandle = 0
let requestGeneration = 0
let boundStore: AppStore | null = null

const LIVE_PHASES = new Set<RunPhase>(['Pending', 'Running', 'PendingApproval'])
const marked = new Marked({ gfm: true, breaks: true })

const isEmbedded = computed(() => Boolean(props.embeddedAgent))
const runAgentMismatch = computed(() => Boolean(
  run.value && isEmbedded.value && run.value.agent !== props.embeddedAgent,
))
const backRoute = computed<Route>(() => isEmbedded.value
  ? { kind: 'agent', name: props.embeddedAgent, tab: 'runs' }
  : { kind: 'menu', menu: 'activity' })
const backHref = computed(() => hashFor(backRoute.value))
const backLabel = computed(() => isEmbedded.value ? 'Runs' : 'Activity')
const globalRunHref = computed(() => hashFor({ kind: 'run', id: run.value?.id || props.runId }))

const phaseMeta: Record<RunPhase, { label: string; cls: string; tone: 'success' | 'warning' | 'danger' }> = {
  Pending: { label: 'Pending', cls: 'pending', tone: 'warning' },
  Running: { label: 'Running', cls: 'running', tone: 'warning' },
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
    // PendingApproval is still live work. Keep the updating hint visible while
    // a child is paused for approval.
    live: running + queued + waiting > 0,
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

function stepClass(step: RunStep): 'ok' | 'err' | 'wait' | 'unknown' | 'audit' {
  if (step.outcome === 'ok') return 'ok'
  if (step.outcome === 'error') return 'err'
  if (step.outcome === 'pending_approval') return run.value?.phase === 'PendingApproval' ? 'wait' : 'audit'
  // A future or malformed outcome must remain visibly unresolved. Treating it
  // as success would overstate the evidence in a run trace.
  return 'unknown'
}

function stepStatus(step: RunStep): AIActionStatus {
  if (step.outcome === 'ok') return 'succeeded'
  if (step.outcome === 'error') return 'failed'
  if (step.outcome === 'pending_approval') return run.value?.phase === 'PendingApproval' ? 'waiting' : 'idle'
  return 'unknown'
}

function stepStatusLabel(step: RunStep): string {
  if (step.outcome === 'ok') return 'Completed'
  if (step.outcome === 'error') return 'Failed'
  // This is an immutable audit record, not the current approval decision.
  if (step.outcome === 'pending_approval') return 'Approval requested'
  return 'Unknown outcome'
}

function stepOutcome(step: RunStep): string {
  const parts = [step.outcome]
  if (step.durationMS) parts.push(fmtDuration(step.durationMS))
  if (step.at) parts.push(fmtTime(step.at))
  return parts.join(' · ')
}

function stepDetailsID(step: RunStep): string {
  return `agents-run-step-details-${step.id}`
}

function stepsSummary(): string {
  const current = run.value
  if (!current?.steps.length) return 'No tool calls'
  const failed = current.steps.filter(step => step.outcome === 'error').length
  const waiting = current.steps.filter(step => step.outcome === 'pending_approval').length
  const unknown = current.steps.filter(step => !['ok', 'error', 'pending_approval'].includes(step.outcome)).length
  if (waiting) return `${waiting} approval ${waiting === 1 ? 'request' : 'requests'}`
  if (failed) return `${failed} failed`
  if (unknown) return `${unknown} unresolved`
  return 'Completed'
}

function activityNeedsAttention(current: Run): boolean {
  return current.steps.some(step => stepClass(step) === 'wait' || stepClass(step) === 'unknown')
}

function activityIsBusy(current: Run): boolean {
  return (current.phase === 'Pending' || current.phase === 'Running') && !activityNeedsAttention(current)
}

const stepsByID = computed(() => new Map((run.value?.steps || []).map(step => [step.id, step])))
const stepGroups = computed(() => [{
  key: 'steps', label: '',
  rows: (run.value?.steps || []).map((step, index) => ({
    id: step.id, title: step.tool, target: `Step ${index + 1}`,
    status: stepStatus(step), statusLabel: stepStatusLabel(step), outcome: stepOutcome(step),
    attention: stepClass(step) === 'wait' || stepClass(step) === 'unknown',
    error: stepClass(step) === 'err',
    expandable: Boolean(step.args || step.result || step.error), detailsId: stepDetailsID(step),
  })),
}])

function toggleSteps(): void {
  stepsExpanded.value = !stepsExpanded.value
}

function toggleInspector(): void {
  inspectorOpen.value = !inspectorOpen.value
}

function turnProgressStatus(current: Run): AITurnProgressStatus {
  if (cancellingRuns.value.has(current.id)) return 'stopping'
  const states: Record<RunPhase, AITurnProgressStatus> = {
    Pending: 'pending', Running: 'running', PendingApproval: 'waiting',
    Succeeded: 'completed', Failed: 'failed', Aborted: 'aborted',
  }
  return states[current.phase] || 'pending'
}

function turnProgressDuration(current: Run): string | undefined {
  // Worked duration is measured model/tool time. The elapsed run duration is
  // intentionally separate, and a live wall clock must never fill in missing
  // work evidence during approval or recovery pauses.
  if (current.workedDurationMS != null && Number.isFinite(current.workedDurationMS) && current.workedDurationMS >= 0) {
    return fmtDuration(current.workedDurationMS)
  }
  return undefined
}

function elapsed(current: Run): string {
  return fmtDuration(Math.max(0, now.value - new Date(current.startedAt || current.createdAt).getTime()))
}

function openChild(child: RunSummary): void {
  emit('navigate', { kind: 'run', id: child.id })
}

function goBack(): void {
  emit('navigate', backRoute.value)
}

onMounted(() => {
  bindStore(props.store)
  void load()
})
watch(() => [props.store, props.api, props.runId, props.embeddedAgent] as const, () => {
  bindStore(props.store)
  requestGeneration += 1
  resolvingInboxID.value = ''
  cancellingRuns.value = new Set()
  stopLive()
  run.value = null
  error.value = null
  loading.value = false
  refreshMode.value = 'foreground'
  expanded.value = new Set()
  stepsExpanded.value = true
  inspectorOpen.value = true
  void load()
}, { flush: 'post' })
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
    <ResourceBackLink :href="backHref" @back="goBack">{{ backLabel }}</ResourceBackLink>
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
      <template #header>
        <div class="k-resource-page__heading">
          <template v-if="isEmbedded">
            <h2 class="k-resource-page__title">Run</h2>
            <div class="k-resource-page__meta agents-run-heading-meta">
              <code class="agents-run-heading-id mono">{{ runId }}</code>
              <span class="k-resource-page__separator" aria-hidden="true">·</span>
              <span>Agent <code>{{ run?.agent || embeddedAgent }}</code></span>
              <template v-if="run && !runAgentMismatch">
                <span class="k-resource-page__separator" aria-hidden="true">·</span>
                <span class="k-resource-page__status">
                  <StatusBadge class="agents-phase" :class="`agents-phase-${phaseMeta[run.phase].cls}`" :status="phaseMeta[run.phase].label" :tone="phaseMeta[run.phase].tone" />
                </span>
              </template>
            </div>
          </template>
          <template v-else>
            <h1 class="k-resource-page__title">Run</h1>
            <div class="k-resource-page__meta">
              <span class="k-resource-page__kind">Run</span>
              <template v-if="run">
                <span class="k-resource-page__separator" aria-hidden="true">·</span>
                <span class="k-resource-page__status">
                  <StatusBadge class="agents-phase" :class="`agents-phase-${phaseMeta[run.phase].cls}`" :status="phaseMeta[run.phase].label" :tone="phaseMeta[run.phase].tone" />
                </span>
              </template>
            </div>
            <p class="k-resource-page__subtitle">{{ runId }}</p>
          </template>
        </div>
        <div v-if="run && (!isEmbedded || !runAgentMismatch)" class="k-resource-page__header-side">
          <div class="k-resource-page__actions agents-detail-actions" role="group" aria-label="Run actions">
            <button v-if="LIVE_PHASES.has(run.phase)" class="k-btn k-btn--ghost secondary" type="button" :disabled="cancellingRuns.has(runId)" :aria-busy="cancellingRuns.has(runId) || undefined" @click="cancel"><X :stroke-width="1.75" aria-hidden="true" /> {{ cancellingRuns.has(runId) ? 'Cancelling…' : 'Cancel run' }}</button>
            <button class="k-btn k-btn--ghost secondary" type="button" :disabled="loading && refreshMode === 'foreground'" :aria-busy="loading && refreshMode === 'foreground' || undefined" @click="load('foreground')"><RefreshCw :class="{ 'k-spin': loading && refreshMode === 'foreground' }" :stroke-width="1.75" aria-hidden="true" /> {{ loading && refreshMode === 'foreground' ? 'Refreshing…' : 'Refresh' }}</button>
          </div>
        </div>
      </template>
      <template v-if="run" #body>
        <div v-if="runAgentMismatch" class="k-card agents-state agents-run-mismatch" role="alert">
          <div>
            <h3>This run belongs to a different agent</h3>
            <p>Run <code>{{ run.id }}</code> belongs to agent <code>{{ run.agent }}</code>, but this Runs view is scoped to <code>{{ embeddedAgent }}</code>.</p>
            <a class="k-dashboard-action" :href="globalRunHref">Open this run in Activity</a>
          </div>
        </div>
        <div v-else class="agents-run-layout">
          <main class="agents-run-main">
            <AITranscript class="agents-run-transcript">
              <AIConversationTurn
                v-if="run.input"
                class="agents-run-message agents-run-input-message"
                role="user"
                bubble
                aria-label="Run input"
              >
                <div class="agents-body">{{ run.input }}</div>
              </AIConversationTurn>
              <AIConversationTurn
                v-if="run.output || run.message || run.steps.length || (run.phase === 'PendingApproval' && run.pending)"
                class="agents-run-message agents-run-output-message"
                role="assistant"
                :aria-label="run.phase === 'Failed' || run.phase === 'Aborted' ? 'Partial run output' : 'Run output'"
              >
              <template #progress>
                <AITurnProgress
                  :turn-id="run.id"
                  :status="turnProgressStatus(run)"
                  :duration="turnProgressDuration(run)"
                  :interrupted="run.phase === 'Aborted'"
                />
              </template>
              <template #trace>
                <section class="agents-run-activity" aria-labelledby="agents-run-steps-heading">
                  <h2 id="agents-run-steps-heading" class="sr-only">Run steps</h2>
                  <AIActivityFeed
                    v-if="run.steps.length"
                    panel-id="agents-run-steps-panel"
                    :groups="stepGroups"
                    :count="run.steps.length"
                    :summary="stepsSummary()"
                    :expanded="stepsExpanded"
                    :expanded-row-keys="[...expanded]"
                    :busy="activityIsBusy(run)"
                    :attention="activityNeedsAttention(run)"
                    :error="run.steps.some(step => step.outcome === 'error')"
                    label="step"
                    @toggle="toggleSteps"
                    @toggle-row="toggle"
                  >
                    <template #details="{ row }">
                      <template v-for="step in [stepsByID.get(row.id)]" :key="row.id">
                        <template v-if="step">
                          <div v-if="step.args" class="agents-kv"><span>args</span><pre>{{ prettyJSON(step.args) }}</pre></div>
                          <div v-if="step.error" class="agents-kv"><span>error</span><pre class="err">{{ step.error }}</pre></div>
                          <div v-else-if="step.result" class="agents-kv"><span>result</span><pre>{{ prettyJSON(step.result) }}</pre></div>
                        </template>
                      </template>
                    </template>
                  </AIActivityFeed>
                  <p v-else class="agents-hint"><Wrench :stroke-width="1.75" aria-hidden="true" /> This run made no tool calls.</p>
                </section>
              </template>

              <div v-if="(run.phase === 'Failed' || run.phase === 'Aborted') && run.message" class="agents-err" role="alert">{{ run.message }}</div>
              <h3 v-if="(run.phase === 'Failed' || run.phase === 'Aborted') && run.output">Partial output</h3>
              <div v-if="run.output || !(run.phase === 'Failed' || run.phase === 'Aborted')" class="agents-body k-ai-prose" v-html="markdownHTML(run.output || run.message || '')"></div>
              <div v-if="run.sources?.length" class="agents-runsources">
                <span class="agents-runmeta-k">sources</span>
                <ul><li v-for="source in run.sources" :key="source"><a :href="source" target="_blank" rel="noopener noreferrer">{{ source }}</a></li></ul>
              </div>

              <template #after>
                <AIInterrupt
                  v-if="run.phase === 'PendingApproval' && run.pending"
                  class="agents-approval"
                  :status="resolvingInboxID ? 'busy' : 'pending'"
                  :busy="!!resolvingInboxID"
                  :invalid="!approvalDisclosureAvailable(run.pending.tool, run.pending.args)"
                  title="Approval required"
                  aria-label="Tool approval required"
                >
                  <ApprovalDisclosure :tool="run.pending.tool" :args="run.pending.args" paused details-only />
                  <template #actions>
                    <div class="agents-approval-actions">
                      <button class="k-btn k-btn--primary" type="button" :disabled="!!resolvingInboxID || !approvalDisclosureAvailable(run.pending.tool, run.pending.args)" :aria-busy="resolvingInboxID === run.pending.inboxID || undefined" @click="resolve(run.pending.inboxID, 'approve')"><Check :stroke-width="1.75" aria-hidden="true" /> {{ resolvingInboxID === run.pending.inboxID ? 'Resolving…' : 'Approve & resume' }}</button>
                      <button class="k-btn k-btn--ghost secondary" type="button" :disabled="!!resolvingInboxID" @click="resolve(run.pending.inboxID, 'deny')"><X :stroke-width="1.75" aria-hidden="true" /> Deny</button>
                    </div>
                  </template>
                </AIInterrupt>
              </template>
              </AIConversationTurn>
            </AITranscript>
          </main>

          <aside class="agents-run-inspector" :class="{ 'is-collapsed': !inspectorOpen }" aria-label="Run evidence">
            <button
              class="agents-run-inspector-toggle"
              type="button"
              :aria-expanded="inspectorOpen"
              aria-controls="agents-run-inspector-panel"
              @click="toggleInspector"
            >
              <span>Run evidence</span><span class="muted">{{ inspectorOpen ? 'Hide' : 'Show' }}</span>
            </button>
            <div v-show="inspectorOpen" id="agents-run-inspector-panel" class="agents-run-inspector-panel">
              <section class="agents-run-inspector-section" aria-labelledby="agents-run-details-heading">
                <h2 id="agents-run-details-heading">Run details</h2>
                <div class="agents-runmeta">
                  <div class="agents-runmeta-cell"><span class="agents-runmeta-k">agent</span><span class="agents-runmeta-v"><button class="k-dashboard-action" type="button" @click="emit('navigate', { kind: 'agent', name: run.agent, tab: 'chat' })">{{ run.agent }}</button></span></div>
                  <div class="agents-runmeta-cell"><span class="agents-runmeta-k">trigger</span><span class="agents-runmeta-v"><span class="mono">{{ run.trigger }}</span> <span class="muted">({{ run.class }})</span></span></div>
                  <div v-if="run.sessionID" class="agents-runmeta-cell"><span class="agents-runmeta-k">session</span><span class="agents-runmeta-v mono">{{ run.sessionID }}</span></div>
                  <div class="agents-runmeta-cell"><span class="agents-runmeta-k">started</span><span class="agents-runmeta-v">{{ fmtTime(run.startedAt || run.createdAt) }}</span></div>
                  <div class="agents-runmeta-cell"><span class="agents-runmeta-k">duration</span><span class="agents-runmeta-v"><template v-if="run.durationMS">{{ fmtDuration(run.durationMS) }}</template><span v-else-if="LIVE_PHASES.has(run.phase)" class="agents-elapsed"><LoaderCircle class="k-spin" :size="13" :stroke-width="1.75" aria-hidden="true" />{{ elapsed(run) }}</span><template v-else>—</template></span></div>
                  <div class="agents-runmeta-cell"><span class="agents-runmeta-k">usage</span><span class="agents-runmeta-v">{{ fmtTokens(run.inputTokens) }} in · {{ fmtTokens(run.outputTokens) }} out · {{ fmtUSD(run.usdMicros) }}</span></div>
                  <div v-if="run.attempt && run.attempt > 1" class="agents-runmeta-cell"><span class="agents-runmeta-k">attempt</span><span class="agents-runmeta-v">{{ run.attempt }}</span></div>
                  <div v-if="run.parentRunID" class="agents-runmeta-cell"><span class="agents-runmeta-k">parent</span><span class="agents-runmeta-v"><button class="k-dashboard-action" type="button" @click="emit('navigate', { kind: 'run', id: run.parentRunID! })">{{ run.parentRunID.slice(0, 8) }}</button></span></div>
                </div>
                <div v-if="run.input" class="agents-runinput"><span class="agents-runmeta-k">input</span><pre>{{ run.input }}</pre></div>
              </section>

              <section v-if="!run.children?.length && fanOutGranted" class="agents-run-inspector-section" aria-labelledby="agents-run-children-heading">
                <h2 id="agents-run-children-heading">Child runs (0)</h2>
                <p class="agents-hint"><Circle :stroke-width="1.75" aria-hidden="true" /><template v-if="run.steps.some(step => step.tool === 'spawn')"> This run called <span class="mono">spawn</span> but no worker runs were recorded — check the steps above for the error it came back with.</template><template v-else> Research fan-out is enabled and the agent was told how to use it, but it answered this request directly rather than splitting it up. That is the right call for a narrow question — a fan-out you do not need is just slower. For a request with genuinely independent parts, phrasing them explicitly ("compare X, Y and Z") makes the split obvious.</template></p>
              </section>
              <section v-else-if="run.children?.length" class="agents-run-inspector-section" aria-labelledby="agents-run-children-heading">
                <h2 id="agents-run-children-heading">Child runs ({{ run.children.length }})</h2>
                <p class="agents-child-summary" :class="{ 'is-live': childSummary.live }"><LoaderCircle v-if="childSummary.live" class="k-spin" :size="13" :stroke-width="1.75" aria-hidden="true" />{{ childSummary.text }} <span v-if="childSummary.live" class="muted">— this updates as they finish</span></p>
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
              </section>
            </div>
          </aside>
        </div>
      </template>
    </ResourcePage>
  </div>
</template>
