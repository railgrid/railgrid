<script setup lang="ts">
import InlineNotification from './portalkit/InlineNotification.vue'
import { ref, computed, nextTick, onUnmounted, watch } from 'vue'
import { ArrowLeft, Boxes, Laptop, Server, ArrowRight, Copy, Check, Loader2, PartyPopper } from 'lucide-vue-next'
import { createEdge, probeEdge } from './api'
import { parseEdgeLabelPairs } from './edgeLabels'
import { MACOS_MASKED_JOIN_TOKEN, hubURLForCluster, macosJoinSnippet } from './macos'
import CreateGuidance from './portalkit/CreateGuidance.vue'
import { edgeTypeLabel, type EdgeType, type ErrorResponse } from './types'
import { track, trackFailure } from './telemetry'

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
let copyGeneration = 0
let active = true

function stopPolling(): void {
  if (pollTimer) clearInterval(pollTimer)
  if (elapsedTimer) clearInterval(elapsedTimer)
  pollTimer = null
  elapsedTimer = null
}

function clearSetupSecret(): void {
  copyGeneration += 1
  if (copyTimer) clearTimeout(copyTimer)
  copyTimer = null
  joinToken.value = null
  copied.value = null
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
const nameError = computed(() => {
  if (!trimmed.value) return ''
  return trimmed.value.length > 253 || !/^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?(?:\.[a-z0-9](?:[-a-z0-9]*[a-z0-9])?)*$/.test(trimmed.value)
    ? 'Use up to 253 lowercase letters, numbers, hyphens, or dots. Each part must start and end with a letter or number.'
    : ''
})
const edgeTypeLocked = computed(() => props.requiredType !== undefined)
const labelResult = computed(() => parseEdgeLabelPairs(labels.value, 'label'))
const canContinue = computed(() => trimmed.value.length > 0 && !nameError.value && !labelResult.value.error && !saving.value && (!props.requiredType || edgeType.value === props.requiredType))
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
  if (!active || typeof document === 'undefined') return
  document.getElementById('edge-wizard-step-heading')?.focus()
}
watch(step, (current) => {
  if (current === 1) return
  void focusCurrentStepHeading()
})
watch(() => props.requiredType, (requiredType) => {
  if (requiredType) edgeType.value = requiredType
}, { immediate: true })
const hubURL = computed(() => {
  return hubURLForCluster(props.cluster)
})

const masked = MACOS_MASKED_JOIN_TOKEN
function helmSnippet(token: string) {
  return `helm install railgrid-agent oci://ghcr.io/railgrid/charts/railgrid-agent \\
  --namespace railgrid-agent --create-namespace \\
  --set agent.edgeName=${trimmed.value} \\
  --set agent.hub.url=${hubURL.value} \\
  --set agent.hub.token=${token}`
}
function cliSnippet(token: string) {
  if (edgeType.value === 'macos') return macosJoinSnippet(trimmed.value, props.cluster, token)
  return `railgrid agent join \\
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
  const generation = ++copyGeneration
  if (copyTimer) {
    clearTimeout(copyTimer)
    copyTimer = null
  }
  copied.value = null
  failedCopyField.value = null
  try {
    await navigator.clipboard.writeText(build(joinToken.value))
    if (!active || generation !== copyGeneration) return
    track('edge_command_copied', { command: field === 'helm' ? 'join_helm' : 'join_cli', kind: edgeType.value, placement: 'wizard' })
    copied.value = field
    failedCopyField.value = null
    copyFeedback.value = `${label} copied to clipboard.`
    copyTimer = setTimeout(() => {
      copied.value = null
      copyFeedback.value = ''
      copyTimer = null
    }, 2000)
  } catch {
    if (!active || generation !== copyGeneration) return
    failedCopyField.value = field
    copyFeedback.value = `Could not copy the ${label.toLowerCase()}. The join token remains masked; try copying again.`
  }
}

async function handleCreate() {
  if (!trimmed.value) { error.value = 'Name is required'; return }
  if (nameError.value) return
  if (labelResult.value.error) return
  if (props.requiredType && edgeType.value !== props.requiredType) {
    error.value = `This flow requires a ${props.requiredType === 'kubernetes' ? 'KubernetesCluster' : props.requiredType === 'server' ? 'LinuxServer' : 'MacOSServer'} edge.`
    edgeType.value = props.requiredType
    return
  }
  saving.value = true
  error.value = null
  try {
    await createEdge(trimmed.value, edgeType.value, labelResult.value.value)
    track('edge_created', { kind: edgeType.value })
    if (!active) return
    step.value = 2
    startPolling()
  } catch (e) {
    trackFailure('edge_create_failed', e, { kind: edgeType.value })
    if (!active) return
    error.value = (e as ErrorResponse)?.message ?? 'Create failed'
  } finally {
    if (active) saving.value = false
  }
}

function startPolling() {
  const edgeName = trimmed.value
  const type = edgeType.value
  const startedAt = Date.now()
  elapsed.value = 0
  elapsedTimer = setInterval(() => {
    elapsed.value = Math.floor((Date.now() - startedAt) / 1000)
    if (elapsed.value >= 30 && !joinToken.value) {
      if (!tokenError.value) track('edge_join_token_failed', { kind: type })
      tokenError.value = `Could not retrieve join token. Run: railgrid edge join-command ${edgeName}`
    }
  }, 1000)
  pollTimer = setInterval(async () => {
    if (!active || step.value !== 2) return
    try {
      const p = await probeEdge(edgeName, type)
      if (!active || step.value !== 2 || !p) return
      if (!joinToken.value && p.joinToken) {
        joinToken.value = p.joinToken
        tokenError.value = null
      }
      if (p.connected) {
        agentVersion.value = p.agentVersion ?? null
        track('edge_connected', { kind: type, placement: 'wizard' })
        stopPolling()
        step.value = 3
        clearSetupSecret()
      }
    } catch { /* transient; keep polling */ }
  }, 2500)
}

function leaveWizard(event: 'cancel' | 'created'): void {
  if (event === 'cancel') track('edge_connect_cancelled', { step: step.value === 1 ? 'configure' : step.value === 2 ? 'install' : 'connected' })
  else if (step.value === 2) track('edge_connect_skipped', { kind: edgeType.value })
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
  <div class="wiz k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="saving" @click="leaveWizard('cancel')">
      <ArrowLeft :size="14" aria-hidden="true" /> {{ props.cancelLabel }}
    </button>
    <header class="k-create-header">
      <h1 class="k-create-title">Connect an edge</h1>
      <p class="k-create-description">A Kubernetes cluster, or a Linux or MacOS host, that you want to manage from this workspace.</p>
    </header>

    <ol class="wiz-steps k-wizard-steps" aria-label="Edge connection progress">
      <li v-for="(l, i) in stepLabels" :key="l"
          :aria-current="step === i + 1 ? 'step' : undefined">
        {{ l }}
      </li>
    </ol>
    <span class="wiz-sr-only" role="status" aria-live="polite" aria-atomic="true">{{ connectionAnnouncement }}</span>

    <InlineNotification v-if="error" tone="error" :message="error" />

    <!-- Step 1 -->
    <form v-if="step === 1" class="k-create-surface k-create-surface--guided" :aria-busy="saving" @submit.prevent="canContinue && handleCreate()">
      <div class="k-create-body k-create-body--guided">
        <div class="k-create-fields">
          <label for="edge-name" class="lbl">Edge name</label>
          <input id="edge-name" v-model="name" class="k-input" placeholder="e.g. prod-us-east-1" required maxlength="253" :aria-invalid="nameError ? 'true' : undefined" :aria-describedby="nameError ? 'edge-name-error' : 'edge-name-help'" :disabled="saving" />
          <p v-if="nameError" id="edge-name-error" class="field-error" role="alert">{{ nameError }}</p>
          <p v-else id="edge-name-help" class="muted">Use a unique name with lowercase letters, numbers, hyphens, or dots. Each part must start and end with a letter or number.</p>

          <fieldset class="types">
            <legend class="lbl">Type</legend>
            <label class="type" :class="{ sel: edgeType === 'kubernetes' }" for="edge-type-kubernetes">
              <input id="edge-type-kubernetes" v-model="edgeType" class="type-radio" name="edge-type" type="radio" value="kubernetes" :disabled="saving || (edgeTypeLocked && props.requiredType !== 'kubernetes')" />
              <Boxes :size="15" aria-hidden="true" /> <span><b>Kubernetes</b><small>Existing K8s cluster</small></span>
            </label>
            <label class="type" :class="{ sel: edgeType === 'server' }" for="edge-type-server">
              <input id="edge-type-server" v-model="edgeType" class="type-radio" name="edge-type" type="radio" value="server" :disabled="saving || (edgeTypeLocked && props.requiredType !== 'server')" />
              <Server :size="15" aria-hidden="true" /> <span><b>Linux</b><small>Bare-metal or VM: SSH, host services, local runner</small></span>
            </label>
            <label class="type" :class="{ sel: edgeType === 'macos' }" for="edge-type-macos">
              <input id="edge-type-macos" v-model="edgeType" class="type-radio" name="edge-type" type="radio" value="macos" :disabled="saving || (edgeTypeLocked && props.requiredType !== 'macos')" />
              <Laptop :size="15" aria-hidden="true" /> <span><b>MacOS</b><small>Host services and local runner</small></span>
            </label>
          </fieldset>
          <p v-if="edgeTypeLocked" class="muted">This edge type is required to continue the originating {{ props.requiredType === 'kubernetes' ? 'workload' : 'resource' }} flow.</p>

          <label for="edge-labels" class="lbl">Labels <span class="muted">(optional)</span></label>
          <input id="edge-labels" v-model="labels" class="k-input" placeholder="env=prod, region=us-east" :aria-invalid="labelResult.error ? 'true' : undefined" :aria-describedby="labelResult.error ? 'edge-labels-help edge-labels-error' : 'edge-labels-help'" :disabled="saving" />
          <p id="edge-labels-help" class="muted">{{ edgeType === 'kubernetes' ? 'Use key=value pairs to target this edge with workloads.' : 'Use key=value pairs to label this edge. Workloads target Kubernetes edges only.' }}</p>
          <p v-if="labelResult.error" id="edge-labels-error" class="field-error" role="alert">{{ labelResult.error }}</p>
        </div>
        <CreateGuidance
          title="Before you create"
          :description="edgeType === 'kubernetes' ? 'You will need access to the cluster and Helm or the Railgrid CLI to install the agent.' : 'You will need access to this host and the Railgrid CLI to install the agent.'"
        />
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--ghost" :disabled="saving" @click="leaveWizard('cancel')">Cancel</button>
        <button type="submit" class="k-btn k-btn--primary" :disabled="!canContinue">
          <Loader2 v-if="saving" :size="14" class="k-spin" aria-hidden="true" />
          {{ saving ? 'Creating…' : 'Create & continue' }}
          <ArrowRight v-if="!saving" :size="14" aria-hidden="true" />
        </button>
      </div>
    </form>

    <!-- Step 2 -->
    <div v-else-if="step === 2" class="k-create-surface">
      <div class="wiz-card">
      <h3 id="edge-wizard-step-heading" tabindex="-1">Install the agent on your {{ edgeType === 'kubernetes' ? 'Kubernetes cluster' : `${edgeTypeLabel(edgeType)} host` }}</h3>
      <p class="muted">Run one of the commands below from the target. This updates automatically when
        <b>{{ trimmed }}</b> connects.</p>

      <InlineNotification v-if="tokenError" tone="warning" announce="assertive" :message="tokenError" />
      <div v-else-if="!joinToken" class="muted row"><Loader2 :size="14" class="k-spin" aria-hidden="true" /> Generating join token…</div>

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
          <div class="snippet-head"><span>CLI — railgrid agent join</span>
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

      <InlineNotification v-if="failedCopyField" tone="warning" announce="assertive" message="Clipboard access is unavailable. The one-time join token remains masked; retry the explicit copy action." />

      <span class="wiz-sr-only" role="status" aria-live="polite">{{ copyFeedback }}</span>

      <div class="waiting"><Loader2 :size="14" class="k-spin" aria-hidden="true" /> Waiting for <b>{{ trimmed }}</b> to connect… <span class="muted">({{ fmt(elapsed) }})</span></div>
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--ghost" @click="leaveWizard('cancel')">{{ props.cancelLabel }}</button>
        <button type="button" class="k-btn k-btn--ghost" @click="leaveWizard('created')">Skip waiting — continue</button>
      </div>
    </div>

    <!-- Step 3 -->
    <div v-else class="k-create-surface">
      <div class="wiz-card center">
      <PartyPopper :size="30" aria-hidden="true" />
      <h3 id="edge-wizard-step-heading" tabindex="-1"><b>{{ trimmed }}</b> is online</h3>
      <p class="muted">Agent {{ agentVersion || '—' }} · connected after {{ fmt(elapsed) }}</p>
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--primary" @click="leaveWizard('created')">Continue <ArrowRight :size="14" aria-hidden="true" /></button>
      </div>
    </div>
  </div>
</template>
