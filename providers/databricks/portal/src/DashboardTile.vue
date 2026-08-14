<script setup lang="ts">
// Dashboard tile for the databricks provider, mounted by
// <faros-dashboard-tile-databricks> (see element.ts).
//
// Warehouses lead, not tables. Tables are what you browse; a warehouse is what
// costs money while it runs and what every query fails on when it stops — so
// "which warehouses are running" is the fact worth a dashboard slot, and the
// table count rides along as context.

import { computed, h, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, setTenant, setTenantSelection, setToken } from './api'
import type { Connection, Warehouse } from './types'
import {
  createTilePoller,
  hasWorkspaceContext,
  isBenignTileError,
  mostRecent,
  navigateFromTile,
  tileClass,
  tileErrorText,
  type TileContext,
  type TilePoller,
} from './portalkit/dashboardtile'
import { ic } from './portalkit/icons'

// Inline chevron — provider bundles are self-contained (no shared icon lib).
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
const warehouses = ref<Warehouse[]>([])
const connections = ref<Connection[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
let poller: TilePoller | null = null

// Databricks reports warehouse state in its own vocabulary; treat anything
// that is not explicitly RUNNING/STARTING as stopped rather than guessing at
// the full enum, so a new state string never reads as "running".
function isRunning(w: Warehouse): boolean {
  const state = (w.state ?? '').toUpperCase()
  return state === 'RUNNING' || state === 'STARTING'
}

const stats = computed(() => {
  const total = warehouses.value.length
  const running = warehouses.value.filter(isRunning).length
  return { total, running, connections: connections.value.length }
})

const rows = computed(() => mostRecent(warehouses.value, (w) => w.creationTimestamp))

async function load() {
  const ctx = props.context
  if (!hasWorkspaceContext(ctx)) {
    warehouses.value = []
    connections.value = []
    error.value = null
    loading.value = false
    return
  }
  setToken(ctx?.token ?? null)
  setTenant(ctx?.tenant ?? null)
  setTenantSelection(ctx?.orgUUID ?? null, ctx?.workspaceUUID ?? null)
  try {
    const [w, c] = await Promise.all([api.listWarehouses(), api.listConnections()])
    warehouses.value = w
    connections.value = c
    error.value = null
  } catch (e) {
    warehouses.value = []
    connections.value = []
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
    <div v-if="loading" :class="tileClass.message">Loading warehouses&hellip;</div>
    <div v-else-if="error" :class="tileClass.error">Failed to load: {{ error }}</div>

    <template v-else>
      <div :class="tileClass.stats">
        <span :class="[tileClass.stat, tileClass.statTotal]">
          <span v-html="ic('package', tileClass.statIcon)" />
          <span :class="tileClass.statNum">{{ stats.total }}</span>
          <span :class="tileClass.statLabel">{{ stats.total === 1 ? 'warehouse' : 'warehouses' }}</span>
        </span>
        <span v-if="stats.running > 0" :class="[tileClass.stat, tileClass.statOk]">
          <span v-html="ic('check', tileClass.statIcon)" />
          <span class="tabular-nums">{{ stats.running }}</span>
          <span :class="tileClass.statLabel">running</span>
        </span>
        <span :class="[tileClass.stat, tileClass.statMuted]">
          <span v-html="ic('link', tileClass.statIcon)" />
          <span class="tabular-nums">{{ stats.connections }}</span>
          <span>{{ stats.connections === 1 ? 'connection' : 'connections' }}</span>
        </span>
      </div>

      <div v-if="rows.length">
        <div :class="tileClass.sectionLabel">Warehouses</div>
        <ul :class="tileClass.list">
          <li v-for="w in rows" :key="w.name">
            <button
              type="button"
              :class="tileClass.row"
              @click="navigateFromTile(rootRef, `warehouses/${w.name}`)"
            >
              <span
                :class="[tileClass.rowDot, isRunning(w) ? 'bg-success' : 'bg-text-muted']"
                aria-hidden="true"
              />
              <span :class="tileClass.rowPrimary">{{ w.name }}</span>
              <span :class="tileClass.rowSecondary">{{ (w.state || 'unknown').toLowerCase() }}</span>
              <ChevronRight :class="tileClass.chevron" />
            </button>
          </li>
        </ul>
      </div>

      <div v-else-if="stats.connections === 0" :class="tileClass.empty">
        No Databricks connection yet — connect a workspace to list warehouses.
      </div>
      <div v-else :class="tileClass.empty">No warehouses yet.</div>
    </template>
  </div>
</template>
