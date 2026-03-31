<script setup lang="ts">
import { computed } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { useGraphQLQuery } from '@/composables/useGraphQL'
import { LIST_EDGES, type ListEdgesResult } from '@/graphql/queries/edges'
import { LIST_VIRTUAL_WORKLOADS, type ListVirtualWorkloadsResult } from '@/graphql/queries/workloads'
import { Server, Wifi, WifiOff, CheckCircle, ChevronRight, Activity, Gauge, Layers } from 'lucide-vue-next'

const { data, loading, error } = useGraphQLQuery<ListEdgesResult>(LIST_EDGES, undefined, 10000)
const { data: workloadsData } = useGraphQLQuery<ListVirtualWorkloadsResult>(LIST_VIRTUAL_WORKLOADS, undefined, 10000)

const edges = computed(() => data.value?.kedge_faros_sh?.v1alpha1?.Edges?.items ?? [])
const workloads = computed(() => workloadsData.value?.kedge_faros_sh?.v1alpha1?.VirtualWorkloads?.items ?? [])

const stats = computed(() => {
  const items = edges.value
  const total = items.length
  const ready = items.filter((e) => e.status?.phase === 'Ready').length
  const connected = items.filter((e) => e.status?.connected).length
  return { total, ready, connected, disconnected: total - connected }
})

const healthPct = computed(() => {
  if (stats.value.total === 0) return 0
  return Math.round((stats.value.ready / stats.value.total) * 100)
})

const workloadStats = computed(() => {
  const total = workloads.value.length
  const running = workloads.value.filter((w) => w.status?.phase === 'Running').length
  const totalEdges = workloads.value.reduce((sum, w) => sum + (w.status?.edges?.length ?? 0), 0)
  return { total, running, totalEdges }
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
      <!-- Bento grid -->
      <div class="bento">
        <!-- Hero: fleet health (big, 2x2) -->
        <div
          class="border-beam bento-hero stagger-item flex flex-col justify-between rounded-2xl border border-border-subtle bg-surface-raised/80 p-6 backdrop-blur"
          style="animation-delay: 0ms"
        >
          <div>
            <div class="flex items-center gap-2">
              <Gauge class="h-4 w-4 text-accent" :stroke-width="1.75" />
              <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Fleet Health</span>
            </div>
            <div class="mt-6 flex items-end gap-3">
              <span class="text-gradient text-6xl font-bold tabular-nums tracking-tighter">{{ healthPct }}</span>
              <span class="mb-2 text-2xl font-light text-text-muted">%</span>
            </div>
            <p class="mt-1 text-[13px] text-text-muted">
              {{ stats.ready }} of {{ stats.total }} edges ready
            </p>
          </div>
          <!-- Mini bar chart -->
          <div class="mt-auto flex items-end gap-1 pt-6">
            <div
              v-for="(edge, i) in edges.slice(0, 20)"
              :key="i"
              class="w-2 rounded-t transition-all duration-300"
              :class="edge.status?.phase === 'Ready' ? 'bg-success/60' : 'bg-danger/40'"
              :style="{ height: edge.status?.connected ? '24px' : '12px' }"
            />
          </div>
        </div>

        <!-- Stat: Total -->
        <div
          class="tilt-card stagger-item flex flex-col justify-between rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 60ms"
        >
          <div class="flex items-center justify-between">
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Total</span>
            <div class="flex h-7 w-7 items-center justify-center rounded-lg bg-accent-subtle">
              <Server class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
            </div>
          </div>
          <span class="mt-auto text-3xl font-bold tabular-nums text-text-primary">{{ stats.total }}</span>
        </div>

        <!-- Stat: Connected -->
        <div
          class="tilt-card stagger-item flex flex-col justify-between rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 120ms"
        >
          <div class="flex items-center justify-between">
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Connected</span>
            <div class="flex h-7 w-7 items-center justify-center rounded-lg bg-success-subtle">
              <Wifi class="h-3.5 w-3.5 text-success" :stroke-width="1.75" />
            </div>
          </div>
          <span class="mt-auto text-3xl font-bold tabular-nums text-success">{{ stats.connected }}</span>
        </div>

        <!-- Stat: Ready (with glow) -->
        <div
          class="tilt-card stagger-item flex flex-col justify-between rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 180ms"
        >
          <div class="flex items-center justify-between">
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Ready</span>
            <div class="flex h-7 w-7 items-center justify-center rounded-lg bg-success-subtle">
              <CheckCircle class="h-3.5 w-3.5 text-success" :stroke-width="1.75" />
            </div>
          </div>
          <span class="mt-auto text-3xl font-bold tabular-nums text-text-primary">{{ stats.ready }}</span>
        </div>

        <!-- Stat: Disconnected -->
        <div
          class="tilt-card stagger-item flex flex-col justify-between rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 240ms"
        >
          <div class="flex items-center justify-between">
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Disconnected</span>
            <div class="flex h-7 w-7 items-center justify-center rounded-lg bg-danger-subtle">
              <WifiOff class="h-3.5 w-3.5 text-danger" :stroke-width="1.75" />
            </div>
          </div>
          <span class="mt-auto text-3xl font-bold tabular-nums text-danger">{{ stats.disconnected }}</span>
        </div>

        <!-- Virtual Workloads -->
        <div
          class="tilt-card stagger-item flex flex-col justify-between rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 280ms"
        >
          <div class="flex items-center justify-between">
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Workloads</span>
            <div class="flex h-7 w-7 items-center justify-center rounded-lg bg-accent-subtle">
              <Layers class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
            </div>
          </div>
          <div class="mt-auto flex items-end gap-2">
            <span class="text-3xl font-bold tabular-nums text-text-primary">{{ workloadStats.total }}</span>
            <span class="mb-0.5 text-[12px] text-success">{{ workloadStats.running }} running</span>
          </div>
        </div>

        <!-- Workloads quick link -->
        <router-link
          to="/workloads"
          class="tilt-card stagger-item flex flex-col justify-between rounded-2xl border border-accent/20 bg-accent/[0.03] p-5 backdrop-blur transition-all hover:bg-accent/[0.06]"
          style="animation-delay: 320ms"
        >
          <div class="flex items-center justify-between">
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-accent">Virtual Workloads</span>
            <ChevronRight class="h-4 w-4 text-accent/50" :stroke-width="1.75" />
          </div>
          <span class="mt-auto text-[13px] font-medium text-accent/80">Deploy across edges</span>
        </router-link>

        <!-- Recent edges (wide, spans 3 cols) -->
        <div
          v-if="edges.length > 0"
          class="stagger-item col-span-3 rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 300ms"
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
          <div class="mt-4 space-y-1">
            <router-link
              v-for="(edge, i) in edges.slice(0, 5)"
              :key="edge.metadata.name"
              :to="`/edges/${edge.metadata.name}`"
              class="card-glow stagger-item group flex items-center justify-between rounded-xl border border-border-subtle bg-surface-overlay/50 px-4 py-2.5 transition-all duration-150 hover:bg-accent/[0.03]"
              :style="{ animationDelay: `${(i + 5) * 50}ms` }"
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
