<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ArrowLeft, Loader2, Rocket } from 'lucide-vue-next'
import { createWorkload, deployMarketplaceApp, listEdges, type WorkloadDraft } from './api'
import { MARKETPLACE, type MarketplaceApp } from './marketplace'
import type { Edge, ErrorResponse } from './types'

const props = defineProps<{
  mode: 'manual' | 'marketplace'
  appType?: string
}>()
const emit = defineEmits<{
  cancel: []
  completed: [message: string]
}>()

const edges = ref<Edge[]>([])
const loading = ref(true)
const busy = ref(false)
const error = ref<string | null>(null)
const draft = ref({
  name: '',
  image: 'nginx:latest',
  replicas: 1,
  strategy: 'Spread' as 'Spread' | 'Singleton',
  selector: 'env=dev',
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

function parseSelector(value: string): Record<string, string> {
  const selector: Record<string, string> = {}
  for (const pair of value.split(',')) {
    const [key, val] = pair.split('=').map((part) => part.trim())
    if (key && val) selector[key] = val
  }
  return selector
}

const canSubmit = computed(() => {
  if (!active || loading.value || busy.value) return false
  if (props.mode === 'marketplace') {
    return !!app.value && !!deployName.value.trim() && kubernetesEdges.value.some((edge) => edge.name === deployEdge.value)
  }
  return !!draft.value.name.trim() && !!draft.value.image.trim()
})

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
      selector: parseSelector(draft.value.selector),
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

onMounted(async () => {
  const generation = lifecycleGeneration
  const result = await listEdges().then((value) => ({ value }), (reason) => ({ reason }))
  if (!isCurrent(generation)) return
  if ('value' in result) {
    edges.value = result.value
    if (props.mode === 'marketplace' && app.value) {
      deployName.value = app.value.type
    }
    deployEdge.value = (props.mode === 'marketplace' ? kubernetesEdges.value : edges.value)[0]?.name ?? ''
  } else {
    error.value = (result.reason as ErrorResponse)?.message ?? 'Failed to load edges'
  }
  loading.value = false
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
      <Loader2 :size="14" class="spin" /> Loading edges…
    </div>

    <form v-else-if="app && props.mode === 'marketplace'" class="k-create-surface k-create-surface--wide" @submit.prevent="submit">
      <div class="k-create-body">
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
      <p v-if="kubernetesEdges.length === 0" class="banner warn" role="status">Connect a KubernetesCluster edge before deploying a marketplace app.</p>
      <p class="muted">Deploys <span class="mono">{{ app.chart.chart }}@{{ app.chart.version }}</span> onto <b>{{ deployEdge || '—' }}</b> and wires an Edges Service on port {{ app.port }}. Auth: {{ credentialHint[app.credential] }}.</p>
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--ghost" :disabled="busy" @click="cancel">Cancel</button>
        <button type="submit" class="k-btn k-btn--primary" :disabled="!canSubmit">
          <Loader2 v-if="busy" :size="14" class="spin" />
          <Rocket v-else :size="14" aria-hidden="true" />
          {{ busy ? 'Deploying…' : 'Deploy' }}
        </button>
      </div>
    </form>

    <div v-else-if="props.mode === 'marketplace'" class="k-create-surface">
      <div class="k-create-body">
      <p class="banner error" role="alert">That marketplace app is not available.</p>
      </div>
      <div class="k-create-actions"><button class="k-btn k-btn--ghost" :disabled="busy" @click="cancel">Back to workloads</button></div>
    </div>

    <form v-else class="k-create-surface k-create-surface--wide" @submit.prevent="submit">
      <div class="k-create-body">
      <label class="fld">
        <span class="lbl">Name</span>
        <input v-model="draft.name" class="k-input" placeholder="nginx-demo" />
      </label>
      <label class="fld">
        <span class="lbl">Image</span>
        <input v-model="draft.image" class="k-input" placeholder="nginx:latest" />
      </label>
      <div class="row" style="gap: 12px; align-items: flex-start;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Replicas</span>
          <input v-model="draft.replicas" type="number" min="1" class="k-input" />
        </label>
        <label class="fld" style="flex: 1;">
          <span class="lbl">Strategy</span>
          <select v-model="draft.strategy" class="k-input">
            <option value="Spread">Spread (all matching edges)</option>
            <option value="Singleton">Singleton (one edge)</option>
          </select>
        </label>
      </div>
      <label class="fld">
        <span class="lbl">Edge selector (key=value, comma-separated)</span>
        <input v-model="draft.selector" class="k-input" placeholder="env=dev" />
      </label>
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--ghost" :disabled="busy" @click="cancel">Cancel</button>
        <button type="submit" class="k-btn k-btn--primary" :disabled="!canSubmit">
          <Loader2 v-if="busy" :size="14" class="spin" />
          {{ busy ? 'Creating…' : 'Create workload' }}
        </button>
      </div>
    </form>
  </div>
</template>
