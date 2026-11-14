<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { ArrowLeft, Check, ChevronRight, ExternalLink, LayoutTemplate, X } from 'lucide-vue-next'
import AgentTemplateCards from '../components/AgentTemplateCards.vue'
import AgentTemplateListInput from '../components/AgentTemplateListInput.vue'
import AgentDigestSchedule from '../components/AgentDigestSchedule.vue'
import FormSelect, { type FormSelectOption } from '../portalkit/FormSelect.vue'
import CreateGuidance from '../portalkit/CreateGuidance.vue'
import { portalHref } from '../portalkit/navigation'
import { toast } from '../portalkit/toast'
import { confirmDialog } from '../portalkit/confirm'
import { mutate } from '../mutate'
import type { ApiClient } from '../api'
import {
  instantiateTemplate,
  previewPrompt,
  slotsOfKind,
  templateBackend,
  templateSlotLabel as slotLabel,
  type AgentTemplate,
  type TemplateSlotKind,
  type TemplateWiringSlot,
} from '../agent-template'
import { validDigestCron } from '../agent-digest-cron'
import type { AppStore } from '../store'
import { emptyAgentCreateDraft, type AgentCreateDraft, type AgentCredentialFamily } from '../agent-create-draft'
import type { CreateSuccessDetail, Route } from '../router'
import {
  AGENT_BACKEND_HARNESS,
  AGENT_BACKEND_MODEL,
  HARNESS_EDGE_KINDS,
  MODEL_PROVIDER_CODEX,
  WORKSPACE_MODES,
  credentialUnavailable,
  edgeKey,
  harnessLabel,
  splitEdgeKey,
  type Agent,
  type AgentBackendType,
  type AgentCreate as AgentCreateBody,
  type HarnessWorkspace,
} from '../types'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'

const REASONING_EFFORT_RE = /^[a-z][a-z0-9_-]{0,31}$/
const NAME_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/
const props = withDefaults(defineProps<{
  store: AppStore
  api: ApiClient
  authorityEpoch?: number
  createSession?: number
  initialDraft?: AgentCreateDraft | null
  initialBackendType?: AgentBackendType
  /** A template name from #/create/agent/<template>; preselected once the catalog loads. */
  initialTemplate?: string
}>(), { authorityEpoch: 0, createSession: 0, initialDraft: null, initialBackendType: AGENT_BACKEND_MODEL, initialTemplate: '' })
const emit = defineEmits<{
  navigate: [route: Route]
  'add-credential': [detail: {
    family: AgentCredentialFamily
    draft: AgentCreateDraft
    store: AppStore
    authorityEpoch: number
    createSession: number
  }]
  'create-success': [detail: CreateSuccessDetail]
  'create-cancel': [detail: Pick<CreateSuccessDetail, 'store' | 'authorityEpoch' | 'createSession'>]
}>()
const revision = useStoreRevision(() => props.store)

const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

const initialDraft = props.initialDraft || emptyAgentCreateDraft(props.initialBackendType, props.initialTemplate)
const name = ref(initialDraft.name)
// Templates: the catalog is a bundle asset (see api.agentTemplates). The chosen
// template drives the backend, the parameter fields and which wiring is asked
// for; '' is the blank form this view always was.
const templates = ref<AgentTemplate[]>([])
const templatesError = ref('')
const template = ref(initialDraft.template)
const templateDrafts = new Map(Object.entries(initialDraft.templateDrafts || {}))
const templatesLoading = ref(false)
const templatesLoaded = ref(false)
const parameters = ref<Record<string, string>>({ ...initialDraft.parameters })
// Connection-slot wiring, by slot key. The other slot kinds reuse the fields
// below (model credential, channel, machine, harness identity, GitHub).
const slotWiring = ref<Record<string, string>>({ ...initialDraft.wiring })
const researchSpecialist = ref(initialDraft.researchSpecialist ?? Boolean(initialDraft.parameters.domain?.trim()))
const digestTimeMissing = ref(initialDraft.digestTimeMissing ?? false)
const teamExamples = {
  'Platform team': 'We maintain Kubernetes clusters and help teams ship internal tools.',
  'Product team': 'We build product features and use this workspace to coordinate releases.',
}
const customPrompt = ref(initialDraft.customPrompt)
const backendType = ref<AgentBackendType>(initialDraft.backendType)
const modelCredential = ref(initialDraft.modelCredential)
const harnessEdge = ref(initialDraft.harnessEdge)
const harnessCredential = ref(initialDraft.harnessCredential)
const harnessModel = ref(initialDraft.harnessModel)
const harnessReasoningEffort = ref(initialDraft.harnessReasoningEffort)
const harnessWorkspace = ref<HarnessWorkspace>(initialDraft.harnessWorkspace)
const harnessGitHubConnection = ref(initialDraft.harnessGitHubConnection)
const systemPrompt = ref(initialDraft.systemPrompt)
const channel = ref(initialDraft.channel)
const errors = reactive<Record<string, string>>({})
const busy = ref(false)
const nameInput = ref<HTMLInputElement | null>(null)
const form = ref<HTMLFormElement | null>(null)
const settings = ref<HTMLDetailsElement | null>(null)
const templatePicker = ref<HTMLDialogElement | null>(null)

const agents = computed(() => { revision.value; return { ...props.store.agents } })
const credentials = computed(() => { revision.value; return { ...props.store.credentials } })
const isHarness = computed(() => backendType.value === AGENT_BACKEND_HARNESS)
// Two credential families, and the API rejects one where the other belongs: a
// chat endpoint cannot drive a coding harness, and a `claude setup-token` value
// is not a bearer any chat API would accept. So each picker offers only its own.
const chatCredentials = computed(() => { revision.value; return props.store.chatCredentials() })
const harnessCredentials = computed(() => { revision.value; return props.store.harnessCredentials().filter(item => !credentialUnavailable(item)) })
const edgeSlice = computed(() => { revision.value; return { ...props.store.edges } })
// A runner is a process on a machine, so a KubernetesCluster edge can never host
// one. It is filtered here as well as refused by the write path: an option that
// cannot work should not be offered, not merely rejected after being picked.
const hostEdges = computed(() => edgeSlice.value.data.filter(edge => HARNESS_EDGE_KINDS.includes(edge.kind)))
const machineOffline = computed(() => hostEdges.value.some(edge => edgeKey(edge.kind, edge.name) === harnessEdge.value && edge.connected === false))
const connectMachineHref = computed(() => portalHref('/providers/edges/connect/edge', props.api.tenant()))
const edgeOptions = computed<FormSelectOption[]>(() => [
  { value: '', label: '— pick a machine —' },
  ...hostEdges.value.map(edge => ({
    value: edgeKey(edge.kind, edge.name),
    label: `${edge.name} (${edge.kind === 'MacOSServer' ? 'macOS' : 'Linux'}${edge.connected === false ? ', offline' : ''})`,
  })),
])
const harnessCredentialOptions = computed<FormSelectOption[]>(() => [
  { value: '', label: '— pick a harness credential —' },
  ...harnessCredentials.value.map(credential => ({
    value: credential.name,
    label: `${credential.name} (${harnessLabel(credential.provider)})`,
  })),
])
const workspaceOptions = computed<FormSelectOption[]>(() => WORKSPACE_MODES.map(mode => ({ value: mode.id, label: mode.label })))
// The harness is NOT chosen here: it is derived from the credential's provider.
const selectedHarnessProvider = computed(() =>
  harnessCredentials.value.find(credential => credential.name === harnessCredential.value)?.provider)
const selectedHarness = computed(() => harnessLabel(selectedHarnessProvider.value))
const usesCodex = computed(() => selectedHarnessProvider.value === MODEL_PROVIDER_CODEX)
const reasoningEffort = computed(() => usesCodex.value ? harnessReasoningEffort.value.trim() : '')
const channels = computed(() => { revision.value; return props.store.channelConnections() })
const githubConnections = computed(() => { revision.value; return props.store.githubConnections() })
const usableGitHubConnections = computed(() => githubConnections.value.filter(connection =>
  connection.spec.auth !== 'oauth' || connection.status?.oauthConnected === true))
const githubConnectionOptions = computed(() => [
  { value: '', label: '— none —' },
  ...usableGitHubConnections.value.map(item => ({ value: item.metadata.name, label: item.spec.displayName || item.metadata.name })),
])
const selectedTemplate = computed(() => templates.value.find(item => item.metadata.name === template.value) || null)
const isNewsReporter = computed(() => selectedTemplate.value?.metadata.name === 'news-reporter')
const isResearcher = computed(() => selectedTemplate.value?.metadata.name === 'researcher')
const isWorkspaceAssistant = computed(() => selectedTemplate.value?.metadata.name === 'workspace-assistant')
const isCodeReviewer = computed(() => selectedTemplate.value?.metadata.name === 'code-reviewer')
const templateParameters = computed(() => selectedTemplate.value?.spec.parameters ?? [])
const digestParameter = computed(() => isNewsReporter.value ? templateParameters.value.find(param => param.key === 'digestTime') : undefined)
const definitionParameters = computed(() => templateParameters.value.filter(param => param !== digestParameter.value))
const effectiveParameters = computed(() => isResearcher.value && !researchSpecialist.value
  ? { ...parameters.value, domain: '' }
  : parameters.value)
const connectionSlots = computed(() => selectedTemplate.value ? slotsOfKind(selectedTemplate.value, 'connection') : [])
function slotOf(kind: TemplateSlotKind): TemplateWiringSlot | undefined {
  return selectedTemplate.value ? slotsOfKind(selectedTemplate.value, kind)[0] : undefined
}
const channelSlot = computed(() => slotOf('channel'))
const githubSlot = computed(() => slotOf('githubConnection'))
const connectionSlice = computed(() => { revision.value; return { ...props.store.connections } })
const connections = computed(() => connectionSlice.value.data)
const githubSelectionUnavailable = computed(() => connectionSlice.value.hasSnapshot && Boolean(harnessGitHubConnection.value) &&
  !usableGitHubConnections.value.some(connection => connection.metadata.name === harnessGitHubConnection.value))
const connectionSetupHref = (type?: string) => portalHref(
  '/providers/agents/#/create/connection' + (type ? '/' + type : ''), props.api.tenant(),
)
const githubSetupHref = computed(() => githubConnections.value.length
  ? portalHref('/providers/agents/#/connections', props.api.tenant())
  : connectionSetupHref('github'))
const harnessSetupHref = computed(() => portalHref('/providers/agents/#/create/model/harness', props.api.tenant()))
const reviewerSetupOpened = ref(false)
const pendingRefresh = new Set<'edges' | 'credentials' | 'connections'>()
function openReviewerSetup(resource: 'edges' | 'credentials' | 'connections'): void {
  reviewerSetupOpened.value = true
  pendingRefresh.add(resource)
}

function refreshReviewerConnections(): void {
  void props.store.load('edges')
  void props.store.load('credentials')
  void props.store.load('connections')
}

function refreshAfterSetup(): void {
  for (const resource of pendingRefresh) void props.store.load(resource)
  pendingRefresh.clear()
}
function refreshOnVisibility(): void {
  if (document.visibilityState === 'visible') refreshAfterSetup()
}
function connectionOptions(type: string, required: boolean): FormSelectOption[] {
  return [
    { value: '', label: required ? '— pick a connection —' : '— none —' },
    ...connections.value.filter(item => item.spec.type === type).map(item => ({ value: item.metadata.name, label: item.spec.displayName || item.metadata.name })),
  ]
}
const promptSource = computed(() => selectedTemplate.value && !customPrompt.value
  ? selectedTemplate.value.spec.agent.systemPrompt ?? ''
  : systemPrompt.value)
const instructions = computed(() => selectedTemplate.value
  ? previewPrompt(selectedTemplate.value, effectiveParameters.value, promptSource.value)
  : promptSource.value)
watch(selectedTemplate, current => {
  if (!current) return
  backendType.value = templateBackend(current)
  if (!name.value.trim()) name.value = current.spec.suggestedName || current.metadata.name
}, { immediate: true, flush: 'sync' })
async function loadTemplates(): Promise<void> {
  if (templatesLoading.value) return
  templatesLoading.value = true
  try {
    templates.value = await props.api.agentTemplates()
    templatesLoaded.value = true
    templatesError.value = ''
  } catch (e) {
    templatesError.value = (e as Error).message
  } finally {
    templatesLoading.value = false
  }
}
function editInstructions(event: Event): void {
  systemPrompt.value = (event.target as HTMLTextAreaElement).value
  customPrompt.value = Boolean(selectedTemplate.value)
}
const credentialOptions = computed(() => chatCredentials.value.filter(item => !credentialUnavailable(item)).map(item => ({
  value: item.name,
  label: item.connectionRef ? `${item.connectionName || item.connectionRef} · ${item.displayName || item.model}` : `${item.name}${item.model ? ` (${item.model})` : ''}`,
})))
function selectOnlyModel(): void {
  if (modelCredential.value || !credentials.value.hasSnapshot || credentials.value.error) return
  const eligible = chatCredentials.value.filter(model => !credentialUnavailable(model))
  // The workspace default (spec.default) wins when it works; otherwise the
  // only model there is. Several unmarked models are still the user's choice.
  const preferred = eligible.find(model => model.default === true && model.ready === true)
  if (preferred) modelCredential.value = preferred.name
  else if (eligible.length === 1 && eligible[0].ready === true) modelCredential.value = eligible[0].name
}
watch([chatCredentials, credentials], selectOnlyModel, { immediate: true })

function snapshotDraft(): AgentCreateDraft {
  return {
    name: name.value, backendType: backendType.value, modelCredential: modelCredential.value,
    harnessEdge: harnessEdge.value, harnessCredential: harnessCredential.value, harnessModel: harnessModel.value,
    harnessReasoningEffort: harnessReasoningEffort.value, harnessWorkspace: harnessWorkspace.value,
    harnessGitHubConnection: harnessGitHubConnection.value, systemPrompt: systemPrompt.value,
    customPrompt: customPrompt.value, channel: channel.value, template: template.value,
    parameters: { ...parameters.value }, wiring: { ...slotWiring.value }, researchSpecialist: researchSpecialist.value,
    digestTimeMissing: digestTimeMissing.value,
  }
}

function switchTemplate(value: string): void {
  if (value === template.value) return
  templateDrafts.set(template.value, snapshotDraft())
  const suggestion = selectedTemplate.value?.spec.suggestedName || selectedTemplate.value?.metadata.name
  const currentName = name.value === suggestion ? '' : name.value
  const draft = templateDrafts.get(value) || {
    ...emptyAgentCreateDraft(backendType.value, value),
    name: currentName,
    modelCredential: modelCredential.value,
    // An author can bring their own instructions to the first template.
    systemPrompt: selectedTemplate.value ? '' : systemPrompt.value,
    customPrompt: Boolean(value && !template.value && systemPrompt.value.trim()),
  }
  clearErrors()
  name.value = draft.name
  backendType.value = draft.backendType
  modelCredential.value = draft.modelCredential
  harnessEdge.value = draft.harnessEdge
  harnessCredential.value = draft.harnessCredential
  harnessModel.value = draft.harnessModel
  harnessReasoningEffort.value = draft.harnessReasoningEffort
  harnessWorkspace.value = draft.harnessWorkspace
  harnessGitHubConnection.value = draft.harnessGitHubConnection
  systemPrompt.value = draft.systemPrompt
  customPrompt.value = draft.customPrompt
  channel.value = draft.channel
  parameters.value = { ...draft.parameters }
  slotWiring.value = { ...draft.wiring }
  researchSpecialist.value = draft.researchSpecialist ?? Boolean(draft.parameters.domain?.trim())
  digestTimeMissing.value = draft.digestTimeMissing ?? false
  template.value = value
  selectOnlyModel()
}

function chooseBackend(type: AgentBackendType): void {
  if (!selectedTemplate.value) backendType.value = type
}

function clearTemplate(): void {
  switchTemplate('')
}

function chooseTemplate(value: string): void {
  switchTemplate(value)
  templatePicker.value?.close()
  void nextTick(() => nameInput.value?.focus())
}

function openTemplates(): void {
  templatePicker.value?.showModal()
  if (!templatesLoaded.value) void loadTemplates()
}

const guidance = computed(() => {
  if (isCodeReviewer.value) return { title: 'Review behavior', description: 'Reports findings in chat and posts a GitHub review only when asked.' }
  if (isNewsReporter.value) return { title: 'Scheduled digest', description: 'Review the schedule and destination before creating the agent.' }
  if (isResearcher.value) return { title: 'Research behavior', description: 'Investigates independent questions in parallel.' }
  if (isWorkspaceAssistant.value) return { title: 'Workspace behavior', description: 'Asks before making changes.' }
  return { title: 'After you create the agent', description: 'A conversation opens when the agent is created.' }
})

async function applyTeamExample(example: string): Promise<void> {
  const activeTemplate = template.value
  const before = parameters.value.teamContext
  if (before === example) return
  if (before?.trim()) {
    const authority = captureAuthority()
    if (!await confirmDialog({
      title: 'Replace team context?',
      message: 'The example will replace your current team context.',
      confirmLabel: 'Use example',
    }) || !authorityIsCurrent(authority) || template.value !== activeTemplate || parameters.value.teamContext !== before) return
  }
  parameters.value.teamContext = example
  await nextTick()
  const field = form.value?.querySelector('#agent-create-param-teamContext')
  if (field instanceof HTMLTextAreaElement) {
    field.focus()
    field.setSelectionRange(example.length, example.length)
  }
}

const channelOptions = computed(() => [
  { value: '', label: '— none —' },
  ...channels.value.map(item => ({
    value: item.metadata.name,
    label: `${item.spec.displayName || item.metadata.name} (${item.spec.type})`,
  })),
])
const hasOption = (options: FormSelectOption[], value: string) => Boolean(value) && options.some(option => option.value === value)
const githubConnectionStatus = computed(() => {
  if (githubSelectionUnavailable.value) {
    return usableGitHubConnections.value.length
      ? 'The selected GitHub connection is unavailable. Choose another connection.'
      : 'The selected GitHub connection is unavailable. Reconnect it or add another.'
  }
  if (!githubSlot.value?.required || hasOption(githubConnectionOptions.value, harnessGitHubConnection.value)) return ''
  if (connectionSlice.value.error) return 'Could not check GitHub connections.'
  if (!connectionSlice.value.hasSnapshot || connectionSlice.value.loading) return 'Checking GitHub connections…'
  return errors.harnessGitHubConnection || (isCodeReviewer.value ? '' : 'Select a GitHub connection to create this agent.')
})
const requiresReview = computed(() => {
  const spec = selectedTemplate.value?.spec
  const access = spec?.agent.tools?.background
  return Boolean(spec?.schedules?.length || access?.families?.some(family => family !== 'core') ||
    access?.toolsets?.length || access?.connections?.length ||
    connectionSlots.value.some(slot => slotWiring.value[slot.key]))
})
const canSubmit = computed(() => {
  if (template.value && !selectedTemplate.value) return false
  if (!selectedTemplate.value) return isHarness.value
    ? hostEdges.value.length > 0 && harnessCredentials.value.length > 0
    : credentialOptions.value.length > 0
  return (isHarness.value
    ? hasOption(edgeOptions.value, harnessEdge.value) && hasOption(harnessCredentialOptions.value, harnessCredential.value)
    : hasOption(credentialOptions.value, modelCredential.value)) &&
    (!channelSlot.value?.required || hasOption(channelOptions.value, channel.value)) &&
    (!githubSlot.value?.required || hasOption(githubConnectionOptions.value, harnessGitHubConnection.value)) &&
    connectionSlots.value.every(slot => !slot.required || hasOption(connectionOptions(slot.connectionType || '', true), slotWiring.value[slot.key]))
})

onMounted(() => {
  void nextTick(() => nameInput.value?.focus())
  if (template.value) void loadTemplates()
  window.addEventListener('focus', refreshAfterSetup)
  document.addEventListener('visibilitychange', refreshOnVisibility)
})
onUnmounted(() => {
  window.removeEventListener('focus', refreshAfterSetup)
  document.removeEventListener('visibilitychange', refreshOnVisibility)
})

function clearErrors(): void {
  for (const key of Object.keys(errors)) delete errors[key]
}

function addCredential(family: AgentCredentialFamily): void {
  if (busy.value) return
  emit('add-credential', {
    family,
    draft: { ...snapshotDraft(), templateDrafts: Object.fromEntries(templateDrafts) },
    store: props.store,
    authorityEpoch: props.authorityEpoch,
    createSession: props.createSession,
  })
}

function cancel(): void {
  if (busy.value) return
  emit('create-cancel', {
    store: props.store,
    authorityEpoch: props.authorityEpoch,
    createSession: props.createSession,
  })
}

async function submit(): Promise<void> {
  if (busy.value) return
  clearErrors()
  const normalizedName = name.value.trim()
  if (!normalizedName) errors.name = 'A name is required.'
  else if (!NAME_RE.test(normalizedName)) errors.name = 'Lowercase letters, digits and dashes only.'
  else if (agents.value.data.some(agent => agent.metadata.name === normalizedName)) errors.name = 'An agent with that name already exists.'
  if (isHarness.value) {
    // A harness-backed agent needs a machine and an identity, and needs NO chat
    // credential. Requiring one here was the bug: it blocked the whole backend.
    if (reasoningEffort.value && !REASONING_EFFORT_RE.test(reasoningEffort.value)) {
      errors.harnessReasoningEffort = 'Use up to 32 lowercase letters, digits, dashes, or underscores. Start with a letter.'
      if (settings.value) settings.value.open = true
    }
    if (!hasOption(edgeOptions.value, harnessEdge.value)) errors.harnessEdge = 'Pick the machine this agent runs its turns on.'
    if (!hasOption(harnessCredentialOptions.value, harnessCredential.value)) errors.harnessCredential = 'Pick the harness identity its turns run as.'
  } else if (!credentialOptions.value.some(option => option.value === modelCredential.value)) {
    errors.modelCredential = 'Pick the model this agent reasons with.'
  }
  const chosen = selectedTemplate.value
  if (isResearcher.value && researchSpecialist.value && !parameters.value.domain?.trim()) {
    errors['param-domain'] = 'Enter an area of expertise, or choose Generalist.'
  }
  if (template.value && !chosen) errors.template = 'Load the selected template before creating this agent.'
  if (chosen) {
    const digest = digestParameter.value
    if (digest && digestTimeMissing.value) {
      errors[`param-${digest.key}`] = 'Enter a cron expression, or choose a time for the digest.'
    } else if (digest && !validDigestCron(parameters.value[digest.key]?.trim() || digest.default || '')) {
      errors[`param-${digest.key}`] = 'Enter a valid cron expression.'
    }
    for (const param of templateParameters.value) {
      if (param.required && !(effectiveParameters.value[param.key] ?? '').trim() && !param.default) errors[`param-${param.key}`] = `${param.label} is required.`
    }
    if ((channelSlot.value?.required || channel.value) && !hasOption(channelOptions.value, channel.value)) errors.channel = 'This template posts to a channel; pick one.'
    if (githubSlot.value?.required && !hasOption(githubConnectionOptions.value, harnessGitHubConnection.value)) errors.harnessGitHubConnection = 'This template needs a GitHub connection.'
    for (const slot of connectionSlots.value) {
      if ((slot.required || slotWiring.value[slot.key]) && !hasOption(connectionOptions(slot.connectionType || '', !!slot.required), slotWiring.value[slot.key])) errors[`slot-${slot.key}`] = `Pick a ${slotLabel(slot)}.`
    }
  }
  if (Object.keys(errors).length) {
    await nextTick()
    form.value?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus()
    return
  }
  if (chosen) {
    await submitTemplate(chosen, normalizedName)
    return
  }

  const body: AgentCreateBody = { name: normalizedName, displayName: normalizedName }
  if (isHarness.value) {
    const edge = splitEdgeKey(harnessEdge.value)
    body.backendType = AGENT_BACKEND_HARNESS
    body.harness = {
      edgeRef: { kind: edge.kind as 'LinuxServer' | 'MacOSServer', name: edge.name },
      credentialRef: harnessCredential.value,
      ...(harnessModel.value.trim() ? { model: harnessModel.value.trim() } : {}),
      ...(reasoningEffort.value ? { reasoningEffort: reasoningEffort.value } : {}),
      workspace: harnessWorkspace.value,
      ...(harnessGitHubConnection.value ? { githubConnectionRef: harnessGitHubConnection.value } : {}),
    }
  } else {
    body.modelCredential = modelCredential.value
  }
  const prompt = systemPrompt.value.trim()
  if (prompt) body.systemPrompt = prompt
  busy.value = true
  let result: Agent | undefined
  try {
    result = await mutate(props.store, {
      run: () => props.api.createAgent(body),
      failure: 'Create failed',
      reload: ['agents'],
    })
  } finally {
    busy.value = false
  }
  if (result) emit('create-success', {
    resource: 'agent',
    name: normalizedName,
    item: result,
    store: props.store,
    authorityEpoch: props.authorityEpoch,
    createSession: props.createSession,
  })
}

// submitTemplate instantiates the chosen template with this form's fields as
// its wiring and writes the result through the kube client — the same
// function the provider runs for MCP's create_agent(template=...), held to
// the same output by the parity fixtures.
async function submitTemplate(chosen: AgentTemplate, normalizedName: string): Promise<void> {
  const wiring: Record<string, string> = { ...slotWiring.value }
  for (const slot of chosen.spec.wiring ?? []) {
    switch (slot.slot) {
      case 'modelCredential': wiring[slot.key] = modelCredential.value; break
      case 'channel': wiring[slot.key] = channel.value; break
      case 'edge': wiring[slot.key] = harnessEdge.value; break
      case 'harnessCredential': wiring[slot.key] = harnessCredential.value; break
      case 'githubConnection': wiring[slot.key] = harnessGitHubConnection.value; break
      default: break
    }
  }
  let instance
  try {
    instance = instantiateTemplate(chosen, { name: normalizedName, parameters: effectiveParameters.value, wiring })
  } catch (e) {
    errors.template = (e as Error).message
    return
  }
  if (customPrompt.value) {
    instance.agent.spec.systemPrompt = previewPrompt(chosen, effectiveParameters.value, systemPrompt.value).trim() || undefined
  }
  const harness = instance.agent.spec.backend?.harness
  if (harness) {
    if (harnessModel.value.trim()) harness.model = harnessModel.value.trim()
    if (reasoningEffort.value) harness.reasoningEffort = reasoningEffort.value
    harness.workspace = harnessWorkspace.value
  }
  const { agent, schedules } = instance
  const background = agent.spec.tools?.background
  const access = [
    ...(background?.families ?? []).filter(family => family !== 'core'),
    ...(background?.toolsets ?? []),
    ...(background?.connections ?? []),
  ]
  const effects = schedules.map(schedule => {
    const when = [schedule.spec.schedule || schedule.spec.runAt, schedule.spec.timeZone].filter(Boolean).join(' ')
    const destination = channels.value.find(item => item.metadata.name === channel.value)
    const delivery = destination && schedule.spec.channelRef === 'primary'
      ? ` Posts to ${destination.spec.displayName || destination.metadata.name}.` : ''
    return `${schedule.spec.suspend ? 'Paused schedule' : 'Schedule'} ${schedule.metadata.name}${when ? ` (${when})` : ''}.${delivery}`
  })
  if (access.length) effects.push(`Background runs can use ${access.join(', ')}.`)
  const authority = captureAuthority()

  busy.value = true
  let result
  try {
    if (effects.length) {
      const confirmed = await confirmDialog({
        title: 'Review what this template adds',
        message: effects.join(' '),
        confirmLabel: 'Create & open chat',
      })
      if (!confirmed || !authorityIsCurrent(authority)) return
    }
    result = await mutate(props.store, {
      run: () => props.api.createAgentFromTemplate(agent, schedules),
      failure: 'Create failed',
      reload: schedules.length ? ['agents', 'schedules'] : ['agents'],
    })
  } finally {
    busy.value = false
  }
  if (!result) return
  for (const warning of result.warnings) toast('error', warning)
  emit('create-success', {
    resource: 'agent',
    name: normalizedName,
    item: result.agent,
    store: props.store,
    authorityEpoch: props.authorityEpoch,
    createSession: props.createSession,
  })
}
</script>

<template>
  <div class="k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="busy" @click="cancel">
      <ArrowLeft aria-hidden="true" /> Agents
    </button>
    <header class="k-create-header agents-create-header">
      <div>
        <h1 class="k-create-title">New agent</h1>
        <p v-if="selectedTemplate" class="k-create-description" data-selected-template>{{ selectedTemplate.spec.displayName }} template</p>
        <p v-else class="k-create-description">Set up an agent for your workspace.</p>
      </div>
      <button class="k-btn k-btn--ghost" type="button" :disabled="busy" aria-haspopup="dialog" @click="openTemplates">
        <LayoutTemplate aria-hidden="true" /> Templates
      </button>
    </header>

    <dialog ref="templatePicker" class="k-modal agents-template-picker" aria-labelledby="agents-template-picker-title">
      <div class="agents-template-picker-header">
        <h2 id="agents-template-picker-title" class="k-modal__title">Templates</h2>
        <button class="k-icon-action" type="button" aria-label="Close templates" autofocus @click="templatePicker?.close()">
          <X aria-hidden="true" />
        </button>
      </div>
      <p v-if="templatesLoading" role="status">Loading templates…</p>
      <div v-else-if="templatesError" class="agents-stale" role="status">
        Could not load agent templates.
        <button class="k-btn k-btn--ghost" type="button" @click="loadTemplates">Retry</button>
      </div>
      <AgentTemplateCards v-else-if="templates.length" :templates="templates" :selected="template" @select="chooseTemplate" />
      <p v-else>No templates are available.</p>
    </dialog>

    <div v-if="agents.error && !agents.hasSnapshot" class="k-card agents-state agents-state-error" role="alert">
      Could not load existing agents. {{ agents.error }}
      <button class="k-btn k-btn--ghost" type="button" :disabled="agents.loading" @click="store.load('agents')">Retry</button>
    </div>
    <div v-else-if="credentials.error && !credentials.hasSnapshot" class="k-card agents-state agents-state-error" role="alert">
      Could not load model credentials. {{ credentials.error }}
      <button class="k-btn k-btn--ghost" type="button" :disabled="credentials.loading" @click="store.load('credentials')">Retry</button>
    </div>
    <div v-else-if="!agents.hasSnapshot || !credentials.hasSnapshot" class="k-card agents-state agents-state-loading k-loading-reveal" role="status">
      Loading existing agents and model credentials…
    </div>

    <template v-else>
      <div v-if="agents.error" class="agents-stale" role="status">
        Showing the last loaded agents. {{ agents.error }}
        <button class="k-btn k-btn--ghost" type="button" :disabled="agents.loading" @click="store.load('agents')">Retry</button>
      </div>
      <div v-if="credentials.error" class="agents-stale" role="status">
        Showing the last loaded model credentials. {{ credentials.error }}
        <button class="k-btn k-btn--ghost" type="button" :disabled="credentials.loading" @click="store.load('credentials')">Retry</button>
      </div>

      <form ref="form" class="agents-create-form agents-guided-form k-create-surface k-create-surface--guided" aria-label="Create agent" :aria-busy="busy" @submit.prevent="submit">
        <div class="k-create-body k-create-body--guided">
          <div class="k-create-fields" :class="{ 'agents-template-fields': selectedTemplate }">
            <div v-if="selectedTemplate" class="agents-template-selection">
              <button class="k-btn k-btn--text" type="button" :disabled="busy" @click="clearTemplate">Clear template</button>
            </div>
            <div v-else-if="template" class="agents-stale" role="status">
              {{ templatesError ? 'Could not load the selected template.' : templatesLoaded ? 'This template is unavailable.' : 'Loading the selected template…' }}
              <button v-if="templatesError" class="k-btn k-btn--ghost" type="button" @click="loadTemplates">Retry</button>
              <button class="k-btn k-btn--text" type="button" :disabled="busy" @click="clearTemplate">Clear template</button>
            </div>

            <section class="agents-create-section" aria-labelledby="agent-create-definition-heading">
              <h2 id="agent-create-definition-heading">Agent definition</h2>
              <label for="agent-create-name">
                Name *
                <input
                  id="agent-create-name"
                  ref="nameInput"
                  v-model="name"
                  class="k-input"
                  name="name"
                  aria-label="Name"
                  placeholder="research-bot"
                  autocomplete="off"
                  required
                  :disabled="busy"
                  :aria-invalid="errors.name ? 'true' : undefined"
                  :aria-describedby="errors.name ? 'agent-create-name-hint agent-create-name-error' : 'agent-create-name-hint'"
                />
                <span v-if="errors.name" id="agent-create-name-error" class="agents-fielderr" role="alert">{{ errors.name }}</span>
                <span id="agent-create-name-hint" class="agents-hint">A short id you'll reference from schedules and triggers.</span>
              </label>

              <label v-if="!selectedTemplate">
                <span id="agent-create-system-prompt-label">Instructions (optional)</span>
                <textarea :value="instructions" class="k-input" rows="3" placeholder="Describe how this agent should help." :disabled="busy" aria-labelledby="agent-create-system-prompt-label" @input="editInstructions" />
              </label>
              <div v-for="param in definitionParameters" :key="param.key" class="agents-template-parameter">
                <fieldset v-if="isResearcher && param.key === 'domain'" class="agents-template-choice">
                  <legend>Research focus</legend>
                  <div class="agents-grid2">
                    <label class="agents-template-radio k-btn k-btn--ghost">
                      <input v-model="researchSpecialist" type="radio" name="research-focus" :value="false" :disabled="busy" />
                      <span><strong>Generalist</strong><small>Research any topic.</small></span>
                    </label>
                    <label class="agents-template-radio k-btn k-btn--ghost">
                      <input v-model="researchSpecialist" type="radio" name="research-focus" :value="true" :disabled="busy" />
                      <span><strong>Specialist</strong><small>Focus on a field you define.</small></span>
                    </label>
                  </div>
                </fieldset>
                <template v-if="!isResearcher || param.key !== 'domain' || researchSpecialist">
                  <label :for="`agent-create-param-${param.key}`" :id="`agent-create-param-${param.key}-label`">
                    {{ param.label }}{{ param.required || (isResearcher && param.key === 'domain') ? ' *' : param.default ? ' (optional)' : '' }}
                  </label>
                  <div v-if="isWorkspaceAssistant && param.key === 'teamContext'" class="agents-form-actions">
                    <span class="agents-hint">Start with</span>
                    <button v-for="(example, label) in teamExamples" :key="label" type="button" class="k-btn k-btn--ghost"
                      :disabled="busy" @click="applyTeamExample(example)">{{ label }}</button>
                  </div>
                  <AgentTemplateListInput
                    v-if="param.type === 'list' && (isCodeReviewer || isNewsReporter)"
                    :id="`agent-create-param-${param.key}`"
                    :model-value="parameters[param.key] || ''"
                    :placeholder="isCodeReviewer ? 'Add owner/repo…' : 'Add a topic…'"
                    :disabled="busy" :required="!!param.required" :invalid="!!errors[`param-${param.key}`]"
                    :labelledby="`agent-create-param-${param.key}-label`"
                    :describedby="[`agent-create-param-${param.key}-hint`, errors[`param-${param.key}`] ? `agent-create-param-${param.key}-error` : ''].filter(Boolean).join(' ')"
                    @update:model-value="parameters[param.key] = $event"
                  />
                  <textarea
                    v-else-if="param.type !== 'string'"
                    :id="`agent-create-param-${param.key}`"
                    v-model="parameters[param.key]"
                    class="k-input"
                    :rows="param.type === 'list' ? 3 : 2"
                    :placeholder="isCodeReviewer && param.key === 'reviewFocus' ? 'Use template default' : param.default || (param.type === 'list' ? 'One per line' : '')"
                    :disabled="busy"
                    :aria-labelledby="`agent-create-param-${param.key}-label`"
                    :aria-describedby="[`agent-create-param-${param.key}-hint`, errors[`param-${param.key}`] ? `agent-create-param-${param.key}-error` : ''].filter(Boolean).join(' ')"
                    :aria-invalid="errors[`param-${param.key}`] ? 'true' : undefined"
                  />
                  <input
                    v-else
                    :id="`agent-create-param-${param.key}`"
                    v-model="parameters[param.key]"
                    class="k-input"
                    :placeholder="param.default || ''"
                    :disabled="busy"
                    autocomplete="off"
                    :aria-labelledby="`agent-create-param-${param.key}-label`"
                    :aria-describedby="[`agent-create-param-${param.key}-hint`, errors[`param-${param.key}`] ? `agent-create-param-${param.key}-error` : ''].filter(Boolean).join(' ')"
                    :aria-invalid="errors[`param-${param.key}`] ? 'true' : undefined"
                  />
                  <span v-if="errors[`param-${param.key}`]" :id="`agent-create-param-${param.key}-error`" class="agents-fielderr" role="alert">{{ errors[`param-${param.key}`] }}</span>
                  <span :id="`agent-create-param-${param.key}-hint`" class="agents-hint">{{ param.type === 'list' && (isCodeReviewer || isNewsReporter) ? 'Press Enter to add an item, or paste one per line.' : isResearcher && param.key === 'domain' ? 'For example, cloud infrastructure pricing or EU energy policy.' : param.description }}{{ param.default && param.type !== 'string' && !(isResearcher && param.key === 'domain') ? ' Leave blank for the default.' : '' }}</span>
                </template>
              </div>
            </section>

            <section v-if="digestParameter" class="agents-create-section" aria-labelledby="agent-create-digest-heading">
              <h2 id="agent-create-digest-heading">Digest delivery</h2>
              <AgentDigestSchedule :model-value="parameters[digestParameter.key] || ''" :key="template"
                v-model:time-missing="digestTimeMissing"
                @update:model-value="parameters[digestParameter.key] = $event"
                :default-value="digestParameter.default || ''" :disabled="busy" :error="errors[`param-${digestParameter.key}`]" />
              <label v-if="channelSlot">
                <span id="agent-create-channel-label">Primary channel{{ channelSlot?.required ? ' *' : ' (optional)' }}</span>
                <FormSelect id="agent-create-channel" v-model="channel" :options="channelOptions" :disabled="busy" :required="!!channelSlot?.required" :invalid="Boolean(errors.channel)" labelledby="agent-create-channel-label" :describedby="['agent-create-channel-hint', errors.channel ? 'agent-create-channel-error' : ''].filter(Boolean).join(' ')" />
                <span v-if="errors.channel" id="agent-create-channel-error" class="agents-fielderr" role="alert">{{ errors.channel }}</span>
                <span id="agent-create-channel-hint" class="agents-hint">{{ channelSlot?.required && !hasOption(channelOptions, channel) ? 'Choose a channel for the digest.' : channelSlot?.description || 'Where this agent messages you.' }}</span>
              </label>
              <div v-if="channelSlot?.required && !channels.length" class="agents-form-actions">
                <a class="k-dashboard-action agents-setup-link" :href="connectionSetupHref()" target="_blank" rel="noopener noreferrer"
                  aria-label="Add a channel (opens in a new tab)" @click="pendingRefresh.add('connections')" @auxclick.middle="pendingRefresh.add('connections')">
                  Add a channel <ExternalLink aria-hidden="true" />
                </a>
                <button type="button" class="k-btn k-btn--ghost" :disabled="busy || connectionSlice.loading" @click="store.load('connections')">Check again</button>
              </div>
            </section>

            <section class="agents-create-section" aria-labelledby="agent-create-execution-heading">
              <h2 id="agent-create-execution-heading">{{ selectedTemplate ? (isHarness ? 'Required connections' : 'Connections') : 'Execution' }}</h2>
              <div v-if="!selectedTemplate" class="agents-create-backends" role="group" aria-label="Execution type">
                <button class="k-btn k-btn--ghost" type="button" :aria-pressed="!isHarness" :disabled="busy" @click="chooseBackend(AGENT_BACKEND_MODEL)">Chat model</button>
                <button class="k-btn k-btn--ghost" type="button" data-use-coding-harness :aria-pressed="isHarness" :disabled="busy" @click="chooseBackend(AGENT_BACKEND_HARNESS)">Coding harness</button>
              </div>
              <template v-if="isHarness">
                <div v-if="edgeSlice.error && !edgeSlice.hasSnapshot" class="k-inline-notification k-inline-notification--error" role="alert">
                  <span class="k-inline-notification__body"><span class="k-inline-notification__message">Could not load machines. {{ edgeSlice.error }}</span></span>
                  <button class="k-inline-notification__action" type="button" :disabled="edgeSlice.loading" @click="store.load('edges')">Retry</button>
                </div>
                <div v-else-if="!edgeSlice.hasSnapshot" class="agents-state agents-state-loading k-loading-reveal" role="status">Loading machines…</div>
                <template v-else>
                  <div v-if="edgeSlice.error" id="agent-create-edge-stale" class="k-inline-notification k-inline-notification--warning" role="status">
                    <span class="k-inline-notification__body"><span class="k-inline-notification__message">Showing the last loaded machines. {{ edgeSlice.error }}</span></span>
                    <button class="k-inline-notification__action" type="button" :disabled="edgeSlice.loading" @click="store.load('edges')">{{ edgeSlice.loading ? 'Retrying…' : 'Retry' }}</button>
                  </div>
                  <div :class="selectedTemplate && 'agents-grid2'">
                    <div class="agents-template-parameter">
                      <label>
                        <span id="agent-create-edge-label">Machine *</span>
                        <FormSelect
                          v-if="!isCodeReviewer || hostEdges.length"
                          id="agent-create-edge"
                          v-model="harnessEdge"
                          name="harnessEdge"
                          :options="edgeOptions"
                          :disabled="busy"
                          :required="true"
                          :invalid="Boolean(errors.harnessEdge)"
                          labelledby="agent-create-edge-label"
                          :describedby="[!isCodeReviewer || machineOffline ? 'agent-create-edge-hint' : '', !isCodeReviewer && hostEdges.length === 0 ? 'agent-create-edge-empty' : '', errors.harnessEdge ? 'agent-create-edge-error' : '', edgeSlice.error ? 'agent-create-edge-stale' : ''].filter(Boolean).join(' ')"
                        />
                        <span v-if="errors.harnessEdge" id="agent-create-edge-error" class="agents-fielderr" role="alert">{{ errors.harnessEdge }}</span>
                        <span v-if="!isCodeReviewer || machineOffline" id="agent-create-edge-hint" class="agents-hint" :class="{ 'agents-warn-inline': machineOffline }">{{ machineOffline ? 'This machine is offline. Chat turns need it to reconnect.' : 'Runs on a Linux or macOS machine.' }}</span>
                        <span v-if="!isCodeReviewer && hostEdges.length === 0" id="agent-create-edge-empty" class="agents-hint">
                          Connect a Linux or macOS machine in Edges, then check again here. Keep this tab open to retain your agent draft.
                        </span>
                      </label>
                      <div v-if="hostEdges.length === 0" class="agents-form-actions">
                        <a v-if="isCodeReviewer" :href="connectMachineHref" @click="openReviewerSetup('edges')" @auxclick.middle="openReviewerSetup('edges')" target="_blank" rel="noopener noreferrer"
                          class="k-dashboard-action agents-setup-link" aria-label="Connect machine (opens in a new tab)">
                          Connect machine <ExternalLink aria-hidden="true" />
                        </a>
                        <template v-else>
                          <a :href="connectMachineHref" @click="pendingRefresh.add('edges')" @auxclick.middle="pendingRefresh.add('edges')" target="_blank" rel="noopener noreferrer"
                            class="k-dashboard-action agents-setup-link" aria-label="Connect a machine (opens in a new tab)">
                            Connect a machine <ExternalLink aria-hidden="true" />
                          </a>
                          <button type="button" class="k-btn k-btn--ghost" :disabled="busy || edgeSlice.loading" @click="store.load('edges')">{{ edgeSlice.loading ? 'Checking machines…' : 'Check again' }}</button>
                        </template>
                      </div>
                    </div>
                    <div v-if="isCodeReviewer && !harnessCredentials.length" class="agents-template-parameter">
                      <span>Harness credential *</span>
                      <div class="agents-form-actions">
                        <a class="k-dashboard-action agents-setup-link" :href="harnessSetupHref" target="_blank" rel="noopener noreferrer"
                          aria-label="Add harness credential (opens in a new tab)" @click="openReviewerSetup('credentials')" @auxclick.middle="openReviewerSetup('credentials')">
                          Add harness credential <ExternalLink aria-hidden="true" />
                        </a>
                      </div>
                    </div>
                    <label v-else>
                      <span id="agent-create-harnesscred-label">Harness credential *</span>
                      <FormSelect
                        id="agent-create-harnesscred"
                        v-model="harnessCredential"
                        name="harnessCredential"
                        :options="harnessCredentialOptions"
                        :disabled="busy"
                        :required="true"
                        :invalid="Boolean(errors.harnessCredential)"
                        labelledby="agent-create-harnesscred-label"
                        :describedby="[!isCodeReviewer ? 'agent-create-harnesscred-hint' : '', harnessCredentials.length === 0 ? 'agent-create-harnesscred-empty' : '', errors.harnessCredential ? 'agent-create-harnesscred-error' : ''].filter(Boolean).join(' ')"
                      />
                      <span v-if="errors.harnessCredential" id="agent-create-harnesscred-error" class="agents-fielderr" role="alert">{{ errors.harnessCredential }}</span>
                      <span v-if="!isCodeReviewer" id="agent-create-harnesscred-hint" class="agents-hint">{{ selectedHarness ? `Runs ${selectedHarness}.` : 'Choose a Claude Code or Codex credential.' }}</span>
                      <span v-if="harnessCredentials.length === 0" id="agent-create-harnesscred-empty" class="agents-hint">
                        No harness identities yet —
                        <button type="button" class="k-dashboard-action" :disabled="busy" @click="addCredential('harness')">
                          add a Claude Code or Codex one under Models → Provider connections
                        </button>
                      </span>
                    </label>
                  </div>
                  <div v-if="!selectedTemplate" class="agents-template-parameter">
                    <label>
                      <span id="agent-create-harness-model-label">Model</span>
                      <input id="agent-create-harness-model" v-model="harnessModel" class="k-input" placeholder="Harness default" :disabled="busy" aria-labelledby="agent-create-harness-model-label" aria-describedby="agent-create-harness-model-hint" />
                      <span id="agent-create-harness-model-hint" class="agents-hint">Optional — blank leaves the harness’s own default.</span>
                    </label>
                    <label v-if="usesCodex">
                      <span id="agent-create-harness-effort-label">Reasoning effort</span>
                      <input id="agent-create-harness-effort" :disabled="busy" v-model="harnessReasoningEffort" class="k-input" placeholder="Harness default" maxlength="32" aria-labelledby="agent-create-harness-effort-label"  :aria-invalid="!!errors.harnessReasoningEffort || undefined"
                        :aria-describedby="errors.harnessReasoningEffort ? 'agent-create-harness-effort-hint agent-create-harness-effort-error' : 'agent-create-harness-effort-hint'" />
                      <span v-if="errors.harnessReasoningEffort" id="agent-create-harness-effort-error" class="agents-fielderr" role="alert">{{ errors.harnessReasoningEffort }}</span>
                      <span id="agent-create-harness-effort-hint" class="agents-hint">Codex only. Enter an effort supported by the selected model, such as max. Leave blank for the harness default.</span>
                    </label>

                    <label>
                      <span id="agent-create-workspace-label">Working directory</span>
                      <FormSelect v-model="harnessWorkspace" :options="workspaceOptions" :disabled="busy" labelledby="agent-create-workspace-label" />
                    </label>

                  </div>
                </template>
                <template v-if="selectedTemplate">
                  <div class="agents-template-parameter">
                    <span id="agent-create-github-label">GitHub connection{{ githubSlot?.required ? ' *' : '' }}</span>
                    <FormSelect v-if="usableGitHubConnections.length || !githubSlot?.required" id="agent-create-github" name="harnessGitHubConnection"
                      :model-value="githubSelectionUnavailable ? '' : harnessGitHubConnection" @update:model-value="harnessGitHubConnection = $event"
                      :options="githubConnectionOptions" :disabled="busy" :required="!!githubSlot?.required"
                      :invalid="githubSelectionUnavailable || Boolean(errors.harnessGitHubConnection)" labelledby="agent-create-github-label"
                      :describedby="[githubConnectionStatus ? 'agent-create-github-status' : '', !isCodeReviewer ? 'agent-create-github-hint' : ''].filter(Boolean).join(' ')" />
                    <p v-if="!isCodeReviewer" id="agent-create-github-hint" class="agents-hint">{{ githubSlot?.description }}</p>
                    <div v-if="githubSlot?.required && !usableGitHubConnections.length && connectionSlice.hasSnapshot" class="agents-form-actions">
                      <a class="k-dashboard-action agents-setup-link" :href="githubSetupHref" target="_blank" rel="noopener noreferrer"
                        :aria-label="(githubConnections.length ? 'Manage GitHub connections' : 'Add GitHub connection') + ' (opens in a new tab)'"
                        @click="isCodeReviewer ? openReviewerSetup('connections') : pendingRefresh.add('connections')" @auxclick.middle="isCodeReviewer ? openReviewerSetup('connections') : pendingRefresh.add('connections')">
                        {{ githubConnections.length ? 'Manage GitHub connections' : 'Add GitHub connection' }}
                        <ExternalLink aria-hidden="true" />
                      </a>
                      <button v-if="!isCodeReviewer || connectionSlice.error" type="button" class="k-btn k-btn--ghost" :disabled="busy || connectionSlice.loading" @click="store.load('connections')">Check again</button>
                    </div>
                    <button v-if="!connectionSlice.hasSnapshot && connectionSlice.error" class="k-btn k-btn--ghost" type="button"
                      :disabled="busy || connectionSlice.loading" @click="store.load('connections')">Retry GitHub connections</button>
                    <p v-if="githubConnectionStatus" id="agent-create-github-status" role="status"
                      :class="githubSelectionUnavailable ? 'agents-fielderr' : 'agents-hint'">{{ githubConnectionStatus }}</p>
                  </div>
                </template>
                <div v-if="isCodeReviewer && reviewerSetupOpened && !edgeSlice.error && !connectionSlice.error && !credentials.error &&
                  (!hostEdges.length || !harnessCredentials.length || !usableGitHubConnections.length)" class="agents-form-actions">
                  <button type="button" class="k-btn k-btn--text" :disabled="busy || edgeSlice.loading || credentials.loading || connectionSlice.loading"
                    @click="refreshReviewerConnections">Refresh connections</button>
                </div>
              </template>

              <div v-if="!isHarness || connectionSlots.length" :class="{ 'agents-grid2': connectionSlots.length > 0 }">
                <div v-if="!isHarness" class="agents-template-parameter">
                  <label>
                    <span id="agent-create-model-label">Model *</span>
                    <FormSelect
                      id="agent-create-model"
                      v-model="modelCredential"
                      name="modelCredential"
                      :options="credentialOptions"
                      placeholder="— pick a model —"
                      :required="true"
                      :disabled="busy"
                      :invalid="Boolean(errors.modelCredential)"
                      labelledby="agent-create-model-label"
                      :describedby="['agent-create-model-hint', errors.modelCredential ? 'agent-create-model-error' : ''].filter(Boolean).join(' ')"
                    />
                    <span v-if="errors.modelCredential" id="agent-create-model-error" class="agents-fielderr" role="alert">{{ errors.modelCredential }}</span>
                    <span id="agent-create-model-hint" class="agents-hint">{{ selectedTemplate && !hasOption(credentialOptions, modelCredential) ? 'Choose a model to create this agent.' : chatCredentials.some(model => model.name === modelCredential && model.ready === true) ? 'Ready to use' : 'The default model for this agent.' }}</span>
                  </label>
                  <p v-if="credentialOptions.length === 0" id="agent-create-model-empty" class="agents-hint">
                    This workspace needs a chat model.
                    <button type="button" class="k-dashboard-action" :disabled="busy" @click="addCredential('chat')">Connect a model</button>
                  </p>
                </div>
                <label v-for="slot in connectionSlots" :key="slot.key">
                  <span :id="`agent-create-slot-${slot.key}-label`">{{ slot.connectionType === 'websearch' ? 'Web search' : slotLabel(slot) }}{{ slot.required ? ' *' : ' (optional)' }}</span>
                  <FormSelect
                    :id="`agent-create-slot-${slot.key}`"
                    :model-value="slotWiring[slot.key] ?? ''"
                    @update:model-value="value => { slotWiring[slot.key] = value }"
                    :options="connectionOptions(slot.connectionType || '', !!slot.required)"
                    :disabled="busy"
                    :required="!!slot.required"
                    :invalid="Boolean(errors[`slot-${slot.key}`])"
                    :labelledby="`agent-create-slot-${slot.key}-label`"
                    :describedby="[`agent-create-slot-${slot.key}-hint`, errors[`slot-${slot.key}`] ? `agent-create-slot-${slot.key}-error` : ''].filter(Boolean).join(' ')"
                  />
                  <span v-if="errors[`slot-${slot.key}`]" :id="`agent-create-slot-${slot.key}-error`" class="agents-fielderr" role="alert">{{ errors[`slot-${slot.key}`] }}</span>
                  <span :id="`agent-create-slot-${slot.key}-hint`" class="agents-hint">{{ slot.connectionType === 'websearch' && !slotWiring[slot.key] ? 'Without web search, the agent can only read pages you link it to.' : slot.description }}</span>
                </label>
              </div>
              <label v-if="channelSlot && !digestParameter">
                <span id="agent-create-channel-label">Primary channel{{ channelSlot?.required ? ' *' : ' (optional)' }}</span>
                <FormSelect id="agent-create-channel" v-model="channel" :options="channelOptions" :disabled="busy" :required="!!channelSlot?.required" :invalid="Boolean(errors.channel)" labelledby="agent-create-channel-label" :describedby="['agent-create-channel-hint', errors.channel ? 'agent-create-channel-error' : ''].filter(Boolean).join(' ')" />
                <span v-if="errors.channel" id="agent-create-channel-error" class="agents-fielderr" role="alert">{{ errors.channel }}</span>
                <span id="agent-create-channel-hint" class="agents-hint">{{ channelSlot?.description || 'Optional — where this agent messages you.' }}</span>
              </label>
              <div v-if="!digestParameter && channelSlot?.required && !channels.length" class="agents-form-actions">
                <a class="k-dashboard-action agents-setup-link" :href="connectionSetupHref()" target="_blank" rel="noopener noreferrer"
                  aria-label="Add a channel (opens in a new tab)" @click="pendingRefresh.add('connections')" @auxclick.middle="pendingRefresh.add('connections')">
                  Add a channel <ExternalLink aria-hidden="true" />
                </a>
                <button type="button" class="k-btn k-btn--ghost" :disabled="busy || connectionSlice.loading" @click="store.load('connections')">Check again</button>
              </div>
            </section>

            <section v-if="!selectedTemplate" class="agents-create-section" aria-labelledby="agent-create-inputs-heading">
              <h2 id="agent-create-inputs-heading">Template inputs</h2>
              <p v-if="!templateParameters.length" class="agents-hint">No template inputs to fill in.</p>
            </section>
            <section v-if="!selectedTemplate" class="agents-create-section" aria-labelledby="agent-create-connections-heading">
              <h2 id="agent-create-connections-heading">Connections & automation</h2>
              <p v-if="!isHarness && !channelSlot && !connectionSlots.length" class="agents-hint">Add connections or automation after creating the agent.</p>
              <label v-if="isHarness">
                <span id="agent-create-github-label">GitHub connection{{ githubSlot?.required ? ' *' : '' }}</span>
                <FormSelect id="agent-create-github" v-model="harnessGitHubConnection" name="harnessGitHubConnection" :options="githubConnectionOptions" :disabled="busy" :required="!!githubSlot?.required" :invalid="Boolean(errors.harnessGitHubConnection)" labelledby="agent-create-github-label" :describedby="['agent-create-github-hint', errors.harnessGitHubConnection ? 'agent-create-github-error' : ''].filter(Boolean).join(' ')" />
                <span v-if="errors.harnessGitHubConnection" id="agent-create-github-error" class="agents-fielderr" role="alert">{{ errors.harnessGitHubConnection }}</span>
                <span id="agent-create-github-hint" class="agents-hint">{{ githubSlot?.description || 'Optional — its token reaches the harness as GH_TOKEN for each turn, never the machine, so `gh` can read and review pull requests.' }}</span>
              </label>
              <p v-if="isHarness" class="agents-hint">A coding harness uses its own tools, configured on the selected machine.</p>
            </section>
            <details v-if="selectedTemplate" ref="settings" :key="template" class="agents-template-settings">
              <summary><ChevronRight aria-hidden="true" /> Instructions and settings</summary>
              <div class="agents-template-parameter">
                <label>
                  <span id="agent-create-system-prompt-label">Instructions (optional)</span>
                  <textarea :value="promptSource" class="k-input" rows="6" :disabled="busy" aria-labelledby="agent-create-system-prompt-label"
                    aria-describedby="agent-create-system-prompt-hint" @input="editInstructions" />
                  <span id="agent-create-system-prompt-hint" class="agents-hint">
                    {{ templateParameters.length ? 'Keep placeholders to insert the values from this form.' : 'Prefilled from the template. You can edit these instructions.' }}
                  </span>
                </label>
                <details class="agents-template-settings">
                  <summary><ChevronRight aria-hidden="true" /> View generated instructions</summary>
                  <pre class="agents-template-prompt" data-generated-instructions>{{ instructions }}</pre>
                </details>
                <p class="agents-hint">{{ isHarness ? 'Execution uses a coding harness.' : 'Execution uses a chat model.' }}</p>
                <template v-if="isHarness">
                  <label>
                    <span id="agent-create-harness-model-label">Model</span>
                    <input id="agent-create-harness-model" v-model="harnessModel" class="k-input" placeholder="Harness default" :disabled="busy" aria-labelledby="agent-create-harness-model-label" aria-describedby="agent-create-harness-model-hint" />
                    <span id="agent-create-harness-model-hint" class="agents-hint">Optional — blank leaves the harness’s own default.</span>
                  </label>
                  <label v-if="usesCodex">
                    <span id="agent-create-harness-effort-label">Reasoning effort</span>
                    <input id="agent-create-harness-effort" :disabled="busy" v-model="harnessReasoningEffort" class="k-input" placeholder="Harness default" maxlength="32" aria-labelledby="agent-create-harness-effort-label"  :aria-invalid="!!errors.harnessReasoningEffort || undefined"
                        :aria-describedby="errors.harnessReasoningEffort ? 'agent-create-harness-effort-hint agent-create-harness-effort-error' : 'agent-create-harness-effort-hint'" />
                      <span v-if="errors.harnessReasoningEffort" id="agent-create-harness-effort-error" class="agents-fielderr" role="alert">{{ errors.harnessReasoningEffort }}</span>
                    <span id="agent-create-harness-effort-hint" class="agents-hint">Codex only. Enter an effort supported by the selected model, such as max. Leave blank for the harness default.</span>
                  </label>

                  <label>
                    <span id="agent-create-workspace-label">Working directory</span>
                    <FormSelect v-model="harnessWorkspace" :options="workspaceOptions" :disabled="busy" labelledby="agent-create-workspace-label" />
                  </label>

                </template>
              </div>
            </details>
          </div>

          <CreateGuidance :title="guidance.title" :description="guidance.description" />
        </div>

        <div v-if="errors.template" class="k-inline-notification k-inline-notification--error" role="alert">
          <span class="k-inline-notification__body"><span class="k-inline-notification__message">{{ errors.template }}</span></span>
        </div>
        <div class="k-create-actions">
          <button type="button" class="k-btn k-btn--ghost" :disabled="busy" @click="cancel">Cancel</button>
          <button class="k-btn k-btn--primary" type="submit" :disabled="busy || !canSubmit">
            <Check aria-hidden="true" /> {{ busy ? 'Creating…' : requiresReview ? 'Review & create' : 'Create & open chat' }}
          </button>
        </div>
      </form>
    </template>
  </div>
</template>
