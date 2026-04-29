<script setup lang="ts">
import { computed } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { useGraphQLQuery } from '@/composables/useGraphQL'
import { LIST_EDGES, type ListEdgesResult } from '@/graphql/queries/edges'
import { LIST_VIRTUAL_WORKLOADS, type ListVirtualWorkloadsResult } from '@/graphql/queries/workloads'
import { Server, Wifi, WifiOff, CheckCircle, XCircle, ChevronRight, Activity, Gauge, Layers } from 'lucide-vue-next'

const { data, loading, error } = useGraphQLQuery<ListEdgesResult>(LIST_EDGES, undefined, 10000)
const { data: workloadsData } = useGraphQLQuery<ListVirtualWorkloadsResult>(LIST_VIRTUAL_WORKLOADS, undefined, 10000)

const edges = computed(() => data.value?.kedge_faros_sh?.v1alpha1?.Edges?.items ?? [])
const workloads = computed(() => workloadsData.value?.kedge_faros_sh?.v1alpha1?.VirtualWorkloads?.items ?? [])

const stats = computed(() => {
  const items = edges.value
  const total = items.length
  const ready = items.filter((e) => e.status?.phase === 'Ready').length
  const connected = items.filter((e) => e.status?.connected).length
  return { total, ready, notReady: total - ready, connected, disconnected: total - connected }
})

const healthPct = computed(() => {
  if (stats.value.total === 0) return 0
  return Math.round((stats.value.ready / stats.value.total) * 100)
})

const workloadStats = computed(() => {
  const total = workloads.value.length
  const running = workloads.value.filter((w) => w.status?.phase === 'Running').length
  const pending = workloads.value.filter((w) => w.status?.phase === 'Pending' || w.status?.phase === 'Scheduling').length
  const failed = workloads.value.filter((w) => w.status?.phase === 'Failed').length
  const totalEdges = workloads.value.reduce((sum, w) => sum + (w.status?.edges?.length ?? 0), 0)
  return { total, running, pending, failed, totalEdges }
})
</script>

<template>
  <AppLayout>
    <div v-if="error" class="flex items-center gap-2 rounded-xl border border-danger/20 bg-danger-subtle p-4 text-[13px] text-danger">
      {{ error }}
    </div>

    <div v-else-if="loading && !data" class="mt-20 flex flex-col items-center justify-center">
      <div class="shimmer h-8 w-8 rounded-xl" />
      <div class="shimmer mt-4 h-3 w-40 rounded" />
    </div>

    <template v-else>
      <div class="dashboard-grid">
        <!-- Fleet health compact panel -->
        <div
          class="border-beam stagger-item rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 0ms"
        >
          <div class="flex items-center gap-2">
            <Gauge class="h-4 w-4 text-accent" :stroke-width="1.75" />
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Fleet Health</span>
          </div>
          <div class="mt-3 flex items-end gap-3">
            <span class="text-gradient text-5xl font-bold tabular-nums tracking-tighter">{{ healthPct }}</span>
            <span class="mb-1 text-xl font-light text-text-muted">%</span>
          </div>
          <!-- Progress bar -->
          <div class="mt-3 h-1.5 w-full overflow-hidden rounded-full bg-surface-overlay">
            <div
              class="h-full rounded-full transition-all duration-500"
              :class="healthPct >= 80 ? 'bg-success' : healthPct >= 50 ? 'bg-warning' : 'bg-danger'"
              :style="{ width: `${healthPct}%` }"
            />
          </div>
          <p class="mt-2 text-[12px] text-text-muted">{{ stats.ready }}/{{ stats.total }} edges ready</p>
        </div>

        <!-- Edge status combined: ready + not ready -->
        <div
          class="tilt-card stagger-item rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 60ms"
        >
          <div class="flex items-center gap-2">
            <Server class="h-4 w-4 text-accent" :stroke-width="1.75" />
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Edge Status</span>
            <span class="ml-auto rounded-md bg-accent-subtle px-1.5 py-0.5 text-[10px] font-semibold tabular-nums text-accent">{{ stats.total }}</span>
          </div>
          <div class="mt-3 flex gap-4">
            <div class="flex-1 rounded-xl bg-success/[0.06] px-3 py-2.5">
              <div class="flex items-center gap-1.5">
                <CheckCircle class="h-3 w-3 text-success" :stroke-width="2" />
                <span class="text-[10px] font-medium uppercase tracking-wider text-success/70">Ready</span>
              </div>
              <span class="mt-1 block text-2xl font-bold tabular-nums text-success">{{ stats.ready }}</span>
            </div>
            <div class="flex-1 rounded-xl bg-danger/[0.06] px-3 py-2.5">
              <div class="flex items-center gap-1.5">
                <XCircle class="h-3 w-3 text-danger" :stroke-width="2" />
                <span class="text-[10px] font-medium uppercase tracking-wider text-danger/70">Not Ready</span>
              </div>
              <span class="mt-1 block text-2xl font-bold tabular-nums text-danger">{{ stats.notReady }}</span>
            </div>
          </div>
        </div>

        <!-- Connection status combined: connected + disconnected -->
        <div
          class="tilt-card stagger-item rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 120ms"
        >
          <div class="flex items-center gap-2">
            <Wifi class="h-4 w-4 text-accent" :stroke-width="1.75" />
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Connectivity</span>
          </div>
          <div class="mt-3 flex gap-4">
            <div class="flex-1 rounded-xl bg-success/[0.06] px-3 py-2.5">
              <div class="flex items-center gap-1.5">
                <Wifi class="h-3 w-3 text-success" :stroke-width="2" />
                <span class="text-[10px] font-medium uppercase tracking-wider text-success/70">Online</span>
              </div>
              <span class="mt-1 block text-2xl font-bold tabular-nums text-success">{{ stats.connected }}</span>
            </div>
            <div class="flex-1 rounded-xl bg-danger/[0.06] px-3 py-2.5">
              <div class="flex items-center gap-1.5">
                <WifiOff class="h-3 w-3 text-danger" :stroke-width="2" />
                <span class="text-[10px] font-medium uppercase tracking-wider text-danger/70">Offline</span>
              </div>
              <span class="mt-1 block text-2xl font-bold tabular-nums text-danger">{{ stats.disconnected }}</span>
            </div>
          </div>
        </div>

        <!-- Workloads combined panel -->
        <div
          class="tilt-card stagger-item rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 180ms"
        >
          <div class="flex items-center gap-2">
            <Layers class="h-4 w-4 text-accent" :stroke-width="1.75" />
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Workloads</span>
            <router-link to="/workloads" class="ml-auto text-[10px] font-medium text-accent transition-colors hover:text-accent-hover">
              View all &rarr;
            </router-link>
          </div>
          <div class="mt-3 grid grid-cols-2 gap-2">
            <div class="rounded-xl bg-surface-overlay/60 px-3 py-2">
              <span class="text-[10px] font-medium text-text-muted">Total</span>
              <span class="block text-xl font-bold tabular-nums text-text-primary">{{ workloadStats.total }}</span>
            </div>
            <div class="rounded-xl bg-success/[0.06] px-3 py-2">
              <span class="text-[10px] font-medium text-success/70">Running</span>
              <span class="block text-xl font-bold tabular-nums text-success">{{ workloadStats.running }}</span>
            </div>
            <div class="rounded-xl bg-warning/[0.06] px-3 py-2">
              <span class="text-[10px] font-medium text-warning/70">Pending</span>
              <span class="block text-xl font-bold tabular-nums text-warning">{{ workloadStats.pending }}</span>
            </div>
            <div class="rounded-xl bg-accent-subtle px-3 py-2">
              <span class="text-[10px] font-medium text-accent/70">Edge Placements</span>
              <span class="block text-xl font-bold tabular-nums text-accent">{{ workloadStats.totalEdges }}</span>
            </div>
          </div>
        </div>

        <!-- Recent edges (full width) -->
        <div
          v-if="edges.length > 0"
          class="stagger-item col-span-full rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 240ms"
        >
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-2">
              <Activity class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
              <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Recent Edges</span>
            </div>
            <router-link to="/edges" class="text-[11px] font-medium text-accent transition-colors hover:text-accent-hover">
              View all &rarr;
            </router-link>
          </div>
          <div class="mt-3 space-y-1">
            <router-link
              v-for="(edge, i) in edges.slice(0, 8)"
              :key="edge.metadata.name"
              :to="`/edges/${edge.metadata.name}`"
              class="card-glow stagger-item group flex items-center justify-between rounded-xl border border-border-subtle bg-surface-overlay/50 px-4 py-2 transition-all duration-150 hover:bg-accent/[0.03]"
              :style="{ animationDelay: `${(i + 5) * 40}ms` }"
            >
              <div class="flex items-center gap-3">
                <Server class="h-3.5 w-3.5 text-text-muted transition-colors group-hover:text-accent" :stroke-width="1.75" />
                <span class="text-[13px] font-medium text-text-primary">{{ edge.metadata.name }}</span>
                <span class="font-mono text-[10px] text-text-muted">{{ edge.spec?.type }}</span>
              </div>
              <div class="flex items-center gap-3">
                <StatusBadge :status="edge.status?.phase" :connected="edge.status?.connected" />
                <ChevronRight class="h-3.5 w-3.5 text-text-muted/20 transition-all group-hover:translate-x-0.5 group-hover:text-accent/50" :stroke-width="1.75" />
              </div>
            </router-link>
          </div>
        </div>
      </div>
    </template>
  </AppLayout>
</template>
