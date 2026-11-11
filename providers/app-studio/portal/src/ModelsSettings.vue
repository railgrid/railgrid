<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'
import { ArrowLeft, Check, Cpu, Loader2, Play, Plus, RefreshCw, Star } from 'lucide-vue-next'
import ResourceTableEditButton from './portalkit/ResourceTableEditButton.vue'
import ResourceTableDeleteButton from './portalkit/ResourceTableDeleteButton.vue'
import ResourceTableActionButton from './portalkit/ResourceTableActionButton.vue'
import ResourceTable from './portalkit/ResourceTable.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import PortalTabs from './portalkit/Tabs.vue'
import ModelCollection from './agentkit/ModelCollection.vue'
import ActionMenu from './portalkit/ActionMenu.vue'
import ResourceTableExpandButton from './portalkit/ResourceTableExpandButton.vue'
import ModelIDSelector from './agentkit/ModelIDSelector.vue'
import { ensureAgentUIStyles } from './agentkit/styles'
import ModelUsageSection from './agentkit/ModelUsageSection.vue'
import ReasoningEffortControl from './ReasoningEffortControl.vue'
import FirstRunGuide from './portalkit/FirstRunGuide.vue'
import InlineNotification from './portalkit/InlineNotification.vue'
import { toast } from './portalkit/toast'
import { api, ProjectAPIRequestError } from './api'
import { llmProviderSelection, type LLMProviderPreset } from './llmDiscovery'
import type {
  ProjectLLMDiscoveredModel,
  ProjectLLMModelSettings,
  ProjectLLMSettings,
  ProjectModelProviderConnection,
  ProjectModelProviderConnectionWrite,
  ProjectModelProviderConnectionDraft,
  ProjectLLMCredentialMode,
  RailgridContext,
} from './types'

ensureAgentUIStyles()

const providerOptions = [
  { value: 'openai', label: 'OpenAI' },
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'openrouter', label: 'OpenRouter' },
  { value: 'custom', label: 'Custom OpenAI-compatible' },
  { value: 'google', label: 'Google AI Studio / Vertex AI' },
] as const

const props = withDefaults(defineProps<{
  ctx: RailgridContext | null
  routePage?: boolean
  providerConnectionsRoute?: boolean
  usageRoute?: boolean
  providerDetourActive?: boolean
  connections?: ProjectModelProviderConnection[]
  connectionsLoading?: boolean
  connectionsError?: string | null
  connectionStatusChecking?: boolean
  connectionStatusTimedOut?: boolean
  modelTests?: Record<string, { state: string; tone: 'success' | 'danger' | 'muted'; error?: string }>
  settings: ProjectLLMSettings | null
  loading: boolean
  loadError: string | null
  saving: boolean
  actionError: string | null
  editorOpen: boolean
  creationRoute: boolean
  editingModelID: string | null
  connectionRef: string
  name: string
  model: string
  reasoningEffort?: string
  reasoningEffortOptions?: string[]
  reasoningEffortError?: string
  nameError: string
  modelError: string
  modelHint: string
  discoveredModels: ProjectLLMDiscoveredModel[]
  canDiscover?: boolean
  discoveryComplete?: boolean
  discoveryLoading: boolean
  discoveryError: string | null
  testing: boolean
  testStatus: string | null
  testError: string | null
  requireConnectionTest: boolean
  nameOnlyEdit: boolean
}>(), {
  routePage: false,
  providerConnectionsRoute: false,
  usageRoute: false,
  providerDetourActive: false,
  connections: () => [],
  connectionsLoading: false,
  connectionsError: null,
  modelTests: () => ({}),
  reasoningEffort: '',
  reasoningEffortOptions: () => [],
  reasoningEffortError: '',
  nameOnlyEdit: false,
})

const emit = defineEmits<{
  retry: []
  navigate: [route: string]
  openEditor: [modelID?: string, connectionRef?: string, connectionInfo?: ProjectModelProviderConnection]
  beginProviderDetour: []
  continueModelWithProvider: [connection: ProjectModelProviderConnection]
  cancelProviderDetour: []
  cancelEditor: []
  save: []
  testSaved: [modelID: string]
  delete: [modelID: string]
  setDefault: [modelID: string]
  selectConnection: [connectionRef: string]
  discoverConnection: [connectionRef: string]
  selectDiscoveredModel: [model: ProjectLLMDiscoveredModel]
  connectionsChanged: [connection?: ProjectModelProviderConnection]
  retryConnectionStatus: [connectionRef: string]
  deleteConnection: [connection: ProjectModelProviderConnection]
  'update:name': [value: string]
  'update:model': [value: string]
  'update:reasoningEffort': [value: string]
}>()

const MODELS_ROUTE = '~models'
const USAGE_ROUTE = '~models/usage'
const setupTabs = [
  { id: 'models', label: 'Models' },
  { id: 'usage', label: 'Usage & cost' },
]
const providerConnectionsHeading = ref<HTMLHeadingElement | null>(null)
const modelDetailsPrefix = useId()
const expandedModel = ref<string | null>(null)
const pendingConnection = ref<ProjectModelProviderConnection | null>(null)
const connectionEditorOpen = ref(false)
const connectionStage = ref<'details' | 'verify'>('details')
const connectionOriginal = ref<ProjectModelProviderConnection | null>(null)
const connectionPreset = ref<LLMProviderPreset>('openai')
const connectionProvider = ref('openai-compatible')
const connectionBaseURL = ref('https://api.openai.com/v1')
const connectionCredentialMode = ref<ProjectLLMCredentialMode>('api-key')
const connectionWorkspaceID = ref('')
const connectionAPIKey = ref('')
const connectionDisplayName = ref('')
const connectionVerificationModel = ref('')
const connectionDiscoveredModels = ref<ProjectLLMDiscoveredModel[]>([])
const connectionDiscoverySucceeded = ref(false)
const connectionVerifiedFingerprint = ref('')
const connectionBusy = ref(false)
const connectionError = ref('')
const connectionNotice = ref('')
const connectionDraftRevision = ref(0)
const connectionHeadingRef = ref<HTMLHeadingElement | null>(null)
const connectionCredentialRef = ref<HTMLInputElement | HTMLTextAreaElement | null>(null)
let connectionRequestSerial = 0

const selectedConnection = computed(() => connectionRows.value.find(item => item.name === props.connectionRef) ?? null)
const selectedConnectionSnapshotReady = computed(() => {
  const connection = selectedConnection.value
  return Boolean(connection?.uid && !connection.activationPending && connection.secretName && connection.secretUID && connection.secretResourceVersion
    && connection.connectionFingerprint && connection.generation !== undefined
    && connection.observedGeneration === connection.generation)
})
const modelRows = computed(() => (props.settings?.models || []).map(saved => ({
  id: saved.id,
  name: saved.name,
  model: saved.model,
  endpoint: saved.baseURL || 'Provider default',
  providerConnection: saved.connectionRef
    ? connectionRows.value.find(connection => connection.name === saved.connectionRef)?.displayName
      || saved.connectionName || saved.connectionRef
    : 'Direct credential',
  configured: saved.configured,
  default: saved.default,
  status: props.modelTests?.[saved.id]?.state || 'Not tested',
  tone: props.modelTests?.[saved.id]?.tone,
  saved,
})))
const activeConnectionBindings = computed(() => (name: string) => (props.settings?.models ?? []).filter(model => model.connectionRef === name))
const connectionRows = computed(() => {
  const current = [...props.connections]
  return current.sort((a, b) => (a.displayName || a.name).localeCompare(b.displayName || b.name))
})
watch(() => props.connections, connections => {
  const saved = pendingConnection.value
  if (saved && !connections.some(connection => connection.name === saved.name && connection.uid === saved.uid && !connection.deleting)) {
    pendingConnection.value = null
  }
})
const connectionTableRows = computed(() => connectionRows.value.map(connection => {
  const bindingCount = activeConnectionBindings.value(connection.name).length
  const explicitDiscoveryRequired = connection.conditions?.some(condition => condition.type === 'Reachable'
    && condition.status === 'Unknown' && condition.reason === 'ExplicitDiscoveryRequired') ?? false
  const status = connection.activationPending ? 'Changes pending' : connection.ready
    ? 'Ready'
    : connection.secretResolved === false
      ? 'Credential missing'
      : connection.reachable === false
        ? 'Unreachable'
        : explicitDiscoveryRequired
          ? 'Not verified'
          : 'Checking'
  const tone = connection.activationPending ? 'warning' as const : connection.ready ? 'success' as const
    : connection.secretResolved === false || connection.reachable === false ? 'danger' as const
      : 'warning' as const
  return {
    id: connection.name,
    name: connection.displayName || connection.name,
    endpoint: connection.baseURL || 'Provider default endpoint',
    bindingCount,
    status,
    statusMessage: explicitDiscoveryRequired
      ? 'No provider check has been run yet. Discover models or test a model to verify this connection.'
      : undefined,
    tone,
    connection,
  }
}))
const providerConnectionColumns = [
  { key: 'name', label: 'Provider', primary: true, sortable: true },
  { key: 'endpoint', label: 'Endpoint' },
  { key: 'bindingCount', label: 'Model bindings', sortable: true },
  { key: 'status', label: 'Readiness', sortable: true },
  { key: 'actions', label: '', ariaLabel: 'Actions', align: 'end' as const },
]

function connectionFromTableRow(row: Record<string, unknown>): ProjectModelProviderConnection {
  return row.connection as ProjectModelProviderConnection
}

function connectionTableText(row: Record<string, unknown>, key: string): string {
  const value = row[key]
  return typeof value === 'string' ? value : ''
}

function connectionTableTone(row: Record<string, unknown>): 'success' | 'warning' | 'danger' {
  return row.tone === 'success' || row.tone === 'danger' ? row.tone : 'warning'
}

function connectionBindingCount(row: Record<string, unknown>): number {
  return typeof row.bindingCount === 'number' ? row.bindingCount : 0
}

const connectionEndpoint = computed(() => normalizedConnectionBaseURL())
function savedConnectionCredentialMode(saved: ProjectModelProviderConnection): ProjectLLMCredentialMode {
  return saved.credentialMode
    || (saved.provider === 'google-ai-studio' && saved.baseURL.includes('aiplatform.googleapis.com') ? 'service-account-json' : 'api-key')
}

function isConnectionNameOnlyEdit(
  saved: ProjectModelProviderConnection | null,
  provider: string,
  baseURL: string,
  workspaceID: string,
  credentialMode: ProjectLLMCredentialMode,
  apiKey: string,
  displayName: string,
  verificationModel: string,
): boolean {
  if (!saved || apiKey.trim() || verificationModel.trim()) return false
  return saved.provider === provider
    && saved.baseURL === baseURL
    && (saved.anthropicWorkspaceID || '') === workspaceID.trim()
    && savedConnectionCredentialMode(saved) === credentialMode
    && displayName.trim() !== (saved.displayName || '')
}

const connectionNameOnlyEdit = computed(() => isConnectionNameOnlyEdit(
  connectionOriginal.value,
  connectionProvider.value,
  connectionEndpoint.value,
  connectionWorkspaceID.value,
  connectionCredentialMode.value,
  connectionAPIKey.value,
  connectionDisplayName.value,
  connectionVerificationModel.value,
))
const connectionHasReusableKey = computed(() => {
  const saved = connectionOriginal.value
  return Boolean(saved?.connectionFingerprint && saved.observedGeneration === saved.generation
    && saved.secretResolved !== false
    && saved.provider === connectionProvider.value
    && saved.baseURL === connectionEndpoint.value
    && (saved.anthropicWorkspaceID || '') === connectionWorkspaceID.value.trim())
})
const connectionBaseURLError = computed(() => {
  if (!connectionEndpoint.value) return 'Enter an API endpoint.'
  try {
    const parsed = new URL(connectionEndpoint.value)
    return parsed.protocol === 'https:' || parsed.protocol === 'http:' ? '' : 'Use an HTTP or HTTPS endpoint.'
  } catch {
    return 'Enter a valid API endpoint.'
  }
})
const connectionCanContinue = computed(() => !connectionBusy.value && (connectionNameOnlyEdit.value
  || (!connectionBaseURLError.value && (Boolean(connectionAPIKey.value.trim()) || connectionHasReusableKey.value))))
const connectionDraftFingerprint = computed(() => String(connectionDraftRevision.value))
const connectionVerified = computed(() => connectionVerifiedFingerprint.value === connectionDraftFingerprint.value)
const connectionVerificationWillCallModel = computed(() => {
  const saved = connectionOriginal.value
  const dependentModels = saved ? activeConnectionBindings.value(saved.name) : []
  const verifyingConfig = !saved || connectionHasConfigChanges.value || Boolean(connectionAPIKey.value.trim())
  return Boolean((dependentModels.length && verifyingConfig)
    || (connectionVerificationModel.value.trim() && (verifyingConfig || !connectionVerified.value)))
})
const connectionHasConfigChanges = computed(() => {
  const saved = connectionOriginal.value
  return Boolean(saved && (saved.provider !== connectionProvider.value || saved.baseURL !== connectionEndpoint.value
    || (saved.anthropicWorkspaceID || '') !== connectionWorkspaceID.value.trim()
    || savedConnectionCredentialMode(saved) !== connectionCredentialMode.value))
})

function normalizedConnectionBaseURL(): string {
  const trimmed = connectionBaseURL.value.trim().replace(/\/+$/, '')
  if (connectionProvider.value === 'google-ai-studio' && connectionCredentialMode.value === 'service-account-json') {
    if (!trimmed || trimmed === 'https://generativelanguage.googleapis.com') return 'https://aiplatform.googleapis.com'
  }
  return trimmed
}

function providerPresetFor(provider: string, baseURL: string): LLMProviderPreset {
  const normalized = provider.trim().toLowerCase()
  if (normalized === 'google-ai-studio') return 'google'
  const endpoint = baseURL.trim().replace(/\/+$/, '').toLowerCase()
  if (endpoint === 'https://api.anthropic.com/v1') return 'anthropic'
  if (endpoint === 'https://openrouter.ai/api/v1') return 'openrouter'
  if (endpoint === 'https://api.openai.com/v1') return 'openai'
  return 'custom'
}

function connectionDisplayNameForSave(
  displayName: string,
  editing: boolean,
  preset: LLMProviderPreset,
  connections: ProjectModelProviderConnection[],
): string {
  const entered = displayName.trim()
  if (entered || editing) return entered
  const brand = preset === 'anthropic'
    ? 'Anthropic'
    : providerOptions.find(option => option.value === preset)?.label || 'OpenAI-compatible'
  const taken = new Set(connections.map(connection => (connection.displayName || connection.name).trim().toLowerCase()))
  let candidate = brand
  for (let suffix = 2; taken.has(candidate.toLowerCase()); suffix += 1) candidate = `${brand} ${suffix}`
  return candidate
}

function invalidateConnectionProbe() {
  connectionRequestSerial += 1
  connectionDraftRevision.value += 1
  connectionBusy.value = false
  connectionDiscoverySucceeded.value = false
  connectionDiscoveredModels.value = []
  connectionVerifiedFingerprint.value = ''
  connectionNotice.value = ''
  connectionError.value = ''
}

function openConnectionEditor(saved?: ProjectModelProviderConnection) {
  invalidateConnectionProbe()
  connectionOriginal.value = saved ?? null
  pendingConnection.value = null
  connectionPreset.value = saved ? providerPresetFor(saved.provider, saved.baseURL) : 'openai'
  const selection = saved ? { provider: saved.provider, baseURL: saved.baseURL } : llmProviderSelection('openai')
  connectionProvider.value = selection.provider
  connectionBaseURL.value = selection.baseURL
  connectionCredentialMode.value = saved ? savedConnectionCredentialMode(saved) : 'api-key'
  connectionWorkspaceID.value = saved?.anthropicWorkspaceID || ''
  connectionAPIKey.value = ''
  connectionDisplayName.value = saved?.displayName || ''
  connectionVerificationModel.value = ''
  connectionDiscoveredModels.value = saved?.models ?? []
  connectionDiscoverySucceeded.value = false
  connectionStage.value = saved ? 'verify' : 'details'
  connectionEditorOpen.value = true
}

function closeConnectionEditor() {
  if (connectionBusy.value) return
  invalidateConnectionProbe()
  connectionEditorOpen.value = false
  connectionOriginal.value = null
  connectionAPIKey.value = ''
  if (props.providerDetourActive) emit('cancelProviderDetour')
}

watch(connectionEditorOpen, open => {
  if (open) void nextTick(() => connectionHeadingRef.value?.focus({ preventScroll: true }))
  else if (!props.providerDetourActive) void focusProviderConnections()
})

async function focusProviderConnections() {
  await nextTick()
  providerConnectionsHeading.value?.focus()
}

watch(() => props.providerConnectionsRoute, active => {
  if (active && !props.providerDetourActive) void focusProviderConnections()
  if (!active && connectionEditorOpen.value) {
    invalidateConnectionProbe()
    connectionEditorOpen.value = false
    connectionOriginal.value = null
    connectionAPIKey.value = ''
  }
}, { immediate: true, flush: 'post' })

watch(() => props.usageRoute, active => {
  if (!active) return
  invalidateConnectionProbe()
  connectionEditorOpen.value = false
  connectionOriginal.value = null
  connectionAPIKey.value = ''
  pendingConnection.value = null
}, { flush: 'sync' })

function chooseConnectionProvider(value: string) {
  const preset = value as LLMProviderPreset
  if (preset === connectionPreset.value) return
  connectionPreset.value = preset
  const selection = llmProviderSelection(preset, connectionBaseURL.value)
  connectionProvider.value = selection.provider
  connectionBaseURL.value = selection.baseURL
  connectionCredentialMode.value = 'api-key'
  connectionAPIKey.value = ''
  connectionWorkspaceID.value = ''
  invalidateConnectionProbe()
}

function updateConnectionBaseURL(value: string) {
  if (value !== connectionBaseURL.value) connectionAPIKey.value = ''
  connectionBaseURL.value = value
  invalidateConnectionProbe()
}

function updateConnectionCredentialMode(value: string) {
  const mode = value as ProjectLLMCredentialMode
  if (mode === connectionCredentialMode.value) return
  connectionCredentialMode.value = mode
  connectionAPIKey.value = ''
  if (mode === 'service-account-json' && connectionProvider.value === 'google-ai-studio'
    && (!connectionBaseURL.value || connectionBaseURL.value === 'https://generativelanguage.googleapis.com')) {
    connectionBaseURL.value = 'https://aiplatform.googleapis.com'
  }
  if (mode === 'api-key' && connectionProvider.value === 'google-ai-studio'
    && connectionBaseURL.value === 'https://aiplatform.googleapis.com') {
    connectionBaseURL.value = 'https://generativelanguage.googleapis.com'
  }
  invalidateConnectionProbe()
}

function updateConnectionWorkspaceID(value: string) {
  if (value !== connectionWorkspaceID.value) connectionAPIKey.value = ''
  connectionWorkspaceID.value = value
  invalidateConnectionProbe()
}

function draftProbeBody(): ProjectModelProviderConnectionDraft {
  const saved = connectionOriginal.value
  const canReuse = connectionHasReusableKey.value && !connectionAPIKey.value.trim()
  return {
    provider: connectionProvider.value,
    baseURL: connectionEndpoint.value,
    credentialMode: connectionCredentialMode.value,
    anthropicWorkspaceID: connectionWorkspaceID.value.trim(),
    ...(connectionAPIKey.value.trim() ? { apiKey: connectionAPIKey.value } : {}),
    ...(canReuse && saved ? { existingConnectionID: saved.name, connectionFingerprint: saved.connectionFingerprint } : {}),
  }
}

async function continueConnection() {
  connectionError.value = ''
  connectionNotice.value = ''
  if (!connectionCanContinue.value) {
    connectionError.value = connectionBaseURLError.value || 'Enter an API key or refresh the saved connection status before continuing.'
    return
  }
  if (connectionCredentialMode.value === 'service-account-json') {
    connectionStage.value = 'verify'
    connectionNotice.value = 'Enter a Vertex AI model ID below, then verify the provider key.'
    return
  }
  const serial = ++connectionRequestSerial
  const fingerprint = connectionDraftFingerprint.value
  connectionBusy.value = true
  try {
    const result = await api.discoverLLMModels(props.ctx, draftProbeBody())
    if (serial !== connectionRequestSerial || fingerprint !== connectionDraftFingerprint.value) return
    connectionDiscoveredModels.value = result.models
    connectionDiscoverySucceeded.value = true
    if (!connectionOriginal.value) {
      const body: ProjectModelProviderConnectionWrite = {
        provider: connectionProvider.value,
        baseURL: connectionEndpoint.value,
        anthropicWorkspaceID: connectionWorkspaceID.value.trim(),
        displayName: connectionDisplayNameForSave(connectionDisplayName.value, Boolean(connectionOriginal.value), connectionPreset.value, connectionRows.value),
        credentialMode: connectionCredentialMode.value,
        ...(connectionAPIKey.value.trim() ? { apiKey: connectionAPIKey.value } : {}),
      }
      const connected = await api.saveModelProviderConnection(props.ctx, body)
      if (serial !== connectionRequestSerial || fingerprint !== connectionDraftFingerprint.value) return
      acceptSavedConnection({ ...connected, credentialMode: connectionCredentialMode.value }, fingerprint)
      return
    }
    connectionNotice.value = result.models.length
      ? `${result.models.length} model${result.models.length === 1 ? '' : 's'} found. Choose one to verify it, or save the changes.`
      : 'Provider access confirmed. No models were listed; you can enter an exact model ID to verify it.'
    connectionStage.value = 'verify'
  } catch (error) {
    if (serial !== connectionRequestSerial || fingerprint !== connectionDraftFingerprint.value) return
    connectionDiscoverySucceeded.value = false
    if (error instanceof ProjectAPIRequestError && error.reason === 'DiscoveryUnsupported') {
      connectionError.value = ''
      connectionNotice.value = error.message
      connectionStage.value = 'verify'
      return
    }
    connectionError.value = error instanceof Error ? error.message : String(error)
    connectionNotice.value = ''
    connectionStage.value = 'details'
    void nextTick(() => connectionCredentialRef.value?.focus())
  } finally {
    if (serial === connectionRequestSerial) connectionBusy.value = false
  }
}

function acceptSavedConnection(connection: ProjectModelProviderConnection, fingerprint: string) {
  const editing = connectionOriginal.value !== null
  pendingConnection.value = connection.activationPending ? connection : null
  connectionAPIKey.value = ''
  connectionEditorOpen.value = false
  connectionOriginal.value = null
  connectionNotice.value = ''
  connectionError.value = ''
  // The parent adds the acknowledged connection before refreshing the list.
  connectionVerifiedFingerprint.value = fingerprint
  emit('connectionsChanged', connection)
  if (!connection.activationPending) toast('ok', editing ? 'Provider connection updated.' : 'Provider connected.')
  // A provider connection created from the model form is a detour in that
  // binding flow. Return to the draft as soon as the save is acknowledged.
  if (props.providerDetourActive) emit('continueModelWithProvider', connection)
}

function selectConnectionDiscoveryModel(model: ProjectLLMDiscoveredModel) {
  setConnectionVerificationModel(model.id)
}

function setConnectionVerificationModel(value: string) {
  if (value === connectionVerificationModel.value) return
  connectionVerificationModel.value = value
  connectionDraftRevision.value += 1
  connectionVerifiedFingerprint.value = ''
  connectionError.value = ''
}

async function verifyConnectionDraft(model: string, effort?: string): Promise<void> {
  const result = await api.testLLMConnection(props.ctx, {
    ...draftProbeBody(),
    model,
    ...(effort ? { reasoningEffort: effort } : {}),
  })
  if (!result.ok) throw new Error('The provider did not confirm this connection.')
}

async function verifyConnectionModels(
  dependentModels: Pick<ProjectLLMModelSettings, 'model' | 'reasoningEffort'>[],
  optionalModel: string,
  verifyOptional: boolean,
  verify: (model: string, effort?: string) => Promise<void>,
  isCurrent: () => boolean,
): Promise<number | null> {
  const tested = new Set<string>()
  for (const binding of dependentModels) {
    await verify(binding.model, binding.reasoningEffort)
    tested.add(`${binding.model}\u0000${binding.reasoningEffort ?? ''}`)
    if (!isCurrent()) return null
  }
  const selectedModel = optionalModel.trim()
  if (selectedModel && verifyOptional && !tested.has(`${selectedModel}\u0000`)) {
    await verify(selectedModel)
    tested.add(`${selectedModel}\u0000`)
    if (!isCurrent()) return null
  }
  return tested.size
}

async function saveProviderConnection() {
  connectionError.value = ''
  connectionNotice.value = ''
  const saved = connectionOriginal.value
  const dependentModels = saved ? activeConnectionBindings.value(saved.name) : []
  const typedKey = connectionAPIKey.value.trim()
  const nameOnlyEdit = connectionNameOnlyEdit.value
  const verifyingConfig = !saved || connectionHasConfigChanges.value || Boolean(typedKey)
  if (connectionStage.value !== 'verify') {
    await continueConnection()
    return
  }
  if (connectionBaseURLError.value && !nameOnlyEdit) {
    connectionError.value = connectionBaseURLError.value
    return
  }
  if (!typedKey && !connectionHasReusableKey.value && (connectionHasConfigChanges.value || saved) && !nameOnlyEdit) {
    connectionError.value = 'Enter the provider key again after changing its endpoint or workspace.'
    return
  }
  if (connectionCredentialMode.value === 'service-account-json' && !connectionVerificationModel.value.trim()
    && !dependentModels.length && (!connectionDiscoverySucceeded.value || verifyingConfig) && !nameOnlyEdit) {
    connectionError.value = 'Enter a Vertex AI model ID to verify this provider connection.'
    return
  }
  const requestSerial = ++connectionRequestSerial
  const fingerprint = connectionDraftFingerprint.value
  connectionBusy.value = true
  try {
    const optionalModel = connectionVerificationModel.value.trim()
    const verifiedModelCount = await verifyConnectionModels(
      verifyingConfig ? dependentModels : [],
      optionalModel,
      verifyingConfig || !connectionVerified.value,
      verifyConnectionDraft,
      () => requestSerial === connectionRequestSerial && fingerprint === connectionDraftFingerprint.value,
    )
    if (verifiedModelCount === null) return
    if ((!saved || verifyingConfig) && !connectionDiscoverySucceeded.value && verifiedModelCount === 0) {
      connectionError.value = 'Discover models or choose a verification model before saving this provider connection.'
      return
    }
    connectionVerifiedFingerprint.value = fingerprint
    const body: ProjectModelProviderConnectionWrite = {
      ...(saved ? { name: saved.name } : {}),
      provider: connectionProvider.value,
      baseURL: nameOnlyEdit && saved ? saved.baseURL : connectionEndpoint.value,
      anthropicWorkspaceID: nameOnlyEdit && saved ? saved.anthropicWorkspaceID || '' : connectionWorkspaceID.value.trim(),
      displayName: connectionDisplayNameForSave(connectionDisplayName.value, Boolean(saved), connectionPreset.value, connectionRows.value),
      credentialMode: connectionCredentialMode.value,
      ...(typedKey ? { apiKey: connectionAPIKey.value } : {}),
    }
    const result = await api.saveModelProviderConnection(props.ctx, body, saved || undefined)
    if (requestSerial !== connectionRequestSerial || fingerprint !== connectionDraftFingerprint.value) return
    acceptSavedConnection({ ...result, credentialMode: connectionCredentialMode.value }, fingerprint)
  } catch (error) {
    if (requestSerial !== connectionRequestSerial || fingerprint !== connectionDraftFingerprint.value) return
    connectionError.value = error instanceof Error ? error.message : String(error)
  } finally {
    if (requestSerial === connectionRequestSerial) connectionBusy.value = false
  }
}

function addModelFor(connection: ProjectModelProviderConnection) {
  emit('openEditor', undefined, connection.name, connection)
}

watch(() => [connectionProvider.value, connectionEndpoint.value, connectionWorkspaceID.value, connectionCredentialMode.value], () => {
  // Endpoint/provider/workspace edits invalidate the saved-key reuse state.
  connectionVerifiedFingerprint.value = ''
})

const legacyEditing = computed(() => Boolean(props.editingModelID && !props.connectionRef))
const modelConnectionOptions = computed(() => connectionRows.value.filter(connection => !connection.deleting))
const selectedModelRows = computed(() => props.discoveryComplete ? props.discoveredModels : selectedConnection.value?.models ?? props.discoveredModels)
const modelCanSave = computed(() => Boolean(props.nameOnlyEdit || (props.connectionRef && selectedConnectionSnapshotReady.value))
  && props.model.trim().length > 0
  && !props.saving && !props.testing && !props.reasoningEffortError)

const modelLimitReached = computed(() => (props.settings?.models.length ?? 0) >= 20)
const createActions = computed(() => [
  { id: 'model', label: 'Add model', description: 'Use a saved provider connection', disabled: modelLimitReached.value },
  { id: 'provider', label: 'Connect provider', description: 'Save API access for models' },
])
const showFirstRun = computed(() => !props.loading && !props.loadError && !props.connectionsLoading
  && !props.connectionsError && !modelRows.value.length && !connectionRows.value.length)

function selectCreateAction(id: string) {
  if (id === 'provider') openConnectionEditor()
  else emit('openEditor')
}

function toggleModelDetails(id: string) {
  expandedModel.value = expandedModel.value === id ? null : id
}

function startProviderDetour() {
  emit('beginProviderDetour')
  openConnectionEditor()
}

</script>

<template>
  <section class="grid gap-4" :aria-label="providerConnectionsRoute ? 'Provider connections' : creationRoute ? 'Add model' : 'Models'">
    <template v-if="routePage && !creationRoute && !editorOpen && !connectionEditorOpen">
      <header class="k-resource-page__header">
        <div class="k-resource-page__heading">
          <h2 class="k-resource-page__title">Setup</h2>
          <p class="k-resource-page__subtitle">Manage the models and provider access App Studio uses.</p>
        </div>
      </header>
      <PortalTabs class="k-tabs--compact" :tabs="setupTabs" :active="usageRoute ? 'usage' : 'models'" aria-label="Setup sections" @select="emit('navigate', $event === 'usage' ? USAGE_ROUTE : MODELS_ROUTE)" />
    </template>

    <InlineNotification v-if="loadError" tone="error" :message="loadError" action-label="Retry" :action-busy="loading" action-busy-label="Retrying models…" @action="emit('retry')" />
    <InlineNotification v-if="actionError" tone="error" :message="actionError" />

    <template v-if="usageRoute">
      <ModelUsageSection provider="App Studio" />
    </template>

    <template v-else-if="connectionEditorOpen">
      <div class="k-create-page">
        <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="connectionBusy" @click="closeConnectionEditor"><ArrowLeft class="h-3.5 w-3.5" :stroke-width="1.75" /> Back to Models</button>
        <header class="k-create-header">
          <h1 ref="connectionHeadingRef" class="k-create-title" tabindex="-1">{{ connectionOriginal ? 'Edit provider connection' : 'Connect provider' }}</h1>
          <p class="k-create-description">Save a provider credential once so multiple model configurations can reuse it.</p>
        </header>
        <form class="k-create-surface k-create-surface--wide k-model-form agents-provider-connect" aria-label="Provider connection form" novalidate :aria-busy="connectionBusy" @submit.prevent="connectionStage === 'details' ? continueConnection() : saveProviderConnection()">
          <div class="k-create-body">
            <section class="agents-provider-connect__section" aria-labelledby="provider-connect-heading">
              <h2 id="provider-connect-heading" class="k-model-form-heading">Provider connection</h2>
              <label for="provider-connection-name" class="k-model-form-field">Connection name (optional)
                <input id="provider-connection-name" class="k-input" :value="connectionDisplayName" maxlength="100" placeholder="For example, OpenAI production" :disabled="connectionBusy" @input="connectionDisplayName = ($event.target as HTMLInputElement).value; connectionVerifiedFingerprint = ''">
                <span class="k-model-form-hint">This label identifies the saved provider credential, not a model.</span>
              </label>
              <label class="k-model-form-field"><span id="connection-provider-label">Provider</span>
                <select id="model-provider" class="k-input" :value="connectionPreset" aria-labelledby="connection-provider-label" :disabled="connectionBusy" @change="chooseConnectionProvider(($event.target as HTMLSelectElement).value)"><option v-for="option in providerOptions" :key="option.value" :value="option.value">{{ option.label }}</option></select>
                <span class="k-model-form-hint">{{ connectionPreset === 'custom' ? 'Use an OpenAI-compatible API endpoint. You can enter a model ID if the provider does not list models.' : 'The endpoint is selected for this provider. You can review it before connecting.' }}</span>
              </label>
              <label v-if="connectionPreset === 'custom' || connectionCredentialMode === 'service-account-json'" class="k-model-form-field">API endpoint
                <input id="model-base-url" class="k-input min-w-0 font-mono text-[12px]" type="url" autocomplete="url" :value="connectionBaseURL" placeholder="https://your-provider.example/v1" :disabled="connectionBusy" :required="connectionPreset === 'custom'" :aria-required="connectionPreset === 'custom'" :aria-invalid="Boolean(connectionBaseURLError)" aria-describedby="model-base-url-help" @input="updateConnectionBaseURL(($event.target as HTMLInputElement).value)">
                <span id="model-base-url-help" class="k-model-form-hint">{{ connectionCredentialMode === 'service-account-json' ? 'Use the Vertex AI endpoint for this service account.' : 'Use the provider API endpoint.' }}</span>
              </label>
              <details v-else class="agents-provider-connect__details"><summary>Connection details</summary><p class="k-model-form-hint break-all">API endpoint: <span class="font-mono">{{ connectionEndpoint }}</span></p></details>
            </section>
            <section class="k-model-form-section" aria-labelledby="provider-key-heading">
              <div class="k-model-form-row"><h2 id="provider-key-heading" class="k-model-form-heading">{{ connectionCredentialMode === 'service-account-json' ? 'Service account JSON' : 'API key' }}</h2><label v-if="connectionPreset === 'google'" class="k-model-form-field max-w-xs">Credential method
                <select id="model-credential-method" class="k-input" :value="connectionCredentialMode" :disabled="connectionBusy" @change="updateConnectionCredentialMode(($event.target as HTMLSelectElement).value)"><option value="api-key">Gemini API key</option><option value="service-account-json">Vertex AI service account</option></select>
              </label></div>
              <label for="model-credential" class="sr-only">{{ connectionCredentialMode === 'service-account-json' ? 'Service account JSON' : 'API key' }}</label>
              <textarea v-if="connectionCredentialMode === 'service-account-json'" id="model-credential" ref="connectionCredentialRef" class="k-input min-h-[132px] resize-y font-mono text-[12px] leading-5" :value="connectionAPIKey" autocomplete="new-password" :placeholder="connectionNameOnlyEdit ? 'Leave blank to save the label only' : connectionOriginal && connectionHasReusableKey ? 'Leave blank to keep the saved key' : 'Paste the complete service-account JSON'" :disabled="connectionBusy" :required="!connectionHasReusableKey && !connectionNameOnlyEdit" @input="connectionAPIKey = ($event.target as HTMLTextAreaElement).value; invalidateConnectionProbe()" />
              <input v-else id="model-credential" ref="connectionCredentialRef" class="k-input" type="password" :value="connectionAPIKey" autocomplete="new-password" :placeholder="connectionNameOnlyEdit ? 'Leave blank to save the label only' : connectionOriginal && connectionHasReusableKey ? 'Leave blank to keep the saved key' : 'Paste your API key'" :disabled="connectionBusy" :required="!connectionHasReusableKey && !connectionNameOnlyEdit" @input="connectionAPIKey = ($event.target as HTMLInputElement).value; invalidateConnectionProbe()">
              <p class="k-model-form-hint">Stored in this workspace and never returned to the browser. Models on this connection reuse this key. Changing the provider, endpoint, credential method, or Anthropic workspace clears a typed key.</p>
              <label v-if="connectionPreset === 'anthropic'" class="k-model-form-field">Anthropic workspace ID (if required by your key)
                <input id="anthropic-workspace-id" class="k-input" :value="connectionWorkspaceID" placeholder="wrkspc_…" :disabled="connectionBusy" @input="updateConnectionWorkspaceID(($event.target as HTMLInputElement).value)">
                <span class="k-model-form-hint">Leave blank for a workspace-scoped key. Re-enter the key when changing its workspace.</span>
              </label>
              <p v-if="connectionOriginal && !connectionAPIKey && !connectionHasReusableKey && !connectionNameOnlyEdit" class="k-model-form-hint" role="status">Checking whether the saved credential can be reused. Refresh connections if this takes longer than a moment.</p>
            </section>
            <InlineNotification v-if="connectionError" tone="error" :message="connectionError" />
            <p v-else-if="connectionNotice" class="k-model-form-hint" role="status" aria-live="polite">{{ connectionNotice }}</p>
            <section v-if="connectionStage === 'verify'" class="k-model-form-section" aria-labelledby="provider-model-heading">
              <div class="k-model-form-row"><h2 id="provider-model-heading" class="k-model-form-heading">Verify provider access</h2><button type="button" class="k-btn k-btn--ghost" :disabled="connectionBusy || !connectionCanContinue || connectionCredentialMode === 'service-account-json'" @click="continueConnection"><RefreshCw class="h-3.5 w-3.5" :stroke-width="1.75" /> Refresh models</button></div>
              <label for="model-id" class="k-model-form-field">Verification model (optional for providers with model discovery)
                <ModelIDSelector :model-value="connectionVerificationModel" :models="connectionDiscoveredModels" :disabled="connectionBusy" described-by="provider-model-help" @update:model-value="setConnectionVerificationModel($event)" @select="selectConnectionDiscoveryModel" />
              </label>
              <p id="provider-model-help" class="k-model-form-hint">{{ connectionOriginal && activeConnectionBindings(connectionOriginal.name).length ? `Saving changes will verify all ${activeConnectionBindings(connectionOriginal.name).length} model bindings that use this connection.` : connectionCredentialMode === 'service-account-json' ? 'Enter an exact Vertex AI model ID to send a short verification request.' : 'Discovery checks provider access. You can also select a model to send a short request before saving.' }}</p>
              <p v-if="connectionVerificationWillCallModel" class="k-model-form-hint">The provider may charge for each model verification request.</p>
              <p v-if="connectionVerified" class="k-model-form-hint" role="status"><Check class="mr-1 inline h-3.5 w-3.5" /> Provider checks passed. You can save this connection.</p>
            </section>
          </div>
          <footer class="k-create-actions agents-provider-connect__actions">
            <p class="k-model-form-hint agents-provider-connect__charge">{{ connectionStage === 'details' ? connectionOriginal ? 'Continue discovers available models without sending a model request or saving changes.' : 'Discovery checks provider access without sending a model request or adding a model binding. A successful provider connection is saved.' : connectionVerificationWillCallModel ? 'Verify sends the selected model request before saving the provider connection.' : 'Discovery checks provider access before saving. No model request will be sent.' }}</p>
            <button type="button" class="k-btn k-btn--ghost" :disabled="connectionBusy" @click="closeConnectionEditor">Cancel</button>
            <button v-if="connectionStage === 'details'" type="submit" class="k-btn k-btn--primary" :disabled="!connectionCanContinue"><Loader2 v-if="connectionBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />{{ connectionBusy ? connectionOriginal ? 'Checking provider…' : 'Verifying & connecting…' : connectionOriginal ? 'Continue' : 'Verify & connect' }}</button>
            <button v-else type="submit" class="k-btn k-btn--primary" :disabled="connectionBusy || !connectionCanContinue || (connectionCredentialMode === 'service-account-json' && !connectionVerificationModel.trim() && !connectionNameOnlyEdit)"><Loader2 v-if="connectionBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />{{ connectionBusy ? 'Verifying…' : connectionNameOnlyEdit || connectionOriginal && !connectionAPIKey && !connectionHasConfigChanges ? 'Save changes' : connectionOriginal ? 'Verify & save' : 'Verify & connect' }}</button>
          </footer>
        </form>
      </div>
    </template>

    <template v-else-if="!editorOpen && !creationRoute">
      <header class="k-resource-page__header">
        <div class="k-resource-page__heading">
          <h3 class="k-resource-page__title">Available models</h3>
          <p class="k-resource-page__subtitle">Models you can use to create and chat in projects.</p>
        </div>
        <div class="k-resource-page__actions">
          <ActionMenu label="Create" primary-menu :items="createActions" :disabled="saving || connectionsLoading" @select="selectCreateAction" />
        </div>
      </header>

      <FirstRunGuide v-if="showFirstRun" title="Connect a provider to add models" description="Connect a provider once, then reuse it across models in App Studio." primary-label="Connect provider" :steps="[
        { label: 'Connect a provider', description: 'Save a workspace credential.' },
        { label: 'Add a model', description: 'Choose a model on the saved connection.' },
        { label: 'Use it in projects', description: 'Select a model when creating or chatting.' },
      ]" @primary="openConnectionEditor()"><template #icon><Cpu :stroke-width="1.75" /></template></FirstRunGuide>
      <div v-else-if="loading && !settings && !loadError" class="grid min-h-48 content-start gap-3 rounded-lg border border-dashed border-border-subtle bg-surface p-4" role="status" aria-live="polite" aria-busy="true">
        <div class="shimmer h-4 w-36 rounded bg-surface-overlay" /><div class="shimmer h-24 w-full rounded bg-surface-overlay" /><div class="text-[12px] text-text-muted">Loading models…</div>
      </div>
      <div v-else>
        <div v-if="loading" class="flex items-center gap-2 text-[11px] text-text-muted" role="status" aria-live="polite" aria-busy="true"><Loader2 class="h-3.5 w-3.5 animate-spin text-accent" :stroke-width="1.75" /> Refreshing models…</div>
        <InlineNotification v-if="testError" tone="error" :message="testError" />
        <InlineNotification v-else-if="testStatus" tone="success" :message="testStatus" />
        <ModelCollection v-if="modelRows.length" class="setup-table" :rows="modelRows" preference-key="railgrid:app-studio:models:layout" table-only show-provider-connection>
          <template #name-prefix="{ row }">
            <ResourceTableExpandButton
              :expanded="expandedModel === row.id"
              :label="`Show details for ${row.name}`"
              :expanded-label="`Hide details for ${row.name}`"
              :controls="`${modelDetailsPrefix}-${row.id}`"
              @click="toggleModelDetails(row.id)"
            />
          </template>
          <template #after-row="{ row, columnCount }">
            <tr v-if="expandedModel === row.id" :id="`${modelDetailsPrefix}-${row.id}`">
              <td :colspan="columnCount">
                <div class="grid gap-1 py-2 text-[12px] text-text-secondary">
                  <p class="break-all">Endpoint: {{ row.endpoint }}</p>
                  <p v-if="row.saved.catalog">${{ row.saved.catalog.inputPer1M }} input · ${{ row.saved.catalog.outputPer1M }} output (USD per 1M tokens, catalog estimate)</p>
                  <p v-else>Pricing unavailable</p>
                  <p v-if="row.default">Default for new projects</p>
                </div>
              </td>
            </tr>
          </template>
          <template #details="{ row: { saved } }"><InlineNotification v-if="modelTests?.[saved.id]?.error" tone="error" :message="modelTests[saved.id].error" /></template>
          <template #actions="{ row: { saved } }">
            <ResourceTableEditButton :label="`Edit ${saved.name}`" :disabled="saving" @click="emit('openEditor', saved.id)" />
            <ResourceTableActionButton :icon="Play" :label="`Test connection for ${saved.name}`" :disabled="saving || !saved.configured" :busy="modelTests?.[saved.id]?.state === 'Testing…'" @click="emit('testSaved', saved.id)" />
            <ResourceTableActionButton v-if="!saved.default" :icon="Star" :label="`Make ${saved.name} default`" :disabled="saving || !saved.configured" @click="emit('setDefault', saved.id)" />
            <ResourceTableDeleteButton :label="`Delete ${saved.name}`" :disabled="saving" @click="emit('delete', saved.id)" />
          </template>
        </ModelCollection>
        <p v-else-if="!loadError && !loading && !showFirstRun" class="text-[13px] text-text-muted">No models configured</p>
      </div>

      <section class="k-resource-page k-resource-page--collection app-studio-provider-connections setup-table" aria-labelledby="app-studio-provider-connections-title">
        <header class="k-resource-page__header">
          <div class="k-resource-page__heading">
            <h3 id="app-studio-provider-connections-title" ref="providerConnectionsHeading" class="k-resource-page__title" tabindex="-1">Provider connections</h3>
            <p class="k-resource-page__subtitle">Save provider credentials once and reuse them across models.</p>
          </div>
        </header>
        <InlineNotification v-if="pendingConnection" tone="warning" message="Provider changes are saved and waiting for revision space. Existing model bindings keep their previous configuration." action-label="Dismiss" @action="pendingConnection = null" />
        <div v-if="connectionsLoading && !connections.length" class="grid gap-3 rounded-lg border border-dashed border-border-subtle bg-surface p-4" role="status" aria-busy="true">
          <Loader2 class="h-4 w-4 animate-spin text-accent" :stroke-width="1.75" /> Loading provider connections…
        </div>
        <InlineNotification v-else-if="connectionsError" tone="error" :message="connectionsError" action-label="Retry" :action-busy="connectionsLoading" action-busy-label="Retrying connections…" @action="emit('connectionsChanged')" />
        <ResourceTable v-if="connectionRows.length" :rows="connectionTableRows" :columns="providerConnectionColumns" row-key="id" :interactive="false" searchable search-placeholder="Search provider connections…" :search-keys="['name', 'endpoint', 'status']" aria-label="Provider connections" loaded empty-text="No provider connections match your search.">
          <template #name="{ row }">
            <div class="grid min-w-0 gap-1"><span class="font-medium">{{ row.name }}</span><span class="text-[10px] text-text-muted">{{ connectionFromTableRow(row).provider === 'google-ai-studio' ? 'Google AI Studio / Vertex' : providerOptions.find(option => option.value === providerPresetFor(connectionFromTableRow(row).provider, connectionFromTableRow(row).baseURL))?.label || 'OpenAI-compatible' }}</span></div>
          </template>
          <template #endpoint="{ row }"><span class="break-all font-mono text-[11px]">{{ connectionTableText(row, 'endpoint') }}</span></template>
          <template #bindingCount="{ row }"><span class="text-[12px]">{{ connectionBindingCount(row) }} model{{ connectionBindingCount(row) === 1 ? '' : 's' }}</span></template>
          <template #status="{ row }"><StatusBadge :status="connectionTableText(row, 'status')" :tone="connectionTableTone(row)" /><p v-if="connectionTableText(row, 'statusMessage')" class="mt-1 max-w-[48ch] text-[11px] leading-4 text-text-muted">{{ connectionTableText(row, 'statusMessage') }}</p></template>
          <template #actions="{ row }">
            <ResourceTableActionButton :icon="Plus" :label="`Add model using ${row.name}`" :disabled="saving || connectionBusy || modelLimitReached || connectionFromTableRow(row).deleting" @click="addModelFor(connectionFromTableRow(row))" />
            <ResourceTableEditButton :label="`Edit provider connection ${row.name}`" :disabled="saving || connectionBusy || connectionFromTableRow(row).deleting" @click="openConnectionEditor(connectionFromTableRow(row))" />
            <ResourceTableDeleteButton :label="`Delete provider connection ${row.name}`" :disabled="saving || connectionBusy || connectionBindingCount(row) > 0 || connectionFromTableRow(row).deleting" @click="emit('deleteConnection', connectionFromTableRow(row))" />
          </template>
        </ResourceTable>
        <p v-else-if="!connectionsLoading && !connectionsError && !showFirstRun" class="text-[13px] text-text-muted">No provider connections</p>
      </section>
    </template>

    <template v-else>
      <form class="k-create-surface k-create-surface--wide k-model-form agents-provider-connect" aria-label="Model binding form" novalidate :aria-busy="saving || testing" @submit.prevent="emit('save')">
        <div class="k-create-body">
            <section class="agents-provider-connect__section" aria-labelledby="binding-provider-heading">
              <h2 id="binding-provider-heading" class="k-model-form-heading">Provider connection</h2>
              <InlineNotification v-if="legacyEditing" tone="warning" message="This model uses a legacy direct credential. Select a saved provider connection to migrate it; its stable model ID, workspace default, and prior run history will be preserved." />
              <label for="model-provider" class="k-model-form-field">Saved provider connection
                <select id="model-provider" class="k-input" :value="connectionRef" :disabled="saving || testing || modelConnectionOptions.length === 0" @change="emit('selectConnection', ($event.target as HTMLSelectElement).value)"><option value="" disabled>Select a provider connection</option><option v-for="connection in modelConnectionOptions" :key="connection.name" :value="connection.name">{{ connection.displayName || connection.name }} · {{ providerOptions.find(option => option.value === providerPresetFor(connection.provider, connection.baseURL))?.label || 'OpenAI-compatible' }}</option></select>
                <span v-if="connectionRef && selectedConnection" class="k-model-form-hint">{{ selectedConnection.baseURL }}</span>
                <span v-if="connectionRef && selectedConnection && !selectedConnectionSnapshotReady && !connectionStatusTimedOut" class="k-model-form-hint" role="status" aria-live="polite">{{ connectionStatusChecking ? 'Confirming the saved provider key version. Keep this page open while the workspace finishes updating.' : 'The saved provider key version still needs confirmation.' }}</span>
                <span v-if="connectionRef && selectedConnection && !selectedConnectionSnapshotReady && connectionStatusTimedOut" class="k-model-form-hint" role="status" aria-live="polite">The saved provider key version is still updating. <button type="button" class="font-medium text-accent underline underline-offset-2" :disabled="connectionsLoading" @click="emit('retryConnectionStatus', connectionRef)">Retry status check</button>.</span>
              </label>
              <p v-if="!modelConnectionOptions.length" class="k-model-form-hint">No provider connections are saved yet. <button type="button" class="font-medium text-accent underline underline-offset-2" @click="startProviderDetour">Connect a provider</button> before adding a model.</p>
              <p v-else-if="connectionRef && !selectedConnection" class="k-model-form-hint" role="status">This model's provider connection is not currently visible. Check workspace access or refresh connections.</p>
              <p v-if="modelConnectionOptions.length" class="k-model-form-hint">Need another provider? <button type="button" class="font-medium text-accent underline underline-offset-2" :disabled="saving || testing" @click="startProviderDetour">Connect provider</button></p>
            </section>
            <section class="k-model-form-section" aria-labelledby="binding-model-heading">
              <div class="k-model-form-row"><h2 id="binding-model-heading" class="k-model-form-heading">Model</h2><button type="button" class="k-btn k-btn--ghost" :disabled="!canDiscover || discoveryLoading || saving" @click="emit('discoverConnection', connectionRef)"><RefreshCw class="h-3.5 w-3.5" :stroke-width="1.75" />Refresh models</button></div>
              <label for="model-id" class="k-model-form-field">Model ID
                <ModelIDSelector :model-value="model" :models="selectedModelRows" :invalid="Boolean(modelError)" :disabled="saving || testing || !connectionRef" :described-by="modelError ? 'model-id-error binding-model-help' : 'binding-model-help'" @update:model-value="emit('update:model', $event)" @select="emit('selectDiscoveredModel', $event)" />
                <span v-if="modelError" id="model-id-error" class="k-model-form-hint text-danger" role="alert">{{ modelError }}</span>
                <span id="binding-model-help" class="k-model-form-hint" role="status">{{ discoveryLoading ? 'Loading models…' : modelHint || 'Choose a discovered model or enter its exact ID.' }}</span>
              </label>
              <InlineNotification v-if="discoveryError" tone="warning" :message="`${discoveryError} You can still enter a model ID manually.`" />
              <ReasoningEffortControl variant="settings" :effort="reasoningEffort" :options="reasoningEffortOptions" :validation-error="reasoningEffortError" :disabled="saving || testing" @update:effort="emit('update:reasoningEffort', $event)" />
            </section>
            <label v-if="model.trim() || editingModelID" for="model-display-name" class="k-model-form-section k-model-form-field">Display name (optional)
              <input id="model-display-name" class="k-input" :class="nameError ? 'border-danger focus:border-danger focus:ring-danger/20' : ''" :value="name" :placeholder="model.split('/').pop() || 'Model name'" maxlength="80" :disabled="saving || testing" :aria-invalid="Boolean(nameError)" :aria-describedby="nameError ? 'model-display-name-error' : undefined" @input="emit('update:name', ($event.target as HTMLInputElement).value)">
              <span v-if="nameError" id="model-display-name-error" class="k-model-form-hint text-danger" role="alert">{{ nameError }}</span>
              <span v-else class="k-model-form-hint">Defaults to the selected model ID. You can choose a shorter name for model pickers.</span>
            </label>
            <div v-if="testError || testStatus">
              <InlineNotification v-if="testError" tone="error" :message="testError" />
              <p v-else-if="testStatus" class="k-model-form-hint" role="status">{{ testStatus }}</p>
            </div>
        </div>
        <footer class="k-create-actions agents-provider-connect__actions">
          <p v-if="!nameOnlyEdit" class="k-model-form-hint agents-provider-connect__charge">Runs a short model test. Provider charges may apply.</p>
          <button type="button" class="k-btn k-btn--ghost" :disabled="saving || testing" @click="emit('cancelEditor')">Cancel</button>
          <button type="submit" class="k-btn k-btn--primary" :disabled="!modelCanSave">{{ testing ? 'Verifying…' : saving ? 'Saving…' : legacyEditing ? 'Verify & migrate' : editingModelID ? requireConnectionTest ? 'Verify & save' : 'Save changes' : 'Verify & connect' }}</button>
        </footer>
      </form>

    </template>
  </section>
</template>

<style scoped>
.app-studio-provider-connections { margin-block-start: 1rem; }
.setup-table :deep(.k-table__cell--primary) { min-inline-size: 18rem; }
@media (max-width: 640px) {
  .setup-table :deep(.k-table__primary) { flex-wrap: wrap; }
  .setup-table :deep(.k-table__primary-content) { flex-basis: 100%; }
  .setup-table :deep(:is(.k-table__primary-actions, .k-table__primary-actions .k-table-action)) {
    margin-inline-start: 0;
    opacity: 1;
  }
}
.agents-provider-connect .agents-provider-connect__link{color:var(--color-accent);text-decoration:underline;text-underline-offset:3px;}
.agents-provider-connect__section{display:grid;gap:12px;min-width:0;}
.agents-provider-connect__details{font-size:12px;color:var(--color-text-secondary);}
.agents-provider-connect__details summary{cursor:pointer;}
.agents-provider-connect__details p{margin-top:8px;overflow-wrap:anywhere;}
.agents-provider-connect__charge{flex:1 1 100%;max-width:75ch;}
@media(max-width:620px){.agents-provider-connect__actions{position:static;}}
</style>
