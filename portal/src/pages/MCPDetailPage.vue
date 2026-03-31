<script setup lang="ts">
import { computed, ref, toRef } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { useMCPGet, updateMCP, deleteMCP, type KubernetesMCP } from '@/composables/useKubeAPI'
import { useAuthStore } from '@/stores/auth'
import {
  Bot, ArrowLeft, Wifi, WifiOff, Server, Hash, Clock, Copy, Check,
  FileCode, ChevronDown, ChevronUp, Pencil, Trash2, Shield,
} from 'lucide-vue-next'

const props = defineProps<{ name: string }>()
const router = useRouter()
const auth = useAuthStore()

const nameRef = toRef(props, 'name')
const { data: mcp, loading, error, refetch } = useMCPGet(nameRef, 10000)

const showYaml = ref(false)
const editing = ref(false)
const editToolsets = ref('')
const editReadOnly = ref(false)
const editMatchLabels = ref('')
const saving = ref(false)
const saveError = ref<string | null>(null)
const copiedField = ref<string | null>(null)

const readyCond = computed(() => mcp.value?.status?.conditions?.find((c) => c.type === 'Ready'))

const details = computed(() => {
  if (!mcp.value) return []
  return [
    { label: 'Endpoint URL', value: mcp.value.status?.URL || 'Pending...', icon: Server, mono: true },
    { label: 'Connected Edges', value: String(mcp.value.status?.connectedEdges ?? 0), icon: Wifi },
    { label: 'Toolsets', value: mcp.value.spec?.toolsets?.length ? mcp.value.spec.toolsets.join(', ') : 'all', icon: Hash },
    { label: 'Read Only', value: mcp.value.spec?.readOnly ? 'Yes' : 'No', icon: Shield },
    { label: 'Created', value: mcp.value.metadata?.creationTimestamp ?? '-', icon: Clock },
    { label: 'UID', value: mcp.value.metadata?.uid ?? '-', icon: Hash, mono: true },
  ]
})

const edgeSelectorDisplay = computed(() => {
  const sel = mcp.value?.spec?.edgeSelector
  if (!sel) return 'All edges (no selector)'
  const labels = sel.matchLabels
  if (labels && Object.keys(labels).length > 0) {
    return Object.entries(labels).map(([k, v]) => `${k}=${v}`).join(', ')
  }
  if (sel.matchExpressions?.length) {
    return sel.matchExpressions.map((e) => `${e.key} ${e.operator} ${e.values?.join(',') ?? ''}`).join(', ')
  }
  return 'All edges (empty selector)'
})

const yamlContent = computed(() => {
  if (!mcp.value) return ''
  return JSON.stringify(mcp.value, null, 2)
})

// --- Config snippets ---
const maskedToken = '••••••••••••••••'

function buildClaudeCodeSnippet(token: string) {
  const url = mcp.value?.status?.URL ?? '<MCP_URL>'
  return `claude mcp add --transport http kedge-${props.name} "${url}" \\
  -H "Authorization: Bearer ${token}"`
}

function buildClaudeDesktopSnippet(token: string) {
  const url = mcp.value?.status?.URL ?? '<MCP_URL>'
  return JSON.stringify(
    {
      mcpServers: {
        [`kedge-${props.name}`]: {
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

function startEdit() {
  if (!mcp.value) return
  editToolsets.value = mcp.value.spec?.toolsets?.join(', ') ?? ''
  editReadOnly.value = mcp.value.spec?.readOnly ?? false
  const labels = mcp.value.spec?.edgeSelector?.matchLabels
  editMatchLabels.value = labels
    ? Object.entries(labels).map(([k, v]) => `${k}=${v}`).join(', ')
    : ''
  saveError.value = null
  editing.value = true
}

async function saveEdit() {
  if (!mcp.value) return
  saving.value = true
  saveError.value = null
  try {
    const updated: KubernetesMCP = {
      ...mcp.value,
      spec: {
        ...mcp.value.spec,
        toolsets: editToolsets.value.trim()
          ? editToolsets.value.split(',').map((s) => s.trim()).filter(Boolean)
          : undefined,
        readOnly: editReadOnly.value,
        edgeSelector: editMatchLabels.value.trim()
          ? {
              matchLabels: Object.fromEntries(
                editMatchLabels.value.split(',').map((pair) => {
                  const [k, v] = pair.split('=').map((s) => s.trim())
                  return [k, v ?? '']
                }),
              ),
            }
          : undefined,
      },
    }
    await updateMCP(updated)
    editing.value = false
    await refetch()
  } catch (e) {
    saveError.value = e instanceof Error ? e.message : 'Save failed'
  } finally {
    saving.value = false
  }
}

async function handleDelete() {
  if (!confirm(`Delete MCP server "${props.name}"? This cannot be undone.`)) return
  try {
    await deleteMCP(props.name)
    router.push('/mcp')
  } catch (e) {
    alert(e instanceof Error ? e.message : 'Delete failed')
  }
}

async function copySnippet(builder: (token: string) => string, field: string) {
  try {
    const token = await auth.getValidToken()
    await navigator.clipboard.writeText(builder(token))
    copiedField.value = field
    setTimeout(() => (copiedField.value = null), 2000)
  } catch {}
}

</script>

<template>
  <AppLayout>
    <!-- Back link -->
    <router-link
      to="/mcp"
      class="stagger-item mb-5 inline-flex items-center gap-1.5 rounded-lg px-2 py-1 text-[12px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
      style="animation-delay: 0ms"
    >
      <ArrowLeft class="h-3 w-3" :stroke-width="2" />
      Back to MCP servers
    </router-link>

    <div v-if="error" class="flex items-center gap-2 rounded-xl border border-danger/20 bg-danger-subtle p-4 text-[13px] text-danger">
      {{ error }}
    </div>

    <div v-else-if="loading && !mcp" class="mt-16 flex flex-col items-center justify-center gap-3">
      <div class="shimmer h-8 w-8 rounded-xl" />
      <div class="shimmer h-3 w-40 rounded" />
    </div>

    <template v-else-if="mcp">
      <!-- Hero + info grid -->
      <div class="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <!-- Hero card -->
        <div
          class="border-beam stagger-item col-span-1 flex flex-col rounded-2xl border border-border-subtle bg-surface-raised/80 p-6 backdrop-blur lg:col-span-2"
          style="animation-delay: 40ms"
        >
          <div class="flex items-start gap-4">
            <div class="relative flex h-14 w-14 shrink-0 items-center justify-center">
              <div class="absolute inset-0 rounded-xl bg-accent/15 blur-md" />
              <div class="relative flex h-14 w-14 items-center justify-center rounded-xl border border-accent/20 bg-surface-overlay">
                <Bot class="h-7 w-7 text-accent" :stroke-width="1.5" />
              </div>
            </div>
            <div class="flex-1">
              <div class="flex items-center gap-3">
                <h1 class="text-gradient text-xl font-bold tracking-tight">{{ mcp.metadata.name }}</h1>
                <span
                  v-if="mcp.metadata.name === 'default'"
                  class="rounded-full border border-accent/20 bg-accent-subtle px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wider text-accent"
                >
                  global
                </span>
                <StatusBadge :status="readyCond?.status === 'True' ? 'Ready' : 'Pending'" />
              </div>
              <div class="mt-1.5 flex items-center gap-4 text-[12px] text-text-muted">
                <div class="flex items-center gap-1.5">
                  <component
                    :is="(mcp.status?.connectedEdges ?? 0) > 0 ? Wifi : WifiOff"
                    class="h-3 w-3"
                    :class="(mcp.status?.connectedEdges ?? 0) > 0 ? 'text-success' : 'text-danger'"
                    :stroke-width="1.75"
                  />
                  {{ mcp.status?.connectedEdges ?? 0 }} edges connected
                </div>
                <span class="font-mono text-[11px] text-text-muted/60">{{ edgeSelectorDisplay }}</span>
              </div>
            </div>
            <div class="flex items-center gap-2">
              <button
                class="glow-ring flex items-center gap-1.5 rounded-lg border border-border-subtle bg-surface-overlay/80 px-3 py-1.5 text-[11px] font-medium text-text-secondary transition-all hover:border-accent/30 hover:text-accent"
                @click="startEdit"
              >
                <Pencil class="h-3 w-3" :stroke-width="2" />
                Edit
              </button>
              <button
                v-if="mcp.metadata.name !== 'default'"
                class="flex items-center gap-1.5 rounded-lg border border-border-subtle bg-surface-overlay/80 px-3 py-1.5 text-[11px] font-medium text-text-secondary transition-all hover:border-danger/30 hover:bg-danger-subtle hover:text-danger"
                @click="handleDelete"
              >
                <Trash2 class="h-3 w-3" :stroke-width="2" />
                Delete
              </button>
            </div>
          </div>

          <!-- Details grid -->
          <dl class="mt-6 grid grid-cols-2 gap-x-8 gap-y-3 border-t border-border-subtle pt-5">
            <div v-for="item in details" :key="item.label" class="flex items-center justify-between">
              <dt class="flex items-center gap-2 text-[12px] text-text-muted">
                <component :is="item.icon" class="h-3 w-3" :stroke-width="1.75" />
                {{ item.label }}
              </dt>
              <dd
                class="text-[12px] text-text-secondary"
                :class="{ 'font-mono text-[11px] max-w-[240px] truncate': item.mono }"
              >
                {{ item.value }}
              </dd>
            </div>
          </dl>
        </div>

        <!-- Conditions + config snippets (right column) -->
        <div class="flex flex-col gap-4">
          <div
            class="stagger-item rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
            style="animation-delay: 120ms"
          >
            <h2 class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Conditions</h2>
            <div v-if="mcp.status?.conditions?.length" class="mt-4 space-y-2">
              <div
                v-for="cond in mcp.status.conditions"
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
      </div>

      <!-- Config snippets -->
      <div
        class="stagger-item mt-5 grid grid-cols-1 gap-4 lg:grid-cols-2"
        style="animation-delay: 160ms"
      >
        <div class="rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur">
          <div class="flex items-center justify-between mb-3">
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Claude Code</span>
            <button
              class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
              @click="copySnippet(buildClaudeCodeSnippet, 'code')"
            >
              <component :is="copiedField === 'code' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
              {{ copiedField === 'code' ? 'Copied' : 'Copy' }}
            </button>
          </div>
          <pre class="overflow-x-auto rounded-lg bg-surface-overlay/60 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ claudeCodeSnippet }}</pre>
        </div>
        <div class="rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur">
          <div class="flex items-center justify-between mb-3">
            <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Claude Desktop</span>
            <button
              class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
              @click="copySnippet(buildClaudeDesktopSnippet, 'desktop')"
            >
              <component :is="copiedField === 'desktop' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
              {{ copiedField === 'desktop' ? 'Copied' : 'Copy' }}
            </button>
          </div>
          <pre class="overflow-x-auto rounded-lg bg-surface-overlay/60 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ claudeDesktopSnippet }}</pre>
        </div>
      </div>

      <!-- YAML section -->
      <div class="stagger-item mt-5" style="animation-delay: 200ms">
        <button
          class="glow-ring flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-4 py-2 text-[12px] font-medium text-text-secondary backdrop-blur transition-all duration-150 hover:border-accent/30 hover:text-text-primary"
          @click="showYaml = !showYaml"
        >
          <FileCode class="h-3.5 w-3.5" :stroke-width="1.75" />
          {{ showYaml ? 'Hide' : 'Show' }} Resource JSON
          <component :is="showYaml ? ChevronUp : ChevronDown" class="h-3 w-3 text-text-muted" :stroke-width="1.75" />
        </button>
        <div v-if="showYaml" class="mt-3">
          <div class="border-beam rounded-2xl">
            <pre class="max-h-[500px] overflow-auto rounded-2xl border border-border-subtle bg-surface-overlay/60 p-5 font-mono text-[11px] leading-relaxed text-text-secondary backdrop-blur">{{ yamlContent }}</pre>
          </div>
        </div>
      </div>

      <!-- Edit modal -->
      <Teleport to="body">
        <div v-if="editing" class="fixed inset-0 z-[100] flex items-center justify-center bg-black/50 backdrop-blur-sm" @click.self="editing = false">
          <div class="w-full max-w-lg rounded-2xl border border-border-subtle bg-surface-raised p-6 shadow-2xl">
            <h2 class="text-lg font-bold text-text-primary mb-4">Edit MCP Server</h2>

            <div v-if="saveError" class="mb-4 rounded-lg border border-danger/20 bg-danger-subtle p-3 text-[12px] text-danger">
              {{ saveError }}
            </div>

            <div class="space-y-4">
              <div>
                <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Edge Selector (matchLabels)</label>
                <input
                  v-model="editMatchLabels"
                  type="text"
                  placeholder="env=prod, region=us-east (empty = all edges)"
                  class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
                />
                <p class="mt-1 text-[10px] text-text-muted">Comma-separated key=value pairs. Leave empty to match all edges.</p>
              </div>

              <div>
                <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Toolsets</label>
                <input
                  v-model="editToolsets"
                  type="text"
                  placeholder="core, config, helm (empty = all)"
                  class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
                />
                <p class="mt-1 text-[10px] text-text-muted">Available: core, config, helm, kcp, kiali, kubevirt. Leave empty for all.</p>
              </div>

              <div class="flex items-center gap-2">
                <input
                  v-model="editReadOnly"
                  type="checkbox"
                  id="edit-readonly"
                  class="h-4 w-4 rounded border-border-subtle accent-accent"
                />
                <label for="edit-readonly" class="text-[12px] text-text-secondary">Read-only mode</label>
              </div>
            </div>

            <div class="mt-6 flex items-center justify-end gap-3">
              <button
                class="rounded-lg border border-border-subtle px-4 py-2 text-[12px] font-medium text-text-secondary transition-all hover:bg-surface-hover"
                @click="editing = false"
                :disabled="saving"
              >
                Cancel
              </button>
              <button
                class="rounded-lg bg-accent px-4 py-2 text-[12px] font-medium text-white transition-all hover:bg-accent-hover disabled:opacity-50"
                @click="saveEdit"
                :disabled="saving"
              >
                {{ saving ? 'Saving...' : 'Save Changes' }}
              </button>
            </div>
          </div>
        </div>
      </Teleport>
    </template>
  </AppLayout>
</template>
