<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import MCPCreateModal from '@/components/MCPCreateModal.vue'
import { useGraphQLQuery, graphqlMutate } from '@/composables/useGraphQL'
import { useAuthStore } from '@/stores/auth'
import { LIST_MCP_SERVERS, type ListMCPResult, type MCPItem } from '@/graphql/queries/mcp'
import { DELETE_MCP } from '@/graphql/mutations'
import { Bot, Plus, Server, Wifi, Copy, Check, Trash2, ClipboardCopy } from 'lucide-vue-next'

const router = useRouter()
const auth = useAuthStore()
const { data, loading, error, refetch } = useGraphQLQuery<ListMCPResult>(LIST_MCP_SERVERS, undefined, 10000)
const showCreate = ref(false)
const copiedField = ref<string | null>(null)

const mcpServers = computed(() => data.value?.mcp_kedge_faros_sh?.v1alpha1?.KubernetesList?.items ?? [])
const defaultMCP = computed(() => mcpServers.value.find((m) => m.metadata.name === 'default'))

const columns = [
  { key: 'name', label: 'Name' },
  { key: 'url', label: 'Endpoint URL' },
  { key: 'connectedEdges', label: 'Connected Edges' },
  { key: 'toolsets', label: 'Toolsets' },
  { key: 'readOnly', label: 'Read Only' },
  { key: 'status', label: 'Status' },
]

const rows = computed(() =>
  mcpServers.value.map((m: MCPItem) => {
    const readyCond = m.status?.conditions?.find((c) => c.type === 'Ready')
    return {
      name: m.metadata.name,
      url: m.status?.URL ?? '-',
      connectedEdges: m.status?.connectedEdges ?? 0,
      toolsets: m.spec?.toolsets?.length ? m.spec.toolsets.join(', ') : 'all',
      readOnly: m.spec?.readOnly ? 'Yes' : 'No',
      status: readyCond?.status === 'True' ? 'Ready' : 'Pending',
      _raw: m,
    }
  }),
)

const stats = computed(() => {
  const total = mcpServers.value.length
  const ready = mcpServers.value.filter((m) => {
    const cond = m.status?.conditions?.find((c) => c.type === 'Ready')
    return cond?.status === 'True'
  }).length
  const totalEdges = mcpServers.value.reduce((sum, m) => sum + (m.status?.connectedEdges ?? 0), 0)
  return { total, ready, totalEdges }
})

function handleRowClick(row: Record<string, unknown>) {
  router.push(`/mcp/${row.name}`)
}

async function handleDelete(name: string, event: Event) {
  event.stopPropagation()
  if (!confirm(`Delete MCP server "${name}"?`)) return
  try {
    await graphqlMutate(DELETE_MCP, { name })
    await refetch()
  } catch (e) {
    alert(e instanceof Error ? e.message : 'Delete failed')
  }
}

function handleCreated() {
  showCreate.value = false
  refetch()
}

// --- Config snippet generation ---
const maskedToken = '••••••••••••••••'

function buildClaudeCodeSnippet(token: string) {
  const url = defaultMCP.value?.status?.URL ?? '<MCP_URL>'
  return `claude mcp add --transport http kedge "${url}" \\
  -H "Authorization: Bearer ${token}"`
}

function buildClaudeDesktopSnippet(token: string) {
  const url = defaultMCP.value?.status?.URL ?? '<MCP_URL>'
  return JSON.stringify(
    {
      mcpServers: {
        kedge: {
          url,
          headers: { Authorization: `Bearer ${token}` },
        },
      },
    },
    null,
    2,
  )
}

const claudeCodeSnippet = computed(() => buildClaudeCodeSnippet(maskedToken))
const claudeDesktopSnippet = computed(() => buildClaudeDesktopSnippet(maskedToken))

async function copySnippet(builder: (token: string) => string, field: string) {
  try {
    const token = await auth.getValidToken()
    await navigator.clipboard.writeText(builder(token))
    copiedField.value = field
    setTimeout(() => (copiedField.value = null), 2000)
  } catch {
    // fallback
  }
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
  <AppLayout>
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
      <button
        class="glow-ring flex items-center gap-2 rounded-xl border border-accent/30 bg-accent/10 px-3.5 py-2 text-[12px] font-medium text-accent transition-all hover:bg-accent/20"
        @click="showCreate = true"
      >
        <Plus class="h-3.5 w-3.5" :stroke-width="2" />
        New MCP Server
      </button>
    </div>

    <!-- Global MCP config card -->
    <div
      v-if="defaultMCP"
      class="border-beam stagger-item mb-5 rounded-2xl border border-border-subtle bg-surface-raised/80 p-6 backdrop-blur"
      style="animation-delay: 60ms"
    >
      <div class="flex items-center gap-2 mb-4">
        <Bot class="h-4 w-4 text-accent" :stroke-width="1.75" />
        <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Global MCP Configuration</span>
        <StatusBadge
          :status="defaultMCP.status?.conditions?.find(c => c.type === 'Ready')?.status === 'True' ? 'Ready' : 'Pending'"
        />
      </div>

      <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <!-- Claude Code config -->
        <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4">
          <div class="flex items-center justify-between mb-2">
            <span class="text-[11px] font-semibold uppercase tracking-wider text-text-muted">Claude Code</span>
            <button
              class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
              @click="copySnippet(buildClaudeCodeSnippet, 'claude-code')"
            >
              <component :is="copiedField === 'claude-code' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
              {{ copiedField === 'claude-code' ? 'Copied' : 'Copy' }}
            </button>
          </div>
          <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ claudeCodeSnippet }}</pre>
        </div>

        <!-- Claude Desktop config -->
        <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4">
          <div class="flex items-center justify-between mb-2">
            <span class="text-[11px] font-semibold uppercase tracking-wider text-text-muted">Claude Desktop / claude_desktop_config.json</span>
            <button
              class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
              @click="copySnippet(buildClaudeDesktopSnippet, 'claude-desktop')"
            >
              <component :is="copiedField === 'claude-desktop' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
              {{ copiedField === 'claude-desktop' ? 'Copied' : 'Copy' }}
            </button>
          </div>
          <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ claudeDesktopSnippet }}</pre>
        </div>
      </div>

      <!-- Endpoint URL -->
      <div class="mt-4 flex items-center gap-2">
        <span class="text-[11px] text-text-muted">Endpoint:</span>
        <code class="rounded-md border border-border-subtle bg-surface-overlay px-2 py-0.5 font-mono text-[11px] text-accent">
          {{ defaultMCP.status?.URL ?? 'Pending...' }}
        </code>
        <button
          v-if="defaultMCP.status?.URL"
          class="flex items-center gap-1 rounded-md px-1.5 py-0.5 text-text-muted transition-all hover:text-accent"
          @click="copyToClipboard(defaultMCP.status!.URL!, 'url')"
        >
          <component :is="copiedField === 'url' ? Check : ClipboardCopy" class="h-3 w-3" :stroke-width="2" />
        </button>
        <span class="text-[11px] text-text-muted">
          &middot; {{ defaultMCP.status?.connectedEdges ?? 0 }} edges
          &middot; {{ defaultMCP.spec?.toolsets?.length ? defaultMCP.spec.toolsets.join(', ') : 'all toolsets' }}
          <template v-if="defaultMCP.spec?.readOnly"> &middot; read-only</template>
        </span>
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
        <template #name="{ value }">
          <div class="flex items-center gap-2">
            <span class="font-medium text-text-primary">{{ value }}</span>
            <span
              v-if="value === 'default'"
              class="rounded-full border border-accent/20 bg-accent-subtle px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wider text-accent"
            >
              global
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
        <template #readOnly="{ value, row }">
          <div class="flex items-center justify-between">
            <span class="text-[12px]" :class="value === 'Yes' ? 'text-warning' : 'text-text-muted'">{{ value }}</span>
            <button
              v-if="row.name !== 'default'"
              class="ml-4 opacity-0 group-hover:opacity-100 flex h-6 w-6 items-center justify-center rounded-lg text-text-muted transition-all hover:bg-danger-subtle hover:text-danger"
              title="Delete"
              @click="handleDelete(row.name as string, $event)"
            >
              <Trash2 class="h-3 w-3" :stroke-width="2" />
            </button>
          </div>
        </template>
      </ResourceTable>
    </div>

    <!-- Create modal -->
    <MCPCreateModal
      v-if="showCreate"
      @close="showCreate = false"
      @created="handleCreated"
    />
  </AppLayout>
</template>
