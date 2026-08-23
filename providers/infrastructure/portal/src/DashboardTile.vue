<script setup lang="ts">
// Tile content for the infrastructure provider's dashboard summary.
// Mounted by <faros-dashboard-tile-infrastructure> (see element.ts).
//
// Gives the user an at-a-glance read on what they've provisioned in the
// CURRENT workspace:
//   - total instances + per-phase breakdown (Ready / Pending / Deleting / Failed)
//   - top-4 most-recent instances with template + phase chip and a
//     click-through that bubbles faros-navigate up to the portal so it
//     pushes /providers/infrastructure/instances/<name>.
//
// Auth + workspace headers come from the farosContext the host pushed
// onto the element and the standard portal tenant slot in localStorage
// (same shape api.ts reads in App.vue). The tile is read-only — even if
// the workspace isn't bootstrapped yet (X-Faros-Tenant resolver returns
// nothing), we just render an empty state instead of bubbling errors.

import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowRight } from 'lucide-vue-next'
import { AlertTriangle, Check, ChevronRight, Clock, Package } from 'lucide-vue-next'
import { api, isContextChangedError, setTenant, setToken } from './api'
import { tileClass } from './portalkit/dashboardtile'
import { createLatestRefreshController, createResourceTombstones } from './refresh'
import type { Instance } from './types'

interface FarosContext {
  token?: string | null
  // tenant is the kcp cluster name (auth.clusterName in the shell).
  // Used as the /graphql/<cluster> path segment for gateway calls.
  tenant?: string | null
  basePath?: string
}

const props = defineProps<{ context: FarosContext | null }>()
const rootRef = ref<HTMLElement | null>(null)

const instances = ref<Instance[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
let pollHandle: number | null = null
const tombstones = createResourceTombstones()
let tombstoneTenant: string | null | undefined

function phaseFor(instance: Instance): string {
  return instance.deletionTimestamp || tombstones.has(instance.name, instance.uid)
    ? 'Deleting'
    : instance.phase
}

const stats = computed(() => {
  const total = instances.value.length
  const ready = instances.value.filter((i) => phaseFor(i) === 'Ready').length
  const pending = instances.value.filter((i) => phaseFor(i) === 'Pending').length
  const deleting = instances.value.filter((i) => phaseFor(i) === 'Deleting').length
  const failed = instances.value.filter((i) => phaseFor(i) === 'Failed').length
  return { total, ready, pending, deleting, failed }
})

// Most-recent first, capped at 4 so the tile stays a fixed height.
const recent = computed(() =>
  [...instances.value]
    .sort((a, b) => (b.createdAt || '').localeCompare(a.createdAt || ''))
    .slice(0, 4),
)

// Refresh delegates to the shared GraphQL client in api.ts. The shell
// pushes the same token + tenant (auth.clusterName) to every mounted
// element, so even when the tile and the full provider page are open
// at the same time both end up with the same module state.
const refresh = createLatestRefreshController(async requestID => {
  const ctx = props.context
  if (!ctx) {
    if (refresh.isCurrent(requestID)) loading.value = true
    return
  }
  if (!ctx.tenant) {
    // No workspace selected yet — render the empty state rather than
    // querying the gateway without a cluster.
    instances.value = []
    error.value = null
    loading.value = false
    loaded.value = true
    return
  }
  loading.value = true
  try {
    setToken(ctx.token ?? null)
    setTenant(ctx.tenant ?? null)
    const r = await api.listInstances()
    if (!refresh.isCurrent(requestID)) return
    for (const instance of r.items) {
      if (instance.deletionTimestamp) tombstones.add(instance.name, instance.uid)
    }
    tombstones.reconcile(r.identities)
    instances.value = r.items
    error.value = null
    loaded.value = true
  } catch (e) {
    if (!refresh.isCurrent(requestID) || isContextChangedError(e)) return
    const err = e as { reason?: string; message?: string } | Error
    const reason = (err as { reason?: string }).reason
    const message = (err as { message?: string }).message ?? String(err)
    if (reason === 'APIBindingMissing' || reason === 'TenantMissing') {
      // Tenant has no binding yet (or never selected a workspace) —
      // empty tile is the right state, not an error banner.
      instances.value = []
      error.value = null
      loaded.value = true
    } else {
      error.value = `${reason ?? 'Error'} — ${message}`
      // eslint-disable-next-line no-console
      console.warn('infrastructure tile listInstances failed', err)
    }
  } finally {
    if (refresh.isCurrent(requestID)) loading.value = false
  }
})

function load(): Promise<void> {
  return refresh.request()
}

function dispatchNavigate(path: string) {
  // CustomEvent bubbles up through the mount div → the portal-side
  // DashboardTile.vue listener → router.push('/providers/infrastructure/'+path).
  rootRef.value?.dispatchEvent(
    new CustomEvent('faros-navigate', { detail: { path }, bubbles: true }),
  )
}

function openInstance(instance: Instance) {
  if (phaseFor(instance) === 'Deleting') return
  dispatchNavigate('instances/' + encodeURIComponent(instance.name))
}

onMounted(() => {
  // 30s poll matches the catalog/instance list cadence in the main app —
  // anything tighter wastes the hub roundtrips for a tile users glance at.
  pollHandle = window.setInterval(() => { void load() }, 30000)
})
onUnmounted(() => {
  if (pollHandle !== null) window.clearInterval(pollHandle)
  refresh.stop()
})
watch(
  () => [props.context === null, props.context?.tenant, props.context?.token, props.context?.basePath] as const,
  () => {
    if (tombstoneTenant !== props.context?.tenant) tombstones.clear()
    tombstoneTenant = props.context?.tenant
    refresh.invalidate()
    instances.value = []
    error.value = null
    loaded.value = false
    loading.value = props.context === null
    void load()
  },
  { immediate: true },
)

// Phase → dot colour. Unknown phases fall through to the neutral bucket so a
// future kro phase string doesn't render as "Failed" by mistake.
const phaseDot: Record<string, string> = {
  Ready: 'bg-success',
  Pending: 'bg-text-muted',
  Deleting: 'bg-warning',
  Failed: 'bg-danger',
}
function dotFor(phase: string) {
  return phaseDot[phase] ?? 'bg-text-muted'
}
</script>

<template>
  <div ref="rootRef" :class="tileClass.root" :aria-busy="loading">
    <div v-if="!loaded && loading" class="space-y-2" role="status" aria-live="polite" aria-busy="true" aria-label="Loading infrastructure instances">
      <div class="shimmer h-3 w-32 rounded" aria-hidden="true" />
      <div v-for="i in 4" :key="i" class="shimmer h-7 w-full rounded" aria-hidden="true" />
    </div>
    <div v-else-if="!loaded && error" :class="tileClass.error" role="alert" aria-live="assertive">
      Failed to load: {{ error }}
      <button type="button" class="ml-2 font-medium underline" @click="load">Retry</button>
    </div>

    <template v-else>
      <span v-if="loading" class="sr-only" role="status" aria-live="polite">Updating infrastructure instances…</span>
      <div v-if="error" :class="tileClass.error" role="alert" aria-live="assertive">
        Showing the last successful result. {{ error }}
        <button type="button" class="ml-2 font-medium underline" @click="load">Retry</button>
      </div>
      <!-- Slim horizontal status row (matches the clusters/edges tiles): a
           single inline line of icon + count + label chips rather than four
           stacked boxes, so the tile stays compact. -->
      <div :class="tileClass.stats">
        <span :class="[tileClass.stat, tileClass.statTotal]">
          <Package :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
          <span :class="tileClass.statNum">{{ stats.total }}</span>
          <span :class="tileClass.statLabel">total</span>
        </span>
        <span :class="[tileClass.stat, tileClass.statOk]">
          <Check :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
          <span class="tabular-nums">{{ stats.ready }}</span>
          <span :class="tileClass.statLabel">ready</span>
        </span>
        <span v-if="stats.pending > 0" :class="[tileClass.stat, tileClass.statMuted]">
          <Clock :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
          <span class="tabular-nums">{{ stats.pending }}</span>
          <span :class="tileClass.statLabel">pending</span>
        </span>
        <span v-if="stats.deleting > 0" :class="[tileClass.stat, tileClass.statWarn]">
          <Clock :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
          <span class="tabular-nums">{{ stats.deleting }}</span>
          <span :class="tileClass.statLabel">deleting</span>
        </span>
        <span v-if="stats.failed > 0" :class="[tileClass.stat, tileClass.statBad]">
          <AlertTriangle :class="tileClass.statIcon" :stroke-width="1.75" aria-hidden="true" />
          <span class="tabular-nums">{{ stats.failed }}</span>
          <span :class="tileClass.statLabel">failed</span>
        </span>
      </div>

      <!-- Recent instances. Click anywhere on the row → instance detail
           page. Bubbles via faros-navigate so the portal owns the URL.
           Row style matches the kubernetes-edges "Recent" list: a single
           compact line per item (phase icon · name · template · animated
           chevron) so the dashboard reads consistently across providers. -->
      <div v-if="recent.length > 0">
        <div :class="tileClass.sectionLabel">Recent</div>
        <ul :class="tileClass.list">
          <li v-for="i in recent" :key="i.uid ?? i.name">
            <button
              type="button"
              :class="tileClass.row"
              :disabled="phaseFor(i) === 'Deleting'"
              :title="phaseFor(i) === 'Deleting' ? 'Deletion in progress' : undefined"
              @click="openInstance(i)"
            >
              <span :class="[tileClass.rowDot, dotFor(phaseFor(i))]" aria-hidden="true" />
              <span :class="tileClass.rowPrimary">{{ i.name }}</span>
              <span :class="tileClass.rowSecondary">
                {{ i.template }}<template v-if="phaseFor(i) === 'Deleting'"> · Deleting</template>
              </span>
              <ChevronRight v-if="phaseFor(i) !== 'Deleting'" :class="tileClass.chevron" :stroke-width="2" />
            </button>
          </li>
        </ul>
      </div>

      <!-- Explicit empty state. The "scope hint" line covers a real
           migration footgun: instances provisioned before the user
           picked a workspace in the sidebar landed in the personal-org
           scope (no X-Faros-Workspace header), and the workspace-aware
           list now reads from a different namespace. The pointer to
           the Instances page lets them at least see their stranded
           CRs via the "no workspace" view there. -->
      <div v-else :class="tileClass.empty">
        <div>
          No instances yet in this workspace.
          <button
            type="button"
            class="ml-1 font-medium text-accent transition-colors hover:text-accent-hover"
            @click="dispatchNavigate('templates')"
          >
            Browse templates <ArrowRight :size="14" aria-hidden="true" />
          </button>
        </div>
        <div class="mt-1 text-text-muted/70">
          Provisioned before picking a workspace?
          <button
            type="button"
            class="font-medium text-accent transition-colors hover:text-accent-hover"
            @click="dispatchNavigate('instances')"
          >
            Open Instances <ArrowRight :size="14" aria-hidden="true" />
          </button>
        </div>
      </div>
    </template>
  </div>
</template>
