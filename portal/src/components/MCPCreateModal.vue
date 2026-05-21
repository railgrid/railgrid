<script setup lang="ts">
import { computed, ref } from 'vue'
import { graphqlMutate } from '@/composables/useGraphQL'
import { useEscapeKey } from '@/composables/useEscapeKey'
import { CREATE_MCP, CREATE_LINUX_MCP, CREATE_AGGREGATE_MCP } from '@/graphql/mutations'
import type { MCPKind } from '@/graphql/queries/mcp'
import { X } from 'lucide-vue-next'

const emit = defineEmits<{
  close: []
  created: []
}>()

useEscapeKey(() => emit('close'))

const kind = ref<MCPKind>('aggregate')
const name = ref('')
const matchLabels = ref('')
const toolsets = ref('')        // kube or linux toolsets (per-kind CRDs)
const kubeToolsets = ref('')    // aggregate-only
const linuxToolsets = ref('')   // aggregate-only
const readOnly = ref(false)
const displayName = ref('')
const instructions = ref('')
const saving = ref(false)
const error = ref<string | null>(null)

const toolsetHint = computed(() => {
  switch (kind.value) {
    case 'linux':
      return 'Available toolsets: core, systemd, diag, net, pkg. Empty = core only.'
    case 'aggregate':
      return 'Two lists (kube + linux). Empty kube = upstream defaults; empty linux = core only.'
    default:
      return 'Available toolsets: core, config, helm, kcp, kiali, kubevirt. Empty = all.'
  }
})

const edgeTypeNote = computed(() => {
  switch (kind.value) {
    case 'linux':
      return 'Selector matches server-type edges only (SSH transport).'
    case 'aggregate':
      return 'Selector matches BOTH kubernetes-type and server-type edges; kube tools route to kube edges, linux tools to server edges.'
    default:
      return 'Selector matches kubernetes-type edges only.'
  }
})

// Comma-separated list → []string, dropping empties.
function parseList(s: string): string[] {
  return s.split(',').map((x) => x.trim()).filter(Boolean)
}

async function handleCreate() {
  if (!name.value.trim()) {
    error.value = 'Name is required'
    return
  }

  saving.value = true
  error.value = null
  try {
    const spec: Record<string, unknown> = {}

    if (matchLabels.value.trim()) {
      spec.edgeSelector = {
        matchLabels: Object.fromEntries(
          matchLabels.value.split(',').map((pair) => {
            const [k, v] = pair.split('=').map((s) => s.trim())
            return [k, v ?? '']
          }),
        ),
      }
    }

    if (kind.value === 'aggregate') {
      const kt = parseList(kubeToolsets.value)
      const lt = parseList(linuxToolsets.value)
      if (kt.length > 0) spec.kubernetesToolsets = kt
      if (lt.length > 0) spec.linuxToolsets = lt
    } else if (toolsets.value.trim()) {
      spec.toolsets = parseList(toolsets.value)
    }

    if (readOnly.value) {
      spec.readOnly = true
    }

    // Metadata overrides — only sent when non-empty so the hub's auto-gen
    // defaults take effect for unconfigured CRs.
    if (displayName.value.trim()) {
      spec.displayName = displayName.value.trim()
    }
    if (instructions.value.trim()) {
      spec.instructions = instructions.value.trim()
    }

    const mutation =
      kind.value === 'aggregate'
        ? CREATE_AGGREGATE_MCP
        : kind.value === 'linux'
        ? CREATE_LINUX_MCP
        : CREATE_MCP
    await graphqlMutate(mutation, {
      object: {
        metadata: { name: name.value.trim() },
        spec,
      },
    })
    emit('created')
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Create failed'
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <div
      class="fixed inset-0 z-[100] flex items-center justify-center bg-black/50 backdrop-blur-sm"
      @click.self="emit('close')"
    >
      <div class="w-full max-w-lg rounded-2xl border border-border-subtle bg-surface-raised p-6 shadow-2xl">
        <div class="flex items-center justify-between mb-4">
          <h2 class="text-lg font-bold text-text-primary">Create MCP Server</h2>
          <button
            class="flex h-7 w-7 items-center justify-center rounded-lg text-text-muted transition-all hover:bg-surface-hover hover:text-text-primary"
            @click="emit('close')"
          >
            <X class="h-4 w-4" :stroke-width="2" />
          </button>
        </div>

        <div v-if="error" class="mb-4 rounded-lg border border-danger/20 bg-danger-subtle p-3 text-[12px] text-danger">
          {{ error }}
        </div>

        <div class="space-y-4">
          <div>
            <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Kind</label>
            <div class="grid grid-cols-3 gap-2">
              <button
                type="button"
                class="rounded-lg border px-3 py-2 text-[12px] font-medium transition-all"
                :class="kind === 'aggregate'
                  ? 'border-success/50 bg-success/10 text-success'
                  : 'border-border-subtle bg-surface-overlay text-text-secondary hover:bg-surface-hover'"
                @click="kind = 'aggregate'"
              >
                Aggregate
                <span class="block text-[9px] font-normal opacity-70">kube + linux</span>
              </button>
              <button
                type="button"
                class="rounded-lg border px-3 py-2 text-[12px] font-medium transition-all"
                :class="kind === 'kubernetes'
                  ? 'border-accent/50 bg-accent/10 text-accent'
                  : 'border-border-subtle bg-surface-overlay text-text-secondary hover:bg-surface-hover'"
                @click="kind = 'kubernetes'"
              >
                Kubernetes
              </button>
              <button
                type="button"
                class="rounded-lg border px-3 py-2 text-[12px] font-medium transition-all"
                :class="kind === 'linux'
                  ? 'border-warning/50 bg-warning/10 text-warning'
                  : 'border-border-subtle bg-surface-overlay text-text-secondary hover:bg-surface-hover'"
                @click="kind = 'linux'"
              >
                Linux (SSH)
              </button>
            </div>
            <p class="mt-1 text-[10px] text-text-muted">{{ edgeTypeNote }}</p>
          </div>

          <div>
            <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Name</label>
            <input
              v-model="name"
              type="text"
              placeholder="my-mcp-server"
              class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
              autofocus
            />
            <p class="mt-1 text-[10px] text-text-muted">Unique name for this MCP server configuration.</p>
          </div>

          <div>
            <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Edge Selector (matchLabels)</label>
            <input
              v-model="matchLabels"
              type="text"
              placeholder="env=prod, region=us-east (empty = all edges)"
              class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
            />
            <p class="mt-1 text-[10px] text-text-muted">Comma-separated key=value pairs to select edges by labels. Leave empty to match all connected edges.</p>
          </div>

          <!-- Per-kind CRDs: single toolsets input. -->
          <div v-if="kind !== 'aggregate'">
            <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Toolsets</label>
            <input
              v-model="toolsets"
              type="text"
              :placeholder="kind === 'linux' ? 'core, systemd, diag (empty = core)' : 'core, config, helm (empty = all)'"
              class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
            />
            <p class="mt-1 text-[10px] text-text-muted">{{ toolsetHint }}</p>
          </div>

          <!-- Aggregate: two separate toolset lists since each maps onto a
               different upstream toolset registry. -->
          <template v-else>
            <div>
              <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Kubernetes Toolsets</label>
              <input
                v-model="kubeToolsets"
                type="text"
                placeholder="core, config, helm (empty = upstream defaults)"
                class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
              />
              <p class="mt-1 text-[10px] text-text-muted">Available: core, config, helm, kcp, kiali, kubevirt.</p>
            </div>
            <div>
              <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Linux Toolsets</label>
              <input
                v-model="linuxToolsets"
                type="text"
                placeholder="core, systemd, diag (empty = core only)"
                class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
              />
              <p class="mt-1 text-[10px] text-text-muted">Available: core, systemd, diag, net, pkg.</p>
            </div>
          </template>

          <div class="flex items-center gap-2">
            <input
              v-model="readOnly"
              type="checkbox"
              id="create-readonly"
              class="h-4 w-4 rounded border-border-subtle accent-accent"
            />
            <label for="create-readonly" class="text-[12px] text-text-secondary">Read-only mode (disables write operations)</label>
          </div>

          <!-- Optional MCP metadata: surfaced to AI clients on `initialize`. -->
          <div>
            <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Display Name (optional)</label>
            <input
              v-model="displayName"
              type="text"
              placeholder="Kedge — Production (auto-generated if empty)"
              class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
            />
            <p class="mt-1 text-[10px] text-text-muted">Shown in Claude Desktop / Cursor server pickers.</p>
          </div>

          <div>
            <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">LLM Instructions (optional)</label>
            <textarea
              v-model="instructions"
              rows="4"
              placeholder="Add per-environment guidance forwarded to the LLM on initialize — e.g. &quot;This is production. Ask before any destructive operation.&quot;"
              class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[11px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none resize-y"
            ></textarea>
            <p class="mt-1 text-[10px] text-text-muted">Empty = the hub auto-generates a generic explanation of the endpoint.</p>
          </div>
        </div>

        <div class="mt-6 flex items-center justify-end gap-3">
          <button
            class="rounded-lg border border-border-subtle px-4 py-2 text-[12px] font-medium text-text-secondary transition-all hover:bg-surface-hover"
            @click="emit('close')"
            :disabled="saving"
          >
            Cancel
          </button>
          <button
            class="rounded-lg bg-accent px-4 py-2 text-[12px] font-medium text-white transition-all hover:bg-accent-hover disabled:opacity-50"
            @click="handleCreate"
            :disabled="saving"
          >
            {{ saving ? 'Creating...' : 'Create' }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
