<script setup lang="ts">
// Dashboard tile for the databricks provider, mounted by
// <faros-dashboard-tile-databricks> (see element.ts).
//
// Warehouses lead, not tables. Tables are what you browse; a warehouse is what
// costs money while it runs and what every query fails on when it stops — so
// "which warehouses are running" is the fact worth a dashboard slot, and the
// table count rides along as context.

import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Check, ChevronRight, Link2, Package } from 'lucide-vue-next'
import { api, setHostFetch, setTenant, setTenantSelection, setToken } from './api'
import { formatDatabricksError } from './errors'
import type { Connection, Warehouse } from './types'
import {
  createTilePoller,
  hasWorkspaceContext,
  isBenignTileError,
  mostRecent,
  navigateFromTile,
  tileClass,
  type TileContext,
  type TilePoller,
} from './portalkit/dashboardtile'

const props = defineProps<{ context: TileContext | null }>()

const rootRef = ref<HTMLElement | null>(null)
const warehouses = ref<Warehouse[]>([])
const connections = ref<Connection[]>([])
const loading = ref(true)
const loaded = ref(false)
const error = ref<string | null>(null)
let poller: TilePoller | null = null
let requestGeneration = 0

function contextIdentity(ctx: TileContext | null): string {
  return JSON.stringify([ctx?.tenant ?? '', ctx?.orgUUID ?? '', ctx?.workspaceUUID ?? ''])
}

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
  const generation = ++requestGeneration
  const ctx = props.context
  if (!hasWorkspaceContext(ctx)) {
    if (generation !== requestGeneration) return
    warehouses.value = []
    connections.value = []
    error.value = null
    loading.value = false
    loaded.value = true
    return
  }
  loading.value = true
  setHostFetch(ctx?.fetch)
  setToken(ctx?.token ?? null)
  setTenant(ctx?.tenant ?? null)
  setTenantSelection(ctx?.orgUUID ?? null, ctx?.workspaceUUID ?? null)
  try {
    const [w, c] = await Promise.all([api.listWarehouses(), api.listConnections()])
    if (generation !== requestGeneration) return
    warehouses.value = w
    connections.value = c
    error.value = null
    loaded.value = true
  } catch (e) {
    if (generation !== requestGeneration || (e as { reason?: string })?.reason === 'ContextChanged') return
    if (isBenignTileError(e)) {
      warehouses.value = []
      connections.value = []
      error.value = null
      loaded.value = true
    } else {
      error.value = formatDatabricksError(e)
    }
  } finally {
    if (generation === requestGeneration) loading.value = false
  }
}

function retry() {
  poller?.refresh()
}

onMounted(() => {
  poller = createTilePoller(load)
  poller.start()
})
onUnmounted(() => {
  requestGeneration += 1
  poller?.stop()
})
watch(
  () => [props.context?.tenant, props.context?.orgUUID, props.context?.workspaceUUID, props.context?.token, props.context?.basePath] as const,
  (_next, previous) => {
    requestGeneration += 1
    if (contextIdentity(props.context) !== JSON.stringify([previous?.[0] ?? '', previous?.[1] ?? '', previous?.[2] ?? ''])) {
      warehouses.value = []
      connections.value = []
      error.value = null
      loaded.value = false
      loading.value = true
    }
    poller?.refresh()
  },
)
</script>

<template>
  <div ref="rootRef" :class="tileClass.root" :aria-busy="loading">
    <div v-if="!loaded && loading" :class="tileClass.message" role="status" aria-live="polite">Loading warehouses&hellip;</div>
    <div v-else-if="!loaded && error" :class="tileClass.error" role="alert" aria-live="assertive">
      Failed to load: {{ error }}
      <button type="button" class="k-dashboard-action" @click="retry">Retry</button>
    </div>

    <template v-else>
      <span v-if="loading" class="sr-only" role="status" aria-live="polite">Updating Databricks warehouses…</span>
      <div v-if="error" :class="tileClass.error" role="status" aria-live="polite">
        Showing the last successful result. {{ error }}
        <button type="button" class="k-dashboard-action" @click="retry">Retry</button>
      </div>
      <div :class="tileClass.stats">
        <span :class="[tileClass.stat, tileClass.statTotal]">
          <Package :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
          <span :class="tileClass.statNum">{{ stats.total }}</span>
          <span :class="tileClass.statLabel">{{ stats.total === 1 ? 'warehouse' : 'warehouses' }}</span>
        </span>
        <span v-if="stats.running > 0" :class="[tileClass.stat, tileClass.statOk]">
          <Check :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
          <span class="tabular-nums">{{ stats.running }}</span>
          <span :class="tileClass.statLabel">running</span>
        </span>
        <span :class="[tileClass.stat, tileClass.statMuted]">
          <Link2 :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
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
              <ChevronRight :class="tileClass.chevron" :stroke-width="1.75" aria-hidden="true" />
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
