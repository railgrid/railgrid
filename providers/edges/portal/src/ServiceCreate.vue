<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ArrowLeft, Globe2, Loader2, Plus, Server } from 'lucide-vue-next'
import { createKubeEdgeService, fetchServiceCatalog, listEdges } from './api'
import type { CatalogEntry } from './api'
import type { Edge, EdgeServiceDraft, EdgeType, ErrorResponse } from './types'
import CreateGuidance, { type CreateGuidanceValue } from './portalkit/CreateGuidance.vue'
import FirstRunGuide from './portalkit/FirstRunGuide.vue'
import { toast } from './portalkit/toast'

const props = withDefaults(defineProps<{
  initialEdgeType?: EdgeType | null
  initialEdgeName?: string | null
}>(), {
  initialEdgeType: null,
  initialEdgeName: null,
})
const emit = defineEmits<{
  cancel: []
  created: [name: string]
  connectEdge: []
}>()

const catalog = ref<CatalogEntry[]>([])
const edges = ref<Edge[]>([])
const loading = ref(true)
const busy = ref(false)
const error = ref<string | null>(null)
const selectedEdgeKey = ref('')
let active = true

function catalogFor(type?: string): CatalogEntry | undefined {
  return catalog.value.find((entry) => entry.type === type)
}

const catalogGroups = computed(() => catalog.value.reduce<{ category: string; items: CatalogEntry[] }[]>((groups, entry) => {
  const category = entry.category || 'Other'
  let group = groups.find((item) => item.category === category)
  if (!group) {
    group = { category, items: [] }
    groups.push(group)
  }
  group.items.push(entry)
  return groups
}, []))

const genericFallback: CatalogEntry = {
  type: 'generic',
  displayName: 'Generic HTTP service',
  category: 'Other',
  defaultPort: 80,
  defaultScheme: 'http',
  auth: 'bearer',
  credential: { optional: true, packing: 'single', fields: [{ key: 'token', label: 'Bearer token (optional)', secret: true }] },
}

const draft = ref<EdgeServiceDraft>({
  name: '',
  edgeName: '',
  serviceType: 'home-assistant',
  targetNamespace: '',
  targetName: '',
  scheme: 'http',
  host: '',
  port: 8123,
  instructions: '',
})
const targetMode = ref<'host' | 'kube'>('host')

function edgeKey(edge: Pick<Edge, 'type' | 'name'>): string {
  return `${edge.type}/${edge.name}`
}

const selectedEdge = computed(() => edges.value.find((edge) => edgeKey(edge) === selectedEdgeKey.value))
const selectedEdgeIsServer = computed(() => selectedEdge.value?.type === 'server')
const selectedCatalogEntry = computed(() => catalogFor(draft.value.serviceType))
const hostRequired = computed(() => !!selectedCatalogEntry.value?.hostRequired)
const schemeLocked = computed(() => !!catalogFor(draft.value.serviceType)?.schemeLocked)

function resetTargetMode(): void {
  targetMode.value = selectedEdgeIsServer.value || hostRequired.value ? 'host' : 'kube'
}

function onEdgeChange(): void {
  draft.value.edgeName = selectedEdge.value?.name ?? ''
  resetTargetMode()
}

function onTypeChange(): void {
  const entry = catalogFor(draft.value.serviceType)
  if (entry?.defaultPort) draft.value.port = entry.defaultPort
  if (entry?.defaultScheme) draft.value.scheme = entry.defaultScheme
  resetTargetMode()
}

function chooseInitialEdge(): void {
  const requested = props.initialEdgeName
    ? edges.value.find((edge) => edge.name === props.initialEdgeName && (!props.initialEdgeType || edge.type === props.initialEdgeType))
    : undefined
  const matchingType = props.initialEdgeType
    ? edges.value.find((edge) => edge.type === props.initialEdgeType)
    : undefined
  const initial = requested || matchingType || edges.value[0]
  selectedEdgeKey.value = initial ? edgeKey(initial) : ''
  draft.value.edgeName = initial?.name ?? ''
  resetTargetMode()
}

function cancel(): void {
  if (busy.value) return
  emit('cancel')
}

// Accept a pasted URL for convenience, while keeping the API contract's host,
// scheme, and port fields separate. Service proxies always dial the root path.
function normalizeHost(value?: string): string {
  const raw = value?.trim() ?? ''
  if (!raw || !/^https?:\/\//i.test(raw)) return raw
  try {
    return new URL(raw).hostname.trim()
  } catch {
    return ''
  }
}

function applyHostUrl(): void {
  const raw = draft.value.host?.trim()
  if (!raw || !/^https?:\/\//i.test(raw)) {
    draft.value.host = raw || ''
    return
  }
  try {
    const url = new URL(raw)
    draft.value.scheme = url.protocol.replace(':', '')
    draft.value.host = url.hostname
    draft.value.port = Number(url.port) || (url.protocol === 'https:' ? 443 : 80)
  } catch {
    // Leave an invalid value visible for the user to correct.
  }
}

const normalizedHost = computed(() => normalizeHost(draft.value.host))
const serviceTypeLabel = computed(() => selectedCatalogEntry.value?.displayName || draft.value.serviceType || 'Not selected')
const endpointSummary = computed(() => {
  if (targetMode.value === 'host') {
    return `${draft.value.scheme || 'http'}://${normalizedHost.value || 'agent-loopback'}:${Number(draft.value.port) || 8123}`
  }
  const namespace = draft.value.targetNamespace.trim() || 'default'
  return `${namespace}/${draft.value.targetName.trim() || 'not-selected'}:${Number(draft.value.port) || 8123}`
})
const credentialSummary = computed(() => {
  const selected = selectedCatalogEntry.value
  if (!selected || selected.auth === 'none' || !selected.credential.fields?.length) return 'Not required'
  return selected.credential.optional ? 'Optional after creation' : 'Required after creation'
})
const serviceGuidanceValues = computed<CreateGuidanceValue[]>(() => [
  { label: 'Service name', value: draft.value.name.trim() || 'Not entered yet', technical: true },
  { label: 'Edge', value: selectedEdge.value?.name || 'Not selected', technical: true },
  { label: 'Service type', value: serviceTypeLabel.value },
  { label: 'Endpoint', value: endpointSummary.value, technical: true },
  { label: 'Credentials', value: credentialSummary.value },
  { label: 'MCP tools', value: selectedCatalogEntry.value?.tools?.length ? `${selectedCatalogEntry.value.tools.length} declared` : 'None declared' },
])
const servicePrerequisites = [
  'An edge that can reach this service.',
  'For a host target, a hostname or IP reachable from the agent; blank uses agent loopback when the type allows it.',
  'For a Kubernetes target, the Service namespace and name on the selected cluster.',
  'The protocol and port exposed by the target.',
]
const serviceNextSteps = [
  'Faros creates a cluster-scoped Service bound to the selected edge.',
  'The controller probes the endpoint and reports status separately.',
  'If authentication is required, add the credential on the Service detail page; Faros stores it in a workspace Secret.',
  'Provider-declared tools become available through the Edges MCP endpoint when the Service is ready.',
]
const canCreate = computed(() => {
  if (!draft.value.name.trim() || !selectedEdge.value) return false
  if (hostRequired.value) return targetMode.value === 'host' && !!normalizedHost.value
  return targetMode.value === 'host' || !!draft.value.targetName.trim()
})

async function onCreate(): Promise<void> {
  if (!canCreate.value) return
  const edge = selectedEdge.value
  if (!edge) return
  applyHostUrl()
  const byHost = targetMode.value === 'host'
  const host = byHost ? normalizeHost(draft.value.host) : ''
  if (hostRequired.value && (!byHost || !host)) return
  busy.value = true
  error.value = null
  const name = draft.value.name.trim()
  try {
    await createKubeEdgeService({
      name,
      edgeName: edge.name,
      edgeKind: edge.type === 'server' ? 'LinuxServer' : 'KubernetesCluster',
      serviceType: draft.value.serviceType,
      targetNamespace: draft.value.targetNamespace.trim() || 'default',
      targetName: byHost ? '' : draft.value.targetName.trim(),
      scheme: draft.value.scheme || 'http',
      host: byHost ? host || undefined : undefined,
      port: Number(draft.value.port) || 8123,
      instructions: draft.value.instructions?.trim() || undefined,
    })
    if (!active) return
    toast('info', `Service creation requested for ${name}.`)
    emit('created', name)
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Create failed'
  } finally {
    busy.value = false
  }
}

onMounted(async () => {
  loading.value = true
  const [catalogResult, edgesResult] = await Promise.allSettled([fetchServiceCatalog(), listEdges()])
  if (catalogResult.status === 'fulfilled' && catalogResult.value.length) {
    catalog.value = catalogResult.value
    if (!catalogFor(draft.value.serviceType)) draft.value.serviceType = catalogResult.value[0].type
  } else {
    catalog.value = [genericFallback]
    if (catalogResult.status === 'rejected') error.value = (catalogResult.reason as ErrorResponse)?.message ?? 'Failed to load service catalog'
  }
  if (edgesResult.status === 'fulfilled') {
    edges.value = edgesResult.value
    chooseInitialEdge()
  } else {
    error.value = (edgesResult.reason as ErrorResponse)?.message ?? 'Failed to load edges'
  }
  loading.value = false
})
onUnmounted(() => {
  active = false
})
</script>

<template>
  <div class="k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="busy" @click="cancel">
      <ArrowLeft :size="14" aria-hidden="true" /> Services
    </button>
    <header class="k-create-header">
      <h1 class="k-create-title">Create service</h1>
      <p class="k-create-description">Declare a service on an edge and expose its tools through the Edges MCP endpoint.</p>
    </header>

    <div v-if="error" class="banner error" role="alert">{{ error }}</div>
    <div v-if="loading" class="waiting" role="status" aria-live="polite">
      <Loader2 :size="14" class="spin" /> Loading service types and edges…
    </div>

    <FirstRunGuide
      v-else-if="!error && edges.length === 0"
      title="Connect an edge first"
      description="A Service must run beside an edge before Faros can expose its endpoint and tools."
      primary-label="Connect edge"
      :steps="[
        { label: 'Edge', description: 'Connect the cluster or server that can reach the service.' },
        { label: 'Service endpoint', description: 'Choose its type, address, protocol, and port.' },
        { label: 'Credentials and Ready', description: 'Add credentials when required and let the controller verify it.' },
      ]"
      journey-label="Service setup path"
      @primary="emit('connectEdge')"
    >
      <template #icon><Server aria-hidden="true" /></template>
    </FirstRunGuide>

    <form v-else class="k-create-surface k-create-surface--wide k-create-surface--guided" @submit.prevent="onCreate">
      <div class="k-create-body k-create-body--guided">
      <div class="k-create-fields">
      <div class="service-create-grid service-create-grid--two">
        <label class="fld">
          <span class="lbl">Name</span>
          <input v-model="draft.name" class="k-input" placeholder="home-assistant" />
        </label>
        <label class="fld">
          <span class="lbl">Edge</span>
          <select v-model="selectedEdgeKey" class="k-input" @change="onEdgeChange">
            <option value="" disabled>Select an edge</option>
            <option v-for="edge in edges" :key="edgeKey(edge)" :value="edgeKey(edge)">
              {{ edge.name }} ({{ edge.type === 'server' ? 'LinuxServer' : 'KubernetesCluster' }})
            </option>
          </select>
        </label>
      </div>

      <div class="service-create-grid service-create-grid--three">
        <label class="fld">
          <span class="lbl">Type</span>
          <select v-model="draft.serviceType" class="k-input" @change="onTypeChange">
            <optgroup v-for="group in catalogGroups" :key="group.category" :label="group.category">
              <option v-for="entry in group.items" :key="entry.type" :value="entry.type">{{ entry.displayName }}</option>
            </optgroup>
          </select>
        </label>
        <label class="fld">
          <span class="lbl">Scheme</span>
          <select v-model="draft.scheme" class="k-input" :disabled="schemeLocked" :title="schemeLocked ? 'Fixed by the service type' : ''">
            <option value="http">http</option>
            <option value="https">https</option>
          </select>
        </label>
        <label class="fld">
          <span class="lbl">Port</span>
          <input v-model="draft.port" type="number" min="1" max="65535" class="k-input" />
        </label>
      </div>

      <label class="fld">
        <span class="lbl">Target</span>
        <div class="service-create-target-modes">
          <label>
            <input v-model="targetMode" type="radio" value="host" /> <Globe2 :size="13" aria-hidden="true" /> Host / IP
          </label>
          <label :class="{ 'is-disabled': selectedEdgeIsServer || hostRequired }">
            <input v-model="targetMode" type="radio" value="kube" :disabled="selectedEdgeIsServer || hostRequired" /> Kubernetes Service
          </label>
        </div>
      </label>

      <div v-if="targetMode === 'host'" class="service-create-grid">
        <label class="fld">
          <span class="lbl">Host {{ catalogFor(draft.serviceType)?.hostRequired ? '(required)' : '(blank = agent loopback)' }}</span>
          <input v-model="draft.host" class="k-input" @blur="applyHostUrl" placeholder="192.168.1.1, myui.example.com, or paste https://myui.example.com" />
          <span v-if="catalogFor(draft.serviceType)?.hostHelp" class="muted" style="font-size: 12px; margin-top: 4px;">{{ catalogFor(draft.serviceType)?.hostHelp }}</span>
        </label>
      </div>
      <div v-else class="service-create-grid service-create-grid--two">
        <label class="fld">
          <span class="lbl">Target namespace</span>
          <input v-model="draft.targetNamespace" class="k-input" placeholder="home" />
        </label>
        <label class="fld">
          <span class="lbl">Target service name</span>
          <input v-model="draft.targetName" class="k-input" placeholder="home-assistant" />
        </label>
      </div>

      <label class="fld">
        <span class="lbl">AI instructions (optional)</span>
        <textarea v-model="draft.instructions" class="k-input" rows="3" placeholder="Gates are cover.gate_main. Living room light is light.living_room." />
      </label>

      </div>
      <CreateGuidance
        title="Describe the service endpoint"
        description="Choose where the service runs and how Edges reaches it. Credentials are added after creation only when the selected type requires them."
        :prerequisites="servicePrerequisites"
        :values="serviceGuidanceValues"
        :next-steps="serviceNextSteps"
      />
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--ghost" :disabled="busy" @click="cancel">Cancel</button>
        <button type="submit" class="k-btn k-btn--primary" :disabled="busy || !canCreate">
          <Loader2 v-if="busy" :size="14" class="spin" />
          <Plus v-else :size="14" aria-hidden="true" />
          {{ busy ? 'Creating…' : 'Create service' }}
        </button>
      </div>
    </form>
  </div>
</template>
