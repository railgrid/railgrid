<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import EdgeCreateModal from '@/components/EdgeCreateModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { useGraphQLQuery, graphqlMutate } from '@/composables/useGraphQL'
import { useHubVersion, isAgentOutdated } from '@/composables/useHubVersion'
import { LIST_EDGES, type ListEdgesResult, type EdgeItem } from '@/graphql/queries/edges'
import { DELETE_EDGE } from '@/graphql/mutations'
import { formatAge } from '@/utils/time'
import { Wifi, WifiOff, Server, CheckCircle, Plus, Trash2, ArrowUpCircle } from 'lucide-vue-next'

const router = useRouter()
const { data, loading, error, refetch } = useGraphQLQuery<ListEdgesResult>(LIST_EDGES, undefined, 10000)
const { hubVersion } = useHubVersion()
const showCreate = ref(false)
const deleteTarget = ref<string | null>(null)
const deleteBusy = ref(false)
const deleteError = ref<string | null>(null)

function handleCreated() {
  refetch()
}

function requestDelete(name: string, event: Event) {
  event.stopPropagation()
  deleteError.value = null
  deleteTarget.value = name
}

async function confirmDelete() {
  if (!deleteTarget.value) return
  deleteBusy.value = true
  deleteError.value = null
  try {
    await graphqlMutate(DELETE_EDGE, { name: deleteTarget.value })
    deleteTarget.value = null
    await refetch()
  } catch (e) {
    deleteError.value = e instanceof Error ? e.message : 'Delete failed'
  } finally {
    deleteBusy.value = false
  }
}

function cancelDelete() {
  if (deleteBusy.value) return
  deleteTarget.value = null
  deleteError.value = null
}

const columns = [
  { key: 'name', label: 'Name' },
  { key: 'type', label: 'Type' },
  { key: 'phase', label: 'Phase' },
  { key: 'connected', label: 'Connected' },
  { key: 'agentVersion', label: 'Agent Version' },
  { key: 'lastHeartbeat', label: 'Last Heartbeat' },
  { key: 'age', label: 'Age' },
  { key: 'actions', label: '' },
]

const edges = computed(() => data.value?.kedge_faros_sh?.v1alpha1?.Edges?.items ?? [])

const rows = computed(() =>
  edges.value.map((e: EdgeItem) => ({
    name: e.metadata.name,
    type: e.spec?.type ?? '',
    phase: e.status?.phase ?? 'Unknown',
    connected: e.status?.connected ?? false,
    agentVersion: e.status?.agentVersion ?? '',
    lastHeartbeat: e.status?.lastHeartbeatTime ? formatAge(e.status.lastHeartbeatTime) : '-',
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
        <template #agentVersion="{ value }">
          <div class="flex items-center gap-1.5">
            <span class="font-mono text-[11px] text-text-secondary">{{ value || '-' }}</span>
            <span
              v-if="isAgentOutdated(value as string, hubVersion?.version)"
              class="flex items-center gap-1 rounded-md border border-warning/30 bg-warning/10 px-1.5 py-0.5 text-[10px] font-medium text-warning"
              :title="`Hub is on ${hubVersion?.version}. Click the edge to see upgrade instructions.`"
            >
              <ArrowUpCircle class="h-3 w-3" :stroke-width="2" />
              Upgrade
            </span>
          </div>
        </template>
        <template #age="{ value }">
          <span class="font-mono text-[12px] text-text-muted">{{ value }}</span>
        </template>
        <template #actions="{ row }">
          <button
            class="flex h-7 w-7 items-center justify-center rounded-lg text-text-muted/40 opacity-0 transition-all group-hover:opacity-100 hover:bg-danger-subtle hover:text-danger"
            title="Delete edge"
            @click.stop="requestDelete(row.name as string, $event)"
          >
            <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" />
          </button>
        </template>
      </ResourceTable>
    </div>

    <!-- Create modal -->
    <EdgeCreateModal
      v-if="showCreate"
      @close="showCreate = false"
      @created="handleCreated"
    />

    <!-- Delete confirmation -->
    <ConfirmDialog
      v-if="deleteTarget"
      title="Delete edge?"
      :message="`This will permanently delete edge ${deleteTarget} and revoke its agent credentials. This cannot be undone.`"
      confirm-label="Delete"
      :busy="deleteBusy"
      @cancel="cancelDelete"
      @confirm="confirmDelete"
    />
    <div
      v-if="deleteError"
      class="fixed bottom-4 right-4 z-[110] rounded-lg border border-danger/20 bg-danger-subtle px-4 py-3 text-[12px] text-danger shadow-lg"
    >
      {{ deleteError }}
    </div>
  </AppLayout>
</template>
