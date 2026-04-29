<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import WorkloadCreateModal from '@/components/WorkloadCreateModal.vue'
import { useGraphQLQuery, graphqlMutate } from '@/composables/useGraphQL'
import { DELETE_VIRTUAL_WORKLOAD } from '@/graphql/mutations'
import {
  LIST_VIRTUAL_WORKLOADS,
  type ListVirtualWorkloadsResult,
  type VirtualWorkloadItem,
} from '@/graphql/queries/workloads'
import {
  CheckCircle,
  AlertTriangle,
  Plus,
  Layers,
  Trash2,
  MapPin,
} from 'lucide-vue-next'

const router = useRouter()
const showCreate = ref(false)
const deleteConfirm = ref<{ name: string; namespace: string } | null>(null)
const deleting = ref(false)
const deleteError = ref<string | null>(null)

const {
  data,
  loading,
  error,
  refetch,
} = useGraphQLQuery<ListVirtualWorkloadsResult>(LIST_VIRTUAL_WORKLOADS, undefined, 10000)

const workloads = computed(
  () => data.value?.kedge_faros_sh?.v1alpha1?.VirtualWorkloads?.items ?? [],
)

const columns = [
  { key: 'name', label: 'Name' },
  { key: 'namespace', label: 'Namespace' },
  { key: 'phase', label: 'Status' },
  { key: 'replicas', label: 'Replicas' },
  { key: 'image', label: 'Image' },
  { key: 'strategy', label: 'Strategy' },
  { key: 'edges', label: 'Edges' },
  { key: 'age', label: 'Age' },
  { key: 'actions', label: '' },
]

const rows = computed(() =>
  workloads.value.map((w: VirtualWorkloadItem) => ({
    name: w.metadata.name,
    namespace: w.metadata.namespace,
    phase: w.status?.phase ?? 'Unknown',
    replicas: `${w.status?.readyReplicas ?? 0}/${w.spec?.replicas ?? 0}`,
    image: w.spec?.simple?.image ?? '(template)',
    strategy: w.spec?.placement?.strategy ?? '-',
    edges: w.status?.edges?.length ?? 0,
    age: formatAge(w.metadata.creationTimestamp),
    _raw: w,
  })),
)

// --- Stats ---
const stats = computed(() => {
  const total = workloads.value.length
  const running = workloads.value.filter((w) => w.status?.phase === 'Running').length
  const pending = workloads.value.filter((w) => w.status?.phase === 'Pending').length
  const failed = workloads.value.filter((w) => w.status?.phase === 'Failed').length
  const totalEdges = workloads.value.reduce((sum, w) => sum + (w.status?.edges?.length ?? 0), 0)
  return { total, running, pending, failed, totalEdges }
})

// --- Helpers ---
function formatAge(timestamp: string): string {
  const diff = Date.now() - new Date(timestamp).getTime()
  const hours = Math.floor(diff / 3600000)
  if (hours < 1) return `${Math.floor(diff / 60000)}m`
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

function handleRowClick(row: Record<string, unknown>) {
  router.push(`/workloads/${row.namespace}/${row.name}`)
}

function handleCreated() {
  refetch()
}

function confirmDelete(name: string, namespace: string) {
  deleteConfirm.value = { name, namespace }
  deleteError.value = null
}

async function executeDelete() {
  if (!deleteConfirm.value) return
  deleting.value = true
  deleteError.value = null
  try {
    await graphqlMutate(DELETE_VIRTUAL_WORKLOAD, {
      name: deleteConfirm.value.name,
      namespace: deleteConfirm.value.namespace,
    })
    deleteConfirm.value = null
    refetch()
  } catch (e) {
    deleteError.value = e instanceof Error ? e.message : 'Delete failed'
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <AppLayout>
    <!-- Stats row -->
    <div class="stagger-item mb-5 flex items-center gap-3 flex-wrap" style="animation-delay: 0ms">
      <div class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-2 backdrop-blur">
        <Layers class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
        <span class="text-[20px] font-bold tabular-nums text-text-primary">{{ stats.total }}</span>
        <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">workloads</span>
      </div>
      <div class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-2 backdrop-blur">
        <CheckCircle class="h-3.5 w-3.5 text-success" :stroke-width="1.75" />
        <span class="text-[20px] font-bold tabular-nums text-success">{{ stats.running }}</span>
        <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">running</span>
      </div>
      <div v-if="stats.pending > 0" class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-2 backdrop-blur">
        <AlertTriangle class="h-3.5 w-3.5 text-warning" :stroke-width="1.75" />
        <span class="text-[20px] font-bold tabular-nums text-warning">{{ stats.pending }}</span>
        <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">pending</span>
      </div>
      <div v-if="stats.failed > 0" class="flex items-center gap-2 rounded-xl border border-danger/20 bg-danger-subtle px-3 py-2 backdrop-blur">
        <AlertTriangle class="h-3.5 w-3.5 text-danger" :stroke-width="1.75" />
        <span class="text-[20px] font-bold tabular-nums text-danger">{{ stats.failed }}</span>
        <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">failed</span>
      </div>
      <div class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-2 backdrop-blur">
        <MapPin class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
        <span class="text-[20px] font-bold tabular-nums text-accent">{{ stats.totalEdges }}</span>
        <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">edge placements</span>
      </div>
      <div class="ml-auto flex items-center gap-3">
        <div class="flex items-center gap-1.5">
          <div class="live-dot h-1.5 w-1.5 rounded-full text-success" />
          <span class="font-mono text-[10px] text-text-muted">auto-refresh 10s</span>
        </div>
        <button
          class="glow-ring flex items-center gap-2 rounded-xl border border-accent/30 bg-accent/10 px-3.5 py-2 text-[12px] font-medium text-accent transition-all hover:bg-accent/20"
          @click="showCreate = true"
        >
          <Plus class="h-3.5 w-3.5" :stroke-width="2" />
          Create Workload
        </button>
      </div>
    </div>

    <!-- Table -->
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
        <template #namespace="{ value }">
          <span class="rounded-md border border-border-subtle bg-surface-overlay px-2 py-0.5 font-mono text-[11px] text-text-secondary">{{ value }}</span>
        </template>
        <template #phase="{ value }">
          <StatusBadge :status="value as string" />
        </template>
        <template #replicas="{ value }">
          <span class="font-mono text-[12px] text-text-secondary">{{ value }}</span>
        </template>
        <template #image="{ value }">
          <span class="max-w-[200px] truncate font-mono text-[11px] text-text-muted" :title="value as string">{{ value }}</span>
        </template>
        <template #strategy="{ value }">
          <span class="rounded-md border border-border-subtle bg-surface-overlay px-2 py-0.5 text-[11px] text-text-secondary">{{ value }}</span>
        </template>
        <template #edges="{ value }">
          <span class="font-mono text-[12px] text-text-muted">{{ value }}</span>
        </template>
        <template #age="{ value }">
          <span class="font-mono text-[12px] text-text-muted">{{ value }}</span>
        </template>
        <template #actions="{ row }">
          <button
            class="flex h-7 w-7 items-center justify-center rounded-lg text-text-muted/40 opacity-0 transition-all group-hover:opacity-100 hover:bg-danger-subtle hover:text-danger"
            title="Delete workload"
            @click.stop="confirmDelete(row.name as string, row.namespace as string)"
          >
            <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" />
          </button>
        </template>
      </ResourceTable>
    </div>

    <!-- Create modal -->
    <WorkloadCreateModal
      v-if="showCreate"
      @close="showCreate = false"
      @created="handleCreated"
    />

    <!-- Delete confirmation modal -->
    <Teleport to="body">
      <div
        v-if="deleteConfirm"
        class="fixed inset-0 z-[100] flex items-center justify-center bg-black/50 backdrop-blur-sm"
        @click.self="deleteConfirm = null"
      >
        <div class="w-full max-w-md rounded-2xl border border-border-subtle bg-surface-raised p-6 shadow-2xl">
          <h3 class="text-[14px] font-bold text-text-primary">Delete virtual workload?</h3>
          <p class="mt-2 text-[12px] text-text-muted">
            This will permanently delete
            <span class="font-mono font-medium text-text-secondary">{{ deleteConfirm.name }}</span>
            from namespace
            <span class="font-mono font-medium text-text-secondary">{{ deleteConfirm.namespace }}</span>
            and remove it from all edges.
          </p>
          <div v-if="deleteError" class="mt-3 rounded-lg border border-danger/20 bg-danger-subtle p-3 text-[12px] text-danger">
            {{ deleteError }}
          </div>
          <div class="mt-5 flex items-center justify-end gap-3">
            <button
              class="rounded-lg border border-border-subtle px-4 py-2 text-[12px] font-medium text-text-secondary transition-all hover:bg-surface-hover"
              @click="deleteConfirm = null"
              :disabled="deleting"
            >
              Cancel
            </button>
            <button
              class="rounded-lg bg-danger px-4 py-2 text-[12px] font-medium text-white transition-all hover:bg-danger/80 disabled:opacity-50"
              @click="executeDelete"
              :disabled="deleting"
            >
              {{ deleting ? 'Deleting...' : 'Delete' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </AppLayout>
</template>
