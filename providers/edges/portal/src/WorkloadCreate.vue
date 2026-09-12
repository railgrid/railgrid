<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ArrowLeft, Loader2, Rocket, Server } from 'lucide-vue-next'
import { createWorkload, deployMarketplaceApp, listEdges, type WorkloadDraft } from './api'
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

type SelectorResult = {
  value: Record<string, string>
  error: string | null
}

function parseSelector(value: string): SelectorResult {
  const selector: Record<string, string> = {}
  if (!value.trim()) return { value: selector, error: null }

  for (const rawPair of value.split(',')) {
    const parts = rawPair.split('=')
    if (parts.length !== 2) {
      return { value: selector, error: 'Use key=value pairs separated by commas.' }
    }
    const [key, val] = parts.map((part) => part.trim())
    if (!key || !val) {
      return { value: selector, error: 'Selector keys and values cannot be empty.' }
    }
    if (Object.hasOwn(selector, key)) {
      return { value: selector, error: `Selector key "${key}" is listed more than once.` }
    }
    selector[key] = val
  }
  return { value: selector, error: null }
}

// DEFAULT_TARGET_NAMESPACE mirrors the Workload CRD default for
// spec.targetNamespace (kept local so the api module mock in tests stays flat).
const DEFAULT_TARGET_NAMESPACE = 'default'
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

const selectorResult = computed(() => parseSelector(draft.value.selector))
const selectorError = computed(() => selectorResult.value.error)
const targetNamespaceError = computed(() => validateTargetNamespace(draft.value.targetNamespace))
const targetNamespace = computed(() => draft.value.targetNamespace.trim() || DEFAULT_TARGET_NAMESPACE)
const pullSecretsResult = computed(() => parseImagePullSecrets(draft.value.imagePullSecrets))
const pullSecretsError = computed(() => pullSecretsResult.value.error)
const pullSecretsSummary = computed(() => pullSecretsError.value
  ? 'Fix the Secret names'
  : pullSecretsResult.value.value.join(', ') || 'None (public image)')
const canSubmit = computed(() => {
  if (!active || loading.value || busy.value || edgeLoadError.value || kubernetesEdges.value.length === 0) return false
  if (targetNamespaceError.value) return false
  if (props.mode === 'marketplace') {
    return !!app.value && !!deployName.value.trim() && kubernetesEdges.value.some((edge) => edge.name === deployEdge.value)
  }
  return !!draft.value.name.trim() && !!draft.value.image.trim() && !selectorError.value && !pullSecretsError.value
})
const manualSelector = computed(() => selectorError.value ? {} : selectorResult.value.value)
const matchingEdges = computed(() => kubernetesEdges.value.filter(edge => {
  return Object.entries(manualSelector.value).every(([key, value]) => edge.labels?.[key] === value)
}))
const placementCount = computed(() => draft.value.strategy === 'Singleton'
  ? Math.min(1, matchingEdges.value.length)
  : matchingEdges.value.length)
const placementSummary = computed(() => selectorError.value
  ? 'Fix the selector to preview placements'
  : `${placementCount.value} current match${placementCount.value === 1 ? '' : 'es'}`)
const workloadGuidanceValues = computed<CreateGuidanceValue[]>(() => app.value ? [
  { label: 'Workload name', value: deployName.value.trim() || 'Not entered yet', technical: true },
  { label: 'Kubernetes edge', value: deployEdge.value || 'Not selected', technical: true },
  { label: 'Namespace', value: targetNamespaceError.value ? 'Fix the namespace' : targetNamespace.value, technical: true },
  { label: 'Chart', value: `${app.value.chart.chart}@${app.value.chart.version}`, technical: true },
  { label: 'Service port', value: String(app.value.port), technical: true },
  { label: 'Credentials', value: credentialHint[app.value.credential] || 'Review after creation' },
] : [
  { label: 'Workload name', value: draft.value.name.trim() || 'Not entered yet', technical: true },
  { label: 'Namespace', value: targetNamespaceError.value ? 'Fix the namespace' : targetNamespace.value, technical: true },
  { label: 'Image', value: draft.value.image.trim() || 'Not entered yet', technical: true },
  { label: 'Image pull secrets', value: pullSecretsSummary.value, technical: true },
  { label: 'Replicas', value: String(Number(draft.value.replicas) || 1), technical: true },
  { label: 'Strategy', value: draft.value.strategy },
  { label: 'Edge selector', value: draft.value.selector.trim() || 'All Kubernetes edges', technical: true },
  { label: 'Placements', value: placementSummary.value },
])
const workloadPrerequisites = computed(() => app.value ? [
  'A KubernetesCluster edge available for this singleton deployment.',
  'The pinned marketplace chart and version shown here.',
  'Any service credential listed below, added after deployment when required.',
] : [
  'At least one KubernetesCluster edge.',
  'A container image the target clusters can pull. For a private registry, a docker-registry Secret in the target namespace on every selected edge.',
  'Matching labels on target edges. Spread uses every match; Singleton uses one.',
])
const workloadNextSteps = computed(() => app.value ? [
  'Faros creates a singleton Helm Workload pinned to the selected edge.',
  'The edge agent creates the target namespace if it is missing; it is never deleted with the workload.',
  'Faros also declares an Edges Service for the chart endpoint.',
  'The edge agent applies the chart and reports workload readiness.',
  'Add the Service credential after deployment when the app requires one.',
] : [
  'Faros creates the Workload; the edge agent creates the target namespace if it is missing and never deletes it.',
  'The scheduler creates Placements for matching Kubernetes edges.',
  'Edge agents apply the derived Deployments and Workload status aggregates their readiness.',
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
      replicas: Number(draft.value.replicas) || 1,
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
        <p class="muted">Faros could not verify that a KubernetesCluster edge is available, so workload creation is paused.</p>
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

    <form v-else-if="app && props.mode === 'marketplace'" class="k-create-surface k-create-surface--wide k-create-surface--guided" @submit.prevent="submit">
      <div class="k-create-body k-create-body--guided">
      <div class="k-create-fields">
      <label class="fld">
        <span class="lbl">Workload name</span>
        <input v-model="deployName" class="k-input" :placeholder="app.type" />
      </label>
      <label class="fld">
        <span class="lbl">Edge</span>
        <select v-model="deployEdge" class="k-input" :disabled="kubernetesEdges.length === 0">
          <option value="" disabled>Select an edge</option>
          <option v-for="edge in kubernetesEdges" :key="edge.name" :value="edge.name">{{ edge.name }} (KubernetesCluster)</option>
        </select>
      </label>
      <label class="fld">
        <span class="lbl">Target namespace</span>
        <input
          v-model="draft.targetNamespace"
          class="k-input"
          placeholder="default"
          :aria-invalid="targetNamespaceError ? 'true' : undefined"
          :aria-describedby="targetNamespaceError ? 'marketplace-namespace-error' : 'marketplace-namespace-help'"
        />
        <span v-if="targetNamespaceError" id="marketplace-namespace-error" class="field-error" role="alert">{{ targetNamespaceError }}</span>
        <span v-else id="marketplace-namespace-help" class="field-help">Namespace on the edge cluster the chart renders into. It is created on each edge if missing and never deleted with the workload.</span>
      </label>
      <p class="muted">Deploys <span class="mono">{{ app.chart.chart }}@{{ app.chart.version }}</span> onto <b>{{ deployEdge || '—' }}</b> and wires an Edges Service on port {{ app.port }}. Auth: {{ credentialHint[app.credential] }}.</p>
      </div>
      <CreateGuidance
        title="Deploy this marketplace app"
        description="Review the pinned chart, target edge, and follow-up Service before starting the deployment."
        :prerequisites="workloadPrerequisites"
        :values="workloadGuidanceValues"
        :next-steps="workloadNextSteps"
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

    <form v-else class="k-create-surface k-create-surface--wide k-create-surface--guided" @submit.prevent="submit">
      <div class="k-create-body k-create-body--guided">
      <div class="k-create-fields">
      <label class="fld">
        <span class="lbl">Name</span>
        <input v-model="draft.name" class="k-input" placeholder="nginx-demo" />
      </label>
      <label class="fld">
        <span class="lbl">Image</span>
        <input v-model="draft.image" class="k-input" placeholder="nginx:latest" />
      </label>
      <label class="fld">
        <span class="lbl">Image pull secrets (comma-separated)</span>
        <input
          v-model="draft.imagePullSecrets"
          class="k-input"
          placeholder="ghcr-pull"
          :aria-invalid="pullSecretsError ? 'true' : undefined"
          :aria-describedby="pullSecretsError ? 'workload-pull-secrets-error' : 'workload-pull-secrets-help'"
        />
        <span v-if="pullSecretsError" id="workload-pull-secrets-error" class="field-error" role="alert">{{ pullSecretsError }}</span>
        <span v-else id="workload-pull-secrets-help" class="field-help">Names of docker-registry Secrets for a private image. Each Secret must already exist in the target namespace on every selected edge; the workload only references it and never carries the token.</span>
      </label>
      <div class="workload-create-grid">
        <label class="fld">
          <span class="lbl">Replicas</span>
          <input v-model="draft.replicas" type="number" min="1" class="k-input" />
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
        <span class="lbl">Target namespace</span>
        <input
          v-model="draft.targetNamespace"
          class="k-input"
          placeholder="default"
          :aria-invalid="targetNamespaceError ? 'true' : undefined"
          :aria-describedby="targetNamespaceError ? 'workload-namespace-error' : 'workload-namespace-help'"
        />
        <span v-if="targetNamespaceError" id="workload-namespace-error" class="field-error" role="alert">{{ targetNamespaceError }}</span>
        <span v-else id="workload-namespace-help" class="field-help">Namespace on the edge cluster the Deployment and Service land in. It is created on each edge if missing and never deleted with the workload.</span>
      </label>
      <label class="fld">
        <span class="lbl">Edge selector (key=value, comma-separated)</span>
        <input
          v-model="draft.selector"
          class="k-input"
          placeholder="env=dev"
          :aria-invalid="selectorError ? 'true' : undefined"
          :aria-describedby="selectorError ? 'workload-selector-error' : undefined"
        />
        <span v-if="selectorError" id="workload-selector-error" class="field-error" role="alert">{{ selectorError }}</span>
      </label>
      </div>
      <CreateGuidance
        title="Define the workload and placement"
        description="Choose the image and match labels to the Kubernetes edges that should run it."
        :prerequisites="workloadPrerequisites"
        :values="workloadGuidanceValues"
        :next-steps="workloadNextSteps"
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
