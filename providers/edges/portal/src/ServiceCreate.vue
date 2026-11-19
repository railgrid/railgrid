<script setup lang="ts">
import LoadingSkeleton from './portalkit/LoadingSkeleton.vue'
import InlineNotification from './portalkit/InlineNotification.vue'
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { ArrowLeft, Globe2, Loader2, Plus, Server } from 'lucide-vue-next'
import { createKubeEdgeService, fetchServiceCatalog, listEdges } from './api'
import type { CatalogEntry } from './api'
import type { Edge, EdgeServiceDraft, EdgeType, ErrorResponse } from './types'
import CreateGuidance, { type CreateGuidanceValue } from './portalkit/CreateGuidance.vue'
import FirstRunGuide from './portalkit/FirstRunGuide.vue'
import { toast } from './portalkit/toast'
import { parseServiceHost, servicePortError } from './serviceValidation'
import { track, trackFailure } from './telemetry'

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
const readError = ref<string | null>(null)
const portError = ref<string | null>(null)
const selectedEdgeKey = ref('')
const heading = ref<HTMLElement | null>(null)
let readingInputs = false
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
const selectedEdgeIsHost = computed(() => selectedEdge.value?.type !== 'kubernetes')
const selectedCatalogEntry = computed(() => catalogFor(draft.value.serviceType))
const hostRequired = computed(() => !!selectedCatalogEntry.value?.hostRequired)
const schemeLocked = computed(() => !!catalogFor(draft.value.serviceType)?.schemeLocked)
const parsedHost = computed(() => parseServiceHost(draft.value.host, schemeLocked.value ? selectedCatalogEntry.value?.defaultScheme : undefined))
const hostInputError = computed(() => targetMode.value === 'host' ? parsedHost.value.error || '' : '')
const targetRestriction = computed(() => !selectedEdge.value ? '' : hostRequired.value
  ? 'This service type requires a host or IP address.'
  : selectedEdgeIsHost.value ? 'Kubernetes Service targets require a Kubernetes edge.' : '')

function resetTargetMode(): void {
  targetMode.value = selectedEdgeIsHost.value || hostRequired.value ? 'host' : 'kube'
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
  const parsed = parseServiceHost(value)
  return parsed.error ? '' : parsed.host
}

function applyHostUrl(): void {
  if (hostInputError.value) return
  const parsed = parsedHost.value
  draft.value.host = parsed.host
  if (parsed.scheme) draft.value.scheme = parsed.scheme
  if (parsed.port) draft.value.port = parsed.port
}

const normalizedHost = computed(() => normalizeHost(draft.value.host))
const endpointSummary = computed(() => {
  if (hostInputError.value) return 'Correct the host URL'
  const port = targetMode.value === 'host' ? parsedHost.value.port ?? draft.value.port : draft.value.port
  if (servicePortError(port)) return 'Enter a valid port'
  if (targetMode.value === 'host') {
    if (hostRequired.value && !normalizedHost.value) return 'Host required'
    const scheme = parsedHost.value.scheme || draft.value.scheme || 'http'
    return `${scheme}://${normalizedHost.value || 'agent-loopback'}:${Number(port)}`
  }
  const namespace = draft.value.targetNamespace.trim() || 'default'
  return `${namespace}/${draft.value.targetName.trim() || 'not-selected'}:${Number(port)}`
})
const credentialSummary = computed(() => {
  const selected = selectedCatalogEntry.value
  if (!selected || selected.auth === 'none' || !selected.credential.fields?.length) return 'Not required'
  return selected.credential.optional ? 'Optional after creation' : 'Required after creation'
})
const serviceGuidanceValues = computed<CreateGuidanceValue[]>(() => [
  { label: 'Endpoint', value: endpointSummary.value, technical: true },
  { label: 'Credentials', value: credentialSummary.value },
])
const canCreate = computed(() => {
  if (!draft.value.name.trim() || !selectedEdge.value || hostInputError.value) return false
  if (hostRequired.value) return targetMode.value === 'host' && !!normalizedHost.value
  return targetMode.value === 'host' || !!draft.value.targetName.trim()
})

async function onCreate(): Promise<void> {
  if (busy.value || loading.value || !canCreate.value) return
  const edge = selectedEdge.value
  if (!edge) return
  if (targetMode.value === 'host') applyHostUrl()
  portError.value = servicePortError(draft.value.port)
  if (portError.value) {
    document.getElementById('service-create-port')?.focus()
    return
  }
  const byHost = targetMode.value === 'host'
  const host = byHost ? normalizeHost(draft.value.host) : ''
  if (hostRequired.value && (!byHost || !host)) return
  busy.value = true
  error.value = null
  const name = draft.value.name.trim()
  // The type is one of this bundle's own catalog entries, never free text.
  const labels = { type: catalogFor(draft.value.serviceType)?.type ?? 'other', kind: edge.type, target: byHost ? 'host' : 'kube' }
  try {
    await createKubeEdgeService({
      name,
      edgeName: edge.name,
      edgeKind: edge.type === 'server' ? 'LinuxServer' : edge.type === 'macos' ? 'MacOSServer' : 'KubernetesCluster',
      serviceType: draft.value.serviceType,
      targetNamespace: draft.value.targetNamespace.trim() || 'default',
      targetName: byHost ? '' : draft.value.targetName.trim(),
      scheme: draft.value.scheme || 'http',
      host: byHost ? host || undefined : undefined,
      port: Number(draft.value.port),
      instructions: draft.value.instructions?.trim() || undefined,
    })
    track('service_created', labels)
    if (!active) return
    toast('info', `Service creation requested for ${name}.`)
    emit('created', name)
  } catch (e) {
    trackFailure('service_create_failed', e, labels)
    error.value = (e as ErrorResponse)?.message ?? 'Create failed'
  } finally {
    busy.value = false
  }
}

async function loadInputs(): Promise<void> {
  if (readingInputs || busy.value) return
  readingInputs = true
  loading.value = true
  const focused = typeof document !== 'undefined' ? document.activeElement : null
  const firstCatalogLoad = catalog.value.length === 0
  const [catalogResult, edgesResult] = await Promise.allSettled([fetchServiceCatalog(), listEdges()])
  if (!active) return
  readError.value = null
  if (catalogResult.status === 'fulfilled' && catalogResult.value.length) {
    catalog.value = catalogResult.value
    if (!catalogFor(draft.value.serviceType)) {
      draft.value.serviceType = catalogResult.value[0].type
      onTypeChange()
    } else if (firstCatalogLoad) onTypeChange()
  } else {
    if (!catalog.value.length) catalog.value = [genericFallback]
    if (!catalogFor(draft.value.serviceType)) {
      draft.value.serviceType = genericFallback.type
      onTypeChange()
    }
    if (catalogResult.status === 'rejected') readError.value = (catalogResult.reason as ErrorResponse)?.message ?? 'Failed to load service types'
  }
  if (edgesResult.status === 'fulfilled') {
    edges.value = edgesResult.value
    if (!edges.value.some(edge => edgeKey(edge) === selectedEdgeKey.value)) chooseInitialEdge()
  } else {
    readError.value = (edgesResult.reason as ErrorResponse)?.message ?? 'Failed to load edges'
  }
  loading.value = false
  readingInputs = false
  await nextTick()
  if (focused && !focused.isConnected && document.activeElement === document.body) heading.value?.focus()
}
onMounted(loadInputs)
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
      <h1 ref="heading" tabindex="-1" class="k-create-title">Create service</h1>
      <p class="k-create-description">Declare a service on an edge and expose its tools through the Edges MCP endpoint.</p>
    </header>

    <InlineNotification v-if="error" tone="error" :message="error" />
    <InlineNotification v-if="readError" tone="error">
      <div class="service-create-recovery">
        <span>{{ readError }}</span>
        <button
          type="button"
          class="k-btn k-btn--ghost"
          :aria-disabled="loading || busy || undefined"
          :aria-busy="loading || undefined"
          @click="loadInputs"
        >
          {{ loading ? 'Retrying service types and edges…' : 'Retry service types and edges' }}
        </button>
      </div>
    </InlineNotification>
    <LoadingSkeleton v-if="loading" label="Loading service types and edges…" />

    <FirstRunGuide
      v-else-if="!error && !readError && edges.length === 0"
      title="Connect an edge first"
      description="A Service must run beside an edge before Railgrid can expose its endpoint and tools."
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

    <form v-else class="k-create-surface k-create-surface--wide k-create-surface--guided" :aria-busy="busy" novalidate @submit.prevent="onCreate">
      <div class="k-create-body k-create-body--guided">
      <div class="k-create-fields">
      <div class="service-create-grid service-create-grid--two">
        <label class="fld">
          <span class="lbl">Name (required)</span>
          <input v-model="draft.name" class="k-input" required placeholder="home-assistant" />
        </label>
        <label class="fld">
          <span class="lbl">Edge (required)</span>
          <select v-model="selectedEdgeKey" class="k-input" required aria-describedby="service-create-edge-help" @change="onEdgeChange">
            <option value="" disabled>Select an edge</option>
            <option v-for="edge in edges" :key="edgeKey(edge)" :value="edgeKey(edge)">
              {{ edge.name }} ({{ edge.type === 'server' ? 'LinuxServer' : edge.type === 'macos' ? 'MacOSServer' : 'KubernetesCluster' }})
            </option>
          </select>
          <span id="service-create-edge-help" class="field-help">Choose an edge that can reach this service.</span>
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
        <div class="fld">
          <label for="service-create-scheme" class="lbl">Scheme</label>
          <select id="service-create-scheme" v-model="draft.scheme" class="k-input" :disabled="schemeLocked" :aria-describedby="schemeLocked ? 'service-create-scheme-help' : undefined">
            <option value="http">http</option>
            <option value="https">https</option>
          </select>
          <span v-if="schemeLocked" id="service-create-scheme-help" class="field-help">Fixed by the selected service type.</span>
        </div>
        <div class="fld">
          <label for="service-create-port" class="lbl">Port (required)</label>
          <input id="service-create-port" v-model="draft.port" type="number" min="1" max="65535" class="k-input" required :aria-invalid="portError ? 'true' : undefined" :aria-describedby="portError ? 'service-create-port-error' : undefined" @input="portError = null" />
          <span v-if="portError" id="service-create-port-error" class="error" role="alert">{{ portError }}</span>
        </div>
      </div>

      <fieldset class="fld service-target-fieldset" :aria-describedby="targetRestriction ? 'service-create-target-help' : undefined">
        <legend class="lbl">Target</legend>
        <div class="service-create-target-modes">
          <label class="k-checkbox-hit">
            <input v-model="targetMode" class="k-checkbox" name="service-create-target-mode" type="radio" value="host" /> <Globe2 :size="13" aria-hidden="true" /> Host / IP
          </label>
          <label class="k-checkbox-hit" :class="{ 'is-disabled': selectedEdgeIsHost || hostRequired }">
            <input v-model="targetMode" class="k-checkbox" name="service-create-target-mode" type="radio" value="kube" :disabled="selectedEdgeIsHost || hostRequired" /> Kubernetes Service
          </label>
        </div>
        <p v-if="targetRestriction" id="service-create-target-help" class="field-help">{{ targetRestriction }}</p>
      </fieldset>

      <div v-if="targetMode === 'host'" class="service-create-grid">
        <label class="fld">
          <span id="service-create-host-label" class="lbl">Host {{ catalogFor(draft.serviceType)?.hostRequired ? '(required)' : '(blank = agent loopback)' }}</span>
          <input v-model="draft.host" class="k-input" :required="hostRequired" aria-labelledby="service-create-host-label" :aria-invalid="hostInputError ? 'true' : undefined" :aria-describedby="hostInputError ? 'service-create-host-help service-create-host-error' : 'service-create-host-help'" @blur="applyHostUrl" placeholder="192.168.1.1, myui.example.com, or paste https://myui.example.com" />
          <span id="service-create-host-help" class="field-help">{{ catalogFor(draft.serviceType)?.hostHelp || 'Use a host or IP reachable from the agent. Leave blank to use agent loopback when allowed.' }} A pasted root URL sets the scheme and port.</span>
          <span v-if="hostInputError" id="service-create-host-error" class="error" role="alert">{{ hostInputError }}</span>
        </label>
      </div>
      <div v-else class="service-create-grid service-create-grid--two">
        <label class="fld">
          <span class="lbl">Target namespace (optional)</span>
          <input v-model="draft.targetNamespace" class="k-input" placeholder="home" />
        </label>
        <label class="fld">
          <span class="lbl">Target service name (required)</span>
          <input v-model="draft.targetName" class="k-input" required placeholder="home-assistant" />
        </label>
      </div>

      <label class="fld">
        <span class="lbl">AI instructions (optional)</span>
        <textarea v-model="draft.instructions" class="k-input" rows="3" placeholder="Gates are cover.gate_main. Living room light is light.living_room." />
      </label>

      </div>
      <CreateGuidance
        title="Before you create"
        description="Railgrid will bind this service to the selected edge and check its endpoint. Add any required credentials on the service detail page after creation."
        values-heading="Connection"
        :values="serviceGuidanceValues"
      />
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--ghost" :disabled="busy" @click="cancel">Cancel</button>
        <button type="submit" class="k-btn k-btn--primary" :disabled="busy || !canCreate">
          <Loader2 v-if="busy" :size="14" class="k-spin" aria-hidden="true" />
          <Plus v-else :size="14" aria-hidden="true" />
          {{ busy ? 'Creating…' : 'Create service' }}
        </button>
      </div>
    </form>
  </div>
</template>
