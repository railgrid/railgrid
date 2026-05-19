<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import FirstEdgeWizard from '@/components/FirstEdgeWizard.vue'
import { useGraphQLQuery } from '@/composables/useGraphQL'
import { LIST_EDGES, type ListEdgesResult } from '@/graphql/queries/edges'
import { LIST_VIRTUAL_WORKLOADS, type ListVirtualWorkloadsResult } from '@/graphql/queries/workloads'
import { formatAge } from '@/utils/time'
import { Server, Wifi, WifiOff, ChevronRight, Activity, Gauge, Layers } from 'lucide-vue-next'

const { data, loading, error, refetch } = useGraphQLQuery<ListEdgesResult>(LIST_EDGES, undefined, 10000)
const { data: workloadsData } = useGraphQLQuery<ListVirtualWorkloadsResult>(LIST_VIRTUAL_WORKLOADS, undefined, 10000)

const edges = computed(() => data.value?.kedge_faros_sh?.v1alpha1?.Edges?.items ?? [])
const workloads = computed(() => workloadsData.value?.kedge_faros_sh?.v1alpha1?.VirtualWorkloads?.items ?? [])

// Latch the wizard decision on first load. Once the wizard is open, it stays open
// (even after the new edge appears in LIST_EDGES) until the wizard itself completes.
const wizardOpen = ref<boolean | null>(null)
watch(
  [loading, error, edges],
  () => {
    if (wizardOpen.value !== null) return
    if (loading.value) return
    if (error.value) return
    wizardOpen.value = edges.value.length === 0
  },
  { immediate: true },
)
const showWizard = computed(() => wizardOpen.value === true)

function onWizardConnected() {
  wizardOpen.value = false
  refetch()
}

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

const recentEdges = computed(() =>
  [...edges.value]
    .sort((a, b) => (b.metadata.creationTimestamp ?? '').localeCompare(a.metadata.creationTimestamp ?? ''))
    .slice(0, 8),
)

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

    <div v-else-if="showWizard" class="py-8">
      <FirstEdgeWizard @connected="onWizardConnected" />
    </div>

    <template v-else>
      <div class="dashboard-grid">
        <!-- Fleet health compact panel -->
        <div
          class="border-beam stagger-item rounded-xl border border-border-subtle bg-surface-raised/80 p-3 backdrop-blur"
          style="animation-delay: 0ms"
        >
          <div class="flex items-center gap-1.5">
            <Gauge class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Fleet Health</span>
          </div>
          <div class="mt-1.5 flex items-baseline gap-1">
            <span class="text-gradient text-2xl font-bold tabular-nums tracking-tight">{{ healthPct }}</span>
            <span class="text-sm font-light text-text-muted">%</span>
            <span class="ml-auto text-[11px] tabular-nums text-text-muted">{{ stats.ready }}/{{ stats.total }}</span>
          </div>
          <div class="mt-2 h-1 w-full overflow-hidden rounded-full bg-surface-overlay">
            <div
              class="h-full rounded-full transition-all duration-500"
              :class="healthPct >= 80 ? 'bg-success' : healthPct >= 50 ? 'bg-warning' : 'bg-danger'"
              :style="{ width: `${healthPct}%` }"
            />
          </div>
        </div>

        <!-- Edge status compact: ready/total -->
        <div
          class="tilt-card stagger-item rounded-xl border border-border-subtle bg-surface-raised/80 p-3 backdrop-blur"
          style="animation-delay: 60ms"
        >
          <div class="flex items-center gap-1.5">
            <Server class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Edge Status</span>
          </div>
          <div class="mt-1.5 flex items-baseline gap-1">
            <span class="text-2xl font-bold tabular-nums" :class="stats.notReady === 0 && stats.total > 0 ? 'text-success' : stats.ready === 0 ? 'text-danger' : 'text-warning'">
              {{ stats.ready }}<span class="text-text-muted/60">/</span>{{ stats.total }}
            </span>
            <span class="ml-auto text-[11px] font-medium" :class="stats.notReady > 0 ? 'text-danger' : 'text-success'">
              {{ stats.notReady > 0 ? `${stats.notReady} not ready` : 'All ready' }}
            </span>
          </div>
          <div class="mt-2 flex h-1 w-full gap-0.5 overflow-hidden rounded-full bg-surface-overlay">
            <div v-if="stats.ready > 0" class="h-full bg-success transition-all duration-500" :style="{ width: `${(stats.ready / Math.max(stats.total, 1)) * 100}%` }" />
            <div v-if="stats.notReady > 0" class="h-full bg-danger transition-all duration-500" :style="{ width: `${(stats.notReady / Math.max(stats.total, 1)) * 100}%` }" />
          </div>
        </div>

        <!-- Connectivity compact: online/total -->
        <div
          class="tilt-card stagger-item rounded-xl border border-border-subtle bg-surface-raised/80 p-3 backdrop-blur"
          style="animation-delay: 120ms"
        >
          <div class="flex items-center gap-1.5">
            <Wifi class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Connectivity</span>
          </div>
          <div class="mt-1.5 flex items-baseline gap-1">
            <span class="text-2xl font-bold tabular-nums" :class="stats.disconnected === 0 && stats.total > 0 ? 'text-success' : stats.connected === 0 ? 'text-danger' : 'text-warning'">
              {{ stats.connected }}<span class="text-text-muted/60">/</span>{{ stats.total }}
            </span>
            <span class="ml-auto text-[11px] font-medium" :class="stats.disconnected > 0 ? 'text-danger' : 'text-success'">
              {{ stats.disconnected > 0 ? `${stats.disconnected} offline` : 'All online' }}
            </span>
          </div>
          <div class="mt-2 flex h-1 w-full gap-0.5 overflow-hidden rounded-full bg-surface-overlay">
            <div v-if="stats.connected > 0" class="h-full bg-success transition-all duration-500" :style="{ width: `${(stats.connected / Math.max(stats.total, 1)) * 100}%` }" />
            <div v-if="stats.disconnected > 0" class="h-full bg-danger transition-all duration-500" :style="{ width: `${(stats.disconnected / Math.max(stats.total, 1)) * 100}%` }" />
          </div>
        </div>

        <!-- Workloads compact: running/total -->
        <div
          class="tilt-card stagger-item rounded-xl border border-border-subtle bg-surface-raised/80 p-3 backdrop-blur"
          style="animation-delay: 180ms"
        >
          <div class="flex items-center gap-1.5">
            <Layers class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Workloads</span>
            <router-link to="/workloads" class="ml-auto text-[10px] font-medium text-accent transition-colors hover:text-accent-hover">
              View all &rarr;
            </router-link>
          </div>
          <div class="mt-1.5 flex items-baseline gap-1">
            <span class="text-2xl font-bold tabular-nums" :class="workloadStats.total === 0 ? 'text-text-muted' : workloadStats.running === workloadStats.total ? 'text-success' : 'text-warning'">
              {{ workloadStats.running }}<span class="text-text-muted/60">/</span>{{ workloadStats.total }}
            </span>
            <span class="ml-auto flex items-center gap-2 text-[11px] tabular-nums">
              <span v-if="workloadStats.pending > 0" class="text-warning">{{ workloadStats.pending }} pending</span>
              <span class="text-text-muted">{{ workloadStats.totalEdges }} placements</span>
            </span>
          </div>
          <div class="mt-2 flex h-1 w-full gap-0.5 overflow-hidden rounded-full bg-surface-overlay">
            <div v-if="workloadStats.running > 0" class="h-full bg-success transition-all duration-500" :style="{ width: `${(workloadStats.running / Math.max(workloadStats.total, 1)) * 100}%` }" />
            <div v-if="workloadStats.pending > 0" class="h-full bg-warning transition-all duration-500" :style="{ width: `${(workloadStats.pending / Math.max(workloadStats.total, 1)) * 100}%` }" />
            <div v-if="workloadStats.failed > 0" class="h-full bg-danger transition-all duration-500" :style="{ width: `${(workloadStats.failed / Math.max(workloadStats.total, 1)) * 100}%` }" />
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
              <span class="text-[10px] tabular-nums text-text-muted/60">({{ recentEdges.length }}/{{ edges.length }})</span>
            </div>
            <router-link to="/edges" class="text-[11px] font-medium text-accent transition-colors hover:text-accent-hover">
              View all &rarr;
            </router-link>
          </div>

          <!-- Column headers (lg+ only; below that the rows just stack key info) -->
          <div class="mt-4 hidden lg:grid grid-cols-[minmax(0,1fr)_140px_130px_80px_70px_100px_16px] items-center gap-4 px-4 pb-2 text-[9px] font-semibold uppercase tracking-[0.15em] text-text-muted/60">
            <span>Edge</span>
            <span>Connection</span>
            <span>Agent</span>
            <span>Heartbeat</span>
            <span>Age</span>
            <span class="text-right">Status</span>
            <span aria-hidden="true" />
          </div>

          <div class="mt-1 space-y-1 lg:mt-0">
            <router-link
              v-for="(edge, i) in recentEdges"
              :key="edge.metadata.name"
              :to="`/edges/${edge.metadata.name}`"
              class="card-glow stagger-item group flex items-center justify-between gap-4 rounded-xl border border-border-subtle bg-surface-overlay/50 px-4 py-2.5 transition-all duration-150 hover:bg-accent/[0.03] lg:grid lg:grid-cols-[minmax(0,1fr)_140px_130px_80px_70px_100px_16px]"
              :style="{ animationDelay: `${(i + 5) * 40}ms` }"
            >
              <!-- Identity: name + type subtitle -->
              <div class="flex min-w-0 items-center gap-3">
                <Server class="h-3.5 w-3.5 shrink-0 text-text-muted transition-colors group-hover:text-accent" :stroke-width="1.75" />
                <div class="min-w-0 leading-tight">
                  <div class="truncate text-[13px] font-medium text-text-primary">{{ edge.metadata.name }}</div>
                  <div class="font-mono text-[10px] text-text-muted">{{ edge.spec?.type ?? '—' }}</div>
                </div>
              </div>

              <!-- Connection -->
              <div class="hidden items-center gap-1.5 text-[11px] lg:flex" :class="edge.status?.connected ? 'text-success' : 'text-danger'">
                <component :is="edge.status?.connected ? Wifi : WifiOff" class="h-3 w-3 shrink-0" :stroke-width="1.75" />
                <span>{{ edge.status?.connected ? 'Connected' : 'Disconnected' }}</span>
              </div>

              <!-- Agent version -->
              <span class="hidden truncate font-mono text-[11px] text-text-muted lg:inline">{{ edge.status?.agentVersion || '—' }}</span>

              <!-- Last heartbeat -->
              <span class="hidden tabular-nums text-[11px] text-text-muted lg:inline">{{ edge.status?.lastHeartbeatTime ? formatAge(edge.status.lastHeartbeatTime) : '—' }}</span>

              <!-- Age -->
              <span class="hidden tabular-nums text-[11px] text-text-muted lg:inline">{{ formatAge(edge.metadata.creationTimestamp) }}</span>

              <!-- Status badge -->
              <div class="flex justify-end">
                <StatusBadge :status="edge.status?.phase" :connected="edge.status?.connected" />
              </div>

              <!-- Chevron -->
              <ChevronRight class="h-3.5 w-3.5 shrink-0 text-text-muted/20 transition-all group-hover:translate-x-0.5 group-hover:text-accent/50" :stroke-width="1.75" />
            </router-link>
          </div>
        </div>
      </div>
    </template>
  </AppLayout>
</template>
