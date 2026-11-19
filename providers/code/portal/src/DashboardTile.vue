<script setup lang="ts">
import LoadingSkeleton from './portalkit/LoadingSkeleton.vue'
// Dashboard tile for the code provider, mounted by
// <railgrid-dashboard-tile-code> (see element.ts).
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

import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { AlertTriangle, ChevronRight, Clock, Link2, Package } from 'lucide-vue-next'
import { api } from './api'
import type { Connection, Repository } from './types'
import {
  hasWorkspaceContext,
  isBenignTileError,
  mostRecent,
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
const repositories = ref<Repository[]>([])
const connections = ref<Connection[]>([])
const loading = ref(true)
const loaded = ref(false)
const error = ref<string | null>(null)
const retryPending = ref(false)
const retryError = ref<string | null>(null)
const displayError = computed(() => retryPending.value ? retryError.value : error.value)
let retryGeneration = 0
let refresh!: LatestRefreshController
const poller = createAdaptiveRefreshTimer(() => load('background'), () => {
  if (!loaded.value || error.value) return FAST_REFRESH_MS
  const pending = repositories.value.some(repository => !repository.ready || !!repository.deletionTimestamp) ||
    connections.value.some(connection => !connection.validated || !!connection.deletionTimestamp)
  return pending ? FAST_REFRESH_MS : STABLE_REFRESH_MS
})

const stats = computed(() => {
  const repos = repositories.value.length
  const broken = connections.value.filter((c) => !c.validated).length
  const notReady = repositories.value.filter((r) => !r.ready).length
  return { repos, connections: connections.value.length, broken, notReady }
})

// Repositories carry no timestamp in the list projection, so order by name for
// a stable list rather than faking recency.
const rows = computed(() => mostRecent(repositories.value, (r) => r.name))

function load(mode: 'foreground' | 'background' = 'foreground') {
  void refresh.request(mode)
}

async function retry(): Promise<void> {
  if (retryPending.value) return
  const generation = ++retryGeneration
  const focused = document.activeElement
  retryError.value = error.value
  retryPending.value = true
  await refresh.request('foreground')
  if (generation !== retryGeneration) return
  retryPending.value = false
  await nextTick()
  if (focused && !focused.isConnected && document.activeElement === document.body) rootRef.value?.focus()
}

refresh = createLatestRefreshController(async (requestID, _mode) => {
  const ctx = props.context
  if (!hasWorkspaceContext(ctx)) {
    if (!refresh.isCurrent(requestID)) return
    repositories.value = []
    connections.value = []
    error.value = null
    loaded.value = true
    loading.value = false
    poller.schedule()
    return
  }
  loading.value = true
  try {
    // Both lists in parallel: the tile is worthless without the connection
    // health, and serialising doubles the time the card sits on "Loading".
    const readContext = { fetch: ctx?.fetch ?? null, token: ctx?.token ?? null, tenant: ctx?.tenant ?? null }
    const [repos, conns] = await Promise.all([api.listRepositories(readContext), api.listConnections(readContext)])
    if (!refresh.isCurrent(requestID)) return
    repositories.value = repos
    connections.value = conns
    error.value = null
    loaded.value = true
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    error.value = !loaded.value && isBenignTileError(e) ? null : tileErrorText(e)
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
  retryGeneration += 1
  poller.stop()
  refresh.stop()
})
watch(
  [() => props.context?.tenant, () => props.context?.orgUUID, () => props.context?.workspaceUUID,
    () => props.context?.user?.userId || props.context?.user?.sub || props.context?.user?.email,
    () => props.context?.fetch,
    () => typeof props.context?.fetch === 'function' ? null : props.context?.token],
  () => {
    retryGeneration += 1
    retryPending.value = false
    retryError.value = null
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
  <div ref="rootRef" :class="tileClass.root" :aria-busy="loading || retryPending" tabindex="-1">
    <div v-if="displayError" :class="tileClass.error" :role="loaded ? 'status' : 'alert'" aria-live="polite">{{ loaded ? 'Could not refresh. Showing cached data.' : 'Could not load repositories.' }} {{ displayError }} <button type="button" class="k-dashboard-action" :aria-disabled="retryPending || undefined" :aria-busy="retryPending || undefined" @click="retry">{{ retryPending ? 'Retrying…' : 'Retry' }}</button></div>
    <LoadingSkeleton v-if="loading && !loaded && !displayError" label="Loading repositories&hellip;" compact />

    <template v-if="loaded">
      <span v-if="loading && !displayError" class="sr-only" role="status" aria-live="polite">Updating repositories…</span>
      <div :class="tileClass.stats">
        <span :class="[tileClass.stat, tileClass.statTotal]">
          <Package :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
          <span :class="tileClass.statNum">{{ stats.repos }}</span>
          <span :class="tileClass.statLabel">{{ stats.repos === 1 ? 'repository' : 'repositories' }}</span>
        </span>
        <span :class="[tileClass.stat, tileClass.statMuted]">
          <Link2 :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
          <span class="tabular-nums">{{ stats.connections }}</span>
          <span>{{ stats.connections === 1 ? 'connection' : 'connections' }}</span>
        </span>
        <!-- A broken connection is the failure everything else inherits, so it
             is the one number that earns colour on this tile. -->
        <span v-if="stats.broken > 0" :class="[tileClass.stat, tileClass.statBad]">
          <AlertTriangle :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
          <span class="tabular-nums">{{ stats.broken }}</span>
          <span :class="tileClass.statLabel">not validated</span>
        </span>
        <span v-if="stats.notReady > 0" :class="[tileClass.stat, tileClass.statWarn]">
          <Clock :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
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
              :aria-label="`Open repository ${repo.repo || repo.name}, ${repo.ready ? 'ready' : 'not ready'}, connection ${repo.connectionRef}`"
              @click="navigateFromTile(rootRef, `repositories/${repo.name}`)"
            >
              <span
                :class="[tileClass.rowDot, repo.ready ? 'bg-success' : 'bg-warning']"
                aria-hidden="true"
              />
              <span :class="tileClass.rowPrimary">{{ repo.repo || repo.name }}</span>
              <span :class="tileClass.rowSecondary">{{ repo.connectionRef }} · {{ repo.ready ? 'Ready' : 'Not ready' }}</span>
              <ChevronRight :class="tileClass.chevron" :stroke-width="1.75" aria-hidden="true" />
            </button>
          </li>
        </ul>
      </div>

      <div v-else-if="stats.connections === 0" :class="tileClass.empty" role="status" aria-live="polite">
        No git connection yet — connect a provider to create repositories.
      </div>
      <div v-else :class="tileClass.empty" role="status" aria-live="polite">No repositories yet.</div>
    </template>
  </div>
</template>
