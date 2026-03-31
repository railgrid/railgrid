<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import EdgeCreateModal from '@/components/EdgeCreateModal.vue'
import { useGraphQLQuery } from '@/composables/useGraphQL'
import { LIST_EDGES, type ListEdgesResult, type EdgeItem } from '@/graphql/queries/edges'
import { Wifi, WifiOff, Server, CheckCircle, Plus } from 'lucide-vue-next'

const router = useRouter()
const { data, loading, error, refetch } = useGraphQLQuery<ListEdgesResult>(LIST_EDGES, undefined, 10000)
const showCreate = ref(false)

function handleCreated() {
  refetch()
}

const columns = [
  { key: 'name', label: 'Name' },
  { key: 'type', label: 'Type' },
  { key: 'phase', label: 'Phase' },
  { key: 'connected', label: 'Connected' },
  { key: 'hostname', label: 'Hostname' },
  { key: 'agentVersion', label: 'Agent Version' },
  { key: 'age', label: 'Age' },
]

function formatAge(timestamp: string): string {
  const diff = Date.now() - new Date(timestamp).getTime()
  const hours = Math.floor(diff / 3600000)
  if (hours < 1) return `${Math.floor(diff / 60000)}m`
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

const edges = computed(() => data.value?.kedge_faros_sh?.v1alpha1?.Edges?.items ?? [])

const rows = computed(() =>
  edges.value.map((e: EdgeItem) => ({
    name: e.metadata.name,
    type: e.spec?.type ?? '',
    phase: e.status?.phase ?? 'Unknown',
    connected: e.status?.connected ?? false,
    hostname: e.status?.hostname ?? '',
    agentVersion: e.status?.agentVersion ?? '',
    age: formatAge(e.metadata.creationTimestamp),
    _raw: e,
  })),
)

const stats = computed(() => {
  const total = edges.value.length
  const ready = edges.value.filter((e) => e.status?.phase === 'Ready').length
  const connected = edges.value.filter((e) => e.status?.connected).length
  return { total, ready, connected }
})

function handleRowClick(row: Record<string, unknown>) {
  router.push(`/edges/${row.name}`)
}
</script>

<template>
  <AppLayout>
    <!-- Mini stat pills -->
    <div class="stagger-item mb-5 flex items-center gap-3" style="animation-delay: 0ms">
      <div class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-2 backdrop-blur">
        <Server class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
        <span class="text-[20px] font-bold tabular-nums text-text-primary">{{ stats.total }}</span>
        <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">edges</span>
      </div>
      <div class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-2 backdrop-blur">
        <CheckCircle class="h-3.5 w-3.5 text-success" :stroke-width="1.75" />
        <span class="text-[20px] font-bold tabular-nums text-success">{{ stats.ready }}</span>
        <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">ready</span>
      </div>
      <div class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-2 backdrop-blur">
        <Wifi class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
        <span class="text-[20px] font-bold tabular-nums text-accent">{{ stats.connected }}</span>
        <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">online</span>
      </div>
      <div class="ml-auto flex items-center gap-1.5">
        <div class="live-dot h-1.5 w-1.5 rounded-full text-success" />
        <span class="font-mono text-[10px] text-text-muted">auto-refresh 10s</span>
      </div>
      <button
        class="glow-ring flex items-center gap-2 rounded-xl border border-accent/30 bg-accent/10 px-3.5 py-2 text-[12px] font-medium text-accent transition-all hover:bg-accent/20"
        @click="showCreate = true"
      >
        <Plus class="h-3.5 w-3.5" :stroke-width="2" />
        New Edge
      </button>
    </div>

    <!-- Table in border-beam wrapper -->
    <div class="border-beam stagger-item rounded-2xl" style="animation-delay: 80ms">
      <ResourceTable
        :columns="columns"
        :rows="rows"
        :loading="loading && !data"
        :error="error"
        @row-click="handleRowClick"
      >
        <template #name="{ value }">
          <span class="font-medium text-text-primary">{{ value }}</span>
        </template>
        <template #type="{ value }">
          <span class="rounded-md border border-border-subtle bg-surface-overlay px-2 py-0.5 font-mono text-[11px] text-text-secondary">{{ value }}</span>
        </template>
        <template #phase="{ value, row }">
          <StatusBadge :status="value as string" :connected="row.connected as boolean" />
        </template>
        <template #connected="{ value }">
          <div class="flex items-center gap-1.5">
            <component
              :is="value ? Wifi : WifiOff"
              class="h-3.5 w-3.5"
              :class="value ? 'text-success' : 'text-danger'"
              :stroke-width="1.75"
            />
            <span :class="value ? 'text-success' : 'text-danger'" class="text-[12px] font-medium">
              {{ value ? 'Yes' : 'No' }}
            </span>
          </div>
        </template>
        <template #age="{ value }">
          <span class="font-mono text-[12px] text-text-muted">{{ value }}</span>
        </template>
      </ResourceTable>
    </div>

    <!-- Create modal -->
    <EdgeCreateModal
      v-if="showCreate"
      @close="showCreate = false"
      @created="handleCreated"
    />
  </AppLayout>
</template>
