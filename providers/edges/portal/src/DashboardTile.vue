<script setup lang="ts">
// Dashboard tile for the edges provider, mounted by
// <faros-dashboard-tile-edges> (see element.ts).
//
// An edge fleet is interesting when something stops reporting, not when
// everything is fine — so this tile inverts the usual "most recent first"
// ordering and lists the edges whose heartbeat is OLDEST. A disconnected edge
// is the row you need; a healthy one is the row you scroll past. The headline
// is therefore "N of M connected" rather than a bare total, because the ratio
// is the fact worth glancing at.

import { computed, h, onMounted, onUnmounted, ref, watch } from 'vue'
import { listEdges, setTenant, setToken } from './api'
import type { Edge } from './types'
import {
  TILE_ROWS,
  createTilePoller,
  hasWorkspaceContext,
  isBenignTileError,
  navigateFromTile,
  tileClass,
  tileErrorText,
  type TileContext,
  type TilePoller,
} from './portalkit/dashboardtile'
import { ic } from './portalkit/icons'

// Inline chevron — provider bundles are self-contained (no shared icon lib),
// the same reason the infrastructure tile inlines its own.
const ChevronRight = (props: { class?: string }) =>
  h(
    'svg',
    {
      xmlns: 'http://www.w3.org/2000/svg',
      viewBox: '0 0 24 24',
      fill: 'none',
      stroke: 'currentColor',
      'stroke-width': 2,
      'stroke-linecap': 'round',
      'stroke-linejoin': 'round',
      class: props.class,
    },
    [h('path', { d: 'm9 18 6-6-6-6' })],
  )

const props = defineProps<{ context: TileContext | null }>()

const rootRef = ref<HTMLElement | null>(null)
const edges = ref<Edge[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
let poller: TilePoller | null = null

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

async function load() {
  const ctx = props.context
  if (!hasWorkspaceContext(ctx)) {
    edges.value = []
    error.value = null
    loading.value = false
    return
  }
  setToken(ctx?.token ?? null)
  setTenant(ctx?.tenant ?? null)
  try {
    edges.value = await listEdges()
    error.value = null
  } catch (e) {
    edges.value = []
    error.value = isBenignTileError(e) ? null : tileErrorText(e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  poller = createTilePoller(load)
  poller.start()
})
onUnmounted(() => poller?.stop())
watch(() => props.context, () => poller?.refresh())
</script>

<template>
  <div ref="rootRef" :class="tileClass.root">
    <div v-if="loading" :class="tileClass.message">Loading edges&hellip;</div>
    <div v-else-if="error" :class="tileClass.error">Failed to load: {{ error }}</div>

    <template v-else>
      <div :class="tileClass.stats">
        <span :class="[tileClass.stat, tileClass.statTotal]">
          <span v-html="ic('check', tileClass.statIcon)" />
          <span :class="tileClass.statNum">{{ stats.connected }}</span>
          <span :class="tileClass.statLabel">of</span>
          <span class="tabular-nums">{{ stats.total }}</span>
          <span :class="tileClass.statLabel">connected</span>
        </span>
        <span v-if="stats.offline > 0" :class="[tileClass.stat, tileClass.statBad]">
          <span v-html="ic('alert-triangle', tileClass.statIcon)" />
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
              @click="navigateFromTile(rootRef, `${edge.name}?type=${edge.type}`)"
            >
              <span
                :class="[tileClass.rowDot, edge.connected ? 'bg-success' : 'bg-danger']"
                aria-hidden="true"
              />
              <span :class="tileClass.rowPrimary">{{ edge.name }}</span>
              <span
                :class="[tileClass.rowSecondary, edge.connected ? '' : 'text-danger']"
              >{{ age(edge.lastHeartbeatTime) }}</span>
              <ChevronRight :class="tileClass.chevron" />
            </button>
          </li>
        </ul>
      </div>

      <div v-else :class="tileClass.empty">No edges enrolled yet.</div>
    </template>
  </div>
</template>
