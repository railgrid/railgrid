<script setup lang="ts">
import { ref, computed, onUnmounted } from 'vue'
import { ArrowLeft, Boxes, Server, ArrowRight, Copy, Check, Loader2, CircleDot, PartyPopper } from 'lucide-vue-next'
import { createEdge, probeEdge } from './api'
import type { EdgeType, ErrorResponse } from './types'

const props = defineProps<{ cluster: string | null }>()
const emit = defineEmits<{
  cancel: []
  created: [name: string, type: EdgeType]
}>()

type Step = 1 | 2 | 3
const step = ref<Step>(1)
const name = ref('')
const edgeType = ref<EdgeType>('kubernetes')
const labels = ref('')
const saving = ref(false)
const error = ref<string | null>(null)

const joinToken = ref<string | null>(null)
const tokenError = ref<string | null>(null)
const copied = ref<string | null>(null)
const copyFeedback = ref('')
const agentVersion = ref<string | null>(null)
const elapsed = ref(0)

let pollTimer: ReturnType<typeof setInterval> | null = null
let elapsedTimer: ReturnType<typeof setInterval> | null = null
let copyTimer: ReturnType<typeof setTimeout> | null = null
onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
  if (elapsedTimer) clearInterval(elapsedTimer)
  if (copyTimer) clearTimeout(copyTimer)
})

const trimmed = computed(() => name.value.trim())
const canContinue = computed(() => trimmed.value.length > 0 && !saving.value)

const hubURL = computed(() => {
  const origin = window.location.origin
  return props.cluster ? `${origin}/clusters/${props.cluster}` : origin
})

const masked = '••••••••••••••••'
function helmSnippet(token: string) {
  return `helm install faros-agent oci://ghcr.io/faroshq/charts/faros-agent \\
  --namespace faros-agent --create-namespace \\
  --set agent.edgeName=${trimmed.value} \\
  --set agent.hub.url=${hubURL.value} \\
  --set agent.hub.token=${token}`
}
function cliSnippet(token: string) {
  return `faros agent join \\
  --hub-url ${hubURL.value} \\
  --edge-name ${trimmed.value} \\
  --type ${edgeType.value} \\
  --token ${token}`
}
const helmText = computed(() => helmSnippet(masked))
const cliText = computed(() => cliSnippet(masked))

function copyControlLabel(field: string, label: string): string {
  return copied.value === field ? 'Copied' : `Copy ${label}`
}

async function copy(build: (t: string) => string, field: string, label: string) {
  if (!joinToken.value) return
  try {
    await navigator.clipboard.writeText(build(joinToken.value))
    copied.value = field
    copyFeedback.value = `${label} copied to clipboard.`
    if (copyTimer) clearTimeout(copyTimer)
    copyTimer = setTimeout(() => {
      copied.value = null
      copyFeedback.value = ''
      copyTimer = null
    }, 2000)
  } catch {
    copyFeedback.value = `Could not copy the ${label.toLowerCase()}. Select the command and copy it manually.`
  }
}

function parseLabels(): Record<string, string> {
  const out: Record<string, string> = {}
  if (labels.value.trim()) {
    for (const pair of labels.value.split(',')) {
      const [k, v] = pair.split('=').map((s) => s.trim())
      if (k) out[k] = v ?? ''
    }
  }
  return out
}

async function handleCreate() {
  if (!trimmed.value) { error.value = 'Name is required'; return }
  saving.value = true
  error.value = null
  try {
    await createEdge(trimmed.value, edgeType.value, parseLabels())
    step.value = 2
    startPolling()
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Create failed'
  } finally {
    saving.value = false
  }
}

function startPolling() {
  const edgeName = trimmed.value
  const type = edgeType.value
  const tokenDeadline = Date.now() + 30000
  elapsed.value = 0
  elapsedTimer = setInterval(() => (elapsed.value += 1), 1000)
  pollTimer = setInterval(async () => {
    try {
      const p = await probeEdge(edgeName, type)
      if (!p) return
      if (!joinToken.value && p.joinToken) joinToken.value = p.joinToken
      if (!joinToken.value && Date.now() > tokenDeadline) {
        tokenError.value = `Could not retrieve join token. Run: faros edge join-command ${edgeName}`
      }
      if (p.connected) {
        agentVersion.value = p.agentVersion ?? null
        if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
        if (elapsedTimer) { clearInterval(elapsedTimer); elapsedTimer = null }
        step.value = 3
      }
    } catch { /* transient; keep polling */ }
  }, 2500)
}

function fmt(s: number) {
  if (s < 60) return `${s}s`
  return `${Math.floor(s / 60)}m ${s % 60}s`
}
</script>

<template>
  <div class="wiz">
    <div class="wiz-hero">
      <h1>Connect your first edge</h1>
      <p>A Kubernetes cluster or Linux/SSH server you want to manage from this workspace.</p>
    </div>

    <div class="wiz-steps">
      <span v-for="(l, i) in ['Configure', 'Install agent', 'Connected']" :key="l"
            class="wiz-step" :class="{ done: step > i + 1, active: step === i + 1 }">
        <CircleDot :size="12" /> {{ l }}
      </span>
    </div>

    <div v-if="error" class="banner error">{{ error }}</div>

    <!-- Step 1 -->
    <div v-if="step === 1" class="wiz-card k-card">
      <label for="edge-name" class="lbl">Edge name</label>
      <input id="edge-name" v-model="name" class="k-input" placeholder="e.g. prod-us-east-1" @keyup.enter="canContinue && handleCreate()" />

      <fieldset class="types">
        <legend class="lbl">Type</legend>
        <label class="type" :class="{ sel: edgeType === 'kubernetes' }" for="edge-type-kubernetes">
          <input id="edge-type-kubernetes" v-model="edgeType" class="type-radio" name="edge-type" type="radio" value="kubernetes" />
          <Boxes :size="15" aria-hidden="true" /> <span><b>Kubernetes</b><small>Existing K8s cluster</small></span>
        </label>
        <label class="type" :class="{ sel: edgeType === 'server' }" for="edge-type-server">
          <input id="edge-type-server" v-model="edgeType" class="type-radio" name="edge-type" type="radio" value="server" />
          <Server :size="15" aria-hidden="true" /> <span><b>Server</b><small>Bare-metal or VM (SSH)</small></span>
        </label>
      </fieldset>

      <label for="edge-labels" class="lbl">Labels <span class="muted">(optional)</span></label>
      <input id="edge-labels" v-model="labels" class="k-input" placeholder="env=prod, region=us-east" />

      <div class="wiz-actions">
        <button type="button" class="k-btn k-btn--ghost" @click="emit('cancel')">
          <ArrowLeft :size="14" aria-hidden="true" /> Back to edges
        </button>
        <button type="button" class="k-btn k-btn--primary" :disabled="!canContinue" @click="handleCreate">
          <Loader2 v-if="saving" :size="14" class="spin" />
          {{ saving ? 'Creating…' : 'Create & continue' }}
          <ArrowRight v-if="!saving" :size="14" />
        </button>
      </div>
    </div>

    <!-- Step 2 -->
    <div v-else-if="step === 2" class="wiz-card k-card">
      <h3>Install the agent on your {{ edgeType === 'kubernetes' ? 'cluster' : 'server' }}</h3>
      <p class="muted">Run one of the commands below from the target. This updates automatically when
        <b>{{ trimmed }}</b> connects.</p>

      <div v-if="tokenError" class="banner warn">{{ tokenError }}</div>
      <div v-else-if="!joinToken" class="muted row"><Loader2 :size="14" class="spin" /> Generating join token…</div>

      <template v-if="joinToken || tokenError">
        <div v-if="edgeType === 'kubernetes'" class="snippet">
          <div class="snippet-head"><span>Helm (recommended)</span>
            <button
              type="button"
              class="k-icon-action snippet-copy"
              :disabled="!joinToken"
              :aria-label="copyControlLabel('helm', 'Helm command')"
              :data-k-tip="copyControlLabel('helm', 'Helm command')"
              @click="copy(helmSnippet, 'helm', 'Helm command')"
            >
              <component :is="copied === 'helm' ? Check : Copy" :size="12" :stroke-width="1.75" aria-hidden="true" />
            </button>
          </div>
          <pre>{{ helmText }}</pre>
        </div>
        <div class="snippet">
          <div class="snippet-head"><span>CLI — faros agent join</span>
            <button
              type="button"
              class="k-icon-action snippet-copy"
              :disabled="!joinToken"
              :aria-label="copyControlLabel('cli', 'CLI command')"
              :data-k-tip="copyControlLabel('cli', 'CLI command')"
              @click="copy(cliSnippet, 'cli', 'CLI command')"
            >
              <component :is="copied === 'cli' ? Check : Copy" :size="12" :stroke-width="1.75" aria-hidden="true" />
            </button>
          </div>
          <pre>{{ cliText }}</pre>
        </div>
      </template>

      <span class="wiz-sr-only" role="status" aria-live="polite">{{ copyFeedback }}</span>

      <div class="waiting"><Loader2 :size="14" class="spin" /> Waiting for <b>{{ trimmed }}</b> to connect… <span class="muted">({{ fmt(elapsed) }})</span></div>
      <div class="wiz-actions">
        <button type="button" class="k-btn k-btn--ghost" @click="emit('cancel')">Back to edges</button>
        <button type="button" class="k-btn k-btn--ghost" @click="emit('created', trimmed, edgeType)">Skip — view edge details</button>
      </div>
    </div>

    <!-- Step 3 -->
    <div v-else class="wiz-card k-card center">
      <PartyPopper :size="30" />
      <h3><b>{{ trimmed }}</b> is online</h3>
      <p class="muted">Agent {{ agentVersion || '—' }} · connected after {{ fmt(elapsed) }}</p>
      <div class="wiz-actions">
        <button type="button" class="k-btn k-btn--primary" @click="emit('created', trimmed, edgeType)">View edge details <ArrowRight :size="14" /></button>
      </div>
    </div>
  </div>
</template>
