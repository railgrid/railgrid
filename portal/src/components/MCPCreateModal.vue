<script setup lang="ts">
import { ref } from 'vue'
import { graphqlMutate } from '@/composables/useGraphQL'
import { CREATE_MCP } from '@/graphql/mutations'
import { X } from 'lucide-vue-next'

const emit = defineEmits<{
  close: []
  created: []
}>()

const name = ref('')
const matchLabels = ref('')
const toolsets = ref('')
const readOnly = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)

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

    if (toolsets.value.trim()) {
      spec.toolsets = toolsets.value.split(',').map((s) => s.trim()).filter(Boolean)
    }

    if (readOnly.value) {
      spec.readOnly = true
    }

    await graphqlMutate(CREATE_MCP, {
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

          <div>
            <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Toolsets</label>
            <input
              v-model="toolsets"
              type="text"
              placeholder="core, config, helm (empty = all)"
              class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
            />
            <p class="mt-1 text-[10px] text-text-muted">Available toolsets: core, config, helm, kcp, kiali, kubevirt. Leave empty for all.</p>
          </div>

          <div class="flex items-center gap-2">
            <input
              v-model="readOnly"
              type="checkbox"
              id="create-readonly"
              class="h-4 w-4 rounded border-border-subtle accent-accent"
            />
            <label for="create-readonly" class="text-[12px] text-text-secondary">Read-only mode (disables write operations)</label>
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
