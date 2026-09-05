<script setup lang="ts">
import { ref, computed, nextTick, onUnmounted, watch } from 'vue'
import { ArrowLeft, Boxes, Server, ArrowRight, Copy, Check, Loader2, CircleDot, PartyPopper } from 'lucide-vue-next'
import { createEdge, probeEdge } from './api'
import CreateGuidance, { type CreateGuidanceValue } from './portalkit/CreateGuidance.vue'
import type { EdgeType, ErrorResponse } from './types'

const props = withDefaults(defineProps<{
  cluster: string | null
  requiredType?: EdgeType
  cancelLabel?: string
}>(), {
  cancelLabel: 'Back to edges',
})
const emit = defineEmits<{
  cancel: []
  created: [name: string, type: EdgeType]
}>()

type Step = 1 | 2 | 3
const step = ref<Step>(1)
const name = ref('')
const edgeType = ref<EdgeType>(props.requiredType ?? 'kubernetes')
const labels = ref('')
const saving = ref(false)
const error = ref<string | null>(null)

const joinToken = ref<string | null>(null)
const tokenError = ref<string | null>(null)
const copied = ref<string | null>(null)
const copyFeedback = ref('')
const failedCopyField = ref<string | null>(null)
const agentVersion = ref<string | null>(null)
const elapsed = ref(0)

let pollTimer: ReturnType<typeof setInterval> | null = null
let elapsedTimer: ReturnType<typeof setInterval> | null = null
let copyTimer: ReturnType<typeof setTimeout> | null = null
let active = true

function stopPolling(): void {
  if (pollTimer) clearInterval(pollTimer)
  if (elapsedTimer) clearInterval(elapsedTimer)
  pollTimer = null
  elapsedTimer = null
}

function clearSetupSecret(): void {
  joinToken.value = null
  failedCopyField.value = null
  copyFeedback.value = ''
}

onUnmounted(() => {
  active = false
  stopPolling()
  if (copyTimer) clearTimeout(copyTimer)
  clearSetupSecret()
})

const trimmed = computed(() => name.value.trim())
const edgeTypeLocked = computed(() => props.requiredType !== undefined)
const canContinue = computed(() => trimmed.value.length > 0 && !saving.value && (!props.requiredType || edgeType.value === props.requiredType))
const stepLabels = ['Configure', 'Install agent', 'Connected'] as const
const connectionAnnouncement = computed(() => {
  if (step.value === 1) return 'Step 1 of 3: Configure.'
  if (step.value === 3) return `${trimmed.value} connected.`
  if (tokenError.value) return ''
  return joinToken.value
    ? `Waiting for ${trimmed.value} to connect.`
    : `Generating join token for ${trimmed.value}.`
})
async function focusCurrentStepHeading(): Promise<void> {
  await nextTick()
  document.getElementById('edge-wizard-step-heading')?.focus()
}
watch(step, (current) => {
  if (current === 1) return
  void focusCurrentStepHeading()
})
watch(() => props.requiredType, (requiredType) => {
  if (requiredType) edgeType.value = requiredType
}, { immediate: true })
const edgeGuidanceValues = computed<CreateGuidanceValue[]>(() => [
  { label: 'Edge name', value: trimmed.value || 'Not entered yet', technical: true },
  { label: 'Resource type', value: edgeType.value === 'kubernetes' ? 'KubernetesCluster' : 'LinuxServer', technical: true },
  { label: 'Scheduling labels', value: labels.value.trim() || 'None', technical: true },
])
const edgePrerequisites = [
  'Access to the target cluster with Helm, or to the Linux host with the Faros CLI.',
  'A unique Kubernetes-compatible name in this workspace.',
  'Optional key=value labels if Workloads will target this edge.',
]
const edgeNextSteps = [
  'Faros creates a KubernetesCluster or LinuxServer resource and mints a one-time join token.',
  'Run the generated command on the target; the token is masked here and copied only when requested.',
  'The agent exchanges the token for an edge-scoped credential and opens its outbound tunnel.',
]

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
  if (copied.value === field) return 'Copied'
  return failedCopyField.value === field ? `Retry copying ${label}` : `Copy ${label}`
}

async function copy(build: (t: string) => string, field: string, label: string) {
  if (!joinToken.value) return
  if (copyTimer) {
    clearTimeout(copyTimer)
    copyTimer = null
  }
  copied.value = null
  failedCopyField.value = null
  try {
    await navigator.clipboard.writeText(build(joinToken.value))
    copied.value = field
    failedCopyField.value = null
    copyFeedback.value = `${label} copied to clipboard.`
    copyTimer = setTimeout(() => {
      copied.value = null
      copyFeedback.value = ''
      copyTimer = null
    }, 2000)
  } catch {
    failedCopyField.value = field
    copyFeedback.value = `Could not copy the ${label.toLowerCase()}. The join token remains masked; try copying again.`
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
  if (props.requiredType && edgeType.value !== props.requiredType) {
    error.value = `This flow requires a ${props.requiredType === 'kubernetes' ? 'KubernetesCluster' : 'LinuxServer'} edge.`
    edgeType.value = props.requiredType
    return
  }
  saving.value = true
  error.value = null
  try {
    await createEdge(trimmed.value, edgeType.value, parseLabels())
    if (!active) return
    step.value = 2
    startPolling()
  } catch (e) {
    if (!active) return
    error.value = (e as ErrorResponse)?.message ?? 'Create failed'
  } finally {
    if (active) saving.value = false
  }
}

function startPolling() {
  const edgeName = trimmed.value
  const type = edgeType.value
  const tokenDeadline = Date.now() + 30000
  elapsed.value = 0
  elapsedTimer = setInterval(() => (elapsed.value += 1), 1000)
  pollTimer = setInterval(async () => {
    if (!active || step.value !== 2) return
    try {
      const p = await probeEdge(edgeName, type)
      if (!active || step.value !== 2 || !p) return
      if (!joinToken.value && p.joinToken) joinToken.value = p.joinToken
      if (!joinToken.value && Date.now() > tokenDeadline) {
        tokenError.value = `Could not retrieve join token. Run: faros edge join-command ${edgeName}`
      }
      if (p.connected) {
        agentVersion.value = p.agentVersion ?? null
        stopPolling()
        step.value = 3
        clearSetupSecret()
      }
    } catch { /* transient; keep polling */ }
  }, 2500)
}

function leaveWizard(event: 'cancel' | 'created'): void {
  active = false
  stopPolling()
  clearSetupSecret()
  if (event === 'cancel') emit('cancel')
  else emit('created', trimmed.value, edgeType.value)
}

function fmt(s: number) {
  if (s < 60) return `${s}s`
  return `${Math.floor(s / 60)}m ${s % 60}s`
}
</script>

<template>
  <div class="wiz">
    <div class="wiz-hero">
      <h1>Connect an edge</h1>
      <p>A Kubernetes cluster or Linux/SSH server you want to manage from this workspace.</p>
    </div>

    <ol class="wiz-steps" aria-label="Edge connection progress">
      <li v-for="(l, i) in stepLabels" :key="l"
          class="wiz-step" :class="{ done: step > i + 1, active: step === i + 1 }"
          :aria-current="step === i + 1 ? 'step' : undefined">
        <CircleDot :size="12" /> {{ l }}
      </li>
    </ol>
    <span class="wiz-sr-only" role="status" aria-live="polite" aria-atomic="true">{{ connectionAnnouncement }}</span>

    <div v-if="error" class="banner error" role="alert" aria-live="assertive">{{ error }}</div>

    <!-- Step 1 -->
    <div v-if="step === 1" class="wiz-card k-card k-create-surface--guided">
      <div class="k-create-body--guided">
        <div class="k-create-fields">
          <label for="edge-name" class="lbl">Edge name</label>
          <input id="edge-name" v-model="name" class="k-input" placeholder="e.g. prod-us-east-1" @keyup.enter="canContinue && handleCreate()" />

          <fieldset class="types">
            <legend class="lbl">Type</legend>
            <label class="type" :class="{ sel: edgeType === 'kubernetes' }" for="edge-type-kubernetes">
              <input id="edge-type-kubernetes" v-model="edgeType" class="type-radio" name="edge-type" type="radio" value="kubernetes" :disabled="edgeTypeLocked && props.requiredType !== 'kubernetes'" />
              <Boxes :size="15" aria-hidden="true" /> <span><b>Kubernetes</b><small>Existing K8s cluster</small></span>
            </label>
            <label class="type" :class="{ sel: edgeType === 'server' }" for="edge-type-server">
              <input id="edge-type-server" v-model="edgeType" class="type-radio" name="edge-type" type="radio" value="server" :disabled="edgeTypeLocked && props.requiredType !== 'server'" />
              <Server :size="15" aria-hidden="true" /> <span><b>Server</b><small>Bare-metal or VM (SSH)</small></span>
            </label>
          </fieldset>
          <p v-if="edgeTypeLocked" class="muted">This edge type is required to continue the originating {{ props.requiredType === 'kubernetes' ? 'workload' : 'resource' }} flow.</p>

          <label for="edge-labels" class="lbl">Labels <span class="muted">(optional)</span></label>
          <input id="edge-labels" v-model="labels" class="k-input" placeholder="env=prod, region=us-east" />

          <div class="wiz-actions">
            <button type="button" class="k-btn k-btn--ghost" :disabled="saving" @click="leaveWizard('cancel')">
              <ArrowLeft :size="14" aria-hidden="true" /> {{ props.cancelLabel }}
            </button>
            <button type="button" class="k-btn k-btn--primary" :disabled="!canContinue" @click="handleCreate">
              <Loader2 v-if="saving" :size="14" class="spin" />
              {{ saving ? 'Creating…' : 'Create & continue' }}
              <ArrowRight v-if="!saving" :size="14" />
            </button>
          </div>
        </div>
        <CreateGuidance
          title="Configure the edge"
          description="Choose how Faros will identify and manage this target. After creation, install the agent from the generated command."
          :prerequisites="edgePrerequisites"
          :values="edgeGuidanceValues"
          :next-steps="edgeNextSteps"
        />
      </div>
    </div>

    <!-- Step 2 -->
    <div v-else-if="step === 2" class="wiz-card k-card">
      <h3 id="edge-wizard-step-heading" tabindex="-1">Install the agent on your {{ edgeType === 'kubernetes' ? 'cluster' : 'server' }}</h3>
      <p class="muted">Run one of the commands below from the target. This updates automatically when
        <b>{{ trimmed }}</b> connects.</p>

      <div v-if="tokenError" class="banner warn" role="alert" aria-live="assertive">{{ tokenError }}</div>
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

      <div v-if="failedCopyField" class="banner warn" role="alert">
        Clipboard access is unavailable. The one-time join token remains masked; retry the explicit copy action.
      </div>

      <span class="wiz-sr-only" role="status" aria-live="polite">{{ copyFeedback }}</span>

      <div class="waiting"><Loader2 :size="14" class="spin" /> Waiting for <b>{{ trimmed }}</b> to connect… <span class="muted">({{ fmt(elapsed) }})</span></div>
      <div class="wiz-actions">
        <button type="button" class="k-btn k-btn--ghost" @click="leaveWizard('cancel')">{{ props.cancelLabel }}</button>
        <button type="button" class="k-btn k-btn--ghost" @click="leaveWizard('created')">Skip waiting — continue</button>
      </div>
    </div>

    <!-- Step 3 -->
    <div v-else class="wiz-card k-card center">
      <PartyPopper :size="30" />
      <h3 id="edge-wizard-step-heading" tabindex="-1"><b>{{ trimmed }}</b> is online</h3>
      <p class="muted">Agent {{ agentVersion || '—' }} · connected after {{ fmt(elapsed) }}</p>
      <div class="wiz-actions">
        <button type="button" class="k-btn k-btn--primary" @click="leaveWizard('created')">Continue <ArrowRight :size="14" /></button>
      </div>
    </div>
  </div>
</template>
