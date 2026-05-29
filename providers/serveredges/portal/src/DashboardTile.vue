<script setup lang="ts">
// Tile content for the server-edges dashboard summary. Same shape as
// kubernetes-edges' tile minus the workloads section (workloads are
// kube-only today). Mounted by <kedge-dashboard-tile-server-edges>.

import { computed, watch, inject } from 'vue'
import { useAuthStore, type KedgeContext } from './auth-adapter'
import { useGraphQLQuery } from '@/composables/useGraphQL'
import { LIST_EDGES, type ListEdgesResult } from '@/graphql/queries/edges'
import { Server, Wifi, WifiOff, ChevronRight } from 'lucide-vue-next'

const props = defineProps<{ context: KedgeContext | null }>()
const auth = useAuthStore()

watch(() => props.context, (ctx) => auth.hydrate(ctx), { immediate: true })

const { data, loading, error } = useGraphQLQuery<ListEdgesResult>(LIST_EDGES, undefined, 30000)

const servers = computed(() =>
  (data.value?.kedge_faros_sh?.v1alpha1?.Edges?.items ?? []).filter((e) => e.spec?.type === 'server'),
)

const stats = computed(() => {
  const total = servers.value.length
  const ready = servers.value.filter((e) => e.status?.phase === 'Ready').length
  const connected = servers.value.filter((e) => e.status?.connected).length
  const healthPct = total === 0 ? 0 : Math.round((ready / total) * 100)
  return { total, ready, connected, healthPct }
})

const recent = computed(() =>
  [...servers.value]
    .sort((a, b) => (b.metadata.creationTimestamp ?? '').localeCompare(a.metadata.creationTimestamp ?? ''))
    .slice(0, 3),
)

const dispatchNavigate = inject<(path: string) => void>('dispatchNavigate', () => {})

const healthColor = computed(() => {
  const p = stats.value.healthPct
  if (p >= 80) return 'bg-success'
  if (p >= 50) return 'bg-warning'
  return 'bg-danger'
})

// Agent version distribution as a quick "do I have stragglers" signal.
// Sorts by count desc so the dominant version surfaces first.
const versionDist = computed(() => {
  const m = new Map<string, number>()
  for (const s of servers.value) {
    const v = s.status?.agentVersion || 'unknown'
    m.set(v, (m.get(v) ?? 0) + 1)
  }
  return [...m.entries()].sort((a, b) => b[1] - a[1]).slice(0, 3)
})
</script>

<template>
  <div v-if="error" class="text-[11px] text-danger">{{ error }}</div>
  <div v-else-if="loading && !data" class="text-[11px] text-text-muted">Loading servers&hellip;</div>
  <div v-else-if="servers.length === 0" class="space-y-2">
    <div class="text-[11px] text-text-muted">No servers connected yet.</div>
    <button
      type="button"
      class="text-[11px] font-medium text-accent hover:text-accent-hover"
      @click="dispatchNavigate('')"
    >
      Connect your first server &rarr;
    </button>
  </div>

  <div v-else class="space-y-4">
    <!-- Stat trio + health bar — same shape as kubernetes-edges' tile
         so the user reads both at a glance with the same mental model. -->
    <div>
      <div class="mb-2 flex items-center gap-1.5">
        <Server class="h-3 w-3 text-text-muted" :stroke-width="1.75" />
        <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Servers</span>
      </div>
      <div class="grid grid-cols-3 gap-2">
        <div class="rounded-lg border border-border-subtle bg-surface-overlay/40 p-2">
          <div class="text-[9px] uppercase tracking-wider text-text-muted/70">Total</div>
          <div class="mt-0.5 text-lg font-bold tabular-nums text-text-primary">{{ stats.total }}</div>
        </div>
        <div class="rounded-lg border border-border-subtle bg-surface-overlay/40 p-2">
          <div class="text-[9px] uppercase tracking-wider text-text-muted/70">Ready</div>
          <div
            class="mt-0.5 text-lg font-bold tabular-nums"
            :class="stats.ready === stats.total ? 'text-success' : 'text-warning'"
          >
            {{ stats.ready }}
          </div>
        </div>
        <div class="rounded-lg border border-border-subtle bg-surface-overlay/40 p-2">
          <div class="text-[9px] uppercase tracking-wider text-text-muted/70">Online</div>
          <div
            class="mt-0.5 text-lg font-bold tabular-nums"
            :class="stats.connected === stats.total ? 'text-success' : 'text-warning'"
          >
            {{ stats.connected }}
          </div>
        </div>
      </div>
      <div class="mt-2 flex items-center gap-2">
        <div class="h-1 flex-1 overflow-hidden rounded-full bg-surface-overlay">
          <div class="h-full rounded-full transition-all duration-500" :class="healthColor" :style="{ width: `${stats.healthPct}%` }" />
        </div>
        <span class="text-[10px] tabular-nums text-text-muted">{{ stats.healthPct }}% healthy</span>
      </div>
    </div>

    <!-- Agent version distribution — quick visibility into upgrade lag.
         Hidden when only one distinct version (the normal case). -->
    <div v-if="versionDist.length > 1">
      <div class="mb-1 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Agent versions</div>
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px]">
        <span v-for="[ver, count] in versionDist" :key="ver" class="text-text-muted">
          <span class="font-mono text-text-secondary">{{ ver }}</span>
          <span class="ml-1 tabular-nums">×{{ count }}</span>
        </span>
      </div>
    </div>

    <div>
      <div class="mb-2 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Recent</div>
      <ul class="space-y-1">
        <li v-for="edge in recent" :key="edge.metadata.name">
          <button
            type="button"
            class="group flex w-full items-center gap-2 rounded-lg border border-border-subtle bg-surface-overlay/40 px-2.5 py-1.5 text-left transition-colors hover:bg-accent/[0.04]"
            @click="dispatchNavigate(edge.metadata.name)"
          >
            <Server class="h-3 w-3 shrink-0 text-text-muted" :stroke-width="1.75" />
            <span class="min-w-0 flex-1 truncate text-[12px] text-text-primary">{{ edge.metadata.name }}</span>
            <component
              :is="edge.status?.connected ? Wifi : WifiOff"
              class="h-3 w-3 shrink-0"
              :class="edge.status?.connected ? 'text-success' : 'text-text-muted/60'"
              :stroke-width="1.75"
            />
            <ChevronRight class="h-3 w-3 shrink-0 text-text-muted/30 transition-all group-hover:translate-x-0.5 group-hover:text-accent/60" :stroke-width="1.75" />
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>
