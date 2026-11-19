<script setup lang="ts">
import LoadingSkeleton from '../portalkit/LoadingSkeleton.vue'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { AlertTriangle, Bot, ChevronRight, Clock } from 'lucide-vue-next'
import { ApiClient } from '../api'
import { hashFor } from '../router'
import ResourceTimestamp from '../portalkit/ResourceTimestamp.vue'
import type { Agent, RailgridContext, RunSummary, Schedule } from '../types'
import {
  TILE_ROWS,
  createTilePoller,
  hasWorkspaceContext,
  isBenignTileError,
  tileErrorText,
  type TilePoller,
} from '../portalkit/dashboardtile'

const FAILED_PHASES = new Set(['Failed', 'Error', 'Cancelled', 'Timeout'])
const props = defineProps<{ context: RailgridContext | null }>()
const emit = defineEmits<{ navigate: [path: string] }>()

const agents = ref<Agent[]>([])
const runs = ref<RunSummary[]>([])
const schedules = ref<Schedule[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
const hasSnapshot = ref(false)
const tileContent = ref<HTMLElement | null>(null)
const api = new ApiClient()
let poller: TilePoller | null = null
let contextGeneration = 0
let activeContext: RailgridContext | null = null
let activeAuthorityKey = ''

function applyContext(context: RailgridContext | null): void {
  const nextAuthorityKey = authorityKey(context)
  const authorityChanged = nextAuthorityKey !== activeAuthorityKey
  activeContext = context
  activeAuthorityKey = nextAuthorityKey
  api.setContext(context)
  if (authorityChanged) contextGeneration += 1
  // A same-workspace caller change is still an authority boundary. Never keep
  // the prior user's snapshot visible while the new caller loads (or if that
  // caller's refresh is denied).
  if (authorityChanged) {
    agents.value = []
    runs.value = []
    schedules.value = []
    error.value = null
    hasSnapshot.value = false
    loading.value = true
  }
  poller?.refresh()
}

watch(() => props.context, applyContext, { immediate: true, flush: 'sync' })

onMounted(() => {
  poller = createTilePoller(load)
  poller.start()
})
onBeforeUnmount(() => {
  poller?.stop()
  poller = null
  contextGeneration += 1
})

async function load(): Promise<void> {
  const generation = contextGeneration
  if (!hasWorkspaceContext(activeContext)) {
    agents.value = []
    runs.value = []
    schedules.value = []
    error.value = null
    loading.value = false
    hasSnapshot.value = true
    return
  }
  if (!hasSnapshot.value) loading.value = true
  try {
    const [nextAgents, runPage, nextSchedules] = await Promise.all([
      api.listAgents(),
      api.listRuns({ limit: TILE_ROWS * 2 } as Record<string, unknown>),
      api.listSchedules(),
    ])
    if (generation !== contextGeneration) return
    agents.value = nextAgents
    runs.value = runPage.items
    schedules.value = nextSchedules
    error.value = null
    hasSnapshot.value = true
  } catch (cause) {
    if (generation !== contextGeneration) return
    if (!hasSnapshot.value && isBenignTileError(cause)) {
      error.value = null
      if (!hasSnapshot.value) {
        agents.value = []
        runs.value = []
        schedules.value = []
        hasSnapshot.value = true
      }
    } else {
      error.value = tileErrorText(cause)
    }
  } finally {
    if (generation === contextGeneration) loading.value = false
  }
}

async function retryLoad(): Promise<void> {
  if (loading.value) return
  const focused = document.activeElement
  await load()
  await nextTick()
  if (focused && !focused.isConnected && document.activeElement === document.body) tileContent.value?.focus()
}

const failed = computed(() => runs.value.filter(run => FAILED_PHASES.has(run.phase)).length)
const visibleRuns = computed(() => runs.value.slice(0, TILE_ROWS))
const nextRun = computed(() => {
  const now = Date.now()
  return schedules.value
    .filter(schedule => !schedule.spec.suspend && schedule.status?.nextRun)
    .map(schedule => ({ at: schedule.status!.nextRun!, agent: schedule.spec.agentRef }))
    .filter(item => Date.parse(item.at) > now)
    .sort((left, right) => left.at.localeCompare(right.at))[0] ?? null
})

function phaseDot(phase: string): string {
  if (FAILED_PHASES.has(phase)) return 'agents-tile-dot-bad'
  if (phase === 'Succeeded' || phase === 'Completed') return 'agents-tile-dot-ok'
  return 'agents-tile-dot-idle'
}

function contextKey(context: RailgridContext | null): string {
  if (!context) return ''
  return [context.tenant, context.orgUUID, context.workspaceUUID].map(part => part ?? '').join('\u0000')
}

function authorityKey(context: RailgridContext | null): string {
  if (!context) return ''
  return [
    contextKey(context),
    context.user?.sub,
    context.user?.userId,
    context.user?.email,
    // Host-owned fetch renews credentials without changing the caller. The
    // legacy bearer remains an authority boundary when no host fetch exists.
    context.fetch ? '' : context.token,
    context.basePath,
  ].map(part => part ?? '').join('\u0000')
}

defineExpose({ load, api, applyContext })
</script>

<template>
  <LoadingSkeleton v-if="loading && !hasSnapshot && !error" label="Loading agents…" compact />
  <div v-else-if="error && !hasSnapshot" class="agents-tile-err" role="alert" aria-live="assertive">
    <span>Could not load agents. {{ error }}</span>
    <button class="k-btn k-btn--ghost secondary" type="button" :aria-disabled="loading || undefined" :aria-busy="loading || undefined" @click="retryLoad">{{ loading ? 'Retrying…' : 'Retry' }}</button>
  </div>
  <div v-else ref="tileContent" class="agents-tile" tabindex="-1">
    <div v-if="error" class="agents-tile-err" role="status" aria-live="polite">
      Could not refresh. Showing the last loaded data. {{ error }}
    </div>
    <div class="agents-tile-stats">
      <span class="agents-tile-stat"><Bot aria-hidden="true" /><strong>{{ agents.length }}</strong> {{ agents.length === 1 ? 'agent' : 'agents' }}</span>
      <span v-if="failed" class="agents-tile-stat agents-tile-bad"><AlertTriangle aria-hidden="true" /><strong>{{ failed }}</strong> failed</span>
      <span v-if="nextRun" class="agents-tile-stat agents-tile-next"><Clock aria-hidden="true" />next <ResourceTimestamp :value="nextRun.at" label="Next run" /> · {{ nextRun.agent }}</span>
    </div>

    <div v-if="visibleRuns.length">
      <div class="agents-tile-label">Recent runs</div>
      <ul class="agents-tile-rows">
        <li v-for="run in visibleRuns" :key="run.id" class="agents-tile-timed-row">
          <button class="k-btn k-btn--ghost" type="button" @click="emit('navigate', hashFor({ kind: 'run', id: run.id }))">
            <span :class="['agents-tile-dot', phaseDot(run.phase)]" aria-hidden="true" />
            <span class="agents-tile-agent">{{ run.agent }}</span>
            <span :class="FAILED_PHASES.has(run.phase) ? 'agents-tile-bad' : 'agents-tile-dim'">{{ run.phase.toLowerCase() }}</span>
            <ChevronRight class="agents-tile-chev" aria-hidden="true" />
          </button>
          <ResourceTimestamp :value="run.createdAt" label="Run started" />
        </li>
      </ul>
    </div>
    <p v-else class="agents-tile-empty">{{ agents.length ? 'No runs yet.' : 'No agents yet — create one to get started.' }}</p>
  </div>
</template>

<style scoped>
.agents-tile-timed-row { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 8px; font-size: 11px; }
.agents-tile-timed-row > button { flex: 1 1 140px; min-width: 0; width: auto; }
</style>
