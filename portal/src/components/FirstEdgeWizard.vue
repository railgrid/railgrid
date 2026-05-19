<script setup lang="ts">
import { ref, computed, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { graphqlMutate } from '@/composables/useGraphQL'
import { useAuthStore } from '@/stores/auth'
import { CREATE_EDGE } from '@/graphql/mutations'
import { GET_EDGE, type GetEdgeResult } from '@/graphql/queries/edges'
import { createGraphQLClient } from '@/graphql/client'
import {
  Hexagon,
  ArrowRight,
  Copy,
  Check,
  CheckCircle2,
  Server,
  Loader2,
  Sparkles,
  AlertCircle,
  PartyPopper,
} from 'lucide-vue-next'

const router = useRouter()
const auth = useAuthStore()

const emit = defineEmits<{
  connected: [name: string]
}>()

type Step = 1 | 2 | 3

const step = ref<Step>(1)
const name = ref('')
const edgeType = ref<'kubernetes' | 'server'>('kubernetes')
const labels = ref('')
const saving = ref(false)
const error = ref<string | null>(null)

// Post-creation
const joinToken = ref<string | null>(null)
const tokenError = ref<string | null>(null)
const copiedField = ref<string | null>(null)

// Connection waiting
const agentVersion = ref<string | null>(null)
const elapsed = ref(0)

let pollTimer: ReturnType<typeof setInterval> | null = null
let elapsedTimer: ReturnType<typeof setInterval> | null = null

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
  if (elapsedTimer) clearInterval(elapsedTimer)
})

const hubURL = computed(() => {
  const origin = window.location.origin
  const cluster = auth.clusterName
  return cluster ? `${origin}/clusters/${cluster}` : origin
})

const trimmedName = computed(() => name.value.trim())
const canContinue = computed(() => trimmedName.value.length > 0 && !saving.value)

function buildHelmSnippet(token: string) {
  return `helm install kedge-agent oci://ghcr.io/faroshq/charts/kedge-agent \\
  --namespace kedge-agent --create-namespace \\
  --set agent.edgeName=${trimmedName.value} \\
  --set agent.hub.url=${hubURL.value} \\
  --set agent.hub.token=${token}`
}

function buildCLIJoinSnippet(token: string) {
  return `kedge agent join \\
  --hub-url ${hubURL.value} \\
  --edge-name ${trimmedName.value} \\
  --type ${edgeType.value} \\
  --token ${token}`
}

const helmSnippet = computed(() =>
  joinToken.value ? buildHelmSnippet(joinToken.value) : buildHelmSnippet('••••••••••••••••'),
)
const cliJoinSnippet = computed(() =>
  joinToken.value ? buildCLIJoinSnippet(joinToken.value) : buildCLIJoinSnippet('••••••••••••••••'),
)

async function copyText(text: string, field: string) {
  try {
    await navigator.clipboard.writeText(text)
    copiedField.value = field
    setTimeout(() => (copiedField.value = null), 2000)
  } catch {}
}

async function handleCreate() {
  if (!trimmedName.value) {
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
        name: trimmedName.value,
        ...(Object.keys(parsedLabels).length > 0 ? { labels: parsedLabels } : {}),
      },
      spec: { type: edgeType.value },
    }

    await graphqlMutate(CREATE_EDGE, { object })

    // Advance to step 2 immediately; poll for token + connection in background
    step.value = 2
    startPolling()
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Create failed'
  } finally {
    saving.value = false
  }
}

function startPolling() {
  const edgeName = trimmedName.value
  const tokenDeadline = Date.now() + 30000

  elapsed.value = 0
  elapsedTimer = setInterval(() => {
    elapsed.value += 1
  }, 1000)

  pollTimer = setInterval(async () => {
    try {
      const client = createGraphQLClient(auth.clusterName!, () => auth.getValidToken())
      const result = await client.query(GET_EDGE, { name: edgeName }).toPromise()
      const edge = (result.data as GetEdgeResult | undefined)?.kedge_faros_sh?.v1alpha1?.Edge
      if (!edge) return

      if (!joinToken.value && edge.status?.joinToken) {
        joinToken.value = edge.status.joinToken
      }
      if (!joinToken.value && Date.now() > tokenDeadline) {
        tokenError.value = `Could not retrieve join token. Run: kedge edge join-command ${edgeName}`
      }

      if (edge.status?.connected) {
        agentVersion.value = edge.status.agentVersion ?? null
        if (pollTimer) {
          clearInterval(pollTimer)
          pollTimer = null
        }
        if (elapsedTimer) {
          clearInterval(elapsedTimer)
          elapsedTimer = null
        }
        step.value = 3
        emit('connected', edgeName)
      }
    } catch {
      // ignore transient errors; keep polling
    }
  }, 2500)
}

function formatElapsed(s: number) {
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const r = s % 60
  return `${m}m ${r}s`
}

function viewEdge() {
  router.push(`/edges/${trimmedName.value}`)
}

function finish() {
  emit('connected', trimmedName.value)
}
</script>

<template>
  <div class="mx-auto w-full max-w-3xl">
    <!-- Hero header -->
    <div class="mb-6 flex items-start gap-4">
      <div class="relative flex h-12 w-12 shrink-0 items-center justify-center">
        <div class="absolute inset-0 rounded-xl bg-accent/20 blur-md" />
        <div class="relative flex h-12 w-12 items-center justify-center rounded-xl border border-accent/25 bg-surface-overlay">
          <Hexagon class="h-6 w-6 text-accent" :stroke-width="1.5" />
        </div>
      </div>
      <div class="flex-1">
        <h1 class="text-[18px] font-bold text-text-primary flex items-center gap-2">
          Welcome to Kedge
          <Sparkles class="h-4 w-4 text-accent" :stroke-width="1.75" />
        </h1>
        <p class="mt-1 text-[12px] text-text-muted">
          Let's connect your first edge — a Kubernetes cluster or server you want to manage from this hub.
        </p>
      </div>
    </div>

    <!-- Progress steps -->
    <div class="mb-6 flex items-center gap-2">
      <div
        v-for="(label, idx) in ['Configure', 'Install agent', 'Connected']"
        :key="label"
        class="flex flex-1 items-center gap-2"
      >
        <div
          class="flex h-6 w-6 shrink-0 items-center justify-center rounded-full border text-[10px] font-bold tabular-nums transition-all duration-300"
          :class="
            step > (idx + 1)
              ? 'border-success bg-success/15 text-success'
              : step === (idx + 1)
              ? 'border-accent bg-accent text-white'
              : 'border-border-default bg-surface-overlay text-text-muted'
          "
        >
          <CheckCircle2 v-if="step > (idx + 1)" class="h-3.5 w-3.5" :stroke-width="2" />
          <span v-else>{{ idx + 1 }}</span>
        </div>
        <span
          class="text-[11px] font-semibold uppercase tracking-[0.15em] transition-colors"
          :class="step >= (idx + 1) ? 'text-text-primary' : 'text-text-muted'"
        >
          {{ label }}
        </span>
        <div
          v-if="idx < 2"
          class="ml-1 h-px flex-1 transition-colors"
          :class="step > (idx + 1) ? 'bg-success/50' : 'bg-border-subtle'"
        />
      </div>
    </div>

    <!-- Body -->
    <div class="border-beam rounded-2xl">
      <div class="space-y-5 rounded-2xl border border-border-subtle bg-surface-raised/80 p-6 backdrop-blur">
        <!-- Error banner -->
        <div
          v-if="error"
          class="flex items-center gap-2 rounded-xl border border-danger/20 bg-danger-subtle p-3 text-[12px] text-danger"
        >
          <AlertCircle class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
          {{ error }}
        </div>

        <!-- Step 1: Configure -->
        <template v-if="step === 1">
          <div class="space-y-4">
            <div>
              <label class="mb-1 block text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">
                Edge name
              </label>
              <input
                v-model="name"
                type="text"
                placeholder="e.g. prod-us-east-1"
                class="w-full rounded-xl border border-border-default bg-surface-overlay/60 px-3 py-2.5 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/40 focus:outline-none"
                autofocus
                @keyup.enter="canContinue && handleCreate()"
              />
              <p class="mt-1 text-[10px] text-text-muted">
                Lowercase letters, digits and dashes. This is how the edge will appear across the platform.
              </p>
            </div>

            <div>
              <label class="mb-1 block text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">
                Edge type
              </label>
              <div class="grid grid-cols-2 gap-2">
                <button
                  type="button"
                  class="rounded-xl border px-3 py-3 text-left transition-all"
                  :class="
                    edgeType === 'kubernetes'
                      ? 'border-accent/40 bg-accent/5'
                      : 'border-border-subtle bg-surface-overlay/40 hover:bg-surface-hover'
                  "
                  @click="edgeType = 'kubernetes'"
                >
                  <div class="text-[12px] font-semibold text-text-primary">Kubernetes</div>
                  <div class="mt-0.5 text-[10px] text-text-muted">Existing K8s cluster (Helm-installable)</div>
                </button>
                <button
                  type="button"
                  class="rounded-xl border px-3 py-3 text-left transition-all"
                  :class="
                    edgeType === 'server'
                      ? 'border-accent/40 bg-accent/5'
                      : 'border-border-subtle bg-surface-overlay/40 hover:bg-surface-hover'
                  "
                  @click="edgeType = 'server'"
                >
                  <div class="text-[12px] font-semibold text-text-primary">Server</div>
                  <div class="mt-0.5 text-[10px] text-text-muted">Bare-metal or VM (SSH-accessible)</div>
                </button>
              </div>
            </div>

            <div>
              <label class="mb-1 block text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">
                Labels <span class="text-text-muted/60">(optional)</span>
              </label>
              <input
                v-model="labels"
                type="text"
                placeholder="env=prod, region=us-east"
                class="w-full rounded-xl border border-border-default bg-surface-overlay/60 px-3 py-2.5 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/40 focus:outline-none"
              />
              <p class="mt-1 text-[10px] text-text-muted">
                Comma-separated key=value pairs. Used later for MCP edge selectors.
              </p>
            </div>
          </div>

          <div class="flex items-center justify-end gap-3 pt-2">
            <button
              type="button"
              class="group flex items-center gap-2 rounded-xl bg-accent px-4 py-2.5 text-[12px] font-semibold text-white transition-all hover:bg-accent-hover hover:shadow-lg hover:shadow-accent/20 active:scale-[0.98] disabled:pointer-events-none disabled:opacity-40"
              :disabled="!canContinue"
              @click="handleCreate"
            >
              <Loader2 v-if="saving" class="h-3.5 w-3.5 animate-spin" :stroke-width="2" />
              <span>{{ saving ? 'Creating...' : 'Create & continue' }}</span>
              <ArrowRight v-if="!saving" class="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" :stroke-width="2" />
            </button>
          </div>
        </template>

        <!-- Step 2: Install agent & wait -->
        <template v-else-if="step === 2">
          <div>
            <h3 class="text-[13px] font-semibold text-text-primary">
              Install the agent on your {{ edgeType === 'kubernetes' ? 'cluster' : 'server' }}
            </h3>
            <p class="mt-1 text-[11px] text-text-muted">
              Run one of the commands below from the {{ edgeType === 'kubernetes' ? 'cluster you want to register' : 'server you want to register' }}.
              This page will update automatically when <span class="font-mono text-text-secondary">{{ trimmedName }}</span> connects.
            </p>
          </div>

          <div v-if="tokenError" class="rounded-xl border border-warning/20 bg-warning/5 p-3 text-[11px] text-warning">
            {{ tokenError }}
          </div>

          <div v-else-if="!joinToken" class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-overlay/40 p-3 text-[11px] text-text-muted">
            <Loader2 class="h-3.5 w-3.5 animate-spin text-accent" :stroke-width="2" />
            Generating join token…
          </div>

          <div v-if="joinToken || tokenError" class="space-y-3">
            <!-- Helm (kubernetes only) -->
            <div v-if="edgeType === 'kubernetes'" class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4">
              <div class="mb-2 flex items-center justify-between">
                <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">
                  Option A — Helm (recommended)
                </span>
                <button
                  type="button"
                  class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent disabled:opacity-40"
                  :disabled="!joinToken"
                  @click="copyText(helmSnippet, 'helm')"
                >
                  <component :is="copiedField === 'helm' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                  {{ copiedField === 'helm' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ helmSnippet }}</pre>
            </div>

            <!-- CLI -->
            <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-4">
              <div class="mb-2 flex items-center justify-between">
                <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">
                  {{ edgeType === 'kubernetes' ? 'Option B' : 'CLI' }} — kedge agent join
                </span>
                <button
                  type="button"
                  class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent disabled:opacity-40"
                  :disabled="!joinToken"
                  @click="copyText(cliJoinSnippet, 'cli')"
                >
                  <component :is="copiedField === 'cli' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                  {{ copiedField === 'cli' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ cliJoinSnippet }}</pre>
            </div>
          </div>

          <!-- Live waiting indicator -->
          <div class="flex items-center gap-3 rounded-xl border border-accent/20 bg-accent/5 p-4">
            <div class="relative flex h-8 w-8 shrink-0 items-center justify-center">
              <div class="absolute inset-0 animate-ping rounded-full bg-accent/30" />
              <div class="relative flex h-8 w-8 items-center justify-center rounded-full bg-accent/20">
                <Server class="h-4 w-4 text-accent" :stroke-width="1.75" />
              </div>
            </div>
            <div class="flex-1">
              <div class="text-[12px] font-semibold text-text-primary">
                Waiting for <span class="font-mono">{{ trimmedName }}</span> to connect…
              </div>
              <div class="mt-0.5 text-[11px] text-text-muted">
                Elapsed: <span class="tabular-nums">{{ formatElapsed(elapsed) }}</span>
                · Once the agent comes online, you'll be taken to the dashboard.
              </div>
            </div>
          </div>

          <div class="flex items-center justify-between pt-1">
            <button
              type="button"
              class="text-[11px] font-medium text-text-muted transition-colors hover:text-text-secondary"
              @click="router.push(`/edges/${trimmedName}`)"
            >
              Skip — I'll come back later
            </button>
            <span class="text-[10px] text-text-muted/70">
              Polling every 2.5s
            </span>
          </div>
        </template>

        <!-- Step 3: Connected -->
        <template v-else>
          <div class="flex flex-col items-center text-center">
            <div class="relative flex h-16 w-16 items-center justify-center">
              <div class="absolute inset-0 rounded-2xl bg-success/20 blur-lg" />
              <div class="relative flex h-16 w-16 items-center justify-center rounded-2xl border border-success/30 bg-success/10">
                <PartyPopper class="h-8 w-8 text-success" :stroke-width="1.5" />
              </div>
            </div>
            <h3 class="mt-4 text-[16px] font-bold text-text-primary">
              <span class="font-mono">{{ trimmedName }}</span> is online
            </h3>
            <p class="mt-1 text-[12px] text-text-muted">
              Your first edge is reporting back. You can manage workloads and MCP servers from the dashboard.
            </p>

            <div class="mt-4 grid w-full max-w-md grid-cols-2 gap-2">
              <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-3 text-left">
                <div class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Agent</div>
                <div class="mt-0.5 truncate font-mono text-[11px] text-text-secondary">
                  {{ agentVersion || '—' }}
                </div>
              </div>
              <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-3 text-left">
                <div class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Connected after</div>
                <div class="mt-0.5 tabular-nums text-[11px] text-text-secondary">
                  {{ formatElapsed(elapsed) }}
                </div>
              </div>
            </div>

            <div class="mt-6 flex items-center gap-3">
              <button
                type="button"
                class="group flex items-center gap-2 rounded-xl border border-border-default bg-surface-overlay/60 px-4 py-2.5 text-[12px] font-semibold text-text-primary transition-all hover:border-accent/30 hover:bg-surface-hover active:scale-[0.98]"
                @click="viewEdge"
              >
                <Server class="h-3.5 w-3.5" :stroke-width="1.75" />
                View edge
              </button>
              <button
                type="button"
                class="group flex items-center gap-2 rounded-xl bg-accent px-4 py-2.5 text-[12px] font-semibold text-white transition-all hover:bg-accent-hover hover:shadow-lg hover:shadow-accent/20 active:scale-[0.98]"
                @click="finish"
              >
                Go to dashboard
                <ArrowRight class="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" :stroke-width="2" />
              </button>
            </div>
          </div>
        </template>
      </div>
    </div>

    <!-- Footer: only relevant on step 1 -->
    <div v-if="step === 1" class="mt-3 flex items-center justify-between px-1 text-[10px] text-text-muted">
      <span>You can always add more edges from the Edges page.</span>
      <button
        type="button"
        class="font-medium text-text-muted transition-colors hover:text-text-secondary"
        @click="router.push('/edges')"
      >
        Already have an edge? Go to Edges →
      </button>
    </div>
  </div>
</template>
