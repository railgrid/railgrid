<script setup lang="ts">
// Tile content for the server-edges dashboard summary. Same shape as
// kubernetesedges' DashboardTile but filters edges to type="server"
// and uses "servers" labeling.

import { computed, watch, inject } from 'vue'
import { useAuthStore, type KedgeContext } from './auth-adapter'
import { useGraphQLQuery } from '@/composables/useGraphQL'
import { LIST_EDGES, type ListEdgesResult } from '@/graphql/queries/edges'
import { Server, Wifi, WifiOff, ChevronRight } from 'lucide-vue-next'

const props = defineProps<{ context: KedgeContext | null }>()
const auth = useAuthStore()

watch(() => props.context, (ctx) => auth.hydrate(ctx), { immediate: true })

const { data, loading, error } = useGraphQLQuery<ListEdgesResult>(LIST_EDGES, undefined, 30000)

const items = computed(() =>
  (data.value?.kedge_faros_sh?.v1alpha1?.Edges?.items ?? []).filter((e) => e.spec?.type === 'server'),
)

const stats = computed(() => {
  const total = items.value.length
  const ready = items.value.filter((e) => e.status?.phase === 'Ready').length
  const connected = items.value.filter((e) => e.status?.connected).length
  return { total, ready, connected }
})

const recent = computed(() =>
  [...items.value]
    .sort((a, b) => (b.metadata.creationTimestamp ?? '').localeCompare(a.metadata.creationTimestamp ?? ''))
    .slice(0, 3),
)

const dispatchNavigate = inject<(path: string) => void>('dispatchNavigate', () => {})
</script>

<template>
  <div v-if="error" class="text-[11px] text-danger">{{ error }}</div>
  <div v-else-if="loading && !data" class="text-[11px] text-text-muted">Loading servers&hellip;</div>
  <div v-else-if="items.length === 0" class="text-[11px] text-text-muted">
    No servers connected yet.
  </div>
  <div v-else class="space-y-3">
    <div class="flex items-baseline gap-3">
      <div class="flex items-baseline gap-1">
        <span class="text-2xl font-bold tabular-nums text-text-primary">{{ stats.total }}</span>
        <span class="text-[11px] text-text-muted">servers</span>
      </div>
      <span class="text-[11px] text-text-muted">
        <span :class="stats.ready === stats.total ? 'text-success' : 'text-warning'">{{ stats.ready }} ready</span>
        <span class="mx-1 text-text-muted/50">·</span>
        <span :class="stats.connected === stats.total ? 'text-success' : 'text-warning'">{{ stats.connected }} connected</span>
      </span>
    </div>

    <ul class="space-y-1">
      <li v-for="edge in recent" :key="edge.metadata.name">
        <button
          type="button"
          class="group flex w-full items-center gap-2 rounded-lg border border-border-subtle bg-surface-overlay/40 px-3 py-2 text-left transition-colors hover:bg-accent/[0.04]"
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
</template>
