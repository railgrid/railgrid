<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ArrowLeft, Loader2, Rocket, Server } from 'lucide-vue-next'
import { createWorkload, deployMarketplaceApp, listEdges, type WorkloadDraft } from './api'
import { parseEdgeLabelPairs } from './edgeLabels'
import { MARKETPLACE, type MarketplaceApp } from './marketplace'
import type { Edge, ErrorResponse } from './types'
import CreateGuidance, { type CreateGuidanceValue } from './portalkit/CreateGuidance.vue'
import FirstRunGuide from './portalkit/FirstRunGuide.vue'

const props = defineProps<{
  mode: 'manual' | 'marketplace'
  appType?: string
}>()
const emit = defineEmits<{
  cancel: []
  completed: [message: string]
  connectEdge: []
}>()

const edges = ref<Edge[]>([])
const loading = ref(true)
const busy = ref(false)
const error = ref<string | null>(null)
const edgeLoadError = ref<string | null>(null)
const draft = ref({
  name: '',
  image: 'nginx:latest',
  replicas: 1,
  strategy: 'Spread' as 'Spread' | 'Singleton',
  selector: 'env=dev',
  // targetNamespace is shared by both modes: the edge-cluster namespace the
  // rendered objects land in. Empty means the CRD default ("default").
  targetNamespace: '',
  // imagePullSecrets is the simple-mode list of Secret names, comma-separated.
  imagePullSecrets: '',
})
const deployName = ref('')
const deployEdge = ref('')

// A route-owned form can disappear while an API read or mutation is still in
// flight. Keep a local lifecycle fence so a canceled/unmounted deployment
// cannot update the old form or start a mutation after its target preflight.
let active = true
let lifecycleGeneration = 0

function isCurrent(generation: number): boolean {
  return active && generation === lifecycleGeneration
}

function cancel(): void {
  if (busy.value) return
  active = false
  lifecycleGeneration += 1
  emit('cancel')
}

const app = computed<MarketplaceApp | null>(() =>
  props.mode === 'marketplace' ? MARKETPLACE.find((entry) => entry.type === props.appType) ?? null : null,
)
// Marketplace charts expose a Kubernetes Service target in the second step
// of deployMarketplaceApp. Keep LinuxServer edges out of this choice rather
// than allowing a workload target that the follow-up Service cannot reach.
const kubernetesEdges = computed(() => edges.value.filter((edge) => edge.type === 'kubernetes'))
const title = computed(() => app.value ? `Deploy ${app.value.label}` : 'New workload')
const credentialHint: Record<string, string> = {
  'api-key': 'API key (mint it in the app, paste on the Services tab)',
  'user-pass': '"username:password" (paste on the Services tab)',
  password: 'web password (paste on the Services tab)',
  optional: 'no token needed',
}

// Namespace names are RFC 1123 DNS labels — the rule the CRD enforces on
// spec.targetNamespace. Secret names are DNS subdomains (labels joined by dots).
const DNS_LABEL_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/
const DNS_LABEL_MAX = 63
const DNS_SUBDOMAIN_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/
const DNS_SUBDOMAIN_MAX = 253

function validateTargetNamespace(value: string): string | null {
  const ns = value.trim()
  if (!ns) return null
  if (ns.length > DNS_LABEL_MAX) return `Namespace names are at most ${DNS_LABEL_MAX} characters.`
  if (!DNS_LABEL_RE.test(ns)) return 'Use lowercase letters, digits and hyphens, starting and ending with a letter or digit.'
  return null
}

type PullSecretsResult = {
  value: string[]
  error: string | null
}

function parseImagePullSecrets(value: string): PullSecretsResult {
  const names: string[] = []
  if (!value.trim()) return { value: names, error: null }

  for (const rawName of value.split(',')) {
    const name = rawName.trim()
    if (!name) {
      return { value: names, error: 'Secret names cannot be empty; separate them with commas.' }
    }
    if (name.length > DNS_SUBDOMAIN_MAX || !DNS_SUBDOMAIN_RE.test(name)) {
      return { value: names, error: `"${name}" is not a valid Secret name (lowercase letters, digits, hyphens and dots).` }
    }
    if (names.includes(name)) {
      return { value: names, error: `Secret "${name}" is listed more than once.` }
    }
    names.push(name)
  }
  return { value: names, error: null }
}

const selectorResult = computed(() => parseEdgeLabelPairs(draft.value.selector, 'selector'))
const selectorError = computed(() => selectorResult.value.error)
const targetNamespaceError = computed(() => validateTargetNamespace(draft.value.targetNamespace))
const pullSecretsResult = computed(() => parseImagePullSecrets(draft.value.imagePullSecrets))
const pullSecretsError = computed(() => pullSecretsResult.value.error)
const replicasError = computed(() => Number.isSafeInteger(Number(draft.value.replicas)) && Number(draft.value.replicas) >= 1
  ? null : 'Enter a whole number of replicas of at least 1.')
const canSubmit = computed(() => {
  if (!active || loading.value || busy.value || edgeLoadError.value || kubernetesEdges.value.length === 0) return false
  if (targetNamespaceError.value) return false
  if (props.mode === 'marketplace') {
    return !!app.value && !!deployName.value.trim() && kubernetesEdges.value.some((edge) => edge.name === deployEdge.value)
  }
  return !!draft.value.name.trim() && !!draft.value.image.trim() && !selectorError.value && !pullSecretsError.value && !replicasError.value
})
const manualSelector = computed(() => selectorError.value ? {} : selectorResult.value.value)
const matchingEdges = computed(() => kubernetesEdges.value.filter(edge => {
  return Object.entries(manualSelector.value).every(([key, value]) => edge.labels?.[key] === value)
}))
const placementSummary = computed(() => selectorError.value
  ? 'Fix the selector to preview placements'
  : matchingEdges.value.length === 0 ? 'No matching edges'
  : draft.value.strategy === 'Singleton'
    ? `Up to 1 placement from ${matchingEdges.value.length} matching edge${matchingEdges.value.length === 1 ? '' : 's'}`
    : `${matchingEdges.value.length} matching edge${matchingEdges.value.length === 1 ? '' : 's'}`)
const workloadGuidanceValues = computed<CreateGuidanceValue[]>(() => [
  { label: 'Estimate', value: placementSummary.value },
])

async function submit(): Promise<void> {
  if (!active || !canSubmit.value) return
  const generation = ++lifecycleGeneration
  busy.value = true
  error.value = null
  try {
    if (props.mode === 'marketplace') {
      const selected = app.value
      if (!selected) {
        error.value = 'That marketplace app is no longer available.'
        return
      }
      const selectedEdge = kubernetesEdges.value.find((edge) => edge.name === deployEdge.value)
      if (!selectedEdge) {
        error.value = 'Select a KubernetesCluster edge before deploying a marketplace app.'
        return
      }
      // The form's edge list is only a snapshot. Re-read it immediately before
      // the first marketplace mutation so a disconnected/deleted target cannot
      // receive a workload (and its follow-up Service) after the form loaded.
      const latestEdges = await listEdges()
      if (!isCurrent(generation)) return
      const latestEdge = latestEdges.find((edge) => edge.name === selectedEdge.name && edge.type === 'kubernetes')
      edges.value = latestEdges
      if (!latestEdge) {
        deployEdge.value = kubernetesEdges.value[0]?.name ?? ''
        error.value = 'The selected KubernetesCluster edge is no longer available. Choose another edge.'
        return
      }
      await deployMarketplaceApp({
        name: deployName.value.trim(),
        edgeName: latestEdge.name,
        chart: selected.chart,
        values: selected.values,
        serviceType: selected.type,
        port: selected.port,
        targetNamespace: draft.value.targetNamespace.trim() || undefined,
      })
      if (!isCurrent(generation)) return
      emit('completed', `${selected.label} deployment started as ${deployName.value.trim()}.`)
      return
    }

    const workload: WorkloadDraft = {
      name: draft.value.name.trim(),
      image: draft.value.image.trim(),
      replicas: Number(draft.value.replicas),
      strategy: draft.value.strategy,
      selector: manualSelector.value,
      targetNamespace: draft.value.targetNamespace.trim() || undefined,
      imagePullSecrets: pullSecretsResult.value.value,
    }
    await createWorkload(workload)
    if (!isCurrent(generation)) return
    emit('completed', `Workload ${workload.name} created.`)
  } catch (e) {
    if (!isCurrent(generation)) return
    error.value = (e as ErrorResponse)?.message ?? (props.mode === 'marketplace' ? 'Deploy failed' : 'Create failed')
  } finally {
    if (isCurrent(generation)) busy.value = false
  }
}

async function loadEdges(): Promise<void> {
  const generation = lifecycleGeneration
  loading.value = true
  edgeLoadError.value = null
  const result = await listEdges().then((value) => ({ value }), (reason) => ({ reason }))
  if (!isCurrent(generation)) return
  if ('value' in result) {
    edges.value = result.value
    if (props.mode === 'marketplace' && app.value) {
      deployName.value = app.value.type
    }
    deployEdge.value = (props.mode === 'marketplace' ? kubernetesEdges.value : edges.value)[0]?.name ?? ''
  } else {
    edgeLoadError.value = (result.reason as ErrorResponse)?.message ?? 'Failed to load edges'
  }
  loading.value = false
}

onMounted(async () => {
  await loadEdges()
})

onUnmounted(() => {
  active = false
  lifecycleGeneration += 1
})
</script>

<template>
  <div class="k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="busy" @click="cancel">
      <ArrowLeft :size="14" aria-hidden="true" /> Workloads
    </button>
    <header class="k-create-header">
      <h1 class="k-create-title">{{ title }}</h1>
      <p v-if="app" class="k-create-description">Deploy a pinned Helm chart onto one KubernetesCluster edge and wire its Edges service.</p>
      <p v-else class="k-create-description">Deploy a workload across matching Kubernetes edges.</p>
    </header>

    <div v-if="error" class="banner error" role="alert">{{ error }}</div>
    <div v-if="loading" class="waiting" role="status" aria-live="polite">
      <Loader2 :size="14" class="spin" aria-hidden="true" /> Loading edges…
    </div>

    <div v-else-if="edgeLoadError" class="k-create-surface">
      <div class="k-create-body">
        <div class="banner error" role="alert">{{ edgeLoadError }}</div>
        <p class="muted">Railgrid could not verify that a KubernetesCluster edge is available, so workload creation is paused.</p>
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--ghost" :disabled="busy" @click="cancel">Back to workloads</button>
        <button type="button" class="k-btn k-btn--primary" :disabled="busy" @click="loadEdges">Retry</button>
      </div>
    </div>

    <div v-else-if="props.mode === 'marketplace' && !app" class="k-create-surface">
      <div class="k-create-body">
      <p class="banner error" role="alert">That marketplace app is not available.</p>
      </div>
      <div class="k-create-actions"><button class="k-btn k-btn--ghost" :disabled="busy" @click="cancel">Back to workloads</button></div>
    </div>

    <FirstRunGuide
      v-else-if="!error && kubernetesEdges.length === 0"
      title="Connect a Kubernetes edge first"
      description="Workloads are scheduled only onto KubernetesCluster edges."
      primary-label="Connect edge"
      :steps="[
        { label: 'Kubernetes edge', description: 'Connect the cluster that will run the workload.' },
        { label: 'Workload and placement', description: 'Choose an image or chart and the edges it should target.' },
        { label: 'Placements running', description: 'Agents apply the workload and report readiness per edge.' },
      ]"
      journey-label="Workload deployment path"
      @primary="emit('connectEdge')"
    >
      <template #icon><Server aria-hidden="true" /></template>
    </FirstRunGuide>

    <form v-else-if="app && props.mode === 'marketplace'" class="k-create-surface k-create-surface--wide k-create-surface--guided" :aria-busy="busy" @submit.prevent="submit">
      <div class="k-create-body k-create-body--guided">
      <div class="k-create-fields">
      <label class="fld">
        <span class="lbl">Workload name (required)</span>
        <input v-model="deployName" class="k-input" required :placeholder="app.type" />
      </label>
      <label class="fld">
        <span class="lbl">Edge (required)</span>
        <select v-model="deployEdge" class="k-input" required :disabled="kubernetesEdges.length === 0">
          <option value="" disabled>Select an edge</option>
          <option v-for="edge in kubernetesEdges" :key="edge.name" :value="edge.name">{{ edge.name }} (KubernetesCluster)</option>
        </select>
      </label>
      <label class="fld">
        <span id="marketplace-namespace-label" class="lbl">Target namespace (optional)</span>
        <input
          v-model="draft.targetNamespace"
          class="k-input"
          placeholder="default"
          aria-labelledby="marketplace-namespace-label"
          :aria-invalid="targetNamespaceError ? 'true' : undefined"
          :aria-describedby="targetNamespaceError ? 'marketplace-namespace-error' : 'marketplace-namespace-help'"
        />
        <span v-if="targetNamespaceError" id="marketplace-namespace-error" class="field-error" role="alert">{{ targetNamespaceError }}</span>
        <span v-else id="marketplace-namespace-help" class="field-help">Namespace on the edge cluster the chart renders into. It is created on each edge if missing and never deleted with the workload.</span>
      </label>
      <p class="muted">Deploys <span class="mono">{{ app.chart.chart }}@{{ app.chart.version }}</span> onto <b>{{ deployEdge || '—' }}</b> and wires an Edges Service on port {{ app.port }}. Auth: {{ credentialHint[app.credential] }}.</p>
      </div>
      <CreateGuidance
        title="Before you deploy"
        description="Railgrid creates a singleton workload on the selected edge and declares a Service for its endpoint. Add any required Service credential after deployment."
      />
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--ghost" :disabled="busy" @click="cancel">Cancel</button>
        <button type="submit" class="k-btn k-btn--primary" :disabled="!canSubmit">
          <Loader2 v-if="busy" :size="14" class="spin" aria-hidden="true" />
          <Rocket v-else :size="14" aria-hidden="true" />
          {{ busy ? 'Deploying…' : 'Deploy' }}
        </button>
      </div>
    </form>

    <form v-else class="k-create-surface k-create-surface--wide k-create-surface--guided" :aria-busy="busy" @submit.prevent="submit">
      <div class="k-create-body k-create-body--guided">
      <div class="k-create-fields">
      <label class="fld">
        <span class="lbl">Name (required)</span>
        <input v-model="draft.name" class="k-input" required placeholder="nginx-demo" />
      </label>
      <label class="fld">
        <span class="lbl">Image (required)</span>
        <input v-model="draft.image" class="k-input" required placeholder="nginx:latest" />
      </label>
      <label class="fld">
        <span id="workload-pull-secrets-label" class="lbl">Image pull secrets (optional, comma-separated)</span>
        <input
          v-model="draft.imagePullSecrets"
          class="k-input"
          placeholder="ghcr-pull"
          aria-labelledby="workload-pull-secrets-label"
          :aria-invalid="pullSecretsError ? 'true' : undefined"
          :aria-describedby="pullSecretsError ? 'workload-pull-secrets-error' : 'workload-pull-secrets-help'"
        />
        <span v-if="pullSecretsError" id="workload-pull-secrets-error" class="field-error" role="alert">{{ pullSecretsError }}</span>
        <span v-else id="workload-pull-secrets-help" class="field-help">Names of docker-registry Secrets for a private image. Each Secret must already exist in the target namespace on every selected edge; the workload only references it and never carries the token.</span>
      </label>
      <div class="workload-create-grid">
        <label class="fld">
          <span id="workload-replicas-label" class="lbl">Replicas (required)</span>
          <input v-model="draft.replicas" type="number" min="1" step="1" class="k-input" required aria-labelledby="workload-replicas-label" :aria-invalid="replicasError ? 'true' : undefined" :aria-describedby="replicasError ? 'workload-replicas-error' : undefined" />
          <span v-if="replicasError" id="workload-replicas-error" class="field-error" role="alert">{{ replicasError }}</span>
        </label>
        <label class="fld">
          <span class="lbl">Strategy</span>
          <select v-model="draft.strategy" class="k-input">
            <option value="Spread">Spread (all matching edges)</option>
            <option value="Singleton">Singleton (one edge)</option>
          </select>
        </label>
      </div>
      <label class="fld">
        <span id="workload-namespace-label" class="lbl">Target namespace (optional)</span>
        <input
          v-model="draft.targetNamespace"
          class="k-input"
          placeholder="default"
          aria-labelledby="workload-namespace-label"
          :aria-invalid="targetNamespaceError ? 'true' : undefined"
          :aria-describedby="targetNamespaceError ? 'workload-namespace-error' : 'workload-namespace-help'"
        />
        <span v-if="targetNamespaceError" id="workload-namespace-error" class="field-error" role="alert">{{ targetNamespaceError }}</span>
        <span v-else id="workload-namespace-help" class="field-help">Namespace on the edge cluster the Deployment and Service land in. It is created on each edge if missing and never deleted with the workload.</span>
      </label>
      <label class="fld">
        <span id="workload-selector-label" class="lbl">Edge selector (optional, comma-separated key=value pairs)</span>
        <input
          v-model="draft.selector"
          class="k-input"
          placeholder="env=dev"
          aria-labelledby="workload-selector-label"
          :aria-invalid="selectorError ? 'true' : undefined"
          :aria-describedby="selectorError ? 'workload-selector-error' : undefined"
        />
        <span v-if="selectorError" id="workload-selector-error" class="field-error" role="alert">{{ selectorError }}</span>
      </label>
      </div>
      <CreateGuidance
        title="Before you create"
        description="The selector and strategy determine where the workload runs. This preview uses the currently loaded edges; placement may change."
        values-heading="Placement preview"
        :values="workloadGuidanceValues"
      />
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--ghost" :disabled="busy" @click="cancel">Cancel</button>
        <button type="submit" class="k-btn k-btn--primary" :disabled="!canSubmit">
          <Loader2 v-if="busy" :size="14" class="spin" aria-hidden="true" />
          {{ busy ? 'Creating…' : 'Create workload' }}
        </button>
      </div>
    </form>
  </div>
</template>
