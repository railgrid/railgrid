<script setup lang="ts">
import { computed, ref } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { useGraphQLQuery } from '@/composables/useGraphQL'
import { useAuthStore } from '@/stores/auth'
import { GET_EDGE, GET_EDGE_YAML, type GetEdgeResult, type GetEdgeYamlResult } from '@/graphql/queries/edges'
import { Server, Wifi, WifiOff, Clock, Hash, FileCode, ChevronDown, ChevronUp, ArrowLeft, TerminalSquare, Copy, Check } from 'lucide-vue-next'

const props = defineProps<{ name: string }>()
const auth = useAuthStore()

const { data, loading, error } = useGraphQLQuery<GetEdgeResult>(
  GET_EDGE,
  { name: props.name },
  10000,
)

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

// --- Join instructions ---
const showJoinInstructions = ref(false)
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

async function copySnippet(builder: (token: string) => string, field: string) {
  if (!joinToken.value) return
  try {
    await navigator.clipboard.writeText(builder(joinToken.value))
    copiedField.value = field
    setTimeout(() => (copiedField.value = null), 2000)
  } catch {}
}

const details = computed(() => {
  if (!edge.value) return []
  return [
    { label: 'Type', value: edge.value.spec?.type, icon: Server },
    { label: 'Hostname', value: edge.value.status?.hostname || '-', icon: Server },
    { label: 'Agent Version', value: edge.value.status?.agentVersion || '-', icon: Hash },
    { label: 'Created', value: edge.value.metadata?.creationTimestamp, icon: Clock },
    { label: 'UID', value: edge.value.metadata?.uid, icon: Hash, mono: true },
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
              <dd
                class="text-[12px] text-text-secondary"
                :class="{ 'font-mono text-[11px]': item.mono, 'max-w-[160px] truncate': item.mono }"
              >
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

      <!-- Actions -->
      <div class="stagger-item mt-5 flex items-center gap-3" style="animation-delay: 200ms">
        <!-- SSH Terminal button -->
        <router-link
          v-if="canSSH"
          :to="`/edges/${props.name}/terminal`"
          class="glow-ring flex items-center gap-2 rounded-xl border border-accent/30 bg-accent/10 px-4 py-2 text-[12px] font-medium text-accent backdrop-blur transition-all duration-150 hover:bg-accent/20 hover:shadow-lg hover:shadow-accent/10"
        >
          <TerminalSquare class="h-3.5 w-3.5" :stroke-width="1.75" />
          SSH Terminal
        </router-link>

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
      </div>

      <!-- Join Instructions -->
      <div v-if="showJoinInstructions" class="stagger-item mt-4" style="animation-delay: 220ms">
        <div v-if="joinTokenError" class="rounded-xl border border-warning/20 bg-warning/5 p-4 text-[12px] text-warning">
          {{ joinTokenError }}
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
          <pre class="max-h-[500px] overflow-auto rounded-2xl border border-border-subtle bg-surface-overlay/60 p-5 font-mono text-[11px] leading-relaxed text-text-secondary backdrop-blur">{{ yaml }}</pre>
        </div>
      </div>
    </template>
  </AppLayout>
</template>
