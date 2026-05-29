<script setup lang="ts">
// Tile content for the kubernetes-edges dashboard summary. Mounted by
// the <kedge-dashboard-tile-kubernetes-edges> custom element, which
// owns the Vue app + auth-store hydration the same way
// KubernetesEdgesHost does for the full-page element.
//
// Aims to give an at-a-glance picture of what the user has:
//   - cluster trio (total / ready / connected) + a health bar
//   - workload trio (running / pending / failed) for what's deployed
//   - top recent clusters, each click-through bubbles kedge-navigate.
//
// Click handlers dispatch through the provided `dispatchNavigate` so
// the portal catches them and pushes /providers/kubernetes-edges/{path}.

import { computed, watch, inject } from 'vue'
import { useAuthStore, type KedgeContext } from './auth-adapter'
import { useGraphQLQuery } from '@/composables/useGraphQL'
import { LIST_EDGES, type ListEdgesResult } from '@/graphql/queries/edges'
import { LIST_VIRTUAL_WORKLOADS, type ListVirtualWorkloadsResult } from '@/graphql/queries/workloads'
import { Server, Layers, Wifi, WifiOff, ChevronRight, CheckCircle2, AlertCircle, Clock } from 'lucide-vue-next'

const props = defineProps<{ context: KedgeContext | null }>()
const auth = useAuthStore()

watch(() => props.context, (ctx) => auth.hydrate(ctx), { immediate: true })

const { data: edgesData, loading: edgesLoading, error: edgesError } = useGraphQLQuery<ListEdgesResult>(LIST_EDGES, undefined, 30000)
const { data: workloadsData } = useGraphQLQuery<ListVirtualWorkloadsResult>(LIST_VIRTUAL_WORKLOADS, undefined, 30000)

const clusters = computed(() =>
  (edgesData.value?.kedge_faros_sh?.v1alpha1?.Edges?.items ?? []).filter((e) => e.spec?.type === 'kubernetes'),
)
const workloads = computed(() => workloadsData.value?.kedge_faros_sh?.v1alpha1?.VirtualWorkloads?.items ?? [])

const clusterStats = computed(() => {
  const total = clusters.value.length
  const ready = clusters.value.filter((e) => e.status?.phase === 'Ready').length
  const connected = clusters.value.filter((e) => e.status?.connected).length
  const healthPct = total === 0 ? 0 : Math.round((ready / total) * 100)
  return { total, ready, connected, healthPct }
})

const workloadStats = computed(() => {
  const total = workloads.value.length
  const running = workloads.value.filter((w) => w.status?.phase === 'Running').length
  const pending = workloads.value.filter((w) => w.status?.phase === 'Pending' || w.status?.phase === 'Scheduling').length
  const failed = workloads.value.filter((w) => w.status?.phase === 'Failed').length
  return { total, running, pending, failed }
})

const recent = computed(() =>
  [...clusters.value]
    .sort((a, b) => (b.metadata.creationTimestamp ?? '').localeCompare(a.metadata.creationTimestamp ?? ''))
    .slice(0, 3),
)

const dispatchNavigate = inject<(path: string) => void>('dispatchNavigate', () => {})

// Color the health bar by ready/total ratio. The same thresholds the
// old central dashboard used so the visual ranking stays consistent.
const healthColor = computed(() => {
  const p = clusterStats.value.healthPct
  if (p >= 80) return 'bg-success'
  if (p >= 50) return 'bg-warning'
  return 'bg-danger'
})
</script>

<template>
  <div v-if="edgesError" class="text-[11px] text-danger">{{ edgesError }}</div>
  <div v-else-if="edgesLoading && !edgesData" class="text-[11px] text-text-muted">Loading clusters&hellip;</div>
  <div v-else-if="clusters.length === 0" class="space-y-2">
    <div class="text-[11px] text-text-muted">No Kubernetes clusters connected yet.</div>
    <button
      type="button"
      class="text-[11px] font-medium text-accent hover:text-accent-hover"
      @click="dispatchNavigate('')"
    >
      Connect your first cluster &rarr;
    </button>
  </div>

  <div v-else class="space-y-4">
    <!-- Cluster stat trio + health bar. The bar gives a fast "is the
         fleet OK" read; the numbers carry exact state. -->
    <div>
      <div class="mb-2 flex items-center gap-1.5">
        <Server class="h-3 w-3 text-text-muted" :stroke-width="1.75" />
        <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Clusters</span>
      </div>
      <div class="grid grid-cols-3 gap-2">
        <div class="rounded-lg border border-border-subtle bg-surface-overlay/40 p-2">
          <div class="text-[9px] uppercase tracking-wider text-text-muted/70">Total</div>
          <div class="mt-0.5 text-lg font-bold tabular-nums text-text-primary">{{ clusterStats.total }}</div>
        </div>
        <div class="rounded-lg border border-border-subtle bg-surface-overlay/40 p-2">
          <div class="text-[9px] uppercase tracking-wider text-text-muted/70">Ready</div>
          <div
            class="mt-0.5 text-lg font-bold tabular-nums"
            :class="clusterStats.ready === clusterStats.total ? 'text-success' : 'text-warning'"
          >
            {{ clusterStats.ready }}
          </div>
        </div>
        <div class="rounded-lg border border-border-subtle bg-surface-overlay/40 p-2">
          <div class="text-[9px] uppercase tracking-wider text-text-muted/70">Online</div>
          <div
            class="mt-0.5 text-lg font-bold tabular-nums"
            :class="clusterStats.connected === clusterStats.total ? 'text-success' : 'text-warning'"
          >
            {{ clusterStats.connected }}
          </div>
        </div>
      </div>
      <div class="mt-2 flex items-center gap-2">
        <div class="h-1 flex-1 overflow-hidden rounded-full bg-surface-overlay">
          <div class="h-full rounded-full transition-all duration-500" :class="healthColor" :style="{ width: `${clusterStats.healthPct}%` }" />
        </div>
        <span class="text-[10px] tabular-nums text-text-muted">{{ clusterStats.healthPct }}% healthy</span>
      </div>
    </div>

    <!-- Workload stats. Hidden when there are no workloads at all,
         to keep the empty state quiet rather than nagging. -->
    <div v-if="workloadStats.total > 0">
      <div class="mb-2 flex items-center gap-1.5">
        <Layers class="h-3 w-3 text-text-muted" :stroke-width="1.75" />
        <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Workloads</span>
        <span class="text-[10px] tabular-nums text-text-muted/60">({{ workloadStats.total }})</span>
        <button
          type="button"
          class="ml-auto text-[10px] font-medium text-accent transition-colors hover:text-accent-hover"
          @click="dispatchNavigate('workloads')"
        >
          View all &rarr;
        </button>
      </div>
      <div class="flex items-center gap-3 text-[11px]">
        <span class="inline-flex items-center gap-1 text-success">
          <CheckCircle2 class="h-3 w-3" :stroke-width="1.75" />
          <span class="tabular-nums">{{ workloadStats.running }}</span>
          <span class="text-text-muted">running</span>
        </span>
        <span v-if="workloadStats.pending > 0" class="inline-flex items-center gap-1 text-warning">
          <Clock class="h-3 w-3" :stroke-width="1.75" />
          <span class="tabular-nums">{{ workloadStats.pending }}</span>
          <span class="text-text-muted">pending</span>
        </span>
        <span v-if="workloadStats.failed > 0" class="inline-flex items-center gap-1 text-danger">
          <AlertCircle class="h-3 w-3" :stroke-width="1.75" />
          <span class="tabular-nums">{{ workloadStats.failed }}</span>
          <span class="text-text-muted">failed</span>
        </span>
      </div>
    </div>

    <!-- Recent clusters with click-through. Three lines max so the tile
         stays scannable; "View all" is in the tile header (the portal-
         supplied chrome). -->
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
