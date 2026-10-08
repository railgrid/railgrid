<script setup lang="ts">
// Dashboard tile for the edges provider, mounted by
// <railgrid-dashboard-tile-edges> (see element.ts).
//
// An edge fleet is interesting when something stops reporting, not when
// everything is fine — so this tile inverts the usual "most recent first"
// ordering and lists the edges whose heartbeat is OLDEST. A disconnected edge
// is the row you need; a healthy one is the row you scroll past. The headline
// is therefore "N of M connected" rather than a bare total, because the ratio
// is the fact worth glancing at.

import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { AlertTriangle, Check, ChevronRight } from 'lucide-vue-next'
import { listEdges, setHostFetch, setTenant, setToken } from './api'
import type { Edge } from './types'
import {
  TILE_ROWS,
  hasWorkspaceContext,
  isBenignTileError,
  navigateFromTile,
  tileClass,
  tileErrorText,
  type TileContext,
} from './portalkit/dashboardtile'
import {
  FAST_REFRESH_MS,
  STABLE_REFRESH_MS,
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  type LatestRefreshController,
} from './refresh'

const props = defineProps<{ context: TileContext | null }>()

const rootRef = ref<HTMLElement | null>(null)
const edges = ref<Edge[]>([])
const loading = ref(true)
const loaded = ref(false)
const error = ref<string | null>(null)
let refresh!: LatestRefreshController
const poller = createAdaptiveRefreshTimer(() => load('background'), () =>
  !loaded.value || error.value || edges.value.some(edge => !edge.connected) ? FAST_REFRESH_MS : STABLE_REFRESH_MS,
)

const stats = computed(() => {
  const total = edges.value.length
  const connected = edges.value.filter((e) => e.connected).length
  return { total, connected, offline: total - connected }
})

// Disconnected first, then by oldest heartbeat. An edge that has never
// reported (no timestamp) sorts to the very top: it is the most likely to be
// mid-enrolment or broken, and the least likely to be noticed elsewhere.
const rows = computed(() =>
  [...edges.value]
    .sort((a, b) => {
      if (a.connected !== b.connected) return a.connected ? 1 : -1
      return (a.lastHeartbeatTime || '').localeCompare(b.lastHeartbeatTime || '')
    })
    .slice(0, TILE_ROWS),
)

// Heartbeats are the whole point of the list, so render them as an age rather
// than a timestamp nobody can subtract at a glance.
function age(iso?: string): string {
  if (!iso) return 'never'
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return 'unknown'
  const seconds = Math.max(0, Math.round((Date.now() - then) / 1000))
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.round(hours / 24)}d ago`
}

function load(mode: 'foreground' | 'background' = 'foreground'): void {
  void refresh.request(mode)
}

refresh = createLatestRefreshController(async (requestID) => {
  const ctx = props.context
  if (!hasWorkspaceContext(ctx)) {
    if (!refresh.isCurrent(requestID)) return
    edges.value = []
    error.value = null
    loaded.value = true
    loading.value = false
    poller.schedule()
    return
  }
  setHostFetch(ctx?.fetch)
  setToken(ctx?.token ?? null)
  setTenant(ctx?.tenant ?? null)
  loading.value = true
  try {
    const next = await listEdges()
    if (!refresh.isCurrent(requestID)) return
    edges.value = next
    error.value = null
    loaded.value = true
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    error.value = isBenignTileError(e) ? null : tileErrorText(e)
    if (!error.value && !loaded.value) loaded.value = true
  } finally {
    if (refresh.isCurrent(requestID)) {
      loading.value = false
      poller.schedule()
    }
  }
})

onMounted(() => {
  load()
  poller.schedule()
})
onUnmounted(() => {
  poller.stop()
  refresh.stop()
})
watch(
  [() => props.context?.tenant, () => props.context?.orgUUID, () => props.context?.workspaceUUID,
    () => props.context?.user?.userId || props.context?.user?.sub || props.context?.user?.email],
  () => {
    edges.value = []
    error.value = null
    loaded.value = false
    loading.value = true
    refresh.invalidate()
    load()
  },
)
watch([() => props.context?.token, () => props.context?.fetch], () => {
  refresh.invalidate()
  load('background')
})
</script>

<template>
  <div ref="rootRef" :class="tileClass.root" :aria-busy="loading">
    <div v-if="loading && !loaded" :class="tileClass.message" role="status" aria-live="polite">Loading edges&hellip;</div>
    <div v-else-if="error && !loaded" :class="tileClass.error" role="alert">Failed to load: {{ error }} <button type="button" class="k-dashboard-action" @click="load()">Retry</button></div>

    <template v-else>
      <div v-if="error" :class="tileClass.error" role="status" aria-live="polite">Showing cached edges. {{ error }} <button type="button" class="k-dashboard-action" @click="load()">Retry</button></div>
      <span v-else-if="loading" class="sr-only" role="status" aria-live="polite">Updating edges…</span>
      <div :class="tileClass.stats">
        <span :class="[tileClass.stat, tileClass.statTotal]">
          <Check :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
          <span :class="tileClass.statNum">{{ stats.connected }}</span>
          <span :class="tileClass.statLabel">of</span>
          <span class="tabular-nums">{{ stats.total }}</span>
          <span :class="tileClass.statLabel">connected</span>
        </span>
        <span v-if="stats.offline > 0" :class="[tileClass.stat, tileClass.statBad]">
          <AlertTriangle :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
          <span class="tabular-nums">{{ stats.offline }}</span>
          <span :class="tileClass.statLabel">offline</span>
        </span>
      </div>

      <div v-if="rows.length">
        <!-- Named for the ordering, which is the point of this list: the edge
             you need to look at is the one that stopped reporting. -->
        <div :class="tileClass.sectionLabel">Least recently seen</div>
        <ul :class="tileClass.list">
          <li v-for="edge in rows" :key="`${edge.type}/${edge.name}`">
            <button
              type="button"
              :class="tileClass.row"
              :aria-label="`Open edge ${edge.name}, ${edge.connected ? 'connected' : 'offline'}, last seen ${age(edge.lastHeartbeatTime)}`"
              @click="navigateFromTile(rootRef, `${edge.name}?type=${edge.type}`)"
            >
              <span
                :class="[tileClass.rowDot, edge.connected ? 'bg-success' : 'bg-danger']"
                aria-hidden="true"
              />
              <span :class="tileClass.rowPrimary">{{ edge.name }}</span>
              <span
                :class="[tileClass.rowSecondary, edge.connected ? '' : 'text-danger']"
              >{{ edge.connected ? 'Connected' : 'Offline' }} · {{ age(edge.lastHeartbeatTime) }}</span>
              <ChevronRight :class="tileClass.chevron" :stroke-width="1.75" aria-hidden="true" />
            </button>
          </li>
        </ul>
      </div>

      <div v-else :class="tileClass.empty" role="status" aria-live="polite">No edges enrolled yet.</div>
    </template>
  </div>
</template>
