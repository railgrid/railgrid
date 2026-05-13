<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import YamlViewer from '@/components/YamlViewer.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { useGraphQLQuery, graphqlMutate } from '@/composables/useGraphQL'
import { useAuthStore } from '@/stores/auth'
import { useTerminalSessionsStore } from '@/stores/terminalSessions'
import { GET_EDGE, GET_EDGE_YAML, type GetEdgeResult, type GetEdgeYamlResult } from '@/graphql/queries/edges'
import { DELETE_EDGE, UPDATE_EDGE } from '@/graphql/mutations'
import { formatDateTimeWithAge } from '@/utils/time'
import { Server, Wifi, WifiOff, Clock, Hash, Activity, FileCode, ChevronDown, ChevronUp, ArrowLeft, TerminalSquare, Copy, Check, Trash2, Pencil, Tag } from 'lucide-vue-next'

const props = defineProps<{ name: string }>()
const auth = useAuthStore()
const router = useRouter()
const terminalStore = useTerminalSessionsStore()

function openTerminal(event: MouseEvent) {
  const cluster = auth.clusterName
  if (!cluster) return
  // Shift-click opens a brand-new session even if one already exists for this edge.
  terminalStore.openSession({
    edgeName: props.name,
    cluster,
    forceNew: event.shiftKey,
  })
}

const { data, loading, error, refetch } = useGraphQLQuery<GetEdgeResult>(
  GET_EDGE,
  { name: props.name },
  10000,
)

const showDeleteConfirm = ref(false)
const deleteBusy = ref(false)
const deleteError = ref<string | null>(null)

async function handleDelete() {
  deleteBusy.value = true
  deleteError.value = null
  try {
    await graphqlMutate(DELETE_EDGE, { name: props.name })
    router.push('/edges')
  } catch (e) {
    deleteError.value = e instanceof Error ? e.message : 'Delete failed'
  } finally {
    deleteBusy.value = false
  }
}

// --- Labels edit ---
const editingLabels = ref(false)
const labelsInput = ref('')
const labelsSaving = ref(false)
const labelsError = ref<string | null>(null)

const labelEntries = computed(() => {
  const labels = edge.value?.metadata?.labels ?? {}
  return Object.entries(labels)
})

function startLabelsEdit() {
  const labels = edge.value?.metadata?.labels ?? {}
  labelsInput.value = Object.entries(labels)
    .map(([k, v]) => `${k}=${v}`)
    .join(', ')
  labelsError.value = null
  editingLabels.value = true
}

function cancelLabelsEdit() {
  editingLabels.value = false
  labelsError.value = null
}

async function saveLabels() {
  labelsSaving.value = true
  labelsError.value = null
  try {
    const parsed: Record<string, string> = {}
    if (labelsInput.value.trim()) {
      for (const pair of labelsInput.value.split(',')) {
        const [k, v] = pair.split('=').map((s) => s.trim())
        if (k) parsed[k] = v ?? ''
      }
    }
    await graphqlMutate(UPDATE_EDGE, {
      name: props.name,
      object: { metadata: { labels: parsed } },
    })
    editingLabels.value = false
    await refetch()
  } catch (e) {
    labelsError.value = e instanceof Error ? e.message : 'Save failed'
  } finally {
    labelsSaving.value = false
  }
}

const showYaml = ref(false)
const { data: yamlData, loading: yamlLoading } = useGraphQLQuery<GetEdgeYamlResult>(
  GET_EDGE_YAML,
  { name: props.name },
)

const edge = computed(() => data.value?.kedge_faros_sh?.v1alpha1?.Edge)
const yaml = computed(() => yamlData.value?.kedge_faros_sh?.v1alpha1?.EdgeYaml ?? '')

const canSSH = computed(() => {
  if (!edge.value) return false
  return edge.value.spec?.type === 'server' && edge.value.status?.connected
})

// CLI access commands
const isServerType = computed(() => edge.value?.spec?.type === 'server')
const isK8sType = computed(() => edge.value?.spec?.type === 'kubernetes')

const sshCommand = computed(() => {
  if (!isServerType.value || !edge.value?.status?.connected) return ''
  return `kedge ssh ${props.name}`
})

const sshCommandWithArgs = computed(() => {
  if (!isServerType.value || !edge.value?.status?.connected) return ''
  return `kedge ssh ${props.name} -- <command>`
})

const mcpCommand = computed(() => {
  if (!isK8sType.value || !edge.value?.status?.connected) return ''
  return `kedge mcp url --edge ${props.name}`
})

const hasAccessCommands = computed(() => {
  return (isServerType.value && sshCommand.value) || (isK8sType.value && mcpCommand.value)
})

// --- Join instructions ---
const showJoinInstructions = ref(false)
const showAccessCommands = ref(false)
const copiedField = ref<string | null>(null)

const joinToken = computed(() => edge.value?.status?.joinToken ?? null)
const joinTokenError = computed(() => {
  if (!edge.value) return null
  if (!edge.value.status?.joinToken) return 'Join token not available. It may have been consumed after first connection.'
  return null
})

const needsJoin = computed(() => {
  if (!edge.value) return false
  return !edge.value.status?.connected
})

const hubURL = computed(() => {
  const origin = window.location.origin
  const cluster = auth.clusterName
  return cluster ? `${origin}/clusters/${cluster}` : origin
})

const edgeType = computed(() => edge.value?.spec?.type ?? 'kubernetes')
const maskedToken = '••••••••••••••••'

function buildHelmSnippet(token: string) {
  return `helm install kedge-agent oci://ghcr.io/faroshq/charts/kedge-agent \\
  --namespace kedge-agent --create-namespace \\
  --set agent.edgeName=${props.name} \\
  --set agent.hub.url=${hubURL.value} \\
  --set agent.hub.token=${token}`
}

function buildCLIJoinSnippet(token: string) {
  return `kedge agent join \\
  --hub-url ${hubURL.value} \\
  --edge-name ${props.name} \\
  --type ${edgeType.value} \\
  --token ${token}`
}

function buildCLIRunSnippet(token: string) {
  return `kedge agent run \\
  --hub-url ${hubURL.value} \\
  --edge-name ${props.name} \\
  --type ${edgeType.value} \\
  --token ${token}`
}

const helmSnippet = computed(() => buildHelmSnippet(maskedToken))
const cliJoinSnippet = computed(() => buildCLIJoinSnippet(maskedToken))
const cliRunSnippet = computed(() => buildCLIRunSnippet(maskedToken))

function toggleJoinInstructions() {
  showJoinInstructions.value = !showJoinInstructions.value
}

// --- Regenerate join token ---
const regenerating = ref(false)
const regenerateError = ref<string | null>(null)

async function regenerateJoinToken() {
  regenerating.value = true
  regenerateError.value = null
  try {
    const existing = edge.value?.metadata?.labels ?? {}
    await graphqlMutate(UPDATE_EDGE, {
      name: props.name,
      object: {
        metadata: {
          annotations: { 'kedge.faros.sh/regenerate-join-token': 'true' },
          // Preserve labels so the partial update doesn't drop them.
          ...(Object.keys(existing).length > 0 ? { labels: existing } : {}),
        },
      },
    })
    // Poll until the controller mints a fresh token (typically <2s).
    const deadline = Date.now() + 30000
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 1000))
      await refetch()
      if (edge.value?.status?.joinToken) {
        showJoinInstructions.value = true
        return
      }
    }
    regenerateError.value = 'Timed out waiting for new join token. Refresh and try again.'
  } catch (e) {
    regenerateError.value = e instanceof Error ? e.message : 'Regenerate failed'
  } finally {
    regenerating.value = false
  }
}

async function copySnippet(builder: (token: string) => string, field: string) {
  if (!joinToken.value) return
  try {
    await navigator.clipboard.writeText(builder(joinToken.value))
    copiedField.value = field
    setTimeout(() => (copiedField.value = null), 2000)
  } catch {}
}

async function copyToClipboard(text: string, field: string) {
  try {
    await navigator.clipboard.writeText(text)
    copiedField.value = field
    setTimeout(() => (copiedField.value = null), 2000)
  } catch {}
}

const details = computed(() => {
  if (!edge.value) return []
  return [
    { label: 'Type', value: edge.value.spec?.type, icon: Server },
    { label: 'Agent Version', value: edge.value.status?.agentVersion || '-', icon: Hash },
    { label: 'Last Heartbeat', value: edge.value.status?.lastHeartbeatTime ? formatDateTimeWithAge(edge.value.status.lastHeartbeatTime) : '-', icon: Activity },
    { label: 'Created', value: formatDateTimeWithAge(edge.value.metadata?.creationTimestamp), icon: Clock },
  ].filter((d) => d.value)
})
</script>

<template>
  <AppLayout>
    <!-- Back link -->
    <router-link
      to="/edges"
      class="stagger-item mb-5 inline-flex items-center gap-1.5 rounded-lg px-2 py-1 text-[12px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
      style="animation-delay: 0ms"
    >
      <ArrowLeft class="h-3 w-3" :stroke-width="2" />
      Back to edges
    </router-link>

    <div v-if="error" class="flex items-center gap-2 rounded-xl border border-danger/20 bg-danger-subtle p-4 text-[13px] text-danger">
      {{ error }}
    </div>

    <div v-else-if="loading && !data" class="mt-16 flex flex-col items-center justify-center gap-3">
      <div class="shimmer h-8 w-8 rounded-xl" />
      <div class="shimmer h-3 w-40 rounded" />
    </div>

    <template v-else-if="edge">
      <!-- Asymmetric layout: big hero left, stacked info right -->
      <div class="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <!-- Hero card (spans 2 cols) -->
        <div
          class="border-beam stagger-item col-span-1 flex flex-col rounded-2xl border border-border-subtle bg-surface-raised/80 p-6 backdrop-blur lg:col-span-2"
          style="animation-delay: 40ms"
        >
          <div class="flex items-start gap-4">
            <div class="relative flex h-14 w-14 shrink-0 items-center justify-center">
              <div class="absolute inset-0 rounded-xl bg-accent/15 blur-md" />
              <div class="relative flex h-14 w-14 items-center justify-center rounded-xl border border-accent/20 bg-surface-overlay">
                <Server class="h-7 w-7 text-accent" :stroke-width="1.5" />
              </div>
            </div>
            <div class="flex-1">
              <div class="flex items-center gap-3">
                <h1 class="text-gradient text-xl font-bold tracking-tight">{{ edge.metadata?.name }}</h1>
                <StatusBadge :status="edge.status?.phase" :connected="edge.status?.connected" />
              </div>
              <div class="mt-1.5 flex items-center gap-4 text-[12px] text-text-muted">
                <div class="flex items-center gap-1.5">
                  <component
                    :is="edge.status?.connected ? Wifi : WifiOff"
                    class="h-3 w-3"
                    :class="edge.status?.connected ? 'text-success' : 'text-danger'"
                    :stroke-width="1.75"
                  />
                  {{ edge.status?.connected ? 'Connected' : 'Disconnected' }}
                </div>
                <span class="font-mono text-[11px] text-text-muted/60">{{ edge.spec?.type }}</span>
              </div>
            </div>
            <div class="flex items-center gap-2">
              <button
                class="flex items-center gap-1.5 rounded-lg border border-border-subtle bg-surface-overlay/80 px-3 py-1.5 text-[11px] font-medium text-text-secondary transition-all hover:border-danger/30 hover:bg-danger-subtle hover:text-danger"
                @click="showDeleteConfirm = true"
              >
                <Trash2 class="h-3 w-3" :stroke-width="2" />
                Delete
              </button>
            </div>
          </div>

          <!-- Details grid inside hero -->
          <dl class="mt-6 grid grid-cols-2 gap-x-8 gap-y-3 border-t border-border-subtle pt-5">
            <div
              v-for="item in details"
              :key="item.label"
              class="flex items-center justify-between"
            >
              <dt class="flex items-center gap-2 text-[12px] text-text-muted">
                <component :is="item.icon" class="h-3 w-3" :stroke-width="1.75" />
                {{ item.label }}
              </dt>
              <dd class="text-[12px] text-text-secondary">
                {{ item.value }}
              </dd>
            </div>
          </dl>
        </div>

        <!-- Conditions (right column, tall) -->
        <div
          class="stagger-item rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 120ms"
        >
          <h2 class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Conditions</h2>
          <div v-if="edge.status?.conditions?.length" class="mt-4 space-y-2">
            <div
              v-for="cond in edge.status.conditions"
              :key="cond.type"
              class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-3 transition-colors duration-150 hover:border-border-default"
            >
              <div class="flex items-center justify-between">
                <span class="text-[12px] font-medium text-text-primary">{{ cond.type }}</span>
                <StatusBadge :status="cond.status === 'True' ? 'Ready' : 'Pending'" />
              </div>
              <p v-if="cond.message" class="mt-1.5 text-[11px] leading-relaxed text-text-muted">{{ cond.message }}</p>
            </div>
          </div>
          <div v-else class="mt-8 flex flex-col items-center text-text-muted/30">
            <p class="text-[12px]">No conditions</p>
          </div>
        </div>
      </div>

      <!-- Labels -->
      <div
        class="stagger-item mt-4 rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
        style="animation-delay: 160ms"
      >
        <div class="flex items-center justify-between">
          <h2 class="flex items-center gap-2 text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">
            <Tag class="h-3 w-3" :stroke-width="1.75" />
            Labels
          </h2>
          <button
            v-if="!editingLabels"
            class="flex items-center gap-1.5 rounded-lg border border-border-subtle bg-surface-overlay/80 px-2.5 py-1 text-[11px] font-medium text-text-secondary transition-all hover:border-accent/30 hover:text-accent"
            @click="startLabelsEdit"
          >
            <Pencil class="h-3 w-3" :stroke-width="2" />
            Edit
          </button>
        </div>

        <div v-if="!editingLabels" class="mt-3">
          <div v-if="labelEntries.length" class="flex flex-wrap gap-2">
            <span
              v-for="[k, v] in labelEntries"
              :key="k"
              class="rounded-md border border-border-subtle bg-surface-overlay px-2 py-0.5 font-mono text-[11px] text-text-secondary"
            >
              {{ k }}<span class="text-text-muted">=</span>{{ v }}
            </span>
          </div>
          <p v-else class="text-[12px] text-text-muted/60">No labels. Edit to add labels for MCP edge selectors.</p>
        </div>

        <div v-else class="mt-3">
          <input
            v-model="labelsInput"
            type="text"
            placeholder="env=prod, region=us-east"
            class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
          />
          <p class="mt-1 text-[10px] text-text-muted">Comma-separated key=value pairs. Leave empty to remove all labels.</p>
          <div v-if="labelsError" class="mt-2 rounded-lg border border-danger/20 bg-danger-subtle p-2 text-[11px] text-danger">
            {{ labelsError }}
          </div>
          <div class="mt-3 flex items-center justify-end gap-2">
            <button
              class="rounded-lg border border-border-subtle px-3 py-1.5 text-[11px] font-medium text-text-secondary transition-all hover:bg-surface-hover"
              :disabled="labelsSaving"
              @click="cancelLabelsEdit"
            >
              Cancel
            </button>
            <button
              class="rounded-lg bg-accent px-3 py-1.5 text-[11px] font-medium text-white transition-all hover:bg-accent-hover disabled:opacity-50"
              :disabled="labelsSaving"
              @click="saveLabels"
            >
              {{ labelsSaving ? 'Saving...' : 'Save' }}
            </button>
          </div>
        </div>
      </div>

      <!-- Actions -->
      <div class="stagger-item mt-5 flex items-center gap-3" style="animation-delay: 200ms">
        <!-- SSH Terminal button — opens in the bottom dock; shift-click for a new tab -->
        <button
          v-if="canSSH"
          class="glow-ring flex items-center gap-2 rounded-xl border border-accent/30 bg-accent/10 px-4 py-2 text-[12px] font-medium text-accent backdrop-blur transition-all duration-150 hover:bg-accent/20 hover:shadow-lg hover:shadow-accent/10"
          title="Open SSH terminal (shift-click for a new session)"
          @click="openTerminal"
        >
          <TerminalSquare class="h-3.5 w-3.5" :stroke-width="1.75" />
          SSH Terminal
        </button>

        <button
          v-if="needsJoin"
          class="glow-ring flex items-center gap-2 rounded-xl border border-accent/30 bg-accent/10 px-4 py-2 text-[12px] font-medium text-accent backdrop-blur transition-all duration-150 hover:bg-accent/20"
          @click="toggleJoinInstructions"
        >
          <TerminalSquare class="h-3.5 w-3.5" :stroke-width="1.75" />
          {{ showJoinInstructions ? 'Hide' : 'Show' }} Join Instructions
          <component :is="showJoinInstructions ? ChevronUp : ChevronDown" class="h-3 w-3" :stroke-width="1.75" />
        </button>

        <button
          class="glow-ring flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-4 py-2 text-[12px] font-medium text-text-secondary backdrop-blur transition-all duration-150 hover:border-accent/30 hover:text-text-primary"
          @click="showYaml = !showYaml"
        >
          <FileCode class="h-3.5 w-3.5" :stroke-width="1.75" />
          {{ showYaml ? 'Hide' : 'Show' }} YAML
          <component :is="showYaml ? ChevronUp : ChevronDown" class="h-3 w-3 text-text-muted" :stroke-width="1.75" />
        </button>

        <button
          v-if="hasAccessCommands"
          class="glow-ring flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-4 py-2 text-[12px] font-medium text-text-secondary backdrop-blur transition-all duration-150 hover:border-accent/30 hover:text-text-primary"
          @click="showAccessCommands = !showAccessCommands"
        >
          <TerminalSquare class="h-3.5 w-3.5" :stroke-width="1.75" />
          {{ showAccessCommands ? 'Hide' : 'Show' }} CLI Commands
          <component :is="showAccessCommands ? ChevronUp : ChevronDown" class="h-3 w-3 text-text-muted" :stroke-width="1.75" />
        </button>
      </div>

      <!-- Join Instructions -->
      <div v-if="showJoinInstructions" class="stagger-item mt-4" style="animation-delay: 220ms">
        <div v-if="joinTokenError" class="rounded-xl border border-warning/20 bg-warning/5 p-4 text-[12px] text-warning">
          <p>{{ joinTokenError }}</p>
          <div v-if="regenerateError" class="mt-2 text-[11px] text-danger">{{ regenerateError }}</div>
          <button
            class="mt-3 flex items-center gap-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-1.5 text-[11px] font-medium text-warning transition-all hover:bg-warning/20 disabled:opacity-50"
            :disabled="regenerating"
            @click="regenerateJoinToken"
          >
            {{ regenerating ? 'Generating new token...' : 'Regenerate join token' }}
          </button>
        </div>

        <template v-else-if="joinToken">
          <!-- Step 1: Install -->
          <div class="mb-4">
            <h3 class="text-[11px] font-semibold uppercase tracking-[0.15em] text-text-muted mb-2">Step 1: Install the kedge CLI</h3>
            <pre class="overflow-x-auto rounded-lg bg-surface-overlay/60 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">curl -fsSL https://github.com/faroshq/kedge/releases/latest/download/kubectl-kedge_linux_amd64.tar.gz | tar xz
sudo mv kubectl-kedge /usr/local/bin/kedge

# Or via krew:
kubectl krew index add faros https://github.com/faroshq/krew-index.git
kubectl krew install faros/kedge</pre>
          </div>

          <!-- Step 2: Connect -->
          <div>
            <h3 class="text-[11px] font-semibold uppercase tracking-[0.15em] text-text-muted mb-2">
              Step 2: Connect this {{ edgeType === 'kubernetes' ? 'Kubernetes cluster' : 'server' }} as an edge
            </h3>
            <div class="space-y-3">
              <!-- Helm (kubernetes only) -->
              <div v-if="edgeType === 'kubernetes'" class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4">
                <div class="flex items-center justify-between mb-2">
                  <span class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Option A — Helm (recommended)</span>
                  <button
                    class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                    @click="copySnippet(buildHelmSnippet, 'helm')"
                  >
                    <component :is="copiedField === 'helm' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                    {{ copiedField === 'helm' ? 'Copied' : 'Copy' }}
                  </button>
                </div>
                <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ helmSnippet }}</pre>
              </div>

              <!-- CLI join -->
              <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4">
                <div class="flex items-center justify-between mb-2">
                  <span class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">
                    {{ edgeType === 'kubernetes' ? 'Option B' : 'Option A' }} — CLI persistent install
                  </span>
                  <button
                    class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                    @click="copySnippet(buildCLIJoinSnippet, 'join')"
                  >
                    <component :is="copiedField === 'join' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                    {{ copiedField === 'join' ? 'Copied' : 'Copy' }}
                  </button>
                </div>
                <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ cliJoinSnippet }}</pre>
              </div>

              <!-- CLI run -->
              <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4">
                <div class="flex items-center justify-between mb-2">
                  <span class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">
                    {{ edgeType === 'kubernetes' ? 'Option C' : 'Option B' }} — Foreground process (dev)
                  </span>
                  <button
                    class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                    @click="copySnippet(buildCLIRunSnippet, 'run')"
                  >
                    <component :is="copiedField === 'run' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                    {{ copiedField === 'run' ? 'Copied' : 'Copy' }}
                  </button>
                </div>
                <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ cliRunSnippet }}</pre>
              </div>
            </div>
          </div>
        </template>
      </div>

      <div v-if="showYaml" class="stagger-item mt-3" style="animation-delay: 240ms">
        <div v-if="yamlLoading" class="flex items-center gap-2 text-[12px] text-text-muted">
          <div class="shimmer h-4 w-4 rounded" />
          Loading...
        </div>
        <div v-else class="border-beam rounded-2xl">
          <YamlViewer :source="yaml" />
        </div>
      </div>

      <!-- Access Commands -->
      <div v-if="showAccessCommands && hasAccessCommands" class="stagger-item mt-4" style="animation-delay: 260ms">
        <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4">
          <div class="flex items-center justify-between mb-3">
            <h3 class="text-[11px] font-semibold uppercase tracking-[0.15em] text-text-muted">
              CLI Access Commands
            </h3>
          </div>

          <!-- SSH Commands for Server Type -->
          <div v-if="isServerType && sshCommand" class="space-y-3 mb-4">
            <div class="text-[12px] text-text-secondary mb-2">
              Connect to this server via SSH:
            </div>
            <div class="rounded-lg bg-surface/80 p-3">
              <div class="flex items-center justify-between mb-1">
                <span class="text-[10px] font-medium text-text-muted">Interactive SSH session</span>
                <button
                  class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                  @click="copyToClipboard(sshCommand, 'ssh')"
                >
                  <component :is="copiedField === 'ssh' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                  {{ copiedField === 'ssh' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="font-mono text-[11px] text-text-secondary">{{ sshCommand }}</pre>
            </div>
            <div class="rounded-lg bg-surface/80 p-3">
              <div class="flex items-center justify-between mb-1">
                <span class="text-[10px] font-medium text-text-muted">Run single command</span>
                <button
                  class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                  @click="copyToClipboard(sshCommandWithArgs, 'ssh-cmd')"
                >
                  <component :is="copiedField === 'ssh-cmd' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                  {{ copiedField === 'ssh-cmd' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="font-mono text-[11px] text-text-secondary">{{ sshCommandWithArgs }}</pre>
            </div>
          </div>

          <!-- MCP Command for Kubernetes Type -->
          <div v-if="isK8sType && mcpCommand" class="space-y-3">
            <div class="text-[12px] text-text-secondary mb-2">
              Connect to this Kubernetes cluster via WebSocket (MCP):
            </div>
            <div class="rounded-lg bg-surface/80 p-3">
              <div class="flex items-center justify-between mb-1">
                <span class="text-[10px] font-medium text-text-muted">MCP endpoint URL</span>
                <button
                  class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                  @click="copyToClipboard(mcpCommand, 'mcp')"
                >
                  <component :is="copiedField === 'mcp' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                  {{ copiedField === 'mcp' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="font-mono text-[11px] text-text-secondary">{{ mcpCommand }}</pre>
            </div>
          </div>
        </div>
      </div>

      <!-- Access Commands -->
      <div v-if="showAccessCommands && hasAccessCommands" class="stagger-item mt-4" style="animation-delay: 260ms">
        <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4">
          <div class="flex items-center justify-between mb-3">
            <h3 class="text-[11px] font-semibold uppercase tracking-[0.15em] text-text-muted">
              CLI Access Commands
            </h3>
          </div>

          <!-- SSH Commands for Server Type -->
          <div v-if="isServerType && sshCommand" class="space-y-3 mb-4">
            <div class="text-[12px] text-text-secondary mb-2">
              Connect to this server via SSH:
            </div>
            <div class="rounded-lg bg-surface/80 p-3">
              <div class="flex items-center justify-between mb-1">
                <span class="text-[10px] font-medium text-text-muted">Interactive SSH session</span>
                <button
                  class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                  @click="copyToClipboard(sshCommand, 'ssh')"
                >
                  <component :is="copiedField === 'ssh' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                  {{ copiedField === 'ssh' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="font-mono text-[11px] text-text-secondary">{{ sshCommand }}</pre>
            </div>
            <div class="rounded-lg bg-surface/80 p-3">
              <div class="flex items-center justify-between mb-1">
                <span class="text-[10px] font-medium text-text-muted">Run single command</span>
                <button
                  class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                  @click="copyToClipboard(sshCommandWithArgs, 'ssh-cmd')"
                >
                  <component :is="copiedField === 'ssh-cmd' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                  {{ copiedField === 'ssh-cmd' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="font-mono text-[11px] text-text-secondary">{{ sshCommandWithArgs }}</pre>
            </div>
          </div>

          <!-- MCP Command for Kubernetes Type -->
          <div v-if="isK8sType && mcpCommand" class="space-y-3">
            <div class="text-[12px] text-text-secondary mb-2">
              Connect to this Kubernetes cluster via WebSocket (MCP):
            </div>
            <div class="rounded-lg bg-surface/80 p-3">
              <div class="flex items-center justify-between mb-1">
                <span class="text-[10px] font-medium text-text-muted">MCP endpoint URL</span>
                <button
                  class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                  @click="copyToClipboard(mcpCommand, 'mcp')"
                >
                  <component :is="copiedField === 'mcp' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                  {{ copiedField === 'mcp' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="font-mono text-[11px] text-text-secondary">{{ mcpCommand }}</pre>
            </div>
          </div>
        </div>
      </div>
    </template>

    <ConfirmDialog
      v-if="showDeleteConfirm"
      title="Delete edge?"
      :message="`This will permanently delete edge ${props.name} and revoke its agent credentials. This cannot be undone.`"
      confirm-label="Delete"
      :busy="deleteBusy"
      @cancel="showDeleteConfirm = false"
      @confirm="handleDelete"
    />
    <div
      v-if="deleteError"
      class="fixed bottom-4 right-4 z-[110] rounded-lg border border-danger/20 bg-danger-subtle px-4 py-3 text-[12px] text-danger shadow-lg"
    >
      {{ deleteError }}
    </div>
  </AppLayout>
</template>
