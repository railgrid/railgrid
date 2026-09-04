<script setup lang="ts">
import { ref, computed, onUnmounted, watch } from 'vue'
import { Globe2, KeyRound, Plug, RefreshCw, Save, Server } from 'lucide-vue-next'
import { confirmDialog } from './portalkit/confirm'
import { connectEdgeService, deleteEdgeService, getService, updateEdgeService } from './api'
import type { CatalogCredentialField, CatalogEntry, EdgeServiceEdit } from './api'
import type { Edge, EdgeService, ErrorResponse } from './types'
import ActionMenu, { type ActionMenuItem } from './portalkit/ActionMenu.vue'
import ConditionsPanel from './portalkit/ConditionsPanel.vue'
import ResourcePage from './portalkit/ResourcePage.vue'
import ResourceBackLink from './portalkit/ResourceBackLink.vue'
import ResourceSectionCard from './portalkit/ResourceSectionCard.vue'
import ResourceStatCards, { type ResourceStatCard, type ResourceStatTone } from './portalkit/ResourceStatCards.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import { toast } from './portalkit/toast'

// The route owns the resource identity. A list-row snapshot is optional and is
// used only as a seed; every instance page performs an exact getService read so
// a resource beyond the current cursor page remains directly loadable.
const props = withDefaults(defineProps<{
  service?: EdgeService | null
  serviceName?: string | null
  catalog: CatalogEntry[]
  edges: Edge[]
}>(), { service: null, serviceName: null })
const emit = defineEmits<{ back: []; saved: []; deleted: [] }>()

const service = ref<EdgeService | null>(props.service)
const readLoading = ref(true)
const readLoaded = ref(false)
const readError = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const busy = ref(false)
// Delete has a distinct lifecycle from ordinary save/connect mutations. Keep
// this state after the actions menu closes so the resource status and the
// announcement remain truthful until the parent navigates away on success.
const deleting = ref(false)
let selectionGeneration = 0
let selectedName = ''
let active = true

const currentName = computed(() => service.value?.name || props.serviceName || props.service?.name || '')
const title = computed(() => currentName.value || 'Service')
const actionItems = computed<ActionMenuItem[]>(() => [{
  id: 'delete',
  label: deleting.value ? 'Deleting service…' : 'Delete service',
  tone: 'danger',
  disabled: !service.value || busy.value || readLoading.value || deleting.value,
  busy: deleting.value,
}])

function selectAction(action: string): void {
  if (action === 'delete') void onDelete()
}

// Editable configuration, kept local so navigating between instance routes
// never serializes unfinished form values or credential text into the URL.
const form = ref<EdgeServiceEdit>({})
const instructions = ref('')
const targetMode = ref<'host' | 'kube'>('host')

function seedForm(next: EdgeService | null): void {
  form.value = next ? {
    serviceType: next.serviceType,
    scheme: next.scheme || 'http',
    port: next.port,
    host: next.host ?? '',
    targetNamespace: next.targetNamespace ?? '',
    targetName: next.targetName ?? '',
  } : {}
  instructions.value = next?.instructions ?? ''
  targetMode.value = next?.host ? 'host' : next?.targetName ? 'kube' : 'host'
}
seedForm(service.value)

// The catalog entry for the selected type drives provider information, scheme
// locking, target hints, and credential fields.
const entry = computed(() => props.catalog.find((c) => c.type === form.value.serviceType))
const schemeLocked = computed(() => !!entry.value?.schemeLocked)
const edgeIsServer = computed(() =>
  service.value?.edgeKind === 'LinuxServer' ||
  props.edges.find((e) => e.name === service.value?.edgeName)?.type === 'server',
)

function onTypeChange(): void {
  const catalogEntry = entry.value
  if (!catalogEntry) return
  if (catalogEntry.defaultPort) form.value.port = catalogEntry.defaultPort
  if (catalogEntry.defaultScheme) form.value.scheme = catalogEntry.defaultScheme
  if (catalogEntry.hostRequired) targetMode.value = 'host'
}

const AUTH_LABELS: Record<string, string> = {
  bearer: 'Bearer token',
  apiKeyHeader: 'API key (header)',
  apiKeyQuery: 'API key (query)',
  basic: 'Basic auth',
  proxmox: 'Proxmox API token',
  pihole: 'Session login',
  qbittorrent: 'Session login',
  none: 'No auth',
}
function authLabel(auth?: string): string {
  return AUTH_LABELS[auth ?? ''] ?? auth ?? '—'
}

function errorMessage(error: unknown, fallback: string): string {
  return (error as ErrorResponse)?.message ?? fallback
}

function isNotFound(error: unknown): boolean {
  return (error as ErrorResponse)?.reason === 'NotFound'
}

async function load(): Promise<void> {
  const name = currentName.value.trim()
  const generation = ++selectionGeneration
  const hadSnapshot = readLoaded.value && service.value !== null
  readLoading.value = true
  readError.value = null
  if (!name) {
    service.value = null
    readLoaded.value = false
    readError.value = 'This service link does not include a resource name.'
    readLoading.value = false
    return
  }
  try {
    const next = await getService(name)
    if (generation !== selectionGeneration) return
    service.value = next
    seedForm(next)
    readLoaded.value = true
  } catch (error) {
    if (generation !== selectionGeneration) return
    if (isNotFound(error)) {
      // A confirmed not-found must remove the previous snapshot so a stale
      // service cannot remain actionable under a missing URL-owned resource.
      service.value = null
      readLoaded.value = false
      readError.value = `Service "${name}" was not found in this workspace.`
    } else {
      readLoaded.value = hadSnapshot
      readError.value = errorMessage(error, 'Failed to load service')
    }
  } finally {
    if (generation === selectionGeneration) readLoading.value = false
  }
}

// Reuse the mounted editor as shell history moves between service/<name>
// routes. Only identity changes reset the form and read contract.
watch(
  () => [props.serviceName, props.service] as const,
  ([name, next]) => {
    const nextName = name || next?.name || ''
    if (nextName === selectedName) return
    selectedName = nextName
    service.value = next ?? null
    seedForm(service.value)
    readLoaded.value = false
    readError.value = null
    mutationError.value = null
    busy.value = false
    deleting.value = false
    void load()
  },
  { immediate: true },
)

const serviceStatus = computed(() => {
  if (deleting.value) return 'Deleting'
  if (!readLoaded.value) {
    if (readLoading.value) return 'Loading'
    if (readError.value) return 'Unavailable'
  }
  if (service.value?.phase) return service.value.phase
  return 'Pending'
})

function statusTone(status: string): 'success' | 'warning' | 'danger' | 'muted' {
  switch (status.toLowerCase()) {
    case 'ready': return 'success'
    case 'unreachable':
    case 'unavailable':
    case 'failed':
    case 'error': return 'danger'
    case 'loading':
    case 'detected':
    case 'pending':
    case 'deleting': return 'warning'
    default: return 'muted'
  }
}
const targetSummary = computed(() => {
  const value = service.value
  if (!value) return '—'
  const host = value.host?.trim()
  const targetName = value.targetName?.trim()
  const target = host || (targetName
    ? `${value.targetNamespace ? `${value.targetNamespace}/` : ''}${targetName}`
    : 'Agent loopback')
  return value.port ? `${target}:${value.port}` : target
})

// The catalog distinguishes a service that has no credential mechanism from a
// service that supports an optional credential. Keep those separate from a
// required credential so an optional, unconfigured service is not presented as
// unhealthy while its credential form remains available.
const credentialsSupported = computed(() => {
  const auth = (entry.value?.auth ?? '').toLowerCase()
  return !!entry.value && auth !== '' && auth !== 'none'
})
const credentialsOptional = computed(() => credentialsSupported.value && !!entry.value?.credential.optional)
const credentialsRequired = computed(() => credentialsSupported.value && !credentialsOptional.value)

const credentialState = computed(() => {
  if (!credentialsSupported.value) {
    return { value: 'Not required', detail: 'No credentials required', tone: 'default' as const }
  }
  if (service.value?.hasCredentials) {
    return { value: 'Configured', detail: 'Credentials configured', tone: 'success' as const }
  }
  if (credentialsOptional.value) {
    return { value: 'Not configured (optional)', detail: 'Optional credential', tone: 'default' as const }
  }
  if (credentialsRequired.value) {
    return { value: 'Missing', detail: 'Credentials missing', tone: 'warning' as const }
  }
  return { value: 'Not required', detail: 'No credentials required', tone: 'default' as const }
})

const serviceStatTone = computed<ResourceStatTone>(() => {
  const tone = statusTone(serviceStatus.value)
  if (tone === 'danger') return tone
  if (credentialState.value.tone === 'warning') return 'warning'
  return tone === 'muted' ? 'default' : tone
})

const credentialDescription = computed(() => {
  if (!credentialsSupported.value) return 'This service does not require credentials.'
  if (credentialsOptional.value) {
    return 'Credentials are optional. Set one when this service requires authentication; the value is written to a workspace Secret and is never displayed after submission.'
  }
  return 'Set or update the credential used to authenticate this service. The value is written to a workspace Secret and is never displayed after submission.'
})

const serviceStatCards = computed<ResourceStatCard[]>(() => [
  {
    id: 'status',
    label: 'Status',
    value: serviceStatus.value,
    detail: credentialState.value.detail,
    icon: Plug,
    tone: serviceStatTone.value,
  },
  { id: 'edge', label: 'Edge', value: service.value?.edgeName || '—', icon: Server, mono: true },
  { id: 'target', label: 'Target', value: targetSummary.value, icon: Globe2, mono: true },
])

async function refreshDetail(): Promise<void> {
  if (busy.value) return
  await load()
}

async function onSaveConfig(): Promise<void> {
  const name = currentName.value
  if (!name || !service.value) return
  const generation = selectionGeneration
  busy.value = true
  mutationError.value = null
  try {
    const byHost = targetMode.value === 'host'
    await updateEdgeService(name, {
      serviceType: form.value.serviceType,
      scheme: form.value.scheme,
      port: Number(form.value.port) || service.value.port,
      host: byHost ? form.value.host?.trim() : undefined,
      targetNamespace: form.value.targetNamespace,
      targetName: byHost ? '' : form.value.targetName,
      instructions: instructions.value,
      targetMode: targetMode.value,
    })
    if (!active || generation !== selectionGeneration || currentName.value !== name) return
    toast('ok', `Service configuration saved for ${name}.`)
    await load()
    emit('saved')
  } catch (error) {
    mutationError.value = errorMessage(error, 'Save failed')
  } finally {
    busy.value = false
  }
}

// Credential inputs are packed into the single token Secret by the API. The
// existing Secret value is never read or rendered; only hasCredentials and the
// catalog's field labels/hints are exposed here.
const credInputs = ref<Record<string, string>>({})
const credFields = computed<CatalogCredentialField[]>(() =>
  entry.value?.credential.fields ?? [{ key: 'token', label: 'Credential', secret: true }],
)
function packedCredential(): string {
  if (entry.value?.credential.packing === 'userpass') {
    const username = (credInputs.value.username ?? '').trim()
    const password = credInputs.value.password ?? ''
    return `${username}:${password}`
  }
  const key = credFields.value[0]?.key ?? 'token'
  return (credInputs.value[key] ?? '').trim()
}
const credFilled = computed(() => {
  if (entry.value?.credential.packing === 'userpass') {
    return !!(credInputs.value.username?.trim() && credInputs.value.password)
  }
  return !!packedCredential()
})

async function onSaveCreds(): Promise<void> {
  const name = currentName.value
  const token = packedCredential()
  if (!credentialsSupported.value || !name || !service.value || !token) return
  const generation = selectionGeneration
  busy.value = true
  mutationError.value = null
  try {
    await connectEdgeService(name, token)
    if (!active || generation !== selectionGeneration || currentName.value !== name) return
    credInputs.value = {}
    toast('ok', `Service credentials saved for ${name}.`)
    await load()
    emit('saved')
  } catch (error) {
    mutationError.value = errorMessage(error, 'Connect failed')
  } finally {
    busy.value = false
  }
}

async function onDelete(): Promise<void> {
  const name = currentName.value
  if (!name || !service.value || busy.value || deleting.value) return
  if (!(await confirmDialog({
    title: `Delete service "${name}"?`,
    message: 'Its MCP tools stop being exposed.',
    danger: true,
    confirmLabel: 'Delete',
  }))) return
  deleting.value = true
  busy.value = true
  mutationError.value = null
  const generation = selectionGeneration
  try {
    await deleteEdgeService(name)
    if (!active || generation !== selectionGeneration || currentName.value !== name) return
    toast('info', `Service deletion requested for ${name}.`)
    emit('deleted')
  } catch (error) {
    mutationError.value = errorMessage(error, 'Delete failed')
    deleting.value = false
  } finally {
    busy.value = false
  }
}

onUnmounted(() => {
  active = false
  selectionGeneration += 1
})
</script>

<template>
  <div class="service-detail">
    <ResourceBackLink class="service-detail__back" href="/ui/providers/edges/services" @back="emit('back')">
      Services
    </ResourceBackLink>
    <div class="service-detail__resource">
      <ResourcePage
        class="service-detail__page"
        :title="title"
        kind="Service"
        :subtitle="entry?.displayName || service?.serviceType || 'Edge service'"
        :loaded="readLoaded"
        :loading="readLoading"
        :error="readError"
        :stale="readLoaded && !!readError"
        retryable
        @retry="refreshDetail"
      >
        <template #meta>
          <span>Edges</span>
          <span class="service-detail__separator" aria-hidden="true">·</span>
          <span class="mono">{{ service?.edgeName || 'Edge unavailable' }}</span>
        </template>
        <template #status>
          <StatusBadge :status="serviceStatus" :tone="statusTone(serviceStatus)" />
        </template>
        <template #actions>
          <div class="service-detail__actions" role="group" aria-label="Service actions">
            <button class="k-btn k-btn--ghost" type="button" :disabled="busy || readLoading" :aria-busy="readLoading || undefined" @click="refreshDetail">
              <RefreshCw :size="14" :class="{ spin: readLoading }" aria-hidden="true" />
              {{ readLoading ? 'Refreshing…' : 'Refresh' }}
            </button>
            <ActionMenu
              label="More service actions"
              :items="actionItems"
              :disabled="!service || busy || readLoading || deleting"
              @select="selectAction"
            />
          </div>
        </template>

        <template #summary>
          <ResourceStatCards class="service-detail__stats" :cards="serviceStatCards" density="compact" aria-label="Service summary" />
        </template>

        <template #body>
          <p v-if="mutationError" class="banner error service-detail__mutation-error" role="alert" aria-live="assertive">{{ mutationError }}</p>
          <p v-if="deleting" class="waiting" role="status" aria-live="polite">Deleting this service. The last successful snapshot remains visible until the hub confirms removal.</p>
          <div class="service-detail__sections">
            <ResourceSectionCard class="service-detail__card" id="service-provider" eyebrow="Provider" title="Provider info" description="The catalog definition determines authentication, reachability, and exposed AI tools.">
              <div class="service-detail__facts">
                <div><span class="lbl">Type</span><strong>{{ entry?.displayName || service?.serviceType || '—' }}</strong></div>
                <div><span class="lbl">Auth</span><StatusBadge :status="authLabel(entry?.auth)" tone="muted" /></div>
                <div><span class="lbl">Default port</span><strong class="mono">{{ entry?.defaultPort ?? '—' }}</strong></div>
                <div><span class="lbl">Default scheme</span><strong class="mono">{{ entry?.defaultScheme || 'http' }}{{ entry?.schemeLocked ? ' (fixed)' : '' }}</strong></div>
                <div><span class="lbl">Reachability</span><strong>{{ entry?.hostRequired ? 'LAN host (required)' : targetMode === 'kube' ? 'Kubernetes Service' : 'Agent loopback' }}</strong></div>
              </div>
              <p v-if="entry?.description" class="service-detail__description">{{ entry.description }}</p>
              <div v-if="entry?.tools?.length" class="service-detail__tools">
                <span class="lbl">Exposed AI tools</span>
                <ul>
                  <li v-for="tool in entry.tools" :key="tool.name"><span class="mono">{{ tool.name }}</span><span v-if="tool.description" class="muted"> — {{ tool.description }}</span></li>
                </ul>
              </div>
              <p v-else class="muted service-detail__empty-tools">Proxy-only — this service exposes no AI tools.</p>
            </ResourceSectionCard>

            <ResourceSectionCard class="service-detail__card" id="service-configuration" eyebrow="Desired state" title="Configuration" description="Update the service type, endpoint, and AI guidance without exposing stored credentials.">
              <div class="service-detail__form-grid service-detail__form-grid--three">
                <label class="fld"><span class="lbl">Type</span>
                  <select v-model="form.serviceType" class="k-input" :disabled="busy || !service" @change="onTypeChange">
                    <option v-if="service && !props.catalog.some((item) => item.type === form.serviceType)" :value="form.serviceType">{{ form.serviceType }}</option>
                    <option v-for="catalogEntry in props.catalog" :key="catalogEntry.type" :value="catalogEntry.type">{{ catalogEntry.displayName }}</option>
                  </select>
                </label>
                <label class="fld"><span class="lbl">Scheme</span>
                  <select v-model="form.scheme" class="k-input" :disabled="busy || !service || schemeLocked" :title="schemeLocked ? 'Fixed by the service type' : ''"><option value="http">http</option><option value="https">https</option></select>
                </label>
                <label class="fld"><span class="lbl">Port</span><input v-model="form.port" type="number" min="1" max="65535" class="k-input" :disabled="busy || !service" /></label>
              </div>
              <div class="service-detail__target-mode" role="group" aria-label="Service target mode">
                <span class="lbl">Target</span>
                <label><input v-model="targetMode" type="radio" value="host" :disabled="busy || !service" /> Host / IP</label>
                <label :class="{ 'is-disabled': edgeIsServer }"><input v-model="targetMode" type="radio" value="kube" :disabled="busy || !service || edgeIsServer" /> Kubernetes Service</label>
              </div>
              <div v-if="targetMode === 'host'" class="service-detail__form-grid">
                <label class="fld"><span class="lbl">Host {{ entry?.hostRequired ? '(required)' : '(blank = agent loopback)' }}</span><input v-model="form.host" class="k-input" :disabled="busy || !service" placeholder="192.168.1.1, myui.example.com" /><span v-if="entry?.hostHelp" class="muted service-detail__field-help">{{ entry.hostHelp }}</span></label>
              </div>
              <div v-else class="service-detail__form-grid service-detail__form-grid--two">
                <label class="fld"><span class="lbl">Target namespace</span><input v-model="form.targetNamespace" class="k-input" :disabled="busy || !service" placeholder="home" /></label>
                <label class="fld"><span class="lbl">Target service name</span><input v-model="form.targetName" class="k-input" :disabled="busy || !service" placeholder="home-assistant" /></label>
              </div>
              <label class="fld"><span class="lbl">AI instructions (optional)</span><textarea v-model="instructions" class="k-input" rows="3" :disabled="busy || !service" placeholder="Describe this service's entities/rooms so the AI knows your setup."></textarea></label>
              <div class="service-detail__form-actions"><button class="k-btn k-btn--primary" type="button" :disabled="busy || !service" @click="onSaveConfig"><Save :size="14" aria-hidden="true" /> Save configuration</button></div>
            </ResourceSectionCard>

            <ResourceSectionCard class="service-detail__card" id="service-credentials" eyebrow="Access" title="Credentials" :description="credentialDescription">
              <template v-if="credentialsSupported">
                <p class="muted service-detail__credential-hint">{{ entry?.credential.hint || 'Credential' }}{{ credentialsOptional ? ' (optional)' : '' }} — makes the service Ready when authentication is required.</p>
                <div class="service-detail__credential-row">
                  <label v-for="field in credFields" :key="field.key" class="fld"><span class="lbl">{{ field.label }}</span><input v-model="credInputs[field.key]" :type="field.secret ? 'password' : 'text'" class="k-input" :disabled="busy || !service" :placeholder="field.label" autocomplete="new-password" /><span v-if="field.help" class="muted service-detail__field-help">{{ field.help }}</span></label>
                  <button class="k-btn k-btn--ghost" type="button" :disabled="busy || !service || !credFilled" @click="onSaveCreds"><KeyRound :size="14" aria-hidden="true" /> {{ service?.hasCredentials ? 'Update' : 'Set' }} credentials</button>
                </div>
              </template>
              <p v-else class="muted service-detail__credential-none">No credentials required for this service.</p>
            </ResourceSectionCard>

            <ResourceSectionCard class="service-detail__card" id="service-status" eyebrow="Diagnostics" title="Status" description="Controller observations and the current service endpoint.">
              <div class="service-detail__status-row"><StatusBadge :status="serviceStatus" :tone="statusTone(serviceStatus)" /><span v-if="service?.version" class="muted">Version <span class="mono">{{ service.version }}</span></span><span v-if="service?.installType" class="muted">Install <span class="mono">{{ service.installType }}</span></span></div>
              <a v-if="service?.url" class="service-detail__url mono" :href="service.url" target="_blank" rel="noopener">{{ service.url }}</a>
              <ConditionsPanel :conditions="service?.conditions || []" empty-text="No conditions reported yet." />
            </ResourceSectionCard>
          </div>
        </template>
      </ResourcePage>
    </div>
  </div>
</template>
