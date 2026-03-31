<script setup lang="ts">
import { ref } from 'vue'
import { createVirtualWorkload, type VirtualWorkloadCreateSpec } from '@/composables/useWorkloadAPI'
import { X, Plus, Trash2 } from 'lucide-vue-next'

const emit = defineEmits<{
  close: []
  created: []
}>()

const name = ref('')
const namespace = ref('default')
const image = ref('')
const replicas = ref(1)
const containerPort = ref<string>('')
const strategy = ref<'Singleton' | 'Spread'>('Singleton')
const edgeSelectorLabels = ref('')
const envVars = ref<Array<{ name: string; value: string }>>([])
const showAdvanced = ref(false)
const expose = ref(false)
const dnsName = ref('')
const accessPort = ref<string>('')
const saving = ref(false)
const error = ref<string | null>(null)

function addEnvVar() {
  envVars.value.push({ name: '', value: '' })
}

function removeEnvVar(index: number) {
  envVars.value.splice(index, 1)
}

async function handleCreate() {
  if (!name.value.trim()) {
    error.value = 'Name is required'
    return
  }
  if (!image.value.trim()) {
    error.value = 'Container image is required'
    return
  }

  saving.value = true
  error.value = null

  try {
    // Parse edge selector labels
    const edgeSelector: Record<string, string> = {}
    if (edgeSelectorLabels.value.trim()) {
      for (const pair of edgeSelectorLabels.value.split(',')) {
        const [k, v] = pair.split('=').map((s) => s.trim())
        if (k) edgeSelector[k] = v ?? ''
      }
    }

    const spec: VirtualWorkloadCreateSpec = {
      name: name.value.trim(),
      namespace: namespace.value,
      image: image.value.trim(),
      replicas: replicas.value,
      strategy: strategy.value,
      ...(containerPort.value ? { containerPort: parseInt(containerPort.value) } : {}),
      ...(Object.keys(edgeSelector).length > 0 ? { edgeSelector } : {}),
      ...(envVars.value.length > 0
        ? { env: envVars.value.filter((e) => e.name.trim()) }
        : {}),
      ...(expose.value ? { expose: true } : {}),
      ...(dnsName.value ? { dnsName: dnsName.value } : {}),
      ...(accessPort.value ? { accessPort: parseInt(accessPort.value) } : {}),
    }

    await createVirtualWorkload(spec)
    emit('created')
    emit('close')
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
      <div class="w-full max-w-2xl max-h-[90vh] overflow-y-auto rounded-2xl border border-border-subtle bg-surface-raised p-6 shadow-2xl">
        <div class="flex items-center justify-between mb-4">
          <h2 class="text-lg font-bold text-text-primary">Create Virtual Workload</h2>
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
          <!-- Name -->
          <div>
            <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Name</label>
            <input
              v-model="name"
              type="text"
              placeholder="nginx-demo"
              class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
              autofocus
            />
          </div>

          <!-- Namespace -->
          <div>
            <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Namespace</label>
            <input
              v-model="namespace"
              type="text"
              placeholder="default"
              class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
            />
          </div>

          <!-- Image -->
          <div>
            <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Container Image</label>
            <input
              v-model="image"
              type="text"
              placeholder="nginx:latest"
              class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
            />
            <p class="mt-1 text-[10px] text-text-muted">Full image reference, e.g. nginx:latest, ghcr.io/org/app:v1.0</p>
          </div>

          <!-- Replicas & Port -->
          <div class="grid grid-cols-2 gap-4">
            <div>
              <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Replicas</label>
              <input
                v-model.number="replicas"
                type="number"
                min="0"
                max="100"
                class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary focus:border-accent/50 focus:outline-none"
              />
            </div>
            <div>
              <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Container Port</label>
              <input
                v-model="containerPort"
                type="text"
                placeholder="80 (optional)"
                class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
              />
            </div>
          </div>

          <!-- Placement -->
          <div>
            <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Placement Strategy</label>
            <select
              v-model="strategy"
              class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 text-[12px] text-text-primary focus:border-accent/50 focus:outline-none"
            >
              <option value="Singleton">Singleton (one edge)</option>
              <option value="Spread">Spread (all matching edges)</option>
            </select>
          </div>

          <!-- Edge selector -->
          <div>
            <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Edge Selector Labels</label>
            <input
              v-model="edgeSelectorLabels"
              type="text"
              placeholder="env=dev, region=us-east (optional)"
              class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
            />
            <p class="mt-1 text-[10px] text-text-muted">Comma-separated key=value pairs to select target edges. Leave empty for all edges.</p>
          </div>

          <!-- Advanced toggle -->
          <button
            class="flex items-center gap-1.5 text-[11px] font-medium text-accent transition-colors hover:text-accent-hover"
            @click="showAdvanced = !showAdvanced"
          >
            {{ showAdvanced ? 'Hide' : 'Show' }} advanced options
          </button>

          <template v-if="showAdvanced">
            <!-- Access -->
            <div>
              <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-2">Access / Exposure</label>
              <div class="space-y-3">
                <label class="flex items-center gap-2 text-[12px] text-text-secondary cursor-pointer">
                  <input
                    v-model="expose"
                    type="checkbox"
                    class="h-3.5 w-3.5 rounded border-border-subtle bg-surface-overlay accent-accent"
                  />
                  Expose externally
                </label>
                <div v-if="expose" class="grid grid-cols-2 gap-4">
                  <div>
                    <input
                      v-model="dnsName"
                      type="text"
                      placeholder="DNS name (optional)"
                      class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
                    />
                  </div>
                  <div>
                    <input
                      v-model="accessPort"
                      type="text"
                      placeholder="Access port (optional)"
                      class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
                    />
                  </div>
                </div>
              </div>
            </div>

            <!-- Env vars -->
            <div>
              <div class="flex items-center justify-between mb-2">
                <label class="text-[11px] font-semibold uppercase tracking-wider text-text-muted">Environment Variables</label>
                <button
                  class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] font-medium text-accent transition-all hover:bg-accent/10"
                  @click="addEnvVar"
                >
                  <Plus class="h-3 w-3" :stroke-width="2" />
                  Add
                </button>
              </div>
              <div class="space-y-2">
                <div v-for="(env, i) in envVars" :key="i" class="flex items-center gap-2">
                  <input
                    v-model="env.name"
                    type="text"
                    placeholder="KEY"
                    class="flex-1 rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
                  />
                  <input
                    v-model="env.value"
                    type="text"
                    placeholder="value"
                    class="flex-1 rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
                  />
                  <button
                    class="flex h-8 w-8 items-center justify-center rounded-lg text-text-muted transition-all hover:bg-danger-subtle hover:text-danger"
                    @click="removeEnvVar(i)"
                  >
                    <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" />
                  </button>
                </div>
                <p v-if="envVars.length === 0" class="text-[10px] text-text-muted">No environment variables configured.</p>
              </div>
            </div>
          </template>
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
