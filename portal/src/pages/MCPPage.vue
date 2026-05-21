<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import MCPCreateModal from '@/components/MCPCreateModal.vue'
import MCPHelpModal from '@/components/MCPHelpModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { useGraphQLQuery, graphqlMutate } from '@/composables/useGraphQL'
import { useAuthStore } from '@/stores/auth'
import { LIST_MCP_SERVERS, type ListMCPResult, type MCPItem, type MCPKind } from '@/graphql/queries/mcp'
import { DELETE_MCP, DELETE_LINUX_MCP, DELETE_AGGREGATE_MCP } from '@/graphql/mutations'
import { Bot, Plus, Server, Wifi, Copy, Check, Trash2, ClipboardCopy, Terminal, Layers, ChevronDown, ChevronUp, HelpCircle } from 'lucide-vue-next'

const router = useRouter()
const auth = useAuthStore()
const { data, loading, error, refetch } = useGraphQLQuery<ListMCPResult>(LIST_MCP_SERVERS, undefined, 10000)
const showCreate = ref(false)
const showHelp = ref(false)
const copiedField = ref<string | null>(null)
// Per-card expanded state for the three "Default" snippet cards.  Default to
// collapsed so the page stays compact; the user expands a card to reveal the
// Claude Code / Claude Desktop config snippets when they're ready to wire one
// up.  Keyed by kind so the aggregate / kube / linux cards toggle independently.
const expanded = ref<Record<string, boolean>>({})
function toggle(key: string) {
  expanded.value[key] = !expanded.value[key]
}

// MCP servers come from three CRDs:
//   - KubernetesMCPs: route to kubernetes-type edges,    /services/mcp/...
//   - LinuxMCPs:      route to server-type edges over SSH, /services/linux-mcp/...
//   - MCPServers:     aggregate kube + linux + list_targets, /services/mcpserver/...
// The portal table merges all three into a single list with a "Kind" column.
const kubeMCPs = computed(() => data.value?.kedge_faros_sh?.v1alpha1?.KubernetesMCPs?.items ?? [])
const linuxMCPs = computed(() => data.value?.kedge_faros_sh?.v1alpha1?.LinuxMCPs?.items ?? [])
const aggregateMCPs = computed(() => data.value?.kedge_faros_sh?.v1alpha1?.MCPServers?.items ?? [])
const defaultMCP = computed(() => kubeMCPs.value.find((m) => m.metadata.name === 'default'))
const defaultLinuxMCP = computed(() => linuxMCPs.value.find((m) => m.metadata.name === 'default'))
const defaultAggregateMCP = computed(() => aggregateMCPs.value.find((m) => m.metadata.name === 'default'))

const columns = [
  { key: 'name', label: 'Name' },
  { key: 'kind', label: 'Kind' },
  { key: 'url', label: 'Endpoint URL' },
  { key: 'connectedEdges', label: 'Connected Edges' },
  { key: 'toolsets', label: 'Toolsets' },
  { key: 'readOnly', label: 'Read Only' },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: '' },
]

// MCPRow is a Record so it satisfies ResourceTable's row constraint
// (the table indexes columns by string key); the field set just happens to
// be a fixed mix of strings, numbers, and an _raw escape hatch.
interface MCPRow extends Record<string, unknown> {
  name: string
  // displayName is the optional user-set human-readable title; the Name
  // template shows it below the kube-name as a subtitle when present.
  displayName: string
  kind: MCPKind
  url: string
  connectedEdges: number
  toolsets: string
  readOnly: string
  status: string
  _raw: MCPItem
}

function toRow(m: MCPItem, kind: MCPKind): MCPRow {
  const readyCond = m.status?.conditions?.find((c) => c.type === 'Ready')
  // Edge count semantics differ by kind: aggregate splits across two
  // counters; per-kind CRDs report a single total.
  const connected =
    kind === 'aggregate'
      ? (m.status?.kubernetesEdges ?? 0) + (m.status?.linuxEdges ?? 0)
      : (m.status?.connectedEdges ?? 0)
  // Same idea for toolset summary: aggregate has two arrays.
  let toolsets = 'all'
  if (kind === 'aggregate') {
    const parts: string[] = []
    if (m.spec?.kubernetesToolsets?.length) parts.push('k8s:' + m.spec.kubernetesToolsets.join('+'))
    if (m.spec?.linuxToolsets?.length) parts.push('linux:' + m.spec.linuxToolsets.join('+'))
    toolsets = parts.length ? parts.join(' / ') : 'kube+linux defaults'
  } else if (m.spec?.toolsets?.length) {
    toolsets = m.spec.toolsets.join(', ')
  }
  return {
    name: m.metadata.name,
    displayName: m.spec?.displayName ?? '',
    kind,
    url: m.status?.URL ?? '-',
    connectedEdges: connected,
    toolsets,
    readOnly: m.spec?.readOnly ? 'Yes' : 'No',
    status: readyCond?.status === 'True' ? 'Ready' : 'Pending',
    _raw: m,
  }
}

const rows = computed<MCPRow[]>(() => [
  ...aggregateMCPs.value.map((m) => toRow(m, 'aggregate')),
  ...kubeMCPs.value.map((m) => toRow(m, 'kubernetes')),
  ...linuxMCPs.value.map((m) => toRow(m, 'linux')),
])

const stats = computed(() => {
  const all = [...kubeMCPs.value, ...linuxMCPs.value, ...aggregateMCPs.value]
  const total = all.length
  const ready = all.filter((m) => {
    const cond = m.status?.conditions?.find((c) => c.type === 'Ready')
    return cond?.status === 'True'
  }).length
  // For the header summary we want a sense of "how many edges are
  // reachable", so we sum across kube/linux totals.  The aggregate CRD
  // shares the same edges and would double-count, so we skip it here.
  const totalEdges =
    [...kubeMCPs.value, ...linuxMCPs.value].reduce(
      (sum, m) => sum + (m.status?.connectedEdges ?? 0),
      0,
    )
  return { total, ready, totalEdges }
})

function handleRowClick(row: Record<string, unknown>) {
  // Linux MCP detail page TBD — for now both kinds route to the kube detail
  // page since KubernetesMCP and LinuxMCP have near-identical schemas.
  router.push(`/mcp/${row.name}?kind=${row.kind}`)
}

interface DeleteTarget {
  name: string
  kind: MCPKind
}
const deleteTarget = ref<DeleteTarget | null>(null)
const deleteBusy = ref(false)
const deleteError = ref<string | null>(null)

function requestDelete(name: string, kind: MCPKind, event: Event) {
  event.stopPropagation()
  deleteError.value = null
  deleteTarget.value = { name, kind }
}

async function confirmDelete() {
  if (!deleteTarget.value) return
  deleteBusy.value = true
  deleteError.value = null
  try {
    const mutation =
      deleteTarget.value.kind === 'aggregate'
        ? DELETE_AGGREGATE_MCP
        : deleteTarget.value.kind === 'linux'
        ? DELETE_LINUX_MCP
        : DELETE_MCP
    await graphqlMutate(mutation, { name: deleteTarget.value.name })
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

// --- Config snippet generation ---
const maskedToken = '••••••••••••••••'

// urlFor pulls the endpoint off the default MCP of a given kind.  Returns a
// placeholder when the controller hasn't populated status.URL yet.
function urlFor(kind: MCPKind): string {
  let item: MCPItem | undefined
  switch (kind) {
    case 'aggregate':
      item = defaultAggregateMCP.value
      break
    case 'linux':
      item = defaultLinuxMCP.value
      break
    default:
      item = defaultMCP.value
  }
  return item?.status?.URL ?? '<MCP_URL>'
}

// serverNameFor builds the `claude mcp add` short name. Matches the kedge CLI
// (`kedge mcp url`) so identifiers are stable whether the user wires up MCPs
// from the portal or the terminal:
//   aggregate (MCPServers/<n>)   -> kedge-<n>
//   kubernetes (KubernetesMCPs/<n>) -> kedge-kubernetes-clusters-<n>
//   linux (LinuxMCPs/<n>)        -> kedge-servers-<n>
// `name` defaults to "default" because the three header cards target the
// built-in `default` CR for each kind.
function serverNameFor(kind: MCPKind, name = 'default'): string {
  switch (kind) {
    case 'aggregate':
      return `kedge-${name}`
    case 'linux':
      return `kedge-servers-${name}`
    default:
      return `kedge-kubernetes-clusters-${name}`
  }
}

function buildClaudeCodeSnippet(token: string, kind: MCPKind) {
  return `claude mcp add --transport http ${serverNameFor(kind)} "${urlFor(kind)}" \\
  -H "Authorization: Bearer ${token}"`
}

function buildClaudeDesktopSnippet(token: string, kind: MCPKind) {
  return JSON.stringify(
    {
      mcpServers: {
        [serverNameFor(kind)]: {
          url: urlFor(kind),
          headers: { Authorization: `Bearer ${token}` },
        },
      },
    },
    null,
    2,
  )
}

const claudeCodeSnippet = computed(() => buildClaudeCodeSnippet(maskedToken, 'kubernetes'))
const claudeDesktopSnippet = computed(() => buildClaudeDesktopSnippet(maskedToken, 'kubernetes'))
const claudeCodeSnippetLinux = computed(() => buildClaudeCodeSnippet(maskedToken, 'linux'))
const claudeDesktopSnippetLinux = computed(() => buildClaudeDesktopSnippet(maskedToken, 'linux'))
const claudeCodeSnippetAggregate = computed(() => buildClaudeCodeSnippet(maskedToken, 'aggregate'))
const claudeDesktopSnippetAggregate = computed(() => buildClaudeDesktopSnippet(maskedToken, 'aggregate'))

async function copySnippet(
  builder: (token: string, kind: MCPKind) => string,
  field: string,
  kind: MCPKind = 'kubernetes',
) {
  try {
    const token = await auth.getValidToken()
    await navigator.clipboard.writeText(builder(token, kind))
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
      <div class="flex items-center gap-2">
        <button
          class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-2 text-[12px] font-medium text-text-secondary transition-all hover:border-accent/30 hover:text-accent"
          title="What's the difference between Aggregate, Kubernetes, and Linux MCP servers?"
          @click="showHelp = true"
        >
          <HelpCircle class="h-3.5 w-3.5" :stroke-width="1.75" />
          Which one do I use?
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

    <!-- Aggregate MCP (kube + linux + list_targets) — recommended entry point -->
    <div
      v-if="defaultAggregateMCP"
      class="border-beam stagger-item mb-5 rounded-2xl border border-accent/30 bg-surface-raised/80 p-4 backdrop-blur"
      style="animation-delay: 30ms"
    >
      <!-- Compact header row: always visible, click to expand snippets. -->
      <button
        class="flex w-full items-center gap-3 text-left"
        :aria-expanded="!!expanded['aggregate']"
        @click="toggle('aggregate')"
      >
        <Layers class="h-4 w-4 text-accent shrink-0" :stroke-width="1.75" />
        <span class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted shrink-0">
          {{ defaultAggregateMCP.spec?.displayName || 'Aggregate MCP — Default' }}
        </span>
        <span class="rounded-full border border-accent/30 bg-accent/10 px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wider text-accent shrink-0">
          recommended
        </span>
        <StatusBadge
          :status="defaultAggregateMCP.status?.conditions?.find(c => c.type === 'Ready')?.status === 'True' ? 'Ready' : 'Pending'"
        />
        <span class="text-[11px] text-text-muted ml-auto">
          {{ defaultAggregateMCP.status?.kubernetesEdges ?? 0 }} kube · {{ defaultAggregateMCP.status?.linuxEdges ?? 0 }} linux
          <template v-if="defaultAggregateMCP.spec?.readOnly"> · read-only</template>
        </span>
        <component
          :is="expanded['aggregate'] ? ChevronUp : ChevronDown"
          class="h-4 w-4 text-text-muted shrink-0"
          :stroke-width="1.75"
        />
      </button>

      <!-- Endpoint URL inline — visible whether expanded or not so users can
           grab the URL without expanding. -->
      <div class="mt-3 flex items-center gap-2 flex-wrap">
        <span class="text-[10px] uppercase tracking-wider text-text-muted">Endpoint</span>
        <code class="flex-1 min-w-0 truncate rounded-md border border-border-subtle bg-surface-overlay px-2 py-1 font-mono text-[11px] text-accent">
          {{ defaultAggregateMCP.status?.URL ?? 'Pending...' }}
        </code>
        <button
          v-if="defaultAggregateMCP.status?.URL"
          class="flex items-center gap-1 rounded-md border border-border-subtle px-2 py-1 text-[10px] text-text-muted transition-all hover:border-accent/30 hover:text-accent shrink-0"
          @click="copyToClipboard(defaultAggregateMCP.status!.URL!, 'url-aggregate')"
        >
          <component :is="copiedField === 'url-aggregate' ? Check : ClipboardCopy" class="h-3 w-3" :stroke-width="2" />
          {{ copiedField === 'url-aggregate' ? 'Copied' : 'Copy URL' }}
        </button>
      </div>

      <!-- Expanded body: description + Claude Code / Desktop snippets. -->
      <div v-if="expanded['aggregate']" class="mt-4 border-t border-border-subtle pt-4">
        <p class="mb-4 text-[12px] text-text-secondary">
          One endpoint with every kube cluster and Linux edge plus a
          <code class="rounded-md border border-border-subtle bg-surface-overlay px-1 py-0.5 font-mono text-[11px]">list_targets</code>
          tool the AI can call to discover what's available. Use this single entry instead of registering kube and linux MCPs separately.
        </p>

        <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4 min-w-0">
            <div class="flex items-center justify-between mb-2">
              <span class="text-[11px] font-semibold uppercase tracking-wider text-text-muted">Claude Code</span>
              <button
                class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                @click="copySnippet(buildClaudeCodeSnippet, 'claude-code-aggregate', 'aggregate')"
              >
                <component :is="copiedField === 'claude-code-aggregate' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                {{ copiedField === 'claude-code-aggregate' ? 'Copied' : 'Copy' }}
              </button>
            </div>
            <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ claudeCodeSnippetAggregate }}</pre>
          </div>

          <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4 min-w-0">
            <div class="flex items-center justify-between mb-2">
              <span class="text-[11px] font-semibold uppercase tracking-wider text-text-muted">Claude Desktop / claude_desktop_config.json</span>
              <button
                class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                @click="copySnippet(buildClaudeDesktopSnippet, 'claude-desktop-aggregate', 'aggregate')"
              >
                <component :is="copiedField === 'claude-desktop-aggregate' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                {{ copiedField === 'claude-desktop-aggregate' ? 'Copied' : 'Copy' }}
              </button>
            </div>
            <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ claudeDesktopSnippetAggregate }}</pre>
          </div>
        </div>
      </div>
    </div>

    <!-- Global Kubernetes MCP config card (collapsible) -->
    <div
      v-if="defaultMCP"
      class="border-beam stagger-item mb-5 rounded-2xl border border-border-subtle bg-surface-raised/80 p-4 backdrop-blur"
      style="animation-delay: 60ms"
    >
      <button
        class="flex w-full items-center gap-3 text-left"
        :aria-expanded="!!expanded['kube']"
        @click="toggle('kube')"
      >
        <Bot class="h-4 w-4 text-accent shrink-0" :stroke-width="1.75" />
        <span class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted shrink-0">
          {{ defaultMCP.spec?.displayName || 'Kubernetes MCP — Default' }}
        </span>
        <StatusBadge
          :status="defaultMCP.status?.conditions?.find(c => c.type === 'Ready')?.status === 'True' ? 'Ready' : 'Pending'"
        />
        <span class="text-[11px] text-text-muted ml-auto">
          {{ defaultMCP.status?.connectedEdges ?? 0 }} edges
          <template v-if="defaultMCP.spec?.readOnly"> · read-only</template>
        </span>
        <component
          :is="expanded['kube'] ? ChevronUp : ChevronDown"
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

      <div v-if="expanded['kube']" class="mt-4 border-t border-border-subtle pt-4">
        <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4 min-w-0">
            <div class="flex items-center justify-between mb-2">
              <span class="text-[11px] font-semibold uppercase tracking-wider text-text-muted">Claude Code</span>
              <button
                class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                @click="copySnippet(buildClaudeCodeSnippet, 'claude-code', 'kubernetes')"
              >
                <component :is="copiedField === 'claude-code' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                {{ copiedField === 'claude-code' ? 'Copied' : 'Copy' }}
              </button>
            </div>
            <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ claudeCodeSnippet }}</pre>
          </div>
          <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4 min-w-0">
            <div class="flex items-center justify-between mb-2">
              <span class="text-[11px] font-semibold uppercase tracking-wider text-text-muted">Claude Desktop / claude_desktop_config.json</span>
              <button
                class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                @click="copySnippet(buildClaudeDesktopSnippet, 'claude-desktop', 'kubernetes')"
              >
                <component :is="copiedField === 'claude-desktop' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                {{ copiedField === 'claude-desktop' ? 'Copied' : 'Copy' }}
              </button>
            </div>
            <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ claudeDesktopSnippet }}</pre>
          </div>
        </div>
      </div>
    </div>

    <!-- Global Linux MCP config card (server-type edges over SSH) -->
    <div
      v-if="defaultLinuxMCP"
      class="border-beam stagger-item mb-5 rounded-2xl border border-border-subtle bg-surface-raised/80 p-4 backdrop-blur"
      style="animation-delay: 90ms"
    >
      <button
        class="flex w-full items-center gap-3 text-left"
        :aria-expanded="!!expanded['linux']"
        @click="toggle('linux')"
      >
        <Terminal class="h-4 w-4 text-accent shrink-0" :stroke-width="1.75" />
        <span class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted shrink-0">
          {{ defaultLinuxMCP.spec?.displayName || 'Linux MCP — Default (SSH)' }}
        </span>
        <StatusBadge
          :status="defaultLinuxMCP.status?.conditions?.find(c => c.type === 'Ready')?.status === 'True' ? 'Ready' : 'Pending'"
        />
        <span class="text-[11px] text-text-muted ml-auto">
          {{ defaultLinuxMCP.status?.connectedEdges ?? 0 }} edges
          <template v-if="defaultLinuxMCP.spec?.readOnly"> · read-only</template>
        </span>
        <component
          :is="expanded['linux'] ? ChevronUp : ChevronDown"
          class="h-4 w-4 text-text-muted shrink-0"
          :stroke-width="1.75"
        />
      </button>

      <div class="mt-3 flex items-center gap-2 flex-wrap">
        <span class="text-[10px] uppercase tracking-wider text-text-muted">Endpoint</span>
        <code class="flex-1 min-w-0 truncate rounded-md border border-border-subtle bg-surface-overlay px-2 py-1 font-mono text-[11px] text-accent">
          {{ defaultLinuxMCP.status?.URL ?? 'Pending...' }}
        </code>
        <button
          v-if="defaultLinuxMCP.status?.URL"
          class="flex items-center gap-1 rounded-md border border-border-subtle px-2 py-1 text-[10px] text-text-muted transition-all hover:border-accent/30 hover:text-accent shrink-0"
          @click="copyToClipboard(defaultLinuxMCP.status!.URL!, 'url-linux')"
        >
          <component :is="copiedField === 'url-linux' ? Check : ClipboardCopy" class="h-3 w-3" :stroke-width="2" />
          {{ copiedField === 'url-linux' ? 'Copied' : 'Copy URL' }}
        </button>
      </div>

      <div v-if="expanded['linux']" class="mt-4 border-t border-border-subtle pt-4">
        <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4 min-w-0">
            <div class="flex items-center justify-between mb-2">
              <span class="text-[11px] font-semibold uppercase tracking-wider text-text-muted">Claude Code</span>
              <button
                class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                @click="copySnippet(buildClaudeCodeSnippet, 'claude-code-linux', 'linux')"
              >
                <component :is="copiedField === 'claude-code-linux' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                {{ copiedField === 'claude-code-linux' ? 'Copied' : 'Copy' }}
              </button>
            </div>
            <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ claudeCodeSnippetLinux }}</pre>
          </div>
          <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4 min-w-0">
            <div class="flex items-center justify-between mb-2">
              <span class="text-[11px] font-semibold uppercase tracking-wider text-text-muted">Claude Desktop / claude_desktop_config.json</span>
              <button
                class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                @click="copySnippet(buildClaudeDesktopSnippet, 'claude-desktop-linux', 'linux')"
              >
                <component :is="copiedField === 'claude-desktop-linux' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                {{ copiedField === 'claude-desktop-linux' ? 'Copied' : 'Copy' }}
              </button>
            </div>
            <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ claudeDesktopSnippetLinux }}</pre>
          </div>
        </div>
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
            <!-- Custom display name (spec.displayName) shown as subtitle so
                 operators see the title their AI clients will surface. -->
            <span
              v-if="row.displayName"
              class="truncate text-[10px] text-text-muted/70"
              :title="row.displayName as string"
            >
              {{ row.displayName }}
            </span>
          </div>
        </template>
        <template #kind="{ value }">
          <span
            class="inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-wider"
            :class="value === 'aggregate'
              ? 'border-success/30 bg-success/10 text-success'
              : value === 'linux'
              ? 'border-warning/30 bg-warning/10 text-warning'
              : 'border-accent/30 bg-accent/10 text-accent'"
          >
            <component
              :is="value === 'aggregate' ? Layers : value === 'linux' ? Terminal : Bot"
              class="h-3 w-3"
              :stroke-width="2"
            />
            {{ value === 'aggregate' ? 'Aggregate' : value === 'linux' ? 'Linux' : 'Kubernetes' }}
          </span>
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
            @click.stop="requestDelete(row.name as string, row.kind as MCPKind, $event)"
          >
            <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" />
          </button>
        </template>
      </ResourceTable>
    </div>

    <!-- Help modal: explains aggregate vs kubernetes vs linux. -->
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
      :message="`This will permanently delete ${deleteTarget.kind === 'aggregate' ? 'Aggregate' : deleteTarget.kind === 'linux' ? 'Linux' : 'Kubernetes'} MCP server '${deleteTarget.name}'. This cannot be undone.`"
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
