<script setup lang="ts">
// Dashboard tile for the code provider, mounted by
// <faros-dashboard-tile-code> (see element.ts).
//
// What a user needs to know about Code at a glance is not "how many
// repositories exist" — it is whether the thing repositories depend on is
// working. A Connection is the credential every repository, package crawl and
// commit rides on; when one stops validating, everything under it fails at
// once and the repository list looks fine right up until you click into it.
// So the headline counts repositories, but the breakdown leads with broken
// connections and the rows carry each repository's own ready state.
//
// Read-only, and silent about a workspace that has not been bootstrapped: see
// portalkit/dashboardtile.

import { computed, h, onMounted, onUnmounted, ref, watch } from 'vue'
import { api } from './api'
import type { Connection, Repository } from './types'
import {
  hasWorkspaceContext,
  isBenignTileError,
  mostRecent,
  navigateFromTile,
  TILE_POLL_MS,
  tileClass,
  tileErrorText,
  type TileContext,
} from './portalkit/dashboardtile'
import { ic } from './portalkit/icons'
import { createLatestRefreshController, type LatestRefreshController } from './refresh'

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
const repositories = ref<Repository[]>([])
const connections = ref<Connection[]>([])
const loading = ref(true)
const loaded = ref(false)
const error = ref<string | null>(null)
let timer: number | undefined
let refresh!: LatestRefreshController

const stats = computed(() => {
  const repos = repositories.value.length
  const broken = connections.value.filter((c) => !c.validated).length
  const notReady = repositories.value.filter((r) => !r.ready).length
  return { repos, connections: connections.value.length, broken, notReady }
})

// Repositories carry no timestamp in the list projection, so order by name for
// a stable list rather than faking recency.
const rows = computed(() => mostRecent(repositories.value, (r) => r.name))

function load() {
  refresh.request()
}

refresh = createLatestRefreshController(async requestID => {
  const ctx = props.context
  if (!hasWorkspaceContext(ctx)) {
    if (!refresh.isCurrent(requestID)) return
    repositories.value = []
    connections.value = []
    error.value = null
    loaded.value = true
    loading.value = false
    return
  }
  loading.value = true
  try {
    // Both lists in parallel: the tile is worthless without the connection
    // health, and serialising doubles the time the card sits on "Loading".
    const readContext = { token: ctx?.token ?? null, tenant: ctx?.tenant ?? null }
    const [repos, conns] = await Promise.all([api.listRepositories(readContext), api.listConnections(readContext)])
    if (!refresh.isCurrent(requestID)) return
    repositories.value = repos
    connections.value = conns
    error.value = null
    loaded.value = true
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    error.value = isBenignTileError(e) ? null : tileErrorText(e)
  } finally {
    if (refresh.isCurrent(requestID)) loading.value = false
  }
})

onMounted(() => {
  load()
  timer = window.setInterval(load, TILE_POLL_MS)
})
onUnmounted(() => {
  window.clearInterval(timer)
  refresh.stop()
})
watch(
  [() => props.context?.tenant, () => props.context?.token, () => props.context?.orgUUID, () => props.context?.workspaceUUID],
  () => {
    repositories.value = []
    connections.value = []
    error.value = null
    loaded.value = false
    loading.value = true
    refresh.invalidate()
    load()
  },
)
</script>

<template>
  <div ref="rootRef" :class="tileClass.root" :aria-busy="loading">
    <div v-if="loading && !loaded" :class="tileClass.message" role="status" aria-live="polite">Loading repositories&hellip;</div>
    <div v-else-if="error && !loaded" :class="tileClass.error" role="alert" aria-live="assertive">Failed to load: {{ error }} <button type="button" class="link" @click="load">Retry</button></div>

    <template v-else>
      <div v-if="error" :class="tileClass.error" role="alert" aria-live="assertive">Showing cached data. {{ error }} <button type="button" class="link" @click="load">Retry</button></div>
      <span v-else-if="loading" class="sr-only" role="status" aria-live="polite">Updating repositories…</span>
      <div :class="tileClass.stats">
        <span :class="[tileClass.stat, tileClass.statTotal]">
          <span v-html="ic('package', tileClass.statIcon)" />
          <span :class="tileClass.statNum">{{ stats.repos }}</span>
          <span :class="tileClass.statLabel">{{ stats.repos === 1 ? 'repository' : 'repositories' }}</span>
        </span>
        <span :class="[tileClass.stat, tileClass.statMuted]">
          <span v-html="ic('link', tileClass.statIcon)" />
          <span class="tabular-nums">{{ stats.connections }}</span>
          <span>{{ stats.connections === 1 ? 'connection' : 'connections' }}</span>
        </span>
        <!-- A broken connection is the failure everything else inherits, so it
             is the one number that earns colour on this tile. -->
        <span v-if="stats.broken > 0" :class="[tileClass.stat, tileClass.statBad]">
          <span v-html="ic('alert-triangle', tileClass.statIcon)" />
          <span class="tabular-nums">{{ stats.broken }}</span>
          <span :class="tileClass.statLabel">not validated</span>
        </span>
        <span v-if="stats.notReady > 0" :class="[tileClass.stat, tileClass.statWarn]">
          <span v-html="ic('clock', tileClass.statIcon)" />
          <span class="tabular-nums">{{ stats.notReady }}</span>
          <span :class="tileClass.statLabel">not ready</span>
        </span>
      </div>

      <div v-if="rows.length">
        <div :class="tileClass.sectionLabel">Repositories</div>
        <ul :class="tileClass.list">
          <li v-for="repo in rows" :key="repo.name">
            <button
              type="button"
              :class="tileClass.row"
              @click="navigateFromTile(rootRef, `repositories/${repo.name}`)"
            >
              <!-- Dot carries the ready state; repeating it as a word would
                   be two indicators for one fact. The trailing slot shows the
                   connection instead, which is what you actually need when a
                   repository is not ready. -->
              <span
                :class="[tileClass.rowDot, repo.ready ? 'bg-success' : 'bg-warning']"
                aria-hidden="true"
              />
              <span :class="tileClass.rowPrimary">{{ repo.repo || repo.name }}</span>
              <span :class="tileClass.rowSecondary">{{ repo.connectionRef }}</span>
              <ChevronRight :class="tileClass.chevron" />
            </button>
          </li>
        </ul>
      </div>

      <div v-else-if="stats.connections === 0" :class="tileClass.empty">
        No git connection yet — connect a provider to create repositories.
      </div>
      <div v-else :class="tileClass.empty">No repositories yet.</div>
    </template>
  </div>
</template>
