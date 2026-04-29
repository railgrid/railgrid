<script setup lang="ts">
import { ref, computed } from 'vue'
import { graphqlMutate } from '@/composables/useGraphQL'
import { useAuthStore } from '@/stores/auth'
import { CREATE_EDGE } from '@/graphql/mutations'
import { GET_EDGE } from '@/graphql/queries/edges'
import { createGraphQLClient } from '@/graphql/client'
import { X, Copy, Check } from 'lucide-vue-next'

const emit = defineEmits<{
  close: []
  created: []
}>()

const name = ref('')
const edgeType = ref('kubernetes')
const labels = ref('')
const saving = ref(false)
const error = ref<string | null>(null)

// Post-creation state
const joinToken = ref<string | null>(null)
const tokenError = ref<string | null>(null)
const copiedField = ref<string | null>(null)

const auth = useAuthStore()
const created = computed(() => joinToken.value !== null || tokenError.value !== null)

// Hub URL includes the kcp cluster path so the agent knows which workspace to connect to
const hubURL = computed(() => {
  const origin = window.location.origin
  const cluster = auth.clusterName
  return cluster ? `${origin}/clusters/${cluster}` : origin
})

function buildHelmSnippet(token: string) {
  return `helm install kedge-agent oci://ghcr.io/faroshq/charts/kedge-agent \\
  --namespace kedge-agent --create-namespace \\
  --set agent.edgeName=${name.value} \\
  --set agent.hub.url=${hubURL.value} \\
  --set agent.hub.token=${token}`
}

function buildCLIJoinSnippet(token: string) {
  return `kedge agent join \\
  --hub-url ${hubURL.value} \\
  --edge-name ${name.value} \\
  --type ${edgeType.value} \\
  --token ${token}`
}

function buildCLIRunSnippet(token: string) {
  return `kedge agent run \\
  --hub-url ${hubURL.value} \\
  --edge-name ${name.value} \\
  --type ${edgeType.value} \\
  --token ${token}`
}

const maskedToken = '••••••••••••••••'

const helmSnippet = computed(() => buildHelmSnippet(maskedToken))
const cliJoinSnippet = computed(() => buildCLIJoinSnippet(maskedToken))
const cliRunSnippet = computed(() => buildCLIRunSnippet(maskedToken))

async function copySnippet(builder: (token: string) => string, field: string) {
  if (!joinToken.value) return
  try {
    await navigator.clipboard.writeText(builder(joinToken.value))
    copiedField.value = field
    setTimeout(() => (copiedField.value = null), 2000)
  } catch {}
}

async function handleCreate() {
  if (!name.value.trim()) {
    error.value = 'Name is required'
    return
  }

  saving.value = true
  error.value = null
  try {
    const parsedLabels: Record<string, string> = {}
    if (labels.value.trim()) {
      for (const pair of labels.value.split(',')) {
        const [k, v] = pair.split('=').map((s) => s.trim())
        if (k) parsedLabels[k] = v ?? ''
      }
    }

    const object: Record<string, unknown> = {
      metadata: {
        name: name.value.trim(),
        ...(Object.keys(parsedLabels).length > 0 ? { labels: parsedLabels } : {}),
      },
      spec: { type: edgeType.value },
    }

    await graphqlMutate(CREATE_EDGE, { object })
    emit('created')

    // Poll for join token via GraphQL
    const edgeName = name.value.trim()
    const deadline = Date.now() + 30000
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 1000))
      try {
        const client = createGraphQLClient(auth.clusterName!, () => auth.getValidToken())
        const result = await client.query(GET_EDGE, { name: edgeName }).toPromise()
        const token = result.data?.kedge_faros_sh?.v1alpha1?.Edge?.status?.joinToken
        if (token) {
          joinToken.value = token
          return
        }
      } catch {
        // keep polling
      }
    }
    tokenError.value = 'Could not retrieve join token. Run: kedge edge join-command ' + edgeName
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
          <h2 class="text-lg font-bold text-text-primary">{{ created ? 'Edge Created' : 'Create Edge' }}</h2>
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

        <!-- Creation form -->
        <template v-if="!created">
          <div class="space-y-4">
            <div>
              <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Name</label>
              <input
                v-model="name"
                type="text"
                placeholder="my-edge"
                class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
                autofocus
              />
            </div>

            <div>
              <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Type</label>
              <select
                v-model="edgeType"
                class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 text-[12px] text-text-primary focus:border-accent/50 focus:outline-none"
              >
                <option value="kubernetes">Kubernetes</option>
                <option value="server">Server (SSH)</option>
              </select>
              <p class="mt-1 text-[10px] text-text-muted">Kubernetes cluster or bare-metal/VM server.</p>
            </div>

            <div>
              <label class="block text-[11px] font-semibold uppercase tracking-wider text-text-muted mb-1">Labels</label>
              <input
                v-model="labels"
                type="text"
                placeholder="env=prod, region=us-east (optional)"
                class="w-full rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/50 focus:outline-none"
              />
              <p class="mt-1 text-[10px] text-text-muted">Comma-separated key=value pairs for MCP edge selectors.</p>
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
        </template>

        <!-- Join instructions (post-creation) -->
        <template v-else>
          <div v-if="tokenError" class="mb-4 rounded-lg border border-warning/20 bg-warning/5 p-3 text-[12px] text-warning">
            {{ tokenError }}
          </div>

          <template v-if="joinToken">
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

          <div v-else-if="!tokenError" class="flex items-center gap-2 text-[12px] text-text-muted">
            <div class="shimmer h-4 w-4 rounded" />
            Waiting for join token...
          </div>

          <div class="mt-6 flex items-center justify-end">
            <button
              class="rounded-lg bg-accent px-4 py-2 text-[12px] font-medium text-white transition-all hover:bg-accent-hover"
              @click="emit('close')"
            >
              Done
            </button>
          </div>
        </template>
      </div>
    </div>
  </Teleport>
</template>
