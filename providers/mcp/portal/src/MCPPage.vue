<script setup lang="ts">
// MCPServer-only list page. The dedicated KubernetesMCP / LinuxMCP
// CRDs were removed; their tools live behind the MCPServer aggregate,
// contributed in code via the providers/mcp/aggregate.RegisterToolFamily
// registry. The portal therefore tracks just one kind of MCP resource.

import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import ResourceTable from '@/components/ResourceTable.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import MCPCreateModal from './MCPCreateModal.vue'
import MCPHelpModal from './MCPHelpModal.vue'
import MCPSetupPanel from './MCPSetupPanel.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { useGraphQLQuery, graphqlMutate, graphqlQuery } from '@/composables/useGraphQL'
import { LIST_MCP_SERVERS, type ListMCPResult, type MCPItem } from '@/graphql/queries/mcp'
import { GET_SECRET, type GetSecretResult } from '@/graphql/queries/secrets'
import { DELETE_AGGREGATE_MCP } from '@/graphql/mutations'
import { Bot, Plus, Server, Wifi, Check, Trash2, ClipboardCopy, Layers, ChevronDown, ChevronUp, HelpCircle } from 'lucide-vue-next'

const router = useRouter()
const { data, loading, error, refetch } = useGraphQLQuery<ListMCPResult>(LIST_MCP_SERVERS, undefined, 10000)
const showCreate = ref(false)
const showHelp = ref(false)
const copiedField = ref<string | null>(null)
// Whether the default-MCP detail card is expanded. Single card now —
// no per-kind ones since only MCPServer exists.
const expanded = ref(false)

const mcps = computed(() => data.value?.kedge_faros_sh?.v1alpha1?.MCPServers?.items ?? [])
const defaultMCP = computed(() => mcps.value.find((m) => m.metadata.name === 'default'))

// Resolve the default MCPServer's long-lived (legacy) SA token from
// status.tokenSecretRef so the setup panel renders a working bearer
// credential (not the portal user's short-lived OIDC token).
const defaultToken = ref<string | undefined>(undefined)
watch(
  () => defaultMCP.value?.status?.tokenSecretRef,
  async (secretRef) => {
    if (!secretRef?.name || !secretRef?.namespace) {
      defaultToken.value = undefined
      return
    }
    try {
      const res = await graphqlQuery<GetSecretResult>(GET_SECRET, {
        name: secretRef.name,
        namespace: secretRef.namespace,
      })
      const encoded = res.v1?.Secret?.data?.token
      defaultToken.value = encoded ? atob(encoded) : undefined
    } catch {
      defaultToken.value = undefined
    }
  },
  { immediate: true },
)

const columns = [
  { key: 'name', label: 'Name' },
  { key: 'url', label: 'Endpoint URL' },
  { key: 'connectedEdges', label: 'Connected Edges' },
  { key: 'toolsets', label: 'Toolsets' },
  { key: 'readOnly', label: 'Read Only' },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: '' },
]

interface MCPRow extends Record<string, unknown> {
  name: string
  displayName: string
  url: string
  connectedEdges: number
  toolsets: string
  readOnly: string
  status: string
  _raw: MCPItem
}

function toRow(m: MCPItem): MCPRow {
  const readyCond = m.status?.conditions?.find((c) => c.type === 'Ready')
  const connected = (m.status?.kubernetesEdges ?? 0) + (m.status?.linuxEdges ?? 0)
  const parts: string[] = []
  if (m.spec?.kubernetesToolsets?.length) parts.push('k8s:' + m.spec.kubernetesToolsets.join('+'))
  if (m.spec?.linuxToolsets?.length) parts.push('linux:' + m.spec.linuxToolsets.join('+'))
  const toolsets = parts.length ? parts.join(' / ') : 'kube+linux defaults'
  return {
    name: m.metadata.name,
    displayName: m.spec?.displayName ?? '',
    url: m.status?.URL ?? '-',
    connectedEdges: connected,
    toolsets,
    readOnly: m.spec?.readOnly ? 'Yes' : 'No',
    status: readyCond?.status === 'True' ? 'Ready' : 'Pending',
    _raw: m,
  }
}

const rows = computed<MCPRow[]>(() => mcps.value.map(toRow))

const stats = computed(() => {
  const total = mcps.value.length
  const ready = mcps.value.filter((m) => {
    const cond = m.status?.conditions?.find((c) => c.type === 'Ready')
    return cond?.status === 'True'
  }).length
  // Sum live edges across MCPServers. Multiple MCPServers may target
  // overlapping edges via different edgeSelectors; we surface the raw
  // sum and label it accordingly.
  const totalEdges = mcps.value.reduce(
    (sum, m) => sum + (m.status?.kubernetesEdges ?? 0) + (m.status?.linuxEdges ?? 0),
    0,
  )
  return { total, ready, totalEdges }
})

function handleRowClick(row: Record<string, unknown>) {
  router.push(`/${row.name}`)
}

interface DeleteTarget {
  name: string
}
const deleteTarget = ref<DeleteTarget | null>(null)
const deleteBusy = ref(false)
const deleteError = ref<string | null>(null)

function requestDelete(name: string, event: Event) {
  event.stopPropagation()
  deleteError.value = null
  deleteTarget.value = { name }
}

async function confirmDelete() {
  if (!deleteTarget.value) return
  deleteBusy.value = true
  deleteError.value = null
  try {
    await graphqlMutate(DELETE_AGGREGATE_MCP, { name: deleteTarget.value.name })
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

function handleCreated() {
  showCreate.value = false
  refetch()
}

function urlForDefault(): string {
  return defaultMCP.value?.status?.URL ?? '<MCP_URL>'
}

// serverNameFor matches the kedge CLI (`kedge mcp url`) — MCPServer-
// shaped names just take a `kedge-<n>` prefix.
function serverNameFor(name = 'default'): string {
  return `kedge-${name}`
}

async function copyToClipboard(text: string, field: string) {
  try {
    await navigator.clipboard.writeText(text)
    copiedField.value = field
    setTimeout(() => (copiedField.value = null), 2000)
  } catch {
    // fallback
  }
}
</script>

<template>
  <div>
    <!-- Header row -->
    <div class="stagger-item mb-5 flex items-center justify-between" style="animation-delay: 0ms">
      <div class="flex items-center gap-3">
        <div class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-2 backdrop-blur">
          <Bot class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
          <span class="text-[20px] font-bold tabular-nums text-text-primary">{{ stats.total }}</span>
          <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">MCP servers</span>
        </div>
        <div class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-2 backdrop-blur">
          <Wifi class="h-3.5 w-3.5 text-success" :stroke-width="1.75" />
          <span class="text-[20px] font-bold tabular-nums text-success">{{ stats.totalEdges }}</span>
          <span class="text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">edges connected</span>
        </div>
        <div class="ml-auto flex items-center gap-1.5">
          <div class="live-dot h-1.5 w-1.5 rounded-full text-success" />
          <span class="font-mono text-[10px] text-text-muted">auto-refresh 10s</span>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <button
          class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-2 text-[12px] font-medium text-text-secondary transition-all hover:border-accent/30 hover:text-accent"
          title="How does the MCPServer aggregate work?"
          @click="showHelp = true"
        >
          <HelpCircle class="h-3.5 w-3.5" :stroke-width="1.75" />
          How does this work?
        </button>
        <button
          class="glow-ring flex items-center gap-2 rounded-xl border border-accent/30 bg-accent/10 px-3.5 py-2 text-[12px] font-medium text-accent transition-all hover:bg-accent/20"
          @click="showCreate = true"
        >
          <Plus class="h-3.5 w-3.5" :stroke-width="2" />
          New MCP Server
        </button>
      </div>
    </div>

    <!-- Default MCP card with collapsible client setup snippets. -->
    <div
      v-if="defaultMCP"
      class="border-beam stagger-item mb-5 rounded-2xl border border-accent/30 bg-surface-raised/80 p-4 backdrop-blur"
      style="animation-delay: 30ms"
    >
      <button
        class="flex w-full items-center gap-3 text-left"
        :aria-expanded="expanded"
        @click="expanded = !expanded"
      >
        <Layers class="h-4 w-4 text-accent shrink-0" :stroke-width="1.75" />
        <span class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted shrink-0">
          {{ defaultMCP.spec?.displayName || 'MCPServer — Default' }}
        </span>
        <StatusBadge
          :status="defaultMCP.status?.conditions?.find(c => c.type === 'Ready')?.status === 'True' ? 'Ready' : 'Pending'"
        />
        <span class="text-[11px] text-text-muted ml-auto">
          {{ defaultMCP.status?.kubernetesEdges ?? 0 }} kube · {{ defaultMCP.status?.linuxEdges ?? 0 }} linux
          <template v-if="defaultMCP.spec?.readOnly"> · read-only</template>
        </span>
        <component
          :is="expanded ? ChevronUp : ChevronDown"
          class="h-4 w-4 text-text-muted shrink-0"
          :stroke-width="1.75"
        />
      </button>

      <div class="mt-3 flex items-center gap-2 flex-wrap">
        <span class="text-[10px] uppercase tracking-wider text-text-muted">Endpoint</span>
        <code class="flex-1 min-w-0 truncate rounded-md border border-border-subtle bg-surface-overlay px-2 py-1 font-mono text-[11px] text-accent">
          {{ defaultMCP.status?.URL ?? 'Pending...' }}
        </code>
        <button
          v-if="defaultMCP.status?.URL"
          class="flex items-center gap-1 rounded-md border border-border-subtle px-2 py-1 text-[10px] text-text-muted transition-all hover:border-accent/30 hover:text-accent shrink-0"
          @click="copyToClipboard(defaultMCP.status!.URL!, 'url')"
        >
          <component :is="copiedField === 'url' ? Check : ClipboardCopy" class="h-3 w-3" :stroke-width="2" />
          {{ copiedField === 'url' ? 'Copied' : 'Copy URL' }}
        </button>
      </div>

      <div v-if="expanded" class="mt-4 border-t border-border-subtle pt-4">
        <p class="mb-4 text-[12px] text-text-secondary">
          One endpoint covering every kube cluster and Linux edge plus a
          <code class="rounded-md border border-border-subtle bg-surface-overlay px-1 py-0.5 font-mono text-[11px]">list_targets</code>
          tool the AI can call to discover what's available.
        </p>

        <MCPSetupPanel
          embedded
          :server-name="serverNameFor()"
          :url="urlForDefault()"
          :token="defaultToken"
        />
      </div>
    </div>

    <!-- Table -->
    <div class="border-beam stagger-item rounded-2xl" style="animation-delay: 120ms">
      <ResourceTable
        :columns="columns"
        :rows="rows"
        :loading="loading && !data"
        :error="error"
        @row-click="handleRowClick"
      >
        <template #name="{ value, row }">
          <div class="flex flex-col gap-0.5">
            <div class="flex items-center gap-2">
              <span class="font-medium text-text-primary">{{ value }}</span>
              <span
                v-if="value === 'default'"
                class="rounded-full border border-accent/20 bg-accent-subtle px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wider text-accent"
              >
                global
              </span>
            </div>
            <span
              v-if="row.displayName"
              class="truncate text-[10px] text-text-muted/70"
              :title="row.displayName as string"
            >
              {{ row.displayName }}
            </span>
          </div>
        </template>
        <template #url="{ value }">
          <span class="font-mono text-[11px] text-text-muted max-w-[300px] truncate block">{{ value }}</span>
        </template>
        <template #connectedEdges="{ value }">
          <div class="flex items-center gap-1.5">
            <Server class="h-3 w-3 text-text-muted" :stroke-width="1.75" />
            <span class="text-[13px] font-medium" :class="(value as number) > 0 ? 'text-success' : 'text-text-muted'">{{ value }}</span>
          </div>
        </template>
        <template #toolsets="{ value }">
          <span class="rounded-md border border-border-subtle bg-surface-overlay px-2 py-0.5 font-mono text-[11px] text-text-secondary">{{ value }}</span>
        </template>
        <template #status="{ value }">
          <StatusBadge :status="value as string" />
        </template>
        <template #readOnly="{ value }">
          <span class="text-[12px]" :class="value === 'Yes' ? 'text-warning' : 'text-text-muted'">{{ value }}</span>
        </template>
        <template #actions="{ row }">
          <button
            v-if="row.name !== 'default'"
            class="flex h-7 w-7 items-center justify-center rounded-lg text-text-muted/40 opacity-0 transition-all group-hover:opacity-100 hover:bg-danger-subtle hover:text-danger"
            title="Delete MCP server"
            @click.stop="requestDelete(row.name as string, $event)"
          >
            <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" />
          </button>
        </template>
      </ResourceTable>
    </div>

    <!-- Help modal -->
    <MCPHelpModal v-if="showHelp" @close="showHelp = false" />

    <!-- Create modal -->
    <MCPCreateModal
      v-if="showCreate"
      @close="showCreate = false"
      @created="handleCreated"
    />

    <!-- Delete confirmation -->
    <ConfirmDialog
      v-if="deleteTarget"
      title="Delete MCP server?"
      :message="`This will permanently delete MCP server '${deleteTarget.name}'. This cannot be undone.`"
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
  </div>
</template>
